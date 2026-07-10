package transport

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/ai"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type generateArtifactInput struct {
	ManifestID string `json:"manifestId,omitempty"`
	Model      string `json:"model,omitempty"`
}

type generateImplementationInput struct {
	BundleID    string `json:"bundleId,omitempty"`
	Model       string `json:"model,omitempty"`
	Instruction string `json:"instruction,omitempty"`
}

func (s *Server) CreateWorkbenchBundle(context *gin.Context) {
	if s.services.Workbench == nil {
		serviceUnavailable(context, "workbench")
		return
	}
	var input core.CreateWorkbenchBundleInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	bundle, err := s.services.Workbench.CreateBundle(context.Request.Context(), context.Param("projectId"), actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writeWorkbenchBundle(context, http.StatusCreated, bundle)
}

func (s *Server) GetWorkbenchBundle(context *gin.Context) {
	if s.services.Workbench == nil {
		serviceUnavailable(context, "workbench")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	bundle, err := s.services.Workbench.GetBundle(context.Request.Context(), context.Param("bundleId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writeWorkbenchBundle(context, http.StatusOK, bundle)
}

func writeWorkbenchBundle(context *gin.Context, status int, bundle core.WorkbenchBundle) {
	context.Header("ETag", entityHashETag("workbench-bundle", bundle.ID, bundle.ManifestHash))
	if status == http.StatusCreated {
		context.Header("Location", "/v1/workbench-bundles/"+bundle.ID)
	}
	context.JSON(status, bundle)
}

func (s *Server) CreateImplementationProposal(context *gin.Context) {
	if s.services.Implementation == nil {
		serviceUnavailable(context, "implementation")
		return
	}
	var input core.CreateImplementationProposalInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	proposal, err := s.services.Implementation.Create(context.Request.Context(), context.Param("projectId"), actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writeImplementationProposal(context, http.StatusCreated, proposal)
}

func (s *Server) GetImplementationProposal(context *gin.Context) {
	if s.services.Implementation == nil {
		serviceUnavailable(context, "implementation")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	proposal, err := s.services.Implementation.Get(context.Request.Context(), context.Param("implementationProposalId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writeImplementationProposal(context, http.StatusOK, proposal)
}

func (s *Server) DecideImplementationProposal(context *gin.Context) {
	if s.services.Implementation == nil {
		serviceUnavailable(context, "implementation")
		return
	}
	var input core.DecideImplementationInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	current, err := s.services.Implementation.Get(context.Request.Context(), context.Param("implementationProposalId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if !matchETag(context, implementationProposalETag(current), "implementation proposal") {
		return
	}
	if input.Version != 0 && input.Version != current.Version {
		preconditionFailed(context, "implementation proposal")
		return
	}
	input.Version = current.Version
	updated, err := s.services.Implementation.Decide(context.Request.Context(), current.ID, actor, input)
	if err != nil {
		conditionalServiceError(s, context, "implementation proposal", err)
		return
	}
	writeImplementationProposal(context, http.StatusOK, updated)
}

func (s *Server) ApplyImplementationProposal(context *gin.Context) {
	if s.services.Implementation == nil {
		serviceUnavailable(context, "implementation")
		return
	}
	var input core.ApplyImplementationInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	current, err := s.services.Implementation.Get(context.Request.Context(), context.Param("implementationProposalId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if !matchETag(context, implementationProposalETag(current), "implementation proposal") {
		return
	}
	if input.Version != 0 && input.Version != current.Version {
		preconditionFailed(context, "implementation proposal")
		return
	}
	if current.Status != "ready" {
		s.businessError(context, core.ErrConflict)
		return
	}
	input.Version = current.Version
	revision, err := s.services.Implementation.Apply(context.Request.Context(), current.ID, actor, input)
	if err != nil {
		conditionalServiceError(s, context, "implementation proposal", err)
		return
	}
	context.Header("ETag", revisionETag(revision))
	context.Header("Location", "/v1/revisions/"+revision.ID)
	context.JSON(http.StatusOK, revision)
}

func implementationProposalETag(proposal core.ImplementationProposal) string {
	return entityVersionETag("implementation-proposal", proposal.ID, proposal.Version)
}

func writeImplementationProposal(context *gin.Context, status int, proposal core.ImplementationProposal) {
	context.Header("ETag", implementationProposalETag(proposal))
	if status == http.StatusCreated {
		context.Header("Location", "/v1/implementation-proposals/"+proposal.ID)
	}
	context.JSON(status, proposal)
}

func (s *Server) GenerateArtifactProposal(context *gin.Context) {
	if s.services.Generation == nil {
		serviceUnavailable(context, "generation")
		return
	}
	var input generateArtifactInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	manifestID := strings.TrimSpace(context.Param("manifestId"))
	if manifestID == "" {
		manifestID = strings.TrimSpace(input.ManifestID)
	}
	if manifestID == "" {
		s.businessError(context, core.ErrInvalidInput)
		return
	}
	if strings.TrimSpace(input.Model) == "" {
		s.businessError(context, core.ErrInvalidInput)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	result, err := s.services.Generation.GenerateArtifactProposal(context.Request.Context(), manifestID, actor, strings.TrimSpace(input.Model))
	if err != nil {
		s.generationError(context, err)
		return
	}
	context.Header("ETag", proposalETag(result.Proposal))
	context.Header("Location", "/v1/output-proposals/"+result.Proposal.ID)
	context.JSON(http.StatusCreated, result)
}

func (s *Server) GenerateImplementation(context *gin.Context) {
	if s.services.Generation == nil {
		serviceUnavailable(context, "generation")
		return
	}
	var input generateImplementationInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	bundleID := strings.TrimSpace(context.Param("bundleId"))
	if bundleID == "" {
		bundleID = strings.TrimSpace(input.BundleID)
	}
	if bundleID == "" {
		s.businessError(context, core.ErrInvalidInput)
		return
	}
	if strings.TrimSpace(input.Model) == "" {
		s.businessError(context, core.ErrInvalidInput)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	result, err := s.services.Generation.GenerateImplementation(
		context.Request.Context(), bundleID, actor, strings.TrimSpace(input.Model), strings.TrimSpace(input.Instruction),
	)
	if err != nil {
		s.generationError(context, err)
		return
	}
	context.Header("ETag", implementationProposalETag(result.Proposal))
	context.Header("Location", "/v1/implementation-proposals/"+result.Proposal.ID)
	context.JSON(http.StatusCreated, result)
}

func (s *Server) generationError(context *gin.Context, err error) {
	if s.logger != nil {
		s.writeServiceError(context.GetString("request_id"), context.FullPath(), err)
	}
	switch {
	case errors.Is(err, ai.ErrNotConfigured):
		problem.Write(context, problem.New(http.StatusServiceUnavailable, "ai_not_configured", "AI provider is not configured", "Configure an AI provider before starting generation."))
	case errors.Is(err, ai.ErrRateLimited):
		problem.Write(context, problem.New(http.StatusTooManyRequests, "ai_rate_limited", "AI provider rate limit exceeded", "Retry generation after the provider rate limit resets."))
	case errors.Is(err, ai.ErrUnavailable):
		problem.Write(context, problem.New(http.StatusServiceUnavailable, "ai_unavailable", "AI provider is unavailable", "The AI provider could not complete this generation request."))
	case errors.Is(err, ai.ErrInvalidOutput):
		problem.Write(context, problem.New(http.StatusBadGateway, "ai_invalid_output", "AI output is invalid", "The AI provider returned output that did not satisfy the required schema."))
	case errors.Is(err, ai.ErrContextTooLong):
		problem.Write(context, problem.New(http.StatusUnprocessableEntity, "ai_context_too_long", "AI input is too large", "Reduce or split the pinned generation input."))
	default:
		writeBusinessProblem(context, err)
	}
}
