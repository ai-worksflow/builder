package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/migrations"
)

func TestValidatePostgresRolePosture(t *testing.T) {
	safe := postgresRolePostureFacts{
		roleCount: 1, roleName: "worksflow_api_login", sessionRoleName: "worksflow_api_login",
		databaseCount: 1, schemaCount: 1, schemaName: "worksflow",
		apiHasSchemaUsage: true, applicationHasSchemaUsage: true,
		faultOperatorHasSchemaUsage:         true,
		qualificationOperatorHasSchemaUsage: true,
		reachableRoleCount:                  2, isApplicationRoleReachable: true,
		stableGroupRoleCount: 5, isApplicationRoleMember: true,
		tableCount:                                       postgresExpectedRepositoryIndexTables,
		applicationExactTableACLCount:                    postgresExpectedRepositoryIndexTables,
		apiExactTableACLCount:                            postgresExpectedRepositoryIndexTables,
		gcPrivateTableCount:                              postgresExpectedRepositoryGCPrivateTables,
		goldenFaultTableCount:                            postgresExpectedGoldenFaultTables,
		exactGoldenFaultOperatorTableACLCount:            postgresExpectedGoldenFaultTables,
		qualificationPromotionTableCount:                 postgresExpectedQualificationPromotionTables,
		exactQualificationPromotionTableACLCount:         postgresExpectedQualificationPromotionTables,
		credentialSetTableCount:                          postgresExpectedCredentialSetTables,
		exactCredentialSetTableContractCount:             postgresExpectedCredentialSetTables,
		credentialSetNamedTableCount:                     postgresExpectedCredentialSetTables,
		exactCredentialSetTriggerContractCount:           postgresExpectedCredentialSetTriggers,
		credentialSetTriggerCount:                        postgresExpectedCredentialSetTriggers,
		qualificationEvidenceTableCount:                  postgresExpectedQualificationEvidenceTables,
		exactQualificationEvidenceTableContractCount:     postgresExpectedQualificationEvidenceTables,
		qualificationEvidenceNamedTableCount:             postgresExpectedQualificationEvidenceTables,
		exactQualificationEvidenceTriggerContractCount:   postgresExpectedQualificationEvidenceTriggers,
		qualificationEvidenceTriggerCount:                postgresExpectedQualificationEvidenceTotalTriggers,
		qualificationPlanTableCount:                      postgresExpectedQualificationPlanTables,
		exactQualificationPlanTableContractCount:         postgresExpectedQualificationPlanTables,
		qualificationPlanNamedTableCount:                 postgresExpectedQualificationPlanTables,
		qualificationPlanIndexCount:                      postgresExpectedQualificationPlanIndexes,
		exactQualificationPlanIndexContractCount:         postgresExpectedQualificationPlanIndexes,
		qualificationPlanNamedIndexCount:                 postgresExpectedQualificationPlanIndexes,
		exactQualificationPlanTriggerContractCount:       postgresExpectedQualificationPlanTriggers,
		qualificationPlanTriggerCount:                    postgresExpectedQualificationPlanTriggers,
		protectedTableCount:                              postgresExpectedProtectedTables,
		applicationExactProtectedTableACLCount:           postgresExpectedProtectedTables,
		applicationFunctionCount:                         postgresExpectedApplicationFunctions,
		securityDefinerApplicationFunctionCount:          postgresExpectedApplicationFunctions,
		migrationOwnedApplicationFunctionCount:           postgresExpectedApplicationFunctions,
		fixedSearchPathApplicationFunctionCount:          postgresExpectedApplicationFunctions,
		applicationBoundaryExecuteCount:                  postgresExpectedApplicationFunctions,
		apiApplicationFunctionExecuteCount:               postgresExpectedApplicationFunctions,
		expectedGCFunctionCount:                          postgresExpectedRepositoryGCFunctions,
		securityDefinerGCFunctionCount:                   postgresExpectedRepositoryGCFunctions,
		migrationOwnedGCFunctionCount:                    postgresExpectedRepositoryGCFunctions,
		fixedSearchPathGCFunctionCount:                   postgresExpectedRepositoryGCFunctions,
		exactResultGCFunctionCount:                       postgresExpectedRepositoryGCFunctions,
		operatorExpectedGCFunctionCount:                  postgresExpectedRepositoryGCFunctions,
		internalFunctionCount:                            postgresExpectedInternalFunctions,
		exactOwnerACLInternalFunctionCount:               postgresExpectedInternalFunctions,
		modelGovernanceFunctionCount:                     postgresExpectedModelGovernanceFunctions,
		exactModelGovernanceFunctionContractCount:        postgresExpectedModelGovernanceFunctions,
		qualificationPromotionFunctionCount:              postgresExpectedQualificationPromotionFunctions,
		exactQualificationPromotionFunctionContractCount: postgresExpectedQualificationPromotionFunctions,
		credentialSetFunctionCount:                       postgresExpectedCredentialSetFunctions,
		exactCredentialSetFunctionContractCount:          postgresExpectedCredentialSetFunctions,
		credentialSetNamedFunctionCount:                  postgresExpectedCredentialSetFunctions,
		qualificationEvidenceFunctionCount:               postgresExpectedQualificationEvidenceFunctions,
		exactQualificationEvidenceFunctionContractCount:  postgresExpectedQualificationEvidenceFunctions,
		qualificationEvidenceNamedFunctionCount:          postgresExpectedQualificationEvidenceNamedFunctions,
		qualificationPlanFunctionCount:                   postgresExpectedQualificationPlanFunctions,
		exactQualificationPlanFunctionContractCount:      postgresExpectedQualificationPlanFunctions,
		qualificationPlanNamedFunctionCount:              postgresExpectedQualificationPlanFunctions,
		sandboxCheckpointHelperCount:                     postgresExpectedSandboxCheckpointHelpers,
		exactSandboxCheckpointHelperContractCount:        postgresExpectedSandboxCheckpointHelpers,
		schemaOwnerIsExact:                               true,
		ownedBoundaryTableCount:                          postgresExpectedOwnedBoundaryTables,
		exactOwnedBoundaryTableCount:                     postgresExpectedOwnedBoundaryTables,
		ownedBoundaryIndexCount:                          postgresExpectedOwnedBoundaryIndexes,
		exactOwnedBoundaryIndexCount:                     postgresExpectedOwnedBoundaryIndexes,
		ownedBoundaryRoutineCount:                        postgresExpectedOwnedBoundaryRoutines,
		exactOwnedBoundaryRoutineCount:                   postgresExpectedOwnedBoundaryRoutines,
		ownedRelationCount:                               1,
		exactOwnedRelationCount:                          1,
		securityDefinerFunctionCount:                     postgresExpectedSecurityDefinerFunctions,
		exactOwnedSecurityDefinerFunctionCount:           postgresExpectedSecurityDefinerFunctions,
		reachableExecutableSecurityDefinerCount:          postgresExpectedApplicationFunctions,
		reachableExpectedApplicationDefinerCount:         postgresExpectedApplicationFunctions,
		gcFunctionCount:                                  postgresExpectedRepositoryGCFunctions,
	}
	if err := validatePostgresRolePosture(safe); err != nil {
		t.Fatalf("safe posture rejected: %v", err)
	}

	tests := []struct {
		name       string
		mutate     func(*postgresRolePostureFacts)
		wantDetail string
	}{
		{"missing current role", func(f *postgresRolePostureFacts) { f.roleCount = 0 }, "current API role"},
		{"session role switch", func(f *postgresRolePostureFacts) { f.sessionRoleName = "postgres" }, "session role differs"},
		{"superuser", func(f *postgresRolePostureFacts) { f.isSuperuser = true }, "superuser"},
		{"bypass rls", func(f *postgresRolePostureFacts) { f.bypassesRLS = true }, "row-level security"},
		{"create role", func(f *postgresRolePostureFacts) { f.canCreateRole = true }, "create roles"},
		{"create database", func(f *postgresRolePostureFacts) { f.canCreateDatabase = true }, "create databases"},
		{"replication", func(f *postgresRolePostureFacts) { f.canReplicate = true }, "replication"},
		{"reachable privileged role", func(f *postgresRolePostureFacts) { f.hasReachableClusterAuthority = true }, "assume a role"},
		{"application not reachable", func(f *postgresRolePostureFacts) { f.isApplicationRoleReachable = false }, "cannot reach the application"},
		{"forbidden stable role reachable", func(f *postgresRolePostureFacts) { f.forbiddenStableRoleReachable = true }, "Golden-fault-operator"},
		{"reachable role admin option", func(f *postgresRolePostureFacts) { f.hasReachableRoleAdminOption = true }, "ADMIN OPTION"},
		{"missing database", func(f *postgresRolePostureFacts) { f.databaseCount = 0 }, "current database"},
		{"database owner membership", func(f *postgresRolePostureFacts) { f.ownsOrInheritsDatabaseOwner = true }, "database owner"},
		{"database create", func(f *postgresRolePostureFacts) { f.canCreateInDatabase = true }, "create schemas"},
		{"missing schema", func(f *postgresRolePostureFacts) { f.schemaCount = 0 }, "current schema"},
		{"missing schema usage", func(f *postgresRolePostureFacts) { f.applicationHasSchemaUsage = false }, "USAGE"},
		{"missing fault operator schema usage", func(f *postgresRolePostureFacts) { f.faultOperatorHasSchemaUsage = false }, "fault operator requires"},
		{"missing qualification operator schema usage", func(f *postgresRolePostureFacts) { f.qualificationOperatorHasSchemaUsage = false }, "qualification promotion operator requires"},
		{"schema create", func(f *postgresRolePostureFacts) { f.canCreateInSchema = true }, "create objects"},
		{"application schema create", func(f *postgresRolePostureFacts) { f.applicationCanCreateInSchema = true }, "create objects"},
		{"fault operator schema create", func(f *postgresRolePostureFacts) { f.faultOperatorCanCreateInSchema = true }, "fault operator can create"},
		{"qualification operator schema create", func(f *postgresRolePostureFacts) { f.qualificationOperatorCanCreateInSchema = true }, "qualification promotion operator can create"},
		{"schema object owner", func(f *postgresRolePostureFacts) { f.ownedSchemaObjectCount = 1 }, "owns or inherits objects"},
		{"missing stable group", func(f *postgresRolePostureFacts) { f.stableGroupRoleCount = 4 }, "group roles are incomplete"},
		{"unsafe stable group", func(f *postgresRolePostureFacts) { f.stableGroupRolesUnsafe = true }, "NOLOGIN"},
		{"application membership missing", func(f *postgresRolePostureFacts) { f.isApplicationRoleMember = false }, "not an application group"},
		{"migration membership", func(f *postgresRolePostureFacts) { f.isMigrationOwnerRoleMember = true }, "migration-owner group member"},
		{"operator membership", func(f *postgresRolePostureFacts) { f.isOperatorRoleMember = true }, "operator member"},
		{"fault operator membership", func(f *postgresRolePostureFacts) { f.isGoldenFaultOperatorRoleMember = true }, "fault operator member"},
		{"qualification operator membership", func(f *postgresRolePostureFacts) { f.isQualificationPromotionOperatorRoleMember = true }, "qualification promotion operator member"},
		{"missing table", func(f *postgresRolePostureFacts) { f.tableCount-- }, "table catalog"},
		{"table owner membership", func(f *postgresRolePostureFacts) { f.ownsOrInheritsTableOwner = true }, "inherits"},
		{"application table ACL", func(f *postgresRolePostureFacts) { f.applicationExactTableACLCount-- }, "table privileges"},
		{"API table ACL", func(f *postgresRolePostureFacts) { f.apiExactTableACLCount-- }, "table privileges"},
		{"public index table ACL", func(f *postgresRolePostureFacts) { f.publicPrivilegedIndexTableCount = 1 }, "PUBLIC can access"},
		{"missing private table", func(f *postgresRolePostureFacts) { f.gcPrivateTableCount-- }, "private table catalog"},
		{"private table access", func(f *postgresRolePostureFacts) { f.apiPrivilegedGCPrivateTableCount = 1 }, "private tables"},
		{"missing Golden fault table", func(f *postgresRolePostureFacts) { f.goldenFaultTableCount-- }, "Golden fault consume tables"},
		{"Golden fault operator ACL drift", func(f *postgresRolePostureFacts) { f.exactGoldenFaultOperatorTableACLCount-- }, "dedicated operator ACL"},
		{"missing qualification promotion table", func(f *postgresRolePostureFacts) { f.qualificationPromotionTableCount-- }, "qualification promotion consume/handoff"},
		{"qualification promotion table ACL drift", func(f *postgresRolePostureFacts) { f.exactQualificationPromotionTableACLCount-- }, "SELECT-only operator ACL"},
		{"qualification promotion table ACL outside boundary", func(f *postgresRolePostureFacts) { f.unexpectedQualificationPromotionTableACLCount = 1 }, "SELECT-only operator ACL"},
		{"missing CredentialSet table", func(f *postgresRolePostureFacts) { f.credentialSetTableCount-- }, "exact four-table"},
		{"CredentialSet table owner or ACL drift", func(f *postgresRolePostureFacts) { f.exactCredentialSetTableContractCount-- }, "migration-owner-only"},
		{"unexpected CredentialSet table", func(f *postgresRolePostureFacts) { f.credentialSetNamedTableCount++ }, "exact four-table"},
		{"CredentialSet trigger drift", func(f *postgresRolePostureFacts) { f.exactCredentialSetTriggerContractCount-- }, "trigger contracts"},
		{"unexpected CredentialSet trigger", func(f *postgresRolePostureFacts) { f.credentialSetTriggerCount++ }, "trigger contracts"},
		{"missing Qualification Evidence table", func(f *postgresRolePostureFacts) { f.qualificationEvidenceTableCount-- }, "Qualification Evidence tables"},
		{"Qualification Evidence table owner or ACL drift", func(f *postgresRolePostureFacts) { f.exactQualificationEvidenceTableContractCount-- }, "migration-owner-only"},
		{"unexpected Qualification Evidence table", func(f *postgresRolePostureFacts) { f.qualificationEvidenceNamedTableCount++ }, "Qualification Evidence tables"},
		{"Qualification Evidence trigger drift", func(f *postgresRolePostureFacts) { f.exactQualificationEvidenceTriggerContractCount-- }, "Qualification Evidence events"},
		{"unexpected Qualification Evidence trigger", func(f *postgresRolePostureFacts) { f.qualificationEvidenceTriggerCount++ }, "Qualification Evidence events"},
		{"missing Qualification Plan table", func(f *postgresRolePostureFacts) { f.qualificationPlanTableCount-- }, "Qualification Plan authority tables"},
		{"Qualification Plan table owner or ACL drift", func(f *postgresRolePostureFacts) { f.exactQualificationPlanTableContractCount-- }, "migration-owner-only"},
		{"unexpected Qualification Plan table", func(f *postgresRolePostureFacts) { f.qualificationPlanNamedTableCount++ }, "exact two-table"},
		{"missing Qualification Plan index", func(f *postgresRolePostureFacts) { f.qualificationPlanIndexCount-- }, "exact eight-index"},
		{"Qualification Plan index drift", func(f *postgresRolePostureFacts) { f.exactQualificationPlanIndexContractCount-- }, "exact eight-index"},
		{"unexpected Qualification Plan index", func(f *postgresRolePostureFacts) { f.qualificationPlanNamedIndexCount++ }, "exact eight-index"},
		{"Qualification Plan trigger drift", func(f *postgresRolePostureFacts) { f.exactQualificationPlanTriggerContractCount-- }, "authority-guard trigger"},
		{"unexpected Qualification Plan trigger", func(f *postgresRolePostureFacts) { f.qualificationPlanTriggerCount++ }, "authority-guard trigger"},
		{"missing protected table", func(f *postgresRolePostureFacts) { f.protectedTableCount-- }, "protected-table"},
		{"protected table ACL drift", func(f *postgresRolePostureFacts) { f.applicationExactProtectedTableACLCount-- }, "protected-table"},
		{"hidden relation ACL", func(f *postgresRolePostureFacts) { f.reachableNonApplicationRelationACLCount = 1 }, "reachable non-application"},
		{"public schema relation ACL", func(f *postgresRolePostureFacts) { f.publicSchemaRelationACLCount = 1 }, "PUBLIC has"},
		{"application relation grant option", func(f *postgresRolePostureFacts) { f.applicationRelationGrantOptionCount = 1 }, "grant option"},
		{"application ordinary column ACL", func(f *postgresRolePostureFacts) { f.schemaColumnACLCount = 1 }, "must not contain column-level ACLs"},
		{"hidden column ACL", func(f *postgresRolePostureFacts) { f.reachableNonApplicationColumnACLCount = 1 }, "reachable non-application"},
		{"public schema column ACL", func(f *postgresRolePostureFacts) { f.publicSchemaColumnACLCount = 1 }, "PUBLIC has"},
		{"application column grant option", func(f *postgresRolePostureFacts) { f.applicationColumnGrantOptionCount = 1 }, "grant option"},
		{"missing application function", func(f *postgresRolePostureFacts) { f.applicationFunctionCount-- }, "application SECURITY DEFINER"},
		{"application function security invoker", func(f *postgresRolePostureFacts) { f.securityDefinerApplicationFunctionCount-- }, "must all be SECURITY DEFINER"},
		{"application function wrong owner", func(f *postgresRolePostureFacts) { f.migrationOwnedApplicationFunctionCount-- }, "worksflow_migration_owner"},
		{"application function mutable path", func(f *postgresRolePostureFacts) { f.fixedSearchPathApplicationFunctionCount-- }, "trusted search_path"},
		{"application function grant missing", func(f *postgresRolePostureFacts) { f.applicationBoundaryExecuteCount-- }, "application SECURITY DEFINER"},
		{"API application function grant missing", func(f *postgresRolePostureFacts) { f.apiApplicationFunctionExecuteCount-- }, "application SECURITY DEFINER"},
		{"public application function grant", func(f *postgresRolePostureFacts) { f.publicApplicationFunctionExecuteCount = 1 }, "PUBLIC"},
		{"unexpected application function grant", func(f *postgresRolePostureFacts) { f.unexpectedApplicationFunctionGranteeCount = 1 }, "unexpected roles"},
		{"missing exact GC function", func(f *postgresRolePostureFacts) { f.expectedGCFunctionCount-- }, "function contract is incomplete"},
		{"GC security invoker", func(f *postgresRolePostureFacts) { f.securityDefinerGCFunctionCount-- }, "SECURITY DEFINER"},
		{"GC wrong owner", func(f *postgresRolePostureFacts) { f.migrationOwnedGCFunctionCount-- }, "worksflow_migration_owner"},
		{"GC mutable search path", func(f *postgresRolePostureFacts) { f.fixedSearchPathGCFunctionCount-- }, "search_path"},
		{"GC wrong result", func(f *postgresRolePostureFacts) { f.exactResultGCFunctionCount-- }, "RETURNS TABLE"},
		{"operator exact GC grant missing", func(f *postgresRolePostureFacts) { f.operatorExpectedGCFunctionCount-- }, "operator lacks"},
		{"public exact GC grant", func(f *postgresRolePostureFacts) { f.publicExpectedGCFunctionCount = 1 }, "API or PUBLIC"},
		{"API exact GC grant", func(f *postgresRolePostureFacts) { f.apiExpectedGCFunctionCount = 1 }, "API or PUBLIC"},
		{"unexpected GC function grant", func(f *postgresRolePostureFacts) { f.unexpectedGCFunctionGranteeCount = 1 }, "outside the operator"},
		{"missing internal function", func(f *postgresRolePostureFacts) { f.internalFunctionCount-- }, "internal routines"},
		{"internal function non-owner grant", func(f *postgresRolePostureFacts) { f.exactOwnerACLInternalFunctionCount-- }, "owner-only"},
		{"model governance routine drift", func(f *postgresRolePostureFacts) { f.exactModelGovernanceFunctionContractCount-- }, "Model Governance owner-only"},
		{"qualification promotion routine drift", func(f *postgresRolePostureFacts) { f.exactQualificationPromotionFunctionContractCount-- }, "qualification promotion consume routine"},
		{"qualification promotion routine ACL outside boundary", func(f *postgresRolePostureFacts) { f.unexpectedQualificationPromotionFunctionACLCount = 1 }, "qualification promotion consume routine"},
		{"missing CredentialSet routine", func(f *postgresRolePostureFacts) { f.credentialSetFunctionCount-- }, "CredentialSet SHA-256"},
		{"CredentialSet routine contract drift", func(f *postgresRolePostureFacts) { f.exactCredentialSetFunctionContractCount-- }, "CredentialSet SHA-256"},
		{"unexpected CredentialSet routine", func(f *postgresRolePostureFacts) { f.credentialSetNamedFunctionCount++ }, "CredentialSet SHA-256"},
		{"missing Qualification Evidence routine", func(f *postgresRolePostureFacts) { f.qualificationEvidenceFunctionCount-- }, "Qualification Evidence SHA-256"},
		{"Qualification Evidence routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationEvidenceFunctionContractCount-- }, "Qualification Evidence SHA-256"},
		{"unexpected Qualification Evidence routine", func(f *postgresRolePostureFacts) { f.qualificationEvidenceNamedFunctionCount++ }, "Qualification Evidence SHA-256"},
		{"missing Qualification Plan routine", func(f *postgresRolePostureFacts) { f.qualificationPlanFunctionCount-- }, "Qualification Plan SHA-256"},
		{"Qualification Plan routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationPlanFunctionContractCount-- }, "Qualification Plan SHA-256"},
		{"unexpected Qualification Plan routine", func(f *postgresRolePostureFacts) { f.qualificationPlanNamedFunctionCount++ }, "Qualification Plan SHA-256"},
		{"missing sandbox checkpoint helper", func(f *postgresRolePostureFacts) { f.sandboxCheckpointHelperCount-- }, "sandbox checkpoint helper"},
		{"sandbox checkpoint helper drift", func(f *postgresRolePostureFacts) { f.exactSandboxCheckpointHelperContractCount-- }, "SQL/STABLE SECURITY INVOKER"},
		{"wrong schema owner", func(f *postgresRolePostureFacts) { f.schemaOwnerIsExact = false }, "not owned exactly"},
		{"missing owned boundary table", func(f *postgresRolePostureFacts) { f.ownedBoundaryTableCount-- }, "not owned exactly"},
		{"wrong boundary table owner", func(f *postgresRolePostureFacts) { f.exactOwnedBoundaryTableCount-- }, "not owned exactly"},
		{"missing owned boundary index", func(f *postgresRolePostureFacts) { f.ownedBoundaryIndexCount-- }, "not owned exactly"},
		{"wrong boundary index owner", func(f *postgresRolePostureFacts) { f.exactOwnedBoundaryIndexCount-- }, "not owned exactly"},
		{"missing owned boundary routine", func(f *postgresRolePostureFacts) { f.ownedBoundaryRoutineCount-- }, "not owned exactly"},
		{"wrong boundary routine owner", func(f *postgresRolePostureFacts) { f.exactOwnedBoundaryRoutineCount-- }, "not owned exactly"},
		{"reachable relation owner", func(f *postgresRolePostureFacts) { f.exactOwnedRelationCount-- }, "solely owned"},
		{"missing security definer", func(f *postgresRolePostureFacts) { f.securityDefinerFunctionCount-- }, "exact migration-owner set"},
		{"member-owned security definer", func(f *postgresRolePostureFacts) { f.exactOwnedSecurityDefinerFunctionCount-- }, "exact migration-owner set"},
		{"public ordinary routine", func(f *postgresRolePostureFacts) { f.publicSchemaRoutineExecuteCount = 1 }, "PUBLIC can execute trusted-schema"},
		{"hidden routine ACL", func(f *postgresRolePostureFacts) { f.reachableNonApplicationRoutineACLCount = 1 }, "reachable non-application"},
		{"application routine grant option", func(f *postgresRolePostureFacts) { f.applicationRoutineGrantOptionCount = 1 }, "grant option"},
		{"missing reachable application definer", func(f *postgresRolePostureFacts) { f.reachableExecutableSecurityDefinerCount-- }, "exact ten-function"},
		{"unexpected reachable definer", func(f *postgresRolePostureFacts) {
			f.reachableUnexpectedSecurityDefinerCount = 1
			f.reachableExecutableSecurityDefinerCount++
		}, "exact ten-function"},
		{"reachable GC execution", func(f *postgresRolePostureFacts) { f.reachableGCFunctionExecuteCount = 1 }, "can execute repository index GC"},
		{"future executable GC function", func(f *postgresRolePostureFacts) { f.gcFunctionCount++; f.executableGCFunctionCount = 1 }, "execute repository index GC"},
		{"inconsistent GC catalog", func(f *postgresRolePostureFacts) { f.gcFunctionCount = 0 }, "inconsistent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := safe
			test.mutate(&facts)
			err := validatePostgresRolePosture(facts)
			if !errors.Is(err, ErrUnsafePostgresAPIRolePosture) ||
				!strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("validate error = %v, want unsafe posture containing %q", err, test.wantDetail)
			}
		})
	}
}

func TestPostgresRolePostureQueryPinsGCContracts(t *testing.T) {
	for _, required := range []string{
		"WITH RECURSIVE",
		"session_reachable_roles(role_oid)",
		"membership.inherit_option OR membership.set_option",
		"reachable_non_application_acl_count",
		"reachable_non_application_direct_acl_count",
		"schema_columns.total_acl_count",
		"CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS column_acl",
		"application_grant_option_count",
		"exact_security_definer_owner_count",
		"routine.proowner = stable.migration_owner_oid",
		"relation.relowner = stable.migration_owner_oid",
		"routine.prosecdef",
		"search_path=pg_catalog, %I, pg_temp",
		"pg_catalog.pg_get_function_result(routine.oid) = expected.result_contract",
		"TABLE(run_id uuid, capability_id uuid, project_id uuid, tree_hash text",
		"TABLE(receipt_id uuid, capability_id uuid, run_id uuid, project_id uuid",
		"TABLE(run_id uuid, run_status text, planned_at timestamp with time zone",
		"TABLE(ready boolean, reason text, trusted_schema text",
		"repository_exact_tree_literal_index_gc_blob_delete_auth",
		"golden_fault_consume_reservations",
		"golden_fault_consume_results",
		"worksflow_golden_fault_operator",
		"validate_golden_fault_consume_result",
		"reject_golden_fault_ledger_mutation",
		"model_governance_activation_records",
		"model_governance_activation_heads",
		"model_governance_revocation_anchor",
		"append_model_governance_activation",
		"append_model_governance_genesis",
		"observe_model_governance_revocation_authority",
		"observe_model_governance_trust_policy",
		"enforce_model_governance_activation_authority_anchor",
		"trigger.tgtype = 7",
		"reject_model_governance_immutable_mutation",
		"expected_model_governance_functions",
		"model_governance_function_facts",
		"qualification_promotion_consumptions",
		"qualification_promotion_handoffs",
		"worksflow_qualification_promotion_operator",
		"consume_verified_qualification_promotion",
		"reject_qualification_promotion_mutation",
		"qualification_evidence_events",
		"qualification_evidence_operations",
		"qualification_evidence_heads",
		"qualification_evidence_projection_authorizations",
		"qualification_evidence_events_immutable",
		"qualification_evidence_operations_immutable",
		"qualification_evidence_heads_guard",
		"qualification_evidence_sha256",
		"reject_qualification_evidence_immutable_mutation",
		"guard_qualification_evidence_head_projection",
		"append_qualification_evidence_event",
		"expected_qualification_evidence_functions",
		"qualification_evidence_function_facts",
		"qualification_plan_authorities",
		"qualification_plan_identity_reservations",
		"qualification_plan_identity_r_authority_id_identity_kind_or_key",
		"qualification_plan_authorities_immutable",
		"qualification_plan_identity_reservations_immutable",
		"qualification_evidence_plan_authority_guard",
		"qualification_plan_sha256",
		"reject_qualification_plan_immutable_mutation",
		"freeze_qualification_plan_authority",
		"resolve_qualification_plan_authority",
		"guard_qualification_evidence_plan_authority",
		"expected_qualification_plan_indexes",
		"expected_qualification_plan_triggers",
		"expected_qualification_plan_functions",
		"qualification_plan_function_facts",
		"expected_qualification_promotion_functions",
		"qualification_promotion_function_facts",
		"acquire_repository_exact_tree_literal_index_build_claim",
		"acquire_candidate_workspace_lease",
		"abandon_sandbox_session_candidate",
		"complete_abandoned_sandbox_session",
		"expected_internal_functions",
		"exact_owner_acl_count",
		"sandbox_checkpoint_is_exact",
		"uuid, uuid, uuid, bigint, bigint, bigint, bigint, text, uuid, text, text, text",
		"routine.prorettype = 'boolean'::pg_catalog.regtype",
		"NOT routine.proretset",
		"routine.provolatile = 's'",
		"language.lanname = 'sql'",
		"routine_acl.grantee NOT IN",
	} {
		if !strings.Contains(postgresRolePostureQuery, required) {
			t.Fatalf("posture query is missing %q", required)
		}
	}
	if strings.Contains(postgresRolePostureQuery, "SET ROLE") {
		t.Fatal("posture verification must not mutate the database session role")
	}
	if strings.Contains(
		postgresRolePostureQuery,
		"pg_catalog.has_function_privilege(stable.operator_oid, routine.oid",
	) {
		t.Fatal("operator GC candidates must use explicit ACLs, not PUBLIC-effective execution")
	}
}

func TestVerifyPostgresAPIRolePostureEnvironmentPolicy(t *testing.T) {
	ctx := context.Background()
	for _, environment := range []string{config.EnvironmentDevelopment, config.EnvironmentTest} {
		if err := VerifyPostgresAPIRolePosture(ctx, nil, environment); err != nil {
			t.Fatalf("%s posture should be skipped: %v", environment, err)
		}
	}
	for _, environment := range []string{
		config.EnvironmentStaging,
		config.EnvironmentProduction,
		"unknown",
	} {
		err := VerifyPostgresAPIRolePosture(ctx, nil, environment)
		if !errors.Is(err, ErrUnsafePostgresAPIRolePosture) {
			t.Fatalf("%s error = %v, want unsafe posture", environment, err)
		}
	}
}

func TestScanPostgresRolePostureFailsClosed(t *testing.T) {
	want := errors.New("catalog unavailable")
	_, err := scanPostgresRolePosture(func(...any) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("scan error = %v, want %v", err, want)
	}
}

func TestPostgresAPIRolePostureRealPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
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
		t.Fatalf("inspect test PostgreSQL role: %v", err)
	}
	if !canProvisionRoles {
		t.Skip("real PostgreSQL role-posture canary requires a role that can provision fixture logins")
	}

	lockConnection, err := admin.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve advisory-lock connection: %v", err)
	}
	defer lockConnection.Close()
	const advisoryLockID int64 = 804357060326886689
	if _, err := lockConnection.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_lock($1)`, advisoryLockID); err != nil {
		t.Fatalf("lock role-posture fixture: %v", err)
	}
	defer lockConnection.ExecContext(context.Background(), `SELECT pg_catalog.pg_advisory_unlock($1)`, advisoryLockID)

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "posture_" + suffix
	ownerRole := "posture_owner_" + suffix
	apiRole := "posture_api_" + suffix
	bypassRole := "posture_bypass_" + suffix
	hiddenRole := "posture_hidden_" + suffix
	elevatedOwnerRole := "posture_elevated_" + suffix
	apiPassword := "api_" + suffix
	bypassPassword := "bypass_" + suffix

	createdStableRoles := make([]string, 0, 5)
	var apiDatabase *sql.DB
	var bypassDatabase *sql.DB
	defer func() {
		if bypassDatabase != nil {
			_ = bypassDatabase.Close()
		}
		if apiDatabase != nil {
			_ = apiDatabase.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schemaName+` CASCADE`)
		for _, role := range []string{hiddenRole, elevatedOwnerRole, bypassRole, apiRole, ownerRole} {
			_, _ = admin.ExecContext(cleanupCtx, `DROP OWNED BY `+role)
			_, _ = admin.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS `+role)
		}
		for index := len(createdStableRoles) - 1; index >= 0; index-- {
			role := createdStableRoles[index]
			_, _ = admin.ExecContext(cleanupCtx, `DROP OWNED BY `+role)
			_, _ = admin.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS `+role)
		}
	}()
	for _, role := range []string{
		postgresApplicationRole,
		postgresMigrationOwnerRole,
		postgresRepositoryIndexGCOperatorRole,
		postgresGoldenFaultOperatorRole,
		postgresQualificationPromotionOperatorRole,
	} {
		var exists bool
		if err := admin.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			t.Fatalf("inspect stable role %s: %v", role, err)
		}
		if !exists {
			if _, err := admin.ExecContext(ctx, `CREATE ROLE `+role+` NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`); err != nil {
				t.Fatalf("create stable role %s: %v", role, err)
			}
			createdStableRoles = append(createdStableRoles, role)
		}
	}

	for _, statement := range []string{
		`CREATE ROLE ` + ownerRole + ` NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`,
		`CREATE ROLE ` + apiRole + ` LOGIN PASSWORD '` + apiPassword + `' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`,
		`CREATE ROLE ` + bypassRole + ` LOGIN PASSWORD '` + bypassPassword + `' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`,
		`CREATE ROLE ` + hiddenRole + ` NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION`,
		`CREATE ROLE ` + elevatedOwnerRole + ` NOLOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS CREATEDB NOCREATEROLE NOREPLICATION`,
		`GRANT ` + postgresMigrationOwnerRole + ` TO ` + ownerRole,
		`GRANT ` + postgresMigrationOwnerRole + ` TO ` + elevatedOwnerRole + ` WITH INHERIT FALSE, SET FALSE`,
		`GRANT ` + postgresApplicationRole + ` TO ` + apiRole,
		`GRANT ` + hiddenRole + ` TO ` + apiRole + ` WITH INHERIT FALSE, SET TRUE`,
		`GRANT ` + apiRole + ` TO ` + bypassRole,
		`CREATE SCHEMA ` + schemaName + ` AUTHORIZATION ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON SCHEMA ` + schemaName + ` FROM PUBLIC`,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresApplicationRole,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresRepositoryIndexGCOperatorRole,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresGoldenFaultOperatorRole,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresQualificationPromotionOperatorRole,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision role-posture fixture with %q: %v", statement, err)
		}
	}

	indexTables := []string{
		"repository_exact_tree_literal_index_blobs",
		"repository_exact_tree_literal_index_members",
		"repository_exact_tree_literal_index_manifests",
		"repository_exact_tree_literal_index_build_claims",
	}
	privateTables := []string{
		"repository_exact_tree_literal_index_gc_runs",
		"repository_exact_tree_literal_index_gc_capabilities",
		"repository_exact_tree_literal_index_gc_receipts",
		"repository_exact_tree_literal_index_gc_tombstones",
		"repository_exact_tree_literal_index_gc_tree_delete_auth",
		"repository_exact_tree_literal_index_gc_blob_delete_auth",
	}
	faultTables := []string{
		"golden_fault_consume_reservations",
		"golden_fault_consume_results",
	}
	modelGovernanceTables := []string{
		"model_governance_activation_records",
		"model_governance_activation_heads",
		"model_governance_revocation_anchor",
	}
	qualificationPromotionTables := []string{
		"qualification_promotion_consumptions",
		"qualification_promotion_handoffs",
	}
	credentialSetTables := []string{
		"credential_set_events",
		"credential_set_operations",
		"credential_set_heads",
		"credential_set_projection_authorizations",
	}
	qualificationEvidenceTables := []string{
		"qualification_evidence_events",
		"qualification_evidence_operations",
		"qualification_evidence_heads",
		"qualification_evidence_projection_authorizations",
	}
	allBoundaryTables := append(append(append(append(append(append([]string{}, indexTables...), privateTables...), faultTables...), modelGovernanceTables...), qualificationPromotionTables...), credentialSetTables...)
	allBoundaryTables = append(allBoundaryTables, qualificationEvidenceTables...)
	for _, tableName := range allBoundaryTables {
		for _, statement := range []string{
			`CREATE TABLE ` + schemaName + `.` + tableName + ` (id bigint, value bigint)`,
			`ALTER TABLE ` + schemaName + `.` + tableName + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON TABLE ` + schemaName + `.` + tableName + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("provision table fixture with %q: %v", statement, err)
			}
		}
	}
	for _, statement := range []string{
		`CREATE TABLE ` + schemaName + `.qualification_plan_authorities (
  authority_id bigint PRIMARY KEY,
  operation_id bigint NOT NULL UNIQUE,
  plan_artifact_id text NOT NULL UNIQUE,
  orchestration_id bigint NOT NULL UNIQUE,
  project_id bigint, workflow_run_id bigint, node_key text, target_revision_id bigint,
  id bigint, value bigint
)`,
		`CREATE INDEX qualification_plan_authorities_target_idx ON ` + schemaName + `.qualification_plan_authorities (project_id, workflow_run_id, node_key, target_revision_id)`,
		`CREATE TABLE ` + schemaName + `.qualification_plan_identity_reservations (
  identity_value text PRIMARY KEY,
  authority_id bigint NOT NULL,
  identity_kind text NOT NULL,
  ordinal integer NOT NULL,
  id bigint, value bigint,
  UNIQUE (authority_id, identity_kind, ordinal)
)`,
		`CREATE INDEX qualification_plan_identity_authority_idx ON ` + schemaName + `.qualification_plan_identity_reservations (authority_id)`,
		`ALTER TABLE ` + schemaName + `.qualification_plan_authorities OWNER TO ` + postgresMigrationOwnerRole,
		`ALTER TABLE ` + schemaName + `.qualification_plan_identity_reservations OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.qualification_plan_authorities FROM PUBLIC`,
		`REVOKE ALL ON TABLE ` + schemaName + `.qualification_plan_identity_reservations FROM PUBLIC`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision Qualification Plan table fixture with %q: %v", statement, err)
		}
	}
	for _, statement := range []string{
		`CREATE TABLE ` + schemaName + `.schema_migrations (version text)`,
		`ALTER TABLE ` + schemaName + `.schema_migrations OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.schema_migrations FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.business_records (id bigint, value bigint)`,
		`ALTER TABLE ` + schemaName + `.business_records OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.business_records FROM PUBLIC`,
		`CREATE SEQUENCE ` + schemaName + `.business_sequence`,
		`ALTER SEQUENCE ` + schemaName + `.business_sequence OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON SEQUENCE ` + schemaName + `.business_sequence FROM PUBLIC`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision supporting table fixture with %q: %v", statement, err)
		}
	}
	indexNumber := postgresExpectedQualificationPlanIndexes
	for _, tableName := range allBoundaryTables {
		for _, columnName := range []string{"id", "value"} {
			indexNumber++
			statement := fmt.Sprintf(
				`CREATE INDEX posture_boundary_%02d ON %s.%s (%s)`,
				indexNumber, schemaName, tableName, columnName,
			)
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("create boundary index fixture with %q: %v", statement, err)
			}
		}
	}
	for indexNumber < postgresExpectedOwnedBoundaryIndexes {
		indexNumber++
		statement := fmt.Sprintf(
			`CREATE INDEX posture_boundary_%02d ON %s.repository_exact_tree_literal_index_blobs ((id + value + %d))`,
			indexNumber, schemaName, indexNumber,
		)
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create supplemental boundary index fixture with %q: %v", statement, err)
		}
	}
	for _, statement := range []string{
		`GRANT SELECT, INSERT ON ` + schemaName + `.repository_exact_tree_literal_index_blobs TO ` + postgresApplicationRole,
		`GRANT SELECT, INSERT ON ` + schemaName + `.repository_exact_tree_literal_index_members TO ` + postgresApplicationRole,
		`GRANT SELECT, INSERT, UPDATE ON ` + schemaName + `.repository_exact_tree_literal_index_manifests TO ` + postgresApplicationRole,
		`GRANT SELECT ON ` + schemaName + `.repository_exact_tree_literal_index_build_claims TO ` + postgresApplicationRole,
		`GRANT SELECT ON ` + schemaName + `.schema_migrations TO ` + postgresApplicationRole,
		`GRANT SELECT, INSERT ON ` + schemaName + `.golden_fault_consume_reservations TO ` + postgresGoldenFaultOperatorRole,
		`GRANT SELECT, INSERT ON ` + schemaName + `.golden_fault_consume_results TO ` + postgresGoldenFaultOperatorRole,
		`GRANT SELECT ON ` + schemaName + `.qualification_promotion_consumptions TO ` + postgresQualificationPromotionOperatorRole,
		`GRANT SELECT ON ` + schemaName + `.qualification_promotion_handoffs TO ` + postgresQualificationPromotionOperatorRole,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ` + schemaName + `.business_records TO ` + postgresApplicationRole,
		`GRANT USAGE, SELECT, UPDATE ON SEQUENCE ` + schemaName + `.business_sequence TO ` + postgresApplicationRole,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("grant application table fixture with %q: %v", statement, err)
		}
	}

	functionDefinitions := []string{
		fmt.Sprintf(`
CREATE FUNCTION %s.acquire_candidate_workspace_lease(
  uuid, bigint, uuid, integer
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.rotate_candidate_workspace_session(
  uuid, bigint, bigint, uuid
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.update_candidate_workspace_flags(
  uuid, bigint, bigint, bigint, uuid, boolean, boolean, boolean, text, text, text
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.freeze_candidate_workspace(
  uuid, bigint, bigint, bigint, uuid, uuid, text
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.abandon_candidate_workspace(
  uuid, bigint, bigint, bigint, uuid, uuid, text
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.abandon_sandbox_session_candidate(
  uuid, uuid, bigint, bigint, bigint, bigint, uuid, uuid, text, uuid
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.complete_abandoned_sandbox_session(
  uuid, bigint, bigint, uuid
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer, integer, bigint, integer
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.renew_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.release_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint
) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.sandbox_checkpoint_is_exact(
  uuid, uuid, uuid, bigint, bigint, bigint, bigint, text, uuid, text, text, text
) RETURNS boolean LANGUAGE sql STABLE SECURITY INVOKER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_repository_exact_tree_literal_index_gc_audit_mutation()
RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_repository_exact_tree_literal_index_blob_mutation()
RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_repository_exact_tree_literal_index_member_mutation()
RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_repository_exact_tree_literal_index_manifest_delete()
RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.lock_candidate_exact_tree_literal_index_reference()
RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_repository_exact_tree_literal_index_manifest_insert()
RETURNS boolean LANGUAGE sql AS $$ SELECT false $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_repository_exact_tree_literal_index_member_insert()
RETURNS boolean LANGUAGE sql AS $$ SELECT false $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.publish_repository_exact_tree_literal_index_manifest()
RETURNS boolean LANGUAGE sql AS $$ SELECT false $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.validate_golden_fault_consume_result()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN NEW; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_golden_fault_ledger_mutation()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN NEW; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_qualification_promotion_mutation()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN NEW; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_model_governance_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN NEW; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.append_model_governance_activation(
  bigint, text, uuid, text, text, uuid, text, text, text, text, text,
  bigint, bigint, text, text, text, text, text, text, text, timestamptz
) RETURNS SETOF %s.model_governance_activation_records
LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.append_model_governance_genesis(
  uuid, text, text, uuid, text, text, text, text, text, bigint, bigint,
  text, text, text, text, text, text, text, text, text, text, bigint,
  text, bigint, timestamptz, text
) RETURNS SETOF %s.model_governance_activation_records
LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.observe_model_governance_revocation_authority(
  bigint, text, bytea, jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.observe_model_governance_trust_policy(
  text, text, bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.enforce_model_governance_activation_authority_anchor()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN NEW; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.consume_verified_qualification_promotion(
  uuid, text, bytea, jsonb, text, text, bytea, jsonb,
  uuid, uuid, text, bytea, jsonb
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.credential_set_sha256(bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
AS $$ SELECT 'sha256:' || repeat('0', 64) $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_credential_set_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_credential_set_head_projection()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.append_credential_set_event(
  text, bytea, jsonb, text, bytea, jsonb,
  text, bytea, jsonb, text, bytea, jsonb
) RETURNS boolean LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.qualification_evidence_sha256(bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ SELECT 'sha256:' || repeat('0', 64) $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_qualification_evidence_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_qualification_evidence_head_projection()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.append_qualification_evidence_event(
  text, bytea, jsonb, text, bytea, jsonb
) RETURNS boolean LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.qualification_plan_sha256(bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ SELECT 'sha256:' || repeat('0', 64) $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_qualification_plan_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.freeze_qualification_plan_authority(
  uuid, uuid, text, bytea, jsonb, text, bytea, jsonb,
  text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb,
  text, bytea, jsonb, text, bytea, jsonb
) RETURNS SETOF %s.qualification_plan_authorities
LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.resolve_qualification_plan_authority(uuid)
RETURNS SETOF %s.qualification_plan_authorities
LANGUAGE sql STABLE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ SELECT * FROM %s.qualification_plan_authorities WHERE false $$`,
			schemaName, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_qualification_evidence_plan_authority()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ BEGIN RETURN NEW; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.plan_repository_exact_tree_literal_index_gc(
  uuid, bigint, integer, integer, integer
) RETURNS TABLE (
  run_id uuid, capability_id uuid, project_id uuid, tree_hash text,
  publication_created_at timestamptz, index_commitment text,
  planned_rank integer, expires_at timestamptz
) LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$
  SELECT NULL::uuid, NULL::uuid, NULL::uuid, NULL::text,
         NULL::timestamptz, NULL::text, NULL::integer, NULL::timestamptz
  WHERE false
$$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.execute_repository_exact_tree_literal_index_gc(uuid)
RETURNS TABLE (
  receipt_id uuid, capability_id uuid, run_id uuid, project_id uuid,
  tree_hash text, publication_created_at timestamptz, index_commitment text,
  outcome text, deleted_member_count integer, deleted_blob_count integer,
  logical_bytes_released bigint, blob_bytes_freed bigint,
  executed_at timestamptz, idempotent boolean
) LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$
  SELECT NULL::uuid, NULL::uuid, NULL::uuid, NULL::uuid, NULL::text,
         NULL::timestamptz, NULL::text, NULL::text, NULL::integer,
         NULL::integer, NULL::bigint, NULL::bigint, NULL::timestamptz,
         NULL::boolean
  WHERE false
$$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.inspect_repository_exact_tree_literal_index_gc_run(uuid)
RETURNS TABLE (
  run_id uuid, run_status text, planned_at timestamptz, cutoff_at timestamptz,
  keep_per_project integer, batch_size integer,
  capability_ttl_milliseconds integer, planned_capability_count integer,
  deleted_capability_count integer, protected_capability_count integer,
  stale_capability_count integer, expired_capability_count integer,
  pending_capability_count integer, logical_bytes_released bigint,
  blob_bytes_freed bigint
) LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$
  SELECT NULL::uuid, NULL::text, NULL::timestamptz, NULL::timestamptz,
         NULL::integer, NULL::integer, NULL::integer, NULL::integer,
         NULL::integer, NULL::integer, NULL::integer, NULL::integer,
         NULL::integer, NULL::bigint, NULL::bigint
  WHERE false
$$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.repository_exact_tree_literal_index_gc_readiness()
RETURNS TABLE (
  ready boolean, reason text, trusted_schema text,
  operator_role_exists boolean, application_role_exists boolean,
  migration_owner_role_exists boolean, stable_group_roles_safe boolean,
  objects_owned_by_migration_owner boolean, operator_execute_granted boolean,
  application_claim_execute_granted boolean,
  application_schema_head_read_granted boolean,
  public_claim_execute_revoked boolean, public_schema_create_revoked boolean
) LANGUAGE sql SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp AS $$
  SELECT NULL::boolean, NULL::text, NULL::text, NULL::boolean, NULL::boolean,
         NULL::boolean, NULL::boolean, NULL::boolean, NULL::boolean,
         NULL::boolean, NULL::boolean, NULL::boolean, NULL::boolean
  WHERE false
$$`, schemaName, schemaName),
	}
	for _, definition := range functionDefinitions {
		if _, err := admin.ExecContext(ctx, definition); err != nil {
			t.Fatalf("create posture function fixture: %v\n%s", err, definition)
		}
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`
CREATE TRIGGER model_governance_activation_authority_anchor
BEFORE INSERT ON %s.model_governance_activation_records
FOR EACH ROW EXECUTE FUNCTION %s.enforce_model_governance_activation_authority_anchor()`, schemaName, schemaName)); err != nil {
		t.Fatalf("create posture Model Governance anchor trigger: %v", err)
	}
	for _, triggerDefinition := range []string{
		fmt.Sprintf(`CREATE TRIGGER credential_set_events_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.credential_set_events
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_credential_set_immutable_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER credential_set_operations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.credential_set_operations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_credential_set_immutable_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER credential_set_heads_guard
BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON %s.credential_set_heads
FOR EACH STATEMENT EXECUTE FUNCTION %s.guard_credential_set_head_projection()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_evidence_events_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_evidence_events
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_evidence_immutable_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_evidence_operations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_evidence_operations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_evidence_immutable_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_evidence_heads_guard
BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON %s.qualification_evidence_heads
FOR EACH STATEMENT EXECUTE FUNCTION %s.guard_qualification_evidence_head_projection()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_plan_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_plan_authorities
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_plan_immutable_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_plan_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_plan_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_plan_immutable_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_evidence_plan_authority_guard
BEFORE INSERT ON %s.qualification_evidence_events
FOR EACH ROW EXECUTE FUNCTION %s.guard_qualification_evidence_plan_authority()`, schemaName, schemaName),
	} {
		if _, err := admin.ExecContext(ctx, triggerDefinition); err != nil {
			t.Fatalf("create posture owner-only ledger trigger fixture: %v\n%s", err, triggerDefinition)
		}
	}

	applicationFunctionReferences := []string{
		"acquire_candidate_workspace_lease(uuid,bigint,uuid,integer)",
		"rotate_candidate_workspace_session(uuid,bigint,bigint,uuid)",
		"update_candidate_workspace_flags(uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text)",
		"freeze_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)",
		"abandon_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)",
		"abandon_sandbox_session_candidate(uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid)",
		"complete_abandoned_sandbox_session(uuid,bigint,bigint,uuid)",
		"acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer,integer,bigint,integer)",
		"renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)",
		"release_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint)",
	}
	gcFunctionReferences := []string{
		"plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)",
		"execute_repository_exact_tree_literal_index_gc(uuid)",
		"inspect_repository_exact_tree_literal_index_gc_run(uuid)",
		"repository_exact_tree_literal_index_gc_readiness()",
	}
	internalSecurityDefinerReferences := []string{
		"guard_repository_exact_tree_literal_index_gc_audit_mutation()",
		"guard_repository_exact_tree_literal_index_blob_mutation()",
		"guard_repository_exact_tree_literal_index_member_mutation()",
		"guard_repository_exact_tree_literal_index_manifest_delete()",
		"lock_candidate_exact_tree_literal_index_reference()",
		"append_model_governance_activation(bigint,text,uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,timestamp with time zone)",
		"append_model_governance_genesis(uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,text,text,text,bigint,text,bigint,timestamp with time zone,text)",
		"observe_model_governance_revocation_authority(bigint,text,bytea,jsonb)",
		"observe_model_governance_trust_policy(text,text,bigint)",
		"consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb)",
		"append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)",
		"append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb)",
		"freeze_qualification_plan_authority(uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)",
	}
	internalInvokerReferences := []string{
		"guard_repository_exact_tree_literal_index_manifest_insert()",
		"guard_repository_exact_tree_literal_index_member_insert()",
		"publish_repository_exact_tree_literal_index_manifest()",
		"validate_golden_fault_consume_result()",
		"reject_golden_fault_ledger_mutation()",
		"reject_model_governance_immutable_mutation()",
		"reject_qualification_promotion_mutation()",
		"enforce_model_governance_activation_authority_anchor()",
		"credential_set_sha256(bytea)",
		"reject_credential_set_immutable_mutation()",
		"guard_credential_set_head_projection()",
		"qualification_evidence_sha256(bytea)",
		"reject_qualification_evidence_immutable_mutation()",
		"guard_qualification_evidence_head_projection()",
		"qualification_plan_sha256(bytea)",
		"reject_qualification_plan_immutable_mutation()",
		"resolve_qualification_plan_authority(uuid)",
		"guard_qualification_evidence_plan_authority()",
	}
	sandboxCheckpointHelperReference := "sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)"
	ownerFunctionReferences := append([]string{}, applicationFunctionReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, gcFunctionReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, internalSecurityDefinerReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, internalInvokerReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, sandboxCheckpointHelperReference)
	for _, functionReference := range ownerFunctionReferences {
		qualified := schemaName + "." + functionReference
		for _, statement := range []string{
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("secure posture function fixture with %q: %v", statement, err)
			}
		}
	}
	for _, functionReference := range applicationFunctionReferences {
		if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schemaName+`.`+functionReference+` TO `+postgresApplicationRole); err != nil {
			t.Fatalf("grant claim function %s: %v", functionReference, err)
		}
	}
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schemaName+`.`+sandboxCheckpointHelperReference+` TO `+postgresApplicationRole); err != nil {
		t.Fatalf("grant sandbox checkpoint helper: %v", err)
	}
	for _, functionReference := range gcFunctionReferences {
		if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schemaName+`.`+functionReference+` TO `+postgresRepositoryIndexGCOperatorRole); err != nil {
			t.Fatalf("grant GC function %s: %v", functionReference, err)
		}
	}
	qualificationPromotionFunctionReference := "consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb)"
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schemaName+`.`+qualificationPromotionFunctionReference+` TO `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("grant qualification promotion consume function: %v", err)
	}

	apiDatabase = openPostgresRolePostureLogin(t, dsn, apiRole, apiPassword, schemaName, "")
	if err := apiDatabase.PingContext(ctx); err != nil {
		t.Fatalf("connect as true API login: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentStaging); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) {
			position := int(postgresError.Position) - 1
			start := position - 120
			if start < 0 {
				start = 0
			}
			end := position + 120
			if end > len(postgresRolePostureQuery) {
				end = len(postgresRolePostureQuery)
			}
			t.Fatalf("safe real PostgreSQL API login catalog query failed at byte %d near %q: %+v", postgresError.Position, postgresRolePostureQuery[start:end], postgresError)
		}
		t.Fatalf("safe real PostgreSQL API login rejected: %v", err)
	}
	unexpectedPromotionTableACL := `SELECT ON ` + schemaName + `.business_records`
	if _, err := admin.ExecContext(ctx, `GRANT `+unexpectedPromotionTableACL+` TO `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("grant qualification-promotion operator an out-of-bound table ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "SELECT-only operator ACL") {
		t.Fatalf("qualification-promotion out-of-bound table ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE `+unexpectedPromotionTableACL+` FROM `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("revoke qualification-promotion operator out-of-bound table ACL: %v", err)
	}
	unexpectedPromotionFunction := schemaName + "." + applicationFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+unexpectedPromotionFunction+` TO `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("grant qualification-promotion operator an out-of-bound routine ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "qualification promotion consume routine") {
		t.Fatalf("qualification-promotion out-of-bound routine ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION `+unexpectedPromotionFunction+` FROM `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("revoke qualification-promotion operator out-of-bound routine ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-qualification-promotion ACL restore posture rejected: %v", err)
	}
	credentialSHA := schemaName + ".credential_set_sha256(bytea)"
	credentialReject := schemaName + ".reject_credential_set_immutable_mutation()"
	credentialGuard := schemaName + ".guard_credential_set_head_projection()"
	credentialAppend := schemaName + ".append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)"
	credentialSetContractMutations := []struct {
		name       string
		mutate     []string
		restore    []string
		wantDetail string
	}{
		{
			name:       "CredentialSet arbitrary table grantee",
			mutate:     []string{`GRANT SELECT ON ` + schemaName + `.credential_set_events TO ` + bypassRole},
			restore:    []string{`REVOKE SELECT ON ` + schemaName + `.credential_set_events FROM ` + bypassRole},
			wantDetail: "migration-owner-only",
		},
		{
			name: "CredentialSet SHA-256 volatility and config",
			mutate: []string{
				`ALTER FUNCTION ` + credentialSHA + ` STABLE`,
				`ALTER FUNCTION ` + credentialSHA + ` SET search_path TO pg_catalog`,
			},
			restore: []string{
				`ALTER FUNCTION ` + credentialSHA + ` IMMUTABLE`,
				`ALTER FUNCTION ` + credentialSHA + ` RESET ALL`,
			},
			wantDetail: "CredentialSet SHA-256",
		},
		{
			name:       "CredentialSet reject security mode",
			mutate:     []string{`ALTER FUNCTION ` + credentialReject + ` SECURITY DEFINER`},
			restore:    []string{`ALTER FUNCTION ` + credentialReject + ` SECURITY INVOKER`},
			wantDetail: "CredentialSet SHA-256",
		},
		{
			name:       "CredentialSet guard path",
			mutate:     []string{`ALTER FUNCTION ` + credentialGuard + ` SET search_path TO pg_catalog, ` + schemaName + `, pg_temp`},
			restore:    []string{`ALTER FUNCTION ` + credentialGuard + ` SET search_path TO pg_catalog, ` + schemaName},
			wantDetail: "CredentialSet SHA-256",
		},
		{
			name:       "CredentialSet append security mode",
			mutate:     []string{`ALTER FUNCTION ` + credentialAppend + ` SECURITY INVOKER`},
			restore:    []string{`ALTER FUNCTION ` + credentialAppend + ` SECURITY DEFINER`},
			wantDetail: "CredentialSet SHA-256",
		},
		{
			name:       "CredentialSet arbitrary function grantee",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + credentialSHA + ` TO ` + bypassRole},
			restore:    []string{`REVOKE EXECUTE ON FUNCTION ` + credentialSHA + ` FROM ` + bypassRole},
			wantDetail: "owner-only ACL",
		},
		{
			name:       "CredentialSet disabled head trigger",
			mutate:     []string{`ALTER TABLE ` + schemaName + `.credential_set_heads DISABLE TRIGGER credential_set_heads_guard`},
			restore:    []string{`ALTER TABLE ` + schemaName + `.credential_set_heads ENABLE TRIGGER credential_set_heads_guard`},
			wantDetail: "trigger contracts",
		},
		{
			name: "CredentialSet unexpected projection trigger",
			mutate: []string{`CREATE TRIGGER credential_set_projection_authorizations_unexpected
BEFORE UPDATE ON ` + schemaName + `.credential_set_projection_authorizations
FOR EACH STATEMENT EXECUTE FUNCTION ` + schemaName + `.reject_credential_set_immutable_mutation()`},
			restore:    []string{`DROP TRIGGER credential_set_projection_authorizations_unexpected ON ` + schemaName + `.credential_set_projection_authorizations`},
			wantDetail: "trigger contracts",
		},
	}
	for _, mutation := range credentialSetContractMutations {
		for _, statement := range mutation.mutate {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply %s drift with %q: %v", mutation.name, statement, err)
			}
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), mutation.wantDetail) {
			t.Fatalf("%s posture error = %v", mutation.name, err)
		}
		for _, statement := range mutation.restore {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore %s drift with %q: %v", mutation.name, statement, err)
			}
		}
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-CredentialSet-contract restore posture rejected: %v", err)
	}
	qualificationEvidenceSHA := schemaName + ".qualification_evidence_sha256(bytea)"
	qualificationEvidenceAppend := schemaName + ".append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb)"
	qualificationEvidenceContractMutations := []struct {
		name       string
		mutate     []string
		restore    []string
		wantDetail string
	}{
		{
			name:       "Qualification Evidence arbitrary table grantee",
			mutate:     []string{`GRANT SELECT ON ` + schemaName + `.qualification_evidence_events TO ` + bypassRole},
			restore:    []string{`REVOKE SELECT ON ` + schemaName + `.qualification_evidence_events FROM ` + bypassRole},
			wantDetail: "Qualification Evidence tables",
		},
		{
			name:       "Qualification Evidence SHA-256 configuration",
			mutate:     []string{`ALTER FUNCTION ` + qualificationEvidenceSHA + ` RESET ALL`},
			restore:    []string{`ALTER FUNCTION ` + qualificationEvidenceSHA + ` SET search_path TO pg_catalog`},
			wantDetail: "Qualification Evidence SHA-256",
		},
		{
			name:       "Qualification Evidence append security mode",
			mutate:     []string{`ALTER FUNCTION ` + qualificationEvidenceAppend + ` SECURITY INVOKER`},
			restore:    []string{`ALTER FUNCTION ` + qualificationEvidenceAppend + ` SECURITY DEFINER`},
			wantDetail: "Qualification Evidence SHA-256",
		},
		{
			name:       "Qualification Evidence arbitrary function grantee",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + qualificationEvidenceSHA + ` TO ` + bypassRole},
			restore:    []string{`REVOKE EXECUTE ON FUNCTION ` + qualificationEvidenceSHA + ` FROM ` + bypassRole},
			wantDetail: "owner-only",
		},
		{
			name:       "Qualification Evidence disabled head trigger",
			mutate:     []string{`ALTER TABLE ` + schemaName + `.qualification_evidence_heads DISABLE TRIGGER qualification_evidence_heads_guard`},
			restore:    []string{`ALTER TABLE ` + schemaName + `.qualification_evidence_heads ENABLE TRIGGER qualification_evidence_heads_guard`},
			wantDetail: "Qualification Evidence events",
		},
		{
			name: "Qualification Evidence unexpected projection trigger",
			mutate: []string{`CREATE TRIGGER qualification_evidence_projection_authorizations_unexpected
BEFORE UPDATE ON ` + schemaName + `.qualification_evidence_projection_authorizations
FOR EACH STATEMENT EXECUTE FUNCTION ` + schemaName + `.reject_qualification_evidence_immutable_mutation()`},
			restore:    []string{`DROP TRIGGER qualification_evidence_projection_authorizations_unexpected ON ` + schemaName + `.qualification_evidence_projection_authorizations`},
			wantDetail: "Qualification Evidence events",
		},
	}
	for _, mutation := range qualificationEvidenceContractMutations {
		for _, statement := range mutation.mutate {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply %s drift with %q: %v", mutation.name, statement, err)
			}
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), mutation.wantDetail) {
			t.Fatalf("%s posture error = %v", mutation.name, err)
		}
		for _, statement := range mutation.restore {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore %s drift with %q: %v", mutation.name, statement, err)
			}
		}
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-Qualification-Evidence-contract restore posture rejected: %v", err)
	}
	qualificationPlanSHA := schemaName + ".qualification_plan_sha256(bytea)"
	qualificationPlanFreeze := schemaName + ".freeze_qualification_plan_authority(uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)"
	qualificationPlanResolve := schemaName + ".resolve_qualification_plan_authority(uuid)"
	qualificationPlanGuard := schemaName + ".guard_qualification_evidence_plan_authority()"
	qualificationPlanContractMutations := []struct {
		name       string
		mutate     []string
		restore    []string
		wantDetail string
	}{
		{
			name:       "Qualification Plan table ACL",
			mutate:     []string{`GRANT SELECT ON ` + schemaName + `.qualification_plan_authorities TO ` + bypassRole},
			restore:    []string{`REVOKE SELECT ON ` + schemaName + `.qualification_plan_authorities FROM ` + bypassRole},
			wantDetail: "Qualification Plan authority tables",
		},
		{
			name:   "Qualification Plan function owner",
			mutate: []string{`ALTER FUNCTION ` + qualificationPlanGuard + ` OWNER TO ` + ownerRole},
			restore: []string{
				`ALTER FUNCTION ` + qualificationPlanGuard + ` OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + qualificationPlanGuard + ` FROM ` + ownerRole,
				`REVOKE ALL ON FUNCTION ` + qualificationPlanGuard + ` FROM PUBLIC`,
			},
			wantDetail: "Qualification Plan SHA-256",
		},
		{
			name:       "Qualification Plan function ACL",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + qualificationPlanSHA + ` TO ` + bypassRole},
			restore:    []string{`REVOKE EXECUTE ON FUNCTION ` + qualificationPlanSHA + ` FROM ` + bypassRole},
			wantDetail: "Qualification Plan SHA-256",
		},
		{
			name:       "Qualification Plan disabled Evidence guard",
			mutate:     []string{`ALTER TABLE ` + schemaName + `.qualification_evidence_events DISABLE TRIGGER qualification_evidence_plan_authority_guard`},
			restore:    []string{`ALTER TABLE ` + schemaName + `.qualification_evidence_events ENABLE TRIGGER qualification_evidence_plan_authority_guard`},
			wantDetail: "authority-guard trigger",
		},
		{
			name:       "Qualification Plan resolve search path",
			mutate:     []string{`ALTER FUNCTION ` + qualificationPlanResolve + ` SET search_path TO pg_catalog`},
			restore:    []string{`ALTER FUNCTION ` + qualificationPlanResolve + ` SET search_path TO pg_catalog, ` + schemaName},
			wantDetail: "Qualification Plan SHA-256",
		},
		{
			name:       "Qualification Plan freeze security mode",
			mutate:     []string{`ALTER FUNCTION ` + qualificationPlanFreeze + ` SECURITY INVOKER`},
			restore:    []string{`ALTER FUNCTION ` + qualificationPlanFreeze + ` SECURITY DEFINER`},
			wantDetail: "Qualification Plan SHA-256",
		},
		{
			name: "Qualification Plan extra named routine",
			mutate: []string{
				`CREATE FUNCTION ` + schemaName + `.qualification_plan_unexpected() RETURNS boolean LANGUAGE sql SECURITY INVOKER SET search_path TO pg_catalog AS $$ SELECT false $$`,
				`ALTER FUNCTION ` + schemaName + `.qualification_plan_unexpected() OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + schemaName + `.qualification_plan_unexpected() FROM PUBLIC`,
			},
			restore:    []string{`DROP FUNCTION ` + schemaName + `.qualification_plan_unexpected()`},
			wantDetail: "Qualification Plan SHA-256",
		},
	}
	for _, mutation := range qualificationPlanContractMutations {
		for _, statement := range mutation.mutate {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply %s drift with %q: %v", mutation.name, statement, err)
			}
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), mutation.wantDetail) {
			t.Fatalf("%s posture error = %v", mutation.name, err)
		}
		for _, statement := range mutation.restore {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore %s drift with %q: %v", mutation.name, statement, err)
			}
		}
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-Qualification-Plan-contract restore posture rejected: %v", err)
	}
	helperFunction := schemaName + "." + sandboxCheckpointHelperReference
	internalFunction := schemaName + "." + internalInvokerReferences[0]
	routineContractMutations := []struct {
		name       string
		mutate     []string
		restore    []string
		wantDetail string
	}{
		{
			name:       "internal routine arbitrary grantee",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + internalFunction + ` TO ` + bypassRole},
			restore:    []string{`REVOKE EXECUTE ON FUNCTION ` + internalFunction + ` FROM ` + bypassRole},
			wantDetail: "owner-only EXECUTE",
		},
		{
			name:       "helper arbitrary grantee",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + helperFunction + ` TO ` + bypassRole},
			restore:    []string{`REVOKE EXECUTE ON FUNCTION ` + helperFunction + ` FROM ` + bypassRole},
			wantDetail: "sandbox checkpoint helper",
		},
		{
			name:       "helper application grant option",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + helperFunction + ` TO ` + postgresApplicationRole + ` WITH GRANT OPTION`},
			restore:    []string{`REVOKE GRANT OPTION FOR EXECUTE ON FUNCTION ` + helperFunction + ` FROM ` + postgresApplicationRole},
			wantDetail: "sandbox checkpoint helper",
		},
		{
			name:       "helper volatility",
			mutate:     []string{`ALTER FUNCTION ` + helperFunction + ` VOLATILE`},
			restore:    []string{`ALTER FUNCTION ` + helperFunction + ` STABLE`},
			wantDetail: "SQL/STABLE SECURITY INVOKER",
		},
		{
			name:       "helper security mode",
			mutate:     []string{`ALTER FUNCTION ` + helperFunction + ` SECURITY DEFINER`},
			restore:    []string{`ALTER FUNCTION ` + helperFunction + ` SECURITY INVOKER`},
			wantDetail: "SQL/STABLE SECURITY INVOKER",
		},
		{
			name:       "helper search path",
			mutate:     []string{`ALTER FUNCTION ` + helperFunction + ` SET search_path TO ` + schemaName + `, pg_temp`},
			restore:    []string{`ALTER FUNCTION ` + helperFunction + ` SET search_path TO pg_catalog, ` + schemaName + `, pg_temp`},
			wantDetail: "fixed search_path",
		},
	}
	for _, mutation := range routineContractMutations {
		for _, statement := range mutation.mutate {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply %s drift with %q: %v", mutation.name, statement, err)
			}
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), mutation.wantDetail) {
			t.Fatalf("%s posture error = %v", mutation.name, err)
		}
		for _, statement := range mutation.restore {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore %s drift with %q: %v", mutation.name, statement, err)
			}
		}
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-routine-contract restore posture rejected: %v", err)
	}
	applicationColumnACL := `UPDATE(value) ON ` + schemaName + `.business_records`
	if _, err := admin.ExecContext(ctx, `GRANT `+applicationColumnACL+` TO `+postgresApplicationRole); err != nil {
		t.Fatalf("grant application column privilege: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "must not contain column-level ACLs") {
		t.Fatalf("application column ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE `+applicationColumnACL+` FROM `+postgresApplicationRole); err != nil {
		t.Fatalf("revoke application column privilege: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-column-ACL restore posture rejected: %v", err)
	}
	hiddenMutations := []struct {
		name       string
		grant      string
		revoke     string
		wantDetail string
	}{
		{
			name:       "schema migrations update",
			grant:      `GRANT UPDATE ON ` + schemaName + `.schema_migrations TO ` + hiddenRole,
			revoke:     `REVOKE UPDATE ON ` + schemaName + `.schema_migrations FROM ` + hiddenRole,
			wantDetail: "reachable non-application",
		},
		{
			name:       "trusted schema create",
			grant:      `GRANT CREATE ON SCHEMA ` + schemaName + ` TO ` + hiddenRole,
			revoke:     `REVOKE CREATE ON SCHEMA ` + schemaName + ` FROM ` + hiddenRole,
			wantDetail: "create objects",
		},
		{
			name:       "GC private table select",
			grant:      `GRANT SELECT ON ` + schemaName + `.repository_exact_tree_literal_index_gc_runs TO ` + hiddenRole,
			revoke:     `REVOKE SELECT ON ` + schemaName + `.repository_exact_tree_literal_index_gc_runs FROM ` + hiddenRole,
			wantDetail: "reachable non-application",
		},
		{
			name:       "business table delete",
			grant:      `GRANT DELETE ON ` + schemaName + `.business_records TO ` + hiddenRole,
			revoke:     `REVOKE DELETE ON ` + schemaName + `.business_records FROM ` + hiddenRole,
			wantDetail: "reachable non-application",
		},
		{
			name:       "business column update",
			grant:      `GRANT UPDATE(value) ON ` + schemaName + `.business_records TO ` + hiddenRole,
			revoke:     `REVOKE UPDATE(value) ON ` + schemaName + `.business_records FROM ` + hiddenRole,
			wantDetail: "reachable non-application",
		},
		{
			name:       "business sequence usage",
			grant:      `GRANT USAGE ON SEQUENCE ` + schemaName + `.business_sequence TO ` + hiddenRole,
			revoke:     `REVOKE USAGE ON SEQUENCE ` + schemaName + `.business_sequence FROM ` + hiddenRole,
			wantDetail: "reachable non-application",
		},
		{
			name:       "application definer execute",
			grant:      `GRANT EXECUTE ON FUNCTION ` + schemaName + `.` + applicationFunctionReferences[0] + ` TO ` + hiddenRole,
			revoke:     `REVOKE EXECUTE ON FUNCTION ` + schemaName + `.` + applicationFunctionReferences[0] + ` FROM ` + hiddenRole,
			wantDetail: "reachable non-application",
		},
	}
	for _, mutation := range hiddenMutations {
		if _, err := admin.ExecContext(ctx, mutation.grant); err != nil {
			t.Fatalf("grant hidden-role %s privilege: %v", mutation.name, err)
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), mutation.wantDetail) {
			t.Fatalf("hidden-role %s posture error = %v", mutation.name, err)
		}
		if _, err := admin.ExecContext(ctx, mutation.revoke); err != nil {
			t.Fatalf("revoke hidden-role %s privilege: %v", mutation.name, err)
		}
	}
	publicDriftFunction := schemaName + `.posture_public_drift()`
	for _, statement := range []string{
		`CREATE FUNCTION ` + publicDriftFunction + ` RETURNS boolean LANGUAGE sql AS $$ SELECT true $$`,
		`ALTER FUNCTION ` + publicDriftFunction + ` OWNER TO ` + postgresMigrationOwnerRole,
		`GRANT EXECUTE ON FUNCTION ` + publicDriftFunction + ` TO PUBLIC`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision PUBLIC routine drift with %q: %v", statement, err)
		}
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "PUBLIC can execute trusted-schema") {
		t.Fatalf("PUBLIC routine drift error = %v", err)
	}
	for _, statement := range []string{
		`REVOKE ALL ON FUNCTION ` + publicDriftFunction + ` FROM PUBLIC`,
		`DROP FUNCTION ` + publicDriftFunction,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("remove PUBLIC routine drift with %q: %v", statement, err)
		}
	}
	elevatedOwnerFunction := schemaName + "." + applicationFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+elevatedOwnerFunction+` OWNER TO `+elevatedOwnerRole); err != nil {
		t.Fatalf("assign elevated migration-member function owner: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "owned exactly") {
		t.Fatalf("elevated migration-member owner error = %v", err)
	}
	for _, statement := range []string{
		`ALTER FUNCTION ` + elevatedOwnerFunction + ` OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON FUNCTION ` + elevatedOwnerFunction + ` FROM ` + elevatedOwnerRole,
		`REVOKE ALL ON FUNCTION ` + elevatedOwnerFunction + ` FROM PUBLIC`,
		`GRANT EXECUTE ON FUNCTION ` + elevatedOwnerFunction + ` TO ` + postgresApplicationRole,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("restore exact migration-owner function with %q: %v", statement, err)
		}
	}
	arbitraryApplicationFunction := schemaName + "." + applicationFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+arbitraryApplicationFunction+` TO `+bypassRole); err != nil {
		t.Fatalf("grant application function to unexpected role: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "unexpected roles") {
		t.Fatalf("unexpected application function grantee error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION `+arbitraryApplicationFunction+` FROM `+bypassRole); err != nil {
		t.Fatalf("restore application function ACL: %v", err)
	}
	arbitraryGCFunction := schemaName + "." + gcFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+arbitraryGCFunction+` TO `+bypassRole); err != nil {
		t.Fatalf("grant GC function to unexpected role: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "outside the operator") {
		t.Fatalf("unexpected GC function grantee error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION `+arbitraryGCFunction+` FROM `+bypassRole); err != nil {
		t.Fatalf("restore GC function ACL: %v", err)
	}
	if containsPostgresRole(createdStableRoles, postgresApplicationRole) {
		if _, err := admin.ExecContext(ctx, `ALTER ROLE `+postgresApplicationRole+` LOGIN`); err != nil {
			t.Fatalf("weaken stable application role: %v", err)
		}
		stableRoleRestored := false
		defer func() {
			if !stableRoleRestored {
				_, _ = admin.ExecContext(context.Background(), `ALTER ROLE `+postgresApplicationRole+` NOLOGIN`)
			}
		}()
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "isolated NOLOGIN") {
			t.Fatalf("unsafe stable group role error = %v", err)
		}
		if _, err := admin.ExecContext(ctx, `ALTER ROLE `+postgresApplicationRole+` NOLOGIN`); err != nil {
			t.Fatalf("restore stable application role: %v", err)
		}
		stableRoleRestored = true
	}

	bypassDatabase = openPostgresRolePostureLogin(t, dsn, bypassRole, bypassPassword, schemaName, apiRole)
	if err := bypassDatabase.PingContext(ctx); err != nil {
		t.Fatalf("connect with startup role override: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, bypassDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "session role differs") {
		t.Fatalf("startup role/session bypass error = %v", err)
	}

	privateTable := schemaName + ".repository_exact_tree_literal_index_gc_runs"
	if _, err := admin.ExecContext(ctx, `GRANT SELECT ON `+privateTable+` TO `+postgresApplicationRole); err != nil {
		t.Fatalf("grant dangerous private-table ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "GC private tables") {
		t.Fatalf("dangerous private-table ACL error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE SELECT ON `+privateTable+` FROM `+postgresApplicationRole); err != nil {
		t.Fatalf("restore private-table ACL: %v", err)
	}

	faultReservationTable := schemaName + ".golden_fault_consume_reservations"
	if _, err := admin.ExecContext(ctx, `GRANT SELECT ON `+faultReservationTable+` TO `+postgresApplicationRole); err != nil {
		t.Fatalf("grant dangerous Golden fault ledger ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) ||
		(!strings.Contains(err.Error(), "Golden fault consume tables") && !strings.Contains(err.Error(), "protected-table")) {
		t.Fatalf("dangerous Golden fault ledger ACL error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE SELECT ON `+faultReservationTable+` FROM `+postgresApplicationRole); err != nil {
		t.Fatalf("restore Golden fault ledger ACL: %v", err)
	}

	blobTable := schemaName + ".repository_exact_tree_literal_index_blobs"
	if _, err := admin.ExecContext(ctx, `GRANT UPDATE ON `+blobTable+` TO `+postgresApplicationRole); err != nil {
		t.Fatalf("grant excessive blob ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "table privileges") {
		t.Fatalf("excessive application table ACL error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE UPDATE ON `+blobTable+` FROM `+postgresApplicationRole); err != nil {
		t.Fatalf("restore blob ACL: %v", err)
	}

	planFunction := schemaName + "." + gcFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` OWNER TO `+apiRole); err != nil {
		t.Fatalf("assign forbidden GC function ownership: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "migration-owner") {
		t.Fatalf("forbidden GC function ownership error = %v", err)
	}
	for _, statement := range []string{
		`ALTER FUNCTION ` + planFunction + ` OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON FUNCTION ` + planFunction + ` FROM ` + apiRole,
		`REVOKE ALL ON FUNCTION ` + planFunction + ` FROM PUBLIC`,
		`GRANT EXECUTE ON FUNCTION ` + planFunction + ` TO ` + postgresRepositoryIndexGCOperatorRole,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("restore GC function ownership with %q: %v", statement, err)
		}
	}

	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` SECURITY INVOKER`); err != nil {
		t.Fatalf("weaken GC function security mode: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "SECURITY DEFINER") {
		t.Fatalf("GC security mode error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` SECURITY DEFINER`); err != nil {
		t.Fatalf("restore GC function security mode: %v", err)
	}

	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` SET search_path TO `+schemaName+`, pg_temp`); err != nil {
		t.Fatalf("weaken GC function search path: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "search_path") {
		t.Fatalf("GC search-path error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+planFunction+` SET search_path TO pg_catalog, `+schemaName+`, pg_temp`); err != nil {
		t.Fatalf("restore GC function search path: %v", err)
	}

	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("restored safe PostgreSQL posture rejected: %v", err)
	}

	migratedSchemaName := "posture_migrated_" + suffix
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+migratedSchemaName+` AUTHORIZATION `+ownerRole); err != nil {
		t.Fatalf("create migrated posture schema: %v", err)
	}
	migrationDatabase := openPostgresRolePostureLogin(t, dsn, connectionConfigUser(t, dsn), connectionConfigPassword(t, dsn), migratedSchemaName, "")
	migratedAPIDatabase := openPostgresRolePostureLogin(t, dsn, apiRole, apiPassword, migratedSchemaName, "")
	defer func() {
		_ = migratedAPIDatabase.Close()
		_ = migrationDatabase.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+migratedSchemaName+` CASCADE`)
	}()
	if err := migrations.Up(ctx, migrationDatabase); err != nil {
		t.Fatalf("apply real migrations 1-74 for API posture: %v", err)
	}
	if err := migratedAPIDatabase.PingContext(ctx); err != nil {
		t.Fatalf("connect API login to migrated schema: %v", err)
	}
	migratedFacts, err := scanPostgresRolePosture(
		migratedAPIDatabase.QueryRowContext(ctx, postgresRolePostureQuery).Scan,
	)
	if err != nil {
		t.Fatalf("scan real migrated-schema posture facts: %v", err)
	}
	if migratedFacts.protectedTableCount != postgresExpectedProtectedTables ||
		migratedFacts.ownedBoundaryTableCount != postgresExpectedOwnedBoundaryTables ||
		migratedFacts.ownedBoundaryIndexCount != postgresExpectedOwnedBoundaryIndexes ||
		migratedFacts.ownedBoundaryRoutineCount != postgresExpectedOwnedBoundaryRoutines ||
		migratedFacts.internalFunctionCount != postgresExpectedInternalFunctions ||
		migratedFacts.securityDefinerFunctionCount != postgresExpectedSecurityDefinerFunctions ||
		migratedFacts.qualificationEvidenceTableCount != postgresExpectedQualificationEvidenceTables ||
		migratedFacts.qualificationEvidenceFunctionCount != postgresExpectedQualificationEvidenceFunctions ||
		migratedFacts.qualificationEvidenceNamedFunctionCount != postgresExpectedQualificationEvidenceNamedFunctions ||
		migratedFacts.qualificationEvidenceTriggerCount != postgresExpectedQualificationEvidenceTotalTriggers ||
		migratedFacts.qualificationPlanTableCount != postgresExpectedQualificationPlanTables ||
		migratedFacts.qualificationPlanIndexCount != postgresExpectedQualificationPlanIndexes ||
		migratedFacts.qualificationPlanFunctionCount != postgresExpectedQualificationPlanFunctions ||
		migratedFacts.qualificationPlanTriggerCount != postgresExpectedQualificationPlanTriggers {
		t.Fatalf(
			"real migrated catalog counts = protected:%d tables:%d indexes:%d routines:%d internal:%d definers:%d evidence-tables:%d evidence-functions:%d evidence-named-functions:%d evidence-triggers:%d plan-tables:%d plan-indexes:%d plan-functions:%d plan-triggers:%d",
			migratedFacts.protectedTableCount,
			migratedFacts.ownedBoundaryTableCount,
			migratedFacts.ownedBoundaryIndexCount,
			migratedFacts.ownedBoundaryRoutineCount,
			migratedFacts.internalFunctionCount,
			migratedFacts.securityDefinerFunctionCount,
			migratedFacts.qualificationEvidenceTableCount,
			migratedFacts.qualificationEvidenceFunctionCount,
			migratedFacts.qualificationEvidenceNamedFunctionCount,
			migratedFacts.qualificationEvidenceTriggerCount,
			migratedFacts.qualificationPlanTableCount,
			migratedFacts.qualificationPlanIndexCount,
			migratedFacts.qualificationPlanFunctionCount,
			migratedFacts.qualificationPlanTriggerCount,
		)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, migratedAPIDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("real migrated-schema API posture rejected: %v", err)
	}
}

func openPostgresRolePostureLogin(
	t *testing.T,
	baseDSN string,
	role string,
	password string,
	schema string,
	startupRole string,
) *sql.DB {
	t.Helper()
	connectionConfig, err := pgx.ParseConfig(baseDSN)
	if err != nil {
		t.Fatalf("parse PostgreSQL test DSN: %v", err)
	}
	connectionConfig.User = role
	connectionConfig.Password = password
	connectionConfig.RuntimeParams["search_path"] = schema
	if startupRole != "" {
		connectionConfig.RuntimeParams["role"] = startupRole
	}
	database := stdlib.OpenDB(*connectionConfig)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	return database
}

func containsPostgresRole(roles []string, target string) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func connectionConfigUser(t *testing.T, dsn string) string {
	t.Helper()
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL test DSN user: %v", err)
	}
	return connectionConfig.User
}

func connectionConfigPassword(t *testing.T, dsn string) string {
	t.Helper()
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL test DSN password: %v", err)
	}
	return connectionConfig.Password
}
