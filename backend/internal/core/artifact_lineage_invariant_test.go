package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
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

func TestGenericArtifactMutationRejectsSystemManagedKinds(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"requirement_baseline", "workspace", "quality_report", "test_report"} {
		if err := ensureGenericArtifactMutationAllowed(kind); !errors.Is(err, ErrForbidden) {
			t.Fatalf("generic mutation accepted system-managed %s: %v", kind, err)
		}
	}
	for _, kind := range []string{"product_requirements", "blueprint", "page_spec", "prototype"} {
		if err := ensureGenericArtifactMutationAllowed(kind); err != nil {
			t.Fatalf("generic mutation rejected human-editable %s: %v", kind, err)
		}
	}
}

func TestArtifactAndOutputProposalServicesCannotMutateSystemManagedArtifacts(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, userID := newArtifactLineageFixture(t, database)
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := NewProposalService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"requirement_baseline", "workspace", "quality_report", "test_report"} {
		t.Run(kind, func(t *testing.T) {
			if _, err := artifacts.Create(
				ctx, projectID.String(), userID.String(),
				CreateArtifactInput{Kind: kind, Title: "Forbidden " + kind, Content: json.RawMessage(`{"schemaVersion":1}`)},
			); !errors.Is(err, ErrForbidden) {
				t.Fatalf("generic Create accepted %s: %v", kind, err)
			}

			ref := seedArtifactLineageRevision(
				t, database, store, projectID, userID, kind, "approved", "current",
				json.RawMessage(`{"schemaVersion":1,"files":[]}`),
			)
			artifactID, revisionID := uuid.MustParse(ref.ArtifactID), uuid.MustParse(ref.RevisionID)
			var revision storage.ArtifactRevisionModel
			if err := database.Where("id = ?", revisionID).Take(&revision).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			draftID := uuid.New()
			draft := storage.ArtifactDraftModel{
				ID: draftID, ArtifactID: artifactID, BaseRevisionID: &revisionID, Sequence: 1,
				ETag: draftETag(draftID, 1, revision.ContentHash), SchemaVersion: revision.SchemaVersion,
				ContentStore: revision.ContentStore, ContentRef: revision.ContentRef,
				ContentHash: revision.ContentHash, ByteSize: revision.ByteSize, Status: "draft",
				CreatedBy: userID, UpdatedBy: userID, CreatedAt: now, UpdatedAt: now,
			}
			if err := database.Create(&draft).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
				Update("latest_draft_id", draftID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := artifacts.UpdateDraft(
				ctx, draftID.String(), userID.String(), draft.ETag,
				UpdateDraftInput{Content: json.RawMessage(`{"schemaVersion":1,"mutated":true}`)},
			); !errors.Is(err, ErrForbidden) {
				t.Fatalf("generic UpdateDraft accepted %s: %v", kind, err)
			}
			if _, err := artifacts.CreateRevision(
				ctx, artifactID.String(), userID.String(), draft.ETag,
				CreateRevisionInput{ChangeSummary: "Forbidden manual revision"},
			); !errors.Is(err, ErrForbidden) {
				t.Fatalf("generic CreateRevision accepted %s: %v", kind, err)
			}

			if _, err := proposals.CreateManifest(
				ctx, projectID.String(), userID.String(), CreateManifestInput{
					JobType: "generic_system_mutation", BaseRevision: &ref,
					Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "proposal/v1",
				},
			); !errors.Is(err, ErrForbidden) {
				t.Fatalf("generic InputManifest accepted system-managed %s base: %v", kind, err)
			}
		})
	}
}

func TestArtifactServiceAllowsEmptyBlueprintWorkflowTargetButRejectsForgedRequirementIDs(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, service, projectID, userID := newArtifactLineageFixture(t, database)
	baselineRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "requirement_baseline", "approved", "current",
		json.RawMessage(`{
			"requirements":[
				{"type":"requirement","requirementId":"REQ-1","priority":"must","acceptanceCriterionIds":["AC-1"]},
				{"type":"acceptanceCriterion","acceptanceCriterionId":"AC-1"}
			]
		}`),
	)
	baselineSource := ArtifactSourceInput{Ref: baselineRef, Purpose: "requirement_baseline", Required: true}
	empty := json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[],"pageSpecs":[]}`)
	created, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "blueprint", Title: "Workflow target", Content: empty,
		SourceVersions: []ArtifactSourceInput{baselineSource},
	})
	if err != nil {
		t.Fatalf("create empty Blueprint workflow target: %v", err)
	}
	updated, err := service.UpdateDraft(ctx, created.Draft.ID, userID.String(), created.Draft.ETag, UpdateDraftInput{
		Content: json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[],"pageSpecs":[],"draftNote":"AI pending"}`),
	})
	if err != nil {
		t.Fatalf("update empty Blueprint workflow target: %v", err)
	}
	if _, err := service.CreateRevision(ctx, created.Artifact.ID, userID.String(), updated.ETag, CreateRevisionInput{
		ChangeSummary: "Initialize Blueprint AI target", ChangeSource: "system",
	}); err != nil {
		t.Fatalf("checkpoint empty Blueprint workflow target: %v", err)
	}

	for name, requirementIDs := range map[string][]string{
		"forged": {"REQ-BOGUS"},
		"mixed":  {"REQ-1", "REQ-BOGUS"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
				Kind: "blueprint", Title: "Invalid " + name,
				Content:        semanticBlueprintFixture(t, requirementIDs),
				SourceVersions: []ArtifactSourceInput{baselineSource},
			}); !errors.Is(err, ErrBlockingGate) {
				t.Fatalf("Blueprint accepted forged Requirement Baseline trace: %v", err)
			}
		})
	}
}

func TestArtifactServiceEnforcesPageSpecLineageOnCreateUpdateAndRevision(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, service, projectID, userID := newArtifactLineageFixture(t, database)
	baselineRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "requirement_baseline", "approved", "current",
		json.RawMessage(`{
			"requirements":[
				{"type":"requirement","requirementId":"REQ-1","priority":"must","acceptanceCriterionIds":["AC-1"]},
				{"type":"acceptanceCriterion","acceptanceCriterionId":"AC-1"}
			]
		}`),
	)
	blueprintPayload := json.RawMessage(`{
		"nodes":[
			{"id":"feature-orders","key":"FEATURE-ORDERS","kind":"feature","title":"Orders"},
			{"id":"page-orders","key":"PAGE-ORDERS","kind":"page","title":"Orders","route":"/orders","userGoal":"Review orders","requirementIds":["REQ-1"]}
		],
		"edges":[{"id":"contains-orders","sourceNodeId":"feature-orders","targetNodeId":"page-orders","kind":"contains"}]
	}`)
	blueprintRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "blueprint", "approved", "current", blueprintPayload,
	)
	seedArtifactLineageRevisionSource(t, database, blueprintRef, baselineRef, "requirement_baseline", true, nil, userID)
	pageAnchor := "page-orders"
	blueprintRef.AnchorID = &pageAnchor
	validSource := ArtifactSourceInput{Ref: blueprintRef, Purpose: "blueprint", Required: true}
	pageSpecPayload := json.RawMessage(`{
		"blueprintPageNodeId":"page-orders","title":"Orders","route":"/orders","userGoal":"Review orders",
		"acceptanceCriterionIds":["AC-1"],"states":[],"dataBindings":[],"interactions":[]
	}`)

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
		"wrong_blueprint_route": {
			Kind: "page_spec", Title: "Wrong route",
			Content:        json.RawMessage(`{"blueprintPageNodeId":"page-orders","route":"/wrong","userGoal":"Review orders"}`),
			SourceVersions: []ArtifactSourceInput{validSource},
		},
		"forged_acceptance_id": {
			Kind: "page_spec", Title: "Forged acceptance",
			Content:        json.RawMessage(`{"blueprintPageNodeId":"page-orders","route":"/orders","userGoal":"Review orders","acceptanceCriterionIds":["AC-BOGUS"]}`),
			SourceVersions: []ArtifactSourceInput{validSource},
		},
		"forged_api_operation": {
			Kind: "page_spec", Title: "Forged operation",
			Content:        json.RawMessage(`{"blueprintPageNodeId":"page-orders","route":"/orders","userGoal":"Review orders","dataBindings":[{"source":"api","operationId":"api-bogus"}]}`),
			SourceVersions: []ArtifactSourceInput{validSource},
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
		Content: json.RawMessage(`{
			"blueprintPageNodeId":"page-orders","title":"Orders updated","route":"/orders","userGoal":"Review orders",
			"acceptanceCriterionIds":["AC-1"],"states":[],"dataBindings":[],"interactions":[]
		}`),
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
	if _, err := service.UpdateDraft(ctx, updated.ID, userID.String(), updated.ETag, UpdateDraftInput{
		Content: json.RawMessage(`{"blueprintPageNodeId":"page-orders","route":"/orders","userGoal":"Review orders","acceptanceCriterionIds":["AC-1","AC-BOGUS"]}`),
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("mixed valid/forged PageSpec acceptance trace bypassed update gate: %v", err)
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

func TestPageSpecFormalReviewRequiresCurrentBlueprintSourcePostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, service, projectID, userID := newArtifactLineageFixture(t, database)
	baselineRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "requirement_baseline", "approved", "current",
		json.RawMessage(`{
  "requirements":[
    {"type":"requirement","requirementId":"REQ-1","priority":"must","acceptanceCriterionIds":["AC-1"]},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-1"}
  ]
}`),
	)
	blueprintPayload := json.RawMessage(`{
  "nodes":[
    {"id":"feature-orders","key":"FEATURE-ORDERS","kind":"feature","requirementIds":["REQ-1"]},
    {"id":"page-orders","key":"PAGE-ORDERS","kind":"page","title":"Orders","route":"/orders","userGoal":"Review orders","requirementIds":["REQ-1"]}
  ],
  "edges":[{"id":"contains-orders","sourceNodeId":"feature-orders","targetNodeId":"page-orders","kind":"contains"}]
}`)
	blueprintRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "blueprint", "approved", "current", blueprintPayload,
	)
	seedArtifactLineageRevisionSource(t, database, blueprintRef, baselineRef, "requirement_baseline", true, nil, userID)
	anchor := "page-orders"
	blueprintRef.AnchorID = &anchor
	sources := []ArtifactSourceInput{{Ref: blueprintRef, Purpose: "blueprint", Required: true}}
	pageSpecPayload := json.RawMessage(`{
  "blueprintPageNodeId":"page-orders","title":"Orders","route":"/orders","userGoal":"Review orders",
  "acceptanceCriterionIds":["AC-1"],"requiredRoles":[],"states":[],"dataBindings":[],"interactions":[]
}`)
	if err := service.validateArtifactLineageForReview(ctx, database, projectID, "page_spec", pageSpecPayload, sources); err != nil {
		t.Fatalf("current Blueprint source was rejected at formal review: %v", err)
	}
	deliverySliceSources := []ArtifactSourceInput{{
		Ref: blueprintRef, Purpose: "delivery_slice_blueprint", Required: true,
	}}
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "page_spec", pageSpecPayload, deliverySliceSources,
	); err != nil {
		t.Fatalf("workflow delivery-slice Blueprint source was rejected at formal review: %v", err)
	}
	if err := database.Model(&storage.ArtifactHealthModel{}).
		Where("artifact_id = ?", blueprintRef.ArtifactID).Update("sync_status", "needs_sync").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.validateArtifactLineageForReview(ctx, database, projectID, "page_spec", pageSpecPayload, sources); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("formal PageSpec accepted a stale Blueprint source: %v", err)
	}
	if err := service.validateArtifactLineage(ctx, database, projectID, "page_spec", pageSpecPayload, sources); err != nil {
		t.Fatalf("draft PageSpec could not retain a stale exact Blueprint source for conflict handling: %v", err)
	}
	if err := database.Model(&storage.ArtifactHealthModel{}).
		Where("artifact_id = ?", blueprintRef.ArtifactID).Update("sync_status", "current").Error; err != nil {
		t.Fatal(err)
	}

	newRevisionID := uuid.New()
	now := time.Now().UTC()
	newerBlueprintPayload := append(append(json.RawMessage(nil), blueprintPayload...), '\n')
	contentRef := store.addFinalized("newer-blueprint-"+newRevisionID.String(), newerBlueprintPayload)
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: newRevisionID, ArtifactID: uuid.MustParse(blueprintRef.ArtifactID), RevisionNumber: 2, SchemaVersion: 1,
		ContentStore: "mongo", ContentRef: contentRef.ID, ContentHash: contentRef.ContentHash, ByteSize: contentRef.ByteSize,
		WorkflowStatus: "approved", ChangeSource: "human", ChangeSummary: "Newer Blueprint",
		CreatedBy: userID, CreatedAt: now, ApprovedAt: &now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", blueprintRef.ArtifactID).Updates(map[string]any{
		"latest_revision_id": newRevisionID, "latest_approved_revision_id": newRevisionID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.validateArtifactLineageForReview(ctx, database, projectID, "page_spec", pageSpecPayload, sources); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("formal PageSpec accepted an older approved Blueprint source: %v", err)
	}
	if err := service.validateArtifactLineage(ctx, database, projectID, "page_spec", pageSpecPayload, sources); err != nil {
		t.Fatalf("draft PageSpec could not retain an older approved Blueprint source: %v", err)
	}
}

func TestArtifactServicePrototypeLineageFormalAndExploratory(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, service, projectID, userID := newArtifactLineageFixture(t, database)
	pageSpecRef := seedArtifactSemanticPageSpecRevision(
		t, database, store, projectID, userID, "approved", "current",
		json.RawMessage(`{
			"blueprintPageNodeId":"page-orders","title":"Orders","route":"/orders","userGoal":"Review orders",
			"acceptanceCriterionIds":["AC-1"],"requiredRoles":["orders-reader"],
			"states":[
				{"id":"state-ready","key":"ready","title":"Ready","required":true,"fixtureIds":["fixture-ready"]},
				{"id":"state-loading","key":"loading","title":"Loading","required":true,"fixtureIds":[]},
				{"id":"state-empty","key":"empty","title":"Empty","required":true,"fixtureIds":[]},
				{"id":"state-error","key":"error","title":"Error","required":true,"fixtureIds":[]}
			],
			"interactions":[{"id":"interaction-open","trigger":"click","outcome":"Open details"}],
			"dataBindings":[{"id":"binding-orders","name":"Orders","source":"api","operationId":"api-orders","required":true}]
		}`),
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
	designSystemRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "design_system", "approved", "current",
		json.RawMessage(`{"tokens":[]}`),
	)
	componentPayload, err := json.Marshal(map[string]any{
		"pageSpecRevision": pageSpecRef, "exploratory": false,
		"layers": map[string]any{"component-layer": map[string]any{
			"id": "component-layer", "kind": "componentInstance", "childIds": []any{},
			"componentRef": designSystemRef,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Unfrozen component ref", Content: componentPayload,
		SourceVersions: []ArtifactSourceInput{pageSpecSource},
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("Prototype accepted a componentRef outside its immutable sources: %v", err)
	}
	if _, err := service.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Frozen component ref", Content: componentPayload,
		SourceVersions: []ArtifactSourceInput{
			pageSpecSource,
			{Ref: designSystemRef, Purpose: "design_system", Required: true},
		},
	}); err != nil {
		t.Fatalf("Prototype rejected an exact immutable design-system componentRef: %v", err)
	}
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", formalPayload, []ArtifactSourceInput{pageSpecSource},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("strict review accepted missing PageSpec semantic coverage: %v", err)
	}
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", prototypeLineageCoveragePayload(t, pageSpecRef),
		[]ArtifactSourceInput{pageSpecSource},
	); err != nil {
		t.Fatalf("strict review rejected complete PageSpec semantic coverage: %v", err)
	}
	deliverySlicePageSpecSource := pageSpecSource
	deliverySlicePageSpecSource.Purpose = "delivery_slice_page_spec"
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", prototypeLineageCoveragePayload(t, pageSpecRef),
		[]ArtifactSourceInput{deliverySlicePageSpecSource},
	); err != nil {
		t.Fatalf("workflow delivery-slice PageSpec source was rejected at formal review: %v", err)
	}
	workflowReviewPageSpecSource := pageSpecSource
	workflowReviewPageSpecSource.Purpose = "workflow_node:custom-page-review"
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", prototypeLineageCoveragePayload(t, pageSpecRef),
		[]ArtifactSourceInput{workflowReviewPageSpecSource},
	); err != nil {
		t.Fatalf("custom workflow PageSpec review output was rejected at formal review: %v", err)
	}
	emptyWorkflowNodeSource := workflowReviewPageSpecSource
	emptyWorkflowNodeSource.Purpose = "workflow_node:"
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", prototypeLineageCoveragePayload(t, pageSpecRef),
		[]ArtifactSourceInput{emptyWorkflowNodeSource},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("empty workflow-node purpose bypassed Prototype PageSpec authority gate: %v", err)
	}
	arbitraryPurposeSource := workflowReviewPageSpecSource
	arbitraryPurposeSource.Purpose = "selected_page_spec"
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", prototypeLineageCoveragePayload(t, pageSpecRef),
		[]ArtifactSourceInput{arbitraryPurposeSource},
	); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("arbitrary PageSpec purpose bypassed Prototype authority gate: %v", err)
	}
	if err := database.Model(&storage.ArtifactRevisionSourceModel{}).
		Where("revision_id = ? AND purpose = ?", pageSpecRef.RevisionID, "blueprint").
		Update("purpose", "delivery_slice_blueprint").Error; err != nil {
		t.Fatal(err)
	}
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", prototypeLineageCoveragePayload(t, pageSpecRef),
		[]ArtifactSourceInput{deliverySlicePageSpecSource},
	); err != nil {
		t.Fatalf("Prototype rejected a PageSpec frozen from a delivery-slice Blueprint source: %v", err)
	}
	if err := database.Model(&storage.ArtifactRevisionSourceModel{}).
		Where("revision_id = ? AND purpose = ?", pageSpecRef.RevisionID, "delivery_slice_blueprint").
		Update("purpose", "blueprint").Error; err != nil {
		t.Fatal(err)
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

	unapprovedRef := seedArtifactSemanticPageSpecRevision(
		t, database, store, projectID, userID, "draft", "needs_sync",
		json.RawMessage(`{"blueprintPageNodeId":"page-orders","title":"Experiment"}`),
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
	if err := service.validateArtifactLineageForReview(
		ctx, database, projectID, "prototype", prototypeLineagePayload(t, unapprovedRef, true),
		[]ArtifactSourceInput{unapprovedSource},
	); !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "exploratory") {
		t.Fatalf("formal review accepted an exploratory Prototype: %v", err)
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

func TestPrototypeApprovalRejectsSemanticallyIncompletePageSpecCoverage(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, ownerID := newArtifactLineageFixture(t, database)
	now := time.Now().UTC()
	reviewerID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: reviewerID, Email: "semantic-reviewer-" + uuid.NewString() + "@example.com",
		DisplayName: "Semantic Reviewer", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectMemberModel{
		ProjectID: projectID, UserID: reviewerID, Role: "editor", JoinedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	pageSpecRef := seedArtifactSemanticPageSpecRevision(
		t, database, store, projectID, ownerID, "approved", "current",
		json.RawMessage(`{
			"blueprintPageNodeId":"page-orders","title":"Orders","route":"/orders","userGoal":"Review orders",
			"acceptanceCriterionIds":["AC-1"],"requiredRoles":["orders-reader"],
			"states":[
				{"id":"state-ready","key":"ready","title":"Ready","required":true,"fixtureIds":["fixture-ready"]},
				{"id":"state-loading","key":"loading","title":"Loading","required":true,"fixtureIds":[]},
				{"id":"state-empty","key":"empty","title":"Empty","required":true,"fixtureIds":[]},
				{"id":"state-error","key":"error","title":"Error","required":true,"fixtureIds":[]}
			],
			"interactions":[{"id":"interaction-open","trigger":"click","outcome":"Open details"}],
			"dataBindings":[{"id":"binding-orders","name":"Orders","source":"api","operationId":"api-orders","required":true}]
		}`),
	)
	incomplete := prototypeLineageReviewIncompletePayload(t, pageSpecRef)
	if report := ValidateArtifactContent("prototype", incomplete); !report.Valid {
		t.Fatalf("test fixture must pass standalone Prototype validation so only cross-artifact semantics block approval: %#v", report.Findings)
	}
	pageSpecSource := ArtifactSourceInput{Ref: pageSpecRef, Purpose: "page_spec", Required: true}
	created, err := artifacts.Create(ctx, projectID.String(), ownerID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Semantically incomplete", Content: incomplete,
		SourceVersions: []ArtifactSourceInput{pageSpecSource},
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, ownerID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Submit incomplete semantic coverage"},
	)
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := NewReviewService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	review, err := reviews.Submit(ctx, projectID.String(), created.Artifact.ID, ownerID.String(), SubmitReviewInput{
		RevisionID: revision.ID, ReviewerIDs: []string{reviewerID.String()}, MinimumApprovals: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviews.Decide(ctx, review.ID, reviewerID.String(), DecideReviewInput{Decision: "approve"}); !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "PageSpec state") {
		t.Fatalf("approval did not fail on authoritative PageSpec semantic coverage: %v", err)
	}
}

func TestArtifactRevisionFreezesAppliedOutputProposalIdentity(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, userID := newArtifactLineageFixture(t, database)
	created, err := artifacts.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "product_requirements", Title: "Requirements", Content: json.RawMessage(`{
  "summary":"Before",
  "blocks":[{
    "id":"requirement-1","type":"requirement","requirementId":"REQ-1",
    "priority":"must","text":"Keep Proposal lineage immutable.",
    "acceptanceCriteria":[{"id":"AC-1","statement":"The revision freezes the applied Proposal identity."}]
  }]
}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := artifacts.CreateRevision(ctx, created.Artifact.ID, userID.String(), created.Draft.ETag, CreateRevisionInput{ChangeSummary: "Initial requirements"})
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := NewProposalService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	baseRef := VersionRef{ArtifactID: base.ArtifactID, RevisionID: base.ID, ContentHash: base.ContentHash}
	manifest, err := proposals.CreateManifest(ctx, projectID.String(), userID.String(), CreateManifestInput{
		JobType: "derive_requirements", BaseRevision: &baseRef,
		Sources:     nil,
		Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "requirements-proposal/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := proposals.CreateProposal(ctx, projectID.String(), userID.String(), CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: created.Artifact.ID,
		Operations: []domain.ProposalOperation{{ID: "summary", Kind: domain.OperationReplace, Path: "/summary", Value: json.RawMessage(`"After"`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = proposals.Decide(ctx, proposal.ID, userID.String(), DecideProposalInput{
		OperationID: "summary", Decision: domain.DecisionAccepted, Version: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := proposals.Apply(ctx, proposal.ID, userID.String(), ApplyProposalInput{Version: proposal.Version})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := artifacts.CreateRevision(ctx, created.Artifact.ID, userID.String(), draft.ETag, CreateRevisionInput{ChangeSummary: "Apply reviewed AI proposal"})
	if err != nil {
		t.Fatal(err)
	}
	if revision.ProposalID == nil || *revision.ProposalID != proposal.ID || revision.SourceManifestID == nil || *revision.SourceManifestID != manifest.ID {
		t.Fatalf("revision did not freeze applied proposal/manifest identity: %+v", revision)
	}
	var dependencyCount int64
	if err := database.Model(&storage.ArtifactDependencyModel{}).Where("target_artifact_id = ?", created.Artifact.ID).Count(&dependencyCount).Error; err != nil {
		t.Fatal(err)
	}
	if dependencyCount != 0 {
		t.Fatalf("base-only transform generated %d self/phantom artifact dependencies", dependencyCount)
	}
}

func TestPrototypeProposalApplyCanonicalizesLegacyContentFromPageSpecReviewOutput(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	store, artifacts, projectID, userID := newArtifactLineageFixture(t, database)
	pageSpecRef := seedArtifactSemanticPageSpecRevision(
		t, database, store, projectID, userID, "approved", "current",
		json.RawMessage(`{
			"blueprintPageNodeId":"page-orders","title":"Orders","route":"/orders","userGoal":"Review orders",
			"acceptanceCriterionIds":["AC-1"],"requiredRoles":["orders-reader"],
			"states":[
				{"id":"state-ready","key":"ready","title":"Ready","required":true,"fixtureIds":["fixture-ready"]},
				{"id":"state-loading","key":"loading","title":"Loading","required":true,"fixtureIds":[]},
				{"id":"state-empty","key":"empty","title":"Empty","required":true,"fixtureIds":[]},
				{"id":"state-error","key":"error","title":"Error","required":true,"fixtureIds":[]}
			],
			"interactions":[{"id":"interaction-open","trigger":"click","outcome":"Open details"}],
			"dataBindings":[{"id":"binding-orders","name":"Orders","source":"api","operationId":"api-orders","required":true}]
		}`),
	)
	pageSpecSource := ArtifactSourceInput{Ref: pageSpecRef, Purpose: "page_spec", Required: true}
	created, err := artifacts.Create(ctx, projectID.String(), userID.String(), CreateArtifactInput{
		Kind: "prototype", Title: "Orders Prototype", Content: prototypeLineagePayload(t, pageSpecRef, false),
		SourceVersions: []ArtifactSourceInput{pageSpecSource},
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, userID.String(), created.Draft.ETag,
		CreateRevisionInput{ChangeSummary: "Initialize Prototype target", ChangeSource: "system"},
	)
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := NewProposalService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	baseRef := VersionRef{ArtifactID: base.ArtifactID, RevisionID: base.ID, ContentHash: base.ContentHash}
	manifest, err := proposals.CreateManifest(ctx, projectID.String(), userID.String(), CreateManifestInput{
		JobType: "generate_prototype", BaseRevision: &baseRef,
		Sources:     []ManifestSourceInput{{Ref: pageSpecRef, Purpose: "workflow_node:custom-page-review"}},
		Constraints: json.RawMessage(`{}`), OutputSchemaVersion: "prototype-proposal/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := proposals.CreateProposal(ctx, projectID.String(), userID.String(), CreateProposalInput{
		ManifestID: manifest.ID, ArtifactID: created.Artifact.ID,
		Operations: []domain.ProposalOperation{{
			ID: "replace-prototype", Kind: domain.OperationReplace, Value: prototypeLegacyLineageCoveragePayload(t, pageSpecRef),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = proposals.Decide(ctx, proposal.ID, userID.String(), DecideProposalInput{
		OperationID: "replace-prototype", Decision: domain.DecisionAccepted, Version: proposal.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	var dirtyContent map[string]any
	if err := json.Unmarshal(prototypeLineagePayload(t, pageSpecRef, false), &dirtyContent); err != nil {
		t.Fatal(err)
	}
	dirtyContent["manualDraftNote"] = "discard only after explicit confirmation"
	dirtyPayload, err := json.Marshal(dirtyContent)
	if err != nil {
		t.Fatal(err)
	}
	dirtyDraft, err := artifacts.UpdateDraft(
		ctx, created.Draft.ID, userID.String(), created.Draft.ETag,
		UpdateDraftInput{Content: dirtyPayload},
	)
	if err != nil {
		t.Fatalf("create divergent working draft: %v", err)
	}
	if _, err := proposals.Apply(
		ctx, proposal.ID, userID.String(), ApplyProposalInput{Version: proposal.Version},
	); !errors.Is(err, ErrProposalStale) {
		t.Fatalf("Proposal silently discarded an unrevisioned draft without confirmation: %v", err)
	}
	if _, err := proposals.Apply(ctx, proposal.ID, userID.String(), ApplyProposalInput{
		Version: proposal.Version, DiscardUnrevisionedChanges: true,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Proposal replacement did not require the exact confirmed draft identity: %v", err)
	}
	dirtyContent["manualDraftNote"] = "saved after the confirmation snapshot"
	dirtyPayload, err = json.Marshal(dirtyContent)
	if err != nil {
		t.Fatal(err)
	}
	latestDirtyDraft, err := artifacts.UpdateDraft(
		ctx, dirtyDraft.ID, userID.String(), dirtyDraft.ETag,
		UpdateDraftInput{Content: dirtyPayload},
	)
	if err != nil {
		t.Fatalf("save a concurrent draft update: %v", err)
	}
	if _, err := proposals.Apply(ctx, proposal.ID, userID.String(), ApplyProposalInput{
		Version: proposal.Version, DiscardUnrevisionedChanges: true,
		ExpectedDraftID: dirtyDraft.ID, ExpectedDraftETag: dirtyDraft.ETag,
		ExpectedDraftContentHash: dirtyDraft.ContentHash,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Proposal replacement overwrote a draft saved after confirmation: %v", err)
	}
	draft, err := proposals.Apply(ctx, proposal.ID, userID.String(), ApplyProposalInput{
		Version: proposal.Version, DiscardUnrevisionedChanges: true,
		ExpectedDraftID: latestDirtyDraft.ID, ExpectedDraftETag: latestDirtyDraft.ETag,
		ExpectedDraftContentHash: latestDirtyDraft.ContentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalizedLegacyPrototype(t, draft.Content)
	var appliedContent map[string]any
	if err := json.Unmarshal(draft.Content, &appliedContent); err != nil {
		t.Fatal(err)
	}
	if _, exists := appliedContent["manualDraftNote"]; exists {
		t.Fatal("explicit Proposal replacement retained the discarded draft-only field")
	}
	var audit storage.AuditEventModel
	if err := database.Where(
		"action = ? AND target_type = ? AND target_id = ?",
		"proposal.applied", "output_proposal", proposal.ID,
	).Order("created_at DESC").Take(&audit).Error; err != nil {
		t.Fatal(err)
	}
	var auditMetadata map[string]any
	if err := json.Unmarshal(audit.Metadata, &auditMetadata); err != nil {
		t.Fatal(err)
	}
	if auditMetadata["discardedUnrevisionedChanges"] != true {
		t.Fatalf("Proposal audit did not record the explicit draft replacement: %#v", auditMetadata)
	}
	if auditMetadata["discardedDraftId"] != latestDirtyDraft.ID ||
		auditMetadata["discardedDraftEtag"] != latestDirtyDraft.ETag ||
		auditMetadata["discardedDraftContentHash"] != latestDirtyDraft.ContentHash ||
		auditMetadata["discardedDraftUpdatedBy"] != latestDirtyDraft.UpdatedBy ||
		auditMetadata["discardedDraftSequence"] != float64(latestDirtyDraft.Sequence) {
		t.Fatalf("Proposal audit did not freeze the discarded draft identity: %#v", auditMetadata)
	}
	if auditMetadata["canonicalizationContract"] != "prototype-proposal-v1" ||
		auditMetadata["reviewedPatchedContentHash"] == "" ||
		auditMetadata["appliedContentHash"] != draft.ContentHash {
		t.Fatalf("Proposal audit did not record the reviewed-to-canonical transform: %#v", auditMetadata)
	}
	var persistedDraft storage.ArtifactDraftModel
	if err := database.Where("id = ?", uuid.MustParse(draft.ID)).Take(&persistedDraft).Error; err != nil {
		t.Fatal(err)
	}
	storedDraft, err := store.Get(ctx, persistedDraft.ContentRef, persistedDraft.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalizedLegacyPrototype(t, storedDraft.Payload)
	if len(draft.SourceVersions) != 1 || draft.SourceVersions[0].Purpose != "workflow_node:custom-page-review" {
		t.Fatalf("Proposal apply did not preserve the frozen PageSpec review source: %#v", draft.SourceVersions)
	}
	revision, err := artifacts.CreateRevision(
		ctx, created.Artifact.ID, userID.String(), draft.ETag,
		CreateRevisionInput{ChangeSummary: "Apply reviewed Prototype proposal", ChangeSource: "ai_proposal"},
	)
	if err != nil {
		t.Fatalf("CreateRevision rejected the exact workflow PageSpec review source: %v", err)
	}
	if revision.ProposalID == nil || *revision.ProposalID != proposal.ID ||
		revision.SourceManifestID == nil || *revision.SourceManifestID != manifest.ID ||
		len(revision.SourceVersions) != 1 || revision.SourceVersions[0].Purpose != "workflow_node:custom-page-review" {
		t.Fatalf("Prototype revision lost its Proposal/Manifest/PageSpec review lineage: %+v", revision)
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

func seedArtifactLineageRevisionSource(
	t *testing.T,
	database *gorm.DB,
	target VersionRef,
	source VersionRef,
	purpose string,
	required bool,
	anchorID *string,
	addedBy uuid.UUID,
) {
	t.Helper()
	if err := database.Create(&storage.ArtifactRevisionSourceModel{
		RevisionID: uuid.MustParse(target.RevisionID), Ordinal: 0,
		SourceArtifactID: uuid.MustParse(source.ArtifactID), SourceRevisionID: uuid.MustParse(source.RevisionID),
		SourceContentHash: source.ContentHash, SourceAnchorID: cloneStringPointer(anchorID),
		Purpose: purpose, Required: required, AddedBy: addedBy, AddedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func seedArtifactSemanticPageSpecRevision(
	t *testing.T,
	database *gorm.DB,
	store *baselineContentStoreSpy,
	projectID uuid.UUID,
	userID uuid.UUID,
	workflowStatus string,
	syncStatus string,
	pageSpecPayload json.RawMessage,
) VersionRef {
	t.Helper()
	baselineRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "requirement_baseline", "approved", "current",
		json.RawMessage(`{
  "requirements":[
    {"type":"requirement","requirementId":"REQ-1","priority":"must","acceptanceCriterionIds":["AC-1"]},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-1"}
  ]
}`),
	)
	blueprintRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "blueprint", "approved", "current",
		json.RawMessage(`{
  "nodes":[
    {"id":"feature-orders","key":"FEATURE-ORDERS","kind":"feature","requirementIds":["REQ-1"]},
    {"id":"page-orders","key":"PAGE-ORDERS","kind":"page","title":"Orders","route":"/orders","userGoal":"Review orders","requirementIds":["REQ-1"]},
    {"id":"api-orders","key":"API-ORDERS","kind":"apiOperation","method":"GET","path":"/api/orders","requirementIds":["REQ-1"]},
    {"id":"permission-orders","key":"PERMISSION-ORDERS","kind":"permission","roles":["orders-reader"],"requirementIds":["REQ-1"]}
  ],
  "edges":[
    {"id":"contains-orders","sourceNodeId":"feature-orders","targetNodeId":"page-orders","kind":"contains","required":true},
    {"id":"calls-orders","sourceNodeId":"page-orders","targetNodeId":"api-orders","kind":"calls","required":true},
    {"id":"protect-orders","sourceNodeId":"api-orders","targetNodeId":"permission-orders","kind":"requires","required":true}
  ]
}`),
	)
	seedArtifactLineageRevisionSource(
		t, database, blueprintRef, baselineRef, "requirement_baseline", true, nil, userID,
	)
	pageSpecRef := seedArtifactLineageRevision(
		t, database, store, projectID, userID, "page_spec", workflowStatus, syncStatus, pageSpecPayload,
	)
	anchor := "page-orders"
	seedArtifactLineageRevisionSource(
		t, database, pageSpecRef, blueprintRef, "blueprint", true, &anchor, userID,
	)
	return pageSpecRef
}

func prototypeLineagePayload(t *testing.T, pageSpec VersionRef, exploratory bool) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"pageSpecRevision":  pageSpec,
		"exploratory":       exploratory,
		"states":            []any{},
		"breakpoints":       []any{},
		"layers":            map[string]any{},
		"frames":            []any{},
		"overrides":         []any{},
		"interactions":      []any{},
		"fixtures":          []any{},
		"tokenBindings":     []any{},
		"componentBindings": []any{},
		"assets":            []any{},
		"traceLinks":        []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func prototypeLineageCoveragePayload(t *testing.T, pageSpec VersionRef) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"pageSpecRevision": pageSpec,
		"exploratory":      false,
		"states": []any{
			map[string]any{"id": "state-ready", "key": "ready", "title": "Ready", "required": true, "fixtureIds": []any{"fixture-ready"}, "pageStateId": "state-ready"},
			map[string]any{"id": "state-loading", "key": "loading", "title": "Loading", "required": true, "fixtureIds": []any{}},
			map[string]any{"id": "state-empty", "key": "empty", "title": "Empty", "required": true, "fixtureIds": []any{}},
			map[string]any{"id": "state-error", "key": "error", "title": "Error", "required": true, "fixtureIds": []any{}},
		},
		"breakpoints": []any{
			map[string]any{"id": "breakpoint-desktop", "name": "desktop", "minWidth": 1024, "viewportWidth": 1440, "viewportHeight": 900},
			map[string]any{"id": "breakpoint-tablet", "name": "tablet", "minWidth": 768, "maxWidth": 1023, "viewportWidth": 768, "viewportHeight": 1024},
			map[string]any{"id": "breakpoint-mobile", "name": "mobile", "minWidth": 0, "maxWidth": 767, "viewportWidth": 390, "viewportHeight": 844},
		},
		"frames": prototypeLineageFrames(),
		"fixtures": []any{map[string]any{
			"id": "fixture-ready", "name": "Ready orders", "stateId": "state-ready",
			"response": map[string]any{"orders": []any{}}, "statusCode": 200, "latencyMs": 0,
			"sanitized": true, "contentHash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		"interactions": []any{
			map[string]any{
				"id": "interaction-open", "sourceLayerId": "layer-orders", "trigger": "click",
				"actions": []any{
					map[string]any{"type": "updateBinding", "bindingId": "binding-orders", "value": map[string]any{}},
				},
			},
		},
		"layers": map[string]any{"layer-orders": map[string]any{
			"id": "layer-orders", "kind": "frame", "name": "Orders", "childIds": []any{}, "dataBindingId": "binding-orders",
			"layout": map[string]any{"x": 0, "y": 0, "width": 1440, "height": 900}, "style": map[string]any{}, "properties": map[string]any{},
			"requirementIds": []any{}, "acceptanceCriterionIds": []any{"AC-1"}, "fieldMetadata": map[string]any{},
		}},
		"overrides":         []any{},
		"tokenBindings":     []any{},
		"componentBindings": []any{},
		"assets":            []any{},
		"traceLinks":        []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func prototypeLegacyLineageCoveragePayload(t *testing.T, pageSpec VersionRef) json.RawMessage {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal(prototypeLineageCoveragePayload(t, pageSpec), &content); err != nil {
		t.Fatal(err)
	}
	for _, item := range content["breakpoints"].([]any) {
		breakpoint := item.(map[string]any)
		name := firstString(breakpoint, "name")
		breakpoint["key"] = name
		breakpoint["title"] = strings.ToUpper(name[:1]) + name[1:]
		breakpoint["width"] = breakpoint["viewportWidth"]
		breakpoint["height"] = breakpoint["viewportHeight"]
		breakpoint["legacyScale"] = "preserved"
		delete(breakpoint, "name")
		delete(breakpoint, "minWidth")
		delete(breakpoint, "maxWidth")
		delete(breakpoint, "viewportWidth")
		delete(breakpoint, "viewportHeight")
	}
	layer := content["layers"].(map[string]any)["layer-orders"].(map[string]any)
	layer["type"] = "screen"
	layer["props"] = map[string]any{"role": "main", "legacyCopy": "preserved"}
	layer["legacyPluginField"] = "preserved"
	for _, field := range []string{
		"kind", "layout", "style", "properties", "requirementIds",
		"acceptanceCriterionIds", "fieldMetadata",
	} {
		delete(layer, field)
	}
	for _, item := range content["frames"].([]any) {
		delete(item.(map[string]any), "title")
	}
	for _, field := range []string{"overrides", "tokenBindings", "componentBindings", "assets", "traceLinks"} {
		delete(content, field)
	}
	content["legacyExtension"] = map[string]any{"preserved": true}
	payload, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertCanonicalizedLegacyPrototype(t *testing.T, payload json.RawMessage) {
	t.Helper()
	if report := ValidateArtifactContent("prototype", payload); !report.Valid {
		t.Fatalf("persisted legacy Prototype was not canonical: %#v", report.Findings)
	}
	var content map[string]any
	if err := json.Unmarshal(payload, &content); err != nil {
		t.Fatal(err)
	}
	if len(content["states"].([]any)) != 4 || len(content["frames"].([]any)) != 12 ||
		len(content["layers"].(map[string]any)) != 1 {
		t.Fatal("Prototype compatibility migration invented or discarded semantic states, frames, or layers")
	}
	for _, field := range []string{"overrides", "tokenBindings", "componentBindings", "assets", "traceLinks"} {
		if collection, ok := content[field].([]any); !ok || len(collection) != 0 {
			t.Fatalf("canonical auxiliary collection %s was not persisted as an empty array: %#v", field, content[field])
		}
	}
	breakpoint := content["breakpoints"].([]any)[0].(map[string]any)
	if breakpoint["id"] != "breakpoint-desktop" || breakpoint["name"] != "Desktop" ||
		breakpoint["minWidth"] != float64(1024) || breakpoint["viewportWidth"] != float64(1440) ||
		breakpoint["viewportHeight"] != float64(900) || breakpoint["key"] != "desktop" ||
		breakpoint["legacyScale"] != "preserved" {
		t.Fatalf("legacy breakpoint was not canonicalized while preserving aliases: %#v", breakpoint)
	}
	layer := content["layers"].(map[string]any)["layer-orders"].(map[string]any)
	properties := layer["properties"].(map[string]any)
	if layer["kind"] != "frame" || layer["name"] != "Orders" || properties["role"] != "main" ||
		properties["legacyCopy"] != "preserved" || layer["legacyPluginField"] != "preserved" {
		t.Fatalf("legacy layer aliases or unknown fields were not preserved canonically: %#v", layer)
	}
	for _, field := range []string{"layout", "style", "fieldMetadata"} {
		if _, ok := layer[field].(map[string]any); !ok {
			t.Fatalf("canonical layer object %s is missing: %#v", field, layer[field])
		}
	}
	layout := layer["layout"].(map[string]any)
	if _, validX := nonNegativeInteger(layout["x"]); !validX {
		t.Fatalf("legacy layer did not receive a renderable x coordinate: %#v", layout)
	}
	if _, validY := nonNegativeInteger(layout["y"]); !validY {
		t.Fatalf("legacy layer did not receive a renderable y coordinate: %#v", layout)
	}
	if _, validWidth := positiveInteger(layout["width"]); !validWidth {
		t.Fatalf("legacy layer did not receive a renderable width: %#v", layout)
	}
	if _, validHeight := positiveInteger(layout["height"]); !validHeight {
		t.Fatalf("legacy layer did not receive a renderable height: %#v", layout)
	}
	for _, field := range []string{"childIds", "requirementIds", "acceptanceCriterionIds"} {
		if _, ok := layer[field].([]any); !ok {
			t.Fatalf("canonical layer array %s is missing: %#v", field, layer[field])
		}
	}
	frame := content["frames"].([]any)[0].(map[string]any)
	if frame["title"] != "Ready · Desktop" {
		t.Fatalf("legacy frame title was not derived deterministically: %#v", frame)
	}
	legacyExtension, ok := content["legacyExtension"].(map[string]any)
	if !ok || legacyExtension["preserved"] != true {
		t.Fatalf("unknown top-level fields were discarded: %#v", content["legacyExtension"])
	}
}

func prototypeLineageFrames() []any {
	frames := make([]any, 0, 12)
	for _, stateID := range []string{"state-ready", "state-loading", "state-empty", "state-error"} {
		for _, breakpointID := range []string{"breakpoint-desktop", "breakpoint-tablet", "breakpoint-mobile"} {
			frames = append(frames, map[string]any{
				"id": stateID + "-" + breakpointID, "stateId": stateID,
				"breakpointId": breakpointID, "rootLayerId": "layer-orders",
				"title": stateID + " · " + breakpointID,
			})
		}
	}
	return frames
}

func prototypeLineageReviewIncompletePayload(t *testing.T, pageSpec VersionRef) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"pageSpecRevision": pageSpec,
		"exploratory":      false,
		"states": []any{
			map[string]any{"id": "state-ready", "key": "ready", "title": "Ready", "required": true, "fixtureIds": []any{}},
		},
		"breakpoints": []any{
			map[string]any{"id": "breakpoint-desktop", "name": "desktop", "minWidth": 1024, "viewportWidth": 1440, "viewportHeight": 900},
			map[string]any{"id": "breakpoint-tablet", "name": "tablet", "minWidth": 768, "maxWidth": 1023, "viewportWidth": 768, "viewportHeight": 1024},
			map[string]any{"id": "breakpoint-mobile", "name": "mobile", "minWidth": 0, "maxWidth": 767, "viewportWidth": 390, "viewportHeight": 844},
		},
		"layers": map[string]any{
			"layer-root": map[string]any{
				"id": "layer-root", "childIds": []any{}, "kind": "frame", "name": "Page",
				"layout": map[string]any{"x": 0, "y": 0, "width": 1440, "height": 900}, "style": map[string]any{}, "properties": map[string]any{},
				"requirementIds": []any{}, "acceptanceCriterionIds": []any{}, "fieldMetadata": map[string]any{},
			},
		},
		"frames": []any{
			map[string]any{"id": "frame-desktop", "stateId": "state-ready", "breakpointId": "breakpoint-desktop", "rootLayerId": "layer-root", "title": "Ready · Desktop"},
			map[string]any{"id": "frame-tablet", "stateId": "state-ready", "breakpointId": "breakpoint-tablet", "rootLayerId": "layer-root", "title": "Ready · Tablet"},
			map[string]any{"id": "frame-mobile", "stateId": "state-ready", "breakpointId": "breakpoint-mobile", "rootLayerId": "layer-root", "title": "Ready · Mobile"},
		},
		"overrides":         []any{},
		"fixtures":          []any{},
		"interactions":      []any{},
		"tokenBindings":     []any{},
		"componentBindings": []any{},
		"assets":            []any{},
		"traceLinks":        []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
