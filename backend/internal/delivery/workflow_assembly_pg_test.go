package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	storage "github.com/worksflow/builder/backend/internal/storage/postgres"
	"github.com/worksflow/builder/backend/internal/workflow"
	"github.com/worksflow/builder/backend/migrations"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestQualityAssemblesLinearWorkspaceOutputsPostgres(t *testing.T) {
	database, cleanup := deliveryAssemblyPostgresDatabase(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	actorID, projectID := uuid.New(), uuid.New()
	if err := database.Create(&storage.UserModel{
		ID: actorID, Email: "quality-workspace-" + uuid.NewString() + "@example.com",
		DisplayName: "Quality Owner", PasswordHash: "not-used", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&storage.ProjectModel{
		ID: projectID, Name: "Quality workspace assembly", Lifecycle: "active", Version: 1,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	artifactID := uuid.New()
	revisionIDs := []uuid.UUID{uuid.New(), uuid.New()}
	hashes := []string{deliveryTestHash("assembly-w1"), deliveryTestHash("assembly-w2")}
	if err := database.Create(&storage.ArtifactModel{
		ID: artifactID, ProjectID: projectID, Kind: "workspace", ArtifactKey: "WORKSPACE-ASSEMBLY",
		Title: "Application Workspace", Lifecycle: "active", Version: 2,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for index, revisionID := range revisionIDs {
		var parent *uuid.UUID
		status := "superseded"
		if index == 1 {
			value := revisionIDs[0]
			parent = &value
			status = "approved"
		}
		approvedAt := now.Add(time.Duration(index+1) * time.Second)
		if err := database.Create(&storage.ArtifactRevisionModel{
			ID: revisionID, ArtifactID: artifactID, RevisionNumber: uint64(index + 1), ParentRevisionID: parent,
			SchemaVersion: 1, ContentStore: "mongo", ContentRef: "quality-assembly-" + revisionID.String(),
			ContentHash: hashes[index], WorkflowStatus: status, ChangeSource: "ai_proposal",
			ChangeSummary: "Apply Workbench group", CreatedBy: actorID, CreatedAt: approvedAt, ApprovedAt: &approvedAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Model(&storage.ArtifactModel{}).Where("id = ?", artifactID).Updates(map[string]any{
		"latest_revision_id": revisionIDs[1], "latest_approved_revision_id": revisionIDs[1],
	}).Error; err != nil {
		t.Fatal(err)
	}

	runID := uuid.NewString()
	bindings := make([]domain.NodeInputBinding, 0, len(revisionIDs))
	for index, revisionID := range revisionIDs {
		output, _ := json.Marshal(map[string]any{"workspaceRevision": domain.ArtifactRef{
			ArtifactID: artifactID.String(), RevisionID: revisionID.String(), ContentHash: hashes[index],
		}})
		key := "workbench-" + string(rune('a'+index))
		bindings = append(bindings, domain.NodeInputBinding{
			EdgeID: "quality-" + key, FromPort: "default", ToPort: "default",
			Source: domain.NodeOutputReference{RunID: runID, NodeKey: key, DefinitionNodeID: key},
			Value:  output, Output: output,
		})
	}
	inputs, err := domain.NewNodeInputEnvelope(bindings)
	if err != nil {
		t.Fatal(err)
	}
	execution := workflow.Execution{
		Run: workflow.RunRecord{ID: runID, ProjectID: projectID.String()}, Inputs: inputs,
	}
	selected, err := workspaceRevisionForQuality(ctx, database, execution)
	if err != nil {
		t.Fatal(err)
	}
	if selected.RevisionID != revisionIDs[1].String() || selected.ContentHash != hashes[1] {
		t.Fatalf("selected workspace = %+v, want final descendant", selected)
	}

	branchID := uuid.New()
	branchHash := deliveryTestHash("assembly-branch")
	approvedAt := now.Add(3 * time.Second)
	if err := database.Create(&storage.ArtifactRevisionModel{
		ID: branchID, ArtifactID: artifactID, RevisionNumber: 3, ParentRevisionID: &revisionIDs[0],
		SchemaVersion: 1, ContentStore: "mongo", ContentRef: "quality-assembly-branch",
		ContentHash: branchHash, WorkflowStatus: "superseded", ChangeSource: "ai_proposal",
		ChangeSummary: "Unselected branch", CreatedBy: actorID, CreatedAt: approvedAt, ApprovedAt: &approvedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	branchOutput, _ := json.Marshal(map[string]any{"workspaceRevision": domain.ArtifactRef{
		ArtifactID: artifactID.String(), RevisionID: branchID.String(), ContentHash: branchHash,
	}})
	branchInputs, err := domain.NewNodeInputEnvelope(append(bindings, domain.NodeInputBinding{
		EdgeID: "quality-workbench-branch", FromPort: "default", ToPort: "default",
		Source: domain.NodeOutputReference{RunID: runID, NodeKey: "workbench-branch", DefinitionNodeID: "workbench-branch"},
		Value:  branchOutput, Output: branchOutput,
	}))
	if err != nil {
		t.Fatal(err)
	}
	execution.Inputs = branchInputs
	if _, err := workspaceRevisionForQuality(ctx, database, execution); err == nil {
		t.Fatal("quality accepted WorkspaceRevision inputs from divergent branches")
	}
}

func deliveryAssemblyPostgresDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	base, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "delivery_assembly_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(
		gormpostgres.Open(deliveryAssemblyDSNWithSearchPath(t, dsn, schema)),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
		t.Fatal(err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(context.Background(), sqlDatabase); err != nil {
		t.Fatal(err)
	}
	return database, func() {
		_ = sqlDatabase.Close()
		_ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error
	}
}

func deliveryAssemblyDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func deliveryTestHash(seed string) string {
	digest := sha256.Sum256([]byte("delivery-assembly:" + seed))
	return "sha256:" + hex.EncodeToString(digest[:])
}
