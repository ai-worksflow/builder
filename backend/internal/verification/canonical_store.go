package verification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"gorm.io/gorm"
)

const (
	CanonicalPlanSchemaVersion = "canonical-verification-plan/v1"
	CanonicalRunSchemaVersion  = "canonical-verification-run/v1"

	canonicalPlanAggregateType    = "canonical_verification_plan"
	canonicalReceiptAggregateType = "canonical_verification_receipt"
)

var (
	ErrCanonicalPlanNotFound    = errors.New("canonical verification plan was not found")
	ErrCanonicalPlanConflict    = errors.New("canonical verification plan conflicts with committed content")
	ErrCanonicalRunNotFound     = errors.New("canonical verification run was not found")
	ErrCanonicalReceiptNotFound = errors.New("canonical verification receipt was not found")
)

type CanonicalPlan struct {
	ID        string               `json:"id"`
	Content   CanonicalPlanContent `json:"content"`
	PlanHash  string               `json:"planHash"`
	CreatedBy string               `json:"createdBy"`
	CreatedAt time.Time            `json:"createdAt"`
	Replayed  bool                 `json:"replayed"`
}

type canonicalPlanRow struct {
	ID                         string          `gorm:"column:id"`
	SchemaVersion              string          `gorm:"column:schema_version"`
	Scope                      string          `gorm:"column:scope"`
	ProjectID                  string          `gorm:"column:project_id"`
	WorkspaceArtifactID        string          `gorm:"column:workspace_artifact_id"`
	WorkspaceRevisionID        string          `gorm:"column:workspace_revision_id"`
	WorkspaceContentHash       string          `gorm:"column:workspace_content_hash"`
	BuildManifestID            string          `gorm:"column:build_manifest_id"`
	BuildManifestHash          string          `gorm:"column:build_manifest_hash"`
	BuildContractID            string          `gorm:"column:build_contract_id"`
	BuildContractHash          string          `gorm:"column:build_contract_hash"`
	FullStackTemplateID        string          `gorm:"column:full_stack_template_id"`
	FullStackTemplateHash      string          `gorm:"column:full_stack_template_hash"`
	VerificationProfileID      string          `gorm:"column:verification_profile_id"`
	VerificationProfileVersion int64           `gorm:"column:verification_profile_version"`
	VerificationProfileHash    string          `gorm:"column:verification_profile_hash"`
	TemplateReleases           json.RawMessage `gorm:"column:template_releases"`
	Obligations                json.RawMessage `gorm:"column:obligations"`
	CheckIDs                   json.RawMessage `gorm:"column:check_ids"`
	RequiredCheckIDs           json.RawMessage `gorm:"column:required_check_ids"`
	CheckCount                 int             `gorm:"column:check_count"`
	ObligationCount            int             `gorm:"column:obligation_count"`
	RuntimePolicyHash          string          `gorm:"column:runtime_policy_hash"`
	ContentStore               string          `gorm:"column:content_store"`
	ContentRef                 string          `gorm:"column:content_ref"`
	ContentHash                string          `gorm:"column:content_hash"`
	PlanHash                   string          `gorm:"column:plan_hash"`
	CreatedBy                  string          `gorm:"column:created_by"`
	CreatedAt                  time.Time       `gorm:"column:created_at"`
}

func (canonicalPlanRow) TableName() string { return "canonical_verification_plans" }

func (store *PostgresStore) SaveCanonicalPlan(
	ctx context.Context,
	planID string,
	createdBy string,
	compiled CompiledCanonicalPlan,
) (CanonicalPlan, error) {
	if store == nil || store.database == nil || store.contents == nil || ctx == nil {
		return CanonicalPlan{}, ErrReceiptStoreDown
	}
	if !validUUIDs(planID, createdBy) {
		return CanonicalPlan{}, planInvalid("Canonical Plan identity or actor")
	}
	parsed, err := ParseCanonicalPlan(compiled.Content, compiled.PlanHash)
	if err != nil {
		return CanonicalPlan{}, err
	}
	payload, err := domain.CanonicalJSON(parsed.Content)
	if err != nil {
		return CanonicalPlan{}, receiptIntegrity("encode Canonical VerificationPlan", err)
	}
	contentRef, err := store.contents.PutPending(
		ctx, parsed.Content.ProjectID, canonicalPlanAggregateType, planID, 1, payload,
	)
	if err != nil {
		return CanonicalPlan{}, fmt.Errorf("put pending Canonical VerificationPlan content: %w", err)
	}
	if contentRef.ContentHash != parsed.PlanHash {
		_ = store.contents.Abort(context.Background(), contentRef.ID)
		return CanonicalPlan{}, receiptIntegrity("Canonical VerificationPlan content hash mismatch", nil)
	}
	abort := true
	defer func() {
		if abort {
			_ = store.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	projections, err := encodeCanonicalPlanProjections(parsed.Content)
	if err != nil {
		return CanonicalPlan{}, err
	}
	var persisted canonicalPlanRow
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Exec(`
INSERT INTO canonical_verification_plans (
  id, schema_version, scope, project_id,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  build_manifest_id, build_manifest_hash, build_contract_id, build_contract_hash,
  full_stack_template_id, full_stack_template_hash,
  verification_profile_id, verification_profile_version, verification_profile_hash,
  template_releases, obligations, check_ids, required_check_ids,
  check_count, obligation_count, runtime_policy_hash,
  content_store, content_ref, content_hash, plan_hash, created_by
) VALUES (
  ?, 'canonical-verification-plan/v1', 'canonical', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?::jsonb, ?::jsonb, ?::jsonb, ?::jsonb, ?, ?, ?, 'mongo', ?, ?, ?, ?
)
ON CONFLICT DO NOTHING
`, planID, parsed.Content.ProjectID,
			parsed.Content.Subject.WorkspaceArtifactID, parsed.Content.Subject.WorkspaceRevisionID,
			parsed.Content.Subject.WorkspaceContentHash,
			parsed.Content.BuildManifest.ID, parsed.Content.BuildManifest.ContentHash,
			parsed.Content.BuildContract.ID, parsed.Content.BuildContract.ContentHash,
			parsed.Content.FullStackTemplate.ID, parsed.Content.FullStackTemplate.ContentHash,
			parsed.Content.Profile.ID, int64(parsed.Content.Profile.Version), parsed.Content.Profile.ContentHash,
			projections.templateReleases, projections.obligations, projections.checkIDs,
			projections.requiredCheckIDs, len(parsed.Content.Checks), len(parsed.Content.Obligations),
			projections.runtimePolicyHash, contentRef.ID, contentRef.ContentHash, parsed.PlanHash, createdBy)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		query := transaction.Where("id = ?", planID)
		if replayed {
			query = transaction.Where(
				"workspace_revision_id = ? AND workspace_content_hash = ? AND verification_profile_id = ? AND verification_profile_version = ? AND verification_profile_hash = ? AND runtime_policy_hash = ? AND plan_hash = ?",
				parsed.Content.Subject.WorkspaceRevisionID, parsed.Content.Subject.WorkspaceContentHash,
				parsed.Content.Profile.ID, int64(parsed.Content.Profile.Version), parsed.Content.Profile.ContentHash,
				projections.runtimePolicyHash, parsed.PlanHash,
			)
		}
		if err := query.Take(&persisted).Error; errors.Is(err, gorm.ErrRecordNotFound) && replayed {
			return ErrCanonicalPlanConflict
		} else if err != nil {
			return err
		}
		if persisted.PlanHash != parsed.PlanHash || persisted.ProjectID != parsed.Content.ProjectID {
			return ErrCanonicalPlanConflict
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CanonicalPlan{}, mapReceiptStoreError("save Canonical VerificationPlan", err)
	}
	if replayed {
		_ = store.contents.Abort(context.Background(), contentRef.ID)
		abort = false
		plan, loadErr := store.hydrateCanonicalPlan(ctx, persisted)
		plan.Replayed = loadErr == nil
		return plan, loadErr
	}
	abort = false
	if err := store.contents.Finalize(ctx, contentRef.ID); err != nil {
		return CanonicalPlan{}, fmt.Errorf("%w: finalize Canonical VerificationPlan content: %v", core.ErrContentNotReady, err)
	}
	return store.GetCanonicalPlan(ctx, persisted.ProjectID, persisted.ID)
}

func (store *PostgresStore) GetCanonicalPlan(
	ctx context.Context,
	projectID string,
	planID string,
) (CanonicalPlan, error) {
	if !validUUIDs(projectID, planID) {
		return CanonicalPlan{}, planInvalid("Canonical Plan project or identity")
	}
	var row canonicalPlanRow
	err := store.database.WithContext(ctx).Where("id = ? AND project_id = ?", planID, projectID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CanonicalPlan{}, ErrCanonicalPlanNotFound
	}
	if err != nil {
		return CanonicalPlan{}, mapReceiptStoreError("load Canonical VerificationPlan", err)
	}
	return store.hydrateCanonicalPlan(ctx, row)
}

func (store *PostgresStore) hydrateCanonicalPlan(ctx context.Context, row canonicalPlanRow) (CanonicalPlan, error) {
	stored, err := store.contents.Get(ctx, row.ContentRef, row.ContentHash)
	if err != nil {
		return CanonicalPlan{}, receiptIntegrity("load Canonical VerificationPlan content", err)
	}
	if stored.ProjectID != row.ProjectID || stored.AggregateType != canonicalPlanAggregateType ||
		stored.AggregateID != row.ID || stored.SchemaVersion != 1 || stored.State != content.StateFinalized {
		return CanonicalPlan{}, receiptIntegrity("Canonical VerificationPlan content identity", nil)
	}
	var document CanonicalPlanContent
	if err := json.Unmarshal(stored.Payload, &document); err != nil {
		return CanonicalPlan{}, receiptIntegrity("decode Canonical VerificationPlan", err)
	}
	parsed, err := ParseCanonicalPlan(document, row.PlanHash)
	if err != nil || !canonicalPlanMatchesRow(parsed.Content, row) {
		return CanonicalPlan{}, receiptIntegrity("Canonical VerificationPlan projection mismatch", err)
	}
	return CanonicalPlan{
		ID: row.ID, Content: parsed.Content, PlanHash: parsed.PlanHash,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.UTC(),
	}, nil
}

func encodeCanonicalPlanProjections(document CanonicalPlanContent) (encodedPlanProjections, error) {
	candidateShape := PlanContent{
		TemplateReleases: document.TemplateReleases,
		Checks:           document.Checks, Obligations: document.Obligations, RuntimePolicy: document.RuntimePolicy,
	}
	return encodePlanProjections(candidateShape)
}

func canonicalPlanMatchesRow(document CanonicalPlanContent, row canonicalPlanRow) bool {
	projections, err := encodeCanonicalPlanProjections(document)
	if err != nil {
		return false
	}
	return row.SchemaVersion == CanonicalPlanSchemaVersion && row.Scope == string(ScopeCanonical) &&
		row.ProjectID == document.ProjectID &&
		row.WorkspaceArtifactID == document.Subject.WorkspaceArtifactID &&
		row.WorkspaceRevisionID == document.Subject.WorkspaceRevisionID &&
		row.WorkspaceContentHash == document.Subject.WorkspaceContentHash &&
		row.BuildManifestID == document.BuildManifest.ID && row.BuildManifestHash == document.BuildManifest.ContentHash &&
		row.BuildContractID == document.BuildContract.ID && row.BuildContractHash == document.BuildContract.ContentHash &&
		row.FullStackTemplateID == document.FullStackTemplate.ID && row.FullStackTemplateHash == document.FullStackTemplate.ContentHash &&
		row.VerificationProfileID == document.Profile.ID &&
		row.VerificationProfileVersion == int64(document.Profile.Version) &&
		row.VerificationProfileHash == document.Profile.ContentHash &&
		row.CheckCount == len(document.Checks) && row.ObligationCount == len(document.Obligations) &&
		row.RuntimePolicyHash == projections.runtimePolicyHash && row.ContentHash == row.PlanHash &&
		jsonProjectionEqual(row.TemplateReleases, projections.templateReleases) &&
		jsonProjectionEqual(row.Obligations, projections.obligations) &&
		jsonProjectionEqual(row.CheckIDs, projections.checkIDs) &&
		jsonProjectionEqual(row.RequiredCheckIDs, projections.requiredCheckIDs)
}

type CreateCanonicalRunInput struct {
	ID          string
	ProjectID   string
	Plan        PlanReference
	RequestKey  string
	RequestHash string
	Reason      string
	CreatedBy   string
}

type CanonicalRun struct {
	SchemaVersion string        `json:"schemaVersion"`
	ID            string        `json:"id"`
	ProjectID     string        `json:"projectId"`
	Plan          PlanReference `json:"plan"`
	RequestKey    string        `json:"requestKey"`
	RequestHash   string        `json:"requestHash"`
	Reason        string        `json:"reason"`
	State         RunState      `json:"state"`
	Version       uint64        `json:"version"`
	FenceEpoch    uint64        `json:"fenceEpoch"`
	CreatedBy     string        `json:"createdBy"`
	UpdatedBy     string        `json:"updatedBy"`
	CreatedAt     time.Time     `json:"createdAt"`
	UpdatedAt     time.Time     `json:"updatedAt"`
	Replayed      bool          `json:"replayed"`
}

type canonicalRunRow struct {
	ID            string    `gorm:"column:id"`
	SchemaVersion string    `gorm:"column:schema_version"`
	ProjectID     string    `gorm:"column:project_id"`
	PlanID        string    `gorm:"column:plan_id"`
	PlanHash      string    `gorm:"column:plan_hash"`
	RequestKey    string    `gorm:"column:request_key"`
	RequestHash   string    `gorm:"column:request_hash"`
	Reason        string    `gorm:"column:reason"`
	State         string    `gorm:"column:state"`
	Version       int64     `gorm:"column:version"`
	FenceEpoch    int64     `gorm:"column:fence_epoch"`
	CreatedBy     string    `gorm:"column:created_by"`
	UpdatedBy     string    `gorm:"column:updated_by"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (canonicalRunRow) TableName() string { return "canonical_verification_runs" }

func PrepareCreateCanonicalRunInput(input CreateCanonicalRunInput) (CreateCanonicalRunInput, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.Reason = strings.TrimSpace(input.Reason)
	if !validUUIDs(input.ID, input.ProjectID, input.Plan.ID, input.CreatedBy) ||
		!exactSHA256(input.Plan.ContentHash) || input.RequestKey == "" || len(input.RequestKey) > 128 ||
		input.Reason == "" || len(input.Reason) > 1000 || strings.ContainsRune(input.RequestKey, '\x00') ||
		strings.ContainsRune(input.Reason, '\x00') {
		return CreateCanonicalRunInput{}, runInvalid("Canonical Run request")
	}
	fingerprint := struct {
		SchemaVersion string        `json:"schemaVersion"`
		ProjectID     string        `json:"projectId"`
		Plan          PlanReference `json:"plan"`
		Reason        string        `json:"reason"`
		CreatedBy     string        `json:"createdBy"`
	}{"canonical-verification-run-request/v1", input.ProjectID, input.Plan, input.Reason, input.CreatedBy}
	hash, err := domain.CanonicalHash(fingerprint)
	if err != nil {
		return CreateCanonicalRunInput{}, runInvalid("Canonical Run request hash")
	}
	input.RequestHash = "sha256:" + hash
	return input, nil
}

func (store *PostgresStore) CreateCanonicalRun(ctx context.Context, input CreateCanonicalRunInput) (CanonicalRun, error) {
	prepared, err := PrepareCreateCanonicalRunInput(input)
	if err != nil || (input.RequestHash != "" && input.RequestHash != prepared.RequestHash) {
		return CanonicalRun{}, runInvalid("Canonical Run request hash")
	}
	var row canonicalRunRow
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Exec(`
INSERT INTO canonical_verification_runs (
  id, schema_version, project_id, plan_id, plan_hash,
  request_key, request_hash, reason, state, version, fence_epoch, created_by, updated_by
) VALUES (?, 'canonical-verification-run/v1', ?, ?, ?, ?, ?, ?, 'queued', 1, 0, ?, ?)
ON CONFLICT DO NOTHING
`, prepared.ID, prepared.ProjectID, prepared.Plan.ID, prepared.Plan.ContentHash,
			prepared.RequestKey, prepared.RequestHash, prepared.Reason, prepared.CreatedBy, prepared.CreatedBy)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		if err := transaction.Where("project_id = ? AND request_key = ?", prepared.ProjectID, prepared.RequestKey).Take(&row).Error; err != nil {
			return err
		}
		if row.ID != prepared.ID || row.PlanID != prepared.Plan.ID || row.PlanHash != prepared.Plan.ContentHash ||
			row.RequestHash != prepared.RequestHash || row.Reason != prepared.Reason || row.CreatedBy != prepared.CreatedBy {
			return ErrRunIdempotencyConflict
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CanonicalRun{}, mapRunStoreError("create Canonical Run", err)
	}
	run, err := canonicalRunFromRow(row)
	run.Replayed = err == nil && replayed
	return run, err
}

func (store *PostgresStore) GetCanonicalRun(ctx context.Context, projectID, runID string) (CanonicalRun, error) {
	if !validUUIDs(projectID, runID) {
		return CanonicalRun{}, runInvalid("Canonical Run project or identity")
	}
	var row canonicalRunRow
	err := store.database.WithContext(ctx).Where("id = ? AND project_id = ?", runID, projectID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CanonicalRun{}, ErrCanonicalRunNotFound
	}
	if err != nil {
		return CanonicalRun{}, mapRunStoreError("load Canonical Run", err)
	}
	return canonicalRunFromRow(row)
}

func (store *PostgresStore) ListCanonicalRuns(
	ctx context.Context,
	projectID string,
	subject CanonicalPlanSubject,
	limit int,
) ([]CanonicalRun, error) {
	if !validUUIDs(projectID) || limit < 1 || limit > 100 {
		return nil, runInvalid("Canonical Run list request")
	}
	if normalized, err := normalizeCanonicalPlanSubject(subject); err != nil || normalized != subject {
		return nil, runInvalid("Canonical Run list WorkspaceRevision")
	}
	var rows []canonicalRunRow
	err := store.database.WithContext(ctx).
		Table("canonical_verification_runs AS run").
		Select("run.*").
		Joins("JOIN canonical_verification_plans AS plan ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash").
		Where("run.project_id = ? AND plan.workspace_artifact_id = ? AND plan.workspace_revision_id = ? AND plan.workspace_content_hash = ?",
			projectID, subject.WorkspaceArtifactID, subject.WorkspaceRevisionID, subject.WorkspaceContentHash).
		Order("run.created_at DESC, run.id DESC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, mapRunStoreError("list Canonical VerificationRuns", err)
	}
	runs := make([]CanonicalRun, len(rows))
	for index, row := range rows {
		run, err := canonicalRunFromRow(row)
		if err != nil {
			return nil, err
		}
		runs[index] = run
	}
	return runs, nil
}

func (store *PostgresStore) FindCanonicalRunByRequest(
	ctx context.Context,
	projectID, requestKey string,
) (CanonicalRun, bool, error) {
	requestKey = strings.TrimSpace(requestKey)
	if !validUUIDs(projectID) || requestKey == "" || len(requestKey) > 128 || strings.ContainsRune(requestKey, '\x00') {
		return CanonicalRun{}, false, runInvalid("Canonical Run request lookup")
	}
	var row canonicalRunRow
	err := store.database.WithContext(ctx).
		Where("project_id = ? AND request_key = ?", projectID, requestKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CanonicalRun{}, false, nil
	}
	if err != nil {
		return CanonicalRun{}, false, mapRunStoreError("find Canonical Run request", err)
	}
	run, err := canonicalRunFromRow(row)
	return run, err == nil, err
}

func canonicalRunFromRow(row canonicalRunRow) (CanonicalRun, error) {
	state := RunState(row.State)
	if row.SchemaVersion != CanonicalRunSchemaVersion || !validUUIDs(row.ID, row.ProjectID, row.PlanID, row.CreatedBy, row.UpdatedBy) ||
		!exactSHA256(row.PlanHash) || !exactSHA256(row.RequestHash) || row.RequestKey == "" || row.Reason == "" ||
		!validRunState(state) || row.Version < 1 || row.FenceEpoch < 0 || row.CreatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return CanonicalRun{}, runIntegrity("Canonical Run row projection", nil)
	}
	return CanonicalRun{
		SchemaVersion: row.SchemaVersion, ID: row.ID, ProjectID: row.ProjectID,
		Plan:       PlanReference{ID: row.PlanID, ContentHash: row.PlanHash},
		RequestKey: row.RequestKey, RequestHash: row.RequestHash, Reason: row.Reason,
		State: state, Version: uint64(row.Version), FenceEpoch: uint64(row.FenceEpoch),
		CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

type canonicalReceiptRow struct {
	ID           string    `gorm:"column:id"`
	ProjectID    string    `gorm:"column:project_id"`
	RunID        string    `gorm:"column:run_id"`
	PayloadHash  string    `gorm:"column:payload_hash"`
	ContentRef   string    `gorm:"column:content_ref"`
	ContentHash  string    `gorm:"column:content_hash"`
	Decision     string    `gorm:"column:decision"`
	BlockerCount int       `gorm:"column:blocker_count"`
	MustCount    int       `gorm:"column:must_count"`
	MustPassed   int       `gorm:"column:must_passed_count"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (canonicalReceiptRow) TableName() string { return "canonical_verification_receipts" }

func (store *PostgresStore) GetCanonicalReceipt(
	ctx context.Context,
	projectID, receiptID, expectedHash string,
) (CanonicalReceipt, error) {
	if !validUUIDs(projectID, receiptID) || !exactSHA256(expectedHash) {
		return CanonicalReceipt{}, fmt.Errorf("%w: Canonical Receipt reference", ErrInvalidReceipt)
	}
	var row canonicalReceiptRow
	err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND payload_hash = ?", receiptID, projectID, expectedHash).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CanonicalReceipt{}, ErrCanonicalReceiptNotFound
	}
	if err != nil {
		return CanonicalReceipt{}, mapReceiptStoreError("load Canonical Receipt", err)
	}
	stored, err := store.contents.Get(ctx, row.ContentRef, row.ContentHash)
	if err != nil {
		return CanonicalReceipt{}, receiptIntegrity("load Canonical Receipt content", err)
	}
	if stored.ProjectID != projectID || stored.AggregateType != canonicalReceiptAggregateType ||
		stored.AggregateID != receiptID || stored.SchemaVersion != 1 || stored.State != content.StateFinalized ||
		stored.ID != row.ContentRef || stored.ContentHash != row.ContentHash {
		return CanonicalReceipt{}, receiptIntegrity("Canonical Receipt storage projection", nil)
	}
	var receipt CanonicalReceipt
	if err := json.Unmarshal(stored.Payload, &receipt); err != nil {
		return CanonicalReceipt{}, receiptIntegrity("decode Canonical Receipt", err)
	}
	parsed, err := ParseCanonicalReceipt(receipt)
	if err != nil || parsed.ID != row.ID || parsed.RunID != row.RunID || parsed.ProjectID != row.ProjectID ||
		parsed.PayloadHash != row.PayloadHash || string(parsed.Decision) != row.Decision ||
		parsed.BlockerCount != row.BlockerCount || parsed.MustCount != row.MustCount ||
		parsed.MustPassedCount != row.MustPassed {
		return CanonicalReceipt{}, receiptIntegrity("Canonical Receipt content projection", err)
	}
	return parsed, nil
}

func (store *PostgresStore) GetCanonicalReceiptByRun(
	ctx context.Context,
	projectID, runID string,
) (CanonicalReceipt, error) {
	if !validUUIDs(projectID, runID) {
		return CanonicalReceipt{}, fmt.Errorf("%w: Canonical Receipt Run reference", ErrInvalidReceipt)
	}
	var row canonicalReceiptRow
	err := store.database.WithContext(ctx).Where("project_id = ? AND run_id = ?", projectID, runID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CanonicalReceipt{}, ErrCanonicalReceiptNotFound
	}
	if err != nil {
		return CanonicalReceipt{}, mapReceiptStoreError("load Canonical Receipt by Run", err)
	}
	return store.GetCanonicalReceipt(ctx, projectID, row.ID, row.PayloadHash)
}
