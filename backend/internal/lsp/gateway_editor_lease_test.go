package lsp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type gatewayLeaseClock struct {
	mu  sync.Mutex
	now time.Time
}

type gatewayEditorLeaseStoreNoop struct {
	contract GatewayEditorLeaseContract
}

func (store gatewayEditorLeaseStoreNoop) Contract() GatewayEditorLeaseContract {
	return store.contract
}

func (gatewayEditorLeaseStoreNoop) AcquireGatewayEditorLease(
	context.Context,
	GatewayEditorLeaseInput,
) (bool, error) {
	return true, nil
}

func (gatewayEditorLeaseStoreNoop) RenewGatewayEditorLease(
	context.Context,
	GatewayEditorLeaseInput,
) (bool, error) {
	return true, nil
}

func (gatewayEditorLeaseStoreNoop) ReleaseGatewayEditorLease(
	context.Context,
	GatewayEditorLeaseInput,
) (bool, error) {
	return true, nil
}

func (clock *gatewayLeaseClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *gatewayLeaseClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func countLeaseCalls(calls []string, operation string) int {
	count := 0
	for _, call := range calls {
		if call == operation {
			count++
		}
	}
	return count
}

func waitForLeaseCalls(t *testing.T, recorder *gatewaySecurityRecorder, operation string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls, _ := recorder.leaseSnapshot()
		if countLeaseCalls(calls, operation) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease calls = %#v, want %d %s", calls, want, operation)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGatewaySecurityConstructionRequiresConstrainedEditorLease(t *testing.T) {
	if _, err := NewGatewaySecurity(gatewaySecurityNoop{}, gatewaySecurityNoop{}, nil); !errors.Is(err, ErrGatewaySecurityUnavailable) {
		t.Fatalf("nil editor lease store = %v", err)
	}
	invalid := gatewayEditorLeaseStoreNoop{contract: GatewayEditorLeaseContract{
		TTL: time.Second, HeartbeatInterval: time.Second,
	}}
	if _, err := NewGatewaySecurity(gatewaySecurityNoop{}, gatewaySecurityNoop{}, invalid); !errors.Is(err, ErrGatewaySecurityUnavailable) {
		t.Fatalf("unsafe editor lease contract = %v", err)
	}
	valid := gatewayEditorLeaseStoreNoop{contract: GatewayEditorLeaseContract{
		TTL: GatewayEditorLeaseTTL, HeartbeatInterval: GatewayEditorHeartbeatInterval,
	}}
	if _, err := NewGatewaySecurity(gatewaySecurityNoop{}, gatewaySecurityNoop{}, valid); err != nil {
		t.Fatalf("production editor lease contract = %v", err)
	}
}

func TestGatewayDuplicateEditorIsBlockedBeforeRuntimeStartAcrossActorHandoff(t *testing.T) {
	security := &gatewaySecurityRecorder{enforceLease: true}
	first := newGatewayFixture(t, nil)
	first.security = security
	cancelFirst, firstDone := startGatewayFixture(t, first, gatewayBindingID)
	_ = receiveGatewayFrame(t, first.process.writes, "first initialize")
	_ = receiveGatewayFrame(t, first.process.writes, "first initialized")
	_ = receiveGatewayFrame(t, first.connection.writes, "first bound")
	waitForLeaseCalls(t, security, "renew", 1)

	second := newGatewayFixture(t, nil)
	second.security = security
	second.grant.ActorID = uuid.NewString()
	_, secondDone := startGatewayFixture(t, second, gatewayServerMessageID)
	select {
	case err := <-secondDone:
		if !errors.Is(err, ErrGatewayEditorLeaseConflict) {
			t.Fatalf("duplicate editor = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("duplicate editor did not fail closed")
	}
	select {
	case start := <-second.runtime.starts:
		t.Fatalf("duplicate editor reached runtime.Start: %#v", start)
	default:
	}
	_, events := security.snapshot()
	if countGatewayAudit(events, GatewayAuditEditorLeaseConflict) != 1 {
		t.Fatalf("conflict audit facts = %#v", events)
	}
	if err := waitGatewayDone(t, cancelFirst, firstDone); err != nil {
		t.Fatal(err)
	}
}

func TestGatewaySnapshotBindingsAreConcurrentAndNeverTouchEditorLease(t *testing.T) {
	security := &gatewaySecurityRecorder{acquireDenied: true}
	first := newGatewayFixture(t, nil)
	second := newGatewayFixture(t, nil)
	first.grant.Mode, second.grant.Mode = TicketModeSnapshot, TicketModeSnapshot
	first.security, second.security = security, security
	cancelFirst, firstDone := startGatewayFixture(t, first, gatewayBindingID)
	cancelSecond, secondDone := startGatewayFixture(t, second, gatewayServerMessageID)
	for _, fixture := range []*gatewayFixture{first, second} {
		_ = receiveGatewayFrame(t, fixture.process.writes, "snapshot initialize")
		_ = receiveGatewayFrame(t, fixture.process.writes, "snapshot initialized")
		_ = receiveGatewayFrame(t, fixture.connection.writes, "snapshot bound")
	}
	if calls, inputs := security.leaseSnapshot(); len(calls) != 0 || len(inputs) != 0 {
		t.Fatalf("snapshot touched editor lease: calls=%#v inputs=%#v", calls, inputs)
	}
	if err := waitGatewayDone(t, cancelFirst, firstDone); err != nil {
		t.Fatal(err)
	}
	if err := waitGatewayDone(t, cancelSecond, secondDone); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayStrictActivityRenewsEditorLeaseButMalformedFrameDoesNot(t *testing.T) {
	security := &gatewaySecurityRecorder{}
	fixture := newGatewayFixture(t, nil)
	fixture.security = security
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID, gatewayServerMessageID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	waitForLeaseCalls(t, security, "renew", 1)

	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopeDocumentOpen, 2, fixture.head, &fixture.document,
		DocumentOpenEnvelopePayload{LanguageID: "typescript", Text: fixture.content},
	)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "didOpen")
	waitForLeaseCalls(t, security, "renew", 2)
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopePing, 3, fixture.head, nil, PingEnvelopePayload{Nonce: "heartbeat"},
	)}
	_ = receiveGatewayFrame(t, fixture.connection.writes, "pong")
	waitForLeaseCalls(t, security, "renew", 3)

	fixture.connection.reads <- gatewayFrameResult{value: []byte(`{"kind":"client.ping"}`)}
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("malformed envelope did not close Gateway")
	}
	calls, _ := security.leaseSnapshot()
	if countLeaseCalls(calls, "renew") != 3 || countLeaseCalls(calls, "release") != 1 {
		t.Fatalf("strict renewal/cleanup calls = %#v", calls)
	}
}

func TestGatewayBoundRenewalRestoresFullLeaseAfterNearMaxStartup(t *testing.T) {
	base := time.Now()
	clock := &gatewayLeaseClock{now: base}
	security := &gatewaySecurityRecorder{
		enforceLease: true, leaseNow: clock.Now,
		leaseContract: GatewayEditorLeaseContract{
			TTL: 90 * time.Millisecond, HeartbeatInterval: 30 * time.Millisecond,
		},
	}
	fixture := newGatewayFixture(t, nil)
	fixture.security = security
	gateway, err := newGateway(
		fixture.resolver, fixture.runtime, fixture.authority, security,
		sequenceGatewayIDs(gatewayBindingID, gatewayServerMessageID), clock.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	completed := make(chan error, 1)
	go func() { completed <- gateway.Serve(ctx, fixture.connection, fixture.grant, fixture.bind) }()
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	// 60/90 scales the production 20s/30s startup boundary. The original
	// acquire is now close to expiry before initialize completes.
	clock.Advance(59 * time.Millisecond)
	fixture.process.reads <- gatewayFrameResult{value: gatewayInitializeResponse(fixture.profile)}
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	waitForLeaseCalls(t, security, "renew", 1)
	clock.Advance(40 * time.Millisecond) // beyond acquire+TTL, inside bound-renew+TTL
	fixture.connection.reads <- gatewayFrameResult{value: gatewayClientEnvelopeJSON(
		t, ClientEnvelopePing, 2, fixture.head, nil, PingEnvelopePayload{Nonce: "after-slow-start"},
	)}
	_ = receiveGatewayFrame(t, fixture.connection.writes, "post-start pong")
	waitForLeaseCalls(t, security, "renew", 2)
	if err := waitGatewayDone(t, cancel, completed); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayDelayedHeartbeatSignalCannotExtendAbsoluteRenewalDeadline(t *testing.T) {
	base := time.Now()
	clock := &gatewayLeaseClock{now: base}
	security := &gatewaySecurityRecorder{}
	fixture := newGatewayFixture(t, nil)
	protocol, err := newEnvelopeProtocol(
		fixture.bind.ConnectionID, gatewayBindingID, fixture.head, fixture.profile,
		fixture.bind.Documents, sequenceGatewayIDs(gatewayServerMessageID),
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := newGateway(
		fixture.resolver, fixture.runtime, fixture.authority, security,
		sequenceGatewayIDs(gatewayServerMessageID), clock.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	state := &gatewayEditorLeaseState{
		input: GatewayEditorLeaseInput{
			ProjectID: fixture.grant.ProjectID, SessionID: fixture.grant.SessionID,
			ProfileID: fixture.profile.ID, ProfileContentHash: fixture.profile.ContentHash,
			CapabilityHash: fixture.profile.CapabilityHash, OwnerBindingID: gatewayBindingID,
		},
		contract: GatewayEditorLeaseContract{
			TTL: 90 * time.Millisecond, HeartbeatInterval: 30 * time.Millisecond,
		},
		protocol: protocol,
	}
	if err := state.recordSuccessfulRenewal(base); err != nil {
		t.Fatal(err)
	}
	clock.Advance(61 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &gatewaySession{
		gateway: gateway, grant: fixture.grant, profile: fixture.profile, protocol: protocol,
		ctx: ctx, cancel: cancel, editorLease: state, heartbeatReset: make(chan struct{}, 1),
	}
	// This signal represents an old renewal whose scheduler delivery was
	// delayed past the absolute deadline. It must not grant a fresh 60ms.
	session.heartbeatReset <- struct{}{}
	if err := session.heartbeatLoop(); !errors.Is(err, ErrGatewayEditorLeaseLost) {
		t.Fatalf("delayed heartbeat signal = %v", err)
	}
	_, events := security.snapshot()
	if countGatewayAudit(events, GatewayAuditEditorLeaseLost) != 1 {
		t.Fatalf("delayed heartbeat lost audit = %#v", events)
	}
}

func TestGatewayEditorIdleDeadlineTerminatesAndReleases(t *testing.T) {
	security := &gatewaySecurityRecorder{leaseContract: GatewayEditorLeaseContract{
		TTL: 90 * time.Millisecond, HeartbeatInterval: 30 * time.Millisecond,
	}}
	fixture := newGatewayFixture(t, nil)
	fixture.security = security
	_, completed := startGatewayFixture(t, fixture, gatewayBindingID)
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
	_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
	_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
	select {
	case err := <-completed:
		if !errors.Is(err, ErrGatewayEditorLeaseLost) {
			t.Fatalf("idle deadline = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle editor was not actively terminated")
	}
	calls, _ := security.leaseSnapshot()
	_, events := security.snapshot()
	if countLeaseCalls(calls, "release") != 1 || fixture.process.terminates.Load() != 1 ||
		countGatewayAudit(events, GatewayAuditEditorLeaseLost) != 1 ||
		countGatewayAudit(events, GatewayAuditEditorLeaseRelease) != 1 {
		t.Fatalf("idle cleanup calls=%#v terminate=%d events=%#v", calls, fixture.process.terminates.Load(), events)
	}
}

func TestGatewayEditorLeaseReleasesAcrossEveryPostAcquireStartupExit(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*gatewayFixture, *gatewaySecurityRecorder)
	}{
		{name: "acquire audit failure", setup: func(_ *gatewayFixture, security *gatewaySecurityRecorder) {
			security.failActions = map[string]error{GatewayAuditEditorLeaseAcquire: errors.New("audit unavailable")}
		}},
		{name: "runtime start failure", setup: func(fixture *gatewayFixture, _ *gatewaySecurityRecorder) {
			fixture.runtime.startErr = errors.New("start failed")
		}},
		{name: "runtime identity drift", setup: func(fixture *gatewayFixture, _ *gatewaySecurityRecorder) {
			fixture.process.profile = lspTestProfile("typescript-alt")
		}},
		{name: "initialize read failure", setup: func(fixture *gatewayFixture, _ *gatewaySecurityRecorder) {
			fixture.process.reads <- gatewayFrameResult{err: errors.New("initialize failed")}
		}},
		{name: "binding audit failure", setup: func(fixture *gatewayFixture, security *gatewaySecurityRecorder) {
			fixture.process.reads <- gatewayFrameResult{value: gatewayInitializeResponse(fixture.profile)}
			security.failActions = map[string]error{GatewayAuditBindingOpen: errors.New("audit unavailable")}
		}},
		{name: "bound write failure", setup: func(fixture *gatewayFixture, _ *gatewaySecurityRecorder) {
			fixture.process.reads <- gatewayFrameResult{value: gatewayInitializeResponse(fixture.profile)}
			fixture.connection.writeErr = errors.New("bound write failed")
		}},
		{name: "initial bound renewal fenced", setup: func(fixture *gatewayFixture, security *gatewaySecurityRecorder) {
			fixture.process.reads <- gatewayFrameResult{value: gatewayInitializeResponse(fixture.profile)}
			security.renewLost = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			security := &gatewaySecurityRecorder{}
			fixture := newGatewayFixture(t, nil)
			fixture.security = security
			test.setup(fixture, security)
			gateway, err := newGateway(
				fixture.resolver, fixture.runtime, fixture.authority, security,
				sequenceGatewayIDs(gatewayBindingID), time.Now,
			)
			if err != nil {
				t.Fatal(err)
			}
			if serveErr := gateway.Serve(context.Background(), fixture.connection, fixture.grant, fixture.bind); serveErr == nil {
				t.Fatal("startup exit unexpectedly succeeded")
			}
			calls, _ := security.leaseSnapshot()
			if countLeaseCalls(calls, "acquire") != 1 || countLeaseCalls(calls, "release") != 1 {
				t.Fatalf("post-acquire cleanup calls = %#v", calls)
			}
		})
	}
}

func TestGatewayLeaseStoreOutageFailsClosedBeforeRuntimeAndReleaseOutageIsTyped(t *testing.T) {
	t.Run("acquire", func(t *testing.T) {
		security := &gatewaySecurityRecorder{acquireErr: errors.New("Redis unavailable")}
		fixture := newGatewayFixture(t, nil)
		fixture.security = security
		gateway, err := newGateway(
			fixture.resolver, fixture.runtime, fixture.authority, security,
			sequenceGatewayIDs(gatewayBindingID), time.Now,
		)
		if err != nil {
			t.Fatal(err)
		}
		err = gateway.Serve(context.Background(), fixture.connection, fixture.grant, fixture.bind)
		if !errors.Is(err, ErrGatewaySecurityUnavailable) {
			t.Fatalf("acquire outage = %v", err)
		}
		select {
		case start := <-fixture.runtime.starts:
			t.Fatalf("lease outage reached runtime.Start: %#v", start)
		default:
		}
		calls, _ := security.leaseSnapshot()
		_, events := security.snapshot()
		if countLeaseCalls(calls, "release") != 0 || countGatewayAudit(events, GatewayAuditEditorLeaseLost) != 1 {
			t.Fatalf("acquire outage calls=%#v events=%#v", calls, events)
		}
	})

	t.Run("release", func(t *testing.T) {
		security := &gatewaySecurityRecorder{releaseErr: errors.New("Redis unavailable")}
		fixture := newGatewayFixture(t, nil)
		fixture.security = security
		cancel, completed := startGatewayFixture(t, fixture, gatewayBindingID)
		_ = receiveGatewayFrame(t, fixture.process.writes, "initialize")
		_ = receiveGatewayFrame(t, fixture.process.writes, "initialized")
		_ = receiveGatewayFrame(t, fixture.connection.writes, "bound")
		if err := waitGatewayDone(t, cancel, completed); !errors.Is(err, ErrGatewaySecurityUnavailable) {
			t.Fatalf("release outage = %v", err)
		}
		_, events := security.snapshot()
		if countGatewayAudit(events, GatewayAuditEditorLeaseLost) != 1 {
			t.Fatalf("release outage audit = %#v", events)
		}
	})
}
