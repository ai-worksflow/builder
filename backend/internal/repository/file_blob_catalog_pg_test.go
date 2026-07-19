package repository

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
	"github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGORMFileBlobCatalogIsTenantScopedAndDeduplicatesExactBytesPostgres(t *testing.T) {
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
	schema := "repository_file_catalog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	scopedDSN := repositoryCatalogDSNWithSearchPath(t, dsn, schema)
	sqlDatabase, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDatabase.Close()
	if err := migrations.Up(ctx, sqlDatabase); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDatabase}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	actorID, projectID, otherProjectID := uuid.New(), uuid.New(), uuid.New()
	if _, err := sqlDatabase.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Repository catalog actor', 'not-used')
`, actorID, "repository-catalog-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDatabase.ExecContext(ctx, `
INSERT INTO projects (id, name, created_by)
VALUES ($1, 'Repository catalog', $3), ($2, 'Other repository catalog', $3)
`, projectID, otherProjectID, actorID); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewGORMFileBlobCatalog(database)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	firstID := uuid.NewString()
	firstPointer := FileBlobPointer{
		Store: FileContentStore, Ref: "file-object-" + firstID, OwnerID: firstID,
		ContentHash: digestFixture("catalog-raw"), ByteSize: 12,
		ContentObjectHash: digestFixture("catalog-object"),
	}
	first, err := catalog.RegisterFileBlob(ctx, FileBlobRegistration{
		ID: firstID, ProjectID: projectID.String(), Pointer: firstPointer,
		CreatedBy: actorID.String(), CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("register first blob: %v", err)
	}
	if first != firstPointer {
		t.Fatalf("registered pointer = %#v, want %#v", first, firstPointer)
	}

	duplicateID := uuid.NewString()
	duplicatePointer := FileBlobPointer{
		Store: FileContentStore, Ref: "file-object-" + duplicateID, OwnerID: duplicateID,
		ContentHash: firstPointer.ContentHash, ByteSize: firstPointer.ByteSize,
		ContentObjectHash: digestFixture("duplicate-object"),
	}
	duplicate, err := catalog.RegisterFileBlob(ctx, FileBlobRegistration{
		ID: duplicateID, ProjectID: projectID.String(), Pointer: duplicatePointer,
		CreatedBy: actorID.String(), CreatedAt: createdAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("deduplicate semantic blob: %v", err)
	}
	if duplicate != firstPointer {
		t.Fatalf("duplicate returned %#v, want first pointer %#v", duplicate, firstPointer)
	}
	found, ok, err := catalog.FindFileBlob(ctx, projectID.String(), firstPointer.ContentHash, firstPointer.ByteSize)
	if err != nil || !ok || found != firstPointer {
		t.Fatalf("find exact blob: found=%#v ok=%v err=%v", found, ok, err)
	}

	otherID := uuid.NewString()
	otherPointer := FileBlobPointer{
		Store: FileContentStore, Ref: "file-object-" + otherID, OwnerID: otherID,
		ContentHash: firstPointer.ContentHash, ByteSize: firstPointer.ByteSize,
		ContentObjectHash: digestFixture("other-tenant-object"),
	}
	other, err := catalog.RegisterFileBlob(ctx, FileBlobRegistration{
		ID: otherID, ProjectID: otherProjectID.String(), Pointer: otherPointer,
		CreatedBy: actorID.String(), CreatedAt: createdAt,
	})
	if err != nil || other != otherPointer {
		t.Fatalf("register same bytes in other tenant: pointer=%#v err=%v", other, err)
	}
	if _, ok, err := catalog.FindFileBlob(ctx, uuid.NewString(), firstPointer.ContentHash, firstPointer.ByteSize); err != nil || ok {
		t.Fatalf("cross-tenant lookup leaked a blob: ok=%v err=%v", ok, err)
	}

	wrongOwner := firstPointer
	wrongOwner.OwnerID = uuid.NewString()
	if _, err := catalog.RegisterFileBlob(ctx, FileBlobRegistration{
		ID: uuid.NewString(), ProjectID: projectID.String(), Pointer: wrongOwner,
		CreatedBy: actorID.String(), CreatedAt: createdAt,
	}); err == nil {
		t.Fatal("catalog accepted pointer whose owner differs from registration ID")
	}
}

func repositoryCatalogDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if parsed, err := url.Parse(strings.TrimSpace(dsn)); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
