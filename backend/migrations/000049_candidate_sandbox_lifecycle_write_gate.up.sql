-- Candidate writes and SandboxSession lifecycle decisions must serialize at
-- the database boundary. HTTP authorization alone cannot fence a mutation
-- that was already in flight when another replica begins suspend/terminate.

CREATE OR REPLACE FUNCTION gate_candidate_journal_by_sandbox_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  linked_session record;
BEGIN
  -- Trigger names are ordered, and this gate is deliberately installed as
  -- candidate_workspace_00_* so it locks every nonterminal Session row before
  -- the existing journal guard locks the Candidate row. Lifecycle transitions
  -- use the same Session -> Candidate order.
  FOR linked_session IN
    SELECT session.id, session.state
    FROM sandbox_sessions AS session
    WHERE session.candidate_id = NEW.candidate_id
      -- Failed and terminated Sessions are immutable audit history. They no
      -- longer own a runtime or writer path, and the product explicitly
      -- permits a fresh Session for the same recoverable Candidate.
      AND session.state NOT IN ('failed', 'terminated')
    ORDER BY session.id
    FOR SHARE
  LOOP
    IF linked_session.state <> 'ready' THEN
      RAISE EXCEPTION 'Candidate journal append is fenced by SandboxSession lifecycle state %',
        linked_session.state
        USING ERRCODE = '40001';
    END IF;
  END LOOP;

  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_workspace_00_sandbox_state_gate
BEFORE INSERT ON candidate_workspace_journal
FOR EACH ROW EXECUTE FUNCTION gate_candidate_journal_by_sandbox_state();
