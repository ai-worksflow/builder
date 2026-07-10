package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func TestArtifactLineageHelpersRequireExactPageIdentity(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{
		"nodes":[
			{"id":"feature-orders","kind":"feature"},
			{"id":"page-orders","kind":"page"}
		],
		"semantic":{"nodes":[{"id":"page-history","type":"page"}]}
	}`)
	if !blueprintContainsPageNode(payload, "page-orders") || !blueprintContainsPageNode(payload, "page-history") {
		t.Fatal("Blueprint Page nodes were not found in canonical or semantic nodes")
	}
	if blueprintContainsPageNode(payload, "feature-orders") || blueprintContainsPageNode(payload, "missing") {
		t.Fatal("a non-Page or missing node satisfied the PageSpec anchor")
	}

	left := VersionRef{ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: "sha256:page"}
	right := left
	if !sameWholeVersionRef(left, right) {
		t.Fatal("identical whole revision refs must match")
	}
	right.ContentHash = "sha256:other"
	if sameWholeVersionRef(left, right) {
		t.Fatal("different content hashes must not match")
	}
	anchor := "page-orders"
	right = left
	right.AnchorID = &anchor
	if sameWholeVersionRef(left, right) {
		t.Fatal("anchored refs must not satisfy a whole PageSpec revision identity")
	}
}

func TestArtifactServiceEnforcesPageSpecLineageOnCreateUpdateAndRevision(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, service, projectID, userID := newArtifactLineageFixture(t, database)
	blueprintPayload := json.RawMessage(`{
		"nodes":[
			{"id":"feature-orders","key":"FEATURE-ORDERS","kind":"feature"},
			{"id":"page-orders","key":"PAGE-ORDERS","kind":"page"}
		]
	}`)
	blueprintRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "blueprint", "approved", "current", blueprintPayload,
	)
	pageAnchor := "page-orders"
	blueprintRef.AnchorID = &pageAnchor
	validSource := ArtifactSourceInput{Ref: blueprintRef, Purpose: "blueprint", Required: true}
	pageSpecPayload := json.RawMessage(`{"blueprintPageNodeId":"page-orders","title":"Orders"}`)

	missingAnchor := blueprintRef
	missingAnchor.AnchorID = nil
	missingNode := blueprintRef
	missingNodeAnchor := "page-missing"
	missingNode.AnchorID = &missingNodeAnchor
	unapprovedBlueprintRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "blueprint", "draft", "current", blueprintPayload,
	)
	unapprovedBlueprintRef.AnchorID = &pageAnchor
	for name, input := range map[string]CreateArtifactInput{
		"content_source_mismatch": {
			Kind: "page_spec", Title: "Wrong Page", Content: json.RawMessage(`{"blueprintPageNodeId":"page-other"}`),
			SourceVersions: []ArtifactSourceInput{validSource},
		},
		"missing_source_anchor": {
			Kind: "page_spec", Title: "Missing anchor", Content: pageSpecPayload,
			SourceVersions: []ArtifactSourceInput{{Ref: missingAnchor, Purpose: "blueprint", Required: true}},
		},
		"missing_blueprint_node": {
			Kind: "page_spec", Title: "Missing node", Content: pageSpecPayload,
			SourceVersions: []ArtifactSourceInput{{Ref: missingNode, Purpose: "blueprint", Required: true}},
		},
		"unapproved_blueprint": {
			Kind: "page_spec", Title: "Unapproved Blueprint", Content: pageSpecPayload,
			SourceVersions: []ArtifactSourceInput{{Ref: unapprovedBlueprintRef, Purpose: "blueprint", Required: true}},
		},
		"missing_blueprint_source": {
			Kind: "page_spec", Title: "Missing source", Content: pageSpecPayload,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Create(ctx, projectID.String(), userID.String(), input); !errors.Is(err, ErrBlockingGate) {
				t.Fatalf("expected PageSpec lineage gate, got %v", err)
			}
		})
	}

	created, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "page_spec", Title: "Orders", Content: pageSpecPayload,
		SourceVersions: []ArtifactSourceInput{validSource},
	})
	if err != nil {
		t.Fatalf("valid PageSpec create failed: %v", err)
	}
	if created.Draft == nil || len(created.Draft.SourceVersions) != 1 {
		t.Fatalf("valid PageSpec did not retain its Blueprint source: %#v", created.Draft)
	}

	updated, err := service.UpdateDraft(ctx, created.Draft.ID, userID.String(), created.Draft.ETag, UpdateDraftInput{
		Content: json.RawMessage(`{"blueprintPageNodeId":"page-orders","title":"Orders updated"}`),
		// SourceVersions deliberately omitted: the service must validate and retain the existing source.
	})
	if err != nil {
		t.Fatalf("content-only PageSpec update failed: %v", err)
	}
	if len(updated.SourceVersions) != 1 || updated.SourceVersions[0].RevisionID != blueprintRef.RevisionID ||
		updated.SourceVersions[0].AnchorID == nil || *updated.SourceVersions[0].AnchorID != pageAnchor {
		t.Fatalf("content-only update lost exact Blueprint lineage: %#v", updated.SourceVersions)
	}

	if _, err := service.UpdateDraft(ctx, updated.ID, userID.String(), updated.ETag, UpdateDraftInput{
		Content: pageSpecPayload, SourceVersions: []ArtifactSourceInput{},
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("explicit empty PageSpec sources bypassed lineage gate: %v", err)
	}
	if _, err := service.UpdateDraft(ctx, updated.ID, userID.String(), updated.ETag, UpdateDraftInput{
		Content: json.RawMessage(`{"blueprintPageNodeId":"page-other","title":"Drifted"}`),
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("content/source anchor drift bypassed lineage gate: %v", err)
	}

	corruptAnchor := "page-missing"
	if err := database.Model(&storage.ArtifactDraftSourceModel{}).
		Where("draft_id = ?", updated.ID).Update("source_anchor_id", corruptAnchor).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateRevision(
		ctx, created.Artifact.ID, userID.String(), updated.ETag, CreateRevisionInput{ChangeSummary: "Freeze invalid lineage"},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("CreateRevision froze corrupted PageSpec lineage: %v", err)
	}
	var revisions int64
	if err := database.Model(&storage.ArtifactRevisionModel{}).
		Where("artifact_id = ?", created.Artifact.ID).Count(&revisions).Error; err != nil {
		t.Fatal(err)
	}
	if revisions != 0 {
		t.Fatalf("lineage-blocked PageSpec created %d revision(s)", revisions)
	}
}

func TestArtifactServicePrototypeLineageFormalAndExploratory(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, service, projectID, userID := newArtifactLineageFixture(t, database)
	pageSpecRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "page_spec", "approved", "current",
		json.RawMessage(`{"blueprintPageNodeId":"page-orders","title":"Orders"}`),
	)
	pageSpecSource := ArtifactSourceInput{Ref: pageSpecRef, Purpose: "page_spec", Required: true}
	formalPayload := prototypeLineagePayload(t, pageSpecRef, false)

	created, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Orders Prototype", Content: formalPayload,
		SourceVersions: []ArtifactSourceInput{pageSpecSource},
	})
	if err != nil {
		t.Fatalf("valid formal Prototype create failed: %v", err)
	}
	if created.Draft == nil {
		t.Fatal("valid formal Prototype has no draft")
	}
	if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Duplicate PageSpec source", Content: formalPayload,
		SourceVersions: []ArtifactSourceInput{
			pageSpecSource,
			{Ref: pageSpecRef, Purpose: "secondary_page_spec", Required: false},
		},
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("Prototype accepted multiple PageSpec sources: %v", err)
	}

	driftedRef := pageSpecRef
	driftedRef.ContentHash = "sha256:drifted"
	if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Drifted Prototype", Content: prototypeLineagePayload(t, driftedRef, false),
		SourceVersions: []ArtifactSourceInput{pageSpecSource},
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("Prototype content/source mismatch bypassed gate: %v", err)
	}

	if err := database.Model(&storage.ArtifactHealthModel{}).
		Where("artifact_id = ?", pageSpecRef.ArtifactID).Update("sync_status", "needs_sync").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Stale Prototype", Content: formalPayload,
		SourceVersions: []ArtifactSourceInput{pageSpecSource},
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("formal Prototype accepted a stale PageSpec: %v", err)
	}

	unapprovedRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "page_spec", "draft", "needs_sync",
		json.RawMessage(`{"blueprintPageNodeId":"page-experiment","title":"Experiment"}`),
	)
	unapprovedSource := ArtifactSourceInput{Ref: unapprovedRef, Purpose: "page_spec", Required: true}
	if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Unapproved formal", Content: prototypeLineagePayload(t, unapprovedRef, false),
		SourceVersions: []ArtifactSourceInput{unapprovedSource},
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("formal Prototype accepted an unapproved PageSpec: %v", err)
	}
	if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Exploratory", Content: prototypeLineagePayload(t, unapprovedRef, true),
		SourceVersions: []ArtifactSourceInput{unapprovedSource},
	}); err != nil {
		t.Fatalf("exact exploratory Prototype should allow an unapproved PageSpec: %v", err)
	}

	if err := database.Model(&storage.ArtifactDraftSourceModel{}).
		Where("draft_id = ?", created.Draft.ID).Update("source_content_hash", "sha256:corrupt").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateRevision(
		ctx, created.Artifact.ID, userID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Freeze invalid Prototype lineage"},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("CreateRevision froze corrupted Prototype lineage: %v", err)
	}
}

func newArtifactLineageFixture(
	t *testing.T,
	database *gorm.DB,
) (*baselineContentStoreSpy, *ArtifactService, uuid.UUID, uuid.UUID) {
	t.Helper()
	now := time.Now().UTC()
	userID := uuid.New()
	projectID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: userID, Email: "artifact-lineage-" + uuid.NewString() + "@example.com",
		DisplayName: "Lineage Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Artifact lineage", Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: userID, Role: "owner", JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	store := newBaselineContentStoreSpy()
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewArtifactService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	return store, service, projectID, userID
}

func seedArtifactLineageRevision(
	t *testing.T,
	database *gorm.DB,
	store *baselineContentStoreSpy,
	projectID uuid.UUID,
	userID uuid.UUID,
	kind string,
	workflowStatus string,
	syncStatus string,
	payload json.RawMessage,
) VersionRef {
	t.Helper()
	now := time.Now().UTC()
	artifactID := uuid.New()
	revisionID := uuid.New()
	contentRef := store.addFinalized("lineage-source-"+revisionID.String(), payload)
	artifact := storage.ArtifactModel{
		ID: artifactID, ProjectID: projectID, Kind: kind,
		ArtifactKey: strings.ToUpper(kind) + "-" + strings.ToUpper(artifactID.String()[:8]),
		Title:       kind, Lifecycle: "active", Version: 1,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	var approvedAt *time.Time
	if workflowStatus == "approved" {
		approvedAt = &now
	}
	revision := storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: artifactID, RevisionNumber: 1, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash,
		ByteSize: contentRef.ByteSize, WorkflowStatus: workflowStatus, ChangeSource: "human",
		ChangeSummary: "Lineage source", CreatedBy: userID, CreatedAt: now, ApprovedAt: approvedAt,
	}
	if err := database.Create(&revision).Error; err != nil {
		t.Fatal(err)
	}
	pointers := map[string]any{"latest_revision_id": revisionID}
	if workflowStatus == "approved" {
		pointers["latest_approved_revision_id"] = revisionID
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).Updates(pointers).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactHealthModel{
		ArtifactID: artifactID, SyncStatus: syncStatus, DeliveryStatus: "incomplete",
		Report: json.RawMessage(`{}`), ComputedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return VersionRef{ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: contentRef.ContentHash}
}

func prototypeLineagePayload(t *testing.T, pageSpec VersionRef, exploratory bool) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"pageSpecRevision": pageSpec,
		"exploratory":      exploratory,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
