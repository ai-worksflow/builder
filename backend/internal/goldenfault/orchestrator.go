package goldenfault

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const ConsumeRequestSchemaV1 = "worksflow-golden-fault-consume-request/v1"

var (
	// ErrTrustedAuthorityNotFound means that the immutable qualification
	// repository does not contain the requested authority. It is deliberately
	// distinct from the consume-ledger ErrNotFound result.
	ErrTrustedAuthorityNotFound = errors.New("trusted Golden fault authority not found")
	// ErrTrustedAuthorityUnavailable covers corrupt, drifting, or unavailable
	// server-owned authority material. Callers must not repair it with values
	// supplied in the HTTP request.
	ErrTrustedAuthorityUnavailable = errors.New("trusted Golden fault authority is unavailable")
	// ErrFaultCredentialForbidden is returned for every role or run-scope
	// mismatch. It intentionally does not disclose which claim drifted.
	ErrFaultCredentialForbidden = errors.New("Golden fault credential is not authorized for this authority")
)

// ConsumeCommand is the complete public mutation input. Operation, resource,
// fence, envelope, and digest fields are intentionally absent: they are loaded
// from TrustedAuthorityRepository.
type ConsumeCommand struct {
	AuthorityID uuid.UUID
	FixtureID   uuid.UUID
	RunID       uuid.UUID
}

type consumeRequestDocument struct {
	FixtureID     string `json:"fixtureId"`
	RunID         string `json:"runId"`
	SchemaVersion string `json:"schemaVersion"`
}

// DecodeConsumeRequest accepts exactly the three-field public request
// document. Duplicate, unknown, null, non-UTF-8, trailing, and non-canonical
// UUID values are rejected before authority lookup.
func DecodeConsumeRequest(input []byte, authorityID string) (ConsumeCommand, error) {
	if err := requireExactObject(input, map[string]valueKind{
		"fixtureId": valueString, "runId": valueString, "schemaVersion": valueString,
	}); err != nil {
		return ConsumeCommand{}, fmt.Errorf("%w: consume request shape: %v", ErrInvalidAuthority, err)
	}
	var document consumeRequestDocument
	if err := decodeStrictJSON(input, &document); err != nil {
		return ConsumeCommand{}, fmt.Errorf("%w: decode consume request: %v", ErrInvalidAuthority, err)
	}
	if document.SchemaVersion != ConsumeRequestSchemaV1 || !validUUID(authorityID) ||
		!validUUID(document.FixtureID) || !validUUID(document.RunID) {
		return ConsumeCommand{}, fmt.Errorf("%w: consume request schema or IDs are invalid", ErrInvalidAuthority)
	}
	return ConsumeCommand{
		AuthorityID: uuid.MustParse(authorityID), FixtureID: uuid.MustParse(document.FixtureID),
		RunID: uuid.MustParse(document.RunID),
	}, nil
}

// RunPrincipal is produced only after the platform credential verifier has
// authenticated an audience-bound, run-scoped Bearer credential. It contains
// identity claims, never the raw credential or a credential handle.
type RunPrincipal struct {
	ActorID   uuid.UUID
	Audience  string
	FixtureID uuid.UUID
	ProjectID uuid.UUID
	Role      string
	RunID     uuid.UUID
	TenantID  uuid.UUID
}

// TrustedAuthorityRecord is an immutable server-side projection of the
// approved Golden fixture and root trust policy. Implementations must return
// the exact historical record for AuthorityID; latest-by-name lookup is not a
// valid implementation.
type TrustedAuthorityRecord struct {
	AuthorityID          uuid.UUID
	Audience             string
	Envelope             []byte
	Expected             ExpectedBinding
	FaultOperatorActorID uuid.UUID
	ProjectID            uuid.UUID
	TenantID             uuid.UUID
	TrustPolicy          TrustPolicy
}

type TrustedAuthorityRepository interface {
	Load(context.Context, uuid.UUID) (TrustedAuthorityRecord, error)
}

// Consumer is the run-scoped orchestration boundary used by the HTTP
// transport. It owns only a closed typed adapter registry; no request value can
// select a shell command, URL, SQL statement, signal, or resource identifier.
type Consumer struct {
	adapters   map[OperationKind]Adapter
	ledger     Ledger
	repository TrustedAuthorityRepository
}

func NewConsumer(
	repository TrustedAuthorityRepository,
	ledger Ledger,
	adapters map[OperationKind]Adapter,
) (*Consumer, error) {
	if repository == nil || interfaceIsNil(repository) || ledger == nil || interfaceIsNil(ledger) {
		return nil, errors.New("trusted Golden fault authority repository and ledger are required")
	}
	registry := make(map[OperationKind]Adapter, len(adapters))
	for operation, adapter := range adapters {
		if _, allowed := selectorForOperation(operation); !allowed || adapter == nil || interfaceIsNil(adapter) {
			return nil, fmt.Errorf("%w: invalid adapter registration for %q", ErrAdapterMissing, operation)
		}
		registry[operation] = adapter
	}
	return &Consumer{repository: repository, ledger: ledger, adapters: registry}, nil
}

func (consumer *Consumer) Consume(
	ctx context.Context,
	principal RunPrincipal,
	command ConsumeCommand,
) (ConsumeRecord, error) {
	if consumer == nil || consumer.repository == nil || consumer.ledger == nil || ctx == nil || interfaceIsNil(ctx) {
		return ConsumeRecord{}, errors.New("Golden fault consumer and context are required")
	}
	if !validRunPrincipal(principal) {
		return ConsumeRecord{}, ErrFaultCredentialForbidden
	}
	if command.AuthorityID.Version() != 4 || command.FixtureID.Version() != 4 || command.RunID.Version() != 4 {
		return ConsumeRecord{}, fmt.Errorf("%w: consume command IDs must be canonical UUID-v4 values", ErrInvalidAuthority)
	}
	if principal.RunID != command.RunID || principal.FixtureID != command.FixtureID {
		return ConsumeRecord{}, ErrFaultCredentialForbidden
	}

	record, err := consumer.repository.Load(ctx, command.AuthorityID)
	if err != nil {
		if errors.Is(err, ErrTrustedAuthorityNotFound) {
			return ConsumeRecord{}, ErrTrustedAuthorityNotFound
		}
		return ConsumeRecord{}, fmt.Errorf("%w: repository read failed", ErrTrustedAuthorityUnavailable)
	}
	// Copy the envelope before verification so a repository implementation
	// cannot mutate aliased bytes after returning the immutable projection.
	record.Envelope = bytes.Clone(record.Envelope)
	if err := validateTrustedAuthorityRecord(record, command.AuthorityID); err != nil {
		return ConsumeRecord{}, fmt.Errorf("%w: %v", ErrTrustedAuthorityUnavailable, err)
	}
	if principal.ActorID != record.FaultOperatorActorID || principal.TenantID != record.TenantID ||
		principal.ProjectID != record.ProjectID || principal.Audience != record.Audience ||
		principal.RunID != record.Expected.RunID || principal.FixtureID != record.Expected.FixtureID ||
		command.RunID != record.Expected.RunID || command.FixtureID != record.Expected.FixtureID {
		return ConsumeRecord{}, ErrFaultCredentialForbidden
	}

	verifier, err := NewVerifier(record.TrustPolicy)
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("%w: trust policy is invalid", ErrTrustedAuthorityUnavailable)
	}
	service, err := NewService(verifier, consumer.ledger, consumer.adapters)
	if err != nil {
		return ConsumeRecord{}, fmt.Errorf("%w: fault service is not configured", ErrTrustedAuthorityUnavailable)
	}
	return service.Consume(ctx, record.Envelope, record.Expected)
}

// CanonicalConsumeReceipt returns the exact immutable receipt bytes after
// revalidating their complete reservation/result closure. HTTP code must write
// these bytes directly rather than re-marshalling the receipt.
func CanonicalConsumeReceipt(record ConsumeRecord) ([]byte, error) {
	if record.State != ConsumeStateTerminal || record.Terminal == nil {
		return nil, fmt.Errorf("%w: consume record has no terminal receipt", ErrOutcomeUnknown)
	}
	if err := validateReservation(record.Reservation); err != nil {
		return nil, err
	}
	if err := validateTerminalClosure(*record.Terminal, record.Reservation); err != nil {
		return nil, err
	}
	return bytes.Clone(record.Terminal.ReceiptJSON), nil
}

func validateTrustedAuthorityRecord(record TrustedAuthorityRecord, requested uuid.UUID) error {
	if record.AuthorityID != requested || record.Expected.AuthorityID != requested ||
		record.FaultOperatorActorID.Version() != 4 || record.TenantID.Version() != 4 ||
		record.ProjectID.Version() != 4 || !validIdentity(record.Audience) ||
		len(record.Envelope) == 0 || sha256Digest(record.Envelope) != record.Expected.EnvelopeDigest {
		return errors.New("authority identity, scope, audience, or envelope binding is invalid")
	}
	if err := validateExpectedBinding(record.Expected); err != nil {
		return err
	}
	return nil
}

func validRunPrincipal(principal RunPrincipal) bool {
	return principal.Role == FaultOperatorRole && principal.ActorID.Version() == 4 &&
		principal.TenantID.Version() == 4 && principal.ProjectID.Version() == 4 &&
		principal.RunID.Version() == 4 && principal.FixtureID.Version() == 4 &&
		validIdentity(principal.Audience)
}

var _ interface {
	Consume(context.Context, RunPrincipal, ConsumeCommand) (ConsumeRecord, error)
} = (*Consumer)(nil)
