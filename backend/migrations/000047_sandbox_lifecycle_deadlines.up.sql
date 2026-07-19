-- Operational leases for server-owned SandboxSession TTL enforcement. The
-- lease is deliberately separate from the immutable session/event projection:
-- workers may retry or take over, while lifecycle facts still advance only
-- through transition_sandbox_session and its append-only audit chain.

INSERT INTO users (
  id, email, display_name, password_hash, disabled_at
) VALUES (
  '00000000-0000-4000-8000-000000000047',
  'sandbox-lifecycle-worker@system.worksflow',
  'Sandbox Lifecycle Worker',
  '!service-account-no-login',
  statement_timestamp()
)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE sandbox_session_activity (
  session_id uuid PRIMARY KEY REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  last_activity_at timestamptz NOT NULL,
  idle_deadline timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT sandbox_session_activity_time_order CHECK (
    idle_deadline >= last_activity_at AND updated_at >= last_activity_at
  )
);

INSERT INTO sandbox_session_activity (
  session_id, session_epoch, last_activity_at, idle_deadline, updated_at
)
SELECT id, session_epoch, updated_at, idle_deadline, statement_timestamp()
FROM sandbox_sessions;

CREATE INDEX sandbox_session_activity_deadline_idx
  ON sandbox_session_activity (idle_deadline, session_id);

CREATE OR REPLACE FUNCTION sync_sandbox_session_activity_from_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO sandbox_session_activity (
    session_id, session_epoch, last_activity_at, idle_deadline, updated_at
  ) VALUES (
    NEW.id, NEW.session_epoch, NEW.updated_at, NEW.idle_deadline, statement_timestamp()
  )
  ON CONFLICT (session_id) DO UPDATE
  SET session_epoch = EXCLUDED.session_epoch,
      last_activity_at = GREATEST(
        sandbox_session_activity.last_activity_at,
        EXCLUDED.last_activity_at
      ),
      idle_deadline = EXCLUDED.idle_deadline,
      updated_at = statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_session_activity_projection
AFTER INSERT OR UPDATE ON sandbox_sessions
FOR EACH ROW EXECUTE FUNCTION sync_sandbox_session_activity_from_projection();

CREATE TABLE sandbox_lifecycle_deadline_leases (
  session_id uuid PRIMARY KEY REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  action text NOT NULL CHECK (action IN ('suspend', 'terminate')),
  lease_owner text NOT NULL CHECK (
    lease_owner = btrim(lease_owner) AND length(lease_owner) BETWEEN 1 AND 200
  ),
  lease_epoch bigint NOT NULL CHECK (lease_epoch > 0),
  lease_expires_at timestamptz NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  attempt_count bigint NOT NULL CHECK (attempt_count > 0),
  last_error text CHECK (
    last_error IS NULL OR (
      last_error = btrim(last_error) AND length(last_error) BETWEEN 1 AND 2000
    )
  ),
  claimed_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT sandbox_lifecycle_deadline_lease_time_order CHECK (
    lease_expires_at > claimed_at AND next_attempt_at >= claimed_at
  )
);

CREATE INDEX sandbox_lifecycle_deadline_retry_idx
  ON sandbox_lifecycle_deadline_leases (next_attempt_at, lease_expires_at, session_id);
