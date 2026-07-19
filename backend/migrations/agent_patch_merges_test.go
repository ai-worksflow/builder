package migrations

import (
	"strings"
	"testing"
)

func TestAgentPatchMergeMigrationKeepsPlansAndApplicationsImmutable(t *testing.T) {
	up, err := files.ReadFile("000034_agent_patch_merges.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000034_agent_patch_merges.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(up)
	for _, required := range []string{
		"CREATE TABLE agent_patch_merge_plans",
		"CREATE TABLE agent_patch_merge_applications",
		"attempt.state <> 'review_ready'",
		"attempt.evidence->'patch' IS DISTINCT FROM NEW.patch_reference",
		"candidate.writer_lease_owner_id IS DISTINCT FROM NEW.created_by",
		"journal.attribution <> 'agent'",
		"agent_patch_merge_plan_immutable",
		"agent_patch_merge_application_immutable",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Agent patch merge migration is missing %q", required)
		}
	}
	if strings.Count(sql, "BEFORE UPDATE OR DELETE") != 2 {
		t.Fatal("both Agent patch merge tables must reject update and delete")
	}
	rollback := string(down)
	for _, required := range []string{
		"DROP TABLE IF EXISTS agent_patch_merge_applications",
		"DROP TABLE IF EXISTS agent_patch_merge_plans",
		"DROP FUNCTION IF EXISTS agent_patch_merge_operations_are_valid(jsonb)",
	} {
		if !strings.Contains(rollback, required) {
			t.Fatalf("Agent patch merge rollback is missing %q", required)
		}
	}
}
