-- Promotion v2 may read the immutable Plan locator before locking Evidence in
-- the established Evidence -> Plan order. Fence it before either relation.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

DO $qualification_plan_authority_down_guard$
BEGIN
  -- Fence migration-73 writers in their established order before observing
  -- the Plan ledgers. Legacy Evidence rows do not block this rollback: only
  -- immutable state owned by migration 000074 makes removal unsafe.
  LOCK TABLE qualification_evidence_events IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_authorities IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_identity_reservations IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM qualification_plan_authorities)
     OR EXISTS (SELECT 1 FROM qualification_plan_identity_reservations) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Plan authority while immutable authority state is nonempty';
  END IF;
END;
$qualification_plan_authority_down_guard$;

DROP TRIGGER IF EXISTS qualification_evidence_plan_authority_guard
  ON qualification_evidence_events;
DROP TRIGGER IF EXISTS qualification_plan_identity_reservations_immutable
  ON qualification_plan_identity_reservations;
DROP TRIGGER IF EXISTS qualification_plan_authorities_immutable
  ON qualification_plan_authorities;

DROP FUNCTION IF EXISTS guard_qualification_evidence_plan_authority();
DROP FUNCTION IF EXISTS resolve_qualification_plan_authority(uuid);
DROP FUNCTION IF EXISTS freeze_qualification_plan_authority(
  uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,
  text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb
);
DROP FUNCTION IF EXISTS reject_qualification_plan_immutable_mutation();
DROP TABLE IF EXISTS qualification_plan_identity_reservations;
DROP TABLE IF EXISTS qualification_plan_authorities;
DROP FUNCTION IF EXISTS qualification_plan_sha256(bytea);
