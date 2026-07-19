-- A v2 Run is only the local lifecycle projection of one immutable controller
-- Operation.  Earlier boundaries guaranteed that an Operation points at an
-- exact Run, but did not guarantee the inverse: a direct SQL writer could
-- commit a Run without ever creating its controller authority.

-- Hold all three writer tables through both the upgrade scan and trigger
-- installation.  SHARE ROW EXCLUSIVE conflicts with ordinary INSERT/UPDATE
-- writers, closing the scan/install TOCTOU window.
LOCK TABLE release_preview_runs, release_deployment_runs,
  release_delivery_operations IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM release_preview_runs AS run
    WHERE run.schema_version = 'release-preview-run/v2'
      AND (
        SELECT count(*)
        FROM release_delivery_operations AS operation
        WHERE operation.project_id = run.project_id
          AND operation.kind = 'preview'
          AND operation.preview_run_id = run.id
          AND operation.deployment_run_id IS NULL
      ) <> 1
  ) THEN
    RAISE EXCEPTION 'cannot establish v2 Run authority while a Preview Run lacks exactly one matching delivery Operation'
      USING ERRCODE = '40001';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM release_deployment_runs AS run
    WHERE run.schema_version = 'release-deployment-run/v2'
      AND (
        SELECT count(*)
        FROM release_delivery_operations AS operation
        WHERE operation.project_id = run.project_id
          AND operation.kind = 'production'
          AND operation.preview_run_id IS NULL
          AND operation.deployment_run_id = run.id
      ) <> 1
  ) THEN
    RAISE EXCEPTION 'cannot establish v2 Run authority while a Deployment Run lacks exactly one matching delivery Operation'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION validate_release_delivery_run_operation_authority()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  matching_operation_count bigint;
BEGIN
  IF TG_TABLE_NAME = 'release_preview_runs' THEN
    IF NEW.schema_version <> 'release-preview-run/v2' THEN
      RETURN NEW;
    END IF;
    SELECT count(*)
    INTO matching_operation_count
    FROM release_delivery_operations AS operation
    WHERE operation.project_id = NEW.project_id
      AND operation.kind = 'preview'
      AND operation.preview_run_id = NEW.id
      AND operation.deployment_run_id IS NULL;
    IF matching_operation_count <> 1 THEN
      RAISE EXCEPTION 'v2 Preview Run must commit with exactly one matching delivery Operation'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_TABLE_NAME = 'release_deployment_runs' THEN
    IF NEW.schema_version <> 'release-deployment-run/v2' THEN
      RETURN NEW;
    END IF;
    SELECT count(*)
    INTO matching_operation_count
    FROM release_delivery_operations AS operation
    WHERE operation.project_id = NEW.project_id
      AND operation.kind = 'production'
      AND operation.preview_run_id IS NULL
      AND operation.deployment_run_id = NEW.id;
    IF matching_operation_count <> 1 THEN
      RAISE EXCEPTION 'v2 Deployment Run must commit with exactly one matching delivery Operation'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'release delivery Run operation authority guard is attached to an unexpected table'
    USING ERRCODE = '40001';
END;
$$;

CREATE CONSTRAINT TRIGGER release_preview_run_operation_authority_guard
AFTER INSERT ON release_preview_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_operation_authority();

CREATE CONSTRAINT TRIGGER release_deployment_run_operation_authority_guard
AFTER INSERT ON release_deployment_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_operation_authority();

COMMENT ON FUNCTION validate_release_delivery_run_operation_authority() IS
  'At transaction commit, every newly admitted v2 Run must have exactly one same-project, correctly typed immutable delivery Operation.';
