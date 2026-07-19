package qualificationevidence

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
	postgresQualificationEvidenceRequestSchema = "worksflow-qualification-evidence-event-request/v1"
	maximumQualificationEvidenceEventBytes     = 2 << 20
	maximumQualificationEvidenceRequestBytes   = (2 << 20) + (256 << 10)
)

type postgresQualificationEvidenceRequest struct {
	Event           Event  `json:"event"`
	EventHash       string `json:"eventHash"`
	ExpectedVersion uint64 `json:"expectedVersion"`
	OrchestrationID string `json:"orchestrationId"`
	SchemaVersion   string `json:"schemaVersion"`
}

type postgresQualificationEvidenceMaterials struct {
	eventBytes      []byte
	eventDocument   string
	eventHash       string
	requestBytes    []byte
	requestDocument string
	requestHash     string
}

// PostgresStore is the durable, non-secret qualification-evidence event
// ledger created by migration 000073. It deliberately has owner-only
// authority: no production operator role, DSN, credential broker, KMS,
// capture, signer, or sealer is supplied by this adapter.
//
// This Store is the only supported writer. It supplies CanonicalJSON bytes and
// replays the inserted bytes with applyEvent inside the same transaction, so a
// non-canonical or unreplayable commit is rolled back. The database function
// is intentionally not executable by PUBLIC or the application role. Before
// any future non-owner operator receives EXECUTE, migration 000073 must gain an
// independent database-side raw canonical-JSON validator (JSONB equality alone
// cannot distinguish duplicate keys or normalized number spellings).
type PostgresStore struct {
	database *sql.DB
}

func NewPostgresStore(database *sql.DB) (*PostgresStore, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: PostgreSQL qualification-evidence database is required", ErrInvalid)
	}
	return &PostgresStore{database: database}, nil
}

func (store *PostgresStore) TrustedTime(ctx context.Context) (time.Time, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL qualification-evidence store or context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	var observed sql.NullTime
	if err := store.database.QueryRowContext(ctx, `
SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&observed); err != nil {
		return time.Time{}, fmt.Errorf("query PostgreSQL qualification-evidence trusted time: %w", err)
	}
	if !observed.Valid {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL qualification-evidence trusted time is NULL", ErrInvalid)
	}
	now := observed.Time.UTC()
	if now.IsZero() || !now.Equal(now.Truncate(time.Millisecond)) {
		return time.Time{}, fmt.Errorf("%w: PostgreSQL qualification-evidence trusted time is non-canonical", ErrInvalid)
	}
	return now, nil
}

func (store *PostgresStore) Create(ctx context.Context, orchestrationID string, event Event) (Snapshot, bool, error) {
	if event.Kind != EventReserved {
		return Snapshot{}, false, fmt.Errorf("%w: qualification-evidence reservation event is required", ErrInvalid)
	}
	return store.append(ctx, orchestrationID, 0, event)
}

func (store *PostgresStore) Append(ctx context.Context, orchestrationID string, expectedVersion uint64, event Event) (Snapshot, error) {
	snapshot, _, err := store.append(ctx, orchestrationID, expectedVersion, event)
	return snapshot, err
}

func (store *PostgresStore) append(ctx context.Context, orchestrationID string, expectedVersion uint64, event Event) (Snapshot, bool, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) {
		return Snapshot{}, false, fmt.Errorf("%w: PostgreSQL qualification-evidence store or context is nil", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	if !validUUIDv4(orchestrationID) || !validUUIDv4(event.EventID) || expectedVersion > uint64(math.MaxInt64) {
		return Snapshot{}, false, fmt.Errorf("%w: PostgreSQL qualification-evidence identity or version is invalid", ErrInvalid)
	}

	// A globally reused EventID is decided before time, version, operation, or
	// transition checks. This makes exact replay inspectable after the head has
	// advanced and makes drift deterministically fail closed.
	if exact, err := store.inspectExistingEvent(ctx, orchestrationID, expectedVersion, event); err != nil || exact {
		if err != nil {
			return Snapshot{}, false, err
		}
		snapshot, loadErr := store.Load(ctx, orchestrationID)
		return snapshot, false, loadErr
	}

	if event.Kind == EventReserved {
		if _, err := applyEvent(Snapshot{OrchestrationID: orchestrationID}, event); err != nil {
			return Snapshot{}, false, err
		}
	} else {
		current, err := store.Load(ctx, orchestrationID)
		if err != nil {
			return Snapshot{}, false, err
		}
		if current.Version != expectedVersion {
			return current, false, ErrCASConflict
		}
		if _, err := applyEvent(current, event); err != nil {
			return Snapshot{}, false, err
		}
	}
	materials, err := buildPostgresQualificationEvidenceMaterials(orchestrationID, expectedVersion, event)
	if err != nil {
		return Snapshot{}, false, err
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("begin PostgreSQL qualification-evidence append: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var created bool
	if err := transaction.QueryRowContext(ctx, `
SELECT append_qualification_evidence_event($1,$2,$3::jsonb,$4,$5,$6::jsonb)`,
		materials.requestHash, materials.requestBytes, materials.requestDocument,
		materials.eventHash, materials.eventBytes, materials.eventDocument,
	).Scan(&created); err != nil {
		classified := classifyPostgresQualificationEvidenceError(err)
		_ = transaction.Rollback()
		if event.Kind == EventReserved && errors.Is(classified, ErrCASConflict) {
			// SERIALIZABLE waiters can receive 40001 because their transaction
			// snapshot predates the winning reservation. Reconcile through a new
			// strongly consistent read; only the exact Plan/command/trust tuple is
			// an idempotent reuse. A different winner remains a conflict.
			current, loadErr := store.Load(ctx, orchestrationID)
			if loadErr == nil && event.Plan != nil && current.Plan != nil &&
				current.CommandHash == event.CommandHash &&
				current.TrustBindingsDigest == event.TrustBindingsDigest &&
				canonicalEqual(*current.Plan, *event.Plan) {
				return current, false, nil
			}
			if loadErr != nil && !errors.Is(loadErr, ErrNotFound) {
				return Snapshot{}, false, loadErr
			}
		}
		return Snapshot{}, false, classified
	}
	snapshot, err := loadPostgresQualificationEvidenceSnapshot(ctx, transaction, orchestrationID)
	if err != nil {
		return Snapshot{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return Snapshot{}, false, errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("commit PostgreSQL qualification-evidence append: %w", err))
	}
	committed = true
	return snapshot, created, nil
}

func (store *PostgresStore) inspectExistingEvent(ctx context.Context, orchestrationID string, expectedVersion uint64, event Event) (bool, error) {
	var storedOrchestrationID string
	var storedExpectedVersion int64
	var requestedAt, eventAt time.Time
	var storedRequestHash, storedEventHash string
	var storedRequestBytes, storedEventBytes []byte
	err := store.database.QueryRowContext(ctx, `
SELECT orchestration_id, expected_version, requested_at, event_at,
       request_hash, request_bytes, event_hash, event_bytes
FROM qualification_evidence_events
WHERE event_id=$1`, event.EventID).Scan(
		&storedOrchestrationID, &storedExpectedVersion, &requestedAt, &eventAt,
		&storedRequestHash, &storedRequestBytes, &storedEventHash, &storedEventBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect PostgreSQL qualification-evidence EventID: %w", err)
	}
	if storedOrchestrationID != orchestrationID || storedExpectedVersion < 0 || uint64(storedExpectedVersion) != expectedVersion {
		return false, ErrIdempotencyConflict
	}
	requested, requestedErr := canonicalTime(requestedAt.UTC())
	authoritative, authoritativeErr := canonicalTime(eventAt.UTC())
	if requestedErr != nil || authoritativeErr != nil || (event.At != requested && event.At != authoritative) {
		return false, ErrIdempotencyConflict
	}
	candidate := cloneEvent(event)
	candidate.At = requested
	materials, err := buildPostgresQualificationEvidenceMaterials(orchestrationID, expectedVersion, candidate)
	if err != nil || materials.eventHash != storedEventHash || !bytes.Equal(materials.eventBytes, storedEventBytes) ||
		materials.requestHash != storedRequestHash || !bytes.Equal(materials.requestBytes, storedRequestBytes) {
		return false, ErrIdempotencyConflict
	}
	return true, nil
}

func (store *PostgresStore) Load(ctx context.Context, orchestrationID string) (Snapshot, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || !validUUIDv4(orchestrationID) {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL qualification-evidence store, context, or identity is invalid", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Snapshot{}, fmt.Errorf("begin PostgreSQL qualification-evidence load: %w", err)
	}
	defer transaction.Rollback()
	snapshot, err := loadPostgresQualificationEvidenceSnapshot(ctx, transaction, orchestrationID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Snapshot{}, fmt.Errorf("commit PostgreSQL qualification-evidence load: %w", err)
	}
	return snapshot, nil
}

func (store *PostgresStore) Events(ctx context.Context, orchestrationID string) ([]Event, error) {
	if store == nil || store.database == nil || isNilInterface(ctx) || !validUUIDv4(orchestrationID) {
		return nil, fmt.Errorf("%w: PostgreSQL qualification-evidence store, context, or identity is invalid", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loadPostgresQualificationEvidenceEvents(ctx, store.database, orchestrationID)
}

func buildPostgresQualificationEvidenceMaterials(orchestrationID string, expectedVersion uint64, event Event) (postgresQualificationEvidenceMaterials, error) {
	if !validUUIDv4(orchestrationID) || !validUUIDv4(event.EventID) || expectedVersion > uint64(math.MaxInt64) {
		return postgresQualificationEvidenceMaterials{}, fmt.Errorf("%w: PostgreSQL qualification-evidence request identity is invalid", ErrInvalid)
	}
	if _, err := parseCanonicalTime(event.At); err != nil {
		return postgresQualificationEvidenceMaterials{}, fmt.Errorf("%w: qualification-evidence event time is not canonical", ErrInvalidTransition)
	}
	eventBytes, err := CanonicalJSON(event)
	if err != nil || len(eventBytes) == 0 || len(eventBytes) > maximumQualificationEvidenceEventBytes {
		return postgresQualificationEvidenceMaterials{}, fmt.Errorf("%w: qualification-evidence event exceeds its canonical bound", ErrInvalid)
	}
	eventHash := sha256Digest(eventBytes)
	request := postgresQualificationEvidenceRequest{
		Event: event, EventHash: eventHash, ExpectedVersion: expectedVersion,
		OrchestrationID: orchestrationID, SchemaVersion: postgresQualificationEvidenceRequestSchema,
	}
	requestBytes, err := CanonicalJSON(request)
	if err != nil || len(requestBytes) == 0 || len(requestBytes) > maximumQualificationEvidenceRequestBytes {
		return postgresQualificationEvidenceMaterials{}, fmt.Errorf("%w: qualification-evidence event request exceeds its canonical bound", ErrInvalid)
	}
	return postgresQualificationEvidenceMaterials{
		eventBytes: eventBytes, eventDocument: string(eventBytes), eventHash: eventHash,
		requestBytes: requestBytes, requestDocument: string(requestBytes), requestHash: sha256Digest(requestBytes),
	}, nil
}

type postgresQualificationEvidenceQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadPostgresQualificationEvidenceSnapshot(ctx context.Context, queryer postgresQualificationEvidenceQueryer, orchestrationID string) (Snapshot, error) {
	events, err := loadPostgresQualificationEvidenceEvents(ctx, queryer, orchestrationID)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := reduceEvents(orchestrationID, events)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL qualification-evidence event ledger is invalid: %v", ErrInvalidTransition, err)
	}
	var headVersion int64
	var headPhase, headEventID, headCommandHash, headTrustDigest, headActiveArtifact string
	var headEventAt time.Time
	var headActiveOperation sql.NullString
	var headPlanDocument []byte
	err = queryer.QueryRowContext(ctx, `
SELECT version, phase, last_event_id, last_event_at, command_hash, trust_bindings_digest,
       active_operation_id, active_artifact_id, plan_document
FROM qualification_evidence_heads
WHERE orchestration_id=$1`, orchestrationID).Scan(
		&headVersion, &headPhase, &headEventID, &headEventAt, &headCommandHash, &headTrustDigest,
		&headActiveOperation, &headActiveArtifact, &headPlanDocument,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("scan PostgreSQL qualification-evidence head: %w", err)
	}
	headAt, timeErr := canonicalTime(headEventAt.UTC())
	activeOperation := ""
	if headActiveOperation.Valid {
		activeOperation = headActiveOperation.String
	}
	if headVersion <= 0 || uint64(headVersion) != snapshot.Version || Phase(headPhase) != snapshot.Phase ||
		headEventID != snapshot.LastEventID || timeErr != nil || headAt != snapshot.LastEventAt ||
		headCommandHash != snapshot.CommandHash || headTrustDigest != snapshot.TrustBindingsDigest ||
		activeOperation != snapshot.ActiveOperationID || headActiveArtifact != snapshot.ActiveArtifactID {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL qualification-evidence head drifted from event replay", ErrInvalidTransition)
	}
	if snapshot.Plan == nil {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL qualification-evidence replay has no plan", ErrInvalidTransition)
	}
	var storedPlan Plan
	planDecoder := json.NewDecoder(bytes.NewReader(headPlanDocument))
	planDecoder.DisallowUnknownFields()
	if err := planDecoder.Decode(&storedPlan); err != nil || ValidatePlan(storedPlan) != nil || !canonicalEqual(storedPlan, *snapshot.Plan) {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL qualification-evidence head plan is invalid", ErrInvalidTransition)
	}
	var trailing any
	if err := planDecoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Snapshot{}, fmt.Errorf("%w: PostgreSQL qualification-evidence head plan has trailing JSON", ErrInvalidTransition)
	}
	return snapshot, nil
}

func loadPostgresQualificationEvidenceEvents(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, orchestrationID string) ([]Event, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT orchestration_id, expected_version, event_at, requested_at,
       request_hash, request_bytes, request_document,
       event_hash, event_bytes, event_document
FROM qualification_evidence_events
WHERE orchestration_id=$1
ORDER BY version`, orchestrationID)
	if err != nil {
		return nil, fmt.Errorf("query PostgreSQL qualification-evidence events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var storedOrchestrationID string
		var expectedVersion int64
		var eventAt, requestedAt time.Time
		var requestHash, eventHash string
		var requestBytes, requestDocument, eventBytes, eventDocument []byte
		if err := rows.Scan(
			&storedOrchestrationID, &expectedVersion, &eventAt, &requestedAt,
			&requestHash, &requestBytes, &requestDocument,
			&eventHash, &eventBytes, &eventDocument,
		); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL qualification-evidence event: %w", err)
		}
		event, err := decodePostgresQualificationEvidenceEvent(
			storedOrchestrationID, expectedVersion, eventAt, requestedAt,
			requestHash, requestBytes, requestDocument,
			eventHash, eventBytes, eventDocument,
		)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrNotFound
	}
	if _, err := reduceEvents(orchestrationID, events); err != nil {
		return nil, fmt.Errorf("%w: PostgreSQL qualification-evidence event ledger is invalid: %v", ErrInvalidTransition, err)
	}
	return events, nil
}

func decodePostgresQualificationEvidenceEvent(
	orchestrationID string,
	expectedVersion int64,
	eventAt, requestedAt time.Time,
	requestHash string,
	requestBytes, requestDocument []byte,
	eventHash string,
	eventBytes, eventDocument []byte,
) (Event, error) {
	if len(eventBytes) == 0 || len(eventBytes) > maximumQualificationEvidenceEventBytes || !validDigest(eventHash) || sha256Digest(eventBytes) != eventHash {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence event bytes or hash is invalid", ErrInvalidTransition)
	}
	var event Event
	decoder := json.NewDecoder(bytes.NewReader(eventBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("%w: decode PostgreSQL qualification-evidence event: %v", ErrInvalidTransition, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence event has trailing JSON", ErrInvalidTransition)
	}
	canonical, err := CanonicalJSON(event)
	if err != nil || !bytes.Equal(canonical, eventBytes) {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence event bytes are not canonical", ErrInvalidTransition)
	}
	var documented Event
	if err := json.Unmarshal(eventDocument, &documented); err != nil || !canonicalEqual(documented, event) {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence event document drifted", ErrInvalidTransition)
	}
	if expectedVersion < 0 || len(requestBytes) == 0 || len(requestBytes) > maximumQualificationEvidenceRequestBytes ||
		!validDigest(requestHash) || sha256Digest(requestBytes) != requestHash {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence request bytes or hash is invalid", ErrInvalidTransition)
	}
	var request postgresQualificationEvidenceRequest
	requestDecoder := json.NewDecoder(bytes.NewReader(requestBytes))
	requestDecoder.DisallowUnknownFields()
	if err := requestDecoder.Decode(&request); err != nil {
		return Event{}, fmt.Errorf("%w: decode PostgreSQL qualification-evidence request: %v", ErrInvalidTransition, err)
	}
	if err := requestDecoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence request has trailing JSON", ErrInvalidTransition)
	}
	canonicalRequest, err := CanonicalJSON(request)
	if err != nil || !bytes.Equal(canonicalRequest, requestBytes) {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence request bytes are not canonical", ErrInvalidTransition)
	}
	var documentedRequest postgresQualificationEvidenceRequest
	if err := json.Unmarshal(requestDocument, &documentedRequest); err != nil || !canonicalEqual(documentedRequest, request) {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence request document drifted", ErrInvalidTransition)
	}
	if request.SchemaVersion != postgresQualificationEvidenceRequestSchema ||
		request.OrchestrationID != orchestrationID || request.ExpectedVersion != uint64(expectedVersion) ||
		request.EventHash != eventHash || !canonicalEqual(request.Event, event) {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence request binding drifted", ErrInvalidTransition)
	}
	requested, requestedErr := canonicalTime(requestedAt.UTC())
	authoritative, authoritativeErr := canonicalTime(eventAt.UTC())
	if requestedErr != nil || authoritativeErr != nil || event.At != requested {
		return Event{}, fmt.Errorf("%w: PostgreSQL qualification-evidence event time is invalid", ErrInvalidTransition)
	}
	event.At = authoritative
	return event, nil
}

func classifyPostgresQualificationEvidenceError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "WQE01", "40001", "40P01":
			return errors.Join(ErrCASConflict, fmt.Errorf("append PostgreSQL qualification-evidence event: %w", err))
		case "WQE02":
			return errors.Join(ErrIdempotencyConflict, fmt.Errorf("append PostgreSQL qualification-evidence event: %w", err))
		case "WQE03", "23502", "23503", "23505", "23514", "22001", "22003", "22007", "22021", "22023", "22P02":
			return errors.Join(ErrInvalidTransition, fmt.Errorf("append PostgreSQL qualification-evidence event: %w", err))
		case "WQE04":
			return ErrNotFound
		default:
			return fmt.Errorf("append PostgreSQL qualification-evidence event: %w", err)
		}
	}
	return errors.Join(ErrStoreOutcomeUnknown, fmt.Errorf("append PostgreSQL qualification-evidence event: %w", err))
}

var _ Store = (*PostgresStore)(nil)
