package credentialset

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryStoreAtomicReservationAndCASAreThreadSafe(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	event := Event{
		At: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC), Kind: EventIssueReserved,
		IssuedAt: "2026-07-19T12:00:00.000Z", ExpiresAt: "2026-07-19T12:10:00.000Z",
		EventID:          "10000000-0000-4000-8000-000000000006",
		IssueCommandHash: sha256Digest([]byte("command")), OperationID: testIssueOperationID,
	}
	var created atomic.Int64
	var failures atomic.Int64
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			snapshot, won, err := store.CreateIssue(ctx, testSetID, event)
			if err != nil || snapshot.Version != 1 || snapshot.Phase != PhaseIssueReserved {
				failures.Add(1)
				return
			}
			if won {
				created.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || created.Load() != 1 {
		t.Fatalf("reservations: failures=%d winners=%d", failures.Load(), created.Load())
	}

	var appended atomic.Int64
	var conflicts atomic.Int64
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Append(ctx, testSetID, 1, Event{
				At: event.At, EventID: "10000000-0000-4000-8000-000000000007",
				Kind: EventPrepareStarted, OperationID: testIssueOperationID,
			})
			switch {
			case err == nil:
				appended.Add(1)
			case errors.Is(err, ErrCASConflict):
				conflicts.Add(1)
			default:
				failures.Add(1)
			}
		}()
	}
	wait.Wait()
	if appended.Load() != 64 || conflicts.Load() != 0 || failures.Load() != 0 {
		t.Fatalf("exact event replay: successes=%d conflicts=%d failures=%d", appended.Load(), conflicts.Load(), failures.Load())
	}
	events, err := store.Events(ctx, testSetID)
	if err != nil || len(events) != 2 {
		t.Fatalf("append-only events = %d, %v", len(events), err)
	}
}

func TestMemoryStoreRejectsEventTimeRegression(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	at := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	snapshot, _, err := store.CreateIssue(ctx, testSetID, Event{
		At: at, EventID: "10000000-0000-4000-8000-000000000006", Kind: EventIssueReserved,
		IssuedAt: "2026-07-19T12:00:00.000Z", ExpiresAt: "2026-07-19T12:10:00.000Z",
		IssueCommandHash: sha256Digest([]byte("command")), OperationID: testIssueOperationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(ctx, testSetID, snapshot.Version, Event{
		At: at.Add(-time.Millisecond), EventID: "10000000-0000-4000-8000-000000000007",
		Kind: EventPrepareStarted, OperationID: testIssueOperationID,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("regressing event time error = %v", err)
	}
}

func TestMemoryStoreReservesOperationIDsAcrossIssueAndRevocationKinds(t *testing.T) {
	trig := newTestRig(t)
	ctx := context.Background()
	if _, err := trig.service.Issue(ctx, trig.command); err != nil {
		t.Fatal(err)
	}

	secondBroker := &fakeBroker{errorText: "not-secret"}
	secondService, err := NewGoldenService(Config{
		Audience: testAudience, Broker: secondBroker, Clock: trig.clock, Issuer: testIssuer,
		Signer: trig.signer, Store: trig.store,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondCommand := trig.command
	secondCommand.SetID = "20000000-0000-4000-8000-000000000001"
	secondCommand.RunID = "20000000-0000-4000-8000-000000000002"
	secondCommand.FixtureID = "20000000-0000-4000-8000-000000000003"
	secondCommand.OperationID = "20000000-0000-4000-8000-000000000004"
	second, err := secondService.Issue(ctx, secondCommand)
	if err != nil {
		t.Fatal(err)
	}

	// Reusing the first set's issue operation as the second set's revoke
	// operation would also reuse credential-sign/<operation>/attestation at the
	// signer. The in-memory reference store must match PostgreSQL's global
	// operation primary key and reject the cross-kind collision first.
	_, err = secondService.Revoke(ctx, RevokeCommand{
		Binding: second.Binding, OperationID: trig.command.OperationID,
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-kind operation reuse error = %v", err)
	}
	snapshot, loadErr := trig.store.Load(ctx, secondCommand.SetID)
	if loadErr != nil || snapshot.Phase != PhaseIssued || snapshot.RevocationOperationID != "" {
		t.Fatalf("rejected operation reuse mutated second set: phase=%s revoke=%q err=%v", snapshot.Phase, snapshot.RevocationOperationID, loadErr)
	}
}

func TestMemoryStoreClonesPersistedCommitments(t *testing.T) {
	trig := newTestRig(t)
	issued, err := trig.service.Issue(context.Background(), trig.command)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := trig.store.Load(context.Background(), testSetID)
	if err != nil {
		t.Fatal(err)
	}
	originalActor := snapshot.Binding.Members[0].ActorID
	snapshot.Binding.Members[0].ActorID = "mutated"
	snapshot.IssueAttestation.Payload[0] ^= 0xff
	events, err := trig.store.Events(context.Background(), testSetID)
	if err != nil {
		t.Fatal(err)
	}
	for index := range events {
		if events[index].Binding != nil {
			events[index].Binding.Members[0].ActorID = "mutated-again"
		}
	}
	reloaded, err := trig.store.Load(context.Background(), testSetID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Binding.Members[0].ActorID != originalActor || reloaded.IssueAttestation.PayloadDigest != issued.Attestation.PayloadDigest {
		t.Fatal("caller mutation escaped into the append-only store")
	}
}
