DROP TRIGGER IF EXISTS candidate_workspace_00_sandbox_state_gate
  ON candidate_workspace_journal;
DROP FUNCTION IF EXISTS gate_candidate_journal_by_sandbox_state();

