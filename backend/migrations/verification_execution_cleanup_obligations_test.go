package migrations

import (
	"strings"
	"testing"
)

func TestVerificationExecutionCleanupObligationsMigrationDeclaresDurableFencedAuthority(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000052_verification_execution_cleanup_obligations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000052_verification_execution_cleanup_obligations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"CREATE TABLE verification_execution_cleanups",
		"PRIMARY KEY (scope, attempt_id, attempt_fence_epoch)",
		"state IN ('registered', 'pending', 'cleaning', 'completed')",
		"NEW.lease_epoch < OLD.lease_epoch",
		"cleanup.state IS DISTINCT FROM 'completed'",
		"Candidate Receipt requires completed cleanup for every exact Attempt fence",
		"Canonical Receipt requires completed cleanup for every exact Attempt fence",
		"guard_canonical_verification_run_transition_v2",
		"guard_canonical_verification_attempt_transition_v2",
		"cleanup.state = 'completed'",
		"policy.state = 'active'",
		"CREATE CONSTRAINT TRIGGER candidate_verification_run_claim_cleanup_guard",
		"CREATE CONSTRAINT TRIGGER candidate_verification_attempt_claim_cleanup_guard",
		"CREATE CONSTRAINT TRIGGER canonical_verification_run_claim_cleanup_guard",
		"CREATE CONSTRAINT TRIGGER canonical_verification_attempt_claim_cleanup_guard",
		"DEFERRABLE INITIALLY DEFERRED",
		"exact-fence cleanup registration in the same transaction",
		"verification profile policy is inactive before Canonical execution claim",
		"Queued Canonical policy reconciliation cannot claim or invent runtime resources",
	} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("verification cleanup migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"THEN 'completed'",
		"policy.state = 'active'\n+      WHERE cleanup.scope",
	} {
		if strings.Contains(string(up), forbidden) {
			t.Fatalf("verification cleanup migration contains unsafe backfill/claim authority %q", forbidden)
		}
	}
	for _, required := range []string{
		"DROP TRIGGER canonical_verification_receipt_cleanup_guard",
		"DROP TRIGGER canonical_verification_attempt_claim_cleanup_guard",
		"DROP TRIGGER canonical_verification_run_claim_cleanup_guard",
		"DROP TRIGGER candidate_verification_attempt_claim_cleanup_guard",
		"DROP TRIGGER candidate_verification_run_claim_cleanup_guard",
		"guard_canonical_verification_attempt_transition()",
		"guard_canonical_verification_run_transition()",
		"DROP TABLE verification_execution_cleanups",
	} {
		if !strings.Contains(string(down), required) {
			t.Fatalf("verification cleanup rollback is missing %q", required)
		}
	}
}
