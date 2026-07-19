package goldenfault

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
)

const resolutionSchemaV1 = "worksflow-golden-fault-resource-resolution/v1"

type Service struct {
	verifier *Verifier
	ledger   Ledger
	adapters map[OperationKind]Adapter
}

// NewService copies a closed, typed adapter registry. Missing adapters remain
// denied; extra operation names can never be registered.
func NewService(verifier *Verifier, ledger Ledger, adapters map[OperationKind]Adapter) (*Service, error) {
	if verifier == nil || ledger == nil || interfaceIsNil(ledger) {
		return nil, errors.New("Golden fault verifier and ledger are required")
	}
	registry := make(map[OperationKind]Adapter, len(adapters))
	for operation, adapter := range adapters {
		if _, allowed := selectorForOperation(operation); !allowed || adapter == nil || interfaceIsNil(adapter) {
			return nil, fmt.Errorf("%w: invalid adapter registration for %q", ErrAdapterMissing, operation)
		}
		registry[operation] = adapter
	}
	return &Service{verifier: verifier, ledger: ledger, adapters: registry}, nil
}

func (service *Service) Consume(ctx context.Context, envelope []byte, expected ExpectedBinding) (ConsumeRecord, error) {
	if service == nil || service.verifier == nil || service.ledger == nil || ctx == nil {
		return ConsumeRecord{}, errors.New("Golden fault service and context are required")
	}
	if err := validateExpectedBinding(expected); err != nil {
		return ConsumeRecord{}, err
	}
	if sha256Digest(envelope) != expected.EnvelopeDigest {
		return ConsumeRecord{}, fmt.Errorf("%w: DSSE envelope digest does not match the Golden fixture", ErrInvalidAuthority)
	}
	// An exact prior reservation is authoritative even after envelope expiry.
	// This is the response-lost replay path; it can only return the same durable
	// result or the same unknown/conflict state and never reaches an adapter.
	if existing, inspectErr := service.ledger.Inspect(ctx, expected.AuthorityID); inspectErr == nil {
		if err := validateExistingAuthorityRecord(existing, expected); err != nil {
			return ConsumeRecord{}, err
		}
		if existing.State == ConsumeStateTerminal {
			existing.Idempotent = true
			return existing, nil
		}
		existing.Idempotent = true
		return existing, fmt.Errorf("%w: %w", ErrConflict, ErrOutcomeUnknown)
	} else if !errors.Is(inspectErr, ErrNotFound) {
		return ConsumeRecord{}, fmt.Errorf("inspect Golden fault authority before reservation: %w", inspectErr)
	}
	now, err := service.ledger.TrustedTime(ctx)
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("read trusted fault-authority time: %w", err)
	}
	if now.IsZero() {
		return ConsumeRecord{}, errors.New("read trusted fault-authority time: ledger returned zero time")
	}
	verified, err := service.verifier.VerifyAt(envelope, expected, now)
	if err != nil {
		return ConsumeRecord{}, err
	}
	adapter, exists := service.adapters[verified.Predicate.OperationKind]
	if !exists || adapter == nil || interfaceIsNil(adapter) {
		return ConsumeRecord{}, fmt.Errorf("%w for operation %q", ErrAdapterMissing, verified.Predicate.OperationKind)
	}
	resolution, err := safeResolve(ctx, adapter, verified)
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("resolve exact Golden fault resource: %w", err)
	}
	if err := validateResolution(resolution, verified.Predicate.ExpectedFenceDigest); err != nil {
		return ConsumeRecord{}, err
	}
	resolutionDigest, err := hashResolution(expected.AuthorityID, resolution)
	if err != nil {
		return ConsumeRecord{}, err
	}
	predicateJSON, err := canonicalJSON(verified.Predicate)
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("canonicalize verified Golden fault predicate: %w", err)
	}
	reservation := Reservation{
		AuthorityID: expected.AuthorityID, FixtureID: expected.FixtureID, RunID: expected.RunID,
		OperationKind: verified.Predicate.OperationKind, ResourceSelector: verified.Predicate.ResourceSelector,
		ExpectedFenceDigest: verified.Predicate.ExpectedFenceDigest, EnvelopeDigest: verified.EnvelopeDigest,
		PayloadDigest: verified.PayloadDigest, PredicateDigest: sha256Digest(predicateJSON),
		AuthorityIssuedAt: verified.IssuedAt, AuthorityExpiresAt: verified.ExpiresAt,
		SignerIdentities:   append([]string(nil), verified.SignerIdentities...),
		ResolvedResourceID: resolution.ResourceID, ResolvedHeadDigest: resolution.HeadDigest,
		ResolvedFenceDigest: resolution.FenceDigest, ResolutionDigest: resolutionDigest,
		AdapterInvocationID: uuid.New(), ReservedAt: now.UTC(),
	}
	record, err := service.ledger.Reserve(ctx, reservation)
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("reserve Golden fault authority: %w", err)
	}
	if err := validateRecord(record, reservation); err != nil {
		return ConsumeRecord{}, err
	}
	if record.Idempotent {
		if record.State == ConsumeStateTerminal {
			return record, nil
		}
		return record, fmt.Errorf("%w: %w", ErrConflict, ErrOutcomeUnknown)
	}

	result, err := safeExecute(ctx, adapter, AdapterRequest{
		Authority: verified, AdapterInvocationID: reservation.AdapterInvocationID, Resource: resolution,
	})
	if err != nil {
		// The durable reservation is the only truthful state after a response
		// whose external commit status cannot be proven. Never invoke again.
		return record, fmt.Errorf("%w: adapter execution did not produce a terminal receipt: %v", ErrOutcomeUnknown, err)
	}
	if err := validateAdapterResult(result); err != nil {
		return record, fmt.Errorf("%w: adapter returned an invalid terminal result: %v", ErrOutcomeUnknown, err)
	}
	completedAt, err := service.ledger.TrustedTime(ctx)
	if err != nil {
		return record, fmt.Errorf("%w: read trusted terminal time: %v", ErrOutcomeUnknown, err)
	}
	if completedAt.IsZero() || completedAt.Before(reservation.ReservedAt) {
		return record, fmt.Errorf("%w: trusted terminal time is unavailable", ErrOutcomeUnknown)
	}
	terminal, err := buildTerminalResult(reservation, result, completedAt.UTC())
	if err != nil {
		return record, fmt.Errorf("%w: build terminal receipt: %v", ErrOutcomeUnknown, err)
	}
	committed, err := service.ledger.CommitTerminal(ctx, terminal)
	if err == nil {
		if validateErr := validateRecord(committed, reservation); validateErr != nil {
			return ConsumeRecord{}, validateErr
		}
		return committed, nil
	}
	// A lost response after INSERT is reconciled only by reading the exact
	// authority. The adapter is never called a second time.
	inspected, inspectErr := service.ledger.Inspect(ctx, reservation.AuthorityID)
	if inspectErr == nil && inspected.State == ConsumeStateTerminal {
		if validateErr := validateRecord(inspected, reservation); validateErr == nil {
			return inspected, nil
		}
	}
	return record, fmt.Errorf("%w: terminal receipt commit could not be reconciled", ErrOutcomeUnknown)
}

func (service *Service) Inspect(ctx context.Context, query AuthorityQuery) (ConsumeRecord, error) {
	if service == nil || service.ledger == nil || ctx == nil {
		return ConsumeRecord{}, errors.New("Golden fault service and context are required")
	}
	if query.AuthorityID == uuid.Nil || query.FixtureID == uuid.Nil || query.RunID == uuid.Nil ||
		!validDigest(query.EnvelopeDigest) || !validDigest(query.PayloadDigest) {
		return ConsumeRecord{}, fmt.Errorf("%w: exact authority query is incomplete", ErrConflict)
	}
	record, err := service.ledger.Inspect(ctx, query.AuthorityID)
	if err != nil {
		return ConsumeRecord{}, err
	}
	reservation := record.Reservation
	if reservation.AuthorityID != query.AuthorityID || reservation.FixtureID != query.FixtureID ||
		reservation.RunID != query.RunID || reservation.EnvelopeDigest != query.EnvelopeDigest ||
		reservation.PayloadDigest != query.PayloadDigest {
		return ConsumeRecord{}, fmt.Errorf("%w: query does not bind the committed authority", ErrConflict)
	}
	if record.State == ConsumeStateReserved {
		if record.Terminal != nil {
			return ConsumeRecord{}, fmt.Errorf("%w: reserved authority unexpectedly has a terminal result", ErrConflict)
		}
		return record, ErrOutcomeUnknown
	}
	if record.State != ConsumeStateTerminal || record.Terminal == nil {
		return ConsumeRecord{}, fmt.Errorf("%w: ledger returned an invalid consume state", ErrConflict)
	}
	if err := validateTerminalClosure(*record.Terminal, reservation); err != nil {
		return ConsumeRecord{}, err
	}
	return record, nil
}

type resolutionDocument struct {
	AuthorityID   string `json:"authorityId"`
	FenceDigest   string `json:"fenceDigest"`
	HeadDigest    string `json:"headDigest"`
	ResourceID    string `json:"resourceId"`
	SchemaVersion string `json:"schemaVersion"`
}

func hashResolution(authorityID uuid.UUID, resolution ResourceResolution) (string, error) {
	encoded, err := canonicalJSON(resolutionDocument{
		AuthorityID: authorityID.String(), FenceDigest: resolution.FenceDigest, HeadDigest: resolution.HeadDigest,
		ResourceID: resolution.ResourceID, SchemaVersion: resolutionSchemaV1,
	})
	if err != nil {
		return "", fmt.Errorf("canonicalize fault resource resolution: %w", err)
	}
	return sha256Digest(encoded), nil
}

func validateResolution(resolution ResourceResolution, expectedFence string) error {
	if !validResourceID(resolution.ResourceID) || !validDigest(resolution.HeadDigest) ||
		!validDigest(resolution.FenceDigest) || resolution.FenceDigest != expectedFence {
		return fmt.Errorf("%w: resolved resource identity, head, or expected fence is invalid", ErrConflict)
	}
	return nil
}

func validateAdapterResult(result AdapterResult) error {
	if (result.Outcome != AdapterOutcomeApplied && result.Outcome != AdapterOutcomeRefused) ||
		!validDigest(result.ResultDigest) || !validDigest(result.ObservedHeadDigest) ||
		!validDigest(result.ObservedFenceDigest) {
		return errors.New("outcome and all result observation digests must be from the closed contract")
	}
	return nil
}

func buildTerminalResult(reservation Reservation, result AdapterResult, completedAt time.Time) (TerminalResult, error) {
	resultID := uuid.New()
	receipt := ConsumeReceipt{
		AdapterInvocationID: reservation.AdapterInvocationID, AdapterResultDigest: result.ResultDigest,
		AuthorityID: reservation.AuthorityID, CompletedAt: formatCanonicalTime(completedAt),
		EnvelopeDigest: reservation.EnvelopeDigest, ExpectedFenceDigest: reservation.ExpectedFenceDigest,
		FixtureID: reservation.FixtureID, ObservedFenceDigest: result.ObservedFenceDigest,
		ObservedHeadDigest: result.ObservedHeadDigest, OperationKind: reservation.OperationKind,
		Outcome: result.Outcome, PayloadDigest: reservation.PayloadDigest, PredicateDigest: reservation.PredicateDigest,
		ReservedAt: formatCanonicalTime(reservation.ReservedAt), ResolutionDigest: reservation.ResolutionDigest,
		ResolvedFenceDigest: reservation.ResolvedFenceDigest, ResolvedHeadDigest: reservation.ResolvedHeadDigest,
		ResolvedResourceID: reservation.ResolvedResourceID, ResourceSelector: reservation.ResourceSelector,
		ResultID: resultID, RunID: reservation.RunID, SchemaVersion: ReceiptSchemaV1,
	}
	encoded, err := canonicalJSON(receipt)
	if err != nil {
		return TerminalResult{}, err
	}
	return TerminalResult{
		AuthorityID: reservation.AuthorityID, ResultID: resultID, Outcome: result.Outcome,
		AdapterResultDigest: result.ResultDigest, ObservedHeadDigest: result.ObservedHeadDigest,
		ObservedFenceDigest: result.ObservedFenceDigest, CompletedAt: completedAt,
		Receipt: receipt, ReceiptJSON: encoded, ReceiptDigest: sha256Digest(encoded),
	}, nil
}

func validateRecord(record ConsumeRecord, expected Reservation) error {
	actual := record.Reservation
	if record.State != ConsumeStateReserved && record.State != ConsumeStateTerminal {
		return fmt.Errorf("%w: ledger returned an invalid state", ErrConflict)
	}
	bindingsMatch := reservationBindingsEqual(actual, expected)
	if !bindingsMatch || (!record.Idempotent && !reservationsEqual(actual, expected)) {
		return fmt.Errorf("%w: authority ID already binds a different reservation", ErrConflict)
	}
	if record.State == ConsumeStateTerminal {
		if record.Terminal == nil {
			return fmt.Errorf("%w: terminal result is missing", ErrConflict)
		}
		if err := validateTerminalClosure(*record.Terminal, actual); err != nil {
			return err
		}
	} else if record.Terminal != nil {
		return fmt.Errorf("%w: reserved record unexpectedly has a terminal result", ErrConflict)
	}
	return nil
}

func validateExistingAuthorityRecord(record ConsumeRecord, expected ExpectedBinding) error {
	reservation := record.Reservation
	if err := validateReservation(reservation); err != nil {
		return fmt.Errorf("%w: stored reservation is invalid: %v", ErrConflict, err)
	}
	if reservation.AuthorityID != expected.AuthorityID || reservation.FixtureID != expected.FixtureID ||
		reservation.RunID != expected.RunID || reservation.OperationKind != expected.OperationKind ||
		reservation.ResourceSelector != expected.ResourceSelector ||
		reservation.ExpectedFenceDigest != expected.ExpectedFenceDigest ||
		reservation.EnvelopeDigest != expected.EnvelopeDigest || reservation.PayloadDigest != expected.PayloadDigest {
		return fmt.Errorf("%w: authority ID already binds a different signed authority", ErrConflict)
	}
	if record.State == ConsumeStateReserved && record.Terminal != nil {
		return fmt.Errorf("%w: reserved authority unexpectedly has a terminal result", ErrConflict)
	}
	if record.State == ConsumeStateTerminal {
		if record.Terminal == nil {
			return fmt.Errorf("%w: terminal authority result is missing", ErrConflict)
		}
		if err := validateTerminalClosure(*record.Terminal, reservation); err != nil {
			return err
		}
		return nil
	}
	if record.State != ConsumeStateReserved {
		return fmt.Errorf("%w: ledger returned an invalid state", ErrConflict)
	}
	return nil
}

func validateTerminalClosure(terminal TerminalResult, reservation Reservation) error {
	if err := validateTerminalResult(terminal); err != nil {
		return fmt.Errorf("%w: terminal result is invalid: %v", ErrConflict, err)
	}
	var parsed ConsumeReceipt
	if err := decodeStrictJSON(terminal.ReceiptJSON, &parsed); err != nil {
		return fmt.Errorf("%w: terminal receipt strict parse failed: %v", ErrConflict, err)
	}
	canonical, err := canonicalJSON(parsed)
	if err != nil || !reflect.DeepEqual(parsed, terminal.Receipt) || !reflect.DeepEqual(canonical, terminal.ReceiptJSON) {
		return fmt.Errorf("%w: terminal receipt bytes, parsed receipt, and stored receipt diverge", ErrConflict)
	}
	receipt := parsed
	if terminal.AuthorityID != reservation.AuthorityID || receipt.AuthorityID != reservation.AuthorityID ||
		receipt.FixtureID != reservation.FixtureID || receipt.RunID != reservation.RunID ||
		receipt.OperationKind != reservation.OperationKind || receipt.ResourceSelector != reservation.ResourceSelector ||
		receipt.ExpectedFenceDigest != reservation.ExpectedFenceDigest || receipt.EnvelopeDigest != reservation.EnvelopeDigest ||
		receipt.PayloadDigest != reservation.PayloadDigest || receipt.PredicateDigest != reservation.PredicateDigest ||
		receipt.ResolvedResourceID != reservation.ResolvedResourceID ||
		receipt.ResolvedHeadDigest != reservation.ResolvedHeadDigest ||
		receipt.ResolvedFenceDigest != reservation.ResolvedFenceDigest ||
		receipt.ResolutionDigest != reservation.ResolutionDigest ||
		receipt.AdapterInvocationID != reservation.AdapterInvocationID ||
		receipt.ReservedAt != formatCanonicalTime(reservation.ReservedAt) ||
		receipt.ResultID != terminal.ResultID || receipt.Outcome != terminal.Outcome ||
		receipt.AdapterResultDigest != terminal.AdapterResultDigest ||
		receipt.ObservedHeadDigest != terminal.ObservedHeadDigest ||
		receipt.ObservedFenceDigest != terminal.ObservedFenceDigest ||
		receipt.CompletedAt != formatCanonicalTime(terminal.CompletedAt) {
		return fmt.Errorf("%w: terminal receipt does not close every reservation and result field", ErrConflict)
	}
	return nil
}

func reservationsEqual(left, right Reservation) bool {
	return reservationBindingsEqual(left, right) && left.AdapterInvocationID == right.AdapterInvocationID &&
		left.ReservedAt.Equal(right.ReservedAt)
}

func reservationBindingsEqual(left, right Reservation) bool {
	if left.AuthorityID != right.AuthorityID || left.FixtureID != right.FixtureID || left.RunID != right.RunID ||
		left.OperationKind != right.OperationKind || left.ResourceSelector != right.ResourceSelector ||
		left.ExpectedFenceDigest != right.ExpectedFenceDigest || left.EnvelopeDigest != right.EnvelopeDigest ||
		left.PayloadDigest != right.PayloadDigest || left.PredicateDigest != right.PredicateDigest ||
		!left.AuthorityIssuedAt.Equal(right.AuthorityIssuedAt) || !left.AuthorityExpiresAt.Equal(right.AuthorityExpiresAt) ||
		left.ResolvedResourceID != right.ResolvedResourceID || left.ResolvedHeadDigest != right.ResolvedHeadDigest ||
		left.ResolvedFenceDigest != right.ResolvedFenceDigest || left.ResolutionDigest != right.ResolutionDigest ||
		len(left.SignerIdentities) != len(right.SignerIdentities) {
		return false
	}
	for index := range left.SignerIdentities {
		if left.SignerIdentities[index] != right.SignerIdentities[index] {
			return false
		}
	}
	return true
}

func safeResolve(ctx context.Context, adapter Adapter, authority VerifiedAuthority) (resolution ResourceResolution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("adapter resolver panicked")
		}
	}()
	return adapter.Resolve(ctx, authority)
}

func safeExecute(ctx context.Context, adapter Adapter, request AdapterRequest) (result AdapterResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("adapter execution panicked")
		}
	}()
	return adapter.Execute(ctx, request)
}

func interfaceIsNil(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
