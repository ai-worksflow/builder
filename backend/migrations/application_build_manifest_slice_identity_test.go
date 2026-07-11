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

const applicationBuildManifestSliceMigrationFile = "000017_application_build_manifest_slice_identity.up.sql"

func TestApplicationBuildManifestSliceIdentityMigrationDeclaresExactRootPins(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(applicationBuildManifestSliceMigrationFile)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000017_application_build_manifest_slice_identity.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(up))
	for _, required := range []string{
		"add column delivery_slice_id", "refuses to self-certify existing current workflow build manifest slice identities",
		"offline-verify immutable bundle payloads",
		"application_build_manifests_delivery_slice_shape_check",
		"application build manifest slice and lineage identity is immutable",
		"derived application build manifest delivery slice must match its exact parent",
		"before insert or update on application_build_manifests",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("slice identity migration is missing %q", required)
		}
	}
	for _, required := range []string{
		"drop trigger if exists application_build_manifest_slice_identity",
		"drop column if exists delivery_slice_id",
	} {
		if !strings.Contains(strings.ToLower(string(down)), required) {
			t.Fatalf("slice identity rollback is missing %q", required)
		}
	}
}

func TestApplicationBuildManifestSliceIdentityMigrationRejectsPreexistingCurrentGroupsAndFencesPostgres(t *testing.T) {
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
	schema := "manifest_slice_identity_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
		if name == applicationBuildManifestSliceMigrationFile {
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

	userID, projectID, definitionID, versionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	runID, compilerID, rootID, derivedID, sliceID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id,email,display_name,password_hash) VALUES ($1,$2,'Owner','hash')`, userID, userID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id,name,created_by) VALUES ($1,'Project',$2)`, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definitions (id,project_id,workflow_key,title,created_by) VALUES ($1,$2,'slice-pins','Slice pins',$3)`, definitionID, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_definition_versions (id,definition_id,version,schema_version,content,content_hash,created_by) VALUES ($1,$2,1,1,'{}','slice-version',$3)`, versionID, definitionID, userID); err != nil {
		t.Fatal(err)
	}
	contextJSON := `{"nodes":{"compile":{"output":{"schemaVersion":1,"projectId":"` + projectID.String() + `","runId":"` + runID.String() + `","manifestGroupKey":"` + compilerID.String() + `","sliceIds":["` + sliceID + `"],"bundleIds":["` + rootID.String() + `"]}}}}`
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_runs (id,project_id,definition_version_id,status,scope,context,started_by) VALUES ($1,$2,$3,'running','{}',$4,$5)`, runID, projectID, versionID, contextJSON, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_node_runs (id,run_id,node_key,node_type,status,attempt) VALUES ($1,$2,'compile','manifest_compiler','completed',1)`, compilerID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,workflow_run_id,root_manifest_id,root_ordinal,manifest_group_key,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$3,$1,0,$4,1,'root-ref','root-content','root-manifest','frozen',$5)`, rootID, projectID, runID, compilerID.String(), userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,workflow_run_id,root_manifest_id,derived_from_id,root_ordinal,manifest_group_key,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$3,$4,$4,0,$5,1,'derived-ref','derived-content','derived-manifest','frozen',$6)`, derivedID, projectID, runID, rootID, compilerID.String(), userID); err != nil {
		t.Fatal(err)
	}

	if _, err := tx.ExecContext(ctx, `SAVEPOINT preexisting_current_group`); err != nil {
		t.Fatal(err)
	}
	up, _ := files.ReadFile(applicationBuildManifestSliceMigrationFile)
	if _, err := tx.ExecContext(ctx, string(up)); err == nil {
		t.Fatal("migration self-certified a preexisting current workflow group from its compiler output")
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT preexisting_current_group`); err != nil {
		t.Fatal(err)
	}
	// This simulates the required deployment-specific offline audit: the test
	// fixture has no immutable bundle store, so its pre-migration rows are
	// explicitly quarantined into the migration-owned legacy group.
	if _, err := tx.ExecContext(ctx, `UPDATE application_build_manifests SET manifest_group_key='legacy' WHERE root_manifest_id=$1`, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	var legacyPinned int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM application_build_manifests WHERE root_manifest_id=$1 AND delivery_slice_id IS NOT NULL`, rootID).Scan(&legacyPinned); err != nil {
		t.Fatal(err)
	}
	if legacyPinned != 0 {
		t.Fatal("migration invented delivery slice identities for legacy rows")
	}

	exactRootID, exactGroupID, exactSliceID := uuid.New(), uuid.NewString(), uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,workflow_run_id,root_manifest_id,root_ordinal,manifest_group_key,delivery_slice_id,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$3,$1,0,$4,$5,1,'exact-ref','exact-content','exact-manifest','frozen',$6)`, exactRootID, projectID, runID, exactGroupID, exactSliceID, userID); err != nil {
		t.Fatalf("new current workflow root with an exact slice was rejected: %v", err)
	}

	_, _ = tx.ExecContext(ctx, `SAVEPOINT immutable_slice`)
	_, immutableErr := tx.ExecContext(ctx, `UPDATE application_build_manifests SET delivery_slice_id=$2 WHERE id=$1`, exactRootID, uuid.NewString())
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT immutable_slice`)
	if immutableErr == nil {
		t.Fatal("persisted delivery slice identity was mutable")
	}
	_, _ = tx.ExecContext(ctx, `SAVEPOINT immutable_group`)
	_, immutableGroupErr := tx.ExecContext(ctx, `UPDATE application_build_manifests SET manifest_group_key=$2 WHERE id=$1`, exactRootID, uuid.NewString())
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT immutable_group`)
	if immutableGroupErr == nil {
		t.Fatal("manifest group could be changed to bypass immutable slice identity")
	}

	_, _ = tx.ExecContext(ctx, `SAVEPOINT missing_current_slice`)
	missingRootID := uuid.New()
	_, missingErr := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,workflow_run_id,root_manifest_id,root_ordinal,manifest_group_key,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$3,$1,0,$4,1,'missing-ref','missing-content','missing-manifest','frozen',$5)`, missingRootID, projectID, runID, uuid.NewString(), userID)
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT missing_current_slice`)
	if missingErr == nil {
		t.Fatal("new current workflow root omitted its delivery slice identity")
	}

	_, _ = tx.ExecContext(ctx, `SAVEPOINT mismatched_derived_slice`)
	mismatchedDerivedID := uuid.New()
	_, mismatchErr := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,workflow_run_id,root_manifest_id,derived_from_id,root_ordinal,manifest_group_key,delivery_slice_id,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$3,$4,$4,0,$5,$6,1,'mismatch-ref','mismatch-content','mismatch-manifest','frozen',$7)`, mismatchedDerivedID, projectID, runID, exactRootID, exactGroupID, uuid.NewString(), userID)
	_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT mismatched_derived_slice`)
	if mismatchErr == nil {
		t.Fatal("derived row accepted a delivery slice different from its parent")
	}
	exactDerivedID := uuid.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,workflow_run_id,root_manifest_id,derived_from_id,root_ordinal,manifest_group_key,delivery_slice_id,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$3,$4,$4,0,$5,$6,1,'exact-derived-ref','exact-derived-content','exact-derived-manifest','frozen',$7)`, exactDerivedID, projectID, runID, exactRootID, exactGroupID, exactSliceID, userID); err != nil {
		t.Fatalf("derived row did not inherit its exact parent slice: %v", err)
	}

	standaloneID := uuid.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,root_manifest_id,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$1,1,'standalone-ref','standalone-content','standalone-manifest','frozen',$3)`, standaloneID, projectID, userID); err != nil {
		t.Fatalf("standalone manifest was incorrectly required to carry a slice: %v", err)
	}
	legacyID := uuid.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_build_manifests (id,project_id,workflow_run_id,root_manifest_id,root_ordinal,manifest_group_key,schema_version,content_ref,content_hash,manifest_hash,status,created_by) VALUES ($1,$2,$3,$1,1,'legacy',1,'legacy-ref','legacy-content','legacy-manifest','frozen',$4)`, legacyID, projectID, runID, userID); err != nil {
		t.Fatalf("legacy workflow manifest was incorrectly required to carry a slice: %v", err)
	}

	down, _ := files.ReadFile("000017_application_build_manifest_slice_identity.down.sql")
	if _, err := tx.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("apply migration 017 down: %v", err)
	}
	var columns int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='application_build_manifests' AND column_name='delivery_slice_id'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("migration rollback left delivery_slice_id behind")
	}
}
