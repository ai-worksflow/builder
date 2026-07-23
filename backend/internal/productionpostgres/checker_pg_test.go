package productionpostgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/migrations"
)

// This opt-in canary proves that the production LOGIN -> NOLOGIN operator
// shape can execute only through inheritance. In particular, migration 80's
// SECURITY DEFINER caller guard must accept the original LOGIN while a direct
// SET ROLE attempt remains impossible.
func TestRuntimeOperatorLoginMembershipRealPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	var canProvisionRoles bool
	if err := admin.QueryRowContext(ctx, `
SELECT rolsuper OR rolcreaterole
FROM pg_catalog.pg_roles
WHERE rolname = current_user`).Scan(&canProvisionRoles); err != nil {
		t.Fatalf("inspect PostgreSQL test role: %v", err)
	}
	if !canProvisionRoles {
		t.Skip("runtime operator canary requires a role that can provision fixture logins")
	}

	lockConnection, err := admin.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve advisory-lock connection: %v", err)
	}
	defer lockConnection.Close()
	const advisoryLockID int64 = 804357060326886689
	if _, err := lockConnection.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, advisoryLockID); err != nil {
		t.Fatalf("lock production-posture fixture: %v", err)
	}
	defer lockConnection.ExecContext(context.Background(), `SELECT pg_catalog.pg_advisory_unlock($1)`, advisoryLockID)

	stableRoles := []string{
		applicationGroupRole,
		migrationOwnerRole,
		gcOperatorRole,
		goldenFaultRole,
		promotionOperatorRole,
		policyOperatorRole,
		inputPrecommitOperatorRole,
		sourceVerifierOperatorRole,
		credentialResolverOperatorRole,
		handoffOperatorRole,
	}
	createdStableRoles := make([]string, 0, len(stableRoles))
	for _, role := range stableRoles {
		var exists bool
		if err := admin.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			t.Fatalf("inspect stable role %s: %v", role, err)
		}
		if exists {
			continue
		}
		if _, err := admin.ExecContext(ctx, `CREATE ROLE `+role+` NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`); err != nil {
			t.Fatalf("create stable role %s: %v", role, err)
		}
		createdStableRoles = append(createdStableRoles, role)
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "runtime_operator_" + suffix
	type runtimeCase struct {
		name       string
		group      string
		password   string
		invocation string
		argument   any
		database   *sql.DB
	}
	runtimes := []runtimeCase{
		{
			name:       "promotion_login_" + suffix,
			group:      promotionOperatorRole,
			password:   "promotion_" + suffix,
			invocation: "inspect_qualification_promotion_v2_operation",
			argument:   uuid.NewString(),
		},
		{
			name:       "input_precommit_login_" + suffix,
			group:      inputPrecommitOperatorRole,
			password:   "input_" + suffix,
			invocation: "inspect_qualification_input_precommit_operation_v1",
			argument:   uuid.NewString(),
		},
		{
			name:       "source_verifier_login_" + suffix,
			group:      sourceVerifierOperatorRole,
			password:   "source_" + suffix,
			invocation: "inspect_qualification_input_source_receipt_v1",
			argument:   "sha256:" + strings.Repeat("0", 64),
		},
		{
			name:       "credential_resolver_login_" + suffix,
			group:      credentialResolverOperatorRole,
			password:   "credential_" + suffix,
			invocation: "inspect_qualification_input_credential_receipt_v1",
			argument:   "sha256:" + strings.Repeat("1", 64),
		},
		{
			name:       "handoff_login_" + suffix,
			group:      handoffOperatorRole,
			password:   "handoff_" + suffix,
			invocation: "inspect_qualification_promotion_v2_handoff_completion",
			argument:   uuid.NewString(),
		},
	}
	var migrationDatabase *sql.DB
	defer func() {
		if migrationDatabase != nil {
			_ = migrationDatabase.Close()
		}
		for index := range runtimes {
			if runtimes[index].database != nil {
				_ = runtimes[index].database.Close()
			}
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schemaName+` CASCADE`)
		for _, runtime := range runtimes {
			_, _ = admin.ExecContext(cleanupCtx, `DROP OWNED BY `+runtime.name)
			_, _ = admin.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS `+runtime.name)
		}
		for index := len(createdStableRoles) - 1; index >= 0; index-- {
			role := createdStableRoles[index]
			_, _ = admin.ExecContext(cleanupCtx, `DROP OWNED BY `+role)
			_, _ = admin.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS `+role)
		}
	}()

	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schemaName); err != nil {
		t.Fatalf("create runtime-operator schema: %v", err)
	}
	migrationDatabase = openRuntimeOperatorDatabase(
		t,
		dsn,
		runtimeDSNUser(t, dsn),
		runtimeDSNPassword(t, dsn),
		schemaName,
	)
	if err := migrations.Up(ctx, migrationDatabase); err != nil {
		t.Fatalf("apply current migrations for runtime-operator canary: %v", err)
	}

	for index := range runtimes {
		runtime := &runtimes[index]
		if _, err := admin.ExecContext(ctx, fmt.Sprintf(
			`CREATE ROLE %s LOGIN INHERIT PASSWORD '%s' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`,
			runtime.name,
			runtime.password,
		)); err != nil {
			t.Fatalf("create runtime login %s: %v", runtime.name, err)
		}
		if _, err := admin.ExecContext(ctx, fmt.Sprintf(
			`GRANT %s TO %s WITH INHERIT TRUE, SET FALSE, ADMIN FALSE`,
			runtime.group,
			runtime.name,
		)); err != nil {
			t.Fatalf("grant exact operator membership to %s: %v", runtime.name, err)
		}
		runtime.database = openRuntimeOperatorDatabase(
			t,
			dsn,
			runtime.name,
			runtime.password,
			schemaName,
		)
		if err := runtime.database.PingContext(ctx); err != nil {
			t.Fatalf("connect as runtime login %s: %v", runtime.name, err)
		}

		var currentRole string
		var sessionRole string
		var roleSetting string
		var inherits bool
		var membershipCount int
		var exactMembershipCount int
		var groupMemberCount int
		var canUseGroup bool
		var canSetGroup bool
		if err := runtime.database.QueryRowContext(ctx, `
SELECT
  current_user::text,
  session_user::text,
  pg_catalog.current_setting('role'),
  login.rolinherit,
  (
    SELECT pg_catalog.count(*)::integer
    FROM pg_catalog.pg_auth_members AS membership
    WHERE membership.member = login.oid
  ),
  (
    SELECT pg_catalog.count(*)::integer
    FROM pg_catalog.pg_auth_members AS membership
    WHERE membership.member = login.oid
      AND membership.inherit_option
      AND NOT membership.set_option
      AND NOT membership.admin_option
  ),
	(
	  SELECT pg_catalog.count(*)::integer
	  FROM pg_catalog.pg_auth_members AS membership
	  JOIN pg_catalog.pg_roles AS operator ON operator.oid = membership.roleid
	  WHERE operator.rolname = $1
	),
  pg_catalog.pg_has_role(session_user, $1::name, 'USAGE'),
  pg_catalog.pg_has_role(session_user, $1::name, 'SET')
FROM pg_catalog.pg_roles AS login
WHERE login.rolname = session_user`, runtime.group).Scan(
			&currentRole,
			&sessionRole,
			&roleSetting,
			&inherits,
			&membershipCount,
			&exactMembershipCount,
			&groupMemberCount,
			&canUseGroup,
			&canSetGroup,
		); err != nil {
			t.Fatalf("inspect runtime membership for %s: %v", runtime.name, err)
		}
		if currentRole != runtime.name || sessionRole != runtime.name || roleSetting != "none" ||
			!inherits || membershipCount != 1 || exactMembershipCount != 1 ||
			groupMemberCount != 1 || !canUseGroup || canSetGroup {
			t.Fatalf(
				"runtime membership for %s = current=%q session=%q role=%q inherit=%t memberships=%d/%d group-members=%d use=%t set=%t",
				runtime.name,
				currentRole,
				sessionRole,
				roleSetting,
				inherits,
				membershipCount,
				exactMembershipCount,
				groupMemberCount,
				canUseGroup,
				canSetGroup,
			)
		}

		invocation := fmt.Sprintf(
			`SELECT pg_catalog.count(*) FROM %s.%s($1)`,
			pgx.Identifier{schemaName}.Sanitize(),
			pgx.Identifier{runtime.invocation}.Sanitize(),
		)
		var resultCount int
		if err := runtime.database.QueryRowContext(ctx, invocation, runtime.argument).Scan(&resultCount); err != nil {
			t.Fatalf("call inherited operator routine %s as %s: %v", runtime.invocation, runtime.name, err)
		}
		if resultCount != 0 {
			t.Fatalf("empty fixture routine %s returned %d rows", runtime.invocation, resultCount)
		}
		if _, err := runtime.database.ExecContext(ctx, `SET ROLE `+runtime.group); err == nil {
			t.Fatalf("runtime login %s could SET ROLE to %s", runtime.name, runtime.group)
		}
	}
}

func openRuntimeOperatorDatabase(
	t *testing.T,
	baseDSN string,
	role string,
	password string,
	schema string,
) *sql.DB {
	t.Helper()
	configuration, err := pgx.ParseConfig(baseDSN)
	if err != nil {
		t.Fatalf("parse PostgreSQL test DSN: %v", err)
	}
	configuration.User = role
	configuration.Password = password
	configuration.RuntimeParams["search_path"] = schema
	database := stdlib.OpenDB(*configuration)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	return database
}

func runtimeDSNUser(t *testing.T, dsn string) string {
	t.Helper()
	configuration, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL test DSN user: %v", err)
	}
	return configuration.User
}

func runtimeDSNPassword(t *testing.T, dsn string) string {
	t.Helper()
	configuration, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL test DSN password: %v", err)
	}
	return configuration.Password
}
