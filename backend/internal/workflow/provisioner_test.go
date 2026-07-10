package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStartupProvisionerUpgradesExistingV1OnceAndKeepsHistory(t *testing.T) {
	store := NewMemoryStore(nil)
	projectID, definitionID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	legacyVersionID := uuid.NewString()
	legacy, err := minimumLoopDefinitionV1(definitionID, actorID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDefinition(context.Background(), DefinitionRecord{
		VersionID: legacyVersionID, ProjectID: projectID, Key: MinimumLoopKey,
		Title: minimumLoopTitle, Description: minimumLoopDescription,
		Published: true, Definition: legacy,
	}); err != nil {
		t.Fatal(err)
	}
	installations := []minimumLoopInstallation{{
		definitionID: definitionID, projectID: projectID, installerUserID: actorID,
	}}
	upgraded, err := upgradeMinimumLoopInstallations(context.Background(), store, installations, time.Now())
	if err != nil || upgraded != 1 {
		t.Fatalf("first startup upgraded %d definitions: %v", upgraded, err)
	}
	versions, err := store.ListDefinitionVersions(context.Background(), definitionID)
	if err != nil || len(versions) != 3 {
		t.Fatalf("provisioned versions = %+v, error = %v", versions, err)
	}
	if versions[0].VersionID != legacyVersionID || versions[0].Published ||
		versions[1].Definition.Version != 2 || versions[1].Published ||
		versions[2].Definition.Version != MinimumLoopCurrentVersion || !versions[2].Published {
		t.Fatalf("startup provisioner did not preserve drafts and publish current version: %+v", versions)
	}
	upgraded, err = upgradeMinimumLoopInstallations(context.Background(), store, installations, time.Now())
	if err != nil || upgraded != 0 {
		t.Fatalf("idempotent startup upgraded %d definitions: %v", upgraded, err)
	}
}
