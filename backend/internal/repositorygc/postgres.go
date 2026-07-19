package repositorygc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	operatorRoleName       = "worksflow_repository_index_gc_operator"
	migrationOwnerRoleName = "worksflow_migration_owner"
	applicationRoleName    = "worksflow_application"
)

var (
	ErrPostgresReadiness = errors.New("repository exact-tree index GC PostgreSQL readiness failed")
	hexDigestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type PostgresAuthority struct {
	database *sql.DB
}

func NewPostgresAuthority(database *sql.DB) (*PostgresAuthority, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: database is required", ErrNotConfigured)
	}
	return &PostgresAuthority{database: database}, nil
}

func (authority *PostgresAuthority) Readiness(ctx context.Context) error {
	if authority == nil || authority.database == nil {
		return ErrNotConfigured
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrPostgresReadiness)
	}
	if err := authority.database.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: database is unreachable: %v", ErrPostgresReadiness, err)
	}

	var posture postgresPosture
	if err := authority.database.QueryRowContext(
		ctx, postgresPostureQuery, operatorRoleName, migrationOwnerRoleName, applicationRoleName,
	).Scan(
		&posture.roleName,
		&posture.sessionRoleName,
		&posture.isSuperuser,
		&posture.bypassesRLS,
		&posture.canCreateRole,
		&posture.canCreateDatabase,
		&posture.canReplicate,
		&posture.reachableRoleElevated,
		&posture.reachableRoleHasAdminOption,
		&posture.ownsDatabase,
		&posture.canCreateInDatabase,
		&posture.schemaName,
		&posture.canCreateInSchema,
		&posture.isOperatorMember,
		&posture.isMigrationOwnerMember,
		&posture.isApplicationMember,
		&posture.stableGroupRolesSafe,
		&posture.indexTableCount,
		&posture.ownsIndexTable,
		&posture.hasDirectTablePrivilege,
		&posture.functionCount,
		&posture.executableFunctionCount,
		&posture.secureFunctionContractCount,
		&posture.forbiddenSecurityDefinerCount,
		&posture.reachableSchemaObjectOwnerCount,
		&posture.privilegedRelationCount,
		&posture.privilegedSequenceCount,
		&posture.executableNonGCFunctionCount,
		&posture.grantableGCFunctionCount,
		&posture.relatedObjectsExactlyOwned,
		&posture.internalFunctionACLExact,
		&posture.sandboxCheckpointDependencyExact,
	); err != nil {
		return fmt.Errorf("%w: inspect database role and function catalogs: %v", ErrPostgresReadiness, err)
	}
	if err := posture.validate(); err != nil {
		return err
	}

	rows, err := authority.database.QueryContext(ctx, `
SELECT
  ready,
  reason,
  trusted_schema,
  operator_role_exists,
  application_role_exists,
  operator_execute_granted,
  application_claim_execute_granted,
  public_claim_execute_revoked,
  public_schema_create_revoked,
  migration_owner_role_exists,
  objects_owned_by_migration_owner,
  stable_group_roles_safe,
  application_schema_head_read_granted
FROM repository_exact_tree_literal_index_gc_readiness()`)
	if err != nil {
		return fmt.Errorf("%w: invoke database readiness authority: %v", ErrPostgresReadiness, err)
	}
	defer rows.Close()
	var readiness postgresReadiness
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("%w: read database readiness authority: %v", ErrPostgresReadiness, err)
		}
		return fmt.Errorf("%w: database readiness authority returned no row", ErrPostgresReadiness)
	}
	if err := rows.Scan(
		&readiness.ready,
		&readiness.reason,
		&readiness.trustedSchema,
		&readiness.operatorRoleExists,
		&readiness.applicationRoleExists,
		&readiness.operatorExecuteGranted,
		&readiness.applicationClaimExecuteGranted,
		&readiness.publicClaimExecuteRevoked,
		&readiness.publicSchemaCreateRevoked,
		&readiness.migrationOwnerRoleExists,
		&readiness.objectsOwnedByMigrationOwner,
		&readiness.stableGroupRolesSafe,
		&readiness.applicationSchemaHeadReadGranted,
	); err != nil {
		return fmt.Errorf("%w: scan database readiness authority: %v", ErrPostgresReadiness, err)
	}
	if rows.Next() {
		return fmt.Errorf("%w: database readiness authority returned multiple rows", ErrPostgresReadiness)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: finish database readiness authority: %v", ErrPostgresReadiness, err)
	}
	if err := readiness.validate(posture.schemaName); err != nil {
		return err
	}
	return nil
}

func (authority *PostgresAuthority) Plan(ctx context.Context, input PlanInput) ([]Capability, error) {
	if authority == nil || authority.database == nil {
		return nil, ErrNotConfigured
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: plan context is required", ErrAuthorityContract)
	}
	if err := inputPolicy(input).Validate(); err != nil {
		return nil, err
	}
	if input.RunID == uuid.Nil {
		return nil, fmt.Errorf("%w: plan run ID is required", ErrAuthorityContract)
	}
	rows, err := authority.database.QueryContext(ctx, `
SELECT
  run_id,
  capability_id,
  project_id,
  tree_hash,
  publication_created_at,
  index_commitment,
  planned_rank,
  expires_at
FROM plan_repository_exact_tree_literal_index_gc($1, $2, $3, $4, $5)`,
		input.RunID,
		input.Retention.Milliseconds(),
		input.KeepPerProject,
		input.BatchSize,
		int(input.CapabilityTTL.Milliseconds()),
	)
	if err != nil {
		return nil, fmt.Errorf("invoke repository exact-tree index GC plan authority: %w", err)
	}
	defer rows.Close()

	capabilities := make([]Capability, 0, input.BatchSize)
	for rows.Next() {
		var capability Capability
		var projectID uuid.UUID
		var treeHash, commitment string
		var publicationCreatedAt time.Time
		var plannedRank int
		if err := rows.Scan(
			&capability.RunID,
			&capability.CapabilityID,
			&projectID,
			&treeHash,
			&publicationCreatedAt,
			&commitment,
			&plannedRank,
			&capability.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan repository exact-tree index GC capability: %w", err)
		}
		if projectID == uuid.Nil || !hexDigestPattern.MatchString(treeHash) ||
			!hexDigestPattern.MatchString(commitment) || publicationCreatedAt.IsZero() ||
			plannedRank < 1 || plannedRank <= input.KeepPerProject {
			return nil, fmt.Errorf("%w: plan returned malformed immutable-tree authority", ErrAuthorityContract)
		}
		capabilities = append(capabilities, capability)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read repository exact-tree index GC capabilities: %w", err)
	}
	return capabilities, nil
}

func (authority *PostgresAuthority) Execute(ctx context.Context, capabilityID uuid.UUID) (Receipt, error) {
	if authority == nil || authority.database == nil {
		return Receipt{}, ErrNotConfigured
	}
	if ctx == nil {
		return Receipt{}, fmt.Errorf("%w: execute context is required", ErrAuthorityContract)
	}
	if capabilityID == uuid.Nil {
		return Receipt{}, fmt.Errorf("%w: execute capability ID is required", ErrAuthorityContract)
	}
	var receipt Receipt
	var projectID uuid.UUID
	var treeHash, commitment string
	var publicationCreatedAt, executedAt time.Time
	var deletedMemberCount, deletedBlobCount int64
	var logicalBytesReleased, blobBytesFreed int64
	err := authority.database.QueryRowContext(ctx, `
SELECT
  receipt_id,
  capability_id,
  run_id,
  project_id,
  tree_hash,
  publication_created_at,
  index_commitment,
  deleted_member_count,
  deleted_blob_count,
  outcome,
  logical_bytes_released,
  blob_bytes_freed,
  executed_at,
  idempotent
FROM execute_repository_exact_tree_literal_index_gc($1)`, capabilityID).Scan(
		&receipt.ReceiptID,
		&receipt.CapabilityID,
		&receipt.RunID,
		&projectID,
		&treeHash,
		&publicationCreatedAt,
		&commitment,
		&deletedMemberCount,
		&deletedBlobCount,
		&receipt.Outcome,
		&logicalBytesReleased,
		&blobBytesFreed,
		&executedAt,
		&receipt.Idempotent,
	)
	if err != nil {
		return Receipt{}, fmt.Errorf("invoke repository exact-tree index GC execute authority: %w", err)
	}
	if projectID == uuid.Nil || !hexDigestPattern.MatchString(treeHash) ||
		!hexDigestPattern.MatchString(commitment) || publicationCreatedAt.IsZero() || executedAt.IsZero() ||
		deletedMemberCount < 0 || deletedBlobCount < 0 || logicalBytesReleased < 0 || blobBytesFreed < 0 {
		return Receipt{}, fmt.Errorf("%w: execute returned malformed immutable-tree receipt", ErrAuthorityContract)
	}
	switch receipt.Outcome {
	case OutcomeDeleted:
		// Deleted trees may legitimately be empty.
	case OutcomeProtected, OutcomeStale, OutcomeExpired:
		if deletedMemberCount != 0 || deletedBlobCount != 0 || logicalBytesReleased != 0 || blobBytesFreed != 0 {
			return Receipt{}, fmt.Errorf("%w: non-deletion receipt reported released data", ErrAuthorityContract)
		}
	default:
		return Receipt{}, fmt.Errorf("%w: execute returned an unknown terminal outcome", ErrAuthorityContract)
	}
	return receipt, nil
}

func (authority *PostgresAuthority) Inspect(ctx context.Context, runID uuid.UUID) (Inspection, error) {
	if authority == nil || authority.database == nil {
		return Inspection{}, ErrNotConfigured
	}
	if ctx == nil {
		return Inspection{}, fmt.Errorf("%w: inspect context is required", ErrAuthorityContract)
	}
	if runID == uuid.Nil {
		return Inspection{}, fmt.Errorf("%w: inspect run ID is required", ErrAuthorityContract)
	}
	var inspection Inspection
	var plannedAt, cutoffAt time.Time
	var keepPerProject, batchSize int
	var capabilityTTLMilliseconds int64
	var pending int64
	err := authority.database.QueryRowContext(ctx, `
SELECT
  run_id,
  run_status,
  planned_at,
  cutoff_at,
  keep_per_project,
  batch_size,
  capability_ttl_milliseconds,
  planned_capability_count,
  deleted_capability_count,
  pending_capability_count,
  protected_capability_count,
  stale_capability_count,
  expired_capability_count,
  logical_bytes_released,
  blob_bytes_freed
FROM inspect_repository_exact_tree_literal_index_gc_run($1)`, runID).Scan(
		&inspection.RunID,
		&inspection.State,
		&plannedAt,
		&cutoffAt,
		&keepPerProject,
		&batchSize,
		&capabilityTTLMilliseconds,
		&inspection.Result.Planned,
		&inspection.Result.Deleted,
		&pending,
		&inspection.Result.Protected,
		&inspection.Result.Stale,
		&inspection.Result.Expired,
		&inspection.Result.LogicalBytesReleased,
		&inspection.Result.BlobBytesFreed,
	)
	if err != nil {
		return Inspection{}, fmt.Errorf("invoke repository exact-tree index GC inspection authority: %w", err)
	}
	inspection.Result.SchemaVersion = ResultSchemaVersion
	inspection.Result.RunID = inspection.RunID
	if plannedAt.IsZero() || cutoffAt.IsZero() || cutoffAt.After(plannedAt) ||
		keepPerProject < MinimumKeepPerProject || batchSize < 1 || batchSize > MaximumBatchSize ||
		capabilityTTLMilliseconds <= 0 || capabilityTTLMilliseconds > MaximumCapabilityTTL.Milliseconds() || pending < 0 ||
		inspection.Result.Deleted+inspection.Result.Protected+pending+inspection.Result.Stale+inspection.Result.Expired != inspection.Result.Planned {
		return Inspection{}, fmt.Errorf("%w: inspection returned malformed run authority", ErrAuthorityContract)
	}
	return inspection, nil
}

func inputPolicy(input PlanInput) Policy {
	return Policy{
		Retention: input.Retention, KeepPerProject: input.KeepPerProject,
		BatchSize: input.BatchSize, CapabilityTTL: input.CapabilityTTL,
	}
}

type postgresPosture struct {
	roleName                         string
	sessionRoleName                  string
	isSuperuser                      bool
	bypassesRLS                      bool
	canCreateRole                    bool
	canCreateDatabase                bool
	canReplicate                     bool
	reachableRoleElevated            bool
	reachableRoleHasAdminOption      bool
	ownsDatabase                     bool
	canCreateInDatabase              bool
	schemaName                       string
	canCreateInSchema                bool
	isOperatorMember                 bool
	isMigrationOwnerMember           bool
	isApplicationMember              bool
	stableGroupRolesSafe             bool
	indexTableCount                  int
	ownsIndexTable                   bool
	hasDirectTablePrivilege          bool
	functionCount                    int
	executableFunctionCount          int
	secureFunctionContractCount      int
	forbiddenSecurityDefinerCount    int
	reachableSchemaObjectOwnerCount  int
	privilegedRelationCount          int
	privilegedSequenceCount          int
	executableNonGCFunctionCount     int
	grantableGCFunctionCount         int
	relatedObjectsExactlyOwned       bool
	internalFunctionACLExact         bool
	sandboxCheckpointDependencyExact bool
}

func (posture postgresPosture) validate() error {
	violations := make([]string, 0, 10)
	if strings.TrimSpace(posture.roleName) == "" || posture.roleName != posture.sessionRoleName {
		violations = append(violations, "session and current database roles are absent or different")
	}
	if posture.isSuperuser || posture.bypassesRLS || posture.canCreateRole ||
		posture.canCreateDatabase || posture.canReplicate || posture.reachableRoleElevated {
		violations = append(violations, "session login can reach platform administration authority")
	}
	if posture.reachableRoleHasAdminOption {
		violations = append(violations, "session login or a reachable role can delegate database role authority")
	}
	if posture.ownsDatabase || posture.canCreateInDatabase {
		violations = append(violations, "session login owns or can create objects in the database")
	}
	if strings.TrimSpace(posture.schemaName) == "" || posture.canCreateInSchema {
		violations = append(violations, "trusted schema is absent or writable by the operator")
	}
	if !posture.isOperatorMember {
		violations = append(violations, "current database role is not a GC operator member")
	}
	if posture.isMigrationOwnerMember || posture.isApplicationMember {
		violations = append(violations, "current database role crosses the migration, application, and GC operator boundary")
	}
	if !posture.stableGroupRolesSafe {
		violations = append(violations, "stable application, migration-owner, and GC operator roles are absent, login-capable, or elevated")
	}
	if posture.indexTableCount != 10 || posture.ownsIndexTable || posture.hasDirectTablePrivilege {
		violations = append(violations, "current database role has direct repository-index or GC-audit mutation authority")
	}
	if posture.functionCount != 4 || posture.executableFunctionCount != 4 ||
		posture.secureFunctionContractCount != 4 {
		violations = append(violations, "exact GC function contract is absent or not executable")
	}
	if posture.forbiddenSecurityDefinerCount != 0 {
		violations = append(violations, "session login can execute a non-GC SECURITY DEFINER function")
	}
	if posture.reachableSchemaObjectOwnerCount != 0 {
		violations = append(violations, "session login or a reachable role owns a trusted-schema object")
	}
	if posture.privilegedRelationCount != 0 {
		violations = append(violations, "session login can access a trusted-schema table, view, or foreign relation directly")
	}
	if posture.privilegedSequenceCount != 0 {
		violations = append(violations, "session login can access a trusted-schema sequence directly")
	}
	if posture.executableNonGCFunctionCount != 0 || posture.grantableGCFunctionCount != 0 {
		violations = append(violations, "session login function authority exceeds exact non-grantable GC EXECUTE")
	}
	if !posture.relatedObjectsExactlyOwned {
		violations = append(violations, "trusted schema, storage relations, or boundary routines are not exactly migration-role owned")
	}
	if !posture.internalFunctionACLExact {
		violations = append(violations, "exact-index internal routine authority is not owner-only")
	}
	if !posture.sandboxCheckpointDependencyExact {
		violations = append(violations, "Sandbox checkpoint helper owner, shape, path, or application authority is not exact")
	}
	if len(violations) > 0 {
		return fmt.Errorf("%w: %s", ErrPostgresReadiness, strings.Join(violations, "; "))
	}
	return nil
}

type postgresReadiness struct {
	ready                            bool
	reason                           string
	trustedSchema                    string
	operatorRoleExists               bool
	applicationRoleExists            bool
	operatorExecuteGranted           bool
	applicationClaimExecuteGranted   bool
	publicClaimExecuteRevoked        bool
	publicSchemaCreateRevoked        bool
	migrationOwnerRoleExists         bool
	objectsOwnedByMigrationOwner     bool
	stableGroupRolesSafe             bool
	applicationSchemaHeadReadGranted bool
}

func (readiness postgresReadiness) validate(currentSchema string) error {
	if !readiness.ready || readiness.reason != "ready" || readiness.trustedSchema != currentSchema ||
		!readiness.operatorRoleExists || !readiness.applicationRoleExists ||
		!readiness.operatorExecuteGranted || !readiness.applicationClaimExecuteGranted ||
		!readiness.publicClaimExecuteRevoked || !readiness.publicSchemaCreateRevoked ||
		!readiness.migrationOwnerRoleExists || !readiness.objectsOwnedByMigrationOwner ||
		!readiness.stableGroupRolesSafe || !readiness.applicationSchemaHeadReadGranted {
		return fmt.Errorf("%w: database readiness authority rejected the deployment posture", ErrPostgresReadiness)
	}
	return nil
}

const postgresPostureQuery = `
WITH
reachable_roles AS (
  SELECT role.*
  FROM pg_catalog.pg_roles AS role
  WHERE pg_catalog.pg_has_role(session_user, role.oid, 'MEMBER')
),
migration_owner_facts AS (
  SELECT role.oid
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname = $2
),
application_role_facts AS (
  SELECT role.oid
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname = $3
),
current_role_facts AS (
  SELECT
    role.rolname,
    role.rolsuper,
    role.rolbypassrls,
    role.rolcreaterole,
    role.rolcreatedb,
    role.rolreplication
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname = current_user
),
stable_group_role_facts AS (
  SELECT count(*) = 3 AND bool_and(
    NOT role.rolcanlogin
    AND NOT role.rolsuper
    AND NOT role.rolbypassrls
    AND NOT role.rolcreaterole
    AND NOT role.rolcreatedb
    AND NOT role.rolreplication
  ) AS roles_are_safe
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname IN ($1, $2, $3)
),
reachable_role_facts AS (
  SELECT coalesce(bool_or(
    role.rolsuper
    OR role.rolbypassrls
    OR role.rolcreaterole
    OR role.rolcreatedb
    OR role.rolreplication
  ), false) AS has_elevated_role
  FROM reachable_roles AS role
),
reachable_membership_facts AS (
  SELECT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_auth_members AS membership
    JOIN reachable_roles AS role ON role.oid = membership.member
    WHERE membership.admin_option
  ) AS has_admin_option
),
database_facts AS (
  SELECT
    pg_catalog.pg_has_role(session_user, database.datdba, 'MEMBER') AS owns_database,
    coalesce(bool_or(pg_catalog.has_database_privilege(
      role.oid, database.oid, 'CREATE'
    )), false) AS can_create
  FROM pg_catalog.pg_database AS database
  CROSS JOIN reachable_roles AS role
  WHERE database.datname = pg_catalog.current_database()
  GROUP BY database.datdba
),
schema_facts AS (
  SELECT
    namespace.oid,
    namespace.nspname,
    namespace.nspowner,
    coalesce(bool_or(pg_catalog.has_schema_privilege(
      role.oid, namespace.oid, 'CREATE'
    )), false) AS can_create
  FROM pg_catalog.pg_namespace AS namespace
  CROSS JOIN reachable_roles AS role
  WHERE namespace.nspname = pg_catalog.current_schema()
  GROUP BY namespace.oid, namespace.nspname, namespace.nspowner
),
index_table_facts AS (
  SELECT
    count(*)::integer AS table_count,
    coalesce(bool_or(pg_catalog.pg_has_role(session_user, relation.relowner, 'MEMBER')), false) AS owns_table,
    coalesce(bool_or(
      EXISTS (
        SELECT 1
        FROM reachable_roles AS role
        WHERE pg_catalog.has_table_privilege(role.oid, relation.oid, 'SELECT')
          OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'INSERT')
          OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'UPDATE')
          OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'DELETE')
          OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'TRUNCATE')
          OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'REFERENCES')
          OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'TRIGGER')
      )
    ), false) AS can_destroy
  FROM schema_facts AS schema_state
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.oid
   AND relation.relname IN (
     'repository_exact_tree_literal_index_manifests',
     'repository_exact_tree_literal_index_members',
     'repository_exact_tree_literal_index_blobs',
     'repository_exact_tree_literal_index_build_claims',
     'repository_exact_tree_literal_index_gc_runs',
     'repository_exact_tree_literal_index_gc_capabilities',
     'repository_exact_tree_literal_index_gc_receipts',
     'repository_exact_tree_literal_index_gc_tombstones',
     'repository_exact_tree_literal_index_gc_tree_delete_auth',
     'repository_exact_tree_literal_index_gc_blob_delete_auth'
   )
   AND relation.relkind IN ('r', 'p')
),
expected_functions(signature, output_names, output_types) AS (
  VALUES
    (
      pg_catalog.format('%I.plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)', pg_catalog.current_schema()),
      ARRAY[
        'run_id', 'capability_id', 'project_id', 'tree_hash',
        'publication_created_at', 'index_commitment', 'planned_rank', 'expires_at'
      ]::text[],
      ARRAY[
        'uuid', 'uuid', 'uuid', 'text', 'timestamp with time zone', 'text', 'integer',
        'timestamp with time zone'
      ]::text[]
    ),
    (
      pg_catalog.format('%I.execute_repository_exact_tree_literal_index_gc(uuid)', pg_catalog.current_schema()),
      ARRAY[
        'receipt_id', 'capability_id', 'run_id', 'project_id', 'tree_hash',
        'publication_created_at', 'index_commitment', 'outcome', 'deleted_member_count',
        'deleted_blob_count', 'logical_bytes_released', 'blob_bytes_freed', 'executed_at',
        'idempotent'
      ]::text[],
      ARRAY[
        'uuid', 'uuid', 'uuid', 'uuid', 'text', 'timestamp with time zone', 'text', 'text',
        'integer', 'integer', 'bigint', 'bigint', 'timestamp with time zone', 'boolean'
      ]::text[]
    ),
    (
      pg_catalog.format('%I.inspect_repository_exact_tree_literal_index_gc_run(uuid)', pg_catalog.current_schema()),
      ARRAY[
        'run_id', 'run_status', 'planned_at', 'cutoff_at', 'keep_per_project', 'batch_size',
        'capability_ttl_milliseconds', 'planned_capability_count', 'deleted_capability_count',
        'protected_capability_count', 'stale_capability_count', 'expired_capability_count',
        'pending_capability_count', 'logical_bytes_released', 'blob_bytes_freed'
      ]::text[],
      ARRAY[
        'uuid', 'text', 'timestamp with time zone', 'timestamp with time zone', 'integer',
        'integer', 'integer', 'integer', 'integer', 'integer', 'integer', 'integer', 'integer',
        'bigint', 'bigint'
      ]::text[]
    ),
    (
      pg_catalog.format('%I.repository_exact_tree_literal_index_gc_readiness()', pg_catalog.current_schema()),
      ARRAY[
        'ready', 'reason', 'trusted_schema', 'operator_role_exists', 'application_role_exists',
        'migration_owner_role_exists', 'stable_group_roles_safe', 'objects_owned_by_migration_owner',
        'operator_execute_granted', 'application_claim_execute_granted',
        'application_schema_head_read_granted', 'public_claim_execute_revoked',
        'public_schema_create_revoked'
      ]::text[],
      ARRAY[
        'boolean', 'text', 'text', 'boolean', 'boolean', 'boolean', 'boolean', 'boolean',
        'boolean', 'boolean', 'boolean', 'boolean', 'boolean'
      ]::text[]
    )
),
expected_function_oids AS (
  SELECT
    expected.signature,
    expected.output_names,
    expected.output_types,
    pg_catalog.to_regprocedure(expected.signature) AS routine_oid
  FROM expected_functions AS expected
),
expected_boundary_functions(signature) AS (
  VALUES
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_gc_audit_mutation()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_blob_mutation()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_member_mutation()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_manifest_delete()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_manifest_insert()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_member_insert()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.publish_repository_exact_tree_literal_index_manifest()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.lock_candidate_exact_tree_literal_index_reference()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.execute_repository_exact_tree_literal_index_gc(uuid)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.inspect_repository_exact_tree_literal_index_gc_run(uuid)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.repository_exact_tree_literal_index_gc_readiness()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer,integer,bigint,integer)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.release_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.acquire_candidate_workspace_lease(uuid,bigint,uuid,integer)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.rotate_candidate_workspace_session(uuid,bigint,bigint,uuid)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.update_candidate_workspace_flags(uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.freeze_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.abandon_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.abandon_sandbox_session_candidate(uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.complete_abandoned_sandbox_session(uuid,bigint,bigint,uuid)', pg_catalog.current_schema())),
    (pg_catalog.format('%I.sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)', pg_catalog.current_schema()))
),
expected_boundary_function_oids AS (
  SELECT pg_catalog.to_regprocedure(expected.signature) AS routine_oid
  FROM expected_boundary_functions AS expected
),
expected_internal_functions(signature) AS (
  VALUES
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_gc_audit_mutation()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_blob_mutation()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_member_mutation()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_manifest_delete()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_manifest_insert()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.guard_repository_exact_tree_literal_index_member_insert()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.publish_repository_exact_tree_literal_index_manifest()', pg_catalog.current_schema())),
    (pg_catalog.format('%I.lock_candidate_exact_tree_literal_index_reference()', pg_catalog.current_schema()))
),
expected_internal_function_oids AS (
  SELECT pg_catalog.to_regprocedure(expected.signature) AS routine_oid
  FROM expected_internal_functions AS expected
),
expected_sandbox_checkpoint_helper(signature) AS (
  VALUES (
    pg_catalog.format(
      '%I.sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)',
      pg_catalog.current_schema()
    )
  )
),
expected_sandbox_checkpoint_helper_oid AS (
  SELECT pg_catalog.to_regprocedure(expected.signature) AS routine_oid
  FROM expected_sandbox_checkpoint_helper AS expected
),
function_facts AS (
  SELECT
    count(expected.routine_oid)::integer AS function_count,
    count(*) FILTER (
      WHERE pg_catalog.has_function_privilege(current_user, expected.routine_oid, 'EXECUTE')
    )::integer AS executable_function_count,
    count(*) FILTER (
      WHERE routine.prosecdef
        AND routine.proretset
        AND routine.prorettype = 'pg_catalog.record'::pg_catalog.regtype
        AND routine.proconfig = ARRAY[
          pg_catalog.format(
            'search_path=pg_catalog, %I, pg_temp',
            pg_catalog.current_schema()
          )
        ]::text[]
        AND output_arguments.names = expected.output_names
        AND output_arguments.types = expected.output_types
        AND output_arguments.modes_are_exact
        AND routine.proowner = migration_owner.oid
    )::integer AS secure_contract_count
  FROM expected_function_oids AS expected
  CROSS JOIN migration_owner_facts AS migration_owner
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = expected.routine_oid
  LEFT JOIN LATERAL (
    SELECT
      coalesce(
        array_agg(routine.proargnames[position.index] ORDER BY position.index)
          FILTER (WHERE routine.proargmodes[position.index] = 't'),
        ARRAY[]::text[]
      ) AS names,
      coalesce(
        array_agg(
          pg_catalog.format_type(routine.proallargtypes[position.index], NULL)
          ORDER BY position.index
        ) FILTER (WHERE routine.proargmodes[position.index] = 't'),
        ARRAY[]::text[]
      ) AS types,
      coalesce(bool_and(
        CASE
          WHEN position.index <= routine.pronargs
            THEN routine.proargmodes[position.index] = 'i'
          ELSE routine.proargmodes[position.index] = 't'
        END
      ), false) AS modes_are_exact
    FROM pg_catalog.generate_subscripts(routine.proallargtypes, 1) AS position(index)
  ) AS output_arguments ON true
),
internal_function_acl_facts AS (
  SELECT (
    count(expected.routine_oid) = 8
    AND count(*) FILTER (
      WHERE routine.proowner = migration_owner.oid
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(
            coalesce(
              routine.proacl,
              pg_catalog.acldefault('f', routine.proowner)
            )
          ) AS privilege
          WHERE privilege.grantee = routine.proowner
            AND privilege.privilege_type = 'EXECUTE'
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(
            coalesce(
              routine.proacl,
              pg_catalog.acldefault('f', routine.proowner)
            )
          ) AS privilege
          WHERE privilege.privilege_type = 'EXECUTE'
            AND privilege.grantee <> routine.proowner
        )
    ) = 8
  ) AS acl_is_exact
  FROM expected_internal_function_oids AS expected
  CROSS JOIN migration_owner_facts AS migration_owner
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = expected.routine_oid
),
sandbox_checkpoint_helper_facts AS (
  SELECT (
    count(expected.routine_oid) = 1
    AND count(*) FILTER (
      WHERE routine.proowner = migration_owner.oid
        AND NOT routine.prosecdef
        AND routine.prokind = 'f'
        AND routine.prorettype = 'boolean'::pg_catalog.regtype
        AND NOT routine.proretset
        AND routine.provolatile = 's'
        AND language.lanname = 'sql'
        AND routine.proconfig = ARRAY[
          pg_catalog.format(
            'search_path=pg_catalog, %I, pg_temp',
            pg_catalog.current_schema()
          )
        ]::text[]
        AND EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(
            coalesce(
              routine.proacl,
              pg_catalog.acldefault('f', routine.proowner)
            )
          ) AS privilege
          WHERE privilege.grantee = application_role.oid
            AND privilege.privilege_type = 'EXECUTE'
            AND NOT privilege.is_grantable
        )
        AND NOT EXISTS (
          SELECT 1
          FROM pg_catalog.aclexplode(
            coalesce(
              routine.proacl,
              pg_catalog.acldefault('f', routine.proowner)
            )
          ) AS privilege
          WHERE privilege.privilege_type = 'EXECUTE'
            AND (
              privilege.grantee NOT IN (routine.proowner, application_role.oid)
              OR (
                privilege.grantee = application_role.oid
                AND privilege.is_grantable
              )
            )
        )
    ) = 1
  ) AS dependency_is_exact
  FROM expected_sandbox_checkpoint_helper_oid AS expected
  CROSS JOIN migration_owner_facts AS migration_owner
  CROSS JOIN application_role_facts AS application_role
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = expected.routine_oid
  LEFT JOIN pg_catalog.pg_language AS language ON language.oid = routine.prolang
),
schema_owned_objects(owner_oid) AS (
  SELECT namespace.nspowner
  FROM schema_facts AS namespace
  UNION ALL
  SELECT relation.relowner
  FROM pg_catalog.pg_class AS relation
  JOIN schema_facts AS namespace ON namespace.oid = relation.relnamespace
  UNION ALL
  SELECT routine.proowner
  FROM pg_catalog.pg_proc AS routine
  JOIN schema_facts AS namespace ON namespace.oid = routine.pronamespace
  UNION ALL
  SELECT type_entry.typowner
  FROM pg_catalog.pg_type AS type_entry
  JOIN schema_facts AS namespace ON namespace.oid = type_entry.typnamespace
  UNION ALL
  SELECT collation_entry.collowner
  FROM pg_catalog.pg_collation AS collation_entry
  JOIN schema_facts AS namespace ON namespace.oid = collation_entry.collnamespace
  UNION ALL
  SELECT conversion_entry.conowner
  FROM pg_catalog.pg_conversion AS conversion_entry
  JOIN schema_facts AS namespace ON namespace.oid = conversion_entry.connamespace
  UNION ALL
  SELECT operator_entry.oprowner
  FROM pg_catalog.pg_operator AS operator_entry
  JOIN schema_facts AS namespace ON namespace.oid = operator_entry.oprnamespace
  UNION ALL
  SELECT operator_class.opcowner
  FROM pg_catalog.pg_opclass AS operator_class
  JOIN schema_facts AS namespace ON namespace.oid = operator_class.opcnamespace
  UNION ALL
  SELECT operator_family.opfowner
  FROM pg_catalog.pg_opfamily AS operator_family
  JOIN schema_facts AS namespace ON namespace.oid = operator_family.opfnamespace
  UNION ALL
  SELECT text_search_config.cfgowner
  FROM pg_catalog.pg_ts_config AS text_search_config
  JOIN schema_facts AS namespace ON namespace.oid = text_search_config.cfgnamespace
  UNION ALL
  SELECT text_search_dictionary.dictowner
  FROM pg_catalog.pg_ts_dict AS text_search_dictionary
  JOIN schema_facts AS namespace ON namespace.oid = text_search_dictionary.dictnamespace
  UNION ALL
  SELECT statistics_entry.stxowner
  FROM pg_catalog.pg_statistic_ext AS statistics_entry
  JOIN schema_facts AS namespace ON namespace.oid = statistics_entry.stxnamespace
  UNION ALL
  SELECT extension_entry.extowner
  FROM pg_catalog.pg_extension AS extension_entry
  JOIN schema_facts AS namespace ON namespace.oid = extension_entry.extnamespace
  UNION ALL
  SELECT default_acl_entry.defaclrole
  FROM pg_catalog.pg_default_acl AS default_acl_entry
  JOIN schema_facts AS namespace ON namespace.oid = default_acl_entry.defaclnamespace
),
schema_object_authority_facts AS (
  SELECT count(*) FILTER (
    WHERE EXISTS (
      SELECT 1
      FROM reachable_roles AS role
      WHERE role.oid = object_entry.owner_oid
    )
  )::integer AS reachable_owner_count
  FROM schema_owned_objects AS object_entry
),
relation_authority_facts AS (
  SELECT count(*) FILTER (
    WHERE EXISTS (
      SELECT 1
      FROM reachable_roles AS role
      WHERE pg_catalog.has_table_privilege(role.oid, relation.oid, 'SELECT')
        OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'INSERT')
        OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'UPDATE')
        OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'DELETE')
        OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'TRUNCATE')
        OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'REFERENCES')
        OR pg_catalog.has_table_privilege(role.oid, relation.oid, 'TRIGGER')
        OR pg_catalog.has_any_column_privilege(role.oid, relation.oid, 'SELECT')
        OR pg_catalog.has_any_column_privilege(role.oid, relation.oid, 'INSERT')
        OR pg_catalog.has_any_column_privilege(role.oid, relation.oid, 'UPDATE')
        OR pg_catalog.has_any_column_privilege(role.oid, relation.oid, 'REFERENCES')
    )
  )::integer AS privileged_relation_count
  FROM schema_facts AS namespace
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = namespace.oid
   AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
),
sequence_authority_facts AS (
  SELECT count(*) FILTER (
    WHERE EXISTS (
      SELECT 1
      FROM reachable_roles AS role
      WHERE pg_catalog.has_sequence_privilege(role.oid, sequence_entry.oid, 'SELECT')
        OR pg_catalog.has_sequence_privilege(role.oid, sequence_entry.oid, 'USAGE')
        OR pg_catalog.has_sequence_privilege(role.oid, sequence_entry.oid, 'UPDATE')
    )
  )::integer AS privileged_sequence_count
  FROM schema_facts AS namespace
  JOIN pg_catalog.pg_class AS sequence_entry
    ON sequence_entry.relnamespace = namespace.oid
   AND sequence_entry.relkind = 'S'
),
function_authority_facts AS (
  SELECT
    count(*) FILTER (
      WHERE expected.routine_oid IS NULL
        AND EXISTS (
          SELECT 1
          FROM reachable_roles AS role
          WHERE pg_catalog.has_function_privilege(role.oid, routine.oid, 'EXECUTE')
        )
    )::integer AS executable_non_gc_count,
    count(*) FILTER (
      WHERE expected.routine_oid IS NOT NULL
        AND EXISTS (
          SELECT 1
          FROM reachable_roles AS role
          WHERE pg_catalog.has_function_privilege(
            role.oid, routine.oid, 'EXECUTE WITH GRANT OPTION'
          )
        )
    )::integer AS grantable_gc_count
  FROM schema_facts AS namespace
  JOIN pg_catalog.pg_proc AS routine ON routine.pronamespace = namespace.oid
  LEFT JOIN expected_function_oids AS expected ON expected.routine_oid = routine.oid
),
boundary_table_owner_facts AS (
  SELECT
    count(*)::integer AS table_count,
    count(*) FILTER (WHERE relation.relowner = migration_owner.oid)::integer AS exact_owner_count
  FROM schema_facts AS schema_state
  CROSS JOIN migration_owner_facts AS migration_owner
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.oid
   AND relation.relname IN (
     'repository_exact_tree_literal_index_manifests',
     'repository_exact_tree_literal_index_members',
     'repository_exact_tree_literal_index_blobs',
     'repository_exact_tree_literal_index_build_claims',
     'repository_exact_tree_literal_index_gc_runs',
     'repository_exact_tree_literal_index_gc_capabilities',
     'repository_exact_tree_literal_index_gc_receipts',
     'repository_exact_tree_literal_index_gc_tombstones',
     'repository_exact_tree_literal_index_gc_tree_delete_auth',
     'repository_exact_tree_literal_index_gc_blob_delete_auth'
   )
   AND relation.relkind IN ('r', 'p')
),
boundary_index_owner_facts AS (
  SELECT
    count(*)::integer AS index_count,
    count(*) FILTER (WHERE index_relation.relowner = migration_owner.oid)::integer AS exact_owner_count
  FROM schema_facts AS schema_state
  CROSS JOIN migration_owner_facts AS migration_owner
  JOIN pg_catalog.pg_class AS indexed_relation
    ON indexed_relation.relnamespace = schema_state.oid
   AND indexed_relation.relname IN (
     'repository_exact_tree_literal_index_manifests',
     'repository_exact_tree_literal_index_members',
     'repository_exact_tree_literal_index_blobs',
     'repository_exact_tree_literal_index_build_claims',
     'repository_exact_tree_literal_index_gc_runs',
     'repository_exact_tree_literal_index_gc_capabilities',
     'repository_exact_tree_literal_index_gc_receipts',
     'repository_exact_tree_literal_index_gc_tombstones',
     'repository_exact_tree_literal_index_gc_tree_delete_auth',
     'repository_exact_tree_literal_index_gc_blob_delete_auth'
   )
   AND indexed_relation.relkind IN ('r', 'p')
  JOIN pg_catalog.pg_index AS index_entry ON index_entry.indrelid = indexed_relation.oid
  JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid = index_entry.indexrelid
),
schema_storage_owner_facts AS (
  SELECT count(*) FILTER (
    WHERE relation.relowner <> migration_owner.oid
  )::integer AS non_exact_owner_count
  FROM schema_facts AS schema_state
  CROSS JOIN migration_owner_facts AS migration_owner
  JOIN pg_catalog.pg_class AS relation
    ON relation.relnamespace = schema_state.oid
   AND (
     relation.relkind IN ('r', 'p', 'S')
     OR (
       relation.relkind IN ('i', 'I')
       AND EXISTS (
         SELECT 1
         FROM pg_catalog.pg_index AS index_entry
         JOIN pg_catalog.pg_class AS indexed_relation
           ON indexed_relation.oid = index_entry.indrelid
         WHERE index_entry.indexrelid = relation.oid
           AND indexed_relation.relkind IN ('r', 'p')
       )
     )
   )
),
boundary_function_owner_facts AS (
  SELECT
    count(expected.routine_oid)::integer AS function_count,
    count(*) FILTER (WHERE routine.proowner = migration_owner.oid)::integer AS exact_owner_count
  FROM expected_boundary_function_oids AS expected
  CROSS JOIN migration_owner_facts AS migration_owner
  LEFT JOIN pg_catalog.pg_proc AS routine ON routine.oid = expected.routine_oid
),
related_object_owner_facts AS (
  SELECT (
    schema_state.nspowner = migration_owner.oid
    AND boundary_tables.table_count = 10
    AND boundary_tables.exact_owner_count = 10
    AND boundary_indexes.index_count = 22
    AND boundary_indexes.exact_owner_count = 22
    AND schema_storage.non_exact_owner_count = 0
    AND boundary_functions.function_count = 23
    AND boundary_functions.exact_owner_count = 23
  ) AS exactly_owned
  FROM schema_facts AS schema_state
  CROSS JOIN migration_owner_facts AS migration_owner
  CROSS JOIN boundary_table_owner_facts AS boundary_tables
  CROSS JOIN boundary_index_owner_facts AS boundary_indexes
  CROSS JOIN schema_storage_owner_facts AS schema_storage
  CROSS JOIN boundary_function_owner_facts AS boundary_functions
),
security_definer_facts AS (
  SELECT count(*)::integer AS forbidden_executable_count
  FROM pg_catalog.pg_proc AS routine
  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = routine.pronamespace
  WHERE namespace.nspname = pg_catalog.current_schema()
    AND routine.prosecdef
    AND routine.oid NOT IN (
      SELECT expected.routine_oid
      FROM expected_function_oids AS expected
      WHERE expected.routine_oid IS NOT NULL
    )
    AND EXISTS (
      SELECT 1
      FROM reachable_roles AS role
      WHERE pg_catalog.has_function_privilege(role.oid, routine.oid, 'EXECUTE')
    )
)
SELECT
  role_state.rolname,
  session_user::text,
  role_state.rolsuper,
  role_state.rolbypassrls,
  role_state.rolcreaterole,
  role_state.rolcreatedb,
  role_state.rolreplication,
  reachable_role_state.has_elevated_role,
  reachable_membership_state.has_admin_option,
  database_state.owns_database,
  database_state.can_create,
  schema_state.nspname,
  schema_state.can_create,
  pg_catalog.pg_has_role(current_user, $1, 'MEMBER'),
  pg_catalog.pg_has_role(current_user, $2, 'MEMBER'),
  pg_catalog.pg_has_role(current_user, $3, 'MEMBER'),
  stable_roles.roles_are_safe,
  index_tables.table_count,
  index_tables.owns_table,
  index_tables.can_destroy,
  functions.function_count,
  functions.executable_function_count,
  functions.secure_contract_count,
  security_definers.forbidden_executable_count,
  schema_objects.reachable_owner_count,
  relation_authority.privileged_relation_count,
  sequence_authority.privileged_sequence_count,
  function_authority.executable_non_gc_count,
  function_authority.grantable_gc_count,
  related_owners.exactly_owned,
  internal_functions.acl_is_exact,
  sandbox_checkpoint_helper.dependency_is_exact
FROM current_role_facts AS role_state
CROSS JOIN reachable_role_facts AS reachable_role_state
CROSS JOIN reachable_membership_facts AS reachable_membership_state
CROSS JOIN database_facts AS database_state
CROSS JOIN schema_facts AS schema_state
CROSS JOIN stable_group_role_facts AS stable_roles
CROSS JOIN index_table_facts AS index_tables
CROSS JOIN function_facts AS functions
CROSS JOIN security_definer_facts AS security_definers
CROSS JOIN schema_object_authority_facts AS schema_objects
CROSS JOIN relation_authority_facts AS relation_authority
CROSS JOIN sequence_authority_facts AS sequence_authority
CROSS JOIN function_authority_facts AS function_authority
CROSS JOIN related_object_owner_facts AS related_owners
CROSS JOIN internal_function_acl_facts AS internal_functions
CROSS JOIN sandbox_checkpoint_helper_facts AS sandbox_checkpoint_helper`
