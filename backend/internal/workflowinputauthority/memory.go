package workflowinputauthority

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type nodeIdentity struct {
	run  uuid.UUID
	node uuid.UUID
}

type identityReservation struct {
	authority uuid.UUID
	role      string
}

// MemoryStore is a concurrency-safe semantic reference for immutable identity
// ownership, exact idempotency, node convergence, cloning, and stale reads. It
// is not a production activation transaction or a substitute for PostgreSQL
// locks, FKs, deferred triggers, ACLs, or current-pointer checks.
type MemoryStore struct {
	mu sync.RWMutex

	clock func() time.Time

	byAuthority  map[uuid.UUID]Record
	byNode       map[nodeIdentity]uuid.UUID
	byOperation  map[uuid.UUID]uuid.UUID
	reservations map[uuid.UUID]identityReservation
	stale        map[uuid.UUID]bool
}

// NewMemoryStore accepts an optional deterministic clock for focused tests.
func NewMemoryStore(clocks ...func() time.Time) *MemoryStore {
	clock := time.Now
	if len(clocks) == 1 && clocks[0] != nil {
		clock = clocks[0]
	}
	return &MemoryStore{
		clock: clock, byAuthority: map[uuid.UUID]Record{}, byNode: map[nodeIdentity]uuid.UUID{},
		byOperation: map[uuid.UUID]uuid.UUID{}, reservations: map[uuid.UUID]identityReservation{},
		stale: map[uuid.UUID]bool{},
	}
}

func (store *MemoryStore) Freeze(ctx context.Context, transaction Transaction, candidate Candidate) (Record, error) {
	if store == nil || ctx == nil {
		return Record{}, invalid("store", "memory store and context are required")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if _, ok := transaction.(MemoryTransaction); !ok {
		return Record{}, invalid("store.transaction", "nil, typed-nil, or unknown transaction")
	}
	proposed, err := Compile(candidate)
	if err != nil {
		return Record{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if authorityID, exists := store.byOperation[proposed.OperationID]; exists {
		existing := store.byAuthority[authorityID]
		if !sameImmutableRecord(existing, proposed) {
			return Record{}, fmt.Errorf("%w: operation identity is already bound to different canonical bytes", ErrConflict)
		}
		return idempotentClone(existing), nil
	}
	if reservation, exists := store.reservations[proposed.OperationID]; exists {
		return Record{}, fmt.Errorf("%w: operation identity is reserved as %s for %s", ErrConflict, reservation.role, reservation.authority)
	}
	if reservation, exists := store.reservations[proposed.AuthorityID]; exists {
		return Record{}, fmt.Errorf("%w: authority identity is reserved as %s for %s", ErrConflict, reservation.role, reservation.authority)
	}
	activationID := mustUUID(proposed.Input.Gate.ActivationEventID)
	if reservation, exists := store.reservations[activationID]; exists {
		return Record{}, fmt.Errorf("%w: activation event identity is reserved as %s for %s", ErrConflict, reservation.role, reservation.authority)
	}
	if existing, exists := store.byAuthority[proposed.AuthorityID]; exists {
		if !sameImmutableRecord(existing, proposed) {
			return Record{}, fmt.Errorf("%w: authority identity is already bound to different canonical bytes", ErrConflict)
		}
		return idempotentClone(existing), nil
	}
	node := nodeIdentity{run: proposed.WorkflowRunID, node: proposed.NodeRunID}
	if authorityID, exists := store.byNode[node]; exists {
		return Record{}, fmt.Errorf("%w: workflow node is already bound to authority %s under a different operation", ErrConflict, authorityID)
	}

	frozenAt := store.clock().UTC().Truncate(time.Millisecond)
	if frozenAt.Year() < 1678 || frozenAt.Year() >= 2262 {
		return Record{}, invalid("store.clock", "timestamp is outside the authority range")
	}
	stored := cloneRecord(proposed)
	stored.FrozenAt = frozenAt
	if err := ValidateRecord(stored); err != nil {
		return Record{}, err
	}
	store.byAuthority[stored.AuthorityID] = cloneRecord(stored)
	store.byOperation[stored.OperationID] = stored.AuthorityID
	store.byNode[node] = stored.AuthorityID
	store.reservations[stored.OperationID] = identityReservation{authority: stored.AuthorityID, role: "operation"}
	store.reservations[stored.AuthorityID] = identityReservation{authority: stored.AuthorityID, role: "authority"}
	store.reservations[activationID] = identityReservation{authority: stored.AuthorityID, role: "activation-event"}
	store.stale[stored.AuthorityID] = false
	return cloneRecord(stored), nil
}

func (store *MemoryStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || operationID.Version() != 4 {
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
	return store.validatedClone(authorityID)
}

func (store *MemoryStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.validatedClone(authorityID)
}

func (store *MemoryStore) ResolveNode(ctx context.Context, workflowRunID, nodeRunID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || workflowRunID.Version() != 4 || nodeRunID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	authorityID, exists := store.byNode[nodeIdentity{run: workflowRunID, node: nodeRunID}]
	if !exists {
		return Record{}, ErrNotFound
	}
	return store.validatedClone(authorityID)
}

func (store *MemoryStore) AssertCurrent(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, exists := store.byAuthority[authorityID]; !exists {
		return Record{}, ErrNotFound
	}
	if store.stale[authorityID] {
		return Record{}, ErrStale
	}
	return store.validatedClone(authorityID)
}

// MarkStale lets semantic tests model a Promotion-time currency failure. A
// production implementation derives staleness from locked mutable pointers;
// it never trusts this memory-only flag.
func (store *MemoryStore) MarkStale(authorityID uuid.UUID) error {
	if store == nil || authorityID.Version() != 4 {
		return ErrNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.byAuthority[authorityID]; !exists {
		return ErrNotFound
	}
	store.stale[authorityID] = true
	return nil
}

// validatedClone requires the caller to hold at least store.mu.RLock.
func (store *MemoryStore) validatedClone(authorityID uuid.UUID) (Record, error) {
	record, exists := store.byAuthority[authorityID]
	if !exists {
		return Record{}, ErrNotFound
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: stored authority failed independent validation: %v", ErrConflict, err)
	}
	return cloneRecord(record), nil
}

func idempotentClone(record Record) Record {
	clone := cloneRecord(record)
	clone.Idempotent = true
	return clone
}

var _ Store = (*MemoryStore)(nil)
