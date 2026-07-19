package credentialset

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	postgresEventRequestSchema = "worksflow-credential-set-event-request/v1"
	maximumBindingBytes        = 128 << 10
	maximumPayloadBytes        = 256 << 10
	maximumEnvelopeBytes       = 384 << 10
	maximumEventRequestBytes   = 16 << 10
)

type postgresEventAttestationRef struct {
	EnvelopeDigest string `json:"envelopeDigest"`
	KeyID          string `json:"keyId"`
	PayloadDigest  string `json:"payloadDigest"`
}

type postgresEventBindingRef struct {
	ContentHash string `json:"contentHash"`
}

type postgresEventProjection struct {
	Attestation           *postgresEventAttestationRef `json:"attestation"`
	Binding               *postgresEventBindingRef     `json:"binding"`
	EventID               string                       `json:"eventId"`
	ExpiresAt             string                       `json:"expiresAt"`
	IssueCommandHash      string                       `json:"issueCommandHash"`
	IssuedAt              string                       `json:"issuedAt"`
	Kind                  EventKind                    `json:"kind"`
	OperationID           string                       `json:"operationId"`
	RequestedAt           string                       `json:"requestedAt"`
	RevocationCommandHash string                       `json:"revocationCommandHash"`
	RevokedAt             string                       `json:"revokedAt"`
}

type postgresEventRequest struct {
	Event           postgresEventProjection `json:"event"`
	ExpectedVersion uint64                  `json:"expectedVersion"`
	SchemaVersion   string                  `json:"schemaVersion"`
	SetID           string                  `json:"setId"`
}

type postgresEventMaterials struct {
	requestHash      string
	requestBytes     []byte
	requestDocument  string
	bindingHash      any
	bindingBytes     any
	bindingDocument  any
	envelopeHash     any
	envelopeBytes    any
	envelopeDocument any
	payloadHash      any
	payloadBytes     any
	payloadDocument  any
}

// PostgresStore is the durable, non-secret CredentialSet event ledger created
// by migration 000072. The authority is currently migration-owner-only and is
// not wired as a production runtime. A dedicated reviewed CredentialSet
// operator identity and DSN still must be provisioned; neither the ordinary
// application role nor a migration-owner DSN is a runtime substitute.
type PostgresStore struct {
	database *sql.DB
}

func NewPostgresStore(database *sql.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL credential-set database is required", ErrInvalid)
	}
	return &PostgresStore{database: database}, nil
}

func (store *PostgresStore) TrustedTime(ctx context.Context) (time.Time, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL store or context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	var observed sql.NullTime
	if err := store.database.QueryRowContext(ctx, `
SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&observed); err != nil {
		return time.Time{}, fmt.Errorf("query PostgreSQL credential-set trusted time: %w", err)
	}
	if !observed.Valid {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL trusted time is NULL", ErrInvalid)
	}
	now := observed.Time.UTC()
	if now.IsZero() || !now.Equal(now.Truncate(time.Millisecond)) {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL trusted time is non-canonical", ErrInvalid)
	}
	return now, nil
}

func (store *PostgresStore) CreateIssue(ctx context.Context, setID string, event Event) (Snapshot, bool, error) {
	if event.Kind != EventIssueReserved {
		return Snapshot{}, false, fmt.Errorf("%w: issue reservation event is required", ErrInvalid)
	}
	snapshot, created, err := store.append(ctx, setID, 0, event)
	return snapshot, created, err
}

func (store *PostgresStore) Append(ctx context.Context, setID string, expectedVersion uint64, event Event) (Snapshot, error) {
	snapshot, _, err := store.append(ctx, setID, expectedVersion, event)
	return snapshot, err
}

func (store *PostgresStore) append(ctx context.Context, setID string, expectedVersion uint64, event Event) (Snapshot, bool, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return Snapshot{}, false, fmt.Errorf("%w: PostgreSQL store or context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	if !validUUIDv4(setID) || expectedVersion > uint64(math.MaxInt64) {
		return Snapshot{}, false, fmt.Errorf("%w: PostgreSQL event identity or version is invalid", ErrInvalid)
	}
	if !validUUIDv4(event.EventID) {
		return Snapshot{}, false, fmt.Errorf("%w: PostgreSQL event ID is invalid", ErrInvalid)
	}
	if exact, inspectErr := store.inspectExistingEvent(ctx, setID, expectedVersion, event); inspectErr != nil || exact {
		if inspectErr != nil {
			return Snapshot{}, false, inspectErr
		}
		snapshot, loadErr := store.Load(ctx, setID)
		return snapshot, false, loadErr
	}
	materials, err := buildPostgresEventMaterials(setID, expectedVersion, event)
	if err != nil {
		return Snapshot{}, false, err
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("begin PostgreSQL credential-set append: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var created bool
	if err := transaction.QueryRowContext(ctx, `
SELECT append_credential_set_event(
  $1, $2, $3::jsonb, $4, $5, $6::jsonb,
  $7, $8, $9::jsonb, $10, $11, $12::jsonb
)`,
		materials.requestHash, materials.requestBytes, materials.requestDocument,
		materials.bindingHash, materials.bindingBytes, materials.bindingDocument,
		materials.envelopeHash, materials.envelopeBytes, materials.envelopeDocument,
		materials.payloadHash, materials.payloadBytes, materials.payloadDocument,
	).Scan(&created); err != nil {
		return Snapshot{}, false, classifyPostgresCredentialSetError(err)
	}
	snapshot, err := loadPostgresSnapshot(ctx, transaction, setID)
	if err != nil {
		return Snapshot{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return Snapshot{}, false, errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("commit PostgreSQL credential-set append: %w", err))
	}
	committed = true
	return snapshot, created, nil
}

// inspectExistingEvent closes the EventID replay before rebuilding a request.
// The immutable ledger stores both requested_at and DB-authoritative event_at;
// Events exposes the latter, so an exact replay may legitimately carry either
// time while every other request and structured byte remains identical.
func (store *PostgresStore) inspectExistingEvent(ctx context.Context, setID string, expectedVersion uint64, event Event) (bool, error) {
	var storedSetID string
	var storedExpected int64
	var requestedAt, eventAt time.Time
	var requestHash string
	var requestBytes []byte
	var bindingHash, envelopeHash, payloadHash sql.NullString
	var bindingBytes, envelopeBytes, payloadBytes []byte
	err := store.database.QueryRowContext(ctx, `
SELECT set_id, (request_document->>'expectedVersion')::bigint, requested_at, event_at,
       request_hash, request_bytes, binding_hash, binding_bytes,
       envelope_digest, envelope_bytes, payload_digest, payload_bytes
FROM credential_set_events
WHERE event_id = $1`, event.EventID).Scan(
		&storedSetID, &storedExpected, &requestedAt, &eventAt,
		&requestHash, &requestBytes, &bindingHash, &bindingBytes,
		&envelopeHash, &envelopeBytes, &payloadHash, &payloadBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect PostgreSQL credential-set event ID: %w", err)
	}
	if storedSetID != setID || storedExpected < 0 || uint64(storedExpected) != expectedVersion ||
		(!event.At.Equal(requestedAt.UTC()) && !event.At.Equal(eventAt.UTC())) {
		return false, ErrIdempotencyConflict
	}
	candidate := cloneEvent(event)
	candidate.At = requestedAt.UTC()
	materials, materialErr := buildPostgresEventMaterials(setID, expectedVersion, candidate)
	if materialErr != nil || materials.requestHash != requestHash || !bytes.Equal(materials.requestBytes, requestBytes) ||
		!nullableMaterialEqual(materials.bindingHash, materials.bindingBytes, bindingHash, bindingBytes) ||
		!nullableMaterialEqual(materials.envelopeHash, materials.envelopeBytes, envelopeHash, envelopeBytes) ||
		!nullableMaterialEqual(materials.payloadHash, materials.payloadBytes, payloadHash, payloadBytes) {
		return false, ErrIdempotencyConflict
	}
	return true, nil
}

func nullableMaterialEqual(hashValue, bytesValue any, storedHash sql.NullString, storedBytes []byte) bool {
	if hashValue == nil || bytesValue == nil {
		return !storedHash.Valid && len(storedBytes) == 0
	}
	hash, hashOK := hashValue.(string)
	encoded, bytesOK := bytesValue.([]byte)
	return hashOK && bytesOK && storedHash.Valid && hash == storedHash.String && bytes.Equal(encoded, storedBytes)
}

func (store *PostgresStore) Load(ctx context.Context, setID string) (Snapshot, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL store or context is nil", ErrInvalid)
	}
	if !validUUIDv4(setID) {
		return Snapshot{}, fmt.Errorf("%w: set id is invalid", ErrInvalid)
	}
	return loadPostgresSnapshot(ctx, store.database, setID)
}

func (store *PostgresStore) Events(ctx context.Context, setID string) ([]Event, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return nil, fmt.Errorf("%w: PostgreSQL store or context is nil", ErrInvalid)
	}
	if !validUUIDv4(setID) {
		return nil, fmt.Errorf("%w: set id is invalid", ErrInvalid)
	}
	rows, err := store.database.QueryContext(ctx, `
SELECT event_at, event_id, event_kind, operation_id,
       issued_at, expires_at, issue_command_hash, revocation_command_hash, revoked_at,
       binding_hash, binding_bytes,
       envelope_digest, envelope_bytes, attestation_key_id, payload_digest, payload_bytes
FROM credential_set_events
WHERE set_id = $1
ORDER BY version`, setID)
	if err != nil {
		return nil, fmt.Errorf("query PostgreSQL credential-set events: %w", err)
	}
	defer rows.Close()
	var result []Event
	for rows.Next() {
		event, scanErr := scanPostgresEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, ErrNotFound
	}
	if _, err := reduceEvents(setID, result); err != nil {
		return nil, fmt.Errorf("%w: PostgreSQL event ledger is invalid: %v", ErrInvalidTransition, err)
	}
	return result, nil
}

func buildPostgresEventMaterials(setID string, expectedVersion uint64, event Event) (postgresEventMaterials, error) {
	if !validUUIDv4(event.EventID) || event.At.IsZero() || event.At.Location() != time.UTC ||
		!event.At.Equal(event.At.UTC().Truncate(time.Millisecond)) {
		return postgresEventMaterials{}, fmt.Errorf("%w: event time or identity is invalid", ErrInvalidTransition)
	}
	requestedAt, err := canonicalTime(event.At)
	if err != nil {
		return postgresEventMaterials{}, fmt.Errorf("%w: event time is not canonical", ErrInvalidTransition)
	}
	projection := postgresEventProjection{
		EventID: event.EventID, ExpiresAt: event.ExpiresAt, IssueCommandHash: event.IssueCommandHash,
		IssuedAt: event.IssuedAt, Kind: event.Kind, OperationID: event.OperationID,
		RequestedAt: requestedAt, RevocationCommandHash: event.RevocationCommandHash, RevokedAt: event.RevokedAt,
	}
	materials := postgresEventMaterials{}
	if event.Binding != nil {
		if ValidateBinding(*event.Binding) != nil || event.Binding.SetID != setID {
			return postgresEventMaterials{}, fmt.Errorf("%w: event binding is invalid", ErrInvalid)
		}
		bindingBytes, marshalErr := json.Marshal(event.Binding)
		if marshalErr != nil || len(bindingBytes) > maximumBindingBytes {
			return postgresEventMaterials{}, fmt.Errorf("%w: event binding exceeds its canonical bound", ErrInvalid)
		}
		bindingHash := sha256Digest(bindingBytes)
		projection.Binding = &postgresEventBindingRef{ContentHash: bindingHash}
		materials.bindingHash, materials.bindingBytes, materials.bindingDocument = bindingHash, bindingBytes, string(bindingBytes)
	}
	if event.Attestation != nil {
		if !validAttestation(*event.Attestation) || len(event.Attestation.Envelope) > maximumEnvelopeBytes ||
			len(event.Attestation.Payload) > maximumPayloadBytes {
			return postgresEventMaterials{}, fmt.Errorf("%w: attestation is invalid or exceeds its public byte bound", ErrInvalid)
		}
		if err := validateCanonicalJSONObject(event.Attestation.Envelope); err != nil {
			return postgresEventMaterials{}, fmt.Errorf("%w: envelope is not canonical JSON: %v", ErrInvalid, err)
		}
		if err := validateCanonicalJSONObject(event.Attestation.Payload); err != nil {
			return postgresEventMaterials{}, fmt.Errorf("%w: payload is not canonical JSON: %v", ErrInvalid, err)
		}
		projection.Attestation = &postgresEventAttestationRef{
			EnvelopeDigest: event.Attestation.EnvelopeDigest, KeyID: event.Attestation.KeyID,
			PayloadDigest: event.Attestation.PayloadDigest,
		}
		materials.envelopeHash, materials.envelopeBytes, materials.envelopeDocument =
			event.Attestation.EnvelopeDigest, event.Attestation.Envelope, string(event.Attestation.Envelope)
		materials.payloadHash, materials.payloadBytes, materials.payloadDocument =
			event.Attestation.PayloadDigest, event.Attestation.Payload, string(event.Attestation.Payload)
	}
	request := postgresEventRequest{
		Event: projection, ExpectedVersion: expectedVersion, SchemaVersion: postgresEventRequestSchema, SetID: setID,
	}
	requestBytes, err := json.Marshal(request)
	if err != nil || len(requestBytes) > maximumEventRequestBytes {
		return postgresEventMaterials{}, fmt.Errorf("%w: canonical event request is invalid or oversized", ErrInvalid)
	}
	materials.requestBytes = requestBytes
	materials.requestHash = sha256Digest(requestBytes)
	materials.requestDocument = string(requestBytes)
	return materials, nil
}

func validateCanonicalJSONObject(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if _, object := document.(map[string]any); !object {
		return errors.New("document must be an object")
	}
	if decoder.More() {
		return errors.New("document has trailing JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("document has trailing JSON")
	}
	canonical, err := json.Marshal(document)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return errors.New("document bytes are not canonical")
	}
	return nil
}

type postgresSnapshotQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadPostgresSnapshot(ctx context.Context, queryer postgresSnapshotQueryer, setID string) (Snapshot, error) {
	row := queryer.QueryRowContext(ctx, `
SELECT set_id, version, phase, last_event_id, last_event_at,
       issue_operation_id, issue_command_hash, issued_at, expires_at,
       binding_hash, binding_bytes,
       issue_envelope_digest, issue_envelope_bytes, issue_attestation_key_id,
       issue_payload_digest, issue_payload_bytes,
       revocation_operation_id, revocation_command_hash, revoked_at,
       revocation_envelope_digest, revocation_envelope_bytes, revocation_attestation_key_id,
       revocation_payload_digest, revocation_payload_bytes
FROM credential_set_heads
WHERE set_id = $1`, setID)
	var snapshot Snapshot
	var version sql.NullInt64
	var phase, lastEventID, issueOperationID, issueCommandHash sql.NullString
	var issuedAt, expiresAt sql.NullTime
	var lastEventAt sql.NullTime
	var bindingHash sql.NullString
	var bindingBytes []byte
	var issueEnvelopeDigest, issueKeyID, issuePayloadDigest sql.NullString
	var issueEnvelope, issuePayload []byte
	var revokeOperationID, revokeCommandHash sql.NullString
	var revokedAt sql.NullTime
	var revokeEnvelopeDigest, revokeKeyID, revokePayloadDigest sql.NullString
	var revokeEnvelope, revokePayload []byte
	if err := row.Scan(
		&snapshot.SetID, &version, &phase, &lastEventID, &lastEventAt,
		&issueOperationID, &issueCommandHash, &issuedAt, &expiresAt,
		&bindingHash, &bindingBytes,
		&issueEnvelopeDigest, &issueEnvelope, &issueKeyID, &issuePayloadDigest, &issuePayload,
		&revokeOperationID, &revokeCommandHash, &revokedAt,
		&revokeEnvelopeDigest, &revokeEnvelope, &revokeKeyID, &revokePayloadDigest, &revokePayload,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Snapshot{}, ErrNotFound
		}
		return Snapshot{}, fmt.Errorf("scan PostgreSQL credential-set head: %w", err)
	}
	if !version.Valid || version.Int64 <= 0 || !phase.Valid || !lastEventID.Valid || !lastEventAt.Valid ||
		!issueOperationID.Valid || !issueCommandHash.Valid || !issuedAt.Valid || !expiresAt.Valid {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL credential-set head contains NULL authority", ErrInvalidTransition)
	}
	snapshot.Version = uint64(version.Int64)
	snapshot.Phase = Phase(phase.String)
	snapshot.LastEventID = lastEventID.String
	snapshot.LastEventAt = lastEventAt.Time.UTC()
	snapshot.IssueOperationID = issueOperationID.String
	snapshot.IssueCommandHash = issueCommandHash.String
	snapshot.IssuedAt = issuedAt.Time.UTC().Format(canonicalTimeLayout)
	snapshot.ExpiresAt = expiresAt.Time.UTC().Format(canonicalTimeLayout)
	if bindingHash.Valid || len(bindingBytes) > 0 {
		binding, err := decodePostgresBinding(bindingBytes, bindingHash.String)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Binding = &binding
	}
	if issueEnvelopeDigest.Valid || len(issueEnvelope) > 0 {
		attestation, err := decodePostgresAttestation(issueEnvelope, issueEnvelopeDigest.String, issueKeyID.String, issuePayload, issuePayloadDigest.String)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.IssueAttestation = &attestation
	}
	if revokeOperationID.Valid {
		snapshot.RevocationOperationID = revokeOperationID.String
	}
	if revokeCommandHash.Valid {
		snapshot.RevocationCommandHash = revokeCommandHash.String
	}
	if revokedAt.Valid {
		snapshot.RevokedAt = revokedAt.Time.UTC().Format(canonicalTimeLayout)
	}
	if revokeEnvelopeDigest.Valid || len(revokeEnvelope) > 0 {
		attestation, err := decodePostgresAttestation(revokeEnvelope, revokeEnvelopeDigest.String, revokeKeyID.String, revokePayload, revokePayloadDigest.String)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.RevocationAttestation = &attestation
	}
	return snapshot, nil
}

type postgresEventScanner interface {
	Scan(...any) error
}

func scanPostgresEvent(row postgresEventScanner) (Event, error) {
	var event Event
	var issuedAt, expiresAt, revokedAt sql.NullTime
	var bindingHash, envelopeDigest, keyID, payloadDigest sql.NullString
	var bindingBytes, envelopeBytes, payloadBytes []byte
	if err := row.Scan(
		&event.At, &event.EventID, &event.Kind, &event.OperationID,
		&issuedAt, &expiresAt, &event.IssueCommandHash, &event.RevocationCommandHash, &revokedAt,
		&bindingHash, &bindingBytes, &envelopeDigest, &envelopeBytes, &keyID, &payloadDigest, &payloadBytes,
	); err != nil {
		return Event{}, fmt.Errorf("scan PostgreSQL credential-set event: %w", err)
	}
	event.At = event.At.UTC()
	if issuedAt.Valid {
		event.IssuedAt = issuedAt.Time.UTC().Format(canonicalTimeLayout)
	}
	if expiresAt.Valid {
		event.ExpiresAt = expiresAt.Time.UTC().Format(canonicalTimeLayout)
	}
	if revokedAt.Valid {
		event.RevokedAt = revokedAt.Time.UTC().Format(canonicalTimeLayout)
	}
	if bindingHash.Valid || len(bindingBytes) > 0 {
		binding, err := decodePostgresBinding(bindingBytes, bindingHash.String)
		if err != nil {
			return Event{}, err
		}
		event.Binding = &binding
	}
	if envelopeDigest.Valid || len(envelopeBytes) > 0 {
		attestation, err := decodePostgresAttestation(envelopeBytes, envelopeDigest.String, keyID.String, payloadBytes, payloadDigest.String)
		if err != nil {
			return Event{}, err
		}
		event.Attestation = &attestation
	}
	return event, nil
}

func decodePostgresBinding(encoded []byte, expectedHash string) (SetBinding, error) {
	if len(encoded) == 0 || len(encoded) > maximumBindingBytes || sha256Digest(encoded) != expectedHash || validateCanonicalJSONObject(encoded) != nil {
		return SetBinding{}, fmt.Errorf("%w: PostgreSQL binding bytes are invalid", ErrInvalidTransition)
	}
	var binding SetBinding
	if err := json.Unmarshal(encoded, &binding); err != nil || ValidateBinding(binding) != nil {
		return SetBinding{}, fmt.Errorf("%w: PostgreSQL binding document is invalid", ErrInvalidTransition)
	}
	return binding, nil
}

func decodePostgresAttestation(envelope []byte, envelopeDigest, keyID string, payload []byte, payloadDigest string) (Attestation, error) {
	attestation := Attestation{
		Envelope: envelope, EnvelopeDigest: envelopeDigest, KeyID: keyID,
		Payload: payload, PayloadDigest: payloadDigest,
	}
	if len(envelope) > maximumEnvelopeBytes || len(payload) > maximumPayloadBytes || !validAttestation(attestation) ||
		validateCanonicalJSONObject(envelope) != nil || validateCanonicalJSONObject(payload) != nil {
		return Attestation{}, fmt.Errorf("%w: PostgreSQL attestation bytes are invalid", ErrInvalidTransition)
	}
	return attestation, nil
}

func classifyPostgresCredentialSetError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "WSC01":
			return errors.Join(ErrCASConflict, fmt.Errorf("append PostgreSQL credential-set event: %w", err))
		case "WSC02":
			return errors.Join(ErrIdempotencyConflict, fmt.Errorf("append PostgreSQL credential-set event: %w", err))
		case "WSC03", "23502", "23503", "23505", "23514", "22001", "22003", "22007", "22021", "22023", "22P02":
			return errors.Join(ErrInvalidTransition, fmt.Errorf("append PostgreSQL credential-set event: %w", err))
		case "WSC04":
			return ErrNotFound
		case "40001", "40P01":
			return errors.Join(ErrCASConflict, fmt.Errorf("append PostgreSQL credential-set event: %w", err))
		default:
			return fmt.Errorf("append PostgreSQL credential-set event: %w", err)
		}
	}
	return errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("append PostgreSQL credential-set event: %w", err))
}

var _ Store = (*PostgresStore)(nil)
