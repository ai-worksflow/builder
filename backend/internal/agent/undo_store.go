package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type patchUndoPlanRow struct {
	ID                               string          `gorm:"column:id"`
	SchemaVersion                    string          `gorm:"column:schema_version"`
	OperationID                      string          `gorm:"column:operation_id"`
	ProjectID                        string          `gorm:"column:project_id"`
	SandboxSessionID                 string          `gorm:"column:sandbox_session_id"`
	CandidateID                      string          `gorm:"column:candidate_id"`
	MergeID                          string          `gorm:"column:merge_id"`
	MergePlanContentHash             string          `gorm:"column:merge_plan_content_hash"`
	MergeApplicationContentHash      string          `gorm:"column:merge_application_content_hash"`
	MergeBeforeTreeHash              string          `gorm:"column:merge_before_tree_hash"`
	MergedTreeHash                   string          `gorm:"column:merged_tree_hash"`
	CurrentTreeHash                  string          `gorm:"column:current_tree_hash"`
	PlannedTreeHash                  string          `gorm:"column:planned_tree_hash"`
	ExpectedSessionVersion           int64           `gorm:"column:expected_session_version"`
	ExpectedSessionEpoch             int64           `gorm:"column:expected_session_epoch"`
	ExpectedCandidateVersion         int64           `gorm:"column:expected_candidate_version"`
	ExpectedCandidateJournalSequence int64           `gorm:"column:expected_candidate_journal_sequence"`
	ExpectedWriterLeaseEpoch         int64           `gorm:"column:expected_writer_lease_epoch"`
	Disposition                      string          `gorm:"column:disposition"`
	Operations                       json.RawMessage `gorm:"column:operations"`
	Conflicts                        json.RawMessage `gorm:"column:conflicts"`
	ContentHash                      string          `gorm:"column:content_hash"`
	CreatedBy                        string          `gorm:"column:created_by"`
	CreatedAt                        time.Time       `gorm:"column:created_at"`
}

func (patchUndoPlanRow) TableName() string { return "agent_patch_undo_plans" }

type patchUndoApplicationRow struct {
	UndoID                string    `gorm:"column:undo_id"`
	SchemaVersion         string    `gorm:"column:schema_version"`
	PlanContentHash       string    `gorm:"column:plan_content_hash"`
	ProjectID             string    `gorm:"column:project_id"`
	CandidateID           string    `gorm:"column:candidate_id"`
	JournalSequenceFrom   int64     `gorm:"column:journal_sequence_from"`
	JournalSequenceTo     int64     `gorm:"column:journal_sequence_to"`
	CandidateVersionFrom  int64     `gorm:"column:candidate_version_from"`
	CandidateVersionTo    int64     `gorm:"column:candidate_version_to"`
	BeforeTreeStore       string    `gorm:"column:before_tree_store"`
	BeforeTreeOwnerID     string    `gorm:"column:before_tree_owner_id"`
	BeforeTreeRef         string    `gorm:"column:before_tree_ref"`
	BeforeTreeContentHash string    `gorm:"column:before_tree_content_hash"`
	BeforeTreeHash        string    `gorm:"column:before_tree_hash"`
	BeforeTreeFileCount   int       `gorm:"column:before_tree_file_count"`
	BeforeTreeByteSize    int64     `gorm:"column:before_tree_byte_size"`
	AfterTreeStore        string    `gorm:"column:after_tree_store"`
	AfterTreeOwnerID      string    `gorm:"column:after_tree_owner_id"`
	AfterTreeRef          string    `gorm:"column:after_tree_ref"`
	AfterTreeContentHash  string    `gorm:"column:after_tree_content_hash"`
	AfterTreeHash         string    `gorm:"column:after_tree_hash"`
	AfterTreeFileCount    int       `gorm:"column:after_tree_file_count"`
	AfterTreeByteSize     int64     `gorm:"column:after_tree_byte_size"`
	ContentHash           string    `gorm:"column:content_hash"`
	AppliedBy             string    `gorm:"column:applied_by"`
	AppliedAt             time.Time `gorm:"column:applied_at"`
}

func (patchUndoApplicationRow) TableName() string { return "agent_patch_undo_applications" }

func (store *PostgresStore) SavePatchUndoPlan(
	ctx context.Context,
	plan PatchUndoPlanRecord,
) (PatchUndoPlanRecord, bool, error) {
	if err := validateAgentStoreContext(ctx); err != nil {
		return PatchUndoPlanRecord{}, false, err
	}
	plan, err := ParsePatchUndoPlanRecord(plan)
	if err != nil {
		return PatchUndoPlanRecord{}, false, err
	}
	operations, err := json.Marshal(plan.Operations)
	if err != nil {
		return PatchUndoPlanRecord{}, false, agentIntegrity("encode patch undo operations", err)
	}
	conflicts, err := json.Marshal(plan.Conflicts)
	if err != nil {
		return PatchUndoPlanRecord{}, false, agentIntegrity("encode patch undo conflicts", err)
	}
	result := store.database.WithContext(ctx).Exec(`
INSERT INTO agent_patch_undo_plans (
  id, schema_version, operation_id, project_id, sandbox_session_id, candidate_id,
  merge_id, merge_plan_content_hash, merge_application_content_hash,
  merge_before_tree_hash, merged_tree_hash, current_tree_hash, planned_tree_hash,
  expected_session_version, expected_session_epoch, expected_candidate_version,
  expected_candidate_journal_sequence, expected_writer_lease_epoch,
  disposition, operations, conflicts, content_hash, created_by, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?
)
ON CONFLICT DO NOTHING
`, plan.ID, plan.SchemaVersion, plan.OperationID, plan.ProjectID, plan.SandboxSessionID,
		plan.CandidateID, plan.MergeID, plan.MergePlanContentHash,
		plan.MergeApplicationContentHash, plan.MergeBeforeTreeHash, plan.MergedTreeHash,
		plan.CurrentTreeHash, plan.PlannedTreeHash, int64(plan.ExpectedSessionVersion),
		int64(plan.ExpectedSessionEpoch), int64(plan.ExpectedCandidateVersion),
		int64(plan.ExpectedCandidateJournalSequence), int64(plan.ExpectedWriterLeaseEpoch),
		plan.Disposition, string(operations), string(conflicts), plan.ContentHash,
		plan.CreatedBy, plan.CreatedAt)
	if result.Error != nil {
		return PatchUndoPlanRecord{}, false, mapPatchUndoStoreError("save patch undo plan", result.Error)
	}
	persisted, found, err := store.FindPatchUndoPlanByOperation(
		ctx, plan.ProjectID, plan.CreatedBy, plan.OperationID,
	)
	if err != nil {
		return PatchUndoPlanRecord{}, false, err
	}
	if !found {
		return PatchUndoPlanRecord{}, false, agentIntegrity("saved patch undo plan is unavailable", nil)
	}
	if !equalJSON(persisted, plan) {
		return PatchUndoPlanRecord{}, false, ErrPatchUndoReplay
	}
	return persisted, result.RowsAffected == 0, nil
}

func (store *PostgresStore) FindPatchUndoPlanByOperation(
	ctx context.Context,
	projectID, actorID, operationID string,
) (PatchUndoPlanRecord, bool, error) {
	if err := validateIDs(ctx, projectID, actorID); err != nil ||
		!agentOperationPattern.MatchString(operationID) {
		if err != nil {
			return PatchUndoPlanRecord{}, false, err
		}
		return PatchUndoPlanRecord{}, false, ErrPatchUndoInvalid
	}
	var rows []patchUndoPlanRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND created_by = ? AND operation_id = ?", projectID, actorID, operationID,
	).Limit(1).Find(&rows)
	if result.Error != nil {
		return PatchUndoPlanRecord{}, false, mapPatchUndoStoreError("find patch undo plan", result.Error)
	}
	if len(rows) == 0 {
		return PatchUndoPlanRecord{}, false, nil
	}
	plan, err := hydratePatchUndoPlan(rows[0])
	if err != nil {
		return PatchUndoPlanRecord{}, false, agentIntegrity("hydrate patch undo plan", err)
	}
	return plan, true, nil
}

func (store *PostgresStore) GetPatchUndoPlan(
	ctx context.Context,
	projectID, undoID string,
) (PatchUndoPlanRecord, error) {
	if err := validateIDs(ctx, projectID, undoID); err != nil {
		return PatchUndoPlanRecord{}, err
	}
	var rows []patchUndoPlanRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND id = ?", projectID, undoID,
	).Limit(1).Find(&rows)
	if result.Error != nil {
		return PatchUndoPlanRecord{}, mapPatchUndoStoreError("get patch undo plan", result.Error)
	}
	if len(rows) == 0 {
		return PatchUndoPlanRecord{}, ErrPatchUndoNotFound
	}
	plan, err := hydratePatchUndoPlan(rows[0])
	if err != nil {
		return PatchUndoPlanRecord{}, agentIntegrity("hydrate patch undo plan", err)
	}
	return plan, nil
}

func (store *PostgresStore) FindAppliedPatchUndoPlan(
	ctx context.Context,
	projectID, mergeID string,
) (PatchUndoPlanRecord, bool, error) {
	if err := validateIDs(ctx, projectID, mergeID); err != nil {
		return PatchUndoPlanRecord{}, false, err
	}
	var rows []patchUndoPlanRow
	result := store.database.WithContext(ctx).
		Table("agent_patch_undo_plans AS plans").
		Select("plans.*").
		Joins("JOIN agent_patch_undo_applications AS applications ON applications.undo_id = plans.id AND applications.project_id = plans.project_id").
		Where("plans.project_id = ? AND plans.merge_id = ?", projectID, mergeID).
		Order("applications.applied_at DESC, plans.id DESC").
		Limit(1).
		Find(&rows)
	if result.Error != nil {
		return PatchUndoPlanRecord{}, false, mapPatchUndoStoreError("find applied patch undo plan", result.Error)
	}
	if len(rows) == 0 {
		return PatchUndoPlanRecord{}, false, nil
	}
	plan, err := hydratePatchUndoPlan(rows[0])
	if err != nil {
		return PatchUndoPlanRecord{}, false, agentIntegrity("hydrate applied patch undo plan", err)
	}
	return plan, true, nil
}

func hydratePatchUndoPlan(row patchUndoPlanRow) (PatchUndoPlanRecord, error) {
	if row.ExpectedSessionVersion <= 0 || row.ExpectedSessionEpoch <= 0 ||
		row.ExpectedCandidateVersion <= 0 || row.ExpectedCandidateJournalSequence < 0 ||
		row.ExpectedWriterLeaseEpoch <= 0 {
		return PatchUndoPlanRecord{}, ErrPatchUndoInvalid
	}
	var operations []repository.FileOperation
	var conflicts []PatchMergeConflict
	if err := decodeStrictJSON(row.Operations, &operations); err != nil {
		return PatchUndoPlanRecord{}, err
	}
	if err := decodeStrictJSON(row.Conflicts, &conflicts); err != nil {
		return PatchUndoPlanRecord{}, err
	}
	return ParsePatchUndoPlanRecord(PatchUndoPlanRecord{
		SchemaVersion: row.SchemaVersion, ID: row.ID, OperationID: row.OperationID,
		ProjectID: row.ProjectID, SandboxSessionID: row.SandboxSessionID,
		CandidateID: row.CandidateID, MergeID: row.MergeID,
		MergePlanContentHash:        row.MergePlanContentHash,
		MergeApplicationContentHash: row.MergeApplicationContentHash,
		MergeBeforeTreeHash:         row.MergeBeforeTreeHash, MergedTreeHash: row.MergedTreeHash,
		CurrentTreeHash: row.CurrentTreeHash, PlannedTreeHash: row.PlannedTreeHash,
		ExpectedSessionVersion:           uint64(row.ExpectedSessionVersion),
		ExpectedSessionEpoch:             uint64(row.ExpectedSessionEpoch),
		ExpectedCandidateVersion:         uint64(row.ExpectedCandidateVersion),
		ExpectedCandidateJournalSequence: uint64(row.ExpectedCandidateJournalSequence),
		ExpectedWriterLeaseEpoch:         uint64(row.ExpectedWriterLeaseEpoch),
		Disposition:                      PatchMergeDisposition(row.Disposition),
		Operations:                       operations,
		Conflicts:                        conflicts,
		ContentHash:                      row.ContentHash,
		CreatedBy:                        row.CreatedBy,
		CreatedAt:                        row.CreatedAt,
	})
}

func (store *PostgresStore) SavePatchUndoApplication(
	ctx context.Context,
	application PatchUndoApplication,
) (PatchUndoApplication, bool, error) {
	if err := validateAgentStoreContext(ctx); err != nil {
		return PatchUndoApplication{}, false, err
	}
	application, err := ParsePatchUndoApplication(application)
	if err != nil {
		return PatchUndoApplication{}, false, err
	}
	before, after := application.BeforeTree, application.AfterTree
	result := store.database.WithContext(ctx).Exec(`
INSERT INTO agent_patch_undo_applications (
  undo_id, schema_version, plan_content_hash, project_id, candidate_id,
  journal_sequence_from, journal_sequence_to, candidate_version_from, candidate_version_to,
  before_tree_store, before_tree_owner_id, before_tree_ref, before_tree_content_hash,
  before_tree_hash, before_tree_file_count, before_tree_byte_size,
  after_tree_store, after_tree_owner_id, after_tree_ref, after_tree_content_hash,
  after_tree_hash, after_tree_file_count, after_tree_byte_size,
  content_hash, applied_by, applied_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT DO NOTHING
`, application.UndoID, application.SchemaVersion, application.PlanContentHash,
		application.ProjectID, application.CandidateID, int64(application.JournalSequenceFrom),
		int64(application.JournalSequenceTo), int64(application.CandidateVersionFrom),
		int64(application.CandidateVersionTo), before.Store, before.OwnerID, before.Ref,
		before.ContentObjectHash, before.TreeHash, before.FileCount, before.ByteSize,
		after.Store, after.OwnerID, after.Ref, after.ContentObjectHash, after.TreeHash,
		after.FileCount, after.ByteSize, application.ContentHash, application.AppliedBy, application.AppliedAt)
	if result.Error != nil {
		return PatchUndoApplication{}, false, mapPatchUndoStoreError("save patch undo application", result.Error)
	}
	persisted, found, err := store.GetPatchUndoApplication(ctx, application.ProjectID, application.UndoID)
	if err != nil {
		return PatchUndoApplication{}, false, err
	}
	if !found {
		return PatchUndoApplication{}, false, agentIntegrity("saved patch undo application is unavailable", nil)
	}
	if !equalJSON(persisted, application) {
		return PatchUndoApplication{}, false, ErrPatchUndoReplay
	}
	return persisted, result.RowsAffected == 0, nil
}

func (store *PostgresStore) GetPatchUndoApplication(
	ctx context.Context,
	projectID, undoID string,
) (PatchUndoApplication, bool, error) {
	if err := validateIDs(ctx, projectID, undoID); err != nil {
		return PatchUndoApplication{}, false, err
	}
	var rows []patchUndoApplicationRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND undo_id = ?", projectID, undoID,
	).Limit(1).Find(&rows)
	if result.Error != nil {
		return PatchUndoApplication{}, false, mapPatchUndoStoreError("get patch undo application", result.Error)
	}
	if len(rows) == 0 {
		return PatchUndoApplication{}, false, nil
	}
	value, err := hydratePatchUndoApplication(rows[0])
	if err != nil {
		return PatchUndoApplication{}, false, agentIntegrity("hydrate patch undo application", err)
	}
	return value, true, nil
}

func hydratePatchUndoApplication(row patchUndoApplicationRow) (PatchUndoApplication, error) {
	if row.JournalSequenceFrom <= 0 || row.JournalSequenceTo < row.JournalSequenceFrom ||
		row.CandidateVersionFrom <= 0 || row.CandidateVersionTo <= row.CandidateVersionFrom {
		return PatchUndoApplication{}, ErrPatchUndoInvalid
	}
	return ParsePatchUndoApplication(PatchUndoApplication{
		SchemaVersion: row.SchemaVersion, UndoID: row.UndoID, PlanContentHash: row.PlanContentHash,
		ProjectID: row.ProjectID, CandidateID: row.CandidateID,
		JournalSequenceFrom: uint64(row.JournalSequenceFrom), JournalSequenceTo: uint64(row.JournalSequenceTo),
		CandidateVersionFrom: uint64(row.CandidateVersionFrom), CandidateVersionTo: uint64(row.CandidateVersionTo),
		BeforeTree: repository.TreeBlobPointer{
			Store: row.BeforeTreeStore, OwnerID: row.BeforeTreeOwnerID, Ref: row.BeforeTreeRef,
			ContentObjectHash: row.BeforeTreeContentHash, TreeHash: row.BeforeTreeHash,
			FileCount: row.BeforeTreeFileCount, ByteSize: row.BeforeTreeByteSize,
		},
		AfterTree: repository.TreeBlobPointer{
			Store: row.AfterTreeStore, OwnerID: row.AfterTreeOwnerID, Ref: row.AfterTreeRef,
			ContentObjectHash: row.AfterTreeContentHash, TreeHash: row.AfterTreeHash,
			FileCount: row.AfterTreeFileCount, ByteSize: row.AfterTreeByteSize,
		},
		ContentHash: row.ContentHash, AppliedBy: row.AppliedBy, AppliedAt: row.AppliedAt,
	})
}

func mapPatchUndoStoreError(operation string, err error) error {
	mapped := mapAgentStoreError(operation, err)
	switch {
	case errors.Is(mapped, ErrAttemptVersionConflict):
		return errors.Join(ErrPatchUndoFenced, mapped)
	case errors.Is(mapped, ErrAgentOperationReplay):
		return errors.Join(ErrPatchUndoReplay, mapped)
	case errors.Is(mapped, ErrInvalidAttempt):
		return errors.Join(ErrPatchUndoInvalid, mapped)
	default:
		return mapped
	}
}

var _ interface {
	SavePatchUndoPlan(context.Context, PatchUndoPlanRecord) (PatchUndoPlanRecord, bool, error)
	FindPatchUndoPlanByOperation(context.Context, string, string, string) (PatchUndoPlanRecord, bool, error)
	GetPatchUndoPlan(context.Context, string, string) (PatchUndoPlanRecord, error)
	FindAppliedPatchUndoPlan(context.Context, string, string) (PatchUndoPlanRecord, bool, error)
	SavePatchUndoApplication(context.Context, PatchUndoApplication) (PatchUndoApplication, bool, error)
	GetPatchUndoApplication(context.Context, string, string) (PatchUndoApplication, bool, error)
} = (*PostgresStore)(nil)
