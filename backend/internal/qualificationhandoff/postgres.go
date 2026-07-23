package qualificationhandoff

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	postgresSessionCleanupTimeout     = 5 * time.Second
	defaultPostgresTransactionRetries = 3
	maximumPostgresTransactionRetries = 10
	postgresReadinessProbeHandoffID   = "00000000-0000-4000-8000-000000000082"
)

type PostgresSessionAffinityMode string

const (
	PostgresSessionAffinityUnverified      PostgresSessionAffinityMode = "unverified"
	PostgresSessionAffinityDirect          PostgresSessionAffinityMode = "direct"
	PostgresSessionAffinitySessionPool     PostgresSessionAffinityMode = "session-pool"
	PostgresSessionAffinityTransactionPool PostgresSessionAffinityMode = "transaction-pool"
)

type PostgresStoreConfig struct {
	SessionAffinityMode   PostgresSessionAffinityMode
	MaxTransactionRetries int
}

// PostgresStore is backed by one independently configured Handoff operator
// DSN. It has no Promotion/Input fallback, never reads or acknowledges the
// pending Outbox, and invokes no business capability other than the migration
// 82 Complete and Inspect routines. Durable dispatch/ack ownership remains in
// the worker layer outside this package.
type PostgresStore struct {
	database              *sql.DB
	commit                func(*sql.Tx) error
	cleanupTimeout        time.Duration
	maxTransactionRetries int
}

func NewPostgresStore(database *sql.DB, config PostgresStoreConfig) (*PostgresStore, error) {
	if database == nil {
		return nil, invalid("postgresStore.database", "dedicated Handoff operator database is required")
	}
	if config.SessionAffinityMode != PostgresSessionAffinityDirect &&
		config.SessionAffinityMode != PostgresSessionAffinitySessionPool {
		return nil, invalid(
			"postgresStore.sessionAffinityMode",
			"verified direct or session-pool affinity is required; got %q",
			config.SessionAffinityMode,
		)
	}
	if config.MaxTransactionRetries < 0 || config.MaxTransactionRetries > maximumPostgresTransactionRetries {
		return nil, invalid(
			"postgresStore.maxTransactionRetries", "must be between 0 and %d",
			maximumPostgresTransactionRetries,
		)
	}
	retries := config.MaxTransactionRetries
	if retries == 0 {
		retries = defaultPostgresTransactionRetries
	}
	return &PostgresStore{
		database:              database,
		commit:                func(transaction *sql.Tx) error { return transaction.Commit() },
		cleanupTimeout:        postgresSessionCleanupTimeout,
		maxTransactionRetries: retries,
	}, nil
}

// Readiness proves that this pool reaches a read-write primary through the
// exact Handoff operator capability. The probe UUID is lookup-only: whether a
// row happens to exist is irrelevant, while permission/caller/posture errors
// remain fatal.
func (store *PostgresStore) Readiness(ctx context.Context) error {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return invalid("postgresStore", "store, database, and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	probe, err := uuid.Parse(postgresReadinessProbeHandoffID)
	if err != nil {
		return fmt.Errorf("%w: readiness probe identity is invalid", ErrConflict)
	}
	var primaryReadWrite bool
	var encoded []byte
	if err := store.database.QueryRowContext(ctx, postgresInspectQuery, probe).Scan(
		&primaryReadWrite,
		&encoded,
	); err != nil {
		return fmt.Errorf("qualification handoff PostgreSQL capability is not ready: %w", err)
	}
	if !primaryReadWrite {
		return ErrNotReady
	}
	return nil
}

func (store *PostgresStore) Complete(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return Record{}, invalid("postgresStore", "store, database, and context are required")
	}
	if err := ValidateHandoffID(handoffID); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	maxRetries := store.maxTransactionRetries
	if maxRetries < 0 || maxRetries > maximumPostgresTransactionRetries {
		return Record{}, invalid("postgresStore.maxTransactionRetries", "store has invalid retry configuration")
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		record, err := store.completeAttempt(ctx, handoffID)
		if !errors.Is(err, errPostgresRetryableAttempt) {
			return record, err
		}
		if attempt == maxRetries || ctx.Err() != nil {
			return Record{}, ErrRetryable
		}
	}
	return Record{}, ErrRetryable
}

var errPostgresRetryableAttempt = errors.New("qualification handoff PostgreSQL attempt was definitely aborted and is retryable")

func (store *PostgresStore) completeAttempt(
	ctx context.Context,
	handoffID uuid.UUID,
) (_ Record, returnedErr error) {
	connection, err := store.database.Conn(ctx)
	if err != nil {
		return Record{}, ErrOutcomeUnknown
	}
	connectionReusable := true
	defer func() {
		if cleanupErr := releaseHandoffPostgresConnection(
			connection, !connectionReusable, store.cleanupDuration(),
		); cleanupErr != nil {
			returnedErr = ErrStoreOutcomeUnknown
		}
	}()

	if err := acquireHandoffSessionLock(ctx, connection, handoffID); err != nil {
		connectionReusable = false
		return Record{}, ErrOutcomeUnknown
	}
	lockHeld := true
	defer func() {
		if !lockHeld {
			return
		}
		if cleanupErr := store.releaseSessionLock(ctx, connection, handoffID); cleanupErr != nil {
			// Unknown cleanup dominates any otherwise-definite abort. The caller
			// must reconcile the same ID before attempting more work.
			connectionReusable = false
			lockHeld = false
			returnedErr = ErrStoreOutcomeUnknown
		}
	}()

	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		// A failed BEGIN acknowledgement does not prove whether this backend
		// entered a transaction. Never unlock and pool a session that may retain
		// either an open transaction or the session fence.
		connectionReusable = false
		lockHeld = false
		return Record{}, ErrOutcomeUnknown
	}
	transactionFinished := false
	defer func() {
		if transactionFinished {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
		defer cancel()
		rollbackErr := rollbackHandoffTransaction(rollbackCtx, transaction)
		transactionFinished = true
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			// Unknown rollback state must not return a possibly open backend to
			// the pool. The immutable result remains conservatively unknown.
			connectionReusable = false
			lockHeld = false
			returnedErr = ErrStoreOutcomeUnknown
		}
	}()

	var primaryReadWrite bool
	if err := transaction.QueryRowContext(ctx, postgresPrimaryReadWriteQuery).Scan(&primaryReadWrite); err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, ErrOutcomeUnknown
	}
	if !primaryReadWrite {
		return Record{}, ErrNotReady
	}

	var encoded []byte
	err = transaction.QueryRowContext(ctx, postgresCompleteQuery, handoffID).Scan(&encoded)
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, classifyPostgresCompleteError(err)
	}
	record, err := DecodeCompleteBundle(encoded)
	if err != nil || record.HandoffID != handoffID {
		return Record{}, fmt.Errorf("%w: PostgreSQL Complete returned a corrupt or different immutable bundle", ErrConflict)
	}

	commit := store.commit
	if commit == nil {
		commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	}
	if err := commit(transaction); err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		// Commit acknowledgement loss is both an immutable-outcome and a
		// physical-session ambiguity. Reconciliation uses the same Handoff ID
		// on a fresh primary connection; this backend is never unlocked/poolable.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
		_ = rollbackHandoffTransaction(cleanupCtx, transaction)
		cancel()
		transactionFinished = true
		connectionReusable = false
		lockHeld = false
		return Record{}, ErrStoreOutcomeUnknown
	}
	transactionFinished = true

	if err := store.releaseSessionLock(ctx, connection, handoffID); err != nil {
		connectionReusable = false
		lockHeld = false
		return Record{}, ErrStoreOutcomeUnknown
	}
	lockHeld = false
	return cloneRecord(record), nil
}

func (store *PostgresStore) Inspect(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || !validUUIDv4Value(handoffID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	var primaryReadWrite bool
	var encoded []byte
	if err := store.database.QueryRowContext(ctx, postgresInspectQuery, handoffID).Scan(
		&primaryReadWrite, &encoded,
	); err != nil {
		return Record{}, classifyPostgresInspectError(err)
	}
	if !primaryReadWrite {
		return Record{}, ErrOutcomeUnknown
	}
	if len(encoded) == 0 {
		return Record{}, ErrNotFound
	}
	record, err := DecodeInspectBundle(encoded)
	if err != nil || record.HandoffID != handoffID {
		return Record{}, fmt.Errorf("%w: PostgreSQL Inspect returned a corrupt or different immutable bundle", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (store *PostgresStore) releaseSessionLock(
	parent context.Context,
	connection *sql.Conn,
	handoffID uuid.UUID,
) error {
	timeout := store.cleanupDuration()
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	var unlocked bool
	err := connection.QueryRowContext(cleanup, postgresReleaseSessionLockQuery, handoffID).Scan(&unlocked)
	if err == nil && unlocked {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("Handoff session advisory unlock returned false")
}

func (store *PostgresStore) cleanupDuration() time.Duration {
	if store == nil || store.cleanupTimeout <= 0 || store.cleanupTimeout > postgresSessionCleanupTimeout {
		return postgresSessionCleanupTimeout
	}
	return store.cleanupTimeout
}

func rollbackHandoffTransaction(ctx context.Context, transaction *sql.Tx) error {
	if transaction == nil || isNilInterface(ctx) {
		return errors.New("qualification handoff PostgreSQL transaction and cleanup context are required")
	}
	result := make(chan error, 1)
	go func() {
		result <- transaction.Rollback()
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("qualification handoff PostgreSQL rollback cleanup deadline: %w", ctx.Err()))
	}
}

func releaseHandoffPostgresConnection(connection *sql.Conn, discard bool, timeout time.Duration) error {
	if connection == nil {
		return nil
	}
	if timeout <= 0 || timeout > postgresSessionCleanupTimeout {
		timeout = postgresSessionCleanupTimeout
	}
	result := make(chan error, 1)
	go func() {
		var poisonErr error
		if discard {
			poisonErr = poisonPostgresConnection(connection)
		}
		closeErr := connection.Close()
		if errors.Is(closeErr, sql.ErrConnDone) {
			closeErr = nil
		}
		result <- errors.Join(poisonErr, closeErr)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		return errors.Join(ErrStoreOutcomeUnknown, errors.New("qualification handoff PostgreSQL connection cleanup deadline exceeded"))
	}
}

func acquireHandoffSessionLock(ctx context.Context, connection *sql.Conn, handoffID uuid.UUID) error {
	_, err := connection.ExecContext(ctx, postgresAcquireSessionLockQuery, handoffID)
	return err
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

func classifyPostgresCompleteError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return ErrOutcomeUnknown
	}
	switch postgresError.Code {
	case "WPH01":
		return ErrInvalid
	case "WPH02", "23502", "23503", "23505", "23514", "23P01":
		return ErrConflict
	case "WPH03":
		return ErrNotReady
	default:
		return ErrOutcomeUnknown
	}
}

func classifyPostgresInspectError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "WPH01":
			return ErrInvalid
		case "WPH02", "23502", "23503", "23505", "23514", "23P01":
			return ErrConflict
		}
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
	for current := err; current != nil; {
		if postgresError, ok := current.(*pgconn.PgError); ok {
			return postgresError.Code == "40001" || postgresError.Code == "40P01"
		}
		if _, ok := current.(interface{ Unwrap() []error }); ok {
			return false
		}
		wrapped, ok := current.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		current = wrapped.Unwrap()
	}
	return false
}

const postgresAcquireSessionLockQuery = `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text, 0
  )
)`

const postgresReleaseSessionLockQuery = `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text, 0
  )
)`

const postgresPrimaryReadWriteQuery = `
SELECT
  NOT pg_catalog.pg_is_in_recovery()
  AND pg_catalog.current_setting('transaction_read_only') = 'off'`

const postgresCompleteQuery = `
SELECT value
FROM complete_qualification_promotion_v2_handoff($1) AS value`

const postgresInspectQuery = `
SELECT
  posture.primary_read_write,
  CASE WHEN posture.primary_read_write THEN (
    SELECT value
    FROM inspect_qualification_promotion_v2_handoff_completion($1) AS value
    LIMIT 1
  ) END
FROM (
  SELECT
    NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
) AS posture`

var _ AtomicStore = (*PostgresStore)(nil)

var _ interface {
	Readiness(context.Context) error
} = (*PostgresStore)(nil)
