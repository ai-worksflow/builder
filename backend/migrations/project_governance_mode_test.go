package migrations

import (
	"strings"
	"testing"
)

func TestProjectGovernanceModeMigrationPinsProjectRunAndReviewEvidence(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000019_project_governance_mode.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000019_project_governance_mode.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(up))
	for _, required := range []string{
		"projects_governance_mode_check", "governance_mode text not null default 'team'",
		"review_decisions", "solo_self_review boolean not null default false",
		"workflow_runs_governance_mode_check", "workflow_run_governance_mode_immutable",
		"review_request_policy_immutable",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("project governance migration is missing %q", required)
		}
	}
	for _, required := range []string{"drop column solo_self_review", "drop column governance_mode"} {
		if !strings.Contains(strings.ToLower(string(down)), required) {
			t.Fatalf("project governance rollback is missing %q", required)
		}
	}
}
