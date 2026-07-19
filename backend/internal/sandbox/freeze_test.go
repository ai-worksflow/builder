package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	testFreezeReceiptID  = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	testFreezeProposalID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	testWrongCheckpoint  = "12121212-1212-4212-8212-121212121212"
)

type candidateFreezeProposalsFake struct {
	sessions    *facadeSessionStoreFake
	candidates  *facadeCandidatesFake
	checkpoint  repository.CandidateSnapshot
	now         time.Time
	committed   *core.CandidateImplementationResult
	request     core.CandidateImplementationRequest
	findCalls   int
	createCalls int
}

func (service *candidateFreezeProposalsFake) FindCandidateImplementation(
	_ context.Context,
	_, _ string,
	request core.CandidateImplementationRequest,
) (core.CandidateImplementationResult, bool, error) {
	service.findCalls++
	if service.committed == nil {
		return core.CandidateImplementationResult{}, false, nil
	}
	if request != service.request {
		return core.CandidateImplementationResult{}, true, core.ErrConflict
	}
	result := *service.committed
	result.Replayed = true
	return result, true, nil
}

func (service *candidateFreezeProposalsFake) CreateCandidateImplementation(
	_ context.Context,
	projectID, actorID string,
	request core.CandidateImplementationRequest,
	record repository.CandidateMutationRecord,
	_ core.CandidateFileResolver,
) (core.CandidateImplementationResult, error) {
	service.createCalls++
	if service.committed != nil {
		return core.CandidateImplementationResult{}, core.ErrConflict
	}
	frozen, _, err := record.Candidate.Freeze(
		request.ExpectedCandidateVersion,
		request.ExpectedSessionEpoch,
		actorID,
		request.Reason,
		service.checkpoint,
		service.now,
	)
	if err != nil {
		return core.CandidateImplementationResult{}, err
	}
	service.candidates.record = repository.CandidateMutationRecord{
		Candidate:          frozen,
		CurrentTreePointer: record.CurrentTreePointer,
	}
	projected := service.sessions.session.Clone()
	projected.document.Candidate = candidateState(frozen)
	projected.touch(service.now)
	if err := projected.Validate(); err != nil {
		return core.CandidateImplementationResult{}, err
	}
	service.sessions.session = projected

	baseRevision := cloneExactRevisionReference(record.Candidate.BaseWorkspaceRevision)
	source := &core.CandidateImplementationSource{
		FreezeReceiptID:      testFreezeReceiptID,
		RepositorySnapshotID: record.Candidate.RepositorySnapshotID,
		SessionID:            request.SessionID,
		CandidateID:          record.Candidate.ID,
		CandidateSnapshotID:  request.CheckpointID,
		CandidateVersion:     record.Candidate.Version,
		JournalSequence:      record.Candidate.JournalSequence,
		SessionEpoch:         record.Candidate.SessionEpoch,
		WriterLeaseEpoch:     record.Candidate.WriterLeaseEpoch,
		BaseTreeHash:         record.Candidate.BaseTreeHash,
		TreeHash:             record.Candidate.CurrentTree.TreeHash,
		FullStackTemplate: core.ExactContentReference{
			ID:          record.Candidate.FullStackTemplate.ID,
			ContentHash: record.Candidate.FullStackTemplate.ContentHash,
		},
	}
	result := core.CandidateImplementationResult{
		Proposal: core.ImplementationProposal{
			ID:              testFreezeProposalID,
			ProjectID:       projectID,
			BuildManifestID: record.Candidate.BuildManifest.ID,
			ExecutionSource: core.ImplementationSourceCandidateFreeze,
			CandidateSource: source,
			Status:          "open",
			Version:         1,
			PayloadHash:     sandboxDigest("4"),
			CreatedBy:       actorID,
			CreatedAt:       service.now,
		},
		Receipt: core.CandidateImplementationFreezeReceipt{
			ID:                       testFreezeReceiptID,
			ProjectID:                projectID,
			SessionID:                request.SessionID,
			CandidateID:              record.Candidate.ID,
			CandidateSnapshotID:      request.CheckpointID,
			ImplementationProposalID: testFreezeProposalID,
			RequestKey:               request.RequestKey,
			RequestHash:              sandboxDigest("5"),
			SessionVersion:           request.ExpectedSessionVersion,
			CandidateVersion:         record.Candidate.Version,
			JournalSequence:          record.Candidate.JournalSequence,
			SessionEpoch:             record.Candidate.SessionEpoch,
			WriterLeaseEpoch:         record.Candidate.WriterLeaseEpoch,
			BaseTreeHash:             record.Candidate.BaseTreeHash,
			CandidateTreeHash:        record.Candidate.CurrentTree.TreeHash,
			BuildManifest:            record.Candidate.BuildManifest,
			BuildContract:            record.Candidate.BuildContract,
			FullStackTemplate:        record.Candidate.FullStackTemplate,
			BaseWorkspaceRevision:    baseRevision,
			ProposalPayloadHash:      sandboxDigest("4"),
			Reason:                   request.Reason,
			CreatedBy:                actorID,
			CreatedAt:                service.now,
		},
	}
	service.request = request
	service.committed = &result
	return result, nil
}

func TestCandidateFreezeServiceCreatesImmutableProposalAndReplaysAfterFreeze(t *testing.T) {
	service, proposals, input := candidateFreezeFixture(t)

	result, err := service.Freeze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Candidate.Status != repository.CandidateFrozen {
		t.Fatalf("first freeze did not produce a frozen Candidate: %#v", result)
	}
	if result.Proposal.ExecutionSource != core.ImplementationSourceCandidateFreeze ||
		result.Proposal.CandidateSource == nil ||
		result.Proposal.CandidateSource.SessionID != input.SessionID {
		t.Fatalf("Proposal omitted exact Candidate lineage: %#v", result.Proposal)
	}
	if hasAction(result.Session.AllowedActions, ActionEdit) ||
		hasAction(result.Session.AllowedActions, ActionFreeze) ||
		!hasAction(result.Session.AllowedActions, ActionView) ||
		!hasAction(result.Session.AllowedActions, ActionTerminate) {
		t.Fatalf("frozen session exposed the wrong actions: %#v", result.Session.AllowedActions)
	}

	replay, err := service.Freeze(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Proposal.ID != result.Proposal.ID ||
		replay.Receipt.ID != result.Receipt.ID {
		t.Fatalf("intrinsic replay did not recover the original result: %#v", replay)
	}
	if proposals.createCalls != 1 || proposals.findCalls != 2 {
		t.Fatalf("freeze side effects were repeated: find=%d create=%d", proposals.findCalls, proposals.createCalls)
	}
}

func TestCandidateFreezeServiceRejectsNonExactCheckpointBeforeCreation(t *testing.T) {
	service, proposals, input := candidateFreezeFixture(t)
	input.CheckpointID = testWrongCheckpoint

	_, err := service.Freeze(context.Background(), input)
	if !errors.Is(err, ErrSessionProjectionStale) {
		t.Fatalf("non-exact checkpoint was not rejected: %v", err)
	}
	if proposals.createCalls != 0 || proposals.committed != nil {
		t.Fatalf("Proposal was created behind a failed checkpoint gate: %#v", proposals.committed)
	}
}

func candidateFreezeFixture(
	t *testing.T,
) (*CandidateFreezeService, *candidateFreezeProposalsFake, CandidateFreezeInput) {
	t.Helper()
	candidate, _ := dirtyCandidate(t, cleanCandidate(t), sandboxBaseTime.Add(time.Second))
	session := readyTestSession(t, candidate, sandboxBaseTime.Add(3*time.Second))
	checkpointAt := sandboxBaseTime.Add(6 * time.Second)
	checkpoint, err := candidate.Checkpoint(
		candidate.Version,
		candidate.SessionEpoch,
		candidate.WriterLeaseEpoch,
		testCheckpoint,
		testActorID,
		"freeze boundary",
		checkpointAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	view := session.Snapshot()
	session, err = session.RecordCheckpoint(
		view.Version,
		view.SessionEpoch,
		checkpoint,
		checkpointAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates := &facadeCandidatesFake{
		record: repository.CandidateMutationRecord{Candidate: candidate},
	}
	sessions := &facadeSessionStoreFake{
		session:    session,
		candidates: candidates,
		now:        checkpointAt.Add(3 * time.Second),
	}
	proposals := &candidateFreezeProposalsFake{
		sessions:   sessions,
		candidates: candidates,
		checkpoint: checkpoint,
		now:        checkpointAt.Add(2 * time.Second),
	}
	service, err := NewCandidateFreezeService(
		sessions,
		candidates,
		proposals,
		&facadeFilesFake{},
		&facadeAccessFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ready := session.Snapshot()
	return service, proposals, CandidateFreezeInput{
		ProjectID:                testProjectID,
		SessionID:                testSessionID,
		ActorID:                  testActorID,
		RequestKey:               "candidate-freeze-test",
		CheckpointID:             testCheckpoint,
		Reason:                   "Freeze exact Candidate into implementation Proposal",
		ExpectedSessionVersion:   ready.Version,
		ExpectedSessionEpoch:     ready.SessionEpoch,
		ExpectedCandidateVersion: ready.Candidate.Version,
		ExpectedWriterLeaseEpoch: ready.Candidate.WriterLeaseEpoch,
	}
}
