package application

import (
	"context"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

type ArtifactRepository interface {
	Create(context.Context, *domain.Artifact) error
	Get(context.Context, string) (*domain.Artifact, error)
	Save(context.Context, *domain.Artifact, uint64) error
}

type RevisionRepository interface {
	Create(context.Context, domain.Revision) error
	Get(context.Context, string) (domain.Revision, error)
	LatestNumber(context.Context, string) (int, error)
}

type DraftRepository interface {
	Create(context.Context, *domain.Draft) error
	Get(context.Context, string) (*domain.Draft, error)
	Save(context.Context, *domain.Draft, uint64) error
}

type DependencyRepository interface {
	Create(context.Context, domain.Dependency) error
	ListByArtifact(context.Context, string) ([]domain.Dependency, error)
}

type TraceLinkRepository interface {
	Create(context.Context, domain.TraceLink) error
	ListFrom(context.Context, domain.ArtifactRef) ([]domain.TraceLink, error)
	ListTo(context.Context, domain.ArtifactRef) ([]domain.TraceLink, error)
}

type ReviewRepository interface {
	Create(context.Context, *domain.Review) error
	Get(context.Context, string) (*domain.Review, error)
	Save(context.Context, *domain.Review, uint64) error
}

type CommentRepository interface {
	Create(context.Context, *domain.Comment) error
	Get(context.Context, string) (*domain.Comment, error)
	Save(context.Context, *domain.Comment, uint64) error
	ListByAnchor(context.Context, domain.CommentAnchor) ([]domain.Comment, error)
}

type ManifestRepository interface {
	Create(context.Context, domain.InputManifest) error
	Get(context.Context, string) (domain.InputManifest, error)
}

type ProposalRepository interface {
	Create(context.Context, *domain.OutputProposal) error
	Get(context.Context, string) (*domain.OutputProposal, error)
	Save(context.Context, *domain.OutputProposal, uint64) error
}

type WorkflowDefinitionRepository interface {
	Create(context.Context, domain.WorkflowDefinition) error
	Get(context.Context, string, int) (domain.WorkflowDefinition, error)
	LatestVersion(context.Context, string) (int, error)
}

type WorkflowRunRepository interface {
	Create(context.Context, *domain.WorkflowRun) error
	Get(context.Context, string) (*domain.WorkflowRun, error)
	Save(context.Context, *domain.WorkflowRun, uint64) error
}

type DeliverySliceRepository interface {
	Create(context.Context, *domain.DeliverySlice) error
	Get(context.Context, string) (*domain.DeliverySlice, error)
	Save(context.Context, *domain.DeliverySlice, uint64) error
}

// TransactionManager allows services to commit multi-aggregate transitions atomically.
type TransactionManager interface {
	WithinTransaction(context.Context, func(context.Context) error) error
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID(prefix string) string
}
