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
	postgresApplicationRole                             = "worksflow_application"
	postgresMigrationOwnerRole                          = "worksflow_migration_owner"
	postgresRepositoryIndexGCOperatorRole               = "worksflow_repository_index_gc_operator"
	postgresGoldenFaultOperatorRole                     = "worksflow_golden_fault_operator"
	postgresQualificationPromotionOperatorRole          = "worksflow_qualification_promotion_operator"
	postgresExpectedRepositoryGCFunctions               = 4
	postgresExpectedApplicationFunctions                = 10
	postgresExpectedRepositoryIndexTables               = 4
	postgresExpectedRepositoryGCPrivateTables           = 6
	postgresExpectedGoldenFaultTables                   = 2
	postgresExpectedQualificationPromotionTables        = 2
	postgresExpectedQualificationPromotionFunctions     = 1
	postgresExpectedCredentialSetTables                 = 4
	postgresExpectedCredentialSetFunctions              = 4
	postgresExpectedCredentialSetTriggers               = 3
	postgresExpectedQualificationEvidenceTables         = 4
	postgresExpectedQualificationEvidenceFunctions      = 4
	postgresExpectedQualificationEvidenceTriggers       = 3
	postgresExpectedQualificationEvidenceNamedFunctions = 5
	postgresExpectedQualificationEvidenceTotalTriggers  = 4
	postgresExpectedQualificationPlanTables             = 2
	postgresExpectedQualificationPlanIndexes            = 8
	postgresExpectedQualificationPlanFunctions          = 5
	postgresExpectedQualificationPlanTriggers           = 3
	postgresExpectedModelGovernanceFunctions            = 6
	postgresExpectedProtectedTables                     = 28
	postgresExpectedOwnedBoundaryTables                 = 27
	postgresExpectedOwnedBoundaryIndexes                = 67
	postgresExpectedOwnedBoundaryRoutines               = 46
	postgresExpectedInternalFunctions                   = 20
	postgresExpectedSandboxCheckpointHelpers            = 1
	postgresExpectedSecurityDefinerFunctions            = 27
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
      '` + postgresQualificationPromotionOperatorRole + `'
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
          '` + postgresQualificationPromotionOperatorRole + `'
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
    ) AS qualification_operator_oid
  FROM (VALUES
    ('` + postgresApplicationRole + `'::text),
    ('` + postgresMigrationOwnerRole + `'::text),
    ('` + postgresRepositoryIndexGCOperatorRole + `'::text),
    ('` + postgresGoldenFaultOperatorRole + `'::text),
    ('` + postgresQualificationPromotionOperatorRole + `'::text)
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
      ) AS qualification_operator_can_create
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
    ('qualification_promotion_handoffs'::text)
),
qualification_promotion_table_acl_facts AS (
  SELECT
    count(relation.oid)::integer AS table_count,
    count(relation.oid) FILTER (
      WHERE stable.qualification_operator_oid IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee = stable.qualification_operator_oid
            AND table_acl.privilege_type = 'SELECT'
            AND NOT table_acl.is_grantable
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(coalesce(
            relation.relacl,
            pg_catalog.acldefault('r', relation.relowner)
          )) AS table_acl
          WHERE table_acl.grantee NOT IN (
            relation.relowner, stable.qualification_operator_oid
          )
             OR (
               table_acl.grantee = stable.qualification_operator_oid
               AND (
                 table_acl.privilege_type <> 'SELECT'
                 OR table_acl.is_grantable
               )
             )
        )
    )::integer AS exact_operator_acl_count
  FROM expected_qualification_promotion_tables AS expected
  CROSS JOIN schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relname = expected.table_name
   AND relation.relkind IN ('r', 'p')
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
    AND (
      candidate.relname NOT IN (
        'qualification_promotion_consumptions',
        'qualification_promotion_handoffs'
      )
      OR candidate_acl.privilege_type <> 'SELECT'
      OR candidate_acl.is_grantable
    )
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
     'uuid, text, uuid, bigint'::text)
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
      WHERE routine.proconfig = ARRAY[
        pg_catalog.format(
          'search_path=pg_catalog, %I, pg_temp', schema_state.schema_name
        )
      ]::text[]
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
    ('guard_qualification_evidence_plan_authority'::text, ''::text)
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
expected_qualification_promotion_functions(function_name, identity_arguments) AS (
  VALUES
    ('consume_verified_qualification_promotion'::text,
     'uuid, text, bytea, jsonb, text, text, bytea, jsonb, uuid, uuid, text, bytea, jsonb'::text)
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
     'credential_set_events',
     'credential_set_operations',
     'credential_set_heads',
     'credential_set_projection_authorizations',
     'qualification_evidence_events',
     'qualification_evidence_operations',
     'qualification_evidence_heads',
     'qualification_evidence_projection_authorizations',
     'qualification_plan_authorities',
     'qualification_plan_identity_reservations'
   )
  JOIN pg_catalog.pg_index AS index_catalog
    ON index_catalog.indrelid = table_relation.oid
  JOIN pg_catalog.pg_class AS index_relation
    ON index_relation.oid = index_catalog.indexrelid
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
        AND routine.provolatile = 'v'
        AND language.lanname = 'plpgsql'
        AND routine.prosecdef
        AND NOT routine.proretset
        AND routine.prorettype = 'boolean'::pg_catalog.regtype
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
          WHERE routine_acl.privilege_type = 'EXECUTE'
            AND routine_acl.grantee NOT IN (
              routine.proowner, stable.qualification_operator_oid
            )
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
      candidate.proname <> 'consume_verified_qualification_promotion'
      OR pg_catalog.oidvectortypes(candidate.proargtypes) <>
        'uuid, text, bytea, jsonb, text, text, bytea, jsonb, uuid, uuid, text, bytea, jsonb'
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
  schema_owners.owned_object_count,
  stable_roles.role_count,
  stable_roles.has_unsafe_role,
  stable_roles.api_is_application_member,
  stable_roles.api_is_migration_owner_member,
  stable_roles.api_is_gc_operator_member,
  stable_roles.api_is_golden_fault_operator_member,
  stable_roles.api_is_qualification_promotion_operator_member,
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
  qualification_promotion_tables.exact_operator_acl_count,
  qualification_promotion_unexpected_tables.unexpected_operator_acl_count,
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
  qualification_promotion_unexpected_routines.unexpected_operator_acl_count,
  credential_set_routines.function_count,
  credential_set_routines.exact_contract_count,
  credential_set_named_routines.named_function_count,
  qualification_evidence_routines.function_count,
  qualification_evidence_routines.exact_contract_count,
  qualification_evidence_named_routines.named_function_count,
  qualification_plan_routines.function_count,
  qualification_plan_routines.exact_contract_count,
  qualification_plan_named_routines.named_function_count,
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
CROSS JOIN qualification_promotion_unexpected_table_acl_facts AS qualification_promotion_unexpected_tables
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
CROSS JOIN protected_table_acl_facts AS protected_tables
CROSS JOIN schema_relation_acl_facts AS schema_relations
CROSS JOIN schema_column_acl_facts AS schema_columns
CROSS JOIN application_function_facts AS application_functions
CROSS JOIN expected_gc_function_facts AS expected_gc_routines
CROSS JOIN internal_function_facts AS internal_routines
CROSS JOIN model_governance_function_facts AS model_governance_routines
CROSS JOIN qualification_promotion_function_facts AS qualification_promotion_routines
CROSS JOIN qualification_promotion_unexpected_function_acl_facts AS qualification_promotion_unexpected_routines
CROSS JOIN credential_set_function_facts AS credential_set_routines
CROSS JOIN credential_set_named_function_facts AS credential_set_named_routines
CROSS JOIN qualification_evidence_function_facts AS qualification_evidence_routines
CROSS JOIN qualification_evidence_named_function_facts AS qualification_evidence_named_routines
CROSS JOIN qualification_plan_function_facts AS qualification_plan_routines
CROSS JOIN qualification_plan_named_function_facts AS qualification_plan_named_routines
CROSS JOIN sandbox_checkpoint_helper_facts AS sandbox_checkpoint_helper
CROSS JOIN owner_boundary_facts AS owners
CROSS JOIN schema_routine_acl_facts AS schema_routines
CROSS JOIN gc_function_facts AS gc_routines`

type postgresRolePostureQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type postgresRolePostureFacts struct {
	roleCount                                        int
	roleName                                         string
	sessionRoleName                                  string
	isSuperuser                                      bool
	bypassesRLS                                      bool
	canCreateRole                                    bool
	canCreateDatabase                                bool
	canReplicate                                     bool
	reachableRoleCount                               int
	hasReachableClusterAuthority                     bool
	isApplicationRoleReachable                       bool
	forbiddenStableRoleReachable                     bool
	hasReachableRoleAdminOption                      bool
	databaseCount                                    int
	ownsOrInheritsDatabaseOwner                      bool
	canCreateInDatabase                              bool
	schemaCount                                      int
	schemaName                                       string
	apiHasSchemaUsage                                bool
	canCreateInSchema                                bool
	applicationHasSchemaUsage                        bool
	applicationCanCreateInSchema                     bool
	faultOperatorHasSchemaUsage                      bool
	faultOperatorCanCreateInSchema                   bool
	qualificationOperatorHasSchemaUsage              bool
	qualificationOperatorCanCreateInSchema           bool
	ownedSchemaObjectCount                           int
	stableGroupRoleCount                             int
	stableGroupRolesUnsafe                           bool
	isApplicationRoleMember                          bool
	isMigrationOwnerRoleMember                       bool
	isOperatorRoleMember                             bool
	isGoldenFaultOperatorRoleMember                  bool
	isQualificationPromotionOperatorRoleMember       bool
	tableCount                                       int
	ownsOrInheritsTableOwner                         bool
	applicationExactTableACLCount                    int
	apiExactTableACLCount                            int
	publicPrivilegedIndexTableCount                  int
	gcPrivateTableCount                              int
	apiPrivilegedGCPrivateTableCount                 int
	goldenFaultTableCount                            int
	exactGoldenFaultOperatorTableACLCount            int
	qualificationPromotionTableCount                 int
	exactQualificationPromotionTableACLCount         int
	unexpectedQualificationPromotionTableACLCount    int
	credentialSetTableCount                          int
	exactCredentialSetTableContractCount             int
	credentialSetNamedTableCount                     int
	exactCredentialSetTriggerContractCount           int
	credentialSetTriggerCount                        int
	qualificationEvidenceTableCount                  int
	exactQualificationEvidenceTableContractCount     int
	qualificationEvidenceNamedTableCount             int
	exactQualificationEvidenceTriggerContractCount   int
	qualificationEvidenceTriggerCount                int
	qualificationPlanTableCount                      int
	exactQualificationPlanTableContractCount         int
	qualificationPlanNamedTableCount                 int
	qualificationPlanIndexCount                      int
	exactQualificationPlanIndexContractCount         int
	qualificationPlanNamedIndexCount                 int
	exactQualificationPlanTriggerContractCount       int
	qualificationPlanTriggerCount                    int
	protectedTableCount                              int
	applicationExactProtectedTableACLCount           int
	reachableNonApplicationRelationACLCount          int
	publicSchemaRelationACLCount                     int
	applicationRelationGrantOptionCount              int
	schemaColumnACLCount                             int
	reachableNonApplicationColumnACLCount            int
	publicSchemaColumnACLCount                       int
	applicationColumnGrantOptionCount                int
	applicationFunctionCount                         int
	securityDefinerApplicationFunctionCount          int
	migrationOwnedApplicationFunctionCount           int
	fixedSearchPathApplicationFunctionCount          int
	applicationBoundaryExecuteCount                  int
	apiApplicationFunctionExecuteCount               int
	publicApplicationFunctionExecuteCount            int
	unexpectedApplicationFunctionGranteeCount        int
	expectedGCFunctionCount                          int
	securityDefinerGCFunctionCount                   int
	migrationOwnedGCFunctionCount                    int
	fixedSearchPathGCFunctionCount                   int
	exactResultGCFunctionCount                       int
	operatorExpectedGCFunctionCount                  int
	apiExpectedGCFunctionCount                       int
	publicExpectedGCFunctionCount                    int
	unexpectedGCFunctionGranteeCount                 int
	internalFunctionCount                            int
	exactOwnerACLInternalFunctionCount               int
	modelGovernanceFunctionCount                     int
	exactModelGovernanceFunctionContractCount        int
	qualificationPromotionFunctionCount              int
	exactQualificationPromotionFunctionContractCount int
	unexpectedQualificationPromotionFunctionACLCount int
	credentialSetFunctionCount                       int
	exactCredentialSetFunctionContractCount          int
	credentialSetNamedFunctionCount                  int
	qualificationEvidenceFunctionCount               int
	exactQualificationEvidenceFunctionContractCount  int
	qualificationEvidenceNamedFunctionCount          int
	qualificationPlanFunctionCount                   int
	exactQualificationPlanFunctionContractCount      int
	qualificationPlanNamedFunctionCount              int
	sandboxCheckpointHelperCount                     int
	exactSandboxCheckpointHelperContractCount        int
	schemaOwnerIsExact                               bool
	ownedBoundaryTableCount                          int
	exactOwnedBoundaryTableCount                     int
	ownedBoundaryIndexCount                          int
	exactOwnedBoundaryIndexCount                     int
	ownedBoundaryRoutineCount                        int
	exactOwnedBoundaryRoutineCount                   int
	ownedRelationCount                               int
	exactOwnedRelationCount                          int
	securityDefinerFunctionCount                     int
	exactOwnedSecurityDefinerFunctionCount           int
	publicSchemaRoutineExecuteCount                  int
	reachableNonApplicationRoutineACLCount           int
	applicationRoutineGrantOptionCount               int
	reachableExecutableSecurityDefinerCount          int
	reachableExpectedApplicationDefinerCount         int
	reachableUnexpectedSecurityDefinerCount          int
	reachableGCFunctionExecuteCount                  int
	gcFunctionCount                                  int
	executableGCFunctionCount                        int
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
		&facts.ownedSchemaObjectCount,
		&facts.stableGroupRoleCount,
		&facts.stableGroupRolesUnsafe,
		&facts.isApplicationRoleMember,
		&facts.isMigrationOwnerRoleMember,
		&facts.isOperatorRoleMember,
		&facts.isGoldenFaultOperatorRoleMember,
		&facts.isQualificationPromotionOperatorRoleMember,
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
		&facts.unexpectedQualificationPromotionTableACLCount,
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
		&facts.unexpectedQualificationPromotionFunctionACLCount,
		&facts.credentialSetFunctionCount,
		&facts.exactCredentialSetFunctionContractCount,
		&facts.credentialSetNamedFunctionCount,
		&facts.qualificationEvidenceFunctionCount,
		&facts.exactQualificationEvidenceFunctionContractCount,
		&facts.qualificationEvidenceNamedFunctionCount,
		&facts.qualificationPlanFunctionCount,
		&facts.exactQualificationPlanFunctionContractCount,
		&facts.qualificationPlanNamedFunctionCount,
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
		violations = append(violations, "session API login can reach the migration-owner, GC-operator, Golden-fault-operator, or qualification-promotion-operator role")
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
	if facts.canCreateInSchema || facts.applicationCanCreateInSchema {
		violations = append(violations, "application group or API can create objects in the trusted schema")
	}
	if facts.faultOperatorCanCreateInSchema {
		violations = append(violations, "Golden fault operator can create objects in the trusted schema")
	}
	if facts.qualificationOperatorCanCreateInSchema {
		violations = append(violations, "qualification promotion operator can create objects in the trusted schema")
	}
	if facts.ownedSchemaObjectCount != 0 {
		violations = append(violations, "current API role owns or inherits objects in the trusted schema")
	}
	if facts.stableGroupRoleCount != 5 {
		violations = append(violations, "stable application, migration-owner, GC-operator, Golden-fault-operator, and qualification-promotion-operator group roles are incomplete")
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
		facts.unexpectedQualificationPromotionTableACLCount != 0 {
		violations = append(violations, "qualification promotion consume/handoff tables or their SELECT-only operator ACL contract are incomplete")
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
		facts.unexpectedQualificationPromotionFunctionACLCount != 0 {
		violations = append(violations, "qualification promotion consume routine signature, boolean result, owner, search_path, security mode, or dedicated operator ACL contract has drifted")
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
		violations = append(violations, "reachable SECURITY DEFINER execution is not the exact ten-function application contract")
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
