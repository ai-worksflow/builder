package migrations

import (
	"strings"
	"testing"
)

func TestLegacyAIImplementationProposalGateDeclaresCandidateOnlyFailClosedAuthority(t *testing.T) {
	t.Parallel()

	up, err := files.ReadFile("000053_legacy_ai_implementation_proposal_gate.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000053_legacy_ai_implementation_proposal_gate.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	upText := string(up)
	for _, required := range []string{
		"ADD COLUMN unimplemented_count integer",
		"ADD COLUMN blocking_diagnostic_count integer",
		"WHERE execution_source = 'candidate_freeze'",
		"SET unimplemented_count = 0",
		"blocking_diagnostic_count = 0",
		"DROP CONSTRAINT implementation_proposals_candidate_verification_shape",
		"ADD CONSTRAINT implementation_proposals_candidate_verification_shape",
		"status IN ('stale', 'rejected', 'failed')",
		"DISABLE TRIGGER implementation_proposal_build_contract_guard",
		"ENABLE TRIGGER implementation_proposal_build_contract_guard",
		"'manual_generation', 'workflow_runner', 'conversation_command'",
		"TG_OP = 'INSERT'",
		"legacy AI ImplementationProposal creation is disabled",
		"new candidate_freeze ImplementationProposal requires an exact VerificationReceipt binding",
		"NEW.status IN ('reviewing', 'ready', 'applied', 'partially_applied')",
		"new ImplementationProposal requires exact unimplemented and blocking diagnostic projections",
		"ImplementationProposal diagnostic projections are immutable",
		"candidate_freeze ImplementationProposal diagnostic projections must both be zero",
		"reviewable or applied ImplementationProposal requires zero unimplemented items and zero blocking diagnostics",
		"CREATE TRIGGER implementation_proposal_00_legacy_ai_gate",
		"CREATE TRIGGER implementation_operation_decision_00_legacy_ai_gate",
		"BEFORE INSERT OR UPDATE OR DELETE ON implementation_operation_decisions",
		"proposal.candidate_verification_binding_version",
		"freeze_receipt.implementation_proposal_id = target_proposal_id",
		"freeze_receipt.verification_receipt_id = target_verification_receipt_id",
		"JOIN candidate_verification_receipts AS verification_receipt",
		"FOR KEY SHARE OF freeze_receipt, verification_receipt",
		"candidate_freeze ImplementationProposal decisions require an exact verified freeze receipt",
		"FOR KEY SHARE",
		"legacy AI ImplementationProposal decisions are disabled",
	} {
		if !strings.Contains(upText, required) {
			t.Fatalf("legacy AI Proposal gate migration is missing %q", required)
		}
	}
	if strings.Contains(upText, "WHERE execution_source IN ('manual_generation'") {
		t.Fatal("legacy AI Proposal gate migration rewrites historical AI Proposal rows")
	}
	if strings.Contains(upText, "quarantine") {
		t.Fatal("legacy AI Proposal gate migration invents quarantine policy")
	}

	downText := string(down)
	for _, required := range []string{
		"DROP TRIGGER IF EXISTS implementation_operation_decision_00_legacy_ai_gate",
		"DROP FUNCTION IF EXISTS guard_legacy_ai_implementation_decision()",
		"DROP TRIGGER IF EXISTS implementation_proposal_00_legacy_ai_gate",
		"DROP FUNCTION IF EXISTS guard_legacy_ai_implementation_proposal()",
		"ADD CONSTRAINT implementation_proposals_candidate_verification_shape",
		"candidate_verification_binding_version = 'candidate-verification-binding/v1'",
		") NOT VALID",
		"DROP COLUMN IF EXISTS blocking_diagnostic_count",
		"DROP COLUMN IF EXISTS unimplemented_count",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("legacy AI Proposal gate rollback is missing %q", required)
		}
	}
}
