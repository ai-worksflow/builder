DROP FUNCTION IF EXISTS complete_abandoned_sandbox_session(uuid, bigint, bigint, uuid);
DROP FUNCTION IF EXISTS abandon_sandbox_session_candidate(
  uuid, uuid, bigint, bigint, bigint, bigint, uuid, uuid, text, uuid
);

DROP TRIGGER IF EXISTS sandbox_session_candidate_abandon_event_guard
  ON sandbox_session_transition_events;
DROP TRIGGER IF EXISTS sandbox_session_transition_event_guard
  ON sandbox_session_transition_events;

CREATE TRIGGER sandbox_session_transition_event_guard
BEFORE INSERT ON sandbox_session_transition_events
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_transition_event();

DROP FUNCTION IF EXISTS validate_sandbox_session_abandon_event();

DELETE FROM sandbox_lifecycle_deadline_leases
WHERE action = 'abandon_cleanup';

ALTER TABLE sandbox_lifecycle_deadline_leases
  DROP CONSTRAINT sandbox_lifecycle_deadline_leases_action_check;

ALTER TABLE sandbox_lifecycle_deadline_leases
  ADD CONSTRAINT sandbox_lifecycle_deadline_leases_action_check CHECK (
    action IN ('suspend', 'terminate')
  );

ALTER TABLE sandbox_session_transition_events
  DROP CONSTRAINT sandbox_session_transition_events_event_kind_check;

ALTER TABLE sandbox_session_transition_events
  ADD CONSTRAINT sandbox_session_transition_events_event_kind_check CHECK (event_kind IN (
    'candidate.synced', 'checkpoint.attached',
    'lifecycle.started', 'lifecycle.ready', 'lifecycle.suspend_requested',
    'lifecycle.suspended', 'lifecycle.resume_requested',
    'lifecycle.terminate_requested', 'lifecycle.cancelled',
    'lifecycle.terminated', 'lifecycle.failed'
  ));
