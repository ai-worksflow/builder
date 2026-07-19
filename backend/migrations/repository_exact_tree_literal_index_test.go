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

const repositoryExactTreeLiteralIndexMigration = "000062_repository_exact_tree_literal_index"

func TestRepositoryExactTreeLiteralIndexMigrationDeclaresCompleteImmutableTrigramIndex(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(repositoryExactTreeLiteralIndexMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile(repositoryExactTreeLiteralIndexMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public",
		"CREATE TABLE repository_exact_tree_literal_index_blobs",
		"PRIMARY KEY (project_id, content_hash)",
		"repository_exact_tree_literal_index_blob_body_shape",
		"USING gin ((body COLLATE \"C\") public.gin_trgm_ops)",
		"CREATE INDEX repository_exact_tree_literal_index_blob_ascii_body_trgm_idx",
		"translate(",
		"CREATE TABLE repository_exact_tree_literal_index_manifests",
		"PRIMARY KEY (project_id, tree_hash)",
		"tree_commitment text NOT NULL",
		"index_commitment text NOT NULL",
		"CREATE TABLE repository_exact_tree_literal_index_members",
		"path text COLLATE \"C\" NOT NULL",
		"CHECK (repository_path_is_safe(path))",
		"repository_exact_tree_literal_index_member_blob_fk",
		"publish_repository_exact_tree_literal_index_manifest",
		"cannot become ready before all members are complete",
		"BEFORE TRUNCATE ON repository_exact_tree_literal_index_blobs",
		"BEFORE TRUNCATE ON repository_exact_tree_literal_index_members",
		"BEFORE TRUNCATE ON repository_exact_tree_literal_index_manifests",
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE",
		"not repository source authority",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("exact-tree literal index migration is missing %q", required)
		}
	}
	downText := string(down)
	for _, required := range []string{
		"DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_member_insert_guard",
		"DROP FUNCTION IF EXISTS publish_repository_exact_tree_literal_index_manifest",
		"DROP TABLE IF EXISTS repository_exact_tree_literal_index_members",
		"DROP TABLE IF EXISTS repository_exact_tree_literal_index_manifests",
		"DROP TABLE IF EXISTS repository_exact_tree_literal_index_blobs",
		"leaves the extension installed",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("exact-tree literal index rollback is missing %q", required)
		}
	}
	if strings.Contains(downText, "DROP EXTENSION") {
		t.Fatal("feature rollback must not remove the shared pg_trgm extension")
	}
}

func TestRepositoryExactTreeLiteralIndexMigrationPostgresCanary(t *testing.T) {
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
	schema := "repository_literal_index_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
		t.Fatalf("apply migrations (pg_trgm is a required fail-closed prerequisite): %v", err)
	}

	var extensionSchema string
	if err := database.QueryRowContext(ctx, `
SELECT namespace.nspname
FROM pg_extension AS extension
JOIN pg_namespace AS namespace ON namespace.oid = extension.extnamespace
WHERE extension.extname = 'pg_trgm'`).Scan(&extensionSchema); err != nil {
		t.Fatalf("pg_trgm extension is unavailable after migration: %v", err)
	}
	if extensionSchema != "public" {
		t.Fatalf("pg_trgm extension schema=%q, want public", extensionSchema)
	}
	for _, table := range []string{
		"repository_exact_tree_literal_index_blobs",
		"repository_exact_tree_literal_index_manifests",
		"repository_exact_tree_literal_index_members",
	} {
		var actual string
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, table).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != table {
			t.Fatalf("table %s resolved as %q", table, actual)
		}
	}

	actorID, projectID, otherProjectID := uuid.New(), uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Literal index actor', 'not-used')`,
		actorID, "literal-index-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Literal index project', $3), ($2, 'Other literal index project', $3)`,
		projectID, otherProjectID, actorID); err != nil {
		t.Fatal(err)
	}

	textBody := "RareNeedleXYZ exact body"
	textHash := "sha256:" + strings.Repeat("a", 64)
	binaryHash := "sha256:" + strings.Repeat("b", 64)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id, content_hash, byte_size, is_text, body
) VALUES
  ($1, $2, octet_length($3::text), true, $3),
  ($1, $4, 8, false, NULL)`, projectID, textHash, textBody, binaryHash); err != nil {
		t.Fatalf("insert exact derived blobs: %v", err)
	}
	treeHash := "sha256:" + strings.Repeat("c", 64)
	treeCommitment := "sha256:" + strings.Repeat("d", 64)
	indexCommitment := "sha256:" + strings.Repeat("e", 64)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id, tree_hash, schema_version, status,
  file_count, text_file_count, skipped_file_count, total_bytes,
  tree_commitment, index_commitment
) VALUES (
  $1, $2, 'repository-exact-tree-literal-index/v1', 'building',
  2, 1, 1, octet_length($3::text) + 8, $4, $5
)`, projectID, treeHash, textBody, treeCommitment, indexCommitment); err != nil {
		t.Fatalf("insert building manifest: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_members (
  project_id, tree_hash, path, mode, content_hash, byte_size, indexed
) VALUES
  ($1, $2, 'assets/raw.bin', '100644', $3, 8, false),
  ($1, $2, 'src/main.ts', '100755', $4, octet_length($5::text), true)`,
		projectID, treeHash, binaryHash, textHash, textBody); err != nil {
		t.Fatalf("insert complete canonical members: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE repository_exact_tree_literal_index_manifests
SET status='ready', ready_at=statement_timestamp()
WHERE project_id=$1 AND tree_hash=$2`, projectID, treeHash); err != nil {
		t.Fatalf("publish complete exact-tree index: %v", err)
	}

	var caseSensitive, caseSensitiveMiss, caseInsensitive int
	if err := database.QueryRowContext(ctx, `
SELECT
  count(*) FILTER (WHERE (body COLLATE "C") LIKE ('%RareNeedleXYZ%' COLLATE "C") ESCAPE '!'),
  count(*) FILTER (WHERE (body COLLATE "C") LIKE ('%rareneedlexyz%' COLLATE "C") ESCAPE '!'),
  count(*) FILTER (WHERE translate(
    body COLLATE "C",
    'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
    'abcdefghijklmnopqrstuvwxyz'
  ) LIKE ('%rareneedlexyz%' COLLATE "C") ESCAPE '!')
FROM repository_exact_tree_literal_index_blobs
WHERE project_id=$1 AND is_text`, projectID).Scan(
		&caseSensitive, &caseSensitiveMiss, &caseInsensitive,
	); err != nil {
		t.Fatal(err)
	}
	if caseSensitive != 1 || caseSensitiveMiss != 0 || caseInsensitive != 1 {
		t.Fatalf("case semantics sensitive=%d miss=%d insensitive=%d", caseSensitive, caseSensitiveMiss, caseInsensitive)
	}

	assertExactTreeLiteralIndexMutationRejected(t, ctx, database,
		`UPDATE repository_exact_tree_literal_index_blobs SET byte_size=byte_size+1 WHERE project_id=$1 AND content_hash=$2`, projectID, textHash)
	assertExactTreeLiteralIndexMutationRejected(t, ctx, database,
		`UPDATE repository_exact_tree_literal_index_members SET mode='100644' WHERE project_id=$1 AND tree_hash=$2 AND path='src/main.ts'`, projectID, treeHash)
	assertExactTreeLiteralIndexMutationRejected(t, ctx, database,
		`DELETE FROM repository_exact_tree_literal_index_manifests WHERE project_id=$1 AND tree_hash=$2`, projectID, treeHash)
	assertExactTreeLiteralIndexMutationRejected(t, ctx, database,
		`TRUNCATE repository_exact_tree_literal_index_members`)
	assertExactTreeLiteralIndexMutationRejected(t, ctx, database, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id, content_hash, byte_size, is_text, body
) VALUES ($1, $2, 1, true, 'x')`, projectID, textHash)

	partialTreeHash := "sha256:" + strings.Repeat("f", 64)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id, tree_hash, schema_version, status,
  file_count, text_file_count, skipped_file_count, total_bytes,
  tree_commitment, index_commitment
) VALUES ($1,$2,'repository-exact-tree-literal-index/v1','building',1,1,0,$3,$4,$5)`,
		projectID, partialTreeHash, len(textBody), treeCommitment, indexCommitment); err != nil {
		t.Fatal(err)
	}
	assertExactTreeLiteralIndexMutationRejected(t, ctx, database, `
UPDATE repository_exact_tree_literal_index_manifests
SET status='ready', ready_at=statement_timestamp()
WHERE project_id=$1 AND tree_hash=$2`, projectID, partialTreeHash)
	assertExactTreeLiteralIndexMutationRejected(t, ctx, database, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id, tree_hash, schema_version, status,
  file_count, text_file_count, skipped_file_count, total_bytes,
  tree_commitment, index_commitment, ready_at
) VALUES ($1,$2,'repository-exact-tree-literal-index/v1','ready',0,0,0,0,$3,$4,statement_timestamp())`,
		projectID, "sha256:"+strings.Repeat("1", 64), treeCommitment, indexCommitment)

	foreignTreeHash := "sha256:" + strings.Repeat("2", 64)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id, tree_hash, schema_version, status,
  file_count, text_file_count, skipped_file_count, total_bytes,
  tree_commitment, index_commitment
) VALUES ($1,$2,'repository-exact-tree-literal-index/v1','building',1,1,0,$3,$4,$5)`,
		otherProjectID, foreignTreeHash, len(textBody), treeCommitment, indexCommitment); err != nil {
		t.Fatal(err)
	}
	assertExactTreeLiteralIndexMutationRejected(t, ctx, database, `
INSERT INTO repository_exact_tree_literal_index_members (
  project_id, tree_hash, path, mode, content_hash, byte_size, indexed
) VALUES ($1,$2,'src/foreign.ts','100644',$3,$4,true)`,
		otherProjectID, foreignTreeHash, textHash, len(textBody))

	// Give the planner enough normally-maintained, cross-project data to prefer
	// both final project-scoped GIN paths without disabling sequential scans.
	// These rows are derived-cache-only and need no tree membership for the
	// operator/index canary.
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id, content_hash, byte_size, is_text, body
)
SELECT CASE WHEN value % 2 = 0 THEN $1::uuid ELSE $2::uuid END,
       'sha256:' || md5(value::text) || md5(value::text),
       octet_length('ordinary filler body ' || value::text || repeat('x', 3000)),
       true,
       'ordinary filler body ' || value::text || repeat('x', 3000)
FROM generate_series(1, 30000) AS value`, projectID, otherProjectID); err != nil {
		t.Fatalf("seed trigram planner canary: %v", err)
	}
	// VACUUM flushes GIN's fast-update pending list as normal maintenance would;
	// the plan assertion therefore exercises the persisted posting tree instead
	// of depending on autovacuum timing under a parallel test run.
	if _, err := database.ExecContext(ctx, `VACUUM (ANALYZE) repository_exact_tree_literal_index_blobs`); err != nil {
		t.Fatal(err)
	}
	casePlan := explainExactTreeLiteralPlan(t, ctx, database, `
SELECT content_hash
FROM repository_exact_tree_literal_index_blobs
WHERE project_id = $1
  AND is_text
  AND (body COLLATE "C") LIKE ('%RareNeedleXYZ%' COLLATE "C") ESCAPE '!'`, projectID)
	if !strings.Contains(casePlan, "repository_exact_tree_literal_index_blob_project_body_trgm_idx") {
		t.Fatalf("normal maintained case-sensitive plan did not use project-fenced body GIN:\n%s", casePlan)
	}
	foldedPlan := explainExactTreeLiteralPlan(t, ctx, database, `
SELECT content_hash
FROM repository_exact_tree_literal_index_blobs
WHERE project_id = $1
  AND is_text
  AND translate(
    body COLLATE "C",
    'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
    'abcdefghijklmnopqrstuvwxyz'
  ) LIKE ('%rareneedlexyz%' COLLATE "C") ESCAPE '!'`, projectID)
	// PostgreSQL truncates this canonical identifier to NAMEDATALEN-1 bytes in
	// catalog and EXPLAIN output; to_regclass still resolves the long SQL name.
	if !strings.Contains(foldedPlan, "repository_exact_tree_literal_index_blob_project_ascii_body_trg") {
		t.Fatalf("normal maintained case-insensitive plan did not use project-fenced ASCII-folded body GIN:\n%s", foldedPlan)
	}

	downSQL, err := files.ReadFile(repositoryExactTreeLiteralIndexMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(downSQL)); err != nil {
		t.Fatalf("rollback exact-tree literal index migration: %v", err)
	}
	var remaining sql.NullString
	if err := database.QueryRowContext(ctx,
		`SELECT to_regclass('repository_exact_tree_literal_index_manifests')::text`,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining.Valid {
		t.Fatalf("exact-tree literal index manifest survived rollback as %q", remaining.String)
	}
	if err := database.QueryRowContext(ctx, `
SELECT namespace.nspname
FROM pg_extension AS extension
JOIN pg_namespace AS namespace ON namespace.oid = extension.extnamespace
WHERE extension.extname = 'pg_trgm'`).Scan(&extensionSchema); err != nil {
		t.Fatalf("rollback removed shared pg_trgm extension: %v", err)
	}
}

func assertExactTreeLiteralIndexMutationRejected(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	statement string,
	arguments ...any,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, statement, arguments...); err == nil {
		t.Fatalf("mutation unexpectedly succeeded: %s", strings.TrimSpace(statement))
	}
}

func explainExactTreeLiteralPlan(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	statement string,
	arguments ...any,
) string {
	t.Helper()
	rows, err := database.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+statement, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	lines := make([]string, 0, 16)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}
