DROP TRIGGER IF EXISTS review_request_policy_immutable ON review_requests;
DROP FUNCTION IF EXISTS review_request_policy_immutable();

DROP TRIGGER IF EXISTS workflow_run_governance_mode_immutable ON workflow_runs;
DROP FUNCTION IF EXISTS workflow_run_governance_mode_immutable();

ALTER TABLE workflow_runs
  DROP CONSTRAINT workflow_runs_governance_mode_check,
  DROP COLUMN governance_mode;

ALTER TABLE review_decisions
  DROP COLUMN solo_self_review;

ALTER TABLE projects
  DROP CONSTRAINT projects_governance_mode_check,
  DROP COLUMN governance_mode;
