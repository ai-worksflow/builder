-- Workflow Input Authority is durable lineage proof. Rollback is permitted
-- only before the first authority generation and before any later Promotion
-- or handoff relation has retained its ID.
DO $workflow_input_authority_down_guard$
DECLARE
  schema_name text := current_schema();
  dependent record;
  dependent_count bigint;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  LOCK TABLE projects IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_run_events IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_authorities IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_review_defaults IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_exact_approved_sources IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_identity_reservations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE artifact_revisions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE quality_runs IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE input_manifests IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE application_build_manifests IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE application_build_contracts IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE canonical_review_approval_receipts IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_input_authority_identity_reservations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_input_authority_predecessors IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_input_authority_manifests IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_input_authority_revisions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_input_authority_review_receipts IN ACCESS EXCLUSIVE MODE;

  IF EXISTS (SELECT 1 FROM qualification_policy_authorities)
     OR EXISTS (SELECT 1 FROM qualification_policy_review_defaults)
     OR EXISTS (SELECT 1 FROM qualification_policy_exact_approved_sources)
     OR EXISTS (SELECT 1 FROM qualification_policy_identity_reservations)
     OR EXISTS (SELECT 1 FROM workflow_input_authorities)
     OR EXISTS (SELECT 1 FROM workflow_input_authority_identity_reservations)
     OR EXISTS (SELECT 1 FROM workflow_input_authority_predecessors)
     OR EXISTS (SELECT 1 FROM workflow_input_authority_manifests)
     OR EXISTS (SELECT 1 FROM workflow_input_authority_revisions)
     OR EXISTS (SELECT 1 FROM workflow_input_authority_review_receipts)
     OR EXISTS (SELECT 1 FROM workflow_node_runs WHERE input_authority_id IS NOT NULL) THEN
    RAISE EXCEPTION 'cannot roll back Workflow Input Authority after an authority, child, reservation, or node binding exists; Qualification Policy Authority history is included';
  END IF;

  -- Future migrations 000080/000081/000082 own their table names. Discover their
  -- exact authority columns instead of making this historical migration depend
  -- on a table that did not exist when 000078 was published.
  FOR dependent IN
    SELECT DISTINCT candidate_column.table_name, candidate_column.column_name
    FROM information_schema.columns AS candidate_column
    WHERE candidate_column.table_schema = schema_name
      AND candidate_column.column_name IN ('workflow_input_authority_id','input_authority_id')
      AND candidate_column.table_name NOT IN (
        'workflow_node_runs','workflow_input_authorities',
        'workflow_input_authority_identity_reservations',
        'workflow_input_authority_predecessors','workflow_input_authority_manifests',
        'workflow_input_authority_revisions','workflow_input_authority_review_receipts'
      )
    ORDER BY candidate_column.table_name, candidate_column.column_name
  LOOP
    EXECUTE format('LOCK TABLE %I.%I IN ACCESS EXCLUSIVE MODE', schema_name, dependent.table_name);
    EXECUTE format(
      'SELECT count(*) FROM %I.%I WHERE %I IS NOT NULL',
      schema_name, dependent.table_name, dependent.column_name
    ) INTO dependent_count;
    IF dependent_count > 0 THEN
      RAISE EXCEPTION 'cannot roll back Workflow Input Authority while %.% retains % rows',
        dependent.table_name, dependent.column_name, dependent_count;
    END IF;
  END LOOP;
END;
$workflow_input_authority_down_guard$;

DROP TRIGGER workflow_input_authority_event_exact_closure ON workflow_run_events;
DROP TRIGGER workflow_input_authority_event_identity_guard ON workflow_run_events;
DROP TRIGGER workflow_node_input_authority_exact_closure ON workflow_node_runs;
DROP TRIGGER workflow_input_authority_review_receipts_exact_closure ON workflow_input_authority_review_receipts;
DROP TRIGGER workflow_input_authority_revisions_exact_closure ON workflow_input_authority_revisions;
DROP TRIGGER workflow_input_authority_manifests_exact_closure ON workflow_input_authority_manifests;
DROP TRIGGER workflow_input_authority_predecessors_exact_closure ON workflow_input_authority_predecessors;
DROP TRIGGER workflow_input_authorities_exact_closure ON workflow_input_authorities;

DROP TRIGGER workflow_node_stable_identity_v1_immutable ON workflow_node_runs;
DROP TRIGGER workflow_input_authority_review_receipts_immutable ON workflow_input_authority_review_receipts;
DROP TRIGGER workflow_input_authority_revisions_immutable ON workflow_input_authority_revisions;
DROP TRIGGER workflow_input_authority_manifests_immutable ON workflow_input_authority_manifests;
DROP TRIGGER workflow_input_authority_predecessors_immutable ON workflow_input_authority_predecessors;
DROP TRIGGER workflow_input_authority_identity_reservations_immutable ON workflow_input_authority_identity_reservations;
DROP TRIGGER workflow_input_authorities_immutable ON workflow_input_authorities;

DROP FUNCTION assert_current_workflow_input_authority_v1(uuid);
DROP FUNCTION resolve_workflow_input_authority_for_node_v1(uuid,uuid);
DROP FUNCTION resolve_workflow_input_authority_v1(uuid);
DROP FUNCTION inspect_workflow_input_authority_operation_v1(uuid);
DROP FUNCTION workflow_input_authority_bundle_v1(uuid);
DROP FUNCTION freeze_workflow_input_authority_v1(
  uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb
);
DROP FUNCTION validate_workflow_input_authority_closure_v1();
DROP FUNCTION guard_workflow_input_authority_event_identity_v1();
DROP FUNCTION workflow_input_authority_replay_is_exact_v1(
  uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb
);
DROP FUNCTION workflow_input_authority_record_is_exact(uuid);
DROP FUNCTION guard_workflow_node_stable_identity_v1();
DROP FUNCTION reject_workflow_input_authority_mutation();

ALTER TABLE workflow_node_runs
  DROP CONSTRAINT workflow_node_runs_input_authority_fk;

DROP TABLE workflow_input_authority_review_receipts;
DROP TABLE workflow_input_authority_revisions;
DROP TABLE workflow_input_authority_manifests;
DROP TABLE workflow_input_authority_predecessors;
DROP TABLE workflow_input_authority_identity_reservations;
DROP TABLE workflow_input_authorities;

DROP TRIGGER qualification_policy_exact_sources_exact_closure
  ON qualification_policy_exact_approved_sources;
DROP TRIGGER qualification_policy_identity_reservations_exact_closure
  ON qualification_policy_identity_reservations;
DROP TRIGGER qualification_policy_review_defaults_exact_closure
  ON qualification_policy_review_defaults;
DROP TRIGGER qualification_policy_authorities_exact_closure
  ON qualification_policy_authorities;
DROP TRIGGER qualification_policy_exact_approved_sources_immutable
  ON qualification_policy_exact_approved_sources;
DROP TRIGGER qualification_policy_identity_reservations_immutable
  ON qualification_policy_identity_reservations;
DROP TRIGGER qualification_policy_review_defaults_immutable
  ON qualification_policy_review_defaults;
DROP TRIGGER qualification_policy_authorities_immutable
  ON qualification_policy_authorities;

DROP FUNCTION assert_current_qualification_policy_authority_v1(uuid);
DROP FUNCTION resolve_current_qualification_policy_authority_v1(uuid,text,text);
DROP FUNCTION resolve_qualification_policy_authority_v1(uuid);
DROP FUNCTION inspect_qualification_policy_operation_v1(uuid);
DROP FUNCTION issue_qualification_policy_authority_v1(
  uuid,uuid,text,text,uuid,text,text,bigint,text,timestamptz,text,text,
  text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb
);
DROP FUNCTION validate_qualification_policy_authority_closure_v1();
DROP FUNCTION qualification_policy_authority_record_is_exact_v1(uuid);
DROP FUNCTION qualification_policy_embedded_uuid_references_v1(jsonb);
DROP FUNCTION reject_qualification_policy_authority_mutation();

DROP TABLE qualification_policy_identity_reservations;
DROP TABLE qualification_policy_exact_approved_sources;
DROP TABLE qualification_policy_review_defaults;
DROP TABLE qualification_policy_authorities;

DROP INDEX workflow_node_runs_input_authority_unique;
ALTER TABLE workflow_node_runs
  DROP CONSTRAINT workflow_node_runs_workflow_input_exact_unique,
  DROP CONSTRAINT workflow_node_runs_stable_identity_shape,
  DROP COLUMN input_authority_id,
  DROP COLUMN slice_id,
  DROP COLUMN slice_kind,
  DROP COLUMN definition_node_id;

ALTER TABLE workflow_run_events
  DROP CONSTRAINT workflow_run_events_workflow_input_exact_unique;
ALTER TABLE canonical_review_approval_receipts
  DROP CONSTRAINT canonical_review_receipts_workflow_input_exact_unique;

DROP FUNCTION workflow_input_canonical_jsonb_bytes(jsonb);
DROP FUNCTION workflow_input_timestamp_is_exact(text);
DROP FUNCTION workflow_input_uuid_is_exact(text);
DROP FUNCTION workflow_input_normalize_sha256(text);
DROP FUNCTION workflow_input_authority_hash(text,bytea);
DROP FUNCTION qualification_policy_authority_hash(text,bytea);
DROP FUNCTION workflow_input_raw_sha256(bytea);
