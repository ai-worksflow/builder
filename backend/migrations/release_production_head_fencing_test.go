package migrations

import (
	"strings"
	"testing"
)

func TestReleaseProductionHeadFencingDeclaresSerialCAS(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000048_release_production_head_fencing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000048_release_production_head_fencing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE release_production_heads",
		"expected_revision_id",
		"expected_production_receipt_id",
		"release_deployment_runs_one_nonterminal_environment_idx",
		"FOR UPDATE",
		"expected production head is stale",
		"release production head CAS requires the exact verifying Run and immutable passed facts",
		"healthy release deployment Run requires its committed production head CAS",
		"FOREIGN KEY (deployment_revision_id, deployment_revision_hash)",
		"FOREIGN KEY (production_receipt_id, production_receipt_hash)",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("production head fencing migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TABLE IF EXISTS release_production_heads",
		"DROP COLUMN IF EXISTS expected_revision_id",
		"DROP COLUMN IF EXISTS expected_production_receipt_id",
		"DROP COLUMN IF EXISTS environment",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("production head fencing rollback is missing %q", expected)
		}
	}
}
