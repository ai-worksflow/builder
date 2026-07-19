DROP TRIGGER IF EXISTS release_deployment_run_head_terminal_guard ON release_deployment_runs;
DROP FUNCTION IF EXISTS validate_release_deployment_run_head_terminal();

DROP TRIGGER IF EXISTS release_deployment_run_head_insert_guard ON release_deployment_runs;
DROP FUNCTION IF EXISTS validate_release_deployment_run_head_insert();

DROP TRIGGER IF EXISTS release_production_head_mutation_guard ON release_production_heads;
DROP FUNCTION IF EXISTS validate_release_production_head_mutation();

DROP INDEX IF EXISTS release_deployment_runs_one_nonterminal_environment_idx;
DROP TABLE IF EXISTS release_production_heads;

ALTER TABLE release_deployment_runs
  DROP CONSTRAINT IF EXISTS release_deployment_run_expected_receipt_exact_fk,
  DROP CONSTRAINT IF EXISTS release_deployment_run_expected_revision_exact_fk,
  DROP CONSTRAINT IF EXISTS release_deployment_run_expected_head_shape,
  DROP CONSTRAINT IF EXISTS release_deployment_run_environment_shape,
  DROP COLUMN IF EXISTS expected_production_receipt_hash,
  DROP COLUMN IF EXISTS expected_production_receipt_id,
  DROP COLUMN IF EXISTS expected_revision_hash,
  DROP COLUMN IF EXISTS expected_revision_id,
  DROP COLUMN IF EXISTS environment;
