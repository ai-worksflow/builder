package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

var ErrControlUnavailable = errors.New("sandbox lifecycle control is not configured")

const sandboxControlTimeout = 12 * time.Minute
const sandboxResumeLeaseTTL = 5 * time.Minute

type ControlSessionStore interface {
	Get(context.Context, string, string) (SandboxSession, error)
	Transition(context.Context, string, string, uint64, uint64, State, string, string, string) (SandboxSession, error)
	SyncCandidate(context.Context, string, string, uint64, uint64, string) (SandboxSession, error)
	AttachCheckpoint(context.Context, string, string, uint64, uint64, string, string) (SandboxSession, error)
	AbandonCandidate(
		context.Context, string, string, string, uint64, uint64, uint64, uint64, string, string, string,
	) (SandboxSession, error)
	CompleteCandidateAbandon(context.Context, string, string, uint64, uint64, string) (SandboxSession, error)
}

type SessionControlInput struct {
	ProjectID              string
	SessionID              string
	ActorID                string
	ExpectedSessionVersion uint64
	ExpectedSessionEpoch   uint64
}

type TerminateSessionInput struct {
	SessionControlInput
	Reason string
}

type CandidateAbandonInput struct {
	ProjectID                string
	SessionID                string
	CandidateID              string
	ActorID                  string
	CheckpointID             string
	Reason                   string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
}

type LifecycleControlError struct {
	Session SessionView
	Cause   error
}

func (err *LifecycleControlError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sandbox lifecycle control failed for session %s: %v", err.Session.ID, err.Cause)
}

func (err *LifecycleControlError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

type ControlService struct {
	sessions   ControlSessionStore
	candidates CandidateControls
	workspaces *WorkspaceMaterializer
	runtime    RuntimeManager
	resources  RuntimeEpochFencer
	access     ProjectAuthorizer
	events     StreamEventStore
	newID      func() string
}

type RuntimeEpochFencer interface {
	FenceEpoch(context.Context, string, string, uint64, string, string) error
}

func NewControlService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeManager,
	access ProjectAuthorizer,
	events StreamEventStore,
	resources ...RuntimeEpochFencer,
) (*ControlService, error) {
	return newControlService(sessions, candidates, workspaces, runtime, access, events, uuid.NewString, resources...)
}

func newControlService(
	sessions ControlSessionStore,
	candidates CandidateControls,
	workspaces *WorkspaceMaterializer,
	runtime RuntimeManager,
	access ProjectAuthorizer,
	events StreamEventStore,
	newID func() string,
	resources ...RuntimeEpochFencer,
) (*ControlService, error) {
	if sessions == nil || candidates == nil || workspaces == nil || runtime == nil ||
		access == nil || events == nil || newID == nil {
		return nil, errors.New("sandbox control stores, workspace, runtime, access, events, and ID source are required")
	}
	if len(resources) > 1 || (len(resources) == 1 && resources[0] == nil) {
		return nil, errors.New("at most one non-nil sandbox runtime resource fencer is allowed")
	}
	var resourceFencer RuntimeEpochFencer
	if len(resources) == 1 {
		resourceFencer = resources[0]
	}
	return &ControlService{
		sessions: sessions, candidates: candidates, workspaces: workspaces,
		runtime: runtime, resources: resourceFencer, access: access, events: events, newID: newID,
	}, nil
}

func (service *ControlService) Suspend(ctx context.Context, input SessionControlInput) (SessionView, error) {
	if err := service.validate(ctx, input); err != nil {
		return SessionView{}, err
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return SessionView{}, fmt.Errorf("authorize sandbox suspend: %w", err)
	}
	return service.suspend(ctx, input, "user requested suspend")
}

func (service *ControlService) suspendDeadline(
	ctx context.Context,
	input SessionControlInput,
) (SessionView, error) {
	if err := service.validate(ctx, input); err != nil {
		return SessionView{}, err
	}
	return service.suspend(ctx, input, "idle TTL elapsed; checkpointed Candidate before hibernate")
}

func (service *ControlService) suspend(
	ctx context.Context,
	input SessionControlInput,
	reason string,
) (SessionView, error) {
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return SessionView{}, err
	}
	view := session.Snapshot()
	if service.completedSuspendRetry(view, input) {
		return view, nil
	}
	retrying := view.State == StateSuspending && view.SessionEpoch == input.ExpectedSessionEpoch &&
		view.Version == input.ExpectedSessionVersion+1
	if !retrying {
		if err := session.Authorize(ActionSuspend, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
			return SessionView{}, err
		}
	}
	record, err := service.candidates.Get(ctx, input.ProjectID, view.Candidate.ID)
	if err != nil {
		return SessionView{}, err
	}
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		return SessionView{}, ErrSessionProjectionStale
	}
	mount, err := service.workspaces.Materialize(ctx, view, record.Candidate)
	if err != nil {
		return SessionView{}, err
	}
	spec, err := RuntimeSpecForSession(view, mount)
	if err != nil {
		return SessionView{}, err
	}
	if !retrying {
		transitioned, transitionErr := service.sessions.Transition(
			ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
			StateSuspending, input.ActorID, reason, "",
		)
		if transitionErr != nil {
			return SessionView{}, transitionErr
		}
		view = transitioned.Snapshot()
		service.publishState(ctx, view)
	}
	durable, cancel := service.durableContext(ctx)
	defer cancel()
	if _, err := service.runtime.Suspend(durable, spec); err != nil {
		return service.failAndCleanup(durable, view, input.ActorID, &spec, "runtime suspend failed", err)
	}
	suspended, err := service.transitionOrCurrent(
		durable, view, input.ActorID, StateSuspended, "runtime suspended", nil,
	)
	if err != nil {
		return service.failAndCleanup(durable, view, input.ActorID, &spec, "sandbox suspend reconciliation failed", err)
	}
	service.publishState(durable, suspended)
	return suspended, nil
}

func (service *ControlService) Resume(ctx context.Context, input SessionControlInput) (SessionView, error) {
	if err := service.validate(ctx, input); err != nil {
		return SessionView{}, err
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return SessionView{}, fmt.Errorf("authorize sandbox resume: %w", err)
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return SessionView{}, err
	}
	view := session.Snapshot()
	if service.completedResumeRetry(view, input) {
		return view, nil
	}
	retrying := view.State == StateResuming && view.SessionEpoch == input.ExpectedSessionEpoch+1 &&
		view.Version >= input.ExpectedSessionVersion+1
	if !retrying {
		if err := session.Authorize(ActionResume, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
			return SessionView{}, err
		}
		record, err := service.candidates.Get(ctx, input.ProjectID, view.Candidate.ID)
		if err != nil {
			return SessionView{}, err
		}
		if !candidateProjectionMatches(view.Candidate, record.Candidate) {
			return SessionView{}, ErrSessionProjectionStale
		}
		mount, err := service.workspaces.Materialize(ctx, view, record.Candidate)
		if err != nil {
			return SessionView{}, err
		}
		oldSpec, err := RuntimeSpecForSession(view, mount)
		if err != nil {
			return SessionView{}, err
		}
		cleanup, cancel := service.durableContext(ctx)
		if err := service.runtime.Terminate(cleanup, oldSpec); err != nil {
			cancel()
			return SessionView{}, err
		}
		if err := service.fenceRuntimeResources(
			cleanup, view, input.ActorID, "old sandbox runtime was terminated for resume",
		); err != nil {
			cancel()
			return SessionView{}, err
		}
		cancel()
		transitioned, err := service.sessions.Transition(
			ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
			StateResuming, input.ActorID, "user requested resume", "",
		)
		if err != nil {
			return SessionView{}, err
		}
		view = transitioned.Snapshot()
		service.publishState(ctx, view)
	}
	durable, cancel := service.durableContext(ctx)
	defer cancel()
	return service.finishResume(durable, view, input.ActorID)
}

func (service *ControlService) finishResume(
	ctx context.Context,
	view SessionView,
	actorID string,
) (SessionView, error) {
	view, record, err := service.ensureResumeLease(ctx, view, actorID)
	if err != nil {
		return view, err
	}
	view, err = service.ensureResumeCheckpoint(ctx, view, record.Candidate, actorID)
	if err != nil {
		return view, err
	}
	mount, err := service.workspaces.RebindSession(ctx, view, record.Candidate)
	if err != nil {
		return service.failAndCleanup(ctx, view, actorID, nil, "workspace resume reconciliation failed", err)
	}
	spec, err := RuntimeSpecForSession(view, mount)
	if err != nil {
		return service.failAndCleanup(ctx, view, actorID, nil, "runtime resume specification failed", err)
	}
	if _, err := service.runtime.Ensure(ctx, spec); err != nil {
		return service.failAndCleanup(ctx, view, actorID, &spec, "runtime resume failed", err)
	}
	if _, err := service.runtime.Start(ctx, spec); err != nil {
		return service.failAndCleanup(ctx, view, actorID, &spec, "runtime resume failed", err)
	}
	if _, err := service.runtime.WaitReady(ctx, spec); err != nil {
		return service.failAndCleanup(ctx, view, actorID, &spec, "runtime resume failed", err)
	}
	ready, err := service.transitionOrCurrent(ctx, view, actorID, StateReady, "runtime ready", nil)
	if err != nil {
		return service.failAndCleanup(ctx, view, actorID, &spec, "sandbox resume reconciliation failed", err)
	}
	service.publishState(ctx, ready)
	return ready, nil
}

func (service *ControlService) ensureResumeLease(
	ctx context.Context,
	view SessionView,
	actorID string,
) (SessionView, repository.CandidateMutationRecord, error) {
	record, err := service.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return view, repository.CandidateMutationRecord{}, errors.Join(ErrSessionReconciliation, err)
	}
	projectionExact := candidateProjectionMatches(view.Candidate, record.Candidate)
	if !projectionExact && !resumeLeaseAdvanceMatches(view.Candidate, record.Candidate, actorID) {
		return view, repository.CandidateMutationRecord{}, ErrSessionProjectionStale
	}
	lease := record.Candidate.Lease
	if projectionExact && (lease == nil || lease.OwnerID != actorID || !lease.ExpiresAt.After(time.Now().UTC().Add(5*time.Second))) {
		leased, leaseErr := service.candidates.AcquireLease(
			ctx, view.ProjectID, view.Candidate.ID, view.Candidate.Version, actorID, sandboxResumeLeaseTTL,
		)
		if leaseErr != nil {
			return view, repository.CandidateMutationRecord{}, leaseErr
		}
		record = leased
		if !resumeLeaseAdvanceMatches(view.Candidate, record.Candidate, actorID) {
			return view, repository.CandidateMutationRecord{}, ErrSessionProjectionStale
		}
		projectionExact = false
	}
	if projectionExact {
		return view, record, nil
	}
	synced, syncErr := service.sessions.SyncCandidate(
		ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch, actorID,
	)
	if syncErr != nil {
		current, loadErr := service.sessions.Get(ctx, view.ProjectID, view.ID)
		if loadErr == nil && candidateProjectionMatches(current.Snapshot().Candidate, record.Candidate) {
			view = current.Snapshot()
			service.publishState(ctx, view)
			return view, record, nil
		}
		return view, repository.CandidateMutationRecord{}, errors.Join(ErrSessionReconciliation, syncErr, loadErr)
	}
	view = synced.Snapshot()
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		return view, repository.CandidateMutationRecord{}, ErrSessionProjectionStale
	}
	service.publishState(ctx, view)
	return view, record, nil
}

func resumeLeaseAdvanceMatches(projected CandidateState, candidate repository.CandidateWorkspace, actorID string) bool {
	lease := candidate.Lease
	return lease != nil && lease.OwnerID == actorID && lease.Epoch == candidate.WriterLeaseEpoch &&
		candidate.ID == projected.ID && candidate.RepositorySnapshotID == projected.RepositorySnapshotID &&
		candidate.BaseTreeHash == projected.BaseTreeHash && candidate.CurrentTree.TreeHash == projected.TreeHash &&
		candidate.Version == projected.Version+1 && candidate.JournalSequence == projected.JournalSequence &&
		candidate.SessionEpoch == projected.SessionEpoch && candidate.WriterLeaseEpoch == projected.WriterLeaseEpoch+1 &&
		candidate.Dirty == projected.Dirty && candidate.Conflicted == projected.Conflicted &&
		candidate.Stale == projected.Stale && candidate.RebaseRequired == projected.RebaseRequired
}

func (service *ControlService) ensureResumeCheckpoint(
	ctx context.Context,
	view SessionView,
	candidate repository.CandidateWorkspace,
	actorID string,
) (SessionView, error) {
	if !candidate.Dirty || exactCheckpointForView(view) {
		return view, nil
	}
	checkpointID := service.newID()
	if !validUUID(checkpointID) {
		return view, fmt.Errorf("%w: generated checkpoint ID", ErrInvalidSession)
	}
	checkpoint, err := service.candidates.CreateCheckpoint(ctx, repository.CreateCheckpointInput{
		ID: checkpointID, ProjectID: view.ProjectID, CandidateID: candidate.ID,
		ExpectedCandidateVersion: candidate.Version,
		ExpectedSessionEpoch:     candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: candidate.WriterLeaseEpoch,
		ActorID:                  actorID, Reason: "sandbox resume epoch rotation",
	})
	if err != nil {
		return view, err
	}
	attached, err := service.sessions.AttachCheckpoint(
		ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch, actorID, checkpoint.ID,
	)
	if err != nil {
		current, loadErr := service.sessions.Get(ctx, view.ProjectID, view.ID)
		if loadErr == nil && exactCheckpointForView(current.Snapshot()) {
			return current.Snapshot(), nil
		}
		return view, errors.Join(ErrSessionReconciliation, err, loadErr)
	}
	return attached.Snapshot(), nil
}

func (service *ControlService) Terminate(ctx context.Context, input TerminateSessionInput) (SessionView, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" || len(input.Reason) > 1000 {
		return SessionView{}, fmt.Errorf("%w: termination reason is required and bounded", ErrInvalidSession)
	}
	if err := service.validate(ctx, input.SessionControlInput); err != nil {
		return SessionView{}, err
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return SessionView{}, fmt.Errorf("authorize sandbox terminate: %w", err)
	}
	return service.terminate(ctx, input)
}

// AbandonCandidate is a destructive, exact-lineage operation. PostgreSQL
// commits the Candidate abandonment and SandboxSession terminating projection
// atomically before any fallible runtime cleanup begins. A retry carrying the
// original fences resumes cleanup; it cannot abandon a different Candidate.
func (service *ControlService) AbandonCandidate(
	ctx context.Context,
	input CandidateAbandonInput,
) (CandidateSessionResult, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	input.CheckpointID = strings.TrimSpace(input.CheckpointID)
	if input.Reason == "" || len(input.Reason) > 1000 ||
		!validUUID(input.CandidateID) || input.ExpectedCandidateVersion == 0 ||
		input.ExpectedWriterLeaseEpoch == 0 {
		return CandidateSessionResult{}, fmt.Errorf("%w: exact Candidate and bounded abandonment reason are required", ErrInvalidSession)
	}
	if err := service.validate(ctx, SessionControlInput{
		ProjectID: input.ProjectID, SessionID: input.SessionID, ActorID: input.ActorID,
		ExpectedSessionVersion: input.ExpectedSessionVersion,
		ExpectedSessionEpoch:   input.ExpectedSessionEpoch,
	}); err != nil {
		return CandidateSessionResult{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return CandidateSessionResult{}, fmt.Errorf("authorize Candidate abandon: %w", err)
	}
	if err := service.access.RequireSandboxControl(ctx, input.ProjectID, input.ActorID); err != nil {
		return CandidateSessionResult{}, fmt.Errorf("authorize sandbox abandon cleanup: %w", err)
	}

	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return CandidateSessionResult{}, err
	}
	view := session.Snapshot()
	if service.completedAbandonRetry(view, input) {
		return service.abandonResult(ctx, view)
	}
	retrying := service.pendingAbandonRetry(view, input)
	if !retrying {
		if err := session.Authorize(ActionAbandon, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
			return CandidateSessionResult{}, err
		}
		if view.Candidate.ID != input.CandidateID ||
			view.Candidate.Version != input.ExpectedCandidateVersion ||
			view.Candidate.WriterLeaseEpoch != input.ExpectedWriterLeaseEpoch {
			return CandidateSessionResult{}, ErrSessionProjectionStale
		}
		checkpointExact := exactCheckpointForView(view)
		if input.CheckpointID != "" && (!checkpointExact || view.LatestCheckpoint.ID != input.CheckpointID) {
			return CandidateSessionResult{}, ErrCheckpointMismatch
		}
		if view.Candidate.Dirty && (input.CheckpointID == "" || !checkpointExact) {
			return CandidateSessionResult{}, ErrCheckpointRequired
		}
		record, recordErr := service.candidates.Get(ctx, input.ProjectID, input.CandidateID)
		if recordErr != nil {
			return CandidateSessionResult{}, recordErr
		}
		if !candidateProjectionMatches(view.Candidate, record.Candidate) {
			return CandidateSessionResult{}, ErrSessionProjectionStale
		}
		transitioned, abandonErr := service.sessions.AbandonCandidate(
			ctx, input.ProjectID, input.SessionID, input.CandidateID,
			input.ExpectedSessionVersion, input.ExpectedSessionEpoch,
			input.ExpectedCandidateVersion, input.ExpectedWriterLeaseEpoch,
			input.ActorID, input.CheckpointID, input.Reason,
		)
		if abandonErr != nil {
			return CandidateSessionResult{}, abandonErr
		}
		view = transitioned.Snapshot()
		service.publishState(ctx, view)
	}

	durable, cancel := service.durableContext(ctx)
	defer cancel()
	if cleanupErr := service.cleanupAbandonedRuntime(durable, view, input.ActorID); cleanupErr != nil {
		return CandidateSessionResult{Session: view}, &LifecycleControlError{
			Session: view, Cause: errors.Join(ErrSessionReconciliation, cleanupErr),
		}
	}
	terminated, err := service.sessions.CompleteCandidateAbandon(
		durable, view.ProjectID, view.ID, view.Version, view.SessionEpoch, input.ActorID,
	)
	if err != nil {
		current, loadErr := service.sessions.Get(durable, view.ProjectID, view.ID)
		if loadErr == nil && service.completedAbandonRetry(current.Snapshot(), input) {
			return service.abandonResult(durable, current.Snapshot())
		}
		return CandidateSessionResult{Session: view}, &LifecycleControlError{
			Session: view, Cause: errors.Join(ErrSessionReconciliation, err, loadErr),
		}
	}
	view = terminated.Snapshot()
	service.publishState(durable, view)
	return service.abandonResult(durable, view)
}

func (service *ControlService) pendingAbandonRetry(view SessionView, input CandidateAbandonInput) bool {
	return view.State == StateTerminating && view.Candidate.Status == repository.CandidateAbandoned &&
		view.Candidate.ID == input.CandidateID && view.SessionEpoch == input.ExpectedSessionEpoch &&
		view.Version == input.ExpectedSessionVersion+1 &&
		view.Candidate.Version == input.ExpectedCandidateVersion+1 &&
		view.Candidate.WriterLeaseEpoch == input.ExpectedWriterLeaseEpoch+1 &&
		view.LastTransition.Reason == input.Reason
}

func (service *ControlService) completedAbandonRetry(view SessionView, input CandidateAbandonInput) bool {
	return view.State == StateTerminated && view.Candidate.Status == repository.CandidateAbandoned &&
		view.Candidate.ID == input.CandidateID && view.SessionEpoch == input.ExpectedSessionEpoch &&
		view.Version == input.ExpectedSessionVersion+2 &&
		view.Candidate.Version == input.ExpectedCandidateVersion+1 &&
		view.Candidate.WriterLeaseEpoch == input.ExpectedWriterLeaseEpoch+1
}

func (service *ControlService) cleanupAbandonedRuntime(
	ctx context.Context,
	view SessionView,
	actorID string,
) error {
	mount, exists, err := service.workspaces.ExistingMount(view.ID)
	if err != nil {
		return err
	}
	if exists {
		spec, specErr := RuntimeSpecForSession(view, mount)
		if specErr != nil {
			return specErr
		}
		if err := service.runtime.Terminate(ctx, spec); err != nil {
			return err
		}
	}
	if err := service.fenceRuntimeResources(
		ctx, view, actorID, "abandoned Candidate runtime was terminated",
	); err != nil {
		return err
	}
	if exists {
		if err := service.workspaces.Remove(view.ID); err != nil {
			return err
		}
	}
	return nil
}

// completeAbandonDeadline is the service-account recovery path for a client
// that disappeared after the atomic Candidate+Session abandonment fence. The
// persistent lifecycle lease supplies takeover/retry fencing; no user RBAC is
// re-evaluated because no new destructive decision is made here.
func (service *ControlService) completeAbandonDeadline(
	ctx context.Context,
	input SessionControlInput,
) (SessionView, error) {
	if err := service.validate(ctx, input); err != nil {
		return SessionView{}, err
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return SessionView{}, err
	}
	view := session.Snapshot()
	if view.State == StateTerminated && view.Candidate.Status == repository.CandidateAbandoned {
		return view, nil
	}
	if view.State != StateTerminating || view.Candidate.Status != repository.CandidateAbandoned ||
		view.Version != input.ExpectedSessionVersion || view.SessionEpoch != input.ExpectedSessionEpoch {
		return SessionView{}, ErrVersionConflict
	}
	record, err := service.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return SessionView{}, err
	}
	if record.Candidate.Status != repository.CandidateAbandoned ||
		!candidateProjectionMatches(view.Candidate, record.Candidate) {
		return SessionView{}, ErrSessionProjectionStale
	}
	durable, cancel := service.durableContext(ctx)
	defer cancel()
	if err := service.cleanupAbandonedRuntime(durable, view, input.ActorID); err != nil {
		return view, &LifecycleControlError{
			Session: view, Cause: errors.Join(ErrSessionReconciliation, err),
		}
	}
	terminated, err := service.sessions.CompleteCandidateAbandon(
		durable, view.ProjectID, view.ID, view.Version, view.SessionEpoch, input.ActorID,
	)
	if err != nil {
		current, loadErr := service.sessions.Get(durable, view.ProjectID, view.ID)
		if loadErr == nil && current.Snapshot().State == StateTerminated &&
			current.Snapshot().Candidate.Status == repository.CandidateAbandoned {
			return current.Snapshot(), nil
		}
		return view, &LifecycleControlError{
			Session: view, Cause: errors.Join(ErrSessionReconciliation, err, loadErr),
		}
	}
	view = terminated.Snapshot()
	service.publishState(durable, view)
	return view, nil
}

func (service *ControlService) abandonResult(
	ctx context.Context,
	view SessionView,
) (CandidateSessionResult, error) {
	record, err := service.candidates.Get(ctx, view.ProjectID, view.Candidate.ID)
	if err != nil {
		return CandidateSessionResult{Session: view}, errors.Join(ErrSessionReconciliation, err)
	}
	if view.State != StateTerminated || record.Candidate.Status != repository.CandidateAbandoned ||
		!candidateProjectionMatches(view.Candidate, record.Candidate) {
		return CandidateSessionResult{Session: view, Candidate: record.Candidate}, ErrSessionProjectionStale
	}
	return CandidateSessionResult{Session: view, Candidate: record.Candidate}, nil
}

func (service *ControlService) terminateDeadline(
	ctx context.Context,
	input TerminateSessionInput,
) (SessionView, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" || len(input.Reason) > 1000 {
		return SessionView{}, fmt.Errorf("%w: termination reason is required and bounded", ErrInvalidSession)
	}
	if err := service.validate(ctx, input.SessionControlInput); err != nil {
		return SessionView{}, err
	}
	return service.terminate(ctx, input)
}

func (service *ControlService) terminate(ctx context.Context, input TerminateSessionInput) (SessionView, error) {
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return SessionView{}, err
	}
	view := session.Snapshot()
	if service.completedTerminateRetry(view, input.SessionControlInput) {
		return view, nil
	}
	retrying := view.State == StateTerminating && view.SessionEpoch == input.ExpectedSessionEpoch &&
		view.Version == input.ExpectedSessionVersion+1 && view.LastTransition.Reason == input.Reason
	if !retrying {
		if err := session.Authorize(ActionTerminate, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
			return SessionView{}, err
		}
		transitioned, err := service.sessions.Transition(
			ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
			StateTerminating, input.ActorID, input.Reason, "",
		)
		if err != nil {
			return SessionView{}, err
		}
		view = transitioned.Snapshot()
		service.publishState(ctx, view)
	}
	durable, cancel := service.durableContext(ctx)
	defer cancel()
	mount, exists, err := service.workspaces.ExistingMount(view.ID)
	if err != nil {
		return view, err
	}
	if exists {
		spec, specErr := RuntimeSpecForSession(view, mount)
		if specErr != nil {
			return view, specErr
		}
		if err := service.runtime.Terminate(durable, spec); err != nil {
			return view, err
		}
	}
	if err := service.fenceRuntimeResources(
		durable, view, input.ActorID, "sandbox runtime was terminated",
	); err != nil {
		return view, err
	}
	if exists {
		if err := service.workspaces.Remove(view.ID); err != nil {
			return view, err
		}
	}
	terminated, err := service.transitionOrCurrent(
		durable, view, input.ActorID, StateTerminated, "runtime terminated", nil,
	)
	if err != nil {
		return view, err
	}
	service.publishState(durable, terminated)
	return terminated, nil
}

func (service *ControlService) validate(ctx context.Context, input SessionControlInput) error {
	if service == nil {
		return ErrControlUnavailable
	}
	if ctx == nil || !validUUID(input.ProjectID) || !validUUID(input.SessionID) || !validUUID(input.ActorID) ||
		input.ExpectedSessionVersion == 0 || input.ExpectedSessionEpoch == 0 {
		return ErrInvalidSession
	}
	return nil
}

func (service *ControlService) completedSuspendRetry(view SessionView, input SessionControlInput) bool {
	return view.State == StateSuspended && view.SessionEpoch == input.ExpectedSessionEpoch &&
		view.Version == input.ExpectedSessionVersion+2
}

func (service *ControlService) completedResumeRetry(view SessionView, input SessionControlInput) bool {
	if view.State != StateReady || view.SessionEpoch != input.ExpectedSessionEpoch+1 {
		return false
	}
	return view.Version >= input.ExpectedSessionVersion+3
}

func (service *ControlService) completedTerminateRetry(view SessionView, input SessionControlInput) bool {
	return view.State == StateTerminated && view.SessionEpoch == input.ExpectedSessionEpoch &&
		view.Version == input.ExpectedSessionVersion+2
}

func exactCheckpointForView(view SessionView) bool {
	checkpoint := view.LatestCheckpoint
	return checkpoint != nil && checkpoint.CandidateID == view.Candidate.ID &&
		checkpoint.CandidateVersion == view.Candidate.Version &&
		checkpoint.JournalSequence == view.Candidate.JournalSequence &&
		checkpoint.SessionEpoch == view.Candidate.SessionEpoch &&
		checkpoint.WriterLeaseEpoch == view.Candidate.WriterLeaseEpoch &&
		checkpoint.TreeHash == view.Candidate.TreeHash
}

func (service *ControlService) transitionOrCurrent(
	ctx context.Context,
	view SessionView,
	actorID string,
	target State,
	reason string,
	checkpoint *repository.CandidateSnapshot,
) (SessionView, error) {
	checkpointID := ""
	if checkpoint != nil {
		checkpointID = checkpoint.ID
	}
	transitioned, err := service.sessions.Transition(
		ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
		target, actorID, reason, checkpointID,
	)
	if err == nil {
		return transitioned.Snapshot(), nil
	}
	current, loadErr := service.sessions.Get(ctx, view.ProjectID, view.ID)
	if loadErr == nil {
		currentView := current.Snapshot()
		if currentView.State == target && currentView.SessionEpoch == view.SessionEpoch &&
			currentView.Version == view.Version+1 {
			return currentView, nil
		}
	}
	return view, errors.Join(err, loadErr)
}

func (service *ControlService) failAndCleanup(
	ctx context.Context,
	view SessionView,
	actorID string,
	spec *RuntimeSpec,
	reason string,
	cause error,
) (SessionView, error) {
	runtimeTerminated := false
	if spec != nil {
		if err := service.runtime.Terminate(ctx, *spec); err != nil {
			cause = errors.Join(cause, fmt.Errorf("clean up failed runtime: %w", err))
		} else {
			runtimeTerminated = true
		}
	}
	if runtimeTerminated {
		if err := service.fenceRuntimeResources(ctx, view, actorID, "failed sandbox runtime was terminated"); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	current, err := service.sessions.Get(ctx, view.ProjectID, view.ID)
	if err == nil {
		view = current.Snapshot()
		if view.State == StateFailed || view.State == StateTerminated {
			return view, &LifecycleControlError{Session: view, Cause: cause}
		}
	}
	failed, transitionErr := service.sessions.Transition(
		ctx, view.ProjectID, view.ID, view.Version, view.SessionEpoch,
		StateFailed, actorID, reason, "",
	)
	if transitionErr == nil {
		view = failed.Snapshot()
		service.publishState(ctx, view)
	} else {
		cause = errors.Join(cause, err, transitionErr)
	}
	return view, &LifecycleControlError{Session: view, Cause: cause}
}

func (service *ControlService) durableContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), sandboxControlTimeout)
}

func (service *ControlService) fenceRuntimeResources(
	ctx context.Context,
	view SessionView,
	actorID, reason string,
) error {
	if service.resources == nil {
		return nil
	}
	if err := service.resources.FenceEpoch(
		ctx, view.ProjectID, view.ID, view.SessionEpoch, actorID, reason,
	); err != nil {
		return fmt.Errorf("fence sandbox runtime resources: %w", err)
	}
	return nil
}

func (service *ControlService) publishState(ctx context.Context, view SessionView) {
	publishSessionState(ctx, service.events, view)
}
