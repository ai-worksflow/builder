package migrations

import (
	"strings"
	"testing"
)

func TestSandboxAbsoluteTTLReconciliationCapsActivityAndWakesTermination(t *testing.T) {
	up, err := files.ReadFile("000087_sandbox_absolute_ttl_reconciliation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000087_sandbox_absolute_ttl_reconciliation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := string(up)
	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION sync_sandbox_session_activity_from_projection()",
		"LEAST(NEW.updated_at, NEW.idle_deadline)",
		"GREATEST(",
		"EXCLUDED.idle_deadline",
		"UPDATE sandbox_lifecycle_deadline_leases",
		"WHERE action = 'terminate'",
		"AND last_error IS NOT NULL",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Fatalf("absolute TTL reconciliation migration is missing %q", fragment)
		}
	}
	if !strings.Contains(string(down), "last_activity_at = GREATEST(") {
		t.Fatal("absolute TTL reconciliation down migration does not restore the prior trigger")
	}
}
