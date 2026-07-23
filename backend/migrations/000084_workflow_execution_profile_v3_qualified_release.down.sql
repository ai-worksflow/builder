SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended(
    'worksflow:workflow-input-authority-migration:v1',0
  )
);

LOCK TABLE projects IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_run_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_deployment_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_delivery_operations IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_release_v1_lease_claims IN ACCESS EXCLUSIVE MODE;

DO $qualification_release_v1_down_guard$
BEGIN
  IF EXISTS (SELECT 1 FROM qualification_release_v1_controller_bootstraps)
     OR EXISTS (SELECT 1 FROM qualification_release_v1_identity_reservations)
     OR EXISTS (SELECT 1 FROM qualification_release_v1_authorizations)
     OR EXISTS (SELECT 1 FROM qualification_release_v1_controller_bindings)
     OR EXISTS (SELECT 1 FROM qualification_release_v1_lease_claims)
     OR EXISTS (SELECT 1 FROM qualification_release_v1_results)
     OR EXISTS (SELECT 1 FROM qualification_release_v1_transaction_grants)
     OR EXISTS (
       SELECT 1 FROM workflow_run_events
       WHERE event_type IN (
         'node.execution_authorized','node.completed','node.failed'
       )
         AND id IN (
           SELECT action_event_id FROM qualification_release_v1_authorizations
           UNION ALL
           SELECT completion_event_id FROM qualification_release_v1_authorizations
         )
     )
     OR EXISTS (
       SELECT 1 FROM release_deployment_runs AS run
       JOIN qualification_release_v1_controller_bindings AS binding
         ON binding.production_run_id=run.id
     ) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Release v1 after durable authority exists; use a forward fix'
      USING ERRCODE='55000';
  END IF;
END;
$qualification_release_v1_down_guard$;

-- Revoke only the schema capability introduced by migration 84.  A deployment
-- that already provided effective USAGE keeps it; the marker lives on a
-- migration-84-owned function and therefore cannot outlive this rollback.
DO $qualification_release_v1_operator_schema_acl_down$
DECLARE
  v_schema text:=pg_catalog.current_schema();
  v_runtime_caller regprocedure:=pg_catalog.to_regprocedure(
    pg_catalog.format(
      '%I.qualification_release_v1_runtime_caller_is_exact()',
      pg_catalog.current_schema()
    )
  );
BEGIN
  IF EXISTS (
       SELECT 1 FROM pg_catalog.pg_roles
       WHERE rolname='worksflow_qualification_release_operator'
     )
     AND v_runtime_caller IS NOT NULL
     AND pg_catalog.obj_description(v_runtime_caller::oid,'pg_proc')
       ='000084 granted schema USAGE to worksflow_qualification_release_operator'
  THEN
    EXECUTE pg_catalog.format(
      'REVOKE USAGE ON SCHEMA %I FROM worksflow_qualification_release_operator',
      v_schema
    );
  END IF;
END;
$qualification_release_v1_operator_schema_acl_down$;

DROP TRIGGER IF EXISTS qualification_release_deployment_revision_exact_closure
  ON release_deployment_revisions;
DROP TRIGGER IF EXISTS qualification_release_production_receipt_exact_closure
  ON release_production_receipts;
DROP TRIGGER IF EXISTS qualification_release_controller_result_exact_closure
  ON release_delivery_operation_results;
DROP TRIGGER IF EXISTS qualification_release_controller_operation_exact_closure
  ON release_delivery_operations;
DROP TRIGGER IF EXISTS qualification_release_production_run_exact_closure
  ON release_deployment_runs;
DROP TRIGGER IF EXISTS qualification_release_outbox_exact_closure
  ON outbox_events;
DROP TRIGGER IF EXISTS qualification_release_workflow_event_exact_closure
  ON workflow_run_events;
DROP TRIGGER IF EXISTS qualification_release_workflow_node_exact_closure
  ON workflow_node_runs;
DROP TRIGGER IF EXISTS qualification_release_workflow_run_exact_closure
  ON workflow_runs;
DROP TRIGGER IF EXISTS qualification_release_v1_outbox_event_immutable
  ON outbox_events;
DROP TRIGGER IF EXISTS qualification_release_v1_workflow_event_immutable
  ON workflow_run_events;
DROP TRIGGER IF EXISTS qualification_release_v1_node_transition_guard
  ON workflow_node_runs;
DROP TRIGGER IF EXISTS qualification_release_v1_run_transition_guard
  ON workflow_runs;

-- Restore migration 82's deny-until-qualified-release boundary before the
-- v1 result predicates and private relations are removed.
CREATE OR REPLACE FUNCTION guard_workflow_execution_profile_v3_run()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
BEGIN
  IF NEW.execution_profile_version = 'workflow-engine/v3'
     OR NEW.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR NEW.status = 'waiting_qualification' THEN
    IF NEW.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
       OR NEW.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
       OR NOT EXISTS (
         SELECT 1 FROM workflow_definition_versions AS version
         WHERE version.id = NEW.definition_version_id
           AND version.execution_profile_version = NEW.execution_profile_version
           AND version.execution_profile_hash = NEW.execution_profile_hash
           AND version.content_hash = version.content->>'hash'
           AND workflow_execution_profile_v3_definition_is_database_admissible(
             version.content
           ) IS TRUE
       ) THEN
      RAISE EXCEPTION 'workflow run does not bind the exact workflow-engine/v3 definition'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'completed' THEN
      RAISE EXCEPTION 'workflow-engine/v3 cannot complete before the qualified release authority'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  IF TG_OP = 'UPDATE'
     AND OLD.execution_profile_version = 'workflow-engine/v3' THEN
    SELECT * INTO v_completion
    FROM qualification_promotion_v2_handoff_completions
    WHERE workflow_run_id = NEW.id;
    IF OLD.status = 'waiting_qualification'
       AND NEW.status IS DISTINCT FROM 'waiting_qualification' THEN
      IF NEW.status <> 'waiting_input'
         OR v_completion.handoff_id IS NULL
         OR NEW.event_cursor <> v_completion.event_cursor_after
         OR NEW.context#>ARRAY['nodes',v_completion.node_key,'output']
           IS DISTINCT FROM v_completion.gate_output_document
         OR NOT EXISTS (
           SELECT 1 FROM workflow_node_runs AS gate
           WHERE gate.id = v_completion.node_run_id
             AND gate.status = 'completed'
             AND gate.output_revision_id = v_completion.output_revision_id
             AND gate.completed_at = v_completion.completed_at
         )
         OR NOT EXISTS (
           SELECT 1 FROM workflow_node_runs AS publish
           WHERE publish.id = v_completion.publish_node_run_id
             AND publish.status = 'waiting_input'
             AND publish.attempt = 0
             AND publish.output_revision_id IS NULL
             AND publish.lease_owner IS NULL
             AND publish.lease_expires_at IS NULL
             AND publish.started_at IS NULL
             AND publish.completed_at IS NULL
             AND publish.failure IS NULL
         ) THEN
        RAISE EXCEPTION 'workflow-engine/v3 may leave qualification only through exact Handoff'
          USING ERRCODE = '23514';
      END IF;
    END IF;
    IF v_completion.handoff_id IS NOT NULL
       AND NEW.context#>ARRAY['nodes',v_completion.node_key,'output']
         IS DISTINCT FROM v_completion.gate_output_document THEN
      RAISE EXCEPTION 'workflow-engine/v3 Handoff QualityResult is immutable'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

DO $qualification_release_v1_restore_guard_security$
DECLARE v_schema text:=pg_catalog.current_schema();
BEGIN
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.guard_workflow_execution_profile_v3_run() SET search_path TO pg_catalog, %I',
    v_schema,v_schema
  );
  EXECUTE pg_catalog.format(
    'REVOKE ALL ON FUNCTION %I.guard_workflow_execution_profile_v3_run() FROM PUBLIC',
    v_schema
  );
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname='worksflow_migration_owner'
  ) THEN
    EXECUTE pg_catalog.format(
      'ALTER FUNCTION %I.guard_workflow_execution_profile_v3_run() OWNER TO worksflow_migration_owner',
      v_schema
    );
  END IF;
END;
$qualification_release_v1_restore_guard_security$;

DROP TRIGGER IF EXISTS qualification_release_result_exact_closure
  ON qualification_release_v1_results;
DROP TRIGGER IF EXISTS qualification_release_lease_claim_exact_closure
  ON qualification_release_v1_lease_claims;
DROP TRIGGER IF EXISTS qualification_release_binding_exact_closure
  ON qualification_release_v1_controller_bindings;
DROP TRIGGER IF EXISTS qualification_release_authorization_exact_closure
  ON qualification_release_v1_authorizations;
DROP TRIGGER IF EXISTS qualification_release_bootstrap_exact_closure
  ON qualification_release_v1_controller_bootstraps;
DROP TRIGGER IF EXISTS qualification_release_grant_empty_closure
  ON qualification_release_v1_transaction_grants;

DROP FUNCTION IF EXISTS validate_qualification_release_v1_closure();
DROP FUNCTION IF EXISTS guard_qualification_release_v1_event_mutation();
DROP FUNCTION IF EXISTS guard_qualification_release_v1_workflow_transition();
DROP FUNCTION IF EXISTS apply_qualification_release_failure_v1(uuid,uuid,uuid,text,integer);
DROP FUNCTION IF EXISTS apply_qualification_release_result_v1(uuid,uuid,uuid,text,integer);
DROP FUNCTION IF EXISTS qualification_release_v1_failure_completion_is_exact(uuid);
DROP FUNCTION IF EXISTS qualification_release_v1_completion_is_exact(uuid);
DROP FUNCTION IF EXISTS inspect_qualification_release_failure_v1(uuid);
DROP FUNCTION IF EXISTS record_qualification_release_failure_v1(uuid);
DROP FUNCTION IF EXISTS qualification_release_v1_failure_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS qualification_release_v1_failure_is_exact(uuid);
DROP FUNCTION IF EXISTS inspect_qualification_release_result_v1(uuid);
DROP FUNCTION IF EXISTS record_qualification_release_result_v1(uuid);
DROP FUNCTION IF EXISTS qualification_release_v1_result_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS qualification_release_v1_result_is_exact(uuid);
DROP FUNCTION IF EXISTS renew_qualification_release_publish_lease_v1(uuid,uuid,uuid,uuid,text,integer,timestamptz,timestamptz);
DROP FUNCTION IF EXISTS inspect_qualification_release_publish_claim_v1(uuid);
DROP FUNCTION IF EXISTS claim_qualification_release_publish_v1(uuid,uuid,uuid,uuid,text,integer);
DROP FUNCTION IF EXISTS qualification_release_v1_lease_claim_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS qualification_release_v1_lease_claim_chain_is_exact(uuid,integer);
DROP FUNCTION IF EXISTS qualification_release_v1_lease_claim_is_exact(uuid);
DROP FUNCTION IF EXISTS inspect_qualification_release_controller_v1(uuid);
DROP FUNCTION IF EXISTS start_qualification_release_controller_v1(uuid,uuid,text,integer);
DROP FUNCTION IF EXISTS qualification_release_v1_controller_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS qualification_release_v1_controller_binding_is_exact(uuid);
DROP FUNCTION IF EXISTS resolve_qualification_release_for_publish_v1(uuid,uuid);
DROP FUNCTION IF EXISTS resolve_qualification_release_authorization_v1(uuid);
DROP FUNCTION IF EXISTS inspect_qualification_release_operation_v1(uuid);
DROP FUNCTION IF EXISTS authorize_qualification_release_v1(uuid,uuid,uuid,text,uuid,uuid,uuid,uuid);
DROP FUNCTION IF EXISTS qualification_release_v1_authorization_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS qualification_release_v1_authorization_is_exact(uuid);
DROP FUNCTION IF EXISTS qualification_release_v1_workspace_revision_document(uuid);
DROP FUNCTION IF EXISTS inspect_qualification_release_controller_bootstrap_v1();
DROP FUNCTION IF EXISTS bootstrap_qualification_release_controller_v1(uuid,text,text,text,text);
DROP FUNCTION IF EXISTS qualification_release_v1_bootstrap_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS qualification_release_v1_controller_bootstrap_is_exact(uuid);

DROP TRIGGER IF EXISTS qualification_release_results_immutable
  ON qualification_release_v1_results;
DROP TRIGGER IF EXISTS qualification_release_controller_bindings_immutable
  ON qualification_release_v1_controller_bindings;
DROP TRIGGER IF EXISTS qualification_release_lease_claims_immutable
  ON qualification_release_v1_lease_claims;
DROP TRIGGER IF EXISTS qualification_release_authorizations_immutable
  ON qualification_release_v1_authorizations;
DROP TRIGGER IF EXISTS qualification_release_identity_reservations_immutable
  ON qualification_release_v1_identity_reservations;
DROP TRIGGER IF EXISTS qualification_release_controller_bootstraps_immutable
  ON qualification_release_v1_controller_bootstraps;
DROP FUNCTION IF EXISTS reject_qualification_release_v1_mutation();

DROP TABLE IF EXISTS qualification_release_v1_transaction_grants;
DROP TABLE IF EXISTS qualification_release_v1_results;
DROP TABLE IF EXISTS qualification_release_v1_controller_bindings;
DROP TABLE IF EXISTS qualification_release_v1_lease_claims;
DROP TABLE IF EXISTS qualification_release_v1_authorizations;
DROP TABLE IF EXISTS qualification_release_v1_identity_reservations;
DROP TABLE IF EXISTS qualification_release_v1_controller_bootstraps;

DROP FUNCTION IF EXISTS qualification_release_v1_require_primary();
DROP FUNCTION IF EXISTS qualification_release_v1_bootstrap_caller_is_exact();
DROP FUNCTION IF EXISTS qualification_release_v1_runtime_caller_is_exact();
DROP FUNCTION IF EXISTS qualification_release_v1_uuid_v4_is_exact(uuid);
DROP FUNCTION IF EXISTS qualification_release_v1_timestamp(timestamptz);
DROP FUNCTION IF EXISTS qualification_release_v1_hash(text,bytea);
