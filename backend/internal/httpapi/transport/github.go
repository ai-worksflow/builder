package transport

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	worksgithub "github.com/worksflow/builder/backend/internal/github"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type GitHubAPI interface {
	Connect(context.Context, string, string, string) (worksgithub.ConnectionStatus, error)
	Disconnect(context.Context, string, string) (worksgithub.ConnectionStatus, error)
	Status(context.Context, string, string) (worksgithub.ConnectionStatus, error)
	Repositories(context.Context, string, string) ([]worksgithub.Repository, error)
	Branches(context.Context, string, string, string, string) ([]worksgithub.Branch, error)
	Preview(context.Context, string, string, worksgithub.PreviewInput) (worksgithub.ChangesPreview, error)
	Push(context.Context, string, string, worksgithub.PushInput) (worksgithub.PushResult, error)
	CreatePullRequest(context.Context, string, string, worksgithub.PullRequestInput) (worksgithub.PullRequestResult, error)
}

type GitHubHandler struct {
	service GitHubAPI
	maxBody int64
}

func NewGitHubHandler(service GitHubAPI, maxBody int64) (*GitHubHandler, error) {
	if service == nil {
		return nil, errors.New("GitHub service is required")
	}
	if maxBody <= 0 || maxBody > worksgithub.MaxRequestBytes {
		maxBody = worksgithub.MaxRequestBytes
	}
	return &GitHubHandler{service: service, maxBody: maxBody}, nil
}

func RegisterGitHubRoutes(routes gin.IRoutes, handler *GitHubHandler, mutationMiddleware ...gin.HandlerFunc) error {
	if routes == nil || handler == nil {
		return errors.New("GitHub routes and handler are required")
	}
	routes.GET("/projects/:projectId/github/status", handler.status)
	routes.POST("/projects/:projectId/github/connect", githubHandlers(mutationMiddleware, handler.connect)...)
	routes.POST("/projects/:projectId/github/disconnect", githubHandlers(mutationMiddleware, handler.disconnect)...)
	routes.GET("/projects/:projectId/github/repositories", handler.repositories)
	routes.GET("/projects/:projectId/github/branches", handler.branches)
	routes.POST("/projects/:projectId/github/preview", githubHandlers(mutationMiddleware, handler.preview)...)
	routes.POST("/projects/:projectId/github/push", githubHandlers(mutationMiddleware, handler.push)...)
	routes.POST("/projects/:projectId/github/pull-requests", githubHandlers(mutationMiddleware, handler.pullRequest)...)
	return nil
}

func githubHandlers(middleware []gin.HandlerFunc, handler gin.HandlerFunc) []gin.HandlerFunc {
	result := make([]gin.HandlerFunc, 0, len(middleware)+1)
	result = append(result, middleware...)
	return append(result, handler)
}

func (h *GitHubHandler) connect(c *gin.Context) {
	var input struct {
		Token string `json:"token"`
	}
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	value, err := h.service.Connect(c.Request.Context(), c.Param("projectId"), actor, input.Token)
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"connection": value})
}
func (h *GitHubHandler) disconnect(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	value, err := h.service.Disconnect(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"connection": value})
}
func (h *GitHubHandler) status(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	value, err := h.service.Status(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"connection": value})
}
func (h *GitHubHandler) repositories(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	items, err := h.service.Repositories(c.Request.Context(), c.Param("projectId"), actor)
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"repositories": items})
}
func (h *GitHubHandler) branches(c *gin.Context) {
	actor, ok := actorID(c)
	if !ok {
		return
	}
	items, err := h.service.Branches(c.Request.Context(), c.Param("projectId"), actor, c.Query("owner"), c.Query("repo"))
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"branches": items})
}
func (h *GitHubHandler) preview(c *gin.Context) {
	var input worksgithub.PreviewInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	value, err := h.service.Preview(c.Request.Context(), c.Param("projectId"), actor, input)
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"preview": value})
}
func (h *GitHubHandler) push(c *gin.Context) {
	var input worksgithub.PushInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	value, err := h.service.Push(c.Request.Context(), c.Param("projectId"), actor, input)
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": value})
}
func (h *GitHubHandler) pullRequest(c *gin.Context) {
	var input worksgithub.PullRequestInput
	if err := DecodeJSON(c, &input, h.maxBody); err != nil {
		WriteJSONError(c, err)
		return
	}
	actor, ok := actorID(c)
	if !ok {
		return
	}
	value, err := h.service.CreatePullRequest(c.Request.Context(), c.Param("projectId"), actor, input)
	if err != nil {
		writeGitHubError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"pullRequest": value})
}

func writeGitHubError(c *gin.Context, err error) {
	if integrationError, ok := worksgithub.AsError(err); ok {
		if integrationError.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(integrationError.RetryAfter))
		}
		problem.Write(c, problem.New(integrationError.Status, integrationError.Code, http.StatusText(integrationError.Status), integrationError.Detail))
		return
	}
	switch {
	case errors.Is(err, core.ErrNotFound):
		problem.Write(c, problem.New(http.StatusNotFound, "not_found", "Resource not found", "The project or GitHub integration was not found."))
	case errors.Is(err, core.ErrForbidden):
		problem.Write(c, problem.New(http.StatusForbidden, "forbidden", "Operation forbidden", "The current project role cannot perform this GitHub operation."))
	case errors.Is(err, core.ErrInvalidInput):
		problem.Write(c, problem.New(http.StatusBadRequest, "invalid_request", "Invalid request", err.Error()))
	default:
		problem.Write(c, problem.New(http.StatusInternalServerError, "github_internal_error", "GitHub integration failed", "The GitHub operation could not be completed."))
	}
}
