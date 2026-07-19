-- Removing the shared admission mutex while any new-generation, uncertain,
-- blocked, or currently deploying authority exists could re-open two writers.
-- Refuse the downgrade rather than silently weakening the release boundary.
DO $$
BEGIN
  IF EXISTS (
       SELECT 1 FROM release_preview_runs
       WHERE schema_version = 'release-preview-run/v2'
          OR state = 'reconcile_blocked'
     )
     OR EXISTS (
       SELECT 1 FROM release_deployment_runs
       WHERE schema_version = 'release-deployment-run/v2'
          OR state = 'reconcile_blocked'
     )
     OR EXISTS (
       SELECT 1 FROM deployments
       WHERE status = 'deploying'
     ) THEN
    RAISE EXCEPTION 'cannot downgrade legacy deployment controller gate while v2, reconcile-blocked, or deploying authority exists'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

DROP TRIGGER IF EXISTS deployment_version_controller_singleflight_guard
  ON deployment_versions;
DROP FUNCTION IF EXISTS validate_legacy_deployment_version_controller_gate();

-- Restore the migration-000056 v2 shape guard.  Migration 000058 changes
-- reconciliation mutation guards, not this insert boundary.
CREATE OR REPLACE FUNCTION validate_release_delivery_run_insert_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF (TG_TABLE_NAME = 'release_preview_runs'
        AND NEW.schema_version <> 'release-preview-run/v2')
     OR (TG_TABLE_NAME = 'release_deployment_runs'
        AND NEW.schema_version <> 'release-deployment-run/v2') THEN
    RAISE EXCEPTION 'release delivery Run v1 is historical read-only authority'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.state <> 'queued'
     OR NEW.version <> 1
     OR NEW.fence_epoch <> 0
     OR NEW.lease_worker_id IS NOT NULL
     OR NEW.lease_epoch IS NOT NULL
     OR NEW.lease_expires_at IS NOT NULL
     OR NEW.started_at IS NOT NULL
     OR NEW.finished_at IS NOT NULL THEN
    RAISE EXCEPTION 'release delivery Run v2 must start as pristine queued work'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

