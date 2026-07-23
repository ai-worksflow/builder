package workflowinputauthority

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type unknownTransaction struct{}

func (*unknownTransaction) workflowInputAuthorityTransaction() {}

func TestMemoryStoreExactReplayCloneAndCurrentSemantics(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 19, 1, 2, 3, 456789000, time.FixedZone("offset", 3600))
	store := NewMemoryStore(func() time.Time { return now })
	candidate := goldenCandidate(t)
	record, err := Compile(candidate)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Freeze(context.Background(), nil, candidate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil transaction error = %v", err)
	}
	var typedNil *unknownTransaction
	if _, err := store.Freeze(context.Background(), typedNil, candidate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed-nil/unknown transaction error = %v", err)
	}
	stored, err := store.Freeze(context.Background(), MemoryTransaction{}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	wantFrozenAt := now.UTC().Truncate(time.Millisecond)
	if !stored.FrozenAt.Equal(wantFrozenAt) || stored.Idempotent {
		t.Fatalf("store-authored fields = %s idempotent=%v", stored.FrozenAt, stored.Idempotent)
	}

	replayed, err := store.Freeze(context.Background(), MemoryTransaction{}, candidate)
	if err != nil || !replayed.Idempotent || replayed.AuthorityHash != stored.AuthorityHash {
		t.Fatalf("exact replay = %#v, %v", replayed, err)
	}
	byOperation, err := store.InspectOperation(context.Background(), record.OperationID)
	if err != nil || byOperation.Idempotent {
		t.Fatalf("operation inspection = %#v, %v", byOperation, err)
	}
	byNode, err := store.ResolveNode(context.Background(), record.WorkflowRunID, record.NodeRunID)
	if err != nil || byNode.AuthorityID != stored.AuthorityID {
		t.Fatalf("node resolution = %#v, %v", byNode, err)
	}

	stored.InputBytes[0] = 'X'
	stored.Materials.Definition[0] = 'X'
	stored.Materials.Revisions[0].Bytes[0] = 'X'
	resolved, err := store.ResolveAuthority(context.Background(), record.AuthorityID)
	if err != nil || resolved.InputBytes[0] == 'X' || resolved.Materials.Definition[0] == 'X' || resolved.Materials.Revisions[0].Bytes[0] == 'X' {
		t.Fatalf("returned mutation leaked into store: %v", err)
	}
	if _, err := store.AssertCurrent(context.Background(), record.AuthorityID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStale(record.AuthorityID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssertCurrent(context.Background(), record.AuthorityID); !errors.Is(err, ErrStale) {
		t.Fatalf("stale assertion error = %v", err)
	}
	if _, err := store.ResolveAuthority(context.Background(), record.AuthorityID); err != nil {
		t.Fatalf("historical resolution must survive staleness: %v", err)
	}
}

func TestMemoryStoreDerivesCanonicalDocumentsInsteadOfAcceptingCallerHashes(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	candidate := goldenCandidate(t)
	candidate.Request.SchemaVersion = "caller/request"
	candidate.Request.MediaType = "caller/request"
	candidate.Input.SchemaVersion = "caller/input"
	candidate.Input.MediaType = "caller/input"
	candidate.Input.TargetHash = digest("0")

	stored, err := store.Freeze(context.Background(), MemoryTransaction{}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	want := mustCompileGolden(t)
	if stored.RequestHash != want.RequestHash || stored.TargetHash != want.TargetHash || stored.InputHash != want.InputHash ||
		stored.AuthorityHash != want.AuthorityHash || stored.Request.SchemaVersion != FreezeRequestSchemaV1 ||
		stored.Input.SchemaVersion != InputSchemaV1 || stored.Envelope.SchemaVersion != AuthoritySchemaV1 {
		t.Fatalf("caller-authored derived hash/schema material reached Store output: %#v", stored)
	}
}

func TestMemoryStoreConcurrentExactFreezeCreatesOneAuthority(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(func() time.Time {
		return time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	})
	candidate := goldenCandidate(t)
	record := mustCompileGolden(t)

	const writers = 32
	results := make(chan Record, writers)
	errorsChannel := make(chan error, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			stored, err := store.Freeze(context.Background(), MemoryTransaction{}, candidate)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- stored
		}()
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
	count := 0
	nonReplay := 0
	for result := range results {
		count++
		if result.AuthorityID != record.AuthorityID {
			t.Fatalf("writer returned authority %s", result.AuthorityID)
		}
		if !result.Idempotent {
			nonReplay++
		}
	}
	if count != writers || nonReplay != 1 {
		t.Fatalf("results=%d initial commits=%d", count, nonReplay)
	}
}

func TestMemoryStoreSameOperationDifferentCandidateConflicts(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	base := goldenCandidate(t)
	if _, err := store.Freeze(context.Background(), MemoryTransaction{}, base); err != nil {
		t.Fatal(err)
	}
	different := goldenCandidate(t)
	different.Request.AuthorityID = "20202020-2020-4020-8020-202020202020"
	if _, err := store.Freeze(context.Background(), MemoryTransaction{}, different); !errors.Is(err, ErrConflict) {
		t.Fatalf("same operation/different candidate error = %v", err)
	}
}

func TestMemoryStoreReorderedRetainedSetsAreExactReplay(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	base := goldenCandidate(t)
	first, err := store.Freeze(context.Background(), MemoryTransaction{}, base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := goldenCandidate(t)
	reordered.Materials.InputManifests[0], reordered.Materials.InputManifests[1] =
		reordered.Materials.InputManifests[1], reordered.Materials.InputManifests[0]
	reordered.Materials.Revisions[0], reordered.Materials.Revisions[1] =
		reordered.Materials.Revisions[1], reordered.Materials.Revisions[0]
	replayed, err := store.Freeze(context.Background(), MemoryTransaction{}, reordered)
	if err != nil || !replayed.Idempotent || replayed.AuthorityHash != first.AuthorityHash {
		t.Fatalf("reordered retained fact set replay = %#v, %v", replayed, err)
	}
}

func TestMemoryStoreSameNodeFreshIdentitiesConflictAndDoNotAliasOperation(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	base := goldenCandidate(t)
	if _, err := store.Freeze(context.Background(), MemoryTransaction{}, base); err != nil {
		t.Fatal(err)
	}
	different := goldenCandidate(t)
	different.Request.OperationID = "20202020-2020-4020-8020-202020202020"
	different.Request.AuthorityID = "21212121-2121-4121-8121-212121212121"
	different.Input.Gate.ActivationEventID = "23232323-2323-4323-8323-232323232323"
	if _, err := store.Freeze(context.Background(), MemoryTransaction{}, different); !errors.Is(err, ErrConflict) {
		t.Fatalf("same node/fresh identities error = %v", err)
	}
	loserOperation := uuid.MustParse(different.Request.OperationID)
	if _, err := store.InspectOperation(context.Background(), loserOperation); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed operation was aliased to winner: %v", err)
	}
}

func TestMemoryStoreReservesAuthorityOperationAndActivationIdentitiesAcrossRoles(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	base := goldenCandidate(t)
	if _, err := store.Freeze(context.Background(), MemoryTransaction{}, base); err != nil {
		t.Fatal(err)
	}

	reused := goldenCandidate(t)
	reused.Request.OperationID = "24242424-2424-4424-8424-242424242424"
	reused.Request.AuthorityID = base.Input.Gate.ActivationEventID
	reused.Request.NodeRunID = "25252525-2525-4525-8525-252525252525"
	reused.Input.Gate.NodeRunID = reused.Request.NodeRunID
	reused.Input.Gate.ActivationEventID = "26262626-2626-4626-8626-262626262626"
	if _, err := store.Freeze(context.Background(), MemoryTransaction{}, reused); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-role identity reuse error = %v", err)
	}
}
