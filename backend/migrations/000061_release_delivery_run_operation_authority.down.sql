-- Removing the inverse Run -> Operation boundary would make already-created
-- v2 Runs interpretable under a weaker authority model.  Only an empty v2
-- boundary may be downgraded.  Historical v1 Runs remain unaffected.
LOCK TABLE release_preview_runs, release_deployment_runs,
  release_delivery_operations IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
  IF EXISTS (
       SELECT 1 FROM release_preview_runs
       WHERE schema_version = 'release-preview-run/v2'
     )
     OR EXISTS (
       SELECT 1 FROM release_deployment_runs
       WHERE schema_version = 'release-deployment-run/v2'
     )
     OR EXISTS (SELECT 1 FROM release_delivery_operations) THEN
    RAISE EXCEPTION 'cannot downgrade v2 Run operation authority while v2 Runs or delivery Operations exist'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

DROP TRIGGER IF EXISTS release_preview_run_operation_authority_guard
  ON release_preview_runs;
DROP TRIGGER IF EXISTS release_deployment_run_operation_authority_guard
  ON release_deployment_runs;
DROP FUNCTION IF EXISTS validate_release_delivery_run_operation_authority();
