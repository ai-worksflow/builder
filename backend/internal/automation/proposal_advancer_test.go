package automation

import (
	"context"
	"errors"
	"testing"

	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

type proposalFake struct {
	value       domain.OutputProposal
	draft       core.ArtifactDraft
	decideCalls int
	applyCalls  int
}

func (fake *proposalFake) GetProposal(context.Context, string, string) (domain.OutputProposal, error) {
	return fake.value, nil
}

func (fake *proposalFake) Decide(
	_ context.Context,
	_ string,
	actorID string,
	input core.DecideProposalInput,
) (domain.OutputProposal, error) {
	fake.decideCalls++
	for index := range fake.value.Operations {
		operation := &fake.value.Operations[index]
		if operation.ID != input.OperationID || operation.Decision != domain.DecisionPending {
			continue
		}
		operation.Decision = input.Decision
		operation.DecidedBy = actorID
		operation.Reason = input.Reason
		fake.value.Version++
	}
	accepted := 0
	pending := 0
	for _, operation := range fake.value.Operations {
		if operation.Decision == domain.DecisionAccepted {
			accepted++
		}
		if operation.Decision == domain.DecisionPending {
			pending++
		}
	}
	if pending == 0 && accepted > 0 {
		fake.value.Status = domain.ProposalReady
	}
	return fake.value, nil
}

func (fake *proposalFake) Apply(
	context.Context,
	string,
	string,
	core.ApplyProposalInput,
) (core.ArtifactDraft, error) {
	fake.applyCalls++
	fake.value.Status = domain.ProposalApplied
	fake.value.Version++
	for index := range fake.value.Operations {
		if fake.value.Operations[index].Decision == domain.DecisionAccepted {
			fake.value.Operations[index].Decision = domain.DecisionApplied
		}
	}
	return fake.draft, nil
}

type artifactFake struct {
	resource            core.VersionedArtifact
	proposalID          string
	createRevisionCalls int
}

func (fake *artifactFake) Get(context.Context, string, string, bool) (core.VersionedArtifact, error) {
	return fake.resource, nil
}

func (fake *artifactFake) CreateRevision(
	_ context.Context,
	artifactID string,
	_ string,
	expectedETag string,
	input core.CreateRevisionInput,
) (core.ArtifactRevision, error) {
	fake.createRevisionCalls++
	if fake.resource.Draft == nil || fake.resource.Draft.ETag != expectedETag {
		return core.ArtifactRevision{}, core.ErrConflict
	}
	if input.ChangeSource != "ai_proposal" {
		return core.ArtifactRevision{}, errors.New("wrong change source")
	}
	revision := core.ArtifactRevision{
		ID: "revision-1", ArtifactID: artifactID, RevisionNumber: 2,
		ContentHash: "sha256:revision", WorkflowStatus: "draft",
		ProposalID: &fake.proposalID,
	}
	fake.resource.LatestRevision = &revision
	return revision, nil
}

type reviewFake struct {
	items       []core.ReviewRequest
	submitCalls int
	decideCalls int
}

func (fake *reviewFake) List(context.Context, string, string) ([]core.ReviewRequest, error) {
	return append([]core.ReviewRequest(nil), fake.items...), nil
}

func (fake *reviewFake) Submit(
	_ context.Context,
	projectID, artifactID, _ string,
	input core.SubmitReviewInput,
) (core.ReviewRequest, error) {
	fake.submitCalls++
	review := core.ReviewRequest{
		ID: "review-1", ProjectID: projectID, ArtifactID: artifactID,
		RevisionID: input.RevisionID, ContentHash: "sha256:revision",
		Status: "open", ETag: `"review:1"`,
		Policy: core.ReviewPolicy{
			ReviewerIDs: input.ReviewerIDs, MinimumApprovals: 1,
			GovernanceMode:        core.GovernanceModeSolo,
			SoloSelfReviewOwnerID: input.ReviewerIDs[0],
		},
	}
	fake.items = append(fake.items, review)
	return review, nil
}

func (fake *reviewFake) DecideIfMatch(
	_ context.Context,
	reviewID, _ string,
	expectedETag string,
	input core.DecideReviewInput,
) (core.ReviewRequest, error) {
	fake.decideCalls++
	for index := range fake.items {
		review := &fake.items[index]
		if review.ID != reviewID || review.ETag != expectedETag {
			continue
		}
		if input.Decision != "approve" || !input.SoloReviewConfirmed || input.Summary == "" {
			return core.ReviewRequest{}, core.ErrInvalidInput
		}
		review.Status = "approved"
		review.ETag = `"review:2"`
		return *review, nil
	}
	return core.ReviewRequest{}, core.ErrConflict
}

func TestAdvanceProposalCompletesAndResumesExactSoloReview(t *testing.T) {
	proposal := &proposalFake{
		value: domain.OutputProposal{
			ID: "proposal-1", ProjectID: "project-1", ArtifactID: "artifact-1",
			Status: domain.ProposalOpen, Version: 1,
			Operations: []domain.ProposalOperation{
				{ID: "operation-a", Decision: domain.DecisionPending},
				{ID: "operation-b", Decision: domain.DecisionPending},
			},
		},
		draft: core.ArtifactDraft{
			ID: "draft-1", ArtifactID: "artifact-1",
			ContentHash: "sha256:revision", ETag: `"draft:1"`,
		},
	}
	artifacts := &artifactFake{
		proposalID: "proposal-1",
		resource: core.VersionedArtifact{
			Artifact: core.Artifact{ID: "artifact-1", ProjectID: "project-1"},
			Draft:    &proposal.draft,
		},
	}
	reviews := &reviewFake{}
	service, err := NewService(proposal, artifacts, reviews)
	if err != nil {
		t.Fatal(err)
	}
	input := AdvanceProposalInput{
		AcceptedOperationIDs: []string{"operation-a", "operation-b"},
		ReviewerIDs:          []string{"owner-1"},
		ReviewSummary:        "Reviewed the exact generated change.",
		ApproveReview:        true,
		SoloReviewConfirmed:  true,
	}
	result, err := service.AdvanceProposal(context.Background(), "proposal-1", "owner-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stage != "approved" || result.Revision.ID != "revision-1" || result.Review.Status != "approved" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if proposal.decideCalls != 2 || proposal.applyCalls != 1 ||
		artifacts.createRevisionCalls != 1 || reviews.submitCalls != 1 || reviews.decideCalls != 1 {
		t.Fatalf(
			"calls decide=%d apply=%d revision=%d submit=%d review=%d",
			proposal.decideCalls,
			proposal.applyCalls,
			artifacts.createRevisionCalls,
			reviews.submitCalls,
			reviews.decideCalls,
		)
	}

	resumed, err := service.AdvanceProposal(context.Background(), "proposal-1", "owner-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != "approved" || proposal.applyCalls != 1 ||
		artifacts.createRevisionCalls != 1 || reviews.submitCalls != 1 || reviews.decideCalls != 1 {
		t.Fatalf("resume repeated a committed operation: %#v", resumed)
	}
}

func TestAdvanceProposalRejectsChangedSelectionDuringResume(t *testing.T) {
	proposals := &proposalFake{value: domain.OutputProposal{
		ID: "proposal-1", ProjectID: "project-1", ArtifactID: "artifact-1",
		Status: domain.ProposalReady, Version: 3,
		Operations: []domain.ProposalOperation{
			{ID: "operation-a", Decision: domain.DecisionAccepted},
			{ID: "operation-b", Decision: domain.DecisionRejected},
		},
	}}
	service, err := NewService(proposals, &artifactFake{}, &reviewFake{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AdvanceProposal(context.Background(), "proposal-1", "owner-1", AdvanceProposalInput{
		AcceptedOperationIDs: []string{"operation-b"},
		ReviewerIDs:          []string{"owner-1"},
		ReviewSummary:        "Changed selection",
	})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
	if proposals.applyCalls != 0 {
		t.Fatal("changed selection reached apply")
	}
}

func TestAdvanceProposalRejectsEmptySelectionBeforeMutation(t *testing.T) {
	proposals := &proposalFake{value: domain.OutputProposal{
		ID: "proposal-1", ProjectID: "project-1", ArtifactID: "artifact-1",
		Status: domain.ProposalOpen, Version: 1,
		Operations: []domain.ProposalOperation{
			{ID: "operation-a", Decision: domain.DecisionPending},
		},
	}}
	service, err := NewService(proposals, &artifactFake{}, &reviewFake{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AdvanceProposal(context.Background(), "proposal-1", "owner-1", AdvanceProposalInput{
		ReviewerIDs:   []string{"owner-1"},
		ReviewSummary: "No operation selected",
	})
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input", err)
	}
	if proposals.decideCalls != 0 || proposals.applyCalls != 0 {
		t.Fatal("empty selection mutated proposal state")
	}
}
