package transport

import (
	"net/http"

	"github.com/gin-gonic/gin"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
)

// RegisterBusinessRoutes adds the authenticated business API to a /v1 route
// group. The caller is responsible for attaching authentication to group. An
// optional persistence middleware may be supplied; when present it runs after
// the route has captured its idempotency key and before the business handler.
func RegisterBusinessRoutes(group *gin.RouterGroup, server *Server, persistence ...gin.HandlerFunc) {
	if group == nil || server == nil {
		return
	}
	server.RegisterBusinessRoutes(group, persistence...)
}

func (s *Server) RegisterBusinessRoutes(group *gin.RouterGroup, persistence ...gin.HandlerFunc) {
	csrf := worksmiddleware.RequireCSRF(s.config.Security)
	ifMatch := worksmiddleware.RequireIfMatch()
	requireIdempotency := worksmiddleware.CaptureIdempotencyKey(true)
	optionalIdempotency := worksmiddleware.CaptureIdempotencyKey(false)
	persistIdempotency := func(context *gin.Context) { context.Next() }
	if len(persistence) > 0 && persistence[0] != nil {
		persistIdempotency = persistence[0]
	}
	command := func(method, path string, handler gin.HandlerFunc, prefix ...gin.HandlerFunc) {
		handlers := append([]gin.HandlerFunc{}, prefix...)
		handlers = append(handlers, csrf, requireIdempotency, persistIdempotency, handler)
		group.Handle(method, path, handlers...)
	}
	conditionalCommand := func(method, path string, handler gin.HandlerFunc, prefix ...gin.HandlerFunc) {
		handlers := append([]gin.HandlerFunc{}, prefix...)
		handlers = append(handlers, csrf, ifMatch, requireIdempotency, persistIdempotency, handler)
		group.Handle(method, path, handlers...)
	}
	conditionalMutation := func(method, path string, handler gin.HandlerFunc, prefix ...gin.HandlerFunc) {
		handlers := append([]gin.HandlerFunc{}, prefix...)
		handlers = append(handlers, csrf, ifMatch, optionalIdempotency, persistIdempotency, handler)
		group.Handle(method, path, handlers...)
	}

	group.GET("/projects/:projectId/artifacts", s.ListArtifacts)
	command(http.MethodPost, "/projects/:projectId/artifacts", s.CreateArtifact)
	group.GET("/artifacts/:artifactId", s.GetArtifact)
	group.GET("/artifacts/:artifactId/draft", s.GetArtifactDraft)
	conditionalMutation(http.MethodPatch, "/drafts/:draftId", s.UpdateArtifactDraft)
	conditionalMutation(http.MethodPatch, "/artifacts/:artifactId/drafts/:draftId", s.UpdateArtifactDraft)
	group.GET("/artifacts/:artifactId/revisions", s.ListArtifactRevisions)
	conditionalCommand(http.MethodPost, "/artifacts/:artifactId/revisions", s.CreateArtifactRevision)
	group.GET("/revisions/:revisionId", s.GetArtifactRevision)

	for _, collection := range []string{"documents", "blueprints", "page-specs", "prototypes"} {
		collectionMiddleware := withArtifactCollection(collection)
		group.GET("/projects/:projectId/"+collection, collectionMiddleware, s.ListCollectionArtifacts)
		command(http.MethodPost, "/projects/:projectId/"+collection, s.CreateCollectionArtifact, collectionMiddleware)
		group.GET("/"+collection+"/:artifactId", collectionMiddleware, s.GetCollectionArtifact)
		conditionalMutation(http.MethodPatch, "/"+collection+"/:artifactId/draft", s.UpdateCollectionDraft, collectionMiddleware)
		group.GET("/"+collection+"/:artifactId/revisions", collectionMiddleware, s.ListCollectionArtifactRevisions)
		conditionalCommand(http.MethodPost, "/"+collection+"/:artifactId/revisions", s.CreateCollectionArtifactRevision, collectionMiddleware)
	}

	group.GET("/projects/:projectId/traces", s.ListTraceLinks)
	command(http.MethodPost, "/projects/:projectId/traces", s.CreateTraceLink)
	group.GET("/projects/:projectId/trace", s.ListTraceLinks)
	command(http.MethodPost, "/projects/:projectId/trace", s.CreateTraceLink)
	group.GET("/projects/:projectId/trace-matrix", s.GetTraceMatrix)
	group.GET("/artifacts/:artifactId/dependencies", s.ListArtifactDependencies)
	command(http.MethodPost, "/artifacts/:artifactId/dependencies", s.CreateArtifactDependency)
	command(http.MethodPost, "/projects/:projectId/dependencies", s.CreateProjectDependency)

	group.GET("/projects/:projectId/reviews", s.ListProjectReviews)
	group.GET("/projects/:projectId/reviews/:reviewId", s.GetReview)
	command(http.MethodPost, "/projects/:projectId/reviews", s.SubmitProjectReview)
	group.GET("/revisions/:revisionId/reviews", s.ListRevisionReviews)
	command(http.MethodPost, "/revisions/:revisionId/reviews", s.SubmitRevisionReview)
	group.GET("/reviews/:reviewId", s.GetReview)
	conditionalCommand(http.MethodPost, "/reviews/:reviewId/decision", s.DecideReview)
	conditionalCommand(http.MethodPost, "/reviews/:reviewId/decisions", s.DecideReview)
	conditionalCommand(http.MethodPost, "/projects/:projectId/reviews/:reviewId/decisions", s.DecideReview)

	group.GET("/projects/:projectId/comments", s.ListProjectComments)
	command(http.MethodPost, "/projects/:projectId/comments", s.CreateProjectComment)
	group.GET("/artifacts/:artifactId/comments", s.ListArtifactComments)
	command(http.MethodPost, "/artifacts/:artifactId/comments", s.CreateArtifactComment)
	group.GET("/revisions/:revisionId/comments", s.ListRevisionComments)
	command(http.MethodPost, "/revisions/:revisionId/comments", s.CreateRevisionComment)
	group.GET("/comments/:commentId", s.GetComment)
	conditionalMutation(http.MethodPatch, "/comments/:commentId", s.ResolveComment)

	command(http.MethodPost, "/projects/:projectId/requirement-baselines", s.CompileRequirementBaseline)
	command(http.MethodPost, "/projects/:projectId/baseline/compile", s.CompileRequirementBaseline)
	command(http.MethodPost, "/projects/:projectId/impact-reports", s.AnalyzeImpact)
	group.GET("/impact-reports/:reportId", s.GetImpactReport)

	command(http.MethodPost, "/projects/:projectId/input-manifests", s.CreateInputManifest)
	command(http.MethodPost, "/projects/:projectId/manifests", s.CreateInputManifest)
	group.GET("/input-manifests/:manifestId", s.GetInputManifest)
	group.GET("/manifests/:manifestId", s.GetInputManifest)

	group.GET("/projects/:projectId/output-proposals", s.ListOutputProposals)
	command(http.MethodPost, "/projects/:projectId/output-proposals", s.CreateOutputProposal)
	group.GET("/projects/:projectId/proposals", s.ListOutputProposals)
	command(http.MethodPost, "/projects/:projectId/proposals", s.CreateOutputProposal)
	group.GET("/output-proposals/:proposalId", s.GetOutputProposal)
	group.GET("/proposals/:proposalId", s.GetOutputProposal)
	conditionalCommand(http.MethodPost, "/output-proposals/:proposalId/decisions", s.DecideOutputProposal)
	conditionalCommand(http.MethodPost, "/proposals/:proposalId/decisions", s.DecideOutputProposal)
	conditionalCommand(http.MethodPost, "/output-proposals/:proposalId/apply", s.ApplyOutputProposal)
	conditionalCommand(http.MethodPost, "/proposals/:proposalId/apply", s.ApplyOutputProposal)

	command(http.MethodPost, "/projects/:projectId/workbench-bundles", s.CreateWorkbenchBundle)
	command(http.MethodPost, "/projects/:projectId/build-manifests", s.CreateWorkbenchBundle)
	group.GET("/workbench-bundles/:bundleId", s.GetWorkbenchBundle)
	group.GET("/build-manifests/:bundleId", s.GetWorkbenchBundle)

	command(http.MethodPost, "/projects/:projectId/implementation-proposals", s.CreateImplementationProposal)
	group.GET("/implementation-proposals/:implementationProposalId", s.GetImplementationProposal)
	conditionalCommand(http.MethodPost, "/implementation-proposals/:implementationProposalId/decisions", s.DecideImplementationProposal)
	conditionalCommand(http.MethodPost, "/implementation-proposals/:implementationProposalId/apply", s.ApplyImplementationProposal)

	command(http.MethodPost, "/input-manifests/:manifestId/generate", s.GenerateArtifactProposal)
	command(http.MethodPost, "/manifests/:manifestId/generate", s.GenerateArtifactProposal)
	command(http.MethodPost, "/generation/artifact-proposals", s.GenerateArtifactProposal)
	command(http.MethodPost, "/workbench-bundles/:bundleId/generate", s.GenerateImplementation)
	command(http.MethodPost, "/build-manifests/:bundleId/generate", s.GenerateImplementation)
	command(http.MethodPost, "/generation/implementation-proposals", s.GenerateImplementation)

	group.GET("/notifications", s.ListNotifications)
	conditionalMutation(http.MethodPatch, "/notifications/:notificationId", s.MarkNotification)
	group.GET("/projects/:projectId/audit", s.ListAuditEvents)
	group.GET("/projects/:projectId/presence", s.ListPresence)
	command(http.MethodPost, "/projects/:projectId/presence/heartbeat", s.HeartbeatPresence)
}
