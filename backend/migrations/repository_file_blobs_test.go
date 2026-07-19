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

func TestRepositoryFileBlobsMigrationDeclaresTenantScopedImmutableCatalog(t *testing.T) {
	up, err := files.ReadFile("000026_repository_file_blobs.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000026_repository_file_blobs.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText, downText := string(up), string(down)
	for _, expected := range []string{
		"CREATE TABLE repository_file_blobs",
		"project_id uuid NOT NULL REFERENCES projects(id)",
		"content_object_hash text NOT NULL",
		"content_hash text NOT NULL",
		"repository_file_blob_owner_identity",
		"repository_file_blob_semantic_unique",
		"prevent_repository_file_blob_mutation",
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("file blob migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TRIGGER IF EXISTS repository_file_blob_immutable",
		"DROP FUNCTION IF EXISTS prevent_repository_file_blob_mutation",
		"DROP TABLE IF EXISTS repository_file_blobs",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("file blob rollback is missing %q", expected)
		}
	}
}

func TestRepositoryFileBlobsMigrationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "repository_file_blob_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
		t.Fatalf("migrations.Up failed in temporary schema: %v", err)
	}

	actorID, projectID, otherProjectID := uuid.New(), uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'File blob actor', 'not-used')
`, actorID, "file-blob-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'File blob project', $3), ($2, 'Other file blob project', $3)
`, projectID, otherProjectID, actorID); err != nil {
		t.Fatal(err)
	}

	blobID := uuid.New()
	rawHash := "sha256:" + strings.Repeat("a", 64)
	objectHash := "sha256:" + strings.Repeat("b", 64)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_file_blobs (
  id, project_id, store, owner_id, content_ref, content_object_hash,
  content_hash, byte_size, created_by
) VALUES ($1, $2, 'content', $1, $3, $4, $5, 7, $6)
`, blobID, projectID, "file-content-"+blobID.String(), objectHash, rawHash, actorID); err != nil {
		t.Fatalf("insert exact file blob registration: %v", err)
	}

	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_file_blobs (
  id, project_id, store, owner_id, content_ref, content_object_hash,
  content_hash, byte_size, created_by
) VALUES ($1, $2, 'content', $1, $3, $4, $5, 7, $6)
`, uuid.New(), projectID, "duplicate-semantic-"+uuid.NewString(), objectHash, rawHash, actorID); err == nil {
		t.Fatal("duplicate tenant-scoped semantic file content was accepted")
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_file_blobs (
  id, project_id, store, owner_id, content_ref, content_object_hash,
  content_hash, byte_size, created_by
) VALUES ($1, $2, 'content', $3, $4, $5, $6, 7, $7)
`, uuid.New(), projectID, uuid.New(), "wrong-owner-"+uuid.NewString(), objectHash, rawHash, actorID); err == nil {
		t.Fatal("file blob whose owner differs from row identity was accepted")
	}
	if _, err := database.ExecContext(ctx, `UPDATE repository_file_blobs SET byte_size = 8 WHERE id = $1`, blobID); err == nil {
		t.Fatal("immutable file blob registration was updated")
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM repository_file_blobs WHERE id = $1`, blobID); err == nil {
		t.Fatal("immutable file blob registration was deleted")
	}

	otherBlobID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_file_blobs (
  id, project_id, store, owner_id, content_ref, content_object_hash,
  content_hash, byte_size, created_by
) VALUES ($1, $2, 'content', $1, $3, $4, $5, 7, $6)
`, otherBlobID, otherProjectID, "other-file-content-"+otherBlobID.String(), objectHash, rawHash, actorID); err != nil {
		t.Fatalf("same content in another tenant should be independent: %v", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM repository_file_blobs
WHERE project_id = $1 AND content_hash = $2 AND byte_size = 7
`, projectID, rawHash).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("tenant lookup returned %d rows, want 1", count)
	}

	downSQL, err := files.ReadFile("000026_repository_file_blobs.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(downSQL)); err != nil {
		t.Fatalf("file blob rollback failed: %v", err)
	}
	var table sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT to_regclass('repository_file_blobs')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table.Valid {
		t.Fatalf("repository_file_blobs survived rollback as %q", table.String)
	}
}
