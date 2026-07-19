package migrations

import (
	"strings"
	"testing"
)

func TestCandidateFreezeVerificationGateDeclaresExactReceiptAuthority(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile("000041_candidate_freeze_verification_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000041_candidate_freeze_verification_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, expected := range []string{
		"candidate_verification_binding_version",
		"candidate_verification_receipt_id",
		"candidate_verification_receipt_hash",
		"candidate_implementation_freezes_verification_exact_fk",
		"REFERENCES candidate_verification_receipts(id, payload_hash)",
		"verification_receipt.decision = 'passed'",
		"verification_run.state = 'passed'",
		"profile_policy.state = 'active'",
		"verification_receipt.candidate_snapshot_id = NEW.candidate_snapshot_id",
		"verification_receipt.tree_hash = NEW.candidate_tree_hash",
		"verification_plan.session_version = NEW.session_version",
		"proposal.candidate_verification_receipt_id = NEW.verification_receipt_id",
		"implementation proposal Candidate source and VerificationReceipt identity are immutable",
		"NOT VALID",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Candidate freeze verification migration is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"DROP TRIGGER IF EXISTS candidate_implementation_freeze_verification_guard",
		"DROP FUNCTION IF EXISTS validate_candidate_implementation_freeze_verification",
		"DROP COLUMN IF EXISTS verification_receipt_id",
		"DROP COLUMN IF EXISTS candidate_verification_receipt_id",
	} {
		if !strings.Contains(string(down), expected) {
			t.Fatalf("Candidate freeze verification rollback is missing %q", expected)
		}
	}
}
