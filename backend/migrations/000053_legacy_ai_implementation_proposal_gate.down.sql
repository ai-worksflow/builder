DROP TRIGGER IF EXISTS implementation_operation_decision_00_legacy_ai_gate
  ON implementation_operation_decisions;
DROP FUNCTION IF EXISTS guard_legacy_ai_implementation_decision();

DROP TRIGGER IF EXISTS implementation_proposal_00_legacy_ai_gate
  ON implementation_proposals;
DROP FUNCTION IF EXISTS guard_legacy_ai_implementation_proposal();

-- Restore migration 041's strict Candidate VerificationReceipt shape. Keep it
-- NOT VALID so an unverified historical row terminalized while 053 was active
-- remains readable after rollback, matching 041's treatment of old history.
ALTER TABLE implementation_proposals
  DROP CONSTRAINT implementation_proposals_candidate_verification_shape,
  ADD CONSTRAINT implementation_proposals_candidate_verification_shape CHECK (
    (
      execution_source = 'candidate_freeze'
      AND candidate_verification_binding_version = 'candidate-verification-binding/v1'
      AND candidate_verification_receipt_id IS NOT NULL
      AND candidate_verification_receipt_hash IS NOT NULL
    ) OR (
      execution_source <> 'candidate_freeze'
      AND candidate_verification_binding_version IS NULL
      AND candidate_verification_receipt_id IS NULL
      AND candidate_verification_receipt_hash IS NULL
    )
  ) NOT VALID;

ALTER TABLE implementation_proposals
  DROP CONSTRAINT IF EXISTS implementation_proposals_blocking_diagnostic_count_check,
  DROP CONSTRAINT IF EXISTS implementation_proposals_unimplemented_count_check,
  DROP COLUMN IF EXISTS blocking_diagnostic_count,
  DROP COLUMN IF EXISTS unimplemented_count;
