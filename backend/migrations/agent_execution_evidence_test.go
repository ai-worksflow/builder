package migrations

import (
	"strings"
	"testing"
)

func TestAgentExecutionEvidenceMigrationIndexesImmutableReachability(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000033_agent_execution_evidence.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000033_agent_execution_evidence.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"candidate_workspace_journal_exact_after_tree_idx",
		"candidate_id, after_tree_hash",
		"after_tree_content_hash",
		"agent_attempts_evidence_refs_idx",
		"evidence jsonb_path_ops",
	} {
		if !strings.Contains(string(up), expected) {
			t.Fatalf("Agent execution evidence migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP INDEX IF EXISTS agent_attempts_evidence_refs_idx",
		"DROP INDEX IF EXISTS candidate_workspace_journal_exact_after_tree_idx",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Agent execution evidence rollback is missing %q", expected)
		}
	}
}
