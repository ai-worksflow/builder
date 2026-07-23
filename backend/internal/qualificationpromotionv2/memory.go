package qualificationpromotionv2

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is a deterministic, concurrency-safe semantic reference. It is
// intentionally not a production authority implementation.
type MemoryStore struct {
	mu    sync.Mutex
	clock func() time.Time

	prepared                     map[string]PreparedAuthority
	byOperation                  map[uuid.UUID]Record
	byHandoff                    map[uuid.UUID]uuid.UUID
	byWorkflowInput              map[uuid.UUID]uuid.UUID
	byPlan                       map[uuid.UUID]uuid.UUID
	byReceipt                    map[string]uuid.UUID
	byInputPrecommit             map[string]uuid.UUID
	byOutputRevision             map[uuid.UUID]uuid.UUID
	promotionReservations        map[uuid.UUID]uuid.UUID
	artifactRevisionReservations map[uuid.UUID]string

	unknownAfterCommitOnce bool
}

func NewMemoryStore(clock func() time.Time) (*MemoryStore, error) {
	if clock == nil {
		return nil, invalid("memoryStore", "trusted clock is required")
	}
	return &MemoryStore{
		clock:                        clock,
		prepared:                     make(map[string]PreparedAuthority),
		byOperation:                  make(map[uuid.UUID]Record),
		byHandoff:                    make(map[uuid.UUID]uuid.UUID),
		byWorkflowInput:              make(map[uuid.UUID]uuid.UUID),
		byPlan:                       make(map[uuid.UUID]uuid.UUID),
		byReceipt:                    make(map[string]uuid.UUID),
		byInputPrecommit:             make(map[string]uuid.UUID),
		byOutputRevision:             make(map[uuid.UUID]uuid.UUID),
		promotionReservations:        make(map[uuid.UUID]uuid.UUID),
		artifactRevisionReservations: make(map[uuid.UUID]string),
	}, nil
}

// ReserveArtifactRevision models the shared global revision-identity write
// point populated by ordinary artifact revisions. Promotion must conflict
// rather than check-and-reuse one of these identities.
func (store *MemoryStore) ReserveArtifactRevision(identity uuid.UUID) error {
	if store == nil || !validUUIDv4Value(identity) {
		return invalid("memoryStore.artifactRevision", "canonical UUIDv4 identity is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, reserved := store.artifactRevisionReservations[identity]; reserved {
		return fmt.Errorf("%w: artifact revision identity is already reserved", ErrConflict)
	}
	store.artifactRevisionReservations[identity] = "artifact-revision"
	return nil
}

// InstallAuthority installs a server-owned semantic fixture. It validates only
// the two lookup identities here; complete currentness, independent-policy,
// event, target, Receipt, and hash closure checks occur atomically in Consume.
func (store *MemoryStore) InstallAuthority(prepared PreparedAuthority) error {
	if store == nil || !validUUIDv4(prepared.WorkflowInput.AuthorityID) || !validUUIDv4(prepared.Plan.AuthorityID) {
		return invalid("memoryStore.prepared", "Workflow Input and Plan authority IDs are required")
	}
	key := preparedKey(prepared.WorkflowInput.AuthorityID, prepared.Plan.AuthorityID)
	clone := clonePrepared(prepared)
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.prepared[key]; ok && !reflect.DeepEqual(existing, clone) {
		return fmt.Errorf("%w: prepared authority key is already bound to different server-owned facts", ErrConflict)
	}
	store.prepared[key] = clone
	// The target is an existing artifact revision and therefore already owns
	// its global revision identity before Promotion allocates an output. Seed
	// that durable reservation here so conflict checks do not depend on mutable
	// authority availability.
	if targetRevisionID, err := uuid.Parse(clone.Target.TargetRevisionID); err == nil && validUUIDv4Value(targetRevisionID) {
		if _, reserved := store.artifactRevisionReservations[targetRevisionID]; !reserved {
			store.artifactRevisionReservations[targetRevisionID] = "artifact-revision"
		}
	}
	return nil
}

// RemoveAuthority models retirement after a committed operation. Exact
// operation replay remains inspectable because Consume checks it first.
func (store *MemoryStore) RemoveAuthority(workflowInputAuthorityID, planAuthorityID uuid.UUID) {
	if store == nil {
		return
	}
	store.mu.Lock()
	delete(store.prepared, preparedKey(workflowInputAuthorityID.String(), planAuthorityID.String()))
	store.mu.Unlock()
}

func (store *MemoryStore) Consume(ctx context.Context, command ConsumeCommand) (Record, error) {
	if store == nil || store.clock == nil || ctx == nil {
		return Record{}, invalid("memoryStore", "store, clock, and context are required")
	}
	if err := ValidateCommand(command); err != nil {
		return Record{}, err
	}
	select {
	case <-ctx.Done():
		return Record{}, ctx.Err()
	default:
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	// Replay is deliberately resolved before mutable currentness or authority
	// availability so committed results survive retirement and supersession.
	if existing, ok := store.byOperation[command.OperationID]; ok {
		if existing.Command != command {
			return Record{}, fmt.Errorf("%w: operation ID is bound to different command identities", ErrConflict)
		}
		if err := ValidateRecord(existing); err != nil {
			return Record{}, fmt.Errorf("%w: stored operation failed immutable validation", ErrConflict)
		}
		existing.Idempotent = true
		return cloneRecord(existing), nil
	}
	if err := store.requireCommandAvailableLocked(command); err != nil {
		return Record{}, err
	}

	prepared, ok := store.prepared[preparedKey(command.WorkflowInputAuthorityID.String(), command.PlanAuthorityID.String())]
	if !ok {
		return Record{}, fmt.Errorf("%w: exact Workflow Input and Plan authority pair is unavailable", ErrNotReady)
	}
	// This check is intentionally before any independent receipt registry
	// lookup. Migration 81 has no such lookup or admission path at all.
	if err := validateInitialIndependent(prepared.IndependentRequirements); err != nil {
		return Record{}, err
	}
	record, err := Compile(command, prepared, store.clock())
	if err != nil {
		return Record{}, err
	}
	if err := store.requireReceiptAvailableLocked(record); err != nil {
		return Record{}, err
	}
	stored := cloneRecord(record)
	store.byOperation[command.OperationID] = stored
	store.byHandoff[command.HandoffID] = command.OperationID
	store.byWorkflowInput[command.WorkflowInputAuthorityID] = command.OperationID
	store.byPlan[command.PlanAuthorityID] = command.OperationID
	store.byReceipt[record.ReceiptID] = command.OperationID
	store.byInputPrecommit[record.Closure.InputPrecommit.AuthorityID] = command.OperationID
	store.byOutputRevision[command.OutputRevisionID] = command.OperationID
	store.artifactRevisionReservations[command.OutputRevisionID] = "qualification-promotion-v2"
	for _, identity := range []uuid.UUID{command.OperationID, command.HandoffID, command.OutputRevisionID} {
		store.promotionReservations[identity] = command.OperationID
	}
	if store.unknownAfterCommitOnce {
		store.unknownAfterCommitOnce = false
		return Record{}, ErrStoreOutcomeUnknown
	}
	return cloneRecord(stored), nil
}

func (store *MemoryStore) requireCommandAvailableLocked(command ConsumeCommand) error {
	if _, reserved := store.artifactRevisionReservations[command.OutputRevisionID]; reserved {
		return fmt.Errorf("%w: output revision identity is already globally reserved", ErrConflict)
	}
	for _, identity := range []uuid.UUID{command.OperationID, command.HandoffID, command.OutputRevisionID} {
		if _, reserved := store.promotionReservations[identity]; reserved {
			return fmt.Errorf("%w: Promotion identity is already reserved", ErrConflict)
		}
	}
	for _, binding := range []struct {
		name  string
		found bool
	}{
		{name: "workflowInputAuthorityId", found: hasUUIDKey(store.byWorkflowInput, command.WorkflowInputAuthorityID)},
		{name: "planAuthorityId", found: hasUUIDKey(store.byPlan, command.PlanAuthorityID)},
		{name: "handoffId", found: hasUUIDKey(store.byHandoff, command.HandoffID)},
		{name: "outputRevisionId", found: hasUUIDKey(store.byOutputRevision, command.OutputRevisionID)},
	} {
		if binding.found {
			return fmt.Errorf("%w: %s was already consumed or reserved", ErrConflict, binding.name)
		}
	}
	return nil
}

func (store *MemoryStore) requireReceiptAvailableLocked(record Record) error {
	if _, found := store.byReceipt[record.ReceiptID]; found {
		return fmt.Errorf("%w: receiptId was already consumed", ErrConflict)
	}
	if _, found := store.byInputPrecommit[record.Closure.InputPrecommit.AuthorityID]; found {
		return fmt.Errorf("%w: input precommit authority was already consumed", ErrConflict)
	}
	return nil
}

func hasUUIDKey(values map[uuid.UUID]uuid.UUID, key uuid.UUID) bool {
	_, found := values[key]
	return found
}

func (store *MemoryStore) InspectOperation(ctx context.Context, operationID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || !validUUIDv4Value(operationID) {
		return Record{}, ErrNotFound
	}
	select {
	case <-ctx.Done():
		return Record{}, ctx.Err()
	default:
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.byOperation[operationID]
	if !ok {
		return Record{}, ErrNotFound
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: stored operation failed immutable validation", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) InspectHandoff(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	if store == nil || ctx == nil || !validUUIDv4Value(handoffID) {
		return Record{}, ErrNotFound
	}
	select {
	case <-ctx.Done():
		return Record{}, ctx.Err()
	default:
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operationID, ok := store.byHandoff[handoffID]
	if !ok {
		return Record{}, ErrNotFound
	}
	record := store.byOperation[operationID]
	if err := ValidateRecord(record); err != nil {
		return Record{}, fmt.Errorf("%w: stored handoff failed immutable validation", ErrConflict)
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) InjectCommitUnknownOnce() {
	if store == nil {
		return
	}
	store.mu.Lock()
	store.unknownAfterCommitOnce = true
	store.mu.Unlock()
}

func preparedKey(workflowInputAuthorityID, planAuthorityID string) string {
	return workflowInputAuthorityID + "\x00" + planAuthorityID
}

var _ AtomicStore = (*MemoryStore)(nil)
var _ PendingHandoffResolver = (*MemoryStore)(nil)
