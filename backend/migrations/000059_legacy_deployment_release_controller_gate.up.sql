-- The historical /deployments writer and Release Controller v3 used to own
-- independent single-flight locks.  That allowed a legacy static deployment
-- version and a v2 controller Run to be admitted concurrently for one
-- project.  Serialize both admission paths on the durable projects row.

-- Installing guards cannot repair authority which is already split across
-- both writers. Refuse the boundary upgrade until that exact project has been
-- reconciled explicitly by an operator.
LOCK TABLE deployments, deployment_versions, release_preview_runs,
  release_deployment_runs IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM deployment_versions AS version
    JOIN deployments AS deployment ON deployment.id = version.deployment_id
    WHERE version.status = 'deploying'
      AND deployment.status <> 'deploying'
  ) THEN
    RAISE EXCEPTION 'cannot establish legacy/controller gate while a deploying legacy version has a non-deploying parent; reconcile it explicitly'
      USING ERRCODE = '40001';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM deployments AS deployment
    WHERE (
        deployment.status = 'deploying'
        OR EXISTS (
          SELECT 1 FROM deployment_versions AS version
          WHERE version.deployment_id = deployment.id
            AND version.status = 'deploying'
        )
      )
      AND (
        EXISTS (
          SELECT 1
          FROM release_preview_runs AS preview
          WHERE preview.project_id = deployment.project_id
            AND preview.schema_version = 'release-preview-run/v2'
            AND preview.state IN (
              'queued','claimed','submitting','reconcile_wait','reconciling',
              'verifying','reconcile_blocked'
            )
        )
        OR EXISTS (
          SELECT 1
          FROM release_deployment_runs AS production
          WHERE production.project_id = deployment.project_id
            AND production.schema_version = 'release-deployment-run/v2'
            AND production.state IN (
              'queued','claimed','submitting','reconcile_wait','reconciling',
              'verifying','reconcile_blocked'
            )
        )
      )
  ) THEN
    RAISE EXCEPTION 'cannot establish legacy/controller gate while deploying legacy and active v2 authority coexist; reconcile it explicitly'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION validate_legacy_deployment_version_controller_gate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  deployment_project_id uuid;
  deployment_environment text;
  deployment_status text;
BEGIN
  SELECT deployment.project_id, deployment.environment, deployment.status
  INTO deployment_project_id, deployment_environment, deployment_status
  FROM deployments AS deployment
  WHERE deployment.id = NEW.deployment_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'legacy deployment version requires its durable deployment'
      USING ERRCODE = '23503';
  END IF;

  -- This is the shared cross-writer mutex.  The v2 Run insert guard below
  -- locks the same row before inspecting legacy deployment state.
  PERFORM 1
  FROM projects
  WHERE id = deployment_project_id
  FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'legacy deployment version requires its durable project'
      USING ERRCODE = '23503';
  END IF;

  IF deployment_environment = 'production' THEN
    RAISE EXCEPTION 'legacy production deployment is disabled; use Release Controller v3'
      USING ERRCODE = '40001';
  END IF;

  IF deployment_status <> 'deploying' OR NEW.status <> 'deploying' THEN
    RAISE EXCEPTION 'legacy preview version requires a deploying parent and must start as deploying authority'
      USING ERRCODE = '40001';
  END IF;

  IF EXISTS (
       SELECT 1
       FROM release_preview_runs
       WHERE project_id = deployment_project_id
         AND schema_version = 'release-preview-run/v2'
         AND state IN (
           'queued','claimed','submitting','reconcile_wait','reconciling',
           'verifying','reconcile_blocked'
         )
     )
     OR EXISTS (
       SELECT 1
       FROM release_deployment_runs
       WHERE project_id = deployment_project_id
         AND schema_version = 'release-deployment-run/v2'
         AND state IN (
           'queued','claimed','submitting','reconcile_wait','reconciling',
           'verifying','reconcile_blocked'
         )
     ) THEN
    RAISE EXCEPTION 'legacy preview deployment conflicts with active Release Controller v3 authority'
      USING ERRCODE = '40001';
  END IF;

  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS deployment_version_controller_singleflight_guard
  ON deployment_versions;
CREATE TRIGGER deployment_version_controller_singleflight_guard
BEFORE INSERT ON deployment_versions
FOR EACH ROW EXECUTE FUNCTION validate_legacy_deployment_version_controller_gate();

-- Replace the v2 admission guard established by migration 000056.  Invalid v1
-- writes remain historical/read-only.  Every valid v2 admission locks the
-- same project row as the legacy deployment-version trigger before checking
-- whether a legacy provider operation is already in flight.
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

  PERFORM 1
  FROM projects
  WHERE id = NEW.project_id
  FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'release delivery Run requires its durable project'
      USING ERRCODE = '23503';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM deployments AS deployment
    WHERE deployment.project_id = NEW.project_id
      AND (
        deployment.status = 'deploying'
        OR EXISTS (
          SELECT 1
          FROM deployment_versions AS version
          WHERE version.deployment_id = deployment.id
            AND version.status = 'deploying'
        )
      )
  ) THEN
    RAISE EXCEPTION 'Release Controller v3 Run conflicts with a deploying legacy deployment'
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
