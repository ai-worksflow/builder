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
		faultOperatorHasSchemaUsage:                           true,
		qualificationOperatorHasSchemaUsage:                   true,
		qualificationPolicyOperatorHasSchemaUsage:             true,
		qualificationInputPrecommitOperatorHasSchemaUsage:     true,
		qualificationSourceVerifierOperatorHasSchemaUsage:     true,
		qualificationCredentialResolverOperatorHasSchemaUsage: true,
		qualificationHandoffOperatorHasSchemaUsage:            true,
		reachableRoleCount:                                    2, isApplicationRoleReachable: true,
		stableGroupRoleCount: 10, isApplicationRoleMember: true,
		tableCount:                                            postgresExpectedRepositoryIndexTables,
		applicationExactTableACLCount:                         postgresExpectedRepositoryIndexTables,
		apiExactTableACLCount:                                 postgresExpectedRepositoryIndexTables,
		gcPrivateTableCount:                                   postgresExpectedRepositoryGCPrivateTables,
		goldenFaultTableCount:                                 postgresExpectedGoldenFaultTables,
		exactGoldenFaultOperatorTableACLCount:                 postgresExpectedGoldenFaultTables,
		qualificationPromotionTableCount:                      postgresExpectedQualificationPromotionTables,
		exactQualificationPromotionTableACLCount:              postgresExpectedQualificationPromotionTables,
		qualificationPromotionNamedTableCount:                 postgresExpectedQualificationPromotionNamedTables,
		qualificationPromotionTriggerCount:                    postgresExpectedQualificationPromotionTriggers,
		exactQualificationPromotionTriggerContractCount:       postgresExpectedQualificationPromotionTriggers,
		qualificationPromotionNamedTriggerCount:               postgresExpectedQualificationPromotionNamedTriggers,
		qualificationHandoffTableCount:                        postgresExpectedQualificationHandoffTables,
		exactQualificationHandoffTableContractCount:           postgresExpectedQualificationHandoffTables,
		qualificationHandoffIndexCount:                        postgresExpectedQualificationHandoffIndexes,
		exactQualificationHandoffIndexContractCount:           postgresExpectedQualificationHandoffIndexes,
		qualificationHandoffNamedIndexCount:                   postgresExpectedQualificationHandoffIndexes,
		qualificationHandoffTriggerCount:                      postgresExpectedQualificationHandoffTriggers,
		exactQualificationHandoffTriggerContractCount:         postgresExpectedQualificationHandoffTriggers,
		qualificationHandoffNamedTriggerCount:                 postgresExpectedQualificationHandoffTriggers,
		qualificationInputTableCount:                          postgresExpectedQualificationInputTables,
		exactQualificationInputTableContractCount:             postgresExpectedQualificationInputTables,
		qualificationInputNamedTableCount:                     postgresExpectedQualificationInputTables,
		qualificationInputIndexCount:                          postgresExpectedQualificationInputIndexes,
		exactQualificationInputIndexContractCount:             postgresExpectedQualificationInputIndexes,
		qualificationInputTriggerCount:                        postgresExpectedQualificationInputTriggers,
		exactQualificationInputTriggerContractCount:           postgresExpectedQualificationInputTriggers,
		qualificationInputNamedTriggerCount:                   postgresExpectedQualificationInputTriggers,
		credentialSetTableCount:                               postgresExpectedCredentialSetTables,
		exactCredentialSetTableContractCount:                  postgresExpectedCredentialSetTables,
		credentialSetNamedTableCount:                          postgresExpectedCredentialSetTables,
		exactCredentialSetTriggerContractCount:                postgresExpectedCredentialSetTriggers,
		credentialSetTriggerCount:                             postgresExpectedCredentialSetTriggers,
		qualificationEvidenceTableCount:                       postgresExpectedQualificationEvidenceTables,
		exactQualificationEvidenceTableContractCount:          postgresExpectedQualificationEvidenceTables,
		qualificationEvidenceNamedTableCount:                  postgresExpectedQualificationEvidenceTables,
		exactQualificationEvidenceTriggerContractCount:        postgresExpectedQualificationEvidenceTriggers,
		qualificationEvidenceTriggerCount:                     postgresExpectedQualificationEvidenceTotalTriggers,
		qualificationPlanTableCount:                           postgresExpectedQualificationPlanTables,
		exactQualificationPlanTableContractCount:              postgresExpectedQualificationPlanTables,
		qualificationPlanNamedTableCount:                      postgresExpectedQualificationPlanTables,
		qualificationPlanIndexCount:                           postgresExpectedQualificationPlanIndexes,
		exactQualificationPlanIndexContractCount:              postgresExpectedQualificationPlanIndexes,
		qualificationPlanNamedIndexCount:                      postgresExpectedQualificationPlanIndexes,
		exactQualificationPlanTriggerContractCount:            postgresExpectedQualificationPlanTriggers,
		qualificationPlanTriggerCount:                         postgresExpectedQualificationPlanTriggers,
		qualificationReceiptV3TableCount:                      postgresExpectedQualificationReceiptV3Tables,
		exactQualificationReceiptV3TableContractCount:         postgresExpectedQualificationReceiptV3Tables,
		qualificationReceiptV3NamedTableCount:                 postgresExpectedQualificationReceiptV3Tables,
		qualificationReceiptV3IndexCount:                      postgresExpectedQualificationReceiptV3Indexes,
		exactQualificationReceiptV3IndexContractCount:         postgresExpectedQualificationReceiptV3Indexes,
		qualificationReceiptV3NamedIndexCount:                 postgresExpectedQualificationReceiptV3Indexes,
		exactQualificationReceiptV3TriggerContractCount:       postgresExpectedQualificationReceiptV3Triggers,
		qualificationReceiptV3TriggerCount:                    postgresExpectedQualificationReceiptV3Triggers,
		canonicalReviewTableCount:                             postgresExpectedCanonicalReviewTables,
		exactCanonicalReviewTableContractCount:                postgresExpectedCanonicalReviewTables,
		canonicalReviewNamedTableCount:                        postgresExpectedCanonicalReviewTables,
		canonicalReviewIndexCount:                             postgresExpectedCanonicalReviewIndexes,
		exactCanonicalReviewIndexContractCount:                postgresExpectedCanonicalReviewIndexes,
		canonicalReviewNamedIndexCount:                        postgresExpectedCanonicalReviewIndexes,
		exactCanonicalReviewTriggerContractCount:              postgresExpectedCanonicalReviewTriggers,
		canonicalReviewTriggerCount:                           postgresExpectedCanonicalReviewTriggers,
		protectedTableCount:                                   postgresExpectedProtectedTables,
		applicationExactProtectedTableACLCount:                postgresExpectedProtectedTables,
		applicationFunctionCount:                              postgresExpectedApplicationFunctions,
		securityDefinerApplicationFunctionCount:               postgresExpectedApplicationFunctions,
		migrationOwnedApplicationFunctionCount:                postgresExpectedApplicationFunctions,
		fixedSearchPathApplicationFunctionCount:               postgresExpectedApplicationFunctions,
		applicationBoundaryExecuteCount:                       postgresExpectedApplicationFunctions,
		apiApplicationFunctionExecuteCount:                    postgresExpectedApplicationFunctions,
		workflowInputApplicationFunctionCount:                 postgresExpectedWorkflowInputApplicationFunctions,
		exactWorkflowInputApplicationFunctionContractCount:    postgresExpectedWorkflowInputApplicationFunctions,
		expectedGCFunctionCount:                               postgresExpectedRepositoryGCFunctions,
		securityDefinerGCFunctionCount:                        postgresExpectedRepositoryGCFunctions,
		migrationOwnedGCFunctionCount:                         postgresExpectedRepositoryGCFunctions,
		fixedSearchPathGCFunctionCount:                        postgresExpectedRepositoryGCFunctions,
		exactResultGCFunctionCount:                            postgresExpectedRepositoryGCFunctions,
		operatorExpectedGCFunctionCount:                       postgresExpectedRepositoryGCFunctions,
		internalFunctionCount:                                 postgresExpectedInternalFunctions,
		exactOwnerACLInternalFunctionCount:                    postgresExpectedInternalFunctions,
		modelGovernanceFunctionCount:                          postgresExpectedModelGovernanceFunctions,
		exactModelGovernanceFunctionContractCount:             postgresExpectedModelGovernanceFunctions,
		qualificationPromotionFunctionCount:                   postgresExpectedQualificationPromotionFunctions,
		exactQualificationPromotionFunctionContractCount:      postgresExpectedQualificationPromotionFunctions,
		qualificationPromotionNamedFunctionCount:              postgresExpectedQualificationPromotionNamedFunctions,
		qualificationHandoffFunctionCount:                     postgresExpectedQualificationHandoffFunctions,
		exactQualificationHandoffFunctionContractCount:        postgresExpectedQualificationHandoffFunctions,
		qualificationHandoffNamedFunctionCount:                postgresExpectedQualificationHandoffFunctions,
		qualificationHandoffSecurityDefinerCount:              postgresExpectedQualificationHandoffSecurityDefiners,
		qualificationPolicyFunctionCount:                      postgresExpectedQualificationPolicyFunctions,
		exactQualificationPolicyFunctionContractCount:         postgresExpectedQualificationPolicyFunctions,
		qualificationInputFunctionCount:                       postgresExpectedQualificationInputFunctions,
		exactQualificationInputFunctionContractCount:          postgresExpectedQualificationInputFunctions,
		qualificationInputNamedFunctionCount:                  postgresExpectedQualificationInputFunctions,
		qualificationInputSecurityDefinerCount:                postgresExpectedQualificationInputSecurityDefiners,
		workflowInputAuthorityTableCount:                      postgresExpectedWorkflowInputAuthorityTables,
		exactWorkflowInputAuthorityTableContractCount:         postgresExpectedWorkflowInputAuthorityTables,
		workflowInputAuthorityNamedTableCount:                 postgresExpectedWorkflowInputAuthorityTables,
		workflowInputAuthorityTriggerCount:                    postgresExpectedWorkflowInputAuthorityTriggers,
		exactWorkflowInputAuthorityTriggerContractCount:       postgresExpectedWorkflowInputAuthorityTriggers,
		workflowInputAuthorityNamedTriggerCount:               postgresExpectedWorkflowInputAuthorityTriggers,
		workflowExecutionProfileV3TriggerCount:                postgresExpectedWorkflowExecutionProfileV3Triggers,
		exactWorkflowExecutionProfileV3TriggerContractCount:   postgresExpectedWorkflowExecutionProfileV3Triggers,
		workflowExecutionProfileV3NamedTriggerCount:           postgresExpectedWorkflowExecutionProfileV3Triggers,
		workflowExecutionProfileV3ExactHashContractCount:      postgresExpectedWorkflowExecutionProfileV3HashContracts,
		workflowSharedLegacyTriggerCount:                      postgresExpectedWorkflowSharedLegacyTriggers,
		exactWorkflowSharedLegacyTriggerContractCount:         postgresExpectedWorkflowSharedLegacyTriggers,
		workflowSharedRelationTriggerCount:                    postgresExpectedWorkflowSharedRelationTriggers,
		workflowAuthorityTriggerFunctionCount:                 postgresExpectedWorkflowAuthorityTriggerFunctions,
		exactWorkflowAuthorityTriggerFunctionContractCount:    postgresExpectedWorkflowAuthorityTriggerFunctions,
		workflowAuthorityTriggerNamedFunctionCount:            postgresExpectedWorkflowAuthorityTriggerFunctions,
		workflowSharedLegacyTriggerFunctionCount:              postgresExpectedWorkflowSharedLegacyTriggers,
		exactWorkflowSharedLegacyTriggerFunctionContractCount: postgresExpectedWorkflowSharedLegacyTriggers,
		workflowSharedLegacyTriggerNamedFunctionCount:         postgresExpectedWorkflowSharedLegacyTriggers,
		credentialSetFunctionCount:                            postgresExpectedCredentialSetFunctions,
		exactCredentialSetFunctionContractCount:               postgresExpectedCredentialSetFunctions,
		credentialSetNamedFunctionCount:                       postgresExpectedCredentialSetFunctions,
		qualificationEvidenceFunctionCount:                    postgresExpectedQualificationEvidenceFunctions,
		exactQualificationEvidenceFunctionContractCount:       postgresExpectedQualificationEvidenceFunctions,
		qualificationEvidenceNamedFunctionCount:               postgresExpectedQualificationEvidenceNamedFunctions,
		qualificationPlanFunctionCount:                        postgresExpectedQualificationPlanFunctions,
		exactQualificationPlanFunctionContractCount:           postgresExpectedQualificationPlanFunctions,
		qualificationPlanNamedFunctionCount:                   postgresExpectedQualificationPlanFunctions,
		qualificationReceiptV3FunctionCount:                   postgresExpectedQualificationReceiptV3Functions,
		exactQualificationReceiptV3FunctionContractCount:      postgresExpectedQualificationReceiptV3Functions,
		qualificationReceiptV3NamedFunctionCount:              postgresExpectedQualificationReceiptV3Functions,
		qualificationReceiptV3SecurityDefinerCount:            postgresExpectedQualificationReceiptV3Definers,
		canonicalReviewFunctionCount:                          postgresExpectedCanonicalReviewFunctions,
		exactCanonicalReviewFunctionContractCount:             postgresExpectedCanonicalReviewFunctions,
		canonicalReviewNamedFunctionCount:                     postgresExpectedCanonicalReviewFunctions,
		canonicalReviewSecurityDefinerCount:                   postgresExpectedCanonicalReviewDefiners,
		sandboxCheckpointHelperCount:                          postgresExpectedSandboxCheckpointHelpers,
		exactSandboxCheckpointHelperContractCount:             postgresExpectedSandboxCheckpointHelpers,
		schemaOwnerIsExact:                                    true,
		ownedBoundaryTableCount:                               postgresExpectedOwnedBoundaryTables,
		exactOwnedBoundaryTableCount:                          postgresExpectedOwnedBoundaryTables,
		ownedBoundaryIndexCount:                               postgresExpectedOwnedBoundaryIndexes,
		exactOwnedBoundaryIndexCount:                          postgresExpectedOwnedBoundaryIndexes,
		ownedBoundaryRoutineCount:                             postgresExpectedOwnedBoundaryRoutines,
		exactOwnedBoundaryRoutineCount:                        postgresExpectedOwnedBoundaryRoutines,
		ownedRelationCount:                                    1,
		exactOwnedRelationCount:                               1,
		securityDefinerFunctionCount:                          postgresExpectedSecurityDefinerFunctions,
		exactOwnedSecurityDefinerFunctionCount:                postgresExpectedSecurityDefinerFunctions,
		reachableExecutableSecurityDefinerCount:               postgresExpectedApplicationFunctions,
		reachableExpectedApplicationDefinerCount:              postgresExpectedApplicationFunctions,
		gcFunctionCount:                                       postgresExpectedRepositoryGCFunctions,
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
		{"forbidden stable role reachable", func(f *postgresRolePostureFacts) { f.forbiddenStableRoleReachable = true }, "input-authority operator"},
		{"reachable role admin option", func(f *postgresRolePostureFacts) { f.hasReachableRoleAdminOption = true }, "ADMIN OPTION"},
		{"missing database", func(f *postgresRolePostureFacts) { f.databaseCount = 0 }, "current database"},
		{"database owner membership", func(f *postgresRolePostureFacts) { f.ownsOrInheritsDatabaseOwner = true }, "database owner"},
		{"database create", func(f *postgresRolePostureFacts) { f.canCreateInDatabase = true }, "create schemas"},
		{"missing schema", func(f *postgresRolePostureFacts) { f.schemaCount = 0 }, "current schema"},
		{"missing schema usage", func(f *postgresRolePostureFacts) { f.applicationHasSchemaUsage = false }, "USAGE"},
		{"missing fault operator schema usage", func(f *postgresRolePostureFacts) { f.faultOperatorHasSchemaUsage = false }, "fault operator requires"},
		{"missing qualification operator schema usage", func(f *postgresRolePostureFacts) { f.qualificationOperatorHasSchemaUsage = false }, "qualification promotion operator requires"},
		{"missing qualification policy operator schema usage", func(f *postgresRolePostureFacts) { f.qualificationPolicyOperatorHasSchemaUsage = false }, "qualification policy operator requires"},
		{"missing input precommit operator schema usage", func(f *postgresRolePostureFacts) { f.qualificationInputPrecommitOperatorHasSchemaUsage = false }, "all three qualification input-authority"},
		{"missing source verifier operator schema usage", func(f *postgresRolePostureFacts) { f.qualificationSourceVerifierOperatorHasSchemaUsage = false }, "all three qualification input-authority"},
		{"missing credential resolver operator schema usage", func(f *postgresRolePostureFacts) { f.qualificationCredentialResolverOperatorHasSchemaUsage = false }, "all three qualification input-authority"},
		{"missing handoff operator schema usage", func(f *postgresRolePostureFacts) { f.qualificationHandoffOperatorHasSchemaUsage = false }, "handoff operator requires"},
		{"schema create", func(f *postgresRolePostureFacts) { f.canCreateInSchema = true }, "create objects"},
		{"application schema create", func(f *postgresRolePostureFacts) { f.applicationCanCreateInSchema = true }, "create objects"},
		{"fault operator schema create", func(f *postgresRolePostureFacts) { f.faultOperatorCanCreateInSchema = true }, "fault operator can create"},
		{"qualification operator schema create", func(f *postgresRolePostureFacts) { f.qualificationOperatorCanCreateInSchema = true }, "qualification promotion operator can create"},
		{"qualification policy operator schema create", func(f *postgresRolePostureFacts) { f.qualificationPolicyOperatorCanCreateInSchema = true }, "qualification policy operator can create"},
		{"input precommit operator schema create", func(f *postgresRolePostureFacts) { f.qualificationInputPrecommitOperatorCanCreateInSchema = true }, "input-authority operators can create"},
		{"handoff operator schema create", func(f *postgresRolePostureFacts) { f.qualificationHandoffOperatorCanCreateInSchema = true }, "handoff operator can create"},
		{"schema object owner", func(f *postgresRolePostureFacts) { f.ownedSchemaObjectCount = 1 }, "owns or inherits objects"},
		{"missing stable group", func(f *postgresRolePostureFacts) { f.stableGroupRoleCount = 8 }, "group roles are incomplete"},
		{"unsafe stable group", func(f *postgresRolePostureFacts) { f.stableGroupRolesUnsafe = true }, "NOLOGIN"},
		{"application membership missing", func(f *postgresRolePostureFacts) { f.isApplicationRoleMember = false }, "not an application group"},
		{"migration membership", func(f *postgresRolePostureFacts) { f.isMigrationOwnerRoleMember = true }, "migration-owner group member"},
		{"operator membership", func(f *postgresRolePostureFacts) { f.isOperatorRoleMember = true }, "operator member"},
		{"fault operator membership", func(f *postgresRolePostureFacts) { f.isGoldenFaultOperatorRoleMember = true }, "fault operator member"},
		{"qualification operator membership", func(f *postgresRolePostureFacts) { f.isQualificationPromotionOperatorRoleMember = true }, "qualification promotion operator member"},
		{"qualification policy operator membership", func(f *postgresRolePostureFacts) { f.isQualificationPolicyOperatorRoleMember = true }, "qualification policy operator member"},
		{"input precommit operator membership", func(f *postgresRolePostureFacts) { f.isQualificationInputPrecommitOperatorRoleMember = true }, "input-authority operator member"},
		{"handoff operator membership", func(f *postgresRolePostureFacts) { f.isQualificationHandoffOperatorRoleMember = true }, "handoff operator member"},
		{"missing table", func(f *postgresRolePostureFacts) { f.tableCount-- }, "table catalog"},
		{"table owner membership", func(f *postgresRolePostureFacts) { f.ownsOrInheritsTableOwner = true }, "inherits"},
		{"application table ACL", func(f *postgresRolePostureFacts) { f.applicationExactTableACLCount-- }, "table privileges"},
		{"API table ACL", func(f *postgresRolePostureFacts) { f.apiExactTableACLCount-- }, "table privileges"},
		{"public index table ACL", func(f *postgresRolePostureFacts) { f.publicPrivilegedIndexTableCount = 1 }, "PUBLIC can access"},
		{"missing private table", func(f *postgresRolePostureFacts) { f.gcPrivateTableCount-- }, "private table catalog"},
		{"private table access", func(f *postgresRolePostureFacts) { f.apiPrivilegedGCPrivateTableCount = 1 }, "private tables"},
		{"missing Golden fault table", func(f *postgresRolePostureFacts) { f.goldenFaultTableCount-- }, "Golden fault consume tables"},
		{"Golden fault operator ACL drift", func(f *postgresRolePostureFacts) { f.exactGoldenFaultOperatorTableACLCount-- }, "dedicated operator ACL"},
		{"missing qualification promotion table", func(f *postgresRolePostureFacts) { f.qualificationPromotionTableCount-- }, "historical v1 tables"},
		{"qualification promotion table ACL drift", func(f *postgresRolePostureFacts) { f.exactQualificationPromotionTableACLCount-- }, "migration-owner-only boundary"},
		{"unexpected qualification promotion table", func(f *postgresRolePostureFacts) { f.qualificationPromotionNamedTableCount++ }, "historical v1 tables"},
		{"qualification promotion table ACL outside boundary", func(f *postgresRolePostureFacts) { f.unexpectedQualificationPromotionTableACLCount = 1 }, "without Promotion-operator data ACLs"},
		{"missing qualification promotion trigger", func(f *postgresRolePostureFacts) { f.qualificationPromotionTriggerCount-- }, "identity-reservation"},
		{"qualification promotion trigger contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationPromotionTriggerContractCount-- }, "append-only trigger"},
		{"unexpected qualification promotion trigger", func(f *postgresRolePostureFacts) { f.qualificationPromotionNamedTriggerCount++ }, "trigger contracts have drifted"},
		{"missing qualification handoff table", func(f *postgresRolePostureFacts) { f.qualificationHandoffTableCount-- }, "exact four-table"},
		{"qualification handoff table contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationHandoffTableContractCount-- }, "exact four-table"},
		{"missing qualification handoff index", func(f *postgresRolePostureFacts) { f.qualificationHandoffIndexCount-- }, "exact nineteen-index"},
		{"qualification handoff index drift", func(f *postgresRolePostureFacts) { f.exactQualificationHandoffIndexContractCount-- }, "exact nineteen-index"},
		{"unexpected qualification handoff index", func(f *postgresRolePostureFacts) { f.qualificationHandoffNamedIndexCount++ }, "exact nineteen-index"},
		{"missing qualification handoff trigger", func(f *postgresRolePostureFacts) { f.qualificationHandoffTriggerCount-- }, "deferred closure"},
		{"qualification handoff trigger drift", func(f *postgresRolePostureFacts) { f.exactQualificationHandoffTriggerContractCount-- }, "deferred closure"},
		{"unexpected qualification handoff trigger", func(f *postgresRolePostureFacts) { f.qualificationHandoffNamedTriggerCount++ }, "deferred closure"},
		{"missing qualification input table", func(f *postgresRolePostureFacts) { f.qualificationInputTableCount-- }, "exact eight-table"},
		{"qualification input table contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationInputTableContractCount-- }, "exact eight-table"},
		{"unexpected qualification input table", func(f *postgresRolePostureFacts) { f.qualificationInputNamedTableCount++ }, "exact eight-table"},
		{"missing qualification input index", func(f *postgresRolePostureFacts) { f.qualificationInputIndexCount-- }, "index catalog"},
		{"qualification input index drift", func(f *postgresRolePostureFacts) { f.exactQualificationInputIndexContractCount-- }, "index catalog"},
		{"missing qualification input trigger", func(f *postgresRolePostureFacts) { f.qualificationInputTriggerCount-- }, "seven immutable, one head no-removal, and three deferred"},
		{"qualification input trigger contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationInputTriggerContractCount-- }, "seven immutable, one head no-removal, and three deferred"},
		{"unexpected qualification input trigger", func(f *postgresRolePostureFacts) { f.qualificationInputNamedTriggerCount++ }, "seven immutable, one head no-removal, and three deferred"},
		{"qualification policy relation privilege", func(f *postgresRolePostureFacts) { f.qualificationPolicyRelationPrivilegeCount = 1 }, "policy operator can access"},
		{"qualification policy column privilege", func(f *postgresRolePostureFacts) { f.qualificationPolicyColumnPrivilegeCount = 1 }, "policy operator can access"},
		{"qualification policy sequence privilege", func(f *postgresRolePostureFacts) { f.qualificationPolicySequencePrivilegeCount = 1 }, "policy operator can access"},
		{"qualification input operator table privilege", func(f *postgresRolePostureFacts) { f.qualificationInputOperatorRelationPrivilegeCount = 1 }, "input-authority or Handoff operators can access"},
		{"qualification input operator column privilege", func(f *postgresRolePostureFacts) { f.qualificationInputOperatorColumnPrivilegeCount = 1 }, "input-authority or Handoff operators can access"},
		{"qualification input operator sequence privilege", func(f *postgresRolePostureFacts) { f.qualificationInputOperatorSequencePrivilegeCount = 1 }, "input-authority or Handoff operators can access"},
		{"qualification policy relation owner", func(f *postgresRolePostureFacts) { f.qualificationPolicyOwnedRelationCount = 1 }, "policy operator can access"},
		{"missing Workflow Input authority table", func(f *postgresRolePostureFacts) { f.workflowInputAuthorityTableCount-- }, "exact ten-table"},
		{"Workflow Input authority table owner or ACL drift", func(f *postgresRolePostureFacts) { f.exactWorkflowInputAuthorityTableContractCount-- }, "migration-owner-only"},
		{"unexpected Workflow Input authority table", func(f *postgresRolePostureFacts) { f.workflowInputAuthorityNamedTableCount++ }, "exact ten-table"},
		{"missing Workflow Input authority trigger", func(f *postgresRolePostureFacts) { f.workflowInputAuthorityTriggerCount-- }, "activation-event identity"},
		{"Workflow Input authority trigger drift", func(f *postgresRolePostureFacts) { f.exactWorkflowInputAuthorityTriggerContractCount-- }, "deferred closure trigger"},
		{"unexpected Workflow Input authority trigger", func(f *postgresRolePostureFacts) { f.workflowInputAuthorityNamedTriggerCount++ }, "immutability"},
		{"missing workflow-engine/v3 trigger", func(f *postgresRolePostureFacts) { f.workflowExecutionProfileV3TriggerCount-- }, "workflow-engine/v3"},
		{"workflow-engine/v3 trigger drift", func(f *postgresRolePostureFacts) { f.exactWorkflowExecutionProfileV3TriggerContractCount-- }, "external-qualification gate"},
		{"unexpected workflow-engine/v3 trigger", func(f *postgresRolePostureFacts) { f.workflowExecutionProfileV3NamedTriggerCount++ }, "workflow-engine/v3"},
		{"workflow-engine/v3 hash contract drift", func(f *postgresRolePostureFacts) { f.workflowExecutionProfileV3ExactHashContractCount-- }, "descriptor hash"},
		{"missing shared Workflow legacy trigger", func(f *postgresRolePostureFacts) { f.workflowSharedLegacyTriggerCount-- }, "shared workflow definition"},
		{"shared Workflow legacy trigger drift", func(f *postgresRolePostureFacts) { f.exactWorkflowSharedLegacyTriggerContractCount-- }, "legacy trigger contracts"},
		{"unexpected shared Workflow relation trigger", func(f *postgresRolePostureFacts) { f.workflowSharedRelationTriggerCount++ }, "trigger allowlist"},
		{"missing Workflow authority trigger function", func(f *postgresRolePostureFacts) { f.workflowAuthorityTriggerFunctionCount-- }, "trigger function owner"},
		{"Workflow authority trigger function contract drift", func(f *postgresRolePostureFacts) { f.exactWorkflowAuthorityTriggerFunctionContractCount-- }, "parallel-safety"},
		{"Workflow authority trigger function overload", func(f *postgresRolePostureFacts) { f.workflowAuthorityTriggerNamedFunctionCount++ }, "search-path contracts"},
		{"missing shared Workflow legacy trigger function", func(f *postgresRolePostureFacts) { f.workflowSharedLegacyTriggerFunctionCount-- }, "legacy trigger function"},
		{"shared Workflow legacy trigger function drift", func(f *postgresRolePostureFacts) { f.exactWorkflowSharedLegacyTriggerFunctionContractCount-- }, "owner reachability"},
		{"shared Workflow legacy trigger function overload", func(f *postgresRolePostureFacts) { f.workflowSharedLegacyTriggerNamedFunctionCount++ }, "invoker"},
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
		{"missing Qualification Receipt v3 table", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3TableCount-- }, "exact three-table"},
		{"Qualification Receipt v3 table owner or ACL drift", func(f *postgresRolePostureFacts) { f.exactQualificationReceiptV3TableContractCount-- }, "migration-owner-only"},
		{"unexpected Qualification Receipt v3 table", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3NamedTableCount++ }, "exact three-table"},
		{"missing Qualification Receipt v3 index", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3IndexCount-- }, "exact fourteen-index"},
		{"Qualification Receipt v3 index drift", func(f *postgresRolePostureFacts) { f.exactQualificationReceiptV3IndexContractCount-- }, "exact fourteen-index"},
		{"unexpected Qualification Receipt v3 index", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3NamedIndexCount++ }, "exact fourteen-index"},
		{"Qualification Receipt v3 trigger drift", func(f *postgresRolePostureFacts) { f.exactQualificationReceiptV3TriggerContractCount-- }, "history-only trigger"},
		{"unexpected Qualification Receipt v3 trigger", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3TriggerCount++ }, "history-only trigger"},
		{"missing Canonical Review table", func(f *postgresRolePostureFacts) { f.canonicalReviewTableCount-- }, "Canonical Review approval receipt table"},
		{"Canonical Review table contract drift", func(f *postgresRolePostureFacts) { f.exactCanonicalReviewTableContractCount-- }, "table columns"},
		{"unexpected Canonical Review table", func(f *postgresRolePostureFacts) { f.canonicalReviewNamedTableCount++ }, "approval receipt table"},
		{"missing Canonical Review index", func(f *postgresRolePostureFacts) { f.canonicalReviewIndexCount-- }, "Canonical Review authority indexes"},
		{"Canonical Review index contract drift", func(f *postgresRolePostureFacts) { f.exactCanonicalReviewIndexContractCount-- }, "operator classes"},
		{"unexpected Canonical Review index", func(f *postgresRolePostureFacts) { f.canonicalReviewNamedIndexCount++ }, "authority indexes"},
		{"Canonical Review trigger contract drift", func(f *postgresRolePostureFacts) { f.exactCanonicalReviewTriggerContractCount-- }, "deferred constraint trigger"},
		{"unexpected Canonical Review trigger", func(f *postgresRolePostureFacts) { f.canonicalReviewTriggerCount++ }, "ordinary or deferred"},
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
		{"missing Workflow Input application routine", func(f *postgresRolePostureFacts) { f.workflowInputApplicationFunctionCount-- }, "Workflow Input application routine"},
		{"Workflow Input application routine contract drift", func(f *postgresRolePostureFacts) { f.exactWorkflowInputApplicationFunctionContractCount-- }, "Workflow Input application routine"},
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
		{"qualification promotion routine drift", func(f *postgresRolePostureFacts) { f.exactQualificationPromotionFunctionContractCount-- }, "four-function Promotion-operator"},
		{"unexpected qualification promotion routine", func(f *postgresRolePostureFacts) { f.qualificationPromotionNamedFunctionCount++ }, "Promotion v2 routines"},
		{"qualification promotion routine ACL outside boundary", func(f *postgresRolePostureFacts) { f.unexpectedQualificationPromotionFunctionACLCount = 1 }, "four-function Promotion-operator"},
		{"missing qualification handoff routine", func(f *postgresRolePostureFacts) { f.qualificationHandoffFunctionCount-- }, "Qualification Handoff routine"},
		{"qualification handoff routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationHandoffFunctionContractCount-- }, "two-function Handoff-operator"},
		{"unexpected qualification handoff routine", func(f *postgresRolePostureFacts) { f.qualificationHandoffNamedFunctionCount++ }, "Qualification Handoff routine"},
		{"qualification handoff definer drift", func(f *postgresRolePostureFacts) { f.qualificationHandoffSecurityDefinerCount-- }, "five-function definer"},
		{"qualification handoff routine ACL outside boundary", func(f *postgresRolePostureFacts) { f.unexpectedQualificationHandoffFunctionACLCount = 1 }, "two-function Handoff-operator"},
		{"qualification policy routine missing", func(f *postgresRolePostureFacts) { f.qualificationPolicyFunctionCount-- }, "qualification policy issue"},
		{"qualification policy routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationPolicyFunctionContractCount-- }, "qualification policy issue"},
		{"qualification policy routine ACL outside boundary", func(f *postgresRolePostureFacts) { f.unexpectedQualificationPolicyFunctionACLCount = 1 }, "qualification policy issue"},
		{"missing qualification input routine", func(f *postgresRolePostureFacts) { f.qualificationInputFunctionCount-- }, "Qualification Input Precommit routines"},
		{"qualification input routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationInputFunctionContractCount-- }, "Qualification Input Precommit routines"},
		{"unexpected qualification input routine", func(f *postgresRolePostureFacts) { f.qualificationInputNamedFunctionCount++ }, "Qualification Input Precommit routines"},
		{"qualification input security definer drift", func(f *postgresRolePostureFacts) { f.qualificationInputSecurityDefinerCount-- }, "Qualification Input Precommit routines"},
		{"qualification input ACL outside boundary", func(f *postgresRolePostureFacts) { f.unexpectedQualificationInputFunctionACLCount = 1 }, "Qualification Input Precommit routines"},
		{"missing CredentialSet routine", func(f *postgresRolePostureFacts) { f.credentialSetFunctionCount-- }, "CredentialSet SHA-256"},
		{"CredentialSet routine contract drift", func(f *postgresRolePostureFacts) { f.exactCredentialSetFunctionContractCount-- }, "CredentialSet SHA-256"},
		{"unexpected CredentialSet routine", func(f *postgresRolePostureFacts) { f.credentialSetNamedFunctionCount++ }, "CredentialSet SHA-256"},
		{"missing Qualification Evidence routine", func(f *postgresRolePostureFacts) { f.qualificationEvidenceFunctionCount-- }, "Qualification Evidence SHA-256"},
		{"Qualification Evidence routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationEvidenceFunctionContractCount-- }, "Qualification Evidence SHA-256"},
		{"unexpected Qualification Evidence routine", func(f *postgresRolePostureFacts) { f.qualificationEvidenceNamedFunctionCount++ }, "Qualification Evidence SHA-256"},
		{"missing Qualification Plan routine", func(f *postgresRolePostureFacts) { f.qualificationPlanFunctionCount-- }, "Qualification Plan SHA-256"},
		{"Qualification Plan routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationPlanFunctionContractCount-- }, "Qualification Plan SHA-256"},
		{"unexpected Qualification Plan routine", func(f *postgresRolePostureFacts) { f.qualificationPlanNamedFunctionCount++ }, "Qualification Plan SHA-256"},
		{"missing Qualification Receipt v3 routine", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3FunctionCount-- }, "Qualification Receipt v3 SHA-256"},
		{"Qualification Receipt v3 routine contract drift", func(f *postgresRolePostureFacts) { f.exactQualificationReceiptV3FunctionContractCount-- }, "Qualification Receipt v3 SHA-256"},
		{"unexpected Qualification Receipt v3 routine", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3NamedFunctionCount++ }, "Qualification Receipt v3 SHA-256"},
		{"Qualification Receipt v3 SECURITY DEFINER drift", func(f *postgresRolePostureFacts) { f.qualificationReceiptV3SecurityDefinerCount-- }, "exact three-function SECURITY DEFINER"},
		{"missing Canonical Review routine", func(f *postgresRolePostureFacts) { f.canonicalReviewFunctionCount-- }, "Canonical Review authority routine"},
		{"Canonical Review routine contract drift", func(f *postgresRolePostureFacts) { f.exactCanonicalReviewFunctionContractCount-- }, "identity arguments"},
		{"unexpected Canonical Review routine", func(f *postgresRolePostureFacts) { f.canonicalReviewNamedFunctionCount++ }, "Canonical Review authority routine"},
		{"Canonical Review SECURITY DEFINER drift", func(f *postgresRolePostureFacts) { f.canonicalReviewSecurityDefinerCount-- }, "exact four-function SECURITY DEFINER"},
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
		{"missing reachable application definer", func(f *postgresRolePostureFacts) { f.reachableExecutableSecurityDefinerCount-- }, "exact fifteen-function"},
		{"unexpected reachable definer", func(f *postgresRolePostureFacts) {
			f.reachableUnexpectedSecurityDefinerCount = 1
			f.reachableExecutableSecurityDefinerCount++
		}, "exact fifteen-function"},
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
		"artifact_revision_identity_reservations",
		"qualification_promotion_v2_independent_receipts",
		"qualification_promotion_v2_consumptions",
		"qualification_promotion_v2_consumption_independent_receipts",
		"qualification_promotion_v2_handoffs",
		"qualification_promotion_v2_identity_reservations",
		"expected_qualification_handoff_table_columns",
		"qualification_promotion_v2_handoff_lineage_members",
		"creation_transaction_id",
		"qualification_promotion_v2_revisi_creation_transaction_id_check",
		"qualification_promotion_v2_handof_creation_transaction_id_check",
		"qualification_promotion_v2_hando_creation_transaction_id_check1",
		"qualification_handoff_lineage_members_pkey",
		"qualification_handoff_lineage_members_ordinal_key",
		"qualification_handoff_lineage_members_immutable",
		"qualification_handoff_lineage_member_exact_closure",
		"qualification_promotion_named_table_facts",
		"expected_qualification_promotion_triggers",
		"artifact_revisions_shared_identity_reservation",
		"qualification_promotion_v2_identity_reservations_immutable",
		"qualification_promotion_trigger_facts",
		"worksflow_qualification_promotion_operator",
		"worksflow_qualification_policy_operator",
		"worksflow_qualification_input_precommit_operator",
		"worksflow_qualification_source_verifier_operator",
		"worksflow_qualification_credential_resolver_operator",
		"expected_qualification_input_tables",
		"qualification_input_precommit_executable_binding_generations",
		"qualification_input_precommit_executable_binding_heads",
		"qualification_input_source_receipt_admissions",
		"qualification_input_credential_receipt_admissions",
		"qualification_input_precommit_authorities",
		"qualification_input_precommit_identity_reservations",
		"qualification_input_precommit_wia_reservations",
		"qualification_input_precommit_plan_reservations",
		"expected_qualification_input_triggers",
		"qualification_input_precommit_binding_heads_no_removal",
		"qualification_input_source_admission_exact_closure",
		"qualification_input_credential_admission_exact_closure",
		"qualification_input_precommit_authority_exact_closure",
		"expected_qualification_input_functions",
		"qualification_input_function_facts",
		"qualification_input_precommit_hash_v1",
		"qualification_input_precommit_string_is_secret_free_v1",
		"review_qualification_input_precommit_executable_binding_v1",
		"issue_qualification_input_precommit_v1",
		"admit_qualification_input_source_receipt_v1",
		"admit_qualification_input_credential_receipt_v1",
		"resolve_qualification_input_precommit_for_promotion_v1",
		"consume_verified_qualification_promotion",
		"assert_current_qualification_policy_authority_v1",
		"assert_current_workflow_input_authority_v1",
		"expected_workflow_input_authority_tables",
		"qualification_policy_exact_approved_sources",
		"workflow_input_authority_review_receipts",
		"workflow_input_authority_table_facts",
		"expected_workflow_authority_triggers",
		"workflow_input_authority_event_identity_guard",
		"guard_workflow_input_authority_event_identity_v1",
		"workflow_execution_profile_v3_definition_guard",
		"external_qualification_gate_node_v3_guard",
		"workflow_authority_trigger_contract_rows",
		"workflow_authority_named_trigger_facts",
		"workflow_shared_legacy_trigger_facts",
		"workflow_shared_relation_total_trigger_facts",
		"workflow_definition_execution_profile_immutable",
		"workflow_run_governance_mode_immutable",
		"expected_workflow_authority_trigger_functions",
		"workflow_authority_trigger_function_facts",
		"workflow_authority_trigger_named_function_facts",
		"expected_workflow_shared_legacy_trigger_functions",
		"workflow_shared_legacy_trigger_function_facts",
		"routine.prorettype = 'trigger'::pg_catalog.regtype",
		"routine.provolatile = 'v'",
		"routine.proparallel = 'u'",
		"trigger.tgtype = expected.trigger_type",
		"trigger.tgdeferrable = expected.is_deferrable",
		"constraint_binding.condeferred",
		"expected_workflow_input_application_functions",
		"workflow_input_application_function_facts",
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
		"qualification_receipt_v3_requests",
		"qualification_receipt_v3_observations",
		"qualification_receipt_v3_receipts",
		"qualification_receipt_v3_requ_plan_authority_id_request_kin_key",
		"qualification_receipt_v3_requ_operation_id_request_kind_sig_key",
		"qualification_receipt_v3_observati_request_hash_record_hash_key",
		"qualification_receipt_v3_requests_orchestration_idx",
		"qualification_receipt_v3_observations_state_idx",
		"qualification_receipt_v3_receipts_target_idx",
		"qualification_receipt_v3_requests_immutable",
		"qualification_receipt_v3_observations_immutable",
		"qualification_receipt_v3_receipts_immutable",
		"qualification_evidence_v1_receipt_tail_history_only",
		"qualification_promotion_v1_new_consumption_history_only",
		"qualification_receipt_v3_sha256",
		"reject_qualification_receipt_v3_mutation",
		"guard_qualification_evidence_v1_receipt_tail_history_only",
		"guard_qualification_promotion_v1_new_consumption_history_only",
		"start_qualification_receipt_v3_requests",
		"append_qualification_receipt_v3_observation",
		"complete_qualification_receipt_v3",
		"TABLE(request_record qualification_receipt_v3_requests, created boolean)",
		"TABLE(observation_record qualification_receipt_v3_observations, idempotent boolean)",
		"TABLE(receipt_record qualification_receipt_v3_receipts, idempotent boolean)",
		"expected_qualification_receipt_v3_indexes",
		"expected_qualification_receipt_v3_triggers",
		"expected_qualification_receipt_v3_functions",
		"qualification_receipt_v3_function_facts",
		"canonical_review_approval_receipts",
		"canonical_review_receipts_pkey",
		"canonical_review_receipts_hash_key",
		"canonical_review_receipts_revision_key",
		"canonical_review_receipts_target_idx",
		"review_decisions_request_id_id_key",
		"canonical_review_approval_receipts_immutable",
		"canonical_review_requests_controlled_mutation",
		"canonical_review_decisions_controlled_mutation",
		"canonical_review_approved_requires_receipt",
		"canonical_review_uuid_is_exact",
		"canonical_review_text_is_trimmed",
		"canonical_review_timestamp_is_exact",
		"canonical_review_authority_hash",
		"canonical_review_jsonb_bytes",
		"reject_canonical_review_receipt_mutation",
		"canonical_review_approval_receipt_record_is_exact",
		"guard_canonical_review_source_mutation",
		"resolve_canonical_review_approval_receipt",
		"issue_canonical_review_approval_receipt",
		"canonical_review_approval_receipt_is_exact",
		"require_canonical_review_approval_receipt",
		"expected_canonical_review_table_columns",
		"expected_canonical_review_indexes",
		"expected_canonical_review_triggers",
		"expected_canonical_review_functions",
		"canonical_review_function_facts",
		"pg_catalog.pg_get_function_identity_arguments(routine.oid)",
		"ARRAY['uuid_ops', 'uuid_ops', 'uuid_ops']::text[]",
		"ARRAY['pg_catalog.default']::text[]",
		"constraint_binding.conkey::smallint[]",
		"trigger.tgconstraint <> 0::pg_catalog.oid",
		"constraint_binding.contype = 't'",
		"constraint_binding.condeferred =",
		"expected.update_columns[trigger_column.ordinal_position::integer]",
		"ARRAY['plan_authority_id', 'request_kind', 'signer_role']::text[]",
		"ARRAY['request_hash', 'generation', 'sequence', 'status']::text[]",
		"ARRAY['project_id', 'workflow_run_id', 'node_key'",
		"index_catalog.indnkeyatts",
		"index_catalog.indnatts",
		"NOT index_catalog.indnullsnotdistinct",
		"index_catalog.indexprs IS NULL",
		"index_catalog.indpred IS NULL",
		"access_method.amname = 'btree'",
		"pg_catalog.pg_opclass AS operator_class",
		"NOT operator_class.opcdefault",
		"operator_class.opcmethod <> index_relation.relam",
		"operator_class.opcintype <> attribute.atttypid",
		"key_entry.collation_oid <> attribute.attcollation",
		"key_entry.option_bits <> 0",
		"constraint_binding.conrelid = table_relation.oid",
		"constraint_binding.conindid = index_relation.oid",
		"constraint_binding.contype IN ('p', 'u', 'x')",
		"constraint_binding.contype::text = expected.constraint_type",
		"trigger.tgqual IS NULL",
		"cardinality(trigger.tgattr::smallint[]) = 0",
		"trigger.tgnargs = 0",
		"trigger.tgargs = ''::bytea",
		"trigger.tgconstraint = 0::pg_catalog.oid",
		"trigger.tgconstrrelid = 0::pg_catalog.oid",
		"NOT trigger.tgdeferrable",
		"NOT trigger.tginitdeferred",
		"trigger.tgoldtable IS NULL",
		"trigger.tgnewtable IS NULL",
		"trigger.tgparentid = 0::pg_catalog.oid",
		"expected_qualification_promotion_functions",
		"qualification_promotion_function_facts",
		"qualification_promotion_named_function_facts",
		"consume_qualification_promotion_v2",
		"inspect_qualification_promotion_v2_operation",
		"resolve_qualification_promotion_v2_handoff",
		"assert_pending_qualification_promotion_v2_handoff",
		"inspect_historical_qualification_promotion_v1_operation",
		"expected_qualification_policy_operator_functions",
		"qualification_policy_operator_function_facts",
		"issue_qualification_policy_authority_v1",
		"inspect_qualification_policy_operation_v1",
		"resolve_qualification_policy_authority_v1",
		"resolve_current_qualification_policy_authority_v1",
		"qualification_policy_data_privilege_facts",
		"acquire_repository_exact_tree_literal_index_build_claim",
		"acquire_candidate_workspace_lease",
		"abandon_sandbox_session_candidate",
		"complete_abandoned_sandbox_session",
		"expected_internal_functions",
		"exact_owner_acl_count",
		"expected_workflow_execution_profile_v3_hash_functions",
		"workflow_execution_profile_v3_hash_constraint_facts",
		"854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104",
		"aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef",
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

func TestQualificationPromotionV2PostureRealPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
		t.Skip("Promotion-v2 posture canary requires a role that can provision stable fixture roles")
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

	stableRoles := []string{
		postgresApplicationRole,
		postgresMigrationOwnerRole,
		postgresRepositoryIndexGCOperatorRole,
		postgresGoldenFaultOperatorRole,
		postgresQualificationPromotionOperatorRole,
		postgresQualificationPolicyOperatorRole,
		postgresQualificationInputPrecommitOperatorRole,
		postgresQualificationSourceVerifierOperatorRole,
		postgresQualificationCredentialResolverOperatorRole,
		postgresQualificationHandoffOperatorRole,
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

	schemaName := "promotion_posture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var migrationDatabase *sql.DB
	defer func() {
		if migrationDatabase != nil {
			_ = migrationDatabase.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schemaName+` CASCADE`)
		for index := len(createdStableRoles) - 1; index >= 0; index-- {
			role := createdStableRoles[index]
			_, _ = admin.ExecContext(cleanupCtx, `DROP OWNED BY `+role)
			_, _ = admin.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS `+role)
		}
	}()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schemaName); err != nil {
		t.Fatalf("create Promotion-v2 posture schema: %v", err)
	}
	migrationDatabase = openPostgresRolePostureLogin(
		t,
		dsn,
		connectionConfigUser(t, dsn),
		connectionConfigPassword(t, dsn),
		schemaName,
		"",
	)

	if err := migrations.Up(ctx, migrationDatabase); err != nil {
		t.Fatalf("apply current migrations for Promotion-v2 posture: %v", err)
	}
	facts, err := scanPostgresRolePosture(
		migrationDatabase.QueryRowContext(ctx, postgresRolePostureQuery).Scan,
	)
	if err != nil {
		t.Fatalf("scan Promotion-v2 posture from current migrations: %v", err)
	}
	if facts.workflowExecutionProfileV3ExactHashContractCount != postgresExpectedWorkflowExecutionProfileV3HashContracts {
		t.Fatalf(
			"current-migration workflow-engine/v3 hash contracts=%d, want %d",
			facts.workflowExecutionProfileV3ExactHashContractCount,
			postgresExpectedWorkflowExecutionProfileV3HashContracts,
		)
	}
	if facts.qualificationInputTableCount != postgresExpectedQualificationInputTables ||
		facts.exactQualificationInputTableContractCount != postgresExpectedQualificationInputTables ||
		facts.qualificationInputNamedTableCount != postgresExpectedQualificationInputTables ||
		facts.qualificationInputIndexCount != postgresExpectedQualificationInputIndexes ||
		facts.exactQualificationInputIndexContractCount != postgresExpectedQualificationInputIndexes ||
		facts.qualificationInputTriggerCount != postgresExpectedQualificationInputTriggers ||
		facts.exactQualificationInputTriggerContractCount != postgresExpectedQualificationInputTriggers ||
		facts.qualificationInputNamedTriggerCount != postgresExpectedQualificationInputTriggers ||
		facts.qualificationInputFunctionCount != postgresExpectedQualificationInputFunctions ||
		facts.exactQualificationInputFunctionContractCount != postgresExpectedQualificationInputFunctions ||
		facts.qualificationInputNamedFunctionCount != postgresExpectedQualificationInputFunctions ||
		facts.qualificationInputSecurityDefinerCount != postgresExpectedQualificationInputSecurityDefiners ||
		facts.unexpectedQualificationInputFunctionACLCount != 0 {
		t.Fatalf(
			"current-migration input-precommit posture tables=%d/%d/%d indexes=%d/%d triggers=%d/%d/%d functions=%d/%d/%d definers=%d unexpected-function-acl=%d",
			facts.qualificationInputTableCount,
			facts.exactQualificationInputTableContractCount,
			facts.qualificationInputNamedTableCount,
			facts.qualificationInputIndexCount,
			facts.exactQualificationInputIndexContractCount,
			facts.qualificationInputTriggerCount,
			facts.exactQualificationInputTriggerContractCount,
			facts.qualificationInputNamedTriggerCount,
			facts.qualificationInputFunctionCount,
			facts.exactQualificationInputFunctionContractCount,
			facts.qualificationInputNamedFunctionCount,
			facts.qualificationInputSecurityDefinerCount,
			facts.unexpectedQualificationInputFunctionACLCount,
		)
	}
	if facts.qualificationPromotionTableCount != postgresExpectedQualificationPromotionTables ||
		facts.exactQualificationPromotionTableACLCount != postgresExpectedQualificationPromotionTables ||
		facts.qualificationPromotionNamedTableCount != postgresExpectedQualificationPromotionNamedTables ||
		facts.unexpectedQualificationPromotionTableACLCount != 0 ||
		facts.qualificationPromotionTriggerCount != postgresExpectedQualificationPromotionTriggers ||
		facts.exactQualificationPromotionTriggerContractCount != postgresExpectedQualificationPromotionTriggers ||
		facts.qualificationPromotionNamedTriggerCount != postgresExpectedQualificationPromotionNamedTriggers ||
		facts.qualificationPromotionFunctionCount != postgresExpectedQualificationPromotionFunctions ||
		facts.exactQualificationPromotionFunctionContractCount != postgresExpectedQualificationPromotionFunctions ||
		facts.qualificationPromotionNamedFunctionCount != postgresExpectedQualificationPromotionNamedFunctions ||
		facts.unexpectedQualificationPromotionFunctionACLCount != 0 {
		t.Fatalf(
			"current-migration Promotion-v2 posture tables=%d/%d/%d unexpected-table-acl=%d triggers=%d/%d/%d functions=%d/%d/%d unexpected-function-acl=%d",
			facts.qualificationPromotionTableCount,
			facts.exactQualificationPromotionTableACLCount,
			facts.qualificationPromotionNamedTableCount,
			facts.unexpectedQualificationPromotionTableACLCount,
			facts.qualificationPromotionTriggerCount,
			facts.exactQualificationPromotionTriggerContractCount,
			facts.qualificationPromotionNamedTriggerCount,
			facts.qualificationPromotionFunctionCount,
			facts.exactQualificationPromotionFunctionContractCount,
			facts.qualificationPromotionNamedFunctionCount,
			facts.unexpectedQualificationPromotionFunctionACLCount,
		)
	}
	if facts.qualificationHandoffTableCount != postgresExpectedQualificationHandoffTables ||
		facts.exactQualificationHandoffTableContractCount != postgresExpectedQualificationHandoffTables ||
		facts.qualificationHandoffIndexCount != postgresExpectedQualificationHandoffIndexes ||
		facts.exactQualificationHandoffIndexContractCount != postgresExpectedQualificationHandoffIndexes ||
		facts.qualificationHandoffNamedIndexCount != postgresExpectedQualificationHandoffIndexes ||
		facts.qualificationHandoffTriggerCount != postgresExpectedQualificationHandoffTriggers ||
		facts.exactQualificationHandoffTriggerContractCount != postgresExpectedQualificationHandoffTriggers ||
		facts.qualificationHandoffNamedTriggerCount != postgresExpectedQualificationHandoffTriggers ||
		facts.qualificationHandoffFunctionCount != postgresExpectedQualificationHandoffFunctions ||
		facts.exactQualificationHandoffFunctionContractCount != postgresExpectedQualificationHandoffFunctions ||
		facts.qualificationHandoffNamedFunctionCount != postgresExpectedQualificationHandoffFunctions ||
		facts.qualificationHandoffSecurityDefinerCount != postgresExpectedQualificationHandoffSecurityDefiners ||
		facts.unexpectedQualificationHandoffFunctionACLCount != 0 {
		t.Fatalf(
			"current-migration Handoff posture tables=%d/%d indexes=%d/%d/%d triggers=%d/%d/%d functions=%d/%d/%d definers=%d unexpected-function-acl=%d",
			facts.qualificationHandoffTableCount,
			facts.exactQualificationHandoffTableContractCount,
			facts.qualificationHandoffIndexCount,
			facts.exactQualificationHandoffIndexContractCount,
			facts.qualificationHandoffNamedIndexCount,
			facts.qualificationHandoffTriggerCount,
			facts.exactQualificationHandoffTriggerContractCount,
			facts.qualificationHandoffNamedTriggerCount,
			facts.qualificationHandoffFunctionCount,
			facts.exactQualificationHandoffFunctionContractCount,
			facts.qualificationHandoffNamedFunctionCount,
			facts.qualificationHandoffSecurityDefinerCount,
			facts.unexpectedQualificationHandoffFunctionACLCount,
		)
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

	createdStableRoles := make([]string, 0, 10)
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
		postgresQualificationPolicyOperatorRole,
		postgresQualificationInputPrecommitOperatorRole,
		postgresQualificationSourceVerifierOperatorRole,
		postgresQualificationCredentialResolverOperatorRole,
		postgresQualificationHandoffOperatorRole,
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
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresQualificationPolicyOperatorRole,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresQualificationInputPrecommitOperatorRole,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresQualificationSourceVerifierOperatorRole,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresQualificationCredentialResolverOperatorRole,
		`GRANT USAGE ON SCHEMA ` + schemaName + ` TO ` + postgresQualificationHandoffOperatorRole,
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
		"artifact_revision_identity_reservations",
		"qualification_promotion_v2_independent_receipts",
		"qualification_promotion_v2_consumptions",
		"qualification_promotion_v2_consumption_independent_receipts",
		"qualification_promotion_v2_handoffs",
		"qualification_promotion_v2_identity_reservations",
	}
	workflowInputAuthorityTables := []string{
		"qualification_policy_authorities",
		"qualification_policy_review_defaults",
		"qualification_policy_exact_approved_sources",
		"qualification_policy_identity_reservations",
		"workflow_input_authorities",
		"workflow_input_authority_identity_reservations",
		"workflow_input_authority_predecessors",
		"workflow_input_authority_manifests",
		"workflow_input_authority_revisions",
		"workflow_input_authority_review_receipts",
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
	qualificationReceiptV3Tables := []string{
		"qualification_receipt_v3_requests",
		"qualification_receipt_v3_observations",
		"qualification_receipt_v3_receipts",
	}
	qualificationInputTables := []string{
		"qualification_input_precommit_executable_binding_generations",
		"qualification_input_precommit_executable_binding_heads",
		"qualification_input_source_receipt_admissions",
		"qualification_input_credential_receipt_admissions",
		"qualification_input_precommit_authorities",
		"qualification_input_precommit_identity_reservations",
		"qualification_input_precommit_wia_reservations",
		"qualification_input_precommit_plan_reservations",
	}
	qualificationHandoffTables := []string{
		"qualification_promotion_v2_revision_transaction_grants",
		"qualification_promotion_v2_revision_authority_bindings",
		"qualification_promotion_v2_handoff_lineage_members",
		"qualification_promotion_v2_handoff_completions",
	}
	qualificationHandoffTableDefinitions := map[string]string{
		"qualification_promotion_v2_revision_transaction_grants": `(
  output_revision_id uuid NOT NULL,
  handoff_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  backend_pid integer NOT NULL,
  transaction_id text NOT NULL,
  granted_at timestamptz NOT NULL
)`,
		"qualification_promotion_v2_revision_authority_bindings": `(
  handoff_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  output_revision_id uuid NOT NULL,
  workflow_input_authority_id uuid NOT NULL,
  workflow_input_authority_hash text NOT NULL,
  plan_authority_id uuid NOT NULL,
  plan_authority_hash text NOT NULL,
  receipt_id text NOT NULL,
  receipt_envelope_hash text NOT NULL,
  promotion_request_hash text NOT NULL,
  promotion_closure_hash text NOT NULL,
  promotion_revision_intent_hash text NOT NULL,
  promotion_consumption_hash text NOT NULL,
  target_document jsonb NOT NULL,
  authority_hash text NOT NULL,
  authority_bytes bytea NOT NULL,
  authority_document jsonb NOT NULL,
  creation_transaction_id text NOT NULL,
  created_at timestamptz NOT NULL,
  CONSTRAINT qualification_promotion_v2_revisi_creation_transaction_id_check
    CHECK (creation_transaction_id ~ '^[1-9][0-9]{0,19}$')
)`,
		"qualification_promotion_v2_handoff_lineage_members": `(
  handoff_id uuid NOT NULL,
  member_kind text NOT NULL,
  member_ordinal bigint NOT NULL,
  member_key text NOT NULL,
  row_hash text NOT NULL,
  creation_transaction_id text NOT NULL,
  CONSTRAINT qualification_handoff_lineage_members_pkey
    PRIMARY KEY (handoff_id, member_kind, member_key),
  CONSTRAINT qualification_handoff_lineage_members_ordinal_key
    UNIQUE (handoff_id, member_kind, member_ordinal),
  CONSTRAINT qualification_promotion_v2_handof_creation_transaction_id_check
    CHECK (creation_transaction_id ~ '^[1-9][0-9]{0,19}$')
)`,
		"qualification_promotion_v2_handoff_completions": `(
  handoff_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  consumption_hash text NOT NULL,
  output_revision_id uuid NOT NULL,
  output_revision_content_hash text NOT NULL,
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  node_run_id uuid NOT NULL,
  node_key text NOT NULL,
  publish_node_run_id uuid NOT NULL,
  publish_node_key text NOT NULL,
  event_cursor_before bigint NOT NULL,
  event_cursor_after bigint NOT NULL,
  gate_output_document jsonb NOT NULL,
  gate_completed_event_id uuid NOT NULL,
  publish_authorization_event_id uuid NOT NULL,
  completion_hash text NOT NULL,
  completion_bytes bytea NOT NULL,
  completion_document jsonb NOT NULL,
  creation_transaction_id text NOT NULL,
  completed_at timestamptz NOT NULL,
  CONSTRAINT qualification_promotion_v2_hando_creation_transaction_id_check1
    CHECK (creation_transaction_id ~ '^[1-9][0-9]{0,19}$')
)`,
	}
	allBoundaryTables := append(append(append(append(append(append([]string{}, indexTables...), privateTables...), faultTables...), modelGovernanceTables...), qualificationPromotionTables...), credentialSetTables...)
	allBoundaryTables = append(allBoundaryTables, qualificationEvidenceTables...)
	allBoundaryTables = append(allBoundaryTables, qualificationInputTables...)
	allBoundaryTables = append(allBoundaryTables, qualificationHandoffTables...)
	for _, tableName := range allBoundaryTables {
		tableDefinition := "(id bigint, value bigint)"
		if handoffDefinition, ok := qualificationHandoffTableDefinitions[tableName]; ok {
			tableDefinition = handoffDefinition
		}
		for _, statement := range []string{
			`CREATE TABLE ` + schemaName + `.` + tableName + ` ` + tableDefinition,
			`ALTER TABLE ` + schemaName + `.` + tableName + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON TABLE ` + schemaName + `.` + tableName + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("provision table fixture with %q: %v", statement, err)
			}
		}
	}
	for _, tableName := range workflowInputAuthorityTables {
		tableDefinition := `CREATE TABLE ` + schemaName + `.` + tableName + ` (id uuid)`
		if tableName == "workflow_input_authorities" {
			tableDefinition = `CREATE TABLE ` + schemaName + `.` + tableName + ` (
  id uuid,
  execution_profile_hash text NOT NULL CHECK (
    execution_profile_hash = 'sha256:854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
  )
)`
		}
		for _, statement := range []string{
			tableDefinition,
			`ALTER TABLE ` + schemaName + `.` + tableName + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON TABLE ` + schemaName + `.` + tableName + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("provision Workflow Input authority table fixture with %q: %v", statement, err)
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
		`CREATE TABLE ` + schemaName + `.qualification_receipt_v3_requests (
  request_hash text PRIMARY KEY,
  plan_authority_id bigint NOT NULL,
  request_kind text NOT NULL,
  signer_role text NOT NULL,
  operation_id bigint NOT NULL,
  orchestration_id bigint NOT NULL,
  id bigint, value bigint,
  UNIQUE (plan_authority_id, request_kind, signer_role),
  UNIQUE (operation_id, request_kind, signer_role)
)`,
		`CREATE INDEX qualification_receipt_v3_requests_orchestration_idx ON ` + schemaName + `.qualification_receipt_v3_requests (orchestration_id, request_kind, signer_role)`,
		`CREATE TABLE ` + schemaName + `.qualification_receipt_v3_observations (
  request_hash text NOT NULL,
  sequence bigint NOT NULL,
  generation bigint NOT NULL,
  record_hash text NOT NULL,
  status text NOT NULL,
  claim_id bigint UNIQUE,
  acknowledgement_id bigint UNIQUE,
  id bigint, value bigint,
  PRIMARY KEY (request_hash, sequence),
  UNIQUE (record_hash),
  UNIQUE (request_hash, record_hash)
)`,
		`CREATE INDEX qualification_receipt_v3_observations_state_idx ON ` + schemaName + `.qualification_receipt_v3_observations (request_hash, generation, sequence, status)`,
		`CREATE TABLE ` + schemaName + `.qualification_receipt_v3_receipts (
  receipt_id text PRIMARY KEY,
  plan_authority_id bigint NOT NULL UNIQUE,
  receipt_sign_operation_id bigint NOT NULL UNIQUE,
  project_id bigint,
  workflow_run_id bigint,
  node_key text,
  target_revision_id bigint,
  id bigint, value bigint
)`,
		`CREATE INDEX qualification_receipt_v3_receipts_target_idx ON ` + schemaName + `.qualification_receipt_v3_receipts (project_id, workflow_run_id, node_key, target_revision_id)`,
		`ALTER TABLE ` + schemaName + `.qualification_receipt_v3_requests OWNER TO ` + postgresMigrationOwnerRole,
		`ALTER TABLE ` + schemaName + `.qualification_receipt_v3_observations OWNER TO ` + postgresMigrationOwnerRole,
		`ALTER TABLE ` + schemaName + `.qualification_receipt_v3_receipts OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.qualification_receipt_v3_requests FROM PUBLIC`,
		`REVOKE ALL ON TABLE ` + schemaName + `.qualification_receipt_v3_observations FROM PUBLIC`,
		`REVOKE ALL ON TABLE ` + schemaName + `.qualification_receipt_v3_receipts FROM PUBLIC`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision Qualification Receipt v3 table fixture with %q: %v", statement, err)
		}
	}
	allBoundaryTables = append(allBoundaryTables, qualificationReceiptV3Tables...)
	for _, statement := range []string{
		`CREATE TABLE ` + schemaName + `.review_requests (
  id uuid NOT NULL,
  status text NOT NULL
)`,
		`CREATE TABLE ` + schemaName + `.review_decisions (
  id uuid NOT NULL,
  review_request_id uuid NOT NULL,
  CONSTRAINT review_decisions_request_id_id_key UNIQUE (review_request_id, id)
)`,
		`CREATE TABLE ` + schemaName + `.canonical_review_approval_receipts (
  review_request_id uuid NOT NULL,
  receipt_hash text NOT NULL,
  receipt_bytes bytea NOT NULL,
  receipt_document jsonb NOT NULL,
  review_request_snapshot_hash text NOT NULL,
  review_request_snapshot_bytes bytea NOT NULL,
  review_request_snapshot_document jsonb NOT NULL,
  revision_snapshot_hash text NOT NULL,
  revision_snapshot_bytes bytea NOT NULL,
  revision_snapshot_document jsonb NOT NULL,
  policy_snapshot_hash text NOT NULL,
  policy_snapshot_bytes bytea NOT NULL,
  policy_snapshot_document jsonb NOT NULL,
  decisions_snapshot_hash text NOT NULL,
  decisions_snapshot_bytes bytea NOT NULL,
  decisions_snapshot_document jsonb NOT NULL,
  governance_snapshot_hash text NOT NULL,
  governance_snapshot_bytes bytea NOT NULL,
  governance_snapshot_document jsonb NOT NULL,
  approval_snapshot_hash text NOT NULL,
  approval_snapshot_bytes bytea NOT NULL,
  approval_snapshot_document jsonb NOT NULL,
  project_id uuid NOT NULL,
  artifact_id uuid NOT NULL,
  revision_id uuid NOT NULL,
  revision_content_hash text NOT NULL,
  closed_by_decision_id uuid NOT NULL,
  approval_count integer NOT NULL,
  minimum_approvals integer NOT NULL,
  governance_mode text NOT NULL,
  owner_count integer NOT NULL,
  solo_self_review boolean NOT NULL,
  sole_owner_id uuid,
  issued_at timestamp with time zone NOT NULL,
	  CONSTRAINT canonical_review_receipts_pkey PRIMARY KEY (review_request_id),
	  CONSTRAINT canonical_review_receipts_hash_key UNIQUE (receipt_hash),
	  CONSTRAINT canonical_review_receipts_revision_key UNIQUE (revision_id),
	  CONSTRAINT canonical_review_receipts_workflow_input_exact_unique UNIQUE (
	    review_request_id, receipt_hash, project_id, artifact_id,
	    revision_id, revision_content_hash
	  )
)`,
		`CREATE INDEX canonical_review_receipts_target_idx ON ` + schemaName + `.canonical_review_approval_receipts (project_id, artifact_id, revision_id)`,
		`ALTER TABLE ` + schemaName + `.review_requests OWNER TO ` + postgresMigrationOwnerRole,
		`ALTER TABLE ` + schemaName + `.review_decisions OWNER TO ` + postgresMigrationOwnerRole,
		`ALTER TABLE ` + schemaName + `.canonical_review_approval_receipts OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.review_requests FROM PUBLIC`,
		`REVOKE ALL ON TABLE ` + schemaName + `.review_decisions FROM PUBLIC`,
		`REVOKE ALL ON TABLE ` + schemaName + `.canonical_review_approval_receipts FROM PUBLIC`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision Canonical Review authority table fixture with %q: %v", statement, err)
		}
	}
	for _, statement := range []string{
		`CREATE TABLE ` + schemaName + `.schema_migrations (version text)`,
		`ALTER TABLE ` + schemaName + `.schema_migrations OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.schema_migrations FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.business_records (id bigint, value bigint)`,
		`ALTER TABLE ` + schemaName + `.business_records OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.business_records FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.artifact_revisions (id uuid)`,
		`ALTER TABLE ` + schemaName + `.artifact_revisions OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.artifact_revisions FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.artifact_revision_sources (id uuid)`,
		`ALTER TABLE ` + schemaName + `.artifact_revision_sources OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.artifact_revision_sources FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.artifact_dependencies (id uuid)`,
		`ALTER TABLE ` + schemaName + `.artifact_dependencies OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.artifact_dependencies FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.trace_links (id uuid)`,
		`ALTER TABLE ` + schemaName + `.trace_links OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.trace_links FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.outbox_events (id uuid)`,
		`ALTER TABLE ` + schemaName + `.outbox_events OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.outbox_events FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.workflow_definition_versions (
  id uuid, content jsonb, content_hash text,
  execution_profile_version text, execution_profile_hash text
)`,
		`ALTER TABLE ` + schemaName + `.workflow_definition_versions OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.workflow_definition_versions FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.workflow_runs (
	  id uuid, definition_version_id uuid, status text,
	  execution_profile_version text, execution_profile_hash text,
	  context jsonb, event_cursor bigint
	)`,
		`ALTER TABLE ` + schemaName + `.workflow_runs OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.workflow_runs FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.workflow_node_runs (id uuid)`,
		`ALTER TABLE ` + schemaName + `.workflow_node_runs OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.workflow_node_runs FROM PUBLIC`,
		`CREATE TABLE ` + schemaName + `.workflow_run_events (id uuid)`,
		`ALTER TABLE ` + schemaName + `.workflow_run_events OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON TABLE ` + schemaName + `.workflow_run_events FROM PUBLIC`,
		`CREATE SEQUENCE ` + schemaName + `.business_sequence`,
		`ALTER SEQUENCE ` + schemaName + `.business_sequence OWNER TO ` + postgresMigrationOwnerRole,
		`REVOKE ALL ON SEQUENCE ` + schemaName + `.business_sequence FROM PUBLIC`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("provision supporting table fixture with %q: %v", statement, err)
		}
	}
	indexNumber := postgresExpectedQualificationPlanIndexes +
		postgresExpectedQualificationReceiptV3Indexes +
		postgresExpectedCanonicalReviewIndexes
	for _, tableName := range allBoundaryTables {
		if tableName == "qualification_promotion_v2_revision_transaction_grants" ||
			tableName == "qualification_promotion_v2_revision_authority_bindings" ||
			tableName == "qualification_promotion_v2_handoff_lineage_members" ||
			tableName == "qualification_promotion_v2_handoff_completions" {
			continue
		}
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
	qualificationHandoffIndexes := []struct {
		name, tableName, expression string
	}{
		{"artifact_revisions_ordinary_content_unique", "artifact_revisions", "id"},
		{"artifact_revisions_promotion_handoff_unique", "artifact_revisions", "id"},
		{"qualification_promotion_handoff_pending_dispatch_unique", "outbox_events", "id"},
		{"qualification_promotion_v2_ha_publish_authorization_event_i_key", "qualification_promotion_v2_handoff_completions", "publish_authorization_event_id"},
		{"qualification_promotion_v2_handoff__gate_completed_event_id_key", "qualification_promotion_v2_handoff_completions", "gate_completed_event_id"},
		{"qualification_promotion_v2_handoff_compl_output_revision_id_key", "qualification_promotion_v2_handoff_completions", "output_revision_id"},
		{"qualification_promotion_v2_handoff_complet_consumption_hash_key", "qualification_promotion_v2_handoff_completions", "consumption_hash"},
		{"qualification_promotion_v2_handoff_completi_completion_hash_key", "qualification_promotion_v2_handoff_completions", "completion_hash"},
		{"qualification_promotion_v2_handoff_completions_operation_id_key", "qualification_promotion_v2_handoff_completions", "operation_id"},
		{"qualification_promotion_v2_handoff_completions_pkey", "qualification_promotion_v2_handoff_completions", "handoff_id"},
		{"qualification_promotion_v2_revision_auth_output_revision_id_key", "qualification_promotion_v2_revision_authority_bindings", "output_revision_id"},
		{"qualification_promotion_v2_revision_authorit_authority_hash_key", "qualification_promotion_v2_revision_authority_bindings", "authority_hash"},
		{"qualification_promotion_v2_revision_authority__operation_id_key", "qualification_promotion_v2_revision_authority_bindings", "operation_id"},
		{"qualification_promotion_v2_revision_authority_bindings_pkey", "qualification_promotion_v2_revision_authority_bindings", "handoff_id"},
		{"qualification_promotion_v2_revision_transactio_operation_id_key", "qualification_promotion_v2_revision_transaction_grants", "operation_id"},
		{"qualification_promotion_v2_revision_transaction__handoff_id_key", "qualification_promotion_v2_revision_transaction_grants", "handoff_id"},
		{"qualification_promotion_v2_revision_transaction_grants_pkey", "qualification_promotion_v2_revision_transaction_grants", "output_revision_id"},
	}
	// The lineage table's PRIMARY KEY and UNIQUE constraints already created
	// the two exact named Handoff indexes.
	indexNumber += 2
	for _, indexFixture := range qualificationHandoffIndexes {
		indexNumber++
		statement := fmt.Sprintf(
			`CREATE INDEX %s ON %s.%s (%s)`,
			indexFixture.name, schemaName, indexFixture.tableName,
			indexFixture.expression,
		)
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create qualification Handoff index fixture with %q: %v", statement, err)
		}
	}
	for supplemental := 0; supplemental < 12; supplemental++ {
		indexNumber++
		statement := fmt.Sprintf(
			`CREATE INDEX posture_input_boundary_%02d ON %s.qualification_input_precommit_authorities ((id + value + %d))`,
			indexNumber, schemaName, supplemental,
		)
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create qualification input supplemental index fixture with %q: %v", statement, err)
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
CREATE FUNCTION %s.canonical_review_uuid_is_exact(p_value text)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog AS $$ SELECT false $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.canonical_review_text_is_trimmed(p_value text)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog AS $$ SELECT false $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.canonical_review_timestamp_is_exact(p_value text)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN false; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.canonical_review_authority_hash(
  p_domain text, p_value bytea
) RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog AS $$ SELECT ''::text $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.canonical_review_jsonb_bytes(p_value jsonb)
RETURNS bytea LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ BEGIN RETURN ''::bytea; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_canonical_review_receipt_mutation()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.canonical_review_approval_receipt_record_is_exact(
  p_receipt %s.canonical_review_approval_receipts
) RETURNS boolean LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_canonical_review_source_mutation()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ BEGIN RETURN NEW; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.resolve_canonical_review_approval_receipt(
  p_project_id uuid, p_revision_id uuid, p_receipt_hash text
) RETURNS SETOF %s.canonical_review_approval_receipts
LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.issue_canonical_review_approval_receipt(
  p_review_request_id uuid
) RETURNS TABLE (
  receipt_record %s.canonical_review_approval_receipts,
  created boolean
) LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.canonical_review_approval_receipt_is_exact(
  p_project_id uuid, p_revision_id uuid, p_review_request_id uuid
) RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.require_canonical_review_approval_receipt()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
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
	CREATE FUNCTION %s.issue_qualification_policy_authority_v1(
	  uuid, uuid, text, text, uuid, text, text, bigint, text,
	  timestamp with time zone, text, text, text, bytea, jsonb, text,
	  bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb
	) RETURNS SETOF %s.qualification_policy_authorities
	LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.inspect_qualification_policy_operation_v1(uuid)
	RETURNS SETOF %s.qualification_policy_authorities
	LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.resolve_qualification_policy_authority_v1(uuid)
	RETURNS SETOF %s.qualification_policy_authorities
	LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.resolve_current_qualification_policy_authority_v1(uuid, text, text)
	RETURNS SETOF %s.qualification_policy_authorities
	LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.assert_current_qualification_policy_authority_v1(uuid)
	RETURNS SETOF %s.qualification_policy_authorities
	LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.assert_current_workflow_input_authority_v1(uuid)
	RETURNS SETOF jsonb
	LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.qualification_promotion_v2_hash(text, bytea)
	RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
	SET search_path TO pg_catalog
	AS $$ SELECT 'sha256:' || repeat('0', 64) $$`, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.qualification_promotion_v2_timestamp(timestamptz)
	RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
	SET search_path TO pg_catalog
	AS $$ SELECT '2026-07-19T00:00:00.000Z'::text $$`, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.reject_qualification_promotion_v2_mutation()
	RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
	SET search_path TO pg_catalog
	AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.reserve_ordinary_artifact_revision_identity_v1()
	RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN NEW; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.qualification_promotion_v2_plan_is_exact(uuid)
	RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY INVOKER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.qualification_promotion_v2_store_record_is_exact(uuid)
	RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY INVOKER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.qualification_promotion_v2_store_bundle(uuid, boolean, boolean)
	RETURNS jsonb LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY INVOKER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN '{}'::jsonb; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.consume_qualification_promotion_v2(uuid, uuid, uuid, uuid, uuid)
	RETURNS SETOF jsonb LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.inspect_qualification_promotion_v2_operation(uuid)
	RETURNS SETOF jsonb LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.resolve_qualification_promotion_v2_handoff(uuid)
	RETURNS SETOF jsonb LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.assert_pending_qualification_promotion_v2_handoff(uuid)
	RETURNS SETOF jsonb LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.inspect_historical_qualification_promotion_v1_operation(uuid)
	RETURNS SETOF jsonb LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.freeze_workflow_input_authority_v1(
	  uuid, uuid, uuid, uuid, bigint, uuid, bigint,
	  bytea, bytea, bytea, bytea, bytea, jsonb
	) RETURNS SETOF %s.workflow_input_authorities
	LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s, pg_temp
	AS $$ BEGIN
	  PERFORM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104';
	  RETURN;
	END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.inspect_workflow_input_authority_operation_v1(uuid)
	RETURNS SETOF jsonb
	LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
	CREATE FUNCTION %s.resolve_workflow_input_authority_for_node_v1(uuid, uuid)
	RETURNS SETOF jsonb
	LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
	SET search_path TO pg_catalog, %s
	AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
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
CREATE FUNCTION %s.qualification_receipt_v3_sha256(bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ SELECT 'sha256:' || repeat('0', 64) $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_qualification_receipt_v3_mutation()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_qualification_evidence_v1_receipt_tail_history_only()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s
AS $$ BEGIN RETURN NEW; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.guard_qualification_promotion_v1_new_consumption_history_only()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog
AS $$ BEGIN RETURN NEW; END $$`, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.start_qualification_receipt_v3_requests(
  text, bytea, jsonb, text, bytea, jsonb, text, bytea, text, bytea
) RETURNS TABLE (
  request_record %s.qualification_receipt_v3_requests, created boolean
) LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.append_qualification_receipt_v3_observation(
  text, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb,
  text, bytea, text, bytea, jsonb, text, bytea, jsonb
) RETURNS TABLE (
  observation_record %s.qualification_receipt_v3_observations,
  idempotent boolean
) LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.complete_qualification_receipt_v3(
  uuid, text, text, text, text, text, text, text, text, text,
  bytea, jsonb, text, bytea, text, bytea, jsonb, text
) RETURNS TABLE (
  receipt_record %s.qualification_receipt_v3_receipts, idempotent boolean
) LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s, pg_temp
AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
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
	functionDefinitions = append(
		functionDefinitions,
		qualificationInputPostureFunctionDefinitions(schemaName)...,
	)
	functionDefinitions = append(
		functionDefinitions,
		qualificationHandoffPostureFunctionDefinitions(schemaName)...,
	)
	for _, definition := range functionDefinitions {
		if _, err := admin.ExecContext(ctx, definition); err != nil {
			t.Fatalf("create posture function fixture: %v\n%s", err, definition)
		}
	}
	workflowSharedLegacyTriggerFunctionDefinitions := []string{
		fmt.Sprintf(`CREATE FUNCTION %s.guard_workflow_definition_execution_profile_identity()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.guard_workflow_run_execution_profile_identity()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.workflow_run_governance_mode_immutable()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
AS $$ BEGIN RETURN NULL; END $$`, schemaName),
	}
	workflowSharedLegacyTriggerFunctionReferences := []string{
		"guard_workflow_definition_execution_profile_identity()",
		"guard_workflow_run_execution_profile_identity()",
		"workflow_run_governance_mode_immutable()",
	}
	for index, definition := range workflowSharedLegacyTriggerFunctionDefinitions {
		if _, err := admin.ExecContext(ctx, definition); err != nil {
			t.Fatalf("create shared Workflow legacy trigger function fixture: %v\n%s", err, definition)
		}
		qualified := schemaName + "." + workflowSharedLegacyTriggerFunctionReferences[index]
		for _, statement := range []string{
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
			`GRANT EXECUTE ON FUNCTION ` + qualified + ` TO ` + postgresApplicationRole,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("secure shared Workflow legacy trigger function fixture with %q: %v", statement, err)
			}
		}
	}
	workflowAuthorityTriggerFunctionDefinitions := []string{
		fmt.Sprintf(`CREATE FUNCTION %s.reject_qualification_policy_authority_mutation()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.validate_qualification_policy_authority_closure_v1()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.reject_workflow_input_authority_mutation()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.guard_workflow_node_stable_identity_v1()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog AS $$ BEGIN RETURN NULL; END $$`, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.guard_workflow_input_authority_event_identity_v1()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.validate_workflow_input_authority_closure_v1()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.guard_workflow_execution_profile_v3_definition()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN
  PERFORM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104';
  RETURN NULL;
END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.guard_workflow_execution_profile_v3_run()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN
  PERFORM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104';
  RETURN NULL;
END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.guard_external_qualification_gate_node_v3()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN
  PERFORM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104';
  RETURN NULL;
END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.validate_workflow_execution_profile_v3_run_closure()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
	}
	workflowAuthorityTriggerFunctionReferences := []string{
		"reject_qualification_policy_authority_mutation()",
		"validate_qualification_policy_authority_closure_v1()",
		"reject_workflow_input_authority_mutation()",
		"guard_workflow_node_stable_identity_v1()",
		"guard_workflow_input_authority_event_identity_v1()",
		"validate_workflow_input_authority_closure_v1()",
		"guard_workflow_execution_profile_v3_definition()",
		"guard_workflow_execution_profile_v3_run()",
		"guard_external_qualification_gate_node_v3()",
		"validate_workflow_execution_profile_v3_run_closure()",
	}
	for index, definition := range workflowAuthorityTriggerFunctionDefinitions {
		if _, err := admin.ExecContext(ctx, definition); err != nil {
			t.Fatalf("create Workflow authority trigger function fixture: %v\n%s", err, definition)
		}
		qualified := schemaName + "." + workflowAuthorityTriggerFunctionReferences[index]
		for _, statement := range []string{
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("secure Workflow authority trigger function fixture with %q: %v", statement, err)
			}
		}
	}
	type workflowAuthorityTriggerFixture struct {
		name, tableName, timingEvents, level, functionName string
		constraint                                         bool
	}
	workflowAuthorityTriggerFixtures := []workflowAuthorityTriggerFixture{
		{"workflow_definition_execution_profile_immutable", "workflow_definition_versions", "BEFORE UPDATE", "ROW", "guard_workflow_definition_execution_profile_identity", false},
		{"workflow_run_execution_profile_immutable", "workflow_runs", "BEFORE UPDATE", "ROW", "guard_workflow_run_execution_profile_identity", false},
		{"workflow_run_governance_mode_immutable", "workflow_runs", "BEFORE UPDATE", "ROW", "workflow_run_governance_mode_immutable", false},
		{"qualification_policy_authorities_immutable", "qualification_policy_authorities", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_qualification_policy_authority_mutation", false},
		{"qualification_policy_review_defaults_immutable", "qualification_policy_review_defaults", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_qualification_policy_authority_mutation", false},
		{"qualification_policy_exact_approved_sources_immutable", "qualification_policy_exact_approved_sources", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_qualification_policy_authority_mutation", false},
		{"qualification_policy_identity_reservations_immutable", "qualification_policy_identity_reservations", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_qualification_policy_authority_mutation", false},
		{"qualification_policy_authorities_exact_closure", "qualification_policy_authorities", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_qualification_policy_authority_closure_v1", true},
		{"qualification_policy_review_defaults_exact_closure", "qualification_policy_review_defaults", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_qualification_policy_authority_closure_v1", true},
		{"qualification_policy_exact_sources_exact_closure", "qualification_policy_exact_approved_sources", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_qualification_policy_authority_closure_v1", true},
		{"qualification_policy_identity_reservations_exact_closure", "qualification_policy_identity_reservations", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_qualification_policy_authority_closure_v1", true},
		{"workflow_input_authorities_immutable", "workflow_input_authorities", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_workflow_input_authority_mutation", false},
		{"workflow_input_authority_identity_reservations_immutable", "workflow_input_authority_identity_reservations", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_workflow_input_authority_mutation", false},
		{"workflow_input_authority_predecessors_immutable", "workflow_input_authority_predecessors", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_workflow_input_authority_mutation", false},
		{"workflow_input_authority_manifests_immutable", "workflow_input_authority_manifests", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_workflow_input_authority_mutation", false},
		{"workflow_input_authority_revisions_immutable", "workflow_input_authority_revisions", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_workflow_input_authority_mutation", false},
		{"workflow_input_authority_review_receipts_immutable", "workflow_input_authority_review_receipts", "BEFORE UPDATE OR DELETE OR TRUNCATE", "STATEMENT", "reject_workflow_input_authority_mutation", false},
		{"workflow_node_stable_identity_v1_immutable", "workflow_node_runs", "BEFORE UPDATE", "ROW", "guard_workflow_node_stable_identity_v1", false},
		{"workflow_input_authority_event_identity_guard", "workflow_run_events", "BEFORE INSERT OR UPDATE OR DELETE", "ROW", "guard_workflow_input_authority_event_identity_v1", false},
		{"workflow_input_authorities_exact_closure", "workflow_input_authorities", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_input_authority_closure_v1", true},
		{"workflow_input_authority_predecessors_exact_closure", "workflow_input_authority_predecessors", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_input_authority_closure_v1", true},
		{"workflow_input_authority_manifests_exact_closure", "workflow_input_authority_manifests", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_input_authority_closure_v1", true},
		{"workflow_input_authority_revisions_exact_closure", "workflow_input_authority_revisions", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_input_authority_closure_v1", true},
		{"workflow_input_authority_review_receipts_exact_closure", "workflow_input_authority_review_receipts", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_input_authority_closure_v1", true},
		{"workflow_node_input_authority_exact_closure", "workflow_node_runs", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_input_authority_closure_v1", true},
		{"workflow_input_authority_event_exact_closure", "workflow_run_events", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_input_authority_closure_v1", true},
		{"workflow_execution_profile_v3_definition_guard", "workflow_definition_versions", "BEFORE INSERT OR UPDATE OF content, content_hash, execution_profile_version, execution_profile_hash", "ROW", "guard_workflow_execution_profile_v3_definition", false},
		{"workflow_execution_profile_v3_run_guard", "workflow_runs", "BEFORE INSERT OR UPDATE OF definition_version_id, execution_profile_version, execution_profile_hash, status, context, event_cursor", "ROW", "guard_workflow_execution_profile_v3_run", false},
		{"external_qualification_gate_node_v3_guard", "workflow_node_runs", "BEFORE INSERT OR UPDATE", "ROW", "guard_external_qualification_gate_node_v3", false},
		{"workflow_execution_profile_v3_run_exact_closure", "workflow_runs", "AFTER INSERT OR UPDATE", "ROW", "validate_workflow_execution_profile_v3_run_closure", true},
		{"workflow_execution_profile_v3_node_exact_closure", "workflow_node_runs", "AFTER INSERT OR UPDATE OR DELETE", "ROW", "validate_workflow_execution_profile_v3_run_closure", true},
	}
	createWorkflowAuthorityTriggerFixture := func(fixture workflowAuthorityTriggerFixture) error {
		triggerKind := "TRIGGER"
		deferredContract := ""
		if fixture.constraint {
			triggerKind = "CONSTRAINT TRIGGER"
			deferredContract = "DEFERRABLE INITIALLY DEFERRED\n"
		}
		definition := fmt.Sprintf(
			"CREATE %s %s\n%s ON %s.%s\n%sFOR EACH %s EXECUTE FUNCTION %s.%s()",
			triggerKind, fixture.name, fixture.timingEvents, schemaName,
			fixture.tableName, deferredContract, fixture.level, schemaName,
			fixture.functionName,
		)
		_, err := admin.ExecContext(ctx, definition)
		return err
	}
	for _, fixture := range workflowAuthorityTriggerFixtures {
		if err := createWorkflowAuthorityTriggerFixture(fixture); err != nil {
			t.Fatalf("create Workflow authority trigger fixture %s: %v", fixture.name, err)
		}
	}
	// Named contracts above cover the pre-WIA boundary, policy/application
	// functions, and the seven SECURITY DEFINER trigger functions used by
	// migrations 78/79. The remaining current-head definers exercise the
	// exhaustive owner/PUBLIC/reachability closure.
	const namedFixtureSecurityDefinerFunctions = 79
	for index := 0; index < postgresExpectedSecurityDefinerFunctions-namedFixtureSecurityDefinerFunctions; index++ {
		functionName := fmt.Sprintf("posture_head_security_definer_%02d", index)
		qualified := schemaName + "." + functionName + "()"
		for _, statement := range []string{
			`CREATE FUNCTION ` + qualified + ` RETURNS boolean LANGUAGE sql SECURITY DEFINER SET search_path TO pg_catalog AS $$ SELECT false $$`,
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("provision current-head SECURITY DEFINER fixture with %q: %v", statement, err)
			}
		}
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`
CREATE TRIGGER model_governance_activation_authority_anchor
BEFORE INSERT ON %s.model_governance_activation_records
FOR EACH ROW EXECUTE FUNCTION %s.enforce_model_governance_activation_authority_anchor()`, schemaName, schemaName)); err != nil {
		t.Fatalf("create posture Model Governance anchor trigger: %v", err)
	}
	for _, triggerDefinition := range []string{
		fmt.Sprintf(`CREATE TRIGGER qualification_input_precommit_executable_bindings_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_input_precommit_executable_binding_generations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_input_precommit_binding_heads_no_removal
BEFORE DELETE OR TRUNCATE ON %s.qualification_input_precommit_executable_binding_heads
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_input_source_receipt_admissions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_input_source_receipt_admissions
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_input_credential_receipt_admissions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_input_credential_receipt_admissions
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_input_precommit_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_input_precommit_authorities
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_input_precommit_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_input_precommit_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_input_precommit_wia_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_input_precommit_wia_reservations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_input_precommit_plan_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_input_precommit_plan_reservations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_input_source_admission_exact_closure
AFTER INSERT ON %s.qualification_input_source_receipt_admissions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.enforce_qualification_input_source_admission_closure_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_input_credential_admission_exact_closure
AFTER INSERT ON %s.qualification_input_credential_receipt_admissions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.enforce_qualification_input_credential_admission_closure_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_input_precommit_authority_exact_closure
AFTER INSERT ON %s.qualification_input_precommit_authorities
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.enforce_qualification_input_precommit_authority_closure_v1()`, schemaName, schemaName),
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
		fmt.Sprintf(`CREATE TRIGGER qualification_receipt_v3_requests_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_receipt_v3_requests
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_receipt_v3_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_receipt_v3_observations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_receipt_v3_observations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_receipt_v3_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_receipt_v3_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_receipt_v3_receipts
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_receipt_v3_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_evidence_v1_receipt_tail_history_only
BEFORE INSERT ON %s.qualification_evidence_events
FOR EACH ROW EXECUTE FUNCTION %s.guard_qualification_evidence_v1_receipt_tail_history_only()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_promotion_v1_new_consumption_history_only
BEFORE INSERT ON %s.qualification_promotion_consumptions
FOR EACH ROW EXECUTE FUNCTION %s.guard_qualification_promotion_v1_new_consumption_history_only()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER artifact_revisions_shared_identity_reservation
BEFORE INSERT ON %s.artifact_revisions
FOR EACH ROW EXECUTE FUNCTION %s.reserve_ordinary_artifact_revision_identity_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER artifact_revision_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.artifact_revision_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_promotion_v2_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_promotion_v2_independent_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_independent_receipts
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_promotion_v2_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_promotion_v2_consumptions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_consumptions
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_promotion_v2_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_promotion_v2_consumption_independent_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_consumption_independent_receipts
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_promotion_v2_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_promotion_v2_handoffs_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_handoffs
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_promotion_v2_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_promotion_v2_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_promotion_v2_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER canonical_review_approval_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.canonical_review_approval_receipts
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_canonical_review_receipt_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER canonical_review_requests_controlled_mutation
BEFORE UPDATE OR DELETE ON %s.review_requests
FOR EACH ROW EXECUTE FUNCTION %s.guard_canonical_review_source_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER canonical_review_decisions_controlled_mutation
BEFORE INSERT OR UPDATE OR DELETE ON %s.review_decisions
FOR EACH ROW EXECUTE FUNCTION %s.guard_canonical_review_source_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER canonical_review_approved_requires_receipt
AFTER INSERT OR UPDATE OF status ON %s.review_requests
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.require_canonical_review_approval_receipt()`, schemaName, schemaName),
	} {
		if _, err := admin.ExecContext(ctx, triggerDefinition); err != nil {
			t.Fatalf("create posture owner-only ledger trigger fixture: %v\n%s", err, triggerDefinition)
		}
	}
	for _, triggerDefinition := range []string{
		fmt.Sprintf(`CREATE TRIGGER qualification_handoff_revision_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_revision_authority_bindings
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_handoff_v1_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_handoff_completions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_handoff_completions
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_handoff_v1_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_handoff_lineage_members_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON %s.qualification_promotion_v2_handoff_lineage_members
FOR EACH STATEMENT EXECUTE FUNCTION %s.reject_qualification_handoff_v1_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_handoff_outbox_immutable
BEFORE UPDATE OR DELETE ON %s.outbox_events
FOR EACH ROW EXECUTE FUNCTION %s.reject_qualification_handoff_v1_mutation()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE TRIGGER qualification_promotion_v2_handoff_pending_dispatch
AFTER INSERT ON %s.qualification_promotion_v2_handoffs
FOR EACH ROW EXECUTE FUNCTION %s.enqueue_qualification_promotion_v2_handoff_v1()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_grant_empty_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.qualification_promotion_v2_revision_transaction_grants
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_completion_exact_closure
AFTER INSERT ON %s.qualification_promotion_v2_handoff_completions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_revision_authority_exact_closure
AFTER INSERT ON %s.qualification_promotion_v2_revision_authority_bindings
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_lineage_member_exact_closure
AFTER INSERT ON %s.qualification_promotion_v2_handoff_lineage_members
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_revision_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.artifact_revisions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_revision_source_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.artifact_revision_sources
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_dependency_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.artifact_dependencies
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_trace_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.trace_links
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_event_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.workflow_run_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_outbox_exact_closure
AFTER INSERT OR DELETE ON %s.outbox_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_node_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.workflow_node_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
		fmt.Sprintf(`CREATE CONSTRAINT TRIGGER qualification_handoff_run_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON %s.workflow_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION %s.validate_qualification_handoff_v1_closure()`, schemaName, schemaName),
	} {
		if _, err := admin.ExecContext(ctx, triggerDefinition); err != nil {
			t.Fatalf("create Qualification Handoff trigger fixture: %v\n%s", err, triggerDefinition)
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
		"issue_canonical_review_approval_receipt(uuid)",
		"canonical_review_approval_receipt_is_exact(uuid,uuid,uuid)",
		"freeze_workflow_input_authority_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)",
		"inspect_workflow_input_authority_operation_v1(uuid)",
		"resolve_workflow_input_authority_for_node_v1(uuid,uuid)",
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
		"start_qualification_receipt_v3_requests(text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea)",
		"append_qualification_receipt_v3_observation(text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text,bytea,jsonb)",
		"complete_qualification_receipt_v3(uuid,text,text,text,text,text,text,text,text,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text)",
		"resolve_canonical_review_approval_receipt(uuid,uuid,text)",
		"require_canonical_review_approval_receipt()",
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
		"qualification_receipt_v3_sha256(bytea)",
		"reject_qualification_receipt_v3_mutation()",
		"guard_qualification_evidence_v1_receipt_tail_history_only()",
		"guard_qualification_promotion_v1_new_consumption_history_only()",
		"canonical_review_uuid_is_exact(text)",
		"canonical_review_text_is_trimmed(text)",
		"canonical_review_timestamp_is_exact(text)",
		"canonical_review_authority_hash(text,bytea)",
		"canonical_review_jsonb_bytes(jsonb)",
		"reject_canonical_review_receipt_mutation()",
		"guard_canonical_review_source_mutation()",
	}
	canonicalReviewAdditionalOwnerReference :=
		"canonical_review_approval_receipt_record_is_exact(" + schemaName +
			".canonical_review_approval_receipts)"
	sandboxCheckpointHelperReference := "sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)"
	ownerFunctionReferences := append([]string{}, applicationFunctionReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, gcFunctionReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, internalSecurityDefinerReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, internalInvokerReferences...)
	ownerFunctionReferences = append(ownerFunctionReferences, canonicalReviewAdditionalOwnerReference)
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
	qualificationPromotionFunctionReferences := []string{
		"consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb)",
		"assert_current_qualification_policy_authority_v1(uuid)",
		"assert_current_workflow_input_authority_v1(uuid)",
		"qualification_promotion_v2_hash(text,bytea)",
		"qualification_promotion_v2_timestamp(timestamp with time zone)",
		"reject_qualification_promotion_v2_mutation()",
		"reserve_ordinary_artifact_revision_identity_v1()",
		"qualification_promotion_v2_plan_is_exact(uuid)",
		"qualification_promotion_v2_store_record_is_exact(uuid)",
		"qualification_promotion_v2_store_bundle(uuid,boolean,boolean)",
		"consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)",
		"inspect_qualification_promotion_v2_operation(uuid)",
		"resolve_qualification_promotion_v2_handoff(uuid)",
		"assert_pending_qualification_promotion_v2_handoff(uuid)",
		"inspect_historical_qualification_promotion_v1_operation(uuid)",
		"resolve_qualification_input_precommit_for_promotion_v1(uuid,uuid)",
	}
	for _, functionReference := range qualificationPromotionFunctionReferences {
		qualified := schemaName + "." + functionReference
		for _, statement := range []string{
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("secure qualification-promotion operator function with %q: %v", statement, err)
			}
		}
	}
	for _, functionReference := range []string{
		"consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)",
		"inspect_qualification_promotion_v2_operation(uuid)",
		"inspect_historical_qualification_promotion_v1_operation(uuid)",
		"resolve_qualification_input_precommit_for_promotion_v1(uuid,uuid)",
	} {
		if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schemaName+`.`+functionReference+` TO `+postgresQualificationPromotionOperatorRole); err != nil {
			t.Fatalf("grant Qualification Promotion v2 operator function %s: %v", functionReference, err)
		}
	}
	qualificationHandoffFunctionReferences := []string{
		"qualification_handoff_v1_hash(text,bytea)",
		"qualification_handoff_v1_timestamp(timestamp with time zone)",
		"reject_qualification_handoff_v1_mutation()",
		"enqueue_qualification_promotion_v2_handoff_v1()",
		"qualification_handoff_v1_quality_result(uuid,uuid)",
		"qualification_handoff_v1_completion_is_exact(uuid)",
		"qualification_handoff_v1_completion_bundle(uuid,boolean,boolean)",
		"inspect_qualification_promotion_v2_handoff_completion(uuid)",
		"complete_qualification_promotion_v2_handoff(uuid)",
		"validate_qualification_handoff_v1_closure()",
	}
	for _, functionReference := range qualificationHandoffFunctionReferences {
		qualified := schemaName + "." + functionReference
		for _, statement := range []string{
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("secure qualification Handoff function with %q: %v", statement, err)
			}
		}
	}
	for _, functionReference := range []string{
		"complete_qualification_promotion_v2_handoff(uuid)",
		"inspect_qualification_promotion_v2_handoff_completion(uuid)",
	} {
		if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schemaName+`.`+functionReference+` TO `+postgresQualificationHandoffOperatorRole); err != nil {
			t.Fatalf("grant Qualification Handoff function %s: %v", functionReference, err)
		}
	}
	qualificationPolicyFunctionReferences := []string{
		"issue_qualification_policy_authority_v1(uuid,uuid,text,text,uuid,text,text,bigint,text,timestamp with time zone,text,text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)",
		"inspect_qualification_policy_operation_v1(uuid)",
		"resolve_qualification_policy_authority_v1(uuid)",
		"resolve_current_qualification_policy_authority_v1(uuid,text,text)",
	}
	for _, functionReference := range qualificationPolicyFunctionReferences {
		qualified := schemaName + "." + functionReference
		for _, statement := range []string{
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
			`GRANT EXECUTE ON FUNCTION ` + qualified + ` TO ` + postgresQualificationPolicyOperatorRole,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("secure qualification-policy operator function with %q: %v", statement, err)
			}
		}
	}
	qualificationInputFunctionReferences := []string{
		"qualification_input_precommit_hash_v1(text,bytea)",
		"qualification_input_precommit_timestamp_v1(timestamp with time zone)",
		"qualification_input_precommit_string_is_secret_free_v1(text)",
		"qualification_input_precommit_caller_is_v1(text)",
		"reject_qualification_input_precommit_mutation_v1()",
		"review_qualification_input_precommit_executable_binding_v1(text,bigint,text,text,text)",
		"qualification_input_source_admission_is_exact_v1(text)",
		"qualification_input_credential_admission_is_exact_v1(text)",
		"admit_qualification_input_source_receipt_v1(text,bytea,jsonb,text,bytea,jsonb)",
		"admit_qualification_input_credential_receipt_v1(text,bytea,jsonb,text,bytea,jsonb)",
		"inspect_qualification_input_source_receipt_v1(text)",
		"inspect_qualification_input_credential_receipt_v1(text)",
		"resolve_qualification_input_source_receipt_admission_v1(text)",
		"resolve_qualification_input_credential_receipt_admission_v1(text)",
		"qualification_input_precommit_plan_is_exact_v1(uuid)",
		"qualification_input_precommit_authority_record_is_exact_v1(uuid)",
		"issue_qualification_input_precommit_v1(uuid,uuid,uuid,uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)",
		"inspect_qualification_input_precommit_operation_v1(uuid)",
		"resolve_qualification_input_precommit_authority_v1(uuid)",
		"resolve_qualification_input_precommit_for_promotion_v1(uuid,uuid)",
		"enforce_qualification_input_source_admission_closure_v1()",
		"enforce_qualification_input_credential_admission_closure_v1()",
		"enforce_qualification_input_precommit_authority_closure_v1()",
		"qualification_input_precommit_apply_security_v1()",
	}
	for _, functionReference := range qualificationInputFunctionReferences {
		qualified := schemaName + "." + functionReference
		for _, statement := range []string{
			`ALTER FUNCTION ` + qualified + ` OWNER TO ` + postgresMigrationOwnerRole,
			`REVOKE ALL ON FUNCTION ` + qualified + ` FROM PUBLIC`,
		} {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("secure qualification input function with %q: %v", statement, err)
			}
		}
	}
	qualificationInputOperatorGrants := map[string][]string{
		postgresQualificationInputPrecommitOperatorRole: {
			"issue_qualification_input_precommit_v1(uuid,uuid,uuid,uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb)",
			"inspect_qualification_input_precommit_operation_v1(uuid)",
			"resolve_qualification_input_precommit_authority_v1(uuid)",
		},
		postgresQualificationSourceVerifierOperatorRole: {
			"admit_qualification_input_source_receipt_v1(text,bytea,jsonb,text,bytea,jsonb)",
			"inspect_qualification_input_source_receipt_v1(text)",
			"resolve_qualification_input_source_receipt_admission_v1(text)",
		},
		postgresQualificationCredentialResolverOperatorRole: {
			"admit_qualification_input_credential_receipt_v1(text,bytea,jsonb,text,bytea,jsonb)",
			"inspect_qualification_input_credential_receipt_v1(text)",
			"resolve_qualification_input_credential_receipt_admission_v1(text)",
		},
	}
	for role, functionReferences := range qualificationInputOperatorGrants {
		for _, functionReference := range functionReferences {
			if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+schemaName+`.`+functionReference+` TO `+role); err != nil {
				t.Fatalf("grant qualification input function %s to %s: %v", functionReference, role, err)
			}
		}
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
		facts, scanErr := scanPostgresRolePosture(
			apiDatabase.QueryRowContext(ctx, postgresRolePostureQuery).Scan,
		)
		t.Fatalf("safe real PostgreSQL API login rejected: %v; promotion tables=%d/%d/%d triggers=%d/%d/%d functions=%d/%d/%d; receipt facts tables=%d/%d/%d indexes=%d/%d/%d triggers=%d/%d functions=%d/%d/%d definers=%d; canonical tables=%d/%d/%d indexes=%d/%d/%d triggers=%d/%d functions=%d/%d/%d definers=%d scan=%v",
			err,
			facts.qualificationPromotionTableCount,
			facts.exactQualificationPromotionTableACLCount,
			facts.qualificationPromotionNamedTableCount,
			facts.qualificationPromotionTriggerCount,
			facts.exactQualificationPromotionTriggerContractCount,
			facts.qualificationPromotionNamedTriggerCount,
			facts.qualificationPromotionFunctionCount,
			facts.exactQualificationPromotionFunctionContractCount,
			facts.qualificationPromotionNamedFunctionCount,
			facts.qualificationReceiptV3TableCount,
			facts.exactQualificationReceiptV3TableContractCount,
			facts.qualificationReceiptV3NamedTableCount,
			facts.qualificationReceiptV3IndexCount,
			facts.exactQualificationReceiptV3IndexContractCount,
			facts.qualificationReceiptV3NamedIndexCount,
			facts.exactQualificationReceiptV3TriggerContractCount,
			facts.qualificationReceiptV3TriggerCount,
			facts.qualificationReceiptV3FunctionCount,
			facts.exactQualificationReceiptV3FunctionContractCount,
			facts.qualificationReceiptV3NamedFunctionCount,
			facts.qualificationReceiptV3SecurityDefinerCount,
			facts.canonicalReviewTableCount,
			facts.exactCanonicalReviewTableContractCount,
			facts.canonicalReviewNamedTableCount,
			facts.canonicalReviewIndexCount,
			facts.exactCanonicalReviewIndexContractCount,
			facts.canonicalReviewNamedIndexCount,
			facts.exactCanonicalReviewTriggerContractCount,
			facts.canonicalReviewTriggerCount,
			facts.canonicalReviewFunctionCount,
			facts.exactCanonicalReviewFunctionContractCount,
			facts.canonicalReviewNamedFunctionCount,
			facts.canonicalReviewSecurityDefinerCount,
			scanErr,
		)
	}
	handoffTableContractMutations := []struct {
		name       string
		mutate     string
		restore    string
		wantDetail string
	}{
		{
			name: "Handoff lineage extra column",
			mutate: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_handoff_lineage_members ADD COLUMN posture_drift text`,
			restore: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_handoff_lineage_members DROP COLUMN posture_drift`,
			wantDetail: "exact four-table",
		},
		{
			name: "Handoff binding creation transaction constraint",
			mutate: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_revision_authority_bindings DROP CONSTRAINT qualification_promotion_v2_revisi_creation_transaction_id_check`,
			restore: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_revision_authority_bindings ADD CONSTRAINT qualification_promotion_v2_revisi_creation_transaction_id_check CHECK (creation_transaction_id ~ '^[1-9][0-9]{0,19}$')`,
			wantDetail: "exact four-table",
		},
		{
			name: "Handoff completion creation transaction column",
			mutate: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_handoff_completions RENAME COLUMN creation_transaction_id TO posture_creation_transaction_id`,
			restore: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_handoff_completions RENAME COLUMN posture_creation_transaction_id TO creation_transaction_id`,
			wantDetail: "exact four-table",
		},
		{
			name: "Handoff lineage ordinal constraint index",
			mutate: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_handoff_lineage_members DROP CONSTRAINT qualification_handoff_lineage_members_ordinal_key`,
			restore: `ALTER TABLE ` + schemaName +
				`.qualification_promotion_v2_handoff_lineage_members ADD CONSTRAINT qualification_handoff_lineage_members_ordinal_key UNIQUE (handoff_id, member_kind, member_ordinal)`,
			wantDetail: "exact nineteen-index",
		},
	}
	for _, mutation := range handoffTableContractMutations {
		if _, err := admin.ExecContext(ctx, mutation.mutate); err != nil {
			t.Fatalf("apply %s drift: %v", mutation.name, err)
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), mutation.wantDetail) {
			t.Fatalf("%s posture error = %v", mutation.name, err)
		}
		if _, err := admin.ExecContext(ctx, mutation.restore); err != nil {
			t.Fatalf("restore %s drift: %v", mutation.name, err)
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
			t.Fatalf("post-%s restore posture rejected: %v", mutation.name, err)
		}
	}
	unexpectedPromotionTableACL := `SELECT ON ` + schemaName + `.business_records`
	if _, err := admin.ExecContext(ctx, `GRANT `+unexpectedPromotionTableACL+` TO `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("grant qualification-promotion operator an out-of-bound table ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "without Promotion-operator data ACLs") {
		t.Fatalf("qualification-promotion out-of-bound table ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE `+unexpectedPromotionTableACL+` FROM `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("revoke qualification-promotion operator out-of-bound table ACL: %v", err)
	}
	unexpectedPromotionFunction := schemaName + "." + applicationFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+unexpectedPromotionFunction+` TO `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("grant qualification-promotion operator an out-of-bound routine ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "four-function Promotion-operator") {
		t.Fatalf("qualification-promotion out-of-bound routine ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION `+unexpectedPromotionFunction+` FROM `+postgresQualificationPromotionOperatorRole); err != nil {
		t.Fatalf("revoke qualification-promotion operator out-of-bound routine ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-qualification-promotion ACL restore posture rejected: %v", err)
	}
	unexpectedPolicyTableACL := `SELECT ON ` + schemaName + `.business_records`
	if _, err := admin.ExecContext(ctx, `GRANT `+unexpectedPolicyTableACL+` TO `+postgresQualificationPolicyOperatorRole); err != nil {
		t.Fatalf("grant qualification-policy operator an out-of-bound table ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "qualification policy operator can access") {
		t.Fatalf("qualification-policy out-of-bound table ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE `+unexpectedPolicyTableACL+` FROM `+postgresQualificationPolicyOperatorRole); err != nil {
		t.Fatalf("revoke qualification-policy operator out-of-bound table ACL: %v", err)
	}
	unexpectedPolicyFunction := schemaName + "." + applicationFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+unexpectedPolicyFunction+` TO `+postgresQualificationPolicyOperatorRole); err != nil {
		t.Fatalf("grant qualification-policy operator an out-of-bound routine ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "qualification policy issue") {
		t.Fatalf("qualification-policy out-of-bound routine ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION `+unexpectedPolicyFunction+` FROM `+postgresQualificationPolicyOperatorRole); err != nil {
		t.Fatalf("revoke qualification-policy operator out-of-bound routine ACL: %v", err)
	}
	policyIssueFunction := schemaName + "." + qualificationPolicyFunctionReferences[0]
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION `+policyIssueFunction+` TO `+postgresQualificationPolicyOperatorRole+` WITH GRANT OPTION`); err != nil {
		t.Fatalf("grant qualification-policy operator routine grant option: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "qualification policy issue") {
		t.Fatalf("qualification-policy routine grant-option posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE GRANT OPTION FOR EXECUTE ON FUNCTION `+policyIssueFunction+` FROM `+postgresQualificationPolicyOperatorRole); err != nil {
		t.Fatalf("revoke qualification-policy operator routine grant option: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-qualification-policy ACL restore posture rejected: %v", err)
	}
	workflowInputTable := schemaName + ".workflow_input_authorities"
	if _, err := admin.ExecContext(ctx, `GRANT SELECT ON `+workflowInputTable+` TO `+postgresApplicationRole); err != nil {
		t.Fatalf("grant application an out-of-bound Workflow Input table ACL: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "exact ten-table") {
		t.Fatalf("Workflow Input table ACL posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE SELECT ON `+workflowInputTable+` FROM `+postgresApplicationRole); err != nil {
		t.Fatalf("revoke application Workflow Input table ACL: %v", err)
	}
	workflowInputInspect := schemaName + ".inspect_workflow_input_authority_operation_v1(uuid)"
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+workflowInputInspect+` VOLATILE`); err != nil {
		t.Fatalf("drift Workflow Input inspect volatility: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "Workflow Input application routine") {
		t.Fatalf("Workflow Input application routine drift posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+workflowInputInspect+` STABLE`); err != nil {
		t.Fatalf("restore Workflow Input inspect volatility: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-Workflow-Input-contract restore posture rejected: %v", err)
	}
	workflowAuthorityTriggerMutations := []struct {
		name       string
		mutate     []string
		restore    []string
		wantDetail string
	}{
		{
			name: "missing Workflow Input authority trigger",
			mutate: []string{
				`DROP TRIGGER workflow_input_authorities_immutable ON ` + schemaName + `.workflow_input_authorities`,
			},
			restore: []string{
				`CREATE TRIGGER workflow_input_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON ` + schemaName + `.workflow_input_authorities
FOR EACH STATEMENT EXECUTE FUNCTION ` + schemaName + `.reject_workflow_input_authority_mutation()`,
			},
			wantDetail: "activation-event identity",
		},
		{
			name: "disabled workflow-engine/v3 node guard",
			mutate: []string{
				`ALTER TABLE ` + schemaName + `.workflow_node_runs DISABLE TRIGGER external_qualification_gate_node_v3_guard`,
			},
			restore: []string{
				`ALTER TABLE ` + schemaName + `.workflow_node_runs ENABLE TRIGGER external_qualification_gate_node_v3_guard`,
			},
			wantDetail: "workflow-engine/v3",
		},
		{
			name: "Workflow Input activation-event trigger type",
			mutate: []string{
				`DROP TRIGGER workflow_input_authority_event_identity_guard ON ` + schemaName + `.workflow_run_events`,
				`CREATE TRIGGER workflow_input_authority_event_identity_guard
BEFORE INSERT OR UPDATE ON ` + schemaName + `.workflow_run_events
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_workflow_input_authority_event_identity_v1()`,
			},
			restore: []string{
				`DROP TRIGGER workflow_input_authority_event_identity_guard ON ` + schemaName + `.workflow_run_events`,
				`CREATE TRIGGER workflow_input_authority_event_identity_guard
BEFORE INSERT OR UPDATE OR DELETE ON ` + schemaName + `.workflow_run_events
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_workflow_input_authority_event_identity_v1()`,
			},
			wantDetail: "activation-event identity",
		},
		{
			name: "workflow-engine/v3 node guard function binding",
			mutate: []string{
				`DROP TRIGGER external_qualification_gate_node_v3_guard ON ` + schemaName + `.workflow_node_runs`,
				`CREATE TRIGGER external_qualification_gate_node_v3_guard
BEFORE INSERT OR UPDATE ON ` + schemaName + `.workflow_node_runs
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_workflow_execution_profile_v3_run()`,
			},
			restore: []string{
				`DROP TRIGGER external_qualification_gate_node_v3_guard ON ` + schemaName + `.workflow_node_runs`,
				`CREATE TRIGGER external_qualification_gate_node_v3_guard
BEFORE INSERT OR UPDATE ON ` + schemaName + `.workflow_node_runs
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_external_qualification_gate_node_v3()`,
			},
			wantDetail: "workflow-engine/v3",
		},
		{
			name: "workflow-engine/v3 deferred constraint attributes",
			mutate: []string{
				`DROP TRIGGER workflow_execution_profile_v3_node_exact_closure ON ` + schemaName + `.workflow_node_runs`,
				`CREATE CONSTRAINT TRIGGER workflow_execution_profile_v3_node_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON ` + schemaName + `.workflow_node_runs
DEFERRABLE INITIALLY IMMEDIATE
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.validate_workflow_execution_profile_v3_run_closure()`,
			},
			restore: []string{
				`DROP TRIGGER workflow_execution_profile_v3_node_exact_closure ON ` + schemaName + `.workflow_node_runs`,
				`CREATE CONSTRAINT TRIGGER workflow_execution_profile_v3_node_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON ` + schemaName + `.workflow_node_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.validate_workflow_execution_profile_v3_run_closure()`,
			},
			wantDetail: "workflow-engine/v3",
		},
		{
			name: "workflow-engine/v3 definition update-column contract",
			mutate: []string{
				`DROP TRIGGER workflow_execution_profile_v3_definition_guard ON ` + schemaName + `.workflow_definition_versions`,
				`CREATE TRIGGER workflow_execution_profile_v3_definition_guard
BEFORE INSERT OR UPDATE OF content, content_hash, execution_profile_version ON ` + schemaName + `.workflow_definition_versions
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_workflow_execution_profile_v3_definition()`,
			},
			restore: []string{
				`DROP TRIGGER workflow_execution_profile_v3_definition_guard ON ` + schemaName + `.workflow_definition_versions`,
				`CREATE TRIGGER workflow_execution_profile_v3_definition_guard
BEFORE INSERT OR UPDATE OF content, content_hash, execution_profile_version, execution_profile_hash ON ` + schemaName + `.workflow_definition_versions
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_workflow_execution_profile_v3_definition()`,
			},
			wantDetail: "workflow-engine/v3",
		},
		{
			name: "shared Workflow legacy trigger function binding",
			mutate: []string{
				`DROP TRIGGER workflow_run_governance_mode_immutable ON ` + schemaName + `.workflow_runs`,
				`CREATE TRIGGER workflow_run_governance_mode_immutable
BEFORE UPDATE ON ` + schemaName + `.workflow_runs
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_workflow_run_execution_profile_identity()`,
			},
			restore: []string{
				`DROP TRIGGER workflow_run_governance_mode_immutable ON ` + schemaName + `.workflow_runs`,
				`CREATE TRIGGER workflow_run_governance_mode_immutable
BEFORE UPDATE ON ` + schemaName + `.workflow_runs
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.workflow_run_governance_mode_immutable()`,
			},
			wantDetail: "legacy trigger contracts",
		},
		{
			name: "unexpected trigger on a shared Workflow relation",
			mutate: []string{
				`CREATE TRIGGER posture_unexpected_shared_workflow_guard
BEFORE INSERT ON ` + schemaName + `.workflow_run_events
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_workflow_input_authority_event_identity_v1()`,
			},
			restore: []string{
				`DROP TRIGGER posture_unexpected_shared_workflow_guard ON ` + schemaName + `.workflow_run_events`,
			},
			wantDetail: "trigger allowlist",
		},
		{
			name: "unexpected trigger on a Workflow Input authority table",
			mutate: []string{
				`CREATE TRIGGER posture_unexpected_authority_guard
BEFORE INSERT ON ` + schemaName + `.workflow_input_authorities
FOR EACH STATEMENT EXECUTE FUNCTION ` + schemaName + `.reject_workflow_input_authority_mutation()`,
			},
			restore: []string{
				`DROP TRIGGER posture_unexpected_authority_guard ON ` + schemaName + `.workflow_input_authorities`,
			},
			wantDetail: "activation-event identity",
		},
	}
	for _, mutation := range workflowAuthorityTriggerMutations {
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
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
			t.Fatalf("post-%s restore posture rejected: %v", mutation.name, err)
		}
	}
	workflowInputRejectFunction := schemaName + ".reject_workflow_input_authority_mutation()"
	workflowInputEventGuardFunction := schemaName + ".guard_workflow_input_authority_event_identity_v1()"
	workflowV3DefinitionGuardFunction := schemaName + ".guard_workflow_execution_profile_v3_definition()"
	workflowAuthorityTriggerFunctionMutations := []struct {
		name    string
		mutate  []string
		restore []string
	}{
		{
			name: "Workflow authority trigger function owner",
			mutate: []string{
				`ALTER FUNCTION ` + workflowInputRejectFunction + ` OWNER TO ` + ownerRole,
			},
			restore: []string{
				`ALTER FUNCTION ` + workflowInputRejectFunction + ` OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + workflowInputRejectFunction + ` FROM ` + ownerRole,
				`REVOKE ALL ON FUNCTION ` + workflowInputRejectFunction + ` FROM PUBLIC`,
			},
		},
		{
			name: "Workflow authority trigger function ACL",
			mutate: []string{
				`GRANT EXECUTE ON FUNCTION ` + workflowInputRejectFunction + ` TO ` + bypassRole,
			},
			restore: []string{
				`REVOKE EXECUTE ON FUNCTION ` + workflowInputRejectFunction + ` FROM ` + bypassRole,
			},
		},
		{
			name: "Workflow authority trigger function language",
			mutate: []string{
				`CREATE OR REPLACE FUNCTION ` + workflowInputRejectFunction + ` RETURNS trigger
LANGUAGE internal SECURITY INVOKER VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SET search_path TO pg_catalog AS 'suppress_redundant_updates_trigger'`,
			},
			restore: []string{
				`CREATE OR REPLACE FUNCTION ` + workflowInputRejectFunction + ` RETURNS trigger
LANGUAGE plpgsql SECURITY INVOKER VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SET search_path TO pg_catalog AS $$ BEGIN RETURN NULL; END $$`,
			},
		},
		{
			name: "Workflow authority trigger function security mode",
			mutate: []string{
				`ALTER FUNCTION ` + workflowV3DefinitionGuardFunction + ` SECURITY INVOKER`,
			},
			restore: []string{
				`ALTER FUNCTION ` + workflowV3DefinitionGuardFunction + ` SECURITY DEFINER`,
			},
		},
		{
			name: "Workflow authority trigger function search path",
			mutate: []string{
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` SET search_path TO pg_catalog, ` + schemaName + `, pg_temp`,
			},
			restore: []string{
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` SET search_path TO pg_catalog, ` + schemaName,
			},
		},
		{
			name: "Workflow authority trigger function execution attributes",
			mutate: []string{
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` STABLE`,
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` STRICT`,
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` PARALLEL SAFE`,
			},
			restore: []string{
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` VOLATILE`,
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` CALLED ON NULL INPUT`,
				`ALTER FUNCTION ` + workflowInputEventGuardFunction + ` PARALLEL UNSAFE`,
			},
		},
		{
			name: "Workflow authority trigger function signature overload",
			mutate: []string{
				`CREATE FUNCTION ` + schemaName + `.guard_external_qualification_gate_node_v3(integer)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog AS $$ SELECT true $$`,
				`ALTER FUNCTION ` + schemaName + `.guard_external_qualification_gate_node_v3(integer) OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + schemaName + `.guard_external_qualification_gate_node_v3(integer) FROM PUBLIC`,
			},
			restore: []string{
				`DROP FUNCTION ` + schemaName + `.guard_external_qualification_gate_node_v3(integer)`,
			},
		},
	}
	for _, mutation := range workflowAuthorityTriggerFunctionMutations {
		for _, statement := range mutation.mutate {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply %s drift with %q: %v", mutation.name, statement, err)
			}
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "trigger function owner") {
			t.Fatalf("%s posture error = %v", mutation.name, err)
		}
		for _, statement := range mutation.restore {
			if _, err := admin.ExecContext(ctx, statement); err != nil {
				t.Fatalf("restore %s drift with %q: %v", mutation.name, statement, err)
			}
		}
		if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
			t.Fatalf("post-%s restore posture rejected: %v", mutation.name, err)
		}
	}
	workflowLegacyDefinitionGuardFunction := schemaName + ".guard_workflow_definition_execution_profile_identity()"
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+workflowLegacyDefinitionGuardFunction+` SECURITY DEFINER`); err != nil {
		t.Fatalf("drift shared Workflow legacy trigger function security mode: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); !errors.Is(err, ErrUnsafePostgresAPIRolePosture) || !strings.Contains(err.Error(), "legacy trigger function") {
		t.Fatalf("shared Workflow legacy trigger function posture error = %v", err)
	}
	if _, err := admin.ExecContext(ctx, `ALTER FUNCTION `+workflowLegacyDefinitionGuardFunction+` SECURITY INVOKER`); err != nil {
		t.Fatalf("restore shared Workflow legacy trigger function security mode: %v", err)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, apiDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf("post-shared-Workflow-legacy-trigger-function restore posture rejected: %v", err)
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
	qualificationReceiptV3SHA := schemaName + ".qualification_receipt_v3_sha256(bytea)"
	qualificationReceiptV3Start := schemaName + ".start_qualification_receipt_v3_requests(text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea)"
	qualificationReceiptV3ContractMutations := []struct {
		name       string
		mutate     []string
		restore    []string
		wantDetail string
	}{
		{
			name:       "Qualification Receipt v3 table ACL",
			mutate:     []string{`GRANT SELECT ON ` + schemaName + `.qualification_receipt_v3_requests TO ` + bypassRole},
			restore:    []string{`REVOKE SELECT ON ` + schemaName + `.qualification_receipt_v3_requests FROM ` + bypassRole},
			wantDetail: "Qualification Receipt v3 tables",
		},
		{
			name: "Qualification Receipt v3 explicit index key columns",
			mutate: []string{
				`DROP INDEX ` + schemaName + `.qualification_receipt_v3_receipts_target_idx`,
				`CREATE INDEX qualification_receipt_v3_receipts_target_idx ON ` + schemaName + `.qualification_receipt_v3_receipts (receipt_id)`,
			},
			restore: []string{
				`DROP INDEX ` + schemaName + `.qualification_receipt_v3_receipts_target_idx`,
				`CREATE INDEX qualification_receipt_v3_receipts_target_idx ON ` + schemaName + `.qualification_receipt_v3_receipts (project_id, workflow_run_id, node_key, target_revision_id)`,
			},
			wantDetail: "exact fourteen-index",
		},
		{
			name: "Qualification Receipt v3 unique constraint binding",
			mutate: []string{
				`ALTER TABLE ` + schemaName + `.qualification_receipt_v3_receipts DROP CONSTRAINT qualification_receipt_v3_receipts_plan_authority_id_key`,
				`CREATE UNIQUE INDEX qualification_receipt_v3_receipts_plan_authority_id_key ON ` + schemaName + `.qualification_receipt_v3_receipts (plan_authority_id)`,
			},
			restore: []string{
				`DROP INDEX ` + schemaName + `.qualification_receipt_v3_receipts_plan_authority_id_key`,
				`ALTER TABLE ` + schemaName + `.qualification_receipt_v3_receipts ADD CONSTRAINT qualification_receipt_v3_receipts_plan_authority_id_key UNIQUE (plan_authority_id)`,
			},
			wantDetail: "exact fourteen-index",
		},
		{
			name: "Qualification Receipt v3 partial index predicate",
			mutate: []string{
				`DROP INDEX ` + schemaName + `.qualification_receipt_v3_receipts_target_idx`,
				`CREATE INDEX qualification_receipt_v3_receipts_target_idx ON ` + schemaName + `.qualification_receipt_v3_receipts (project_id, workflow_run_id, node_key, target_revision_id) WHERE project_id IS NOT NULL`,
			},
			restore: []string{
				`DROP INDEX ` + schemaName + `.qualification_receipt_v3_receipts_target_idx`,
				`CREATE INDEX qualification_receipt_v3_receipts_target_idx ON ` + schemaName + `.qualification_receipt_v3_receipts (project_id, workflow_run_id, node_key, target_revision_id)`,
			},
			wantDetail: "exact fourteen-index",
		},
		{
			name:       "Qualification Receipt v3 disabled history guard",
			mutate:     []string{`ALTER TABLE ` + schemaName + `.qualification_evidence_events DISABLE TRIGGER qualification_evidence_v1_receipt_tail_history_only`},
			restore:    []string{`ALTER TABLE ` + schemaName + `.qualification_evidence_events ENABLE TRIGGER qualification_evidence_v1_receipt_tail_history_only`},
			wantDetail: "history-only trigger",
		},
		{
			name: "Qualification Receipt v3 conditional history guard",
			mutate: []string{
				`DROP TRIGGER qualification_evidence_v1_receipt_tail_history_only ON ` + schemaName + `.qualification_evidence_events`,
				`CREATE TRIGGER qualification_evidence_v1_receipt_tail_history_only
BEFORE INSERT ON ` + schemaName + `.qualification_evidence_events
FOR EACH ROW WHEN (false)
EXECUTE FUNCTION ` + schemaName + `.guard_qualification_evidence_v1_receipt_tail_history_only()`,
			},
			restore: []string{
				`DROP TRIGGER qualification_evidence_v1_receipt_tail_history_only ON ` + schemaName + `.qualification_evidence_events`,
				`CREATE TRIGGER qualification_evidence_v1_receipt_tail_history_only
BEFORE INSERT ON ` + schemaName + `.qualification_evidence_events
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_qualification_evidence_v1_receipt_tail_history_only()`,
			},
			wantDetail: "history-only trigger",
		},
		{
			name: "Qualification Receipt v3 arbitrary extra trigger",
			mutate: []string{`CREATE TRIGGER posture_receipt_unexpected
BEFORE INSERT ON ` + schemaName + `.qualification_receipt_v3_requests
FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.reject_qualification_receipt_v3_mutation()`},
			restore:    []string{`DROP TRIGGER posture_receipt_unexpected ON ` + schemaName + `.qualification_receipt_v3_requests`},
			wantDetail: "history-only trigger",
		},
		{
			name:       "Qualification Receipt v3 SHA-256 ACL",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + qualificationReceiptV3SHA + ` TO ` + bypassRole},
			restore:    []string{`REVOKE EXECUTE ON FUNCTION ` + qualificationReceiptV3SHA + ` FROM ` + bypassRole},
			wantDetail: "owner-only ACL",
		},
		{
			name:       "Qualification Receipt v3 writer security mode",
			mutate:     []string{`ALTER FUNCTION ` + qualificationReceiptV3Start + ` SECURITY INVOKER`},
			restore:    []string{`ALTER FUNCTION ` + qualificationReceiptV3Start + ` SECURITY DEFINER`},
			wantDetail: "exact three-function SECURITY DEFINER",
		},
		{
			name: "Qualification Receipt v3 extra named routine",
			mutate: []string{
				`CREATE FUNCTION ` + schemaName + `.qualification_receipt_v3_unexpected() RETURNS boolean LANGUAGE sql SECURITY INVOKER SET search_path TO pg_catalog AS $$ SELECT false $$`,
				`ALTER FUNCTION ` + schemaName + `.qualification_receipt_v3_unexpected() OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + schemaName + `.qualification_receipt_v3_unexpected() FROM PUBLIC`,
			},
			restore:    []string{`DROP FUNCTION ` + schemaName + `.qualification_receipt_v3_unexpected()`},
			wantDetail: "Qualification Receipt v3 SHA-256",
		},
	}
	for _, mutation := range qualificationReceiptV3ContractMutations {
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
		t.Fatalf("post-Qualification-Receipt-v3-contract restore posture rejected: %v", err)
	}
	canonicalReviewTable := schemaName + ".canonical_review_approval_receipts"
	canonicalReviewTextTrimmed := schemaName + ".canonical_review_text_is_trimmed(text)"
	canonicalReviewTimestampExact := schemaName + ".canonical_review_timestamp_is_exact(text)"
	canonicalReviewHash := schemaName + ".canonical_review_authority_hash(text,bytea)"
	canonicalReviewJSON := schemaName + ".canonical_review_jsonb_bytes(jsonb)"
	canonicalReviewRecordExact := schemaName +
		".canonical_review_approval_receipt_record_is_exact(" + schemaName +
		".canonical_review_approval_receipts)"
	canonicalReviewIssue := schemaName + ".issue_canonical_review_approval_receipt(uuid)"
	canonicalReviewContractMutations := []struct {
		name       string
		mutate     []string
		restore    []string
		wantDetail string
	}{
		{
			name:       "Canonical Review table column nullability",
			mutate:     []string{`ALTER TABLE ` + canonicalReviewTable + ` ALTER COLUMN receipt_hash DROP NOT NULL`},
			restore:    []string{`ALTER TABLE ` + canonicalReviewTable + ` ALTER COLUMN receipt_hash SET NOT NULL`},
			wantDetail: "table columns",
		},
		{
			name:       "Canonical Review table ACL",
			mutate:     []string{`GRANT SELECT ON ` + canonicalReviewTable + ` TO ` + bypassRole},
			restore:    []string{`REVOKE SELECT ON ` + canonicalReviewTable + ` FROM ` + bypassRole},
			wantDetail: "owner-only ACL",
		},
		{
			name:   "Canonical Review table owner",
			mutate: []string{`ALTER TABLE ` + canonicalReviewTable + ` OWNER TO ` + ownerRole},
			restore: []string{
				`ALTER TABLE ` + canonicalReviewTable + ` OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON TABLE ` + canonicalReviewTable + ` FROM ` + ownerRole,
				`REVOKE ALL ON TABLE ` + canonicalReviewTable + ` FROM PUBLIC`,
			},
			wantDetail: "persistence, owner",
		},
		{
			name: "Canonical Review index key order",
			mutate: []string{
				`DROP INDEX ` + schemaName + `.canonical_review_receipts_target_idx`,
				`CREATE INDEX canonical_review_receipts_target_idx ON ` + canonicalReviewTable + ` (artifact_id, project_id, revision_id)`,
			},
			restore: []string{
				`DROP INDEX ` + schemaName + `.canonical_review_receipts_target_idx`,
				`CREATE INDEX canonical_review_receipts_target_idx ON ` + canonicalReviewTable + ` (project_id, artifact_id, revision_id)`,
			},
			wantDetail: "ordered keys",
		},
		{
			name: "Canonical Review index operator class",
			mutate: []string{
				`ALTER TABLE ` + canonicalReviewTable + ` DROP CONSTRAINT canonical_review_receipts_hash_key`,
				`CREATE UNIQUE INDEX canonical_review_receipts_hash_key ON ` + canonicalReviewTable + ` (receipt_hash text_pattern_ops)`,
			},
			restore: []string{
				`DROP INDEX ` + schemaName + `.canonical_review_receipts_hash_key`,
				`ALTER TABLE ` + canonicalReviewTable + ` ADD CONSTRAINT canonical_review_receipts_hash_key UNIQUE (receipt_hash)`,
			},
			wantDetail: "operator classes",
		},
		{
			name: "Canonical Review index collation",
			mutate: []string{
				`ALTER TABLE ` + canonicalReviewTable + ` DROP CONSTRAINT canonical_review_receipts_hash_key`,
				`CREATE UNIQUE INDEX canonical_review_receipts_hash_key ON ` + canonicalReviewTable + ` (receipt_hash COLLATE "C")`,
			},
			restore: []string{
				`DROP INDEX ` + schemaName + `.canonical_review_receipts_hash_key`,
				`ALTER TABLE ` + canonicalReviewTable + ` ADD CONSTRAINT canonical_review_receipts_hash_key UNIQUE (receipt_hash)`,
			},
			wantDetail: "collations",
		},
		{
			name: "Canonical Review unique constraint binding",
			mutate: []string{
				`ALTER TABLE ` + canonicalReviewTable + ` DROP CONSTRAINT canonical_review_receipts_hash_key`,
				`CREATE UNIQUE INDEX canonical_review_receipts_hash_key ON ` + canonicalReviewTable + ` (receipt_hash)`,
			},
			restore: []string{
				`DROP INDEX ` + schemaName + `.canonical_review_receipts_hash_key`,
				`ALTER TABLE ` + canonicalReviewTable + ` ADD CONSTRAINT canonical_review_receipts_hash_key UNIQUE (receipt_hash)`,
			},
			wantDetail: "constraint bindings",
		},
		{
			name:       "Canonical Review ordinary trigger enablement",
			mutate:     []string{`ALTER TABLE ` + canonicalReviewTable + ` DISABLE TRIGGER canonical_review_approval_receipts_immutable`},
			restore:    []string{`ALTER TABLE ` + canonicalReviewTable + ` ENABLE TRIGGER canonical_review_approval_receipts_immutable`},
			wantDetail: "ordinary or deferred",
		},
		{
			name: "Canonical Review ordinary trigger function binding",
			mutate: []string{
				`DROP TRIGGER canonical_review_requests_controlled_mutation ON ` + schemaName + `.review_requests`,
				`CREATE TRIGGER canonical_review_requests_controlled_mutation BEFORE UPDATE OR DELETE ON ` + schemaName + `.review_requests FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.reject_canonical_review_receipt_mutation()`,
			},
			restore: []string{
				`DROP TRIGGER canonical_review_requests_controlled_mutation ON ` + schemaName + `.review_requests`,
				`CREATE TRIGGER canonical_review_requests_controlled_mutation BEFORE UPDATE OR DELETE ON ` + schemaName + `.review_requests FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.guard_canonical_review_source_mutation()`,
			},
			wantDetail: "trigger attributes",
		},
		{
			name: "Canonical Review deferred trigger mode",
			mutate: []string{
				`DROP TRIGGER canonical_review_approved_requires_receipt ON ` + schemaName + `.review_requests`,
				`CREATE CONSTRAINT TRIGGER canonical_review_approved_requires_receipt AFTER INSERT OR UPDATE OF status ON ` + schemaName + `.review_requests DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.require_canonical_review_approval_receipt()`,
			},
			restore: []string{
				`DROP TRIGGER canonical_review_approved_requires_receipt ON ` + schemaName + `.review_requests`,
				`CREATE CONSTRAINT TRIGGER canonical_review_approved_requires_receipt AFTER INSERT OR UPDATE OF status ON ` + schemaName + `.review_requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.require_canonical_review_approval_receipt()`,
			},
			wantDetail: "deferred constraint trigger",
		},
		{
			name: "Canonical Review deferred trigger update columns",
			mutate: []string{
				`DROP TRIGGER canonical_review_approved_requires_receipt ON ` + schemaName + `.review_requests`,
				`CREATE CONSTRAINT TRIGGER canonical_review_approved_requires_receipt AFTER INSERT OR UPDATE ON ` + schemaName + `.review_requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.require_canonical_review_approval_receipt()`,
			},
			restore: []string{
				`DROP TRIGGER canonical_review_approved_requires_receipt ON ` + schemaName + `.review_requests`,
				`CREATE CONSTRAINT TRIGGER canonical_review_approved_requires_receipt AFTER INSERT OR UPDATE OF status ON ` + schemaName + `.review_requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ` + schemaName + `.require_canonical_review_approval_receipt()`,
			},
			wantDetail: "trigger attributes",
		},
		{
			name:   "Canonical Review function owner",
			mutate: []string{`ALTER FUNCTION ` + canonicalReviewRecordExact + ` OWNER TO ` + ownerRole},
			restore: []string{
				`ALTER FUNCTION ` + canonicalReviewRecordExact + ` OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + canonicalReviewRecordExact + ` FROM ` + ownerRole,
				`REVOKE ALL ON FUNCTION ` + canonicalReviewRecordExact + ` FROM PUBLIC`,
			},
			wantDetail: "routine identity arguments",
		},
		{
			name:       "Canonical Review owner-only function ACL",
			mutate:     []string{`GRANT EXECUTE ON FUNCTION ` + canonicalReviewHash + ` TO ` + bypassRole},
			restore:    []string{`REVOKE EXECUTE ON FUNCTION ` + canonicalReviewHash + ` FROM ` + bypassRole},
			wantDetail: "owner, ACL",
		},
		{
			name:       "Canonical Review function security mode",
			mutate:     []string{`ALTER FUNCTION ` + canonicalReviewIssue + ` SECURITY INVOKER`},
			restore:    []string{`ALTER FUNCTION ` + canonicalReviewIssue + ` SECURITY DEFINER`},
			wantDetail: "exact four-function SECURITY DEFINER",
		},
		{
			name:       "Canonical Review function search path",
			mutate:     []string{`ALTER FUNCTION ` + canonicalReviewJSON + ` SET search_path TO pg_catalog`},
			restore:    []string{`ALTER FUNCTION ` + canonicalReviewJSON + ` SET search_path TO pg_catalog, ` + schemaName},
			wantDetail: "search_path",
		},
		{
			name:       "Canonical Review trim helper parallel safety",
			mutate:     []string{`ALTER FUNCTION ` + canonicalReviewTextTrimmed + ` PARALLEL RESTRICTED`},
			restore:    []string{`ALTER FUNCTION ` + canonicalReviewTextTrimmed + ` PARALLEL SAFE`},
			wantDetail: "parallel safety",
		},
		{
			name:       "Canonical Review timestamp helper volatility",
			mutate:     []string{`ALTER FUNCTION ` + canonicalReviewTimestampExact + ` STABLE`},
			restore:    []string{`ALTER FUNCTION ` + canonicalReviewTimestampExact + ` IMMUTABLE`},
			wantDetail: "volatility",
		},
		{
			name:       "Canonical Review function volatility",
			mutate:     []string{`ALTER FUNCTION ` + canonicalReviewHash + ` VOLATILE`},
			restore:    []string{`ALTER FUNCTION ` + canonicalReviewHash + ` IMMUTABLE`},
			wantDetail: "volatility",
		},
		{
			name:       "Canonical Review function strictness",
			mutate:     []string{`ALTER FUNCTION ` + canonicalReviewHash + ` CALLED ON NULL INPUT`},
			restore:    []string{`ALTER FUNCTION ` + canonicalReviewHash + ` STRICT`},
			wantDetail: "strictness",
		},
		{
			name:       "Canonical Review function parallel safety",
			mutate:     []string{`ALTER FUNCTION ` + canonicalReviewHash + ` PARALLEL UNSAFE`},
			restore:    []string{`ALTER FUNCTION ` + canonicalReviewHash + ` PARALLEL SAFE`},
			wantDetail: "parallel safety",
		},
		{
			name: "Canonical Review function identity arguments",
			mutate: []string{
				`ALTER FUNCTION ` + canonicalReviewHash + ` RENAME TO posture_saved_canonical_review_authority_hash`,
				`CREATE FUNCTION ` + schemaName + `.canonical_review_authority_hash(p_domain text) RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER SET search_path TO pg_catalog AS $$ SELECT ''::text $$`,
				`ALTER FUNCTION ` + schemaName + `.canonical_review_authority_hash(text) OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + schemaName + `.canonical_review_authority_hash(text) FROM PUBLIC`,
			},
			restore: []string{
				`DROP FUNCTION ` + schemaName + `.canonical_review_authority_hash(text)`,
				`ALTER FUNCTION ` + schemaName + `.posture_saved_canonical_review_authority_hash(text,bytea) RENAME TO canonical_review_authority_hash`,
			},
			wantDetail: "identity arguments",
		},
		{
			name: "Canonical Review function result",
			mutate: []string{
				`ALTER FUNCTION ` + canonicalReviewHash + ` RENAME TO posture_saved_canonical_review_authority_hash`,
				`CREATE FUNCTION ` + schemaName + `.canonical_review_authority_hash(p_domain text, p_value bytea) RETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER SET search_path TO pg_catalog AS $$ SELECT false $$`,
				`ALTER FUNCTION ` + schemaName + `.canonical_review_authority_hash(text,bytea) OWNER TO ` + postgresMigrationOwnerRole,
				`REVOKE ALL ON FUNCTION ` + schemaName + `.canonical_review_authority_hash(text,bytea) FROM PUBLIC`,
			},
			restore: []string{
				`DROP FUNCTION ` + schemaName + `.canonical_review_authority_hash(text,bytea)`,
				`ALTER FUNCTION ` + schemaName + `.posture_saved_canonical_review_authority_hash(text,bytea) RENAME TO canonical_review_authority_hash`,
			},
			wantDetail: "results",
		},
	}
	for _, mutation := range canonicalReviewContractMutations {
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
		t.Fatalf("post-Canonical-Review-contract restore posture rejected: %v", err)
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
		t.Fatalf("apply current real migrations for API posture: %v", err)
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
		migratedFacts.workflowInputAuthorityTriggerCount != postgresExpectedWorkflowInputAuthorityTriggers ||
		migratedFacts.exactWorkflowInputAuthorityTriggerContractCount != postgresExpectedWorkflowInputAuthorityTriggers ||
		migratedFacts.workflowInputAuthorityNamedTriggerCount != postgresExpectedWorkflowInputAuthorityTriggers ||
		migratedFacts.workflowExecutionProfileV3TriggerCount != postgresExpectedWorkflowExecutionProfileV3Triggers ||
		migratedFacts.exactWorkflowExecutionProfileV3TriggerContractCount != postgresExpectedWorkflowExecutionProfileV3Triggers ||
		migratedFacts.workflowExecutionProfileV3NamedTriggerCount != postgresExpectedWorkflowExecutionProfileV3Triggers ||
		migratedFacts.workflowExecutionProfileV3ExactHashContractCount != postgresExpectedWorkflowExecutionProfileV3HashContracts ||
		migratedFacts.workflowSharedLegacyTriggerCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		migratedFacts.exactWorkflowSharedLegacyTriggerContractCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		migratedFacts.workflowSharedRelationTriggerCount != postgresExpectedWorkflowSharedRelationTriggers ||
		migratedFacts.workflowAuthorityTriggerFunctionCount != postgresExpectedWorkflowAuthorityTriggerFunctions ||
		migratedFacts.exactWorkflowAuthorityTriggerFunctionContractCount != postgresExpectedWorkflowAuthorityTriggerFunctions ||
		migratedFacts.workflowAuthorityTriggerNamedFunctionCount != postgresExpectedWorkflowAuthorityTriggerFunctions ||
		migratedFacts.workflowSharedLegacyTriggerFunctionCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		migratedFacts.exactWorkflowSharedLegacyTriggerFunctionContractCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		migratedFacts.workflowSharedLegacyTriggerNamedFunctionCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		migratedFacts.qualificationEvidenceTableCount != postgresExpectedQualificationEvidenceTables ||
		migratedFacts.qualificationEvidenceFunctionCount != postgresExpectedQualificationEvidenceFunctions ||
		migratedFacts.qualificationEvidenceNamedFunctionCount != postgresExpectedQualificationEvidenceNamedFunctions ||
		migratedFacts.qualificationEvidenceTriggerCount != postgresExpectedQualificationEvidenceTotalTriggers ||
		migratedFacts.qualificationPlanTableCount != postgresExpectedQualificationPlanTables ||
		migratedFacts.qualificationPlanIndexCount != postgresExpectedQualificationPlanIndexes ||
		migratedFacts.qualificationPlanFunctionCount != postgresExpectedQualificationPlanFunctions ||
		migratedFacts.qualificationPlanTriggerCount != postgresExpectedQualificationPlanTriggers ||
		migratedFacts.qualificationReceiptV3TableCount != postgresExpectedQualificationReceiptV3Tables ||
		migratedFacts.qualificationReceiptV3IndexCount != postgresExpectedQualificationReceiptV3Indexes ||
		migratedFacts.qualificationReceiptV3FunctionCount != postgresExpectedQualificationReceiptV3Functions ||
		migratedFacts.qualificationReceiptV3SecurityDefinerCount != postgresExpectedQualificationReceiptV3Definers ||
		migratedFacts.qualificationReceiptV3TriggerCount != postgresExpectedQualificationReceiptV3Triggers ||
		migratedFacts.canonicalReviewTableCount != postgresExpectedCanonicalReviewTables ||
		migratedFacts.canonicalReviewIndexCount != postgresExpectedCanonicalReviewIndexes ||
		migratedFacts.canonicalReviewFunctionCount != postgresExpectedCanonicalReviewFunctions ||
		migratedFacts.canonicalReviewSecurityDefinerCount != postgresExpectedCanonicalReviewDefiners ||
		migratedFacts.canonicalReviewTriggerCount != postgresExpectedCanonicalReviewTriggers {
		t.Fatalf(
			"real migrated catalog counts = protected:%d tables:%d indexes:%d routines:%d internal:%d definers:%d workflow-input-triggers:%d/%d/%d workflow-v3-triggers:%d/%d/%d workflow-v3-hash-contracts:%d workflow-shared-triggers:%d/%d/%d workflow-authority-trigger-functions:%d/%d/%d workflow-legacy-trigger-functions:%d/%d/%d evidence-tables:%d evidence-functions:%d evidence-named-functions:%d evidence-triggers:%d plan-tables:%d plan-indexes:%d plan-functions:%d plan-triggers:%d receipt-tables:%d receipt-indexes:%d receipt-functions:%d receipt-definers:%d receipt-triggers:%d canonical-tables:%d canonical-indexes:%d canonical-functions:%d canonical-definers:%d canonical-triggers:%d",
			migratedFacts.protectedTableCount,
			migratedFacts.ownedBoundaryTableCount,
			migratedFacts.ownedBoundaryIndexCount,
			migratedFacts.ownedBoundaryRoutineCount,
			migratedFacts.internalFunctionCount,
			migratedFacts.securityDefinerFunctionCount,
			migratedFacts.workflowInputAuthorityTriggerCount,
			migratedFacts.exactWorkflowInputAuthorityTriggerContractCount,
			migratedFacts.workflowInputAuthorityNamedTriggerCount,
			migratedFacts.workflowExecutionProfileV3TriggerCount,
			migratedFacts.exactWorkflowExecutionProfileV3TriggerContractCount,
			migratedFacts.workflowExecutionProfileV3NamedTriggerCount,
			migratedFacts.workflowExecutionProfileV3ExactHashContractCount,
			migratedFacts.workflowSharedLegacyTriggerCount,
			migratedFacts.exactWorkflowSharedLegacyTriggerContractCount,
			migratedFacts.workflowSharedRelationTriggerCount,
			migratedFacts.workflowAuthorityTriggerFunctionCount,
			migratedFacts.exactWorkflowAuthorityTriggerFunctionContractCount,
			migratedFacts.workflowAuthorityTriggerNamedFunctionCount,
			migratedFacts.workflowSharedLegacyTriggerFunctionCount,
			migratedFacts.exactWorkflowSharedLegacyTriggerFunctionContractCount,
			migratedFacts.workflowSharedLegacyTriggerNamedFunctionCount,
			migratedFacts.qualificationEvidenceTableCount,
			migratedFacts.qualificationEvidenceFunctionCount,
			migratedFacts.qualificationEvidenceNamedFunctionCount,
			migratedFacts.qualificationEvidenceTriggerCount,
			migratedFacts.qualificationPlanTableCount,
			migratedFacts.qualificationPlanIndexCount,
			migratedFacts.qualificationPlanFunctionCount,
			migratedFacts.qualificationPlanTriggerCount,
			migratedFacts.qualificationReceiptV3TableCount,
			migratedFacts.qualificationReceiptV3IndexCount,
			migratedFacts.qualificationReceiptV3FunctionCount,
			migratedFacts.qualificationReceiptV3SecurityDefinerCount,
			migratedFacts.qualificationReceiptV3TriggerCount,
			migratedFacts.canonicalReviewTableCount,
			migratedFacts.canonicalReviewIndexCount,
			migratedFacts.canonicalReviewFunctionCount,
			migratedFacts.canonicalReviewSecurityDefinerCount,
			migratedFacts.canonicalReviewTriggerCount,
		)
	}
	if err := VerifyPostgresAPIRolePosture(ctx, migratedAPIDatabase, config.EnvironmentProduction); err != nil {
		t.Fatalf(
			"real migrated-schema API posture rejected: %v; canonical indexes=%d/%d/%d; promotion routines=%d/%d unexpected=%d; policy routines=%d/%d unexpected=%d; reachable definers=%d expected=%d unexpected=%d",
			err,
			migratedFacts.canonicalReviewIndexCount,
			migratedFacts.exactCanonicalReviewIndexContractCount,
			migratedFacts.canonicalReviewNamedIndexCount,
			migratedFacts.qualificationPromotionFunctionCount,
			migratedFacts.exactQualificationPromotionFunctionContractCount,
			migratedFacts.unexpectedQualificationPromotionFunctionACLCount,
			migratedFacts.qualificationPolicyFunctionCount,
			migratedFacts.exactQualificationPolicyFunctionContractCount,
			migratedFacts.unexpectedQualificationPolicyFunctionACLCount,
			migratedFacts.reachableExecutableSecurityDefinerCount,
			migratedFacts.reachableExpectedApplicationDefinerCount,
			migratedFacts.reachableUnexpectedSecurityDefinerCount,
		)
	}
}

func qualificationHandoffPostureFunctionDefinitions(schemaName string) []string {
	return []string{
		fmt.Sprintf(`
CREATE FUNCTION %s.qualification_handoff_v1_hash(text, bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ SELECT ''::text $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.qualification_handoff_v1_timestamp(timestamptz)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ SELECT ''::text $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.reject_qualification_handoff_v1_mutation()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.enqueue_qualification_promotion_v2_handoff_v1()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.qualification_handoff_v1_quality_result(uuid, uuid)
RETURNS jsonb LANGUAGE plpgsql STABLE STRICT PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN '{}'::jsonb; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.qualification_handoff_v1_completion_is_exact(uuid)
RETURNS boolean LANGUAGE plpgsql STABLE CALLED ON NULL INPUT PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.qualification_handoff_v1_completion_bundle(uuid, boolean, boolean)
RETURNS jsonb LANGUAGE plpgsql STABLE CALLED ON NULL INPUT PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN '{}'::jsonb; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.inspect_qualification_promotion_v2_handoff_completion(uuid)
RETURNS SETOF jsonb LANGUAGE plpgsql STABLE CALLED ON NULL INPUT PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.complete_qualification_promotion_v2_handoff(uuid)
RETURNS SETOF jsonb LANGUAGE plpgsql VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
		fmt.Sprintf(`
CREATE FUNCTION %s.validate_qualification_handoff_v1_closure()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
	}
}

func qualificationInputPostureFunctionDefinitions(schemaName string) []string {
	return []string{
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_precommit_hash_v1(text, bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ SELECT ''::text $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_precommit_timestamp_v1(timestamptz)
RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ SELECT ''::text $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_precommit_string_is_secret_free_v1(text)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ SELECT true $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_precommit_caller_is_v1(text)
RETURNS boolean LANGUAGE sql STABLE STRICT PARALLEL SAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ SELECT false $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.reject_qualification_input_precommit_mutation_v1()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.review_qualification_input_precommit_executable_binding_v1(
  text, bigint, text, text, text
) RETURNS SETOF %s.qualification_input_precommit_executable_binding_generations
LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_source_admission_is_exact_v1(text)
RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_credential_admission_is_exact_v1(text)
RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.admit_qualification_input_source_receipt_v1(
  text, bytea, jsonb, text, bytea, jsonb
) RETURNS SETOF %s.qualification_input_source_receipt_admissions
LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.admit_qualification_input_credential_receipt_v1(
  text, bytea, jsonb, text, bytea, jsonb
) RETURNS SETOF %s.qualification_input_credential_receipt_admissions
LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.inspect_qualification_input_source_receipt_v1(text)
RETURNS SETOF %s.qualification_input_source_receipt_admissions
LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.inspect_qualification_input_credential_receipt_v1(text)
RETURNS SETOF %s.qualification_input_credential_receipt_admissions
LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.resolve_qualification_input_source_receipt_admission_v1(text)
RETURNS SETOF %s.qualification_input_source_receipt_admissions
LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.resolve_qualification_input_credential_receipt_admission_v1(text)
RETURNS SETOF %s.qualification_input_credential_receipt_admissions
LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_precommit_plan_is_exact_v1(uuid)
RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_precommit_authority_record_is_exact_v1(uuid)
RETURNS boolean LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN false; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.issue_qualification_input_precommit_v1(
  uuid, uuid, uuid, uuid, uuid,
  text, bytea, jsonb, text, bytea, jsonb,
  text, bytea, jsonb, text, bytea, jsonb
) RETURNS SETOF %s.qualification_input_precommit_authorities
LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.inspect_qualification_input_precommit_operation_v1(uuid)
RETURNS SETOF %s.qualification_input_precommit_authorities
LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.resolve_qualification_input_precommit_authority_v1(uuid)
RETURNS SETOF %s.qualification_input_precommit_authorities
LANGUAGE plpgsql STABLE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.resolve_qualification_input_precommit_for_promotion_v1(uuid, uuid)
RETURNS SETOF %s.qualification_input_precommit_authorities
LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.enforce_qualification_input_source_admission_closure_v1()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.enforce_qualification_input_credential_admission_closure_v1()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.enforce_qualification_input_precommit_authority_closure_v1()
RETURNS trigger LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY DEFINER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN NULL; END $$`, schemaName, schemaName),
		fmt.Sprintf(`CREATE FUNCTION %s.qualification_input_precommit_apply_security_v1()
RETURNS void LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
SET search_path TO pg_catalog, %s AS $$ BEGIN RETURN; END $$`, schemaName, schemaName),
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
