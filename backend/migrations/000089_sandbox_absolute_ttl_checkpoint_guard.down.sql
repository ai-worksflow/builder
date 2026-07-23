CREATE OR REPLACE FUNCTION validate_sandbox_session_transition_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_sessions%ROWTYPE;
  candidate candidate_workspaces%ROWTYPE;
  lifecycle_valid boolean := false;
  candidate_unchanged boolean;
  candidate_freeze_committed boolean := false;
  absolute_ttl_cleanup boolean := false;
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

  absolute_ttl_cleanup := NEW.actor_id = '00000000-0000-4000-8000-000000000047'::uuid
    AND NEW.event_kind IN ('lifecycle.terminate_requested', 'lifecycle.terminated')
    AND NEW.candidate_version_to = NEW.candidate_version_from
    AND NEW.candidate_journal_sequence_to = NEW.candidate_journal_sequence_from
    AND NEW.candidate_session_epoch_to = NEW.candidate_session_epoch_from
    AND NEW.candidate_writer_lease_epoch_to = NEW.candidate_writer_lease_epoch_from;

  IF NOT FOUND OR (NOT absolute_ttl_cleanup AND candidate.status NOT IN ('active', 'frozen')) THEN
    RAISE EXCEPTION 'SandboxSession transitions require an exact active or governed-frozen Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF candidate.status = 'frozen' AND NOT absolute_ttl_cleanup THEN
    SELECT EXISTS (
      SELECT 1
      FROM candidate_implementation_freezes AS receipt
      JOIN candidate_workspace_control_events AS event
        ON event.candidate_id = receipt.candidate_id
       AND event.event_kind = 'candidate.frozen'
       AND event.candidate_snapshot_id = receipt.candidate_snapshot_id
       AND event.candidate_version_from = receipt.candidate_version
       AND event.candidate_version_to = candidate.version
      WHERE receipt.project_id = parent.project_id
        AND receipt.session_id = parent.id
        AND receipt.candidate_id = candidate.id
        AND receipt.candidate_snapshot_id = parent.latest_checkpoint_id
        AND receipt.candidate_version + 1 = candidate.version
        AND receipt.journal_sequence = candidate.journal_sequence
        AND receipt.session_epoch = candidate.session_epoch
        AND receipt.writer_lease_epoch + 1 = candidate.writer_lease_epoch
        AND receipt.candidate_tree_hash = candidate.current_tree_hash
    ) INTO candidate_freeze_committed;
    IF NOT candidate_freeze_committed THEN
      RAISE EXCEPTION 'frozen Candidate has no exact governed implementation freeze receipt'
        USING ERRCODE = '40001';
    END IF;
  END IF;

  IF NEW.sequence <> parent.version
     OR NEW.session_version_from <> parent.version
     OR NEW.session_version_to <> parent.version + 1
     OR NEW.state_from <> parent.state
     OR NEW.session_epoch_from <> parent.session_epoch
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
     ) THEN
    RAISE EXCEPTION 'SandboxSession event failed state, epoch, or CAS version precondition'
      USING ERRCODE = '40001';
  END IF;

  candidate_unchanged := ROW(
    NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
    NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
    NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
    NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
    NEW.candidate_tree_hash_to,
    NEW.candidate_dirty_to, NEW.candidate_conflicted_to,
    NEW.candidate_stale_to, NEW.candidate_rebase_required_to
  ) IS NOT DISTINCT FROM ROW(
    parent.candidate_version, parent.candidate_journal_sequence,
    parent.candidate_session_epoch, parent.candidate_writer_lease_epoch,
    parent.candidate_tree_store, parent.candidate_tree_owner_id,
    parent.candidate_tree_ref, parent.candidate_tree_content_hash,
    parent.candidate_tree_hash,
    parent.candidate_dirty, parent.candidate_conflicted,
    parent.candidate_stale, parent.candidate_rebase_required
  );

  IF NEW.event_kind = 'candidate.synced' THEN
    lifecycle_valid := parent.state IN ('ready', 'resuming')
      AND NEW.state_to = parent.state
      AND NEW.session_epoch_to = parent.session_epoch
      AND candidate.version > parent.candidate_version
      AND (candidate.status = 'active' OR candidate_freeze_committed);
  ELSIF NEW.event_kind = 'checkpoint.attached' THEN
    lifecycle_valid := candidate.status = 'active'
      AND parent.state IN ('ready', 'resuming')
      AND NEW.state_to = parent.state
      AND NEW.session_epoch_to = parent.session_epoch
      AND candidate_unchanged
      AND NEW.latest_checkpoint_id_to IS DISTINCT FROM parent.latest_checkpoint_id;
  ELSIF NEW.event_kind = 'lifecycle.started' THEN
    lifecycle_valid := candidate.status = 'active'
      AND parent.state = 'provisioning' AND NEW.state_to = 'starting';
  ELSIF NEW.event_kind = 'lifecycle.ready' THEN
    lifecycle_valid := candidate.status = 'active'
      AND parent.state IN ('starting', 'resuming') AND NEW.state_to = 'ready';
  ELSIF NEW.event_kind = 'lifecycle.suspend_requested' THEN
    lifecycle_valid := candidate.status = 'active'
      AND parent.state = 'ready' AND NEW.state_to = 'suspending';
  ELSIF NEW.event_kind = 'lifecycle.suspended' THEN
    lifecycle_valid := candidate.status = 'active'
      AND parent.state = 'suspending' AND NEW.state_to = 'suspended';
  ELSIF NEW.event_kind = 'lifecycle.resume_requested' THEN
    lifecycle_valid := candidate.status = 'active'
      AND parent.state = 'suspended'
      AND NEW.state_to = 'resuming'
      AND NEW.session_epoch_to = parent.session_epoch + 1
      AND NEW.candidate_session_epoch_to = parent.candidate_session_epoch + 1
      AND candidate.session_epoch = NEW.candidate_session_epoch_to
      AND candidate.version = parent.candidate_version + 1;
  ELSIF NEW.event_kind = 'lifecycle.terminate_requested' THEN
    lifecycle_valid := parent.state IN ('ready', 'suspended', 'failed') AND NEW.state_to = 'terminating';
  ELSIF NEW.event_kind = 'lifecycle.cancelled' THEN
    lifecycle_valid := candidate.status = 'active'
      AND parent.state IN ('provisioning', 'starting', 'resuming')
      AND NEW.state_to = 'terminating' AND NEW.reason = 'cancel';
  ELSIF NEW.event_kind = 'lifecycle.terminated' THEN
    lifecycle_valid := parent.state = 'terminating' AND NEW.state_to = 'terminated';
  ELSIF NEW.event_kind = 'lifecycle.failed' THEN
    lifecycle_valid := parent.state IN (
      'provisioning', 'starting', 'ready', 'suspending', 'resuming', 'terminating'
    ) AND NEW.state_to = 'failed';
  END IF;

  IF NOT lifecycle_valid THEN
    RAISE EXCEPTION 'invalid SandboxSession lifecycle transition'
      USING ERRCODE = '55000';
  END IF;

  IF NEW.event_kind <> 'candidate.synced'
     AND NEW.event_kind <> 'lifecycle.resume_requested'
     AND NOT candidate_unchanged THEN
    RAISE EXCEPTION 'SandboxSession lifecycle transition cannot smuggle a Candidate change'
      USING ERRCODE = '40001';
  END IF;

  IF NOT absolute_ttl_cleanup AND (ROW(
    candidate.version, candidate.journal_sequence,
    candidate.session_epoch, candidate.writer_lease_epoch,
    candidate.current_tree_store, candidate.current_tree_owner_id,
    candidate.current_tree_ref, candidate.current_tree_content_hash,
    candidate.current_tree_hash,
    candidate.dirty, candidate.conflicted, candidate.stale, candidate.rebase_required
  ) IS DISTINCT FROM ROW(
    NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
    NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
    NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
    NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
    NEW.candidate_tree_hash_to,
    NEW.candidate_dirty_to, NEW.candidate_conflicted_to,
    NEW.candidate_stale_to, NEW.candidate_rebase_required_to
  ) OR NEW.candidate_session_epoch_to <> NEW.session_epoch_to) THEN
    RAISE EXCEPTION 'SandboxSession event does not project the exact live Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.latest_checkpoint_id_to IS DISTINCT FROM parent.latest_checkpoint_id
     AND NOT sandbox_checkpoint_is_exact(
       NEW.latest_checkpoint_id_to, parent.candidate_id, parent.project_id,
       NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
       NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
       NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
       NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
       NEW.candidate_tree_hash_to
     ) THEN
    RAISE EXCEPTION 'SandboxSession event checkpoint is not exact for the current Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.event_kind IN (
       'lifecycle.suspend_requested',
       'lifecycle.terminate_requested',
       'lifecycle.cancelled'
     )
     AND candidate.dirty
     AND NOT candidate_freeze_committed
     AND NOT sandbox_checkpoint_is_exact(
       NEW.latest_checkpoint_id_to, parent.candidate_id, parent.project_id,
       NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
       NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
       NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
       NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
       NEW.candidate_tree_hash_to
     ) THEN
    RAISE EXCEPTION 'dirty Candidate requires an exact current CandidateSnapshot before suspend, terminate, or cancel'
      USING ERRCODE = '23514';
  END IF;

  IF NEW.event_kind = 'lifecycle.ready'
     AND parent.state = 'resuming'
     AND candidate.dirty
     AND NOT sandbox_checkpoint_is_exact(
       NEW.latest_checkpoint_id_to, parent.candidate_id, parent.project_id,
       NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
       NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
       NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
       NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
       NEW.candidate_tree_hash_to
     ) THEN
    RAISE EXCEPTION 'dirty resumed Candidate requires an exact current CandidateSnapshot before ready'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.event_kind = 'lifecycle.failed' THEN
    IF NEW.failure_reason_to IS DISTINCT FROM NEW.reason THEN
      RAISE EXCEPTION 'failed SandboxSession must persist its exact failure reason'
        USING ERRCODE = '23514';
    END IF;
  ELSIF NEW.failure_reason_to IS DISTINCT FROM parent.failure_reason THEN
    RAISE EXCEPTION 'SandboxSession failure reason may change only on failure'
      USING ERRCODE = '55000';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;
