package qualificationhandoff

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type fakeAtomicStore struct {
	mu             sync.Mutex
	record         Record
	completeErr    error
	inspectErr     error
	complete       int
	inspect        int
	inspectCtx     error
	inspectBounded bool
	inspectID      uuid.UUID
	cancel         context.CancelFunc
}

func (store *fakeAtomicStore) Complete(context.Context, uuid.UUID) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.complete++
	if store.cancel != nil {
		store.cancel()
	}
	return cloneRecord(store.record), store.completeErr
}

func (store *fakeAtomicStore) Inspect(ctx context.Context, handoffID uuid.UUID) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspect++
	store.inspectCtx = ctx.Err()
	_, store.inspectBounded = ctx.Deadline()
	store.inspectID = handoffID
	return cloneRecord(store.record), store.inspectErr
}

func TestServiceCompleteAndInspect(t *testing.T) {
	record := testRecord(t)
	store := &fakeAtomicStore{record: record}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.Complete(context.Background(), record.HandoffID)
	if err != nil || !SameImmutableRecord(completed, record) {
		t.Fatalf("Complete() = %#v, %v", completed, err)
	}
	inspected, err := service.Inspect(context.Background(), record.HandoffID)
	if err != nil || !SameImmutableRecord(inspected, record) || !inspected.Idempotent {
		t.Fatalf("Inspect() = %#v, %v", inspected, err)
	}
	if store.complete != 1 || store.inspect != 1 {
		t.Fatalf("calls complete=%d inspect=%d", store.complete, store.inspect)
	}
}

func TestServiceCommitUnknownInspectsSameIDWithoutCancelledContext(t *testing.T) {
	record := testRecord(t)
	ctx, cancel := context.WithCancel(context.Background())
	store := &fakeAtomicStore{record: record, completeErr: ErrStoreOutcomeUnknown, cancel: cancel}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := service.Complete(ctx, record.HandoffID)
	if err != nil || !SameImmutableRecord(resolved, record) || !resolved.Idempotent {
		t.Fatalf("Complete() = %#v, %v", resolved, err)
	}
	if store.complete != 1 || store.inspect != 1 || store.inspectCtx != nil || !store.inspectBounded ||
		store.inspectID != record.HandoffID {
		t.Fatalf("recovery calls complete=%d inspect=%d inspectCtx=%v bounded=%v inspectID=%s", store.complete, store.inspect, store.inspectCtx, store.inspectBounded, store.inspectID)
	}
}

func TestServiceCommitUnknownNeverRepeatsOrTreatsAbsentAsSafe(t *testing.T) {
	record := testRecord(t)
	store := &fakeAtomicStore{record: record, completeErr: ErrStoreOutcomeUnknown, inspectErr: ErrNotFound}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(context.Background(), record.HandoffID); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrNotFound) {
		t.Fatalf("Complete() error = %v", err)
	}
	if store.complete != 1 || store.inspect != 1 {
		t.Fatalf("commit-unknown recovery repeated completion: complete=%d inspect=%d", store.complete, store.inspect)
	}
}

func TestServiceRejectsDifferentOrCorruptStoreResult(t *testing.T) {
	record := testRecord(t)
	for name, mutate := range map[string]func(*Record){
		"different": func(record *Record) { record.HandoffID = uuid.MustParse(testUUID(40)) },
		"corrupt":   func(record *Record) { record.Bundle.OutputRevision.ContentHash = testDigest('8') },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneRecord(record)
			mutate(&changed)
			service, err := NewService(&fakeAtomicStore{record: changed})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Complete(context.Background(), record.HandoffID); !errors.Is(err, ErrConflict) {
				t.Fatalf("Complete() error = %v", err)
			}
		})
	}
}

func TestServiceSanitizesStoreDiagnostics(t *testing.T) {
	record := testRecord(t)
	service, err := NewService(&fakeAtomicStore{
		record: record, completeErr: errors.New("password=super-secret /root/private"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Complete(context.Background(), record.HandoffID)
	if !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "/root/private") {
		t.Fatalf("Complete() leaked store diagnostic: %v", err)
	}
}
