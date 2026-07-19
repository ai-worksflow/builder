package release

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

type deliveryReconciliationCaseRow struct {
	ID                     string     `gorm:"column:id"`
	SchemaVersion          string     `gorm:"column:schema_version"`
	ProjectID              string     `gorm:"column:project_id"`
	RunKind                string     `gorm:"column:run_kind"`
	RunID                  string     `gorm:"column:run_id"`
	RunSchemaVersion       string     `gorm:"column:run_schema_version"`
	ExpectedRunVersion     uint64     `gorm:"column:expected_run_version"`
	OperationID            string     `gorm:"column:operation_id"`
	OperationRequestHash   string     `gorm:"column:operation_request_hash"`
	ControllerSchema       string     `gorm:"column:controller_schema_version"`
	ControllerID           string     `gorm:"column:controller_id"`
	ControllerVersion      string     `gorm:"column:controller_version"`
	ControllerProtocol     string     `gorm:"column:controller_protocol"`
	ControllerTrustDigest  string     `gorm:"column:controller_trust_key_digest"`
	PreviousRemoteState    string     `gorm:"column:previous_remote_state"`
	ResumeRemoteState      string     `gorm:"column:resume_remote_state"`
	SubmitAttemptCount     uint64     `gorm:"column:submit_attempt_count"`
	ReconcileAttemptCount  uint64     `gorm:"column:reconcile_attempt_count"`
	LastAttemptOrdinal     uint64     `gorm:"column:last_attempt_ordinal"`
	LastAttemptKind        string     `gorm:"column:last_attempt_kind"`
	LastAttemptWorkerID    string     `gorm:"column:last_attempt_worker_id"`
	LastAttemptFenceEpoch  uint64     `gorm:"column:last_attempt_fence_epoch"`
	LastAttemptStartedAt   time.Time  `gorm:"column:last_attempt_started_at"`
	LastAttemptCompletedAt time.Time  `gorm:"column:last_attempt_completed_at"`
	LastAttemptOutcome     string     `gorm:"column:last_attempt_outcome"`
	LastObservationSeq     *uint64    `gorm:"column:last_observation_sequence"`
	LastObservedAt         *time.Time `gorm:"column:last_observed_at"`
	QuarantineErrorCode    string     `gorm:"column:quarantine_error_code"`
	QuarantineErrorDetail  string     `gorm:"column:quarantine_error_detail"`
	ActorID                string     `gorm:"column:actor_id"`
	Reason                 string     `gorm:"column:reason"`
	IdempotencyKey         string     `gorm:"column:idempotency_key"`
	RequestHash            string     `gorm:"column:request_hash"`
	CaseDocument           string     `gorm:"column:case_document"`
	CaseHash               string     `gorm:"column:case_hash"`
	CreatedAt              time.Time  `gorm:"column:created_at"`
}

func (deliveryReconciliationCaseRow) TableName() string {
	return "release_delivery_reconciliation_cases"
}

type blockedDeliverySnapshot struct {
	RunSchemaVersion       string     `gorm:"column:run_schema_version"`
	RunState               string     `gorm:"column:run_state"`
	RunVersion             uint64     `gorm:"column:run_version"`
	LeaseWorkerID          *string    `gorm:"column:lease_worker_id"`
	LeaseEpoch             *uint64    `gorm:"column:lease_epoch"`
	LeaseExpiresAt         *time.Time `gorm:"column:lease_expires_at"`
	OperationID            *string    `gorm:"column:operation_id"`
	OperationRequestHash   *string    `gorm:"column:operation_request_hash"`
	ControllerSchema       *string    `gorm:"column:controller_schema_version"`
	ControllerID           *string    `gorm:"column:controller_id"`
	ControllerVersion      *string    `gorm:"column:controller_version"`
	ControllerProtocol     *string    `gorm:"column:controller_protocol"`
	ControllerTrustDigest  *string    `gorm:"column:controller_trust_key_digest"`
	RemoteState            *string    `gorm:"column:remote_state"`
	SubmitAttemptCount     uint64     `gorm:"column:submit_attempt_count"`
	ReconcileAttemptCount  uint64     `gorm:"column:reconcile_attempt_count"`
	LastObservationSeq     *uint64    `gorm:"column:last_observation_sequence"`
	LastObservedAt         *time.Time `gorm:"column:last_observed_at"`
	LastErrorCode          *string    `gorm:"column:last_error_code"`
	LastErrorDetail        *string    `gorm:"column:last_error_detail"`
	LastAttemptOrdinal     *uint64    `gorm:"column:last_attempt_ordinal"`
	LastAttemptKind        *string    `gorm:"column:last_attempt_kind"`
	LastAttemptWorkerID    *string    `gorm:"column:last_attempt_worker_id"`
	LastAttemptFenceEpoch  *uint64    `gorm:"column:last_attempt_fence_epoch"`
	LastAttemptStartedAt   *time.Time `gorm:"column:last_attempt_started_at"`
	LastAttemptCompleted   *time.Time `gorm:"column:last_attempt_completed_at"`
	LastAttemptOutcome     *string    `gorm:"column:last_attempt_outcome"`
	LastAttemptErrorCode   *string    `gorm:"column:last_attempt_error_code"`
	LastAttemptErrorDetail *string    `gorm:"column:last_attempt_error_detail"`
	ResumeRemoteState      string     `gorm:"column:resume_remote_state"`
}

func (store *ReconciledDeliveryStore) ResumeBlockedDelivery(
	ctx context.Context,
	input ResumeBlockedDeliveryInput,
) (DeliveryReconciliationCase, bool, error) {
	if ctx == nil || !validUUID(input.ID) || !validUUID(input.ProjectID) || !validUUID(input.RunID) ||
		!validUUID(input.ActorID) || (input.RunKind != DeliveryOperationPreview && input.RunKind != DeliveryOperationProduction) ||
		input.ExpectedVersion == 0 || input.ExpectedVersion > uint64(^uint64(0)>>1) ||
		!boundedIdentifier(input.ExpectedErrorCode, 128) || !boundedText(input.Reason, 1000) ||
		!boundedIdentifier(input.IdempotencyKey, 128) || !exactHash(input.RequestHash) {
		return DeliveryReconciliationCase{}, false, invalid("blocked delivery reconciliation input")
	}
	var resolved DeliveryReconciliationCase
	replayed := false
	err := store.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		row, found, err := loadDeliveryReconciliationCaseByKey(transaction, input.ProjectID, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			loaded, err := deliveryReconciliationCaseFromRow(row)
			if err != nil {
				return err
			}
			if loaded.ID != input.ID || loaded.RequestHash != input.RequestHash ||
				loaded.ActorID != input.ActorID {
				return ErrDeliveryReconciliationConflict
			}
			resolved, replayed = loaded, true
			return nil
		}

		snapshot, err := loadBlockedDeliverySnapshot(transaction, input, true)
		if err != nil {
			return err
		}
		if snapshot.RunSchemaVersion == "release-preview-run/v1" ||
			snapshot.RunSchemaVersion == "release-deployment-run/v1" {
			return ErrDeliveryReconciliationLegacy
		}
		if snapshot.RunVersion != input.ExpectedVersion || snapshot.RunState != string(DeliveryReconcileBlocked) ||
			snapshot.LeaseWorkerID != nil || snapshot.LeaseEpoch != nil || snapshot.LeaseExpiresAt != nil ||
			snapshot.OperationID == nil || snapshot.RemoteState == nil || *snapshot.RemoteState != "quarantined" ||
			snapshot.LastErrorCode == nil || *snapshot.LastErrorCode != input.ExpectedErrorCode ||
			snapshot.LastErrorDetail == nil {
			return ErrDeliveryReconciliationConflict
		}
		if snapshot.ControllerSchema == nil || snapshot.ControllerID == nil || snapshot.ControllerVersion == nil ||
			snapshot.ControllerProtocol == nil || snapshot.ControllerTrustDigest == nil ||
			snapshot.OperationRequestHash == nil || snapshot.LastAttemptOrdinal == nil ||
			snapshot.LastAttemptKind == nil || snapshot.LastAttemptWorkerID == nil ||
			snapshot.LastAttemptFenceEpoch == nil || snapshot.LastAttemptStartedAt == nil ||
			snapshot.LastAttemptCompleted == nil || snapshot.LastAttemptOutcome == nil ||
			snapshot.LastAttemptErrorCode == nil || snapshot.LastAttemptErrorDetail == nil ||
			*snapshot.LastAttemptOutcome != "quarantined" ||
			*snapshot.LastAttemptErrorCode != *snapshot.LastErrorCode ||
			*snapshot.LastAttemptErrorDetail != *snapshot.LastErrorDetail {
			return fmt.Errorf("%w: blocked delivery evidence is incomplete", ErrBundleIntegrity)
		}
		controller := DeliveryControllerIdentity{
			SchemaVersion: *snapshot.ControllerSchema, ID: *snapshot.ControllerID,
			Version: *snapshot.ControllerVersion, Protocol: *snapshot.ControllerProtocol,
			TrustKeyDigest: *snapshot.ControllerTrustDigest,
		}
		if controller != store.controller {
			return fmt.Errorf("%w: blocked Operation is pinned to a different controller identity", ErrDeliveryReconciliationConflict)
		}
		var lastObservation *DeliveryReconciliationObservation
		if snapshot.LastObservationSeq != nil && snapshot.LastObservedAt != nil {
			lastObservation = &DeliveryReconciliationObservation{
				Sequence:   *snapshot.LastObservationSeq,
				ObservedAt: snapshot.LastObservedAt.UTC().Truncate(time.Microsecond),
			}
		}
		var createdAt time.Time
		if err := transaction.Raw(`SELECT statement_timestamp()`).Row().Scan(&createdAt); err != nil {
			return fmt.Errorf("load database reconciliation audit time: %w", err)
		}
		createdAt = createdAt.UTC().Truncate(time.Microsecond)
		resolved, err = newDeliveryReconciliationCase(newDeliveryReconciliationCaseInput{Case: DeliveryReconciliationCase{
			ID: input.ID, ProjectID: input.ProjectID, RunKind: input.RunKind, RunID: input.RunID,
			RunSchemaVersion: snapshot.RunSchemaVersion, ExpectedRunVersion: input.ExpectedVersion,
			OperationID: *snapshot.OperationID, OperationRequestHash: *snapshot.OperationRequestHash,
			Controller: controller, ResumeRemoteState: snapshot.ResumeRemoteState,
			SubmitAttemptCount:    snapshot.SubmitAttemptCount,
			ReconcileAttemptCount: snapshot.ReconcileAttemptCount,
			LastAttempt: DeliveryReconciliationAttempt{
				Ordinal: *snapshot.LastAttemptOrdinal, Kind: DeliveryAttemptKind(*snapshot.LastAttemptKind),
				WorkerID: *snapshot.LastAttemptWorkerID, FenceEpoch: *snapshot.LastAttemptFenceEpoch,
				StartedAt:   snapshot.LastAttemptStartedAt.UTC().Truncate(time.Microsecond),
				CompletedAt: snapshot.LastAttemptCompleted.UTC().Truncate(time.Microsecond),
				Outcome:     *snapshot.LastAttemptOutcome, ErrorCode: *snapshot.LastAttemptErrorCode,
				ErrorDetail: *snapshot.LastAttemptErrorDetail,
			},
			LastObservation: lastObservation,
			QuarantineError: DeliveryReconciliationError{Code: *snapshot.LastErrorCode, Detail: *snapshot.LastErrorDetail},
			ActorID:         input.ActorID, Reason: strings.TrimSpace(input.Reason),
			IdempotencyKey: input.IdempotencyKey, RequestHash: input.RequestHash, CreatedAt: createdAt,
		}})
		if err != nil {
			return err
		}
		document, err := domain.CanonicalJSON(resolved)
		if err != nil {
			return invalid("delivery reconciliation Case document")
		}
		if err := insertDeliveryReconciliationCase(transaction, resolved, document); err != nil {
			return err
		}
		operationUpdate := transaction.Exec(`
UPDATE release_delivery_operations
SET remote_state = ?, next_attempt_at = statement_timestamp(),
    last_error_code = NULL, last_error_detail = NULL,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND request_hash = ? AND remote_state = 'quarantined'
`, resolved.ResumeRemoteState, resolved.OperationID, resolved.OperationRequestHash)
		if operationUpdate.Error != nil {
			return operationUpdate.Error
		}
		if operationUpdate.RowsAffected != 1 {
			return ErrDeliveryReconciliationConflict
		}
		table := "release_preview_runs"
		if input.RunKind == DeliveryOperationProduction {
			table = "release_deployment_runs"
		}
		runUpdate := transaction.Exec(fmt.Sprintf(`
UPDATE %s
SET state = 'reconcile_wait', version = version + 1, updated_by = ?,
    updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
WHERE id = ? AND project_id = ? AND state = 'reconcile_blocked' AND version = ?
  AND lease_worker_id IS NULL AND lease_epoch IS NULL AND lease_expires_at IS NULL
`, table), input.ActorID, input.RunID, input.ProjectID, input.ExpectedVersion)
		if runUpdate.Error != nil {
			return runUpdate.Error
		}
		if runUpdate.RowsAffected != 1 {
			return ErrDeliveryReconciliationConflict
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isDeliveryReconciliationConflict(err) {
			return DeliveryReconciliationCase{}, false, ErrDeliveryReconciliationConflict
		}
		return DeliveryReconciliationCase{}, false, err
	}
	return resolved, replayed, nil
}

func loadBlockedDeliverySnapshot(
	transaction *gorm.DB,
	input ResumeBlockedDeliveryInput,
	lock bool,
) (blockedDeliverySnapshot, error) {
	table, runLink := "release_preview_runs", "operation.preview_run_id"
	if input.RunKind == DeliveryOperationProduction {
		table, runLink = "release_deployment_runs", "operation.deployment_run_id"
	}
	var snapshot blockedDeliverySnapshot
	lockClause := ""
	if lock {
		// operation is the nullable side for historical v1 Runs, so PostgreSQL
		// cannot lock it in this outer-join statement. Lock the Run here and the
		// exact Operation immediately after its identity is projected.
		lockClause = "FOR UPDATE OF run"
	}
	query := fmt.Sprintf(`
SELECT run.schema_version AS run_schema_version, run.state AS run_state,
       run.version AS run_version, run.lease_worker_id, run.lease_epoch, run.lease_expires_at,
       operation.id::text AS operation_id, operation.request_hash AS operation_request_hash,
       operation.controller_schema_version, operation.controller_id, operation.controller_version,
       operation.controller_protocol, operation.controller_trust_key_digest,
       operation.remote_state, COALESCE(operation.submit_attempt_count, 0) AS submit_attempt_count,
       COALESCE(operation.reconcile_attempt_count, 0) AS reconcile_attempt_count,
       operation.last_observation_sequence, operation.last_observed_at,
       operation.last_error_code, operation.last_error_detail,
       attempt.ordinal AS last_attempt_ordinal, attempt.kind AS last_attempt_kind,
       attempt.worker_id AS last_attempt_worker_id, attempt.fence_epoch AS last_attempt_fence_epoch,
       attempt.started_at AS last_attempt_started_at, attempt.completed_at AS last_attempt_completed_at,
       attempt.outcome AS last_attempt_outcome, attempt.error_code AS last_attempt_error_code,
       attempt.error_detail AS last_attempt_error_detail,
       COALESCE((
         SELECT observed.outcome
         FROM release_delivery_operation_attempts AS observed
         WHERE observed.operation_id = operation.id
           AND observed.outcome IN ('accepted','running')
           AND observed.observation_sequence IS NOT NULL
           AND observed.observed_at IS NOT NULL
         ORDER BY observed.ordinal DESC LIMIT 1
       ), 'submit_unknown') AS resume_remote_state
FROM %s AS run
LEFT JOIN release_delivery_operations AS operation ON %s = run.id
LEFT JOIN LATERAL (
  SELECT evidence.*
  FROM release_delivery_operation_attempts AS evidence
  WHERE evidence.operation_id = operation.id
  ORDER BY evidence.ordinal DESC LIMIT 1
) AS attempt ON true
WHERE run.id = ? AND run.project_id = ?
%s
`, table, runLink, lockClause)
	if err := transaction.Raw(query, input.RunID, input.ProjectID).Scan(&snapshot).Error; err != nil {
		return blockedDeliverySnapshot{}, err
	}
	if snapshot.RunSchemaVersion == "" {
		return blockedDeliverySnapshot{}, ErrDeliveryReconciliationNotFound
	}
	if lock && snapshot.OperationID != nil {
		var lockedOperationID string
		if err := transaction.Raw(`
SELECT id::text FROM release_delivery_operations WHERE id = ? FOR UPDATE
`, *snapshot.OperationID).Row().Scan(&lockedOperationID); err != nil || lockedOperationID != *snapshot.OperationID {
			if err == nil {
				err = ErrDeliveryReconciliationConflict
			}
			return blockedDeliverySnapshot{}, err
		}
	}
	return snapshot, nil
}

func (store *Store) GetBlockedDeliveryReconciliationSnapshot(
	ctx context.Context,
	projectID string,
	runKind DeliveryOperationKind,
	runID string,
) (DeliveryReconciliationBlockSnapshot, error) {
	if ctx == nil || !validUUID(projectID) || !validUUID(runID) ||
		(runKind != DeliveryOperationPreview && runKind != DeliveryOperationProduction) {
		return DeliveryReconciliationBlockSnapshot{}, invalid("blocked delivery reconciliation snapshot input")
	}
	snapshot, err := loadBlockedDeliverySnapshot(store.database.WithContext(ctx), ResumeBlockedDeliveryInput{
		ProjectID: projectID, RunKind: runKind, RunID: runID,
	}, false)
	if err != nil {
		return DeliveryReconciliationBlockSnapshot{}, err
	}
	if snapshot.RunSchemaVersion == "release-preview-run/v1" ||
		snapshot.RunSchemaVersion == "release-deployment-run/v1" {
		return DeliveryReconciliationBlockSnapshot{}, ErrDeliveryReconciliationLegacy
	}
	if snapshot.RunState != string(DeliveryReconcileBlocked) || snapshot.OperationID == nil ||
		snapshot.OperationRequestHash == nil || snapshot.RemoteState == nil || *snapshot.RemoteState != "quarantined" ||
		snapshot.LastErrorCode == nil || snapshot.LastErrorDetail == nil ||
		snapshot.ControllerSchema == nil || snapshot.ControllerID == nil || snapshot.ControllerVersion == nil ||
		snapshot.ControllerProtocol == nil || snapshot.ControllerTrustDigest == nil ||
		snapshot.LeaseWorkerID != nil || snapshot.LeaseEpoch != nil || snapshot.LeaseExpiresAt != nil {
		return DeliveryReconciliationBlockSnapshot{}, ErrDeliveryReconciliationConflict
	}
	return DeliveryReconciliationBlockSnapshot{
		SchemaVersion: "release-delivery-reconciliation-block/v1",
		ProjectID:     projectID, RunKind: runKind, RunID: runID,
		RunSchemaVersion: snapshot.RunSchemaVersion, ExpectedRunVersion: snapshot.RunVersion,
		OperationID: *snapshot.OperationID, OperationRequestHash: *snapshot.OperationRequestHash,
		Controller: DeliveryControllerIdentity{
			SchemaVersion: *snapshot.ControllerSchema, ID: *snapshot.ControllerID,
			Version: *snapshot.ControllerVersion, Protocol: *snapshot.ControllerProtocol,
			TrustKeyDigest: *snapshot.ControllerTrustDigest,
		},
		LastError: DeliveryReconciliationError{Code: *snapshot.LastErrorCode, Detail: *snapshot.LastErrorDetail},
	}, nil
}

func insertDeliveryReconciliationCase(
	transaction *gorm.DB,
	value DeliveryReconciliationCase,
	document []byte,
) error {
	var observationSequence, observationTime any
	if value.LastObservation != nil {
		observationSequence, observationTime = value.LastObservation.Sequence, value.LastObservation.ObservedAt
	}
	return transaction.Exec(`
INSERT INTO release_delivery_reconciliation_cases (
  id, schema_version, project_id, run_kind, run_id, run_schema_version,
  expected_run_version, operation_id, operation_request_hash,
  controller_schema_version, controller_id, controller_version, controller_protocol,
  controller_trust_key_digest, previous_remote_state, resume_remote_state,
  submit_attempt_count, reconcile_attempt_count,
  last_attempt_ordinal, last_attempt_kind, last_attempt_worker_id, last_attempt_fence_epoch,
  last_attempt_started_at, last_attempt_completed_at, last_attempt_outcome,
  last_observation_sequence, last_observed_at,
  quarantine_error_code, quarantine_error_detail, actor_id, reason,
  idempotency_key, request_hash, case_document, case_hash, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?
)
`, value.ID, value.SchemaVersion, value.ProjectID, value.RunKind, value.RunID, value.RunSchemaVersion,
		value.ExpectedRunVersion, value.OperationID, value.OperationRequestHash,
		value.Controller.SchemaVersion, value.Controller.ID, value.Controller.Version,
		value.Controller.Protocol, value.Controller.TrustKeyDigest,
		value.PreviousRemoteState, value.ResumeRemoteState,
		value.SubmitAttemptCount, value.ReconcileAttemptCount,
		value.LastAttempt.Ordinal, value.LastAttempt.Kind, value.LastAttempt.WorkerID,
		value.LastAttempt.FenceEpoch, value.LastAttempt.StartedAt, value.LastAttempt.CompletedAt,
		value.LastAttempt.Outcome, observationSequence, observationTime,
		value.QuarantineError.Code, value.QuarantineError.Detail, value.ActorID, value.Reason,
		value.IdempotencyKey, value.RequestHash, string(document), value.CaseHash, value.CreatedAt).Error
}

func (store *Store) GetDeliveryReconciliationCase(
	ctx context.Context,
	projectID, caseID string,
) (DeliveryReconciliationCase, error) {
	var row deliveryReconciliationCaseRow
	err := store.database.WithContext(ctx).
		Where("project_id = ? AND id = ?", projectID, caseID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DeliveryReconciliationCase{}, ErrDeliveryReconciliationNotFound
	}
	if err != nil {
		return DeliveryReconciliationCase{}, err
	}
	return deliveryReconciliationCaseFromRow(row)
}

func (store *Store) ListDeliveryReconciliationCases(
	ctx context.Context,
	projectID string,
	limit int,
) ([]DeliveryReconciliationCase, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []deliveryReconciliationCaseRow
	if err := store.database.WithContext(ctx).Where("project_id = ?", projectID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]DeliveryReconciliationCase, 0, len(rows))
	for _, row := range rows {
		value, err := deliveryReconciliationCaseFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func loadDeliveryReconciliationCaseByKey(
	transaction *gorm.DB,
	projectID, idempotencyKey string,
) (deliveryReconciliationCaseRow, bool, error) {
	var row deliveryReconciliationCaseRow
	err := transaction.Where("project_id = ? AND idempotency_key = ?", projectID, idempotencyKey).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return deliveryReconciliationCaseRow{}, false, nil
	}
	return row, err == nil, err
}

func deliveryReconciliationCaseFromRow(row deliveryReconciliationCaseRow) (DeliveryReconciliationCase, error) {
	var value DeliveryReconciliationCase
	if err := decodeReleaseStrictJSON(json.RawMessage(row.CaseDocument), &value); err != nil {
		return DeliveryReconciliationCase{}, fmt.Errorf("%w: decode reconciliation Case: %v", ErrBundleIntegrity, err)
	}
	parsed, err := ParseDeliveryReconciliationCase(value)
	if err != nil {
		return DeliveryReconciliationCase{}, fmt.Errorf("%w: invalid reconciliation Case: %v", ErrBundleIntegrity, err)
	}
	canonical, err := domain.CanonicalJSON(parsed)
	if err != nil || !bytes.Equal(canonical, []byte(row.CaseDocument)) ||
		parsed.ID != row.ID || parsed.SchemaVersion != row.SchemaVersion || parsed.ProjectID != row.ProjectID ||
		string(parsed.RunKind) != row.RunKind || parsed.RunID != row.RunID ||
		parsed.RunSchemaVersion != row.RunSchemaVersion || parsed.ExpectedRunVersion != row.ExpectedRunVersion ||
		parsed.OperationID != row.OperationID || parsed.OperationRequestHash != row.OperationRequestHash ||
		parsed.CaseHash != row.CaseHash || parsed.RequestHash != row.RequestHash || parsed.ActorID != row.ActorID ||
		parsed.IdempotencyKey != row.IdempotencyKey || !parsed.CreatedAt.Equal(row.CreatedAt) {
		return DeliveryReconciliationCase{}, fmt.Errorf("%w: reconciliation Case projection mismatch", ErrBundleIntegrity)
	}
	return parsed, nil
}

func isDeliveryReconciliationConflict(err error) bool {
	if errors.Is(err, ErrDeliveryReconciliationConflict) {
		return true
	}
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) {
		return false
	}
	return postgres.Code == "40001" || postgres.Code == "23505" || postgres.Code == "55000"
}
