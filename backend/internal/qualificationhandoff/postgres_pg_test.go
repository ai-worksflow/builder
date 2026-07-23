package qualificationhandoff

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// This canary drives the production adapter through the independently
// authenticated migration-82 LOGIN on a real PostgreSQL instance. The full
// successful state transition is covered by migration 82's seeded fixture;
// this package-level canary deliberately uses an absent opaque identity so it
// can prove the executable capability and its least-privilege boundary without
// rebuilding caller-authored upstream facts.
func TestPostgresStoreHandoffRoleCapabilityCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if err := base.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	roleLock, err := base.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roleLock.Close()
	if _, err := roleLock.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended('worksflow-model-governance-postgres-role-test', 0)
)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = roleLock.ExecContext(context.Background(), `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended('worksflow-model-governance-postgres-role-test', 0)
)`)
	}()

	const operator = "worksflow_qualification_handoff_operator"
	var operatorExists bool
	if err := base.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, operator).Scan(&operatorExists); err != nil {
		t.Fatal(err)
	}
	if operatorExists {
		t.Skipf("global role %s is already in use despite the role-test lock", operator)
	}
	if _, err := base.ExecContext(ctx, `CREATE ROLE `+handoffPostgresTestIdentifier(operator)+`
NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schema := "qualification_handoff_adapter_" + suffix
	login := "worksflow_qh_handoff_" + suffix
	password := "qualification-handoff-adapter-test-password"
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA `+handoffPostgresTestIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup := context.Background()
		_, _ = base.ExecContext(cleanup, `DROP SCHEMA IF EXISTS `+handoffPostgresTestIdentifier(schema)+` CASCADE`)
		_, _ = base.ExecContext(cleanup, `DROP ROLE IF EXISTS `+handoffPostgresTestIdentifier(login))
		_, _ = base.ExecContext(cleanup, `DROP ROLE IF EXISTS `+handoffPostgresTestIdentifier(operator))
	}()

	migrationDatabase, err := sql.Open("pgx", handoffPostgresTestDSN(t, dsn, "", "", schema))
	if err != nil {
		t.Fatal(err)
	}
	defer migrationDatabase.Close()
	applyHandoffPostgresMigrationPrefix(t, ctx, migrationDatabase)

	if _, err := base.ExecContext(ctx, `CREATE ROLE `+handoffPostgresTestIdentifier(login)+`
LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD `+
		handoffPostgresTestLiteral(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `GRANT `+handoffPostgresTestIdentifier(operator)+` TO `+
		handoffPostgresTestIdentifier(login)+` WITH INHERIT TRUE, SET FALSE, ADMIN FALSE`); err != nil {
		t.Fatal(err)
	}

	runtimeDatabase, err := sql.Open("pgx", handoffPostgresTestDSN(t, dsn, login, password, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeDatabase.Close()
	runtimeDatabase.SetMaxOpenConns(2)
	runtimeDatabase.SetMaxIdleConns(2)
	var sessionUser, currentRole string
	if err := runtimeDatabase.QueryRowContext(ctx, `
SELECT session_user::text, pg_catalog.current_setting('role')`).Scan(&sessionUser, &currentRole); err != nil {
		t.Fatal(err)
	}
	if sessionUser != login || currentRole != "none" {
		t.Fatalf("Handoff session posture = %q/%q", sessionUser, currentRole)
	}

	store, err := NewPostgresStore(runtimeDatabase, PostgresStoreConfig{
		SessionAffinityMode:   PostgresSessionAffinityDirect,
		MaxTransactionRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffID := uuid.New()
	if _, err := store.Inspect(ctx, handoffID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("real Inspect(absent) error = %v", err)
	}
	if _, err := store.Complete(ctx, handoffID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("real Complete(absent) error = %v", err)
	}
	if _, err := runtimeDatabase.ExecContext(ctx,
		`SELECT count(*) FROM qualification_promotion_v2_handoff_completions`); !handoffPostgresPermissionDenied(err) {
		t.Fatalf("Handoff LOGIN direct table read error = %v, want 42501", err)
	}
	var canComplete, canInspect, canPromote bool
	if err := runtimeDatabase.QueryRowContext(ctx, `SELECT
  pg_catalog.has_function_privilege(
    session_user,
    'complete_qualification_promotion_v2_handoff(uuid)',
    'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    session_user,
    'inspect_qualification_promotion_v2_handoff_completion(uuid)',
    'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    session_user,
    'consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)',
    'EXECUTE'
  )`).Scan(&canComplete, &canInspect, &canPromote); err != nil {
		t.Fatal(err)
	}
	if !canComplete || !canInspect || canPromote {
		t.Fatalf("Handoff capability set complete=%v inspect=%v promote=%v", canComplete, canInspect, canPromote)
	}
}

func applyHandoffPostgresMigrationPrefix(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `CREATE TABLE schema_migrations (
  version text PRIMARY KEY,
  checksum text NOT NULL,
  down_checksum text,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		base := filepath.Base(name)
		if base > "000082_qualification_handoff_v1.up.sql" {
			break
		}
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(contents)); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) {
				t.Fatalf("apply %s SQLSTATE=%s position=%d: %v", base, postgresError.Code, postgresError.Position, err)
			}
			t.Fatalf("apply %s: %v", base, err)
		}
	}
}

func handoffPostgresTestDSN(t *testing.T, dsn, user, password, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		if user != "" {
			parsed.User = url.UserPassword(user, password)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	if user != "" {
		t.Fatalf("role canary requires a URL PostgreSQL DSN, got %q", dsn)
	}
	return fmt.Sprintf("%s search_path=%s", dsn, schema)
}

func handoffPostgresTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func handoffPostgresTestLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func handoffPostgresPermissionDenied(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "42501"
}
