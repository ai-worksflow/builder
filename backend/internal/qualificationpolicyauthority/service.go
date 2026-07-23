package qualificationpolicyauthority

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Service struct {
	clock  DatabaseClock
	source PolicySource
	store  Store
}

func NewService(source PolicySource, store Store, clock DatabaseClock) (*Service, error) {
	if isNilInterface(source) || isNilInterface(store) || isNilInterface(clock) {
		return nil, invalid("service", "trusted policy source, immutable store, and database clock are required")
	}
	return &Service{clock: clock, source: source, store: store}, nil
}

// Issue inspects the operation before resolving its opaque source so exact
// replay remains possible after a reviewed source is retired. Generation and
// previous hash are derived from the current head, and Store performs the final
// atomic compare-and-swap append.
func (service *Service) Issue(ctx context.Context, command IssueCommand) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.source) ||
		isNilInterface(service.store) || isNilInterface(service.clock) {
		return Record{}, invalid("service", "service or dependencies are incomplete")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := validateIssueCommand(command); err != nil {
		return Record{}, err
	}

	existing, inspectErr := service.store.InspectOperation(ctx, command.OperationID)
	if inspectErr == nil {
		return replayRecord(existing, command)
	}
	if !errors.Is(inspectErr, ErrNotFound) {
		return Record{}, fmt.Errorf("inspect qualification policy operation: %w", inspectErr)
	}

	resolved, err := service.source.Resolve(ctx, command.PolicySourceID)
	if err != nil {
		return Record{}, fmt.Errorf("resolve trusted qualification policy source: %w", err)
	}
	resolved = cloneResolvedPolicy(resolved)
	if err := ValidateResolvedPolicy(resolved); err != nil {
		return Record{}, err
	}

	generation := int64(1)
	var previousHash *string
	current, currentErr := service.store.ResolveCurrent(ctx, resolved.ProjectID, resolved.ExecutionProfile)
	switch {
	case currentErr == nil:
		if err := ValidateRecord(current); err != nil {
			return Record{}, wrapStoredConflict("current policy", err)
		}
		if !headMatchesRecord(resolved.ProjectID, resolved.ExecutionProfile, current) {
			return Record{}, fmt.Errorf("%w: current policy is outside the resolved project/profile", ErrConflict)
		}
		if command.ExpectedPreviousAuthorityHash != current.AuthorityHash {
			if concurrent, reconcileErr := service.store.InspectOperation(ctx, command.OperationID); reconcileErr == nil {
				return replayRecord(concurrent, command)
			}
			return Record{}, fmt.Errorf("%w: expected previous authority is not the current head", ErrConflict)
		}
		if current.Document.Generation >= MaximumJavaScriptSafeInteger {
			return Record{}, fmt.Errorf("%w: policy generation is exhausted", ErrConflict)
		}
		generation = current.Document.Generation + 1
		previousHash = cloneStringPointer(&current.AuthorityHash)
	case errors.Is(currentErr, ErrNotFound):
		if command.ExpectedPreviousAuthorityHash != "" {
			if concurrent, reconcileErr := service.store.InspectOperation(ctx, command.OperationID); reconcileErr == nil {
				return replayRecord(concurrent, command)
			}
			return Record{}, fmt.Errorf("%w: expected previous authority does not exist", ErrConflict)
		}
	default:
		return Record{}, fmt.Errorf("resolve current qualification policy: %w", currentErr)
	}

	issuedAt, err := service.clock.Now(ctx)
	if err != nil {
		return Record{}, fmt.Errorf("read qualification policy database time: %w", err)
	}
	candidate, err := compileRecord(command, resolved, generation, previousHash, issuedAt)
	if err != nil {
		return Record{}, err
	}
	stored, err := service.store.Issue(ctx, candidate)
	if errors.Is(err, ErrStoreOutcomeUnknown) {
		return service.recoverUnknownOutcome(ctx, command, candidate)
	}
	if err != nil {
		// A concurrent exact operation may have committed between the initial
		// inspection and append. Reconcile only this operation identity.
		if errors.Is(err, ErrConflict) {
			currentOperation, reconcileErr := service.store.InspectOperation(ctx, command.OperationID)
			if reconcileErr == nil {
				return replayRecord(currentOperation, command)
			}
		}
		return Record{}, err
	}
	if err := ValidateRecord(stored); err != nil {
		return Record{}, wrapStoredConflict("issued policy", err)
	}
	if stored.Idempotent {
		return replayRecord(stored, command)
	}
	if !sameImmutableRecord(stored, candidate) {
		return Record{}, fmt.Errorf("%w: store returned different immutable authority bytes", ErrConflict)
	}
	return cloneRecord(stored), nil
}

func (service *Service) recoverUnknownOutcome(ctx context.Context, command IssueCommand, candidate Record) (Record, error) {
	recovered, err := service.store.InspectOperation(ctx, command.OperationID)
	if err != nil {
		return Record{}, ErrOutcomeUnknown
	}
	replayed, err := replayRecord(recovered, command)
	if err != nil {
		return Record{}, err
	}
	if !sameImmutableRecord(recovered, candidate) {
		return Record{}, fmt.Errorf("%w: uncertain issue resolved to different canonical bytes", ErrConflict)
	}
	replayed.Idempotent = true
	return replayed, nil
}

func (service *Service) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || operationID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	record, err := service.store.InspectOperation(ctx, operationID)
	if err != nil {
		return Record{}, err
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, wrapStoredConflict("operation", err)
	}
	return cloneRecord(record), nil
}

func (service *Service) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	record, err := service.store.ResolveAuthority(ctx, authorityID)
	if err != nil {
		return Record{}, err
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, wrapStoredConflict("authority", err)
	}
	if record.Command.AuthorityID != authorityID {
		return Record{}, fmt.Errorf("%w: opaque authority ID resolved to a different record", ErrConflict)
	}
	return cloneRecord(record), nil
}

// ResolveCurrent is a diagnostic projection. WIA Freeze and Promotion must use
// a transaction-scoped production implementation of AssertCurrent instead.
func (service *Service) ResolveCurrent(ctx context.Context, projectID uuid.UUID, profile ExecutionProfileBinding) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || projectID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	record, err := service.store.ResolveCurrent(ctx, projectID, profile)
	if err != nil {
		return Record{}, err
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, wrapStoredConflict("current policy", err)
	}
	if !headMatchesRecord(projectID, profile, record) {
		return Record{}, fmt.Errorf("%w: current policy projection is outside the requested scope", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (service *Service) AssertCurrent(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if service == nil || isNilInterface(ctx) || isNilInterface(service.store) || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	record, err := service.store.AssertCurrent(ctx, authorityID)
	if err != nil {
		return Record{}, err
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, wrapStoredConflict("current policy", err)
	}
	if record.Command.AuthorityID != authorityID || record.Document.Status != AuthorityStatusActive {
		return Record{}, ErrStale
	}
	return cloneRecord(record), nil
}

func replayRecord(record Record, command IssueCommand) (Record, error) {
	if err := ValidateRecord(record); err != nil {
		return Record{}, wrapStoredConflict("operation replay", err)
	}
	if !sameIssueCommand(record.Command, command) {
		return Record{}, fmt.Errorf("%w: operation is bound to a different issue command", ErrConflict)
	}
	return idempotentClone(record), nil
}
