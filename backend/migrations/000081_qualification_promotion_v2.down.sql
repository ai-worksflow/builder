-- Qualification Promotion v2 migration 000081 rollback. Stop new
-- WIA/Promotion work before the first relation lock. Existing
-- callers drain; the subsequent lock order cannot form a DDL/runtime cycle.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

DO $qualification_promotion_v2_down_guard$
BEGIN
  LOCK TABLE qualification_promotion_v2_consumptions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_promotion_v2_consumption_independent_receipts IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_promotion_v2_handoffs IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_promotion_v2_identity_reservations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_promotion_v2_independent_receipts IN ACCESS EXCLUSIVE MODE;
  -- Ordinary revision inserts acquire artifact_revisions before the shared
  -- reservation relation; rollback uses the same order after v2 is drained.
  LOCK TABLE artifact_revisions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE artifact_revision_identity_reservations IN ACCESS EXCLUSIVE MODE;

  IF EXISTS (SELECT 1 FROM qualification_promotion_v2_independent_receipts)
     OR EXISTS (SELECT 1 FROM qualification_promotion_v2_consumptions)
     OR EXISTS (SELECT 1 FROM qualification_promotion_v2_consumption_independent_receipts)
     OR EXISTS (SELECT 1 FROM qualification_promotion_v2_handoffs)
     OR EXISTS (SELECT 1 FROM qualification_promotion_v2_identity_reservations)
     OR EXISTS (
       SELECT 1 FROM artifact_revision_identity_reservations
       WHERE owner_kind = 'qualification-promotion-v2'
          OR owner_operation_id IS NOT NULL
     ) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Promotion v2 while immutable promotion history exists'
      USING ERRCODE = '55000';
  END IF;
END;
$qualification_promotion_v2_down_guard$;

DO $qualification_promotion_v2_restore_v1_acl$
DECLARE
  schema_name text := pg_catalog.current_schema();
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_promotion_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'GRANT SELECT ON TABLE %I.qualification_promotion_consumptions '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT SELECT ON TABLE %I.qualification_promotion_handoffs '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.consume_verified_qualification_promotion('
      'uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb) '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.assert_current_workflow_input_authority_v1(uuid) '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.assert_current_qualification_policy_authority_v1(uuid) '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
  END IF;
END;
$qualification_promotion_v2_restore_v1_acl$;

DROP TRIGGER IF EXISTS artifact_revisions_shared_identity_reservation ON artifact_revisions;
DROP FUNCTION IF EXISTS reserve_ordinary_artifact_revision_identity_v1();

DROP FUNCTION IF EXISTS consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid);
DROP FUNCTION IF EXISTS inspect_qualification_promotion_v2_operation(uuid);
DROP FUNCTION IF EXISTS resolve_qualification_promotion_v2_handoff(uuid);
DROP FUNCTION IF EXISTS assert_pending_qualification_promotion_v2_handoff(uuid);
DROP FUNCTION IF EXISTS inspect_historical_qualification_promotion_v1_operation(uuid);
DROP FUNCTION IF EXISTS qualification_promotion_v2_store_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS qualification_promotion_v2_store_record_is_exact(uuid);
DROP FUNCTION IF EXISTS qualification_promotion_v2_plan_is_exact(uuid);

DROP TRIGGER IF EXISTS qualification_promotion_v2_consumption_independent_receipts_immutable
  ON qualification_promotion_v2_consumption_independent_receipts;
DROP TRIGGER IF EXISTS qualification_promotion_v2_handoffs_immutable
  ON qualification_promotion_v2_handoffs;
DROP TRIGGER IF EXISTS qualification_promotion_v2_identity_reservations_immutable
  ON qualification_promotion_v2_identity_reservations;
DROP TRIGGER IF EXISTS qualification_promotion_v2_independent_receipts_immutable
  ON qualification_promotion_v2_independent_receipts;
DROP TRIGGER IF EXISTS qualification_promotion_v2_consumptions_immutable
  ON qualification_promotion_v2_consumptions;
DROP TRIGGER IF EXISTS artifact_revision_identity_reservations_immutable
  ON artifact_revision_identity_reservations;

ALTER TABLE artifact_revision_identity_reservations
  DROP CONSTRAINT IF EXISTS artifact_revision_identity_promotion_operation_fk;

DROP TABLE IF EXISTS qualification_promotion_v2_consumption_independent_receipts;
DROP TABLE IF EXISTS qualification_promotion_v2_handoffs;
DROP TABLE IF EXISTS qualification_promotion_v2_identity_reservations;
DROP TABLE IF EXISTS qualification_promotion_v2_independent_receipts;
DROP TABLE IF EXISTS qualification_promotion_v2_consumptions;
-- Only derived owner_kind=artifact-revision backfill remains after the guard.
DROP TABLE IF EXISTS artifact_revision_identity_reservations;

DROP FUNCTION IF EXISTS reject_qualification_promotion_v2_mutation();
DROP FUNCTION IF EXISTS qualification_promotion_v2_timestamp(timestamptz);
DROP FUNCTION IF EXISTS qualification_promotion_v2_hash(text,bytea);
