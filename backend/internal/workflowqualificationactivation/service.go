package workflowqualificationactivation

import (
	"context"
	"errors"
	"time"
)

const commitUnknownInspectTimeout = 5 * time.Second

// Service is the only orchestration boundary admitted by the worker. It never
// accepts, derives, or replaces authority identities from delivery content.
type Service struct {
	resolver Resolver
	store    AtomicStore
}

func NewService(resolver Resolver, store AtomicStore) (*Service, error) {
	if isNilInterface(resolver) || isNilInterface(store) {
		return nil, errors.New("workflow qualification activation resolver and atomic store are required")
	}
	return &Service{resolver: resolver, store: store}, nil
}

func (service *Service) Activate(ctx context.Context, completionEventID CompletionEventID) (Record, error) {
	if service == nil || isNilInterface(service.resolver) || isNilInterface(service.store) || isNilInterface(ctx) || !completionEventID.valid() {
		return Record{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	resolution, err := service.resolver.Resolve(ctx, completionEventID)
	if err != nil {
		return Record{}, sanitizeResolverError(err)
	}
	if err := validateResolution(completionEventID, resolution); err != nil {
		return Record{}, sanitizeActivationError(err)
	}
	if resolution.Classification == ClassificationNonTarget {
		return ignoredRecord(completionEventID), nil
	}

	record, err := service.store.Activate(ctx, completionEventID, resolution.Candidate)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		// Commit acknowledgement loss is reconciled even if Commit consumed the
		// command deadline. Inspection uses the same resolver-owned identities
		// and cannot allocate or submit a second generation.
		reconcile, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitUnknownInspectTimeout)
		defer cancel()
		resolved, inspectErr := service.store.Inspect(reconcile, completionEventID, resolution.Candidate)
		if inspectErr != nil {
			return Record{}, ErrOutcomeUnknown
		}
		resolved.Idempotent = true
		if err := validateRecord(completionEventID, resolution.Candidate, resolved); err != nil {
			return Record{}, err
		}
		return resolved, nil
	}
	if err != nil {
		return Record{}, sanitizeActivationError(err)
	}
	if err := validateRecord(completionEventID, resolution.Candidate, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Inspect re-resolves the same immutable completion event and proves the
// exact committed activation. It is used by durable workers after ambiguous
// process or transport outcomes; a missing record is never treated as proof
// that allocating a new identity would be safe.
func (service *Service) Inspect(ctx context.Context, completionEventID CompletionEventID) (Record, error) {
	if service == nil || isNilInterface(service.resolver) || isNilInterface(service.store) || isNilInterface(ctx) || !completionEventID.valid() {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	resolution, err := service.resolver.Resolve(ctx, completionEventID)
	if err != nil {
		return Record{}, sanitizeResolverError(err)
	}
	if err := validateResolution(completionEventID, resolution); err != nil {
		return Record{}, sanitizeActivationError(err)
	}
	if resolution.Classification == ClassificationNonTarget {
		return ignoredRecord(completionEventID), nil
	}
	record, err := service.store.Inspect(ctx, completionEventID, resolution.Candidate)
	if err != nil {
		return Record{}, sanitizeInspectError(err)
	}
	record.Idempotent = true
	if err := validateRecord(completionEventID, resolution.Candidate, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func sanitizeResolverError(err error) error {
	for _, class := range []error{ErrInvalid, ErrNotFound, ErrNotReady, ErrConflict, ErrRetryable, ErrOutcomeUnknown} {
		if errors.Is(err, class) {
			return class
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrOutcomeUnknown
}

func sanitizeActivationError(err error) error {
	for _, class := range []error{ErrInvalid, ErrNotFound, ErrNotReady, ErrConflict, ErrRetryable, ErrOutcomeUnknown, ErrStoreOutcomeUnknown} {
		if errors.Is(err, class) {
			return class
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
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

var _ interface {
	Activate(context.Context, CompletionEventID) (Record, error)
	Inspect(context.Context, CompletionEventID) (Record, error)
} = (*Service)(nil)
