package core

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestProjectCreateInitializesProjectBriefHealth(t *testing.T) {
	database, cleanup := baselinePostgresDatabase(t)
	defer cleanup()

	now := time.Now().UTC()
	ownerID := uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: ownerID, Email: "project-owner-" + uuid.NewString() + "@example.com",
		DisplayName: "Project Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	access, err := NewAccessControl(database)
	if err != nil {
		t.Fatal(err)
	}
	store := newBaselineContentStoreSpy()
	projects, err := NewProjectService(database, store, access)
	if err != nil {
		t.Fatal(err)
	}

	created, err := projects.Create(context.Background(), ownerID.String(), CreateProjectInput{
		Name: "Health invariant", Description: "Verify the initial Project Brief health row.",
	})
	if err != nil {
		t.Fatal(err)
	}
	var health storage.ArtifactHealthModel
	if err := database.Where("artifact_id = ?", created.InitialArtifactID).Take(&health).Error; err != nil {
		t.Fatalf("load initial Project Brief health: %v", err)
	}
	if health.SyncStatus != "current" || health.DeliveryStatus != "incomplete" ||
		health.FindingCount != 0 || health.BlockingCount != 0 || string(health.Report) != "{}" {
		t.Fatalf("unexpected initial Project Brief health: %#v", health)
	}
}
