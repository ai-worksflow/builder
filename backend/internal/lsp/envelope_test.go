package lsp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

const (
	envelopeBindingID = "20000000-0000-4000-8000-000000000001"
	envelopeRequestID = "20000000-0000-4000-8000-000000000002"
)

func envelopeContentDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func envelopeFixture(t *testing.T, content string) (*EnvelopeProtocol, SandboxHeadFence, DocumentFence) {
	t.Helper()
	head := validHead()
	uri, err := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/page.ts")
	if err != nil {
		t.Fatal(err)
	}
	document := DocumentFence{
		ModelURI: uri, OpenID: testOpen, ModelVersion: 1, SavedContentHash: envelopeContentDigest(content),
	}
	protocol, err := NewEnvelopeProtocol(
		testConnection, envelopeBindingID, head, lspTestProfile("typescript"), []DocumentFence{document},
	)
	if err != nil {
		t.Fatal(err)
	}
	return protocol, head, document
}

func clientEnvelopeJSON(
	t *testing.T,
	kind string,
	sequence uint64,
	head SandboxHeadFence,
	document *DocumentFence,
	payload any,
) []byte {
	t.Helper()
	messageID := clientEnvelopeMessageID(sequence)
	method := clientEnvelopeMethod(kind)
	var replyTo *string
	if kind == ClientEnvelopeRequest {
		legacy, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("request test payload must expose its intended identity: %#v", payload)
		}
		requestID, ok := legacy["requestId"].(string)
		if !ok {
			t.Fatalf("request test payload requestId = %#v", legacy["requestId"])
		}
		requestMethod, ok := legacy["method"].(string)
		if !ok {
			t.Fatalf("request test payload method = %#v", legacy["method"])
		}
		messageID, method = requestID, requestMethod
		payload = map[string]any{"params": legacy["params"]}
	}
	if kind == ClientEnvelopeCancel {
		cancel, ok := payload.(CancelEnvelopePayload)
		if !ok {
			t.Fatalf("cancel test payload = %#v", payload)
		}
		replyTo = &cancel.ReplyTo
		payload = EmptyEnvelopePayload{}
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	value := struct {
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
	}{
		SchemaVersion: EnvelopeSchemaVersion, ConnectionID: testConnection,
		BindingID: envelopeBindingID, Sequence: sequence, MessageID: messageID,
		ReplyTo: replyTo, Kind: kind, Method: method, Head: head,
		Document: document, Payload: encodedPayload,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func clientEnvelopeMessageID(sequence uint64) string {
	return fmt.Sprintf("21000000-0000-4000-8000-%012d", sequence)
}

func clientEnvelopeMethod(kind string) string {
	switch kind {
	case ClientEnvelopeDocumentOpen:
		return EnvelopeMethodDocumentOpen
	case ClientEnvelopeDocumentChange:
		return EnvelopeMethodDocumentChange
	case ClientEnvelopeDocumentSave:
		return EnvelopeMethodDocumentSave
	case ClientEnvelopeDocumentClose:
		return EnvelopeMethodDocumentClose
	case ClientEnvelopeCancel:
		return EnvelopeMethodCancel
	case ClientEnvelopeHeadRebind:
		return EnvelopeMethodHeadRebind
	case ClientEnvelopePing:
		return EnvelopeMethodPing
	default:
		return ""
	}
}

func openEnvelopeDocument(
	t *testing.T,
	protocol *EnvelopeProtocol,
	head SandboxHeadFence,
	document DocumentFence,
	content string,
) {
	t.Helper()
	message, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, head, &document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: content},
	))
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if message.Kind != ClientEnvelopeDocumentOpen || message.Sequence != 2 ||
		message.Document == nil || !message.Document.Equal(document) {
		t.Fatalf("open envelope drifted: %#v", message)
	}
}

func TestClientEnvelopeFullTextLifecycleIsExactlyFenced(t *testing.T) {
	protocol, head, document := envelopeFixture(t, "const first = 1\n")
	openEnvelopeDocument(t, protocol, head, document, "const first = 1\n")

	changed := document
	changed.ModelVersion++
	message, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeDocumentChange, 3, head, &changed,
		DocumentChangeEnvelopePayload{Text: "const second = 2\n"},
	))
	if err != nil {
		t.Fatal(err)
	}
	change, ok := message.Payload.(DocumentChangeEnvelopePayload)
	if !ok || change.Text != "const second = 2\n" {
		t.Fatalf("change payload = %#v", message.Payload)
	}

	requestParams := map[string]any{
		"textDocument": map[string]any{"uri": document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 6},
	}
	request, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeRequest, 4, head, &changed,
		map[string]any{"requestId": envelopeRequestID, "method": "textDocument/hover", "params": requestParams},
	))
	if err != nil {
		t.Fatal(err)
	}
	requestPayload, ok := request.Payload.(RequestEnvelopePayload)
	if !ok || request.MessageID != envelopeRequestID || request.Method != "textDocument/hover" ||
		requestPayload.Params == nil {
		t.Fatalf("request payload = %#v", request.Payload)
	}
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeCancel, 5, head, &changed, CancelEnvelopePayload{ReplyTo: envelopeRequestID},
	)); err != nil {
		t.Fatal(err)
	}

	saved := changed
	saved.SavedContentHash = envelopeContentDigest("const second = 2\n")
	ping, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopePing, 6, head, nil, PingEnvelopePayload{Nonce: "ping-6"},
	))
	if err != nil || ping.Document != nil {
		t.Fatalf("ping = %#v, %v", ping, err)
	}

	next := head
	next.Version++
	next.JournalSequence++
	next.TreeHash = lspDigest("c")
	rebind, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeHeadRebind, 7, next, nil,
		HeadRebindEnvelopePayload{Documents: []DocumentFence{saved}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if !rebind.Head.Equal(next) {
		t.Fatalf("rebind head = %#v", rebind.Head)
	}
	currentHead, currentDocuments := protocol.CurrentHeadAndDocuments()
	if !currentHead.Equal(next) || len(currentDocuments) != 1 || !currentDocuments[0].Equal(saved) {
		t.Fatalf("current projection = %#v %#v", currentHead, currentDocuments)
	}
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeDocumentClose, 8, next, &saved, EmptyEnvelopePayload{},
	)); err != nil {
		t.Fatal(err)
	}
	_, currentDocuments = protocol.CurrentHeadAndDocuments()
	if len(currentDocuments) != 0 {
		t.Fatalf("closed document retained: %#v", currentDocuments)
	}
}

func TestClientEnvelopeRejectsWireDriftBeforeStateMutation(t *testing.T) {
	validFrame := func(t *testing.T) []byte {
		_, head, _ := envelopeFixture(t, "x")
		return clientEnvelopeJSON(t, ClientEnvelopePing, 2, head, nil, PingEnvelopePayload{Nonce: "n-2"})
	}

	for name, mutate := range map[string]func([]byte) []byte{
		"unknown top field": func(value []byte) []byte {
			return []byte(strings.TrimSuffix(string(value), "}") + `,"shadow":true}`)
		},
		"duplicate top field": func(value []byte) []byte {
			return []byte(strings.Replace(
				string(value), `"kind":"client.ping"`, `"kind":"client.ping","kind":"client.ping"`, 1,
			))
		},
		"null kind": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"kind":"client.ping"`, `"kind":null`, 1))
		},
		"alias": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"connectionId"`, `"connection_id"`, 1))
		},
		"float sequence": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"sequence":2`, `"sequence":2.0`, 1))
		},
		"skipped sequence": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"sequence":2`, `"sequence":3`, 1))
		},
		"old connection": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), testConnection, testSession, 1))
		},
		"old binding": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), envelopeBindingID, testSession, 1))
		},
		"missing message ID": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"messageId"`, `"message_id"`, 1))
		},
		"message ID aliases connection": func(value []byte) []byte {
			return []byte(strings.Replace(
				string(value), clientEnvelopeMessageID(2), testConnection, 1,
			))
		},
		"invalid message ID": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), clientEnvelopeMessageID(2), "not-a-uuid", 1))
		},
		"replyTo not allowed": func(value []byte) []byte {
			return []byte(strings.Replace(
				string(value), `"replyTo":null`, `"replyTo":"`+envelopeRequestID+`"`, 1,
			))
		},
		"method drift": func(value []byte) []byte {
			return []byte(strings.Replace(
				string(value), `"method":"worksflow/ping"`, `"method":"worksflow/pong"`, 1,
			))
		},
		"undirected kind": func(value []byte) []byte {
			return []byte(strings.Replace(
				string(value), `"kind":"client.ping"`, `"kind":"ping"`, 1,
			))
		},
		"nested duplicate": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), `"payload":{"nonce":"n-2"}`, `"payload":{"nonce":"n-2","nonce":"n-2"}`, 1))
		},
		"deep payload": func(value []byte) []byte {
			deep := strings.Repeat(`{"x":`, 18) + `true` + strings.Repeat(`}`, 18)
			return []byte(strings.Replace(string(value), `{"nonce":"n-2"}`, deep, 1))
		},
		"oversized frame": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "n-2", strings.Repeat("n", 513<<10), 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			protocol, _, _ := envelopeFixture(t, "x")
			if _, err := protocol.DecodeClientEnvelope(mutate(validFrame(t))); err == nil {
				t.Fatal("wire drift was accepted")
			}
			// The rejected frame must not burn sequence 2.
			if _, err := protocol.DecodeClientEnvelope(validFrame(t)); err != nil {
				t.Fatalf("invalid frame partially mutated sequence: %v", err)
			}
		})
	}
}

func TestClientEnvelopeUsesOnlyMessageIDAndReplyToForCorrelation(t *testing.T) {
	protocol, head, document := envelopeFixture(t, "a")
	openEnvelopeDocument(t, protocol, head, document, "a")
	params := map[string]any{
		"textDocument": map[string]any{"uri": document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 0},
	}
	requestFrame := clientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, head, &document,
		map[string]any{
			"requestId": envelopeRequestID, "method": "textDocument/hover", "params": params,
		},
	)
	requestFields, err := decodeExactObject(requestFrame, []string{
		"schemaVersion", "connectionId", "bindingId", "sequence", "messageId",
		"replyTo", "kind", "method", "sandboxHeadFence", "documentFence", "payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	var messageID, method string
	if decodeString(requestFields["messageId"], &messageID) != nil || messageID != envelopeRequestID ||
		decodeString(requestFields["method"], &method) != nil || method != "textDocument/hover" ||
		!isEnvelopeJSONNull(requestFields["replyTo"]) {
		t.Fatalf("request common correlation drifted: %s", requestFrame)
	}
	if _, err := decodeExactObject(requestFields["payload"], []string{"params"}); err != nil {
		t.Fatalf("request payload contains correlation identity: %v\n%s", err, requestFrame)
	}
	request, err := protocol.DecodeClientEnvelope(requestFrame)
	if err != nil || request.MessageID != envelopeRequestID || request.Method != "textDocument/hover" {
		t.Fatalf("request admission = %#v, %v", request, err)
	}

	cancelFrame := clientEnvelopeJSON(
		t, ClientEnvelopeCancel, 4, head, &document,
		CancelEnvelopePayload{ReplyTo: envelopeRequestID},
	)
	cancelFields, err := decodeExactObject(cancelFrame, []string{
		"schemaVersion", "connectionId", "bindingId", "sequence", "messageId",
		"replyTo", "kind", "method", "sandboxHeadFence", "documentFence", "payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	replyTo, err := decodeEnvelopeReplyTo(cancelFields["replyTo"])
	if err != nil || replyTo == nil || *replyTo != envelopeRequestID ||
		decodeEmptyEnvelopePayload(cancelFields["payload"]) != nil {
		t.Fatalf("cancel common correlation drifted: %s", cancelFrame)
	}
	if _, err := protocol.DecodeClientEnvelope(cancelFrame); err != nil {
		t.Fatalf("cancel admission = %v", err)
	}

	legacyProtocol, legacyHead, legacyDocument := envelopeFixture(t, "a")
	openEnvelopeDocument(t, legacyProtocol, legacyHead, legacyDocument, "a")
	legacyFrame := clientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, legacyHead, &legacyDocument,
		map[string]any{
			"requestId": envelopeRequestID, "method": "textDocument/hover", "params": params,
		},
	)
	legacyFrame = []byte(strings.Replace(
		string(legacyFrame), `"payload":{"params":`,
		`"payload":{"requestId":"`+envelopeRequestID+`","method":"textDocument/hover","params":`, 1,
	))
	if _, err := legacyProtocol.DecodeClientEnvelope(legacyFrame); !errors.Is(err, ErrEnvelopeMalformed) {
		t.Fatalf("legacy payload correlation = %v\n%s", err, legacyFrame)
	}
}

func TestClientEnvelopeRejectsReusedMessageIDEvenAtNextSequence(t *testing.T) {
	protocol, head, _ := envelopeFixture(t, "x")
	first := clientEnvelopeJSON(
		t, ClientEnvelopePing, 2, head, nil, PingEnvelopePayload{Nonce: "first"},
	)
	if _, err := protocol.DecodeClientEnvelope(first); err != nil {
		t.Fatal(err)
	}
	second := clientEnvelopeJSON(
		t, ClientEnvelopePing, 3, head, nil, PingEnvelopePayload{Nonce: "second"},
	)
	second = []byte(strings.Replace(
		string(second), clientEnvelopeMessageID(3), clientEnvelopeMessageID(2), 1,
	))
	if _, err := protocol.DecodeClientEnvelope(second); !errors.Is(err, ErrEnvelopeRequestUnknown) {
		t.Fatalf("reused message ID = %v", err)
	}
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopePing, 3, head, nil, PingEnvelopePayload{Nonce: "second"},
	)); err != nil {
		t.Fatalf("reused ID burned sequence: %v", err)
	}
}

func TestClientEnvelopeRejectsDocumentRequestAndRebindDrift(t *testing.T) {
	t.Run("unpaired UTF-16 escape is not repaired into source text", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "x")
		valid := clientEnvelopeJSON(
			t, ClientEnvelopeDocumentOpen, 2, head, &document,
			DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: "x"},
		)
		invalid := []byte(strings.Replace(string(valid), `"text":"x"`, `"text":"\ud800"`, 1))
		if _, err := protocol.DecodeClientEnvelope(invalid); !errors.Is(err, ErrEnvelopeMalformed) {
			t.Fatalf("unpaired surrogate = %v", err)
		}
	})

	t.Run("profile file glob is exact authority", func(t *testing.T) {
		head := validHead()
		uri, err := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/api/main.go")
		if err != nil {
			t.Fatal(err)
		}
		document := DocumentFence{
			ModelURI: uri, OpenID: testOpen, ModelVersion: 1, SavedContentHash: envelopeContentDigest("package main"),
		}
		if _, err := NewEnvelopeProtocol(
			testConnection, envelopeBindingID, head, lspTestProfile("typescript"), []DocumentFence{document},
		); !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("foreign profile path = %v", err)
		}
	})

	t.Run("open hash does not commit to full text", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "expected")
		_, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeDocumentOpen, 2, head, &document,
			DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: "fabricated"},
		))
		if !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("hash mismatch = %v", err)
		}
	})

	t.Run("change must be version-monotonic full text", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "a")
		openEnvelopeDocument(t, protocol, head, document, "a")
		invalid := clientEnvelopeJSON(
			t, ClientEnvelopeDocumentChange, 3, head, &document, map[string]any{"changes": []any{}},
		)
		if _, err := protocol.DecodeClientEnvelope(invalid); err == nil {
			t.Fatal("incremental/unchanged model version accepted")
		}
	})

	t.Run("save must carry a new content commitment", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "a")
		openEnvelopeDocument(t, protocol, head, document, "a")
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeDocumentSave, 3, head, &document, EmptyEnvelopePayload{},
		)); !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("no-op saved fence = %v", err)
		}
	})

	t.Run("request URI and method are exact", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "a")
		openEnvelopeDocument(t, protocol, head, document, "a")
		foreignURI, _ := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/other.ts")
		payload := map[string]any{
			"requestId": envelopeRequestID, "method": "textDocument/hover",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": foreignURI},
				"position":     map[string]any{"line": 0, "character": 0},
			},
		}
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeRequest, 3, head, &document, payload,
		)); err == nil {
			t.Fatal("foreign request URI accepted")
		}
		payload["method"] = "workspace/applyEdit"
		payload["params"] = map[string]any{}
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeRequest, 3, head, &document, payload,
		)); err == nil {
			t.Fatal("write method accepted")
		}
	})

	t.Run("cancel only acts on exact pending request", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "a")
		openEnvelopeDocument(t, protocol, head, document, "a")
		unknown := "30000000-0000-4000-8000-000000000001"
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeCancel, 3, head, &document, CancelEnvelopePayload{ReplyTo: unknown},
		)); !errors.Is(err, ErrEnvelopeRequestUnknown) {
			t.Fatalf("unknown cancel = %v", err)
		}
	})

	t.Run("rebind documents are exact and sorted", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "a")
		openEnvelopeDocument(t, protocol, head, document, "a")
		next := head
		next.Version++
		next.JournalSequence++
		next.TreeHash = lspDigest("d")
		missing := HeadRebindEnvelopePayload{Documents: []DocumentFence{}}
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeHeadRebind, 3, next, nil, missing,
		)); !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("missing rebind document = %v", err)
		}
		next.Version = head.Version + 2
		next.JournalSequence = head.JournalSequence + 1
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeHeadRebind, 3, next, nil,
			HeadRebindEnvelopePayload{Documents: []DocumentFence{document}},
		)); !errors.Is(err, ErrEnvelopeHeadStale) {
			t.Fatalf("non-monotonic rebind = %v", err)
		}
	})

	t.Run("rebind saved hash must commit to synchronized full text", func(t *testing.T) {
		protocol, head, document := envelopeFixture(t, "a")
		next := head
		next.Version++
		next.JournalSequence++
		next.TreeHash = lspDigest("d")
		unsynced := document
		unsynced.SavedContentHash = envelopeContentDigest("b")
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeHeadRebind, 2, next, nil,
			HeadRebindEnvelopePayload{Documents: []DocumentFence{unsynced}},
		)); !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("unsynced saved hash transition = %v", err)
		}

		openEnvelopeDocument(t, protocol, head, document, "a")
		changed := document
		changed.ModelVersion++
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeDocumentChange, 3, head, &changed,
			DocumentChangeEnvelopePayload{Text: "b"},
		)); err != nil {
			t.Fatal(err)
		}
		wrongHash := changed
		wrongHash.SavedContentHash = envelopeContentDigest("c")
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeHeadRebind, 4, next, nil,
			HeadRebindEnvelopePayload{Documents: []DocumentFence{wrongHash}},
		)); !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("mismatched saved hash transition = %v", err)
		}
		wrongVersion := changed
		wrongVersion.ModelVersion++
		wrongVersion.SavedContentHash = envelopeContentDigest("b")
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeHeadRebind, 4, next, nil,
			HeadRebindEnvelopePayload{Documents: []DocumentFence{wrongVersion}},
		)); !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("modelVersion transition = %v", err)
		}
	})

	t.Run("rebind rejects an unsorted complete projection", func(t *testing.T) {
		head := validHead()
		firstURI, _ := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/a.ts")
		secondURI, _ := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/z.ts")
		first := DocumentFence{
			ModelURI: firstURI, OpenID: testOpen, ModelVersion: 1, SavedContentHash: envelopeContentDigest("a"),
		}
		second := DocumentFence{
			ModelURI: secondURI, OpenID: "40000000-0000-4000-8000-000000000001",
			ModelVersion: 1, SavedContentHash: envelopeContentDigest("z"),
		}
		protocol, err := NewEnvelopeProtocol(
			testConnection, envelopeBindingID, head, lspTestProfile("typescript"),
			[]DocumentFence{first, second},
		)
		if err != nil {
			t.Fatal(err)
		}
		openEnvelopeDocument(t, protocol, head, first, "a")
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeDocumentOpen, 3, head, &second,
			DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: "z"},
		)); err != nil {
			t.Fatal(err)
		}
		next := head
		next.Version++
		next.JournalSequence++
		next.TreeHash = lspDigest("e")
		if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
			t, ClientEnvelopeHeadRebind, 4, next, nil,
			HeadRebindEnvelopePayload{Documents: []DocumentFence{second, first}},
		)); !errors.Is(err, ErrEnvelopeDocumentStale) {
			t.Fatalf("unsorted rebind projection = %v", err)
		}
	})
}

func TestServerEnvelopeConstructionAndStrictBrowserValidation(t *testing.T) {
	if result, responseError, err := validateServerResponseBody(
		json.RawMessage("null"), nil, lspTestLimits(),
	); err != nil || string(result) != "null" || responseError != nil {
		t.Fatalf("successful null result = %s, %#v, %v", result, responseError, err)
	}
	protocol, head, document := envelopeFixture(t, "const value = 1\n")
	openEnvelopeDocument(t, protocol, head, document, "const value = 1\n")
	params := map[string]any{
		"textDocument": map[string]any{"uri": document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 0},
	}
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, head, &document,
		map[string]any{"requestId": envelopeRequestID, "method": "textDocument/hover", "params": params},
	)); err != nil {
		t.Fatal(err)
	}
	response, err := protocol.BuildResponseEnvelope(envelopeRequestID, json.RawMessage(`{"contents":"safe"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	responseJSON, _ := response.MarshalJSONStrict(lspTestLimits().MaxFrameBytes)
	decoded, err := DecodeServerEnvelope(responseJSON, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: envelopeBindingID, ExpectedSequence: 2,
		ReplyTo: response.ReplyTo,
		Head:    head, Document: &document, Method: "textDocument/hover", Limits: lspTestLimits(),
	})
	if err != nil || decoded.Document == nil || !decoded.Document.Equal(document) {
		t.Fatalf("response decode = %#v, %v", decoded, err)
	}
	if response.ReplyTo == nil || *response.ReplyTo != envelopeRequestID ||
		response.Method != "textDocument/hover" || response.MessageID == envelopeRequestID {
		t.Fatalf("response common correlation = %#v", response)
	}
	if _, err := decodeExactObject(response.Payload, []string{"status", "result", "error"}); err != nil {
		t.Fatalf("response payload contains duplicate correlation identity: %v\n%s", err, response.Payload)
	}
	if _, err := protocol.BuildResponseEnvelope(envelopeRequestID, json.RawMessage(`{}`), nil); !errors.Is(err, ErrEnvelopeRequestUnknown) {
		t.Fatalf("duplicate response = %v", err)
	}

	diagnostics, err := protocol.BuildDiagnosticsEnvelope(document, json.RawMessage(`{"uri":"`+document.ModelURI+`","version":1,"diagnostics":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsJSON, _ := diagnostics.MarshalJSONStrict(lspTestLimits().MaxFrameBytes)
	if _, err := DecodeServerEnvelope(diagnosticsJSON, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: envelopeBindingID, ExpectedSequence: 3,
		Head: head, Document: &document, Method: EnvelopeMethodDiagnostics, Limits: lspTestLimits(),
	}); err != nil {
		t.Fatalf("diagnostics decode = %v", err)
	}

	connectionError, err := protocol.BuildErrorEnvelope("runtime-unavailable", "language server stopped")
	if err != nil {
		t.Fatal(err)
	}
	errorJSON, _ := connectionError.MarshalJSONStrict(lspTestLimits().MaxFrameBytes)
	if _, err := DecodeServerEnvelope(errorJSON, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: envelopeBindingID, ExpectedSequence: 4,
		Head: head, Method: EnvelopeMethodError, Limits: lspTestLimits(),
	}); err != nil {
		t.Fatalf("error decode = %v", err)
	}

	ping, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopePing, 4, head, nil, PingEnvelopePayload{Nonce: "heartbeat-1"},
	))
	if err != nil {
		t.Fatal(err)
	}
	pong, err := protocol.BuildPongEnvelope(ping.MessageID, "heartbeat-1")
	if err != nil {
		t.Fatal(err)
	}
	pongJSON, _ := pong.MarshalJSONStrict(lspTestLimits().MaxFrameBytes)
	if _, err := DecodeServerEnvelope(pongJSON, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: envelopeBindingID, ExpectedSequence: 5,
		ReplyTo: pong.ReplyTo, Method: EnvelopeMethodPong, Head: head,
		Nonce: "heartbeat-1", Limits: lspTestLimits(),
	}); err != nil {
		t.Fatalf("pong decode = %v", err)
	}
	if _, err := protocol.BuildPongEnvelope(ping.MessageID, "heartbeat-1"); !errors.Is(err, ErrEnvelopeRequestUnknown) {
		t.Fatalf("duplicate pong = %v", err)
	}
}

func TestPongRequiresTheExactPendingPingAndNonce(t *testing.T) {
	protocol, head, _ := envelopeFixture(t, "x")
	ping, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopePing, 2, head, nil, PingEnvelopePayload{Nonce: "exact"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.BuildPongEnvelope(ping.MessageID, "wrong"); !errors.Is(err, ErrEnvelopeRequestUnknown) {
		t.Fatalf("wrong pong nonce = %v", err)
	}
	nonPing := clientEnvelopeMessageID(99)
	if _, err := protocol.BuildPongEnvelope(nonPing, "exact"); !errors.Is(err, ErrEnvelopeRequestUnknown) {
		t.Fatalf("non-ping reply target = %v", err)
	}
	if _, err := protocol.BuildPongEnvelope(ping.MessageID, "exact"); err != nil {
		t.Fatalf("exact pong = %v", err)
	}
}

func TestServerEnvelopeMessageIDIsUniqueAndDoesNotBurnSequenceOnFailure(t *testing.T) {
	protocol, head, _ := envelopeFixture(t, "x")
	firstID := "22000000-0000-4000-8000-000000000001"
	secondID := "22000000-0000-4000-8000-000000000002"
	protocol.idSource = envelopeIDSequence(firstID, firstID, secondID)
	first, err := protocol.BuildErrorEnvelope("runtime-error", "first")
	if err != nil || first.MessageID != firstID || first.Sequence != 2 {
		t.Fatalf("first server message = %#v, %v", first, err)
	}
	if _, err := protocol.BuildErrorEnvelope("runtime-error", "duplicate"); !errors.Is(err, ErrEnvelopeMalformed) {
		t.Fatalf("duplicate server message ID = %v", err)
	}
	second, err := protocol.BuildErrorEnvelope("runtime-error", "second")
	if err != nil || second.MessageID != secondID || second.Sequence != 3 || !second.Head.Equal(head) {
		t.Fatalf("second server message = %#v, %v", second, err)
	}
}

func TestServerMessageIDCannotAliasTheClientMessageItRepliesTo(t *testing.T) {
	protocol, head, _ := envelopeFixture(t, "x")
	ping, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopePing, 2, head, nil, PingEnvelopePayload{Nonce: "exact"},
	))
	if err != nil {
		t.Fatal(err)
	}
	serverID := "22000000-0000-4000-8000-000000000003"
	protocol.idSource = envelopeIDSequence(ping.MessageID, serverID)
	if _, err := protocol.BuildPongEnvelope(ping.MessageID, "exact"); !errors.Is(err, ErrEnvelopeMalformed) {
		t.Fatalf("aliased server/client message ID = %v", err)
	}
	pong, err := protocol.BuildPongEnvelope(ping.MessageID, "exact")
	if err != nil || pong.MessageID != serverID || pong.Sequence != 2 {
		t.Fatalf("pong after rejected ID = %#v, %v", pong, err)
	}
}

func envelopeIDSequence(values ...string) func() string {
	index := 0
	return func() string {
		if index >= len(values) {
			return "exhausted"
		}
		value := values[index]
		index++
		return value
	}
}

func TestServerEnvelopeDecoderRejectsCorrelationAndPayloadDrift(t *testing.T) {
	protocol, head, document := envelopeFixture(t, "x")
	serverError, err := protocol.BuildErrorEnvelope("runtime-unavailable", "stopped")
	if err != nil {
		t.Fatal(err)
	}
	valid, _ := serverError.MarshalJSONStrict(lspTestLimits().MaxFrameBytes)
	nullPayload := []byte(strings.Replace(
		string(valid), `"payload":`+string(serverError.Payload), `"payload":null`, 1,
	))
	expectation := ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: envelopeBindingID, ExpectedSequence: 2,
		Head: head, Method: EnvelopeMethodError, Limits: lspTestLimits(),
	}
	for name, invalid := range map[string][]byte{
		"unknown": []byte(strings.TrimSuffix(string(valid), "}") + `,"extra":true}`),
		"duplicate": []byte(strings.Replace(
			string(valid), `"kind":"server.error"`, `"kind":"server.error","kind":"server.error"`, 1,
		)),
		"sequence":           []byte(strings.Replace(string(valid), `"sequence":2`, `"sequence":3`, 1)),
		"foreign binding":    []byte(strings.Replace(string(valid), envelopeBindingID, testSession, 1)),
		"missing message ID": []byte(strings.Replace(string(valid), `"messageId"`, `"message_id"`, 1)),
		"invalid message ID": []byte(strings.Replace(string(valid), serverError.MessageID, "not-a-uuid", 1)),
		"message ID aliases binding": []byte(strings.Replace(
			string(valid), serverError.MessageID, envelopeBindingID, 1,
		)),
		"replyTo not allowed": []byte(strings.Replace(
			string(valid), `"replyTo":null`, `"replyTo":"`+envelopeRequestID+`"`, 1,
		)),
		"method drift": []byte(strings.Replace(
			string(valid), `"method":"worksflow/error"`, `"method":"worksflow/pong"`, 1,
		)),
		"undirected kind": []byte(strings.Replace(
			string(valid), `"kind":"server.error"`, `"kind":"error"`, 1,
		)),
		"payload alias": []byte(strings.Replace(string(valid), `"message"`, `"errorMessage"`, 1)),
		"payload duplicate": []byte(strings.Replace(
			string(valid), `"code":"runtime-unavailable"`, `"code":"runtime-unavailable","code":"runtime-unavailable"`, 1,
		)),
		"null payload": nullPayload,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeServerEnvelope(invalid, expectation); err == nil {
				t.Fatalf("server drift accepted: %s", invalid)
			}
		})
	}

	// A response can never shed the original request/document fence.
	responsePayload := serverResponsePayload{
		Status: "ok", Result: json.RawMessage(`{"contents":"x"}`), Error: nil,
	}
	encodedPayload, _ := json.Marshal(responsePayload)
	replyTo := envelopeRequestID
	bare := ServerEnvelope{
		SchemaVersion: EnvelopeSchemaVersion, ConnectionID: testConnection,
		BindingID: envelopeBindingID, Sequence: 2,
		MessageID: "20000000-0000-4000-8000-000000000099", ReplyTo: &replyTo,
		Kind: ServerEnvelopeResponse, Method: "textDocument/hover",
		Head: head, Document: nil, Payload: encodedPayload,
	}
	bareJSON, _ := json.Marshal(bare)
	if _, err := DecodeServerEnvelope(bareJSON, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: envelopeBindingID, ExpectedSequence: 2,
		ReplyTo: &replyTo, Head: head, Document: &document,
		Method: "textDocument/hover", Limits: lspTestLimits(),
	}); !errors.Is(err, ErrEnvelopeDocumentStale) {
		t.Fatalf("unfenced response = %v", err)
	}

	legacyPayload := json.RawMessage(`{"requestId":"` + envelopeRequestID +
		`","method":"textDocument/hover","status":"ok","result":{"contents":"x"},"error":null}`)
	legacy := ServerEnvelope{
		SchemaVersion: EnvelopeSchemaVersion, ConnectionID: testConnection,
		BindingID: envelopeBindingID, Sequence: 2,
		MessageID: "20000000-0000-4000-8000-000000000098", ReplyTo: &replyTo,
		Kind: ServerEnvelopeResponse, Method: "textDocument/hover",
		Head: head, Document: &document, Payload: legacyPayload,
	}
	legacyJSON, _ := json.Marshal(legacy)
	if _, err := DecodeServerEnvelope(legacyJSON, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: envelopeBindingID, ExpectedSequence: 2,
		ReplyTo: &replyTo, Head: head, Document: &document,
		Method: "textDocument/hover", Limits: lspTestLimits(),
	}); !errors.Is(err, ErrEnvelopeMalformed) {
		t.Fatalf("legacy response payload correlation = %v", err)
	}
}

func TestServerEnvelopeNeverReclassifiesAStaleRequestAsCurrent(t *testing.T) {
	protocol, head, document := envelopeFixture(t, "a")
	openEnvelopeDocument(t, protocol, head, document, "a")
	params := map[string]any{
		"textDocument": map[string]any{"uri": document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 0},
	}
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, head, &document,
		map[string]any{"requestId": envelopeRequestID, "method": "textDocument/hover", "params": params},
	)); err != nil {
		t.Fatal(err)
	}
	changed := document
	changed.ModelVersion++
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeDocumentChange, 4, head, &changed,
		DocumentChangeEnvelopePayload{Text: "b"},
	)); err != nil {
		t.Fatal(err)
	}
	stale, err := protocol.BuildResponseEnvelope(
		envelopeRequestID, json.RawMessage(`{"contents":"old"}`), nil,
	)
	if err != nil || stale.Kind != ServerEnvelopeStale || stale.Document == nil ||
		!stale.Document.Equal(document) || !stale.Head.Equal(head) {
		t.Fatalf("stale response = %#v, %v", stale, err)
	}

	secondRequest := "50000000-0000-4000-8000-000000000001"
	params["textDocument"] = map[string]any{"uri": changed.ModelURI}
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeRequest, 5, head, &changed,
		map[string]any{"requestId": secondRequest, "method": "textDocument/hover", "params": params},
	)); err != nil {
		t.Fatal(err)
	}
	next := head
	next.Version++
	next.JournalSequence++
	next.TreeHash = lspDigest("f")
	if _, err := protocol.DecodeClientEnvelope(clientEnvelopeJSON(
		t, ClientEnvelopeHeadRebind, 6, next, nil,
		HeadRebindEnvelopePayload{Documents: []DocumentFence{changed}},
	)); err != nil {
		t.Fatal(err)
	}
	stale, err = protocol.BuildStaleEnvelope(secondRequest, "head-rebound")
	if err != nil || stale.Kind != ServerEnvelopeStale || stale.Document == nil ||
		!stale.Document.Equal(changed) || !stale.Head.Equal(head) {
		t.Fatalf("rebound request stale = %#v, %v", stale, err)
	}
}

func TestEnvelopeServerSequenceIsRaceSafeAndMonotonic(t *testing.T) {
	protocol, _, _ := envelopeFixture(t, "x")
	const count = 32
	sequences := make(chan uint64, count)
	errorsSeen := make(chan error, count)
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			envelope, err := protocol.BuildErrorEnvelope("runtime-error", fmt.Sprintf("failure %d", index))
			if err != nil {
				errorsSeen <- err
				return
			}
			sequences <- envelope.Sequence
		}(index)
	}
	group.Wait()
	close(sequences)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
	values := make([]int, 0, count)
	for sequence := range sequences {
		values = append(values, int(sequence))
	}
	sort.Ints(values)
	for index, value := range values {
		if value != index+2 {
			t.Fatalf("server sequences = %#v", values)
		}
	}
}
