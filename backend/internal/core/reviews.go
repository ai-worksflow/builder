package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/canonicalreviewreceipt"
	"github.com/worksflow/builder/backend/internal/storage/content"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ReviewPolicy struct {
	ReviewerIDs           []string       `json:"reviewerIds"`
	MinimumApprovals      int            `json:"minimumApprovals"`
	ProhibitSelfReview    bool           `json:"prohibitSelfReview"`
	GovernanceMode        GovernanceMode `json:"governanceMode"`
	SoloSelfReviewOwnerID string         `json:"soloSelfReviewOwnerId,omitempty"`
}

type ReviewDecision struct {
	ID             string    `json:"id"`
	ReviewerID     string    `json:"reviewerId"`
	Decision       string    `json:"decision"`
	Summary        string    `json:"summary"`
	SoloSelfReview bool      `json:"soloSelfReview,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

type ReviewRequest struct {
	ID                     string           `json:"id"`
	ProjectID              string           `json:"projectId"`
	ArtifactID             string           `json:"artifactId"`
	RevisionID             string           `json:"revisionId"`
	ContentHash            string           `json:"contentHash"`
	Status                 string           `json:"status"`
	Policy                 ReviewPolicy     `json:"policy"`
	ReviewAuthorityVersion int16            `json:"reviewAuthorityVersion"`
	AuthorityState         string           `json:"authorityState"`
	RequestedBy            string           `json:"requestedBy"`
	RequestedAt            time.Time        `json:"requestedAt"`
	ClosedAt               *time.Time       `json:"closedAt,omitempty"`
	Decisions              []ReviewDecision `json:"decisions"`
	ETag                   string           `json:"etag"`
}

type SubmitReviewInput struct {
	RevisionID        string   `json:"revisionId"`
	ReviewerIDs       []string `json:"reviewerIds"`
	MinimumApprovals  int      `json:"minimumApprovals,omitempty"`
	AllowSelfApproval bool     `json:"allowSelfApproval,omitempty"`
}

type DecideReviewInput struct {
	Decision            string `json:"decision"`
	Summary             string `json:"summary"`
	SoloReviewConfirmed bool   `json:"soloReviewConfirmed,omitempty"`
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
	actorUUID, err := uuid.Parse(actorID)
	if err != nil || actorUUID == uuid.Nil || actorUUID.String() != actorID {
		return ReviewRequest{}, fmt.Errorf("%w: actor id", ErrInvalidInput)
	}
	revisionUUID, err := uuid.Parse(input.RevisionID)
	if err != nil || revisionUUID == uuid.Nil || revisionUUID.String() != input.RevisionID {
		return ReviewRequest{}, fmt.Errorf("%w: revision id", ErrInvalidInput)
	}
	artifactUUID, projectUUID, err := s.authorizeArtifactInProject(ctx, projectID, artifactID, actorID, ActionEdit)
	if err != nil {
		return ReviewRequest{}, err
	}
	if input.MinimumApprovals <= 0 {
		input.MinimumApprovals = 1
	}
	if input.MinimumApprovals > 20 {
		return ReviewRequest{}, fmt.Errorf("%w: minimum approvals", ErrInvalidInput)
	}
	if len(input.ReviewerIDs) > 20 {
		return ReviewRequest{}, fmt.Errorf("%w: reviewer count", ErrInvalidInput)
	}
	reviewerUUIDs, reviewerIDs, err := parseReviewerIDs(input.ReviewerIDs)
	if err != nil {
		return ReviewRequest{}, err
	}
	if len(reviewerIDs) > 0 && input.MinimumApprovals > len(reviewerIDs) {
		return ReviewRequest{}, fmt.Errorf("%w: minimum approvals exceed assigned reviewers", ErrInvalidInput)
	}
	var policy ReviewPolicy
	now := s.now().UTC().Truncate(time.Millisecond)
	if !canonicalReviewTimeInDomain(now) {
		return ReviewRequest{}, fmt.Errorf("%w: review clock is outside the canonical authority range", ErrConflict)
	}
	requestModel := storage.ReviewRequestModel{
		ID: uuid.New(), ProjectID: projectUUID, ArtifactID: artifactUUID, RevisionID: revisionUUID,
		Status: "open", RequestedBy: actorUUID, RequestedAt: now, ReviewAuthorityVersion: 1,
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		governance, err := LockProjectGovernance(ctx, transaction, projectUUID)
		if err != nil {
			return err
		}
		if _, err := s.accessWithDatabase(transaction).Authorize(ctx, projectID, actorID, ActionEdit); err != nil {
			return err
		}
		if err := validateReviewerMembership(transaction, projectUUID, reviewerUUIDs); err != nil {
			return err
		}
		policy = ReviewPolicy{
			ReviewerIDs: reviewerIDs, MinimumApprovals: input.MinimumApprovals,
			ProhibitSelfReview: true, GovernanceMode: governance.Mode,
		}
		if input.AllowSelfApproval {
			_, authorizeErr := s.accessWithDatabase(transaction).Authorize(ctx, projectID, actorID, ActionEdit)
			if authorizeErr != nil {
				return authorizeErr
			}
			if governance.Mode != GovernanceModeSolo || governance.OwnerCount != 1 || governance.SoleOwnerID == "" ||
				!containsString(reviewerIDs, governance.SoleOwnerID) {
				return ErrForbidden
			}
		}
		var artifact storage.ArtifactModel
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", artifactUUID).Take(&artifact).Error; err != nil {
			return err
		}
		if artifact.ProjectID != projectUUID {
			return ErrNotFound
		}
		if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
			return err
		}
		if artifact.Lifecycle != "active" {
			return fmt.Errorf("%w: artifact lifecycle must be active", ErrBlockingGate)
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
		if revision.CreatedBy == uuid.Nil || !canonicalReviewDigest(revision.ContentHash) {
			return ErrConflict
		}
		if !canonicalReviewTimeInDomain(revision.CreatedAt) {
			return ErrConflict
		}
		if now.Before(revision.CreatedAt.UTC()) {
			now = canonicalReviewCeilingMillisecond(revision.CreatedAt)
			if !canonicalReviewTimeInDomain(now) {
				return ErrConflict
			}
			requestModel.RequestedAt = now
		}
		if input.AllowSelfApproval && revision.CreatedBy.String() == governance.SoleOwnerID {
			policy.SoloSelfReviewOwnerID = governance.SoleOwnerID
		}
		approvalCapacity, err := canonicalReviewApprovalCapacity(
			transaction, projectUUID, reviewerUUIDs, revision.CreatedBy, policy.SoloSelfReviewOwnerID,
		)
		if err != nil {
			return err
		}
		if input.MinimumApprovals > approvalCapacity {
			return fmt.Errorf("%w: minimum approvals exceed eligible non-author reviewers", ErrInvalidInput)
		}
		policyPayload, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		requestModel.Policy = policyPayload
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
		decisions, err := s.listDecisions(ctx, request.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, reviewForRead(request, decisions))
	}
	return result, nil
}

// Get returns a fail-closed read projection even for legacy or malformed
// authority. This keeps the exact decision endpoint reachable so its locked
// mutation path can durably stale and restart an unrecoverable open request.
func (s *ReviewService) Get(ctx context.Context, projectID, reviewID, actorID string) (ReviewRequest, error) {
	if _, err := s.access.Authorize(ctx, projectID, actorID, ActionView); err != nil {
		return ReviewRequest{}, err
	}
	projectUUID, err := uuid.Parse(projectID)
	if err != nil || projectUUID == uuid.Nil || projectUUID.String() != projectID {
		return ReviewRequest{}, fmt.Errorf("%w: project id", ErrInvalidInput)
	}
	reviewUUID, err := uuid.Parse(reviewID)
	if err != nil || reviewUUID == uuid.Nil || reviewUUID.String() != reviewID {
		return ReviewRequest{}, fmt.Errorf("%w: review id", ErrInvalidInput)
	}
	var request storage.ReviewRequestModel
	if err := s.database.WithContext(ctx).Where("id = ? AND project_id = ?", reviewUUID, projectUUID).
		Take(&request).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ReviewRequest{}, ErrNotFound
		}
		return ReviewRequest{}, err
	}
	decisions, err := s.listDecisions(ctx, request.ID)
	if err != nil {
		return ReviewRequest{}, err
	}
	return reviewForRead(request, decisions), nil
}

func (s *ReviewService) Decide(ctx context.Context, reviewID, actorID string, input DecideReviewInput) (ReviewRequest, error) {
	return s.DecideIfMatch(ctx, reviewID, actorID, "", input)
}

func (s *ReviewService) DecideIfMatch(ctx context.Context, reviewID, actorID, expectedETag string, input DecideReviewInput) (ReviewRequest, error) {
	reviewUUID, err := uuid.Parse(reviewID)
	if err != nil || reviewUUID == uuid.Nil || reviewUUID.String() != reviewID {
		return ReviewRequest{}, fmt.Errorf("%w: review id", ErrInvalidInput)
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil || actorUUID == uuid.Nil || actorUUID.String() != actorID {
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
	if len([]byte(input.Summary)) > 4096 {
		return ReviewRequest{}, fmt.Errorf("%w: review summary", ErrInvalidInput)
	}
	if input.Decision != "approve" && input.SoloReviewConfirmed {
		return ReviewRequest{}, fmt.Errorf("%w: solo review confirmation", ErrInvalidInput)
	}
	expectedETag = strings.TrimSpace(expectedETag)
	if len([]byte(expectedETag)) > 512 {
		return ReviewRequest{}, fmt.Errorf("%w: review precondition", ErrInvalidInput)
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	if !canonicalReviewTimeInDomain(now) {
		return ReviewRequest{}, fmt.Errorf("%w: review clock is outside the canonical authority range", ErrConflict)
	}
	var request storage.ReviewRequestModel
	var policy ReviewPolicy
	stale := false
	soloSelfReview := false
	var requestProject struct {
		ProjectID uuid.UUID `gorm:"column:project_id"`
	}
	if err := s.database.WithContext(ctx).Model(&storage.ReviewRequestModel{}).
		Select("project_id").Where("id = ?", reviewUUID).Take(&requestProject).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ReviewRequest{}, ErrNotFound
		}
		return ReviewRequest{}, err
	}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		governance, err := LockProjectGovernance(ctx, transaction, requestProject.ProjectID)
		if err != nil {
			return err
		}
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reviewUUID).Take(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if request.Status != "open" {
			return ErrConflict
		}
		var currentDecisions []storage.ReviewDecisionModel
		if err := transaction.Where("review_request_id = ?", request.ID).
			Order("created_at ASC, id ASC").Find(&currentDecisions).Error; err != nil {
			return err
		}
		currentETag := reviewEntityTag(request, currentDecisions)
		if expectedETag == "" {
			expectedETag = currentETag
		} else if currentETag != expectedETag {
			return ErrConflict
		}
		staleForAuthorityRestart := func(reason string) error {
			if err := staleOpenReviewForAuthorityRestart(transaction, &request, now); err != nil {
				return err
			}
			stale = true
			return enqueue(transaction, "review", reviewID, "review.stale", "worksflow.review.stale", map[string]any{
				"projectId": request.ProjectID.String(), "reviewId": reviewID, "reason": reason,
			})
		}
		role, err := s.accessWithDatabase(transaction).Authorize(ctx, request.ProjectID.String(), actorID, ActionReview)
		if err != nil {
			return err
		}
		if request.ReviewAuthorityVersion == 0 {
			return staleForAuthorityRestart("legacy_authority_version")
		}
		policy, err = decodeReviewPolicy(request.Policy)
		if err != nil {
			return staleForAuthorityRestart("invalid_authority_policy")
		}
		authorityDrifted, err := canonicalReviewAuthorityDrifted(transaction, request, policy, governance, currentDecisions)
		if err != nil {
			return err
		}
		if authorityDrifted {
			return staleForAuthorityRestart("authority_facts_drift")
		}
		var causalTimeOK bool
		now, causalTimeOK = canonicalReviewCausalDecisionTime(now, request.RequestedAt, currentDecisions)
		if !causalTimeOK {
			return fmt.Errorf("%w: review clock cannot advance within the canonical authority range", ErrConflict)
		}
		if len(policy.ReviewerIDs) > 0 && !containsString(policy.ReviewerIDs, actorID) {
			return ErrForbidden
		}
		var artifact storage.ArtifactModel
		var revision storage.ArtifactRevisionModel
		if input.Decision == "approve" {
			locks, err := lockArtifactApprovalSourceClosure(
				ctx, transaction, request.ProjectID, request.ArtifactID, request.RevisionID,
			)
			if err != nil {
				return err
			}
			var ok bool
			artifact, ok = locks.artifacts[request.ArtifactID]
			if !ok {
				return fmt.Errorf("%w: review artifact is missing from approval lock set", ErrBlockingGate)
			}
			revision, ok = locks.revisions[request.RevisionID]
			if !ok {
				return fmt.Errorf("%w: review revision is missing from approval lock set", ErrBlockingGate)
			}
		} else {
			if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.ArtifactID).Take(&artifact).Error; err != nil {
				return err
			}
			if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.RevisionID).Take(&revision).Error; err != nil {
				return err
			}
		}
		if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
			return err
		}
		if artifact.Lifecycle != "active" {
			return staleForAuthorityRestart("artifact_lifecycle_drift")
		}
		if revision.ContentHash != request.ContentHash || revision.WorkflowStatus != "in_review" {
			return staleForAuthorityRestart("revision_state_drift")
		}
		if artifact.LatestRevisionID == nil || *artifact.LatestRevisionID != revision.ID {
			return staleForAuthorityRestart("revision_pointer_drift")
		}
		if input.Decision == "approve" {
			if policy.ProhibitSelfReview && revision.CreatedBy == actorUUID {
				if policy.GovernanceMode != GovernanceModeSolo || policy.SoloSelfReviewOwnerID != actorID {
					return ErrSelfApproval
				}
				if err := RequireSoloSelfReview(governance, role, input.SoloReviewConfirmed, input.Summary); err != nil {
					return err
				}
				soloSelfReview = true
			}
			if input.SoloReviewConfirmed && !soloSelfReview {
				return fmt.Errorf("%w: solo review confirmation is only valid for the exact Solo Owner self-review", ErrInvalidInput)
			}
			if err := s.checkApprovalGates(ctx, transaction, artifact, revision); err != nil {
				return err
			}
		}
		roleValue := string(role)
		governanceMode := string(governance.Mode)
		ownerCount := int(governance.OwnerCount)
		confirmed := soloSelfReview && input.SoloReviewConfirmed
		preconditionETag := expectedETag
		var soleOwnerID *uuid.UUID
		if governance.SoleOwnerID != "" {
			parsedOwnerID, parseErr := uuid.Parse(governance.SoleOwnerID)
			if parseErr != nil {
				return fmt.Errorf("stored sole owner identity is invalid: %w", parseErr)
			}
			soleOwnerID = &parsedOwnerID
		}
		decision := storage.ReviewDecisionModel{
			ID: uuid.New(), ReviewRequestID: request.ID, ReviewerID: actorUUID,
			Decision: input.Decision, Summary: input.Summary,
			SoloSelfReview: soloSelfReview, CreatedAt: now, ReviewAuthorityVersion: 1,
			ReviewerRoleAtDecision: &roleValue, GovernanceModeAtDecision: &governanceMode,
			OwnerCountAtDecision: &ownerCount, SoleOwnerIDAtDecision: soleOwnerID,
			SoloReviewConfirmed: &confirmed, PreconditionETag: &preconditionETag,
		}
		createdDecision := transaction.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "review_request_id"}, {Name: "reviewer_id"}},
			DoNothing: true,
		}).Create(&decision)
		if createdDecision.Error != nil {
			return createdDecision.Error
		}
		if createdDecision.RowsAffected != 1 {
			return ErrConflict
		}
		if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("review_request_id = ? AND reviewer_id = ?", request.ID, actorUUID).
			Take(&decision).Error; err != nil {
			return err
		}
		if input.Decision == "request_changes" {
			request.Status = "changes_requested"
			request.ClosedAt = &now
			request.ClosedByDecisionID = &decision.ID
			if err := transaction.Model(&storage.ReviewRequestModel{}).Where("id = ?", request.ID).
				Updates(map[string]any{"status": request.Status, "closed_at": now, "closed_by_decision_id": decision.ID}).Error; err != nil {
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
				request.ClosedByDecisionID = &decision.ID
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
					Updates(map[string]any{"status": request.Status, "closed_at": now, "closed_by_decision_id": decision.ID}).Error; err != nil {
					return err
				}
				if _, err := issueCanonicalReviewReceipt(ctx, transaction, request.ID); err != nil {
					return err
				}
				if err := enforceCanonicalReviewReceiptConstraint(
					transaction, request.ProjectID, request.RevisionID, request.ID,
				); err != nil {
					return err
				}
			}
		}
		auditMetadata := map[string]any{
			"decision": input.Decision, "summary": input.Summary,
			"subjectAuthorId": revision.CreatedBy.String(),
		}
		if soloSelfReview {
			auditMetadata["soloSelfReview"] = true
			auditMetadata["governanceMode"] = GovernanceModeSolo
		}
		if err := insertAudit(transaction, request.ProjectID, actorUUID, "review.decided", "review_request", reviewID, auditMetadata); err != nil {
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
			"soloSelfReview": soloSelfReview, "governanceMode": policy.GovernanceMode,
		})
	})
	if err != nil {
		if reconciled, ok, reconcileErr := s.reconcileCanonicalReviewDecision(
			ctx, reviewUUID, actorUUID, expectedETag, input,
		); reconcileErr != nil {
			return ReviewRequest{}, errors.Join(err, reconcileErr)
		} else if ok {
			return reconciled, nil
		}
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

// reconcileCanonicalReviewDecision handles the narrow commit-unknown case. It
// never retries the mutation and never infers success from mutable review rows:
// the exact closing decision and the database-authored immutable receipt must
// both match the caller's original precondition and payload.
func (s *ReviewService) reconcileCanonicalReviewDecision(
	ctx context.Context,
	reviewID, actorID uuid.UUID,
	expectedETag string,
	input DecideReviewInput,
) (ReviewRequest, bool, error) {
	if input.Decision != "approve" || expectedETag == "" {
		return ReviewRequest{}, false, nil
	}
	var result ReviewRequest
	reconciled := false
	err := s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var request storage.ReviewRequestModel
		if err := transaction.Where("id = ?", reviewID).Take(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if request.ReviewAuthorityVersion != 1 || request.Status != "approved" ||
			request.ClosedAt == nil || request.ClosedByDecisionID == nil {
			return nil
		}
		if _, err := s.accessWithDatabase(transaction).Authorize(
			ctx, request.ProjectID.String(), actorID.String(), ActionView,
		); err != nil {
			return err
		}
		var decision storage.ReviewDecisionModel
		if err := transaction.Where(
			"review_request_id = ? AND reviewer_id = ?", request.ID, actorID,
		).Take(&decision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if decision.ReviewAuthorityVersion != 1 || decision.ID != *request.ClosedByDecisionID ||
			decision.Decision != "approve" || decision.Summary != input.Summary ||
			decision.PreconditionETag == nil || *decision.PreconditionETag != expectedETag ||
			decision.SoloReviewConfirmed == nil || *decision.SoloReviewConfirmed != input.SoloReviewConfirmed ||
			!decision.CreatedAt.Equal(*request.ClosedAt) {
			return nil
		}
		exact, err := canonicalReviewReceiptExists(
			transaction, request.ProjectID, request.RevisionID, request.ID,
		)
		if err != nil {
			return err
		}
		if !exact {
			return nil
		}
		policy, err := decodeReviewPolicy(request.Policy)
		if err != nil {
			return err
		}
		var models []storage.ReviewDecisionModel
		if err := transaction.Where("review_request_id = ?", request.ID).
			Order("created_at ASC, id ASC").Find(&models).Error; err != nil {
			return err
		}
		result = reviewFromModels(request, policy, reviewDecisionsFromModels(models))
		reconciled = true
		return nil
	})
	if err != nil {
		return ReviewRequest{}, false, fmt.Errorf("reconcile canonical review decision: %w", err)
	}
	return result, reconciled, nil
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
	if artifact.Kind == "blueprint" || artifact.Kind == "page_spec" || artifact.Kind == "prototype" {
		var sourceModels []storage.ArtifactRevisionSourceModel
		if err := transaction.Where("revision_id = ?", revision.ID).
			Order("ordinal ASC").Find(&sourceModels).Error; err != nil {
			return err
		}
		artifacts := &ArtifactService{contents: s.contents}
		if err := artifacts.validateArtifactLineageForReview(
			ctx,
			transaction,
			artifact.ProjectID,
			artifact.Kind,
			stored.Payload,
			sourceInputsFromRevisionModels(sourceModels),
		); err != nil {
			return err
		}
	}
	report := ValidateArtifactContent(artifact.Kind, stored.Payload)
	if !report.Valid {
		encoded, _ := json.Marshal(report.Findings)
		return fmt.Errorf("%w: validation findings %s", ErrBlockingGate, encoded)
	}
	return nil
}

func staleOpenReviewForAuthorityRestart(
	transaction *gorm.DB,
	request *storage.ReviewRequestModel,
	closedAt time.Time,
) error {
	var artifact storage.ArtifactModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND project_id = ?", request.ArtifactID, request.ProjectID).
		Take(&artifact).Error; err != nil {
		return err
	}
	if err := ensureGenericArtifactMutationAllowed(artifact.Kind); err != nil {
		return err
	}
	var revision storage.ArtifactRevisionModel
	if err := transaction.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND artifact_id = ?", request.RevisionID, request.ArtifactID).
		Take(&revision).Error; err != nil {
		return err
	}
	request.Status = "stale"
	request.ClosedAt = &closedAt
	request.ClosedByDecisionID = nil
	if err := transaction.Model(&storage.ReviewRequestModel{}).Where("id = ?", request.ID).
		Updates(map[string]any{"status": "stale", "closed_at": closedAt, "closed_by_decision_id": nil}).Error; err != nil {
		return err
	}
	if revision.WorkflowStatus == "in_review" {
		if err := transaction.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", revision.ID).
			Update("workflow_status", "changes_requested").Error; err != nil {
			return err
		}
	}
	return nil
}

func canonicalReviewAuthorityDrifted(
	transaction *gorm.DB,
	request storage.ReviewRequestModel,
	policy ReviewPolicy,
	governance ProjectGovernance,
	decisions []storage.ReviewDecisionModel,
) (bool, error) {
	if !policy.ProhibitSelfReview || policy.GovernanceMode != governance.Mode ||
		governance.OwnerCount < 1 || governance.OwnerCount > 1000000 {
		return true, nil
	}
	if request.ID == uuid.Nil || request.ProjectID == uuid.Nil || request.ArtifactID == uuid.Nil ||
		request.RevisionID == uuid.Nil || request.RequestedBy == uuid.Nil ||
		!canonicalReviewTimeInDomain(request.RequestedAt) {
		return true, nil
	}
	// DecideIfMatch only calls this helper for an open request. A threshold that
	// was already reached without atomically closing the request is an invalid
	// append-only dead end; it must be staled before a clean authority restart.
	if len(decisions) >= policy.MinimumApprovals || len(decisions) > 20 {
		return true, nil
	}
	if policy.SoloSelfReviewOwnerID != "" &&
		(governance.Mode != GovernanceModeSolo || governance.SoleOwnerID != policy.SoloSelfReviewOwnerID) {
		return true, nil
	}
	var revision storage.ArtifactRevisionModel
	if err := transaction.Select("id", "artifact_id", "content_hash", "created_by", "created_at").
		Where("id = ? AND artifact_id = ?", request.RevisionID, request.ArtifactID).
		Take(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		return false, err
	}
	if revision.ID == uuid.Nil || revision.ArtifactID == uuid.Nil || revision.CreatedBy == uuid.Nil ||
		!canonicalReviewDigest(request.ContentHash) || revision.ContentHash != request.ContentHash ||
		(policy.SoloSelfReviewOwnerID != "" && policy.SoloSelfReviewOwnerID != revision.CreatedBy.String()) ||
		!canonicalReviewTimeInDomain(revision.CreatedAt) ||
		request.RequestedAt.Before(revision.CreatedAt) {
		return true, nil
	}
	reviewerUUIDs := make([]uuid.UUID, 0, len(policy.ReviewerIDs))
	for _, reviewerID := range policy.ReviewerIDs {
		reviewerUUID, err := uuid.Parse(reviewerID)
		if err != nil || reviewerUUID == uuid.Nil || reviewerUUID.String() != reviewerID {
			return true, nil
		}
		var member storage.ProjectMemberModel
		err = transaction.Select("role").Where("project_id = ? AND user_id = ?", request.ProjectID, reviewerUUID).Take(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if role := Role(member.Role); role != RoleOwner && role != RoleAdmin && role != RoleEditor {
			return true, nil
		}
		reviewerUUIDs = append(reviewerUUIDs, reviewerUUID)
	}
	approvalCapacity, err := canonicalReviewApprovalCapacity(
		transaction, request.ProjectID, reviewerUUIDs, revision.CreatedBy, policy.SoloSelfReviewOwnerID,
	)
	if err != nil {
		return false, err
	}
	if policy.MinimumApprovals > approvalCapacity {
		return true, nil
	}
	seenDecisionIDs := map[uuid.UUID]struct{}{}
	seenReviewers := map[uuid.UUID]struct{}{}
	for index, decision := range decisions {
		if decision.ReviewAuthorityVersion != 1 || decision.ReviewerRoleAtDecision == nil ||
			decision.GovernanceModeAtDecision == nil || decision.OwnerCountAtDecision == nil ||
			decision.SoloReviewConfirmed == nil || decision.PreconditionETag == nil ||
			decision.ID == uuid.Nil || decision.ReviewerID == uuid.Nil || decision.Decision != "approve" ||
			!canonicalReviewTimeInDomain(decision.CreatedAt) ||
			decision.CreatedAt.Before(request.RequestedAt) || strings.TrimSpace(decision.Summary) != decision.Summary ||
			len([]byte(decision.Summary)) > 4096 ||
			*decision.GovernanceModeAtDecision != string(governance.Mode) ||
			int64(*decision.OwnerCountAtDecision) != governance.OwnerCount ||
			*decision.PreconditionETag != reviewEntityTag(request, decisions[:index]) {
			return true, nil
		}
		if _, duplicate := seenDecisionIDs[decision.ID]; duplicate {
			return true, nil
		}
		seenDecisionIDs[decision.ID] = struct{}{}
		if _, duplicate := seenReviewers[decision.ReviewerID]; duplicate {
			return true, nil
		}
		seenReviewers[decision.ReviewerID] = struct{}{}
		if len(policy.ReviewerIDs) > 0 && !containsString(policy.ReviewerIDs, decision.ReviewerID.String()) {
			return true, nil
		}
		if governance.SoleOwnerID == "" {
			if decision.SoleOwnerIDAtDecision != nil {
				return true, nil
			}
		} else if decision.SoleOwnerIDAtDecision == nil || decision.SoleOwnerIDAtDecision.String() != governance.SoleOwnerID {
			return true, nil
		}
		var member storage.ProjectMemberModel
		err := transaction.Select("role").Where(
			"project_id = ? AND user_id = ?", request.ProjectID, decision.ReviewerID,
		).Take(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if member.Role != *decision.ReviewerRoleAtDecision {
			return true, nil
		}
		if decision.SoloSelfReview {
			if decision.ReviewerID != revision.CreatedBy || *decision.ReviewerRoleAtDecision != string(RoleOwner) ||
				governance.Mode != GovernanceModeSolo || governance.OwnerCount != 1 ||
				governance.SoleOwnerID != decision.ReviewerID.String() || !*decision.SoloReviewConfirmed ||
				strings.TrimSpace(decision.Summary) == "" || policy.SoloSelfReviewOwnerID != decision.ReviewerID.String() {
				return true, nil
			}
		} else if *decision.SoloReviewConfirmed || decision.ReviewerID == revision.CreatedBy {
			return true, nil
		}
	}
	return false, nil
}

func parseReviewerIDs(values []string) ([]uuid.UUID, []string, error) {
	unique := map[uuid.UUID]struct{}{}
	parsed := make([]uuid.UUID, 0, len(values))
	canonical := make([]string, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			return nil, nil, fmt.Errorf("%w: reviewer id", ErrInvalidInput)
		}
		if _, duplicate := unique[id]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate reviewer id", ErrInvalidInput)
		}
		unique[id] = struct{}{}
		parsed = append(parsed, id)
		canonical = append(canonical, id.String())
	}
	return parsed, canonical, nil
}

func canonicalReviewApprovalCapacity(
	transaction *gorm.DB,
	projectID uuid.UUID,
	reviewerIDs []uuid.UUID,
	revisionAuthorID uuid.UUID,
	soloSelfReviewOwnerID string,
) (int, error) {
	if len(reviewerIDs) > 0 {
		capacity := 0
		for _, reviewerID := range reviewerIDs {
			if reviewerID != revisionAuthorID || soloSelfReviewOwnerID == reviewerID.String() {
				capacity++
			}
		}
		return capacity, nil
	}
	var capacity int64
	if err := transaction.Model(&storage.ProjectMemberModel{}).
		Where("project_id = ? AND user_id <> ? AND role IN ?", projectID, revisionAuthorID, []string{
			string(RoleOwner), string(RoleAdmin), string(RoleEditor),
		}).Count(&capacity).Error; err != nil {
		return 0, err
	}
	if soloSelfReviewOwnerID == revisionAuthorID.String() {
		capacity++
	}
	if capacity > 20 {
		capacity = 20
	}
	return int(capacity), nil
}

func validateReviewerMembership(transaction *gorm.DB, projectID uuid.UUID, reviewerIDs []uuid.UUID) error {
	for _, reviewerID := range reviewerIDs {
		var member storage.ProjectMemberModel
		if err := transaction.Where("project_id = ? AND user_id = ?", projectID, reviewerID).Take(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		role := Role(member.Role)
		if role != RoleOwner && role != RoleAdmin && role != RoleEditor {
			return ErrForbidden
		}
	}
	return nil
}

func (s *ReviewService) authorizeArtifactInProject(ctx context.Context, projectID, artifactID, actorID string, action Action) (uuid.UUID, uuid.UUID, error) {
	artifactUUID, projectUUID, err := (&ArtifactService{database: s.database, access: s.access}).authorizeArtifact(ctx, artifactID, actorID, action)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if artifactUUID == uuid.Nil || projectUUID == uuid.Nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: canonical artifact or project identity", ErrInvalidInput)
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
		Order("created_at ASC, id ASC").Find(&models).Error; err != nil {
		return nil, err
	}
	return reviewDecisionsFromModels(models), nil
}

func reviewDecisionsFromModels(models []storage.ReviewDecisionModel) []ReviewDecision {
	result := make([]ReviewDecision, 0, len(models))
	for _, model := range models {
		result = append(result, ReviewDecision{
			ID: model.ID.String(), ReviewerID: model.ReviewerID.String(), Decision: model.Decision,
			Summary: model.Summary, SoloSelfReview: model.SoloSelfReview, CreatedAt: model.CreatedAt,
		})
	}
	return result
}

func decodeReviewPolicy(value json.RawMessage) (ReviewPolicy, error) {
	var policy ReviewPolicy
	if err := canonicalreviewreceipt.StrictDecode(value, &policy); err != nil {
		return ReviewPolicy{}, fmt.Errorf("decode review policy: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := canonicalreviewreceipt.StrictDecode(value, &fields); err != nil {
		return ReviewPolicy{}, fmt.Errorf("decode review policy shape: %w", err)
	}
	if reviewerIDs, exists := fields["reviewerIds"]; !exists || len(reviewerIDs) == 0 || reviewerIDs[0] != '[' {
		return ReviewPolicy{}, errors.New("stored review policy reviewer set is not an array")
	}
	if governanceMode, exists := fields["governanceMode"]; !exists || len(governanceMode) < 2 || governanceMode[0] != '"' {
		return ReviewPolicy{}, errors.New("stored review policy governance mode is not a string")
	}
	if minimum, exists := fields["minimumApprovals"]; !exists || len(minimum) == 0 {
		return ReviewPolicy{}, errors.New("stored review policy minimum approval threshold is missing")
	}
	if prohibit, exists := fields["prohibitSelfReview"]; !exists || string(prohibit) != "true" {
		return ReviewPolicy{}, errors.New("stored review policy must prohibit self-review")
	}
	if soloOwner, exists := fields["soloSelfReviewOwnerId"]; exists && (len(soloOwner) < 2 || soloOwner[0] != '"') {
		return ReviewPolicy{}, errors.New("stored Solo Owner review policy is not a string")
	}
	if policy.MinimumApprovals < 1 || policy.MinimumApprovals > 20 || len(policy.ReviewerIDs) > 20 {
		return ReviewPolicy{}, errors.New("stored review policy is invalid")
	}
	if !ValidGovernanceMode(policy.GovernanceMode) {
		return ReviewPolicy{}, errors.New("stored review policy governance mode is invalid")
	}
	seen := map[string]struct{}{}
	for _, reviewerID := range policy.ReviewerIDs {
		parsed, err := uuid.Parse(reviewerID)
		if err != nil || parsed == uuid.Nil || parsed.String() != reviewerID {
			return ReviewPolicy{}, errors.New("stored review policy reviewer identity is invalid")
		}
		if _, duplicate := seen[reviewerID]; duplicate {
			return ReviewPolicy{}, errors.New("stored review policy contains duplicate reviewers")
		}
		seen[reviewerID] = struct{}{}
	}
	if len(policy.ReviewerIDs) > 0 && policy.MinimumApprovals > len(policy.ReviewerIDs) {
		return ReviewPolicy{}, errors.New("stored review policy threshold exceeds its reviewers")
	}
	if policy.GovernanceMode == GovernanceModeTeam && policy.SoloSelfReviewOwnerID != "" {
		return ReviewPolicy{}, errors.New("stored team review policy contains Solo Owner authority")
	}
	if policy.SoloSelfReviewOwnerID != "" {
		parsed, err := uuid.Parse(policy.SoloSelfReviewOwnerID)
		if err != nil || parsed == uuid.Nil || parsed.String() != policy.SoloSelfReviewOwnerID || !containsString(policy.ReviewerIDs, policy.SoloSelfReviewOwnerID) {
			return ReviewPolicy{}, errors.New("stored Solo Owner review policy is invalid")
		}
	}
	return policy, nil
}

func reviewFromModels(model storage.ReviewRequestModel, policy ReviewPolicy, decisions []ReviewDecision) ReviewRequest {
	if decisions == nil {
		decisions = []ReviewDecision{}
	}
	authorityState := "legacy"
	if model.ReviewAuthorityVersion == 1 {
		authorityState = "current"
	}
	return ReviewRequest{
		ID: model.ID.String(), ProjectID: model.ProjectID.String(), ArtifactID: model.ArtifactID.String(),
		RevisionID: model.RevisionID.String(), ContentHash: model.ContentHash, Status: model.Status,
		Policy: policy, ReviewAuthorityVersion: model.ReviewAuthorityVersion, AuthorityState: authorityState,
		RequestedBy: model.RequestedBy.String(), RequestedAt: model.RequestedAt,
		ClosedAt: model.ClosedAt, Decisions: decisions,
		ETag: reviewEntityTagFromDTO(model, decisions),
	}
}

func reviewForRead(model storage.ReviewRequestModel, decisions []ReviewDecision) ReviewRequest {
	policy, err := decodeReviewPolicy(model.Policy)
	if err != nil {
		policy = ReviewPolicy{
			ReviewerIDs: []string{}, MinimumApprovals: 1,
			ProhibitSelfReview: true, GovernanceMode: GovernanceModeTeam,
		}
	}
	review := reviewFromModels(model, policy, decisions)
	if model.ReviewAuthorityVersion != 1 {
		review.AuthorityState = "legacy"
	} else if err != nil {
		review.AuthorityState = "invalid"
	}
	return review
}

func reviewEntityTag(model storage.ReviewRequestModel, decisions []storage.ReviewDecisionModel) string {
	latest := int64(0)
	for _, decision := range decisions {
		if !canonicalReviewTimeInDomain(decision.CreatedAt) {
			continue
		}
		if value := decision.CreatedAt.UnixNano(); value > latest {
			latest = value
		}
	}
	return fmt.Sprintf(`"review:%s:%s:%d:%d"`, model.ID, model.Status, len(decisions), latest)
}

func reviewEntityTagFromDTO(model storage.ReviewRequestModel, decisions []ReviewDecision) string {
	latest := int64(0)
	for _, decision := range decisions {
		if !canonicalReviewTimeInDomain(decision.CreatedAt) {
			continue
		}
		if value := decision.CreatedAt.UnixNano(); value > latest {
			latest = value
		}
	}
	return fmt.Sprintf(`"review:%s:%s:%d:%d"`, model.ID, model.Status, len(decisions), latest)
}

func canonicalReviewTimeInDomain(value time.Time) bool {
	year := value.UTC().Year()
	return year >= 1678 && year < 2262
}

func canonicalReviewCausalDecisionTime(
	now time.Time,
	requestedAt time.Time,
	decisions []storage.ReviewDecisionModel,
) (time.Time, bool) {
	causal := now.UTC().Truncate(time.Millisecond)
	if !causal.After(requestedAt.UTC()) {
		causal = requestedAt.UTC().Add(time.Millisecond).Truncate(time.Millisecond)
	}
	if len(decisions) > 0 {
		latest := decisions[len(decisions)-1].CreatedAt.UTC()
		if !causal.After(latest) {
			causal = latest.Add(time.Millisecond).Truncate(time.Millisecond)
		}
	}
	return causal, canonicalReviewTimeInDomain(causal)
}

func canonicalReviewDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func canonicalReviewCeilingMillisecond(value time.Time) time.Time {
	canonical := value.UTC().Truncate(time.Millisecond)
	if canonical.Before(value.UTC()) {
		canonical = canonical.Add(time.Millisecond)
	}
	return canonical
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func CanonicalReviewApprovalDecision(policy ReviewPolicy, reviewerID, authorID string, soloSelfReview bool) bool {
	if len(policy.ReviewerIDs) > 0 && !containsString(policy.ReviewerIDs, reviewerID) {
		return false
	}
	if reviewerID != authorID {
		return true
	}
	return policy.ProhibitSelfReview && policy.GovernanceMode == GovernanceModeSolo &&
		policy.SoloSelfReviewOwnerID == reviewerID && soloSelfReview
}
