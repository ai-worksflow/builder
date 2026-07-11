package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const workflowExecutionProfilesMigrationFile = "000016_workflow_execution_profiles.up.sql"

func TestWorkflowExecutionProfilesMigrationDeclaresExactImmutablePins(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(workflowExecutionProfilesMigrationFile)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000016_workflow_execution_profiles.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(up))
	for _, required := range []string{
		"legacy-pre-pin/v0", "workflow_definition_versions_execution_profile_content_check",
		"workflow_runs_definition_execution_profile_fk", "workflow_definition_execution_profile_immutable",
		"workflow_run_execution_profile_immutable", "jsonb_typeof(content->'executionprofile')",
		"alter column execution_profile_version set default", "old.content is distinct from new.content",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("workflow execution profile migration is missing %q", required)
		}
	}
	for _, required := range []string{"drop trigger if exists workflow_run_execution_profile_immutable", "drop column if exists execution_profile_hash", "drop column if exists execution_profile_version"} {
		if !strings.Contains(strings.ToLower(string(down)), required) {
			t.Fatalf("workflow execution profile rollback is missing %q", required)
		}
	}
}

func TestWorkflowExecutionProfilesMigrationBackfillsAndFencesPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockID); err != nil {
		t.Fatal(err)
	}
	schema := "workflow_profile_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path = "`+schema+`", public`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == workflowExecutionProfilesMigrationFile {
			break
		}
		contents, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	userID, projectID, definitionID, versionID, runID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id,email,display_name,password_hash) VALUES ($1,$2,'Owner','hash')`, userID, userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id,name,created_by) VALUES ($1,'Project',$2)`, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'legacy','Legacy',$3)`, definitionID, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definition_versions (id,definition_id,version,schema_version,content,content_hash,created_by) VALUES ($1,$2,1,1,'{}','legacy-content',$3)`, versionID, definitionID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_runs (id,project_id,definition_version_id,status,scope,context,started_by) VALUES ($1,$2,$3,'running','{}','{}',$4)`, runID, projectID, versionID, userID); err != nil {
		t.Fatal(err)
	}
	up, _ := files.ReadFile(workflowExecutionProfilesMigrationFile)
	if _, err := tx.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply migration 016: %v", err)
	}
	const legacyHash = "bee729c4921a93fd2e229cd610314359ca420610c195ada00a201507bfd7a14c"
	var definitionProfile, definitionHash, runProfile, runHash string
	if err := tx.QueryRowContext(ctx, `SELECT execution_profile_version, execution_profile_hash FROM workflow_definition_versions WHERE id=$1`, versionID).Scan(&definitionProfile, &definitionHash); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT execution_profile_version, execution_profile_hash FROM workflow_runs WHERE id=$1`, runID).Scan(&runProfile, &runHash); err != nil {
		t.Fatal(err)
	}
	if definitionProfile != "legacy-pre-pin/v0" || definitionHash != legacyHash || runProfile != definitionProfile || runHash != definitionHash {
		t.Fatalf("legacy backfill drifted: definition=%s/%s run=%s/%s", definitionProfile, definitionHash, runProfile, runHash)
	}

	// A binary from before migration 016 omits both new columns. During a rolling
	// deployment it may continue writing only profile-less definitions/runs, which
	// must be fenced into the exact frozen legacy profile.
	rollingDefinitionID, rollingVersionID, rollingRunID := uuid.New(), uuid.New(), uuid.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'rolling-legacy','Rolling legacy',$3)`, rollingDefinitionID, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definition_versions (id,definition_id,version,schema_version,content,content_hash,created_by) VALUES ($1,$2,1,1,'{}','rolling-legacy-content',$3)`, rollingVersionID, rollingDefinitionID, userID); err != nil {
		t.Fatalf("pre-016 definition writer was not rolling-compatible: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_runs (id,project_id,definition_version_id,status,scope,context,started_by) VALUES ($1,$2,$3,'running','{}','{}',$4)`, rollingRunID, projectID, rollingVersionID, userID); err != nil {
		t.Fatalf("pre-016 run writer was not rolling-compatible: %v", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT execution_profile_version, execution_profile_hash FROM workflow_runs WHERE id=$1`, rollingRunID).Scan(&runProfile, &runHash); err != nil {
		t.Fatal(err)
	}
	if runProfile != "legacy-pre-pin/v0" || runHash != legacyHash {
		t.Fatalf("rolling legacy write was not fenced to the legacy profile: %s/%s", runProfile, runHash)
	}
	_, _ = tx.ExecContext(ctx, `SAVEPOINT reject_definition_profile_update`)
	_, rejectedDefinitionUpdate := tx.ExecContext(ctx, `UPDATE workflow_definition_versions SET execution_profile_hash=$2 WHERE id=$1`, versionID, strings.Repeat("a", 64))
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT reject_definition_profile_update`)
	if rejectedDefinitionUpdate == nil {
		t.Fatal("definition execution profile was mutable")
	}
	_, _ = tx.ExecContext(ctx, `SAVEPOINT reject_definition_content_update`)
	_, rejectedDefinitionContentUpdate := tx.ExecContext(ctx, `UPDATE workflow_definition_versions SET content='{"mutated":true}'::jsonb, content_hash='mutated' WHERE id=$1`, versionID)
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT reject_definition_content_update`)
	if rejectedDefinitionContentUpdate == nil {
		t.Fatal("immutable workflow definition content and hash were mutable")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_definition_versions SET published=true WHERE id=$1`, versionID); err != nil {
		t.Fatalf("publication state was incorrectly frozen with definition identity: %v", err)
	}
	_, _ = tx.ExecContext(ctx, `SAVEPOINT reject_run_profile_update`)
	_, rejectedRunUpdate := tx.ExecContext(ctx, `UPDATE workflow_runs SET execution_profile_version='workflow-engine/v1' WHERE id=$1`, runID)
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT reject_run_profile_update`)
	if rejectedRunUpdate == nil {
		t.Fatal("run execution profile was mutable")
	}

	currentDefinitionID, currentVersionID := uuid.New(), uuid.New()
	const currentHash = "dd247a77ce3cfa1095a575a238b93c4bd41dd991eac07e8b62ec170864470da1"
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'current','Current',$3)`, currentDefinitionID, projectID, userID); err != nil {
		t.Fatal(err)
	}
	content := `{"executionProfile":{"version":"workflow-engine/v2","hash":"` + currentHash + `"}}`
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definition_versions (id,definition_id,version,schema_version,content,content_hash,execution_profile_version,execution_profile_hash,created_by) VALUES ($1,$2,1,1,$3,'current-content','workflow-engine/v2',$4,$5)`, currentVersionID, currentDefinitionID, content, currentHash, userID); err != nil {
		t.Fatalf("exact current profile row was rejected: %v", err)
	}
	currentRunID := uuid.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_runs (id,project_id,definition_version_id,status,scope,context,started_by,execution_profile_version,execution_profile_hash) VALUES ($1,$2,$3,'running','{}','{}',$4,'workflow-engine/v2',$5)`, currentRunID, projectID, currentVersionID, userID, currentHash); err != nil {
		t.Fatalf("exact current-profile run was rejected: %v", err)
	}
	_, _ = tx.ExecContext(ctx, `SAVEPOINT reject_legacy_writer_current_run`)
	_, rejectedLegacyWriterCurrentRun := tx.ExecContext(ctx, `INSERT INTO workflow_runs (id,project_id,definition_version_id,status,scope,context,started_by) VALUES ($1,$2,$3,'running','{}','{}',$4)`, uuid.New(), projectID, currentVersionID, userID)
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT reject_legacy_writer_current_run`)
	if rejectedLegacyWriterCurrentRun == nil {
		t.Fatal("pre-016 writer silently started a current-profile definition")
	}
	_, _ = tx.ExecContext(ctx, `SAVEPOINT reject_mismatched_run_profile`)
	_, rejectedRunInsert := tx.ExecContext(ctx, `INSERT INTO workflow_runs (id,project_id,definition_version_id,status,scope,context,started_by,execution_profile_version,execution_profile_hash) VALUES ($1,$2,$3,'running','{}','{}',$4,'legacy-pre-pin/v0',$5)`, uuid.New(), projectID, currentVersionID, userID, legacyHash)
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT reject_mismatched_run_profile`)
	if rejectedRunInsert == nil {
		t.Fatal("run with a profile different from its definition was accepted")
	}

	down, _ := files.ReadFile("000016_workflow_execution_profiles.down.sql")
	if _, err := tx.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("apply migration 016 down: %v", err)
	}
	var columns int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('workflow_definition_versions','workflow_runs') AND column_name IN ('execution_profile_version','execution_profile_hash')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatalf("migration rollback left %d execution-profile columns", columns)
	}
}
