package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	ServerMessageKindResponse     = "server.response"
	ServerMessageKindNotification = "server.notification"

	ServerMessageAccepted     ServerMessageDisposition = "accepted"
	ServerMessageStaleDropped ServerMessageDisposition = "stale-dropped"

	maxServerMessageDepth = 16
	maxServerRequestIDs   = 65_536
	maxServerErrorMessage = 4 << 10
	maxServerText         = 64 << 10
	maxServerLabel        = 4 << 10
	maxServerDetail       = 16 << 10
)

var (
	ErrServerMessageMalformed      = errors.New("malformed LSP server message")
	ErrServerMessageTooLarge       = errors.New("LSP server message exceeds effective limit")
	ErrServerRequestForbidden      = errors.New("LSP server-to-client request is forbidden")
	ErrServerNotificationForbidden = errors.New("LSP server notification is forbidden")
	ErrServerResponseUnknown       = errors.New("unknown, duplicate, or completed LSP server response")
	ErrServerResponseMethodInvalid = errors.New("LSP server response method is not admitted")
	ErrServerResultInvalid         = errors.New("invalid LSP server result")
	ErrServerRequestLimit          = errors.New("LSP pending request limit reached")
	ErrServerRequestIDLimit        = errors.New("LSP connection request ID budget exhausted")
)

// PendingServerRequest is immutable correlation state captured before a
// JSON-RPC request is written to the language-server process. The result is
// always fenced to these values, never to whatever head happens to be current
// when the process replies.
type PendingServerRequest struct {
	ID       string
	Method   string
	Head     SandboxHeadFence
	Document DocumentFence
}

type ServerMessageDisposition string

// ServerResponseError is the only error shape allowed to cross the adapter.
// Arbitrary server error data is intentionally not retained.
type ServerResponseError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

// FilteredServerMessage is a safe, method-specific projection. Payload is
// freshly marshalled from validated DTOs; it never aliases the server frame.
// A stale-drop deliberately carries no Payload or Error body.
type FilteredServerMessage struct {
	Kind        string
	RequestID   string
	Method      string
	Head        SandboxHeadFence
	Document    DocumentFence
	Disposition ServerMessageDisposition
	Payload     json.RawMessage
	Error       *ServerResponseError
}

// ServerMessageFilter is connection-local. It admits only an exact canonical
// profile method subset and makes JSON-RPC request IDs single-use. A response
// consumes its pending entry before result parsing, so a malformed or stale
// response cannot be followed by a second response for the same ID.
type ServerMessageFilter struct {
	mu      sync.Mutex
	methods map[string]bool
	limits  EffectiveLimits
	paths   map[string]struct{}
	pending map[string]PendingServerRequest
	seen    map[string]struct{}
}

func NewServerMessageFilter(
	methods []string,
	limits EffectiveLimits,
	repositoryPaths []string,
) (*ServerMessageFilter, error) {
	if ValidateCanonicalProductionV1MethodAllowlist(methods) != nil ||
		limits.MaxFrameBytes <= 0 || limits.MaxResultBytes <= 0 ||
		limits.MaxConcurrentRequests <= 0 || limits.MaxDiagnosticsPerDocument <= 0 ||
		limits.MaxCompletionItems <= 0 || limits.MaxNavigationLocations <= 0 ||
		len(repositoryPaths) == 0 || len(repositoryPaths) > repository.MaxTreeFiles {
		return nil, ErrServerMessageMalformed
	}
	paths := make(map[string]struct{}, len(repositoryPaths))
	for index, candidatePath := range repositoryPaths {
		normalized, err := repository.NormalizePath(candidatePath)
		if err != nil || normalized != candidatePath ||
			(index > 0 && repositoryPaths[index-1] >= candidatePath) {
			return nil, ErrServerMessageMalformed
		}
		paths[candidatePath] = struct{}{}
	}
	return &ServerMessageFilter{
		methods: stringSet(slices.Clone(methods)),
		limits:  limits,
		paths:   paths,
		pending: make(map[string]PendingServerRequest),
		seen:    make(map[string]struct{}),
	}, nil
}

// RegisterPending reserves a canonical request ID exactly once for the life
// of this connection. All production-v1 request results are document-scoped.
func (filter *ServerMessageFilter) RegisterPending(request PendingServerRequest) error {
	if filter == nil || !canonicalUUID(request.ID) || request.Head.Validate() != nil ||
		request.Document.ValidateAgainstHead(request.Head) != nil ||
		IsPermanentlyForbiddenMethod(request.Method) || !productionV1MethodSet[request.Method] ||
		!filter.methods[request.Method] || request.Method == "textDocument/publishDiagnostics" {
		return ErrServerResponseMethodInvalid
	}
	filter.mu.Lock()
	defer filter.mu.Unlock()
	if _, exists := filter.seen[request.ID]; exists {
		return ErrServerResponseUnknown
	}
	if len(filter.seen) >= maxServerRequestIDs {
		return ErrServerRequestIDLimit
	}
	if len(filter.pending) >= filter.limits.MaxConcurrentRequests {
		return ErrServerRequestLimit
	}
	filter.seen[request.ID] = struct{}{}
	filter.pending[request.ID] = request
	return nil
}

// CancelPending completes a request without accepting a server response. A
// timeout, browser cancellation, rebind, or binding close must use this before
// releasing its own request state; a late response is then rejected as
// unknown/completed and the ID can never be registered again.
func (filter *ServerMessageFilter) CancelPending(requestID string) error {
	if filter == nil || !canonicalUUID(requestID) {
		return ErrServerResponseUnknown
	}
	filter.mu.Lock()
	defer filter.mu.Unlock()
	if _, exists := filter.pending[requestID]; !exists {
		return ErrServerResponseUnknown
	}
	delete(filter.pending, requestID)
	return nil
}

// Filter parses exactly one JSON-RPC 2.0 server message. JSON-RPC batches and
// every server-to-client request are permanently rejected. currentDocuments
// must be the Gateway's latest canonical, URI-sorted binding projection.
func (filter *ServerMessageFilter) Filter(
	frame []byte,
	currentHead SandboxHeadFence,
	currentDocuments []DocumentFence,
) (FilteredServerMessage, error) {
	if filter == nil || len(frame) == 0 {
		return FilteredServerMessage{}, ErrServerMessageMalformed
	}
	if int64(len(frame)) > filter.limits.MaxFrameBytes {
		return FilteredServerMessage{}, ErrServerMessageTooLarge
	}
	if currentHead.Validate() != nil || validateCurrentDocuments(currentHead, currentDocuments) != nil {
		return FilteredServerMessage{}, ErrServerMessageMalformed
	}
	if !utf8.Valid(frame) {
		return FilteredServerMessage{}, ErrServerMessageMalformed
	}
	fields, err := decodeServerTopObject(frame)
	if err != nil {
		return FilteredServerMessage{}, fmt.Errorf("%w: %v", ErrServerMessageMalformed, err)
	}
	_, hasID := fields["id"]
	_, hasMethod := fields["method"]
	if hasID && hasMethod {
		if err := validateServerJSONDocument(frame, maxServerMessageDepth); err != nil {
			return FilteredServerMessage{}, fmt.Errorf("%w: %v", ErrServerMessageMalformed, err)
		}
		method, _ := decodeOptionalServerString(fields["method"], 256)
		if method != "" && (IsPermanentlyForbiddenMethod(method) || isForbiddenServerRequestMethod(method)) {
			return FilteredServerMessage{}, fmt.Errorf("%w: %s", ErrServerRequestForbidden, method)
		}
		return FilteredServerMessage{}, ErrServerRequestForbidden
	}
	if hasID {
		return filter.filterResponse(frame, fields, currentHead, currentDocuments)
	}
	if hasMethod {
		if err := validateServerJSONDocument(frame, maxServerMessageDepth); err != nil {
			return FilteredServerMessage{}, fmt.Errorf("%w: %v", ErrServerMessageMalformed, err)
		}
		return filter.filterNotification(fields, currentHead, currentDocuments)
	}
	return FilteredServerMessage{}, ErrServerMessageMalformed
}

func (filter *ServerMessageFilter) filterResponse(
	frame []byte,
	fields map[string]json.RawMessage,
	currentHead SandboxHeadFence,
	currentDocuments []DocumentFence,
) (FilteredServerMessage, error) {
	requestID, err := decodeRequiredServerString(fields["id"], 64)
	if err != nil || !canonicalUUID(requestID) {
		return FilteredServerMessage{}, ErrServerMessageMalformed
	}

	// Completion is atomic and intentionally precedes exact result validation.
	// Any subsequent protocol failure leaves the ID consumed.
	filter.mu.Lock()
	request, exists := filter.pending[requestID]
	if exists {
		delete(filter.pending, requestID)
	}
	filter.mu.Unlock()
	if !exists {
		return FilteredServerMessage{}, ErrServerResponseUnknown
	}
	if err := validateServerJSONDocument(frame, maxServerMessageDepth); err != nil {
		return FilteredServerMessage{}, fmt.Errorf("%w: %v", ErrServerMessageMalformed, err)
	}

	if err := requireServerFields(fields, []string{"jsonrpc", "id"}, []string{"result", "error"}); err != nil {
		return FilteredServerMessage{}, fmt.Errorf("%w: %v", ErrServerMessageMalformed, err)
	}
	if err := requireJSONRPCVersion(fields["jsonrpc"]); err != nil {
		return FilteredServerMessage{}, err
	}
	result, hasResult := fields["result"]
	errorValue, hasError := fields["error"]
	if hasResult == hasError {
		return FilteredServerMessage{}, ErrServerMessageMalformed
	}
	if hasResult && int64(len(result)) > filter.limits.MaxResultBytes {
		return FilteredServerMessage{}, ErrServerMessageTooLarge
	}

	message := FilteredServerMessage{
		Kind: ServerMessageKindResponse, RequestID: request.ID, Method: request.Method,
		Head: request.Head, Document: request.Document, Disposition: ServerMessageAccepted,
	}
	if hasResult {
		message.Payload, err = sanitizeServerResult(
			request.Method, result, request, filter.limits, filter.paths,
		)
	} else {
		message.Error, err = decodeServerResponseError(errorValue)
	}
	if err != nil {
		return FilteredServerMessage{}, err
	}
	if pendingRequestIsStale(request, currentHead, currentDocuments) {
		message.Disposition = ServerMessageStaleDropped
		message.Payload = nil
		message.Error = nil
	}
	return message, nil
}

func (filter *ServerMessageFilter) filterNotification(
	fields map[string]json.RawMessage,
	currentHead SandboxHeadFence,
	currentDocuments []DocumentFence,
) (FilteredServerMessage, error) {
	if err := requireServerFields(fields, []string{"jsonrpc", "method", "params"}, nil); err != nil {
		return FilteredServerMessage{}, fmt.Errorf("%w: %v", ErrServerMessageMalformed, err)
	}
	if err := requireJSONRPCVersion(fields["jsonrpc"]); err != nil {
		return FilteredServerMessage{}, err
	}
	method, err := decodeRequiredServerString(fields["method"], 256)
	if err != nil {
		return FilteredServerMessage{}, ErrServerMessageMalformed
	}
	if IsPermanentlyForbiddenMethod(method) || isForbiddenServerRequestMethod(method) {
		return FilteredServerMessage{}, fmt.Errorf("%w: %s", ErrServerNotificationForbidden, method)
	}
	// window/logMessage and window/showMessage are deliberately not in the
	// immutable production-v1 capability baseline and cannot cross the browser
	// boundary. The only admitted server notification is versioned diagnostics.
	if method != "textDocument/publishDiagnostics" || !filter.methods[method] {
		return FilteredServerMessage{}, fmt.Errorf("%w: %s", ErrServerNotificationForbidden, method)
	}
	if int64(len(fields["params"])) > filter.limits.MaxResultBytes {
		return FilteredServerMessage{}, ErrServerMessageTooLarge
	}
	payload, document, stale, err := sanitizePublishDiagnostics(
		fields["params"], currentHead, currentDocuments, filter.limits, filter.paths,
	)
	if err != nil {
		return FilteredServerMessage{}, err
	}
	message := FilteredServerMessage{
		Kind: ServerMessageKindNotification, Method: method, Head: currentHead,
		Document: document, Disposition: ServerMessageAccepted, Payload: payload,
	}
	if stale {
		message.Disposition = ServerMessageStaleDropped
		message.Payload = nil
	}
	return message, nil
}

func validateCurrentDocuments(head SandboxHeadFence, documents []DocumentFence) error {
	for index, document := range documents {
		if document.ValidateAgainstHead(head) != nil ||
			(index > 0 && documents[index-1].ModelURI >= document.ModelURI) {
			return ErrServerMessageMalformed
		}
	}
	return nil
}

func pendingRequestIsStale(
	request PendingServerRequest,
	currentHead SandboxHeadFence,
	currentDocuments []DocumentFence,
) bool {
	if !request.Head.Equal(currentHead) {
		return true
	}
	index, found := slices.BinarySearchFunc(currentDocuments, request.Document.ModelURI, func(document DocumentFence, uri string) int {
		return strings.Compare(document.ModelURI, uri)
	})
	return !found || !request.Document.Equal(currentDocuments[index])
}

func requireJSONRPCVersion(value json.RawMessage) error {
	version, err := decodeRequiredServerString(value, 3)
	if err != nil || version != "2.0" {
		return ErrServerMessageMalformed
	}
	return nil
}

func requireServerFields(fields map[string]json.RawMessage, required, alternatives []string) error {
	allowed := make(map[string]bool, len(required)+len(alternatives))
	for _, name := range required {
		allowed[name] = true
		if _, exists := fields[name]; !exists {
			return fmt.Errorf("missing field %q", name)
		}
	}
	for _, name := range alternatives {
		allowed[name] = true
	}
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

func decodeServerTopObject(value []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("JSON-RPC batch or non-object message is forbidden")
	}
	result := map[string]json.RawMessage{}
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok || forbiddenJSONKey(name) {
			return nil, errors.New("invalid JSON object field")
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		result[name] = slices.Clone(raw)
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("unterminated object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values are forbidden")
	}
	return result, nil
}

func validateServerJSONDocument(value []byte, maximumDepth int) error {
	if !utf8.Valid(value) {
		return errors.New("JSON must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := validateServerJSONValue(decoder, 0, maximumDepth); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are forbidden")
	}
	return nil
}

func validateServerJSONValue(decoder *json.Decoder, depth, maximumDepth int) error {
	if depth > maximumDepth {
		return errors.New("JSON nesting depth exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch typed := token.(type) {
	case string:
		if !validServerString(typed, maxServerText) {
			return errors.New("invalid JSON string")
		}
		return nil
	case json.Number:
		if !validSafeIntegerLexeme(typed.String()) {
			return errors.New("non-integer or widened JSON number")
		}
		return nil
	case json.Delim:
		if depth == maximumDepth {
			return errors.New("JSON nesting depth exceeds limit")
		}
		switch typed {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok || !validServerString(name, 256) || forbiddenJSONKey(name) || seen[name] {
					return errors.New("duplicate, forbidden, or invalid JSON object field")
				}
				seen[name] = true
				if err := validateServerJSONValue(decoder, depth+1, maximumDepth); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("unterminated JSON object")
			}
		case '[':
			for decoder.More() {
				if err := validateServerJSONValue(decoder, depth+1, maximumDepth); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("unterminated JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	return nil
}

func forbiddenJSONKey(value string) bool {
	switch value {
	case "__proto__", "prototype", "constructor":
		return true
	default:
		return false
	}
}

func validSafeIntegerLexeme(value string) bool {
	if value == "" || strings.ContainsAny(value, ".eE+") || (len(value) > 1 && value[0] == '0') ||
		strings.HasPrefix(value, "-0") {
		return false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return false
	}
	return parsed >= -int64(maxSafeWireInteger) && parsed <= int64(maxSafeWireInteger)
}

func isForbiddenServerRequestMethod(method string) bool {
	return strings.HasPrefix(method, "window/") || strings.HasPrefix(method, "telemetry/") ||
		strings.Contains(strings.ToLower(method), "showdocument") ||
		strings.Contains(strings.ToLower(method), "workdoneprogress/create") ||
		strings.Contains(strings.ToLower(method), "progress/create")
}

func decodeRequiredServerString(value json.RawMessage, maximum int) (string, error) {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", errors.New("string is required")
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil || !validServerString(result, maximum) {
		return "", errors.New("invalid string")
	}
	return result, nil
}

func decodeOptionalServerString(value json.RawMessage, maximum int) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	return decodeRequiredServerString(value, maximum)
}

func validServerString(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') &&
		!strings.ContainsRune(value, utf8.RuneError)
}

type serverRange struct {
	Start BrowserPosition `json:"start"`
	End   BrowserPosition `json:"end"`
}

type serverMarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type serverHover struct {
	Contents any          `json:"contents"`
	Range    *serverRange `json:"range,omitempty"`
}

type serverLocation struct {
	URI   string      `json:"uri"`
	Range serverRange `json:"range"`
}

type serverDiagnostic struct {
	Range    serverRange `json:"range"`
	Severity *uint64     `json:"severity,omitempty"`
	Code     any         `json:"code,omitempty"`
	Source   string      `json:"source,omitempty"`
	Message  string      `json:"message"`
	Tags     []uint64    `json:"tags,omitempty"`
}

type serverPublishDiagnostics struct {
	URI         string             `json:"uri"`
	Version     uint64             `json:"version"`
	Diagnostics []serverDiagnostic `json:"diagnostics"`
}

type serverDocumentDiagnosticReport struct {
	Kind     string             `json:"kind"`
	ResultID string             `json:"resultId,omitempty"`
	Items    []serverDiagnostic `json:"items"`
}

func sanitizeServerResult(
	method string,
	value json.RawMessage,
	request PendingServerRequest,
	limits EffectiveLimits,
	repositoryPaths map[string]struct{},
) (json.RawMessage, error) {
	if len(value) == 0 || int64(len(value)) > limits.MaxResultBytes {
		return nil, ErrServerMessageTooLarge
	}
	var sanitized any
	var err error
	switch method {
	case "textDocument/hover":
		sanitized, err = sanitizeHover(value)
	case "textDocument/completion":
		sanitized, err = sanitizeCompletion(value, limits)
	case "textDocument/signatureHelp":
		sanitized, err = sanitizeSignatureHelp(value, limits)
	case "textDocument/declaration", "textDocument/definition", "textDocument/implementation",
		"textDocument/references", "textDocument/typeDefinition":
		sanitized, err = sanitizeNavigation(value, request.Head, limits, repositoryPaths)
	case "textDocument/documentHighlight":
		sanitized, err = sanitizeDocumentHighlights(value, limits)
	case "textDocument/documentSymbol":
		sanitized, err = sanitizeDocumentSymbols(value, limits)
	case "textDocument/semanticTokens/full", "textDocument/semanticTokens/range":
		sanitized, err = sanitizeSemanticTokens(value, limits)
	case "textDocument/inlayHint":
		sanitized, err = sanitizeInlayHints(value, limits)
	case "textDocument/diagnostic":
		sanitized, err = sanitizeDocumentDiagnostics(value, limits)
	default:
		return nil, fmt.Errorf("%w: %s", ErrServerResponseMethodInvalid, method)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerResultInvalid, err)
	}
	encoded, err := json.Marshal(sanitized)
	if err != nil || int64(len(encoded)) > limits.MaxResultBytes {
		return nil, ErrServerMessageTooLarge
	}
	return json.RawMessage(encoded), nil
}

func decodeServerResponseError(value json.RawMessage) (*ServerResponseError, error) {
	fields, err := decodeMethodObject(value, []string{"code", "message"}, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid response error", ErrServerResultInvalid)
	}
	code, err := decodeServerInt(fields["code"])
	if err != nil || code < -2_147_483_648 || code > 2_147_483_647 {
		return nil, fmt.Errorf("%w: invalid response error code", ErrServerResultInvalid)
	}
	message, err := decodeRequiredServerString(fields["message"], maxServerErrorMessage)
	if err != nil || message == "" {
		return nil, fmt.Errorf("%w: invalid response error message", ErrServerResultInvalid)
	}
	return &ServerResponseError{Code: code, Message: message}, nil
}

func sanitizeHover(value json.RawMessage) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	fields, err := decodeMethodObject(value, []string{"contents"}, []string{"range"})
	if err != nil {
		return nil, err
	}
	contents, err := decodeMarkupOrString(fields["contents"], maxServerText)
	if err != nil {
		return nil, err
	}
	result := serverHover{Contents: contents}
	if raw, exists := fields["range"]; exists {
		parsed, err := decodeServerRange(raw)
		if err != nil {
			return nil, err
		}
		result.Range = &parsed
	}
	return result, nil
}

func sanitizeNavigation(
	value json.RawMessage,
	head SandboxHeadFence,
	limits EffectiveLimits,
	repositoryPaths map[string]struct{},
) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return decodeServerLocation(trimmed, head, repositoryPaths)
	}
	raw, err := decodeServerArray(value, limits.MaxNavigationLocations)
	if err != nil {
		return nil, err
	}
	locations := make([]serverLocation, len(raw))
	for index, encoded := range raw {
		locations[index], err = decodeServerLocation(encoded, head, repositoryPaths)
		if err != nil {
			return nil, err
		}
	}
	return locations, nil
}

func decodeServerLocation(
	value json.RawMessage,
	head SandboxHeadFence,
	repositoryPaths map[string]struct{},
) (serverLocation, error) {
	fields, err := decodeMethodObject(value, []string{"uri", "range"}, nil)
	if err != nil {
		return serverLocation{}, err
	}
	uri, err := decodeCandidateURIForHead(fields["uri"], head, repositoryPaths)
	if err != nil {
		return serverLocation{}, err
	}
	rangeValue, err := decodeServerRange(fields["range"])
	if err != nil {
		return serverLocation{}, err
	}
	return serverLocation{URI: uri, Range: rangeValue}, nil
}

func sanitizePublishDiagnostics(
	value json.RawMessage,
	head SandboxHeadFence,
	documents []DocumentFence,
	limits EffectiveLimits,
	repositoryPaths map[string]struct{},
) (json.RawMessage, DocumentFence, bool, error) {
	fields, err := decodeMethodObject(value, []string{"uri", "version", "diagnostics"}, nil)
	if err != nil {
		return nil, DocumentFence{}, false, fmt.Errorf("%w: %v", ErrServerResultInvalid, err)
	}
	uri, err := decodeCandidateURIForHead(fields["uri"], head, repositoryPaths)
	if err != nil {
		return nil, DocumentFence{}, false, fmt.Errorf("%w: %v", ErrServerResultInvalid, err)
	}
	version, err := decodeServerUint(fields["version"], maxSafeWireInteger)
	if err != nil || version == 0 {
		return nil, DocumentFence{}, false, fmt.Errorf("%w: invalid diagnostic version", ErrServerResultInvalid)
	}
	diagnostics, err := decodeDiagnostics(fields["diagnostics"], limits.MaxDiagnosticsPerDocument)
	if err != nil {
		return nil, DocumentFence{}, false, fmt.Errorf("%w: %v", ErrServerResultInvalid, err)
	}
	result := serverPublishDiagnostics{URI: uri, Version: version, Diagnostics: diagnostics}
	encoded, err := json.Marshal(result)
	if err != nil || int64(len(encoded)) > limits.MaxResultBytes {
		return nil, DocumentFence{}, false, ErrServerMessageTooLarge
	}
	index, found := slices.BinarySearchFunc(documents, uri, func(document DocumentFence, expected string) int {
		return strings.Compare(document.ModelURI, expected)
	})
	if !found {
		return encoded, DocumentFence{}, true, nil
	}
	document := documents[index]
	return encoded, document, document.ModelVersion != version, nil
}

func sanitizeDocumentDiagnostics(value json.RawMessage, limits EffectiveLimits) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	fields, err := decodeMethodObject(value, []string{"kind", "items"}, []string{"resultId"})
	if err != nil {
		return nil, err
	}
	kind, err := decodeRequiredServerString(fields["kind"], 16)
	if err != nil || kind != "full" {
		return nil, errors.New("only full document diagnostic reports are admitted")
	}
	items, err := decodeDiagnostics(fields["items"], limits.MaxDiagnosticsPerDocument)
	if err != nil {
		return nil, err
	}
	result := serverDocumentDiagnosticReport{Kind: kind, Items: items}
	if raw, exists := fields["resultId"]; exists {
		result.ResultID, err = decodeRequiredServerString(raw, 256)
		if err != nil || result.ResultID == "" {
			return nil, errors.New("invalid diagnostic resultId")
		}
	}
	return result, nil
}

func decodeDiagnostics(value json.RawMessage, maximum int) ([]serverDiagnostic, error) {
	raw, err := decodeServerArray(value, maximum)
	if err != nil {
		return nil, err
	}
	result := make([]serverDiagnostic, len(raw))
	for index, encoded := range raw {
		fields, err := decodeMethodObject(
			encoded, []string{"range", "message"}, []string{"severity", "code", "source", "tags"},
		)
		if err != nil {
			return nil, err
		}
		result[index].Range, err = decodeServerRange(fields["range"])
		if err != nil {
			return nil, err
		}
		result[index].Message, err = decodeRequiredServerString(fields["message"], maxServerDetail)
		if err != nil || result[index].Message == "" {
			return nil, errors.New("invalid diagnostic message")
		}
		if encodedSeverity, exists := fields["severity"]; exists {
			severity, err := decodeServerUint(encodedSeverity, 4)
			if err != nil || severity == 0 {
				return nil, errors.New("invalid diagnostic severity")
			}
			result[index].Severity = &severity
		}
		if encodedCode, exists := fields["code"]; exists {
			result[index].Code, err = decodeStringOrInteger(encodedCode, 256)
			if err != nil {
				return nil, errors.New("invalid diagnostic code")
			}
		}
		if encodedSource, exists := fields["source"]; exists {
			result[index].Source, err = decodeRequiredServerString(encodedSource, 256)
			if err != nil || result[index].Source == "" {
				return nil, errors.New("invalid diagnostic source")
			}
		}
		if encodedTags, exists := fields["tags"]; exists {
			result[index].Tags, err = decodeEnumArray(encodedTags, 2, 1, 2)
			if err != nil {
				return nil, errors.New("invalid diagnostic tags")
			}
		}
	}
	return result, nil
}

type serverCompletionList struct {
	IsIncomplete bool                   `json:"isIncomplete"`
	Items        []serverCompletionItem `json:"items"`
}

type serverCompletionItem struct {
	Label            string                `json:"label"`
	Kind             *uint64               `json:"kind,omitempty"`
	Detail           string                `json:"detail,omitempty"`
	Documentation    any                   `json:"documentation,omitempty"`
	SortText         string                `json:"sortText,omitempty"`
	FilterText       string                `json:"filterText,omitempty"`
	InsertText       string                `json:"insertText,omitempty"`
	InsertTextFormat *uint64               `json:"insertTextFormat,omitempty"`
	TextEdit         *serverCompletionEdit `json:"textEdit,omitempty"`
	Preselect        *bool                 `json:"preselect,omitempty"`
	CommitCharacters []string              `json:"commitCharacters,omitempty"`
}

type serverCompletionEdit struct {
	Range   serverRange `json:"range"`
	NewText string      `json:"newText"`
}

func sanitizeCompletion(value json.RawMessage, limits EffectiveLimits) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return nil, errors.New("completion result is empty")
	}
	if trimmed[0] == '[' {
		items, err := decodeCompletionItems(trimmed, limits)
		if err != nil {
			return nil, err
		}
		return items, nil
	}
	fields, err := decodeMethodObject(trimmed, []string{"isIncomplete", "items"}, nil)
	if err != nil {
		return nil, err
	}
	var incomplete bool
	if err := decodeBool(fields["isIncomplete"], &incomplete); err != nil {
		return nil, errors.New("invalid completion isIncomplete")
	}
	items, err := decodeCompletionItems(fields["items"], limits)
	if err != nil {
		return nil, err
	}
	return serverCompletionList{IsIncomplete: incomplete, Items: items}, nil
}

func decodeCompletionItems(value json.RawMessage, limits EffectiveLimits) ([]serverCompletionItem, error) {
	raw, err := decodeServerArray(value, limits.MaxCompletionItems)
	if err != nil {
		return nil, err
	}
	result := make([]serverCompletionItem, len(raw))
	for index, encoded := range raw {
		fields, err := decodeMethodObject(encoded, []string{"label"}, []string{
			"kind", "detail", "documentation", "sortText", "filterText", "insertText",
			"insertTextFormat", "textEdit", "preselect", "commitCharacters",
		})
		if err != nil {
			return nil, err
		}
		item := &result[index]
		item.Label, err = decodeRequiredServerString(fields["label"], maxServerLabel)
		if err != nil || item.Label == "" {
			return nil, errors.New("invalid completion label")
		}
		if rawKind, exists := fields["kind"]; exists {
			kind, err := decodeServerUint(rawKind, 25)
			if err != nil || kind == 0 {
				return nil, errors.New("invalid completion kind")
			}
			item.Kind = &kind
		}
		if rawDetail, exists := fields["detail"]; exists {
			item.Detail, err = decodeRequiredServerString(rawDetail, maxServerDetail)
			if err != nil {
				return nil, errors.New("invalid completion detail")
			}
		}
		if rawDocumentation, exists := fields["documentation"]; exists {
			item.Documentation, err = decodeMarkupOrString(rawDocumentation, maxServerText)
			if err != nil {
				return nil, errors.New("invalid completion documentation")
			}
		}
		for _, field := range []struct {
			name   string
			target *string
			limit  int
		}{
			{name: "sortText", target: &item.SortText, limit: maxServerLabel},
			{name: "filterText", target: &item.FilterText, limit: maxServerLabel},
		} {
			if rawText, exists := fields[field.name]; exists {
				*field.target, err = decodeRequiredServerString(rawText, field.limit)
				if err != nil {
					return nil, fmt.Errorf("invalid completion %s", field.name)
				}
			}
		}
		rawInsert, hasInsert := fields["insertText"]
		rawEdit, hasEdit := fields["textEdit"]
		if hasInsert == hasEdit {
			return nil, errors.New("completion must contain exactly one plain insertText or textEdit")
		}
		if hasInsert {
			item.InsertText, err = decodeRequiredServerString(rawInsert, int(limits.MaxDocumentBytes))
			if err != nil {
				return nil, errors.New("invalid completion insertText")
			}
		} else {
			editFields, err := decodeMethodObject(rawEdit, []string{"range", "newText"}, nil)
			if err != nil {
				return nil, errors.New("invalid completion textEdit")
			}
			editRange, err := decodeServerRange(editFields["range"])
			if err != nil {
				return nil, err
			}
			newText, err := decodeRequiredServerString(editFields["newText"], int(limits.MaxDocumentBytes))
			if err != nil {
				return nil, errors.New("invalid completion newText")
			}
			item.TextEdit = &serverCompletionEdit{Range: editRange, NewText: newText}
		}
		if rawFormat, exists := fields["insertTextFormat"]; exists {
			format, err := decodeServerUint(rawFormat, 2)
			if err != nil || format != 1 {
				return nil, errors.New("snippet completion is forbidden")
			}
			item.InsertTextFormat = &format
		}
		if rawPreselect, exists := fields["preselect"]; exists {
			var preselect bool
			if err := decodeBool(rawPreselect, &preselect); err != nil {
				return nil, errors.New("invalid completion preselect")
			}
			item.Preselect = &preselect
		}
		if rawCharacters, exists := fields["commitCharacters"]; exists {
			item.CommitCharacters, err = decodeStringArray(rawCharacters, 32, 16)
			if err != nil {
				return nil, errors.New("invalid completion commitCharacters")
			}
		}
	}
	return result, nil
}

type serverSignatureHelp struct {
	Signatures      []serverSignatureInformation `json:"signatures"`
	ActiveSignature *uint64                      `json:"activeSignature,omitempty"`
	ActiveParameter *uint64                      `json:"activeParameter,omitempty"`
}

type serverSignatureInformation struct {
	Label         string                       `json:"label"`
	Documentation any                          `json:"documentation,omitempty"`
	Parameters    []serverParameterInformation `json:"parameters,omitempty"`
}

type serverParameterInformation struct {
	Label         any `json:"label"`
	Documentation any `json:"documentation,omitempty"`
}

func sanitizeSignatureHelp(value json.RawMessage, limits EffectiveLimits) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	fields, err := decodeMethodObject(value, []string{"signatures"}, []string{"activeSignature", "activeParameter"})
	if err != nil {
		return nil, err
	}
	maximum := min(limits.MaxCompletionItems, 128)
	rawSignatures, err := decodeServerArray(fields["signatures"], maximum)
	if err != nil {
		return nil, err
	}
	result := serverSignatureHelp{Signatures: make([]serverSignatureInformation, len(rawSignatures))}
	for index, rawSignature := range rawSignatures {
		signatureFields, err := decodeMethodObject(rawSignature, []string{"label"}, []string{"documentation", "parameters"})
		if err != nil {
			return nil, err
		}
		signature := &result.Signatures[index]
		signature.Label, err = decodeRequiredServerString(signatureFields["label"], maxServerDetail)
		if err != nil || signature.Label == "" {
			return nil, errors.New("invalid signature label")
		}
		if rawDocumentation, exists := signatureFields["documentation"]; exists {
			signature.Documentation, err = decodeMarkupOrString(rawDocumentation, maxServerText)
			if err != nil {
				return nil, errors.New("invalid signature documentation")
			}
		}
		if rawParameters, exists := signatureFields["parameters"]; exists {
			rawValues, err := decodeServerArray(rawParameters, 256)
			if err != nil {
				return nil, err
			}
			signature.Parameters = make([]serverParameterInformation, len(rawValues))
			for parameterIndex, rawParameter := range rawValues {
				parameterFields, err := decodeMethodObject(rawParameter, []string{"label"}, []string{"documentation"})
				if err != nil {
					return nil, err
				}
				parameter := &signature.Parameters[parameterIndex]
				parameter.Label, err = decodeParameterLabel(parameterFields["label"], signature.Label)
				if err != nil {
					return nil, err
				}
				if rawDocumentation, exists := parameterFields["documentation"]; exists {
					parameter.Documentation, err = decodeMarkupOrString(rawDocumentation, maxServerText)
					if err != nil {
						return nil, errors.New("invalid parameter documentation")
					}
				}
			}
		}
	}
	activeSignature := uint64(0)
	if rawActive, exists := fields["activeSignature"]; exists {
		active, err := decodeServerUint(rawActive, uint64(maximum))
		if err != nil || int(active) >= len(result.Signatures) {
			return nil, errors.New("invalid activeSignature")
		}
		activeSignature = active
		result.ActiveSignature = &active
	}
	if rawActive, exists := fields["activeParameter"]; exists {
		active, err := decodeServerUint(rawActive, 255)
		if err != nil {
			return nil, errors.New("invalid activeParameter")
		}
		if len(result.Signatures) == 0 {
			return nil, errors.New("activeParameter requires an active signature")
		}
		signature := result.Signatures[activeSignature]
		if int(active) >= len(signature.Parameters) {
			return nil, errors.New("activeParameter exceeds active signature")
		}
		result.ActiveParameter = &active
	}
	return result, nil
}

func decodeParameterLabel(value json.RawMessage, signature string) (any, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		label, err := decodeRequiredServerString(trimmed, maxServerLabel)
		if err != nil || label == "" {
			return nil, errors.New("invalid parameter label")
		}
		return label, nil
	}
	raw, err := decodeServerArray(trimmed, 2)
	if err != nil || len(raw) != 2 {
		return nil, errors.New("invalid parameter label offsets")
	}
	start, err := decodeServerUint(raw[0], uint64(len(signature)))
	if err != nil {
		return nil, errors.New("invalid parameter label start")
	}
	end, err := decodeServerUint(raw[1], uint64(len(signature)))
	if err != nil || end < start {
		return nil, errors.New("invalid parameter label end")
	}
	return []uint64{start, end}, nil
}

type serverDocumentHighlight struct {
	Range serverRange `json:"range"`
	Kind  *uint64     `json:"kind,omitempty"`
}

func sanitizeDocumentHighlights(value json.RawMessage, limits EffectiveLimits) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	raw, err := decodeServerArray(value, limits.MaxNavigationLocations)
	if err != nil {
		return nil, err
	}
	result := make([]serverDocumentHighlight, len(raw))
	for index, encoded := range raw {
		fields, err := decodeMethodObject(encoded, []string{"range"}, []string{"kind"})
		if err != nil {
			return nil, err
		}
		result[index].Range, err = decodeServerRange(fields["range"])
		if err != nil {
			return nil, err
		}
		if rawKind, exists := fields["kind"]; exists {
			kind, err := decodeServerUint(rawKind, 3)
			if err != nil || kind == 0 {
				return nil, errors.New("invalid document highlight kind")
			}
			result[index].Kind = &kind
		}
	}
	return result, nil
}

type serverDocumentSymbol struct {
	Name           string                 `json:"name"`
	Detail         string                 `json:"detail,omitempty"`
	Kind           uint64                 `json:"kind"`
	Tags           []uint64               `json:"tags,omitempty"`
	Range          serverRange            `json:"range"`
	SelectionRange serverRange            `json:"selectionRange"`
	Children       []serverDocumentSymbol `json:"children,omitempty"`
}

func sanitizeDocumentSymbols(value json.RawMessage, limits EffectiveLimits) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	remaining := limits.MaxNavigationLocations
	return decodeDocumentSymbolArray(value, 0, &remaining)
}

func decodeDocumentSymbolArray(value json.RawMessage, depth int, remaining *int) ([]serverDocumentSymbol, error) {
	if depth > 8 || remaining == nil || *remaining < 0 {
		return nil, errors.New("document symbol nesting exceeds limit")
	}
	raw, err := decodeServerArray(value, *remaining)
	if err != nil {
		return nil, err
	}
	*remaining -= len(raw)
	result := make([]serverDocumentSymbol, len(raw))
	for index, encoded := range raw {
		fields, err := decodeMethodObject(encoded, []string{
			"name", "kind", "range", "selectionRange",
		}, []string{"detail", "tags", "children"})
		if err != nil {
			return nil, err
		}
		symbol := &result[index]
		symbol.Name, err = decodeRequiredServerString(fields["name"], maxServerLabel)
		if err != nil || symbol.Name == "" {
			return nil, errors.New("invalid document symbol name")
		}
		if rawDetail, exists := fields["detail"]; exists {
			symbol.Detail, err = decodeRequiredServerString(rawDetail, maxServerDetail)
			if err != nil {
				return nil, errors.New("invalid document symbol detail")
			}
		}
		symbol.Kind, err = decodeServerUint(fields["kind"], 26)
		if err != nil || symbol.Kind == 0 {
			return nil, errors.New("invalid document symbol kind")
		}
		symbol.Range, err = decodeServerRange(fields["range"])
		if err != nil {
			return nil, err
		}
		symbol.SelectionRange, err = decodeServerRange(fields["selectionRange"])
		if err != nil || !serverRangeContains(symbol.Range, symbol.SelectionRange) {
			return nil, errors.New("selectionRange must be within symbol range")
		}
		if rawTags, exists := fields["tags"]; exists {
			symbol.Tags, err = decodeEnumArray(rawTags, 1, 1, 1)
			if err != nil {
				return nil, errors.New("invalid document symbol tags")
			}
		}
		if rawChildren, exists := fields["children"]; exists {
			symbol.Children, err = decodeDocumentSymbolArray(rawChildren, depth+1, remaining)
			if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

type serverSemanticTokens struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint64 `json:"data"`
}

func sanitizeSemanticTokens(value json.RawMessage, limits EffectiveLimits) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	fields, err := decodeMethodObject(value, []string{"data"}, []string{"resultId"})
	if err != nil {
		return nil, err
	}
	maximum := limits.MaxNavigationLocations * 5
	rawData, err := decodeServerArray(fields["data"], maximum)
	if err != nil || len(rawData)%5 != 0 {
		return nil, errors.New("semantic token data must be bounded five-tuples")
	}
	result := serverSemanticTokens{Data: make([]uint64, len(rawData))}
	for index, raw := range rawData {
		result.Data[index], err = decodeServerUint(raw, uint64(^uint32(0)))
		if err != nil {
			return nil, errors.New("invalid semantic token integer")
		}
	}
	if rawResultID, exists := fields["resultId"]; exists {
		result.ResultID, err = decodeRequiredServerString(rawResultID, 256)
		if err != nil || result.ResultID == "" {
			return nil, errors.New("invalid semantic token resultId")
		}
	}
	return result, nil
}

type serverInlayHint struct {
	Position     BrowserPosition `json:"position"`
	Label        string          `json:"label"`
	Kind         *uint64         `json:"kind,omitempty"`
	Tooltip      any             `json:"tooltip,omitempty"`
	PaddingLeft  *bool           `json:"paddingLeft,omitempty"`
	PaddingRight *bool           `json:"paddingRight,omitempty"`
}

func sanitizeInlayHints(value json.RawMessage, limits EffectiveLimits) (any, error) {
	if isJSONNull(value) {
		return nil, nil
	}
	raw, err := decodeServerArray(value, limits.MaxNavigationLocations)
	if err != nil {
		return nil, err
	}
	result := make([]serverInlayHint, len(raw))
	for index, encoded := range raw {
		fields, err := decodeMethodObject(encoded, []string{"position", "label"}, []string{
			"kind", "tooltip", "paddingLeft", "paddingRight",
		})
		if err != nil {
			return nil, err
		}
		hint := &result[index]
		hint.Position, err = decodeServerPosition(fields["position"])
		if err != nil {
			return nil, err
		}
		// Label parts can carry locations and commands. Production v1 admits
		// only inert string labels.
		hint.Label, err = decodeRequiredServerString(fields["label"], maxServerLabel)
		if err != nil || hint.Label == "" {
			return nil, errors.New("invalid inlay hint label")
		}
		if rawKind, exists := fields["kind"]; exists {
			kind, err := decodeServerUint(rawKind, 2)
			if err != nil || kind == 0 {
				return nil, errors.New("invalid inlay hint kind")
			}
			hint.Kind = &kind
		}
		if rawTooltip, exists := fields["tooltip"]; exists {
			hint.Tooltip, err = decodeMarkupOrString(rawTooltip, maxServerText)
			if err != nil {
				return nil, errors.New("invalid inlay hint tooltip")
			}
		}
		for _, optional := range []struct {
			name   string
			target **bool
		}{{"paddingLeft", &hint.PaddingLeft}, {"paddingRight", &hint.PaddingRight}} {
			if rawPadding, exists := fields[optional.name]; exists {
				var padding bool
				if err := decodeBool(rawPadding, &padding); err != nil {
					return nil, fmt.Errorf("invalid inlay hint %s", optional.name)
				}
				*optional.target = &padding
			}
		}
	}
	return result, nil
}

func decodeServerPosition(value json.RawMessage) (BrowserPosition, error) {
	fields, err := decodeMethodObject(value, []string{"line", "character"}, nil)
	if err != nil {
		return BrowserPosition{}, err
	}
	line, err := decodeServerUint(fields["line"], maxLSPPositionValue)
	if err != nil {
		return BrowserPosition{}, errors.New("invalid position line")
	}
	character, err := decodeServerUint(fields["character"], maxLSPPositionValue)
	if err != nil {
		return BrowserPosition{}, errors.New("invalid position character")
	}
	return BrowserPosition{Line: line, Character: character}, nil
}

func decodeServerRange(value json.RawMessage) (serverRange, error) {
	fields, err := decodeMethodObject(value, []string{"start", "end"}, nil)
	if err != nil {
		return serverRange{}, err
	}
	start, err := decodeServerPosition(fields["start"])
	if err != nil {
		return serverRange{}, err
	}
	end, err := decodeServerPosition(fields["end"])
	if err != nil || compareServerPositions(start, end) > 0 {
		return serverRange{}, errors.New("range end precedes start")
	}
	return serverRange{Start: start, End: end}, nil
}

func compareServerPositions(left, right BrowserPosition) int {
	if left.Line < right.Line || (left.Line == right.Line && left.Character < right.Character) {
		return -1
	}
	if left == right {
		return 0
	}
	return 1
}

func serverRangeContains(outer, inner serverRange) bool {
	return compareServerPositions(outer.Start, inner.Start) <= 0 &&
		compareServerPositions(inner.End, outer.End) <= 0
}

func decodeCandidateURIForHead(
	value json.RawMessage,
	head SandboxHeadFence,
	repositoryPaths map[string]struct{},
) (string, error) {
	uri, err := decodeRequiredServerString(value, 1_024)
	if err != nil {
		return "", errors.New("invalid server document URI")
	}
	modelURI, err := CandidateDocumentURI(uri, head)
	if err != nil {
		return "", errors.New("foreign or non-canonical server document URI")
	}
	identity, err := ParseCandidateModelURI(modelURI)
	if err != nil {
		return "", errors.New("invalid translated Candidate URI")
	}
	if _, exists := repositoryPaths[identity.Path]; !exists {
		return "", errors.New("server document URI is absent from the exact Candidate tree")
	}
	return modelURI, nil
}

func decodeMarkupOrString(value json.RawMessage, maximum int) (any, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("markup or string is required")
	}
	if trimmed[0] == '"' {
		return decodeRequiredServerString(trimmed, maximum)
	}
	fields, err := decodeMethodObject(trimmed, []string{"kind", "value"}, nil)
	if err != nil {
		return nil, err
	}
	kind, err := decodeRequiredServerString(fields["kind"], 16)
	// The initialize request advertises plaintext only. Accepting markdown here
	// would widen the negotiated capability and create a second rendering/XSS
	// surface in the browser.
	if err != nil || kind != "plaintext" {
		return nil, errors.New("unsupported markup kind")
	}
	text, err := decodeRequiredServerString(fields["value"], maximum)
	if err != nil {
		return nil, errors.New("invalid markup value")
	}
	return serverMarkupContent{Kind: kind, Value: text}, nil
}

func decodeServerArray(value json.RawMessage, maximum int) ([]json.RawMessage, error) {
	if maximum < 0 || len(value) == 0 || isJSONNull(value) {
		return nil, errors.New("array is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, errors.New("array is required")
	}
	result := make([]json.RawMessage, 0, min(maximum, 32))
	for decoder.More() {
		if len(result) >= maximum {
			return nil, errors.New("array exceeds limit")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		result = append(result, slices.Clone(raw))
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, errors.New("unterminated array")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple values are forbidden")
	}
	if result == nil {
		result = []json.RawMessage{}
	}
	return result, nil
}

func decodeServerUint(value json.RawMessage, maximum uint64) (uint64, error) {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || strings.HasPrefix(trimmed, "-") || !validSafeIntegerLexeme(trimmed) {
		return 0, errors.New("unsigned integer is required")
	}
	parsed, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil || parsed > maximum {
		return 0, errors.New("unsigned integer exceeds limit")
	}
	return parsed, nil
}

func decodeServerInt(value json.RawMessage) (int64, error) {
	trimmed := strings.TrimSpace(string(value))
	if !validSafeIntegerLexeme(trimmed) {
		return 0, errors.New("integer is required")
	}
	return strconv.ParseInt(trimmed, 10, 64)
}

func decodeStringOrInteger(value json.RawMessage, maximum int) (any, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return nil, errors.New("string or integer is required")
	}
	if trimmed[0] == '"' {
		return decodeRequiredServerString(trimmed, maximum)
	}
	integer, err := decodeServerInt(trimmed)
	if err != nil || integer < -2_147_483_648 || integer > 2_147_483_647 {
		return nil, errors.New("integer exceeds signed 32-bit limit")
	}
	return integer, nil
}

func decodeEnumArray(value json.RawMessage, maximum int, lower, upper uint64) ([]uint64, error) {
	raw, err := decodeServerArray(value, maximum)
	if err != nil {
		return nil, err
	}
	result := make([]uint64, len(raw))
	seen := map[uint64]bool{}
	for index, encoded := range raw {
		result[index], err = decodeServerUint(encoded, upper)
		if err != nil || result[index] < lower || seen[result[index]] {
			return nil, errors.New("invalid or duplicate enum")
		}
		seen[result[index]] = true
	}
	return result, nil
}

func decodeStringArray(value json.RawMessage, maximum, maximumString int) ([]string, error) {
	raw, err := decodeServerArray(value, maximum)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(raw))
	seen := map[string]bool{}
	for index, encoded := range raw {
		result[index], err = decodeRequiredServerString(encoded, maximumString)
		if err != nil || result[index] == "" || seen[result[index]] {
			return nil, errors.New("invalid or duplicate string")
		}
		seen[result[index]] = true
	}
	return result, nil
}

func isJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
