package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/worksflow/builder/backend/internal/repository"
)

type patchMergePlanRow struct {
	ID                               string          `gorm:"column:id"`
	SchemaVersion                    string          `gorm:"column:schema_version"`
	OperationID                      string          `gorm:"column:operation_id"`
	ProjectID                        string          `gorm:"column:project_id"`
	SandboxSessionID                 string          `gorm:"column:sandbox_session_id"`
	CandidateID                      string          `gorm:"column:candidate_id"`
	AttemptID                        string          `gorm:"column:attempt_id"`
	AttemptVersion                   int64           `gorm:"column:attempt_version"`
	PatchReference                   json.RawMessage `gorm:"column:patch_reference"`
	PatchRawHash                     string          `gorm:"column:patch_raw_hash"`
	PatchContentHash                 string          `gorm:"column:patch_content_hash"`
	BaseTreeHash                     string          `gorm:"column:base_tree_hash"`
	CurrentTreeHash                  string          `gorm:"column:current_tree_hash"`
	ProposedTreeHash                 string          `gorm:"column:proposed_tree_hash"`
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

func (patchMergePlanRow) TableName() string { return "agent_patch_merge_plans" }

type patchMergeApplicationRow struct {
	MergeID               string    `gorm:"column:merge_id"`
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

func (patchMergeApplicationRow) TableName() string { return "agent_patch_merge_applications" }

func (store *PostgresStore) SavePatchMergePlan(
	ctx context.Context,
	plan PatchMergePlanRecord,
) (PatchMergePlanRecord, bool, error) {
	if err := validateAgentStoreContext(ctx); err != nil {
		return PatchMergePlanRecord{}, false, err
	}
	plan, err := ParsePatchMergePlanRecord(plan)
	if err != nil {
		return PatchMergePlanRecord{}, false, err
	}
	patchReference, err := json.Marshal(plan.PatchReference)
	if err != nil {
		return PatchMergePlanRecord{}, false, agentIntegrity("encode patch merge evidence reference", err)
	}
	operations, err := json.Marshal(plan.Operations)
	if err != nil {
		return PatchMergePlanRecord{}, false, agentIntegrity("encode patch merge operations", err)
	}
	conflicts, err := json.Marshal(plan.Conflicts)
	if err != nil {
		return PatchMergePlanRecord{}, false, agentIntegrity("encode patch merge conflicts", err)
	}
	result := store.database.WithContext(ctx).Exec(`
INSERT INTO agent_patch_merge_plans (
  id, schema_version, operation_id, project_id, sandbox_session_id, candidate_id,
  attempt_id, attempt_version, patch_reference, patch_raw_hash, patch_content_hash,
  base_tree_hash, current_tree_hash, proposed_tree_hash, planned_tree_hash,
  expected_session_version, expected_session_epoch, expected_candidate_version,
  expected_candidate_journal_sequence, expected_writer_lease_epoch,
  disposition, operations, conflicts, content_hash, created_by, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?
)
ON CONFLICT DO NOTHING
`, plan.ID, plan.SchemaVersion, plan.OperationID, plan.ProjectID, plan.SandboxSessionID,
		plan.CandidateID, plan.AttemptID, int64(plan.AttemptVersion), string(patchReference),
		plan.PatchRawHash, plan.PatchContentHash, plan.BaseTreeHash, plan.CurrentTreeHash,
		plan.ProposedTreeHash, plan.PlannedTreeHash, int64(plan.ExpectedSessionVersion),
		int64(plan.ExpectedSessionEpoch), int64(plan.ExpectedCandidateVersion),
		int64(plan.ExpectedCandidateJournalSequence), int64(plan.ExpectedWriterLeaseEpoch),
		plan.Disposition, string(operations), string(conflicts), plan.ContentHash, plan.CreatedBy, plan.CreatedAt)
	if result.Error != nil {
		return PatchMergePlanRecord{}, false, mapPatchMergeStoreError("save patch merge plan", result.Error)
	}
	persisted, found, err := store.FindPatchMergePlanByOperation(ctx, plan.ProjectID, plan.CreatedBy, plan.OperationID)
	if err != nil {
		return PatchMergePlanRecord{}, false, err
	}
	if !found {
		return PatchMergePlanRecord{}, false, agentIntegrity("saved patch merge plan is unavailable", nil)
	}
	if !equalJSON(persisted, plan) {
		return PatchMergePlanRecord{}, false, ErrPatchMergeReplay
	}
	return persisted, result.RowsAffected == 0, nil
}

func (store *PostgresStore) FindPatchMergePlanByOperation(
	ctx context.Context,
	projectID, actorID, operationID string,
) (PatchMergePlanRecord, bool, error) {
	if err := validateIDs(ctx, projectID, actorID); err != nil ||
		!agentOperationPattern.MatchString(operationID) {
		if err != nil {
			return PatchMergePlanRecord{}, false, err
		}
		return PatchMergePlanRecord{}, false, ErrPatchMergeInvalid
	}
	var rows []patchMergePlanRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND created_by = ? AND operation_id = ?", projectID, actorID, operationID,
	).Limit(1).Find(&rows)
	if result.Error != nil {
		return PatchMergePlanRecord{}, false, mapPatchMergeStoreError("find patch merge plan", result.Error)
	}
	if len(rows) == 0 {
		return PatchMergePlanRecord{}, false, nil
	}
	plan, err := hydratePatchMergePlan(rows[0])
	if err != nil {
		return PatchMergePlanRecord{}, false, agentIntegrity("hydrate patch merge plan", err)
	}
	return plan, true, nil
}

func (store *PostgresStore) GetPatchMergePlan(
	ctx context.Context,
	projectID, mergeID string,
) (PatchMergePlanRecord, error) {
	if err := validateIDs(ctx, projectID, mergeID); err != nil {
		return PatchMergePlanRecord{}, err
	}
	var rows []patchMergePlanRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND id = ?", projectID, mergeID,
	).Limit(1).Find(&rows)
	if result.Error != nil {
		return PatchMergePlanRecord{}, mapPatchMergeStoreError("get patch merge plan", result.Error)
	}
	if len(rows) == 0 {
		return PatchMergePlanRecord{}, ErrPatchMergeNotFound
	}
	plan, err := hydratePatchMergePlan(rows[0])
	if err != nil {
		return PatchMergePlanRecord{}, agentIntegrity("hydrate patch merge plan", err)
	}
	return plan, nil
}

func (store *PostgresStore) ListPatchMergePlans(
	ctx context.Context,
	projectID, attemptID string,
	limit int,
) ([]PatchMergePlanRecord, error) {
	if err := validateIDs(ctx, projectID, attemptID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, ErrPatchMergeInvalid
	}
	var rows []patchMergePlanRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND attempt_id = ?", projectID, attemptID,
	).Order("created_at DESC, id DESC").Limit(limit).Find(&rows)
	if result.Error != nil {
		return nil, mapPatchMergeStoreError("list patch merge plans", result.Error)
	}
	plans := make([]PatchMergePlanRecord, len(rows))
	for index, row := range rows {
		plan, err := hydratePatchMergePlan(row)
		if err != nil {
			return nil, agentIntegrity("hydrate patch merge plan list", err)
		}
		plans[index] = plan
	}
	return plans, nil
}

// ResolvePatchMergeProject returns only the project scope needed to authorize
// a merge-scoped request before any merge plan or application payload is read.
func (store *PostgresStore) ResolvePatchMergeProject(
	ctx context.Context,
	mergeID string,
) (string, error) {
	if err := validateIDs(ctx, mergeID); err != nil {
		return "", err
	}
	var rows []struct {
		ProjectID string `gorm:"column:project_id"`
	}
	result := store.database.WithContext(ctx).Table("agent_patch_merge_plans").
		Select("project_id").Where("id = ?", mergeID).Limit(1).Find(&rows)
	if result.Error != nil {
		return "", mapPatchMergeStoreError("resolve patch merge project", result.Error)
	}
	if len(rows) == 0 {
		return "", ErrPatchMergeNotFound
	}
	if !validUUIDs(rows[0].ProjectID) {
		return "", agentIntegrity("resolve patch merge project", ErrPatchMergeInvalid)
	}
	return rows[0].ProjectID, nil
}

func hydratePatchMergePlan(row patchMergePlanRow) (PatchMergePlanRecord, error) {
	if row.AttemptVersion <= 0 || row.ExpectedSessionVersion <= 0 || row.ExpectedSessionEpoch <= 0 ||
		row.ExpectedCandidateVersion <= 0 || row.ExpectedCandidateJournalSequence < 0 ||
		row.ExpectedWriterLeaseEpoch <= 0 {
		return PatchMergePlanRecord{}, ErrPatchMergeInvalid
	}
	var reference BlobReference
	var operations []repository.FileOperation
	var conflicts []PatchMergeConflict
	if err := decodeStrictJSON(row.PatchReference, &reference); err != nil {
		return PatchMergePlanRecord{}, err
	}
	if err := decodeStrictJSON(row.Operations, &operations); err != nil {
		return PatchMergePlanRecord{}, err
	}
	if err := decodeStrictJSON(row.Conflicts, &conflicts); err != nil {
		return PatchMergePlanRecord{}, err
	}
	return ParsePatchMergePlanRecord(PatchMergePlanRecord{
		SchemaVersion: row.SchemaVersion, ID: row.ID, OperationID: row.OperationID,
		ProjectID: row.ProjectID, SandboxSessionID: row.SandboxSessionID,
		CandidateID: row.CandidateID, AttemptID: row.AttemptID,
		AttemptVersion: uint64(row.AttemptVersion), PatchReference: reference,
		PatchRawHash: row.PatchRawHash, PatchContentHash: row.PatchContentHash,
		BaseTreeHash: row.BaseTreeHash, CurrentTreeHash: row.CurrentTreeHash,
		ProposedTreeHash: row.ProposedTreeHash, PlannedTreeHash: row.PlannedTreeHash,
		ExpectedSessionVersion:           uint64(row.ExpectedSessionVersion),
		ExpectedSessionEpoch:             uint64(row.ExpectedSessionEpoch),
		ExpectedCandidateVersion:         uint64(row.ExpectedCandidateVersion),
		ExpectedCandidateJournalSequence: uint64(row.ExpectedCandidateJournalSequence),
		ExpectedWriterLeaseEpoch:         uint64(row.ExpectedWriterLeaseEpoch),
		Disposition:                      PatchMergeDisposition(row.Disposition), Operations: operations, Conflicts: conflicts,
		ContentHash: row.ContentHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
	})
}

func (store *PostgresStore) SavePatchMergeApplication(
	ctx context.Context,
	application PatchMergeApplication,
) (PatchMergeApplication, bool, error) {
	if err := validateAgentStoreContext(ctx); err != nil {
		return PatchMergeApplication{}, false, err
	}
	application, err := ParsePatchMergeApplication(application)
	if err != nil {
		return PatchMergeApplication{}, false, err
	}
	before, after := application.BeforeTree, application.AfterTree
	result := store.database.WithContext(ctx).Exec(`
INSERT INTO agent_patch_merge_applications (
  merge_id, schema_version, plan_content_hash, project_id, candidate_id,
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
`, application.MergeID, application.SchemaVersion, application.PlanContentHash,
		application.ProjectID, application.CandidateID, int64(application.JournalSequenceFrom),
		int64(application.JournalSequenceTo), int64(application.CandidateVersionFrom),
		int64(application.CandidateVersionTo), before.Store, before.OwnerID, before.Ref,
		before.ContentObjectHash, before.TreeHash, before.FileCount, before.ByteSize,
		after.Store, after.OwnerID, after.Ref, after.ContentObjectHash, after.TreeHash,
		after.FileCount, after.ByteSize, application.ContentHash, application.AppliedBy, application.AppliedAt)
	if result.Error != nil {
		return PatchMergeApplication{}, false, mapPatchMergeStoreError("save patch merge application", result.Error)
	}
	persisted, found, err := store.GetPatchMergeApplication(ctx, application.ProjectID, application.MergeID)
	if err != nil {
		return PatchMergeApplication{}, false, err
	}
	if !found {
		return PatchMergeApplication{}, false, agentIntegrity("saved patch merge application is unavailable", nil)
	}
	if !equalJSON(persisted, application) {
		return PatchMergeApplication{}, false, ErrPatchMergeReplay
	}
	return persisted, result.RowsAffected == 0, nil
}

func (store *PostgresStore) GetPatchMergeApplication(
	ctx context.Context,
	projectID, mergeID string,
) (PatchMergeApplication, bool, error) {
	if err := validateIDs(ctx, projectID, mergeID); err != nil {
		return PatchMergeApplication{}, false, err
	}
	var rows []patchMergeApplicationRow
	result := store.database.WithContext(ctx).Where(
		"project_id = ? AND merge_id = ?", projectID, mergeID,
	).Limit(1).Find(&rows)
	if result.Error != nil {
		return PatchMergeApplication{}, false, mapPatchMergeStoreError("get patch merge application", result.Error)
	}
	if len(rows) == 0 {
		return PatchMergeApplication{}, false, nil
	}
	value, err := hydratePatchMergeApplication(rows[0])
	if err != nil {
		return PatchMergeApplication{}, false, agentIntegrity("hydrate patch merge application", err)
	}
	return value, true, nil
}

func hydratePatchMergeApplication(row patchMergeApplicationRow) (PatchMergeApplication, error) {
	if row.JournalSequenceFrom <= 0 || row.JournalSequenceTo < row.JournalSequenceFrom ||
		row.CandidateVersionFrom <= 0 || row.CandidateVersionTo <= row.CandidateVersionFrom {
		return PatchMergeApplication{}, ErrPatchMergeInvalid
	}
	return ParsePatchMergeApplication(PatchMergeApplication{
		SchemaVersion: row.SchemaVersion, MergeID: row.MergeID, PlanContentHash: row.PlanContentHash,
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

func mapPatchMergeStoreError(operation string, err error) error {
	mapped := mapAgentStoreError(operation, err)
	switch {
	case errors.Is(mapped, ErrAttemptVersionConflict):
		return errors.Join(ErrPatchMergeFenced, mapped)
	case errors.Is(mapped, ErrAgentOperationReplay):
		return errors.Join(ErrPatchMergeReplay, mapped)
	case errors.Is(mapped, ErrInvalidAttempt):
		return errors.Join(ErrPatchMergeInvalid, mapped)
	default:
		return mapped
	}
}

var _ interface {
	SavePatchMergePlan(context.Context, PatchMergePlanRecord) (PatchMergePlanRecord, bool, error)
	FindPatchMergePlanByOperation(context.Context, string, string, string) (PatchMergePlanRecord, bool, error)
	GetPatchMergePlan(context.Context, string, string) (PatchMergePlanRecord, error)
	ListPatchMergePlans(context.Context, string, string, int) ([]PatchMergePlanRecord, error)
	SavePatchMergeApplication(context.Context, PatchMergeApplication) (PatchMergeApplication, bool, error)
	GetPatchMergeApplication(context.Context, string, string) (PatchMergeApplication, bool, error)
} = (*PostgresStore)(nil)
