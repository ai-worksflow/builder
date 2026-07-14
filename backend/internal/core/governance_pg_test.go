package core

import (
	"context"
	"encoding/json"
	"errors"
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
	teamReview, err := reviews.Submit(ctx, projectID.String(), teamTarget.ArtifactID, ownerID.String(), SubmitReviewInput{
		RevisionID: teamTarget.RevisionID, ReviewerIDs: []string{ownerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviews.Decide(ctx, teamReview.ID, ownerID.String(), DecideReviewInput{
		Decision: "approve", Summary: "Attempted self-review.", SoloReviewConfirmed: true,
	}); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("team policy allowed self-approval: %v", err)
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
}
