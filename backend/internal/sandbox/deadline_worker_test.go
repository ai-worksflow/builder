package sandbox

import (
	"context"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type deadlineLeaseStoreFake struct {
	lease     *DeadlineLease
	completed int
	retried   int
}

func (store *deadlineLeaseStoreFake) ClaimDueDeadline(context.Context, string, time.Duration) (*DeadlineLease, error) {
	lease := store.lease
	store.lease = nil
	return lease, nil
}
func (store *deadlineLeaseStoreFake) CompleteDeadline(context.Context, DeadlineLease) error {
	store.completed++
	return nil
}
func (store *deadlineLeaseStoreFake) RetryDeadline(context.Context, DeadlineLease, time.Duration, string) error {
	store.retried++
	return nil
}

type deadlineCandidateStoreFake struct {
	record       repository.CandidateMutationRecord
	checkpoint   *repository.CandidateSnapshot
	checkpointAt time.Time
	createdBy    string
}

func (store *deadlineCandidateStoreFake) Get(context.Context, string, string) (repository.CandidateMutationRecord, error) {
	return store.record, nil
}
func (store *deadlineCandidateStoreFake) CreateCheckpoint(
	_ context.Context,
	input repository.CreateCheckpointInput,
) (repository.CandidateSnapshot, error) {
	candidate := store.record.Candidate
	checkpoint := repository.CandidateSnapshot{
		SchemaVersion: repository.CandidateSnapshotSchemaVersion,
		ID:            input.ID, ProjectID: candidate.ProjectID, CandidateID: candidate.ID,
		CandidateVersion: candidate.Version, JournalSequence: candidate.JournalSequence,
		SessionEpoch: candidate.SessionEpoch, WriterLeaseEpoch: candidate.WriterLeaseEpoch,
		Tree: candidate.CurrentTree, Reason: input.Reason, CreatedBy: input.ActorID,
		CreatedAt: store.checkpointAt,
	}
	store.createdBy = input.ActorID
	store.checkpoint = &checkpoint
	return checkpoint, nil
}

type deadlineSessionStoreFake struct {
	session    SandboxSession
	candidates *deadlineCandidateStoreFake
	attached   int
	synced     int
}

func (store *deadlineSessionStoreFake) Get(context.Context, string, string) (SandboxSession, error) {
	return store.session, nil
}
func (store *deadlineSessionStoreFake) SyncCandidate(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	_ string,
) (SandboxSession, error) {
	view := store.session.Snapshot()
	candidate := store.candidates.record.Candidate
	now := view.UpdatedAt.Add(time.Second)
	if candidate.UpdatedAt.After(now) {
		now = candidate.UpdatedAt.Add(time.Second)
	}
	next, err := store.session.UpdateCandidate(
		expectedVersion, expectedEpoch, view.Candidate.Version, candidate, now,
	)
	if err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	store.synced++
	return next.Clone(), nil
}
func (store *deadlineSessionStoreFake) AttachCheckpoint(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	_, checkpointID string,
) (SandboxSession, error) {
	checkpoint := store.candidates.checkpoint
	if checkpoint == nil || checkpoint.ID != checkpointID {
		return SandboxSession{}, ErrCheckpointMismatch
	}
	next, err := store.session.RecordCheckpoint(
		expectedVersion, expectedEpoch, *checkpoint, checkpoint.CreatedAt,
	)
	if err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	store.attached++
	return next, nil
}

type deadlineControllerFake struct {
	suspended  []SessionControlInput
	terminated []TerminateSessionInput
	abandoned  []SessionControlInput
}

func (controller *deadlineControllerFake) suspendDeadline(
	_ context.Context,
	input SessionControlInput,
) (SessionView, error) {
	controller.suspended = append(controller.suspended, input)
	return SessionView{}, nil
}
func (controller *deadlineControllerFake) terminateDeadline(
	_ context.Context,
	input TerminateSessionInput,
) (SessionView, error) {
	controller.terminated = append(controller.terminated, input)
	return SessionView{}, nil
}
func (controller *deadlineControllerFake) completeAbandonDeadline(
	_ context.Context,
	input SessionControlInput,
) (SessionView, error) {
	controller.abandoned = append(controller.abandoned, input)
	return SessionView{}, nil
}

func TestDeadlineWorkerSuspendsDirtyCandidateWithoutTakingEditorLease(t *testing.T) {
	dirty, _ := dirtyCandidate(t, cleanCandidate(t), sandboxBaseTime.Add(time.Second))
	ready := readyTestSession(t, dirty, sandboxBaseTime.Add(3*time.Second))
	view := ready.Snapshot()
	leaseStore := &deadlineLeaseStoreFake{lease: &DeadlineLease{
		SessionID: view.ID, ProjectID: view.ProjectID, Action: DeadlineSuspend,
		Owner: "deadline-worker", LeaseEpoch: 1, ObservedAt: view.TTL.IdleDeadline,
	}}
	candidates := &deadlineCandidateStoreFake{
		record:       repository.CandidateMutationRecord{Candidate: dirty},
		checkpointAt: view.UpdatedAt.Add(time.Second),
	}
	sessions := &deadlineSessionStoreFake{session: ready, candidates: candidates}
	controller := &deadlineControllerFake{}
	worker, err := newDeadlineWorker(
		leaseStore, sessions, candidates, controller,
		DeadlineWorkerConfig{
			WorkerID: "deadline-worker", LeaseDuration: time.Minute, RetryDelay: time.Second,
		},
		func() string { return testCheckpoint },
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run dirty Candidate deadline: %v", err)
	}
	if !processed || leaseStore.completed != 1 || leaseStore.retried != 0 || sessions.attached != 0 ||
		candidates.createdBy != "" || len(controller.suspended) != 1 ||
		len(controller.terminated) != 0 {
		t.Fatalf("deadline worker did not suspend without mutating Candidate exactly once: lease=%#v sessions=%#v controller=%#v", leaseStore, sessions, controller)
	}
	control := controller.suspended[0]
	current := sessions.session.Snapshot()
	if control.ActorID != SandboxLifecycleWorkerActorID ||
		control.ExpectedSessionVersion != current.Version ||
		control.ExpectedSessionEpoch != current.SessionEpoch {
		t.Fatalf("deadline control did not bind the exact Session: input=%#v view=%#v", control, current)
	}
}

func TestDeadlineWorkerDropsStaleIdleClaimAfterActivity(t *testing.T) {
	ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
	view := ready.Snapshot()
	leaseStore := &deadlineLeaseStoreFake{lease: &DeadlineLease{
		SessionID: view.ID, ProjectID: view.ProjectID, Action: DeadlineSuspend,
		Owner: "deadline-worker", LeaseEpoch: 1,
		ObservedAt: view.TTL.IdleDeadline.Add(-time.Second),
	}}
	candidates := &deadlineCandidateStoreFake{record: repository.CandidateMutationRecord{Candidate: cleanCandidate(t)}}
	sessions := &deadlineSessionStoreFake{session: ready, candidates: candidates}
	controller := &deadlineControllerFake{}
	worker, err := newDeadlineWorker(
		leaseStore, sessions, candidates, controller,
		DeadlineWorkerConfig{WorkerID: "deadline-worker", LeaseDuration: time.Minute, RetryDelay: time.Second},
		func() string { return testCheckpoint },
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed || leaseStore.completed != 1 || leaseStore.retried != 0 ||
		len(controller.suspended) != 0 || len(controller.terminated) != 0 {
		t.Fatalf("stale idle claim was not completed as a no-op: processed=%v err=%v lease=%#v controller=%#v", processed, err, leaseStore, controller)
	}
}

func TestDeadlineActionAbsoluteTTLOverridesIdle(t *testing.T) {
	ready := readyTestSession(t, cleanCandidate(t), sandboxBaseTime)
	view := ready.Snapshot()
	if action, due := deadlineActionAt(view, view.TTL.ExpiresAt); !due || action != DeadlineTerminate {
		t.Fatalf("absolute TTL action = %q, %v", action, due)
	}
	if action, due := deadlineActionAt(view, view.TTL.IdleDeadline); !due || action != DeadlineSuspend {
		t.Fatalf("idle TTL action = %q, %v", action, due)
	}
	if _, due := deadlineActionAt(view, view.TTL.IdleDeadline.Add(-time.Nanosecond)); due {
		t.Fatal("deadline worker acted before the exact idle deadline")
	}
}

func TestDeadlineWorkerTerminatesAtAbsoluteTTLWithoutMutatingCandidate(t *testing.T) {
	candidate := cleanCandidate(t)
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	view := ready.Snapshot()
	advanced, _, err := candidate.AcquireLease(
		candidate.Version, testActorID, time.Minute, view.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	leaseStore := &deadlineLeaseStoreFake{lease: &DeadlineLease{
		SessionID: view.ID, ProjectID: view.ProjectID, Action: DeadlineTerminate,
		Owner: "deadline-worker", LeaseEpoch: 1, ObservedAt: view.TTL.ExpiresAt,
	}}
	candidates := &deadlineCandidateStoreFake{
		record: repository.CandidateMutationRecord{Candidate: advanced},
	}
	sessions := &deadlineSessionStoreFake{session: ready, candidates: candidates}
	controller := &deadlineControllerFake{}
	worker, err := newDeadlineWorker(
		leaseStore, sessions, candidates, controller,
		DeadlineWorkerConfig{WorkerID: "deadline-worker", LeaseDuration: time.Minute, RetryDelay: time.Second},
		func() string { return testCheckpoint },
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed || sessions.synced != 0 || leaseStore.completed != 1 ||
		leaseStore.retried != 0 || len(controller.terminated) != 1 || len(controller.suspended) != 0 {
		t.Fatalf("absolute TTL did not terminate without Candidate mutation exactly once: processed=%v err=%v sessions=%#v lease=%#v controller=%#v", processed, err, sessions, leaseStore, controller)
	}
	input := controller.terminated[0]
	if input.ExpectedSessionVersion != view.Version || input.ExpectedSessionEpoch != view.SessionEpoch ||
		input.Reason != "SandboxSession absolute TTL elapsed; the durable Candidate is retained independently" {
		t.Fatalf("termination did not bind the exact expired Session boundary: input=%#v view=%#v", input, view)
	}
}

func TestDeadlineWorkerTerminatesSuspendedSessionWithStaleCandidateProjection(t *testing.T) {
	candidate := cleanCandidate(t)
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	view := ready.Snapshot()
	suspending, err := ready.BeginSuspend(view.Version, view.SessionEpoch, view.UpdatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	view = suspending.Snapshot()
	suspended, err := suspending.MarkSuspended(view.Version, view.SessionEpoch, view.UpdatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	view = suspended.Snapshot()
	advanced, _, err := candidate.AcquireLease(
		candidate.Version, testActorID, time.Minute, view.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	leaseStore := &deadlineLeaseStoreFake{lease: &DeadlineLease{
		SessionID: view.ID, ProjectID: view.ProjectID, Action: DeadlineTerminate,
		Owner: "deadline-worker", LeaseEpoch: 1, ObservedAt: view.TTL.ExpiresAt,
	}}
	candidates := &deadlineCandidateStoreFake{record: repository.CandidateMutationRecord{Candidate: advanced}}
	sessions := &deadlineSessionStoreFake{session: suspended, candidates: candidates}
	controller := &deadlineControllerFake{}
	worker, err := newDeadlineWorker(
		leaseStore, sessions, candidates, controller,
		DeadlineWorkerConfig{WorkerID: "deadline-worker", LeaseDuration: time.Minute, RetryDelay: time.Second},
		func() string { return testCheckpoint },
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed || sessions.synced != 0 || leaseStore.completed != 1 ||
		leaseStore.retried != 0 || len(controller.terminated) != 1 {
		t.Fatalf("stale suspended Session was not terminated at absolute TTL: processed=%v err=%v sessions=%#v lease=%#v controller=%#v", processed, err, sessions, leaseStore, controller)
	}
}

func TestDeadlineWorkerClaimsAbandonedTerminatingSessionForCleanup(t *testing.T) {
	candidate := cleanCandidate(t)
	leased, _, err := candidate.AcquireLease(
		candidate.Version, testActorID, 5*time.Minute, candidate.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	ready := readyTestSession(t, leased, sandboxBaseTime.Add(3*time.Second))
	candidates := &controlCandidatesFake{
		record:      repository.CandidateMutationRecord{Candidate: leased},
		checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: ready, candidates: candidates}
	view := ready.Snapshot()
	transitioning, err := sessions.AbandonCandidate(
		context.Background(), view.ProjectID, view.ID, view.Candidate.ID,
		view.Version, view.SessionEpoch, view.Candidate.Version, view.Candidate.WriterLeaseEpoch,
		testActorID, "", "discard superseded experiment",
	)
	if err != nil {
		t.Fatal(err)
	}
	view = transitioning.Snapshot()
	leaseStore := &deadlineLeaseStoreFake{lease: &DeadlineLease{
		SessionID: view.ID, ProjectID: view.ProjectID, Action: DeadlineAbandonCleanup,
		Owner: "deadline-worker", LeaseEpoch: 1, ObservedAt: view.UpdatedAt,
	}}
	controller := &deadlineControllerFake{}
	worker, err := newDeadlineWorker(
		leaseStore, sessions, candidates, controller,
		DeadlineWorkerConfig{WorkerID: "deadline-worker", LeaseDuration: time.Minute, RetryDelay: time.Second},
		func() string { return testCheckpoint },
	)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed || leaseStore.completed != 1 || leaseStore.retried != 0 ||
		len(controller.abandoned) != 1 || len(controller.suspended) != 0 || len(controller.terminated) != 0 {
		t.Fatalf("abandoned cleanup was not dispatched exactly once: processed=%v lease=%#v controller=%#v err=%v", processed, leaseStore, controller, err)
	}
	input := controller.abandoned[0]
	if input.ActorID != SandboxLifecycleWorkerActorID ||
		input.ExpectedSessionVersion != view.Version || input.ExpectedSessionEpoch != view.SessionEpoch ||
		view.Candidate.Status != repository.CandidateAbandoned || view.State != StateTerminating {
		t.Fatalf("background cleanup did not bind the exact abandoned projection: input=%#v view=%#v", input, view)
	}
}
