package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func serverControlLimits() EffectiveLimits {
	return EffectiveLimits{MaxFrameBytes: 512 << 10}
}

func TestServerControlDropsOnlyKnownPayloadFreeNotifications(t *testing.T) {
	for _, method := range []string{
		"$/logTrace", "$/progress", "telemetry/event", "window/logMessage", "window/showMessage",
	} {
		frame := []byte(`{"jsonrpc":"2.0","method":"` + method + `","params":{"secret":"never-retain"}}`)
		message, err := DecodeServerControlMessage(frame, serverControlLimits())
		if err != nil || message.Method != method ||
			message.Disposition != ServerControlDropNotification ||
			message.Response != nil || message.AuditCode != "" {
			t.Fatalf("DecodeServerControlMessage(%q) = %#v, %v", method, message, err)
		}
	}

	message, err := DecodeServerControlMessage(
		[]byte(`{"jsonrpc":"2.0","method":"workspace/unknown","params":{}}`),
		serverControlLimits(),
	)
	if err != nil || message.Disposition != ServerControlTerminate ||
		message.AuditCode != "server_notification_forbidden" || message.Response != nil ||
		message.Method != serverControlUnknownNotificationAuditMethod {
		t.Fatalf("unknown notification = %#v, %v", message, err)
	}
	if _, err := DecodeServerControlMessage(
		[]byte(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{}}`),
		serverControlLimits(),
	); !errors.Is(err, ErrServerControlNotApplicable) {
		t.Fatalf("diagnostics must use result filter: %v", err)
	}
}

func TestServerControlRespondsInternallyAndClassifiesTermination(t *testing.T) {
	for _, method := range []string{
		"client/registerCapability", "client/unregisterCapability", "window/showMessageRequest",
		"window/workDoneProgress/create", "workspace/configuration", "workspace/workspaceFolders",
	} {
		frame := []byte(`{"jsonrpc":"2.0","id":7,"method":"` + method + `","params":{"source":"private"}}`)
		message, err := DecodeServerControlMessage(frame, serverControlLimits())
		if err != nil || message.Disposition != ServerControlRespondContinue ||
			message.Method != method || message.AuditCode != "server_request_rejected" {
			t.Fatalf("recoverable request %q = %#v, %v", method, message, err)
		}
		if strings.Contains(string(message.Response), "private") ||
			string(message.Response) != `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"Method not found"}}` {
			t.Fatalf("internal response leaked request payload: %s", message.Response)
		}
	}

	for _, method := range []string{
		"workspace/applyEdit", "workspace/executeCommand", "window/showDocument", "unknown/callback",
	} {
		message, err := DecodeServerControlMessage(
			[]byte(`{"jsonrpc":"2.0","id":"callback-1","method":"`+method+`","params":{}}`),
			serverControlLimits(),
		)
		if err != nil || message.Disposition != ServerControlRespondTerminate ||
			message.AuditCode != "server_request_forbidden" ||
			!strings.Contains(string(message.Response), `"id":"callback-1"`) {
			t.Fatalf("forbidden request %q = %#v, %v", method, message, err)
		}
		wantMethod := method
		if method == "unknown/callback" {
			wantMethod = serverControlUnknownRequestAuditMethod
		}
		if message.Method != wantMethod {
			t.Fatalf("forbidden request %q audit method = %q, want %q", method, message.Method, wantMethod)
		}
	}
}

func TestServerControlNormalizesSecretBearingUnknownMethodsBeforeAuditProjection(t *testing.T) {
	requestMethods := []string{"secretAlpha/callback", "secretBeta/callback"}
	for _, method := range requestMethods {
		message, err := DecodeServerControlMessage([]byte(
			`{"jsonrpc":"2.0","id":"safe-id","method":"`+method+`","params":{"secret":"body"}}`,
		), serverControlLimits())
		if err != nil || message.Method != serverControlUnknownRequestAuditMethod ||
			message.Disposition != ServerControlRespondTerminate ||
			bytes.Contains(message.Response, []byte(method)) || bytes.Contains(message.Response, []byte("body")) {
			t.Fatalf("unknown request %q = %#v, %v", method, message, err)
		}
	}

	notificationMethods := []string{"secretAlpha/notification", "secretBeta/notification"}
	for _, method := range notificationMethods {
		message, err := DecodeServerControlMessage([]byte(
			`{"jsonrpc":"2.0","method":"`+method+`","params":{"secret":"body"}}`,
		), serverControlLimits())
		if err != nil || message.Method != serverControlUnknownNotificationAuditMethod ||
			message.Disposition != ServerControlTerminate || message.Response != nil {
			t.Fatalf("unknown notification %q = %#v, %v", method, message, err)
		}
	}
}

func TestServerControlRejectsAmbiguousIDsAndMalformedFrames(t *testing.T) {
	tests := []string{
		`[]`,
		`{"jsonrpc":"2.0","id":null,"method":"workspace/configuration"}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"workspace/configuration"}`,
		`{"jsonrpc":"2.0","id":9007199254740992,"method":"workspace/configuration"}`,
		`{"jsonrpc":"2.0","id":1,"method":"workspace/configuration","extra":true}`,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"workspace/configuration"}`,
	}
	for _, frame := range tests {
		if _, err := DecodeServerControlMessage([]byte(frame), serverControlLimits()); !errors.Is(err, ErrServerControlMalformed) {
			t.Fatalf("malformed frame %s error = %v", frame, err)
		}
	}

	response := `{"jsonrpc":"2.0","id":"11111111-1111-4111-8111-111111111111","result":null}`
	if _, err := DecodeServerControlMessage([]byte(response), serverControlLimits()); !errors.Is(err, ErrServerControlNotApplicable) {
		t.Fatalf("ordinary response error = %v", err)
	}

	message, err := DecodeServerControlMessage(
		[]byte(`{"jsonrpc":"2.0","id":"safe","method":"workspace/configuration"}`),
		serverControlLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var responseObject map[string]any
	if err := json.Unmarshal(message.Response, &responseObject); err != nil || responseObject["id"] != "safe" {
		t.Fatalf("normalized response = %s, %v", message.Response, err)
	}
}
