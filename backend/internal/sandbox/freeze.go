package sandbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/repository"
)

var ErrFreezeUnavailable = errors.New("sandbox Candidate freeze is not configured")

type CandidateImplementationProposals interface {
	FindCandidateImplementation(
		context.Context,
		string,
		string,
		core.CandidateImplementationRequest,
	) (core.CandidateImplementationResult, bool, error)
	CreateCandidateImplementation(
		context.Context,
		string,
		string,
		core.CandidateImplementationRequest,
		repository.CandidateMutationRecord,
		core.CandidateFileResolver,
	) (core.CandidateImplementationResult, error)
}

type CandidateFreezeInput struct {
	ProjectID                string
	SessionID                string
	ActorID                  string
	RequestKey               string
	CheckpointID             string
	VerificationReceipt      repository.ExactReference
	Reason                   string
	ExpectedSessionVersion   uint64
	ExpectedSessionEpoch     uint64
	ExpectedCandidateVersion uint64
	ExpectedWriterLeaseEpoch uint64
}

type CandidateFreezeResult struct {
	Session   SessionView                               `json:"session"`
	Candidate repository.CandidateWorkspace             `json:"candidate"`
	Proposal  core.ImplementationProposal               `json:"proposal"`
	Receipt   core.CandidateImplementationFreezeReceipt `json:"receipt"`
	Replayed  bool                                      `json:"replayed"`
}

type CandidateFreezeService struct {
	sessions   SessionStore
	candidates CandidateControls
	proposals  CandidateImplementationProposals
	files      RepositoryFileBlobs
	access     ProjectAuthorizer
}

func NewCandidateFreezeService(
	sessions SessionStore,
	candidates CandidateControls,
	proposals CandidateImplementationProposals,
	files RepositoryFileBlobs,
	access ProjectAuthorizer,
) (*CandidateFreezeService, error) {
	if sessions == nil || candidates == nil || proposals == nil || files == nil || access == nil {
		return nil, ErrFreezeUnavailable
	}
	return &CandidateFreezeService{
		sessions: sessions, candidates: candidates, proposals: proposals, files: files, access: access,
	}, nil
}

func (service *CandidateFreezeService) Freeze(
	ctx context.Context,
	input CandidateFreezeInput,
) (CandidateFreezeResult, error) {
	if service == nil {
		return CandidateFreezeResult{}, ErrFreezeUnavailable
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return CandidateFreezeResult{}, fmt.Errorf("authorize Candidate freeze: %w", err)
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return CandidateFreezeResult{}, err
	}
	view := session.Snapshot()
	request := core.CandidateImplementationRequest{
		RequestKey: input.RequestKey, SessionID: input.SessionID,
		CandidateID:  view.Candidate.ID,
		CheckpointID: input.CheckpointID, Reason: input.Reason,
		VerificationReceipt:      input.VerificationReceipt,
		ExpectedSessionVersion:   input.ExpectedSessionVersion,
		ExpectedSessionEpoch:     input.ExpectedSessionEpoch,
		ExpectedCandidateVersion: input.ExpectedCandidateVersion,
		ExpectedWriterLeaseEpoch: input.ExpectedWriterLeaseEpoch,
	}

	// Read the immutable receipt before authorizing the live "freeze" action.
	// A committed freeze intentionally makes that action unavailable, while a
	// retry with the same request must still recover its original Proposal.
	if replay, found, err := service.proposals.FindCandidateImplementation(
		ctx, input.ProjectID, input.ActorID, request,
	); found || err != nil {
		if err != nil {
			return CandidateFreezeResult{}, err
		}
		return service.result(ctx, input, replay)
	}

	if err := session.Authorize(ActionFreeze, input.ExpectedSessionVersion, input.ExpectedSessionEpoch); err != nil {
		return CandidateFreezeResult{}, err
	}
	if view.Candidate.Version != input.ExpectedCandidateVersion ||
		view.Candidate.WriterLeaseEpoch != input.ExpectedWriterLeaseEpoch ||
		!exactFreezeCheckpoint(view, input.CheckpointID) {
		return CandidateFreezeResult{}, ErrSessionProjectionStale
	}
	record, err := service.candidates.Get(ctx, input.ProjectID, view.Candidate.ID)
	if err != nil {
		return CandidateFreezeResult{}, err
	}
	if !candidateProjectionMatches(view.Candidate, record.Candidate) {
		return CandidateFreezeResult{}, ErrSessionProjectionStale
	}
	created, err := service.proposals.CreateCandidateImplementation(
		ctx, input.ProjectID, input.ActorID, request, record, service.files,
	)
	if err != nil {
		return CandidateFreezeResult{}, err
	}
	return service.result(ctx, input, created)
}

func (service *CandidateFreezeService) result(
	ctx context.Context,
	input CandidateFreezeInput,
	implementation core.CandidateImplementationResult,
) (CandidateFreezeResult, error) {
	if implementation.Receipt.ProjectID != input.ProjectID ||
		implementation.Receipt.SessionID != input.SessionID ||
		implementation.Receipt.CandidateSnapshotID != input.CheckpointID ||
		implementation.Receipt.VerificationReceipt != input.VerificationReceipt ||
		implementation.Receipt.RequestKey != input.RequestKey {
		return CandidateFreezeResult{}, ErrSessionProjectionStale
	}
	session, err := service.sessions.Get(ctx, input.ProjectID, input.SessionID)
	if err != nil {
		return CandidateFreezeResult{}, errors.Join(ErrSessionReconciliation, err)
	}
	view := session.Snapshot()
	record, err := service.candidates.Get(ctx, input.ProjectID, implementation.Receipt.CandidateID)
	if err != nil {
		return CandidateFreezeResult{}, errors.Join(ErrSessionReconciliation, err)
	}
	if record.Candidate.Status != repository.CandidateFrozen ||
		record.Candidate.Version != implementation.Receipt.CandidateVersion+1 ||
		record.Candidate.CurrentTree.TreeHash != implementation.Receipt.CandidateTreeHash ||
		!candidateProjectionMatches(view.Candidate, record.Candidate) {
		return CandidateFreezeResult{}, ErrSessionProjectionStale
	}
	return CandidateFreezeResult{
		Session: view, Candidate: record.Candidate, Proposal: implementation.Proposal,
		Receipt: implementation.Receipt, Replayed: implementation.Replayed,
	}, nil
}

func exactFreezeCheckpoint(view SessionView, checkpointID string) bool {
	checkpoint := view.LatestCheckpoint
	return checkpoint != nil && checkpoint.ID == checkpointID &&
		checkpoint.CandidateID == view.Candidate.ID &&
		checkpoint.CandidateVersion == view.Candidate.Version &&
		checkpoint.JournalSequence == view.Candidate.JournalSequence &&
		checkpoint.SessionEpoch == view.Candidate.SessionEpoch &&
		checkpoint.WriterLeaseEpoch == view.Candidate.WriterLeaseEpoch &&
		checkpoint.TreeHash == view.Candidate.TreeHash
}

var _ core.CandidateFileResolver = (RepositoryFileBlobs)(nil)
