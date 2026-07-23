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
	PatchUndoPlanSchemaVersion        = "agent-patch-undo-plan/v1"
	PatchUndoApplicationSchemaVersion = "agent-patch-undo-application/v1"
)

var (
	ErrPatchUndoInvalid        = errors.New("invalid Agent patch undo")
	ErrPatchUndoNotFound       = errors.New("Agent patch undo was not found")
	ErrPatchUndoReplay         = errors.New("Agent patch undo idempotency input changed")
	ErrPatchUndoFenced         = errors.New("Agent patch undo source fence changed")
	ErrPatchUndoReconciliation = errors.New("Agent patch undo reconciliation is pending")
)

type NewPatchUndoPlanInput struct {
	ID                               string
	OperationID                      string
	ProjectID                        string
	SandboxSessionID                 string
	CandidateID                      string
	MergeID                          string
	MergePlanContentHash             string
	MergeApplicationContentHash      string
	ExpectedSessionVersion           uint64
	ExpectedSessionEpoch             uint64
	ExpectedCandidateVersion         uint64
	ExpectedCandidateJournalSequence uint64
	ExpectedWriterLeaseEpoch         uint64
	CreatedBy                        string
}

// PatchUndoPlanRecord is persisted before restore journal rows are written.
// It binds one exact applied merge and one exact current Candidate fence.
type PatchUndoPlanRecord struct {
	SchemaVersion                    string                     `json:"schemaVersion"`
	ID                               string                     `json:"id"`
	OperationID                      string                     `json:"operationId"`
	ProjectID                        string                     `json:"projectId"`
	SandboxSessionID                 string                     `json:"sandboxSessionId"`
	CandidateID                      string                     `json:"candidateId"`
	MergeID                          string                     `json:"mergeId"`
	MergePlanContentHash             string                     `json:"mergePlanContentHash"`
	MergeApplicationContentHash      string                     `json:"mergeApplicationContentHash"`
	MergeBeforeTreeHash              string                     `json:"mergeBeforeTreeHash"`
	MergedTreeHash                   string                     `json:"mergedTreeHash"`
	CurrentTreeHash                  string                     `json:"currentTreeHash"`
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

func NewPatchUndoPlanRecord(
	input NewPatchUndoPlanInput,
	plan PlatformPatchUndoPlan,
	now time.Time,
) (PatchUndoPlanRecord, error) {
	record := PatchUndoPlanRecord{
		SchemaVersion: PatchUndoPlanSchemaVersion,
		ID:            input.ID, OperationID: input.OperationID, ProjectID: input.ProjectID,
		SandboxSessionID: input.SandboxSessionID, CandidateID: input.CandidateID,
		MergeID: input.MergeID, MergePlanContentHash: input.MergePlanContentHash,
		MergeApplicationContentHash: input.MergeApplicationContentHash,
		MergeBeforeTreeHash:         plan.MergeBeforeTreeHash, MergedTreeHash: plan.MergedTreeHash,
		CurrentTreeHash: plan.CurrentTreeHash, PlannedTreeHash: plan.PlannedTreeHash,
		ExpectedSessionVersion:           input.ExpectedSessionVersion,
		ExpectedSessionEpoch:             input.ExpectedSessionEpoch,
		ExpectedCandidateVersion:         input.ExpectedCandidateVersion,
		ExpectedCandidateJournalSequence: input.ExpectedCandidateJournalSequence,
		ExpectedWriterLeaseEpoch:         input.ExpectedWriterLeaseEpoch,
		Operations:                       cloneMergeOperations(plan.Operations),
		Conflicts:                        cloneMergeConflicts(plan.Conflicts),
		CreatedBy:                        input.CreatedBy,
		CreatedAt:                        canonicalDatabaseTime(now),
	}
	if plan.MergeID != input.MergeID {
		return PatchUndoPlanRecord{}, fmt.Errorf("%w: merge identity", ErrPatchUndoInvalid)
	}
	switch {
	case len(record.Conflicts) != 0:
		record.Disposition = PatchMergeConflicted
	case len(record.Operations) == 0:
		record.Disposition = PatchMergeNoop
	default:
		record.Disposition = PatchMergePlanned
	}
	if err := validatePatchUndoPlan(record, false); err != nil {
		return PatchUndoPlanRecord{}, err
	}
	hash, err := domain.CanonicalHash(patchUndoPlanPayload(record))
	if err != nil {
		return PatchUndoPlanRecord{}, fmt.Errorf("%w: hash plan: %v", ErrPatchUndoInvalid, err)
	}
	record.ContentHash = "sha256:" + hash
	return record, nil
}

func ParsePatchUndoPlanRecord(record PatchUndoPlanRecord) (PatchUndoPlanRecord, error) {
	if err := validatePatchUndoPlan(record, true); err != nil {
		return PatchUndoPlanRecord{}, err
	}
	expected, err := domain.CanonicalHash(patchUndoPlanPayload(record))
	if err != nil || record.ContentHash != "sha256:"+expected {
		return PatchUndoPlanRecord{}, fmt.Errorf("%w: plan content hash", ErrPatchUndoInvalid)
	}
	record.Operations = cloneMergeOperations(record.Operations)
	record.Conflicts = cloneMergeConflicts(record.Conflicts)
	return record, nil
}

func validatePatchUndoPlan(record PatchUndoPlanRecord, requireHash bool) error {
	if record.SchemaVersion != PatchUndoPlanSchemaVersion ||
		!validUUIDs(record.ID, record.ProjectID, record.SandboxSessionID, record.CandidateID, record.MergeID, record.CreatedBy) ||
		!agentOperationPattern.MatchString(record.OperationID) || record.OperationID != strings.TrimSpace(record.OperationID) ||
		!sha256Pattern.MatchString(record.MergePlanContentHash) ||
		!sha256Pattern.MatchString(record.MergeApplicationContentHash) ||
		!sha256Pattern.MatchString(record.MergeBeforeTreeHash) ||
		!sha256Pattern.MatchString(record.MergedTreeHash) ||
		!sha256Pattern.MatchString(record.CurrentTreeHash) ||
		!sha256Pattern.MatchString(record.PlannedTreeHash) ||
		record.MergeBeforeTreeHash == record.MergedTreeHash ||
		record.ExpectedSessionVersion == 0 || record.ExpectedSessionEpoch == 0 ||
		record.ExpectedCandidateVersion == 0 || record.ExpectedWriterLeaseEpoch == 0 ||
		record.ExpectedSessionVersion > maxPatchMergeBigint ||
		record.ExpectedSessionEpoch > maxPatchMergeBigint ||
		record.ExpectedCandidateVersion > maxPatchMergeBigint ||
		record.ExpectedCandidateJournalSequence > maxPatchMergeBigint ||
		record.ExpectedWriterLeaseEpoch > maxPatchMergeBigint || record.CreatedAt.IsZero() ||
		(requireHash && !sha256Pattern.MatchString(record.ContentHash)) {
		return fmt.Errorf("%w: plan identity or exact fence", ErrPatchUndoInvalid)
	}
	if len(record.Operations) > MaxPlatformPatchOperations || len(record.Conflicts) > MaxPlatformPatchOperations {
		return fmt.Errorf("%w: plan size", ErrPatchUndoInvalid)
	}
	seenOperations := make(map[string]bool, len(record.Operations))
	seenPaths := make(map[string]bool, len(record.Operations))
	for _, value := range record.Operations {
		operation, err := repository.NormalizeOperation(value)
		if err != nil || operation != value || seenOperations[operation.ID] || seenPaths[operation.Path] ||
			(operation.Kind != repository.OperationUpsert && operation.Kind != repository.OperationDelete) {
			return fmt.Errorf("%w: plan operation", ErrPatchUndoInvalid)
		}
		seenOperations[operation.ID], seenPaths[operation.Path] = true, true
	}
	if !sort.SliceIsSorted(record.Operations, func(left, right int) bool {
		return record.Operations[left].Path < record.Operations[right].Path
	}) {
		return fmt.Errorf("%w: plan operation order", ErrPatchUndoInvalid)
	}
	seenConflicts := make(map[string]bool, len(record.Conflicts))
	for _, conflict := range record.Conflicts {
		path, err := repository.NormalizePath(conflict.Path)
		if err != nil || path != conflict.Path || conflict.Reason != PatchMergeConflictConcurrentChange ||
			seenConflicts[path] || !validPatchFileState(conflict.Base) ||
			!validPatchFileState(conflict.Current) || !validPatchFileState(conflict.Proposed) ||
			conflict.Base == conflict.Proposed || conflict.Current == conflict.Base || conflict.Current == conflict.Proposed {
			return fmt.Errorf("%w: plan conflict", ErrPatchUndoInvalid)
		}
		seenConflicts[path] = true
	}
	if !sort.SliceIsSorted(record.Conflicts, func(left, right int) bool {
		return record.Conflicts[left].Path < record.Conflicts[right].Path
	}) {
		return fmt.Errorf("%w: plan conflict order", ErrPatchUndoInvalid)
	}
	switch record.Disposition {
	case PatchMergePlanned:
		if len(record.Operations) == 0 || len(record.Conflicts) != 0 || record.CurrentTreeHash == record.PlannedTreeHash {
			return fmt.Errorf("%w: planned shape", ErrPatchUndoInvalid)
		}
	case PatchMergeConflicted:
		if len(record.Operations) != 0 || len(record.Conflicts) == 0 || record.CurrentTreeHash != record.PlannedTreeHash {
			return fmt.Errorf("%w: conflict shape", ErrPatchUndoInvalid)
		}
	case PatchMergeNoop:
		if len(record.Operations) != 0 || len(record.Conflicts) != 0 || record.CurrentTreeHash != record.PlannedTreeHash {
			return fmt.Errorf("%w: no-op shape", ErrPatchUndoInvalid)
		}
	default:
		return fmt.Errorf("%w: disposition", ErrPatchUndoInvalid)
	}
	return nil
}

func patchUndoPlanPayload(record PatchUndoPlanRecord) any {
	return struct {
		SchemaVersion                    string                     `json:"schemaVersion"`
		ID                               string                     `json:"id"`
		OperationID                      string                     `json:"operationId"`
		ProjectID                        string                     `json:"projectId"`
		SandboxSessionID                 string                     `json:"sandboxSessionId"`
		CandidateID                      string                     `json:"candidateId"`
		MergeID                          string                     `json:"mergeId"`
		MergePlanContentHash             string                     `json:"mergePlanContentHash"`
		MergeApplicationContentHash      string                     `json:"mergeApplicationContentHash"`
		MergeBeforeTreeHash              string                     `json:"mergeBeforeTreeHash"`
		MergedTreeHash                   string                     `json:"mergedTreeHash"`
		CurrentTreeHash                  string                     `json:"currentTreeHash"`
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
		record.SchemaVersion, record.ID, record.OperationID, record.ProjectID,
		record.SandboxSessionID, record.CandidateID, record.MergeID,
		record.MergePlanContentHash, record.MergeApplicationContentHash,
		record.MergeBeforeTreeHash, record.MergedTreeHash, record.CurrentTreeHash,
		record.PlannedTreeHash, record.ExpectedSessionVersion, record.ExpectedSessionEpoch,
		record.ExpectedCandidateVersion, record.ExpectedCandidateJournalSequence,
		record.ExpectedWriterLeaseEpoch, record.Disposition, record.Operations,
		record.Conflicts, record.CreatedBy, record.CreatedAt,
	}
}

type PatchUndoApplication struct {
	SchemaVersion        string                     `json:"schemaVersion"`
	UndoID               string                     `json:"undoId"`
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

func NewPatchUndoApplication(
	plan PatchUndoPlanRecord,
	mutation repository.BatchMutationResult,
	actorID string,
	now time.Time,
) (PatchUndoApplication, error) {
	plan, err := ParsePatchUndoPlanRecord(plan)
	if err != nil || plan.Disposition != PatchMergePlanned || len(mutation.Entries) != len(plan.Operations) ||
		len(mutation.Entries) == 0 || actorID != plan.CreatedBy || now.IsZero() ||
		mutation.BeforeTree.TreeHash != plan.CurrentTreeHash || mutation.AfterTree.TreeHash != plan.PlannedTreeHash {
		return PatchUndoApplication{}, fmt.Errorf("%w: application source", ErrPatchUndoInvalid)
	}
	for index, entry := range mutation.Entries {
		if entry.CandidateID != plan.CandidateID || entry.ActorID != actorID || entry.Attribution != "restore" ||
			entry.Operation != plan.Operations[index] || entry.SessionEpoch != plan.ExpectedSessionEpoch ||
			entry.LeaseEpoch != plan.ExpectedWriterLeaseEpoch ||
			entry.CandidateFrom != plan.ExpectedCandidateVersion+uint64(index) ||
			entry.CandidateTo != plan.ExpectedCandidateVersion+uint64(index)+1 ||
			entry.Sequence != plan.ExpectedCandidateJournalSequence+uint64(index)+1 {
			return PatchUndoApplication{}, fmt.Errorf("%w: application journal", ErrPatchUndoInvalid)
		}
	}
	first, last := mutation.Entries[0], mutation.Entries[len(mutation.Entries)-1]
	application := PatchUndoApplication{
		SchemaVersion: PatchUndoApplicationSchemaVersion,
		UndoID:        plan.ID, PlanContentHash: plan.ContentHash,
		ProjectID: plan.ProjectID, CandidateID: plan.CandidateID,
		JournalSequenceFrom: first.Sequence, JournalSequenceTo: last.Sequence,
		CandidateVersionFrom: first.CandidateFrom, CandidateVersionTo: last.CandidateTo,
		BeforeTree: mutation.BeforeTree, AfterTree: mutation.AfterTree,
		AppliedBy: actorID, AppliedAt: canonicalDatabaseTime(now),
	}
	if err := validatePatchUndoApplication(application, false); err != nil {
		return PatchUndoApplication{}, err
	}
	hash, err := domain.CanonicalHash(patchUndoApplicationPayload(application))
	if err != nil {
		return PatchUndoApplication{}, fmt.Errorf("%w: hash application: %v", ErrPatchUndoInvalid, err)
	}
	application.ContentHash = "sha256:" + hash
	return application, nil
}

func ParsePatchUndoApplication(value PatchUndoApplication) (PatchUndoApplication, error) {
	if err := validatePatchUndoApplication(value, true); err != nil {
		return PatchUndoApplication{}, err
	}
	expected, err := domain.CanonicalHash(patchUndoApplicationPayload(value))
	if err != nil || value.ContentHash != "sha256:"+expected {
		return PatchUndoApplication{}, fmt.Errorf("%w: application content hash", ErrPatchUndoInvalid)
	}
	return value, nil
}

func validatePatchUndoApplication(value PatchUndoApplication, requireHash bool) error {
	if value.SchemaVersion != PatchUndoApplicationSchemaVersion ||
		!validUUIDs(value.UndoID, value.ProjectID, value.CandidateID, value.AppliedBy) ||
		!sha256Pattern.MatchString(value.PlanContentHash) || value.JournalSequenceFrom == 0 ||
		value.JournalSequenceTo < value.JournalSequenceFrom || value.CandidateVersionFrom == 0 ||
		value.CandidateVersionTo <= value.CandidateVersionFrom || value.AppliedAt.IsZero() ||
		value.JournalSequenceTo > maxPatchMergeBigint || value.CandidateVersionTo > maxPatchMergeBigint ||
		value.JournalSequenceTo-value.JournalSequenceFrom != value.CandidateVersionTo-value.CandidateVersionFrom-1 ||
		!validMergeTreePointer(value.BeforeTree) || !validMergeTreePointer(value.AfterTree) ||
		value.BeforeTree.OwnerID != value.CandidateID || value.AfterTree.OwnerID != value.CandidateID ||
		value.BeforeTree.TreeHash == value.AfterTree.TreeHash ||
		(requireHash && !sha256Pattern.MatchString(value.ContentHash)) {
		return fmt.Errorf("%w: application identity", ErrPatchUndoInvalid)
	}
	return nil
}

func patchUndoApplicationPayload(value PatchUndoApplication) any {
	return struct {
		SchemaVersion        string                     `json:"schemaVersion"`
		UndoID               string                     `json:"undoId"`
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
		value.SchemaVersion, value.UndoID, value.PlanContentHash, value.ProjectID,
		value.CandidateID, value.JournalSequenceFrom, value.JournalSequenceTo,
		value.CandidateVersionFrom, value.CandidateVersionTo, value.BeforeTree,
		value.AfterTree, value.AppliedBy, value.AppliedAt,
	}
}
