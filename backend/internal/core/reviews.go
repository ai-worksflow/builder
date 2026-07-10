package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ReviewPolicy struct {
	ReviewerIDs        []string `json:"reviewerIds"`
	MinimumApprovals   int      `json:"minimumApprovals"`
	ProhibitSelfReview bool     `json:"prohibitSelfReview"`
}

type ReviewDecision struct {
	ID         string    `json:"id"`
	ReviewerID string    `json:"reviewerId"`
	Decision   string    `json:"decision"`
	Summary    string    `json:"summary"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ReviewRequest struct {
	ID          string           `json:"id"`
	ProjectID   string           `json:"projectId"`
	ArtifactID  string           `json:"artifactId"`
	RevisionID  string           `json:"revisionId"`
	ContentHash string           `json:"contentHash"`
	Status      string           `json:"status"`
	Policy      ReviewPolicy     `json:"policy"`
	RequestedBy string           `json:"requestedBy"`
	RequestedAt time.Time        `json:"requestedAt"`
	ClosedAt    *time.Time       `json:"closedAt,omitempty"`
	Decisions   []ReviewDecision `json:"decisions"`
	ETag        string           `json:"etag"`
}

type SubmitReviewInput struct {
	RevisionID        string   `json:"revisionId"`
	ReviewerIDs       []string `json:"reviewerIds"`
	MinimumApprovals  int      `json:"minimumApprovals,omitempty"`
	AllowSelfApproval bool     `json:"allowSelfApproval,omitempty"`
}

type DecideReviewInput struct {
	Decision string `json:"decision"`
	Summary  string `json:"summary"`
}

type ReviewService struct {
	database *gorm.DB
	contents content.Store
	access   *AccessControl
	now      func() time.Time
}

func NewReviewService(database *gorm.DB, contents content.Store, access *AccessControl) (*ReviewService, error) {
	if database == nil || contents == nil || access == nil {
		return nil, errors.New("review database, content store and access control are required")
	}
	return &ReviewService{database: database, contents: contents, access: access, now: time.Now}, nil
}

func (s *ReviewService) Submit(ctx context.Context, projectID, artifactID, actorID string, input SubmitReviewInput) (ReviewRequest, error) {
	artifactUUID, projectUUID, err := s.authorizeArtifactInProject(ctx, projectID, artifactID, actorID, ActionEdit)
	if err != nil {
		return ReviewRequest{}, err
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return ReviewRequest{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	revisionUUID, err := uuid.Parse(input.RevisionID)
	if err != nil {
		return ReviewRequest{}, fmt.Errorf("%w: revision id", ErrInvalidInput)
	}
	if input.MinimumApprovals <= 0 {
		input.MinimumApprovals = 1
	}
	if input.MinimumApprovals > 20 {
		return ReviewRequest{}, fmt.Errorf("%w: minimum approvals", ErrInvalidInput)
	}
	reviewerUUIDs, reviewerIDs, err := s.validateReviewers(ctx, projectUUID, input.ReviewerIDs)
	if err != nil {
		return ReviewRequest{}, err
	}
	if len(reviewerIDs) > 0 && input.MinimumApprovals > len(reviewerIDs) {
		return ReviewRequest{}, fmt.Errorf("%w: minimum approvals exceed assigned reviewers", ErrInvalidInput)
	}
	// Self approval is a platform invariant. The input field is accepted only so
	// clients receive an explicit rejection rather than silently weakening policy.
	if input.AllowSelfApproval {
		return ReviewRequest{}, ErrForbidden
	}
	policy := ReviewPolicy{ReviewerIDs: reviewerIDs, MinimumApprovals: input.MinimumApprovals, ProhibitSelfReview: true}
	policyPayload, err := json.Marshal(policy)
	if err != nil {
		return ReviewRequest{}, err
	}
	now := s.now().UTC()
	requestModel := storage.ReviewRequestModel{
		ID: uuid.New(), ProjectID: projectUUID, ArtifactID: artifactUUID, RevisionID: revisionUUID,
		Status: "open", Policy: policyPayload, RequestedBy: actorUUID, RequestedAt: now,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var artifact storage.ArtifactModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", artifactUUID).Take(&artifact).Error; err != nil {
			return err
		}
		if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
			return err
		}
		var revision storage.ArtifactRevisionModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND artifact_id = ?", revisionUUID, artifactUUID).Take(&revision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if artifact.LatestRevisionID == nil || *artifact.LatestRevisionID != revisionUUID {
			return ErrConflict
		}
		if revision.WorkflowStatus != "draft" && revision.WorkflowStatus != "changes_requested" {
			return ErrConflict
		}
		requestModel.ContentHash = revision.ContentHash
		if err := transaction.Create(&requestModel).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", revisionUUID).
			Updates(map[string]any{"workflow_status": "in_review"}).Error; err != nil {
			return err
		}
		for _, reviewerID := range reviewerUUIDs {
			responsibility := storage.ArtifactResponsibilityModel{
				ArtifactID: artifactUUID, UserID: reviewerID, Responsibility: "reviewer",
				Reason: "Required reviewer for " + requestModel.ID.String(), AssignedBy: actorUUID, AssignedAt: now,
			}
			if err := transaction.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "artifact_id"}, {Name: "user_id"}, {Name: "responsibility"}},
				DoUpdates: clause.AssignmentColumns([]string{"reason", "assigned_by", "assigned_at"}),
			}).Create(&responsibility).Error; err != nil {
				return err
			}
			if reviewerID != actorUUID {
				if err := insertNotification(transaction, reviewerID, projectUUID, "review", "Review requested", "An artifact revision is waiting for your review.", "review_request", requestModel.ID.String()); err != nil {
					return err
				}
			}
		}
		if err := insertAudit(transaction, projectUUID, actorUUID, "review.submitted", "review_request", requestModel.ID.String(), map[string]any{"artifactId": artifactID, "revisionId": input.RevisionID}); err != nil {
			return err
		}
		return enqueue(transaction, "review", requestModel.ID.String(), "review.submitted", "worksflow.review.submitted", map[string]any{
			"projectId": projectID, "artifactId": artifactID, "revisionId": input.RevisionID, "reviewId": requestModel.ID.String(),
		})
	})
	if err != nil {
		return ReviewRequest{}, err
	}
	return reviewFromModels(requestModel, policy, nil), nil
}

func (s *ReviewService) List(ctx context.Context, projectID, actorID string) ([]ReviewRequest, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return nil, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	var requests []storage.ReviewRequestModel
	if err := s.database.WithContext(ctx).Where("project_id = ?", projectUUID).
		Order("requested_at DESC").Find(&requests).Error; err != nil {
		return nil, err
	}
	result := make([]ReviewRequest, 0, len(requests))
	for _, request := range requests {
		policy, err := decodeReviewPolicy(request.Policy)
		if err != nil {
			return nil, err
		}
		decisions, err := s.listDecisions(ctx, request.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, reviewFromModels(request, policy, decisions))
	}
	return result, nil
}

func (s *ReviewService) Decide(ctx context.Context, reviewID, actorID string, input DecideReviewInput) (ReviewRequest, error) {
	return s.DecideIfMatch(ctx, reviewID, actorID, "", input)
}

func (s *ReviewService) DecideIfMatch(ctx context.Context, reviewID, actorID, expectedETag string, input DecideReviewInput) (ReviewRequest, error) {
	reviewUUID, err := uuid.Parse(reviewID)
	if err != nil {
		return ReviewRequest{}, fmt.Errorf("%w: review id", ErrInvalidInput)
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil {
		return ReviewRequest{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	input.Decision = strings.TrimSpace(input.Decision)
	input.Summary = strings.TrimSpace(input.Summary)
	if input.Decision != "approve" && input.Decision != "request_changes" {
		return ReviewRequest{}, fmt.Errorf("%w: review decision", ErrInvalidInput)
	}
	if input.Decision == "request_changes" && input.Summary == "" {
		return ReviewRequest{}, fmt.Errorf("%w: change request summary", ErrInvalidInput)
	}
	now := s.now().UTC()
	var request storage.ReviewRequestModel
	var policy ReviewPolicy
	stale := false
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reviewUUID).Take(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if request.Status != "open" {
			return ErrConflict
		}
		if expectedETag != "" {
			var currentDecisions []storage.ReviewDecisionModel
			if err := transaction.Where("review_request_id = ?", request.ID).Order("created_at ASC").Find(&currentDecisions).Error; err != nil {
				return err
			}
			if reviewEntityTag(request, currentDecisions) != expectedETag {
				return ErrConflict
			}
		}
		if _, err := s.accessWithDatabase(transaction).Authorize(ctx, request.ProjectID.String(), actorID, ActionReview); err != nil {
			return err
		}
		policy, err = decodeReviewPolicy(request.Policy)
		if err != nil {
			return err
		}
		if len(policy.ReviewerIDs) > 0 && !containsString(policy.ReviewerIDs, actorID) {
			return ErrForbidden
		}
		var artifact storage.ArtifactModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.ArtifactID).Take(&artifact).Error; err != nil {
			return err
		}
		if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
			return err
		}
		var revision storage.ArtifactRevisionModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.RevisionID).Take(&revision).Error; err != nil {
			return err
		}
		if revision.ContentHash != request.ContentHash || revision.WorkflowStatus != "in_review" {
			request.Status = "stale"
			request.ClosedAt = &now
			if err := transaction.Model(&storage.ReviewRequestModel{}).Where("id = ?", request.ID).
				Updates(map[string]any{"status": "stale", "closed_at": now}).Error; err != nil {
				return err
			}
			stale = true
			return enqueue(transaction, "review", reviewID, "review.stale", "worksflow.review.stale", map[string]any{
				"projectId": request.ProjectID.String(), "reviewId": reviewID,
			})
		}
		if artifact.LatestRevisionID == nil || *artifact.LatestRevisionID != revision.ID {
			return ErrConflict
		}
		if input.Decision == "approve" {
			if policy.ProhibitSelfReview && revision.CreatedBy == actorUUID {
				return ErrSelfApproval
			}
			if err := s.checkApprovalGates(ctx, transaction, artifact, revision); err != nil {
				return err
			}
		}
		decision := storage.ReviewDecisionModel{
			ID: uuid.New(), ReviewRequestID: request.ID, ReviewerID: actorUUID,
			Decision: input.Decision, Summary: input.Summary, CreatedAt: now,
		}
		if err := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "review_request_id"}, {Name: "reviewer_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"decision", "summary", "created_at"}),
		}).Create(&decision).Error; err != nil {
			return err
		}
		if input.Decision == "request_changes" {
			request.Status = "changes_requested"
			request.ClosedAt = &now
			if err := transaction.Model(&storage.ReviewRequestModel{}).Where("id = ?", request.ID).
				Updates(map[string]any{"status": request.Status, "closed_at": now}).Error; err != nil {
				return err
			}
			if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", revision.ID).
				Update("workflow_status", "changes_requested").Error; err != nil {
				return err
			}
		} else {
			var approvals int64
			if err := transaction.Model(&storage.ReviewDecisionModel{}).
				Where("review_request_id = ? AND decision = 'approve'", request.ID).Count(&approvals).Error; err != nil {
				return err
			}
			if approvals >= int64(policy.MinimumApprovals) {
				request.Status = "approved"
				request.ClosedAt = &now
				if artifact.LatestApprovedRevisionID != nil && *artifact.LatestApprovedRevisionID != revision.ID {
					if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", *artifact.LatestApprovedRevisionID).
						Updates(map[string]any{"workflow_status": "superseded", "superseded_at": now}).Error; err != nil {
						return err
					}
				}
				if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", revision.ID).
					Updates(map[string]any{"workflow_status": "approved", "approved_at": now}).Error; err != nil {
					return err
				}
				if err := transaction.Model(&storage.ArtifactModel{}).Where("id = ?", artifact.ID).
					Updates(map[string]any{"latest_approved_revision_id": revision.ID, "version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
					return err
				}
				if err := transaction.Model(&storage.ReviewRequestModel{}).Where("id = ?", request.ID).
					Updates(map[string]any{"status": request.Status, "closed_at": now}).Error; err != nil {
					return err
				}
			}
		}
		if err := insertAudit(transaction, request.ProjectID, actorUUID, "review.decided", "review_request", reviewID, map[string]any{"decision": input.Decision}); err != nil {
			return err
		}
		if request.RequestedBy != actorUUID {
			if err := insertNotification(transaction, request.RequestedBy, request.ProjectID, "review", "Review decision recorded", "A reviewer selected "+input.Decision+".", "review_request", reviewID); err != nil {
				return err
			}
		}
		eventType := "review.decision_recorded"
		if request.Status == "approved" {
			eventType = "artifact.revision_approved"
		}
		return enqueue(transaction, "review", reviewID, eventType, "worksflow."+strings.ReplaceAll(eventType, "_", "."), map[string]any{
			"projectId": request.ProjectID.String(), "artifactId": request.ArtifactID.String(),
			"revisionId": request.RevisionID.String(), "reviewId": reviewID,
			"decision": input.Decision, "status": request.Status,
		})
	})
	if err != nil {
		return ReviewRequest{}, err
	}
	if stale {
		return ReviewRequest{}, ErrConflict
	}
	decisions, err := s.listDecisions(ctx, request.ID)
	if err != nil {
		return ReviewRequest{}, err
	}
	return reviewFromModels(request, policy, decisions), nil
}

func (s *ReviewService) checkApprovalGates(ctx context.Context, transaction *gorm.DB, artifact storage.ArtifactModel, revision storage.ArtifactRevisionModel) error {
	var blockers int64
	if err := transaction.Model(&storage.CommentThreadModel{}).
		Where("artifact_id = ? AND severity = 'blocking' AND resolved_at IS NULL AND (revision_id = ? OR revision_id IS NULL)", artifact.ID, revision.ID).
		Count(&blockers).Error; err != nil {
		return err
	}
	if blockers > 0 {
		return fmt.Errorf("%w: %d blocking comments remain", ErrBlockingGate, blockers)
	}
	var requiredDependencies, coveredDependencies int64
	if err := transaction.Raw(`
SELECT
  count(*) AS required_dependencies,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1
    FROM trace_links AS traces
    WHERE traces.project_id = dependencies.project_id
      AND traces.source_artifact_id = dependencies.source_artifact_id
      AND traces.source_revision_id = dependencies.source_revision_id
      AND traces.target_artifact_id = dependencies.target_artifact_id
      AND traces.target_revision_id = dependencies.target_revision_id
      AND traces.relation = dependencies.relation
  )) AS covered_dependencies
FROM artifact_dependencies AS dependencies
WHERE dependencies.project_id = ?
  AND dependencies.required = true
  AND (
    (dependencies.source_artifact_id = ? AND dependencies.source_revision_id = ?)
    OR (dependencies.target_artifact_id = ? AND dependencies.target_revision_id = ?)
  )
`, artifact.ProjectID, artifact.ID, revision.ID, artifact.ID, revision.ID).
		Row().Scan(&requiredDependencies, &coveredDependencies); err != nil {
		return fmt.Errorf("compute review trace coverage: %w", err)
	}
	if coveredDependencies != requiredDependencies {
		return fmt.Errorf("%w: only %d of %d required dependencies have exact trace links", ErrBlockingGate, coveredDependencies, requiredDependencies)
	}
	if artifact.Kind != "project_brief" && artifact.Kind != "product_requirements" && artifact.Kind != "requirement_baseline" {
		var health storage.ArtifactHealthModel
		if err := transaction.Where("artifact_id = ?", artifact.ID).Take(&health).Error; err == nil && health.SyncStatus != "current" {
			return fmt.Errorf("%w: artifact is %s", ErrBlockingGate, health.SyncStatus)
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	stored, err := s.contents.Get(ctx, revision.ContentRef, revision.ContentHash)
	if err != nil {
		return err
	}
	report := ValidateArtifactContent(artifact.Kind, stored.Payload)
	if !report.Valid {
		encoded, _ := json.Marshal(report.Findings)
		return fmt.Errorf("%w: validation findings %s", ErrBlockingGate, encoded)
	}
	return nil
}

func (s *ReviewService) validateReviewers(ctx context.Context, projectID uuid.UUID, values []string) ([]uuid.UUID, []string, error) {
	unique := map[uuid.UUID]struct{}{}
	parsed := make([]uuid.UUID, 0, len(values))
	canonical := make([]string, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: reviewer id", ErrInvalidInput)
		}
		if _, duplicate := unique[id]; duplicate {
			continue
		}
		unique[id] = struct{}{}
		var member storage.ProjectMemberModel
		if err := s.database.WithContext(ctx).Where("project_id = ? AND user_id = ?", projectID, id).Take(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, ErrNotFound
			}
			return nil, nil, err
		}
		role := Role(member.Role)
		if role != RoleOwner && role != RoleAdmin && role != RoleEditor {
			return nil, nil, ErrForbidden
		}
		parsed = append(parsed, id)
		canonical = append(canonical, id.String())
	}
	return parsed, canonical, nil
}

func (s *ReviewService) authorizeArtifactInProject(ctx context.Context, projectID, artifactID, actorID string, action Action) (uuid.UUID, uuid.UUID, error) {
	artifactUUID, projectUUID, err := (&ArtifactService{database: s.database, access: s.access}).authorizeArtifact(ctx, artifactID, actorID, action)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if projectUUID.String() != projectID {
		return uuid.Nil, uuid.Nil, ErrNotFound
	}
	return artifactUUID, projectUUID, nil
}

func (s *ReviewService) accessWithDatabase(database *gorm.DB) *AccessControl {
	return &AccessControl{database: database}
}

func (s *ReviewService) listDecisions(ctx context.Context, requestID uuid.UUID) ([]ReviewDecision, error) {
	var models []storage.ReviewDecisionModel
	if err := s.database.WithContext(ctx).Where("review_request_id = ?", requestID).
		Order("created_at ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]ReviewDecision, 0, len(models))
	for _, model := range models {
		result = append(result, ReviewDecision{
			ID: model.ID.String(), ReviewerID: model.ReviewerID.String(), Decision: model.Decision,
			Summary: model.Summary, CreatedAt: model.CreatedAt,
		})
	}
	return result, nil
}

func decodeReviewPolicy(value json.RawMessage) (ReviewPolicy, error) {
	var policy ReviewPolicy
	if err := json.Unmarshal(value, &policy); err != nil {
		return ReviewPolicy{}, fmt.Errorf("decode review policy: %w", err)
	}
	if policy.MinimumApprovals < 1 {
		return ReviewPolicy{}, errors.New("stored review policy is invalid")
	}
	return policy, nil
}

func reviewFromModels(model storage.ReviewRequestModel, policy ReviewPolicy, decisions []ReviewDecision) ReviewRequest {
	if decisions == nil {
		decisions = []ReviewDecision{}
	}
	return ReviewRequest{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), ArtifactID: model.ArtifactID.String(),
		RevisionID: model.RevisionID.String(), ContentHash: model.ContentHash, Status: model.Status,
		Policy: policy, RequestedBy: model.RequestedBy.String(), RequestedAt: model.RequestedAt,
		ClosedAt: model.ClosedAt, Decisions: decisions,
		ETag: reviewEntityTagFromDTO(model, decisions),
	}
}

func reviewEntityTag(model storage.ReviewRequestModel, decisions []storage.ReviewDecisionModel) string {
	latest := int64(0)
	for _, decision := range decisions {
		if value := decision.CreatedAt.UnixNano(); value > latest {
			latest = value
		}
	}
	return fmt.Sprintf(`"review:%s:%s:%d:%d"`, model.ID, model.Status, len(decisions), latest)
}

func reviewEntityTagFromDTO(model storage.ReviewRequestModel, decisions []ReviewDecision) string {
	latest := int64(0)
	for _, decision := range decisions {
		if value := decision.CreatedAt.UnixNano(); value > latest {
			latest = value
		}
	}
	return fmt.Sprintf(`"review:%s:%s:%d:%d"`, model.ID, model.Status, len(decisions), latest)
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
