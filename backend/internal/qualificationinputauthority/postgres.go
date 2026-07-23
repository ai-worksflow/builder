package qualificationinputauthority

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	postgresSessionCleanupTimeout              = 5 * time.Second
	defaultPostgresTransactionRetries          = 3
	maximumPostgresTransactionRetries          = 10
	postgresInputPrecommitSessionLockNamespace = "worksflow:qualification-input-precommit:v1:operation:"
	postgresSourceAdmissionLockNamespace       = "worksflow:qualification-input-precommit:v1:source-request:"
	postgresCredentialAdmissionLockNamespace   = "worksflow:qualification-input-precommit:v1:credential-request:"
)

// PostgresSessionAffinityMode is an asserted deployment property. Session
// advisory locks are acquired before BEGIN, so transaction-pooling proxies
// cannot implement this protocol safely.
type PostgresSessionAffinityMode string

const (
	PostgresSessionAffinityUnverified      PostgresSessionAffinityMode = "unverified"
	PostgresSessionAffinityDirect          PostgresSessionAffinityMode = "direct"
	PostgresSessionAffinitySessionPool     PostgresSessionAffinityMode = "session-pool"
	PostgresSessionAffinityTransactionPool PostgresSessionAffinityMode = "transaction-pool"
)

// PostgresRoleDatabase binds one independently authenticated least-privilege
// LOGIN to its verified connection-affinity posture.
type PostgresRoleDatabase struct {
	Database            *sql.DB
	SessionAffinityMode PostgresSessionAffinityMode
}

// PostgresStoreConfig deliberately requires three distinct *sql.DB pools.
// Their DSNs must authenticate, respectively, as the input-precommit, sealed
// source-verifier, and sealed credential-resolver LOGINs. A shared retry bound
// applies only to complete attempts that PostgreSQL unequivocally aborted with
// 40001 or 40P01.
type PostgresStoreConfig struct {
	InputPrecommit        PostgresRoleDatabase
	SourceVerifier        PostgresRoleDatabase
	CredentialResolver    PostgresRoleDatabase
	MaxTransactionRetries int
}

// PostgresStore invokes only the capability routines granted to its three
// isolated roles. It never reads authority tables directly.
type PostgresStore struct {
	inputDatabase         *sql.DB
	sourceDatabase        *sql.DB
	credentialDatabase    *sql.DB
	commit                func(*sql.Tx) error
	cleanupTimeout        time.Duration
	maxTransactionRetries int
}

func NewPostgresStore(config PostgresStoreConfig) (*PostgresStore, error) {
	roles := []struct {
		name string
		role PostgresRoleDatabase
	}{
		{name: "inputPrecommit", role: config.InputPrecommit},
		{name: "sourceVerifier", role: config.SourceVerifier},
		{name: "credentialResolver", role: config.CredentialResolver},
	}
	for _, item := range roles {
		if item.role.Database == nil {
			return nil, invalid("postgresStore."+item.name+".database", "independent role database is required")
		}
		if item.role.SessionAffinityMode != PostgresSessionAffinityDirect &&
			item.role.SessionAffinityMode != PostgresSessionAffinitySessionPool {
			return nil, invalid(
				"postgresStore."+item.name+".sessionAffinityMode",
				"verified direct or session-pool affinity is required; got %q",
				item.role.SessionAffinityMode,
			)
		}
	}
	if config.InputPrecommit.Database == config.SourceVerifier.Database ||
		config.InputPrecommit.Database == config.CredentialResolver.Database ||
		config.SourceVerifier.Database == config.CredentialResolver.Database {
		return nil, invalid("postgresStore.databases", "three distinct least-privilege role pools are required")
	}
	if config.MaxTransactionRetries < 0 || config.MaxTransactionRetries > maximumPostgresTransactionRetries {
		return nil, invalid(
			"postgresStore.maxTransactionRetries",
			"must be between 0 and %d",
			maximumPostgresTransactionRetries,
		)
	}
	retries := config.MaxTransactionRetries
	if retries == 0 {
		retries = defaultPostgresTransactionRetries
	}
	return &PostgresStore{
		inputDatabase:         config.InputPrecommit.Database,
		sourceDatabase:        config.SourceVerifier.Database,
		credentialDatabase:    config.CredentialResolver.Database,
		commit:                func(transaction *sql.Tx) error { return transaction.Commit() },
		cleanupTimeout:        postgresSessionCleanupTimeout,
		maxTransactionRetries: retries,
	}, nil
}

func (store *PostgresStore) admitSourceReceipt(
	ctx context.Context,
	grant verifiedSourceGrant,
) (ReceiptAdmissionRecord, error) {
	candidate, err := compileReceiptAdmission(ReceiptKindSource, grant.proof, grant.requestBytes)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	return store.admitReceipt(ctx, ReceiptKindSource, candidate)
}

func (store *PostgresStore) admitCredentialReceipt(
	ctx context.Context,
	grant verifiedCredentialGrant,
) (ReceiptAdmissionRecord, error) {
	candidate, err := compileReceiptAdmission(ReceiptKindCredential, grant.proof, grant.requestBytes)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	return store.admitReceipt(ctx, ReceiptKindCredential, candidate)
}

func (store *PostgresStore) admitReceipt(
	ctx context.Context,
	kind string,
	candidate ReceiptAdmissionRecord,
) (ReceiptAdmissionRecord, error) {
	if store == nil || isNilInterface(ctx) {
		return ReceiptAdmissionRecord{}, invalid("postgresStore.receiptAdmission", "store and context are required")
	}
	if err := ctx.Err(); err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	if err := validateReceiptAdmissionRecord(candidate); err != nil || candidate.Document.Kind != kind {
		return ReceiptAdmissionRecord{}, invalid("postgresStore.receiptAdmission", "candidate is not an exact closed-kind admission")
	}
	database, lockNamespace, query, err := store.admissionRoute(kind)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	return postgresRetriedWrite(store, ctx, database, lockNamespace+candidate.Document.RequestHash,
		func(transaction *sql.Tx) (ReceiptAdmissionRecord, error) {
			if err := requirePostgresPrimaryReadWrite(ctx, transaction); err != nil {
				return ReceiptAdmissionRecord{}, err
			}
			row := postgresAdmissionRow{}
			err := transaction.QueryRowContext(
				ctx,
				query,
				candidate.Document.RequestHash,
				candidate.RequestBytes,
				string(candidate.RequestBytes),
				candidate.AdmissionHash,
				candidate.DocumentBytes,
				string(candidate.DocumentBytes),
			).Scan(row.destinations()...)
			if err != nil {
				return ReceiptAdmissionRecord{}, classifyPostgresWriteError(err)
			}
			stored, err := receiptAdmissionFromPostgresRow(kind, row)
			if err != nil {
				return ReceiptAdmissionRecord{}, err
			}
			if !sameReceiptAdmission(stored, candidate) {
				// The SQL first-commit-winner routine intentionally returns an
				// existing observation. Surface that as conflict so Service can
				// recover the exact winner by (kind, requestHash).
				return ReceiptAdmissionRecord{}, ErrConflict
			}
			return stored, nil
		},
	)
}

func (store *PostgresStore) resolveReceiptAdmission(
	ctx context.Context,
	kind string,
	admissionHash string,
) (ReceiptAdmissionRecord, error) {
	if store == nil || isNilInterface(ctx) || !validDigest(admissionHash) {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	database, _, _, err := store.admissionRoute(kind)
	if err != nil {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	query := postgresResolveSourceAdmissionQuery
	if kind == ReceiptKindCredential {
		query = postgresResolveCredentialAdmissionQuery
	}
	return inspectPostgresAdmission(ctx, database, query, kind, admissionHash)
}

func (store *PostgresStore) resolveReceiptAdmissionForRequest(
	ctx context.Context,
	kind string,
	requestHash string,
) (ReceiptAdmissionRecord, error) {
	if store == nil || isNilInterface(ctx) || !validDigest(requestHash) {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	database, _, _, err := store.admissionRoute(kind)
	if err != nil {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	query := postgresInspectSourceAdmissionQuery
	if kind == ReceiptKindCredential {
		query = postgresInspectCredentialAdmissionQuery
	}
	return inspectPostgresAdmission(ctx, database, query, kind, requestHash)
}

func (store *PostgresStore) admissionRoute(
	kind string,
) (*sql.DB, string, string, error) {
	switch kind {
	case ReceiptKindSource:
		if store.sourceDatabase == nil {
			return nil, "", "", invalid("postgresStore.sourceDatabase", "database is required")
		}
		return store.sourceDatabase, postgresSourceAdmissionLockNamespace, postgresAdmitSourceReceiptQuery, nil
	case ReceiptKindCredential:
		if store.credentialDatabase == nil {
			return nil, "", "", invalid("postgresStore.credentialDatabase", "database is required")
		}
		return store.credentialDatabase, postgresCredentialAdmissionLockNamespace, postgresAdmitCredentialReceiptQuery, nil
	default:
		return nil, "", "", invalid("postgresStore.receiptKind", "closed receipt kind is required")
	}
}

func (store *PostgresStore) Issue(ctx context.Context, candidate Record) (Record, error) {
	if store == nil || store.inputDatabase == nil || isNilInterface(ctx) {
		return Record{}, invalid("postgresStore", "store, input-precommit database, and context are required")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := ValidateRecord(candidate); err != nil {
		return Record{}, err
	}
	return postgresRetriedWrite(
		store,
		ctx,
		store.inputDatabase,
		postgresInputPrecommitSessionLockNamespace+candidate.Command.OperationID.String(),
		func(transaction *sql.Tx) (Record, error) {
			if err := requirePostgresPrimaryReadWrite(ctx, transaction); err != nil {
				return Record{}, err
			}
			priorRow := postgresAuthorityRow{}
			priorErr := transaction.QueryRowContext(
				ctx, postgresInspectOperationInTransactionQuery, candidate.Command.OperationID,
			).Scan(priorRow.destinations()...)
			preexisting := priorErr == nil
			if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
				return Record{}, classifyPostgresWriteError(priorErr)
			}
			if preexisting {
				if _, err := recordFromPostgresAuthorityRow(priorRow); err != nil {
					return Record{}, err
				}
			}

			row := postgresAuthorityRow{}
			err := transaction.QueryRowContext(
				ctx,
				postgresIssueQuery,
				candidate.Command.OperationID,
				candidate.Command.AuthorityID,
				candidate.Command.WorkflowInputAuthorityID,
				candidate.Command.QualificationPolicyAuthorityID,
				candidate.Command.QualificationPlanAuthorityID,
				candidate.RequestHash,
				candidate.RequestBytes,
				string(candidate.RequestBytes),
				candidate.SourceRequestHash,
				candidate.SourceRequestBytes,
				string(candidate.SourceRequestBytes),
				candidate.CredentialRequestHash,
				candidate.CredentialRequestBytes,
				string(candidate.CredentialRequestBytes),
				candidate.AuthorityHash,
				candidate.DocumentBytes,
				string(candidate.DocumentBytes),
			).Scan(row.destinations()...)
			if err != nil {
				return Record{}, classifyPostgresWriteError(err)
			}
			stored, err := recordFromPostgresAuthorityRow(row)
			if err != nil {
				return Record{}, err
			}
			if !sameImmutableRecord(stored, candidate) {
				return Record{}, fmt.Errorf("%w: PostgreSQL returned different immutable precommit bytes", ErrConflict)
			}
			stored.Idempotent = preexisting
			return stored, nil
		},
	)
}

func (store *PostgresStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store == nil || store.inputDatabase == nil || isNilInterface(ctx) || !validUUIDv4Value(operationID) {
		return Record{}, ErrNotFound
	}
	return inspectPostgresAuthority(ctx, store.inputDatabase, postgresInspectOperationQuery, operationID)
}

func (store *PostgresStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || store.inputDatabase == nil || isNilInterface(ctx) || !validUUIDv4Value(authorityID) {
		return Record{}, ErrNotFound
	}
	return inspectPostgresAuthority(ctx, store.inputDatabase, postgresResolveAuthorityQuery, authorityID)
}

func inspectPostgresAuthority(
	ctx context.Context,
	database *sql.DB,
	query string,
	identity uuid.UUID,
) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	var primaryReadWrite bool
	row := postgresAuthorityRow{}
	destinations := append([]any{&primaryReadWrite}, row.destinations()...)
	if err := database.QueryRowContext(ctx, query, identity).Scan(destinations...); err != nil {
		return Record{}, classifyPostgresInspectError(err)
	}
	if !primaryReadWrite {
		return Record{}, ErrOutcomeUnknown
	}
	if !row.authorityID.Valid {
		return Record{}, ErrNotFound
	}
	record, err := recordFromPostgresAuthorityRow(row)
	if err != nil {
		return Record{}, err
	}
	if (query == postgresInspectOperationQuery && record.Command.OperationID != identity) ||
		(query == postgresResolveAuthorityQuery && record.Command.AuthorityID != identity) {
		return Record{}, fmt.Errorf("%w: PostgreSQL inspection returned another authority", ErrConflict)
	}
	return cloneRecord(record), nil
}

func inspectPostgresAdmission(
	ctx context.Context,
	database *sql.DB,
	query string,
	kind string,
	hash string,
) (ReceiptAdmissionRecord, error) {
	var primaryReadWrite bool
	row := postgresAdmissionRow{}
	destinations := append([]any{&primaryReadWrite}, row.destinations()...)
	if err := database.QueryRowContext(ctx, query, hash).Scan(destinations...); err != nil {
		return ReceiptAdmissionRecord{}, classifyPostgresInspectError(err)
	}
	if !primaryReadWrite {
		return ReceiptAdmissionRecord{}, ErrOutcomeUnknown
	}
	if !row.requestHash.Valid {
		return ReceiptAdmissionRecord{}, ErrNotFound
	}
	record, err := receiptAdmissionFromPostgresRow(kind, row)
	if err != nil {
		return ReceiptAdmissionRecord{}, err
	}
	if (query == postgresInspectSourceAdmissionQuery || query == postgresInspectCredentialAdmissionQuery) &&
		record.Document.RequestHash != hash {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL inspection returned another request", ErrConflict)
	}
	if (query == postgresResolveSourceAdmissionQuery || query == postgresResolveCredentialAdmissionQuery) &&
		record.AdmissionHash != hash {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL recovery returned another admission", ErrConflict)
	}
	return cloneReceiptAdmission(record), nil
}

// PostgresClock reads the database authority time from the input-precommit
// role's read-write-primary connection. Callers may pass the same *sql.DB used
// in PostgresStoreConfig.InputPrecommit.
type PostgresClock struct {
	database *sql.DB
}

func NewPostgresClock(database *sql.DB) (*PostgresClock, error) {
	if database == nil {
		return nil, invalid("postgresClock.database", "input-precommit database is required")
	}
	return &PostgresClock{database: database}, nil
}

func (clock *PostgresClock) Now(ctx context.Context) (time.Time, error) {
	if clock == nil || clock.database == nil || isNilInterface(ctx) {
		return time.Time{}, invalid("postgresClock", "clock, database, and context are required")
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	var primaryReadWrite bool
	var value sql.NullTime
	if err := clock.database.QueryRowContext(ctx, postgresClockQuery).Scan(&primaryReadWrite, &value); err != nil {
		return time.Time{}, classifyPostgresInspectError(err)
	}
	if !primaryReadWrite || !value.Valid {
		return time.Time{}, ErrNotReady
	}
	normalized := value.Time.UTC()
	if !validDatabaseTime(normalized) {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL returned corrupt authority time", ErrConflict)
	}
	return normalized, nil
}

var errPostgresRetryableAttempt = errors.New("qualification input precommit PostgreSQL attempt was definitely aborted")

func postgresRetriedWrite[T any](
	store *PostgresStore,
	ctx context.Context,
	database *sql.DB,
	lockIdentity string,
	write func(*sql.Tx) (T, error),
) (T, error) {
	var zero T
	if store == nil || database == nil || write == nil || lockIdentity == "" {
		return zero, invalid("postgresStore.write", "complete write attempt configuration is required")
	}
	retries := store.maxTransactionRetries
	if retries < 0 || retries > maximumPostgresTransactionRetries {
		return zero, invalid("postgresStore.maxTransactionRetries", "store has invalid retry configuration")
	}
	for attempt := 0; attempt <= retries; attempt++ {
		value, err := postgresWriteAttempt(store, ctx, database, lockIdentity, write)
		if !errors.Is(err, errPostgresRetryableAttempt) {
			return value, err
		}
		if attempt == retries || ctx.Err() != nil {
			return zero, ErrRetryable
		}
	}
	return zero, ErrRetryable
}

func postgresWriteAttempt[T any](
	store *PostgresStore,
	ctx context.Context,
	database *sql.DB,
	lockIdentity string,
	write func(*sql.Tx) (T, error),
) (_ T, returnedErr error) {
	var zero T
	connection, err := database.Conn(ctx)
	if err != nil {
		return zero, ErrOutcomeUnknown
	}
	defer connection.Close()

	if err := acquirePostgresSessionLock(ctx, connection, lockIdentity); err != nil {
		_ = poisonPostgresConnection(connection)
		return zero, ErrOutcomeUnknown
	}
	lockHeld := true
	defer func() {
		if !lockHeld {
			return
		}
		if err := store.releasePostgresSessionLock(ctx, connection, lockIdentity); err != nil {
			returnedErr = ErrStoreOutcomeUnknown
		}
	}()

	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return zero, errPostgresRetryableAttempt
		}
		// A failed BEGIN acknowledgement does not prove whether the backend
		// entered a transaction.  Unlocking and pooling that session could
		// therefore retain either an open transaction or the session fence.
		// Discard the physical connection; backend termination releases both.
		_ = poisonPostgresConnection(connection)
		lockHeld = false
		return zero, ErrOutcomeUnknown
	}
	transactionFinished := false
	defer func() {
		if transactionFinished {
			return
		}
		rollbackErr := transaction.Rollback()
		transactionFinished = true
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			// A connection whose rollback result is unknown must never return to
			// the pool.  No COMMIT was requested, but callers still need the
			// conservative recovery class because the backend/session state was
			// not observed to close cleanly.
			_ = poisonPostgresConnection(connection)
			lockHeld = false
			returnedErr = ErrStoreOutcomeUnknown
		}
	}()

	value, err := write(transaction)
	if err != nil {
		if isDefinitePostgresRetryable(err) {
			return zero, errPostgresRetryableAttempt
		}
		return zero, err
	}
	commit := store.commit
	if commit == nil {
		commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	}
	if err := commit(transaction); err != nil {
		if isDefinitePostgresRetryable(err) {
			return zero, errPostgresRetryableAttempt
		}
		// Commit acknowledgement loss is both an immutable-outcome ambiguity
		// and a physical-session ambiguity.  Recovery is by deterministic
		// operation/admission identity on a fresh connection; this connection
		// is never unlocked and pooled for reuse.
		_ = transaction.Rollback()
		transactionFinished = true
		_ = poisonPostgresConnection(connection)
		lockHeld = false
		return zero, ErrStoreOutcomeUnknown
	}
	transactionFinished = true
	if err := store.releasePostgresSessionLock(ctx, connection, lockIdentity); err != nil {
		lockHeld = false
		return zero, ErrStoreOutcomeUnknown
	}
	lockHeld = false
	return value, nil
}

func requirePostgresPrimaryReadWrite(ctx context.Context, transaction *sql.Tx) error {
	var primaryReadWrite bool
	if err := transaction.QueryRowContext(ctx, postgresPrimaryReadWriteQuery).Scan(&primaryReadWrite); err != nil {
		return classifyPostgresWriteError(err)
	}
	if !primaryReadWrite {
		return ErrNotReady
	}
	return nil
}

func acquirePostgresSessionLock(ctx context.Context, connection *sql.Conn, identity string) error {
	_, err := connection.ExecContext(ctx, postgresAcquireSessionLockQuery, identity)
	return err
}

func (store *PostgresStore) releasePostgresSessionLock(
	parent context.Context,
	connection *sql.Conn,
	identity string,
) error {
	timeout := store.cleanupTimeout
	if timeout <= 0 {
		timeout = postgresSessionCleanupTimeout
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	var unlocked bool
	err := connection.QueryRowContext(cleanup, postgresReleaseSessionLockQuery, identity).Scan(&unlocked)
	if err == nil && unlocked {
		return nil
	}
	poisonErr := poisonPostgresConnection(connection)
	if err != nil {
		return errors.Join(err, poisonErr)
	}
	return errors.Join(errors.New("Qualification Input session advisory unlock returned false"), poisonErr)
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

func classifyPostgresWriteError(err error) error {
	if err == nil {
		return nil
	}
	if isDefinitePostgresRetryable(err) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return ErrOutcomeUnknown
	}
	switch postgresError.Code {
	case "WIP01":
		return ErrInvalid
	case "WIP02", "23502", "23503", "23505", "23514", "23P01":
		return ErrConflict
	case "WIP03":
		return ErrStale
	default:
		return ErrOutcomeUnknown
	}
}

func classifyPostgresInspectError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "WIP02" || postgresError.Code == "23514") {
		return ErrConflict
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

type postgresAdmissionRow struct {
	requestHash       sql.NullString
	requestBytes      []byte
	requestDocument   []byte
	admissionHash     sql.NullString
	admissionBytes    []byte
	admissionDocument []byte
	authorityID       sql.NullString
	executableDigest  sql.NullString
	receiptHash       sql.NullString
	admittedAt        sql.NullTime
}

func (row *postgresAdmissionRow) destinations() []any {
	return []any{
		&row.requestHash, &row.requestBytes, &row.requestDocument,
		&row.admissionHash, &row.admissionBytes, &row.admissionDocument,
		&row.authorityID, &row.executableDigest, &row.receiptHash, &row.admittedAt,
	}
}

func receiptAdmissionFromPostgresRow(kind string, row postgresAdmissionRow) (ReceiptAdmissionRecord, error) {
	if !row.requestHash.Valid || !row.admissionHash.Valid || !row.authorityID.Valid ||
		!row.executableDigest.Valid || !row.receiptHash.Valid || !row.admittedAt.Valid ||
		len(row.requestBytes) == 0 || len(row.requestDocument) == 0 || len(row.admissionBytes) == 0 ||
		len(row.admissionDocument) == 0 {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL receipt admission row is incomplete", ErrConflict)
	}
	document, err := DecodeReceiptAdmission(row.admissionBytes, row.admissionHash.String)
	if err != nil {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL receipt admission bytes are corrupt", ErrConflict)
	}
	if err := verifyJSONBProjection(row.admissionDocument, row.admissionBytes, &document); err != nil {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL receipt admission projection is corrupt", ErrConflict)
	}
	if document.Kind != kind || document.AuthorityID != row.authorityID.String ||
		document.ExecutableDigest != row.executableDigest.String || document.ReceiptHash != row.receiptHash.String ||
		document.RequestHash != row.requestHash.String {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL receipt admission scalars drifted", ErrConflict)
	}
	switch kind {
	case ReceiptKindSource:
		request, err := DecodeSourceRequest(row.requestBytes, row.requestHash.String)
		if err != nil || verifyJSONBProjection(row.requestDocument, row.requestBytes, &request) != nil {
			return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL source request projection is corrupt", ErrConflict)
		}
	case ReceiptKindCredential:
		request, err := DecodeCredentialRequest(row.requestBytes, row.requestHash.String)
		if err != nil || verifyJSONBProjection(row.requestDocument, row.requestBytes, &request) != nil {
			return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL credential request projection is corrupt", ErrConflict)
		}
	default:
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL receipt admission kind is invalid", ErrConflict)
	}
	admittedAt := row.admittedAt.Time.UTC()
	if !validDatabaseTime(admittedAt) {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL receipt admission time is corrupt", ErrConflict)
	}
	record := ReceiptAdmissionRecord{
		Document:      document,
		DocumentBytes: append([]byte(nil), row.admissionBytes...),
		RequestBytes:  append([]byte(nil), row.requestBytes...),
		AdmissionHash: row.admissionHash.String,
	}
	if err := validateReceiptAdmissionRecord(record); err != nil {
		return ReceiptAdmissionRecord{}, fmt.Errorf("%w: PostgreSQL receipt admission closure is corrupt", ErrConflict)
	}
	return record, nil
}

type postgresAuthorityRow struct {
	authorityID sql.NullString
	operationID sql.NullString

	requestHash               sql.NullString
	requestBytes              []byte
	requestDocument           []byte
	sourceRequestHash         sql.NullString
	sourceRequestBytes        []byte
	sourceRequestDocument     []byte
	credentialRequestHash     sql.NullString
	credentialRequestBytes    []byte
	credentialRequestDocument []byte
	authorityHash             sql.NullString
	authorityBytes            []byte
	authorityDocument         []byte

	workflowInputAuthorityID          sql.NullString
	workflowInputAuthorityHash        sql.NullString
	workflowInputHash                 sql.NullString
	qualificationPolicyAuthorityID    sql.NullString
	qualificationPolicyAuthorityHash  sql.NullString
	policyPlanInputProfileHash        sql.NullString
	sourcePolicyDigest                sql.NullString
	credentialProfileDocument         []byte
	qualificationPlanAuthorityID      sql.NullString
	qualificationPlanAuthorityHash    sql.NullString
	qualificationPlanInputAuthorityID sql.NullString
	qualificationPlanInputHash        sql.NullString
	sourceDocument                    []byte
	credentialSetDocument             []byte

	sourceVerifierAuthorityID          sql.NullString
	sourceVerifierExecutableDigest     sql.NullString
	sourceReceiptHash                  sql.NullString
	sourceAdmissionHash                sql.NullString
	credentialResolverAuthorityID      sql.NullString
	credentialResolverExecutableDigest sql.NullString
	credentialReceiptHash              sql.NullString
	credentialAdmissionHash            sql.NullString
	issuedAt                           sql.NullTime
}

func (row *postgresAuthorityRow) destinations() []any {
	return []any{
		&row.authorityID, &row.operationID,
		&row.requestHash, &row.requestBytes, &row.requestDocument,
		&row.sourceRequestHash, &row.sourceRequestBytes, &row.sourceRequestDocument,
		&row.credentialRequestHash, &row.credentialRequestBytes, &row.credentialRequestDocument,
		&row.authorityHash, &row.authorityBytes, &row.authorityDocument,
		&row.workflowInputAuthorityID, &row.workflowInputAuthorityHash, &row.workflowInputHash,
		&row.qualificationPolicyAuthorityID, &row.qualificationPolicyAuthorityHash,
		&row.policyPlanInputProfileHash, &row.sourcePolicyDigest, &row.credentialProfileDocument,
		&row.qualificationPlanAuthorityID, &row.qualificationPlanAuthorityHash,
		&row.qualificationPlanInputAuthorityID, &row.qualificationPlanInputHash,
		&row.sourceDocument, &row.credentialSetDocument,
		&row.sourceVerifierAuthorityID, &row.sourceVerifierExecutableDigest,
		&row.sourceReceiptHash, &row.sourceAdmissionHash,
		&row.credentialResolverAuthorityID, &row.credentialResolverExecutableDigest,
		&row.credentialReceiptHash, &row.credentialAdmissionHash, &row.issuedAt,
	}
}

func recordFromPostgresAuthorityRow(row postgresAuthorityRow) (Record, error) {
	stringsRequired := []sql.NullString{
		row.authorityID, row.operationID, row.requestHash, row.sourceRequestHash,
		row.credentialRequestHash, row.authorityHash, row.workflowInputAuthorityID,
		row.workflowInputAuthorityHash, row.workflowInputHash, row.qualificationPolicyAuthorityID,
		row.qualificationPolicyAuthorityHash, row.policyPlanInputProfileHash, row.sourcePolicyDigest,
		row.qualificationPlanAuthorityID, row.qualificationPlanAuthorityHash,
		row.qualificationPlanInputAuthorityID, row.qualificationPlanInputHash,
		row.sourceVerifierAuthorityID, row.sourceVerifierExecutableDigest, row.sourceReceiptHash,
		row.sourceAdmissionHash, row.credentialResolverAuthorityID,
		row.credentialResolverExecutableDigest, row.credentialReceiptHash, row.credentialAdmissionHash,
	}
	for _, value := range stringsRequired {
		if !value.Valid || value.String == "" {
			return Record{}, fmt.Errorf("%w: PostgreSQL precommit scalar row is incomplete", ErrConflict)
		}
	}
	bytesRequired := [][]byte{
		row.requestBytes, row.requestDocument, row.sourceRequestBytes, row.sourceRequestDocument,
		row.credentialRequestBytes, row.credentialRequestDocument, row.authorityBytes,
		row.authorityDocument, row.credentialProfileDocument, row.sourceDocument,
		row.credentialSetDocument,
	}
	for _, value := range bytesRequired {
		if len(value) == 0 {
			return Record{}, fmt.Errorf("%w: PostgreSQL precommit byte row is incomplete", ErrConflict)
		}
	}
	if !row.issuedAt.Valid {
		return Record{}, fmt.Errorf("%w: PostgreSQL precommit time is absent", ErrConflict)
	}
	operationID, err := parseExactPostgresUUID(row.operationID.String)
	if err != nil {
		return Record{}, err
	}
	authorityID, err := parseExactPostgresUUID(row.authorityID.String)
	if err != nil {
		return Record{}, err
	}
	wiaID, err := parseExactPostgresUUID(row.workflowInputAuthorityID.String)
	if err != nil {
		return Record{}, err
	}
	policyID, err := parseExactPostgresUUID(row.qualificationPolicyAuthorityID.String)
	if err != nil {
		return Record{}, err
	}
	planID, err := parseExactPostgresUUID(row.qualificationPlanAuthorityID.String)
	if err != nil {
		return Record{}, err
	}
	request, err := DecodeIssueRequest(row.requestBytes, row.requestHash.String)
	if err != nil || verifyJSONBProjection(row.requestDocument, row.requestBytes, &request) != nil {
		return Record{}, fmt.Errorf("%w: PostgreSQL issue request is corrupt", ErrConflict)
	}
	sourceRequest, err := DecodeSourceRequest(row.sourceRequestBytes, row.sourceRequestHash.String)
	if err != nil || verifyJSONBProjection(row.sourceRequestDocument, row.sourceRequestBytes, &sourceRequest) != nil {
		return Record{}, fmt.Errorf("%w: PostgreSQL source request is corrupt", ErrConflict)
	}
	credentialRequest, err := DecodeCredentialRequest(row.credentialRequestBytes, row.credentialRequestHash.String)
	if err != nil || verifyJSONBProjection(row.credentialRequestDocument, row.credentialRequestBytes, &credentialRequest) != nil {
		return Record{}, fmt.Errorf("%w: PostgreSQL credential request is corrupt", ErrConflict)
	}
	document, err := DecodeAuthority(row.authorityBytes, row.authorityHash.String)
	if err != nil || verifyJSONBProjection(row.authorityDocument, row.authorityBytes, &document) != nil {
		return Record{}, fmt.Errorf("%w: PostgreSQL authority document is corrupt", ErrConflict)
	}
	var credentialProfile CredentialProfile
	if err := verifyJSONBProjection(row.credentialProfileDocument, nil, &credentialProfile); err != nil ||
		credentialProfile != document.Policy.CredentialProfile {
		return Record{}, fmt.Errorf("%w: PostgreSQL credential profile projection drifted", ErrConflict)
	}
	var source SourceProjection
	if err := verifyJSONBProjection(row.sourceDocument, nil, &source); err != nil || source != document.Plan.Source {
		return Record{}, fmt.Errorf("%w: PostgreSQL source projection drifted", ErrConflict)
	}
	var credentialSet CredentialSetProjection
	if err := verifyJSONBProjection(row.credentialSetDocument, nil, &credentialSet); err != nil ||
		credentialSet != document.Plan.CredentialSet {
		return Record{}, fmt.Errorf("%w: PostgreSQL credential-set projection drifted", ErrConflict)
	}
	issuedAt := row.issuedAt.Time.UTC()
	if !validDatabaseTime(issuedAt) {
		return Record{}, fmt.Errorf("%w: PostgreSQL authority time is corrupt", ErrConflict)
	}
	record := Record{
		Command: IssueCommand{
			OperationID:                    operationID,
			AuthorityID:                    authorityID,
			WorkflowInputAuthorityID:       wiaID,
			QualificationPolicyAuthorityID: policyID,
			QualificationPlanAuthorityID:   planID,
		},
		Request: request, RequestBytes: append([]byte(nil), row.requestBytes...), RequestHash: row.requestHash.String,
		SourceRequest: sourceRequest, SourceRequestBytes: append([]byte(nil), row.sourceRequestBytes...),
		SourceRequestHash:      row.sourceRequestHash.String,
		CredentialRequest:      credentialRequest,
		CredentialRequestBytes: append([]byte(nil), row.credentialRequestBytes...),
		CredentialRequestHash:  row.credentialRequestHash.String,
		Document:               document, DocumentBytes: append([]byte(nil), row.authorityBytes...),
		AuthorityHash: row.authorityHash.String, IssuedAt: issuedAt,
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: PostgreSQL authority closure is corrupt", ErrConflict)
	}
	if row.workflowInputAuthorityHash.String != document.WorkflowInput.AuthorityHash ||
		row.workflowInputHash.String != document.WorkflowInput.InputHash ||
		row.qualificationPolicyAuthorityHash.String != document.Policy.AuthorityHash ||
		row.policyPlanInputProfileHash.String != document.Policy.PlanInputProfileHash ||
		row.sourcePolicyDigest.String != document.Policy.SourcePolicyDigest ||
		row.qualificationPlanAuthorityHash.String != document.Plan.AuthorityHash ||
		row.qualificationPlanInputAuthorityID.String != document.Plan.InputAuthorityID ||
		row.qualificationPlanInputHash.String != document.Plan.InputHash ||
		row.sourceVerifierAuthorityID.String != document.SourceProof.AuthorityID ||
		row.sourceVerifierExecutableDigest.String != document.SourceProof.ExecutableDigest ||
		row.sourceReceiptHash.String != document.SourceProof.ReceiptHash ||
		row.sourceAdmissionHash.String != document.SourceProof.AdmissionHash ||
		row.credentialResolverAuthorityID.String != document.CredentialProof.AuthorityID ||
		row.credentialResolverExecutableDigest.String != document.CredentialProof.ExecutableDigest ||
		row.credentialReceiptHash.String != document.CredentialProof.ReceiptHash ||
		row.credentialAdmissionHash.String != document.CredentialProof.AdmissionHash {
		return Record{}, fmt.Errorf("%w: PostgreSQL authority scalar projections drifted", ErrConflict)
	}
	return record, nil
}

func parseExactPostgresUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || !validUUIDv4Value(parsed) {
		return uuid.Nil, fmt.Errorf("%w: PostgreSQL UUID scalar is not canonical UUIDv4", ErrConflict)
	}
	return parsed, nil
}

func verifyJSONBProjection[T any](projection []byte, canonical []byte, destination *T) error {
	if len(projection) == 0 || len(projection) > MaximumCanonicalBytes || destination == nil {
		return invalid("postgresProjection", "bounded JSON object is required")
	}
	if err := rejectDuplicateNames(projection); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(projection))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return invalid("postgresProjection", "strict decode: %v", err)
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	encoded, err := CanonicalJSON(*destination)
	if err != nil {
		return err
	}
	if canonical != nil && !bytes.Equal(encoded, canonical) {
		return invalid("postgresProjection", "JSONB projection differs from retained canonical bytes")
	}
	return nil
}

const postgresAuthorityProjection = `
  authority.authority_id::text,
  authority.operation_id::text,
  authority.request_hash,
  authority.request_bytes,
  authority.request_document::text,
  authority.source_request_hash,
  authority.source_request_bytes,
  authority.source_request_document::text,
  authority.credential_request_hash,
  authority.credential_request_bytes,
  authority.credential_request_document::text,
  authority.authority_hash,
  authority.authority_bytes,
  authority.authority_document::text,
  authority.workflow_input_authority_id::text,
  authority.workflow_input_authority_hash,
  authority.workflow_input_hash,
  authority.qualification_policy_authority_id::text,
  authority.qualification_policy_authority_hash,
  authority.policy_plan_input_profile_hash,
  authority.source_policy_digest,
  authority.credential_profile_document::text,
  authority.qualification_plan_authority_id::text,
  authority.qualification_plan_authority_hash,
  authority.qualification_plan_input_authority_id::text,
  authority.qualification_plan_input_hash,
  authority.source_document::text,
  authority.credential_set_document::text,
  authority.source_verifier_authority_id,
  authority.source_verifier_executable_digest,
  authority.source_receipt_hash,
  authority.source_admission_hash,
  authority.credential_resolver_authority_id,
  authority.credential_resolver_executable_digest,
  authority.credential_receipt_hash,
  authority.credential_admission_hash,
  authority.issued_at`

const postgresAdmissionProjection = `
  admission.request_hash,
  admission.request_bytes,
  admission.request_document::text,
  admission.admission_hash,
  admission.admission_bytes,
  admission.admission_document::text,
  admission.authority_id,
  admission.executable_digest,
  admission.receipt_hash,
  admission.admitted_at`

const postgresAcquireSessionLockQuery = `
SELECT pg_catalog.pg_advisory_lock(pg_catalog.hashtextextended($1::text, 0))`

const postgresReleaseSessionLockQuery = `
SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended($1::text, 0))`

const postgresPrimaryReadWriteQuery = `
SELECT
  NOT pg_catalog.pg_is_in_recovery()
  AND pg_catalog.current_setting('transaction_read_only') = 'off'`

const postgresClockQuery = `
SELECT
  posture.primary_read_write,
  CASE WHEN posture.primary_read_write
    THEN pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp())
  END
FROM (
  SELECT NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
) AS posture`

const postgresAdmitSourceReceiptQuery = `
SELECT` + postgresAdmissionProjection + `
FROM admit_qualification_input_source_receipt_v1(
  $1, $2, $3::jsonb, $4, $5, $6::jsonb
) AS admission`

const postgresAdmitCredentialReceiptQuery = `
SELECT` + postgresAdmissionProjection + `
FROM admit_qualification_input_credential_receipt_v1(
  $1, $2, $3::jsonb, $4, $5, $6::jsonb
) AS admission`

const postgresIssueQuery = `
SELECT` + postgresAuthorityProjection + `
FROM issue_qualification_input_precommit_v1(
  $1, $2, $3, $4, $5,
  $6, $7, $8::jsonb,
  $9, $10, $11::jsonb,
  $12, $13, $14::jsonb,
  $15, $16, $17::jsonb
) AS authority`

const postgresInspectOperationInTransactionQuery = `
SELECT` + postgresAuthorityProjection + `
FROM inspect_qualification_input_precommit_operation_v1($1) AS authority`

const postgresInspectOperationQuery = `
WITH posture AS MATERIALIZED (
  SELECT NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
), authority AS MATERIALIZED (
  SELECT value.*
  FROM posture
  CROSS JOIN LATERAL inspect_qualification_input_precommit_operation_v1($1) AS value
  WHERE posture.primary_read_write
)
SELECT posture.primary_read_write,` + postgresAuthorityProjection + `
FROM posture
LEFT JOIN authority ON true`

const postgresResolveAuthorityQuery = `
WITH posture AS MATERIALIZED (
  SELECT NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
), authority AS MATERIALIZED (
  SELECT value.*
  FROM posture
  CROSS JOIN LATERAL resolve_qualification_input_precommit_authority_v1($1) AS value
  WHERE posture.primary_read_write
)
SELECT posture.primary_read_write,` + postgresAuthorityProjection + `
FROM posture
LEFT JOIN authority ON true`

const postgresInspectSourceAdmissionQuery = `
WITH posture AS MATERIALIZED (
  SELECT NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
), admission AS MATERIALIZED (
  SELECT value.*
  FROM posture
  CROSS JOIN LATERAL inspect_qualification_input_source_receipt_v1($1) AS value
  WHERE posture.primary_read_write
)
SELECT posture.primary_read_write,` + postgresAdmissionProjection + `
FROM posture
LEFT JOIN admission ON true`

const postgresInspectCredentialAdmissionQuery = `
WITH posture AS MATERIALIZED (
  SELECT NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
), admission AS MATERIALIZED (
  SELECT value.*
  FROM posture
  CROSS JOIN LATERAL inspect_qualification_input_credential_receipt_v1($1) AS value
  WHERE posture.primary_read_write
)
SELECT posture.primary_read_write,` + postgresAdmissionProjection + `
FROM posture
LEFT JOIN admission ON true`

const postgresResolveSourceAdmissionQuery = `
WITH posture AS MATERIALIZED (
  SELECT NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
), admission AS MATERIALIZED (
  SELECT value.*
  FROM posture
  CROSS JOIN LATERAL resolve_qualification_input_source_receipt_admission_v1($1) AS value
  WHERE posture.primary_read_write
)
SELECT posture.primary_read_write,` + postgresAdmissionProjection + `
FROM posture
LEFT JOIN admission ON true`

const postgresResolveCredentialAdmissionQuery = `
WITH posture AS MATERIALIZED (
  SELECT NOT pg_catalog.pg_is_in_recovery()
    AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_read_write
), admission AS MATERIALIZED (
  SELECT value.*
  FROM posture
  CROSS JOIN LATERAL resolve_qualification_input_credential_receipt_admission_v1($1) AS value
  WHERE posture.primary_read_write
)
SELECT posture.primary_read_write,` + postgresAdmissionProjection + `
FROM posture
LEFT JOIN admission ON true`

var _ Store = (*PostgresStore)(nil)
var _ DatabaseClock = (*PostgresClock)(nil)
