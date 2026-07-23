DO $workflow_execution_profile_v3_down_guard$
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  LOCK TABLE projects IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_definition_versions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_authorities IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_review_defaults IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_exact_approved_sources IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE qualification_policy_identity_reservations IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (
       SELECT 1 FROM workflow_definition_versions
       WHERE execution_profile_version = 'workflow-engine/v3'
          OR execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     )
     OR EXISTS (
       SELECT 1 FROM workflow_runs
       WHERE execution_profile_version = 'workflow-engine/v3'
          OR execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
          OR status = 'waiting_qualification'
     )
     OR EXISTS (
       SELECT 1 FROM workflow_node_runs
       WHERE node_type = 'external_qualification_gate'
          OR status = 'waiting_qualification'
          OR input_authority_id IS NOT NULL
     )
     OR EXISTS (SELECT 1 FROM workflow_input_authorities) THEN
    RAISE EXCEPTION 'cannot roll back workflow-engine/v3 after a definition, run, gate, or Workflow Input Authority exists';
  END IF;
END;
$workflow_execution_profile_v3_down_guard$;

DROP TRIGGER IF EXISTS workflow_execution_profile_v3_node_exact_closure ON workflow_node_runs;
DROP TRIGGER IF EXISTS workflow_execution_profile_v3_run_exact_closure ON workflow_runs;
DROP TRIGGER IF EXISTS external_qualification_gate_node_v3_guard ON workflow_node_runs;
DROP TRIGGER IF EXISTS workflow_execution_profile_v3_run_guard ON workflow_runs;
DROP TRIGGER IF EXISTS workflow_execution_profile_v3_definition_guard ON workflow_definition_versions;

DROP FUNCTION IF EXISTS validate_workflow_execution_profile_v3_run_closure();
DROP FUNCTION IF EXISTS guard_external_qualification_gate_node_v3();
DROP FUNCTION IF EXISTS guard_workflow_execution_profile_v3_run();
DROP FUNCTION IF EXISTS guard_workflow_execution_profile_v3_definition();
DROP FUNCTION IF EXISTS workflow_execution_profile_v3_definition_is_database_admissible(jsonb);

ALTER TABLE workflow_node_runs
  DROP CONSTRAINT workflow_node_runs_status_check,
  ADD CONSTRAINT workflow_node_runs_status_check CHECK (
    status IN ('pending','ready','running','waiting_input','waiting_review','completed','failed','cancelled','stale')
  );

ALTER TABLE workflow_runs
  DROP CONSTRAINT workflow_runs_status_check,
  ADD CONSTRAINT workflow_runs_status_check CHECK (
    status IN ('pending','running','waiting_input','waiting_review','completed','failed','cancelled','stale')
  );
