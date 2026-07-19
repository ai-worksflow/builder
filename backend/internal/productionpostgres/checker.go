package productionpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/platform"
)

const (
	applicationGroupRole  = "worksflow_application"
	migrationOwnerRole    = "worksflow_migration_owner"
	gcOperatorRole        = "worksflow_repository_index_gc_operator"
	goldenFaultRole       = "worksflow_golden_fault_operator"
	promotionOperatorRole = "worksflow_qualification_promotion_operator"
)

// sessionPostureQuery takes one catalog snapshot per connected identity. The
// four identities cannot share a transaction, so endpoint/database equality
// is also pinned in validated configuration and observed database names are
// compared after inspection.
const sessionPostureQuery = `
WITH RECURSIVE
current_role_facts AS (
  SELECT
    count(*)::integer AS role_count,
    coalesce(min(role.rolname), '') AS role_name,
    session_user::text AS session_role_name,
    coalesce(bool_or(role.rolcanlogin), false) AS can_login,
    coalesce(bool_or(
      role.rolsuper OR role.rolbypassrls OR role.rolcreaterole OR
      role.rolcreatedb OR role.rolreplication
    ), false) AS has_cluster_authority,
    min(role.oid) AS role_oid
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
    coalesce(bool_or(role.rolname = '` + applicationGroupRole + `'), false)
      AS application_is_reachable,
    coalesce(bool_or(role.rolname = '` + migrationOwnerRole + `'), false)
      AS migration_owner_is_reachable,
    coalesce(bool_or(role.rolname = '` + gcOperatorRole + `'), false)
      AS gc_operator_is_reachable,
    coalesce(bool_or(role.rolname = '` + goldenFaultRole + `'), false)
      AS golden_fault_operator_is_reachable,
    coalesce(bool_or(role.rolname = '` + promotionOperatorRole + `'), false)
      AS promotion_operator_is_reachable,
    EXISTS (
      SELECT 1
      FROM pg_catalog.pg_auth_members AS membership
      JOIN session_reachable_roles AS member
        ON member.role_oid = membership.member
      WHERE membership.admin_option
    ) AS has_admin_option
  FROM pg_catalog.pg_roles AS role
  JOIN session_reachable_roles AS reachable ON reachable.role_oid = role.oid
),
stable_role_facts AS (
  SELECT
    count(*)::integer AS role_count,
    count(*) FILTER (
      WHERE role.rolcanlogin OR role.rolsuper OR role.rolbypassrls OR
        role.rolcreaterole OR role.rolcreatedb OR role.rolreplication
    )::integer AS unsafe_role_count,
    count(*) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM pg_catalog.pg_auth_members AS membership
        WHERE membership.member = role.oid
      )
    )::integer AS outgoing_membership_count,
    count(*) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM pg_catalog.pg_auth_members AS membership
        WHERE membership.roleid = role.oid AND membership.admin_option
      )
    )::integer AS administered_role_count,
    min(role.oid) FILTER (WHERE role.rolname = '` + migrationOwnerRole + `')
      AS migration_owner_oid
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname IN (
    '` + applicationGroupRole + `',
    '` + migrationOwnerRole + `',
    '` + gcOperatorRole + `',
	'` + goldenFaultRole + `',
	'` + promotionOperatorRole + `'
  )
),
database_facts AS (
  SELECT
    count(*)::integer AS database_count,
    coalesce(min(database.datname), '') AS database_name,
    EXISTS (
      SELECT 1
      FROM pg_catalog.pg_stat_ssl AS ssl_state
      WHERE ssl_state.pid = pg_catalog.pg_backend_pid()
        AND ssl_state.ssl
    ) AS transport_uses_tls,
    NOT pg_catalog.pg_is_in_recovery()
      AND pg_catalog.current_setting('transaction_read_only') = 'off'
      AS primary_is_read_write,
    coalesce(bool_or(EXISTS (
      SELECT 1 FROM session_reachable_roles AS reachable
      WHERE reachable.role_oid = database.datdba
    )), false) AS reachable_owns_database,
    coalesce(bool_or(EXISTS (
      SELECT 1 FROM session_reachable_roles AS reachable
      WHERE pg_catalog.has_database_privilege(reachable.role_oid, database.oid, 'CREATE')
    )), false) AS reachable_can_create
  FROM pg_catalog.pg_database AS database
  WHERE database.datname = pg_catalog.current_database()
),
schema_facts AS (
  SELECT
    count(*)::integer AS schema_count,
    coalesce(min(namespace.nspname), '') AS schema_name,
    min(namespace.oid) AS schema_oid,
    coalesce(bool_or(namespace.nspowner = stable.migration_owner_oid), false)
      AS owned_by_migration_owner,
    coalesce(bool_or(EXISTS (
      SELECT 1 FROM session_reachable_roles AS reachable
      WHERE reachable.role_oid = namespace.nspowner
    )), false) AS reachable_owns_schema,
    coalesce(bool_or(EXISTS (
      SELECT 1 FROM session_reachable_roles AS reachable
      WHERE pg_catalog.has_schema_privilege(reachable.role_oid, namespace.oid, 'USAGE')
    )), false) AS reachable_has_usage,
    coalesce(bool_or(EXISTS (
      SELECT 1 FROM session_reachable_roles AS reachable
      WHERE pg_catalog.has_schema_privilege(reachable.role_oid, namespace.oid, 'CREATE')
    )), false) AS reachable_can_create
  FROM stable_role_facts AS stable
  LEFT JOIN pg_catalog.pg_namespace AS namespace ON namespace.nspname = $1
),
relation_facts AS (
  SELECT
    count(*) FILTER (WHERE relation.relkind IN ('r', 'p', 'S', 'v', 'm', 'f', 'i', 'I'))::integer
      AS owned_boundary_count,
    count(*) FILTER (
      WHERE relation.relkind IN ('r', 'p', 'S', 'v', 'm', 'f', 'i', 'I')
        AND relation.relowner = stable.migration_owner_oid
    )::integer AS migration_owned_boundary_count,
    count(*) FILTER (
      WHERE relation.relkind IN ('r', 'p', 'v', 'm', 'f') AND EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'SELECT')
          OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'INSERT')
          OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'UPDATE')
          OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'DELETE')
          OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRUNCATE')
          OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'REFERENCES')
          OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRIGGER')
      )
    )::integer AS reachable_table_privilege_count,
    count(*) FILTER (
      WHERE relation.relkind IN ('r', 'p')
        AND relation.relname IN (
          'qualification_promotion_consumptions',
          'qualification_promotion_handoffs'
        )
        AND EXISTS (
          SELECT 1 FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'SELECT')
        )
        AND NOT EXISTS (
          SELECT 1 FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'INSERT')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'UPDATE')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'DELETE')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRUNCATE')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'REFERENCES')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRIGGER')
        )
    )::integer AS promotion_exact_table_select_count,
    count(*) FILTER (
      WHERE relation.relkind IN ('r', 'p', 'v', 'm', 'f')
        AND EXISTS (
          SELECT 1 FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'SELECT')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'INSERT')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'UPDATE')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'DELETE')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRUNCATE')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'REFERENCES')
            OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRIGGER')
        )
        AND (
          relation.relname NOT IN (
            'qualification_promotion_consumptions',
            'qualification_promotion_handoffs'
          )
          OR EXISTS (
            SELECT 1 FROM session_reachable_roles AS reachable
            WHERE pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'INSERT')
              OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'UPDATE')
              OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'DELETE')
              OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRUNCATE')
              OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'REFERENCES')
              OR pg_catalog.has_table_privilege(reachable.role_oid, relation.oid, 'TRIGGER')
          )
        )
    )::integer AS promotion_unexpected_table_privilege_count,
    count(*) FILTER (
      WHERE relation.relkind = 'S' AND EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE pg_catalog.has_sequence_privilege(reachable.role_oid, relation.oid, 'SELECT')
          OR pg_catalog.has_sequence_privilege(reachable.role_oid, relation.oid, 'USAGE')
          OR pg_catalog.has_sequence_privilege(reachable.role_oid, relation.oid, 'UPDATE')
      )
    )::integer AS reachable_sequence_privilege_count,
    count(*) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = relation.relowner
      )
    )::integer AS reachable_owned_relation_count,
    count(*) FILTER (
      WHERE role_state.role_oid IS NOT NULL AND EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(relation.relacl) AS acl
        WHERE acl.grantee = role_state.role_oid
      )
    )::integer AS direct_relation_acl_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  CROSS JOIN current_role_facts AS role_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'S', 'v', 'm', 'f', 'i', 'I')
),
column_facts AS (
  SELECT
    count(*) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'SELECT')
          OR pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'INSERT')
          OR pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'UPDATE')
          OR pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'REFERENCES')
      )
    )::integer AS reachable_column_privilege_count,
    count(*) FILTER (
      WHERE role_state.role_oid IS NOT NULL AND EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(attribute.attacl) AS acl
        WHERE acl.grantee = role_state.role_oid
      )
    )::integer AS direct_column_acl_count,
    count(*) FILTER (
      WHERE (
        relation.relname NOT IN (
          'qualification_promotion_consumptions',
          'qualification_promotion_handoffs'
        )
        AND EXISTS (
          SELECT 1 FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'SELECT')
            OR pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'INSERT')
            OR pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'UPDATE')
            OR pg_catalog.has_column_privilege(reachable.role_oid, relation.oid, attribute.attnum, 'REFERENCES')
        )
      )
      OR EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(attribute.attacl) AS acl
        JOIN session_reachable_roles AS reachable ON reachable.role_oid = acl.grantee
      )
    )::integer AS promotion_unexpected_column_privilege_count
  FROM schema_facts AS schema_state
  CROSS JOIN current_role_facts AS role_state
  LEFT JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.schema_oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
  LEFT JOIN pg_catalog.pg_attribute AS attribute
    ON attribute.attrelid = relation.oid
   AND attribute.attnum > 0
   AND NOT attribute.attisdropped
),
routine_facts AS (
  SELECT
    count(routine.oid)::integer AS routine_count,
    count(routine.oid) FILTER (
      WHERE routine.proowner = stable.migration_owner_oid
    )::integer AS migration_owned_routine_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE pg_catalog.has_function_privilege(reachable.role_oid, routine.oid, 'EXECUTE')
      )
    )::integer AS reachable_execute_count,
    count(routine.oid) FILTER (
      WHERE routine.proname = 'consume_verified_qualification_promotion'
        AND pg_catalog.oidvectortypes(routine.proargtypes) =
          'uuid, text, bytea, jsonb, text, text, bytea, jsonb, uuid, uuid, text, bytea, jsonb'
        AND EXISTS (
          SELECT 1 FROM session_reachable_roles AS reachable
          WHERE pg_catalog.has_function_privilege(reachable.role_oid, routine.oid, 'EXECUTE')
        )
    )::integer AS promotion_exact_routine_execute_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE pg_catalog.has_function_privilege(reachable.role_oid, routine.oid, 'EXECUTE')
      )
        AND (
          routine.proname <> 'consume_verified_qualification_promotion'
          OR pg_catalog.oidvectortypes(routine.proargtypes) <>
            'uuid, text, bytea, jsonb, text, text, bytea, jsonb, uuid, uuid, text, bytea, jsonb'
        )
    )::integer AS promotion_unexpected_routine_execute_count,
    count(routine.oid) FILTER (
      WHERE EXISTS (
        SELECT 1 FROM session_reachable_roles AS reachable
        WHERE reachable.role_oid = routine.proowner
      )
    )::integer AS reachable_owned_routine_count,
    count(routine.oid) FILTER (
      WHERE role_state.role_oid IS NOT NULL AND EXISTS (
        SELECT 1
        FROM pg_catalog.aclexplode(routine.proacl) AS acl
        WHERE acl.grantee = role_state.role_oid
      )
    )::integer AS direct_routine_acl_count
  FROM schema_facts AS schema_state
  CROSS JOIN stable_role_facts AS stable
  CROSS JOIN current_role_facts AS role_state
  LEFT JOIN pg_catalog.pg_proc AS routine
    ON routine.pronamespace = schema_state.schema_oid
),
schema_direct_acl_facts AS (
  SELECT count(*)::integer AS direct_acl_count
  FROM schema_facts AS schema_state
  CROSS JOIN current_role_facts AS role_state
  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = schema_state.schema_oid
  CROSS JOIN LATERAL pg_catalog.aclexplode(namespace.nspacl) AS acl
  WHERE acl.grantee = role_state.role_oid
)
SELECT
  role_state.role_count,
  role_state.role_name,
  role_state.session_role_name,
  role_state.can_login,
  role_state.has_cluster_authority,
  reachable.role_count,
  reachable.has_cluster_authority,
  reachable.application_is_reachable,
  reachable.migration_owner_is_reachable,
  reachable.gc_operator_is_reachable,
  reachable.golden_fault_operator_is_reachable,
  reachable.promotion_operator_is_reachable,
  reachable.has_admin_option,
  stable.role_count,
  stable.unsafe_role_count,
  stable.outgoing_membership_count,
  stable.administered_role_count,
  database_state.database_count,
  database_state.database_name,
  database_state.transport_uses_tls,
  database_state.primary_is_read_write,
  database_state.reachable_owns_database,
  database_state.reachable_can_create,
  schema_state.schema_count,
  schema_state.schema_name,
  schema_state.owned_by_migration_owner,
  schema_state.reachable_owns_schema,
  schema_state.reachable_has_usage,
  schema_state.reachable_can_create,
  relations.owned_boundary_count,
  relations.migration_owned_boundary_count,
  relations.reachable_table_privilege_count,
  relations.promotion_exact_table_select_count,
  relations.promotion_unexpected_table_privilege_count,
  relations.reachable_sequence_privilege_count,
  relations.reachable_owned_relation_count,
  relations.direct_relation_acl_count,
  columns.reachable_column_privilege_count,
  columns.direct_column_acl_count,
  columns.promotion_unexpected_column_privilege_count,
  routines.routine_count,
  routines.migration_owned_routine_count,
  routines.reachable_execute_count,
  routines.promotion_exact_routine_execute_count,
  routines.promotion_unexpected_routine_execute_count,
  routines.reachable_owned_routine_count,
  routines.direct_routine_acl_count,
  schema_direct.direct_acl_count
FROM current_role_facts AS role_state
CROSS JOIN reachable_role_facts AS reachable
CROSS JOIN stable_role_facts AS stable
CROSS JOIN database_facts AS database_state
CROSS JOIN schema_facts AS schema_state
CROSS JOIN relation_facts AS relations
CROSS JOIN column_facts AS columns
CROSS JOIN routine_facts AS routines
CROSS JOIN schema_direct_acl_facts AS schema_direct`

type rowScanner interface {
	Scan(...any) error
}

type sessionQueryer interface {
	QueryRowContext(context.Context, string, ...any) rowScanner
}

type sqlQueryer struct{ database *sql.DB }

func (q sqlQueryer) QueryRowContext(ctx context.Context, query string, args ...any) rowScanner {
	return q.database.QueryRowContext(ctx, query, args...)
}

type sessionFacts struct {
	roleCount                               int
	roleName                                string
	sessionRoleName                         string
	canLogin                                bool
	hasClusterAuthority                     bool
	reachableRoleCount                      int
	reachableHasClusterAuthority            bool
	applicationReachable                    bool
	migrationOwnerReachable                 bool
	gcOperatorReachable                     bool
	goldenFaultOperatorReachable            bool
	promotionOperatorReachable              bool
	hasAdminOption                          bool
	stableRoleCount                         int
	unsafeStableRoleCount                   int
	stableOutgoingMembershipCount           int
	stableAdministeredRoleCount             int
	databaseCount                           int
	databaseName                            string
	transportUsesTLS                        bool
	primaryIsReadWrite                      bool
	reachableOwnsDatabase                   bool
	reachableCanCreateDatabaseObjects       bool
	schemaCount                             int
	schemaName                              string
	schemaOwnedByMigrationOwner             bool
	reachableOwnsSchema                     bool
	reachableHasSchemaUsage                 bool
	reachableCanCreateInSchema              bool
	ownedBoundaryRelationCount              int
	migrationOwnedBoundaryCount             int
	reachableTablePrivilegeCount            int
	promotionExactTableSelectCount          int
	promotionUnexpectedTablePrivilegeCount  int
	reachableSequencePrivilegeCount         int
	reachableOwnedRelationCount             int
	directRelationACLCount                  int
	reachableColumnPrivilegeCount           int
	directColumnACLCount                    int
	promotionUnexpectedColumnPrivilegeCount int
	routineCount                            int
	migrationOwnedRoutineCount              int
	reachableRoutineExecuteCount            int
	promotionExactRoutineExecuteCount       int
	promotionUnexpectedRoutineExecuteCount  int
	reachableOwnedRoutineCount              int
	directRoutineACLCount                   int
	directSchemaACLCount                    int
}

type databaseHandle struct {
	database *sql.DB
	queryer  sessionQueryer
	ping     func(context.Context) error
	close    func() error
}

type verificationDependencies struct {
	now               func() time.Time
	verifyTrustAnchor func(string) error
	open              func(RoleKind, string) (*databaseHandle, error)
	verifyApplication func(context.Context, *sql.DB, string) error
	inspect           func(context.Context, sessionQueryer, string) (sessionFacts, error)
}

func defaultVerificationDependencies() verificationDependencies {
	return verificationDependencies{
		now:               time.Now,
		verifyTrustAnchor: VerifyTrustAnchorFile,
		open: func(_ RoleKind, dsn string) (*databaseHandle, error) {
			database, err := sql.Open("pgx", dsn)
			if err != nil {
				return nil, err
			}
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(0)
			return &databaseHandle{
				database: database,
				queryer:  sqlQueryer{database: database},
				ping:     database.PingContext,
				close:    database.Close,
			}, nil
		},
		verifyApplication: platform.VerifyPostgresAPIRolePosture,
		inspect:           inspectSession,
	}
}

// Verify checks four concurrently held, distinct production identities within
// one bounded call. Their catalog queries cannot share a transaction, so this
// is not an atomic cross-identity snapshot and does not issue or consume
// qualification/promotion authority.
func Verify(ctx context.Context, config Config) (Result, error) {
	return verifyWithDependencies(ctx, config, defaultVerificationDependencies())
}

func verifyWithDependencies(
	ctx context.Context,
	configuration Config,
	dependencies verificationDependencies,
) (Result, error) {
	if dependencies.now == nil {
		dependencies.now = time.Now
	}
	result := newResult(dependencies.now(), "")
	if ctx == nil || dependencies.verifyTrustAnchor == nil || dependencies.open == nil ||
		dependencies.verifyApplication == nil || dependencies.inspect == nil {
		return result, fail(&result, ErrInvalidConfiguration, FailureConfigurationInvalid, "")
	}
	validated, err := validateConfig(configuration)
	if err != nil {
		return result, fail(&result, ErrInvalidConfiguration, FailureConfigurationInvalid, "")
	}
	result.Schema = validated.schema
	if err := dependencies.verifyTrustAnchor(validated.application.rootCertificate); err != nil {
		return result, fail(&result, ErrInvalidConfiguration, FailureConfigurationInvalid, "")
	}

	type roleConnection struct {
		kind     RoleKind
		expected string
		dsn      string
		handle   *databaseHandle
		facts    sessionFacts
	}
	connections := []roleConnection{
		{kind: RoleApplication, expected: validated.application.username, dsn: validated.application.scoped},
		{kind: RoleMigrator, expected: validated.migrator.username, dsn: validated.migrator.scoped},
		{kind: RoleQualification, expected: validated.qualification.username, dsn: validated.qualification.scoped},
		{kind: RolePromotion, expected: validated.promotion.username, dsn: validated.promotion.scoped},
	}
	defer func() {
		for index := range connections {
			if connections[index].handle != nil && connections[index].handle.close != nil {
				_ = connections[index].handle.close()
			}
		}
	}()

	for index := range connections {
		connection := &connections[index]
		handle, openErr := dependencies.open(connection.kind, connection.dsn)
		if openErr != nil || handle == nil || handle.ping == nil || handle.close == nil || handle.queryer == nil {
			return result, fail(&result, ErrOperational, FailureConnectionUnavailable, connection.kind)
		}
		connection.handle = handle
		if pingErr := handle.ping(ctx); pingErr != nil {
			return result, fail(&result, ErrOperational, FailureConnectionUnavailable, connection.kind)
		}
	}

	if err := dependencies.verifyApplication(
		ctx,
		connections[0].handle.database,
		config.EnvironmentProduction,
	); err != nil {
		return result, fail(&result, ErrUnsafePosture, FailureApplicationPostureUnsafe, RoleApplication)
	}

	for index := range connections {
		connection := &connections[index]
		facts, inspectErr := dependencies.inspect(ctx, connection.handle.queryer, validated.schema)
		if inspectErr != nil {
			return result, fail(&result, ErrOperational, FailureCatalogInspectionFailed, connection.kind)
		}
		connection.facts = facts
		violations := validateSessionFacts(connection.kind, connection.expected, validated.schema, facts)
		if len(violations) != 0 {
			code := FailureApplicationPostureUnsafe
			if connection.kind == RoleMigrator {
				code = FailureMigratorPostureUnsafe
			} else if connection.kind == RoleQualification {
				code = FailureAuditorPostureUnsafe
			} else if connection.kind == RolePromotion {
				code = FailurePromotionPostureUnsafe
			}
			return result, fail(&result, ErrUnsafePosture, code, connection.kind)
		}
		result.Roles[index].Identity = facts.roleName
		result.Roles[index].Status = StatusPassed
	}

	databaseName := connections[0].facts.databaseName
	identities := make(map[string]struct{}, len(connections))
	identityMismatch := databaseName == ""
	for _, connection := range connections {
		if connection.facts.databaseName != databaseName {
			identityMismatch = true
		}
		if _, exists := identities[connection.facts.roleName]; exists {
			identityMismatch = true
		}
		identities[connection.facts.roleName] = struct{}{}
	}
	if identityMismatch {
		return result, fail(&result, ErrUnsafePosture, FailureIdentityScopeMismatch, "")
	}
	result.Status = StatusPassed
	result.Failure = nil
	return result, nil
}

func inspectSession(ctx context.Context, queryer sessionQueryer, schema string) (sessionFacts, error) {
	if queryer == nil {
		return sessionFacts{}, errors.New("PostgreSQL session is unavailable")
	}
	var facts sessionFacts
	err := queryer.QueryRowContext(ctx, sessionPostureQuery, schema).Scan(
		&facts.roleCount,
		&facts.roleName,
		&facts.sessionRoleName,
		&facts.canLogin,
		&facts.hasClusterAuthority,
		&facts.reachableRoleCount,
		&facts.reachableHasClusterAuthority,
		&facts.applicationReachable,
		&facts.migrationOwnerReachable,
		&facts.gcOperatorReachable,
		&facts.goldenFaultOperatorReachable,
		&facts.promotionOperatorReachable,
		&facts.hasAdminOption,
		&facts.stableRoleCount,
		&facts.unsafeStableRoleCount,
		&facts.stableOutgoingMembershipCount,
		&facts.stableAdministeredRoleCount,
		&facts.databaseCount,
		&facts.databaseName,
		&facts.transportUsesTLS,
		&facts.primaryIsReadWrite,
		&facts.reachableOwnsDatabase,
		&facts.reachableCanCreateDatabaseObjects,
		&facts.schemaCount,
		&facts.schemaName,
		&facts.schemaOwnedByMigrationOwner,
		&facts.reachableOwnsSchema,
		&facts.reachableHasSchemaUsage,
		&facts.reachableCanCreateInSchema,
		&facts.ownedBoundaryRelationCount,
		&facts.migrationOwnedBoundaryCount,
		&facts.reachableTablePrivilegeCount,
		&facts.promotionExactTableSelectCount,
		&facts.promotionUnexpectedTablePrivilegeCount,
		&facts.reachableSequencePrivilegeCount,
		&facts.reachableOwnedRelationCount,
		&facts.directRelationACLCount,
		&facts.reachableColumnPrivilegeCount,
		&facts.directColumnACLCount,
		&facts.promotionUnexpectedColumnPrivilegeCount,
		&facts.routineCount,
		&facts.migrationOwnedRoutineCount,
		&facts.reachableRoutineExecuteCount,
		&facts.promotionExactRoutineExecuteCount,
		&facts.promotionUnexpectedRoutineExecuteCount,
		&facts.reachableOwnedRoutineCount,
		&facts.directRoutineACLCount,
		&facts.directSchemaACLCount,
	)
	if err != nil {
		return sessionFacts{}, err
	}
	return facts, nil
}

func validateSessionFacts(kind RoleKind, expectedRole, schema string, facts sessionFacts) []string {
	violations := make([]string, 0, 16)
	if facts.roleCount != 1 || facts.roleName == "" || facts.roleName != expectedRole ||
		facts.sessionRoleName != facts.roleName || !facts.canLogin {
		violations = append(violations, "login identity is not exact")
	}
	if facts.hasClusterAuthority || facts.reachableHasClusterAuthority || facts.hasAdminOption {
		violations = append(violations, "login can reach cluster or role administration authority")
	}
	if facts.stableRoleCount != 5 || facts.unsafeStableRoleCount != 0 ||
		facts.stableOutgoingMembershipCount != 0 || facts.stableAdministeredRoleCount != 0 {
		violations = append(violations, "stable group role boundary is unsafe")
	}
	if facts.databaseCount != 1 || facts.databaseName == "" || !facts.transportUsesTLS ||
		!facts.primaryIsReadWrite || facts.reachableOwnsDatabase ||
		facts.reachableCanCreateDatabaseObjects {
		violations = append(violations, "login can own the database or create database objects")
	}
	if facts.schemaCount != 1 || facts.schemaName != schema || !facts.schemaOwnedByMigrationOwner {
		violations = append(violations, "trusted schema ownership is not exact")
	}
	if facts.directSchemaACLCount != 0 || facts.directRelationACLCount != 0 ||
		facts.directColumnACLCount != 0 || facts.directRoutineACLCount != 0 {
		violations = append(violations, "login has direct trusted-schema ACLs outside its group boundary")
	}

	switch kind {
	case RoleApplication:
		if facts.reachableRoleCount != 2 || !facts.applicationReachable ||
			facts.migrationOwnerReachable || facts.gcOperatorReachable || facts.goldenFaultOperatorReachable || facts.promotionOperatorReachable {
			violations = append(violations, "application login does not have exactly one application-group path")
		}
		if facts.reachableOwnsSchema || !facts.reachableHasSchemaUsage || facts.reachableCanCreateInSchema ||
			facts.reachableOwnedRelationCount != 0 || facts.reachableOwnedRoutineCount != 0 {
			violations = append(violations, "application login can own or create trusted-schema objects")
		}
	case RoleMigrator:
		if facts.reachableRoleCount != 2 || facts.applicationReachable ||
			!facts.migrationOwnerReachable || facts.gcOperatorReachable || facts.goldenFaultOperatorReachable || facts.promotionOperatorReachable {
			violations = append(violations, "migrator login does not have exactly one migration-owner path")
		}
		if !facts.reachableOwnsSchema || !facts.reachableHasSchemaUsage || !facts.reachableCanCreateInSchema {
			violations = append(violations, "migrator cannot reach the migration-owned schema authority")
		}
		if facts.ownedBoundaryRelationCount < 1 ||
			facts.migrationOwnedBoundaryCount != facts.ownedBoundaryRelationCount ||
			facts.reachableOwnedRelationCount < facts.ownedBoundaryRelationCount ||
			facts.migrationOwnedRoutineCount != facts.routineCount ||
			facts.reachableOwnedRoutineCount != facts.routineCount {
			violations = append(violations, "migration owner does not solely own the trusted-schema boundary")
		}
	case RoleQualification:
		if facts.reachableRoleCount != 1 || facts.applicationReachable ||
			facts.migrationOwnerReachable || facts.gcOperatorReachable || facts.goldenFaultOperatorReachable || facts.promotionOperatorReachable {
			violations = append(violations, "qualification auditor can inherit or SET ROLE into a stable group")
		}
		if facts.reachableOwnsSchema || facts.reachableHasSchemaUsage || facts.reachableCanCreateInSchema ||
			facts.reachableTablePrivilegeCount != 0 || facts.reachableSequencePrivilegeCount != 0 ||
			facts.reachableColumnPrivilegeCount != 0 || facts.reachableOwnedRelationCount != 0 ||
			facts.reachableRoutineExecuteCount != 0 || facts.reachableOwnedRoutineCount != 0 {
			violations = append(violations, "qualification auditor can access trusted-schema data, functions, or ownership")
		}
	case RolePromotion:
		if facts.reachableRoleCount != 2 || facts.applicationReachable || facts.migrationOwnerReachable ||
			facts.gcOperatorReachable || facts.goldenFaultOperatorReachable || !facts.promotionOperatorReachable {
			violations = append(violations, "promotion login does not have exactly one qualification-promotion-operator path")
		}
		if facts.reachableOwnsSchema || !facts.reachableHasSchemaUsage || facts.reachableCanCreateInSchema ||
			facts.reachableTablePrivilegeCount != 2 || facts.reachableSequencePrivilegeCount != 0 ||
			facts.promotionExactTableSelectCount != 2 || facts.promotionUnexpectedTablePrivilegeCount != 0 ||
			facts.reachableColumnPrivilegeCount < 1 || facts.promotionUnexpectedColumnPrivilegeCount != 0 ||
			facts.reachableOwnedRelationCount != 0 || facts.reachableRoutineExecuteCount != 1 ||
			facts.promotionExactRoutineExecuteCount != 1 || facts.promotionUnexpectedRoutineExecuteCount != 0 ||
			facts.reachableOwnedRoutineCount != 0 {
			violations = append(violations, "promotion login does not have the exact two-table SELECT and one consume-routine contract")
		}
	default:
		violations = append(violations, "role kind is unsupported")
	}
	return violations
}
