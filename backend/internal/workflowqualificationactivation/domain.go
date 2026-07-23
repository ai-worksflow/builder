// Package workflowqualificationactivation owns the durable boundary that turns
// one committed workflow node.completed event into an immutable Workflow Input
// Authority and the matching external-qualification gate activation.
//
// The only identity admitted from the delivery plane is CompletionEventID.
// Every project, run, node, authority, operation, activation-event, material,
// and hash fact is resolved by a server-side Resolver and is revalidated by
// Workflow Input Authority inside the activation transaction.
package workflowqualificationactivation

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/workflowinputauthority"
)

const maximumSafeInteger = int64(9007199254740991)

var (
	ErrInvalid             = errors.New("workflow qualification activation is invalid")
	ErrNotFound            = errors.New("workflow qualification activation source is not found")
	ErrNotReady            = errors.New("workflow qualification activation prerequisites are not ready")
	ErrConflict            = errors.New("workflow qualification activation conflicts with immutable state")
	ErrRetryable           = errors.New("workflow qualification activation encountered retryable database contention")
	ErrStoreOutcomeUnknown = errors.New("workflow qualification activation store commit outcome is unknown")
	ErrOutcomeUnknown      = errors.New("workflow qualification activation outcome is unknown; inspect the same completion event")
)

// CompletionEventID is deliberately opaque. It can only be constructed by
// parsing one canonical UUIDv4 and exposes no way to replace the identity with
// caller-authored workflow facts.
type CompletionEventID struct {
	value uuid.UUID
}

func ParseCompletionEventID(value string) (CompletionEventID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 || parsed == uuid.Nil {
		return CompletionEventID{}, fmt.Errorf("%w: completionEventId must be a canonical UUIDv4", ErrInvalid)
	}
	return CompletionEventID{value: parsed}, nil
}

func (id CompletionEventID) String() string {
	if id.value == uuid.Nil {
		return ""
	}
	return id.value.String()
}

func (id CompletionEventID) valid() bool {
	return id.value != uuid.Nil && id.value.Version() == 4 && id.value.Variant() == uuid.RFC4122 && id.value.String() == id.String()
}

type Classification string

const (
	ClassificationTarget    Classification = "target"
	ClassificationNonTarget Classification = "non-target"
)

// Resolution is transferred by value from a trusted, server-side Resolver.
// A non-target result must carry no Candidate. A target result must carry the
// complete Candidate, including stable operation, authority, and activation
// event identities allocated before delivery.
type Resolution struct {
	Classification Classification
	Candidate      workflowinputauthority.Candidate
}

// Resolver is intentionally only an interface in this package. Production
// wiring must provide a resolver that reads immutable Quality completion and
// precommit facts and fails closed. There is no fallback resolver that derives
// authority from the broker payload or mutable client input.
type Resolver interface {
	Resolve(context.Context, CompletionEventID) (Resolution, error)
}

type Disposition string

const (
	DispositionActivated Disposition = "activated"
	DispositionIgnored   Disposition = "ignored-non-target"
)

// Record is the non-secret immutable activation projection. Authority bytes
// remain owned by workflowinputauthority; these hashes and identities are
// sufficient for worker acknowledgement and exact replay comparison.
type Record struct {
	CompletionEventID       CompletionEventID
	Disposition             Disposition
	OperationID             uuid.UUID
	AuthorityID             uuid.UUID
	WorkflowRunID           uuid.UUID
	NodeRunID               uuid.UUID
	ActivationEventID       uuid.UUID
	ActivationEventSequence int64
	RequestHash             string
	TargetHash              string
	InputHash               string
	AuthorityHash           string
	Idempotent              bool
}

// AtomicStore owns the complete PostgreSQL transaction. Activate must call
// Workflow Input Authority Freeze before any other database authority entry;
// Inspect must prove the same immutable authority and complete workflow/event/
// outbox closure on a fresh read-write primary.
type AtomicStore interface {
	Activate(context.Context, CompletionEventID, workflowinputauthority.Candidate) (Record, error)
	Inspect(context.Context, CompletionEventID, workflowinputauthority.Candidate) (Record, error)
}

func validateResolution(completionEventID CompletionEventID, resolution Resolution) error {
	if !completionEventID.valid() {
		return fmt.Errorf("%w: completionEventId is invalid", ErrInvalid)
	}
	switch resolution.Classification {
	case ClassificationNonTarget:
		if !reflect.DeepEqual(resolution.Candidate, workflowinputauthority.Candidate{}) {
			return fmt.Errorf("%w: a non-target resolution must not carry workflow authority facts", ErrConflict)
		}
		return nil
	case ClassificationTarget:
		compiled, err := workflowinputauthority.Compile(resolution.Candidate)
		if err != nil {
			return errors.Join(ErrInvalid, err)
		}
		if completionEventID.String() == compiled.OperationID.String() ||
			completionEventID.String() == compiled.AuthorityID.String() ||
			completionEventID.String() == compiled.Input.Gate.ActivationEventID ||
			completionEventID.String() == compiled.WorkflowRunID.String() ||
			completionEventID.String() == compiled.NodeRunID.String() {
			return fmt.Errorf("%w: completion event identity is reused by another authority role", ErrConflict)
		}
		return nil
	default:
		return fmt.Errorf("%w: resolver classification is not closed", ErrInvalid)
	}
}

func recordFromAuthority(completionEventID CompletionEventID, authority workflowinputauthority.Record, idempotent bool) Record {
	activationEventID, _ := uuid.Parse(authority.Input.Gate.ActivationEventID)
	return Record{
		CompletionEventID: completionEventID,
		Disposition:       DispositionActivated,
		OperationID:       authority.OperationID, AuthorityID: authority.AuthorityID,
		WorkflowRunID: authority.WorkflowRunID, NodeRunID: authority.NodeRunID,
		ActivationEventID:       activationEventID,
		ActivationEventSequence: authority.Input.Gate.ActivationEventSequence,
		RequestHash:             authority.RequestHash, TargetHash: authority.TargetHash,
		InputHash: authority.InputHash, AuthorityHash: authority.AuthorityHash,
		Idempotent: idempotent,
	}
}

func validateRecord(completionEventID CompletionEventID, candidate workflowinputauthority.Candidate, record Record) error {
	if !completionEventID.valid() || record.CompletionEventID != completionEventID || record.Disposition != DispositionActivated {
		return fmt.Errorf("%w: store returned another completion event or disposition", ErrConflict)
	}
	expected, err := workflowinputauthority.Compile(candidate)
	if err != nil {
		return errors.Join(ErrInvalid, err)
	}
	want := recordFromAuthority(completionEventID, expected, record.Idempotent)
	if record != want || record.ActivationEventSequence < 1 || record.ActivationEventSequence > maximumSafeInteger {
		return fmt.Errorf("%w: store returned a different immutable activation", ErrConflict)
	}
	return nil
}

func ignoredRecord(completionEventID CompletionEventID) Record {
	return Record{CompletionEventID: completionEventID, Disposition: DispositionIgnored, Idempotent: true}
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
