package migrations

import (
	"strings"
	"testing"
)

func TestCandidateAbandonSandboxReconciliationMigrationContract(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000050_candidate_abandon_sandbox_reconciliation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000050_candidate_abandon_sandbox_reconciliation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := string(up)
	for _, fragment := range []string{
		"'candidate.abandoned', 'candidate.abandon_completed'",
		"action IN ('suspend', 'terminate', 'abandon_cleanup')",
		"validate_sandbox_session_abandon_event",
		"candidate.status <> 'abandoned'",
		"candidate_workspace_control_events",
		"sandbox_checkpoint_is_exact",
		"abandon_sandbox_session_candidate",
		"PERFORM abandon_candidate_workspace",
		"complete_abandoned_sandbox_session",
		"'abandoned Candidate runtime terminated'",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Fatalf("Candidate abandon reconciliation migration is missing %q", fragment)
		}
	}
	if strings.Index(upSQL, "PERFORM abandon_candidate_workspace") >
		strings.Index(upSQL, "'candidate.abandoned', target_actor_id") {
		t.Fatal("SandboxSession abandon event is appended before the exact Candidate terminal transition")
	}
	downSQL := string(down)
	for _, fragment := range []string{
		"DROP FUNCTION IF EXISTS complete_abandoned_sandbox_session",
		"DROP FUNCTION IF EXISTS abandon_sandbox_session_candidate",
		"DROP FUNCTION IF EXISTS validate_sandbox_session_abandon_event",
		"action IN ('suspend', 'terminate')",
	} {
		if !strings.Contains(downSQL, fragment) {
			t.Fatalf("Candidate abandon reconciliation rollback is missing %q", fragment)
		}
	}
}
