package qualificationpromotion

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type MemoryStore struct {
	mu                     sync.Mutex
	clock                  func() time.Time
	byOperation            map[uuid.UUID]ConsumptionRecord
	byNonce                map[string]uuid.UUID
	byKey                  map[string]uuid.UUID
	unknownAfterCommitOnce bool
}

func NewMemoryStore(clock func() time.Time) (*MemoryStore, error) {
	if clock == nil {
		return nil, fmt.Errorf("%w: trusted clock is required", ErrInvalid)
	}
	return &MemoryStore{
		clock:       clock,
		byOperation: make(map[uuid.UUID]ConsumptionRecord),
		byNonce:     make(map[string]uuid.UUID),
		byKey:       make(map[string]uuid.UUID),
	}, nil
}

func (store *MemoryStore) trustedTime(ctx context.Context) (time.Time, error) {
	if store == nil || store.clock == nil || ctx == nil {
		return time.Time{}, fmt.Errorf("%w: memory store and context are required", ErrInvalid)
	}
	select {
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	default:
	}
	return store.clock().UTC().Truncate(time.Millisecond), nil
}

func (store *MemoryStore) append(ctx context.Context, command appendCommand) (ConsumptionRecord, error) {
	if store == nil || ctx == nil {
		return ConsumptionRecord{}, fmt.Errorf("%w: memory store and context are required", ErrInvalid)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record := cloneRecord(command.record)
	if existing, ok := store.byOperation[record.OperationID]; ok {
		if !sameImmutableRecord(existing, record) {
			return ConsumptionRecord{}, fmt.Errorf("%w: operation ID is bound to different bytes", ErrConflict)
		}
		existing.Idempotent = true
		return cloneRecord(existing), nil
	}
	if operationID, ok := store.byNonce[record.VerifiedPromotion.AuthorityNonce]; ok {
		existing := store.byOperation[operationID]
		if !sameImmutableRecord(existing, record) {
			return ConsumptionRecord{}, fmt.Errorf("%w: authority nonce was already consumed for a different target, digest, operation, or handoff", ErrConflict)
		}
		existing.Idempotent = true
		return cloneRecord(existing), nil
	}
	now := store.clock().UTC().Truncate(time.Millisecond)
	if err := validateVerifiedPromotionAt(record.VerifiedPromotion, now); err != nil {
		return ConsumptionRecord{}, err
	}
	record.ConsumedAt = now
	record.Handoff.CreatedAt = now
	store.byOperation[record.OperationID] = cloneRecord(record)
	store.byNonce[record.VerifiedPromotion.AuthorityNonce] = record.OperationID
	store.byKey[consumptionKeyString(ConsumptionKey{
		Target:                   record.VerifiedPromotion.PromotionTarget,
		AuthorityNonce:           record.VerifiedPromotion.AuthorityNonce,
		PromotionAuthorityDigest: record.VerifiedPromotion.PromotionAuthorityDigest,
	})] = record.OperationID
	if store.unknownAfterCommitOnce {
		store.unknownAfterCommitOnce = false
		return ConsumptionRecord{}, ErrOutcomeUnknown
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) inspectOperation(ctx context.Context, operationID uuid.UUID) (ConsumptionRecord, error) {
	if store == nil || ctx == nil || operationID.Version() != 4 {
		return ConsumptionRecord{}, ErrNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.byOperation[operationID]
	if !ok {
		return ConsumptionRecord{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) inspectKey(ctx context.Context, key ConsumptionKey) (ConsumptionRecord, error) {
	if store == nil || ctx == nil {
		return ConsumptionRecord{}, ErrNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operationID, ok := store.byKey[consumptionKeyString(key)]
	if !ok {
		return ConsumptionRecord{}, ErrNotFound
	}
	return cloneRecord(store.byOperation[operationID]), nil
}

func consumptionKeyString(key ConsumptionKey) string {
	digest, _ := targetDigest(key.Target)
	return digest + "\x00" + key.AuthorityNonce + "\x00" + key.PromotionAuthorityDigest
}

var _ store = (*MemoryStore)(nil)
