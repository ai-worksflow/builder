DROP TRIGGER IF EXISTS release_deployment_run_update_guard ON release_deployment_runs;
DROP TRIGGER IF EXISTS release_preview_run_update_guard ON release_preview_runs;
DROP FUNCTION IF EXISTS validate_release_delivery_run_update();

DROP TRIGGER IF EXISTS release_deployment_revision_immutable ON release_deployment_revisions;
DROP TRIGGER IF EXISTS release_production_receipt_immutable ON release_production_receipts;
DROP TRIGGER IF EXISTS release_promotion_approval_immutable ON release_promotion_approvals;
DROP TRIGGER IF EXISTS release_preview_receipt_immutable ON release_preview_receipts;
DROP FUNCTION IF EXISTS prevent_release_delivery_fact_mutation();

DROP TRIGGER IF EXISTS release_deployment_revision_insert_guard ON release_deployment_revisions;
DROP FUNCTION IF EXISTS validate_release_deployment_revision_insert();

ALTER TABLE IF EXISTS release_production_receipts
  DROP CONSTRAINT IF EXISTS release_production_receipt_source_exact_fk;

ALTER TABLE IF EXISTS release_deployment_revisions
  DROP CONSTRAINT IF EXISTS release_deployment_revision_source_exact_fk;
ALTER TABLE IF EXISTS release_deployment_runs
  DROP CONSTRAINT IF EXISTS release_deployment_run_source_exact_fk;

DROP TABLE IF EXISTS release_deployment_revisions;

DROP TRIGGER IF EXISTS release_production_receipt_insert_guard ON release_production_receipts;
DROP FUNCTION IF EXISTS validate_release_production_receipt_insert();
DROP TABLE IF EXISTS release_production_receipts;

DROP TABLE IF EXISTS release_deployment_runs;

DROP TRIGGER IF EXISTS release_promotion_approval_insert_guard ON release_promotion_approvals;
DROP FUNCTION IF EXISTS validate_release_promotion_approval_insert();
DROP TABLE IF EXISTS release_promotion_approvals;

DROP TRIGGER IF EXISTS release_preview_receipt_insert_guard ON release_preview_receipts;
DROP FUNCTION IF EXISTS validate_release_preview_receipt_insert();
DROP TABLE IF EXISTS release_preview_receipts;
DROP TABLE IF EXISTS release_preview_runs;
