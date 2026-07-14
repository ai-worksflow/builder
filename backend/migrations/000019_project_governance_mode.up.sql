ALTER TABLE projects
  ADD COLUMN governance_mode text NOT NULL DEFAULT 'team',
  ADD CONSTRAINT projects_governance_mode_check
    CHECK (governance_mode IN ('solo', 'team'));

ALTER TABLE review_decisions
  ADD COLUMN solo_self_review boolean NOT NULL DEFAULT false;

ALTER TABLE workflow_runs
  ADD COLUMN governance_mode text NOT NULL DEFAULT 'team',
  ADD CONSTRAINT workflow_runs_governance_mode_check
    CHECK (governance_mode IN ('solo', 'team'));

CREATE FUNCTION workflow_run_governance_mode_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.governance_mode IS DISTINCT FROM OLD.governance_mode THEN
    RAISE EXCEPTION 'workflow run governance mode is immutable';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER workflow_run_governance_mode_immutable
BEFORE UPDATE ON workflow_runs
FOR EACH ROW EXECUTE FUNCTION workflow_run_governance_mode_immutable();

CREATE FUNCTION review_request_policy_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.policy IS DISTINCT FROM OLD.policy THEN
    RAISE EXCEPTION 'review request policy is immutable';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER review_request_policy_immutable
BEFORE UPDATE ON review_requests
FOR EACH ROW EXECUTE FUNCTION review_request_policy_immutable();
