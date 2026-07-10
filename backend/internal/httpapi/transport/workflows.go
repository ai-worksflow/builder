package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	runtime "github.com/worksflow/builder/backend/internal/workflow"
)

type WorkflowAPI interface {
	ListDefinitions(context.Context, string, string) ([]runtime.DefinitionRecord, error)
	ListDefinitionVersions(context.Context, string, string, string) ([]runtime.DefinitionRecord, error)
	CreateDefinition(context.Context, string, string, runtime.CreateDefinitionInput) (runtime.DefinitionRecord, error)
	CreateDefinitionVersion(context.Context, string, string, string, runtime.CreateDefinitionVersionInput) (runtime.DefinitionRecord, error)
	PublishDefinitionVersion(context.Context, string, string, string, string) (runtime.DefinitionRecord, error)
	Start(context.Context, string, string, runtime.StartRequest) (*runtime.RunRecord, error)
	GetRun(context.Context, string, string, string) (*runtime.RunRecord, error)
	ListRuns(context.Context, string, string, runtime.RunListOptions) (runtime.RunPage, error)
	Events(context.Context, string, string, string, uint64, int) ([]runtime.Event, error)
	Resume(context.Context, string, string, string, string, json.RawMessage) error
	AuthorizeExecution(context.Context, string, string, string, string) error
	RecordProposal(context.Context, string, string, string, string, domain.ProposalRef) error
	ResolveReview(context.Context, string, string, string, string, runtime.ReviewResolution, string) error
	Cancel(context.Context, string, string, string, string) error
	Retry(context.Context, string, string, string, string, string) error
	Waive(context.Context, string, string, string, string, string) error
}

type WorkflowDependencies struct {
	Facade           WorkflowAPI
	MaxJSONBodyBytes int64
}
type WorkflowHandler struct {
	facade  WorkflowAPI
	maxBody int64
}

func NewWorkflowHandler(dependencies WorkflowDependencies) (*WorkflowHandler, error) {
	if dependencies.Facade == nil {
		return nil, errors.New("workflow facade is required")
	}
	if dependencies.MaxJSONBodyBytes <= 0 {
		dependencies.MaxJSONBodyBytes = 1 << 20
	}
	return &WorkflowHandler{facade: dependencies.Facade, maxBody: dependencies.MaxJSONBodyBytes}, nil
}

// RegisterWorkflowRoutes is intentionally independent from NewRouter. Pass a
// project-protected group carrying authentication/CSRF/idempotency middleware.
func RegisterWorkflowRoutes(routes gin.IRoutes, handler *WorkflowHandler, mutationMiddleware ...gin.HandlerFunc) error {
	if routes == nil || handler == nil {
		return errors.New("workflow routes and handler are required")
	}
	routes.GET("/projects/:projectId/workflow-definitions", handler.listDefinitions)
	routes.POST("/projects/:projectId/workflow-definitions", workflowHandlers(mutationMiddleware, handler.createDefinition)...)
	routes.GET("/projects/:projectId/workflow-definitions/:definitionId/versions", handler.listVersions)
	routes.POST("/projects/:projectId/workflow-definitions/:definitionId/versions", workflowHandlers(mutationMiddleware, handler.createVersion)...)
	routes.POST("/projects/:projectId/workflow-definitions/:definitionId/versions/:versionId/publish", workflowHandlers(mutationMiddleware, handler.publishVersion)...)
	routes.POST("/projects/:projectId/workflow-runs", workflowHandlers(mutationMiddleware, handler.start)...)
	routes.GET("/projects/:projectId/workflow-runs", handler.listRuns)
	routes.GET("/projects/:projectId/workflow-runs/:runId", handler.getRun)
	routes.GET("/projects/:projectId/workflow-runs/:runId/events", handler.events)
	routes.POST("/projects/:projectId/workflow-runs/:runId/resume", workflowHandlers(mutationMiddleware, handler.resume)...)
	routes.POST("/projects/:projectId/workflow-runs/:runId/execute", workflowHandlers(mutationMiddleware, handler.authorizeExecution)...)
	routes.POST("/projects/:projectId/workflow-runs/:runId/proposals", workflowHandlers(mutationMiddleware, handler.recordProposal)...)
	routes.POST("/projects/:projectId/workflow-runs/:runId/approve", workflowHandlers(mutationMiddleware, handler.approve)...)
	routes.POST("/projects/:projectId/workflow-runs/:runId/cancel", workflowHandlers(mutationMiddleware, handler.cancel)...)
	routes.POST("/projects/:projectId/workflow-runs/:runId/retry", workflowHandlers(mutationMiddleware, handler.retry)...)
	routes.POST("/projects/:projectId/workflow-runs/:runId/waive", workflowHandlers(mutationMiddleware, handler.waive)...)
	return nil
}

func workflowHandlers(middleware []gin.HandlerFunc, handler gin.HandlerFunc) []gin.HandlerFunc {
	handlers := make([]gin.HandlerFunc, 0, len(middleware)+1)
	handlers = append(handlers, middleware...)
	handlers = append(handlers, handler)
	return handlers
}

type startWorkflowInput struct {
	RunID               string             `json:"runId,omitempty"`
	DefinitionVersionID string             `json:"definitionVersionId"`
	InputManifest       domain.ManifestRef `json:"inputManifest"`
	Scope               json.RawMessage    `json:"scope,omitempty"`
}
type resumeWorkflowInput struct {
	NodeKey string          `json:"nodeKey"`
	Output  json.RawMessage `json:"output"`
}
type executeWorkflowInput struct {
	NodeKey string `json:"nodeKey"`
}
type proposalWorkflowInput struct {
	NodeKey  string             `json:"nodeKey"`
	Proposal domain.ProposalRef `json:"proposal"`
}
type approveWorkflowInput struct {
	NodeKey    string                   `json:"nodeKey"`
	Resolution runtime.ReviewResolution `json:"resolution"`
	Reason     string                   `json:"reason,omitempty"`
}
type reasonWorkflowInput struct {
	NodeKey string `json:"nodeKey,omitempty"`
	Reason  string `json:"reason"`
}

func (h *WorkflowHandler) listDefinitions(c *gin.Context) {
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	items, err := h.facade.ListDefinitions(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	response := make([]gin.H, 0, len(items))
	for _, item := range items {
		response = append(response, definitionResponse(item))
	}
	c.JSON(http.StatusOK, gin.H{"items": response, "total": len(response)})
}
func (h *WorkflowHandler) listVersions(c *gin.Context) {
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	items, err := h.facade.ListDefinitionVersions(c.Request.Context(), c.Param("projectId"), c.Param("definitionId"), actor)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	response := make([]gin.H, 0, len(items))
	for _, item := range items {
		response = append(response, definitionResponse(item))
	}
	c.JSON(http.StatusOK, gin.H{"items": response, "total": len(response)})
}
func (h *WorkflowHandler) createDefinition(c *gin.Context) {
	var input runtime.CreateDefinitionInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	record, err := h.facade.CreateDefinition(c.Request.Context(), c.Param("projectId"), actor, input)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Header("Location", "/v1/projects/"+c.Param("projectId")+"/workflow-definitions/"+record.Definition.ID+"/versions")
	c.JSON(http.StatusCreated, definitionResponse(record))
}
func (h *WorkflowHandler) createVersion(c *gin.Context) {
	var input runtime.CreateDefinitionVersionInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	record, err := h.facade.CreateDefinitionVersion(c.Request.Context(), c.Param("projectId"), c.Param("definitionId"), actor, input)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Header("Location", "/v1/projects/"+c.Param("projectId")+"/workflow-definitions/"+record.Definition.ID+"/versions")
	c.JSON(http.StatusCreated, definitionResponse(record))
}
func (h *WorkflowHandler) publishVersion(c *gin.Context) {
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	record, err := h.facade.PublishDefinitionVersion(c.Request.Context(), c.Param("projectId"), c.Param("definitionId"), c.Param("versionId"), actor)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.JSON(http.StatusOK, definitionResponse(record))
}
func (h *WorkflowHandler) start(c *gin.Context) {
	var input startWorkflowInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	run, err := h.facade.Start(c.Request.Context(), c.Param("projectId"), actor, runtime.StartRequest{RunID: input.RunID, DefinitionVersionID: input.DefinitionVersionID, InputManifest: input.InputManifest, Scope: input.Scope})
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Header("Location", "/v1/projects/"+c.Param("projectId")+"/workflow-runs/"+run.ID)
	c.JSON(http.StatusCreated, runResponse(run))
}
func (h *WorkflowHandler) getRun(c *gin.Context) {
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	run, err := h.facade.GetRun(c.Request.Context(), c.Param("projectId"), c.Param("runId"), actor)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Header("ETag", `"workflow-run:`+run.ID+`:`+strconv.FormatUint(run.EventCursor, 10)+`"`)
	c.JSON(http.StatusOK, runResponse(run))
}
func (h *WorkflowHandler) listRuns(c *gin.Context) {
	if !allowWorkflowQuery(c, "status", "limit", "cursor") {
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_workflow_query", "Workflow query is invalid", "limit must be an integer from 1 to 100."))
			return
		}
		limit = parsed
	}
	page, err := h.facade.ListRuns(c.Request.Context(), c.Param("projectId"), actor, runtime.RunListOptions{
		Status: runtime.RunStatus(c.Query("status")), Limit: limit, Cursor: c.Query("cursor"),
	})
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}
func (h *WorkflowHandler) events(c *gin.Context) {
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	after, _ := strconv.ParseUint(c.Query("after"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := h.facade.Events(c.Request.Context(), c.Param("projectId"), c.Param("runId"), actor, after, limit)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}
func (h *WorkflowHandler) resume(c *gin.Context) {
	var input resumeWorkflowInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	if err := h.facade.Resume(c.Request.Context(), c.Param("projectId"), c.Param("runId"), input.NodeKey, actor, input.Output); err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *WorkflowHandler) authorizeExecution(c *gin.Context) {
	var input executeWorkflowInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	if err := h.facade.AuthorizeExecution(c.Request.Context(), c.Param("projectId"), c.Param("runId"), input.NodeKey, actor); err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *WorkflowHandler) recordProposal(c *gin.Context) {
	var input proposalWorkflowInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	if err := h.facade.RecordProposal(c.Request.Context(), c.Param("projectId"), c.Param("runId"), input.NodeKey, actor, input.Proposal); err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *WorkflowHandler) approve(c *gin.Context) {
	var input approveWorkflowInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	if err := h.facade.ResolveReview(c.Request.Context(), c.Param("projectId"), c.Param("runId"), input.NodeKey, actor, input.Resolution, input.Reason); err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *WorkflowHandler) cancel(c *gin.Context) {
	h.reasonAction(c, func(ctx context.Context, projectID, runID, nodeKey, actor, reason string) error {
		return h.facade.Cancel(ctx, projectID, runID, actor, reason)
	})
}
func (h *WorkflowHandler) retry(c *gin.Context) { h.reasonAction(c, h.facade.Retry) }
func (h *WorkflowHandler) waive(c *gin.Context) { h.reasonAction(c, h.facade.Waive) }
func (h *WorkflowHandler) reasonAction(c *gin.Context, action func(context.Context, string, string, string, string, string) error) {
	var input reasonWorkflowInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := workflowActor(c)
	if !ok {
		return
	}
	if err := action(c.Request.Context(), c.Param("projectId"), c.Param("runId"), input.NodeKey, actor, input.Reason); err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func workflowActor(c *gin.Context) (string, bool) {
	identity, ok := worksmiddleware.GetIdentity(c)
	if !ok {
		problem.Write(c, problem.New(http.StatusUnauthorized, "authentication_required", "Authentication required", "A valid session is required."))
		return "", false
	}
	return identity.Session.User.ID, true
}
func allowWorkflowQuery(c *gin.Context, allowed ...string) bool {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key := range c.Request.URL.Query() {
		if !set[key] {
			problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_workflow_query", "Workflow query is invalid", "Unknown query parameter: "+key+"."))
			return false
		}
	}
	return true
}
func definitionResponse(record runtime.DefinitionRecord) gin.H {
	return gin.H{"id": record.Definition.ID, "versionId": record.VersionID, "projectId": record.ProjectID, "key": record.Key, "title": record.Title, "description": record.Description, "published": record.Published, "version": record.Definition.Version, "contentHash": record.Definition.Hash, "definition": record.Definition}
}
func runResponse(run *runtime.RunRecord) gin.H {
	nodes := make([]*runtime.NodeRecord, 0, len(run.Nodes))
	for _, node := range run.Nodes {
		nodes = append(nodes, node)
	}
	return gin.H{"id": run.ID, "projectId": run.ProjectID, "definitionVersionId": run.DefinitionVersionID, "definition": run.Definition, "inputManifest": run.InputManifest, "status": run.Status, "scope": run.Scope, "context": run.Context, "eventCursor": run.EventCursor, "startedBy": run.StartedBy, "startedAt": run.StartedAt, "completedAt": run.CompletedAt, "cancelledAt": run.CancelledAt, "failure": run.Failure, "createdAt": run.CreatedAt, "updatedAt": run.UpdatedAt, "nodes": nodes}
}
func writeWorkflowError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound), errors.Is(err, domain.ErrNotFound):
		problem.Write(c, problem.New(http.StatusNotFound, "not_found", "Resource not found", "The workflow resource was not found."))
	case errors.Is(err, core.ErrForbidden):
		problem.WriteError(c, err)
	case errors.Is(err, runtime.ErrCASConflict), errors.Is(err, runtime.ErrLeaseLost), errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrStaleProposal):
		problem.Write(c, problem.New(http.StatusConflict, "workflow_conflict", "Workflow conflict", "The workflow changed or its lease is no longer valid."))
	case errors.Is(err, domain.ErrSelfApproval), errors.Is(err, core.ErrSelfApproval):
		problem.Write(c, problem.New(http.StatusConflict, "self_approval", "Self approval is not allowed", "The workflow starter cannot approve this gate."))
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, domain.ErrValidation), errors.Is(err, domain.ErrInvalidTransition):
		problem.Write(c, problem.New(http.StatusUnprocessableEntity, "invalid_workflow_transition", "Workflow transition is invalid", err.Error()))
	default:
		problem.WriteError(c, err)
	}
}
