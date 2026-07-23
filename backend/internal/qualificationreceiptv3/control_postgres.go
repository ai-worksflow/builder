package qualificationreceiptv3

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const (
	maximumPostgresControlRequestBytes        = 16 << 20
	maximumPostgresControlPayloadBytes        = 8 << 20
	maximumPostgresControlPAEBytes            = (8 << 20) + 256
	maximumPostgresControlObservationBytes    = 1 << 20
	maximumPostgresControlAuthenticationBytes = 4 << 20
	maximumPostgresControlResultBytes         = 8 << 20
	maximumPostgresControlTokenBytes          = 1 << 20
	maximumPostgresControlEnvelopeBytes       = 16 << 20
	maximumPostgresControlCompletionBytes     = 1 << 20
)

// PostgresStore is the owner-only durable adapter for migration 000075. It
// deliberately carries no DSN, runtime role, grant bootstrap, HTTP surface,
// signer, or operational authority. Every returned row is rebuilt from raw
// canonical bytes and cross-checked against JSONB and scalar projections.
type PostgresStore struct {
	database *sql.DB
	commit   func(*sql.Tx) error
}

func NewPostgresStore(database *sql.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL Receipt v3 control database is required", ErrControlInvalid)
	}
	return &PostgresStore{
		database: database,
		commit:   func(transaction *sql.Tx) error { return transaction.Commit() },
	}, nil
}

// NewPostgresControlStore is an explicit alias for callers that keep several
// package-local PostgreSQL stores in one composition root.
func NewPostgresControlStore(database *sql.DB) (*PostgresStore, error) {
	return NewPostgresStore(database)
}

// PostgresExpectedResolver resolves only the two immutable receipt-sign
// requests. It never reads expected payload material from a submitted DSSE
// envelope or from mutable application state.
type PostgresExpectedResolver struct {
	database *sql.DB
}

func NewPostgresExpectedResolver(database *sql.DB) (*PostgresExpectedResolver, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL expected Receipt resolver database is required", ErrControlInvalid)
	}
	return &PostgresExpectedResolver{database: database}, nil
}

const postgresControlRequestColumns = `
request_hash, request_bytes, request_document, plan_authority_id, orchestration_id,
operation_id, request_kind, signer_role, operational_authority_id, authentication_key_id,
signer_identity, signer_key_id, snapshot_id, snapshot_digest, receipt_id,
plan_authority_hash, input_hash, projection_hash, evidence_plan_hash, target_hash,
trust_hash, trust_bindings_digest, trust_policy_digest, evidence_head_version,
evidence_last_event_id, evidence_last_event_hash, evidence_command_digest,
evidence_trust_digest, evidence_closure_digest, artifact_index_digest,
payload_hash, payload_bytes, pae_hash, pae_bytes, started_at`

const postgresControlObservationColumns = `
request_hash, sequence, generation, record_hash, observation_bytes, observation_document,
status, observed_at, recorded_at, operational_authority_id, authentication_key_id,
authentication_payload_hash, authentication_payload_bytes, authentication_payload_document,
authentication_envelope_hash, authentication_envelope_bytes, authentication_envelope_document,
result_hash, result_bytes, result_document, signature_hash, signature_bytes,
claim_hash, claim_id, claim_bytes, claim_document, acknowledgement_hash,
acknowledgement_id, acknowledgement_bytes, acknowledgement_document`

const postgresControlCompletionColumns = `
receipt_id, plan_authority_id, orchestration_id, snapshot_operation_id,
receipt_sign_operation_id, snapshot_request_hash, snapshot_observation_hash,
verification_request_hash, verification_observation_hash, runner_request_hash,
runner_observation_hash, approver_request_hash, approver_observation_hash,
plan_authority_hash, evidence_closure_digest, artifact_index_digest,
snapshot_id, snapshot_digest, runner_identity, runner_key_id,
runner_signature_hash, runner_signature_bytes, approver_identity, approver_key_id,
approver_signature_hash, approver_signature_bytes, project_id, workflow_run_id,
node_key, target_revision_id, target_revision_content_hash, subject, stage_gate,
payload_hash, payload_bytes, payload_document, pae_hash, pae_bytes,
envelope_hash, envelope_bytes, envelope_document, completion_hash,
completion_bytes, completion_document, completed_at`

const postgresControlRequestSelect = `SELECT ` + postgresControlRequestColumns + `
FROM qualification_receipt_v3_requests`

const postgresControlObservationSelect = `SELECT ` + postgresControlObservationColumns + `
FROM qualification_receipt_v3_observations`

const postgresControlCompletionSelect = `SELECT ` + postgresControlCompletionColumns + `
FROM qualification_receipt_v3_receipts`

const postgresControlStartQuery = `
WITH returned AS (
  SELECT (result.request_record).*, result.created
  FROM start_qualification_receipt_v3_requests(
    $1,$2,$3::jsonb,$4,$5,$6::jsonb,$7,$8,$9,$10
  ) AS result
)
SELECT ` + postgresControlRequestColumns + `, created
FROM returned
ORDER BY signer_role COLLATE "C"`

const postgresControlAppendQuery = `
WITH returned AS (
  SELECT (result.observation_record).*, result.idempotent
  FROM append_qualification_receipt_v3_observation(
    $1,$2,$3,$4::jsonb,$5,$6,$7::jsonb,$8,$9,$10::jsonb,
    $11,$12,$13,$14,$15::jsonb,$16,$17,$18::jsonb
  ) AS result
)
SELECT ` + postgresControlObservationColumns + `, idempotent
FROM returned`

const postgresControlCompleteQuery = `
WITH returned AS (
  SELECT (result.receipt_record).*, result.idempotent
  FROM complete_qualification_receipt_v3(
    $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14,$15,$16,$17::jsonb,$18
  ) AS result
)
SELECT ` + postgresControlCompletionColumns + `, idempotent
FROM returned`

func (store *PostgresStore) StartBatch(ctx context.Context, candidates []RequestRecord) (StoreStartOutcome, error) {
	if err := store.validateWriteContext(ctx); err != nil {
		return StoreStartOutcome{}, err
	}
	if len(candidates) == 0 {
		return StoreStartOutcome{}, ErrControlInvalid
	}
	lookup := ControlLookup{
		AuthorityID: candidates[0].Key.AuthorityID,
		OperationID: candidates[0].Key.OperationID,
		Kind:        candidates[0].Key.Kind,
	}
	if err := validateAttemptRecords(candidates, lookup, false); err != nil {
		return StoreStartOutcome{}, err
	}
	ordered := cloneRequestRecords(candidates)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key.Role < ordered[j].Key.Role })

	var secondaryHash any
	var secondaryBytes any
	var secondaryDocument any
	var payloadHash any
	var payloadBytes any
	var paeHash any
	var paeBytes any
	if len(ordered) == 2 {
		secondaryHash = ordered[1].RequestHash
		secondaryBytes = ordered[1].RequestBytes
		secondaryDocument = string(ordered[1].RequestBytes)
		payloadHash = ordered[0].PayloadHash
		payloadBytes = ordered[0].Payload
		paeHash = ordered[0].PAEHash
		paeBytes = ordered[0].PAE
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return StoreStartOutcome{}, fmt.Errorf("begin PostgreSQL Receipt v3 request start: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	rows, err := transaction.QueryContext(ctx, postgresControlStartQuery,
		ordered[0].RequestHash, ordered[0].RequestBytes, string(ordered[0].RequestBytes),
		secondaryHash, secondaryBytes, secondaryDocument,
		payloadHash, payloadBytes, paeHash, paeBytes,
	)
	if err != nil {
		return StoreStartOutcome{}, classifyPostgresControlWriteError("start requests", err)
	}
	defer rows.Close()
	stored := make([]RequestRecord, 0, len(ordered))
	created := false
	for rows.Next() {
		rowCreated := false
		record, scanErr := scanPostgresControlRequest(rows, &rowCreated)
		if scanErr != nil {
			if errors.Is(scanErr, ErrControlInvalid) || errors.Is(scanErr, ErrControlConflict) || errors.Is(scanErr, ErrControlNotFound) {
				return StoreStartOutcome{}, scanErr
			}
			return StoreStartOutcome{}, classifyPostgresControlWriteError("scan started requests", scanErr)
		}
		if len(stored) == 0 {
			created = rowCreated
		} else if created != rowCreated {
			return StoreStartOutcome{}, fmt.Errorf("%w: PostgreSQL request batch returned mixed ownership flags", ErrControlConflict)
		}
		record.Idempotent = !rowCreated
		stored = append(stored, record)
	}
	if err := rows.Err(); err != nil {
		return StoreStartOutcome{}, classifyPostgresControlWriteError("read started requests", err)
	}
	if err := rows.Close(); err != nil {
		return StoreStartOutcome{}, classifyPostgresControlWriteError("close started requests", err)
	}
	if len(stored) != len(ordered) || validateAttemptRecords(stored, lookup, true) != nil ||
		!sameRequestBatch(stored, ordered, false) {
		return StoreStartOutcome{}, fmt.Errorf("%w: PostgreSQL start returned a different or incomplete request batch", ErrControlConflict)
	}
	if err := store.commit(transaction); err != nil {
		return StoreStartOutcome{}, errors.Join(
			ErrControlStoreOutcomeUnknown,
			fmt.Errorf("commit PostgreSQL Receipt v3 request start: %w", err),
		)
	}
	committed = true
	return StoreStartOutcome{Requests: cloneRequestRecords(stored), Created: created}, nil
}

func (store *PostgresStore) InspectAttempt(ctx context.Context, lookup ControlLookup) ([]RequestRecord, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || validateControlLookup(lookup) != nil {
		return nil, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := store.database.QueryContext(ctx, postgresControlRequestSelect+`
WHERE plan_authority_id=$1 AND operation_id=$2 AND request_kind=$3
ORDER BY signer_role COLLATE "C"`, lookup.AuthorityID, lookup.OperationID, lookup.Kind)
	if err != nil {
		return nil, fmt.Errorf("inspect PostgreSQL Receipt v3 attempt: %w", err)
	}
	defer rows.Close()
	records := make([]RequestRecord, 0, len(expectedControlRoles(lookup.Kind)))
	for rows.Next() {
		record, scanErr := scanPostgresControlRequest(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read PostgreSQL Receipt v3 attempt: %w", err)
	}
	if len(records) == 0 {
		return nil, ErrControlNotFound
	}
	if err := validateAttemptRecords(records, lookup, true); err != nil {
		return nil, err
	}
	return cloneRequestRecords(records), nil
}

func (store *PostgresStore) InspectRequest(ctx context.Context, key RequestKey) (RequestRecord, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || validateRequestKey(key) != nil {
		return RequestRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return RequestRecord{}, err
	}
	return inspectPostgresControlRequest(ctx, store.database, key)
}

func (store *PostgresStore) AppendObservation(ctx context.Context, candidate ObservationRecord) (ObservationRecord, error) {
	if err := store.validateWriteContext(ctx); err != nil {
		return ObservationRecord{}, err
	}
	request, err := inspectPostgresControlRequest(ctx, store.database, candidate.RequestKey)
	if err != nil {
		return ObservationRecord{}, err
	}
	if request.RequestHash != candidate.RequestHash {
		return ObservationRecord{}, ErrControlNotFound
	}
	if err := validateObservationRecord(candidate, request, false); err != nil {
		return ObservationRecord{}, err
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ObservationRecord{}, fmt.Errorf("begin PostgreSQL Receipt v3 observation append: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var idempotent bool
	stored, err := scanPostgresControlObservation(transaction.QueryRowContext(ctx, postgresControlAppendQuery,
		candidate.RequestHash,
		candidate.AuthenticationPayloadHash, candidate.AuthenticationPayloadBytes, string(candidate.AuthenticationPayloadBytes),
		candidate.AuthenticationEnvelopeHash, candidate.AuthenticationBytes, string(candidate.AuthenticationBytes),
		nullableControlString(candidate.ResultHash), nullableControlBytes(candidate.ResultBytes), nullableControlJSON(candidate.ResultBytes),
		nullableControlString(candidate.SignatureHash), nullableControlBytes(candidate.Signature),
		nullableControlString(candidate.ClaimTokenHash), nullableControlBytes(candidate.ClaimBytes), nullableControlJSON(candidate.ClaimBytes),
		nullableControlString(candidate.AckTokenHash), nullableControlBytes(candidate.AckBytes), nullableControlJSON(candidate.AckBytes),
	), request, &idempotent)
	if err != nil {
		if errors.Is(err, ErrControlInvalid) || errors.Is(err, ErrControlConflict) || errors.Is(err, ErrControlNotFound) {
			return ObservationRecord{}, err
		}
		return ObservationRecord{}, classifyPostgresControlWriteError("append observation", err)
	}
	stored.Idempotent = idempotent
	if !sameObservation(stored, candidate, false) || validateObservationRecord(stored, request, true) != nil {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL append returned different observation bytes", ErrControlConflict)
	}
	if err := store.commit(transaction); err != nil {
		return ObservationRecord{}, errors.Join(
			ErrControlStoreOutcomeUnknown,
			fmt.Errorf("commit PostgreSQL Receipt v3 observation append: %w", err),
		)
	}
	committed = true
	return cloneObservation(stored), nil
}

func (store *PostgresStore) InspectObservation(ctx context.Context, requestHash string, sequence uint64) (ObservationRecord, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || !validDigest(requestHash) || sequence == 0 || sequence > uint64(maximumSafeInteger) {
		return ObservationRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return ObservationRecord{}, err
	}
	request, err := inspectPostgresControlRequestByHash(ctx, store.database, requestHash)
	if err != nil {
		return ObservationRecord{}, err
	}
	return scanPostgresControlObservation(store.database.QueryRowContext(ctx,
		postgresControlObservationSelect+` WHERE request_hash=$1 AND sequence=$2`, requestHash, sequence,
	), request)
}

func (store *PostgresStore) InspectTerminalObservation(ctx context.Context, requestHash string) (ObservationRecord, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || !validDigest(requestHash) {
		return ObservationRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return ObservationRecord{}, err
	}
	request, err := inspectPostgresControlRequestByHash(ctx, store.database, requestHash)
	if err != nil {
		return ObservationRecord{}, err
	}
	record, err := scanPostgresControlObservation(store.database.QueryRowContext(ctx,
		postgresControlObservationSelect+`
WHERE request_hash=$1
ORDER BY sequence DESC LIMIT 1`, requestHash,
	), request)
	if err != nil {
		return ObservationRecord{}, err
	}
	if record.Status == ObservationPending {
		return ObservationRecord{}, ErrControlNotFound
	}
	return record, nil
}

func (store *PostgresStore) Complete(ctx context.Context, candidate CompletionRecord) (CompletionRecord, error) {
	if err := store.validateWriteContext(ctx); err != nil {
		return CompletionRecord{}, err
	}
	if err := validateCompletionCandidate(candidate); err != nil {
		return CompletionRecord{}, err
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return CompletionRecord{}, fmt.Errorf("begin PostgreSQL Receipt v3 completion: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var idempotent bool
	stored, err := scanPostgresControlCompletion(transaction.QueryRowContext(ctx, postgresControlCompleteQuery,
		candidate.AuthorityID,
		candidate.RequestHashes.SnapshotSeal, candidate.ObservationHashes.SnapshotSeal,
		candidate.RequestHashes.SnapshotVerify, candidate.ObservationHashes.SnapshotVerify,
		candidate.RequestHashes.RunnerSign, candidate.ObservationHashes.RunnerSign,
		candidate.RequestHashes.ApproverSign, candidate.ObservationHashes.ApproverSign,
		candidate.PayloadDigest, candidate.Payload, string(candidate.Payload), candidate.PAEDigest, candidate.PAE,
		candidate.EnvelopeDigest, candidate.Envelope, string(candidate.Envelope), candidate.verificationEnvelopeHash,
	), &idempotent)
	if err != nil {
		if errors.Is(err, ErrControlInvalid) || errors.Is(err, ErrControlConflict) || errors.Is(err, ErrControlNotFound) {
			return CompletionRecord{}, err
		}
		return CompletionRecord{}, classifyPostgresControlWriteError("complete Receipt", err)
	}
	stored.Idempotent = idempotent
	if !sameCompletion(stored, candidate, false) || validateStoredCompletion(stored) != nil {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion returned different immutable bytes", ErrControlConflict)
	}
	if err := store.commit(transaction); err != nil {
		return CompletionRecord{}, errors.Join(
			ErrControlStoreOutcomeUnknown,
			fmt.Errorf("commit PostgreSQL Receipt v3 completion: %w", err),
		)
	}
	committed = true
	return cloneCompletion(stored), nil
}

func (store *PostgresStore) InspectCompletion(ctx context.Context, authorityID uuid.UUID) (CompletionRecord, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || authorityID.Version() != 4 {
		return CompletionRecord{}, ErrControlNotFound
	}
	if err := ctx.Err(); err != nil {
		return CompletionRecord{}, err
	}
	return scanPostgresControlCompletion(store.database.QueryRowContext(ctx,
		postgresControlCompletionSelect+` WHERE plan_authority_id=$1`, authorityID,
	))
}

func (resolver *PostgresExpectedResolver) ResolveExpected(ctx context.Context, authorityID, receiptID string) (ExpectedResolution, error) {
	if resolver == nil || resolver.database == nil || isNilInterface(ctx) {
		return ExpectedResolution{}, fmt.Errorf("%w: PostgreSQL expected resolver or context is incomplete", ErrControlInvalid)
	}
	if err := ctx.Err(); err != nil {
		return ExpectedResolution{}, err
	}
	if !validUUIDv4(authorityID) || !validStableID(receiptID) {
		return ExpectedResolution{}, ErrControlInvalid
	}
	rows, err := resolver.database.QueryContext(ctx, postgresControlRequestSelect+`
WHERE plan_authority_id=$1 AND receipt_id=$2 AND request_kind='receipt-sign'
ORDER BY signer_role COLLATE "C"`, authorityID, receiptID)
	if err != nil {
		return ExpectedResolution{}, fmt.Errorf("resolve PostgreSQL expected Receipt requests: %w", err)
	}
	defer rows.Close()
	records := make([]RequestRecord, 0, 2)
	for rows.Next() {
		record, scanErr := scanPostgresControlRequest(rows)
		if scanErr != nil {
			return ExpectedResolution{}, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return ExpectedResolution{}, fmt.Errorf("read PostgreSQL expected Receipt requests: %w", err)
	}
	if len(records) == 0 {
		return ExpectedResolution{}, ErrControlNotFound
	}
	if len(records) != 2 {
		return ExpectedResolution{}, fmt.Errorf("%w: PostgreSQL expected Receipt requires two frozen signer rows", ErrControlConflict)
	}
	lookup := ControlLookup{AuthorityID: records[0].Key.AuthorityID, OperationID: records[0].Key.OperationID, Kind: RequestKindReceiptSign}
	if err := validateAttemptRecords(records, lookup, true); err != nil || !samePayloadClosure(records) || !sameAnchorClosure(records) {
		return ExpectedResolution{}, fmt.Errorf("%w: PostgreSQL expected signer request closure is invalid", ErrControlConflict)
	}
	receipt, err := DecodePayload(records[0].Payload)
	if err != nil || receipt.PlanAuthority.AuthorityID != authorityID || receipt.ReceiptID != receiptID ||
		receipt.OperationID != records[0].Key.OperationID.String() || !signingRequestsMatchExpectedReceipt(records, receipt) {
		return ExpectedResolution{}, fmt.Errorf("%w: PostgreSQL expected payload drifts from frozen signer requests", ErrControlConflict)
	}
	return ExpectedResolution{
		AuthorityID: authorityID, ReceiptID: receiptID,
		Payload: bytes.Clone(records[0].Payload), PayloadDigest: records[0].PayloadHash,
	}, nil
}

func (store *PostgresStore) validateWriteContext(ctx context.Context) error {
	if store == nil || store.database == nil || store.commit == nil || isNilInterface(ctx) {
		return fmt.Errorf("%w: PostgreSQL Receipt v3 Store or context is incomplete", ErrControlInvalid)
	}
	return ctx.Err()
}

func signingRequestsMatchExpectedReceipt(records []RequestRecord, receipt Receipt) bool {
	if len(records) != 2 {
		return false
	}
	seen := make(map[ControlRole]bool, 2)
	for _, record := range records {
		request := record.Request
		if request.PlanAuthorityID != receipt.PlanAuthority.AuthorityID || request.PlanAuthorityHash != receipt.PlanAuthority.AuthorityHash ||
			request.InputHash != receipt.PlanAuthority.InputHash || request.ProjectionHash != receipt.PlanAuthority.ProjectionHash ||
			request.EvidencePlanHash != receipt.PlanAuthority.EvidencePlanHash || request.TargetHash != receipt.PlanAuthority.TargetHash ||
			request.TrustHash != receipt.PlanAuthority.TrustHash || request.TrustBindingsDigest != receipt.PlanAuthority.TrustBindingsDigest ||
			request.TrustPolicyDigest != receipt.Trust.TrustPolicyDigest || request.EvidenceClosureDigest != receipt.Evidence.ClosureDigest ||
			request.ArtifactIndexDigest != receipt.ArtifactIndex.ContentDigest || request.SnapshotID != receipt.Snapshot.SnapshotID ||
			request.SnapshotDigest != receipt.Snapshot.SnapshotDigest || request.ReceiptID != receipt.ReceiptID {
			return false
		}
		var binding SignerIdentityBinding
		switch record.Key.Role {
		case ControlRoleRunner:
			binding = receipt.Signers.Runner
		case ControlRoleReleaseApprover:
			binding = receipt.Signers.Approver
		default:
			return false
		}
		if seen[record.Key.Role] || binding.Role != string(record.Key.Role) || binding.Identity != request.SignerIdentity ||
			binding.KeyID != request.SignerKeyID || request.AuthenticationKeyID != request.SignerKeyID {
			return false
		}
		seen[record.Key.Role] = true
	}
	return seen[ControlRoleRunner] && seen[ControlRoleReleaseApprover]
}

func nullableControlString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableControlBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullableControlJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

type postgresControlScanner interface {
	Scan(...any) error
}

type postgresControlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func inspectPostgresControlRequest(ctx context.Context, queryer postgresControlQueryer, key RequestKey) (RequestRecord, error) {
	return scanPostgresControlRequest(queryer.QueryRowContext(ctx, postgresControlRequestSelect+`
WHERE plan_authority_id=$1 AND operation_id=$2 AND request_kind=$3 AND signer_role=$4`,
		key.AuthorityID, key.OperationID, key.Kind, key.Role,
	))
}

func inspectPostgresControlRequestByHash(ctx context.Context, queryer postgresControlQueryer, requestHash string) (RequestRecord, error) {
	return scanPostgresControlRequest(queryer.QueryRowContext(ctx,
		postgresControlRequestSelect+` WHERE request_hash=$1`, requestHash,
	))
}

func scanPostgresControlRequest(row postgresControlScanner, trailing ...any) (RequestRecord, error) {
	var (
		requestHash, planAuthorityID, orchestrationID, operationID                 string
		requestKind, signerRole, operationalAuthorityID, authenticationKeyID       string
		signerIdentity, signerKeyID, snapshotID, snapshotDigest, receiptID         string
		planAuthorityHash, inputHash, projectionHash, evidencePlanHash, targetHash string
		trustHash, trustBindingsDigest, trustPolicyDigest                          string
		evidenceLastEventID, evidenceLastEventHash, evidenceCommandDigest          string
		evidenceTrustDigest, evidenceClosureDigest, artifactIndexDigest            string
		payloadHash, paeHash                                                       string
		requestBytes, requestDocument, payloadBytes, paeBytes                      []byte
		evidenceHeadVersion                                                        int64
		startedAt                                                                  time.Time
	)
	destinations := []any{
		&requestHash, &requestBytes, &requestDocument, &planAuthorityID, &orchestrationID,
		&operationID, &requestKind, &signerRole, &operationalAuthorityID, &authenticationKeyID,
		&signerIdentity, &signerKeyID, &snapshotID, &snapshotDigest, &receiptID,
		&planAuthorityHash, &inputHash, &projectionHash, &evidencePlanHash, &targetHash,
		&trustHash, &trustBindingsDigest, &trustPolicyDigest, &evidenceHeadVersion,
		&evidenceLastEventID, &evidenceLastEventHash, &evidenceCommandDigest,
		&evidenceTrustDigest, &evidenceClosureDigest, &artifactIndexDigest,
		&payloadHash, &payloadBytes, &paeHash, &paeBytes, &startedAt,
	}
	destinations = append(destinations, trailing...)
	if err := row.Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RequestRecord{}, ErrControlNotFound
		}
		return RequestRecord{}, fmt.Errorf("scan PostgreSQL Receipt v3 request: %w", err)
	}
	if !postgresControlByteSize(requestBytes, 1, maximumPostgresControlRequestBytes) ||
		!postgresControlOptionalByteSize(payloadBytes, maximumPostgresControlPayloadBytes) ||
		!postgresControlOptionalByteSize(paeBytes, maximumPostgresControlPAEBytes) {
		return RequestRecord{}, fmt.Errorf("%w: PostgreSQL request material exceeds its durable bound", ErrControlConflict)
	}

	var request, projected ControlRequest
	if err := decodeCanonicalPostgresControlJSON(requestBytes, &request, "request raw bytes"); err != nil {
		return RequestRecord{}, err
	}
	if err := decodePostgresControlJSONProjection(requestDocument, &projected, "request JSONB projection"); err != nil || projected != request {
		return RequestRecord{}, fmt.Errorf("%w: PostgreSQL request JSONB projection differs from raw bytes", ErrControlConflict)
	}
	canonicalDocument, err := CanonicalJSON(projected)
	if err != nil || !bytes.Equal(canonicalDocument, requestBytes) {
		return RequestRecord{}, fmt.Errorf("%w: PostgreSQL request JSONB does not canonicalize to raw bytes", ErrControlConflict)
	}
	authorityUUID, authorityErr := parseExactControlUUID(planAuthorityID)
	operationUUID, operationErr := parseExactControlUUID(operationID)
	if authorityErr != nil || operationErr != nil || evidenceHeadVersion < 1 || evidenceHeadVersion > maximumSafeInteger {
		return RequestRecord{}, fmt.Errorf("%w: PostgreSQL request identity or version projection is invalid", ErrControlConflict)
	}
	record := RequestRecord{
		Key: RequestKey{
			AuthorityID: authorityUUID, OperationID: operationUUID,
			Kind: RequestKind(requestKind), Role: ControlRole(signerRole),
		},
		Request: request, RequestBytes: bytes.Clone(requestBytes), RequestHash: requestHash,
		Payload: bytes.Clone(payloadBytes), PayloadHash: payloadHash,
		PAE: bytes.Clone(paeBytes), PAEHash: paeHash,
		StartedAt: startedAt.UTC(),
	}
	if err := validateRequestRecord(record, true); err != nil {
		return RequestRecord{}, fmt.Errorf("%w: stored PostgreSQL request closure is invalid: %v", ErrControlConflict, err)
	}
	if requestHash != SHA256Digest(requestBytes) ||
		request.PlanAuthorityID != planAuthorityID || request.OrchestrationID != orchestrationID || request.OperationID != operationID ||
		request.Kind != RequestKind(requestKind) || request.Role != ControlRole(signerRole) ||
		request.OperationalAuthorityID != operationalAuthorityID || request.AuthenticationKeyID != authenticationKeyID ||
		request.SignerIdentity != signerIdentity || request.SignerKeyID != signerKeyID || request.SnapshotID != snapshotID ||
		request.SnapshotDigest != snapshotDigest || request.ReceiptID != receiptID || request.PlanAuthorityHash != planAuthorityHash ||
		request.InputHash != inputHash || request.ProjectionHash != projectionHash || request.EvidencePlanHash != evidencePlanHash ||
		request.TargetHash != targetHash || request.TrustHash != trustHash || request.TrustBindingsDigest != trustBindingsDigest ||
		request.TrustPolicyDigest != trustPolicyDigest || request.EvidenceHeadVersion != uint64(evidenceHeadVersion) ||
		request.EvidenceLastEventID != evidenceLastEventID || request.EvidenceLastEventHash != evidenceLastEventHash ||
		request.EvidenceCommandDigest != evidenceCommandDigest || request.EvidenceTrustDigest != evidenceTrustDigest ||
		request.EvidenceClosureDigest != evidenceClosureDigest || request.ArtifactIndexDigest != artifactIndexDigest ||
		request.PayloadDigest != payloadHash || request.PAEDigest != paeHash {
		return RequestRecord{}, fmt.Errorf("%w: PostgreSQL request scalar/hash projections drifted from canonical bytes", ErrControlConflict)
	}
	return record, nil
}

func decodeCanonicalPostgresControlJSON(raw []byte, target any, label string) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: PostgreSQL %s is empty", ErrControlConflict, label)
	}
	if err := decodeStrictJSON(raw, target); err != nil {
		return fmt.Errorf("%w: decode PostgreSQL %s: %v", ErrControlConflict, label, err)
	}
	canonical, err := CanonicalJSON(target)
	if err != nil || !bytes.Equal(canonical, raw) {
		return fmt.Errorf("%w: PostgreSQL %s is not exact canonical JSON", ErrControlConflict, label)
	}
	return nil
}

func decodePostgresControlJSONProjection(raw []byte, target any, label string) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: PostgreSQL %s is empty", ErrControlConflict, label)
	}
	if err := decodeStrictJSON(raw, target); err != nil {
		return fmt.Errorf("%w: decode PostgreSQL %s: %v", ErrControlConflict, label, err)
	}
	return nil
}

func canonicalizePostgresControlJSON(raw []byte, label string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: decode PostgreSQL %s: %v", ErrControlConflict, label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: PostgreSQL %s has trailing JSON", ErrControlConflict, label)
	}
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize PostgreSQL %s: %v", ErrControlConflict, label, err)
	}
	return canonical, nil
}

func requirePostgresControlJSONProjection(raw, document []byte, label string) error {
	canonical, err := canonicalizePostgresControlJSON(document, label+" JSONB projection")
	if err != nil || !bytes.Equal(canonical, raw) {
		return fmt.Errorf("%w: PostgreSQL %s JSONB projection differs from canonical raw bytes", ErrControlConflict, label)
	}
	return nil
}

func parseExactControlUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 4 || parsed.String() != value {
		return uuid.Nil, ErrControlConflict
	}
	return parsed, nil
}

func scanPostgresControlObservation(row postgresControlScanner, request RequestRecord, trailing ...any) (ObservationRecord, error) {
	var (
		requestHash, recordHash, status, operationalAuthorityID, authenticationKeyID string
		observationBytes, observationDocument                                        []byte
		authenticationPayloadHash                                                    string
		authenticationPayloadBytes, authenticationPayloadDocument                    []byte
		authenticationEnvelopeHash                                                   string
		authenticationEnvelopeBytes, authenticationEnvelopeDocument                  []byte
		resultHash, signatureHash, claimHash, claimID                                sql.NullString
		acknowledgementHash, acknowledgementID                                       sql.NullString
		resultBytes, resultDocument, signatureBytes                                  []byte
		claimBytes, claimDocument, acknowledgementBytes, acknowledgementDocument     []byte
		sequence, generation                                                         int64
		observedAt, recordedAt                                                       time.Time
	)
	destinations := []any{
		&requestHash, &sequence, &generation, &recordHash, &observationBytes, &observationDocument,
		&status, &observedAt, &recordedAt, &operationalAuthorityID, &authenticationKeyID,
		&authenticationPayloadHash, &authenticationPayloadBytes, &authenticationPayloadDocument,
		&authenticationEnvelopeHash, &authenticationEnvelopeBytes, &authenticationEnvelopeDocument,
		&resultHash, &resultBytes, &resultDocument, &signatureHash, &signatureBytes,
		&claimHash, &claimID, &claimBytes, &claimDocument, &acknowledgementHash,
		&acknowledgementID, &acknowledgementBytes, &acknowledgementDocument,
	}
	destinations = append(destinations, trailing...)
	if err := row.Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ObservationRecord{}, ErrControlNotFound
		}
		return ObservationRecord{}, fmt.Errorf("scan PostgreSQL Receipt v3 observation: %w", err)
	}
	if !postgresControlByteSize(observationBytes, 1, maximumPostgresControlObservationBytes) ||
		!postgresControlByteSize(authenticationPayloadBytes, 1, maximumPostgresControlAuthenticationBytes) ||
		!postgresControlByteSize(authenticationEnvelopeBytes, 1, maximumPostgresControlAuthenticationBytes) ||
		!postgresControlOptionalByteSize(resultBytes, maximumPostgresControlResultBytes) ||
		!postgresControlOptionalByteSize(claimBytes, maximumPostgresControlTokenBytes) ||
		!postgresControlOptionalByteSize(acknowledgementBytes, maximumPostgresControlTokenBytes) ||
		(len(signatureBytes) != 0 && len(signatureBytes) != ed25519.SignatureSize) {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation material exceeds its durable bound", ErrControlConflict)
	}
	if sequence < 1 || generation < 1 || sequence > maximumSafeInteger || generation > maximumSafeInteger ||
		requestHash != request.RequestHash || operationalAuthorityID != request.Request.OperationalAuthorityID ||
		authenticationKeyID != request.Request.AuthenticationKeyID {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation scalar identity drifted", ErrControlConflict)
	}

	var authenticationPayload, projectedAuthenticationPayload ObservationAuthenticationPayload
	if err := decodeCanonicalPostgresControlJSON(authenticationPayloadBytes, &authenticationPayload, "observation authentication payload"); err != nil {
		return ObservationRecord{}, err
	}
	if err := decodePostgresControlJSONProjection(authenticationPayloadDocument, &projectedAuthenticationPayload, "observation authentication payload JSONB"); err != nil ||
		projectedAuthenticationPayload != authenticationPayload {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL authentication payload JSONB drifted", ErrControlConflict)
	}
	if canonical, err := CanonicalJSON(projectedAuthenticationPayload); err != nil || !bytes.Equal(canonical, authenticationPayloadBytes) {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL authentication payload JSONB does not close raw bytes", ErrControlConflict)
	}

	var authenticationEnvelope, projectedAuthenticationEnvelope ObservationAuthenticationEnvelope
	if err := decodeCanonicalPostgresControlJSON(authenticationEnvelopeBytes, &authenticationEnvelope, "observation authentication envelope"); err != nil {
		return ObservationRecord{}, err
	}
	if err := decodePostgresControlJSONProjection(authenticationEnvelopeDocument, &projectedAuthenticationEnvelope, "observation authentication envelope JSONB"); err != nil ||
		projectedAuthenticationEnvelope != authenticationEnvelope {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL authentication envelope JSONB drifted", ErrControlConflict)
	}
	if canonical, err := CanonicalJSON(projectedAuthenticationEnvelope); err != nil || !bytes.Equal(canonical, authenticationEnvelopeBytes) {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL authentication envelope JSONB does not close raw bytes", ErrControlConflict)
	}

	record := ObservationRecord{
		RequestKey: request.Key, RequestHash: requestHash,
		Generation: uint64(generation), Sequence: uint64(sequence), Status: ObservationStatus(status),
		ObservedAt: observedAt.UTC(), RecordedAt: recordedAt.UTC(),
		AuthenticationKeyID:   authenticationKeyID,
		AuthenticationPayload: authenticationPayload, AuthenticationPayloadBytes: bytes.Clone(authenticationPayloadBytes),
		AuthenticationPayloadHash: authenticationPayloadHash,
		AuthenticationEnvelope:    authenticationEnvelope, AuthenticationBytes: bytes.Clone(authenticationEnvelopeBytes),
		AuthenticationEnvelopeHash: authenticationEnvelopeHash,
		ResultHash:                 nullControlString(resultHash), Signature: bytes.Clone(signatureBytes),
		SignatureHash: nullControlString(signatureHash), ClaimTokenHash: nullControlString(claimHash),
		AckTokenHash: nullControlString(acknowledgementHash), RecordHash: recordHash,
	}

	if len(resultBytes) != 0 || len(resultDocument) != 0 || resultHash.Valid {
		if !resultHash.Valid || len(resultBytes) == 0 || len(resultDocument) == 0 {
			return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation result columns are not role-closed", ErrControlConflict)
		}
		switch request.Key.Kind {
		case RequestKindSnapshotSeal:
			var raw, projected PreReceiptSnapshotBinding
			if err := decodeCanonicalPostgresControlJSON(resultBytes, &raw, "snapshot-seal result"); err != nil {
				return ObservationRecord{}, err
			}
			if err := decodePostgresControlJSONProjection(resultDocument, &projected, "snapshot-seal result JSONB"); err != nil || projected != raw {
				return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL snapshot-seal result JSONB drifted", ErrControlConflict)
			}
		case RequestKindSnapshotVerify:
			var raw, projected SnapshotVerificationBinding
			if err := decodeCanonicalPostgresControlJSON(resultBytes, &raw, "snapshot-verification result"); err != nil {
				return ObservationRecord{}, err
			}
			if err := decodePostgresControlJSONProjection(resultDocument, &projected, "snapshot-verification result JSONB"); err != nil || projected != raw {
				return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL snapshot-verification result JSONB drifted", ErrControlConflict)
			}
		default:
			return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL signer observation contains result material", ErrControlConflict)
		}
		if err := requirePostgresControlJSONProjection(resultBytes, resultDocument, "observation result"); err != nil {
			return ObservationRecord{}, err
		}
		record.Result = append(json.RawMessage(nil), resultBytes...)
		record.ResultBytes = bytes.Clone(resultBytes)
	}

	if len(claimBytes) != 0 || len(claimDocument) != 0 || claimHash.Valid || claimID.Valid {
		if !claimHash.Valid || !claimID.Valid || len(claimBytes) == 0 || len(claimDocument) == 0 {
			return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation claim columns are incomplete", ErrControlConflict)
		}
		var claim, projected ClaimToken
		if err := decodeCanonicalPostgresControlJSON(claimBytes, &claim, "observation claim"); err != nil {
			return ObservationRecord{}, err
		}
		if err := decodePostgresControlJSONProjection(claimDocument, &projected, "observation claim JSONB"); err != nil || projected != claim || claim.ClaimID != claimID.String {
			return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation claim projection drifted", ErrControlConflict)
		}
		if err := requirePostgresControlJSONProjection(claimBytes, claimDocument, "observation claim"); err != nil {
			return ObservationRecord{}, err
		}
		record.Claim = claim
		record.ClaimBytes = bytes.Clone(claimBytes)
	}

	if len(acknowledgementBytes) != 0 || len(acknowledgementDocument) != 0 || acknowledgementHash.Valid || acknowledgementID.Valid {
		if !acknowledgementHash.Valid || !acknowledgementID.Valid || len(acknowledgementBytes) == 0 || len(acknowledgementDocument) == 0 {
			return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation acknowledgement columns are incomplete", ErrControlConflict)
		}
		var acknowledgement, projected AcknowledgementToken
		if err := decodeCanonicalPostgresControlJSON(acknowledgementBytes, &acknowledgement, "observation acknowledgement"); err != nil {
			return ObservationRecord{}, err
		}
		if err := decodePostgresControlJSONProjection(acknowledgementDocument, &projected, "observation acknowledgement JSONB"); err != nil ||
			projected != acknowledgement || acknowledgement.AcknowledgementID != acknowledgementID.String {
			return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation acknowledgement projection drifted", ErrControlConflict)
		}
		if err := requirePostgresControlJSONProjection(acknowledgementBytes, acknowledgementDocument, "observation acknowledgement"); err != nil {
			return ObservationRecord{}, err
		}
		record.Acknowledgement = acknowledgement
		record.AckBytes = bytes.Clone(acknowledgementBytes)
	}

	var rawProjection, documentProjection controlObservationProjection
	if err := decodeCanonicalPostgresControlJSON(observationBytes, &rawProjection, "observation record projection"); err != nil {
		return ObservationRecord{}, err
	}
	if err := decodePostgresControlJSONProjection(observationDocument, &documentProjection, "observation record JSONB"); err != nil || documentProjection != rawProjection {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation record JSONB drifted", ErrControlConflict)
	}
	wantProjection := controlObservationProjection{
		AcknowledgementTokenHash: record.AckTokenHash, AuthenticationEnvelopeHash: record.AuthenticationEnvelopeHash,
		AuthenticationKeyID: record.AuthenticationKeyID, AuthenticationPayloadHash: record.AuthenticationPayloadHash,
		ClaimTokenHash: record.ClaimTokenHash, Generation: record.Generation,
		ObservedAt: record.ObservedAt.Format(canonicalTimeLayout), RecordedAt: record.RecordedAt.Format(canonicalTimeLayout),
		RequestHash: record.RequestHash, ResultHash: record.ResultHash, Sequence: record.Sequence,
		SignatureHash: record.SignatureHash, Status: record.Status,
	}
	if rawProjection != wantProjection || recordHash != SHA256Digest(observationBytes) ||
		authenticationPayloadHash != SHA256Digest(authenticationPayloadBytes) ||
		authenticationEnvelopeHash != SHA256Digest(authenticationEnvelopeBytes) ||
		(resultHash.Valid && resultHash.String != SHA256Digest(resultBytes)) ||
		(signatureHash.Valid && signatureHash.String != SHA256Digest(signatureBytes)) ||
		(claimHash.Valid && claimHash.String != SHA256Digest(claimBytes)) ||
		(acknowledgementHash.Valid && acknowledgementHash.String != SHA256Digest(acknowledgementBytes)) {
		return ObservationRecord{}, fmt.Errorf("%w: PostgreSQL observation raw/hash/scalar closure drifted", ErrControlConflict)
	}
	if err := validateObservationRecord(record, request, true); err != nil {
		return ObservationRecord{}, fmt.Errorf("%w: stored PostgreSQL observation closure is invalid: %v", ErrControlConflict, err)
	}
	return record, nil
}

func nullControlString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

type postgresControlEnvelope struct {
	PayloadType string                     `json:"payloadType"`
	Payload     string                     `json:"payload"`
	Signatures  []postgresControlSignature `json:"signatures"`
}

type postgresControlSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

func scanPostgresControlCompletion(row postgresControlScanner, trailing ...any) (CompletionRecord, error) {
	var (
		receiptID, planAuthorityID, orchestrationID, snapshotOperationID, receiptSignOperationID string
		snapshotRequestHash, snapshotObservationHash                                             string
		verificationRequestHash, verificationObservationHash                                     string
		runnerRequestHash, runnerObservationHash, approverRequestHash, approverObservationHash   string
		planAuthorityHash, evidenceClosureDigest, artifactIndexDigest                            string
		snapshotID, snapshotDigest                                                               string
		runnerIdentity, runnerKeyID, runnerSignatureHash                                         string
		approverIdentity, approverKeyID, approverSignatureHash                                   string
		projectID, workflowRunID, nodeKey, targetRevisionID, targetRevisionContentHash           string
		subject, stageGate                                                                       string
		payloadHash, paeHash, envelopeHash, completionHash                                       string
		runnerSignatureBytes, approverSignatureBytes                                             []byte
		payloadBytes, payloadDocument, paeBytes                                                  []byte
		envelopeBytes, envelopeDocument, completionBytes, completionDocument                     []byte
		completedAt                                                                              time.Time
	)
	destinations := []any{
		&receiptID, &planAuthorityID, &orchestrationID, &snapshotOperationID,
		&receiptSignOperationID, &snapshotRequestHash, &snapshotObservationHash,
		&verificationRequestHash, &verificationObservationHash, &runnerRequestHash,
		&runnerObservationHash, &approverRequestHash, &approverObservationHash,
		&planAuthorityHash, &evidenceClosureDigest, &artifactIndexDigest,
		&snapshotID, &snapshotDigest, &runnerIdentity, &runnerKeyID,
		&runnerSignatureHash, &runnerSignatureBytes, &approverIdentity, &approverKeyID,
		&approverSignatureHash, &approverSignatureBytes, &projectID, &workflowRunID,
		&nodeKey, &targetRevisionID, &targetRevisionContentHash, &subject, &stageGate,
		&payloadHash, &payloadBytes, &payloadDocument, &paeHash, &paeBytes,
		&envelopeHash, &envelopeBytes, &envelopeDocument, &completionHash,
		&completionBytes, &completionDocument, &completedAt,
	}
	destinations = append(destinations, trailing...)
	if err := row.Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompletionRecord{}, ErrControlNotFound
		}
		return CompletionRecord{}, fmt.Errorf("scan PostgreSQL Receipt v3 completion: %w", err)
	}
	if !postgresControlByteSize(payloadBytes, 1, maximumPostgresControlPayloadBytes) ||
		!postgresControlByteSize(paeBytes, 1, maximumPostgresControlPAEBytes) ||
		!postgresControlByteSize(envelopeBytes, 1, maximumPostgresControlEnvelopeBytes) ||
		!postgresControlByteSize(completionBytes, 1, maximumPostgresControlCompletionBytes) ||
		len(runnerSignatureBytes) != ed25519.SignatureSize || len(approverSignatureBytes) != ed25519.SignatureSize {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion material exceeds its durable bound", ErrControlConflict)
	}
	authorityUUID, authorityErr := parseExactControlUUID(planAuthorityID)
	if authorityErr != nil {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion Plan Authority UUID is invalid", ErrControlConflict)
	}
	receipt, err := DecodePayload(payloadBytes)
	if err != nil {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion payload is not exact Receipt v3: %v", ErrControlConflict, err)
	}
	if err := requirePostgresControlJSONProjection(payloadBytes, payloadDocument, "completion payload"); err != nil {
		return CompletionRecord{}, err
	}
	if payloadHash != SHA256Digest(payloadBytes) || paeHash != SHA256Digest(paeBytes) ||
		!bytes.Equal(paeBytes, templateauthority.DSSEPAE(InTotoPayloadType, payloadBytes)) {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion payload/PAE hash closure drifted", ErrControlConflict)
	}

	var envelope, projectedEnvelope postgresControlEnvelope
	if err := decodeExactPostgresControlEnvelope(envelopeBytes, &envelope); err != nil {
		return CompletionRecord{}, err
	}
	if err := decodePostgresControlJSONProjection(envelopeDocument, &projectedEnvelope, "completion DSSE envelope JSONB"); err != nil ||
		!equalPostgresControlEnvelopes(projectedEnvelope, envelope) {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion envelope JSONB drifted", ErrControlConflict)
	}
	if canonical, err := json.Marshal(projectedEnvelope); err != nil || !bytes.Equal(canonical, envelopeBytes) {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion envelope JSONB does not close raw bytes", ErrControlConflict)
	}
	if envelopeHash != SHA256Digest(envelopeBytes) || envelope.PayloadType != InTotoPayloadType || len(envelope.Signatures) != 2 {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion envelope shape/hash is invalid", ErrControlConflict)
	}
	decodedPayload, err := decodeStrictControlBase64(envelope.Payload)
	if err != nil || !bytes.Equal(decodedPayload, payloadBytes) {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion envelope payload differs from raw payload", ErrControlConflict)
	}
	if envelope.Signatures[0].KeyID >= envelope.Signatures[1].KeyID {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion signatures are not in canonical key order", ErrControlConflict)
	}
	signatures := make(map[string][]byte, 2)
	for _, signature := range envelope.Signatures {
		decoded, decodeErr := decodeStrictControlBase64(signature.Sig)
		if decodeErr != nil || len(decoded) != 64 || signature.KeyID == "" {
			return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion contains invalid signature encoding", ErrControlConflict)
		}
		if _, duplicate := signatures[signature.KeyID]; duplicate {
			return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion contains duplicate signer key", ErrControlConflict)
		}
		signatures[signature.KeyID] = decoded
	}
	if !bytes.Equal(signatures[runnerKeyID], runnerSignatureBytes) || !bytes.Equal(signatures[approverKeyID], approverSignatureBytes) ||
		runnerSignatureHash != SHA256Digest(runnerSignatureBytes) || approverSignatureHash != SHA256Digest(approverSignatureBytes) {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion signer scalar/raw closure drifted", ErrControlConflict)
	}

	var document, projectedDocument CompletionDocument
	if err := decodeCanonicalPostgresControlJSON(completionBytes, &document, "completion terminal document"); err != nil {
		return CompletionRecord{}, err
	}
	if err := decodePostgresControlJSONProjection(completionDocument, &projectedDocument, "completion terminal document JSONB"); err != nil ||
		projectedDocument != document {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion document JSONB drifted", ErrControlConflict)
	}
	if canonical, err := CanonicalJSON(projectedDocument); err != nil || !bytes.Equal(canonical, completionBytes) || completionHash != SHA256Digest(completionBytes) {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion document raw/hash closure drifted", ErrControlConflict)
	}

	record := CompletionRecord{
		AuthorityID: authorityUUID, ReceiptID: receiptID,
		PlanAuthorityHash: planAuthorityHash, EvidenceClosureDigest: evidenceClosureDigest,
		SnapshotID: snapshotID, SnapshotDigest: snapshotDigest,
		RequestHashes: CompletionRequestHashes{
			SnapshotSeal: snapshotRequestHash, SnapshotVerify: verificationRequestHash,
			RunnerSign: runnerRequestHash, ApproverSign: approverRequestHash,
		},
		ObservationHashes: CompletionObservationHashes{
			SnapshotSeal: snapshotObservationHash, SnapshotVerify: verificationObservationHash,
			RunnerSign: runnerObservationHash, ApproverSign: approverObservationHash,
		},
		Operations: CompletionOperations{Snapshot: snapshotOperationID, ReceiptSign: receiptSignOperationID},
		Payload:    bytes.Clone(payloadBytes), PayloadDigest: payloadHash,
		PAE: bytes.Clone(paeBytes), PAEDigest: paeHash,
		Envelope: bytes.Clone(envelopeBytes), EnvelopeDigest: envelopeHash,
		Document: document, DocumentBytes: bytes.Clone(completionBytes), DocumentHash: completionHash,
		CompletedAt: completedAt.UTC(),
	}
	target := receipt.Target.PromotionTarget
	if receipt.ReceiptID != receiptID || receipt.PlanAuthority.AuthorityID != planAuthorityID ||
		receipt.PlanAuthority.AuthorityHash != planAuthorityHash || receipt.Evidence.OrchestrationID != orchestrationID ||
		receipt.Snapshot.OperationID != snapshotOperationID || receipt.OperationID != receiptSignOperationID ||
		receipt.Evidence.ClosureDigest != evidenceClosureDigest || receipt.ArtifactIndex.ContentDigest != artifactIndexDigest ||
		receipt.Snapshot.SnapshotID != snapshotID || receipt.Snapshot.SnapshotDigest != snapshotDigest ||
		receipt.Signers.Runner.Identity != runnerIdentity || receipt.Signers.Runner.KeyID != runnerKeyID ||
		receipt.Signers.Approver.Identity != approverIdentity || receipt.Signers.Approver.KeyID != approverKeyID ||
		target.ProjectID != projectID || target.WorkflowRunID != workflowRunID || target.NodeKey != nodeKey ||
		target.TargetRevision.ID != targetRevisionID || target.TargetRevision.ContentHash != targetRevisionContentHash ||
		target.Subject != subject || target.StageGate != stageGate {
		return CompletionRecord{}, fmt.Errorf("%w: PostgreSQL completion explicit scalar projections drifted from payload", ErrControlConflict)
	}

	// The verifier grant is intentionally absent from persistence. Validate a
	// temporary copy first; only a fully closed immutable row may rehydrate the
	// in-process grant on the value returned to ControlService/Verifier.
	validated := cloneCompletion(record)
	validated.verificationEnvelopeHash = validated.EnvelopeDigest
	if err := validateStoredCompletion(validated); err != nil {
		return CompletionRecord{}, fmt.Errorf("%w: stored PostgreSQL completion closure is invalid: %v", ErrControlConflict, err)
	}
	record.verificationEnvelopeHash = record.EnvelopeDigest
	return record, nil
}

func equalPostgresControlEnvelopes(left, right postgresControlEnvelope) bool {
	if left.PayloadType != right.PayloadType || left.Payload != right.Payload || len(left.Signatures) != len(right.Signatures) {
		return false
	}
	for index := range left.Signatures {
		if left.Signatures[index] != right.Signatures[index] {
			return false
		}
	}
	return true
}

func decodeExactPostgresControlEnvelope(raw []byte, target *postgresControlEnvelope) error {
	if err := decodeStrictJSON(raw, target); err != nil {
		return fmt.Errorf("%w: decode PostgreSQL completion DSSE envelope: %v", ErrControlConflict, err)
	}
	exact, err := json.Marshal(target)
	if err != nil || !bytes.Equal(exact, raw) {
		return fmt.Errorf("%w: PostgreSQL completion DSSE envelope is not exact canonical JSON", ErrControlConflict)
	}
	return nil
}

func decodeStrictControlBase64(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, ErrControlConflict
	}
	return decoded, nil
}

func classifyPostgresControlWriteError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrControlNotFound) {
		return errors.Join(ErrControlNotFound, fmt.Errorf("PostgreSQL Receipt v3 %s: %w", operation, err))
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		wrapped := fmt.Errorf("PostgreSQL Receipt v3 %s: %w", operation, err)
		switch postgresError.Code {
		case "WQR01":
			return errors.Join(ErrControlNotFound, wrapped)
		case "WQR02", "40001", "40P01", "23503", "23505", "23P01":
			return errors.Join(ErrControlConflict, wrapped)
		case "WQR03", "23502", "23514", "22001", "22003", "22007", "22008", "22021", "22023", "22P02":
			return errors.Join(ErrControlInvalid, wrapped)
		case "WQR04":
			return errors.Join(ErrControlNotReady, wrapped)
		default:
			return wrapped
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.Join(
		ErrControlStoreOutcomeUnknown,
		fmt.Errorf("PostgreSQL Receipt v3 %s transport outcome: %w", operation, err),
	)
}

func postgresControlByteSize(value []byte, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum
}

func postgresControlOptionalByteSize(value []byte, maximum int) bool {
	return len(value) == 0 || len(value) <= maximum
}

var _ ControlStore = (*PostgresStore)(nil)
var _ ExpectedResolver = (*PostgresExpectedResolver)(nil)
