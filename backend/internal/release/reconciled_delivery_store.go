package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"gorm.io/gorm"
)

const requiredDeliveryReconciliationMigration = "000061_release_delivery_run_operation_authority"

// ReconciledDeliveryStore is the only write authority for new delivery Runs.
// The controller identity is supplied by process configuration and cannot be
// injected through an API request. Historical Store read methods are embedded
// so v1 evidence remains readable without remaining executable.
type ReconciledDeliveryStore struct {
	*Store
	controller    DeliveryControllerIdentity
	claimSequence atomic.Uint64
}

func NewReconciledDeliveryStore(
	store *Store,
	controller DeliveryControllerIdentity,
) (*ReconciledDeliveryStore, error) {
	if store == nil || store.database == nil {
		return nil, errors.New("release reconciled delivery store is required")
	}
	parsed, err := validateDeliveryControllerForStore(controller)
	if err != nil {
		return nil, err
	}
	return &ReconciledDeliveryStore{Store: store, controller: parsed}, nil
}

func (store *ReconciledDeliveryStore) Readiness(ctx context.Context) error {
	if store == nil || store.database == nil || ctx == nil {
		return errors.New("reconciled release delivery store is not configured")
	}
	var migrationApplied, operationsPresent, resultsPresent, previewSingleFlightPresent, reconciliationCasesPresent bool
	var authorityTriggersPresent, atomicCaseGuardPresent, runOperationGuardsDeferred bool
	var legacyGateFunctionPresent, v2ProjectLockPresent, nestedAuthorityFunctionPresent bool
	if err := store.database.WithContext(ctx).Raw(`
SELECT
  EXISTS (SELECT 1 FROM schema_migrations WHERE version = ?),
  to_regclass('release_delivery_operations') IS NOT NULL,
  to_regclass('release_delivery_operation_results') IS NOT NULL,
  to_regclass('release_preview_runs_one_nonterminal_bundle_idx') IS NOT NULL,
  to_regclass('release_delivery_reconciliation_cases') IS NOT NULL,
  NOT EXISTS (
    SELECT 1
    FROM (VALUES
      ('release_delivery_operations', 'release_delivery_operation_insert_guard', 'validate_release_delivery_operation_insert()'),
      ('release_delivery_operations', 'release_delivery_operation_mutation_guard', 'validate_release_delivery_operation_mutation()'),
      ('release_delivery_operations', 'release_delivery_operation_nested_authority_guard', 'validate_release_delivery_nested_authority_insert()'),
      ('release_delivery_operation_attempts', 'release_delivery_operation_attempt_00_reconcile_only_guard', 'prevent_resubmit_after_delivery_reconciliation()'),
      ('release_delivery_operation_attempts', 'release_delivery_operation_attempt_insert_guard', 'validate_release_delivery_operation_attempt_insert()'),
      ('release_delivery_operation_attempts', 'release_delivery_operation_attempt_mutation_guard', 'validate_release_delivery_operation_attempt_mutation()'),
      ('release_delivery_operation_attempts', 'release_delivery_operation_attempt_projection', 'project_release_delivery_operation_attempt_insert()'),
      ('release_delivery_operation_results', 'release_delivery_operation_result_insert_guard', 'validate_release_delivery_operation_result_insert()'),
      ('release_delivery_operation_results', 'release_delivery_operation_result_immutable', 'prevent_release_delivery_operation_result_mutation()'),
      ('release_preview_receipts', 'release_preview_receipt_operation_authority_guard', 'validate_release_delivery_fact_operation_authority_insert()'),
      ('release_production_receipts', 'release_production_receipt_operation_authority_guard', 'validate_release_delivery_fact_operation_authority_insert()'),
      ('release_deployment_revisions', 'release_deployment_revision_operation_authority_guard', 'validate_release_delivery_fact_operation_authority_insert()'),
      ('release_preview_runs', 'release_preview_run_v2_insert_guard', 'validate_release_delivery_run_insert_v2()'),
      ('release_preview_runs', 'release_preview_run_update_guard', 'validate_release_delivery_run_update()'),
      ('release_preview_runs', 'release_preview_run_operation_authority_guard', 'validate_release_delivery_run_operation_authority()'),
      ('release_deployment_runs', 'release_deployment_run_v2_insert_guard', 'validate_release_delivery_run_insert_v2()'),
      ('release_deployment_runs', 'release_deployment_run_update_guard', 'validate_release_delivery_run_update()'),
      ('release_deployment_runs', 'release_deployment_run_operation_authority_guard', 'validate_release_delivery_run_operation_authority()'),
      ('release_delivery_reconciliation_cases', 'release_delivery_reconciliation_case_insert_guard', 'validate_release_delivery_reconciliation_case_insert()'),
      ('release_delivery_reconciliation_cases', 'release_delivery_reconciliation_case_applied_guard', 'validate_release_delivery_reconciliation_case_applied()'),
      ('release_delivery_reconciliation_cases', 'release_delivery_reconciliation_case_immutable', 'prevent_release_delivery_reconciliation_case_mutation()'),
      ('deployment_versions', 'deployment_version_controller_singleflight_guard', 'validate_legacy_deployment_version_controller_gate()')
    ) AS expected(table_name, trigger_name, function_name)
    WHERE NOT EXISTS (
      SELECT 1
      FROM pg_trigger AS installed
      WHERE installed.tgrelid = to_regclass(expected.table_name)
        AND installed.tgname = expected.trigger_name
        AND NOT installed.tgisinternal
        AND installed.tgenabled IN ('O','A')
        AND installed.tgfoid = to_regprocedure(expected.function_name)
    )
  ),
  EXISTS (
    SELECT 1 FROM pg_trigger
    WHERE tgrelid = to_regclass('release_delivery_reconciliation_cases')
      AND tgname = 'release_delivery_reconciliation_case_applied_guard'
      AND NOT tgisinternal AND tgenabled IN ('O','A')
      AND tgdeferrable AND tginitdeferred
  ),
  (
    SELECT count(*) = 2
    FROM pg_trigger
    WHERE (
        (tgrelid = to_regclass('release_preview_runs')
          AND tgname = 'release_preview_run_operation_authority_guard')
        OR
        (tgrelid = to_regclass('release_deployment_runs')
          AND tgname = 'release_deployment_run_operation_authority_guard')
      )
      AND NOT tgisinternal AND tgenabled IN ('O','A')
      AND tgdeferrable AND tginitdeferred
      AND tgfoid = to_regprocedure('validate_release_delivery_run_operation_authority()')
  ),
  COALESCE(
    position(
      'FOR UPDATE' IN pg_get_functiondef(
        to_regprocedure('validate_legacy_deployment_version_controller_gate()')
      )
    ) > 0
    AND position(
      'legacy production deployment is disabled' IN pg_get_functiondef(
        to_regprocedure('validate_legacy_deployment_version_controller_gate()')
      )
    ) > 0,
    false
  ),
  COALESCE(
    position(
      'FOR UPDATE' IN pg_get_functiondef(
        to_regprocedure('validate_release_delivery_run_insert_v2()')
      )
    ) > 0
    AND position(
      'status = ''deploying''' IN pg_get_functiondef(
        to_regprocedure('validate_release_delivery_run_insert_v2()')
      )
    ) > 0,
    false
  ),
  COALESCE(
    position(
      'release_delivery_canonical_json' IN pg_get_functiondef(
        to_regprocedure('release_delivery_embedded_hash_is_exact(jsonb,text)')
      )
    ) > 0
    AND position(
      'jsonb_set' IN pg_get_functiondef(
        to_regprocedure('release_delivery_embedded_hash_is_exact(jsonb,text)')
      )
    ) > 0,
    false
  )
`, requiredDeliveryReconciliationMigration).Row().Scan(
		&migrationApplied, &operationsPresent, &resultsPresent, &previewSingleFlightPresent,
		&reconciliationCasesPresent, &authorityTriggersPresent, &atomicCaseGuardPresent,
		&runOperationGuardsDeferred, &legacyGateFunctionPresent, &v2ProjectLockPresent,
		&nestedAuthorityFunctionPresent,
	); err != nil {
		return fmt.Errorf("inspect reconciled release delivery schema: %w", err)
	}
	if !migrationApplied || !operationsPresent || !resultsPresent || !previewSingleFlightPresent ||
		!reconciliationCasesPresent || !authorityTriggersPresent || !atomicCaseGuardPresent ||
		!runOperationGuardsDeferred || !legacyGateFunctionPresent || !v2ProjectLockPresent ||
		!nestedAuthorityFunctionPresent {
		return fmt.Errorf("reconciled release delivery requires migration %s", requiredDeliveryReconciliationMigration)
	}
	var legacyAuthoritySplit, crossWriterConflict, orphanV2Run bool
	if err := store.database.WithContext(ctx).Raw(`
SELECT
EXISTS (
  SELECT 1
  FROM deployment_versions AS version
  JOIN deployments AS deployment ON deployment.id = version.deployment_id
  WHERE version.status = 'deploying'
    AND deployment.status <> 'deploying'
),
EXISTS (
  SELECT 1
  FROM deployments AS deployment
  WHERE (
      deployment.status = 'deploying'
      OR EXISTS (
        SELECT 1 FROM deployment_versions AS version
        WHERE version.deployment_id = deployment.id
          AND version.status = 'deploying'
      )
    )
    AND (
      EXISTS (
        SELECT 1
        FROM release_preview_runs AS preview
        WHERE preview.project_id = deployment.project_id
          AND preview.schema_version = 'release-preview-run/v2'
          AND preview.state IN (
            'queued','claimed','submitting','reconcile_wait','reconciling',
            'verifying','reconcile_blocked'
          )
      )
      OR EXISTS (
        SELECT 1
        FROM release_deployment_runs AS production
        WHERE production.project_id = deployment.project_id
          AND production.schema_version = 'release-deployment-run/v2'
          AND production.state IN (
            'queued','claimed','submitting','reconcile_wait','reconciling',
            'verifying','reconcile_blocked'
          )
      )
    )
),
EXISTS (
  SELECT 1
  FROM release_preview_runs AS preview
  WHERE preview.schema_version = 'release-preview-run/v2'
    AND (
      SELECT count(*)
      FROM release_delivery_operations AS operation
      WHERE operation.preview_run_id = preview.id
        AND operation.deployment_run_id IS NULL
        AND operation.kind = 'preview'
        AND operation.project_id = preview.project_id
    ) <> 1
  UNION ALL
  SELECT 1
  FROM release_deployment_runs AS production
  WHERE production.schema_version = 'release-deployment-run/v2'
    AND (
      SELECT count(*)
      FROM release_delivery_operations AS operation
      WHERE operation.deployment_run_id = production.id
        AND operation.preview_run_id IS NULL
        AND operation.kind = 'production'
        AND operation.project_id = production.project_id
    ) <> 1
)
`).Row().Scan(&legacyAuthoritySplit, &crossWriterConflict, &orphanV2Run); err != nil {
		return fmt.Errorf("inspect legacy and controller release authority: %w", err)
	}
	if legacyAuthoritySplit {
		return errors.New("a deploying legacy version has a non-deploying parent; reconcile the project before serving release mutations")
	}
	if crossWriterConflict {
		return errors.New("deploying legacy and active Release Controller v3 authority coexist; reconcile the project before serving release mutations")
	}
	if orphanV2Run {
		return errors.New("a release delivery v2 Run is missing its exact Operation authority; reconcile it before serving release mutations")
	}
	var activeIdentityDrift bool
	if err := store.database.WithContext(ctx).Raw(`
SELECT EXISTS (
  SELECT 1
  FROM release_delivery_operations AS operation
  LEFT JOIN release_preview_runs AS preview ON preview.id = operation.preview_run_id
  LEFT JOIN release_deployment_runs AS production ON production.id = operation.deployment_run_id
  WHERE (
    preview.state IN ('queued','claimed','submitting','reconcile_wait','reconciling','verifying','reconcile_blocked')
    OR production.state IN ('queued','claimed','submitting','reconcile_wait','reconciling','verifying','reconcile_blocked')
  )
  AND (
    operation.controller_schema_version <> ?
    OR operation.controller_id <> ?
    OR operation.controller_version <> ?
    OR operation.controller_protocol <> ?
    OR operation.controller_trust_key_digest <> ?
  )
)
`, store.controller.SchemaVersion, store.controller.ID, store.controller.Version,
		store.controller.Protocol, store.controller.TrustKeyDigest).Row().Scan(&activeIdentityDrift); err != nil {
		return fmt.Errorf("inspect active release delivery controller authority: %w", err)
	}
	if activeIdentityDrift {
		return errors.New("active release delivery Operation is pinned to a different controller authority; reconcile it before rotation")
	}
	return nil
}

type deliveryOperationRow struct {
	ID                       string  `gorm:"column:id"`
	ProjectID                string  `gorm:"column:project_id"`
	Kind                     string  `gorm:"column:kind"`
	PreviewRunID             *string `gorm:"column:preview_run_id"`
	DeploymentRunID          *string `gorm:"column:deployment_run_id"`
	RequestSchemaVersion     string  `gorm:"column:request_schema_version"`
	RequestDocument          string  `gorm:"column:request_document"`
	RequestHash              string  `gorm:"column:request_hash"`
	ControllerSchemaVersion  string  `gorm:"column:controller_schema_version"`
	ControllerID             string  `gorm:"column:controller_id"`
	ControllerVersion        string  `gorm:"column:controller_version"`
	ControllerProtocol       string  `gorm:"column:controller_protocol"`
	ControllerTrustKeyDigest string  `gorm:"column:controller_trust_key_digest"`
	RemoteState              string  `gorm:"column:remote_state"`
	TerminalResultHash       *string `gorm:"column:terminal_result_hash"`
	ReconcileOnly            bool    `gorm:"column:reconcile_only;->"`
}

func (deliveryOperationRow) TableName() string { return "release_delivery_operations" }

func (store *ReconciledDeliveryStore) CreatePreviewRun(
	ctx context.Context,
	input CreatePreviewRunInput,
) (PreviewRun, bool, error) {
	bundle, err := ParseBundle(input.BundleDocument)
	if err != nil || bundle.ID != input.ReleaseBundle.ID || bundle.BundleHash != input.ReleaseBundle.ContentHash ||
		bundle.ProjectID != input.ProjectID || bundle.CreatedBy == "" {
		return PreviewRun{}, false, invalid("preview delivery exact Bundle document")
	}
	operation, err := newPreviewDeliveryOperationRequest(
		input.ID, bundle, input.Reason, deterministicPreviewNamespace(input.ProjectID, bundle.ID),
	)
	if err != nil {
		return PreviewRun{}, false, err
	}
	var row previewRunRow
	replayed := false
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		result := transaction.Exec(`
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES (?, 'release-preview-run/v2', ?, ?, ?, ?, ?, ?, 'queued', 1, ?, ?)
ON CONFLICT (project_id, request_key) DO NOTHING
`, input.ID, input.ProjectID, input.ReleaseBundle.ID, input.ReleaseBundle.ContentHash,
			input.RequestKey, input.RequestHash, input.Reason, input.CreatedBy, input.CreatedBy)
		if result.Error != nil {
			return result.Error
		}
		replayed = result.RowsAffected == 0
		if err := transaction.Where("project_id = ? AND request_key = ?", input.ProjectID, input.RequestKey).
			Take(&row).Error; err != nil {
			return err
		}
		if !samePreviewRunRequest(row, input) {
			return ErrBundleConflict
		}
		if replayed {
			return store.verifyStoredDeliveryOperation(transaction, row.ID, DeliveryOperationPreview, operation)
		}
		return store.insertDeliveryOperation(transaction, operation, input.CreatedBy, row.ID, "")
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isPreviewRunConflict(err) {
			return PreviewRun{}, false, fmt.Errorf("%w: another Run owns the deterministic preview namespace", ErrPreviewRunConflict)
		}
		return PreviewRun{}, false, err
	}
	return store.previewRunFromRow(ctx, row), replayed, nil
}

func (store *ReconciledDeliveryStore) CreateProductionRun(
	ctx context.Context,
	input CreateProductionRunInput,
) (ProductionRun, bool, error) {
	environment := strings.TrimSpace(input.Environment)
	if environment == "" {
		environment = "production"
	}
	if err := validateProductionDeliveryDocuments(input); err != nil {
		return ProductionRun{}, false, err
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
			if !sameProductionRunRequest(existing, input, environment) {
				return ErrBundleConflict
			}
			row, replayed = existing, true
			return store.verifyStoredDeliveryOperationByRun(transaction, row.ID, DeliveryOperationProduction)
		}
		if !errors.Is(existingResult.Error, gorm.ErrRecordNotFound) {
			return existingResult.Error
		}

		expected := ExpectedProductionHead{
			Revision:          optionalReference(head.DeploymentRevisionID, head.DeploymentRevisionHash),
			ProductionReceipt: optionalReference(head.ProductionReceiptID, head.ProductionReceiptHash),
		}
		operation, err := newProductionDeliveryOperationRequest(
			input.ID, environment, input.Reason, input.Operation,
			input.BundleDocument, input.PreviewDocument, input.ApprovalDocument,
			input.SourceDocument, expected,
		)
		if err != nil {
			return err
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
) VALUES (?, 'release-deployment-run/v2', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', 1, ?, ?)
`, input.ID, input.ProjectID, environment, input.Operation,
			input.ReleaseBundle.ID, input.ReleaseBundle.ContentHash,
			input.PreviewReceipt.ID, input.PreviewReceipt.ContentHash,
			input.PromotionApproval.ID, input.PromotionApproval.ContentHash,
			sourceID, sourceHash,
			head.DeploymentRevisionID, head.DeploymentRevisionHash,
			head.ProductionReceiptID, head.ProductionReceiptHash,
			input.RequestKey, input.RequestHash, input.Reason, input.CreatedBy, input.CreatedBy)
		if result.Error != nil {
			return result.Error
		}
		if err := transaction.Where("id = ?", input.ID).Take(&row).Error; err != nil {
			return err
		}
		return store.insertDeliveryOperation(transaction, operation, input.CreatedBy, "", row.ID)
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isProductionHeadConflict(err) {
			return ProductionRun{}, false, fmt.Errorf("%w: %v", ErrProductionHeadConflict, err)
		}
		return ProductionRun{}, false, err
	}
	return store.productionRunFromRow(ctx, row), replayed, nil
}

func (store *ReconciledDeliveryStore) insertDeliveryOperation(
	transaction *gorm.DB,
	request DeliveryOperationRequest,
	createdBy, previewRunID, deploymentRunID string,
) error {
	request, err := ParseDeliveryOperationRequest(request)
	if err != nil {
		return err
	}
	var preview, deployment any
	if previewRunID != "" {
		preview = previewRunID
	}
	if deploymentRunID != "" {
		deployment = deploymentRunID
	}
	result := transaction.Exec(`
INSERT INTO release_delivery_operations (
  id, schema_version, project_id, kind, preview_run_id, deployment_run_id,
  request_schema_version, request_document, request_hash,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest,
  remote_state, created_by
) VALUES (?, 'release-delivery-operation/v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'prepared', ?)
`, request.OperationID, request.ProjectID, request.Kind, preview, deployment,
		request.SchemaVersion, string(request.RequestDocument), request.RequestHash,
		store.controller.SchemaVersion, store.controller.ID, store.controller.Version,
		store.controller.Protocol, store.controller.TrustKeyDigest, createdBy)
	return result.Error
}

func (store *ReconciledDeliveryStore) verifyStoredDeliveryOperation(
	transaction *gorm.DB,
	runID string,
	kind DeliveryOperationKind,
	expected DeliveryOperationRequest,
) error {
	var row deliveryOperationRow
	query := transaction.Where("kind = ?", kind)
	if kind == DeliveryOperationPreview {
		query = query.Where("preview_run_id = ?", runID)
	} else {
		query = query.Where("deployment_run_id = ?", runID)
	}
	if err := query.Take(&row).Error; err != nil {
		return fmt.Errorf("%w: exact delivery Operation is missing: %v", ErrBundleIntegrity, err)
	}
	actual, err := store.deliveryOperationRequestFromRow(row)
	if err != nil || actual.RequestHash != expected.RequestHash ||
		string(actual.RequestDocument) != string(expected.RequestDocument) {
		return ErrBundleConflict
	}
	return nil
}

func (store *ReconciledDeliveryStore) verifyStoredDeliveryOperationByRun(
	transaction *gorm.DB,
	runID string,
	kind DeliveryOperationKind,
) error {
	var row deliveryOperationRow
	query := transaction.Where("kind = ?", kind)
	if kind == DeliveryOperationPreview {
		query = query.Where("preview_run_id = ?", runID)
	} else {
		query = query.Where("deployment_run_id = ?", runID)
	}
	if err := query.Take(&row).Error; err != nil {
		return fmt.Errorf("%w: exact delivery Operation is missing: %v", ErrBundleIntegrity, err)
	}
	_, err := store.deliveryOperationRequestFromRow(row)
	return err
}

func (store *ReconciledDeliveryStore) deliveryOperationRequestFromRow(
	row deliveryOperationRow,
) (DeliveryOperationRequest, error) {
	identity := DeliveryControllerIdentity{
		SchemaVersion:  row.ControllerSchemaVersion,
		ID:             row.ControllerID,
		Version:        row.ControllerVersion,
		Protocol:       row.ControllerProtocol,
		TrustKeyDigest: row.ControllerTrustKeyDigest,
	}
	if identity != store.controller {
		return DeliveryOperationRequest{}, fmt.Errorf("%w: delivery controller authority drift", ErrBundleIntegrity)
	}
	request := DeliveryOperationRequest{
		SchemaVersion:   row.RequestSchemaVersion,
		OperationID:     row.ID,
		Kind:            DeliveryOperationKind(row.Kind),
		ProjectID:       row.ProjectID,
		RequestHash:     row.RequestHash,
		RequestDocument: append(json.RawMessage(nil), []byte(row.RequestDocument)...),
	}
	return ParseDeliveryOperationRequest(request)
}

func samePreviewRunRequest(row previewRunRow, input CreatePreviewRunInput) bool {
	return row.ID == input.ID && row.ProjectID == input.ProjectID &&
		row.ReleaseBundleID == input.ReleaseBundle.ID &&
		row.ReleaseBundleHash == input.ReleaseBundle.ContentHash &&
		row.RequestHash == input.RequestHash && row.Reason == input.Reason &&
		row.CreatedBy == input.CreatedBy
}

func sameProductionRunRequest(row productionRunRow, input CreateProductionRunInput, environment string) bool {
	return row.ID == input.ID && row.ProjectID == input.ProjectID &&
		row.Operation == string(input.Operation) && row.Environment == environment &&
		row.ReleaseBundleID == input.ReleaseBundle.ID && row.ReleaseBundleHash == input.ReleaseBundle.ContentHash &&
		row.PreviewReceiptID == input.PreviewReceipt.ID && row.PreviewReceiptHash == input.PreviewReceipt.ContentHash &&
		row.PromotionApprovalID == input.PromotionApproval.ID &&
		row.PromotionApprovalHash == input.PromotionApproval.ContentHash &&
		row.RequestHash == input.RequestHash && row.Reason == input.Reason && row.CreatedBy == input.CreatedBy &&
		sameOptionalReference(row.SourceRevisionID, row.SourceRevisionHash, input.SourceRevision)
}

func validateProductionDeliveryDocuments(input CreateProductionRunInput) error {
	bundle, err := ParseBundle(input.BundleDocument)
	if err != nil || bundle.ProjectID != input.ProjectID || bundle.ID != input.ReleaseBundle.ID ||
		bundle.BundleHash != input.ReleaseBundle.ContentHash {
		return invalid("production delivery exact Bundle document")
	}
	preview, err := ParsePreviewReceipt(input.PreviewDocument)
	if err != nil || preview.ID != input.PreviewReceipt.ID || preview.PayloadHash != input.PreviewReceipt.ContentHash {
		return invalid("production delivery exact PreviewReceipt document")
	}
	approval, err := ParsePromotionApproval(input.ApprovalDocument)
	if err != nil || approval.ID != input.PromotionApproval.ID || approval.PayloadHash != input.PromotionApproval.ContentHash {
		return invalid("production delivery exact PromotionApproval document")
	}
	if (input.SourceRevision == nil) != (input.SourceDocument == nil) {
		return invalid("production delivery source document")
	}
	if input.SourceDocument != nil {
		source, parseErr := ParseDeploymentRevision(*input.SourceDocument)
		if parseErr != nil || source.ID != input.SourceRevision.ID || source.PayloadHash != input.SourceRevision.ContentHash {
			return invalid("production delivery exact source DeploymentRevision document")
		}
	}
	return nil
}
