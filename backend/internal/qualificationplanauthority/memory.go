package qualificationplanauthority

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is a thread-safe semantic reference store for tests. Unlike the
// qualificationevidence MemoryStore, it deliberately enforces cross-authority
// global UUID reservation because this authority is where all plan identities
// are allocated and frozen.
type MemoryStore struct {
	mu sync.Mutex

	clock func() time.Time

	byOperation       map[uuid.UUID]Record
	byAuthority       map[uuid.UUID]Record
	reservedUUIDs     map[uuid.UUID]uuid.UUID
	reservedArtifacts map[string]uuid.UUID

	unknownAfterCommitOnce bool
}

func NewMemoryStore(clocks ...func() time.Time) *MemoryStore {
	clock := time.Now
	if len(clocks) > 0 && clocks[0] != nil {
		clock = clocks[0]
	}
	return &MemoryStore{
		clock: clock, byOperation: make(map[uuid.UUID]Record), byAuthority: make(map[uuid.UUID]Record),
		reservedUUIDs: make(map[uuid.UUID]uuid.UUID), reservedArtifacts: make(map[string]uuid.UUID),
	}
}

func (store *MemoryStore) Freeze(ctx context.Context, candidate Record) (Record, error) {
	if store == nil || isNilInterface(ctx) {
		return Record{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := validateRecordMaterials(candidate, false); err != nil {
		return Record{}, err
	}
	if err := validateUniqueRecordUUIDs(candidate); err != nil {
		return Record{}, fmt.Errorf("%w: qualification plan identities collide within one authority", ErrConflict)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, exists := store.byOperation[candidate.OperationID]; exists {
		if !sameImmutableRecord(existing, candidate) {
			return Record{}, fmt.Errorf("%w: operation ID is bound to different canonical bytes", ErrConflict)
		}
		existing.Idempotent = true
		return cloneRecord(existing), nil
	}
	if _, exists := store.byAuthority[candidate.AuthorityID]; exists {
		return Record{}, fmt.Errorf("%w: authority ID is already frozen", ErrConflict)
	}
	for _, identity := range reservedRecordUUIDs(candidate) {
		if owner, reserved := store.reservedUUIDs[identity]; reserved && owner != candidate.AuthorityID {
			return Record{}, fmt.Errorf("%w: UUID %s is globally reserved", ErrConflict, identity)
		}
	}
	artifactID := candidate.Envelope.ArtifactID
	if owner, reserved := store.reservedArtifacts[artifactID]; reserved && owner != candidate.AuthorityID {
		return Record{}, fmt.Errorf("%w: plan artifact ID is globally reserved", ErrConflict)
	}
	now := store.clock()
	if now.IsZero() || now.Nanosecond()%int(time.Millisecond) != 0 {
		return Record{}, fmt.Errorf("%w: store clock must have millisecond precision", ErrInvalid)
	}
	now = now.UTC()
	stored := cloneRecord(candidate)
	stored.FrozenAt = now
	stored.Idempotent = false
	store.byOperation[stored.OperationID] = cloneRecord(stored)
	store.byAuthority[stored.AuthorityID] = cloneRecord(stored)
	for _, identity := range reservedRecordUUIDs(stored) {
		store.reservedUUIDs[identity] = stored.AuthorityID
	}
	store.reservedArtifacts[artifactID] = stored.AuthorityID
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
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.byOperation[operationID]
	if !exists {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) ResolveAuthority(ctx context.Context, authorityID uuid.UUID) (Record, error) {
	if store == nil || isNilInterface(ctx) || authorityID.Version() != 4 {
		return Record{}, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.byAuthority[authorityID]
	if !exists {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

// InjectCommitUnknownOnce is test-only fault injection. The next successful
// Freeze is committed atomically and then reports an unknown outcome.
func (store *MemoryStore) InjectCommitUnknownOnce() {
	if store == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.unknownAfterCommitOnce = true
}

var _ Store = (*MemoryStore)(nil)
