-- Candidate reconciliation is still required before an expired SandboxSession
-- can emit an exact terminal lifecycle event. Such reconciliation may commit
-- after expires_at and must not move operational activity beyond the hard TTL.

CREATE OR REPLACE FUNCTION sync_sandbox_session_activity_from_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO sandbox_session_activity (
    session_id, session_epoch, last_activity_at, idle_deadline, updated_at
  ) VALUES (
    NEW.id,
    NEW.session_epoch,
    LEAST(NEW.updated_at, NEW.idle_deadline),
    NEW.idle_deadline,
    statement_timestamp()
  )
  ON CONFLICT (session_id) DO UPDATE
  SET session_epoch = EXCLUDED.session_epoch,
      last_activity_at = LEAST(
        GREATEST(
          sandbox_session_activity.last_activity_at,
          EXCLUDED.last_activity_at
        ),
        EXCLUDED.idle_deadline
      ),
      idle_deadline = EXCLUDED.idle_deadline,
      updated_at = statement_timestamp();
  RETURN NEW;
END;
$$;

-- Existing failed attempts are retryable operational state. Wake them now so
-- the corrected worker can reconcile and terminate without waiting for the
-- previous bounded backoff.
UPDATE sandbox_lifecycle_deadline_leases
SET next_attempt_at = statement_timestamp(),
    lease_expires_at = GREATEST(lease_expires_at, statement_timestamp()),
    updated_at = statement_timestamp()
WHERE action = 'terminate'
  AND last_error IS NOT NULL;
