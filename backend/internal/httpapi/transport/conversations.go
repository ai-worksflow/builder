package transport

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/conversation"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type ConversationAPI interface {
	Create(context.Context, string, string, conversation.CreateConversationInput) (conversation.Conversation, error)
	Get(context.Context, string, string, string) (conversation.Conversation, error)
	List(context.Context, string, string, conversation.ListOptions) (conversation.ConversationPage, error)
	Update(context.Context, string, string, string, string, conversation.UpdateConversationInput) (conversation.Conversation, error)
	AppendMessage(context.Context, string, string, string, conversation.AppendMessageInput) (conversation.Message, error)
	ListMessages(context.Context, string, string, string, conversation.ListOptions) (conversation.MessagePage, error)
	CreateSummaryCheckpoint(context.Context, string, string, string, string, conversation.CreateSummaryCheckpointInput) (conversation.ConversationSummaryCheckpoint, error)
	GetSummaryCheckpoint(context.Context, string, string, string, string) (conversation.ConversationSummaryCheckpoint, error)
	ListSummaryCheckpoints(context.Context, string, string, string, conversation.ListOptions) (conversation.SummaryCheckpointPage, error)
	ListSummaryCheckpointSourceMessages(context.Context, string, string, string, string, conversation.ListOptions) (conversation.MessagePage, error)
	DecideSummaryCheckpoint(context.Context, string, string, string, string, string, conversation.DecideSummaryCheckpointInput) (conversation.ConversationSummaryCheckpoint, error)
	CreateIntentProposal(context.Context, string, string, string, conversation.CreateIntentProposalInput) (conversation.WorkflowIntentProposal, conversation.Message, error)
	GenerateIntentProposal(context.Context, string, string, string, conversation.GenerateIntentProposalInput) (conversation.GeneratedIntentProposal, error)
	GetProposal(context.Context, string, string, string, string) (conversation.WorkflowIntentProposal, error)
	ListProposals(context.Context, string, string, string, conversation.ListOptions) (conversation.ProposalPage, error)
	DecideProposal(context.Context, string, string, string, string, string, conversation.DecideProposalInput) (conversation.WorkflowIntentProposal, *conversation.ConversationCommand, error)
	GetCommand(context.Context, string, string, string, string) (conversation.ConversationCommand, error)
	ListCommands(context.Context, string, string, string, conversation.ListOptions) (conversation.CommandPage, error)
	ExecuteCommand(context.Context, string, string, string, string, string, conversation.ExecuteCommandInput) (conversation.ConversationCommand, error)
	RejectCommand(context.Context, string, string, string, string, string, conversation.RejectCommandInput) (conversation.ConversationCommand, error)
}

type ConversationDependencies struct {
	Service          ConversationAPI
	MaxJSONBodyBytes int64
}

type ConversationHandler struct {
	service ConversationAPI
	maxBody int64
}

func NewConversationHandler(dependencies ConversationDependencies) (*ConversationHandler, error) {
	if dependencies.Service == nil {
		return nil, errors.New("conversation service is required")
	}
	if dependencies.MaxJSONBodyBytes <= 0 {
		dependencies.MaxJSONBodyBytes = 1 << 20
	}
	return &ConversationHandler{service: dependencies.Service, maxBody: dependencies.MaxJSONBodyBytes}, nil
}

func RegisterConversationRoutes(routes gin.IRoutes, handler *ConversationHandler, mutationMiddleware ...gin.HandlerFunc) error {
	if routes == nil || handler == nil {
		return errors.New("conversation routes and handler are required")
	}
	mutation := func(handlerFunc gin.HandlerFunc) []gin.HandlerFunc {
		handlers := []gin.HandlerFunc{conversationNoStore}
		handlers = append(handlers, mutationMiddleware...)
		return append(handlers, handlerFunc)
	}
	conditional := func(handlerFunc gin.HandlerFunc) []gin.HandlerFunc {
		handlers := []gin.HandlerFunc{conversationNoStore, worksmiddleware.RequireIfMatch()}
		handlers = append(handlers, mutationMiddleware...)
		return append(handlers, handlerFunc)
	}

	routes.GET("/projects/:projectId/conversations", conversationNoStore, handler.list)
	routes.POST("/projects/:projectId/conversations", mutation(handler.create)...)
	routes.GET("/projects/:projectId/conversations/:conversationId", conversationNoStore, handler.get)
	routes.PATCH("/projects/:projectId/conversations/:conversationId", conditional(handler.update)...)

	routes.GET("/projects/:projectId/conversations/:conversationId/messages", conversationNoStore, handler.listMessages)
	routes.POST("/projects/:projectId/conversations/:conversationId/messages", mutation(handler.appendMessage)...)
	routes.GET("/projects/:projectId/conversations/:conversationId/summary-checkpoints", conversationNoStore, handler.listSummaryCheckpoints)
	routes.POST("/projects/:projectId/conversations/:conversationId/summary-checkpoints", conditional(handler.createSummaryCheckpoint)...)
	routes.GET("/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId", conversationNoStore, handler.getSummaryCheckpoint)
	routes.GET("/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/source-messages", conversationNoStore, handler.listSummaryCheckpointSourceMessages)
	routes.POST("/projects/:projectId/conversations/:conversationId/summary-checkpoints/:checkpointId/decision", conditional(handler.decideSummaryCheckpoint)...)

	routes.GET("/projects/:projectId/conversations/:conversationId/intent-proposals", conversationNoStore, handler.listProposals)
	routes.POST("/projects/:projectId/conversations/:conversationId/intent-proposals", mutation(handler.createProposal)...)
	routes.POST("/projects/:projectId/conversations/:conversationId/intent-proposals/generate", mutation(handler.generateProposal)...)
	routes.GET("/projects/:projectId/conversations/:conversationId/intent-proposals/:proposalId", conversationNoStore, handler.getProposal)
	routes.POST("/projects/:projectId/conversations/:conversationId/intent-proposals/:proposalId/decision", conditional(handler.decideProposal)...)

	routes.GET("/projects/:projectId/conversations/:conversationId/commands", conversationNoStore, handler.listCommands)
	routes.GET("/projects/:projectId/conversations/:conversationId/commands/:commandId", conversationNoStore, handler.getCommand)
	routes.POST("/projects/:projectId/conversations/:conversationId/commands/:commandId/execute", conditional(handler.executeCommand)...)
	routes.POST("/projects/:projectId/conversations/:conversationId/commands/:commandId/reject", conditional(handler.rejectCommand)...)
	return nil
}

func conversationNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func (h *ConversationHandler) create(c *gin.Context) {
	var input conversation.CreateConversationInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.Create(c.Request.Context(), c.Param("projectId"), actor, input)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.Header("ETag", result.ETag)
	c.Header("Location", "/v1/projects/"+c.Param("projectId")+"/conversations/"+result.ID)
	c.JSON(http.StatusCreated, result)
}

func (h *ConversationHandler) get(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.Get(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) list(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	options, ok := conversationListOptions(c)
	if !ok {
		return
	}
	result, err := h.service.List(c.Request.Context(), c.Param("projectId"), actor, options)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) update(c *gin.Context) {
	var input conversation.UpdateConversationInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.Update(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, worksmiddleware.IfMatch(c), input)
	if err != nil {
		writeConversationError(c, err, true)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) appendMessage(c *gin.Context) {
	var input conversation.AppendMessageInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.AppendMessage(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, input)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *ConversationHandler) listMessages(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	options, ok := conversationListOptions(c)
	if !ok {
		return
	}
	result, err := h.service.ListMessages(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, options)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) createSummaryCheckpoint(c *gin.Context) {
	var input conversation.CreateSummaryCheckpointInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.CreateSummaryCheckpoint(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor,
		worksmiddleware.IfMatch(c), input,
	)
	if err != nil {
		writeConversationError(c, err, true)
		return
	}
	c.Header("ETag", result.ETag)
	c.Header("Location", conversationBasePath(c)+"/summary-checkpoints/"+result.ID)
	c.JSON(http.StatusCreated, result)
}

func (h *ConversationHandler) getSummaryCheckpoint(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.GetSummaryCheckpoint(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("checkpointId"), actor,
	)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) listSummaryCheckpoints(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	options, ok := conversationListOptions(c)
	if !ok {
		return
	}
	result, err := h.service.ListSummaryCheckpoints(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, options,
	)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) listSummaryCheckpointSourceMessages(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	options, ok := conversationListOptions(c)
	if !ok {
		return
	}
	result, err := h.service.ListSummaryCheckpointSourceMessages(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("checkpointId"), actor, options,
	)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) decideSummaryCheckpoint(c *gin.Context) {
	var input conversation.DecideSummaryCheckpointInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.DecideSummaryCheckpoint(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("checkpointId"),
		actor, worksmiddleware.IfMatch(c), input,
	)
	if err != nil {
		writeConversationError(c, err, true)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) createProposal(c *gin.Context) {
	var input conversation.CreateIntentProposalInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	proposal, message, err := h.service.CreateIntentProposal(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, input)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.Header("ETag", proposal.ETag)
	c.Header("Location", conversationBasePath(c)+"/intent-proposals/"+proposal.ID)
	c.JSON(http.StatusCreated, gin.H{"proposal": proposal, "message": message})
}

func (h *ConversationHandler) generateProposal(c *gin.Context) {
	var input conversation.GenerateIntentProposalInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.GenerateIntentProposal(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, input)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.Header("ETag", result.Proposal.ETag)
	c.Header("Location", conversationBasePath(c)+"/intent-proposals/"+result.Proposal.ID)
	c.JSON(http.StatusCreated, result)
}

func (h *ConversationHandler) getProposal(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.GetProposal(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("proposalId"), actor)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) listProposals(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	options, ok := conversationListOptions(c)
	if !ok {
		return
	}
	result, err := h.service.ListProposals(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, options)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) decideProposal(c *gin.Context) {
	var input conversation.DecideProposalInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	proposal, command, err := h.service.DecideProposal(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("proposalId"),
		actor, worksmiddleware.IfMatch(c), input,
	)
	if err != nil {
		writeConversationError(c, err, true)
		return
	}
	c.Header("ETag", proposal.ETag)
	response := gin.H{"proposal": proposal}
	if command != nil {
		response["command"] = command
		c.Header("X-Command-ETag", command.ETag)
		c.Header("X-Command-Location", conversationBasePath(c)+"/commands/"+command.ID)
	}
	c.JSON(http.StatusOK, response)
}

func (h *ConversationHandler) getCommand(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.GetCommand(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("commandId"), actor)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) listCommands(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	options, ok := conversationListOptions(c)
	if !ok {
		return
	}
	result, err := h.service.ListCommands(c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), actor, options)
	if err != nil {
		writeConversationError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) executeCommand(c *gin.Context) {
	var input conversation.ExecuteCommandInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.ExecuteCommand(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("commandId"),
		actor, worksmiddleware.IfMatch(c), input,
	)
	if err != nil {
		writeConversationError(c, err, true)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func (h *ConversationHandler) rejectCommand(c *gin.Context) {
	var input conversation.RejectCommandInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.service.RejectCommand(
		c.Request.Context(), c.Param("projectId"), c.Param("conversationId"), c.Param("commandId"),
		actor, worksmiddleware.IfMatch(c), input,
	)
	if err != nil {
		writeConversationError(c, err, true)
		return
	}
	c.Header("ETag", result.ETag)
	c.JSON(http.StatusOK, result)
}

func conversationListOptions(c *gin.Context) (conversation.ListOptions, bool) {
	for key := range c.Request.URL.Query() {
		if key != "limit" && key != "cursor" {
			problem.Write(c, problem.New(http.StatusBadRequest, "invalid_query", "Invalid query parameter", "Only limit and cursor are supported."))
			return conversation.ListOptions{}, false
		}
	}
	options := conversation.ListOptions{Cursor: strings.TrimSpace(c.Query("cursor"))}
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 200 {
			problem.Write(c, problem.New(http.StatusBadRequest, "invalid_page_limit", "Invalid page limit", "limit must be an integer from 1 to 200."))
			return conversation.ListOptions{}, false
		}
		options.Limit = limit
	}
	return options, true
}

func conversationBasePath(c *gin.Context) string {
	return "/v1/projects/" + c.Param("projectId") + "/conversations/" + c.Param("conversationId")
}

func writeConversationError(c *gin.Context, err error, conditional bool) {
	var checkpointRequired *conversation.IntentSummaryCheckpointRequiredError
	if errors.As(err, &checkpointRequired) {
		details := problem.New(http.StatusConflict, "conversation_summary_checkpoint_required", "Controlled summary checkpoint required", "The approved conversation checkpoint plus the complete continuous tail through this message exceeds the explicit AI context budget. Create and review the recommended immutable prefix checkpoint; no messages were silently omitted.")
		details.Extensions = map[string]interface{}{
			"triggerMessageId":            checkpointRequired.TriggerMessageID,
			"triggerSequence":             checkpointRequired.TriggerSequence,
			"messageCount":                checkpointRequired.MessageCount,
			"messageContentBytes":         checkpointRequired.MessageContentBytes,
			"contextBytes":                checkpointRequired.ContextBytes,
			"currentApprovedCheckpointId": checkpointRequired.CurrentApprovedCheckpointID,
			"currentThroughSequence":      checkpointRequired.CurrentThroughSequence,
			"recommendedThroughMessageId": checkpointRequired.RecommendedThroughMessageID,
			"recommendedThroughSequence":  checkpointRequired.RecommendedThroughSequence,
			"createHref":                  conversationBasePath(c) + "/summary-checkpoints",
		}
		problem.Write(c, details)
		return
	}
	switch {
	case errors.Is(err, conversation.ErrSummaryCheckpointChainStale):
		problem.Write(c, problem.New(http.StatusConflict, "conversation_summary_checkpoint_chain_stale", "Summary checkpoint chain changed", "Another approved checkpoint advanced the conversation summary chain. Refresh and create a new checkpoint from the current approved head."))
	case conditional && errors.Is(err, core.ErrConflict):
		problem.Write(c, problem.New(http.StatusPreconditionFailed, "etag_mismatch", "Precondition failed", "The conversation control-plane resource changed since it was loaded."))
	case errors.Is(err, conversation.ErrIntentSummaryCheckpointRequired):
		problem.Write(c, problem.New(http.StatusConflict, "conversation_summary_checkpoint_required", "Controlled summary checkpoint required", "The complete ordered conversation through this message exceeds the explicit AI context budget. Create and review a controlled summary checkpoint before generating another intent; no messages were silently omitted."))
	case errors.Is(err, ai.ErrNotConfigured), errors.Is(err, ai.ErrUnavailable):
		problem.Write(c, problem.New(http.StatusServiceUnavailable, "ai_unavailable", "AI service unavailable", "The AI intent proposal service is not available."))
	case errors.Is(err, ai.ErrRateLimited):
		problem.Write(c, problem.New(http.StatusTooManyRequests, "ai_rate_limited", "AI rate limit exceeded", "Try generating the intent proposal again later."))
	case errors.Is(err, ai.ErrContextTooLong):
		problem.Write(c, problem.New(http.StatusRequestEntityTooLarge, "ai_context_too_long", "Conversation context is too large", "Use a shorter conversation context or fewer workflow candidates."))
	case errors.Is(err, ai.ErrInvalidOutput):
		problem.Write(c, problem.New(http.StatusBadGateway, "ai_invalid_output", "AI output was rejected", "The AI response did not satisfy the workflow intent proposal schema."))
	default:
		writeBusinessProblem(c, err)
	}
}
