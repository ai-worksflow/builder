package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/agent"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/sandbox"
)

const defaultAgentMaxJSONBodyBytes int64 = 1 << 20

// AgentControlAPI is the authenticated, project-scoped browser control plane.
// Worker lease and lifecycle methods intentionally are not exposed here.
type AgentControlAPI interface {
	CreateTaskAttempt(context.Context, agent.CreateTaskAttemptInput) (agent.TaskAttemptResult, error)
	GetTaskGraph(context.Context, string, string, string) (agent.TaskGraph, error)
	AdvanceTaskGraph(context.Context, agent.AdvanceTaskGraphInput) (agent.TaskGraphAdvanceResult, error)
	RetryAttempt(context.Context, agent.RetryAttemptInput) (agent.TaskAttemptResult, error)
	GetAttempt(context.Context, string, string) (agent.TaskAttemptResult, error)
	ListAttempts(context.Context, string, string, string, int) ([]agent.AgentAttempt, error)
	ListEvents(context.Context, string, string, uint64, int) ([]agent.AttemptEvent, error)
	CancelAttempt(context.Context, string, string, uint64, string) (agent.AgentAttempt, error)
}

type AgentReviewAPI interface {
	ReadEvidence(context.Context, string, string, agent.EvidenceKind) (agent.EvidenceReadResult, error)
}

type AgentPatchFileReviewAPI interface {
	ReadPatchFile(context.Context, string, string, string, agent.PatchFileSide) (agent.PatchFileReviewResult, error)
}

type AgentMergeAPI interface {
	MergePatch(context.Context, agent.MergePatchInput) (agent.MergePatchResult, error)
}

type AgentUndoAPI interface {
	UndoPatch(context.Context, agent.UndoPatchInput) (agent.UndoPatchResult, error)
}

type AgentHistoryAPI interface {
	ListAttemptMerges(context.Context, string, string, int) ([]agent.PatchMergeHistoryItem, error)
}

// AgentSessionProjectResolver derives project scope from the authoritative
// SandboxSession. The browser never supplies a project ID for Session routes.
type AgentSessionProjectResolver interface {
	ResolveProject(context.Context, string, string) (string, error)
}

type AgentDependencies struct {
	Service          AgentControlAPI
	Review           AgentReviewAPI
	PatchFiles       AgentPatchFileReviewAPI
	Merge            AgentMergeAPI
	Undo             AgentUndoAPI
	History          AgentHistoryAPI
	Sessions         AgentSessionProjectResolver
	MaxJSONBodyBytes int64
}

type AgentHandler struct {
	service    AgentControlAPI
	review     AgentReviewAPI
	patchFiles AgentPatchFileReviewAPI
	merge      AgentMergeAPI
	undo       AgentUndoAPI
	history    AgentHistoryAPI
	sessions   AgentSessionProjectResolver
	maxJSON    int64
}

func NewAgentHandler(dependencies AgentDependencies) (*AgentHandler, error) {
	if dependencies.Service == nil || dependencies.Review == nil || dependencies.PatchFiles == nil || dependencies.Merge == nil ||
		dependencies.Undo == nil || dependencies.History == nil || dependencies.Sessions == nil {
		return nil, errors.New("Agent control, review, patch file review, merge, undo, history, and SandboxSession services are required")
	}
	maxJSON := dependencies.MaxJSONBodyBytes
	if maxJSON <= 0 {
		maxJSON = defaultAgentMaxJSONBodyBytes
	}
	return &AgentHandler{
		service: dependencies.Service, review: dependencies.Review, patchFiles: dependencies.PatchFiles,
		merge: dependencies.Merge,
		undo:  dependencies.Undo, history: dependencies.History,
		sessions: dependencies.Sessions, maxJSON: maxJSON,
	}, nil
}

func RegisterAgentRoutes(
	routes gin.IRoutes,
	handler *AgentHandler,
	mutationMiddleware ...gin.HandlerFunc,
) error {
	if routes == nil || handler == nil {
		return errors.New("Agent routes and handler are required")
	}
	routes.GET(
		"/sandbox-sessions/:sessionId/agent-attempts",
		agentNoStore,
		handler.listAttempts,
	)
	routes.GET(
		"/sandbox-sessions/:sessionId/agent-task-graph",
		agentNoStore,
		handler.getTaskGraph,
	)
	routes.GET("/agent-attempts/:attemptId", agentNoStore, handler.getAttempt)
	routes.GET("/agent-attempts/:attemptId/events", agentNoStore, handler.listEvents)
	routes.GET("/agent-attempts/:attemptId/merges", agentNoStore, handler.listMerges)
	routes.GET("/agent-attempts/:attemptId/evidence/:kind", agentNoStore, handler.readEvidence)
	routes.GET("/agent-attempts/:attemptId/patch-file", agentNoStore, handler.readPatchFile)

	mutation := []gin.HandlerFunc{agentNoStore}
	mutation = append(mutation, mutationMiddleware...)
	create := append(append([]gin.HandlerFunc(nil), mutation...), handler.createAttempt)
	routes.POST("/sandbox-sessions/:sessionId/agent-attempts", create...)
	advanceGraph := append(append([]gin.HandlerFunc(nil), mutation...), handler.advanceTaskGraph)
	routes.POST("/sandbox-sessions/:sessionId/agent-task-graph/advance", advanceGraph...)
	merge := []gin.HandlerFunc{agentNoStore, worksmiddleware.RequireIfMatch()}
	merge = append(merge, mutationMiddleware...)
	merge = append(merge, handler.mergePatch)
	routes.POST("/agent-attempts/:attemptId/merge", merge...)
	undo := []gin.HandlerFunc{agentNoStore, worksmiddleware.RequireIfMatch()}
	undo = append(undo, mutationMiddleware...)
	undo = append(undo, handler.undoPatch)
	routes.POST("/agent-merges/:mergeId/undo", undo...)

	// Gin parameters consume a complete path segment. The terminal route keeps
	// the API's immutable-resource actions (`{attemptId}:cancel|retry`) explicit.
	control := []gin.HandlerFunc{agentNoStore, worksmiddleware.RequireIfMatch()}
	control = append(control, mutationMiddleware...)
	control = append(control, handler.controlAttempt)
	routes.POST("/agent-attempts/:attemptId", control...)
	return nil
}

type createAgentAttemptRequest struct {
	TaskKey         string `json:"taskKey"`
	Instruction     string `json:"instruction"`
	ExecutorProfile string `json:"executorProfile"`
}

type advanceAgentTaskGraphRequest struct {
	Instruction     string `json:"instruction"`
	ExecutorProfile string `json:"executorProfile"`
}

func (handler *AgentHandler) getTaskGraph(c *gin.Context) {
	actor, projectID, ok := handler.sessionIdentity(c)
	if !ok {
		return
	}
	result, err := handler.service.GetTaskGraph(
		c.Request.Context(), projectID, c.Param("sessionId"), actor,
	)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (handler *AgentHandler) advanceTaskGraph(c *gin.Context) {
	actor, projectID, ok := handler.sessionIdentity(c)
	if !ok {
		return
	}
	var request advanceAgentTaskGraphRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.AdvanceTaskGraph(c.Request.Context(), agent.AdvanceTaskGraphInput{
		ProjectID: projectID, SandboxSessionID: c.Param("sessionId"),
		Instruction: request.Instruction, ExecutorProfile: request.ExecutorProfile,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	status := http.StatusOK
	if result.Attempt != nil {
		writeAgentAttemptHeaders(c, result.Attempt.Attempt)
		c.Header("Location", agentAttemptLocation(result.Attempt.Attempt.ID))
		if !result.Replayed {
			status = http.StatusCreated
		}
	}
	c.JSON(status, result)
}

func (handler *AgentHandler) createAttempt(c *gin.Context) {
	actor, projectID, ok := handler.sessionIdentity(c)
	if !ok {
		return
	}
	var request createAgentAttemptRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.service.CreateTaskAttempt(c.Request.Context(), agent.CreateTaskAttemptInput{
		ProjectID: projectID, SandboxSessionID: c.Param("sessionId"),
		TaskKey: request.TaskKey, Instruction: request.Instruction,
		ExecutorProfile: request.ExecutorProfile, ActorID: actor,
		OperationID: worksmiddleware.IdempotencyKey(c),
	})
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	writeAgentAttemptHeaders(c, result.Attempt)
	c.Header("Location", agentAttemptLocation(result.Attempt.ID))
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, result)
}

func (handler *AgentHandler) getAttempt(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := handler.service.GetAttempt(c.Request.Context(), c.Param("attemptId"), actor)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	writeAgentAttemptHeaders(c, result.Attempt)
	c.JSON(http.StatusOK, result)
}

type agentAttemptListResponse struct {
	Attempts []agent.AgentAttempt `json:"attempts"`
}

func (handler *AgentHandler) listAttempts(c *gin.Context) {
	actor, projectID, ok := handler.sessionIdentity(c)
	if !ok {
		return
	}
	limit, ok := parseAgentQueryUint(c, "limit", 50, 1, 100)
	if !ok {
		return
	}
	attempts, err := handler.service.ListAttempts(
		c.Request.Context(), projectID, c.Param("sessionId"), actor, int(limit),
	)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	if attempts == nil {
		attempts = []agent.AgentAttempt{}
	}
	c.JSON(http.StatusOK, agentAttemptListResponse{Attempts: attempts})
}

type agentEventListResponse struct {
	Events        []agent.AttemptEvent `json:"events"`
	AfterSequence uint64               `json:"afterSequence"`
	LastSequence  uint64               `json:"lastSequence"`
}

func (handler *AgentHandler) listEvents(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	after, ok := parseAgentQueryUint(c, "afterSequence", 0, 0, ^uint64(0))
	if !ok {
		return
	}
	limit, ok := parseAgentQueryUint(c, "limit", 200, 1, 1000)
	if !ok {
		return
	}
	events, err := handler.service.ListEvents(
		c.Request.Context(), c.Param("attemptId"), actor, after, int(limit),
	)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	if events == nil {
		events = []agent.AttemptEvent{}
	}
	last := after
	if len(events) > 0 {
		last = events[len(events)-1].Sequence
	}
	c.JSON(http.StatusOK, agentEventListResponse{
		Events: events, AfterSequence: after, LastSequence: last,
	})
}

type agentMergeHistoryListResponse struct {
	Merges []agent.PatchMergeHistoryItem `json:"merges"`
}

func (handler *AgentHandler) listMerges(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	limit, ok := parseAgentQueryUint(c, "limit", 50, 1, 100)
	if !ok {
		return
	}
	merges, err := handler.history.ListAttemptMerges(
		c.Request.Context(), c.Param("attemptId"), actor, int(limit),
	)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	if merges == nil {
		merges = []agent.PatchMergeHistoryItem{}
	}
	c.JSON(http.StatusOK, agentMergeHistoryListResponse{Merges: merges})
}

func (handler *AgentHandler) readEvidence(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := handler.review.ReadEvidence(
		c.Request.Context(), c.Param("attemptId"), actor, agent.EvidenceKind(c.Param("kind")),
	)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	writeAgentAttemptHeaders(c, result.Attempt)
	c.Header("ETag", `"agent-evidence:`+result.Attempt.ID+`:`+string(result.Kind)+`:`+result.Reference.ContentHash+`"`)
	c.Header("X-Content-Hash", result.RawHash)
	c.Header("X-Content-Object-Hash", result.Reference.ContentHash)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, result.MediaType, result.Value)
}

func (handler *AgentHandler) readPatchFile(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := handler.patchFiles.ReadPatchFile(
		c.Request.Context(), c.Param("attemptId"), actor,
		c.Query("path"), agent.PatchFileSide(c.Query("side")),
	)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	writeAgentAttemptHeaders(c, result.Attempt)
	c.Header("ETag", `"agent-patch-file:`+result.Attempt.ID+`:`+string(result.Side)+`:`+result.RepresentationHash+`"`)
	c.Header("X-File-Exists", strconv.FormatBool(result.Exists))
	c.Header("X-Content-Hash", result.ContentHash)
	c.Header("X-File-Mode", result.Mode)
	c.Header("X-Byte-Size", strconv.FormatInt(result.ByteSize, 10))
	c.Header("X-Patch-Content-Hash", result.PatchContentHash)
	c.Header("X-Content-Type-Options", "nosniff")
	if !result.Exists {
		c.Status(http.StatusNoContent)
		return
	}
	c.Data(http.StatusOK, "application/octet-stream", result.Value)
}

type agentAttemptReasonRequest struct {
	Reason string `json:"reason"`
}

type mergeAgentPatchRequest struct {
	ExpectedSessionVersion   uint64 `json:"expectedSessionVersion"`
	ExpectedSessionEpoch     uint64 `json:"expectedSessionEpoch"`
	ExpectedCandidateVersion uint64 `json:"expectedCandidateVersion"`
	ExpectedWriterLeaseEpoch uint64 `json:"expectedWriterLeaseEpoch"`
}

func (handler *AgentHandler) mergePatch(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	attemptID := c.Param("attemptId")
	expectedAttemptVersion, ok := parseAgentAttemptETag(
		c, worksmiddleware.IfMatch(c), attemptID,
	)
	if !ok {
		return
	}
	var request mergeAgentPatchRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.merge.MergePatch(c.Request.Context(), agent.MergePatchInput{
		AttemptID: attemptID, ActorID: actor,
		OperationID:              worksmiddleware.IdempotencyKey(c),
		ExpectedAttemptVersion:   expectedAttemptVersion,
		ExpectedSessionVersion:   request.ExpectedSessionVersion,
		ExpectedSessionEpoch:     request.ExpectedSessionEpoch,
		ExpectedCandidateVersion: request.ExpectedCandidateVersion,
		ExpectedWriterLeaseEpoch: request.ExpectedWriterLeaseEpoch,
	})
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	c.Header("Location", "/v1/agent-merges/"+result.Plan.ID)
	c.Header("ETag", `"agent-merge:`+result.Plan.ID+`:`+result.Plan.ContentHash+`"`)
	c.Header("X-Agent-Merge-Disposition", string(result.Plan.Disposition))
	status := http.StatusCreated
	if result.Replayed || result.Plan.Disposition == agent.PatchMergeNoop {
		status = http.StatusOK
	}
	if result.Plan.Disposition == agent.PatchMergeConflicted {
		status = http.StatusConflict
	}
	c.JSON(status, result)
}

func (handler *AgentHandler) undoPatch(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	mergeID := c.Param("mergeId")
	expectedMergeContentHash, ok := parseAgentMergeETag(
		c, worksmiddleware.IfMatch(c), mergeID,
	)
	if !ok {
		return
	}
	var request mergeAgentPatchRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}
	result, err := handler.undo.UndoPatch(c.Request.Context(), agent.UndoPatchInput{
		MergeID: mergeID, ExpectedMergeContentHash: expectedMergeContentHash,
		ActorID: actor, OperationID: worksmiddleware.IdempotencyKey(c),
		ExpectedSessionVersion:   request.ExpectedSessionVersion,
		ExpectedSessionEpoch:     request.ExpectedSessionEpoch,
		ExpectedCandidateVersion: request.ExpectedCandidateVersion,
		ExpectedWriterLeaseEpoch: request.ExpectedWriterLeaseEpoch,
	})
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	c.Header("Location", "/v1/agent-undos/"+result.Plan.ID)
	c.Header("ETag", `"agent-undo:`+result.Plan.ID+`:`+result.Plan.ContentHash+`"`)
	c.Header("X-Agent-Undo-Disposition", string(result.Plan.Disposition))
	status := http.StatusCreated
	if result.Replayed || result.Plan.Disposition == agent.PatchMergeNoop {
		status = http.StatusOK
	}
	if result.Plan.Disposition == agent.PatchMergeConflicted {
		status = http.StatusConflict
	}
	c.JSON(status, result)
}

func (handler *AgentHandler) controlAttempt(c *gin.Context) {
	attemptID, action, ok := parseAgentAttemptAction(c)
	if !ok {
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	current, err := handler.service.GetAttempt(c.Request.Context(), attemptID, actor)
	if err != nil {
		writeAgentProblem(c, err)
		return
	}
	expectedVersion, ok := parseAgentAttemptETag(c, worksmiddleware.IfMatch(c), attemptID)
	if !ok {
		return
	}
	if expectedVersion != current.Attempt.Version {
		writeAgentAttemptHeaders(c, current.Attempt)
		preconditionFailed(c, "AgentAttempt")
		return
	}
	var request agentAttemptReasonRequest
	if err := DecodeJSON(c, &request, handler.maxJSON); err != nil {
		WriteJSONError(c, err)
		return
	}

	switch action {
	case "cancel":
		attempt, cancelErr := handler.service.CancelAttempt(
			c.Request.Context(), attemptID, actor, expectedVersion, request.Reason,
		)
		if cancelErr != nil {
			writeAgentProblem(c, cancelErr)
			return
		}
		writeAgentAttemptHeaders(c, attempt)
		c.Header("Location", agentAttemptLocation(attempt.ID))
		c.JSON(http.StatusOK, attempt)
	case "retry":
		result, retryErr := handler.service.RetryAttempt(c.Request.Context(), agent.RetryAttemptInput{
			ProjectID: current.Attempt.ProjectID, ParentAttemptID: attemptID,
			Reason: request.Reason, ActorID: actor,
			OperationID: worksmiddleware.IdempotencyKey(c),
		})
		if retryErr != nil {
			writeAgentProblem(c, retryErr)
			return
		}
		writeAgentAttemptHeaders(c, result.Attempt)
		c.Header("Location", agentAttemptLocation(result.Attempt.ID))
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		c.JSON(status, result)
	}
}

func (handler *AgentHandler) sessionIdentity(c *gin.Context) (string, string, bool) {
	actor, ok := actorID(c)
	if !ok {
		return "", "", false
	}
	projectID, err := handler.sessions.ResolveProject(c.Request.Context(), c.Param("sessionId"), actor)
	if err != nil {
		writeAgentProblem(c, err)
		return "", "", false
	}
	return actor, projectID, true
}

func parseAgentAttemptAction(c *gin.Context) (string, string, bool) {
	raw := c.Param("attemptId")
	separator := strings.LastIndexByte(raw, ':')
	if separator <= 0 {
		writeAgentActionNotFound(c)
		return "", "", false
	}
	action := raw[separator+1:]
	if action != "cancel" && action != "retry" {
		writeAgentActionNotFound(c)
		return "", "", false
	}
	return raw[:separator], action, true
}

func writeAgentActionNotFound(c *gin.Context) {
	problem.Write(c, problem.New(
		http.StatusNotFound,
		"agent_attempt_action_not_found",
		"AgentAttempt action not found",
		"The requested AgentAttempt action does not exist.",
	))
}

func parseAgentAttemptETag(c *gin.Context, value, attemptID string) (uint64, bool) {
	prefix := `"agent-attempt:` + attemptID + `:`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		problem.Write(c, problem.New(
			http.StatusBadRequest,
			"invalid_agent_attempt_etag",
			"Invalid AgentAttempt ETag",
			"If-Match does not identify this AgentAttempt.",
		))
		return 0, false
	}
	version, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`), 10, 64)
	if err != nil || version == 0 {
		problem.Write(c, problem.New(
			http.StatusBadRequest,
			"invalid_agent_attempt_etag",
			"Invalid AgentAttempt ETag",
			"If-Match contains an invalid AgentAttempt version.",
		))
		return 0, false
	}
	return version, true
}

func parseAgentMergeETag(c *gin.Context, value, mergeID string) (string, bool) {
	prefix := `"agent-merge:` + mergeID + `:sha256:`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		problem.Write(c, problem.New(
			http.StatusBadRequest,
			"invalid_agent_merge_etag",
			"Invalid Agent patch merge ETag",
			"If-Match does not identify this immutable Agent patch merge.",
		))
		return "", false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
		problem.Write(c, problem.New(
			http.StatusBadRequest,
			"invalid_agent_merge_etag",
			"Invalid Agent patch merge ETag",
			"If-Match contains an invalid immutable merge content hash.",
		))
		return "", false
	}
	return "sha256:" + digest, true
}

func parseAgentQueryUint(
	c *gin.Context,
	name string,
	defaultValue, minimum, maximum uint64,
) (uint64, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return defaultValue, true
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		problem.Write(c, problem.New(
			http.StatusBadRequest,
			"invalid_agent_query",
			"Invalid Agent query",
			name+" is outside its supported unsigned integer range.",
		))
		return 0, false
	}
	return parsed, true
}

func writeAgentAttemptHeaders(c *gin.Context, attempt agent.AgentAttempt) {
	c.Header("ETag", entityVersionETag("agent-attempt", attempt.ID, attempt.Version))
	c.Header("X-Agent-Attempt-State", string(attempt.State))
	c.Header("X-Agent-Fence-Epoch", strconv.FormatUint(attempt.FenceEpoch, 10))
}

func agentAttemptLocation(attemptID string) string {
	return "/v1/agent-attempts/" + attemptID
}

func agentNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func writeAgentProblem(c *gin.Context, err error) {
	switch {
	case errors.Is(err, agent.ErrAttemptNotFound), errors.Is(err, agent.ErrPlanNotFound),
		errors.Is(err, agent.ErrPatchMergeNotFound), errors.Is(err, agent.ErrPatchUndoNotFound),
		errors.Is(err, agent.ErrEvidenceUnavailable), errors.Is(err, agent.ErrPatchFileUnavailable),
		errors.Is(err, sandbox.ErrSessionNotFound):
		problem.Write(c, problem.New(
			http.StatusNotFound,
			"agent_resource_not_found",
			"Agent resource not found",
			"The requested same-project AgentAttempt, TaskCapsule, ContextPack, or SandboxSession was not found.",
		))
	case errors.Is(err, agent.ErrAgentStoreUnavailable):
		problem.Write(c, problem.New(
			http.StatusServiceUnavailable,
			"agent_control_unavailable",
			"Agent control plane unavailable",
			"Durable AgentAttempt state could not be read or advanced. Retry the same operation with the same Idempotency-Key.",
		))
	case errors.Is(err, agent.ErrAttemptVersionConflict):
		preconditionFailed(c, "AgentAttempt")
	case errors.Is(err, agent.ErrPatchMergeFenced), errors.Is(err, agent.ErrPatchUndoFenced),
		errors.Is(err, sandbox.ErrVersionConflict),
		errors.Is(err, sandbox.ErrCandidateVersionConflict), errors.Is(err, sandbox.ErrEpochFenced),
		errors.Is(err, sandbox.ErrSessionProjectionStale):
		preconditionFailed(c, "Agent patch merge")
	case errors.Is(err, agent.ErrPatchMergeReplay):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"agent_patch_merge_replay",
			"Agent patch merge input changed",
			"Reuse an Idempotency-Key only with the exact Attempt and SandboxSession/Candidate fences.",
		))
	case errors.Is(err, agent.ErrPatchUndoReplay):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"agent_patch_undo_replay",
			"Agent patch undo input changed",
			"Reuse an Idempotency-Key only with the exact merge and SandboxSession/Candidate fences.",
		))
	case errors.Is(err, agent.ErrPatchMergeReconciliation),
		errors.Is(err, agent.ErrPatchUndoReconciliation),
		errors.Is(err, sandbox.ErrSessionReconciliation),
		errors.Is(err, repository.ErrMutationReconciliation),
		errors.Is(err, repository.ErrTreeFinalizationPending):
		problem.Write(c, problem.New(
			http.StatusServiceUnavailable,
			"agent_patch_merge_reconciliation",
			"Agent patch merge reconciliation is pending",
			"Retry the same request with the same Idempotency-Key so the committed journal, workspace, Session, and immutable application receipt can converge.",
		))
	case errors.Is(err, agent.ErrPlanningBlocked), errors.Is(err, agent.ErrTaskGraphBlocked):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"agent_planning_blocked",
			"Agent task planning is blocked",
			"The exact ready SandboxSession, Candidate, BuildContract, TemplateRelease, or executable obligation graph is unavailable.",
		))
	case errors.Is(err, agent.ErrPlanningDrift), errors.Is(err, agent.ErrAgentStoreIntegrity):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"agent_source_changed",
			"Agent source facts changed",
			"The exact immutable planning or AgentAttempt projection no longer matches its authoritative source.",
		))
	case errors.Is(err, agent.ErrAgentOperationReplay):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"agent_operation_replay",
			"Agent operation input changed",
			"Retry with the same Idempotency-Key only when the Session, task, instruction, executor profile, parent, and reason are exact.",
		))
	case errors.Is(err, agent.ErrAttemptState), errors.Is(err, agent.ErrAttemptFenced),
		errors.Is(err, agent.ErrAttemptLease):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"agent_attempt_fenced",
			"AgentAttempt transition is fenced",
			"Refresh the exact AgentAttempt version and state before retrying this action.",
		))
	case errors.Is(err, agent.ErrEvidencePending):
		problem.Write(c, problem.New(
			http.StatusConflict,
			"agent_evidence_pending",
			"Agent evidence is still finalizing",
			"Retry after the immutable evidence object has reached finalized state.",
		))
	case errors.Is(err, agent.ErrInvalidContextPack), errors.Is(err, agent.ErrInvalidTaskCapsule),
		errors.Is(err, agent.ErrInvalidAttempt), errors.Is(err, agent.ErrEvidenceInvalid),
		errors.Is(err, agent.ErrPatchMergeInvalid), errors.Is(err, agent.ErrPatchUndoInvalid):
		problem.Write(c, problem.New(
			http.StatusUnprocessableEntity,
			"invalid_agent_input",
			"Agent input is invalid",
			"One or more Agent task, instruction, retry, or identity fields are invalid.",
		))
	case errors.Is(err, core.ErrForbidden), errors.Is(err, core.ErrNotFound):
		problem.WriteError(c, err)
	default:
		problem.WriteError(c, err)
	}
}

var _ AgentControlAPI = (*agent.ControlService)(nil)
var _ AgentReviewAPI = (*agent.ReviewService)(nil)
var _ AgentPatchFileReviewAPI = (*agent.PatchFileReviewService)(nil)
var _ AgentMergeAPI = (*agent.PatchMergeService)(nil)
var _ AgentUndoAPI = (*agent.PatchUndoService)(nil)
var _ AgentHistoryAPI = (*agent.PatchHistoryService)(nil)
