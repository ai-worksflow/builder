package transport

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/httpapi/problem"
)

type submitReviewRequest struct {
	ArtifactID        string           `json:"artifactId,omitempty"`
	RevisionID        string           `json:"revisionId,omitempty"`
	Target            *core.VersionRef `json:"target,omitempty"`
	ReviewerIDs       []string         `json:"reviewerIds,omitempty"`
	RequiredReviewers []string         `json:"requiredReviewerIds,omitempty"`
	MinimumApprovals  int              `json:"minimumApprovals,omitempty"`
	AllowSelfApproval bool             `json:"allowSelfApproval,omitempty"`
	Summary           string           `json:"summary,omitempty"`
}

type createCommentRequest struct {
	ArtifactID      string           `json:"artifactId,omitempty"`
	RevisionID      *string          `json:"revisionId,omitempty"`
	Target          *core.VersionRef `json:"target,omitempty"`
	ParentThreadID  *string          `json:"parentId,omitempty"`
	ParentMessageID *string          `json:"parentMessageId,omitempty"`
	Body            string           `json:"body"`
	Anchor          json.RawMessage  `json:"anchor,omitempty"`
	Severity        string           `json:"severity,omitempty"`
	AssignedTo      *string          `json:"assignedTo,omitempty"`
	Mentions        []string         `json:"mentions,omitempty"`
}

func (s *Server) ListProjectReviews(context *gin.Context) {
	if s.services.Reviews == nil {
		serviceUnavailable(context, "review")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Reviews.List(context.Request.Context(), context.Param("projectId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	artifactID := strings.TrimSpace(context.Query("artifactId"))
	status := strings.TrimSpace(context.Query("status"))
	filtered := make([]core.ReviewRequest, 0, len(items))
	for _, item := range items {
		if artifactID != "" && item.ArtifactID != artifactID || status != "" && item.Status != status {
			continue
		}
		filtered = append(filtered, reviewDTO(item))
	}
	writePage(context, filtered)
}

func (s *Server) ListRevisionReviews(context *gin.Context) {
	if s.services.Reviews == nil || s.services.Artifacts == nil {
		serviceUnavailable(context, "review and artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	revisionID := context.Param("revisionId")
	revision, artifact, ok := s.revisionArtifact(context, revisionID, actor)
	if !ok {
		return
	}
	items, err := s.services.Reviews.List(context.Request.Context(), artifact.Artifact.ProjectID, actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	filtered := make([]core.ReviewRequest, 0, 1)
	for _, item := range items {
		if item.RevisionID == revision.ID {
			filtered = append(filtered, reviewDTO(item))
		}
	}
	writePage(context, filtered)
}

func (s *Server) SubmitProjectReview(context *gin.Context) {
	s.submitReview(context, context.Param("projectId"), "")
}

func (s *Server) GetReview(context *gin.Context) {
	if s.services.Reviews == nil {
		serviceUnavailable(context, "review")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	review, found := s.findReview(context, context.Param("reviewId"), context.Param("projectId"), actor)
	if !found {
		return
	}
	review = reviewDTO(review)
	context.Header("ETag", review.ETag)
	context.JSON(http.StatusOK, review)
}

func (s *Server) SubmitRevisionReview(context *gin.Context) {
	s.submitReview(context, "", context.Param("revisionId"))
}

func (s *Server) submitReview(context *gin.Context, projectID, pathRevisionID string) {
	if s.services.Reviews == nil || s.services.Artifacts == nil {
		serviceUnavailable(context, "review and artifact")
		return
	}
	var input submitReviewRequest
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	artifactID := strings.TrimSpace(input.ArtifactID)
	revisionID := strings.TrimSpace(input.RevisionID)
	if input.Target != nil {
		if artifactID == "" {
			artifactID = input.Target.ArtifactID
		}
		if revisionID == "" {
			revisionID = input.Target.RevisionID
		}
	}
	if pathRevisionID != "" {
		if revisionID != "" && revisionID != pathRevisionID {
			problem.WriteError(context, core.ErrInvalidInput)
			return
		}
		revisionID = pathRevisionID
		revision, artifact, resolved := s.revisionArtifact(context, revisionID, actor)
		if !resolved {
			return
		}
		if artifactID != "" && artifactID != revision.ArtifactID || input.Target != nil && input.Target.ContentHash != "" && input.Target.ContentHash != revision.ContentHash {
			problem.WriteError(context, core.ErrConflict)
			return
		}
		artifactID = revision.ArtifactID
		projectID = artifact.Artifact.ProjectID
	} else {
		if artifactID == "" || revisionID == "" {
			problem.WriteError(context, core.ErrInvalidInput)
			return
		}
		artifact, getErr := s.services.Artifacts.Get(context.Request.Context(), artifactID, actor, false)
		if getErr != nil {
			s.businessError(context, getErr)
			return
		}
		if artifact.Artifact.ProjectID != projectID {
			problem.WriteError(context, core.ErrNotFound)
			return
		}
		revision, revisionErr := s.services.Artifacts.GetRevision(context.Request.Context(), revisionID, actor)
		if revisionErr != nil {
			s.businessError(context, revisionErr)
			return
		}
		if revision.ArtifactID != artifactID || input.Target != nil && input.Target.ContentHash != "" && input.Target.ContentHash != revision.ContentHash {
			problem.WriteError(context, core.ErrConflict)
			return
		}
	}
	reviewers := input.ReviewerIDs
	if len(reviewers) == 0 {
		reviewers = input.RequiredReviewers
	}
	created, err := s.services.Reviews.Submit(context.Request.Context(), projectID, artifactID, actor, core.SubmitReviewInput{
		RevisionID: revisionID, ReviewerIDs: reviewers, MinimumApprovals: input.MinimumApprovals,
		AllowSelfApproval: input.AllowSelfApproval,
	})
	if err != nil {
		s.businessError(context, err)
		return
	}
	created = reviewDTO(created)
	context.Header("ETag", created.ETag)
	context.Header("Location", "/v1/reviews/"+created.ID)
	context.JSON(http.StatusCreated, created)
}

func (s *Server) DecideReview(context *gin.Context) {
	if s.services.Reviews == nil {
		serviceUnavailable(context, "review")
		return
	}
	var input core.DecideReviewInput
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	switch input.Decision {
	case "approved":
		input.Decision = "approve"
	case "changesRequested":
		input.Decision = "request_changes"
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	current, found := s.findReview(context, context.Param("reviewId"), context.Param("projectId"), actor)
	if !found {
		return
	}
	if !matchETag(context, current.ETag, "review request") {
		return
	}
	var updated core.ReviewRequest
	var err error
	if conditional, ok := s.services.Reviews.(ConditionalReviewService); ok {
		updated, err = conditional.DecideIfMatch(context.Request.Context(), current.ID, actor, current.ETag, input)
	} else {
		updated, err = s.services.Reviews.Decide(context.Request.Context(), current.ID, actor, input)
	}
	if err != nil {
		s.businessError(context, err)
		return
	}
	updated = reviewDTO(updated)
	context.Header("ETag", updated.ETag)
	context.JSON(http.StatusOK, updated)
}

func (s *Server) findReview(context *gin.Context, reviewID, projectID, actor string) (core.ReviewRequest, bool) {
	projectIDs := []string{}
	if projectID != "" {
		projectIDs = append(projectIDs, projectID)
	} else {
		if s.services.Projects == nil {
			serviceUnavailable(context, "project")
			return core.ReviewRequest{}, false
		}
		projects, err := s.services.Projects.List(context.Request.Context(), actor)
		if err != nil {
			s.businessError(context, err)
			return core.ReviewRequest{}, false
		}
		for _, project := range projects {
			projectIDs = append(projectIDs, project.ID)
		}
	}
	for _, candidateProjectID := range projectIDs {
		items, err := s.services.Reviews.List(context.Request.Context(), candidateProjectID, actor)
		if err != nil {
			s.businessError(context, err)
			return core.ReviewRequest{}, false
		}
		for _, item := range items {
			if item.ID == reviewID {
				return item, true
			}
		}
	}
	problem.WriteError(context, core.ErrNotFound)
	return core.ReviewRequest{}, false
}

func (s *Server) ListProjectComments(context *gin.Context) {
	if s.services.Comments == nil {
		serviceUnavailable(context, "comment")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Comments.ListProject(context.Request.Context(), context.Param("projectId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, filterComments(items, strings.TrimSpace(context.Query("artifactId")), strings.TrimSpace(context.Query("revisionId"))))
}

func (s *Server) ListArtifactComments(context *gin.Context) {
	if s.services.Comments == nil {
		serviceUnavailable(context, "comment")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	items, err := s.services.Comments.ListArtifact(context.Request.Context(), context.Param("artifactId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, filterComments(items, "", strings.TrimSpace(context.Query("revisionId"))))
}

func (s *Server) ListRevisionComments(context *gin.Context) {
	if s.services.Comments == nil || s.services.Artifacts == nil {
		serviceUnavailable(context, "comment and artifact")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	revision, _, resolved := s.revisionArtifact(context, context.Param("revisionId"), actor)
	if !resolved {
		return
	}
	items, err := s.services.Comments.ListArtifact(context.Request.Context(), revision.ArtifactID, actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	writePage(context, filterComments(items, "", revision.ID))
}

func (s *Server) GetComment(context *gin.Context) {
	if s.services.Comments == nil {
		serviceUnavailable(context, "comment")
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	comment, err := s.services.Comments.Get(context.Request.Context(), context.Param("commentId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", comment.ETag)
	context.JSON(http.StatusOK, comment)
}

func (s *Server) CreateProjectComment(context *gin.Context) {
	s.createComment(context, context.Param("projectId"), "", "")
}

func (s *Server) CreateArtifactComment(context *gin.Context) {
	s.createComment(context, "", context.Param("artifactId"), "")
}

func (s *Server) CreateRevisionComment(context *gin.Context) {
	s.createComment(context, "", "", context.Param("revisionId"))
}

func (s *Server) createComment(context *gin.Context, projectID, artifactID, revisionID string) {
	if s.services.Comments == nil || s.services.Artifacts == nil {
		serviceUnavailable(context, "comment and artifact")
		return
	}
	var input createCommentRequest
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	if input.ArtifactID != "" {
		if artifactID != "" && artifactID != input.ArtifactID {
			problem.WriteError(context, core.ErrInvalidInput)
			return
		}
		artifactID = input.ArtifactID
	}
	if input.Target != nil {
		if artifactID == "" {
			artifactID = input.Target.ArtifactID
		}
		if revisionID == "" {
			revisionID = input.Target.RevisionID
		}
	}
	if input.RevisionID != nil {
		if revisionID != "" && revisionID != *input.RevisionID {
			problem.WriteError(context, core.ErrInvalidInput)
			return
		}
		revisionID = *input.RevisionID
	}
	if revisionID != "" {
		revision, artifact, resolved := s.revisionArtifact(context, revisionID, actor)
		if !resolved {
			return
		}
		if artifactID != "" && artifactID != revision.ArtifactID {
			problem.WriteError(context, core.ErrInvalidInput)
			return
		}
		if input.Target != nil && input.Target.ContentHash != "" && input.Target.ContentHash != revision.ContentHash {
			problem.WriteError(context, core.ErrConflict)
			return
		}
		artifactID = revision.ArtifactID
		if projectID != "" && projectID != artifact.Artifact.ProjectID {
			problem.WriteError(context, core.ErrNotFound)
			return
		}
		projectID = artifact.Artifact.ProjectID
	}
	if artifactID == "" {
		problem.WriteError(context, core.ErrInvalidInput)
		return
	}
	artifact, err := s.services.Artifacts.Get(context.Request.Context(), artifactID, actor, false)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if projectID != "" && projectID != artifact.Artifact.ProjectID {
		problem.WriteError(context, core.ErrNotFound)
		return
	}
	var revisionPointer *string
	if revisionID != "" {
		revisionPointer = &revisionID
	}
	created, err := s.services.Comments.Create(context.Request.Context(), artifactID, actor, core.CreateCommentInput{
		RevisionID: revisionPointer, ParentThreadID: input.ParentThreadID, ParentMessageID: input.ParentMessageID,
		Body: input.Body, Anchor: input.Anchor, Severity: input.Severity, AssignedTo: input.AssignedTo, Mentions: input.Mentions,
	})
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", created.ETag)
	context.Header("Location", "/v1/comments/"+created.ID)
	context.JSON(http.StatusCreated, created)
}

func (s *Server) ResolveComment(context *gin.Context) {
	if s.services.Comments == nil {
		serviceUnavailable(context, "comment")
		return
	}
	var input struct {
		Resolved *bool `json:"resolved"`
	}
	if err := DecodeJSON(context, &input, s.config.HTTP.MaxJSONBodyBytes); err != nil {
		WriteJSONError(context, err)
		return
	}
	if input.Resolved == nil {
		s.businessError(context, core.ErrInvalidInput)
		return
	}
	actor, ok := actorID(context)
	if !ok {
		return
	}
	current, err := s.services.Comments.Get(context.Request.Context(), context.Param("commentId"), actor)
	if err != nil {
		s.businessError(context, err)
		return
	}
	if !matchETag(context, current.ETag, "comment thread") {
		return
	}
	var updated core.CommentThread
	if conditional, ok := s.services.Comments.(ConditionalCommentService); ok {
		updated, err = conditional.SetResolvedIfMatch(context.Request.Context(), current.ID, actor, current.ETag, *input.Resolved)
	} else {
		updated, err = s.services.Comments.SetResolved(context.Request.Context(), current.ID, actor, *input.Resolved)
	}
	if err != nil {
		s.businessError(context, err)
		return
	}
	context.Header("ETag", updated.ETag)
	context.JSON(http.StatusOK, updated)
}

func (s *Server) revisionArtifact(context *gin.Context, revisionID, actor string) (core.ArtifactRevision, core.VersionedArtifact, bool) {
	revision, err := s.services.Artifacts.GetRevision(context.Request.Context(), revisionID, actor)
	if err != nil {
		s.businessError(context, err)
		return core.ArtifactRevision{}, core.VersionedArtifact{}, false
	}
	artifact, err := s.services.Artifacts.Get(context.Request.Context(), revision.ArtifactID, actor, false)
	if err != nil {
		s.businessError(context, err)
		return core.ArtifactRevision{}, core.VersionedArtifact{}, false
	}
	return revision, artifact, true
}

func filterComments(items []core.CommentThread, artifactID, revisionID string) []core.CommentThread {
	filtered := make([]core.CommentThread, 0, len(items))
	for _, item := range items {
		if artifactID != "" && item.ArtifactID != artifactID {
			continue
		}
		if revisionID != "" && (item.RevisionID == nil || *item.RevisionID != revisionID) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func reviewDTO(review core.ReviewRequest) core.ReviewRequest {
	return review
}
