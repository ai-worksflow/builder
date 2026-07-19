package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

type deliveryOperationResultRow struct {
	OperationID             string          `gorm:"column:operation_id"`
	RequestHash             string          `gorm:"column:request_hash"`
	ProjectID               string          `gorm:"column:project_id"`
	Kind                    string          `gorm:"column:kind"`
	Status                  string          `gorm:"column:status"`
	ControllerSchemaVersion string          `gorm:"column:controller_schema_version"`
	ControllerID            string          `gorm:"column:controller_id"`
	ControllerVersion       string          `gorm:"column:controller_version"`
	ControllerProtocol      string          `gorm:"column:controller_protocol"`
	ControllerTrustDigest   string          `gorm:"column:controller_trust_key_digest"`
	Provider                *string         `gorm:"column:provider"`
	ProviderRef             *string         `gorm:"column:provider_ref"`
	PublicURL               *string         `gorm:"column:public_url"`
	Checks                  json.RawMessage `gorm:"column:checks"`
	PreviousHeadID          *string         `gorm:"column:previous_head_id"`
	PreviousHeadHash        *string         `gorm:"column:previous_head_hash"`
	NoMutation              bool            `gorm:"column:no_mutation"`
	RejectionCode           *string         `gorm:"column:rejection_code"`
	RejectionDetail         *string         `gorm:"column:rejection_detail"`
	CompletedAt             time.Time       `gorm:"column:completed_at"`
	ResultDocument          string          `gorm:"column:result_document"`
	ResultHash              string          `gorm:"column:result_hash"`
}

func (deliveryOperationResultRow) TableName() string {
	return "release_delivery_operation_results"
}

func (store *ReconciledDeliveryStore) ClaimDeliveryOperation(
	ctx context.Context,
	workerID string,
	leaseTTL time.Duration,
) (*ReconciledDeliveryClaim, error) {
	if !boundedIdentifier(workerID, 128) || leaseTTL < 5*time.Second {
		return nil, invalid("release delivery claim")
	}
	leaseMicroseconds := leaseTTL.Microseconds()
	for _, kind := range deliveryClaimOrder(store.claimSequence.Add(1)) {
		var claim *ReconciledDeliveryClaim
		var err error
		if kind == DeliveryOperationProduction {
			claim, err = store.claimReconciledProduction(ctx, workerID, leaseMicroseconds)
		} else {
			claim, err = store.claimReconciledPreview(ctx, workerID, leaseMicroseconds)
		}
		if err != nil {
			return nil, err
		}
		if claim != nil {
			return claim, nil
		}
	}
	return nil, ErrNoDeliveryWork
}

// Every store instance alternates which queue receives first claim priority.
// Because each worker service executes claims serially, a continuously fed
// Preview queue can delay an eligible Production operation by at most one
// successful claim per replica instead of starving it indefinitely. Each SQL
// claim still uses SKIP LOCKED, so replicas cannot claim the same Run.
func deliveryClaimOrder(sequence uint64) [2]DeliveryOperationKind {
	if sequence%2 == 1 {
		return [2]DeliveryOperationKind{DeliveryOperationProduction, DeliveryOperationPreview}
	}
	return [2]DeliveryOperationKind{DeliveryOperationPreview, DeliveryOperationProduction}
}

func (store *ReconciledDeliveryStore) claimReconciledPreview(
	ctx context.Context,
	workerID string,
	leaseMicroseconds int64,
) (*ReconciledDeliveryClaim, error) {
	var preview previewRunRow
	err := store.database.WithContext(ctx).Raw(`
UPDATE release_preview_runs AS run
SET state = CASE
      WHEN run.state = 'queued' THEN 'claimed'
      WHEN run.state IN ('reconcile_wait','submitting') THEN 'reconciling'
      ELSE run.state
    END,
    version = run.version + 1,
    fence_epoch = run.fence_epoch + 1,
	lease_worker_id = ?, lease_epoch = run.fence_epoch + 1,
	lease_expires_at = statement_timestamp() + (? * interval '1 microsecond'),
    started_at = COALESCE(run.started_at, statement_timestamp()),
    updated_at = GREATEST(clock_timestamp(), run.updated_at + interval '1 microsecond')
WHERE run.id = (
  SELECT candidate.id
  FROM release_preview_runs AS candidate
  JOIN release_delivery_operations AS operation ON operation.preview_run_id = candidate.id
  WHERE candidate.schema_version = 'release-preview-run/v2'
    AND (
      candidate.state = 'queued'
      OR (candidate.state = 'reconcile_wait'
          AND operation.next_attempt_at <= statement_timestamp())
      OR (candidate.state IN ('claimed','submitting','reconciling','verifying')
          AND candidate.lease_expires_at <= statement_timestamp())
    )
  ORDER BY candidate.created_at, candidate.id
  FOR UPDATE OF candidate SKIP LOCKED LIMIT 1
)
RETURNING run.id::text, run.project_id::text, run.release_bundle_id::text,
          run.release_bundle_hash, run.request_key, run.request_hash, run.reason,
		  run.state, run.version, run.lease_epoch, run.lease_expires_at, run.created_by::text,
		  run.created_at, run.updated_at
`, workerID, leaseMicroseconds).Scan(&preview).Error
	if err != nil {
		return nil, err
	}
	if preview.ID == "" {
		return nil, nil
	}
	return store.hydrateReconciledPreviewClaim(ctx, preview, workerID)
}

func (store *ReconciledDeliveryStore) claimReconciledProduction(
	ctx context.Context,
	workerID string,
	leaseMicroseconds int64,
) (*ReconciledDeliveryClaim, error) {
	var production productionRunRow
	err := store.database.WithContext(ctx).Raw(`
UPDATE release_deployment_runs AS run
SET state = CASE
      WHEN run.state = 'queued' THEN 'claimed'
      WHEN run.state IN ('reconcile_wait','submitting') THEN 'reconciling'
      ELSE run.state
    END,
    version = run.version + 1,
    fence_epoch = run.fence_epoch + 1,
	lease_worker_id = ?, lease_epoch = run.fence_epoch + 1,
	lease_expires_at = statement_timestamp() + (? * interval '1 microsecond'),
    started_at = COALESCE(run.started_at, statement_timestamp()),
    updated_at = GREATEST(clock_timestamp(), run.updated_at + interval '1 microsecond')
WHERE run.id = (
  SELECT candidate.id
  FROM release_deployment_runs AS candidate
  JOIN release_delivery_operations AS operation ON operation.deployment_run_id = candidate.id
  WHERE candidate.schema_version = 'release-deployment-run/v2'
    AND (
      candidate.state = 'queued'
      OR (candidate.state = 'reconcile_wait'
          AND operation.next_attempt_at <= statement_timestamp())
      OR (candidate.state IN ('claimed','submitting','reconciling','verifying')
          AND candidate.lease_expires_at <= statement_timestamp())
    )
  ORDER BY candidate.created_at, candidate.id
  FOR UPDATE OF candidate SKIP LOCKED LIMIT 1
)
RETURNING run.id::text, run.project_id::text, run.environment, run.operation,
          run.release_bundle_id::text, run.release_bundle_hash,
          run.preview_receipt_id::text, run.preview_receipt_hash,
          run.promotion_approval_id::text, run.promotion_approval_hash,
          run.source_revision_id::text, run.source_revision_hash,
          run.expected_revision_id::text, run.expected_revision_hash,
          run.expected_production_receipt_id::text, run.expected_production_receipt_hash,
          run.request_key, run.request_hash, run.reason, run.state, run.version,
		  run.lease_epoch, run.lease_expires_at, run.created_by::text, run.created_at, run.updated_at
`, workerID, leaseMicroseconds).Scan(&production).Error
	if err != nil {
		return nil, err
	}
	if production.ID == "" {
		return nil, nil
	}
	return store.hydrateReconciledProductionClaim(ctx, production, workerID)
}

func (store *ReconciledDeliveryStore) hydrateReconciledPreviewClaim(
	ctx context.Context,
	row previewRunRow,
	workerID string,
) (*ReconciledDeliveryClaim, error) {
	operation, request, err := store.loadDeliveryOperation(ctx, row.ID, DeliveryOperationPreview)
	if err != nil {
		return nil, err
	}
	bundle, err := store.Get(ctx, row.ProjectID, row.ReleaseBundleID, row.ReleaseBundleHash)
	if err != nil {
		return nil, err
	}
	return &ReconciledDeliveryClaim{
		Request: request, Controller: store.controller, RemoteState: operation.RemoteState,
		ReconcileOnly: operation.ReconcileOnly,
		RunState:      DeliveryRunState(row.State),
		Preview: &PreviewClaim{
			Run: store.previewRunFromRow(ctx, row), Bundle: bundle,
			Lease: DeliveryLease{WorkerID: workerID, Epoch: row.LeaseEpoch, ExpiresAt: row.LeaseExpiresAt},
		},
	}, nil
}

func (store *ReconciledDeliveryStore) hydrateReconciledProductionClaim(
	ctx context.Context,
	row productionRunRow,
	workerID string,
) (*ReconciledDeliveryClaim, error) {
	operation, request, err := store.loadDeliveryOperation(ctx, row.ID, DeliveryOperationProduction)
	if err != nil {
		return nil, err
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
	return &ReconciledDeliveryClaim{
		Request: request, Controller: store.controller, RemoteState: operation.RemoteState,
		ReconcileOnly: operation.ReconcileOnly,
		RunState:      DeliveryRunState(row.State),
		Production: &ProductionClaim{
			Run: store.productionRunFromRow(ctx, row), Bundle: bundle, Preview: preview,
			Approval: approval, Source: source,
			Lease: DeliveryLease{WorkerID: workerID, Epoch: row.LeaseEpoch, ExpiresAt: row.LeaseExpiresAt},
		},
	}, nil
}

func (store *ReconciledDeliveryStore) loadDeliveryOperation(
	ctx context.Context,
	runID string,
	kind DeliveryOperationKind,
) (deliveryOperationRow, DeliveryOperationRequest, error) {
	var row deliveryOperationRow
	runColumn := "preview_run_id"
	if kind == DeliveryOperationProduction {
		runColumn = "deployment_run_id"
	}
	if err := store.database.WithContext(ctx).Raw(fmt.Sprintf(`
SELECT operation.*,
       EXISTS (
         SELECT 1 FROM release_delivery_reconciliation_cases AS resolution
         WHERE resolution.operation_id = operation.id
       ) AS reconcile_only
FROM release_delivery_operations AS operation
WHERE operation.kind = ? AND operation.%s = ?
`, runColumn), kind, runID).Scan(&row).Error; err != nil {
		return deliveryOperationRow{}, DeliveryOperationRequest{}, err
	}
	if row.ID == "" {
		return deliveryOperationRow{}, DeliveryOperationRequest{}, gorm.ErrRecordNotFound
	}
	request, err := store.deliveryOperationRequestFromRow(row)
	if err != nil {
		return deliveryOperationRow{}, DeliveryOperationRequest{}, err
	}
	return row, request, nil
}

func (store *ReconciledDeliveryStore) RenewDeliveryOperation(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	leaseTTL time.Duration,
) error {
	if leaseTTL < 5*time.Second {
		return invalid("release delivery lease renewal")
	}
	table, runID, projectID, lease, err := deliveryClaimIdentity(claim)
	if err != nil {
		return err
	}
	result := store.database.WithContext(ctx).Exec(fmt.Sprintf(`
UPDATE %s
SET lease_expires_at = statement_timestamp() + (? * interval '1 microsecond'),
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ?
  AND state IN ('claimed','submitting','reconciling','verifying')
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
`, table), leaseTTL.Microseconds(),
		runID, projectID, lease.WorkerID, lease.Epoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func (store *ReconciledDeliveryStore) BeginDeliveryAttempt(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	kind DeliveryAttemptKind,
) (DeliveryOperationAttempt, error) {
	table, runID, projectID, lease, err := deliveryClaimIdentity(claim)
	if err != nil {
		return DeliveryOperationAttempt{}, err
	}
	if (kind == DeliveryAttemptSubmit && claim.RunState != DeliveryClaimed) ||
		((kind == DeliveryAttemptReconcile || kind == DeliveryAttemptResubmit) && claim.RunState != DeliveryReconciling) {
		return DeliveryOperationAttempt{}, invalid("delivery attempt phase")
	}
	var attempt DeliveryOperationAttempt
	err = store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if kind == DeliveryAttemptSubmit {
			result := transaction.Exec(fmt.Sprintf(`
UPDATE %s
SET state = 'submitting', version = version + 1,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ? AND state = 'claimed'
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
`, table), runID, projectID, lease.WorkerID, lease.Epoch)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrDeliveryFence
			}
		}
		result := transaction.Exec(`
INSERT INTO release_delivery_operation_attempts (
  operation_id, ordinal, schema_version, kind, worker_id, fence_epoch
) VALUES (?, 1, 'release-delivery-operation-attempt/v1', ?, ?, ?)
`, claim.Request.OperationID, kind, lease.WorkerID, lease.Epoch)
		if result.Error != nil {
			return result.Error
		}
		return transaction.Raw(`
SELECT operation_id::text, ordinal, kind, worker_id, fence_epoch
FROM release_delivery_operation_attempts
WHERE operation_id = ? AND kind = ? AND fence_epoch = ?
`, claim.Request.OperationID, kind, lease.Epoch).Scan(&attempt).Error
	})
	if err != nil {
		return DeliveryOperationAttempt{}, err
	}
	if attempt.OperationID == "" || attempt.Ordinal == 0 {
		return DeliveryOperationAttempt{}, fmt.Errorf("%w: delivery Attempt was not persisted", ErrBundleIntegrity)
	}
	return attempt, nil
}

func (store *ReconciledDeliveryStore) RecordDeliveryObservation(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	observation DeliveryOperationObservation,
	nextAttemptAt time.Time,
) error {
	parsed, err := ParseDeliveryOperationObservation(observation, store.controller, claim.Request)
	if err != nil {
		return err
	}
	responseHash, err := deliveryObservationEvidenceHash(parsed)
	if err != nil {
		return err
	}
	return store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := completeDeliveryAttemptObservation(transaction, attempt, parsed, responseHash); err != nil {
			return err
		}
		terminalHash := any(nil)
		if parsed.Result != nil {
			terminalHash = parsed.Result.ResultHash
			if err := store.insertDeliveryOperationResult(transaction, claim, attempt, *parsed.Result); err != nil {
				return err
			}
		}
		var next any
		if !nextAttemptAt.IsZero() {
			next = nextAttemptAt.UTC().Truncate(time.Microsecond)
		}
		operationUpdate := transaction.Exec(`
UPDATE release_delivery_operations
SET remote_state = ?, next_attempt_at = ?,
    last_observation_sequence = ?, last_observed_at = ?,
    terminal_result_hash = ?, last_error_code = NULL, last_error_detail = NULL,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND request_hash = ?
`, parsed.State, next, parsed.Sequence, parsed.ObservedAt, terminalHash,
			claim.Request.OperationID, claim.Request.RequestHash)
		if operationUpdate.Error != nil {
			return operationUpdate.Error
		}
		if operationUpdate.RowsAffected != 1 {
			return ErrDeliveryFence
		}
		switch parsed.State {
		case DeliveryRemoteAccepted, DeliveryRemoteRunning:
			return store.transitionDeliveryRun(transaction, claim,
				[]DeliveryRunState{DeliverySubmitting, DeliveryReconciling}, DeliveryReconcileWait, true, false)
		case DeliveryRemoteCompleted:
			return store.transitionDeliveryRun(transaction, claim,
				[]DeliveryRunState{DeliverySubmitting, DeliveryReconciling}, DeliveryVerifying, false, false)
		case DeliveryRemoteRejected:
			return store.transitionDeliveryRun(transaction, claim,
				[]DeliveryRunState{DeliverySubmitting, DeliveryReconciling}, DeliveryError, true, true)
		default:
			return invalid("record delivery observation state")
		}
	})
}

func (store *ReconciledDeliveryStore) RecordDeliveryUnknown(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	code, detail string,
	nextAttemptAt time.Time,
) error {
	if !boundedIdentifier(code, 128) || !boundedText(detail, 4000) || nextAttemptAt.IsZero() {
		return invalid("unknown delivery outcome evidence")
	}
	return store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := completeDeliveryAttemptError(transaction, attempt, "unknown", nil, code, detail); err != nil {
			return err
		}
		remoteState := claim.RemoteState
		if remoteState == "prepared" {
			remoteState = "submit_unknown"
		}
		result := transaction.Exec(`
UPDATE release_delivery_operations
SET remote_state = ?, next_attempt_at = ?, last_error_code = ?, last_error_detail = ?,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND request_hash = ?
`, remoteState, nextAttemptAt.UTC().Truncate(time.Microsecond), code, detail,
			claim.Request.OperationID, claim.Request.RequestHash)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDeliveryFence
		}
		return store.transitionDeliveryRun(transaction, claim,
			[]DeliveryRunState{DeliverySubmitting, DeliveryReconciling}, DeliveryReconcileWait, true, false)
	})
}

func (store *ReconciledDeliveryStore) RecordDeliveryNotFound(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
) error {
	return store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		status := 404
		return completeDeliveryAttemptError(
			transaction, attempt, "not_found", &status,
			"controller-operation-not-found", "the exact controller operation was not found",
		)
	})
}

func (store *ReconciledDeliveryStore) QuarantineDeliveryOperation(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	code, detail string,
) error {
	if !boundedIdentifier(code, 128) || !boundedText(detail, 4000) {
		return invalid("delivery quarantine evidence")
	}
	return store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := completeDeliveryAttemptError(transaction, attempt, "quarantined", nil, code, detail); err != nil {
			return err
		}
		result := transaction.Exec(`
UPDATE release_delivery_operations
SET remote_state = 'quarantined', next_attempt_at = NULL,
    terminal_result_hash = NULL, last_error_code = ?, last_error_detail = ?,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND request_hash = ?
`, code, detail, claim.Request.OperationID, claim.Request.RequestHash)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDeliveryFence
		}
		return store.transitionDeliveryRun(transaction, claim,
			[]DeliveryRunState{DeliverySubmitting, DeliveryReconciling}, DeliveryReconcileBlocked, true, false)
	})
}

func (store *ReconciledDeliveryStore) insertDeliveryOperationResult(
	transaction *gorm.DB,
	claim ReconciledDeliveryClaim,
	attempt DeliveryOperationAttempt,
	result DeliveryOperationResult,
) error {
	result, err := ParseDeliveryOperationResult(result, store.controller, claim.Request)
	if err != nil {
		return err
	}
	document, err := domain.CanonicalJSON(result)
	if err != nil {
		return err
	}
	checks, err := json.Marshal(result.Checks)
	if err != nil {
		return err
	}
	var previousID, previousHash any
	if result.PreviousHead != nil {
		previousID, previousHash = result.PreviousHead.ID, result.PreviousHead.ContentHash
	}
	return transaction.Exec(`
INSERT INTO release_delivery_operation_results (
  operation_id, schema_version, request_hash, project_id, kind, status,
  controller_schema_version, controller_id, controller_version,
  controller_protocol, controller_trust_key_digest,
  provider, provider_ref, public_url, checks,
  previous_head_id, previous_head_hash,
  no_mutation, rejection_code, rejection_detail,
  worker_id, fence_epoch, completed_at, result_document, result_hash
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, result.OperationID, result.SchemaVersion, result.RequestHash, result.ProjectID, result.Kind, result.Status,
		result.Controller.SchemaVersion, result.Controller.ID, result.Controller.Version,
		result.Controller.Protocol, result.Controller.TrustKeyDigest,
		nullableDeliveryText(result.Provider), nullableDeliveryText(result.ProviderRef), nullableDeliveryText(result.PublicURL),
		string(checks), previousID, previousHash, result.NoMutation,
		nullableDeliveryText(result.RejectionCode), nullableDeliveryText(result.RejectionDetail),
		attempt.WorkerID, attempt.FenceEpoch, result.CompletedAt, string(document), result.ResultHash).Error
}

func completeDeliveryAttemptObservation(
	transaction *gorm.DB,
	attempt DeliveryOperationAttempt,
	observation DeliveryOperationObservation,
	responseHash string,
) error {
	result := transaction.Exec(`
UPDATE release_delivery_operation_attempts
SET completed_at = statement_timestamp(), outcome = ?, response_hash = ?,
    observation_sequence = ?, observed_at = ?
WHERE operation_id = ? AND ordinal = ? AND kind = ?
  AND worker_id = ? AND fence_epoch = ? AND completed_at IS NULL
`, observation.State, responseHash, observation.Sequence, observation.ObservedAt,
		attempt.OperationID, attempt.Ordinal, attempt.Kind, attempt.WorkerID, attempt.FenceEpoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func completeDeliveryAttemptError(
	transaction *gorm.DB,
	attempt DeliveryOperationAttempt,
	outcome string,
	httpStatus *int,
	code, detail string,
) error {
	result := transaction.Exec(`
UPDATE release_delivery_operation_attempts
SET completed_at = statement_timestamp(), outcome = ?, http_status = ?,
    error_code = ?, error_detail = ?
WHERE operation_id = ? AND ordinal = ? AND kind = ?
  AND worker_id = ? AND fence_epoch = ? AND completed_at IS NULL
`, outcome, httpStatus, code, detail,
		attempt.OperationID, attempt.Ordinal, attempt.Kind, attempt.WorkerID, attempt.FenceEpoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func deliveryObservationEvidenceHash(observation DeliveryOperationObservation) (string, error) {
	if observation.Result != nil {
		return observation.Result.ResultHash, nil
	}
	hash, err := domain.CanonicalHash(observation)
	if err != nil {
		return "", err
	}
	return "sha256:" + hash, nil
}

func (store *ReconciledDeliveryStore) transitionDeliveryRun(
	transaction *gorm.DB,
	claim ReconciledDeliveryClaim,
	from []DeliveryRunState,
	to DeliveryRunState,
	releaseLease, finish bool,
) error {
	table, runID, projectID, lease, err := deliveryClaimIdentity(claim)
	if err != nil {
		return err
	}
	states := make([]string, len(from))
	for index := range from {
		states[index] = string(from[index])
	}
	var finished any
	if finish {
		finished = store.now().UTC().Truncate(time.Microsecond)
	}
	var result *gorm.DB
	if releaseLease {
		result = transaction.Exec(fmt.Sprintf(`
UPDATE %s
SET state = ?, version = version + 1,
    lease_worker_id = NULL, lease_epoch = NULL, lease_expires_at = NULL, finished_at = ?,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ? AND state IN ?
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
`, table), to, finished, runID, projectID, states, lease.WorkerID, lease.Epoch)
	} else {
		result = transaction.Exec(fmt.Sprintf(`
UPDATE %s
SET state = ?, version = version + 1,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ? AND state IN ?
  AND lease_worker_id = ? AND lease_epoch = ?
  AND lease_expires_at > statement_timestamp()
`, table), to, runID, projectID, states, lease.WorkerID, lease.Epoch)
	}
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFence
	}
	return nil
}

func deliveryClaimIdentity(
	claim ReconciledDeliveryClaim,
) (table, runID, projectID string, lease DeliveryLease, err error) {
	if claim.Preview != nil && claim.Production == nil && claim.Request.Kind == DeliveryOperationPreview {
		return "release_preview_runs", claim.Preview.Run.ID, claim.Preview.Run.ProjectID, claim.Preview.Lease, nil
	}
	if claim.Production != nil && claim.Preview == nil && claim.Request.Kind == DeliveryOperationProduction {
		return "release_deployment_runs", claim.Production.Run.ID, claim.Production.Run.ProjectID, claim.Production.Lease, nil
	}
	return "", "", "", DeliveryLease{}, invalid("delivery claim identity")
}

func nullableDeliveryText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (store *ReconciledDeliveryStore) FinalizeDeliveryOperation(
	ctx context.Context,
	claim ReconciledDeliveryClaim,
) error {
	return store.finalizeReconciledDeliveryOperation(ctx, claim)
}

var _ ReconciledDeliveryWorkerStore = (*ReconciledDeliveryStore)(nil)

// Keep errors imported in this authority file: callers classify missing rows
// separately from integrity failures during finalization.
var _ = errors.Is
