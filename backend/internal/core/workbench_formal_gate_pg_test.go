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

func TestCreateWorkbenchBundleFormalPrototypeGatePostgres(t *testing.T) {
	database, cleanup := multiBundlePostgresDatabase(t)
	defer cleanup()

	fixture := seedMultiBundlePostgresFixture(t, database)
	ctx := context.Background()
	prototype := fixture.rootA.PrototypeRevision
	artifactID := uuid.MustParse(prototype.ArtifactID)
	revisionID := uuid.MustParse(prototype.RevisionID)
	now := time.Now().UTC()

	replacementPayload := json.RawMessage(`{"frames":[],"replacement":true}`)
	replacementStored := fixture.contents.addFinalized(
		fixture.projectID.String(), "artifact_revision", uuid.NewString(), 1, replacementPayload,
	)
	replacementRevisionID := uuid.New()
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: replacementRevisionID, ArtifactID: artifactID, RevisionNumber: 2,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: replacementStored.ID,
		ContentHash: replacementStored.ContentHash, ByteSize: replacementStored.ByteSize,
		WorkflowStatus: "approved", ChangeSource: "human", ChangeSummary: "replacement",
		CreatedBy: fixture.ownerID, CreatedAt: now, ApprovedAt: &now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	resetFormalState := func(t *testing.T) {
		t.Helper()
		if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
			Updates(map[string]any{
				"lifecycle": "active", "latest_approved_revision_id": revisionID,
			}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", revisionID).
			Update("workflow_status", "approved").Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Where("artifact_id = ?", artifactID).
			Delete(&storage.ArtifactHealthModel{}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Create(&storage.ArtifactHealthModel{
			ArtifactID: artifactID, SyncStatus: "current", DeliveryStatus: "incomplete",
			Report: json.RawMessage(`{}`), ComputedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	formalGateMessage := "exact latest approved revision"
	tests := []struct {
		name   string
		mutate func(*testing.T)
	}{
		{
			name: "archived artifact",
			mutate: func(t *testing.T) {
				if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
					Update("lifecycle", "archived").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing latest approval",
			mutate: func(t *testing.T) {
				if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
					Update("latest_approved_revision_id", nil).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "selected revision is not latest approval",
			mutate: func(t *testing.T) {
				if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
					Update("latest_approved_revision_id", replacementRevisionID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "selected revision is not approved",
			mutate: func(t *testing.T) {
				if err := database.Model(&storage.ArtifactRevisionModel{}).Where("id = ?", revisionID).
					Update("workflow_status", "changes_requested").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing dependency health",
			mutate: func(t *testing.T) {
				if err := database.Where("artifact_id = ?", artifactID).
					Delete(&storage.ArtifactHealthModel{}).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "dependency health is not current",
			mutate: func(t *testing.T) {
				if err := database.Model(&storage.ArtifactHealthModel{}).Where("artifact_id = ?", artifactID).
					Update("sync_status", "needs_sync").Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetFormalState(t)
			test.mutate(t)
			_, err := fixture.workbench.CreateBundle(
				ctx, fixture.projectID.String(), fixture.ownerID.String(),
				CreateWorkbenchBundleInput{PrototypeRevision: prototype},
			)
			if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), formalGateMessage) {
				t.Fatalf("non-formal Prototype passed the exact formal gate: %v", err)
			}
		})
	}

	t.Run("current exact approval reaches downstream validation", func(t *testing.T) {
		resetFormalState(t)
		if err := database.Transaction(func(transaction *gorm.DB) error {
			return lockFormalPrototypeWorkbenchInput(transaction, fixture.projectID, artifactID, revisionID)
		}); err != nil {
			t.Fatalf("transactional formal-input lock rejected exact current approval: %v", err)
		}
		_, err := fixture.workbench.CreateBundle(
			ctx, fixture.projectID.String(), fixture.ownerID.String(),
			CreateWorkbenchBundleInput{PrototypeRevision: prototype},
		)
		if err == nil || strings.Contains(err.Error(), formalGateMessage) ||
			!strings.Contains(err.Error(), "immutable required source") {
			t.Fatalf("exact current approval did not reach downstream bundle validation: %v", err)
		}
	})

	t.Run("explicit owner stale waiver reaches downstream validation", func(t *testing.T) {
		resetFormalState(t)
		if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).
			Update("lifecycle", "archived").Error; err != nil {
			t.Fatal(err)
		}
		_, err := fixture.workbench.CreateBundle(
			ctx, fixture.projectID.String(), fixture.ownerID.String(),
			CreateWorkbenchBundleInput{
				PrototypeRevision: prototype, AllowStale: true, OverrideReason: "approved recovery build",
			},
		)
		if err == nil || strings.Contains(err.Error(), formalGateMessage) ||
			!strings.Contains(err.Error(), "immutable required source") {
			t.Fatalf("authorized explicit stale waiver did not reach downstream validation: %v", err)
		}
	})

	t.Run("editor cannot use stale waiver", func(t *testing.T) {
		resetFormalState(t)
		if err := database.Model(&storage.ArtifactHealthModel{}).Where("artifact_id = ?", artifactID).
			Update("sync_status", "blocked").Error; err != nil {
			t.Fatal(err)
		}
		editorID := seedMultiBundleUser(t, database, "formal-gate-editor")
		seedMultiBundleMembership(t, database, fixture.projectID, editorID, RoleEditor)
		_, err := fixture.workbench.CreateBundle(
			ctx, fixture.projectID.String(), editorID.String(),
			CreateWorkbenchBundleInput{
				PrototypeRevision: prototype, AllowStale: true, OverrideReason: "editor override",
			},
		)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("editor used the admin-only stale waiver: %v", err)
		}
	})
}
