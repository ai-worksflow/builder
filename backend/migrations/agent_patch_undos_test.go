package migrations

import (
	"strings"
	"testing"
)

func TestAgentPatchUndoMigrationBindsAppliedMergeAndRestoreJournal(t *testing.T) {
	up, err := files.ReadFile("000035_agent_patch_undos.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000035_agent_patch_undos.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(up)
	for _, required := range []string{
		"CREATE TABLE agent_patch_undo_plans",
		"CREATE TABLE agent_patch_undo_applications",
		"merge_application.content_hash <> NEW.merge_application_content_hash",
		"journal.attribution <> 'restore'",
		"agent_patch_undo_plan_immutable",
		"agent_patch_undo_application_immutable",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Agent patch undo migration is missing %q", required)
		}
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS agent_patch_undo_plans") {
		t.Fatal("Agent patch undo rollback does not remove the immutable plan table")
	}
}
