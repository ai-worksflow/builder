DO $qualification_receipt_v3_down_guard$
BEGIN
  -- Fence every supported writer in the same global order before deciding
  -- whether immutable v3 control history permits rollback.
  LOCK TABLE qualification_evidence_events IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_authorities IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_identity_reservations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_requests IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_observations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_receipts IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM qualification_receipt_v3_requests)
     OR EXISTS (SELECT 1 FROM qualification_receipt_v3_observations)
     OR EXISTS (SELECT 1 FROM qualification_receipt_v3_receipts) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Receipt v3 while immutable control state is nonempty';
  END IF;
END;
$qualification_receipt_v3_down_guard$;

DROP TRIGGER IF EXISTS qualification_promotion_v1_new_consumption_history_only
  ON qualification_promotion_consumptions;
DROP TRIGGER IF EXISTS qualification_evidence_v1_receipt_tail_history_only
  ON qualification_evidence_events;
DROP TRIGGER IF EXISTS qualification_receipt_v3_receipts_immutable
  ON qualification_receipt_v3_receipts;
DROP TRIGGER IF EXISTS qualification_receipt_v3_observations_immutable
  ON qualification_receipt_v3_observations;
DROP TRIGGER IF EXISTS qualification_receipt_v3_requests_immutable
  ON qualification_receipt_v3_requests;

DROP FUNCTION IF EXISTS guard_qualification_promotion_v1_new_consumption_history_only();
DROP FUNCTION IF EXISTS guard_qualification_evidence_v1_receipt_tail_history_only();
DROP FUNCTION IF EXISTS complete_qualification_receipt_v3(
  uuid,text,text,text,text,text,text,text,text,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text
);
DROP FUNCTION IF EXISTS append_qualification_receipt_v3_observation(
  text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text,bytea,jsonb
);
DROP FUNCTION IF EXISTS start_qualification_receipt_v3_requests(
  text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea
);
DROP FUNCTION IF EXISTS reject_qualification_receipt_v3_mutation();

DROP TABLE IF EXISTS qualification_receipt_v3_receipts;
DROP TABLE IF EXISTS qualification_receipt_v3_observations;
DROP TABLE IF EXISTS qualification_receipt_v3_requests;
DROP FUNCTION IF EXISTS qualification_receipt_v3_sha256(bytea);
