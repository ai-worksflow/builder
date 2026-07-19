package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCandidateControlStorePostgresLeaseRotateCheckpointAndFreeze(t *testing.T) {
	fixture := openCandidateStorePostgresFixture(t)
	seed := fixture.seedCandidate(t)
	store, err := NewCandidateControlStore(fixture.gorm, fixture.store)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(
		fixture.context, fixture.otherProjectID.String(), seed.candidateID.String(),
	); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("cross-project Candidate read error = %v, want not found", err)
	}
	if _, err := store.AcquireLease(
		fixture.context, fixture.otherProjectID.String(), seed.candidateID.String(),
		2, fixture.actorID.String(), 5*time.Minute,
	); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("cross-project lease error = %v, want not found", err)
	}

	leased, err := store.AcquireLease(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
		2, fixture.actorID.String(), 5*time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if leased.Candidate.Version != 3 || leased.Candidate.SessionEpoch != 1 ||
		leased.Candidate.WriterLeaseEpoch != 2 || leased.Candidate.Lease == nil ||
		leased.Candidate.Lease.OwnerID != fixture.actorID.String() {
		t.Fatalf("leased Candidate = %#v", leased.Candidate)
	}

	rotated, err := store.RotateSession(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
		leased.Candidate.Version, leased.Candidate.SessionEpoch, fixture.actorID.String(),
	)
	if err != nil {
		t.Fatalf("rotate session: %v", err)
	}
	if rotated.Candidate.Version != 4 || rotated.Candidate.SessionEpoch != 2 ||
		rotated.Candidate.WriterLeaseEpoch != 3 || rotated.Candidate.Lease != nil {
		t.Fatalf("rotated Candidate = %#v", rotated.Candidate)
	}
	if _, err := store.RotateSession(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
		rotated.Candidate.Version, 1, fixture.actorID.String(),
	); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("stale session epoch error = %v, want fenced", err)
	}

	resumedLease, err := store.AcquireLease(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
		rotated.Candidate.Version, fixture.actorID.String(), 5*time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire resumed lease: %v", err)
	}
	if resumedLease.Candidate.Version != 5 || resumedLease.Candidate.WriterLeaseEpoch != 4 ||
		resumedLease.Candidate.SessionEpoch != 2 || resumedLease.Candidate.Lease == nil {
		t.Fatalf("resumed Candidate lease = %#v", resumedLease.Candidate)
	}

	checkpoint, err := store.CreateCheckpoint(fixture.context, CreateCheckpointInput{
		ID: uuid.NewString(), ProjectID: fixture.projectID.String(), CandidateID: seed.candidateID.String(),
		ExpectedCandidateVersion: resumedLease.Candidate.Version,
		ExpectedSessionEpoch:     resumedLease.Candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: resumedLease.Candidate.WriterLeaseEpoch,
		ActorID:                  fixture.actorID.String(), Reason: "resume checkpoint",
	})
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	if checkpoint.CandidateVersion != resumedLease.Candidate.Version ||
		checkpoint.SessionEpoch != resumedLease.Candidate.SessionEpoch ||
		checkpoint.WriterLeaseEpoch != resumedLease.Candidate.WriterLeaseEpoch ||
		checkpoint.Tree.TreeHash != resumedLease.Candidate.CurrentTree.TreeHash || checkpoint.CreatedAt.IsZero() {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}

	replayed, err := store.CreateCheckpoint(fixture.context, CreateCheckpointInput{
		ID: uuid.NewString(), ProjectID: fixture.projectID.String(), CandidateID: seed.candidateID.String(),
		ExpectedCandidateVersion: resumedLease.Candidate.Version,
		ExpectedSessionEpoch:     resumedLease.Candidate.SessionEpoch,
		ExpectedWriterLeaseEpoch: resumedLease.Candidate.WriterLeaseEpoch,
		ActorID:                  fixture.actorID.String(), Reason: "retry with a new request id",
	})
	if err != nil || replayed.ID != checkpoint.ID {
		t.Fatalf("exact checkpoint retry = %#v, %v; want %s", replayed, err, checkpoint.ID)
	}

	frozen, err := store.Freeze(
		fixture.context, fixture.projectID.String(), seed.candidateID.String(),
		resumedLease.Candidate.Version, resumedLease.Candidate.SessionEpoch,
		resumedLease.Candidate.WriterLeaseEpoch, fixture.actorID.String(), checkpoint.ID,
		"freeze exact candidate for proposal",
	)
	if err != nil {
		t.Fatalf("freeze Candidate: %v", err)
	}
	if frozen.Candidate.Status != CandidateFrozen || frozen.Candidate.Version != 6 ||
		frozen.Candidate.Lease != nil || frozen.Candidate.WriterLeaseEpoch != 5 {
		t.Fatalf("frozen Candidate = %#v", frozen.Candidate)
	}
}
