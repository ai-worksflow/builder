-- Rollback is intentionally possible only before the first immutable Quality
-- completion has been admitted.  Once present, the retained candidate is
-- historical authority and cannot be reconstructed or discarded safely.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1',0)
);

LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_run_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_v3_quality_completion_precommits IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_v3_quality_completion_materials IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_v3_quality_completion_material_manifests IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_v3_quality_completion_material_revisions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_v3_quality_completion_material_review_receipts IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_v3_quality_completion_candidate_snapshots IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_v3_quality_completion_identity_reservations IN ACCESS EXCLUSIVE MODE;

DO $workflow_v3_quality_completion_rollback_guard$
BEGIN
  IF EXISTS (SELECT 1 FROM workflow_v3_quality_completion_precommits)
     OR EXISTS (SELECT 1 FROM workflow_v3_quality_completion_materials)
     OR EXISTS (
       SELECT 1 FROM workflow_v3_quality_completion_material_manifests
     ) OR EXISTS (
       SELECT 1 FROM workflow_v3_quality_completion_material_revisions
     ) OR EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_review_receipts
     ) OR EXISTS (
       SELECT 1 FROM workflow_v3_quality_completion_candidate_snapshots
     ) OR EXISTS (
       SELECT 1 FROM workflow_v3_quality_completion_identity_reservations
     ) THEN
    RAISE EXCEPTION
      'cannot roll back 000085 after an immutable v3 Quality completion exists'
      USING ERRCODE='55000';
  END IF;
END;
$workflow_v3_quality_completion_rollback_guard$;

DO $workflow_v3_quality_completion_restore_acl$
DECLARE
  v_schema text:=pg_catalog.current_schema();
  v_marker text;
BEGIN
  SELECT pg_catalog.obj_description(
    pg_catalog.to_regprocedure(
      v_schema||'.workflow_v3_quality_completion_runtime_caller_is_exact_v1(text)'
    ),'pg_proc'
  ) INTO v_marker;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='worksflow_application'
  ) THEN
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.freeze_workflow_input_authority_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb) TO worksflow_application',
      v_schema
    );
  END IF;
  IF v_marker=
       '000085 granted schema USAGE to worksflow_workflow_input_authority_operator'
     AND EXISTS (
       SELECT 1 FROM pg_catalog.pg_roles
       WHERE rolname='worksflow_workflow_input_authority_operator'
     ) THEN
    EXECUTE pg_catalog.format(
      'REVOKE USAGE ON SCHEMA %I FROM worksflow_workflow_input_authority_operator',
      v_schema
    );
  END IF;
END;
$workflow_v3_quality_completion_restore_acl$;

DROP TRIGGER IF EXISTS workflow_v3_quality_completion_run_mutation_guard
  ON workflow_runs;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_node_mutation_guard
  ON workflow_node_runs;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_event_immutable
  ON workflow_run_events;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_outbox_immutable
  ON outbox_events;
DROP TRIGGER IF EXISTS workflow_v3_quality_run_exact_closure
  ON workflow_runs;
DROP TRIGGER IF EXISTS workflow_v3_quality_node_exact_closure
  ON workflow_node_runs;
DROP TRIGGER IF EXISTS workflow_v3_quality_event_exact_closure
  ON workflow_run_events;
DROP TRIGGER IF EXISTS workflow_v3_quality_outbox_exact_closure
  ON outbox_events;
DROP TRIGGER IF EXISTS workflow_v3_quality_precommit_exact_closure
  ON workflow_v3_quality_completion_precommits;
DROP TRIGGER IF EXISTS workflow_v3_quality_material_exact_closure
  ON workflow_v3_quality_completion_materials;
DROP TRIGGER IF EXISTS workflow_v3_quality_manifest_exact_closure
  ON workflow_v3_quality_completion_material_manifests;
DROP TRIGGER IF EXISTS workflow_v3_quality_revision_exact_closure
  ON workflow_v3_quality_completion_material_revisions;
DROP TRIGGER IF EXISTS workflow_v3_quality_review_exact_closure
  ON workflow_v3_quality_completion_material_review_receipts;
DROP TRIGGER IF EXISTS workflow_v3_quality_snapshot_exact_closure
  ON workflow_v3_quality_completion_candidate_snapshots;
DROP TRIGGER IF EXISTS workflow_v3_quality_reservation_exact_closure
  ON workflow_v3_quality_completion_identity_reservations;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_precommits_immutable
  ON workflow_v3_quality_completion_precommits;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_materials_immutable
  ON workflow_v3_quality_completion_materials;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_material_manifests_immutable
  ON workflow_v3_quality_completion_material_manifests;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_material_revisions_immutable
  ON workflow_v3_quality_completion_material_revisions;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_material_receipts_immutable
  ON workflow_v3_quality_completion_material_review_receipts;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_snapshots_immutable
  ON workflow_v3_quality_completion_candidate_snapshots;
DROP TRIGGER IF EXISTS workflow_v3_quality_completion_reservations_immutable
  ON workflow_v3_quality_completion_identity_reservations;

DROP FUNCTION IF EXISTS freeze_workflow_input_authority_from_quality_precommit_v1(
  uuid,uuid,uuid,uuid,bigint,uuid,bigint,
  bytea,bytea,bytea,bytea,bytea,jsonb
);
DROP FUNCTION IF EXISTS resolve_workflow_v3_quality_completion_candidate_v1(uuid);
DROP FUNCTION IF EXISTS resolve_workflow_v3_quality_completion_material_plan_v1(
  uuid,uuid,bytea
);
DROP FUNCTION IF EXISTS inspect_workflow_v3_quality_completion_precommit_v1(uuid);
DROP FUNCTION IF EXISTS precommit_workflow_v3_quality_completion_v1(
  uuid,uuid,uuid,uuid,uuid,uuid,uuid,bigint,uuid,text,integer,
  timestamptz,jsonb,uuid,bytea
);
DROP FUNCTION IF EXISTS admit_workflow_v3_quality_completion_materials_v1(
  uuid,uuid,bytea,bytea,bytea,bytea,bytea,jsonb
);
DROP FUNCTION IF EXISTS validate_workflow_v3_quality_completion_closure_v1();
DROP FUNCTION IF EXISTS guard_workflow_v3_quality_completion_event_mutation_v1();
DROP FUNCTION IF EXISTS guard_workflow_v3_quality_completion_workflow_mutation_v1();
DROP FUNCTION IF EXISTS workflow_v3_quality_completion_commit_is_exact_v1(uuid);
DROP FUNCTION IF EXISTS workflow_v3_quality_completion_snapshot_is_exact_v1(uuid);
DROP FUNCTION IF EXISTS workflow_v3_quality_completion_material_bundle_v1(uuid);
DROP FUNCTION IF EXISTS workflow_v3_quality_completion_rfc3339_v1(timestamptz);
DROP FUNCTION IF EXISTS workflow_v3_quality_completion_require_primary_v1();
DROP FUNCTION IF EXISTS reject_workflow_v3_quality_completion_mutation_v1();
DROP FUNCTION IF EXISTS workflow_v3_quality_completion_runtime_caller_is_exact_v1(text);

DROP TABLE IF EXISTS workflow_v3_quality_completion_identity_reservations;
DROP TABLE IF EXISTS workflow_v3_quality_completion_candidate_snapshots;
DROP TABLE IF EXISTS workflow_v3_quality_completion_material_review_receipts;
DROP TABLE IF EXISTS workflow_v3_quality_completion_material_revisions;
DROP TABLE IF EXISTS workflow_v3_quality_completion_material_manifests;
DROP TABLE IF EXISTS workflow_v3_quality_completion_materials;
DROP TABLE IF EXISTS workflow_v3_quality_completion_precommits;
