package qualificationrelease

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
	defaultTransactionRetries = 3
	maximumTransactionRetries = 10
	postgresCleanupTimeout    = 5 * time.Second
)

const (
	resolveForPublishQuery = `SELECT value FROM resolve_qualification_release_for_publish_v1($1,$2) AS value`
	claimPublishQuery      = `SELECT value FROM claim_qualification_release_publish_v1($1,$2,$3,$4,$5,$6) AS value`
	inspectClaimQuery      = `SELECT value FROM inspect_qualification_release_publish_claim_v1($1) AS value`
	renewLeaseQuery        = `
SELECT value
FROM renew_qualification_release_publish_lease_v1(
  $1,$2,$3,$4,$5,$6,$7,
  pg_catalog.date_trunc(
    'milliseconds',
    pg_catalog.clock_timestamp() + ($8 * interval '1 millisecond')
  )
) AS value`
	startControllerQuery   = `SELECT value FROM start_qualification_release_controller_v1($1,$2,$3,$4) AS value`
	inspectControllerQuery = `SELECT value FROM inspect_qualification_release_controller_v1($1) AS value`
	recordHealthyQuery     = `SELECT value FROM record_qualification_release_result_v1($1) AS value`
	inspectHealthyQuery    = `SELECT value FROM inspect_qualification_release_result_v1($1) AS value`
	recordFailureQuery     = `SELECT value FROM record_qualification_release_failure_v1($1) AS value`
	inspectFailureQuery    = `SELECT value FROM inspect_qualification_release_failure_v1($1) AS value`
	applyHealthyQuery      = `SELECT value FROM apply_qualification_release_result_v1($1,$2,$3,$4,$5) AS value`
	applyFailureQuery      = `SELECT value FROM apply_qualification_release_failure_v1($1,$2,$3,$4,$5) AS value`
	inspectBootstrapQuery  = `SELECT value FROM inspect_qualification_release_controller_bootstrap_v1() AS value`
)

type PostgresStoreConfig struct {
	MaxTransactionRetries int
}

// PostgresStore issues only migration-84 operator capabilities. It never reads
// qualification tables directly and has no application-credential fallback.
type PostgresStore struct {
	database              *sql.DB
	commit                func(*sql.Tx) error
	cleanupTimeout        time.Duration
	maxTransactionRetries int
}

func NewPostgresStore(database *sql.DB, config PostgresStoreConfig) (*PostgresStore, error) {
	if database == nil || config.MaxTransactionRetries < 0 ||
		config.MaxTransactionRetries > maximumTransactionRetries {
		return nil, ErrInvalid
	}
	retries := config.MaxTransactionRetries
	if retries == 0 {
		retries = defaultTransactionRetries
	}
	return &PostgresStore{
		database: database, cleanupTimeout: postgresCleanupTimeout,
		maxTransactionRetries: retries,
		commit:                func(transaction *sql.Tx) error { return transaction.Commit() },
	}, nil
}

func (store *PostgresStore) Resolve(ctx context.Context, target Target) (Authorization, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || target.Validate() != nil {
		return Authorization{}, ErrInvalid
	}
	encoded, err := store.query(ctx, resolveForPublishQuery, target.WorkflowRunID, target.PublishNodeRunID)
	if err != nil {
		return Authorization{}, err
	}
	return decodeAuthorizationBundle(encoded, target)
}

func (store *PostgresStore) Claim(
	ctx context.Context,
	authorization Authorization,
	claimEventID uuid.UUID,
	owner string,
	leaseDuration time.Duration,
) (Claim, error) {
	target := Target{WorkflowRunID: authorization.WorkflowRunID, PublishNodeRunID: authorization.PublishNodeRunID}
	if store == nil || authorization.ValidateFor(target) != nil || !validUUIDv4(claimEventID) ||
		!boundedText(owner, 200) || leaseDuration < time.Second || leaseDuration > 5*time.Minute ||
		leaseDuration%time.Millisecond != 0 {
		return Claim{}, ErrInvalid
	}
	encoded, err := store.mutate(ctx, claimPublishQuery,
		authorization.AuthorizationID, authorization.WorkflowRunID, authorization.PublishNodeRunID,
		claimEventID, owner, leaseDuration.Milliseconds(),
	)
	if err != nil {
		return Claim{}, err
	}
	return decodeClaimBundle(encoded, authorization, claimEventID)
}

func (store *PostgresStore) InspectClaim(
	ctx context.Context,
	authorization Authorization,
	claimEventID uuid.UUID,
) (Claim, error) {
	if store == nil || !validUUIDv4(claimEventID) {
		return Claim{}, ErrNotFound
	}
	encoded, err := store.query(ctx, inspectClaimQuery, claimEventID)
	if err != nil {
		return Claim{}, err
	}
	return decodeClaimBundle(encoded, authorization, claimEventID)
}

func (store *PostgresStore) Renew(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
	expectedExpiry time.Time,
	extension time.Duration,
) (Claim, error) {
	if store == nil || claim.ValidateFor(authorization) != nil || !claim.Active ||
		expectedExpiry.IsZero() || expectedExpiry.Nanosecond()%int(time.Millisecond) != 0 ||
		extension < time.Second || extension > 4*time.Minute || extension%time.Millisecond != 0 {
		return Claim{}, ErrInvalid
	}
	encoded, err := store.mutate(ctx, renewLeaseQuery,
		authorization.AuthorizationID, authorization.WorkflowRunID, authorization.PublishNodeRunID,
		claim.ClaimEventID, claim.Owner, claim.Attempt, expectedExpiry, extension.Milliseconds(),
	)
	if err != nil {
		return Claim{}, err
	}
	return decodeClaimBundle(encoded, authorization, claim.ClaimEventID)
}

func (store *PostgresStore) Start(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
) (ControllerBinding, error) {
	if store == nil || claim.ValidateFor(authorization) != nil || !claim.Active {
		return ControllerBinding{}, ErrInvalid
	}
	encoded, err := store.mutate(ctx, startControllerQuery,
		authorization.AuthorizationID, claim.ClaimEventID, claim.Owner, claim.Attempt,
	)
	if err != nil {
		return ControllerBinding{}, err
	}
	return decodeControllerBundle(encoded, authorization)
}

func (store *PostgresStore) InspectController(
	ctx context.Context,
	authorization Authorization,
) (ControllerBinding, error) {
	if store == nil || !validUUIDv4(authorization.AuthorizationID) {
		return ControllerBinding{}, ErrNotFound
	}
	encoded, err := store.query(ctx, inspectControllerQuery, authorization.AuthorizationID)
	if err != nil {
		return ControllerBinding{}, err
	}
	return decodeControllerBundle(encoded, authorization)
}

func (store *PostgresStore) RecordHealthy(ctx context.Context, authorization Authorization) (TerminalRecord, error) {
	return store.recordTerminal(ctx, authorization, true, false)
}

func (store *PostgresStore) InspectHealthy(ctx context.Context, authorization Authorization) (TerminalRecord, error) {
	return store.recordTerminal(ctx, authorization, true, true)
}

func (store *PostgresStore) RecordFailure(ctx context.Context, authorization Authorization) (TerminalRecord, error) {
	return store.recordTerminal(ctx, authorization, false, false)
}

func (store *PostgresStore) InspectFailure(ctx context.Context, authorization Authorization) (TerminalRecord, error) {
	return store.recordTerminal(ctx, authorization, false, true)
}

func (store *PostgresStore) recordTerminal(
	ctx context.Context,
	authorization Authorization,
	healthy, inspect bool,
) (TerminalRecord, error) {
	if store == nil || !validUUIDv4(authorization.AuthorizationID) {
		return TerminalRecord{}, ErrInvalid
	}
	query := recordFailureQuery
	if healthy {
		query = recordHealthyQuery
	}
	var encoded []byte
	var err error
	if inspect {
		if healthy {
			query = inspectHealthyQuery
		} else {
			query = inspectFailureQuery
		}
		encoded, err = store.query(ctx, query, authorization.AuthorizationID)
	} else {
		encoded, err = store.mutate(ctx, query, authorization.AuthorizationID)
	}
	if err != nil {
		return TerminalRecord{}, err
	}
	return decodeTerminalBundle(encoded, authorization, healthy)
}

func (store *PostgresStore) ApplyHealthy(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
) (TerminalRecord, error) {
	return store.apply(ctx, authorization, claim, true)
}

func (store *PostgresStore) ApplyFailure(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
) (TerminalRecord, error) {
	return store.apply(ctx, authorization, claim, false)
}

func (store *PostgresStore) apply(
	ctx context.Context,
	authorization Authorization,
	claim Claim,
	healthy bool,
) (TerminalRecord, error) {
	if store == nil || claim.ValidateFor(authorization) != nil {
		return TerminalRecord{}, ErrInvalid
	}
	query := applyFailureQuery
	if healthy {
		query = applyHealthyQuery
	}
	encoded, err := store.mutate(ctx, query, authorization.AuthorizationID,
		authorization.WorkflowRunID, authorization.PublishNodeRunID, claim.Owner, claim.Attempt)
	if err != nil {
		return TerminalRecord{}, err
	}
	return decodeTerminalBundle(encoded, authorization, healthy)
}

// Readiness proves the operator caller posture, read-write primary, exact
// immutable Controller bootstrap, and credential separation from the
// application pool. The bootstrap routine is inspect-only.
func (store *PostgresStore) Readiness(
	ctx context.Context,
	expected ControllerIdentity,
	application *sql.DB,
) error {
	if store == nil || store.database == nil || application == nil ||
		isNilInterface(ctx) || expected.Validate() != nil {
		return ErrInvalid
	}
	operatorUser, operatorPrimary, err := inspectPostgresSession(ctx, store.database)
	if err != nil || !operatorPrimary {
		return wrap(ErrNotReady, "qualification release operator is not a read-write primary")
	}
	applicationUser, applicationPrimary, err := inspectPostgresSession(ctx, application)
	if err != nil || !applicationPrimary || applicationUser == operatorUser {
		return wrap(ErrNotReady, "qualification release operator and application credentials are not distinct")
	}
	encoded, err := store.query(ctx, inspectBootstrapQuery)
	if err != nil {
		return wrap(ErrNotReady, "qualification release Controller bootstrap is unavailable")
	}
	actual, err := decodeBootstrapBundle(encoded)
	if err != nil || actual != expected {
		return wrap(ErrConflict, "qualification release Controller bootstrap identity drifted")
	}
	return nil
}

func inspectPostgresSession(ctx context.Context, database *sql.DB) (string, bool, error) {
	var sessionUser, currentUser, roleSetting string
	var primary bool
	err := database.QueryRowContext(ctx, `
SELECT session_user::text,current_user::text,
       pg_catalog.current_setting('role'),
       NOT pg_catalog.pg_is_in_recovery()
         AND pg_catalog.current_setting('transaction_read_only')='off'
`).Scan(&sessionUser, &currentUser, &roleSetting, &primary)
	if err != nil {
		return "", false, err
	}
	if sessionUser == "" || currentUser != sessionUser || roleSetting != "none" {
		return "", false, ErrConflict
	}
	return sessionUser, primary, nil
}

func (store *PostgresStore) query(ctx context.Context, query string, arguments ...any) ([]byte, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return nil, ErrInvalid
	}
	var encoded []byte
	if err := store.database.QueryRowContext(ctx, query, arguments...).Scan(&encoded); err != nil {
		return nil, classifyPostgresError(err)
	}
	if len(encoded) == 0 || len(encoded) > maximumCapabilityBundleBytes {
		return nil, ErrConflict
	}
	return append([]byte(nil), encoded...), nil
}

func (store *PostgresStore) mutate(ctx context.Context, query string, arguments ...any) ([]byte, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return nil, ErrInvalid
	}
	for attempt := 0; attempt <= store.maxTransactionRetries; attempt++ {
		encoded, err := store.mutateAttempt(ctx, query, arguments...)
		if !errors.Is(err, errDefinitelyAborted) {
			return encoded, err
		}
		if attempt == store.maxTransactionRetries || ctx.Err() != nil {
			return nil, ErrRetryable
		}
	}
	return nil, ErrRetryable
}

var errDefinitelyAborted = errors.New("qualification release PostgreSQL transaction definitely aborted")

func (store *PostgresStore) mutateAttempt(
	ctx context.Context,
	query string,
	arguments ...any,
) (_ []byte, returnedErr error) {
	connection, err := store.database.Conn(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrStoreOutcomeUnknown
	}
	reusable := true
	defer func() {
		if cleanupErr := releasePostgresConnection(connection, !reusable, store.cleanupDuration()); cleanupErr != nil {
			returnedErr = ErrStoreOutcomeUnknown
		}
	}()

	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isDefiniteAbort(err) {
			return nil, errDefinitelyAborted
		}
		reusable = false
		return nil, ErrStoreOutcomeUnknown
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
		defer cancel()
		if rollbackErr := rollbackPostgresTransaction(cleanup, transaction); rollbackErr != nil &&
			!errors.Is(rollbackErr, sql.ErrTxDone) {
			reusable = false
			returnedErr = ErrStoreOutcomeUnknown
		}
		finished = true
	}()

	var encoded []byte
	if err := transaction.QueryRowContext(ctx, query, arguments...).Scan(&encoded); err != nil {
		if isDefiniteAbort(err) {
			return nil, errDefinitelyAborted
		}
		classified := classifyPostgresError(err)
		if errors.Is(classified, ErrOutcomeUnknown) {
			reusable = false
			return nil, ErrStoreOutcomeUnknown
		}
		return nil, classified
	}
	if len(encoded) == 0 || len(encoded) > maximumCapabilityBundleBytes {
		return nil, ErrConflict
	}
	commit := store.commit
	if commit == nil {
		commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	}
	if err := commit(transaction); err != nil {
		finished = true
		if isDefiniteAbort(err) {
			return nil, errDefinitelyAborted
		}
		reusable = false
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.cleanupDuration())
		_ = rollbackPostgresTransaction(cleanup, transaction)
		cancel()
		return nil, ErrStoreOutcomeUnknown
	}
	finished = true
	return append([]byte(nil), encoded...), nil
}

func (store *PostgresStore) cleanupDuration() time.Duration {
	if store == nil || store.cleanupTimeout <= 0 || store.cleanupTimeout > postgresCleanupTimeout {
		return postgresCleanupTimeout
	}
	return store.cleanupTimeout
}

func classifyPostgresError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "WQR01", "22023":
			return ErrInvalid
		case "WQR02", "23502", "23503", "23505", "23514", "23P01", "42501":
			return ErrConflict
		case "WQR03":
			return ErrNotReady
		case "40001", "40P01":
			return ErrRetryable
		}
	}
	return ErrOutcomeUnknown
}

func isDefiniteAbort(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return false
	}
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) &&
		(postgresError.Code == "40001" || postgresError.Code == "40P01")
}

func rollbackPostgresTransaction(ctx context.Context, transaction *sql.Tx) error {
	if transaction == nil || isNilInterface(ctx) {
		return ErrStoreOutcomeUnknown
	}
	result := make(chan error, 1)
	go func() { result <- transaction.Rollback() }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ErrStoreOutcomeUnknown
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
		return fmt.Errorf("%w: PostgreSQL connection cleanup deadline", ErrStoreOutcomeUnknown)
	}
}

func poisonPostgresConnection(connection *sql.Conn) error {
	err := connection.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}
