package transport

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

func (s *Server) ListTraceLinks(context *gin.Context) {
	if s.services.Traces == nil {
		serviceUnavailable(context, "trace")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Traces.ListLinks(
		context.Request.Context(), context.Param("projectId"), actor, strings.TrimSpace(context.Query("artifactId")),
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, items)
}

func (s *Server) CreateTraceLink(context *gin.Context) {
	if s.services.Traces == nil {
		serviceUnavailable(context, "trace")
		return
	}
	var input core.CreateTraceLinkInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	created, err := s.services.Traces.CreateLink(context.Request.Context(), context.Param("projectId"), actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("Location", "/v1/projects/"+created.ProjectID+"/traces/"+created.ID)
	context.JSON(http.StatusCreated, created)
}

func (s *Server) GetTraceMatrix(context *gin.Context) {
	if s.services.Traces == nil {
		serviceUnavailable(context, "trace")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	projectID := context.Param("projectId")
	items, err := s.services.Traces.ListLinks(context.Request.Context(), projectID, actor, strings.TrimSpace(context.Query("artifactId")))
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.JSON(http.StatusOK, gin.H{"projectId": projectID, "links": items})
}

func (s *Server) ListArtifactDependencies(context *gin.Context) {
	if s.services.Traces == nil {
		serviceUnavailable(context, "trace")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Traces.ListDependencies(context.Request.Context(), context.Param("artifactId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, items)
}

func (s *Server) CreateProjectDependency(context *gin.Context) {
	s.createDependency(context, "")
}

func (s *Server) CreateArtifactDependency(context *gin.Context) {
	s.createDependency(context, context.Param("artifactId"))
}

func (s *Server) createDependency(context *gin.Context, targetArtifactID string) {
	if s.services.Traces == nil {
		serviceUnavailable(context, "trace")
		return
	}
	var input core.CreateDependencyInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	if targetArtifactID != "" && input.Target.ArtifactID != targetArtifactID {
		problem.WriteError(context, core.ErrInvalidInput)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	projectID := context.Param("projectId")
	if projectID == "" && s.services.Artifacts != nil {
		artifact, err := s.services.Artifacts.Get(context.Request.Context(), targetArtifactID, actor, false)
		if err != nil {
			s.businessError(context, err)
			return
		}
		projectID = artifact.Artifact.ProjectID
	}
	if projectID == "" {
		problem.WriteError(context, core.ErrInvalidInput)
		return
	}
	created, err := s.services.Traces.CreateDependency(context.Request.Context(), projectID, actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("Location", "/v1/artifacts/"+created.Target.ArtifactID+"/dependencies/"+created.ID)
	context.JSON(http.StatusCreated, created)
}
