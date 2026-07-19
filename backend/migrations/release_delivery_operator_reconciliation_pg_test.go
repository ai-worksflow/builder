package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestReleaseDeliveryOperatorReconciliationPostgresMigration(t *testing.T) {
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
	schema := releaseOperationTestSchema(t, ctx, base, dsn)
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	applyReleaseOperationMigrationsThrough(
		t, ctx, database, "000058_release_delivery_operator_reconciliation.up.sql",
	)
	for _, object := range []string{
		"release_delivery_reconciliation_cases",
		"release_delivery_reconciliation_case_insert_guard",
		"release_delivery_operation_attempt_00_reconcile_only_guard",
	} {
		var present bool
		if err := database.QueryRowContext(ctx, `
SELECT to_regclass($1) IS NOT NULL OR EXISTS (
  SELECT 1 FROM pg_trigger WHERE tgname = $1 AND NOT tgisinternal
)
`, object).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if !present {
			t.Fatalf("migration did not create %s", object)
		}
	}
}
