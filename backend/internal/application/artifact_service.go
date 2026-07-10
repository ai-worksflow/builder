package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

type ArtifactService struct {
	Artifacts ArtifactRepository
	Revisions RevisionRepository
	Drafts    DraftRepository
	Reviews   ReviewRepository
	Tx        TransactionManager
	Clock     Clock
	IDs       IDGenerator
}

type CreateArtifactCommand struct {
	ID        string
	ProjectID string
	Type      domain.ArtifactType
	Title     string
}

func (s ArtifactService) CreateArtifact(ctx context.Context, command CreateArtifactCommand) (*domain.Artifact, error) {
	if s.Artifacts == nil {
		return nil, domain.ErrInvalidArgument
	}
	id := command.ID
	if id == "" && s.IDs != nil {
		id = s.IDs.NewID("artifact")
	}
	artifact, err := domain.NewArtifact(id, command.ProjectID, command.Type, command.Title, serviceNow(s.Clock))
	if err != nil {
		return nil, err
	}
	if err := s.Artifacts.Create(ctx, artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

type CreateDraftCommand struct {
	ID               string
	ArtifactID       string
	AuthorID         string
	Content          json.RawMessage
	SourceManifestID string
}

func (s ArtifactService) CreateDraft(ctx context.Context, command CreateDraftCommand) (*domain.Draft, error) {
	if s.Artifacts == nil || s.Revisions == nil || s.Drafts == nil {
		return nil, domain.ErrInvalidArgument
	}
	artifact, err := s.Artifacts.Get(ctx, command.ArtifactID)
	if err != nil {
		return nil, err
	}
	if artifact.ArchivedAt != nil {
		return nil, &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "artifact", Message: "cannot create a draft for an archived artifact"}
	}
	var base *domain.ArtifactRef
	if artifact.CurrentRevisionID != "" {
		revision, err := s.Revisions.Get(ctx, artifact.CurrentRevisionID)
		if err != nil {
			return nil, err
		}
		ref := revision.Ref("")
		base = &ref
	}
	id := command.ID
	if id == "" && s.IDs != nil {
		id = s.IDs.NewID("draft")
	}
	draft, err := domain.NewDraft(id, artifact.ID, command.AuthorID, base, command.Content, serviceNow(s.Clock))
	if err != nil {
		return nil, err
	}
	draft.SourceManifestID = strings.TrimSpace(command.SourceManifestID)
	if err := s.Drafts.Create(ctx, draft); err != nil {
		return nil, err
	}
	return draft, nil
}

func (s ArtifactService) UpdateDraft(ctx context.Context, draftID string, content json.RawMessage, expectedVersion uint64) (*domain.Draft, error) {
	draft, err := s.Drafts.Get(ctx, draftID)
	if err != nil {
		return nil, err
	}
	if err := draft.UpdateContent(content, expectedVersion, serviceNow(s.Clock)); err != nil {
		return nil, err
	}
	if err := s.Drafts.Save(ctx, draft, expectedVersion); err != nil {
		return nil, err
	}
	return draft, nil
}

type SubmitReviewCommand struct {
	ReviewID             string
	DraftID              string
	ReviewerID           string
	ExpectedDraftVersion uint64
}

func (s ArtifactService) SubmitReview(ctx context.Context, command SubmitReviewCommand) (*domain.Review, error) {
	if s.Drafts == nil || s.Reviews == nil {
		return nil, domain.ErrInvalidArgument
	}
	draft, err := s.Drafts.Get(ctx, command.DraftID)
	if err != nil {
		return nil, err
	}
	now := serviceNow(s.Clock)
	if err := draft.Submit(command.ExpectedDraftVersion, now); err != nil {
		return nil, err
	}
	reviewID := command.ReviewID
	if reviewID == "" && s.IDs != nil {
		reviewID = s.IDs.NewID("review")
	}
	review, err := domain.NewReview(reviewID, *draft, command.ReviewerID, now)
	if err != nil {
		return nil, err
	}
	err = inTransaction(ctx, s.Tx, func(tx context.Context) error {
		if err := s.Drafts.Save(tx, draft, command.ExpectedDraftVersion); err != nil {
			return err
		}
		return s.Reviews.Create(tx, review)
	})
	if err != nil {
		return nil, err
	}
	return review, nil
}

type ReviewDecision string

const (
	ApproveReview        ReviewDecision = "approve"
	RequestReviewChanges ReviewDecision = "request_changes"
	DismissReview        ReviewDecision = "dismiss"
)

type DecideReviewCommand struct {
	ReviewID              string
	ActorID               string
	Decision              ReviewDecision
	Summary               string
	ExpectedReviewVersion uint64
	ExpectedDraftVersion  uint64
}

func (s ArtifactService) DecideReview(ctx context.Context, command DecideReviewCommand) error {
	if s.Drafts == nil || s.Reviews == nil {
		return domain.ErrInvalidArgument
	}
	review, err := s.Reviews.Get(ctx, command.ReviewID)
	if err != nil {
		return err
	}
	draft, err := s.Drafts.Get(ctx, review.DraftID)
	if err != nil {
		return err
	}
	if draft.Version != command.ExpectedDraftVersion {
		return &domain.DomainError{Kind: domain.ErrConflict, Field: "draft", Message: "draft version changed while review was open"}
	}
	now := serviceNow(s.Clock)
	switch command.Decision {
	case ApproveReview:
		if err := review.Approve(command.ActorID, command.Summary, command.ExpectedReviewVersion, now); err != nil {
			return err
		}
		if err := draft.Approve(command.ExpectedDraftVersion, now); err != nil {
			return err
		}
	case RequestReviewChanges:
		if err := review.RequestChanges(command.ActorID, command.Summary, command.ExpectedReviewVersion, now); err != nil {
			return err
		}
		if err := draft.RequestChanges(command.ExpectedDraftVersion, now); err != nil {
			return err
		}
	case DismissReview:
		if err := review.Dismiss(command.ActorID, command.Summary, command.ExpectedReviewVersion, now); err != nil {
			return err
		}
	default:
		return domain.ErrInvalidArgument
	}
	return inTransaction(ctx, s.Tx, func(tx context.Context) error {
		if err := s.Reviews.Save(tx, review, command.ExpectedReviewVersion); err != nil {
			return err
		}
		if command.Decision == DismissReview {
			return nil
		}
		return s.Drafts.Save(tx, draft, command.ExpectedDraftVersion)
	})
}

type PublishDraftCommand struct {
	DraftID                 string
	ActorID                 string
	RevisionID              string
	ExpectedDraftVersion    uint64
	ExpectedArtifactVersion uint64
}

func (s ArtifactService) PublishApprovedDraft(ctx context.Context, command PublishDraftCommand) (domain.Revision, error) {
	if s.Artifacts == nil || s.Revisions == nil || s.Drafts == nil {
		return domain.Revision{}, domain.ErrInvalidArgument
	}
	draft, err := s.Drafts.Get(ctx, command.DraftID)
	if err != nil {
		return domain.Revision{}, err
	}
	if draft.Status != domain.DraftApproved {
		return domain.Revision{}, &domain.DomainError{Kind: domain.ErrInvalidTransition, Field: "draft", Message: "only an approved draft can become a revision"}
	}
	artifact, err := s.Artifacts.Get(ctx, draft.ArtifactID)
	if err != nil {
		return domain.Revision{}, err
	}
	if artifact.Version != command.ExpectedArtifactVersion || draft.Version != command.ExpectedDraftVersion {
		return domain.Revision{}, domain.ErrConflict
	}
	if err := ensureDraftBaseIsCurrent(ctx, s.Revisions, artifact, draft); err != nil {
		return domain.Revision{}, err
	}
	latest, err := s.Revisions.LatestNumber(ctx, artifact.ID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.Revision{}, err
	}
	revisionID := command.RevisionID
	if revisionID == "" && s.IDs != nil {
		revisionID = s.IDs.NewID("revision")
	}
	now := serviceNow(s.Clock)
	revision, err := domain.NewRevision(revisionID, artifact.ID, latest+1, draft.BaseRevision, draft.SourceManifestID, draft.Content, command.ActorID, now)
	if err != nil {
		return domain.Revision{}, err
	}
	if err := artifact.AdvanceRevision(revision.Ref(""), command.ExpectedArtifactVersion, now); err != nil {
		return domain.Revision{}, err
	}
	if err := draft.MarkApplied(command.ExpectedDraftVersion, now); err != nil {
		return domain.Revision{}, err
	}
	err = inTransaction(ctx, s.Tx, func(tx context.Context) error {
		if err := s.Revisions.Create(tx, revision); err != nil {
			return err
		}
		if err := s.Artifacts.Save(tx, artifact, command.ExpectedArtifactVersion); err != nil {
			return err
		}
		return s.Drafts.Save(tx, draft, command.ExpectedDraftVersion)
	})
	if err != nil {
		return domain.Revision{}, err
	}
	return revision, nil
}

func ensureDraftBaseIsCurrent(ctx context.Context, revisions RevisionRepository, artifact *domain.Artifact, draft *domain.Draft) error {
	if artifact.CurrentRevisionID == "" {
		if draft.BaseRevision != nil {
			return domain.ErrConflict
		}
		return nil
	}
	if draft.BaseRevision == nil || draft.BaseRevision.RevisionID != artifact.CurrentRevisionID {
		return domain.ErrConflict
	}
	current, err := revisions.Get(ctx, artifact.CurrentRevisionID)
	if err != nil {
		return err
	}
	if !draft.BaseRevision.Equal(current.Ref("")) {
		return domain.ErrConflict
	}
	return nil
}
