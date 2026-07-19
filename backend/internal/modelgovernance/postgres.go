package modelgovernance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const postgresActivationColumns = `
  operation_id,
  request_hash,
  authority_kind,
  workload,
  profile_id,
  profile_content_hash,
  receipt_digest,
  receipt_payload_digest,
  activation_envelope_digest,
  activation_payload_digest,
  previous_generation,
  generation,
  previous_fence,
  fence,
  corpus_content_hash,
  provider_route_authority_hash,
  runner_immutable_digest,
  source_tree_digest,
  trust_policy_hash,
  genesis_envelope_digest,
  genesis_payload_digest,
  initial_revocation_authority_id,
  initial_revocation_authority_hash,
  initial_revocation_authority_epoch,
  activated_at`

// PostgresActivationStore is the durable ActivationStore introduced by
// migration 000069. Its DSN must use the separately provisioned governance
// authority role once that role exists; the ordinary API role deliberately has
// no access to the owner-only v1 entrypoints. The migration-owner DSN must
// never be used as a runtime substitute, so this adapter remains unwired.
type PostgresActivationStore struct {
	database *sql.DB
}

func NewPostgresActivationStore(database *sql.DB) (*PostgresActivationStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL activation database is required", ErrGovernanceInvalid)
	}
	return &PostgresActivationStore{database: database}, nil
}

func (store *PostgresActivationStore) TrustedTime(ctx context.Context) (time.Time, error) {
	if store == nil || store.database == nil || nilGovernanceDependency(ctx) {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL activation store and non-nil context are required", ErrRuntimeAuthority)
	}
	var observed sql.NullTime
	if err := store.database.QueryRowContext(ctx, `
SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&observed); err != nil {
		return time.Time{}, fmt.Errorf("%w: query PostgreSQL trusted time: %v", ErrRuntimeAuthority, err)
	}
	if !observed.Valid {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL trusted time is NULL", ErrRuntimeAuthority)
	}
	normalized, err := normalizeGovernanceTrustedTime(observed.Time)
	if err != nil || !canonicalGovernanceTime(normalized) {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL trusted time is not canonical UTC milliseconds", ErrRuntimeAuthority)
	}
	return normalized, nil
}

func (store *PostgresActivationStore) AppendActivation(ctx context.Context, command ActivationAppend) (ActivationRecord, error) {
	if store == nil || store.database == nil || nilGovernanceDependency(ctx) {
		return ActivationRecord{}, fmt.Errorf("%w: PostgreSQL activation store and non-nil context are required", ErrActivationConflict)
	}
	if err := validateActivationAppend(command); err != nil {
		return ActivationRecord{}, err
	}
	if !postgresActivationGeneration(command.ExpectedGeneration) || !postgresActivationGeneration(command.Record.Generation) {
		return ActivationRecord{}, fmt.Errorf("%w: activation generation exceeds PostgreSQL bigint", ErrActivationConflict)
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ActivationRecord{}, fmt.Errorf("begin PostgreSQL activation append: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	record, err := scanPostgresActivationRecord(transaction.QueryRowContext(ctx, `
SELECT `+postgresActivationColumns+`
FROM append_model_governance_activation(
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
  $12, $13, $14, $15, $16, $17, $18, $19, $20, $21
)`,
		int64(command.ExpectedGeneration), command.ExpectedFence,
		command.Record.OperationID, command.Record.RequestHash, command.Record.Workload,
		command.Record.ProfileID, command.Record.ProfileContentHash,
		command.Record.ReceiptDigest, command.Record.ReceiptPayloadDigest,
		command.Record.ActivationEnvelopeDigest, command.Record.ActivationPayloadDigest,
		int64(command.Record.PreviousGeneration), int64(command.Record.Generation),
		command.Record.PreviousFence, command.Record.Fence, command.Record.CorpusContentHash,
		command.Record.ProviderRouteAuthorityHash, command.Record.RunnerImmutableDigest,
		command.Record.SourceTreeDigest, command.Record.TrustPolicyHash, command.Record.ActivatedAt,
	))
	if err != nil {
		return ActivationRecord{}, classifyPostgresActivationAppendError(err)
	}
	if !sameActivationRecord(record, command.Record) {
		return ActivationRecord{}, fmt.Errorf("%w: PostgreSQL append returned a different immutable record", ErrActivationConflict)
	}
	if err := commitPostgresActivation(transaction); err != nil {
		return ActivationRecord{}, err
	}
	committed = true
	return record, nil
}

func (store *PostgresActivationStore) AppendGenesis(ctx context.Context, command GenesisAppend) (ActivationRecord, error) {
	if store == nil || store.database == nil || nilGovernanceDependency(ctx) {
		return ActivationRecord{}, fmt.Errorf("%w: PostgreSQL Genesis store and non-nil context are required", ErrActivationConflict)
	}
	if err := validateGenesisAppend(command); err != nil {
		return ActivationRecord{}, err
	}
	if !postgresActivationGeneration(command.Record.InitialRevocationAuthorityEpoch) {
		return ActivationRecord{}, fmt.Errorf("%w: Genesis revocation epoch exceeds PostgreSQL bigint", ErrActivationConflict)
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ActivationRecord{}, fmt.Errorf("begin PostgreSQL Genesis append: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	record, err := scanPostgresActivationRecord(transaction.QueryRowContext(ctx, `
SELECT `+postgresActivationColumns+`
FROM append_model_governance_genesis(
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
  $11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
  $21, $22, $23, $24, $25, $26
)`,
		command.Record.OperationID, command.Record.RequestHash, command.Record.Workload,
		command.Record.ProfileID, command.Record.ProfileContentHash,
		command.Record.ReceiptDigest, command.Record.ReceiptPayloadDigest,
		command.Record.GenesisEnvelopeDigest, command.Record.GenesisPayloadDigest,
		int64(command.Record.PreviousGeneration), int64(command.Record.Generation),
		command.Record.PreviousFence, command.Record.Fence,
		command.Record.CorpusContentHash, command.Record.ProviderRouteAuthorityHash,
		command.Record.RunnerImmutableDigest, command.Record.SourceTreeDigest,
		command.Record.TrustPolicyHash, command.CurrentTrustPolicyHash,
		command.Record.InitialRevocationAuthorityID, command.Record.InitialRevocationAuthorityHash,
		int64(command.Record.InitialRevocationAuthorityEpoch), command.CurrentRevocationAuthorityHash,
		int64(command.CurrentRevocationAuthorityEpoch), command.Record.ActivatedAt, command.ExpectedFence,
	))
	if err != nil {
		return ActivationRecord{}, classifyPostgresActivationAppendError(err)
	}
	if !sameActivationRecord(record, command.Record) {
		return ActivationRecord{}, fmt.Errorf("%w: PostgreSQL Genesis append returned different immutable bytes", ErrActivationConflict)
	}
	if err := commitPostgresActivation(transaction); err != nil {
		return ActivationRecord{}, err
	}
	committed = true
	return record, nil
}

func (store *PostgresActivationStore) GetActivationOperation(ctx context.Context, operationID string) (ActivationRecord, error) {
	if !validUUIDv4(operationID) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return store.readActivation(ctx, `
SELECT `+postgresActivationColumns+`
FROM model_governance_activation_records
WHERE operation_id = $1`, operationID)
}

func (store *PostgresActivationStore) GetActiveActivation(ctx context.Context, workload string) (ActivationRecord, error) {
	if !validStableID(workload) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return store.readActivation(ctx, `
SELECT `+prefixedPostgresActivationColumns("record")+`
FROM model_governance_activation_heads AS head
JOIN model_governance_activation_records AS record
  ON record.workload = head.workload AND record.operation_id = head.operation_id
WHERE head.workload = $1`, workload)
}

func (store *PostgresActivationStore) GetActivationGeneration(ctx context.Context, workload string, generation uint64) (ActivationRecord, error) {
	if !validStableID(workload) || generation == 0 || !postgresActivationGeneration(generation) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return store.readActivation(ctx, `
SELECT `+postgresActivationColumns+`
FROM model_governance_activation_records
WHERE workload = $1 AND generation = $2`, workload, int64(generation))
}

func (store *PostgresActivationStore) GetActivatedProfile(ctx context.Context, binding CorpusProfileBinding) (ActivationRecord, error) {
	if !validUUIDv4(binding.ID) || !validDigest(binding.ContentHash) || !validStableID(binding.Workload) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	return store.readActivation(ctx, `
SELECT `+postgresActivationColumns+`
FROM model_governance_activation_records
WHERE workload = $1 AND profile_id = $2 AND profile_content_hash = $3`,
		binding.Workload, binding.ID, binding.ContentHash)
}

func (store *PostgresActivationStore) ObserveGovernanceRevocationAuthority(ctx context.Context, authority GovernanceRevocationAuthority) error {
	if store == nil || store.database == nil || nilGovernanceDependency(ctx) {
		return fmt.Errorf("%w: PostgreSQL activation store and non-nil context are required", ErrGovernanceUntrusted)
	}
	encoded, err := CanonicalGovernanceRevocationAuthorityJSON(authority)
	if err != nil || sha256Digest(encoded) != authority.AuthorityHash {
		return fmt.Errorf("%w: revocation authority canonical bytes do not match its hash", ErrGovernanceUntrusted)
	}
	if _, err := ParseGovernanceRevocationAuthority(encoded, authority.AuthorityHash); err != nil {
		return err
	}
	now, err := store.TrustedTime(ctx)
	if err != nil {
		return err
	}
	if err := ValidateGovernanceRevocationAuthority(authority, now); err != nil {
		return err
	}
	if authority.Epoch > uint64(math.MaxInt64) {
		return fmt.Errorf("%w: revocation epoch exceeds PostgreSQL bigint", ErrGovernanceUntrusted)
	}
	// The JSONB argument is derived from the same canonical bytes. It is not an
	// independently caller-supplied authority, and SQL rechecks bytes/hash/JSON.
	if _, err := store.database.ExecContext(ctx, `
SELECT observe_model_governance_revocation_authority($1, $2, $3, $4::jsonb)`,
		int64(authority.Epoch), authority.AuthorityHash, encoded, string(encoded)); err != nil {
		return classifyPostgresRevocationObservationError(err)
	}
	return nil
}

func (store *PostgresActivationStore) ObserveGovernanceTrustPolicy(ctx context.Context, observation GovernanceTrustPolicyObservation) error {
	if store == nil || store.database == nil || nilGovernanceDependency(ctx) || !validDigest(observation.PolicyHash) ||
		!validDigest(observation.RevocationAuthorityHash) || observation.RevocationEpoch == 0 ||
		!postgresActivationGeneration(observation.RevocationEpoch) {
		return fmt.Errorf("%w: PostgreSQL trust-policy observation is invalid", ErrGovernanceUntrusted)
	}
	if _, err := store.database.ExecContext(ctx, `
SELECT observe_model_governance_trust_policy($1, $2, $3)`,
		observation.PolicyHash, observation.RevocationAuthorityHash, int64(observation.RevocationEpoch)); err != nil {
		return classifyPostgresRevocationObservationError(err)
	}
	return nil
}

func (store *PostgresActivationStore) readActivation(ctx context.Context, query string, arguments ...any) (ActivationRecord, error) {
	if store == nil || store.database == nil || nilGovernanceDependency(ctx) {
		return ActivationRecord{}, fmt.Errorf("%w: PostgreSQL activation store and non-nil context are required", ErrRuntimeAuthority)
	}
	record, err := scanPostgresActivationRecord(store.database.QueryRowContext(ctx, query, arguments...))
	if errors.Is(err, sql.ErrNoRows) {
		return ActivationRecord{}, ErrActivationNotFound
	}
	if err != nil {
		return ActivationRecord{}, err
	}
	return record, nil
}

type postgresActivationRowScanner interface {
	Scan(...any) error
}

func scanPostgresActivationRecord(row postgresActivationRowScanner) (ActivationRecord, error) {
	var operationID, profileID uuid.NullUUID
	var requestHash, authorityKind, workload, profileHash sql.NullString
	var receiptDigest, receiptPayloadDigest sql.NullString
	var activationEnvelopeDigest, activationPayloadDigest sql.NullString
	var previousGeneration, generation sql.NullInt64
	var previousFence, fence, corpusHash, routeHash sql.NullString
	var runnerDigest, sourceDigest, policyHash sql.NullString
	var genesisEnvelopeDigest, genesisPayloadDigest sql.NullString
	var revocationAuthorityID, revocationAuthorityHash sql.NullString
	var revocationAuthorityEpoch sql.NullInt64
	var activatedAt sql.NullTime
	if err := row.Scan(
		&operationID, &requestHash, &authorityKind, &workload, &profileID, &profileHash,
		&receiptDigest, &receiptPayloadDigest, &activationEnvelopeDigest,
		&activationPayloadDigest, &previousGeneration, &generation,
		&previousFence, &fence, &corpusHash, &routeHash, &runnerDigest,
		&sourceDigest, &policyHash, &genesisEnvelopeDigest, &genesisPayloadDigest,
		&revocationAuthorityID, &revocationAuthorityHash, &revocationAuthorityEpoch, &activatedAt,
	); err != nil {
		return ActivationRecord{}, err
	}
	if !operationID.Valid || !requestHash.Valid || !authorityKind.Valid || !workload.Valid || !profileID.Valid || !profileHash.Valid ||
		!receiptDigest.Valid || !receiptPayloadDigest.Valid || !activationEnvelopeDigest.Valid ||
		!activationPayloadDigest.Valid || !previousGeneration.Valid || !generation.Valid ||
		!previousFence.Valid || !fence.Valid || !corpusHash.Valid || !routeHash.Valid ||
		!runnerDigest.Valid || !sourceDigest.Valid || !policyHash.Valid || !activatedAt.Valid ||
		previousGeneration.Int64 < 0 || generation.Int64 <= 0 {
		return ActivationRecord{}, fmt.Errorf("%w: PostgreSQL activation row contains NULL or invalid scalar values", ErrActivationConflict)
	}
	record := ActivationRecord{
		AuthorityKind: authorityKind.String,
		OperationID:   operationID.UUID.String(), RequestHash: requestHash.String,
		Workload: workload.String, ProfileID: profileID.UUID.String(), ProfileContentHash: profileHash.String,
		ReceiptDigest: receiptDigest.String, ReceiptPayloadDigest: receiptPayloadDigest.String,
		ActivationEnvelopeDigest: activationEnvelopeDigest.String,
		ActivationPayloadDigest:  activationPayloadDigest.String,
		PreviousGeneration:       uint64(previousGeneration.Int64), Generation: uint64(generation.Int64),
		PreviousFence: previousFence.String, Fence: fence.String,
		CorpusContentHash: corpusHash.String, ProviderRouteAuthorityHash: routeHash.String,
		RunnerImmutableDigest: runnerDigest.String, SourceTreeDigest: sourceDigest.String,
		TrustPolicyHash:       policyHash.String,
		GenesisEnvelopeDigest: genesisEnvelopeDigest.String, GenesisPayloadDigest: genesisPayloadDigest.String,
		InitialRevocationAuthorityID:   revocationAuthorityID.String,
		InitialRevocationAuthorityHash: revocationAuthorityHash.String,
		ActivatedAt:                    activatedAt.Time.UTC(),
	}
	if revocationAuthorityEpoch.Valid && revocationAuthorityEpoch.Int64 > 0 {
		record.InitialRevocationAuthorityEpoch = uint64(revocationAuthorityEpoch.Int64)
	} else if revocationAuthorityEpoch.Valid && revocationAuthorityEpoch.Int64 != 0 {
		return ActivationRecord{}, fmt.Errorf("%w: PostgreSQL activation row has invalid revocation epoch", ErrActivationConflict)
	}
	if err := validateRegistryRecord(record); err != nil {
		return ActivationRecord{}, fmt.Errorf("%w: stored PostgreSQL activation row is invalid: %v", ErrActivationConflict, err)
	}
	return record, nil
}

type postgresActivationCommitter interface {
	Commit() error
}

func commitPostgresActivation(transaction postgresActivationCommitter) error {
	if err := transaction.Commit(); err != nil {
		return errors.Join(
			ErrActivationOutcomeUnknown,
			fmt.Errorf("commit PostgreSQL activation append: %w", err),
		)
	}
	return nil
}

func classifyPostgresActivationAppendError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "23502", "23503", "23505", "23514", "22001", "22003", "22007", "22021", "22023", "22P02":
			return errors.Join(ErrActivationConflict, fmt.Errorf("append PostgreSQL activation: %w", err))
		}
	}
	return fmt.Errorf("append PostgreSQL activation: %w", err)
}

func classifyPostgresRevocationObservationError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "23502", "23505", "23514", "22001", "22003", "22007", "22021", "22023", "22P02":
			return errors.Join(ErrGovernanceUntrusted, fmt.Errorf("observe PostgreSQL revocation authority: %w", err))
		}
	}
	return fmt.Errorf("observe PostgreSQL revocation authority: %w", err)
}

func postgresActivationGeneration(value uint64) bool {
	return value <= uint64(math.MaxInt64)
}

func prefixedPostgresActivationColumns(prefix string) string {
	return "\n  " + prefix + ".operation_id,\n" +
		"  " + prefix + ".request_hash,\n" +
		"  " + prefix + ".authority_kind,\n" +
		"  " + prefix + ".workload,\n" +
		"  " + prefix + ".profile_id,\n" +
		"  " + prefix + ".profile_content_hash,\n" +
		"  " + prefix + ".receipt_digest,\n" +
		"  " + prefix + ".receipt_payload_digest,\n" +
		"  " + prefix + ".activation_envelope_digest,\n" +
		"  " + prefix + ".activation_payload_digest,\n" +
		"  " + prefix + ".previous_generation,\n" +
		"  " + prefix + ".generation,\n" +
		"  " + prefix + ".previous_fence,\n" +
		"  " + prefix + ".fence,\n" +
		"  " + prefix + ".corpus_content_hash,\n" +
		"  " + prefix + ".provider_route_authority_hash,\n" +
		"  " + prefix + ".runner_immutable_digest,\n" +
		"  " + prefix + ".source_tree_digest,\n" +
		"  " + prefix + ".trust_policy_hash,\n" +
		"  " + prefix + ".genesis_envelope_digest,\n" +
		"  " + prefix + ".genesis_payload_digest,\n" +
		"  " + prefix + ".initial_revocation_authority_id,\n" +
		"  " + prefix + ".initial_revocation_authority_hash,\n" +
		"  " + prefix + ".initial_revocation_authority_epoch,\n" +
		"  " + prefix + ".activated_at"
}

var _ ActivationStore = (*PostgresActivationStore)(nil)
var _ GenesisBootstrapStore = (*PostgresActivationStore)(nil)
