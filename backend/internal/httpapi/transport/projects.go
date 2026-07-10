package transport

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type projectResponse struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	Lifecycle       string    `json:"lifecycle"`
	CurrentUserRole core.Role `json:"currentUserRole"`
	CreatedBy       string    `json:"createdBy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	ETag            string    `json:"etag"`
}

type createProjectInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	TeamID      *string `json:"teamId,omitempty"`
}

type updateProjectInput struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Lifecycle   *string `json:"lifecycle,omitempty"`
}

func (s *Server) ListProjects(context *gin.Context) {
	identity, _ := worksmiddleware.GetIdentity(context)
	projects, err := s.services.Projects.List(context.Request.Context(), identity.Session.User.ID)
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	items := make([]projectResponse, 0, len(projects))
	for _, project := range projects {
		items = append(items, projectDTO(project))
	}
	context.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

func (s *Server) GetProject(context *gin.Context) {
	identity, _ := worksmiddleware.GetIdentity(context)
	project, err := s.services.Projects.Get(context.Request.Context(), context.Param("projectId"), identity.Session.User.ID)
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Header("ETag", project.ETag)
	context.JSON(http.StatusOK, projectDTO(project))
}

func (s *Server) CreateProject(context *gin.Context) {
	var input createProjectInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	identity, _ := worksmiddleware.GetIdentity(context)
	created, err := s.services.Projects.Create(context.Request.Context(), identity.Session.User.ID, core.CreateProjectInput{
		Name: input.Name, Description: input.Description,
	})
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Header("ETag", created.Project.ETag)
	context.Header("Location", "/v1/projects/"+created.Project.ID)
	context.Header("X-Initial-Artifact-ID", created.InitialArtifactID)
	context.Header("X-Initial-Artifact-Draft-ID", created.InitialArtifactDraftID)
	context.JSON(http.StatusCreated, projectDTO(created.Project))
}

func (s *Server) UpdateProject(context *gin.Context) {
	projectID := context.Param("projectId")
	expectedVersion, ok := parseProjectETag(worksmiddleware.IfMatch(context), projectID)
	if !ok {
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_project_etag", "Invalid project ETag", "If-Match does not identify this project version."))
		return
	}
	var input updateProjectInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	identity, _ := worksmiddleware.GetIdentity(context)
	project, err := s.services.Projects.Update(context.Request.Context(), projectID, identity.Session.User.ID, expectedVersion, core.UpdateProjectInput{
		Name: input.Name, Description: input.Description, Lifecycle: input.Lifecycle,
	})
	if errors.Is(err, core.ErrConflict) {
		problem.Write(context, problem.New(http.StatusPreconditionFailed, "etag_mismatch", "Precondition failed", "The project changed since it was loaded."))
		return
	}
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Header("ETag", project.ETag)
	context.JSON(http.StatusOK, projectDTO(project))
}

func (s *Server) ArchiveProject(context *gin.Context) {
	projectID := context.Param("projectId")
	expectedVersion, ok := parseProjectETag(worksmiddleware.IfMatch(context), projectID)
	if !ok {
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_project_etag", "Invalid project ETag", "If-Match does not identify this project version."))
		return
	}
	identity, _ := worksmiddleware.GetIdentity(context)
	if _, err := s.services.Access.Authorize(context.Request.Context(), projectID, identity.Session.User.ID, core.ActionAdmin); err != nil {
		problem.WriteError(context, err)
		return
	}
	lifecycle := "archived"
	_, err := s.services.Projects.Update(context.Request.Context(), projectID, identity.Session.User.ID, expectedVersion, core.UpdateProjectInput{Lifecycle: &lifecycle})
	if errors.Is(err, core.ErrConflict) {
		problem.Write(context, problem.New(http.StatusPreconditionFailed, "etag_mismatch", "Precondition failed", "The project changed since it was loaded."))
		return
	}
	if err != nil {
		s.writeServiceError(worksmiddleware.GetRequestID(context), context.FullPath(), err)
		problem.WriteError(context, err)
		return
	}
	context.Status(http.StatusNoContent)
}

func (s *Server) AuthorizeProject(context *gin.Context) {
	identity, _ := worksmiddleware.GetIdentity(context)
	projectID := context.Param("projectId")
	action := core.Action(strings.TrimSpace(context.Query("action")))
	if !validProjectAction(action) {
		problem.Write(context, problem.New(http.StatusBadRequest, "invalid_action", "Invalid authorization action", "The requested project action is not supported."))
		return
	}
	role, err := s.services.Access.Authorize(context.Request.Context(), projectID, identity.Session.User.ID, action)
	if err != nil {
		if errors.Is(err, core.ErrForbidden) {
			context.JSON(http.StatusOK, gin.H{"projectId": projectID, "action": action, "allowed": false, "role": role})
			return
		}
		problem.WriteError(context, err)
		return
	}
	context.JSON(http.StatusOK, gin.H{"projectId": projectID, "action": action, "allowed": true, "role": role})
}

func validProjectAction(action core.Action) bool {
	switch action {
	case core.ActionView, core.ActionComment, core.ActionEdit, core.ActionReview,
		core.ActionApprove, core.ActionPublish, core.ActionAdmin:
		return true
	default:
		return false
	}
}

func projectDTO(project core.Project) projectResponse {
	return projectResponse{
		ID: project.ID, Name: project.Name, Description: project.Description,
		Lifecycle: project.Lifecycle, CurrentUserRole: project.Role,
		CreatedBy: project.CreatedBy, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt,
		ETag: project.ETag,
	}
}

func parseProjectETag(value, projectID string) (uint64, bool) {
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		return 0, false
	}
	prefix := "project:" + projectID + ":"
	if !strings.HasPrefix(unquoted, prefix) {
		return 0, false
	}
	version, err := strconv.ParseUint(strings.TrimPrefix(unquoted, prefix), 10, 64)
	return version, err == nil && version > 0
}
