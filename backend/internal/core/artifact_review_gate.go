package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type ReviewGateCheck struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	SourceID string `json:"sourceId,omitempty"`
}

type ArtifactReviewGate struct {
	Passed                       bool              `json:"passed"`
	Checks                       []ReviewGateCheck `json:"checks"`
	UnresolvedBlockingCommentIDs []string          `json:"unresolvedBlockingCommentIds"`
	TraceCoverage                float64           `json:"traceCoverage"`
}

type artifactReviewGateEvidence struct {
	ArtifactID             string
	ArtifactKind           string
	ArtifactLifecycle      string
	RevisionID             string
	RevisionNumber         uint64
	RevisionStatus         string
	RevisionContentRef     string
	RevisionContentHash    string
	RevisionCreatedBy      uuid.UUID
	LatestApprovedRevision *uuid.UUID
	DraftContentHash       string
	HealthKnown            bool
	SyncStatus             string
	RequiredDependencies   int64
	CoveredDependencies    int64
	BlockingCommentIDs     []string
	ReviewApproved         bool
	ReviewStatus           string
	ReviewID               string
	ReviewApprovals        int
	ReviewRequired         int
	ContentLoaded          bool
	ContentFindings        []ValidationFinding
}

// ReviewGate evaluates the latest immutable artifact revision. It is a
// read-only snapshot: unresolved blockers, dependency trace coverage, sync
// health and canonical review evidence are recomputed on every request.
func (s *ArtifactService) ReviewGate(ctx context.Context, artifactID, actorID string) (ArtifactReviewGate, error) {
	artifactUUID, projectUUID, err := s.authorizeArtifact(ctx, artifactID, actorID, ActionView)
	if err != nil {
		return ArtifactReviewGate{}, err
	}
	evidence := artifactReviewGateEvidence{ArtifactID: artifactID}
	err = s.database.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		var artifact storage.ArtifactModel
		if err := transaction.Where("id = ? AND project_id = ?", artifactUUID, projectUUID).Take(&artifact).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		evidence.ArtifactKind = artifact.Kind
		evidence.ArtifactLifecycle = artifact.Lifecycle
		evidence.LatestApprovedRevision = artifact.LatestApprovedRevisionID
		if artifact.LatestDraftID != nil {
			var draft storage.ArtifactDraftModel
			err := transaction.Select("content_hash").Where("id = ? AND artifact_id = ?", *artifact.LatestDraftID, artifact.ID).Take(&draft).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				evidence.DraftContentHash = draft.ContentHash
			}
		}
		if artifact.LatestRevisionID == nil {
			return s.loadReviewGateComments(transaction, artifact, nil, &evidence)
		}
		var revision storage.ArtifactRevisionModel
		if err := transaction.Where("id = ? AND artifact_id = ?", *artifact.LatestRevisionID, artifact.ID).Take(&revision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		evidence.RevisionID = revision.ID.String()
		evidence.RevisionNumber = revision.RevisionNumber
		evidence.RevisionStatus = revision.WorkflowStatus
		evidence.RevisionContentRef = revision.ContentRef
		evidence.RevisionContentHash = revision.ContentHash
		evidence.RevisionCreatedBy = revision.CreatedBy
		if err := s.loadReviewGateComments(transaction, artifact, &revision.ID, &evidence); err != nil {
			return err
		}
		var health storage.ArtifactHealthModel
		err := transaction.Where("artifact_id = ?", artifact.ID).Take(&health).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			evidence.HealthKnown = true
			evidence.SyncStatus = health.SyncStatus
		}
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
`, projectUUID, artifact.ID, revision.ID, artifact.ID, revision.ID).
			Row().Scan(&evidence.RequiredDependencies, &evidence.CoveredDependencies); err != nil {
			return fmt.Errorf("compute artifact trace coverage: %w", err)
		}
		return s.loadReviewGateApproval(transaction, artifact, revision, &evidence)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ArtifactReviewGate{}, err
	}
	if evidence.RevisionID != "" {
		stored, contentErr := s.contents.Get(ctx, evidence.RevisionContentRef, evidence.RevisionContentHash)
		if contentErr == nil {
			evidence.ContentLoaded = true
			evidence.ContentFindings = ValidateArtifactContent(evidence.ArtifactKind, stored.Payload).Findings
		}
	}
	return evaluateArtifactReviewGate(evidence), nil
}

func (s *ArtifactService) loadReviewGateComments(transaction *gorm.DB, artifact storage.ArtifactModel, revisionID *uuid.UUID, evidence *artifactReviewGateEvidence) error {
	query := transaction.Model(&storage.CommentThreadModel{}).
		Select("id").
		Where("artifact_id = ? AND severity = 'blocking' AND resolved_at IS NULL AND outdated_at IS NULL", artifact.ID)
	if revisionID == nil {
		query = query.Where("revision_id IS NULL")
	} else {
		query = query.Where("revision_id = ? OR revision_id IS NULL", *revisionID)
	}
	var ids []uuid.UUID
	if err := query.Order("created_at ASC, id ASC").Scan(&ids).Error; err != nil {
		return fmt.Errorf("load artifact review blockers: %w", err)
	}
	evidence.BlockingCommentIDs = make([]string, 0, len(ids))
	for _, id := range ids {
		evidence.BlockingCommentIDs = append(evidence.BlockingCommentIDs, id.String())
	}
	return nil
}

func (s *ArtifactService) loadReviewGateApproval(transaction *gorm.DB, artifact storage.ArtifactModel, revision storage.ArtifactRevisionModel, evidence *artifactReviewGateEvidence) error {
	var requests []storage.ReviewRequestModel
	if err := transaction.Where(
		"project_id = ? AND artifact_id = ? AND revision_id = ? AND content_hash = ?",
		artifact.ProjectID, artifact.ID, revision.ID, revision.ContentHash,
	).Order("requested_at DESC, id DESC").Find(&requests).Error; err != nil {
		return fmt.Errorf("load artifact review evidence: %w", err)
	}
	if len(requests) > 0 {
		evidence.ReviewStatus = requests[0].Status
		evidence.ReviewID = requests[0].ID.String()
	}
	canonicalRevision := revision.WorkflowStatus == "approved" && revision.ApprovedAt != nil && artifact.LatestApprovedRevisionID != nil && *artifact.LatestApprovedRevisionID == revision.ID
	if !canonicalRevision {
		return nil
	}
	for _, request := range requests {
		if request.Status != "approved" || request.ClosedAt == nil {
			continue
		}
		policy, err := decodeReviewPolicy(request.Policy)
		if err != nil {
			continue
		}
		// Self-approval is a platform invariant, not merely a UI preference.
		// Historical or manually inserted policies that weaken it are not
		// canonical approval evidence.
		if !policy.ProhibitSelfReview {
			continue
		}
		var decisions []storage.ReviewDecisionModel
		if err := transaction.Where("review_request_id = ? AND decision = 'approve'", request.ID).
			Order("created_at ASC, id ASC").Find(&decisions).Error; err != nil {
			return fmt.Errorf("load artifact review approvals: %w", err)
		}
		assigned := make(map[string]struct{}, len(policy.ReviewerIDs))
		for _, reviewerID := range policy.ReviewerIDs {
			assigned[reviewerID] = struct{}{}
		}
		approvals := 0
		for _, decision := range decisions {
			if policy.ProhibitSelfReview && decision.ReviewerID == revision.CreatedBy {
				continue
			}
			if len(assigned) > 0 {
				if _, ok := assigned[decision.ReviewerID.String()]; !ok {
					continue
				}
			}
			approvals++
		}
		if approvals >= policy.MinimumApprovals {
			evidence.ReviewApproved = true
			evidence.ReviewStatus = request.Status
			evidence.ReviewID = request.ID.String()
			evidence.ReviewApprovals = approvals
			evidence.ReviewRequired = policy.MinimumApprovals
			return nil
		}
		if evidence.ReviewRequired == 0 || approvals > evidence.ReviewApprovals {
			evidence.ReviewApprovals = approvals
			evidence.ReviewRequired = policy.MinimumApprovals
		}
	}
	return nil
}

func evaluateArtifactReviewGate(evidence artifactReviewGateEvidence) ArtifactReviewGate {
	checks := make([]ReviewGateCheck, 0, 10+len(evidence.ContentFindings))
	appendCheck := func(passed bool, code, message, path, sourceID string) {
		severity := "error"
		if passed {
			severity = "info"
		}
		checks = append(checks, ReviewGateCheck{Code: code, Severity: severity, Message: message, Path: path, SourceID: sourceID})
	}
	appendCheck(
		evidence.ArtifactLifecycle == "active",
		"artifact_active",
		mapGateMessage(evidence.ArtifactLifecycle == "active", "Artifact is active.", "Archived artifacts cannot pass review."),
		"lifecycle", evidence.ArtifactID,
	)
	hasRevision := evidence.RevisionID != ""
	appendCheck(
		hasRevision,
		"latest_revision_present",
		mapGateMessage(hasRevision, fmt.Sprintf("Latest immutable revision %d is available.", evidence.RevisionNumber), "Create an immutable artifact revision before review."),
		"latestRevisionId", evidence.RevisionID,
	)
	if hasRevision {
		current := evidence.DraftContentHash == "" || evidence.DraftContentHash == evidence.RevisionContentHash
		appendCheck(current, "draft_matches_latest_revision", mapGateMessage(current, "The working draft matches the latest revision.", "The working draft has unrevisioned changes."), "activeDraftId", evidence.RevisionID)
	}
	if !hasRevision || !evidence.ContentLoaded {
		appendCheck(false, "artifact_content_available", "The latest revision content is unavailable or failed its integrity check.", "content", evidence.RevisionID)
	} else if len(evidence.ContentFindings) == 0 {
		appendCheck(true, "artifact_content_valid", "Artifact content satisfies its structural validation rules.", "content", evidence.RevisionID)
	} else {
		for _, finding := range evidence.ContentFindings {
			passed := finding.Severity != "blocker"
			appendCheck(passed, finding.Code, finding.Message, finding.Path, evidence.RevisionID)
		}
	}
	commentsResolved := len(evidence.BlockingCommentIDs) == 0
	appendCheck(
		commentsResolved,
		"blocking_comments_resolved",
		mapGateMessage(commentsResolved, "No unresolved blocking comment threads apply to this revision.", fmt.Sprintf("Resolve %d blocking comment thread(s).", len(evidence.BlockingCommentIDs))),
		"comments", evidence.ArtifactID,
	)
	requiresHealth := evidence.ArtifactKind != "project_brief" && evidence.ArtifactKind != "product_requirements" && evidence.ArtifactKind != "requirement_baseline"
	healthPassed := !requiresHealth || evidence.HealthKnown && evidence.SyncStatus == "current"
	healthMessage := "This artifact kind does not require downstream sync health for approval."
	if requiresHealth && healthPassed {
		healthMessage = "Artifact dependency health is current."
	} else if requiresHealth && !evidence.HealthKnown {
		healthMessage = "Artifact dependency health has not been computed."
	} else if requiresHealth {
		healthMessage = "Artifact dependency health is " + evidence.SyncStatus + "."
	}
	appendCheck(healthPassed, "artifact_sync_current", healthMessage, "syncStatus", evidence.ArtifactID)
	coverage := 0.0
	if hasRevision && evidence.RequiredDependencies == 0 {
		coverage = 1
	} else if evidence.RequiredDependencies > 0 {
		coverage = float64(evidence.CoveredDependencies) / float64(evidence.RequiredDependencies)
	}
	tracePassed := hasRevision && evidence.CoveredDependencies == evidence.RequiredDependencies
	appendCheck(
		tracePassed,
		"required_trace_coverage",
		mapGateMessage(tracePassed, fmt.Sprintf("All %d required dependencies have exact trace links.", evidence.RequiredDependencies), fmt.Sprintf("Only %d of %d required dependencies have exact trace links.", evidence.CoveredDependencies, evidence.RequiredDependencies)),
		"trace", evidence.RevisionID,
	)
	reviewMessage := "The latest exact revision has a canonical approved review."
	if !evidence.ReviewApproved {
		reviewMessage = "The latest exact revision does not have a canonical approved review."
		if evidence.ReviewStatus == "approved" {
			if evidence.ReviewRequired < 1 {
				reviewMessage = "An approved review record exists, but its canonical review policy is invalid."
			} else {
				reviewMessage = fmt.Sprintf("An approved review record exists, but only %d of %d required canonical approvals are valid.", evidence.ReviewApprovals, evidence.ReviewRequired)
			}
		} else if evidence.ReviewStatus != "" {
			reviewMessage = "The latest exact revision review is " + evidence.ReviewStatus + "."
		}
	}
	appendCheck(evidence.ReviewApproved, "canonical_review_approved", reviewMessage, "review", evidence.ReviewID)
	passed := true
	for _, check := range checks {
		if check.Severity == "error" {
			passed = false
			break
		}
	}
	blockingIDs := append([]string{}, evidence.BlockingCommentIDs...)
	sort.Strings(blockingIDs)
	return ArtifactReviewGate{
		Passed: passed, Checks: checks,
		UnresolvedBlockingCommentIDs: blockingIDs, TraceCoverage: coverage,
	}
}

func mapGateMessage(condition bool, passed, failed string) string {
	if condition {
		return passed
	}
	return failed
}
