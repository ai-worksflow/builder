package migrations

import (
	"strings"
	"testing"
)

func TestImplementationBuildContractBindingMigrationFailsClosed(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000024_implementation_build_contract_binding.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000024_implementation_build_contract_binding.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"application_build_contract_id_hash_unique",
		"implementation_generation_build_contract_exact_fk",
		"implementation_proposal_build_contract_exact_fk",
		"exact_application_build_contract_is_ready",
		"contract.status = 'ready'",
		"manifest.status = 'frozen'",
		"policy.state <> 'approved'",
		"new implementation work requires an exact Application Build Contract",
		"legacy unbound implementation work cannot advance",
		"generated implementation proposal Build Contract differs from its generation claim",
		"implementation_generation_build_contract_guard",
		"implementation_proposal_build_contract_guard",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("implementation Build Contract migration is missing %q", expected)
		}
	}
	downText := string(down)
	for _, expected := range []string{
		"DROP TRIGGER IF EXISTS implementation_proposal_build_contract_guard",
		"DROP TRIGGER IF EXISTS implementation_generation_build_contract_guard",
		"DROP FUNCTION IF EXISTS exact_application_build_contract_is_ready",
		"DROP COLUMN IF EXISTS application_build_contract_hash",
		"DROP CONSTRAINT IF EXISTS application_build_contract_id_hash_unique",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("implementation Build Contract rollback is missing %q", expected)
		}
	}
}
