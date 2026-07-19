DROP TRIGGER IF EXISTS candidate_verification_attempt_transition_guard
  ON candidate_verification_attempts;
CREATE TRIGGER candidate_verification_attempt_transition_guard
BEFORE UPDATE OR DELETE ON candidate_verification_attempts
FOR EACH ROW EXECUTE FUNCTION guard_candidate_verification_attempt_transition();
DROP FUNCTION IF EXISTS guard_candidate_verification_attempt_worker_transition_v2();

DROP TRIGGER IF EXISTS candidate_verification_run_transition_guard
  ON candidate_verification_runs;
CREATE TRIGGER candidate_verification_run_transition_guard
BEFORE UPDATE OR DELETE ON candidate_verification_runs
FOR EACH ROW EXECUTE FUNCTION guard_candidate_verification_run_transition();
DROP FUNCTION IF EXISTS guard_candidate_verification_run_worker_transition_v2();

-- The disabled verification service principal is deliberately retained: Run,
-- Attempt, and Receipt audit rows may reference it and history is immutable.
