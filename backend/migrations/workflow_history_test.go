package migrations

import (
	"strings"
	"testing"
)

func TestWorkflowHistoryMigrationSupportsCursorOrdering(t *testing.T) {
	t.Parallel()
	contents, err := files.ReadFile("000004_workflow_run_history.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"(project_id, created_at desc, id desc)",
		"(project_id, status, created_at desc, id desc)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("workflow history migration is missing %q", required)
		}
	}
}
