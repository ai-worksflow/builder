package transport

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
	"github.com/worksflow/builder/backend/internal/lsp"
)

const defaultLSPTicketRequestBytes int64 = 64 << 10

type LSPTicketIssuer interface {
	Issue(context.Context, lsp.IssueTicketInput) (lsp.TicketView, error)
}

type LSPProjectResolver interface {
	ResolveProject(context.Context, string, string) (string, error)
}

type LSPTicketDependencies struct {
	Tickets          LSPTicketIssuer
	Projects         LSPProjectResolver
	MaxJSONBodyBytes int64
}

type LSPTicketHandler struct {
	tickets  LSPTicketIssuer
	projects LSPProjectResolver
	maxJSON  int64
}

func NewLSPTicketHandler(dependencies LSPTicketDependencies) (*LSPTicketHandler, error) {
	if dependencies.Tickets == nil || dependencies.Projects == nil {
		return nil, errors.New("LSP ticket issuer and SandboxSession project resolver are required")
	}
	maxJSON := dependencies.MaxJSONBodyBytes
	if maxJSON <= 0 || maxJSON > defaultLSPTicketRequestBytes {
		maxJSON = defaultLSPTicketRequestBytes
	}
	return &LSPTicketHandler{tickets: dependencies.Tickets, projects: dependencies.Projects, maxJSON: maxJSON}, nil
}

// RegisterLSPTicketRoutes deliberately accepts only the caller-selected CSRF
// middleware. Generic idempotency persistence must never capture a response
// containing a one-time bearer secret.
func RegisterLSPTicketRoutes(
	routes gin.IRoutes,
	handler *LSPTicketHandler,
	csrfMiddleware ...gin.HandlerFunc,
) error {
	if routes == nil || handler == nil {
		return errors.New("LSP ticket routes and handler are required")
	}
	handlers := []gin.HandlerFunc{sandboxNoStore}
	handlers = append(handlers, csrfMiddleware...)
	handlers = append(handlers, handler.issue)
	routes.POST("/sandbox-sessions/:sessionId/lsp-tickets", handlers...)
	return nil
}

func (handler *LSPTicketHandler) issue(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	encoded, err := readStrictLSPJSON(c, handler.maxJSON)
	if err != nil {
		WriteJSONError(c, err)
		return
	}
	request, err := lsp.DecodeTicketRequest(encoded)
	if err != nil {
		problem.Write(c, problem.New(
			http.StatusBadRequest, "lsp_message_malformed", "Malformed LSP ticket request",
			"The ticket request must exactly match sandbox-lsp-ticket-request/v1.",
		))
		return
	}
	sessionID := strings.TrimSpace(c.Param("sessionId"))
	projectID, err := handler.projects.ResolveProject(c.Request.Context(), sessionID, actor)
	if err != nil {
		problem.Write(c, problem.New(
			http.StatusForbidden, "lsp_forbidden", "LSP access forbidden",
			"The authenticated actor cannot resolve this SandboxSession.",
		))
		return
	}
	view, err := handler.tickets.Issue(c.Request.Context(), lsp.IssueTicketInput{
		ProjectID: projectID, SessionID: sessionID, ActorID: actor,
		Origin: c.GetHeader("Origin"), Mode: request.Mode, Head: request.Head,
		TemplateRelease: request.TemplateRelease, ProfileIDs: request.ProfileIDs,
	})
	if err != nil {
		writeLSPProblem(c, err)
		return
	}
	c.Header("Location", "/v1/lsp-tickets/"+view.ID)
	c.JSON(http.StatusCreated, view)
}

func readStrictLSPJSON(c *gin.Context, maximum int64) ([]byte, error) {
	contentType := c.GetHeader("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return nil, errUnsupportedJSONType
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maximum)
	value, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, errEmptyJSONBody
	}
	return value, nil
}

func writeLSPProblem(c *gin.Context, err error) {
	status, code, title, detail := http.StatusServiceUnavailable, "lsp_ticket_store_unavailable", "LSP unavailable",
		"The LSP control plane is temporarily unavailable; ordinary editing and Candidate saves remain available."
	switch {
	case errors.Is(err, lsp.ErrOriginForbidden):
		status, code, title, detail = http.StatusForbidden, "lsp_origin_forbidden", "LSP Origin forbidden", "The browser Origin is not authorized for this ticket."
	case errors.Is(err, lsp.ErrForbidden):
		status, code, title, detail = http.StatusForbidden, "lsp_forbidden", "LSP access forbidden", "The actor lacks the exact project, mode, or writer-lease authority."
	case errors.Is(err, lsp.ErrSessionNotReady):
		status, code, title, detail = http.StatusConflict, "lsp_session_not_ready", "SandboxSession is not ready", "Refresh the authoritative SandboxSession before requesting another ticket."
	case errors.Is(err, lsp.ErrHeadStale):
		status, code, title, detail = http.StatusConflict, "lsp_head_stale", "Candidate head is stale", "Refresh the SandboxSession and Candidate tree; do not overwrite a dirty editor."
	case errors.Is(err, lsp.ErrProfileNotDeclared):
		status, code, title, detail = http.StatusUnprocessableEntity, "lsp_profile_not_declared", "Language server profile is not declared", "The approved exact TemplateRelease does not declare this canonical profile scope."
	case errors.Is(err, lsp.ErrTicketInvalid):
		status, code, title, detail = http.StatusBadRequest, "lsp_message_malformed", "Malformed LSP ticket request", "The ticket request failed strict validation."
	case errors.Is(err, lsp.ErrRateLimited):
		status, code, title, detail = http.StatusTooManyRequests, "lsp_rate_limited", "LSP rate limit exceeded", "Wait for the bounded retry window; ordinary editing and Candidate saves remain available."
	case errors.Is(err, lsp.ErrTicketUnavailable), errors.Is(err, lsp.ErrAuthorityUnavailable):
		// Keep the default stable, non-sensitive unavailable response.
	case errors.Is(err, lsp.ErrAuditUnavailable):
		// Security audit is mandatory; an audit outage fails closed.
	default:
		// Unknown dependency failures fail closed without exposing internals.
	}
	problem.Write(c, problem.New(status, code, title, detail))
}
