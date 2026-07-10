DROP INDEX IF EXISTS deployment_versions_build_artifact_idx;

ALTER TABLE deployment_versions
  DROP CONSTRAINT IF EXISTS deployment_versions_build_artifact_complete_check,
  DROP COLUMN IF EXISTS build_total_bytes,
  DROP COLUMN IF EXISTS build_file_count,
  DROP COLUMN IF EXISTS build_entry_path,
  DROP COLUMN IF EXISTS build_hash,
  DROP COLUMN IF EXISTS build_content_hash,
  DROP COLUMN IF EXISTS build_content_ref,
  DROP COLUMN IF EXISTS build_artifact_id;

DROP INDEX IF EXISTS quality_runs_build_artifact_id_unique;

ALTER TABLE quality_runs
  DROP CONSTRAINT IF EXISTS quality_runs_build_artifact_complete_check,
  DROP COLUMN IF EXISTS build_total_bytes,
  DROP COLUMN IF EXISTS build_file_count,
  DROP COLUMN IF EXISTS build_entry_path,
  DROP COLUMN IF EXISTS build_hash,
  DROP COLUMN IF EXISTS build_content_hash,
  DROP COLUMN IF EXISTS build_content_ref,
  DROP COLUMN IF EXISTS build_artifact_id;
