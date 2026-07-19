package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestLegacyDeploymentReleaseControllerGatePostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	t.Run("legacy first and v3 first cannot establish two writers", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000059_legacy_deployment_release_controller_gate.up.sql",
		)

		actorID := uuid.New()
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'legacy controller gate actor', 'not-used')
`, actorID, "legacy-controller-gate-"+uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}

		legacyFirst := seedLegacyControllerGateProject(t, ctx, database, actorID, "legacy-first", "deploying")
		if _, err := database.ExecContext(ctx, `
CREATE TABLE legacy_deployment_admission_probe (
  deployment_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'deploying'
);
CREATE TRIGGER legacy_deployment_admission_probe_guard
BEFORE INSERT ON legacy_deployment_admission_probe
FOR EACH ROW EXECUTE FUNCTION validate_legacy_deployment_version_controller_gate();
`); err != nil {
			t.Fatal(err)
		}

		legacyTransaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacyTransaction.ExecContext(ctx,
			`INSERT INTO legacy_deployment_admission_probe (deployment_id) VALUES ($1)`,
			legacyFirst.deploymentID,
		); err != nil {
			legacyTransaction.Rollback()
			t.Fatalf("legacy admission did not acquire the shared project lock: %v", err)
		}
		v3AfterLegacy := make(chan error, 1)
		go func() {
			_, insertErr := database.ExecContext(ctx, previewRunInsertSQL,
				uuid.New(), legacyFirst.projectID, legacyFirst.bundleID, legacyFirst.bundleHash,
				"v3-after-legacy-"+uuid.NewString(), releaseOperationDigest("v3-after-legacy"), actorID,
			)
			v3AfterLegacy <- insertErr
		}()
		assertAdmissionWaitsForSharedProjectLock(t, v3AfterLegacy)
		if err := legacyTransaction.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := receiveAdmissionResult(t, v3AfterLegacy); err == nil ||
			!strings.Contains(err.Error(), "Release Controller v3 Run conflicts with a deploying legacy deployment") {
			t.Fatalf("v3 admission after legacy lock error=%v, want stable conflict", err)
		}
		assertTableCount(t, ctx, database, "release_preview_runs", legacyFirst.projectID, 0)

		v3First := seedLegacyControllerGateProject(t, ctx, database, actorID, "v3-first", "ready")
		v3Transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		v3RunID := uuid.New()
		if _, err := v3Transaction.ExecContext(ctx, previewRunInsertSQL,
			v3RunID, v3First.projectID, v3First.bundleID, v3First.bundleHash,
			"v3-first-"+v3RunID.String(), releaseOperationDigest("v3-first"), actorID,
		); err != nil {
			v3Transaction.Rollback()
			t.Fatalf("v3 admission did not acquire the shared project lock: %v", err)
		}
		legacyAfterV3 := make(chan error, 1)
		go func() {
			legacyWriter, beginErr := database.BeginTx(ctx, nil)
			if beginErr != nil {
				legacyAfterV3 <- beginErr
				return
			}
			defer legacyWriter.Rollback()
			if _, updateErr := legacyWriter.ExecContext(ctx,
				`UPDATE deployments SET status = 'deploying' WHERE id = $1`, v3First.deploymentID,
			); updateErr != nil {
				legacyAfterV3 <- updateErr
				return
			}
			_, insertErr := legacyWriter.ExecContext(ctx, legacyVersionProbeInsertSQL,
				uuid.New(), v3First.deploymentID, uuid.New(), uuid.New(),
				releaseOperationDigest("legacy-v3-first-workspace"), actorID,
			)
			if insertErr == nil {
				insertErr = legacyWriter.Commit()
			}
			legacyAfterV3 <- insertErr
		}()
		assertAdmissionWaitsForSharedProjectLock(t, legacyAfterV3)
		if err := v3Transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := receiveAdmissionResult(t, legacyAfterV3); err == nil ||
			!strings.Contains(err.Error(), "legacy preview deployment conflicts with active Release Controller v3 authority") {
			t.Fatalf("legacy admission after v3 lock error=%v, want stable conflict", err)
		}
		assertTableCount(t, ctx, database, "release_preview_runs", v3First.projectID, 1)
		assertTableCount(t, ctx, database, "deployment_versions", v3First.projectID, 0)

		production := seedLegacyControllerGateDeployment(t, ctx, database, actorID, v3First.projectID, "production", "ready")
		if _, err := database.ExecContext(ctx, legacyVersionProbeInsertSQL,
			uuid.New(), production, uuid.New(), uuid.New(),
			releaseOperationDigest("legacy-production-workspace"), actorID,
		); err == nil || !strings.Contains(err.Error(), "legacy production deployment is disabled; use Release Controller v3") {
			t.Fatalf("direct legacy production version error=%v, want v3-only rejection", err)
		}

		splitParent := seedLegacyControllerGateProject(t, ctx, database, actorID, "split-parent", "ready")
		if _, err := database.ExecContext(ctx, legacyVersionProbeInsertSQL,
			uuid.New(), splitParent.deploymentID, uuid.New(), uuid.New(),
			releaseOperationDigest("legacy-split-parent-workspace"), actorID,
		); err == nil || !strings.Contains(err.Error(), "requires a deploying parent") {
			t.Fatalf("ready parent accepted deploying legacy version: %v", err)
		}
	})

	t.Run("preexisting dual authority blocks the upgrade", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000058_zzzzzzzz.up.sql",
		)
		actorID := uuid.New()
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'legacy controller upgrade actor', 'not-used')
`, actorID, "legacy-controller-upgrade-"+uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		seed := seedLegacyControllerGateProject(t, ctx, database, actorID, "upgrade-conflict", "deploying")
		if _, err := database.ExecContext(ctx, previewRunInsertSQL,
			uuid.New(), seed.projectID, seed.bundleID, seed.bundleHash,
			"upgrade-conflict-"+uuid.NewString(), releaseOperationDigest("upgrade-conflict"), actorID,
		); err != nil {
			t.Fatalf("seed pre-migration dual authority: %v", err)
		}
		up, err := files.ReadFile("000059_legacy_deployment_release_controller_gate.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, applyErr := transaction.ExecContext(ctx, string(up))
		_ = transaction.Rollback()
		if applyErr == nil || !strings.Contains(applyErr.Error(), "deploying legacy and active v2 authority coexist") {
			t.Fatalf("dual-authority upgrade error=%v, want explicit fail-closed rejection", applyErr)
		}
		var triggerCount int
		if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM pg_trigger
WHERE tgrelid = 'deployment_versions'::regclass
  AND tgname = 'deployment_version_controller_singleflight_guard'
`).Scan(&triggerCount); err != nil {
			t.Fatal(err)
		}
		if triggerCount != 0 {
			t.Fatalf("failed upgrade leaked %d controller gate triggers", triggerCount)
		}
	})

	t.Run("preexisting split legacy parent and version blocks the upgrade", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(t, ctx, database, "000058_zzzzzzzz.up.sql")
		actorID := uuid.New()
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'legacy split upgrade actor', 'not-used')
`, actorID, "legacy-split-upgrade-"+uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		seed := seedLegacyControllerGateProject(t, ctx, database, actorID, "upgrade-split", "ready")
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			transaction.Rollback()
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, legacyVersionProbeInsertSQL,
			uuid.New(), seed.deploymentID, uuid.New(), uuid.New(),
			releaseOperationDigest("legacy-upgrade-split-workspace"), actorID,
		); err != nil {
			transaction.Rollback()
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		up, err := files.ReadFile("000059_legacy_deployment_release_controller_gate.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err = database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, applyErr := transaction.ExecContext(ctx, string(up))
		_ = transaction.Rollback()
		if applyErr == nil || !strings.Contains(applyErr.Error(), "deploying legacy version has a non-deploying parent") {
			t.Fatalf("split legacy upgrade error=%v, want explicit fail-closed rejection", applyErr)
		}
	})

	t.Run("upgrade scan serializes an older v2 writer", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(t, ctx, database, "000058_zzzzzzzz.up.sql")
		actorID := uuid.New()
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'legacy lock upgrade actor', 'not-used')
`, actorID, "legacy-lock-upgrade-"+uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		seed := seedLegacyControllerGateProject(t, ctx, database, actorID, "upgrade-lock", "deploying")
		writer, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		runID := uuid.New()
		if _, err := writer.ExecContext(ctx, previewRunInsertSQL,
			runID, seed.projectID, seed.bundleID, seed.bundleHash,
			"upgrade-lock-"+runID.String(), releaseOperationDigest("upgrade-lock"), actorID,
		); err != nil {
			writer.Rollback()
			t.Fatal(err)
		}
		up, err := files.ReadFile("000059_legacy_deployment_release_controller_gate.up.sql")
		if err != nil {
			writer.Rollback()
			t.Fatal(err)
		}
		migrationResult := make(chan error, 1)
		go func() {
			migration, beginErr := database.BeginTx(ctx, nil)
			if beginErr != nil {
				migrationResult <- beginErr
				return
			}
			defer migration.Rollback()
			_, applyErr := migration.ExecContext(ctx, string(up))
			if applyErr == nil {
				applyErr = migration.Commit()
			}
			migrationResult <- applyErr
		}()
		assertAdmissionWaitsForSharedProjectLock(t, migrationResult)
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
		applyErr := receiveAdmissionResult(t, migrationResult)
		if applyErr == nil || !strings.Contains(applyErr.Error(), "deploying legacy and active v2 authority coexist") {
			t.Fatalf("serialized upgrade error=%v, want post-lock conflict scan", applyErr)
		}
	})

	t.Run("empty downgrade restores the prior boundary", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000059_legacy_deployment_release_controller_gate.up.sql",
		)
		down, err := files.ReadFile("000059_legacy_deployment_release_controller_gate.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(down)); err != nil {
			transaction.Rollback()
			t.Fatalf("empty legacy/controller gate downgrade failed: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		var triggerCount int
		var restoredDefinition string
		if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_trigger
   WHERE tgrelid = 'deployment_versions'::regclass
     AND tgname = 'deployment_version_controller_singleflight_guard'),
  pg_get_functiondef('validate_release_delivery_run_insert_v2()'::regprocedure)
`).Scan(&triggerCount, &restoredDefinition); err != nil {
			t.Fatal(err)
		}
		if triggerCount != 0 || strings.Contains(restoredDefinition, "FROM projects") || strings.Contains(restoredDefinition, "status = 'deploying'") {
			t.Fatalf("empty downgrade did not restore the prior boundary: trigger=%d function=%s", triggerCount, restoredDefinition)
		}
	})
}

type legacyControllerGateSeed struct {
	projectID    uuid.UUID
	deploymentID uuid.UUID
	bundleID     uuid.UUID
	bundleHash   string
}

func seedLegacyControllerGateProject(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	actorID uuid.UUID,
	label, deploymentStatus string,
) legacyControllerGateSeed {
	t.Helper()
	projectID, bundleID := uuid.New(), uuid.New()
	bundleHash := releaseOperationDigest("legacy-controller-gate-" + label)
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, $2, $3)
`, projectID, "legacy controller gate "+label, actorID); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_bundles (
  id, schema_version, project_id, workspace_artifact_id, workspace_revision_id,
  workspace_content_hash, canonical_receipt_id, canonical_receipt_hash,
  release_artifacts, content_store, content_ref, content_hash, bundle_hash,
  created_by, created_at
) VALUES ($1, 'release-bundle/v1', $2, $3, $4, $5, $6, $7,
          $8::jsonb, 'blob', $9, $10, $11, $12, $13)
`, bundleID, projectID, uuid.New(), uuid.New(), releaseOperationDigest(label+"-workspace"),
		uuid.New(), releaseOperationDigest(label+"-receipt"), releaseOperationArtifacts(t),
		"blob://legacy-controller-gate/"+bundleID.String(), releaseOperationDigest(label+"-content"),
		bundleHash, actorID, time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)); err != nil {
		transaction.Rollback()
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	deploymentID := seedLegacyControllerGateDeployment(
		t, ctx, database, actorID, projectID, "preview", deploymentStatus,
	)
	return legacyControllerGateSeed{
		projectID: projectID, deploymentID: deploymentID,
		bundleID: bundleID, bundleHash: bundleHash,
	}
}

func seedLegacyControllerGateDeployment(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	actorID, projectID uuid.UUID,
	environment, status string,
) uuid.UUID {
	t.Helper()
	deploymentID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO deployments (
  id, project_id, environment, environment_ref, provider, status,
  version, created_by, created_at, updated_at
) VALUES ($1, $2, $3, 'legacy-gate', 'local-static', $4, 1, $5,
          statement_timestamp(), statement_timestamp())
`, deploymentID, projectID, environment, status, actorID); err != nil {
		t.Fatal(err)
	}
	return deploymentID
}

func assertAdmissionWaitsForSharedProjectLock(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("admission bypassed the shared project lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}

func receiveAdmissionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("admission remained blocked after the shared project lock was released")
		return nil
	}
}

func assertTableCount(t *testing.T, ctx context.Context, database *sql.DB, table string, projectID uuid.UUID, expected int) {
	t.Helper()
	query := `SELECT count(*) FROM ` + table
	if table == "deployment_versions" {
		query += ` AS version JOIN deployments AS deployment ON deployment.id = version.deployment_id WHERE deployment.project_id = $1`
	} else {
		query += ` WHERE project_id = $1`
	}
	var count int
	if err := database.QueryRowContext(ctx, query, projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("%s rows for project = %d, want %d", table, count, expected)
	}
}

const previewRunInsertSQL = `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v2', $2, $3, $4, $5, $6,
          'legacy/controller single-flight canary', 'queued', 1, $7, $7)
`

// The controller gate trigger sorts before the historical release-authority
// trigger, so these intentionally incomplete lineage values can only reach
// that older trigger if the new cross-writer gate fails to reject the insert.
const legacyVersionProbeInsertSQL = `
INSERT INTO deployment_versions (
  id, deployment_id, number, action,
  workspace_artifact_id, workspace_revision_id, workspace_content_hash,
  provider_ref, entry_path, checksum, file_count, total_bytes,
  environment_ref, environment_variable_names, status, message, created_by
) VALUES (
  $1, $2, 1, 'publish', $3, $4, $5,
  '', '', '', 0, 0, 'legacy-gate', '[]'::jsonb, 'deploying', '', $6
)
`
