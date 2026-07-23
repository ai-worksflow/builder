DO $canonical_review_authority_down_guard$
BEGIN
  LOCK TABLE review_requests IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE review_decisions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE canonical_review_approval_receipts IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM canonical_review_approval_receipts)
     OR EXISTS (SELECT 1 FROM review_requests WHERE review_authority_version = 1)
     OR EXISTS (SELECT 1 FROM review_decisions WHERE review_authority_version = 1) THEN
    RAISE EXCEPTION 'cannot roll back Canonical Review authority after version 1 review state exists';
  END IF;
END;
$canonical_review_authority_down_guard$;

DROP TRIGGER IF EXISTS canonical_review_approved_requires_receipt ON review_requests;
DROP TRIGGER IF EXISTS canonical_review_decisions_controlled_mutation ON review_decisions;
DROP TRIGGER IF EXISTS canonical_review_requests_controlled_mutation ON review_requests;
DROP TRIGGER IF EXISTS canonical_review_approval_receipts_immutable ON canonical_review_approval_receipts;

DROP FUNCTION IF EXISTS require_canonical_review_approval_receipt();
DROP FUNCTION IF EXISTS canonical_review_approval_receipt_is_exact(uuid,uuid,uuid);
DROP FUNCTION IF EXISTS issue_canonical_review_approval_receipt(uuid);
DROP FUNCTION IF EXISTS resolve_canonical_review_approval_receipt(uuid,uuid,text);
DROP FUNCTION IF EXISTS guard_canonical_review_source_mutation();
DROP FUNCTION IF EXISTS reject_canonical_review_receipt_mutation();
DROP FUNCTION IF EXISTS canonical_review_approval_receipt_record_is_exact(canonical_review_approval_receipts);
DROP TABLE canonical_review_approval_receipts;
DROP FUNCTION IF EXISTS canonical_review_jsonb_bytes(jsonb);
DROP FUNCTION IF EXISTS canonical_review_authority_hash(text,bytea);

ALTER TABLE review_requests DROP CONSTRAINT review_requests_authority_close_shape_check;
ALTER TABLE review_requests DROP CONSTRAINT review_requests_closed_by_decision_fk;
ALTER TABLE review_decisions DROP CONSTRAINT review_decisions_request_id_id_key;
ALTER TABLE review_decisions DROP CONSTRAINT review_decisions_authority_facts_check;
ALTER TABLE review_decisions DROP CONSTRAINT review_decisions_authority_version_check;
ALTER TABLE review_requests DROP CONSTRAINT review_requests_authority_version_check;

ALTER TABLE review_decisions
  DROP COLUMN precondition_etag,
  DROP COLUMN solo_review_confirmed,
  DROP COLUMN sole_owner_id_at_decision,
  DROP COLUMN owner_count_at_decision,
  DROP COLUMN governance_mode_at_decision,
  DROP COLUMN reviewer_role_at_decision,
  DROP COLUMN review_authority_version;

ALTER TABLE review_requests
  DROP COLUMN closed_by_decision_id,
  DROP COLUMN review_authority_version;

CREATE FUNCTION review_request_policy_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.policy IS DISTINCT FROM OLD.policy THEN
    RAISE EXCEPTION 'review request policy is immutable';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER review_request_policy_immutable
BEFORE UPDATE ON review_requests
FOR EACH ROW EXECUTE FUNCTION review_request_policy_immutable();
