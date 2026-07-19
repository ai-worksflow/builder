package migrations

import (
	"strings"
	"testing"
)

func TestCandidateFreezeBuildContractGuardMigration(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000038_candidate_freeze_build_contract_guard.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000038_candidate_freeze_build_contract_guard.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upText := string(up)
	for _, expected := range []string{
		"CREATE OR REPLACE FUNCTION guard_implementation_build_contract_binding()",
		"NEW.execution_source NOT IN ('manual_submission', 'candidate_freeze')",
		"exact_application_build_contract_is_ready",
		"claim.application_build_contract_id = target_contract_id",
		"claim.application_build_contract_hash = target_contract_hash",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("Candidate freeze Build Contract guard migration is missing %q", expected)
		}
	}

	downText := string(down)
	if !strings.Contains(downText, "NEW.execution_source <> 'manual_submission'") {
		t.Fatal("Candidate freeze Build Contract guard rollback does not restore the prior generation-claim rule")
	}
	if strings.Contains(downText, "NEW.execution_source NOT IN ('manual_submission', 'candidate_freeze')") {
		t.Fatal("Candidate freeze Build Contract guard rollback still exempts candidate_freeze")
	}
}
