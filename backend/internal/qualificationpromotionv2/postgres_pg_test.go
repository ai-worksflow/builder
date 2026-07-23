package qualificationpromotionv2

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

// This canary runs the production adapter through an independently
// authenticated Promotion LOGIN against the real migration-81 routines. A
// successful aggregate append is covered by the migration fixture; using
// absent opaque IDs here proves that the adapter reaches the capability
// boundary and maps WPV03 without needing caller-authored authority rows.
func TestPostgresStorePromotionRoleCapabilityCanary(t *testing.T) {
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

	// Global role names are shared by isolated-schema migration tests. Use the
	// same lock namespace as the newer posture canaries before creating,
	// granting, or dropping the Promotion operator role.
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

	const operator = "worksflow_qualification_promotion_operator"
	var operatorExists bool
	if err := base.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, operator).Scan(&operatorExists); err != nil {
		t.Fatal(err)
	}
	createdOperator := false
	if !operatorExists {
		if _, err := base.ExecContext(ctx, `CREATE ROLE `+promotionV2PostgresIdentifier(operator)+`
NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
			t.Fatal(err)
		}
		createdOperator = true
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schema := "qualification_promotion_v2_adapter_" + suffix
	login := "worksflow_qpv2_" + suffix
	password := "qualification-promotion-v2-adapter-test-password"
	defer func() {
		cleanup := context.Background()
		_, _ = base.ExecContext(cleanup, `DROP SCHEMA IF EXISTS `+promotionV2PostgresIdentifier(schema)+` CASCADE`)
		_, _ = base.ExecContext(cleanup, `DROP ROLE IF EXISTS `+promotionV2PostgresIdentifier(login))
		if createdOperator {
			_, _ = base.ExecContext(cleanup, `DROP ROLE IF EXISTS `+promotionV2PostgresIdentifier(operator))
		}
	}()
	if _, err := base.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA `+promotionV2PostgresIdentifier(schema)); err != nil {
		t.Fatal(err)
	}

	migrationDatabase, err := sql.Open("pgx", promotionV2PostgresDSN(t, dsn, "", "", schema))
	if err != nil {
		t.Fatal(err)
	}
	defer migrationDatabase.Close()
	applyPromotionV2PostgresMigrationPrefix(t, ctx, migrationDatabase)

	if _, err := base.ExecContext(ctx, `CREATE ROLE `+promotionV2PostgresIdentifier(login)+`
LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD `+
		promotionV2PostgresLiteral(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := base.ExecContext(ctx, `GRANT `+promotionV2PostgresIdentifier(operator)+` TO `+
		promotionV2PostgresIdentifier(login)+` WITH INHERIT TRUE, SET FALSE, ADMIN FALSE`); err != nil {
		t.Fatal(err)
	}

	runtimeDatabase, err := sql.Open("pgx", promotionV2PostgresDSN(t, dsn, login, password, schema))
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
		t.Fatalf("Promotion session posture = %q/%q", sessionUser, currentRole)
	}

	store, err := NewPostgresStore(runtimeDatabase, PostgresStoreConfig{
		SessionAffinityMode:   PostgresSessionAffinityDirect,
		MaxTransactionRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := ConsumeCommand{
		OperationID: uuid.New(), WorkflowInputAuthorityID: uuid.New(), PlanAuthorityID: uuid.New(),
		HandoffID: uuid.New(), OutputRevisionID: uuid.New(),
	}
	if _, err := store.InspectOperation(ctx, command.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("real InspectOperation(absent) error = %v", err)
	}
	if _, err := store.Consume(ctx, command); !errors.Is(err, ErrNotReady) {
		t.Fatalf("real Consume(absent authority) error = %v, want ErrNotReady", err)
	}
	if _, err := store.InspectOperation(ctx, command.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("real post-failure InspectOperation() error = %v", err)
	}

	var canConsume, canInspect, canInspectHistory, canResolveHandoff, canAssertHandoff, canLegacyConsume bool
	if err := runtimeDatabase.QueryRowContext(ctx, `SELECT
  pg_catalog.has_function_privilege(
    session_user, 'consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)', 'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    session_user, 'inspect_qualification_promotion_v2_operation(uuid)', 'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    session_user, 'inspect_historical_qualification_promotion_v1_operation(uuid)', 'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    session_user, 'resolve_qualification_promotion_v2_handoff(uuid)', 'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    session_user, 'assert_pending_qualification_promotion_v2_handoff(uuid)', 'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    session_user,
    'consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb)',
    'EXECUTE'
  )`).Scan(
		&canConsume,
		&canInspect,
		&canInspectHistory,
		&canResolveHandoff,
		&canAssertHandoff,
		&canLegacyConsume,
	); err != nil {
		t.Fatal(err)
	}
	if !canConsume || !canInspect || !canInspectHistory || canResolveHandoff || canAssertHandoff || canLegacyConsume {
		t.Fatalf(
			"Promotion capabilities consume=%v inspect=%v history=%v resolve-handoff=%v assert-handoff=%v legacy=%v",
			canConsume,
			canInspect,
			canInspectHistory,
			canResolveHandoff,
			canAssertHandoff,
			canLegacyConsume,
		)
	}

	for _, table := range []string{
		"artifact_revision_identity_reservations",
		"qualification_promotion_v2_independent_receipts",
		"qualification_promotion_v2_consumptions",
		"qualification_promotion_v2_consumption_independent_receipts",
		"qualification_promotion_v2_handoffs",
		"qualification_promotion_v2_identity_reservations",
	} {
		var canRead, canWrite bool
		if err := runtimeDatabase.QueryRowContext(ctx, `SELECT
  pg_catalog.has_table_privilege(session_user, $1, 'SELECT'),
  pg_catalog.has_table_privilege(session_user, $1, 'INSERT,UPDATE,DELETE,TRUNCATE')`,
			schema+"."+table,
		).Scan(&canRead, &canWrite); err != nil {
			t.Fatal(err)
		}
		if canRead || canWrite {
			t.Fatalf("Promotion LOGIN has direct table capability on %s: read=%v write=%v", table, canRead, canWrite)
		}
	}
	if _, err := runtimeDatabase.ExecContext(ctx,
		`SELECT count(*) FROM qualification_promotion_v2_consumptions`); !promotionV2PostgresPermissionDenied(err) {
		t.Fatalf("Promotion LOGIN direct table read error = %v, want 42501", err)
	}
	if _, err := runtimeDatabase.ExecContext(ctx,
		`SELECT * FROM resolve_qualification_promotion_v2_handoff($1)`, command.HandoffID,
	); !promotionV2PostgresPermissionDenied(err) {
		t.Fatalf("Promotion LOGIN private Handoff capability error = %v, want 42501", err)
	}
}

func applyPromotionV2PostgresMigrationPrefix(t *testing.T, ctx context.Context, database *sql.DB) {
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
		if base > "000081_qualification_promotion_v2.up.sql" {
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

func promotionV2PostgresDSN(t *testing.T, dsn, user, password, schema string) string {
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

func promotionV2PostgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func promotionV2PostgresLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func promotionV2PostgresPermissionDenied(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "42501"
}
