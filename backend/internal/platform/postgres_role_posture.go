package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/config"
)

const (
	postgresApplicationRole                                 = "worksflow_application"
	postgresMigrationOwnerRole                              = "worksflow_migration_owner"
	postgresRepositoryIndexGCOperatorRole                   = "worksflow_repository_index_gc_operator"
	postgresGoldenFaultOperatorRole                         = "worksflow_golden_fault_operator"
	postgresQualificationPromotionOperatorRole              = "worksflow_qualification_promotion_operator"
	postgresQualificationPolicyOperatorRole                 = "worksflow_qualification_policy_operator"
	postgresQualificationInputPrecommitOperatorRole         = "worksflow_qualification_input_precommit_operator"
	postgresQualificationSourceVerifierOperatorRole         = "worksflow_qualification_source_verifier_operator"
	postgresQualificationCredentialResolverOperatorRole     = "worksflow_qualification_credential_resolver_operator"
	postgresQualificationHandoffOperatorRole                = "worksflow_qualification_handoff_operator"
	postgresExpectedRepositoryGCFunctions                   = 4
	postgresExpectedApplicationFunctions                    = 15
	postgresExpectedWorkflowInputApplicationFunctions       = 3
	postgresExpectedRepositoryIndexTables                   = 4
	postgresExpectedRepositoryGCPrivateTables               = 6
	postgresExpectedGoldenFaultTables                       = 2
	postgresExpectedQualificationPromotionTables            = 8
	postgresExpectedQualificationPromotionFunctions         = 16
	postgresExpectedQualificationPromotionTriggers          = 7
	postgresExpectedQualificationPromotionNamedTables       = 12
	postgresExpectedQualificationPromotionNamedFunctions    = 19
	postgresExpectedQualificationPromotionNamedTriggers     = 8
	postgresExpectedQualificationHandoffTables              = 4
	postgresExpectedQualificationHandoffIndexes             = 19
	postgresExpectedQualificationHandoffFunctions           = 10
	postgresExpectedQualificationHandoffTriggers            = 17
	postgresExpectedQualificationHandoffSecurityDefiners    = 5
	postgresExpectedQualificationPolicyFunctions            = 4
	postgresExpectedQualificationInputTables                = 8
	postgresExpectedQualificationInputIndexes               = 28
	postgresExpectedQualificationInputTriggers              = 11
	postgresExpectedQualificationInputFunctions             = 24
	postgresExpectedQualificationInputSecurityDefiners      = 18
	postgresExpectedWorkflowInputAuthorityTables            = 10
	postgresExpectedWorkflowInputAuthorityTriggers          = 23
	postgresExpectedWorkflowExecutionProfileV3Triggers      = 5
	postgresExpectedWorkflowExecutionProfileV3HashContracts = 5
	postgresExpectedWorkflowAuthorityTriggerFunctions       = 10
	postgresExpectedWorkflowSharedRelationTriggers          = 15
	postgresExpectedWorkflowSharedLegacyTriggers            = 3
	postgresExpectedCredentialSetTables                     = 4
	postgresExpectedCredentialSetFunctions                  = 4
	postgresExpectedCredentialSetTriggers                   = 3
	postgresExpectedQualificationEvidenceTables             = 4
	postgresExpectedQualificationEvidenceFunctions          = 4
	postgresExpectedQualificationEvidenceTriggers           = 3
	postgresExpectedQualificationEvidenceNamedFunctions     = 6
	postgresExpectedQualificationEvidenceTotalTriggers      = 5
	postgresExpectedQualificationPlanTables                 = 2
	postgresExpectedQualificationPlanIndexes                = 8
	postgresExpectedQualificationPlanFunctions              = 5
	postgresExpectedQualificationPlanTriggers               = 3
	postgresExpectedQualificationReceiptV3Tables            = 3
	postgresExpectedQualificationReceiptV3Indexes           = 14
	postgresExpectedQualificationReceiptV3Functions         = 7
	postgresExpectedQualificationReceiptV3Definers          = 3
	postgresExpectedQualificationReceiptV3Triggers          = 5
	postgresExpectedCanonicalReviewTables                   = 1
	postgresExpectedCanonicalReviewIndexes                  = 6
	postgresExpectedCanonicalReviewFunctions                = 12
	postgresExpectedCanonicalReviewDefiners                 = 4
	postgresExpectedCanonicalReviewTriggers                 = 4
	postgresExpectedModelGovernanceFunctions                = 6
	postgresExpectedProtectedTables                         = 50
	postgresExpectedOwnedBoundaryTables                     = 49
	postgresExpectedOwnedBoundaryIndexes                    = 157
	postgresExpectedOwnedBoundaryRoutines                   = 115
	postgresExpectedInternalFunctions                       = 36
	postgresExpectedSandboxCheckpointHelpers                = 1
	postgresExpectedSecurityDefinerFunctions                = 81
)

var ErrUnsafePostgresAPIRolePosture = errors.New("unsafe PostgreSQL API role posture")

// postgresRolePostureQuery deliberately takes one catalog snapshot. Splitting
// these checks across queries would permit a grant, ownership, or role change
// to race startup approval.
const postgresRolePostureQuery = `
WITH RECURSIVE
current_role_facts AS (
  SELECT
    count(*)::integer AS role_count,
    coalesce(min(role.rolname), '') AS role_name,
    session_user::text AS session_role_name,
    coalesce(bool_or(role.rolsuper), false) AS is_superuser,
    coalesce(bool_or(role.rolbypassrls), false) AS bypasses_rls,
    coalesce(bool_or(role.rolcreaterole), false) AS can_create_role,
    coalesce(bool_or(role.rolcreatedb), false) AS can_create_database,
    coalesce(bool_or(role.rolreplication), false) AS can_replicate
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname = current_user
),
session_reachable_roles(role_oid) AS (
  SELECT role.oid
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname = session_user
  UNION
  SELECT membership.roleid
  FROM session_reachable_roles AS reachable
  JOIN pg_catalog.pg_auth_members AS membership
    ON membership.member = reachable.role_oid
  WHERE membership.inherit_option OR membership.set_option
),
reachable_role_facts AS (
  SELECT
    count(*)::integer AS role_count,
    coalesce(bool_or(
      role.rolsuper OR role.rolbypassrls OR role.rolcreaterole OR
      role.rolcreatedb OR role.rolreplication
    ), false) AS has_cluster_authority,
    coalesce(bool_or(role.rolname = '` + postgresApplicationRole + `'), false)
      AS application_is_reachable,
    coalesce(bool_or(role.rolname IN (
      '` + postgresMigrationOwnerRole + `',
      '` + postgresRepositoryIndexGCOperatorRole + `',
	      '` + postgresGoldenFaultOperatorRole + `',
	      '` + postgresQualificationPromotionOperatorRole + `',
	      '` + postgresQualificationPolicyOperatorRole + `',
	      '` + postgresQualificationInputPrecommitOperatorRole + `',
	      '` + postgresQualificationSourceVerifierOperatorRole + `',
	      '` + postgresQualificationCredentialResolverOperatorRole + `'
	      ,'` + postgresQualificationHandoffOperatorRole + `'
    )), false) AS forbidden_stable_role_is_reachable,
    EXISTS (
      SELECT 1
      FROM pg_catalog.pg_auth_members AS membership
      JOIN session_reachable_roles AS member_role
        ON member_role.role_oid = membership.member
      WHERE membership.admin_option
    ) AS has_role_admin_option
  FROM pg_catalog.pg_roles AS role
  JOIN session_reachable_roles AS reachable ON reachable.role_oid = role.oid
),
database_facts AS (
  SELECT
    count(*)::integer AS database_count,
    coalesce(bool_or(
      EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = database.datdba
      )
    ), false) AS owns_or_inherits_owner,
    coalesce(bool_or(
      EXISTS (
        SELECT 1
        FROM session_reachable_roles AS reachable
        WHERE pg_catalog.has_database_privilege(
          reachable.role_oid, database.oid, 'CREATE'
        )
      )
    ), false) AS can_create
  FROM pg_catalog.pg_database AS database
  WHERE database.datname = pg_catalog.current_database()
),
schema_facts AS (
  SELECT
    count(*)::integer AS schema_count,
    coalesce(min(namespace.nspname), '') AS schema_name,
    min(namespace.oid) AS schema_oid,
    coalesce(bool_or(
      pg_catalog.has_schema_privilege(current_user, namespace.oid, 'USAGE')
    ), false) AS api_has_usage,
    coalesce(bool_or(
      EXISTS (
        SELECT 1
        FROM session_reachable_roles AS reachable
        WHERE pg_catalog.has_schema_privilege(
          reachable.role_oid, namespace.oid, 'CREATE'
        )
      )
    ), false) AS api_can_create
  FROM pg_catalog.pg_namespace AS namespace
  WHERE namespace.nspname = pg_catalog.current_schema()
),
stable_role_facts AS (
  SELECT
    count(role.oid)::integer AS role_count,
    coalesce(bool_or(
	      role.rolcanlogin OR role.rolsuper OR role.rolbypassrls OR
	      role.rolcreaterole OR role.rolcreatedb OR role.rolreplication
	      OR EXISTS (
	        SELECT 1
	        FROM pg_catalog.pg_auth_members AS membership
	        WHERE membership.member = role.oid
	      )
	      OR EXISTS (
	        SELECT 1
	        FROM pg_catalog.pg_auth_members AS membership
	        WHERE membership.roleid = role.oid
	          AND membership.admin_option
	      )
	      OR EXISTS (
        SELECT 1
        FROM pg_catalog.pg_roles AS elevated_role
        WHERE (
          elevated_role.rolsuper OR elevated_role.rolbypassrls OR
          elevated_role.rolcreaterole OR elevated_role.rolcreatedb OR
          elevated_role.rolreplication
        )
          AND pg_catalog.pg_has_role(role.oid, elevated_role.oid, 'MEMBER')
      )
      OR EXISTS (
        SELECT 1
        FROM pg_catalog.pg_roles AS peer_role
        WHERE peer_role.rolname IN (
          '` + postgresApplicationRole + `',
          '` + postgresMigrationOwnerRole + `',
          '` + postgresRepositoryIndexGCOperatorRole + `',
	          '` + postgresGoldenFaultOperatorRole + `',
	          '` + postgresQualificationPromotionOperatorRole + `',
	          '` + postgresQualificationPolicyOperatorRole + `',
	          '` + postgresQualificationInputPrecommitOperatorRole + `',
	          '` + postgresQualificationSourceVerifierOperatorRole + `',
	          '` + postgresQualificationCredentialResolverOperatorRole + `'
	          ,'` + postgresQualificationHandoffOperatorRole + `'
        )
          AND peer_role.oid <> role.oid
          AND pg_catalog.pg_has_role(role.oid, peer_role.oid, 'MEMBER')
      )
    ), false) AS has_unsafe_role,
    coalesce(bool_or(
      expected.role_name = '` + postgresApplicationRole + `'
      AND EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = role.oid
      )
    ), false) AS api_is_application_member,
    coalesce(bool_or(
      expected.role_name = '` + postgresMigrationOwnerRole + `'
      AND EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = role.oid
      )
    ), false) AS api_is_migration_owner_member,
    coalesce(bool_or(
      expected.role_name = '` + postgresRepositoryIndexGCOperatorRole + `'
      AND EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = role.oid
      )
    ), false) AS api_is_gc_operator_member,
    coalesce(bool_or(
      expected.role_name = '` + postgresGoldenFaultOperatorRole + `'
      AND EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = role.oid
      )
    ), false) AS api_is_golden_fault_operator_member,
    coalesce(bool_or(
      expected.role_name = '` + postgresQualificationPromotionOperatorRole + `'
      AND EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = role.oid
      )
	    ), false) AS api_is_qualification_promotion_operator_member,
	    coalesce(bool_or(
	      expected.role_name = '` + postgresQualificationPolicyOperatorRole + `'
	      AND EXISTS (
	        SELECT 1 FROM session_reachable_roles AS reachable
	        WHERE reachable.role_oid = role.oid
	      )
	    ), false) AS api_is_qualification_policy_operator_member,
	    coalesce(bool_or(
	      expected.role_name = '` + postgresQualificationInputPrecommitOperatorRole + `'
	      AND EXISTS (
	        SELECT 1 FROM session_reachable_roles AS reachable
	        WHERE reachable.role_oid = role.oid
	      )
	    ), false) AS api_is_qualification_input_precommit_operator_member,
	    coalesce(bool_or(
	      expected.role_name = '` + postgresQualificationSourceVerifierOperatorRole + `'
	      AND EXISTS (
	        SELECT 1 FROM session_reachable_roles AS reachable
	        WHERE reachable.role_oid = role.oid
	      )
	    ), false) AS api_is_qualification_source_verifier_operator_member,
	    coalesce(bool_or(
	      expected.role_name = '` + postgresQualificationCredentialResolverOperatorRole + `'
	      AND EXISTS (
	        SELECT 1 FROM session_reachable_roles AS reachable
	        WHERE reachable.role_oid = role.oid
	      )
	    ), false) AS api_is_qualification_credential_resolver_operator_member,
	    coalesce(bool_or(
	      expected.role_name = '` + postgresQualificationHandoffOperatorRole + `'
	      AND EXISTS (
	        SELECT 1 FROM session_reachable_roles AS reachable
	        WHERE reachable.role_oid = role.oid
	      )
	    ), false) AS api_is_qualification_handoff_operator_member,
    min(role.oid) FILTER (
      WHERE expected.role_name = '` + postgresApplicationRole + `'
    ) AS application_oid,
    min(role.oid) FILTER (
      WHERE expected.role_name = '` + postgresMigrationOwnerRole + `'
    ) AS migration_owner_oid,
    min(role.oid) FILTER (
      WHERE expected.role_name = '` + postgresRepositoryIndexGCOperatorRole + `'
    ) AS operator_oid,
    min(role.oid) FILTER (
      WHERE expected.role_name = '` + postgresGoldenFaultOperatorRole + `'
    ) AS fault_operator_oid,
	    min(role.oid) FILTER (
	      WHERE expected.role_name = '` + postgresQualificationPromotionOperatorRole + `'
	    ) AS qualification_operator_oid,
	    min(role.oid) FILTER (
	      WHERE expected.role_name = '` + postgresQualificationPolicyOperatorRole + `'
	    ) AS qualification_policy_operator_oid,
	    min(role.oid) FILTER (
	      WHERE expected.role_name = '` + postgresQualificationInputPrecommitOperatorRole + `'
	    ) AS qualification_input_precommit_operator_oid,
	    min(role.oid) FILTER (
	      WHERE expected.role_name = '` + postgresQualificationSourceVerifierOperatorRole + `'
	    ) AS qualification_source_verifier_operator_oid,
	    min(role.oid) FILTER (
	      WHERE expected.role_name = '` + postgresQualificationCredentialResolverOperatorRole + `'
	    ) AS qualification_credential_resolver_operator_oid,
	    min(role.oid) FILTER (
	      WHERE expected.role_name = '` + postgresQualificationHandoffOperatorRole + `'
	    ) AS qualification_handoff_operator_oid
  FROM (VALUES
    ('` + postgresApplicationRole + `'::text),
    ('` + postgresMigrationOwnerRole + `'::text),
    ('` + postgresRepositoryIndexGCOperatorRole + `'::text),
    ('` + postgresGoldenFaultOperatorRole + `'::text),
	    ('` + postgresQualificationPromotionOperatorRole + `'::text),
	    ('` + postgresQualificationPolicyOperatorRole + `'::text),
	    ('` + postgresQualificationInputPrecommitOperatorRole + `'::text),
	    ('` + postgresQualificationSourceVerifierOperatorRole + `'::text),
	    ('` + postgresQualificationCredentialResolverOperatorRole + `'::text),
	    ('` + postgresQualificationHandoffOperatorRole + `'::text)
  ) AS expected(role_name)
  LEFT JOIN pg_catalog.pg_roles AS role ON role.rolname = expected.role_name
),
schema_acl_facts AS (
  SELECT
    stable.application_oid IS NOT NULL
      AND schema_state.schema_oid IS NOT NULL
      AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_namespace AS namespace
        CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
          namespace.nspacl,
          pg_catalog.acldefault('n', namespace.nspowner)
        )) AS schema_acl
        WHERE namespace.oid = schema_state.schema_oid
          AND schema_acl.grantee = stable.application_oid
          AND schema_acl.privilege_type = 'USAGE'
          AND NOT schema_acl.is_grantable
      ) AS application_has_direct_usage,
    stable.application_oid IS NOT NULL
      AND schema_state.schema_oid IS NOT NULL
      AND pg_catalog.has_schema_privilege(
        stable.application_oid, schema_state.schema_oid, 'CREATE'
      ) AS application_can_create,
    stable.fault_operator_oid IS NOT NULL
      AND schema_state.schema_oid IS NOT NULL
      AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_namespace AS namespace
        CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
          namespace.nspacl,
          pg_catalog.acldefault('n', namespace.nspowner)
        )) AS schema_acl
        WHERE namespace.oid = schema_state.schema_oid
          AND schema_acl.grantee = stable.fault_operator_oid
          AND schema_acl.privilege_type = 'USAGE'
          AND NOT schema_acl.is_grantable
      ) AS fault_operator_has_direct_usage,
    stable.fault_operator_oid IS NOT NULL
      AND schema_state.schema_oid IS NOT NULL
      AND pg_catalog.has_schema_privilege(
        stable.fault_operator_oid, schema_state.schema_oid, 'CREATE'
      ) AS fault_operator_can_create,
    stable.qualification_operator_oid IS NOT NULL
      AND schema_state.schema_oid IS NOT NULL
      AND EXISTS (
        SELECT 1
        FROM pg_catalog.pg_namespace AS namespace
        CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
          namespace.nspacl,
          pg_catalog.acldefault('n', namespace.nspowner)
        )) AS schema_acl
        WHERE namespace.oid = schema_state.schema_oid
          AND schema_acl.grantee = stable.qualification_operator_oid
          AND schema_acl.privilege_type = 'USAGE'
          AND NOT schema_acl.is_grantable
      ) AS qualification_operator_has_direct_usage,
	    stable.qualification_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_operator_oid, schema_state.schema_oid, 'CREATE'
	      ) AS qualification_operator_can_create,
	    stable.qualification_policy_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND EXISTS (
	        SELECT 1
	        FROM pg_catalog.pg_namespace AS namespace
	        CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
	          namespace.nspacl,
	          pg_catalog.acldefault('n', namespace.nspowner)
	        )) AS schema_acl
	        WHERE namespace.oid = schema_state.schema_oid
	          AND schema_acl.grantee = stable.qualification_policy_operator_oid
	          AND schema_acl.privilege_type = 'USAGE'
	          AND NOT schema_acl.is_grantable
	      ) AS qualification_policy_operator_has_direct_usage,
	    stable.qualification_policy_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_policy_operator_oid, schema_state.schema_oid, 'CREATE'
	      ) AS qualification_policy_operator_can_create,
	    stable.qualification_input_precommit_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_input_precommit_operator_oid,
	        schema_state.schema_oid, 'USAGE'
	      ) AS qualification_input_precommit_operator_has_usage,
	    stable.qualification_input_precommit_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_input_precommit_operator_oid,
	        schema_state.schema_oid, 'CREATE'
	      ) AS qualification_input_precommit_operator_can_create,
	    stable.qualification_source_verifier_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_source_verifier_operator_oid,
	        schema_state.schema_oid, 'USAGE'
	      ) AS qualification_source_verifier_operator_has_usage,
	    stable.qualification_source_verifier_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_source_verifier_operator_oid,
	        schema_state.schema_oid, 'CREATE'
	      ) AS qualification_source_verifier_operator_can_create,
	    stable.qualification_credential_resolver_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_credential_resolver_operator_oid,
	        schema_state.schema_oid, 'USAGE'
	      ) AS qualification_credential_resolver_operator_has_usage,
	    stable.qualification_credential_resolver_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_credential_resolver_operator_oid,
	        schema_state.schema_oid, 'CREATE'
	      ) AS qualification_credential_resolver_operator_can_create,
	    stable.qualification_handoff_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_handoff_operator_oid,
	        schema_state.schema_oid, 'USAGE'
	      ) AS qualification_handoff_operator_has_usage,
	    stable.qualification_handoff_operator_oid IS NOT NULL
	      AND schema_state.schema_oid IS NOT NULL
	      AND pg_catalog.has_schema_privilege(
	        stable.qualification_handoff_operator_oid,
	        schema_state.schema_oid, 'CREATE'
	      ) AS qualification_handoff_operator_can_create
  FROM stable_role_facts AS stable
  CROSS JOIN schema_facts AS schema_state
),
schema_object_owner_facts AS (
  SELECT count(*)::integer AS owned_object_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_namespace AS namespace
    ON namespace.oid = schema_state.schema_oid
  CROSS JOIN LATERAL (
    SELECT relation.oid
    FROM pg_catalog.pg_class AS relation
    WHERE relation.relnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = relation.relowner)
    UNION ALL
    SELECT routine.oid
    FROM pg_catalog.pg_proc AS routine
    WHERE routine.pronamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = routine.proowner)
    UNION ALL
    SELECT catalog_type.oid
    FROM pg_catalog.pg_type AS catalog_type
    WHERE catalog_type.typnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = catalog_type.typowner)
    UNION ALL
    SELECT catalog_collation.oid
    FROM pg_catalog.pg_collation AS catalog_collation
    WHERE catalog_collation.collnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = catalog_collation.collowner)
    UNION ALL
    SELECT catalog_conversion.oid
    FROM pg_catalog.pg_conversion AS catalog_conversion
    WHERE catalog_conversion.connamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = catalog_conversion.conowner)
    UNION ALL
    SELECT catalog_operator.oid
    FROM pg_catalog.pg_operator AS catalog_operator
    WHERE catalog_operator.oprnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = catalog_operator.oprowner)
    UNION ALL
    SELECT operator_class.oid
    FROM pg_catalog.pg_opclass AS operator_class
    WHERE operator_class.opcnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = operator_class.opcowner)
    UNION ALL
    SELECT operator_family.oid
    FROM pg_catalog.pg_opfamily AS operator_family
    WHERE operator_family.opfnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = operator_family.opfowner)
    UNION ALL
    SELECT catalog_configuration.oid
    FROM pg_catalog.pg_ts_config AS catalog_configuration
    WHERE catalog_configuration.cfgnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = catalog_configuration.cfgowner)
    UNION ALL
    SELECT catalog_dictionary.oid
    FROM pg_catalog.pg_ts_dict AS catalog_dictionary
    WHERE catalog_dictionary.dictnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = catalog_dictionary.dictowner)
    UNION ALL
    SELECT statistic.oid
    FROM pg_catalog.pg_statistic_ext AS statistic
    WHERE statistic.stxnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = statistic.stxowner)
    UNION ALL
    SELECT catalog_extension.oid
    FROM pg_catalog.pg_extension AS catalog_extension
    WHERE catalog_extension.extnamespace = namespace.oid
      AND EXISTS (SELECT 1 FROM session_reachable_roles AS reachable WHERE reachable.role_oid = catalog_extension.extowner)
  ) AS owned_object
),
expected_index_tables(table_name, can_select, can_insert, can_update) AS (
  VALUES
    ('repository_exact_tree_literal_index_blobs'::text, true, true, false),
    ('repository_exact_tree_literal_index_members'::text, true, true, false),
    ('repository_exact_tree_literal_index_manifests'::text, true, true, true),
    ('repository_exact_tree_literal_index_build_claims'::text, true, false, false)
),
index_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    coalesce(bool_or(
      relation.oid IS NOT NULL
      AND pg_catalog.pg_has_role(current_user, relation.relowner, 'MEMBER')
    ), false) AS api_owns_or_inherits_owner,
    count(relation.oid) FILTER (
      WHERE stable.application_oid IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee = stable.application_oid
          GROUP BY table_acl.grantee
          HAVING bool_or(table_acl.privilege_type = 'SELECT') = expected.can_select
             AND bool_or(table_acl.privilege_type = 'INSERT') = expected.can_insert
             AND bool_or(table_acl.privilege_type = 'UPDATE') = expected.can_update
             AND NOT bool_or(table_acl.privilege_type IN (
               'DELETE', 'TRUNCATE', 'REFERENCES', 'TRIGGER'
             ))
        )
    )::integer AS application_exact_direct_acl_count,
    count(relation.oid) FILTER (
      WHERE pg_catalog.has_table_privilege(current_user, relation.oid, 'SELECT') = expected.can_select
        AND pg_catalog.has_table_privilege(current_user, relation.oid, 'INSERT') = expected.can_insert
        AND pg_catalog.has_table_privilege(current_user, relation.oid, 'UPDATE') = expected.can_update
        AND NOT pg_catalog.has_table_privilege(current_user, relation.oid, 'DELETE')
        AND NOT pg_catalog.has_table_privilege(current_user, relation.oid, 'TRUNCATE')
        AND NOT pg_catalog.has_table_privilege(current_user, relation.oid, 'REFERENCES')
        AND NOT pg_catalog.has_table_privilege(current_user, relation.oid, 'TRIGGER')
    )::integer AS api_exact_acl_count,
    count(relation.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          relation.relacl,
          pg_catalog.acldefault('r', relation.relowner)
        )) AS table_acl
        WHERE table_acl.grantee = 0
          AND table_acl.privilege_type IN (
            'SELECT', 'INSERT', 'UPDATE', 'DELETE', 'TRUNCATE',
            'REFERENCES', 'TRIGGER'
          )
      )
    )::integer AS public_privileged_table_count
  FROM expected_index_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
expected_gc_private_tables(table_name) AS (
  VALUES
    ('repository_exact_tree_literal_index_gc_runs'::text),
    ('repository_exact_tree_literal_index_gc_capabilities'::text),
    ('repository_exact_tree_literal_index_gc_receipts'::text),
    ('repository_exact_tree_literal_index_gc_tombstones'::text),
    ('repository_exact_tree_literal_index_gc_tree_delete_auth'::text),
    ('repository_exact_tree_literal_index_gc_blob_delete_auth'::text)
),
gc_private_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE pg_catalog.has_table_privilege(current_user, relation.oid, 'SELECT')
         OR pg_catalog.has_table_privilege(current_user, relation.oid, 'INSERT')
         OR pg_catalog.has_table_privilege(current_user, relation.oid, 'UPDATE')
         OR pg_catalog.has_table_privilege(current_user, relation.oid, 'DELETE')
         OR pg_catalog.has_table_privilege(current_user, relation.oid, 'TRUNCATE')
         OR pg_catalog.has_table_privilege(current_user, relation.oid, 'REFERENCES')
         OR pg_catalog.has_table_privilege(current_user, relation.oid, 'TRIGGER')
    )::integer AS api_privileged_table_count
  FROM expected_gc_private_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
    AND relation.relkind IN ('r', 'p')
),
expected_golden_fault_tables(table_name) AS (
  VALUES
    ('golden_fault_consume_reservations'::text),
    ('golden_fault_consume_results'::text)
),
golden_fault_table_acl_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.fault_operator_oid IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee = stable.fault_operator_oid
            AND table_acl.privilege_type = 'SELECT'
            AND NOT table_acl.is_grantable
        )
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee = stable.fault_operator_oid
            AND table_acl.privilege_type = 'INSERT'
            AND NOT table_acl.is_grantable
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee NOT IN (
            relation.relowner, stable.fault_operator_oid
          )
             OR (
               table_acl.grantee = stable.fault_operator_oid
               AND (
                 table_acl.privilege_type NOT IN ('SELECT', 'INSERT')
                 OR table_acl.is_grantable
               )
             )
        )
    )::integer AS exact_fault_operator_acl_count
  FROM expected_golden_fault_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
expected_qualification_promotion_tables(table_name) AS (
  VALUES
    ('qualification_promotion_consumptions'::text),
    ('qualification_promotion_handoffs'::text),
    ('artifact_revision_identity_reservations'::text),
    ('qualification_promotion_v2_independent_receipts'::text),
    ('qualification_promotion_v2_consumptions'::text),
    ('qualification_promotion_v2_consumption_independent_receipts'::text),
    ('qualification_promotion_v2_handoffs'::text),
    ('qualification_promotion_v2_identity_reservations'::text)
),
qualification_promotion_table_acl_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND relation.relpersistence = 'p'
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> relation.relowner
        )
    )::integer AS exact_owner_only_count
  FROM expected_qualification_promotion_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
qualification_promotion_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
   AND (
     relation.relname IN (
       'qualification_promotion_consumptions',
       'qualification_promotion_handoffs',
       'artifact_revision_identity_reservations'
     )
     OR relation.relname LIKE 'qualification\_promotion\_v2\_%' ESCAPE '\'
   )
),
qualification_promotion_unexpected_table_acl_facts AS (
  SELECT count(*)::integer AS unexpected_operator_acl_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  JOIN pg_catalog.pg_class AS candidate
    ON candidate.relnamespace = schema_state.schema_oid
   AND candidate.relkind IN ('r', 'p', 'S', 'v', 'm', 'f')
  CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
    candidate.relacl,
    pg_catalog.acldefault('r', candidate.relowner)
  )) AS candidate_acl
  WHERE candidate_acl.grantee = stable.qualification_operator_oid
),
expected_qualification_promotion_triggers(
  table_name, trigger_name, function_name, trigger_type
) AS (
  VALUES
    ('artifact_revisions'::text,
     'artifact_revisions_shared_identity_reservation'::text,
     'reserve_ordinary_artifact_revision_identity_v1'::text, 7::smallint),
    ('artifact_revision_identity_reservations'::text,
     'artifact_revision_identity_reservations_immutable'::text,
     'reject_qualification_promotion_v2_mutation'::text, 58::smallint),
    ('qualification_promotion_v2_independent_receipts'::text,
     'qualification_promotion_v2_independent_receipts_immutable'::text,
     'reject_qualification_promotion_v2_mutation'::text, 58::smallint),
    ('qualification_promotion_v2_consumptions'::text,
     'qualification_promotion_v2_consumptions_immutable'::text,
     'reject_qualification_promotion_v2_mutation'::text, 58::smallint),
    ('qualification_promotion_v2_consumption_independent_receipts'::text,
     'qualification_promotion_v2_consumption_independent_receipts_imm'::text,
     'reject_qualification_promotion_v2_mutation'::text, 58::smallint),
    ('qualification_promotion_v2_handoffs'::text,
     'qualification_promotion_v2_handoffs_immutable'::text,
     'reject_qualification_promotion_v2_mutation'::text, 58::smallint),
    ('qualification_promotion_v2_identity_reservations'::text,
     'qualification_promotion_v2_identity_reservations_immutable'::text,
     'reject_qualification_promotion_v2_mutation'::text, 58::smallint)
),
qualification_promotion_trigger_facts AS (
  SELECT
    count(trigger.oid)::integer AS trigger_count,
    count(trigger.oid) FILTER (
      WHERE trigger.tgtype = expected.trigger_type
        AND trigger.tgenabled = 'O'
        AND NOT trigger.tgisinternal
        AND trigger.tgqual IS NULL
        AND pg_catalog.cardinality(trigger.tgattr::smallint[]) = 0
        AND trigger.tgnargs = 0
        AND trigger.tgargs = ''::bytea
        AND trigger.tgconstraint = 0::pg_catalog.oid
        AND trigger.tgconstrrelid = 0::pg_catalog.oid
        AND NOT trigger.tgdeferrable
        AND NOT trigger.tginitdeferred
        AND trigger.tgoldtable IS NULL
        AND trigger.tgnewtable IS NULL
        AND trigger.tgparentid = 0::pg_catalog.oid
        AND routine.pronamespace = schema_state.schema_oid
        AND routine.proname = expected.function_name
        AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
    )::integer AS exact_trigger_count
  FROM expected_qualification_promotion_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
qualification_promotion_named_trigger_facts AS (
  SELECT count(*)::integer AS named_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
   AND (
     relation.relname IN (
       'artifact_revision_identity_reservations',
       'qualification_promotion_v2_independent_receipts',
       'qualification_promotion_v2_consumptions',
       'qualification_promotion_v2_consumption_independent_receipts',
       'qualification_promotion_v2_handoffs',
       'qualification_promotion_v2_identity_reservations'
     )
     OR trigger.tgname = 'artifact_revisions_shared_identity_reservation'
   )
),
expected_qualification_handoff_tables(table_name) AS (
  VALUES
    ('qualification_promotion_v2_revision_transaction_grants'::text),
    ('qualification_promotion_v2_revision_authority_bindings'::text),
    ('qualification_promotion_v2_handoff_lineage_members'::text),
    ('qualification_promotion_v2_handoff_completions'::text)
),
expected_qualification_handoff_table_columns(
  table_name, ordinal_position, column_name, data_type,
  has_default_collation
) AS (
  VALUES
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     1::smallint, 'output_revision_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     2::smallint, 'handoff_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     3::smallint, 'operation_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     4::smallint, 'backend_pid'::text, 'integer'::text, false),
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     5::smallint, 'transaction_id'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     6::smallint, 'granted_at'::text, 'timestamp with time zone'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     1::smallint, 'handoff_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     2::smallint, 'operation_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     3::smallint, 'output_revision_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     4::smallint, 'workflow_input_authority_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     5::smallint, 'workflow_input_authority_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     6::smallint, 'plan_authority_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     7::smallint, 'plan_authority_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     8::smallint, 'receipt_id'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     9::smallint, 'receipt_envelope_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     10::smallint, 'promotion_request_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     11::smallint, 'promotion_closure_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     12::smallint, 'promotion_revision_intent_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     13::smallint, 'promotion_consumption_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     14::smallint, 'target_document'::text, 'jsonb'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     15::smallint, 'authority_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     16::smallint, 'authority_bytes'::text, 'bytea'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     17::smallint, 'authority_document'::text, 'jsonb'::text, false),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     18::smallint, 'creation_transaction_id'::text, 'text'::text, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     19::smallint, 'created_at'::text, 'timestamp with time zone'::text, false),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     1::smallint, 'handoff_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     2::smallint, 'member_kind'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     3::smallint, 'member_ordinal'::text, 'bigint'::text, false),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     4::smallint, 'member_key'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     5::smallint, 'row_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     6::smallint, 'creation_transaction_id'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     1::smallint, 'handoff_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     2::smallint, 'operation_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     3::smallint, 'consumption_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     4::smallint, 'output_revision_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     5::smallint, 'output_revision_content_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     6::smallint, 'project_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     7::smallint, 'workflow_run_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     8::smallint, 'node_run_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     9::smallint, 'node_key'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     10::smallint, 'publish_node_run_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     11::smallint, 'publish_node_key'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     12::smallint, 'event_cursor_before'::text, 'bigint'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     13::smallint, 'event_cursor_after'::text, 'bigint'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     14::smallint, 'gate_output_document'::text, 'jsonb'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     15::smallint, 'gate_completed_event_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     16::smallint, 'publish_authorization_event_id'::text, 'uuid'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     17::smallint, 'completion_hash'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     18::smallint, 'completion_bytes'::text, 'bytea'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     19::smallint, 'completion_document'::text, 'jsonb'::text, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     20::smallint, 'creation_transaction_id'::text, 'text'::text, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     21::smallint, 'completed_at'::text, 'timestamp with time zone'::text, false)
),
expected_qualification_handoff_creation_xid_constraints(
  table_name, constraint_name
) AS (
  VALUES
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     'qualification_promotion_v2_revisi_creation_transaction_id_check'::text),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     'qualification_promotion_v2_handof_creation_transaction_id_check'::text),
    ('qualification_promotion_v2_handoff_completions'::text,
     'qualification_promotion_v2_hando_creation_transaction_id_check1'::text)
),
qualification_handoff_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND relation.relpersistence = 'p'
        AND (
          SELECT count(*)
          FROM pg_catalog.pg_attribute AS attribute
          WHERE attribute.attrelid = relation.oid
            AND attribute.attnum > 0
            AND NOT attribute.attisdropped
        ) = (
          SELECT count(*)
          FROM expected_qualification_handoff_table_columns AS expected_column
          WHERE expected_column.table_name = expected.table_name
        )
        AND NOT EXISTS (
          SELECT 1
          FROM expected_qualification_handoff_table_columns AS expected_column
          LEFT JOIN pg_catalog.pg_attribute AS attribute
            ON attribute.attrelid = relation.oid
           AND attribute.attnum = expected_column.ordinal_position
           AND NOT attribute.attisdropped
          LEFT JOIN pg_catalog.pg_attrdef AS column_default
            ON column_default.adrelid = relation.oid
           AND column_default.adnum = attribute.attnum
          WHERE expected_column.table_name = expected.table_name
            AND (
              attribute.attname IS DISTINCT FROM expected_column.column_name
              OR pg_catalog.format_type(attribute.atttypid, attribute.atttypmod)
                   IS DISTINCT FROM expected_column.data_type
              OR attribute.attnotnull IS DISTINCT FROM true
              OR column_default.oid IS NOT NULL
              OR (
                expected_column.has_default_collation
                AND attribute.attcollation IS DISTINCT FROM
                  'pg_catalog.default'::pg_catalog.regcollation
              )
              OR (
                NOT expected_column.has_default_collation
                AND attribute.attcollation <> 0::pg_catalog.oid
              )
            )
        )
        AND NOT EXISTS (
          SELECT 1
          FROM expected_qualification_handoff_creation_xid_constraints
            AS expected_constraint
          LEFT JOIN pg_catalog.pg_constraint AS constraint_binding
            ON constraint_binding.conrelid = relation.oid
           AND constraint_binding.conname = expected_constraint.constraint_name
          WHERE expected_constraint.table_name = expected.table_name
            AND (
              constraint_binding.oid IS NULL
              OR constraint_binding.contype <> 'c'
              OR constraint_binding.condeferrable
              OR constraint_binding.condeferred
              OR NOT constraint_binding.convalidated
              OR constraint_binding.connoinherit
              OR pg_catalog.pg_get_constraintdef(
                   constraint_binding.oid, false
                 ) <> 'CHECK ((creation_transaction_id ~ ''^[1-9][0-9]{0,19}$''::text))'
            )
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> relation.relowner
        )
    )::integer AS exact_owner_only_count
  FROM expected_qualification_handoff_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
expected_qualification_handoff_indexes(index_name, table_name) AS (
  VALUES
    ('artifact_revisions_ordinary_content_unique'::text,
     'artifact_revisions'::text),
    ('artifact_revisions_promotion_handoff_unique'::text,
     'artifact_revisions'::text),
    ('qualification_promotion_handoff_pending_dispatch_unique'::text,
     'outbox_events'::text),
    ('qualification_promotion_v2_ha_publish_authorization_event_i_key'::text,
     'qualification_promotion_v2_handoff_completions'::text),
    ('qualification_promotion_v2_handoff__gate_completed_event_id_key'::text,
     'qualification_promotion_v2_handoff_completions'::text),
    ('qualification_promotion_v2_handoff_compl_output_revision_id_key'::text,
     'qualification_promotion_v2_handoff_completions'::text),
    ('qualification_promotion_v2_handoff_complet_consumption_hash_key'::text,
     'qualification_promotion_v2_handoff_completions'::text),
    ('qualification_promotion_v2_handoff_completi_completion_hash_key'::text,
     'qualification_promotion_v2_handoff_completions'::text),
    ('qualification_promotion_v2_handoff_completions_operation_id_key'::text,
     'qualification_promotion_v2_handoff_completions'::text),
    ('qualification_promotion_v2_handoff_completions_pkey'::text,
     'qualification_promotion_v2_handoff_completions'::text),
    ('qualification_promotion_v2_revision_auth_output_revision_id_key'::text,
     'qualification_promotion_v2_revision_authority_bindings'::text),
    ('qualification_promotion_v2_revision_authorit_authority_hash_key'::text,
     'qualification_promotion_v2_revision_authority_bindings'::text),
    ('qualification_promotion_v2_revision_authority__operation_id_key'::text,
     'qualification_promotion_v2_revision_authority_bindings'::text),
    ('qualification_promotion_v2_revision_authority_bindings_pkey'::text,
     'qualification_promotion_v2_revision_authority_bindings'::text),
    ('qualification_handoff_lineage_members_ordinal_key'::text,
     'qualification_promotion_v2_handoff_lineage_members'::text),
    ('qualification_handoff_lineage_members_pkey'::text,
     'qualification_promotion_v2_handoff_lineage_members'::text),
    ('qualification_promotion_v2_revision_transactio_operation_id_key'::text,
     'qualification_promotion_v2_revision_transaction_grants'::text),
    ('qualification_promotion_v2_revision_transaction__handoff_id_key'::text,
     'qualification_promotion_v2_revision_transaction_grants'::text),
    ('qualification_promotion_v2_revision_transaction_grants_pkey'::text,
     'qualification_promotion_v2_revision_transaction_grants'::text)
),
qualification_handoff_index_facts AS (
  SELECT
    count(index_relation.oid)::integer AS index_count,
    count(index_relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND index_relation.relowner = stable.migration_owner_oid
        AND index_catalog.indisvalid
        AND index_catalog.indisready
        AND index_catalog.indislive
    )::integer AS exact_index_count
  FROM expected_qualification_handoff_indexes AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS table_relation
    ON table_relation.relnamespace = schema_state.schema_oid
   AND table_relation.relname = expected.table_name
   AND table_relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indrelid = table_relation.oid
  LEFT JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.oid = index_catalog.indexrelid
   AND index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relname = expected.index_name
   AND index_relation.relkind IN ('i', 'I')
),
qualification_handoff_named_index_facts AS (
  SELECT count(index_relation.oid)::integer AS named_index_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS table_relation
    ON table_relation.relnamespace = schema_state.schema_oid
   AND table_relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indrelid = table_relation.oid
  JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.oid = index_catalog.indexrelid
   AND index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relkind IN ('i', 'I')
  WHERE table_relation.relname IN (
      'qualification_promotion_v2_revision_transaction_grants',
      'qualification_promotion_v2_revision_authority_bindings',
      'qualification_promotion_v2_handoff_lineage_members',
      'qualification_promotion_v2_handoff_completions'
    )
    OR index_relation.relname IN (
      'artifact_revisions_ordinary_content_unique',
      'artifact_revisions_promotion_handoff_unique',
      'qualification_promotion_handoff_pending_dispatch_unique'
    )
),
expected_qualification_handoff_triggers(
  table_name, trigger_name, function_name, trigger_type,
  is_constraint, is_deferrable, is_initially_deferred
) AS (
  VALUES
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     'qualification_handoff_revision_authorities_immutable'::text,
     'reject_qualification_handoff_v1_mutation'::text,
     58::smallint, false, false, false),
    ('qualification_promotion_v2_handoff_completions'::text,
     'qualification_handoff_completions_immutable'::text,
     'reject_qualification_handoff_v1_mutation'::text,
     58::smallint, false, false, false),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     'qualification_handoff_lineage_members_immutable'::text,
     'reject_qualification_handoff_v1_mutation'::text,
     58::smallint, false, false, false),
    ('outbox_events'::text,
     'qualification_handoff_outbox_immutable'::text,
     'reject_qualification_handoff_v1_mutation'::text,
     27::smallint, false, false, false),
    ('qualification_promotion_v2_handoffs'::text,
     'qualification_promotion_v2_handoff_pending_dispatch'::text,
     'enqueue_qualification_promotion_v2_handoff_v1'::text,
     5::smallint, false, false, false),
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     'qualification_handoff_grant_empty_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true),
    ('qualification_promotion_v2_handoff_completions'::text,
     'qualification_handoff_completion_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     5::smallint, true, true, true),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     'qualification_handoff_revision_authority_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     5::smallint, true, true, true),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     'qualification_handoff_lineage_member_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     5::smallint, true, true, true),
    ('artifact_revisions'::text,
     'qualification_handoff_revision_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true),
    ('artifact_revision_sources'::text,
     'qualification_handoff_revision_source_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true),
    ('artifact_dependencies'::text,
     'qualification_handoff_dependency_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true),
    ('trace_links'::text,
     'qualification_handoff_trace_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true),
    ('workflow_run_events'::text,
     'qualification_handoff_event_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true),
    ('outbox_events'::text,
     'qualification_handoff_outbox_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     13::smallint, true, true, true),
    ('workflow_node_runs'::text,
     'qualification_handoff_node_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true),
    ('workflow_runs'::text,
     'qualification_handoff_run_exact_closure'::text,
     'validate_qualification_handoff_v1_closure'::text,
     29::smallint, true, true, true)
),
qualification_handoff_trigger_facts AS (
  SELECT
    count(trigger.oid)::integer AS trigger_count,
    count(trigger.oid) FILTER (
      WHERE trigger.tgtype = expected.trigger_type
        AND trigger.tgenabled = 'O'
        AND NOT trigger.tgisinternal
        AND trigger.tgqual IS NULL
        AND pg_catalog.cardinality(trigger.tgattr::smallint[]) = 0
        AND trigger.tgnargs = 0
        AND trigger.tgargs = ''::bytea
        AND trigger.tgconstrrelid = 0::pg_catalog.oid
        AND trigger.tgdeferrable = expected.is_deferrable
        AND trigger.tginitdeferred = expected.is_initially_deferred
        AND trigger.tgoldtable IS NULL
        AND trigger.tgnewtable IS NULL
        AND trigger.tgparentid = 0::pg_catalog.oid
        AND routine.pronamespace = schema_state.schema_oid
        AND routine.proname = expected.function_name
        AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
        AND (
          (NOT expected.is_constraint
            AND trigger.tgconstraint = 0::pg_catalog.oid)
          OR
          (expected.is_constraint
            AND trigger.tgconstraint <> 0::pg_catalog.oid
            AND (
              SELECT count(*) = 1
                AND bool_and(
                  constraint_binding.connamespace = schema_state.schema_oid
                  AND constraint_binding.conname = expected.trigger_name
                  AND constraint_binding.contype = 't'
                  AND constraint_binding.conrelid = relation.oid
                  AND constraint_binding.conindid = 0::pg_catalog.oid
                  AND constraint_binding.confrelid = 0::pg_catalog.oid
                  AND constraint_binding.condeferrable = expected.is_deferrable
                  AND constraint_binding.condeferred = expected.is_initially_deferred
                  AND constraint_binding.convalidated
                  AND constraint_binding.connoinherit
                )
              FROM pg_catalog.pg_constraint AS constraint_binding
              WHERE constraint_binding.oid = trigger.tgconstraint
            )
          )
        )
    )::integer AS exact_trigger_count
  FROM expected_qualification_handoff_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
qualification_handoff_named_trigger_facts AS (
  SELECT count(*)::integer AS named_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_trigger AS trigger
    ON NOT trigger.tgisinternal
   AND (
     trigger.tgname LIKE 'qualification\_handoff\_%' ESCAPE '\'
     OR trigger.tgname = 'qualification_promotion_v2_handoff_pending_dispatch'
   )
  JOIN pg_catalog.pg_class AS relation
    ON relation.oid = trigger.tgrelid
   AND relation.relnamespace = schema_state.schema_oid
),
expected_qualification_input_tables(table_name) AS (
  VALUES
    ('qualification_input_precommit_executable_binding_generations'::text),
	('qualification_input_precommit_executable_binding_heads'::text),
    ('qualification_input_source_receipt_admissions'::text),
    ('qualification_input_credential_receipt_admissions'::text),
    ('qualification_input_precommit_authorities'::text),
    ('qualification_input_precommit_identity_reservations'::text),
    ('qualification_input_precommit_wia_reservations'::text),
    ('qualification_input_precommit_plan_reservations'::text)
),
qualification_input_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND relation.relpersistence = 'p'
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> relation.relowner
        )
    )::integer AS exact_owner_only_count
  FROM expected_qualification_input_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
qualification_input_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
   AND relation.relname LIKE 'qualification\_input\_%' ESCAPE '\'
),
qualification_input_index_facts AS (
  SELECT
    count(index_relation.oid)::integer AS index_count,
    count(index_relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND index_relation.relowner = stable.migration_owner_oid
        AND index_catalog.indisvalid
        AND index_catalog.indisready
        AND index_catalog.indislive
    )::integer AS exact_index_count
  FROM expected_qualification_input_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS table_relation
    ON table_relation.relnamespace = schema_state.schema_oid
   AND table_relation.relname = expected.table_name
   AND table_relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indrelid = table_relation.oid
  LEFT JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.oid = index_catalog.indexrelid
   AND index_relation.relkind IN ('i', 'I')
),
expected_qualification_input_triggers(
  table_name, trigger_name, function_name, trigger_type,
  is_constraint, is_deferrable, is_initially_deferred
) AS (
  VALUES
    ('qualification_input_precommit_executable_binding_generations'::text,
     'qualification_input_precommit_executable_bindings_immutable'::text,
     'reject_qualification_input_precommit_mutation_v1'::text,
     58::smallint, false, false, false),
	('qualification_input_precommit_executable_binding_heads'::text,
	 'qualification_input_precommit_binding_heads_no_removal'::text,
	 'reject_qualification_input_precommit_mutation_v1'::text,
	 42::smallint, false, false, false),
    ('qualification_input_source_receipt_admissions'::text,
     'qualification_input_source_receipt_admissions_immutable'::text,
     'reject_qualification_input_precommit_mutation_v1'::text,
     58::smallint, false, false, false),
    ('qualification_input_credential_receipt_admissions'::text,
     'qualification_input_credential_receipt_admissions_immutable'::text,
     'reject_qualification_input_precommit_mutation_v1'::text,
     58::smallint, false, false, false),
    ('qualification_input_precommit_authorities'::text,
     'qualification_input_precommit_authorities_immutable'::text,
     'reject_qualification_input_precommit_mutation_v1'::text,
     58::smallint, false, false, false),
    ('qualification_input_precommit_identity_reservations'::text,
     'qualification_input_precommit_identity_reservations_immutable'::text,
     'reject_qualification_input_precommit_mutation_v1'::text,
     58::smallint, false, false, false),
    ('qualification_input_precommit_wia_reservations'::text,
     'qualification_input_precommit_wia_reservations_immutable'::text,
     'reject_qualification_input_precommit_mutation_v1'::text,
     58::smallint, false, false, false),
    ('qualification_input_precommit_plan_reservations'::text,
     'qualification_input_precommit_plan_reservations_immutable'::text,
     'reject_qualification_input_precommit_mutation_v1'::text,
     58::smallint, false, false, false),
    ('qualification_input_source_receipt_admissions'::text,
     'qualification_input_source_admission_exact_closure'::text,
     'enforce_qualification_input_source_admission_closure_v1'::text,
     5::smallint, true, true, true),
    ('qualification_input_credential_receipt_admissions'::text,
     'qualification_input_credential_admission_exact_closure'::text,
     'enforce_qualification_input_credential_admission_closure_v1'::text,
     5::smallint, true, true, true),
    ('qualification_input_precommit_authorities'::text,
     'qualification_input_precommit_authority_exact_closure'::text,
     'enforce_qualification_input_precommit_authority_closure_v1'::text,
     5::smallint, true, true, true)
),
qualification_input_trigger_facts AS (
  SELECT
    count(trigger.oid)::integer AS trigger_count,
    count(trigger.oid) FILTER (
      WHERE trigger.tgtype = expected.trigger_type
        AND trigger.tgenabled = 'O'
        AND NOT trigger.tgisinternal
        AND trigger.tgqual IS NULL
        AND pg_catalog.cardinality(trigger.tgattr::smallint[]) = 0
        AND trigger.tgnargs = 0
        AND trigger.tgargs = ''::bytea
        AND trigger.tgconstrrelid = 0::pg_catalog.oid
        AND trigger.tgdeferrable = expected.is_deferrable
        AND trigger.tginitdeferred = expected.is_initially_deferred
        AND trigger.tgoldtable IS NULL
        AND trigger.tgnewtable IS NULL
        AND trigger.tgparentid = 0::pg_catalog.oid
        AND routine.pronamespace = schema_state.schema_oid
        AND routine.proname = expected.function_name
        AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
        AND (
          (NOT expected.is_constraint AND trigger.tgconstraint = 0::pg_catalog.oid)
          OR
          (expected.is_constraint
            AND trigger.tgconstraint <> 0::pg_catalog.oid
            AND (
              SELECT count(*) = 1
                AND bool_and(
                  constraint_binding.connamespace = schema_state.schema_oid
                  AND constraint_binding.conname = expected.trigger_name
                  AND constraint_binding.contype = 't'
                  AND constraint_binding.conrelid = relation.oid
                  AND constraint_binding.conindid = 0::pg_catalog.oid
                  AND constraint_binding.confrelid = 0::pg_catalog.oid
                  AND constraint_binding.condeferrable = expected.is_deferrable
                  AND constraint_binding.condeferred = expected.is_initially_deferred
                  AND constraint_binding.convalidated
                  AND constraint_binding.connoinherit
                )
              FROM pg_catalog.pg_constraint AS constraint_binding
              WHERE constraint_binding.oid = trigger.tgconstraint
            )
          )
        )
    )::integer AS exact_trigger_count
  FROM expected_qualification_input_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
qualification_input_named_trigger_facts AS (
  SELECT count(*)::integer AS named_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
   AND relation.relname LIKE 'qualification\_input\_%' ESCAPE '\'
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
),
qualification_policy_data_privilege_facts AS (
	  SELECT
	    count(relation.oid) FILTER (
	      WHERE relation.relkind IN ('r', 'p', 'v', 'm', 'f')
	        AND (
	          pg_catalog.has_table_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'SELECT'
	          )
	          OR pg_catalog.has_table_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'INSERT'
	          )
	          OR pg_catalog.has_table_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'UPDATE'
	          )
	          OR pg_catalog.has_table_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'DELETE'
	          )
	          OR pg_catalog.has_table_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'TRUNCATE'
	          )
	          OR pg_catalog.has_table_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'REFERENCES'
	          )
	          OR pg_catalog.has_table_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'TRIGGER'
	          )
	        )
	    )::integer AS relation_privilege_count,
	    count(relation.oid) FILTER (
	      WHERE relation.relkind = 'S'
	        AND (
	          pg_catalog.has_sequence_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'SELECT'
	          )
	          OR pg_catalog.has_sequence_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'USAGE'
	          )
	          OR pg_catalog.has_sequence_privilege(
	            stable.qualification_policy_operator_oid, relation.oid, 'UPDATE'
	          )
	        )
	    )::integer AS sequence_privilege_count,
	    count(relation.oid) FILTER (
	      WHERE relation.relowner = stable.qualification_policy_operator_oid
	    )::integer AS owned_relation_count,
	    (
	      SELECT count(*)::integer
	      FROM pg_catalog.pg_class AS column_relation
	      JOIN pg_catalog.pg_attribute AS attribute
	        ON attribute.attrelid = column_relation.oid
	       AND attribute.attnum > 0
	       AND NOT attribute.attisdropped
	      WHERE column_relation.relnamespace = schema_state.schema_oid
	        AND column_relation.relkind IN ('r', 'p', 'v', 'm', 'f')
	        AND (
	          pg_catalog.has_column_privilege(
	            stable.qualification_policy_operator_oid,
	            column_relation.oid,
	            attribute.attnum,
	            'SELECT'
	          )
	          OR pg_catalog.has_column_privilege(
	            stable.qualification_policy_operator_oid,
	            column_relation.oid,
	            attribute.attnum,
	            'INSERT'
	          )
	          OR pg_catalog.has_column_privilege(
	            stable.qualification_policy_operator_oid,
	            column_relation.oid,
	            attribute.attnum,
	            'UPDATE'
	          )
	          OR pg_catalog.has_column_privilege(
	            stable.qualification_policy_operator_oid,
	            column_relation.oid,
	            attribute.attnum,
	            'REFERENCES'
	          )
	        )
	    ) AS column_privilege_count
	  FROM schema_facts AS schema_state
	  CROSS JOIN stable_role_facts AS stable
	  LEFT JOIN pg_catalog.pg_class AS relation
	    ON relation.relnamespace = schema_state.schema_oid
	   AND relation.relkind IN ('r', 'p', 'S', 'v', 'm', 'f', 'i', 'I')
	  GROUP BY schema_state.schema_oid, stable.qualification_policy_operator_oid
),
qualification_private_operator_roles(role_oid) AS (
  SELECT role_oid
  FROM stable_role_facts AS stable
  CROSS JOIN LATERAL (VALUES
    (stable.qualification_input_precommit_operator_oid),
    (stable.qualification_source_verifier_operator_oid),
    (stable.qualification_credential_resolver_operator_oid),
    (stable.qualification_handoff_operator_oid)
  ) AS operator(role_oid)
  WHERE role_oid IS NOT NULL
),
qualification_input_operator_roles(role_oid, operator_kind) AS (
  SELECT role_oid, operator_kind
  FROM stable_role_facts AS stable
  CROSS JOIN LATERAL (VALUES
    (stable.qualification_input_precommit_operator_oid, 'input_precommit'::text),
    (stable.qualification_source_verifier_operator_oid, 'source_verifier'::text),
    (stable.qualification_credential_resolver_operator_oid, 'credential_resolver'::text)
  ) AS operator(role_oid, operator_kind)
  WHERE role_oid IS NOT NULL
),
qualification_input_operator_data_privilege_facts AS (
  SELECT
    count(relation.oid) FILTER (
      WHERE relation.relkind IN ('r', 'p', 'v', 'm', 'f')
        AND EXISTS (
          SELECT 1 FROM qualification_private_operator_roles AS operator
          WHERE pg_catalog.has_table_privilege(operator.role_oid, relation.oid, 'SELECT')
             OR pg_catalog.has_table_privilege(operator.role_oid, relation.oid, 'INSERT')
             OR pg_catalog.has_table_privilege(operator.role_oid, relation.oid, 'UPDATE')
             OR pg_catalog.has_table_privilege(operator.role_oid, relation.oid, 'DELETE')
             OR pg_catalog.has_table_privilege(operator.role_oid, relation.oid, 'TRUNCATE')
             OR pg_catalog.has_table_privilege(operator.role_oid, relation.oid, 'REFERENCES')
             OR pg_catalog.has_table_privilege(operator.role_oid, relation.oid, 'TRIGGER')
        )
    )::integer AS relation_privilege_count,
    count(attribute.attnum) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM qualification_private_operator_roles AS operator
        WHERE pg_catalog.has_column_privilege(
          operator.role_oid, relation.oid, attribute.attnum, 'SELECT'
        ) OR pg_catalog.has_column_privilege(
          operator.role_oid, relation.oid, attribute.attnum, 'INSERT'
        ) OR pg_catalog.has_column_privilege(
          operator.role_oid, relation.oid, attribute.attnum, 'UPDATE'
        ) OR pg_catalog.has_column_privilege(
          operator.role_oid, relation.oid, attribute.attnum, 'REFERENCES'
        )
      )
    )::integer AS column_privilege_count,
    count(relation.oid) FILTER (
      WHERE relation.relkind = 'S'
        AND EXISTS (
          SELECT 1 FROM qualification_private_operator_roles AS operator
          WHERE pg_catalog.has_sequence_privilege(operator.role_oid, relation.oid, 'SELECT')
             OR pg_catalog.has_sequence_privilege(operator.role_oid, relation.oid, 'USAGE')
             OR pg_catalog.has_sequence_privilege(operator.role_oid, relation.oid, 'UPDATE')
        )
    )::integer AS sequence_privilege_count,
    count(relation.oid) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM qualification_private_operator_roles AS operator
        WHERE relation.relowner = operator.role_oid
      )
    )::integer AS owned_relation_count
  FROM schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'S', 'v', 'm', 'f')
  LEFT JOIN pg_catalog.pg_attribute AS attribute
    ON attribute.attrelid = relation.oid
   AND attribute.attnum > 0
   AND NOT attribute.attisdropped
),
expected_workflow_input_authority_tables(table_name) AS (
  VALUES
    ('qualification_policy_authorities'::text),
    ('qualification_policy_review_defaults'::text),
    ('qualification_policy_exact_approved_sources'::text),
    ('qualification_policy_identity_reservations'::text),
    ('workflow_input_authorities'::text),
    ('workflow_input_authority_identity_reservations'::text),
    ('workflow_input_authority_predecessors'::text),
    ('workflow_input_authority_manifests'::text),
    ('workflow_input_authority_revisions'::text),
    ('workflow_input_authority_review_receipts'::text)
),
workflow_input_authority_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> relation.relowner
        )
    )::integer AS exact_owner_only_count
  FROM expected_workflow_input_authority_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
workflow_input_authority_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
   AND (
     relation.relname LIKE 'qualification\_policy\_%' ESCAPE '\'
     OR relation.relname LIKE 'workflow\_input\_authorit%' ESCAPE '\'
	   )
),
expected_workflow_authority_triggers(
  contract_family, table_name, trigger_name, function_name, trigger_type,
  update_columns, is_constraint, is_deferrable, is_initially_deferred
) AS (
  VALUES
    ('workflow-input'::text, 'qualification_policy_authorities'::text,
     'qualification_policy_authorities_immutable'::text,
     'reject_qualification_policy_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'qualification_policy_review_defaults'::text,
     'qualification_policy_review_defaults_immutable'::text,
     'reject_qualification_policy_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'qualification_policy_exact_approved_sources'::text,
     'qualification_policy_exact_approved_sources_immutable'::text,
     'reject_qualification_policy_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'qualification_policy_identity_reservations'::text,
     'qualification_policy_identity_reservations_immutable'::text,
     'reject_qualification_policy_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'qualification_policy_authorities'::text,
     'qualification_policy_authorities_exact_closure'::text,
     'validate_qualification_policy_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'qualification_policy_review_defaults'::text,
     'qualification_policy_review_defaults_exact_closure'::text,
     'validate_qualification_policy_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'qualification_policy_exact_approved_sources'::text,
     'qualification_policy_exact_sources_exact_closure'::text,
     'validate_qualification_policy_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'qualification_policy_identity_reservations'::text,
     'qualification_policy_identity_reservations_exact_closure'::text,
     'validate_qualification_policy_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'workflow_input_authorities'::text,
     'workflow_input_authorities_immutable'::text,
     'reject_workflow_input_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_input_authority_identity_reservations'::text,
     'workflow_input_authority_identity_reservations_immutable'::text,
     'reject_workflow_input_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_input_authority_predecessors'::text,
     'workflow_input_authority_predecessors_immutable'::text,
     'reject_workflow_input_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_input_authority_manifests'::text,
     'workflow_input_authority_manifests_immutable'::text,
     'reject_workflow_input_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_input_authority_revisions'::text,
     'workflow_input_authority_revisions_immutable'::text,
     'reject_workflow_input_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_input_authority_review_receipts'::text,
     'workflow_input_authority_review_receipts_immutable'::text,
     'reject_workflow_input_authority_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_node_runs'::text,
     'workflow_node_stable_identity_v1_immutable'::text,
     'guard_workflow_node_stable_identity_v1'::text, 19::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_run_events'::text,
     'workflow_input_authority_event_identity_guard'::text,
     'guard_workflow_input_authority_event_identity_v1'::text, 31::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-input'::text, 'workflow_input_authorities'::text,
     'workflow_input_authorities_exact_closure'::text,
     'validate_workflow_input_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'workflow_input_authority_predecessors'::text,
     'workflow_input_authority_predecessors_exact_closure'::text,
     'validate_workflow_input_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'workflow_input_authority_manifests'::text,
     'workflow_input_authority_manifests_exact_closure'::text,
     'validate_workflow_input_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'workflow_input_authority_revisions'::text,
     'workflow_input_authority_revisions_exact_closure'::text,
     'validate_workflow_input_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'workflow_input_authority_review_receipts'::text,
     'workflow_input_authority_review_receipts_exact_closure'::text,
     'validate_workflow_input_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'workflow_node_runs'::text,
     'workflow_node_input_authority_exact_closure'::text,
     'validate_workflow_input_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-input'::text, 'workflow_run_events'::text,
     'workflow_input_authority_event_exact_closure'::text,
     'validate_workflow_input_authority_closure_v1'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-v3'::text, 'workflow_definition_versions'::text,
     'workflow_execution_profile_v3_definition_guard'::text,
     'guard_workflow_execution_profile_v3_definition'::text, 23::smallint,
     ARRAY['content','content_hash','execution_profile_version','execution_profile_hash']::text[],
     false, false, false),
    ('workflow-v3'::text, 'workflow_runs'::text,
     'workflow_execution_profile_v3_run_guard'::text,
     'guard_workflow_execution_profile_v3_run'::text, 23::smallint,
     ARRAY['definition_version_id','execution_profile_version','execution_profile_hash','status','context','event_cursor']::text[],
     false, false, false),
    ('workflow-v3'::text, 'workflow_node_runs'::text,
     'external_qualification_gate_node_v3_guard'::text,
     'guard_external_qualification_gate_node_v3'::text, 23::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-v3'::text, 'workflow_runs'::text,
     'workflow_execution_profile_v3_run_exact_closure'::text,
     'validate_workflow_execution_profile_v3_run_closure'::text, 21::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-v3'::text, 'workflow_node_runs'::text,
     'workflow_execution_profile_v3_node_exact_closure'::text,
     'validate_workflow_execution_profile_v3_run_closure'::text, 29::smallint,
     ARRAY[]::text[], true, true, true),
    ('workflow-shared-legacy'::text, 'workflow_definition_versions'::text,
     'workflow_definition_execution_profile_immutable'::text,
     'guard_workflow_definition_execution_profile_identity'::text, 19::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-shared-legacy'::text, 'workflow_runs'::text,
     'workflow_run_execution_profile_immutable'::text,
     'guard_workflow_run_execution_profile_identity'::text, 19::smallint,
     ARRAY[]::text[], false, false, false),
    ('workflow-shared-legacy'::text, 'workflow_runs'::text,
     'workflow_run_governance_mode_immutable'::text,
     'workflow_run_governance_mode_immutable'::text, 19::smallint,
     ARRAY[]::text[], false, false, false)
),
workflow_authority_trigger_contract_rows AS (
  SELECT
    expected.contract_family,
    trigger.oid AS trigger_oid,
    trigger.tgtype = expected.trigger_type
      AND trigger.tgenabled = 'O'
      AND NOT trigger.tgisinternal
      AND trigger.tgqual IS NULL
      AND pg_catalog.cardinality(trigger.tgattr::smallint[]) =
        pg_catalog.cardinality(expected.update_columns)
      AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.unnest(trigger.tgattr::smallint[])
          WITH ORDINALITY AS trigger_column(attribute_number, ordinal_position)
        LEFT JOIN pg_catalog.pg_attribute AS attribute
          ON attribute.attrelid = relation.oid
         AND attribute.attnum = trigger_column.attribute_number
         AND NOT attribute.attisdropped
        WHERE attribute.attname IS DISTINCT FROM
          expected.update_columns[trigger_column.ordinal_position::integer]
      )
      AND trigger.tgnargs = 0
      AND trigger.tgargs = ''::bytea
      AND trigger.tgconstrrelid = 0::pg_catalog.oid
      AND trigger.tgdeferrable = expected.is_deferrable
      AND trigger.tginitdeferred = expected.is_initially_deferred
      AND trigger.tgoldtable IS NULL
      AND trigger.tgnewtable IS NULL
      AND trigger.tgparentid = 0::pg_catalog.oid
      AND routine.pronamespace = schema_state.schema_oid
      AND routine.proname = expected.function_name
      AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
      AND (
        (NOT expected.is_constraint
          AND trigger.tgconstraint = 0::pg_catalog.oid)
        OR
        (expected.is_constraint
          AND trigger.tgconstraint <> 0::pg_catalog.oid
          AND (
            SELECT count(*) = 1
              AND bool_and(
                constraint_binding.connamespace = schema_state.schema_oid
                AND constraint_binding.conname = expected.trigger_name
                AND constraint_binding.contype = 't'
                AND constraint_binding.conrelid = relation.oid
                AND constraint_binding.conindid = 0::pg_catalog.oid
                AND constraint_binding.confrelid = 0::pg_catalog.oid
                AND constraint_binding.condeferrable = expected.is_deferrable
                AND constraint_binding.condeferred =
                  expected.is_initially_deferred
                AND constraint_binding.convalidated
                AND constraint_binding.connoinherit
              )
            FROM pg_catalog.pg_constraint AS constraint_binding
            WHERE constraint_binding.oid = trigger.tgconstraint
          )
        )
      ) AS is_exact
  FROM expected_workflow_authority_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
workflow_input_authority_trigger_facts AS (
  SELECT
    count(trigger_oid)::integer AS trigger_count,
    count(*) FILTER (WHERE is_exact)::integer AS exact_trigger_count
  FROM workflow_authority_trigger_contract_rows
  WHERE contract_family = 'workflow-input'
),
workflow_execution_profile_v3_trigger_facts AS (
  SELECT
    count(trigger_oid)::integer AS trigger_count,
    count(*) FILTER (WHERE is_exact)::integer AS exact_trigger_count
  FROM workflow_authority_trigger_contract_rows
  WHERE contract_family = 'workflow-v3'
),
expected_workflow_execution_profile_v3_hash_functions(function_name) AS (
  VALUES
    ('freeze_workflow_input_authority_v1'::text),
    ('guard_workflow_execution_profile_v3_definition'::text),
    ('guard_workflow_execution_profile_v3_run'::text),
    ('guard_external_qualification_gate_node_v3'::text)
),
workflow_execution_profile_v3_hash_function_facts AS (
  SELECT count(routine.oid) FILTER (
      WHERE pg_catalog.strpos(
              routine.prosrc,
              '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
            ) > 0
        AND pg_catalog.strpos(
              routine.prosrc,
              'aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef'
            ) = 0
    )::integer AS exact_hash_function_count
  FROM expected_workflow_execution_profile_v3_hash_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
),
workflow_execution_profile_v3_hash_constraint_facts AS (
  SELECT CASE WHEN count(*) = 1
    AND bool_and(
      pg_catalog.strpos(
        pg_catalog.pg_get_expr(constraint_row.conbin, constraint_row.conrelid),
        'sha256:854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
      ) > 0
      AND pg_catalog.strpos(
        pg_catalog.pg_get_expr(constraint_row.conbin, constraint_row.conrelid),
        'aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef'
      ) = 0
    ) THEN 1 ELSE 0 END::integer AS exact_hash_constraint_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = 'workflow_input_authorities'
  JOIN pg_catalog.pg_constraint AS constraint_row
    ON constraint_row.conrelid = relation.oid
   AND constraint_row.contype = 'c'
  WHERE pg_catalog.strpos(
          pg_catalog.pg_get_expr(constraint_row.conbin, constraint_row.conrelid),
          'execution_profile_hash'
        ) > 0
),
workflow_execution_profile_v3_hash_facts AS (
  SELECT (
    functions.exact_hash_function_count + constraints.exact_hash_constraint_count
  )::integer AS exact_hash_contract_count
  FROM workflow_execution_profile_v3_hash_function_facts AS functions
  CROSS JOIN workflow_execution_profile_v3_hash_constraint_facts AS constraints
),
workflow_shared_legacy_trigger_facts AS (
  SELECT
    count(trigger_oid)::integer AS trigger_count,
    count(*) FILTER (WHERE is_exact)::integer AS exact_trigger_count
  FROM workflow_authority_trigger_contract_rows
  WHERE contract_family = 'workflow-shared-legacy'
),
workflow_authority_named_trigger_facts AS (
  SELECT
    count(*) FILTER (
      WHERE relation.relname IN (
             'qualification_policy_authorities',
             'qualification_policy_review_defaults',
             'qualification_policy_exact_approved_sources',
             'qualification_policy_identity_reservations',
             'workflow_input_authorities',
             'workflow_input_authority_identity_reservations',
             'workflow_input_authority_predecessors',
             'workflow_input_authority_manifests',
             'workflow_input_authority_revisions',
             'workflow_input_authority_review_receipts'
           )
         OR trigger.tgname LIKE 'qualification\_policy\_%' ESCAPE '\'
         OR trigger.tgname LIKE 'workflow\_input\_%' ESCAPE '\'
         OR trigger.tgname IN (
           'workflow_node_stable_identity_v1_immutable',
           'workflow_node_input_authority_exact_closure'
         )
    )::integer AS workflow_input_named_trigger_count,
    count(*) FILTER (
      WHERE trigger.tgname LIKE 'workflow\_execution\_profile\_v3\_%' ESCAPE '\'
         OR trigger.tgname = 'external_qualification_gate_node_v3_guard'
    )::integer AS workflow_v3_named_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
),
workflow_shared_relation_total_trigger_facts AS (
  SELECT count(trigger.oid)::integer AS total_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname IN (
     'workflow_definition_versions',
     'workflow_runs',
     'workflow_node_runs',
     'workflow_run_events'
   )
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
),
expected_workflow_authority_trigger_functions(
  function_name, is_security_definer, schema_in_search_path
) AS (
  VALUES
    ('reject_qualification_policy_authority_mutation'::text, false, false),
    ('validate_qualification_policy_authority_closure_v1'::text, true, true),
    ('reject_workflow_input_authority_mutation'::text, false, false),
    ('guard_workflow_node_stable_identity_v1'::text, false, false),
    ('guard_workflow_input_authority_event_identity_v1'::text, true, true),
    ('validate_workflow_input_authority_closure_v1'::text, true, true),
    ('guard_workflow_execution_profile_v3_definition'::text, true, true),
    ('guard_workflow_execution_profile_v3_run'::text, true, true),
    ('guard_external_qualification_gate_node_v3'::text, true, true),
    ('validate_workflow_execution_profile_v3_run_closure'::text, true, true)
),
workflow_authority_trigger_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proretset
        AND NOT routine.proleakproof
        AND routine.prorettype = 'trigger'::pg_catalog.regtype
        AND routine.pronargs = 0
        AND routine.pronargdefaults = 0
        AND routine.provariadic = 0::pg_catalog.oid
        AND routine.proallargtypes IS NULL
        AND routine.proargmodes IS NULL
        AND routine.proargnames IS NULL
        AND routine.proargdefaults IS NULL
        AND language.lanname = 'plpgsql'
        AND routine.provolatile = 'v'
        AND NOT routine.proisstrict
        AND routine.proparallel = 'u'
        AND routine.prosecdef = expected.is_security_definer
        AND routine.proconfig = CASE
          WHEN expected.schema_in_search_path THEN ARRAY[
            pg_catalog.format(
              'search_path=pg_catalog, %I', schema_state.schema_name
            )
          ]::text[]
          ELSE ARRAY['search_path=pg_catalog']::text[]
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.migration_owner_oid
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_contract_count
  FROM expected_workflow_authority_trigger_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
workflow_authority_trigger_named_function_facts AS (
  SELECT count(routine.oid)::integer AS named_function_count
  FROM expected_workflow_authority_trigger_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
),
expected_workflow_shared_legacy_trigger_functions(function_name) AS (
  VALUES
    ('guard_workflow_definition_execution_profile_identity'::text),
    ('guard_workflow_run_execution_profile_identity'::text),
    ('workflow_run_governance_mode_immutable'::text)
),
workflow_shared_legacy_trigger_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.application_oid IS NOT NULL
        AND NOT EXISTS (
          SELECT 1
          FROM session_reachable_roles AS reachable
          WHERE reachable.role_oid = routine.proowner
        )
        AND routine.prokind = 'f'
        AND NOT routine.proretset
        AND NOT routine.proleakproof
        AND routine.prorettype = 'trigger'::pg_catalog.regtype
        AND routine.pronargs = 0
        AND routine.pronargdefaults = 0
        AND routine.provariadic = 0::pg_catalog.oid
        AND routine.proallargtypes IS NULL
        AND routine.proargmodes IS NULL
        AND routine.proargnames IS NULL
        AND routine.proargdefaults IS NULL
        AND language.lanname = 'plpgsql'
        AND routine.provolatile = 'v'
        AND NOT routine.proisstrict
        AND routine.proparallel = 'u'
        AND NOT routine.prosecdef
        AND routine.proconfig IS NULL
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.application_oid
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type <> 'EXECUTE'
             OR routine_acl.grantee NOT IN (
               routine.proowner, stable.application_oid
             )
             OR (
               routine_acl.grantee = stable.application_oid
               AND routine_acl.is_grantable
             )
        )
    )::integer AS exact_contract_count
  FROM expected_workflow_shared_legacy_trigger_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
workflow_shared_legacy_trigger_named_function_facts AS (
  SELECT count(routine.oid)::integer AS named_function_count
  FROM expected_workflow_shared_legacy_trigger_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
),
expected_credential_set_tables(table_name) AS (
  VALUES
    ('credential_set_events'::text),
    ('credential_set_operations'::text),
    ('credential_set_heads'::text),
    ('credential_set_projection_authorizations'::text)
),
credential_set_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_owner_only_count
  FROM expected_credential_set_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
credential_set_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
   AND relation.relname LIKE 'credential\_set\_%' ESCAPE '\'
),
expected_credential_set_triggers(
  table_name, trigger_name, function_name, trigger_type
) AS (
  VALUES
    ('credential_set_events'::text, 'credential_set_events_immutable'::text,
     'reject_credential_set_immutable_mutation'::text, 58::smallint),
    ('credential_set_operations'::text, 'credential_set_operations_immutable'::text,
     'reject_credential_set_immutable_mutation'::text, 58::smallint),
    ('credential_set_heads'::text, 'credential_set_heads_guard'::text,
     'guard_credential_set_head_projection'::text, 62::smallint)
),
credential_set_trigger_facts AS (
  SELECT count(trigger.oid) FILTER (
    WHERE trigger.tgtype = expected.trigger_type
      AND trigger.tgenabled = 'O'
      AND NOT trigger.tgisinternal
      AND routine.pronamespace = schema_state.schema_oid
      AND routine.proname = expected.function_name
      AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
  )::integer AS exact_trigger_count
  FROM expected_credential_set_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
credential_set_total_trigger_facts AS (
  SELECT count(trigger.oid)::integer AS total_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname IN (
     'credential_set_events',
     'credential_set_operations',
     'credential_set_heads',
     'credential_set_projection_authorizations'
   )
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
),
expected_qualification_evidence_tables(table_name) AS (
  VALUES
    ('qualification_evidence_events'::text),
    ('qualification_evidence_operations'::text),
    ('qualification_evidence_heads'::text),
    ('qualification_evidence_projection_authorizations'::text)
),
qualification_evidence_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_owner_only_count
  FROM expected_qualification_evidence_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
qualification_evidence_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
   AND relation.relname LIKE 'qualification\_evidence\_%' ESCAPE '\'
),
expected_qualification_evidence_triggers(
  table_name, trigger_name, function_name, trigger_type
) AS (
  VALUES
    ('qualification_evidence_events'::text,
     'qualification_evidence_events_immutable'::text,
     'reject_qualification_evidence_immutable_mutation'::text, 58::smallint),
    ('qualification_evidence_operations'::text,
     'qualification_evidence_operations_immutable'::text,
     'reject_qualification_evidence_immutable_mutation'::text, 58::smallint),
    ('qualification_evidence_heads'::text,
     'qualification_evidence_heads_guard'::text,
     'guard_qualification_evidence_head_projection'::text, 62::smallint)
),
qualification_evidence_trigger_facts AS (
  SELECT count(trigger.oid) FILTER (
    WHERE trigger.tgtype = expected.trigger_type
      AND trigger.tgenabled = 'O'
      AND NOT trigger.tgisinternal
      AND routine.pronamespace = schema_state.schema_oid
      AND routine.proname = expected.function_name
      AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
  )::integer AS exact_trigger_count
  FROM expected_qualification_evidence_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
qualification_evidence_total_trigger_facts AS (
  SELECT count(trigger.oid)::integer AS total_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname IN (
     'qualification_evidence_events',
     'qualification_evidence_operations',
     'qualification_evidence_heads',
     'qualification_evidence_projection_authorizations'
   )
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
),
expected_qualification_plan_tables(table_name) AS (
  VALUES
    ('qualification_plan_authorities'::text),
    ('qualification_plan_identity_reservations'::text)
),
qualification_plan_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_owner_only_count
  FROM expected_qualification_plan_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
qualification_plan_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
   AND relation.relname LIKE 'qualification\_plan\_%' ESCAPE '\'
),
expected_qualification_plan_indexes(
  table_name, index_name, is_unique, is_primary
) AS (
  VALUES
    ('qualification_plan_authorities'::text,
     'qualification_plan_authorities_pkey'::text, true, true),
    ('qualification_plan_authorities'::text,
     'qualification_plan_authorities_operation_id_key'::text, true, false),
    ('qualification_plan_authorities'::text,
     'qualification_plan_authorities_plan_artifact_id_key'::text, true, false),
    ('qualification_plan_authorities'::text,
     'qualification_plan_authorities_orchestration_id_key'::text, true, false),
    ('qualification_plan_authorities'::text,
     'qualification_plan_authorities_target_idx'::text, false, false),
    ('qualification_plan_identity_reservations'::text,
     'qualification_plan_identity_reservations_pkey'::text, true, true),
    ('qualification_plan_identity_reservations'::text,
     'qualification_plan_identity_r_authority_id_identity_kind_or_key'::text,
     true, false),
    ('qualification_plan_identity_reservations'::text,
     'qualification_plan_identity_authority_idx'::text, false, false)
),
qualification_plan_index_facts AS (
  SELECT
    count(index_relation.oid)::integer AS index_count,
    count(index_relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND index_relation.relowner = stable.migration_owner_oid
        AND index_catalog.indisvalid
        AND index_catalog.indisready
        AND index_catalog.indislive
        AND index_catalog.indimmediate
        AND NOT index_catalog.indisexclusion
        AND index_catalog.indisunique = expected.is_unique
        AND index_catalog.indisprimary = expected.is_primary
    )::integer AS exact_contract_count
  FROM expected_qualification_plan_indexes AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS table_relation
    ON table_relation.relnamespace = schema_state.schema_oid
   AND table_relation.relname = expected.table_name
   AND table_relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relname = expected.index_name
   AND index_relation.relkind IN ('i', 'I')
  LEFT JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indexrelid = index_relation.oid
   AND index_catalog.indrelid = table_relation.oid
),
qualification_plan_named_index_facts AS (
  SELECT count(*)::integer AS named_index_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relkind IN ('i', 'I')
   AND index_relation.relname LIKE 'qualification\_plan\_%' ESCAPE '\'
),
expected_qualification_plan_triggers(
  table_name, trigger_name, function_name, trigger_type
) AS (
  VALUES
    ('qualification_plan_authorities'::text,
     'qualification_plan_authorities_immutable'::text,
     'reject_qualification_plan_immutable_mutation'::text, 58::smallint),
    ('qualification_plan_identity_reservations'::text,
     'qualification_plan_identity_reservations_immutable'::text,
     'reject_qualification_plan_immutable_mutation'::text, 58::smallint),
    ('qualification_evidence_events'::text,
     'qualification_evidence_plan_authority_guard'::text,
     'guard_qualification_evidence_plan_authority'::text, 7::smallint)
),
qualification_plan_trigger_facts AS (
  SELECT count(trigger.oid) FILTER (
    WHERE trigger.tgtype = expected.trigger_type
      AND trigger.tgenabled = 'O'
      AND NOT trigger.tgisinternal
      AND routine.pronamespace = schema_state.schema_oid
      AND routine.proname = expected.function_name
      AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
  )::integer AS exact_trigger_count
  FROM expected_qualification_plan_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
qualification_plan_named_trigger_facts AS (
  SELECT count(*)::integer AS named_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
   AND (
     trigger.tgname LIKE 'qualification\_plan\_%' ESCAPE '\'
     OR trigger.tgname = 'qualification_evidence_plan_authority_guard'
   )
),
expected_qualification_receipt_v3_tables(table_name) AS (
  VALUES
    ('qualification_receipt_v3_requests'::text),
    ('qualification_receipt_v3_observations'::text),
    ('qualification_receipt_v3_receipts'::text)
),
qualification_receipt_v3_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_owner_only_count
  FROM expected_qualification_receipt_v3_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
qualification_receipt_v3_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
   AND relation.relname LIKE 'qualification\_receipt\_v3\_%' ESCAPE '\'
),
expected_qualification_receipt_v3_indexes(
  table_name, index_name, key_columns, is_unique, is_primary,
  constraint_name, constraint_type
) AS (
  VALUES
    ('qualification_receipt_v3_requests'::text,
     'qualification_receipt_v3_requests_pkey'::text,
     ARRAY['request_hash']::text[], true, true,
     'qualification_receipt_v3_requests_pkey'::text, 'p'::text),
    ('qualification_receipt_v3_requests'::text,
     'qualification_receipt_v3_requ_plan_authority_id_request_kin_key'::text,
     ARRAY['plan_authority_id', 'request_kind', 'signer_role']::text[],
     true, false,
     'qualification_receipt_v3_requ_plan_authority_id_request_kin_key'::text,
     'u'::text),
    ('qualification_receipt_v3_requests'::text,
     'qualification_receipt_v3_requ_operation_id_request_kind_sig_key'::text,
     ARRAY['operation_id', 'request_kind', 'signer_role']::text[],
     true, false,
     'qualification_receipt_v3_requ_operation_id_request_kind_sig_key'::text,
     'u'::text),
    ('qualification_receipt_v3_requests'::text,
     'qualification_receipt_v3_requests_orchestration_idx'::text,
     ARRAY['orchestration_id', 'request_kind', 'signer_role']::text[],
     false, false, NULL::text, NULL::text),
    ('qualification_receipt_v3_observations'::text,
     'qualification_receipt_v3_observations_claim_id_key'::text,
     ARRAY['claim_id']::text[], true, false,
     'qualification_receipt_v3_observations_claim_id_key'::text, 'u'::text),
    ('qualification_receipt_v3_observations'::text,
     'qualification_receipt_v3_observations_acknowledgement_id_key'::text,
     ARRAY['acknowledgement_id']::text[], true, false,
     'qualification_receipt_v3_observations_acknowledgement_id_key'::text,
     'u'::text),
    ('qualification_receipt_v3_observations'::text,
     'qualification_receipt_v3_observations_pkey'::text,
     ARRAY['request_hash', 'sequence']::text[], true, true,
     'qualification_receipt_v3_observations_pkey'::text, 'p'::text),
    ('qualification_receipt_v3_observations'::text,
     'qualification_receipt_v3_observations_record_hash_key'::text,
     ARRAY['record_hash']::text[], true, false,
     'qualification_receipt_v3_observations_record_hash_key'::text,
     'u'::text),
    ('qualification_receipt_v3_observations'::text,
     'qualification_receipt_v3_observati_request_hash_record_hash_key'::text,
     ARRAY['request_hash', 'record_hash']::text[], true, false,
     'qualification_receipt_v3_observati_request_hash_record_hash_key'::text,
     'u'::text),
    ('qualification_receipt_v3_observations'::text,
     'qualification_receipt_v3_observations_state_idx'::text,
     ARRAY['request_hash', 'generation', 'sequence', 'status']::text[],
     false, false, NULL::text, NULL::text),
    ('qualification_receipt_v3_receipts'::text,
     'qualification_receipt_v3_receipts_pkey'::text,
     ARRAY['receipt_id']::text[], true, true,
     'qualification_receipt_v3_receipts_pkey'::text, 'p'::text),
    ('qualification_receipt_v3_receipts'::text,
     'qualification_receipt_v3_receipts_plan_authority_id_key'::text,
     ARRAY['plan_authority_id']::text[], true, false,
     'qualification_receipt_v3_receipts_plan_authority_id_key'::text,
     'u'::text),
    ('qualification_receipt_v3_receipts'::text,
     'qualification_receipt_v3_receipts_receipt_sign_operation_id_key'::text,
     ARRAY['receipt_sign_operation_id']::text[], true, false,
     'qualification_receipt_v3_receipts_receipt_sign_operation_id_key'::text,
     'u'::text),
    ('qualification_receipt_v3_receipts'::text,
     'qualification_receipt_v3_receipts_target_idx'::text,
     ARRAY['project_id', 'workflow_run_id', 'node_key',
           'target_revision_id']::text[],
     false, false, NULL::text, NULL::text)
),
qualification_receipt_v3_index_facts AS (
  SELECT
    count(index_relation.oid)::integer AS index_count,
    count(index_relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND index_relation.relowner = stable.migration_owner_oid
        AND index_catalog.indisvalid
        AND index_catalog.indisready
        AND index_catalog.indislive
        AND index_catalog.indimmediate
        AND NOT index_catalog.indisexclusion
        AND index_catalog.indisunique = expected.is_unique
        AND index_catalog.indisprimary = expected.is_primary
        AND NOT index_catalog.indnullsnotdistinct
        AND index_catalog.indexprs IS NULL
        AND index_catalog.indpred IS NULL
        AND index_catalog.indnkeyatts =
          pg_catalog.cardinality(expected.key_columns)
        AND index_catalog.indnatts =
          pg_catalog.cardinality(expected.key_columns)
        AND access_method.amname = 'btree'
        AND NOT EXISTS (
          SELECT 1
          FROM ROWS FROM (
            pg_catalog.unnest(index_catalog.indkey::smallint[]),
            pg_catalog.unnest(index_catalog.indclass::pg_catalog.oid[]),
            pg_catalog.unnest(index_catalog.indcollation::pg_catalog.oid[]),
            pg_catalog.unnest(index_catalog.indoption::smallint[])
          ) WITH ORDINALITY AS key_entry(
            attribute_number, operator_class_oid, collation_oid,
            option_bits, ordinal_position
          )
          LEFT JOIN pg_catalog.pg_attribute AS attribute
            ON attribute.attrelid = table_relation.oid
           AND attribute.attnum = key_entry.attribute_number
           AND key_entry.attribute_number > 0
           AND NOT attribute.attisdropped
          LEFT JOIN pg_catalog.pg_opclass AS operator_class
            ON operator_class.oid = key_entry.operator_class_oid
          WHERE key_entry.ordinal_position <= index_catalog.indnkeyatts
            AND (
              attribute.attname IS DISTINCT FROM
                expected.key_columns[key_entry.ordinal_position::integer]
              OR operator_class.oid IS NULL
              OR NOT operator_class.opcdefault
              OR operator_class.opcmethod <> index_relation.relam
              OR operator_class.opcintype <> attribute.atttypid
              OR key_entry.collation_oid <> attribute.attcollation
              OR key_entry.option_bits <> 0
            )
        )
        AND (
          (expected.constraint_name IS NULL AND NOT EXISTS (
            SELECT 1
            FROM pg_catalog.pg_constraint AS constraint_binding
            WHERE constraint_binding.conrelid = table_relation.oid
              AND constraint_binding.conindid = index_relation.oid
              AND constraint_binding.contype IN ('p', 'u', 'x')
          ))
          OR
          (expected.constraint_name IS NOT NULL
           AND (
             SELECT count(*) = 1
               AND bool_and(
                 constraint_binding.connamespace = schema_state.schema_oid
                 AND constraint_binding.conrelid = table_relation.oid
                 AND constraint_binding.conindid = index_relation.oid
                 AND constraint_binding.conname = expected.constraint_name
                 AND constraint_binding.contype::text = expected.constraint_type
               )
             FROM pg_catalog.pg_constraint AS constraint_binding
             WHERE constraint_binding.conrelid = table_relation.oid
               AND constraint_binding.conindid = index_relation.oid
               AND constraint_binding.contype IN ('p', 'u', 'x')
           ))
        )
    )::integer AS exact_contract_count
  FROM expected_qualification_receipt_v3_indexes AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS table_relation
    ON table_relation.relnamespace = schema_state.schema_oid
   AND table_relation.relname = expected.table_name
   AND table_relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relname = expected.index_name
   AND index_relation.relkind IN ('i', 'I')
  LEFT JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indexrelid = index_relation.oid
   AND index_catalog.indrelid = table_relation.oid
  LEFT JOIN pg_catalog.pg_am AS access_method
    ON access_method.oid = index_relation.relam
),
qualification_receipt_v3_named_index_facts AS (
  SELECT count(*)::integer AS named_index_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relkind IN ('i', 'I')
   AND index_relation.relname LIKE 'qualification\_receipt\_v3\_%' ESCAPE '\'
),
expected_qualification_receipt_v3_triggers(
  table_name, trigger_name, function_name, trigger_type
) AS (
  VALUES
    ('qualification_receipt_v3_requests'::text,
     'qualification_receipt_v3_requests_immutable'::text,
     'reject_qualification_receipt_v3_mutation'::text, 58::smallint),
    ('qualification_receipt_v3_observations'::text,
     'qualification_receipt_v3_observations_immutable'::text,
     'reject_qualification_receipt_v3_mutation'::text, 58::smallint),
    ('qualification_receipt_v3_receipts'::text,
     'qualification_receipt_v3_receipts_immutable'::text,
     'reject_qualification_receipt_v3_mutation'::text, 58::smallint),
    ('qualification_evidence_events'::text,
     'qualification_evidence_v1_receipt_tail_history_only'::text,
     'guard_qualification_evidence_v1_receipt_tail_history_only'::text,
     7::smallint),
    ('qualification_promotion_consumptions'::text,
     'qualification_promotion_v1_new_consumption_history_only'::text,
     'guard_qualification_promotion_v1_new_consumption_history_only'::text,
     7::smallint)
),
qualification_receipt_v3_trigger_facts AS (
  SELECT count(trigger.oid) FILTER (
    WHERE trigger.tgtype = expected.trigger_type
      AND trigger.tgenabled = 'O'
      AND NOT trigger.tgisinternal
      AND trigger.tgqual IS NULL
      AND pg_catalog.cardinality(trigger.tgattr::smallint[]) = 0
      AND trigger.tgnargs = 0
      AND trigger.tgargs = ''::bytea
      AND trigger.tgconstraint = 0::pg_catalog.oid
      AND trigger.tgconstrrelid = 0::pg_catalog.oid
      AND NOT trigger.tgdeferrable
      AND NOT trigger.tginitdeferred
      AND trigger.tgoldtable IS NULL
      AND trigger.tgnewtable IS NULL
      AND trigger.tgparentid = 0::pg_catalog.oid
      AND routine.pronamespace = schema_state.schema_oid
      AND routine.proname = expected.function_name
      AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
  )::integer AS exact_trigger_count
  FROM expected_qualification_receipt_v3_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
qualification_receipt_v3_named_trigger_facts AS (
  SELECT count(*)::integer AS named_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
   AND (
     relation.relname IN (
       'qualification_receipt_v3_requests',
       'qualification_receipt_v3_observations',
       'qualification_receipt_v3_receipts'
     )
     OR trigger.tgname IN (
       'qualification_evidence_v1_receipt_tail_history_only',
       'qualification_promotion_v1_new_consumption_history_only'
     )
   )
),
expected_canonical_review_table_columns(
  ordinal_position, column_name, data_type, is_not_null, has_default_collation
) AS (
  VALUES
    (1, 'review_request_id'::text, 'uuid'::text, true, false),
    (2, 'receipt_hash'::text, 'text'::text, true, true),
    (3, 'receipt_bytes'::text, 'bytea'::text, true, false),
    (4, 'receipt_document'::text, 'jsonb'::text, true, false),
    (5, 'review_request_snapshot_hash'::text, 'text'::text, true, true),
    (6, 'review_request_snapshot_bytes'::text, 'bytea'::text, true, false),
    (7, 'review_request_snapshot_document'::text, 'jsonb'::text, true, false),
    (8, 'revision_snapshot_hash'::text, 'text'::text, true, true),
    (9, 'revision_snapshot_bytes'::text, 'bytea'::text, true, false),
    (10, 'revision_snapshot_document'::text, 'jsonb'::text, true, false),
    (11, 'policy_snapshot_hash'::text, 'text'::text, true, true),
    (12, 'policy_snapshot_bytes'::text, 'bytea'::text, true, false),
    (13, 'policy_snapshot_document'::text, 'jsonb'::text, true, false),
    (14, 'decisions_snapshot_hash'::text, 'text'::text, true, true),
    (15, 'decisions_snapshot_bytes'::text, 'bytea'::text, true, false),
    (16, 'decisions_snapshot_document'::text, 'jsonb'::text, true, false),
    (17, 'governance_snapshot_hash'::text, 'text'::text, true, true),
    (18, 'governance_snapshot_bytes'::text, 'bytea'::text, true, false),
    (19, 'governance_snapshot_document'::text, 'jsonb'::text, true, false),
    (20, 'approval_snapshot_hash'::text, 'text'::text, true, true),
    (21, 'approval_snapshot_bytes'::text, 'bytea'::text, true, false),
    (22, 'approval_snapshot_document'::text, 'jsonb'::text, true, false),
    (23, 'project_id'::text, 'uuid'::text, true, false),
    (24, 'artifact_id'::text, 'uuid'::text, true, false),
    (25, 'revision_id'::text, 'uuid'::text, true, false),
    (26, 'revision_content_hash'::text, 'text'::text, true, true),
    (27, 'closed_by_decision_id'::text, 'uuid'::text, true, false),
    (28, 'approval_count'::text, 'integer'::text, true, false),
    (29, 'minimum_approvals'::text, 'integer'::text, true, false),
    (30, 'governance_mode'::text, 'text'::text, true, true),
    (31, 'owner_count'::text, 'integer'::text, true, false),
    (32, 'solo_self_review'::text, 'boolean'::text, true, false),
    (33, 'sole_owner_id'::text, 'uuid'::text, false, false),
    (34, 'issued_at'::text, 'timestamp with time zone'::text, true, false)
),
canonical_review_table_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND relation.relowner = stable.migration_owner_oid
        AND relation.relkind = 'r'
        AND relation.relpersistence = 'p'
        AND NOT relation.relispartition
        AND relation.reloftype = 0::pg_catalog.oid
        AND NOT relation.relrowsecurity
        AND NOT relation.relforcerowsecurity
        AND relation.relnatts = (
          SELECT count(*) FROM expected_canonical_review_table_columns
        )
        AND (
          SELECT count(*)
          FROM pg_catalog.pg_attribute AS attribute
          WHERE attribute.attrelid = relation.oid
            AND attribute.attnum > 0
            AND NOT attribute.attisdropped
        ) = (SELECT count(*) FROM expected_canonical_review_table_columns)
        AND NOT EXISTS (
          SELECT 1
          FROM expected_canonical_review_table_columns AS expected_column
          LEFT JOIN pg_catalog.pg_attribute AS attribute
            ON attribute.attrelid = relation.oid
           AND attribute.attnum = expected_column.ordinal_position
           AND NOT attribute.attisdropped
          LEFT JOIN pg_catalog.pg_attrdef AS column_default
            ON column_default.adrelid = relation.oid
           AND column_default.adnum = attribute.attnum
          LEFT JOIN pg_catalog.pg_collation AS collation_catalog
            ON collation_catalog.oid = attribute.attcollation
          LEFT JOIN pg_catalog.pg_namespace AS collation_schema
            ON collation_schema.oid = collation_catalog.collnamespace
          WHERE attribute.attname IS DISTINCT FROM expected_column.column_name
             OR pg_catalog.format_type(attribute.atttypid, attribute.atttypmod)
                  IS DISTINCT FROM expected_column.data_type
             OR attribute.attnotnull IS DISTINCT FROM expected_column.is_not_null
             OR column_default.oid IS NOT NULL
             OR attribute.attidentity <> ''
             OR attribute.attgenerated <> ''
             OR attribute.attinhcount <> 0
             OR NOT attribute.attislocal
             OR (
               expected_column.has_default_collation
               AND NOT (
                 collation_schema.nspname = 'pg_catalog'
                 AND collation_catalog.collname = 'default'
                 AND collation_catalog.collprovider = 'd'
               )
             )
             OR (
               NOT expected_column.has_default_collation
               AND attribute.attcollation <> 0::pg_catalog.oid
             )
        )
        AND coalesce((
          SELECT array_agg(table_acl.privilege_type ORDER BY table_acl.privilege_type)
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee = stable.migration_owner_oid
            AND NOT table_acl.is_grantable
        ), ARRAY[]::text[]) = ARRAY[
          'DELETE', 'INSERT', 'REFERENCES', 'SELECT', 'TRIGGER', 'TRUNCATE',
          'UPDATE'
        ]::text[]
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee <> stable.migration_owner_oid
             OR table_acl.is_grantable
        )
    )::integer AS exact_contract_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = 'canonical_review_approval_receipts'
),
canonical_review_named_table_facts AS (
  SELECT count(*)::integer AS named_table_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
   AND relation.relname LIKE 'canonical\_review\_%' ESCAPE '\'
),
expected_canonical_review_indexes(
  table_name, index_name, key_columns, operator_classes, collations,
  is_unique, is_primary, constraint_name, constraint_type
) AS (
  VALUES
    ('canonical_review_approval_receipts'::text,
     'canonical_review_receipts_pkey'::text,
     ARRAY['review_request_id']::text[], ARRAY['uuid_ops']::text[],
     ARRAY['']::text[], true, true,
     'canonical_review_receipts_pkey'::text, 'p'::text),
    ('canonical_review_approval_receipts'::text,
     'canonical_review_receipts_hash_key'::text,
     ARRAY['receipt_hash']::text[], ARRAY['text_ops']::text[],
     ARRAY['pg_catalog.default']::text[], true, false,
     'canonical_review_receipts_hash_key'::text, 'u'::text),
    ('canonical_review_approval_receipts'::text,
     'canonical_review_receipts_revision_key'::text,
     ARRAY['revision_id']::text[], ARRAY['uuid_ops']::text[],
     ARRAY['']::text[], true, false,
     'canonical_review_receipts_revision_key'::text, 'u'::text),
	    ('canonical_review_approval_receipts'::text,
	     'canonical_review_receipts_target_idx'::text,
	     ARRAY['project_id', 'artifact_id', 'revision_id']::text[],
	     ARRAY['uuid_ops', 'uuid_ops', 'uuid_ops']::text[],
	     ARRAY['', '', '']::text[], false, false, NULL::text, NULL::text),
	    ('canonical_review_approval_receipts'::text,
	     'canonical_review_receipts_workflow_input_exact_unique'::text,
	     ARRAY[
	       'review_request_id', 'receipt_hash', 'project_id',
	       'artifact_id', 'revision_id', 'revision_content_hash'
	     ]::text[],
	     ARRAY[
	       'uuid_ops', 'text_ops', 'uuid_ops', 'uuid_ops', 'uuid_ops', 'text_ops'
	     ]::text[],
	     ARRAY[
	       '', 'pg_catalog.default', '', '', '', 'pg_catalog.default'
	     ]::text[], true, false,
	     'canonical_review_receipts_workflow_input_exact_unique'::text,
	     'u'::text),
	    ('review_decisions'::text,
     'review_decisions_request_id_id_key'::text,
     ARRAY['review_request_id', 'id']::text[],
     ARRAY['uuid_ops', 'uuid_ops']::text[], ARRAY['', '']::text[],
     true, false, 'review_decisions_request_id_id_key'::text, 'u'::text)
),
canonical_review_index_facts AS (
  SELECT
    count(index_relation.oid)::integer AS index_count,
    count(index_relation.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND index_relation.relowner = stable.migration_owner_oid
        AND index_relation.relpersistence = 'p'
        AND index_catalog.indisvalid
        AND index_catalog.indisready
        AND index_catalog.indislive
        AND index_catalog.indimmediate
        AND NOT index_catalog.indisexclusion
        AND NOT index_catalog.indisclustered
        AND NOT index_catalog.indisreplident
        AND index_catalog.indisunique = expected.is_unique
        AND index_catalog.indisprimary = expected.is_primary
        AND NOT index_catalog.indnullsnotdistinct
        AND index_catalog.indexprs IS NULL
        AND index_catalog.indpred IS NULL
        AND index_catalog.indnkeyatts = pg_catalog.cardinality(expected.key_columns)
        AND index_catalog.indnatts = pg_catalog.cardinality(expected.key_columns)
        AND access_method.amname = 'btree'
        AND NOT EXISTS (
          SELECT 1
          FROM ROWS FROM (
            pg_catalog.unnest(index_catalog.indkey::smallint[]),
            pg_catalog.unnest(index_catalog.indclass::pg_catalog.oid[]),
            pg_catalog.unnest(index_catalog.indcollation::pg_catalog.oid[]),
            pg_catalog.unnest(index_catalog.indoption::smallint[])
          ) WITH ORDINALITY AS key_entry(
            attribute_number, operator_class_oid, collation_oid,
            option_bits, ordinal_position
          )
          LEFT JOIN pg_catalog.pg_attribute AS attribute
            ON attribute.attrelid = table_relation.oid
           AND attribute.attnum = key_entry.attribute_number
           AND key_entry.attribute_number > 0
           AND NOT attribute.attisdropped
          LEFT JOIN pg_catalog.pg_opclass AS operator_class
            ON operator_class.oid = key_entry.operator_class_oid
          LEFT JOIN pg_catalog.pg_namespace AS operator_class_schema
            ON operator_class_schema.oid = operator_class.opcnamespace
          LEFT JOIN pg_catalog.pg_collation AS collation_catalog
            ON collation_catalog.oid = key_entry.collation_oid
          LEFT JOIN pg_catalog.pg_namespace AS collation_schema
            ON collation_schema.oid = collation_catalog.collnamespace
          WHERE key_entry.ordinal_position <= index_catalog.indnkeyatts
            AND (
              attribute.attname IS DISTINCT FROM
                expected.key_columns[key_entry.ordinal_position::integer]
              OR operator_class_schema.nspname IS DISTINCT FROM 'pg_catalog'
              OR operator_class.opcname IS DISTINCT FROM
                expected.operator_classes[key_entry.ordinal_position::integer]
              OR NOT operator_class.opcdefault
              OR operator_class.opcmethod <> index_relation.relam
              OR operator_class.opcintype <> attribute.atttypid
              OR key_entry.option_bits <> 0
              OR (
                expected.collations[key_entry.ordinal_position::integer] = ''
                AND key_entry.collation_oid <> 0::pg_catalog.oid
              )
              OR (
                expected.collations[key_entry.ordinal_position::integer] =
                  'pg_catalog.default'
                AND NOT (
                  collation_schema.nspname = 'pg_catalog'
                  AND collation_catalog.collname = 'default'
                  AND collation_catalog.collprovider = 'd'
                )
              )
            )
        )
        AND (
          (expected.constraint_name IS NULL AND NOT EXISTS (
            SELECT 1
            FROM pg_catalog.pg_constraint AS constraint_binding
            WHERE constraint_binding.conrelid = table_relation.oid
              AND constraint_binding.conindid = index_relation.oid
              AND constraint_binding.contype IN ('p', 'u', 'x')
          ))
          OR
          (expected.constraint_name IS NOT NULL AND (
            SELECT count(*) = 1
              AND bool_and(
                constraint_binding.connamespace = schema_state.schema_oid
                AND constraint_binding.conrelid = table_relation.oid
                AND constraint_binding.conindid = index_relation.oid
                AND constraint_binding.conname = expected.constraint_name
                AND constraint_binding.contype::text = expected.constraint_type
                AND NOT constraint_binding.condeferrable
                AND NOT constraint_binding.condeferred
                AND constraint_binding.convalidated
                AND constraint_binding.connoinherit
                AND constraint_binding.conkey::smallint[] =
                  ARRAY(
                    SELECT key_attribute
                    FROM pg_catalog.unnest(index_catalog.indkey::smallint[])
                      WITH ORDINALITY AS index_key(
                        key_attribute, ordinal_position
                      )
                    ORDER BY ordinal_position
                  )::smallint[]
              )
            FROM pg_catalog.pg_constraint AS constraint_binding
            WHERE constraint_binding.conrelid = table_relation.oid
              AND constraint_binding.conindid = index_relation.oid
              AND constraint_binding.contype IN ('p', 'u', 'x')
          ))
        )
    )::integer AS exact_contract_count
  FROM expected_canonical_review_indexes AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS table_relation
    ON table_relation.relnamespace = schema_state.schema_oid
   AND table_relation.relname = expected.table_name
   AND table_relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relname = expected.index_name
   AND index_relation.relkind IN ('i', 'I')
  LEFT JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indexrelid = index_relation.oid
   AND index_catalog.indrelid = table_relation.oid
  LEFT JOIN pg_catalog.pg_am AS access_method
    ON access_method.oid = index_relation.relam
),
canonical_review_named_index_facts AS (
  SELECT count(*)::integer AS named_index_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.relnamespace = schema_state.schema_oid
   AND index_relation.relkind IN ('i', 'I')
   AND (
     index_relation.relname LIKE 'canonical\_review\_%' ESCAPE '\'
     OR index_relation.relname = 'review_decisions_request_id_id_key'
   )
),
expected_canonical_review_triggers(
  table_name, trigger_name, function_name, trigger_type, update_columns,
  is_constraint, is_deferrable, is_initially_deferred
) AS (
  VALUES
    ('canonical_review_approval_receipts'::text,
     'canonical_review_approval_receipts_immutable'::text,
     'reject_canonical_review_receipt_mutation'::text, 58::smallint,
     ARRAY[]::text[], false, false, false),
    ('review_requests'::text,
     'canonical_review_requests_controlled_mutation'::text,
     'guard_canonical_review_source_mutation'::text, 27::smallint,
     ARRAY[]::text[], false, false, false),
    ('review_decisions'::text,
     'canonical_review_decisions_controlled_mutation'::text,
     'guard_canonical_review_source_mutation'::text, 31::smallint,
     ARRAY[]::text[], false, false, false),
    ('review_requests'::text,
     'canonical_review_approved_requires_receipt'::text,
     'require_canonical_review_approval_receipt'::text, 21::smallint,
     ARRAY['status']::text[], true, true, true)
),
canonical_review_trigger_facts AS (
  SELECT count(trigger.oid) FILTER (
    WHERE trigger.tgtype = expected.trigger_type
      AND trigger.tgenabled = 'O'
      AND NOT trigger.tgisinternal
      AND trigger.tgqual IS NULL
      AND pg_catalog.cardinality(trigger.tgattr::smallint[]) =
        pg_catalog.cardinality(expected.update_columns)
      AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.unnest(trigger.tgattr::smallint[])
          WITH ORDINALITY AS trigger_column(attribute_number, ordinal_position)
        LEFT JOIN pg_catalog.pg_attribute AS attribute
          ON attribute.attrelid = relation.oid
         AND attribute.attnum = trigger_column.attribute_number
         AND NOT attribute.attisdropped
        WHERE attribute.attname IS DISTINCT FROM
          expected.update_columns[trigger_column.ordinal_position::integer]
      )
      AND trigger.tgnargs = 0
      AND trigger.tgargs = ''::bytea
      AND trigger.tgconstrrelid = 0::pg_catalog.oid
      AND trigger.tgdeferrable = expected.is_deferrable
      AND trigger.tginitdeferred = expected.is_initially_deferred
      AND trigger.tgoldtable IS NULL
      AND trigger.tgnewtable IS NULL
      AND trigger.tgparentid = 0::pg_catalog.oid
      AND routine.pronamespace = schema_state.schema_oid
      AND routine.proname = expected.function_name
      AND pg_catalog.oidvectortypes(routine.proargtypes) = ''
      AND (
        (NOT expected.is_constraint
          AND trigger.tgconstraint = 0::pg_catalog.oid)
        OR
        (expected.is_constraint
          AND trigger.tgconstraint <> 0::pg_catalog.oid
          AND (
            SELECT count(*) = 1
              AND bool_and(
                constraint_binding.connamespace = schema_state.schema_oid
                AND constraint_binding.conname = expected.trigger_name
                AND constraint_binding.contype = 't'
                AND constraint_binding.conrelid = relation.oid
                AND constraint_binding.conindid = 0::pg_catalog.oid
                AND constraint_binding.confrelid = 0::pg_catalog.oid
                AND constraint_binding.condeferrable = expected.is_deferrable
                AND constraint_binding.condeferred =
                  expected.is_initially_deferred
                AND constraint_binding.convalidated
                AND constraint_binding.connoinherit
              )
            FROM pg_catalog.pg_constraint AS constraint_binding
            WHERE constraint_binding.oid = trigger.tgconstraint
          )
        )
      )
  )::integer AS exact_trigger_count
  FROM expected_canonical_review_triggers AS expected
  CROSS JOIN schema_facts AS schema_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND trigger.tgname = expected.trigger_name
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = trigger.tgfoid
),
canonical_review_named_trigger_facts AS (
  SELECT count(*)::integer AS named_trigger_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_trigger AS trigger
    ON trigger.tgrelid = relation.oid
   AND NOT trigger.tgisinternal
   AND (
     trigger.tgname LIKE 'canonical\_review\_%' ESCAPE '\'
     OR trigger.tgname = 'review_request_policy_immutable'
   )
),
expected_protected_tables(table_name, allowed_application_privileges) AS (
  VALUES
    ('repository_exact_tree_literal_index_blobs'::text, ARRAY['INSERT', 'SELECT']::text[]),
    ('repository_exact_tree_literal_index_members'::text, ARRAY['INSERT', 'SELECT']::text[]),
    ('repository_exact_tree_literal_index_manifests'::text, ARRAY['INSERT', 'SELECT', 'UPDATE']::text[]),
    ('repository_exact_tree_literal_index_build_claims'::text, ARRAY['SELECT']::text[]),
    ('repository_exact_tree_literal_index_gc_runs'::text, ARRAY[]::text[]),
    ('repository_exact_tree_literal_index_gc_capabilities'::text, ARRAY[]::text[]),
    ('repository_exact_tree_literal_index_gc_receipts'::text, ARRAY[]::text[]),
    ('repository_exact_tree_literal_index_gc_tombstones'::text, ARRAY[]::text[]),
    ('repository_exact_tree_literal_index_gc_tree_delete_auth'::text, ARRAY[]::text[]),
    ('repository_exact_tree_literal_index_gc_blob_delete_auth'::text, ARRAY[]::text[]),
    ('golden_fault_consume_reservations'::text, ARRAY[]::text[]),
    ('golden_fault_consume_results'::text, ARRAY[]::text[]),
    ('model_governance_activation_records'::text, ARRAY[]::text[]),
    ('model_governance_activation_heads'::text, ARRAY[]::text[]),
    ('model_governance_revocation_anchor'::text, ARRAY[]::text[]),
    ('qualification_promotion_consumptions'::text, ARRAY[]::text[]),
    ('qualification_promotion_handoffs'::text, ARRAY[]::text[]),
    ('artifact_revision_identity_reservations'::text, ARRAY[]::text[]),
    ('qualification_promotion_v2_independent_receipts'::text, ARRAY[]::text[]),
    ('qualification_promotion_v2_consumptions'::text, ARRAY[]::text[]),
    ('qualification_promotion_v2_consumption_independent_receipts'::text, ARRAY[]::text[]),
    ('qualification_promotion_v2_handoffs'::text, ARRAY[]::text[]),
    ('qualification_promotion_v2_identity_reservations'::text, ARRAY[]::text[]),
	('qualification_input_precommit_executable_binding_generations'::text, ARRAY[]::text[]),
	('qualification_input_precommit_executable_binding_heads'::text, ARRAY[]::text[]),
	('qualification_input_source_receipt_admissions'::text, ARRAY[]::text[]),
	('qualification_input_credential_receipt_admissions'::text, ARRAY[]::text[]),
	('qualification_input_precommit_authorities'::text, ARRAY[]::text[]),
	    ('qualification_input_precommit_identity_reservations'::text, ARRAY[]::text[]),
	    ('qualification_input_precommit_wia_reservations'::text, ARRAY[]::text[]),
	    ('qualification_input_precommit_plan_reservations'::text, ARRAY[]::text[]),
    ('qualification_promotion_v2_revision_transaction_grants'::text,
     ARRAY[]::text[]),
    ('qualification_promotion_v2_revision_authority_bindings'::text,
     ARRAY[]::text[]),
    ('qualification_promotion_v2_handoff_lineage_members'::text,
     ARRAY[]::text[]),
    ('qualification_promotion_v2_handoff_completions'::text,
     ARRAY[]::text[]),
    ('credential_set_events'::text, ARRAY[]::text[]),
    ('credential_set_operations'::text, ARRAY[]::text[]),
    ('credential_set_heads'::text, ARRAY[]::text[]),
    ('credential_set_projection_authorizations'::text, ARRAY[]::text[]),
    ('qualification_evidence_events'::text, ARRAY[]::text[]),
    ('qualification_evidence_operations'::text, ARRAY[]::text[]),
    ('qualification_evidence_heads'::text, ARRAY[]::text[]),
    ('qualification_evidence_projection_authorizations'::text, ARRAY[]::text[]),
    ('qualification_plan_authorities'::text, ARRAY[]::text[]),
    ('qualification_plan_identity_reservations'::text, ARRAY[]::text[]),
    ('qualification_receipt_v3_requests'::text, ARRAY[]::text[]),
    ('qualification_receipt_v3_observations'::text, ARRAY[]::text[]),
    ('qualification_receipt_v3_receipts'::text, ARRAY[]::text[]),
    ('canonical_review_approval_receipts'::text, ARRAY[]::text[]),
    ('schema_migrations'::text, ARRAY['SELECT']::text[])
),
protected_table_acl_facts AS (
  SELECT
    count(relation.oid)::integer AS relation_count,
    count(relation.oid) FILTER (
      WHERE coalesce((
        SELECT array_agg(DISTINCT table_acl.privilege_type ORDER BY table_acl.privilege_type)
        FROM pg_catalog.aclexplode(coalesce(
          relation.relacl,
          pg_catalog.acldefault('r', relation.relowner)
        )) AS table_acl
        WHERE table_acl.grantee = stable.application_oid
      ), ARRAY[]::text[]) = expected.allowed_application_privileges
      AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          relation.relacl,
          pg_catalog.acldefault('r', relation.relowner)
        )) AS table_acl
        WHERE table_acl.grantee = stable.application_oid
          AND table_acl.is_grantable
      )
    )::integer AS application_exact_acl_count
  FROM expected_protected_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
),
schema_relation_acl_facts AS (
  SELECT
    count(relation.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          relation.relacl,
          pg_catalog.acldefault(
            CASE WHEN relation.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END,
            relation.relowner
          )
        )) AS relation_acl
        JOIN session_reachable_roles AS reachable
          ON reachable.role_oid = relation_acl.grantee
        WHERE reachable.role_oid <> stable.application_oid
      )
    )::integer AS reachable_non_application_acl_count,
    count(relation.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          relation.relacl,
          pg_catalog.acldefault(
            CASE WHEN relation.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END,
            relation.relowner
          )
        )) AS relation_acl
        WHERE relation_acl.grantee = 0
      )
    )::integer AS public_acl_count,
    count(relation.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          relation.relacl,
          pg_catalog.acldefault(
            CASE WHEN relation.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END,
            relation.relowner
          )
        )) AS relation_acl
        WHERE relation_acl.grantee = stable.application_oid
          AND relation_acl.is_grantable
      )
    )::integer AS application_grant_option_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
),
schema_column_acl_facts AS (
  SELECT
    count(*)::integer AS total_acl_count,
    count(*) FILTER (
      WHERE column_acl.grantee <> stable.application_oid
        AND EXISTS (
          SELECT 1
          FROM session_reachable_roles AS reachable
          WHERE reachable.role_oid = column_acl.grantee
        )
    )::integer AS reachable_non_application_acl_count,
    count(*) FILTER (WHERE column_acl.grantee = 0)::integer AS public_acl_count,
    count(*) FILTER (
      WHERE column_acl.grantee = stable.application_oid
        AND column_acl.is_grantable
    )::integer AS application_grant_option_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
  JOIN pg_catalog.pg_attribute AS attribute
    ON attribute.attrelid = relation.oid
   AND attribute.attnum > 0
   AND NOT attribute.attisdropped
   AND attribute.attacl IS NOT NULL
  CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS column_acl
),
expected_application_functions(function_name, identity_arguments) AS (
  VALUES
    ('acquire_candidate_workspace_lease'::text,
     'uuid, bigint, uuid, integer'::text),
    ('rotate_candidate_workspace_session'::text,
     'uuid, bigint, bigint, uuid'::text),
    ('update_candidate_workspace_flags'::text,
     'uuid, bigint, bigint, bigint, uuid, boolean, boolean, boolean, text, text, text'::text),
    ('freeze_candidate_workspace'::text,
     'uuid, bigint, bigint, bigint, uuid, uuid, text'::text),
    ('abandon_candidate_workspace'::text,
     'uuid, bigint, bigint, bigint, uuid, uuid, text'::text),
    ('abandon_sandbox_session_candidate'::text,
     'uuid, uuid, bigint, bigint, bigint, bigint, uuid, uuid, text, uuid'::text),
    ('complete_abandoned_sandbox_session'::text,
     'uuid, bigint, bigint, uuid'::text),
    ('acquire_repository_exact_tree_literal_index_build_claim'::text,
     'uuid, text, uuid, bigint, integer, integer, bigint, integer'::text),
    ('renew_repository_exact_tree_literal_index_build_claim'::text,
     'uuid, text, uuid, bigint, integer'::text),
    ('release_repository_exact_tree_literal_index_build_claim'::text,
     'uuid, text, uuid, bigint'::text),
    ('issue_canonical_review_approval_receipt'::text,
     'uuid'::text),
	    ('canonical_review_approval_receipt_is_exact'::text,
	     'uuid, uuid, uuid'::text),
	    ('freeze_workflow_input_authority_v1'::text,
	     'uuid, uuid, uuid, uuid, bigint, uuid, bigint, bytea, bytea, bytea, bytea, bytea, jsonb'::text),
	    ('inspect_workflow_input_authority_operation_v1'::text,
	     'uuid'::text),
	    ('resolve_workflow_input_authority_for_node_v1'::text,
	     'uuid, uuid'::text)
),
application_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (WHERE routine.prosecdef)::integer
      AS security_definer_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
    )::integer AS migration_owner_count,
	    count(routine.oid) FILTER (
	      WHERE routine.proconfig = CASE
	        WHEN expected.function_name IN (
	          'inspect_workflow_input_authority_operation_v1',
	          'resolve_workflow_input_authority_for_node_v1'
	        ) THEN ARRAY[
	          pg_catalog.format(
	            'search_path=pg_catalog, %I', schema_state.schema_name
	          )
	        ]::text[]
	        ELSE ARRAY[
	          pg_catalog.format(
	            'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
	          )
	        ]::text[]
	      END
	    )::integer AS fixed_search_path_count,
    count(routine.oid) FILTER (
      WHERE stable.application_oid IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.application_oid
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
    )::integer AS application_executable_count,
    count(routine.oid) FILTER (
      WHERE pg_catalog.has_function_privilege(current_user, routine.oid, 'EXECUTE')
    )::integer AS api_executable_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          routine.proacl,
          pg_catalog.acldefault('f', routine.proowner)
        )) AS routine_acl
        WHERE routine_acl.grantee = 0
          AND routine_acl.privilege_type = 'EXECUTE'
      )
    )::integer AS public_executable_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          routine.proacl,
          pg_catalog.acldefault('f', routine.proowner)
        )) AS routine_acl
        WHERE routine_acl.privilege_type = 'EXECUTE'
          AND routine_acl.grantee NOT IN (
            stable.application_oid, routine.proowner
          )
      )
    )::integer AS unexpected_executable_count
  FROM expected_application_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
),
expected_workflow_input_application_functions(
  function_name, identity_arguments, result_kind, volatility, uses_temporary_schema
) AS (
  VALUES
    ('freeze_workflow_input_authority_v1'::text,
     'uuid, uuid, uuid, uuid, bigint, uuid, bigint, bytea, bytea, bytea, bytea, bytea, jsonb'::text,
     'authority'::text, 'v'::"char", true),
    ('inspect_workflow_input_authority_operation_v1'::text,
     'uuid'::text, 'jsonb_set'::text, 's'::"char", false),
    ('resolve_workflow_input_authority_for_node_v1'::text,
     'uuid, uuid'::text, 'jsonb_set'::text, 's'::"char", false)
),
workflow_input_application_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND stable.application_oid IS NOT NULL
        AND workflow_input_authorities.oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND routine.provolatile = expected.volatility
        AND language.lanname = 'plpgsql'
        AND routine.prosecdef
        AND routine.proretset
        AND NOT routine.proleakproof
        AND NOT routine.proisstrict
        AND routine.proparallel = 'u'
        AND CASE expected.result_kind
          WHEN 'authority' THEN
            routine.prorettype = workflow_input_authorities.reltype
          WHEN 'jsonb_set' THEN
            routine.prorettype = 'jsonb'::pg_catalog.regtype
          ELSE false
        END
        AND routine.proconfig = CASE
          WHEN expected.uses_temporary_schema THEN ARRAY[
            pg_catalog.format(
              'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
            )
          ]::text[]
          ELSE ARRAY[
            pg_catalog.format(
              'search_path=pg_catalog, %I', schema_state.schema_name
            )
          ]::text[]
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.application_oid
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee NOT IN (
              routine.proowner, stable.application_oid
            )
        )
    )::integer AS exact_contract_count
  FROM expected_workflow_input_application_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS workflow_input_authorities
    ON workflow_input_authorities.relnamespace = schema_state.schema_oid
   AND workflow_input_authorities.relname = 'workflow_input_authorities'
   AND workflow_input_authorities.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
expected_gc_functions(function_name, identity_arguments, result_contract) AS (
  VALUES
    ('plan_repository_exact_tree_literal_index_gc'::text,
     'uuid, bigint, integer, integer, integer'::text,
     'TABLE(run_id uuid, capability_id uuid, project_id uuid, tree_hash text, publication_created_at timestamp with time zone, index_commitment text, planned_rank integer, expires_at timestamp with time zone)'::text),
    ('execute_repository_exact_tree_literal_index_gc'::text,
     'uuid'::text,
     'TABLE(receipt_id uuid, capability_id uuid, run_id uuid, project_id uuid, tree_hash text, publication_created_at timestamp with time zone, index_commitment text, outcome text, deleted_member_count integer, deleted_blob_count integer, logical_bytes_released bigint, blob_bytes_freed bigint, executed_at timestamp with time zone, idempotent boolean)'::text),
    ('inspect_repository_exact_tree_literal_index_gc_run'::text,
     'uuid'::text,
     'TABLE(run_id uuid, run_status text, planned_at timestamp with time zone, cutoff_at timestamp with time zone, keep_per_project integer, batch_size integer, capability_ttl_milliseconds integer, planned_capability_count integer, deleted_capability_count integer, protected_capability_count integer, stale_capability_count integer, expired_capability_count integer, pending_capability_count integer, logical_bytes_released bigint, blob_bytes_freed bigint)'::text),
    ('repository_exact_tree_literal_index_gc_readiness'::text,
     ''::text,
     'TABLE(ready boolean, reason text, trusted_schema text, operator_role_exists boolean, application_role_exists boolean, migration_owner_role_exists boolean, stable_group_roles_safe boolean, objects_owned_by_migration_owner boolean, operator_execute_granted boolean, application_claim_execute_granted boolean, application_schema_head_read_granted boolean, public_claim_execute_revoked boolean, public_schema_create_revoked boolean)'::text)
),
expected_internal_functions(function_name, identity_arguments) AS (
  VALUES
    ('guard_repository_exact_tree_literal_index_gc_audit_mutation'::text, ''::text),
    ('guard_repository_exact_tree_literal_index_blob_mutation'::text, ''::text),
    ('guard_repository_exact_tree_literal_index_member_mutation'::text, ''::text),
    ('guard_repository_exact_tree_literal_index_manifest_delete'::text, ''::text),
    ('guard_repository_exact_tree_literal_index_manifest_insert'::text, ''::text),
    ('guard_repository_exact_tree_literal_index_member_insert'::text, ''::text),
    ('publish_repository_exact_tree_literal_index_manifest'::text, ''::text),
    ('lock_candidate_exact_tree_literal_index_reference'::text, ''::text),
    ('validate_golden_fault_consume_result'::text, ''::text),
    ('reject_golden_fault_ledger_mutation'::text, ''::text),
    ('reject_qualification_promotion_mutation'::text, ''::text),
    ('qualification_evidence_sha256'::text, 'bytea'::text),
    ('reject_qualification_evidence_immutable_mutation'::text, ''::text),
    ('guard_qualification_evidence_head_projection'::text, ''::text),
    ('append_qualification_evidence_event'::text,
     'text, bytea, jsonb, text, bytea, jsonb'::text),
    ('qualification_plan_sha256'::text, 'bytea'::text),
    ('reject_qualification_plan_immutable_mutation'::text, ''::text),
    ('freeze_qualification_plan_authority'::text,
     'uuid, uuid, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb'::text),
    ('resolve_qualification_plan_authority'::text, 'uuid'::text),
    ('guard_qualification_evidence_plan_authority'::text, ''::text),
    ('qualification_receipt_v3_sha256'::text, 'bytea'::text),
    ('reject_qualification_receipt_v3_mutation'::text, ''::text),
    ('guard_qualification_evidence_v1_receipt_tail_history_only'::text,
     ''::text),
    ('guard_qualification_promotion_v1_new_consumption_history_only'::text,
     ''::text),
    ('start_qualification_receipt_v3_requests'::text,
     'text, bytea, jsonb, text, bytea, jsonb, text, bytea, text, bytea'::text),
    ('append_qualification_receipt_v3_observation'::text,
     'text, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, text, bytea, jsonb, text, bytea, jsonb'::text),
    ('complete_qualification_receipt_v3'::text,
     'uuid, text, text, text, text, text, text, text, text, text, bytea, jsonb, text, bytea, text, bytea, jsonb, text'::text),
    ('canonical_review_uuid_is_exact'::text, 'text'::text),
    ('canonical_review_text_is_trimmed'::text, 'text'::text),
    ('canonical_review_timestamp_is_exact'::text, 'text'::text),
    ('canonical_review_authority_hash'::text, 'text, bytea'::text),
    ('canonical_review_jsonb_bytes'::text, 'jsonb'::text),
    ('reject_canonical_review_receipt_mutation'::text, ''::text),
    ('guard_canonical_review_source_mutation'::text, ''::text),
    ('resolve_canonical_review_approval_receipt'::text,
     'uuid, uuid, text'::text),
    ('require_canonical_review_approval_receipt'::text, ''::text)
),
expected_sandbox_checkpoint_helpers(function_name, identity_arguments) AS (
  VALUES
    ('sandbox_checkpoint_is_exact'::text,
     'uuid, uuid, uuid, bigint, bigint, bigint, bigint, text, uuid, text, text, text'::text)
),
expected_model_governance_functions(function_name, identity_arguments, contract_kind) AS (
  VALUES
    ('reject_model_governance_immutable_mutation'::text, ''::text, 'guard'::text),
    ('append_model_governance_activation'::text,
     'bigint, text, uuid, text, text, uuid, text, text, text, text, text, bigint, bigint, text, text, text, text, text, text, text, timestamp with time zone'::text,
     'append'::text),
    ('append_model_governance_genesis'::text,
     'uuid, text, text, uuid, text, text, text, text, text, bigint, bigint, text, text, text, text, text, text, text, text, text, text, bigint, text, bigint, timestamp with time zone, text'::text,
     'append'::text),
    ('observe_model_governance_revocation_authority'::text,
     'bigint, text, bytea, jsonb'::text, 'observe'::text),
    ('observe_model_governance_trust_policy'::text,
     'text, text, bigint'::text, 'observe'::text),
    ('enforce_model_governance_activation_authority_anchor'::text,
     ''::text, 'anchor_guard'::text)
),
expected_qualification_promotion_functions(
	  function_name, identity_arguments, result_contract, language_name,
	  volatility, is_strict, parallel_safety, is_security_definer,
	  returns_set, search_path_kind, acl_kind
) AS (
	  VALUES
	    ('consume_verified_qualification_promotion'::text,
	     'uuid, text, bytea, jsonb, text, text, bytea, jsonb, uuid, uuid, text, bytea, jsonb'::text,
	     'boolean'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
	     true, false, 'catalog_schema_temp'::text, 'owner'::text),
	    ('assert_current_qualification_policy_authority_v1'::text,
	     'uuid'::text, 'SETOF qualification_policy_authorities'::text,
	     'plpgsql'::text, 'v'::"char", false, 'u'::"char", true, true,
	     'catalog_schema_temp'::text, 'owner'::text),
	    ('assert_current_workflow_input_authority_v1'::text,
	     'uuid'::text, 'SETOF jsonb'::text, 'plpgsql'::text, 'v'::"char",
	     false, 'u'::"char", true, true, 'catalog_schema_temp'::text,
	     'owner'::text),
	    ('qualification_promotion_v2_hash'::text, 'text, bytea'::text,
	     'text'::text, 'sql'::text, 'i'::"char", true, 's'::"char", false,
	     false, 'catalog'::text, 'owner'::text),
	    ('qualification_promotion_v2_timestamp'::text,
	     'timestamp with time zone'::text, 'text'::text, 'sql'::text,
	     'i'::"char", true, 's'::"char", false, false, 'catalog'::text,
	     'owner'::text),
	    ('reject_qualification_promotion_v2_mutation'::text, ''::text,
	     'trigger'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
	     false, false, 'catalog'::text, 'owner'::text),
	    ('reserve_ordinary_artifact_revision_identity_v1'::text, ''::text,
	     'trigger'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
	     true, false, 'catalog_schema_temp'::text, 'owner'::text),
	    ('qualification_promotion_v2_plan_is_exact'::text, 'uuid'::text,
	     'boolean'::text, 'plpgsql'::text, 's'::"char", false, 'u'::"char",
	     false, false, 'catalog_schema_temp'::text, 'owner'::text),
	    ('qualification_promotion_v2_store_record_is_exact'::text, 'uuid'::text,
	     'boolean'::text, 'plpgsql'::text, 's'::"char", false, 'u'::"char",
	     false, false, 'catalog_schema_temp'::text, 'owner'::text),
	    ('qualification_promotion_v2_store_bundle'::text,
	     'uuid, boolean, boolean'::text, 'jsonb'::text, 'plpgsql'::text,
	     's'::"char", false, 'u'::"char", false, false,
	     'catalog_schema_temp'::text, 'owner'::text),
	    ('consume_qualification_promotion_v2'::text,
	     'uuid, uuid, uuid, uuid, uuid'::text, 'SETOF jsonb'::text,
	     'plpgsql'::text, 'v'::"char", false, 'u'::"char", true, true,
	     'catalog_schema_temp'::text, 'promotion_operator'::text),
	    ('inspect_qualification_promotion_v2_operation'::text, 'uuid'::text,
	     'SETOF jsonb'::text, 'plpgsql'::text, 's'::"char", false,
	     'u'::"char", true, true, 'catalog_schema_temp'::text,
	     'promotion_operator'::text),
	    ('resolve_qualification_promotion_v2_handoff'::text, 'uuid'::text,
	     'SETOF jsonb'::text, 'plpgsql'::text, 's'::"char", false,
	     'u'::"char", true, true, 'catalog_schema_temp'::text, 'owner'::text),
	    ('assert_pending_qualification_promotion_v2_handoff'::text, 'uuid'::text,
	     'SETOF jsonb'::text, 'plpgsql'::text, 'v'::"char", false,
	     'u'::"char", true, true, 'catalog_schema_temp'::text, 'owner'::text),
	    ('inspect_historical_qualification_promotion_v1_operation'::text,
	     'uuid'::text, 'SETOF jsonb'::text, 'plpgsql'::text, 's'::"char",
	     false, 'u'::"char", true, true, 'catalog_schema_temp'::text,
	     'promotion_operator'::text),
	    ('resolve_qualification_input_precommit_for_promotion_v1'::text,
	     'uuid, uuid'::text, 'qualification_input_authority_rows'::text,
	     'plpgsql'::text, 'v'::"char", false, 'u'::"char", true, true,
	     'catalog_schema'::text, 'promotion_operator'::text)
),
expected_qualification_handoff_functions(
  function_name, identity_arguments, result_contract, language_name,
  volatility, is_strict, parallel_safety, is_security_definer,
  returns_set, acl_kind
) AS (
  VALUES
    ('qualification_handoff_v1_hash'::text, 'text, bytea'::text,
     'text'::text, 'sql'::text, 'i'::"char", true, 's'::"char", false,
     false, 'owner'::text),
    ('qualification_handoff_v1_timestamp'::text,
     'timestamp with time zone'::text, 'text'::text, 'sql'::text,
     'i'::"char", true, 's'::"char", false, false, 'owner'::text),
    ('reject_qualification_handoff_v1_mutation'::text, ''::text,
     'trigger'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
     true, false, 'owner'::text),
    ('enqueue_qualification_promotion_v2_handoff_v1'::text, ''::text,
     'trigger'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
     true, false, 'owner'::text),
    ('qualification_handoff_v1_quality_result'::text, 'uuid, uuid'::text,
     'jsonb'::text, 'plpgsql'::text, 's'::"char", true, 'u'::"char",
     false, false, 'owner'::text),
    ('qualification_handoff_v1_completion_is_exact'::text, 'uuid'::text,
     'boolean'::text, 'plpgsql'::text, 's'::"char", false, 'u'::"char",
     false, false, 'owner'::text),
    ('qualification_handoff_v1_completion_bundle'::text,
     'uuid, boolean, boolean'::text, 'jsonb'::text, 'plpgsql'::text,
     's'::"char", false, 'u'::"char", false, false, 'owner'::text),
    ('inspect_qualification_promotion_v2_handoff_completion'::text,
     'uuid'::text, 'SETOF jsonb'::text, 'plpgsql'::text, 's'::"char", false,
     'u'::"char", true, true, 'handoff_operator'::text),
    ('complete_qualification_promotion_v2_handoff'::text, 'uuid'::text,
     'SETOF jsonb'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
     true, true, 'handoff_operator'::text),
    ('validate_qualification_handoff_v1_closure'::text, ''::text,
     'trigger'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
     true, false, 'owner'::text)
),
expected_qualification_policy_operator_functions(
	  function_name, identity_arguments, volatility, uses_temporary_schema
) AS (
	  VALUES
	    ('issue_qualification_policy_authority_v1'::text,
	     'uuid, uuid, text, text, uuid, text, text, bigint, text, timestamp with time zone, text, text, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb'::text,
	     'v'::"char", true),
	    ('inspect_qualification_policy_operation_v1'::text,
	     'uuid'::text, 's'::"char", false),
	    ('resolve_qualification_policy_authority_v1'::text,
	     'uuid'::text, 's'::"char", false),
	    ('resolve_current_qualification_policy_authority_v1'::text,
	     'uuid, text, text'::text, 's'::"char", false)
),
expected_qualification_input_functions(
  function_name, identity_arguments, result_contract, language_name,
  volatility, is_strict, parallel_safety, is_security_definer,
  returns_set, acl_kind
) AS (
  VALUES
    ('qualification_input_precommit_hash_v1'::text, 'text, bytea'::text,
     'text'::text, 'sql'::text, 'i'::"char", true, 's'::"char", false,
     false, 'owner'::text),
    ('qualification_input_precommit_timestamp_v1'::text,
     'timestamp with time zone'::text, 'text'::text, 'sql'::text,
     'i'::"char", true, 's'::"char", false, false, 'owner'::text),
	('qualification_input_precommit_string_is_secret_free_v1'::text,
	 'text'::text, 'boolean'::text, 'sql'::text, 'i'::"char", true,
	 's'::"char", false, false, 'owner'::text),
    ('qualification_input_precommit_caller_is_v1'::text, 'text'::text,
     'boolean'::text, 'sql'::text, 's'::"char", true, 's'::"char", false,
     false, 'owner'::text),
    ('reject_qualification_input_precommit_mutation_v1'::text, ''::text,
     'trigger'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char",
     false, false, 'owner'::text),
    ('review_qualification_input_precommit_executable_binding_v1'::text,
     'text, bigint, text, text, text'::text, 'binding_rows'::text,
     'plpgsql'::text, 'v'::"char", false, 'u'::"char", true, true,
     'owner'::text),
    ('qualification_input_source_admission_is_exact_v1'::text, 'text'::text,
     'boolean'::text, 'plpgsql'::text, 's'::"char", false, 'u'::"char",
     true, false, 'owner'::text),
    ('qualification_input_credential_admission_is_exact_v1'::text, 'text'::text,
     'boolean'::text, 'plpgsql'::text, 's'::"char", false, 'u'::"char",
     true, false, 'owner'::text),
    ('admit_qualification_input_source_receipt_v1'::text,
     'text, bytea, jsonb, text, bytea, jsonb'::text, 'source_rows'::text,
     'plpgsql'::text, 'v'::"char", false, 'u'::"char", true, true,
     'source_operator'::text),
    ('admit_qualification_input_credential_receipt_v1'::text,
     'text, bytea, jsonb, text, bytea, jsonb'::text, 'credential_rows'::text,
     'plpgsql'::text, 'v'::"char", false, 'u'::"char", true, true,
     'credential_operator'::text),
    ('inspect_qualification_input_source_receipt_v1'::text, 'text'::text,
     'source_rows'::text, 'plpgsql'::text, 's'::"char", false, 'u'::"char",
     true, true, 'source_operator'::text),
    ('inspect_qualification_input_credential_receipt_v1'::text, 'text'::text,
     'credential_rows'::text, 'plpgsql'::text, 's'::"char", false,
     'u'::"char", true, true, 'credential_operator'::text),
    ('resolve_qualification_input_source_receipt_admission_v1'::text,
     'text'::text, 'source_rows'::text, 'plpgsql'::text, 's'::"char", false,
     'u'::"char", true, true, 'source_operator'::text),
    ('resolve_qualification_input_credential_receipt_admission_v1'::text,
     'text'::text, 'credential_rows'::text, 'plpgsql'::text, 's'::"char",
     false, 'u'::"char", true, true, 'credential_operator'::text),
    ('qualification_input_precommit_plan_is_exact_v1'::text, 'uuid'::text,
     'boolean'::text, 'plpgsql'::text, 's'::"char", false, 'u'::"char",
     true, false, 'owner'::text),
    ('qualification_input_precommit_authority_record_is_exact_v1'::text,
     'uuid'::text, 'boolean'::text, 'plpgsql'::text, 's'::"char", false,
     'u'::"char", true, false, 'owner'::text),
    ('issue_qualification_input_precommit_v1'::text,
     'uuid, uuid, uuid, uuid, uuid, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb'::text,
     'authority_rows'::text, 'plpgsql'::text, 'v'::"char", false,
     'u'::"char", true, true, 'input_operator'::text),
    ('inspect_qualification_input_precommit_operation_v1'::text, 'uuid'::text,
     'authority_rows'::text, 'plpgsql'::text, 's'::"char", false,
     'u'::"char", true, true, 'input_operator'::text),
    ('resolve_qualification_input_precommit_authority_v1'::text, 'uuid'::text,
     'authority_rows'::text, 'plpgsql'::text, 's'::"char", false,
     'u'::"char", true, true, 'input_operator'::text),
    ('resolve_qualification_input_precommit_for_promotion_v1'::text,
     'uuid, uuid'::text, 'authority_rows'::text, 'plpgsql'::text,
     'v'::"char", false, 'u'::"char", true, true,
     'promotion_operator'::text),
    ('enforce_qualification_input_source_admission_closure_v1'::text,
     ''::text, 'trigger'::text, 'plpgsql'::text, 'v'::"char", false,
     'u'::"char", true, false, 'owner'::text),
    ('enforce_qualification_input_credential_admission_closure_v1'::text,
     ''::text, 'trigger'::text, 'plpgsql'::text, 'v'::"char", false,
     'u'::"char", true, false, 'owner'::text),
    ('enforce_qualification_input_precommit_authority_closure_v1'::text,
     ''::text, 'trigger'::text, 'plpgsql'::text, 'v'::"char", false,
     'u'::"char", true, false, 'owner'::text),
    ('qualification_input_precommit_apply_security_v1'::text, ''::text,
     'void'::text, 'plpgsql'::text, 'v'::"char", false, 'u'::"char", false,
     false, 'owner'::text)
),
expected_credential_set_functions(function_name, identity_arguments, contract_kind) AS (
  VALUES
    ('credential_set_sha256'::text, 'bytea'::text, 'sha256'::text),
    ('reject_credential_set_immutable_mutation'::text, ''::text, 'reject'::text),
    ('guard_credential_set_head_projection'::text, ''::text, 'guard'::text),
    ('append_credential_set_event'::text,
     'text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb'::text,
     'append'::text)
),
expected_qualification_evidence_functions(
  function_name, identity_arguments, contract_kind
) AS (
  VALUES
    ('qualification_evidence_sha256'::text, 'bytea'::text, 'sha256'::text),
    ('reject_qualification_evidence_immutable_mutation'::text,
     ''::text, 'reject'::text),
    ('guard_qualification_evidence_head_projection'::text,
     ''::text, 'guard'::text),
    ('append_qualification_evidence_event'::text,
     'text, bytea, jsonb, text, bytea, jsonb'::text, 'append'::text)
),
expected_qualification_plan_functions(
  function_name, identity_arguments, contract_kind
) AS (
  VALUES
    ('qualification_plan_sha256'::text, 'bytea'::text, 'sha256'::text),
    ('reject_qualification_plan_immutable_mutation'::text,
     ''::text, 'reject'::text),
    ('freeze_qualification_plan_authority'::text,
     'uuid, uuid, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb'::text,
     'freeze'::text),
    ('resolve_qualification_plan_authority'::text,
     'uuid'::text, 'resolve'::text),
    ('guard_qualification_evidence_plan_authority'::text,
     ''::text, 'evidence_guard'::text)
),
expected_qualification_receipt_v3_functions(
  function_name, identity_arguments, result_contract, contract_kind
) AS (
  VALUES
    ('qualification_receipt_v3_sha256'::text,
     'bytea'::text, 'text'::text, 'sha256'::text),
    ('reject_qualification_receipt_v3_mutation'::text,
     ''::text, 'trigger'::text, 'reject'::text),
    ('guard_qualification_evidence_v1_receipt_tail_history_only'::text,
     ''::text, 'trigger'::text, 'evidence_guard'::text),
    ('guard_qualification_promotion_v1_new_consumption_history_only'::text,
     ''::text, 'trigger'::text, 'promotion_guard'::text),
    ('start_qualification_receipt_v3_requests'::text,
     'text, bytea, jsonb, text, bytea, jsonb, text, bytea, text, bytea'::text,
     'TABLE(request_record qualification_receipt_v3_requests, created boolean)'::text,
     'writer'::text),
    ('append_qualification_receipt_v3_observation'::text,
     'text, text, bytea, jsonb, text, bytea, jsonb, text, bytea, jsonb, text, bytea, text, bytea, jsonb, text, bytea, jsonb'::text,
     'TABLE(observation_record qualification_receipt_v3_observations, idempotent boolean)'::text,
     'writer'::text),
    ('complete_qualification_receipt_v3'::text,
     'uuid, text, text, text, text, text, text, text, text, text, bytea, jsonb, text, bytea, text, bytea, jsonb, text'::text,
     'TABLE(receipt_record qualification_receipt_v3_receipts, idempotent boolean)'::text,
     'writer'::text)
),
expected_canonical_review_functions(
  function_name, identity_types, identity_arguments, result_contract,
  return_kind, language_name, volatility, is_strict, parallel_safety,
  is_security_definer, returns_set, search_path_kind, acl_kind
) AS (
  VALUES
    ('canonical_review_uuid_is_exact'::text, 'text'::text,
     'p_value text'::text, 'boolean'::text, 'boolean'::text,
     'sql'::text, 'i'::text, true, 's'::text, false, false,
     'catalog'::text, 'owner'::text),
    ('canonical_review_text_is_trimmed'::text, 'text'::text,
     'p_value text'::text, 'boolean'::text, 'boolean'::text,
     'sql'::text, 'i'::text, true, 's'::text, false, false,
     'catalog'::text, 'owner'::text),
    ('canonical_review_timestamp_is_exact'::text, 'text'::text,
     'p_value text'::text, 'boolean'::text, 'boolean'::text,
     'plpgsql'::text, 'i'::text, true, 's'::text, false, false,
     'catalog'::text, 'owner'::text),
    ('canonical_review_authority_hash'::text, 'text, bytea'::text,
     'p_domain text, p_value bytea'::text, 'text'::text, 'text'::text,
     'sql'::text, 'i'::text, true, 's'::text, false, false,
     'catalog'::text, 'owner'::text),
    ('canonical_review_jsonb_bytes'::text, 'jsonb'::text,
     'p_value jsonb'::text, 'bytea'::text, 'bytea'::text,
     'plpgsql'::text, 'i'::text, true, 's'::text, false, false,
     'catalog_schema'::text, 'owner'::text),
    ('reject_canonical_review_receipt_mutation'::text, ''::text,
     ''::text, 'trigger'::text, 'trigger'::text,
     'plpgsql'::text, 'v'::text, false, 'u'::text, false, false,
     'catalog'::text, 'owner'::text),
    ('canonical_review_approval_receipt_record_is_exact'::text,
     'canonical_review_approval_receipts'::text,
     'p_receipt canonical_review_approval_receipts'::text,
     'boolean'::text, 'boolean'::text,
     'plpgsql'::text, 'i'::text, true, 's'::text, false, false,
     'catalog_schema'::text, 'owner'::text),
    ('guard_canonical_review_source_mutation'::text, ''::text,
     ''::text, 'trigger'::text, 'trigger'::text,
     'plpgsql'::text, 'v'::text, false, 'u'::text, false, false,
     'catalog_schema'::text, 'owner'::text),
    ('resolve_canonical_review_approval_receipt'::text,
     'uuid, uuid, text'::text,
     'p_project_id uuid, p_revision_id uuid, p_receipt_hash text'::text,
     'SETOF canonical_review_approval_receipts'::text,
     'receipt'::text, 'plpgsql'::text, 'v'::text, false, 'u'::text,
     true, true, 'catalog_schema_temp'::text, 'owner'::text),
    ('issue_canonical_review_approval_receipt'::text, 'uuid'::text,
     'p_review_request_id uuid'::text,
     'TABLE(receipt_record canonical_review_approval_receipts, created boolean)'::text,
     'record'::text, 'plpgsql'::text, 'v'::text, false, 'u'::text,
     true, true, 'catalog_schema_temp'::text, 'application'::text),
    ('canonical_review_approval_receipt_is_exact'::text,
     'uuid, uuid, uuid'::text,
     'p_project_id uuid, p_revision_id uuid, p_review_request_id uuid'::text,
     'boolean'::text, 'boolean'::text,
     'plpgsql'::text, 's'::text, false, 'u'::text, true, false,
     'catalog_schema_temp'::text, 'application'::text),
    ('require_canonical_review_approval_receipt'::text, ''::text,
     ''::text, 'trigger'::text, 'trigger'::text,
     'plpgsql'::text, 'v'::text, false, 'u'::text, true, false,
     'catalog_schema_temp'::text, 'owner'::text)
),
expected_owned_functions(function_name, identity_arguments) AS (
  SELECT function_name, identity_arguments
  FROM expected_application_functions
  UNION ALL
  SELECT function_name, identity_arguments
  FROM expected_gc_functions
  UNION ALL
  SELECT function_name, identity_arguments
  FROM expected_internal_functions
  UNION ALL
  SELECT function_name, identity_arguments
  FROM expected_sandbox_checkpoint_helpers
  UNION ALL
  SELECT function_name, identity_arguments
  FROM expected_model_governance_functions
  UNION ALL
  SELECT function_name, identity_arguments
  FROM expected_qualification_promotion_functions
  UNION ALL
	  SELECT function_name, identity_arguments
	  FROM expected_qualification_input_functions
	  WHERE function_name <> 'resolve_qualification_input_precommit_for_promotion_v1'
	  UNION ALL
	  SELECT function_name, identity_arguments
	  FROM expected_qualification_handoff_functions
	  UNION ALL
  SELECT function_name, identity_arguments
  FROM expected_credential_set_functions
),
boundary_table_owner_rows AS (
  SELECT relation.oid, relation.relowner AS owner_oid
  FROM expected_protected_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
  WHERE expected.table_name <> 'schema_migrations'
),
boundary_index_owner_rows AS (
  SELECT index_relation.oid, index_relation.relowner AS owner_oid
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS table_relation
    ON table_relation.relnamespace = schema_state.schema_oid
   AND table_relation.relname IN (
     'repository_exact_tree_literal_index_blobs',
     'repository_exact_tree_literal_index_members',
     'repository_exact_tree_literal_index_manifests',
     'repository_exact_tree_literal_index_build_claims',
     'repository_exact_tree_literal_index_gc_runs',
     'repository_exact_tree_literal_index_gc_capabilities',
     'repository_exact_tree_literal_index_gc_receipts',
     'repository_exact_tree_literal_index_gc_tombstones',
     'repository_exact_tree_literal_index_gc_tree_delete_auth',
     'repository_exact_tree_literal_index_gc_blob_delete_auth',
     'golden_fault_consume_reservations',
     'golden_fault_consume_results',
     'model_governance_activation_records',
     'model_governance_activation_heads',
     'model_governance_revocation_anchor',
     'qualification_promotion_consumptions',
     'qualification_promotion_handoffs',
     'artifact_revision_identity_reservations',
     'qualification_promotion_v2_independent_receipts',
     'qualification_promotion_v2_consumptions',
     'qualification_promotion_v2_consumption_independent_receipts',
     'qualification_promotion_v2_handoffs',
     'qualification_promotion_v2_identity_reservations',
	 'qualification_input_precommit_executable_binding_generations',
	 'qualification_input_precommit_executable_binding_heads',
	 'qualification_input_source_receipt_admissions',
	 'qualification_input_credential_receipt_admissions',
	 'qualification_input_precommit_authorities',
	     'qualification_input_precommit_identity_reservations',
	     'qualification_input_precommit_wia_reservations',
	     'qualification_input_precommit_plan_reservations',
       'qualification_promotion_v2_revision_transaction_grants',
       'qualification_promotion_v2_revision_authority_bindings',
       'qualification_promotion_v2_handoff_lineage_members',
       'qualification_promotion_v2_handoff_completions',
     'credential_set_events',
     'credential_set_operations',
     'credential_set_heads',
     'credential_set_projection_authorizations',
     'qualification_evidence_events',
     'qualification_evidence_operations',
     'qualification_evidence_heads',
     'qualification_evidence_projection_authorizations',
     'qualification_plan_authorities',
     'qualification_plan_identity_reservations',
     'qualification_receipt_v3_requests',
     'qualification_receipt_v3_observations',
     'qualification_receipt_v3_receipts',
     'canonical_review_approval_receipts',
     'review_decisions'
   )
  JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indrelid = table_relation.oid
	  JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.oid = index_catalog.indexrelid
   AND (
     table_relation.relname <> 'review_decisions'
     OR index_relation.relname = 'review_decisions_request_id_id_key'
	   )
	  UNION
	  SELECT index_relation.oid, index_relation.relowner AS owner_oid
	  FROM schema_facts AS schema_state
	  JOIN pg_catalog.pg_class AS index_relation
	    ON index_relation.relnamespace = schema_state.schema_oid
	   AND index_relation.relkind IN ('i', 'I')
	   AND index_relation.relname IN (
	     'artifact_revisions_ordinary_content_unique',
	     'artifact_revisions_promotion_handoff_unique',
	     'qualification_promotion_handoff_pending_dispatch_unique'
	   )
),
boundary_routine_owner_rows AS (
  SELECT routine.oid, routine.proowner AS owner_oid
  FROM expected_owned_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
),
owner_boundary_facts AS (
  SELECT
    schema_state.schema_oid IS NOT NULL
      AND stable.migration_owner_oid IS NOT NULL
      AND namespace.nspowner = stable.migration_owner_oid
      AS schema_owner_is_exact,
    (SELECT count(*)::integer FROM boundary_table_owner_rows) AS table_count,
    (SELECT count(*)::integer FROM boundary_table_owner_rows
      WHERE owner_oid = stable.migration_owner_oid) AS exact_table_owner_count,
    (SELECT count(*)::integer FROM boundary_index_owner_rows) AS index_count,
    (SELECT count(*)::integer FROM boundary_index_owner_rows
      WHERE owner_oid = stable.migration_owner_oid) AS exact_index_owner_count,
    (SELECT count(*)::integer FROM boundary_routine_owner_rows) AS routine_count,
    (SELECT count(*)::integer FROM boundary_routine_owner_rows
      WHERE owner_oid = stable.migration_owner_oid) AS exact_routine_owner_count,
    (SELECT count(*)::integer
      FROM pg_catalog.pg_class AS relation
      WHERE relation.relnamespace = schema_state.schema_oid
        AND relation.relkind IN ('r', 'p', 'S')) AS owned_relation_count,
    (SELECT count(*)::integer
      FROM pg_catalog.pg_class AS relation
      WHERE relation.relnamespace = schema_state.schema_oid
        AND relation.relkind IN ('r', 'p', 'S')
        AND relation.relowner = stable.migration_owner_oid)
      AS exact_owned_relation_count,
    (SELECT count(*)::integer
      FROM pg_catalog.pg_proc AS routine
      WHERE routine.pronamespace = schema_state.schema_oid
        AND routine.prosecdef) AS security_definer_count,
    (SELECT count(*)::integer
      FROM pg_catalog.pg_proc AS routine
      WHERE routine.pronamespace = schema_state.schema_oid
        AND routine.prosecdef
        AND routine.proowner = stable.migration_owner_oid)
      AS exact_security_definer_owner_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_namespace AS namespace
    ON namespace.oid = schema_state.schema_oid
),
expected_gc_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (WHERE routine.prosecdef)::integer
      AS security_definer_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
    )::integer AS migration_owner_count,
    count(routine.oid) FILTER (
      WHERE routine.proconfig = ARRAY[
        pg_catalog.format(
          'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
        )
      ]::text[]
    )::integer AS fixed_search_path_count,
    count(routine.oid) FILTER (
      WHERE pg_catalog.pg_get_function_result(routine.oid) = expected.result_contract
    )::integer AS exact_result_count,
    count(routine.oid) FILTER (
      WHERE stable.operator_oid IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.operator_oid
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
    )::integer AS operator_executable_count,
    count(routine.oid) FILTER (
      WHERE pg_catalog.has_function_privilege(current_user, routine.oid, 'EXECUTE')
    )::integer AS api_executable_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          routine.proacl,
          pg_catalog.acldefault('f', routine.proowner)
        )) AS routine_acl
        WHERE routine_acl.grantee = 0
          AND routine_acl.privilege_type = 'EXECUTE'
      )
    )::integer AS public_executable_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          routine.proacl,
          pg_catalog.acldefault('f', routine.proowner)
        )) AS routine_acl
        WHERE routine_acl.privilege_type = 'EXECUTE'
          AND routine_acl.grantee NOT IN (
            stable.operator_oid, routine.proowner
          )
      )
    )::integer AS unexpected_executable_count
  FROM expected_gc_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
),
internal_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee <> routine.proowner
        )
    )::integer AS exact_owner_acl_count
  FROM expected_internal_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
),
model_governance_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND routine.provolatile = 'v'
        AND language.lanname = 'plpgsql'
        AND CASE expected.contract_kind
          WHEN 'guard' THEN
            NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'append' THEN
            routine.prosecdef
            AND routine.proretset
            AND routine.prorettype = activation_records.reltype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
          WHEN 'observe' THEN
            routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'void'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
          WHEN 'anchor_guard' THEN
            NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
            AND EXISTS (
              SELECT 1
              FROM pg_catalog.pg_trigger AS trigger
              WHERE trigger.tgrelid = activation_records.oid
                AND trigger.tgfoid = routine.oid
                AND trigger.tgname = 'model_governance_activation_authority_anchor'
                AND NOT trigger.tgisinternal
                AND trigger.tgenabled = 'O'
                AND trigger.tgtype = 7
            )
          ELSE false
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee <> routine.proowner
        )
    )::integer AS exact_contract_count
  FROM expected_model_governance_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS activation_records
    ON activation_records.relnamespace = schema_state.schema_oid
   AND activation_records.relname = 'model_governance_activation_records'
   AND activation_records.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_promotion_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND stable.qualification_operator_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
	    AND NOT routine.proleakproof
	    AND pg_catalog.pg_get_function_result(routine.oid) = CASE expected.result_contract
	      WHEN 'qualification_input_authority_rows' THEN
	        'SETOF qualification_input_precommit_authorities'
	      ELSE expected.result_contract
	    END
	    AND language.lanname = expected.language_name
	    AND routine.provolatile = expected.volatility
	    AND routine.proisstrict = expected.is_strict
	    AND routine.proparallel = expected.parallel_safety
	    AND routine.prosecdef = expected.is_security_definer
	    AND routine.proretset = expected.returns_set
	    AND routine.proconfig = CASE expected.search_path_kind
	      WHEN 'catalog' THEN ARRAY['search_path=pg_catalog']::text[]
	      WHEN 'catalog_schema_temp' THEN ARRAY[
	        pg_catalog.format(
	          'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
	        )
	      ]::text[]
	      WHEN 'catalog_schema' THEN ARRAY[
	        pg_catalog.format(
	          'search_path=pg_catalog, %I', schema_state.schema_name
	        )
	      ]::text[]
	      ELSE NULL::text[]
	    END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
        )
	    AND (
	      (expected.acl_kind = 'owner' AND NOT EXISTS (
	        SELECT 1
	        FROM pg_catalog.aclexplode(coalesce(
	          routine.proacl,
	          pg_catalog.acldefault('f', routine.proowner)
	        )) AS routine_acl
	        WHERE routine_acl.grantee <> routine.proowner
	      ))
	      OR
	      (expected.acl_kind = 'promotion_operator'
	       AND EXISTS (
	         SELECT 1
	         FROM pg_catalog.aclexplode(coalesce(
	           routine.proacl,
	           pg_catalog.acldefault('f', routine.proowner)
	         )) AS routine_acl
	         WHERE routine_acl.grantee = stable.qualification_operator_oid
	           AND routine_acl.privilege_type = 'EXECUTE'
	           AND NOT routine_acl.is_grantable
	       )
	       AND NOT EXISTS (
	         SELECT 1
	         FROM pg_catalog.aclexplode(coalesce(
	           routine.proacl,
	           pg_catalog.acldefault('f', routine.proowner)
	         )) AS routine_acl
	         WHERE routine_acl.privilege_type <> 'EXECUTE'
	            OR routine_acl.grantee NOT IN (
	              routine.proowner, stable.qualification_operator_oid
	            )
	            OR (
	              routine_acl.grantee = stable.qualification_operator_oid
	              AND routine_acl.is_grantable
	            )
	       ))
	    )
    )::integer AS exact_contract_count
  FROM expected_qualification_promotion_functions AS expected
	CROSS JOIN schema_facts AS schema_state
	CROSS JOIN stable_role_facts AS stable
	LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_promotion_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND (
     routine.proname LIKE '%qualification\_promotion\_v2%' ESCAPE '\'
     OR routine.proname IN (
       'consume_verified_qualification_promotion',
       'assert_current_qualification_policy_authority_v1',
       'assert_current_workflow_input_authority_v1',
	       'reserve_ordinary_artifact_revision_identity_v1',
	       'inspect_historical_qualification_promotion_v1_operation',
	       'resolve_qualification_input_precommit_for_promotion_v1'
     )
   )
),
qualification_promotion_unexpected_function_acl_facts AS (
  SELECT count(*)::integer AS unexpected_operator_acl_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  JOIN pg_catalog.pg_proc AS candidate
    ON candidate.pronamespace = schema_state.schema_oid
  CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
    candidate.proacl,
    pg_catalog.acldefault('f', candidate.proowner)
  )) AS candidate_acl
  WHERE candidate_acl.grantee = stable.qualification_operator_oid
    AND candidate_acl.privilege_type = 'EXECUTE'
	    AND (
	      NOT EXISTS (
	        SELECT 1
	        FROM expected_qualification_promotion_functions AS expected
	        WHERE candidate.proname = expected.function_name
	          AND pg_catalog.oidvectortypes(candidate.proargtypes) = expected.identity_arguments
	          AND expected.acl_kind = 'promotion_operator'
	      )
	      OR candidate_acl.is_grantable
	    )
),
qualification_handoff_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND stable.qualification_handoff_operator_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proleakproof
        AND pg_catalog.pg_get_function_result(routine.oid) =
          expected.result_contract
        AND language.lanname = expected.language_name
        AND routine.provolatile = expected.volatility
        AND routine.proisstrict = expected.is_strict
        AND routine.proparallel = expected.parallel_safety
        AND routine.prosecdef = expected.is_security_definer
        AND routine.proretset = expected.returns_set
        AND routine.proconfig = ARRAY[
          pg_catalog.format(
            'search_path=pg_catalog, %I', schema_state.schema_name
          )
        ]::text[]
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
        AND (
          (expected.acl_kind = 'owner' AND NOT EXISTS (
            SELECT 1
            FROM pg_catalog.aclexplode(coalesce(
              routine.proacl,
              pg_catalog.acldefault('f', routine.proowner)
            )) AS routine_acl
            WHERE routine_acl.grantee <> routine.proowner
          ))
          OR
          (expected.acl_kind = 'handoff_operator'
            AND EXISTS (
              SELECT 1
              FROM pg_catalog.aclexplode(coalesce(
                routine.proacl,
                pg_catalog.acldefault('f', routine.proowner)
              )) AS routine_acl
              WHERE routine_acl.grantee =
                  stable.qualification_handoff_operator_oid
                AND routine_acl.privilege_type = 'EXECUTE'
                AND NOT routine_acl.is_grantable
            )
            AND NOT EXISTS (
              SELECT 1
              FROM pg_catalog.aclexplode(coalesce(
                routine.proacl,
                pg_catalog.acldefault('f', routine.proowner)
              )) AS routine_acl
              WHERE routine_acl.privilege_type <> 'EXECUTE'
                 OR routine_acl.grantee NOT IN (
                   routine.proowner,
                   stable.qualification_handoff_operator_oid
                 )
                 OR (
                   routine_acl.grantee =
                     stable.qualification_handoff_operator_oid
                   AND routine_acl.is_grantable
                 )
            )
          )
        )
    )::integer AS exact_contract_count,
    count(routine.oid) FILTER (WHERE routine.prosecdef)::integer
      AS security_definer_count
  FROM expected_qualification_handoff_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) =
     expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_handoff_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND (
     routine.proname LIKE 'qualification\_handoff\_v1\_%' ESCAPE '\'
     OR routine.proname IN (
       'reject_qualification_handoff_v1_mutation',
       'enqueue_qualification_promotion_v2_handoff_v1',
       'inspect_qualification_promotion_v2_handoff_completion',
       'complete_qualification_promotion_v2_handoff',
       'validate_qualification_handoff_v1_closure'
     )
   )
),
qualification_handoff_unexpected_function_acl_facts AS (
  SELECT count(*)::integer AS unexpected_operator_acl_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  JOIN pg_catalog.pg_proc AS candidate
    ON candidate.pronamespace = schema_state.schema_oid
  CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
    candidate.proacl,
    pg_catalog.acldefault('f', candidate.proowner)
  )) AS candidate_acl
  WHERE candidate_acl.grantee = stable.qualification_handoff_operator_oid
    AND candidate_acl.privilege_type = 'EXECUTE'
    AND (
      NOT EXISTS (
        SELECT 1
        FROM expected_qualification_handoff_functions AS expected
        WHERE candidate.proname = expected.function_name
          AND pg_catalog.oidvectortypes(candidate.proargtypes) =
            expected.identity_arguments
          AND expected.acl_kind = 'handoff_operator'
      )
      OR candidate_acl.is_grantable
    )
),
qualification_input_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proleakproof
        AND pg_catalog.pg_get_function_result(routine.oid) =
          CASE expected.result_contract
            WHEN 'binding_rows' THEN
              'SETOF qualification_input_precommit_executable_binding_generations'
            WHEN 'source_rows' THEN
              'SETOF qualification_input_source_receipt_admissions'
            WHEN 'credential_rows' THEN
              'SETOF qualification_input_credential_receipt_admissions'
            WHEN 'authority_rows' THEN
              'SETOF qualification_input_precommit_authorities'
            ELSE expected.result_contract
          END
        AND language.lanname = expected.language_name
        AND routine.provolatile = expected.volatility
        AND routine.proisstrict = expected.is_strict
        AND routine.proparallel = expected.parallel_safety
        AND routine.prosecdef = expected.is_security_definer
        AND routine.proretset = expected.returns_set
        AND routine.proconfig = ARRAY[
          pg_catalog.format(
            'search_path=pg_catalog, %I', schema_state.schema_name
          )
        ]::text[]
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
        AND (
          (expected.acl_kind = 'owner' AND NOT EXISTS (
            SELECT 1
            FROM pg_catalog.aclexplode(coalesce(
              routine.proacl,
              pg_catalog.acldefault('f', routine.proowner)
            )) AS routine_acl
            WHERE routine_acl.grantee <> routine.proowner
          ))
          OR
          (expected.acl_kind <> 'owner'
            AND CASE expected.acl_kind
              WHEN 'input_operator' THEN stable.qualification_input_precommit_operator_oid
              WHEN 'source_operator' THEN stable.qualification_source_verifier_operator_oid
              WHEN 'credential_operator' THEN stable.qualification_credential_resolver_operator_oid
              WHEN 'promotion_operator' THEN stable.qualification_operator_oid
              ELSE NULL::pg_catalog.oid
            END IS NOT NULL
            AND EXISTS (
              SELECT 1
              FROM pg_catalog.aclexplode(coalesce(
                routine.proacl,
                pg_catalog.acldefault('f', routine.proowner)
              )) AS routine_acl
              WHERE routine_acl.grantee = CASE expected.acl_kind
                WHEN 'input_operator' THEN stable.qualification_input_precommit_operator_oid
                WHEN 'source_operator' THEN stable.qualification_source_verifier_operator_oid
                WHEN 'credential_operator' THEN stable.qualification_credential_resolver_operator_oid
                WHEN 'promotion_operator' THEN stable.qualification_operator_oid
                ELSE NULL::pg_catalog.oid
              END
                AND routine_acl.privilege_type = 'EXECUTE'
                AND NOT routine_acl.is_grantable
            )
            AND NOT EXISTS (
              SELECT 1
              FROM pg_catalog.aclexplode(coalesce(
                routine.proacl,
                pg_catalog.acldefault('f', routine.proowner)
              )) AS routine_acl
              WHERE routine_acl.privilege_type <> 'EXECUTE'
                 OR routine_acl.grantee NOT IN (
                   routine.proowner,
                   CASE expected.acl_kind
                     WHEN 'input_operator' THEN stable.qualification_input_precommit_operator_oid
                     WHEN 'source_operator' THEN stable.qualification_source_verifier_operator_oid
                     WHEN 'credential_operator' THEN stable.qualification_credential_resolver_operator_oid
                     WHEN 'promotion_operator' THEN stable.qualification_operator_oid
                     ELSE NULL::pg_catalog.oid
                   END
                 )
                 OR routine_acl.is_grantable
            )
          )
        )
    )::integer AS exact_contract_count,
    count(routine.oid) FILTER (WHERE routine.prosecdef)::integer
      AS security_definer_count
  FROM expected_qualification_input_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_input_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND (
     routine.proname LIKE 'qualification\_input\_%' ESCAPE '\'
     OR routine.proname LIKE 'admit\_qualification\_input\_%' ESCAPE '\'
     OR routine.proname LIKE 'inspect\_qualification\_input\_%' ESCAPE '\'
     OR routine.proname LIKE 'resolve\_qualification\_input\_%' ESCAPE '\'
     OR routine.proname LIKE 'review\_qualification\_input\_%' ESCAPE '\'
     OR routine.proname LIKE 'reject\_qualification\_input\_%' ESCAPE '\'
     OR routine.proname LIKE 'enforce\_qualification\_input\_%' ESCAPE '\'
     OR routine.proname LIKE 'issue\_qualification\_input\_%' ESCAPE '\'
   )
),
qualification_input_unexpected_function_acl_facts AS (
  SELECT count(*)::integer AS unexpected_operator_acl_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS candidate
    ON candidate.pronamespace = schema_state.schema_oid
  CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
    candidate.proacl,
    pg_catalog.acldefault('f', candidate.proowner)
  )) AS candidate_acl
  JOIN qualification_input_operator_roles AS operator
    ON operator.role_oid = candidate_acl.grantee
  WHERE candidate_acl.privilege_type = 'EXECUTE'
    AND (
      NOT EXISTS (
        SELECT 1
        FROM expected_qualification_input_functions AS expected
        WHERE candidate.proname = expected.function_name
          AND pg_catalog.oidvectortypes(candidate.proargtypes) = expected.identity_arguments
          AND expected.acl_kind = CASE operator.operator_kind
            WHEN 'input_precommit' THEN 'input_operator'
            WHEN 'source_verifier' THEN 'source_operator'
            WHEN 'credential_resolver' THEN 'credential_operator'
          END
      )
      OR candidate_acl.is_grantable
    )
),
qualification_policy_operator_function_facts AS (
	  SELECT
	    count(routine.oid)::integer AS function_count,
	    count(routine.oid) FILTER (
	      WHERE stable.migration_owner_oid IS NOT NULL
	        AND stable.qualification_policy_operator_oid IS NOT NULL
	        AND policy_authorities.oid IS NOT NULL
	        AND routine.proowner = stable.migration_owner_oid
	        AND routine.prokind = 'f'
	        AND routine.provolatile = expected.volatility
	        AND language.lanname = 'plpgsql'
	        AND routine.prosecdef
	        AND routine.proretset
	        AND NOT routine.proleakproof
	        AND NOT routine.proisstrict
	        AND routine.proparallel = 'u'
	        AND routine.prorettype = policy_authorities.reltype
	        AND routine.proconfig = CASE
	          WHEN expected.uses_temporary_schema THEN ARRAY[
	            pg_catalog.format(
	              'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
	            )
	          ]::text[]
	          ELSE ARRAY[
	            pg_catalog.format(
	              'search_path=pg_catalog, %I', schema_state.schema_name
	            )
	          ]::text[]
	        END
	        AND EXISTS (
	          SELECT 1
	          FROM pg_catalog.aclexplode(coalesce(
	            routine.proacl,
	            pg_catalog.acldefault('f', routine.proowner)
	          )) AS routine_acl
	          WHERE routine_acl.grantee = routine.proowner
	            AND routine_acl.privilege_type = 'EXECUTE'
	        )
	        AND EXISTS (
	          SELECT 1
	          FROM pg_catalog.aclexplode(coalesce(
	            routine.proacl,
	            pg_catalog.acldefault('f', routine.proowner)
	          )) AS routine_acl
	          WHERE routine_acl.grantee = stable.qualification_policy_operator_oid
	            AND routine_acl.privilege_type = 'EXECUTE'
	            AND NOT routine_acl.is_grantable
	        )
	        AND NOT EXISTS (
	          SELECT 1
	          FROM pg_catalog.aclexplode(coalesce(
	            routine.proacl,
	            pg_catalog.acldefault('f', routine.proowner)
	          )) AS routine_acl
	          WHERE routine_acl.privilege_type = 'EXECUTE'
	            AND (
	              routine_acl.grantee NOT IN (
	                routine.proowner, stable.qualification_policy_operator_oid
	              )
	              OR (
	                routine_acl.grantee = stable.qualification_policy_operator_oid
	                AND routine_acl.is_grantable
	              )
	            )
	        )
	    )::integer AS exact_contract_count
	  FROM expected_qualification_policy_operator_functions AS expected
	  CROSS JOIN schema_facts AS schema_state
	  CROSS JOIN stable_role_facts AS stable
	  LEFT JOIN pg_catalog.pg_class AS policy_authorities
	    ON policy_authorities.relnamespace = schema_state.schema_oid
	   AND policy_authorities.relname = 'qualification_policy_authorities'
	   AND policy_authorities.relkind IN ('r', 'p')
	  LEFT JOIN pg_catalog.pg_proc AS routine
	    ON routine.pronamespace = schema_state.schema_oid
	   AND routine.proname = expected.function_name
	   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
	  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_policy_operator_unexpected_function_acl_facts AS (
	  SELECT count(*)::integer AS unexpected_operator_acl_count
	  FROM schema_facts AS schema_state
	  CROSS JOIN stable_role_facts AS stable
	  JOIN pg_catalog.pg_proc AS candidate
	    ON candidate.pronamespace = schema_state.schema_oid
	  CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(
	    candidate.proacl,
	    pg_catalog.acldefault('f', candidate.proowner)
	  )) AS candidate_acl
	  WHERE candidate_acl.grantee = stable.qualification_policy_operator_oid
	    AND candidate_acl.privilege_type = 'EXECUTE'
	    AND (
	      NOT EXISTS (
	        SELECT 1
	        FROM expected_qualification_policy_operator_functions AS expected
	        WHERE candidate.proname = expected.function_name
	          AND pg_catalog.oidvectortypes(candidate.proargtypes) = expected.identity_arguments
	      )
	      OR candidate_acl.is_grantable
	    )
),
credential_set_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proretset
        AND NOT routine.proleakproof
        AND CASE expected.contract_kind
          WHEN 'sha256' THEN
            language.lanname = 'sql'
            AND routine.provolatile = 'i'
            AND routine.proisstrict
            AND routine.proparallel = 's'
            AND NOT routine.prosecdef
            AND routine.prorettype = 'text'::pg_catalog.regtype
            AND routine.proconfig IS NULL
          WHEN 'reject' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'guard' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I', schema_state.schema_name
              )
            ]::text[]
          WHEN 'append' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND routine.prosecdef
            AND routine.prorettype = 'boolean'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
          ELSE false
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.migration_owner_oid
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_contract_count
  FROM expected_credential_set_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
credential_set_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname LIKE '%credential\_set\_%' ESCAPE '\'
),
qualification_evidence_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proretset
        AND NOT routine.proleakproof
        AND CASE expected.contract_kind
          WHEN 'sha256' THEN
            language.lanname = 'sql'
            AND routine.provolatile = 'i'
            AND routine.proisstrict
            AND routine.proparallel = 's'
            AND NOT routine.prosecdef
            AND routine.prorettype = 'text'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'reject' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'guard' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I', schema_state.schema_name
              )
            ]::text[]
          WHEN 'append' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND routine.prosecdef
            AND routine.prorettype = 'boolean'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
          ELSE false
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.migration_owner_oid
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_contract_count
  FROM expected_qualification_evidence_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_evidence_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname LIKE '%qualification\_evidence\_%' ESCAPE '\'
),
qualification_plan_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proleakproof
        AND CASE expected.contract_kind
          WHEN 'sha256' THEN
            language.lanname = 'sql'
            AND routine.provolatile = 'i'
            AND routine.proisstrict
            AND routine.proparallel = 's'
            AND NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'text'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'reject' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'freeze' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND routine.prosecdef
            AND routine.proretset
            AND routine.prorettype = authority_records.reltype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
          WHEN 'resolve' THEN
            language.lanname = 'sql'
            AND routine.provolatile = 's'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND routine.proretset
            AND routine.prorettype = authority_records.reltype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I', schema_state.schema_name
              )
            ]::text[]
          WHEN 'evidence_guard' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I', schema_state.schema_name
              )
            ]::text[]
          ELSE false
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.migration_owner_oid
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_contract_count
  FROM expected_qualification_plan_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS authority_records
    ON authority_records.relnamespace = schema_state.schema_oid
   AND authority_records.relname = 'qualification_plan_authorities'
   AND authority_records.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_plan_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND (
     routine.proname LIKE '%qualification\_plan\_%' ESCAPE '\'
     OR routine.proname = 'guard_qualification_evidence_plan_authority'
   )
),
qualification_receipt_v3_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proleakproof
        AND pg_catalog.pg_get_function_result(routine.oid) = expected.result_contract
        AND CASE expected.contract_kind
          WHEN 'sha256' THEN
            language.lanname = 'sql'
            AND routine.provolatile = 'i'
            AND routine.proisstrict
            AND routine.proparallel = 's'
            AND NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'text'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'reject' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'evidence_guard' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I', schema_state.schema_name
              )
            ]::text[]
          WHEN 'promotion_guard' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND NOT routine.prosecdef
            AND NOT routine.proretset
            AND routine.prorettype = 'trigger'::pg_catalog.regtype
            AND routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'writer' THEN
            language.lanname = 'plpgsql'
            AND routine.provolatile = 'v'
            AND NOT routine.proisstrict
            AND routine.proparallel = 'u'
            AND routine.prosecdef
            AND routine.proretset
            AND routine.prorettype = 'record'::pg_catalog.regtype
            AND routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
          ELSE false
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.migration_owner_oid
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee <> stable.migration_owner_oid
        )
    )::integer AS exact_contract_count,
    count(routine.oid) FILTER (WHERE routine.prosecdef)::integer
      AS security_definer_count
  FROM expected_qualification_receipt_v3_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
qualification_receipt_v3_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND (
     routine.proname LIKE '%qualification\_receipt\_v3%' ESCAPE '\'
     OR routine.proname IN (
       'guard_qualification_evidence_v1_receipt_tail_history_only',
       'guard_qualification_promotion_v1_new_consumption_history_only'
     )
   )
),
canonical_review_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND routine.prokind = 'f'
        AND NOT routine.proleakproof
        AND routine.pronargs = pg_catalog.cardinality(routine.proargtypes::oid[])
        AND routine.pronargdefaults = 0
        AND routine.provariadic = 0::pg_catalog.oid
        AND pg_catalog.oidvectortypes(routine.proargtypes) =
          expected.identity_types
        AND pg_catalog.pg_get_function_identity_arguments(routine.oid) =
          expected.identity_arguments
        AND pg_catalog.pg_get_function_result(routine.oid) =
          expected.result_contract
        AND language.lanname = expected.language_name
        AND routine.provolatile::text = expected.volatility
        AND routine.proisstrict = expected.is_strict
        AND routine.proparallel::text = expected.parallel_safety
        AND routine.prosecdef = expected.is_security_definer
        AND routine.proretset = expected.returns_set
        AND CASE expected.return_kind
          WHEN 'text' THEN routine.prorettype = 'text'::pg_catalog.regtype
          WHEN 'bytea' THEN routine.prorettype = 'bytea'::pg_catalog.regtype
          WHEN 'boolean' THEN routine.prorettype = 'boolean'::pg_catalog.regtype
          WHEN 'trigger' THEN routine.prorettype = 'trigger'::pg_catalog.regtype
          WHEN 'record' THEN routine.prorettype = 'record'::pg_catalog.regtype
          WHEN 'receipt' THEN routine.prorettype = receipt_relation.reltype
          ELSE false
        END
        AND CASE expected.search_path_kind
          WHEN 'catalog' THEN
            routine.proconfig = ARRAY['search_path=pg_catalog']::text[]
          WHEN 'catalog_schema' THEN
            routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I', schema_state.schema_name
              )
            ]::text[]
          WHEN 'catalog_schema_temp' THEN
            routine.proconfig = ARRAY[
              pg_catalog.format(
                'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
              )
            ]::text[]
          ELSE false
        END
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.migration_owner_oid
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
        AND (
          (expected.acl_kind = 'owner' AND NOT EXISTS (
            SELECT 1
            FROM pg_catalog.aclexplode(coalesce(
              routine.proacl,
              pg_catalog.acldefault('f', routine.proowner)
            )) AS routine_acl
            WHERE routine_acl.grantee <> stable.migration_owner_oid
               OR routine_acl.privilege_type <> 'EXECUTE'
               OR routine_acl.is_grantable
          ))
          OR
          (expected.acl_kind = 'application'
            AND stable.application_oid IS NOT NULL
            AND EXISTS (
              SELECT 1
              FROM pg_catalog.aclexplode(coalesce(
                routine.proacl,
                pg_catalog.acldefault('f', routine.proowner)
              )) AS routine_acl
              WHERE routine_acl.grantee = stable.application_oid
                AND routine_acl.privilege_type = 'EXECUTE'
                AND NOT routine_acl.is_grantable
            )
            AND NOT EXISTS (
              SELECT 1
              FROM pg_catalog.aclexplode(coalesce(
                routine.proacl,
                pg_catalog.acldefault('f', routine.proowner)
              )) AS routine_acl
              WHERE routine_acl.grantee NOT IN (
                      stable.migration_owner_oid, stable.application_oid
                    )
                 OR routine_acl.privilege_type <> 'EXECUTE'
                 OR routine_acl.is_grantable
            ))
        )
    )::integer AS exact_contract_count,
    count(routine.oid) FILTER (WHERE routine.prosecdef)::integer
      AS security_definer_count
  FROM expected_canonical_review_functions AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS receipt_relation
    ON receipt_relation.relnamespace = schema_state.schema_oid
   AND receipt_relation.relname = 'canonical_review_approval_receipts'
   AND receipt_relation.relkind IN ('r', 'p')
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_types
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
canonical_review_named_function_facts AS (
  SELECT count(*)::integer AS named_function_count
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND (
     routine.proname LIKE '%canonical\_review%' ESCAPE '\'
     OR routine.proname = 'review_request_policy_immutable'
   )
),
sandbox_checkpoint_helper_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE stable.migration_owner_oid IS NOT NULL
        AND stable.application_oid IS NOT NULL
        AND routine.proowner = stable.migration_owner_oid
        AND NOT routine.prosecdef
        AND routine.prokind = 'f'
        AND routine.prorettype = 'boolean'::pg_catalog.regtype
        AND NOT routine.proretset
        AND routine.provolatile = 's'
        AND language.lanname = 'sql'
        AND routine.proconfig = ARRAY[
          pg_catalog.format(
            'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
          )
        ]::text[]
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = routine.proowner
            AND routine_acl.privilege_type = 'EXECUTE'
        )
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.grantee = stable.application_oid
            AND routine_acl.privilege_type = 'EXECUTE'
            AND NOT routine_acl.is_grantable
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            routine.proacl,
            pg_catalog.acldefault('f', routine.proowner)
          )) AS routine_acl
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND (
              routine_acl.grantee NOT IN (
                routine.proowner, stable.application_oid
              )
              OR (
                routine_acl.grantee = stable.application_oid
                AND routine_acl.is_grantable
              )
            )
        )
    )::integer AS exact_contract_count
  FROM expected_sandbox_checkpoint_helpers AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND routine.proname = expected.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected.identity_arguments
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
schema_routine_acl_facts AS (
  SELECT
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          routine.proacl,
          pg_catalog.acldefault('f', routine.proowner)
        )) AS routine_acl
        WHERE routine_acl.grantee = 0
          AND routine_acl.privilege_type = 'EXECUTE'
      )
    )::integer AS public_executable_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          routine.proacl,
          pg_catalog.acldefault('f', routine.proowner)
        )) AS routine_acl
        JOIN session_reachable_roles AS reachable
          ON reachable.role_oid = routine_acl.grantee
        WHERE reachable.role_oid <> stable.application_oid
          AND routine_acl.privilege_type = 'EXECUTE'
      )
    )::integer AS reachable_non_application_direct_acl_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(coalesce(
          routine.proacl,
          pg_catalog.acldefault('f', routine.proowner)
        )) AS routine_acl
        WHERE routine_acl.grantee = stable.application_oid
          AND routine_acl.privilege_type = 'EXECUTE'
          AND routine_acl.is_grantable
      )
    )::integer AS application_grant_option_count,
    count(routine.oid) FILTER (
      WHERE routine.prosecdef
        AND EXISTS (
          SELECT 1
          FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_function_privilege(
            reachable.role_oid, routine.oid, 'EXECUTE'
          )
        )
    )::integer AS reachable_executable_security_definer_count,
    count(routine.oid) FILTER (
      WHERE routine.prosecdef
        AND expected_application.function_name IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_function_privilege(
            reachable.role_oid, routine.oid, 'EXECUTE'
          )
        )
    )::integer AS reachable_expected_application_definer_count,
    count(routine.oid) FILTER (
      WHERE routine.prosecdef
        AND expected_application.function_name IS NULL
        AND EXISTS (
          SELECT 1
          FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_function_privilege(
            reachable.role_oid, routine.oid, 'EXECUTE'
          )
        )
    )::integer AS reachable_unexpected_security_definer_count,
    count(routine.oid) FILTER (
      WHERE expected_gc.function_name IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_function_privilege(
            reachable.role_oid, routine.oid, 'EXECUTE'
          )
        )
    )::integer AS reachable_gc_execute_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
  LEFT JOIN expected_application_functions AS expected_application
    ON routine.proname = expected_application.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected_application.identity_arguments
  LEFT JOIN expected_gc_functions AS expected_gc
    ON routine.proname = expected_gc.function_name
   AND pg_catalog.oidvectortypes(routine.proargtypes) = expected_gc.identity_arguments
),
gc_function_facts AS (
  SELECT
    count(routine.oid)::integer AS function_count,
    count(routine.oid) FILTER (
      WHERE pg_catalog.has_function_privilege(current_user, routine.oid, 'EXECUTE')
    )::integer AS api_executable_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
   AND (
     routine.proowner = stable.operator_oid
     OR (
       stable.operator_oid IS NOT NULL
       AND EXISTS (
         SELECT 1
         FROM pg_catalog.aclexplode(coalesce(
           routine.proacl,
           pg_catalog.acldefault('f', routine.proowner)
         )) AS routine_acl
         WHERE routine_acl.grantee = stable.operator_oid
           AND routine_acl.privilege_type = 'EXECUTE'
       )
     )
     OR routine.proname LIKE '%repository\_exact\_tree\_literal\_index\_gc%' ESCAPE '\'
     OR routine.proname LIKE 'gc\_repository\_exact\_tree\_literal\_index%' ESCAPE '\'
   )
)
SELECT
  api_role.role_count,
  api_role.role_name,
  api_role.session_role_name,
  api_role.is_superuser,
  api_role.bypasses_rls,
  api_role.can_create_role,
  api_role.can_create_database,
  api_role.can_replicate,
  reachable_roles.role_count,
  reachable_roles.has_cluster_authority,
  reachable_roles.application_is_reachable,
  reachable_roles.forbidden_stable_role_is_reachable,
  reachable_roles.has_role_admin_option,
  current_database_state.database_count,
  current_database_state.owns_or_inherits_owner,
  current_database_state.can_create,
  schema_state.schema_count,
  schema_state.schema_name,
  schema_state.api_has_usage,
  schema_state.api_can_create,
  schema_acl.application_has_direct_usage,
  schema_acl.application_can_create,
  schema_acl.fault_operator_has_direct_usage,
  schema_acl.fault_operator_can_create,
	  schema_acl.qualification_operator_has_direct_usage,
	  schema_acl.qualification_operator_can_create,
	  schema_acl.qualification_policy_operator_has_direct_usage,
	  schema_acl.qualification_policy_operator_can_create,
	  schema_acl.qualification_input_precommit_operator_has_usage,
	  schema_acl.qualification_input_precommit_operator_can_create,
	  schema_acl.qualification_source_verifier_operator_has_usage,
	  schema_acl.qualification_source_verifier_operator_can_create,
	  schema_acl.qualification_credential_resolver_operator_has_usage,
	  schema_acl.qualification_credential_resolver_operator_can_create,
	  schema_acl.qualification_handoff_operator_has_usage,
	  schema_acl.qualification_handoff_operator_can_create,
  schema_owners.owned_object_count,
  stable_roles.role_count,
  stable_roles.has_unsafe_role,
  stable_roles.api_is_application_member,
  stable_roles.api_is_migration_owner_member,
  stable_roles.api_is_gc_operator_member,
  stable_roles.api_is_golden_fault_operator_member,
	  stable_roles.api_is_qualification_promotion_operator_member,
	  stable_roles.api_is_qualification_policy_operator_member,
	  stable_roles.api_is_qualification_input_precommit_operator_member,
	  stable_roles.api_is_qualification_source_verifier_operator_member,
	  stable_roles.api_is_qualification_credential_resolver_operator_member,
	  stable_roles.api_is_qualification_handoff_operator_member,
  index_tables.table_count,
  index_tables.api_owns_or_inherits_owner,
  index_tables.application_exact_direct_acl_count,
  index_tables.api_exact_acl_count,
  index_tables.public_privileged_table_count,
  gc_private_tables.table_count,
  gc_private_tables.api_privileged_table_count,
  golden_fault_tables.table_count,
  golden_fault_tables.exact_fault_operator_acl_count,
  qualification_promotion_tables.table_count,
  qualification_promotion_tables.exact_owner_only_count,
	qualification_promotion_named_tables.named_table_count,
	qualification_promotion_unexpected_tables.unexpected_operator_acl_count,
	qualification_promotion_triggers.trigger_count,
	qualification_promotion_triggers.exact_trigger_count,
	qualification_promotion_named_triggers.named_trigger_count,
	qualification_handoff_tables.table_count,
	qualification_handoff_tables.exact_owner_only_count,
	qualification_handoff_indexes.index_count,
	qualification_handoff_indexes.exact_index_count,
	qualification_handoff_named_indexes.named_index_count,
	qualification_handoff_triggers.trigger_count,
	qualification_handoff_triggers.exact_trigger_count,
	qualification_handoff_named_triggers.named_trigger_count,
	qualification_input_tables.table_count,
	qualification_input_tables.exact_owner_only_count,
	qualification_input_named_tables.named_table_count,
	qualification_input_indexes.index_count,
	qualification_input_indexes.exact_index_count,
	qualification_input_triggers.trigger_count,
	qualification_input_triggers.exact_trigger_count,
	qualification_input_named_triggers.named_trigger_count,
	  qualification_policy_data.relation_privilege_count,
	  qualification_policy_data.column_privilege_count,
	  qualification_policy_data.sequence_privilege_count,
	  qualification_policy_data.owned_relation_count,
	  qualification_input_operator_data.relation_privilege_count,
	  qualification_input_operator_data.column_privilege_count,
	  qualification_input_operator_data.sequence_privilege_count,
	  qualification_input_operator_data.owned_relation_count,
  workflow_input_authority_tables.table_count,
  workflow_input_authority_tables.exact_owner_only_count,
  workflow_input_authority_named_tables.named_table_count,
  workflow_input_authority_triggers.trigger_count,
  workflow_input_authority_triggers.exact_trigger_count,
  workflow_authority_named_triggers.workflow_input_named_trigger_count,
  workflow_execution_profile_v3_triggers.trigger_count,
  workflow_execution_profile_v3_triggers.exact_trigger_count,
  workflow_authority_named_triggers.workflow_v3_named_trigger_count,
  workflow_execution_profile_v3_hashes.exact_hash_contract_count,
  workflow_shared_legacy_triggers.trigger_count,
  workflow_shared_legacy_triggers.exact_trigger_count,
  workflow_shared_relation_triggers.total_trigger_count,
  workflow_authority_trigger_routines.function_count,
  workflow_authority_trigger_routines.exact_contract_count,
  workflow_authority_trigger_named_routines.named_function_count,
  workflow_shared_legacy_trigger_routines.function_count,
  workflow_shared_legacy_trigger_routines.exact_contract_count,
  workflow_shared_legacy_trigger_named_routines.named_function_count,
  credential_set_tables.table_count,
  credential_set_tables.exact_owner_only_count,
  credential_set_named_tables.named_table_count,
  credential_set_triggers.exact_trigger_count,
  credential_set_all_triggers.total_trigger_count,
  qualification_evidence_tables.table_count,
  qualification_evidence_tables.exact_owner_only_count,
  qualification_evidence_named_tables.named_table_count,
  qualification_evidence_triggers.exact_trigger_count,
  qualification_evidence_all_triggers.total_trigger_count,
  qualification_plan_tables.table_count,
  qualification_plan_tables.exact_owner_only_count,
  qualification_plan_named_tables.named_table_count,
  qualification_plan_indexes.index_count,
  qualification_plan_indexes.exact_contract_count,
  qualification_plan_named_indexes.named_index_count,
  qualification_plan_triggers.exact_trigger_count,
  qualification_plan_named_triggers.named_trigger_count,
  qualification_receipt_v3_tables.table_count,
  qualification_receipt_v3_tables.exact_owner_only_count,
  qualification_receipt_v3_named_tables.named_table_count,
  qualification_receipt_v3_indexes.index_count,
  qualification_receipt_v3_indexes.exact_contract_count,
  qualification_receipt_v3_named_indexes.named_index_count,
  qualification_receipt_v3_triggers.exact_trigger_count,
  qualification_receipt_v3_named_triggers.named_trigger_count,
  canonical_review_tables.table_count,
  canonical_review_tables.exact_contract_count,
  canonical_review_named_tables.named_table_count,
  canonical_review_indexes.index_count,
  canonical_review_indexes.exact_contract_count,
  canonical_review_named_indexes.named_index_count,
  canonical_review_triggers.exact_trigger_count,
  canonical_review_named_triggers.named_trigger_count,
  protected_tables.relation_count,
  protected_tables.application_exact_acl_count,
  schema_relations.reachable_non_application_acl_count,
  schema_relations.public_acl_count,
  schema_relations.application_grant_option_count,
  schema_columns.total_acl_count,
  schema_columns.reachable_non_application_acl_count,
  schema_columns.public_acl_count,
  schema_columns.application_grant_option_count,
  application_functions.function_count,
  application_functions.security_definer_count,
  application_functions.migration_owner_count,
  application_functions.fixed_search_path_count,
  application_functions.application_executable_count,
  application_functions.api_executable_count,
  application_functions.public_executable_count,
  application_functions.unexpected_executable_count,
  workflow_input_application_routines.function_count,
  workflow_input_application_routines.exact_contract_count,
  expected_gc_routines.function_count,
  expected_gc_routines.security_definer_count,
  expected_gc_routines.migration_owner_count,
  expected_gc_routines.fixed_search_path_count,
  expected_gc_routines.exact_result_count,
  expected_gc_routines.operator_executable_count,
  expected_gc_routines.api_executable_count,
  expected_gc_routines.public_executable_count,
  expected_gc_routines.unexpected_executable_count,
  internal_routines.function_count,
  internal_routines.exact_owner_acl_count,
  model_governance_routines.function_count,
  model_governance_routines.exact_contract_count,
  qualification_promotion_routines.function_count,
	qualification_promotion_routines.exact_contract_count,
	qualification_promotion_named_routines.named_function_count,
	  qualification_promotion_unexpected_routines.unexpected_operator_acl_count,
	qualification_handoff_routines.function_count,
	qualification_handoff_routines.exact_contract_count,
	qualification_handoff_named_routines.named_function_count,
	qualification_handoff_routines.security_definer_count,
	qualification_handoff_unexpected_routines.unexpected_operator_acl_count,
	  qualification_policy_operator_routines.function_count,
	  qualification_policy_operator_routines.exact_contract_count,
	  qualification_policy_operator_unexpected_routines.unexpected_operator_acl_count,
	  qualification_input_routines.function_count,
	  qualification_input_routines.exact_contract_count,
	  qualification_input_named_routines.named_function_count,
	  qualification_input_routines.security_definer_count,
	  qualification_input_unexpected_routines.unexpected_operator_acl_count,
  credential_set_routines.function_count,
  credential_set_routines.exact_contract_count,
  credential_set_named_routines.named_function_count,
  qualification_evidence_routines.function_count,
  qualification_evidence_routines.exact_contract_count,
  qualification_evidence_named_routines.named_function_count,
  qualification_plan_routines.function_count,
  qualification_plan_routines.exact_contract_count,
  qualification_plan_named_routines.named_function_count,
  qualification_receipt_v3_routines.function_count,
  qualification_receipt_v3_routines.exact_contract_count,
  qualification_receipt_v3_named_routines.named_function_count,
  qualification_receipt_v3_routines.security_definer_count,
  canonical_review_routines.function_count,
  canonical_review_routines.exact_contract_count,
  canonical_review_named_routines.named_function_count,
  canonical_review_routines.security_definer_count,
  sandbox_checkpoint_helper.function_count,
  sandbox_checkpoint_helper.exact_contract_count,
  owners.schema_owner_is_exact,
  owners.table_count,
  owners.exact_table_owner_count,
  owners.index_count,
  owners.exact_index_owner_count,
  owners.routine_count,
  owners.exact_routine_owner_count,
  owners.owned_relation_count,
  owners.exact_owned_relation_count,
  owners.security_definer_count,
  owners.exact_security_definer_owner_count,
  schema_routines.public_executable_count,
  schema_routines.reachable_non_application_direct_acl_count,
  schema_routines.application_grant_option_count,
  schema_routines.reachable_executable_security_definer_count,
  schema_routines.reachable_expected_application_definer_count,
  schema_routines.reachable_unexpected_security_definer_count,
  schema_routines.reachable_gc_execute_count,
  gc_routines.function_count,
  gc_routines.api_executable_count
FROM current_role_facts AS api_role
CROSS JOIN reachable_role_facts AS reachable_roles
CROSS JOIN database_facts AS current_database_state
CROSS JOIN schema_facts AS schema_state
CROSS JOIN schema_acl_facts AS schema_acl
CROSS JOIN schema_object_owner_facts AS schema_owners
CROSS JOIN stable_role_facts AS stable_roles
CROSS JOIN index_table_facts AS index_tables
CROSS JOIN gc_private_table_facts AS gc_private_tables
CROSS JOIN golden_fault_table_acl_facts AS golden_fault_tables
CROSS JOIN qualification_promotion_table_acl_facts AS qualification_promotion_tables
CROSS JOIN qualification_promotion_named_table_facts AS qualification_promotion_named_tables
CROSS JOIN qualification_promotion_unexpected_table_acl_facts AS qualification_promotion_unexpected_tables
CROSS JOIN qualification_promotion_trigger_facts AS qualification_promotion_triggers
CROSS JOIN qualification_promotion_named_trigger_facts AS qualification_promotion_named_triggers
CROSS JOIN qualification_handoff_table_facts AS qualification_handoff_tables
CROSS JOIN qualification_handoff_index_facts AS qualification_handoff_indexes
CROSS JOIN qualification_handoff_named_index_facts AS qualification_handoff_named_indexes
CROSS JOIN qualification_handoff_trigger_facts AS qualification_handoff_triggers
CROSS JOIN qualification_handoff_named_trigger_facts AS qualification_handoff_named_triggers
CROSS JOIN qualification_input_table_facts AS qualification_input_tables
CROSS JOIN qualification_input_named_table_facts AS qualification_input_named_tables
CROSS JOIN qualification_input_index_facts AS qualification_input_indexes
CROSS JOIN qualification_input_trigger_facts AS qualification_input_triggers
CROSS JOIN qualification_input_named_trigger_facts AS qualification_input_named_triggers
CROSS JOIN qualification_policy_data_privilege_facts AS qualification_policy_data
CROSS JOIN qualification_input_operator_data_privilege_facts AS qualification_input_operator_data
CROSS JOIN workflow_input_authority_table_facts AS workflow_input_authority_tables
CROSS JOIN workflow_input_authority_named_table_facts AS workflow_input_authority_named_tables
CROSS JOIN workflow_input_authority_trigger_facts AS workflow_input_authority_triggers
CROSS JOIN workflow_execution_profile_v3_trigger_facts AS workflow_execution_profile_v3_triggers
CROSS JOIN workflow_execution_profile_v3_hash_facts AS workflow_execution_profile_v3_hashes
CROSS JOIN workflow_shared_legacy_trigger_facts AS workflow_shared_legacy_triggers
CROSS JOIN workflow_authority_named_trigger_facts AS workflow_authority_named_triggers
CROSS JOIN workflow_shared_relation_total_trigger_facts AS workflow_shared_relation_triggers
CROSS JOIN workflow_authority_trigger_function_facts AS workflow_authority_trigger_routines
CROSS JOIN workflow_authority_trigger_named_function_facts AS workflow_authority_trigger_named_routines
CROSS JOIN workflow_shared_legacy_trigger_function_facts AS workflow_shared_legacy_trigger_routines
CROSS JOIN workflow_shared_legacy_trigger_named_function_facts AS workflow_shared_legacy_trigger_named_routines
CROSS JOIN credential_set_table_facts AS credential_set_tables
CROSS JOIN credential_set_named_table_facts AS credential_set_named_tables
CROSS JOIN credential_set_trigger_facts AS credential_set_triggers
CROSS JOIN credential_set_total_trigger_facts AS credential_set_all_triggers
CROSS JOIN qualification_evidence_table_facts AS qualification_evidence_tables
CROSS JOIN qualification_evidence_named_table_facts AS qualification_evidence_named_tables
CROSS JOIN qualification_evidence_trigger_facts AS qualification_evidence_triggers
CROSS JOIN qualification_evidence_total_trigger_facts AS qualification_evidence_all_triggers
CROSS JOIN qualification_plan_table_facts AS qualification_plan_tables
CROSS JOIN qualification_plan_named_table_facts AS qualification_plan_named_tables
CROSS JOIN qualification_plan_index_facts AS qualification_plan_indexes
CROSS JOIN qualification_plan_named_index_facts AS qualification_plan_named_indexes
CROSS JOIN qualification_plan_trigger_facts AS qualification_plan_triggers
CROSS JOIN qualification_plan_named_trigger_facts AS qualification_plan_named_triggers
CROSS JOIN qualification_receipt_v3_table_facts AS qualification_receipt_v3_tables
CROSS JOIN qualification_receipt_v3_named_table_facts AS qualification_receipt_v3_named_tables
CROSS JOIN qualification_receipt_v3_index_facts AS qualification_receipt_v3_indexes
CROSS JOIN qualification_receipt_v3_named_index_facts AS qualification_receipt_v3_named_indexes
CROSS JOIN qualification_receipt_v3_trigger_facts AS qualification_receipt_v3_triggers
CROSS JOIN qualification_receipt_v3_named_trigger_facts AS qualification_receipt_v3_named_triggers
CROSS JOIN canonical_review_table_facts AS canonical_review_tables
CROSS JOIN canonical_review_named_table_facts AS canonical_review_named_tables
CROSS JOIN canonical_review_index_facts AS canonical_review_indexes
CROSS JOIN canonical_review_named_index_facts AS canonical_review_named_indexes
CROSS JOIN canonical_review_trigger_facts AS canonical_review_triggers
CROSS JOIN canonical_review_named_trigger_facts AS canonical_review_named_triggers
CROSS JOIN protected_table_acl_facts AS protected_tables
CROSS JOIN schema_relation_acl_facts AS schema_relations
CROSS JOIN schema_column_acl_facts AS schema_columns
CROSS JOIN application_function_facts AS application_functions
CROSS JOIN workflow_input_application_function_facts AS workflow_input_application_routines
CROSS JOIN expected_gc_function_facts AS expected_gc_routines
CROSS JOIN internal_function_facts AS internal_routines
CROSS JOIN model_governance_function_facts AS model_governance_routines
CROSS JOIN qualification_promotion_function_facts AS qualification_promotion_routines
CROSS JOIN qualification_promotion_named_function_facts AS qualification_promotion_named_routines
CROSS JOIN qualification_promotion_unexpected_function_acl_facts AS qualification_promotion_unexpected_routines
CROSS JOIN qualification_handoff_function_facts AS qualification_handoff_routines
CROSS JOIN qualification_handoff_named_function_facts AS qualification_handoff_named_routines
CROSS JOIN qualification_handoff_unexpected_function_acl_facts AS qualification_handoff_unexpected_routines
CROSS JOIN qualification_policy_operator_function_facts AS qualification_policy_operator_routines
CROSS JOIN qualification_policy_operator_unexpected_function_acl_facts AS qualification_policy_operator_unexpected_routines
CROSS JOIN qualification_input_function_facts AS qualification_input_routines
CROSS JOIN qualification_input_named_function_facts AS qualification_input_named_routines
CROSS JOIN qualification_input_unexpected_function_acl_facts AS qualification_input_unexpected_routines
CROSS JOIN credential_set_function_facts AS credential_set_routines
CROSS JOIN credential_set_named_function_facts AS credential_set_named_routines
CROSS JOIN qualification_evidence_function_facts AS qualification_evidence_routines
CROSS JOIN qualification_evidence_named_function_facts AS qualification_evidence_named_routines
CROSS JOIN qualification_plan_function_facts AS qualification_plan_routines
CROSS JOIN qualification_plan_named_function_facts AS qualification_plan_named_routines
CROSS JOIN qualification_receipt_v3_function_facts AS qualification_receipt_v3_routines
CROSS JOIN qualification_receipt_v3_named_function_facts AS qualification_receipt_v3_named_routines
CROSS JOIN canonical_review_function_facts AS canonical_review_routines
CROSS JOIN canonical_review_named_function_facts AS canonical_review_named_routines
CROSS JOIN sandbox_checkpoint_helper_facts AS sandbox_checkpoint_helper
CROSS JOIN owner_boundary_facts AS owners
CROSS JOIN schema_routine_acl_facts AS schema_routines
CROSS JOIN gc_function_facts AS gc_routines`

type postgresRolePostureQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type postgresRolePostureFacts struct {
	roleCount                                                int
	roleName                                                 string
	sessionRoleName                                          string
	isSuperuser                                              bool
	bypassesRLS                                              bool
	canCreateRole                                            bool
	canCreateDatabase                                        bool
	canReplicate                                             bool
	reachableRoleCount                                       int
	hasReachableClusterAuthority                             bool
	isApplicationRoleReachable                               bool
	forbiddenStableRoleReachable                             bool
	hasReachableRoleAdminOption                              bool
	databaseCount                                            int
	ownsOrInheritsDatabaseOwner                              bool
	canCreateInDatabase                                      bool
	schemaCount                                              int
	schemaName                                               string
	apiHasSchemaUsage                                        bool
	canCreateInSchema                                        bool
	applicationHasSchemaUsage                                bool
	applicationCanCreateInSchema                             bool
	faultOperatorHasSchemaUsage                              bool
	faultOperatorCanCreateInSchema                           bool
	qualificationOperatorHasSchemaUsage                      bool
	qualificationOperatorCanCreateInSchema                   bool
	qualificationPolicyOperatorHasSchemaUsage                bool
	qualificationPolicyOperatorCanCreateInSchema             bool
	qualificationInputPrecommitOperatorHasSchemaUsage        bool
	qualificationInputPrecommitOperatorCanCreateInSchema     bool
	qualificationSourceVerifierOperatorHasSchemaUsage        bool
	qualificationSourceVerifierOperatorCanCreateInSchema     bool
	qualificationCredentialResolverOperatorHasSchemaUsage    bool
	qualificationCredentialResolverOperatorCanCreateInSchema bool
	qualificationHandoffOperatorHasSchemaUsage               bool
	qualificationHandoffOperatorCanCreateInSchema            bool
	ownedSchemaObjectCount                                   int
	stableGroupRoleCount                                     int
	stableGroupRolesUnsafe                                   bool
	isApplicationRoleMember                                  bool
	isMigrationOwnerRoleMember                               bool
	isOperatorRoleMember                                     bool
	isGoldenFaultOperatorRoleMember                          bool
	isQualificationPromotionOperatorRoleMember               bool
	isQualificationPolicyOperatorRoleMember                  bool
	isQualificationInputPrecommitOperatorRoleMember          bool
	isQualificationSourceVerifierOperatorRoleMember          bool
	isQualificationCredentialResolverOperatorRoleMember      bool
	isQualificationHandoffOperatorRoleMember                 bool
	tableCount                                               int
	ownsOrInheritsTableOwner                                 bool
	applicationExactTableACLCount                            int
	apiExactTableACLCount                                    int
	publicPrivilegedIndexTableCount                          int
	gcPrivateTableCount                                      int
	apiPrivilegedGCPrivateTableCount                         int
	goldenFaultTableCount                                    int
	exactGoldenFaultOperatorTableACLCount                    int
	qualificationPromotionTableCount                         int
	exactQualificationPromotionTableACLCount                 int
	qualificationPromotionNamedTableCount                    int
	unexpectedQualificationPromotionTableACLCount            int
	qualificationPromotionTriggerCount                       int
	exactQualificationPromotionTriggerContractCount          int
	qualificationPromotionNamedTriggerCount                  int
	qualificationHandoffTableCount                           int
	exactQualificationHandoffTableContractCount              int
	qualificationHandoffIndexCount                           int
	exactQualificationHandoffIndexContractCount              int
	qualificationHandoffNamedIndexCount                      int
	qualificationHandoffTriggerCount                         int
	exactQualificationHandoffTriggerContractCount            int
	qualificationHandoffNamedTriggerCount                    int
	qualificationInputTableCount                             int
	exactQualificationInputTableContractCount                int
	qualificationInputNamedTableCount                        int
	qualificationInputIndexCount                             int
	exactQualificationInputIndexContractCount                int
	qualificationInputTriggerCount                           int
	exactQualificationInputTriggerContractCount              int
	qualificationInputNamedTriggerCount                      int
	qualificationPolicyRelationPrivilegeCount                int
	qualificationPolicyColumnPrivilegeCount                  int
	qualificationPolicySequencePrivilegeCount                int
	qualificationPolicyOwnedRelationCount                    int
	qualificationInputOperatorRelationPrivilegeCount         int
	qualificationInputOperatorColumnPrivilegeCount           int
	qualificationInputOperatorSequencePrivilegeCount         int
	qualificationInputOperatorOwnedRelationCount             int
	workflowInputAuthorityTableCount                         int
	exactWorkflowInputAuthorityTableContractCount            int
	workflowInputAuthorityNamedTableCount                    int
	workflowInputAuthorityTriggerCount                       int
	exactWorkflowInputAuthorityTriggerContractCount          int
	workflowInputAuthorityNamedTriggerCount                  int
	workflowExecutionProfileV3TriggerCount                   int
	exactWorkflowExecutionProfileV3TriggerContractCount      int
	workflowExecutionProfileV3NamedTriggerCount              int
	workflowExecutionProfileV3ExactHashContractCount         int
	workflowSharedLegacyTriggerCount                         int
	exactWorkflowSharedLegacyTriggerContractCount            int
	workflowSharedRelationTriggerCount                       int
	workflowAuthorityTriggerFunctionCount                    int
	exactWorkflowAuthorityTriggerFunctionContractCount       int
	workflowAuthorityTriggerNamedFunctionCount               int
	workflowSharedLegacyTriggerFunctionCount                 int
	exactWorkflowSharedLegacyTriggerFunctionContractCount    int
	workflowSharedLegacyTriggerNamedFunctionCount            int
	credentialSetTableCount                                  int
	exactCredentialSetTableContractCount                     int
	credentialSetNamedTableCount                             int
	exactCredentialSetTriggerContractCount                   int
	credentialSetTriggerCount                                int
	qualificationEvidenceTableCount                          int
	exactQualificationEvidenceTableContractCount             int
	qualificationEvidenceNamedTableCount                     int
	exactQualificationEvidenceTriggerContractCount           int
	qualificationEvidenceTriggerCount                        int
	qualificationPlanTableCount                              int
	exactQualificationPlanTableContractCount                 int
	qualificationPlanNamedTableCount                         int
	qualificationPlanIndexCount                              int
	exactQualificationPlanIndexContractCount                 int
	qualificationPlanNamedIndexCount                         int
	exactQualificationPlanTriggerContractCount               int
	qualificationPlanTriggerCount                            int
	qualificationReceiptV3TableCount                         int
	exactQualificationReceiptV3TableContractCount            int
	qualificationReceiptV3NamedTableCount                    int
	qualificationReceiptV3IndexCount                         int
	exactQualificationReceiptV3IndexContractCount            int
	qualificationReceiptV3NamedIndexCount                    int
	exactQualificationReceiptV3TriggerContractCount          int
	qualificationReceiptV3TriggerCount                       int
	canonicalReviewTableCount                                int
	exactCanonicalReviewTableContractCount                   int
	canonicalReviewNamedTableCount                           int
	canonicalReviewIndexCount                                int
	exactCanonicalReviewIndexContractCount                   int
	canonicalReviewNamedIndexCount                           int
	exactCanonicalReviewTriggerContractCount                 int
	canonicalReviewTriggerCount                              int
	protectedTableCount                                      int
	applicationExactProtectedTableACLCount                   int
	reachableNonApplicationRelationACLCount                  int
	publicSchemaRelationACLCount                             int
	applicationRelationGrantOptionCount                      int
	schemaColumnACLCount                                     int
	reachableNonApplicationColumnACLCount                    int
	publicSchemaColumnACLCount                               int
	applicationColumnGrantOptionCount                        int
	applicationFunctionCount                                 int
	securityDefinerApplicationFunctionCount                  int
	migrationOwnedApplicationFunctionCount                   int
	fixedSearchPathApplicationFunctionCount                  int
	applicationBoundaryExecuteCount                          int
	apiApplicationFunctionExecuteCount                       int
	publicApplicationFunctionExecuteCount                    int
	unexpectedApplicationFunctionGranteeCount                int
	workflowInputApplicationFunctionCount                    int
	exactWorkflowInputApplicationFunctionContractCount       int
	expectedGCFunctionCount                                  int
	securityDefinerGCFunctionCount                           int
	migrationOwnedGCFunctionCount                            int
	fixedSearchPathGCFunctionCount                           int
	exactResultGCFunctionCount                               int
	operatorExpectedGCFunctionCount                          int
	apiExpectedGCFunctionCount                               int
	publicExpectedGCFunctionCount                            int
	unexpectedGCFunctionGranteeCount                         int
	internalFunctionCount                                    int
	exactOwnerACLInternalFunctionCount                       int
	modelGovernanceFunctionCount                             int
	exactModelGovernanceFunctionContractCount                int
	qualificationPromotionFunctionCount                      int
	exactQualificationPromotionFunctionContractCount         int
	qualificationPromotionNamedFunctionCount                 int
	unexpectedQualificationPromotionFunctionACLCount         int
	qualificationHandoffFunctionCount                        int
	exactQualificationHandoffFunctionContractCount           int
	qualificationHandoffNamedFunctionCount                   int
	qualificationHandoffSecurityDefinerCount                 int
	unexpectedQualificationHandoffFunctionACLCount           int
	qualificationPolicyFunctionCount                         int
	exactQualificationPolicyFunctionContractCount            int
	unexpectedQualificationPolicyFunctionACLCount            int
	qualificationInputFunctionCount                          int
	exactQualificationInputFunctionContractCount             int
	qualificationInputNamedFunctionCount                     int
	qualificationInputSecurityDefinerCount                   int
	unexpectedQualificationInputFunctionACLCount             int
	credentialSetFunctionCount                               int
	exactCredentialSetFunctionContractCount                  int
	credentialSetNamedFunctionCount                          int
	qualificationEvidenceFunctionCount                       int
	exactQualificationEvidenceFunctionContractCount          int
	qualificationEvidenceNamedFunctionCount                  int
	qualificationPlanFunctionCount                           int
	exactQualificationPlanFunctionContractCount              int
	qualificationPlanNamedFunctionCount                      int
	qualificationReceiptV3FunctionCount                      int
	exactQualificationReceiptV3FunctionContractCount         int
	qualificationReceiptV3NamedFunctionCount                 int
	qualificationReceiptV3SecurityDefinerCount               int
	canonicalReviewFunctionCount                             int
	exactCanonicalReviewFunctionContractCount                int
	canonicalReviewNamedFunctionCount                        int
	canonicalReviewSecurityDefinerCount                      int
	sandboxCheckpointHelperCount                             int
	exactSandboxCheckpointHelperContractCount                int
	schemaOwnerIsExact                                       bool
	ownedBoundaryTableCount                                  int
	exactOwnedBoundaryTableCount                             int
	ownedBoundaryIndexCount                                  int
	exactOwnedBoundaryIndexCount                             int
	ownedBoundaryRoutineCount                                int
	exactOwnedBoundaryRoutineCount                           int
	ownedRelationCount                                       int
	exactOwnedRelationCount                                  int
	securityDefinerFunctionCount                             int
	exactOwnedSecurityDefinerFunctionCount                   int
	publicSchemaRoutineExecuteCount                          int
	reachableNonApplicationRoutineACLCount                   int
	applicationRoutineGrantOptionCount                       int
	reachableExecutableSecurityDefinerCount                  int
	reachableExpectedApplicationDefinerCount                 int
	reachableUnexpectedSecurityDefinerCount                  int
	reachableGCFunctionExecuteCount                          int
	gcFunctionCount                                          int
	executableGCFunctionCount                                int
}

// VerifyPostgresAPIRolePosture prevents the API process from starting in a
// shared environment unless it is a distinct least-privilege login and every
// database-side production boundary is already present.
func VerifyPostgresAPIRolePosture(
	ctx context.Context,
	database *sql.DB,
	environment string,
) error {
	if database == nil {
		return verifyPostgresAPIRolePosture(ctx, nil, environment)
	}
	return verifyPostgresAPIRolePosture(ctx, database, environment)
}

func verifyPostgresAPIRolePosture(
	ctx context.Context,
	database postgresRolePostureQueryer,
	environment string,
) error {
	switch environment {
	case config.EnvironmentDevelopment, config.EnvironmentTest:
		return nil
	case config.EnvironmentStaging, config.EnvironmentProduction:
		// Shared environments are verified below.
	default:
		return fmt.Errorf("%w: unsupported environment %q", ErrUnsafePostgresAPIRolePosture, environment)
	}
	if database == nil {
		return fmt.Errorf("%w: database is unavailable", ErrUnsafePostgresAPIRolePosture)
	}

	facts, err := scanPostgresRolePosture(database.QueryRowContext(ctx, postgresRolePostureQuery).Scan)
	if err != nil {
		return fmt.Errorf("%w: inspect PostgreSQL catalogs: %w", ErrUnsafePostgresAPIRolePosture, err)
	}
	return validatePostgresRolePosture(facts)
}

func scanPostgresRolePosture(scan func(...any) error) (postgresRolePostureFacts, error) {
	var facts postgresRolePostureFacts
	err := scan(
		&facts.roleCount,
		&facts.roleName,
		&facts.sessionRoleName,
		&facts.isSuperuser,
		&facts.bypassesRLS,
		&facts.canCreateRole,
		&facts.canCreateDatabase,
		&facts.canReplicate,
		&facts.reachableRoleCount,
		&facts.hasReachableClusterAuthority,
		&facts.isApplicationRoleReachable,
		&facts.forbiddenStableRoleReachable,
		&facts.hasReachableRoleAdminOption,
		&facts.databaseCount,
		&facts.ownsOrInheritsDatabaseOwner,
		&facts.canCreateInDatabase,
		&facts.schemaCount,
		&facts.schemaName,
		&facts.apiHasSchemaUsage,
		&facts.canCreateInSchema,
		&facts.applicationHasSchemaUsage,
		&facts.applicationCanCreateInSchema,
		&facts.faultOperatorHasSchemaUsage,
		&facts.faultOperatorCanCreateInSchema,
		&facts.qualificationOperatorHasSchemaUsage,
		&facts.qualificationOperatorCanCreateInSchema,
		&facts.qualificationPolicyOperatorHasSchemaUsage,
		&facts.qualificationPolicyOperatorCanCreateInSchema,
		&facts.qualificationInputPrecommitOperatorHasSchemaUsage,
		&facts.qualificationInputPrecommitOperatorCanCreateInSchema,
		&facts.qualificationSourceVerifierOperatorHasSchemaUsage,
		&facts.qualificationSourceVerifierOperatorCanCreateInSchema,
		&facts.qualificationCredentialResolverOperatorHasSchemaUsage,
		&facts.qualificationCredentialResolverOperatorCanCreateInSchema,
		&facts.qualificationHandoffOperatorHasSchemaUsage,
		&facts.qualificationHandoffOperatorCanCreateInSchema,
		&facts.ownedSchemaObjectCount,
		&facts.stableGroupRoleCount,
		&facts.stableGroupRolesUnsafe,
		&facts.isApplicationRoleMember,
		&facts.isMigrationOwnerRoleMember,
		&facts.isOperatorRoleMember,
		&facts.isGoldenFaultOperatorRoleMember,
		&facts.isQualificationPromotionOperatorRoleMember,
		&facts.isQualificationPolicyOperatorRoleMember,
		&facts.isQualificationInputPrecommitOperatorRoleMember,
		&facts.isQualificationSourceVerifierOperatorRoleMember,
		&facts.isQualificationCredentialResolverOperatorRoleMember,
		&facts.isQualificationHandoffOperatorRoleMember,
		&facts.tableCount,
		&facts.ownsOrInheritsTableOwner,
		&facts.applicationExactTableACLCount,
		&facts.apiExactTableACLCount,
		&facts.publicPrivilegedIndexTableCount,
		&facts.gcPrivateTableCount,
		&facts.apiPrivilegedGCPrivateTableCount,
		&facts.goldenFaultTableCount,
		&facts.exactGoldenFaultOperatorTableACLCount,
		&facts.qualificationPromotionTableCount,
		&facts.exactQualificationPromotionTableACLCount,
		&facts.qualificationPromotionNamedTableCount,
		&facts.unexpectedQualificationPromotionTableACLCount,
		&facts.qualificationPromotionTriggerCount,
		&facts.exactQualificationPromotionTriggerContractCount,
		&facts.qualificationPromotionNamedTriggerCount,
		&facts.qualificationHandoffTableCount,
		&facts.exactQualificationHandoffTableContractCount,
		&facts.qualificationHandoffIndexCount,
		&facts.exactQualificationHandoffIndexContractCount,
		&facts.qualificationHandoffNamedIndexCount,
		&facts.qualificationHandoffTriggerCount,
		&facts.exactQualificationHandoffTriggerContractCount,
		&facts.qualificationHandoffNamedTriggerCount,
		&facts.qualificationInputTableCount,
		&facts.exactQualificationInputTableContractCount,
		&facts.qualificationInputNamedTableCount,
		&facts.qualificationInputIndexCount,
		&facts.exactQualificationInputIndexContractCount,
		&facts.qualificationInputTriggerCount,
		&facts.exactQualificationInputTriggerContractCount,
		&facts.qualificationInputNamedTriggerCount,
		&facts.qualificationPolicyRelationPrivilegeCount,
		&facts.qualificationPolicyColumnPrivilegeCount,
		&facts.qualificationPolicySequencePrivilegeCount,
		&facts.qualificationPolicyOwnedRelationCount,
		&facts.qualificationInputOperatorRelationPrivilegeCount,
		&facts.qualificationInputOperatorColumnPrivilegeCount,
		&facts.qualificationInputOperatorSequencePrivilegeCount,
		&facts.qualificationInputOperatorOwnedRelationCount,
		&facts.workflowInputAuthorityTableCount,
		&facts.exactWorkflowInputAuthorityTableContractCount,
		&facts.workflowInputAuthorityNamedTableCount,
		&facts.workflowInputAuthorityTriggerCount,
		&facts.exactWorkflowInputAuthorityTriggerContractCount,
		&facts.workflowInputAuthorityNamedTriggerCount,
		&facts.workflowExecutionProfileV3TriggerCount,
		&facts.exactWorkflowExecutionProfileV3TriggerContractCount,
		&facts.workflowExecutionProfileV3NamedTriggerCount,
		&facts.workflowExecutionProfileV3ExactHashContractCount,
		&facts.workflowSharedLegacyTriggerCount,
		&facts.exactWorkflowSharedLegacyTriggerContractCount,
		&facts.workflowSharedRelationTriggerCount,
		&facts.workflowAuthorityTriggerFunctionCount,
		&facts.exactWorkflowAuthorityTriggerFunctionContractCount,
		&facts.workflowAuthorityTriggerNamedFunctionCount,
		&facts.workflowSharedLegacyTriggerFunctionCount,
		&facts.exactWorkflowSharedLegacyTriggerFunctionContractCount,
		&facts.workflowSharedLegacyTriggerNamedFunctionCount,
		&facts.credentialSetTableCount,
		&facts.exactCredentialSetTableContractCount,
		&facts.credentialSetNamedTableCount,
		&facts.exactCredentialSetTriggerContractCount,
		&facts.credentialSetTriggerCount,
		&facts.qualificationEvidenceTableCount,
		&facts.exactQualificationEvidenceTableContractCount,
		&facts.qualificationEvidenceNamedTableCount,
		&facts.exactQualificationEvidenceTriggerContractCount,
		&facts.qualificationEvidenceTriggerCount,
		&facts.qualificationPlanTableCount,
		&facts.exactQualificationPlanTableContractCount,
		&facts.qualificationPlanNamedTableCount,
		&facts.qualificationPlanIndexCount,
		&facts.exactQualificationPlanIndexContractCount,
		&facts.qualificationPlanNamedIndexCount,
		&facts.exactQualificationPlanTriggerContractCount,
		&facts.qualificationPlanTriggerCount,
		&facts.qualificationReceiptV3TableCount,
		&facts.exactQualificationReceiptV3TableContractCount,
		&facts.qualificationReceiptV3NamedTableCount,
		&facts.qualificationReceiptV3IndexCount,
		&facts.exactQualificationReceiptV3IndexContractCount,
		&facts.qualificationReceiptV3NamedIndexCount,
		&facts.exactQualificationReceiptV3TriggerContractCount,
		&facts.qualificationReceiptV3TriggerCount,
		&facts.canonicalReviewTableCount,
		&facts.exactCanonicalReviewTableContractCount,
		&facts.canonicalReviewNamedTableCount,
		&facts.canonicalReviewIndexCount,
		&facts.exactCanonicalReviewIndexContractCount,
		&facts.canonicalReviewNamedIndexCount,
		&facts.exactCanonicalReviewTriggerContractCount,
		&facts.canonicalReviewTriggerCount,
		&facts.protectedTableCount,
		&facts.applicationExactProtectedTableACLCount,
		&facts.reachableNonApplicationRelationACLCount,
		&facts.publicSchemaRelationACLCount,
		&facts.applicationRelationGrantOptionCount,
		&facts.schemaColumnACLCount,
		&facts.reachableNonApplicationColumnACLCount,
		&facts.publicSchemaColumnACLCount,
		&facts.applicationColumnGrantOptionCount,
		&facts.applicationFunctionCount,
		&facts.securityDefinerApplicationFunctionCount,
		&facts.migrationOwnedApplicationFunctionCount,
		&facts.fixedSearchPathApplicationFunctionCount,
		&facts.applicationBoundaryExecuteCount,
		&facts.apiApplicationFunctionExecuteCount,
		&facts.publicApplicationFunctionExecuteCount,
		&facts.unexpectedApplicationFunctionGranteeCount,
		&facts.workflowInputApplicationFunctionCount,
		&facts.exactWorkflowInputApplicationFunctionContractCount,
		&facts.expectedGCFunctionCount,
		&facts.securityDefinerGCFunctionCount,
		&facts.migrationOwnedGCFunctionCount,
		&facts.fixedSearchPathGCFunctionCount,
		&facts.exactResultGCFunctionCount,
		&facts.operatorExpectedGCFunctionCount,
		&facts.apiExpectedGCFunctionCount,
		&facts.publicExpectedGCFunctionCount,
		&facts.unexpectedGCFunctionGranteeCount,
		&facts.internalFunctionCount,
		&facts.exactOwnerACLInternalFunctionCount,
		&facts.modelGovernanceFunctionCount,
		&facts.exactModelGovernanceFunctionContractCount,
		&facts.qualificationPromotionFunctionCount,
		&facts.exactQualificationPromotionFunctionContractCount,
		&facts.qualificationPromotionNamedFunctionCount,
		&facts.unexpectedQualificationPromotionFunctionACLCount,
		&facts.qualificationHandoffFunctionCount,
		&facts.exactQualificationHandoffFunctionContractCount,
		&facts.qualificationHandoffNamedFunctionCount,
		&facts.qualificationHandoffSecurityDefinerCount,
		&facts.unexpectedQualificationHandoffFunctionACLCount,
		&facts.qualificationPolicyFunctionCount,
		&facts.exactQualificationPolicyFunctionContractCount,
		&facts.unexpectedQualificationPolicyFunctionACLCount,
		&facts.qualificationInputFunctionCount,
		&facts.exactQualificationInputFunctionContractCount,
		&facts.qualificationInputNamedFunctionCount,
		&facts.qualificationInputSecurityDefinerCount,
		&facts.unexpectedQualificationInputFunctionACLCount,
		&facts.credentialSetFunctionCount,
		&facts.exactCredentialSetFunctionContractCount,
		&facts.credentialSetNamedFunctionCount,
		&facts.qualificationEvidenceFunctionCount,
		&facts.exactQualificationEvidenceFunctionContractCount,
		&facts.qualificationEvidenceNamedFunctionCount,
		&facts.qualificationPlanFunctionCount,
		&facts.exactQualificationPlanFunctionContractCount,
		&facts.qualificationPlanNamedFunctionCount,
		&facts.qualificationReceiptV3FunctionCount,
		&facts.exactQualificationReceiptV3FunctionContractCount,
		&facts.qualificationReceiptV3NamedFunctionCount,
		&facts.qualificationReceiptV3SecurityDefinerCount,
		&facts.canonicalReviewFunctionCount,
		&facts.exactCanonicalReviewFunctionContractCount,
		&facts.canonicalReviewNamedFunctionCount,
		&facts.canonicalReviewSecurityDefinerCount,
		&facts.sandboxCheckpointHelperCount,
		&facts.exactSandboxCheckpointHelperContractCount,
		&facts.schemaOwnerIsExact,
		&facts.ownedBoundaryTableCount,
		&facts.exactOwnedBoundaryTableCount,
		&facts.ownedBoundaryIndexCount,
		&facts.exactOwnedBoundaryIndexCount,
		&facts.ownedBoundaryRoutineCount,
		&facts.exactOwnedBoundaryRoutineCount,
		&facts.ownedRelationCount,
		&facts.exactOwnedRelationCount,
		&facts.securityDefinerFunctionCount,
		&facts.exactOwnedSecurityDefinerFunctionCount,
		&facts.publicSchemaRoutineExecuteCount,
		&facts.reachableNonApplicationRoutineACLCount,
		&facts.applicationRoutineGrantOptionCount,
		&facts.reachableExecutableSecurityDefinerCount,
		&facts.reachableExpectedApplicationDefinerCount,
		&facts.reachableUnexpectedSecurityDefinerCount,
		&facts.reachableGCFunctionExecuteCount,
		&facts.gcFunctionCount,
		&facts.executableGCFunctionCount,
	)
	return facts, err
}

func validatePostgresRolePosture(facts postgresRolePostureFacts) error {
	violations := make([]string, 0, 32)
	if facts.roleCount != 1 || strings.TrimSpace(facts.roleName) == "" {
		violations = append(violations, "current API role is absent or ambiguous in pg_roles")
	}
	if facts.sessionRoleName != facts.roleName {
		violations = append(violations, "session role differs from current API role")
	}
	if facts.isSuperuser {
		violations = append(violations, "current API role is a superuser")
	}
	if facts.bypassesRLS {
		violations = append(violations, "current API role bypasses row-level security")
	}
	if facts.canCreateRole {
		violations = append(violations, "current API role can create roles")
	}
	if facts.canCreateDatabase {
		violations = append(violations, "current API role can create databases")
	}
	if facts.canReplicate {
		violations = append(violations, "current API role has replication authority")
	}
	if facts.hasReachableClusterAuthority {
		violations = append(violations, "current API login can assume a role with cluster-level authority")
	}
	if facts.reachableRoleCount < 1 || !facts.isApplicationRoleReachable {
		violations = append(violations, "session API login cannot reach the application group through an INHERIT or SET membership path")
	}
	if facts.forbiddenStableRoleReachable {
		violations = append(violations, "session API login can reach a migration, maintenance, qualification, Promotion, Policy, or input-authority operator role")
	}
	if facts.hasReachableRoleAdminOption {
		violations = append(violations, "session API login has ADMIN OPTION on a reachable role membership")
	}
	if facts.databaseCount != 1 {
		violations = append(violations, "current database is absent or ambiguous in pg_database")
	}
	if facts.ownsOrInheritsDatabaseOwner {
		violations = append(violations, "current API role owns or inherits the current database owner")
	}
	if facts.canCreateInDatabase {
		violations = append(violations, "current API role can create schemas in the current database")
	}
	if facts.schemaCount != 1 || strings.TrimSpace(facts.schemaName) == "" {
		violations = append(violations, "current schema is absent or ambiguous in pg_namespace")
	}
	if !facts.apiHasSchemaUsage || !facts.applicationHasSchemaUsage {
		violations = append(violations, "application group and API require USAGE on the trusted schema")
	}
	if !facts.faultOperatorHasSchemaUsage {
		violations = append(violations, "Golden fault operator requires direct non-grantable USAGE on the trusted schema")
	}
	if !facts.qualificationOperatorHasSchemaUsage {
		violations = append(violations, "qualification promotion operator requires direct non-grantable USAGE on the trusted schema")
	}
	if !facts.qualificationPolicyOperatorHasSchemaUsage {
		violations = append(violations, "qualification policy operator requires direct non-grantable USAGE on the trusted schema")
	}
	if !facts.qualificationInputPrecommitOperatorHasSchemaUsage ||
		!facts.qualificationSourceVerifierOperatorHasSchemaUsage ||
		!facts.qualificationCredentialResolverOperatorHasSchemaUsage {
		violations = append(violations, "all three qualification input-authority operators require trusted-schema USAGE")
	}
	if !facts.qualificationHandoffOperatorHasSchemaUsage {
		violations = append(violations, "qualification handoff operator requires trusted-schema USAGE")
	}
	if facts.canCreateInSchema || facts.applicationCanCreateInSchema {
		violations = append(violations, "application group or API can create objects in the trusted schema")
	}
	if facts.faultOperatorCanCreateInSchema {
		violations = append(violations, "Golden fault operator can create objects in the trusted schema")
	}
	if facts.qualificationOperatorCanCreateInSchema {
		violations = append(violations, "qualification promotion operator can create objects in the trusted schema")
	}
	if facts.qualificationPolicyOperatorCanCreateInSchema {
		violations = append(violations, "qualification policy operator can create objects in the trusted schema")
	}
	if facts.qualificationInputPrecommitOperatorCanCreateInSchema ||
		facts.qualificationSourceVerifierOperatorCanCreateInSchema ||
		facts.qualificationCredentialResolverOperatorCanCreateInSchema {
		violations = append(violations, "qualification input-authority operators can create objects in the trusted schema")
	}
	if facts.qualificationHandoffOperatorCanCreateInSchema {
		violations = append(violations, "qualification handoff operator can create objects in the trusted schema")
	}
	if facts.ownedSchemaObjectCount != 0 {
		violations = append(violations, "current API role owns or inherits objects in the trusted schema")
	}
	if facts.stableGroupRoleCount != 10 {
		violations = append(violations, "stable application, migration, maintenance, qualification, Policy, Promotion, three input-authority, and Handoff group roles are incomplete")
	}
	if facts.stableGroupRolesUnsafe {
		violations = append(violations, "stable group roles must be isolated NOLOGIN roles without reachable cluster-level authority")
	}
	if !facts.isApplicationRoleMember {
		violations = append(violations, "current API role is not an application group member")
	}
	if facts.isMigrationOwnerRoleMember {
		violations = append(violations, "current API role is a migration-owner group member")
	}
	if facts.isOperatorRoleMember {
		violations = append(violations, "current API role is a repository index GC operator member")
	}
	if facts.isGoldenFaultOperatorRoleMember {
		violations = append(violations, "current API role is a Golden fault operator member")
	}
	if facts.isQualificationPromotionOperatorRoleMember {
		violations = append(violations, "current API role is a qualification promotion operator member")
	}
	if facts.isQualificationPolicyOperatorRoleMember {
		violations = append(violations, "current API role is a qualification policy operator member")
	}
	if facts.isQualificationInputPrecommitOperatorRoleMember ||
		facts.isQualificationSourceVerifierOperatorRoleMember ||
		facts.isQualificationCredentialResolverOperatorRoleMember {
		violations = append(violations, "current API role is a qualification input-authority operator member")
	}
	if facts.isQualificationHandoffOperatorRoleMember {
		violations = append(violations, "current API role is a qualification handoff operator member")
	}
	if facts.tableCount != postgresExpectedRepositoryIndexTables {
		violations = append(violations, "exact-tree index table catalog is incomplete")
	}
	if facts.ownsOrInheritsTableOwner {
		violations = append(violations, "current API role owns or inherits an exact-tree index table owner")
	}
	if facts.applicationExactTableACLCount != postgresExpectedRepositoryIndexTables ||
		facts.apiExactTableACLCount != postgresExpectedRepositoryIndexTables {
		violations = append(violations, "application or API exact-tree index table privileges exceed or miss the exact contract")
	}
	if facts.publicPrivilegedIndexTableCount != 0 {
		violations = append(violations, "PUBLIC can access exact-tree index tables")
	}
	if facts.gcPrivateTableCount != postgresExpectedRepositoryGCPrivateTables {
		violations = append(violations, "repository index GC private table catalog is incomplete")
	}
	if facts.apiPrivilegedGCPrivateTableCount != 0 {
		violations = append(violations, "current API role can access repository index GC private tables")
	}
	if facts.goldenFaultTableCount != postgresExpectedGoldenFaultTables ||
		facts.exactGoldenFaultOperatorTableACLCount != postgresExpectedGoldenFaultTables {
		violations = append(violations, "Golden fault consume tables or their dedicated operator ACL contract are incomplete")
	}
	if facts.qualificationPromotionTableCount != postgresExpectedQualificationPromotionTables ||
		facts.exactQualificationPromotionTableACLCount != postgresExpectedQualificationPromotionTables ||
		facts.qualificationPromotionNamedTableCount != postgresExpectedQualificationPromotionNamedTables ||
		facts.unexpectedQualificationPromotionTableACLCount != 0 {
		violations = append(violations, "Qualification Promotion v2, Handoff extension, and historical v1 tables must be the exact migration-owner-only boundary without Promotion-operator data ACLs")
	}
	if facts.qualificationPromotionTriggerCount != postgresExpectedQualificationPromotionTriggers ||
		facts.exactQualificationPromotionTriggerContractCount != postgresExpectedQualificationPromotionTriggers ||
		facts.qualificationPromotionNamedTriggerCount != postgresExpectedQualificationPromotionNamedTriggers {
		violations = append(violations, "Qualification Promotion v2 identity-reservation and append-only trigger contracts have drifted")
	}
	if facts.qualificationHandoffTableCount != postgresExpectedQualificationHandoffTables ||
		facts.exactQualificationHandoffTableContractCount != postgresExpectedQualificationHandoffTables {
		violations = append(violations, "Qualification Handoff tables must be the exact four-table migration-owner-only extension with frozen transaction lineage")
	}
	if facts.qualificationHandoffIndexCount != postgresExpectedQualificationHandoffIndexes ||
		facts.exactQualificationHandoffIndexContractCount != postgresExpectedQualificationHandoffIndexes ||
		facts.qualificationHandoffNamedIndexCount != postgresExpectedQualificationHandoffIndexes {
		violations = append(violations, "Qualification Handoff indexes must be the exact nineteen-index valid migration-owner catalog")
	}
	if facts.qualificationHandoffTriggerCount != postgresExpectedQualificationHandoffTriggers ||
		facts.exactQualificationHandoffTriggerContractCount != postgresExpectedQualificationHandoffTriggers ||
		facts.qualificationHandoffNamedTriggerCount != postgresExpectedQualificationHandoffTriggers {
		violations = append(violations, "Qualification Handoff immutable, dispatch, and deferred closure trigger contracts have drifted")
	}
	if facts.qualificationInputTableCount != postgresExpectedQualificationInputTables ||
		facts.exactQualificationInputTableContractCount != postgresExpectedQualificationInputTables ||
		facts.qualificationInputNamedTableCount != postgresExpectedQualificationInputTables {
		violations = append(violations, "Qualification Input Precommit tables must be the exact eight-table migration-owner-only boundary")
	}
	if facts.qualificationInputIndexCount != postgresExpectedQualificationInputIndexes ||
		facts.exactQualificationInputIndexContractCount != postgresExpectedQualificationInputIndexes {
		violations = append(violations, "Qualification Input Precommit index catalog must be the exact valid migration-owner set")
	}
	if facts.qualificationInputTriggerCount != postgresExpectedQualificationInputTriggers ||
		facts.exactQualificationInputTriggerContractCount != postgresExpectedQualificationInputTriggers ||
		facts.qualificationInputNamedTriggerCount != postgresExpectedQualificationInputTriggers {
		violations = append(violations, "Qualification Input Precommit seven immutable, one head no-removal, and three deferred closure triggers have drifted")
	}
	if facts.qualificationPolicyRelationPrivilegeCount != 0 ||
		facts.qualificationPolicyColumnPrivilegeCount != 0 ||
		facts.qualificationPolicySequencePrivilegeCount != 0 ||
		facts.qualificationPolicyOwnedRelationCount != 0 {
		violations = append(violations, "qualification policy operator can access or own trusted-schema data objects")
	}
	if facts.qualificationInputOperatorRelationPrivilegeCount != 0 ||
		facts.qualificationInputOperatorColumnPrivilegeCount != 0 ||
		facts.qualificationInputOperatorSequencePrivilegeCount != 0 ||
		facts.qualificationInputOperatorOwnedRelationCount != 0 {
		violations = append(violations, "qualification input-authority or Handoff operators can access or own trusted-schema data objects")
	}
	if facts.workflowInputAuthorityTableCount != postgresExpectedWorkflowInputAuthorityTables ||
		facts.exactWorkflowInputAuthorityTableContractCount != postgresExpectedWorkflowInputAuthorityTables ||
		facts.workflowInputAuthorityNamedTableCount != postgresExpectedWorkflowInputAuthorityTables {
		violations = append(violations, "Qualification Policy and Workflow Input authority tables must be the exact ten-table migration-owner-only boundary without non-owner ACLs")
	}
	if facts.workflowInputAuthorityTriggerCount != postgresExpectedWorkflowInputAuthorityTriggers ||
		facts.exactWorkflowInputAuthorityTriggerContractCount != postgresExpectedWorkflowInputAuthorityTriggers ||
		facts.workflowInputAuthorityNamedTriggerCount != postgresExpectedWorkflowInputAuthorityTriggers {
		violations = append(violations, "Qualification Policy and Workflow Input authority immutability, activation-event identity, and deferred closure trigger contracts have drifted")
	}
	if facts.workflowExecutionProfileV3TriggerCount != postgresExpectedWorkflowExecutionProfileV3Triggers ||
		facts.exactWorkflowExecutionProfileV3TriggerContractCount != postgresExpectedWorkflowExecutionProfileV3Triggers ||
		facts.workflowExecutionProfileV3NamedTriggerCount != postgresExpectedWorkflowExecutionProfileV3Triggers {
		violations = append(violations, "workflow-engine/v3 definition, run, external-qualification gate, and deferred closure trigger contracts have drifted")
	}
	if facts.workflowExecutionProfileV3ExactHashContractCount != postgresExpectedWorkflowExecutionProfileV3HashContracts {
		violations = append(violations, "workflow-engine/v3 descriptor hash function or Workflow Input constraint contracts have drifted")
	}
	if facts.workflowSharedLegacyTriggerCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		facts.exactWorkflowSharedLegacyTriggerContractCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		facts.workflowSharedRelationTriggerCount != postgresExpectedWorkflowSharedRelationTriggers {
		violations = append(violations, "shared workflow definition, run, node, and event relation trigger allowlist or legacy trigger contracts have drifted")
	}
	if facts.workflowAuthorityTriggerFunctionCount != postgresExpectedWorkflowAuthorityTriggerFunctions ||
		facts.exactWorkflowAuthorityTriggerFunctionContractCount != postgresExpectedWorkflowAuthorityTriggerFunctions ||
		facts.workflowAuthorityTriggerNamedFunctionCount != postgresExpectedWorkflowAuthorityTriggerFunctions {
		violations = append(violations, "Qualification Policy, Workflow Input, and workflow-engine/v3 trigger function owner, ACL, signature, language, security, volatility, strictness, parallel-safety, or search-path contracts have drifted")
	}
	if facts.workflowSharedLegacyTriggerFunctionCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		facts.exactWorkflowSharedLegacyTriggerFunctionContractCount != postgresExpectedWorkflowSharedLegacyTriggers ||
		facts.workflowSharedLegacyTriggerNamedFunctionCount != postgresExpectedWorkflowSharedLegacyTriggers {
		violations = append(violations, "shared workflow legacy trigger function signature, language, invoker, execution-attribute, search-path, owner reachability, or ACL contracts have drifted")
	}
	if facts.credentialSetTableCount != postgresExpectedCredentialSetTables ||
		facts.exactCredentialSetTableContractCount != postgresExpectedCredentialSetTables ||
		facts.credentialSetNamedTableCount != postgresExpectedCredentialSetTables {
		violations = append(violations, "CredentialSet tables must be the exact four-table migration-owner-only boundary without non-owner ACLs")
	}
	if facts.exactCredentialSetTriggerContractCount != postgresExpectedCredentialSetTriggers ||
		facts.credentialSetTriggerCount != postgresExpectedCredentialSetTriggers {
		violations = append(violations, "CredentialSet events, operations, and heads trigger contracts are incomplete or excessive")
	}
	if facts.qualificationEvidenceTableCount != postgresExpectedQualificationEvidenceTables ||
		facts.exactQualificationEvidenceTableContractCount != postgresExpectedQualificationEvidenceTables ||
		facts.qualificationEvidenceNamedTableCount != postgresExpectedQualificationEvidenceTables {
		violations = append(violations, "Qualification Evidence tables must be the exact four-table migration-owner-only boundary without non-owner ACLs")
	}
	if facts.exactQualificationEvidenceTriggerContractCount != postgresExpectedQualificationEvidenceTriggers ||
		facts.qualificationEvidenceTriggerCount != postgresExpectedQualificationEvidenceTotalTriggers {
		violations = append(violations, "Qualification Evidence events, operations, and heads trigger contracts are incomplete or excessive")
	}
	if facts.qualificationPlanTableCount != postgresExpectedQualificationPlanTables ||
		facts.exactQualificationPlanTableContractCount != postgresExpectedQualificationPlanTables ||
		facts.qualificationPlanNamedTableCount != postgresExpectedQualificationPlanTables {
		violations = append(violations, "Qualification Plan authority tables must be the exact two-table migration-owner-only boundary without non-owner ACLs")
	}
	if facts.qualificationPlanIndexCount != postgresExpectedQualificationPlanIndexes ||
		facts.exactQualificationPlanIndexContractCount != postgresExpectedQualificationPlanIndexes ||
		facts.qualificationPlanNamedIndexCount != postgresExpectedQualificationPlanIndexes {
		violations = append(violations, "Qualification Plan authority indexes must be the exact eight-index valid migration-owner catalog")
	}
	if facts.exactQualificationPlanTriggerContractCount != postgresExpectedQualificationPlanTriggers ||
		facts.qualificationPlanTriggerCount != postgresExpectedQualificationPlanTriggers {
		violations = append(violations, "Qualification Plan immutability and Qualification Evidence authority-guard trigger contracts are incomplete or excessive")
	}
	if facts.qualificationReceiptV3TableCount != postgresExpectedQualificationReceiptV3Tables ||
		facts.exactQualificationReceiptV3TableContractCount != postgresExpectedQualificationReceiptV3Tables ||
		facts.qualificationReceiptV3NamedTableCount != postgresExpectedQualificationReceiptV3Tables {
		violations = append(violations, "Qualification Receipt v3 tables must be the exact three-table migration-owner-only boundary without non-owner ACLs")
	}
	if facts.qualificationReceiptV3IndexCount != postgresExpectedQualificationReceiptV3Indexes ||
		facts.exactQualificationReceiptV3IndexContractCount != postgresExpectedQualificationReceiptV3Indexes ||
		facts.qualificationReceiptV3NamedIndexCount != postgresExpectedQualificationReceiptV3Indexes {
		violations = append(violations, "Qualification Receipt v3 indexes must be the exact fourteen-index valid migration-owner catalog")
	}
	if facts.exactQualificationReceiptV3TriggerContractCount != postgresExpectedQualificationReceiptV3Triggers ||
		facts.qualificationReceiptV3TriggerCount != postgresExpectedQualificationReceiptV3Triggers {
		violations = append(violations, "Qualification Receipt v3 immutability and v1 history-only trigger contracts are incomplete or excessive")
	}
	if facts.canonicalReviewTableCount != postgresExpectedCanonicalReviewTables ||
		facts.exactCanonicalReviewTableContractCount != postgresExpectedCanonicalReviewTables ||
		facts.canonicalReviewNamedTableCount != postgresExpectedCanonicalReviewTables {
		violations = append(violations, "Canonical Review approval receipt table columns, persistence, owner, or owner-only ACL contract has drifted")
	}
	if facts.canonicalReviewIndexCount != postgresExpectedCanonicalReviewIndexes ||
		facts.exactCanonicalReviewIndexContractCount != postgresExpectedCanonicalReviewIndexes ||
		facts.canonicalReviewNamedIndexCount != postgresExpectedCanonicalReviewIndexes {
		violations = append(violations, "Canonical Review authority indexes, ordered keys, operator classes, collations, or constraint bindings have drifted")
	}
	if facts.exactCanonicalReviewTriggerContractCount != postgresExpectedCanonicalReviewTriggers ||
		facts.canonicalReviewTriggerCount != postgresExpectedCanonicalReviewTriggers {
		violations = append(violations, "Canonical Review ordinary or deferred constraint trigger attributes have drifted")
	}
	if facts.protectedTableCount != postgresExpectedProtectedTables ||
		facts.applicationExactProtectedTableACLCount != postgresExpectedProtectedTables {
		violations = append(violations, "application protected-table direct ACL contract is incomplete or excessive")
	}
	if facts.reachableNonApplicationRelationACLCount != 0 ||
		facts.reachableNonApplicationColumnACLCount != 0 {
		violations = append(violations, "a reachable non-application role has a direct trusted-schema relation or column ACL")
	}
	if facts.publicSchemaRelationACLCount != 0 || facts.publicSchemaColumnACLCount != 0 {
		violations = append(violations, "PUBLIC has a trusted-schema relation or column ACL")
	}
	if facts.applicationRelationGrantOptionCount != 0 ||
		facts.applicationColumnGrantOptionCount != 0 {
		violations = append(violations, "application relation or column privileges include grant option")
	}
	if facts.schemaColumnACLCount != 0 {
		violations = append(violations, "trusted schema must not contain column-level ACLs")
	}
	if facts.applicationFunctionCount != postgresExpectedApplicationFunctions ||
		facts.applicationBoundaryExecuteCount != postgresExpectedApplicationFunctions ||
		facts.apiApplicationFunctionExecuteCount != postgresExpectedApplicationFunctions {
		violations = append(violations, "application SECURITY DEFINER function execute contract is incomplete")
	}
	if facts.securityDefinerApplicationFunctionCount != postgresExpectedApplicationFunctions {
		violations = append(violations, "application mutation functions must all be SECURITY DEFINER")
	}
	if facts.migrationOwnedApplicationFunctionCount != postgresExpectedApplicationFunctions {
		violations = append(violations, "application mutation functions must be owned exactly by worksflow_migration_owner")
	}
	if facts.fixedSearchPathApplicationFunctionCount != postgresExpectedApplicationFunctions {
		violations = append(violations, "application mutation functions lack the exact trusted search_path")
	}
	if facts.publicApplicationFunctionExecuteCount != 0 {
		violations = append(violations, "PUBLIC can execute application mutation functions")
	}
	if facts.unexpectedApplicationFunctionGranteeCount != 0 {
		violations = append(violations, "application mutation functions grant EXECUTE to unexpected roles")
	}
	if facts.workflowInputApplicationFunctionCount != postgresExpectedWorkflowInputApplicationFunctions ||
		facts.exactWorkflowInputApplicationFunctionContractCount != postgresExpectedWorkflowInputApplicationFunctions {
		violations = append(violations, "Workflow Input application routine signatures, results, owner, language, volatility, strictness, parallel safety, search path, security mode, or application ACL contract have drifted")
	}
	if facts.expectedGCFunctionCount != postgresExpectedRepositoryGCFunctions {
		violations = append(violations, "repository index GC function contract is incomplete")
	}
	if facts.securityDefinerGCFunctionCount != postgresExpectedRepositoryGCFunctions {
		violations = append(violations, "repository index GC functions must all be SECURITY DEFINER")
	}
	if facts.migrationOwnedGCFunctionCount != postgresExpectedRepositoryGCFunctions {
		violations = append(violations, "repository index GC functions must be owned exactly by worksflow_migration_owner")
	}
	if facts.fixedSearchPathGCFunctionCount != postgresExpectedRepositoryGCFunctions {
		violations = append(violations, "repository index GC functions lack the exact trusted search_path")
	}
	if facts.exactResultGCFunctionCount != postgresExpectedRepositoryGCFunctions {
		violations = append(violations, "repository index GC RETURNS TABLE contract is not exact")
	}
	if facts.operatorExpectedGCFunctionCount != postgresExpectedRepositoryGCFunctions {
		violations = append(violations, "repository index GC operator lacks the exact function contract")
	}
	if facts.apiExpectedGCFunctionCount != 0 || facts.publicExpectedGCFunctionCount != 0 {
		violations = append(violations, "repository index GC functions are executable by the API or PUBLIC")
	}
	if facts.unexpectedGCFunctionGranteeCount != 0 {
		violations = append(violations, "repository index GC functions grant EXECUTE outside the operator and owner")
	}
	if facts.internalFunctionCount != postgresExpectedInternalFunctions ||
		facts.exactOwnerACLInternalFunctionCount != postgresExpectedInternalFunctions {
		violations = append(violations, "owner-only internal routines must have owner-only EXECUTE privileges")
	}
	if facts.modelGovernanceFunctionCount != postgresExpectedModelGovernanceFunctions ||
		facts.exactModelGovernanceFunctionContractCount != postgresExpectedModelGovernanceFunctions {
		violations = append(violations, "Model Governance owner-only routine signature, result, owner, search_path, security mode, or ACL contract has drifted")
	}
	if facts.qualificationPromotionFunctionCount != postgresExpectedQualificationPromotionFunctions ||
		facts.exactQualificationPromotionFunctionContractCount != postgresExpectedQualificationPromotionFunctions ||
		facts.qualificationPromotionNamedFunctionCount != postgresExpectedQualificationPromotionNamedFunctions ||
		facts.unexpectedQualificationPromotionFunctionACLCount != 0 {
		violations = append(violations, "Qualification Promotion v2 routines, revoked v1 authorities, owner, search_path, security mode, or four-function Promotion-operator ACL contract have drifted")
	}
	if facts.qualificationHandoffFunctionCount != postgresExpectedQualificationHandoffFunctions ||
		facts.exactQualificationHandoffFunctionContractCount != postgresExpectedQualificationHandoffFunctions ||
		facts.qualificationHandoffNamedFunctionCount != postgresExpectedQualificationHandoffFunctions ||
		facts.qualificationHandoffSecurityDefinerCount != postgresExpectedQualificationHandoffSecurityDefiners ||
		facts.unexpectedQualificationHandoffFunctionACLCount != 0 {
		violations = append(violations, "Qualification Handoff routine signatures, results, owner, search path, exact five-function definer boundary, or dedicated two-function Handoff-operator ACL contract have drifted")
	}
	if facts.qualificationPolicyFunctionCount != postgresExpectedQualificationPolicyFunctions ||
		facts.exactQualificationPolicyFunctionContractCount != postgresExpectedQualificationPolicyFunctions ||
		facts.unexpectedQualificationPolicyFunctionACLCount != 0 {
		violations = append(violations, "qualification policy issue, inspect, and resolve routine signatures, set results, owner, search_path, security mode, or dedicated operator ACL contract have drifted")
	}
	if facts.qualificationInputFunctionCount != postgresExpectedQualificationInputFunctions ||
		facts.exactQualificationInputFunctionContractCount != postgresExpectedQualificationInputFunctions ||
		facts.qualificationInputNamedFunctionCount != postgresExpectedQualificationInputFunctions ||
		facts.qualificationInputSecurityDefinerCount != postgresExpectedQualificationInputSecurityDefiners ||
		facts.unexpectedQualificationInputFunctionACLCount != 0 {
		violations = append(violations, "Qualification Input Precommit routines, security mode, search path, result, or exact three-operator and Promotion-resolver ACL contracts have drifted")
	}
	if facts.credentialSetFunctionCount != postgresExpectedCredentialSetFunctions ||
		facts.exactCredentialSetFunctionContractCount != postgresExpectedCredentialSetFunctions ||
		facts.credentialSetNamedFunctionCount != postgresExpectedCredentialSetFunctions {
		violations = append(violations, "CredentialSet SHA-256, reject, projection-guard, or append routine signature, result, owner, language, volatility, strictness, parallel safety, search_path, security mode, or owner-only ACL has drifted")
	}
	if facts.qualificationEvidenceFunctionCount != postgresExpectedQualificationEvidenceFunctions ||
		facts.exactQualificationEvidenceFunctionContractCount != postgresExpectedQualificationEvidenceFunctions ||
		facts.qualificationEvidenceNamedFunctionCount != postgresExpectedQualificationEvidenceNamedFunctions {
		violations = append(violations, "Qualification Evidence SHA-256, reject, projection-guard, or append routine signature, result, owner, language, volatility, strictness, parallel safety, search_path, security mode, or owner-only ACL has drifted")
	}
	if facts.qualificationPlanFunctionCount != postgresExpectedQualificationPlanFunctions ||
		facts.exactQualificationPlanFunctionContractCount != postgresExpectedQualificationPlanFunctions ||
		facts.qualificationPlanNamedFunctionCount != postgresExpectedQualificationPlanFunctions {
		violations = append(violations, "Qualification Plan SHA-256, reject, freeze, resolve, or Evidence guard routine signature, result, owner, language, volatility, strictness, parallel safety, search_path, security mode, or owner-only ACL has drifted")
	}
	if facts.qualificationReceiptV3FunctionCount != postgresExpectedQualificationReceiptV3Functions ||
		facts.exactQualificationReceiptV3FunctionContractCount != postgresExpectedQualificationReceiptV3Functions ||
		facts.qualificationReceiptV3NamedFunctionCount != postgresExpectedQualificationReceiptV3Functions ||
		facts.qualificationReceiptV3SecurityDefinerCount != postgresExpectedQualificationReceiptV3Definers {
		violations = append(violations, "Qualification Receipt v3 SHA-256, guards, writers, return types, owner, language, volatility, strictness, parallel safety, search_path, exact three-function SECURITY DEFINER boundary, or owner-only ACL has drifted")
	}
	if facts.canonicalReviewFunctionCount != postgresExpectedCanonicalReviewFunctions ||
		facts.exactCanonicalReviewFunctionContractCount != postgresExpectedCanonicalReviewFunctions ||
		facts.canonicalReviewNamedFunctionCount != postgresExpectedCanonicalReviewFunctions ||
		facts.canonicalReviewSecurityDefinerCount != postgresExpectedCanonicalReviewDefiners {
		violations = append(violations, "Canonical Review authority routine identity arguments, results, owner, ACL, language, volatility, strictness, parallel safety, search_path, or exact four-function SECURITY DEFINER boundary has drifted")
	}
	if facts.sandboxCheckpointHelperCount != postgresExpectedSandboxCheckpointHelpers ||
		facts.exactSandboxCheckpointHelperContractCount != postgresExpectedSandboxCheckpointHelpers {
		violations = append(violations, "sandbox checkpoint helper must be the exact migration-owner SQL/STABLE SECURITY INVOKER boolean contract with fixed search_path and application-only non-grantable execution")
	}
	if !facts.schemaOwnerIsExact ||
		facts.ownedBoundaryTableCount != postgresExpectedOwnedBoundaryTables ||
		facts.exactOwnedBoundaryTableCount != postgresExpectedOwnedBoundaryTables ||
		facts.ownedBoundaryIndexCount != postgresExpectedOwnedBoundaryIndexes ||
		facts.exactOwnedBoundaryIndexCount != postgresExpectedOwnedBoundaryIndexes ||
		facts.ownedBoundaryRoutineCount != postgresExpectedOwnedBoundaryRoutines ||
		facts.exactOwnedBoundaryRoutineCount != postgresExpectedOwnedBoundaryRoutines {
		violations = append(violations, "trusted schema and exact production boundary objects are not owned exactly by worksflow_migration_owner")
	}
	if facts.ownedRelationCount < 1 || facts.exactOwnedRelationCount != facts.ownedRelationCount {
		violations = append(violations, "trusted-schema tables, partitioned tables, and sequences are not solely owned by worksflow_migration_owner")
	}
	if facts.securityDefinerFunctionCount != postgresExpectedSecurityDefinerFunctions ||
		facts.exactOwnedSecurityDefinerFunctionCount != postgresExpectedSecurityDefinerFunctions {
		violations = append(violations, "trusted-schema SECURITY DEFINER functions are not the exact migration-owner set")
	}
	if facts.publicSchemaRoutineExecuteCount != 0 {
		violations = append(violations, "PUBLIC can execute trusted-schema routines")
	}
	if facts.reachableNonApplicationRoutineACLCount != 0 {
		violations = append(violations, "a reachable non-application role has a direct trusted-schema routine ACL")
	}
	if facts.applicationRoutineGrantOptionCount != 0 {
		violations = append(violations, "application routine EXECUTE includes grant option")
	}
	if facts.reachableExecutableSecurityDefinerCount != postgresExpectedApplicationFunctions ||
		facts.reachableExpectedApplicationDefinerCount != postgresExpectedApplicationFunctions ||
		facts.reachableUnexpectedSecurityDefinerCount != 0 {
		violations = append(violations, "reachable SECURITY DEFINER execution is not the exact fifteen-function application contract")
	}
	if facts.reachableGCFunctionExecuteCount != 0 {
		violations = append(violations, "a session-reachable role can execute repository index GC functions")
	}
	if facts.gcFunctionCount < facts.expectedGCFunctionCount || facts.executableGCFunctionCount < 0 ||
		facts.executableGCFunctionCount > facts.gcFunctionCount {
		violations = append(violations, "repository index GC function catalog facts are inconsistent")
	} else if facts.executableGCFunctionCount != 0 {
		violations = append(violations, "current API role can execute repository index GC functions")
	}
	if len(violations) > 0 {
		return fmt.Errorf("%w: %s", ErrUnsafePostgresAPIRolePosture, strings.Join(violations, "; "))
	}
	return nil
}
