package migrations

import (
	"strings"
	"testing"
)

func TestReleaseDeliveryOperationReconciliationDeclaresExactFailClosedAuthority(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000056_release_delivery_operation_reconciliation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000056_release_delivery_operation_reconciliation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"CREATE TABLE release_delivery_operations",
		"CREATE TABLE release_delivery_operation_attempts",
		"CREATE TABLE release_delivery_operation_results",
		"release-preview-run/v2",
		"release-deployment-run/v2",
		"release_delivery_canonical_json",
		"octet_length(request_document) BETWEEN 2 AND 16777216",
		"release-preview-receipt/v2",
		"release-production-receipt/v2",
		"release-deployment-revision/v2",
		"controller_operation_id",
		"controller_result_hash",
		"kind text NOT NULL CHECK (kind IN ('submit','reconcile','resubmit'))",
		"SET state = 'reconcile_blocked'",
		"state IN ('queued','claimed','deploying','verifying')",
		"request_hash text NOT NULL",
		"controller_trust_key_digest",
		"release_delivery_operation_result_request_exact_fk",
		"release_delivery_operation_terminal_result_exact_fk",
		"operation Result requires the exact request, controller, completed Attempt, and live fence",
		"FROM release_deployment_runs AS run WHERE run.id = operation.deployment_run_id",
		"expired submission must be fenced into reconciliation",
		"active-to-error requires the exact immutable rejected controller Result",
		"state IN (\n    'queued','claimed','submitting','reconcile_wait','reconciling','verifying','reconcile_blocked'",
		"release delivery operation Attempts are append-only evidence",
		"release delivery operation Results are immutable exact authority",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("release delivery reconciliation migration is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"OLD.state IN ('claimed','submitting','reconciling','verifying') AND NEW.state = 'error'",
		"WHERE state IN ('queued','claimed','submitting','reconciling','verifying')",
		"ON DELETE CASCADE",
		"operation.request_hash <> NEW.request_hash",
		"next_head_id",
		"next_head_hash",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("release delivery reconciliation migration trusts unsafe shortcut %q", forbidden)
		}
	}

	downText := string(down)
	for _, expected := range []string{
		"cannot downgrade release delivery reconciliation while v2, active, blocked, or operation authority exists",
		"schema_version = 'release-preview-run/v2'",
		"schema_version = 'release-deployment-run/v2'",
		"SELECT 1 FROM release_delivery_operations",
		"DROP TABLE release_delivery_operation_results",
		"CREATE OR REPLACE FUNCTION validate_release_delivery_run_update()",
	} {
		if !strings.Contains(downText, expected) {
			t.Fatalf("release delivery reconciliation rollback is missing %q", expected)
		}
	}
	if strings.Contains(downText, "DELETE FROM release_") {
		t.Fatal("release delivery reconciliation rollback silently deletes authority")
	}
}
