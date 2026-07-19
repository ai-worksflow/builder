package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAttemptLifecycleRequiresExactWorkerFenceAndPlatformEvidence(t *testing.T) {
	fixture := newAgentFixture(t)
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	executor := testExecutor()
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: executor,
	}, capsule, pack, fixture.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != AttemptPending || attempt.Version != 1 || attempt.FenceEpoch != 0 ||
		!sha256Pattern.MatchString(attempt.RequestKeyHash) || !sha256Pattern.MatchString(attempt.ConfigurationHash) {
		t.Fatalf("invalid new Attempt: %#v", attempt)
	}

	attempt = advanceAttempt(t, attempt, fixture.actorID, "", 0, AttemptReady, fixture.now.Add(3*time.Second), AttemptEvidence{})
	attempt = advanceAttempt(t, attempt, fixture.actorID, "", 0, AttemptQueued, fixture.now.Add(4*time.Second), AttemptEvidence{})
	attempt, event, err := attempt.Claim(
		attempt.Version, fixture.actorID, "runner-a", 5*time.Second, fixture.now.Add(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventLeaseClaimed || attempt.State != AttemptClaimed || attempt.FenceEpoch != 1 {
		t.Fatalf("unexpected claim: attempt=%#v event=%#v", attempt, event)
	}
	attempt = advanceAttempt(
		t, attempt, fixture.actorID, "runner-a", 1, AttemptRunning,
		fixture.now.Add(6*time.Second), AttemptEvidence{},
	)

	attempt, event, err = attempt.Claim(
		attempt.Version, fixture.actorID, "runner-b", 10*time.Second, fixture.now.Add(10*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventLeaseReclaimed || attempt.FenceEpoch != 2 || attempt.Lease.WorkerID != "runner-b" {
		t.Fatalf("expired lease was not fenced: attempt=%#v event=%#v", attempt, event)
	}
	if _, _, err := attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: 1,
		ActorID: fixture.actorID, WorkerID: "runner-a", Target: AttemptPatchReady,
		Reason: "stale worker completion", Evidence: testPatchEvidence(),
	}, fixture.now.Add(11*time.Second)); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("stale worker was not fenced: %v", err)
	}
	if _, _, err := attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: 2,
		ActorID: fixture.actorID, WorkerID: "runner-b", Target: AttemptPatchReady,
		Reason: "missing platform patch evidence",
	}, fixture.now.Add(11*time.Second)); !errors.Is(err, ErrAttemptState) {
		t.Fatalf("patch_ready without evidence succeeded: %v", err)
	}

	attempt = advanceAttempt(
		t, attempt, fixture.actorID, "runner-b", 2, AttemptPatchReady,
		fixture.now.Add(11*time.Second), testPatchEvidence(),
	)
	attempt = advanceAttempt(
		t, attempt, fixture.actorID, "runner-b", 2, AttemptValidating,
		fixture.now.Add(12*time.Second), AttemptEvidence{},
	)
	if _, _, err := attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: 2,
		ActorID: fixture.actorID, WorkerID: "runner-b", Target: AttemptReviewReady,
		Reason: "claims validation without platform evidence",
	}, fixture.now.Add(13*time.Second)); !errors.Is(err, ErrAttemptState) {
		t.Fatalf("review_ready without validation evidence succeeded: %v", err)
	}
	validation := testBlob("b", 256)
	attempt = advanceAttempt(
		t, attempt, fixture.actorID, "runner-b", 2, AttemptReviewReady,
		fixture.now.Add(13*time.Second), AttemptEvidence{Validation: &validation},
	)
	if attempt.FinishedAt == nil || attempt.Lease != nil || attempt.Evidence.Patch == nil ||
		attempt.Evidence.Validation == nil {
		t.Fatalf("review-ready Attempt lost exact evidence or lease cleanup: %#v", attempt)
	}
}

func TestAttemptCancelFencesWorkerAndRetryPreservesRequest(t *testing.T) {
	fixture := newAgentFixture(t)
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	executor := testExecutor()
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: executor,
	}, capsule, pack, fixture.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	attempt = advanceAttempt(t, attempt, fixture.actorID, "", 0, AttemptReady, fixture.now.Add(3*time.Second), AttemptEvidence{})
	attempt = advanceAttempt(t, attempt, fixture.actorID, "", 0, AttemptQueued, fixture.now.Add(4*time.Second), AttemptEvidence{})
	attempt, _, err = attempt.Claim(attempt.Version, fixture.actorID, "runner-a", time.Minute, fixture.now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	oldFence := attempt.FenceEpoch
	attempt, event, err := attempt.Cancel(
		attempt.Version, fixture.actorID, "User cancelled the exact Attempt.", fixture.now.Add(6*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventControlCancelled || attempt.State != AttemptCancelled ||
		attempt.FenceEpoch != oldFence+1 || attempt.Lease != nil {
		t.Fatalf("cancel did not fence the worker: attempt=%#v event=%#v", attempt, event)
	}

	retry, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: executor,
		Parent: &attempt, RetryReason: "Retry after the user corrected the task precondition.",
	}, capsule, pack, fixture.now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if retry.ParentAttemptID != attempt.ID || retry.RequestKeyHash != attempt.RequestKeyHash ||
		retry.ConfigurationHash != attempt.ConfigurationHash || retry.State != AttemptPending {
		t.Fatalf("retry did not preserve exact request lineage: %#v", retry)
	}

	changed := executor
	changed.Model = "another-model"
	if _, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: changed,
		Parent: &attempt, RetryReason: "Try another model.",
	}, capsule, pack, fixture.now.Add(7*time.Second)); !errors.Is(err, ErrInvalidAttempt) {
		t.Fatalf("changed model was accepted as a retry: %v", err)
	}
	if _, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: executor, Parent: &attempt,
	}, capsule, pack, fixture.now.Add(7*time.Second)); !errors.Is(err, ErrInvalidAttempt) {
		t.Fatalf("retry without reason succeeded: %v", err)
	}
}

func TestParseAttemptRejectsTamperedProjection(t *testing.T) {
	fixture := newAgentFixture(t)
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: testExecutor(),
	}, capsule, pack, fixture.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAttempt(attempt); err != nil {
		t.Fatalf("valid Attempt did not parse: %v", err)
	}

	tampered := attempt
	tampered.Executor.Model = "unrecorded-model"
	if _, err := ParseAttempt(tampered); !errors.Is(err, ErrInvalidAttempt) {
		t.Fatalf("tampered executor parsed: %v", err)
	}

	tampered = attempt
	tampered.State = AttemptReviewReady
	finished := tampered.UpdatedAt
	tampered.FinishedAt = &finished
	if _, err := ParseAttempt(tampered); !errors.Is(err, ErrInvalidAttempt) {
		t.Fatalf("review-ready projection without evidence parsed: %v", err)
	}

	tampered = attempt
	tampered.Lease = &AttemptLease{
		WorkerID: "runner-a", Epoch: 1, ExpiresAt: attempt.UpdatedAt.Add(time.Minute),
	}
	if _, err := ParseAttempt(tampered); !errors.Is(err, ErrInvalidAttempt) {
		t.Fatalf("pending projection with a lease parsed: %v", err)
	}
}

func advanceAttempt(
	t *testing.T,
	attempt AgentAttempt,
	actorID, workerID string,
	fence uint64,
	target AttemptState,
	now time.Time,
	evidence AttemptEvidence,
) AgentAttempt {
	t.Helper()
	next, _, err := attempt.Advance(AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ExpectedFenceEpoch: fence,
		ActorID: actorID, WorkerID: workerID, Target: target,
		Reason: "advance exact test Attempt", Evidence: evidence,
	}, now)
	if err != nil {
		t.Fatalf("advance %s -> %s: %v", attempt.State, target, err)
	}
	return next
}

func testExecutor() ExecutorIdentity {
	return ExecutorIdentity{
		Adapter: "codex-cli", Provider: "openai", Model: "qualified-model",
		RunnerImageDigest: testHash("8"), ModelPolicyHash: testHash("9"),
		ParametersHash: testHash("a"), PromptHash: testHash("b"),
		OutputSchemaHash: testHash("7"), ToolchainHash: testHash("c"),
	}
}

func testPatchEvidence() AttemptEvidence {
	patch := testBlob("d", 1024)
	structured := testBlob("e", 512)
	stdout := testBlob("f", 128)
	return AttemptEvidence{Patch: &patch, StructuredResult: &structured, Stdout: &stdout}
}
