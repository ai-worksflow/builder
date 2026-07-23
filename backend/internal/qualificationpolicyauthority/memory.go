package qualificationpolicyauthority

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

type policyHeadKey struct {
	projectID      uuid.UUID
	profileHash    string
	profileVersion string
}

type reservedIdentity struct {
	authorityID uuid.UUID
	role        string
}

// MemoryStore is a concurrency-safe semantic reference for append-only
// generations, operation replay, compare-and-swap head updates, suspension,
// and immutable cloning. It is not a substitute for PostgreSQL locks, FKs,
// deferred closure triggers, ACLs, or transaction-scoped AssertCurrent.
type MemoryStore struct {
	mu sync.RWMutex

	byOperation  map[uuid.UUID]uuid.UUID
	byAuthority  map[uuid.UUID]Record
	byHash       map[string]uuid.UUID
	heads        map[policyHeadKey]uuid.UUID
	reservations map[uuid.UUID]reservedIdentity
	references   map[uuid.UUID]string

	unknownAfterCommitOnce bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byOperation:  make(map[uuid.UUID]uuid.UUID),
		byAuthority:  make(map[uuid.UUID]Record),
		byHash:       make(map[string]uuid.UUID),
		heads:        make(map[policyHeadKey]uuid.UUID),
		reservations: make(map[uuid.UUID]reservedIdentity),
		references:   make(map[uuid.UUID]string),
	}
}

func (store *MemoryStore) Issue(ctx context.Context, candidate Record) (Record, error) {
	if store == nil || isNilInterface(ctx) {
		return Record{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if candidate.Idempotent {
		return Record{}, invalid("store", "candidate cannot author response idempotency metadata")
	}
	if err := ValidateRecord(candidate); err != nil {
		return Record{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if authorityID, exists := store.byOperation[candidate.Command.OperationID]; exists {
		existing, err := store.validatedCloneLocked(authorityID)
		if err != nil {
			return Record{}, err
		}
		if !sameIssueCommand(existing.Command, candidate.Command) {
			return Record{}, fmt.Errorf("%w: operation ID is bound to a different issue command", ErrConflict)
		}
		return idempotentClone(existing), nil
	}
	if _, exists := store.byAuthority[candidate.Command.AuthorityID]; exists {
		return Record{}, fmt.Errorf("%w: authority ID is already frozen", ErrConflict)
	}
	for identity, role := range map[uuid.UUID]string{
		candidate.Command.OperationID: "operation",
		candidate.Command.AuthorityID: "authority",
	} {
		if reservation, exists := store.reservations[identity]; exists {
			return Record{}, fmt.Errorf(
				"%w: %s identity is already reserved as %s for authority %s",
				ErrConflict,
				role,
				reservation.role,
				reservation.authorityID,
			)
		}
		if referenceRole, exists := store.references[identity]; exists {
			return Record{}, fmt.Errorf("%w: %s identity is already retained as an embedded %s reference", ErrConflict, role, referenceRole)
		}
	}
	for _, reference := range embeddedUUIDReferences(candidate.Document) {
		if reservation, exists := store.reservations[reference.id]; exists {
			return Record{}, fmt.Errorf(
				"%w: embedded %s reference is already reserved as %s for authority %s",
				ErrConflict,
				reference.role,
				reservation.role,
				reservation.authorityID,
			)
		}
	}
	if owner, exists := store.byHash[candidate.AuthorityHash]; exists {
		return Record{}, fmt.Errorf("%w: authority hash is already bound to %s", ErrConflict, owner)
	}

	projectID := uuid.MustParse(candidate.Document.ProjectID)
	key := policyHeadKey{
		projectID:      projectID,
		profileHash:    candidate.Document.ExecutionProfile.Hash,
		profileVersion: candidate.Document.ExecutionProfile.Version,
	}
	currentID, hasCurrent := store.heads[key]
	if !hasCurrent {
		if candidate.Document.Generation != 1 || candidate.Document.PreviousAuthorityHash != nil ||
			candidate.Command.ExpectedPreviousAuthorityHash != "" {
			return Record{}, fmt.Errorf("%w: first generation compare-and-swap cursor is not empty", ErrConflict)
		}
	} else {
		current, err := store.validatedCloneLocked(currentID)
		if err != nil {
			return Record{}, err
		}
		if candidate.Command.ExpectedPreviousAuthorityHash != current.AuthorityHash ||
			candidate.Document.PreviousAuthorityHash == nil || *candidate.Document.PreviousAuthorityHash != current.AuthorityHash ||
			candidate.Document.Generation != current.Document.Generation+1 {
			return Record{}, fmt.Errorf("%w: current policy head changed", ErrConflict)
		}
		if candidate.IssuedAt.Before(current.IssuedAt) {
			return Record{}, fmt.Errorf("%w: database authority time moved backwards", ErrConflict)
		}
	}

	stored := cloneRecord(candidate)
	stored.Idempotent = false
	store.byOperation[stored.Command.OperationID] = stored.Command.AuthorityID
	store.byAuthority[stored.Command.AuthorityID] = cloneRecord(stored)
	store.byHash[stored.AuthorityHash] = stored.Command.AuthorityID
	store.heads[key] = stored.Command.AuthorityID
	store.reservations[stored.Command.OperationID] = reservedIdentity{authorityID: stored.Command.AuthorityID, role: "operation"}
	store.reservations[stored.Command.AuthorityID] = reservedIdentity{authorityID: stored.Command.AuthorityID, role: "authority"}
	for _, reference := range embeddedUUIDReferences(stored.Document) {
		if _, exists := store.references[reference.id]; !exists {
			store.references[reference.id] = reference.role
		}
	}
	if store.unknownAfterCommitOnce {
		store.unknownAfterCommitOnce = false
		return Record{}, ErrStoreOutcomeUnknown
	}
	return cloneRecord(stored), nil
}

func (store *MemoryStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store == nil || isNilInterface(ctx) || operationID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	authorityID, exists := store.byOperation[operationID]
	if !exists {
		return Record{}, ErrNotFound
	}
	return store.validatedCloneLocked(authorityID)
}

func (store *MemoryStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || isNilInterface(ctx) || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.validatedCloneLocked(authorityID)
}

func (store *MemoryStore) ResolveCurrent(ctx context.Context, projectID uuid.UUID, profile ExecutionProfileBinding) (Record, error) {
	if store == nil || isNilInterface(ctx) || projectID.Version() != 4 ||
		profile.Version != ExecutionProfileV3 || !validDigest(profile.Hash) {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	authorityID, exists := store.heads[policyHeadKey{
		projectID: projectID, profileHash: profile.Hash, profileVersion: profile.Version,
	}]
	if !exists {
		return Record{}, ErrNotFound
	}
	record, err := store.validatedCloneLocked(authorityID)
	if err != nil {
		return Record{}, err
	}
	if !headMatchesRecord(projectID, profile, record) {
		return Record{}, fmt.Errorf("%w: current-head index points to a different policy scope", ErrConflict)
	}
	return record, nil
}

func (store *MemoryStore) AssertCurrent(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || isNilInterface(ctx) || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, err := store.validatedCloneLocked(authorityID)
	if err != nil {
		return Record{}, err
	}
	projectID := uuid.MustParse(record.Document.ProjectID)
	key := policyHeadKey{
		projectID: projectID, profileHash: record.Document.ExecutionProfile.Hash,
		profileVersion: record.Document.ExecutionProfile.Version,
	}
	if store.heads[key] != authorityID || record.Document.Status != AuthorityStatusActive {
		return Record{}, ErrStale
	}
	return record, nil
}

// InjectCommitUnknownOnce is a test-only fault. The next successful append is
// committed atomically and then reports an unknown result to exercise recovery.
func (store *MemoryStore) InjectCommitUnknownOnce() {
	if store == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.unknownAfterCommitOnce = true
}

// validatedCloneLocked requires store.mu to be held for reading or writing.
func (store *MemoryStore) validatedCloneLocked(authorityID uuid.UUID) (Record, error) {
	record, exists := store.byAuthority[authorityID]
	if !exists {
		return Record{}, ErrNotFound
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, wrapStoredConflict("authority", err)
	}
	return cloneRecord(record), nil
}

var _ Store = (*MemoryStore)(nil)
