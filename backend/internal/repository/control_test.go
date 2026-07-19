package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCandidateControlEventsFenceLeaseAndSessionEpochs(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	actorID := uuid.NewString()
	candidate := controlTestCandidate(t, actorID, now)

	leased, lease, acquired, err := candidate.AcquireLeaseWithEvent(candidate.Version, actorID, time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if acquired.Kind != ControlLeaseAcquired || acquired.WriterLeaseFrom != 0 || acquired.WriterLeaseTo != 1 ||
		acquired.WriterLease == nil || acquired.WriterLease.Epoch != lease.Epoch {
		t.Fatalf("lease event = %#v", acquired)
	}

	rotated, event, err := leased.RotateSession(
		leased.Version, leased.SessionEpoch, actorID, "gateway session resumed", now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Lease != nil || rotated.SessionEpoch != 2 || rotated.WriterLeaseEpoch != 2 ||
		event.Kind != ControlSessionRotated || event.SessionEpochFrom != 1 || event.SessionEpochTo != 2 {
		t.Fatalf("rotation = %#v event=%#v", rotated, event)
	}
	if _, _, err := rotated.AcquireLease(rotated.Version, actorID, time.Minute, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := leased.Apply(
		leased.Version, rotated.SessionEpoch, lease.Epoch, actorID, "user",
		FileOperation{ID: "stale", Kind: OperationDelete, Path: "README.md"}, now.Add(3*time.Second),
	); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("old candidate accepted a future session epoch: %v", err)
	}
}

func TestCandidateFlagsAreTypedMonotonicAndFenceTheWriter(t *testing.T) {
	now := time.Date(2026, time.July, 16, 13, 0, 0, 0, time.UTC)
	actorID := uuid.NewString()
	candidate := controlTestCandidate(t, actorID, now)
	leased, _, err := candidate.AcquireLease(candidate.Version, actorID, time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	stale, event, err := leased.UpdateFlags(
		leased.Version, leased.SessionEpoch, actorID, "canonical upstream advanced",
		CandidateFlags{Stale: true, RebaseRequired: true}, now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Lease != nil || !stale.Stale || !stale.RebaseRequired || event.Kind != ControlFlagsUpdated ||
		event.FlagsFrom.Stale || !event.FlagsTo.Stale {
		t.Fatalf("flags transition = %#v event=%#v", stale, event)
	}
	if _, _, err := stale.UpdateFlags(
		stale.Version, stale.SessionEpoch, actorID, "attempt unsafe in-place clear", CandidateFlags{}, now.Add(3*time.Second),
	); !errors.Is(err, ErrCandidateState) {
		t.Fatalf("stale/rebase flags were cleared in place: %v", err)
	}
	stale, _, err = stale.AcquireLease(stale.Version, actorID, time.Minute, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := stale.Checkpoint(
		stale.Version, stale.SessionEpoch, stale.WriterLeaseEpoch,
		uuid.NewString(), actorID, "stale checkpoint", now.Add(4*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := stale.Freeze(
		stale.Version, stale.SessionEpoch, actorID, "freeze", checkpoint, now.Add(5*time.Second),
	); !errors.Is(err, ErrCandidateState) {
		t.Fatalf("stale candidate froze: %v", err)
	}
}

func TestCandidateMutationFencesStaleAndConflictedLineages(t *testing.T) {
	now := time.Date(2026, time.July, 16, 13, 30, 0, 0, time.UTC)
	actorID := uuid.NewString()
	candidate := controlTestCandidate(t, actorID, now)
	leased, _, err := candidate.AcquireLease(candidate.Version, actorID, time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	operation := FileOperation{
		ID: "blocked-change", Kind: OperationUpsert, Path: "README.md",
		ExpectedHash: testDigest("base"), ContentHash: testDigest("blocked"), ByteSize: 7, Mode: "100644",
	}

	stale := leased
	stale.Stale, stale.RebaseRequired = true, true
	if _, _, err := stale.Apply(
		stale.Version, stale.SessionEpoch, stale.WriterLeaseEpoch,
		actorID, "user", operation, now.Add(2*time.Second),
	); !errors.Is(err, ErrCandidateState) {
		t.Fatalf("stale mutation error = %v, want Candidate state", err)
	}

	conflicted := leased
	conflicted.Conflicted = true
	if _, _, err := conflicted.Apply(
		conflicted.Version, conflicted.SessionEpoch, conflicted.WriterLeaseEpoch,
		actorID, "user", operation, now.Add(2*time.Second),
	); !errors.Is(err, ErrCandidateState) {
		t.Fatalf("conflicted user mutation error = %v, want Candidate state", err)
	}
	if _, _, err := conflicted.Apply(
		conflicted.Version, conflicted.SessionEpoch, conflicted.WriterLeaseEpoch,
		actorID, "merge", operation, now.Add(2*time.Second),
	); err != nil {
		t.Fatalf("conflict resolution merge was blocked: %v", err)
	}
}

func TestFreezeAndDirtyAbandonRequireExactCheckpoint(t *testing.T) {
	now := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)
	actorID := uuid.NewString()
	candidate := controlTestCandidate(t, actorID, now)
	leased, lease, err := candidate.AcquireLease(candidate.Version, actorID, time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	dirty, _, err := leased.Apply(
		leased.Version, leased.SessionEpoch, lease.Epoch, actorID, "user",
		FileOperation{
			ID: "edit", Kind: OperationUpsert, Path: "README.md", ExpectedHash: testDigest("base"),
			ContentHash: testDigest("next"), ByteSize: 4,
		}, now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dirty.Abandon(
		dirty.Version, dirty.SessionEpoch, actorID, "close sandbox", nil, now.Add(3*time.Second),
	); !errors.Is(err, ErrCandidateState) {
		t.Fatalf("dirty candidate abandoned without checkpoint: %v", err)
	}

	checkpoint, err := dirty.Checkpoint(
		dirty.Version, dirty.SessionEpoch, dirty.WriterLeaseEpoch,
		uuid.NewString(), actorID, "autosave", now.Add(3*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	wrong := checkpoint
	wrong.CandidateVersion--
	if _, _, err := dirty.Freeze(
		dirty.Version, dirty.SessionEpoch, actorID, "freeze", wrong, now.Add(4*time.Second),
	); !errors.Is(err, ErrCandidateState) {
		t.Fatalf("mismatched checkpoint froze candidate: %v", err)
	}
	frozen, event, err := dirty.Freeze(
		dirty.Version, dirty.SessionEpoch, actorID, "freeze exact candidate", checkpoint, now.Add(4*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != CandidateFrozen || frozen.Lease != nil || event.Checkpoint == nil ||
		event.Checkpoint.TreeHash != dirty.CurrentTree.TreeHash || event.Kind != ControlFrozen {
		t.Fatalf("freeze = %#v event=%#v", frozen, event)
	}
}

func controlTestCandidate(t *testing.T, actorID string, now time.Time) CandidateWorkspace {
	t.Helper()
	tree, err := NewTree([]TreeFile{{Path: "README.md", ContentHash: testDigest("base"), ByteSize: 4}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewCandidate(uuid.NewString(), RepositorySnapshot{
		ID: uuid.NewString(), ProjectID: uuid.NewString(),
		BuildManifest:     ExactReference{ID: uuid.NewString(), ContentHash: testDigest("manifest")[len("sha256:"):]},
		BuildContract:     ExactReference{ID: uuid.NewString(), ContentHash: testDigest("contract")[len("sha256:"):]},
		FullStackTemplate: ExactReference{ID: uuid.NewString(), ContentHash: testDigest("stack")},
		Tree:              tree, CreatedBy: actorID, CreatedAt: now.Add(-time.Second),
	}, actorID, now)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}
