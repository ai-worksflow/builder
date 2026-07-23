package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/domain"
	"gorm.io/gorm"
)

const postgresQualityCompletionPrecommitQuery = `
SELECT
  precommit_id::text,
  workflow_input_operation_id::text, workflow_input_authority_id::text,
  activation_event_id::text,
  project_id::text, workflow_run_id::text,
  quality_node_run_id::text, quality_node_key,
  gate_node_run_id::text, gate_node_key,
  expected_run_cursor, completion_event_sequence,
  completion_event_id::text, completion_event_payload,
  completion_event_actor_id::text, quality_completed_at,
  quality_lease_owner, quality_attempt, workspace_revision_id::text,
  gate_input_raw_bytes, gate_input_raw_bytes_hash,
  gate_input_raw_bytes_size, gate_input_semantic_hash,
  gate_input_binding_count
FROM precommit_workflow_v3_quality_completion_v1(
  ?,?,?,?,?,?,?,?,?,?,?,?,CAST(? AS jsonb),CAST(? AS uuid),?
)`

const postgresQualityCompletionInspectQuery = `
SELECT
  precommit_id::text,
  workflow_input_operation_id::text, workflow_input_authority_id::text,
  activation_event_id::text,
  project_id::text, workflow_run_id::text,
  quality_node_run_id::text, quality_node_key,
  gate_node_run_id::text, gate_node_key,
  expected_run_cursor, completion_event_sequence,
  completion_event_id::text, completion_event_payload,
  completion_event_actor_id::text, quality_completed_at,
  quality_lease_owner, quality_attempt, workspace_revision_id::text,
  gate_input_raw_bytes, gate_input_raw_bytes_hash,
  gate_input_raw_bytes_size, gate_input_semantic_hash,
  gate_input_binding_count
FROM inspect_workflow_v3_quality_completion_precommit_v1(?)`

type qualityCompletionPrecommitDatabaseRecord struct {
	PrecommitID              string
	WorkflowInputOperationID string
	WorkflowInputAuthorityID string
	ActivationEventID        string
	ProjectID                string
	WorkflowRunID            string
	QualityNodeRunID         string
	QualityNodeKey           string
	GateNodeRunID            string
	GateNodeKey              string
	ExpectedRunCursor        uint64
	CompletionEventSequence  uint64
	CompletionEventID        string
	CompletionEventPayload   json.RawMessage
	CompletionEventActorID   string
	CompletedAt              time.Time
	LeaseOwner               string
	LeaseAttempt             int
	OutputRevisionID         string
	GateInputCanonical       json.RawMessage
	GateInputRawHash         string
	GateInputRawSize         int64
	GateInputSemanticHash    string
	GateInputBindingCount    int
}

type qualityCompletionPrecommitRow interface {
	Scan(...any) error
}

func precommitQualityCompletionTx(
	tx *gorm.DB,
	precommit *QualityCompletionPrecommitMutation,
) (qualityCompletionPrecommitDatabaseRecord, error) {
	if tx == nil || precommit == nil {
		return qualityCompletionPrecommitDatabaseRecord{}, domain.ErrInvalidArgument
	}
	actor := any(nil)
	if precommit.CompletionEventActorID != "" {
		actor = precommit.CompletionEventActorID
	}
	record, err := scanQualityCompletionPrecommit(tx.Raw(
		postgresQualityCompletionPrecommitQuery,
		precommit.PrecommitID,
		precommit.WorkflowInputOperationID, precommit.WorkflowInputAuthorityID,
		precommit.ActivationEventID, precommit.WorkflowRunID,
		precommit.QualityNodeRunID, precommit.GateNodeRunID,
		precommit.ExpectedRunCursor, precommit.CompletionEventID,
		precommit.LeaseOwner, precommit.LeaseAttempt, precommit.CompletedAt,
		string(precommit.CompletionEventPayload), actor,
		[]byte(precommit.GateInputCanonical),
	).Row())
	if err != nil {
		return qualityCompletionPrecommitDatabaseRecord{}, mapQualityCompletionPostgresError("precommit", err)
	}
	if !record.matches(precommit) {
		return qualityCompletionPrecommitDatabaseRecord{}, errors.Join(
			ErrCASConflict, ErrQualityCompletionPrecommitCorrupt,
			fmt.Errorf("database-authored Quality completion precommit differs from mutation %s", precommit.PrecommitID),
		)
	}
	return record, nil
}

func (s *GORMStore) inspectQualityCompletionPrecommit(
	ctx context.Context,
	precommitID string,
) (qualityCompletionPrecommitDatabaseRecord, error) {
	if s == nil || s.db == nil {
		return qualityCompletionPrecommitDatabaseRecord{}, domain.ErrInvalidArgument
	}
	return scanQualityCompletionPrecommit(s.db.WithContext(ctx).Raw(
		postgresQualityCompletionInspectQuery, precommitID,
	).Row())
}

func scanQualityCompletionPrecommit(row qualityCompletionPrecommitRow) (qualityCompletionPrecommitDatabaseRecord, error) {
	var record qualityCompletionPrecommitDatabaseRecord
	var actor sql.NullString
	err := row.Scan(
		&record.PrecommitID,
		&record.WorkflowInputOperationID, &record.WorkflowInputAuthorityID,
		&record.ActivationEventID, &record.ProjectID, &record.WorkflowRunID,
		&record.QualityNodeRunID, &record.QualityNodeKey,
		&record.GateNodeRunID, &record.GateNodeKey,
		&record.ExpectedRunCursor, &record.CompletionEventSequence,
		&record.CompletionEventID, &record.CompletionEventPayload,
		&actor, &record.CompletedAt, &record.LeaseOwner,
		&record.LeaseAttempt, &record.OutputRevisionID,
		&record.GateInputCanonical, &record.GateInputRawHash,
		&record.GateInputRawSize, &record.GateInputSemanticHash,
		&record.GateInputBindingCount,
	)
	if err != nil {
		return qualityCompletionPrecommitDatabaseRecord{}, err
	}
	if actor.Valid {
		record.CompletionEventActorID = actor.String
	}
	return record, nil
}

func (record qualityCompletionPrecommitDatabaseRecord) matches(precommit *QualityCompletionPrecommitMutation) bool {
	return precommit != nil && record.PrecommitID == precommit.PrecommitID &&
		record.WorkflowInputOperationID == precommit.WorkflowInputOperationID &&
		record.WorkflowInputAuthorityID == precommit.WorkflowInputAuthorityID &&
		record.ActivationEventID == precommit.ActivationEventID &&
		record.ProjectID == precommit.ProjectID && record.WorkflowRunID == precommit.WorkflowRunID &&
		record.QualityNodeRunID == precommit.QualityNodeRunID && record.QualityNodeKey == precommit.QualityNodeKey &&
		record.GateNodeRunID == precommit.GateNodeRunID && record.GateNodeKey == precommit.GateNodeKey &&
		record.ExpectedRunCursor == precommit.ExpectedRunCursor &&
		record.CompletionEventSequence == precommit.CompletionEventSequence &&
		record.CompletionEventID == precommit.CompletionEventID &&
		record.CompletionEventActorID == precommit.CompletionEventActorID &&
		record.CompletedAt.Equal(precommit.CompletedAt) &&
		record.LeaseOwner == precommit.LeaseOwner && record.LeaseAttempt == precommit.LeaseAttempt &&
		record.OutputRevisionID == precommit.OutputRevisionID &&
		canonicalQualityCompletionJSONEqual(record.CompletionEventPayload, precommit.CompletionEventPayload) &&
		bytes.Equal(record.GateInputCanonical, precommit.GateInputCanonical) &&
		record.GateInputRawHash == precommit.GateInputRawHash && record.GateInputRawSize == precommit.GateInputRawSize &&
		record.GateInputSemanticHash == precommit.GateInputSemanticHash &&
		record.GateInputBindingCount == precommit.GateInputBindingCount
}

func (s *GORMStore) reconcileQualityCompletionCommitError(
	ctx context.Context,
	precommit *QualityCompletionPrecommitMutation,
	commitErr error,
) error {
	return reconcileQualityCompletionCommitOutcome(ctx, precommit, commitErr, s.inspectQualityCompletionPrecommit)
}

func reconcileQualityCompletionCommitOutcome(
	ctx context.Context,
	precommit *QualityCompletionPrecommitMutation,
	commitErr error,
	inspect func(context.Context, string) (qualityCompletionPrecommitDatabaseRecord, error),
) error {
	if precommit == nil {
		return commitErr
	}
	if inspect == nil {
		return fmt.Errorf("%w: precommit=%s", ErrQualityCompletionOutcomeUnknown, precommit.PrecommitID)
	}
	reconcile, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), qualityCompletionCommitUnknownInspectTimeout,
	)
	defer cancel()
	record, inspectErr := inspect(reconcile, precommit.PrecommitID)
	switch {
	case inspectErr == nil && record.matches(precommit):
		return nil
	case inspectErr == nil:
		return errors.Join(
			ErrCASConflict, ErrQualityCompletionPrecommitCorrupt,
			fmt.Errorf("inspect Quality completion precommit %s returned different immutable bytes", precommit.PrecommitID),
		)
	case errors.Is(inspectErr, sql.ErrNoRows):
		return mapQualityCompletionPostgresError("commit", commitErr)
	default:
		return fmt.Errorf(
			"%w: precommit=%s run=%s qualityNode=%s",
			ErrQualityCompletionOutcomeUnknown, precommit.PrecommitID,
			precommit.WorkflowRunID, precommit.QualityNodeRunID,
		)
	}
}

func mapWorkflowCommitPostgresError(precommit *QualityCompletionPrecommitMutation, err error) error {
	if precommit == nil {
		return err
	}
	return mapQualityCompletionPostgresError("commit", err)
}

func mapQualityCompletionPostgresError(operation string, err error) error {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return err
	}
	wrapped := fmt.Errorf("%s workflow v3 Quality completion: %w", operation, err)
	switch postgresError.Code {
	case "WQC01":
		return errors.Join(domain.ErrInvalidArgument, wrapped)
	case "WQC02":
		return errors.Join(ErrCASConflict, ErrQualityCompletionPrecommitCorrupt, wrapped)
	case "WQC03":
		return errors.Join(ErrCASConflict, ErrQualityCompletionPrecommitStale, wrapped)
	case "WQC04":
		return errors.Join(ErrCASConflict, ErrQualityCompletionPrecommitClosure, wrapped)
	case "40001", "40P01":
		return errors.Join(ErrCASConflict, ErrQualityCompletionRetryable, wrapped)
	default:
		return err
	}
}
