-- Candidate abandonment is a destructive project operation. Commit the
-- Candidate terminal event and the owning SandboxSession mutation fence in one
-- transaction, then let runtime cleanup complete through an idempotent second
-- event. A failed cleanup remains durably visible as terminating+abandoned.

ALTER TABLE sandbox_session_transition_events
  DROP CONSTRAINT sandbox_session_transition_events_event_kind_check;

ALTER TABLE sandbox_session_transition_events
  ADD CONSTRAINT sandbox_session_transition_events_event_kind_check CHECK (event_kind IN (
    'candidate.synced', 'checkpoint.attached',
    'candidate.abandoned', 'candidate.abandon_completed',
    'lifecycle.started', 'lifecycle.ready', 'lifecycle.suspend_requested',
    'lifecycle.suspended', 'lifecycle.resume_requested',
    'lifecycle.terminate_requested', 'lifecycle.cancelled',
    'lifecycle.terminated', 'lifecycle.failed'
  ));

ALTER TABLE sandbox_lifecycle_deadline_leases
  DROP CONSTRAINT sandbox_lifecycle_deadline_leases_action_check;

ALTER TABLE sandbox_lifecycle_deadline_leases
  ADD CONSTRAINT sandbox_lifecycle_deadline_leases_action_check CHECK (
    action IN ('suspend', 'terminate', 'abandon_cleanup')
  );

CREATE OR REPLACE FUNCTION validate_sandbox_session_abandon_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_sessions%ROWTYPE;
  candidate candidate_workspaces%ROWTYPE;
  abandonment candidate_workspace_control_events%ROWTYPE;
BEGIN
  SELECT * INTO parent
  FROM sandbox_sessions
  WHERE id = NEW.session_id
  FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'SandboxSession does not exist'
      USING ERRCODE = '23503';
  END IF;

  SELECT * INTO candidate
  FROM candidate_workspaces
  WHERE id = parent.candidate_id
  FOR SHARE;

  IF NOT FOUND OR candidate.status <> 'abandoned' THEN
    RAISE EXCEPTION 'Candidate abandon reconciliation requires the exact abandoned Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.sequence <> parent.version
     OR NEW.session_version_from <> parent.version
     OR NEW.session_version_to <> parent.version + 1
     OR NEW.state_from <> parent.state
     OR NEW.session_epoch_from <> parent.session_epoch
     OR NEW.session_epoch_to <> parent.session_epoch
     OR NEW.candidate_version_from <> parent.candidate_version
     OR NEW.candidate_journal_sequence_from <> parent.candidate_journal_sequence
     OR NEW.candidate_session_epoch_from <> parent.candidate_session_epoch
     OR NEW.candidate_writer_lease_epoch_from <> parent.candidate_writer_lease_epoch
     OR ROW(
       NEW.candidate_tree_store_from, NEW.candidate_tree_owner_id_from,
       NEW.candidate_tree_ref_from, NEW.candidate_tree_content_hash_from,
       NEW.candidate_tree_hash_from,
       NEW.candidate_dirty_from, NEW.candidate_conflicted_from,
       NEW.candidate_stale_from, NEW.candidate_rebase_required_from,
       NEW.latest_checkpoint_id_from, NEW.failure_reason_from
     ) IS DISTINCT FROM ROW(
       parent.candidate_tree_store, parent.candidate_tree_owner_id,
       parent.candidate_tree_ref, parent.candidate_tree_content_hash,
       parent.candidate_tree_hash,
       parent.candidate_dirty, parent.candidate_conflicted,
       parent.candidate_stale, parent.candidate_rebase_required,
       parent.latest_checkpoint_id, parent.failure_reason
     )
     OR NEW.latest_checkpoint_id_to IS DISTINCT FROM parent.latest_checkpoint_id
     OR NEW.failure_reason_to IS DISTINCT FROM parent.failure_reason THEN
    RAISE EXCEPTION 'Candidate abandon event failed SandboxSession CAS or projection precondition'
      USING ERRCODE = '40001';
  END IF;

  IF ROW(
    NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
    NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
    NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
    NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
    NEW.candidate_tree_hash_to,
    NEW.candidate_dirty_to, NEW.candidate_conflicted_to,
    NEW.candidate_stale_to, NEW.candidate_rebase_required_to
  ) IS DISTINCT FROM ROW(
    candidate.version, candidate.journal_sequence,
    candidate.session_epoch, candidate.writer_lease_epoch,
    candidate.current_tree_store, candidate.current_tree_owner_id,
    candidate.current_tree_ref, candidate.current_tree_content_hash,
    candidate.current_tree_hash,
    candidate.dirty, candidate.conflicted,
    candidate.stale, candidate.rebase_required
  ) OR NEW.candidate_session_epoch_to <> NEW.session_epoch_to THEN
    RAISE EXCEPTION 'Candidate abandon event does not project the exact abandoned Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.event_kind = 'candidate.abandoned' THEN
    IF parent.state <> 'ready'
       OR NEW.state_to <> 'terminating'
       OR candidate.version <> parent.candidate_version + 1
       OR candidate.writer_lease_epoch <> parent.candidate_writer_lease_epoch + 1
       OR candidate.journal_sequence <> parent.candidate_journal_sequence
       OR candidate.session_epoch <> parent.candidate_session_epoch
       OR ROW(
         candidate.current_tree_store, candidate.current_tree_owner_id,
         candidate.current_tree_ref, candidate.current_tree_content_hash,
         candidate.current_tree_hash,
         candidate.dirty, candidate.conflicted,
         candidate.stale, candidate.rebase_required
       ) IS DISTINCT FROM ROW(
         parent.candidate_tree_store, parent.candidate_tree_owner_id,
         parent.candidate_tree_ref, parent.candidate_tree_content_hash,
         parent.candidate_tree_hash,
         parent.candidate_dirty, parent.candidate_conflicted,
         parent.candidate_stale, parent.candidate_rebase_required
       ) THEN
      RAISE EXCEPTION 'Candidate abandon event cannot change runtime epoch, tree, journal, or flags'
        USING ERRCODE = '40001';
    END IF;

    SELECT * INTO abandonment
    FROM candidate_workspace_control_events AS event
    WHERE event.candidate_id = candidate.id
      AND event.candidate_version_from = parent.candidate_version
      AND event.candidate_version_to = candidate.version
      AND event.event_kind = 'candidate.abandoned'
      AND event.actor_id = NEW.actor_id
      AND event.session_epoch_from = parent.candidate_session_epoch
      AND event.session_epoch_to = candidate.session_epoch
      AND event.writer_lease_epoch_from = parent.candidate_writer_lease_epoch
      AND event.writer_lease_epoch_to = candidate.writer_lease_epoch
      AND event.target_status = 'abandoned'
      AND event.reason = NEW.reason;

    IF NOT FOUND
       OR (parent.candidate_dirty AND abandonment.candidate_snapshot_id IS NULL)
       OR (
         abandonment.candidate_snapshot_id IS NOT NULL
         AND (
           abandonment.candidate_snapshot_id IS DISTINCT FROM parent.latest_checkpoint_id
           OR NOT sandbox_checkpoint_is_exact(
             abandonment.candidate_snapshot_id, parent.candidate_id, parent.project_id,
             parent.candidate_version, parent.candidate_journal_sequence,
             parent.candidate_session_epoch, parent.candidate_writer_lease_epoch,
             parent.candidate_tree_store, parent.candidate_tree_owner_id,
             parent.candidate_tree_ref, parent.candidate_tree_content_hash,
             parent.candidate_tree_hash
           )
         )
       ) THEN
      RAISE EXCEPTION 'Candidate abandon event lacks its exact control event or checkpoint'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.event_kind = 'candidate.abandon_completed' THEN
    IF parent.state <> 'terminating'
       OR NEW.state_to <> 'terminated'
       OR NEW.reason <> 'abandoned Candidate runtime terminated'
       OR ROW(
         NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
         NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
         NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
         NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
         NEW.candidate_tree_hash_to,
         NEW.candidate_dirty_to, NEW.candidate_conflicted_to,
         NEW.candidate_stale_to, NEW.candidate_rebase_required_to
       ) IS DISTINCT FROM ROW(
         parent.candidate_version, parent.candidate_journal_sequence,
         parent.candidate_session_epoch, parent.candidate_writer_lease_epoch,
         parent.candidate_tree_store, parent.candidate_tree_owner_id,
         parent.candidate_tree_ref, parent.candidate_tree_content_hash,
         parent.candidate_tree_hash,
         parent.candidate_dirty, parent.candidate_conflicted,
         parent.candidate_stale, parent.candidate_rebase_required
       ) THEN
      RAISE EXCEPTION 'Candidate abandon completion must be a terminal lifecycle-only event'
        USING ERRCODE = '55000';
    END IF;
  ELSE
    RAISE EXCEPTION 'unsupported Candidate abandon event kind'
      USING ERRCODE = '22023';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

DROP TRIGGER sandbox_session_transition_event_guard
  ON sandbox_session_transition_events;

CREATE TRIGGER sandbox_session_transition_event_guard
BEFORE INSERT ON sandbox_session_transition_events
FOR EACH ROW
WHEN (NEW.event_kind NOT IN ('candidate.abandoned', 'candidate.abandon_completed'))
EXECUTE FUNCTION validate_sandbox_session_transition_event();

CREATE TRIGGER sandbox_session_candidate_abandon_event_guard
BEFORE INSERT ON sandbox_session_transition_events
FOR EACH ROW
WHEN (NEW.event_kind IN ('candidate.abandoned', 'candidate.abandon_completed'))
EXECUTE FUNCTION validate_sandbox_session_abandon_event();

CREATE OR REPLACE FUNCTION abandon_sandbox_session_candidate(
  target_session_id uuid,
  target_candidate_id uuid,
  expected_session_version bigint,
  expected_session_epoch bigint,
  expected_candidate_version bigint,
  expected_writer_lease_epoch bigint,
  target_actor_id uuid,
  exact_candidate_snapshot_id uuid,
  abandonment_reason text,
  target_project_id uuid
)
RETURNS TABLE (
  session_version bigint,
  session_state text,
  session_epoch bigint,
  candidate_version bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  parent sandbox_sessions%ROWTYPE;
  candidate candidate_workspaces%ROWTYPE;
BEGIN
  SELECT * INTO parent
  FROM sandbox_sessions
  WHERE id = target_session_id
    AND project_id = target_project_id
  FOR UPDATE;

  IF NOT FOUND
     OR parent.candidate_id <> target_candidate_id
     OR parent.state <> 'ready'
     OR parent.version <> expected_session_version
     OR parent.session_epoch <> expected_session_epoch
     OR parent.candidate_version <> expected_candidate_version
     OR parent.candidate_writer_lease_epoch <> expected_writer_lease_epoch THEN
    RAISE EXCEPTION 'Candidate abandon failed SandboxSession state, scope, or CAS precondition'
      USING ERRCODE = '40001';
  END IF;

  SELECT * INTO candidate
  FROM candidate_workspaces
  WHERE id = target_candidate_id
    AND project_id = target_project_id
  FOR UPDATE;

  IF NOT FOUND
     OR candidate.status <> 'active'
     OR candidate.version <> expected_candidate_version
     OR candidate.session_epoch <> expected_session_epoch
     OR candidate.writer_lease_epoch <> expected_writer_lease_epoch
     OR ROW(
       candidate.journal_sequence, candidate.current_tree_store,
       candidate.current_tree_owner_id, candidate.current_tree_ref,
       candidate.current_tree_content_hash, candidate.current_tree_hash,
       candidate.dirty, candidate.conflicted, candidate.stale, candidate.rebase_required
     ) IS DISTINCT FROM ROW(
       parent.candidate_journal_sequence, parent.candidate_tree_store,
       parent.candidate_tree_owner_id, parent.candidate_tree_ref,
       parent.candidate_tree_content_hash, parent.candidate_tree_hash,
       parent.candidate_dirty, parent.candidate_conflicted,
       parent.candidate_stale, parent.candidate_rebase_required
     ) THEN
    RAISE EXCEPTION 'Candidate abandon failed exact Candidate projection precondition'
      USING ERRCODE = '40001';
  END IF;

  IF candidate.dirty AND exact_candidate_snapshot_id IS NULL THEN
    RAISE EXCEPTION 'Dirty Candidate abandonment requires an exact current CandidateSnapshot'
      USING ERRCODE = '23514';
  END IF;
  IF exact_candidate_snapshot_id IS NOT NULL AND (
    exact_candidate_snapshot_id IS DISTINCT FROM parent.latest_checkpoint_id
    OR NOT sandbox_checkpoint_is_exact(
      exact_candidate_snapshot_id, parent.candidate_id, parent.project_id,
      parent.candidate_version, parent.candidate_journal_sequence,
      parent.candidate_session_epoch, parent.candidate_writer_lease_epoch,
      parent.candidate_tree_store, parent.candidate_tree_owner_id,
      parent.candidate_tree_ref, parent.candidate_tree_content_hash,
      parent.candidate_tree_hash
    )
  ) THEN
    RAISE EXCEPTION 'Candidate abandonment checkpoint is not the attached exact current snapshot'
      USING ERRCODE = '40001';
  END IF;

  PERFORM abandon_candidate_workspace(
    target_candidate_id, expected_candidate_version, expected_session_epoch,
    expected_writer_lease_epoch, target_actor_id,
    exact_candidate_snapshot_id, abandonment_reason
  );

  INSERT INTO sandbox_session_transition_events (
    session_id, sequence, session_version_from, session_version_to,
    state_from, state_to, session_epoch_from, session_epoch_to,
    event_kind, actor_id, reason,
    candidate_version_from, candidate_version_to,
    candidate_journal_sequence_from, candidate_journal_sequence_to,
    candidate_session_epoch_from, candidate_session_epoch_to,
    candidate_writer_lease_epoch_from, candidate_writer_lease_epoch_to,
    candidate_tree_store_from, candidate_tree_store_to,
    candidate_tree_owner_id_from, candidate_tree_owner_id_to,
    candidate_tree_ref_from, candidate_tree_ref_to,
    candidate_tree_content_hash_from, candidate_tree_content_hash_to,
    candidate_tree_hash_from, candidate_tree_hash_to,
    candidate_dirty_from, candidate_dirty_to,
    candidate_conflicted_from, candidate_conflicted_to,
    candidate_stale_from, candidate_stale_to,
    candidate_rebase_required_from, candidate_rebase_required_to,
    latest_checkpoint_id_from, latest_checkpoint_id_to,
    failure_reason_from, failure_reason_to
  )
  SELECT parent.id, parent.version, parent.version, parent.version + 1,
         parent.state, 'terminating', parent.session_epoch, parent.session_epoch,
         'candidate.abandoned', target_actor_id, abandonment_reason,
         parent.candidate_version, current.version,
         parent.candidate_journal_sequence, current.journal_sequence,
         parent.candidate_session_epoch, current.session_epoch,
         parent.candidate_writer_lease_epoch, current.writer_lease_epoch,
         parent.candidate_tree_store, current.current_tree_store,
         parent.candidate_tree_owner_id, current.current_tree_owner_id,
         parent.candidate_tree_ref, current.current_tree_ref,
         parent.candidate_tree_content_hash, current.current_tree_content_hash,
         parent.candidate_tree_hash, current.current_tree_hash,
         parent.candidate_dirty, current.dirty,
         parent.candidate_conflicted, current.conflicted,
         parent.candidate_stale, current.stale,
         parent.candidate_rebase_required, current.rebase_required,
         parent.latest_checkpoint_id, parent.latest_checkpoint_id,
         parent.failure_reason, parent.failure_reason
  FROM candidate_workspaces AS current
  WHERE current.id = target_candidate_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate abandon failed to append its SandboxSession fence'
      USING ERRCODE = '40001';
  END IF;

  RETURN QUERY
  SELECT session.version, session.state, session.session_epoch, session.candidate_version
  FROM sandbox_sessions AS session
  WHERE session.id = target_session_id;
END;
$$;

CREATE OR REPLACE FUNCTION complete_abandoned_sandbox_session(
  target_session_id uuid,
  expected_session_version bigint,
  expected_session_epoch bigint,
  target_actor_id uuid
)
RETURNS TABLE (
  session_version bigint,
  session_state text,
  session_epoch bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  INSERT INTO sandbox_session_transition_events (
    session_id, sequence, session_version_from, session_version_to,
    state_from, state_to, session_epoch_from, session_epoch_to,
    event_kind, actor_id, reason,
    candidate_version_from, candidate_version_to,
    candidate_journal_sequence_from, candidate_journal_sequence_to,
    candidate_session_epoch_from, candidate_session_epoch_to,
    candidate_writer_lease_epoch_from, candidate_writer_lease_epoch_to,
    candidate_tree_store_from, candidate_tree_store_to,
    candidate_tree_owner_id_from, candidate_tree_owner_id_to,
    candidate_tree_ref_from, candidate_tree_ref_to,
    candidate_tree_content_hash_from, candidate_tree_content_hash_to,
    candidate_tree_hash_from, candidate_tree_hash_to,
    candidate_dirty_from, candidate_dirty_to,
    candidate_conflicted_from, candidate_conflicted_to,
    candidate_stale_from, candidate_stale_to,
    candidate_rebase_required_from, candidate_rebase_required_to,
    latest_checkpoint_id_from, latest_checkpoint_id_to,
    failure_reason_from, failure_reason_to
  )
  SELECT session.id, session.version, session.version, session.version + 1,
         session.state, 'terminated', session.session_epoch, session.session_epoch,
         'candidate.abandon_completed', target_actor_id,
         'abandoned Candidate runtime terminated',
         session.candidate_version, session.candidate_version,
         session.candidate_journal_sequence, session.candidate_journal_sequence,
         session.candidate_session_epoch, session.candidate_session_epoch,
         session.candidate_writer_lease_epoch, session.candidate_writer_lease_epoch,
         session.candidate_tree_store, session.candidate_tree_store,
         session.candidate_tree_owner_id, session.candidate_tree_owner_id,
         session.candidate_tree_ref, session.candidate_tree_ref,
         session.candidate_tree_content_hash, session.candidate_tree_content_hash,
         session.candidate_tree_hash, session.candidate_tree_hash,
         session.candidate_dirty, session.candidate_dirty,
         session.candidate_conflicted, session.candidate_conflicted,
         session.candidate_stale, session.candidate_stale,
         session.candidate_rebase_required, session.candidate_rebase_required,
         session.latest_checkpoint_id, session.latest_checkpoint_id,
         session.failure_reason, session.failure_reason
  FROM sandbox_sessions AS session
  JOIN candidate_workspaces AS candidate ON candidate.id = session.candidate_id
  WHERE session.id = target_session_id
    AND session.version = expected_session_version
    AND session.session_epoch = expected_session_epoch
    AND session.state = 'terminating'
    AND candidate.status = 'abandoned'
    AND candidate.version = session.candidate_version
    AND candidate.session_epoch = session.candidate_session_epoch
    AND candidate.writer_lease_epoch = session.candidate_writer_lease_epoch
    AND candidate.current_tree_hash = session.candidate_tree_hash;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate abandon completion failed terminal CAS precondition'
      USING ERRCODE = '40001';
  END IF;

  RETURN QUERY
  SELECT session.version, session.state, session.session_epoch
  FROM sandbox_sessions AS session
  WHERE session.id = target_session_id;
END;
$$;
