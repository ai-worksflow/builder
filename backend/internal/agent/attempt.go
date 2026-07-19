package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type AttemptState string

const (
	AttemptPending            AttemptState = "pending"
	AttemptReady              AttemptState = "ready"
	AttemptQueued             AttemptState = "queued"
	AttemptClaimed            AttemptState = "claimed"
	AttemptRunning            AttemptState = "running"
	AttemptPatchReady         AttemptState = "patch_ready"
	AttemptValidating         AttemptState = "validating"
	AttemptReviewReady        AttemptState = "review_ready"
	AttemptVerificationFailed AttemptState = "verification_failed"
	AttemptFailed             AttemptState = "failed"
	AttemptTimedOut           AttemptState = "timed_out"
	AttemptCancelled          AttemptState = "cancelled"
	AttemptStale              AttemptState = "stale"
)

type ExecutorIdentity struct {
	Adapter           string `json:"adapter"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	RunnerImageDigest string `json:"runnerImageDigest"`
	ModelPolicyHash   string `json:"modelPolicyHash"`
	ParametersHash    string `json:"parametersHash"`
	PromptHash        string `json:"promptHash"`
	OutputSchemaHash  string `json:"outputSchemaHash"`
	ToolchainHash     string `json:"toolchainHash"`
}

type AttemptLease struct {
	WorkerID  string    `json:"workerId"`
	Epoch     uint64    `json:"epoch"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type AttemptEvidence struct {
	Patch            *BlobReference `json:"patch,omitempty"`
	StructuredResult *BlobReference `json:"structuredResult,omitempty"`
	Stdout           *BlobReference `json:"stdout,omitempty"`
	Stderr           *BlobReference `json:"stderr,omitempty"`
	Validation       *BlobReference `json:"validation,omitempty"`
}

type NewAttemptInput struct {
	ID          string
	CreatedBy   string
	Executor    ExecutorIdentity
	Parent      *AgentAttempt
	RetryReason string
}

type AgentAttempt struct {
	SchemaVersion         string                    `json:"schemaVersion"`
	ID                    string                    `json:"id"`
	ProjectID             string                    `json:"projectId"`
	SandboxSessionID      string                    `json:"sandboxSessionId"`
	CandidateID           string                    `json:"candidateId"`
	TaskCapsule           repository.ExactReference `json:"taskCapsule"`
	ContextPack           ContextPackReference      `json:"contextPack"`
	BaseCandidateTreeHash string                    `json:"baseCandidateTreeHash"`
	BuildContractHash     string                    `json:"buildContractHash"`
	TemplateReleaseHashes []string                  `json:"templateReleaseHashes"`
	Executor              ExecutorIdentity          `json:"executor"`
	RequestKeyHash        string                    `json:"requestKeyHash"`
	ConfigurationHash     string                    `json:"configurationHash"`
	ParentAttemptID       string                    `json:"parentAttemptId,omitempty"`
	RetryReason           string                    `json:"retryReason,omitempty"`
	State                 AttemptState              `json:"state"`
	Version               uint64                    `json:"version"`
	FenceEpoch            uint64                    `json:"fenceEpoch"`
	Lease                 *AttemptLease             `json:"lease,omitempty"`
	Evidence              AttemptEvidence           `json:"evidence"`
	ExitReason            string                    `json:"exitReason,omitempty"`
	CreatedBy             string                    `json:"createdBy"`
	CreatedAt             time.Time                 `json:"createdAt"`
	StartedAt             *time.Time                `json:"startedAt,omitempty"`
	FinishedAt            *time.Time                `json:"finishedAt,omitempty"`
	UpdatedAt             time.Time                 `json:"updatedAt"`
}

type AttemptEventKind string

const (
	EventLifecycleAdvanced AttemptEventKind = "lifecycle.advanced"
	EventLeaseClaimed      AttemptEventKind = "lease.claimed"
	EventLeaseReclaimed    AttemptEventKind = "lease.reclaimed"
	EventLeaseRenewed      AttemptEventKind = "lease.renewed"
	EventControlCancelled  AttemptEventKind = "control.cancelled"
	EventControlStale      AttemptEventKind = "control.stale"
)

type AttemptEvent struct {
	SchemaVersion string           `json:"schemaVersion"`
	AttemptID     string           `json:"attemptId"`
	Sequence      uint64           `json:"sequence"`
	VersionFrom   uint64           `json:"versionFrom"`
	VersionTo     uint64           `json:"versionTo"`
	StateFrom     AttemptState     `json:"stateFrom"`
	StateTo       AttemptState     `json:"stateTo"`
	FenceFrom     uint64           `json:"fenceEpochFrom"`
	FenceTo       uint64           `json:"fenceEpochTo"`
	Kind          AttemptEventKind `json:"kind"`
	ActorID       string           `json:"actorId"`
	WorkerID      string           `json:"workerId,omitempty"`
	Reason        string           `json:"reason"`
	Lease         *AttemptLease    `json:"lease,omitempty"`
	Evidence      AttemptEvidence  `json:"evidence"`
	ExitReason    string           `json:"exitReason,omitempty"`
	CreatedAt     time.Time        `json:"createdAt"`
}

type AdvanceAttemptInput struct {
	ExpectedVersion    uint64
	ExpectedFenceEpoch uint64
	ActorID            string
	WorkerID           string
	Target             AttemptState
	Reason             string
	Evidence           AttemptEvidence
	ExitReason         string
}

func NewAttempt(
	input NewAttemptInput,
	capsule TaskCapsule,
	contextPack ContextPack,
	now time.Time,
) (AgentAttempt, error) {
	parsedCapsule, err := ParseTaskCapsule(capsule, contextPack)
	if err != nil {
		return AgentAttempt{}, fmt.Errorf("%w: TaskCapsule: %v", ErrInvalidAttempt, err)
	}
	if !validUUIDs(input.ID, input.CreatedBy) || now.IsZero() {
		return AgentAttempt{}, fmt.Errorf("%w: identity, actor, or time", ErrInvalidAttempt)
	}
	executor, err := normalizeExecutor(input.Executor)
	if err != nil {
		return AgentAttempt{}, err
	}
	if executor.OutputSchemaHash != parsedCapsule.OutputSchemaHash {
		return AgentAttempt{}, fmt.Errorf("%w: executor output schema does not match the exact TaskCapsule", ErrInvalidAttempt)
	}
	retryReason := strings.TrimSpace(input.RetryReason)
	parentID := ""
	if input.Parent == nil {
		if retryReason != "" {
			return AgentAttempt{}, fmt.Errorf("%w: retry reason requires a parent Attempt", ErrInvalidAttempt)
		}
	} else {
		parent := *input.Parent
		if !validUUIDs(parent.ID) || parent.TaskCapsule.ID != parsedCapsule.ID ||
			parent.TaskCapsule.ContentHash != parsedCapsule.ContentHash || !retryableState(parent.State) ||
			retryReason == "" || len(retryReason) > 1000 || parent.Executor != executor {
			return AgentAttempt{}, fmt.Errorf("%w: retry must preserve the exact finished Attempt request and include a reason", ErrInvalidAttempt)
		}
		parentID = parent.ID
	}
	templateHashes := make([]string, len(parsedCapsule.TemplateReleases))
	for index, release := range parsedCapsule.TemplateReleases {
		templateHashes[index] = release.ContentHash
	}
	attempt := AgentAttempt{
		SchemaVersion: AttemptSchemaVersion, ID: input.ID,
		ProjectID: parsedCapsule.ProjectID, SandboxSessionID: parsedCapsule.SandboxSessionID,
		CandidateID: parsedCapsule.CandidateID,
		TaskCapsule: repository.ExactReference{ID: parsedCapsule.ID, ContentHash: parsedCapsule.ContentHash},
		ContextPack: parsedCapsule.ContextPack, BaseCandidateTreeHash: parsedCapsule.BaseCandidateTreeHash,
		BuildContractHash:     parsedCapsule.BuildContract.ContentHash,
		TemplateReleaseHashes: templateHashes, Executor: executor,
		ParentAttemptID: parentID, RetryReason: retryReason,
		State: AttemptPending, Version: 1, Evidence: AttemptEvidence{},
		CreatedBy: input.CreatedBy, CreatedAt: now.UTC().Truncate(time.Microsecond),
		UpdatedAt: now.UTC().Truncate(time.Microsecond),
	}
	attempt.RequestKeyHash, err = semanticHash(attemptRequestKeyPayload(attempt))
	if err != nil {
		return AgentAttempt{}, err
	}
	attempt.ConfigurationHash, err = semanticHash(attemptConfigurationPayload(attempt))
	if err != nil {
		return AgentAttempt{}, err
	}
	if input.Parent != nil && (input.Parent.RequestKeyHash != attempt.RequestKeyHash ||
		input.Parent.ConfigurationHash != attempt.ConfigurationHash) {
		return AgentAttempt{}, fmt.Errorf("%w: changed model, prompt, context, schema, runner, or toolchain requires supersede, not retry", ErrInvalidAttempt)
	}
	return attempt, nil
}

// ParseAttempt validates an authoritative persistence projection before it is
// exposed to a transport or used as the parent of another transition. The
// immutable request hashes are recomputed; runtime fields are checked against
// the state/lease/evidence invariants that the append-only event projection is
// expected to preserve.
func ParseAttempt(attempt AgentAttempt) (AgentAttempt, error) {
	if attempt.SchemaVersion != AttemptSchemaVersion ||
		!validUUIDs(
			attempt.ID,
			attempt.ProjectID,
			attempt.SandboxSessionID,
			attempt.CandidateID,
			attempt.TaskCapsule.ID,
			attempt.ContextPack.ID,
			attempt.CreatedBy,
		) ||
		!sha256Pattern.MatchString(attempt.TaskCapsule.ContentHash) ||
		!sha256Pattern.MatchString(attempt.ContextPack.ContentHash) ||
		!sha256Pattern.MatchString(attempt.BaseCandidateTreeHash) ||
		!exactHashPattern.MatchString(attempt.BuildContractHash) ||
		attempt.Version == 0 || attempt.CreatedAt.IsZero() || attempt.UpdatedAt.IsZero() ||
		attempt.CreatedAt.After(attempt.UpdatedAt) ||
		!canonicalTimestamp(attempt.CreatedAt) || !canonicalTimestamp(attempt.UpdatedAt) {
		return AgentAttempt{}, fmt.Errorf("%w: identity, exact references, version, or timestamps", ErrInvalidAttempt)
	}

	executor, err := normalizeExecutor(attempt.Executor)
	if err != nil || executor != attempt.Executor {
		if err != nil {
			return AgentAttempt{}, err
		}
		return AgentAttempt{}, fmt.Errorf("%w: executor is not canonical", ErrInvalidAttempt)
	}
	if len(attempt.TemplateReleaseHashes) == 0 || len(attempt.TemplateReleaseHashes) > 16 {
		return AgentAttempt{}, fmt.Errorf("%w: exact TemplateRelease hashes", ErrInvalidAttempt)
	}
	for _, hash := range attempt.TemplateReleaseHashes {
		if !sha256Pattern.MatchString(hash) {
			return AgentAttempt{}, fmt.Errorf("%w: exact TemplateRelease hash", ErrInvalidAttempt)
		}
	}

	parentID := strings.TrimSpace(attempt.ParentAttemptID)
	retryReason := strings.TrimSpace(attempt.RetryReason)
	if parentID == "" && retryReason != "" || parentID != "" &&
		(!validUUIDs(parentID) || retryReason == "" || len(retryReason) > 1000) ||
		parentID != attempt.ParentAttemptID || retryReason != attempt.RetryReason {
		return AgentAttempt{}, fmt.Errorf("%w: retry lineage", ErrInvalidAttempt)
	}

	if !knownAttemptState(attempt.State) || attempt.FenceEpoch > attempt.Version-1 {
		return AgentAttempt{}, fmt.Errorf("%w: state or fence epoch", ErrInvalidAttempt)
	}
	if attempt.Lease != nil {
		workerID, workerErr := normalizeStableValue(attempt.Lease.WorkerID, 160)
		if workerErr != nil || workerID != attempt.Lease.WorkerID ||
			!workerState(attempt.State) || attempt.FenceEpoch == 0 ||
			attempt.Lease.Epoch != attempt.FenceEpoch ||
			attempt.Lease.ExpiresAt.IsZero() || !canonicalTimestamp(attempt.Lease.ExpiresAt) ||
			!attempt.Lease.ExpiresAt.After(attempt.UpdatedAt) {
			return AgentAttempt{}, fmt.Errorf("%w: worker lease projection", ErrInvalidAttempt)
		}
	} else if workerState(attempt.State) {
		return AgentAttempt{}, fmt.Errorf("%w: worker state is missing its lease", ErrInvalidAttempt)
	}
	if !workerState(attempt.State) && !finalState(attempt.State) && attempt.FenceEpoch != 0 {
		return AgentAttempt{}, fmt.Errorf("%w: pre-worker state has a fence", ErrInvalidAttempt)
	}

	evidence, err := mergeEvidence(AttemptEvidence{}, attempt.Evidence)
	if err != nil || !equalJSON(evidence, attempt.Evidence) {
		if err != nil {
			return AgentAttempt{}, err
		}
		return AgentAttempt{}, fmt.Errorf("%w: evidence is not canonical", ErrInvalidAttempt)
	}
	if stateRequiresPatch(attempt.State) &&
		(attempt.Evidence.Patch == nil || attempt.Evidence.StructuredResult == nil) {
		return AgentAttempt{}, fmt.Errorf("%w: state requires platform Patch evidence", ErrInvalidAttempt)
	}
	if attempt.State == AttemptReviewReady && attempt.Evidence.Validation == nil {
		return AgentAttempt{}, fmt.Errorf("%w: review-ready state requires validation evidence", ErrInvalidAttempt)
	}

	if attempt.StartedAt != nil &&
		(!canonicalTimestamp(*attempt.StartedAt) || attempt.StartedAt.Before(attempt.CreatedAt) ||
			attempt.StartedAt.After(attempt.UpdatedAt)) {
		return AgentAttempt{}, fmt.Errorf("%w: start timestamp", ErrInvalidAttempt)
	}
	if attempt.FinishedAt != nil &&
		(!canonicalTimestamp(*attempt.FinishedAt) || attempt.FinishedAt.Before(attempt.CreatedAt) ||
			attempt.FinishedAt.After(attempt.UpdatedAt) ||
			attempt.StartedAt != nil && attempt.FinishedAt.Before(*attempt.StartedAt)) {
		return AgentAttempt{}, fmt.Errorf("%w: finish timestamp", ErrInvalidAttempt)
	}
	exitReason := strings.TrimSpace(attempt.ExitReason)
	if finalState(attempt.State) {
		if attempt.FinishedAt == nil || attempt.Lease != nil ||
			attempt.State == AttemptReviewReady && exitReason != "" ||
			attempt.State != AttemptReviewReady && (exitReason == "" || len(exitReason) > 2000) {
			return AgentAttempt{}, fmt.Errorf("%w: terminal projection", ErrInvalidAttempt)
		}
	} else if attempt.FinishedAt != nil || exitReason != "" {
		return AgentAttempt{}, fmt.Errorf("%w: non-terminal projection has terminal facts", ErrInvalidAttempt)
	}
	if exitReason != attempt.ExitReason {
		return AgentAttempt{}, fmt.Errorf("%w: exit reason is not canonical", ErrInvalidAttempt)
	}
	if attempt.State == AttemptPending &&
		(attempt.Version != 1 || attempt.FenceEpoch != 0 || attempt.Lease != nil ||
			attempt.StartedAt != nil || attempt.FinishedAt != nil || !emptyEvidence(attempt.Evidence)) {
		return AgentAttempt{}, fmt.Errorf("%w: initial projection", ErrInvalidAttempt)
	}

	requestHash, err := semanticHash(attemptRequestKeyPayload(attempt))
	if err != nil {
		return AgentAttempt{}, err
	}
	configurationHash, err := semanticHash(attemptConfigurationPayload(attempt))
	if err != nil {
		return AgentAttempt{}, err
	}
	if attempt.RequestKeyHash != requestHash || attempt.ConfigurationHash != configurationHash {
		return AgentAttempt{}, fmt.Errorf("%w: immutable request or configuration hash", ErrInvalidAttempt)
	}
	return cloneAttempt(attempt), nil
}

func (attempt AgentAttempt) Claim(
	expectedVersion uint64,
	actorID, workerID string,
	ttl time.Duration,
	now time.Time,
) (AgentAttempt, AttemptEvent, error) {
	if err := attempt.validateControl(expectedVersion, actorID, now); err != nil {
		return AgentAttempt{}, AttemptEvent{}, err
	}
	workerID, err := normalizeStableValue(workerID, 160)
	if err != nil || ttl < time.Second || ttl > 10*time.Minute || ttl%time.Second != 0 {
		return AgentAttempt{}, AttemptEvent{}, fmt.Errorf("%w: worker identity or bounded lease TTL", ErrInvalidAttempt)
	}
	firstClaim := attempt.State == AttemptQueued && attempt.Lease == nil
	takeover := workerState(attempt.State) && attempt.Lease != nil && !now.Before(attempt.Lease.ExpiresAt)
	if !firstClaim && !takeover {
		return AgentAttempt{}, AttemptEvent{}, ErrAttemptLease
	}
	next := cloneAttempt(attempt)
	next.Version++
	next.FenceEpoch++
	next.Lease = &AttemptLease{
		WorkerID: workerID, Epoch: next.FenceEpoch,
		ExpiresAt: now.UTC().Truncate(time.Microsecond).Add(ttl),
	}
	if firstClaim {
		next.State = AttemptClaimed
	}
	next.UpdatedAt = now.UTC().Truncate(time.Microsecond)
	kind := EventLeaseClaimed
	if takeover {
		kind = EventLeaseReclaimed
	}
	event := attemptEvent(attempt, next, kind, actorID, workerID, "worker lease claimed", next.Evidence, "", now)
	return next, event, nil
}

func (attempt AgentAttempt) Renew(
	expectedVersion, expectedFenceEpoch uint64,
	actorID, workerID string,
	ttl time.Duration,
	now time.Time,
) (AgentAttempt, AttemptEvent, error) {
	if err := attempt.validateWorker(expectedVersion, expectedFenceEpoch, actorID, workerID, now); err != nil {
		return AgentAttempt{}, AttemptEvent{}, err
	}
	if ttl < time.Second || ttl > 10*time.Minute || ttl%time.Second != 0 {
		return AgentAttempt{}, AttemptEvent{}, fmt.Errorf("%w: lease TTL", ErrInvalidAttempt)
	}
	next := cloneAttempt(attempt)
	next.Version++
	next.Lease.ExpiresAt = now.UTC().Truncate(time.Microsecond).Add(ttl)
	next.UpdatedAt = now.UTC().Truncate(time.Microsecond)
	event := attemptEvent(attempt, next, EventLeaseRenewed, actorID, workerID, "worker lease renewed", next.Evidence, "", now)
	return next, event, nil
}

func (attempt AgentAttempt) Advance(input AdvanceAttemptInput, now time.Time) (AgentAttempt, AttemptEvent, error) {
	if err := attempt.validateControl(input.ExpectedVersion, input.ActorID, now); err != nil {
		return AgentAttempt{}, AttemptEvent{}, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || len(reason) > 2000 || !validTransition(attempt.State, input.Target) {
		return AgentAttempt{}, AttemptEvent{}, ErrAttemptState
	}
	workerID := strings.TrimSpace(input.WorkerID)
	if workerTransition(attempt.State, input.Target) {
		if err := attempt.validateWorker(
			input.ExpectedVersion, input.ExpectedFenceEpoch, input.ActorID, workerID, now,
		); err != nil {
			return AgentAttempt{}, AttemptEvent{}, err
		}
	} else if input.ExpectedFenceEpoch != attempt.FenceEpoch || workerID != "" {
		return AgentAttempt{}, AttemptEvent{}, ErrAttemptFenced
	}
	evidence, err := mergeEvidence(attempt.Evidence, input.Evidence)
	if err != nil {
		return AgentAttempt{}, AttemptEvent{}, err
	}
	if input.Target == AttemptPatchReady && (evidence.Patch == nil || evidence.StructuredResult == nil) {
		return AgentAttempt{}, AttemptEvent{}, fmt.Errorf("%w: patch_ready requires platform Patch and structured result evidence", ErrAttemptState)
	}
	if input.Target == AttemptReviewReady && evidence.Validation == nil {
		return AgentAttempt{}, AttemptEvent{}, fmt.Errorf("%w: review_ready requires validation evidence", ErrAttemptState)
	}
	exitReason := strings.TrimSpace(input.ExitReason)
	if finalState(input.Target) && input.Target != AttemptReviewReady && exitReason == "" {
		return AgentAttempt{}, AttemptEvent{}, fmt.Errorf("%w: terminal Attempt requires an exit reason", ErrAttemptState)
	}
	if !finalState(input.Target) && exitReason != "" {
		return AgentAttempt{}, AttemptEvent{}, fmt.Errorf("%w: non-terminal Attempt cannot set exit reason", ErrAttemptState)
	}
	next := cloneAttempt(attempt)
	next.State = input.Target
	next.Version++
	next.Evidence = evidence
	next.ExitReason = exitReason
	now = now.UTC().Truncate(time.Microsecond)
	if input.Target == AttemptRunning && next.StartedAt == nil {
		started := now
		next.StartedAt = &started
	}
	if finalState(input.Target) {
		finished := now
		next.FinishedAt = &finished
		next.Lease = nil
	}
	next.UpdatedAt = now
	event := attemptEvent(
		attempt, next, EventLifecycleAdvanced, input.ActorID, workerID, reason, evidence, exitReason, now,
	)
	return next, event, nil
}

func (attempt AgentAttempt) Cancel(
	expectedVersion uint64,
	actorID, reason string,
	now time.Time,
) (AgentAttempt, AttemptEvent, error) {
	return attempt.controlTerminal(expectedVersion, actorID, reason, AttemptCancelled, EventControlCancelled, now)
}

func (attempt AgentAttempt) MarkStale(
	expectedVersion uint64,
	actorID, reason string,
	now time.Time,
) (AgentAttempt, AttemptEvent, error) {
	return attempt.controlTerminal(expectedVersion, actorID, reason, AttemptStale, EventControlStale, now)
}

func (attempt AgentAttempt) controlTerminal(
	expectedVersion uint64,
	actorID, reason string,
	target AttemptState,
	kind AttemptEventKind,
	now time.Time,
) (AgentAttempt, AttemptEvent, error) {
	if err := attempt.validateControl(expectedVersion, actorID, now); err != nil {
		return AgentAttempt{}, AttemptEvent{}, err
	}
	if finalState(attempt.State) {
		return AgentAttempt{}, AttemptEvent{}, ErrAttemptState
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2000 {
		return AgentAttempt{}, AttemptEvent{}, fmt.Errorf("%w: control reason", ErrInvalidAttempt)
	}
	next := cloneAttempt(attempt)
	next.State = target
	next.Version++
	next.FenceEpoch++
	next.Lease = nil
	next.ExitReason = reason
	now = now.UTC().Truncate(time.Microsecond)
	finished := now
	next.FinishedAt = &finished
	next.UpdatedAt = now
	event := attemptEvent(attempt, next, kind, actorID, "", reason, next.Evidence, reason, now)
	return next, event, nil
}

func (attempt AgentAttempt) validateControl(expectedVersion uint64, actorID string, now time.Time) error {
	if attempt.SchemaVersion != AttemptSchemaVersion || !validUUIDs(attempt.ID, attempt.ProjectID, attempt.SandboxSessionID,
		attempt.CandidateID, attempt.CreatedBy, actorID) || expectedVersion == 0 || attempt.Version != expectedVersion ||
		now.IsZero() || now.Before(attempt.UpdatedAt) || finalState(attempt.State) {
		return ErrAttemptState
	}
	return nil
}

func (attempt AgentAttempt) validateWorker(
	expectedVersion, expectedFenceEpoch uint64,
	actorID, workerID string,
	now time.Time,
) error {
	if err := attempt.validateControl(expectedVersion, actorID, now); err != nil {
		return err
	}
	if attempt.Lease == nil || attempt.FenceEpoch == 0 || attempt.FenceEpoch != expectedFenceEpoch ||
		attempt.Lease.Epoch != expectedFenceEpoch || attempt.Lease.WorkerID != strings.TrimSpace(workerID) ||
		!now.Before(attempt.Lease.ExpiresAt) {
		return ErrAttemptFenced
	}
	return nil
}

func normalizeExecutor(value ExecutorIdentity) (ExecutorIdentity, error) {
	var err error
	value.Adapter, err = normalizeStableValue(value.Adapter, 80)
	if err != nil {
		return ExecutorIdentity{}, fmt.Errorf("%w: executor adapter", ErrInvalidAttempt)
	}
	value.Provider, err = normalizeStableValue(value.Provider, 80)
	if err != nil {
		return ExecutorIdentity{}, fmt.Errorf("%w: executor provider", ErrInvalidAttempt)
	}
	value.Model = strings.TrimSpace(value.Model)
	if value.Model == "" || len(value.Model) > 160 || strings.ContainsAny(value.Model, "\r\n\x00") {
		return ExecutorIdentity{}, fmt.Errorf("%w: executor model", ErrInvalidAttempt)
	}
	for _, hash := range []string{
		value.RunnerImageDigest, value.ModelPolicyHash, value.ParametersHash,
		value.PromptHash, value.OutputSchemaHash, value.ToolchainHash,
	} {
		if !sha256Pattern.MatchString(hash) {
			return ExecutorIdentity{}, fmt.Errorf("%w: executor hashes must be canonical sha256", ErrInvalidAttempt)
		}
	}
	return value, nil
}

func mergeEvidence(current, input AttemptEvidence) (AttemptEvidence, error) {
	result := cloneEvidence(current)
	for _, pair := range []struct {
		current **BlobReference
		input   *BlobReference
	}{
		{&result.Patch, input.Patch}, {&result.StructuredResult, input.StructuredResult},
		{&result.Stdout, input.Stdout}, {&result.Stderr, input.Stderr},
		{&result.Validation, input.Validation},
	} {
		if pair.input == nil {
			continue
		}
		if err := pair.input.validate(); err != nil {
			return AttemptEvidence{}, fmt.Errorf("%w: evidence: %v", ErrInvalidAttempt, err)
		}
		if *pair.current != nil && **pair.current != *pair.input {
			return AttemptEvidence{}, fmt.Errorf("%w: immutable evidence cannot be replaced", ErrAttemptState)
		}
		copyValue := *pair.input
		*pair.current = &copyValue
	}
	return result, nil
}

func attemptEvent(
	before, after AgentAttempt,
	kind AttemptEventKind,
	actorID, workerID, reason string,
	evidence AttemptEvidence,
	exitReason string,
	now time.Time,
) AttemptEvent {
	var lease *AttemptLease
	if after.Lease != nil {
		copyValue := *after.Lease
		lease = &copyValue
	}
	return AttemptEvent{
		SchemaVersion: AttemptEventSchema, AttemptID: before.ID, Sequence: before.Version,
		VersionFrom: before.Version, VersionTo: after.Version,
		StateFrom: before.State, StateTo: after.State,
		FenceFrom: before.FenceEpoch, FenceTo: after.FenceEpoch,
		Kind: kind, ActorID: actorID, WorkerID: workerID, Reason: strings.TrimSpace(reason),
		Lease: lease, Evidence: cloneEvidence(evidence), ExitReason: exitReason,
		CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
}

func cloneAttempt(value AgentAttempt) AgentAttempt {
	result := value
	result.TemplateReleaseHashes = append([]string(nil), value.TemplateReleaseHashes...)
	result.Evidence = cloneEvidence(value.Evidence)
	if value.Lease != nil {
		lease := *value.Lease
		result.Lease = &lease
	}
	if value.StartedAt != nil {
		started := *value.StartedAt
		result.StartedAt = &started
	}
	if value.FinishedAt != nil {
		finished := *value.FinishedAt
		result.FinishedAt = &finished
	}
	return result
}

func cloneEvidence(value AttemptEvidence) AttemptEvidence {
	result := value
	for _, pair := range []struct {
		source *BlobReference
		target **BlobReference
	}{
		{value.Patch, &result.Patch}, {value.StructuredResult, &result.StructuredResult},
		{value.Stdout, &result.Stdout}, {value.Stderr, &result.Stderr},
		{value.Validation, &result.Validation},
	} {
		if pair.source != nil {
			copyValue := *pair.source
			*pair.target = &copyValue
		}
	}
	return result
}

func validTransition(from, to AttemptState) bool {
	switch from {
	case AttemptPending:
		return to == AttemptReady
	case AttemptReady:
		return to == AttemptQueued
	case AttemptClaimed:
		return to == AttemptRunning || to == AttemptFailed || to == AttemptTimedOut
	case AttemptRunning:
		return to == AttemptPatchReady || to == AttemptFailed || to == AttemptTimedOut
	case AttemptPatchReady:
		return to == AttemptValidating || to == AttemptFailed
	case AttemptValidating:
		return to == AttemptReviewReady || to == AttemptVerificationFailed || to == AttemptFailed || to == AttemptTimedOut
	default:
		return false
	}
}

func workerTransition(from, to AttemptState) bool {
	return from == AttemptClaimed || from == AttemptRunning || from == AttemptPatchReady || from == AttemptValidating
}

func workerState(state AttemptState) bool {
	return state == AttemptClaimed || state == AttemptRunning || state == AttemptPatchReady || state == AttemptValidating
}

func finalState(state AttemptState) bool {
	return state == AttemptReviewReady || state == AttemptVerificationFailed || state == AttemptFailed ||
		state == AttemptTimedOut || state == AttemptCancelled || state == AttemptStale
}

func retryableState(state AttemptState) bool {
	return state == AttemptVerificationFailed || state == AttemptFailed || state == AttemptTimedOut || state == AttemptCancelled
}

func knownAttemptState(state AttemptState) bool {
	switch state {
	case AttemptPending, AttemptReady, AttemptQueued, AttemptClaimed, AttemptRunning,
		AttemptPatchReady, AttemptValidating, AttemptReviewReady, AttemptVerificationFailed,
		AttemptFailed, AttemptTimedOut, AttemptCancelled, AttemptStale:
		return true
	default:
		return false
	}
}

func stateRequiresPatch(state AttemptState) bool {
	return state == AttemptPatchReady || state == AttemptValidating ||
		state == AttemptReviewReady || state == AttemptVerificationFailed
}

func emptyEvidence(value AttemptEvidence) bool {
	return value.Patch == nil && value.StructuredResult == nil && value.Stdout == nil &&
		value.Stderr == nil && value.Validation == nil
}

func canonicalTimestamp(value time.Time) bool {
	return value.Location() == time.UTC && value.Equal(value.UTC().Truncate(time.Microsecond))
}

func attemptRequestKeyPayload(attempt AgentAttempt) any {
	return struct {
		ProjectID             string                    `json:"projectId"`
		BaseCandidateTreeHash string                    `json:"baseCandidateTreeHash"`
		BuildContractHash     string                    `json:"buildContractHash"`
		TemplateReleaseHashes []string                  `json:"templateReleaseHashes"`
		TaskCapsule           repository.ExactReference `json:"taskCapsule"`
		ContextPack           ContextPackReference      `json:"contextPack"`
		Executor              ExecutorIdentity          `json:"executor"`
	}{
		attempt.ProjectID, attempt.BaseCandidateTreeHash, attempt.BuildContractHash,
		attempt.TemplateReleaseHashes, attempt.TaskCapsule, attempt.ContextPack, attempt.Executor,
	}
}

func attemptConfigurationPayload(attempt AgentAttempt) any {
	return struct {
		SandboxSessionID string `json:"sandboxSessionId"`
		CandidateID      string `json:"candidateId"`
		RequestKeyHash   string `json:"requestKeyHash"`
	}{attempt.SandboxSessionID, attempt.CandidateID, attempt.RequestKeyHash}
}
