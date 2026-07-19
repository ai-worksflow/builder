package repositorygc

import (
	"errors"
	"strings"
	"testing"
)

func TestPostgresPostureValidationFailsClosed(t *testing.T) {
	safe := postgresPosture{
		roleName: "gc_login", sessionRoleName: "gc_login", schemaName: "public",
		isOperatorMember: true, indexTableCount: 10, functionCount: 4, executableFunctionCount: 4,
		secureFunctionContractCount: 4, relatedObjectsExactlyOwned: true, stableGroupRolesSafe: true,
		internalFunctionACLExact: true, sandboxCheckpointDependencyExact: true,
	}
	if err := safe.validate(); err != nil {
		t.Fatalf("safe posture rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*postgresPosture)
		want   string
	}{
		{"superuser", func(p *postgresPosture) { p.isSuperuser = true }, "administration"},
		{"set role", func(p *postgresPosture) { p.roleName = "worksflow_repository_index_gc_operator" }, "session"},
		{"reachable elevated role", func(p *postgresPosture) { p.reachableRoleElevated = true }, "administration"},
		{"reachable admin option", func(p *postgresPosture) { p.reachableRoleHasAdminOption = true }, "delegate"},
		{"database owner", func(p *postgresPosture) { p.ownsDatabase = true }, "database"},
		{"database create", func(p *postgresPosture) { p.canCreateInDatabase = true }, "database"},
		{"schema create", func(p *postgresPosture) { p.canCreateInSchema = true }, "schema"},
		{"operator role absent", func(p *postgresPosture) { p.isOperatorMember = false }, "operator"},
		{"migration owner member", func(p *postgresPosture) { p.isMigrationOwnerMember = true }, "boundary"},
		{"application member", func(p *postgresPosture) { p.isApplicationMember = true }, "boundary"},
		{"unsafe stable role", func(p *postgresPosture) { p.stableGroupRolesSafe = false }, "stable"},
		{"index table absent", func(p *postgresPosture) { p.indexTableCount = 9 }, "mutation"},
		{"direct table privilege", func(p *postgresPosture) { p.hasDirectTablePrivilege = true }, "mutation"},
		{"function missing", func(p *postgresPosture) { p.functionCount = 3 }, "function"},
		{"function shape changed", func(p *postgresPosture) { p.secureFunctionContractCount = 3 }, "function"},
		{"other definer executable", func(p *postgresPosture) { p.forbiddenSecurityDefinerCount = 1 }, "SECURITY DEFINER"},
		{"schema object owner", func(p *postgresPosture) { p.reachableSchemaObjectOwnerCount = 1 }, "owns"},
		{"business table privilege", func(p *postgresPosture) { p.privilegedRelationCount = 1 }, "relation"},
		{"sequence privilege", func(p *postgresPosture) { p.privilegedSequenceCount = 1 }, "sequence"},
		{"other function executable", func(p *postgresPosture) { p.executableNonGCFunctionCount = 1 }, "function authority"},
		{"gc grant option", func(p *postgresPosture) { p.grantableGCFunctionCount = 1 }, "function authority"},
		{"boundary owner drift", func(p *postgresPosture) { p.relatedObjectsExactlyOwned = false }, "migration-role owned"},
		{"internal function rogue grant", func(p *postgresPosture) { p.internalFunctionACLExact = false }, "owner-only"},
		{"sandbox helper drift", func(p *postgresPosture) { p.sandboxCheckpointDependencyExact = false }, "checkpoint helper"},
	} {
		t.Run(test.name, func(t *testing.T) {
			posture := safe
			test.mutate(&posture)
			err := posture.validate()
			if !errors.Is(err, ErrPostgresReadiness) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate() error = %v, want readiness error containing %q", err, test.want)
			}
		})
	}
}

func TestPostgresReadinessRequiresExactAuthorityFacts(t *testing.T) {
	ready := postgresReadiness{
		ready: true, reason: "ready", trustedSchema: "public",
		operatorRoleExists: true, applicationRoleExists: true,
		operatorExecuteGranted: true, applicationClaimExecuteGranted: true,
		publicClaimExecuteRevoked: true, publicSchemaCreateRevoked: true,
		migrationOwnerRoleExists: true, objectsOwnedByMigrationOwner: true,
		stableGroupRolesSafe: true, applicationSchemaHeadReadGranted: true,
	}
	if err := ready.validate("public"); err != nil {
		t.Fatalf("ready facts rejected: %v", err)
	}
	ready.publicClaimExecuteRevoked = false
	if err := ready.validate("public"); !errors.Is(err, ErrPostgresReadiness) {
		t.Fatalf("unsafe readiness error = %v", err)
	}
}

func TestPostgresContractQueriesNameOnlyDedicatedFunctions(t *testing.T) {
	for _, table := range []string{
		"repository_exact_tree_literal_index_manifests",
		"repository_exact_tree_literal_index_members",
		"repository_exact_tree_literal_index_blobs",
		"repository_exact_tree_literal_index_build_claims",
		"repository_exact_tree_literal_index_gc_runs",
		"repository_exact_tree_literal_index_gc_capabilities",
		"repository_exact_tree_literal_index_gc_receipts",
		"repository_exact_tree_literal_index_gc_tombstones",
		"repository_exact_tree_literal_index_gc_tree_delete_auth",
		"repository_exact_tree_literal_index_gc_blob_delete_auth",
	} {
		if !strings.Contains(postgresPostureQuery, table) {
			t.Fatalf("posture query omits exact catalog relation %s", table)
		}
	}
	for _, function := range []string{
		"plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)",
		"execute_repository_exact_tree_literal_index_gc",
		"inspect_repository_exact_tree_literal_index_gc_run",
		"repository_exact_tree_literal_index_gc_readiness",
	} {
		if !strings.Contains(postgresPostureQuery, function) {
			t.Fatalf("posture query omits %s", function)
		}
	}
	for _, forbidden := range []string{
		"SET ROLE", "DELETE FROM", "TRUNCATE TABLE", "WORKSFLOW_POSTGRES_DSN", "role_membership",
	} {
		if strings.Contains(postgresPostureQuery, forbidden) {
			t.Fatalf("posture query contains forbidden authority shortcut %q", forbidden)
		}
	}
	for _, required := range []string{
		"routine.prosecdef", "routine.proconfig", "routine.proallargtypes", "routine.proargmodes",
		"routine.proargnames", "routine.proowner = migration_owner.oid", "secure_contract_count",
		"pg_catalog.pg_class", "pg_catalog.pg_proc", "pg_catalog.pg_type", "pg_catalog.pg_collation",
		"pg_catalog.pg_conversion", "pg_catalog.pg_operator", "pg_catalog.pg_opclass",
		"pg_catalog.pg_opfamily", "pg_catalog.pg_ts_config", "pg_catalog.pg_ts_dict",
		"pg_catalog.pg_statistic_ext", "pg_catalog.pg_extension", "pg_catalog.pg_default_acl",
		"privileged_relation_count", "privileged_sequence_count", "executable_non_gc_count",
		"has_any_column_privilege", "EXECUTE WITH GRANT OPTION", "expected_boundary_functions",
		"schema_storage_owner_facts", "boundary_index_owner_facts", "index_count = 22",
		"boundary_function_owner_facts", "function_count = 23", "pg_catalog.pg_auth_members",
		"membership.admin_option", "reachable_membership_facts",
		"expected_internal_functions", "internal_function_acl_facts", "count(expected.routine_oid) = 8",
		"privilege.grantee <> routine.proowner", "sandbox_checkpoint_helper_facts",
		"sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)",
		"routine.prokind = 'f'", "routine.prorettype = 'boolean'::pg_catalog.regtype",
		"routine.provolatile = 's'", "language.lanname = 'sql'", "application_role.oid",
		"privilege.grantee NOT IN (routine.proowner, application_role.oid)",
	} {
		if !strings.Contains(postgresPostureQuery, required) {
			t.Fatalf("posture query omits independent function check %q", required)
		}
	}
}
