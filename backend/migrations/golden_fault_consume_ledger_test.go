package migrations

import (
	"strings"
	"testing"
)

func TestGoldenFaultConsumeLedgerMigrationIsAppendOnlyAndClosed(t *testing.T) {
	up, err := files.ReadFile("000068_golden_fault_consume_ledger.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000068_golden_fault_consume_ledger.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	upText := string(up)
	for _, expected := range []string{
		"CREATE TABLE golden_fault_consume_reservations",
		"CREATE TABLE golden_fault_consume_results",
		"golden_fault_operation_selector_exact",
		"golden_fault_fixture_operation_unique",
		"adapter_invocation_id uuid NOT NULL UNIQUE",
		"ON DELETE RESTRICT",
		"validate_golden_fault_consume_result",
		"reject_golden_fault_ledger_mutation",
		"SECURITY INVOKER",
		"BEFORE UPDATE OR DELETE",
		"worksflow-golden-fault-consume-receipt/v1",
		"GRANT USAGE ON SCHEMA %I TO worksflow_golden_fault_operator",
		"GRANT SELECT, INSERT ON TABLE",
		"worksflow_golden_fault_operator",
		"REVOKE ALL ON TABLE %I.golden_fault_consume_reservations FROM worksflow_application",
		"REVOKE ALL ON TABLE %I.golden_fault_consume_results FROM worksflow_application",
	} {
		if !strings.Contains(upText, expected) {
			t.Fatalf("Golden fault ledger migration is missing %q", expected)
		}
	}
	for _, operation := range []string{
		"agent-runner-crash", "agent-runner-timeout", "agent-security-canary",
		"controller-conflict", "controller-maintenance", "controller-not-found", "controller-timeout",
		"lsp-resource-pressure", "lsp-runtime-crash", "lsp-runtime-drift",
		"reference-gateway-outage", "reference-process-restart", "sandbox-dependency-crash",
	} {
		if count := strings.Count(upText, "'"+operation+"'"); count != 2 {
			t.Fatalf("operation %q occurrence count = %d, want enum + exact selector", operation, count)
		}
	}
	for _, forbidden := range []string{
		"GRANT UPDATE", "GRANT DELETE", "ON DELETE CASCADE", "SECURITY DEFINER",
	} {
		if strings.Contains(upText, forbidden) {
			t.Fatalf("append-only Golden fault ledger contains forbidden SQL %q", forbidden)
		}
	}
	downText := string(down)
	for _, expected := range []string{
		"DROP TABLE IF EXISTS golden_fault_consume_results",
		"DROP TABLE IF EXISTS golden_fault_consume_reservations",
		"DROP FUNCTION IF EXISTS validate_golden_fault_consume_result",
		"DROP FUNCTION IF EXISTS reject_golden_fault_ledger_mutation",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("Golden fault ledger rollback is missing %q", expected)
		}
	}
}
