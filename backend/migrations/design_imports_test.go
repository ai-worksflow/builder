package migrations

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestDesignImportMigrationDeclaresImmutableTenantScopedSnapshots(t *testing.T) {
	up, err := files.ReadFile("000013_design_imports.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000013_design_imports.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"CREATE TABLE design_imports",
		"design_import_project_request_unique",
		"prevent_design_import_snapshot_mutation",
		"validate_design_import_tenant_refs",
		"design_import_snapshot_immutable",
		"design_import_tenant_refs",
		"design_import_state_transition",
		"design_import_claim_shape",
		"design_import_independent_reviewer",
		"expected_output_proposal_id",
	} {
		if !strings.Contains(string(up), expected) {
			t.Fatalf("design import migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP FUNCTION IF EXISTS prevent_design_import_snapshot_mutation",
		"DROP FUNCTION IF EXISTS validate_design_import_tenant_refs",
		"DROP FUNCTION IF EXISTS validate_design_import_state_transition",
		"DROP FUNCTION IF EXISTS design_import_stage_rank",
		"DROP TABLE IF EXISTS design_imports",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("design import rollback is missing %q", expected)
		}
	}
}

func TestDesignImportMigrationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "design_import_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	scopedDSN := postgresDSNWithSearchPath(t, dsn, schema)
	database, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed in temporary schema: %v", err)
	}
	var tableName string
	if err := database.QueryRowContext(ctx, `SELECT to_regclass('design_imports')::text`).Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	if tableName != "design_imports" {
		t.Fatalf("expected design_imports table, got %q", tableName)
	}
	var triggers int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_trigger
WHERE tgrelid = 'design_imports'::regclass
  AND NOT tgisinternal
  AND tgname IN ('design_import_snapshot_immutable', 'design_import_tenant_refs', 'design_import_state_transition')
`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 3 {
		t.Fatalf("expected all design import invariant triggers, got %d", triggers)
	}
}

func postgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
