package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestProjectGovernanceModeChangeRejectsActiveWorkflowRunWithoutSideEffectsPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	_, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	projects := &ProjectService{database: database, access: access, now: time.Now}
	project, err := projects.Get(ctx, projectID.String(), ownerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if project.GovernanceMode != GovernanceModeTeam {
		t.Fatalf("initial governance mode = %q, want team", project.GovernanceMode)
	}

	now := time.Now().UTC()
	definitionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionModel{
		ID: definitionID, ProjectID: &projectID, WorkflowKey: "governance-mode-" + uuid.NewString(),
		Title: "Governance mode guard", Lifecycle: "active", CreatedBy: ownerID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	definitionVersionID := uuid.New()
	if err := database.Create(&storage.WorkflowDefinitionVersionModel{
		ID: definitionVersionID, DefinitionID: definitionID, Version: 1, SchemaVersion: 1,
		Content: json.RawMessage(`{"nodes":[],"edges":[]}`), ContentHash: "governance-mode-guard",
		ExecutionProfileVersion: legacyWorkflowExecutionProfileVersion,
		ExecutionProfileHash:    legacyWorkflowExecutionProfileHash,
		ValidationReport:        json.RawMessage(`{}`), Published: true,
		CreatedBy: ownerID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.WorkflowRunModel{
		ID: uuid.New(), ProjectID: projectID, DefinitionVersionID: definitionVersionID,
		ExecutionProfileVersion: legacyWorkflowExecutionProfileVersion,
		ExecutionProfileHash:    legacyWorkflowExecutionProfileHash,
		Status:                  "running", GovernanceMode: string(GovernanceModeTeam),
		Scope: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
		StartedBy: ownerID, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var auditCountBefore int64
	if err := database.Model(&storage.AuditEventModel{}).
		Where("project_id = ?", projectID).Count(&auditCountBefore).Error; err != nil {
		t.Fatal(err)
	}

	solo := GovernanceModeSolo
	if _, err := projects.Update(ctx, project.ID, ownerID.String(), project.Version, UpdateProjectInput{
		GovernanceMode: &solo,
	}); !errors.Is(err, ErrActiveWorkflowRuns) {
		t.Fatalf("governance mode change with active run returned %v, want %v", err, ErrActiveWorkflowRuns)
	}

	reloaded, err := projects.Get(ctx, projectID.String(), ownerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GovernanceMode != GovernanceModeTeam {
		t.Fatalf("governance mode after rejected update = %q, want team", reloaded.GovernanceMode)
	}
	if reloaded.Version != project.Version {
		t.Fatalf("project version after rejected update = %d, want %d", reloaded.Version, project.Version)
	}

	var auditCountAfter int64
	if err := database.Model(&storage.AuditEventModel{}).
		Where("project_id = ?", projectID).Count(&auditCountAfter).Error; err != nil {
		t.Fatal(err)
	}
	if auditCountAfter != auditCountBefore {
		t.Fatalf("project audit count after rejected update = %d, want %d", auditCountAfter, auditCountBefore)
	}
}

func TestSoloGovernanceReviewLifecyclePostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	projects := &ProjectService{database: database, access: access, now: time.Now}
	project, err := projects.Get(ctx, projectID.String(), ownerID.String())
	if err != nil {
		t.Fatal(err)
	}
	solo := GovernanceModeSolo
	project, err = projects.Update(ctx, project.ID, ownerID.String(), project.Version, UpdateProjectInput{GovernanceMode: &solo})
	if err != nil {
		t.Fatalf("enable solo governance: %v", err)
	}
	if project.GovernanceMode != GovernanceModeSolo {
		t.Fatalf("governance mode = %q, want solo", project.GovernanceMode)
	}
	editorID := uuid.New()
	editorEmail := "editor-" + uuid.NewString() + "@example.com"
	now := time.Now().UTC()
	if err := database.Create(&storage.UserModel{
		ID: editorID, Email: editorEmail, DisplayName: "Solo Editor", PasswordHash: "not-used",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	members, err := NewMemberService(database, access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := members.AddExisting(ctx, projectID.String(), ownerID.String(), editorEmail, RoleEditor); err != nil {
		t.Fatalf("add editor to solo project: %v", err)
	}

	target := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Approve a solo-governed requirement.","blocks":[{"type":"requirement","requirementId":"REQ-SOLO","priority":"should"}]}`),
	)
	reviews, err := NewReviewService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviews.Submit(ctx, projectID.String(), target.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: target.RevisionID, ReviewerIDs: []string{ownerID.String(), ownerID.String()}, MinimumApprovals: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate reviewer submission returned %v, want invalid input", err)
	}
	if _, err := reviews.Submit(ctx, projectID.String(), target.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: target.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("author-only review without exact Solo authority returned %v, want invalid input", err)
	}
	review, err := reviews.Submit(ctx, projectID.String(), target.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: target.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
		AllowSelfApproval: true,
	})
	if err != nil {
		t.Fatalf("submit solo review: %v", err)
	}
	if review.RequestedBy != editorID.String() || review.Policy.GovernanceMode != GovernanceModeSolo ||
		review.Policy.SoloSelfReviewOwnerID != ownerID.String() {
		t.Fatalf("review policy did not freeze solo governance: %#v", review.Policy)
	}
	if _, err := reviews.Decide(ctx, review.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Reviewed the scope and accepted the risk.",
	}); !errors.Is(err, ErrSoloReviewConfirmation) {
		t.Fatalf("unconfirmed solo review returned %v", err)
	}
	approved, err := reviews.Decide(ctx, review.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Reviewed the scope and accepted the risk.", SoloReviewConfirmed: true,
	})
	if err != nil {
		t.Fatalf("approve solo review: %v", err)
	}
	if approved.Status != "approved" || len(approved.Decisions) != 1 || !approved.Decisions[0].SoloSelfReview {
		t.Fatalf("unexpected solo approval: %#v", approved)
	}
	reconciled, err := reviews.DecideIfMatch(ctx, review.ID, ownerID.String(), review.ETag, DecideReviewInput{
		Decision: "approve", Summary: "Reviewed the scope and accepted the risk.", SoloReviewConfirmed: true,
	})
	if err != nil || reconciled.Status != "approved" || reconciled.ID != approved.ID {
		t.Fatalf("reconcile exact committed approval = %#v, %v", reconciled, err)
	}
	if _, err := reviews.DecideIfMatch(ctx, review.ID, ownerID.String(), review.ETag, DecideReviewInput{
		Decision: "approve", Summary: "different payload", SoloReviewConfirmed: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-exact closed retry returned %v, want conflict", err)
	}
	var audit storage.AuditEventModel
	if err := database.Where("project_id = ? AND action = ?", projectID, "review.decided").Order("created_at DESC").Take(&audit).Error; err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(audit.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["soloSelfReview"] != true || metadata["governanceMode"] != "solo" ||
		metadata["subjectAuthorId"] != ownerID.String() || metadata["summary"] == "" {
		t.Fatalf("solo review audit metadata is incomplete: %#v", metadata)
	}

	independentlyApprovedTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Allow an independent editor to approve an owner-authored revision.","blocks":[{"type":"requirement","requirementId":"REQ-SOLO-INDEPENDENT","priority":"should"}]}`),
	)
	independentReview, err := reviews.Submit(ctx, projectID.String(), independentlyApprovedTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: independentlyApprovedTarget.RevisionID, ReviewerIDs: []string{ownerID.String(), editorID.String()},
		MinimumApprovals: 1, AllowSelfApproval: true,
	})
	if err != nil {
		t.Fatalf("submit owner-authored solo review with an independent reviewer: %v", err)
	}
	if independentReview.Policy.SoloSelfReviewOwnerID != ownerID.String() {
		t.Fatalf("review policy did not preserve the optional owner self-review authority: %#v", independentReview.Policy)
	}
	independentlyApproved, err := reviews.Decide(ctx, independentReview.ID, editorID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Independently reviewed and approved.",
	})
	if err != nil {
		t.Fatalf("independent editor approval under optional solo self-review policy: %v", err)
	}
	if independentlyApproved.Status != "approved" || len(independentlyApproved.Decisions) != 1 || independentlyApproved.Decisions[0].SoloSelfReview {
		t.Fatalf("independent editor approval was not recorded as an ordinary approval: %#v", independentlyApproved)
	}
	var independentReceipt storage.CanonicalReviewApprovalReceiptModel
	if err := database.Where("review_request_id = ?", independentReview.ID).Take(&independentReceipt).Error; err != nil {
		t.Fatalf("load independently approved Canonical Review receipt: %v", err)
	}
	if independentReceipt.SoloSelfReview || independentReceipt.RevisionID.String() != independentlyApprovedTarget.RevisionID {
		t.Fatalf("independent approval receipt froze incorrect authority facts: %#v", independentReceipt)
	}

	wrongSoloAuthorTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Recover a Solo Owner policy that is not bound to the revision author.","blocks":[{"type":"requirement","requirementId":"REQ-SOLO-AUTHOR-DRIFT","priority":"should"}]}`),
	)
	wrongSoloAuthorRevisionID := uuid.MustParse(wrongSoloAuthorTarget.RevisionID)
	if err := database.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", wrongSoloAuthorRevisionID).
		Update("workflow_status", "in_review").Error; err != nil {
		t.Fatal(err)
	}
	wrongSoloAuthorRequest := storage.ReviewRequestModel{
		ID: uuid.New(), ProjectID: projectID, ArtifactID: uuid.MustParse(wrongSoloAuthorTarget.ArtifactID),
		RevisionID: wrongSoloAuthorRevisionID, ContentHash: wrongSoloAuthorTarget.ContentHash, Status: "open",
		Policy: json.RawMessage(fmt.Sprintf(
			`{"reviewerIds":[%q],"minimumApprovals":1,"prohibitSelfReview":true,"governanceMode":"solo","soloSelfReviewOwnerId":%q}`,
			ownerID.String(), ownerID.String(),
		)),
		RequestedBy: editorID, RequestedAt: time.Now().UTC().Add(time.Second).Truncate(time.Millisecond), ReviewAuthorityVersion: 1,
	}
	if err := database.Create(&wrongSoloAuthorRequest).Error; err != nil {
		t.Fatalf("seed Solo Owner/revision-author drift: %v", err)
	}
	if _, err := reviews.Decide(ctx, wrongSoloAuthorRequest.ID.String(), ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Detect the mismatched Solo Owner author authority.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Solo Owner/revision-author drift returned %v, want conflict after durable stale closure", err)
	}
	if err := database.Where("id = ?", wrongSoloAuthorRequest.ID).Take(&wrongSoloAuthorRequest).Error; err != nil {
		t.Fatal(err)
	}
	var wrongSoloAuthorRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", wrongSoloAuthorTarget.RevisionID).Take(&wrongSoloAuthorRevision).Error; err != nil {
		t.Fatal(err)
	}
	if wrongSoloAuthorRequest.Status != "stale" || wrongSoloAuthorRevision.WorkflowStatus != "changes_requested" {
		t.Fatalf("Solo Owner/revision-author drift did not become restartable: request=%#v revision=%#v", wrongSoloAuthorRequest, wrongSoloAuthorRevision)
	}
	wrongSoloAuthorRestart, err := reviews.Submit(ctx, projectID.String(), wrongSoloAuthorTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: wrongSoloAuthorTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart Solo Owner/revision-author drift review: %v", err)
	}
	if _, err := reviews.Decide(ctx, wrongSoloAuthorRestart.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve under a clean independent authority snapshot.",
	}); err != nil {
		t.Fatalf("approve restarted Solo Owner/revision-author drift review: %v", err)
	}

	impossibleCapacityTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Recover a pre-capacity author-only review.","blocks":[{"type":"requirement","requirementId":"REQ-REVIEW-CAPACITY","priority":"should"}]}`),
	)
	impossibleCapacityRevisionID := uuid.MustParse(impossibleCapacityTarget.RevisionID)
	if err := database.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", impossibleCapacityRevisionID).
		Update("workflow_status", "in_review").Error; err != nil {
		t.Fatal(err)
	}
	impossibleCapacityRequest := storage.ReviewRequestModel{
		ID: uuid.New(), ProjectID: projectID, ArtifactID: uuid.MustParse(impossibleCapacityTarget.ArtifactID),
		RevisionID: impossibleCapacityRevisionID, ContentHash: impossibleCapacityTarget.ContentHash, Status: "open",
		Policy: json.RawMessage(fmt.Sprintf(
			`{"reviewerIds":[%q],"minimumApprovals":1,"prohibitSelfReview":true,"governanceMode":"solo"}`,
			editorID.String(),
		)),
		RequestedBy: editorID, RequestedAt: time.Now().UTC().Add(time.Second).Truncate(time.Millisecond), ReviewAuthorityVersion: 1,
	}
	if err := database.Create(&impossibleCapacityRequest).Error; err != nil {
		t.Fatalf("seed pre-capacity author-only review: %v", err)
	}
	if _, err := reviews.Decide(ctx, impossibleCapacityRequest.ID.String(), editorID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Detect the impossible author-only capacity.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("pre-capacity author-only review returned %v, want conflict after durable stale closure", err)
	}
	if err := database.Where("id = ?", impossibleCapacityRequest.ID).Take(&impossibleCapacityRequest).Error; err != nil {
		t.Fatal(err)
	}
	if impossibleCapacityRequest.Status != "stale" {
		t.Fatalf("pre-capacity author-only review status = %q, want stale", impossibleCapacityRequest.Status)
	}
	impossibleCapacityRestart, err := reviews.Submit(ctx, projectID.String(), impossibleCapacityTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: impossibleCapacityTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart pre-capacity author-only review: %v", err)
	}
	if _, err := reviews.Decide(ctx, impossibleCapacityRestart.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve with an eligible independent reviewer.",
	}); err != nil {
		t.Fatalf("approve restarted pre-capacity review: %v", err)
	}

	emptyCapacityTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Recover an unassigned review after eligible membership disappears.","blocks":[{"type":"requirement","requirementId":"REQ-EMPTY-REVIEW-CAPACITY","priority":"should"}]}`),
	)
	emptyCapacityReview, err := reviews.Submit(ctx, projectID.String(), emptyCapacityTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: emptyCapacityTarget.RevisionID, ReviewerIDs: []string{}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("submit unassigned capacity review: %v", err)
	}
	if err := database.Model(&storage.ProjectMemberModel{}).
		Where("project_id = ? AND user_id = ?", projectID, editorID).
		Updates(map[string]any{"role": string(RoleViewer), "updated_at": time.Now().UTC()}).Error; err != nil {
		t.Fatalf("remove the last independent reviewer authority: %v", err)
	}
	if _, err := reviews.Decide(ctx, emptyCapacityReview.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Detect the disappeared independent approval capacity.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("empty-set membership-loss review returned %v, want conflict after durable stale closure", err)
	}
	var emptyCapacityRequest storage.ReviewRequestModel
	if err := database.Where("id = ?", emptyCapacityReview.ID).Take(&emptyCapacityRequest).Error; err != nil {
		t.Fatal(err)
	}
	if emptyCapacityRequest.Status != "stale" {
		t.Fatalf("empty-set membership-loss review status = %q, want stale", emptyCapacityRequest.Status)
	}
	if err := database.Model(&storage.ProjectMemberModel{}).
		Where("project_id = ? AND user_id = ?", projectID, editorID).
		Updates(map[string]any{"role": string(RoleEditor), "updated_at": time.Now().UTC()}).Error; err != nil {
		t.Fatalf("restore independent reviewer authority: %v", err)
	}
	emptyCapacityRestart, err := reviews.Submit(ctx, projectID.String(), emptyCapacityTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: emptyCapacityTarget.RevisionID, ReviewerIDs: []string{editorID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart empty-set membership-loss review: %v", err)
	}
	if _, err := reviews.Decide(ctx, emptyCapacityRestart.ID, editorID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve after restoring independent reviewer authority.",
	}); err != nil {
		t.Fatalf("approve restarted empty-set membership-loss review: %v", err)
	}

	archivedTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Recover when an artifact is archived during review.","blocks":[{"type":"requirement","requirementId":"REQ-ARCHIVED-REVIEW","priority":"should"}]}`),
	)
	archivedReview, err := reviews.Submit(ctx, projectID.String(), archivedTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: archivedTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("submit lifecycle drift recovery review: %v", err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", archivedTarget.ArtifactID).
		Update("lifecycle", "archived").Error; err != nil {
		t.Fatalf("archive artifact during review: %v", err)
	}
	if _, err := reviews.Decide(ctx, archivedReview.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Detect the archived artifact before receipt issuance.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("archived artifact review returned %v, want conflict after durable stale closure", err)
	}
	var archivedRequest storage.ReviewRequestModel
	if err := database.Where("id = ?", archivedReview.ID).Take(&archivedRequest).Error; err != nil {
		t.Fatal(err)
	}
	var archivedRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", archivedTarget.RevisionID).Take(&archivedRevision).Error; err != nil {
		t.Fatal(err)
	}
	if archivedRequest.Status != "stale" || archivedRevision.WorkflowStatus != "changes_requested" {
		t.Fatalf("archived artifact review did not become restartable: request=%#v revision=%#v", archivedRequest, archivedRevision)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", archivedTarget.ArtifactID).
		Update("lifecycle", "active").Error; err != nil {
		t.Fatalf("reactivate artifact after stale review: %v", err)
	}
	archivedRestart, err := reviews.Submit(ctx, projectID.String(), archivedTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: archivedTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart review after artifact reactivation: %v", err)
	}
	if _, err := reviews.Decide(ctx, archivedRestart.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve after restoring the active lifecycle.",
	}); err != nil {
		t.Fatalf("approve restarted active artifact review: %v", err)
	}

	quorumTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Require an append-only two-reviewer quorum.","blocks":[{"type":"requirement","requirementId":"REQ-SOLO-QUORUM","priority":"should"}]}`),
	)
	quorumReview, err := reviews.Submit(ctx, projectID.String(), quorumTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: quorumTarget.RevisionID, ReviewerIDs: []string{ownerID.String(), editorID.String()},
		MinimumApprovals: 2, AllowSelfApproval: true,
	})
	if err != nil {
		t.Fatalf("submit two-reviewer quorum: %v", err)
	}
	quorumPending, err := reviews.Decide(ctx, quorumReview.ID, editorID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Independent first approval.",
	})
	if err != nil || quorumPending.Status != "open" || len(quorumPending.Decisions) != 1 {
		t.Fatalf("record first quorum approval = %#v, %v", quorumPending, err)
	}
	if _, err := reviews.Decide(ctx, quorumReview.ID, editorID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Attempt to overwrite my prior approval.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate reviewer decision returned %v, want append-only conflict", err)
	}
	quorumApproved, err := reviews.Decide(ctx, quorumReview.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Owner explicitly confirms the two-reviewer quorum.", SoloReviewConfirmed: true,
	})
	if err != nil {
		t.Fatalf("close two-reviewer quorum with chained preconditions: %v", err)
	}
	if quorumApproved.Status != "approved" || len(quorumApproved.Decisions) != 2 {
		t.Fatalf("two-reviewer quorum did not close exactly: %#v", quorumApproved)
	}
	var quorumReceipt storage.CanonicalReviewApprovalReceiptModel
	if err := database.Where("review_request_id = ?", quorumReview.ID).Take(&quorumReceipt).Error; err != nil {
		t.Fatalf("load two-reviewer quorum receipt: %v", err)
	}
	if quorumReceipt.ApprovalCount != 2 || quorumReceipt.MinimumApprovals != 2 {
		t.Fatalf("two-reviewer quorum receipt froze incorrect threshold facts: %#v", quorumReceipt)
	}

	driftReviewerID := uuid.New()
	driftReviewerEmail := "drift-reviewer-" + uuid.NewString() + "@example.com"
	if err := database.Create(&storage.UserModel{
		ID: driftReviewerID, Email: driftReviewerEmail, DisplayName: "Authority Drift Reviewer", PasswordHash: "not-used",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := members.AddExisting(ctx, projectID.String(), ownerID.String(), driftReviewerEmail, RoleEditor); err != nil {
		t.Fatalf("add authority drift reviewer: %v", err)
	}
	driftTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Restart when append-only reviewer authority drifts.","blocks":[{"type":"requirement","requirementId":"REQ-AUTHORITY-DRIFT","priority":"should"}]}`),
	)
	driftReview, err := reviews.Submit(ctx, projectID.String(), driftTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: driftTarget.RevisionID, ReviewerIDs: []string{editorID.String(), driftReviewerID.String()}, MinimumApprovals: 2,
	})
	if err != nil {
		t.Fatalf("submit authority drift review: %v", err)
	}
	if _, err := reviews.Decide(ctx, driftReview.ID, editorID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approval before my role changes.",
	}); err != nil {
		t.Fatalf("record pre-drift approval: %v", err)
	}
	if err := database.Model(&storage.ProjectMemberModel{}).
		Where("project_id = ? AND user_id = ?", projectID, editorID).
		Updates(map[string]any{"role": string(RoleAdmin), "updated_at": now.Add(time.Second)}).Error; err != nil {
		t.Fatalf("change prior reviewer's role: %v", err)
	}
	if _, err := reviews.Decide(ctx, driftReview.ID, driftReviewerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "This must detect the earlier authority drift.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("authority drift closure returned %v, want conflict after durable stale closure", err)
	}
	var driftedRequest storage.ReviewRequestModel
	if err := database.Where("id = ?", driftReview.ID).Take(&driftedRequest).Error; err != nil {
		t.Fatal(err)
	}
	var driftedRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", driftTarget.RevisionID).Take(&driftedRevision).Error; err != nil {
		t.Fatal(err)
	}
	if driftedRequest.Status != "stale" || driftedRequest.ClosedAt == nil ||
		driftedRequest.ClosedByDecisionID != nil || driftedRevision.WorkflowStatus != "changes_requested" {
		t.Fatalf("authority drift did not become restartable: request=%#v revision=%#v", driftedRequest, driftedRevision)
	}
	driftRestart, err := reviews.Submit(ctx, projectID.String(), driftTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: driftTarget.RevisionID, ReviewerIDs: []string{driftReviewerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart review after authority drift: %v", err)
	}
	if _, err := reviews.Decide(ctx, driftRestart.ID, driftReviewerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approval under the restarted authority snapshot.",
	}); err != nil {
		t.Fatalf("approve restarted authority drift review: %v", err)
	}
	var driftReceipt storage.CanonicalReviewApprovalReceiptModel
	if err := database.Where("review_request_id = ?", driftRestart.ID).Take(&driftReceipt).Error; err != nil {
		t.Fatalf("load restarted authority drift receipt: %v", err)
	}

	forgedTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Reject a forged append-only OCC predecessor.","blocks":[{"type":"requirement","requirementId":"REQ-FORGED-OCC","priority":"should"}]}`),
	)
	forgedReview, err := reviews.Submit(ctx, projectID.String(), forgedTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: forgedTarget.RevisionID, ReviewerIDs: []string{editorID.String(), driftReviewerID.String()}, MinimumApprovals: 2,
	})
	if err != nil {
		t.Fatalf("submit forged OCC recovery review: %v", err)
	}
	forgedAt := time.Now().UTC().Truncate(time.Millisecond)
	if !forgedAt.After(forgedReview.RequestedAt) {
		forgedAt = forgedReview.RequestedAt.Add(time.Millisecond)
	}
	adminRole := string(RoleAdmin)
	soloMode := string(GovernanceModeSolo)
	ownerCount := 1
	confirmed := false
	forgedPrecondition := `"review:forged:open:0:0"`
	if err := database.Create(&storage.ReviewDecisionModel{
		ID: uuid.New(), ReviewRequestID: uuid.MustParse(forgedReview.ID), ReviewerID: editorID,
		Decision: "approve", Summary: "Syntactically valid but forged OCC predecessor.", CreatedAt: forgedAt,
		ReviewAuthorityVersion: 1, ReviewerRoleAtDecision: &adminRole, GovernanceModeAtDecision: &soloMode,
		OwnerCountAtDecision: &ownerCount, SoleOwnerIDAtDecision: &ownerID,
		SoloReviewConfirmed: &confirmed, PreconditionETag: &forgedPrecondition,
	}).Error; err != nil {
		t.Fatalf("seed forged append-only predecessor: %v", err)
	}
	if _, err := reviews.Decide(ctx, forgedReview.ID, driftReviewerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Detect the forged predecessor before closing.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged predecessor recovery returned %v, want conflict after durable stale closure", err)
	}
	var forgedRequest storage.ReviewRequestModel
	if err := database.Where("id = ?", forgedReview.ID).Take(&forgedRequest).Error; err != nil {
		t.Fatal(err)
	}
	var forgedRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", forgedTarget.RevisionID).Take(&forgedRevision).Error; err != nil {
		t.Fatal(err)
	}
	if forgedRequest.Status != "stale" || forgedRevision.WorkflowStatus != "changes_requested" {
		t.Fatalf("forged OCC predecessor did not become restartable: request=%#v revision=%#v", forgedRequest, forgedRevision)
	}
	forgedRestart, err := reviews.Submit(ctx, projectID.String(), forgedTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: forgedTarget.RevisionID, ReviewerIDs: []string{driftReviewerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart forged OCC review: %v", err)
	}
	if _, err := reviews.Decide(ctx, forgedRestart.ID, driftReviewerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve the clean restarted OCC chain.",
	}); err != nil {
		t.Fatalf("approve restarted forged OCC review: %v", err)
	}
	var forgedReceipt storage.CanonicalReviewApprovalReceiptModel
	if err := database.Where("review_request_id = ?", forgedRestart.ID).Take(&forgedReceipt).Error; err != nil {
		t.Fatalf("load restarted forged OCC receipt: %v", err)
	}

	secondOwnerID := uuid.New()
	secondOwnerEmail := "second-owner-" + uuid.NewString() + "@example.com"
	if err := database.Create(&storage.UserModel{
		ID: secondOwnerID, Email: secondOwnerEmail, DisplayName: "Second Owner", PasswordHash: "not-used",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := members.AddExisting(ctx, projectID.String(), ownerID.String(), secondOwnerEmail, RoleOwner); !errors.Is(err, ErrSoloOwnerInvariant) {
		t.Fatalf("solo mode accepted a second owner: %v", err)
	}

	team := GovernanceModeTeam
	project, err = projects.Update(ctx, project.ID, ownerID.String(), project.Version, UpdateProjectInput{GovernanceMode: &team})
	if err != nil {
		t.Fatalf("switch solo project to team: %v", err)
	}
	artifacts, err := NewArtifactService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := artifacts.ReviewGate(ctx, target.ArtifactID, ownerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Passed {
		t.Fatalf("switching to team retroactively invalidated frozen solo approval: %#v", gate.Checks)
	}

	teamTarget := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Require independent team approval.","blocks":[{"type":"requirement","requirementId":"REQ-TEAM","priority":"should"}]}`),
	)
	if _, err := reviews.Submit(ctx, projectID.String(), teamTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: teamTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("team policy created an impossible author-only review: %v", err)
	}
}

func TestSoloGovernanceEditorAuthoredReviewUsesIndependentOwnerApprovalPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	projects := &ProjectService{database: database, access: access, now: time.Now}
	project, err := projects.Get(ctx, projectID.String(), ownerID.String())
	if err != nil {
		t.Fatal(err)
	}
	solo := GovernanceModeSolo
	if _, err := projects.Update(ctx, project.ID, ownerID.String(), project.Version, UpdateProjectInput{GovernanceMode: &solo}); err != nil {
		t.Fatalf("enable solo governance: %v", err)
	}

	editorID := uuid.New()
	editorEmail := "editor-" + uuid.NewString() + "@example.com"
	now := time.Now().UTC()
	if err := database.Create(&storage.UserModel{
		ID: editorID, Email: editorEmail, DisplayName: "Independent Editor", PasswordHash: "not-used",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	members, err := NewMemberService(database, access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := members.AddExisting(ctx, projectID.String(), ownerID.String(), editorEmail, RoleEditor); err != nil {
		t.Fatalf("add editor to solo project: %v", err)
	}

	target := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Approve an editor-authored requirement.","blocks":[{"type":"requirement","requirementId":"REQ-INDEPENDENT","priority":"should"}]}`),
	)
	reviews, err := NewReviewService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	review, err := reviews.Submit(ctx, projectID.String(), target.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: target.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
		AllowSelfApproval: true,
	})
	if err != nil {
		t.Fatalf("submit editor-authored review: %v", err)
	}
	if review.Policy.GovernanceMode != GovernanceModeSolo || review.Policy.SoloSelfReviewOwnerID != "" {
		t.Fatalf("editor-authored review froze self-review evidence: %#v", review.Policy)
	}

	approved, err := reviews.Decide(ctx, review.ID, ownerID.String(), DecideReviewInput{Decision: "approve"})
	if err != nil {
		t.Fatalf("independent owner approval required solo confirmation: %v", err)
	}
	if approved.Status != "approved" || len(approved.Decisions) != 1 || approved.Decisions[0].SoloSelfReview {
		t.Fatalf("independent approval was recorded as solo self-review: %#v", approved)
	}

	var audit storage.AuditEventModel
	if err := database.Where("project_id = ? AND action = ? AND target_id = ?", projectID, "review.decided", review.ID).Take(&audit).Error; err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(audit.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if _, exists := metadata["soloSelfReview"]; exists {
		t.Fatalf("independent approval audit contains solo self-review evidence: %#v", metadata)
	}
	if _, exists := metadata["governanceMode"]; exists {
		t.Fatalf("independent approval audit contains solo governance evidence: %#v", metadata)
	}
	if metadata["subjectAuthorId"] != editorID.String() {
		t.Fatalf("independent approval audit lost the revision author: %#v", metadata)
	}

	artifacts, err := NewArtifactService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := artifacts.ReviewGate(ctx, target.ArtifactID, ownerID.String())
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Passed {
		t.Fatalf("independent owner approval is not canonical: %#v", gate.Checks)
	}

	legacyApprovedTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "approved", "current",
		json.RawMessage(`{"summary":"A legacy approval must remain untrusted.","blocks":[{"type":"requirement","requirementId":"REQ-LEGACY-APPROVED","priority":"should"}]}`),
	)
	legacyApprovedRevisionID := uuid.MustParse(legacyApprovedTarget.RevisionID)
	legacyApprovedAt := time.Now().UTC().Truncate(time.Millisecond)
	legacyApprovedRequest := storage.ReviewRequestModel{
		ID: uuid.New(), ProjectID: projectID, ArtifactID: uuid.MustParse(legacyApprovedTarget.ArtifactID),
		RevisionID: legacyApprovedRevisionID, ContentHash: legacyApprovedTarget.ContentHash, Status: "open",
		Policy: json.RawMessage(fmt.Sprintf(
			`{"reviewerIds":[%q],"minimumApprovals":1,"prohibitSelfReview":true,"governanceMode":"solo"}`,
			ownerID.String(),
		)),
		RequestedBy: editorID, RequestedAt: legacyApprovedAt.Add(-time.Minute), ReviewAuthorityVersion: 0,
	}
	if err := database.Create(&legacyApprovedRequest).Error; err != nil {
		t.Fatalf("seed pre-authority approved review: %v", err)
	}
	legacyApprovedDecisionID := uuid.New()
	if err := database.Create(&storage.ReviewDecisionModel{
		ID: legacyApprovedDecisionID, ReviewRequestID: legacyApprovedRequest.ID, ReviewerID: ownerID,
		Decision: "approve", Summary: "Historical approval without canonical authority.",
		CreatedAt: legacyApprovedAt, ReviewAuthorityVersion: 0,
	}).Error; err != nil {
		t.Fatalf("seed pre-authority approval decision: %v", err)
	}
	if err := database.Model(&storage.ReviewRequestModel{}).Where("id = ?", legacyApprovedRequest.ID).
		Updates(map[string]any{
			"status": "approved", "closed_at": legacyApprovedAt,
			"closed_by_decision_id": legacyApprovedDecisionID,
		}).Error; err != nil {
		t.Fatalf("close pre-authority approved review: %v", err)
	}
	legacyApprovedGate, err := artifacts.ReviewGate(ctx, legacyApprovedTarget.ArtifactID, ownerID.String())
	if err != nil {
		t.Fatalf("evaluate pre-authority approved ReviewGate: %v", err)
	}
	legacyApprovalCheck := reviewGateCheckByCode(legacyApprovedGate, "canonical_review_approved")
	if legacyApprovedGate.Passed || legacyApprovalCheck == nil || legacyApprovalCheck.Severity != "error" {
		t.Fatalf("pre-authority approved review passed without an exact receipt: %#v", legacyApprovedGate)
	}

	legacyTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Recover a pre-authority open review.","blocks":[{"type":"requirement","requirementId":"REQ-LEGACY-REVIEW","priority":"should"}]}`),
	)
	legacyRevisionID := uuid.MustParse(legacyTarget.RevisionID)
	if err := database.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", legacyRevisionID).
		Update("workflow_status", "in_review").Error; err != nil {
		t.Fatal(err)
	}
	legacyRequest := storage.ReviewRequestModel{
		ID: uuid.New(), ProjectID: projectID, ArtifactID: uuid.MustParse(legacyTarget.ArtifactID),
		RevisionID: legacyRevisionID, ContentHash: legacyTarget.ContentHash, Status: "open",
		Policy: json.RawMessage(`{"legacyPolicyShape":true}`), RequestedBy: editorID,
		RequestedAt: now, ReviewAuthorityVersion: 0,
	}
	if err := database.Create(&legacyRequest).Error; err != nil {
		t.Fatalf("seed pre-authority open review: %v", err)
	}
	legacyListed, err := reviews.List(ctx, projectID.String(), ownerID.String())
	if err != nil {
		t.Fatalf("list reviews with a legacy policy: %v", err)
	}
	legacyVisible := false
	for _, listed := range legacyListed {
		if listed.ID == legacyRequest.ID.String() {
			legacyVisible = true
			if listed.ReviewAuthorityVersion != 0 || listed.AuthorityState != "legacy" || listed.Policy.ReviewerIDs == nil {
				t.Fatalf("legacy review read projection is not fail-closed: %#v", listed)
			}
		}
	}
	if !legacyVisible {
		t.Fatal("legacy review disappeared from the recoverable list projection")
	}
	legacyLookup, err := reviews.Get(ctx, projectID.String(), legacyRequest.ID.String(), ownerID.String())
	if err != nil || legacyLookup.AuthorityState != "legacy" {
		t.Fatalf("lookup recoverable legacy review = %#v, %v", legacyLookup, err)
	}
	if _, err := reviews.Decide(ctx, legacyRequest.ID.String(), ownerID.String(), DecideReviewInput{Decision: "approve"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy open review recovery returned %v, want conflict after durable stale closure", err)
	}
	var recoveredLegacyRequest storage.ReviewRequestModel
	if err := database.Where("id = ?", legacyRequest.ID).Take(&recoveredLegacyRequest).Error; err != nil {
		t.Fatal(err)
	}
	var recoveredLegacyRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", legacyRevisionID).Take(&recoveredLegacyRevision).Error; err != nil {
		t.Fatal(err)
	}
	if recoveredLegacyRequest.Status != "stale" || recoveredLegacyRequest.ReviewAuthorityVersion != 0 ||
		recoveredLegacyRequest.ClosedAt == nil || recoveredLegacyRequest.ClosedByDecisionID != nil ||
		recoveredLegacyRevision.WorkflowStatus != "changes_requested" {
		t.Fatalf("legacy review did not become safely restartable: request=%#v revision=%#v", recoveredLegacyRequest, recoveredLegacyRevision)
	}
	restartedReview, err := reviews.Submit(ctx, projectID.String(), legacyTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: legacyTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart review under Canonical Review authority: %v", err)
	}
	restartedApproval, err := reviews.Decide(ctx, restartedReview.ID, ownerID.String(), DecideReviewInput{Decision: "approve"})
	if err != nil {
		t.Fatalf("approve restarted Canonical Review: %v", err)
	}
	if restartedApproval.Status != "approved" {
		t.Fatalf("restarted Canonical Review status = %q, want approved", restartedApproval.Status)
	}
	var restartedReceipt storage.CanonicalReviewApprovalReceiptModel
	if err := database.Where("review_request_id = ?", restartedReview.ID).Take(&restartedReceipt).Error; err != nil {
		t.Fatalf("load restarted Canonical Review receipt: %v", err)
	}

	missingGovernanceTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Recover a malformed v1 authority policy.","blocks":[{"type":"requirement","requirementId":"REQ-V1-POLICY-RECOVERY","priority":"should"}]}`),
	)
	missingGovernanceRevisionID := uuid.MustParse(missingGovernanceTarget.RevisionID)
	if err := database.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", missingGovernanceRevisionID).
		Update("workflow_status", "in_review").Error; err != nil {
		t.Fatal(err)
	}
	missingGovernanceRequest := storage.ReviewRequestModel{
		ID: uuid.New(), ProjectID: projectID, ArtifactID: uuid.MustParse(missingGovernanceTarget.ArtifactID),
		RevisionID: missingGovernanceRevisionID, ContentHash: missingGovernanceTarget.ContentHash, Status: "open",
		Policy: json.RawMessage(fmt.Sprintf(
			`{"reviewerIds":[%q],"minimumApprovals":1,"prohibitSelfReview":true}`,
			ownerID.String(),
		)),
		RequestedBy: editorID, RequestedAt: time.Now().UTC().Truncate(time.Millisecond),
		ReviewAuthorityVersion: 1,
	}
	if err := database.Create(&missingGovernanceRequest).Error; err != nil {
		t.Fatalf("seed v1 policy missing governanceMode: %v", err)
	}
	invalidLookup, err := reviews.Get(ctx, projectID.String(), missingGovernanceRequest.ID.String(), ownerID.String())
	if err != nil || invalidLookup.AuthorityState != "invalid" || invalidLookup.Policy.ReviewerIDs == nil {
		t.Fatalf("lookup malformed v1 review = %#v, %v", invalidLookup, err)
	}
	if _, err := reviews.Decide(ctx, missingGovernanceRequest.ID.String(), ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Trigger a safe restart instead of an issuer dead end.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("malformed v1 policy recovery returned %v, want conflict after durable stale closure", err)
	}
	var invalidRequest storage.ReviewRequestModel
	if err := database.Where("id = ?", missingGovernanceRequest.ID).Take(&invalidRequest).Error; err != nil {
		t.Fatal(err)
	}
	var invalidRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", missingGovernanceRevisionID).Take(&invalidRevision).Error; err != nil {
		t.Fatal(err)
	}
	if invalidRequest.Status != "stale" || invalidRevision.WorkflowStatus != "changes_requested" {
		t.Fatalf("malformed v1 policy did not become restartable: request=%#v revision=%#v", invalidRequest, invalidRevision)
	}
	validPolicyRestart, err := reviews.Submit(ctx, projectID.String(), missingGovernanceTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: missingGovernanceTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart malformed v1 policy review: %v", err)
	}
	if _, err := reviews.Decide(ctx, validPolicyRestart.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve the corrected authority policy.",
	}); err != nil {
		t.Fatalf("approve corrected v1 policy review: %v", err)
	}

	thresholdTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Recover an open request whose threshold was already reached.","blocks":[{"type":"requirement","requirementId":"REQ-OPEN-THRESHOLD-RECOVERY","priority":"should"}]}`),
	)
	thresholdReview, err := reviews.Submit(ctx, projectID.String(), thresholdTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: thresholdTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("submit threshold dead-end canary review: %v", err)
	}
	thresholdDecisionAt := time.Now().UTC().Truncate(time.Millisecond)
	if !thresholdDecisionAt.After(thresholdReview.RequestedAt) {
		thresholdDecisionAt = thresholdReview.RequestedAt.Add(time.Millisecond)
	}
	ownerRole := string(RoleOwner)
	soloGovernance := string(GovernanceModeSolo)
	oneOwner := 1
	ordinaryConfirmation := false
	thresholdPrecondition := thresholdReview.ETag
	if err := database.Create(&storage.ReviewDecisionModel{
		ID: uuid.New(), ReviewRequestID: uuid.MustParse(thresholdReview.ID), ReviewerID: ownerID,
		Decision: "approve", Summary: "Canonical approval inserted without its atomic close.", CreatedAt: thresholdDecisionAt,
		ReviewAuthorityVersion: 1, ReviewerRoleAtDecision: &ownerRole,
		GovernanceModeAtDecision: &soloGovernance, OwnerCountAtDecision: &oneOwner,
		SoleOwnerIDAtDecision: &ownerID, SoloReviewConfirmed: &ordinaryConfirmation,
		PreconditionETag: &thresholdPrecondition,
	}).Error; err != nil {
		t.Fatalf("seed already-reached open threshold: %v", err)
	}
	if _, err := reviews.Decide(ctx, thresholdReview.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Detect the append-only threshold dead end.",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("already-reached open threshold recovery returned %v, want conflict after durable stale closure", err)
	}
	var thresholdRequest storage.ReviewRequestModel
	if err := database.Where("id = ?", thresholdReview.ID).Take(&thresholdRequest).Error; err != nil {
		t.Fatal(err)
	}
	var thresholdRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", thresholdTarget.RevisionID).Take(&thresholdRevision).Error; err != nil {
		t.Fatal(err)
	}
	if thresholdRequest.Status != "stale" || thresholdRevision.WorkflowStatus != "changes_requested" {
		t.Fatalf("already-reached threshold did not become restartable: request=%#v revision=%#v", thresholdRequest, thresholdRevision)
	}
	thresholdRestart, err := reviews.Submit(ctx, projectID.String(), thresholdTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: thresholdTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatalf("restart already-reached threshold review: %v", err)
	}
	if _, err := reviews.Decide(ctx, thresholdRestart.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve the clean threshold restart.",
	}); err != nil {
		t.Fatalf("approve restarted threshold review: %v", err)
	}

	zeroID := "00000000-0000-0000-0000-000000000000"
	if _, err := reviews.Submit(ctx, projectID.String(), thresholdTarget.ArtifactID, zeroID, SubmitReviewInput{
		RevisionID: thresholdTarget.RevisionID, MinimumApprovals: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero actor identity returned %v, want invalid input", err)
	}
	if _, err := reviews.Submit(ctx, projectID.String(), thresholdTarget.ArtifactID, editorID.String(), SubmitReviewInput{
		RevisionID: zeroID, MinimumApprovals: 1,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero revision identity returned %v, want invalid input", err)
	}
	if _, err := reviews.Decide(ctx, zeroID, ownerID.String(), DecideReviewInput{Decision: "approve"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero review identity returned %v, want invalid input", err)
	}

	clockRollbackTarget := seedArtifactLineageRevision(
		t, database, store, projectID, editorID, "product_requirements", "draft", "current",
		json.RawMessage(`{"summary":"Keep review request time causal across clock rollback.","blocks":[{"type":"requirement","requirementId":"REQ-REVIEW-CLOCK-ROLLBACK","priority":"should"}]}`),
	)
	var clockRollbackRevision storage.ArtifactRevisionModel
	if err := database.Where("id = ?", clockRollbackTarget.RevisionID).Take(&clockRollbackRevision).Error; err != nil {
		t.Fatal(err)
	}
	originalNow := reviews.now
	reviews.now = func() time.Time { return clockRollbackRevision.CreatedAt.Add(-time.Hour) }
	clockRollbackReview, clockRollbackErr := reviews.Submit(
		ctx, projectID.String(), clockRollbackTarget.ArtifactID, editorID.String(), SubmitReviewInput{
			RevisionID:  clockRollbackTarget.RevisionID,
			ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
		},
	)
	reviews.now = originalNow
	if clockRollbackErr != nil {
		t.Fatalf("submit review during clock rollback: %v", clockRollbackErr)
	}
	if clockRollbackReview.RequestedAt.Before(clockRollbackRevision.CreatedAt) ||
		!clockRollbackReview.RequestedAt.Equal(canonicalReviewCeilingMillisecond(clockRollbackRevision.CreatedAt)) {
		t.Fatalf("clock rollback request time=%s revision time=%s", clockRollbackReview.RequestedAt, clockRollbackRevision.CreatedAt)
	}
	if _, err := reviews.Decide(ctx, clockRollbackReview.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Approve after the causal request-time clamp.",
	}); err != nil {
		t.Fatalf("approve clock-rollback review: %v", err)
	}
}
