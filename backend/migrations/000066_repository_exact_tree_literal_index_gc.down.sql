DO $migration$
DECLARE
  trusted_schema text := pg_catalog.current_schema();
  control_row_count bigint;
BEGIN
  IF trusted_schema IS NULL
     OR trusted_schema IN ('pg_catalog', 'information_schema')
     OR trusted_schema LIKE 'pg_temp_%' THEN
    RAISE EXCEPTION 'Exact-tree literal index GC rollback requires one trusted application schema'
      USING ERRCODE = '3F000';
  END IF;

  PERFORM pg_catalog.set_config(
    'search_path',
    pg_catalog.format('%I,pg_catalog,pg_temp', trusted_schema),
    true
  );

  -- Serialize the audit decision with both planning and execution.  The
  -- executor locks runs before capabilities; using the same order here means
  -- an uncommitted terminal receipt/tombstone cannot commit between a clean
  -- preflight count and destructive DDL.
  EXECUTE pg_catalog.format(
    'LOCK TABLE %1$I.repository_exact_tree_literal_index_gc_runs, '
    '%1$I.repository_exact_tree_literal_index_gc_capabilities, '
    '%1$I.repository_exact_tree_literal_index_gc_receipts, '
    '%1$I.repository_exact_tree_literal_index_gc_tombstones, '
    '%1$I.repository_exact_tree_literal_index_gc_tree_delete_auth, '
    '%1$I.repository_exact_tree_literal_index_gc_blob_delete_auth '
    'IN ACCESS EXCLUSIVE MODE',
    trusted_schema
  );
  EXECUTE pg_catalog.format(
    'SELECT '
    '(SELECT count(*) FROM %1$I.repository_exact_tree_literal_index_gc_runs) + '
    '(SELECT count(*) FROM %1$I.repository_exact_tree_literal_index_gc_capabilities) + '
    '(SELECT count(*) FROM %1$I.repository_exact_tree_literal_index_gc_receipts) + '
    '(SELECT count(*) FROM %1$I.repository_exact_tree_literal_index_gc_tombstones) + '
    '(SELECT count(*) FROM %1$I.repository_exact_tree_literal_index_gc_tree_delete_auth) + '
    '(SELECT count(*) FROM %1$I.repository_exact_tree_literal_index_gc_blob_delete_auth)',
    trusted_schema
  ) INTO control_row_count;
  IF control_row_count <> 0 THEN
    RAISE EXCEPTION
      'Cannot roll back exact-tree literal index GC while audit/control rows exist (runs, capabilities, receipts, tombstones, or authorizations)'
      USING ERRCODE = '55000';
  END IF;
END;
$migration$;

DROP TRIGGER IF EXISTS candidate_workspace_exact_tree_literal_index_reference_lock
  ON candidate_workspaces;
DROP FUNCTION IF EXISTS lock_candidate_exact_tree_literal_index_reference();

DROP FUNCTION IF EXISTS repository_exact_tree_literal_index_gc_readiness();
DROP FUNCTION IF EXISTS inspect_repository_exact_tree_literal_index_gc_run(uuid);
DROP FUNCTION IF EXISTS execute_repository_exact_tree_literal_index_gc(uuid);
DROP FUNCTION IF EXISTS plan_repository_exact_tree_literal_index_gc(
  uuid, bigint, integer, integer, integer
);

DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_tombstone_no_truncate
  ON repository_exact_tree_literal_index_gc_tombstones;
DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_tombstone_append_only
  ON repository_exact_tree_literal_index_gc_tombstones;
DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_receipt_no_truncate
  ON repository_exact_tree_literal_index_gc_receipts;
DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_receipt_append_only
  ON repository_exact_tree_literal_index_gc_receipts;
DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_capability_no_truncate
  ON repository_exact_tree_literal_index_gc_capabilities;
DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_capability_append_only
  ON repository_exact_tree_literal_index_gc_capabilities;
DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_run_no_truncate
  ON repository_exact_tree_literal_index_gc_runs;
DROP TRIGGER IF EXISTS repository_exact_tree_literal_index_gc_run_append_only
  ON repository_exact_tree_literal_index_gc_runs;

DROP TABLE IF EXISTS repository_exact_tree_literal_index_gc_tombstones;
DROP TABLE IF EXISTS repository_exact_tree_literal_index_gc_receipts;
DROP TABLE IF EXISTS repository_exact_tree_literal_index_gc_capabilities;
DROP TABLE IF EXISTS repository_exact_tree_literal_index_gc_runs;
DROP TABLE IF EXISTS repository_exact_tree_literal_index_gc_blob_delete_auth;
DROP TABLE IF EXISTS repository_exact_tree_literal_index_gc_tree_delete_auth;
DROP FUNCTION IF EXISTS guard_repository_exact_tree_literal_index_gc_audit_mutation();

-- Restore the unconditional immutable guards from 000062.  UPDATE, DELETE and
-- TRUNCATE all fail once the private transaction authorization feature is gone.
CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_blob_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $$
BEGIN
  RAISE EXCEPTION 'Exact-tree literal index blobs are immutable'
    USING ERRCODE = '55000';
END;
$$;
ALTER FUNCTION guard_repository_exact_tree_literal_index_blob_mutation() RESET ALL;

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_member_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $$
BEGIN
  RAISE EXCEPTION 'Exact-tree literal index members are immutable'
    USING ERRCODE = '55000';
END;
$$;
ALTER FUNCTION guard_repository_exact_tree_literal_index_member_mutation() RESET ALL;

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_manifest_delete()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $$
BEGIN
  RAISE EXCEPTION 'Exact-tree literal index manifests are immutable'
    USING ERRCODE = '55000';
END;
$$;
ALTER FUNCTION guard_repository_exact_tree_literal_index_manifest_delete() RESET ALL;

-- 000065 and every predecessor migration used PostgreSQL's PUBLIC EXECUTE
-- routine default.  000066 hardens the whole trusted schema, so downgrade
-- restores the whole predecessor surface (including routines outside the ten
-- application SECURITY DEFINER entry points), plus the future-object default.
DO $compatibility_acl$
DECLARE
  trusted_schema text := pg_catalog.current_schema();
BEGIN
  EXECUTE pg_catalog.format(
    'GRANT EXECUTE ON ALL ROUTINES IN SCHEMA %I TO PUBLIC',
    trusted_schema
  );
  EXECUTE pg_catalog.format(
    'ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON ROUTINES TO PUBLIC'
  );
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_migration_owner'
  ) THEN
    EXECUTE pg_catalog.format(
      'ALTER DEFAULT PRIVILEGES FOR ROLE worksflow_migration_owner '
      'GRANT EXECUTE ON ROUTINES TO PUBLIC'
    );
  END IF;
END;
$compatibility_acl$;

-- Keep the predecessor's ten historically exposed entry points explicit as
-- executable documentation of that compatibility surface.
GRANT EXECUTE ON FUNCTION acquire_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer, integer, bigint, integer
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION renew_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint, integer
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION release_repository_exact_tree_literal_index_build_claim(
  uuid, text, uuid, bigint
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION acquire_candidate_workspace_lease(
  uuid, bigint, uuid, integer
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION rotate_candidate_workspace_session(
  uuid, bigint, bigint, uuid
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION update_candidate_workspace_flags(
  uuid, bigint, bigint, bigint, uuid, boolean, boolean, boolean, text, text, text
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION freeze_candidate_workspace(
  uuid, bigint, bigint, bigint, uuid, uuid, text
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION abandon_candidate_workspace(
  uuid, bigint, bigint, bigint, uuid, uuid, text
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION abandon_sandbox_session_candidate(
  uuid, uuid, bigint, bigint, bigint, bigint, uuid, uuid, text, uuid
) TO PUBLIC;
GRANT EXECUTE ON FUNCTION complete_abandoned_sandbox_session(
  uuid, bigint, bigint, uuid
) TO PUBLIC;
