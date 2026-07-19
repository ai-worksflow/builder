package migrations

import (
	"strings"
	"testing"
)

func TestCandidateSandboxLifecycleWriteGateSerializesAtCommitBoundary(t *testing.T) {
	up, err := files.ReadFile("000049_candidate_sandbox_lifecycle_write_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000049_candidate_sandbox_lifecycle_write_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, fragment := range []string{
		"candidate_workspace_00_sandbox_state_gate",
		"BEFORE INSERT ON candidate_workspace_journal",
		"session.state NOT IN ('failed', 'terminated')",
		"FOR SHARE",
		"linked_session.state <> 'ready'",
		"ERRCODE = '40001'",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("lifecycle write gate migration is missing %q", fragment)
		}
	}
	if strings.Index(text, "FOR SHARE") > strings.Index(text, "linked_session.state <> 'ready'") {
		t.Fatal("lifecycle write gate checks state before acquiring the Session row lock")
	}
	for _, fragment := range []string{
		"DROP TRIGGER IF EXISTS candidate_workspace_00_sandbox_state_gate",
		"DROP FUNCTION IF EXISTS gate_candidate_journal_by_sandbox_state()",
	} {
		if !strings.Contains(string(down), fragment) {
			t.Fatalf("lifecycle write gate rollback is missing %q", fragment)
		}
	}
}
