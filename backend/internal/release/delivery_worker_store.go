package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

func (store *Store) ClaimPreview(
	ctx context.Context,
	workerID string,
	leaseTTL time.Duration,
) (*PreviewClaim, error) {
	var row previewRunRow
	leaseExpiresAt := store.now().UTC().Add(leaseTTL)
	err := store.database.WithContext(ctx).Raw(`
UPDATE release_preview_runs AS run
SET state = 'claimed', version = run.version + 1, fence_epoch = run.fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = run.fence_epoch + 1, lease_expires_at = ?,
    started_at = COALESCE(run.started_at, statement_timestamp()), updated_at = statement_timestamp()
WHERE run.id = (
	  SELECT id FROM release_preview_runs
	  WHERE state = 'queued'
	     OR (state IN ('claimed','deploying','verifying') AND lease_expires_at <= statement_timestamp())
  ORDER BY created_at, id
  FOR UPDATE SKIP LOCKED LIMIT 1
)
RETURNING run.id::text, run.project_id::text, run.release_bundle_id::text,
          run.release_bundle_hash, run.request_key, run.request_hash, run.reason,
	          run.state, run.version, run.lease_epoch, run.created_by::text, run.created_at, run.updated_at
`, workerID, leaseExpiresAt).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, ErrNoDeliveryWork
	}
	bundle, err := store.Get(ctx, row.ProjectID, row.ReleaseBundleID, row.ReleaseBundleHash)
	if err != nil {
		return nil, err
	}
	return &PreviewClaim{
		Run: store.previewRunFromRow(ctx, row), Bundle: bundle,
		Lease: DeliveryLease{WorkerID: workerID, Epoch: row.LeaseEpoch, ExpiresAt: leaseExpiresAt},
	}, nil
}

func (store *Store) AdvancePreview(
	ctx context.Context,
	claim PreviewClaim,
	from, to DeliveryRunState,
) (PreviewClaim, error) {
	if !validPreviewAdvance(from, to) {
		return PreviewClaim{}, invalid("preview worker transition")
	}
	var row previewRunRow
	result := store.database.WithContext(ctx).Raw(`
UPDATE release_preview_runs
SET state = ?, version = version + 1, updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
RETURNING id::text, project_id::text, release_bundle_id::text, release_bundle_hash,
          request_key, request_hash, reason, state, version, created_by::text, created_at, updated_at
`, to, claim.Run.ID, claim.Run.ProjectID, from, claim.Lease.WorkerID, claim.Lease.Epoch).Scan(&row)
	if result.Error != nil {
		return PreviewClaim{}, result.Error
	}
	if row.ID == "" {
		return PreviewClaim{}, ErrDeliveryFence
	}
	claim.Run = store.previewRunFromRow(ctx, row)
	return claim, nil
}

func (store *Store) RenewPreview(ctx context.Context, claim PreviewClaim, leaseTTL time.Duration) error {
	result := store.database.WithContext(ctx).Exec(`
UPDATE release_preview_runs
SET lease_expires_at = ?, updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state IN ('claimed','deploying','verifying')
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, store.now().UTC().Add(leaseTTL), claim.Run.ID, claim.Run.ProjectID, claim.Lease.WorkerID, claim.Lease.Epoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func (store *Store) CompletePreview(
	ctx context.Context,
	claim PreviewClaim,
	result PreviewProviderResult,
) (PreviewRun, error) {
	decision := PreviewPassed
	for _, check := range result.Checks {
		if check.Status != "passed" {
			decision = PreviewFailed
		}
	}
	receipt, err := NewPreviewReceipt(NewPreviewReceiptInput{
		ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("preview-receipt\x00"+claim.Run.ID)).String(),
		RunID: claim.Run.ID, Bundle: claim.Bundle,
		Namespace: deterministicPreviewNamespace(claim.Run.ProjectID, claim.Bundle.ID),
		Provider:  result.Provider, ProviderRef: result.ProviderRef, Checks: result.Checks,
		Decision: decision, CreatedBy: claim.Run.CreatedBy, CreatedAt: store.now().UTC(),
	})
	if err != nil {
		return PreviewRun{}, err
	}
	payload, err := domain.CanonicalJSON(receipt)
	if err != nil {
		return PreviewRun{}, err
	}
	contentRef, err := store.contents.PutPending(
		ctx, receipt.ProjectID, previewReceiptAggregate, receipt.ID, 1, payload,
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
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (?, 'release-preview-receipt/v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?::jsonb, ?, 'mongo', ?, ?, ?, ?, ?)
`, receipt.ID, receipt.RunID, receipt.ProjectID, receipt.ReleaseBundle.ID, receipt.ReleaseBundle.ContentHash,
			receipt.CanonicalReceipt.ID, receipt.CanonicalReceipt.ContentHash,
			receipt.Workspace.WorkspaceArtifactID, receipt.Workspace.WorkspaceRevisionID, receipt.Workspace.WorkspaceContentHash,
			string(artifacts), receipt.Namespace, receipt.Provider, receipt.ProviderRef, string(checks), receipt.Decision,
			contentRef.ID, contentRef.ContentHash, receipt.PayloadHash, receipt.CreatedBy, receipt.CreatedAt)
		if insert.Error != nil {
			return insert.Error
		}
		update := transaction.Exec(`
UPDATE release_preview_runs
SET state = ?, version = version + 1, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state = 'verifying'
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, terminal, claim.Run.ID, claim.Run.ProjectID, claim.Lease.WorkerID, claim.Lease.Epoch)
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
		return PreviewRun{}, fmt.Errorf("%w: finalize PreviewReceipt: %v", core.ErrContentNotReady, err)
	}
	return store.GetPreviewRun(ctx, claim.Run.ProjectID, claim.Run.ID)
}

func (store *Store) FailPreview(ctx context.Context, claim PreviewClaim, state DeliveryRunState) error {
	if state != DeliveryError && state != DeliveryFailed && state != DeliveryCancelled {
		return invalid("preview terminal state")
	}
	result := store.database.WithContext(ctx).Exec(`
UPDATE release_preview_runs
SET state = ?, version = version + 1, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state IN ('claimed','deploying','verifying')
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, state, claim.Run.ID, claim.Run.ProjectID, claim.Lease.WorkerID, claim.Lease.Epoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func (store *Store) ClaimProduction(
	ctx context.Context,
	workerID string,
	leaseTTL time.Duration,
) (*ProductionClaim, error) {
	var row productionRunRow
	leaseExpiresAt := store.now().UTC().Add(leaseTTL)
	err := store.database.WithContext(ctx).Raw(`
UPDATE release_deployment_runs AS run
SET state = 'claimed', version = run.version + 1, fence_epoch = run.fence_epoch + 1,
    lease_worker_id = ?, lease_epoch = run.fence_epoch + 1, lease_expires_at = ?,
    started_at = COALESCE(run.started_at, statement_timestamp()), updated_at = statement_timestamp()
WHERE run.id = (
	  SELECT id FROM release_deployment_runs
	  WHERE state = 'queued'
	     OR (state IN ('claimed','deploying','verifying') AND lease_expires_at <= statement_timestamp())
  ORDER BY created_at, id
  FOR UPDATE SKIP LOCKED LIMIT 1
)
RETURNING run.id::text, run.project_id::text, run.environment, run.operation,
          run.release_bundle_id::text, run.release_bundle_hash,
          run.preview_receipt_id::text, run.preview_receipt_hash,
          run.promotion_approval_id::text, run.promotion_approval_hash,
	      run.source_revision_id::text, run.source_revision_hash,
	      run.expected_revision_id::text, run.expected_revision_hash,
	      run.expected_production_receipt_id::text, run.expected_production_receipt_hash,
	          run.request_key, run.request_hash, run.reason, run.state, run.version, run.lease_epoch,
          run.created_by::text, run.created_at, run.updated_at
`, workerID, leaseExpiresAt).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, ErrNoDeliveryWork
	}
	bundle, err := store.Get(ctx, row.ProjectID, row.ReleaseBundleID, row.ReleaseBundleHash)
	if err != nil {
		return nil, err
	}
	preview, err := store.GetPreviewReceipt(ctx, row.ProjectID, row.PreviewReceiptID, row.PreviewReceiptHash)
	if err != nil {
		return nil, err
	}
	approval, err := store.GetPromotionApproval(ctx, row.ProjectID, row.PromotionApprovalID, row.PromotionApprovalHash)
	if err != nil {
		return nil, err
	}
	var source *DeploymentRevision
	if row.SourceRevisionID != nil && row.SourceRevisionHash != nil {
		loaded, loadErr := store.GetDeploymentRevision(ctx, row.ProjectID, *row.SourceRevisionID, *row.SourceRevisionHash)
		if loadErr != nil {
			return nil, loadErr
		}
		source = &loaded
	}
	return &ProductionClaim{
		Run: store.productionRunFromRow(ctx, row), Bundle: bundle, Preview: preview, Approval: approval, Source: source,
		Lease: DeliveryLease{WorkerID: workerID, Epoch: row.LeaseEpoch, ExpiresAt: leaseExpiresAt},
	}, nil
}

func (store *Store) AdvanceProduction(
	ctx context.Context,
	claim ProductionClaim,
	from, to DeliveryRunState,
) (ProductionClaim, error) {
	if !validProductionAdvance(from, to) {
		return ProductionClaim{}, invalid("production worker transition")
	}
	var row productionRunRow
	result := store.database.WithContext(ctx).Raw(`
UPDATE release_deployment_runs
SET state = ?, version = version + 1, updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state = ?
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
RETURNING id::text, project_id::text, environment, operation, release_bundle_id::text, release_bundle_hash,
          preview_receipt_id::text, preview_receipt_hash,
          promotion_approval_id::text, promotion_approval_hash,
          source_revision_id::text, source_revision_hash,
          expected_revision_id::text, expected_revision_hash,
          expected_production_receipt_id::text, expected_production_receipt_hash,
          request_key, request_hash, reason,
          state, version, created_by::text, created_at, updated_at
`, to, claim.Run.ID, claim.Run.ProjectID, from, claim.Lease.WorkerID, claim.Lease.Epoch).Scan(&row)
	if result.Error != nil {
		return ProductionClaim{}, result.Error
	}
	if row.ID == "" {
		return ProductionClaim{}, ErrDeliveryFence
	}
	claim.Run = store.productionRunFromRow(ctx, row)
	return claim, nil
}

func (store *Store) RenewProduction(ctx context.Context, claim ProductionClaim, leaseTTL time.Duration) error {
	result := store.database.WithContext(ctx).Exec(`
UPDATE release_deployment_runs
SET lease_expires_at = ?, updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state IN ('claimed','deploying','verifying')
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, store.now().UTC().Add(leaseTTL), claim.Run.ID, claim.Run.ProjectID, claim.Lease.WorkerID, claim.Lease.Epoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func (store *Store) CompleteProduction(
	ctx context.Context,
	claim ProductionClaim,
	result ProductionProviderResult,
) (ProductionRun, error) {
	decision := PreviewPassed
	if !previewChecksPassed(result.Checks) {
		decision = PreviewFailed
	}
	createdAt := store.now().UTC()
	receipt, err := NewProductionReceipt(NewProductionReceiptInput{
		ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("production-receipt\x00"+claim.Run.ID)).String(),
		RunID: claim.Run.ID, Bundle: claim.Bundle, Preview: claim.Preview, Approval: claim.Approval,
		Operation: claim.Run.Operation, SourceRevision: claim.Run.SourceRevision,
		Provider: result.Provider, ProviderRef: result.ProviderRef, PublicURL: result.PublicURL,
		Checks: result.Checks, Decision: decision,
		CreatedBy: claim.Run.CreatedBy, CreatedAt: createdAt,
	})
	if err != nil {
		return ProductionRun{}, err
	}
	receiptPayload, err := domain.CanonicalJSON(receipt)
	if err != nil {
		return ProductionRun{}, err
	}
	receiptContent, err := store.contents.PutPending(
		ctx, receipt.ProjectID, productionReceiptAggregate, receipt.ID, 1, receiptPayload,
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
		revision, err = NewDeploymentRevision(NewDeploymentRevisionInput{
			ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("deployment-revision\x00"+claim.Run.ID)).String(),
			RunID: claim.Run.ID, Bundle: claim.Bundle, Preview: claim.Preview, Approval: claim.Approval,
			Receipt:   receipt,
			Operation: claim.Run.Operation, SourceRevision: claim.Run.SourceRevision,
			Provider: result.Provider, ProviderRef: result.ProviderRef, PublicURL: result.PublicURL,
			Checks:    result.Checks,
			CreatedBy: claim.Run.CreatedBy, CreatedAt: createdAt,
		})
		if err != nil {
			return ProductionRun{}, err
		}
		revisionPayload, marshalErr := domain.CanonicalJSON(revision)
		if marshalErr != nil {
			return ProductionRun{}, marshalErr
		}
		revisionContent, putErr := store.contents.PutPending(
			ctx, revision.ProjectID, deploymentRevisionAggregate, revision.ID, 1, revisionPayload,
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
  content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (?, 'release-production-receipt/v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, 'mongo', ?, ?, ?, ?, ?)
`, receipt.ID, receipt.RunID, receipt.ProjectID, receipt.Operation,
			receipt.ReleaseBundle.ID, receipt.ReleaseBundle.ContentHash,
			receipt.PreviewReceipt.ID, receipt.PreviewReceipt.ContentHash,
			receipt.Approval.ID, receipt.Approval.ContentHash, sourceID, sourceHash,
			receipt.Provider, receipt.ProviderRef, receipt.PublicURL, string(checks), receipt.Decision,
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
  provider, provider_ref, public_url, checks, content_store, content_ref, content_hash, payload_hash, created_by, created_at
) VALUES (?, 'release-deployment-revision/v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, 'mongo', ?, ?, ?, ?, ?)
`, revision.ID, revision.RunID, revision.ProjectID, revision.Operation,
				revision.ReleaseBundle.ID, revision.ReleaseBundle.ContentHash,
				revision.PreviewReceipt.ID, revision.PreviewReceipt.ContentHash,
				revision.Approval.ID, revision.Approval.ContentHash,
				revision.ProductionReceipt.ID, revision.ProductionReceipt.ContentHash, sourceID, sourceHash,
				revision.Provider, revision.ProviderRef, revision.PublicURL, string(checks),
				revisionContentID, revisionContentHash, revision.PayloadHash, revision.CreatedBy, revision.CreatedAt)
			if insertRevision.Error != nil {
				return insertRevision.Error
			}
			var expectedRevisionID, expectedRevisionHash any
			var expectedReceiptID, expectedReceiptHash any
			if claim.Run.ExpectedRevision != nil {
				expectedRevisionID = claim.Run.ExpectedRevision.ID
				expectedRevisionHash = claim.Run.ExpectedRevision.ContentHash
			}
			if claim.Run.ExpectedReceipt != nil {
				expectedReceiptID = claim.Run.ExpectedReceipt.ID
				expectedReceiptHash = claim.Run.ExpectedReceipt.ContentHash
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
				claim.Run.CreatedBy, claim.Run.ProjectID, claim.Run.Environment,
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
SET state = ?, version = version + 1, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state = 'verifying'
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, terminalState, claim.Run.ID, claim.Run.ProjectID, claim.Lease.WorkerID, claim.Lease.Epoch)
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
	abortReceipt = false
	abortRevision = false
	if err := store.contents.Finalize(ctx, receiptContent.ID); err != nil {
		return ProductionRun{}, fmt.Errorf("%w: finalize ProductionReceipt: %v", core.ErrContentNotReady, err)
	}
	if revisionContentID != "" {
		if err := store.contents.Finalize(ctx, revisionContentID); err != nil {
			return ProductionRun{}, fmt.Errorf("%w: finalize DeploymentRevision: %v", core.ErrContentNotReady, err)
		}
	}
	return store.GetProductionRun(ctx, claim.Run.ProjectID, claim.Run.ID)
}

func (store *Store) FailProduction(ctx context.Context, claim ProductionClaim, state DeliveryRunState) error {
	if state != DeliveryError && state != DeliveryFailed && state != DeliveryCancelled {
		return invalid("production terminal state")
	}
	result := store.database.WithContext(ctx).Exec(`
UPDATE release_deployment_runs
SET state = ?, version = version + 1, lease_worker_id = NULL, lease_epoch = NULL,
    lease_expires_at = NULL, finished_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE id = ? AND project_id = ? AND state IN ('claimed','deploying','verifying')
  AND lease_worker_id = ? AND lease_epoch = ? AND lease_expires_at > statement_timestamp()
`, state, claim.Run.ID, claim.Run.ProjectID, claim.Lease.WorkerID, claim.Lease.Epoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func validPreviewAdvance(from, to DeliveryRunState) bool {
	return (from == DeliveryClaimed && to == DeliveryDeploying) ||
		(from == DeliveryDeploying && to == DeliveryVerifying)
}

func validProductionAdvance(from, to DeliveryRunState) bool {
	return validPreviewAdvance(from, to)
}

func deliveryWorkerError(detail string, err error) error {
	if errors.Is(err, ErrDeliveryFence) {
		return err
	}
	return fmt.Errorf("%s: %w", detail, err)
}
