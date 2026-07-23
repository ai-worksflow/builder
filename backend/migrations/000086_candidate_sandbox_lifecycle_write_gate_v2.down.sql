-- Restore the original conservative gate.

CREATE OR REPLACE FUNCTION gate_candidate_journal_by_sandbox_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  linked_session record;
BEGIN
  FOR linked_session IN
    SELECT session.id, session.state
    FROM sandbox_sessions AS session
    WHERE session.candidate_id = NEW.candidate_id
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
