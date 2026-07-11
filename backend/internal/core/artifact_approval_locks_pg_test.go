package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

type approvalHealthLockContextKey struct{}

func TestApprovalLocksEntireTransitiveSourceClosurePostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	now := time.Now().UTC()
	reviewerID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: reviewerID, Email: "source-lock-reviewer-" + uuid.NewString() + "@example.com",
		DisplayName: "Source Lock Reviewer", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: reviewerID, Role: "editor", JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	deepestSource := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"deepest source"}`),
	)
	directSource := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"direct source"}`),
	)
	seedArtifactLineageRevisionSource(t, database, directSource, deepestSource, "upstream", true, nil, ownerID)
	target := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{
			"summary":"Lock the full approval source closure.",
			"blocks":[{"type":"requirement","requirementId":"REQ-LOCK","priority":"should"}]
		}`),
	)
	seedArtifactLineageRevisionSource(t, database, target, directSource, "evidence", true, nil, ownerID)
	unrelated := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"unrelated"}`),
	)

	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := NewReviewService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	review, err := reviews.Submit(ctx, projectID.String(), target.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: target.RevisionID, ReviewerIDs: []string{reviewerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	marker := &struct{}{}
	sourcesLocked := make(chan struct{})
	releaseApproval := make(chan struct{})
	var pauseOnce sync.Once
	callbackName := "test:pause_after_approval_source_health_locks"
	if err := database.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Context.Value(approvalHealthLockContextKey{}) != marker ||
			query.Statement.Table != (storage.ArtifactHealthModel{}).TableName() {
			return
		}
		if _, locked := query.Statement.Clauses["FOR"]; !locked || query.Error != nil {
			return
		}
		pauseOnce.Do(func() {
			close(sourcesLocked)
			<-releaseApproval
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = database.Callback().Query().Remove(callbackName)
	}()
	released := false
	defer func() {
		if !released {
			close(releaseApproval)
		}
	}()

	type approvalResult struct {
		review ReviewRequest
		err    error
	}
	result := make(chan approvalResult, 1)
	approvalContext := context.WithValue(ctx, approvalHealthLockContextKey{}, marker)
	go func() {
		approved, decideErr := reviews.Decide(
			approvalContext, review.ID, reviewerID.String(), DecideReviewInput{Decision: "approve"},
		)
		result <- approvalResult{review: approved, err: decideErr}
	}()

	select {
	case <-sourcesLocked:
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not reach the locked source-closure checkpoint")
	}

	deepestArtifactID := uuid.MustParse(deepestSource.ArtifactID)
	deepestRevisionID := uuid.MustParse(deepestSource.RevisionID)
	assertApprovalClosureRowLocked(t, database,
		`UPDATE artifacts SET updated_at = updated_at WHERE id = ?`, deepestArtifactID, "transitive artifact")
	assertApprovalClosureRowLocked(t, database,
		`UPDATE artifact_revisions SET change_summary = change_summary WHERE id = ?`, deepestRevisionID, "transitive revision")
	assertApprovalClosureRowLocked(t, database,
		`UPDATE artifact_health SET computed_at = computed_at WHERE artifact_id = ?`, deepestArtifactID, "transitive health")

	if err := database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL lock_timeout = '250ms'`).Error; err != nil {
			return err
		}
		return transaction.Exec(
			`UPDATE artifact_health SET computed_at = computed_at WHERE artifact_id = ?`, uuid.MustParse(unrelated.ArtifactID),
		).Error
	}); err != nil {
		t.Fatalf("approval locked an unrelated health row: %v", err)
	}

	close(releaseApproval)
	released = true
	select {
	case outcome := <-result:
		if outcome.err != nil {
			t.Fatalf("approve after releasing source locks: %v", outcome.err)
		}
		if outcome.review.Status != "approved" {
			t.Fatalf("review status = %q, want approved", outcome.review.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not complete after source locks were released")
	}

	if err := database.Exec(
		`UPDATE artifact_health SET sync_status = 'needs_sync' WHERE artifact_id = ?`, deepestArtifactID,
	).Error; err != nil {
		t.Fatalf("transitive source health remained locked after approval commit: %v", err)
	}
}

func assertApprovalClosureRowLocked(t *testing.T, database *gorm.DB, statement string, id uuid.UUID, label string) {
	t.Helper()
	err := database.Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Exec(`SET LOCAL lock_timeout = '250ms'`).Error; err != nil {
			return err
		}
		return transaction.Exec(statement, id).Error
	})
	if err == nil {
		t.Fatalf("%s was writable while approval was open", label)
	}
	if message := strings.ToLower(err.Error()); !strings.Contains(message, "55p03") && !strings.Contains(message, "lock timeout") {
		t.Fatalf("%s update failed for an unexpected reason: %v", label, err)
	}
}
