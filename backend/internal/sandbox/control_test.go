package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

type controlCandidatesFake struct {
	record      repository.CandidateMutationRecord
	checkpoints map[string]repository.CandidateSnapshot
}

func (store *controlCandidatesFake) Get(context.Context, string, string) (repository.CandidateMutationRecord, error) {
	return store.record, nil
}

func (store *controlCandidatesFake) AcquireLease(
	_ context.Context,
	_, _ string,
	expectedVersion uint64,
	actorID string,
	ttl time.Duration,
) (repository.CandidateMutationRecord, error) {
	now := store.record.Candidate.UpdatedAt.Add(100 * time.Millisecond)
	next, _, err := store.record.Candidate.AcquireLease(expectedVersion, actorID, ttl, now)
	if err != nil {
		return repository.CandidateMutationRecord{}, err
	}
	store.record.Candidate = next
	return store.record, nil
}

func (store *controlCandidatesFake) RotateSession(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	actorID string,
) (repository.CandidateMutationRecord, error) {
	next, _, err := store.record.Candidate.RotateSession(
		expectedVersion, expectedEpoch, actorID, "sandbox resume", store.record.Candidate.UpdatedAt.Add(100*time.Millisecond),
	)
	if err != nil {
		return repository.CandidateMutationRecord{}, err
	}
	store.record.Candidate = next
	return store.record, nil
}

func (store *controlCandidatesFake) CreateCheckpoint(
	_ context.Context,
	input repository.CreateCheckpointInput,
) (repository.CandidateSnapshot, error) {
	key := fmt.Sprintf("%d:%s", store.record.Candidate.Version, store.record.Candidate.CurrentTree.TreeHash)
	if existing, ok := store.checkpoints[key]; ok {
		return existing, nil
	}
	snapshot, err := store.record.Candidate.Checkpoint(
		input.ExpectedCandidateVersion, input.ExpectedSessionEpoch, input.ExpectedWriterLeaseEpoch,
		input.ID, input.ActorID, input.Reason, store.record.Candidate.UpdatedAt.Add(100*time.Millisecond),
	)
	if err != nil {
		return repository.CandidateSnapshot{}, err
	}
	store.checkpoints[key] = snapshot
	return snapshot, nil
}

func (store *controlCandidatesFake) Freeze(
	context.Context, string, string, uint64, uint64, uint64, string, string, string,
) (repository.CandidateMutationRecord, error) {
	return repository.CandidateMutationRecord{}, errors.New("unexpected freeze")
}

func (store *controlCandidatesFake) Abandon(
	context.Context, string, string, uint64, uint64, uint64, string, string, string,
) (repository.CandidateMutationRecord, error) {
	return repository.CandidateMutationRecord{}, errors.New("unexpected abandon")
}

type controlSessionsFake struct {
	session    SandboxSession
	candidates *controlCandidatesFake
}

func (store *controlSessionsFake) Get(context.Context, string, string) (SandboxSession, error) {
	return store.session.Clone(), nil
}

func (store *controlSessionsFake) Transition(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	target State,
	actorID, reason, _ string,
) (SandboxSession, error) {
	view := store.session.Snapshot()
	if view.SessionEpoch != expectedEpoch {
		return SandboxSession{}, ErrEpochFenced
	}
	if view.Version != expectedVersion {
		return SandboxSession{}, ErrVersionConflict
	}
	now := view.UpdatedAt.Add(time.Second)
	var next SandboxSession
	var err error
	switch target {
	case StateSuspending:
		next, err = store.session.BeginSuspend(expectedVersion, expectedEpoch, now)
	case StateSuspended:
		next, err = store.session.MarkSuspended(expectedVersion, expectedEpoch, now)
	case StateResuming:
		rotated, rotateErr := store.candidates.RotateSession(
			context.Background(), view.ProjectID, view.Candidate.ID,
			view.Candidate.Version, view.SessionEpoch, actorID,
		)
		if rotateErr != nil {
			return SandboxSession{}, rotateErr
		}
		next = store.session.Clone()
		next.document.State = StateResuming
		next.document.SessionEpoch = rotated.Candidate.SessionEpoch
		next.document.Candidate = candidateState(rotated.Candidate)
		next.document.LastTransition = Transition{
			From: StateSuspended, To: StateResuming, Reason: reason, At: now,
		}
		next.touch(now)
		err = next.Validate()
	case StateReady:
		next, err = store.session.MarkReady(expectedVersion, expectedEpoch, now)
	case StateTerminating:
		if actorID == SandboxLifecycleWorkerActorID {
			next, err = store.session.beginTerminateDeadline(expectedVersion, expectedEpoch, reason, now)
		} else {
			next, err = store.session.BeginTerminate(expectedVersion, expectedEpoch, reason, now)
		}
	case StateTerminated:
		next, err = store.session.MarkTerminated(expectedVersion, expectedEpoch, now)
	case StateFailed:
		next, err = store.session.Fail(expectedVersion, expectedEpoch, reason, now)
	default:
		err = ErrInvalidTransition
	}
	if err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	return next.Clone(), nil
}

func (store *controlSessionsFake) TransitionDeadline(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	target State,
	actorID, reason string,
) (SandboxSession, error) {
	if actorID != SandboxLifecycleWorkerActorID {
		return SandboxSession{}, ErrInvalidSession
	}
	view := store.session.Snapshot()
	if view.Version != expectedVersion || view.SessionEpoch != expectedEpoch {
		return SandboxSession{}, ErrVersionConflict
	}
	now := view.UpdatedAt.Add(time.Second)
	var next SandboxSession
	var err error
	if target == StateTerminating {
		next, err = store.session.beginTerminateDeadline(expectedVersion, expectedEpoch, reason, now)
	} else if target == StateTerminated {
		next, err = store.session.MarkTerminated(expectedVersion, expectedEpoch, now)
	} else {
		err = ErrInvalidTransition
	}
	if err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	return next.Clone(), nil
}

func (store *controlSessionsFake) SyncCandidate(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	_ string,
) (SandboxSession, error) {
	view := store.session.Snapshot()
	if view.Version != expectedVersion {
		return SandboxSession{}, ErrVersionConflict
	}
	if view.SessionEpoch != expectedEpoch {
		return SandboxSession{}, ErrEpochFenced
	}
	next := store.session.Clone()
	next.document.Candidate = candidateState(store.candidates.record.Candidate)
	next.touch(view.UpdatedAt.Add(time.Second))
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	return next.Clone(), nil
}

func (store *controlSessionsFake) AttachCheckpoint(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	_, checkpointID string,
) (SandboxSession, error) {
	view := store.session.Snapshot()
	if view.Version != expectedVersion {
		return SandboxSession{}, ErrVersionConflict
	}
	if view.SessionEpoch != expectedEpoch {
		return SandboxSession{}, ErrEpochFenced
	}
	var snapshot repository.CandidateSnapshot
	found := false
	for _, candidate := range store.candidates.checkpoints {
		if candidate.ID == checkpointID {
			snapshot, found = candidate, true
			break
		}
	}
	if !found {
		return SandboxSession{}, ErrCheckpointMismatch
	}
	reference, err := checkpointReference(snapshot)
	if err != nil {
		return SandboxSession{}, err
	}
	next := store.session.Clone()
	next.document.LatestCheckpoint = &reference
	next.touch(view.UpdatedAt.Add(time.Second))
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	return next.Clone(), nil
}

func (store *controlSessionsFake) AbandonCandidate(
	_ context.Context,
	_, _ string,
	candidateID string,
	expectedVersion, expectedEpoch, expectedCandidateVersion, expectedWriterEpoch uint64,
	actorID, checkpointID, reason string,
) (SandboxSession, error) {
	view := store.session.Snapshot()
	if view.Version != expectedVersion {
		return SandboxSession{}, ErrVersionConflict
	}
	if view.SessionEpoch != expectedEpoch {
		return SandboxSession{}, ErrEpochFenced
	}
	if view.Candidate.ID != candidateID || view.Candidate.Version != expectedCandidateVersion ||
		view.Candidate.WriterLeaseEpoch != expectedWriterEpoch {
		return SandboxSession{}, ErrCandidateVersionConflict
	}
	var checkpoint *repository.CandidateSnapshot
	for _, value := range store.candidates.checkpoints {
		if value.ID == checkpointID {
			copy := value
			checkpoint = &copy
			break
		}
	}
	now := view.UpdatedAt.Add(time.Second)
	abandoned, _, err := store.candidates.record.Candidate.Abandon(
		expectedCandidateVersion, expectedEpoch, actorID, reason, checkpoint, now,
	)
	if err != nil {
		return SandboxSession{}, err
	}
	store.candidates.record.Candidate = abandoned
	next := store.session.Clone()
	next.document.Candidate = candidateState(abandoned)
	next.document.State = StateTerminating
	next.document.LastTransition = Transition{From: view.State, To: StateTerminating, Reason: reason, At: now}
	next.touch(now)
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	return next.Clone(), nil
}

func (store *controlSessionsFake) CompleteCandidateAbandon(
	_ context.Context,
	_, _ string,
	expectedVersion, expectedEpoch uint64,
	_ string,
) (SandboxSession, error) {
	view := store.session.Snapshot()
	if view.Version != expectedVersion {
		return SandboxSession{}, ErrVersionConflict
	}
	if view.SessionEpoch != expectedEpoch {
		return SandboxSession{}, ErrEpochFenced
	}
	if view.State != StateTerminating || view.Candidate.Status != repository.CandidateAbandoned {
		return SandboxSession{}, ErrInvalidTransition
	}
	now := view.UpdatedAt.Add(time.Second)
	next := store.session.Clone()
	next.document.State = StateTerminated
	next.document.LastTransition = Transition{
		From: StateTerminating, To: StateTerminated,
		Reason: "abandoned Candidate runtime terminated", At: now,
	}
	next.touch(now)
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	store.session = next
	return next.Clone(), nil
}

type controlRuntimeFake struct {
	suspendCalls   int
	ensureCalls    int
	startCalls     int
	waitCalls      int
	terminateSpecs []RuntimeSpec
	suspendError   error
	terminateError error
}

type controlResourceFencerFake struct {
	epochs []uint64
}

func (fencer *controlResourceFencerFake) FenceEpoch(
	_ context.Context,
	_, _ string,
	epoch uint64,
	_, _ string,
) error {
	fencer.epochs = append(fencer.epochs, epoch)
	return nil
}

func (runtime *controlRuntimeFake) Ensure(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.ensureCalls++
	return lifecycleRuntimeStatus(spec, "created", false), nil
}

func (runtime *controlRuntimeFake) Start(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.startCalls++
	return lifecycleRuntimeStatus(spec, "running", false), nil
}

func (runtime *controlRuntimeFake) WaitReady(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.waitCalls++
	return lifecycleRuntimeStatus(spec, "running", true), nil
}

func (runtime *controlRuntimeFake) Suspend(_ context.Context, spec RuntimeSpec) (RuntimeStatus, error) {
	runtime.suspendCalls++
	if runtime.suspendError != nil {
		return RuntimeStatus{}, runtime.suspendError
	}
	return lifecycleRuntimeStatus(spec, "paused", true), nil
}

func (*controlRuntimeFake) Resume(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("old runtime must not be resumed across an epoch")
}

func (runtime *controlRuntimeFake) Terminate(_ context.Context, spec RuntimeSpec) error {
	runtime.terminateSpecs = append(runtime.terminateSpecs, spec)
	return runtime.terminateError
}

func TestControlServiceAbandonCommitsFenceThenRetriesRuntimeCleanup(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{
		"README.md": []byte("discard this clean candidate\n"),
	})
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	candidates := &controlCandidatesFake{
		record:      repository.CandidateMutationRecord{Candidate: candidate},
		checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: ready, candidates: candidates}
	leased, err := candidates.AcquireLease(
		context.Background(), candidate.ProjectID, candidate.ID, candidate.Version,
		testActorID, 5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	readyView := ready.Snapshot()
	if _, err := sessions.SyncCandidate(
		context.Background(), readyView.ProjectID, readyView.ID,
		readyView.Version, readyView.SessionEpoch, testActorID,
	); err != nil {
		t.Fatal(err)
	}
	view := sessions.session.Snapshot()
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.Materialize(context.Background(), view, leased.Candidate); err != nil {
		t.Fatal(err)
	}
	runtime := &controlRuntimeFake{terminateError: errors.New("runtime daemon unavailable")}
	resources := &controlResourceFencerFake{}
	service, err := newControlService(
		sessions, candidates, workspaces, runtime, &facadeAccessFake{}, &lifecycleEventsFake{},
		func() string { return uuid.NewString() }, resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	input := CandidateAbandonInput{
		ProjectID: view.ProjectID, SessionID: view.ID, CandidateID: view.Candidate.ID,
		ActorID: testActorID, Reason: "discard superseded experiment",
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
		ExpectedCandidateVersion: view.Candidate.Version,
		ExpectedWriterLeaseEpoch: view.Candidate.WriterLeaseEpoch,
	}

	partial, err := service.AbandonCandidate(context.Background(), input)
	var lifecycleError *LifecycleControlError
	if !errors.As(err, &lifecycleError) || partial.Session.State != StateTerminating ||
		partial.Session.Candidate.Status != repository.CandidateAbandoned ||
		candidates.record.Candidate.Status != repository.CandidateAbandoned ||
		candidates.record.Candidate.Version != input.ExpectedCandidateVersion+1 ||
		len(runtime.terminateSpecs) != 1 || len(resources.epochs) != 0 {
		t.Fatalf("abandon failure did not preserve durable retry state: result=%#v candidate=%#v calls=%d err=%v", partial, candidates.record.Candidate, len(runtime.terminateSpecs), err)
	}

	secondPartial, err := service.AbandonCandidate(context.Background(), input)
	if !errors.As(err, &lifecycleError) || secondPartial.Session.Version != partial.Session.Version ||
		candidates.record.Candidate.Version != input.ExpectedCandidateVersion+1 ||
		len(runtime.terminateSpecs) != 2 {
		t.Fatalf("same abandon retry repeated the Candidate transition: result=%#v candidate=%#v calls=%d err=%v", secondPartial, candidates.record.Candidate, len(runtime.terminateSpecs), err)
	}

	runtime.terminateError = nil
	completedView, err := service.completeAbandonDeadline(context.Background(), SessionControlInput{
		ProjectID: partial.Session.ProjectID, SessionID: partial.Session.ID,
		ActorID:                SandboxLifecycleWorkerActorID,
		ExpectedSessionVersion: partial.Session.Version,
		ExpectedSessionEpoch:   partial.Session.SessionEpoch,
	})
	if err != nil || completedView.State != StateTerminated ||
		completedView.Candidate.Status != repository.CandidateAbandoned ||
		candidates.record.Candidate.Version != input.ExpectedCandidateVersion+1 ||
		len(runtime.terminateSpecs) != 3 || len(resources.epochs) != 1 {
		t.Fatalf("background abandon recovery did not finish exact cleanup: view=%#v calls=%d fences=%v err=%v", completedView, len(runtime.terminateSpecs), resources.epochs, err)
	}
	if _, exists, err := workspaces.ExistingMount(view.ID); err != nil || exists {
		t.Fatalf("abandoned workspace was not removed: exists=%t err=%v", exists, err)
	}

	replayed, err := service.AbandonCandidate(context.Background(), input)
	if err != nil || replayed.Session.Version != completedView.Version ||
		replayed.Candidate.Status != repository.CandidateAbandoned ||
		len(runtime.terminateSpecs) != 3 || candidates.record.Candidate.Version != input.ExpectedCandidateVersion+1 {
		t.Fatalf("completed abandon replay repeated a terminal transition: result=%#v calls=%d err=%v", replayed, len(runtime.terminateSpecs), err)
	}
}

func (*controlRuntimeFake) Inspect(context.Context, RuntimeSpec) (RuntimeStatus, error) {
	return RuntimeStatus{}, errors.New("unexpected inspect")
}

func TestControlServiceClosesSuspendResumeTerminateLifecycleAndRetries(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{
		"README.md": []byte("sandbox lifecycle\n"),
	})
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	candidates := &controlCandidatesFake{
		record: repository.CandidateMutationRecord{Candidate: candidate}, checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: ready, candidates: candidates}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlRuntimeFake{}
	resources := &controlResourceFencerFake{}
	service, err := newControlService(
		sessions, candidates, workspaces, runtime, &facadeAccessFake{}, &lifecycleEventsFake{},
		func() string { return "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" },
		resources,
	)
	if err != nil {
		t.Fatal(err)
	}

	readyView := ready.Snapshot()
	suspendInput := SessionControlInput{
		ProjectID: readyView.ProjectID, SessionID: readyView.ID, ActorID: testActorID,
		ExpectedSessionVersion: readyView.Version, ExpectedSessionEpoch: readyView.SessionEpoch,
	}
	suspended, err := service.Suspend(context.Background(), suspendInput)
	if err != nil || suspended.State != StateSuspended || runtime.suspendCalls != 1 {
		t.Fatalf("suspend = %#v calls=%d err=%v", suspended, runtime.suspendCalls, err)
	}
	if replayed, err := service.Suspend(context.Background(), suspendInput); err != nil ||
		replayed.Version != suspended.Version || runtime.suspendCalls != 1 {
		t.Fatalf("suspend retry was not idempotent: %#v calls=%d err=%v", replayed, runtime.suspendCalls, err)
	}

	resumeInput := SessionControlInput{
		ProjectID: suspended.ProjectID, SessionID: suspended.ID, ActorID: testActorID,
		ExpectedSessionVersion: suspended.Version, ExpectedSessionEpoch: suspended.SessionEpoch,
	}
	resumed, err := service.Resume(context.Background(), resumeInput)
	if err != nil || resumed.State != StateReady || resumed.SessionEpoch != suspended.SessionEpoch+1 ||
		resumed.Candidate.SessionEpoch != resumed.SessionEpoch || resumed.Candidate.WriterLeaseEpoch <= suspended.Candidate.WriterLeaseEpoch {
		t.Fatalf("resume = %#v err=%v", resumed, err)
	}
	if runtime.ensureCalls != 1 || runtime.startCalls != 1 || runtime.waitCalls != 1 || len(runtime.terminateSpecs) != 1 ||
		runtime.terminateSpecs[0].SessionEpoch != suspended.SessionEpoch ||
		len(resources.epochs) != 1 || resources.epochs[0] != suspended.SessionEpoch {
		t.Fatalf("resume runtime calls = ensure:%d start:%d wait:%d terminate:%#v", runtime.ensureCalls, runtime.startCalls, runtime.waitCalls, runtime.terminateSpecs)
	}
	if replayed, err := service.Resume(context.Background(), resumeInput); err != nil || replayed.Version != resumed.Version ||
		runtime.ensureCalls != 1 {
		t.Fatalf("resume retry was not idempotent: %#v err=%v", replayed, err)
	}

	terminateInput := TerminateSessionInput{
		SessionControlInput: SessionControlInput{
			ProjectID: resumed.ProjectID, SessionID: resumed.ID, ActorID: testActorID,
			ExpectedSessionVersion: resumed.Version, ExpectedSessionEpoch: resumed.SessionEpoch,
		},
		Reason: "user closed the sandbox",
	}
	terminated, err := service.Terminate(context.Background(), terminateInput)
	if err != nil || terminated.State != StateTerminated || len(runtime.terminateSpecs) != 2 ||
		runtime.terminateSpecs[1].SessionEpoch != resumed.SessionEpoch ||
		len(resources.epochs) != 2 || resources.epochs[1] != resumed.SessionEpoch {
		t.Fatalf("terminate = %#v specs=%#v err=%v", terminated, runtime.terminateSpecs, err)
	}
	if _, err := os.Stat(workspaces.mount(terminated.ID).SessionRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminated workspace was retained: %v", err)
	}
	if replayed, err := service.Terminate(context.Background(), terminateInput); err != nil ||
		replayed.Version != terminated.Version || len(runtime.terminateSpecs) != 2 {
		t.Fatalf("terminate retry was not idempotent: %#v err=%v", replayed, err)
	}
}

func TestControlServiceAbsoluteTTLTerminatesDirtySessionWithoutEditorCheckpoint(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("base\n")})
	leased, lease, err := candidate.AcquireLease(
		candidate.Version, testActorID, 20*time.Minute, candidate.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("durable edit\n")
	digest := sha256.Sum256(value)
	hash := fmt.Sprintf("sha256:%x", digest)
	dirty, _, err := leased.Apply(
		leased.Version, leased.SessionEpoch, lease.Epoch, testActorID, "user",
		repository.FileOperation{
			ID: "ttl-edit", Kind: repository.OperationUpsert, Path: "notes.txt",
			ContentHash: hash, ByteSize: int64(len(value)), Mode: "100644",
		},
		leased.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	blobs[hash] = value
	ready := readyTestSession(t, dirty, sandboxBaseTime)
	candidates := &controlCandidatesFake{
		record:      repository.CandidateMutationRecord{Candidate: dirty},
		checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: ready, candidates: candidates}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	view := ready.Snapshot()
	if _, err := workspaces.Materialize(context.Background(), view, dirty); err != nil {
		t.Fatal(err)
	}
	runtime := &controlRuntimeFake{}
	resources := &controlResourceFencerFake{}
	service, err := newControlService(
		sessions, candidates, workspaces, runtime, &facadeAccessFake{}, &lifecycleEventsFake{},
		func() string { return uuid.NewString() }, resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	input := TerminateSessionInput{
		SessionControlInput: SessionControlInput{
			ProjectID: view.ProjectID, SessionID: view.ID, ActorID: SandboxLifecycleWorkerActorID,
			ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
		},
		Reason: "SandboxSession absolute TTL elapsed; the durable Candidate is retained independently",
	}
	if _, err := service.Terminate(context.Background(), input); !errors.Is(err, ErrActionBlocked) {
		t.Fatalf("ordinary terminate bypassed dirty Candidate guard: %v", err)
	}
	terminated, err := service.terminateDeadline(context.Background(), input)
	if err != nil || terminated.State != StateTerminated || len(runtime.terminateSpecs) != 1 ||
		len(resources.epochs) != 1 || candidates.record.Candidate.Version != dirty.Version ||
		len(candidates.checkpoints) != 0 {
		t.Fatalf("absolute TTL did not clean only the runtime boundary: view=%#v runtime=%d fences=%v candidate=%#v err=%v", terminated, len(runtime.terminateSpecs), resources.epochs, candidates.record.Candidate, err)
	}
	if _, exists, err := workspaces.ExistingMount(view.ID); err != nil || exists {
		t.Fatalf("absolute TTL retained the disposable workspace: exists=%t err=%v", exists, err)
	}
}

func TestControlServiceDirtyResumeCreatesFreshExactCheckpoint(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("base\n")})
	leased, lease, err := candidate.AcquireLease(
		candidate.Version, testActorID, 20*time.Minute, candidate.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	value := []byte("generated\n")
	digest := sha256.Sum256(value)
	hash := fmt.Sprintf("sha256:%x", digest)
	dirty, _, err := leased.Apply(
		leased.Version, leased.SessionEpoch, lease.Epoch, testActorID, "user",
		repository.FileOperation{
			ID: "dirty-resume-file", Kind: repository.OperationUpsert, Path: "src/generated.ts",
			ContentHash: hash, ByteSize: int64(len(value)), Mode: "100644",
		},
		leased.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	blobs[hash] = value
	oldCheckpoint, err := dirty.Checkpoint(
		dirty.Version, dirty.SessionEpoch, dirty.WriterLeaseEpoch,
		testCheckpoint, testActorID, "before suspend", dirty.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	input := testSessionInput(dirty)
	input.LatestCheckpoint = &oldCheckpoint
	session, err := NewSession(input, oldCheckpoint.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	session = mustBeginStart(t, session, session.Snapshot().UpdatedAt.Add(time.Second))
	session = mustMarkReady(t, session, session.Snapshot().UpdatedAt.Add(time.Second))
	candidates := &controlCandidatesFake{
		record: repository.CandidateMutationRecord{Candidate: dirty}, checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: session, candidates: candidates}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}
	service, err := newControlService(
		sessions, candidates, workspaces, &controlRuntimeFake{}, &facadeAccessFake{}, &lifecycleEventsFake{},
		func() string { value := ids[0]; ids = ids[1:]; return value },
	)
	if err != nil {
		t.Fatal(err)
	}
	ready := session.Snapshot()
	suspended, err := service.Suspend(context.Background(), SessionControlInput{
		ProjectID: ready.ProjectID, SessionID: ready.ID, ActorID: testActorID,
		ExpectedSessionVersion: ready.Version, ExpectedSessionEpoch: ready.SessionEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := service.Resume(context.Background(), SessionControlInput{
		ProjectID: suspended.ProjectID, SessionID: suspended.ID, ActorID: testActorID,
		ExpectedSessionVersion: suspended.Version, ExpectedSessionEpoch: suspended.SessionEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := resumed.LatestCheckpoint
	if checkpoint == nil || checkpoint.ID == oldCheckpoint.ID ||
		checkpoint.CandidateVersion != resumed.Candidate.Version ||
		checkpoint.SessionEpoch != resumed.SessionEpoch ||
		checkpoint.WriterLeaseEpoch != resumed.Candidate.WriterLeaseEpoch ||
		checkpoint.TreeHash != resumed.Candidate.TreeHash || !exactCheckpointForView(resumed) {
		t.Fatalf("dirty resume checkpoint is not exact and fresh: session=%#v checkpoint=%#v", resumed, checkpoint)
	}
}

func TestControlServiceSuspendFailureFailsClosedAndCleansRuntime(t *testing.T) {
	candidate, blobs := workspaceCandidate(t, map[string][]byte{"README.md": []byte("failure\n")})
	ready := readyTestSession(t, candidate, sandboxBaseTime)
	candidates := &controlCandidatesFake{
		record: repository.CandidateMutationRecord{Candidate: candidate}, checkpoints: map[string]repository.CandidateSnapshot{},
	}
	sessions := &controlSessionsFake{session: ready, candidates: candidates}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: blobs})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlRuntimeFake{suspendError: ErrRuntimeUnavailable}
	resources := &controlResourceFencerFake{}
	service, err := newControlService(
		sessions, candidates, workspaces, runtime, &facadeAccessFake{}, &lifecycleEventsFake{},
		func() string { return "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" },
		resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	view := ready.Snapshot()
	failed, err := service.Suspend(context.Background(), SessionControlInput{
		ProjectID: view.ProjectID, SessionID: view.ID, ActorID: testActorID,
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
	})
	var controlErr *LifecycleControlError
	if !errors.As(err, &controlErr) || !errors.Is(err, ErrRuntimeUnavailable) ||
		failed.State != StateFailed || len(runtime.terminateSpecs) != 1 ||
		len(resources.epochs) != 1 || resources.epochs[0] != view.SessionEpoch {
		t.Fatalf("suspend failure = %#v cleanup=%#v err=%v", failed, runtime.terminateSpecs, err)
	}
}

type controlPostgresTreeReader struct {
	tree repository.TreeManifest
}

func (reader *controlPostgresTreeReader) Get(
	context.Context,
	string,
	string,
	repository.TreeBlobPointer,
) (repository.TreeManifest, error) {
	return reader.tree, nil
}

func TestControlServicePostgresLifecycleCanary(t *testing.T) {
	fixture := openSandboxStorePostgresFixture(t)
	emptyTree, err := repository.NewTree(nil)
	if err != nil {
		t.Fatal(err)
	}
	transaction := fixture.store.database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	defer transaction.Rollback()
	if err := transaction.Exec(`SET LOCAL session_replication_role = replica`).Error; err != nil {
		t.Fatal(err)
	}
	if err := transaction.Exec(`
UPDATE repository_snapshots
SET tree_store = ?, tree_hash = ?, tree_file_count = 0, tree_byte_size = 0
WHERE id = ?
`, repository.TreeContentStore, emptyTree.TreeHash, fixture.candidate.RepositorySnapshotID).Error; err != nil {
		t.Fatal(err)
	}
	if err := transaction.Exec(`
UPDATE candidate_workspaces
SET base_tree_store = ?, current_tree_store = ?, base_tree_hash = ?, current_tree_hash = ?,
    current_tree_file_count = 0, current_tree_byte_size = 0
WHERE id = ?
`, repository.TreeContentStore, repository.TreeContentStore, emptyTree.TreeHash, emptyTree.TreeHash, fixture.candidateID).Error; err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit().Error; err != nil {
		t.Fatal(err)
	}
	fixture.candidate.BaseTreeHash = emptyTree.TreeHash
	fixture.candidate.CurrentTree = emptyTree

	candidateStore, err := repository.NewGORMCandidateStore(
		fixture.store.database, &controlPostgresTreeReader{tree: emptyTree},
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := repository.NewCandidateControlStore(fixture.store.database, candidateStore)
	if err != nil {
		t.Fatal(err)
	}
	record, err := candidates.Get(fixture.context, fixture.projectID.String(), fixture.candidateID.String())
	if err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.New()
	input := fixture.sessionInput(sessionID)
	input.Candidate = record.Candidate
	created, err := fixture.store.Create(fixture.context, input, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	view := created.Snapshot()
	for _, target := range []State{StateStarting, StateReady} {
		advanced, transitionErr := fixture.store.Transition(
			fixture.context, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
			target, fixture.actorID.String(), "postgres control canary", "",
		)
		if transitionErr != nil {
			t.Fatal(transitionErr)
		}
		view = advanced.Snapshot()
	}
	workspaces, err := NewWorkspaceMaterializer(t.TempDir(), &workspaceResolverFake{values: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlRuntimeFake{}
	service, err := NewControlService(
		fixture.store, candidates, workspaces, runtime, &facadeAccessFake{}, &lifecycleEventsFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := service.Suspend(fixture.context, SessionControlInput{
		ProjectID: view.ProjectID, SessionID: view.ID, ActorID: fixture.actorID.String(),
		ExpectedSessionVersion: view.Version, ExpectedSessionEpoch: view.SessionEpoch,
	})
	if err != nil || suspended.State != StateSuspended {
		t.Fatalf("postgres suspend = %#v err=%v", suspended, err)
	}
	resumed, err := service.Resume(fixture.context, SessionControlInput{
		ProjectID: suspended.ProjectID, SessionID: suspended.ID, ActorID: fixture.actorID.String(),
		ExpectedSessionVersion: suspended.Version, ExpectedSessionEpoch: suspended.SessionEpoch,
	})
	if err != nil || resumed.State != StateReady || resumed.SessionEpoch != suspended.SessionEpoch+1 ||
		resumed.Candidate.WriterLeaseEpoch <= suspended.Candidate.WriterLeaseEpoch {
		t.Fatalf("postgres resume = %#v err=%v", resumed, err)
	}
	terminated, err := service.Terminate(fixture.context, TerminateSessionInput{
		SessionControlInput: SessionControlInput{
			ProjectID: resumed.ProjectID, SessionID: resumed.ID, ActorID: fixture.actorID.String(),
			ExpectedSessionVersion: resumed.Version, ExpectedSessionEpoch: resumed.SessionEpoch,
		},
		Reason: "postgres lifecycle canary complete",
	})
	if err != nil || terminated.State != StateTerminated {
		t.Fatalf("postgres terminate = %#v err=%v", terminated, err)
	}
}
