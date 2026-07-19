-- Fail before the first schema ACL, DDL, ownership, or default-privilege
-- mutation when a deployment has a partial or unsafe production role posture.
-- An explicit all-absent posture remains supported for isolated local
-- development; once any stable role exists the production trio is atomic.
DO $stable_role_preflight$
DECLARE
  stable_role_oids oid[];
  stable_role_count integer := 0;
  unsafe_role_count integer := 0;
  outgoing_membership_count integer := 0;
  administrative_membership_count integer := 0;
  explicit_column_privilege_count integer := 0;
BEGIN
  SELECT
    array_agg(role.oid ORDER BY role.oid),
    count(*)::integer,
    count(*) FILTER (
      WHERE role.rolcanlogin
         OR role.rolsuper
         OR role.rolbypassrls
         OR role.rolcreaterole
         OR role.rolcreatedb
         OR role.rolreplication
    )::integer
  INTO stable_role_oids, stable_role_count, unsafe_role_count
  FROM pg_catalog.pg_roles AS role
  WHERE role.rolname IN (
    'worksflow_application',
    'worksflow_migration_owner',
    'worksflow_repository_index_gc_operator'
  );

  -- Column ACLs are independent of relation ACLs and may represent unrelated
  -- business or third-party contracts.  Refuse them before any mutation so a
  -- reviewed deployment cleanup is explicit and rollback remains exact.
  SELECT count(*)::integer
  INTO explicit_column_privilege_count
  FROM pg_catalog.pg_class AS relation
  JOIN pg_catalog.pg_namespace AS namespace
    ON namespace.oid = relation.relnamespace
  JOIN pg_catalog.pg_attribute AS attribute
    ON attribute.attrelid = relation.oid
  CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS privilege
  WHERE namespace.nspname = pg_catalog.current_schema()
    AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
    AND attribute.attnum > 0
    AND NOT attribute.attisdropped;
  IF explicit_column_privilege_count <> 0 THEN
    RAISE EXCEPTION
      'Exact-tree literal index GC requires trusted-schema column privileges to be revoked explicitly before migration'
      USING ERRCODE = '55000';
  END IF;

  IF stable_role_count = 0 THEN
    RETURN;
  END IF;
  IF stable_role_count <> 3 THEN
    RAISE EXCEPTION
      'Exact-tree literal index GC requires either all three stable roles or none for local development'
      USING ERRCODE = '55000';
  END IF;
  IF unsafe_role_count <> 0 THEN
    RAISE EXCEPTION
      'Exact-tree literal index GC stable roles must be NOLOGIN without elevated attributes'
      USING ERRCODE = '55000';
  END IF;

  -- A stable group must never itself be a member of another role.  This is a
  -- stronger, catalog-stable form of forbidding both inherited and SET ROLE
  -- reachability to a peer, intermediate, or elevated authority.  Incoming
  -- non-admin membership remains the intended API/operator/migration-login
  -- attachment point.
  SELECT count(*)::integer
  INTO outgoing_membership_count
  FROM pg_catalog.pg_auth_members AS membership
  WHERE membership.member = ANY(stable_role_oids);
  IF outgoing_membership_count <> 0 THEN
    RAISE EXCEPTION
      'Exact-tree literal index GC stable roles must not inherit or SET-reach another role'
      USING ERRCODE = '55000';
  END IF;

  -- No member may administer one of the stable authority groups.  Otherwise
  -- it could make that authority transitive after this one-time preflight.
  SELECT count(*)::integer
  INTO administrative_membership_count
  FROM pg_catalog.pg_auth_members AS membership
  WHERE membership.roleid = ANY(stable_role_oids)
    AND membership.admin_option;
  IF administrative_membership_count <> 0 THEN
    RAISE EXCEPTION
      'Exact-tree literal index GC stable-role memberships must not grant ADMIN OPTION'
      USING ERRCODE = '55000';
  END IF;
END;
$stable_role_preflight$;

-- Exact-tree literal indexes are derived accelerators, but deleting one must
-- still be a tenant-fenced, auditable operation.  Capture the deployment's
-- trusted schema before hardening the SECURITY DEFINER search path.  This is
-- deliberately dynamic because tests and isolated deployments use a private
-- schema rather than public.
DO $migration$
DECLARE
  trusted_schema text := pg_catalog.current_schema();
BEGIN
  IF trusted_schema IS NULL
     OR trusted_schema IN ('pg_catalog', 'information_schema')
     OR trusted_schema LIKE 'pg_temp_%' THEN
    RAISE EXCEPTION 'Exact-tree literal index GC requires one trusted application schema'
      USING ERRCODE = '3F000';
  END IF;

  EXECUTE pg_catalog.format('REVOKE CREATE ON SCHEMA %I FROM PUBLIC', trusted_schema);
  PERFORM pg_catalog.set_config(
    'search_path',
    pg_catalog.format('%I,pg_catalog,pg_temp', trusted_schema),
    true
  );
END;
$migration$;

CREATE TABLE repository_exact_tree_literal_index_gc_runs (
  run_id uuid PRIMARY KEY,
  retention_milliseconds bigint NOT NULL CHECK (
    retention_milliseconds >= 604800000
  ),
  keep_per_project integer NOT NULL CHECK (keep_per_project >= 8),
  batch_size integer NOT NULL CHECK (batch_size BETWEEN 1 AND 100),
  capability_ttl_milliseconds integer NOT NULL CHECK (
    capability_ttl_milliseconds BETWEEN 1 AND 900000
  ),
  planned_at timestamptz NOT NULL,
  cutoff_at timestamptz NOT NULL,
  planned_by text NOT NULL CHECK (length(planned_by) > 0),
  CONSTRAINT repository_exact_tree_literal_index_gc_run_cutoff_check CHECK (
    cutoff_at = planned_at - make_interval(
      secs => retention_milliseconds::double precision / 1000.0
    )
  )
);

CREATE TABLE repository_exact_tree_literal_index_gc_capabilities (
  capability_id uuid PRIMARY KEY,
  run_id uuid NOT NULL REFERENCES repository_exact_tree_literal_index_gc_runs(run_id)
    ON DELETE RESTRICT,
  project_id uuid NOT NULL,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  schema_version text NOT NULL CHECK (
    schema_version = 'repository-exact-tree-literal-index/v1'
  ),
  manifest_status text NOT NULL CHECK (manifest_status = 'ready'),
  file_count integer NOT NULL CHECK (file_count BETWEEN 0 AND 20000),
  text_file_count integer NOT NULL CHECK (text_file_count BETWEEN 0 AND file_count),
  skipped_file_count integer NOT NULL CHECK (
    skipped_file_count BETWEEN 0 AND file_count
    AND text_file_count + skipped_file_count = file_count
  ),
  total_bytes bigint NOT NULL CHECK (total_bytes BETWEEN 0 AND 67108864),
  tree_commitment text NOT NULL CHECK (tree_commitment ~ '^sha256:[0-9a-f]{64}$'),
  index_commitment text NOT NULL CHECK (index_commitment ~ '^sha256:[0-9a-f]{64}$'),
  publication_created_at timestamptz NOT NULL,
  publication_ready_at timestamptz NOT NULL CHECK (
    publication_ready_at >= publication_created_at
  ),
  planned_rank integer NOT NULL CHECK (planned_rank > 8),
  planned_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  UNIQUE (
    run_id, project_id, tree_hash, publication_created_at, index_commitment
  ),
  CONSTRAINT repository_exact_tree_literal_index_gc_capability_time_check CHECK (
    expires_at > planned_at
    AND expires_at <= planned_at + interval '15 minutes'
  )
);

CREATE INDEX repository_exact_tree_literal_index_gc_capability_run_idx
  ON repository_exact_tree_literal_index_gc_capabilities (
    run_id, planned_rank, project_id, tree_hash, capability_id
  );

CREATE TABLE repository_exact_tree_literal_index_gc_receipts (
  receipt_id uuid PRIMARY KEY,
  capability_id uuid NOT NULL UNIQUE
    REFERENCES repository_exact_tree_literal_index_gc_capabilities(capability_id)
    ON DELETE RESTRICT,
  run_id uuid NOT NULL REFERENCES repository_exact_tree_literal_index_gc_runs(run_id)
    ON DELETE RESTRICT,
  project_id uuid NOT NULL,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  publication_created_at timestamptz NOT NULL,
  index_commitment text NOT NULL CHECK (index_commitment ~ '^sha256:[0-9a-f]{64}$'),
  outcome text NOT NULL CHECK (outcome IN ('deleted', 'protected', 'stale', 'expired')),
  deleted_member_count integer NOT NULL CHECK (deleted_member_count >= 0),
  deleted_blob_count integer NOT NULL CHECK (deleted_blob_count >= 0),
  logical_bytes_released bigint NOT NULL CHECK (logical_bytes_released >= 0),
  blob_bytes_freed bigint NOT NULL CHECK (blob_bytes_freed >= 0),
  executed_at timestamptz NOT NULL,
  executed_by text NOT NULL CHECK (length(executed_by) > 0),
  CONSTRAINT repository_exact_tree_literal_index_gc_receipt_outcome_shape CHECK (
    outcome = 'deleted'
    OR (
      deleted_member_count = 0
      AND deleted_blob_count = 0
      AND logical_bytes_released = 0
      AND blob_bytes_freed = 0
    )
  )
);

CREATE INDEX repository_exact_tree_literal_index_gc_receipt_run_idx
  ON repository_exact_tree_literal_index_gc_receipts (run_id, outcome, capability_id);

-- The primary identity includes both the immutable publication identity and
-- its one-shot capability.  Rebuilding the same project/tree after retention
-- therefore cannot collide with an earlier tombstone.
CREATE TABLE repository_exact_tree_literal_index_gc_tombstones (
  project_id uuid NOT NULL,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  publication_created_at timestamptz NOT NULL,
  index_commitment text NOT NULL CHECK (index_commitment ~ '^sha256:[0-9a-f]{64}$'),
  capability_id uuid NOT NULL UNIQUE
    REFERENCES repository_exact_tree_literal_index_gc_capabilities(capability_id)
    ON DELETE RESTRICT,
  receipt_id uuid NOT NULL UNIQUE
    REFERENCES repository_exact_tree_literal_index_gc_receipts(receipt_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  run_id uuid NOT NULL REFERENCES repository_exact_tree_literal_index_gc_runs(run_id)
    ON DELETE RESTRICT,
  deleted_member_count integer NOT NULL CHECK (deleted_member_count >= 0),
  deleted_blob_count integer NOT NULL CHECK (deleted_blob_count >= 0),
  logical_bytes_released bigint NOT NULL CHECK (logical_bytes_released >= 0),
  blob_bytes_freed bigint NOT NULL CHECK (blob_bytes_freed >= 0),
  deleted_at timestamptz NOT NULL,
  PRIMARY KEY (
    project_id, tree_hash, publication_created_at, index_commitment, capability_id
  )
);

-- These tables are not capabilities.  They are private, transaction-local
-- facts materialized in ordinary tables so mutation guards can require the
-- exact xid, backend, tenant, tree, publication, capability and blob.  A
-- successful executor removes them before returning; rollback/crash removes
-- their inserts with the rest of the transaction.
CREATE TABLE repository_exact_tree_literal_index_gc_tree_delete_auth (
  transaction_id bigint NOT NULL CHECK (transaction_id > 0),
  backend_pid integer NOT NULL CHECK (backend_pid > 0),
  capability_id uuid NOT NULL,
  project_id uuid NOT NULL,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  publication_created_at timestamptz NOT NULL,
  index_commitment text NOT NULL CHECK (index_commitment ~ '^sha256:[0-9a-f]{64}$'),
  PRIMARY KEY (transaction_id, backend_pid, capability_id, project_id, tree_hash)
);

CREATE TABLE repository_exact_tree_literal_index_gc_blob_delete_auth (
  transaction_id bigint NOT NULL CHECK (transaction_id > 0),
  backend_pid integer NOT NULL CHECK (backend_pid > 0),
  capability_id uuid NOT NULL,
  project_id uuid NOT NULL,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  byte_size bigint NOT NULL CHECK (byte_size BETWEEN 0 AND 4194304),
  is_text boolean NOT NULL,
  PRIMARY KEY (
    transaction_id, backend_pid, capability_id, project_id, tree_hash, content_hash
  )
);

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  RAISE EXCEPTION 'Exact-tree literal index GC audit is append-only'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER repository_exact_tree_literal_index_gc_run_append_only
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_gc_runs
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();
CREATE TRIGGER repository_exact_tree_literal_index_gc_run_no_truncate
BEFORE TRUNCATE ON repository_exact_tree_literal_index_gc_runs
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();

CREATE TRIGGER repository_exact_tree_literal_index_gc_capability_append_only
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_gc_capabilities
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();
CREATE TRIGGER repository_exact_tree_literal_index_gc_capability_no_truncate
BEFORE TRUNCATE ON repository_exact_tree_literal_index_gc_capabilities
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();

CREATE TRIGGER repository_exact_tree_literal_index_gc_receipt_append_only
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_gc_receipts
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();
CREATE TRIGGER repository_exact_tree_literal_index_gc_receipt_no_truncate
BEFORE TRUNCATE ON repository_exact_tree_literal_index_gc_receipts
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();

CREATE TRIGGER repository_exact_tree_literal_index_gc_tombstone_append_only
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_gc_tombstones
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();
CREATE TRIGGER repository_exact_tree_literal_index_gc_tombstone_no_truncate
BEFORE TRUNCATE ON repository_exact_tree_literal_index_gc_tombstones
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation();

-- Replace the original unconditional DELETE guards.  UPDATE and TRUNCATE stay
-- impossible; DELETE is admitted only for the exact executor transaction.
CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_blob_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF EXISTS (
      SELECT 1
      FROM repository_exact_tree_literal_index_gc_blob_delete_auth AS delete_auth
      WHERE delete_auth.transaction_id = txid_current()
        AND delete_auth.backend_pid = pg_backend_pid()
        AND delete_auth.project_id = OLD.project_id
        AND delete_auth.content_hash = OLD.content_hash
        AND delete_auth.byte_size = OLD.byte_size
        AND delete_auth.is_text = OLD.is_text
        AND EXISTS (
          SELECT 1
          FROM repository_exact_tree_literal_index_gc_tree_delete_auth AS tree_authorization
          WHERE tree_authorization.transaction_id = delete_auth.transaction_id
            AND tree_authorization.backend_pid = delete_auth.backend_pid
            AND tree_authorization.capability_id = delete_auth.capability_id
            AND tree_authorization.project_id = delete_auth.project_id
            AND tree_authorization.tree_hash = delete_auth.tree_hash
        )
    ) THEN
      RETURN OLD;
    END IF;
  END IF;

  RAISE EXCEPTION 'Exact-tree literal index blobs are immutable outside exact GC authorization'
    USING ERRCODE = '55000';
END;
$$;

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_member_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF EXISTS (
      SELECT 1
      FROM repository_exact_tree_literal_index_gc_tree_delete_auth AS delete_auth
      WHERE delete_auth.transaction_id = txid_current()
        AND delete_auth.backend_pid = pg_backend_pid()
        AND delete_auth.project_id = OLD.project_id
        AND delete_auth.tree_hash = OLD.tree_hash
    ) THEN
      RETURN OLD;
    END IF;
  END IF;

  RAISE EXCEPTION 'Exact-tree literal index members are immutable outside exact GC authorization'
    USING ERRCODE = '55000';
END;
$$;

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_manifest_delete()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF EXISTS (
      SELECT 1
      FROM repository_exact_tree_literal_index_gc_tree_delete_auth AS delete_auth
      WHERE delete_auth.transaction_id = txid_current()
        AND delete_auth.backend_pid = pg_backend_pid()
        AND delete_auth.project_id = OLD.project_id
        AND delete_auth.tree_hash = OLD.tree_hash
        AND delete_auth.publication_created_at = OLD.created_at
        AND delete_auth.index_commitment = OLD.index_commitment
    ) THEN
      RETURN OLD;
    END IF;
  END IF;

  RAISE EXCEPTION 'Exact-tree literal index manifests are immutable outside exact GC authorization'
    USING ERRCODE = '55000';
END;
$$;

-- Candidate current-tree references participate in the same advisory lock as
-- publication, lookup and retention.  Two-tree transitions take their shared
-- locks in bytewise key order to avoid OLD/NEW lock inversion.
CREATE OR REPLACE FUNCTION lock_candidate_exact_tree_literal_index_reference()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  old_key text;
  new_key text;
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM pg_advisory_xact_lock_shared(hashtextextended(
      NEW.project_id::text || '|' || NEW.current_tree_hash,
      0
    ));
    RETURN NEW;
  END IF;

  IF NEW.project_id IS NOT DISTINCT FROM OLD.project_id
     AND NEW.current_tree_hash IS NOT DISTINCT FROM OLD.current_tree_hash THEN
    RETURN NEW;
  END IF;

  old_key := OLD.project_id::text || '|' || OLD.current_tree_hash;
  new_key := NEW.project_id::text || '|' || NEW.current_tree_hash;
  IF old_key COLLATE "C" <= new_key COLLATE "C" THEN
    PERFORM pg_advisory_xact_lock_shared(hashtextextended(old_key, 0));
    IF new_key <> old_key THEN
      PERFORM pg_advisory_xact_lock_shared(hashtextextended(new_key, 0));
    END IF;
  ELSE
    PERFORM pg_advisory_xact_lock_shared(hashtextextended(new_key, 0));
    PERFORM pg_advisory_xact_lock_shared(hashtextextended(old_key, 0));
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_workspace_exact_tree_literal_index_reference_lock
BEFORE INSERT OR UPDATE ON candidate_workspaces
FOR EACH ROW EXECUTE FUNCTION lock_candidate_exact_tree_literal_index_reference();

CREATE OR REPLACE FUNCTION plan_repository_exact_tree_literal_index_gc(
  target_run_id uuid,
  target_retention_milliseconds bigint,
  target_keep_per_project integer,
  target_batch_size integer,
  target_capability_ttl_milliseconds integer
)
RETURNS TABLE (
  run_id uuid,
  capability_id uuid,
  project_id uuid,
  tree_hash text,
  publication_created_at timestamptz,
  index_commitment text,
  planned_rank integer,
  expires_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  observed_at timestamptz := clock_timestamp();
  existing_run repository_exact_tree_literal_index_gc_runs%ROWTYPE;
BEGIN
  IF target_run_id IS NULL
     OR target_retention_milliseconds IS NULL
     OR target_retention_milliseconds < 604800000
     OR target_keep_per_project IS NULL
     OR target_keep_per_project < 8
     OR target_batch_size IS NULL
     OR target_batch_size < 1
     OR target_batch_size > 100
     OR target_capability_ttl_milliseconds IS NULL
     OR target_capability_ttl_milliseconds < 1
     OR target_capability_ttl_milliseconds > 900000 THEN
    RAISE EXCEPTION 'Exact-tree literal index GC plan input is invalid'
      USING ERRCODE = '22023';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtextextended(
    target_run_id::text || '|repository-exact-tree-literal-index-gc-plan',
    0
  ));

  SELECT run.*
  INTO existing_run
  FROM repository_exact_tree_literal_index_gc_runs AS run
  WHERE run.run_id = target_run_id
  FOR UPDATE;

  IF FOUND THEN
    IF existing_run.retention_milliseconds <> target_retention_milliseconds
       OR existing_run.keep_per_project <> target_keep_per_project
       OR existing_run.batch_size <> target_batch_size
       OR existing_run.capability_ttl_milliseconds <> target_capability_ttl_milliseconds THEN
      RAISE EXCEPTION 'Exact-tree literal index GC run id was reused with different parameters'
        USING ERRCODE = '22023';
    END IF;

    RETURN QUERY
    SELECT
      capability.run_id,
      capability.capability_id,
      capability.project_id,
      capability.tree_hash,
      capability.publication_created_at,
      capability.index_commitment,
      capability.planned_rank,
      capability.expires_at
    FROM repository_exact_tree_literal_index_gc_capabilities AS capability
    WHERE capability.run_id = target_run_id
    ORDER BY capability.planned_rank, capability.project_id, capability.tree_hash;
    RETURN;
  END IF;

  INSERT INTO repository_exact_tree_literal_index_gc_runs (
    run_id, retention_milliseconds, keep_per_project, batch_size,
    capability_ttl_milliseconds, planned_at, cutoff_at, planned_by
  ) VALUES (
    target_run_id, target_retention_milliseconds, target_keep_per_project,
    target_batch_size, target_capability_ttl_milliseconds, observed_at,
    observed_at - make_interval(
      secs => target_retention_milliseconds::double precision / 1000.0
    ),
    session_user::text
  );

  WITH ranked AS MATERIALIZED (
    SELECT
      manifest.*,
      row_number() OVER (
        PARTITION BY manifest.project_id
        ORDER BY manifest.ready_at DESC, manifest.created_at DESC,
                 manifest.tree_hash COLLATE "C" DESC
      ) AS retention_rank
    FROM repository_exact_tree_literal_index_manifests AS manifest
    WHERE manifest.status = 'ready'
  ), eligible AS MATERIALIZED (
    SELECT ranked.*
    FROM ranked
    WHERE ranked.retention_rank > target_keep_per_project
      AND ranked.ready_at <= observed_at - make_interval(
        secs => target_retention_milliseconds::double precision / 1000.0
      )
      AND NOT EXISTS (
        SELECT 1
        FROM candidate_workspaces AS candidate
        WHERE candidate.project_id = ranked.project_id
          AND candidate.current_tree_hash = ranked.tree_hash
      )
      AND NOT EXISTS (
        SELECT 1
        FROM repository_exact_tree_literal_index_build_claims AS claim
        WHERE claim.project_id = ranked.project_id
          AND claim.tree_hash = ranked.tree_hash
          AND claim.lease_expires_at > observed_at
      )
    ORDER BY ranked.ready_at, ranked.created_at, ranked.project_id, ranked.tree_hash COLLATE "C"
    LIMIT target_batch_size
  )
  INSERT INTO repository_exact_tree_literal_index_gc_capabilities (
    capability_id, run_id, project_id, tree_hash, schema_version, manifest_status,
    file_count, text_file_count, skipped_file_count, total_bytes,
    tree_commitment, index_commitment, publication_created_at,
    publication_ready_at, planned_rank, planned_at, expires_at
  )
  SELECT
    md5(
      target_run_id::text || '|' || eligible.project_id::text || '|' ||
      eligible.tree_hash || '|' || eligible.created_at::text || '|' ||
      eligible.index_commitment
    )::uuid,
    target_run_id, eligible.project_id, eligible.tree_hash, eligible.schema_version,
    eligible.status, eligible.file_count, eligible.text_file_count,
    eligible.skipped_file_count, eligible.total_bytes, eligible.tree_commitment,
    eligible.index_commitment, eligible.created_at, eligible.ready_at,
    eligible.retention_rank::integer, observed_at,
    observed_at + make_interval(
      secs => target_capability_ttl_milliseconds::double precision / 1000.0
    )
  FROM eligible;

  RETURN QUERY
  SELECT
    capability.run_id,
    capability.capability_id,
    capability.project_id,
    capability.tree_hash,
    capability.publication_created_at,
    capability.index_commitment,
    capability.planned_rank,
    capability.expires_at
  FROM repository_exact_tree_literal_index_gc_capabilities AS capability
  WHERE capability.run_id = target_run_id
  ORDER BY capability.planned_rank, capability.project_id, capability.tree_hash;
END;
$$;

CREATE OR REPLACE FUNCTION execute_repository_exact_tree_literal_index_gc(
  target_capability_id uuid
)
RETURNS TABLE (
  receipt_id uuid,
  capability_id uuid,
  run_id uuid,
  project_id uuid,
  tree_hash text,
  publication_created_at timestamptz,
  index_commitment text,
  outcome text,
  deleted_member_count integer,
  deleted_blob_count integer,
  logical_bytes_released bigint,
  blob_bytes_freed bigint,
  executed_at timestamptz,
  idempotent boolean
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  capability repository_exact_tree_literal_index_gc_capabilities%ROWTYPE;
  gc_run repository_exact_tree_literal_index_gc_runs%ROWTYPE;
  manifest repository_exact_tree_literal_index_manifests%ROWTYPE;
  build_claim repository_exact_tree_literal_index_build_claims%ROWTYPE;
  build_claim_found boolean := false;
  observed_at timestamptz;
  current_rank bigint;
  removed_claims integer := 0;
  removed_members integer := 0;
  removed_blobs integer := 0;
  freed_blob_bytes bigint := 0;
  terminal_outcome text;
  generated_receipt_id uuid;
BEGIN
  IF target_capability_id IS NULL THEN
    RAISE EXCEPTION 'Exact-tree literal index GC capability is required'
      USING ERRCODE = '22023';
  END IF;

  -- Establish the parent relation lock before even the discovery read of the
  -- child capability.  This makes every executor compatible with the
  -- downgrade's runs-then-capabilities ACCESS EXCLUSIVE lock order.
  LOCK TABLE repository_exact_tree_literal_index_gc_runs IN ROW SHARE MODE;

  -- This first read discovers the immutable advisory-lock identity.  It does
  -- not authorize mutation; the row is locked and re-read only after both
  -- advisory locks have been acquired in the global lock order.
  SELECT candidate_capability.*
  INTO capability
  FROM repository_exact_tree_literal_index_gc_capabilities AS candidate_capability
  WHERE candidate_capability.capability_id = target_capability_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Exact-tree literal index GC capability does not exist'
      USING ERRCODE = '22023';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtextextended(
    capability.project_id::text || '|' || capability.tree_hash,
    0
  ));
  PERFORM pg_advisory_xact_lock(hashtextextended(
    capability.project_id::text || '|repository-exact-tree-literal-index-project-quota',
    0
  ));

  -- Lock the immutable run before its capability.  The downgrade takes
  -- ACCESS EXCLUSIVE locks in this same parent-to-child order, so an in-flight
  -- executor can neither disappear under the downgrade nor deadlock it by
  -- holding the child while waiting for the parent.
  SELECT candidate_run.*
  INTO gc_run
  FROM repository_exact_tree_literal_index_gc_runs AS candidate_run
  WHERE candidate_run.run_id = capability.run_id
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Exact-tree literal index GC run disappeared while locked'
      USING ERRCODE = '55000';
  END IF;

  SELECT candidate_capability.*
  INTO capability
  FROM repository_exact_tree_literal_index_gc_capabilities AS candidate_capability
  WHERE candidate_capability.capability_id = target_capability_id
  FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Exact-tree literal index GC capability disappeared while locked'
      USING ERRCODE = '55000';
  END IF;

  -- Every terminal outcome is one immutable receipt.  A retry after an
  -- ambiguous commit, including after capability expiry, replays it exactly.
  RETURN QUERY
  SELECT
    receipt.receipt_id, receipt.capability_id, receipt.run_id,
    receipt.project_id, receipt.tree_hash, receipt.publication_created_at,
    receipt.index_commitment, receipt.outcome, receipt.deleted_member_count,
    receipt.deleted_blob_count, receipt.logical_bytes_released,
    receipt.blob_bytes_freed, receipt.executed_at, true
  FROM repository_exact_tree_literal_index_gc_receipts AS receipt
  WHERE receipt.capability_id = target_capability_id;
  IF FOUND THEN
    RETURN;
  END IF;

  -- renew_repository_exact_tree_literal_index_build_claim updates this row
  -- without taking the tree/quota advisory locks.  A plain MVCC EXISTS could
  -- therefore observe the expired predecessor tuple while a successful
  -- renewal is still uncommitted, then let DELETE recheck to zero rows after
  -- that renewal commits.  Lock the exact row and take the decision timestamp
  -- only after the lock is granted.  A renewal that committed first is always
  -- visible and protects this publication; a renewal waiting behind this lock
  -- cannot report success if the expired row is deleted below.
  SELECT claim.*
  INTO build_claim
  FROM repository_exact_tree_literal_index_build_claims AS claim
  WHERE claim.project_id = capability.project_id
    AND claim.tree_hash = capability.tree_hash
  FOR UPDATE;
  build_claim_found := FOUND;

  observed_at := clock_timestamp();
  generated_receipt_id := md5(target_capability_id::text || '|gc-receipt')::uuid;

  IF capability.expires_at <= observed_at THEN
    terminal_outcome := 'expired';
  ELSE
    SELECT current_manifest.*
    INTO manifest
    FROM repository_exact_tree_literal_index_manifests AS current_manifest
    WHERE current_manifest.project_id = capability.project_id
      AND current_manifest.tree_hash = capability.tree_hash
    FOR UPDATE;

    IF NOT FOUND
       OR manifest.schema_version IS DISTINCT FROM capability.schema_version
       OR manifest.status IS DISTINCT FROM capability.manifest_status
       OR manifest.file_count IS DISTINCT FROM capability.file_count
       OR manifest.text_file_count IS DISTINCT FROM capability.text_file_count
       OR manifest.skipped_file_count IS DISTINCT FROM capability.skipped_file_count
       OR manifest.total_bytes IS DISTINCT FROM capability.total_bytes
       OR manifest.tree_commitment IS DISTINCT FROM capability.tree_commitment
       OR manifest.index_commitment IS DISTINCT FROM capability.index_commitment
       OR manifest.created_at IS DISTINCT FROM capability.publication_created_at
       OR manifest.ready_at IS DISTINCT FROM capability.publication_ready_at THEN
      terminal_outcome := 'stale';
    ELSE
      SELECT ranked.retention_rank
      INTO current_rank
      FROM (
        SELECT
          current_manifest.tree_hash,
          current_manifest.created_at,
          current_manifest.index_commitment,
          row_number() OVER (
            PARTITION BY current_manifest.project_id
            ORDER BY current_manifest.ready_at DESC, current_manifest.created_at DESC,
                     current_manifest.tree_hash COLLATE "C" DESC
          ) AS retention_rank
        FROM repository_exact_tree_literal_index_manifests AS current_manifest
        WHERE current_manifest.project_id = capability.project_id
          AND current_manifest.status = 'ready'
      ) AS ranked
      WHERE ranked.tree_hash = capability.tree_hash
        AND ranked.created_at = capability.publication_created_at
        AND ranked.index_commitment = capability.index_commitment;

      IF current_rank IS NULL
         OR current_rank <= gc_run.keep_per_project
         OR manifest.ready_at > gc_run.cutoff_at THEN
        terminal_outcome := 'stale';
      ELSIF EXISTS (
        SELECT 1
        FROM candidate_workspaces AS candidate
        WHERE candidate.project_id = capability.project_id
          AND candidate.current_tree_hash = capability.tree_hash
      ) OR (
        build_claim_found AND build_claim.lease_expires_at > observed_at
      ) THEN
        terminal_outcome := 'protected';
      END IF;
    END IF;
  END IF;

  IF terminal_outcome IS NOT NULL THEN
    INSERT INTO repository_exact_tree_literal_index_gc_receipts (
      receipt_id, capability_id, run_id, project_id, tree_hash,
      publication_created_at, index_commitment, outcome,
      deleted_member_count, deleted_blob_count, logical_bytes_released,
      blob_bytes_freed, executed_at, executed_by
    ) VALUES (
      generated_receipt_id, capability.capability_id, capability.run_id,
      capability.project_id, capability.tree_hash, capability.publication_created_at,
      capability.index_commitment, terminal_outcome, 0, 0, 0, 0,
      observed_at, session_user::text
    );

    RETURN QUERY
    SELECT
      receipt.receipt_id, receipt.capability_id, receipt.run_id,
      receipt.project_id, receipt.tree_hash, receipt.publication_created_at,
      receipt.index_commitment, receipt.outcome, receipt.deleted_member_count,
      receipt.deleted_blob_count, receipt.logical_bytes_released,
      receipt.blob_bytes_freed, receipt.executed_at, false
    FROM repository_exact_tree_literal_index_gc_receipts AS receipt
    WHERE receipt.capability_id = target_capability_id;
    RETURN;
  END IF;

  -- An expired coordination row is not publication authority.  Remove it
  -- while both tenant/tree and project-quota locks are held so a later build
  -- starts from an unambiguous quota state.
  IF build_claim_found THEN
    IF build_claim.lease_expires_at > observed_at THEN
      RAISE EXCEPTION 'Exact-tree literal index GC reached deletion with a live build claim'
        USING ERRCODE = '55000';
    END IF;

    DELETE FROM repository_exact_tree_literal_index_build_claims AS claim
    WHERE claim.project_id = capability.project_id
      AND claim.tree_hash = capability.tree_hash
      AND claim.owner_token = build_claim.owner_token
      AND claim.attempt = build_claim.attempt
      AND claim.reserved_source_bytes = build_claim.reserved_source_bytes
      AND claim.acquired_at = build_claim.acquired_at
      AND claim.renewed_at = build_claim.renewed_at
      AND claim.lease_expires_at = build_claim.lease_expires_at
      AND claim.lease_expires_at <= observed_at;
    GET DIAGNOSTICS removed_claims = ROW_COUNT;
    IF removed_claims <> 1 THEN
      RAISE EXCEPTION 'Exact-tree literal index GC expired build-claim CAS mismatch'
        USING ERRCODE = '55000';
    END IF;
  END IF;

  INSERT INTO repository_exact_tree_literal_index_gc_tree_delete_auth (
    transaction_id, backend_pid, capability_id, project_id, tree_hash,
    publication_created_at, index_commitment
  ) VALUES (
    txid_current(), pg_backend_pid(), capability.capability_id,
    capability.project_id, capability.tree_hash,
    capability.publication_created_at, capability.index_commitment
  );

  INSERT INTO repository_exact_tree_literal_index_gc_blob_delete_auth (
    transaction_id, backend_pid, capability_id, project_id, tree_hash,
    content_hash, byte_size, is_text
  )
  SELECT DISTINCT
    txid_current(), pg_backend_pid(), capability.capability_id,
    member.project_id, member.tree_hash, member.content_hash,
    member.byte_size, member.indexed
  FROM repository_exact_tree_literal_index_members AS member
  WHERE member.project_id = capability.project_id
    AND member.tree_hash = capability.tree_hash;

  DELETE FROM repository_exact_tree_literal_index_members AS member
  WHERE member.project_id = capability.project_id
    AND member.tree_hash = capability.tree_hash;
  GET DIAGNOSTICS removed_members = ROW_COUNT;
  IF removed_members <> capability.file_count THEN
    RAISE EXCEPTION 'Exact-tree literal index GC member CAS mismatch'
      USING ERRCODE = '55000';
  END IF;

  DELETE FROM repository_exact_tree_literal_index_manifests AS current_manifest
  WHERE current_manifest.project_id = capability.project_id
    AND current_manifest.tree_hash = capability.tree_hash
    AND current_manifest.schema_version = capability.schema_version
    AND current_manifest.status = capability.manifest_status
    AND current_manifest.file_count = capability.file_count
    AND current_manifest.text_file_count = capability.text_file_count
    AND current_manifest.skipped_file_count = capability.skipped_file_count
    AND current_manifest.total_bytes = capability.total_bytes
    AND current_manifest.tree_commitment = capability.tree_commitment
    AND current_manifest.index_commitment = capability.index_commitment
    AND current_manifest.created_at = capability.publication_created_at
    AND current_manifest.ready_at = capability.publication_ready_at;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Exact-tree literal index GC manifest CAS mismatch'
      USING ERRCODE = '55000';
  END IF;

  WITH deleted AS (
    DELETE FROM repository_exact_tree_literal_index_blobs AS blob
    USING repository_exact_tree_literal_index_gc_blob_delete_auth AS delete_auth
    WHERE delete_auth.transaction_id = txid_current()
      AND delete_auth.backend_pid = pg_backend_pid()
      AND delete_auth.capability_id = capability.capability_id
      AND delete_auth.project_id = capability.project_id
      AND delete_auth.tree_hash = capability.tree_hash
      AND blob.project_id = delete_auth.project_id
      AND blob.content_hash = delete_auth.content_hash
      AND blob.byte_size = delete_auth.byte_size
      AND blob.is_text = delete_auth.is_text
      AND NOT EXISTS (
        SELECT 1
        FROM repository_exact_tree_literal_index_members AS remaining_member
        WHERE remaining_member.project_id = blob.project_id
          AND remaining_member.content_hash = blob.content_hash
      )
    RETURNING blob.byte_size
  )
  SELECT count(*)::integer, COALESCE(sum(deleted.byte_size), 0)::bigint
  INTO removed_blobs, freed_blob_bytes
  FROM deleted;

  INSERT INTO repository_exact_tree_literal_index_gc_receipts (
    receipt_id, capability_id, run_id, project_id, tree_hash,
    publication_created_at, index_commitment, outcome,
    deleted_member_count, deleted_blob_count, logical_bytes_released,
    blob_bytes_freed, executed_at, executed_by
  ) VALUES (
    generated_receipt_id, capability.capability_id, capability.run_id,
    capability.project_id, capability.tree_hash, capability.publication_created_at,
    capability.index_commitment, 'deleted', removed_members, removed_blobs,
    capability.total_bytes, freed_blob_bytes, observed_at, session_user::text
  );

  INSERT INTO repository_exact_tree_literal_index_gc_tombstones (
    project_id, tree_hash, publication_created_at, index_commitment,
    capability_id, receipt_id, run_id, deleted_member_count,
    deleted_blob_count, logical_bytes_released, blob_bytes_freed, deleted_at
  ) VALUES (
    capability.project_id, capability.tree_hash, capability.publication_created_at,
    capability.index_commitment, capability.capability_id, generated_receipt_id,
    capability.run_id, removed_members, removed_blobs, capability.total_bytes,
    freed_blob_bytes, observed_at
  );

  DELETE FROM repository_exact_tree_literal_index_gc_blob_delete_auth AS delete_auth
  WHERE delete_auth.transaction_id = txid_current()
    AND delete_auth.backend_pid = pg_backend_pid()
    AND delete_auth.capability_id = capability.capability_id;
  DELETE FROM repository_exact_tree_literal_index_gc_tree_delete_auth AS delete_auth
  WHERE delete_auth.transaction_id = txid_current()
    AND delete_auth.backend_pid = pg_backend_pid()
    AND delete_auth.capability_id = capability.capability_id;

  RETURN QUERY
  SELECT
    receipt.receipt_id, receipt.capability_id, receipt.run_id,
    receipt.project_id, receipt.tree_hash, receipt.publication_created_at,
    receipt.index_commitment, receipt.outcome, receipt.deleted_member_count,
    receipt.deleted_blob_count, receipt.logical_bytes_released,
    receipt.blob_bytes_freed, receipt.executed_at, false
  FROM repository_exact_tree_literal_index_gc_receipts AS receipt
  WHERE receipt.capability_id = target_capability_id;
END;
$$;

CREATE OR REPLACE FUNCTION inspect_repository_exact_tree_literal_index_gc_run(
  target_run_id uuid
)
RETURNS TABLE (
  run_id uuid,
  run_status text,
  planned_at timestamptz,
  cutoff_at timestamptz,
  keep_per_project integer,
  batch_size integer,
  capability_ttl_milliseconds integer,
  planned_capability_count integer,
  deleted_capability_count integer,
  protected_capability_count integer,
  stale_capability_count integer,
  expired_capability_count integer,
  pending_capability_count integer,
  logical_bytes_released bigint,
  blob_bytes_freed bigint
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  IF target_run_id IS NULL OR NOT EXISTS (
    SELECT 1
    FROM repository_exact_tree_literal_index_gc_runs AS existing_run
    WHERE existing_run.run_id = target_run_id
  ) THEN
    RAISE EXCEPTION 'Exact-tree literal index GC run does not exist'
      USING ERRCODE = '22023';
  END IF;

  RETURN QUERY
  WITH summary AS (
    SELECT
      run.run_id,
      run.planned_at,
      run.cutoff_at,
      run.keep_per_project,
      run.batch_size,
      run.capability_ttl_milliseconds,
      count(capability.capability_id)::integer AS planned_count,
      count(receipt.capability_id) FILTER (WHERE receipt.outcome = 'deleted')::integer
        AS deleted_count,
      count(receipt.capability_id) FILTER (WHERE receipt.outcome = 'protected')::integer
        AS protected_count,
      count(receipt.capability_id) FILTER (WHERE receipt.outcome = 'stale')::integer
        AS stale_count,
      count(receipt.capability_id) FILTER (WHERE receipt.outcome = 'expired')::integer
        AS expired_count,
      count(capability.capability_id) FILTER (WHERE receipt.capability_id IS NULL)::integer
        AS pending_count,
      COALESCE(sum(receipt.logical_bytes_released), 0)::bigint AS released_bytes,
      COALESCE(sum(receipt.blob_bytes_freed), 0)::bigint AS freed_bytes
    FROM repository_exact_tree_literal_index_gc_runs AS run
    LEFT JOIN repository_exact_tree_literal_index_gc_capabilities AS capability
      ON capability.run_id = run.run_id
    LEFT JOIN repository_exact_tree_literal_index_gc_receipts AS receipt
      ON receipt.capability_id = capability.capability_id
    WHERE run.run_id = target_run_id
    GROUP BY run.run_id
  )
  SELECT
    summary.run_id,
    CASE
      WHEN summary.pending_count = 0 THEN 'completed'
      WHEN summary.planned_count = summary.pending_count THEN 'planned'
      ELSE 'partially_executed'
    END,
    summary.planned_at,
    summary.cutoff_at,
    summary.keep_per_project,
    summary.batch_size,
    summary.capability_ttl_milliseconds,
    summary.planned_count,
    summary.deleted_count,
    summary.protected_count,
    summary.stale_count,
    summary.expired_count,
    summary.pending_count,
    summary.released_bytes,
    summary.freed_bytes
  FROM summary;
END;
$$;

CREATE OR REPLACE FUNCTION repository_exact_tree_literal_index_gc_readiness()
RETURNS TABLE (
  ready boolean,
  reason text,
  trusted_schema text,
  operator_role_exists boolean,
  application_role_exists boolean,
  migration_owner_role_exists boolean,
  stable_group_roles_safe boolean,
  objects_owned_by_migration_owner boolean,
  operator_execute_granted boolean,
  application_claim_execute_granted boolean,
  application_schema_head_read_granted boolean,
  public_claim_execute_revoked boolean,
  public_schema_create_revoked boolean
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  schema_name text;
  operator_oid oid;
  application_oid oid;
  migration_owner_oid oid;
  gc_function_oids oid[];
  claim_function_oids oid[];
  candidate_function_oids oid[];
  boundary_function_oids oid[];
  internal_function_oids oid[];
  sandbox_checkpoint_helper_oid oid;
  schema_head_oid oid;
  boundary_owners oid[];
  boundary_table_count integer := 0;
  boundary_index_count integer := 0;
  required_index_count integer := 0;
  expected_trigger_count integer := 0;
  actual_boundary_trigger_count integer := 0;
  role_boundary_ok boolean := false;
  owner_boundary_ok boolean := false;
  exact_index_contract_ok boolean := false;
  trigger_contract_ok boolean := false;
  application_table_boundary_ok boolean := false;
  application_function_acl_exact boolean := false;
  application_candidate_execute_ok boolean := false;
  application_definer_boundary_ok boolean := false;
  internal_function_acl_exact boolean := false;
  sandbox_checkpoint_dependency_ok boolean := false;
  public_candidate_execute_revoked boolean := false;
  public_routine_execute_revoked boolean := false;
  public_relation_acl_revoked boolean := false;
  column_acl_boundary_ok boolean := false;
  future_public_routine_default_revoked boolean := false;
  operator_routine_boundary_ok boolean := false;
  gc_private_table_boundary_ok boolean := false;
  gc_acl_only_operator boolean := false;
BEGIN
  SELECT namespace.nspname
  INTO schema_name
  FROM pg_class AS relation
  JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
  WHERE relation.oid = 'repository_exact_tree_literal_index_gc_runs'::regclass;

  SELECT role.oid INTO operator_oid
  FROM pg_roles AS role
  WHERE role.rolname = 'worksflow_repository_index_gc_operator';
  SELECT role.oid INTO application_oid
  FROM pg_roles AS role
  WHERE role.rolname = 'worksflow_application';
  SELECT role.oid INTO migration_owner_oid
  FROM pg_roles AS role
  WHERE role.rolname = 'worksflow_migration_owner';

  -- The migration runner owns the schema ledger lifecycle; raw, transactional
  -- migration fixtures intentionally apply only the embedded SQL chain.  Do
  -- not make function invocation fail when that external ledger is absent,
  -- but keep production readiness false until the exact trusted-schema table
  -- exists and has the application read-only ACL below.
  SELECT relation.oid
  INTO schema_head_oid
  FROM pg_class AS relation
  JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
  WHERE namespace.nspname = schema_name
    AND relation.relname = 'schema_migrations'
    AND relation.relkind = 'r';

  SELECT array_agg(procedure.oid ORDER BY procedure.oid)
  INTO gc_function_oids
  FROM pg_proc AS procedure
  JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
  WHERE namespace.nspname = schema_name
    AND procedure.oid IN (
      'plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)'::regprocedure,
      'execute_repository_exact_tree_literal_index_gc(uuid)'::regprocedure,
      'inspect_repository_exact_tree_literal_index_gc_run(uuid)'::regprocedure,
      'repository_exact_tree_literal_index_gc_readiness()'::regprocedure
    );

  SELECT array_agg(procedure.oid ORDER BY procedure.oid)
  INTO claim_function_oids
  FROM pg_proc AS procedure
  WHERE procedure.oid IN (
    'acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer,integer,bigint,integer)'::regprocedure,
    'renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)'::regprocedure,
    'release_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint)'::regprocedure
  );

  SELECT array_agg(procedure.oid ORDER BY procedure.oid)
  INTO candidate_function_oids
  FROM pg_proc AS procedure
  WHERE procedure.oid IN (
    'acquire_candidate_workspace_lease(uuid,bigint,uuid,integer)'::regprocedure,
    'rotate_candidate_workspace_session(uuid,bigint,bigint,uuid)'::regprocedure,
    'update_candidate_workspace_flags(uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text)'::regprocedure,
    'freeze_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)'::regprocedure,
    'abandon_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)'::regprocedure,
    'abandon_sandbox_session_candidate(uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid)'::regprocedure,
    'complete_abandoned_sandbox_session(uuid,bigint,bigint,uuid)'::regprocedure
  );

  SELECT array_agg(procedure.oid ORDER BY procedure.oid)
  INTO boundary_function_oids
  FROM pg_proc AS procedure
  WHERE procedure.oid IN (
    'guard_repository_exact_tree_literal_index_gc_audit_mutation()'::regprocedure,
    'guard_repository_exact_tree_literal_index_blob_mutation()'::regprocedure,
    'guard_repository_exact_tree_literal_index_member_mutation()'::regprocedure,
    'guard_repository_exact_tree_literal_index_manifest_delete()'::regprocedure,
    'guard_repository_exact_tree_literal_index_manifest_insert()'::regprocedure,
    'guard_repository_exact_tree_literal_index_member_insert()'::regprocedure,
    'publish_repository_exact_tree_literal_index_manifest()'::regprocedure,
    'lock_candidate_exact_tree_literal_index_reference()'::regprocedure,
    'plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)'::regprocedure,
    'execute_repository_exact_tree_literal_index_gc(uuid)'::regprocedure,
    'inspect_repository_exact_tree_literal_index_gc_run(uuid)'::regprocedure,
    'repository_exact_tree_literal_index_gc_readiness()'::regprocedure,
    'acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer,integer,bigint,integer)'::regprocedure,
    'renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)'::regprocedure,
    'release_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint)'::regprocedure,
    'acquire_candidate_workspace_lease(uuid,bigint,uuid,integer)'::regprocedure,
    'rotate_candidate_workspace_session(uuid,bigint,bigint,uuid)'::regprocedure,
    'update_candidate_workspace_flags(uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text)'::regprocedure,
    'freeze_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)'::regprocedure,
    'abandon_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)'::regprocedure,
    'abandon_sandbox_session_candidate(uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid)'::regprocedure,
    'complete_abandoned_sandbox_session(uuid,bigint,bigint,uuid)'::regprocedure,
    'sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)'::regprocedure
  );

  SELECT array_agg(procedure.oid ORDER BY procedure.oid)
  INTO internal_function_oids
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
  );
  sandbox_checkpoint_helper_oid :=
    'sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)'::regprocedure;

  WITH boundary_tables(table_oid) AS (
    SELECT relation.oid
    FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = schema_name
      AND relation.relkind = 'r'
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
      )
  )
  SELECT
    (SELECT count(*)::integer FROM boundary_tables),
    (SELECT count(*)::integer
     FROM pg_index AS index_metadata
     JOIN boundary_tables ON boundary_tables.table_oid = index_metadata.indrelid)
  INTO boundary_table_count, boundary_index_count;

  -- Validate the seven planner-critical indexes by their real pg_index
  -- structure.  pg_get_indexdef is used only for individual key expressions,
  -- while operator classes are tied to their owning extension (which may be
  -- relocated) rather than to search_path-dependent rendered SQL.  A
  -- same-named index on the wrong relation, wrong columns/collations,
  -- predicate, opclasses, or invalid/not-ready state cannot satisfy this.
  -- Two predecessor DDL names exceeded NAMEDATALEN; their expected identities
  -- are the exact 63-byte catalog names PostgreSQL created.
  WITH expected(
    index_oid, table_oid, access_method, indkey_text,
    key_count, attribute_count, key_definitions,
    opclass_names, opclass_namespaces, opclass_extensions,
    collation_names, collation_namespaces, index_expressions, predicate
  ) AS (
    VALUES
      (
        to_regclass('repository_exact_tree_literal_index_blob_project_body_trgm_idx'),
        'repository_exact_tree_literal_index_blobs'::regclass,
        'gin', '1 5', 2, 2, ARRAY['project_id', 'body'],
        ARRAY['uuid_ops', 'gin_trgm_ops'], ARRAY[NULL::text, NULL::text],
        ARRAY['btree_gin', 'pg_trgm'], ARRAY[NULL::text, 'C'],
        ARRAY[NULL::text, 'pg_catalog'], NULL::text, 'is_text'
      ),
      (
        to_regclass('repository_exact_tree_literal_index_blob_project_ascii_body_trg'),
        'repository_exact_tree_literal_index_blobs'::regclass,
        'gin', '1 0', 2, 2,
        ARRAY[
          'project_id',
          'translate(body COLLATE "C", ''ABCDEFGHIJKLMNOPQRSTUVWXYZ''::text, ''abcdefghijklmnopqrstuvwxyz''::text)'
        ],
        ARRAY['uuid_ops', 'gin_trgm_ops'], ARRAY[NULL::text, NULL::text],
        ARRAY['btree_gin', 'pg_trgm'], ARRAY[NULL::text, 'C'],
        ARRAY[NULL::text, 'pg_catalog'],
        'translate(body COLLATE "C", ''ABCDEFGHIJKLMNOPQRSTUVWXYZ''::text, ''abcdefghijklmnopqrstuvwxyz''::text)',
        'is_text'
      ),
      (
        to_regclass('repository_exact_tree_literal_index_member_blob_idx'),
        'repository_exact_tree_literal_index_members'::regclass,
        'btree', '1 5 6 7 2 3', 6, 6,
        ARRAY['project_id', 'content_hash', 'byte_size', 'indexed', 'tree_hash', 'path'],
        ARRAY['uuid_ops', 'text_ops', 'int8_ops', 'bool_ops', 'text_ops', 'text_ops'],
        ARRAY['pg_catalog', 'pg_catalog', 'pg_catalog', 'pg_catalog', 'pg_catalog', 'pg_catalog'],
        ARRAY[NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::text],
        ARRAY[NULL::text, 'default', NULL::text, NULL::text, 'default', 'C'],
        ARRAY[NULL::text, 'pg_catalog', NULL::text, NULL::text, 'pg_catalog', 'pg_catalog'],
        NULL::text, NULL::text
      ),
      (
        to_regclass('repository_exact_tree_literal_index_build_claim_expiry_idx'),
        'repository_exact_tree_literal_index_build_claims'::regclass,
        'btree', '7 1 2', 3, 3,
        ARRAY['lease_expires_at', 'project_id', 'tree_hash'],
        ARRAY['timestamptz_ops', 'uuid_ops', 'text_ops'],
        ARRAY['pg_catalog', 'pg_catalog', 'pg_catalog'],
        ARRAY[NULL::text, NULL::text, NULL::text],
        ARRAY[NULL::text, NULL::text, 'default'],
        ARRAY[NULL::text, NULL::text, 'pg_catalog'], NULL::text, NULL::text
      ),
      (
        to_regclass('repository_exact_tree_literal_index_build_claim_project_quota_i'),
        'repository_exact_tree_literal_index_build_claims'::regclass,
        'btree', '1 7 2 8', 3, 4,
        ARRAY['project_id', 'lease_expires_at', 'tree_hash', 'reserved_source_bytes'],
        ARRAY['uuid_ops', 'timestamptz_ops', 'text_ops'],
        ARRAY['pg_catalog', 'pg_catalog', 'pg_catalog'],
        ARRAY[NULL::text, NULL::text, NULL::text],
        ARRAY[NULL::text, NULL::text, 'default'],
        ARRAY[NULL::text, NULL::text, 'pg_catalog'], NULL::text, NULL::text
      ),
      (
        to_regclass('repository_exact_tree_literal_index_gc_capability_run_idx'),
        'repository_exact_tree_literal_index_gc_capabilities'::regclass,
        'btree', '2 15 3 4 1', 5, 5,
        ARRAY['run_id', 'planned_rank', 'project_id', 'tree_hash', 'capability_id'],
        ARRAY['uuid_ops', 'int4_ops', 'uuid_ops', 'text_ops', 'uuid_ops'],
        ARRAY['pg_catalog', 'pg_catalog', 'pg_catalog', 'pg_catalog', 'pg_catalog'],
        ARRAY[NULL::text, NULL::text, NULL::text, NULL::text, NULL::text],
        ARRAY[NULL::text, NULL::text, NULL::text, 'default', NULL::text],
        ARRAY[NULL::text, NULL::text, NULL::text, 'pg_catalog', NULL::text],
        NULL::text, NULL::text
      ),
      (
        to_regclass('repository_exact_tree_literal_index_gc_receipt_run_idx'),
        'repository_exact_tree_literal_index_gc_receipts'::regclass,
        'btree', '3 8 2', 3, 3,
        ARRAY['run_id', 'outcome', 'capability_id'],
        ARRAY['uuid_ops', 'text_ops', 'uuid_ops'],
        ARRAY['pg_catalog', 'pg_catalog', 'pg_catalog'],
        ARRAY[NULL::text, NULL::text, NULL::text],
        ARRAY[NULL::text, 'default', NULL::text],
        ARRAY[NULL::text, 'pg_catalog', NULL::text], NULL::text, NULL::text
      )
  ), actual AS (
    SELECT
      expected.*,
      index_metadata.*,
      access_method.amname,
      pg_get_expr(index_metadata.indexprs, index_metadata.indrelid, true)
        AS rendered_expressions,
      pg_get_expr(index_metadata.indpred, index_metadata.indrelid, true)
        AS rendered_predicate,
      key_rendering.definitions,
      opclasses.names AS actual_opclass_names,
      opclasses.namespaces AS actual_opclass_namespaces,
      opclasses.extensions AS actual_opclass_extensions,
      collations.names AS actual_collation_names,
      collations.namespaces AS actual_collation_namespaces,
      index_relation.relkind,
      namespace.nspname
    FROM expected
    JOIN pg_index AS index_metadata
      ON index_metadata.indexrelid = expected.index_oid
    JOIN pg_class AS index_relation
      ON index_relation.oid = index_metadata.indexrelid
    JOIN pg_namespace AS namespace
      ON namespace.oid = index_relation.relnamespace
    JOIN pg_am AS access_method ON access_method.oid = index_relation.relam
    CROSS JOIN LATERAL (
      SELECT array_agg(
        pg_get_indexdef(index_metadata.indexrelid, key_number, true)
        ORDER BY key_number
      ) AS definitions
      FROM generate_series(1, index_metadata.indnatts) AS key(key_number)
    ) AS key_rendering
    CROSS JOIN LATERAL (
      SELECT
        array_agg(opclass.opcname ORDER BY entry.ordinality) AS names,
        array_agg(
          CASE WHEN extension.oid IS NULL THEN opclass_namespace.nspname END
          ORDER BY entry.ordinality
        ) AS namespaces,
        array_agg(extension.extname ORDER BY entry.ordinality) AS extensions
      FROM unnest(index_metadata.indclass::oid[]) WITH ORDINALITY
        AS entry(opclass_oid, ordinality)
      JOIN pg_opclass AS opclass ON opclass.oid = entry.opclass_oid
      JOIN pg_namespace AS opclass_namespace
        ON opclass_namespace.oid = opclass.opcnamespace
      LEFT JOIN pg_depend AS dependency
        ON dependency.classid = 'pg_opclass'::regclass
       AND dependency.objid = opclass.oid
       AND dependency.deptype = 'e'
      LEFT JOIN pg_extension AS extension
        ON extension.oid = dependency.refobjid
    ) AS opclasses
    CROSS JOIN LATERAL (
      SELECT
        array_agg(index_collation.collname ORDER BY entry.ordinality) AS names,
        array_agg(collation_namespace.nspname ORDER BY entry.ordinality) AS namespaces
      FROM unnest(index_metadata.indcollation::oid[]) WITH ORDINALITY
        AS entry(collation_oid, ordinality)
      LEFT JOIN pg_collation AS index_collation
        ON index_collation.oid = entry.collation_oid
      LEFT JOIN pg_namespace AS collation_namespace
        ON collation_namespace.oid = index_collation.collnamespace
    ) AS collations
  )
  SELECT count(*)::integer
  INTO required_index_count
  FROM actual
  WHERE actual.nspname = schema_name
    AND actual.relkind = 'i'
    AND actual.indrelid = actual.table_oid
    AND actual.indisvalid
    AND actual.indisready
    AND actual.indislive
    AND NOT actual.indisunique
    AND actual.amname = actual.access_method
    AND actual.indkey::text = actual.indkey_text
    AND actual.indnkeyatts = actual.key_count
    AND actual.indnatts = actual.attribute_count
    AND actual.definitions = actual.key_definitions
    AND actual.actual_opclass_names::text[] = actual.opclass_names
    AND actual.actual_opclass_namespaces::text[]
        IS NOT DISTINCT FROM actual.opclass_namespaces
    AND actual.actual_opclass_extensions::text[]
        IS NOT DISTINCT FROM actual.opclass_extensions
    AND actual.actual_collation_names::text[]
        IS NOT DISTINCT FROM actual.collation_names
    AND actual.actual_collation_namespaces::text[]
        IS NOT DISTINCT FROM actual.collation_namespaces
    AND actual.rendered_expressions IS NOT DISTINCT FROM actual.index_expressions
    AND actual.rendered_predicate IS NOT DISTINCT FROM actual.predicate;
  exact_index_contract_ok := required_index_count = 7;

  WITH expected(trigger_name, table_oid, function_oid, trigger_type) AS (
    VALUES
      ('repository_exact_tree_literal_index_blob_immutable',
       to_regclass('repository_exact_tree_literal_index_blobs'),
       to_regprocedure('guard_repository_exact_tree_literal_index_blob_mutation()'), 27),
      ('repository_exact_tree_literal_index_blob_no_truncate',
       to_regclass('repository_exact_tree_literal_index_blobs'),
       to_regprocedure('guard_repository_exact_tree_literal_index_blob_mutation()'), 34),
      ('repository_exact_tree_literal_index_manifest_insert_guard',
       to_regclass('repository_exact_tree_literal_index_manifests'),
       to_regprocedure('guard_repository_exact_tree_literal_index_manifest_insert()'), 7),
      ('repository_exact_tree_literal_index_manifest_publish_guard',
       to_regclass('repository_exact_tree_literal_index_manifests'),
       to_regprocedure('publish_repository_exact_tree_literal_index_manifest()'), 19),
      ('repository_exact_tree_literal_index_manifest_no_delete',
       to_regclass('repository_exact_tree_literal_index_manifests'),
       to_regprocedure('guard_repository_exact_tree_literal_index_manifest_delete()'), 11),
      ('repository_exact_tree_literal_index_manifest_no_truncate',
       to_regclass('repository_exact_tree_literal_index_manifests'),
       to_regprocedure('guard_repository_exact_tree_literal_index_manifest_delete()'), 34),
      ('repository_exact_tree_literal_index_member_insert_guard',
       to_regclass('repository_exact_tree_literal_index_members'),
       to_regprocedure('guard_repository_exact_tree_literal_index_member_insert()'), 7),
      ('repository_exact_tree_literal_index_member_immutable',
       to_regclass('repository_exact_tree_literal_index_members'),
       to_regprocedure('guard_repository_exact_tree_literal_index_member_mutation()'), 27),
      ('repository_exact_tree_literal_index_member_no_truncate',
       to_regclass('repository_exact_tree_literal_index_members'),
       to_regprocedure('guard_repository_exact_tree_literal_index_member_mutation()'), 34),
      ('repository_exact_tree_literal_index_gc_run_append_only',
       to_regclass('repository_exact_tree_literal_index_gc_runs'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 27),
      ('repository_exact_tree_literal_index_gc_run_no_truncate',
       to_regclass('repository_exact_tree_literal_index_gc_runs'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 34),
      ('repository_exact_tree_literal_index_gc_capability_append_only',
       to_regclass('repository_exact_tree_literal_index_gc_capabilities'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 27),
      ('repository_exact_tree_literal_index_gc_capability_no_truncate',
       to_regclass('repository_exact_tree_literal_index_gc_capabilities'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 34),
      ('repository_exact_tree_literal_index_gc_receipt_append_only',
       to_regclass('repository_exact_tree_literal_index_gc_receipts'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 27),
      ('repository_exact_tree_literal_index_gc_receipt_no_truncate',
       to_regclass('repository_exact_tree_literal_index_gc_receipts'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 34),
      ('repository_exact_tree_literal_index_gc_tombstone_append_only',
       to_regclass('repository_exact_tree_literal_index_gc_tombstones'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 27),
      ('repository_exact_tree_literal_index_gc_tombstone_no_truncate',
       to_regclass('repository_exact_tree_literal_index_gc_tombstones'),
       to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'), 34),
      ('candidate_workspace_exact_tree_literal_index_reference_lock',
       to_regclass('candidate_workspaces'),
       to_regprocedure('lock_candidate_exact_tree_literal_index_reference()'), 23)
  ), matched AS (
    SELECT trigger.oid
    FROM expected
    JOIN pg_trigger AS trigger
      ON trigger.tgname = expected.trigger_name
     AND trigger.tgrelid = expected.table_oid
     AND trigger.tgfoid = expected.function_oid
     AND trigger.tgtype = expected.trigger_type
     AND trigger.tgenabled = 'O'
     AND NOT trigger.tgisinternal
     AND trigger.tgqual IS NULL
     AND trigger.tgattr::text = ''
     AND trigger.tgnargs = 0
     AND trigger.tgconstraint = 0
     AND NOT trigger.tgdeferrable
     AND NOT trigger.tginitdeferred
     AND trigger.tgoldtable IS NULL
     AND trigger.tgnewtable IS NULL
  ), boundary_tables(table_oid) AS (
    SELECT to_regclass(table_name)
    FROM unnest(ARRAY[
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
    ]) AS table_entry(table_name)
  )
  SELECT
    (SELECT count(*)::integer FROM matched),
    count(*)::integer
  INTO expected_trigger_count, actual_boundary_trigger_count
  FROM pg_trigger AS trigger
  WHERE NOT trigger.tgisinternal
    AND (
      trigger.tgrelid IN (SELECT table_oid FROM boundary_tables)
      OR (
        trigger.tgrelid = to_regclass('candidate_workspaces')
        AND (
          trigger.tgname LIKE 'candidate_workspace_exact_tree_literal_index_%'
          OR trigger.tgfoid = to_regprocedure(
            'lock_candidate_exact_tree_literal_index_reference()'
          )
        )
      )
    );
  trigger_contract_ok := expected_trigger_count = 18
    AND actual_boundary_trigger_count = 18;

  SELECT array_agg(DISTINCT boundary.owner_oid ORDER BY boundary.owner_oid)
  INTO boundary_owners
  FROM (
    SELECT relation.relowner AS owner_oid
    FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = schema_name
      AND (
        (relation.relkind = 'r' AND relation.relname IN (
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
        ))
        OR (
          relation.relkind = 'i'
          AND EXISTS (
            SELECT 1
            FROM pg_index AS index_metadata
            JOIN pg_class AS indexed_table
              ON indexed_table.oid = index_metadata.indrelid
            WHERE index_metadata.indexrelid = relation.oid
              AND indexed_table.relnamespace = relation.relnamespace
              AND indexed_table.relname IN (
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
              )
          )
        )
      )
    UNION ALL
    SELECT procedure.proowner
    FROM pg_proc AS procedure
    WHERE procedure.oid = ANY(boundary_function_oids)
    UNION ALL
    SELECT namespace.nspowner
    FROM pg_namespace AS namespace
    WHERE namespace.nspname = schema_name
  ) AS boundary;

  IF operator_oid IS NOT NULL
     AND application_oid IS NOT NULL
     AND migration_owner_oid IS NOT NULL THEN
    SELECT count(*) = 3 AND bool_and(
      NOT role.rolcanlogin
      AND NOT role.rolsuper
      AND NOT role.rolbypassrls
      AND NOT role.rolcreaterole
      AND NOT role.rolcreatedb
      AND NOT role.rolreplication
    )
    AND NOT EXISTS (
      SELECT 1
      FROM pg_auth_members AS membership
      WHERE membership.member IN (operator_oid, application_oid, migration_owner_oid)
    )
    AND NOT EXISTS (
      SELECT 1
      FROM pg_auth_members AS membership
      WHERE membership.roleid IN (operator_oid, application_oid, migration_owner_oid)
        AND membership.admin_option
    )
    INTO role_boundary_ok
    FROM pg_roles AS role
    WHERE role.oid IN (operator_oid, application_oid, migration_owner_oid);
  END IF;

  IF migration_owner_oid IS NOT NULL
     AND cardinality(boundary_function_oids) = 23
     AND boundary_table_count = 10
     AND boundary_index_count = 22
     AND cardinality(boundary_owners) > 0 THEN
    SELECT bool_and(owner_oid = migration_owner_oid)
    INTO owner_boundary_ok
    FROM unnest(boundary_owners) AS owner(owner_oid);
  END IF;

  IF migration_owner_oid IS NOT NULL
     AND cardinality(internal_function_oids) = 8 THEN
    SELECT count(*) = 8 AND bool_and(
      procedure.proowner = migration_owner_oid
      AND EXISTS (
        SELECT 1
        FROM aclexplode(
          COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
        ) AS privilege
        WHERE privilege.grantee = procedure.proowner
          AND privilege.privilege_type = 'EXECUTE'
      )
      AND NOT EXISTS (
        SELECT 1
        FROM aclexplode(
          COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
        ) AS privilege
        WHERE privilege.privilege_type = 'EXECUTE'
          AND (
            privilege.grantee <> procedure.proowner
          )
      )
    )
    INTO internal_function_acl_exact
    FROM pg_proc AS procedure
    WHERE procedure.oid = ANY(internal_function_oids);
  END IF;

  IF migration_owner_oid IS NOT NULL
     AND application_oid IS NOT NULL
     AND sandbox_checkpoint_helper_oid IS NOT NULL THEN
    SELECT
      procedure.proowner = migration_owner_oid
      AND NOT procedure.prosecdef
      AND procedure.prokind = 'f'
      AND procedure.prorettype = 'boolean'::regtype
      AND NOT procedure.proretset
      AND procedure.provolatile = 's'
      AND language.lanname = 'sql'
      AND procedure.proconfig = ARRAY[
        format('search_path=pg_catalog, %I, pg_temp', schema_name)
      ]::text[]
      AND EXISTS (
        SELECT 1
        FROM aclexplode(
          COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
        ) AS privilege
        WHERE privilege.grantee = application_oid
          AND privilege.privilege_type = 'EXECUTE'
          AND NOT privilege.is_grantable
      )
      AND NOT EXISTS (
        SELECT 1
        FROM aclexplode(
          COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
        ) AS privilege
        WHERE privilege.privilege_type = 'EXECUTE'
          AND (
            privilege.grantee NOT IN (procedure.proowner, application_oid)
            OR (
              privilege.grantee = application_oid
              AND privilege.is_grantable
            )
          )
      )
    INTO sandbox_checkpoint_dependency_ok
    FROM pg_proc AS procedure
    JOIN pg_language AS language ON language.oid = procedure.prolang
    WHERE procedure.oid = sandbox_checkpoint_helper_oid;
  END IF;

  IF application_oid IS NOT NULL THEN
    application_table_boundary_ok :=
      has_table_privilege(application_oid, 'repository_exact_tree_literal_index_blobs'::regclass, 'SELECT')
      AND has_table_privilege(application_oid, 'repository_exact_tree_literal_index_blobs'::regclass, 'INSERT')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_blobs'::regclass, 'UPDATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_blobs'::regclass, 'DELETE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_blobs'::regclass, 'TRUNCATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_blobs'::regclass, 'REFERENCES')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_blobs'::regclass, 'TRIGGER')
      AND has_table_privilege(application_oid, 'repository_exact_tree_literal_index_members'::regclass, 'SELECT')
      AND has_table_privilege(application_oid, 'repository_exact_tree_literal_index_members'::regclass, 'INSERT')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_members'::regclass, 'UPDATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_members'::regclass, 'DELETE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_members'::regclass, 'TRUNCATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_members'::regclass, 'REFERENCES')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_members'::regclass, 'TRIGGER')
      AND has_table_privilege(application_oid, 'repository_exact_tree_literal_index_manifests'::regclass, 'SELECT')
      AND has_table_privilege(application_oid, 'repository_exact_tree_literal_index_manifests'::regclass, 'INSERT')
      AND has_table_privilege(application_oid, 'repository_exact_tree_literal_index_manifests'::regclass, 'UPDATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_manifests'::regclass, 'DELETE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_manifests'::regclass, 'TRUNCATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_manifests'::regclass, 'REFERENCES')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_manifests'::regclass, 'TRIGGER')
      AND has_table_privilege(application_oid, 'repository_exact_tree_literal_index_build_claims'::regclass, 'SELECT')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_build_claims'::regclass, 'INSERT')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_build_claims'::regclass, 'UPDATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_build_claims'::regclass, 'DELETE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_build_claims'::regclass, 'TRUNCATE')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_build_claims'::regclass, 'REFERENCES')
      AND NOT has_table_privilege(application_oid, 'repository_exact_tree_literal_index_build_claims'::regclass, 'TRIGGER');
  END IF;

  IF application_oid IS NOT NULL AND operator_oid IS NOT NULL THEN
    SELECT bool_and(
      NOT has_table_privilege(application_oid, relation.oid, 'SELECT')
      AND NOT has_table_privilege(application_oid, relation.oid, 'INSERT')
      AND NOT has_table_privilege(application_oid, relation.oid, 'UPDATE')
      AND NOT has_table_privilege(application_oid, relation.oid, 'DELETE')
      AND NOT has_table_privilege(application_oid, relation.oid, 'TRUNCATE')
      AND NOT has_table_privilege(application_oid, relation.oid, 'REFERENCES')
      AND NOT has_table_privilege(application_oid, relation.oid, 'TRIGGER')
      AND NOT has_table_privilege(operator_oid, relation.oid, 'SELECT')
      AND NOT has_table_privilege(operator_oid, relation.oid, 'INSERT')
      AND NOT has_table_privilege(operator_oid, relation.oid, 'UPDATE')
      AND NOT has_table_privilege(operator_oid, relation.oid, 'DELETE')
      AND NOT has_table_privilege(operator_oid, relation.oid, 'TRUNCATE')
      AND NOT has_table_privilege(operator_oid, relation.oid, 'REFERENCES')
      AND NOT has_table_privilege(operator_oid, relation.oid, 'TRIGGER')
    )
    INTO gc_private_table_boundary_ok
    FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = schema_name
      AND relation.relkind = 'r'
      AND relation.relname IN (
        'repository_exact_tree_literal_index_gc_runs',
        'repository_exact_tree_literal_index_gc_capabilities',
        'repository_exact_tree_literal_index_gc_receipts',
        'repository_exact_tree_literal_index_gc_tombstones',
        'repository_exact_tree_literal_index_gc_tree_delete_auth',
        'repository_exact_tree_literal_index_gc_blob_delete_auth'
      );
  END IF;

  IF operator_oid IS NOT NULL
     AND application_oid IS NOT NULL
     AND cardinality(gc_function_oids) = 4 THEN
    SELECT
      bool_and(
        has_function_privilege(operator_oid, function_oid, 'EXECUTE')
        AND NOT has_function_privilege(application_oid, function_oid, 'EXECUTE')
      )
      AND NOT EXISTS (
        SELECT 1
        FROM unnest(gc_function_oids) AS function_entry(function_oid)
        JOIN pg_proc AS procedure ON procedure.oid = function_entry.function_oid
        CROSS JOIN LATERAL aclexplode(
          COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
        ) AS privilege
        WHERE privilege.privilege_type = 'EXECUTE'
          AND (
            privilege.grantee NOT IN (operator_oid, procedure.proowner)
            OR (privilege.grantee = operator_oid AND privilege.is_grantable)
          )
      )
    INTO gc_acl_only_operator
    FROM unnest(gc_function_oids) AS function_entry(function_oid);
  END IF;

  SELECT NOT EXISTS (
    SELECT 1
    FROM pg_proc AS procedure
    JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
    CROSS JOIN LATERAL aclexplode(
      COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
    ) AS privilege
    WHERE namespace.nspname = schema_name
      AND privilege.privilege_type = 'EXECUTE'
      AND privilege.grantee = 0
  )
  INTO public_routine_execute_revoked;

  SELECT NOT EXISTS (
    SELECT 1
    FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    CROSS JOIN LATERAL aclexplode(
      COALESCE(
        relation.relacl,
        acldefault(
          CASE WHEN relation.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END,
          relation.relowner
        )
      )
    ) AS privilege
    WHERE namespace.nspname = schema_name
      AND relation.relkind IN ('r', 'p', 'S', 'v', 'm', 'f')
      AND privilege.grantee = 0
  )
  INTO public_relation_acl_revoked;

  SELECT NOT EXISTS (
    SELECT 1
    FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    JOIN pg_attribute AS attribute ON attribute.attrelid = relation.oid
    CROSS JOIN LATERAL aclexplode(attribute.attacl) AS privilege
    WHERE namespace.nspname = schema_name
      AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
      AND attribute.attnum > 0
      AND NOT attribute.attisdropped
  )
  INTO column_acl_boundary_ok;

  IF operator_oid IS NOT NULL AND cardinality(gc_function_oids) = 4 THEN
    SELECT
      count(*) FILTER (
        WHERE has_function_privilege(operator_oid, procedure.oid, 'EXECUTE')
      ) = 4
      AND bool_and(
        has_function_privilege(operator_oid, procedure.oid, 'EXECUTE')
        = (procedure.oid = ANY(gc_function_oids))
      )
    INTO operator_routine_boundary_ok
    FROM pg_proc AS procedure
    JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
    WHERE namespace.nspname = schema_name;
  END IF;

  IF application_oid IS NOT NULL
     AND cardinality(claim_function_oids) = 3
     AND cardinality(candidate_function_oids) = 7 THEN
    SELECT
      count(*) FILTER (
        WHERE procedure.prosecdef
          AND has_function_privilege(application_oid, procedure.oid, 'EXECUTE')
      ) = 10
      AND bool_and(
        NOT procedure.prosecdef
        OR has_function_privilege(application_oid, procedure.oid, 'EXECUTE')
           = (
             procedure.oid = ANY(
               array_cat(claim_function_oids, candidate_function_oids)
             )
           )
      )
    INTO application_definer_boundary_ok
    FROM pg_proc AS procedure
    JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
    WHERE namespace.nspname = schema_name;
    application_definer_boundary_ok := application_definer_boundary_ok
      AND NOT EXISTS (
        SELECT 1
        FROM unnest(ARRAY[
          to_regprocedure('guard_repository_exact_tree_literal_index_gc_audit_mutation()'),
          to_regprocedure('lock_candidate_exact_tree_literal_index_reference()'),
          to_regprocedure('guard_repository_exact_tree_literal_index_blob_mutation()'),
          to_regprocedure('guard_repository_exact_tree_literal_index_member_mutation()'),
          to_regprocedure('guard_repository_exact_tree_literal_index_manifest_delete()'),
          to_regprocedure('guard_repository_exact_tree_literal_index_manifest_insert()'),
          to_regprocedure('guard_repository_exact_tree_literal_index_member_insert()'),
          to_regprocedure('publish_repository_exact_tree_literal_index_manifest()')
        ]::oid[]) AS internal(function_oid)
        WHERE has_function_privilege(application_oid, internal.function_oid, 'EXECUTE')
      );
  END IF;

  IF migration_owner_oid IS NOT NULL THEN
    SELECT EXISTS (
      SELECT 1
      FROM pg_default_acl AS defaults
      WHERE defaults.defaclrole = migration_owner_oid
        -- Built-in PUBLIC function EXECUTE is a global default.  PostgreSQL
        -- per-schema defaults can add privileges but cannot subtract that
        -- global default, so the hardened row must intentionally be global.
        AND defaults.defaclnamespace = 0
        AND defaults.defaclobjtype = 'f'
        AND NOT EXISTS (
          SELECT 1
          FROM aclexplode(
            COALESCE(defaults.defaclacl, acldefault('f', defaults.defaclrole))
          ) AS privilege
          WHERE privilege.grantee = 0
            AND privilege.privilege_type = 'EXECUTE'
        )
    )
    INTO future_public_routine_default_revoked;
  END IF;

  trusted_schema := schema_name;
  operator_role_exists := operator_oid IS NOT NULL;
  application_role_exists := application_oid IS NOT NULL;
  migration_owner_role_exists := migration_owner_oid IS NOT NULL;
  stable_group_roles_safe := COALESCE(role_boundary_ok, false);
  objects_owned_by_migration_owner := COALESCE(owner_boundary_ok, false);
  operator_execute_granted := COALESCE(gc_acl_only_operator, false)
    AND COALESCE(operator_routine_boundary_ok, false);

  application_claim_execute_granted := application_oid IS NOT NULL
    AND operator_oid IS NOT NULL
    AND cardinality(claim_function_oids) = 3
    AND (SELECT bool_and(
           has_function_privilege(application_oid, function_oid, 'EXECUTE')
           AND NOT has_function_privilege(operator_oid, function_oid, 'EXECUTE')
         )
         FROM unnest(claim_function_oids) AS function_entry(function_oid));

  application_candidate_execute_ok := application_oid IS NOT NULL
    AND operator_oid IS NOT NULL
    AND cardinality(candidate_function_oids) = 7
    AND (SELECT bool_and(
           has_function_privilege(application_oid, function_oid, 'EXECUTE')
           AND NOT has_function_privilege(operator_oid, function_oid, 'EXECUTE')
         )
         FROM unnest(candidate_function_oids) AS function_entry(function_oid));

  application_function_acl_exact := application_oid IS NOT NULL
    AND cardinality(claim_function_oids) = 3
    AND cardinality(candidate_function_oids) = 7
    AND NOT EXISTS (
      SELECT 1
      FROM unnest(array_cat(claim_function_oids, candidate_function_oids))
        AS function_entry(function_oid)
      JOIN pg_proc AS procedure ON procedure.oid = function_entry.function_oid
      CROSS JOIN LATERAL aclexplode(
        COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
      ) AS privilege
      WHERE privilege.privilege_type = 'EXECUTE'
        AND (
          privilege.grantee NOT IN (application_oid, procedure.proowner)
          OR (privilege.grantee = application_oid AND privilege.is_grantable)
        )
    );

  application_schema_head_read_granted := application_oid IS NOT NULL
    AND schema_head_oid IS NOT NULL
    AND has_table_privilege(application_oid, schema_head_oid, 'SELECT')
    AND NOT has_table_privilege(application_oid, schema_head_oid, 'INSERT')
    AND NOT has_table_privilege(application_oid, schema_head_oid, 'UPDATE')
    AND NOT has_table_privilege(application_oid, schema_head_oid, 'DELETE')
    AND NOT has_table_privilege(application_oid, schema_head_oid, 'TRUNCATE');

  public_claim_execute_revoked := cardinality(claim_function_oids) = 3
    AND NOT EXISTS (
      SELECT 1
      FROM unnest(claim_function_oids) AS function_entry(function_oid)
      JOIN pg_proc AS procedure ON procedure.oid = function_entry.function_oid
      CROSS JOIN LATERAL aclexplode(
        COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
      ) AS privilege
      WHERE privilege.privilege_type = 'EXECUTE'
        AND privilege.grantee = 0
    );

  public_candidate_execute_revoked := cardinality(candidate_function_oids) = 7
    AND NOT EXISTS (
      SELECT 1
      FROM unnest(candidate_function_oids) AS function_entry(function_oid)
      JOIN pg_proc AS procedure ON procedure.oid = function_entry.function_oid
      CROSS JOIN LATERAL aclexplode(
        COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
      ) AS privilege
      WHERE privilege.privilege_type = 'EXECUTE'
        AND privilege.grantee = 0
    );

  public_schema_create_revoked := NOT EXISTS (
    SELECT 1
    FROM pg_namespace AS namespace
    CROSS JOIN LATERAL aclexplode(
      COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))
    ) AS privilege
    WHERE namespace.nspname = schema_name
      AND privilege.privilege_type = 'CREATE'
      AND privilege.grantee = 0
  );

  ready := operator_role_exists
    AND application_role_exists
    AND migration_owner_role_exists
    AND stable_group_roles_safe
    AND objects_owned_by_migration_owner
    AND exact_index_contract_ok
    AND trigger_contract_ok
    AND operator_execute_granted
    AND application_claim_execute_granted
    AND application_candidate_execute_ok
    AND application_function_acl_exact
    AND application_definer_boundary_ok
    AND internal_function_acl_exact
    AND sandbox_checkpoint_dependency_ok
    AND application_table_boundary_ok
    AND gc_private_table_boundary_ok
    AND application_schema_head_read_granted
    AND public_claim_execute_revoked
    AND public_candidate_execute_revoked
    AND public_routine_execute_revoked
    AND public_relation_acl_revoked
    AND column_acl_boundary_ok
    AND future_public_routine_default_revoked
    AND public_schema_create_revoked;
  reason := CASE
    WHEN ready THEN 'ready'
    WHEN NOT migration_owner_role_exists THEN 'worksflow_migration_owner is absent'
    WHEN NOT stable_group_roles_safe THEN
      'stable application, migration-owner, and GC-operator groups must be NOLOGIN without elevated role attributes'
    WHEN NOT objects_owned_by_migration_owner THEN
      'trusted schema, ten exact-index/GC tables and their 22 indexes, and all 23 boundary routines must be owned exactly by worksflow_migration_owner'
    WHEN NOT exact_index_contract_ok THEN
      'planner-critical exact-tree indexes do not match their exact pg_index/pg_get_indexdef contract'
    WHEN NOT trigger_contract_ok THEN
      'exact-tree immutable, delete-guard, audit, or Candidate lock trigger wiring has drifted'
    WHEN NOT operator_role_exists THEN 'worksflow_repository_index_gc_operator is absent'
    WHEN NOT application_role_exists THEN 'worksflow_application is absent'
    WHEN NOT operator_execute_granted THEN 'GC execute privileges are not operator-only'
    WHEN NOT application_claim_execute_granted THEN
      'worksflow_application lacks the exact build-claim function privileges'
    WHEN NOT application_candidate_execute_ok THEN
      'Candidate/Sandbox mutation functions are not application-only'
    WHEN NOT application_function_acl_exact THEN
      'application control functions have an unexpected EXECUTE grantee'
    WHEN NOT application_definer_boundary_ok THEN
      'worksflow_application must execute exactly ten trusted-schema SECURITY DEFINER routines'
    WHEN NOT internal_function_acl_exact THEN
      'exact-index internal routines must have owner-only EXECUTE privileges'
    WHEN NOT sandbox_checkpoint_dependency_ok THEN
      'sandbox checkpoint dependency must be stable SECURITY INVOKER owned by migration owner and executable only by application without grant option'
    WHEN NOT application_table_boundary_ok THEN
      'worksflow_application exact-index table privileges are not minimal and exact'
    WHEN NOT gc_private_table_boundary_ok THEN
      'application or operator has forbidden direct GC audit/authorization table access'
    WHEN NOT application_schema_head_read_granted THEN
      'worksflow_application must have read-only schema_migrations access'
    WHEN NOT public_claim_execute_revoked THEN 'PUBLIC can execute build-claim functions'
    WHEN NOT public_candidate_execute_revoked THEN
      'PUBLIC can execute Candidate/Sandbox mutation functions'
    WHEN NOT public_routine_execute_revoked THEN
      'PUBLIC can execute a routine in the trusted schema'
    WHEN NOT public_relation_acl_revoked THEN
      'PUBLIC has a relation or sequence privilege in the trusted schema'
    WHEN NOT column_acl_boundary_ok THEN
      'trusted-schema relations have forbidden explicit column privileges'
    WHEN NOT future_public_routine_default_revoked THEN
      'future migration-owner routines would grant EXECUTE to PUBLIC'
    WHEN NOT public_schema_create_revoked THEN 'PUBLIC can create in the trusted schema'
    ELSE 'not ready'
  END;
  RETURN NEXT;
END;
$$;

-- Harden every SECURITY DEFINER routine to pg_catalog, the dynamically
-- captured trusted schema, then pg_temp.  Object creation used the trusted
-- schema first; function execution must not.
DO $function_paths$
DECLARE
  schema_name text := current_schema();
  function_identity text;
  migration_owner_exists boolean := EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner'
  );
BEGIN
  FOREACH function_identity IN ARRAY ARRAY[
    'guard_repository_exact_tree_literal_index_gc_audit_mutation()',
    'guard_repository_exact_tree_literal_index_blob_mutation()',
    'guard_repository_exact_tree_literal_index_member_mutation()',
    'guard_repository_exact_tree_literal_index_manifest_delete()',
    'guard_repository_exact_tree_literal_index_manifest_insert()',
    'guard_repository_exact_tree_literal_index_member_insert()',
    'publish_repository_exact_tree_literal_index_manifest()',
    'lock_candidate_exact_tree_literal_index_reference()',
    'plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer)',
    'execute_repository_exact_tree_literal_index_gc(uuid)',
    'inspect_repository_exact_tree_literal_index_gc_run(uuid)',
    'repository_exact_tree_literal_index_gc_readiness()',
    'acquire_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer,integer,bigint,integer)',
    'renew_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint,integer)',
    'release_repository_exact_tree_literal_index_build_claim(uuid,text,uuid,bigint)',
    'acquire_candidate_workspace_lease(uuid,bigint,uuid,integer)',
    'rotate_candidate_workspace_session(uuid,bigint,bigint,uuid)',
    'update_candidate_workspace_flags(uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text)',
    'freeze_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)',
    'abandon_candidate_workspace(uuid,bigint,bigint,bigint,uuid,uuid,text)',
    'abandon_sandbox_session_candidate(uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid)',
    'complete_abandoned_sandbox_session(uuid,bigint,bigint,uuid)',
    'sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)'
  ] LOOP
    EXECUTE format(
      'ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',
      schema_name, function_identity, schema_name
    );
    IF migration_owner_exists THEN
      EXECUTE format(
        'ALTER FUNCTION %I.%s OWNER TO worksflow_migration_owner',
        schema_name, function_identity
      );
    END IF;
  END LOOP;
END;
$function_paths$;

-- Production ownership is one exact, NOLOGIN role rather than any member of
-- that role.  This removes elevated LOGIN principals from proowner/relowner
-- while retaining the role as the controlled migration authority.  Isolated
-- development schemas may omit the stable role; readiness remains false in
-- that posture and the creating principal remains owner.
DO $ownership_boundary$
DECLARE
  schema_name text := current_schema();
  table_name text;
  sequence_name text;
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner'
  ) THEN
    -- The transferred Candidate/Sandbox SECURITY DEFINER routines span older
    -- control-plane tables.  Make the exact NOLOGIN role their real relation
    -- owner instead of adding direct ACLs to a role reachable from the
    -- migration session.  Index ownership follows its table atomically.
    FOR table_name IN
      SELECT relation.relname
      FROM pg_class AS relation
      JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
      WHERE namespace.nspname = schema_name
        AND relation.relkind IN ('r', 'p')
      ORDER BY relation.relkind DESC, relation.relname
    LOOP
      EXECUTE format(
        'ALTER TABLE %I.%I OWNER TO worksflow_migration_owner',
        schema_name, table_name
      );
    END LOOP;

    FOR sequence_name IN
      SELECT relation.relname
      FROM pg_class AS relation
      JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
      WHERE namespace.nspname = schema_name
        AND relation.relkind = 'S'
      ORDER BY relation.relname
    LOOP
      EXECUTE format(
        'ALTER SEQUENCE %I.%I OWNER TO worksflow_migration_owner',
        schema_name, sequence_name
      );
    END LOOP;

    EXECUTE format(
      'ALTER SCHEMA %I OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;
END;
$ownership_boundary$;

REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_runs FROM PUBLIC;
REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_capabilities FROM PUBLIC;
REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_receipts FROM PUBLIC;
REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_tombstones FROM PUBLIC;
REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_tree_delete_auth FROM PUBLIC;
REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_blob_delete_auth FROM PUBLIC;

REVOKE ALL ON FUNCTION plan_repository_exact_tree_literal_index_gc(
  uuid, bigint, integer, integer, integer
) FROM PUBLIC;
REVOKE ALL ON FUNCTION execute_repository_exact_tree_literal_index_gc(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION inspect_repository_exact_tree_literal_index_gc_run(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION repository_exact_tree_literal_index_gc_readiness() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION lock_candidate_exact_tree_literal_index_reference() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_blob_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_member_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_manifest_delete() FROM PUBLIC;

REVOKE ALL ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer, integer, bigint, integer
) FROM PUBLIC;
REVOKE ALL ON FUNCTION renew_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer
) FROM PUBLIC;
REVOKE ALL ON FUNCTION release_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint
) FROM PUBLIC;
REVOKE ALL ON FUNCTION acquire_candidate_workspace_lease(
  uuid, bigint, uuid, integer
) FROM PUBLIC;
REVOKE ALL ON FUNCTION rotate_candidate_workspace_session(
  uuid, bigint, bigint, uuid
) FROM PUBLIC;
REVOKE ALL ON FUNCTION update_candidate_workspace_flags(
  uuid, bigint, bigint, bigint, uuid, boolean, boolean, boolean, text, text, text
) FROM PUBLIC;
REVOKE ALL ON FUNCTION freeze_candidate_workspace(
  uuid, bigint, bigint, bigint, uuid, uuid, text
) FROM PUBLIC;
REVOKE ALL ON FUNCTION abandon_candidate_workspace(
  uuid, bigint, bigint, bigint, uuid, uuid, text
) FROM PUBLIC;
REVOKE ALL ON FUNCTION abandon_sandbox_session_candidate(
  uuid, uuid, bigint, bigint, bigint, bigint, uuid, uuid, text, uuid
) FROM PUBLIC;
REVOKE ALL ON FUNCTION complete_abandoned_sandbox_session(
  uuid, bigint, bigint, uuid
) FROM PUBLIC;

DO $privileges$
DECLARE
  schema_name text := current_schema();
  routine_grant record;
  grantee_sql text;
  schema_head_exists boolean := false;
BEGIN
  SELECT EXISTS (
    SELECT 1
    FROM pg_class AS relation
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = schema_name
      AND relation.relname = 'schema_migrations'
      AND relation.relkind = 'r'
  )
  INTO schema_head_exists;

  -- Harden every existing routine, not only the functions introduced here,
  -- and make the same boundary the default for future routines created by
  -- either the migration principal or the stable migration-owner role.
  EXECUTE format(
    'REVOKE EXECUTE ON ALL ROUTINES IN SCHEMA %I FROM PUBLIC',
    schema_name
  );
  EXECUTE format(
    'REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA %I FROM PUBLIC',
    schema_name
  );
  EXECUTE format(
    'REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA %I FROM PUBLIC',
    schema_name
  );
  EXECUTE format(
    'ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC'
  );
  IF EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner'
  ) THEN
    EXECUTE format(
      'ALTER DEFAULT PRIVILEGES FOR ROLE worksflow_migration_owner '
      'REVOKE EXECUTE ON ROUTINES FROM PUBLIC'
    );
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application') THEN
    -- Preserve all predecessor application functionality, then remove every
    -- 000066 non-application SECURITY DEFINER surface.  The exact ten
    -- Candidate/Sandbox/build-claim definers are re-granted below.
    EXECUTE format(
      'GRANT EXECUTE ON ALL ROUTINES IN SCHEMA %I TO worksflow_application',
      schema_name
    );
    REVOKE ALL ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint,integer,integer,bigint,integer
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION renew_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint,integer
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION release_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION acquire_candidate_workspace_lease(
      uuid,bigint,uuid,integer
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION rotate_candidate_workspace_session(
      uuid,bigint,bigint,uuid
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION update_candidate_workspace_flags(
      uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION freeze_candidate_workspace(
      uuid,bigint,bigint,bigint,uuid,uuid,text
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION abandon_candidate_workspace(
      uuid,bigint,bigint,bigint,uuid,uuid,text
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION abandon_sandbox_session_candidate(
      uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid
    ) FROM worksflow_application;
    REVOKE ALL ON FUNCTION complete_abandoned_sandbox_session(
      uuid,bigint,bigint,uuid
    ) FROM worksflow_application;
    REVOKE ALL ON repository_exact_tree_literal_index_blobs FROM worksflow_application;
    REVOKE ALL ON repository_exact_tree_literal_index_members FROM worksflow_application;
    REVOKE ALL ON repository_exact_tree_literal_index_manifests FROM worksflow_application;
    REVOKE ALL ON repository_exact_tree_literal_index_build_claims FROM worksflow_application;
    IF schema_head_exists THEN
      EXECUTE format(
        'REVOKE ALL ON TABLE %I.schema_migrations FROM worksflow_application',
        schema_name
      );
    END IF;
    EXECUTE format('REVOKE ALL ON TABLE %I.repository_exact_tree_literal_index_gc_runs FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.repository_exact_tree_literal_index_gc_capabilities FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.repository_exact_tree_literal_index_gc_receipts FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.repository_exact_tree_literal_index_gc_tombstones FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.repository_exact_tree_literal_index_gc_tree_delete_auth FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.repository_exact_tree_literal_index_gc_blob_delete_auth FROM worksflow_application', schema_name);
    REVOKE ALL ON FUNCTION plan_repository_exact_tree_literal_index_gc(uuid,bigint,integer,integer,integer) FROM worksflow_application;
    REVOKE ALL ON FUNCTION execute_repository_exact_tree_literal_index_gc(uuid) FROM worksflow_application;
    REVOKE ALL ON FUNCTION inspect_repository_exact_tree_literal_index_gc_run(uuid) FROM worksflow_application;
    REVOKE ALL ON FUNCTION repository_exact_tree_literal_index_gc_readiness() FROM worksflow_application;
    REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_gc_audit_mutation() FROM worksflow_application;
    REVOKE ALL ON FUNCTION lock_candidate_exact_tree_literal_index_reference() FROM worksflow_application;
    REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_blob_mutation() FROM worksflow_application;
    REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_member_mutation() FROM worksflow_application;
    REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_manifest_delete() FROM worksflow_application;
    REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_manifest_insert() FROM worksflow_application;
    REVOKE ALL ON FUNCTION guard_repository_exact_tree_literal_index_member_insert() FROM worksflow_application;
    REVOKE ALL ON FUNCTION publish_repository_exact_tree_literal_index_manifest() FROM worksflow_application;

    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_application', schema_name);
    GRANT SELECT, INSERT ON repository_exact_tree_literal_index_blobs TO worksflow_application;
    GRANT SELECT, INSERT ON repository_exact_tree_literal_index_members TO worksflow_application;
    GRANT SELECT, INSERT, UPDATE ON repository_exact_tree_literal_index_manifests TO worksflow_application;
    GRANT SELECT ON repository_exact_tree_literal_index_build_claims TO worksflow_application;
    IF schema_head_exists THEN
      EXECUTE format(
        'GRANT SELECT ON TABLE %I.schema_migrations TO worksflow_application',
        schema_name
      );
    END IF;
    GRANT EXECUTE ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint,integer,integer,bigint,integer
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION renew_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint,integer
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION release_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION acquire_candidate_workspace_lease(
      uuid,bigint,uuid,integer
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION rotate_candidate_workspace_session(
      uuid,bigint,bigint,uuid
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION update_candidate_workspace_flags(
      uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION freeze_candidate_workspace(
      uuid,bigint,bigint,bigint,uuid,uuid,text
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION abandon_candidate_workspace(
      uuid,bigint,bigint,bigint,uuid,uuid,text
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION abandon_sandbox_session_candidate(
      uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid
    ) TO worksflow_application;
    GRANT EXECUTE ON FUNCTION complete_abandoned_sandbox_session(
      uuid,bigint,bigint,uuid
    ) TO worksflow_application;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname = 'worksflow_repository_index_gc_operator'
  ) THEN
    EXECUTE format(
      'REVOKE EXECUTE ON ALL ROUTINES IN SCHEMA %I '
      'FROM worksflow_repository_index_gc_operator',
      schema_name
    );
    REVOKE ALL ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint,integer,integer,bigint,integer
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION renew_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint,integer
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION release_repository_exact_tree_literal_index_build_claim(
      uuid,text,uuid,bigint
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION acquire_candidate_workspace_lease(
      uuid,bigint,uuid,integer
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION rotate_candidate_workspace_session(
      uuid,bigint,bigint,uuid
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION update_candidate_workspace_flags(
      uuid,bigint,bigint,bigint,uuid,boolean,boolean,boolean,text,text,text
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION freeze_candidate_workspace(
      uuid,bigint,bigint,bigint,uuid,uuid,text
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION abandon_candidate_workspace(
      uuid,bigint,bigint,bigint,uuid,uuid,text
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION abandon_sandbox_session_candidate(
      uuid,uuid,bigint,bigint,bigint,bigint,uuid,uuid,text,uuid
    ) FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON FUNCTION complete_abandoned_sandbox_session(
      uuid,bigint,bigint,uuid
    ) FROM worksflow_repository_index_gc_operator;
    EXECUTE format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_repository_index_gc_operator',
      schema_name
    );
    REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_runs
      FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_capabilities
      FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_receipts
      FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_tombstones
      FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_tree_delete_auth
      FROM worksflow_repository_index_gc_operator;
    REVOKE ALL ON TABLE repository_exact_tree_literal_index_gc_blob_delete_auth
      FROM worksflow_repository_index_gc_operator;
    GRANT EXECUTE ON FUNCTION plan_repository_exact_tree_literal_index_gc(
      uuid,bigint,integer,integer,integer
    ) TO worksflow_repository_index_gc_operator;
    GRANT EXECUTE ON FUNCTION execute_repository_exact_tree_literal_index_gc(uuid)
      TO worksflow_repository_index_gc_operator;
    GRANT EXECUTE ON FUNCTION inspect_repository_exact_tree_literal_index_gc_run(uuid)
      TO worksflow_repository_index_gc_operator;
    GRANT EXECUTE ON FUNCTION repository_exact_tree_literal_index_gc_readiness()
      TO worksflow_repository_index_gc_operator;
  END IF;

  -- These eight routines are private exact-index trigger/guard machinery.
  -- Clear every historical named grantee, not only the three stable groups,
  -- so a predecessor ACL cannot attach or invoke a SECURITY DEFINER trigger
  -- helper.  Their exact surface is owner-only.
  FOR routine_grant IN
    SELECT DISTINCT
      procedure.proname,
      pg_get_function_identity_arguments(procedure.oid) AS identity_arguments,
      privilege.grantee
    FROM pg_proc AS procedure
    CROSS JOIN LATERAL aclexplode(
      COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
    ) AS privilege
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
      AND privilege.privilege_type = 'EXECUTE'
      AND privilege.grantee <> procedure.proowner
  LOOP
    grantee_sql := CASE
      WHEN routine_grant.grantee = 0 THEN 'PUBLIC'
      ELSE quote_ident(pg_get_userbyid(routine_grant.grantee))
    END;
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.%I(%s) FROM %s',
      schema_name,
      routine_grant.proname,
      routine_grant.identity_arguments,
      grantee_sql
    );
  END LOOP;

  -- sandbox_checkpoint_is_exact is the sole helper outside the ten app
  -- definers' transitive routine closure.  Both SECURITY INVOKER Sandbox
  -- transition triggers and the SECURITY DEFINER abandon entrypoint call it,
  -- so retain one non-grantable application edge while removing every other
  -- non-owner grantee.
  FOR routine_grant IN
    SELECT
      procedure.proname,
      pg_get_function_identity_arguments(procedure.oid) AS identity_arguments,
      privilege.grantee
    FROM pg_proc AS procedure
    CROSS JOIN LATERAL aclexplode(
      COALESCE(procedure.proacl, acldefault('f', procedure.proowner))
    ) AS privilege
    WHERE procedure.oid =
      'sandbox_checkpoint_is_exact(uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text)'::regprocedure
      AND privilege.privilege_type = 'EXECUTE'
      AND privilege.grantee <> procedure.proowner
  LOOP
    grantee_sql := CASE
      WHEN routine_grant.grantee = 0 THEN 'PUBLIC'
      ELSE quote_ident(pg_get_userbyid(routine_grant.grantee))
    END;
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.%I(%s) FROM %s',
      schema_name,
      routine_grant.proname,
      routine_grant.identity_arguments,
      grantee_sql
    );
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application') THEN
    GRANT EXECUTE ON FUNCTION sandbox_checkpoint_is_exact(
      uuid,uuid,uuid,bigint,bigint,bigint,bigint,text,uuid,text,text,text
    ) TO worksflow_application;
  END IF;
END;
$privileges$;

COMMENT ON TABLE repository_exact_tree_literal_index_gc_runs IS
  'Append-only operator GC plan parameters; a run id is idempotent only with the exact same inputs.';
COMMENT ON TABLE repository_exact_tree_literal_index_gc_capabilities IS
  'Append-only, expiring full-manifest CAS authority for one exact-tree GC decision.';
COMMENT ON TABLE repository_exact_tree_literal_index_gc_receipts IS
  'Append-only terminal deleted, protected, stale, or expired outcome for every attempted capability.';
COMMENT ON TABLE repository_exact_tree_literal_index_gc_tombstones IS
  'Append-only deletion evidence keyed by immutable publication identity plus capability so a same-tree rebuild remains distinct.';
COMMENT ON FUNCTION plan_repository_exact_tree_literal_index_gc(
  uuid,bigint,integer,integer,integer
) IS
  'Ranks all ready project manifests before protection filters; retention is at least 7d, keep at least 8, batch at most 100, capability TTL at most 15m.';
COMMENT ON FUNCTION execute_repository_exact_tree_literal_index_gc(uuid) IS
  'Exclusive exact-tree then project-quota fenced GC with full manifest CAS, Candidate/all-status and live-claim protection, private xid/backend deletion authorization, and immutable terminal receipt.';
