package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/worksflow/builder/backend/internal/repositorygc"
	"github.com/worksflow/builder/backend/migrations"
)

const (
	repositoryGCStableRoleAdvisoryLockID = int64(804357060326886689)
	testApplicationRole                  = "worksflow_application"
	testMigrationOwnerRole               = "worksflow_migration_owner"
	testOperatorRole                     = "worksflow_repository_index_gc_operator"
)

// This canary deliberately authenticates as a real low-privilege LOGIN. SET
// ROLE would conceal a privileged startup identity and therefore cannot prove
// the command's session/current and reachable-role posture checks.
func TestRepositoryIndexGCCommandRealLowPrivilegeLogin(t *testing.T) {
	baseDSN := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if baseDSN == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	roleLock, err := base.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roleLock.Close()
	if _, err := roleLock.ExecContext(ctx, "SELECT pg_advisory_lock($1)", repositoryGCStableRoleAdvisoryLockID); err != nil {
		t.Fatal(err)
	}

	stableRoles := []string{testApplicationRole, testMigrationOwnerRole, testOperatorRole}
	createdStableRoles := make([]string, 0, len(stableRoles))
	for _, role := range stableRoles {
		var exists bool
		if err := roleLock.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			if _, err := roleLock.ExecContext(ctx, "CREATE ROLE "+role+" NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION"); err != nil {
				t.Fatalf("create stable role %s: %v", role, err)
			}
			createdStableRoles = append(createdStableRoles, role)
		}
	}
	var migrationPrincipal string
	if err := roleLock.QueryRowContext(ctx, `SELECT current_user::text`).Scan(&migrationPrincipal); err != nil {
		t.Fatal(err)
	}
	var migrationPrincipalElevated bool
	if err := roleLock.QueryRowContext(ctx, `
SELECT rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication
FROM pg_roles WHERE rolname=$1`, migrationPrincipal).Scan(&migrationPrincipalElevated); err != nil {
		t.Fatal(err)
	}
	if !migrationPrincipalElevated {
		t.Fatalf("real ownership canary requires an elevated migration principal, got %s", migrationPrincipal)
	}
	var migrationOwnerMember bool
	if err := roleLock.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_auth_members AS membership
  JOIN pg_roles AS member_role ON member_role.oid=membership.member
  JOIN pg_roles AS owner_role ON owner_role.oid=membership.roleid
  WHERE member_role.rolname=$1 AND owner_role.rolname=$2
)`, migrationPrincipal, testMigrationOwnerRole).Scan(&migrationOwnerMember); err != nil {
		t.Fatal(err)
	}
	grantedMigrationOwner := false
	quotedMigrationPrincipal := `"` + strings.ReplaceAll(migrationPrincipal, `"`, `""`) + `"`
	if !migrationOwnerMember {
		if _, err := roleLock.ExecContext(ctx,
			"GRANT "+testMigrationOwnerRole+" TO "+quotedMigrationPrincipal); err != nil {
			t.Fatalf("grant migration-owner boundary to %s: %v", migrationPrincipal, err)
		}
		grantedMigrationOwner = true
	}

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schema := "repository_gc_operator_" + suffix
	login := "worksflow_gc_operator_canary_" + suffix
	hiddenSetRole := "worksflow_gc_hidden_set_role_" + suffix
	rogueRoutineGrantee := "worksflow_gc_rogue_routine_" + suffix
	password := "gc-canary-" + suffix
	if _, err := roleLock.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx, "CREATE ROLE "+login+" LOGIN INHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '"+password+"'"); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx, "GRANT "+testOperatorRole+" TO "+login); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup := context.Background()
		_, _ = roleLock.ExecContext(cleanup, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_, _ = roleLock.ExecContext(cleanup, "REVOKE "+hiddenSetRole+" FROM "+login)
		_, _ = roleLock.ExecContext(cleanup, "DROP OWNED BY "+hiddenSetRole)
		_, _ = roleLock.ExecContext(cleanup, "DROP ROLE IF EXISTS "+hiddenSetRole)
		_, _ = roleLock.ExecContext(cleanup, "DROP OWNED BY "+rogueRoutineGrantee)
		_, _ = roleLock.ExecContext(cleanup, "DROP ROLE IF EXISTS "+rogueRoutineGrantee)
		_, _ = roleLock.ExecContext(cleanup, "REVOKE "+testOperatorRole+" FROM "+login)
		_, _ = roleLock.ExecContext(cleanup, "DROP OWNED BY "+login)
		_, _ = roleLock.ExecContext(cleanup, "DROP ROLE IF EXISTS "+login)
		if grantedMigrationOwner {
			_, _ = roleLock.ExecContext(cleanup,
				"REVOKE "+testMigrationOwnerRole+" FROM "+quotedMigrationPrincipal)
		}
		for index := len(createdStableRoles) - 1; index >= 0; index-- {
			_, _ = roleLock.ExecContext(cleanup, "DROP ROLE IF EXISTS "+createdStableRoles[index])
		}
	}()
	if _, err := roleLock.ExecContext(ctx, "CREATE ROLE "+rogueRoutineGrantee+" NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION"); err != nil {
		t.Fatal(err)
	}

	migrationDSN, err := scopeDSN(baseDSN, schema)
	if err != nil {
		t.Fatal(err)
	}
	migrationDatabase, err := sql.Open("pgx", migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, migrationDatabase); err != nil {
		_ = migrationDatabase.Close()
		t.Fatalf("apply isolated migrations: %v", err)
	}
	defer migrationDatabase.Close()
	seedRepositoryGCOperatorTrees(t, ctx, migrationDatabase)

	// A privileged authenticated session cannot hide behind a startup SET ROLE.
	// This bypass database is intentionally constructed outside loadDSN, which
	// independently rejects role query options before opening a connection.
	bypassURL, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	bypassQuery := bypassURL.Query()
	bypassQuery.Set("search_path", schema)
	bypassQuery.Set("role", testOperatorRole)
	bypassURL.RawQuery = bypassQuery.Encode()
	bypassDatabase, err := sql.Open("pgx", bypassURL.String())
	if err != nil {
		t.Fatal(err)
	}
	bypassAuthority, err := repositorygc.NewPostgresAuthority(bypassDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if err := bypassAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		_ = bypassDatabase.Close()
		t.Fatalf("privileged session startup role bypass error = %v", err)
	}
	if err := bypassDatabase.Close(); err != nil {
		t.Fatal(err)
	}

	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword(login, password)
	query := parsed.Query()
	query.Del("search_path")
	parsed.RawQuery = query.Encode()
	operatorDSN := parsed.String()
	stableRunID := uuid.New()
	policy := repositorygc.Policy{
		Retention: 7 * 24 * time.Hour, KeepPerProject: 8,
		BatchSize: 2, CapabilityTTL: 10 * time.Minute,
	}

	// Persist one of two capability receipts, then close the physical pool to
	// model a process crash before the run can finish.
	interruptedDatabase, err := sql.Open("pgx", mustScopeTestDSN(t, operatorDSN, schema))
	if err != nil {
		t.Fatal(err)
	}
	interruptedAuthority, err := repositorygc.NewPostgresAuthority(interruptedDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		logRepositoryGCOperatorTablePrivileges(t, ctx, interruptedDatabase)
		t.Fatalf("low-privilege readiness before interruption: %v", err)
	}
	internalTriggerHelper := `"` + schema + `".lock_candidate_exact_tree_literal_index_reference()`
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT EXECUTE ON FUNCTION `+internalTriggerHelper+` TO `+rogueRoutineGrantee); err != nil {
		t.Fatalf("grant internal helper to unrelated historical grantee: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted non-owner internal routine grantee: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE ALL ON FUNCTION `+internalTriggerHelper+` FROM `+rogueRoutineGrantee); err != nil {
		t.Fatalf("revoke unrelated internal helper grantee: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after restoring internal helper ACL: %v", err)
	}

	sandboxCheckpointHelper := `"` + schema + `".sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)`
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT EXECUTE ON FUNCTION `+sandboxCheckpointHelper+` TO `+rogueRoutineGrantee); err != nil {
		t.Fatalf("grant Sandbox checkpoint helper to unrelated historical grantee: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted unrelated Sandbox checkpoint helper grantee: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE ALL ON FUNCTION `+sandboxCheckpointHelper+` FROM `+rogueRoutineGrantee); err != nil {
		t.Fatalf("revoke unrelated Sandbox checkpoint helper grantee: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT EXECUTE ON FUNCTION `+sandboxCheckpointHelper+` TO `+testApplicationRole+` WITH GRANT OPTION`); err != nil {
		t.Fatalf("make Sandbox checkpoint application grant delegable: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted delegable Sandbox checkpoint helper grant: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE GRANT OPTION FOR EXECUTE ON FUNCTION `+sandboxCheckpointHelper+` FROM `+testApplicationRole); err != nil {
		t.Fatalf("restore non-grantable Sandbox checkpoint application grant: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` SECURITY DEFINER`); err != nil {
		t.Fatalf("weaken Sandbox checkpoint helper security mode: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted SECURITY DEFINER Sandbox checkpoint helper: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` SECURITY INVOKER`); err != nil {
		t.Fatalf("restore Sandbox checkpoint helper security mode: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` IMMUTABLE`); err != nil {
		t.Fatalf("weaken Sandbox checkpoint helper volatility contract: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted immutable Sandbox checkpoint helper: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` STABLE`); err != nil {
		t.Fatalf("restore Sandbox checkpoint helper volatility: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` SET search_path TO pg_catalog`); err != nil {
		t.Fatalf("weaken Sandbox checkpoint helper search path: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted weakened Sandbox checkpoint helper search path: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` SET search_path TO pg_catalog, "`+schema+`", pg_temp`); err != nil {
		t.Fatalf("restore Sandbox checkpoint helper search path: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` OWNER TO `+quotedMigrationPrincipal); err != nil {
		t.Fatalf("assign Sandbox checkpoint helper to elevated migration-role member: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted elevated Sandbox checkpoint helper owner: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+sandboxCheckpointHelper+` OWNER TO `+testMigrationOwnerRole); err != nil {
		t.Fatalf("restore exact Sandbox checkpoint helper owner: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE ALL ON FUNCTION `+sandboxCheckpointHelper+` FROM `+quotedMigrationPrincipal); err != nil {
		t.Fatalf("remove former Sandbox checkpoint helper owner ACL: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT EXECUTE ON FUNCTION `+sandboxCheckpointHelper+` TO `+testApplicationRole); err != nil {
		t.Fatalf("restore Sandbox checkpoint helper application grant: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after restoring Sandbox checkpoint helper contract: %v", err)
	}
	if _, err := roleLock.ExecContext(ctx,
		"GRANT "+testOperatorRole+" TO "+login+" WITH ADMIN OPTION"); err != nil {
		t.Fatalf("upgrade operator membership with ADMIN OPTION: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted delegable GC role membership: %v", err)
	}
	if _, err := roleLock.ExecContext(ctx,
		"REVOKE ADMIN OPTION FOR "+testOperatorRole+" FROM "+login); err != nil {
		t.Fatalf("revoke operator membership ADMIN OPTION: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after revoking ADMIN OPTION: %v", err)
	}
	planFunction := `"` + schema + `".plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)`
	if _, err := migrationDatabase.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` SECURITY INVOKER`); err != nil {
		t.Fatalf("weaken plan function security mode: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted SECURITY INVOKER plan function: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` SECURITY DEFINER`); err != nil {
		t.Fatalf("restore plan function security mode: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` SET search_path TO pg_catalog`); err != nil {
		t.Fatalf("weaken plan function search path: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted weakened plan function search path: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+planFunction+` SET search_path TO pg_catalog, "`+schema+`", pg_temp`); err != nil {
		t.Fatalf("restore plan function search path: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after restoring plan function posture: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+planFunction+` OWNER TO `+quotedMigrationPrincipal); err != nil {
		t.Fatalf("assign plan function to elevated migration-role member: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted elevated migration-role member as plan function owner: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+planFunction+` OWNER TO `+testMigrationOwnerRole); err != nil {
		t.Fatalf("restore exact migration-role plan function owner: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after restoring exact plan function owner: %v", err)
	}
	usersTable := `"` + schema + `".users`
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT SELECT ON TABLE `+usersTable+` TO `+testOperatorRole); err != nil {
		t.Fatalf("grant unrelated business-table SELECT to operator group: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted reachable business-table SELECT: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE SELECT ON TABLE `+usersTable+` FROM `+testOperatorRole); err != nil {
		t.Fatalf("revoke unrelated business-table SELECT from operator group: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after revoking business-table SELECT: %v", err)
	}
	if _, err := roleLock.ExecContext(ctx,
		"CREATE ROLE "+hiddenSetRole+" NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION"); err != nil {
		t.Fatalf("create hidden SET ROLE authority: %v", err)
	}
	if _, err := roleLock.ExecContext(ctx,
		"GRANT "+hiddenSetRole+" TO "+login+" WITH INHERIT FALSE, SET TRUE"); err != nil {
		t.Fatalf("grant hidden NOINHERIT SET ROLE authority: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT SELECT ON TABLE `+usersTable+` TO `+hiddenSetRole); err != nil {
		t.Fatalf("grant business-table SELECT to hidden SET ROLE authority: %v", err)
	}
	if _, err := interruptedDatabase.ExecContext(ctx,
		`SELECT 1 FROM `+usersTable+` LIMIT 1`); !postgresPermissionDenied(err) {
		t.Fatalf("NOINHERIT hidden table privilege was unexpectedly active without SET ROLE: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted hidden SET ROLE business-table authority: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE SELECT ON TABLE `+usersTable+` FROM `+hiddenSetRole); err != nil {
		t.Fatalf("revoke business-table SELECT from hidden SET ROLE authority: %v", err)
	}
	if _, err := roleLock.ExecContext(ctx, "REVOKE "+hiddenSetRole+" FROM "+login); err != nil {
		t.Fatalf("revoke hidden SET ROLE membership: %v", err)
	}
	if _, err := roleLock.ExecContext(ctx, "DROP ROLE "+hiddenSetRole); err != nil {
		t.Fatalf("drop hidden SET ROLE authority: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after dropping hidden SET ROLE authority: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT SELECT (email) ON TABLE `+usersTable+` TO `+testOperatorRole); err != nil {
		t.Fatalf("grant unrelated business-table column SELECT to operator group: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted reachable business-table column SELECT: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE SELECT (email) ON TABLE `+usersTable+` FROM `+testOperatorRole); err != nil {
		t.Fatalf("revoke unrelated business-table column SELECT from operator group: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after revoking business-table column SELECT: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT SELECT ON TABLE `+usersTable+` TO PUBLIC`); err != nil {
		t.Fatalf("grant unrelated business-table SELECT to PUBLIC: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted PUBLIC business-table SELECT: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE SELECT ON TABLE `+usersTable+` FROM PUBLIC`); err != nil {
		t.Fatalf("revoke unrelated business-table SELECT from PUBLIC: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after revoking PUBLIC business-table SELECT: %v", err)
	}
	unrelatedSequence := `"` + schema + `".repository_gc_unrelated_operator_canary_sequence`
	if _, err := migrationDatabase.ExecContext(ctx,
		`CREATE SEQUENCE `+unrelatedSequence); err != nil {
		t.Fatalf("create unrelated sequence privilege canary: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE ALL ON SEQUENCE `+unrelatedSequence+` FROM PUBLIC`); err != nil {
		t.Fatalf("remove PUBLIC sequence grants before privilege canary: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER SEQUENCE `+unrelatedSequence+` OWNER TO `+testMigrationOwnerRole); err != nil {
		t.Fatalf("set exact sequence owner before privilege canary: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`GRANT USAGE ON SEQUENCE `+unrelatedSequence+` TO `+testOperatorRole); err != nil {
		t.Fatalf("grant unrelated sequence privilege to operator group: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted reachable sequence privilege: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`DROP SEQUENCE `+unrelatedSequence); err != nil {
		t.Fatalf("drop unrelated sequence privilege canary: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after dropping sequence privilege canary: %v", err)
	}
	unrelatedFunction := `"` + schema + `".repository_gc_unrelated_operator_canary()`
	if _, err := migrationDatabase.ExecContext(ctx, `
CREATE FUNCTION `+unrelatedFunction+`
RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT 1'`); err != nil {
		t.Fatalf("create unrelated function ownership canary: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`REVOKE ALL ON FUNCTION `+unrelatedFunction+` FROM PUBLIC, `+testOperatorRole+`, `+testApplicationRole); err != nil {
		t.Fatalf("remove unrelated function grants before ownership canary: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx,
		`ALTER FUNCTION `+unrelatedFunction+` OWNER TO `+login); err != nil {
		t.Fatalf("grant unrelated function ownership to operator login: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); !errors.Is(err, repositorygc.ErrPostgresReadiness) {
		t.Fatalf("operator accepted unrelated function ownership: %v", err)
	}
	if _, err := migrationDatabase.ExecContext(ctx, `DROP FUNCTION `+unrelatedFunction); err != nil {
		t.Fatalf("drop unrelated function ownership canary: %v", err)
	}
	if err := interruptedAuthority.Readiness(ctx); err != nil {
		t.Fatalf("low-privilege readiness after dropping function ownership canary: %v", err)
	}
	if _, err := interruptedDatabase.ExecContext(ctx, `
SELECT acquire_candidate_workspace_lease(NULL::uuid,NULL::bigint,NULL::uuid,NULL::integer)`); !postgresPermissionDenied(err) {
		t.Fatalf("low-privilege GC operator executed Candidate definer: %v", err)
	}
	capabilities, err := interruptedAuthority.Plan(ctx, repositorygc.PlanInput{
		RunID: stableRunID, Retention: policy.Retention, KeepPerProject: policy.KeepPerProject,
		BatchSize: policy.BatchSize, CapabilityTTL: policy.CapabilityTTL,
	})
	if err != nil || len(capabilities) != 2 {
		t.Fatalf("initial durable plan = %#v, %v", capabilities, err)
	}
	if _, err := interruptedAuthority.Execute(ctx, capabilities[0].CapabilityID); err != nil {
		t.Fatalf("persist first receipt before interruption: %v", err)
	}
	if err := interruptedDatabase.Close(); err != nil {
		t.Fatal(err)
	}

	runCommand := func() repositorygc.Result {
		t.Helper()
		var output bytes.Buffer
		err := run(
			ctx,
			[]string{
				"-postgres-schema", schema, "-run-id", stableRunID.String(),
				"-retention", "168h", "-keep-per-project", "8", "-batch-size", "2",
				"-capability-ttl", "10m",
			},
			&output,
			func(key string) (string, bool) {
				if key != postgresDSNEnvironment {
					t.Fatalf("command read unexpected environment key %s", key)
				}
				return operatorDSN, true
			},
			openDatabase,
			executeGC,
		)
		if err != nil {
			t.Fatalf("resume real low-privilege operator: %v", err)
		}
		var result repositorygc.Result
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("decode canonical result %q: %v", output.String(), err)
		}
		return result
	}
	result := runCommand()
	if result.SchemaVersion != repositorygc.ResultSchemaVersion || result.RunID != stableRunID ||
		result.Planned != 2 || result.Deleted != 2 || result.Protected != 0 ||
		result.Stale != 0 || result.Expired != 0 || result.LogicalBytesReleased <= 0 || result.BlobBytesFreed <= 0 {
		t.Fatalf("unexpected resumed GC result: %#v", result)
	}
	if replay := runCommand(); replay != result {
		t.Fatalf("completed stable run did not replay canonically: first=%#v replay=%#v", result, replay)
	}
	var runCount, capabilityCount, receiptCount int
	if err := migrationDatabase.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM repository_exact_tree_literal_index_gc_runs),
  (SELECT count(*) FROM repository_exact_tree_literal_index_gc_capabilities WHERE run_id=$1),
  (SELECT count(*) FROM repository_exact_tree_literal_index_gc_receipts WHERE run_id=$1)`, stableRunID).Scan(
		&runCount, &capabilityCount, &receiptCount,
	); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || capabilityCount != 2 || receiptCount != 2 {
		t.Fatalf("stable resume created replacement authority: runs=%d capabilities=%d receipts=%d", runCount, capabilityCount, receiptCount)
	}

	// Prove the authenticated login itself is active; no SET ROLE shortcut was
	// used to make a privileged migration session look like the operator.
	operatorDatabase, err := sql.Open("pgx", mustScopeTestDSN(t, operatorDSN, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer operatorDatabase.Close()
	var sessionRole, currentRole string
	if err := operatorDatabase.QueryRowContext(ctx, `SELECT session_user::text,current_user::text`).Scan(&sessionRole, &currentRole); err != nil {
		t.Fatal(err)
	}
	if sessionRole != login || currentRole != login {
		t.Fatalf("operator session/current roles = %q/%q, want real login %q", sessionRole, currentRole, login)
	}
}

func seedRepositoryGCOperatorTrees(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	actorID, projectID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id,email,display_name,password_hash)
VALUES ($1,$2,'GC operator canary','not-used')`,
		actorID, "repository-gc-operator-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id,name,created_by)
VALUES ($1,'Repository GC operator canary',$2)`, projectID, actorID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for index := 0; index < 10; index++ {
		label := fmt.Sprintf("operator-resume-%02d", index)
		body := "repository GC stable run body " + label
		byteSize := int64(len([]byte(body)))
		treeHash := repositoryGCCanaryDigest("tree-" + label)
		contentHash := repositoryGCCanaryDigest("content-" + body)
		treeCommitment := repositoryGCCanaryDigest("tree-commitment-" + label)
		indexCommitment := repositoryGCCanaryDigest("index-commitment-" + label)
		readyAt := now.Add(-8*24*time.Hour - time.Duration(index)*time.Hour)
		if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id,content_hash,byte_size,is_text,body,created_at
) VALUES ($1,$2,$3,true,$4,$5)`,
			projectID, contentHash, byteSize, body, readyAt.Add(-time.Minute)); err != nil {
			t.Fatalf("seed blob %d: %v", index, err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id,tree_hash,schema_version,status,file_count,text_file_count,
  skipped_file_count,total_bytes,tree_commitment,index_commitment,created_at
) VALUES ($1,$2,'repository-exact-tree-literal-index/v1','building',1,1,0,$3,$4,$5,$6)`,
			projectID, treeHash, byteSize, treeCommitment, indexCommitment,
			readyAt.Add(-time.Minute)); err != nil {
			t.Fatalf("seed manifest %d: %v", index, err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_members (
  project_id,tree_hash,path,mode,content_hash,byte_size,indexed
) VALUES ($1,$2,$3,'100644',$4,$5,true)`,
			projectID, treeHash, label+".txt", contentHash, byteSize); err != nil {
			t.Fatalf("seed member %d: %v", index, err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE repository_exact_tree_literal_index_manifests
SET status='ready',ready_at=$3
WHERE project_id=$1 AND tree_hash=$2`, projectID, treeHash, readyAt); err != nil {
			t.Fatalf("publish manifest %d: %v", index, err)
		}
	}
}

func repositoryGCCanaryDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", digest)
}

func postgresPermissionDenied(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "42501"
}

func logRepositoryGCOperatorTablePrivileges(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var schema, tables string
	if err := database.QueryRowContext(ctx, `
SELECT current_schema(),coalesce(string_agg(relname,',' ORDER BY relname),'')
FROM pg_class JOIN pg_namespace ON pg_namespace.oid=pg_class.relnamespace
WHERE pg_namespace.nspname=current_schema() AND relkind IN ('r','p')
  AND relname IN (
    'repository_exact_tree_literal_index_manifests','repository_exact_tree_literal_index_members',
    'repository_exact_tree_literal_index_blobs','repository_exact_tree_literal_index_build_claims',
    'repository_exact_tree_literal_index_gc_runs','repository_exact_tree_literal_index_gc_capabilities',
    'repository_exact_tree_literal_index_gc_receipts','repository_exact_tree_literal_index_gc_tombstones',
    'repository_exact_tree_literal_index_gc_tree_delete_auth',
    'repository_exact_tree_literal_index_gc_blob_delete_auth'
  ) GROUP BY current_schema()`).Scan(&schema, &tables); err != nil {
		t.Logf("inspect low-login expected tables: %v", err)
	} else {
		t.Logf("low-login schema=%s expected tables=%s", schema, tables)
	}
	functionRows, functionErr := database.QueryContext(ctx, `
SELECT procedure.proname,procedure.prosecdef,procedure.proretset,
       procedure.proconfig::text,pg_get_function_result(procedure.oid),
       procedure.proargnames::text,procedure.proargmodes::text,
       pg_get_userbyid(procedure.proowner)
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid=procedure.pronamespace
WHERE namespace.nspname=current_schema()
  AND procedure.proname IN (
    'plan_repository_exact_tree_literal_index_gc',
    'execute_repository_exact_tree_literal_index_gc',
    'inspect_repository_exact_tree_literal_index_gc_run',
    'repository_exact_tree_literal_index_gc_readiness'
  )
ORDER BY procedure.proname`)
	if functionErr != nil {
		t.Logf("inspect low-login function contracts: %v", functionErr)
	} else {
		defer functionRows.Close()
		for functionRows.Next() {
			var name, config, result, names, modes, owner string
			var securityDefiner, returnsSet bool
			if err := functionRows.Scan(&name, &securityDefiner, &returnsSet, &config, &result, &names, &modes, &owner); err != nil {
				t.Logf("scan low-login function contract: %v", err)
				break
			}
			t.Logf("low-login function=%s definer=%t set=%t config=%s result=%s names=%s modes=%s owner=%s",
				name, securityDefiner, returnsSet, config, result, names, modes, owner)
		}
	}
	nonGCFunctionRows, nonGCFunctionErr := database.QueryContext(ctx, `
SELECT procedure.proname,pg_get_function_identity_arguments(procedure.oid),
       procedure.prosecdef,coalesce(procedure.proacl::text,''),pg_get_userbyid(procedure.proowner)
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid=procedure.pronamespace
WHERE namespace.nspname=current_schema()
  AND has_function_privilege(current_user,procedure.oid,'EXECUTE')
  AND procedure.proname NOT IN (
    'plan_repository_exact_tree_literal_index_gc',
    'execute_repository_exact_tree_literal_index_gc',
    'inspect_repository_exact_tree_literal_index_gc_run',
    'repository_exact_tree_literal_index_gc_readiness'
  )
ORDER BY procedure.proname,pg_get_function_identity_arguments(procedure.oid)
LIMIT 25`)
	if nonGCFunctionErr != nil {
		t.Logf("inspect executable non-GC functions: %v", nonGCFunctionErr)
	} else {
		defer nonGCFunctionRows.Close()
		for nonGCFunctionRows.Next() {
			var name, arguments, acl, owner string
			var securityDefiner bool
			if err := nonGCFunctionRows.Scan(&name, &arguments, &securityDefiner, &acl, &owner); err != nil {
				t.Logf("scan executable non-GC function: %v", err)
				break
			}
			t.Logf("low-login executable non-GC function=%s(%s) definer=%t acl=%s owner=%s",
				name, arguments, securityDefiner, acl, owner)
		}
	}
	rows, err := database.QueryContext(ctx, `
SELECT role.rolname,relation.relname,
       has_table_privilege(role.oid,relation.oid,'SELECT'),
       has_table_privilege(role.oid,relation.oid,'INSERT'),
       has_table_privilege(role.oid,relation.oid,'UPDATE'),
       has_table_privilege(role.oid,relation.oid,'DELETE'),
       has_table_privilege(role.oid,relation.oid,'TRUNCATE'),
       has_table_privilege(role.oid,relation.oid,'REFERENCES'),
       has_table_privilege(role.oid,relation.oid,'TRIGGER')
FROM pg_roles AS role
CROSS JOIN pg_class AS relation
JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
WHERE pg_has_role(session_user,role.oid,'MEMBER')
  AND namespace.nspname=current_schema()
  AND relation.relkind IN ('r','p')
  AND (
    has_table_privilege(role.oid,relation.oid,'SELECT')
    OR has_table_privilege(role.oid,relation.oid,'INSERT')
    OR has_table_privilege(role.oid,relation.oid,'UPDATE')
    OR has_table_privilege(role.oid,relation.oid,'DELETE')
    OR has_table_privilege(role.oid,relation.oid,'TRUNCATE')
    OR has_table_privilege(role.oid,relation.oid,'REFERENCES')
    OR has_table_privilege(role.oid,relation.oid,'TRIGGER')
  )
ORDER BY role.rolname,relation.relname`)
	if err != nil {
		t.Logf("inspect low-login table privileges: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var role, table string
		var privileges [7]bool
		if err := rows.Scan(
			&role, &table, &privileges[0], &privileges[1], &privileges[2], &privileges[3],
			&privileges[4], &privileges[5], &privileges[6],
		); err != nil {
			t.Logf("scan low-login table privileges: %v", err)
			return
		}
		t.Logf("low-login reachable table privilege role=%s table=%s select/insert/update/delete/truncate/references/trigger=%v", role, table, privileges)
	}
}

func mustScopeTestDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	scoped, err := scopeDSN(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	return scoped
}
