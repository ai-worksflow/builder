package sandbox

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

// SandboxSession is an immutable, server-authoritative lifecycle aggregate.
// Callers can observe only cloned snapshots and can advance it only through
// methods that enforce aggregate-version and session-epoch fencing.
type SandboxSession struct {
	document sandboxSessionDocument
}

func NewSession(input NewSessionInput, now time.Time) (SandboxSession, error) {
	if !validUUID(input.ID) || !validUUID(input.ActorID) || now.IsZero() {
		return SandboxSession{}, fmt.Errorf("%w: session, actor, and timestamp are required", ErrInvalidSession)
	}
	if err := input.Candidate.Validate(); err != nil {
		return SandboxSession{}, fmt.Errorf("%w: candidate: %v", ErrInvalidSession, err)
	}
	if input.Candidate.Status != repository.CandidateActive {
		return SandboxSession{}, fmt.Errorf("%w: candidate must be active", ErrInvalidSession)
	}
	if now.Before(input.Candidate.UpdatedAt) {
		return SandboxSession{}, fmt.Errorf("%w: session cannot predate the exact candidate", ErrInvalidSession)
	}
	if !validDigest(input.RunnerImageDigest) {
		return SandboxSession{}, fmt.Errorf("%w: runner image must be pinned by canonical sha256 digest", ErrInvalidSession)
	}
	if err := input.Quota.validate(); err != nil {
		return SandboxSession{}, err
	}
	if err := input.TTL.validate(); err != nil {
		return SandboxSession{}, err
	}
	services, err := normalizeServices(input.Services)
	if err != nil {
		return SandboxSession{}, err
	}
	ports, err := normalizePorts(input.Ports, services, input.Quota.PreviewPortLimit)
	if err != nil {
		return SandboxSession{}, err
	}
	now = now.UTC()
	document := sandboxSessionDocument{
		SchemaVersion:         SessionSchemaVersion,
		ID:                    strings.TrimSpace(input.ID),
		ProjectID:             input.Candidate.ProjectID,
		ActorID:               strings.TrimSpace(input.ActorID),
		BuildManifest:         input.Candidate.BuildManifest,
		BuildContract:         input.Candidate.BuildContract,
		FullStackTemplate:     input.Candidate.FullStackTemplate,
		TemplateReleases:      templateReleaseReferences(services),
		BaseWorkspaceRevision: cloneExactRevisionReference(input.Candidate.BaseWorkspaceRevision),
		RunnerImageDigest:     strings.TrimSpace(input.RunnerImageDigest),
		Candidate:             candidateState(input.Candidate),
		SessionEpoch:          input.Candidate.SessionEpoch,
		State:                 StateProvisioning,
		Version:               1,
		TTL: TTL{
			Policy: input.TTL, IdleDeadline: now.Add(input.TTL.IdleHibernateAfter), ExpiresAt: now.Add(input.TTL.MaxRuntime),
		},
		Quota: input.Quota, AllowedServices: services, AllowedPorts: ports,
		LastTransition: Transition{To: StateProvisioning, Reason: "created", At: now},
		CreatedAt:      now, UpdatedAt: now,
	}
	if input.LatestCheckpoint != nil {
		checkpoint, err := checkpointReference(*input.LatestCheckpoint)
		if err != nil {
			return SandboxSession{}, err
		}
		if checkpoint.CandidateID != document.Candidate.ID || checkpoint.CandidateVersion != document.Candidate.Version ||
			checkpoint.JournalSequence != document.Candidate.JournalSequence ||
			checkpoint.SessionEpoch != document.Candidate.SessionEpoch ||
			checkpoint.WriterLeaseEpoch != document.Candidate.WriterLeaseEpoch || checkpoint.TreeHash != document.Candidate.TreeHash ||
			input.LatestCheckpoint.CreatedAt.Before(document.Candidate.UpdatedAt) || input.LatestCheckpoint.CreatedAt.After(now) {
			return SandboxSession{}, fmt.Errorf("%w: initial checkpoint does not bind the exact candidate", ErrCheckpointMismatch)
		}
		document.LatestCheckpoint = &checkpoint
	}
	if err := validateDocument(document); err != nil {
		return SandboxSession{}, err
	}
	return SandboxSession{document: cloneDocument(document)}, nil
}

func (session SandboxSession) Validate() error {
	return validateDocument(session.document)
}

func (session SandboxSession) Snapshot() SessionView {
	document := cloneDocument(session.document)
	return SessionView{
		SchemaVersion: document.SchemaVersion,
		ID:            document.ID, ProjectID: document.ProjectID, ActorID: document.ActorID,
		BuildManifest: document.BuildManifest, BuildContract: document.BuildContract,
		FullStackTemplate:     document.FullStackTemplate,
		TemplateReleases:      append([]repository.ExactReference(nil), document.TemplateReleases...),
		BaseWorkspaceRevision: cloneExactRevisionReference(document.BaseWorkspaceRevision),
		RunnerImageDigest:     document.RunnerImageDigest, Candidate: document.Candidate,
		LatestCheckpoint: cloneCheckpoint(document.LatestCheckpoint), SessionEpoch: document.SessionEpoch,
		State: document.State, Version: document.Version, TTL: document.TTL, Quota: document.Quota,
		AllowedServices: cloneServices(document.AllowedServices), AllowedPorts: clonePorts(document.AllowedPorts),
		AllowedActions:  append([]Action(nil), deriveAllowedActions(document)...),
		BlockingReasons: cloneBlockingReasons(deriveBlockingReasons(document)),
		LastTransition:  document.LastTransition, FailureReason: document.FailureReason,
		CreatedAt: document.CreatedAt, UpdatedAt: document.UpdatedAt,
	}
}

func (session SandboxSession) Clone() SandboxSession {
	return SandboxSession{document: cloneDocument(session.document)}
}

// BrowserDisconnected is deliberately a no-op on lifecycle state. Connection
// ownership belongs to the gateway; a dropped browser cannot terminate or
// suspend the server-side session.
func (session SandboxSession) BrowserDisconnected() (SandboxSession, error) {
	if err := session.Validate(); err != nil {
		return SandboxSession{}, err
	}
	return session.Clone(), nil
}

func (session SandboxSession) Authorize(action Action, expectedVersion, sessionEpoch uint64) error {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return err
	}
	if !knownAction(action) {
		return fmt.Errorf("%w: unknown action %q", ErrActionBlocked, action)
	}
	actions := deriveAllowedActions(session.document)
	if actionAllowed(actions, action) {
		return nil
	}
	reasons := make([]BlockingReason, 0, 2)
	for _, reason := range deriveBlockingReasons(session.document) {
		if actionAllowed(reason.Actions, action) {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, BlockingReason{
			Code: BlockingSessionNotReady, Actions: []Action{action},
			Detail: "the action is not available in the authoritative sandbox session state",
		})
	}
	return &ActionError{Action: action, Reasons: cloneBlockingReasons(reasons)}
}

func (session SandboxSession) BeginStart(expectedVersion, sessionEpoch uint64, now time.Time) (SandboxSession, error) {
	return session.advance(expectedVersion, sessionEpoch, StateProvisioning, StateStarting, "provisioned", false, now)
}

func (session SandboxSession) MarkReady(expectedVersion, sessionEpoch uint64, now time.Time) (SandboxSession, error) {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if session.document.State != StateStarting && session.document.State != StateResuming {
		return SandboxSession{}, invalidTransition(session.document.State, StateReady)
	}
	if session.document.State == StateResuming {
		if session.document.Candidate.SessionEpoch != session.document.SessionEpoch {
			return SandboxSession{}, ErrCandidateMismatch
		}
		if session.document.Candidate.Dirty && !hasExactCheckpoint(session.document) {
			return SandboxSession{}, ErrCheckpointRequired
		}
	}
	return session.advance(expectedVersion, sessionEpoch, session.document.State, StateReady, "runtime_ready", false, now)
}

func (session SandboxSession) BeginSuspend(expectedVersion, sessionEpoch uint64, now time.Time) (SandboxSession, error) {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if session.document.State != StateReady {
		return SandboxSession{}, invalidTransition(session.document.State, StateSuspending)
	}
	if session.document.Candidate.Status == repository.CandidateActive &&
		session.document.Candidate.Dirty && !hasExactCheckpoint(session.document) {
		return SandboxSession{}, ErrCheckpointRequired
	}
	return session.advance(expectedVersion, sessionEpoch, StateReady, StateSuspending, "suspend_requested", false, now)
}

func (session SandboxSession) MarkSuspended(expectedVersion, sessionEpoch uint64, now time.Time) (SandboxSession, error) {
	return session.advance(expectedVersion, sessionEpoch, StateSuspending, StateSuspended, "runtime_suspended", false, now)
}

func (session SandboxSession) BeginResume(expectedVersion, sessionEpoch uint64, now time.Time) (SandboxSession, error) {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if session.document.State != StateSuspended {
		return SandboxSession{}, invalidTransition(session.document.State, StateResuming)
	}
	return session.advance(expectedVersion, sessionEpoch, StateSuspended, StateResuming, "resume_requested", true, now)
}

// BindResumedCandidate records the Repository Service fence rotation that must
// complete before a resumed runtime can become ready. The lifecycle epoch is
// advanced first so every old gateway connection is already fenced; the exact
// Candidate is then rebound under CAS before MarkReady is allowed.
func (session SandboxSession) BindResumedCandidate(
	expectedVersion, sessionEpoch, expectedCandidateVersion uint64,
	candidate repository.CandidateWorkspace,
	checkpoint *repository.CandidateSnapshot,
	now time.Time,
) (SandboxSession, error) {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if session.document.State != StateResuming {
		return SandboxSession{}, invalidTransition(session.document.State, StateResuming)
	}
	if expectedCandidateVersion != session.document.Candidate.Version || candidate.Version <= expectedCandidateVersion {
		return SandboxSession{}, ErrCandidateVersionConflict
	}
	if err := candidate.Validate(); err != nil || candidate.Status != repository.CandidateActive {
		return SandboxSession{}, fmt.Errorf("%w: resumed Candidate is invalid or inactive", ErrCandidateMismatch)
	}
	previous := session.document.Candidate
	if candidate.ID != previous.ID || candidate.ProjectID != session.document.ProjectID ||
		candidate.RepositorySnapshotID != previous.RepositorySnapshotID || candidate.BuildManifest != session.document.BuildManifest ||
		candidate.BuildContract != session.document.BuildContract || candidate.FullStackTemplate != session.document.FullStackTemplate ||
		!optionalExactRevisionReferencesEqual(candidate.BaseWorkspaceRevision, session.document.BaseWorkspaceRevision) ||
		candidate.BaseTreeHash != previous.BaseTreeHash || candidate.CurrentTree.TreeHash != previous.TreeHash ||
		candidate.JournalSequence != previous.JournalSequence || candidate.Dirty != previous.Dirty ||
		candidate.Conflicted != previous.Conflicted || candidate.Stale != previous.Stale ||
		candidate.RebaseRequired != previous.RebaseRequired || candidate.SessionEpoch != session.document.SessionEpoch ||
		candidate.WriterLeaseEpoch <= previous.WriterLeaseEpoch || now.IsZero() || now.Before(session.document.UpdatedAt) ||
		candidate.UpdatedAt.After(now) || candidate.UpdatedAt.Before(previous.UpdatedAt) {
		return SandboxSession{}, ErrCandidateMismatch
	}

	var exactCheckpoint *CandidateCheckpointRef
	if checkpoint != nil {
		value, err := checkpointReference(*checkpoint)
		if err != nil {
			return SandboxSession{}, err
		}
		if value.CandidateID != candidate.ID || value.CandidateVersion != candidate.Version ||
			value.JournalSequence != candidate.JournalSequence || value.SessionEpoch != candidate.SessionEpoch ||
			value.WriterLeaseEpoch != candidate.WriterLeaseEpoch || value.TreeHash != candidate.CurrentTree.TreeHash ||
			checkpoint.CreatedAt.Before(candidate.UpdatedAt) || checkpoint.CreatedAt.After(now) {
			return SandboxSession{}, ErrCheckpointMismatch
		}
		exactCheckpoint = &value
	} else if candidate.Dirty {
		return SandboxSession{}, ErrCheckpointRequired
	}

	next := session.Clone()
	next.document.Candidate = candidateState(candidate)
	next.document.LatestCheckpoint = exactCheckpoint
	next.touch(now)
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	return next, nil
}

func (session SandboxSession) BeginTerminate(expectedVersion, sessionEpoch uint64, reason string, now time.Time) (SandboxSession, error) {
	return session.beginTerminate(expectedVersion, sessionEpoch, reason, now, true)
}

// beginTerminateDeadline models the operator-only absolute-TTL path. The
// Candidate tree is already durable outside the disposable runtime, so this
// exact Session transition does not require the user to hold an editor lease
// long enough to create another checkpoint.
func (session SandboxSession) beginTerminateDeadline(
	expectedVersion, sessionEpoch uint64,
	reason string,
	now time.Time,
) (SandboxSession, error) {
	return session.beginTerminate(expectedVersion, sessionEpoch, reason, now, false)
}

func (session SandboxSession) beginTerminate(
	expectedVersion, sessionEpoch uint64,
	reason string,
	now time.Time,
	requireCheckpoint bool,
) (SandboxSession, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return SandboxSession{}, fmt.Errorf("%w: termination reason is required and bounded", ErrInvalidSession)
	}
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if session.document.State != StateReady && session.document.State != StateSuspended && session.document.State != StateFailed {
		return SandboxSession{}, invalidTransition(session.document.State, StateTerminating)
	}
	if requireCheckpoint && session.document.Candidate.Status == repository.CandidateActive &&
		session.document.Candidate.Dirty && !hasExactCheckpoint(session.document) {
		return SandboxSession{}, ErrCheckpointRequired
	}
	return session.advance(expectedVersion, sessionEpoch, session.document.State, StateTerminating, reason, false, now)
}

func (session SandboxSession) Cancel(expectedVersion, sessionEpoch uint64, now time.Time) (SandboxSession, error) {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if session.document.State != StateProvisioning && session.document.State != StateStarting && session.document.State != StateResuming {
		return SandboxSession{}, invalidTransition(session.document.State, StateTerminating)
	}
	if session.document.Candidate.Dirty && !hasExactCheckpoint(session.document) {
		return SandboxSession{}, ErrCheckpointRequired
	}
	return session.advance(expectedVersion, sessionEpoch, session.document.State, StateTerminating, "cancel", false, now)
}

func (session SandboxSession) MarkTerminated(expectedVersion, sessionEpoch uint64, now time.Time) (SandboxSession, error) {
	return session.advance(expectedVersion, sessionEpoch, StateTerminating, StateTerminated, "runtime_terminated", false, now)
}

func (session SandboxSession) Fail(expectedVersion, sessionEpoch uint64, reason string, now time.Time) (SandboxSession, error) {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2000 {
		return SandboxSession{}, fmt.Errorf("%w: failure reason is required and bounded", ErrInvalidSession)
	}
	switch session.document.State {
	case StateProvisioning, StateStarting, StateReady, StateSuspending, StateResuming, StateTerminating:
	default:
		return SandboxSession{}, invalidTransition(session.document.State, StateFailed)
	}
	return session.advance(expectedVersion, sessionEpoch, session.document.State, StateFailed, reason, false, now)
}

func (session SandboxSession) RecordCheckpoint(
	expectedVersion, sessionEpoch uint64,
	snapshot repository.CandidateSnapshot,
	now time.Time,
) (SandboxSession, error) {
	if err := session.Authorize(ActionCheckpoint, expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if now.IsZero() || now.Before(session.document.UpdatedAt) || snapshot.CreatedAt.After(now) {
		return SandboxSession{}, fmt.Errorf("%w: checkpoint timestamp is invalid", ErrCheckpointMismatch)
	}
	checkpoint, err := checkpointReference(snapshot)
	if err != nil {
		return SandboxSession{}, err
	}
	candidate := session.document.Candidate
	if checkpoint.CandidateID != candidate.ID || checkpoint.CandidateVersion != candidate.Version ||
		checkpoint.JournalSequence != candidate.JournalSequence || checkpoint.SessionEpoch != candidate.SessionEpoch ||
		checkpoint.WriterLeaseEpoch != candidate.WriterLeaseEpoch || checkpoint.TreeHash != candidate.TreeHash ||
		snapshot.CreatedAt.Before(candidate.UpdatedAt) {
		return SandboxSession{}, fmt.Errorf("%w: checkpoint does not bind the current candidate version and tree", ErrCheckpointMismatch)
	}
	next := session.Clone()
	next.document.LatestCheckpoint = &checkpoint
	next.touch(now)
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	return next, nil
}

func (session SandboxSession) UpdateCandidate(
	expectedVersion, sessionEpoch, expectedCandidateVersion uint64,
	candidate repository.CandidateWorkspace,
	now time.Time,
) (SandboxSession, error) {
	if err := session.Authorize(ActionEdit, expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if expectedCandidateVersion != session.document.Candidate.Version {
		return SandboxSession{}, ErrCandidateVersionConflict
	}
	if err := candidate.Validate(); err != nil || candidate.Status != repository.CandidateActive {
		return SandboxSession{}, fmt.Errorf("%w: updated candidate is invalid or inactive", ErrCandidateMismatch)
	}
	if candidate.Version <= expectedCandidateVersion {
		return SandboxSession{}, ErrCandidateVersionConflict
	}
	if now.IsZero() || now.Before(session.document.UpdatedAt) || candidate.UpdatedAt.After(now) ||
		candidate.UpdatedAt.Before(session.document.Candidate.UpdatedAt) {
		return SandboxSession{}, fmt.Errorf("%w: candidate update timestamp is invalid", ErrCandidateMismatch)
	}
	if candidate.ID != session.document.Candidate.ID || candidate.ProjectID != session.document.ProjectID ||
		candidate.RepositorySnapshotID != session.document.Candidate.RepositorySnapshotID ||
		candidate.BuildManifest != session.document.BuildManifest || candidate.BuildContract != session.document.BuildContract ||
		candidate.FullStackTemplate != session.document.FullStackTemplate ||
		!optionalExactRevisionReferencesEqual(candidate.BaseWorkspaceRevision, session.document.BaseWorkspaceRevision) ||
		candidate.BaseTreeHash != session.document.Candidate.BaseTreeHash {
		return SandboxSession{}, ErrCandidateMismatch
	}
	if candidate.SessionEpoch != session.document.SessionEpoch {
		return SandboxSession{}, ErrEpochFenced
	}
	next := session.Clone()
	next.document.Candidate = candidateState(candidate)
	next.touch(now)
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	return next, nil
}

func (session SandboxSession) MarshalJSON() ([]byte, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(session.Snapshot())
}

func (*SandboxSession) UnmarshalJSON([]byte) error { return ErrImmutableSession }

func (session SandboxSession) guard(expectedVersion, sessionEpoch uint64) error {
	if err := session.Validate(); err != nil {
		return err
	}
	if sessionEpoch != session.document.SessionEpoch {
		return ErrEpochFenced
	}
	if expectedVersion != session.document.Version {
		return ErrVersionConflict
	}
	return nil
}

func (session SandboxSession) advance(
	expectedVersion, sessionEpoch uint64,
	from, to State,
	reason string,
	incrementEpoch bool,
	now time.Time,
) (SandboxSession, error) {
	if err := session.guard(expectedVersion, sessionEpoch); err != nil {
		return SandboxSession{}, err
	}
	if session.document.State != from {
		return SandboxSession{}, invalidTransition(session.document.State, to)
	}
	if now.IsZero() || now.Before(session.document.UpdatedAt) {
		return SandboxSession{}, fmt.Errorf("%w: transition timestamp cannot move backwards", ErrInvalidSession)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2000 {
		return SandboxSession{}, fmt.Errorf("%w: transition reason is required and bounded", ErrInvalidSession)
	}
	next := session.Clone()
	next.document.State = to
	if incrementEpoch {
		next.document.SessionEpoch++
	}
	next.document.LastTransition = Transition{From: from, To: to, Reason: reason, At: now.UTC()}
	if to == StateFailed {
		next.document.FailureReason = reason
	}
	next.touch(now)
	if err := next.Validate(); err != nil {
		return SandboxSession{}, err
	}
	return next, nil
}

func (session *SandboxSession) touch(now time.Time) {
	now = now.UTC()
	session.document.Version++
	session.document.UpdatedAt = now
	idleDeadline := now.Add(session.document.TTL.Policy.IdleHibernateAfter)
	if idleDeadline.After(session.document.TTL.ExpiresAt) {
		idleDeadline = session.document.TTL.ExpiresAt
	}
	session.document.TTL.IdleDeadline = idleDeadline
}

func validateDocument(document sandboxSessionDocument) error {
	if document.SchemaVersion != SessionSchemaVersion || !validUUID(document.ID) || !validUUID(document.ProjectID) ||
		!validUUID(document.ActorID) || document.SessionEpoch == 0 || document.Version == 0 ||
		document.CreatedAt.IsZero() || document.UpdatedAt.Before(document.CreatedAt) {
		return ErrInvalidSession
	}
	if err := validateExactReference(document.BuildManifest); err != nil {
		return err
	}
	if err := validateExactReference(document.BuildContract); err != nil {
		return err
	}
	if err := validateExactReference(document.FullStackTemplate); err != nil {
		return err
	}
	if document.BaseWorkspaceRevision != nil {
		if err := validateExactRevision(*document.BaseWorkspaceRevision); err != nil {
			return err
		}
	}
	if !validDigest(document.RunnerImageDigest) {
		return ErrInvalidSession
	}
	if err := document.Quota.validate(); err != nil {
		return err
	}
	if err := document.TTL.Policy.validate(); err != nil || document.TTL.ExpiresAt.IsZero() || document.TTL.IdleDeadline.IsZero() ||
		document.TTL.ExpiresAt != document.CreatedAt.Add(document.TTL.Policy.MaxRuntime) ||
		document.TTL.IdleDeadline.After(document.TTL.ExpiresAt) ||
		(document.UpdatedAt.Before(document.TTL.ExpiresAt) && document.TTL.IdleDeadline.Before(document.UpdatedAt)) {
		return ErrInvalidSession
	}
	normalizedServices, err := normalizeServices(document.AllowedServices)
	if err != nil || !reflect.DeepEqual(normalizedServices, document.AllowedServices) {
		return ErrInvalidSession
	}
	if !reflect.DeepEqual(templateReleaseReferences(document.AllowedServices), document.TemplateReleases) {
		return ErrInvalidSession
	}
	normalizedPorts, err := normalizePorts(document.AllowedPorts, document.AllowedServices, document.Quota.PreviewPortLimit)
	if err != nil || !reflect.DeepEqual(normalizedPorts, document.AllowedPorts) {
		return ErrInvalidSession
	}
	if !validUUID(document.Candidate.ID) || !validUUID(document.Candidate.RepositorySnapshotID) ||
		!validDigest(document.Candidate.BaseTreeHash) || !validDigest(document.Candidate.TreeHash) ||
		document.Candidate.Version == 0 || document.Candidate.SessionEpoch == 0 ||
		document.Candidate.WriterLeaseEpoch >= document.Candidate.Version ||
		document.Candidate.SessionEpoch > document.SessionEpoch || document.Candidate.UpdatedAt.IsZero() ||
		document.Candidate.UpdatedAt.After(document.UpdatedAt) {
		return ErrInvalidSession
	}
	switch document.Candidate.Status {
	case repository.CandidateActive, repository.CandidateFrozen, repository.CandidateAbandoned:
	default:
		return ErrInvalidSession
	}
	if document.LatestCheckpoint != nil {
		checkpoint := document.LatestCheckpoint
		if !validUUID(checkpoint.ID) || !validDigest(checkpoint.ContentHash) || !validUUID(checkpoint.CandidateID) ||
			!validDigest(checkpoint.TreeHash) || checkpoint.CandidateID != document.Candidate.ID ||
			checkpoint.CandidateVersion == 0 || checkpoint.CandidateVersion > document.Candidate.Version ||
			checkpoint.JournalSequence > document.Candidate.JournalSequence || checkpoint.SessionEpoch == 0 ||
			checkpoint.SessionEpoch > document.Candidate.SessionEpoch || checkpoint.WriterLeaseEpoch > document.Candidate.WriterLeaseEpoch {
			return ErrInvalidSession
		}
	}
	switch document.State {
	case StateProvisioning, StateStarting, StateReady, StateSuspending, StateSuspended, StateResuming,
		StateTerminating, StateTerminated, StateFailed:
	default:
		return ErrInvalidSession
	}
	if document.LastTransition.To != document.State || strings.TrimSpace(document.LastTransition.Reason) == "" ||
		document.LastTransition.At.IsZero() || document.LastTransition.At.After(document.UpdatedAt) {
		return ErrInvalidSession
	}
	if document.State == StateFailed && strings.TrimSpace(document.FailureReason) == "" {
		return ErrInvalidSession
	}
	return nil
}

func cloneDocument(document sandboxSessionDocument) sandboxSessionDocument {
	result := document
	result.BaseWorkspaceRevision = cloneExactRevisionReference(document.BaseWorkspaceRevision)
	result.LatestCheckpoint = cloneCheckpoint(document.LatestCheckpoint)
	result.TemplateReleases = append([]repository.ExactReference(nil), document.TemplateReleases...)
	result.AllowedServices = cloneServices(document.AllowedServices)
	result.AllowedPorts = clonePorts(document.AllowedPorts)
	return result
}

func cloneExactRevisionReference(
	reference *repository.ExactRevisionReference,
) *repository.ExactRevisionReference {
	if reference == nil {
		return nil
	}
	cloned := *reference
	return &cloned
}

func optionalExactRevisionReferencesEqual(
	left, right *repository.ExactRevisionReference,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func invalidTransition(from, to State) error {
	return fmt.Errorf("%w: cannot move from %q to %q", ErrInvalidTransition, from, to)
}
