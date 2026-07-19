package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestGatewayInitializationDropsHarmlessLogBeforeResponse(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":3,"message":"private startup text"}}`,
	)}
	cancel, completed := startGatewayFixture(t, fixture, gatewayBindingID)

	requireGatewayMethod(t, receiveGatewayFrame(t, fixture.process.writes, "initialize"), "initialize")
	requireGatewayMethod(t, receiveGatewayFrame(t, fixture.process.writes, "initialized"), "initialized")
	bound := receiveGatewayFrame(t, fixture.connection.writes, "bound after startup log")
	if _, err := DecodeServerBound(bound, ServerBoundExpectation{
		ConnectionID: testConnection, BindingID: gatewayBindingID, Head: fixture.head,
		Profile: fixture.profile, Initialized: gatewayInitializedServer(t, fixture.profile),
		Documents: []DocumentFence{fixture.document},
	}); err != nil || bytes.Contains(bound, []byte("private startup text")) {
		t.Fatalf("startup log crossed browser boundary: %v\n%s", err, bound)
	}
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
	_, events := recorder.snapshot()
	if violations := gatewayServerViolationEvents(events); len(violations) != 0 {
		t.Fatalf("harmless startup log was audited as a violation: %#v", violations)
	}
}

func TestGatewayInitializationRejectsRecoverableCallbackInternallyAndContinues(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","id":7,"method":"workspace/configuration","params":{"secret":"startup-private"}}`,
	)}
	cancel, completed := startGatewayFixture(t, fixture, gatewayBindingID)

	requireGatewayMethod(t, receiveGatewayFrame(t, fixture.process.writes, "initialize"), "initialize")
	response := receiveGatewayFrame(t, fixture.process.writes, "startup callback response")
	if string(response) != `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"Method not found"}}` ||
		bytes.Contains(response, []byte("startup-private")) {
		t.Fatalf("startup callback response = %s", response)
	}
	requireGatewayMethod(t, receiveGatewayFrame(t, fixture.process.writes, "initialized"), "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
	_, events := recorder.snapshot()
	violations := gatewayServerViolationEvents(events)
	if len(violations) != 1 || violations[0].Code != "server_request_rejected" ||
		violations[0].ServerViolation == nil ||
		violations[0].ServerViolation.Method != "workspace/configuration" ||
		violations[0].ServerViolation.Ordinal != 1 || violations[0].ServerViolation.Count != 1 {
		t.Fatalf("startup callback audit = %#v", violations)
	}
}

func TestGatewayInitializationNormalizesUnknownRequestBeforeAuditAndResponse(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","id":"startup-id","method":"startupSecret/callback","params":{"source":"private-source"}}`,
	)}
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	response := receiveGatewayFrame(t, fixture.process.writes, "normalized startup callback response")
	if string(response) != `{"jsonrpc":"2.0","id":"startup-id","error":{"code":-32601,"message":"Method not found"}}` ||
		bytes.Contains(response, []byte("startupSecret")) || bytes.Contains(response, []byte("private-source")) {
		t.Fatalf("normalized startup response = %s", response)
	}
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayServerViolation) {
			t.Fatalf("unknown startup callback = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unknown startup callback did not terminate")
	}
	_, events := recorder.snapshot()
	violations := gatewayServerViolationEvents(events)
	if len(violations) != 1 || violations[0].ServerViolation == nil ||
		violations[0].ServerViolation.Method != serverControlUnknownRequestAuditMethod ||
		violations[0].ServerViolation.Ordinal != 1 || violations[0].ServerViolation.Count != 1 {
		t.Fatalf("normalized startup audit = %#v", violations)
	}
	encoded, err := json.Marshal(violations)
	if err != nil || bytes.Contains(encoded, []byte("startupSecret")) ||
		bytes.Contains(encoded, []byte("private-source")) || bytes.Contains(encoded, []byte("startup-id")) {
		t.Fatalf("startup audit covert channel remained: %v %s", err, encoded)
	}
}

func TestGatewayInitializationNormalizesUnknownNotificationBeforeAudit(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","method":"startupNotificationSecret/event","params":{"source":"private-notification"}}`,
	)}
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayServerViolation) {
			t.Fatalf("unknown startup notification = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unknown startup notification did not terminate")
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("unknown notification received a response: %s", frame)
	default:
	}
	_, events := recorder.snapshot()
	violations := gatewayServerViolationEvents(events)
	if len(violations) != 1 || violations[0].ServerViolation == nil ||
		violations[0].ServerViolation.Method != serverControlUnknownNotificationAuditMethod {
		t.Fatalf("normalized startup notification audit = %#v", violations)
	}
	encoded, err := json.Marshal(violations)
	if err != nil || bytes.Contains(encoded, []byte("startupNotificationSecret")) ||
		bytes.Contains(encoded, []byte("private-notification")) {
		t.Fatalf("startup notification covert channel remained: %v %s", err, encoded)
	}
}

func TestGatewayInitializationMalformedServerMessageUsesFixedAuditProjection(t *testing.T) {
	tests := []struct {
		name   string
		frame  string
		secret string
	}{
		{
			name: "invalid method", secret: "initMethodSecret",
			frame: `{"jsonrpc":"2.0","id":"private-init-id","method":"initMethodSecret callback","params":{"source":"private-init-source"}}`,
		},
		{
			name: "unknown top-level field", secret: "initFieldSecret",
			frame: `{"jsonrpc":"2.0","id":7,"method":"workspace/configuration","params":{},"initFieldSecret":"private-init-value"}`,
		},
	}
	var eventIDs []string
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayFixture(t, nil)
			recorder := &gatewaySecurityRecorder{}
			fixture.security = recorder
			fixture.process.reads <- gatewayFrameResult{value: []byte(test.frame)}
			_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
			_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
			select {
			case err := <-completed:
				if !errors.Is(err, ErrGatewayServerViolation) {
					t.Fatalf("malformed initialize frame = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("malformed initialize frame did not terminate")
			}
			select {
			case frame := <-fixture.process.writes:
				t.Fatalf("malformed initialize frame received a response: %s", frame)
			default:
			}
			_, events := recorder.snapshot()
			violations := gatewayServerViolationEvents(events)
			if len(violations) != 1 || violations[0].Code != "server_message_malformed" ||
				violations[0].ServerViolation == nil ||
				violations[0].ServerViolation.Method != serverControlInvalidMessageAuditMethod ||
				violations[0].ServerViolation.Ordinal != 1 ||
				violations[0].ServerViolation.Count != 1 || violations[0].RequestID != "" {
				t.Fatalf("malformed initialize audit = %#v", violations)
			}
			encoded, err := json.Marshal(violations)
			for _, forbidden := range []string{
				test.secret, "private-init-id", "private-init-source", "private-init-value",
			} {
				if err != nil || bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("malformed initialize audit retained %q: %v %s", forbidden, err, encoded)
				}
			}
			eventIDs = append(eventIDs, gatewayAuditEventID(violations[0]).String())
		})
	}
	if len(eventIDs) != 2 || eventIDs[0] != eventIDs[1] {
		t.Fatalf("raw malformed initialize frames influenced event IDs: %#v", eventIDs)
	}
}

func TestGatewayInitializationCapabilityViolationIsAuditedAndTyped(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"hoverProvider":true,"executeCommandProvider":{"commands":["private-build"]}},"serverInfo":{"name":%q,"version":%q}}}`,
		fixture.profile.ServerInfo.Name, fixture.profile.ServerInfo.Version,
	))}
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayServerViolation) || errors.Is(err, ErrGatewayInvalid) {
			t.Fatalf("initialize capability violation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initialize capability violation did not terminate")
	}
	_, events := recorder.snapshot()
	violations := gatewayServerViolationEvents(events)
	if len(violations) != 1 || violations[0].Code != "server_initialize_rejected" ||
		violations[0].ServerViolation == nil ||
		violations[0].ServerViolation.Method != "initialize" ||
		violations[0].ServerViolation.Ordinal != 1 || violations[0].ServerViolation.Count != 1 {
		t.Fatalf("initialize capability audit = %#v", violations)
	}
	encoded, err := json.Marshal(violations[0])
	if err != nil || bytes.Contains(encoded, []byte("private-build")) {
		t.Fatalf("initialize audit retained capability payload: %v %s", err, encoded)
	}
}

func TestGatewayRuntimeHarmlessLogIsDroppedWithoutClosingOrForwarding(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	cancel, completed := startGatewayFixture(t, fixture, gatewayBindingID, gatewayServerMessageID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")

	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","method":"$/progress","params":{"token":"private","value":{"message":"do not forward"}}}`,
	)}
	waitGatewayAuthorityCalls(t, fixture.authority, 3)
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopePing, 2, fixture.head, nil, PingEnvelopePayload{Nonce: "after-log"},
	)}
	pong := receiveGatewayFrame(t, fixture.connection.writes, "pong after dropped log")
	if _, err := DecodeServerEnvelope(pong, ServerEnvelopeExpectation{
		ConnectionID: testConnection, BindingID: gatewayBindingID, ExpectedSequence: 2,
		MessageID: gatewayServerMessageID,
		ReplyTo:   gatewayEnvelopeReplyTo(clientEnvelopeMessageID(2)),
		Method:    EnvelopeMethodPong, Head: fixture.head, Nonce: "after-log",
		Limits: fixture.profile.EffectiveLimits,
	}); err != nil || bytes.Contains(pong, []byte("do not forward")) {
		t.Fatalf("post-log pong = %v\n%s", err, pong)
	}
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
	_, events := recorder.snapshot()
	if violations := gatewayServerViolationEvents(events); len(violations) != 0 {
		t.Fatalf("harmless runtime log was audited as a violation: %#v", violations)
	}
}

func TestGatewayRecoverableServerRequestsAreInternalAndBounded(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")

	for ordinal := uint32(1); ordinal <= maximumRecoverableServerControlViolations+1; ordinal++ {
		fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"workspace/configuration","params":{"secret":"private-%d"}}`,
			ordinal, ordinal,
		))}
		response := receiveGatewayFrame(t, fixture.process.writes, "internal callback response")
		want := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"Method not found"}}`,
			ordinal,
		)
		if string(response) != want || bytes.Contains(response, []byte("private-")) {
			t.Fatalf("callback %d response = %s", ordinal, response)
		}
	}
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayServerViolation) {
			t.Fatalf("repeat limit = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("repeated server callbacks did not terminate the binding")
	}
	select {
	case frame := <-fixture.connection.writes:
		t.Fatalf("server callback crossed browser boundary: %s", frame)
	default:
	}
	_, events := recorder.snapshot()
	violations := gatewayServerViolationEvents(events)
	if len(violations) != int(maximumRecoverableServerControlViolations+1) {
		t.Fatalf("violation count = %d: %#v", len(violations), violations)
	}
	for index, event := range violations {
		ordinal := uint32(index + 1)
		if event.ServerViolation == nil || event.ServerViolation.Ordinal != ordinal ||
			event.ServerViolation.Count != ordinal ||
			event.ServerViolation.Method != "workspace/configuration" {
			t.Fatalf("violation %d = %#v", ordinal, event)
		}
		wantCode := "server_request_rejected"
		if ordinal > maximumRecoverableServerControlViolations {
			wantCode = "server_request_repeat_limit"
		}
		if event.Code != wantCode {
			t.Fatalf("violation %d code = %q", ordinal, event.Code)
		}
	}
}

func TestGatewayRecoverableServerRequestBudgetIsSharedAcrossInitializationAndRuntime(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","id":1,"method":"workspace/configuration","params":{}}`,
	)}
	cancel, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	defer cancel()
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	if response := receiveGatewayFrame(t, fixture.process.writes, "initialize callback response"); string(response) != `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}` {
		t.Fatalf("initialize callback response = %s", response)
	}
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")

	for id := 2; id <= 4; id++ {
		fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"workspace/configuration","params":{}}`, id,
		))}
		response := receiveGatewayFrame(t, fixture.process.writes, "runtime callback response")
		want := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"Method not found"}}`, id,
		)
		if string(response) != want {
			t.Fatalf("runtime callback %d response = %s", id, response)
		}
	}
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayServerViolation) {
			t.Fatalf("cross-stage callback limit = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fourth cross-stage callback did not terminate")
	}
	_, events := recorder.snapshot()
	violations := gatewayServerViolationEvents(events)
	if len(violations) != 4 {
		t.Fatalf("cross-stage violations = %#v", violations)
	}
	for index, event := range violations {
		ordinal := uint32(index + 1)
		if event.ServerViolation == nil || event.ServerViolation.Ordinal != ordinal ||
			event.ServerViolation.Count != ordinal ||
			event.ServerViolation.Method != "workspace/configuration" {
			t.Fatalf("cross-stage violation %d = %#v", ordinal, event)
		}
		if ordinal <= maximumRecoverableServerControlViolations && event.Code != "server_request_rejected" {
			t.Fatalf("cross-stage tolerated violation %d code = %q", ordinal, event.Code)
		}
		if ordinal > maximumRecoverableServerControlViolations && event.Code != "server_request_repeat_limit" {
			t.Fatalf("cross-stage terminal violation code = %q", event.Code)
		}
	}
}

func TestGatewayRuntimeMalformedAndRejectedResultUseFixedAuditProjection(t *testing.T) {
	t.Run("malformed frame", func(t *testing.T) {
		fixture := newGatewayFixture(t, nil)
		recorder := &gatewaySecurityRecorder{}
		fixture.security = recorder
		_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
		_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
		_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
		_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
		fixture.process.reads <- gatewayFrameResult{value: []byte(
			`{"jsonrpc":"2.0","result":{"runtimeFrameSecret":"private-result"}}`,
		)}
		select {
		case err := <-completed:
			if !errors.Is(err, ErrGatewayServerViolation) {
				t.Fatalf("runtime malformed frame = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("runtime malformed frame did not terminate")
		}
		requireFixedMalformedGatewayAudit(t, recorder, "runtimeFrameSecret", "private-result")
	})

	t.Run("unsafe result DTO", func(t *testing.T) {
		fixture := newGatewayFixture(t, nil)
		recorder := &gatewaySecurityRecorder{}
		fixture.security = recorder
		_, completed := startSecurityGatewayRequest(
			t, fixture, gatewayBindingID, gatewayServerRequestID,
		)
		_ = receiveGatewayFrame(t, fixture.process.writes, "request")
		fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%q,"result":{"contents":"safe","runtimeResultSecret":"private-result"}}`,
			gatewayServerRequestID,
		))}
		select {
		case err := <-completed:
			if !errors.Is(err, ErrGatewayServerViolation) {
				t.Fatalf("unsafe result = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("unsafe result did not terminate")
		}
		select {
		case frame := <-fixture.connection.writes:
			t.Fatalf("unsafe result reached browser: %s", frame)
		default:
		}
		requireFixedMalformedGatewayAudit(
			t, recorder, "runtimeResultSecret", "private-result", gatewayServerRequestID,
		)
	})
}

func TestGatewayHighRiskRequestAndUnknownNotificationAuditThenTerminate(t *testing.T) {
	tests := []struct {
		name         string
		frame        string
		method       string
		code         string
		wantResponse string
		forbidden    []string
	}{
		{
			name: "apply edit", method: "workspace/applyEdit", code: "server_request_forbidden",
			frame:        `{"jsonrpc":"2.0","id":"apply-1","method":"workspace/applyEdit","params":{"edit":"private"}}`,
			wantResponse: `{"jsonrpc":"2.0","id":"apply-1","error":{"code":-32601,"message":"Method not found"}}`,
			forbidden:    []string{"private"},
		},
		{
			name: "unknown request", method: serverControlUnknownRequestAuditMethod, code: "server_request_forbidden",
			frame:        `{"jsonrpc":"2.0","id":"unknown-1","method":"runtimeSecret/callback","params":{"text":"private"}}`,
			wantResponse: `{"jsonrpc":"2.0","id":"unknown-1","error":{"code":-32601,"message":"Method not found"}}`,
			forbidden:    []string{"runtimeSecret", "private", "unknown-1"},
		},
		{
			name: "unknown notification", method: serverControlUnknownNotificationAuditMethod, code: "server_notification_forbidden",
			frame:     `{"jsonrpc":"2.0","method":"notificationSecret/event","params":{"text":"private"}}`,
			forbidden: []string{"notificationSecret", "private"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayFixture(t, nil)
			recorder := &gatewaySecurityRecorder{}
			fixture.security = recorder
			_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
			_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
			_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
			_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")

			fixture.process.reads <- gatewayFrameResult{value: []byte(test.frame)}
			if test.wantResponse != "" {
				response := receiveGatewayFrame(t, fixture.process.writes, "forbidden callback response")
				if string(response) != test.wantResponse || bytes.Contains(response, []byte("private")) {
					t.Fatalf("forbidden response = %s", response)
				}
			}
			select {
			case err := <-completed:
				if !errors.Is(err, ErrGatewayServerViolation) {
					t.Fatalf("server violation = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server violation did not terminate")
			}
			select {
			case frame := <-fixture.connection.writes:
				t.Fatalf("forbidden server message crossed browser boundary: %s", frame)
			default:
			}
			_, events := recorder.snapshot()
			violations := gatewayServerViolationEvents(events)
			if len(violations) != 1 || violations[0].Code != test.code ||
				violations[0].ServerViolation == nil ||
				violations[0].ServerViolation.Method != test.method ||
				violations[0].ServerViolation.Ordinal != 1 ||
				violations[0].ServerViolation.Count != 1 {
				t.Fatalf("violation audit = %#v", violations)
			}
			encoded, err := json.Marshal(violations)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range test.forbidden {
				if bytes.Contains(encoded, []byte(forbidden)) {
					t.Fatalf("violation audit retained %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestGatewayInitializationViolationAuditOutageWithholdsResponseAndPayload(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{failActions: map[string]error{
		GatewayAuditServerViolation: errors.New("audit unavailable"),
	}}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","id":7,"method":"workspace/configuration","params":{"source":"private-source"}}`,
	)}
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewaySecurityUnavailable) {
			t.Fatalf("audit outage = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup audit outage did not fail closed")
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("unaudited callback received a response: %s", frame)
	default:
	}
	select {
	case frame := <-fixture.connection.writes:
		t.Fatalf("unaudited callback reached browser: %s", frame)
	default:
	}
	recorder.mu.Lock()
	attempts := append([]GatewayAuditEvent(nil), recorder.attempts...)
	recorder.mu.Unlock()
	violations := gatewayServerViolationEvents(attempts)
	if len(violations) != 1 || violations[0].RequestID != "" || violations[0].Method != "" ||
		violations[0].Document != nil || violations[0].ServerViolation == nil ||
		violations[0].ServerViolation.Method != "workspace/configuration" {
		t.Fatalf("payload-free violation attempt = %#v", violations)
	}
	encoded, err := json.Marshal(violations[0])
	if err != nil || bytes.Contains(encoded, []byte("private-source")) ||
		bytes.Contains(encoded, []byte(`"params"`)) || bytes.Contains(encoded, []byte(`"source"`)) {
		t.Fatalf("violation audit retained payload: %v %s", err, encoded)
	}
}

func TestGatewayMalformedServerMessageAuditOutageFailsClosed(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{failActions: map[string]error{
		GatewayAuditServerViolation: errors.New("audit unavailable"),
	}}
	fixture.security = recorder
	fixture.process.reads <- gatewayFrameResult{value: []byte(
		`{"jsonrpc":"2.0","id":"private-malformed-id","method":"private malformed method","params":{}}`,
	)}
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewaySecurityUnavailable) {
			t.Fatalf("malformed audit outage = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("malformed audit outage did not fail closed")
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("malformed frame received response during audit outage: %s", frame)
	default:
	}
	recorder.mu.Lock()
	attempts := append([]GatewayAuditEvent(nil), recorder.attempts...)
	recorder.mu.Unlock()
	violations := gatewayServerViolationEvents(attempts)
	if len(violations) != 1 || violations[0].ServerViolation == nil ||
		violations[0].ServerViolation.Method != serverControlInvalidMessageAuditMethod ||
		violations[0].Code != "server_message_malformed" {
		t.Fatalf("malformed outage audit projection = %#v", violations)
	}
	encoded, err := json.Marshal(violations)
	if err != nil || bytes.Contains(encoded, []byte("private-malformed-id")) ||
		bytes.Contains(encoded, []byte("private malformed method")) {
		t.Fatalf("malformed outage audit retained raw frame: %v %s", err, encoded)
	}
}

func TestGatewayAuditBoundaryRejectsRawUnknownMethodAndEventIDDoesNotConsumeIt(t *testing.T) {
	base := gatewayAuditFixture(
		t, GatewayAuditServerViolation, "rejected", "server_request_forbidden",
	)
	alpha := base
	alpha.ServerViolation = &GatewayServerViolationAudit{
		Method: "unitSecretAlpha/callback", Ordinal: 1, Count: 1,
	}
	beta := base
	beta.ServerViolation = &GatewayServerViolationAudit{
		Method: "unitSecretBeta/callback", Ordinal: 1, Count: 1,
	}
	if validateGatewayAuditEvent(alpha) == nil || validateGatewayAuditEvent(beta) == nil {
		t.Fatal("raw unknown method entered the Gateway audit contract")
	}
	if gatewayAuditEventID(alpha) != gatewayAuditEventID(beta) {
		t.Fatal("raw unknown method influenced defensive audit event ID inputs")
	}

	decode := func(method string) ServerControlMessage {
		t.Helper()
		message, err := DecodeServerControlMessage([]byte(
			`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{}}`,
		), serverControlLimits())
		if err != nil {
			t.Fatal(err)
		}
		return message
	}
	first, second := decode("unitSecretAlpha/callback"), decode("unitSecretBeta/callback")
	alpha.ServerViolation.Method = first.Method
	beta.ServerViolation.Method = second.Method
	if validateGatewayAuditEvent(alpha) != nil || validateGatewayAuditEvent(beta) != nil ||
		gatewayAuditEventID(alpha) != gatewayAuditEventID(beta) {
		t.Fatalf("normalized unknown audit identity drifted: %#v %#v", alpha, beta)
	}
}

func gatewayServerViolationEvents(events []GatewayAuditEvent) []GatewayAuditEvent {
	result := make([]GatewayAuditEvent, 0)
	for _, event := range events {
		if event.Action == GatewayAuditServerViolation {
			result = append(result, event)
		}
	}
	return result
}

func requireFixedMalformedGatewayAudit(
	t *testing.T,
	recorder *gatewaySecurityRecorder,
	forbidden ...string,
) GatewayAuditEvent {
	t.Helper()
	_, events := recorder.snapshot()
	violations := gatewayServerViolationEvents(events)
	if len(violations) != 1 || violations[0].Code != "server_message_malformed" ||
		violations[0].ServerViolation == nil ||
		violations[0].ServerViolation.Method != serverControlInvalidMessageAuditMethod ||
		violations[0].ServerViolation.Ordinal != 1 ||
		violations[0].ServerViolation.Count != 1 || violations[0].RequestID != "" {
		t.Fatalf("fixed malformed audit = %#v", violations)
	}
	encoded, err := json.Marshal(violations)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("fixed malformed audit retained %q: %s", value, encoded)
		}
	}
	return violations[0]
}

func waitGatewayAuthorityCalls(t *testing.T, authority *gatewayAuthorityFake, minimum int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for authority.callCount() < minimum {
		if time.Now().After(deadline) {
			t.Fatalf("authority calls did not reach %d; got %d", minimum, authority.callCount())
		}
		time.Sleep(time.Millisecond)
	}
}
