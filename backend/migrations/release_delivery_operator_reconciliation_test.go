package migrations

import (
	"strings"
	"testing"
)

func TestReleaseDeliveryOperatorReconciliationMigrationIsFailClosed(t *testing.T) {
	up, err := files.ReadFile("000058_release_delivery_operator_reconciliation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000058_release_delivery_operator_reconciliation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(up)
	for _, required := range []string{
		"CREATE TABLE release_delivery_reconciliation_cases",
		"UNIQUE (project_id, idempotency_key)",
		"UNIQUE (operation_id, expected_run_version)",
		"member.role IN ('owner','admin')",
		"resolution.created_txid = txid_current()",
		"delivery reconciliation Case and exact GET-only resume must commit atomically",
		"run_state <> 'reconcile_blocked'",
		"operation.remote_state <> 'quarantined'",
		"attempt.outcome <> 'quarantined'",
		"COALESCE(derived_resume, 'submit_unknown')",
		"CREATE OR REPLACE FUNCTION validate_release_delivery_operation_mutation()",
		"OLD.remote_state = 'quarantined'",
		"CREATE OR REPLACE FUNCTION validate_release_delivery_run_update()",
		"OLD.state = 'reconcile_blocked' AND NEW.state = 'reconcile_wait'",
		"governed delivery reconciliation is GET-only; resubmission is forbidden",
		"release delivery reconciliation Cases are immutable append-only evidence",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("operator reconciliation migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"force-head", "force_head", "UPDATE release_production_heads",
		"DELETE FROM release_delivery_reconciliation_cases",
	} {
		if strings.Contains(strings.ToLower(sql), strings.ToLower(forbidden)) {
			t.Fatalf("operator reconciliation migration contains forbidden shortcut %q", forbidden)
		}
	}
	if !strings.Contains(string(down), "cannot downgrade governed release delivery reconciliation") {
		t.Fatal("operator reconciliation downgrade does not fail closed")
	}
}
