package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/verification"
)

const defaultVerificationMaxJSONBodyBytes int64 = 1 << 20

type VerificationAPI interface {
	ListActiveProfiles(context.Context, string, string, string) ([]verification.ProfileSummary, error)
	ListCandidateRunsForSession(context.Context, string, string, string, int) ([]verification.RunView, error)
	GetReceiptByID(context.Context, string, string) (verification.Receipt, error)
	ListChecksForRunByID(context.Context, string, string, int, int) (verification.CheckPage, error)
	CreateCandidateRun(context.Context, verification.CreateCandidateRunRequest) (verification.RunView, error)
	GetRunByID(context.Context, string, string) (verification.RunView, error)
	CancelCandidateRun(context.Context, verification.CancelCandidateRunRequest) (verification.RunView, error)
	RetryCandidateRun(context.Context, verification.RetryCandidateRunRequest) (verification.RunView, error)
}

type CanonicalVerificationAPI interface {
	ListCanonicalProfiles(context.Context, string, verification.CanonicalPlanSubject, string) ([]verification.ProfileSummary, error)
	ListCanonicalRuns(context.Context, string, verification.CanonicalPlanSubject, string, int) ([]verification.CanonicalRunView, error)
	CreateCanonicalRun(context.Context, verification.CreateCanonicalRunRequest) (verification.CanonicalRunView, error)
	GetCanonicalRun(context.Context, string, string, string) (verification.CanonicalRunView, error)
}

type VerificationSessionResolver interface {
	ResolveProject(context.Context, string, string) (string, error)
}

type VerificationDependencies struct {
	Service          VerificationAPI
	Canonical        CanonicalVerificationAPI
	Sessions         VerificationSessionResolver
	MaxJSONBodyBytes int64
}

type VerificationHandler struct {
	service   VerificationAPI
	canonical CanonicalVerificationAPI
	sessions  VerificationSessionResolver
	maxJSON   int64
}

func NewVerificationHandler(dependencies VerificationDependencies) (*VerificationHandler, error) {
	if dependencies.Service == nil || dependencies.Sessions == nil {
		return nil, errors.New("verification service and SandboxSession resolver are required")
	}
	maxJSON := dependencies.MaxJSONBodyBytes
	if maxJSON <= 0 {
		maxJSON = defaultVerificationMaxJSONBodyBytes
	}
	return &VerificationHandler{
		service: dependencies.Service, canonical: dependencies.Canonical,
		sessions: dependencies.Sessions, maxJSON: maxJSON,
	}, nil
}

func RegisterVerificationRoutes(
	routes gin.IRoutes,
	handler *VerificationHandler,
	mutationMiddleware ...gin.HandlerFunc,
) error {
	if routes == nil || handler == nil {
		return errors.New("verification routes and handler are required")
	}
	mutation := func(target gin.HandlerFunc) []gin.HandlerFunc {
		handlers := []gin.HandlerFunc{verificationNoStore}
		handlers = append(handlers, mutationMiddleware...)
		return append(handlers, target)
	}
	routes.GET("/sandbox-sessions/:sessionId/verification-runs", verificationNoStore, handler.listCandidateRuns)
	routes.POST(
		"/sandbox-sessions/:sessionId/verification-runs",
		mutation(handler.createCandidateRun)...,
	)
	routes.GET("/sandbox-sessions/:sessionId/verification-profiles", verificationNoStore, handler.listProfiles)
	routes.GET("/verification-runs/:runId/checks", verificationNoStore, handler.listChecks)
	routes.GET("/verification-runs/:runId", verificationNoStore, handler.getRun)
	routes.GET("/verification-receipts/:receiptId", verificationNoStore, handler.getReceipt)
	routes.POST("/verification-runs/:runId", mutation(handler.controlRun)...)
	if handler.canonical != nil {
		routes.GET("/projects/:projectId/canonical-verification-profiles", verificationNoStore, handler.listCanonicalProfiles)
		routes.POST("/projects/:projectId/canonical-verification-runs", mutation(handler.createCanonicalRun)...)
		routes.GET("/projects/:projectId/canonical-verification-runs", verificationNoStore, handler.listCanonicalRuns)
		routes.GET("/projects/:projectId/canonical-verification-runs/:runId", verificationNoStore, handler.getCanonicalRun)
	}
	return nil
}

func canonicalSubjectFromQuery(c *gin.Context) verification.CanonicalPlanSubject {
	return verification.CanonicalPlanSubject{
		WorkspaceArtifactID:  strings.TrimSpace(c.Query("workspaceArtifactId")),
		WorkspaceRevisionID:  strings.TrimSpace(c.Query("workspaceRevisionId")),
		WorkspaceContentHash: strings.TrimSpace(c.Query("workspaceContentHash")),
	}
}

func (handler *VerificationHandler) listCanonicalProfiles(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	profiles, err := handler.canonical.ListCanonicalProfiles(
		c.Request.Context(), c.Param("projectId"), canonicalSubjectFromQuery(c), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	if profiles == nil {
		profiles = []verification.ProfileSummary{}
	}
	c.JSON(http.StatusOK, gin.H{"profiles": profiles})
}

func (handler *VerificationHandler) listCanonicalRuns(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	limit, ok := verificationQueryInt(c, "limit", 20, 1, 100)
	if !ok {
		return
	}
	runs, err := handler.canonical.ListCanonicalRuns(
		c.Request.Context(), c.Param("projectId"), canonicalSubjectFromQuery(c), actor, limit,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	if runs == nil {
		runs = []verification.CanonicalRunView{}
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

type createCanonicalVerificationRunRequest struct {
	WorkspaceRevision struct {
		ArtifactID  string `json:"artifactId"`
		RevisionID  string `json:"revisionId"`
		ContentHash string `json:"contentHash"`
	} `json:"workspaceRevision"`
	VerificationProfile verification.ProfileReference `json:"verificationProfile"`
	Reason              string                        `json:"reason"`
}

func (handler *VerificationHandler) createCanonicalRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request createCanonicalVerificationRunRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	view, err := handler.canonical.CreateCanonicalRun(c.Request.Context(), verification.CreateCanonicalRunRequest{
		ProjectID: c.Param("projectId"),
		WorkspaceRevision: verification.CanonicalPlanSubject{
			WorkspaceArtifactID:  request.WorkspaceRevision.ArtifactID,
			WorkspaceRevisionID:  request.WorkspaceRevision.RevisionID,
			WorkspaceContentHash: request.WorkspaceRevision.ContentHash,
		},
		Profile: request.VerificationProfile, Reason: request.Reason,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf("\"canonical-verification-run:%s:%d\"", view.Run.ID, view.Run.Version))
	c.Header("Location", "/v1/projects/"+view.Run.ProjectID+"/canonical-verification-runs/"+view.Run.ID)
	status := http.StatusCreated
	if view.Run.Replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, view)
}

func (handler *VerificationHandler) getCanonicalRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	view, err := handler.canonical.GetCanonicalRun(
		c.Request.Context(), c.Param("projectId"), c.Param("runId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf("\"canonical-verification-run:%s:%d\"", view.Run.ID, view.Run.Version))
	c.JSON(http.StatusOK, view)
}

func (handler *VerificationHandler) listCandidateRuns(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	projectID, err := handler.sessions.ResolveProject(
		c.Request.Context(), c.Param("sessionId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > 100 {
			problem.Write(c, problem.New(
				http.StatusUnprocessableEntity,
				"invalid_verification_query",
				"Verification query is invalid",
				"limit must be an integer from 1 to 100.",
			))
			return
		}
		limit = value
	}
	runs, err := handler.service.ListCandidateRunsForSession(
		c.Request.Context(), projectID, c.Param("sessionId"), actor, limit,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	if runs == nil {
		runs = []verification.RunView{}
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

func (handler *VerificationHandler) listProfiles(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	projectID, err := handler.sessions.ResolveProject(
		c.Request.Context(), c.Param("sessionId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	profiles, err := handler.service.ListActiveProfiles(c.Request.Context(), projectID, c.Param("sessionId"), actor)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	if profiles == nil {
		profiles = []verification.ProfileSummary{}
	}
	c.JSON(http.StatusOK, gin.H{"profiles": profiles})
}

func (handler *VerificationHandler) listChecks(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	offset, ok := verificationQueryInt(c, "offset", 0, 0, 1_000_000)
	if !ok {
		return
	}
	limit, ok := verificationQueryInt(c, "limit", 50, 1, 100)
	if !ok {
		return
	}
	page, err := handler.service.ListChecksForRunByID(
		c.Request.Context(), c.Param("runId"), actor, offset, limit,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	if page.Checks == nil {
		page.Checks = []verification.CheckResult{}
	}
	c.JSON(http.StatusOK, page)
}

func verificationQueryInt(
	c *gin.Context,
	name string,
	fallback, minimum, maximum int,
) (int, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		problem.Write(c, problem.New(
			http.StatusUnprocessableEntity,
			"invalid_verification_query",
			"Verification query is invalid",
			fmt.Sprintf("%s must be an integer from %d to %d.", name, minimum, maximum),
		))
		return 0, false
	}
	return value, true
}

func (handler *VerificationHandler) getReceipt(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	receipt, err := handler.service.GetReceiptByID(
		c.Request.Context(), c.Param("receiptId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	c.Header("ETag", fmt.Sprintf(
		"\"verification-receipt:%s:%s\"", receipt.ID, receipt.PayloadHash,
	))
	c.JSON(http.StatusOK, receipt)
}

type createCandidateVerificationRunRequest struct {
	CandidateID              string                        `json:"candidateId"`
	CheckpointID             string                        `json:"checkpointId"`
	ExpectedSessionVersion   uint64                        `json:"expectedSessionVersion"`
	ExpectedSessionEpoch     uint64                        `json:"expectedSessionEpoch"`
	ExpectedCandidateVersion uint64                        `json:"expectedCandidateVersion"`
	ExpectedWriterLeaseEpoch uint64                        `json:"expectedWriterLeaseEpoch"`
	VerificationProfile      verification.ProfileReference `json:"verificationProfile"`
	Reason                   string                        `json:"reason"`
}

func (handler *VerificationHandler) createCandidateRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	projectID, err := handler.sessions.ResolveProject(
		c.Request.Context(), c.Param("sessionId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	var request createCandidateVerificationRunRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	view, err := handler.service.CreateCandidateRun(
		c.Request.Context(),
		verification.CreateCandidateRunRequest{
			ProjectID: projectID, SessionID: c.Param("sessionId"),
			CandidateID: request.CandidateID, CheckpointID: request.CheckpointID,
			ExpectedSessionVersion:   request.ExpectedSessionVersion,
			ExpectedSessionEpoch:     request.ExpectedSessionEpoch,
			ExpectedCandidateVersion: request.ExpectedCandidateVersion,
			ExpectedWriterLeaseEpoch: request.ExpectedWriterLeaseEpoch,
			VerificationProfile:      request.VerificationProfile,
			Reason:                   request.Reason, ActorID: actor,
			OperationID: worksmiddleware.IdempotencyKey(c),
		},
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	writeVerificationRunHeaders(c, view)
	c.Header("Location", "/v1/verification-runs/"+view.Run.ID)
	status := http.StatusCreated
	if view.Run.Replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, view)
}

func (handler *VerificationHandler) getRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	view, err := handler.service.GetRunByID(
		c.Request.Context(), c.Param("runId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	writeVerificationRunHeaders(c, view)
	c.JSON(http.StatusOK, view)
}

func (handler *VerificationHandler) controlRun(c *gin.Context) {
	raw := c.Param("runId")
	separator := strings.LastIndexByte(raw, ':')
	if separator <= 0 {
		writeVerificationActionNotFound(c)
		return
	}
	runID, action := raw[:separator], raw[separator+1:]
	if action != "cancel" && action != "retry" {
		writeVerificationActionNotFound(c)
		return
	}
	for index := range c.Params {
		if c.Params[index].Key == "runId" {
			c.Params[index].Value = runID
			break
		}
	}
	if action == "cancel" {
		handler.cancelRun(c)
		return
	}
	handler.retryRun(c)
}

func writeVerificationActionNotFound(c *gin.Context) {
	problem.Write(c, problem.New(
		http.StatusNotFound,
		"verification_run_action_not_found",
		"VerificationRun action not found",
		"The requested VerificationRun action does not exist.",
	))
}

type cancelCandidateVerificationRunRequest struct {
	ExpectedVersion    uint64 `json:"expectedVersion"`
	ExpectedFenceEpoch uint64 `json:"expectedFenceEpoch"`
	Reason             string `json:"reason"`
}

func (handler *VerificationHandler) cancelRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request cancelCandidateVerificationRunRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	current, err := handler.service.GetRunByID(
		c.Request.Context(), c.Param("runId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	view, err := handler.service.CancelCandidateRun(
		c.Request.Context(),
		verification.CancelCandidateRunRequest{
			ProjectID: current.Run.ProjectID, RunID: current.Run.ID,
			ExpectedVersion:    request.ExpectedVersion,
			ExpectedFenceEpoch: request.ExpectedFenceEpoch,
			Reason:             request.Reason, ActorID: actor,
		},
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	writeVerificationRunHeaders(c, view)
	c.JSON(http.StatusOK, view)
}

type retryCandidateVerificationRunRequest struct {
	Reason string `json:"reason"`
}

func (handler *VerificationHandler) retryRun(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	var request retryCandidateVerificationRunRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	parent, err := handler.service.GetRunByID(
		c.Request.Context(), c.Param("runId"), actor,
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	view, err := handler.service.RetryCandidateRun(
		c.Request.Context(),
		verification.RetryCandidateRunRequest{
			ProjectID: parent.Run.ProjectID, ParentRunID: parent.Run.ID,
			Reason: request.Reason, ActorID: actor,
			OperationID: worksmiddleware.IdempotencyKey(c),
		},
	)
	if err != nil {
		writeVerificationProblem(c, err)
		return
	}
	writeVerificationRunHeaders(c, view)
	c.Header("Location", "/v1/verification-runs/"+view.Run.ID)
	status := http.StatusCreated
	if view.Run.Replayed {
		status = http.StatusOK
		c.Header("X-Idempotent-Replay", "true")
	}
	c.JSON(status, view)
}

func verificationNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func writeVerificationRunHeaders(c *gin.Context, view verification.RunView) {
	c.Header("ETag", fmt.Sprintf(
		"\"candidate-verification-run:%s:%d:%d\"",
		view.Run.ID, view.Run.Version, view.Run.FenceEpoch,
	))
	c.Header("X-Verification-Fence-Epoch", fmt.Sprintf("%d", view.Run.FenceEpoch))
}

func writeVerificationProblem(c *gin.Context, err error) {
	slog.ErrorContext(c.Request.Context(), "verification request failed", "error", err)
	switch {
	case errors.Is(err, verification.ErrRunNotFound),
		errors.Is(err, verification.ErrPlanNotFound),
		errors.Is(err, verification.ErrReceiptNotFound),
		errors.Is(err, verification.ErrCanonicalRunNotFound),
		errors.Is(err, verification.ErrCanonicalPlanNotFound),
		errors.Is(err, verification.ErrCanonicalReceiptNotFound):
		problem.Write(c, problem.New(
			http.StatusNotFound,
			"verification_not_found",
			"Verification resource not found",
			"The requested verification resource was not found.",
		))
	case errors.Is(err, verification.ErrInvalidRun),
		errors.Is(err, verification.ErrInvalidPlan),
		errors.Is(err, verification.ErrInvalidReceipt):
		problem.Write(c, problem.New(
			http.StatusUnprocessableEntity,
			"invalid_verification_input",
			"Verification input is invalid",
			"One or more verification values are invalid.",
		))
	case errors.Is(err, verification.ErrRunVersionConflict):
		problem.Write(c, problem.New(
			http.StatusPreconditionFailed,
			"verification_precondition_failed",
			"Verification precondition failed",
			"The VerificationRun version or worker fence changed.",
		))
	case errors.Is(err, verification.ErrRunIdempotencyConflict),
		errors.Is(err, verification.ErrRunTransition),
		errors.Is(err, verification.ErrPlanConflict),
		errors.Is(err, verification.ErrCanonicalPlanConflict):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"verification_conflict",
			"Verification request conflicts",
			"The verification request conflicts with committed state.",
		))
	case errors.Is(err, verification.ErrCandidatePlanningBlocked):
		detail := "The exact Candidate checkpoint or its required canonical inputs are not ready."
		if strings.Contains(err.Error(), "compile exact VerificationPlan:") {
			detail = "The exact inputs are ready, but the VerificationPlan cannot be compiled from the frozen contract, template releases, and profile."
		}
		problem.Write(c, problem.New(
			http.StatusConflict,
			"verification_planning_blocked",
			"Verification planning is blocked",
			detail,
		))
	case errors.Is(err, verification.ErrCandidatePlanningDrift):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"verification_source_drift",
			"Verification source changed",
			"A canonical verification source differs from its exact committed projection.",
		))
	case errors.Is(err, verification.ErrCanonicalPlanningBlocked):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"canonical_verification_planning_blocked",
			"Canonical verification planning is blocked",
			"The exact approved WorkspaceRevision or one of its release inputs is not ready.",
		))
	case errors.Is(err, verification.ErrCanonicalPlanningDrift):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"canonical_verification_source_drift",
			"Canonical verification source changed",
			"A release input differs from its exact committed projection.",
		))
	case errors.Is(err, verification.ErrRunStoreDown):
		problem.Write(c, problem.New(
			http.StatusServiceUnavailable,
			"verification_unavailable",
			"Verification is unavailable",
			"The verification control plane is temporarily unavailable.",
		))
	default:
		writeBusinessProblem(c, err)
	}
}
