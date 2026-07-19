package lsp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	EnvelopeSchemaVersion = "sandbox-lsp-envelope/v1"

	ClientEnvelopeDocumentOpen   = "client.document.open"
	ClientEnvelopeDocumentChange = "client.document.change"
	ClientEnvelopeDocumentSave   = "client.document.save"
	ClientEnvelopeDocumentClose  = "client.document.close"
	ClientEnvelopeRequest        = "client.request"
	ClientEnvelopeCancel         = "client.cancel"
	ClientEnvelopeHeadRebind     = "client.headRebind"
	ClientEnvelopePing           = "client.ping"

	ServerEnvelopeResponse    = "server.response"
	ServerEnvelopeDiagnostics = "server.diagnostics"
	ServerEnvelopeStale       = "server.stale"
	ServerEnvelopeError       = "server.error"
	ServerEnvelopePong        = "server.pong"

	EnvelopeMethodDocumentOpen   = "textDocument/didOpen"
	EnvelopeMethodDocumentChange = "textDocument/didChange"
	EnvelopeMethodDocumentSave   = "textDocument/didSave"
	EnvelopeMethodDocumentClose  = "textDocument/didClose"
	EnvelopeMethodCancel         = "$/cancelRequest"
	EnvelopeMethodHeadRebind     = "worksflow/headRebind"
	EnvelopeMethodPing           = "worksflow/ping"
	EnvelopeMethodPong           = "worksflow/pong"
	EnvelopeMethodError          = "worksflow/error"
	EnvelopeMethodDiagnostics    = "textDocument/publishDiagnostics"

	maximumEnvelopeDepth      = 16
	maximumEnvelopeCode       = 128
	maximumEnvelopeText       = 4 << 10
	maximumEnvelopeMessageIDs = 65_536
)

var (
	ErrEnvelopeMalformed      = errors.New("malformed LSP envelope")
	ErrEnvelopeSequence       = errors.New("non-monotonic LSP envelope sequence")
	ErrEnvelopeConnection     = errors.New("LSP envelope belongs to another connection or binding")
	ErrEnvelopeHeadStale      = errors.New("LSP envelope SandboxHeadFence is stale")
	ErrEnvelopeDocumentStale  = errors.New("LSP envelope DocumentFence is stale")
	ErrEnvelopeDocumentLimit  = errors.New("LSP envelope document synchronization limit exceeded")
	ErrEnvelopeRequestUnknown = errors.New("LSP envelope request is unknown, reused, or not pending")
)

// ClientEnvelope is the fully admitted browser-to-Gateway message. Payload is
// always one of the concrete payload DTOs below; raw browser JSON is never
// retained after admission.
type ClientEnvelope struct {
	SchemaVersion string
	ConnectionID  string
	BindingID     string
	Sequence      uint64
	MessageID     string
	ReplyTo       *string
	Kind          string
	Method        string
	Head          SandboxHeadFence
	Document      *DocumentFence
	Payload       any
}

type DocumentOpenEnvelopePayload struct {
	LanguageID string `json:"languageId"`
	Text       string `json:"text"`
}

type DocumentChangeEnvelopePayload struct {
	Text string `json:"text"`
}

type EmptyEnvelopePayload struct{}

type RequestEnvelopePayload struct {
	Params BrowserRequestPayload
}

type CancelEnvelopePayload struct {
	ReplyTo string `json:"-"`
}

type HeadRebindEnvelopePayload struct {
	Documents []DocumentFence `json:"documents"`
}

type PingEnvelopePayload struct {
	Nonce string `json:"nonce"`
}

type envelopeDocumentState struct {
	fence      DocumentFence
	languageID string
	text       string
	synced     bool
}

type envelopePendingRequest struct {
	id       string
	method   string
	head     SandboxHeadFence
	document DocumentFence
}

type envelopePendingPing struct {
	head  SandboxHeadFence
	nonce string
}

// EnvelopeProtocol is connection-local mutable protocol state. Client and
// server sequences are independent. All state transitions are applied only
// after a complete strict decode, except response/stale completion, which is
// intentionally single-use even if a later payload check fails.
type EnvelopeProtocol struct {
	mu sync.Mutex

	connectionID       string
	bindingID          string
	head               SandboxHeadFence
	profile            ProfileIdentity
	documents          map[string]envelopeDocumentState
	pending            map[string]envelopePendingRequest
	stalePending       map[string]envelopePendingRequest
	pendingPings       map[string]envelopePendingPing
	seenClientMessages map[string]struct{}
	seenServerMessages map[string]struct{}
	idSource           func() string
	totalBytes         int64
	clientSeq          uint64
	serverSeq          uint64
}

func NewEnvelopeProtocol(
	connectionID string,
	bindingID string,
	head SandboxHeadFence,
	profile ProfileIdentity,
	documents []DocumentFence,
) (*EnvelopeProtocol, error) {
	return newEnvelopeProtocol(connectionID, bindingID, head, profile, documents, uuid.NewString)
}

func newEnvelopeProtocol(
	connectionID string,
	bindingID string,
	head SandboxHeadFence,
	profile ProfileIdentity,
	documents []DocumentFence,
	idSource func() string,
) (*EnvelopeProtocol, error) {
	if !canonicalUUID(connectionID) || !canonicalUUID(bindingID) || connectionID == bindingID ||
		head.Validate() != nil || profile.Validate() != nil ||
		len(documents) == 0 || len(documents) > profile.EffectiveLimits.MaxOpenDocuments ||
		idSource == nil {
		return nil, ErrEnvelopeMalformed
	}
	states := make(map[string]envelopeDocumentState, len(documents))
	for index, document := range documents {
		if document.ValidateAgainstHead(head) != nil ||
			!documentAdmittedByProfile(document, profile) ||
			(index > 0 && documents[index-1].ModelURI >= document.ModelURI) {
			return nil, ErrEnvelopeDocumentStale
		}
		states[document.ModelURI] = envelopeDocumentState{fence: document}
	}
	return &EnvelopeProtocol{
		connectionID:       connectionID,
		bindingID:          bindingID,
		head:               head,
		profile:            cloneProfiles([]ProfileIdentity{profile})[0],
		documents:          states,
		pending:            make(map[string]envelopePendingRequest),
		stalePending:       make(map[string]envelopePendingRequest),
		pendingPings:       make(map[string]envelopePendingPing),
		seenClientMessages: make(map[string]struct{}),
		seenServerMessages: make(map[string]struct{}),
		idSource:           idSource,
		clientSeq:          1, // client.bind is sequence 1
		serverSeq:          1, // server.bound is binding sequence 1
	}, nil
}

// DecodeClientEnvelope performs strict, stateful browser admission. A valid
// frame advances the client sequence exactly once. Invalid frames do not
// acquire a sequence number or partially mutate document state.
func (protocol *EnvelopeProtocol) DecodeClientEnvelope(frame []byte) (ClientEnvelope, error) {
	if protocol == nil {
		return ClientEnvelope{}, ErrEnvelopeMalformed
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()

	_, envelope, rawDocument, rawPayload, err := protocol.decodeHeader(frame, protocol.clientSeq)
	if err != nil {
		return ClientEnvelope{}, err
	}

	switch envelope.Kind {
	case ClientEnvelopeDocumentOpen:
		document, err := protocol.requireDocument(rawDocument, envelope.Head)
		if err != nil {
			return ClientEnvelope{}, err
		}
		payload, err := protocol.decodeDocumentOpen(rawPayload, document)
		if err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Document, envelope.Payload = &document, payload
	case ClientEnvelopeDocumentChange:
		document, err := protocol.requireDocument(rawDocument, envelope.Head)
		if err != nil {
			return ClientEnvelope{}, err
		}
		payload, err := protocol.decodeDocumentChange(rawPayload, document)
		if err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Document, envelope.Payload = &document, payload
	case ClientEnvelopeDocumentSave:
		document, err := protocol.requireDocument(rawDocument, envelope.Head)
		if err != nil {
			return ClientEnvelope{}, err
		}
		if err := decodeEmptyEnvelopePayload(rawPayload); err != nil {
			return ClientEnvelope{}, err
		}
		if err := protocol.applyDocumentSave(document); err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Document, envelope.Payload = &document, EmptyEnvelopePayload{}
	case ClientEnvelopeDocumentClose:
		document, err := protocol.requireDocument(rawDocument, envelope.Head)
		if err != nil {
			return ClientEnvelope{}, err
		}
		if err := decodeEmptyEnvelopePayload(rawPayload); err != nil {
			return ClientEnvelope{}, err
		}
		if err := protocol.applyDocumentClose(document); err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Document, envelope.Payload = &document, EmptyEnvelopePayload{}
	case ClientEnvelopeRequest:
		document, err := protocol.requireDocument(rawDocument, envelope.Head)
		if err != nil {
			return ClientEnvelope{}, err
		}
		payload, err := protocol.decodeRequest(
			rawPayload, document, envelope.MessageID, envelope.Method,
		)
		if err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Document, envelope.Payload = &document, payload
	case ClientEnvelopeCancel:
		document, err := protocol.requireDocument(rawDocument, envelope.Head)
		if err != nil {
			return ClientEnvelope{}, err
		}
		payload, err := protocol.decodeCancel(
			rawPayload, document, dereferenceEnvelopeReplyTo(envelope.ReplyTo),
		)
		if err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Document, envelope.Payload = &document, payload
	case ClientEnvelopeHeadRebind:
		if !isEnvelopeJSONNull(rawDocument) {
			return ClientEnvelope{}, ErrEnvelopeMalformed
		}
		payload, err := protocol.decodeHeadRebind(rawPayload, envelope.Head)
		if err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Payload = payload
	case ClientEnvelopePing:
		if !envelope.Head.Equal(protocol.head) || !isEnvelopeJSONNull(rawDocument) {
			return ClientEnvelope{}, ErrEnvelopeHeadStale
		}
		payload, err := decodeNoncePayload(rawPayload)
		if err != nil {
			return ClientEnvelope{}, err
		}
		envelope.Payload = payload
		protocol.pendingPings[envelope.MessageID] = envelopePendingPing{
			head: envelope.Head, nonce: payload.Nonce,
		}
	default:
		return ClientEnvelope{}, ErrEnvelopeMalformed
	}
	protocol.seenClientMessages[envelope.MessageID] = struct{}{}
	protocol.clientSeq = envelope.Sequence
	return envelope, nil
}

func (protocol *EnvelopeProtocol) decodeHeader(
	frame []byte,
	previousSequence uint64,
) (map[string]json.RawMessage, ClientEnvelope, json.RawMessage, json.RawMessage, error) {
	if int64(len(frame)) <= 0 || int64(len(frame)) > protocol.profile.EffectiveLimits.MaxFrameBytes ||
		!utf8.Valid(frame) || validateStrictJSONDocument(frame, maximumEnvelopeDepth) != nil {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeMalformed
	}
	fields, err := decodeExactObject(frame, []string{
		"schemaVersion", "connectionId", "bindingId", "sequence", "messageId",
		"replyTo", "kind", "method", "sandboxHeadFence", "documentFence", "payload",
	})
	if err != nil {
		return nil, ClientEnvelope{}, nil, nil, fmt.Errorf("%w: %v", ErrEnvelopeMalformed, err)
	}
	var envelope ClientEnvelope
	if decodeString(fields["schemaVersion"], &envelope.SchemaVersion) != nil ||
		envelope.SchemaVersion != EnvelopeSchemaVersion ||
		decodeString(fields["kind"], &envelope.Kind) != nil ||
		decodeString(fields["connectionId"], &envelope.ConnectionID) != nil ||
		decodeString(fields["bindingId"], &envelope.BindingID) != nil ||
		decodeString(fields["messageId"], &envelope.MessageID) != nil ||
		decodeString(fields["method"], &envelope.Method) != nil ||
		!canonicalUUID(envelope.ConnectionID) || !canonicalUUID(envelope.BindingID) ||
		!canonicalUUID(envelope.MessageID) {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeMalformed
	}
	if envelope.ConnectionID != protocol.connectionID || envelope.BindingID != protocol.bindingID {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeConnection
	}
	if decodeMethodUint(fields["sequence"], &envelope.Sequence) != nil ||
		previousSequence >= maxSafeWireInteger || envelope.Sequence != previousSequence+1 {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeSequence
	}
	envelope.ReplyTo, err = decodeEnvelopeReplyTo(fields["replyTo"])
	if err != nil || protocol.validateClientHeader(envelope) != nil {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeMalformed
	}
	if envelope.MessageID == protocol.connectionID || envelope.MessageID == protocol.bindingID {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeMalformed
	}
	if _, exists := protocol.seenClientMessages[envelope.MessageID]; exists {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeRequestUnknown
	}
	if _, exists := protocol.seenServerMessages[envelope.MessageID]; exists {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeRequestUnknown
	}
	if len(protocol.seenClientMessages) >= maximumEnvelopeMessageIDs {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeRequestUnknown
	}
	envelope.Head, err = DecodeSandboxHeadFence(fields["sandboxHeadFence"])
	if err != nil {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeHeadStale
	}
	if envelope.Kind == ClientEnvelopeHeadRebind {
		if envelope.Head.MonotonicSuccessorOf(protocol.head) != nil {
			return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeHeadStale
		}
	} else if !envelope.Head.Equal(protocol.head) {
		return nil, ClientEnvelope{}, nil, nil, ErrEnvelopeHeadStale
	}
	return fields, envelope, fields["documentFence"], fields["payload"], nil
}

func (protocol *EnvelopeProtocol) validateClientHeader(envelope ClientEnvelope) error {
	if envelope.ReplyTo != nil && *envelope.ReplyTo == envelope.MessageID {
		return ErrEnvelopeMalformed
	}
	expectedMethod := ""
	switch envelope.Kind {
	case ClientEnvelopeDocumentOpen:
		expectedMethod = EnvelopeMethodDocumentOpen
	case ClientEnvelopeDocumentChange:
		expectedMethod = EnvelopeMethodDocumentChange
	case ClientEnvelopeDocumentSave:
		expectedMethod = EnvelopeMethodDocumentSave
	case ClientEnvelopeDocumentClose:
		expectedMethod = EnvelopeMethodDocumentClose
	case ClientEnvelopeRequest:
		if envelope.ReplyTo != nil || !validEnvelopeAtom(envelope.Method, 256) {
			return ErrEnvelopeMalformed
		}
		return nil
	case ClientEnvelopeCancel:
		expectedMethod = EnvelopeMethodCancel
		if envelope.ReplyTo == nil {
			return ErrEnvelopeMalformed
		}
	case ClientEnvelopeHeadRebind:
		expectedMethod = EnvelopeMethodHeadRebind
	case ClientEnvelopePing:
		expectedMethod = EnvelopeMethodPing
	default:
		return ErrEnvelopeMalformed
	}
	if envelope.Kind != ClientEnvelopeCancel && envelope.ReplyTo != nil {
		return ErrEnvelopeMalformed
	}
	if envelope.Method != expectedMethod {
		return ErrEnvelopeMalformed
	}
	return nil
}

func (protocol *EnvelopeProtocol) requireDocument(
	raw json.RawMessage,
	head SandboxHeadFence,
) (DocumentFence, error) {
	if isEnvelopeJSONNull(raw) {
		return DocumentFence{}, ErrEnvelopeDocumentStale
	}
	document, err := DecodeDocumentFence(raw)
	if err != nil || document.ValidateAgainstHead(head) != nil ||
		!documentAdmittedByProfile(document, protocol.profile) {
		return DocumentFence{}, ErrEnvelopeDocumentStale
	}
	return document, nil
}

func (protocol *EnvelopeProtocol) decodeDocumentOpen(
	raw json.RawMessage,
	document DocumentFence,
) (DocumentOpenEnvelopePayload, error) {
	fields, err := decodeEnvelopePayload(raw, []string{"languageId", "text"})
	if err != nil {
		return DocumentOpenEnvelopePayload{}, err
	}
	var payload DocumentOpenEnvelopePayload
	if decodeString(fields["languageId"], &payload.LanguageID) != nil ||
		decodeEnvelopeText(fields["text"], &payload.Text) != nil ||
		!slices.Contains(protocol.profile.LanguageIDs, payload.LanguageID) {
		return DocumentOpenEnvelopePayload{}, ErrEnvelopeMalformed
	}
	if existing, exists := protocol.documents[document.ModelURI]; exists {
		if existing.synced || !existing.fence.Equal(document) {
			return DocumentOpenEnvelopePayload{}, ErrEnvelopeDocumentStale
		}
	}
	if len(protocol.documents) > protocol.profile.EffectiveLimits.MaxOpenDocuments ||
		len(protocol.documents) == protocol.profile.EffectiveLimits.MaxOpenDocuments &&
			protocol.documents[document.ModelURI].fence.ModelURI == "" {
		return DocumentOpenEnvelopePayload{}, ErrEnvelopeDocumentLimit
	}
	if !contentDigest(payload.Text, document.SavedContentHash) {
		return DocumentOpenEnvelopePayload{}, ErrEnvelopeDocumentStale
	}
	if int64(len(payload.Text)) > protocol.profile.EffectiveLimits.MaxDocumentBytes ||
		protocol.totalBytes+int64(len(payload.Text)) > protocol.profile.EffectiveLimits.MaxTotalSyncBytes {
		return DocumentOpenEnvelopePayload{}, ErrEnvelopeDocumentLimit
	}
	protocol.documents[document.ModelURI] = envelopeDocumentState{
		fence: document, languageID: payload.LanguageID, text: payload.Text, synced: true,
	}
	protocol.totalBytes += int64(len(payload.Text))
	return payload, nil
}

func (protocol *EnvelopeProtocol) decodeDocumentChange(
	raw json.RawMessage,
	document DocumentFence,
) (DocumentChangeEnvelopePayload, error) {
	fields, err := decodeEnvelopePayload(raw, []string{"text"})
	if err != nil {
		return DocumentChangeEnvelopePayload{}, err
	}
	var payload DocumentChangeEnvelopePayload
	if decodeEnvelopeText(fields["text"], &payload.Text) != nil {
		return DocumentChangeEnvelopePayload{}, ErrEnvelopeMalformed
	}
	current, exists := protocol.documents[document.ModelURI]
	if !exists || !current.synced || document.OpenID != current.fence.OpenID ||
		document.SavedContentHash != current.fence.SavedContentHash ||
		current.fence.ModelVersion >= maxSafeWireInteger ||
		document.ModelVersion != current.fence.ModelVersion+1 {
		return DocumentChangeEnvelopePayload{}, ErrEnvelopeDocumentStale
	}
	nextTotal := protocol.totalBytes - int64(len(current.text)) + int64(len(payload.Text))
	if int64(len(payload.Text)) > protocol.profile.EffectiveLimits.MaxDocumentBytes ||
		nextTotal > protocol.profile.EffectiveLimits.MaxTotalSyncBytes {
		return DocumentChangeEnvelopePayload{}, ErrEnvelopeDocumentLimit
	}
	current.fence = document
	current.text = payload.Text
	protocol.documents[document.ModelURI] = current
	protocol.totalBytes = nextTotal
	return payload, nil
}

func (protocol *EnvelopeProtocol) applyDocumentSave(document DocumentFence) error {
	current, exists := protocol.documents[document.ModelURI]
	if !exists || !current.synced || document.OpenID != current.fence.OpenID ||
		document.ModelVersion != current.fence.ModelVersion ||
		document.SavedContentHash == current.fence.SavedContentHash ||
		!contentDigest(current.text, document.SavedContentHash) {
		return ErrEnvelopeDocumentStale
	}
	current.fence = document
	protocol.documents[document.ModelURI] = current
	return nil
}

func (protocol *EnvelopeProtocol) applyDocumentClose(document DocumentFence) error {
	current, exists := protocol.documents[document.ModelURI]
	if !exists || !current.synced || !current.fence.Equal(document) {
		return ErrEnvelopeDocumentStale
	}
	for _, request := range protocol.pending {
		if request.document.ModelURI == document.ModelURI {
			return ErrEnvelopeRequestUnknown
		}
	}
	protocol.totalBytes -= int64(len(current.text))
	delete(protocol.documents, document.ModelURI)
	return nil
}

func (protocol *EnvelopeProtocol) decodeRequest(
	raw json.RawMessage,
	document DocumentFence,
	messageID string,
	method string,
) (RequestEnvelopePayload, error) {
	fields, err := decodeEnvelopePayload(raw, []string{"params"})
	if err != nil {
		return RequestEnvelopePayload{}, err
	}
	if !canonicalUUID(messageID) {
		return RequestEnvelopePayload{}, ErrEnvelopeMalformed
	}
	current, exists := protocol.documents[document.ModelURI]
	if !exists || !current.synced || !current.fence.Equal(document) {
		return RequestEnvelopePayload{}, ErrEnvelopeDocumentStale
	}
	if len(protocol.pending) >= protocol.profile.EffectiveLimits.MaxConcurrentRequests {
		return RequestEnvelopePayload{}, ErrEnvelopeRequestUnknown
	}
	params, err := DecodeBrowserRequestPayload(
		method, protocol.profile.Methods, fields["params"], protocol.head, document,
	)
	if err != nil {
		return RequestEnvelopePayload{}, fmt.Errorf("%w: %v", ErrEnvelopeMalformed, err)
	}
	protocol.pending[messageID] = envelopePendingRequest{
		id: messageID, method: method, head: protocol.head, document: document,
	}
	return RequestEnvelopePayload{Params: params}, nil
}

func (protocol *EnvelopeProtocol) decodeCancel(
	raw json.RawMessage,
	document DocumentFence,
	replyTo string,
) (CancelEnvelopePayload, error) {
	if err := decodeEmptyEnvelopePayload(raw); err != nil {
		return CancelEnvelopePayload{}, err
	}
	if !canonicalUUID(replyTo) {
		return CancelEnvelopePayload{}, ErrEnvelopeMalformed
	}
	request, exists := protocol.pending[replyTo]
	if !exists || !request.head.Equal(protocol.head) || !request.document.Equal(document) {
		return CancelEnvelopePayload{}, ErrEnvelopeRequestUnknown
	}
	delete(protocol.pending, replyTo)
	return CancelEnvelopePayload{ReplyTo: replyTo}, nil
}

func (protocol *EnvelopeProtocol) decodeHeadRebind(
	raw json.RawMessage,
	next SandboxHeadFence,
) (HeadRebindEnvelopePayload, error) {
	payload, err := protocol.validateHeadRebind(raw, next)
	if err != nil {
		return HeadRebindEnvelopePayload{}, err
	}
	for _, document := range payload.Documents {
		current := protocol.documents[document.ModelURI]
		current.fence = document
		protocol.documents[document.ModelURI] = current
	}
	protocol.head = next
	for requestID, request := range protocol.pending {
		protocol.stalePending[requestID] = request
		delete(protocol.pending, requestID)
	}
	return payload, nil
}

func (protocol *EnvelopeProtocol) validateHeadRebind(
	raw json.RawMessage,
	next SandboxHeadFence,
) (HeadRebindEnvelopePayload, error) {
	fields, err := decodeEnvelopePayload(raw, []string{"documents"})
	if err != nil {
		return HeadRebindEnvelopePayload{}, err
	}
	documents, err := decodeEnvelopeDocumentArray(
		fields["documents"], next, protocol.profile.EffectiveLimits.MaxOpenDocuments,
	)
	if err != nil || len(documents) != len(protocol.documents) {
		return HeadRebindEnvelopePayload{}, ErrEnvelopeDocumentStale
	}
	for _, document := range documents {
		current, exists := protocol.documents[document.ModelURI]
		if !exists || current.fence.ModelURI != document.ModelURI ||
			current.fence.OpenID != document.OpenID ||
			current.fence.ModelVersion != document.ModelVersion {
			return HeadRebindEnvelopePayload{}, ErrEnvelopeDocumentStale
		}
		if current.fence.SavedContentHash != document.SavedContentHash &&
			(!current.synced || !contentDigest(current.text, document.SavedContentHash)) {
			return HeadRebindEnvelopePayload{}, ErrEnvelopeDocumentStale
		}
	}
	return HeadRebindEnvelopePayload{Documents: slices.Clone(documents)}, nil
}

// previewClientHeadRebind strictly validates a possible atomic CAS rebind
// without acquiring its sequence or mutating protocol state. Gateway uses the
// proposed exact successor/documents for its opening authority check because
// the Repository CAS has already made the previous head non-current.
func (protocol *EnvelopeProtocol) previewClientHeadRebind(
	frame []byte,
) (SandboxHeadFence, []DocumentFence, bool, error) {
	if protocol == nil {
		return SandboxHeadFence{}, nil, false, ErrEnvelopeMalformed
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	_, envelope, rawDocument, rawPayload, err := protocol.decodeHeader(frame, protocol.clientSeq)
	if err != nil {
		return SandboxHeadFence{}, nil, false, err
	}
	if envelope.Kind != ClientEnvelopeHeadRebind {
		return SandboxHeadFence{}, nil, false, nil
	}
	if !isEnvelopeJSONNull(rawDocument) {
		return SandboxHeadFence{}, nil, false, ErrEnvelopeMalformed
	}
	payload, err := protocol.validateHeadRebind(rawPayload, envelope.Head)
	if err != nil {
		return SandboxHeadFence{}, nil, false, err
	}
	return envelope.Head, slices.Clone(payload.Documents), true, nil
}

// CurrentHeadAndDocuments returns a defensive, URI-sorted projection suitable
// for exact authority rechecks and server-message filtering.
func (protocol *EnvelopeProtocol) CurrentHeadAndDocuments() (SandboxHeadFence, []DocumentFence) {
	if protocol == nil {
		return SandboxHeadFence{}, nil
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	return protocol.head, protocol.sortedDocumentsLocked()
}

func (protocol *EnvelopeProtocol) sortedDocumentsLocked() []DocumentFence {
	result := make([]DocumentFence, 0, len(protocol.documents))
	for _, document := range protocol.documents {
		result = append(result, document.fence)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ModelURI < result[j].ModelURI })
	return result
}

// ServerEnvelope is the exact Gateway-to-browser wire object. Payload is
// canonical JSON produced from validated DTOs and never aliases a caller's
// mutable buffer.
type ServerEnvelope struct {
	SchemaVersion string           `json:"schemaVersion"`
	ConnectionID  string           `json:"connectionId"`
	BindingID     string           `json:"bindingId"`
	Sequence      uint64           `json:"sequence"`
	MessageID     string           `json:"messageId"`
	ReplyTo       *string          `json:"replyTo"`
	Kind          string           `json:"kind"`
	Method        string           `json:"method"`
	Head          SandboxHeadFence `json:"sandboxHeadFence"`
	Document      *DocumentFence   `json:"documentFence"`
	Payload       json.RawMessage  `json:"payload"`
}

type serverResponsePayload struct {
	Status string               `json:"status"`
	Result json.RawMessage      `json:"result"`
	Error  *ServerResponseError `json:"error"`
}

type serverDiagnosticsPayload struct {
	Diagnostics json.RawMessage `json:"diagnostics"`
}

type serverStalePayload struct {
	Code string `json:"code"`
}

type serverErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BuildResponseEnvelope completes one pending request and preserves the head
// and document fence captured when that request was admitted.
func (protocol *EnvelopeProtocol) BuildResponseEnvelope(
	requestID string,
	result json.RawMessage,
	responseError *ServerResponseError,
) (ServerEnvelope, error) {
	if protocol == nil || !canonicalUUID(requestID) {
		return ServerEnvelope{}, ErrEnvelopeRequestUnknown
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	request, exists := protocol.pending[requestID]
	if !exists {
		return ServerEnvelope{}, ErrEnvelopeRequestUnknown
	}
	delete(protocol.pending, requestID)
	current, currentExists := protocol.documents[request.document.ModelURI]
	if !request.head.Equal(protocol.head) || !currentExists ||
		!current.fence.Equal(request.document) {
		document := request.document
		return protocol.buildServerLocked(
			ServerEnvelopeStale, request.method, &request.id, request.head, &document,
			serverStalePayload{Code: "stale-request"},
		)
	}
	resultCopy, errorCopy, err := validateServerResponseBody(
		result, responseError, protocol.profile.EffectiveLimits,
	)
	if err != nil {
		return ServerEnvelope{}, err
	}
	payload := serverResponsePayload{Status: "ok", Result: resultCopy, Error: errorCopy}
	if errorCopy != nil {
		payload.Status = "error"
	}
	document := request.document
	return protocol.buildServerLocked(
		ServerEnvelopeResponse, request.method, &request.id, request.head, &document, payload,
	)
}

// BuildDiagnosticsEnvelope carries only a previously sanitized diagnostics
// projection and always binds it to the current exact document fence.
func (protocol *EnvelopeProtocol) BuildDiagnosticsEnvelope(
	document DocumentFence,
	diagnostics json.RawMessage,
) (ServerEnvelope, error) {
	if protocol == nil {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	current, exists := protocol.documents[document.ModelURI]
	if !exists || !current.synced || !current.fence.Equal(document) ||
		validateDiagnosticsEnvelopePayload(
			diagnostics, document, protocol.profile.EffectiveLimits,
		) != nil {
		return ServerEnvelope{}, ErrEnvelopeDocumentStale
	}
	payload := serverDiagnosticsPayload{Diagnostics: slices.Clone(diagnostics)}
	return protocol.buildServerLocked(
		ServerEnvelopeDiagnostics, EnvelopeMethodDiagnostics, nil, protocol.head, &document, payload,
	)
}

func (protocol *EnvelopeProtocol) BuildStaleEnvelope(
	requestID string,
	code string,
) (ServerEnvelope, error) {
	if protocol == nil || !validEnvelopeAtom(code, maximumEnvelopeCode) {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	request, exists := protocol.pending[requestID]
	if !exists {
		request, exists = protocol.stalePending[requestID]
	}
	if !exists {
		return ServerEnvelope{}, ErrEnvelopeRequestUnknown
	}
	delete(protocol.pending, requestID)
	delete(protocol.stalePending, requestID)
	document := request.document
	return protocol.buildServerLocked(
		ServerEnvelopeStale, request.method, &request.id, request.head, &document,
		serverStalePayload{Code: code},
	)
}

func (protocol *EnvelopeProtocol) BuildErrorEnvelope(code, message string) (ServerEnvelope, error) {
	if protocol == nil || !validEnvelopeAtom(code, maximumEnvelopeCode) ||
		!validEnvelopeMessage(message, maximumEnvelopeText) {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	return protocol.buildServerLocked(
		ServerEnvelopeError, EnvelopeMethodError, nil, protocol.head, nil,
		serverErrorPayload{Code: code, Message: message},
	)
}

func (protocol *EnvelopeProtocol) BuildPongEnvelope(replyTo, nonce string) (ServerEnvelope, error) {
	if protocol == nil || !canonicalUUID(replyTo) || !validEnvelopeNonce(nonce) {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	ping, exists := protocol.pendingPings[replyTo]
	if !exists || ping.nonce != nonce {
		return ServerEnvelope{}, ErrEnvelopeRequestUnknown
	}
	envelope, err := protocol.buildServerLocked(
		ServerEnvelopePong, EnvelopeMethodPong, &replyTo, ping.head, nil,
		PingEnvelopePayload{Nonce: nonce},
	)
	if err == nil {
		delete(protocol.pendingPings, replyTo)
	}
	return envelope, err
}

func (protocol *EnvelopeProtocol) buildServerLocked(
	kind string,
	method string,
	replyTo *string,
	head SandboxHeadFence,
	document *DocumentFence,
	payload any,
) (ServerEnvelope, error) {
	if protocol.serverSeq >= maxSafeWireInteger ||
		len(protocol.seenServerMessages) >= maximumEnvelopeMessageIDs {
		return ServerEnvelope{}, ErrEnvelopeSequence
	}
	if head.Validate() != nil || validateServerHeaderShape(kind, method, replyTo) != nil ||
		replyTo != nil && (!canonicalUUID(*replyTo) || *replyTo == protocol.connectionID ||
			*replyTo == protocol.bindingID) ||
		document == nil && kind != ServerEnvelopeError && kind != ServerEnvelopePong ||
		document != nil && (kind == ServerEnvelopeError || kind == ServerEnvelopePong ||
			document.ValidateAgainstHead(head) != nil) {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	messageID := protocol.idSource()
	if !canonicalUUID(messageID) || messageID == protocol.connectionID ||
		messageID == protocol.bindingID || replyTo != nil && messageID == *replyTo {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	if _, exists := protocol.seenServerMessages[messageID]; exists {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	if _, exists := protocol.seenClientMessages[messageID]; exists {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	encoded, err := json.Marshal(payload)
	if err != nil || validateStrictJSONDocument(encoded, maximumEnvelopeDepth) != nil {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	envelope := ServerEnvelope{
		SchemaVersion: EnvelopeSchemaVersion,
		ConnectionID:  protocol.connectionID,
		BindingID:     protocol.bindingID,
		Sequence:      protocol.serverSeq + 1,
		MessageID:     messageID,
		ReplyTo:       cloneStringPointer(replyTo),
		Kind:          kind,
		Method:        method,
		Head:          head,
		Document:      cloneDocumentPointer(document),
		Payload:       slices.Clone(encoded),
	}
	frame, err := json.Marshal(envelope)
	if err != nil || int64(len(frame)) > protocol.profile.EffectiveLimits.MaxFrameBytes {
		return ServerEnvelope{}, ErrEnvelopeDocumentLimit
	}
	protocol.seenServerMessages[messageID] = struct{}{}
	protocol.serverSeq = envelope.Sequence
	return envelope, nil
}

func (envelope ServerEnvelope) MarshalJSONStrict(maxFrameBytes int64) ([]byte, error) {
	if maxFrameBytes <= 0 || envelope.SchemaVersion != EnvelopeSchemaVersion ||
		!canonicalUUID(envelope.ConnectionID) || !canonicalUUID(envelope.BindingID) ||
		envelope.ConnectionID == envelope.BindingID || envelope.Sequence < 2 ||
		envelope.Sequence > maxSafeWireInteger || !canonicalUUID(envelope.MessageID) ||
		envelope.MessageID == envelope.ConnectionID || envelope.MessageID == envelope.BindingID ||
		envelope.ReplyTo != nil && (!canonicalUUID(*envelope.ReplyTo) ||
			*envelope.ReplyTo == envelope.ConnectionID || *envelope.ReplyTo == envelope.BindingID ||
			*envelope.ReplyTo == envelope.MessageID) ||
		validateServerHeaderShape(envelope.Kind, envelope.Method, envelope.ReplyTo) != nil ||
		envelope.Head.Validate() != nil ||
		envelope.Document == nil && envelope.Kind != ServerEnvelopeError && envelope.Kind != ServerEnvelopePong ||
		envelope.Document != nil && (envelope.Kind == ServerEnvelopeError || envelope.Kind == ServerEnvelopePong ||
			envelope.Document.ValidateAgainstHead(envelope.Head) != nil) ||
		len(envelope.Payload) == 0 || isEnvelopeJSONNull(envelope.Payload) ||
		validateStrictJSONDocument(envelope.Payload, maximumEnvelopeDepth) != nil {
		return nil, ErrEnvelopeMalformed
	}
	value, err := json.Marshal(envelope)
	if err != nil || int64(len(value)) > maxFrameBytes ||
		validateStrictJSONDocument(value, maximumEnvelopeDepth) != nil {
		return nil, ErrEnvelopeMalformed
	}
	return value, nil
}

// ServerEnvelopeExpectation is the browser-side exact fence correlation used
// to validate a Gateway envelope. ExpectedSequence is the next value, not the
// last observed value. MessageID is optional because the browser learns a new
// server message ID from the frame; when supplied it must match exactly.
type ServerEnvelopeExpectation struct {
	ConnectionID     string
	BindingID        string
	ExpectedSequence uint64
	MessageID        string
	ReplyTo          *string
	Method           string
	Head             SandboxHeadFence
	Document         *DocumentFence
	Nonce            string
	Limits           EffectiveLimits
}

// DecodeServerEnvelope is the strict browser-side decoder. It rejects stale
// connection, binding, sequence, head, document, and request correlation even
// when the payload itself is otherwise valid.
func DecodeServerEnvelope(frame []byte, expected ServerEnvelopeExpectation) (ServerEnvelope, error) {
	if !canonicalUUID(expected.ConnectionID) || !canonicalUUID(expected.BindingID) ||
		expected.ConnectionID == expected.BindingID ||
		expected.MessageID != "" && !canonicalUUID(expected.MessageID) ||
		expected.ReplyTo != nil && (!canonicalUUID(*expected.ReplyTo) ||
			*expected.ReplyTo == expected.ConnectionID || *expected.ReplyTo == expected.BindingID) ||
		!validEnvelopeAtom(expected.Method, 256) ||
		expected.ExpectedSequence == 0 || expected.ExpectedSequence > maxSafeWireInteger ||
		expected.Head.Validate() != nil || expected.Limits.MaxFrameBytes <= 0 ||
		int64(len(frame)) <= 0 || int64(len(frame)) > expected.Limits.MaxFrameBytes ||
		!utf8.Valid(frame) || validateStrictJSONDocument(frame, maximumEnvelopeDepth) != nil {
		return ServerEnvelope{}, ErrEnvelopeMalformed
	}
	fields, err := decodeExactObject(frame, []string{
		"schemaVersion", "connectionId", "bindingId", "sequence", "messageId",
		"replyTo", "kind", "method", "sandboxHeadFence", "documentFence", "payload",
	})
	if err != nil {
		return ServerEnvelope{}, fmt.Errorf("%w: %v", ErrEnvelopeMalformed, err)
	}
	var result ServerEnvelope
	if decodeString(fields["schemaVersion"], &result.SchemaVersion) != nil ||
		result.SchemaVersion != EnvelopeSchemaVersion || decodeString(fields["kind"], &result.Kind) != nil ||
		decodeString(fields["connectionId"], &result.ConnectionID) != nil ||
		decodeString(fields["bindingId"], &result.BindingID) != nil ||
		decodeString(fields["messageId"], &result.MessageID) != nil ||
		decodeString(fields["method"], &result.Method) != nil ||
		!canonicalUUID(result.MessageID) ||
		result.ConnectionID != expected.ConnectionID || result.BindingID != expected.BindingID {
		return ServerEnvelope{}, ErrEnvelopeConnection
	}
	if expected.MessageID != "" && result.MessageID != expected.MessageID {
		return ServerEnvelope{}, ErrEnvelopeRequestUnknown
	}
	if decodeMethodUint(fields["sequence"], &result.Sequence) != nil ||
		result.Sequence != expected.ExpectedSequence {
		return ServerEnvelope{}, ErrEnvelopeSequence
	}
	result.ReplyTo, err = decodeEnvelopeReplyTo(fields["replyTo"])
	if err != nil || !equalEnvelopeReplyTo(result.ReplyTo, expected.ReplyTo) ||
		result.Method != expected.Method ||
		result.MessageID == result.ConnectionID || result.MessageID == result.BindingID ||
		result.ReplyTo != nil && result.MessageID == *result.ReplyTo ||
		validateServerHeaderShape(result.Kind, result.Method, result.ReplyTo) != nil {
		return ServerEnvelope{}, ErrEnvelopeRequestUnknown
	}
	result.Head, err = DecodeSandboxHeadFence(fields["sandboxHeadFence"])
	if err != nil || !result.Head.Equal(expected.Head) {
		return ServerEnvelope{}, ErrEnvelopeHeadStale
	}
	if isEnvelopeJSONNull(fields["documentFence"]) {
		if expected.Document != nil || (result.Kind != ServerEnvelopeError && result.Kind != ServerEnvelopePong) {
			return ServerEnvelope{}, ErrEnvelopeDocumentStale
		}
	} else {
		document, decodeErr := DecodeDocumentFence(fields["documentFence"])
		if decodeErr != nil || expected.Document == nil || !document.Equal(*expected.Document) ||
			document.ValidateAgainstHead(result.Head) != nil ||
			result.Kind == ServerEnvelopeError || result.Kind == ServerEnvelopePong {
			return ServerEnvelope{}, ErrEnvelopeDocumentStale
		}
		result.Document = &document
	}
	if err := decodeExpectedServerPayload(result.Kind, fields["payload"], expected); err != nil {
		return ServerEnvelope{}, err
	}
	result.Payload = slices.Clone(fields["payload"])
	return result, nil
}

func decodeExpectedServerPayload(
	kind string,
	raw json.RawMessage,
	expected ServerEnvelopeExpectation,
) error {
	switch kind {
	case ServerEnvelopeResponse:
		fields, err := decodeEnvelopePayload(raw, []string{"status", "result", "error"})
		if err != nil {
			return err
		}
		var status string
		if decodeString(fields["status"], &status) != nil {
			return ErrEnvelopeMalformed
		}
		if _, _, err := decodeServerResponseFields(
			status, fields["result"], fields["error"], expected.Limits,
		); err != nil {
			return err
		}
	case ServerEnvelopeDiagnostics:
		fields, err := decodeEnvelopePayload(raw, []string{"diagnostics"})
		if err != nil {
			return err
		}
		if expected.Document == nil ||
			validateDiagnosticsEnvelopePayload(
				fields["diagnostics"], *expected.Document, expected.Limits,
			) != nil {
			return ErrEnvelopeMalformed
		}
	case ServerEnvelopeStale:
		fields, err := decodeEnvelopePayload(raw, []string{"code"})
		if err != nil {
			return err
		}
		var code string
		if decodeString(fields["code"], &code) != nil || !validEnvelopeAtom(code, maximumEnvelopeCode) {
			return ErrEnvelopeMalformed
		}
	case ServerEnvelopeError:
		fields, err := decodeEnvelopePayload(raw, []string{"code", "message"})
		if err != nil {
			return err
		}
		var code, message string
		if decodeString(fields["code"], &code) != nil || !validEnvelopeAtom(code, maximumEnvelopeCode) ||
			decodeString(fields["message"], &message) != nil || !validEnvelopeMessage(message, maximumEnvelopeText) {
			return ErrEnvelopeMalformed
		}
	case ServerEnvelopePong:
		payload, err := decodeNoncePayload(raw)
		if err != nil || payload.Nonce != expected.Nonce {
			return ErrEnvelopeMalformed
		}
	default:
		return ErrEnvelopeMalformed
	}
	return nil
}

func validateServerHeaderShape(kind, method string, replyTo *string) error {
	switch kind {
	case ServerEnvelopeResponse, ServerEnvelopeStale:
		if replyTo == nil || AdmitBrowserRequestMethod(method, ProductionV1MethodBaseline()) != nil {
			return ErrEnvelopeRequestUnknown
		}
	case ServerEnvelopeDiagnostics:
		if replyTo != nil || method != EnvelopeMethodDiagnostics {
			return ErrEnvelopeMalformed
		}
	case ServerEnvelopeError:
		if replyTo != nil || method != EnvelopeMethodError {
			return ErrEnvelopeMalformed
		}
	case ServerEnvelopePong:
		if replyTo == nil || method != EnvelopeMethodPong {
			return ErrEnvelopeMalformed
		}
	default:
		return ErrEnvelopeMalformed
	}
	return nil
}

func validateServerResponseBody(
	result json.RawMessage,
	responseError *ServerResponseError,
	limits EffectiveLimits,
) (json.RawMessage, *ServerResponseError, error) {
	if (result == nil) == (responseError == nil) {
		return nil, nil, ErrEnvelopeMalformed
	}
	if responseError != nil {
		if responseError.Code == 0 || !validEnvelopeMessage(responseError.Message, maximumEnvelopeText) {
			return nil, nil, ErrEnvelopeMalformed
		}
		copyValue := *responseError
		return json.RawMessage("null"), &copyValue, nil
	}
	if err := validateBoundedRawJSON(result, limits.MaxResultBytes); err != nil {
		return nil, nil, ErrEnvelopeMalformed
	}
	return slices.Clone(result), nil, nil
}

func decodeServerResponseFields(
	status string,
	resultRaw json.RawMessage,
	errorRaw json.RawMessage,
	limits EffectiveLimits,
) (json.RawMessage, *ServerResponseError, error) {
	errorNull := isEnvelopeJSONNull(errorRaw)
	if status != "ok" && status != "error" {
		return nil, nil, ErrEnvelopeMalformed
	}
	if status == "ok" {
		if !errorNull {
			return nil, nil, ErrEnvelopeMalformed
		}
		if validateBoundedRawJSON(resultRaw, limits.MaxResultBytes) != nil {
			return nil, nil, ErrEnvelopeMalformed
		}
		return slices.Clone(resultRaw), nil, nil
	}
	if !isEnvelopeJSONNull(resultRaw) || errorNull {
		return nil, nil, ErrEnvelopeMalformed
	}
	fields, err := decodeEnvelopePayload(errorRaw, []string{"code", "message"})
	if err != nil {
		return nil, nil, err
	}
	var code int64
	if err := decodeStrictInt(fields["code"], &code); err != nil || code == 0 {
		return nil, nil, ErrEnvelopeMalformed
	}
	var message string
	if decodeString(fields["message"], &message) != nil ||
		!validEnvelopeMessage(message, maximumEnvelopeText) {
		return nil, nil, ErrEnvelopeMalformed
	}
	return nil, &ServerResponseError{Code: code, Message: message}, nil
}

func decodeEnvelopePayload(value json.RawMessage, required []string) (map[string]json.RawMessage, error) {
	if len(value) == 0 || isEnvelopeJSONNull(value) || validateStrictJSONDocument(value, maximumEnvelopeDepth) != nil {
		return nil, ErrEnvelopeMalformed
	}
	fields, err := decodeExactObject(value, required)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelopeMalformed, err)
	}
	return fields, nil
}

func decodeEmptyEnvelopePayload(value json.RawMessage) error {
	fields, err := decodeEnvelopePayload(value, []string{})
	if err != nil || len(fields) != 0 {
		return ErrEnvelopeMalformed
	}
	return nil
}

func decodeNoncePayload(value json.RawMessage) (PingEnvelopePayload, error) {
	fields, err := decodeEnvelopePayload(value, []string{"nonce"})
	if err != nil {
		return PingEnvelopePayload{}, err
	}
	var nonce string
	if decodeString(fields["nonce"], &nonce) != nil || !validEnvelopeNonce(nonce) {
		return PingEnvelopePayload{}, ErrEnvelopeMalformed
	}
	return PingEnvelopePayload{Nonce: nonce}, nil
}

func decodeEnvelopeDocumentArray(
	value json.RawMessage,
	head SandboxHeadFence,
	maximum int,
) ([]DocumentFence, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var raw []json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil || len(raw) > maximum {
		return nil, ErrEnvelopeMalformed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrEnvelopeMalformed
	}
	result := make([]DocumentFence, len(raw))
	for index, encoded := range raw {
		document, err := DecodeDocumentFence(encoded)
		if err != nil || document.ValidateAgainstHead(head) != nil ||
			(index > 0 && result[index-1].ModelURI >= document.ModelURI) {
			return nil, ErrEnvelopeDocumentStale
		}
		result[index] = document
	}
	return result, nil
}

func validateBoundedRawJSON(value json.RawMessage, maximum int64) error {
	if int64(len(value)) <= 0 || int64(len(value)) > maximum || !utf8.Valid(value) ||
		validateStrictJSONDocument(value, maximumEnvelopeDepth) != nil {
		return ErrEnvelopeMalformed
	}
	return nil
}

func decodeEnvelopeText(value json.RawMessage, target *string) error {
	if decodeString(value, target) != nil || !utf8.ValidString(*target) ||
		!validJSONUnicodeEscapes(value) {
		return ErrEnvelopeMalformed
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD. That
// recovery is useful for generic JSON but would silently change synchronized
// source text, so the envelope boundary rejects it before acquiring state.
func validJSONUnicodeEscapes(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) < 2 || trimmed[0] != '"' || trimmed[len(trimmed)-1] != '"' {
		return false
	}
	for index := 1; index < len(trimmed)-1; index++ {
		if trimmed[index] != '\\' {
			continue
		}
		index++
		if index >= len(trimmed)-1 {
			return false
		}
		if trimmed[index] != 'u' {
			continue
		}
		code, ok := decodeJSONHexQuad(trimmed, index+1)
		if !ok {
			return false
		}
		index += 4
		if code >= 0xd800 && code <= 0xdbff {
			if index+6 >= len(trimmed) || trimmed[index+1] != '\\' || trimmed[index+2] != 'u' {
				return false
			}
			low, valid := decodeJSONHexQuad(trimmed, index+3)
			if !valid || low < 0xdc00 || low > 0xdfff {
				return false
			}
			index += 6
		} else if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
	}
	return true
}

func decodeJSONHexQuad(value []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(value) {
		return 0, false
	}
	var result uint16
	for _, character := range value[start : start+4] {
		result <<= 4
		switch {
		case character >= '0' && character <= '9':
			result += uint16(character - '0')
		case character >= 'a' && character <= 'f':
			result += uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			result += uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return result, true
}

func documentAdmittedByProfile(document DocumentFence, profile ProfileIdentity) bool {
	identity, err := ParseCandidateModelURI(document.ModelURI)
	return err == nil && profileSupportsRepositoryPath(profile, identity.Path)
}

func validateDiagnosticsEnvelopePayload(
	value json.RawMessage,
	document DocumentFence,
	limits EffectiveLimits,
) error {
	if document.Validate() != nil || limits.MaxDiagnosticsPerDocument <= 0 ||
		validateBoundedRawJSON(value, limits.MaxResultBytes) != nil {
		return ErrEnvelopeMalformed
	}
	fields, err := decodeEnvelopePayload(value, []string{"uri", "version", "diagnostics"})
	if err != nil {
		return err
	}
	var uri string
	var version uint64
	if decodeString(fields["uri"], &uri) != nil || uri != document.ModelURI ||
		decodeMethodUint(fields["version"], &version) != nil || version != document.ModelVersion {
		return ErrEnvelopeDocumentStale
	}
	decoder := json.NewDecoder(bytes.NewReader(fields["diagnostics"]))
	var diagnostics []json.RawMessage
	if err := decoder.Decode(&diagnostics); err != nil || diagnostics == nil ||
		len(diagnostics) > limits.MaxDiagnosticsPerDocument {
		return ErrEnvelopeMalformed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrEnvelopeMalformed
	}
	return nil
}

func decodeStrictInt(value json.RawMessage, target *int64) error {
	if target == nil || len(value) == 0 || isEnvelopeJSONNull(value) {
		return ErrEnvelopeMalformed
	}
	trimmed := bytes.TrimSpace(value)
	start := 0
	if len(trimmed) > 0 && trimmed[0] == '-' {
		start = 1
	}
	if start == len(trimmed) {
		return ErrEnvelopeMalformed
	}
	for _, character := range trimmed[start:] {
		if character < '0' || character > '9' {
			return ErrEnvelopeMalformed
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrEnvelopeMalformed
	}
	return nil
}

func contentDigest(text, expected string) bool {
	sum := sha256.Sum256([]byte(text))
	return expected == "sha256:"+hex.EncodeToString(sum[:])
}

func validEnvelopeNonce(value string) bool {
	return validEnvelopeAtom(value, 128)
}

func validEnvelopeAtom(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && value == strings.TrimSpace(value) &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func validEnvelopeMessage(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00")
}

func isEnvelopeJSONNull(value json.RawMessage) bool {
	return string(bytes.TrimSpace(value)) == "null"
}

func decodeEnvelopeReplyTo(value json.RawMessage) (*string, error) {
	if isEnvelopeJSONNull(value) {
		return nil, nil
	}
	var decoded string
	if decodeString(value, &decoded) != nil || !canonicalUUID(decoded) {
		return nil, ErrEnvelopeMalformed
	}
	return &decoded, nil
}

func dereferenceEnvelopeReplyTo(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func equalEnvelopeReplyTo(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneDocumentPointer(value *DocumentFence) *DocumentFence {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func equalDocumentSequences(left, right []DocumentFence) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !left[index].Equal(right[index]) {
			return false
		}
	}
	return true
}
