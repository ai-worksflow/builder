package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"github.com/worksflow/builder/backend/internal/verification"
	"gorm.io/gorm"
)

const (
	previewReceiptAggregate     = "release_preview_receipt"
	promotionApprovalAggregate  = "release_promotion_approval"
	productionReceiptAggregate  = "release_production_receipt"
	deploymentRevisionAggregate = "release_deployment_revision"
)

type previewRunRow struct {
	ID                string    `gorm:"column:id"`
	ProjectID         string    `gorm:"column:project_id"`
	ReleaseBundleID   string    `gorm:"column:release_bundle_id"`
	ReleaseBundleHash string    `gorm:"column:release_bundle_hash"`
	RequestKey        string    `gorm:"column:request_key"`
	RequestHash       string    `gorm:"column:request_hash"`
	Reason            string    `gorm:"column:reason"`
	State             string    `gorm:"column:state"`
	Version           uint64    `gorm:"column:version"`
	LeaseEpoch        uint64    `gorm:"column:lease_epoch"`
	LeaseExpiresAt    time.Time `gorm:"column:lease_expires_at"`
	CreatedBy         string    `gorm:"column:created_by"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (previewRunRow) TableName() string { return "release_preview_runs" }

type previewReceiptRow struct {
	ID                    string          `gorm:"column:id"`
	SchemaVersion         string          `gorm:"column:schema_version"`
	RunID                 string          `gorm:"column:run_id"`
	ProjectID             string          `gorm:"column:project_id"`
	ReleaseBundleID       string          `gorm:"column:release_bundle_id"`
	ReleaseBundleHash     string          `gorm:"column:release_bundle_hash"`
	CanonicalReceiptID    string          `gorm:"column:canonical_receipt_id"`
	CanonicalReceiptHash  string          `gorm:"column:canonical_receipt_hash"`
	WorkspaceArtifactID   string          `gorm:"column:workspace_artifact_id"`
	WorkspaceRevisionID   string          `gorm:"column:workspace_revision_id"`
	WorkspaceContentHash  string          `gorm:"column:workspace_content_hash"`
	ReleaseArtifacts      json.RawMessage `gorm:"column:release_artifacts"`
	Namespace             string          `gorm:"column:namespace"`
	Provider              string          `gorm:"column:provider"`
	ProviderRef           string          `gorm:"column:provider_ref"`
	Checks                json.RawMessage `gorm:"column:checks"`
	Decision              string          `gorm:"column:decision"`
	ControllerOperationID *string         `gorm:"column:controller_operation_id"`
	ControllerResultHash  *string         `gorm:"column:controller_result_hash"`
	ContentStore          string          `gorm:"column:content_store"`
	ContentRef            string          `gorm:"column:content_ref"`
	ContentHash           string          `gorm:"column:content_hash"`
	PayloadHash           string          `gorm:"column:payload_hash"`
	CreatedBy             string          `gorm:"column:created_by"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
}

func (previewReceiptRow) TableName() string { return "release_preview_receipts" }

type promotionApprovalRow struct {
	ID                 string    `gorm:"column:id"`
	ProjectID          string    `gorm:"column:project_id"`
	ReleaseBundleID    string    `gorm:"column:release_bundle_id"`
	ReleaseBundleHash  string    `gorm:"column:release_bundle_hash"`
	PreviewReceiptID   string    `gorm:"column:preview_receipt_id"`
	PreviewReceiptHash string    `gorm:"column:preview_receipt_hash"`
	Reason             string    `gorm:"column:reason"`
	ContentStore       string    `gorm:"column:content_store"`
	ContentRef         string    `gorm:"column:content_ref"`
	ContentHash        string    `gorm:"column:content_hash"`
	PayloadHash        string    `gorm:"column:payload_hash"`
	CreatedBy          string    `gorm:"column:created_by"`
	CreatedAt          time.Time `gorm:"column:created_at"`
}

func (promotionApprovalRow) TableName() string { return "release_promotion_approvals" }

type productionRunRow struct {
	ID                    string    `gorm:"column:id"`
	ProjectID             string    `gorm:"column:project_id"`
	Environment           string    `gorm:"column:environment"`
	Operation             string    `gorm:"column:operation"`
	ReleaseBundleID       string    `gorm:"column:release_bundle_id"`
	ReleaseBundleHash     string    `gorm:"column:release_bundle_hash"`
	PreviewReceiptID      string    `gorm:"column:preview_receipt_id"`
	PreviewReceiptHash    string    `gorm:"column:preview_receipt_hash"`
	PromotionApprovalID   string    `gorm:"column:promotion_approval_id"`
	PromotionApprovalHash string    `gorm:"column:promotion_approval_hash"`
	SourceRevisionID      *string   `gorm:"column:source_revision_id"`
	SourceRevisionHash    *string   `gorm:"column:source_revision_hash"`
	ExpectedRevisionID    *string   `gorm:"column:expected_revision_id"`
	ExpectedRevisionHash  *string   `gorm:"column:expected_revision_hash"`
	ExpectedReceiptID     *string   `gorm:"column:expected_production_receipt_id"`
	ExpectedReceiptHash   *string   `gorm:"column:expected_production_receipt_hash"`
	RequestKey            string    `gorm:"column:request_key"`
	RequestHash           string    `gorm:"column:request_hash"`
	Reason                string    `gorm:"column:reason"`
	State                 string    `gorm:"column:state"`
	Version               uint64    `gorm:"column:version"`
	LeaseEpoch            uint64    `gorm:"column:lease_epoch"`
	LeaseExpiresAt        time.Time `gorm:"column:lease_expires_at"`
	CreatedBy             string    `gorm:"column:created_by"`
	CreatedAt             time.Time `gorm:"column:created_at"`
	UpdatedAt             time.Time `gorm:"column:updated_at"`
}

func (productionRunRow) TableName() string { return "release_deployment_runs" }

type productionHeadRow struct {
	ProjectID              string  `gorm:"column:project_id"`
	Environment            string  `gorm:"column:environment"`
	DeploymentRevisionID   *string `gorm:"column:deployment_revision_id"`
	DeploymentRevisionHash *string `gorm:"column:deployment_revision_hash"`
	ProductionReceiptID    *string `gorm:"column:production_receipt_id"`
	ProductionReceiptHash  *string `gorm:"column:production_receipt_hash"`
	Generation             uint64  `gorm:"column:generation"`
}

func (productionHeadRow) TableName() string { return "release_production_heads" }

type productionReceiptRow struct {
	ID                    string          `gorm:"column:id"`
	SchemaVersion         string          `gorm:"column:schema_version"`
	RunID                 string          `gorm:"column:run_id"`
	ProjectID             string          `gorm:"column:project_id"`
	Operation             string          `gorm:"column:operation"`
	ReleaseBundleID       string          `gorm:"column:release_bundle_id"`
	ReleaseBundleHash     string          `gorm:"column:release_bundle_hash"`
	PreviewReceiptID      string          `gorm:"column:preview_receipt_id"`
	PreviewReceiptHash    string          `gorm:"column:preview_receipt_hash"`
	PromotionApprovalID   string          `gorm:"column:promotion_approval_id"`
	PromotionApprovalHash string          `gorm:"column:promotion_approval_hash"`
	SourceRevisionID      *string         `gorm:"column:source_revision_id"`
	SourceRevisionHash    *string         `gorm:"column:source_revision_hash"`
	Provider              string          `gorm:"column:provider"`
	ProviderRef           string          `gorm:"column:provider_ref"`
	PublicURL             string          `gorm:"column:public_url"`
	Checks                json.RawMessage `gorm:"column:checks"`
	Decision              string          `gorm:"column:decision"`
	ControllerOperationID *string         `gorm:"column:controller_operation_id"`
	ControllerResultHash  *string         `gorm:"column:controller_result_hash"`
	ContentStore          string          `gorm:"column:content_store"`
	ContentRef            string          `gorm:"column:content_ref"`
	ContentHash           string          `gorm:"column:content_hash"`
	PayloadHash           string          `gorm:"column:payload_hash"`
	CreatedBy             string          `gorm:"column:created_by"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
}

func (productionReceiptRow) TableName() string { return "release_production_receipts" }

type deploymentRevisionRow struct {
	ID                    string          `gorm:"column:id"`
	SchemaVersion         string          `gorm:"column:schema_version"`
	RunID                 string          `gorm:"column:run_id"`
	ProjectID             string          `gorm:"column:project_id"`
	Operation             string          `gorm:"column:operation"`
	ReleaseBundleID       string          `gorm:"column:release_bundle_id"`
	ReleaseBundleHash     string          `gorm:"column:release_bundle_hash"`
	PreviewReceiptID      string          `gorm:"column:preview_receipt_id"`
	PreviewReceiptHash    string          `gorm:"column:preview_receipt_hash"`
	PromotionApprovalID   string          `gorm:"column:promotion_approval_id"`
	PromotionApprovalHash string          `gorm:"column:promotion_approval_hash"`
	ProductionReceiptID   string          `gorm:"column:production_receipt_id"`
	ProductionReceiptHash string          `gorm:"column:production_receipt_hash"`
	SourceRevisionID      *string         `gorm:"column:source_revision_id"`
	SourceRevisionHash    *string         `gorm:"column:source_revision_hash"`
	Provider              string          `gorm:"column:provider"`
	ProviderRef           string          `gorm:"column:provider_ref"`
	PublicURL             string          `gorm:"column:public_url"`
	Checks                json.RawMessage `gorm:"column:checks"`
	ControllerOperationID *string         `gorm:"column:controller_operation_id"`
	ControllerResultHash  *string         `gorm:"column:controller_result_hash"`
	ContentStore          string          `gorm:"column:content_store"`
	ContentRef            string          `gorm:"column:content_ref"`
	ContentHash           string          `gorm:"column:content_hash"`
	PayloadHash           string          `gorm:"column:payload_hash"`
	CreatedBy             string          `gorm:"column:created_by"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
}

func (deploymentRevisionRow) TableName() string { return "release_deployment_revisions" }

func (store *Store) CreatePreviewRun(
	ctx context.Context,
	input CreatePreviewRunInput,
) (PreviewRun, bool, error) {
	result := store.database.WithContext(ctx).Exec(`
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES (?, 'release-preview-run/v1', ?, ?, ?, ?, ?, ?, 'queued', 1, ?, ?)
ON CONFLICT (project_id, request_key) DO NOTHING
`, input.ID, input.ProjectID, input.ReleaseBundle.ID, input.ReleaseBundle.ContentHash,
		input.RequestKey, input.RequestHash, input.Reason, input.CreatedBy, input.CreatedBy)
	if result.Error != nil {
		return PreviewRun{}, false, result.Error
	}
	var row previewRunRow
	if err := store.database.WithContext(ctx).
		Where("project_id = ? AND request_key = ?", input.ProjectID, input.RequestKey).Take(&row).Error; err != nil {
		return PreviewRun{}, false, err
	}
	replayed := result.RowsAffected == 0
	if row.ID != input.ID || row.ReleaseBundleID != input.ReleaseBundle.ID ||
		row.ReleaseBundleHash != input.ReleaseBundle.ContentHash || row.RequestHash != input.RequestHash ||
		row.Reason != input.Reason || row.CreatedBy != input.CreatedBy {
		return PreviewRun{}, false, ErrBundleConflict
	}
	return store.previewRunFromRow(ctx, row), replayed, nil
}

func (store *Store) GetPreviewRun(ctx context.Context, projectID, runID string) (PreviewRun, error) {
	var row previewRunRow
	if err := store.database.WithContext(ctx).Where("id = ? AND project_id = ?", runID, projectID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PreviewRun{}, ErrBundleNotFound
		}
		return PreviewRun{}, err
	}
	return store.previewRunFromRow(ctx, row), nil
}

func (store *Store) ListPreviewRuns(
	ctx context.Context,
	projectID string,
	bundle repository.ExactReference,
	limit int,
) ([]PreviewRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []previewRunRow
	if err := store.database.WithContext(ctx).
		Where("project_id = ? AND release_bundle_id = ? AND release_bundle_hash = ?", projectID, bundle.ID, bundle.ContentHash).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]PreviewRun, 0, len(rows))
	for _, row := range rows {
		result = append(result, store.previewRunFromRow(ctx, row))
	}
	return result, nil
}

func (store *Store) previewRunFromRow(ctx context.Context, row previewRunRow) PreviewRun {
	run := PreviewRun{
		ID: row.ID, ProjectID: row.ProjectID,
		ReleaseBundle: repositoryReference(row.ReleaseBundleID, row.ReleaseBundleHash),
		Reason:        row.Reason, State: DeliveryRunState(row.State), Version: row.Version,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	var receipt previewReceiptRow
	if err := store.database.WithContext(ctx).Where("run_id = ?", row.ID).Take(&receipt).Error; err == nil {
		run.Receipt = referencePointer(receipt.ID, receipt.PayloadHash)
	}
	return run
}

func (store *Store) GetPreviewReceipt(
	ctx context.Context,
	projectID, receiptID, receiptHash string,
) (PreviewReceipt, error) {
	var row previewReceiptRow
	if err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND payload_hash = ?", receiptID, projectID, receiptHash).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PreviewReceipt{}, ErrBundleNotFound
		}
		return PreviewReceipt{}, err
	}
	return store.hydratePreviewReceipt(ctx, row)
}

func (store *Store) SavePromotionApproval(
	ctx context.Context,
	approval PromotionApproval,
) (PromotionApproval, bool, error) {
	parsed, err := ParsePromotionApproval(approval)
	if err != nil {
		return PromotionApproval{}, false, err
	}
	payload, err := domain.CanonicalJSON(parsed)
	if err != nil {
		return PromotionApproval{}, false, err
	}
	reference, err := store.contents.PutPending(
		ctx, parsed.ProjectID, promotionApprovalAggregate, parsed.ID, 1, payload,
	)
	if err != nil {
		return PromotionApproval{}, false, err
	}
	abort := true
	defer func() {
		if abort {
			_ = store.contents.Abort(context.Background(), reference.ID)
		}
	}()
	result := store.database.WithContext(ctx).Exec(`
INSERT INTO release_promotion_approvals (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  preview_receipt_id, preview_receipt_hash, reason,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (?, 'release-promotion-approval/v1', ?, ?, ?, ?, ?, ?, 'mongo', ?, ?, ?, ?, ?)
ON CONFLICT (preview_receipt_id, preview_receipt_hash) DO NOTHING
`, parsed.ID, parsed.ProjectID, parsed.ReleaseBundle.ID, parsed.ReleaseBundle.ContentHash,
		parsed.PreviewReceipt.ID, parsed.PreviewReceipt.ContentHash, parsed.Reason,
		reference.ID, reference.ContentHash, parsed.PayloadHash, parsed.CreatedBy, parsed.CreatedAt)
	if result.Error != nil {
		return PromotionApproval{}, false, result.Error
	}
	replayed := result.RowsAffected == 0
	if replayed {
		_ = store.contents.Abort(context.Background(), reference.ID)
		abort = false
		var row promotionApprovalRow
		if err := store.database.WithContext(ctx).
			Where("preview_receipt_id = ? AND preview_receipt_hash = ?", parsed.PreviewReceipt.ID, parsed.PreviewReceipt.ContentHash).
			Take(&row).Error; err != nil {
			return PromotionApproval{}, false, err
		}
		if row.ID != parsed.ID || row.PayloadHash != parsed.PayloadHash || row.CreatedBy != parsed.CreatedBy {
			return PromotionApproval{}, false, ErrBundleConflict
		}
		value, err := store.hydratePromotionApproval(ctx, row)
		return value, true, err
	}
	abort = false
	if err := store.contents.Finalize(ctx, reference.ID); err != nil {
		return PromotionApproval{}, false, fmt.Errorf("%w: finalize PromotionApproval: %v", core.ErrContentNotReady, err)
	}
	value, err := store.GetPromotionApproval(ctx, parsed.ProjectID, parsed.ID, parsed.PayloadHash)
	return value, false, err
}

func (store *Store) GetPromotionApproval(
	ctx context.Context,
	projectID, approvalID, approvalHash string,
) (PromotionApproval, error) {
	var row promotionApprovalRow
	if err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND payload_hash = ?", approvalID, projectID, approvalHash).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PromotionApproval{}, ErrBundleNotFound
		}
		return PromotionApproval{}, err
	}
	return store.hydratePromotionApproval(ctx, row)
}

func (store *Store) GetPromotionApprovalByPreview(
	ctx context.Context,
	projectID, previewID, previewHash string,
) (PromotionApproval, error) {
	var row promotionApprovalRow
	if err := store.database.WithContext(ctx).
		Where("project_id = ? AND preview_receipt_id = ? AND preview_receipt_hash = ?", projectID, previewID, previewHash).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PromotionApproval{}, ErrBundleNotFound
		}
		return PromotionApproval{}, err
	}
	return store.hydratePromotionApproval(ctx, row)
}

func (store *Store) CreateProductionRun(
	ctx context.Context,
	input CreateProductionRunInput,
) (ProductionRun, bool, error) {
	environment := strings.TrimSpace(input.Environment)
	if environment == "" {
		environment = "production"
	}
	var row productionRunRow
	replayed := false
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if result := transaction.Exec(`
INSERT INTO release_production_heads (project_id, environment, generation, updated_by)
VALUES (?, ?, 0, ?)
ON CONFLICT (project_id, environment) DO NOTHING
`, input.ProjectID, environment, input.CreatedBy); result.Error != nil {
			return result.Error
		}
		var head productionHeadRow
		if result := transaction.Raw(`
SELECT project_id::text, environment, deployment_revision_id::text, deployment_revision_hash,
       production_receipt_id::text, production_receipt_hash, generation
FROM release_production_heads
WHERE project_id = ? AND environment = ?
FOR UPDATE
`, input.ProjectID, environment).Scan(&head); result.Error != nil {
			return result.Error
		}
		if head.ProjectID == "" {
			return fmt.Errorf("%w: production head was not established", ErrBundleIntegrity)
		}
		var existing productionRunRow
		existingResult := transaction.Where(
			"project_id = ? AND request_key = ?", input.ProjectID, input.RequestKey,
		).Take(&existing)
		if existingResult.Error == nil {
			row = existing
			replayed = true
			return nil
		}
		if !errors.Is(existingResult.Error, gorm.ErrRecordNotFound) {
			return existingResult.Error
		}
		var sourceID, sourceHash any
		if input.SourceRevision != nil {
			sourceID, sourceHash = input.SourceRevision.ID, input.SourceRevision.ContentHash
		}
		result := transaction.Exec(`
INSERT INTO release_deployment_runs (
  id, schema_version, project_id, environment, operation,
  release_bundle_id, release_bundle_hash, preview_receipt_id, preview_receipt_hash,
  promotion_approval_id, promotion_approval_hash, source_revision_id, source_revision_hash,
  expected_revision_id, expected_revision_hash,
  expected_production_receipt_id, expected_production_receipt_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES (?, 'release-deployment-run/v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', 1, ?, ?)
ON CONFLICT (project_id, request_key) DO NOTHING
`, input.ID, input.ProjectID, environment, input.Operation,
			input.ReleaseBundle.ID, input.ReleaseBundle.ContentHash,
			input.PreviewReceipt.ID, input.PreviewReceipt.ContentHash,
			input.PromotionApproval.ID, input.PromotionApproval.ContentHash, sourceID, sourceHash,
			head.DeploymentRevisionID, head.DeploymentRevisionHash,
			head.ProductionReceiptID, head.ProductionReceiptHash,
			input.RequestKey, input.RequestHash, input.Reason, input.CreatedBy, input.CreatedBy)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		return transaction.Where("project_id = ? AND request_key = ?", input.ProjectID, input.RequestKey).Take(&row).Error
	})
	if err != nil {
		if isProductionHeadConflict(err) {
			return ProductionRun{}, false, fmt.Errorf("%w: %v", ErrProductionHeadConflict, err)
		}
		return ProductionRun{}, false, err
	}
	if row.ID != input.ID || row.Operation != string(input.Operation) ||
		row.Environment != environment ||
		row.ReleaseBundleID != input.ReleaseBundle.ID || row.ReleaseBundleHash != input.ReleaseBundle.ContentHash ||
		row.PreviewReceiptID != input.PreviewReceipt.ID || row.PreviewReceiptHash != input.PreviewReceipt.ContentHash ||
		row.PromotionApprovalID != input.PromotionApproval.ID || row.PromotionApprovalHash != input.PromotionApproval.ContentHash ||
		row.RequestHash != input.RequestHash || row.Reason != input.Reason || row.CreatedBy != input.CreatedBy ||
		!sameOptionalReference(row.SourceRevisionID, row.SourceRevisionHash, input.SourceRevision) {
		return ProductionRun{}, false, ErrBundleConflict
	}
	return store.productionRunFromRow(ctx, row), replayed, nil
}

func (store *Store) GetProductionRun(ctx context.Context, projectID, runID string) (ProductionRun, error) {
	var row productionRunRow
	if err := store.database.WithContext(ctx).Where("id = ? AND project_id = ?", runID, projectID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ProductionRun{}, ErrBundleNotFound
		}
		return ProductionRun{}, err
	}
	return store.productionRunFromRow(ctx, row), nil
}

func (store *Store) ListProductionRuns(
	ctx context.Context,
	projectID string,
	bundle repository.ExactReference,
	limit int,
) ([]ProductionRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []productionRunRow
	if err := store.database.WithContext(ctx).
		Where("project_id = ? AND release_bundle_id = ? AND release_bundle_hash = ?", projectID, bundle.ID, bundle.ContentHash).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]ProductionRun, 0, len(rows))
	for _, row := range rows {
		result = append(result, store.productionRunFromRow(ctx, row))
	}
	return result, nil
}

func (store *Store) ListProductionRunsForProject(
	ctx context.Context,
	projectID string,
	limit int,
) ([]ProductionRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []productionRunRow
	if err := store.database.WithContext(ctx).
		Where("project_id = ?", projectID).Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]ProductionRun, 0, len(rows))
	for _, row := range rows {
		result = append(result, store.productionRunFromRow(ctx, row))
	}
	return result, nil
}

func (store *Store) productionRunFromRow(ctx context.Context, row productionRunRow) ProductionRun {
	run := ProductionRun{
		ID: row.ID, ProjectID: row.ProjectID, Environment: row.Environment, Operation: DeploymentOperation(row.Operation),
		ReleaseBundle:     repositoryReference(row.ReleaseBundleID, row.ReleaseBundleHash),
		PreviewReceipt:    repositoryReference(row.PreviewReceiptID, row.PreviewReceiptHash),
		PromotionApproval: repositoryReference(row.PromotionApprovalID, row.PromotionApprovalHash),
		SourceRevision:    optionalReference(row.SourceRevisionID, row.SourceRevisionHash),
		ExpectedRevision:  optionalReference(row.ExpectedRevisionID, row.ExpectedRevisionHash),
		ExpectedReceipt:   optionalReference(row.ExpectedReceiptID, row.ExpectedReceiptHash),
		Reason:            row.Reason, State: DeliveryRunState(row.State), Version: row.Version,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	var revision deploymentRevisionRow
	var receipt productionReceiptRow
	if err := store.database.WithContext(ctx).Where("run_id = ?", row.ID).Take(&receipt).Error; err == nil {
		run.Receipt = referencePointer(receipt.ID, receipt.PayloadHash)
	}
	if err := store.database.WithContext(ctx).Where("run_id = ?", row.ID).Take(&revision).Error; err == nil {
		run.Revision = referencePointer(revision.ID, revision.PayloadHash)
	}
	return run
}

func isProductionHeadConflict(err error) bool {
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) {
		return false
	}
	if postgres.Code == "23505" && postgres.ConstraintName == "release_deployment_runs_one_nonterminal_environment_idx" {
		return true
	}
	return postgres.Code == "40001" &&
		(strings.Contains(postgres.Message, "expected production head is stale") ||
			strings.Contains(postgres.Message, "release production head"))
}

func isPreviewRunConflict(err error) bool {
	var postgres *pgconn.PgError
	return errors.As(err, &postgres) && postgres.Code == "23505" &&
		postgres.ConstraintName == "release_preview_runs_one_nonterminal_bundle_idx"
}

func (store *Store) GetProductionReceipt(
	ctx context.Context,
	projectID, receiptID, receiptHash string,
) (ProductionReceipt, error) {
	var row productionReceiptRow
	if err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND payload_hash = ?", receiptID, projectID, receiptHash).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ProductionReceipt{}, ErrBundleNotFound
		}
		return ProductionReceipt{}, err
	}
	return store.hydrateProductionReceipt(ctx, row)
}

func (store *Store) GetDeploymentRevision(
	ctx context.Context,
	projectID, revisionID, revisionHash string,
) (DeploymentRevision, error) {
	var row deploymentRevisionRow
	if err := store.database.WithContext(ctx).
		Where("id = ? AND project_id = ? AND payload_hash = ?", revisionID, projectID, revisionHash).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DeploymentRevision{}, ErrBundleNotFound
		}
		return DeploymentRevision{}, err
	}
	return store.hydrateDeploymentRevision(ctx, row)
}

func (store *Store) hydratePreviewReceipt(ctx context.Context, row previewReceiptRow) (PreviewReceipt, error) {
	var value PreviewReceipt
	if err := store.hydrateDeliveryFact(ctx, row.ProjectID, previewReceiptAggregate, row.ID,
		row.ContentStore, row.ContentRef, row.ContentHash, deliveryFactSchemaNumber(row.SchemaVersion), &value); err != nil {
		return PreviewReceipt{}, err
	}
	parsed, err := ParsePreviewReceipt(value)
	var projectedArtifacts []verification.CanonicalReleaseArtifact
	var projectedChecks []PreviewCheck
	projectionErr := json.Unmarshal(row.ReleaseArtifacts, &projectedArtifacts)
	if projectionErr == nil {
		projectionErr = json.Unmarshal(row.Checks, &projectedChecks)
	}
	if err != nil || parsed.ID != row.ID || parsed.RunID != row.RunID || parsed.ProjectID != row.ProjectID ||
		parsed.ReleaseBundle.ID != row.ReleaseBundleID || parsed.ReleaseBundle.ContentHash != row.ReleaseBundleHash ||
		parsed.CanonicalReceipt.ID != row.CanonicalReceiptID || parsed.CanonicalReceipt.ContentHash != row.CanonicalReceiptHash ||
		parsed.Workspace.WorkspaceArtifactID != row.WorkspaceArtifactID || parsed.Workspace.WorkspaceRevisionID != row.WorkspaceRevisionID ||
		parsed.Workspace.WorkspaceContentHash != row.WorkspaceContentHash || parsed.Namespace != row.Namespace ||
		parsed.Provider != row.Provider || parsed.ProviderRef != row.ProviderRef || string(parsed.Decision) != row.Decision ||
		parsed.SchemaVersion != row.SchemaVersion ||
		!sameControllerOperationProjection(parsed.ControllerOperation, row.ControllerOperationID, row.ControllerResultHash) ||
		parsed.PayloadHash != row.PayloadHash || parsed.CreatedBy != row.CreatedBy ||
		!parsed.CreatedAt.Equal(row.CreatedAt) || projectionErr != nil ||
		!sameArtifacts(parsed.ReleaseArtifacts, projectedArtifacts) || !samePreviewChecks(parsed.Checks, projectedChecks) {
		return PreviewReceipt{}, fmt.Errorf("%w: PreviewReceipt projection mismatch", ErrBundleIntegrity)
	}
	bundle, err := store.Get(ctx, row.ProjectID, row.ReleaseBundleID, row.ReleaseBundleHash)
	if err != nil || bundle.CanonicalReceipt != parsed.CanonicalReceipt || bundle.Workspace != parsed.Workspace ||
		!sameArtifacts(bundle.ReleaseArtifacts, parsed.ReleaseArtifacts) {
		return PreviewReceipt{}, fmt.Errorf("%w: PreviewReceipt Bundle mismatch", ErrBundleIntegrity)
	}
	return parsed, nil
}

func (store *Store) hydratePromotionApproval(ctx context.Context, row promotionApprovalRow) (PromotionApproval, error) {
	var value PromotionApproval
	if err := store.hydrateDeliveryFact(ctx, row.ProjectID, promotionApprovalAggregate, row.ID,
		row.ContentStore, row.ContentRef, row.ContentHash, 1, &value); err != nil {
		return PromotionApproval{}, err
	}
	parsed, err := ParsePromotionApproval(value)
	if err != nil || parsed.ID != row.ID || parsed.ProjectID != row.ProjectID ||
		parsed.ReleaseBundle.ID != row.ReleaseBundleID || parsed.ReleaseBundle.ContentHash != row.ReleaseBundleHash ||
		parsed.PreviewReceipt.ID != row.PreviewReceiptID || parsed.PreviewReceipt.ContentHash != row.PreviewReceiptHash ||
		parsed.Reason != row.Reason || parsed.PayloadHash != row.PayloadHash || parsed.CreatedBy != row.CreatedBy ||
		!parsed.CreatedAt.Equal(row.CreatedAt) {
		return PromotionApproval{}, fmt.Errorf("%w: PromotionApproval projection mismatch", ErrBundleIntegrity)
	}
	preview, err := store.GetPreviewReceipt(ctx, row.ProjectID, row.PreviewReceiptID, row.PreviewReceiptHash)
	if err != nil || preview.Decision != PreviewPassed || preview.ReleaseBundle != parsed.ReleaseBundle {
		return PromotionApproval{}, fmt.Errorf("%w: PromotionApproval PreviewReceipt mismatch", ErrBundleIntegrity)
	}
	return parsed, nil
}

func (store *Store) hydrateProductionReceipt(ctx context.Context, row productionReceiptRow) (ProductionReceipt, error) {
	var value ProductionReceipt
	if err := store.hydrateDeliveryFact(ctx, row.ProjectID, productionReceiptAggregate, row.ID,
		row.ContentStore, row.ContentRef, row.ContentHash, deliveryFactSchemaNumber(row.SchemaVersion), &value); err != nil {
		return ProductionReceipt{}, err
	}
	parsed, err := ParseProductionReceipt(value)
	var projectedChecks []PreviewCheck
	projectionErr := json.Unmarshal(row.Checks, &projectedChecks)
	if err != nil || parsed.ID != row.ID || parsed.RunID != row.RunID || parsed.ProjectID != row.ProjectID ||
		string(parsed.Operation) != row.Operation || parsed.ReleaseBundle.ID != row.ReleaseBundleID ||
		parsed.ReleaseBundle.ContentHash != row.ReleaseBundleHash || parsed.PreviewReceipt.ID != row.PreviewReceiptID ||
		parsed.PreviewReceipt.ContentHash != row.PreviewReceiptHash || parsed.Approval.ID != row.PromotionApprovalID ||
		parsed.Approval.ContentHash != row.PromotionApprovalHash || parsed.Provider != row.Provider ||
		parsed.ProviderRef != row.ProviderRef || parsed.PublicURL != row.PublicURL || string(parsed.Decision) != row.Decision ||
		parsed.SchemaVersion != row.SchemaVersion ||
		!sameControllerOperationProjection(parsed.ControllerOperation, row.ControllerOperationID, row.ControllerResultHash) ||
		parsed.PayloadHash != row.PayloadHash || parsed.CreatedBy != row.CreatedBy ||
		!parsed.CreatedAt.Equal(row.CreatedAt) || projectionErr != nil ||
		!samePreviewChecks(parsed.Checks, projectedChecks) ||
		!sameOptionalReference(row.SourceRevisionID, row.SourceRevisionHash, parsed.SourceRevision) {
		return ProductionReceipt{}, fmt.Errorf("%w: ProductionReceipt projection mismatch", ErrBundleIntegrity)
	}
	approval, approvalErr := store.GetPromotionApproval(ctx, row.ProjectID, row.PromotionApprovalID, row.PromotionApprovalHash)
	preview, previewErr := store.GetPreviewReceipt(ctx, row.ProjectID, row.PreviewReceiptID, row.PreviewReceiptHash)
	bundle, bundleErr := store.Get(ctx, row.ProjectID, row.ReleaseBundleID, row.ReleaseBundleHash)
	if approvalErr != nil || previewErr != nil || bundleErr != nil || approval.ReleaseBundle != parsed.ReleaseBundle ||
		approval.PreviewReceipt != parsed.PreviewReceipt || preview.ReleaseBundle != parsed.ReleaseBundle ||
		bundle.ID != parsed.ReleaseBundle.ID || bundle.BundleHash != parsed.ReleaseBundle.ContentHash {
		return ProductionReceipt{}, fmt.Errorf("%w: ProductionReceipt authority mismatch", ErrBundleIntegrity)
	}
	return parsed, nil
}

func (store *Store) hydrateDeploymentRevision(ctx context.Context, row deploymentRevisionRow) (DeploymentRevision, error) {
	var value DeploymentRevision
	if err := store.hydrateDeliveryFact(ctx, row.ProjectID, deploymentRevisionAggregate, row.ID,
		row.ContentStore, row.ContentRef, row.ContentHash, deliveryFactSchemaNumber(row.SchemaVersion), &value); err != nil {
		return DeploymentRevision{}, err
	}
	parsed, err := ParseDeploymentRevision(value)
	var projectedChecks []PreviewCheck
	projectionErr := json.Unmarshal(row.Checks, &projectedChecks)
	if err != nil || parsed.ID != row.ID || parsed.RunID != row.RunID || parsed.ProjectID != row.ProjectID ||
		string(parsed.Operation) != row.Operation || parsed.ReleaseBundle.ID != row.ReleaseBundleID ||
		parsed.ReleaseBundle.ContentHash != row.ReleaseBundleHash || parsed.PreviewReceipt.ID != row.PreviewReceiptID ||
		parsed.PreviewReceipt.ContentHash != row.PreviewReceiptHash || parsed.Approval.ID != row.PromotionApprovalID ||
		parsed.Approval.ContentHash != row.PromotionApprovalHash || parsed.ProductionReceipt.ID != row.ProductionReceiptID ||
		parsed.ProductionReceipt.ContentHash != row.ProductionReceiptHash || parsed.Provider != row.Provider ||
		parsed.ProviderRef != row.ProviderRef || parsed.PublicURL != row.PublicURL ||
		parsed.SchemaVersion != row.SchemaVersion ||
		!sameControllerOperationProjection(parsed.ControllerOperation, row.ControllerOperationID, row.ControllerResultHash) ||
		parsed.PayloadHash != row.PayloadHash ||
		parsed.CreatedBy != row.CreatedBy || !parsed.CreatedAt.Equal(row.CreatedAt) ||
		projectionErr != nil || !samePreviewChecks(parsed.Checks, projectedChecks) ||
		!sameOptionalReference(row.SourceRevisionID, row.SourceRevisionHash, parsed.SourceRevision) {
		return DeploymentRevision{}, fmt.Errorf("%w: DeploymentRevision projection mismatch", ErrBundleIntegrity)
	}
	approval, approvalErr := store.GetPromotionApproval(ctx, row.ProjectID, row.PromotionApprovalID, row.PromotionApprovalHash)
	preview, previewErr := store.GetPreviewReceipt(ctx, row.ProjectID, row.PreviewReceiptID, row.PreviewReceiptHash)
	bundle, bundleErr := store.Get(ctx, row.ProjectID, row.ReleaseBundleID, row.ReleaseBundleHash)
	receipt, receiptErr := store.GetProductionReceipt(ctx, row.ProjectID, row.ProductionReceiptID, row.ProductionReceiptHash)
	if approvalErr != nil || previewErr != nil || bundleErr != nil || receiptErr != nil ||
		receipt.Decision != PreviewPassed || receipt.RunID != parsed.RunID || receipt.ReleaseBundle != parsed.ReleaseBundle ||
		receipt.PreviewReceipt != parsed.PreviewReceipt || receipt.Approval != parsed.Approval ||
		approval.PreviewReceipt != parsed.PreviewReceipt || preview.ReleaseBundle != parsed.ReleaseBundle || bundle.ID != parsed.ReleaseBundle.ID {
		return DeploymentRevision{}, fmt.Errorf("%w: DeploymentRevision authority mismatch", ErrBundleIntegrity)
	}
	return parsed, nil
}

func (store *Store) hydrateDeliveryFact(
	ctx context.Context,
	projectID, aggregateType, aggregateID, contentStoreName, contentRef, contentHash string,
	schemaVersion int,
	target any,
) error {
	stored, err := store.contents.Get(ctx, contentRef, contentHash)
	if err != nil {
		return fmt.Errorf("%w: load %s content: %v", ErrBundleIntegrity, aggregateType, err)
	}
	if contentStoreName != "mongo" || stored.ID != contentRef || stored.ProjectID != projectID ||
		stored.AggregateType != aggregateType || stored.AggregateID != aggregateID ||
		stored.SchemaVersion != schemaVersion ||
		(stored.State != content.StatePending && stored.State != content.StateFinalized) || stored.ContentHash != contentHash {
		return fmt.Errorf("%w: %s content identity", ErrBundleIntegrity, aggregateType)
	}
	if err := json.Unmarshal(stored.Payload, target); err != nil {
		return fmt.Errorf("%w: decode %s content: %v", ErrBundleIntegrity, aggregateType, err)
	}
	return nil
}

func deliveryFactSchemaNumber(schemaVersion string) int {
	if strings.HasSuffix(schemaVersion, "/v2") {
		return 2
	}
	return 1
}

func sameControllerOperationProjection(
	value *ControllerOperationResultReference,
	operationID, resultHash *string,
) bool {
	if value == nil {
		return operationID == nil && resultHash == nil
	}
	return operationID != nil && resultHash != nil &&
		value.OperationID == *operationID && value.ResultHash == *resultHash
}

func repositoryReference(id, hash string) repository.ExactReference {
	return repository.ExactReference{ID: id, ContentHash: hash}
}

func referencePointer(id, hash string) *repository.ExactReference {
	value := repositoryReference(id, hash)
	return &value
}

func optionalReference(id, hash *string) *repository.ExactReference {
	if id == nil || hash == nil {
		return nil
	}
	return referencePointer(*id, *hash)
}

func sameOptionalReference(id, hash *string, value *repository.ExactReference) bool {
	if id == nil || hash == nil {
		return id == nil && hash == nil && value == nil
	}
	return value != nil && value.ID == *id && value.ContentHash == *hash
}
