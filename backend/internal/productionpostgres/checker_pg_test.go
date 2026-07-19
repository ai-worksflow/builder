package productionpostgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// This opt-in canary validates the dynamic catalog query against PostgreSQL.
// It does not require or claim a safe production posture.
func TestSessionPostureCatalogQueryRealPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open PostgreSQL test connection")
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		t.Fatal("connect PostgreSQL test connection")
	}
	if _, err := inspectSession(ctx, sqlQueryer{database: database}, "public"); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("inspect PostgreSQL catalogs: sqlstate=%s position=%d message=%s", postgresError.Code, postgresError.Position, postgresError.Message)
		}
		t.Fatalf("inspect PostgreSQL catalogs: %v", err)
	}
}
