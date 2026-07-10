ALTER TABLE quality_runs
  ADD COLUMN build_artifact_id uuid,
  ADD COLUMN build_content_ref text,
  ADD COLUMN build_content_hash text,
  ADD COLUMN build_hash text,
  ADD COLUMN build_entry_path text,
  ADD COLUMN build_file_count integer,
  ADD COLUMN build_total_bytes bigint,
  ADD CONSTRAINT quality_runs_build_artifact_complete_check CHECK (
    (build_artifact_id IS NULL AND build_content_ref IS NULL AND build_content_hash IS NULL AND
     build_hash IS NULL AND build_entry_path IS NULL AND build_file_count IS NULL AND build_total_bytes IS NULL)
    OR
    (status = 'passed' AND build_artifact_id IS NOT NULL AND build_content_ref IS NOT NULL AND
     build_content_hash ~ '^sha256:[0-9a-f]{64}$' AND build_hash ~ '^sha256:[0-9a-f]{64}$' AND
     build_entry_path <> '' AND build_file_count > 0 AND build_total_bytes >= 0)
  );

CREATE UNIQUE INDEX quality_runs_build_artifact_id_unique
  ON quality_runs (build_artifact_id)
  WHERE build_artifact_id IS NOT NULL;

ALTER TABLE deployment_versions
  ADD COLUMN build_artifact_id uuid,
  ADD COLUMN build_content_ref text,
  ADD COLUMN build_content_hash text,
  ADD COLUMN build_hash text,
  ADD COLUMN build_entry_path text,
  ADD COLUMN build_file_count integer,
  ADD COLUMN build_total_bytes bigint,
  ADD CONSTRAINT deployment_versions_build_artifact_complete_check CHECK (
    (build_artifact_id IS NULL AND build_content_ref IS NULL AND build_content_hash IS NULL AND build_hash IS NULL AND
     build_entry_path IS NULL AND build_file_count IS NULL AND build_total_bytes IS NULL)
    OR
    (quality_run_id IS NOT NULL AND build_artifact_id IS NOT NULL AND build_content_ref IS NOT NULL AND
     build_content_hash ~ '^sha256:[0-9a-f]{64}$' AND build_hash ~ '^sha256:[0-9a-f]{64}$' AND
     build_entry_path <> '' AND build_file_count > 0 AND build_total_bytes >= 0)
  );

CREATE INDEX deployment_versions_build_artifact_idx
  ON deployment_versions (build_artifact_id, created_at DESC)
  WHERE build_artifact_id IS NOT NULL;
