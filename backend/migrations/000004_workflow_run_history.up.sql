CREATE INDEX workflow_runs_project_history_idx
  ON workflow_runs (project_id, created_at DESC, id DESC);

CREATE INDEX workflow_runs_project_status_history_idx
  ON workflow_runs (project_id, status, created_at DESC, id DESC);
