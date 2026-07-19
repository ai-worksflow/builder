package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	repositoryExactTreeLiteralIndexGCMigration = "000066_repository_exact_tree_literal_index_gc"
	postgresStableRoleAdvisoryLockID           = int64(804357060326886689)
)

func TestRepositoryExactTreeLiteralIndexGCMigrationContract(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(repositoryExactTreeLiteralIndexGCMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile(repositoryExactTreeLiteralIndexGCMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"retention_milliseconds >= 604800000",
		"keep_per_project integer NOT NULL CHECK (keep_per_project >= 8)",
		"batch_size integer NOT NULL CHECK (batch_size BETWEEN 1 AND 100)",
		"capability_ttl_milliseconds BETWEEN 1 AND 900000",
		"repository_exact_tree_literal_index_gc_runs",
		"repository_exact_tree_literal_index_gc_capabilities",
		"repository_exact_tree_literal_index_gc_receipts",
		"repository_exact_tree_literal_index_gc_tombstones",
		"transaction_id bigint NOT NULL",
		"backend_pid integer NOT NULL",
		"ranked AS MATERIALIZED",
		"row_number() OVER",
		"candidate.current_tree_hash = ranked.tree_hash",
		"claim.lease_expires_at > observed_at",
		"pg_advisory_xact_lock_shared",
		"old_key COLLATE \"C\" <= new_key COLLATE \"C\"",
		"plan_repository_exact_tree_literal_index_gc",
		"execute_repository_exact_tree_literal_index_gc",
		"inspect_repository_exact_tree_literal_index_gc_run",
		"repository_exact_tree_literal_index_gc_readiness",
		"publication_created_at",
		"index_commitment",
		"logical_bytes_released",
		"blob_bytes_freed",
		"outcome IN ('deleted', 'protected', 'stale', 'expired')",
		"worksflow_repository_index_gc_operator",
		"worksflow_application",
		"worksflow_migration_owner",
		"GRANT SELECT ON TABLE %I.schema_migrations TO worksflow_application",
		"schema_head_oid IS NOT NULL",
		"relation.relname = 'schema_migrations'",
		"REVOKE ALL ON FUNCTION acquire_candidate_workspace_lease",
		"GRANT EXECUTE ON FUNCTION complete_abandoned_sandbox_session",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"REVOKE EXECUTE ON ALL ROUTINES IN SCHEMA",
		"ALTER DEFAULT PRIVILEGES FOR ROLE worksflow_migration_owner",
		"LOCK TABLE repository_exact_tree_literal_index_gc_runs IN ROW SHARE MODE",
		"FOR UPDATE",
		"claim.owner_token = build_claim.owner_token",
		"pg_get_indexdef(index_metadata.indexrelid, key_number, true)",
		"trigger.tgenabled = 'O'",
		"trigger.tgqual IS NULL",
		"trigger.tgattr::text = ''",
		"trigger.tgnargs = 0",
		"trigger.tgconstraint = 0",
		"sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)",
		"exact-index internal routines must have owner-only EXECUTE privileges",
		"ALTER SCHEMA %I OWNER TO worksflow_migration_owner",
		"DO $stable_role_preflight$",
		"stable_role_count <> 3",
		"membership.member = ANY(stable_role_oids)",
		"membership.admin_option",
		"aclexplode(attribute.attacl)",
		"trusted-schema column privileges to be revoked explicitly before migration",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("exact-tree GC migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"current_setting('worksflow",
		"set_config('worksflow",
		"GRANT EXECUTE ON FUNCTION execute_repository_exact_tree_literal_index_gc(uuid) TO PUBLIC",
		"REVOKE ALL PRIVILEGES (%I) ON TABLE %I.%I",
		"'schema_migrations'::regclass",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("exact-tree GC migration contains forbidden authority %q", forbidden)
		}
	}
	if strings.Index(text, "DO $stable_role_preflight$") > strings.Index(text, "REVOKE CREATE ON SCHEMA") {
		t.Fatal("stable-role preflight does not precede the first schema mutation")
	}
	downText := string(down)
	for _, required := range []string{
		"Cannot roll back exact-tree literal index GC while audit/control rows exist",
		"IN ACCESS EXCLUSIVE MODE",
		"GRANT EXECUTE ON ALL ROUTINES IN SCHEMA",
		"ALTER DEFAULT PRIVILEGES FOR ROLE worksflow_migration_owner",
		"DROP FUNCTION IF EXISTS execute_repository_exact_tree_literal_index_gc(uuid)",
		"DROP TABLE IF EXISTS repository_exact_tree_literal_index_gc_tombstones",
		"Exact-tree literal index blobs are immutable",
		"Exact-tree literal index members are immutable",
		"Exact-tree literal index manifests are immutable",
		") TO PUBLIC",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("exact-tree GC rollback is missing %q", required)
		}
	}
}

func TestRepositoryExactTreeLiteralIndexGCPostgresCanary(t *testing.T) {
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
	roleLock, err := base.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roleLock.Close()
	if _, err := roleLock.ExecContext(ctx, "SELECT pg_advisory_lock($1)", postgresStableRoleAdvisoryLockID); err != nil {
		t.Fatal(err)
	}

	roles := []string{
		"worksflow_application",
		"worksflow_migration_owner",
		"worksflow_repository_index_gc_operator",
	}
	createdRoles := make([]string, 0, len(roles))
	for _, role := range roles {
		var exists bool
		if err := roleLock.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)", role,
		).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			if _, err := roleLock.ExecContext(ctx,
				"CREATE ROLE "+role+" NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION",
			); err != nil {
				t.Fatalf("create stable group %s: %v", role, err)
			}
			createdRoles = append(createdRoles, role)
		}
	}
	var migrationPrincipal string
	if err := roleLock.QueryRowContext(ctx, `SELECT current_user`).Scan(&migrationPrincipal); err != nil {
		t.Fatal(err)
	}
	var migrationMembership bool
	if err := roleLock.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_auth_members AS membership
  JOIN pg_roles AS member_role ON member_role.oid=membership.member
  JOIN pg_roles AS owner_role ON owner_role.oid=membership.roleid
  WHERE member_role.rolname=$1 AND owner_role.rolname='worksflow_migration_owner'
)`, migrationPrincipal).Scan(&migrationMembership); err != nil {
		t.Fatal(err)
	}
	grantedMigrationMembership := false
	if !migrationMembership {
		quotedPrincipal := `"` + strings.ReplaceAll(migrationPrincipal, `"`, `""`) + `"`
		if _, err := roleLock.ExecContext(ctx,
			"GRANT worksflow_migration_owner TO "+quotedPrincipal,
		); err != nil {
			t.Fatalf("grant migration-owner boundary to %s: %v", migrationPrincipal, err)
		}
		grantedMigrationMembership = true
	}
	defer func() {
		if grantedMigrationMembership {
			quotedPrincipal := `"` + strings.ReplaceAll(migrationPrincipal, `"`, `""`) + `"`
			_, _ = roleLock.ExecContext(
				context.Background(), "REVOKE worksflow_migration_owner FROM "+quotedPrincipal,
			)
		}
		for index := len(createdRoles) - 1; index >= 0; index-- {
			if _, err := roleLock.ExecContext(context.Background(),
				"DROP ROLE IF EXISTS "+createdRoles[index],
			); err != nil {
				var externalDependencies int
				if countErr := roleLock.QueryRowContext(context.Background(), `
SELECT count(*) FROM pg_shdepend
WHERE refclassid='pg_authid'::regclass
  AND refobjid=$1::regrole`, createdRoles[index]).Scan(&externalDependencies); countErr != nil || externalDependencies == 0 {
					t.Errorf("drop created role %s: %v (dependency count=%d, count error=%v)",
						createdRoles[index], err, externalDependencies, countErr)
				} else {
					t.Logf("created role %s was concurrently adopted by %d external objects; preserving it: %v",
						createdRoles[index], externalDependencies, err)
				}
			}
		}
	}()
	assertExactTreeGCStableRolePreflight(t, ctx, dsn, roleLock, roles)

	freshDatabase := "repository_literal_gc_db_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, `CREATE DATABASE `+freshDatabase+` TEMPLATE template0`); err != nil {
		t.Fatalf("create fresh exact-tree GC database: %v", err)
	}
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	connectionConfig.Database = freshDatabase
	freshDSN := connectionConfig.ConnString()
	freshBase, err := sql.Open("pgx", freshDSN)
	if err != nil {
		t.Fatal(err)
	}
	schema := "repository_literal_gc_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := freshBase.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		freshBase.Close()
		t.Fatal(err)
	}
	if err := freshBase.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = roleLock.ExecContext(context.Background(), `
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname=$1 AND pid<>pg_backend_pid()`, freshDatabase)
		if _, err := roleLock.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+freshDatabase); err != nil {
			t.Errorf("drop fresh exact-tree GC database %s: %v", freshDatabase, err)
		}
	}()

	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, freshDSN, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `
ALTER DEFAULT PRIVILEGES IN SCHEMA "`+schema+`"
GRANT ALL ON TABLES TO worksflow_application`); err != nil {
		t.Fatalf("seed pre-migration overwide application defaults: %v", err)
	}
	if err := Up(ctx, database); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			t.Fatalf("apply migrations: %v position=%d where=%s", err, postgresError.Position, postgresError.Where)
		}
		t.Fatalf("apply migrations: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
ALTER DEFAULT PRIVILEGES IN SCHEMA "`+schema+`"
REVOKE ALL ON TABLES FROM worksflow_application`); err != nil {
		t.Fatalf("remove overwide application defaults after migration canary: %v", err)
	}

	assertExactTreeGCNamespaceAndFunctionBoundary(t, ctx, database, schema)
	assertExactTreeGCRolePrivileges(t, ctx, database, schema)
	applicationLogin := "worksflow_gc_app_login_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	applicationPassword := "gc-login-" + uuid.NewString()
	if _, err := roleLock.ExecContext(ctx,
		"CREATE ROLE "+applicationLogin+" LOGIN INHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '"+
			applicationPassword+"'",
	); err != nil {
		t.Fatal(err)
	}
	createdRoles = append(createdRoles, applicationLogin)
	if _, err := roleLock.ExecContext(ctx, "GRANT worksflow_application TO "+applicationLogin); err != nil {
		t.Fatal(err)
	}
	applicationConfig, err := pgx.ParseConfig(postgresDSNWithSearchPath(t, freshDSN, schema))
	if err != nil {
		t.Fatal(err)
	}
	applicationConfig.User = applicationLogin
	applicationConfig.Password = applicationPassword
	applicationDatabase, err := sql.Open("pgx", applicationConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	assertExactTreeGCApplicationLoginCriticalPaths(t, ctx, database, applicationDatabase)
	if err := applicationDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	assertExactTreeGCLiveDriftGates(t, ctx, database, roleLock, schema)
	if _, err := database.ExecContext(ctx, `
GRANT DELETE ON repository_exact_tree_literal_index_manifests TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	var readyAfterOvergrant bool
	var overgrantReason string
	if err := database.QueryRowContext(ctx, `
SELECT ready,reason FROM repository_exact_tree_literal_index_gc_readiness()`).Scan(
		&readyAfterOvergrant, &overgrantReason,
	); err != nil {
		t.Fatal(err)
	}
	if readyAfterOvergrant || !strings.Contains(overgrantReason, "not minimal and exact") {
		t.Fatalf("manual application overgrant readiness=%v reason=%q", readyAfterOvergrant, overgrantReason)
	}
	if _, err := database.ExecContext(ctx, `
REVOKE DELETE ON repository_exact_tree_literal_index_manifests FROM worksflow_application`); err != nil {
		t.Fatal(err)
	}
	unexpectedRole := "worksflow_gc_unexpected_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, "CREATE ROLE "+unexpectedRole+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	createdRoles = append(createdRoles, unexpectedRole)
	if _, err := database.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION "`+schema+`".`+
		`acquire_candidate_workspace_lease(uuid,bigint,uuid,integer) TO `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	var readyAfterFunctionOvergrant bool
	var functionOvergrantReason string
	if err := database.QueryRowContext(ctx, `
SELECT ready,reason FROM repository_exact_tree_literal_index_gc_readiness()`).Scan(
		&readyAfterFunctionOvergrant, &functionOvergrantReason,
	); err != nil {
		t.Fatal(err)
	}
	if readyAfterFunctionOvergrant || !strings.Contains(functionOvergrantReason, "unexpected EXECUTE grantee") {
		t.Fatalf("unexpected function grantee readiness=%v reason=%q",
			readyAfterFunctionOvergrant, functionOvergrantReason)
	}
	if _, err := database.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION "`+schema+`".`+
		`acquire_candidate_workspace_lease(uuid,bigint,uuid,integer) FROM `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION "`+schema+`".`+
		`lock_candidate_exact_tree_literal_index_reference() TO `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT ready,reason FROM repository_exact_tree_literal_index_gc_readiness()`).Scan(
		&readyAfterFunctionOvergrant, &functionOvergrantReason,
	); err != nil {
		t.Fatal(err)
	}
	if readyAfterFunctionOvergrant || !strings.Contains(functionOvergrantReason, "owner-only EXECUTE") {
		t.Fatalf("internal function grantee readiness=%v reason=%q",
			readyAfterFunctionOvergrant, functionOvergrantReason)
	}
	if _, err := database.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION "`+schema+`".`+
		`lock_candidate_exact_tree_literal_index_reference() FROM `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION "`+schema+`".`+
		`sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text) TO `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT ready,reason FROM repository_exact_tree_literal_index_gc_readiness()`).Scan(
		&readyAfterFunctionOvergrant, &functionOvergrantReason,
	); err != nil {
		t.Fatal(err)
	}
	if readyAfterFunctionOvergrant || !strings.Contains(functionOvergrantReason, "sandbox checkpoint dependency") {
		t.Fatalf("sandbox checkpoint helper grantee readiness=%v reason=%q",
			readyAfterFunctionOvergrant, functionOvergrantReason)
	}
	if _, err := database.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION "`+schema+`".`+
		`sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text) FROM `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `GRANT USAGE ON SCHEMA "`+schema+`" TO `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	attackConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attackConnection.ExecContext(ctx, `SET ROLE `+unexpectedRole); err != nil {
		attackConnection.Close()
		t.Fatal(err)
	}
	if _, err := attackConnection.ExecContext(ctx, `CREATE TEMP TABLE exact_tree_trigger_attack(id integer)`); err != nil {
		attackConnection.Close()
		t.Fatal(err)
	}
	_, attachErr := attackConnection.ExecContext(ctx, `
CREATE TRIGGER exact_tree_trigger_attack
BEFORE INSERT ON exact_tree_trigger_attack
FOR EACH ROW EXECUTE FUNCTION "`+schema+`".lock_candidate_exact_tree_literal_index_reference()`)
	_, _ = attackConnection.ExecContext(context.Background(), `DROP TABLE IF EXISTS exact_tree_trigger_attack`)
	_, _ = attackConnection.ExecContext(context.Background(), `RESET ROLE`)
	if closeErr := attackConnection.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if attachErr == nil || !strings.Contains(attachErr.Error(), "permission denied") {
		t.Fatalf("hidden role attached private trigger function: %v", attachErr)
	}
	if _, err := database.ExecContext(ctx, `REVOKE USAGE ON SCHEMA "`+schema+`" FROM `+unexpectedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx, "DROP ROLE "+unexpectedRole); err != nil {
		t.Fatal(err)
	}

	actorID, projectID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id,email,display_name,password_hash)
VALUES ($1,$2,'Exact tree GC actor','not-used')`,
		actorID, "exact-tree-gc-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id,name,created_by)
VALUES ($1,'Exact tree GC project',$2)`, projectID, actorID); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	trees := make([]gcManifestFixture, 15)
	sharedContent := "shared exact-tree retention body"
	for index := range trees {
		content := fmt.Sprintf("exact-tree retention body %02d", index)
		if index == 0 || index == 13 {
			content = sharedContent
		}
		trees[index] = seedExactTreeGCManifest(
			t, ctx, database, projectID, fmt.Sprintf("main-%02d", index),
			now.Add(-8*24*time.Hour-time.Duration(index)*time.Hour), content,
		)
	}

	for index, status := range []string{"active", "frozen", "abandoned"} {
		insertRawExactTreeGCCandidate(
			t, ctx, database, actorID, projectID, trees[8+index].treeHash, status,
		)
	}
	insertExactTreeGCClaim(t, ctx, database, projectID, trees[11].treeHash, false)
	insertExactTreeGCClaim(t, ctx, database, projectID, trees[12].treeHash, true)

	ageProjectID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id,name,created_by) VALUES ($1,'Exact tree GC age project',$2)`,
		ageProjectID, actorID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 9; index++ {
		seedExactTreeGCManifest(
			t, ctx, database, ageProjectID, fmt.Sprintf("young-%02d", index),
			now.Add(-6*24*time.Hour-time.Duration(index)*time.Minute),
			fmt.Sprintf("young retention body %02d", index),
		)
	}

	runID := uuid.New()
	capabilities := planExactTreeGC(t, ctx, database, runID, 7*24*time.Hour, 8, 100, 15*time.Minute)
	if len(capabilities) != 3 {
		t.Fatalf("plan capabilities=%d, want expired-claim/shared/unique three: %#v", len(capabilities), capabilities)
	}
	expectedTrees := map[string]int{
		trees[12].treeHash: 13,
		trees[13].treeHash: 14,
		trees[14].treeHash: 15,
	}
	for _, capability := range capabilities {
		if expectedTrees[capability.treeHash] != capability.rank {
			t.Fatalf("unexpected planned tree/rank: %#v", capability)
		}
		if capability.projectID != projectID {
			t.Fatalf("age/protected tenant leaked into plan: %#v", capability)
		}
	}

	if replay := planExactTreeGC(t, ctx, database, runID, 7*24*time.Hour, 8, 100, 15*time.Minute); !sameGCCapabilities(capabilities, replay) {
		t.Fatalf("same run did not replay exact capabilities: first=%#v replay=%#v", capabilities, replay)
	}
	if _, err := database.ExecContext(ctx, `
SELECT * FROM plan_repository_exact_tree_literal_index_gc($1,$2,8,100,$3)`,
		runID, int64((8*24*time.Hour)/time.Millisecond), int((15*time.Minute)/time.Millisecond)); err == nil {
		t.Fatal("run id accepted different retention parameters")
	}

	receipts := make(map[string]gcReceipt)
	for _, capability := range capabilities {
		receipts[capability.treeHash] = executeExactTreeGC(t, ctx, database, capability.id)
	}
	for treeHash, receipt := range receipts {
		if receipt.outcome != "deleted" || receipt.deletedMembers != 1 || receipt.logicalBytes <= 0 {
			t.Fatalf("delete receipt for %s=%#v", treeHash, receipt)
		}
	}
	if shared := receipts[trees[13].treeHash]; shared.deletedBlobs != 0 || shared.blobBytes != 0 {
		t.Fatalf("shared blob was freed: %#v", shared)
	}
	for _, tree := range []gcManifestFixture{trees[12], trees[14]} {
		receipt := receipts[tree.treeHash]
		if receipt.deletedBlobs != 1 || receipt.blobBytes != tree.byteSize {
			t.Fatalf("unique blob receipt for %s=%#v", tree.treeHash, receipt)
		}
	}
	var expiredClaims int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM repository_exact_tree_literal_index_build_claims
WHERE project_id=$1 AND tree_hash=$2`, projectID, trees[12].treeHash).Scan(&expiredClaims); err != nil {
		t.Fatal(err)
	}
	if expiredClaims != 0 {
		t.Fatalf("execute retained %d expired claims", expiredClaims)
	}

	replayReceipt := executeExactTreeGC(t, ctx, database, capabilities[0].id)
	if !replayReceipt.idempotent || replayReceipt.id != receipts[capabilities[0].treeHash].id {
		t.Fatalf("ambiguous retry did not replay receipt: %#v", replayReceipt)
	}
	summary := inspectExactTreeGC(t, ctx, database, runID)
	if summary.status != "completed" || summary.planned != 3 || summary.deleted != 3 ||
		summary.pending != 0 || summary.logicalBytes <= 0 || summary.blobBytes <= 0 {
		t.Fatalf("completed summary=%#v", summary)
	}

	var tombstones int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM repository_exact_tree_literal_index_gc_tombstones
WHERE run_id=$1`, runID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 3 {
		t.Fatalf("tombstones=%d, want 3", tombstones)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE repository_exact_tree_literal_index_gc_receipts
SET executed_at=executed_at WHERE run_id=$1`, runID); err == nil {
		t.Fatal("append-only receipt accepted update")
	}
	if _, err := database.ExecContext(ctx, `
DELETE FROM repository_exact_tree_literal_index_blobs
WHERE project_id=$1 AND content_hash=$2`, projectID, trees[0].contentHash); err == nil {
		t.Fatal("blob delete bypassed exact xid/backend authorization")
	}
	if _, err := database.ExecContext(ctx, `
UPDATE repository_exact_tree_literal_index_blobs
SET created_at=created_at WHERE project_id=$1 AND content_hash=$2`,
		projectID, trees[0].contentHash); err == nil {
		t.Fatal("blob update bypassed immutable guard")
	}
	if _, err := database.ExecContext(ctx, `
TRUNCATE repository_exact_tree_literal_index_members`); err == nil {
		t.Fatal("member truncate bypassed immutable guard")
	}
	var authorizationResidue int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM repository_exact_tree_literal_index_gc_tree_delete_auth)
  +(SELECT count(*) FROM repository_exact_tree_literal_index_gc_blob_delete_auth)`,
	).Scan(&authorizationResidue); err != nil {
		t.Fatal(err)
	}
	if authorizationResidue != 0 {
		t.Fatalf("successful executions left %d private authorization rows", authorizationResidue)
	}

	assertExactTreeGCTerminalOutcomesAndRollback(t, ctx, database, actorID, now)

	// A retained source tree may be indexed again after its exact publication
	// was tombstoned; publication identity plus capability keeps the audit key distinct.
	rebuilt := seedExactTreeGCManifestWithTree(
		t, ctx, database, projectID, trees[14].treeHash, "main-rebuilt",
		now.Add(-time.Hour), "rebuilt exact-tree body",
	)
	if rebuilt.treeHash != trees[14].treeHash {
		t.Fatal("same-tree rebuild changed identity")
	}
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM repository_exact_tree_literal_index_gc_tombstones
WHERE project_id=$1 AND tree_hash=$2`, projectID, rebuilt.treeHash).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("same-tree rebuild changed earlier tombstone count=%d", tombstones)
	}

	downSQL, err := files.ReadFile(repositoryExactTreeLiteralIndexGCMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(downSQL)); err == nil || !strings.Contains(err.Error(), "tombstones") {
		t.Fatalf("rollback did not refuse immutable tombstones: %v", err)
	}
}

func assertExactTreeGCStableRolePreflight(
	t *testing.T,
	ctx context.Context,
	dsn string,
	roleLock *sql.Conn,
	stableRoles []string,
) {
	t.Helper()
	var safeRoleCount int
	if err := roleLock.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_roles AS role
WHERE role.rolname=ANY($1)
  AND NOT role.rolcanlogin
  AND NOT role.rolsuper
  AND NOT role.rolbypassrls
  AND NOT role.rolcreaterole
  AND NOT role.rolcreatedb
  AND NOT role.rolreplication
  AND NOT EXISTS (
    SELECT 1 FROM pg_auth_members AS membership WHERE membership.member=role.oid
  )
  AND NOT EXISTS (
    SELECT 1 FROM pg_auth_members AS membership
    WHERE membership.roleid=role.oid AND membership.admin_option
  )`, stableRoles).Scan(&safeRoleCount); err != nil {
		t.Fatal(err)
	}
	if safeRoleCount != 3 {
		t.Fatalf("stable roles are not a safe isolated trio before preflight canary: %d/3", safeRoleCount)
	}
	var parentRole, adminMember, historicalGrantee string
	renamedRoles := make(map[string]string)
	defer func() {
		_, _ = roleLock.ExecContext(context.Background(), `ALTER ROLE worksflow_application NOCREATEDB`)
		if parentRole != "" {
			_, _ = roleLock.ExecContext(context.Background(), `REVOKE `+parentRole+` FROM worksflow_application`)
			_, _ = roleLock.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+parentRole)
		}
		if adminMember != "" {
			_, _ = roleLock.ExecContext(context.Background(), `REVOKE worksflow_application FROM `+adminMember)
			_, _ = roleLock.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+adminMember)
		}
		if historicalGrantee != "" {
			_, _ = roleLock.ExecContext(context.Background(), `DROP ROLE IF EXISTS `+historicalGrantee)
		}
		for stableRole, backupRole := range renamedRoles {
			var stableExists, backupExists bool
			_ = roleLock.QueryRowContext(context.Background(), `
SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1),
       EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$2)`,
				stableRole, backupRole,
			).Scan(&stableExists, &backupExists)
			if !stableExists && backupExists {
				_, _ = roleLock.ExecContext(context.Background(),
					`ALTER ROLE `+backupRole+` RENAME TO `+stableRole)
			}
		}
	}()

	freshDatabase := "repository_gc_role_gate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, `CREATE DATABASE `+freshDatabase+` TEMPLATE template0`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = roleLock.ExecContext(context.Background(), `
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname=$1 AND pid<>pg_backend_pid()`, freshDatabase)
		if _, err := roleLock.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+freshDatabase); err != nil {
			t.Errorf("drop stable-role preflight database %s: %v", freshDatabase, err)
		}
	}()
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	connectionConfig.Database = freshDatabase
	freshDSN := connectionConfig.ConnString()
	schema := "repository_gc_role_gate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	adminDatabase, err := sql.Open("pgx", freshDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminDatabase.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		adminDatabase.Close()
		t.Fatal(err)
	}
	if err := adminDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, freshDSN, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `
CREATE TABLE schema_migrations (
  version text PRIMARY KEY,
  checksum text NOT NULL,
  down_checksum text,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == repositoryExactTreeLiteralIndexGCMigration+".up.sql" {
			break
		}
		if err := applyFile(ctx, connection, name); err != nil {
			t.Fatalf("prepare role-preflight schema through %s: %v", name, err)
		}
	}
	historicalGrantee = "worksflow_gc_historical_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, `CREATE ROLE `+historicalGrantee+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
GRANT EXECUTE ON FUNCTION guard_repository_exact_tree_literal_index_manifest_insert()
  TO `+historicalGrantee+`;
GRANT EXECUTE ON FUNCTION sandbox_checkpoint_is_exact(
  uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text
) TO `+historicalGrantee); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
GRANT CREATE ON SCHEMA "`+schema+`" TO PUBLIC;
GRANT UPDATE (email) ON users TO worksflow_application;
GRANT SELECT (email) ON users TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	up, err := files.ReadFile(repositoryExactTreeLiteralIndexGCMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	readFingerprint := func() string {
		var fingerprint string
		if err := database.QueryRowContext(ctx, `
SELECT concat_ws('|',
  namespace.nspowner::text,
  coalesce(namespace.nspacl::text,'<null>'),
  coalesce((
    SELECT relation.relowner::text FROM pg_class AS relation
    WHERE relation.oid=to_regclass('repository_exact_tree_literal_index_blobs')
  ),'<missing>'),
  coalesce((
    SELECT attribute.attacl::text
    FROM pg_attribute AS attribute
    WHERE attribute.attrelid='users'::regclass AND attribute.attname='email'
  ),'<null>'),
  coalesce((
    SELECT procedure.proacl::text FROM pg_proc AS procedure
    WHERE procedure.oid=to_regprocedure(
      'renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)'
    )
  ),'<null>'),
  coalesce((
    SELECT procedure.proacl::text FROM pg_proc AS procedure
    WHERE procedure.oid=to_regprocedure(
      'guard_repository_exact_tree_literal_index_manifest_insert()'
    )
  ),'<null>'),
  coalesce((
    SELECT procedure.proacl::text FROM pg_proc AS procedure
    WHERE procedure.oid=to_regprocedure(
      'sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)'
    )
  ),'<null>'),
  coalesce((
    SELECT string_agg(
      defaults.defaclrole::text || ':' || defaults.defaclobjtype::text || ':' ||
      privilege.grantor::text || ':' || privilege.grantee::text || ':' ||
      privilege.privilege_type || ':' || privilege.is_grantable::text,
      ',' ORDER BY defaults.defaclrole,defaults.defaclobjtype,privilege.grantor,
        privilege.grantee,privilege.privilege_type,privilege.is_grantable
    )
    FROM pg_default_acl AS defaults
    CROSS JOIN LATERAL aclexplode(defaults.defaclacl) AS privilege
  ),'<none>'),
  coalesce(to_regclass('repository_exact_tree_literal_index_gc_runs')::text,'<missing>')
)
FROM pg_namespace AS namespace
WHERE namespace.nspname=current_schema()`).Scan(&fingerprint); err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}
	baseline := readFingerprint()
	assertRejectedWithoutMutation := func(reasonFragment string) {
		_, applyErr := database.ExecContext(ctx, string(up))
		if applyErr == nil || !strings.Contains(applyErr.Error(), reasonFragment) {
			t.Fatalf("role preflight error=%v, want %q", applyErr, reasonFragment)
		}
		if after := readFingerprint(); after != baseline {
			t.Fatalf("role preflight mutated schema before rejection\nbefore=%s\nafter=%s", baseline, after)
		}
		var gcObjects int
		if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_class AS relation
JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
WHERE namespace.nspname=current_schema()
  AND relation.relname LIKE 'repository_exact_tree_literal_index_gc_%'`,
		).Scan(&gcObjects); err != nil {
			t.Fatal(err)
		}
		if gcObjects != 0 {
			t.Fatalf("rejected role preflight left %d 000066 relations", gcObjects)
		}
	}

	assertRejectedWithoutMutation("column privileges to be revoked explicitly")
	if _, err := database.ExecContext(ctx, `
REVOKE UPDATE (email) ON users FROM worksflow_application;
REVOKE SELECT (email) ON users FROM PUBLIC`); err != nil {
		t.Fatal(err)
	}
	baseline = readFingerprint()

	if _, err := roleLock.ExecContext(ctx, `ALTER ROLE worksflow_application CREATEDB`); err != nil {
		t.Fatal(err)
	}
	assertRejectedWithoutMutation("NOLOGIN without elevated attributes")
	if _, err := roleLock.ExecContext(ctx, `ALTER ROLE worksflow_application NOCREATEDB`); err != nil {
		t.Fatal(err)
	}

	parentRole = "worksflow_gc_role_parent_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, `CREATE ROLE `+parentRole+` NOLOGIN NOINHERIT`); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx, `GRANT `+parentRole+` TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	assertRejectedWithoutMutation("must not inherit or SET-reach another role")
	if _, err := roleLock.ExecContext(ctx, `REVOKE `+parentRole+` FROM worksflow_application`); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx, `DROP ROLE `+parentRole); err != nil {
		t.Fatal(err)
	}

	adminMember = "worksflow_gc_role_admin_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, `CREATE ROLE `+adminMember+` NOLOGIN NOINHERIT`); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx,
		`GRANT worksflow_application TO `+adminMember+` WITH ADMIN OPTION`,
	); err != nil {
		t.Fatal(err)
	}
	assertRejectedWithoutMutation("must not grant ADMIN OPTION")
	if _, err := roleLock.ExecContext(ctx, `REVOKE worksflow_application FROM `+adminMember); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx, `DROP ROLE `+adminMember); err != nil {
		t.Fatal(err)
	}

	migrationBackup := "worksflow_migration_backup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	operatorBackup := "worksflow_operator_backup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, `ALTER ROLE worksflow_migration_owner RENAME TO `+migrationBackup); err != nil {
		t.Fatal(err)
	}
	renamedRoles["worksflow_migration_owner"] = migrationBackup
	if _, err := roleLock.ExecContext(ctx, `ALTER ROLE worksflow_repository_index_gc_operator RENAME TO `+operatorBackup); err != nil {
		_, _ = roleLock.ExecContext(context.Background(), `ALTER ROLE `+migrationBackup+` RENAME TO worksflow_migration_owner`)
		t.Fatal(err)
	}
	renamedRoles["worksflow_repository_index_gc_operator"] = operatorBackup
	assertRejectedWithoutMutation("all three stable roles or none")
	if _, err := roleLock.ExecContext(ctx, `ALTER ROLE `+operatorBackup+` RENAME TO worksflow_repository_index_gc_operator`); err != nil {
		t.Fatal(err)
	}
	delete(renamedRoles, "worksflow_repository_index_gc_operator")
	if _, err := roleLock.ExecContext(ctx, `ALTER ROLE `+migrationBackup+` RENAME TO worksflow_migration_owner`); err != nil {
		t.Fatal(err)
	}
	delete(renamedRoles, "worksflow_migration_owner")

	backups := make([]string, len(stableRoles))
	for index, role := range stableRoles {
		backups[index] = "worksflow_absent_backup_" + fmt.Sprintf("%d_", index) +
			strings.ReplaceAll(uuid.NewString(), "-", "")
		if _, err := roleLock.ExecContext(ctx, `ALTER ROLE `+role+` RENAME TO `+backups[index]); err != nil {
			for restore := index - 1; restore >= 0; restore-- {
				_, _ = roleLock.ExecContext(context.Background(),
					`ALTER ROLE `+backups[restore]+` RENAME TO `+stableRoles[restore])
			}
			t.Fatal(err)
		}
		renamedRoles[role] = backups[index]
	}
	_, applyErr := database.ExecContext(ctx, string(up))
	if applyErr != nil {
		for index := len(stableRoles) - 1; index >= 0; index-- {
			_, _ = roleLock.ExecContext(context.Background(),
				`ALTER ROLE `+backups[index]+` RENAME TO `+stableRoles[index])
		}
		t.Fatalf("all-absent development posture failed: %v", applyErr)
	}
	var ready, publicSchemaCreate, columnACLs bool
	var reason string
	if err := database.QueryRowContext(ctx, `
SELECT readiness.ready,readiness.reason,
  has_schema_privilege('public',current_schema(),'CREATE'),
  EXISTS (
    SELECT 1
    FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
    JOIN pg_attribute AS attribute ON attribute.attrelid=relation.oid
    CROSS JOIN LATERAL aclexplode(attribute.attacl) AS privilege
    WHERE namespace.nspname=current_schema()
  )
FROM repository_exact_tree_literal_index_gc_readiness() AS readiness`,
	).Scan(&ready, &reason, &publicSchemaCreate, &columnACLs); err != nil {
		t.Fatal(err)
	}
	if ready || !strings.Contains(reason, "worksflow_migration_owner is absent") ||
		publicSchemaCreate || columnACLs {
		t.Fatalf("all-absent development readiness=%v reason=%q public_create=%v column_acls=%v",
			ready, reason, publicSchemaCreate, columnACLs)
	}
	var historicalInternal, historicalHelper bool
	if err := database.QueryRowContext(ctx, `
SELECT
  has_function_privilege($1,'guard_repository_exact_tree_literal_index_manifest_insert()','EXECUTE'),
  has_function_privilege(
    $1,
    'sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)',
    'EXECUTE'
  )`, historicalGrantee).Scan(&historicalInternal, &historicalHelper); err != nil {
		t.Fatal(err)
	}
	if historicalInternal || historicalHelper {
		t.Fatalf("migration preserved historical routine grants internal=%v helper=%v",
			historicalInternal, historicalHelper)
	}
	if _, err := roleLock.ExecContext(ctx, `DROP ROLE `+historicalGrantee); err != nil {
		t.Fatal(err)
	}
	historicalGrantee = ""
	for index := len(stableRoles) - 1; index >= 0; index-- {
		if _, err := roleLock.ExecContext(ctx,
			`ALTER ROLE `+backups[index]+` RENAME TO `+stableRoles[index],
		); err != nil {
			t.Fatal(err)
		}
		delete(renamedRoles, stableRoles[index])
	}
}

func TestRepositoryExactTreeLiteralIndexGCRollbackWithoutTombstonesPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	roleLock, err := base.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roleLock.Close()
	if _, err := roleLock.ExecContext(ctx, "SELECT pg_advisory_lock($1)", postgresStableRoleAdvisoryLockID); err != nil {
		t.Fatal(err)
	}

	freshDatabase := "repository_literal_gc_down_db_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, `CREATE DATABASE `+freshDatabase+` TEMPLATE template0`); err != nil {
		t.Fatal(err)
	}
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	connectionConfig.Database = freshDatabase
	freshDSN := connectionConfig.ConnString()
	freshBase, err := sql.Open("pgx", freshDSN)
	if err != nil {
		t.Fatal(err)
	}
	schema := "repository_literal_gc_down_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := freshBase.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		freshBase.Close()
		t.Fatal(err)
	}
	if err := freshBase.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = roleLock.ExecContext(context.Background(), `
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname=$1 AND pid<>pg_backend_pid()`, freshDatabase)
		if _, err := roleLock.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+freshDatabase); err != nil {
			t.Errorf("drop fresh rollback database %s: %v", freshDatabase, err)
		}
	}()
	database, err := sql.Open("pgx", postgresDSNWithSearchPath(t, freshDSN, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Up(ctx, database); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	actorID, projectID := uuid.New(), uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id,email,display_name,password_hash)
VALUES ($1,$2,'GC rollback actor','not-used')`,
		actorID, "gc-down-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id,name,created_by)
VALUES ($1,'GC rollback project',$2)`, projectID, actorID); err != nil {
		t.Fatal(err)
	}
	manifest := seedExactTreeGCManifest(
		t, ctx, database, projectID, "down-canary",
		time.Now().UTC().Add(-8*24*time.Hour), "rollback immutable body",
	)
	downSQL, err := files.ReadFile(repositoryExactTreeLiteralIndexGCMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(downSQL)); err != nil {
		t.Fatalf("clean GC rollback: %v", err)
	}
	var executeFunction sql.NullString
	if err := database.QueryRowContext(ctx, `
SELECT to_regprocedure('execute_repository_exact_tree_literal_index_gc(uuid)')::text`,
	).Scan(&executeFunction); err != nil {
		t.Fatal(err)
	}
	if executeFunction.Valid {
		t.Fatalf("execute function survived rollback as %q", executeFunction.String)
	}
	if _, err := database.ExecContext(ctx, `
DELETE FROM repository_exact_tree_literal_index_members
WHERE project_id=$1 AND tree_hash=$2`, projectID, manifest.treeHash); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("rollback did not restore unconditional member guard: %v", err)
	}
	var publicClaim bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_proc AS procedure
  CROSS JOIN LATERAL aclexplode(
    COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
  ) AS privilege
  WHERE procedure.oid=
    'renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)'::regprocedure
    AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
)`).Scan(&publicClaim); err != nil {
		t.Fatal(err)
	}
	if !publicClaim {
		t.Fatal("rollback did not restore PUBLIC claim-function compatibility")
	}
	var publicCandidate bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_proc AS procedure
  CROSS JOIN LATERAL aclexplode(
    COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
  ) AS privilege
  WHERE procedure.oid='abandon_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)'::regprocedure
    AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
)`).Scan(&publicCandidate); err != nil {
		t.Fatal(err)
	}
	if !publicCandidate {
		t.Fatal("rollback did not restore PUBLIC Candidate-function compatibility")
	}
	var routinesWithoutPublic int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid=procedure.pronamespace
WHERE namespace.nspname=$1
  AND NOT EXISTS (
    SELECT 1 FROM aclexplode(
      COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
    ) AS privilege
    WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
  )`, schema).Scan(&routinesWithoutPublic); err != nil {
		t.Fatal(err)
	}
	if routinesWithoutPublic != 0 {
		t.Fatalf("rollback left %d predecessor routines without PUBLIC EXECUTE", routinesWithoutPublic)
	}
	futureFunction := "exact_tree_gc_down_future_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := database.ExecContext(ctx, `CREATE FUNCTION "`+schema+`".`+futureFunction+`()
RETURNS integer LANGUAGE sql AS 'SELECT 1'`); err != nil {
		t.Fatal(err)
	}
	var futurePublic bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_proc AS procedure
  CROSS JOIN LATERAL aclexplode(
    COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
  ) AS privilege
  WHERE procedure.oid=to_regprocedure($1)
    AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
)`, schema+"."+futureFunction+"()").Scan(&futurePublic); err != nil {
		t.Fatal(err)
	}
	if !futurePublic {
		t.Fatal("rollback did not restore the future PUBLIC routine default")
	}
	if _, err := database.ExecContext(ctx, `DROP FUNCTION "`+schema+`".`+futureFunction+`()`); err != nil {
		t.Fatal(err)
	}
}

type gcManifestFixture struct {
	treeHash    string
	contentHash string
	byteSize    int64
}

type gcCapability struct {
	id        uuid.UUID
	projectID uuid.UUID
	treeHash  string
	rank      int
	expiresAt time.Time
}

type gcReceipt struct {
	id             uuid.UUID
	outcome        string
	deletedMembers int
	deletedBlobs   int
	logicalBytes   int64
	blobBytes      int64
	idempotent     bool
}

type gcSummary struct {
	status       string
	planned      int
	deleted      int
	protected    int
	stale        int
	expired      int
	pending      int
	logicalBytes int64
	blobBytes    int64
}

func seedExactTreeGCManifest(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	label string,
	readyAt time.Time,
	body string,
) gcManifestFixture {
	t.Helper()
	return seedExactTreeGCManifestWithTree(
		t, ctx, database, projectID, applicationBuildContractCanaryDigest("gc-tree-"+label),
		label, readyAt, body,
	)
}

func seedExactTreeGCManifestWithTree(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	label string,
	readyAt time.Time,
	body string,
) gcManifestFixture {
	t.Helper()
	contentHash := applicationBuildContractCanaryDigest("gc-body-" + body)
	treeCommitment := applicationBuildContractCanaryDigest("gc-tree-commitment-" + label)
	indexCommitment := applicationBuildContractCanaryDigest("gc-index-commitment-" + label)
	byteSize := int64(len([]byte(body)))
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_blobs (
  project_id,content_hash,byte_size,is_text,body,created_at
) VALUES ($1,$2,$3,true,$4,$5)
ON CONFLICT (project_id,content_hash) DO NOTHING`,
		projectID, contentHash, byteSize, body, readyAt.Add(-time.Minute)); err != nil {
		t.Fatalf("seed GC blob %s: %v", label, err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_manifests (
  project_id,tree_hash,schema_version,status,file_count,text_file_count,
  skipped_file_count,total_bytes,tree_commitment,index_commitment,created_at
) VALUES ($1,$2,'repository-exact-tree-literal-index/v1','building',1,1,0,$3,$4,$5,$6)`,
		projectID, treeHash, byteSize, treeCommitment, indexCommitment,
		readyAt.Add(-time.Minute)); err != nil {
		t.Fatalf("seed GC manifest %s: %v", label, err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_members (
  project_id,tree_hash,path,mode,content_hash,byte_size,indexed
) VALUES ($1,$2,$3,'100644',$4,$5,true)`,
		projectID, treeHash, label+".txt", contentHash, byteSize); err != nil {
		t.Fatalf("seed GC member %s: %v", label, err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE repository_exact_tree_literal_index_manifests
SET status='ready',ready_at=$3
WHERE project_id=$1 AND tree_hash=$2`, projectID, treeHash, readyAt); err != nil {
		t.Fatalf("publish GC manifest %s: %v", label, err)
	}
	return gcManifestFixture{treeHash: treeHash, contentHash: contentHash, byteSize: byteSize}
}

func insertRawExactTreeGCCandidate(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	actorID uuid.UUID,
	projectID uuid.UUID,
	treeHash string,
	status string,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	digest := applicationBuildContractCanaryDigest("gc-candidate-" + uuid.NewString())
	rawHash := strings.TrimPrefix(digest, "sha256:")
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO candidate_workspaces (
  id,schema_version,project_id,repository_snapshot_id,
  build_manifest_id,build_manifest_hash,build_contract_id,build_contract_hash,
  full_stack_template_id,full_stack_template_hash,
  base_tree_store,base_tree_owner_id,base_tree_ref,base_tree_content_hash,base_tree_hash,
  current_tree_store,current_tree_owner_id,current_tree_ref,current_tree_content_hash,current_tree_hash,
  current_tree_file_count,current_tree_byte_size,status,dirty,conflicted,stale,rebase_required,
  session_epoch,version,journal_sequence,writer_lease_epoch,created_by,created_at,updated_at
) VALUES (
  $1,'candidate-workspace/v1',$2,$3,$4,$5,$6,$7,$8,$9,
  'blob',$3,'blob://gc/base',$10,$10,'blob',$3,'blob://gc/current',$10,$11,
  1,1,$12,false,false,false,false,1,1,0,0,$13,$14,$14
)`, uuid.New(), projectID, uuid.New(), uuid.New(), rawHash, uuid.New(), rawHash,
		uuid.New(), digest, digest, treeHash, status, actorID,
		time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatalf("insert raw %s Candidate protection: %v", status, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func insertExactTreeGCClaim(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	expired bool,
) {
	t.Helper()
	renewedAt := time.Now().UTC().Truncate(time.Microsecond)
	expiresAt := renewedAt.Add(4 * time.Minute)
	if expired {
		renewedAt = renewedAt.Add(-time.Minute)
		expiresAt = renewedAt.Add(30 * time.Second)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_build_claims (
  project_id,tree_hash,owner_token,attempt,reserved_source_bytes,
  acquired_at,renewed_at,lease_expires_at
) VALUES ($1,$2,$3,1,1,$4,$5,$6)`,
		projectID, treeHash, uuid.New(), renewedAt.Add(-time.Minute), renewedAt, expiresAt); err != nil {
		t.Fatalf("insert GC claim expired=%v: %v", expired, err)
	}
}

func planExactTreeGC(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	runID uuid.UUID,
	retention time.Duration,
	keep int,
	batch int,
	ttl time.Duration,
) []gcCapability {
	t.Helper()
	rows, err := database.QueryContext(ctx, `
SELECT capability_id,project_id,tree_hash,planned_rank,expires_at
FROM plan_repository_exact_tree_literal_index_gc($1,$2,$3,$4,$5)`,
		runID, int64(retention/time.Millisecond), keep, batch, int(ttl/time.Millisecond))
	if err != nil {
		t.Fatalf("plan exact-tree GC: %v", err)
	}
	defer rows.Close()
	var result []gcCapability
	for rows.Next() {
		var capability gcCapability
		if err := rows.Scan(
			&capability.id, &capability.projectID, &capability.treeHash,
			&capability.rank, &capability.expiresAt,
		); err != nil {
			t.Fatal(err)
		}
		result = append(result, capability)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].id.String() < result[right].id.String()
	})
	return result
}

func sameGCCapabilities(left, right []gcCapability) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].id != right[index].id || left[index].projectID != right[index].projectID ||
			left[index].treeHash != right[index].treeHash || left[index].rank != right[index].rank ||
			!left[index].expiresAt.Equal(right[index].expiresAt) {
			return false
		}
	}
	return true
}

func executeExactTreeGC(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	capabilityID uuid.UUID,
) gcReceipt {
	t.Helper()
	var receipt gcReceipt
	if err := database.QueryRowContext(ctx, `
SELECT receipt_id,outcome,deleted_member_count,deleted_blob_count,
       logical_bytes_released,blob_bytes_freed,idempotent
FROM execute_repository_exact_tree_literal_index_gc($1)`, capabilityID).Scan(
		&receipt.id, &receipt.outcome, &receipt.deletedMembers, &receipt.deletedBlobs,
		&receipt.logicalBytes, &receipt.blobBytes, &receipt.idempotent,
	); err != nil {
		t.Fatalf("execute exact-tree GC %s: %v", capabilityID, err)
	}
	return receipt
}

func inspectExactTreeGC(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	runID uuid.UUID,
) gcSummary {
	t.Helper()
	var summary gcSummary
	if err := database.QueryRowContext(ctx, `
SELECT run_status,planned_capability_count,deleted_capability_count,
       protected_capability_count,stale_capability_count,expired_capability_count,
       pending_capability_count,logical_bytes_released,blob_bytes_freed
FROM inspect_repository_exact_tree_literal_index_gc_run($1)`, runID).Scan(
		&summary.status, &summary.planned, &summary.deleted, &summary.protected,
		&summary.stale, &summary.expired, &summary.pending,
		&summary.logicalBytes, &summary.blobBytes,
	); err != nil {
		t.Fatalf("inspect exact-tree GC run %s: %v", runID, err)
	}
	return summary
}

func assertExactTreeGCNamespaceAndFunctionBoundary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	schema string,
) {
	t.Helper()
	var polluted int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_class AS relation
JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
WHERE namespace.nspname='pg_catalog'
  AND relation.relname LIKE 'repository_exact_tree_literal_index_gc_%'`).Scan(&polluted); err != nil {
		t.Fatal(err)
	}
	if polluted != 0 {
		t.Fatalf("migration polluted pg_catalog with %d GC relations", polluted)
	}

	var tables int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_class AS relation
JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace
WHERE namespace.nspname=$1
  AND relation.relkind='r'
  AND relation.relname IN (
    'repository_exact_tree_literal_index_blobs',
    'repository_exact_tree_literal_index_manifests',
    'repository_exact_tree_literal_index_members',
    'repository_exact_tree_literal_index_build_claims',
    'repository_exact_tree_literal_index_gc_runs',
    'repository_exact_tree_literal_index_gc_capabilities',
    'repository_exact_tree_literal_index_gc_receipts',
    'repository_exact_tree_literal_index_gc_tombstones',
    'repository_exact_tree_literal_index_gc_tree_delete_auth',
    'repository_exact_tree_literal_index_gc_blob_delete_auth'
  )`, schema).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 10 {
		t.Fatalf("trusted schema has %d/10 exact-index boundary tables", tables)
	}
	var predecessorASCIIIndex, predecessorQuotaIndex string
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT relname FROM pg_class
   WHERE oid=to_regclass('repository_exact_tree_literal_index_blob_project_ascii_body_trgm_idx')),
  (SELECT relname FROM pg_class
   WHERE oid=to_regclass('repository_exact_tree_literal_index_build_claim_project_quota_idx'))
`).Scan(&predecessorASCIIIndex, &predecessorQuotaIndex); err != nil {
		t.Fatal(err)
	}
	if predecessorASCIIIndex != "repository_exact_tree_literal_index_blob_project_ascii_body_trg" ||
		predecessorQuotaIndex != "repository_exact_tree_literal_index_build_claim_project_quota_i" {
		t.Fatalf("unexpected predecessor NAMEDATALEN identities ascii=%q quota=%q",
			predecessorASCIIIndex, predecessorQuotaIndex)
	}

	functionNames := []string{
		"guard_repository_exact_tree_literal_index_gc_audit_mutation",
		"guard_repository_exact_tree_literal_index_blob_mutation",
		"guard_repository_exact_tree_literal_index_member_mutation",
		"guard_repository_exact_tree_literal_index_manifest_delete",
		"guard_repository_exact_tree_literal_index_manifest_insert",
		"guard_repository_exact_tree_literal_index_member_insert",
		"publish_repository_exact_tree_literal_index_manifest",
		"lock_candidate_exact_tree_literal_index_reference",
		"plan_repository_exact_tree_literal_index_gc",
		"execute_repository_exact_tree_literal_index_gc",
		"inspect_repository_exact_tree_literal_index_gc_run",
		"repository_exact_tree_literal_index_gc_readiness",
		"acquire_repository_exact_tree_literal_index_build_claim",
		"renew_repository_exact_tree_literal_index_build_claim",
		"release_repository_exact_tree_literal_index_build_claim",
		"acquire_candidate_workspace_lease",
		"rotate_candidate_workspace_session",
		"update_candidate_workspace_flags",
		"freeze_candidate_workspace",
		"abandon_candidate_workspace",
		"abandon_sandbox_session_candidate",
		"complete_abandoned_sandbox_session",
		"sandbox_checkpoint_is_exact",
	}
	var functionCount, definerCount, exactPathCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*),count(*) FILTER (WHERE procedure.prosecdef),
       count(*) FILTER (
         WHERE procedure.proconfig=ARRAY[$3]::text[]
       )
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid=procedure.pronamespace
WHERE namespace.nspname=$1 AND procedure.proname=ANY($2)`,
		schema, functionNames, "search_path=pg_catalog, "+schema+", pg_temp",
	).Scan(&functionCount, &definerCount, &exactPathCount); err != nil {
		t.Fatal(err)
	}
	if functionCount != 23 || definerCount != 19 || exactPathCount != 23 {
		t.Fatalf("function boundary count=%d definer=%d exact_path=%d", functionCount, definerCount, exactPathCount)
	}
}

func assertExactTreeGCRolePrivileges(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	schema string,
) {
	t.Helper()
	gcFunctions := []string{
		"plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)",
		"execute_repository_exact_tree_literal_index_gc(uuid)",
		"inspect_repository_exact_tree_literal_index_gc_run(uuid)",
		"repository_exact_tree_literal_index_gc_readiness()",
	}
	for _, function := range gcFunctions {
		var operator, application, public bool
		if err := database.QueryRowContext(ctx, `
SELECT has_function_privilege('worksflow_repository_index_gc_operator',$1,'EXECUTE'),
       has_function_privilege('worksflow_application',$1,'EXECUTE'),
       EXISTS (
         SELECT 1 FROM pg_proc AS procedure
         CROSS JOIN LATERAL aclexplode(
           COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
         ) AS privilege
         WHERE procedure.oid=$1::regprocedure
           AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
       )`, function).Scan(&operator, &application, &public); err != nil {
			t.Fatal(err)
		}
		if !operator || application || public {
			t.Fatalf("GC ACL %s operator=%v application=%v public=%v", function, operator, application, public)
		}
	}
	for _, function := range []string{
		"acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer,integer,bigint,integer)",
		"renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)",
		"release_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint)",
	} {
		var application, public bool
		if err := database.QueryRowContext(ctx, `
SELECT has_function_privilege('worksflow_application',$1,'EXECUTE'),
       EXISTS (
         SELECT 1 FROM pg_proc AS procedure
         CROSS JOIN LATERAL aclexplode(
           COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
         ) AS privilege
         WHERE procedure.oid=$1::regprocedure
           AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
       )`, function).Scan(&application, &public); err != nil {
			t.Fatal(err)
		}
		if !application || public {
			t.Fatalf("claim ACL %s application=%v public=%v", function, application, public)
		}
	}
	for _, function := range []string{
		"acquire_candidate_workspace_lease(uuid,bigint,uuid,integer)",
		"rotate_candidate_workspace_session(uuid,bigint,bigint,uuid)",
		"update_candidate_workspace_flags(uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text)",
		"freeze_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)",
		"abandon_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)",
		"abandon_sandbox_session_candidate(uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid)",
		"complete_abandoned_sandbox_session(uuid,bigint,bigint,uuid)",
	} {
		var application, operator, public bool
		if err := database.QueryRowContext(ctx, `
SELECT has_function_privilege('worksflow_application',$1,'EXECUTE'),
       has_function_privilege('worksflow_repository_index_gc_operator',$1,'EXECUTE'),
       EXISTS (
         SELECT 1 FROM pg_proc AS procedure
         CROSS JOIN LATERAL aclexplode(
           COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
         ) AS privilege
         WHERE procedure.oid=$1::regprocedure
           AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
       )`, function).Scan(&application, &operator, &public); err != nil {
			t.Fatal(err)
		}
		if !application || operator || public {
			t.Fatalf("Candidate/Sandbox ACL %s application=%v operator=%v public=%v",
				function, application, operator, public)
		}
	}

	var publicRoutineCount, operatorRoutineCount, applicationDefinerCount int
	var applicationInternalCount, applicationGrantOptionCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  count(*) FILTER (
    WHERE EXISTS (
      SELECT 1 FROM aclexplode(
        COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
      ) AS privilege
      WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
    )
  ),
  count(*) FILTER (
    WHERE has_function_privilege(
      'worksflow_repository_index_gc_operator',procedure.oid,'EXECUTE'
    )
  ),
  count(*) FILTER (
    WHERE procedure.prosecdef
      AND has_function_privilege('worksflow_application',procedure.oid,'EXECUTE')
  ),
  count(*) FILTER (
    WHERE procedure.proname=ANY($2)
      AND has_function_privilege('worksflow_application',procedure.oid,'EXECUTE')
  ),
  count(*) FILTER (
    WHERE EXISTS (
      SELECT 1 FROM aclexplode(
        COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
      ) AS privilege
      WHERE privilege.grantee='worksflow_application'::regrole
        AND privilege.privilege_type='EXECUTE'
        AND privilege.is_grantable
    )
  )
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid=procedure.pronamespace
WHERE namespace.nspname=$1`, schema, []string{
		"guard_repository_exact_tree_literal_index_gc_audit_mutation",
		"lock_candidate_exact_tree_literal_index_reference",
		"guard_repository_exact_tree_literal_index_blob_mutation",
		"guard_repository_exact_tree_literal_index_member_mutation",
		"guard_repository_exact_tree_literal_index_manifest_delete",
		"guard_repository_exact_tree_literal_index_manifest_insert",
		"guard_repository_exact_tree_literal_index_member_insert",
		"publish_repository_exact_tree_literal_index_manifest",
	}).Scan(
		&publicRoutineCount, &operatorRoutineCount, &applicationDefinerCount,
		&applicationInternalCount, &applicationGrantOptionCount,
	); err != nil {
		t.Fatal(err)
	}
	if publicRoutineCount != 0 || operatorRoutineCount != 4 || applicationDefinerCount != 10 ||
		applicationInternalCount != 0 || applicationGrantOptionCount != 0 {
		t.Fatalf("schema routine boundary public=%d operator=%d app_definer=%d app_internal=%d app_grant_option=%d",
			publicRoutineCount, operatorRoutineCount, applicationDefinerCount,
			applicationInternalCount, applicationGrantOptionCount)
	}

	var exactInternalCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_proc AS procedure
WHERE procedure.oid IN (
  'guard_repository_exact_tree_literal_index_gc_audit_mutation()'::regprocedure,
  'guard_repository_exact_tree_literal_index_blob_mutation()'::regprocedure,
  'guard_repository_exact_tree_literal_index_member_mutation()'::regprocedure,
  'guard_repository_exact_tree_literal_index_manifest_delete()'::regprocedure,
  'guard_repository_exact_tree_literal_index_manifest_insert()'::regprocedure,
  'guard_repository_exact_tree_literal_index_member_insert()'::regprocedure,
  'publish_repository_exact_tree_literal_index_manifest()'::regprocedure,
  'lock_candidate_exact_tree_literal_index_reference()'::regprocedure
)
  AND procedure.proowner='worksflow_migration_owner'::regrole
  AND NOT EXISTS (
    SELECT 1
    FROM aclexplode(
      COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
    ) AS privilege
    WHERE privilege.privilege_type='EXECUTE'
      AND privilege.grantee<>procedure.proowner
  )`).Scan(&exactInternalCount); err != nil {
		t.Fatal(err)
	}
	if exactInternalCount != 8 {
		t.Fatalf("internal owner-only routine count=%d/8", exactInternalCount)
	}

	var helperOwner string
	var helperDefiner, helperStableSQL, helperReturnsBoolean bool
	var helperApplication, helperOperator, helperPublic, helperUnexpected bool
	if err := database.QueryRowContext(ctx, `
SELECT
  procedure.proowner::regrole::text,
  procedure.prosecdef,
  procedure.provolatile='s' AND language.lanname='sql'
    AND procedure.proconfig=ARRAY[$2]::text[],
  procedure.prorettype='boolean'::regtype AND NOT procedure.proretset,
  has_function_privilege('worksflow_application',procedure.oid,'EXECUTE'),
  has_function_privilege('worksflow_repository_index_gc_operator',procedure.oid,'EXECUTE'),
  EXISTS (
    SELECT 1 FROM aclexplode(
      COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
    ) AS privilege
    WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
  ),
  EXISTS (
    SELECT 1 FROM aclexplode(
      COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
    ) AS privilege
    WHERE privilege.privilege_type='EXECUTE'
      AND (
        privilege.grantee NOT IN (
          procedure.proowner,'worksflow_application'::regrole
        )
        OR (
          privilege.grantee='worksflow_application'::regrole
          AND privilege.is_grantable
        )
      )
  )
FROM pg_proc AS procedure
JOIN pg_language AS language ON language.oid=procedure.prolang
WHERE procedure.oid=$1::regprocedure`,
		"sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)",
		"search_path=pg_catalog, "+schema+", pg_temp",
	).Scan(
		&helperOwner, &helperDefiner, &helperStableSQL, &helperReturnsBoolean,
		&helperApplication, &helperOperator, &helperPublic, &helperUnexpected,
	); err != nil {
		t.Fatal(err)
	}
	if helperOwner != "worksflow_migration_owner" || helperDefiner || !helperStableSQL ||
		!helperReturnsBoolean || !helperApplication || helperOperator || helperPublic || helperUnexpected {
		t.Fatalf("sandbox helper owner=%q definer=%v stable_sql=%v boolean=%v app=%v operator=%v public=%v unexpected=%v",
			helperOwner, helperDefiner, helperStableSQL, helperReturnsBoolean,
			helperApplication, helperOperator, helperPublic, helperUnexpected)
	}

	futureFunction := "exact_tree_gc_future_default_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	futureConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := futureConnection.ExecContext(ctx, `SET ROLE worksflow_migration_owner`); err != nil {
		futureConnection.Close()
		t.Fatal(err)
	}
	if _, err := futureConnection.ExecContext(ctx, `CREATE FUNCTION "`+schema+`".`+futureFunction+`()
RETURNS integer LANGUAGE sql AS 'SELECT 1'`); err != nil {
		futureConnection.Close()
		t.Fatal(err)
	}
	if _, err := futureConnection.ExecContext(ctx, `RESET ROLE`); err != nil {
		futureConnection.Close()
		t.Fatal(err)
	}
	if err := futureConnection.Close(); err != nil {
		t.Fatal(err)
	}
	var futurePublic bool
	var futureOwner string
	if err := database.QueryRowContext(ctx, `
SELECT
  EXISTS (
    SELECT 1 FROM aclexplode(
      COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
    ) AS privilege
    WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
  ),
  procedure.proowner::regrole::text
FROM pg_proc AS procedure
WHERE procedure.oid=to_regprocedure($1)`, schema+"."+futureFunction+"()",
	).Scan(&futurePublic, &futureOwner); err != nil {
		t.Fatal(err)
	}
	if futurePublic || futureOwner != "worksflow_migration_owner" {
		t.Fatalf("future function public=%v owner=%q", futurePublic, futureOwner)
	}
	if _, err := database.ExecContext(ctx, `DROP FUNCTION "`+schema+`".`+futureFunction+`()`); err != nil {
		t.Fatal(err)
	}
	principalFutureFunction := "exact_tree_gc_principal_default_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := database.ExecContext(ctx, `CREATE FUNCTION "`+schema+`".`+principalFutureFunction+`()
RETURNS integer LANGUAGE sql AS 'SELECT 1'`); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_proc AS procedure
  CROSS JOIN LATERAL aclexplode(
    COALESCE(procedure.proacl,acldefault('f',procedure.proowner))
  ) AS privilege
  WHERE procedure.oid=to_regprocedure($1)
    AND privilege.grantee=0 AND privilege.privilege_type='EXECUTE'
)`, schema+"."+principalFutureFunction+"()").Scan(&futurePublic); err != nil {
		t.Fatal(err)
	}
	if futurePublic {
		t.Fatal("migration-principal future function inherited PUBLIC EXECUTE")
	}
	if _, err := database.ExecContext(ctx, `DROP FUNCTION "`+schema+`".`+principalFutureFunction+`()`); err != nil {
		t.Fatal(err)
	}

	var schemaRead, schemaWrite, gcTableRead bool
	if err := database.QueryRowContext(ctx, `
SELECT has_table_privilege('worksflow_application','schema_migrations','SELECT'),
       has_table_privilege('worksflow_application','schema_migrations','INSERT,UPDATE,DELETE,TRUNCATE'),
       has_table_privilege(
         'worksflow_application','repository_exact_tree_literal_index_gc_runs','SELECT'
       )`).Scan(&schemaRead, &schemaWrite, &gcTableRead); err != nil {
		t.Fatal(err)
	}
	if !schemaRead || schemaWrite || gcTableRead {
		t.Fatalf("application table boundary schema_read=%v schema_write=%v gc_read=%v", schemaRead, schemaWrite, gcTableRead)
	}

	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SET ROLE worksflow_application`); err != nil {
		t.Fatal(err)
	}
	var head int
	if err := connection.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&head); err != nil || head < 66 {
		t.Fatalf("application read schema head count=%d err=%v", head, err)
	}
	if _, err := connection.ExecContext(ctx, `
SELECT * FROM repository_exact_tree_literal_index_gc_readiness()`); err == nil {
		t.Fatal("application executed operator readiness")
	}
	if _, err := connection.ExecContext(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, `SET ROLE worksflow_repository_index_gc_operator`); err != nil {
		t.Fatal(err)
	}
	var ready bool
	var readinessReason string
	var trustedSchema string
	var operatorExists, applicationExists, migrationExists, stableRoles bool
	var objectsOwned, operatorExecute, applicationClaims bool
	var schemaHeadRead, publicClaims, publicSchema bool
	if err := connection.QueryRowContext(ctx, `
SELECT ready,reason,trusted_schema,operator_role_exists,application_role_exists,
       migration_owner_role_exists,stable_group_roles_safe,objects_owned_by_migration_owner,
       operator_execute_granted,application_claim_execute_granted,
       application_schema_head_read_granted,public_claim_execute_revoked,
       public_schema_create_revoked
FROM repository_exact_tree_literal_index_gc_readiness()`).Scan(
		&ready, &readinessReason, &trustedSchema, &operatorExists, &applicationExists,
		&migrationExists, &stableRoles, &objectsOwned, &operatorExecute,
		&applicationClaims, &schemaHeadRead, &publicClaims, &publicSchema,
	); err != nil {
		t.Fatalf("operator readiness: %v", err)
	}
	if !ready || readinessReason != "ready" || trustedSchema != schema || !operatorExists ||
		!applicationExists || !migrationExists || !stableRoles || !objectsOwned ||
		!operatorExecute || !applicationClaims || !schemaHeadRead || !publicClaims || !publicSchema {
		t.Fatalf("readiness ready=%v reason=%q trusted=%q operator=%v app=%v migration=%v roles=%v owners=%v execute=%v appclaims=%v head=%v claims=%v schema=%v",
			ready, readinessReason, trustedSchema, operatorExists, applicationExists,
			migrationExists, stableRoles, objectsOwned, operatorExecute, applicationClaims,
			schemaHeadRead, publicClaims, publicSchema)
	}
	zeroRun := uuid.New()
	rows, err := connection.QueryContext(ctx, `
SELECT capability_id
FROM plan_repository_exact_tree_literal_index_gc($1,604800000,8,1,1000)`, zeroRun)
	if err != nil {
		t.Fatalf("operator plan: %v", err)
	}
	if rows.Next() {
		t.Fatal("empty database unexpectedly planned an index")
	}
	rows.Close()
	var status string
	if err := connection.QueryRowContext(ctx, `
SELECT run_status FROM inspect_repository_exact_tree_literal_index_gc_run($1)`, zeroRun).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("zero-capability run status=%q", status)
	}
	if _, err := connection.ExecContext(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
}

func assertExactTreeGCApplicationLoginCriticalPaths(
	t *testing.T,
	ctx context.Context,
	adminDatabase *sql.DB,
	applicationDatabase *sql.DB,
) {
	t.Helper()
	seed := seedRepositoryCandidateCanary(t, ctx, adminDatabase)
	candidate := createSandboxCandidateCanary(
		t, ctx, adminDatabase, seed, "gc-application-login-sandbox",
	)
	var leaseExpiresAt time.Time
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT candidate_version,session_epoch,writer_lease_epoch,writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1,$2,$3,300)`,
		candidate.id, candidate.version, seed.actorID,
	).Scan(
		&candidate.version, &candidate.sessionEpoch, &candidate.writerLeaseEpoch, &leaseExpiresAt,
	); err != nil {
		t.Fatalf("application login acquire initial Candidate lease: %v", err)
	}
	sessionID := uuid.New()
	insertSandboxSessionCanary(
		t, ctx, applicationDatabase, seed, candidate.id, sessionID, uuid.Nil, true,
	)
	transition, err := transitionSandboxSessionCanary(
		ctx, applicationDatabase, sessionID, 1, 1,
		"starting", seed.actorID, "application login start", uuid.Nil,
	)
	if err != nil {
		t.Fatalf("application login start SandboxSession: %v", err)
	}
	transition, err = transitionSandboxSessionCanary(
		ctx, applicationDatabase, sessionID, transition.version, transition.sessionEpoch,
		"ready", seed.actorID, "application login ready", uuid.Nil,
	)
	if err != nil {
		t.Fatalf("application login ready SandboxSession: %v", err)
	}
	checkpointID := createSandboxCheckpointCanary(
		t, ctx, applicationDatabase, candidate.id, seed.actorID, "application login checkpoint",
	)
	var attachedVersion int64
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT attach_sandbox_session_checkpoint($1,$2,$3,$4,$5)`,
		sessionID, transition.version, transition.sessionEpoch, seed.actorID, checkpointID,
	).Scan(&attachedVersion); err != nil {
		t.Fatalf("application login attach checkpoint: %v", err)
	}
	transition, err = transitionSandboxSessionCanary(
		ctx, applicationDatabase, sessionID, attachedVersion, transition.sessionEpoch,
		"suspending", seed.actorID, "application login suspend", checkpointID,
	)
	if err != nil {
		t.Fatalf("application login suspend SandboxSession: %v", err)
	}
	transition, err = transitionSandboxSessionCanary(
		ctx, applicationDatabase, sessionID, transition.version, transition.sessionEpoch,
		"suspended", seed.actorID, "application login suspended", checkpointID,
	)
	if err != nil {
		t.Fatalf("application login suspended SandboxSession: %v", err)
	}
	transition, err = transitionSandboxSessionCanary(
		ctx, applicationDatabase, sessionID, transition.version, transition.sessionEpoch,
		"resuming", seed.actorID, "application login resume", checkpointID,
	)
	if err != nil {
		t.Fatalf("application login resume SandboxSession: %v", err)
	}
	transition, err = transitionSandboxSessionCanary(
		ctx, applicationDatabase, sessionID, transition.version, transition.sessionEpoch,
		"ready", seed.actorID, "application login resumed ready", checkpointID,
	)
	if err != nil {
		t.Fatalf("application login resumed-ready SandboxSession: %v", err)
	}
	candidate = readSandboxCandidateCanary(t, ctx, adminDatabase, candidate.id)
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT candidate_version,session_epoch,writer_lease_epoch,writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1,$2,$3,300)`,
		candidate.id, candidate.version, seed.actorID,
	).Scan(
		&candidate.version, &candidate.sessionEpoch, &candidate.writerLeaseEpoch, &leaseExpiresAt,
	); err != nil {
		t.Fatalf("application login reacquire resumed Candidate lease: %v", err)
	}
	var syncedVersion, syncedEpoch, syncedCandidateVersion int64
	var syncedTreeHash string
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT session_version,session_epoch,candidate_version,candidate_tree_hash
FROM sync_sandbox_session_candidate($1,$2,$3,$4)`,
		sessionID, transition.version, transition.sessionEpoch, seed.actorID,
	).Scan(&syncedVersion, &syncedEpoch, &syncedCandidateVersion, &syncedTreeHash); err != nil {
		t.Fatalf("application login sync resumed Candidate: %v", err)
	}
	transition.version = syncedVersion
	transition.sessionEpoch = syncedEpoch
	checkpointID = createSandboxCheckpointCanary(
		t, ctx, applicationDatabase, candidate.id, seed.actorID, "application login abandon checkpoint",
	)
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT attach_sandbox_session_checkpoint($1,$2,$3,$4,$5)`,
		sessionID, transition.version, transition.sessionEpoch, seed.actorID, checkpointID,
	).Scan(&attachedVersion); err != nil {
		t.Fatalf("application login attach abandon checkpoint: %v", err)
	}
	var abandonedVersion, abandonedEpoch, abandonedCandidateVersion int64
	var abandonedState string
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT session_version,session_state,session_epoch,candidate_version
FROM abandon_sandbox_session_candidate($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		sessionID, candidate.id, attachedVersion, transition.sessionEpoch,
		candidate.version, candidate.writerLeaseEpoch, seed.actorID, checkpointID,
		"application login abandon", seed.projectID,
	).Scan(
		&abandonedVersion, &abandonedState, &abandonedEpoch, &abandonedCandidateVersion,
	); err != nil {
		t.Fatalf("application login abandon Candidate/SandboxSession: %v", err)
	}
	if abandonedState != "terminating" || abandonedCandidateVersion != candidate.version+1 {
		t.Fatalf("application login abandonment state=%q candidate_version=%d",
			abandonedState, abandonedCandidateVersion)
	}
	var completedVersion, completedEpoch int64
	var completedState string
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT session_version,session_state,session_epoch
FROM complete_abandoned_sandbox_session($1,$2,$3,$4)`,
		sessionID, abandonedVersion, abandonedEpoch, seed.actorID,
	).Scan(&completedVersion, &completedState, &completedEpoch); err != nil {
		t.Fatalf("application login complete abandonment: %v", err)
	}
	if completedState != "terminated" || completedVersion != abandonedVersion+1 || completedEpoch != abandonedEpoch {
		t.Fatalf("application login completion version=%d state=%q epoch=%d",
			completedVersion, completedState, completedEpoch)
	}

	freezeCandidate := createSandboxCandidateCanary(
		t, ctx, adminDatabase, seed, "gc-application-login-freeze",
	)
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT candidate_version,session_epoch,writer_lease_epoch,writer_lease_expires_at
FROM acquire_candidate_workspace_lease($1,$2,$3,300)`,
		freezeCandidate.id, freezeCandidate.version, seed.actorID,
	).Scan(
		&freezeCandidate.version, &freezeCandidate.sessionEpoch,
		&freezeCandidate.writerLeaseEpoch, &leaseExpiresAt,
	); err != nil {
		t.Fatalf("application login acquire freeze Candidate lease: %v", err)
	}
	freezeCheckpoint := createSandboxCheckpointCanary(
		t, ctx, applicationDatabase, freezeCandidate.id, seed.actorID, "application login freeze",
	)
	var frozenVersion int64
	if err := applicationDatabase.QueryRowContext(ctx, `
SELECT freeze_candidate_workspace($1,$2,$3,$4,$5,$6,$7)`,
		freezeCandidate.id, freezeCandidate.version, freezeCandidate.sessionEpoch,
		freezeCandidate.writerLeaseEpoch, seed.actorID, freezeCheckpoint,
		"application login freeze",
	).Scan(&frozenVersion); err != nil {
		t.Fatalf("application login freeze Candidate: %v", err)
	}
	if frozenVersion != freezeCandidate.version+1 {
		t.Fatalf("application login frozen version=%d", frozenVersion)
	}
}

func assertExactTreeGCLiveDriftGates(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	roleLock *sql.Conn,
	schema string,
) {
	t.Helper()
	assertReadiness := func(wantReady bool, reasonFragment string) {
		var ready bool
		var reason string
		if err := database.QueryRowContext(ctx, `
SELECT ready,reason FROM repository_exact_tree_literal_index_gc_readiness()`,
		).Scan(&ready, &reason); err != nil {
			t.Fatal(err)
		}
		if ready != wantReady || (!wantReady && !strings.Contains(reason, reasonFragment)) {
			t.Fatalf("drift readiness=%v reason=%q, want ready=%v reason containing %q",
				ready, reason, wantReady, reasonFragment)
		}
	}
	assertReadiness(true, "")
	if _, err := database.ExecContext(ctx, `
ALTER TABLE schema_migrations RENAME TO schema_migrations_missing_canary`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "read-only schema_migrations")
	if _, err := database.ExecContext(ctx, `
ALTER TABLE schema_migrations_missing_canary RENAME TO schema_migrations`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	if _, err := database.ExecContext(ctx, `
GRANT UPDATE (email) ON users TO worksflow_application`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "column privileges")
	if _, err := database.ExecContext(ctx, `
REVOKE UPDATE (email) ON users FROM worksflow_application`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	elevatedOwner := "worksflow_gc_elevated_owner_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := roleLock.ExecContext(ctx, "CREATE ROLE "+elevatedOwner+" LOGIN CREATEDB"); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx,
		"GRANT worksflow_migration_owner TO "+elevatedOwner,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
ALTER FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation()
OWNER TO `+elevatedOwner); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "owned exactly")
	if _, err := database.ExecContext(ctx, `
ALTER FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation()
OWNER TO worksflow_migration_owner`); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx,
		"REVOKE worksflow_migration_owner FROM "+elevatedOwner,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := roleLock.ExecContext(ctx, "DROP ROLE "+elevatedOwner); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	if _, err := database.ExecContext(ctx, `
DROP INDEX repository_exact_tree_literal_index_gc_receipt_run_idx;
CREATE INDEX repository_exact_tree_literal_index_gc_receipt_run_idx
  ON repository_exact_tree_literal_index_gc_receipts
  (capability_id, run_id, outcome);
ALTER INDEX repository_exact_tree_literal_index_gc_receipt_run_idx
  OWNER TO worksflow_migration_owner`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "pg_index/pg_get_indexdef")
	if _, err := database.ExecContext(ctx, `
DROP INDEX repository_exact_tree_literal_index_gc_receipt_run_idx;
CREATE INDEX repository_exact_tree_literal_index_gc_receipt_run_idx
  ON repository_exact_tree_literal_index_gc_receipts
  (run_id, outcome, capability_id);
ALTER INDEX repository_exact_tree_literal_index_gc_receipt_run_idx
  OWNER TO worksflow_migration_owner`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	if _, err := database.ExecContext(ctx, `
DROP TRIGGER candidate_workspace_exact_tree_literal_index_reference_lock
  ON candidate_workspaces`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "trigger wiring")
	if _, err := database.ExecContext(ctx, `
CREATE TRIGGER candidate_workspace_exact_tree_literal_index_reference_lock
BEFORE INSERT OR UPDATE ON candidate_workspaces
FOR EACH ROW EXECUTE FUNCTION lock_candidate_exact_tree_literal_index_reference()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")
	if _, err := database.ExecContext(ctx, `
DROP TRIGGER candidate_workspace_exact_tree_literal_index_reference_lock
  ON candidate_workspaces;
CREATE TRIGGER candidate_workspace_exact_tree_literal_index_reference_lock
BEFORE INSERT OR UPDATE ON candidate_workspaces
FOR EACH ROW WHEN (false)
EXECUTE FUNCTION lock_candidate_exact_tree_literal_index_reference()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "trigger wiring")
	if _, err := database.ExecContext(ctx, `
DROP TRIGGER candidate_workspace_exact_tree_literal_index_reference_lock
  ON candidate_workspaces;
CREATE TRIGGER candidate_workspace_exact_tree_literal_index_reference_lock
BEFORE INSERT OR UPDATE ON candidate_workspaces
FOR EACH ROW EXECUTE FUNCTION lock_candidate_exact_tree_literal_index_reference()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	if _, err := database.ExecContext(ctx, `
DROP TRIGGER repository_exact_tree_literal_index_manifest_publish_guard
  ON repository_exact_tree_literal_index_manifests;
CREATE TRIGGER repository_exact_tree_literal_index_manifest_publish_guard
BEFORE UPDATE OF status ON repository_exact_tree_literal_index_manifests
FOR EACH ROW EXECUTE FUNCTION publish_repository_exact_tree_literal_index_manifest()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "trigger wiring")
	if _, err := database.ExecContext(ctx, `
DROP TRIGGER repository_exact_tree_literal_index_manifest_publish_guard
  ON repository_exact_tree_literal_index_manifests;
CREATE TRIGGER repository_exact_tree_literal_index_manifest_publish_guard
BEFORE UPDATE ON repository_exact_tree_literal_index_manifests
FOR EACH ROW EXECUTE FUNCTION publish_repository_exact_tree_literal_index_manifest()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	if _, err := database.ExecContext(ctx, `
DROP TRIGGER repository_exact_tree_literal_index_member_immutable
  ON repository_exact_tree_literal_index_members;
CREATE TRIGGER repository_exact_tree_literal_index_member_immutable
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_members
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "trigger wiring")
	if _, err := database.ExecContext(ctx, `
DROP TRIGGER repository_exact_tree_literal_index_member_immutable
  ON repository_exact_tree_literal_index_members;
CREATE TRIGGER repository_exact_tree_literal_index_member_immutable
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_members
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_member_mutation()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	if _, err := database.ExecContext(ctx, `
ALTER TABLE repository_exact_tree_literal_index_manifests
DISABLE TRIGGER repository_exact_tree_literal_index_manifest_no_delete`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "trigger wiring")
	if _, err := database.ExecContext(ctx, `
ALTER TABLE repository_exact_tree_literal_index_manifests
ENABLE TRIGGER repository_exact_tree_literal_index_manifest_no_delete`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	if _, err := database.ExecContext(ctx, `
CREATE TRIGGER repository_exact_tree_literal_index_gc_extra_canary
BEFORE TRUNCATE ON repository_exact_tree_literal_index_gc_tree_delete_auth
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation()`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(false, "trigger wiring")
	if _, err := database.ExecContext(ctx, `
DROP TRIGGER repository_exact_tree_literal_index_gc_extra_canary
  ON repository_exact_tree_literal_index_gc_tree_delete_auth`); err != nil {
		t.Fatal(err)
	}
	assertReadiness(true, "")

	var exactOwners int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid=procedure.pronamespace
WHERE namespace.nspname=$1
  AND procedure.prosecdef
  AND procedure.proowner='worksflow_migration_owner'::regrole`, schema,
	).Scan(&exactOwners); err != nil {
		t.Fatal(err)
	}
	if exactOwners != 19 {
		t.Fatalf("trusted schema exact-owner SECURITY DEFINER count=%d, want 19", exactOwners)
	}
}

func assertExactTreeGCTerminalOutcomesAndRollback(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	actorID uuid.UUID,
	now time.Time,
) {
	t.Helper()
	createProject := func(label string) (uuid.UUID, []gcManifestFixture) {
		projectID := uuid.New()
		if _, err := database.ExecContext(ctx, `
INSERT INTO projects (id,name,created_by) VALUES ($1,$2,$3)`,
			projectID, "Exact tree GC "+label, actorID); err != nil {
			t.Fatal(err)
		}
		trees := make([]gcManifestFixture, 9)
		for index := range trees {
			trees[index] = seedExactTreeGCManifest(
				t, ctx, database, projectID, fmt.Sprintf("%s-%02d", label, index),
				now.Add(-9*24*time.Hour-time.Duration(index)*time.Hour),
				fmt.Sprintf("%s terminal body %02d", label, index),
			)
		}
		return projectID, trees
	}
	findProjectCapability := func(capabilities []gcCapability, projectID uuid.UUID) gcCapability {
		for _, capability := range capabilities {
			if capability.projectID == projectID {
				return capability
			}
		}
		t.Fatalf("no capability for project %s in %#v", projectID, capabilities)
		return gcCapability{}
	}

	protectedProject, protectedTrees := createProject("protected-after-plan")
	protectedRun := uuid.New()
	protectedCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, protectedRun, 7*24*time.Hour, 8, 100, time.Minute),
		protectedProject,
	)
	insertRawExactTreeGCCandidate(
		t, ctx, database, actorID, protectedProject, protectedTrees[8].treeHash, "active",
	)
	protectedReceipt := executeExactTreeGC(t, ctx, database, protectedCapability.id)
	if protectedReceipt.outcome != "protected" {
		t.Fatalf("post-plan Candidate outcome=%#v", protectedReceipt)
	}
	if summary := inspectExactTreeGC(t, ctx, database, protectedRun); summary.status != "completed" || summary.protected != 1 || summary.pending != 0 {
		t.Fatalf("protected terminal summary=%#v", summary)
	}

	expiredProject, expiredTrees := createProject("expired-capability")
	expiredRun := uuid.New()
	expiredCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, expiredRun, 7*24*time.Hour, 8, 100, time.Millisecond),
		expiredProject,
	)
	time.Sleep(5 * time.Millisecond)
	expiredReceipt := executeExactTreeGC(t, ctx, database, expiredCapability.id)
	if expiredReceipt.outcome != "expired" {
		t.Fatalf("expired capability outcome=%#v", expiredReceipt)
	}
	if summary := inspectExactTreeGC(t, ctx, database, expiredRun); summary.status != "completed" || summary.expired != 1 || summary.pending != 0 {
		t.Fatalf("expired terminal summary=%#v", summary)
	}
	insertRawExactTreeGCCandidate(
		t, ctx, database, actorID, expiredProject, expiredTrees[8].treeHash, "abandoned",
	)

	staleProject, _ := createProject("stale-capability")
	firstRun, secondRun := uuid.New(), uuid.New()
	firstCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, firstRun, 7*24*time.Hour, 8, 100, time.Minute),
		staleProject,
	)
	secondCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, secondRun, 7*24*time.Hour, 8, 100, time.Minute),
		staleProject,
	)
	if receipt := executeExactTreeGC(t, ctx, database, firstCapability.id); receipt.outcome != "deleted" {
		t.Fatalf("first duplicate plan outcome=%#v", receipt)
	}
	if receipt := executeExactTreeGC(t, ctx, database, secondCapability.id); receipt.outcome != "stale" {
		t.Fatalf("second duplicate plan outcome=%#v", receipt)
	}
	if summary := inspectExactTreeGC(t, ctx, database, secondRun); summary.status != "completed" || summary.stale != 1 || summary.pending != 0 {
		t.Fatalf("stale terminal summary=%#v", summary)
	}

	rollbackProject, rollbackTrees := createProject("rollback-injection")
	rollbackRun := uuid.New()
	rollbackCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, rollbackRun, 7*24*time.Hour, 8, 100, time.Minute),
		rollbackProject,
	)
	if _, err := database.ExecContext(ctx, `
CREATE FUNCTION fail_exact_tree_gc_tombstone_canary()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'injected tombstone failure' USING ERRCODE='55000';
END
$$;
CREATE TRIGGER fail_exact_tree_gc_tombstone_canary
BEFORE INSERT ON repository_exact_tree_literal_index_gc_tombstones
FOR EACH ROW EXECUTE FUNCTION fail_exact_tree_gc_tombstone_canary()`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT * FROM execute_repository_exact_tree_literal_index_gc($1)`, rollbackCapability.id); err == nil ||
		!strings.Contains(err.Error(), "injected tombstone failure") {
		t.Fatalf("rollback injection did not fail at tombstone: %v", err)
	}
	var manifests, members, receipts int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM repository_exact_tree_literal_index_manifests
   WHERE project_id=$1 AND tree_hash=$2),
  (SELECT count(*) FROM repository_exact_tree_literal_index_members
   WHERE project_id=$1 AND tree_hash=$2),
  (SELECT count(*) FROM repository_exact_tree_literal_index_gc_receipts
   WHERE capability_id=$3)`, rollbackProject, rollbackTrees[8].treeHash,
		rollbackCapability.id).Scan(&manifests, &members, &receipts); err != nil {
		t.Fatal(err)
	}
	if manifests != 1 || members != 1 || receipts != 0 {
		t.Fatalf("injected rollback manifest=%d members=%d receipts=%d", manifests, members, receipts)
	}
	if _, err := database.ExecContext(ctx, `
DROP TRIGGER fail_exact_tree_gc_tombstone_canary
  ON repository_exact_tree_literal_index_gc_tombstones;
DROP FUNCTION fail_exact_tree_gc_tombstone_canary()`); err != nil {
		t.Fatal(err)
	}
	if receipt := executeExactTreeGC(t, ctx, database, rollbackCapability.id); receipt.outcome != "deleted" {
		t.Fatalf("execute after rollback injection=%#v", receipt)
	}

	lockProject, lockTrees := createProject("exclusive-lock")
	lockRun := uuid.New()
	lockCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, lockRun, 7*24*time.Hour, 8, 100, time.Minute),
		lockProject,
	)
	assertExactTreeGCWaitsForSharedTreeLock(
		t, ctx, database, lockProject, lockTrees[8].treeHash, lockCapability.id,
	)

	renewProject, renewTrees := createProject("renew-commit-race")
	renewRun := uuid.New()
	renewCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, renewRun, 7*24*time.Hour, 8, 100, time.Minute),
		renewProject,
	)
	assertExactTreeGCRenewCommitProtectsPublication(
		t, ctx, database, renewProject, renewTrees[8].treeHash, renewCapability.id,
	)

	downProject, _ := createProject("down-execute-race")
	downRun := uuid.New()
	downCapability := findProjectCapability(
		planExactTreeGC(t, ctx, database, downRun, 7*24*time.Hour, 8, 100, time.Minute),
		downProject,
	)
	assertExactTreeGCDownWaitsForUncommittedExecution(
		t, ctx, database, downCapability.id,
	)
}

func assertExactTreeGCWaitsForSharedTreeLock(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	capabilityID uuid.UUID,
) {
	t.Helper()
	sharedConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sharedConnection.Close()
	sharedTransaction, err := sharedConnection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sharedTransaction.Rollback()
	if _, err := sharedTransaction.ExecContext(ctx, `
SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`,
		projectID.String()+"|"+treeHash); err != nil {
		t.Fatal(err)
	}

	executeConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer executeConnection.Close()
	var backendPID int
	if err := executeConnection.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		value string
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		var value string
		err := executeConnection.QueryRowContext(ctx, `
SELECT outcome FROM execute_repository_exact_tree_literal_index_gc($1)`, capabilityID).Scan(&value)
		finished <- outcome{value: value, err: err}
	}()

	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_locks
  WHERE pid=$1 AND locktype='advisory' AND mode='ExclusiveLock' AND NOT granted
)`, backendPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		select {
		case result := <-finished:
			t.Fatalf("GC did not wait for shared tree lock: outcome=%q err=%v", result.value, result.err)
		default:
			t.Fatal("GC wait did not expose the expected exclusive advisory lock")
		}
	}
	if err := sharedTransaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result.err != nil || result.value != "deleted" {
			t.Fatalf("GC after shared lock release outcome=%q err=%v", result.value, result.err)
		}
	case <-ctx.Done():
		t.Fatalf("GC did not finish after shared lock release: %v", ctx.Err())
	}
}

func assertExactTreeGCRenewCommitProtectsPublication(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	treeHash string,
	capabilityID uuid.UUID,
) {
	t.Helper()
	ownerToken := uuid.New()
	oldRenewedAt := time.Now().UTC().Add(-100 * time.Millisecond)
	oldExpiry := time.Now().UTC().Add(500 * time.Millisecond)
	if _, err := database.ExecContext(ctx, `
INSERT INTO repository_exact_tree_literal_index_build_claims (
  project_id,tree_hash,owner_token,attempt,reserved_source_bytes,
  acquired_at,renewed_at,lease_expires_at
) VALUES ($1,$2,$3,1,1,$4,$5,$6)`,
		projectID, treeHash, ownerToken, oldRenewedAt.Add(-time.Second),
		oldRenewedAt, oldExpiry,
	); err != nil {
		t.Fatal(err)
	}

	renewConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer renewConnection.Close()
	renewTransaction, err := renewConnection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer renewTransaction.Rollback()
	var renewed bool
	var renewedExpiry time.Time
	if err := renewTransaction.QueryRowContext(ctx, `
SELECT renewed,current_lease_expires_at
FROM renew_repository_exact_tree_literal_index_build_claim($1,$2,$3,1,5000)`,
		projectID, treeHash, ownerToken,
	).Scan(&renewed, &renewedExpiry); err != nil {
		t.Fatal(err)
	}
	if !renewed || !renewedExpiry.After(oldExpiry) {
		t.Fatalf("claim renewal renewed=%v old=%v new=%v", renewed, oldExpiry, renewedExpiry)
	}
	if wait := time.Until(oldExpiry.Add(50 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}

	executeConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer executeConnection.Close()
	var executePID int
	if err := executeConnection.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&executePID); err != nil {
		t.Fatal(err)
	}
	type executeResult struct {
		outcome string
		err     error
	}
	finished := make(chan executeResult, 1)
	go func() {
		var outcome string
		err := executeConnection.QueryRowContext(ctx, `
SELECT outcome FROM execute_repository_exact_tree_literal_index_gc($1)`,
			capabilityID,
		).Scan(&outcome)
		finished <- executeResult{outcome: outcome, err: err}
	}()

	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND NOT granted)`,
			executePID,
		).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		select {
		case result := <-finished:
			t.Fatalf("execute crossed uncommitted renewal outcome=%q err=%v", result.outcome, result.err)
		default:
			t.Fatal("execute did not expose its renewal row-lock wait")
		}
	}
	if err := renewTransaction.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result.err != nil || result.outcome != "protected" {
			t.Fatalf("execute after renewal commit outcome=%q err=%v", result.outcome, result.err)
		}
	case <-ctx.Done():
		t.Fatalf("execute did not finish after renewal commit: %v", ctx.Err())
	}

	var manifestCount, claimCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM repository_exact_tree_literal_index_manifests
   WHERE project_id=$1 AND tree_hash=$2),
  (SELECT count(*) FROM repository_exact_tree_literal_index_build_claims
   WHERE project_id=$1 AND tree_hash=$2 AND lease_expires_at>clock_timestamp())`,
		projectID, treeHash,
	).Scan(&manifestCount, &claimCount); err != nil {
		t.Fatal(err)
	}
	if manifestCount != 1 || claimCount != 1 {
		t.Fatalf("renewal protection manifest=%d live_claim=%d", manifestCount, claimCount)
	}
}

func assertExactTreeGCDownWaitsForUncommittedExecution(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	capabilityID uuid.UUID,
) {
	t.Helper()
	executeConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer executeConnection.Close()
	executeTransaction, err := executeConnection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer executeTransaction.Rollback()
	var outcome string
	if err := executeTransaction.QueryRowContext(ctx, `
SELECT outcome FROM execute_repository_exact_tree_literal_index_gc($1)`,
		capabilityID,
	).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "deleted" {
		t.Fatalf("uncommitted execute outcome=%q", outcome)
	}

	downSQL, err := files.ReadFile(repositoryExactTreeLiteralIndexGCMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downConnection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer downConnection.Close()
	var downPID int
	if err := downConnection.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&downPID); err != nil {
		t.Fatal(err)
	}
	downFinished := make(chan error, 1)
	go func() {
		_, err := downConnection.ExecContext(ctx, string(downSQL))
		downFinished <- err
	}()

	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM pg_locks
  WHERE pid=$1
    AND relation='repository_exact_tree_literal_index_gc_runs'::regclass
    AND mode='AccessExclusiveLock'
    AND NOT granted
)`, downPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		select {
		case downErr := <-downFinished:
			t.Fatalf("down crossed uncommitted execute: %v", downErr)
		default:
			t.Fatal("down did not expose the expected ACCESS EXCLUSIVE wait")
		}
	}
	if err := executeTransaction.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case downErr := <-downFinished:
		if downErr == nil || !strings.Contains(downErr.Error(), "audit/control rows") {
			t.Fatalf("down after execute commit did not fail closed: %v", downErr)
		}
	case <-ctx.Done():
		t.Fatalf("down did not finish after execute commit: %v", ctx.Err())
	}
	var executeExists bool
	if err := database.QueryRowContext(ctx, `
SELECT to_regprocedure('execute_repository_exact_tree_literal_index_gc(uuid)') IS NOT NULL`,
	).Scan(&executeExists); err != nil {
		t.Fatal(err)
	}
	if !executeExists {
		t.Fatal("failed downgrade removed execute function")
	}
}
