package verification

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type canonicalWorkerStoreFake struct {
	lease         CanonicalExecutionLease
	plan          CanonicalPlan
	committed     CanonicalReceipt
	heartbeatErr  error
	transitionAt  RunState
	transitionErr error
	cleanupLease  *VerificationCleanupLease
	cleanupFailed string
	cleanupDone   bool
}

func (store *canonicalWorkerStoreFake) ReconcileInactiveVerificationExecution(
	context.Context,
	Scope,
	string,
) (bool, error) {
	return false, nil
}

func (store *canonicalWorkerStoreFake) ClaimVerificationCleanup(
	_ context.Context,
	input ClaimVerificationCleanupInput,
) (VerificationCleanupLease, bool, error) {
	if store.cleanupLease == nil {
		return VerificationCleanupLease{}, false, nil
	}
	lease := *store.cleanupLease
	store.cleanupLease = nil
	lease.WorkerID = input.WorkerID
	return lease, true, nil
}

func (store *canonicalWorkerStoreFake) CompleteVerificationCleanup(
	_ context.Context,
	_ CompleteVerificationCleanupInput,
) error {
	store.cleanupDone = true
	return nil
}

func (store *canonicalWorkerStoreFake) FailVerificationCleanup(
	_ context.Context,
	input FailVerificationCleanupInput,
) error {
	store.cleanupFailed = input.Reason
	return nil
}

func (store *canonicalWorkerStoreFake) ConfirmVerificationOperationQuiesced(
	context.Context,
	Scope,
	VerificationExecutionFence,
	string,
	string,
) error {
	return nil
}

func (store *canonicalWorkerStoreFake) ClaimCanonicalExecution(
	context.Context,
	ClaimCanonicalExecutionInput,
) (CanonicalExecutionLease, bool, error) {
	return store.lease, true, nil
}

func (store *canonicalWorkerStoreFake) HeartbeatCanonicalExecution(
	_ context.Context,
	lease CanonicalExecutionLease,
	_ string,
	_ time.Duration,
) (CanonicalExecutionLease, error) {
	if store.heartbeatErr != nil {
		return CanonicalExecutionLease{}, store.heartbeatErr
	}
	lease.RunVersion++
	lease.AttemptVersion++
	lease.LeaseExpiresAt = lease.LeaseExpiresAt.Add(time.Minute)
	store.lease = lease
	return lease, nil
}

func (store *canonicalWorkerStoreFake) TransitionCanonicalExecution(
	_ context.Context,
	lease CanonicalExecutionLease,
	_ string,
	target RunState,
) (CanonicalExecutionLease, error) {
	if target == store.transitionAt && store.transitionErr != nil {
		return CanonicalExecutionLease{}, store.transitionErr
	}
	lease.State = target
	lease.RunVersion++
	lease.AttemptVersion++
	store.lease = lease
	return lease, nil
}

func (store *canonicalWorkerStoreFake) GetCanonicalPlan(context.Context, string, string) (CanonicalPlan, error) {
	return store.plan, nil
}

func (store *canonicalWorkerStoreFake) CommitCanonicalReceipt(
	_ context.Context,
	_ CanonicalExecutionLease,
	receipt CanonicalReceipt,
	_, _ string,
) (CanonicalReceipt, error) {
	store.committed = receipt
	return receipt, nil
}

func (store *canonicalWorkerStoreFake) CompleteCanonicalExecutionCleanup(
	_ context.Context,
	_ CanonicalExecutionLease,
	_ string,
) error {
	store.cleanupDone = true
	return nil
}

type canonicalMaterializerFake struct{}

func (canonicalMaterializerFake) MaterializeCanonical(context.Context, CanonicalExecutionSpec) error {
	return nil
}
func (canonicalMaterializerFake) PrepareCanonical(context.Context, CanonicalExecutionSpec) error {
	return nil
}
func (canonicalMaterializerFake) CollectCanonical(context.Context, CanonicalExecutionSpec) error {
	return nil
}
func (canonicalMaterializerFake) CleanupCanonical(context.Context, VerificationExecutionFence) error {
	return nil
}

type canonicalExecutorFake struct{}

func (canonicalExecutorFake) ExecuteCanonical(
	_ context.Context,
	_ CanonicalCheckExecutionRequest,
) (CheckExecutionOutcome, error) {
	exitCode := 0
	return CheckExecutionOutcome{Status: CheckPassed, ExitCode: &exitCode, Diagnostics: []Diagnostic{}}, nil
}

type canonicalArtifactsFake struct{}

func (canonicalArtifactsFake) CollectReleaseArtifacts(
	context.Context,
	CanonicalExecutionSpec,
	[]CheckResult,
) ([]CanonicalReleaseArtifact, error) {
	return []CanonicalReleaseArtifact{{
		ID: "api-image", Kind: "oci-image", Store: "oci",
		Ref:         "registry.example/api@" + hashFixture("canonical-worker-image"),
		ContentHash: hashFixture("canonical-worker-image"),
		MediaType:   "application/vnd.oci.image.manifest.v1+json", ByteSize: 4096,
	}}, nil
}

type canonicalWorkerIDs struct{ values []string }

func (ids *canonicalWorkerIDs) NewID() string {
	value := ids.values[0]
	ids.values = ids.values[1:]
	return value
}

func TestCanonicalWorkerRerunsExactRevisionAndCommitsIndependentReceipt(t *testing.T) {
	compiled, err := (PlanCompiler{}).CompileCanonical(validCanonicalPlanInput())
	if err != nil {
		t.Fatal(err)
	}
	planID, runID, attemptID, receiptID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	actorID := uuid.NewString()
	store := &canonicalWorkerStoreFake{
		lease: CanonicalExecutionLease{
			ProjectID: compiled.Content.ProjectID, RunID: runID, AttemptID: attemptID,
			Plan: PlanReference{ID: planID, ContentHash: compiled.PlanHash}, AttemptOrdinal: 1,
			State: RunClaimed, RunVersion: 2, RunFenceEpoch: 1,
			AttemptVersion: 2, AttemptFenceEpoch: 1, WorkerID: "canonical-worker-test",
			LeaseExpiresAt: time.Now().UTC().Add(time.Hour),
		},
		plan: CanonicalPlan{ID: planID, Content: compiled.Content, PlanHash: compiled.PlanHash, CreatedBy: actorID, CreatedAt: time.Now().UTC()},
	}
	worker, err := NewCanonicalWorker(
		store, canonicalMaterializerFake{}, canonicalExecutorFake{}, canonicalArtifactsFake{},
		CandidateWorkerConfig{
			ActorID: actorID, WorkerID: "canonical-worker-test",
			LeaseDuration: time.Minute, HeartbeatInterval: 30 * time.Second,
		},
		&candidateWorkerClockFake{now: time.Now().UTC()},
		&canonicalWorkerIDs{values: []string{attemptID, receiptID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	found, err := worker.RunOnce(context.Background())
	if err != nil || !found {
		t.Fatalf("RunOnce found=%v err=%v", found, err)
	}
	if store.committed.ID != receiptID || store.committed.RunID != runID ||
		store.committed.Subject != compiled.Content.Subject || store.committed.Decision != DecisionPassed ||
		len(store.committed.ReleaseArtifacts) != 1 {
		t.Fatalf("Canonical worker committed wrong Receipt: %+v", store.committed)
	}
}
