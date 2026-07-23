-- Qualification Handoff v1 rollback is forward-guarded. Immutable output,
-- completion, authority, or an unconsumed transaction grant is never erased.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

LOCK TABLE projects IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_run_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_revisions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_revision_sources IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_dependencies IN ACCESS EXCLUSIVE MODE;
LOCK TABLE trace_links IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_consumptions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_handoffs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_revision_identity_reservations IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_revision_transaction_grants IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_revision_authority_bindings IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_handoff_lineage_members IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_handoff_completions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE;

DO $qualification_handoff_v1_down_guard$
BEGIN
  IF EXISTS (
       SELECT 1 FROM qualification_promotion_v2_revision_transaction_grants
     )
     OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_revision_authority_bindings
     )
     OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_handoff_lineage_members
     )
     OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_handoff_completions
     )
     OR EXISTS (
       SELECT 1 FROM artifact_revisions
       WHERE promotion_handoff_id IS NOT NULL
     )
     OR EXISTS (
       SELECT 1 FROM workflow_node_runs AS node
       WHERE node.output_revision_id IN (
         SELECT output_revision_id
         FROM qualification_promotion_v2_handoffs
       )
     )
     OR EXISTS (
       SELECT 1 FROM workflow_run_events AS event
       WHERE event.event_type IN (
         'node.completed','node.execution_authorization_required'
       )
         AND event.payload ? 'handoffId'
     ) THEN
    RAISE EXCEPTION 'cannot roll back Qualification Handoff v1 after durable output exists'
      USING ERRCODE = '55000';
  END IF;
END;
$qualification_handoff_v1_down_guard$;

DROP TRIGGER IF EXISTS qualification_handoff_run_exact_closure ON workflow_runs;
DROP TRIGGER IF EXISTS qualification_handoff_node_exact_closure ON workflow_node_runs;
DROP TRIGGER IF EXISTS qualification_handoff_outbox_exact_closure ON outbox_events;
DROP TRIGGER IF EXISTS qualification_handoff_outbox_immutable ON outbox_events;
DROP TRIGGER IF EXISTS qualification_handoff_event_exact_closure ON workflow_run_events;
DROP TRIGGER IF EXISTS qualification_handoff_trace_exact_closure ON trace_links;
DROP TRIGGER IF EXISTS qualification_handoff_dependency_exact_closure ON artifact_dependencies;
DROP TRIGGER IF EXISTS qualification_handoff_revision_source_exact_closure ON artifact_revision_sources;
DROP TRIGGER IF EXISTS qualification_handoff_revision_exact_closure ON artifact_revisions;
DROP TRIGGER IF EXISTS qualification_handoff_revision_authority_exact_closure
  ON qualification_promotion_v2_revision_authority_bindings;
DROP TRIGGER IF EXISTS qualification_handoff_lineage_member_exact_closure
  ON qualification_promotion_v2_handoff_lineage_members;
DROP TRIGGER IF EXISTS qualification_handoff_completion_exact_closure
  ON qualification_promotion_v2_handoff_completions;
DROP TRIGGER IF EXISTS qualification_handoff_grant_empty_closure
  ON qualification_promotion_v2_revision_transaction_grants;

DROP TRIGGER IF EXISTS qualification_promotion_v2_handoff_pending_dispatch
  ON qualification_promotion_v2_handoffs;
DROP FUNCTION IF EXISTS enqueue_qualification_promotion_v2_handoff_v1();

-- Pending dispatch is a migration-82 derived work item, not workflow history.
DELETE FROM outbox_events
WHERE event_type = 'qualification.promotion_handoff.pending'
  AND aggregate_type = 'qualification_promotion_v2_handoff'
  AND subject = 'worksflow.qualification.promotion-handoff.pending';
DROP INDEX IF EXISTS qualification_promotion_handoff_pending_dispatch_unique;

DROP TRIGGER IF EXISTS qualification_handoff_completions_immutable
  ON qualification_promotion_v2_handoff_completions;
DROP TRIGGER IF EXISTS qualification_handoff_revision_authorities_immutable
  ON qualification_promotion_v2_revision_authority_bindings;
DROP TRIGGER IF EXISTS qualification_handoff_lineage_members_immutable
  ON qualification_promotion_v2_handoff_lineage_members;

DO $qualification_handoff_v1_revoke_runtime$
DECLARE
  v_schema text := pg_catalog.current_schema();
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_handoff_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'REVOKE EXECUTE ON FUNCTION %I.complete_qualification_promotion_v2_handoff(uuid) '
      'FROM worksflow_qualification_handoff_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'REVOKE EXECUTE ON FUNCTION %I.inspect_qualification_promotion_v2_handoff_completion(uuid) '
      'FROM worksflow_qualification_handoff_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'REVOKE USAGE ON SCHEMA %I FROM worksflow_qualification_handoff_operator',
      v_schema
    );
  END IF;
END;
$qualification_handoff_v1_revoke_runtime$;

DROP FUNCTION IF EXISTS complete_qualification_promotion_v2_handoff(uuid);
DROP FUNCTION IF EXISTS inspect_qualification_promotion_v2_handoff_completion(uuid);
DROP FUNCTION IF EXISTS qualification_handoff_v1_completion_bundle(uuid,boolean,boolean);
DROP FUNCTION IF EXISTS validate_qualification_handoff_v1_closure();
DROP FUNCTION IF EXISTS qualification_handoff_v1_completion_is_exact(uuid);
DROP FUNCTION IF EXISTS qualification_handoff_v1_quality_result(uuid,uuid);
DROP FUNCTION IF EXISTS reject_qualification_handoff_v1_mutation();

-- Restore migration-79's deny-until-Handoff guards before removing the
-- relations referenced by migration82's narrow completed shape.
CREATE OR REPLACE FUNCTION guard_workflow_execution_profile_v3_run()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
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
           AND workflow_execution_profile_v3_definition_is_database_admissible(version.content) IS TRUE
       ) THEN
      RAISE EXCEPTION 'workflow run does not bind the exact workflow-engine/v3 definition'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'completed' THEN
      RAISE EXCEPTION 'workflow-engine/v3 cannot complete before the private qualification handoff'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

DROP TRIGGER workflow_execution_profile_v3_run_guard ON workflow_runs;
CREATE TRIGGER workflow_execution_profile_v3_run_guard
BEFORE INSERT OR UPDATE OF definition_version_id, execution_profile_version,
  execution_profile_hash, status
ON workflow_runs
FOR EACH ROW EXECUTE FUNCTION guard_workflow_execution_profile_v3_run();

CREATE OR REPLACE FUNCTION guard_external_qualification_gate_node_v3()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_run workflow_runs%ROWTYPE;
  v_definition jsonb;
  v_definition_type text;
BEGIN
  SELECT * INTO v_run FROM workflow_runs WHERE id = NEW.run_id;
  IF v_run.id IS NULL THEN
    RAISE EXCEPTION 'external qualification node has no workflow run'
      USING ERRCODE = '23503';
  END IF;
  SELECT version.content INTO v_definition
  FROM workflow_definition_versions AS version
  WHERE version.id = v_run.definition_version_id;

  IF NEW.definition_node_id IS NOT NULL THEN
    SELECT value->>'type' INTO v_definition_type
    FROM jsonb_array_elements(COALESCE(v_definition->'nodes','[]'::jsonb)) AS node(value)
    WHERE value->>'id' = NEW.definition_node_id;
  END IF;

  IF NEW.node_type = 'external_qualification_gate'
     OR NEW.status = 'waiting_qualification'
     OR NEW.input_authority_id IS NOT NULL THEN
    IF v_run.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
       OR v_run.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
       OR workflow_execution_profile_v3_definition_is_database_admissible(v_definition) IS NOT TRUE
       OR NEW.node_type IS DISTINCT FROM 'external_qualification_gate'
       OR v_definition_type IS DISTINCT FROM 'external_qualification_gate'
       OR NEW.definition_node_id IS DISTINCT FROM 'external-qualification'
       OR NEW.node_key IS DISTINCT FROM 'external-qualification'
       OR NEW.slice_kind IS DISTINCT FROM 'root' OR NEW.slice_id IS NOT NULL
       OR NEW.attempt <> 0 OR NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS NOT NULL OR NEW.completed_at IS NOT NULL OR NEW.failure IS NOT NULL
       OR NEW.input_manifest_id IS NOT NULL
       OR NEW.output_proposal_id IS NOT NULL
       OR NEW.status NOT IN ('pending','waiting_qualification','cancelled','stale')
       OR (TG_OP = 'INSERT' AND NEW.status <> 'pending')
       OR (TG_OP = 'UPDATE' AND OLD.node_type = 'external_qualification_gate' AND (
         (OLD.status = 'pending' AND NEW.status NOT IN ('pending','waiting_qualification','cancelled','stale'))
         OR (OLD.status = 'waiting_qualification' AND NEW.status NOT IN ('waiting_qualification','cancelled','stale'))
         OR (OLD.status IN ('cancelled','stale') AND NEW.status <> OLD.status)
       ))
       OR (NEW.status = 'pending' AND NEW.input_authority_id IS NOT NULL)
       OR (NEW.status = 'waiting_qualification' AND (
         NEW.input_authority_id IS NULL
         OR NOT EXISTS (
           SELECT 1 FROM workflow_input_authorities AS authority
           WHERE authority.authority_id = NEW.input_authority_id
             AND authority.workflow_run_id = NEW.run_id
             AND authority.node_run_id = NEW.id
         )
       ))
       OR NEW.output_revision_id IS NOT NULL THEN
      RAISE EXCEPTION 'dedicated external qualification gate cannot use a generic workflow transition'
        USING ERRCODE = '23514';
    END IF;
  ELSIF v_run.execution_profile_version = 'workflow-engine/v3'
        AND (NEW.definition_node_id IS NULL OR v_definition_type IS DISTINCT FROM NEW.node_type) THEN
    RAISE EXCEPTION 'workflow-engine/v3 node does not match its stable definition identity'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION validate_workflow_execution_profile_v3_run_closure()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_run_id uuid;
  v_run workflow_runs%ROWTYPE;
  v_expected_external_id text;
BEGIN
  IF TG_TABLE_NAME = 'workflow_runs' THEN
    IF TG_OP = 'DELETE' THEN v_run_id := OLD.id; ELSE v_run_id := NEW.id; END IF;
  ELSE
    IF TG_OP = 'DELETE' THEN v_run_id := OLD.run_id; ELSE v_run_id := NEW.run_id; END IF;
  END IF;
  SELECT * INTO v_run FROM workflow_runs WHERE id = v_run_id;
  IF v_run.id IS NULL OR v_run.execution_profile_version <> 'workflow-engine/v3' THEN
    RETURN NULL;
  END IF;
  SELECT value->>'id' INTO v_expected_external_id
  FROM workflow_definition_versions AS version,
       LATERAL jsonb_array_elements(version.content->'nodes') AS node(value)
  WHERE version.id = v_run.definition_version_id
    AND value->>'type' = 'external_qualification_gate';
  IF v_expected_external_id IS NULL
     OR v_expected_external_id <> 'external-qualification'
     OR (SELECT count(*) FROM workflow_node_runs AS node
         WHERE node.run_id = v_run.id
           AND node.node_type = 'external_qualification_gate'
           AND node.definition_node_id = v_expected_external_id
           AND node.node_key = 'external-qualification'
           AND node.slice_kind = 'root' AND node.slice_id IS NULL
           AND node.attempt = 0
           AND node.lease_owner IS NULL AND node.lease_expires_at IS NULL
           AND node.started_at IS NULL AND node.completed_at IS NULL AND node.failure IS NULL
           AND node.input_manifest_id IS NULL
           AND node.output_proposal_id IS NULL AND node.output_revision_id IS NULL
           AND node.status IN ('pending','waiting_qualification','cancelled','stale')) <> 1
     OR (v_run.status = 'waiting_qualification' AND NOT EXISTS (
       SELECT 1 FROM workflow_node_runs AS node
       WHERE node.run_id = v_run.id
         AND node.node_type = 'external_qualification_gate'
         AND node.definition_node_id = 'external-qualification'
         AND node.node_key = 'external-qualification'
         AND node.status = 'waiting_qualification'
         AND node.input_authority_id IS NOT NULL
         AND node.started_at IS NULL AND node.completed_at IS NULL AND node.failure IS NULL
     ))
     OR (v_run.status <> 'waiting_qualification' AND EXISTS (
       SELECT 1 FROM workflow_node_runs AS node
       WHERE node.run_id = v_run.id
         AND node.node_type = 'external_qualification_gate'
         AND node.definition_node_id = 'external-qualification'
         AND node.node_key = 'external-qualification'
         AND node.status = 'waiting_qualification'
         AND node.input_authority_id IS NOT NULL
         AND node.started_at IS NULL AND node.completed_at IS NULL AND node.failure IS NULL
     )) THEN
    RAISE EXCEPTION 'workflow-engine/v3 run lacks its exact external qualification gate closure'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$function$;

DROP TABLE IF EXISTS qualification_promotion_v2_handoff_completions;
DROP TABLE IF EXISTS qualification_promotion_v2_handoff_lineage_members;
DROP TABLE IF EXISTS qualification_promotion_v2_revision_authority_bindings;
DROP TABLE IF EXISTS qualification_promotion_v2_revision_transaction_grants;

DROP INDEX IF EXISTS artifact_revisions_ordinary_content_unique;
ALTER TABLE artifact_revisions
  DROP CONSTRAINT IF EXISTS artifact_revisions_promotion_handoff_unique,
  DROP CONSTRAINT IF EXISTS artifact_revisions_promotion_handoff_fk,
  DROP COLUMN promotion_handoff_id,
  ADD CONSTRAINT artifact_revisions_artifact_id_content_hash_key
    UNIQUE (artifact_id, content_hash);

-- Restore migration-81's ordinary-only reservation trigger implementation.
CREATE OR REPLACE FUNCTION reserve_ordinary_artifact_revision_identity_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_owner artifact_revision_identity_reservations%ROWTYPE;
BEGIN
  INSERT INTO artifact_revision_identity_reservations(
    id, owner_kind, owner_operation_id, reserved_at
  ) VALUES (
    NEW.id, 'artifact-revision', NULL,
    pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp())
  )
  ON CONFLICT (id) DO NOTHING;
  IF NOT FOUND THEN
    SELECT * INTO v_owner
    FROM artifact_revision_identity_reservations
    WHERE id = NEW.id
    FOR SHARE;
    IF v_owner.owner_kind <> 'artifact-revision' THEN
      RAISE EXCEPTION 'artifact revision identity is owned by Qualification Promotion v2'
        USING ERRCODE = 'WPV02';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

DO $qualification_handoff_v1_restore_promotion_config$
DECLARE
  v_schema text := pg_catalog.current_schema();
BEGIN
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.reserve_ordinary_artifact_revision_identity_v1() '
    'SET search_path TO pg_catalog, %I, pg_temp', v_schema, v_schema
  );
END;
$qualification_handoff_v1_restore_promotion_config$;

CREATE OR REPLACE FUNCTION reject_artifact_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'artifact revisions cannot be deleted';
  END IF;
  IF NEW.artifact_id IS DISTINCT FROM OLD.artifact_id
    OR NEW.revision_number IS DISTINCT FROM OLD.revision_number
    OR NEW.parent_revision_id IS DISTINCT FROM OLD.parent_revision_id
    OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
    OR NEW.content_store IS DISTINCT FROM OLD.content_store
    OR NEW.content_ref IS DISTINCT FROM OLD.content_ref
    OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
    OR NEW.byte_size IS DISTINCT FROM OLD.byte_size
    OR NEW.change_source IS DISTINCT FROM OLD.change_source
    OR NEW.change_summary IS DISTINCT FROM OLD.change_summary
    OR NEW.source_manifest_id IS DISTINCT FROM OLD.source_manifest_id
    OR NEW.proposal_id IS DISTINCT FROM OLD.proposal_id
    OR NEW.implementation_proposal_id IS DISTINCT FROM OLD.implementation_proposal_id
    OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'artifact revision content is immutable';
  END IF;
  RETURN NEW;
END;
$function$;

DROP FUNCTION IF EXISTS qualification_handoff_v1_timestamp(timestamptz);
DROP FUNCTION IF EXISTS qualification_handoff_v1_hash(text,bytea);
