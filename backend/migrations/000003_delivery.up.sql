CREATE TABLE quality_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  workflow_run_id uuid REFERENCES workflow_runs(id) ON DELETE SET NULL,
  workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  workspace_content_hash text NOT NULL,
  report_artifact_id uuid REFERENCES artifacts(id) ON DELETE RESTRICT,
  report_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  status text NOT NULL,
  score integer NOT NULL DEFAULT 0,
  runner_version text NOT NULL,
  sandbox_kind text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  started_at timestamptz NOT NULL,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT quality_runs_status_check CHECK (status IN ('running', 'passed', 'failed', 'error')),
  CONSTRAINT quality_runs_score_check CHECK (score BETWEEN 0 AND 100),
  CONSTRAINT quality_runs_version_positive CHECK (version > 0)
);

CREATE INDEX quality_runs_project_created_idx
  ON quality_runs (project_id, created_at DESC);
CREATE INDEX quality_runs_workspace_idx
  ON quality_runs (workspace_revision_id, created_at DESC);
CREATE INDEX quality_runs_workflow_idx
  ON quality_runs (workflow_run_id, created_at DESC)
  WHERE workflow_run_id IS NOT NULL;

CREATE TABLE quality_diagnostics (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  quality_run_id uuid NOT NULL REFERENCES quality_runs(id) ON DELETE CASCADE,
  check_id text NOT NULL,
  code text NOT NULL,
  severity text NOT NULL,
  message text NOT NULL,
  path text,
  line integer,
  column_number integer,
  suggestion text,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT quality_diagnostics_severity_check CHECK (severity IN ('error', 'warning', 'info')),
  CONSTRAINT quality_diagnostics_line_check CHECK (line IS NULL OR line > 0),
  CONSTRAINT quality_diagnostics_column_check CHECK (column_number IS NULL OR column_number > 0)
);

CREATE INDEX quality_diagnostics_run_idx
  ON quality_diagnostics (quality_run_id, severity, check_id);

CREATE TABLE deployments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  environment text NOT NULL,
  environment_ref text NOT NULL DEFAULT 'default',
  provider text NOT NULL,
  status text NOT NULL,
  active_version_id uuid,
  public_url text,
  version bigint NOT NULL DEFAULT 1,
  last_error text,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT deployments_environment_check CHECK (environment IN ('preview', 'production')),
  CONSTRAINT deployments_status_check CHECK (status IN ('pending', 'deploying', 'ready', 'failed')),
  CONSTRAINT deployments_version_positive CHECK (version > 0),
  CONSTRAINT deployments_project_environment_unique UNIQUE (project_id, environment)
);

CREATE INDEX deployments_project_updated_idx
  ON deployments (project_id, updated_at DESC);

CREATE TABLE deployment_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  deployment_id uuid NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  number bigint NOT NULL,
  action text NOT NULL,
  source_version_id uuid REFERENCES deployment_versions(id) ON DELETE RESTRICT,
  workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  workspace_content_hash text NOT NULL,
  build_manifest_id uuid REFERENCES application_build_manifests(id) ON DELETE SET NULL,
  quality_run_id uuid REFERENCES quality_runs(id) ON DELETE SET NULL,
  provider_ref text NOT NULL DEFAULT '',
  public_url text,
  entry_path text NOT NULL,
  checksum text NOT NULL,
  file_count integer NOT NULL,
  total_bytes bigint NOT NULL,
  environment_ref text NOT NULL,
  environment_variable_names jsonb NOT NULL DEFAULT '[]'::jsonb,
  status text NOT NULL,
  message text NOT NULL DEFAULT '',
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT deployment_versions_number_positive CHECK (number > 0),
  CONSTRAINT deployment_versions_action_check CHECK (action IN ('publish', 'rollback')),
  CONSTRAINT deployment_versions_status_check CHECK (status IN ('deploying', 'ready', 'failed')),
  CONSTRAINT deployment_versions_file_count_check CHECK (file_count >= 0),
  CONSTRAINT deployment_versions_total_bytes_check CHECK (total_bytes >= 0),
  CONSTRAINT deployment_versions_number_unique UNIQUE (deployment_id, number)
);

ALTER TABLE deployments
  ADD CONSTRAINT deployments_active_version_fk
  FOREIGN KEY (active_version_id) REFERENCES deployment_versions(id) ON DELETE RESTRICT
  DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX deployment_versions_deployment_created_idx
  ON deployment_versions (deployment_id, number DESC);
CREATE INDEX deployment_versions_workspace_idx
  ON deployment_versions (workspace_revision_id, created_at DESC);

CREATE TABLE deployment_logs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  deployment_id uuid NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  deployment_version_id uuid REFERENCES deployment_versions(id) ON DELETE CASCADE,
  sequence bigint NOT NULL,
  level text NOT NULL,
  message text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT deployment_logs_level_check CHECK (level IN ('info', 'warning', 'error')),
  CONSTRAINT deployment_logs_sequence_positive CHECK (sequence > 0),
  CONSTRAINT deployment_logs_sequence_unique UNIQUE (deployment_id, sequence)
);

CREATE INDEX deployment_logs_deployment_idx
  ON deployment_logs (deployment_id, sequence ASC);
