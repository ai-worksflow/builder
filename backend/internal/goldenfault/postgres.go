package goldenfault

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PostgresLedger struct {
	database *sql.DB
}

func NewPostgresLedger(database *sql.DB) (*PostgresLedger, error) {
	if database == nil {
		return nil, errors.New("Golden fault PostgreSQL ledger database is required")
	}
	return &PostgresLedger{database: database}, nil
}

func (ledger *PostgresLedger) TrustedTime(ctx context.Context) (time.Time, error) {
	if ledger == nil || ledger.database == nil || ctx == nil {
		return time.Time{}, errors.New("Golden fault PostgreSQL ledger and context are required")
	}
	var now time.Time
	if err := ledger.database.QueryRowContext(ctx, `SELECT date_trunc('milliseconds', clock_timestamp())`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("query PostgreSQL trusted time: %w", err)
	}
	return now.UTC(), nil
}

func (ledger *PostgresLedger) Reserve(ctx context.Context, reservation Reservation) (ConsumeRecord, error) {
	if ledger == nil || ledger.database == nil || ctx == nil {
		return ConsumeRecord{}, errors.New("Golden fault PostgreSQL ledger and context are required")
	}
	if err := validateReservation(reservation); err != nil {
		return ConsumeRecord{}, err
	}
	signerJSON, err := json.Marshal(reservation.SignerIdentities)
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("encode signer identity closure: %w", err)
	}
	var insertedAuthority uuid.UUID
	err = ledger.database.QueryRowContext(ctx, `
INSERT INTO golden_fault_consume_reservations (
  authority_id, fixture_id, run_id, operation_kind, resource_selector,
  expected_fence_digest, envelope_digest, payload_digest, predicate_digest,
  authority_issued_at, authority_expires_at, signer_identities,
  resolved_resource_id, resolved_head_digest, resolved_fence_digest,
  resolution_digest, adapter_invocation_id, reserved_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb,
  $13, $14, $15, $16, $17, $18
)
ON CONFLICT (authority_id) DO NOTHING
RETURNING authority_id`,
		reservation.AuthorityID, reservation.FixtureID, reservation.RunID, reservation.OperationKind,
		reservation.ResourceSelector, reservation.ExpectedFenceDigest, reservation.EnvelopeDigest,
		reservation.PayloadDigest, reservation.PredicateDigest, reservation.AuthorityIssuedAt,
		reservation.AuthorityExpiresAt, string(signerJSON), reservation.ResolvedResourceID,
		reservation.ResolvedHeadDigest, reservation.ResolvedFenceDigest, reservation.ResolutionDigest,
		reservation.AdapterInvocationID, reservation.ReservedAt,
	).Scan(&insertedAuthority)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ConsumeRecord{}, fmt.Errorf("insert Golden fault consume reservation: %w", err)
	}
	record, inspectErr := ledger.Inspect(ctx, reservation.AuthorityID)
	if inspectErr != nil {
		return ConsumeRecord{}, fmt.Errorf("read Golden fault consume reservation after CAS: %w", inspectErr)
	}
	record.Idempotent = errors.Is(err, sql.ErrNoRows)
	return record, nil
}

func (ledger *PostgresLedger) CommitTerminal(ctx context.Context, terminal TerminalResult) (ConsumeRecord, error) {
	if ledger == nil || ledger.database == nil || ctx == nil {
		return ConsumeRecord{}, errors.New("Golden fault PostgreSQL ledger and context are required")
	}
	if err := validateTerminalResult(terminal); err != nil {
		return ConsumeRecord{}, err
	}
	var insertedAuthority uuid.UUID
	err := ledger.database.QueryRowContext(ctx, `
INSERT INTO golden_fault_consume_results (
  authority_id, result_id, outcome, adapter_result_digest,
  observed_head_digest, observed_fence_digest, completed_at,
  receipt_bytes, receipt_document, receipt_digest
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
ON CONFLICT (authority_id) DO NOTHING
RETURNING authority_id`,
		terminal.AuthorityID, terminal.ResultID, terminal.Outcome, terminal.AdapterResultDigest,
		terminal.ObservedHeadDigest, terminal.ObservedFenceDigest, terminal.CompletedAt,
		terminal.ReceiptJSON, string(terminal.ReceiptJSON), terminal.ReceiptDigest,
	).Scan(&insertedAuthority)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ConsumeRecord{}, fmt.Errorf("insert Golden fault terminal result: %w", err)
	}
	record, inspectErr := ledger.Inspect(ctx, terminal.AuthorityID)
	if inspectErr != nil {
		return ConsumeRecord{}, fmt.Errorf("read Golden fault terminal result after CAS: %w", inspectErr)
	}
	record.Idempotent = errors.Is(err, sql.ErrNoRows)
	if record.Terminal == nil || !terminalResultsEqual(*record.Terminal, terminal) {
		return ConsumeRecord{}, fmt.Errorf("%w: terminal CAS found a different result", ErrConflict)
	}
	return record, nil
}

func (ledger *PostgresLedger) Inspect(ctx context.Context, authorityID uuid.UUID) (ConsumeRecord, error) {
	if ledger == nil || ledger.database == nil || ctx == nil || authorityID == uuid.Nil {
		return ConsumeRecord{}, errors.New("Golden fault PostgreSQL ledger, context, and authority ID are required")
	}
	return scanConsumeRecord(ledger.database.QueryRowContext(ctx, `
SELECT
  reservation.authority_id,
  reservation.fixture_id,
  reservation.run_id,
  reservation.operation_kind,
  reservation.resource_selector,
  reservation.expected_fence_digest,
  reservation.envelope_digest,
  reservation.payload_digest,
  reservation.predicate_digest,
  reservation.authority_issued_at,
  reservation.authority_expires_at,
  reservation.signer_identities,
  reservation.resolved_resource_id,
  reservation.resolved_head_digest,
  reservation.resolved_fence_digest,
  reservation.resolution_digest,
  reservation.adapter_invocation_id,
  reservation.reserved_at,
  result.result_id,
  result.outcome,
  result.adapter_result_digest,
  result.observed_head_digest,
  result.observed_fence_digest,
  result.completed_at,
  result.receipt_bytes,
  result.receipt_digest
FROM golden_fault_consume_reservations AS reservation
LEFT JOIN golden_fault_consume_results AS result
  ON result.authority_id = reservation.authority_id
WHERE reservation.authority_id = $1`, authorityID))
}

type rowScanner interface {
	Scan(...any) error
}

func scanConsumeRecord(row rowScanner) (ConsumeRecord, error) {
	var record ConsumeRecord
	var operation string
	var signerJSON []byte
	var resultID uuid.NullUUID
	var outcome, resultDigest, observedHead, observedFence, receiptDigest sql.NullString
	var completedAt sql.NullTime
	var receiptJSON []byte
	err := row.Scan(
		&record.Reservation.AuthorityID, &record.Reservation.FixtureID, &record.Reservation.RunID,
		&operation, &record.Reservation.ResourceSelector, &record.Reservation.ExpectedFenceDigest,
		&record.Reservation.EnvelopeDigest, &record.Reservation.PayloadDigest, &record.Reservation.PredicateDigest,
		&record.Reservation.AuthorityIssuedAt, &record.Reservation.AuthorityExpiresAt, &signerJSON,
		&record.Reservation.ResolvedResourceID, &record.Reservation.ResolvedHeadDigest,
		&record.Reservation.ResolvedFenceDigest, &record.Reservation.ResolutionDigest,
		&record.Reservation.AdapterInvocationID, &record.Reservation.ReservedAt,
		&resultID, &outcome, &resultDigest, &observedHead, &observedFence, &completedAt,
		&receiptJSON, &receiptDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsumeRecord{}, ErrNotFound
	}
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("scan Golden fault consume record: %w", err)
	}
	record.Reservation.OperationKind = OperationKind(operation)
	if err := decodeStrictJSON(signerJSON, &record.Reservation.SignerIdentities); err != nil ||
		!sortedUnique(record.Reservation.SignerIdentities) {
		return ConsumeRecord{}, fmt.Errorf("%w: stored signer identity closure is invalid", ErrConflict)
	}
	if err := validateReservation(record.Reservation); err != nil {
		return ConsumeRecord{}, fmt.Errorf("%w: stored reservation is invalid: %v", ErrConflict, err)
	}
	if !resultID.Valid {
		if outcome.Valid || resultDigest.Valid || observedHead.Valid || observedFence.Valid || completedAt.Valid ||
			len(receiptJSON) != 0 || receiptDigest.Valid {
			return ConsumeRecord{}, fmt.Errorf("%w: partial terminal row was returned", ErrConflict)
		}
		record.State = ConsumeStateReserved
		return record, nil
	}
	if !outcome.Valid || !resultDigest.Valid || !observedHead.Valid || !observedFence.Valid || !completedAt.Valid ||
		len(receiptJSON) == 0 || !receiptDigest.Valid {
		return ConsumeRecord{}, fmt.Errorf("%w: terminal row is incomplete", ErrConflict)
	}
	terminal := TerminalResult{
		AuthorityID: record.Reservation.AuthorityID, ResultID: resultID.UUID,
		Outcome: AdapterOutcome(outcome.String), AdapterResultDigest: resultDigest.String,
		ObservedHeadDigest: observedHead.String, ObservedFenceDigest: observedFence.String,
		CompletedAt: completedAt.Time.UTC(), ReceiptJSON: bytes.Clone(receiptJSON), ReceiptDigest: receiptDigest.String,
	}
	if err := decodeStrictJSON(receiptJSON, &terminal.Receipt); err != nil {
		return ConsumeRecord{}, fmt.Errorf("%w: decode stored consume receipt: %v", ErrConflict, err)
	}
	canonical, err := canonicalJSON(terminal.Receipt)
	if err != nil || !bytes.Equal(canonical, receiptJSON) {
		return ConsumeRecord{}, fmt.Errorf("%w: stored consume receipt is not canonical", ErrConflict)
	}
	if err := validateTerminalResult(terminal); err != nil {
		return ConsumeRecord{}, fmt.Errorf("%w: stored terminal result is invalid: %v", ErrConflict, err)
	}
	record.State = ConsumeStateTerminal
	record.Terminal = &terminal
	return record, nil
}

func validateReservation(reservation Reservation) error {
	selector, exists := selectorForOperation(reservation.OperationKind)
	if reservation.AuthorityID.Version() != 4 || reservation.FixtureID.Version() != 4 || reservation.RunID.Version() != 4 ||
		reservation.AdapterInvocationID.Version() != 4 || !exists || reservation.ResourceSelector != selector ||
		!validDigest(reservation.ExpectedFenceDigest) || !validDigest(reservation.EnvelopeDigest) ||
		!validDigest(reservation.PayloadDigest) || !validDigest(reservation.PredicateDigest) ||
		reservation.EnvelopeDigest == reservation.PayloadDigest || !validResourceID(reservation.ResolvedResourceID) ||
		!validDigest(reservation.ResolvedHeadDigest) || !validDigest(reservation.ResolvedFenceDigest) ||
		reservation.ResolvedFenceDigest != reservation.ExpectedFenceDigest || !validDigest(reservation.ResolutionDigest) ||
		!sortedUnique(reservation.SignerIdentities) || reservation.AuthorityIssuedAt.IsZero() ||
		reservation.AuthorityExpiresAt.IsZero() || reservation.ReservedAt.IsZero() ||
		!reservation.AuthorityExpiresAt.After(reservation.AuthorityIssuedAt) ||
		reservation.AuthorityExpiresAt.Sub(reservation.AuthorityIssuedAt) > MaximumAuthorityLifetime ||
		reservation.ReservedAt.Before(reservation.AuthorityIssuedAt.Add(-MaximumClockSkew)) ||
		!reservation.ReservedAt.Before(reservation.AuthorityExpiresAt) {
		return fmt.Errorf("%w: reservation does not contain an exact, live authority/resource/fence binding", ErrConflict)
	}
	digest, err := hashResolution(reservation.AuthorityID, ResourceResolution{
		ResourceID: reservation.ResolvedResourceID, HeadDigest: reservation.ResolvedHeadDigest,
		FenceDigest: reservation.ResolvedFenceDigest,
	})
	if err != nil || digest != reservation.ResolutionDigest {
		return fmt.Errorf("%w: resource resolution digest is invalid", ErrConflict)
	}
	return nil
}

func validateTerminalResult(terminal TerminalResult) error {
	if terminal.AuthorityID.Version() != 4 || terminal.ResultID.Version() != 4 || terminal.CompletedAt.IsZero() ||
		(terminal.Outcome != AdapterOutcomeApplied && terminal.Outcome != AdapterOutcomeRefused) ||
		!validDigest(terminal.AdapterResultDigest) || !validDigest(terminal.ObservedHeadDigest) ||
		!validDigest(terminal.ObservedFenceDigest) || !validDigest(terminal.ReceiptDigest) ||
		len(terminal.ReceiptJSON) == 0 || sha256Digest(terminal.ReceiptJSON) != terminal.ReceiptDigest {
		return fmt.Errorf("%w: terminal result identity, observations, or receipt digest is invalid", ErrConflict)
	}
	canonical, err := canonicalJSON(terminal.Receipt)
	if err != nil || !bytes.Equal(canonical, terminal.ReceiptJSON) || terminal.Receipt.SchemaVersion != ReceiptSchemaV1 ||
		terminal.Receipt.AuthorityID != terminal.AuthorityID || terminal.Receipt.ResultID != terminal.ResultID ||
		terminal.Receipt.Outcome != terminal.Outcome || terminal.Receipt.AdapterResultDigest != terminal.AdapterResultDigest ||
		terminal.Receipt.ObservedHeadDigest != terminal.ObservedHeadDigest ||
		terminal.Receipt.ObservedFenceDigest != terminal.ObservedFenceDigest ||
		terminal.Receipt.CompletedAt != formatCanonicalTime(terminal.CompletedAt) {
		return fmt.Errorf("%w: terminal receipt is not canonical or does not bind the result", ErrConflict)
	}
	return nil
}

func terminalResultsEqual(left, right TerminalResult) bool {
	return left.AuthorityID == right.AuthorityID && left.ResultID == right.ResultID && left.Outcome == right.Outcome &&
		left.AdapterResultDigest == right.AdapterResultDigest && left.ObservedHeadDigest == right.ObservedHeadDigest &&
		left.ObservedFenceDigest == right.ObservedFenceDigest && left.CompletedAt.Equal(right.CompletedAt) &&
		left.ReceiptDigest == right.ReceiptDigest && bytes.Equal(left.ReceiptJSON, right.ReceiptJSON)
}

var _ Ledger = (*PostgresLedger)(nil)
