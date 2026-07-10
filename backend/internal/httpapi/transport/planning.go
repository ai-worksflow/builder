package transport

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

func (s *Server) CompileRequirementBaseline(context *gin.Context) {
	if s.services.Baselines == nil {
		serviceUnavailable(context, "requirement baseline")
		return
	}
	var input struct {
		Sources []core.VersionRef `json:"sources"`
	}
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	revision, err := s.services.Baselines.Compile(context.Request.Context(), context.Param("projectId"), actor, input.Sources)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", revisionETag(revision))
	context.Header("Location", "/v1/revisions/"+revision.ID)
	context.JSON(http.StatusCreated, revision)
}

func (s *Server) AnalyzeImpact(context *gin.Context) {
	if s.services.Impacts == nil {
		serviceUnavailable(context, "impact analysis")
		return
	}
	var input core.AnalyzeImpactInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	report, err := s.services.Impacts.Analyze(context.Request.Context(), context.Param("projectId"), actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", entityVersionETag("impact-report", report.ID, 1))
	context.Header("Location", "/v1/impact-reports/"+report.ID)
	context.JSON(http.StatusCreated, report)
}

func (s *Server) GetImpactReport(context *gin.Context) {
	if s.services.Impacts == nil {
		serviceUnavailable(context, "impact analysis")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	report, err := s.services.Impacts.Get(context.Request.Context(), context.Param("reportId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", entityVersionETag("impact-report", report.ID, 1))
	context.JSON(http.StatusOK, report)
}

func (s *Server) CreateInputManifest(context *gin.Context) {
	if s.services.Proposals == nil {
		serviceUnavailable(context, "proposal")
		return
	}
	var input core.CreateManifestInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	manifest, err := s.services.Proposals.CreateManifest(context.Request.Context(), context.Param("projectId"), actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", entityHashETag("input-manifest", manifest.ID, manifest.Hash))
	context.Header("Location", "/v1/input-manifests/"+manifest.ID)
	context.JSON(http.StatusCreated, manifest)
}

func (s *Server) GetInputManifest(context *gin.Context) {
	if s.services.Proposals == nil {
		serviceUnavailable(context, "proposal")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	manifest, err := s.services.Proposals.GetManifest(context.Request.Context(), context.Param("manifestId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", entityHashETag("input-manifest", manifest.ID, manifest.Hash))
	context.JSON(http.StatusOK, manifest)
}

func (s *Server) CreateOutputProposal(context *gin.Context) {
	if s.services.Proposals == nil {
		serviceUnavailable(context, "proposal")
		return
	}
	var input core.CreateProposalInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	proposal, err := s.services.Proposals.CreateProposal(context.Request.Context(), context.Param("projectId"), actor, input)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writeProposal(context, http.StatusCreated, proposal)
}

func (s *Server) ListOutputProposals(context *gin.Context) {
	if s.services.Proposals == nil {
		serviceUnavailable(context, "proposal")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Proposals.ListProposals(
		context.Request.Context(), context.Param("projectId"), actor, strings.TrimSpace(context.Query("status")),
	)
	if err != nil {
		s.businessError(context, err)
		return
	}
	artifactID := strings.TrimSpace(context.Query("artifactId"))
	if artifactID != "" {
		filtered := make([]domain.OutputProposal, 0, len(items))
		for _, item := range items {
			if item.ArtifactID == artifactID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writePage(context, items)
}

func (s *Server) GetOutputProposal(context *gin.Context) {
	if s.services.Proposals == nil {
		serviceUnavailable(context, "proposal")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	proposal, err := s.services.Proposals.GetProposal(context.Request.Context(), context.Param("proposalId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writeProposal(context, http.StatusOK, proposal)
}

func (s *Server) DecideOutputProposal(context *gin.Context) {
	if s.services.Proposals == nil {
		serviceUnavailable(context, "proposal")
		return
	}
	var input core.DecideProposalInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	current, err := s.services.Proposals.GetProposal(context.Request.Context(), context.Param("proposalId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if !matchETag(context, proposalETag(current), "output proposal") {
		return
	}
	if input.Version != 0 && input.Version != current.Version {
		preconditionFailed(context, "output proposal")
		return
	}
	input.Version = current.Version
	updated, err := s.services.Proposals.Decide(context.Request.Context(), current.ID, actor, input)
	if err != nil {
		conditionalServiceError(s, context, "output proposal", err)
		return
	}
	writeProposal(context, http.StatusOK, updated)
}

func (s *Server) ApplyOutputProposal(context *gin.Context) {
	if s.services.Proposals == nil {
		serviceUnavailable(context, "proposal")
		return
	}
	var input core.ApplyProposalInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	current, err := s.services.Proposals.GetProposal(context.Request.Context(), context.Param("proposalId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if !matchETag(context, proposalETag(current), "output proposal") {
		return
	}
	if input.Version != 0 && input.Version != current.Version {
		preconditionFailed(context, "output proposal")
		return
	}
	if current.Status != domain.ProposalReady {
		s.businessError(context, core.ErrConflict)
		return
	}
	input.Version = current.Version
	draft, err := s.services.Proposals.Apply(context.Request.Context(), current.ID, actor, input)
	if err != nil {
		conditionalServiceError(s, context, "output proposal", err)
		return
	}
	context.Header("ETag", draft.ETag)
	context.JSON(http.StatusOK, draft)
}

func proposalETag(proposal domain.OutputProposal) string {
	return entityVersionETag("output-proposal", proposal.ID, proposal.Version)
}

func writeProposal(context *gin.Context, status int, proposal domain.OutputProposal) {
	context.Header("ETag", proposalETag(proposal))
	if status == http.StatusCreated {
		context.Header("Location", "/v1/output-proposals/"+proposal.ID)
	}
	context.JSON(status, proposal)
}
