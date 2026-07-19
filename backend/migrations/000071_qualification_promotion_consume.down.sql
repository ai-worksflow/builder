DO $qualification_promotion_rollback_fence$
BEGIN
  LOCK TABLE qualification_promotion_consumptions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_promotion_handoffs IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM qualification_promotion_consumptions)
     OR EXISTS (SELECT 1 FROM qualification_promotion_handoffs) THEN
    RAISE EXCEPTION 'cannot roll back qualification promotion consumption while immutable audit or handoff state is nonempty'
      USING ERRCODE = '55000';
  END IF;
END;
$qualification_promotion_rollback_fence$;

DROP FUNCTION IF EXISTS consume_verified_qualification_promotion(
  uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb
);
DROP TRIGGER IF EXISTS qualification_promotion_handoffs_immutable ON qualification_promotion_handoffs;
DROP TRIGGER IF EXISTS qualification_promotion_consumptions_immutable ON qualification_promotion_consumptions;
DROP TABLE IF EXISTS qualification_promotion_handoffs;
DROP TABLE IF EXISTS qualification_promotion_consumptions;
DROP FUNCTION IF EXISTS reject_qualification_promotion_mutation();
