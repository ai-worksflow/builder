package qualificationevidence

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func reserveMemoryPlan(t *testing.T, store *MemoryStore, plan Plan, trust TrustBindings, eventID string) Snapshot {
	t.Helper()
	commandHash, err := CanonicalDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest, err := CanonicalDigest(trust)
	if err != nil {
		t.Fatal(err)
	}
	copy := clonePlan(plan)
	snapshot, _, err := store.Create(context.Background(), plan.OrchestrationID, Event{
		At: testTime(12, 10, 0), EventID: eventID, Kind: EventReserved, OperationID: plan.Operations.Reserve,
		CommandHash: commandHash, TrustBindingsDigest: trustDigest, Plan: &copy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestMemoryStoreCASIsThreadSafeAndAppendOnly(t *testing.T) {
	store := NewMemoryStore()
	plan, trust := testPlan(t), testTrust()
	snapshot := reserveMemoryPlan(t, store, plan, trust, testUUID(70))
	var success, conflicts, unexpected atomic.Int64
	var wait sync.WaitGroup
	for index := range 32 {
		wait.Add(1)
		go func(value int) {
			defer wait.Done()
			_, err := store.Append(context.Background(), plan.OrchestrationID, snapshot.Version, Event{
				At: testTime(12, 10, 0), EventID: testUUID(100 + value), Kind: EventCredentialIssueStarted,
				OperationID: plan.Operations.CredentialIssue,
			})
			switch {
			case err == nil:
				success.Add(1)
			case errors.Is(err, ErrCASConflict):
				conflicts.Add(1)
			default:
				unexpected.Add(1)
			}
		}(index)
	}
	wait.Wait()
	if success.Load() != 1 || conflicts.Load() != 31 || unexpected.Load() != 0 {
		t.Fatalf("CAS success=%d conflicts=%d unexpected=%d", success.Load(), conflicts.Load(), unexpected.Load())
	}
	events, err := store.Events(context.Background(), plan.OrchestrationID)
	if err != nil || len(events) != 2 {
		t.Fatalf("append-only ledger = %d, %v", len(events), err)
	}
}

func TestMemoryStoreEventIDReplayAndDriftNeverPoisonLedger(t *testing.T) {
	store := NewMemoryStore()
	plan, trust := testPlan(t), testTrust()
	snapshot := reserveMemoryPlan(t, store, plan, trust, testUUID(70))
	event := Event{
		At: testTime(12, 10, 0), EventID: testUUID(71), Kind: EventCredentialIssueStarted,
		OperationID: plan.Operations.CredentialIssue,
	}
	updated, err := store.Append(context.Background(), plan.OrchestrationID, snapshot.Version, event)
	if err != nil || updated.Version != 2 {
		t.Fatalf("first append = %#v, %v", updated, err)
	}
	replayed, err := store.Append(context.Background(), plan.OrchestrationID, snapshot.Version, event)
	if err != nil || replayed.Version != 2 || replayed.LastEventID != event.EventID {
		t.Fatalf("exact EventID replay = %#v, %v", replayed, err)
	}
	drift := event
	drift.OperationID = plan.Operations.RunClosure
	if _, err := store.Append(context.Background(), plan.OrchestrationID, snapshot.Version, drift); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("drifted EventID error = %v", err)
	}
	reloaded, err := store.Load(context.Background(), plan.OrchestrationID)
	events, eventErr := store.Events(context.Background(), plan.OrchestrationID)
	if err != nil || eventErr != nil || reloaded.Version != 2 || len(events) != 2 || reloaded.Phase != PhaseCredentialIssueStarted {
		t.Fatalf("ledger was poisoned: snapshot=%#v events=%d errors=%v/%v", reloaded, len(events), err, eventErr)
	}
}

func TestMemoryStoreRejectsOutOfOrderTransitions(t *testing.T) {
	store := NewMemoryStore()
	plan, trust := testPlan(t), testTrust()
	snapshot := reserveMemoryPlan(t, store, plan, trust, testUUID(70))
	_, err := store.Append(context.Background(), plan.OrchestrationID, snapshot.Version, Event{
		At: testTime(12, 10, 0), EventID: testUUID(71), Kind: EventKMSAttestationStarted,
		OperationID: plan.Operations.KMSAttestation,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("out-of-order KMS error = %v", err)
	}
	reloaded, loadErr := store.Load(context.Background(), plan.OrchestrationID)
	if loadErr != nil || reloaded.Version != 1 || reloaded.Phase != PhaseReserved {
		t.Fatalf("rejected transition mutated state: %#v, %v", reloaded, loadErr)
	}
}

func TestMemoryStoreDoesNotClaimCrossOrchestrationOperationUniqueness(t *testing.T) {
	store := NewMemoryStore()
	trust := testTrust()
	first := testPlan(t)
	second := clonePlan(first)
	second.OrchestrationID = testUUID(201)
	second.RunID = testUUID(202)
	second.FixtureID = testUUID(203)
	second.CredentialSet.SetID = testUUID(204)
	// The operation IDs intentionally remain identical. An in-memory aggregate
	// Store can only enforce their uniqueness within one orchestration; a future
	// durable PostgreSQL Store must enforce the cross-orchestration constraint.
	if err := ValidatePlan(second); err != nil {
		t.Fatal(err)
	}
	reserveMemoryPlan(t, store, first, trust, testUUID(70))
	reserveMemoryPlan(t, store, second, trust, testUUID(71))
	if _, err := store.Load(context.Background(), first.OrchestrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), second.OrchestrationID); err != nil {
		t.Fatal(err)
	}
}
