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

func TestReleasePreviewSingleFlightPostgresMigration(t *testing.T) {
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

	t.Run("duplicate nonterminal exact Bundle authority fails closed", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000056_release_delivery_operation_reconciliation.up.sql",
		)

		actorID, projectID, bundleID := uuid.New(), uuid.New(), uuid.New()
		bundleHash := releaseOperationDigest("preview-singleflight-duplicate-bundle")
		if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'preview single-flight actor', 'not-used')
`, actorID, "preview-singleflight-"+uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'preview single-flight project', $2)
`, projectID, actorID); err != nil {
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
`, bundleID, projectID, uuid.New(), uuid.New(), releaseOperationDigest("duplicate-workspace"),
			uuid.New(), releaseOperationDigest("duplicate-canonical-receipt"), releaseOperationArtifacts(t),
			"blob://preview-singleflight/"+bundleID.String(), releaseOperationDigest("duplicate-content"),
			bundleHash, actorID, time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)); err != nil {
			transaction.Rollback()
			t.Fatalf("seed isolated exact Bundle: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}

		for ordinal := 1; ordinal <= 2; ordinal++ {
			runID := uuid.New()
			if _, err := database.ExecContext(ctx, `
INSERT INTO release_preview_runs (
  id, schema_version, project_id, release_bundle_id, release_bundle_hash,
  request_key, request_hash, reason, state, version, created_by, updated_by
) VALUES ($1, 'release-preview-run/v2', $2, $3, $4, $5, $6,
          'historical duplicate exact Bundle authority', 'queued', 1, $7, $7)
`, runID, projectID, bundleID, bundleHash,
				"duplicate-preview-"+runID.String(), releaseOperationDigest("duplicate-preview-"+runID.String()), actorID); err != nil {
				t.Fatalf("seed duplicate Preview Run %d: %v", ordinal, err)
			}
		}

		up, err := files.ReadFile("000057_release_preview_singleflight.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err = database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, applyErr := transaction.ExecContext(ctx, string(up))
		_ = transaction.Rollback()
		if applyErr == nil || !strings.Contains(applyErr.Error(), "duplicate nonterminal exact Bundle authority exists") {
			t.Fatalf("duplicate authority migration error=%v, want explicit fail-closed rejection", applyErr)
		}
		var index sql.NullString
		if err := database.QueryRowContext(ctx,
			`SELECT to_regclass('release_preview_runs_one_nonterminal_bundle_idx')::text`,
		).Scan(&index); err != nil {
			t.Fatal(err)
		}
		if index.Valid {
			t.Fatalf("failed migration leaked partial index %q", index.String)
		}
	})

	t.Run("empty authority downgrade restores prior boundary", func(t *testing.T) {
		schema := releaseOperationTestSchema(t, ctx, base, dsn)
		database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		applyReleaseOperationMigrationsThrough(
			t, ctx, database, "000057_release_preview_singleflight.up.sql",
		)
		down, err := files.ReadFile("000057_release_preview_singleflight.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, string(down)); err != nil {
			transaction.Rollback()
			t.Fatalf("downgrade empty preview single-flight authority: %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		var index sql.NullString
		var functionDefinition string
		if err := database.QueryRowContext(ctx, `
SELECT
  to_regclass('release_preview_runs_one_nonterminal_bundle_idx')::text,
  pg_get_functiondef('validate_release_bundle_insert()'::regprocedure)
`).Scan(&index, &functionDefinition); err != nil {
			t.Fatal(err)
		}
		if index.Valid || !strings.Contains(functionDefinition, "NEW.created_at := statement_timestamp()") {
			t.Fatalf("empty downgrade index=%v function=%s", index, functionDefinition)
		}
	})
}
