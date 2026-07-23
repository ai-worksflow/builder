package qualificationpromotionv2

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
)

const commitUnknownInspectTimeout = 5 * time.Second

// Service orchestrates only an atomic consume call and immutable inspections.
// It has no authority resolver dependency by design.
type Service struct {
	store AtomicStore
}

func NewService(store AtomicStore) (*Service, error) {
	if isNilInterface(store) {
		return nil, invalid("service", "atomic store is required")
	}
	return &Service{store: store}, nil
}

func (service *Service) Consume(ctx context.Context, command ConsumeCommand) (Record, error) {
	if service == nil || isNilInterface(service.store) || isNilInterface(ctx) {
		return Record{}, invalid("service", "service, atomic store, and context are required")
	}
	if err := ValidateCommand(command); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		// No store call has occurred, so this cancellation is known preflight
		// rather than a commit-unknown transport result.
		return Record{}, err
	}
	record, err := service.store.Consume(ctx, command)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		// Commit acknowledgement loss may cancel the command context while the
		// database is durably committing. Reconcile the exact operation through a
		// bounded context which retains values but not cancellation. A PostgreSQL
		// store has already poisoned the ambiguous physical session, so this read
		// is issued on a fresh primary connection.
		reconcile, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitUnknownInspectTimeout)
		defer cancel()
		resolved, inspectErr := service.store.InspectOperation(reconcile, command.OperationID)
		if inspectErr != nil {
			return Record{}, ErrOutcomeUnknown
		}
		if err := validateReturnedRecord(command, resolved); err != nil {
			return Record{}, err
		}
		resolved.Idempotent = true
		return cloneRecord(resolved), nil
	}
	if err != nil {
		return Record{}, sanitizeConsumeError(err)
	}
	if err := validateReturnedRecord(command, record); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

func (service *Service) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(service.store) || isNilInterface(ctx) || !validUUIDv4Value(operationID) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	record, err := service.store.InspectOperation(ctx, operationID)
	if err != nil {
		return Record{}, sanitizeInspectError(err)
	}
	if record.Command.OperationID != operationID {
		return Record{}, fmt.Errorf("%w: inspected operation returned another identity", ErrConflict)
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: inspected operation is corrupt", ErrConflict)
	}
	return cloneRecord(record), nil
}

func validateReturnedRecord(command ConsumeCommand, record Record) error {
	if record.Command != command {
		return fmt.Errorf("%w: atomic store returned an immutable result for another command", ErrConflict)
	}
	if err := ValidateRecord(record); err != nil {
		return fmt.Errorf("%w: atomic store returned a corrupt immutable aggregate", ErrConflict)
	}
	return nil
}

func sanitizeConsumeError(err error) error {
	// Outcome uncertainty always dominates a joined retryable classification;
	// a caller must never repeat an attempt whose commit status is ambiguous.
	if errors.Is(err, ErrOutcomeUnknown) {
		return ErrOutcomeUnknown
	}
	for _, class := range []error{ErrInvalid, ErrNotReady, ErrStale, ErrConflict, ErrRetryable} {
		if errors.Is(err, class) {
			return class
		}
	}
	// An unclassified failure returned after entering AtomicStore.Consume has
	// unknown commit status; never encourage a new operation allocation.
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
