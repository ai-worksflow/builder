-- Permit fenced worker heartbeats in every active execution phase and allow an
-- expired Run/Attempt pair to restart from claimed under a strictly newer
-- fence. Identity, version CAS, terminal immutability, and parent fencing stay
-- database-enforced.

INSERT INTO users (
  id, email, display_name, password_hash, disabled_at
) VALUES (
  '00000000-0000-4000-8000-000000000042',
  'verification-worker@system.worksflow',
  'Verification Worker',
  '!service-account-no-login',
  statement_timestamp()
)
ON CONFLICT (id) DO NOTHING;

CREATE OR REPLACE FUNCTION guard_candidate_verification_run_worker_transition_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  active_states constant text[] := ARRAY['claimed', 'materializing', 'preparing', 'running', 'collecting'];
  terminal_states constant text[] := ARRAY['passed', 'failed', 'error', 'cancelled', 'timed_out'];
  normal_advance boolean :=
    (OLD.state = 'claimed' AND NEW.state = 'materializing')
    OR (OLD.state = 'materializing' AND NEW.state = 'preparing')
    OR (OLD.state = 'preparing' AND NEW.state = 'running')
    OR (OLD.state = 'running' AND NEW.state = 'collecting')
    OR (OLD.state = 'collecting' AND NEW.state IN ('passed', 'failed'))
    OR (OLD.state <> ALL(terminal_states) AND NEW.state IN ('error', 'timed_out'));
BEGIN
  IF TG_OP = 'DELETE' OR OLD.state = ANY(terminal_states) THEN
    RAISE EXCEPTION 'Terminal Candidate VerificationRun facts are immutable'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.plan_id, NEW.plan_hash,
    NEW.request_key, NEW.request_hash, NEW.reason, NEW.parent_run_id,
    NEW.retry_reason, NEW.created_by, NEW.created_at, NEW.creation_transaction_id
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.plan_id, OLD.plan_hash,
    OLD.request_key, OLD.request_hash, OLD.reason, OLD.parent_run_id,
    OLD.retry_reason, OLD.created_by, OLD.created_at, OLD.creation_transaction_id
  ) OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'Candidate VerificationRun transition lost identity or version CAS'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state = 'cancelled' THEN
    IF OLD.state = 'queued' THEN
      IF NEW.fence_epoch <> 0 OR NEW.lease_epoch IS NOT NULL
         OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
         OR NEW.started_at IS NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Queued Candidate VerificationRun cancellation must close without a worker lease'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'Candidate VerificationRun cancellation lost its exact execution fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS NULL OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Candidate VerificationRun claim must establish a live worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = ANY(active_states) AND NEW.state = 'claimed'
        AND OLD.lease_expires_at <= statement_timestamp() THEN
    IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Candidate VerificationRun reclaim must advance its worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = NEW.state AND OLD.state = ANY(active_states) THEN
    IF OLD.lease_expires_at <= statement_timestamp()
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at <= OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Candidate VerificationRun heartbeat lost its live worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF normal_advance THEN
    IF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR OLD.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'Candidate VerificationRun transition lost its live worker fence'
        USING ERRCODE = '40001';
    END IF;
    IF NEW.state = ANY(terminal_states) THEN
      IF NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Terminal Candidate VerificationRun must close its lease and finish time'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Active Candidate VerificationRun changed unrelated lease or finish facts'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'Invalid Candidate VerificationRun state transition'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

DROP TRIGGER candidate_verification_run_transition_guard ON candidate_verification_runs;
CREATE TRIGGER candidate_verification_run_transition_guard
BEFORE UPDATE OR DELETE ON candidate_verification_runs
FOR EACH ROW EXECUTE FUNCTION guard_candidate_verification_run_worker_transition_v2();

CREATE OR REPLACE FUNCTION guard_candidate_verification_attempt_worker_transition_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent_run candidate_verification_runs%ROWTYPE;
  active_states constant text[] := ARRAY['claimed', 'materializing', 'preparing', 'running', 'collecting'];
  terminal_states constant text[] := ARRAY['passed', 'failed', 'error', 'cancelled', 'timed_out'];
  normal_advance boolean :=
    (OLD.state = 'claimed' AND NEW.state = 'materializing')
    OR (OLD.state = 'materializing' AND NEW.state = 'preparing')
    OR (OLD.state = 'preparing' AND NEW.state = 'running')
    OR (OLD.state = 'running' AND NEW.state = 'collecting')
    OR (OLD.state = 'collecting' AND NEW.state IN ('passed', 'failed'))
    OR (OLD.state <> ALL(terminal_states) AND NEW.state IN ('error', 'timed_out'));
BEGIN
  IF TG_OP = 'DELETE' OR OLD.state = ANY(terminal_states) THEN
    RAISE EXCEPTION 'Terminal VerificationAttempt facts are immutable'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.run_id, NEW.project_id, NEW.plan_id,
    NEW.plan_hash, NEW.ordinal, NEW.parent_attempt_id, NEW.retry_reason,
    NEW.created_by, NEW.created_at, NEW.creation_transaction_id
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.run_id, OLD.project_id, OLD.plan_id,
    OLD.plan_hash, OLD.ordinal, OLD.parent_attempt_id, OLD.retry_reason,
    OLD.created_by, OLD.created_at, OLD.creation_transaction_id
  ) OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'VerificationAttempt transition lost identity or version CAS'
      USING ERRCODE = '40001';
  END IF;
  SELECT * INTO parent_run
  FROM candidate_verification_runs AS run
  WHERE run.id = OLD.run_id;
  IF NOT FOUND OR parent_run.state = ANY(terminal_states) THEN
    RAISE EXCEPTION 'VerificationAttempt cannot advance after its Run is terminal'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.state <> 'cancelled' AND (
    parent_run.lease_expires_at IS NULL
    OR parent_run.lease_expires_at <= statement_timestamp()
    OR parent_run.lease_worker_id IS DISTINCT FROM COALESCE(NEW.lease_worker_id, OLD.lease_worker_id)
  ) THEN
    RAISE EXCEPTION 'VerificationAttempt transition lost its parent Run worker fence'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state = 'cancelled' THEN
    IF OLD.state = 'queued' THEN
      IF NEW.fence_epoch <> 0 OR NEW.lease_epoch IS NOT NULL
         OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
         OR NEW.started_at IS NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Queued VerificationAttempt cancellation must close without a worker lease'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'VerificationAttempt cancellation lost its exact execution fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS NULL OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'VerificationAttempt claim must establish a live worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = ANY(active_states) AND NEW.state = 'claimed'
        AND OLD.lease_expires_at <= statement_timestamp() THEN
    IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'VerificationAttempt reclaim must advance its worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = NEW.state AND OLD.state = ANY(active_states) THEN
    IF OLD.lease_expires_at <= statement_timestamp()
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at <= OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'VerificationAttempt heartbeat lost its live worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF normal_advance THEN
    IF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR OLD.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'VerificationAttempt transition lost its live worker fence'
        USING ERRCODE = '40001';
    END IF;
    IF NEW.state = ANY(terminal_states) THEN
      IF NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Terminal VerificationAttempt must close its lease and finish time'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Active VerificationAttempt changed unrelated lease or finish facts'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'Invalid VerificationAttempt state transition'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

DROP TRIGGER candidate_verification_attempt_transition_guard ON candidate_verification_attempts;
CREATE TRIGGER candidate_verification_attempt_transition_guard
BEFORE UPDATE OR DELETE ON candidate_verification_attempts
FOR EACH ROW EXECUTE FUNCTION guard_candidate_verification_attempt_worker_transition_v2();

COMMENT ON FUNCTION guard_candidate_verification_run_worker_transition_v2() IS
  'Fenced active-phase heartbeat and expired execution reclaim policy for Candidate VerificationRun.';
COMMENT ON FUNCTION guard_candidate_verification_attempt_worker_transition_v2() IS
  'Fenced active-phase heartbeat and expired execution reclaim policy for VerificationAttempt.';
