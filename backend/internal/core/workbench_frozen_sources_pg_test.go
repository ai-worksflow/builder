package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestWorkbenchFrozenSourcesIgnoreMutableDependenciesPostgres(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	store, _, projectID, ownerID := newArtifactLineageFixture(t, database)
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	workbench, err := NewWorkbenchService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}
	pageSpec := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "page_spec", "approved", "current",
		json.RawMessage(`{"title":"Page"}`),
	)
	blueprint := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "blueprint", "approved", "current",
		json.RawMessage(`{"title":"Blueprint"}`),
	)
	prototype := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "prototype", "approved", "current",
		json.RawMessage(`{"title":"Prototype"}`),
	)
	seedArtifactLineageRevisionSource(t, database, pageSpec, blueprint, "blueprint", true, nil, ownerID)
	seedArtifactLineageRevisionSource(t, database, prototype, pageSpec, "page_spec", true, nil, ownerID)

	injected := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "design_system", "approved", "current",
		json.RawMessage(`{"title":"Injected after approval"}`),
	)
	targetRevisionID := uuid.MustParse(prototype.RevisionID)
	if err := database.Create(&storage.ArtifactDependencyModel{
		ID: uuid.New(), ProjectID: projectID,
		SourceArtifactID: uuid.MustParse(injected.ArtifactID), SourceRevisionID: uuid.MustParse(injected.RevisionID),
		SourceContentHash: injected.ContentHash, TargetArtifactID: uuid.MustParse(prototype.ArtifactID),
		TargetRevisionID: &targetRevisionID, Relation: "governs", Required: true,
		CreatedBy: ownerID, CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	sources, err := workbench.collectFrozenRevisionSources(context.Background(), projectID, targetRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsFrozenWorkbenchRevision(sources, pageSpec.RevisionID) ||
		!containsFrozenWorkbenchRevision(sources, blueprint.RevisionID) {
		t.Fatalf("immutable PageSpec/Blueprint closure was incomplete: %+v", sources)
	}
	if containsFrozenWorkbenchRevision(sources, injected.RevisionID) {
		t.Fatalf("mutable artifact_dependencies row entered frozen Workbench sources: %+v", sources)
	}

	frozenContext := seedArtifactLineageRevision(
		t, database, store, projectID, ownerID, "reference_source", "approved", "current",
		json.RawMessage(`{"title":"Frozen context"}`),
	)
	if err := database.Create(&storage.ArtifactRevisionSourceModel{
		RevisionID: targetRevisionID, Ordinal: 1,
		SourceArtifactID: uuid.MustParse(frozenContext.ArtifactID), SourceRevisionID: uuid.MustParse(frozenContext.RevisionID),
		SourceContentHash: frozenContext.ContentHash, Purpose: "reference_context", Required: false,
		AddedBy: ownerID, AddedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	sources, err = workbench.collectFrozenRevisionSources(context.Background(), projectID, targetRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsFrozenWorkbenchRevision(sources, frozenContext.RevisionID) {
		t.Fatalf("immutable optional revision source was not preserved: %+v", sources)
	}
}

func containsFrozenWorkbenchRevision(sources []frozenWorkbenchSource, revisionID string) bool {
	for _, source := range sources {
		if source.Ref.RevisionID == revisionID {
			return true
		}
	}
	return false
}
