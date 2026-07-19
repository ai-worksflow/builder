package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	serverMessageProjectID   = "11111111-1111-4111-8111-111111111111"
	serverMessageSessionID   = "22222222-2222-4222-8222-222222222222"
	serverMessageCandidateID = "33333333-3333-4333-8333-333333333333"
	serverMessageOpenID      = "44444444-4444-4444-8444-444444444444"
	serverMessageHash        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func serverMessageFixture(t *testing.T) (*ServerMessageFilter, SandboxHeadFence, DocumentFence) {
	t.Helper()
	head := SandboxHeadFence{
		ProjectID: serverMessageProjectID, SessionID: serverMessageSessionID,
		SessionEpoch: 1, CandidateID: serverMessageCandidateID, Version: 7,
		JournalSequence: 9, WriterLeaseEpoch: 2, TreeHash: serverMessageHash,
	}
	uri, err := CandidateModelURI(serverMessageProjectID, serverMessageCandidateID, "apps/web/page.tsx")
	if err != nil {
		t.Fatalf("CandidateModelURI() error = %v", err)
	}
	document := DocumentFence{
		ModelURI: uri, OpenID: serverMessageOpenID, ModelVersion: 4, SavedContentHash: serverMessageHash,
	}
	filter, err := NewServerMessageFilter(ProductionV1MethodBaseline(), EffectiveLimits{
		MaxFrameBytes: 512 << 10, MaxResultBytes: 1 << 20, MaxConcurrentRequests: 32,
		MaxDocumentBytes: 1 << 20, MaxDiagnosticsPerDocument: 2_000,
		MaxCompletionItems: 500, MaxNavigationLocations: 5_000,
	}, []string{"apps/web/page.tsx"})
	if err != nil {
		t.Fatalf("NewServerMessageFilter() error = %v", err)
	}
	return filter, head, document
}

func registerServerRequest(
	t *testing.T,
	filter *ServerMessageFilter,
	id, method string,
	head SandboxHeadFence,
	document DocumentFence,
) {
	t.Helper()
	if err := filter.RegisterPending(PendingServerRequest{
		ID: id, Method: method, Head: head, Document: document,
	}); err != nil {
		t.Fatalf("RegisterPending() error = %v", err)
	}
}

func serverResponse(id, result string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":%s}`, id, result))
}

func serverMessageDocumentURI(t *testing.T, document DocumentFence, head SandboxHeadFence) string {
	t.Helper()
	uri, err := ServerDocumentURI(document.ModelURI, head)
	if err != nil {
		t.Fatal(err)
	}
	return uri
}

func TestServerMessageFilterAcceptsAndConsumesExactHoverResponse(t *testing.T) {
	filter, head, document := serverMessageFixture(t)
	id := "55555555-5555-4555-8555-555555555555"
	registerServerRequest(t, filter, id, "textDocument/hover", head, document)

	message, err := filter.Filter(serverResponse(id, `{
		"contents":{"kind":"plaintext","value":"safe hover"},
		"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}}
	}`), head, []DocumentFence{document})
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}
	if message.Kind != ServerMessageKindResponse || message.Method != "textDocument/hover" ||
		message.RequestID != id || message.Disposition != ServerMessageAccepted ||
		!message.Head.Equal(head) || !message.Document.Equal(document) || message.Error != nil {
		t.Fatalf("Filter() message = %#v", message)
	}
	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil || payload["contents"] == nil {
		t.Fatalf("safe payload = %s, error = %v", message.Payload, err)
	}

	if _, err := filter.Filter(serverResponse(id, `null`), head, []DocumentFence{document}); !errors.Is(err, ErrServerResponseUnknown) {
		t.Fatalf("duplicate completed response error = %v", err)
	}
	if err := filter.RegisterPending(PendingServerRequest{
		ID: id, Method: "textDocument/hover", Head: head, Document: document,
	}); !errors.Is(err, ErrServerResponseUnknown) {
		t.Fatalf("re-register completed ID error = %v", err)
	}
	pendingID := "12121212-1212-4212-8212-121212121212"
	registerServerRequest(t, filter, pendingID, "textDocument/hover", head, document)
	if err := filter.RegisterPending(PendingServerRequest{
		ID: pendingID, Method: "textDocument/hover", Head: head, Document: document,
	}); !errors.Is(err, ErrServerResponseUnknown) {
		t.Fatalf("duplicate pending ID error = %v", err)
	}
	unknown := "66666666-6666-4666-8666-666666666666"
	if _, err := filter.Filter(serverResponse(unknown, `null`), head, []DocumentFence{document}); !errors.Is(err, ErrServerResponseUnknown) {
		t.Fatalf("unknown response error = %v", err)
	}
}

func TestServerMessageFilterConsumesMalformedAndStaleResponses(t *testing.T) {
	t.Run("markdown capability drift", func(t *testing.T) {
		filter, head, document := serverMessageFixture(t)
		id := "67676767-6767-4767-8767-676767676767"
		registerServerRequest(t, filter, id, "textDocument/hover", head, document)
		frame := serverResponse(id, `{"contents":{"kind":"markdown","value":"[unsafe](command:test)"}}`)
		if _, err := filter.Filter(frame, head, []DocumentFence{document}); !errors.Is(err, ErrServerResultInvalid) {
			t.Fatalf("markdown capability drift error = %v", err)
		}
		if _, err := filter.Filter(serverResponse(id, `null`), head, []DocumentFence{document}); !errors.Is(err, ErrServerResponseUnknown) {
			t.Fatalf("response after markdown drift error = %v", err)
		}
	})

	t.Run("malformed result", func(t *testing.T) {
		filter, head, document := serverMessageFixture(t)
		id := "77777777-7777-4777-8777-777777777777"
		registerServerRequest(t, filter, id, "textDocument/hover", head, document)
		malformed := serverResponse(id, `{"contents":{"kind":"markdown","kind":"plaintext","value":"x"}}`)
		if _, err := filter.Filter(malformed, head, []DocumentFence{document}); !errors.Is(err, ErrServerMessageMalformed) {
			t.Fatalf("malformed result error = %v", err)
		}
		if _, err := filter.Filter(serverResponse(id, `null`), head, []DocumentFence{document}); !errors.Is(err, ErrServerResponseUnknown) {
			t.Fatalf("response after malformed result error = %v", err)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		filter, head, document := serverMessageFixture(t)
		id := "88888888-8888-4888-8888-888888888888"
		registerServerRequest(t, filter, id, "textDocument/hover", head, document)
		frame := []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%q,"result":null,"extra":true}`, id,
		))
		if _, err := filter.Filter(frame, head, []DocumentFence{document}); !errors.Is(err, ErrServerMessageMalformed) {
			t.Fatalf("unknown response field error = %v", err)
		}
		if _, err := filter.Filter(serverResponse(id, `null`), head, []DocumentFence{document}); !errors.Is(err, ErrServerResponseUnknown) {
			t.Fatalf("response after protocol error = %v", err)
		}
	})

	t.Run("stale head", func(t *testing.T) {
		filter, head, document := serverMessageFixture(t)
		id := "99999999-9999-4999-8999-999999999999"
		registerServerRequest(t, filter, id, "textDocument/hover", head, document)
		current := head
		current.Version++
		current.JournalSequence++
		current.TreeHash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		message, err := filter.Filter(serverResponse(id, `{"contents":"old"}`), current, nil)
		if err != nil {
			t.Fatalf("stale Filter() error = %v", err)
		}
		if message.Disposition != ServerMessageStaleDropped || message.Payload != nil || message.Error != nil ||
			!message.Head.Equal(head) || !message.Document.Equal(document) {
			t.Fatalf("stale message = %#v", message)
		}
		if _, err := filter.Filter(serverResponse(id, `null`), current, nil); !errors.Is(err, ErrServerResponseUnknown) {
			t.Fatalf("response after stale-drop error = %v", err)
		}
	})
}

func TestServerMessageFilterRejectsBatchRequestsAndForbiddenNotifications(t *testing.T) {
	filter, head, document := serverMessageFixture(t)
	tests := []struct {
		name  string
		frame string
		want  error
	}{
		{"batch", `[]`, ErrServerMessageMalformed},
		{"apply edit request", `{"jsonrpc":"2.0","id":"1","method":"workspace/applyEdit","params":{}}`, ErrServerRequestForbidden},
		{"dynamic registration", `{"jsonrpc":"2.0","id":"1","method":"client/registerCapability","params":{}}`, ErrServerRequestForbidden},
		{"execute command", `{"jsonrpc":"2.0","id":"1","method":"workspace/executeCommand","params":{}}`, ErrServerRequestForbidden},
		{"rename", `{"jsonrpc":"2.0","id":"1","method":"textDocument/rename","params":{}}`, ErrServerRequestForbidden},
		{"format", `{"jsonrpc":"2.0","id":"1","method":"textDocument/formatting","params":{}}`, ErrServerRequestForbidden},
		{"code action", `{"jsonrpc":"2.0","id":"1","method":"textDocument/codeAction","params":{}}`, ErrServerRequestForbidden},
		{"show document", `{"jsonrpc":"2.0","id":"1","method":"window/showDocument","params":{}}`, ErrServerRequestForbidden},
		{"progress create", `{"jsonrpc":"2.0","id":"1","method":"window/workDoneProgress/create","params":{}}`, ErrServerRequestForbidden},
		{"telemetry", `{"jsonrpc":"2.0","method":"telemetry/event","params":{}}`, ErrServerNotificationForbidden},
		{"window log", `{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":3,"message":"x"}}`, ErrServerNotificationForbidden},
		{"workspace symbol", `{"jsonrpc":"2.0","method":"workspace/symbol","params":{}}`, ErrServerNotificationForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := filter.Filter([]byte(test.frame), head, []DocumentFence{document}); !errors.Is(err, test.want) {
				t.Fatalf("Filter() error = %v, want %v", err, test.want)
			}
		})
	}
	for _, method := range []string{"workspace/symbol", "textDocument/foldingRange", "workspace/applyEdit"} {
		if err := filter.RegisterPending(PendingServerRequest{
			ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Method: method, Head: head, Document: document,
		}); !errors.Is(err, ErrServerResponseMethodInvalid) {
			t.Fatalf("RegisterPending(%q) error = %v", method, err)
		}
	}
}

func TestServerMessageFilterVersionedDiagnosticsAreExactFenceBound(t *testing.T) {
	filter, head, document := serverMessageFixture(t)
	serverURI := serverMessageDocumentURI(t, document, head)
	frame := []byte(fmt.Sprintf(`{
		"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{
			"uri":%q,"version":4,"diagnostics":[{
				"range":{"start":{"line":0,"character":1},"end":{"line":0,"character":2}},
				"severity":2,"code":"W1","source":"fixture","message":"warning","tags":[1]
			}]
		}
	}`, serverURI))
	message, err := filter.Filter(frame, head, []DocumentFence{document})
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}
	if message.Disposition != ServerMessageAccepted || !message.Document.Equal(document) || len(message.Payload) == 0 {
		t.Fatalf("diagnostic message = %#v", message)
	}
	var diagnosticPayload struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(message.Payload, &diagnosticPayload); err != nil || diagnosticPayload.URI != document.ModelURI {
		t.Fatalf("diagnostic URI was not translated back to Candidate identity: %s, err=%v", message.Payload, err)
	}

	staleFrame := []byte(fmt.Sprintf(`{
		"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{
			"uri":%q,"version":3,"diagnostics":[]
		}
	}`, serverURI))
	message, err = filter.Filter(staleFrame, head, []DocumentFence{document})
	if err != nil || message.Disposition != ServerMessageStaleDropped || message.Payload != nil {
		t.Fatalf("stale diagnostic message = %#v, error = %v", message, err)
	}

	missingVersion := []byte(fmt.Sprintf(`{
		"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{
			"uri":%q,"diagnostics":[]
		}
	}`, serverURI))
	if _, err := filter.Filter(missingVersion, head, []DocumentFence{document}); !errors.Is(err, ErrServerResultInvalid) {
		t.Fatalf("missing diagnostic version error = %v", err)
	}

	unboundURI := "file:///workspace/apps/web/other.tsx"
	unboundFrame := []byte(fmt.Sprintf(`{
		"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{
			"uri":%q,"version":4,"diagnostics":[]
		}
	}`, unboundURI))
	if _, err := filter.Filter(unboundFrame, head, []DocumentFence{document}); !errors.Is(err, ErrServerResultInvalid) {
		t.Fatalf("unbound diagnostic URI escaped exact Candidate tree: %v", err)
	}
}

func TestServerMessageFilterMethodSpecificResultSanitizers(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		result      func(DocumentFence) string
		wantErr     error
		wantPayload bool
	}{
		{
			name: "safe completion", method: "textDocument/completion", wantPayload: true,
			result: func(DocumentFence) string {
				return `{"isIncomplete":false,"items":[{"label":"safe","kind":3,"insertText":"safe","insertTextFormat":1}]}`
			},
		},
		{
			name: "completion command forbidden", method: "textDocument/completion", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `[{"label":"unsafe","insertText":"x","command":{"title":"run","command":"evil"}}]`
			},
		},
		{
			name: "completion cross edit forbidden", method: "textDocument/completion", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `[{"label":"unsafe","insertText":"x","additionalTextEdits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"newText":"import x"}]}]`
			},
		},
		{
			name: "completion snippet forbidden", method: "textDocument/completion", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `[{"label":"unsafe","insertText":"${1:x}","insertTextFormat":2}]`
			},
		},
		{
			name: "navigation canonical", method: "textDocument/definition", wantPayload: true,
			result: func(DocumentFence) string {
				return `[{"uri":"file:///workspace/apps/web/page.tsx","range":{"start":{"line":0,"character":0},"end":{"line":1,"character":0}}}]`
			},
		},
		{
			name: "navigation host URI forbidden", method: "textDocument/references", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `[{"uri":"file:///etc/passwd","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`
			},
		},
		{
			name: "navigation path absent from exact tree", method: "textDocument/references", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `[{"uri":"file:///workspace/apps/web/invented.tsx","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`
			},
		},
		{
			name: "signature", method: "textDocument/signatureHelp", wantPayload: true,
			result: func(DocumentFence) string {
				return `{"signatures":[{"label":"fn(value)","parameters":[{"label":[3,8]}]}],"activeSignature":0,"activeParameter":0}`
			},
		},
		{
			name: "signature default active signature", method: "textDocument/signatureHelp", wantPayload: true,
			result: func(DocumentFence) string {
				return `{"signatures":[{"label":"fn(value)","parameters":[{"label":[3,8]}]}],"activeParameter":0}`
			},
		},
		{
			name: "signature default active parameter out of range", method: "textDocument/signatureHelp", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `{"signatures":[{"label":"fn(value)","parameters":[{"label":[3,8]}]}],"activeParameter":1}`
			},
		},
		{
			name: "signature empty list cannot have active parameter", method: "textDocument/signatureHelp", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `{"signatures":[],"activeParameter":0}`
			},
		},
		{
			name: "document symbols", method: "textDocument/documentSymbol", wantPayload: true,
			result: func(DocumentFence) string {
				return `[{"name":"Page","kind":5,"range":{"start":{"line":0,"character":0},"end":{"line":3,"character":0}},"selectionRange":{"start":{"line":0,"character":6},"end":{"line":0,"character":10}},"children":[]}]`
			},
		},
		{
			name: "semantic tokens", method: "textDocument/semanticTokens/full", wantPayload: true,
			result: func(DocumentFence) string {
				return `{"resultId":"v1","data":[0,0,3,1,0]}`
			},
		},
		{
			name: "semantic token float forbidden", method: "textDocument/semanticTokens/full", wantErr: ErrServerMessageMalformed,
			result: func(DocumentFence) string {
				return `{"data":[0,0,3.5,1,0]}`
			},
		},
		{
			name: "inlay hint inert", method: "textDocument/inlayHint", wantPayload: true,
			result: func(DocumentFence) string {
				return `[{"position":{"line":1,"character":2},"label":": string","kind":1,"paddingLeft":true}]`
			},
		},
		{
			name: "inlay hint edit forbidden", method: "textDocument/inlayHint", wantErr: ErrServerResultInvalid,
			result: func(DocumentFence) string {
				return `[{"position":{"line":1,"character":2},"label":"x","textEdits":[]}]`
			},
		},
		{
			name: "pull diagnostics", method: "textDocument/diagnostic", wantPayload: true,
			result: func(DocumentFence) string {
				return `{"kind":"full","resultId":"d1","items":[]}`
			},
		},
		{
			name: "prototype pollution", method: "textDocument/hover", wantErr: ErrServerMessageMalformed,
			result: func(DocumentFence) string {
				return `{"contents":"x","__proto__":{"polluted":true}}`
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter, head, document := serverMessageFixture(t)
			id := fmt.Sprintf("%08d-aaaa-4aaa-8aaa-%012d", index+1, index+1)
			registerServerRequest(t, filter, id, test.method, head, document)
			message, err := filter.Filter(serverResponse(id, test.result(document)), head, []DocumentFence{document})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Filter() error = %v, want %v", err, test.wantErr)
				}
				if _, repeatErr := filter.Filter(serverResponse(id, `null`), head, []DocumentFence{document}); !errors.Is(repeatErr, ErrServerResponseUnknown) {
					t.Fatalf("response after rejected result error = %v", repeatErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Filter() error = %v", err)
			}
			if test.wantPayload && len(message.Payload) == 0 {
				t.Fatal("accepted result has empty payload")
			}
		})
	}
}

func TestServerMessageFilterNavigationRequiresExactTreeAndRestoresCandidateURI(t *testing.T) {
	filter, head, document := serverMessageFixture(t)
	filter.paths["apps/web/other.tsx"] = struct{}{}
	id := "91919191-9191-4191-8191-919191919191"
	registerServerRequest(t, filter, id, "textDocument/definition", head, document)
	message, err := filter.Filter(serverResponse(id,
		`[{"uri":"file:///workspace/apps/web/other.tsx","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`,
	), head, []DocumentFence{document})
	if err != nil {
		t.Fatal(err)
	}
	want, err := CandidateModelURI(head.ProjectID, head.CandidateID, "apps/web/other.tsx")
	if err != nil {
		t.Fatal(err)
	}
	var locations []serverLocation
	if json.Unmarshal(message.Payload, &locations) != nil || len(locations) != 1 || locations[0].URI != want ||
		string(message.Payload) == `[{"uri":"file:///workspace/apps/web/other.tsx"}]` {
		t.Fatalf("navigation URI was not restored to exact Candidate identity: %s", message.Payload)
	}
}

func TestServerMessageFilterRejectsMissingOrNoncanonicalCandidateTreeCatalog(t *testing.T) {
	limits := EffectiveLimits{
		MaxFrameBytes: 512 << 10, MaxResultBytes: 1 << 20, MaxConcurrentRequests: 32,
		MaxDocumentBytes: 1 << 20, MaxDiagnosticsPerDocument: 2_000,
		MaxCompletionItems: 500, MaxNavigationLocations: 5_000,
	}
	for _, paths := range [][]string{
		nil,
		{"apps/web/z.ts", "apps/web/a.ts"},
		{"apps/web/a.ts", "apps/web/a.ts"},
		{"apps/../secret.ts"},
		{".git/config"},
	} {
		if _, err := NewServerMessageFilter(ProductionV1MethodBaseline(), limits, paths); !errors.Is(err, ErrServerMessageMalformed) {
			t.Fatalf("tree catalog %#v error = %v", paths, err)
		}
	}
}

func TestServerMessageFilterStrictErrorAndBounds(t *testing.T) {
	t.Run("bounded error", func(t *testing.T) {
		filter, head, document := serverMessageFixture(t)
		id := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		registerServerRequest(t, filter, id, "textDocument/hover", head, document)
		frame := []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%q,"error":{"code":-32601,"message":"not found"}}`, id,
		))
		message, err := filter.Filter(frame, head, []DocumentFence{document})
		if err != nil || message.Error == nil || message.Error.Code != -32601 || message.Payload != nil {
			t.Fatalf("error response = %#v, error = %v", message, err)
		}
	})

	t.Run("error data forbidden and consumed", func(t *testing.T) {
		filter, head, document := serverMessageFixture(t)
		id := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		registerServerRequest(t, filter, id, "textDocument/hover", head, document)
		frame := []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%q,"error":{"code":-32601,"message":"bad","data":{"secret":"x"}}}`, id,
		))
		if _, err := filter.Filter(frame, head, []DocumentFence{document}); !errors.Is(err, ErrServerResultInvalid) {
			t.Fatalf("error data result = %v", err)
		}
		if _, err := filter.Filter(serverResponse(id, `null`), head, []DocumentFence{document}); !errors.Is(err, ErrServerResponseUnknown) {
			t.Fatalf("response after bad error object = %v", err)
		}
	})

	t.Run("completion item cap", func(t *testing.T) {
		filter, head, document := serverMessageFixture(t)
		filter.limits.MaxCompletionItems = 1
		id := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		registerServerRequest(t, filter, id, "textDocument/completion", head, document)
		result := `[{"label":"a","insertText":"a"},{"label":"b","insertText":"b"}]`
		if _, err := filter.Filter(serverResponse(id, result), head, []DocumentFence{document}); !errors.Is(err, ErrServerResultInvalid) {
			t.Fatalf("completion cap error = %v", err)
		}
	})
}

func TestServerMessageFilterConcurrentResponseCompletesOnce(t *testing.T) {
	filter, head, document := serverMessageFixture(t)
	id := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	registerServerRequest(t, filter, id, "textDocument/hover", head, document)
	frame := serverResponse(id, `{"contents":"safe"}`)
	var accepted atomic.Int64
	var rejected atomic.Int64
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			message, err := filter.Filter(frame, head, []DocumentFence{document})
			if err == nil && message.Disposition == ServerMessageAccepted {
				accepted.Add(1)
				return
			}
			if errors.Is(err, ErrServerResponseUnknown) {
				rejected.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if accepted.Load() != 1 || rejected.Load() != 31 {
		t.Fatalf("accepted = %d, rejected = %d", accepted.Load(), rejected.Load())
	}
}

func TestServerMessageFilterCancellationCompletesRequestID(t *testing.T) {
	filter, head, document := serverMessageFixture(t)
	id := "abababab-abab-4bab-8bab-abababababab"
	registerServerRequest(t, filter, id, "textDocument/hover", head, document)
	if err := filter.CancelPending(id); err != nil {
		t.Fatalf("CancelPending() error = %v", err)
	}
	if _, err := filter.Filter(serverResponse(id, `{"contents":"late"}`), head, []DocumentFence{document}); !errors.Is(err, ErrServerResponseUnknown) {
		t.Fatalf("late response error = %v", err)
	}
	if err := filter.CancelPending(id); !errors.Is(err, ErrServerResponseUnknown) {
		t.Fatalf("duplicate cancellation error = %v", err)
	}
	if err := filter.RegisterPending(PendingServerRequest{
		ID: id, Method: "textDocument/hover", Head: head, Document: document,
	}); !errors.Is(err, ErrServerResponseUnknown) {
		t.Fatalf("re-register cancelled ID error = %v", err)
	}
}
