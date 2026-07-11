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

func TestWorkflowBuildManifestActivationValidatesHashAndExactRootShape(t *testing.T) {
	manifest := workflowBuildManifestActivation{
		SchemaVersion: 1, ProjectID: uuid.NewString(), RunID: uuid.NewString(), ManifestGroupKey: uuid.NewString(),
		SliceIDs:  []string{uuid.NewString(), uuid.NewString()},
		BundleIDs: []string{uuid.NewString(), uuid.NewString()},
		Sources: []domain.ArtifactRef{{
			ArtifactID: uuid.NewString(), RevisionID: uuid.NewString(), ContentHash: activationTestHash(t, "source"),
		}},
		Constraints: json.RawMessage(`{"scope":"all"}`), CreatedAt: time.Now().UTC(),
	}
	manifest.Hash = activationManifestHash(t, manifest)
	if err := manifest.validate(); err != nil {
		t.Fatalf("valid compiler output rejected: %v", err)
	}

	tampered := manifest
	tampered.BundleIDs = append([]string(nil), manifest.BundleIDs...)
	tampered.BundleIDs[1] = uuid.NewString()
	if err := tampered.validate(); err == nil {
		t.Fatal("compiler output accepted a root mutation without a matching hash")
	}

	duplicate := manifest
	duplicate.BundleIDs = []string{manifest.BundleIDs[0], manifest.BundleIDs[0]}
	duplicate.Hash = activationManifestHash(t, duplicate)
	if err := duplicate.validate(); err == nil {
		t.Fatal("compiler output accepted duplicate root ids with a valid hash")
	}
}

func TestSharedWorkflowManifestActivationBarrierFailsClosedOnIncompleteCurrentCoordinates(t *testing.T) {
	t.Parallel()
	database := &gorm.DB{}
	if err := EnsureWorkflowManifestGroupActivated(
		context.Background(), database, storage.ApplicationBuildManifestModel{},
	); err != nil {
		t.Fatalf("standalone Workbench manifest was incorrectly gated: %v", err)
	}
	runID := uuid.New()
	legacy := "legacy"
	if err := EnsureWorkflowManifestGroupActivated(context.Background(), database, storage.ApplicationBuildManifestModel{
		WorkflowRunID: &runID, ManifestGroupKey: &legacy,
	}); err != nil {
		t.Fatalf("migration-owned legacy Workbench manifest was incorrectly gated: %v", err)
	}
	if err := EnsureWorkflowManifestGroupActivated(context.Background(), database, storage.ApplicationBuildManifestModel{
		WorkflowRunID: &runID,
	}); !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("current workflow manifest with incomplete activation coordinates error = %v", err)
	}
}

func activationManifestHash(t *testing.T, manifest workflowBuildManifestActivation) string {
	t.Helper()
	manifest.Hash = ""
	hash, err := domain.CanonicalHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func activationTestHash(t *testing.T, value string) string {
	t.Helper()
	hash, err := domain.CanonicalHash(map[string]string{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
