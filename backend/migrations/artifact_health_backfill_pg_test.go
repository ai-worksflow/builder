package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestArtifactHealthBackfillPostgresCanary(t *testing.T) {
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
	schema := "artifact_health_backfill_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	userID := uuid.New()
	projectID := uuid.New()
	missingID := uuid.New()
	preservedID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Artifact Health Owner', 'not-used')
`, userID, "artifact-health-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id, name, lifecycle, version, created_by)
VALUES ($1, 'Artifact health canary', 'active', 1, $2)
`, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifacts (id, project_id, kind, artifact_key, title, lifecycle, version, created_by)
VALUES
  ($1, $2, 'project_brief', 'HEALTH-MISSING', 'Missing health', 'active', 1, $3),
  ($4, $2, 'product_requirements', 'HEALTH-PRESERVED', 'Preserved health', 'active', 1, $3)
`, missingID, projectID, userID, preservedID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_health (
  artifact_id, sync_status, delivery_status, finding_count, blocking_count, report, computed_at
) VALUES ($1, 'blocked', 'blocked', 7, 2, '{"sentinel":true}'::jsonb, '2026-07-01T00:00:00Z')
`, preservedID); err != nil {
		t.Fatal(err)
	}

	up, err := files.ReadFile("000020_backfill_artifact_health.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("first idempotent reapply: %v", err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("second idempotent reapply: %v", err)
	}

	var missingRows int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM artifacts
LEFT JOIN artifact_health ON artifact_health.artifact_id = artifacts.id
WHERE artifact_health.artifact_id IS NULL
`).Scan(&missingRows); err != nil {
		t.Fatal(err)
	}
	if missingRows != 0 {
		t.Fatalf("artifact health backfill left %d missing rows", missingRows)
	}
	var syncStatus, deliveryStatus, report string
	var findingCount, blockingCount int
	var computedAt time.Time
	if err := database.QueryRowContext(ctx, `
SELECT sync_status, delivery_status, finding_count, blocking_count, report::text, computed_at
FROM artifact_health
WHERE artifact_id = $1
`, preservedID).Scan(
		&syncStatus, &deliveryStatus, &findingCount, &blockingCount, &report, &computedAt,
	); err != nil {
		t.Fatal(err)
	}
	if syncStatus != "blocked" || deliveryStatus != "blocked" || findingCount != 7 ||
		blockingCount != 2 || report != `{"sentinel": true}` ||
		!computedAt.Equal(time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf(
			"existing health was overwritten: sync=%q delivery=%q findings=%d blocking=%d report=%s computedAt=%s",
			syncStatus, deliveryStatus, findingCount, blockingCount, report, computedAt,
		)
	}

}
