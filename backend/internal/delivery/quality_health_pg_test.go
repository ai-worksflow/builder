package delivery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/core"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
)

func TestPersistQualityReportInitializesArtifactHealthPostgres(t *testing.T) {
	database, cleanup := deliveryAssemblyPostgresDatabase(t)
	defer cleanup()

	now := time.Now().UTC()
	actorID, projectID := uuid.New(), uuid.New()
	publishLineageSeedUserProject(t, database, actorID, projectID, "quality-health")
	contents := newPublishLineagePGContentStore()

	workspaceArtifactID, workspaceRevisionID := uuid.New(), uuid.New()
	workspacePayload := json.RawMessage(`{"files":[{"path":"index.html","content":"ready"}]}`)
	workspaceContent := contents.put(projectID.String(), "artifact_revision", workspaceRevisionID.String(), workspacePayload)
	workspace := storage.ArtifactModel{
		ID: workspaceArtifactID, ProjectID: projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-QUALITY-HEALTH",
		Title: "Workspace", Lifecycle: "active", Version: 1, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&workspace).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: workspaceRevisionID, ArtifactID: workspaceArtifactID, RevisionNumber: 1,
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: workspaceContent.ID,
		ContentHash: workspaceContent.ContentHash, ByteSize: workspaceContent.ByteSize,
		WorkflowStatus: "approved", ChangeSource: "system", ChangeSummary: "Quality health fixture",
		CreatedBy: actorID, CreatedAt: now, ApprovedAt: &now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	completedAt := now.Add(time.Second)
	report := QualityReport{
		ID: uuid.NewString(), ProjectID: projectID.String(),
		WorkspaceRevision: core.VersionRef{
			ArtifactID: workspaceArtifactID.String(), RevisionID: workspaceRevisionID.String(),
			ContentHash: workspaceContent.ContentHash,
		},
		Status: "passed", Passed: true, Score: 100, RunnerVersion: RunnerVersion, SandboxKind: "quality-health-test",
		Checks: []CheckResult{}, Diagnostics: []Diagnostic{},
		ReportArtifactID: uuid.NewString(), ReportRevisionID: uuid.NewString(), CreatedBy: actorID.String(),
		StartedAt: now, CompletedAt: &completedAt, Version: 1,
	}
	service := &QualityService{database: database, contents: contents}
	if err := service.persistReport(context.Background(), &report, nil); err != nil {
		t.Fatal(err)
	}

	var health storage.ArtifactHealthModel
	if err := database.Where("artifact_id = ?", report.ReportArtifactID).Take(&health).Error; err != nil {
		t.Fatalf("load QualityReport health: %v", err)
	}
	if health.SyncStatus != "current" || health.DeliveryStatus != "complete" ||
		health.FindingCount != 0 || health.BlockingCount != 0 || health.ComputedAt.UnixMicro() != completedAt.UnixMicro() {
		t.Fatalf("unexpected initial QualityReport health: %#v", health)
	}
}
