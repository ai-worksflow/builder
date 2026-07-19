package verification

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type candidateWorkerStoreFake struct {
	mu            sync.Mutex
	lease         CandidateExecutionLease
	plan          Plan
	claimed       bool
	transitions   []RunState
	heartbeats    int
	heartbeatErr  error
	heartbeatHit  chan struct{}
	heartbeatOnce sync.Once
	transitionAt  RunState
	transitionErr error
	committed     *CommitCandidateReceiptInput
	cleanupLease  *VerificationCleanupLease
	cleanupFailed string
	cleanupDone   bool
}

func (store *candidateWorkerStoreFake) ReconcileInactiveVerificationExecution(
	context.Context,
	Scope,
	string,
) (bool, error) {
	return false, nil
}

func (store *candidateWorkerStoreFake) ClaimVerificationCleanup(
	_ context.Context,
	input ClaimVerificationCleanupInput,
) (VerificationCleanupLease, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cleanupLease == nil {
		return VerificationCleanupLease{}, false, nil
	}
	lease := *store.cleanupLease
	store.cleanupLease = nil
	lease.WorkerID = input.WorkerID
	return lease, true, nil
}

func (store *candidateWorkerStoreFake) CompleteVerificationCleanup(
	_ context.Context,
	_ CompleteVerificationCleanupInput,
) error {
	store.cleanupDone = true
	return nil
}

func (store *candidateWorkerStoreFake) FailVerificationCleanup(
	_ context.Context,
	input FailVerificationCleanupInput,
) error {
	store.cleanupFailed = input.Reason
	return nil
}

func (store *candidateWorkerStoreFake) ConfirmVerificationOperationQuiesced(
	context.Context,
	Scope,
	VerificationExecutionFence,
	string,
	string,
) error {
	return nil
}

func (store *candidateWorkerStoreFake) ClaimCandidateExecution(
	_ context.Context,
	input ClaimCandidateExecutionInput,
) (CandidateExecutionLease, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.claimed {
		return CandidateExecutionLease{}, false, nil
	}
	store.claimed = true
	store.lease.AttemptID = input.AttemptID
	store.lease.WorkerID = input.WorkerID
	return store.lease, true, nil
}

func (store *candidateWorkerStoreFake) HeartbeatCandidateExecution(
	_ context.Context,
	input HeartbeatCandidateExecutionInput,
) (CandidateExecutionLease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.heartbeatErr != nil {
		return CandidateExecutionLease{}, store.heartbeatErr
	}
	if input.Lease.RunVersion != store.lease.RunVersion ||
		input.Lease.AttemptVersion != store.lease.AttemptVersion {
		return CandidateExecutionLease{}, ErrWorkerLeaseLost
	}
	store.heartbeats++
	store.lease.RunVersion++
	store.lease.AttemptVersion++
	store.lease.LeaseExpiresAt = store.lease.LeaseExpiresAt.Add(time.Second)
	store.heartbeatOnce.Do(func() {
		if store.heartbeatHit != nil {
			close(store.heartbeatHit)
		}
	})
	return store.lease, nil
}

func (store *candidateWorkerStoreFake) TransitionCandidateExecution(
	_ context.Context,
	input TransitionCandidateExecutionInput,
) (CandidateExecutionLease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if input.Target == store.transitionAt && store.transitionErr != nil {
		return CandidateExecutionLease{}, store.transitionErr
	}
	if input.Lease.State != store.lease.State ||
		input.Lease.RunVersion != store.lease.RunVersion ||
		input.Lease.AttemptVersion != store.lease.AttemptVersion {
		return CandidateExecutionLease{}, ErrWorkerLeaseLost
	}
	store.lease.State = input.Target
	store.lease.RunVersion++
	store.lease.AttemptVersion++
	store.transitions = append(store.transitions, input.Target)
	return store.lease, nil
}

func (store *candidateWorkerStoreFake) GetExecutionPlan(
	context.Context,
	string,
	string,
) (Plan, error) {
	return store.plan, nil
}

func (store *candidateWorkerStoreFake) CommitCandidateReceipt(
	_ context.Context,
	input CommitCandidateReceiptInput,
) (Receipt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if input.Lease.State != RunCollecting ||
		input.Lease.RunVersion != store.lease.RunVersion ||
		input.Lease.AttemptVersion != store.lease.AttemptVersion {
		return Receipt{}, ErrWorkerLeaseLost
	}
	copy := input
	store.committed = &copy
	return input.Receipt, nil
}

func (store *candidateWorkerStoreFake) CompleteCandidateExecutionCleanup(
	_ context.Context,
	_ CandidateExecutionLease,
	_ string,
) error {
	store.cleanupDone = true
	return nil
}

type candidateWorkerMaterializerFake struct {
	heartbeat  <-chan struct{}
	block      bool
	started    chan struct{}
	prepareErr error
}

func (materializer candidateWorkerMaterializerFake) Materialize(
	ctx context.Context,
	_ CandidateExecutionSpec,
) error {
	if materializer.started != nil {
		close(materializer.started)
	}
	if materializer.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if materializer.heartbeat != nil {
		select {
		case <-materializer.heartbeat:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (materializer candidateWorkerMaterializerFake) Prepare(context.Context, CandidateExecutionSpec) error {
	return materializer.prepareErr
}

func (candidateWorkerMaterializerFake) Collect(context.Context, CandidateExecutionSpec) error {
	return nil
}

func (candidateWorkerMaterializerFake) CleanupCandidate(context.Context, VerificationExecutionFence) error {
	return nil
}

type candidateWorkerExecutorFake struct {
	failedCheck string
	requests    []CheckExecutionRequest
}

func (executor *candidateWorkerExecutorFake) Execute(
	_ context.Context,
	request CheckExecutionRequest,
) (CheckExecutionOutcome, error) {
	executor.requests = append(executor.requests, request)
	code := 0
	status := CheckPassed
	if request.Check.ID == executor.failedCheck {
		code = 1
		status = CheckFailed
	}
	return CheckExecutionOutcome{
		Status: status, ExitCode: &code, Diagnostics: []Diagnostic{},
	}, nil
}

type candidateWorkerClockFake struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *candidateWorkerClockFake) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.now
	clock.now = clock.now.Add(time.Second)
	return value
}

type candidateWorkerIDsFake struct {
	mu     sync.Mutex
	values []string
}

func (ids *candidateWorkerIDsFake) NewID() string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	value := ids.values[0]
	ids.values = ids.values[1:]
	return value
}

func TestCandidateWorkerProducesPassingImmutableReceipt(t *testing.T) {
	store, clock, ids := newCandidateWorkerFixture(t)
	store.heartbeatHit = make(chan struct{})
	executor := &candidateWorkerExecutorFake{}
	worker, err := NewCandidateWorker(
		store,
		candidateWorkerMaterializerFake{heartbeat: store.heartbeatHit},
		executor,
		candidateWorkerTestConfig(),
		clock,
		ids,
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%t err=%v", processed, err)
	}
	if store.heartbeats == 0 {
		t.Fatal("long-running materialization did not heartbeat")
	}
	assertCandidateWorkerTransitions(t, store.transitions)
	if store.committed == nil || store.committed.Receipt.Decision != DecisionPassed ||
		store.committed.TerminalReason != "" || len(store.committed.Receipt.Checks) != 2 {
		t.Fatalf("committed Receipt = %#v", store.committed)
	}
	if len(executor.requests) != 2 ||
		executor.requests[0].Check.Argv[0] != store.plan.Content.Checks[0].Argv[0] ||
		executor.requests[0].Check.VerifierImageDigest != store.plan.Content.Checks[0].VerifierImageDigest {
		t.Fatalf("executor request was not derived from immutable Plan: %#v", executor.requests)
	}
}

func TestCandidateWorkerPersistsFailedRequiredCheckDecision(t *testing.T) {
	store, clock, ids := newCandidateWorkerFixture(t)
	executor := &candidateWorkerExecutorFake{failedCheck: "oracle-contract"}
	worker, err := NewCandidateWorker(
		store, candidateWorkerMaterializerFake{}, executor,
		candidateWorkerTestConfig(), clock, ids,
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%t err=%v", processed, err)
	}
	if store.committed == nil || store.committed.Receipt.Decision != DecisionFailed ||
		store.committed.TerminalReason == "" {
		t.Fatalf("failed Receipt = %#v", store.committed)
	}
}

func TestFullStackCandidateFailuresNeverProduceFreezeAuthority(t *testing.T) {
	tests := []struct {
		name        string
		failedCheck string
		prepareErr  error
	}{
		{name: "migration", failedCheck: "migration-empty"},
		{name: "service health", failedCheck: "service-health"},
		{name: "tenant isolation", failedCheck: "tenant-isolation"},
		{name: "contract", failedCheck: "oracle-contract"},
		{name: "service startup", prepareErr: errors.New("api did not become healthy")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validCandidatePlanInput()
			input.Profile.NetworkPolicy = map[string]any{
				"dependencyResolver": map[string]any{"network": "resolver-egress"},
				"postgres": map[string]any{
					"image":    "postgres@sha256:" + strings.Repeat("1", 64),
					"database": "verification", "user": "verification", "runtimeUser": "999:999",
				},
				"services": []any{map[string]any{
					"id": "api", "image": "python@sha256:" + strings.Repeat("2", 64),
					"workingDirectory": "apps/api", "argv": []any{"python", "-m", "api"},
					"healthArgv": []any{"python", "-m", "api.health"},
				}},
			}
			input.Profile.BuiltInChecks = []ProfileBuiltInCheck{
				{ID: "migration-empty", Kind: "migration", ImageRole: "python", Argv: []string{"python", "-m", "api.migrate", "empty"}, WorkingDirectory: "apps/api", Required: true, TimeoutSeconds: 60},
				{ID: "migration-upgrade", Kind: "migration", ImageRole: "python", Argv: []string{"python", "-m", "api.migrate", "upgrade"}, WorkingDirectory: "apps/api", Required: true, DependsOn: []string{"migration-empty"}, TimeoutSeconds: 60},
				{ID: "migration-repeat", Kind: "migration", ImageRole: "python", Argv: []string{"python", "-m", "api.migrate", "repeat"}, WorkingDirectory: "apps/api", Required: true, DependsOn: []string{"migration-upgrade"}, TimeoutSeconds: 60},
				{ID: "service-health", Kind: "health", ImageRole: "python", Argv: []string{"python", "-m", "api.health"}, WorkingDirectory: "apps/api", Required: true, DependsOn: []string{"migration-repeat"}, TimeoutSeconds: 60},
				{ID: "tenant-isolation", Kind: "tenant", ImageRole: "python", Argv: []string{"pytest", "tests/integration/tenant"}, WorkingDirectory: "apps/api", Required: true, DependsOn: []string{"service-health"}, TimeoutSeconds: 60},
			}
			compiled, err := (PlanCompiler{}).Compile(input)
			if err != nil {
				t.Fatal(err)
			}
			store, clock, ids := newCandidateWorkerFixture(t)
			store.plan.Content, store.plan.PlanHash = compiled.Content, compiled.PlanHash
			store.lease.ProjectID = compiled.Content.ProjectID
			store.lease.Plan.ContentHash = compiled.PlanHash
			executor := &candidateWorkerExecutorFake{failedCheck: test.failedCheck}
			worker, err := NewCandidateWorker(
				store, candidateWorkerMaterializerFake{prepareErr: test.prepareErr}, executor,
				candidateWorkerTestConfig(), clock, ids,
			)
			if err != nil {
				t.Fatal(err)
			}
			if processed, err := worker.RunOnce(context.Background()); err != nil || !processed {
				t.Fatalf("RunOnce processed=%t err=%v", processed, err)
			}
			if store.committed == nil || store.committed.Receipt.Decision == DecisionPassed {
				t.Fatalf("full-stack failure produced passing Receipt: %#v", store.committed)
			}
			if _, err := store.committed.Receipt.PassedReference(); !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("full-stack failure produced Freeze authority: %v", err)
			}
		})
	}
}

func TestCandidateWorkerStopsOnLeaseLossWithoutReceipt(t *testing.T) {
	store, clock, ids := newCandidateWorkerFixture(t)
	store.heartbeatErr = errors.New("run was cancelled")
	worker, err := NewCandidateWorker(
		store, candidateWorkerMaterializerFake{block: true},
		&candidateWorkerExecutorFake{}, candidateWorkerTestConfig(), clock, ids,
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if !processed || !errors.Is(err, ErrWorkerLeaseLost) {
		t.Fatalf("RunOnce processed=%t err=%v", processed, err)
	}
	if store.committed != nil {
		t.Fatalf("lease-lost execution committed Receipt: %#v", store.committed)
	}
}

func newCandidateWorkerFixture(
	t *testing.T,
) (*candidateWorkerStoreFake, *candidateWorkerClockFake, *candidateWorkerIDsFake) {
	t.Helper()
	compiled, err := (PlanCompiler{}).Compile(validCandidatePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	planID := uuid.NewString()
	attemptID := uuid.NewString()
	runID := uuid.NewString()
	plan := Plan{
		ID: planID, Content: compiled.Content, PlanHash: compiled.PlanHash,
		CreatedBy: uuid.NewString(), CreatedAt: time.Now().UTC(),
	}
	store := &candidateWorkerStoreFake{
		plan: plan,
		lease: CandidateExecutionLease{
			ProjectID: compiled.Content.ProjectID, RunID: runID,
			Plan:           PlanReference{ID: planID, ContentHash: compiled.PlanHash},
			AttemptOrdinal: 1, State: RunClaimed,
			RunVersion: 2, RunFenceEpoch: 1,
			AttemptVersion: 2, AttemptFenceEpoch: 1,
			WorkerID: "quality-worker-1", LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
		},
	}
	clock := &candidateWorkerClockFake{now: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)}
	ids := &candidateWorkerIDsFake{values: []string{attemptID, uuid.NewString()}}
	return store, clock, ids
}

func candidateWorkerTestConfig() CandidateWorkerConfig {
	return CandidateWorkerConfig{
		ActorID: uuid.NewString(), WorkerID: "quality-worker-1",
		LeaseDuration: time.Second, HeartbeatInterval: time.Millisecond,
	}
}

func assertCandidateWorkerTransitions(t *testing.T, actual []RunState) {
	t.Helper()
	expected := []RunState{RunMaterializing, RunPreparing, RunRunning, RunCollecting}
	if len(actual) != len(expected) {
		t.Fatalf("transitions = %#v", actual)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("transitions = %#v", actual)
		}
	}
}
