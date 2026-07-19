package qualificationpromotion

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type PostgresStore struct {
	database *sql.DB
	commit   func(*sql.Tx) error
}

func NewPostgresStore(database *sql.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: qualification promotion operator database is required", ErrInvalid)
	}
	return &PostgresStore{database: database, commit: func(transaction *sql.Tx) error { return transaction.Commit() }}, nil
}

func (store *PostgresStore) trustedTime(ctx context.Context) (time.Time, error) {
	if store == nil || store.database == nil || ctx == nil {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL store and context are required", ErrInvalid)
	}
	var observed time.Time
	if err := store.database.QueryRowContext(ctx, `SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&observed); err != nil {
		return time.Time{}, fmt.Errorf("query qualification promotion PostgreSQL trusted time: %w", err)
	}
	return observed.UTC(), nil
}

func (store *PostgresStore) append(ctx context.Context, command appendCommand) (ConsumptionRecord, error) {
	if store == nil || store.database == nil || store.commit == nil || ctx == nil {
		return ConsumptionRecord{}, fmt.Errorf("%w: PostgreSQL store and context are required", ErrInvalid)
	}
	if err := validatePreparedRecord(command.record); err != nil {
		return ConsumptionRecord{}, err
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ConsumptionRecord{}, fmt.Errorf("begin qualification promotion consume: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var idempotent bool
	err = transaction.QueryRowContext(ctx, `
SELECT consume_verified_qualification_promotion(
  $1, $2, $3, $4::jsonb, $5, $6, $7, $8::jsonb,
  $9, $10, $11, $12, $13::jsonb
)`,
		command.record.OperationID,
		command.record.RequestHash,
		command.record.RequestBytes,
		string(command.record.RequestBytes),
		command.record.TargetDigest,
		command.record.VerifiedPromotionHash,
		command.record.VerifiedPromotionBytes,
		string(command.record.VerifiedPromotionBytes),
		command.record.Handoff.HandoffID,
		command.record.Handoff.OutputRevisionID,
		command.record.Handoff.RevisionIntentDigest,
		command.record.Handoff.RevisionIntentBytes,
		string(command.record.Handoff.RevisionIntentBytes),
	).Scan(&idempotent)
	if err != nil {
		return ConsumptionRecord{}, classifyPostgresError(err)
	}
	record, err := scanPostgresRecord(transaction.QueryRowContext(ctx, postgresRecordQuery+` WHERE consumption.operation_id = $1`, command.record.OperationID))
	if err != nil {
		return ConsumptionRecord{}, err
	}
	if !sameImmutableRecord(record, command.record) {
		return ConsumptionRecord{}, fmt.Errorf("%w: PostgreSQL routine returned different immutable bytes", ErrConflict)
	}
	if err := store.commit(transaction); err != nil {
		return ConsumptionRecord{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	committed = true
	record.Idempotent = idempotent
	return record, nil
}

func (store *PostgresStore) inspectOperation(ctx context.Context, operationID uuid.UUID) (ConsumptionRecord, error) {
	if store == nil || store.database == nil || ctx == nil || operationID.Version() != 4 {
		return ConsumptionRecord{}, ErrNotFound
	}
	return scanPostgresRecord(store.database.QueryRowContext(ctx, postgresRecordQuery+` WHERE consumption.operation_id = $1`, operationID))
}

func (store *PostgresStore) inspectKey(ctx context.Context, key ConsumptionKey) (ConsumptionRecord, error) {
	if store == nil || store.database == nil || ctx == nil || validateTarget(key.Target) != nil ||
		!validUUIDv4(key.AuthorityNonce) || !validDigest(key.PromotionAuthorityDigest) {
		return ConsumptionRecord{}, ErrNotFound
	}
	targetHash, err := targetDigest(key.Target)
	if err != nil {
		return ConsumptionRecord{}, ErrNotFound
	}
	return scanPostgresRecord(store.database.QueryRowContext(ctx, postgresRecordQuery+`
WHERE consumption.target_digest = $1
  AND consumption.authority_nonce = $2
  AND consumption.promotion_authority_digest = $3`, targetHash, key.AuthorityNonce, key.PromotionAuthorityDigest))
}

const postgresRecordQuery = `
SELECT
  consumption.operation_id,
  consumption.qualification_authority_id,
  consumption.request_hash,
  consumption.request_bytes,
  consumption.request_document,
  consumption.target_digest,
  consumption.verified_promotion_hash,
  consumption.verified_promotion_bytes,
  consumption.verified_promotion_document,
  consumption.consumed_at,
  handoff.handoff_id,
  handoff.state,
  handoff.output_revision_id,
  handoff.revision_kind,
  handoff.revision_intent_digest,
  handoff.revision_intent_bytes,
  handoff.revision_intent_document,
  handoff.created_at,
  pg_catalog.convert_from(consumption.request_bytes, 'UTF8')::jsonb = consumption.request_document
    AND pg_catalog.convert_from(consumption.verified_promotion_bytes, 'UTF8')::jsonb = consumption.verified_promotion_document
    AND pg_catalog.convert_from(handoff.revision_intent_bytes, 'UTF8')::jsonb = handoff.revision_intent_document,
  consumption.qualification_authority_id::text = consumption.request_document->>'qualificationAuthorityId'
    AND consumption.target_digest = consumption.request_document->>'targetDigest'
    AND consumption.verified_promotion_hash = consumption.request_document->>'verifiedPromotionHash'
    AND consumption.project_id::text = consumption.verified_promotion_document->'promotionTarget'->>'projectId'
    AND consumption.workflow_run_id::text = consumption.verified_promotion_document->'promotionTarget'->>'workflowRunId'
    AND consumption.node_key = consumption.verified_promotion_document->'promotionTarget'->>'nodeKey'
    AND consumption.target_revision_id::text = consumption.verified_promotion_document->'promotionTarget'->'targetRevision'->>'id'
    AND consumption.target_revision_content_hash = consumption.verified_promotion_document->'promotionTarget'->'targetRevision'->>'contentHash'
    AND consumption.subject = consumption.verified_promotion_document->'promotionTarget'->>'subject'
    AND consumption.stage_gate = consumption.verified_promotion_document->'promotionTarget'->>'stageGate'
    AND consumption.authority_nonce::text = consumption.verified_promotion_document->>'authorityNonce'
    AND consumption.promotion_authority_digest = consumption.verified_promotion_document->>'promotionAuthorityDigest'
    AND consumption.scope = consumption.verified_promotion_document->>'scope'
    AND consumption.single_use_consumption = consumption.verified_promotion_document->>'singleUseConsumption'
    AND consumption.qualification_run_id::text = consumption.verified_promotion_document->>'runId'
    AND consumption.plan_digest = consumption.verified_promotion_document->>'planDigest'
    AND consumption.artifact_index_digest = consumption.verified_promotion_document->>'artifactIndexDigest'
    AND consumption.receipt_payload_digest = consumption.verified_promotion_document->>'receiptPayloadDigest'
    AND consumption.receipt_bundle_digest = consumption.verified_promotion_document->>'receiptBundleDigest'
    AND consumption.golden_authority_artifact_id = consumption.verified_promotion_document->'goldenRuntime'->>'authorityDocumentArtifactId'
    AND consumption.golden_authority_document_digest = consumption.verified_promotion_document->'goldenRuntime'->>'authorityDocumentDigest'
    AND consumption.golden_fault_operation_set_digest = consumption.verified_promotion_document->'goldenRuntime'->>'faultOperationSetDigest'
    AND consumption.golden_fixture_artifact_id = consumption.verified_promotion_document->'goldenRuntime'->>'fixtureDocumentArtifactId'
    AND consumption.golden_fixture_document_digest = consumption.verified_promotion_document->'goldenRuntime'->>'fixtureDocumentDigest'
    AND consumption.golden_fixture_id::text = consumption.verified_promotion_document->'goldenRuntime'->>'fixtureId'
    AND consumption.credential_issuer = consumption.verified_promotion_document->'credentialSet'->>'issuer'
    AND consumption.credential_audience = consumption.verified_promotion_document->'credentialSet'->>'audience'
    AND consumption.credential_set_handle_hash = consumption.verified_promotion_document->'credentialSet'->>'setHandleHash'
    AND consumption.credential_member_bindings_digest = consumption.verified_promotion_document->'credentialSet'->>'memberBindingsDigest'
    AND consumption.credential_member_count::text = consumption.verified_promotion_document->'credentialSet'->>'memberCount'
    AND consumption.credential_issuance_artifact_id = consumption.verified_promotion_document->'credentialSet'->>'issuanceArtifactId'
    AND consumption.credential_issuance_payload_digest = consumption.verified_promotion_document->'credentialSet'->>'issuancePayloadDigest'
    AND consumption.credential_revocation_artifact_id = consumption.verified_promotion_document->'credentialSet'->>'revocationArtifactId'
    AND consumption.credential_revocation_payload_digest = consumption.verified_promotion_document->'credentialSet'->>'revocationPayloadDigest'
    AND consumption.signer_identities = consumption.verified_promotion_document->'signerIdentities'
    AND consumption.credential_issuance_signer_identities = consumption.verified_promotion_document->'credentialIssuanceSignerIdentities'
    AND consumption.credential_revocation_signer_identities = consumption.verified_promotion_document->'credentialRevocationSignerIdentities'
    AND consumption.encryption_signer_identities = consumption.verified_promotion_document->'encryptionSignerIdentities'
    AND consumption.fault_authority_signer_identities = consumption.verified_promotion_document->'faultAuthoritySignerIdentities'
    AND consumption.fault_ledger_attestation_digest = consumption.verified_promotion_document->>'faultLedgerAttestationDigest'
    AND consumption.fault_ledger_attestor_signer_identities = consumption.verified_promotion_document->'faultLedgerAttestorSignerIdentities'
    AND to_char(consumption.authority_expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') = consumption.verified_promotion_document->>'authorityExpiresAt'
    AND to_char(consumption.receipt_issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') = consumption.verified_promotion_document->>'issuedAt'
    AND consumption.decision = consumption.verified_promotion_document->>'decision',
  handoff.operation_id = consumption.operation_id
    AND handoff.project_id = consumption.project_id
    AND handoff.workflow_run_id = consumption.workflow_run_id
    AND handoff.node_key = consumption.node_key
    AND handoff.source_revision_id = consumption.target_revision_id
    AND handoff.source_revision_content_hash = consumption.target_revision_content_hash
    AND handoff.subject = consumption.subject
    AND handoff.stage_gate = consumption.stage_gate
    AND handoff.authority_nonce = consumption.authority_nonce
    AND handoff.promotion_authority_digest = consumption.promotion_authority_digest
    AND handoff.verified_promotion_hash = consumption.verified_promotion_hash
    AND handoff.created_at = consumption.consumed_at
FROM qualification_promotion_consumptions AS consumption
JOIN qualification_promotion_handoffs AS handoff ON handoff.operation_id = consumption.operation_id`

type rowScanner interface {
	Scan(...any) error
}

func scanPostgresRecord(row rowScanner) (ConsumptionRecord, error) {
	var record ConsumptionRecord
	var requestDocument, verifiedDocument, intentDocument []byte
	var documentsExact, columnsExact, handoffExact bool
	err := row.Scan(
		&record.OperationID,
		&record.QualificationAuthorityID,
		&record.RequestHash,
		&record.RequestBytes,
		&requestDocument,
		&record.TargetDigest,
		&record.VerifiedPromotionHash,
		&record.VerifiedPromotionBytes,
		&verifiedDocument,
		&record.ConsumedAt,
		&record.Handoff.HandoffID,
		&record.Handoff.State,
		&record.Handoff.OutputRevisionID,
		&record.Handoff.RevisionKind,
		&record.Handoff.RevisionIntentDigest,
		&record.Handoff.RevisionIntentBytes,
		&intentDocument,
		&record.Handoff.CreatedAt,
		&documentsExact,
		&columnsExact,
		&handoffExact,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsumptionRecord{}, ErrNotFound
	}
	if err != nil {
		return ConsumptionRecord{}, fmt.Errorf("scan qualification promotion consumption: %w", err)
	}
	if !documentsExact || !columnsExact || !handoffExact {
		return ConsumptionRecord{}, fmt.Errorf("%w: stored explicit columns drifted from canonical evidence bytes", ErrConflict)
	}
	if err := decodeExactJSON(record.RequestBytes, &record.Request); err != nil ||
		errOrNotCanonical(record.RequestBytes, record.Request) != nil || sha256Digest(record.RequestBytes) != record.RequestHash {
		return ConsumptionRecord{}, fmt.Errorf("%w: stored consume request is not canonical or hash-bound", ErrConflict)
	}
	if err := decodeExactJSON(record.VerifiedPromotionBytes, &record.VerifiedPromotion); err != nil ||
		errOrNotCanonical(record.VerifiedPromotionBytes, record.VerifiedPromotion) != nil ||
		sha256Digest(record.VerifiedPromotionBytes) != record.VerifiedPromotionHash ||
		validateVerifiedPromotionShape(record.VerifiedPromotion) != nil {
		return ConsumptionRecord{}, fmt.Errorf("%w: stored verified promotion is not canonical or closed", ErrConflict)
	}
	if err := decodeExactJSON(record.Handoff.RevisionIntentBytes, &record.Handoff.RevisionIntent); err != nil ||
		errOrNotCanonical(record.Handoff.RevisionIntentBytes, record.Handoff.RevisionIntent) != nil ||
		sha256Digest(record.Handoff.RevisionIntentBytes) != record.Handoff.RevisionIntentDigest {
		return ConsumptionRecord{}, fmt.Errorf("%w: stored revision intent is not canonical or hash-bound", ErrConflict)
	}
	if !jsonDocumentsEqual(requestDocument, record.RequestBytes) ||
		!jsonDocumentsEqual(verifiedDocument, record.VerifiedPromotionBytes) ||
		!jsonDocumentsEqual(intentDocument, record.Handoff.RevisionIntentBytes) {
		return ConsumptionRecord{}, fmt.Errorf("%w: stored JSON projections differ from exact bytes", ErrConflict)
	}
	record.ConsumedAt = record.ConsumedAt.UTC()
	record.Handoff.CreatedAt = record.Handoff.CreatedAt.UTC()
	record.Handoff.OperationID = record.OperationID
	record.Handoff.Target = record.VerifiedPromotion.PromotionTarget
	record.Handoff.AuthorityNonce = record.VerifiedPromotion.AuthorityNonce
	record.Handoff.PromotionAuthorityDigest = record.VerifiedPromotion.PromotionAuthorityDigest
	record.Handoff.VerifiedPromotionHash = record.VerifiedPromotionHash
	if record.Request.OperationID != record.OperationID.String() ||
		record.Request.QualificationAuthorityID != record.QualificationAuthorityID.String() ||
		record.Request.HandoffID != record.Handoff.HandoffID.String() ||
		record.Request.OutputRevisionID != record.Handoff.OutputRevisionID.String() ||
		record.Request.RevisionIntentDigest != record.Handoff.RevisionIntentDigest ||
		record.Request.TargetDigest != record.TargetDigest || record.Request.VerifiedPromotionHash != record.VerifiedPromotionHash ||
		record.Request.SchemaVersion != RequestSchemaV1 || record.Handoff.State != HandoffStatePending ||
		record.Handoff.RevisionKind != RevisionIntentKindV1 ||
		record.Handoff.RevisionIntent.SchemaVersion != RevisionIntentSchemaV1 ||
		record.Handoff.RevisionIntent.RevisionKind != RevisionIntentKindV1 ||
		record.Handoff.RevisionIntent.HandoffID != record.Handoff.HandoffID.String() ||
		record.Handoff.RevisionIntent.OutputRevisionID != record.Handoff.OutputRevisionID.String() ||
		record.Handoff.RevisionIntent.AuthorityNonce != record.VerifiedPromotion.AuthorityNonce ||
		record.Handoff.RevisionIntent.PromotionAuthorityDigest != record.VerifiedPromotion.PromotionAuthorityDigest ||
		record.Handoff.RevisionIntent.VerifiedPromotionHash != record.VerifiedPromotionHash ||
		!sameTarget(record.Handoff.RevisionIntent.SourceTarget, record.VerifiedPromotion.PromotionTarget) {
		return ConsumptionRecord{}, fmt.Errorf("%w: stored request or handoff does not bind its exact promotion", ErrConflict)
	}
	computedTarget, err := targetDigest(record.VerifiedPromotion.PromotionTarget)
	if err != nil || computedTarget != record.TargetDigest {
		return ConsumptionRecord{}, fmt.Errorf("%w: stored target digest is invalid", ErrConflict)
	}
	return record, nil
}

func errOrNotCanonical(encoded []byte, value any) error {
	canonical, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, encoded) {
		return errors.New("JSON bytes are not canonical")
	}
	return nil
}

func jsonDocumentsEqual(document, exact []byte) bool {
	var left, right any
	return json.Unmarshal(document, &left) == nil && json.Unmarshal(exact, &right) == nil &&
		reflect.DeepEqual(left, right)
}

func validatePreparedRecord(record ConsumptionRecord) error {
	if record.OperationID.Version() != 4 || record.QualificationAuthorityID.Version() != 4 ||
		record.Handoff.HandoffID.Version() != 4 || record.Handoff.OutputRevisionID.Version() != 4 ||
		validateVerifiedPromotionShape(record.VerifiedPromotion) != nil || !sameTarget(record.Handoff.Target, record.VerifiedPromotion.PromotionTarget) ||
		!validDigest(record.RequestHash) || !validDigest(record.TargetDigest) || !validDigest(record.VerifiedPromotionHash) ||
		sha256Digest(record.RequestBytes) != record.RequestHash || sha256Digest(record.VerifiedPromotionBytes) != record.VerifiedPromotionHash ||
		sha256Digest(record.Handoff.RevisionIntentBytes) != record.Handoff.RevisionIntentDigest {
		return fmt.Errorf("%w: prepared PostgreSQL append is incomplete or not hash-bound", ErrInvalid)
	}
	return nil
}

func classifyPostgresError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		if strings.Contains(postgresError.Message, "expired") || strings.Contains(postgresError.Message, "time fence") {
			return fmt.Errorf("%w: %s", ErrAuthorityExpired, postgresError.Message)
		}
		if postgresError.Code == "40001" || postgresError.Code == "23505" || postgresError.Code == "23514" ||
			postgresError.Code == "23503" || postgresError.Code == "23502" || postgresError.Code == "40P01" {
			return fmt.Errorf("%w: %s", ErrConflict, postgresError.Message)
		}
	}
	return fmt.Errorf("append qualification promotion in PostgreSQL: %w", err)
}

var _ store = (*PostgresStore)(nil)
