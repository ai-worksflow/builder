SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

DO $qualification_input_precommit_down_guard$
BEGIN
  IF EXISTS (
       SELECT 1 FROM pg_catalog.pg_class
       WHERE relnamespace = pg_catalog.current_schema()::pg_catalog.regnamespace
         AND relname LIKE 'qualification_promotion_v2_%'
     ) OR EXISTS (
       SELECT 1 FROM pg_catalog.pg_proc
       WHERE pronamespace = pg_catalog.current_schema()::pg_catalog.regnamespace
         AND proname LIKE '%qualification_promotion_v2%'
     ) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Input Precommit while Promotion v2 or its handoff successor is installed'
      USING ERRCODE = 'WIP02';
  END IF;
  LOCK TABLE qualification_input_precommit_executable_binding_heads
    IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_input_precommit_executable_binding_generations
    IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_input_source_receipt_admissions
    IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_input_credential_receipt_admissions
    IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_input_precommit_authorities
    IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_input_precommit_identity_reservations
    IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_input_precommit_wia_reservations
    IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_input_precommit_plan_reservations
    IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM qualification_input_precommit_executable_binding_heads)
     OR EXISTS (SELECT 1 FROM qualification_input_precommit_executable_binding_generations)
     OR EXISTS (SELECT 1 FROM qualification_input_source_receipt_admissions)
     OR EXISTS (SELECT 1 FROM qualification_input_credential_receipt_admissions)
     OR EXISTS (SELECT 1 FROM qualification_input_precommit_authorities)
     OR EXISTS (SELECT 1 FROM qualification_input_precommit_identity_reservations)
     OR EXISTS (SELECT 1 FROM qualification_input_precommit_wia_reservations)
     OR EXISTS (SELECT 1 FROM qualification_input_precommit_plan_reservations) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Input Precommit while immutable authority history exists'
      USING ERRCODE = 'WIP02';
  END IF;
END;
$qualification_input_precommit_down_guard$;

DROP TRIGGER IF EXISTS qualification_input_precommit_authority_exact_closure
  ON qualification_input_precommit_authorities;
DROP TRIGGER IF EXISTS qualification_input_credential_admission_exact_closure
  ON qualification_input_credential_receipt_admissions;
DROP TRIGGER IF EXISTS qualification_input_source_admission_exact_closure
  ON qualification_input_source_receipt_admissions;
DROP TRIGGER IF EXISTS qualification_input_precommit_plan_reservations_immutable
  ON qualification_input_precommit_plan_reservations;
DROP TRIGGER IF EXISTS qualification_input_precommit_wia_reservations_immutable
  ON qualification_input_precommit_wia_reservations;
DROP TRIGGER IF EXISTS qualification_input_precommit_identity_reservations_immutable
  ON qualification_input_precommit_identity_reservations;
DROP TRIGGER IF EXISTS qualification_input_precommit_authorities_immutable
  ON qualification_input_precommit_authorities;
DROP TRIGGER IF EXISTS qualification_input_credential_receipt_admissions_immutable
  ON qualification_input_credential_receipt_admissions;
DROP TRIGGER IF EXISTS qualification_input_source_receipt_admissions_immutable
  ON qualification_input_source_receipt_admissions;
DROP TRIGGER IF EXISTS qualification_input_precommit_executable_bindings_immutable
  ON qualification_input_precommit_executable_binding_generations;
DROP TRIGGER IF EXISTS qualification_input_precommit_binding_heads_no_removal
  ON qualification_input_precommit_executable_binding_heads;

DROP FUNCTION IF EXISTS qualification_input_precommit_apply_security_v1();
DROP FUNCTION IF EXISTS enforce_qualification_input_precommit_authority_closure_v1();
DROP FUNCTION IF EXISTS enforce_qualification_input_credential_admission_closure_v1();
DROP FUNCTION IF EXISTS enforce_qualification_input_source_admission_closure_v1();
DROP FUNCTION IF EXISTS resolve_qualification_input_precommit_for_promotion_v1(uuid,uuid);
DROP FUNCTION IF EXISTS resolve_qualification_input_precommit_authority_v1(uuid);
DROP FUNCTION IF EXISTS inspect_qualification_input_precommit_operation_v1(uuid);
DROP FUNCTION IF EXISTS issue_qualification_input_precommit_v1(
  uuid,uuid,uuid,uuid,uuid,
  text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb
);
DROP FUNCTION IF EXISTS qualification_input_precommit_authority_record_is_exact_v1(uuid);
DROP FUNCTION IF EXISTS qualification_input_precommit_plan_is_exact_v1(uuid);
DROP FUNCTION IF EXISTS resolve_qualification_input_credential_receipt_admission_v1(text);
DROP FUNCTION IF EXISTS resolve_qualification_input_source_receipt_admission_v1(text);
DROP FUNCTION IF EXISTS inspect_qualification_input_credential_receipt_v1(text);
DROP FUNCTION IF EXISTS inspect_qualification_input_source_receipt_v1(text);
DROP FUNCTION IF EXISTS admit_qualification_input_credential_receipt_v1(
  text,bytea,jsonb,text,bytea,jsonb
);
DROP FUNCTION IF EXISTS admit_qualification_input_source_receipt_v1(
  text,bytea,jsonb,text,bytea,jsonb
);
DROP FUNCTION IF EXISTS qualification_input_credential_admission_is_exact_v1(text);
DROP FUNCTION IF EXISTS qualification_input_source_admission_is_exact_v1(text);
DROP FUNCTION IF EXISTS review_qualification_input_precommit_executable_binding_v1(
  text,bigint,text,text,text
);
DROP FUNCTION IF EXISTS reject_qualification_input_precommit_mutation_v1();
DROP FUNCTION IF EXISTS qualification_input_precommit_timestamp_v1(timestamptz);
DROP FUNCTION IF EXISTS qualification_input_precommit_string_is_secret_free_v1(text);
DROP FUNCTION IF EXISTS qualification_input_precommit_caller_is_v1(text);
DROP FUNCTION IF EXISTS qualification_input_precommit_hash_v1(text,bytea);

DROP TABLE IF EXISTS qualification_input_precommit_plan_reservations;
DROP TABLE IF EXISTS qualification_input_precommit_wia_reservations;
DROP TABLE IF EXISTS qualification_input_precommit_identity_reservations;
DROP TABLE IF EXISTS qualification_input_precommit_authorities;
DROP TABLE IF EXISTS qualification_input_credential_receipt_admissions;
DROP TABLE IF EXISTS qualification_input_source_receipt_admissions;
DROP TABLE IF EXISTS qualification_input_precommit_executable_binding_heads;
DROP TABLE IF EXISTS qualification_input_precommit_executable_binding_generations;
