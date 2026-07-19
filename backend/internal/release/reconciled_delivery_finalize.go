package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

func (store *ReconciledDeliveryStore) finalizeReconciledDeliveryOperation(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
) error {
	operation, result, err := store.loadCompletedDeliveryResult(ctx, claim)
	if err != nil {
		return err
	}
	if claim.RunState != DeliveryVerifying {
		// A worker which observed completion can finalize immediately using its
		// still-live fence even though its in-memory claim predates the SQL
		// submitting/reconciling -> verifying transition.
		claim.RunState = DeliveryVerifying
	}
	if result.Kind == DeliveryOperationPreview {
		if claim.Preview == nil || operation.PreviewRunID == nil || *operation.PreviewRunID != claim.Preview.Run.ID {
			return fmt.Errorf("%w: completed preview result lost its Run", ErrBundleIntegrity)
		}
		_, err = store.completeReconciledPreview(ctx, claim, result)
		return err
	}
	if claim.Production == nil || operation.DeploymentRunID == nil || *operation.DeploymentRunID != claim.Production.Run.ID {
		return fmt.Errorf("%w: completed production result lost its Run", ErrBundleIntegrity)
	}
	_, err = store.completeReconciledProduction(ctx, claim, result)
	return err
}

func (store *ReconciledDeliveryStore) loadCompletedDeliveryResult(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
) (deliveryOperationRow, DeliveryOperationResult, error) {
	var operation deliveryOperationRow
	if err := store.database.WithContext(ctx).
		Where("id = ? AND request_hash = ? AND remote_state = 'completed'", claim.Request.OperationID, claim.Request.RequestHash).
		Take(&operation).Error; err != nil {
		return deliveryOperationRow{}, DeliveryOperationResult{}, err
	}
	if operation.TerminalResultHash == nil {
		return deliveryOperationRow{}, DeliveryOperationResult{}, fmt.Errorf("%w: completed Operation has no Result", ErrBundleIntegrity)
	}
	var row deliveryOperationResultRow
	if err := store.database.WithContext(ctx).
		Where("operation_id = ? AND result_hash = ?", operation.ID, *operation.TerminalResultHash).
		Take(&row).Error; err != nil {
		return deliveryOperationRow{}, DeliveryOperationResult{}, err
	}
	var value DeliveryOperationResult
	if err := decodeReleaseStrictJSON([]byte(row.ResultDocument), &value); err != nil {
		return deliveryOperationRow{}, DeliveryOperationResult{}, fmt.Errorf("%w: decode exact controller Result: %v", ErrBundleIntegrity, err)
	}
	parsed, err := ParseDeliveryOperationResult(value, store.controller, claim.Request)
	if err != nil || !sameDeliveryResultProjection(parsed, row) {
		return deliveryOperationRow{}, DeliveryOperationResult{}, fmt.Errorf("%w: controller Result projection mismatch", ErrBundleIntegrity)
	}
	return operation, parsed, nil
}

func (store *ReconciledDeliveryStore) completeReconciledPreview(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	result DeliveryOperationResult,
) (PreviewRun, error) {
	preview := claim.Preview
	if preview == nil {
		return PreviewRun{}, invalid("completed preview claim")
	}
	decision := PreviewPassed
	if !previewChecksPassed(result.Checks) {
		decision = PreviewFailed
	}
	controller := ControllerOperationResultReference{
		OperationID: result.OperationID,
		ResultHash:  result.ResultHash,
	}
	receipt, err := NewPreviewReceiptV2(NewPreviewReceiptInput{
		ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("preview-receipt\x00"+preview.Run.ID)).String(),
		RunID: preview.Run.ID, Bundle: preview.Bundle,
		Namespace: deterministicPreviewNamespace(preview.Run.ProjectID, preview.Bundle.ID),
		Provider:  result.Provider, ProviderRef: result.ProviderRef, Checks: result.Checks,
		Decision: decision, CreatedBy: preview.Run.CreatedBy, CreatedAt: result.CompletedAt,
	}, controller)
	if err != nil {
		return PreviewRun{}, err
	}
	payload, err := domain.CanonicalJSON(receipt)
	if err != nil {
		return PreviewRun{}, err
	}
	contentRef, err := store.contents.PutPending(
		ctx, receipt.ProjectID, previewReceiptAggregate, receipt.ID, 2, payload,
	)
	if err != nil {
		return PreviewRun{}, err
	}
	abort := true
	defer func() {
		if abort {
			_ = store.contents.Abort(context.Background(), contentRef.ID)
		}
	}()
	artifacts, _ := json.Marshal(receipt.ReleaseArtifacts)
	checks, _ := json.Marshal(receipt.Checks)
	terminal := DeliveryRunState(receipt.Decision)
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		insert := transaction.Exec(`
INSERT INTO release_preview_receipts (
  id, schema_version, run_id, project_id, release_bundle_id, release_bundle_hash,
  canonical_receipt_id, canonical_receipt_hash,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  release_artifacts, namespace, provider, provider_ref, checks, decision,
  controller_operation_id, controller_result_hash,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (?, 'release-preview-receipt/v2', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb,
          ?, ?, ?, ?::jsonb, ?, ?, ?, 'mongo', ?, ?, ?, ?, ?)
`, receipt.ID, receipt.RunID, receipt.ProjectID, receipt.ReleaseBundle.ID, receipt.ReleaseBundle.ContentHash,
			receipt.CanonicalReceipt.ID, receipt.CanonicalReceipt.ContentHash,
			receipt.Workspace.WorkspaceArtifactID, receipt.Workspace.WorkspaceRevisionID, receipt.Workspace.WorkspaceContentHash,
			string(artifacts), receipt.Namespace, receipt.Provider, receipt.ProviderRef, string(checks), receipt.Decision,
			controller.OperationID, controller.ResultHash,
			contentRef.ID, contentRef.ContentHash, receipt.PayloadHash, receipt.CreatedBy, receipt.CreatedAt)
		if insert.Error != nil {
			return insert.Error
		}
		update := transaction.Exec(`
UPDATE release_preview_runs
SET state = ?, version = version + 1,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    finished_at = statement_timestamp(),
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ? AND state = 'verifying'
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
`, terminal, preview.Run.ID, preview.Run.ProjectID, preview.Lease.WorkerID, preview.Lease.Epoch)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrDeliveryFence
		}
		return nil
	})
	if err != nil {
		return PreviewRun{}, err
	}
	abort = false
	if err := store.contents.Finalize(ctx, contentRef.ID); err != nil {
		return PreviewRun{}, fmt.Errorf("%w: finalize v2 PreviewReceipt: %v", core.ErrContentNotReady, err)
	}
	return store.GetPreviewRun(ctx, preview.Run.ProjectID, preview.Run.ID)
}

func (store *ReconciledDeliveryStore) completeReconciledProduction(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	result DeliveryOperationResult,
) (ProductionRun, error) {
	production := claim.Production
	if production == nil {
		return ProductionRun{}, invalid("completed production claim")
	}
	decision := PreviewPassed
	if !previewChecksPassed(result.Checks) {
		decision = PreviewFailed
	}
	createdAt := result.CompletedAt
	controller := ControllerOperationResultReference{
		OperationID: result.OperationID,
		ResultHash:  result.ResultHash,
	}
	receipt, err := NewProductionReceiptV2(NewProductionReceiptInput{
		ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("production-receipt\x00"+production.Run.ID)).String(),
		RunID: production.Run.ID, Bundle: production.Bundle, Preview: production.Preview, Approval: production.Approval,
		Operation: production.Run.Operation, SourceRevision: production.Run.SourceRevision,
		Provider: result.Provider, ProviderRef: result.ProviderRef, PublicURL: result.PublicURL,
		Checks: result.Checks, Decision: decision,
		CreatedBy: production.Run.CreatedBy, CreatedAt: createdAt,
	}, controller)
	if err != nil {
		return ProductionRun{}, err
	}
	receiptPayload, err := domain.CanonicalJSON(receipt)
	if err != nil {
		return ProductionRun{}, err
	}
	receiptContent, err := store.contents.PutPending(
		ctx, receipt.ProjectID, productionReceiptAggregate, receipt.ID, 2, receiptPayload,
	)
	if err != nil {
		return ProductionRun{}, err
	}
	abortReceipt := true
	defer func() {
		if abortReceipt {
			_ = store.contents.Abort(context.Background(), receiptContent.ID)
		}
	}()

	var revision DeploymentRevision
	var revisionContentID, revisionContentHash string
	abortRevision := false
	if decision == PreviewPassed {
		revision, err = NewDeploymentRevisionV2(NewDeploymentRevisionInput{
			ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("deployment-revision\x00"+production.Run.ID)).String(),
			RunID: production.Run.ID, Bundle: production.Bundle, Preview: production.Preview, Approval: production.Approval,
			Receipt: receipt, Operation: production.Run.Operation, SourceRevision: production.Run.SourceRevision,
			Provider: result.Provider, ProviderRef: result.ProviderRef, PublicURL: result.PublicURL,
			Checks: result.Checks, CreatedBy: production.Run.CreatedBy, CreatedAt: createdAt,
		}, controller)
		if err != nil {
			return ProductionRun{}, err
		}
		revisionPayload, marshalErr := domain.CanonicalJSON(revision)
		if marshalErr != nil {
			return ProductionRun{}, marshalErr
		}
		revisionContent, putErr := store.contents.PutPending(
			ctx, revision.ProjectID, deploymentRevisionAggregate, revision.ID, 2, revisionPayload,
		)
		if putErr != nil {
			return ProductionRun{}, putErr
		}
		revisionContentID, revisionContentHash = revisionContent.ID, revisionContent.ContentHash
		abortRevision = true
		defer func() {
			if abortRevision {
				_ = store.contents.Abort(context.Background(), revisionContentID)
			}
		}()
	}
	var sourceID, sourceHash any
	if receipt.SourceRevision != nil {
		sourceID, sourceHash = receipt.SourceRevision.ID, receipt.SourceRevision.ContentHash
	}
	checks, _ := json.Marshal(receipt.Checks)
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		insertReceipt := transaction.Exec(`
INSERT INTO release_production_receipts (
  id, schema_version, run_id, project_id, operation,
  release_bundle_id, release_bundle_hash, preview_receipt_id, preview_receipt_hash,
  promotion_approval_id, promotion_approval_hash, source_revision_id, source_revision_hash,
  provider, provider_ref, public_url, checks, decision,
  controller_operation_id, controller_result_hash,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (?, 'release-production-receipt/v2', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?,
          ?, ?, 'mongo', ?, ?, ?, ?, ?)
`, receipt.ID, receipt.RunID, receipt.ProjectID, receipt.Operation,
			receipt.ReleaseBundle.ID, receipt.ReleaseBundle.ContentHash,
			receipt.PreviewReceipt.ID, receipt.PreviewReceipt.ContentHash,
			receipt.Approval.ID, receipt.Approval.ContentHash, sourceID, sourceHash,
			receipt.Provider, receipt.ProviderRef, receipt.PublicURL, string(checks), receipt.Decision,
			controller.OperationID, controller.ResultHash,
			receiptContent.ID, receiptContent.ContentHash, receipt.PayloadHash, receipt.CreatedBy, receipt.CreatedAt)
		if insertReceipt.Error != nil {
			return insertReceipt.Error
		}
		if decision == PreviewPassed {
			insertRevision := transaction.Exec(`
INSERT INTO release_deployment_revisions (
  id, schema_version, run_id, project_id, operation,
  release_bundle_id, release_bundle_hash, preview_receipt_id, preview_receipt_hash,
  promotion_approval_id, promotion_approval_hash, production_receipt_id, production_receipt_hash,
  source_revision_id, source_revision_hash,
  provider, provider_ref, public_url, checks,
  controller_operation_id, controller_result_hash,
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (?, 'release-deployment-revision/v2', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb,
          ?, ?, 'mongo', ?, ?, ?, ?, ?)
`, revision.ID, revision.RunID, revision.ProjectID, revision.Operation,
				revision.ReleaseBundle.ID, revision.ReleaseBundle.ContentHash,
				revision.PreviewReceipt.ID, revision.PreviewReceipt.ContentHash,
				revision.Approval.ID, revision.Approval.ContentHash,
				revision.ProductionReceipt.ID, revision.ProductionReceipt.ContentHash,
				sourceID, sourceHash,
				revision.Provider, revision.ProviderRef, revision.PublicURL, string(checks),
				controller.OperationID, controller.ResultHash,
				revisionContentID, revisionContentHash, revision.PayloadHash, revision.CreatedBy, revision.CreatedAt)
			if insertRevision.Error != nil {
				return insertRevision.Error
			}
			var expectedRevisionID, expectedRevisionHash any
			var expectedReceiptID, expectedReceiptHash any
			if production.Run.ExpectedRevision != nil {
				expectedRevisionID = production.Run.ExpectedRevision.ID
				expectedRevisionHash = production.Run.ExpectedRevision.ContentHash
			}
			if production.Run.ExpectedReceipt != nil {
				expectedReceiptID = production.Run.ExpectedReceipt.ID
				expectedReceiptHash = production.Run.ExpectedReceipt.ContentHash
			}
			headUpdate := transaction.Exec(`
UPDATE release_production_heads
SET deployment_revision_id = ?, deployment_revision_hash = ?,
    production_receipt_id = ?, production_receipt_hash = ?,
    generation = generation + 1, updated_by = ?, updated_at = statement_timestamp()
WHERE project_id = ? AND environment = ?
  AND deployment_revision_id IS NOT DISTINCT FROM ?
  AND deployment_revision_hash IS NOT DISTINCT FROM ?
  AND production_receipt_id IS NOT DISTINCT FROM ?
  AND production_receipt_hash IS NOT DISTINCT FROM ?
`, revision.ID, revision.PayloadHash, receipt.ID, receipt.PayloadHash,
				production.Run.CreatedBy, production.Run.ProjectID, production.Run.Environment,
				expectedRevisionID, expectedRevisionHash, expectedReceiptID, expectedReceiptHash)
			if headUpdate.Error != nil {
				return headUpdate.Error
			}
			if headUpdate.RowsAffected != 1 {
				return ErrProductionHeadConflict
			}
		}
		terminalState := DeliveryFailed
		if decision == PreviewPassed {
			terminalState = DeliveryHealthy
		}
		update := transaction.Exec(`
UPDATE release_deployment_runs
SET state = ?, version = version + 1,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL,
    finished_at = statement_timestamp(),
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ? AND state = 'verifying'
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
`, terminalState, production.Run.ID, production.Run.ProjectID,
			production.Lease.WorkerID, production.Lease.Epoch)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrDeliveryFence
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrProductionHeadConflict) || isProductionHeadConflict(err) {
			return ProductionRun{}, fmt.Errorf("%w: %v", ErrProductionHeadConflict, err)
		}
		return ProductionRun{}, err
	}
	abortReceipt, abortRevision = false, false
	if err := store.contents.Finalize(ctx, receiptContent.ID); err != nil {
		return ProductionRun{}, fmt.Errorf("%w: finalize v2 ProductionReceipt: %v", core.ErrContentNotReady, err)
	}
	if revisionContentID != "" {
		if err := store.contents.Finalize(ctx, revisionContentID); err != nil {
			return ProductionRun{}, fmt.Errorf("%w: finalize v2 DeploymentRevision: %v", core.ErrContentNotReady, err)
		}
	}
	return store.GetProductionRun(ctx, production.Run.ProjectID, production.Run.ID)
}

func sameDeliveryResultProjection(value DeliveryOperationResult, row deliveryOperationResultRow) bool {
	if value.OperationID != row.OperationID || value.RequestHash != row.RequestHash ||
		value.ProjectID != row.ProjectID || string(value.Kind) != row.Kind || string(value.Status) != row.Status ||
		value.Controller.SchemaVersion != row.ControllerSchemaVersion || value.Controller.ID != row.ControllerID ||
		value.Controller.Version != row.ControllerVersion || value.Controller.Protocol != row.ControllerProtocol ||
		value.Controller.TrustKeyDigest != row.ControllerTrustDigest || value.ResultHash != row.ResultHash ||
		value.NoMutation != row.NoMutation || !value.CompletedAt.Equal(row.CompletedAt) {
		return false
	}
	if !sameNullableDeliveryText(value.Provider, row.Provider) ||
		!sameNullableDeliveryText(value.ProviderRef, row.ProviderRef) ||
		!sameNullableDeliveryText(value.PublicURL, row.PublicURL) ||
		!sameNullableDeliveryText(value.RejectionCode, row.RejectionCode) ||
		!sameNullableDeliveryText(value.RejectionDetail, row.RejectionDetail) ||
		!sameOptionalReference(row.PreviousHeadID, row.PreviousHeadHash, value.PreviousHead) {
		return false
	}
	var checks []PreviewCheck
	return json.Unmarshal(row.Checks, &checks) == nil && samePreviewChecks(value.Checks, checks)
}

func sameNullableDeliveryText(value string, projection *string) bool {
	if value == "" {
		return projection == nil
	}
	return projection != nil && *projection == value
}
