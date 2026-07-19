package transport

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/repository"
)

type RepositoryAPI interface {
	Bootstrap(context.Context, repository.BootstrapCandidateInput) (repository.BootstrapCandidateResult, error)
	Get(context.Context, string, string, string) (repository.CandidateWorkspace, error)
	GetSnapshot(context.Context, string, string, string, string) (repository.RepositorySnapshotReceipt, error)
	ListHeads(context.Context, string, string) (repository.CandidateHeadList, error)
	SearchCandidate(context.Context, repository.CandidateSearchInput) (repository.CandidateSearchResult, error)
}

type CandidateRebaseAPI interface {
	Start(context.Context, repository.StartCandidateRebaseInput) (repository.CandidateRebaseResult, error)
	Get(context.Context, string, string, string) (repository.CandidateRebaseResult, error)
	ReadConflictContent(context.Context, string, string, string, string) (repository.CandidateRebaseConflictContent, error)
	ResolveConflict(context.Context, repository.ResolveCandidateRebaseConflictInput) (repository.CandidateRebaseResult, error)
}

type RepositoryDependencies struct {
	Service          RepositoryAPI
	Rebases          CandidateRebaseAPI
	MaxJSONBodyBytes int64
}

type RepositoryHandler struct {
	service RepositoryAPI
	rebases CandidateRebaseAPI
	maxJSON int64
}

func NewRepositoryHandler(dependencies RepositoryDependencies) (*RepositoryHandler, error) {
	if dependencies.Service == nil || dependencies.Rebases == nil {
		return nil, errors.New("repository Candidate and rebase services are required")
	}
	maxJSON := dependencies.MaxJSONBodyBytes
	if maxJSON <= 0 {
		maxJSON = defaultSandboxMaxJSONBodyBytes
	}
	return &RepositoryHandler{service: dependencies.Service, rebases: dependencies.Rebases, maxJSON: maxJSON}, nil
}

func RegisterRepositoryRoutes(
	routes gin.IRoutes,
	handler *RepositoryHandler,
	mutationMiddleware ...gin.HandlerFunc,
) error {
	if routes == nil || handler == nil {
		return errors.New("repository routes and handler are required")
	}
	routes.GET(
		"/projects/:projectId/repository-candidates",
		repositoryNoStore,
		handler.listHeads,
	)
	routes.GET(
		"/projects/:projectId/repository-candidates/:candidateId",
		repositoryNoStore,
		handler.get,
	)
	routes.GET(
		"/projects/:projectId/repository-snapshots/:snapshotId",
		repositoryNoStore,
		handler.getSnapshot,
	)
	routes.POST(
		"/projects/:projectId/repository-candidates/:candidateId/search",
		repositoryNoStore,
		handler.searchCandidate,
	)
	mutation := []gin.HandlerFunc{repositoryNoStore}
	mutation = append(mutation, mutationMiddleware...)
	bootstrapMutation := append(append([]gin.HandlerFunc(nil), mutation...), handler.bootstrap)
	routes.POST("/projects/:projectId/repository-candidates", bootstrapMutation...)
	startRebaseMutation := append(append([]gin.HandlerFunc(nil), mutation...), handler.startRebase)
	routes.POST("/projects/:projectId/repository-candidates/:candidateId/rebases", startRebaseMutation...)
	routes.GET(
		"/projects/:projectId/candidate-rebases/:rebaseId",
		repositoryNoStore,
		handler.getRebase,
	)
	routes.GET(
		"/projects/:projectId/candidate-rebases/:rebaseId/conflicts/:conflictId/content",
		repositoryNoStore,
		handler.getRebaseConflictContent,
	)
	resolveMutation := append(append([]gin.HandlerFunc(nil), mutation...), handler.resolveRebaseConflict)
	routes.POST(
		"/projects/:projectId/candidate-rebases/:rebaseId/conflicts/:conflictId/resolve",
		resolveMutation...,
	)
	return nil
}

type bootstrapRepositoryCandidateRequest struct {
	BuildManifestID string `json:"buildManifestId"`
}

func (handler *RepositoryHandler) bootstrap(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request bootstrapRepositoryCandidateRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.Bootstrap(c.Request.Context(), repository.BootstrapCandidateInput{
		ProjectID: c.Param("projectId"), BuildManifestID: request.BuildManifestID,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		if result.Candidate.ID != "" {
			c.Header("Location", repositoryCandidateLocation(result.Candidate.ProjectID, result.Candidate.ID))
			c.Header("Retry-After", "1")
		}
		writeRepositoryProblem(c, err)
		return
	}
	c.Header("Location", repositoryCandidateLocation(result.Candidate.ProjectID, result.Candidate.ID))
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}

func (handler *RepositoryHandler) get(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	candidate, err := handler.service.Get(
		c.Request.Context(), c.Param("projectId"), c.Param("candidateId"), actor,
	)
	if err != nil {
		writeRepositoryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, candidate)
}

func (handler *RepositoryHandler) getSnapshot(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	receipt, err := handler.service.GetSnapshot(
		c.Request.Context(), c.Param("projectId"), c.Param("snapshotId"),
		c.Query("contentHash"), actor,
	)
	if err != nil {
		writeRepositoryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, receipt)
}

func (handler *RepositoryHandler) listHeads(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	heads, err := handler.service.ListHeads(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeRepositoryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, heads)
}

type searchRepositoryCandidateRequest struct {
	ExpectedHeadGeneration uint64   `json:"expectedHeadGeneration"`
	ExpectedRootHash       string   `json:"expectedRootHash"`
	Query                  string   `json:"query"`
	CaseSensitive          *bool    `json:"caseSensitive,omitempty"`
	IncludeGlobs           []string `json:"includeGlobs,omitempty"`
	MaxMatches             int      `json:"maxMatches,omitempty"`
}

func (handler *RepositoryHandler) searchCandidate(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request searchRepositoryCandidateRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	caseSensitive := true
	if request.CaseSensitive != nil {
		caseSensitive = *request.CaseSensitive
	}
	result, err := handler.service.SearchCandidate(c.Request.Context(), repository.CandidateSearchInput{
		ProjectID: c.Param("projectId"), CandidateID: c.Param("candidateId"),
		ExpectedHeadGeneration: request.ExpectedHeadGeneration, ExpectedRootHash: request.ExpectedRootHash,
		Query: request.Query, CaseSensitive: caseSensitive,
		IncludeGlobs: request.IncludeGlobs, MaxMatches: request.MaxMatches, ActorID: actor,
	})
	if err != nil {
		writeRepositoryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type startCandidateRebaseRequest struct {
	TargetBuildManifestID    string `json:"targetBuildManifestId"`
	ExpectedCandidateVersion uint64 `json:"expectedCandidateVersion"`
	ExpectedSessionEpoch     uint64 `json:"expectedSessionEpoch"`
	ExpectedWriterLeaseEpoch uint64 `json:"expectedWriterLeaseEpoch"`
}

func (handler *RepositoryHandler) startRebase(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request startCandidateRebaseRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.rebases.Start(c.Request.Context(), repository.StartCandidateRebaseInput{
		ProjectID: c.Param("projectId"), PredecessorCandidateID: c.Param("candidateId"),
		TargetBuildManifestID:    request.TargetBuildManifestID,
		ExpectedCandidateVersion: request.ExpectedCandidateVersion,
		ExpectedSessionEpoch:     request.ExpectedSessionEpoch,
		ExpectedWriterLeaseEpoch: request.ExpectedWriterLeaseEpoch,
		ActorID:                  actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		if result.Rebase.ID != "" {
			c.Header("Location", candidateRebaseLocation(result.Rebase.ProjectID, result.Rebase.ID))
			c.Header("Retry-After", "1")
		}
		writeRepositoryProblem(c, err)
		return
	}
	c.Header("Location", candidateRebaseLocation(result.Rebase.ProjectID, result.Rebase.ID))
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}

func (handler *RepositoryHandler) getRebase(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := handler.rebases.Get(
		c.Request.Context(), c.Param("projectId"), c.Param("rebaseId"), actor,
	)
	if err != nil {
		writeRepositoryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (handler *RepositoryHandler) getRebaseConflictContent(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := handler.rebases.ReadConflictContent(
		c.Request.Context(), c.Param("projectId"), c.Param("rebaseId"), c.Param("conflictId"), actor,
	)
	if err != nil {
		writeRepositoryProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type resolveCandidateRebaseConflictRequest struct {
	ExpectedConflictVersion uint64                                       `json:"expectedConflictVersion"`
	Strategy                repository.CandidateRebaseResolutionStrategy `json:"strategy"`
	Content                 *string                                      `json:"content,omitempty"`
	Mode                    string                                       `json:"mode,omitempty"`
}

func (handler *RepositoryHandler) resolveRebaseConflict(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request resolveCandidateRebaseConflictRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.rebases.ResolveConflict(c.Request.Context(), repository.ResolveCandidateRebaseConflictInput{
		ProjectID: c.Param("projectId"), RebaseID: c.Param("rebaseId"), ConflictID: c.Param("conflictId"),
		ExpectedConflictVersion: request.ExpectedConflictVersion, ActorID: actor,
		Strategy: request.Strategy, Content: request.Content, Mode: request.Mode,
	})
	if err != nil {
		if result.Rebase.ID != "" {
			c.Header("Location", candidateRebaseLocation(result.Rebase.ProjectID, result.Rebase.ID))
			c.Header("Retry-After", "1")
		}
		writeRepositoryProblem(c, err)
		return
	}
	c.Header("Location", candidateRebaseLocation(result.Rebase.ProjectID, result.Rebase.ID))
	c.JSON(http.StatusOK, result)
}

func repositoryNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func repositoryCandidateLocation(projectID, candidateID string) string {
	return "/v1/projects/" + projectID + "/repository-candidates/" + candidateID
}

func candidateRebaseLocation(projectID, rebaseID string) string {
	return "/v1/projects/" + projectID + "/candidate-rebases/" + rebaseID
}

func writeRepositoryProblem(c *gin.Context, err error) {
	searchDenial, validSearchDenial := repositorySearchDenial(err)
	switch {
	case validSearchDenial:
		retrySeconds := int64((searchDenial.RetryAfter + time.Second - 1) / time.Second)
		c.Header("Retry-After", strconv.FormatInt(retrySeconds, 10))
		problem.Write(c, problem.New(http.StatusTooManyRequests, "repository_search_rate_limited", "Repository search is rate limited", "Retry after the advertised delay."))
	case errors.Is(err, repository.ErrCandidateSearchIndexUnavailable),
		errors.Is(err, repository.ErrInvalidExactTreeLiteralIndex),
		errors.Is(err, repository.ErrExactTreeLiteralIndexConflict),
		errors.Is(err, repository.ErrExactTreeLiteralIndexContract),
		errors.Is(err, repository.ErrExactTreeLiteralBuildClaimLost),
		errors.Is(err, repository.ErrExactTreeLiteralClaimRelease):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "repository_search_index_unavailable", "Repository search index is unavailable", "The exact-tree index could not be trusted or coordinated safely. Retry after the service is reconciled."))
	case errors.Is(err, repository.ErrExactTreeSearchAdmissionDenied),
		errors.Is(err, repository.ErrExactTreeSearchAdmissionInvalid),
		errors.Is(err, repository.ErrExactTreeSearchAdmissionUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "repository_search_admission_unavailable", "Repository search admission is unavailable", "The repository search could not be admitted safely. Retry later."))
	case errors.Is(err, repository.ErrCandidateNotFound):
		problem.Write(c, problem.New(http.StatusNotFound, "repository_candidate_not_found", "Repository Candidate not found", "The requested same-project Candidate does not exist."))
	case errors.Is(err, repository.ErrRepositorySnapshotNotFound):
		problem.Write(c, problem.New(http.StatusNotFound, "repository_snapshot_not_found", "Repository Snapshot not found", "The requested same-project RepositorySnapshot does not exist."))
	case errors.Is(err, repository.ErrRepositorySnapshotPending):
		c.Header("Retry-After", "1")
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "repository_snapshot_receipt_pending", "Repository Snapshot receipt is pending", "Retry the original Candidate bootstrap with the same Idempotency-Key so content settlement can complete."))
	case errors.Is(err, repository.ErrRepositorySnapshotDrift):
		problem.Write(c, problem.New(http.StatusConflict, "repository_snapshot_content_changed", "Repository Snapshot content differs", "Refresh the exact RepositorySnapshot receipt; its ID and content hash must both match."))
	case errors.Is(err, repository.ErrRepositorySnapshotIntegrity):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "repository_snapshot_integrity_failed", "Repository Snapshot integrity check failed", "The immutable RepositorySnapshot or its tree object could not be verified. Reconcile storage before continuing."))
	case errors.Is(err, repository.ErrInvalidRepositorySnapshotSelection):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_repository_snapshot_selection", "Repository Snapshot selection is invalid", "Use a canonical project ID, RepositorySnapshot ID, and exact content hash from Candidate bootstrap."))
	case errors.Is(err, repository.ErrBootstrapNotReady):
		problem.Write(c, problem.New(http.StatusConflict, "repository_bootstrap_blocked", "Repository Candidate bootstrap is blocked", "Apply an implementation Proposal to produce a canonical WorkspaceRevision and ensure its exact BuildContract and FullStackTemplate remain approved."))
	case errors.Is(err, repository.ErrBootstrapSourceDrift):
		problem.Write(c, problem.New(http.StatusConflict, "repository_bootstrap_source_changed", "Repository bootstrap source changed", "The exact BuildManifest, BuildContract, FullStackTemplate, or WorkspaceRevision changed while the Candidate was being materialized."))
	case errors.Is(err, repository.ErrBootstrapFinalizationPending), errors.Is(err, repository.ErrBootstrapReconciliation):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "repository_bootstrap_reconciliation_pending", "Repository Candidate reconciliation is pending", "The Candidate may have committed. Retry with the same Idempotency-Key; do not create a replacement operation."))
	case errors.Is(err, repository.ErrBootstrapInvalid), errors.Is(err, repository.ErrInvalidCandidate),
		errors.Is(err, repository.ErrInvalidTree), errors.Is(err, repository.ErrTreeLimit):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_repository_bootstrap", "Repository Candidate bootstrap is invalid", "The project, BuildManifest, WorkspaceRevision, or repository file tree is invalid."))
	case errors.Is(err, repository.ErrCandidateHeadLimit):
		problem.Write(c, problem.New(http.StatusConflict, "repository_candidate_heads_ambiguous", "Too many Repository Candidate heads", "The project has more active Candidate heads than can be selected safely. Archive or reconcile old Candidates before continuing."))
	case errors.Is(err, repository.ErrCandidateSearchDrift):
		problem.Write(c, problem.New(http.StatusConflict, "repository_search_head_changed", "Repository Candidate head changed", "Refresh the exact Candidate generation and root hash before searching again."))
	case errors.Is(err, repository.ErrInvalidCandidateSearch):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_repository_search", "Repository Candidate search is invalid", "Use a bounded literal query, valid include globs, and the exact Candidate generation and root hash."))
	case errors.Is(err, repository.ErrExactTreeLiteralProjectActiveBuildQuota):
		c.Header("Retry-After", "1")
		problem.Write(c, problem.New(http.StatusTooManyRequests, "repository_search_index_busy", "Repository search indexing is busy", "The project already has the maximum number of active exact-tree index builds. Retry later."))
	case errors.Is(err, repository.ErrExactTreeLiteralProjectTreeQuota),
		errors.Is(err, repository.ErrExactTreeLiteralProjectSourceBytesQuota):
		problem.Write(c, problem.New(http.StatusConflict, "repository_search_index_quota_exceeded", "Repository search index quota is exceeded", "The project has reached its retained exact-tree or logical source-byte index quota."))
	case errors.Is(err, repository.ErrCandidateRebaseNotFound), errors.Is(err, repository.ErrCandidateRebaseConflictNotFound):
		problem.Write(c, problem.New(http.StatusNotFound, "candidate_rebase_not_found", "Candidate rebase not found", "The requested same-project rebase or conflict does not exist."))
	case errors.Is(err, repository.ErrCandidateRebaseReplay):
		problem.Write(c, problem.New(http.StatusConflict, "candidate_rebase_replay", "Candidate rebase input changed", "Retry the immutable rebase or conflict operation with exactly the same input."))
	case errors.Is(err, repository.ErrCandidateRebaseReconciliation):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "candidate_rebase_reconciliation_pending", "Candidate rebase reconciliation is pending", "The operation may have committed. Retry with the same Idempotency-Key and exact conflict input."))
	case errors.Is(err, repository.ErrCandidateRebaseState), errors.Is(err, repository.ErrCandidateState),
		errors.Is(err, repository.ErrLeaseFenced), errors.Is(err, repository.ErrLeaseRequired):
		problem.Write(c, problem.New(http.StatusConflict, "candidate_rebase_fenced", "Candidate rebase is fenced", "Refresh the exact Candidate and rebase versions; the predecessor, successor, or writer lease changed."))
	case errors.Is(err, repository.ErrInvalidRebase):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_candidate_rebase", "Candidate rebase is invalid", "The target manifest, version fences, strategy, or custom resolution is invalid."))
	default:
		problem.WriteError(c, err)
	}
}

func repositorySearchDenial(
	err error,
) (*repository.ExactTreeSearchAdmissionDeniedError, bool) {
	if err == nil || !errors.Is(err, repository.ErrExactTreeSearchAdmissionDenied) ||
		errors.Is(err, repository.ErrExactTreeSearchAdmissionInvalid) ||
		errors.Is(err, repository.ErrExactTreeSearchAdmissionUnavailable) ||
		errors.Is(err, repository.ErrCandidateSearchIndexUnavailable) ||
		errors.Is(err, repository.ErrInvalidExactTreeLiteralIndex) ||
		errors.Is(err, repository.ErrExactTreeLiteralIndexConflict) ||
		errors.Is(err, repository.ErrExactTreeLiteralIndexContract) ||
		errors.Is(err, repository.ErrExactTreeLiteralBuildClaimLost) ||
		errors.Is(err, repository.ErrExactTreeLiteralClaimRelease) {
		return nil, false
	}
	denial, valid := repositorySearchDenialInSingleErrorChain(err)
	if !valid ||
		(denial.Operation != repository.ExactTreeSearchAdmissionQuery &&
			denial.Operation != repository.ExactTreeSearchAdmissionFirstBuilder) ||
		denial.RetryAfter <= 0 || denial.RetryAfter > time.Hour {
		return nil, false
	}
	return denial, true
}

func repositorySearchDenialInSingleErrorChain(
	err error,
) (*repository.ExactTreeSearchAdmissionDeniedError, bool) {
	for current := err; current != nil; {
		if _, joined := current.(interface{ Unwrap() []error }); joined {
			return nil, false
		}
		if denial, ok := current.(*repository.ExactTreeSearchAdmissionDeniedError); ok {
			return denial, denial != nil
		}
		wrapped, ok := current.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		current = wrapped.Unwrap()
	}
	return nil, false
}

var _ RepositoryAPI = (*repository.CandidateBootstrapService)(nil)
var _ CandidateRebaseAPI = (*repository.CandidateRebaseService)(nil)
