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

const repositoryExactTreeLiteralIndexProjectGINMigration = "000065_repository_exact_tree_literal_index_project_gin"

func TestRepositoryExactTreeLiteralIndexProjectGINMigrationDeclaresTenantScopedPostingLists(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(repositoryExactTreeLiteralIndexProjectGINMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile(repositoryExactTreeLiteralIndexProjectGINMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"CREATE EXTENSION IF NOT EXISTS btree_gin WITH SCHEMA public",
		"repository_exact_tree_literal_index_blob_project_body_trgm_idx",
		"repository_exact_tree_literal_index_blob_project_ascii_body_trgm_idx",
		"project_id,",
		"public.gin_trgm_ops",
		"DROP INDEX repository_exact_tree_literal_index_blob_body_trgm_idx",
		"DROP INDEX repository_exact_tree_literal_index_blob_ascii_body_trgm_idx",
		"Project-fenced",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("project-scoped trigram migration is missing %q", required)
		}
	}
	downText := string(down)
	for _, required := range []string{
		"CREATE INDEX repository_exact_tree_literal_index_blob_body_trgm_idx",
		"CREATE INDEX repository_exact_tree_literal_index_blob_ascii_body_trgm_idx",
		"DROP INDEX repository_exact_tree_literal_index_blob_project_ascii_body_trgm_idx",
		"DROP INDEX repository_exact_tree_literal_index_blob_project_body_trgm_idx",
		"leaves the extension installed",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("project-scoped trigram rollback is missing %q", required)
		}
	}
	if strings.Contains(downText, "DROP EXTENSION") {
		t.Fatal("feature rollback must not remove the shared btree_gin extension")
	}
}

func TestRepositoryExactTreeLiteralIndexProjectGINMigrationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "repository_literal_project_gin_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Up(ctx, database); err != nil {
		t.Fatalf("apply migrations (btree_gin is a required fail-closed prerequisite): %v", err)
	}

	var extensionSchema string
	if err := database.QueryRowContext(ctx, `
SELECT namespace.nspname
FROM pg_extension AS extension
JOIN pg_namespace AS namespace ON namespace.oid = extension.extnamespace
WHERE extension.extname = 'btree_gin'`).Scan(&extensionSchema); err != nil {
		t.Fatalf("btree_gin extension is unavailable after migration: %v", err)
	}
	if extensionSchema != "public" {
		t.Fatalf("btree_gin extension schema=%q, want public", extensionSchema)
	}

	for _, index := range []string{
		"repository_exact_tree_literal_index_blob_project_body_trgm_idx",
		"repository_exact_tree_literal_index_blob_project_ascii_body_trgm_idx",
	} {
		assertProjectScopedExactTreeLiteralGIN(t, ctx, database, index)
	}
	for _, removed := range []string{
		"repository_exact_tree_literal_index_blob_body_trgm_idx",
		"repository_exact_tree_literal_index_blob_ascii_body_trgm_idx",
	} {
		var relation sql.NullString
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, removed).Scan(&relation); err != nil {
			t.Fatal(err)
		}
		if relation.Valid {
			t.Fatalf("global body-only trigram index survived as %q", relation.String)
		}
	}

	if _, err := database.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	actorID, projectID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Project GIN actor', 'not-used')`,
		actorID, "project-gin-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Project GIN project', $2)`, projectID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id, content_hash, byte_size, is_text, body
)
SELECT
  $1,
  'sha256:' || lpad(to_hex(value), 64, '0'),
  octet_length(CASE WHEN value = 25000 THEN 'tenantneedle-' || value::text ELSE 'ordinary-' || value::text END),
  true,
  CASE WHEN value = 25000 THEN 'tenantneedle-' || value::text ELSE 'ordinary-' || value::text END
FROM generate_series(1, 50000) AS values(value);
`, projectID); err != nil {
		t.Fatalf("seed project-scoped planner corpus: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ANALYZE repository_exact_tree_literal_index_blobs`); err != nil {
		t.Fatal(err)
	}
	rows, err := database.QueryContext(ctx, `
EXPLAIN (COSTS OFF)
SELECT content_hash
FROM repository_exact_tree_literal_index_blobs
WHERE project_id = $1
  AND is_text
  AND (body COLLATE "C") LIKE ('%tenantneedle%' COLLATE "C") ESCAPE '!'`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "repository_exact_tree_literal_index_blob_project_body_trgm_idx") {
		t.Fatalf("project-fenced literal lookup did not select the composite GIN:\n%s", plan.String())
	}

	downSQL, err := files.ReadFile(repositoryExactTreeLiteralIndexProjectGINMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(downSQL)); err != nil {
		t.Fatalf("rollback project-scoped trigram migration: %v", err)
	}
	var restored string
	if err := database.QueryRowContext(ctx,
		`SELECT to_regclass('repository_exact_tree_literal_index_blob_body_trgm_idx')::text`,
	).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != "repository_exact_tree_literal_index_blob_body_trgm_idx" {
		t.Fatalf("body-only rollback index resolved as %q", restored)
	}
	var retainedExtension bool
	if err := database.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname='btree_gin')`,
	).Scan(&retainedExtension); err != nil {
		t.Fatal(err)
	}
	if !retainedExtension {
		t.Fatal("rollback removed the shared btree_gin extension")
	}
}

func assertProjectScopedExactTreeLiteralGIN(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	index string,
) {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT operator_class.opcname, namespace.nspname
FROM pg_index AS index
JOIN pg_class AS relation ON relation.oid = index.indexrelid
JOIN unnest(index.indclass) WITH ORDINALITY AS classes(operator_class_id, position)
  ON true
JOIN pg_opclass AS operator_class ON operator_class.oid = classes.operator_class_id
JOIN pg_namespace AS namespace ON namespace.oid = operator_class.opcnamespace
WHERE relation.oid = to_regclass($1)
ORDER BY classes.position`, index)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actual []string
	for rows.Next() {
		var operatorClass, namespace string
		if err := rows.Scan(&operatorClass, &namespace); err != nil {
			t.Fatal(err)
		}
		actual = append(actual, namespace+"."+operatorClass)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"public.uuid_ops", "public.gin_trgm_ops"}
	if len(actual) != len(want) || actual[0] != want[0] || actual[1] != want[1] {
		t.Fatalf("index %s operator classes=%v, want %v", index, actual, want)
	}
}
