package qualificationpromotionv2

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
)

// PostgresSessionAffinityMode records the independently verified pooling
// posture of the Promotion operator DSN. Session advisory locks make this a
// security property rather than a tuning preference.
type PostgresSessionAffinityMode string

const (
	PostgresSessionAffinityUnverified      PostgresSessionAffinityMode = "unverified"
	PostgresSessionAffinityDirect          PostgresSessionAffinityMode = "direct"
	PostgresSessionAffinitySessionPool     PostgresSessionAffinityMode = "session-pool"
	PostgresSessionAffinityTransactionPool PostgresSessionAffinityMode = "transaction-pool"
)

// PostgresStoreConfig is explicit so a caller cannot silently run the
// pre-BEGIN session-lock protocol through a transaction-pooling proxy.
// MaxTransactionRetries is the number of additional complete attempts after
// an unequivocal PostgreSQL serialization/deadlock abort. Zero selects the
// bounded production default.
type PostgresStoreConfig struct {
	SessionAffinityMode   PostgresSessionAffinityMode
	MaxTransactionRetries int
}

// PostgresStore invokes only the capability routines granted to the isolated
// Qualification Promotion operator. The supplied database must be backed by a
// direct PostgreSQL connection or a session-pooling proxy. Transaction-pooling
// proxies are not compatible with the pre-transaction session lock required
// for exact SERIALIZABLE replay convergence. Every consume attempt also
// verifies read-write-primary state inside its transaction, and inspection
// performs that check and lookup in one statement so a replica cannot turn
// recovery lag into a false not-found result.
type PostgresStore struct {
	database              *sql.DB
	commit                func(*sql.Tx) error
	cleanupTimeout        time.Duration
	maxTransactionRetries int
}

func NewPostgresStore(database *sql.DB, config PostgresStoreConfig) (*PostgresStore, error) {
	if database == nil {
		return nil, invalid("postgresStore.database", "Promotion operator database is required")
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
			"postgresStore.maxTransactionRetries",
			"must be between 0 and %d",
			maximumPostgresTransactionRetries,
		)
	}
	maxTransactionRetries := config.MaxTransactionRetries
	if maxTransactionRetries == 0 {
		maxTransactionRetries = defaultPostgresTransactionRetries
	}
	return &PostgresStore{
		database: database,
		commit: func(transaction *sql.Tx) error {
			return transaction.Commit()
		},
		cleanupTimeout:        postgresSessionCleanupTimeout,
		maxTransactionRetries: maxTransactionRetries,
	}, nil
}

func (store *PostgresStore) Consume(ctx context.Context, command ConsumeCommand) (Record, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return Record{}, invalid("postgresStore", "store, database, and context are required")
	}
	if err := ValidateCommand(command); err != nil {
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
		record, err := store.consumeAttempt(ctx, command)
		if !errors.Is(err, errPostgresRetryableAttempt) {
			return record, err
		}
		if attempt == maxRetries || ctx.Err() != nil {
			return Record{}, ErrRetryable
		}
	}
	return Record{}, ErrRetryable
}

var errPostgresRetryableAttempt = errors.New("qualification promotion v2 PostgreSQL attempt was definitely aborted and is retryable")

func (store *PostgresStore) consumeAttempt(
	ctx context.Context,
	command ConsumeCommand,
) (_ Record, returnedErr error) {

	connection, err := store.database.Conn(ctx)
	if err != nil {
		return Record{}, ErrOutcomeUnknown
	}
	defer connection.Close()

	if err := acquirePromotionSessionLock(ctx, connection, command.OperationID); err != nil {
		// A cancelled or failed lock query can have acquired the session lock on
		// the backend before its result became invisible. Never return that
		// physical connection to the pool.
		_ = poisonPostgresConnection(connection)
		return Record{}, ErrOutcomeUnknown
	}
	lockHeld := true
	defer func() {
		if !lockHeld {
			return
		}
		if cleanupErr := store.releaseSessionLock(ctx, connection, command.OperationID); cleanupErr != nil {
			returnedErr = ErrOutcomeUnknown
		}
	}()

	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		// A failed BEGIN acknowledgement does not prove whether PostgreSQL
		// entered a transaction.  Do not unlock or return a session that may
		// retain either the transaction or the Promotion advisory fence.
		_ = poisonPostgresConnection(connection)
		lockHeld = false
		return Record{}, ErrOutcomeUnknown
	}
	transactionFinished := false
	defer func() {
		if transactionFinished {
			return
		}
		rollbackErr := transaction.Rollback()
		transactionFinished = true
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			// An unknown rollback result makes the physical session unsafe for
			// pooling.  The immutable operation must be inspected before reuse.
			_ = poisonPostgresConnection(connection)
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
	err = transaction.QueryRowContext(ctx, postgresConsumeQuery,
		command.OperationID,
		command.WorkflowInputAuthorityID,
		command.PlanAuthorityID,
		command.HandoffID,
		command.OutputRevisionID,
	).Scan(&encoded)
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return Record{}, errPostgresRetryableAttempt
		}
		return Record{}, classifyPostgresConsumeError(err)
	}
	record, err := DecodeConsumeStoreBundle(encoded)
	if err != nil || record.Command != command {
		return Record{}, fmt.Errorf("%w: PostgreSQL consume returned a corrupt or different immutable bundle", ErrConflict)
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
		// physical-session ambiguity. Recovery uses this exact operation ID on
		// a fresh primary connection; this backend is never unlocked or pooled.
		_ = transaction.Rollback()
		transactionFinished = true
		_ = poisonPostgresConnection(connection)
		lockHeld = false
		return Record{}, ErrStoreOutcomeUnknown
	}
	transactionFinished = true

	if err := store.releaseSessionLock(ctx, connection, command.OperationID); err != nil {
		lockHeld = false // releaseSessionLock already poisoned the connection.
		return Record{}, ErrStoreOutcomeUnknown
	}
	lockHeld = false
	return cloneRecord(record), nil
}

func (store *PostgresStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || !validUUIDv4Value(operationID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	var primaryReadWrite bool
	var encoded []byte
	if err := store.database.QueryRowContext(ctx, postgresInspectOperationQuery, operationID).Scan(
		&primaryReadWrite,
		&encoded,
	); err != nil {
		return Record{}, classifyPostgresInspectError(err)
	}
	if !primaryReadWrite {
		return Record{}, ErrOutcomeUnknown
	}
	if len(encoded) == 0 {
		return Record{}, ErrNotFound
	}
	record, err := DecodeStoreBundle(encoded)
	if err != nil || record.Command.OperationID != operationID {
		return Record{}, fmt.Errorf("%w: PostgreSQL inspection returned a corrupt or different immutable bundle", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (store *PostgresStore) releaseSessionLock(
	parent context.Context,
	connection *sql.Conn,
	operationID uuid.UUID,
) error {
	timeout := store.cleanupTimeout
	if timeout <= 0 {
		timeout = postgresSessionCleanupTimeout
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	var unlocked bool
	err := connection.QueryRowContext(cleanup, postgresReleaseSessionLockQuery, operationID).Scan(&unlocked)
	if err == nil && unlocked {
		return nil
	}
	poisonErr := poisonPostgresConnection(connection)
	if err != nil {
		return errors.Join(err, poisonErr)
	}
	return errors.Join(errors.New("Promotion session advisory unlock returned false"), poisonErr)
}

func acquirePromotionSessionLock(ctx context.Context, connection *sql.Conn, operationID uuid.UUID) error {
	_, err := connection.ExecContext(ctx, postgresAcquireSessionLockQuery, operationID)
	return err
}

// poisonPostgresConnection prevents database/sql from pooling a physical
// backend whose session-lock state is unknown. driver.ErrBadConn is the
// database/sql contract for discarding rather than reusing that backend.
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

func classifyPostgresConsumeError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return ErrOutcomeUnknown
	}
	switch postgresError.Code {
	case "WPV01":
		return ErrInvalid
	case "WPV02", "23502", "23503", "23505", "23514", "23P01":
		return ErrConflict
	case "WPV03":
		return ErrNotReady
	case "WPV04":
		return ErrStale
	default:
		return ErrOutcomeUnknown
	}
}

func isDefinitePostgresRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return false
	}
	// Only a linear error chain ending in a concrete PgError is unequivocal.
	// In particular, errors.Join(PgError, transportError) must not turn an
	// ambiguous commit result into an automatic retry.
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

func classifyPostgresInspectError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "WPV02", "23502", "23503", "23505", "23514", "23P01":
			return ErrConflict
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrOutcomeUnknown
}

const postgresAcquireSessionLockQuery = `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text, 0
  )
)`

const postgresReleaseSessionLockQuery = `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text, 0
  )
)`

const postgresConsumeQuery = `
SELECT value
FROM consume_qualification_promotion_v2($1, $2, $3, $4, $5) AS value`

const postgresPrimaryReadWriteQuery = `
SELECT
  NOT pg_catalog.pg_is_in_recovery()
  AND pg_catalog.current_setting('transaction_read_only') = 'off'`

const postgresInspectOperationQuery = `
SELECT
  posture.primary_read_write,
  CASE WHEN posture.primary_read_write THEN (
    SELECT value
    FROM inspect_qualification_promotion_v2_operation($1) AS value
    LIMIT 1
  ) END
FROM (
  SELECT
    NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
) AS posture`

var _ AtomicStore = (*PostgresStore)(nil)
