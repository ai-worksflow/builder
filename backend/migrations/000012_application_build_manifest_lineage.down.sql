DROP INDEX IF EXISTS application_build_manifests_run_root_ordinal_unique;
DROP INDEX IF EXISTS application_build_manifests_root_workspace_unique;
DROP INDEX IF EXISTS application_build_manifests_derived_from_unique;
DROP INDEX IF EXISTS application_build_manifests_root_history_idx;

ALTER TABLE application_build_manifests
  DROP CONSTRAINT IF EXISTS application_build_manifests_lineage_shape_check,
  DROP CONSTRAINT IF EXISTS application_build_manifests_root_ordinal_nonnegative,
  DROP CONSTRAINT IF EXISTS application_build_manifests_workflow_group_shape_check,
  DROP CONSTRAINT IF EXISTS application_build_manifests_workflow_run_project_fk,
  DROP CONSTRAINT IF EXISTS application_build_manifests_workspace_revision_fk,
  DROP CONSTRAINT IF EXISTS application_build_manifests_derived_from_fk,
  DROP CONSTRAINT IF EXISTS application_build_manifests_root_fk,
  DROP CONSTRAINT IF EXISTS application_build_manifests_id_root_project_unique,
  DROP CONSTRAINT IF EXISTS application_build_manifests_id_project_unique,
  DROP COLUMN IF EXISTS manifest_group_key,
  DROP COLUMN IF EXISTS root_ordinal,
  DROP COLUMN IF EXISTS workspace_revision_id,
  DROP COLUMN IF EXISTS derived_from_id,
  DROP COLUMN IF EXISTS root_manifest_id;

ALTER TABLE workflow_runs
  DROP CONSTRAINT IF EXISTS workflow_runs_id_project_unique;
