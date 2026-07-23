-- A stale suspended SandboxSession must remain immutable audit history, but it
-- must not fence a newer ready Session for the same recoverable Candidate.
-- Bind the write gate to the exact Session projection carried by the journal
-- append instead of inspecting every historical nonterminal Session.

CREATE OR REPLACE FUNCTION gate_candidate_journal_by_sandbox_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  linked_session record;
  exact_ready_session_found boolean := false;
BEGIN
  FOR linked_session IN
    SELECT session.id, session.state
    FROM sandbox_sessions AS session
    WHERE session.candidate_id = NEW.candidate_id
      AND session.actor_id = NEW.actor_id
      AND session.session_epoch = NEW.session_epoch
      AND session.candidate_version = NEW.candidate_version_from
      AND session.candidate_journal_sequence = NEW.sequence - 1
      AND session.candidate_writer_lease_epoch = NEW.writer_lease_epoch
      AND session.candidate_tree_hash = NEW.before_tree_hash
      AND session.state NOT IN ('failed', 'terminated')
    ORDER BY session.id
    FOR SHARE
  LOOP
    IF linked_session.state = 'ready' THEN
      exact_ready_session_found := true;
    END IF;
  END LOOP;

  IF NOT exact_ready_session_found THEN
    RAISE EXCEPTION 'Candidate journal append requires an exact ready SandboxSession projection'
      USING ERRCODE = '40001';
  END IF;

  RETURN NEW;
END;
$$;
