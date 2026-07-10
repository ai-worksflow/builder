package transport

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	documentcollaboration "github.com/worksflow/builder/backend/internal/collaboration"
	"github.com/worksflow/builder/backend/internal/core"
	worksmiddleware "github.com/worksflow/builder/backend/internal/httpapi/middleware"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type replaceDocumentMemberBindingsInput struct {
	Items []documentcollaboration.DocumentMemberBindingInput `json:"items"`
}

func (s *Server) GetDocumentMemberBindings(context *gin.Context) {
	if s.services.Collaboration == nil {
		serviceUnavailable(context, "document collaboration")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	bindings, err := s.services.Collaboration.GetMemberBindings(
		context.Request.Context(), context.Param("artifactId"), actor,
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", bindings.ETag)
	context.JSON(http.StatusOK, bindings)
}

func (s *Server) ReplaceDocumentMemberBindings(context *gin.Context) {
	if s.services.Collaboration == nil {
		serviceUnavailable(context, "document collaboration")
		return
	}
	var input replaceDocumentMemberBindingsInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	bindings, err := s.services.Collaboration.ReplaceMemberBindings(
		context.Request.Context(), context.Param("artifactId"), actor,
		worksmiddleware.IfMatch(context), input.Items,
	)
	if errors.Is(err, core.ErrConflict) {
		problem.Write(context, problem.New(http.StatusPreconditionFailed, "etag_mismatch", "Precondition failed", "Document member bindings changed since they were loaded."))
		return
	}
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", bindings.ETag)
	context.JSON(http.StatusOK, bindings)
}

func (s *Server) GetDocumentGraph(context *gin.Context) {
	if s.services.Collaboration == nil {
		serviceUnavailable(context, "document collaboration")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	graph, err := s.services.Collaboration.GetDocumentGraph(
		context.Request.Context(), context.Param("projectId"), actor,
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.JSON(http.StatusOK, graph)
}

func (s *Server) GenerateDownstreamDocument(context *gin.Context) {
	if s.services.Collaboration == nil {
		serviceUnavailable(context, "document collaboration")
		return
	}
	var input documentcollaboration.GenerateDownstreamDocumentInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	input.CommandKey = worksmiddleware.IdempotencyKey(context)
	generated, err := s.services.Collaboration.GenerateDownstreamDocument(
		context.Request.Context(), context.Param("projectId"), actor, input,
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("Location", "/v1/output-proposals/"+generated.Proposal.ID)
	context.JSON(http.StatusCreated, generated)
}

func (s *Server) CreateDocumentSyncBackProposal(context *gin.Context) {
	if s.services.Collaboration == nil {
		serviceUnavailable(context, "document collaboration")
		return
	}
	var input documentcollaboration.CreateSyncBackProposalInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	generated, err := s.services.Collaboration.CreateSyncBackProposal(
		context.Request.Context(), context.Param("projectId"), actor, input,
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("Location", "/v1/output-proposals/"+generated.Proposal.ID)
	context.JSON(http.StatusCreated, generated)
}
