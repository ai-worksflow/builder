package workflowqualificationactivation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/workflowinputauthority"
)

const (
	defaultPostgresTransactionRetries = 3
	maximumPostgresTransactionRetries = 10
	postgresCleanupTimeout            = 5 * time.Second
	qualityNodeType                   = "quality_gate"
)

type PostgresStoreConfig struct {
	MaxTransactionRetries int
}

// PostgresStore owns the Workflow activation transaction. The supplied pool
// must use the application primary role. No statement is issued between BEGIN
// and workflowinputauthority.PostgresStore.Freeze, which makes Freeze's shared
// rollout fence the first database authority entrypoint.
type PostgresStore struct {
	database              *sql.DB
	inputAuthority        *workflowinputauthority.PostgresStore
	commit                func(*sql.Tx) error
	cleanupTimeout        time.Duration
	maxTransactionRetries int
}

func NewPostgresStore(database *sql.DB, config PostgresStoreConfig) (*PostgresStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL database is required", ErrInvalid)
	}
	if config.MaxTransactionRetries < 0 || config.MaxTransactionRetries > maximumPostgresTransactionRetries {
		return nil, fmt.Errorf("%w: MaxTransactionRetries must be between 0 and %d", ErrInvalid, maximumPostgresTransactionRetries)
	}
	inputAuthority, err := workflowinputauthority.NewPostgresStore(database)
	if err != nil {
		return nil, errors.Join(ErrInvalid, err)
	}
	retries := config.MaxTransactionRetries
	if retries == 0 {
		retries = defaultPostgresTransactionRetries
	}
	return &PostgresStore{
		database: database, inputAuthority: inputAuthority,
		commit:         func(transaction *sql.Tx) error { return transaction.Commit() },
		cleanupTimeout: postgresCleanupTimeout, maxTransactionRetries: retries,
	}, nil
}

type activationFacts struct {
	expected          workflowinputauthority.Record
	qualityNodeRunID  uuid.UUID
	qualityNodeKey    string
	activationEventID uuid.UUID
	completionEventID CompletionEventID
}

func compileActivationFacts(completionEventID CompletionEventID, candidate workflowinputauthority.Candidate) (activationFacts, error) {
	if !completionEventID.valid() {
		return activationFacts{}, ErrInvalid
	}
	expected, err := workflowinputauthority.Compile(candidate)
	if err != nil {
		return activationFacts{}, errors.Join(ErrInvalid, err)
	}
	activationEventID, err := uuid.Parse(expected.Input.Gate.ActivationEventID)
	if err != nil || activationEventID.Version() != 4 {
		return activationFacts{}, fmt.Errorf("%w: activation event identity is invalid", ErrInvalid)
	}
	qualityNodeRunID := uuid.Nil
	qualityNodeKey := ""
	for _, predecessor := range expected.Input.Predecessors {
		if predecessor.SourceNodeType != qualityNodeType {
			continue
		}
		parsed, parseErr := uuid.Parse(predecessor.SourceNodeRunID)
		if parseErr != nil || parsed.Version() != 4 || qualityNodeRunID != uuid.Nil {
			return activationFacts{}, fmt.Errorf("%w: Candidate does not bind one exact Quality source node", ErrInvalid)
		}
		qualityNodeRunID = parsed
		qualityNodeKey = predecessor.SourceNodeKey
	}
	if qualityNodeRunID == uuid.Nil || qualityNodeKey == "" {
		return activationFacts{}, fmt.Errorf("%w: Candidate has no Quality source node", ErrInvalid)
	}
	if completionEventID.String() == expected.OperationID.String() ||
		completionEventID.String() == expected.AuthorityID.String() ||
		completionEventID.String() == activationEventID.String() ||
		completionEventID.String() == expected.WorkflowRunID.String() ||
		completionEventID.String() == expected.NodeRunID.String() ||
		completionEventID.String() == qualityNodeRunID.String() {
		return activationFacts{}, fmt.Errorf("%w: completion event identity collides with durable authority", ErrConflict)
	}
	return activationFacts{
		expected: expected, qualityNodeRunID: qualityNodeRunID,
		qualityNodeKey: qualityNodeKey, activationEventID: activationEventID,
		completionEventID: completionEventID,
	}, nil
}

func (store *PostgresStore) Activate(
	ctx context.Context,
	completionEventID CompletionEventID,
	candidate workflowinputauthority.Candidate,
) (Record, error) {
	if store == nil || store.database == nil || store.inputAuthority == nil || isNilInterface(ctx) {
		return Record{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	facts, err := compileActivationFacts(completionEventID, candidate)
	if err != nil {
		return Record{}, err
	}
	for attempt := 0; attempt <= store.maxTransactionRetries; attempt++ {
		record, attemptErr := store.activateAttempt(ctx, candidate, facts)
		if !errors.Is(attemptErr, errPostgresRetryableAttempt) {
			return record, attemptErr
		}
		if attempt == store.maxTransactionRetries || ctx.Err() != nil {
			return Record{}, ErrRetryable
		}
	}
	return Record{}, ErrRetryable
}

var errPostgresRetryableAttempt = errors.New("workflow qualification activation PostgreSQL attempt was definitely aborted")

func (store *PostgresStore) activateAttempt(
	ctx context.Context,
	candidate workflowinputauthority.Candidate,
	facts activationFacts,
) (_ Record, returnedErr error) {
	connection, err := store.database.Conn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return Record{}, ctx.Err()
		}
		return Record{}, ErrStoreOutcomeUnknown
	}
	connectionReusable := true
	defer func() {
		_ = releasePostgresConnection(connection, !connectionReusable, store.cleanupDuration())
	}()

	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		connectionReusable = false
		return Record{}, ErrStoreOutcomeUnknown
	}
	transactionFinished := false
	defer func() {
		if transactionFinished {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
		defer cancel()
		rollbackErr := rollbackTransaction(rollbackCtx, transaction)
		transactionFinished = true
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			connectionReusable = false
			returnedErr = ErrStoreOutcomeUnknown
		}
	}()

	transactionToken, err := workflowinputauthority.NewPostgresTransaction(transaction)
	if err != nil {
		return Record{}, errors.Join(ErrInvalid, err)
	}
	// No database statement may be inserted before this call. Freeze acquires
	// the shared migration fence before its first relation read.
	authority, err := store.inputAuthority.Freeze(ctx, transactionToken, candidate)
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, classifyInputAuthorityError(err)
	}
	if !sameExpectedAuthority(facts.expected, authority) {
		return Record{}, ErrConflict
	}

	if authority.Idempotent {
		exact, primary, closureErr := inspectActivationClosure(ctx, transaction, facts, authority)
		if closureErr != nil {
			if isDefinitePostgresRetryable(closureErr) {
				return Record{}, errPostgresRetryableAttempt
			}
			return Record{}, classifyPostgresError(closureErr)
		}
		if !primary {
			return Record{}, ErrNotReady
		}
		if !exact {
			return Record{}, ErrConflict
		}
	} else if err := activateWorkflowClosure(ctx, transaction, facts, authority); err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, classifyPostgresError(err)
	}

	commit := store.commit
	if commit == nil {
		commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	}
	if err := commit(transaction); err != nil {
		transactionFinished = true
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		connectionReusable = false
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
		_ = rollbackTransaction(cleanupCtx, transaction)
		cancel()
		return Record{}, ErrStoreOutcomeUnknown
	}
	transactionFinished = true
	return recordFromAuthority(facts.completionEventID, authority, authority.Idempotent), nil
}

func activateWorkflowClosure(
	ctx context.Context,
	transaction *sql.Tx,
	facts activationFacts,
	authority workflowinputauthority.Record,
) error {
	var nodeRunID uuid.UUID
	if err := transaction.QueryRowContext(ctx, postgresActivateNodeQuery,
		facts.completionEventID.String(), authority.WorkflowRunID, facts.qualityNodeRunID,
		facts.qualityNodeKey, authority.Request.ExpectedRunCursor, authority.NodeRunID,
		authority.AuthorityID, authority.Input.Gate.NodeKey,
	).Scan(&nodeRunID); err != nil {
		return activationStatementError("attach input authority to gate node", err)
	}
	if nodeRunID != authority.NodeRunID {
		return ErrConflict
	}

	var occurredAt time.Time
	if err := transaction.QueryRowContext(ctx, postgresInsertActivationEventQuery,
		facts.activationEventID, authority.WorkflowRunID,
		authority.Input.Gate.ActivationEventSequence, authority.Input.Gate.NodeKey,
		authority.AuthorityID, authority.NodeRunID,
	).Scan(&occurredAt); err != nil {
		return activationStatementError("append activation event", err)
	}
	if occurredAt.IsZero() {
		return ErrConflict
	}

	var outboxID uuid.UUID
	if err := transaction.QueryRowContext(ctx, postgresInsertActivationOutboxQuery,
		facts.activationEventID, authority.Input.Project.ID,
	).Scan(&outboxID); err != nil {
		return activationStatementError("append activation outbox", err)
	}
	if outboxID != facts.activationEventID {
		return ErrConflict
	}

	var workflowRunID uuid.UUID
	if err := transaction.QueryRowContext(ctx, postgresActivateRunQuery,
		authority.WorkflowRunID, authority.Input.Gate.ActivationEventSequence,
		authority.Input.Project.ID, authority.Request.ExpectedRunCursor,
	).Scan(&workflowRunID); err != nil {
		return activationStatementError("advance workflow run", err)
	}
	if workflowRunID != authority.WorkflowRunID {
		return ErrConflict
	}
	if _, err := transaction.ExecContext(ctx, postgresImmediateConstraintsQuery); err != nil {
		return activationStatementError("validate deferred activation closure", err)
	}
	exact, primary, err := inspectActivationClosure(ctx, transaction, facts, authority)
	if err != nil {
		return err
	}
	if !primary {
		return ErrNotReady
	}
	if !exact {
		return ErrConflict
	}
	return nil
}

func (store *PostgresStore) Inspect(
	ctx context.Context,
	completionEventID CompletionEventID,
	candidate workflowinputauthority.Candidate,
) (Record, error) {
	if store == nil || store.database == nil || store.inputAuthority == nil || isNilInterface(ctx) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	facts, err := compileActivationFacts(completionEventID, candidate)
	if err != nil {
		return Record{}, err
	}
	for attempt := 0; attempt <= store.maxTransactionRetries; attempt++ {
		record, attemptErr := store.inspectAttempt(ctx, candidate, facts)
		if !errors.Is(attemptErr, errPostgresRetryableAttempt) {
			return record, attemptErr
		}
		if attempt == store.maxTransactionRetries || ctx.Err() != nil {
			return Record{}, ErrOutcomeUnknown
		}
	}
	return Record{}, ErrOutcomeUnknown
}

func (store *PostgresStore) inspectAttempt(
	ctx context.Context,
	candidate workflowinputauthority.Candidate,
	facts activationFacts,
) (_ Record, returnedErr error) {
	connection, err := store.database.Conn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return Record{}, ctx.Err()
		}
		return Record{}, ErrOutcomeUnknown
	}
	connectionReusable := true
	defer func() {
		_ = releasePostgresConnection(connection, !connectionReusable, store.cleanupDuration())
	}()

	// This single preflight statement rejects a replica and proves that an
	// immutable operation exists. It runs before, not inside, the transaction;
	// the transaction's first database authority entry remains Freeze.
	var primaryReadWrite, operationExists bool
	if err := connection.QueryRowContext(ctx, postgresInspectPreflightQuery, facts.expected.OperationID).Scan(
		&primaryReadWrite, &operationExists,
	); err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, classifyInspectPostgresError(err)
	}
	if !primaryReadWrite {
		return Record{}, ErrOutcomeUnknown
	}
	if !operationExists {
		return Record{}, ErrNotFound
	}

	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		connectionReusable = false
		return Record{}, ErrOutcomeUnknown
	}
	transactionFinished := false
	defer func() {
		if transactionFinished {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
		defer cancel()
		rollbackErr := rollbackTransaction(rollbackCtx, transaction)
		transactionFinished = true
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			connectionReusable = false
			returnedErr = ErrOutcomeUnknown
		}
	}()

	transactionToken, err := workflowinputauthority.NewPostgresTransaction(transaction)
	if err != nil {
		return Record{}, ErrInvalid
	}
	// Since the immutable operation was found on this same primary session,
	// Freeze can only take its exact replay branch. It byte-compares the full
	// Candidate and supplies the shared rollout fence as the first statement.
	authority, err := store.inputAuthority.Freeze(ctx, transactionToken, candidate)
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, classifyInputAuthorityError(err)
	}
	if !authority.Idempotent || !sameExpectedAuthority(facts.expected, authority) {
		return Record{}, ErrConflict
	}
	exact, primaryReadWrite, err := inspectActivationClosure(ctx, transaction, facts, authority)
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, classifyInspectPostgresError(err)
	}
	if !primaryReadWrite {
		return Record{}, ErrOutcomeUnknown
	}
	if !exact {
		return Record{}, ErrConflict
	}

	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
	rollbackErr := rollbackTransaction(rollbackCtx, transaction)
	cancel()
	transactionFinished = true
	if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		connectionReusable = false
		return Record{}, ErrOutcomeUnknown
	}
	return recordFromAuthority(facts.completionEventID, authority, true), nil
}

func (store *PostgresStore) cleanupDuration() time.Duration {
	if store.cleanupTimeout <= 0 || store.cleanupTimeout > postgresCleanupTimeout {
		return postgresCleanupTimeout
	}
	return store.cleanupTimeout
}

func inspectActivationClosure(
	ctx context.Context,
	transaction *sql.Tx,
	facts activationFacts,
	authority workflowinputauthority.Record,
) (exact bool, primaryReadWrite bool, err error) {
	err = transaction.QueryRowContext(ctx, postgresInspectActivationClosureQuery,
		facts.completionEventID.String(), authority.WorkflowRunID,
		facts.qualityNodeRunID, facts.qualityNodeKey,
		authority.Request.ExpectedRunCursor, authority.NodeRunID,
		authority.AuthorityID, authority.Input.Gate.NodeKey,
		facts.activationEventID, authority.Input.Gate.ActivationEventSequence,
		authority.Input.Project.ID,
	).Scan(&primaryReadWrite, &exact)
	return exact, primaryReadWrite, err
}

func sameExpectedAuthority(expected, actual workflowinputauthority.Record) bool {
	return expected.OperationID == actual.OperationID && expected.AuthorityID == actual.AuthorityID &&
		expected.WorkflowRunID == actual.WorkflowRunID && expected.NodeRunID == actual.NodeRunID &&
		expected.RequestHash == actual.RequestHash && expected.TargetHash == actual.TargetHash &&
		expected.InputHash == actual.InputHash && expected.AuthorityHash == actual.AuthorityHash &&
		expected.Input.Gate.ActivationEventID == actual.Input.Gate.ActivationEventID &&
		expected.Input.Gate.ActivationEventSequence == actual.Input.Gate.ActivationEventSequence
}

func activationStatementError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(ErrConflict, fmt.Errorf("%s returned no exact row", operation))
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func classifyInputAuthorityError(err error) error {
	if errors.Is(err, workflowinputauthority.ErrInvalid) {
		return ErrInvalid
	}
	if errors.Is(err, workflowinputauthority.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, workflowinputauthority.ErrConflict) || errors.Is(err, workflowinputauthority.ErrStale) {
		return ErrConflict
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if classified := classifyPostgresError(err); !errors.Is(classified, ErrOutcomeUnknown) {
		return classified
	}
	return ErrOutcomeUnknown
}

func classifyPostgresError(err error) error {
	if err == nil {
		return nil
	}
	if isDefinitePostgresRetryable(err) {
		return ErrRetryable
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotReady) {
		return err
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrOutcomeUnknown
	}
	switch postgresError.Code {
	case "WIA03", "WQC01", "22001", "22003", "22007", "22021", "22023", "22P02", "23502", "23514":
		return ErrInvalid
	case "WIA02", "WIA04", "WQC02", "WQC03", "WQC04", "23503", "23505", "23P01":
		return ErrConflict
	case "25006", "42501", "42883", "42P01":
		return ErrNotReady
	default:
		return ErrOutcomeUnknown
	}
}

func classifyInspectPostgresError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	classified := classifyPostgresError(err)
	if errors.Is(classified, ErrInvalid) || errors.Is(classified, ErrConflict) {
		return classified
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrOutcomeUnknown
}

func isDefinitePostgresRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return false
	}
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) &&
		(postgresError.Code == "40001" || postgresError.Code == "40P01" || postgresError.Code == "WIA01")
}

func rollbackTransaction(ctx context.Context, transaction *sql.Tx) error {
	if transaction == nil || isNilInterface(ctx) {
		return errors.New("PostgreSQL transaction and cleanup context are required")
	}
	result := make(chan error, 1)
	go func() {
		result <- transaction.Rollback()
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("PostgreSQL rollback cleanup deadline: %w", ctx.Err()))
	}
}

func releasePostgresConnection(connection *sql.Conn, discard bool, timeout time.Duration) error {
	if connection == nil {
		return nil
	}
	if timeout <= 0 || timeout > postgresCleanupTimeout {
		timeout = postgresCleanupTimeout
	}
	result := make(chan error, 1)
	go func() {
		var poisonErr error
		if discard {
			poisonErr = poisonPostgresConnection(connection)
		}
		result <- errors.Join(poisonErr, connection.Close())
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		return errors.Join(ErrStoreOutcomeUnknown, errors.New("PostgreSQL connection discard deadline exceeded"))
	}
}

func poisonPostgresConnection(connection *sql.Conn) error {
	if connection == nil {
		return nil
	}
	err := connection.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}

const postgresActivateNodeQuery = `
UPDATE workflow_node_runs AS gate
SET status = 'waiting_qualification', input_authority_id = $7
WHERE gate.id = $6
  AND gate.run_id = $2
  AND gate.node_key = $8
  AND gate.node_type = 'external_qualification_gate'
  AND gate.definition_node_id = 'external-qualification'
  AND gate.slice_kind = 'root' AND gate.slice_id IS NULL
  AND gate.status = 'pending' AND gate.input_authority_id IS NULL
  AND gate.attempt = 0 AND gate.lease_owner IS NULL AND gate.lease_expires_at IS NULL
  AND gate.started_at IS NULL AND gate.completed_at IS NULL AND gate.failure IS NULL
  AND gate.input_manifest_id IS NULL AND gate.output_proposal_id IS NULL
  AND gate.output_revision_id IS NULL
  AND EXISTS (
    SELECT 1
    FROM workflow_run_events AS source_event
    JOIN workflow_node_runs AS quality
      ON quality.id = $3 AND quality.run_id = source_event.run_id
     AND quality.node_key = source_event.node_key
    WHERE source_event.id = $1
      AND source_event.run_id = $2
      AND source_event.sequence = $5
      AND source_event.event_type = 'node.completed'
      AND source_event.node_key = $4
      AND quality.node_type = 'quality_gate'
      AND quality.status = 'completed'
  )
RETURNING gate.id`

const postgresInsertActivationEventQuery = `
INSERT INTO workflow_run_events(id,run_id,sequence,event_type,node_key,payload)
VALUES(
  $1,$2,$3,'external_qualification_activated',$4,
  pg_catalog.jsonb_build_object('inputAuthorityId',$5::text,'nodeRunId',$6::text)
)
RETURNING created_at`

const postgresInsertActivationOutboxQuery = `
INSERT INTO outbox_events(
  id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
  attempts,available_at,published_at,last_error,created_at
)
SELECT event.id,'workflow_run',event.run_id::text,event.event_type,
       'worksflow.workflow.run.event',
       pg_catalog.jsonb_build_object(
         'id',event.id::text,
         'projectId',$2::text,
         'runId',event.run_id::text,
         'sequence',event.sequence,
         'type',event.event_type,
         'occurredAt',pg_catalog.to_char(
           event.created_at AT TIME ZONE 'UTC',
           'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
         ),
         'payload',event.payload,
         'nodeKey',event.node_key
       ),
       '{}'::jsonb,0,event.created_at,NULL,NULL,event.created_at
FROM workflow_run_events AS event
WHERE event.id = $1
RETURNING id`

const postgresActivateRunQuery = `
UPDATE workflow_runs AS run
SET status = 'waiting_qualification', event_cursor = $2
WHERE run.id = $1
  AND run.project_id = $3
  AND run.event_cursor = $4
  AND run.status IN ('running','waiting_input','waiting_review')
  AND run.execution_profile_version = 'workflow-engine/v3'
  AND run.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
RETURNING run.id`

const postgresImmediateConstraintsQuery = `SET CONSTRAINTS ALL IMMEDIATE`

const postgresInspectPreflightQuery = `
SELECT
  NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off',
  EXISTS(
    SELECT 1
    FROM inspect_workflow_input_authority_operation_v1($1) AS value
  )`

const postgresInspectActivationClosureQuery = `
SELECT
  NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off',
  EXISTS (
    SELECT 1
    FROM workflow_runs AS run
    JOIN workflow_node_runs AS gate
      ON gate.id = $6 AND gate.run_id = run.id
    JOIN workflow_node_runs AS quality
      ON quality.id = $3 AND quality.run_id = run.id
    JOIN workflow_run_events AS source_event
      ON source_event.id = $1 AND source_event.run_id = run.id
    JOIN workflow_run_events AS activation_event
      ON activation_event.id = $9 AND activation_event.run_id = run.id
    JOIN outbox_events AS outbox
      ON outbox.id = activation_event.id
    WHERE run.id = $2
      AND run.project_id = $11
      AND run.execution_profile_version = 'workflow-engine/v3'
      AND run.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
      AND run.event_cursor >= $10
      AND run.status IN (
        'waiting_qualification','waiting_input','waiting_review',
        'running','completed','failed','cancelled','stale'
      )
      AND quality.node_key = $4
      AND quality.node_type = 'quality_gate'
      AND quality.status = 'completed'
      AND source_event.sequence = $5
      AND source_event.event_type = 'node.completed'
      AND source_event.node_key = quality.node_key
      AND gate.node_key = $8
      AND gate.node_type = 'external_qualification_gate'
      AND gate.definition_node_id = 'external-qualification'
      AND gate.slice_kind = 'root' AND gate.slice_id IS NULL
      AND gate.input_authority_id = $7
      AND gate.status IN ('waiting_qualification','completed','cancelled','stale')
      AND gate.attempt = 0 AND gate.lease_owner IS NULL AND gate.lease_expires_at IS NULL
      AND gate.started_at IS NULL AND gate.failure IS NULL
      AND gate.input_manifest_id IS NULL AND gate.output_proposal_id IS NULL
      AND (
        (gate.status = 'completed'
          AND gate.output_revision_id IS NOT NULL
          AND gate.completed_at IS NOT NULL)
        OR
        (gate.status IN ('waiting_qualification','cancelled','stale')
          AND gate.output_revision_id IS NULL
          AND gate.completed_at IS NULL)
      )
      AND activation_event.sequence = $10
      AND activation_event.event_type = 'external_qualification_activated'
      AND activation_event.node_key = gate.node_key
      AND activation_event.payload = pg_catalog.jsonb_build_object(
        'inputAuthorityId',$7::text,'nodeRunId',$6::text
      )
      AND activation_event.actor_id IS NULL
      AND outbox.aggregate_type = 'workflow_run'
      AND outbox.aggregate_id = run.id::text
      AND outbox.event_type = activation_event.event_type
      AND outbox.subject = 'worksflow.workflow.run.event'
      AND outbox.headers = '{}'::jsonb
      AND outbox.available_at = activation_event.created_at
      AND outbox.created_at = activation_event.created_at
      AND outbox.payload = pg_catalog.jsonb_build_object(
        'id',activation_event.id::text,
        'projectId',run.project_id::text,
        'runId',run.id::text,
        'sequence',activation_event.sequence,
        'type',activation_event.event_type,
        'occurredAt',pg_catalog.to_char(
          activation_event.created_at AT TIME ZONE 'UTC',
          'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
        ),
        'payload',activation_event.payload,
        'nodeKey',activation_event.node_key
      )
  )`

var _ AtomicStore = (*PostgresStore)(nil)
