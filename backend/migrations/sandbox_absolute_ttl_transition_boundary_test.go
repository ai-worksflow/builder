package migrations

import (
	"strings"
	"testing"
)

func TestSandboxAbsoluteTTLTransitionBoundaryIsOperatorOnlyAndCandidatePreserving(t *testing.T) {
	up, err := files.ReadFile("000088_sandbox_absolute_ttl_transition_boundary.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000088_sandbox_absolute_ttl_transition_boundary.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := string(up)
	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION transition_expired_sandbox_session(",
		"00000000-0000-4000-8000-000000000047",
		"parent.expires_at > statement_timestamp()",
		"parent.state IN ('ready', 'suspended', 'failed')",
		"parent.state = 'terminating' AND target_state = 'terminated'",
		"parent.candidate_version, parent.candidate_version",
		"NOT absolute_ttl_cleanup AND (ROW(",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Fatalf("absolute TTL transition migration is missing %q", fragment)
		}
	}
	if !strings.Contains(string(down), "DROP FUNCTION IF EXISTS transition_expired_sandbox_session") ||
		strings.Contains(string(down), "absolute_ttl_cleanup") {
		t.Fatal("absolute TTL transition down migration does not restore the prior validator")
	}
}

func TestSandboxAbsoluteTTLCheckpointGuardDoesNotBlockResourceCleanup(t *testing.T) {
	up, err := files.ReadFile("000089_sandbox_absolute_ttl_checkpoint_guard.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000089_sandbox_absolute_ttl_checkpoint_guard.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), "AND NOT absolute_ttl_cleanup\n     AND candidate.dirty") {
		t.Fatal("absolute TTL cleanup is still blocked by the user checkpoint guard")
	}
	if strings.Contains(string(down), "AND NOT absolute_ttl_cleanup\n     AND candidate.dirty") {
		t.Fatal("checkpoint guard down migration does not restore the version 88 validator")
	}
}
