package automation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type ProposalService interface {
	GetProposal(context.Context, string, string) (domain.OutputProposal, error)
	Decide(context.Context, string, string, core.DecideProposalInput) (domain.OutputProposal, error)
	Apply(context.Context, string, string, core.ApplyProposalInput) (core.ArtifactDraft, error)
}

type ArtifactService interface {
	Get(context.Context, string, string, bool) (core.VersionedArtifact, error)
	CreateRevision(context.Context, string, string, string, core.CreateRevisionInput) (core.ArtifactRevision, error)
}

type ReviewService interface {
	Submit(context.Context, string, string, string, core.SubmitReviewInput) (core.ReviewRequest, error)
	List(context.Context, string, string) ([]core.ReviewRequest, error)
	DecideIfMatch(context.Context, string, string, string, core.DecideReviewInput) (core.ReviewRequest, error)
}

type AdvanceProposalInput struct {
	AcceptedOperationIDs []string `json:"acceptedOperationIds"`
	ReviewerIDs          []string `json:"reviewerIds"`
	ReviewSummary        string   `json:"reviewSummary"`
	ApproveReview        bool     `json:"approveReview,omitempty"`
	SoloReviewConfirmed  bool     `json:"soloReviewConfirmed,omitempty"`
}

type AdvanceProposalResult struct {
	Stage    string                `json:"stage"`
	Proposal domain.OutputProposal `json:"proposal"`
	Draft    *core.ArtifactDraft   `json:"draft,omitempty"`
	Revision core.ArtifactRevision `json:"revision"`
	Review   core.ReviewRequest    `json:"review"`
}

type Service struct {
	proposals ProposalService
	artifacts ArtifactService
	reviews   ReviewService
}

func NewService(
	proposals ProposalService,
	artifacts ArtifactService,
	reviews ReviewService,
) (*Service, error) {
	if proposals == nil || artifacts == nil || reviews == nil {
		return nil, errors.New("proposal automation services are required")
	}
	return &Service{proposals: proposals, artifacts: artifacts, reviews: reviews}, nil
}

// AdvanceProposal moves one exact reviewed output proposal to its next durable
// human decision point. Every step is re-entrant: a retry reloads authoritative
// state and resumes after the last committed boundary.
func (s *Service) AdvanceProposal(
	ctx context.Context,
	proposalID, actorID string,
	input AdvanceProposalInput,
) (AdvanceProposalResult, error) {
	if s == nil || ctx == nil || strings.TrimSpace(proposalID) == "" || strings.TrimSpace(actorID) == "" {
		return AdvanceProposalResult{}, fmt.Errorf("%w: proposal automation identity", core.ErrInvalidInput)
	}
	accepted, err := exactIDSet("accepted operation", input.AcceptedOperationIDs, true)
	if err != nil {
		return AdvanceProposalResult{}, err
	}
	reviewers, err := exactIDSet("reviewer", input.ReviewerIDs, false)
	if err != nil {
		return AdvanceProposalResult{}, err
	}
	input.ReviewSummary = strings.TrimSpace(input.ReviewSummary)
	if len([]byte(input.ReviewSummary)) > 4096 || input.ReviewSummary == "" {
		return AdvanceProposalResult{}, fmt.Errorf("%w: review summary", core.ErrInvalidInput)
	}
	if input.SoloReviewConfirmed && !input.ApproveReview {
		return AdvanceProposalResult{}, fmt.Errorf("%w: solo review confirmation requires approval", core.ErrInvalidInput)
	}

	proposal, err := s.proposals.GetProposal(ctx, proposalID, actorID)
	if err != nil {
		return AdvanceProposalResult{}, err
	}
	proposal, err = s.decidePending(ctx, proposal, actorID, accepted)
	if err != nil {
		return AdvanceProposalResult{}, err
	}

	var appliedDraft *core.ArtifactDraft
	switch proposal.Status {
	case domain.ProposalReady:
		draft, applyErr := s.proposals.Apply(
			ctx,
			proposal.ID,
			actorID,
			core.ApplyProposalInput{Version: proposal.Version},
		)
		if applyErr != nil {
			return AdvanceProposalResult{}, fmt.Errorf("apply reviewed proposal: %w", applyErr)
		}
		appliedDraft = &draft
		proposal, err = s.proposals.GetProposal(ctx, proposal.ID, actorID)
		if err != nil {
			return AdvanceProposalResult{}, err
		}
	case domain.ProposalApplied, domain.ProposalPartiallyApplied:
	default:
		return AdvanceProposalResult{}, fmt.Errorf(
			"%w: proposal status %s cannot advance",
			core.ErrConflict,
			proposal.Status,
		)
	}

	revision, err := s.ensureRevision(ctx, proposal, actorID, appliedDraft)
	if err != nil {
		return AdvanceProposalResult{}, err
	}
	review, err := s.ensureReview(ctx, proposal, revision, actorID, sortedKeys(reviewers), input)
	if err != nil {
		return AdvanceProposalResult{}, err
	}
	stage := "review_requested"
	if review.Status == "approved" {
		stage = "approved"
	}
	return AdvanceProposalResult{
		Stage: stage, Proposal: proposal, Draft: appliedDraft, Revision: revision, Review: review,
	}, nil
}

func (s *Service) decidePending(
	ctx context.Context,
	proposal domain.OutputProposal,
	actorID string,
	accepted map[string]bool,
) (domain.OutputProposal, error) {
	known := make(map[string]bool, len(proposal.Operations))
	finalAccepted := 0
	for _, operation := range proposal.Operations {
		known[operation.ID] = true
		switch operation.Decision {
		case domain.DecisionAccepted, domain.DecisionApplied:
			if !accepted[operation.ID] {
				return domain.OutputProposal{}, fmt.Errorf(
					"%w: accepted operation %s is missing from the confirmed selection",
					core.ErrConflict,
					operation.ID,
				)
			}
			finalAccepted++
		case domain.DecisionRejected:
			if accepted[operation.ID] {
				return domain.OutputProposal{}, fmt.Errorf(
					"%w: rejected operation %s cannot be accepted during resume",
					core.ErrConflict,
					operation.ID,
				)
			}
		}
	}
	for operationID := range accepted {
		if !known[operationID] {
			return domain.OutputProposal{}, fmt.Errorf(
				"%w: unknown accepted operation %s",
				core.ErrInvalidInput,
				operationID,
			)
		}
	}
	current := proposal
	for _, operation := range proposal.Operations {
		if operation.Decision != domain.DecisionPending {
			continue
		}
		decision := domain.DecisionRejected
		reason := "Not selected in the confirmed platform automation request."
		if accepted[operation.ID] {
			decision = domain.DecisionAccepted
			reason = ""
			finalAccepted++
		}
		current, _ = refreshOperation(current, operation.ID)
		updated, err := s.proposals.Decide(ctx, current.ID, actorID, core.DecideProposalInput{
			OperationID: operation.ID,
			Decision:    decision,
			Reason:      reason,
			Version:     current.Version,
		})
		if err != nil {
			return domain.OutputProposal{}, fmt.Errorf("decide proposal operation %s: %w", operation.ID, err)
		}
		current = updated
	}
	if finalAccepted == 0 {
		return domain.OutputProposal{}, fmt.Errorf("%w: at least one operation must be accepted", core.ErrInvalidInput)
	}
	return current, nil
}

func (s *Service) ensureRevision(
	ctx context.Context,
	proposal domain.OutputProposal,
	actorID string,
	appliedDraft *core.ArtifactDraft,
) (core.ArtifactRevision, error) {
	resource, err := s.artifacts.Get(ctx, proposal.ArtifactID, actorID, true)
	if err != nil {
		return core.ArtifactRevision{}, err
	}
	if revision := proposalRevision(resource.LatestRevision, proposal.ID); revision != nil {
		return *revision, nil
	}
	draft := appliedDraft
	if draft == nil {
		draft = resource.Draft
	}
	if draft == nil || draft.ArtifactID != proposal.ArtifactID {
		return core.ArtifactRevision{}, fmt.Errorf("%w: applied proposal draft is unavailable", core.ErrConflict)
	}
	revision, err := s.artifacts.CreateRevision(
		ctx,
		proposal.ArtifactID,
		actorID,
		draft.ETag,
		core.CreateRevisionInput{
			ChangeSource:  "ai_proposal",
			ChangeSummary: "Apply reviewed AI proposal",
		},
	)
	if err == nil {
		return revision, nil
	}
	// A retry racing the first successful call can observe a stale draft ETag.
	// Reload once and accept only the exact revision bound to this proposal.
	refreshed, refreshErr := s.artifacts.Get(ctx, proposal.ArtifactID, actorID, true)
	if refreshErr == nil {
		if exact := proposalRevision(refreshed.LatestRevision, proposal.ID); exact != nil {
			return *exact, nil
		}
	}
	return core.ArtifactRevision{}, fmt.Errorf("freeze applied proposal: %w", err)
}

func (s *Service) ensureReview(
	ctx context.Context,
	proposal domain.OutputProposal,
	revision core.ArtifactRevision,
	actorID string,
	reviewerIDs []string,
	input AdvanceProposalInput,
) (core.ReviewRequest, error) {
	reviews, err := s.reviews.List(ctx, proposal.ProjectID, actorID)
	if err != nil {
		return core.ReviewRequest{}, err
	}
	review := exactRevisionReview(reviews, proposal.ArtifactID, revision.ID, revision.ContentHash)
	if review == nil {
		if len(reviewerIDs) == 0 {
			return core.ReviewRequest{}, fmt.Errorf("%w: at least one reviewer is required", core.ErrInvalidInput)
		}
		created, submitErr := s.reviews.Submit(
			ctx,
			proposal.ProjectID,
			proposal.ArtifactID,
			actorID,
			core.SubmitReviewInput{
				RevisionID:        revision.ID,
				ReviewerIDs:       reviewerIDs,
				MinimumApprovals:  1,
				AllowSelfApproval: input.SoloReviewConfirmed,
			},
		)
		if submitErr != nil {
			return core.ReviewRequest{}, fmt.Errorf("request canonical review: %w", submitErr)
		}
		review = &created
	}
	if review.Status == "approved" || !input.ApproveReview {
		return *review, nil
	}
	if review.Status != "open" {
		return core.ReviewRequest{}, fmt.Errorf(
			"%w: canonical review status %s cannot be approved",
			core.ErrConflict,
			review.Status,
		)
	}
	approved, err := s.reviews.DecideIfMatch(
		ctx,
		review.ID,
		actorID,
		review.ETag,
		core.DecideReviewInput{
			Decision:            "approve",
			Summary:             input.ReviewSummary,
			SoloReviewConfirmed: input.SoloReviewConfirmed,
		},
	)
	if err != nil {
		return core.ReviewRequest{}, fmt.Errorf("approve canonical review: %w", err)
	}
	return approved, nil
}

func exactIDSet(label string, values []string, allowEmpty bool) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || result[value] {
			return nil, fmt.Errorf("%w: %s ids must be non-empty and unique", core.ErrInvalidInput, label)
		}
		result[value] = true
	}
	if !allowEmpty && len(result) == 0 {
		return nil, fmt.Errorf("%w: at least one %s id is required", core.ErrInvalidInput, label)
	}
	return result, nil
}

func sortedKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func refreshOperation(
	proposal domain.OutputProposal,
	operationID string,
) (domain.OutputProposal, domain.ProposalOperation) {
	for _, operation := range proposal.Operations {
		if operation.ID == operationID {
			return proposal, operation
		}
	}
	return proposal, domain.ProposalOperation{}
}

func proposalRevision(revision *core.ArtifactRevision, proposalID string) *core.ArtifactRevision {
	if revision == nil || revision.ProposalID == nil || *revision.ProposalID != proposalID {
		return nil
	}
	return revision
}

func exactRevisionReview(
	reviews []core.ReviewRequest,
	artifactID, revisionID, contentHash string,
) *core.ReviewRequest {
	var open *core.ReviewRequest
	for index := range reviews {
		review := &reviews[index]
		if review.ArtifactID != artifactID || review.RevisionID != revisionID || review.ContentHash != contentHash {
			continue
		}
		if review.Status == "approved" {
			return review
		}
		if review.Status == "open" && open == nil {
			open = review
		}
	}
	return open
}
