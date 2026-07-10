package application

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/worksflow/builder/backend/internal/domain"
)

type ProposalService struct {
	Artifacts ArtifactRepository
	Revisions RevisionRepository
	Drafts    DraftRepository
	Manifests ManifestRepository
	Proposals ProposalRepository
	Tx        TransactionManager
	Clock     Clock
	IDs       IDGenerator
}

type CreateManifestCommand struct {
	ID                  string
	ProjectID           string
	JobType             string
	DeliverySliceID     string
	BaseRevision        *domain.ArtifactRef
	Sources             []domain.ManifestSource
	Constraints         json.RawMessage
	OutputSchemaVersion string
	CreatedBy           string
}

func (s ProposalService) CreateManifest(ctx context.Context, command CreateManifestCommand) (domain.InputManifest, error) {
	if s.Manifests == nil {
		return domain.InputManifest{}, domain.ErrInvalidArgument
	}
	id := command.ID
	if id == "" && s.IDs != nil {
		id = s.IDs.NewID("manifest")
	}
	manifest, err := domain.NewInputManifest(
		id, command.ProjectID, command.JobType, command.DeliverySliceID,
		command.BaseRevision, command.Sources, command.Constraints,
		command.OutputSchemaVersion, command.CreatedBy, serviceNow(s.Clock),
	)
	if err != nil {
		return domain.InputManifest{}, err
	}
	if err := s.Manifests.Create(ctx, manifest); err != nil {
		return domain.InputManifest{}, err
	}
	return manifest, nil
}

type CreateProposalCommand struct {
	ID          string
	ManifestID  string
	ArtifactID  string
	Operations  []domain.ProposalOperation
	Assumptions []string
	Questions   []string
	CreatedBy   string
}

func (s ProposalService) CreateProposal(ctx context.Context, command CreateProposalCommand) (*domain.OutputProposal, error) {
	if s.Manifests == nil || s.Proposals == nil {
		return nil, domain.ErrInvalidArgument
	}
	manifest, err := s.Manifests.Get(ctx, command.ManifestID)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.BaseRevision == nil {
		return nil, &domain.DomainError{Kind: domain.ErrManifestUnpinned, Field: "manifest.baseRevision", Message: "a proposal must pin the artifact revision it patches"}
	}
	if manifest.BaseRevision.ArtifactID != command.ArtifactID {
		return nil, &domain.DomainError{Kind: domain.ErrInvalidArgument, Field: "proposal.artifactId", Message: "does not match manifest base revision"}
	}
	id := command.ID
	if id == "" && s.IDs != nil {
		id = s.IDs.NewID("proposal")
	}
	proposal, err := domain.NewOutputProposal(
		id, manifest.ProjectID, command.ArtifactID, manifest.Ref(), *manifest.BaseRevision,
		command.Operations, command.Assumptions, command.Questions, command.CreatedBy, serviceNow(s.Clock),
	)
	if err != nil {
		return nil, err
	}
	if err := s.Proposals.Create(ctx, proposal); err != nil {
		return nil, err
	}
	return proposal, nil
}

func (s ProposalService) Decide(ctx context.Context, proposalID, operationID string, decision domain.ProposalDecision, actorID, reason string, expectedVersion uint64) (*domain.OutputProposal, error) {
	proposal, err := s.Proposals.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if err := proposal.ValidatePayloadHash(); err != nil {
		return nil, err
	}
	if err := proposal.Decide(operationID, decision, actorID, reason, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.Proposals.Save(ctx, proposal, expectedVersion); err != nil {
		return nil, err
	}
	return proposal, nil
}

type ApplyProposalCommand struct {
	ProposalID              string
	DraftID                 string
	ActorID                 string
	ExpectedProposalVersion uint64
	ExpectedArtifactVersion uint64
}

func (s ProposalService) Apply(ctx context.Context, command ApplyProposalCommand) (*domain.Draft, error) {
	if s.Artifacts == nil || s.Revisions == nil || s.Drafts == nil || s.Manifests == nil || s.Proposals == nil {
		return nil, domain.ErrInvalidArgument
	}
	proposal, err := s.Proposals.Get(ctx, command.ProposalID)
	if err != nil {
		return nil, err
	}
	if proposal.Version != command.ExpectedProposalVersion {
		return nil, domain.ErrConflict
	}
	if err := proposal.ValidatePayloadHash(); err != nil {
		return nil, err
	}
	manifest, err := s.Manifests.Get(ctx, proposal.Manifest.ID)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.Ref() != proposal.Manifest || manifest.BaseRevision == nil || !manifest.BaseRevision.Equal(proposal.BaseRevision) {
		return nil, &domain.DomainError{Kind: domain.ErrConflict, Field: "proposal.manifest", Message: "proposal is not bound to the stored input manifest"}
	}
	artifact, err := s.Artifacts.Get(ctx, proposal.ArtifactID)
	if err != nil {
		return nil, err
	}
	if artifact.Version != command.ExpectedArtifactVersion {
		return nil, domain.ErrConflict
	}
	currentRef, currentRevision, err := currentArtifactRevision(ctx, s.Revisions, artifact)
	if err != nil {
		return nil, err
	}
	if currentRevision == nil || !proposal.BaseRevision.Equal(currentRef) {
		expected := proposal.Version
		if err := proposal.MarkStale(currentRef, expected); err != nil {
			return nil, err
		}
		if err := s.Proposals.Save(ctx, proposal, expected); err != nil {
			return nil, err
		}
		return nil, &domain.DomainError{Kind: domain.ErrStaleProposal, Field: "proposal.baseRevision", Message: fmt.Sprintf("expected %s, current revision is %s", proposal.BaseRevision.RevisionID, currentRef.RevisionID)}
	}
	operations, err := proposal.AcceptedOperations()
	if err != nil {
		return nil, err
	}
	content, err := domain.ApplyProposalPatch(currentRevision.Content(), operations)
	if err != nil {
		return nil, err
	}
	draftID := command.DraftID
	if draftID == "" && s.IDs != nil {
		draftID = s.IDs.NewID("draft")
	}
	now := serviceNow(s.Clock)
	draft, err := domain.NewDraft(draftID, artifact.ID, command.ActorID, &currentRef, content, now)
	if err != nil {
		return nil, err
	}
	draft.SourceManifestID = manifest.ID
	proposalVersion := proposal.Version
	if err := proposal.MarkApplied(proposalVersion, now); err != nil {
		return nil, err
	}
	err = inTransaction(ctx, s.Tx, func(tx context.Context) error {
		if err := s.Drafts.Create(tx, draft); err != nil {
			return err
		}
		return s.Proposals.Save(tx, proposal, proposalVersion)
	})
	if err != nil {
		return nil, err
	}
	return draft, nil
}

func currentArtifactRevision(ctx context.Context, revisions RevisionRepository, artifact *domain.Artifact) (domain.ArtifactRef, *domain.Revision, error) {
	if artifact.CurrentRevisionID == "" {
		return domain.ArtifactRef{}, nil, nil
	}
	revision, err := revisions.Get(ctx, artifact.CurrentRevisionID)
	if err != nil {
		return domain.ArtifactRef{}, nil, err
	}
	ref := revision.Ref("")
	return ref, &revision, nil
}
