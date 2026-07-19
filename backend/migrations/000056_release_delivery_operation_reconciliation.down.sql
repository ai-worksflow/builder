-- Never erase unresolved remote-operation authority during rollback.  A v2
-- Run, a blocked legacy Run, or even a prepared Operation requires explicit
-- reconciliation/export before the old resubmitting worker can be restored.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM release_preview_runs
    WHERE schema_version = 'release-preview-run/v2'
       OR state IN ('submitting','reconcile_wait','reconciling','reconcile_blocked')
  ) OR EXISTS (
    SELECT 1 FROM release_deployment_runs
    WHERE schema_version = 'release-deployment-run/v2'
       OR state IN ('submitting','reconcile_wait','reconciling','reconcile_blocked')
  ) OR EXISTS (
    SELECT 1 FROM release_delivery_operations
  ) OR EXISTS (
    SELECT 1 FROM release_preview_receipts
    WHERE schema_version = 'release-preview-receipt/v2'
       OR controller_operation_id IS NOT NULL
  ) OR EXISTS (
    SELECT 1 FROM release_production_receipts
    WHERE schema_version = 'release-production-receipt/v2'
       OR controller_operation_id IS NOT NULL
  ) OR EXISTS (
    SELECT 1 FROM release_deployment_revisions
    WHERE schema_version = 'release-deployment-revision/v2'
       OR controller_operation_id IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'cannot downgrade release delivery reconciliation while v2, active, blocked, or operation authority exists'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

DROP TRIGGER IF EXISTS release_preview_run_update_guard ON release_preview_runs;
DROP TRIGGER IF EXISTS release_deployment_run_update_guard ON release_deployment_runs;
DROP TRIGGER IF EXISTS release_preview_run_v2_insert_guard ON release_preview_runs;
DROP TRIGGER IF EXISTS release_deployment_run_v2_insert_guard ON release_deployment_runs;

DROP INDEX IF EXISTS release_preview_runs_claim_idx;
DROP INDEX IF EXISTS release_deployment_runs_claim_idx;
DROP INDEX IF EXISTS release_deployment_runs_one_nonterminal_environment_idx;

DROP TRIGGER IF EXISTS release_delivery_operation_attempt_projection
  ON release_delivery_operation_attempts;
DROP TRIGGER IF EXISTS release_delivery_operation_result_immutable
  ON release_delivery_operation_results;
DROP TRIGGER IF EXISTS release_delivery_operation_result_insert_guard
  ON release_delivery_operation_results;
DROP TRIGGER IF EXISTS release_delivery_operation_attempt_mutation_guard
  ON release_delivery_operation_attempts;
DROP TRIGGER IF EXISTS release_delivery_operation_attempt_insert_guard
  ON release_delivery_operation_attempts;
DROP TRIGGER IF EXISTS release_delivery_operation_mutation_guard
  ON release_delivery_operations;
DROP TRIGGER IF EXISTS release_delivery_operation_insert_guard
  ON release_delivery_operations;

DROP TRIGGER IF EXISTS release_preview_receipt_operation_authority_guard
  ON release_preview_receipts;
DROP TRIGGER IF EXISTS release_production_receipt_operation_authority_guard
  ON release_production_receipts;
DROP TRIGGER IF EXISTS release_deployment_revision_operation_authority_guard
  ON release_deployment_revisions;
DROP FUNCTION IF EXISTS validate_release_delivery_fact_operation_authority_insert();

ALTER TABLE release_preview_receipts
  DROP CONSTRAINT release_preview_receipt_controller_result_exact_fk,
  DROP CONSTRAINT release_preview_receipt_controller_shape,
  DROP CONSTRAINT release_preview_receipts_schema_version_check,
  DROP COLUMN controller_operation_id,
  DROP COLUMN controller_result_hash,
  ADD CONSTRAINT release_preview_receipts_schema_version_check CHECK (
    schema_version = 'release-preview-receipt/v1'
  );

ALTER TABLE release_production_receipts
  DROP CONSTRAINT release_production_receipt_controller_result_exact_fk,
  DROP CONSTRAINT release_production_receipt_controller_shape,
  DROP CONSTRAINT release_production_receipts_schema_version_check,
  DROP COLUMN controller_operation_id,
  DROP COLUMN controller_result_hash,
  ADD CONSTRAINT release_production_receipts_schema_version_check CHECK (
    schema_version = 'release-production-receipt/v1'
  );

ALTER TABLE release_deployment_revisions
  DROP CONSTRAINT release_deployment_revision_controller_result_exact_fk,
  DROP CONSTRAINT release_deployment_revision_controller_shape,
  DROP CONSTRAINT release_deployment_revisions_schema_version_check,
  DROP COLUMN controller_operation_id,
  DROP COLUMN controller_result_hash,
  ADD CONSTRAINT release_deployment_revisions_schema_version_check CHECK (
    schema_version = 'release-deployment-revision/v1'
  );

ALTER TABLE release_delivery_operations
  DROP CONSTRAINT IF EXISTS release_delivery_operation_terminal_result_exact_fk;

DROP TABLE release_delivery_operation_results;
DROP TABLE release_delivery_operation_attempts;
DROP TABLE release_delivery_operations;

DROP FUNCTION IF EXISTS project_release_delivery_operation_attempt_insert();
DROP FUNCTION IF EXISTS prevent_release_delivery_operation_result_mutation();
DROP FUNCTION IF EXISTS validate_release_delivery_operation_result_insert();
DROP FUNCTION IF EXISTS validate_release_delivery_operation_attempt_mutation();
DROP FUNCTION IF EXISTS validate_release_delivery_operation_attempt_insert();
DROP FUNCTION IF EXISTS validate_release_delivery_operation_mutation();
DROP FUNCTION IF EXISTS validate_release_delivery_operation_insert();
DROP FUNCTION IF EXISTS validate_release_delivery_run_insert_v2();
DROP FUNCTION IF EXISTS release_delivery_canonical_json(jsonb);

ALTER TABLE release_preview_runs
  DROP CONSTRAINT release_preview_runs_schema_version_check,
  DROP CONSTRAINT release_preview_runs_state_check,
  DROP CONSTRAINT release_preview_run_terminal_shape,
  DROP CONSTRAINT release_preview_run_lease_shape,
  ADD CONSTRAINT release_preview_runs_schema_version_check CHECK (
    schema_version = 'release-preview-run/v1'
  ),
  ADD CONSTRAINT release_preview_runs_state_check CHECK (
    state IN ('queued','claimed','deploying','verifying','passed','failed','error','cancelled')
  ),
  ADD CONSTRAINT release_preview_run_terminal_shape CHECK (
    (state IN ('passed','failed','error','cancelled') AND finished_at IS NOT NULL)
    OR (state NOT IN ('passed','failed','error','cancelled') AND finished_at IS NULL)
  ),
  ADD CONSTRAINT release_preview_run_lease_shape CHECK (
    (state IN ('claimed','deploying','verifying') AND lease_worker_id IS NOT NULL
      AND lease_epoch IS NOT NULL AND lease_expires_at IS NOT NULL)
    OR (state NOT IN ('claimed','deploying','verifying') AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  );

ALTER TABLE release_deployment_runs
  DROP CONSTRAINT release_deployment_runs_schema_version_check,
  DROP CONSTRAINT release_deployment_runs_state_check,
  DROP CONSTRAINT release_deployment_run_terminal_shape,
  DROP CONSTRAINT release_deployment_run_lease_shape,
  ADD CONSTRAINT release_deployment_runs_schema_version_check CHECK (
    schema_version = 'release-deployment-run/v1'
  ),
  ADD CONSTRAINT release_deployment_runs_state_check CHECK (
    state IN ('queued','claimed','deploying','verifying','healthy','failed','error','cancelled')
  ),
  ADD CONSTRAINT release_deployment_run_terminal_shape CHECK (
    (state IN ('healthy','failed','error','cancelled') AND finished_at IS NOT NULL)
    OR (state NOT IN ('healthy','failed','error','cancelled') AND finished_at IS NULL)
  ),
  ADD CONSTRAINT release_deployment_run_lease_shape CHECK (
    (state IN ('claimed','deploying','verifying') AND lease_worker_id IS NOT NULL
      AND lease_epoch IS NOT NULL AND lease_expires_at IS NOT NULL)
    OR (state NOT IN ('claimed','deploying','verifying') AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  );

CREATE INDEX release_preview_runs_claim_idx
  ON release_preview_runs (state, lease_expires_at, created_at, id)
  WHERE state IN ('queued','claimed','deploying','verifying');

CREATE INDEX release_deployment_runs_claim_idx
  ON release_deployment_runs (state, lease_expires_at, created_at, id)
  WHERE state IN ('queued','claimed','deploying','verifying');

CREATE UNIQUE INDEX release_deployment_runs_one_nonterminal_environment_idx
  ON release_deployment_runs (project_id, environment)
  WHERE state IN ('queued','claimed','deploying','verifying');

CREATE OR REPLACE FUNCTION validate_release_delivery_run_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_identity jsonb;
  new_identity jsonb;
BEGIN
  old_identity := to_jsonb(OLD) - ARRAY[
    'state','version','fence_epoch','lease_worker_id','lease_epoch','lease_expires_at',
    'started_at','finished_at','updated_by','updated_at'
  ];
  new_identity := to_jsonb(NEW) - ARRAY[
    'state','version','fence_epoch','lease_worker_id','lease_epoch','lease_expires_at',
    'started_at','finished_at','updated_by','updated_at'
  ];
  IF old_identity IS DISTINCT FROM new_identity THEN
    RAISE EXCEPTION 'release delivery Run authority identity is immutable'
      USING ERRCODE = '55000';
  END IF;

  IF NEW.state = OLD.state THEN
    IF OLD.state NOT IN ('claimed','deploying','verifying')
       OR NEW.version <> OLD.version
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_expires_at <= OLD.lease_expires_at THEN
      RAISE EXCEPTION 'release delivery same-state update must be a fenced lease renewal'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'release delivery transition must increment version exactly once'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state = 'claimed' THEN
    IF NOT (
      OLD.state = 'queued'
      OR (OLD.state IN ('claimed','deploying','verifying')
        AND OLD.lease_expires_at <= statement_timestamp())
    ) OR NEW.fence_epoch <> OLD.fence_epoch + 1
      OR NEW.lease_epoch <> NEW.fence_epoch THEN
      RAISE EXCEPTION 'release delivery claim must establish a new fence over queued or expired work'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.fence_epoch <> OLD.fence_epoch
     OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
     OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch THEN
    IF NEW.state NOT IN ('passed','healthy','failed','error','cancelled')
       OR NEW.lease_worker_id IS NOT NULL
       OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL THEN
      RAISE EXCEPTION 'release delivery transition changed its worker fence'
        USING ERRCODE = '40001';
    END IF;
  END IF;

  IF NOT (
    (OLD.state = 'claimed' AND NEW.state = 'deploying')
    OR (OLD.state = 'deploying' AND NEW.state = 'verifying')
    OR (OLD.state IN ('claimed','deploying','verifying')
      AND NEW.state IN ('failed','error','cancelled'))
    OR (TG_TABLE_NAME = 'release_preview_runs'
      AND OLD.state = 'verifying' AND NEW.state IN ('passed','failed'))
    OR (TG_TABLE_NAME = 'release_deployment_runs'
      AND OLD.state = 'verifying' AND NEW.state IN ('healthy','failed'))
  ) THEN
    RAISE EXCEPTION 'invalid release delivery state transition'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_preview_runs' AND NEW.state IN ('passed','failed')
     AND NOT EXISTS (
       SELECT 1 FROM release_preview_receipts AS receipt
       WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
         AND receipt.decision = NEW.state
     ) THEN
    RAISE EXCEPTION 'terminal Preview Run requires its exact immutable PreviewReceipt'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'failed'
     AND NOT EXISTS (
       SELECT 1 FROM release_production_receipts AS receipt
       WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
         AND receipt.decision = 'failed'
     ) THEN
    RAISE EXCEPTION 'failed production Run requires its exact immutable failed ProductionReceipt'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'healthy'
     AND NOT EXISTS (
       SELECT 1
       FROM release_production_receipts AS receipt
       JOIN release_deployment_revisions AS revision
         ON revision.production_receipt_id = receipt.id
        AND revision.production_receipt_hash = receipt.payload_hash
       WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
         AND receipt.decision = 'passed' AND revision.run_id = NEW.id
     ) THEN
    RAISE EXCEPTION 'healthy production Run requires its exact passed ProductionReceipt and DeploymentRevision'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_preview_run_update_guard
BEFORE UPDATE ON release_preview_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_update();

CREATE TRIGGER release_deployment_run_update_guard
BEFORE UPDATE ON release_deployment_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_update();
