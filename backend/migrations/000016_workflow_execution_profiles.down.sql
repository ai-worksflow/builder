DROP TRIGGER IF EXISTS workflow_run_execution_profile_immutable ON workflow_runs;
DROP FUNCTION IF EXISTS guard_workflow_run_execution_profile_identity();
DROP TRIGGER IF EXISTS workflow_definition_execution_profile_immutable ON workflow_definition_versions;
DROP FUNCTION IF EXISTS guard_workflow_definition_execution_profile_identity();

ALTER TABLE workflow_runs
  DROP CONSTRAINT IF EXISTS workflow_runs_definition_execution_profile_fk,
  DROP CONSTRAINT IF EXISTS workflow_runs_execution_profile_hash_check,
  DROP CONSTRAINT IF EXISTS workflow_runs_execution_profile_version_check,
  DROP COLUMN IF EXISTS execution_profile_hash,
  DROP COLUMN IF EXISTS execution_profile_version;

ALTER TABLE workflow_definition_versions
  DROP CONSTRAINT IF EXISTS workflow_definition_versions_execution_profile_identity_unique,
  DROP CONSTRAINT IF EXISTS workflow_definition_versions_execution_profile_content_check,
  DROP CONSTRAINT IF EXISTS workflow_definition_versions_execution_profile_hash_check,
  DROP CONSTRAINT IF EXISTS workflow_definition_versions_execution_profile_version_check,
  DROP COLUMN IF EXISTS execution_profile_hash,
  DROP COLUMN IF EXISTS execution_profile_version;
