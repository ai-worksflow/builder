package verification

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type verificationPhaseBlocker struct {
	target string
	cancel context.CancelFunc
	once   sync.Once
}

func (blocker *verificationPhaseBlocker) run(ctx context.Context, phase string) error {
	if blocker == nil || blocker.target != phase {
		return nil
	}
	blocker.once.Do(func() {
		if blocker.cancel != nil {
			blocker.cancel()
		}
	})
	<-ctx.Done()
	return ctx.Err()
}

type candidateCleanupMaterializerProbe struct {
	blocker            *verificationPhaseBlocker
	collectErr         error
	cleanupCalls       []VerificationExecutionFence
	cleanupContextLive bool
	cleanupHasDeadline bool
	cleanupErr         error
}

func (probe *candidateCleanupMaterializerProbe) Materialize(ctx context.Context, _ CandidateExecutionSpec) error {
	return probe.blocker.run(ctx, "materialize")
}

func (probe *candidateCleanupMaterializerProbe) Prepare(ctx context.Context, _ CandidateExecutionSpec) error {
	return probe.blocker.run(ctx, "prepare")
}

func (probe *candidateCleanupMaterializerProbe) Collect(ctx context.Context, _ CandidateExecutionSpec) error {
	if err := probe.blocker.run(ctx, "collect"); err != nil {
		return err
	}
	return probe.collectErr
}

func (probe *candidateCleanupMaterializerProbe) CleanupCandidate(
	ctx context.Context,
	fence VerificationExecutionFence,
) error {
	probe.cleanupCalls = append(probe.cleanupCalls, fence)
	probe.cleanupContextLive = ctx.Err() == nil
	_, probe.cleanupHasDeadline = ctx.Deadline()
	return probe.cleanupErr
}

type candidateCleanupExecutorProbe struct{ blocker *verificationPhaseBlocker }

func (probe candidateCleanupExecutorProbe) Execute(
	ctx context.Context,
	_ CheckExecutionRequest,
) (CheckExecutionOutcome, error) {
	if err := probe.blocker.run(ctx, "run"); err != nil {
		return CheckExecutionOutcome{}, err
	}
	code := 0
	return CheckExecutionOutcome{Status: CheckPassed, ExitCode: &code, Diagnostics: []Diagnostic{}}, nil
}

func TestCandidateWorkerAlwaysCleansExactFenceAfterCancellationTimeoutOrLeaseLoss(t *testing.T) {
	for _, stop := range []string{"cancel", "deadline", "lease-loss"} {
		for _, phase := range []string{"materialize", "prepare", "run", "collect"} {
			t.Run(stop+"/"+phase, func(t *testing.T) {
				store, clock, ids := newCandidateWorkerFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				if stop == "deadline" {
					ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
				}
				defer cancel()
				blocker := &verificationPhaseBlocker{target: phase}
				if stop == "cancel" {
					blocker.cancel = cancel
				} else if stop == "lease-loss" {
					store.heartbeatErr = errors.New("lease was reclaimed")
				}
				materializer := &candidateCleanupMaterializerProbe{blocker: blocker}
				worker, err := NewCandidateWorker(
					store, materializer, candidateCleanupExecutorProbe{blocker: blocker},
					candidateWorkerTestConfig(), clock, ids,
				)
				if err != nil {
					t.Fatal(err)
				}
				processed, runErr := worker.RunOnce(ctx)
				if !processed {
					t.Fatal("claimed execution was not reported as processed")
				}
				if stop == "cancel" && !errors.Is(runErr, context.Canceled) {
					t.Fatalf("cancelled RunOnce error = %v", runErr)
				}
				if stop == "deadline" && !errors.Is(runErr, context.DeadlineExceeded) {
					t.Fatalf("deadline RunOnce error = %v", runErr)
				}
				if stop == "lease-loss" && !errors.Is(runErr, ErrWorkerLeaseLost) {
					t.Fatalf("lease-lost RunOnce error = %v", runErr)
				}
				if store.committed != nil {
					t.Fatalf("stopped worker terminalized after authority loss: %#v", store.committed)
				}
				if len(materializer.cleanupCalls) != 1 ||
					materializer.cleanupCalls[0].AttemptID != store.lease.AttemptID ||
					materializer.cleanupCalls[0].AttemptFenceEpoch != store.lease.AttemptFenceEpoch {
					t.Fatalf("exact cleanup calls = %#v, lease = %#v", materializer.cleanupCalls, store.lease)
				}
				if !materializer.cleanupContextLive || !materializer.cleanupHasDeadline {
					t.Fatal("cleanup did not receive an independent bounded context")
				}
			})
		}
	}
}

type canonicalCleanupMaterializerProbe struct {
	blocker            *verificationPhaseBlocker
	collectErr         error
	cleanupCalls       []VerificationExecutionFence
	cleanupContextLive bool
	cleanupHasDeadline bool
	cleanupErr         error
}

func (probe *canonicalCleanupMaterializerProbe) MaterializeCanonical(
	ctx context.Context,
	_ CanonicalExecutionSpec,
) error {
	return probe.blocker.run(ctx, "materialize")
}

func (probe *canonicalCleanupMaterializerProbe) PrepareCanonical(
	ctx context.Context,
	_ CanonicalExecutionSpec,
) error {
	return probe.blocker.run(ctx, "prepare")
}

func (probe *canonicalCleanupMaterializerProbe) CollectCanonical(
	ctx context.Context,
	_ CanonicalExecutionSpec,
) error {
	if err := probe.blocker.run(ctx, "collect"); err != nil {
		return err
	}
	return probe.collectErr
}

func (probe *canonicalCleanupMaterializerProbe) CleanupCanonical(
	ctx context.Context,
	fence VerificationExecutionFence,
) error {
	probe.cleanupCalls = append(probe.cleanupCalls, fence)
	probe.cleanupContextLive = ctx.Err() == nil
	_, probe.cleanupHasDeadline = ctx.Deadline()
	return probe.cleanupErr
}

func TestCandidateWorkerReconcilesDurableCleanupBeforeNewExecution(t *testing.T) {
	store, clock, _ := newCandidateWorkerFixture(t)
	cleanup := VerificationCleanupLease{
		Scope: ScopeCandidate,
		Fence: VerificationExecutionFence{
			ProjectID: store.lease.ProjectID, RunID: store.lease.RunID,
			AttemptID: store.lease.AttemptID, AttemptFenceEpoch: store.lease.AttemptFenceEpoch,
		},
		Version: 2, LeaseEpoch: 1, WorkerID: "candidate-worker-test",
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	store.cleanupLease = &cleanup
	materializer := &candidateCleanupMaterializerProbe{blocker: &verificationPhaseBlocker{}}
	worker, err := NewCandidateWorker(
		store, materializer, candidateCleanupExecutorProbe{}, candidateWorkerTestConfig(), clock, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, runErr := worker.RunOnce(context.Background())
	if !processed || runErr != nil || !store.cleanupDone || store.claimed {
		t.Fatalf("durable cleanup processed=%t err=%v done=%t executionClaimed=%t", processed, runErr, store.cleanupDone, store.claimed)
	}
	if len(materializer.cleanupCalls) != 1 || materializer.cleanupCalls[0] != cleanup.Fence {
		t.Fatalf("durable cleanup calls = %#v", materializer.cleanupCalls)
	}
}

func TestCandidateWorkerPersistsDurableCleanupFailureForRetry(t *testing.T) {
	store, clock, _ := newCandidateWorkerFixture(t)
	cleanup := VerificationCleanupLease{
		Scope: ScopeCandidate,
		Fence: VerificationExecutionFence{
			ProjectID: store.lease.ProjectID, RunID: store.lease.RunID,
			AttemptID: store.lease.AttemptID, AttemptFenceEpoch: store.lease.AttemptFenceEpoch,
		},
		Version: 2, LeaseEpoch: 1, WorkerID: "candidate-worker-test",
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	store.cleanupLease = &cleanup
	cleanupFailure := errors.New("daemon removal failed")
	materializer := &candidateCleanupMaterializerProbe{
		blocker: &verificationPhaseBlocker{}, cleanupErr: cleanupFailure,
	}
	worker, err := NewCandidateWorker(
		store, materializer, candidateCleanupExecutorProbe{}, candidateWorkerTestConfig(), clock, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, runErr := worker.RunOnce(context.Background())
	if !processed || !errors.Is(runErr, cleanupFailure) || store.cleanupDone || store.cleanupFailed == "" {
		t.Fatalf("failed durable cleanup processed=%t err=%v done=%t failure=%q", processed, runErr, store.cleanupDone, store.cleanupFailed)
	}
}

func TestCanonicalWorkerReconcilesDurableCleanupBeforeNewExecution(t *testing.T) {
	compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
	if err != nil {
		t.Fatal(err)
	}
	actorID, runID, attemptID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	cleanup := VerificationCleanupLease{
		Scope: ScopeCanonical,
		Fence: VerificationExecutionFence{
			ProjectID: compiled.Content.ProjectID, RunID: runID,
			AttemptID: attemptID, AttemptFenceEpoch: 3,
		},
		Version: 2, LeaseEpoch: 1, WorkerID: "canonical-cleanup-test",
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	store := &canonicalWorkerStoreFake{cleanupLease: &cleanup}
	materializer := &canonicalCleanupMaterializerProbe{blocker: &verificationPhaseBlocker{}}
	worker, err := NewCanonicalWorker(
		store, materializer, canonicalCleanupExecutorProbe{}, canonicalArtifactsFake{},
		CandidateWorkerConfig{
			ActorID: actorID, WorkerID: "canonical-cleanup-test",
			LeaseDuration: time.Second, HeartbeatInterval: time.Millisecond,
		}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, runErr := worker.RunOnce(context.Background())
	if !processed || runErr != nil || !store.cleanupDone || len(materializer.cleanupCalls) != 1 {
		t.Fatalf("Canonical durable cleanup processed=%t err=%v done=%t calls=%#v", processed, runErr, store.cleanupDone, materializer.cleanupCalls)
	}
}

type delayedQuiescenceMaterializer struct {
	started  chan struct{}
	quiesced chan struct{}
	t        *testing.T
}

func (materializer *delayedQuiescenceMaterializer) Materialize(
	ctx context.Context,
	_ CandidateExecutionSpec,
) error {
	close(materializer.started)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)
	close(materializer.quiesced)
	return ctx.Err()
}

func (*delayedQuiescenceMaterializer) Prepare(context.Context, CandidateExecutionSpec) error {
	return nil
}

func (*delayedQuiescenceMaterializer) Collect(context.Context, CandidateExecutionSpec) error {
	return nil
}

func (materializer *delayedQuiescenceMaterializer) CleanupCandidate(
	context.Context,
	VerificationExecutionFence,
) error {
	select {
	case <-materializer.quiesced:
	default:
		materializer.t.Error("cleanup raced an execution operation that had not quiesced")
	}
	return nil
}

func TestCandidateWorkerAwaitsOperationQuiescenceBeforeBestEffortCleanup(t *testing.T) {
	store, clock, ids := newCandidateWorkerFixture(t)
	materializer := &delayedQuiescenceMaterializer{
		started: make(chan struct{}), quiesced: make(chan struct{}), t: t,
	}
	worker, err := NewCandidateWorker(
		store, materializer, candidateCleanupExecutorProbe{}, candidateWorkerTestConfig(), clock, ids,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := worker.RunOnce(ctx)
		done <- runErr
	}()
	<-materializer.started
	cancel()
	if runErr := <-done; !errors.Is(runErr, context.Canceled) {
		t.Fatalf("cancelled delayed operation error = %v", runErr)
	}
	if store.cleanupDone {
		t.Fatal("stopped execution marked its durable cleanup completed")
	}
}

type canonicalCleanupExecutorProbe struct{ blocker *verificationPhaseBlocker }

func (probe canonicalCleanupExecutorProbe) ExecuteCanonical(
	ctx context.Context,
	_ CanonicalCheckExecutionRequest,
) (CheckExecutionOutcome, error) {
	if err := probe.blocker.run(ctx, "run"); err != nil {
		return CheckExecutionOutcome{}, err
	}
	code := 0
	return CheckExecutionOutcome{Status: CheckPassed, ExitCode: &code, Diagnostics: []Diagnostic{}}, nil
}

type canonicalCleanupArtifactsProbe struct{ blocker *verificationPhaseBlocker }

func (probe canonicalCleanupArtifactsProbe) CollectReleaseArtifacts(
	ctx context.Context,
	_ CanonicalExecutionSpec,
	_ []CheckResult,
) ([]CanonicalReleaseArtifact, error) {
	if err := probe.blocker.run(ctx, "collect-artifacts"); err != nil {
		return nil, err
	}
	return canonicalArtifactsFake{}.CollectReleaseArtifacts(ctx, CanonicalExecutionSpec{}, nil)
}

func TestCanonicalWorkerAlwaysCleansExactFenceAfterCancellationTimeoutOrLeaseLoss(t *testing.T) {
	for _, stop := range []string{"cancel", "deadline", "lease-loss"} {
		for _, phase := range []string{"materialize", "prepare", "run", "collect-artifacts", "collect"} {
			t.Run(stop+"/"+phase, func(t *testing.T) {
				compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
				if err != nil {
					t.Fatal(err)
				}
				planID, runID, attemptID := uuid.NewString(), uuid.NewString(), uuid.NewString()
				actorID := uuid.NewString()
				store := &canonicalWorkerStoreFake{
					lease: CanonicalExecutionLease{
						ProjectID: compiled.Content.ProjectID, RunID: runID, AttemptID: attemptID,
						Plan: PlanReference{ID: planID, ContentHash: compiled.PlanHash}, AttemptOrdinal: 1,
						State: RunClaimed, RunVersion: 2, RunFenceEpoch: 7,
						AttemptVersion: 2, AttemptFenceEpoch: 7, WorkerID: "canonical-cleanup-test",
						LeaseExpiresAt: time.Now().UTC().Add(time.Hour),
					},
					plan: CanonicalPlan{
						ID: planID, Content: compiled.Content, PlanHash: compiled.PlanHash,
						CreatedBy: actorID, CreatedAt: time.Now().UTC(),
					},
				}
				ctx, cancel := context.WithCancel(context.Background())
				if stop == "deadline" {
					ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
				}
				defer cancel()
				blocker := &verificationPhaseBlocker{target: phase}
				if stop == "cancel" {
					blocker.cancel = cancel
				} else if stop == "lease-loss" {
					store.heartbeatErr = errors.New("lease was reclaimed")
				}
				materializer := &canonicalCleanupMaterializerProbe{blocker: blocker}
				worker, err := NewCanonicalWorker(
					store, materializer, canonicalCleanupExecutorProbe{blocker: blocker},
					canonicalCleanupArtifactsProbe{blocker: blocker},
					CandidateWorkerConfig{
						ActorID: actorID, WorkerID: "canonical-cleanup-test",
						LeaseDuration: time.Second, HeartbeatInterval: time.Millisecond,
					},
					&candidateWorkerClockFake{now: time.Now().UTC()},
					&canonicalWorkerIDs{values: []string{attemptID, uuid.NewString()}},
				)
				if err != nil {
					t.Fatal(err)
				}
				processed, runErr := worker.RunOnce(ctx)
				if !processed {
					t.Fatal("claimed Canonical execution was not reported as processed")
				}
				if stop == "cancel" && !errors.Is(runErr, context.Canceled) {
					t.Fatalf("cancelled Canonical RunOnce error = %v", runErr)
				}
				if stop == "deadline" && !errors.Is(runErr, context.DeadlineExceeded) {
					t.Fatalf("deadline Canonical RunOnce error = %v", runErr)
				}
				if stop == "lease-loss" && !errors.Is(runErr, ErrWorkerLeaseLost) {
					t.Fatalf("lease-lost Canonical RunOnce error = %v", runErr)
				}
				if store.committed.ID != "" {
					t.Fatalf("stopped Canonical worker terminalized after authority loss: %#v", store.committed)
				}
				if len(materializer.cleanupCalls) != 1 ||
					materializer.cleanupCalls[0].AttemptID != attemptID ||
					materializer.cleanupCalls[0].AttemptFenceEpoch != 7 {
					t.Fatalf("exact Canonical cleanup calls = %#v", materializer.cleanupCalls)
				}
				if !materializer.cleanupContextLive || !materializer.cleanupHasDeadline {
					t.Fatal("Canonical cleanup did not receive an independent bounded context")
				}
			})
		}
	}
}

func TestCandidateWorkerCleansAfterNormalCompletionAndTransitionFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		transition RunState
		wantErr    bool
	}{
		{name: "normal completion"},
		{name: "transition failure", transition: RunPreparing, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, clock, ids := newCandidateWorkerFixture(t)
			if test.wantErr {
				store.transitionAt = test.transition
				store.transitionErr = errors.New("transition compare-and-swap failed")
			}
			materializer := &candidateCleanupMaterializerProbe{blocker: &verificationPhaseBlocker{}}
			worker, err := NewCandidateWorker(
				store, materializer, candidateCleanupExecutorProbe{blocker: &verificationPhaseBlocker{}},
				candidateWorkerTestConfig(), clock, ids,
			)
			if err != nil {
				t.Fatal(err)
			}
			processed, runErr := worker.RunOnce(context.Background())
			if !processed || (runErr != nil) != test.wantErr {
				t.Fatalf("RunOnce processed=%v err=%v", processed, runErr)
			}
			if len(materializer.cleanupCalls) != 1 || !materializer.cleanupHasDeadline {
				t.Fatalf("cleanup calls after terminal path = %#v", materializer.cleanupCalls)
			}
			if test.wantErr && store.committed != nil {
				t.Fatalf("transition failure terminalized execution: %#v", store.committed)
			}
		})
	}
}

func TestCanonicalWorkerCleansAfterNormalCompletionAndTransitionFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		transition RunState
		wantErr    bool
	}{
		{name: "normal completion"},
		{name: "transition failure", transition: RunPreparing, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
			if err != nil {
				t.Fatal(err)
			}
			planID, runID, attemptID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			store := &canonicalWorkerStoreFake{
				lease: CanonicalExecutionLease{
					ProjectID: compiled.Content.ProjectID, RunID: runID, AttemptID: attemptID,
					Plan: PlanReference{ID: planID, ContentHash: compiled.PlanHash}, AttemptOrdinal: 1,
					State: RunClaimed, RunVersion: 2, RunFenceEpoch: 5,
					AttemptVersion: 2, AttemptFenceEpoch: 5, WorkerID: "canonical-cleanup-test",
					LeaseExpiresAt: time.Now().UTC().Add(time.Hour),
				},
				plan: CanonicalPlan{
					ID: planID, Content: compiled.Content, PlanHash: compiled.PlanHash,
					CreatedBy: actorID, CreatedAt: time.Now().UTC(),
				},
			}
			if test.wantErr {
				store.transitionAt = test.transition
				store.transitionErr = errors.New("transition compare-and-swap failed")
			}
			materializer := &canonicalCleanupMaterializerProbe{blocker: &verificationPhaseBlocker{}}
			worker, err := NewCanonicalWorker(
				store, materializer, canonicalCleanupExecutorProbe{blocker: &verificationPhaseBlocker{}},
				canonicalArtifactsFake{},
				CandidateWorkerConfig{
					ActorID: actorID, WorkerID: "canonical-cleanup-test",
					LeaseDuration: time.Second, HeartbeatInterval: time.Millisecond,
				},
				&candidateWorkerClockFake{now: time.Now().UTC()},
				&canonicalWorkerIDs{values: []string{attemptID, uuid.NewString()}},
			)
			if err != nil {
				t.Fatal(err)
			}
			processed, runErr := worker.RunOnce(context.Background())
			if !processed || (runErr != nil) != test.wantErr {
				t.Fatalf("Canonical RunOnce processed=%v err=%v", processed, runErr)
			}
			if len(materializer.cleanupCalls) != 1 || !materializer.cleanupHasDeadline {
				t.Fatalf("Canonical cleanup calls after terminal path = %#v", materializer.cleanupCalls)
			}
			if test.wantErr && store.committed.ID != "" {
				t.Fatalf("Canonical transition failure terminalized execution: %#v", store.committed)
			}
		})
	}
}

func TestCandidateWorkerCleanupFailureLeavesAttemptNonterminalForTakeover(t *testing.T) {
	store, clock, ids := newCandidateWorkerFixture(t)
	cleanupFailure := errors.New("verification network removal failed")
	materializer := &candidateCleanupMaterializerProbe{
		blocker: &verificationPhaseBlocker{}, collectErr: cleanupFailure,
	}
	worker, err := NewCandidateWorker(
		store, materializer, candidateCleanupExecutorProbe{blocker: &verificationPhaseBlocker{}},
		candidateWorkerTestConfig(), clock, ids,
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, runErr := worker.RunOnce(context.Background())
	if !processed || !errors.Is(runErr, cleanupFailure) {
		t.Fatalf("Candidate cleanup failure processed=%v err=%v", processed, runErr)
	}
	if store.committed != nil {
		t.Fatalf("Candidate cleanup failure minted terminal Receipt: %#v", store.committed)
	}
	if store.lease.State != RunCollecting || len(materializer.cleanupCalls) != 1 {
		t.Fatalf("Candidate cleanup failure lost takeover state: lease=%#v cleanup=%#v", store.lease, materializer.cleanupCalls)
	}
}

func TestCanonicalWorkerCleanupFailureLeavesAttemptNonterminalForTakeover(t *testing.T) {
	compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
	if err != nil {
		t.Fatal(err)
	}
	planID, runID, attemptID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &canonicalWorkerStoreFake{
		lease: CanonicalExecutionLease{
			ProjectID: compiled.Content.ProjectID, RunID: runID, AttemptID: attemptID,
			Plan: PlanReference{ID: planID, ContentHash: compiled.PlanHash}, AttemptOrdinal: 1,
			State: RunClaimed, RunVersion: 2, RunFenceEpoch: 5,
			AttemptVersion: 2, AttemptFenceEpoch: 5, WorkerID: "canonical-cleanup-test",
			LeaseExpiresAt: time.Now().UTC().Add(time.Hour),
		},
		plan: CanonicalPlan{
			ID: planID, Content: compiled.Content, PlanHash: compiled.PlanHash,
			CreatedBy: actorID, CreatedAt: time.Now().UTC(),
		},
	}
	cleanupFailure := errors.New("verification network removal failed")
	materializer := &canonicalCleanupMaterializerProbe{
		blocker: &verificationPhaseBlocker{}, collectErr: cleanupFailure,
	}
	worker, err := NewCanonicalWorker(
		store, materializer, canonicalCleanupExecutorProbe{blocker: &verificationPhaseBlocker{}},
		canonicalArtifactsFake{},
		CandidateWorkerConfig{
			ActorID: actorID, WorkerID: "canonical-cleanup-test",
			LeaseDuration: time.Second, HeartbeatInterval: time.Millisecond,
		},
		&candidateWorkerClockFake{now: time.Now().UTC()},
		&canonicalWorkerIDs{values: []string{attemptID, uuid.NewString()}},
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, runErr := worker.RunOnce(context.Background())
	if !processed || !errors.Is(runErr, cleanupFailure) {
		t.Fatalf("Canonical cleanup failure processed=%v err=%v", processed, runErr)
	}
	if store.committed.ID != "" {
		t.Fatalf("Canonical cleanup failure minted terminal Receipt: %#v", store.committed)
	}
	if store.lease.State != RunCollecting || len(materializer.cleanupCalls) != 1 {
		t.Fatalf("Canonical cleanup failure lost takeover state: lease=%#v cleanup=%#v", store.lease, materializer.cleanupCalls)
	}
}
