package qualificationhandoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/qualificationpromotionv2"
)

const commitUnknownInspectTimeout = 5 * time.Second

// Service owns commit-unknown recovery. It never allocates a replacement
// identity and never reconstructs authority from caller-supplied facts.
type Service struct {
	store AtomicStore
}

func NewService(store AtomicStore) (*Service, error) {
	if isNilInterface(store) {
		return nil, invalid("service.store", "atomic store is required")
	}
	return &Service{store: store}, nil
}

func (service *Service) Complete(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(service.store) || isNilInterface(ctx) {
		return Record{}, invalid("service", "service, atomic store, and context are required")
	}
	if err := ValidateHandoffID(handoffID); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := service.store.Complete(ctx, handoffID)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		// Commit acknowledgement loss is reconciled even if the command context
		// was cancelled while Commit was in flight. This bounded inspection uses
		// the same opaque ID; a not-found result remains outcome-unknown and never
		// causes allocation or a second completion identity.
		reconcile, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitUnknownInspectTimeout)
		defer cancel()
		resolved, inspectErr := service.store.Inspect(reconcile, handoffID)
		if inspectErr != nil {
			return Record{}, ErrOutcomeUnknown
		}
		if err := validateReturnedRecord(handoffID, resolved); err != nil {
			return Record{}, err
		}
		resolved.Idempotent = true
		return cloneRecord(resolved), nil
	}
	if err != nil {
		return Record{}, sanitizeCompleteError(err)
	}
	if err := validateReturnedRecord(handoffID, record); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

func (service *Service) Inspect(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(service.store) || isNilInterface(ctx) || !validUUIDv4Value(handoffID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := service.store.Inspect(ctx, handoffID)
	if err != nil {
		return Record{}, sanitizeInspectError(err)
	}
	if err := validateReturnedRecord(handoffID, record); err != nil {
		return Record{}, err
	}
	record.Idempotent = true
	return cloneRecord(record), nil
}

func validateReturnedRecord(handoffID uuid.UUID, record Record) error {
	if record.HandoffID != handoffID || record.Bundle.HandoffID != handoffID.String() {
		return fmt.Errorf("%w: database returned another handoff identity", ErrConflict)
	}
	if err := ValidateRecord(record); err != nil {
		return fmt.Errorf("%w: database returned a corrupt immutable completion", ErrConflict)
	}
	return nil
}

func sanitizeCompleteError(err error) error {
	if errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrStoreOutcomeUnknown) {
		return ErrOutcomeUnknown
	}
	for _, class := range []error{ErrInvalid, ErrNotFound, ErrNotReady, ErrConflict, ErrRetryable} {
		if errors.Is(err, class) {
			return class
		}
	}
	return ErrOutcomeUnknown
}

func sanitizeInspectError(err error) error {
	for _, class := range []error{ErrNotFound, ErrConflict} {
		if errors.Is(err, class) {
			return class
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrOutcomeUnknown
}

func cloneRecord(record Record) Record {
	encoded, err := json.Marshal(record.Bundle)
	if err != nil {
		return Record{}
	}
	var bundle CompletionBundle
	if err := json.Unmarshal(encoded, &bundle); err != nil {
		return Record{}
	}
	return Record{HandoffID: record.HandoffID, Bundle: bundle, Idempotent: record.Idempotent}
}

func SameImmutableRecord(left, right Record) bool {
	if left.HandoffID != right.HandoffID {
		return false
	}
	leftBytes, leftErr := qualificationpromotionv2.CanonicalJSON(left.Bundle)
	rightBytes, rightErr := qualificationpromotionv2.CanonicalJSON(right.Bundle)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
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
