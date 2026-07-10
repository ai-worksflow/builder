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

func TestRevisionSourcesFreezePurposeRequiredAndAnchor(t *testing.T) {
	t.Parallel()

	revisionID := uuid.New()
	sourceArtifactID := uuid.New()
	sourceRevisionID := uuid.New()
	actorID := uuid.New()
	firstAnchor := "page-orders"
	secondAnchor := "page-order-detail"
	addedAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	draftSources := []storage.ArtifactDraftSourceModel{
		{
			DraftID: uuid.New(), SourceArtifactID: sourceArtifactID,
			SourceRevisionID: sourceRevisionID, SourceContentHash: "sha256:blueprint",
			SourceAnchorID: &firstAnchor, Purpose: "blueprint_page", Required: true,
			AddedBy: actorID, AddedAt: addedAt,
		},
		{
			DraftID: uuid.New(), SourceArtifactID: sourceArtifactID,
			SourceRevisionID: sourceRevisionID, SourceContentHash: "sha256:blueprint",
			SourceAnchorID: &secondAnchor, Purpose: "navigation_target", Required: false,
			AddedBy: actorID, AddedAt: addedAt,
		},
	}

	frozen := revisionSourceModelsFromDraft(revisionID, draftSources)
	firstAnchor = "mutated-after-freeze"
	secondAnchor = "mutated-after-freeze"

	if len(frozen) != 2 {
		t.Fatalf("expected both purposes to be frozen, got %d", len(frozen))
	}
	if frozen[0].Ordinal != 0 || frozen[1].Ordinal != 1 {
		t.Fatalf("expected stable source ordinals, got %d and %d", frozen[0].Ordinal, frozen[1].Ordinal)
	}
	if frozen[0].Purpose != "blueprint_page" || !frozen[0].Required || frozen[0].SourceAnchorID == nil || *frozen[0].SourceAnchorID != "page-orders" {
		t.Fatalf("first frozen source lost exact metadata: %#v", frozen[0])
	}
	if frozen[1].Purpose != "navigation_target" || frozen[1].Required || frozen[1].SourceAnchorID == nil || *frozen[1].SourceAnchorID != "page-order-detail" {
		t.Fatalf("second frozen source lost exact metadata: %#v", frozen[1])
	}

	sources := revisionSourcesFromModels(frozen)
	revision := revisionFromModel(storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: uuid.New(), RevisionNumber: 3,
		ContentHash: "sha256:page-spec", WorkflowStatus: "draft",
		CreatedBy: actorID, CreatedAt: addedAt,
	}, json.RawMessage(`{"title":"Orders"}`), sources)
	payload, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	var encoded struct {
		SourceVersions []map[string]any `json:"sourceVersions"`
	}
	if err := json.Unmarshal(payload, &encoded); err != nil {
		t.Fatal(err)
	}
	if len(encoded.SourceVersions) != 2 {
		t.Fatalf("expected two projected revision sources, got %#v", encoded.SourceVersions)
	}
	if encoded.SourceVersions[0]["artifactId"] != sourceArtifactID.String() ||
		encoded.SourceVersions[0]["revisionId"] != sourceRevisionID.String() ||
		encoded.SourceVersions[0]["contentHash"] != "sha256:blueprint" ||
		encoded.SourceVersions[0]["anchorId"] != "page-orders" ||
		encoded.SourceVersions[0]["purpose"] != "blueprint_page" ||
		encoded.SourceVersions[0]["required"] != true {
		t.Fatalf("revision JSON is not a flat exact source ref: %#v", encoded.SourceVersions[0])
	}
	if _, nested := encoded.SourceVersions[0]["version"]; nested {
		t.Fatalf("revision source must be flat, got %#v", encoded.SourceVersions[0])
	}
}

func TestRevisionLineageDeduplicatesDependencyWithoutLosingAnchors(t *testing.T) {
	t.Parallel()

	sourceArtifactID := uuid.New()
	sourceRevisionID := uuid.New()
	firstAnchor := "page-orders"
	secondAnchor := "page-order-detail"
	sources := []storage.ArtifactDraftSourceModel{
		{SourceArtifactID: sourceArtifactID, SourceRevisionID: sourceRevisionID, SourceContentHash: "sha256:blueprint", SourceAnchorID: &firstAnchor, Purpose: "primary", Required: false},
		{SourceArtifactID: sourceArtifactID, SourceRevisionID: sourceRevisionID, SourceContentHash: "sha256:blueprint", SourceAnchorID: &secondAnchor, Purpose: "secondary", Required: true},
		{SourceArtifactID: sourceArtifactID, SourceRevisionID: sourceRevisionID, SourceContentHash: "sha256:blueprint", SourceAnchorID: &firstAnchor, Purpose: "duplicate_anchor", Required: false},
	}

	dependencies, links := revisionLineageModelsFromDraft(
		uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now().UTC(), sources,
	)
	if len(dependencies) != 1 {
		t.Fatalf("same source revision with multiple purposes must create one dependency, got %d", len(dependencies))
	}
	if !dependencies[0].Required {
		t.Fatal("dependency must be required when any frozen purpose is required")
	}
	if len(links) != 3 {
		t.Fatalf("dependency must have whole-source coverage plus both exact anchors, got %d links", len(links))
	}
	anchors := map[string]bool{}
	wholeSourceCovered := false
	for _, link := range links {
		if link.SourceAnchorID == nil {
			wholeSourceCovered = true
			continue
		}
		anchors[*link.SourceAnchorID] = true
	}
	if !wholeSourceCovered || !anchors["page-orders"] || !anchors["page-order-detail"] {
		t.Fatalf("unexpected whole-source and exact-anchor trace coverage: %#v", links)
	}
}

func TestRevisionLineageDoesNotCreateSelfArtifactDependency(t *testing.T) {
	t.Parallel()
	artifactID := uuid.New()
	anchor := "page-orders"
	dependencies, links := revisionLineageModelsFromDraft(
		uuid.New(), artifactID, uuid.New(), uuid.New(), time.Now().UTC(),
		[]storage.ArtifactDraftSourceModel{{
			SourceArtifactID: artifactID, SourceRevisionID: uuid.New(),
			SourceContentHash: "sha256:prior", SourceAnchorID: &anchor,
			Purpose: "editable_base", Required: true,
		}},
	)
	if len(dependencies) != 0 {
		t.Fatalf("same-artifact revision ancestry created an invalid artifact dependency: %+v", dependencies)
	}
	if len(links) != 1 || links[0].SourceAnchorID == nil || *links[0].SourceAnchorID != anchor {
		t.Fatalf("skipping the self dependency lost exact anchor traceability: %+v", links)
	}
}

func TestBlueprintRequiresOneCurrentApprovedRequirementBaseline(t *testing.T) {
	t.Parallel()

	service := &ArtifactService{}
	projectID := uuid.New()
	anchor := "REQ-001"
	for name, sources := range map[string][]ArtifactSourceInput{
		"missing": nil,
		"multiple": {
			{Ref: VersionRef{}, Required: true},
			{Ref: VersionRef{}, Required: true},
		},
		"optional": {{Ref: VersionRef{}, Required: false}},
		"anchored": {{Ref: VersionRef{AnchorID: &anchor}, Required: true}},
	} {
		t.Run(name, func(t *testing.T) {
			err := service.validateBlueprintBaselineSources(context.Background(), nil, projectID, sources)
			if !errors.Is(err, ErrBlockingGate) {
				t.Fatalf("expected Blueprint baseline gate, got %v", err)
			}
		})
	}

	artifactID := uuid.New()
	revisionID := uuid.New()
	revisionHash := "sha256:baseline"
	artifact := storage.ArtifactModel{
		ID: artifactID, Kind: "requirement_baseline", Lifecycle: "active",
		LatestApprovedRevisionID: &revisionID,
	}
	revision := storage.ArtifactRevisionModel{
		ID: revisionID, ArtifactID: artifactID, ContentHash: revisionHash,
		WorkflowStatus: "approved",
	}
	reference := VersionRef{
		ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: revisionHash,
	}
	if !isCurrentApprovedRequirementBaseline(artifact, revision, reference) {
		t.Fatal("expected the exact current approved Requirement Baseline to pass")
	}
	staleID := uuid.New()
	artifact.LatestApprovedRevisionID = &staleID
	if isCurrentApprovedRequirementBaseline(artifact, revision, reference) {
		t.Fatal("a stale Requirement Baseline revision must not satisfy Blueprint creation")
	}
	artifact.LatestApprovedRevisionID = &revisionID
	artifact.Kind = "product_requirements"
	if isCurrentApprovedRequirementBaseline(artifact, revision, reference) {
		t.Fatal("ordinary approved documents must not bypass the Requirement Baseline gate")
	}
}
