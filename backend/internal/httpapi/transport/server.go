package transport

import (
	"context"
	"log/slog"

	"github.com/worksflow/builder/backend/internal/auth"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/generation"
)

type AuthService interface {
	SignUp(context.Context, string, string, string, string, string) (auth.IssuedSession, error)
	SignIn(context.Context, string, string, string, string) (auth.IssuedSession, error)
	Authenticate(context.Context, string) (auth.Session, error)
	SignOut(context.Context, string) error
}

type RotatingAuthService interface {
	Rotate(context.Context, string, string, string) (auth.IssuedSession, error)
}

type ProjectService interface {
	Create(context.Context, string, core.CreateProjectInput) (core.CreatedProject, error)
	List(context.Context, string) ([]core.Project, error)
	Get(context.Context, string, string) (core.Project, error)
	Update(context.Context, string, string, uint64, core.UpdateProjectInput) (core.Project, error)
}

type MemberService interface {
	List(context.Context, string, string) ([]core.ProjectMember, error)
	AddExisting(context.Context, string, string, string, core.Role) (core.ProjectMember, error)
	Invite(context.Context, string, string, string, core.Role) (core.ProjectInvitation, error)
	AcceptInvitation(context.Context, string, string) (core.ProjectMember, error)
	UpdateRole(context.Context, string, string, string, core.Role, string) (core.ProjectMember, error)
	Remove(context.Context, string, string, string, string) error
}

type AccessControl interface {
	Authorize(context.Context, string, string, core.Action) (core.Role, error)
}

type ArtifactService interface {
	Create(context.Context, string, string, core.CreateArtifactInput) (core.VersionedArtifact, error)
	List(context.Context, string, string, string, string) ([]core.Artifact, error)
	Get(context.Context, string, string, bool) (core.VersionedArtifact, error)
	UpdateDraft(context.Context, string, string, string, core.UpdateDraftInput) (core.ArtifactDraft, error)
	CreateRevision(context.Context, string, string, string, core.CreateRevisionInput) (core.ArtifactRevision, error)
	ListRevisions(context.Context, string, string) ([]core.ArtifactRevision, error)
	GetRevision(context.Context, string, string) (core.ArtifactRevision, error)
}

type TraceService interface {
	CreateLink(context.Context, string, string, core.CreateTraceLinkInput) (core.TraceLink, error)
	CreateDependency(context.Context, string, string, core.CreateDependencyInput) (core.ArtifactDependency, error)
	ListLinks(context.Context, string, string, string) ([]core.TraceLink, error)
	ListDependencies(context.Context, string, string) ([]core.ArtifactDependency, error)
}

type ReviewService interface {
	Submit(context.Context, string, string, string, core.SubmitReviewInput) (core.ReviewRequest, error)
	List(context.Context, string, string) ([]core.ReviewRequest, error)
	Decide(context.Context, string, string, core.DecideReviewInput) (core.ReviewRequest, error)
}

type ConditionalReviewService interface {
	DecideIfMatch(context.Context, string, string, string, core.DecideReviewInput) (core.ReviewRequest, error)
}

type CommentService interface {
	Create(context.Context, string, string, core.CreateCommentInput) (core.CommentThread, error)
	Get(context.Context, string, string) (core.CommentThread, error)
	ListArtifact(context.Context, string, string) ([]core.CommentThread, error)
	ListProject(context.Context, string, string) ([]core.CommentThread, error)
	SetResolved(context.Context, string, string, bool) (core.CommentThread, error)
}

type ConditionalCommentService interface {
	SetResolvedIfMatch(context.Context, string, string, string, bool) (core.CommentThread, error)
}

type BaselineService interface {
	Compile(context.Context, string, string, []core.VersionRef) (core.ArtifactRevision, error)
}

type ImpactService interface {
	Analyze(context.Context, string, string, core.AnalyzeImpactInput) (core.ImpactReport, error)
	Get(context.Context, string, string) (core.ImpactReport, error)
}

type ProposalService interface {
	CreateManifest(context.Context, string, string, core.CreateManifestInput) (domain.InputManifest, error)
	CreateProposal(context.Context, string, string, core.CreateProposalInput) (domain.OutputProposal, error)
	GetManifest(context.Context, string, string) (domain.InputManifest, error)
	GetProposal(context.Context, string, string) (domain.OutputProposal, error)
	ListProposals(context.Context, string, string, string) ([]domain.OutputProposal, error)
	Decide(context.Context, string, string, core.DecideProposalInput) (domain.OutputProposal, error)
	Apply(context.Context, string, string, core.ApplyProposalInput) (core.ArtifactDraft, error)
}

type WorkbenchService interface {
	CreateBundle(context.Context, string, string, core.CreateWorkbenchBundleInput) (core.WorkbenchBundle, error)
	GetBundle(context.Context, string, string) (core.WorkbenchBundle, error)
}

type ImplementationService interface {
	Create(context.Context, string, string, core.CreateImplementationProposalInput) (core.ImplementationProposal, error)
	Get(context.Context, string, string) (core.ImplementationProposal, error)
	Decide(context.Context, string, string, core.DecideImplementationInput) (core.ImplementationProposal, error)
	Apply(context.Context, string, string, core.ApplyImplementationInput) (core.ArtifactRevision, error)
}

type ActivityService interface {
	ListNotifications(context.Context, string, string, bool) ([]core.Notification, error)
	MarkNotification(context.Context, string, string, string, bool) (core.Notification, error)
	ListAudit(context.Context, string, string) ([]core.AuditEvent, error)
	HeartbeatPresence(context.Context, string, string, *string) (core.Presence, error)
	ListPresence(context.Context, string, string) ([]core.Presence, error)
}

type GenerationService interface {
	GenerateArtifactProposal(context.Context, string, string, string) (generation.ArtifactGenerationResult, error)
	GenerateImplementation(context.Context, string, string, string, string) (generation.ImplementationGenerationResult, error)
}

type Services struct {
	Auth           AuthService
	Projects       ProjectService
	Members        MemberService
	Access         AccessControl
	Artifacts      ArtifactService
	Traces         TraceService
	Reviews        ReviewService
	Comments       CommentService
	Baselines      BaselineService
	Impacts        ImpactService
	Proposals      ProposalService
	Workbench      WorkbenchService
	Implementation ImplementationService
	Activity       ActivityService
	Generation     GenerationService
}

type Server struct {
	services Services
	config   config.Config
	logger   *slog.Logger
}

func NewServer(services Services, cfg config.Config, logger *slog.Logger) *Server {
	return &Server{services: services, config: cfg, logger: logger}
}

func (s *Server) writeServiceError(requestID, path string, err error) {
	s.logger.Warn("API operation failed", "request_id", requestID, "path", path, "error", err)
}
