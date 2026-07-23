package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/domain"
)

const qualificationReleaseV1Migration = "000084_workflow_execution_profile_v3_qualified_release.up.sql"

func TestQualificationReleaseV1DeclaresClosedAuthority(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(qualificationReleaseV1Migration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000084_workflow_execution_profile_v3_qualified_release.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"000084 cannot reinterpret persisted draft workflow-engine/v3 authority",
		"qualification_release_v1_controller_bootstraps",
		"qualification_release_v1_identity_reservations",
		"qualification_release_v1_authorizations",
		"qualification_release_v1_controller_bindings",
		"qualification_release_v1_lease_claims",
		"qualification_release_v1_results",
		"qualification_release_v1_transaction_grants",
		"worksflow.qualification-release.controller-bootstrap/v1",
		"worksflow.qualification-release.request/v1",
		"worksflow.qualification-release.equivalence/v1",
		"worksflow.qualification-release.authorization/v1",
		"worksflow.qualification-release.lease-claim/v1",
		"worksflow.qualification-release.controller-binding/v1",
		"worksflow.qualification-release.result/v1",
		"worksflow.qualification-release.failure/v1",
		"'pre_submit_cancelled'",
		"'release_cancelled_before_submission'",
		"bootstrap_qualification_release_controller_v1",
		"authorize_qualification_release_v1",
		"claim_qualification_release_publish_v1",
		"renew_qualification_release_publish_lease_v1",
		"start_qualification_release_controller_v1",
		"record_qualification_release_result_v1",
		"apply_qualification_release_result_v1",
		"record_qualification_release_failure_v1",
		"apply_qualification_release_failure_v1",
		"'terminal_fail'",
		"pg_catalog.pg_current_xact_id()::text",
		"CREATE CONSTRAINT TRIGGER qualification_release_grant_empty_closure",
		"CREATE CONSTRAINT TRIGGER qualification_release_lease_claim_exact_closure",
		"worksflow_qualification_release_operator",
		"release_delivery_canonical_json(",
		"release_delivery_embedded_hash_is_exact(",
		"release_delivery_rfc3339_microsecond(",
		"session_user, 'worksflow_migration_owner', 'MEMBER'",
		"000084 granted schema USAGE to worksflow_qualification_release_operator",
		"workflow_execution_profile_v3_definition_is_database_admissible",
		"v_completion.publish_node_key",
		"ARRAY['viewer','commenter','editor','admin','owner']",
		"CREATE OR REPLACE FUNCTION guard_workflow_execution_profile_v3_run()",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Qualification Release migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"current_setting('qualification_release",
		"GRANT SELECT ON TABLE",
		"ON DELETE CASCADE",
		"publish_node_key = 'publish'",
		"TO worksflow_schema_migrator",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Qualification Release migration unexpectedly contains %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "cannot roll back Qualification Release v1 after durable authority exists") {
		t.Fatal("Qualification Release rollback has no durable-authority guard")
	}
	if !strings.Contains(string(down),
		"REVOKE USAGE ON SCHEMA %I FROM worksflow_qualification_release_operator",
	) {
		t.Fatal("Qualification Release rollback does not restore operator schema USAGE")
	}
	bootstrapStart := strings.Index(text, "CREATE FUNCTION bootstrap_qualification_release_controller_v1(")
	if bootstrapStart < 0 {
		t.Fatal("Controller bootstrap function is missing")
	}
	bootstrapFence := strings.Index(text[bootstrapStart:], "pg_advisory_xact_lock_shared")
	bootstrapPrimary := strings.Index(text[bootstrapStart:], "qualification_release_v1_require_primary")
	bootstrapRelation := strings.Index(text[bootstrapStart:], "LOCK TABLE qualification_release_v1_controller_bootstraps")
	if bootstrapFence < 0 || bootstrapPrimary < 0 || bootstrapRelation < 0 ||
		bootstrapFence > bootstrapPrimary || bootstrapFence > bootstrapRelation {
		t.Fatal("Controller bootstrap does not take the shared rollout fence before relation access")
	}
	if !strings.Contains(string(down), "Restore migration 82's deny-until-qualified-release boundary") {
		t.Fatal("Qualification Release rollback does not restore migration 82's completed-state guard")
	}
}

func TestQualificationReleaseV1BootstrapAndEmptyRollbackPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	ensureQualificationReleaseHardenedRoles(t, ctx, base)

	t.Run("empty rollback", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_release_empty_")
		applyQualificationPromotionV2Prefix(t, ctx, database)
		applyQualificationHandoffV1(t, ctx, database)
		applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
		beforeUpstreamACL := qualificationReleaseUpstreamHelperACL(t, ctx, database)
		beforeOperatorUsage := qualificationReleaseOperatorSchemaUsage(t, ctx, database)
		applyQualificationReleaseV1(t, ctx, database)
		assertQualificationReleaseCatalog(t, ctx, database)
		if !qualificationReleaseOperatorSchemaUsage(t, ctx, database) ||
			!qualificationReleaseOperatorSchemaUsageMarked(t, ctx, database) {
			t.Fatal("Qualification Release did not mark its new operator schema USAGE")
		}
		afterUpstreamACL := qualificationReleaseUpstreamHelperACL(t, ctx, database)
		if afterUpstreamACL != beforeUpstreamACL {
			t.Fatalf("Qualification Release upstream ACL before=%+v after=%+v", beforeUpstreamACL, afterUpstreamACL)
		}
		down, _ := files.ReadFile("000084_workflow_execution_profile_v3_qualified_release.down.sql")
		if _, err := database.ExecContext(ctx, string(down)); err != nil {
			t.Fatalf("empty Qualification Release rollback: %v", err)
		}
		afterDownstreamACL := qualificationReleaseUpstreamHelperACL(t, ctx, database)
		if afterDownstreamACL != beforeUpstreamACL {
			t.Fatalf("Qualification Release rollback upstream ACL before=%+v after=%+v", beforeUpstreamACL, afterDownstreamACL)
		}
		if afterUsage := qualificationReleaseOperatorSchemaUsage(t, ctx, database); afterUsage != beforeOperatorUsage {
			t.Fatalf("Qualification Release rollback operator schema USAGE before=%t after=%t",
				beforeOperatorUsage, afterUsage)
		}
		assertQualificationReleaseV3GuardSearchPath(t, ctx, database)
	})

	t.Run("empty rollback preserves preexisting operator schema usage", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_release_preexisting_usage_")
		applyQualificationPromotionV2Prefix(t, ctx, database)
		applyQualificationHandoffV1(t, ctx, database)
		applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
		if _, err := database.ExecContext(ctx, `
DO $grant$
BEGIN
  EXECUTE pg_catalog.format(
    'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_release_operator',
    pg_catalog.current_schema()
  );
END;
$grant$`); err != nil {
			t.Fatalf("seed preexisting operator schema USAGE: %v", err)
		}
		applyQualificationReleaseV1(t, ctx, database)
		if !qualificationReleaseOperatorSchemaUsage(t, ctx, database) ||
			qualificationReleaseOperatorSchemaUsageMarked(t, ctx, database) {
			t.Fatal("Qualification Release claimed provenance for preexisting schema USAGE")
		}
		down, _ := files.ReadFile("000084_workflow_execution_profile_v3_qualified_release.down.sql")
		if _, err := database.ExecContext(ctx, string(down)); err != nil {
			t.Fatalf("rollback with preexisting operator schema USAGE: %v", err)
		}
		if !qualificationReleaseOperatorSchemaUsage(t, ctx, database) {
			t.Fatal("Qualification Release rollback revoked preexisting operator schema USAGE")
		}
	})

	t.Run("bootstrap singleton", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_release_bootstrap_")
		applyQualificationPromotionV2Prefix(t, ctx, database)
		applyQualificationHandoffV1(t, ctx, database)
		applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
		applyQualificationReleaseV1(t, ctx, database)
		bootstrapID := uuid.New()
		trust := "sha256:" + strings.Repeat("7", 64)
		call := func(id uuid.UUID, controller string) map[string]any {
			var raw []byte
			if err := database.QueryRowContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,$2,'2026.07.20','worksflow.release-delivery/v3',$3
) AS value`, id, controller, trust).Scan(&raw); err != nil {
				t.Fatalf("bootstrap Qualification Release Controller: %v", err)
			}
			var bundle map[string]any
			if err := json.Unmarshal(raw, &bundle); err != nil {
				t.Fatal(err)
			}
			return bundle
		}
		if fresh := call(bootstrapID, "qualified-controller"); fresh["idempotent"] != false {
			t.Fatalf("fresh bootstrap idempotent=%v", fresh["idempotent"])
		}
		if replay := call(bootstrapID, "qualified-controller"); replay["idempotent"] != true {
			t.Fatalf("replayed bootstrap idempotent=%v", replay["idempotent"])
		}
		if _, err := database.ExecContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,'other-controller','2026.07.20','worksflow.release-delivery/v3',$2
) AS value`, uuid.New(), trust); err == nil || !strings.Contains(err.Error(), "WQR02") {
			t.Fatalf("second Controller bootstrap error=%v, want WQR02", err)
		}
		if _, err := database.ExecContext(ctx, `
UPDATE qualification_release_v1_controller_bootstraps
SET controller_version='tampered'`); err == nil || !strings.Contains(err.Error(), "WQR02") {
			t.Fatalf("bootstrap tamper error=%v, want WQR02", err)
		}
		down, _ := files.ReadFile("000084_workflow_execution_profile_v3_qualified_release.down.sql")
		if _, err := database.ExecContext(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "55000") {
			t.Fatalf("nonempty Qualification Release rollback=%v, want 55000", err)
		}
		assertQualificationReleasePrivileges(t, ctx, database)
	})

	t.Run("bootstrap cannot cross down fence", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_release_fence_")
		applyQualificationPromotionV2Prefix(t, ctx, database)
		applyQualificationHandoffV1(t, ctx, database)
		applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
		applyQualificationReleaseV1(t, ctx, database)
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1',0)
)`); err != nil {
			_ = transaction.Rollback()
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			_, callErr := database.ExecContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,'fenced-controller','2026.07.20','worksflow.release-delivery/v3',$2
) AS value`, uuid.New(), qualificationReleaseV1TestHash("7"))
			result <- callErr
		}()
		select {
		case callErr := <-result:
			_ = transaction.Rollback()
			t.Fatalf("Controller bootstrap crossed exclusive down fence: %v", callErr)
		case <-time.After(200 * time.Millisecond):
		}
		var durable int
		if err := transaction.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_release_v1_controller_bootstraps`,
		).Scan(&durable); err != nil || durable != 0 {
			_ = transaction.Rollback()
			t.Fatalf("down-fence zero-authority observation=%d error=%v", durable, err)
		}
		if err := transaction.Rollback(); err != nil {
			t.Fatal(err)
		}
		select {
		case callErr := <-result:
			if callErr != nil {
				t.Fatalf("Controller bootstrap after down fence release: %v", callErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Controller bootstrap remained blocked after down fence release")
		}
	})

	t.Run("old v3 hash is rejected", func(t *testing.T) {
		database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_release_old_hash_")
		applyQualificationPromotionV2Prefix(t, ctx, database)
		applyQualificationHandoffV1(t, ctx, database)
		applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
		wia := seedWorkflowInputCanary(t, ctx, database)
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback()
		if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
			t.Fatal(err)
		}
		result, err := transaction.ExecContext(ctx, `
UPDATE workflow_runs
SET context=jsonb_set(
  context,'{supersededProfileHash}',to_jsonb($2::text),true
)
WHERE id=$1`, wia.runID,
			"aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef")
		if err != nil {
			t.Fatalf("seed old profile-v3 hash: %v", err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			t.Fatalf("seed old profile-v3 hash affected=%d error=%v", affected, err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		up, err := files.ReadFile(qualificationReleaseV1Migration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(up)); err == nil ||
			!strings.Contains(err.Error(), "WQR02") {
			t.Fatalf("old profile-v3 hash migration error=%v, want WQR02", err)
		}
		var installed bool
		if err := database.QueryRowContext(ctx, `
SELECT to_regclass(current_schema()||'.qualification_release_v1_authorizations') IS NOT NULL`,
		).Scan(&installed); err != nil || installed {
			t.Fatalf("rejected old-hash migration installed authority=%t error=%v", installed, err)
		}
	})
}

func TestQualificationReleaseV1CanonicalMigratorPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	ensureQualificationReleaseHardenedRoles(t, ctx, base)

	database := qualificationPlanMigrationDatabase(
		t, ctx, base, dsn, "qualification_release_canonical_migrator_",
	)
	var schema string
	if err := database.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}

	migratorRole := "worksflow_test_migrator_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	migratorPassword := "qualification-release-" + uuid.NewString()
	if _, err := base.ExecContext(ctx, fmt.Sprintf(
		"CREATE ROLE %s LOGIN INHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '%s'",
		migratorRole, migratorPassword,
	)); err != nil {
		t.Fatalf("create canonical migrator login: %v", err)
	}
	if _, err := base.ExecContext(ctx,
		"GRANT worksflow_migration_owner TO "+migratorRole,
	); err != nil {
		t.Fatalf("attach canonical migration-owner group: %v", err)
	}
	var migrator *sql.DB
	var registeredDSN string
	t.Cleanup(func() {
		if migrator != nil {
			_ = migrator.Close()
		}
		if registeredDSN != "" {
			stdlib.UnregisterConnConfig(registeredDSN)
		}
		_, _ = base.ExecContext(context.Background(), `
SELECT pg_catalog.pg_terminate_backend(pid)
FROM pg_catalog.pg_stat_activity
WHERE usename=$1 AND pid<>pg_catalog.pg_backend_pid()`, migratorRole)
		_, _ = base.ExecContext(
			context.Background(), "DROP ROLE IF EXISTS "+migratorRole,
		)
	})

	applyQualificationReleaseOwnerPrefixThrough83(t, ctx, database)
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.User = migratorRole
	config.Password = migratorPassword
	config.RuntimeParams["search_path"] = schema
	registeredDSN = stdlib.RegisterConnConfig(config)
	migrator, err = sql.Open("pgx", registeredDSN)
	if err != nil {
		t.Fatal(err)
	}
	migrator.SetMaxOpenConns(1)
	connection, err := migrator.Conn(ctx)
	if err != nil {
		t.Fatalf("connect as canonical migrator: %v", err)
	}
	defer connection.Close()

	var currentUser, sessionUser, roleSetting string
	var roleNameExact, inheritedOwner bool
	var superuser, bypassRLS, createDB, createRole, replication bool
	if err := connection.QueryRowContext(ctx, `
SELECT current_user,session_user,pg_catalog.current_setting('role'),
  role.rolname=$1,
  role.rolsuper,role.rolbypassrls,role.rolcreatedb,role.rolcreaterole,
  role.rolreplication,
  pg_catalog.pg_has_role(current_user,'worksflow_migration_owner','MEMBER')
FROM pg_catalog.pg_roles AS role
WHERE role.rolname=current_user`, migratorRole).Scan(
		&currentUser, &sessionUser, &roleSetting, &roleNameExact,
		&superuser, &bypassRLS, &createDB, &createRole, &replication,
		&inheritedOwner,
	); err != nil {
		t.Fatal(err)
	}
	if currentUser != sessionUser || roleSetting != "none" || !roleNameExact ||
		superuser || bypassRLS || createDB || createRole || replication || !inheritedOwner {
		t.Fatalf("canonical migrator posture current/session/role=%q/%q/%q name/member=%t/%t elevated=%t/%t/%t/%t/%t",
			currentUser, sessionUser, roleSetting, roleNameExact, inheritedOwner,
			superuser, bypassRLS, createDB, createRole, replication)
	}

	var legacyFunctions, legacyOwned, legacyExecute, legacyGrantOptions int
	if err := connection.QueryRowContext(ctx, `
WITH expected(signature) AS (
  VALUES
    ('release_delivery_canonical_json(jsonb)'),
    ('release_delivery_embedded_hash_is_exact(jsonb,text)'),
    ('release_delivery_rfc3339_microsecond(timestamptz)')
), helper AS (
  SELECT pg_catalog.to_regprocedure(
    pg_catalog.format('%I.%s',pg_catalog.current_schema(),signature)
  ) AS oid
  FROM expected
)
SELECT
  count(*) FILTER (WHERE helper.oid IS NOT NULL),
  count(*) FILTER (
    WHERE procedure.proowner='worksflow_migration_owner'::regrole
  ),
  count(*) FILTER (
    WHERE pg_catalog.has_function_privilege(
      current_user,helper.oid,'EXECUTE'
    )
  ),
  count(*) FILTER (
    WHERE pg_catalog.has_function_privilege(
      current_user,helper.oid,'EXECUTE WITH GRANT OPTION'
    )
  )
FROM helper
LEFT JOIN pg_catalog.pg_proc AS procedure ON procedure.oid=helper.oid`,
	).Scan(
		&legacyFunctions, &legacyOwned, &legacyExecute, &legacyGrantOptions,
	); err != nil {
		t.Fatal(err)
	}
	if legacyFunctions != 3 || legacyOwned != 0 || legacyExecute != 3 || legacyGrantOptions != 0 {
		t.Fatalf("legacy Release helpers functions/migration-owned/execute/grant-options=%d/%d/%d/%d, want 3/0/3/0",
			legacyFunctions, legacyOwned, legacyExecute, legacyGrantOptions)
	}
	var provenanceRows int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM canonical_review_83_legacy_release_acl_provenance`).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 3 {
		t.Fatalf("Canonical Review 83 legacy Release ACL provenance rows=%d, want 3",
			provenanceRows)
	}

	beforeACL := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	beforeOperatorUsage := qualificationReleaseOperatorSchemaUsage(t, ctx, database)
	if err := applyFile(ctx, connection, qualificationReleaseV1Migration); err != nil {
		t.Fatalf("canonical migrator apply Qualification Release v1: %v", err)
	}
	afterACL := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	if afterACL != beforeACL {
		t.Fatalf("canonical migration changed legacy helper ACL before=%+v after=%+v",
			beforeACL, afterACL)
	}
	if !qualificationReleaseOperatorSchemaUsage(t, ctx, database) ||
		!qualificationReleaseOperatorSchemaUsageMarked(t, ctx, database) {
		t.Fatal("canonical migration did not mark its operator schema USAGE")
	}
	assertQualificationReleaseCatalog(t, ctx, database)

	down, err := files.ReadFile("000084_workflow_execution_profile_v3_qualified_release.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("canonical migrator empty rollback: %v", err)
	}
	afterDownACL := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	if afterDownACL != beforeACL {
		t.Fatalf("canonical rollback changed legacy helper ACL before=%+v after=%+v",
			beforeACL, afterDownACL)
	}
	if afterUsage := qualificationReleaseOperatorSchemaUsage(t, ctx, database); afterUsage != beforeOperatorUsage {
		t.Fatalf("canonical rollback operator schema USAGE before=%t after=%t",
			beforeOperatorUsage, afterUsage)
	}
	if _, err := connection.ExecContext(ctx, `
DELETE FROM schema_migrations WHERE version=$1`,
		strings.TrimSuffix(qualificationReleaseV1Migration, ".up.sql"),
	); err != nil {
		t.Fatalf("remove rolled-back migration record: %v", err)
	}
	if err := applyFile(ctx, connection, qualificationReleaseV1Migration); err != nil {
		t.Fatalf("canonical migrator reapply Qualification Release v1: %v", err)
	}
	bootstrapID := uuid.New()
	var raw []byte
	if err := connection.QueryRowContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,'canonical-migrator-controller','2026.07.20',
  'worksflow.release-delivery/v3',$2
) AS value`, bootstrapID, qualificationReleaseV1TestHash("7")).Scan(&raw); err != nil {
		t.Fatalf("canonical migrator bootstrap Qualification Release Controller: %v", err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle["idempotent"] != false || bundle["bootstrapId"] != bootstrapID.String() {
		t.Fatalf("canonical migrator bootstrap bundle=%v", bundle)
	}
	assertQualificationReleasePrivileges(t, ctx, database)
}

func TestCanonicalReview83LegacyReleaseACLProvenancePostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	ensureQualificationReleaseHardenedRoles(t, ctx, base)

	database := qualificationPlanMigrationDatabase(
		t, ctx, base, dsn, "canonical_review_83_release_acl_",
	)
	applyQualificationReleaseOwnerPrefixBefore(
		t, ctx, database, canonicalReviewForwardEquivalenceUp,
	)
	before := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	if before.Functions != 3 || before.MigrationOwner != 0 {
		t.Fatalf("legacy Release ACL before 83=%+v, want three helpers and no migration-owner execution", before)
	}
	if _, err := database.ExecContext(ctx, `
GRANT EXECUTE ON FUNCTION release_delivery_canonical_json(jsonb)
TO worksflow_migration_owner`); err != nil {
		t.Fatalf("seed pre-existing legacy Release helper grant: %v", err)
	}
	preExisting := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	if preExisting.MigrationOwner != 1 {
		t.Fatalf("seeded legacy Release ACL=%+v, want one migration-owner grant", preExisting)
	}
	applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
	up := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	if up.MigrationOwner != 3 || up.Application != preExisting.Application ||
		up.Public != preExisting.Public {
		t.Fatalf("legacy Release ACL after 83=%+v, baseline=%+v", up, preExisting)
	}
	var provenanceRows int
	if err := database.QueryRowContext(ctx, `
SELECT count(*)
FROM canonical_review_83_legacy_release_acl_provenance`).Scan(&provenanceRows); err != nil {
		t.Fatal(err)
	}
	if provenanceRows != 2 {
		t.Fatalf("Canonical Review 83 newly-added ACL provenance rows=%d, want 2", provenanceRows)
	}
	down, err := files.ReadFile(canonicalReviewForwardEquivalenceDown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback Canonical Review 83 legacy ACL bridge: %v", err)
	}
	after := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	if after != preExisting {
		t.Fatalf("Canonical Review 83 rollback changed pre-existing legacy ACL before=%+v after=%+v",
			preExisting, after)
	}
	var provenanceGone bool
	if err := database.QueryRowContext(ctx, `
SELECT to_regclass(
  pg_catalog.format(
    '%I.canonical_review_83_legacy_release_acl_provenance',current_schema()
  )
) IS NULL`).Scan(&provenanceGone); err != nil || !provenanceGone {
		t.Fatalf("Canonical Review 83 provenance table gone=%t error=%v", provenanceGone, err)
	}
}

type qualificationReleaseV1CallResult struct {
	bundle map[string]any
	err    error
}

type qualificationReleaseV1AuthorityFixture struct {
	bundleID         uuid.UUID
	bundleHash       string
	canonicalID      uuid.UUID
	canonicalHash    string
	previewID        uuid.UUID
	previewHash      string
	approvalID       uuid.UUID
	approvalHash     string
	releaseArtifacts []byte
}

func TestQualificationReleaseV1EndToEndPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	ensureQualificationReleaseHardenedRoles(t, ctx, base)
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_release_e2e_")
	applyQualificationPromotionV2Prefix(t, ctx, database)
	applyQualificationHandoffV1(t, ctx, database)
	applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
	applyQualificationReleaseV1(t, ctx, database)
	assertQualificationReleaseCatalog(t, ctx, database)

	wia := seedWorkflowInputCanary(t, ctx, database)
	publishNodeID := upgradeQualificationReleaseDefinitionV3(
		t, ctx, database, &wia, "production-release", "owner",
	)
	setQualificationReleaseSourceManifest(t, ctx, database, wia)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForPromotionV2(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatalf("freeze Qualification Release Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	promotionCommand := []uuid.UUID{
		uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New(),
	}
	consumeQualificationPromotionV2(t, ctx, database, promotionCommand)
	seedQualificationHandoffParentLineage(t, ctx, database, wia)
	completeQualificationHandoffV1(t, ctx, database, promotionCommand[3])
	release := seedQualificationReleaseV1Authority(t, ctx, database, wia)
	seedQualificationReleasePreviewSibling(
		t, ctx, database, wia, release, "release-preview-receipt/v1",
	)

	bootstrapID := uuid.New()
	trustDigest := "sha256:" + strings.Repeat("7", 64)
	if _, err := database.ExecContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,'qualified-controller','2026.07.20','worksflow.release-delivery/v3',$2
) AS value`, bootstrapID, trustDigest); err != nil {
		t.Fatalf("bootstrap Qualification Release Controller: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE workflow_runs SET status='running' WHERE id=$1`, wia.runID); err == nil ||
		!strings.Contains(err.Error(), "lacks authorization") {
		t.Fatalf("direct pre-authorization Publish transition error=%v", err)
	}
	adminID := seedQualificationReleaseProjectMember(t, ctx, database, wia.projectID, "admin")
	if _, err := callQualificationReleaseAuthorizationV1(
		ctx, database, uuid.New(), uuid.New(), uuid.New(),
		"admin-forbidden-"+uuid.NewString(), wia.projectID, wia.runID,
		publishNodeID, adminID,
	); err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("admin satisfied owner-required Publish error=%v, want 42501", err)
	}

	operationID, authorizationID, actionEventID := uuid.New(), uuid.New(), uuid.New()
	requestKey := "qualified-release-" + authorizationID.String()
	authorize := func() qualificationReleaseV1CallResult {
		bundle, callErr := callQualificationReleaseAuthorizationV1(
			ctx, database, operationID, authorizationID, actionEventID, requestKey,
			wia.projectID, wia.runID, publishNodeID, wia.userID,
		)
		return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
	}
	assertQualificationReleaseConcurrentReplay(t, authorize)

	var expectedRunID uuid.UUID
	if err := database.QueryRowContext(ctx, `
SELECT expected_production_run_id
FROM qualification_release_v1_authorizations WHERE authorization_id=$1`,
		authorizationID,
	).Scan(&expectedRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE qualification_release_v1_authorizations
SET release_request_key='tampered' WHERE authorization_id=$1`, authorizationID); err == nil ||
		!strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("authorization tamper error=%v, want WQR02", err)
	}
	proposalID := uuid.New()
	for _, projection := range []struct {
		column string
		value  uuid.UUID
	}{
		{column: "input_manifest_id", value: wia.manifestID},
		{column: "output_proposal_id", value: proposalID},
		{column: "output_revision_id", value: wia.targetRevisionID},
	} {
		query := fmt.Sprintf(
			"UPDATE workflow_node_runs SET %s=$2 WHERE id=$1", projection.column,
		)
		if _, err := database.ExecContext(
			ctx, query, publishNodeID, projection.value,
		); err == nil || !strings.Contains(err.Error(), "WQR02") {
			t.Fatalf("direct Publish %s attachment error=%v, want WQR02",
				projection.column, err)
		}
	}
	if _, err := callQualificationReleaseStartV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		uuid.New(), "qualification-release-not-claimed", 1,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("controller start before claim error=%v, want WQR03", err)
	}
	firstLeaseOwner := "qualification-release-worker-epoch-1"
	if _, err := database.ExecContext(ctx, `
UPDATE workflow_node_runs
SET status='running',attempt=attempt+1,lease_owner=$2,
    lease_expires_at=date_trunc('milliseconds',clock_timestamp()+interval '1 minute'),
    started_at=date_trunc('milliseconds',clock_timestamp()),
    updated_at=date_trunc('milliseconds',clock_timestamp())
WHERE id=$1 AND status='ready'`, publishNodeID, firstLeaseOwner); err == nil ||
		!strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("direct generic Publish claim error=%v, want WQR02", err)
	}
	firstClaimEventID := uuid.New()
	claimFirst := func() qualificationReleaseV1CallResult {
		bundle, callErr := callQualificationReleaseClaimV1(
			ctx, database, authorizationID, wia.runID, publishNodeID,
			firstClaimEventID, firstLeaseOwner, 10000,
		)
		return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
	}
	assertQualificationReleaseConcurrentReplay(t, claimFirst)
	var firstExpiry time.Time
	if err := database.QueryRowContext(ctx, `
SELECT lease_expires_at FROM workflow_node_runs WHERE id=$1`, publishNodeID,
	).Scan(&firstExpiry); err != nil {
		t.Fatal(err)
	}
	startFirstEpoch := func() qualificationReleaseV1CallResult {
		bundle, callErr := callQualificationReleaseStartV1(
			ctx, database, authorizationID, wia.runID, publishNodeID,
			firstClaimEventID, firstLeaseOwner, 1,
		)
		return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
	}
	assertQualificationReleaseConcurrentReplay(t, startFirstEpoch)
	var firstBindingHash string
	if err := database.QueryRowContext(ctx, `
SELECT binding_hash FROM qualification_release_v1_controller_bindings
WHERE authorization_id=$1`, authorizationID).Scan(&firstBindingHash); err != nil {
		t.Fatal(err)
	}
	if _, err := callQualificationReleaseClaimV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		uuid.New(), "qualification-release-competing-worker", 5000,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("concurrent different-ID claim error=%v, want WQR03", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE workflow_runs SET event_cursor=event_cursor+1 WHERE id=$1`,
		wia.runID); err == nil || !strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("direct Publish cursor mutation error=%v, want WQR02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE workflow_runs SET status='failed',failure='{"code":"bypass"}'::jsonb
WHERE id=$1`, wia.runID); err == nil || !strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("direct Publish failed transition error=%v, want WQR02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE workflow_runs SET cancelled_at=clock_timestamp()
WHERE id=$1`, wia.runID); err == nil || !strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("direct Publish cancelled_at mutation error=%v, want WQR02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE workflow_node_runs SET lease_expires_at=$2 WHERE id=$1`,
		publishNodeID, firstExpiry.Add(time.Second)); err == nil ||
		!strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("direct generic Publish renew error=%v, want WQR02", err)
	}
	renewedExpiry := firstExpiry.Add(time.Second)
	renewFirst := func() qualificationReleaseV1CallResult {
		bundle, callErr := callQualificationReleaseRenewV1(
			ctx, database, authorizationID, wia.runID, publishNodeID,
			firstClaimEventID, firstLeaseOwner, 1, firstExpiry, renewedExpiry,
		)
		return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
	}
	assertQualificationReleaseConcurrentReplay(t, renewFirst)
	if wait := time.Until(renewedExpiry.Add(150 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if _, err := callQualificationReleaseRenewV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		firstClaimEventID, firstLeaseOwner, 1, firstExpiry, renewedExpiry,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("expired renew replay error=%v, want WQR03", err)
	}
	leaseOwner := "qualification-release-worker-epoch-2"
	secondClaimEventID := uuid.New()
	claimSecond := func() qualificationReleaseV1CallResult {
		bundle, callErr := callQualificationReleaseClaimV1(
			ctx, database, authorizationID, wia.runID, publishNodeID,
			secondClaimEventID, leaseOwner, 300000,
		)
		return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
	}
	assertQualificationReleaseConcurrentReplay(t, claimSecond)
	staleReplay, err := callQualificationReleaseClaimV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		firstClaimEventID, firstLeaseOwner, 10000,
	)
	if err != nil || staleReplay["idempotent"] != true || staleReplay["active"] != false {
		t.Fatalf("stale claim replay bundle=%#v error=%v", staleReplay, err)
	}
	if _, err := callQualificationReleaseStartV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		firstClaimEventID, firstLeaseOwner, 1,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("stale claim borrowed current epoch for Controller start error=%v, want WQR03", err)
	}

	reclaimedStart, err := callQualificationReleaseStartV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		secondClaimEventID, leaseOwner, 2,
	)
	if err != nil || reclaimedStart["idempotent"] != true {
		t.Fatalf("reclaimed epoch Controller replay bundle=%#v error=%v",
			reclaimedStart, err)
	}
	var reclaimedBindingHash string
	if err := database.QueryRowContext(ctx, `
SELECT binding_hash FROM qualification_release_v1_controller_bindings
WHERE authorization_id=$1`, authorizationID).Scan(&reclaimedBindingHash); err != nil ||
		reclaimedBindingHash != firstBindingHash {
		t.Fatalf("reclaimed Controller binding hash=%q want=%q error=%v",
			reclaimedBindingHash, firstBindingHash, err)
	}
	if expectedRunID == uuid.Nil {
		t.Fatal("Qualification Release allocated a nil Production Run identity")
	}
	if _, err := callQualificationReleaseUnaryV1(
		ctx, database,
		"worksflow:qualification-release-result-v1:"+authorizationID.String(),
		"record_qualification_release_result_v1", authorizationID,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("premature healthy result record error=%v, want WQR03", err)
	}

	seedQualificationReleaseV1HealthyControllerResult(
		t, ctx, database, wia, authorizationID, expectedRunID, release,
	)
	record := func() qualificationReleaseV1CallResult {
		bundle, callErr := callQualificationReleaseUnaryV1(
			ctx, database,
			"worksflow:qualification-release-result-v1:"+authorizationID.String(),
			"record_qualification_release_result_v1", authorizationID,
		)
		return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
	}
	assertQualificationReleaseConcurrentReplay(t, record)
	if _, err := callQualificationReleaseApplyV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		firstLeaseOwner, 1,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("stale lease epoch apply error=%v, want WQR03", err)
	}

	apply := func() qualificationReleaseV1CallResult {
		bundle, callErr := callQualificationReleaseApplyV1(
			ctx, database, authorizationID, wia.runID, publishNodeID, leaseOwner, 2,
		)
		return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
	}
	assertQualificationReleaseConcurrentReplay(t, apply)
	if _, err := callQualificationReleaseApplyV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		"qualification-release-wrong-completed-replay", 2,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("completed apply replay with wrong lease error=%v, want WQR03", err)
	}
	assertQualificationReleaseV1Closure(
		t, ctx, database, authorizationID, wia.runID, publishNodeID, actionEventID,
	)

	if _, err := database.ExecContext(ctx, `
UPDATE workflow_run_events SET payload='{}'::jsonb WHERE id=$1`, actionEventID); err == nil ||
		!strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("protected Workflow event tamper error=%v, want WQR02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE workflow_run_events SET payload='{}'::jsonb WHERE id=$1`, firstClaimEventID); err == nil ||
		!strings.Contains(err.Error(), "WQR02") {
		t.Fatalf("protected claim event tamper error=%v, want WQR02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE outbox_events
SET attempts=attempts+1,last_error='delivery retry',
    available_at=available_at+interval '1 second'
WHERE id=$1`, actionEventID); err != nil {
		t.Fatalf("advance mutable Outbox delivery projection: %v", err)
	}
	var completionExact bool
	if err := database.QueryRowContext(ctx, `
SELECT qualification_release_v1_completion_is_exact($1)`, authorizationID,
	).Scan(&completionExact); err != nil || !completionExact {
		t.Fatalf("completion exact after Outbox delivery evolution=%t error=%v", completionExact, err)
	}
	if _, err := callQualificationReleaseStartV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		secondClaimEventID, leaseOwner, 2,
	); err == nil || !strings.Contains(err.Error(), "WQR03") {
		t.Fatalf("controller start replay without active claim error=%v, want WQR03", err)
	}
	var controllerInspection []byte
	if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_release_controller_v1($1) AS value`,
		authorizationID).Scan(&controllerInspection); err != nil || len(controllerInspection) == 0 {
		t.Fatalf("inspect existing Controller after claim loss bytes=%d error=%v",
			len(controllerInspection), err)
	}
	if _, err := database.ExecContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,'qualified-controller','2026.07.20','worksflow.release-delivery/v3',$2
) AS value`, bootstrapID, trustDigest); err != nil {
		t.Fatalf("bootstrap replay after closed release: %v", err)
	}
}

func TestQualificationReleaseV1RoleAuthorizationPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	ensureQualificationReleaseHardenedRoles(t, ctx, base)
	for _, testCase := range []struct {
		name                  string
		requiredRole          string
		actorRole             string
		duplicateExactPreview bool
	}{
		{name: "admin_exact", requiredRole: "admin", actorRole: "admin"},
		{name: "owner_satisfies_admin", requiredRole: "admin", actorRole: "owner"},
		{name: "duplicate_preview_rejected", requiredRole: "admin", actorRole: "owner", duplicateExactPreview: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := qualificationPlanMigrationDatabase(
				t, ctx, base, dsn, "qualification_release_role_"+testCase.name+"_",
			)
			applyQualificationPromotionV2Prefix(t, ctx, database)
			applyQualificationHandoffV1(t, ctx, database)
			applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
			applyQualificationReleaseV1(t, ctx, database)
			assertQualificationReleaseRoleAuthorizationCanary(
				t, ctx, database, testCase.requiredRole, testCase.actorRole,
				testCase.duplicateExactPreview,
			)
		})
	}
}

type qualificationReleaseV1FailureFixture struct {
	wia             workflowInputCanary
	publishNodeID   uuid.UUID
	release         qualificationReleaseV1AuthorityFixture
	authorizationID uuid.UUID
	actionEventID   uuid.UUID
	productionRunID uuid.UUID
	firstClaimID    uuid.UUID
	firstLeaseOwner string
}

func TestQualificationReleaseV1TerminalFailurePostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	ensureQualificationReleaseHardenedRoles(t, ctx, base)
	for _, outcome := range []string{
		"production_failed",
		"controller_rejected",
		"pre_submit_cancelled",
	} {
		t.Run(outcome, func(t *testing.T) {
			database := qualificationPlanMigrationDatabase(
				t, ctx, base, dsn, "qualification_release_failure_"+outcome+"_",
			)
			applyQualificationPromotionV2Prefix(t, ctx, database)
			applyQualificationHandoffV1(t, ctx, database)
			applyCanonicalReviewForwardEquivalenceForRelease(t, ctx, database)
			applyQualificationReleaseV1(t, ctx, database)
			if _, err := database.ExecContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,'qualified-controller','2026.07.20','worksflow.release-delivery/v3',$2
) AS value`, uuid.New(), qualificationReleaseV1TestHash("7")); err != nil {
				t.Fatalf("bootstrap failure-canary Controller: %v", err)
			}
			fixture := seedQualificationReleaseV1FailureFixture(
				t, ctx, database, outcome,
			)
			seedQualificationReleaseV1ControllerFailure(
				t, ctx, database, fixture, outcome,
			)
			var firstExpiry time.Time
			if err := database.QueryRowContext(ctx, `
SELECT lease_expires_at FROM workflow_node_runs WHERE id=$1`, fixture.publishNodeID,
			).Scan(&firstExpiry); err != nil {
				t.Fatal(err)
			}
			if wait := time.Until(firstExpiry.Add(150 * time.Millisecond)); wait > 0 {
				time.Sleep(wait)
			}
			secondClaimID := uuid.New()
			secondLeaseOwner := "qualification-release-failure-epoch-2-" + outcome
			claimSecond := func() qualificationReleaseV1CallResult {
				bundle, callErr := callQualificationReleaseClaimV1(
					ctx, database, fixture.authorizationID, fixture.wia.runID,
					fixture.publishNodeID, secondClaimID, secondLeaseOwner, 300000,
				)
				return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
			}
			assertQualificationReleaseConcurrentReplay(t, claimSecond)
			recordFailure := func() qualificationReleaseV1CallResult {
				bundle, callErr := callQualificationReleaseUnaryV1(
					ctx, database,
					"worksflow:qualification-release-result-v1:"+fixture.authorizationID.String(),
					"record_qualification_release_failure_v1", fixture.authorizationID,
				)
				return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
			}
			assertQualificationReleaseConcurrentReplay(t, recordFailure)
			if _, err := callQualificationReleaseFailureApplyV1(
				ctx, database, fixture.authorizationID, fixture.wia.runID,
				fixture.publishNodeID, fixture.firstLeaseOwner, 1,
			); err == nil || !strings.Contains(err.Error(), "WQR03") {
				t.Fatalf("stale failure apply error=%v, want WQR03", err)
			}
			applyFailure := func() qualificationReleaseV1CallResult {
				bundle, callErr := callQualificationReleaseFailureApplyV1(
					ctx, database, fixture.authorizationID, fixture.wia.runID,
					fixture.publishNodeID, secondLeaseOwner, 2,
				)
				return qualificationReleaseV1CallResult{bundle: bundle, err: callErr}
			}
			assertQualificationReleaseConcurrentReplay(t, applyFailure)
			if _, err := callQualificationReleaseFailureApplyV1(
				ctx, database, fixture.authorizationID, fixture.wia.runID,
				fixture.publishNodeID, "qualification-release-wrong-failure-replay", 2,
			); err == nil || !strings.Contains(err.Error(), "WQR03") {
				t.Fatalf("wrong-owner failed replay error=%v, want WQR03", err)
			}
			assertQualificationReleaseV1FailureClosure(
				t, ctx, database, fixture, outcome,
			)
			if _, err := callQualificationReleaseUnaryV1(
				ctx, database,
				"worksflow:qualification-release-result-v1:"+fixture.authorizationID.String(),
				"record_qualification_release_result_v1", fixture.authorizationID,
			); err == nil || !strings.Contains(err.Error(), "WQR02") {
				t.Fatalf("healthy result after terminal failure error=%v, want WQR02", err)
			}
			if _, err := callQualificationReleaseApplyV1(
				ctx, database, fixture.authorizationID, fixture.wia.runID,
				fixture.publishNodeID, secondLeaseOwner, 2,
			); err == nil || !strings.Contains(err.Error(), "WQR02") {
				t.Fatalf("healthy apply after terminal failure error=%v, want WQR02", err)
			}
			if _, err := callQualificationReleaseClaimV1(
				ctx, database, fixture.authorizationID, fixture.wia.runID,
				fixture.publishNodeID, uuid.New(), "qualification-release-after-failure", 5000,
			); err == nil || !strings.Contains(err.Error(), "WQR03") {
				t.Fatalf("claim after terminal failure error=%v, want WQR03", err)
			}
			if _, err := callQualificationReleaseStartV1(
				ctx, database, fixture.authorizationID, fixture.wia.runID,
				fixture.publishNodeID, secondClaimID, secondLeaseOwner, 2,
			); err == nil || !strings.Contains(err.Error(), "WQR03") {
				t.Fatalf("start after terminal failure error=%v, want WQR03", err)
			}
		})
	}
}

func seedQualificationReleaseV1FailureFixture(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	suffix string,
) qualificationReleaseV1FailureFixture {
	t.Helper()
	wia := seedWorkflowInputCanary(t, ctx, database)
	publishNodeID := upgradeQualificationReleaseDefinitionV3(
		t, ctx, database, &wia,
		"production-release-"+strings.ReplaceAll(uuid.NewString(), "-", ""),
		"owner",
	)
	setQualificationReleaseSourceManifest(t, ctx, database, wia)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForPromotionV2(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatalf("freeze failure-canary Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	promotionCommand := []uuid.UUID{
		uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New(),
	}
	consumeQualificationPromotionV2(t, ctx, database, promotionCommand)
	seedQualificationHandoffParentLineage(t, ctx, database, wia)
	completeQualificationHandoffV1(t, ctx, database, promotionCommand[3])
	release := seedQualificationReleaseV1Authority(t, ctx, database, wia)
	authorizationID, actionEventID := uuid.New(), uuid.New()
	if _, err := callQualificationReleaseAuthorizationV1(
		ctx, database, uuid.New(), authorizationID, actionEventID,
		"failure-canary-"+suffix+"-"+authorizationID.String(),
		wia.projectID, wia.runID, publishNodeID, wia.userID,
	); err != nil {
		t.Fatalf("authorize failure-canary release: %v", err)
	}
	var productionRunID uuid.UUID
	if err := database.QueryRowContext(ctx, `
SELECT expected_production_run_id
FROM qualification_release_v1_authorizations WHERE authorization_id=$1`,
		authorizationID,
	).Scan(&productionRunID); err != nil {
		t.Fatal(err)
	}
	firstClaimID := uuid.New()
	firstLeaseOwner := "qualification-release-failure-epoch-1-" + suffix
	if _, err := callQualificationReleaseClaimV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		firstClaimID, firstLeaseOwner, 5000,
	); err != nil {
		t.Fatalf("claim failure-canary release: %v", err)
	}
	if _, err := callQualificationReleaseStartV1(
		ctx, database, authorizationID, wia.runID, publishNodeID,
		firstClaimID, firstLeaseOwner, 1,
	); err != nil {
		t.Fatalf("start failure-canary Controller: %v", err)
	}
	return qualificationReleaseV1FailureFixture{
		wia: wia, publishNodeID: publishNodeID, release: release,
		authorizationID: authorizationID, actionEventID: actionEventID,
		productionRunID: productionRunID, firstClaimID: firstClaimID,
		firstLeaseOwner: firstLeaseOwner,
	}
}

func assertQualificationReleaseRoleAuthorizationCanary(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	requiredRole string,
	actorRole string,
	duplicateExactPreview bool,
) {
	t.Helper()
	wia := seedWorkflowInputCanary(t, ctx, database)
	publishNodeID := upgradeQualificationReleaseDefinitionV3(
		t, ctx, database, &wia,
		"production-release-"+strings.ReplaceAll(uuid.NewString(), "-", ""),
		requiredRole,
	)
	setQualificationReleaseSourceManifest(t, ctx, database, wia)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForPromotionV2(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatalf("freeze role canary Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	promotionCommand := []uuid.UUID{
		uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New(),
	}
	consumeQualificationPromotionV2(t, ctx, database, promotionCommand)
	seedQualificationHandoffParentLineage(t, ctx, database, wia)
	completeQualificationHandoffV1(t, ctx, database, promotionCommand[3])
	release := seedQualificationReleaseV1Authority(t, ctx, database, wia)
	if duplicateExactPreview {
		seedQualificationReleasePreviewSibling(
			t, ctx, database, wia, release, "release-preview-receipt/v2",
		)
	}
	actorID := wia.userID
	if actorRole != "owner" {
		actorID = seedQualificationReleaseProjectMember(
			t, ctx, database, wia.projectID, actorRole,
		)
	}
	authorizationID := uuid.New()
	bundle, err := callQualificationReleaseAuthorizationV1(
		ctx, database, uuid.New(), authorizationID, uuid.New(),
		"role-canary-"+authorizationID.String(), wia.projectID, wia.runID,
		publishNodeID, actorID,
	)
	if duplicateExactPreview {
		if err == nil || !strings.Contains(err.Error(), "WQR03") {
			t.Fatalf("duplicate exact Preview authorization bundle=%#v error=%v, want WQR03",
				bundle, err)
		}
		var durable int
		if countErr := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_release_v1_authorizations
WHERE authorization_id=$1`, authorizationID).Scan(&durable); countErr != nil || durable != 0 {
			t.Fatalf("ambiguous Preview left authorization=%d error=%v", durable, countErr)
		}
		return
	}
	if err != nil || bundle["idempotent"] != false {
		t.Fatalf("requiredRole=%q actorRole=%q authorization bundle=%#v error=%v",
			requiredRole, actorRole, bundle, err)
	}
	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT qualification_release_v1_authorization_is_exact($1)`,
		authorizationID).Scan(&exact); err != nil || !exact {
		t.Fatalf("requiredRole=%q actorRole=%q exact=%t error=%v",
			requiredRole, actorRole, exact, err)
	}
}

func seedQualificationReleasePreviewSibling(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	release qualificationReleaseV1AuthorityFixture,
	receiptSchemaVersion string,
) {
	t.Helper()
	if receiptSchemaVersion != "release-preview-receipt/v1" &&
		receiptSchemaVersion != "release-preview-receipt/v2" {
		t.Fatalf("unsupported sibling Preview schema %q", receiptSchemaVersion)
	}
	var workspaceHash string
	var controllerOperationID uuid.UUID
	var controllerResultHash string
	if err := database.QueryRowContext(ctx, `
SELECT workflow_input_normalize_sha256(revision.content_hash),
       preview.controller_operation_id,preview.controller_result_hash
FROM artifact_revisions AS revision
CROSS JOIN release_preview_receipts AS preview
WHERE revision.id=$1 AND preview.id=$2`,
		wia.targetRevisionID, release.previewID,
	).Scan(&workspaceHash, &controllerOperationID, &controllerResultHash); err != nil {
		t.Fatalf("read sibling Preview authority: %v", err)
	}
	runID, receiptID, approvalID := uuid.New(), uuid.New(), uuid.New()
	receiptHash := qualificationReleaseV1TestHash("8")
	approvalHash := qualificationReleaseV1TestHash("9")
	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	runSchemaVersion := "release-preview-run/v1"
	version, fenceEpoch := 1, 0
	if receiptSchemaVersion == "release-preview-receipt/v2" {
		runSchemaVersion, version, fenceEpoch = "release-preview-run/v2", 2, 1
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_runs(
  id,schema_version,project_id,release_bundle_id,release_bundle_hash,
  request_key,request_hash,reason,state,version,fence_epoch,started_at,
  finished_at,created_by,updated_by,created_at,updated_at
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,'sibling ambiguity canary','passed',$8,$9,
  $10,$10,$11,$11,$10,$10
)`, runID, runSchemaVersion, wia.projectID, release.bundleID,
		release.bundleHash, "sibling-preview-"+runID.String(),
		qualificationReleaseV1TestHash("8"), version, fenceEpoch, createdAt,
		wia.userID); err != nil {
		t.Fatalf("seed sibling Preview Run: %v", err)
	}
	var operationArg any
	var resultHashArg any
	if receiptSchemaVersion == "release-preview-receipt/v2" {
		operationArg, resultHashArg = controllerOperationID, controllerResultHash
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_receipts(
  id,schema_version,run_id,project_id,release_bundle_id,release_bundle_hash,
  canonical_receipt_id,canonical_receipt_hash,workspace_artifact_id,
  workspace_revision_id,workspace_content_hash,release_artifacts,namespace,
  provider,provider_ref,checks,decision,content_store,content_ref,content_hash,
  payload_hash,created_by,created_at,controller_operation_id,
  controller_result_hash
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,
  'qualification-release-sibling','fixture','sibling/ref',
  '[{"id":"preview","kind":"health","status":"passed"}]'::jsonb,
  'passed','blob',$13,$14,$15,$16,$17,$18,$19
)`, receiptID, receiptSchemaVersion, runID, wia.projectID, release.bundleID,
		release.bundleHash, release.canonicalID, release.canonicalHash,
		wia.targetArtifactID, wia.targetRevisionID, workspaceHash,
		release.releaseArtifacts,
		"blob://qualification-release/sibling-preview/"+receiptID.String(),
		qualificationReleaseV1TestHash("8"), receiptHash, wia.userID, createdAt,
		operationArg, resultHashArg); err != nil {
		t.Fatalf("seed sibling Preview Receipt: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_promotion_approvals(
  id,schema_version,project_id,release_bundle_id,release_bundle_hash,
  preview_receipt_id,preview_receipt_hash,reason,content_store,content_ref,
  content_hash,payload_hash,created_by,created_at
) VALUES (
  $1,'release-promotion-approval/v1',$2,$3,$4,$5,$6,
  'sibling ambiguity canary','blob',$7,$8,$9,$10,$11
)`, approvalID, wia.projectID, release.bundleID, release.bundleHash,
		receiptID, receiptHash,
		"blob://qualification-release/sibling-approval/"+approvalID.String(),
		qualificationReleaseV1TestHash("9"), approvalHash, wia.userID,
		createdAt); err != nil {
		t.Fatalf("seed sibling Promotion Approval: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit sibling Preview authority: %v", err)
	}
}

func setQualificationReleaseSourceManifest(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
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
	if _, err := transaction.ExecContext(ctx, `
UPDATE artifact_revisions SET source_manifest_id=$2 WHERE id=$1`,
		wia.targetRevisionID, wia.manifestID,
	); err != nil {
		t.Fatalf("bind Qualification Release Workspace source manifest: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO project_members(project_id,user_id,role,invited_by)
VALUES($1,$2,'owner',$2)
ON CONFLICT(project_id,user_id) DO UPDATE SET role='owner',updated_at=clock_timestamp()`,
		wia.projectID, wia.userID,
	); err != nil {
		t.Fatalf("bind Qualification Release owner membership: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func upgradeQualificationReleaseDefinitionV3(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture *workflowInputCanary,
	publishKey string,
	requiredRole string,
) uuid.UUID {
	t.Helper()
	seeded, _ := workflowExecutionProfileV3MigrationDefinition(t)
	nodes := append([]domain.NodeDefinition(nil), seeded.Nodes...)
	originalPublishKey := ""
	for index := range nodes {
		if nodes[index].Type != domain.NodePublish {
			continue
		}
		originalPublishKey = nodes[index].ID
		nodes[index].ID = publishKey
		publish := *nodes[index].Publish
		publish.RequiredRole = requiredRole
		nodes[index].Publish = &publish
	}
	if originalPublishKey == "" {
		t.Fatal("profile-v3 fixture has no Publish node")
	}
	edges := append([]domain.WorkflowEdge(nil), seeded.Edges...)
	for index := range edges {
		if edges[index].From == originalPublishKey {
			edges[index].From = publishKey
		}
		if edges[index].To == originalPublishKey {
			edges[index].To = publishKey
		}
		if edges[index].From == "quality" && edges[index].To == "external-qualification" {
			edges[index].ID = "quality-to-external"
		}
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		fixture.definitionID.String(), 1, "Qualification Release v3", "6",
		nodes, edges, *seeded.InputContract, *seeded.OutputContract,
		seeded.ExecutionProfile, fixture.userID.String(), time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	publishNodeID := uuid.New()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_definition_versions
SET schema_version=6,content=$2,content_hash=$3
WHERE id=$1`, fixture.definitionVersionID, encoded, definition.Hash); err != nil {
		t.Fatalf("upgrade Qualification Release definition: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_runs
SET context=jsonb_set(
  context,ARRAY['nodes',$2::text],jsonb_build_object('definitionNodeId',$2::text),true
)
WHERE id=$1`, fixture.runID, publishKey); err != nil {
		t.Fatalf("add custom Qualification Release Publish context: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_node_runs(
  id,run_id,node_key,node_type,status,definition_node_id,slice_kind
) VALUES($1,$2,$3,'publish','pending',$3,'root')`,
		publishNodeID, fixture.runID, publishKey); err != nil {
		t.Fatalf("add custom Qualification Release Publish node: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	fixture.definitionRaw = encoded
	return publishNodeID
}

func seedQualificationReleaseProjectMember(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	projectID uuid.UUID,
	role string,
) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users(id,email,display_name,password_hash)
VALUES($1,$2,'Qualification Release role canary','x')`,
		userID, strings.ToLower(userID.String())+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO project_members(project_id,user_id,role,invited_by)
VALUES($1,$2,$3,$2)`, projectID, userID, role); err != nil {
		t.Fatal(err)
	}
	return userID
}

func assertQualificationReleaseConcurrentReplay(
	t *testing.T,
	call func() qualificationReleaseV1CallResult,
) {
	t.Helper()
	results := make(chan qualificationReleaseV1CallResult, 2)
	for range 2 {
		go func() { results <- call() }()
	}
	var fresh, replay map[string]any
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Qualification Release call: %v", result.err)
		}
		switch result.bundle["idempotent"] {
		case false:
			fresh = result.bundle
		case true:
			replay = result.bundle
		default:
			t.Fatalf("Qualification Release response has invalid idempotency: %#v", result.bundle)
		}
	}
	if fresh == nil || replay == nil {
		t.Fatalf("Qualification Release concurrent fresh/replay drift: fresh=%#v replay=%#v", fresh, replay)
	}
	delete(fresh, "idempotent")
	delete(replay, "idempotent")
	freshJSON, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	replayJSON, err := json.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	if string(freshJSON) != string(replayJSON) {
		t.Fatalf("Qualification Release replay differs from fresh bundle")
	}
}

func callQualificationReleaseAuthorizationV1(
	ctx context.Context,
	database *sql.DB,
	operationID uuid.UUID,
	authorizationID uuid.UUID,
	actionEventID uuid.UUID,
	requestKey string,
	projectID uuid.UUID,
	workflowRunID uuid.UUID,
	publishNodeRunID uuid.UUID,
	actorID uuid.UUID,
) (map[string]any, error) {
	return callQualificationReleaseJSONV1(
		ctx, database,
		"worksflow:qualification-release-v1:"+workflowRunID.String()+":"+publishNodeRunID.String(),
		`SELECT value FROM authorize_qualification_release_v1(
  $1,$2,$3,$4,$5,$6,$7,$8
) AS value`,
		operationID, authorizationID, actionEventID, requestKey, projectID,
		workflowRunID, publishNodeRunID, actorID,
	)
}

func callQualificationReleaseUnaryV1(
	ctx context.Context,
	database *sql.DB,
	lockKey string,
	functionName string,
	authorizationID uuid.UUID,
) (map[string]any, error) {
	switch functionName {
	case "record_qualification_release_result_v1", "record_qualification_release_failure_v1":
	default:
		return nil, fmt.Errorf("unsupported Qualification Release function %q", functionName)
	}
	return callQualificationReleaseJSONV1(
		ctx, database, lockKey,
		fmt.Sprintf("SELECT value FROM %s($1) AS value", functionName),
		authorizationID,
	)
}

func callQualificationReleaseStartV1(
	ctx context.Context,
	database *sql.DB,
	authorizationID uuid.UUID,
	workflowRunID uuid.UUID,
	publishNodeRunID uuid.UUID,
	claimEventID uuid.UUID,
	leaseOwner string,
	leaseAttempt int,
) (map[string]any, error) {
	return callQualificationReleaseJSONV1(
		ctx, database,
		"worksflow:qualification-release-v1:"+workflowRunID.String()+":"+publishNodeRunID.String(),
		`SELECT value FROM start_qualification_release_controller_v1(
  $1,$2,$3,$4
) AS value`,
		authorizationID, claimEventID, leaseOwner, leaseAttempt,
	)
}

func callQualificationReleaseApplyV1(
	ctx context.Context,
	database *sql.DB,
	authorizationID uuid.UUID,
	workflowRunID uuid.UUID,
	publishNodeRunID uuid.UUID,
	leaseOwner string,
	leaseAttempt int,
) (map[string]any, error) {
	return callQualificationReleaseJSONV1(
		ctx, database,
		"worksflow:qualification-release-v1:"+workflowRunID.String()+":"+publishNodeRunID.String(),
		`SELECT value FROM apply_qualification_release_result_v1(
  $1,$2,$3,$4,$5
) AS value`,
		authorizationID, workflowRunID, publishNodeRunID, leaseOwner, leaseAttempt,
	)
}

func callQualificationReleaseFailureApplyV1(
	ctx context.Context,
	database *sql.DB,
	authorizationID uuid.UUID,
	workflowRunID uuid.UUID,
	publishNodeRunID uuid.UUID,
	leaseOwner string,
	leaseAttempt int,
) (map[string]any, error) {
	return callQualificationReleaseJSONV1(
		ctx, database,
		"worksflow:qualification-release-v1:"+workflowRunID.String()+":"+publishNodeRunID.String(),
		`SELECT value FROM apply_qualification_release_failure_v1(
  $1,$2,$3,$4,$5
) AS value`,
		authorizationID, workflowRunID, publishNodeRunID, leaseOwner, leaseAttempt,
	)
}

func callQualificationReleaseClaimV1(
	ctx context.Context,
	database *sql.DB,
	authorizationID uuid.UUID,
	workflowRunID uuid.UUID,
	publishNodeRunID uuid.UUID,
	claimEventID uuid.UUID,
	leaseOwner string,
	leaseDurationMilliseconds int,
) (map[string]any, error) {
	return callQualificationReleaseJSONV1(
		ctx, database,
		"worksflow:qualification-release-v1:"+workflowRunID.String()+":"+publishNodeRunID.String(),
		`SELECT value FROM claim_qualification_release_publish_v1(
  $1,$2,$3,$4,$5,$6
) AS value`,
		authorizationID, workflowRunID, publishNodeRunID, claimEventID,
		leaseOwner, leaseDurationMilliseconds,
	)
}

func callQualificationReleaseRenewV1(
	ctx context.Context,
	database *sql.DB,
	authorizationID uuid.UUID,
	workflowRunID uuid.UUID,
	publishNodeRunID uuid.UUID,
	claimEventID uuid.UUID,
	leaseOwner string,
	leaseAttempt int,
	expectedLeaseExpiresAt time.Time,
	newLeaseExpiresAt time.Time,
) (map[string]any, error) {
	return callQualificationReleaseJSONV1(
		ctx, database,
		"worksflow:qualification-release-v1:"+workflowRunID.String()+":"+publishNodeRunID.String(),
		`SELECT value FROM renew_qualification_release_publish_lease_v1(
  $1,$2,$3,$4,$5,$6,$7,$8
) AS value`,
		authorizationID, workflowRunID, publishNodeRunID, claimEventID,
		leaseOwner, leaseAttempt, expectedLeaseExpiresAt, newLeaseExpiresAt,
	)
}

func callQualificationReleaseJSONV1(
	ctx context.Context,
	database *sql.DB,
	lockKey string,
	query string,
	args ...any,
) (map[string]any, error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(pg_catalog.hashtextextended($1,0))`, lockKey); err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			_, _ = connection.ExecContext(context.WithoutCancel(ctx), `
SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended($1,0))`, lockKey)
		}
	}()
	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err := transaction.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	var unlocked bool
	if err := connection.QueryRowContext(ctx, `
SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended($1,0))`, lockKey,
	).Scan(&unlocked); err != nil || !unlocked {
		return nil, fmt.Errorf("unlock Qualification Release call unlocked=%t: %w", unlocked, err)
	}
	locked = false
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}

func qualificationReleaseV1TestHash(digit string) string {
	return "sha256:" + strings.Repeat(digit, 64)
}

func seedQualificationReleaseV1Authority(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
) qualificationReleaseV1AuthorityFixture {
	t.Helper()
	fixture := qualificationReleaseV1AuthorityFixture{
		bundleID:      uuid.New(),
		canonicalID:   uuid.New(),
		canonicalHash: qualificationReleaseV1TestHash("4"),
		previewID:     uuid.New(),
		approvalID:    uuid.New(),
	}
	artifactKinds := []string{
		"web-static", "migration", "runtime-config-schema",
		"health-readiness-contract", "sbom", "vulnerability-report",
		"provenance", "signature",
	}
	artifacts := make([]map[string]any, 0, len(artifactKinds))
	for index, kind := range artifactKinds {
		artifacts = append(artifacts, map[string]any{
			"id":          fmt.Sprintf("release-artifact-%02d", index),
			"kind":        kind,
			"store":       "blob",
			"ref":         fmt.Sprintf("blob://qualification-release/%02d", index),
			"contentHash": qualificationReleaseV1TestHash("a"),
			"mediaType":   "application/octet-stream",
			"byteSize":    16,
		})
	}
	encodedArtifacts, err := json.Marshal(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	fixture.releaseArtifacts = encodedArtifacts

	var manifestHash, contractHash, workspaceHash string
	if err := database.QueryRowContext(ctx, `
SELECT authority.build_manifest_hash,authority.build_contract_hash,
       workflow_input_normalize_sha256(revision.content_hash)
FROM workflow_input_authorities AS authority
JOIN artifact_revisions AS revision ON revision.id=$2
WHERE authority.authority_id=$1`, wia.authorityID, wia.targetRevisionID,
	).Scan(&manifestHash, &contractHash, &workspaceHash); err != nil {
		t.Fatalf("read Qualification Release upstream hashes: %v", err)
	}

	fullStackID := uuid.New()
	profileID := "qualification-release-v1-" + wia.projectID.String()
	profileHash := qualificationReleaseV1TestHash("2")
	fullStackHash := workflowInputDigest(
		[]byte("qualification-release-full-stack-" + wia.projectID.String()),
	)
	planID, planHash := uuid.New(), qualificationReleaseV1TestHash("1")
	canonicalRunID, attemptID := uuid.New(), uuid.New()
	previewRunID, previewOperationID := uuid.New(), uuid.New()
	previewResultHash := "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	fail := func(label string, execErr error) {
		_ = transaction.Rollback()
		t.Fatalf("%s: %v", label, execErr)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		fail("disable Qualification Release authority fixture triggers", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO full_stack_template_releases(
  id,schema_version,template_id,release_version,document,content_hash,
  created_by,created_at
) VALUES (
  $1::uuid,'full-stack-template/v1','qualification-release',$5::text,
  jsonb_build_object(
    'id',($1::uuid)::text,'schemaVersion','full-stack-template/v1',
    'templateId','qualification-release','version',$5::text,
    'components',jsonb_build_array(jsonb_build_object('role','web'),jsonb_build_object('role','api')),
    'layout',jsonb_build_object(),'contentHash',$2::text,
    'createdBy',($3::uuid)::text,'createdAt',to_jsonb($4::timestamptz)
  ),$2::text,$3::uuid,$4::timestamptz
)`, fullStackID, fullStackHash, wia.userID, createdAt, wia.projectID.String()); err != nil {
		fail("seed Qualification Release FullStackTemplate", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO verification_profile_versions(
  profile_id,version,schema_version,document,content_hash,created_by,created_at
) VALUES (
  $1::text,1,'verification-profile/v1',
  jsonb_build_object(
    'schemaVersion','verification-profile/v1','id',$1::text,'version',1,
    'profileHash',$2::text,'supportedTemplateRoles',jsonb_build_array('web','api'),
    'verifierImages',jsonb_build_array(jsonb_build_object(
      'role','release','image','registry.example/release@sha256:'||repeat('b',64)
    )),'builtInChecks',jsonb_build_array(),'limits',jsonb_build_object(),
    'networkPolicy',jsonb_build_object(),'state','active'
  ),$2::text,$3::uuid,$4::timestamptz
)`, profileID, profileHash, wia.userID, createdAt); err != nil {
		fail("seed Qualification Release VerificationProfile", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO canonical_verification_plans(
  id,schema_version,scope,project_id,workspace_artifact_id,
  workspace_revision_id,workspace_content_hash,build_manifest_id,
  build_manifest_hash,build_contract_id,build_contract_hash,
  full_stack_template_id,full_stack_template_hash,verification_profile_id,
  verification_profile_version,verification_profile_hash,template_releases,
  obligations,check_ids,required_check_ids,check_count,obligation_count,
  runtime_policy_hash,content_store,content_ref,content_hash,plan_hash,
  created_by,created_at
) VALUES (
  $1,'canonical-verification-plan/v1','canonical',$2,$3,$4,$5,$6,$7,$8,$9,
  $10,$11,$12,1,$13,
  jsonb_build_array(
    jsonb_build_object('role','web','id',($14::uuid)::text,'contentHash',$15::text),
    jsonb_build_object('role','api','id',($16::uuid)::text,'contentHash',$17::text)
  ),
  '[{"id":"release","level":"must","status":"ready","oracleIds":["release"]}]'::jsonb,
  '["release-artifacts","release-container-policy","release-sbom","release-vulnerability"]'::jsonb,
  '["release-artifacts","release-container-policy","release-sbom","release-vulnerability"]'::jsonb,
  4,1,$18,'blob',$19,$20,$20,$21,$22
)`, planID, wia.projectID, wia.targetArtifactID, wia.targetRevisionID,
		workspaceHash, wia.buildManifestID, manifestHash, wia.buildContractID,
		contractHash, fullStackID, fullStackHash, profileID, profileHash,
		uuid.New(), qualificationReleaseV1TestHash("c"), uuid.New(),
		qualificationReleaseV1TestHash("d"), qualificationReleaseV1TestHash("e"),
		"blob://qualification-release/plan/"+planID.String(), planHash, wia.userID, createdAt,
	); err != nil {
		fail("seed Qualification Release Canonical Plan", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO canonical_verification_runs(
  id,schema_version,project_id,plan_id,plan_hash,request_key,request_hash,
  reason,state,version,fence_epoch,started_at,finished_at,created_by,
  updated_by,created_at,updated_at
) VALUES (
  $1,'canonical-verification-run/v1',$2,$3,$4,$5,$6,
  'qualification release canonical','passed',1,0,$7,$7,$8,$8,$7,$7
)`, canonicalRunID, wia.projectID, planID, planHash,
		"canonical-"+canonicalRunID.String(), qualificationReleaseV1TestHash("f"),
		createdAt, wia.userID,
	); err != nil {
		fail("seed Qualification Release Canonical Run", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO canonical_verification_receipts(
  id,schema_version,scope,run_id,project_id,plan_id,plan_hash,
  workspace_artifact_id,workspace_revision_id,workspace_content_hash,
  build_manifest_id,build_manifest_hash,build_contract_id,build_contract_hash,
  full_stack_template_id,full_stack_template_hash,verification_profile_id,
  verification_profile_version,verification_profile_hash,attempt_ids,
  release_artifacts,check_count,coverage_count,must_count,must_passed_count,
  blocker_count,warning_count,decision,content_store,content_ref,content_hash,
  payload_hash,created_by,created_at
) VALUES (
  $1,'canonical-verification-receipt/v1','canonical',$2,$3,$4,$5,$6,$7,$8,
  $9,$10,$11,$12,$13,$14,$15,1,$16,jsonb_build_array($17::text),$18::jsonb,
  4,1,1,1,0,0,'passed','blob',$19,$20,$21,$22,$23
)`, fixture.canonicalID, canonicalRunID, wia.projectID, planID, planHash,
		wia.targetArtifactID, wia.targetRevisionID, workspaceHash,
		wia.buildManifestID, manifestHash, wia.buildContractID, contractHash,
		fullStackID, fullStackHash, profileID, profileHash, attemptID,
		fixture.releaseArtifacts, "blob://qualification-release/canonical/"+fixture.canonicalID.String(),
		qualificationReleaseV1TestHash("0"), fixture.canonicalHash, wia.userID, createdAt,
	); err != nil {
		fail("seed Qualification Release Canonical Receipt", err)
	}
	if err := transaction.QueryRowContext(ctx, `
SELECT 'sha256:'||encode(sha256(convert_to(release_delivery_canonical_json(
  jsonb_build_object(
    'schemaVersion','release-bundle/v1','id',$1::uuid::text,
    'projectId',$2::uuid::text,
    'workspace',jsonb_build_object(
      'workspaceArtifactId',$3::uuid::text,
      'workspaceRevisionId',$4::uuid::text,'workspaceContentHash',$5::text
    ),
    'canonicalReceipt',jsonb_build_object('id',$6::uuid::text,'contentHash',$7::text),
    'buildManifest',jsonb_build_object('id',$8::uuid::text,'contentHash',$9::text),
    'buildContract',jsonb_build_object('id',$10::uuid::text,'contentHash',$11::text),
    'fullStackTemplate',jsonb_build_object('id',$12::uuid::text,'contentHash',$13::text),
    'verificationProfile',jsonb_build_object(
      'id',$14::text,'version',1,'contentHash',$15::text
    ),
    'releaseArtifacts',$16::jsonb,'bundleHash','',
    'createdBy',$17::uuid::text,
    'createdAt',release_delivery_rfc3339_microsecond($18::timestamptz)
  )
), 'UTF8')),'hex')`, fixture.bundleID, wia.projectID,
		wia.targetArtifactID, wia.targetRevisionID, workspaceHash,
		fixture.canonicalID, fixture.canonicalHash, wia.buildManifestID,
		manifestHash, wia.buildContractID, contractHash, fullStackID,
		fullStackHash, profileID, profileHash, fixture.releaseArtifacts,
		wia.userID, createdAt,
	).Scan(&fixture.bundleHash); err != nil {
		fail("hash Qualification Release Bundle", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_bundles(
  id,schema_version,project_id,workspace_artifact_id,workspace_revision_id,
  workspace_content_hash,canonical_receipt_id,canonical_receipt_hash,
  release_artifacts,content_store,content_ref,content_hash,bundle_hash,
  created_by,created_at
) VALUES (
  $1,'release-bundle/v1',$2,$3,$4,$5,$6,$7,$8::jsonb,'blob',$9,$10,$11,$12,$13
)`, fixture.bundleID, wia.projectID, wia.targetArtifactID, wia.targetRevisionID,
		workspaceHash, fixture.canonicalID, fixture.canonicalHash, fixture.releaseArtifacts,
		"blob://qualification-release/bundle/"+fixture.bundleID.String(),
		qualificationReleaseV1TestHash("1"), fixture.bundleHash, wia.userID, createdAt,
	); err != nil {
		fail("seed Qualification Release Bundle", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_runs(
  id,schema_version,project_id,release_bundle_id,release_bundle_hash,
  request_key,request_hash,reason,state,version,fence_epoch,started_at,
  finished_at,created_by,updated_by,created_at,updated_at
) VALUES (
  $1,'release-preview-run/v2',$2,$3,$4,$5,$6,
  'qualification release preview','passed',2,1,$7,$7,$8,$8,$7,$7
)`, previewRunID, wia.projectID, fixture.bundleID, fixture.bundleHash,
		"preview-"+previewRunID.String(), qualificationReleaseV1TestHash("2"),
		createdAt, wia.userID,
	); err != nil {
		fail("seed Qualification Release Preview Run", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_delivery_operations(
  id,schema_version,project_id,kind,preview_run_id,deployment_run_id,
  request_schema_version,request_document,request_hash,
  controller_schema_version,controller_id,controller_version,
  controller_protocol,controller_trust_key_digest,remote_state,
  submit_attempt_count,reconcile_attempt_count,next_attempt_at,
  first_submitted_at,last_attempt_at,last_observation_sequence,last_observed_at,
  terminal_result_hash,created_by,created_at,updated_at
) VALUES (
  $1,'release-delivery-operation/v1',$2,'preview',$3,NULL,
  'release-delivery-operation-request/v3','{}',$4,
  'release-delivery-controller-identity/v1','preview-controller','1',
  'worksflow.release-delivery/v3',$5,'completed',1,0,NULL,$6,$6,1,$6,$7,$8,$6,$6
)`, previewOperationID, wia.projectID, previewRunID,
		qualificationReleaseV1TestHash("3"), qualificationReleaseV1TestHash("4"),
		createdAt, previewResultHash, wia.userID,
	); err != nil {
		fail("seed Qualification Release Preview Operation", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_delivery_operation_results(
  operation_id,schema_version,request_hash,project_id,kind,status,
  controller_schema_version,controller_id,controller_version,
  controller_protocol,controller_trust_key_digest,provider,provider_ref,
  public_url,checks,previous_head_id,previous_head_hash,no_mutation,
  rejection_code,rejection_detail,worker_id,fence_epoch,completed_at,
  result_document,result_hash,created_at
) VALUES (
  $1,'release-delivery-operation-result/v1',$2,$3,'preview','completed',
  'release-delivery-controller-identity/v1','preview-controller','1',
  'worksflow.release-delivery/v3',$4,'fixture','preview/ref',NULL,
  '[{"id":"preview","kind":"health","status":"passed"}]'::jsonb,
  NULL,NULL,false,NULL,NULL,'preview-worker',1,$5,'{}',$6,$5
)`, previewOperationID, qualificationReleaseV1TestHash("3"), wia.projectID,
		qualificationReleaseV1TestHash("4"), createdAt, previewResultHash,
	); err != nil {
		fail("seed Qualification Release Preview Result", err)
	}
	if err := transaction.QueryRowContext(ctx, `
SELECT 'sha256:'||encode(sha256(convert_to(release_delivery_canonical_json(
  jsonb_build_object(
    'schemaVersion','release-preview-receipt/v2','id',$1::uuid::text,
    'runId',$2::uuid::text,'projectId',$3::uuid::text,
    'releaseBundle',jsonb_build_object('id',$4::uuid::text,'contentHash',$5::text),
    'canonicalReceipt',jsonb_build_object('id',$6::uuid::text,'contentHash',$7::text),
    'workspace',jsonb_build_object(
      'workspaceArtifactId',$8::uuid::text,
      'workspaceRevisionId',$9::uuid::text,'workspaceContentHash',$10::text
    ),
    'releaseArtifacts',$11::jsonb,'namespace','qualification-release',
    'provider','fixture','providerRef','preview/ref',
    'checks','[{"id":"preview","kind":"health","status":"passed"}]'::jsonb,
    'decision','passed','controllerOperation',jsonb_build_object(
      'operationId',$12::uuid::text,'resultHash',$13::text
    ),'payloadHash','','createdBy',$14::uuid::text,
    'createdAt',release_delivery_rfc3339_microsecond($15::timestamptz)
  )
), 'UTF8')),'hex')`, fixture.previewID, previewRunID, wia.projectID,
		fixture.bundleID, fixture.bundleHash, fixture.canonicalID,
		fixture.canonicalHash, wia.targetArtifactID, wia.targetRevisionID,
		workspaceHash, fixture.releaseArtifacts, previewOperationID,
		previewResultHash, wia.userID, createdAt,
	).Scan(&fixture.previewHash); err != nil {
		fail("hash Qualification Release Preview Receipt", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_preview_receipts(
  id,schema_version,run_id,project_id,release_bundle_id,release_bundle_hash,
  canonical_receipt_id,canonical_receipt_hash,workspace_artifact_id,
  workspace_revision_id,workspace_content_hash,release_artifacts,namespace,
  provider,provider_ref,checks,decision,content_store,content_ref,content_hash,
  payload_hash,created_by,created_at,controller_operation_id,
  controller_result_hash
) VALUES (
  $1,'release-preview-receipt/v2',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,
  'qualification-release','fixture','preview/ref',
  '[{"id":"preview","kind":"health","status":"passed"}]'::jsonb,
  'passed','blob',$12,$13,$14,$15,$16,$17,$18
)`, fixture.previewID, previewRunID, wia.projectID, fixture.bundleID,
		fixture.bundleHash, fixture.canonicalID, fixture.canonicalHash,
		wia.targetArtifactID, wia.targetRevisionID, workspaceHash,
		fixture.releaseArtifacts,
		"blob://qualification-release/preview/"+fixture.previewID.String(),
		qualificationReleaseV1TestHash("6"), fixture.previewHash, wia.userID,
		createdAt, previewOperationID, previewResultHash,
	); err != nil {
		fail("seed Qualification Release Preview Receipt", err)
	}
	if err := transaction.QueryRowContext(ctx, `
SELECT 'sha256:'||encode(sha256(convert_to(release_delivery_canonical_json(
  jsonb_build_object(
    'schemaVersion','release-promotion-approval/v1','id',$1::uuid::text,
    'projectId',$2::uuid::text,
    'releaseBundle',jsonb_build_object('id',$3::uuid::text,'contentHash',$4::text),
    'previewReceipt',jsonb_build_object('id',$5::uuid::text,'contentHash',$6::text),
    'reason','qualified release approved','payloadHash','',
    'createdBy',$7::uuid::text,
    'createdAt',release_delivery_rfc3339_microsecond($8::timestamptz)
  )
), 'UTF8')),'hex')`, fixture.approvalID, wia.projectID,
		fixture.bundleID, fixture.bundleHash, fixture.previewID,
		fixture.previewHash, wia.userID, createdAt,
	).Scan(&fixture.approvalHash); err != nil {
		fail("hash Qualification Release Promotion Approval", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_promotion_approvals(
  id,schema_version,project_id,release_bundle_id,release_bundle_hash,
  preview_receipt_id,preview_receipt_hash,reason,content_store,content_ref,
  content_hash,payload_hash,created_by,created_at
) VALUES (
  $1,'release-promotion-approval/v1',$2,$3,$4,$5,$6,
  'qualified release approved','blob',$7,$8,$9,$10,$11
)`, fixture.approvalID, wia.projectID, fixture.bundleID, fixture.bundleHash,
		fixture.previewID, fixture.previewHash,
		"blob://qualification-release/approval/"+fixture.approvalID.String(),
		qualificationReleaseV1TestHash("7"), fixture.approvalHash, wia.userID, createdAt,
	); err != nil {
		fail("seed Qualification Release Promotion Approval", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit Qualification Release authority fixture: %v", err)
	}
	return fixture
}

func seedQualificationReleaseV1HealthyControllerResult(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	authorizationID uuid.UUID,
	productionRunID uuid.UUID,
	release qualificationReleaseV1AuthorityFixture,
) {
	t.Helper()
	var requestHash, controllerSchema, controllerID, controllerVersion string
	var controllerProtocol, controllerTrust string
	if err := database.QueryRowContext(ctx, `
SELECT operation.request_hash,operation.controller_schema_version,
       operation.controller_id,operation.controller_version,
       operation.controller_protocol,operation.controller_trust_key_digest
FROM qualification_release_v1_controller_bindings AS binding
JOIN release_delivery_operations AS operation
  ON operation.id=binding.controller_operation_id
WHERE binding.authorization_id=$1 AND binding.production_run_id=$2`,
		authorizationID, productionRunID,
	).Scan(&requestHash, &controllerSchema, &controllerID, &controllerVersion,
		&controllerProtocol, &controllerTrust); err != nil {
		t.Fatalf("read qualified Controller binding: %v", err)
	}
	resultHash := "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	receiptID, revisionID := uuid.New(), uuid.New()
	receiptHash := qualificationReleaseV1TestHash("b")
	revisionHash := qualificationReleaseV1TestHash("c")
	completedAt := time.Now().UTC().Truncate(time.Millisecond)
	publicURL := "https://qualification-release.example.test/" + revisionID.String()
	checks := `[{"id":"readiness","kind":"health","status":"passed"},{"id":"rollout","kind":"rollout","status":"passed"}]`

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	fail := func(label string, execErr error) {
		_ = transaction.Rollback()
		t.Fatalf("%s: %v", label, execErr)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		fail("disable terminal Controller fixture triggers", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_delivery_operation_results(
  operation_id,schema_version,request_hash,project_id,kind,status,
  controller_schema_version,controller_id,controller_version,
  controller_protocol,controller_trust_key_digest,provider,provider_ref,
  public_url,checks,previous_head_id,previous_head_hash,no_mutation,
  rejection_code,rejection_detail,worker_id,fence_epoch,completed_at,
  result_document,result_hash,created_at
) VALUES (
  $1,'release-delivery-operation-result/v1',$2,$3,'production','completed',
  $4,$5,$6,$7,$8,'fixture','production/ref',$9,$10::jsonb,
  NULL,NULL,false,NULL,NULL,'qualification-release-controller',1,$11,
  '{}',$12,$11
)`, productionRunID, requestHash, wia.projectID, controllerSchema,
		controllerID, controllerVersion, controllerProtocol, controllerTrust,
		publicURL, checks, completedAt, resultHash,
	); err != nil {
		fail("seed qualified Controller terminal Result", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE release_delivery_operations
SET remote_state='completed',submit_attempt_count=1,
    first_submitted_at=$2,last_attempt_at=$2,last_observation_sequence=1,
    last_observed_at=$2,terminal_result_hash=$3,next_attempt_at=NULL,
    updated_at=$2
WHERE id=$1`, productionRunID, completedAt, resultHash); err != nil {
		fail("terminalize qualified Controller Operation", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE release_deployment_runs
SET state='healthy',version=version+1,finished_at=$2,
    lease_worker_id=NULL,lease_epoch=NULL,lease_expires_at=NULL,
    updated_by=$3,updated_at=$2
WHERE id=$1`, productionRunID, completedAt, wia.userID); err != nil {
		fail("terminalize qualified Production Run", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_production_receipts(
  id,schema_version,run_id,project_id,operation,release_bundle_id,
  release_bundle_hash,preview_receipt_id,preview_receipt_hash,
  promotion_approval_id,promotion_approval_hash,source_revision_id,
  source_revision_hash,provider,provider_ref,public_url,checks,decision,
  content_store,content_ref,content_hash,payload_hash,created_by,created_at,
  controller_operation_id,controller_result_hash
) VALUES (
  $1,'release-production-receipt/v2',$2,$3,'promote',$4,$5,$6,$7,$8,$9,
  NULL,NULL,'fixture','production/ref',$10,$11::jsonb,'passed','blob',$12,
  $13,$14,$15,$16,$2,$17
)`, receiptID, productionRunID, wia.projectID, release.bundleID,
		release.bundleHash, release.previewID, release.previewHash,
		release.approvalID, release.approvalHash, publicURL, checks,
		"blob://qualification-release/production-receipt/"+receiptID.String(),
		qualificationReleaseV1TestHash("d"), receiptHash, wia.userID,
		completedAt, resultHash,
	); err != nil {
		fail("seed qualified Production Receipt", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_deployment_revisions(
  id,schema_version,run_id,project_id,operation,release_bundle_id,
  release_bundle_hash,preview_receipt_id,preview_receipt_hash,
  promotion_approval_id,promotion_approval_hash,production_receipt_id,
  production_receipt_hash,source_revision_id,source_revision_hash,provider,
  provider_ref,public_url,checks,content_store,content_ref,content_hash,
  payload_hash,created_by,created_at,controller_operation_id,
  controller_result_hash
) VALUES (
  $1,'release-deployment-revision/v2',$2,$3,'promote',$4,$5,$6,$7,$8,$9,
  $10,$11,NULL,NULL,'fixture','production/ref',$12,$13::jsonb,'blob',$14,
  $15,$16,$17,$18,$2,$19
)`, revisionID, productionRunID, wia.projectID, release.bundleID,
		release.bundleHash, release.previewID, release.previewHash,
		release.approvalID, release.approvalHash, receiptID, receiptHash,
		publicURL, checks,
		"blob://qualification-release/deployment-revision/"+revisionID.String(),
		qualificationReleaseV1TestHash("e"), revisionHash, wia.userID,
		completedAt, resultHash,
	); err != nil {
		fail("seed qualified Deployment Revision", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit healthy qualified Controller result: %v", err)
	}
}

func seedQualificationReleaseV1ControllerFailure(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture qualificationReleaseV1FailureFixture,
	outcome string,
) {
	t.Helper()
	var requestHash, controllerSchema, controllerID, controllerVersion string
	var controllerProtocol, controllerTrust string
	if err := database.QueryRowContext(ctx, `
SELECT operation.request_hash,operation.controller_schema_version,
       operation.controller_id,operation.controller_version,
       operation.controller_protocol,operation.controller_trust_key_digest
FROM qualification_release_v1_controller_bindings AS binding
JOIN release_delivery_operations AS operation
  ON operation.id=binding.controller_operation_id
WHERE binding.authorization_id=$1 AND binding.production_run_id=$2`,
		fixture.authorizationID, fixture.productionRunID,
	).Scan(&requestHash, &controllerSchema, &controllerID, &controllerVersion,
		&controllerProtocol, &controllerTrust); err != nil {
		t.Fatalf("read failure-canary Controller binding: %v", err)
	}
	resultHash := "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	completedAt := time.Now().UTC().Truncate(time.Millisecond)
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	fail := func(label string, execErr error) {
		_ = transaction.Rollback()
		t.Fatalf("%s: %v", label, execErr)
	}
	if outcome != "pre_submit_cancelled" {
		if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
			fail("disable failure-canary Controller fixture triggers", err)
		}
	}
	if outcome == "production_failed" {
		publicURL := "https://qualification-release-failed.example.test/" + fixture.productionRunID.String()
		checks := `[{"id":"readiness","kind":"health","status":"failed"}]`
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_delivery_operation_results(
  operation_id,schema_version,request_hash,project_id,kind,status,
  controller_schema_version,controller_id,controller_version,
  controller_protocol,controller_trust_key_digest,provider,provider_ref,
  public_url,checks,previous_head_id,previous_head_hash,no_mutation,
  rejection_code,rejection_detail,worker_id,fence_epoch,completed_at,
  result_document,result_hash,created_at
) VALUES (
  $1,'release-delivery-operation-result/v1',$2,$3,'production','completed',
  $4,$5,$6,$7,$8,'fixture','production/failed',$9,$10::jsonb,
  NULL,NULL,false,NULL,NULL,'qualification-release-controller',1,$11,
  '{}',$12,$11
)`, fixture.productionRunID, requestHash, fixture.wia.projectID,
			controllerSchema, controllerID, controllerVersion, controllerProtocol,
			controllerTrust, publicURL, checks, completedAt, resultHash,
		); err != nil {
			fail("seed failed Controller Result", err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE release_delivery_operations
SET remote_state='completed',submit_attempt_count=1,
    first_submitted_at=$2,last_attempt_at=$2,last_observation_sequence=1,
    last_observed_at=$2,terminal_result_hash=$3,next_attempt_at=NULL,
    updated_at=$2
WHERE id=$1`, fixture.productionRunID, completedAt, resultHash); err != nil {
			fail("terminalize failed Controller Operation", err)
		}
		receiptID := uuid.New()
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_production_receipts(
  id,schema_version,run_id,project_id,operation,release_bundle_id,
  release_bundle_hash,preview_receipt_id,preview_receipt_hash,
  promotion_approval_id,promotion_approval_hash,source_revision_id,
  source_revision_hash,provider,provider_ref,public_url,checks,decision,
  content_store,content_ref,content_hash,payload_hash,created_by,created_at,
  controller_operation_id,controller_result_hash
) VALUES (
  $1,'release-production-receipt/v2',$2,$3,'promote',$4,$5,$6,$7,$8,$9,
  NULL,NULL,'fixture','production/failed',$10,$11::jsonb,'failed','blob',$12,
  $13,$14,$15,$16,$2,$17
)`, receiptID, fixture.productionRunID, fixture.wia.projectID,
			fixture.release.bundleID, fixture.release.bundleHash,
			fixture.release.previewID, fixture.release.previewHash,
			fixture.release.approvalID, fixture.release.approvalHash, publicURL,
			checks,
			"blob://qualification-release/failed-receipt/"+receiptID.String(),
			qualificationReleaseV1TestHash("f"),
			qualificationReleaseV1TestHash("a"), fixture.wia.userID,
			completedAt, resultHash,
		); err != nil {
			fail("seed failed Production Receipt", err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE release_deployment_runs
SET state='failed',version=version+1,finished_at=$2,
    lease_worker_id=NULL,lease_epoch=NULL,lease_expires_at=NULL,
    updated_by=$3,updated_at=$2
WHERE id=$1`, fixture.productionRunID, completedAt, fixture.wia.userID); err != nil {
			fail("terminalize failed Production Run", err)
		}
	} else if outcome == "controller_rejected" {
		if _, err := transaction.ExecContext(ctx, `
INSERT INTO release_delivery_operation_results(
  operation_id,schema_version,request_hash,project_id,kind,status,
  controller_schema_version,controller_id,controller_version,
  controller_protocol,controller_trust_key_digest,provider,provider_ref,
  public_url,checks,previous_head_id,previous_head_hash,no_mutation,
  rejection_code,rejection_detail,worker_id,fence_epoch,completed_at,
  result_document,result_hash,created_at
) VALUES (
  $1,'release-delivery-operation-result/v1',$2,$3,'production','rejected',
  $4,$5,$6,$7,$8,NULL,NULL,NULL,'[]'::jsonb,NULL,NULL,true,
  'controller_rejected','fixture rejection detail',
  'qualification-release-controller',1,$9,'{}',$10,$9
)`, fixture.productionRunID, requestHash, fixture.wia.projectID,
			controllerSchema, controllerID, controllerVersion, controllerProtocol,
			controllerTrust, completedAt, resultHash,
		); err != nil {
			fail("seed rejected Controller Result", err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE release_delivery_operations
SET remote_state='rejected',submit_attempt_count=1,
    first_submitted_at=$2,last_attempt_at=$2,last_observation_sequence=1,
    last_observed_at=$2,terminal_result_hash=$3,next_attempt_at=NULL,
    updated_at=$2
WHERE id=$1`, fixture.productionRunID, completedAt, resultHash); err != nil {
			fail("terminalize rejected Controller Operation", err)
		}
		if _, err := transaction.ExecContext(ctx, `
UPDATE release_deployment_runs
SET state='error',version=version+1,finished_at=$2,
    lease_worker_id=NULL,lease_epoch=NULL,lease_expires_at=NULL,
    updated_by=$3,updated_at=$2
WHERE id=$1`, fixture.productionRunID, completedAt, fixture.wia.userID); err != nil {
			fail("terminalize rejected Production Run", err)
		}
	} else if outcome == "pre_submit_cancelled" {
		if _, err := transaction.ExecContext(ctx, `
UPDATE release_deployment_runs
SET state='cancelled',version=version+1,finished_at=$2,
    lease_worker_id=NULL,lease_epoch=NULL,lease_expires_at=NULL,
    updated_by=$3,updated_at=$2
WHERE id=$1 AND state='queued'`, fixture.productionRunID, completedAt,
			fixture.wia.userID); err != nil {
			fail("cancel pre-submit Production Run", err)
		}
		var state string
		if err := transaction.QueryRowContext(ctx, `
SELECT state FROM release_deployment_runs WHERE id=$1`, fixture.productionRunID,
		).Scan(&state); err != nil {
			fail("read cancelled Production Run", err)
		}
		if state != "cancelled" {
			fail("cancel pre-submit Production Run", fmt.Errorf("state=%q", state))
		}
	} else {
		fail("select failure outcome", fmt.Errorf("unsupported outcome %q", outcome))
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit %s qualified Controller failure: %v", outcome, err)
	}
}

func assertQualificationReleaseV1FailureClosure(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture qualificationReleaseV1FailureFixture,
	outcome string,
) {
	t.Helper()
	var authorizationExact, bindingExact, failureExact, chainExact, terminalExact bool
	var runStatus, nodeStatus, storedOutcome, failureCode string
	var resultCount, runCount, operationCount, grantCount, eventCount, outboxCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  qualification_release_v1_authorization_is_exact($1),
  qualification_release_v1_controller_binding_is_exact($1),
  qualification_release_v1_failure_is_exact($1),
  qualification_release_v1_lease_claim_chain_is_exact($1,2),
  qualification_release_v1_failure_completion_is_exact($1),
  (SELECT status FROM workflow_runs WHERE id=$2),
  (SELECT status FROM workflow_node_runs WHERE id=$3),
  (SELECT outcome FROM qualification_release_v1_results
   WHERE authorization_id=$1),
  (SELECT failure_document->>'code' FROM qualification_release_v1_results
   WHERE authorization_id=$1),
  (SELECT count(*) FROM qualification_release_v1_results
   WHERE authorization_id=$1),
  (SELECT count(*) FROM release_deployment_runs AS production_run
   JOIN qualification_release_v1_controller_bindings AS binding
     ON binding.production_run_id=production_run.id
   WHERE binding.authorization_id=$1),
  (SELECT count(*) FROM release_delivery_operations AS operation
   JOIN qualification_release_v1_controller_bindings AS binding
     ON binding.controller_operation_id=operation.id
   WHERE binding.authorization_id=$1),
  (SELECT count(*) FROM qualification_release_v1_transaction_grants
   WHERE authorization_id=$1),
  (SELECT count(*) FROM workflow_run_events
   WHERE id IN (
     SELECT action_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT completion_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT claim_event_id FROM qualification_release_v1_lease_claims
     WHERE authorization_id=$1
   )),
  (SELECT count(*) FROM outbox_events
   WHERE id IN (
     SELECT action_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT completion_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT claim_event_id FROM qualification_release_v1_lease_claims
     WHERE authorization_id=$1
   ))`, fixture.authorizationID, fixture.wia.runID, fixture.publishNodeID,
	).Scan(
		&authorizationExact, &bindingExact, &failureExact, &chainExact,
		&terminalExact, &runStatus, &nodeStatus, &storedOutcome, &failureCode,
		&resultCount, &runCount, &operationCount, &grantCount, &eventCount,
		&outboxCount,
	); err != nil {
		t.Fatal(err)
	}
	wantCode := "release_controller_rejected"
	if outcome == "production_failed" {
		wantCode = "release_production_checks_failed"
	} else if outcome == "pre_submit_cancelled" {
		wantCode = "release_cancelled_before_submission"
	}
	if !authorizationExact || !bindingExact || !failureExact || !chainExact ||
		!terminalExact || runStatus != "failed" || nodeStatus != "failed" ||
		storedOutcome != outcome || failureCode != wantCode || resultCount != 1 ||
		runCount != 1 || operationCount != 1 || grantCount != 0 ||
		eventCount != 4 || outboxCount != 4 {
		t.Fatalf("Qualification Release failure closure exact=%t/%t/%t/%t/%t statuses=%q/%q outcome/code=%q/%q counts=%d/%d/%d/%d/%d/%d",
			authorizationExact, bindingExact, failureExact, chainExact,
			terminalExact, runStatus, nodeStatus, storedOutcome, failureCode,
			resultCount, runCount, operationCount, grantCount, eventCount,
			outboxCount)
	}
	var inspection []byte
	if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_release_failure_v1($1) AS value`,
		fixture.authorizationID,
	).Scan(&inspection); err != nil || len(inspection) == 0 {
		t.Fatalf("inspect immutable Qualification Release failure bytes=%d error=%v",
			len(inspection), err)
	}
	if outcome == "pre_submit_cancelled" {
		var resultHashIsNull bool
		var attempts, controllerResults, receipts, revisions int
		if err := database.QueryRowContext(ctx, `
SELECT
  release_result.controller_result_hash IS NULL,
  (SELECT count(*) FROM release_delivery_operation_attempts
   WHERE operation_id=release_result.controller_operation_id),
  (SELECT count(*) FROM release_delivery_operation_results
   WHERE operation_id=release_result.controller_operation_id),
  (SELECT count(*) FROM release_production_receipts
   WHERE controller_operation_id=release_result.controller_operation_id),
  (SELECT count(*) FROM release_deployment_revisions
   WHERE controller_operation_id=release_result.controller_operation_id)
FROM qualification_release_v1_results AS release_result
WHERE release_result.authorization_id=$1`, fixture.authorizationID).Scan(
			&resultHashIsNull, &attempts, &controllerResults, &receipts, &revisions,
		); err != nil {
			t.Fatal(err)
		}
		if !resultHashIsNull || attempts != 0 || controllerResults != 0 ||
			receipts != 0 || revisions != 0 {
			t.Fatalf("pre-submit cancellation evidence result-hash-null=%t attempts/results/receipts/revisions=%d/%d/%d/%d",
				resultHashIsNull, attempts, controllerResults, receipts, revisions)
		}
	}
}

func assertQualificationReleaseV1Closure(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	authorizationID uuid.UUID,
	workflowRunID uuid.UUID,
	publishNodeRunID uuid.UUID,
	actionEventID uuid.UUID,
) {
	t.Helper()
	var authorizationExact, bindingExact, resultExact, claimChainExact, completionExact bool
	var runStatus, nodeStatus, publicURL string
	var identityCount, claimCount, grantCount, eventCount, outboxCount int
	if err := database.QueryRowContext(ctx, `
SELECT
  qualification_release_v1_authorization_is_exact($1),
  qualification_release_v1_controller_binding_is_exact($1),
  qualification_release_v1_result_is_exact($1),
  qualification_release_v1_lease_claim_chain_is_exact($1,2),
  qualification_release_v1_completion_is_exact($1),
  (SELECT status FROM workflow_runs WHERE id=$2),
  (SELECT status FROM workflow_node_runs WHERE id=$3),
  (SELECT run.context #>> ARRAY[
     'nodes',release_authorization.publish_node_key,'output','url'
   ]
   FROM workflow_runs AS run
   JOIN qualification_release_v1_authorizations AS release_authorization
     ON release_authorization.workflow_run_id=run.id
   WHERE run.id=$2 AND release_authorization.authorization_id=$1),
  (SELECT count(*) FROM qualification_release_v1_identity_reservations
   WHERE owner_authorization_id=$1),
  (SELECT count(*) FROM qualification_release_v1_lease_claims
   WHERE authorization_id=$1),
  (SELECT count(*) FROM qualification_release_v1_transaction_grants
   WHERE authorization_id=$1),
  (SELECT count(*) FROM workflow_run_events
   WHERE id IN (
     SELECT action_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT completion_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT claim_event_id FROM qualification_release_v1_lease_claims
     WHERE authorization_id=$1
   )),
  (SELECT count(*) FROM outbox_events
   WHERE id IN (
     SELECT action_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT completion_event_id FROM qualification_release_v1_authorizations
     WHERE authorization_id=$1
     UNION ALL
     SELECT claim_event_id FROM qualification_release_v1_lease_claims
     WHERE authorization_id=$1
   ))`, authorizationID, workflowRunID, publishNodeRunID,
	).Scan(
		&authorizationExact, &bindingExact, &resultExact, &claimChainExact,
		&completionExact, &runStatus, &nodeStatus, &publicURL, &identityCount,
		&claimCount, &grantCount,
		&eventCount, &outboxCount,
	); err != nil {
		t.Fatal(err)
	}
	if !authorizationExact || !bindingExact || !resultExact || !claimChainExact ||
		!completionExact ||
		runStatus != "completed" || nodeStatus != "completed" || publicURL == "" ||
		identityCount != 5 || claimCount != 2 || grantCount != 0 ||
		eventCount != 4 || outboxCount != 4 {
		t.Fatalf("Qualification Release closure exact=%t/%t/%t/%t/%t statuses=%q/%q url=%q identities/claims/grants/events/outbox=%d/%d/%d/%d/%d",
			authorizationExact, bindingExact, resultExact, claimChainExact,
			completionExact, runStatus, nodeStatus, publicURL, identityCount, claimCount, grantCount,
			eventCount, outboxCount)
	}
	var operationInspection []byte
	if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_release_operation_v1($1) AS value`,
		actionEventID,
	).Scan(&operationInspection); err == nil {
		t.Fatal("operation inspection accepted an Action Event identity")
	}
}

func applyQualificationReleaseV1(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	up, err := files.ReadFile(qualificationReleaseV1Migration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply Qualification Release v1 migration: %v", err)
	}
}

func applyCanonicalReviewForwardEquivalenceForRelease(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	up, err := files.ReadFile(canonicalReviewForwardEquivalenceUp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply owner-executed Canonical Review 83 bridge: %v", err)
	}
}

func applyQualificationReleaseOwnerPrefixThrough83(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	applyQualificationReleaseOwnerPrefixBefore(
		t, ctx, database, qualificationReleaseV1Migration,
	)
}

func applyQualificationReleaseOwnerPrefixBefore(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	boundary string,
) {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `CREATE TABLE schema_migrations (
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
		if name >= boundary {
			break
		}
		if err := applyFile(ctx, connection, name); err != nil {
			t.Fatalf("owner apply Qualification Release prerequisite %s: %v", name, err)
		}
	}
}

func ensureQualificationReleaseHardenedRoles(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:test:qualification-release:hardened-roles',0
  )
)`); err != nil {
		t.Fatalf("lock hardened Qualification Release role bootstrap: %v", err)
	}
	defer func() {
		_, _ = connection.ExecContext(context.Background(), `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:test:qualification-release:hardened-roles',0
  )
)`)
	}()
	roles := []string{
		"worksflow_application",
		"worksflow_auditor",
		"worksflow_golden_fault_operator",
		"worksflow_migration_owner",
		"worksflow_qualification_credential_resolver_operator",
		"worksflow_qualification_evidence_operator",
		"worksflow_qualification_handoff_operator",
		"worksflow_qualification_input_precommit_operator",
		"worksflow_qualification_plan_operator",
		"worksflow_qualification_policy_operator",
		"worksflow_qualification_promotion_operator",
		"worksflow_qualification_receipt_operator",
		"worksflow_qualification_release_operator",
		"worksflow_qualification_source_verifier_operator",
		"worksflow_repository_index_gc_operator",
		"worksflow_schema_migrator",
		"worksflow_workflow_input_authority_operator",
	}
	for _, role := range roles {
		var exists bool
		if err := connection.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1
)`, role).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			query := "CREATE ROLE " + role +
				" NOLOGIN INHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION"
			if _, err := connection.ExecContext(ctx, query); err != nil {
				t.Fatalf("create hardened role %s: %v", role, err)
			}
		}
	}
	var exactCount, outgoingMemberships, administrativeMemberships int
	if err := connection.QueryRowContext(ctx, `
WITH selected AS (
  SELECT * FROM pg_catalog.pg_roles WHERE rolname=ANY($1::text[])
)
SELECT
  (SELECT count(*) FROM selected AS role
    WHERE NOT role.rolcanlogin AND role.rolinherit
      AND NOT (role.rolsuper OR role.rolbypassrls OR role.rolcreatedb
        OR role.rolcreaterole OR role.rolreplication)
  ),
  (SELECT count(*) FROM pg_catalog.pg_auth_members AS membership
   WHERE membership.member IN (SELECT oid FROM selected)),
  (SELECT count(*) FROM pg_catalog.pg_auth_members AS membership
   WHERE membership.roleid IN (SELECT oid FROM selected)
     AND membership.admin_option)
`, roles).Scan(
		&exactCount, &outgoingMemberships, &administrativeMemberships,
	); err != nil {
		t.Fatal(err)
	}
	if exactCount != len(roles) || outgoingMemberships != 0 || administrativeMemberships != 0 {
		t.Fatalf("hardened roles exact/outgoing/admin=%d/%d/%d, want %d/0/0",
			exactCount, outgoingMemberships, administrativeMemberships, len(roles))
	}
	var exact, canOwn bool
	if err := connection.QueryRowContext(ctx, `
SELECT
  NOT role.rolcanlogin AND role.rolinherit
    AND NOT (role.rolsuper OR role.rolbypassrls OR role.rolcreatedb
      OR role.rolcreaterole OR role.rolreplication),
  session_role.rolsuper OR pg_catalog.pg_has_role(
    current_user,'worksflow_migration_owner','MEMBER'
  )
FROM pg_catalog.pg_roles AS role
CROSS JOIN pg_catalog.pg_roles AS session_role
WHERE role.rolname='worksflow_migration_owner'
  AND session_role.rolname=current_user`).Scan(&exact, &canOwn); err != nil {
		t.Fatal(err)
	}
	if !exact || !canOwn {
		t.Fatalf("hardened migration-owner role exact/assignable=%t/%t", exact, canOwn)
	}
}

type qualificationReleaseUpstreamACL struct {
	Functions      int
	MigrationOwner int
	Application    int
	Public         int
}

func qualificationReleaseOperatorSchemaUsage(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) bool {
	t.Helper()
	var usage bool
	if err := database.QueryRowContext(ctx, `
SELECT pg_catalog.has_schema_privilege(
  'worksflow_qualification_release_operator',
  pg_catalog.current_schema(),
  'USAGE'
)`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func qualificationReleaseOperatorSchemaUsageMarked(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) bool {
	t.Helper()
	var marked bool
	if err := database.QueryRowContext(ctx, `
SELECT COALESCE(
  pg_catalog.obj_description(
    pg_catalog.to_regprocedure(pg_catalog.format(
      '%I.qualification_release_v1_runtime_caller_is_exact()',
      pg_catalog.current_schema()
    ))::oid,
    'pg_proc'
  )='000084 granted schema USAGE to worksflow_qualification_release_operator',
  false
)`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	return marked
}

func qualificationReleaseUpstreamHelperACL(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) qualificationReleaseUpstreamACL {
	t.Helper()
	var result qualificationReleaseUpstreamACL
	if err := database.QueryRowContext(ctx, `
WITH expected(signature) AS (
  VALUES
    ('release_delivery_canonical_json(jsonb)'),
    ('release_delivery_embedded_hash_is_exact(jsonb,text)'),
    ('release_delivery_rfc3339_microsecond(timestamptz)')
), helper AS (
  SELECT pg_catalog.to_regprocedure(
    pg_catalog.format('%I.%s',pg_catalog.current_schema(),signature)
  ) AS oid
  FROM expected
), migration_owner AS (
  SELECT oid FROM pg_catalog.pg_roles
  WHERE rolname='worksflow_migration_owner'
), application_role AS (
  SELECT oid FROM pg_catalog.pg_roles
  WHERE rolname='worksflow_application'
)
SELECT
  (SELECT pg_catalog.count(*) FROM helper WHERE oid IS NOT NULL),
  (SELECT pg_catalog.count(*) FROM helper
   CROSS JOIN migration_owner AS role
   WHERE helper.oid IS NOT NULL AND pg_catalog.has_function_privilege(
     role.oid,helper.oid,'EXECUTE'
   )),
  (SELECT pg_catalog.count(*) FROM helper
   CROSS JOIN application_role AS role
   WHERE helper.oid IS NOT NULL AND pg_catalog.has_function_privilege(
     role.oid,helper.oid,'EXECUTE'
   )),
  (SELECT pg_catalog.count(DISTINCT helper.oid)
   FROM helper
   JOIN pg_catalog.pg_proc AS procedure ON procedure.oid=helper.oid
   CROSS JOIN LATERAL pg_catalog.aclexplode(
     COALESCE(
       procedure.proacl,
       pg_catalog.acldefault('f',procedure.proowner)
     )
   ) AS privilege
   WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE')`,
	).Scan(
		&result.Functions, &result.MigrationOwner,
		&result.Application, &result.Public,
	); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertQualificationReleaseModernUpstreamHelpers(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	var functions, ownerExecute, publicExecute int
	if err := database.QueryRowContext(ctx, `
WITH expected(signature) AS (
  VALUES
    ('qualification_handoff_v1_completion_is_exact(uuid)'),
    ('qualification_input_precommit_caller_is_v1(text)'),
    ('workflow_execution_profile_v3_definition_is_database_admissible(jsonb)'),
    ('workflow_input_canonical_jsonb_bytes(jsonb)'),
    ('workflow_input_normalize_sha256(text)'),
    ('workflow_input_uuid_is_exact(text)')
), helper AS (
  SELECT pg_catalog.to_regprocedure(
    pg_catalog.format('%I.%s',pg_catalog.current_schema(),signature)
  ) AS oid
  FROM expected
), migration_owner AS (
  SELECT oid FROM pg_catalog.pg_roles
  WHERE rolname='worksflow_migration_owner'
)
SELECT
  (SELECT pg_catalog.count(*) FROM helper WHERE oid IS NOT NULL),
  (SELECT pg_catalog.count(*) FROM helper
   CROSS JOIN migration_owner AS role
   WHERE helper.oid IS NOT NULL AND pg_catalog.has_function_privilege(
     role.oid,helper.oid,'EXECUTE'
   )),
  (SELECT pg_catalog.count(DISTINCT helper.oid)
   FROM helper
   JOIN pg_catalog.pg_proc AS procedure ON procedure.oid=helper.oid
   CROSS JOIN LATERAL pg_catalog.aclexplode(
     COALESCE(
       procedure.proacl,
       pg_catalog.acldefault('f',procedure.proowner)
     )
   ) AS privilege
   WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE')`,
	).Scan(&functions, &ownerExecute, &publicExecute); err != nil {
		t.Fatal(err)
	}
	if functions != 6 || ownerExecute != 6 || publicExecute != 0 {
		t.Fatalf("Qualification Release modern-upstream functions/owner/public=%d/%d/%d, want 6/6/0",
			functions, ownerExecute, publicExecute)
	}
}

func assertQualificationReleaseCatalog(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var tables, indexes, triggers, functions, definers int
	var guardSearchPath, startHardenedOwner bool
	if err := database.QueryRowContext(ctx, `
WITH private_tables AS (
  SELECT oid FROM pg_catalog.pg_class
  WHERE relnamespace=pg_catalog.current_schema()::regnamespace
    AND relkind='r' AND relname LIKE 'qualification_release_v1_%'
), release_functions AS (
  SELECT prosecdef FROM pg_catalog.pg_proc
  WHERE pronamespace=pg_catalog.current_schema()::regnamespace
    AND (proname LIKE 'qualification_release_v1_%'
      OR proname IN (
        'bootstrap_qualification_release_controller_v1',
        'authorize_qualification_release_v1',
        'claim_qualification_release_publish_v1',
        'inspect_qualification_release_publish_claim_v1',
        'renew_qualification_release_publish_lease_v1',
        'start_qualification_release_controller_v1',
		'record_qualification_release_result_v1',
		'apply_qualification_release_result_v1',
		'record_qualification_release_failure_v1',
		'apply_qualification_release_failure_v1',
        'inspect_qualification_release_controller_bootstrap_v1',
        'inspect_qualification_release_operation_v1',
        'resolve_qualification_release_authorization_v1',
        'resolve_qualification_release_for_publish_v1',
        'inspect_qualification_release_controller_v1',
		'inspect_qualification_release_result_v1',
		'inspect_qualification_release_failure_v1',
        'guard_qualification_release_v1_workflow_transition',
        'guard_qualification_release_v1_event_mutation',
        'validate_qualification_release_v1_closure',
        'reject_qualification_release_v1_mutation'
      ))
)
SELECT
  (SELECT count(*) FROM private_tables),
  (SELECT count(*) FROM pg_catalog.pg_index
   WHERE indrelid IN (SELECT oid FROM private_tables)),
  (SELECT count(*) FROM pg_catalog.pg_trigger AS trigger
   JOIN pg_catalog.pg_class AS relation ON relation.oid=trigger.tgrelid
   WHERE relation.relnamespace=pg_catalog.current_schema()::regnamespace
     AND NOT trigger.tgisinternal
     AND trigger.tgname LIKE 'qualification_release%'),
  (SELECT count(*) FROM release_functions),
  (SELECT count(*) FILTER (WHERE prosecdef) FROM release_functions),
  COALESCE((
    SELECT procedure.proconfig @> ARRAY[
      'search_path=pg_catalog, '||pg_catalog.current_schema()
    ]
    FROM pg_catalog.pg_proc AS procedure
    WHERE procedure.pronamespace=pg_catalog.current_schema()::regnamespace
      AND procedure.proname='guard_workflow_execution_profile_v3_run'
  ),false),
  COALESCE((
    SELECT procedure.prosecdef
      AND owner.rolname='worksflow_migration_owner'
      AND procedure.proconfig @> ARRAY[
        'search_path=pg_catalog, '||pg_catalog.current_schema()
      ]
    FROM pg_catalog.pg_proc AS procedure
    JOIN pg_catalog.pg_roles AS owner ON owner.oid=procedure.proowner
    WHERE procedure.pronamespace=pg_catalog.current_schema()::regnamespace
      AND procedure.proname='start_qualification_release_controller_v1'
  ),false)
`).Scan(
		&tables, &indexes, &triggers, &functions, &definers,
		&guardSearchPath, &startHardenedOwner,
	); err != nil {
		t.Fatal(err)
	}
	if tables != 7 || indexes != 44 || triggers != 25 || functions != 43 || definers != 21 ||
		!guardSearchPath || !startHardenedOwner {
		t.Fatalf("Qualification Release catalog tables/indexes/triggers/functions/definers/guard-path/start-owner=%d/%d/%d/%d/%d/%t/%t",
			tables, indexes, triggers, functions, definers, guardSearchPath, startHardenedOwner)
	}
	legacyACL := qualificationReleaseUpstreamHelperACL(t, ctx, database)
	if legacyACL.Functions != 3 || legacyACL.MigrationOwner != 3 ||
		legacyACL.Public != 0 {
		t.Fatalf("Qualification Release legacy helper ACL=%+v, want functions/owner/public=3/3/0",
			legacyACL)
	}
	assertQualificationReleaseModernUpstreamHelpers(t, ctx, database)
}

func assertQualificationReleaseV3GuardSearchPath(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT COALESCE(procedure.proconfig @> ARRAY[
  'search_path=pg_catalog, '||pg_catalog.current_schema()
],false)
FROM pg_catalog.pg_proc AS procedure
WHERE procedure.pronamespace=pg_catalog.current_schema()::regnamespace
  AND procedure.proname='guard_workflow_execution_profile_v3_run'`,
	).Scan(&exact); err != nil || !exact {
		t.Fatalf("workflow-engine/v3 guard trusted search_path=%t error=%v", exact, err)
	}
}

func assertQualificationReleasePrivileges(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	var configured bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM pg_catalog.pg_roles
  WHERE rolname='worksflow_qualification_release_operator'
)`).Scan(&configured); err != nil {
		t.Fatal(err)
	}
	// Operator roles are provisioned by the deployment role bootstrap, not by
	// schema migrations. A schema-only canary still verifies the fail-closed
	// conditional grant path when that cluster role is not installed.
	if !configured {
		return
	}
	var roleExists, runtimeExecute, claimExecute, renewExecute bool
	var bootstrapExecute, tableInsert, tableSelect, claimTableInsert bool
	if err := database.QueryRowContext(ctx, `
SELECT
  EXISTS(SELECT 1 FROM pg_catalog.pg_roles
         WHERE rolname='worksflow_qualification_release_operator'),
  pg_catalog.has_function_privilege(
    'worksflow_qualification_release_operator',
    current_schema()||'.authorize_qualification_release_v1(uuid,uuid,uuid,text,uuid,uuid,uuid,uuid)',
    'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    'worksflow_qualification_release_operator',
    current_schema()||'.bootstrap_qualification_release_controller_v1(uuid,text,text,text,text)',
    'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    'worksflow_qualification_release_operator',
    current_schema()||'.claim_qualification_release_publish_v1(uuid,uuid,uuid,uuid,text,integer)',
    'EXECUTE'
  ),
  pg_catalog.has_function_privilege(
    'worksflow_qualification_release_operator',
    current_schema()||'.renew_qualification_release_publish_lease_v1(uuid,uuid,uuid,uuid,text,integer,timestamptz,timestamptz)',
    'EXECUTE'
  ),
  pg_catalog.has_table_privilege(
    'worksflow_qualification_release_operator',
    current_schema()||'.qualification_release_v1_authorizations','INSERT'
  ),
  pg_catalog.has_table_privilege(
    'worksflow_qualification_release_operator',
    current_schema()||'.qualification_release_v1_authorizations','SELECT'
  ),
  pg_catalog.has_table_privilege(
    'worksflow_qualification_release_operator',
    current_schema()||'.qualification_release_v1_lease_claims','INSERT'
  )`,
	).Scan(
		&roleExists, &runtimeExecute, &bootstrapExecute, &claimExecute,
		&renewExecute, &tableInsert, &tableSelect, &claimTableInsert,
	); err != nil {
		t.Fatal(err)
	}
	if !roleExists || !runtimeExecute || !claimExecute || !renewExecute ||
		bootstrapExecute || tableInsert || tableSelect || claimTableInsert {
		t.Fatalf("Qualification Release privileges role/runtime/claim/renew/bootstrap/insert/select/claim-insert=%t/%t/%t/%t/%t/%t/%t/%t",
			roleExists, runtimeExecute, claimExecute, renewExecute, bootstrapExecute,
			tableInsert, tableSelect, claimTableInsert)
	}
	var operatorExecuteCount, applicationExecuteCount, publicExecuteCount int
	if err := database.QueryRowContext(ctx, `
WITH release_functions AS (
  SELECT procedure.oid,procedure.proacl,procedure.proowner
  FROM pg_catalog.pg_proc AS procedure
  WHERE procedure.pronamespace=pg_catalog.current_schema()::regnamespace
    AND (procedure.proname LIKE 'qualification_release_v1_%'
      OR procedure.proname IN (
        'bootstrap_qualification_release_controller_v1',
        'authorize_qualification_release_v1',
        'claim_qualification_release_publish_v1',
        'inspect_qualification_release_publish_claim_v1',
        'renew_qualification_release_publish_lease_v1',
        'start_qualification_release_controller_v1',
        'record_qualification_release_result_v1',
        'apply_qualification_release_result_v1',
		'record_qualification_release_failure_v1',
		'apply_qualification_release_failure_v1',
        'inspect_qualification_release_controller_bootstrap_v1',
        'inspect_qualification_release_operation_v1',
        'resolve_qualification_release_authorization_v1',
        'resolve_qualification_release_for_publish_v1',
        'inspect_qualification_release_controller_v1',
        'inspect_qualification_release_result_v1',
		'inspect_qualification_release_failure_v1',
        'guard_qualification_release_v1_workflow_transition',
        'guard_qualification_release_v1_event_mutation',
        'validate_qualification_release_v1_closure',
        'reject_qualification_release_v1_mutation'
      ))
), application_role AS (
  SELECT oid FROM pg_catalog.pg_roles WHERE rolname='worksflow_application'
), operator_role AS (
  SELECT oid FROM pg_catalog.pg_roles
  WHERE rolname='worksflow_qualification_release_operator'
)
SELECT
  (SELECT count(*) FROM release_functions AS function
   CROSS JOIN operator_role AS role
   WHERE pg_catalog.has_function_privilege(
     role.oid,function.oid,'EXECUTE'
   )),
  (SELECT count(*) FROM release_functions AS function
   CROSS JOIN application_role AS role
   WHERE pg_catalog.has_function_privilege(
     role.oid,function.oid,'EXECUTE'
   )),
  (SELECT count(DISTINCT function.oid)
   FROM release_functions AS function
   CROSS JOIN LATERAL pg_catalog.aclexplode(
     COALESCE(
       function.proacl,
       pg_catalog.acldefault('f',function.proowner)
     )
   ) AS privilege
   WHERE privilege.grantee=0 AND privilege.privilege_type='EXECUTE')`,
	).Scan(&operatorExecuteCount, &applicationExecuteCount, &publicExecuteCount); err != nil {
		t.Fatal(err)
	}
	if operatorExecuteCount != 16 || applicationExecuteCount != 0 || publicExecuteCount != 0 {
		t.Fatalf("Qualification Release function allowlist operator/application/public=%d/%d/%d, want 16/0/0",
			operatorExecuteCount, applicationExecuteCount, publicExecuteCount)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
SET LOCAL ROLE worksflow_qualification_release_operator`); err != nil {
		t.Fatalf("assume Qualification Release runtime role: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
SELECT value FROM bootstrap_qualification_release_controller_v1(
  $1,'runtime-forbidden','2026.07.20','worksflow.release-delivery/v3',$2
) AS value`, uuid.New(), qualificationReleaseV1TestHash("7")); err == nil ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("runtime-role Controller bootstrap error=%v, want permission denied", err)
	}
}
