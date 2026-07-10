package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"gorm.io/gorm"
)

func TestBlueprintSelectionManifestsStayIsolatedPostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()

	ownerID := seedMultiBundleUser(t, database, "selection-owner")
	viewerID := seedMultiBundleUser(t, database, "selection-viewer")
	projectID := seedMultiBundleProject(t, database, ownerID, "selection")
	seedMultiBundleMembership(t, database, projectID, ownerID, RoleOwner)
	seedMultiBundleMembership(t, database, projectID, viewerID, RoleViewer)
	contents := newMultiBundleMemoryContentStore()
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := NewProposalService(database, contents, access)
	if err != nil {
		t.Fatal(err)
	}

	requirements := seedMultiBundleApprovedRevision(
		t, database, contents, projectID, ownerID, "product_requirements",
		json.RawMessage(`{"requirements":[{"id":"REQ-1","statement":"Build two pages"}]}`), nil,
	)
	blueprint := seedMultiBundleApprovedRevision(
		t, database, contents, projectID, ownerID, "blueprint",
		json.RawMessage(`{
			"semantic":{"nodes":[
				{"id":"feature-orders","key":"FEATURE-ORDERS","kind":"feature","title":"Orders","requirementIds":["REQ-1"]},
				{"id":"page-orders","key":"PAGE-ORDERS","kind":"page","title":"Orders page","requirementIds":["REQ-1"]},
				{"id":"page-history","key":"PAGE-HISTORY","kind":"page","title":"History page","requirementIds":["REQ-1"]}
			],"edges":[
				{"id":"edge-orders","sourceNodeId":"feature-orders","targetNodeId":"page-orders","kind":"contains","required":true},
				{"id":"edge-history","sourceNodeId":"feature-orders","targetNodeId":"page-history","kind":"contains","required":true}
			]}
		}`), nil,
	)
	seedBlueprintSelectionRevisionSource(t, database, ownerID, blueprint, requirements, nil, "requirements")

	pageOrders := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "page_spec", json.RawMessage(`{"blueprintPageNodeId":"page-orders","title":"Orders"}`), nil)
	pageHistory := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "page_spec", json.RawMessage(`{"blueprintPageNodeId":"page-history","title":"History"}`), nil)
	ordersAnchor, historyAnchor := "page-orders", "page-history"
	seedBlueprintSelectionRevisionSource(t, database, ownerID, pageOrders, blueprint, &ordersAnchor, "blueprint")
	seedBlueprintSelectionRevisionSource(t, database, ownerID, pageHistory, blueprint, &historyAnchor, "blueprint")

	prototypeOrders := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "prototype", json.RawMessage(`{"frames":[],"marker":"orders"}`), nil)
	prototypeHistory := seedMultiBundleApprovedRevision(t, database, contents, projectID, ownerID, "prototype", json.RawMessage(`{"frames":[],"marker":"history"}`), nil)
	seedBlueprintSelectionRevisionSource(t, database, ownerID, prototypeOrders, pageOrders, nil, "page_spec")
	seedBlueprintSelectionRevisionSource(t, database, ownerID, prototypeHistory, pageHistory, nil, "page_spec")

	compile := func(nodeID string) domainInputManifestResult {
		manifest, compileErr := proposals.CreateManifest(
			context.Background(), projectID.String(), ownerID.String(), CreateManifestInput{
				JobType: BlueprintSelectionJobType,
				BlueprintSelection: &BlueprintSelectionInput{
					BlueprintRevision: blueprint,
					NodeIDs:           []string{nodeID},
				},
				ExpectedBlueprintETag: artifactETag(uuid.MustParse(blueprint.ArtifactID), 1),
			},
		)
		if compileErr != nil {
			t.Fatalf("compile selection %s: %v", nodeID, compileErr)
		}
		var constraints struct {
			BlueprintSelection BlueprintSelectionScope `json:"blueprintSelection"`
		}
		if err := json.Unmarshal(manifest.Constraints, &constraints); err != nil {
			t.Fatal(err)
		}
		return domainInputManifestResult{ManifestID: manifest.ID, Hash: manifest.Hash, Scope: constraints.BlueprintSelection, Sources: manifest.Sources}
	}

	orders := compile("page-orders")
	history := compile("page-history")
	if orders.ManifestID == history.ManifestID || orders.Hash == history.Hash || orders.Scope.SelectionID == history.Scope.SelectionID {
		t.Fatalf("different selections collapsed to one immutable identity: orders=%#v history=%#v", orders, history)
	}
	assertBlueprintSelectionBinding(t, orders, "page-orders", pageOrders, prototypeOrders)
	assertBlueprintSelectionBinding(t, history, "page-history", pageHistory, prototypeHistory)
	if selectionContainsExactRef(orders.Sources, pageHistory) || selectionContainsExactRef(orders.Sources, prototypeHistory) ||
		selectionContainsExactRef(history.Sources, pageOrders) || selectionContainsExactRef(history.Sources, prototypeOrders) {
		t.Fatal("selection manifests leaked PageSpec or Prototype refs across independent scopes")
	}
	parent, err := proposals.GetManifest(context.Background(), orders.ManifestID, ownerID.String())
	if err != nil {
		t.Fatal(err)
	}
	derivedSources := make([]ManifestSourceInput, 0, len(parent.Sources))
	for _, source := range parent.Sources {
		ref := VersionRef{ArtifactID: source.Ref.ArtifactID, RevisionID: source.Ref.RevisionID, ContentHash: source.Ref.ContentHash}
		if source.Ref.AnchorID != "" {
			anchor := source.Ref.AnchorID
			ref.AnchorID = &anchor
		}
		derivedSources = append(derivedSources, ManifestSourceInput{Ref: ref, Purpose: "approved_upstream"})
	}
	derivedConstraints, err := domain.CanonicalJSON(map[string]any{
		"instruction":             "Generate selection documentation",
		"parentSelectionManifest": parent.Ref(),
		"frozenSelectionScope":    orders.Scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.CreateManifest(
		context.Background(), projectID.String(), ownerID.String(), CreateManifestInput{
			JobType: SelectionDocumentationJobType, Sources: derivedSources,
			Constraints:         json.RawMessage(`{"instruction":"forged without parent"}`),
			OutputSchemaVersion: "selection-document-proposal/v1",
		},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("selection documentation manifest without parent was accepted: %v", err)
	}
	derived, err := proposals.CreateManifest(
		context.Background(), projectID.String(), ownerID.String(), CreateManifestInput{
			JobType: SelectionDocumentationJobType, Sources: derivedSources,
			Constraints: derivedConstraints, OutputSchemaVersion: "selection-document-proposal/v1",
		},
	)
	if err != nil {
		t.Fatalf("create selection-derived AI manifest: %v", err)
	}
	if derived.ID == parent.ID || !json.Valid(derived.Constraints) {
		t.Fatalf("derived selection manifest = %#v", derived)
	}
	tamperedScope := orders.Scope
	tamperedScope.NodeIDs = []string{"page-history"}
	tamperedConstraints, _ := domain.CanonicalJSON(map[string]any{
		"parentSelectionManifest": parent.Ref(), "frozenSelectionScope": tamperedScope,
	})
	if _, err := proposals.CreateManifest(
		context.Background(), projectID.String(), ownerID.String(), CreateManifestInput{
			JobType: SelectionDocumentationJobType, Sources: derivedSources,
			Constraints: tamperedConstraints, OutputSchemaVersion: "selection-document-proposal/v1",
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered frozen selection scope created a derived AI manifest: %v", err)
	}

	if _, err := proposals.CreateManifest(
		context.Background(), projectID.String(), viewerID.String(), CreateManifestInput{
			JobType:               BlueprintSelectionJobType,
			BlueprintSelection:    &BlueprintSelectionInput{BlueprintRevision: blueprint, NodeIDs: []string{"page-orders"}},
			ExpectedBlueprintETag: artifactETag(uuid.MustParse(blueprint.ArtifactID), 1),
		},
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer compiled a Blueprint selection: %v", err)
	}

	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", blueprint.ArtifactID).Update("version", 2).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.CreateManifest(
		context.Background(), projectID.String(), ownerID.String(), CreateManifestInput{
			JobType:               BlueprintSelectionJobType,
			BlueprintSelection:    &BlueprintSelectionInput{BlueprintRevision: blueprint, NodeIDs: []string{"page-orders"}},
			ExpectedBlueprintETag: artifactETag(uuid.MustParse(blueprint.ArtifactID), 1),
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Blueprint ETag compiled a selection: %v", err)
	}

	var audits, outbox int64
	if err := database.Model(&storage.AuditEventModel{}).Where("project_id = ? AND action = 'blueprint.selection.compiled'", projectID).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&storage.OutboxEventModel{}).Where("event_type = 'blueprint.selection.compiled'").Count(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 2 || outbox != 2 {
		t.Fatalf("selection audit/outbox counts = %d/%d, want 2/2", audits, outbox)
	}
}

type domainInputManifestResult struct {
	ManifestID string
	Hash       string
	Scope      BlueprintSelectionScope
	Sources    []domain.ManifestSource
}

func seedBlueprintSelectionRevisionSource(
	t *testing.T,
	database *gorm.DB,
	actorID uuid.UUID,
	target VersionRef,
	source VersionRef,
	anchorID *string,
	purpose string,
) {
	t.Helper()
	model := storage.ArtifactRevisionSourceModel{
		RevisionID: uuid.MustParse(target.RevisionID), Ordinal: 0,
		SourceArtifactID: uuid.MustParse(source.ArtifactID), SourceRevisionID: uuid.MustParse(source.RevisionID),
		SourceContentHash: source.ContentHash, SourceAnchorID: anchorID, Purpose: purpose,
		Required: true, AddedBy: actorID, AddedAt: time.Now().UTC(),
	}
	if err := database.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
}

func assertBlueprintSelectionBinding(t *testing.T, result domainInputManifestResult, nodeID string, pageSpec, prototype VersionRef) {
	t.Helper()
	if len(result.Scope.NodeIDs) != 1 || result.Scope.NodeIDs[0] != nodeID || len(result.Scope.PageBindings) != 1 {
		t.Fatalf("selection scope for %s = %#v", nodeID, result.Scope)
	}
	binding := result.Scope.PageBindings[0]
	if binding.NodeID != nodeID || binding.PageSpec == nil || binding.Prototype == nil ||
		binding.PageSpec.RevisionID != pageSpec.RevisionID || binding.Prototype.RevisionID != prototype.RevisionID {
		t.Fatalf("selection binding for %s = %#v", nodeID, binding)
	}
}

func selectionContainsExactRef(sources []domain.ManifestSource, ref VersionRef) bool {
	for _, source := range sources {
		if source.Ref.ArtifactID == ref.ArtifactID && source.Ref.RevisionID == ref.RevisionID && source.Ref.ContentHash == ref.ContentHash {
			return true
		}
	}
	return false
}
