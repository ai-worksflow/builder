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
	if err != nil || len(versions) != 5 {
		t.Fatalf("provisioned versions = %+v, error = %v", versions, err)
	}
	if versions[0].VersionID != legacyVersionID || versions[0].Published ||
		versions[1].Definition.Version != 2 || versions[1].Published ||
		versions[2].Definition.Version != 3 || versions[2].Published ||
		versions[3].Definition.Version != 4 || versions[3].Published || versions[3].ExecutionProfile != WorkflowExecutionProfileV1Ref() ||
		versions[4].Definition.Version != MinimumLoopCurrentVersion || !versions[4].Published || versions[4].ExecutionProfile != CurrentWorkflowExecutionProfileRef() {
		t.Fatalf("startup provisioner did not preserve drafts and publish current version: %+v", versions)
	}
	upgraded, err = upgradeMinimumLoopInstallations(context.Background(), store, installations, time.Now())
	if err != nil || upgraded != 0 {
		t.Fatalf("idempotent startup upgraded %d definitions: %v", upgraded, err)
	}

	// Model a crash after publishing v5 but before revoking the previously
	// published v4. The retry must inspect every historical version rather than
	// looking only at v1, otherwise two profiles remain discoverable.
	v4VersionID, err := minimumLoopVersionID(projectID, 4)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	v4 := store.definitionVersions[v4VersionID]
	v4.Published = true
	store.definitionVersions[v4VersionID] = v4
	store.definitions[definitionID][4] = v4
	store.mu.Unlock()
	upgraded, err = upgradeMinimumLoopInstallations(context.Background(), store, installations, time.Now())
	if err != nil || upgraded != 1 {
		t.Fatalf("partial v4 publication repair upgraded %d definitions: %v", upgraded, err)
	}
	versions, err = store.ListDefinitionVersions(context.Background(), definitionID)
	if err != nil || versions[3].Published || !versions[4].Published {
		t.Fatalf("partial profile publication was not repaired: versions=%+v err=%v", versions, err)
	}
}
