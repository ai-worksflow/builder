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

func TestMigrationAdvisoryLockReleaseIgnoresCanceledCallerContext(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(2)
	defer database.Close()

	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	canceled, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	if canceled.Err() == nil {
		t.Fatal("caller context did not cancel")
	}
	// The cleanup helper deliberately owns its bounded context. A canceled
	// migration caller cannot strand the session-level lock in the pool.
	releaseMigrationAdvisoryLock(connection)
	defer connection.Close()

	// Keep the original connection reserved so this probe must use a different
	// physical PostgreSQL backend; advisory locks are re-entrant per session.
	probe, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	var acquired bool
	if err := probe.QueryRowContext(
		ctx,
		"SELECT pg_try_advisory_lock($1)",
		advisoryLockID,
	).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("migration advisory lock remained held after bounded cleanup")
	}
	if _, err := probe.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockID); err != nil {
		t.Fatal(err)
	}
}
