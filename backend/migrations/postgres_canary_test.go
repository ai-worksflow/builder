package migrations

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// A full `go test ./...` run starts PostgreSQL canaries from several packages.
// They intentionally share the production advisory lock, so a canary may spend
// time queued behind otherwise healthy migrations in another test process. Keep
// that queue and migration work bounded independently from each canary's own
// assertion deadline.
const postgresCanaryMigrationTimeout = 3 * time.Minute

func applyPostgresMigrationsForCanary(t *testing.T, database *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), postgresCanaryMigrationTimeout)
	defer cancel()
	if err := Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed in temporary schema: %v", err)
	}
}
