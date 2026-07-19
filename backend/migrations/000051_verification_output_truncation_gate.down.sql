DROP TRIGGER IF EXISTS canonical_receipt_truncation_from_check
  ON canonical_verification_checks;
DROP TRIGGER IF EXISTS canonical_receipt_truncation_from_receipt
  ON canonical_verification_receipts;
DROP FUNCTION IF EXISTS validate_canonical_receipt_truncation_gate();

DROP TRIGGER IF EXISTS candidate_receipt_truncation_from_check
  ON candidate_verification_checks;
DROP TRIGGER IF EXISTS candidate_receipt_truncation_from_receipt
  ON candidate_verification_receipts;
DROP FUNCTION IF EXISTS validate_candidate_receipt_truncation_gate();

ALTER TABLE canonical_verification_checks
  DROP CONSTRAINT IF EXISTS canonical_verification_check_truncation_shape;
ALTER TABLE canonical_verification_checks
  DROP COLUMN IF EXISTS truncated;

ALTER TABLE candidate_verification_checks
  DROP CONSTRAINT IF EXISTS candidate_verification_check_truncation_shape;
