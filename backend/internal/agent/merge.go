package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	PatchMergePlanSchemaVersion        = "agent-patch-merge-plan/v1"
	PatchMergeApplicationSchemaVersion = "agent-patch-merge-application/v1"
	maxPatchMergeBigint                = uint64(^uint64(0) >> 1)
)

var (
	ErrPatchMergeInvalid        = errors.New("invalid Agent patch merge")
	ErrPatchMergeNotFound       = errors.New("Agent patch merge was not found")
	ErrPatchMergeReplay         = errors.New("Agent patch merge idempotency input changed")
	ErrPatchMergeFenced         = errors.New("Agent patch merge source fence changed")
	ErrPatchMergeReconciliation = errors.New("Agent patch merge reconciliation is pending")
)

type PatchMergeDisposition string

const (
	PatchMergePlanned    PatchMergeDisposition = "planned"
	PatchMergeConflicted PatchMergeDisposition = "conflicted"
	PatchMergeNoop       PatchMergeDisposition = "noop"
)

type NewPatchMergePlanInput struct {
	ID                               string
	OperationID                      string
	ProjectID                        string
	SandboxSessionID                 string
	CandidateID                      string
	AttemptID                        string
	AttemptVersion                   uint64
	PatchReference                   BlobReference
	PatchRawHash                     string
	PatchContentHash                 string
	ExpectedSessionVersion           uint64
	ExpectedSessionEpoch             uint64
	ExpectedCandidateVersion         uint64
	ExpectedCandidateJournalSequence uint64
	ExpectedWriterLeaseEpoch         uint64
	CreatedBy                        string
}

// PatchMergePlanRecord is immutable before any repository mutation occurs.
// A planned record is the durable replay source after a process or network
// failure; conflict and no-op records are complete outcomes by themselves.
type PatchMergePlanRecord struct {
	SchemaVersion                    string                     `json:"schemaVersion"`
	ID                               string                     `json:"id"`
	OperationID                      string                     `json:"operationId"`
	ProjectID                        string                     `json:"projectId"`
	SandboxSessionID                 string                     `json:"sandboxSessionId"`
	CandidateID                      string                     `json:"candidateId"`
	AttemptID                        string                     `json:"attemptId"`
	AttemptVersion                   uint64                     `json:"attemptVersion"`
	PatchReference                   BlobReference              `json:"patchReference"`
	PatchRawHash                     string                     `json:"patchRawHash"`
	PatchContentHash                 string                     `json:"patchContentHash"`
	BaseTreeHash                     string                     `json:"baseTreeHash"`
	CurrentTreeHash                  string                     `json:"currentTreeHash"`
	ProposedTreeHash                 string                     `json:"proposedTreeHash"`
	PlannedTreeHash                  string                     `json:"plannedTreeHash"`
	ExpectedSessionVersion           uint64                     `json:"expectedSessionVersion"`
	ExpectedSessionEpoch             uint64                     `json:"expectedSessionEpoch"`
	ExpectedCandidateVersion         uint64                     `json:"expectedCandidateVersion"`
	ExpectedCandidateJournalSequence uint64                     `json:"expectedCandidateJournalSequence"`
	ExpectedWriterLeaseEpoch         uint64                     `json:"expectedWriterLeaseEpoch"`
	Disposition                      PatchMergeDisposition      `json:"disposition"`
	Operations                       []repository.FileOperation `json:"operations"`
	Conflicts                        []PatchMergeConflict       `json:"conflicts"`
	ContentHash                      string                     `json:"contentHash"`
	CreatedBy                        string                     `json:"createdBy"`
	CreatedAt                        time.Time                  `json:"createdAt"`
}

func NewPatchMergePlanRecord(
	input NewPatchMergePlanInput,
	plan PlatformPatchMergePlan,
	now time.Time,
) (PatchMergePlanRecord, error) {
	record := PatchMergePlanRecord{
		SchemaVersion: PatchMergePlanSchemaVersion,
		ID:            input.ID, OperationID: input.OperationID, ProjectID: input.ProjectID,
		SandboxSessionID: input.SandboxSessionID, CandidateID: input.CandidateID,
		AttemptID: input.AttemptID, AttemptVersion: input.AttemptVersion,
		PatchReference: input.PatchReference, PatchRawHash: input.PatchRawHash,
		PatchContentHash: input.PatchContentHash,
		BaseTreeHash:     plan.BaseTreeHash, CurrentTreeHash: plan.CurrentTreeHash,
		ProposedTreeHash: plan.ProposedTreeHash, PlannedTreeHash: plan.PlannedTreeHash,
		ExpectedSessionVersion:           input.ExpectedSessionVersion,
		ExpectedSessionEpoch:             input.ExpectedSessionEpoch,
		ExpectedCandidateVersion:         input.ExpectedCandidateVersion,
		ExpectedCandidateJournalSequence: input.ExpectedCandidateJournalSequence,
		ExpectedWriterLeaseEpoch:         input.ExpectedWriterLeaseEpoch,
		Operations:                       cloneMergeOperations(plan.Operations),
		Conflicts:                        cloneMergeConflicts(plan.Conflicts),
		CreatedBy:                        input.CreatedBy, CreatedAt: now.UTC(),
	}
	switch {
	case len(record.Conflicts) != 0:
		record.Disposition = PatchMergeConflicted
	case len(record.Operations) == 0:
		record.Disposition = PatchMergeNoop
	default:
		record.Disposition = PatchMergePlanned
	}
	if err := validatePatchMergePlan(record, false); err != nil {
		return PatchMergePlanRecord{}, err
	}
	hash, err := domain.CanonicalHash(patchMergePlanPayload(record))
	if err != nil {
		return PatchMergePlanRecord{}, fmt.Errorf("%w: hash plan: %v", ErrPatchMergeInvalid, err)
	}
	record.ContentHash = "sha256:" + hash
	return record, nil
}

func ParsePatchMergePlanRecord(record PatchMergePlanRecord) (PatchMergePlanRecord, error) {
	if err := validatePatchMergePlan(record, true); err != nil {
		return PatchMergePlanRecord{}, err
	}
	expected, err := domain.CanonicalHash(patchMergePlanPayload(record))
	if err != nil || record.ContentHash != "sha256:"+expected {
		return PatchMergePlanRecord{}, fmt.Errorf("%w: plan content hash", ErrPatchMergeInvalid)
	}
	record.Operations = cloneMergeOperations(record.Operations)
	record.Conflicts = cloneMergeConflicts(record.Conflicts)
	return record, nil
}

func cloneMergeOperations(values []repository.FileOperation) []repository.FileOperation {
	cloned := make([]repository.FileOperation, len(values))
	copy(cloned, values)
	return cloned
}

func cloneMergeConflicts(values []PatchMergeConflict) []PatchMergeConflict {
	cloned := make([]PatchMergeConflict, len(values))
	copy(cloned, values)
	return cloned
}

func validatePatchMergePlan(record PatchMergePlanRecord, requireHash bool) error {
	if record.SchemaVersion != PatchMergePlanSchemaVersion ||
		!validUUIDs(record.ID, record.ProjectID, record.SandboxSessionID, record.CandidateID, record.AttemptID, record.CreatedBy) ||
		!agentOperationPattern.MatchString(record.OperationID) || record.OperationID != strings.TrimSpace(record.OperationID) ||
		record.AttemptVersion == 0 || record.ExpectedSessionVersion == 0 ||
		record.ExpectedSessionEpoch == 0 || record.ExpectedCandidateVersion == 0 ||
		record.ExpectedWriterLeaseEpoch == 0 || record.AttemptVersion > maxPatchMergeBigint ||
		record.ExpectedSessionVersion > maxPatchMergeBigint ||
		record.ExpectedSessionEpoch > maxPatchMergeBigint ||
		record.ExpectedCandidateVersion > maxPatchMergeBigint ||
		record.ExpectedCandidateJournalSequence > maxPatchMergeBigint ||
		record.ExpectedWriterLeaseEpoch > maxPatchMergeBigint || record.CreatedAt.IsZero() ||
		record.PatchReference.Store != AgentEvidenceStore || record.PatchReference.OwnerID != record.AttemptID ||
		!sha256Pattern.MatchString(record.PatchReference.ContentHash) || record.PatchReference.Ref == "" ||
		record.PatchReference.ByteSize < 1 || !sha256Pattern.MatchString(record.PatchRawHash) ||
		!sha256Pattern.MatchString(record.PatchContentHash) || !sha256Pattern.MatchString(record.BaseTreeHash) ||
		!sha256Pattern.MatchString(record.CurrentTreeHash) || !sha256Pattern.MatchString(record.ProposedTreeHash) ||
		!sha256Pattern.MatchString(record.PlannedTreeHash) || record.BaseTreeHash == record.ProposedTreeHash ||
		(requireHash && !sha256Pattern.MatchString(record.ContentHash)) {
		return fmt.Errorf("%w: plan identity or exact fence", ErrPatchMergeInvalid)
	}
	if len(record.Operations) > MaxPlatformPatchOperations || len(record.Conflicts) > MaxPlatformPatchOperations {
		return fmt.Errorf("%w: plan size", ErrPatchMergeInvalid)
	}
	seenOperations := make(map[string]bool, len(record.Operations))
	seenPaths := make(map[string]bool, len(record.Operations))
	for _, value := range record.Operations {
		operation, err := repository.NormalizeOperation(value)
		if err != nil || operation != value || seenOperations[operation.ID] || seenPaths[operation.Path] ||
			(operation.Kind != repository.OperationUpsert && operation.Kind != repository.OperationDelete) {
			return fmt.Errorf("%w: plan operation", ErrPatchMergeInvalid)
		}
		seenOperations[operation.ID], seenPaths[operation.Path] = true, true
	}
	if !sort.SliceIsSorted(record.Operations, func(left, right int) bool {
		return record.Operations[left].Path < record.Operations[right].Path
	}) {
		return fmt.Errorf("%w: plan operation order", ErrPatchMergeInvalid)
	}
	seenConflicts := make(map[string]bool, len(record.Conflicts))
	for _, conflict := range record.Conflicts {
		path, err := repository.NormalizePath(conflict.Path)
		if err != nil || path != conflict.Path || conflict.Reason != PatchMergeConflictConcurrentChange ||
			seenConflicts[path] || !validPatchFileState(conflict.Base) ||
			!validPatchFileState(conflict.Current) || !validPatchFileState(conflict.Proposed) ||
			conflict.Current == conflict.Base || conflict.Current == conflict.Proposed {
			return fmt.Errorf("%w: plan conflict", ErrPatchMergeInvalid)
		}
		seenConflicts[path] = true
	}
	if !sort.SliceIsSorted(record.Conflicts, func(left, right int) bool {
		return record.Conflicts[left].Path < record.Conflicts[right].Path
	}) {
		return fmt.Errorf("%w: plan conflict order", ErrPatchMergeInvalid)
	}
	switch record.Disposition {
	case PatchMergePlanned:
		if len(record.Operations) == 0 || len(record.Conflicts) != 0 || record.CurrentTreeHash == record.PlannedTreeHash {
			return fmt.Errorf("%w: planned shape", ErrPatchMergeInvalid)
		}
	case PatchMergeConflicted:
		if len(record.Operations) != 0 || len(record.Conflicts) == 0 || record.CurrentTreeHash != record.PlannedTreeHash {
			return fmt.Errorf("%w: conflict shape", ErrPatchMergeInvalid)
		}
	case PatchMergeNoop:
		if len(record.Operations) != 0 || len(record.Conflicts) != 0 || record.CurrentTreeHash != record.PlannedTreeHash {
			return fmt.Errorf("%w: no-op shape", ErrPatchMergeInvalid)
		}
	default:
		return fmt.Errorf("%w: disposition", ErrPatchMergeInvalid)
	}
	return nil
}

func validPatchFileState(state PatchFileState) bool {
	if !state.Exists {
		return state.ContentHash == "" && state.ByteSize == 0 && state.Mode == ""
	}
	return sha256Pattern.MatchString(state.ContentHash) && state.ByteSize >= 0 &&
		state.ByteSize <= repository.MaxFileBytes && (state.Mode == "100644" || state.Mode == "100755")
}

func patchMergePlanPayload(record PatchMergePlanRecord) any {
	copy := record
	copy.ContentHash = ""
	return struct {
		SchemaVersion                    string                     `json:"schemaVersion"`
		ID                               string                     `json:"id"`
		OperationID                      string                     `json:"operationId"`
		ProjectID                        string                     `json:"projectId"`
		SandboxSessionID                 string                     `json:"sandboxSessionId"`
		CandidateID                      string                     `json:"candidateId"`
		AttemptID                        string                     `json:"attemptId"`
		AttemptVersion                   uint64                     `json:"attemptVersion"`
		PatchReference                   BlobReference              `json:"patchReference"`
		PatchRawHash                     string                     `json:"patchRawHash"`
		PatchContentHash                 string                     `json:"patchContentHash"`
		BaseTreeHash                     string                     `json:"baseTreeHash"`
		CurrentTreeHash                  string                     `json:"currentTreeHash"`
		ProposedTreeHash                 string                     `json:"proposedTreeHash"`
		PlannedTreeHash                  string                     `json:"plannedTreeHash"`
		ExpectedSessionVersion           uint64                     `json:"expectedSessionVersion"`
		ExpectedSessionEpoch             uint64                     `json:"expectedSessionEpoch"`
		ExpectedCandidateVersion         uint64                     `json:"expectedCandidateVersion"`
		ExpectedCandidateJournalSequence uint64                     `json:"expectedCandidateJournalSequence"`
		ExpectedWriterLeaseEpoch         uint64                     `json:"expectedWriterLeaseEpoch"`
		Disposition                      PatchMergeDisposition      `json:"disposition"`
		Operations                       []repository.FileOperation `json:"operations"`
		Conflicts                        []PatchMergeConflict       `json:"conflicts"`
		CreatedBy                        string                     `json:"createdBy"`
		CreatedAt                        time.Time                  `json:"createdAt"`
	}{
		copy.SchemaVersion, copy.ID, copy.OperationID, copy.ProjectID, copy.SandboxSessionID,
		copy.CandidateID, copy.AttemptID, copy.AttemptVersion, copy.PatchReference,
		copy.PatchRawHash, copy.PatchContentHash, copy.BaseTreeHash, copy.CurrentTreeHash,
		copy.ProposedTreeHash, copy.PlannedTreeHash, copy.ExpectedSessionVersion,
		copy.ExpectedSessionEpoch, copy.ExpectedCandidateVersion,
		copy.ExpectedCandidateJournalSequence, copy.ExpectedWriterLeaseEpoch,
		copy.Disposition, copy.Operations, copy.Conflicts, copy.CreatedBy, copy.CreatedAt,
	}
}

type PatchMergeApplication struct {
	SchemaVersion        string                     `json:"schemaVersion"`
	MergeID              string                     `json:"mergeId"`
	PlanContentHash      string                     `json:"planContentHash"`
	ProjectID            string                     `json:"projectId"`
	CandidateID          string                     `json:"candidateId"`
	JournalSequenceFrom  uint64                     `json:"journalSequenceFrom"`
	JournalSequenceTo    uint64                     `json:"journalSequenceTo"`
	CandidateVersionFrom uint64                     `json:"candidateVersionFrom"`
	CandidateVersionTo   uint64                     `json:"candidateVersionTo"`
	BeforeTree           repository.TreeBlobPointer `json:"beforeTree"`
	AfterTree            repository.TreeBlobPointer `json:"afterTree"`
	ContentHash          string                     `json:"contentHash"`
	AppliedBy            string                     `json:"appliedBy"`
	AppliedAt            time.Time                  `json:"appliedAt"`
}

func NewPatchMergeApplication(
	plan PatchMergePlanRecord,
	mutation repository.BatchMutationResult,
	actorID string,
	now time.Time,
) (PatchMergeApplication, error) {
	plan, err := ParsePatchMergePlanRecord(plan)
	if err != nil || plan.Disposition != PatchMergePlanned || len(mutation.Entries) != len(plan.Operations) ||
		len(mutation.Entries) == 0 || actorID != plan.CreatedBy || now.IsZero() {
		return PatchMergeApplication{}, fmt.Errorf("%w: application source", ErrPatchMergeInvalid)
	}
	for index, entry := range mutation.Entries {
		if entry.CandidateID != plan.CandidateID || entry.ActorID != actorID || entry.Attribution != "agent" ||
			entry.Operation != plan.Operations[index] || entry.SessionEpoch != plan.ExpectedSessionEpoch ||
			entry.LeaseEpoch != plan.ExpectedWriterLeaseEpoch ||
			entry.CandidateFrom != plan.ExpectedCandidateVersion+uint64(index) ||
			entry.CandidateTo != plan.ExpectedCandidateVersion+uint64(index)+1 ||
			entry.Sequence != plan.ExpectedCandidateJournalSequence+uint64(index)+1 {
			return PatchMergeApplication{}, fmt.Errorf("%w: application journal", ErrPatchMergeInvalid)
		}
	}
	first, last := mutation.Entries[0], mutation.Entries[len(mutation.Entries)-1]
	application := PatchMergeApplication{
		SchemaVersion: PatchMergeApplicationSchemaVersion,
		MergeID:       plan.ID, PlanContentHash: plan.ContentHash,
		ProjectID: plan.ProjectID, CandidateID: plan.CandidateID,
		JournalSequenceFrom: first.Sequence, JournalSequenceTo: last.Sequence,
		CandidateVersionFrom: first.CandidateFrom, CandidateVersionTo: last.CandidateTo,
		BeforeTree: mutation.BeforeTree, AfterTree: mutation.AfterTree,
		AppliedBy: actorID, AppliedAt: now.UTC(),
	}
	if err := validatePatchMergeApplication(application, false); err != nil {
		return PatchMergeApplication{}, err
	}
	hash, err := domain.CanonicalHash(patchMergeApplicationPayload(application))
	if err != nil {
		return PatchMergeApplication{}, fmt.Errorf("%w: hash application: %v", ErrPatchMergeInvalid, err)
	}
	application.ContentHash = "sha256:" + hash
	return application, nil
}

func ParsePatchMergeApplication(value PatchMergeApplication) (PatchMergeApplication, error) {
	if err := validatePatchMergeApplication(value, true); err != nil {
		return PatchMergeApplication{}, err
	}
	expected, err := domain.CanonicalHash(patchMergeApplicationPayload(value))
	if err != nil || value.ContentHash != "sha256:"+expected {
		return PatchMergeApplication{}, fmt.Errorf("%w: application content hash", ErrPatchMergeInvalid)
	}
	return value, nil
}

func validatePatchMergeApplication(value PatchMergeApplication, requireHash bool) error {
	if value.SchemaVersion != PatchMergeApplicationSchemaVersion ||
		!validUUIDs(value.MergeID, value.ProjectID, value.CandidateID, value.AppliedBy) ||
		!sha256Pattern.MatchString(value.PlanContentHash) || value.JournalSequenceFrom == 0 ||
		value.JournalSequenceTo < value.JournalSequenceFrom || value.CandidateVersionFrom == 0 ||
		value.CandidateVersionTo <= value.CandidateVersionFrom || value.AppliedAt.IsZero() ||
		value.JournalSequenceTo > maxPatchMergeBigint || value.CandidateVersionTo > maxPatchMergeBigint ||
		value.JournalSequenceTo-value.JournalSequenceFrom != value.CandidateVersionTo-value.CandidateVersionFrom-1 ||
		!validMergeTreePointer(value.BeforeTree) || !validMergeTreePointer(value.AfterTree) ||
		value.BeforeTree.TreeHash == value.AfterTree.TreeHash ||
		(requireHash && !sha256Pattern.MatchString(value.ContentHash)) {
		return fmt.Errorf("%w: application identity", ErrPatchMergeInvalid)
	}
	return nil
}

func validMergeTreePointer(pointer repository.TreeBlobPointer) bool {
	return pointer.Store == repository.TreeContentStore && pointer.Ref != "" && validUUIDs(pointer.OwnerID) &&
		sha256Pattern.MatchString(pointer.ContentObjectHash) && sha256Pattern.MatchString(pointer.TreeHash) &&
		pointer.FileCount >= 0 && pointer.FileCount <= repository.MaxTreeFiles &&
		pointer.ByteSize >= 0 && pointer.ByteSize <= repository.MaxTreeBytes
}

func patchMergeApplicationPayload(value PatchMergeApplication) any {
	return struct {
		SchemaVersion        string                     `json:"schemaVersion"`
		MergeID              string                     `json:"mergeId"`
		PlanContentHash      string                     `json:"planContentHash"`
		ProjectID            string                     `json:"projectId"`
		CandidateID          string                     `json:"candidateId"`
		JournalSequenceFrom  uint64                     `json:"journalSequenceFrom"`
		JournalSequenceTo    uint64                     `json:"journalSequenceTo"`
		CandidateVersionFrom uint64                     `json:"candidateVersionFrom"`
		CandidateVersionTo   uint64                     `json:"candidateVersionTo"`
		BeforeTree           repository.TreeBlobPointer `json:"beforeTree"`
		AfterTree            repository.TreeBlobPointer `json:"afterTree"`
		AppliedBy            string                     `json:"appliedBy"`
		AppliedAt            time.Time                  `json:"appliedAt"`
	}{
		value.SchemaVersion, value.MergeID, value.PlanContentHash, value.ProjectID,
		value.CandidateID, value.JournalSequenceFrom, value.JournalSequenceTo,
		value.CandidateVersionFrom, value.CandidateVersionTo, value.BeforeTree,
		value.AfterTree, value.AppliedBy, value.AppliedAt,
	}
}
