package qualificationrelease

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var (
	testTarget = Target{
		WorkflowRunID:    uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		PublishNodeRunID: uuid.MustParse("22222222-2222-4222-8222-222222222222"),
	}
	testAuthorization = Authorization{
		AuthorizationID:         uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		OperationID:             uuid.MustParse("44444444-4444-4444-8444-444444444444"),
		ProjectID:               uuid.MustParse("55555555-5555-4555-8555-555555555555"),
		WorkflowRunID:           testTarget.WorkflowRunID,
		PublishNodeRunID:        testTarget.PublishNodeRunID,
		ExpectedProductionRunID: uuid.MustParse("66666666-6666-4666-8666-666666666666"),
		AuthorizationHash:       testHash,
		AuthorizationDocument:   []byte(`{"schemaVersion":"test"}`),
	}
	testClaimID    = uuid.MustParse("77777777-7777-4777-8777-777777777777")
	testController = ControllerIdentity{
		SchemaVersion: ControllerIdentitySchemaVersion,
		ID:            "controller", Version: "2026.07.20+test",
		Protocol: ControllerProtocolV3, TrustKeyDigest: testHash,
	}
)

func testClaim() Claim {
	return Claim{
		ClaimEventID: testClaimID, AuthorizationID: testAuthorization.AuthorizationID,
		Attempt: 1, Owner: "qualified-worker/01", Active: true, Hash: testHash,
		LeaseExpiresAt: time.Now().UTC().Truncate(time.Millisecond).Add(30 * time.Second),
	}
}

func testBinding() ControllerBinding {
	return ControllerBinding{
		AuthorizationID:       testAuthorization.AuthorizationID,
		ProductionRunID:       testAuthorization.ExpectedProductionRunID,
		ControllerOperationID: testAuthorization.ExpectedProductionRunID,
		ProjectID:             testAuthorization.ProjectID, RequestHash: testHash,
		Controller: testController, Hash: testHash,
	}
}

func healthyRecord() TerminalRecord {
	return TerminalRecord{
		AuthorizationID: testAuthorization.AuthorizationID,
		Outcome:         "healthy", ResultHash: testHash,
		PublishResult: &PublishResult{
			URL: "https://application.example.test", DeploymentID: "88888888-8888-4888-8888-888888888888",
		},
	}
}

func failureRecord(outcome string) TerminalRecord {
	codes := map[string]string{
		"production_failed":    "release_production_checks_failed",
		"controller_rejected":  "release_controller_rejected",
		"pre_submit_cancelled": "release_cancelled_before_submission",
	}
	return TerminalRecord{
		AuthorizationID: testAuthorization.AuthorizationID,
		Outcome:         outcome, ResultHash: testHash,
		Failure: &Failure{
			SchemaVersion: "worksflow-qualification-release-workflow-failure/v1",
			Code:          codes[outcome], Outcome: outcome,
		},
	}
}

type fakeAtomicStore struct {
	mu sync.Mutex

	claimCalls             int
	inspectClaimCalls      int
	renewCalls             int
	startCalls             int
	inspectControllerCalls int
	recordHealthyCalls     int
	inspectHealthyCalls    int
	recordFailureCalls     int
	inspectFailureCalls    int
	applyHealthyCalls      int
	applyFailureCalls      int
	claimIDs               []uuid.UUID

	claimFn             func(int, uuid.UUID) (Claim, error)
	inspectClaimFn      func(int, uuid.UUID) (Claim, error)
	renewFn             func(int, Claim, time.Time, time.Duration) (Claim, error)
	startFn             func(int) (ControllerBinding, error)
	inspectControllerFn func(int) (ControllerBinding, error)
	recordHealthyFn     func(int) (TerminalRecord, error)
	inspectHealthyFn    func(int) (TerminalRecord, error)
	recordFailureFn     func(int) (TerminalRecord, error)
	inspectFailureFn    func(int) (TerminalRecord, error)
	applyHealthyFn      func(int, Claim) (TerminalRecord, error)
	applyFailureFn      func(int, Claim) (TerminalRecord, error)
}

func (store *fakeAtomicStore) Resolve(context.Context, Target) (Authorization, error) {
	return testAuthorization, nil
}

func (store *fakeAtomicStore) Claim(
	_ context.Context,
	_ Authorization,
	claimID uuid.UUID,
	_ string,
	_ time.Duration,
) (Claim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCalls++
	store.claimIDs = append(store.claimIDs, claimID)
	if store.claimFn != nil {
		return store.claimFn(store.claimCalls, claimID)
	}
	claim := testClaim()
	claim.ClaimEventID = claimID
	return claim, nil
}

func (store *fakeAtomicStore) InspectClaim(
	_ context.Context,
	_ Authorization,
	claimID uuid.UUID,
) (Claim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspectClaimCalls++
	if store.inspectClaimFn != nil {
		return store.inspectClaimFn(store.inspectClaimCalls, claimID)
	}
	claim := testClaim()
	claim.ClaimEventID = claimID
	return claim, nil
}

func (store *fakeAtomicStore) Renew(
	_ context.Context,
	_ Authorization,
	claim Claim,
	expected time.Time,
	extension time.Duration,
) (Claim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.renewCalls++
	if store.renewFn != nil {
		return store.renewFn(store.renewCalls, claim, expected, extension)
	}
	claim.LeaseExpiresAt = expected.Add(extension / 2)
	return claim, nil
}

func (store *fakeAtomicStore) Start(context.Context, Authorization, Claim) (ControllerBinding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.startCalls++
	if store.startFn != nil {
		return store.startFn(store.startCalls)
	}
	return testBinding(), nil
}

func (store *fakeAtomicStore) InspectController(context.Context, Authorization) (ControllerBinding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspectControllerCalls++
	if store.inspectControllerFn != nil {
		return store.inspectControllerFn(store.inspectControllerCalls)
	}
	return testBinding(), nil
}

func (store *fakeAtomicStore) RecordHealthy(context.Context, Authorization) (TerminalRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.recordHealthyCalls++
	if store.recordHealthyFn != nil {
		return store.recordHealthyFn(store.recordHealthyCalls)
	}
	return healthyRecord(), nil
}

func (store *fakeAtomicStore) InspectHealthy(context.Context, Authorization) (TerminalRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspectHealthyCalls++
	if store.inspectHealthyFn != nil {
		return store.inspectHealthyFn(store.inspectHealthyCalls)
	}
	return healthyRecord(), nil
}

func (store *fakeAtomicStore) RecordFailure(context.Context, Authorization) (TerminalRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.recordFailureCalls++
	if store.recordFailureFn != nil {
		return store.recordFailureFn(store.recordFailureCalls)
	}
	return failureRecord("production_failed"), nil
}

func (store *fakeAtomicStore) InspectFailure(context.Context, Authorization) (TerminalRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspectFailureCalls++
	if store.inspectFailureFn != nil {
		return store.inspectFailureFn(store.inspectFailureCalls)
	}
	return failureRecord("production_failed"), nil
}

func (store *fakeAtomicStore) ApplyHealthy(_ context.Context, _ Authorization, claim Claim) (TerminalRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.applyHealthyCalls++
	if store.applyHealthyFn != nil {
		return store.applyHealthyFn(store.applyHealthyCalls, claim)
	}
	return healthyRecord(), nil
}

func (store *fakeAtomicStore) ApplyFailure(_ context.Context, _ Authorization, claim Claim) (TerminalRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.applyFailureCalls++
	if store.applyFailureFn != nil {
		return store.applyFailureFn(store.applyFailureCalls, claim)
	}
	return failureRecord("production_failed"), nil
}

type fakeObserver struct {
	observe func(context.Context, ControllerBinding) (ControllerOutcome, error)
}

func (observer fakeObserver) Observe(ctx context.Context, binding ControllerBinding) (ControllerOutcome, error) {
	return observer.observe(ctx, binding)
}

func newTestPublisher(t *testing.T, store AtomicStore, observer ControllerObserver, ids func() uuid.UUID) *QualifiedReleaseControllerPublisher {
	t.Helper()
	publisher, err := NewQualifiedReleaseControllerPublisher(store, observer, PublisherConfig{
		LeaseDuration: 30 * time.Second, PollInterval: 100 * time.Millisecond,
		HeartbeatInterval: 100 * time.Millisecond, IDs: ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func TestPublisherReconcilesEveryCommitUnknownWithSameIdentity(t *testing.T) {
	store := &fakeAtomicStore{}
	store.claimFn = func(call int, claimID uuid.UUID) (Claim, error) {
		if call == 1 {
			return Claim{}, ErrStoreOutcomeUnknown
		}
		claim := testClaim()
		claim.ClaimEventID = claimID
		return claim, nil
	}
	store.inspectClaimFn = func(call int, _ uuid.UUID) (Claim, error) {
		if call == 1 {
			return Claim{}, ErrNotFound
		}
		return testClaim(), nil
	}
	store.startFn = func(call int) (ControllerBinding, error) {
		if call == 1 {
			return ControllerBinding{}, ErrStoreOutcomeUnknown
		}
		return testBinding(), nil
	}
	store.inspectControllerFn = func(int) (ControllerBinding, error) {
		return ControllerBinding{}, ErrNotFound
	}
	store.recordHealthyFn = func(call int) (TerminalRecord, error) {
		if call == 1 {
			return TerminalRecord{}, ErrStoreOutcomeUnknown
		}
		return healthyRecord(), nil
	}
	store.inspectHealthyFn = func(int) (TerminalRecord, error) {
		return TerminalRecord{}, ErrNotFound
	}
	store.applyHealthyFn = func(call int, _ Claim) (TerminalRecord, error) {
		if call == 1 {
			return TerminalRecord{}, ErrStoreOutcomeUnknown
		}
		return healthyRecord(), nil
	}
	var allocated atomic.Int32
	publisher := newTestPublisher(t, store, fakeObserver{observe: func(context.Context, ControllerBinding) (ControllerOutcome, error) {
		return ControllerOutcome{Kind: OutcomeHealthy, RunState: "healthy", RemoteState: "completed", RunVersion: 2}, nil
	}}, func() uuid.UUID {
		allocated.Add(1)
		return testClaimID
	})

	record, err := publisher.Publish(context.Background(), testTarget, "qualified-worker/01")
	if err != nil || record.Outcome != "healthy" {
		t.Fatalf("Publish() = %#v, %v", record, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if allocated.Load() != 1 || store.claimCalls != 2 || len(store.claimIDs) != 2 ||
		store.claimIDs[0] != testClaimID || store.claimIDs[1] != testClaimID ||
		store.inspectClaimCalls != 1 || store.startCalls != 2 ||
		store.inspectControllerCalls != 1 || store.recordHealthyCalls != 2 ||
		store.inspectHealthyCalls != 1 || store.applyHealthyCalls != 2 {
		t.Fatalf("unknown-outcome replay accounting: allocated=%d store=%+v", allocated.Load(), store)
	}
}

func TestPublisherHeartbeatUsesMonotonicScheduleAndDatabaseExpiry(t *testing.T) {
	store := &fakeAtomicStore{}
	renewed := make(chan struct{}, 1)
	store.renewFn = func(_ int, claim Claim, expected time.Time, extension time.Duration) (Claim, error) {
		if expected != claim.LeaseExpiresAt || extension != 30*time.Second {
			t.Fatalf("Renew expected/extension = %s/%s", expected, extension)
		}
		claim.LeaseExpiresAt = expected.Add(5 * time.Second)
		renewed <- struct{}{}
		return claim, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer := fakeObserver{observe: func(ctx context.Context, _ ControllerBinding) (ControllerOutcome, error) {
		select {
		case <-renewed:
			return ControllerOutcome{Kind: OutcomeHealthy, RunState: "healthy", RemoteState: "completed", RunVersion: 3}, nil
		case <-ctx.Done():
			return ControllerOutcome{}, ctx.Err()
		}
	}}
	publisher := newTestPublisher(t, store, observer, func() uuid.UUID { return testClaimID })
	record, err := publisher.Publish(ctx, testTarget, "qualified-worker/01")
	cancel()
	if err != nil || record.Outcome != "healthy" {
		t.Fatalf("Publish() = %#v, %v", record, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.renewCalls != 1 {
		t.Fatalf("renew calls = %d, want 1", store.renewCalls)
	}
	if !containsSQLClockRenewal(renewLeaseQuery) {
		t.Fatalf("renew SQL does not generate expiry from database clock: %s", renewLeaseQuery)
	}
}

func TestPublisherLeaseLossNeverWritesTerminalWorkflowState(t *testing.T) {
	store := &fakeAtomicStore{}
	store.renewFn = func(int, Claim, time.Time, time.Duration) (Claim, error) {
		return Claim{}, ErrNotReady
	}
	publisher := newTestPublisher(t, store, fakeObserver{observe: func(context.Context, ControllerBinding) (ControllerOutcome, error) {
		return ControllerOutcome{Kind: OutcomeActive, RunState: "reconcile_blocked", RemoteState: "running", RunVersion: 4}, nil
	}}, func() uuid.UUID { return testClaimID })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := publisher.Publish(ctx, testTarget, "qualified-worker/01")
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("lease loss error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.recordHealthyCalls != 0 || store.recordFailureCalls != 0 ||
		store.applyHealthyCalls != 0 || store.applyFailureCalls != 0 {
		t.Fatalf("lease loss wrote terminal state: %+v", store)
	}
}

func TestPublisherAppliesEveryMigration84TerminalFailure(t *testing.T) {
	for name, fixture := range map[string]struct {
		runState string
		remote   string
		outcome  string
	}{
		"production failed":    {runState: "failed", remote: "completed", outcome: "production_failed"},
		"controller rejected":  {runState: "error", remote: "rejected", outcome: "controller_rejected"},
		"pre-submit cancelled": {runState: "cancelled", remote: "prepared", outcome: "pre_submit_cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeAtomicStore{}
			store.recordFailureFn = func(int) (TerminalRecord, error) { return failureRecord(fixture.outcome), nil }
			store.applyFailureFn = func(int, Claim) (TerminalRecord, error) { return failureRecord(fixture.outcome), nil }
			publisher := newTestPublisher(t, store, fakeObserver{observe: func(context.Context, ControllerBinding) (ControllerOutcome, error) {
				return ControllerOutcome{Kind: OutcomeFailed, RunState: fixture.runState, RemoteState: fixture.remote, RunVersion: 5}, nil
			}}, func() uuid.UUID { return testClaimID })
			record, err := publisher.Publish(context.Background(), testTarget, "qualified-worker/01")
			if err != nil || record.Outcome != fixture.outcome || record.Failure == nil {
				t.Fatalf("Publish() = %#v, %v", record, err)
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if store.recordHealthyCalls != 0 || store.applyHealthyCalls != 0 ||
				store.recordFailureCalls != 1 || store.applyFailureCalls != 1 {
				t.Fatalf("terminal failure routing = %+v", store)
			}
		})
	}
}

func TestPublisherCancellationLeavesActiveControllerNonterminal(t *testing.T) {
	store := &fakeAtomicStore{}
	observed := make(chan struct{}, 1)
	publisher := newTestPublisher(t, store, fakeObserver{observe: func(context.Context, ControllerBinding) (ControllerOutcome, error) {
		select {
		case observed <- struct{}{}:
		default:
		}
		return ControllerOutcome{Kind: OutcomeActive, RunState: "reconcile_blocked", RemoteState: "running", RunVersion: 4}, nil
	}}, func() uuid.UUID { return testClaimID })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := publisher.Publish(ctx, testTarget, "qualified-worker/01")
		done <- err
	}()
	<-observed
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.recordHealthyCalls != 0 || store.recordFailureCalls != 0 {
		t.Fatalf("cancellation persisted terminal result: %+v", store)
	}
}

func containsSQLClockRenewal(query string) bool {
	return stringsContainsAll(query, "clock_timestamp()", "$8 * interval '1 millisecond'", "date_trunc")
}

func stringsContainsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
