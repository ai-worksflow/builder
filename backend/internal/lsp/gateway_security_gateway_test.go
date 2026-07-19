package lsp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type gatewaySecurityRecorder struct {
	mu            sync.Mutex
	deny          bool
	rateErr       error
	leaseContract GatewayEditorLeaseContract
	acquireDenied bool
	acquireErr    error
	renewLost     bool
	renewErr      error
	releaseLost   bool
	releaseErr    error
	leaseCalls    []string
	leaseInputs   []GatewayEditorLeaseInput
	enforceLease  bool
	leaseOwner    string
	leaseExpires  time.Time
	leaseNow      func() time.Time
	failActions   map[string]error
	rateInputs    []GatewayRequestRateLimitInput
	attempts      []GatewayAuditEvent
	events        []GatewayAuditEvent
}

func (recorder *gatewaySecurityRecorder) contractLocked() GatewayEditorLeaseContract {
	if recorder.leaseContract.TTL == 0 {
		return GatewayEditorLeaseContract{
			TTL: GatewayEditorLeaseTTL, HeartbeatInterval: GatewayEditorHeartbeatInterval,
		}
	}
	return recorder.leaseContract
}

func (recorder *gatewaySecurityRecorder) leaseNowLocked() time.Time {
	if recorder.leaseNow != nil {
		return recorder.leaseNow()
	}
	return time.Now()
}

func (recorder *gatewaySecurityRecorder) AcquireGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (GatewayEditorLeaseContract, bool, error) {
	if err := ctx.Err(); err != nil {
		return GatewayEditorLeaseContract{}, false, err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.leaseCalls = append(recorder.leaseCalls, "acquire")
	recorder.leaseInputs = append(recorder.leaseInputs, input)
	contract := recorder.contractLocked()
	if recorder.acquireErr != nil || recorder.acquireDenied {
		return contract, false, recorder.acquireErr
	}
	if recorder.enforceLease {
		now := recorder.leaseNowLocked()
		if recorder.leaseOwner == "" || !now.Before(recorder.leaseExpires) || recorder.leaseOwner == input.OwnerBindingID {
			recorder.leaseOwner = input.OwnerBindingID
			recorder.leaseExpires = now.Add(contract.TTL)
			return contract, true, nil
		}
		return contract, false, nil
	}
	return contract, true, nil
}

func (recorder *gatewaySecurityRecorder) RenewGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.leaseCalls = append(recorder.leaseCalls, "renew")
	recorder.leaseInputs = append(recorder.leaseInputs, input)
	if recorder.renewErr != nil || recorder.renewLost {
		return false, recorder.renewErr
	}
	if recorder.enforceLease {
		now := recorder.leaseNowLocked()
		if recorder.leaseOwner != input.OwnerBindingID || !now.Before(recorder.leaseExpires) {
			return false, nil
		}
		recorder.leaseExpires = now.Add(recorder.contractLocked().TTL)
	}
	return true, nil
}

func (recorder *gatewaySecurityRecorder) ReleaseGatewayEditorLease(
	ctx context.Context,
	input GatewayEditorLeaseInput,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.leaseCalls = append(recorder.leaseCalls, "release")
	recorder.leaseInputs = append(recorder.leaseInputs, input)
	if recorder.releaseErr != nil || recorder.releaseLost {
		return false, recorder.releaseErr
	}
	if recorder.enforceLease {
		if recorder.leaseOwner != input.OwnerBindingID {
			return false, nil
		}
		recorder.leaseOwner = ""
		recorder.leaseExpires = time.Time{}
	}
	return true, nil
}

func (recorder *gatewaySecurityRecorder) AllowGatewayRequest(
	ctx context.Context,
	input GatewayRequestRateLimitInput,
) (GatewayRequestRateLimitDecision, error) {
	if err := ctx.Err(); err != nil {
		return GatewayRequestRateLimitDecision{}, err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.rateInputs = append(recorder.rateInputs, input)
	if recorder.rateErr != nil {
		return GatewayRequestRateLimitDecision{}, recorder.rateErr
	}
	if recorder.deny {
		return GatewayRequestRateLimitDecision{RetryAfter: time.Second}, nil
	}
	return GatewayRequestRateLimitDecision{Allowed: true}, nil
}

func (recorder *gatewaySecurityRecorder) AppendGatewayAudit(
	ctx context.Context,
	event GatewayAuditEvent,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.attempts = append(recorder.attempts, event)
	if err := recorder.failActions[event.Action]; err != nil {
		return err
	}
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *gatewaySecurityRecorder) snapshot() ([]GatewayRequestRateLimitInput, []GatewayAuditEvent) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]GatewayRequestRateLimitInput(nil), recorder.rateInputs...),
		append([]GatewayAuditEvent(nil), recorder.events...)
}

func (recorder *gatewaySecurityRecorder) leaseSnapshot() ([]string, []GatewayEditorLeaseInput) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.leaseCalls...),
		append([]GatewayEditorLeaseInput(nil), recorder.leaseInputs...)
}

func countGatewayAudit(events []GatewayAuditEvent, action string) int {
	count := 0
	for _, event := range events {
		if event.Action == action {
			count++
		}
	}
	return count
}

func startSecurityGatewayRequest(
	t *testing.T,
	fixture *gatewayFixture,
	ids ...string,
) (context.CancelFunc, <-chan error) {
	t.Helper()
	cancel, completed := startGatewayFixture(t, fixture, ids...)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	params := map[string]any{
		"textDocument": map[string]any{"uri": fixture.document.ModelURI},
		"position":     map[string]any{"line": 0, "character": 0},
	}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeRequest, 3, fixture.head, &fixture.document,
		map[string]any{
			"requestId": gatewayBrowserRequestID, "method": "textDocument/hover", "params": params,
		},
	)}
	return cancel, completed
}

func TestGatewayRequiresSecurityAndRateRejectsBeforeRuntimeWrite(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	if _, err := NewGateway(fixture.resolver, fixture.runtime, fixture.authority, nil); !errors.Is(err, ErrGatewayInvalid) {
		t.Fatalf("nil production security = %v", err)
	}
	recorder := &gatewaySecurityRecorder{deny: true}
	fixture.security = recorder
	_, completed := startSecurityGatewayRequest(t, fixture, gatewayBindingID)
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayRequestRateLimited) {
			t.Fatalf("rate rejection = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rate rejection did not fail closed")
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("rate-rejected request reached language server: %s", frame)
	default:
	}
	inputs, events := recorder.snapshot()
	if len(inputs) != 1 || inputs[0].RequestsPerSecond != fixture.profile.EffectiveLimits.RequestsPerSecond ||
		inputs[0].RequestBurst != fixture.profile.EffectiveLimits.RequestBurst ||
		countGatewayAudit(events, GatewayAuditRequestAdmitted) != 0 ||
		countGatewayAudit(events, GatewayAuditRequestError) != 1 ||
		countGatewayAudit(events, GatewayAuditBindingOpen) != 1 ||
		countGatewayAudit(events, GatewayAuditBindingClose) != 1 {
		t.Fatalf("rate security facts = inputs=%#v events=%#v", inputs, events)
	}
}

func TestGatewayAuditOutageFailsClosedBeforeRuntimeWrite(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{failActions: map[string]error{
		GatewayAuditRequestAdmitted: errors.New("audit unavailable"),
	}}
	fixture.security = recorder
	_, completed := startSecurityGatewayRequest(t, fixture, gatewayBindingID)
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewaySecurityUnavailable) {
			t.Fatalf("audit outage = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("audit outage did not fail closed")
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("unaudited request reached language server: %s", frame)
	default:
	}
}

func TestGatewayRateLimiterOutageFailsClosedBeforeRuntimeWrite(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{rateErr: errors.New("Redis unavailable")}
	fixture.security = recorder
	_, completed := startSecurityGatewayRequest(t, fixture, gatewayBindingID)
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewaySecurityUnavailable) {
			t.Fatalf("rate limiter outage = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rate limiter outage did not fail closed")
	}
	select {
	case frame := <-fixture.process.writes:
		t.Fatalf("request admitted during limiter outage: %s", frame)
	default:
	}
	_, events := recorder.snapshot()
	if countGatewayAudit(events, GatewayAuditRequestError) != 1 ||
		countGatewayAudit(events, GatewayAuditRequestAdmitted) != 0 {
		t.Fatalf("limiter outage audit facts = %#v", events)
	}
}

func TestGatewayTerminalAuditOutageWithholdsServerResult(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{failActions: map[string]error{
		GatewayAuditRequestCompleted: errors.New("audit unavailable"),
	}}
	fixture.security = recorder
	_, completed := startSecurityGatewayRequest(
		t, fixture, gatewayBindingID, gatewayServerRequestID, gatewayServerMessageID,
	)
	_ = receiveGatewayFrame(t, fixture.process.writes, "request")
	fixture.process.reads <- gatewayFrameResult{value: []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"result":{"contents":{"kind":"plaintext","value":"safe"}}}`,
		gatewayServerRequestID,
	))}
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewaySecurityUnavailable) {
			t.Fatalf("terminal audit outage = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal audit outage did not close Gateway")
	}
	select {
	case frame := <-fixture.connection.writes:
		t.Fatalf("unaudited server result reached browser: %s", frame)
	default:
	}
}

func TestGatewayCompletedRequestHasOneDurableTerminalDespiteDuplicateResponse(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	_, completed := startSecurityGatewayRequest(
		t, fixture, gatewayBindingID, gatewayServerRequestID, gatewayServerMessageID,
	)
	request := receiveGatewayFrame(t, fixture.process.writes, "request")
	requireGatewayMethod(t, request, "textDocument/hover")
	response := []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"result":{"contents":{"kind":"plaintext","value":"safe"}}}`,
		gatewayServerRequestID,
	))
	fixture.process.reads <- gatewayFrameResult{value: response}
	_ = receiveGatewayFrame(t, fixture.connection.writes, "response")
	fixture.process.reads <- gatewayFrameResult{value: response}
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("duplicate response did not close Gateway")
	}
	_, events := recorder.snapshot()
	terminal := countGatewayAudit(events, GatewayAuditRequestCompleted) +
		countGatewayAudit(events, GatewayAuditRequestCancel) +
		countGatewayAudit(events, GatewayAuditRequestTimeout) +
		countGatewayAudit(events, GatewayAuditRequestStale) +
		countGatewayAudit(events, GatewayAuditRequestError)
	if countGatewayAudit(events, GatewayAuditRequestAdmitted) != 1 || terminal != 1 ||
		countGatewayAudit(events, GatewayAuditRequestCompleted) != 1 {
		t.Fatalf("duplicate response terminal facts = %#v", events)
	}
}

func TestGatewayCancelAndTimeoutEachEmitOneTerminalFact(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		fixture := newGatewayFixture(t, nil)
		recorder := &gatewaySecurityRecorder{}
		fixture.security = recorder
		cancel, completed := startSecurityGatewayRequest(
			t, fixture, gatewayBindingID, gatewayServerRequestID,
		)
		_ = receiveGatewayFrame(t, fixture.process.writes, "request")
		fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
			t, ClientEnvelopeCancel, 4, fixture.head, &fixture.document,
			CancelEnvelopePayload{ReplyTo: gatewayBrowserRequestID},
		)}
		requireGatewayMethod(t, receiveGatewayFrame(t, fixture.process.writes, "cancel"), "$/cancelRequest")
		if err := waitGatewayDone(t, cancel, completed); err != nil {
			t.Fatal(err)
		}
		_, events := recorder.snapshot()
		if countGatewayAudit(events, GatewayAuditRequestAdmitted) != 1 ||
			countGatewayAudit(events, GatewayAuditRequestCancel) != 1 {
			t.Fatalf("cancel audit facts = %#v", events)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		fixture := newGatewayFixture(t, func(profile *ProfileIdentity) {
			profile.Limits.RequestTimeoutMillis = 25
		})
		recorder := &gatewaySecurityRecorder{}
		fixture.security = recorder
		cancel, completed := startSecurityGatewayRequest(
			t, fixture, gatewayBindingID, gatewayServerRequestID, gatewayServerMessageID,
		)
		_ = receiveGatewayFrame(t, fixture.process.writes, "request")
		requireGatewayMethod(t, receiveGatewayFrame(t, fixture.process.writes, "timeout cancel"), "$/cancelRequest")
		_ = receiveGatewayFrame(t, fixture.connection.writes, "timeout stale")
		if err := waitGatewayDone(t, cancel, completed); err != nil {
			t.Fatal(err)
		}
		_, events := recorder.snapshot()
		if countGatewayAudit(events, GatewayAuditRequestAdmitted) != 1 ||
			countGatewayAudit(events, GatewayAuditRequestTimeout) != 1 {
			t.Fatalf("timeout audit facts = %#v", events)
		}
	})
}

func TestGatewayHeadRebindIsAuditedWithExactSuccessorFence(t *testing.T) {
	fixture := newGatewayFixture(t, nil)
	recorder := &gatewaySecurityRecorder{}
	fixture.security = recorder
	cancel, completed := startGatewayFixture(
		t, fixture, gatewayBindingID, gatewayServerMessageID,
	)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	nextHead := fixture.head
	nextHead.Version++
	nextHead.JournalSequence++
	nextHead.TreeHash = lspDigest("9")
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeHeadRebind, 3, nextHead, nil,
		HeadRebindEnvelopePayload{Documents: []DocumentFence{fixture.document}},
	)}
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopePing, 4, nextHead, nil, PingEnvelopePayload{Nonce: "audited-rebind"},
	)}
	_ = receiveGatewayFrame(t, fixture.connection.writes, "rebind pong")
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
	_, events := recorder.snapshot()
	var rebound *GatewayAuditEvent
	for index := range events {
		if events[index].Action == GatewayAuditBindingRebind {
			rebound = &events[index]
		}
	}
	if rebound == nil || !rebound.Head.Equal(nextHead) || rebound.Document != nil || rebound.Method != "" {
		t.Fatalf("rebind audit fact = %#v events=%#v", rebound, events)
	}
}

func TestGatewayAuthorityOutageIsUnavailableButSemanticDriftIsStale(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "outage", err: ErrAuthorityUnavailable, want: ErrGatewayUnavailable},
		{name: "semantic drift", err: ErrRuntimeBindingStale, want: ErrGatewayStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayFixture(t, nil)
			fixture.authority.failAt, fixture.authority.failErr = 1, test.err
			_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
			_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
			_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
			select {
			case err := <-completed:
				if !errors.Is(err, test.want) {
					t.Fatalf("authority classification = %v, want %v", err, test.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("authority failure did not close Gateway")
			}
		})
	}
}
