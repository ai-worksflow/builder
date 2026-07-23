package qualificationpolicyauthority

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrCorrupt reports that a durable row passed the SQL capability boundary but
// did not reproduce the exact independently validated aggregate. It joins
// ErrConflict so callers fail closed without mistaking corruption for absence.
var ErrCorrupt = errors.New("qualification policy authority durable aggregate is corrupt")

// PostgresStore persists Qualification Policy Authority generations through
// migration 000078's capability functions. It never reads the backing tables
// directly, so the store remains usable by the least-privilege operator role.
type PostgresStore struct {
	database *sql.DB
	commit   func(*sql.Tx) error
}

func NewPostgresStore(database *sql.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, invalid("store.database", "PostgreSQL database is required")
	}
	return &PostgresStore{
		database: database,
		commit: func(transaction *sql.Tx) error {
			return transaction.Commit()
		},
	}, nil
}

func (store *PostgresStore) Issue(ctx context.Context, candidate Record) (Record, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return Record{}, invalid("store", "PostgreSQL store and context are required")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if candidate.Idempotent {
		return Record{}, invalid("store", "candidate cannot author response idempotency metadata")
	}
	if err := ValidateRecord(candidate); err != nil {
		return Record{}, err
	}
	projectID, err := parsePostgresQualificationPolicyUUID(candidate.Document.ProjectID)
	if err != nil {
		return Record{}, err
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Record{}, fmt.Errorf("begin PostgreSQL Qualification Policy Authority issue: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	var insertedByCurrentTransaction bool
	stored, err := scanPostgresQualificationPolicyRecord(
		transaction.QueryRowContext(
			ctx,
			postgresQualificationPolicyIssueQuery,
			candidate.Command.OperationID,
			candidate.Command.AuthorityID,
			candidate.Command.PolicySourceID,
			candidate.Command.ExpectedPreviousAuthorityHash,
			projectID,
			candidate.Document.ExecutionProfile.Version,
			candidate.Document.ExecutionProfile.Hash,
			candidate.Document.Generation,
			candidate.Document.Status,
			candidate.IssuedAt,
			candidate.Document.ExternalGatePolicy,
			candidate.Document.SupersessionPolicy,
			candidate.RevisionPolicyHash,
			candidate.RevisionPolicyBytes,
			string(candidate.RevisionPolicyBytes),
			candidate.PlanInputProfileHash,
			candidate.PlanInputProfileBytes,
			string(candidate.PlanInputProfileBytes),
			candidate.PromotionPolicyHash,
			candidate.PromotionPolicyBytes,
			string(candidate.PromotionPolicyBytes),
			candidate.AuthorityHash,
			candidate.DocumentBytes,
			string(candidate.DocumentBytes),
		),
		&insertedByCurrentTransaction,
	)
	if err != nil {
		classified := classifyPostgresQualificationPolicyError("issue", err, postgresQualificationPolicyIssueError)
		_ = transaction.Rollback()
		if errors.Is(classified, ErrConflict) && !errors.Is(classified, ErrCorrupt) {
			// A SERIALIZABLE waiter can take its statement snapshot before an
			// exact concurrent winner releases the issuer's advisory lock. The
			// resulting serialization/unique conflict is reconciled only by this
			// operation identity and only when every immutable byte is exact.
			existing, reconcileErr := store.InspectOperation(ctx, candidate.Command.OperationID)
			switch {
			case reconcileErr == nil && sameImmutableRecord(existing, candidate):
				existing.Idempotent = true
				return cloneRecord(existing), nil
			case reconcileErr != nil && !errors.Is(reconcileErr, ErrNotFound):
				return Record{}, reconcileErr
			}
		}
		return Record{}, classified
	}
	if !sameImmutableRecord(stored, candidate) {
		return Record{}, corruptPostgresQualificationPolicy("issuer returned immutable state different from the candidate")
	}

	if !insertedByCurrentTransaction {
		if err := transaction.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return Record{}, fmt.Errorf("close PostgreSQL Qualification Policy Authority replay transaction: %w", err)
		}
		stored.Idempotent = true
		return cloneRecord(stored), nil
	}

	commit := store.commit
	if commit == nil {
		commit = func(transaction *sql.Tx) error { return transaction.Commit() }
	}
	if err := commit(transaction); err != nil {
		return Record{}, errors.Join(
			ErrStoreOutcomeUnknown,
			fmt.Errorf("commit PostgreSQL Qualification Policy Authority issue: %w", err),
		)
	}
	stored.Idempotent = false
	return cloneRecord(stored), nil
}

func (store *PostgresStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if !validPostgresQualificationPolicyLookup(store, ctx, operationID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresQualificationPolicyRecord(store.database.QueryRowContext(
		ctx, postgresQualificationPolicyInspectOperationQuery, operationID,
	), nil)
	if err != nil {
		return Record{}, classifyPostgresQualificationPolicyError("inspect operation", err, postgresQualificationPolicyReadError)
	}
	return cloneRecord(record), nil
}

func (store *PostgresStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if !validPostgresQualificationPolicyLookup(store, ctx, authorityID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresQualificationPolicyRecord(store.database.QueryRowContext(
		ctx, postgresQualificationPolicyResolveAuthorityQuery, authorityID,
	), nil)
	if err != nil {
		return Record{}, classifyPostgresQualificationPolicyError("resolve authority", err, postgresQualificationPolicyReadError)
	}
	return cloneRecord(record), nil
}

func (store *PostgresStore) ResolveCurrent(
	ctx context.Context,
	projectID uuid.UUID,
	profile ExecutionProfileBinding,
) (Record, error) {
	if !validPostgresQualificationPolicyLookup(store, ctx, projectID) ||
		profile.Version != ExecutionProfileV3 || !validDigest(profile.Hash) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresQualificationPolicyRecord(store.database.QueryRowContext(
		ctx,
		postgresQualificationPolicyResolveCurrentQuery,
		projectID,
		profile.Version,
		profile.Hash,
	), nil)
	if err != nil {
		return Record{}, classifyPostgresQualificationPolicyError("resolve current authority", err, postgresQualificationPolicyReadError)
	}
	if !headMatchesRecord(projectID, profile, record) {
		return Record{}, corruptPostgresQualificationPolicy("current resolver returned a different project or execution profile")
	}
	return cloneRecord(record), nil
}

// AssertCurrent is diagnostic only. Its implicit statement transaction releases
// the database assertion locks before a later mutation can consume the result.
// Mutation consumers must call AssertCurrentTx inside their owning transaction.
func (store *PostgresStore) AssertCurrent(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if !validPostgresQualificationPolicyLookup(store, ctx, authorityID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresQualificationPolicyRecord(store.database.QueryRowContext(
		ctx, postgresQualificationPolicyAssertCurrentQuery, authorityID,
	), nil)
	if err != nil {
		return Record{}, classifyPostgresQualificationPolicyError("assert current authority", err, postgresQualificationPolicyAssertError)
	}
	return cloneRecord(record), nil
}

// AssertCurrentTx is the mutation-authority path. The caller owns transaction
// completion, and the project/authority locks acquired by the SQL assertion
// remain held until that caller commits or rolls back. To preserve the migration
// lock order, callers must invoke this before touching protected relations.
func (store *PostgresStore) AssertCurrentTx(
	ctx context.Context,
	transaction *sql.Tx,
	authorityID uuid.UUID,
) (Record, error) {
	if transaction == nil {
		return Record{}, invalid("store.transaction", "an existing PostgreSQL transaction is required")
	}
	if !validPostgresQualificationPolicyLookup(store, ctx, authorityID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := scanPostgresQualificationPolicyRecord(transaction.QueryRowContext(
		ctx, postgresQualificationPolicyAssertCurrentQuery, authorityID,
	), nil)
	if err != nil {
		return Record{}, classifyPostgresQualificationPolicyError(
			"assert current authority in caller transaction",
			err,
			postgresQualificationPolicyAssertError,
		)
	}
	return cloneRecord(record), nil
}

// PostgresClock obtains database authority time at exact millisecond precision.
type PostgresClock struct {
	database *sql.DB
}

func NewPostgresClock(database *sql.DB) (*PostgresClock, error) {
	if database == nil {
		return nil, invalid("databaseClock.database", "PostgreSQL database is required")
	}
	return &PostgresClock{database: database}, nil
}

func (clock *PostgresClock) Now(ctx context.Context) (time.Time, error) {
	if clock == nil || clock.database == nil || isNilInterface(ctx) {
		return time.Time{}, invalid("databaseClock", "PostgreSQL clock and context are required")
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	var value time.Time
	if err := clock.database.QueryRowContext(ctx, postgresQualificationPolicyClockQuery).Scan(&value); err != nil {
		return time.Time{}, fmt.Errorf("read PostgreSQL Qualification Policy Authority clock: %w", err)
	}
	normalized, err := normalizePostgresQualificationPolicyTime(value)
	if err != nil {
		return time.Time{}, err
	}
	return normalized, nil
}

func validPostgresQualificationPolicyLookup(store *PostgresStore, ctx context.Context, identity uuid.UUID) bool {
	return store != nil && store.database != nil && !isNilInterface(ctx) && identity.Version() == 4
}

const postgresQualificationPolicyColumns = `
source.authority_id::text,
source.operation_id::text,
source.creation_transaction_id,
source.policy_source_id,
source.expected_previous_authority_hash,
source.project_id::text,
source.execution_profile_version,
source.execution_profile_hash,
source.generation,
source.previous_authority_hash,
source.status,
source.issued_at,
source.external_gate_policy,
source.supersession_policy,
source.revision_policy_hash,
source.revision_policy_bytes,
source.revision_policy_document,
source.plan_input_profile_hash,
source.plan_input_profile_bytes,
source.plan_input_profile_document,
source.promotion_policy_hash,
source.promotion_policy_bytes,
source.promotion_policy_document,
source.authority_hash,
source.authority_bytes,
source.authority_document`

const postgresQualificationPolicyIssueQuery = `
SELECT ` + postgresQualificationPolicyColumns + `,
       source.creation_transaction_id = txid_current()
FROM issue_qualification_policy_authority_v1(
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
  $13,$14,$15::jsonb,$16,$17,$18::jsonb,$19,$20,$21::jsonb,$22,$23,$24::jsonb
) AS source`

const postgresQualificationPolicyInspectOperationQuery = `
SELECT ` + postgresQualificationPolicyColumns + `
FROM inspect_qualification_policy_operation_v1($1) AS source`

const postgresQualificationPolicyResolveAuthorityQuery = `
SELECT ` + postgresQualificationPolicyColumns + `
FROM resolve_qualification_policy_authority_v1($1) AS source`

const postgresQualificationPolicyResolveCurrentQuery = `
SELECT ` + postgresQualificationPolicyColumns + `
FROM resolve_current_qualification_policy_authority_v1($1,$2,$3) AS source`

const postgresQualificationPolicyAssertCurrentQuery = `
SELECT ` + postgresQualificationPolicyColumns + `
FROM assert_current_qualification_policy_authority_v1($1) AS source`

const postgresQualificationPolicyClockQuery = `
SELECT date_trunc('milliseconds', clock_timestamp())`

type postgresQualificationPolicyRow interface {
	Scan(...any) error
}

type postgresQualificationPolicyRecordWire struct {
	authorityID                   string
	operationID                   string
	creationTransactionID         int64
	policySourceID                string
	expectedPreviousAuthorityHash sql.NullString
	projectID                     string
	executionProfileVersion       string
	executionProfileHash          string
	generation                    int64
	previousAuthorityHash         sql.NullString
	status                        string
	issuedAt                      time.Time
	externalGatePolicy            string
	supersessionPolicy            string
	revisionPolicyHash            string
	revisionPolicyBytes           []byte
	revisionPolicyDocument        []byte
	planInputProfileHash          string
	planInputProfileBytes         []byte
	planInputProfileDocument      []byte
	promotionPolicyHash           string
	promotionPolicyBytes          []byte
	promotionPolicyDocument       []byte
	authorityHash                 string
	authorityBytes                []byte
	authorityDocument             []byte
}

func scanPostgresQualificationPolicyRecord(
	row postgresQualificationPolicyRow,
	insertedByCurrentTransaction *bool,
) (Record, error) {
	if row == nil {
		return Record{}, corruptPostgresQualificationPolicy("database row is absent")
	}
	var wire postgresQualificationPolicyRecordWire
	destinations := []any{
		&wire.authorityID,
		&wire.operationID,
		&wire.creationTransactionID,
		&wire.policySourceID,
		&wire.expectedPreviousAuthorityHash,
		&wire.projectID,
		&wire.executionProfileVersion,
		&wire.executionProfileHash,
		&wire.generation,
		&wire.previousAuthorityHash,
		&wire.status,
		&wire.issuedAt,
		&wire.externalGatePolicy,
		&wire.supersessionPolicy,
		&wire.revisionPolicyHash,
		&wire.revisionPolicyBytes,
		&wire.revisionPolicyDocument,
		&wire.planInputProfileHash,
		&wire.planInputProfileBytes,
		&wire.planInputProfileDocument,
		&wire.promotionPolicyHash,
		&wire.promotionPolicyBytes,
		&wire.promotionPolicyDocument,
		&wire.authorityHash,
		&wire.authorityBytes,
		&wire.authorityDocument,
	}
	if insertedByCurrentTransaction != nil {
		destinations = append(destinations, insertedByCurrentTransaction)
	}
	if err := row.Scan(destinations...); err != nil {
		return Record{}, err
	}
	if wire.creationTransactionID <= 0 {
		return Record{}, corruptPostgresQualificationPolicy("creation transaction identity is invalid")
	}

	authorityID, err := parsePostgresQualificationPolicyUUID(wire.authorityID)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("authority identity: %v", err)
	}
	operationID, err := parsePostgresQualificationPolicyUUID(wire.operationID)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("operation identity: %v", err)
	}
	projectID, err := parsePostgresQualificationPolicyUUID(wire.projectID)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("project identity: %v", err)
	}
	issuedAt, err := normalizePostgresQualificationPolicyTime(wire.issuedAt)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("issued time: %v", err)
	}

	revisionPolicy, err := DecodeRevisionPolicy(wire.revisionPolicyBytes, wire.revisionPolicyHash)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("revision policy bytes: %v", err)
	}
	planInputProfile, err := DecodePlanInputProfile(wire.planInputProfileBytes, wire.planInputProfileHash)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("plan input profile bytes: %v", err)
	}
	promotionPolicy, err := DecodePromotionPolicy(wire.promotionPolicyBytes, wire.promotionPolicyHash)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("promotion policy bytes: %v", err)
	}
	authorityDocument, err := DecodeAuthorityDocument(wire.authorityBytes, wire.authorityHash)
	if err != nil {
		return Record{}, corruptPostgresQualificationPolicy("authority bytes: %v", err)
	}

	var revisionProjection RevisionPolicy
	if err := decodePostgresQualificationPolicyProjection(
		wire.revisionPolicyDocument,
		wire.revisionPolicyBytes,
		MaximumCanonicalBytes,
		&revisionProjection,
	); err != nil {
		return Record{}, corruptPostgresQualificationPolicy("revision policy JSONB projection: %v", err)
	}
	var planProjection PlanInputProfile
	if err := decodePostgresQualificationPolicyProjection(
		wire.planInputProfileDocument,
		wire.planInputProfileBytes,
		MaximumCanonicalBytes,
		&planProjection,
	); err != nil {
		return Record{}, corruptPostgresQualificationPolicy("plan input profile JSONB projection: %v", err)
	}
	var promotionProjection PromotionPolicy
	if err := decodePostgresQualificationPolicyProjection(
		wire.promotionPolicyDocument,
		wire.promotionPolicyBytes,
		MaximumCanonicalBytes,
		&promotionProjection,
	); err != nil {
		return Record{}, corruptPostgresQualificationPolicy("promotion policy JSONB projection: %v", err)
	}
	var authorityProjection AuthorityDocument
	if err := decodePostgresQualificationPolicyProjection(
		wire.authorityDocument,
		wire.authorityBytes,
		MaximumAuthorityBytes,
		&authorityProjection,
	); err != nil {
		return Record{}, corruptPostgresQualificationPolicy("authority JSONB projection: %v", err)
	}
	if !reflect.DeepEqual(revisionProjection, revisionPolicy) ||
		!reflect.DeepEqual(planProjection, planInputProfile) ||
		!reflect.DeepEqual(promotionProjection, promotionPolicy) ||
		!reflect.DeepEqual(authorityProjection, authorityDocument) {
		return Record{}, corruptPostgresQualificationPolicy("JSONB and retained-byte typed projections differ")
	}

	expectedPreviousAuthorityHash, err := postgresQualificationPolicyNullableHash(
		wire.expectedPreviousAuthorityHash,
		"expected previous authority hash",
	)
	if err != nil {
		return Record{}, err
	}
	previousAuthorityHash, err := postgresQualificationPolicyNullableHash(
		wire.previousAuthorityHash,
		"previous authority hash",
	)
	if err != nil {
		return Record{}, err
	}
	var previousAuthorityHashPointer *string
	if previousAuthorityHash != "" {
		previousAuthorityHashPointer = &previousAuthorityHash
	}

	record := Record{
		Command: IssueCommand{
			OperationID:                   operationID,
			AuthorityID:                   authorityID,
			PolicySourceID:                wire.policySourceID,
			ExpectedPreviousAuthorityHash: expectedPreviousAuthorityHash,
		},
		RevisionPolicy:        revisionPolicy,
		RevisionPolicyBytes:   append([]byte(nil), wire.revisionPolicyBytes...),
		RevisionPolicyHash:    wire.revisionPolicyHash,
		PlanInputProfile:      planInputProfile,
		PlanInputProfileBytes: append([]byte(nil), wire.planInputProfileBytes...),
		PlanInputProfileHash:  wire.planInputProfileHash,
		PromotionPolicy:       promotionPolicy,
		PromotionPolicyBytes:  append([]byte(nil), wire.promotionPolicyBytes...),
		PromotionPolicyHash:   wire.promotionPolicyHash,
		Document:              authorityDocument,
		DocumentBytes:         append([]byte(nil), wire.authorityBytes...),
		AuthorityHash:         wire.authorityHash,
		IssuedAt:              issuedAt,
	}

	if authorityDocument.AuthorityID != wire.authorityID ||
		authorityDocument.OperationID != wire.operationID ||
		authorityDocument.PolicySourceID != wire.policySourceID ||
		authorityDocument.ProjectID != projectID.String() ||
		authorityDocument.ExecutionProfile.Version != wire.executionProfileVersion ||
		authorityDocument.ExecutionProfile.Hash != wire.executionProfileHash ||
		authorityDocument.Generation != wire.generation ||
		authorityDocument.Status != wire.status ||
		authorityDocument.ExternalGatePolicy != wire.externalGatePolicy ||
		authorityDocument.SupersessionPolicy != wire.supersessionPolicy ||
		authorityDocument.ComponentDigests.RevisionPolicy != wire.revisionPolicyHash ||
		authorityDocument.ComponentDigests.PlanInputProfile != wire.planInputProfileHash ||
		authorityDocument.ComponentDigests.PromotionPolicy != wire.promotionPolicyHash ||
		expectedPreviousAuthorityHash != previousAuthorityHash ||
		!equalPostgresQualificationPolicyOptionalHash(authorityDocument.PreviousAuthorityHash, previousAuthorityHashPointer) {
		return Record{}, corruptPostgresQualificationPolicy("scalar columns differ from the exact authority document")
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, corruptPostgresQualificationPolicy("recovered aggregate validation: %v", err)
	}
	return cloneRecord(record), nil
}

func decodePostgresQualificationPolicyProjection(
	encoded []byte,
	exact []byte,
	maximum int,
	destination any,
) error {
	if len(encoded) == 0 || len(encoded) > maximum || !utf8.Valid(encoded) ||
		bytes.HasPrefix(encoded, []byte{0xef, 0xbb, 0xbf}) {
		return fmt.Errorf("must be bounded BOM-free UTF-8")
	}
	if err := rejectDuplicateNames(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("strict decode: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	canonical, err := canonicalJSONWithLimit(destination, maximum)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, exact) {
		return fmt.Errorf("does not reproduce the exact retained canonical bytes")
	}
	return nil
}

func postgresQualificationPolicyNullableHash(value sql.NullString, field string) (string, error) {
	if !value.Valid {
		return "", nil
	}
	if !validDigest(value.String) {
		return "", corruptPostgresQualificationPolicy("%s is invalid", field)
	}
	return value.String, nil
}

func equalPostgresQualificationPolicyOptionalHash(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func parsePostgresQualificationPolicyUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.Version() != 4 || parsed.String() != value {
		return uuid.Nil, invalid("postgres.identity", "must be an exact canonical UUIDv4")
	}
	return parsed, nil
}

func normalizePostgresQualificationPolicyTime(value time.Time) (time.Time, error) {
	if value.IsZero() || value.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, invalid("databaseClock", "must return a timestamp in range with millisecond precision")
	}
	normalized := value.Round(0).UTC()
	if _, err := formatCanonicalDatabaseTime(normalized); err != nil {
		return time.Time{}, err
	}
	return normalized, nil
}

type postgresQualificationPolicyErrorMode uint8

const (
	postgresQualificationPolicyIssueError postgresQualificationPolicyErrorMode = iota
	postgresQualificationPolicyReadError
	postgresQualificationPolicyAssertError
)

func classifyPostgresQualificationPolicyError(
	operation string,
	err error,
	mode postgresQualificationPolicyErrorMode,
) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCorrupt) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if mode == postgresQualificationPolicyIssueError {
			return corruptPostgresQualificationPolicy("issuer returned no row")
		}
		return ErrNotFound
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return fmt.Errorf("%s PostgreSQL Qualification Policy Authority: %w", operation, err)
	}
	detail := fmt.Errorf("%s PostgreSQL Qualification Policy Authority: %w", operation, err)
	switch postgresError.Code {
	case "WPA01":
		return errors.Join(ErrInvalid, detail)
	case "WPA02":
		if mode == postgresQualificationPolicyAssertError {
			// SQL v1 deliberately uses the same state for absent, suspended,
			// superseded, and failed-exactness assertions. The consuming
			// boundary therefore has one fail-closed stale classification.
			return errors.Join(ErrStale, detail)
		}
		return errors.Join(ErrNotFound, detail)
	case "WPA03":
		if mode == postgresQualificationPolicyIssueError {
			return errors.Join(ErrConflict, detail)
		}
		return errors.Join(ErrConflict, ErrCorrupt, detail)
	case "40001", "40P01", "23503", "23505":
		return errors.Join(ErrConflict, detail)
	case "23502", "23514", "22001", "22003", "22007", "22021", "22023", "22P02":
		if mode == postgresQualificationPolicyIssueError {
			return errors.Join(ErrInvalid, detail)
		}
		return errors.Join(ErrConflict, ErrCorrupt, detail)
	default:
		return detail
	}
}

func corruptPostgresQualificationPolicy(format string, arguments ...any) error {
	detail := fmt.Sprintf(format, arguments...)
	return errors.Join(
		ErrConflict,
		ErrCorrupt,
		fmt.Errorf("PostgreSQL Qualification Policy Authority: %s", detail),
	)
}

var _ Store = (*PostgresStore)(nil)
var _ DatabaseClock = (*PostgresClock)(nil)
