package migrations

import (
	"strings"
	"testing"
)

func TestCandidateSandboxLifecycleWriteGateBindsExactReadyProjection(t *testing.T) {
	up, err := files.ReadFile("000086_candidate_sandbox_lifecycle_write_gate_v2.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, fragment := range []string{
		"session.actor_id = NEW.actor_id",
		"session.session_epoch = NEW.session_epoch",
		"session.candidate_version = NEW.candidate_version_from",
		"session.candidate_journal_sequence = NEW.sequence - 1",
		"session.candidate_writer_lease_epoch = NEW.writer_lease_epoch",
		"session.candidate_tree_hash = NEW.before_tree_hash",
		"linked_session.state = 'ready'",
		"IF NOT exact_ready_session_found",
		"FOR SHARE",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("exact lifecycle write gate migration is missing %q", fragment)
		}
	}
	if strings.Contains(text, "linked_session.state <> 'ready'") {
		t.Fatal("exact lifecycle write gate still lets stale historical Sessions fence the Candidate")
	}
}
