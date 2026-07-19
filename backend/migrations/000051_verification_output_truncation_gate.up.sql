ALTER TABLE candidate_verification_checks
  ADD CONSTRAINT candidate_verification_check_truncation_shape
  CHECK (status <> 'passed' OR NOT truncated) NOT VALID;

ALTER TABLE candidate_verification_checks
  VALIDATE CONSTRAINT candidate_verification_check_truncation_shape;

ALTER TABLE canonical_verification_checks
  ADD COLUMN truncated boolean NOT NULL DEFAULT false;

ALTER TABLE canonical_verification_checks
  ALTER COLUMN truncated DROP DEFAULT;

ALTER TABLE canonical_verification_checks
  ADD CONSTRAINT canonical_verification_check_truncation_shape
  CHECK (status <> 'passed' OR NOT truncated) NOT VALID;

ALTER TABLE canonical_verification_checks
  VALIDATE CONSTRAINT canonical_verification_check_truncation_shape;

CREATE OR REPLACE FUNCTION validate_candidate_receipt_truncation_gate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_receipt_id uuid := CASE
    WHEN TG_TABLE_NAME = 'candidate_verification_receipts' THEN (to_jsonb(NEW)->>'id')::uuid
    ELSE (to_jsonb(NEW)->>'receipt_id')::uuid
  END;
BEGIN
  IF EXISTS (
    SELECT 1
    FROM candidate_verification_receipts AS receipt
    JOIN candidate_verification_checks AS check_result
      ON check_result.receipt_id = receipt.id
    WHERE receipt.id = target_receipt_id
      AND receipt.decision = 'passed'
      AND check_result.required
      AND check_result.truncated
  ) THEN
    RAISE EXCEPTION 'passed Candidate Receipt cannot contain a required check with truncated output evidence'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER candidate_receipt_truncation_from_receipt
AFTER INSERT ON candidate_verification_receipts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_receipt_truncation_gate();

CREATE CONSTRAINT TRIGGER candidate_receipt_truncation_from_check
AFTER INSERT ON candidate_verification_checks
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_receipt_truncation_gate();

CREATE OR REPLACE FUNCTION validate_canonical_receipt_truncation_gate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_receipt_id uuid := CASE
    WHEN TG_TABLE_NAME = 'canonical_verification_receipts' THEN (to_jsonb(NEW)->>'id')::uuid
    ELSE (to_jsonb(NEW)->>'receipt_id')::uuid
  END;
BEGIN
  IF EXISTS (
    SELECT 1
    FROM canonical_verification_receipts AS receipt
    JOIN canonical_verification_checks AS check_result
      ON check_result.receipt_id = receipt.id
    WHERE receipt.id = target_receipt_id
      AND receipt.decision = 'passed'
      AND check_result.required
      AND check_result.truncated
  ) THEN
    RAISE EXCEPTION 'passed Canonical Receipt cannot contain a required check with truncated output evidence'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER canonical_receipt_truncation_from_receipt
AFTER INSERT ON canonical_verification_receipts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_canonical_receipt_truncation_gate();

CREATE CONSTRAINT TRIGGER canonical_receipt_truncation_from_check
AFTER INSERT ON canonical_verification_checks
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_canonical_receipt_truncation_gate();

COMMENT ON CONSTRAINT candidate_verification_check_truncation_shape
  ON candidate_verification_checks IS
  'A bounded log capture that discarded bytes is incomplete evidence and can never be a passed check.';

COMMENT ON COLUMN canonical_verification_checks.truncated IS
  'True when stdout or stderr exceeded the bounded capture; truncated checks can never pass.';
