-- workflow-engine/v3 Quality completion precommit and immutable activation
-- candidate snapshot.  The public precommit ABI is already deployed by the
-- Workflow runtime and must remain exactly fifteen arguments / twenty-four
-- returned columns.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1',0)
);

LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_run_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE;

-- A completed v3 Quality node without the atomic precommit cannot be assigned
-- immutable activation input after the fact.
DO $workflow_v3_quality_completion_existing_guard$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM workflow_node_runs AS node
    JOIN workflow_runs AS run ON run.id=node.run_id
    WHERE run.execution_profile_version='workflow-engine/v3'
      AND run.execution_profile_hash=
        '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
      AND node.node_type='quality_gate'
      AND node.status='completed'
  ) THEN
    RAISE EXCEPTION
      '000085 cannot recover immutable activation input for an already completed v3 Quality node'
      USING ERRCODE='55000';
  END IF;
END;
$workflow_v3_quality_completion_existing_guard$;

CREATE TABLE workflow_v3_quality_completion_precommits (
  precommit_id uuid PRIMARY KEY,
  workflow_input_operation_id uuid NOT NULL UNIQUE,
  workflow_input_authority_id uuid NOT NULL UNIQUE,
  activation_event_id uuid NOT NULL UNIQUE,
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  quality_node_run_id uuid NOT NULL,
  quality_node_key text NOT NULL CHECK (
    pg_catalog.octet_length(quality_node_key) BETWEEN 1 AND 256
    AND quality_node_key=pg_catalog.btrim(quality_node_key)
  ),
  gate_node_run_id uuid NOT NULL,
  gate_node_key text NOT NULL CHECK (
    pg_catalog.octet_length(gate_node_key) BETWEEN 1 AND 256
    AND gate_node_key=pg_catalog.btrim(gate_node_key)
  ),
  expected_run_cursor bigint NOT NULL CHECK (
    expected_run_cursor BETWEEN 0 AND 9007199254740990
  ),
  completion_event_sequence bigint NOT NULL CHECK (
    completion_event_sequence BETWEEN 1 AND 9007199254740991
    AND completion_event_sequence=expected_run_cursor+1
  ),
  completion_event_id uuid NOT NULL UNIQUE,
  completion_event_payload jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(completion_event_payload)='object'
  ),
  completion_event_actor_id uuid,
  quality_completed_at timestamptz NOT NULL CHECK (
    quality_completed_at=pg_catalog.date_trunc('milliseconds',quality_completed_at)
  ),
  quality_lease_owner text NOT NULL CHECK (
    pg_catalog.octet_length(quality_lease_owner) BETWEEN 1 AND 256
    AND quality_lease_owner=pg_catalog.btrim(quality_lease_owner)
  ),
  quality_attempt integer NOT NULL CHECK (
    quality_attempt BETWEEN 1 AND 2147483647
  ),
  workspace_revision_id uuid NOT NULL,
  gate_input_raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(gate_input_raw_bytes) BETWEEN 1 AND 16777216
  ),
  gate_input_raw_bytes_hash text NOT NULL CHECK (
    gate_input_raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  gate_input_raw_bytes_size bigint NOT NULL CHECK (
    gate_input_raw_bytes_size BETWEEN 1 AND 16777216
  ),
  gate_input_semantic_hash text NOT NULL CHECK (
    gate_input_semantic_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  gate_input_binding_count integer NOT NULL CHECK (
    gate_input_binding_count=1
  ),
  UNIQUE(workflow_run_id,quality_node_run_id),
  UNIQUE(workflow_run_id,gate_node_run_id),
  UNIQUE(workflow_run_id,completion_event_sequence),
  FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE RESTRICT,
  FOREIGN KEY(workflow_run_id,project_id)
    REFERENCES workflow_runs(id,project_id) ON DELETE RESTRICT,
  FOREIGN KEY(quality_node_run_id,workflow_run_id,quality_node_key)
    REFERENCES workflow_node_runs(id,run_id,node_key) ON DELETE RESTRICT,
  FOREIGN KEY(gate_node_run_id,workflow_run_id,gate_node_key)
    REFERENCES workflow_node_runs(id,run_id,node_key) ON DELETE RESTRICT,
  FOREIGN KEY(completion_event_id,workflow_run_id,completion_event_sequence)
    REFERENCES workflow_run_events(id,run_id,sequence)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(completion_event_actor_id)
    REFERENCES users(id) ON DELETE RESTRICT,
  FOREIGN KEY(workspace_revision_id)
    REFERENCES artifact_revisions(id) ON DELETE RESTRICT
);

CREATE TABLE workflow_v3_quality_completion_materials (
  precommit_id uuid PRIMARY KEY,
  completion_event_id uuid NOT NULL UNIQUE,
  definition_raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(definition_raw_bytes) BETWEEN 1 AND 8388608
  ),
  run_scope_raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(run_scope_raw_bytes) BETWEEN 1 AND 8388608
  ),
  node_input_raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(node_input_raw_bytes) BETWEEN 1 AND 16777216
  ),
  build_manifest_raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(build_manifest_raw_bytes) BETWEEN 1 AND 8388608
  ),
  build_contract_raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(build_contract_raw_bytes) BETWEEN 1 AND 8388608
  ),
  admission_bundle jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(admission_bundle)='object'
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  FOREIGN KEY(precommit_id)
    REFERENCES workflow_v3_quality_completion_precommits(precommit_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE workflow_v3_quality_completion_material_manifests (
  precommit_id uuid NOT NULL,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 1023),
  manifest_id uuid NOT NULL,
  role text NOT NULL CHECK (
    role IN ('run','predecessor')
  ),
  raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(raw_bytes) BETWEEN 1 AND 8388608
  ),
  raw_bytes_hash text NOT NULL CHECK (
    raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  raw_bytes_size bigint NOT NULL CHECK (
    raw_bytes_size BETWEEN 1 AND 8388608
  ),
  PRIMARY KEY(precommit_id,ordinal),
  UNIQUE(precommit_id,role,manifest_id),
  FOREIGN KEY(precommit_id)
    REFERENCES workflow_v3_quality_completion_materials(precommit_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE workflow_v3_quality_completion_material_revisions (
  precommit_id uuid NOT NULL,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 2047),
  purpose text NOT NULL CHECK (
    pg_catalog.octet_length(purpose) BETWEEN 1 AND 256
    AND purpose=pg_catalog.btrim(purpose)
  ),
  revision_id uuid NOT NULL,
  raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(raw_bytes) BETWEEN 1 AND 16777216
  ),
  raw_bytes_hash text NOT NULL CHECK (
    raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  raw_bytes_size bigint NOT NULL CHECK (
    raw_bytes_size BETWEEN 1 AND 16777216
  ),
  PRIMARY KEY(precommit_id,ordinal),
  UNIQUE(precommit_id,purpose,revision_id),
  UNIQUE(precommit_id,revision_id),
  FOREIGN KEY(precommit_id)
    REFERENCES workflow_v3_quality_completion_materials(precommit_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE workflow_v3_quality_completion_material_review_receipts (
  precommit_id uuid NOT NULL,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 2047),
  review_request_id uuid NOT NULL,
  raw_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(raw_bytes) BETWEEN 1 AND 1048576
  ),
  raw_bytes_hash text NOT NULL CHECK (
    raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  raw_bytes_size bigint NOT NULL CHECK (
    raw_bytes_size BETWEEN 1 AND 1048576
  ),
  PRIMARY KEY(precommit_id,ordinal),
  UNIQUE(precommit_id,review_request_id),
  FOREIGN KEY(precommit_id)
    REFERENCES workflow_v3_quality_completion_materials(precommit_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE workflow_v3_quality_completion_candidate_snapshots (
  precommit_id uuid PRIMARY KEY,
  freeze_request_hash text NOT NULL CHECK (
    freeze_request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  freeze_request_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(freeze_request_bytes) BETWEEN 1 AND 65536
  ),
  workflow_input_hash text NOT NULL CHECK (
    workflow_input_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  workflow_input_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(workflow_input_bytes) BETWEEN 1 AND 33554432
  ),
  freeze_candidate_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(freeze_candidate_bytes) BETWEEN 1 AND 67108864
  ),
  snapshot_hash text NOT NULL UNIQUE CHECK (
    snapshot_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  retained_raw_bytes_size bigint NOT NULL CHECK (
    retained_raw_bytes_size BETWEEN 1 AND 134217728
  ),
  manifest_count integer NOT NULL CHECK (manifest_count BETWEEN 1 AND 1024),
  revision_count integer NOT NULL CHECK (revision_count BETWEEN 1 AND 2048),
  review_receipt_count integer NOT NULL CHECK (
    review_receipt_count BETWEEN 0 AND 2048
  ),
  material_bundle_hash text NOT NULL CHECK (
    material_bundle_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  FOREIGN KEY(precommit_id)
    REFERENCES workflow_v3_quality_completion_precommits(precommit_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE workflow_v3_quality_completion_identity_reservations (
  identity_value uuid PRIMARY KEY,
  identity_kind text NOT NULL CHECK (
    identity_kind IN (
      'precommit','workflow-input-operation','workflow-input-authority',
      'activation-event'
    )
  ),
  precommit_id uuid NOT NULL,
  reserved_at timestamptz NOT NULL CHECK (
    reserved_at=pg_catalog.date_trunc('milliseconds',reserved_at)
  ),
  UNIQUE(precommit_id,identity_kind),
  FOREIGN KEY(precommit_id)
    REFERENCES workflow_v3_quality_completion_precommits(precommit_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX workflow_v3_quality_completion_precommits_project_idx
  ON workflow_v3_quality_completion_precommits(
    project_id,quality_completed_at,precommit_id
  );

CREATE FUNCTION workflow_v3_quality_completion_runtime_caller_is_exact_v1(
  p_role text
)
RETURNS boolean
LANGUAGE sql
STABLE STRICT PARALLEL SAFE
SECURITY INVOKER
AS $function$
  SELECT qualification_input_precommit_caller_is_v1(p_role)
$function$;

CREATE FUNCTION inspect_workflow_v3_quality_completion_precommit_v1(
  p_precommit_id uuid
)
RETURNS SETOF workflow_v3_quality_completion_precommits
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_precommit workflow_v3_quality_completion_precommits%ROWTYPE;
BEGIN
  PERFORM workflow_v3_quality_completion_require_primary_v1();
  IF workflow_v3_quality_completion_runtime_caller_is_exact_v1(
       'worksflow_application'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality completion inspection caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF p_precommit_id IS NULL
     OR workflow_input_uuid_is_exact(p_precommit_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  SELECT * INTO v_precommit
  FROM workflow_v3_quality_completion_precommits
  WHERE precommit_id=p_precommit_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF workflow_v3_quality_completion_commit_is_exact_v1(
       p_precommit_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality completion commit is corrupt'
      USING ERRCODE='WQC02';
  END IF;
  RETURN NEXT v_precommit;
END;
$function$;

-- The retained candidate is useful only when the Workflow mutation that made
-- it authoritative committed in the same transaction.  This predicate is
-- intentionally usable both by deferred closure triggers and by the operator
-- resolver: it proves the immutable snapshot, the exact completion event and
-- outbox envelope, and the post-completion run/node state.
CREATE FUNCTION workflow_v3_quality_completion_commit_is_exact_v1(
  p_precommit_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_precommit workflow_v3_quality_completion_precommits%ROWTYPE;
  v_material workflow_v3_quality_completion_materials%ROWTYPE;
  v_snapshot workflow_v3_quality_completion_candidate_snapshots%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_quality workflow_node_runs%ROWTYPE;
  v_gate workflow_node_runs%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
  v_outbox outbox_events%ROWTYPE;
  v_gate_input jsonb;
  v_expected_outbox_payload jsonb;
BEGIN
  SELECT * INTO v_precommit
  FROM workflow_v3_quality_completion_precommits
  WHERE precommit_id=p_precommit_id;
  SELECT * INTO v_material
  FROM workflow_v3_quality_completion_materials
  WHERE precommit_id=p_precommit_id;
  SELECT * INTO v_snapshot
  FROM workflow_v3_quality_completion_candidate_snapshots
  WHERE precommit_id=p_precommit_id;
  IF v_precommit.precommit_id IS NULL OR v_material.precommit_id IS NULL
     OR v_snapshot.precommit_id IS NULL
     OR workflow_v3_quality_completion_snapshot_is_exact_v1(
          p_precommit_id
        ) IS NOT TRUE THEN
    RETURN false;
  END IF;
  BEGIN
    v_gate_input:=pg_catalog.convert_from(
      v_precommit.gate_input_raw_bytes,'UTF8'
    )::jsonb;
  EXCEPTION WHEN OTHERS THEN
    RETURN false;
  END;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=v_precommit.workflow_run_id;
  SELECT * INTO v_quality FROM workflow_node_runs
  WHERE id=v_precommit.quality_node_run_id;
  SELECT * INTO v_gate FROM workflow_node_runs
  WHERE id=v_precommit.gate_node_run_id;
  SELECT * INTO v_event FROM workflow_run_events
  WHERE id=v_precommit.completion_event_id;
  SELECT * INTO v_outbox FROM outbox_events
  WHERE id=v_precommit.completion_event_id;
  v_expected_outbox_payload:=pg_catalog.jsonb_build_object(
    'id',v_precommit.completion_event_id::text,
    'projectId',v_precommit.project_id::text,
    'runId',v_precommit.workflow_run_id::text,
    'sequence',v_precommit.completion_event_sequence,
    'type','node.completed',
    'nodeKey',v_precommit.quality_node_key,
    'occurredAt',workflow_v3_quality_completion_rfc3339_v1(
      v_precommit.quality_completed_at
    ),
    'payload',v_precommit.completion_event_payload
  )||CASE WHEN v_precommit.completion_event_actor_id IS NULL
      THEN '{}'::jsonb
      ELSE pg_catalog.jsonb_build_object(
        'actorId',v_precommit.completion_event_actor_id::text
      )
    END;
  IF v_run.id IS NULL OR v_run.project_id<>v_precommit.project_id
     OR v_run.execution_profile_version<>'workflow-engine/v3'
     OR v_run.execution_profile_hash<>
          '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR v_run.event_cursor<v_precommit.completion_event_sequence
     OR v_run.context#>ARRAY['nodes',v_precommit.gate_node_key,'input']
          IS DISTINCT FROM v_gate_input
     OR v_quality.id IS NULL
     OR v_quality.run_id<>v_precommit.workflow_run_id
     OR v_quality.node_key<>v_precommit.quality_node_key
     OR v_quality.node_type<>'quality_gate'
     OR v_quality.status<>'completed'
     OR v_quality.attempt<>v_precommit.quality_attempt
     OR v_quality.output_revision_id<>v_precommit.workspace_revision_id
     OR v_quality.lease_owner IS NOT NULL
     OR v_quality.lease_expires_at IS NOT NULL
     OR v_quality.completed_at<>v_precommit.quality_completed_at
     OR v_quality.failure IS NOT NULL
     OR v_gate.id IS NULL OR v_gate.run_id<>v_precommit.workflow_run_id
     OR v_gate.node_key<>v_precommit.gate_node_key
     OR v_gate.node_type<>'external_qualification_gate'
     OR v_gate.definition_node_id<>'external-qualification'
     OR v_gate.slice_kind<>'root' OR v_gate.slice_id IS NOT NULL
     OR v_event.id IS NULL OR v_event.run_id<>v_precommit.workflow_run_id
     OR v_event.sequence<>v_precommit.completion_event_sequence
     OR v_event.event_type<>'node.completed'
     OR v_event.node_key IS DISTINCT FROM v_precommit.quality_node_key
     OR v_event.payload<>v_precommit.completion_event_payload
     OR v_event.actor_id IS DISTINCT FROM
          v_precommit.completion_event_actor_id
     OR v_event.created_at<>v_precommit.quality_completed_at
     OR v_outbox.id IS NULL OR v_outbox.aggregate_type<>'workflow_run'
     OR v_outbox.aggregate_id<>v_precommit.workflow_run_id::text
     OR v_outbox.event_type<>'node.completed'
     OR v_outbox.subject<>'worksflow.workflow.run.event'
     OR v_outbox.payload<>v_expected_outbox_payload
     OR v_outbox.headers<>'{}'::jsonb
     OR v_outbox.created_at<>v_precommit.quality_completed_at THEN
    RETURN false;
  END IF;
  -- In the creation transaction the gate has not yet been activated.  Later
  -- lifecycle transitions are allowed, but its stable identity remains bound
  -- by the row-level guard below.
  IF v_snapshot.creation_transaction_id=
       pg_catalog.pg_current_xact_id()::text
     AND (v_run.event_cursor<>v_precommit.completion_event_sequence
       OR v_outbox.available_at<>v_precommit.quality_completed_at
       OR v_gate.status<>'pending' OR v_gate.attempt<>0
       OR v_gate.input_authority_id IS NOT NULL
       OR v_gate.input_manifest_id IS NOT NULL
       OR v_gate.output_proposal_id IS NOT NULL
       OR v_gate.output_revision_id IS NOT NULL
       OR v_gate.lease_owner IS NOT NULL
       OR v_gate.lease_expires_at IS NOT NULL
       OR v_gate.started_at IS NOT NULL OR v_gate.completed_at IS NOT NULL
       OR v_gate.failure IS NOT NULL) THEN
    RETURN false;
  END IF;
  RETURN true;
EXCEPTION WHEN OTHERS THEN
  RETURN false;
END;
$function$;

CREATE FUNCTION guard_workflow_v3_quality_completion_workflow_mutation_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_precommit workflow_v3_quality_completion_precommits%ROWTYPE;
  v_snapshot_tx text;
  v_gate_input jsonb;
BEGIN
  IF TG_TABLE_NAME='workflow_runs' THEN
    SELECT * INTO v_precommit
    FROM workflow_v3_quality_completion_precommits
    WHERE workflow_run_id=OLD.id;
    IF NOT FOUND THEN RETURN NEW; END IF;
    SELECT creation_transaction_id INTO v_snapshot_tx
    FROM workflow_v3_quality_completion_candidate_snapshots
    WHERE precommit_id=v_precommit.precommit_id;
    BEGIN
      v_gate_input:=pg_catalog.convert_from(
        v_precommit.gate_input_raw_bytes,'UTF8'
      )::jsonb;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'v3 Quality completion gate input is corrupt'
        USING ERRCODE='WQC02';
    END;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.execution_profile_version IS DISTINCT FROM
            OLD.execution_profile_version
       OR NEW.execution_profile_hash IS DISTINCT FROM
            OLD.execution_profile_hash THEN
      RAISE EXCEPTION 'v3 Quality completion run authority is immutable'
        USING ERRCODE='WQC02';
    END IF;
    IF v_snapshot_tx=pg_catalog.pg_current_xact_id()::text THEN
      IF OLD.event_cursor<>v_precommit.expected_run_cursor
         OR NEW.event_cursor<>v_precommit.completion_event_sequence
         OR NEW.context#>ARRAY['nodes',v_precommit.gate_node_key,'input']
              IS DISTINCT FROM v_gate_input THEN
        RAISE EXCEPTION 'v3 Quality completion run transition is not exact'
          USING ERRCODE='WQC02';
      END IF;
    ELSIF NEW.event_cursor<v_precommit.completion_event_sequence
       OR NEW.context#>ARRAY['nodes',v_precommit.gate_node_key,'input']
            IS DISTINCT FROM
          OLD.context#>ARRAY['nodes',v_precommit.gate_node_key,'input'] THEN
      RAISE EXCEPTION 'v3 Quality completion run authority is immutable'
        USING ERRCODE='WQC02';
    END IF;
    RETURN NEW;
  END IF;
  SELECT * INTO v_precommit
  FROM workflow_v3_quality_completion_precommits
  WHERE quality_node_run_id=OLD.id OR gate_node_run_id=OLD.id;
  IF NOT FOUND THEN RETURN NEW; END IF;
  IF OLD.id=v_precommit.quality_node_run_id THEN
    SELECT creation_transaction_id INTO v_snapshot_tx
    FROM workflow_v3_quality_completion_candidate_snapshots
    WHERE precommit_id=v_precommit.precommit_id;
    IF v_snapshot_tx=pg_catalog.pg_current_xact_id()::text THEN
      IF OLD.run_id<>v_precommit.workflow_run_id
         OR OLD.node_key<>v_precommit.quality_node_key
         OR OLD.node_type<>'quality_gate' OR OLD.status<>'running'
         OR OLD.attempt<>v_precommit.quality_attempt
         OR OLD.lease_owner<>v_precommit.quality_lease_owner
         OR OLD.lease_expires_at IS NULL
         OR OLD.lease_expires_at<v_precommit.quality_completed_at
         OR OLD.output_revision_id IS NOT NULL
         OR OLD.completed_at IS NOT NULL OR OLD.failure IS NOT NULL
         OR NEW.id IS DISTINCT FROM OLD.id
         OR NEW.run_id IS DISTINCT FROM OLD.run_id
         OR NEW.node_key IS DISTINCT FROM OLD.node_key
         OR NEW.node_type IS DISTINCT FROM OLD.node_type
         OR NEW.definition_node_id IS DISTINCT FROM OLD.definition_node_id
         OR NEW.slice_kind IS DISTINCT FROM OLD.slice_kind
         OR NEW.slice_id IS DISTINCT FROM OLD.slice_id
         OR NEW.status<>'completed'
         OR NEW.attempt<>v_precommit.quality_attempt
         OR NEW.output_revision_id<>v_precommit.workspace_revision_id
         OR NEW.lease_owner IS NOT NULL
         OR NEW.lease_expires_at IS NOT NULL
         OR NEW.completed_at<>v_precommit.quality_completed_at
         OR NEW.failure IS NOT NULL THEN
        RAISE EXCEPTION 'v3 Quality completion source transition is not exact'
          USING ERRCODE='WQC02';
      END IF;
    ELSIF NEW IS DISTINCT FROM OLD THEN
      RAISE EXCEPTION 'v3 Quality completion source node is immutable'
        USING ERRCODE='WQC02';
    END IF;
  ELSIF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.run_id IS DISTINCT FROM OLD.run_id
     OR NEW.node_key IS DISTINCT FROM OLD.node_key
     OR NEW.node_type IS DISTINCT FROM OLD.node_type
     OR NEW.definition_node_id IS DISTINCT FROM OLD.definition_node_id
     OR NEW.slice_kind IS DISTINCT FROM OLD.slice_kind
     OR NEW.slice_id IS DISTINCT FROM OLD.slice_id THEN
    RAISE EXCEPTION 'v3 Quality completion gate identity is immutable'
      USING ERRCODE='WQC02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER workflow_v3_quality_completion_run_mutation_guard
BEFORE UPDATE ON workflow_runs FOR EACH ROW
EXECUTE FUNCTION guard_workflow_v3_quality_completion_workflow_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_node_mutation_guard
BEFORE UPDATE ON workflow_node_runs FOR EACH ROW
EXECUTE FUNCTION guard_workflow_v3_quality_completion_workflow_mutation_v1();

CREATE FUNCTION guard_workflow_v3_quality_completion_event_mutation_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE v_bound boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM workflow_v3_quality_completion_precommits
    WHERE completion_event_id=OLD.id
  ) INTO v_bound;
  IF NOT v_bound THEN RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END; END IF;
  IF TG_TABLE_NAME='workflow_run_events' OR TG_OP='DELETE' THEN
    RAISE EXCEPTION 'v3 Quality completion event is immutable'
      USING ERRCODE='WQC02';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.aggregate_type IS DISTINCT FROM OLD.aggregate_type
     OR NEW.aggregate_id IS DISTINCT FROM OLD.aggregate_id
     OR NEW.event_type IS DISTINCT FROM OLD.event_type
     OR NEW.subject IS DISTINCT FROM OLD.subject
     OR NEW.payload IS DISTINCT FROM OLD.payload
     OR NEW.headers IS DISTINCT FROM OLD.headers
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'v3 Quality completion outbox identity is immutable'
      USING ERRCODE='WQC02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER workflow_v3_quality_completion_event_immutable
BEFORE UPDATE OR DELETE ON workflow_run_events FOR EACH ROW
EXECUTE FUNCTION guard_workflow_v3_quality_completion_event_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_outbox_immutable
BEFORE UPDATE OR DELETE ON outbox_events FOR EACH ROW
EXECUTE FUNCTION guard_workflow_v3_quality_completion_event_mutation_v1();

CREATE FUNCTION validate_workflow_v3_quality_completion_closure_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_precommit_id uuid;
  v_ids uuid[]:=ARRAY[]::uuid[];
BEGIN
  IF TG_TABLE_NAME IN (
    'workflow_v3_quality_completion_precommits',
    'workflow_v3_quality_completion_materials',
    'workflow_v3_quality_completion_material_manifests',
    'workflow_v3_quality_completion_material_revisions',
    'workflow_v3_quality_completion_material_review_receipts',
    'workflow_v3_quality_completion_candidate_snapshots',
    'workflow_v3_quality_completion_identity_reservations'
  ) THEN
    v_ids:=ARRAY[CASE WHEN TG_OP='DELETE' THEN OLD.precommit_id
                     ELSE NEW.precommit_id END];
  ELSIF TG_TABLE_NAME='workflow_runs' THEN
    SELECT COALESCE(pg_catalog.array_agg(precommit_id),ARRAY[]::uuid[])
    INTO v_ids FROM workflow_v3_quality_completion_precommits
    WHERE workflow_run_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  ELSIF TG_TABLE_NAME='workflow_node_runs' THEN
    SELECT COALESCE(pg_catalog.array_agg(precommit_id),ARRAY[]::uuid[])
    INTO v_ids FROM workflow_v3_quality_completion_precommits
    WHERE quality_node_run_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END
       OR gate_node_run_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  ELSE
    SELECT COALESCE(pg_catalog.array_agg(precommit_id),ARRAY[]::uuid[])
    INTO v_ids FROM workflow_v3_quality_completion_precommits
    WHERE completion_event_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  END IF;
  FOREACH v_precommit_id IN ARRAY v_ids LOOP
    IF workflow_v3_quality_completion_commit_is_exact_v1(
         v_precommit_id
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'v3 Quality completion transaction closure is incomplete'
        USING ERRCODE='WQC04';
    END IF;
  END LOOP;
  RETURN NULL;
END;
$function$;

CREATE CONSTRAINT TRIGGER workflow_v3_quality_precommit_exact_closure
AFTER INSERT ON workflow_v3_quality_completion_precommits
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_material_exact_closure
AFTER INSERT ON workflow_v3_quality_completion_materials
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_manifest_exact_closure
AFTER INSERT ON workflow_v3_quality_completion_material_manifests
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_revision_exact_closure
AFTER INSERT ON workflow_v3_quality_completion_material_revisions
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_review_exact_closure
AFTER INSERT ON workflow_v3_quality_completion_material_review_receipts
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_snapshot_exact_closure
AFTER INSERT ON workflow_v3_quality_completion_candidate_snapshots
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_reservation_exact_closure
AFTER INSERT ON workflow_v3_quality_completion_identity_reservations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_run_exact_closure
AFTER UPDATE OR DELETE ON workflow_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_node_exact_closure
AFTER UPDATE OR DELETE ON workflow_node_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_event_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_run_events
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_v3_quality_outbox_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON outbox_events
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_v3_quality_completion_closure_v1();

CREATE FUNCTION freeze_workflow_input_authority_from_quality_precommit_v1(
  p_operation_id uuid,
  p_authority_id uuid,
  p_workflow_run_id uuid,
  p_node_run_id uuid,
  p_expected_run_cursor bigint,
  p_activation_event_id uuid,
  p_activation_event_sequence bigint,
  p_definition_raw_bytes bytea,
  p_run_scope_raw_bytes bytea,
  p_node_input_raw_bytes bytea,
  p_build_manifest_raw_bytes bytea,
  p_build_contract_raw_bytes bytea,
  p_candidate jsonb
)
RETURNS SETOF workflow_input_authorities
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_precommit workflow_v3_quality_completion_precommits%ROWTYPE;
  v_material workflow_v3_quality_completion_materials%ROWTYPE;
  v_snapshot workflow_v3_quality_completion_candidate_snapshots%ROWTYPE;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  PERFORM workflow_v3_quality_completion_require_primary_v1();
  IF workflow_v3_quality_completion_runtime_caller_is_exact_v1(
       'worksflow_application'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input Quality-precommit freeze caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF pg_catalog.current_setting('transaction_isolation')<>'serializable' THEN
    RAISE EXCEPTION 'Workflow Input Quality-precommit freeze requires SERIALIZABLE isolation'
      USING ERRCODE='WIA04';
  END IF;
  SELECT * INTO v_precommit
  FROM workflow_v3_quality_completion_precommits
  WHERE workflow_input_operation_id=p_operation_id
     OR workflow_input_authority_id=p_authority_id
     OR (workflow_run_id=p_workflow_run_id
       AND gate_node_run_id=p_node_run_id);
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Workflow Input has no immutable Quality completion precommit'
      USING ERRCODE='WIA04';
  END IF;
  SELECT * INTO v_material
  FROM workflow_v3_quality_completion_materials
  WHERE precommit_id=v_precommit.precommit_id;
  SELECT * INTO v_snapshot
  FROM workflow_v3_quality_completion_candidate_snapshots
  WHERE precommit_id=v_precommit.precommit_id;
  IF workflow_v3_quality_completion_snapshot_is_exact_v1(
       v_precommit.precommit_id
     ) IS NOT TRUE
     OR v_precommit.workflow_input_operation_id<>p_operation_id
     OR v_precommit.workflow_input_authority_id<>p_authority_id
     OR v_precommit.workflow_run_id<>p_workflow_run_id
     OR v_precommit.gate_node_run_id<>p_node_run_id
     OR v_precommit.completion_event_sequence<>p_expected_run_cursor
     OR v_precommit.activation_event_id<>p_activation_event_id
     OR p_activation_event_sequence<>p_expected_run_cursor+1
     OR v_material.definition_raw_bytes<>p_definition_raw_bytes
     OR v_material.run_scope_raw_bytes<>p_run_scope_raw_bytes
     OR v_material.node_input_raw_bytes<>p_node_input_raw_bytes
     OR v_material.build_manifest_raw_bytes<>p_build_manifest_raw_bytes
     OR v_material.build_contract_raw_bytes<>p_build_contract_raw_bytes
     OR workflow_input_canonical_jsonb_bytes(p_candidate)<>
          v_snapshot.freeze_candidate_bytes THEN
    RAISE EXCEPTION 'Workflow Input Quality-precommit replay differs from frozen bytes'
      USING ERRCODE='WIA01';
  END IF;
  RETURN QUERY
  SELECT * FROM freeze_workflow_input_authority_v1(
    p_operation_id,p_authority_id,p_workflow_run_id,p_node_run_id,
    p_expected_run_cursor,p_activation_event_id,p_activation_event_sequence,
    p_definition_raw_bytes,p_run_scope_raw_bytes,p_node_input_raw_bytes,
    p_build_manifest_raw_bytes,p_build_contract_raw_bytes,p_candidate
  );
END;
$function$;

CREATE FUNCTION resolve_workflow_v3_quality_completion_candidate_v1(
  p_completion_event_id uuid
)
RETURNS TABLE(
  classification text,
  completion_event_id uuid,
  precommit_id uuid,
  freeze_request_hash text,
  freeze_request_bytes bytea,
  workflow_input_hash text,
  workflow_input_bytes bytea,
  freeze_candidate_bytes bytea,
  definition_raw_bytes bytea,
  run_scope_raw_bytes bytea,
  node_input_raw_bytes bytea,
  build_manifest_raw_bytes bytea,
  build_contract_raw_bytes bytea,
  material_bundle jsonb,
  snapshot_hash text,
  retained_raw_bytes_size bigint
)
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_precommit workflow_v3_quality_completion_precommits%ROWTYPE;
  v_material workflow_v3_quality_completion_materials%ROWTYPE;
  v_snapshot workflow_v3_quality_completion_candidate_snapshots%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
BEGIN
  IF workflow_v3_quality_completion_runtime_caller_is_exact_v1(
       'worksflow_workflow_input_authority_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality completion resolver caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF p_completion_event_id IS NULL
     OR workflow_input_uuid_is_exact(p_completion_event_id::text) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality completion resolver identity is invalid'
      USING ERRCODE='WQC01';
  END IF;
  SELECT * INTO v_precommit
  FROM workflow_v3_quality_completion_precommits AS bound_precommit
  WHERE bound_precommit.completion_event_id=p_completion_event_id;
  IF FOUND THEN
    IF workflow_v3_quality_completion_commit_is_exact_v1(
         v_precommit.precommit_id
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'v3 Quality completion candidate snapshot is corrupt'
        USING ERRCODE='WQC02';
    END IF;
    SELECT * INTO v_material
    FROM workflow_v3_quality_completion_materials AS bound_material
    WHERE bound_material.precommit_id=v_precommit.precommit_id;
    SELECT * INTO v_snapshot
    FROM workflow_v3_quality_completion_candidate_snapshots AS bound_snapshot
    WHERE bound_snapshot.precommit_id=v_precommit.precommit_id;
    RETURN QUERY SELECT
      'target'::text,v_precommit.completion_event_id,
      v_precommit.precommit_id,v_snapshot.freeze_request_hash,
      v_snapshot.freeze_request_bytes,v_snapshot.workflow_input_hash,
      v_snapshot.workflow_input_bytes,v_snapshot.freeze_candidate_bytes,
      v_material.definition_raw_bytes,v_material.run_scope_raw_bytes,
      v_material.node_input_raw_bytes,v_material.build_manifest_raw_bytes,
      v_material.build_contract_raw_bytes,
      workflow_v3_quality_completion_material_bundle_v1(
        v_precommit.precommit_id
      ),v_snapshot.snapshot_hash,v_snapshot.retained_raw_bytes_size;
    RETURN;
  END IF;

  SELECT * INTO v_event FROM workflow_run_events
  WHERE id=p_completion_event_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF v_event.event_type<>'node.completed' OR v_event.node_key IS NULL THEN
    RETURN QUERY SELECT
      'non-target'::text,p_completion_event_id,
      NULL::uuid,NULL::text,NULL::bytea,NULL::text,NULL::bytea,NULL::bytea,
      NULL::bytea,NULL::bytea,NULL::bytea,NULL::bytea,NULL::bytea,NULL::jsonb,
      NULL::text,NULL::bigint;
    RETURN;
  END IF;
  SELECT * INTO v_run FROM workflow_runs WHERE id=v_event.run_id;
  SELECT * INTO v_node FROM workflow_node_runs
  WHERE run_id=v_event.run_id AND node_key=v_event.node_key;
  IF v_run.id IS NULL OR v_node.id IS NULL
     OR v_node.run_id<>v_event.run_id
     OR v_event.sequence<1 OR v_event.sequence>v_run.event_cursor THEN
    RAISE EXCEPTION 'v3 Quality resolver event identity is corrupt'
      USING ERRCODE='WQC02';
  END IF;
  IF v_run.execution_profile_version='workflow-engine/v3'
     AND v_run.execution_profile_hash=
       '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     AND v_node.node_type='quality_gate' THEN
    RAISE EXCEPTION 'v3 Quality completion is missing its immutable precommit'
      USING ERRCODE='WQC04';
  END IF;
  RETURN QUERY SELECT
    'non-target'::text,p_completion_event_id,
    NULL::uuid,NULL::text,NULL::bytea,NULL::text,NULL::bytea,NULL::bytea,
    NULL::bytea,NULL::bytea,NULL::bytea,NULL::bytea,NULL::bytea,NULL::jsonb,
    NULL::text,NULL::bigint;
END;
$function$;

CREATE FUNCTION precommit_workflow_v3_quality_completion_v1(
  p_precommit_id uuid,
  p_workflow_input_operation_id uuid,
  p_workflow_input_authority_id uuid,
  p_activation_event_id uuid,
  p_workflow_run_id uuid,
  p_quality_node_run_id uuid,
  p_gate_node_run_id uuid,
  p_expected_run_cursor bigint,
  p_completion_event_id uuid,
  p_quality_lease_owner text,
  p_quality_attempt integer,
  p_quality_completed_at timestamptz,
  p_completion_event_payload jsonb,
  p_completion_event_actor_id uuid,
  p_gate_input_raw_bytes bytea
)
RETURNS SETOF workflow_v3_quality_completion_precommits
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_existing workflow_v3_quality_completion_precommits%ROWTYPE;
  v_material workflow_v3_quality_completion_materials%ROWTYPE;
  v_project projects%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_quality_node workflow_node_runs%ROWTYPE;
  v_gate_node workflow_node_runs%ROWTYPE;
  v_definition workflow_definition_versions%ROWTYPE;
  v_run_manifest input_manifests%ROWTYPE;
  v_target_revision artifact_revisions%ROWTYPE;
  v_target_artifact artifacts%ROWTYPE;
  v_proposal implementation_proposals%ROWTYPE;
  v_build_manifest application_build_manifests%ROWTYPE;
  v_build_contract application_build_contracts%ROWTYPE;
  v_policy qualification_policy_authorities%ROWTYPE;
  v_probe workflow_input_authorities%ROWTYPE;
  v_node_input jsonb;
  v_binding jsonb;
  v_quality_output jsonb;
  v_candidate_manifests jsonb;
  v_candidate_revisions jsonb;
  v_review_requirements jsonb;
  v_candidate jsonb;
  v_candidate_bytes bytea;
  v_material_bundle jsonb;
  v_material_bundle_hash text;
  v_snapshot_document jsonb;
  v_snapshot_hash text;
  v_target_revision_id uuid;
  v_quality_run_id uuid;
  v_project_id uuid;
  v_retained_bytes bigint;
  v_manifest_count integer;
  v_revision_count integer;
  v_receipt_count integer;
  v_count integer;
  v_now timestamptz;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  PERFORM workflow_v3_quality_completion_require_primary_v1();
  IF workflow_v3_quality_completion_runtime_caller_is_exact_v1(
       'worksflow_application'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality completion precommit caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF pg_catalog.current_setting('transaction_isolation')<>'serializable' THEN
    RAISE EXCEPTION 'v3 Quality completion precommit requires SERIALIZABLE isolation'
      USING ERRCODE='WQC03';
  END IF;
  IF p_precommit_id IS NULL
     OR workflow_input_uuid_is_exact(p_precommit_id::text) IS NOT TRUE
     OR p_workflow_input_operation_id IS NULL
     OR workflow_input_uuid_is_exact(
          p_workflow_input_operation_id::text
        ) IS NOT TRUE
     OR p_workflow_input_authority_id IS NULL
     OR workflow_input_uuid_is_exact(
          p_workflow_input_authority_id::text
        ) IS NOT TRUE
     OR p_activation_event_id IS NULL
     OR workflow_input_uuid_is_exact(p_activation_event_id::text) IS NOT TRUE
     OR p_workflow_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_quality_node_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_quality_node_run_id::text) IS NOT TRUE
     OR p_gate_node_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_gate_node_run_id::text) IS NOT TRUE
     OR p_completion_event_id IS NULL
     OR workflow_input_uuid_is_exact(
          p_completion_event_id::text
        ) IS NOT TRUE
     OR p_expected_run_cursor IS NULL
     OR p_expected_run_cursor NOT BETWEEN 0 AND 9007199254740990
     OR p_quality_attempt IS NULL OR p_quality_attempt<1
     OR p_quality_lease_owner IS NULL
     OR pg_catalog.octet_length(p_quality_lease_owner) NOT BETWEEN 1 AND 256
     OR p_quality_lease_owner<>pg_catalog.btrim(p_quality_lease_owner)
     OR p_quality_completed_at IS NULL
     OR p_quality_completed_at<>
          pg_catalog.date_trunc('milliseconds',p_quality_completed_at)
     OR p_completion_event_payload IS NULL
     OR pg_catalog.jsonb_typeof(p_completion_event_payload)<>'object'
     OR p_completion_event_payload->'attempt' IS DISTINCT FROM
          pg_catalog.to_jsonb(p_quality_attempt)
     OR pg_catalog.octet_length(p_completion_event_payload::text)>1048576
     OR p_gate_input_raw_bytes IS NULL
     OR pg_catalog.octet_length(p_gate_input_raw_bytes)
          NOT BETWEEN 1 AND 16777216 THEN
    RAISE EXCEPTION 'v3 Quality completion precommit argument is invalid'
      USING ERRCODE='WQC01';
  END IF;
  IF (SELECT pg_catalog.count(DISTINCT value)
      FROM pg_catalog.unnest(ARRAY[
        p_precommit_id,p_workflow_input_operation_id,
        p_workflow_input_authority_id,p_activation_event_id,
        p_workflow_run_id,p_quality_node_run_id,p_gate_node_run_id,
        p_completion_event_id
      ]) AS identity(value))<>8 THEN
    RAISE EXCEPTION 'v3 Quality completion identities alias each other'
      USING ERRCODE='WQC01';
  END IF;

  SELECT pg_catalog.count(*) INTO v_count
  FROM workflow_v3_quality_completion_precommits
  WHERE precommit_id=p_precommit_id
     OR workflow_input_operation_id=p_workflow_input_operation_id
     OR workflow_input_authority_id=p_workflow_input_authority_id
     OR activation_event_id=p_activation_event_id
     OR completion_event_id=p_completion_event_id
     OR (workflow_run_id=p_workflow_run_id
       AND quality_node_run_id=p_quality_node_run_id)
     OR (workflow_run_id=p_workflow_run_id
       AND gate_node_run_id=p_gate_node_run_id);
  IF v_count>1 THEN
    RAISE EXCEPTION 'v3 Quality completion identities span snapshots'
      USING ERRCODE='WQC02';
  ELSIF v_count=1 THEN
    SELECT * INTO v_existing
    FROM workflow_v3_quality_completion_precommits
    WHERE precommit_id=p_precommit_id
       OR workflow_input_operation_id=p_workflow_input_operation_id
       OR workflow_input_authority_id=p_workflow_input_authority_id
       OR activation_event_id=p_activation_event_id
       OR completion_event_id=p_completion_event_id
       OR (workflow_run_id=p_workflow_run_id
         AND quality_node_run_id=p_quality_node_run_id)
       OR (workflow_run_id=p_workflow_run_id
         AND gate_node_run_id=p_gate_node_run_id);
    IF v_existing.precommit_id<>p_precommit_id
       OR v_existing.workflow_input_operation_id<>
            p_workflow_input_operation_id
       OR v_existing.workflow_input_authority_id<>
            p_workflow_input_authority_id
       OR v_existing.activation_event_id<>p_activation_event_id
       OR v_existing.workflow_run_id<>p_workflow_run_id
       OR v_existing.quality_node_run_id<>p_quality_node_run_id
       OR v_existing.gate_node_run_id<>p_gate_node_run_id
       OR v_existing.expected_run_cursor<>p_expected_run_cursor
       OR v_existing.completion_event_id<>p_completion_event_id
       OR v_existing.completion_event_payload<>p_completion_event_payload
       OR v_existing.completion_event_actor_id IS DISTINCT FROM
            p_completion_event_actor_id
       OR v_existing.quality_completed_at<>p_quality_completed_at
       OR v_existing.quality_lease_owner<>p_quality_lease_owner
       OR v_existing.quality_attempt<>p_quality_attempt
       OR v_existing.gate_input_raw_bytes<>p_gate_input_raw_bytes
       OR workflow_v3_quality_completion_snapshot_is_exact_v1(
            v_existing.precommit_id
          ) IS NOT TRUE THEN
      RAISE EXCEPTION 'v3 Quality completion replay differs from immutable snapshot'
        USING ERRCODE='WQC02';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  SELECT * INTO v_material
  FROM workflow_v3_quality_completion_materials
  WHERE precommit_id=p_precommit_id
     OR completion_event_id=p_completion_event_id;
  IF NOT FOUND OR v_material.precommit_id<>p_precommit_id
     OR v_material.completion_event_id<>p_completion_event_id
     OR v_material.node_input_raw_bytes<>p_gate_input_raw_bytes
     OR v_material.creation_transaction_id<>
          pg_catalog.pg_current_xact_id()::text THEN
    RAISE EXCEPTION 'v3 Quality completion has no same-transaction material admission'
      USING ERRCODE='WQC04';
  END IF;

  SELECT project_id INTO v_project_id
  FROM workflow_runs WHERE id=p_workflow_run_id;
  IF v_project_id IS NULL THEN
    RAISE EXCEPTION 'v3 Quality completion run is unavailable'
      USING ERRCODE='WQC03';
  END IF;
  SELECT * INTO v_project FROM projects
  WHERE id=v_project_id FOR UPDATE;
  IF NOT FOUND OR v_project.lifecycle<>'active' THEN
    RAISE EXCEPTION 'v3 Quality completion project is unavailable'
      USING ERRCODE='WQC03';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'delivery_slices:'||v_project.id::text,0
    )
  );
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=p_workflow_run_id FOR UPDATE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id=p_workflow_run_id ORDER BY id FOR UPDATE;
  SELECT * INTO v_quality_node FROM workflow_node_runs
  WHERE id=p_quality_node_run_id;
  SELECT * INTO v_gate_node FROM workflow_node_runs
  WHERE id=p_gate_node_run_id;
  v_now:=pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  IF v_run.id IS NULL OR v_run.project_id<>v_project.id
     OR v_run.event_cursor<>p_expected_run_cursor
     OR v_run.status NOT IN ('running','waiting_input','waiting_review')
     OR v_run.started_at IS NULL
     OR v_run.execution_profile_version<>'workflow-engine/v3'
     OR v_run.execution_profile_hash<>
          '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR v_quality_node.id IS NULL OR v_quality_node.run_id<>v_run.id
     OR v_quality_node.node_type<>'quality_gate'
     OR v_quality_node.status<>'running'
     OR v_quality_node.lease_owner<>p_quality_lease_owner
     OR v_quality_node.attempt<>p_quality_attempt
     OR v_quality_node.lease_expires_at IS NULL
     OR v_quality_node.lease_expires_at<p_quality_completed_at
     OR v_quality_node.output_revision_id IS NOT NULL
     OR v_quality_node.completed_at IS NOT NULL
     OR v_quality_node.failure IS NOT NULL
     OR v_gate_node.id IS NULL OR v_gate_node.run_id<>v_run.id
     OR v_gate_node.node_type<>'external_qualification_gate'
     OR v_gate_node.definition_node_id<>'external-qualification'
     OR v_gate_node.node_key<>'external-qualification'
     OR v_gate_node.status<>'pending' OR v_gate_node.attempt<>0
     OR v_gate_node.slice_kind<>'root' OR v_gate_node.slice_id IS NOT NULL
     OR v_gate_node.input_authority_id IS NOT NULL
     OR v_gate_node.input_manifest_id IS NOT NULL
     OR v_gate_node.output_proposal_id IS NOT NULL
     OR v_gate_node.output_revision_id IS NOT NULL
     OR v_gate_node.lease_owner IS NOT NULL
     OR v_gate_node.lease_expires_at IS NOT NULL
     OR v_gate_node.started_at IS NOT NULL
     OR v_gate_node.completed_at IS NOT NULL
     OR v_gate_node.failure IS NOT NULL
     OR p_quality_completed_at<v_quality_node.started_at
     OR p_quality_completed_at>v_now+INTERVAL '5 minutes' THEN
    RAISE EXCEPTION 'v3 Quality completion run/node/lease is stale'
      USING ERRCODE='WQC03';
  END IF;

  SELECT * INTO v_definition FROM workflow_definition_versions
  WHERE id=v_run.definition_version_id FOR SHARE;
  SELECT * INTO v_run_manifest FROM input_manifests
  WHERE id=v_run.input_manifest_id FOR SHARE;
  BEGIN
    v_node_input:=pg_catalog.convert_from(
      p_gate_input_raw_bytes,'UTF8'
    )::jsonb;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'v3 Quality gate input is not UTF-8 JSON'
      USING ERRCODE='WQC01';
  END;
  IF v_definition.id IS NULL OR v_definition.published IS NOT TRUE
     OR v_definition.execution_profile_version<>
          v_run.execution_profile_version
     OR v_definition.execution_profile_hash<>v_run.execution_profile_hash
     OR pg_catalog.convert_from(
          v_material.definition_raw_bytes,'UTF8'
        )::jsonb<>v_definition.content
     OR pg_catalog.convert_from(
          v_material.run_scope_raw_bytes,'UTF8'
        )::jsonb<>v_run.scope
     OR v_run_manifest.id IS NULL
     OR v_run_manifest.project_id<>v_project.id
     OR pg_catalog.jsonb_typeof(v_node_input)<>'object'
     OR v_node_input - ARRAY['bindings','hash']<>'{}'::jsonb
     OR NOT (v_node_input ?& ARRAY['bindings','hash'])
     OR pg_catalog.jsonb_typeof(v_node_input->'bindings')<>'array'
     OR pg_catalog.jsonb_array_length(v_node_input->'bindings')<>1
     OR workflow_input_normalize_sha256(v_node_input->>'hash')
          !~ '^sha256:[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'v3 Quality definition, scope, manifest, or gate input drifted'
      USING ERRCODE='WQC03';
  END IF;
  v_binding:=v_node_input->'bindings'->0;
  v_quality_output:=v_binding->'value';
  IF pg_catalog.jsonb_typeof(v_binding)<>'object'
     OR v_binding->'output' IS DISTINCT FROM v_binding->'value'
     OR COALESCE(v_binding->'mapping','[]'::jsonb)<>'[]'::jsonb
     OR v_binding#>>'{source,runId}'<>v_run.id::text
     OR v_binding#>>'{source,nodeKey}'<>v_quality_node.node_key
     OR v_binding#>>'{source,definitionNodeId}'<>
          v_quality_node.definition_node_id
     OR v_binding->>'fromPort'<>'default'
     OR v_binding->>'toPort'<>'default'
     OR pg_catalog.jsonb_typeof(v_quality_output)<>'object'
     OR v_quality_output->'passed'<>'true'::jsonb
     OR v_quality_output - ARRAY[
       'passed','findings','qualityRunId','workspaceRevision','buildManifest'
     ]<>'{}'::jsonb
     OR NOT (v_quality_output ?& ARRAY[
       'passed','findings','qualityRunId','workspaceRevision','buildManifest'
     ])
     OR workflow_input_uuid_is_exact(
          v_quality_output->>'qualityRunId'
        ) IS NOT TRUE
     OR workflow_input_uuid_is_exact(
          v_quality_output#>>'{workspaceRevision,revisionId}'
        ) IS NOT TRUE
     OR v_binding#>>'{source,outputRevisionId}'<>
          v_quality_output#>>'{workspaceRevision,revisionId}'
     OR workflow_input_normalize_sha256(
          v_quality_output#>>'{workspaceRevision,contentHash}'
        ) !~ '^sha256:[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'v3 Quality completion does not bind one exact passing result'
      USING ERRCODE='WQC01';
  END IF;
  v_target_revision_id:=(
    v_quality_output#>>'{workspaceRevision,revisionId}'
  )::uuid;
  v_quality_run_id:=(v_quality_output->>'qualityRunId')::uuid;

  PERFORM 1 FROM artifacts
  WHERE id IN (
    SELECT revision.artifact_id FROM artifact_revisions AS revision
    WHERE revision.id=v_target_revision_id
       OR revision.id IN (
         SELECT revision_id
         FROM workflow_v3_quality_completion_material_revisions
         WHERE precommit_id=p_precommit_id
       )
  ) ORDER BY id FOR UPDATE;
  PERFORM 1 FROM artifact_revisions
  WHERE id=v_target_revision_id
     OR id IN (
       SELECT revision_id
       FROM workflow_v3_quality_completion_material_revisions
       WHERE precommit_id=p_precommit_id
     )
  ORDER BY id FOR UPDATE;
  SELECT * INTO v_target_revision FROM artifact_revisions
  WHERE id=v_target_revision_id;
  SELECT * INTO v_target_artifact FROM artifacts
  WHERE id=v_target_revision.artifact_id;
  SELECT * INTO v_proposal FROM implementation_proposals
  WHERE id=v_target_revision.implementation_proposal_id FOR SHARE;
  SELECT * INTO v_build_manifest FROM application_build_manifests
  WHERE id=v_proposal.build_manifest_id FOR UPDATE;
  SELECT * INTO v_build_contract FROM application_build_contracts
  WHERE id=v_proposal.application_build_contract_id FOR UPDATE;
  SELECT * INTO v_policy FROM qualification_policy_authorities
  WHERE project_id=v_project.id
    AND execution_profile_version=v_run.execution_profile_version
    AND execution_profile_hash=workflow_input_normalize_sha256(
      v_run.execution_profile_hash
    )
  ORDER BY generation DESC LIMIT 1 FOR UPDATE;
  IF v_target_revision.id IS NULL OR v_target_artifact.id IS NULL
     OR v_target_artifact.project_id<>v_project.id
     OR v_target_artifact.kind<>'workspace'
     OR v_target_artifact.lifecycle<>'active'
     OR v_target_revision.workflow_status<>'approved'
     OR v_target_revision.superseded_at IS NOT NULL
     OR workflow_input_normalize_sha256(v_target_revision.content_hash)<>
          workflow_input_normalize_sha256(
            v_quality_output#>>'{workspaceRevision,contentHash}'
          )
     OR v_proposal.id IS NULL OR v_proposal.project_id<>v_project.id
     OR v_proposal.status NOT IN ('applied','partially_applied')
     OR v_proposal.application_build_contract_id IS NULL
     OR v_build_manifest.id IS NULL OR v_build_contract.id IS NULL
     OR v_build_manifest.project_id<>v_project.id
     OR v_build_contract.project_id<>v_project.id
     OR v_policy.authority_id IS NULL OR v_policy.status<>'active'
     OR qualification_policy_authority_record_is_exact_v1(
          v_policy.authority_id
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality target/build/policy authority is not ready'
      USING ERRCODE='WQC03';
  END IF;
  IF workflow_input_raw_sha256(v_material.build_manifest_raw_bytes)<>
       workflow_input_normalize_sha256(v_build_manifest.content_hash)
     OR workflow_input_raw_sha256(v_material.build_contract_raw_bytes)<>
       workflow_input_normalize_sha256(v_build_contract.content_hash) THEN
    RAISE EXCEPTION 'v3 Quality BuildManifest or BuildContract bytes drifted'
      USING ERRCODE='WQC03';
  END IF;

  SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
    'manifestId',member.manifest_id::text,
    'rawBytesHex',pg_catalog.encode(member.raw_bytes,'hex'),
    'role',member.role
  ) ORDER BY member.ordinal),'[]'::jsonb)
  INTO v_candidate_manifests
  FROM workflow_v3_quality_completion_material_manifests AS member
  WHERE member.precommit_id=p_precommit_id;
  SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
    'canonicalReviewRequired',CASE
      WHEN member.purpose='workspace-target' THEN false
      ELSE review_default.canonical_review_required
    END,
    'currencyPolicy',CASE
      WHEN member.purpose='workspace-target' THEN 'latest-approved-required'
      WHEN exact_source.authority_id IS NOT NULL THEN 'exact-approved'
      ELSE v_policy.revision_policy_document->>'sourceCurrencyPolicy'
    END,
    'purpose',member.purpose,
    'rawBytesHex',pg_catalog.encode(member.raw_bytes,'hex'),
    'revisionId',member.revision_id::text
  ) ORDER BY member.ordinal),'[]'::jsonb)
  INTO v_candidate_revisions
  FROM workflow_v3_quality_completion_material_revisions AS member
  JOIN artifact_revisions AS revision ON revision.id=member.revision_id
  LEFT JOIN qualification_policy_review_defaults AS review_default
    ON review_default.authority_id=v_policy.authority_id
   AND review_default.change_source=revision.change_source
  LEFT JOIN qualification_policy_exact_approved_sources AS exact_source
    ON exact_source.authority_id=v_policy.authority_id
   AND exact_source.purpose=member.purpose
   AND exact_source.artifact_id=revision.artifact_id
   AND exact_source.revision_id=revision.id
   AND exact_source.content_hash=
       workflow_input_normalize_sha256(revision.content_hash)
  WHERE member.precommit_id=p_precommit_id;
  SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
    'purpose',member->>'purpose','revisionId',member->>'revisionId'
  ) ORDER BY pg_catalog.convert_to(member->>'purpose','UTF8'),
             (member->>'revisionId')::uuid),'[]'::jsonb)
  INTO v_review_requirements
  FROM pg_catalog.jsonb_array_elements(v_candidate_revisions) AS item(member)
  WHERE (member->>'canonicalReviewRequired')::boolean;
  IF (SELECT pg_catalog.count(*)
      FROM workflow_v3_quality_completion_material_review_receipts
      WHERE precommit_id=p_precommit_id)<>
       pg_catalog.jsonb_array_length(v_review_requirements)
     OR EXISTS (
       SELECT 1
       FROM pg_catalog.jsonb_array_elements(
         v_review_requirements
       ) AS requirement(member)
       JOIN canonical_review_approval_receipts AS receipt
         ON receipt.project_id=v_project.id
        AND receipt.revision_id=(member->>'revisionId')::uuid
       LEFT JOIN workflow_v3_quality_completion_material_review_receipts
         AS admitted
         ON admitted.precommit_id=p_precommit_id
        AND admitted.review_request_id=receipt.review_request_id
        AND admitted.raw_bytes=receipt.receipt_bytes
       WHERE admitted.review_request_id IS NULL
          OR canonical_review_approval_receipt_record_is_exact(receipt)
               IS NOT TRUE
     ) OR EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_review_receipts
         AS admitted
       LEFT JOIN canonical_review_approval_receipts AS receipt
         ON receipt.review_request_id=admitted.review_request_id
        AND receipt.project_id=v_project.id
        AND receipt.receipt_bytes=admitted.raw_bytes
       WHERE admitted.precommit_id=p_precommit_id
         AND (receipt.review_request_id IS NULL OR NOT EXISTS (
           SELECT 1
           FROM pg_catalog.jsonb_array_elements(
             v_review_requirements
           ) AS requirement(member)
           WHERE (member->>'revisionId')::uuid=receipt.revision_id
         ))
     ) THEN
    RAISE EXCEPTION
      'v3 Quality admitted Canonical Review receipts are incomplete or stale'
      USING ERRCODE='WQC03';
  END IF;
  v_candidate:=pg_catalog.jsonb_build_object(
    'inputManifests',v_candidate_manifests,
    'manifestSubject',v_run_manifest.kind,
    'qualificationPolicy',pg_catalog.jsonb_build_object(
      'authorityHash',v_policy.authority_hash,
      'authorityId',v_policy.authority_id::text,
      'externalGatePolicy',v_policy.external_gate_policy
    ),
    'qualityResult',pg_catalog.jsonb_build_object(
      'buildContractHash',workflow_input_normalize_sha256(
        v_build_contract.contract_hash
      ),
      'buildContractId',v_build_contract.id::text,
      'buildManifestHash',workflow_input_normalize_sha256(
        v_build_manifest.manifest_hash
      ),
      'buildManifestId',v_build_manifest.id::text,
      'passed',true,
      'qualityRunId',v_quality_run_id::text,
      'workspaceRevisionContentHash',workflow_input_normalize_sha256(
        v_target_revision.content_hash
      ),
      'workspaceRevisionId',v_target_revision.id::text
    ),
    'reviewRequirements',v_review_requirements,
    'revisions',v_candidate_revisions
  );

  -- Reuse migration 78's complete authoring predicate in a PL/pgSQL
  -- subtransaction.  The deliberate exception rolls back the probe authority
  -- and the simulated post-Quality row state, while local variables retain
  -- the database-authored Request/Input bytes.  Project/run/node locks were
  -- acquired by the outer transaction and remain held through the real
  -- Workflow writes and COMMIT.
  BEGIN
    UPDATE workflow_runs
    SET context=pg_catalog.jsonb_set(
          context,ARRAY['nodes',v_gate_node.node_key,'input'],v_node_input,true
        ),
        event_cursor=p_expected_run_cursor+1,
        updated_at=p_quality_completed_at
    WHERE id=v_run.id;
    UPDATE workflow_node_runs
    SET status='completed',output_revision_id=v_target_revision.id,
        lease_owner=NULL,lease_expires_at=NULL,
        completed_at=p_quality_completed_at,updated_at=p_quality_completed_at
    WHERE id=v_quality_node.id;
    SELECT * INTO v_probe
    FROM freeze_workflow_input_authority_v1(
      p_workflow_input_operation_id,p_workflow_input_authority_id,
      p_workflow_run_id,p_gate_node_run_id,p_expected_run_cursor+1,
      p_activation_event_id,p_expected_run_cursor+2,
      v_material.definition_raw_bytes,v_material.run_scope_raw_bytes,
      v_material.node_input_raw_bytes,v_material.build_manifest_raw_bytes,
      v_material.build_contract_raw_bytes,v_candidate
    );
    IF v_probe.authority_id IS NULL THEN
      RAISE EXCEPTION 'v3 Quality Workflow Input probe returned no authority'
        USING ERRCODE='WIA04';
    END IF;
    RAISE EXCEPTION 'rollback v3 Quality Workflow Input probe'
      USING ERRCODE='WQCP0';
  EXCEPTION
    WHEN SQLSTATE 'WQCP0' THEN NULL;
    WHEN SQLSTATE 'WIA01' THEN
      RAISE EXCEPTION 'v3 Quality Workflow Input identity probe conflicted'
        USING ERRCODE='WQC02';
    WHEN SQLSTATE 'WIA03' THEN
      RAISE EXCEPTION 'v3 Quality Workflow Input candidate probe was invalid'
        USING ERRCODE='WQC01';
    WHEN SQLSTATE 'WIA04' THEN
      RAISE EXCEPTION 'v3 Quality Workflow Input candidate probe was stale'
        USING ERRCODE='WQC03';
  END;
  IF v_probe.authority_id IS NULL
     OR v_probe.authority_id<>p_workflow_input_authority_id
     OR v_probe.operation_id<>p_workflow_input_operation_id
     OR v_probe.workflow_run_id<>p_workflow_run_id
     OR v_probe.node_run_id<>p_gate_node_run_id THEN
    RAISE EXCEPTION 'v3 Quality Workflow Input probe did not retain exact bytes'
      USING ERRCODE='WQC02';
  END IF;

  INSERT INTO workflow_v3_quality_completion_precommits(
    precommit_id,workflow_input_operation_id,workflow_input_authority_id,
    activation_event_id,project_id,workflow_run_id,quality_node_run_id,
    quality_node_key,gate_node_run_id,gate_node_key,expected_run_cursor,
    completion_event_sequence,completion_event_id,completion_event_payload,
    completion_event_actor_id,quality_completed_at,quality_lease_owner,
    quality_attempt,workspace_revision_id,gate_input_raw_bytes,
    gate_input_raw_bytes_hash,gate_input_raw_bytes_size,
    gate_input_semantic_hash,gate_input_binding_count
  ) VALUES (
    p_precommit_id,p_workflow_input_operation_id,
    p_workflow_input_authority_id,p_activation_event_id,v_project.id,
    p_workflow_run_id,p_quality_node_run_id,v_quality_node.node_key,
    p_gate_node_run_id,v_gate_node.node_key,p_expected_run_cursor,
    p_expected_run_cursor+1,p_completion_event_id,p_completion_event_payload,
    p_completion_event_actor_id,p_quality_completed_at,p_quality_lease_owner,
    p_quality_attempt,v_target_revision.id,p_gate_input_raw_bytes,
    workflow_input_raw_sha256(p_gate_input_raw_bytes),
    pg_catalog.octet_length(p_gate_input_raw_bytes),
    workflow_input_normalize_sha256(v_node_input->>'hash'),1
  ) RETURNING * INTO v_existing;
  INSERT INTO workflow_v3_quality_completion_identity_reservations(
    identity_value,identity_kind,precommit_id,reserved_at
  ) VALUES
    (p_precommit_id,'precommit',p_precommit_id,p_quality_completed_at),
    (p_workflow_input_operation_id,'workflow-input-operation',
      p_precommit_id,p_quality_completed_at),
    (p_workflow_input_authority_id,'workflow-input-authority',
      p_precommit_id,p_quality_completed_at),
    (p_activation_event_id,'activation-event',
      p_precommit_id,p_quality_completed_at);

  v_candidate_bytes:=workflow_input_canonical_jsonb_bytes(v_candidate);
  v_material_bundle:=workflow_v3_quality_completion_material_bundle_v1(
    p_precommit_id
  );
  v_material_bundle_hash:=workflow_input_authority_hash(
    'worksflow.workflow-v3-quality-completion.material-bundle/v1',
    workflow_input_canonical_jsonb_bytes(v_material_bundle)
  );
  SELECT pg_catalog.count(*) INTO v_manifest_count
  FROM workflow_v3_quality_completion_material_manifests
  WHERE precommit_id=p_precommit_id;
  SELECT pg_catalog.count(*) INTO v_revision_count
  FROM workflow_v3_quality_completion_material_revisions
  WHERE precommit_id=p_precommit_id;
  SELECT pg_catalog.count(*) INTO v_receipt_count
  FROM workflow_v3_quality_completion_material_review_receipts
  WHERE precommit_id=p_precommit_id;
  v_retained_bytes:=pg_catalog.octet_length(v_material.definition_raw_bytes)
    +pg_catalog.octet_length(v_material.run_scope_raw_bytes)
    +pg_catalog.octet_length(v_material.node_input_raw_bytes)
    +pg_catalog.octet_length(v_material.build_manifest_raw_bytes)
    +pg_catalog.octet_length(v_material.build_contract_raw_bytes)
    +COALESCE((SELECT pg_catalog.sum(raw_bytes_size)
      FROM workflow_v3_quality_completion_material_manifests
      WHERE precommit_id=p_precommit_id),0)
    +COALESCE((SELECT pg_catalog.sum(raw_bytes_size)
      FROM workflow_v3_quality_completion_material_revisions
      WHERE precommit_id=p_precommit_id),0)
    +COALESCE((SELECT pg_catalog.sum(raw_bytes_size)
      FROM workflow_v3_quality_completion_material_review_receipts
      WHERE precommit_id=p_precommit_id),0);
  v_snapshot_document:=pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-workflow-v3-quality-completion-candidate-snapshot/v1',
    'precommitId',p_precommit_id::text,
    'completionEventId',p_completion_event_id::text,
    'workflowInputOperationId',p_workflow_input_operation_id::text,
    'workflowInputAuthorityId',p_workflow_input_authority_id::text,
    'activationEventId',p_activation_event_id::text,
    'freezeRequestHash',v_probe.request_hash,
    'workflowInputHash',v_probe.input_hash,
    'freezeCandidateRawHash',workflow_input_raw_sha256(v_candidate_bytes),
    'definitionRawHash',workflow_input_raw_sha256(
      v_material.definition_raw_bytes
    ),
    'runScopeRawHash',workflow_input_raw_sha256(
      v_material.run_scope_raw_bytes
    ),
    'nodeInputRawHash',workflow_input_raw_sha256(
      v_material.node_input_raw_bytes
    ),
    'buildManifestRawHash',workflow_input_raw_sha256(
      v_material.build_manifest_raw_bytes
    ),
    'buildContractRawHash',workflow_input_raw_sha256(
      v_material.build_contract_raw_bytes
    ),
    'inputManifestCount',v_manifest_count,
    'revisionCount',v_revision_count,
    'reviewReceiptCount',v_receipt_count,
    'materialBundleHash',v_material_bundle_hash,
    'retainedRawBytesSize',v_retained_bytes
  );
  v_snapshot_hash:=workflow_input_authority_hash(
    'worksflow.workflow-v3-quality-completion.candidate-snapshot/v1',
    workflow_input_canonical_jsonb_bytes(v_snapshot_document)
  );
  INSERT INTO workflow_v3_quality_completion_candidate_snapshots(
    precommit_id,freeze_request_hash,freeze_request_bytes,
    workflow_input_hash,workflow_input_bytes,freeze_candidate_bytes,
    snapshot_hash,retained_raw_bytes_size,manifest_count,revision_count,
    review_receipt_count,material_bundle_hash,creation_transaction_id
  ) VALUES (
    p_precommit_id,v_probe.request_hash,v_probe.request_bytes,
    v_probe.input_hash,v_probe.input_bytes,v_candidate_bytes,v_snapshot_hash,
    v_retained_bytes,v_manifest_count,v_revision_count,v_receipt_count,
    v_material_bundle_hash,pg_catalog.pg_current_xact_id()::text
  );
  IF workflow_v3_quality_completion_snapshot_is_exact_v1(
       p_precommit_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality completion snapshot did not close exactly'
      USING ERRCODE='WQC02';
  END IF;
  RETURN NEXT v_existing;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'v3 Quality completion identity or closure conflicts'
      USING ERRCODE='WQC02';
END;
$function$;

CREATE FUNCTION workflow_v3_quality_completion_material_bundle_v1(
  p_precommit_id uuid
)
RETURNS jsonb
LANGUAGE sql
STABLE STRICT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
  SELECT pg_catalog.jsonb_build_object(
    'inputManifests',COALESCE((
      SELECT pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
        'manifestId',member.manifest_id::text,
        'rawBytesHash',member.raw_bytes_hash,
        'rawBytesHex',pg_catalog.encode(member.raw_bytes,'hex'),
        'rawBytesSize',member.raw_bytes_size,
        'role',member.role
      ) ORDER BY member.ordinal)
      FROM workflow_v3_quality_completion_material_manifests AS member
      WHERE member.precommit_id=p_precommit_id
    ),'[]'::jsonb),
    'revisions',COALESCE((
      SELECT pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
        'purpose',member.purpose,
        'rawBytesHash',member.raw_bytes_hash,
        'rawBytesHex',pg_catalog.encode(member.raw_bytes,'hex'),
        'rawBytesSize',member.raw_bytes_size,
        'revisionId',member.revision_id::text
      ) ORDER BY member.ordinal)
      FROM workflow_v3_quality_completion_material_revisions AS member
      WHERE member.precommit_id=p_precommit_id
    ),'[]'::jsonb),
    'reviewReceipts',COALESCE((
      SELECT pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
        'rawBytesHash',member.raw_bytes_hash,
        'rawBytesHex',pg_catalog.encode(member.raw_bytes,'hex'),
        'rawBytesSize',member.raw_bytes_size,
        'reviewRequestId',member.review_request_id::text
      ) ORDER BY member.ordinal)
      FROM workflow_v3_quality_completion_material_review_receipts AS member
      WHERE member.precommit_id=p_precommit_id
    ),'[]'::jsonb)
  )
$function$;

CREATE FUNCTION workflow_v3_quality_completion_snapshot_is_exact_v1(
  p_precommit_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_precommit workflow_v3_quality_completion_precommits%ROWTYPE;
  v_material workflow_v3_quality_completion_materials%ROWTYPE;
  v_snapshot workflow_v3_quality_completion_candidate_snapshots%ROWTYPE;
  v_material_bundle jsonb;
  v_material_bundle_hash text;
  v_snapshot_document jsonb;
  v_retained_bytes bigint;
  v_manifest_count integer;
  v_revision_count integer;
  v_receipt_count integer;
BEGIN
  SELECT * INTO v_precommit
  FROM workflow_v3_quality_completion_precommits
  WHERE precommit_id=p_precommit_id;
  SELECT * INTO v_material
  FROM workflow_v3_quality_completion_materials
  WHERE precommit_id=p_precommit_id;
  SELECT * INTO v_snapshot
  FROM workflow_v3_quality_completion_candidate_snapshots
  WHERE precommit_id=p_precommit_id;
  IF v_precommit.precommit_id IS NULL OR v_material.precommit_id IS NULL
     OR v_snapshot.precommit_id IS NULL
     OR v_material.completion_event_id<>v_precommit.completion_event_id
     OR v_material.node_input_raw_bytes<>v_precommit.gate_input_raw_bytes
     OR v_precommit.gate_input_raw_bytes_size<>
          pg_catalog.octet_length(v_precommit.gate_input_raw_bytes)
     OR v_precommit.gate_input_raw_bytes_hash<>
          workflow_input_raw_sha256(v_precommit.gate_input_raw_bytes) THEN
    RETURN false;
  END IF;
  SELECT pg_catalog.count(*) INTO v_manifest_count
  FROM workflow_v3_quality_completion_material_manifests
  WHERE precommit_id=p_precommit_id;
  SELECT pg_catalog.count(*) INTO v_revision_count
  FROM workflow_v3_quality_completion_material_revisions
  WHERE precommit_id=p_precommit_id;
  SELECT pg_catalog.count(*) INTO v_receipt_count
  FROM workflow_v3_quality_completion_material_review_receipts
  WHERE precommit_id=p_precommit_id;
  IF v_manifest_count<>v_snapshot.manifest_count
     OR v_revision_count<>v_snapshot.revision_count
     OR v_receipt_count<>v_snapshot.review_receipt_count
     OR EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_manifests AS member
       WHERE member.precommit_id=p_precommit_id
         AND (member.raw_bytes_size<>pg_catalog.octet_length(member.raw_bytes)
           OR member.raw_bytes_hash<>workflow_input_raw_sha256(member.raw_bytes))
     )
     OR EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_revisions AS member
       WHERE member.precommit_id=p_precommit_id
         AND (member.raw_bytes_size<>pg_catalog.octet_length(member.raw_bytes)
           OR member.raw_bytes_hash<>workflow_input_raw_sha256(member.raw_bytes))
     )
     OR EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_review_receipts AS member
       WHERE member.precommit_id=p_precommit_id
         AND (member.raw_bytes_size<>pg_catalog.octet_length(member.raw_bytes)
           OR member.raw_bytes_hash<>workflow_input_raw_sha256(member.raw_bytes))
     ) THEN
    RETURN false;
  END IF;
  v_retained_bytes:=pg_catalog.octet_length(v_material.definition_raw_bytes)
    +pg_catalog.octet_length(v_material.run_scope_raw_bytes)
    +pg_catalog.octet_length(v_material.node_input_raw_bytes)
    +pg_catalog.octet_length(v_material.build_manifest_raw_bytes)
    +pg_catalog.octet_length(v_material.build_contract_raw_bytes)
    +COALESCE((SELECT pg_catalog.sum(raw_bytes_size)
      FROM workflow_v3_quality_completion_material_manifests
      WHERE precommit_id=p_precommit_id),0)
    +COALESCE((SELECT pg_catalog.sum(raw_bytes_size)
      FROM workflow_v3_quality_completion_material_revisions
      WHERE precommit_id=p_precommit_id),0)
    +COALESCE((SELECT pg_catalog.sum(raw_bytes_size)
      FROM workflow_v3_quality_completion_material_review_receipts
      WHERE precommit_id=p_precommit_id),0);
  v_material_bundle:=workflow_v3_quality_completion_material_bundle_v1(
    p_precommit_id
  );
  v_material_bundle_hash:=workflow_input_authority_hash(
    'worksflow.workflow-v3-quality-completion.material-bundle/v1',
    workflow_input_canonical_jsonb_bytes(v_material_bundle)
  );
  v_snapshot_document:=pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-workflow-v3-quality-completion-candidate-snapshot/v1',
    'precommitId',v_precommit.precommit_id::text,
    'completionEventId',v_precommit.completion_event_id::text,
    'workflowInputOperationId',v_precommit.workflow_input_operation_id::text,
    'workflowInputAuthorityId',v_precommit.workflow_input_authority_id::text,
    'activationEventId',v_precommit.activation_event_id::text,
    'freezeRequestHash',v_snapshot.freeze_request_hash,
    'workflowInputHash',v_snapshot.workflow_input_hash,
    'freezeCandidateRawHash',workflow_input_raw_sha256(
      v_snapshot.freeze_candidate_bytes
    ),
    'definitionRawHash',workflow_input_raw_sha256(
      v_material.definition_raw_bytes
    ),
    'runScopeRawHash',workflow_input_raw_sha256(
      v_material.run_scope_raw_bytes
    ),
    'nodeInputRawHash',workflow_input_raw_sha256(
      v_material.node_input_raw_bytes
    ),
    'buildManifestRawHash',workflow_input_raw_sha256(
      v_material.build_manifest_raw_bytes
    ),
    'buildContractRawHash',workflow_input_raw_sha256(
      v_material.build_contract_raw_bytes
    ),
    'inputManifestCount',v_manifest_count,
    'revisionCount',v_revision_count,
    'reviewReceiptCount',v_receipt_count,
    'materialBundleHash',v_material_bundle_hash,
    'retainedRawBytesSize',v_retained_bytes
  );
  RETURN v_snapshot.retained_raw_bytes_size=v_retained_bytes
    AND v_snapshot.material_bundle_hash=v_material_bundle_hash
    AND v_snapshot.freeze_request_hash=workflow_input_authority_hash(
      'worksflow.workflow-input.freeze-request/v1',
      v_snapshot.freeze_request_bytes
    )
    AND v_snapshot.workflow_input_hash=workflow_input_authority_hash(
      'worksflow.workflow-input.input/v1',v_snapshot.workflow_input_bytes
    )
    AND v_snapshot.snapshot_hash=workflow_input_authority_hash(
      'worksflow.workflow-v3-quality-completion.candidate-snapshot/v1',
      workflow_input_canonical_jsonb_bytes(v_snapshot_document)
    )
    AND (SELECT pg_catalog.count(*)
      FROM workflow_v3_quality_completion_identity_reservations
      WHERE precommit_id=p_precommit_id)=4
    AND NOT EXISTS (
      SELECT 1
      FROM workflow_v3_quality_completion_identity_reservations AS reservation
      WHERE reservation.precommit_id=p_precommit_id
        AND reservation.identity_value<>CASE reservation.identity_kind
          WHEN 'precommit' THEN v_precommit.precommit_id
          WHEN 'workflow-input-operation' THEN
            v_precommit.workflow_input_operation_id
          WHEN 'workflow-input-authority' THEN
            v_precommit.workflow_input_authority_id
          WHEN 'activation-event' THEN v_precommit.activation_event_id
        END
    );
EXCEPTION WHEN OTHERS THEN
  RETURN false;
END;
$function$;

CREATE FUNCTION workflow_v3_quality_completion_require_primary_v1()
RETURNS void
LANGUAGE plpgsql
STABLE PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
BEGIN
  IF pg_catalog.pg_is_in_recovery()
     OR pg_catalog.current_setting('transaction_read_only')<>'off' THEN
    RAISE EXCEPTION 'v3 Quality completion requires a read-write primary'
      USING ERRCODE='WQC03';
  END IF;
END;
$function$;

CREATE FUNCTION workflow_v3_quality_completion_rfc3339_v1(
  p_value timestamptz
)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
SECURITY INVOKER
AS $function$
  SELECT CASE
    WHEN pg_catalog.date_part('milliseconds',p_value)::integer % 1000=0
      THEN pg_catalog.to_char(
        p_value AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'
      )
    ELSE pg_catalog.regexp_replace(
      pg_catalog.to_char(
        p_value AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
      ),'0+Z$','Z'
    )
  END
$function$;

CREATE FUNCTION reject_workflow_v3_quality_completion_mutation_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog
AS $function$
BEGIN
  RAISE EXCEPTION 'v3 Quality completion snapshot is immutable'
    USING ERRCODE='WQC02';
END;
$function$;

CREATE TRIGGER workflow_v3_quality_completion_precommits_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON workflow_v3_quality_completion_precommits
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_v3_quality_completion_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_materials_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON workflow_v3_quality_completion_materials
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_v3_quality_completion_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_material_manifests_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON workflow_v3_quality_completion_material_manifests
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_v3_quality_completion_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_material_revisions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON workflow_v3_quality_completion_material_revisions
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_v3_quality_completion_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_material_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON workflow_v3_quality_completion_material_review_receipts
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_v3_quality_completion_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_snapshots_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON workflow_v3_quality_completion_candidate_snapshots
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_v3_quality_completion_mutation_v1();
CREATE TRIGGER workflow_v3_quality_completion_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON workflow_v3_quality_completion_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_v3_quality_completion_mutation_v1();

CREATE FUNCTION admit_workflow_v3_quality_completion_materials_v1(
  p_precommit_id uuid,
  p_completion_event_id uuid,
  p_definition_raw_bytes bytea,
  p_run_scope_raw_bytes bytea,
  p_node_input_raw_bytes bytea,
  p_build_manifest_raw_bytes bytea,
  p_build_contract_raw_bytes bytea,
  p_material_bundle jsonb
)
RETURNS void
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_existing workflow_v3_quality_completion_materials%ROWTYPE;
  v_retained_bytes bigint;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  PERFORM workflow_v3_quality_completion_require_primary_v1();
  IF workflow_v3_quality_completion_runtime_caller_is_exact_v1(
       'worksflow_application'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality material admission caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF pg_catalog.current_setting('transaction_isolation')<>'serializable' THEN
    RAISE EXCEPTION 'v3 Quality material admission requires SERIALIZABLE isolation'
      USING ERRCODE='WQC03';
  END IF;
  IF p_precommit_id IS NULL
     OR workflow_input_uuid_is_exact(p_precommit_id::text) IS NOT TRUE
     OR p_completion_event_id IS NULL
     OR workflow_input_uuid_is_exact(p_completion_event_id::text) IS NOT TRUE
     OR p_precommit_id=p_completion_event_id THEN
    RAISE EXCEPTION 'v3 Quality material admission identity is invalid'
      USING ERRCODE='WQC01';
  END IF;
  IF p_definition_raw_bytes IS NULL
     OR pg_catalog.octet_length(p_definition_raw_bytes) NOT BETWEEN 1 AND 8388608
     OR p_run_scope_raw_bytes IS NULL
     OR pg_catalog.octet_length(p_run_scope_raw_bytes) NOT BETWEEN 1 AND 8388608
     OR p_node_input_raw_bytes IS NULL
     OR pg_catalog.octet_length(p_node_input_raw_bytes) NOT BETWEEN 1 AND 16777216
     OR p_build_manifest_raw_bytes IS NULL
     OR pg_catalog.octet_length(p_build_manifest_raw_bytes) NOT BETWEEN 1 AND 8388608
     OR p_build_contract_raw_bytes IS NULL
     OR pg_catalog.octet_length(p_build_contract_raw_bytes) NOT BETWEEN 1 AND 8388608
     OR p_material_bundle IS NULL
     OR pg_catalog.jsonb_typeof(p_material_bundle)<>'object'
     OR p_material_bundle - ARRAY[
       'inputManifests','revisions','reviewReceipts'
     ]<>'{}'::jsonb
     OR NOT (p_material_bundle ?& ARRAY[
       'inputManifests','revisions','reviewReceipts'
     ])
     OR pg_catalog.jsonb_typeof(p_material_bundle->'inputManifests')<>'array'
     OR pg_catalog.jsonb_typeof(p_material_bundle->'revisions')<>'array'
     OR pg_catalog.jsonb_typeof(p_material_bundle->'reviewReceipts')<>'array'
     OR pg_catalog.octet_length(p_material_bundle::text)>268435456 THEN
    RAISE EXCEPTION 'v3 Quality material admission root is invalid or oversized'
      USING ERRCODE='WQC01';
  END IF;
  IF pg_catalog.jsonb_array_length(p_material_bundle->'inputManifests')
       NOT BETWEEN 1 AND 1024
     OR pg_catalog.jsonb_array_length(p_material_bundle->'revisions')
       NOT BETWEEN 1 AND 2048
     OR pg_catalog.jsonb_array_length(p_material_bundle->'reviewReceipts')>2048
     OR EXISTS (
       SELECT 1
       FROM pg_catalog.jsonb_array_elements(
         p_material_bundle->'inputManifests'
       ) AS member(value)
       WHERE pg_catalog.jsonb_typeof(value)<>'object'
          OR value - ARRAY['manifestId','rawBytesHex','role']<>'{}'::jsonb
          OR NOT (value ?& ARRAY['manifestId','rawBytesHex','role'])
          OR workflow_input_uuid_is_exact(value->>'manifestId') IS NOT TRUE
          OR value->>'role' NOT IN ('run','predecessor')
          OR pg_catalog.jsonb_typeof(value->'rawBytesHex')<>'string'
          OR value->>'rawBytesHex' !~ '^[0-9a-f]+$'
          OR pg_catalog.octet_length(value->>'rawBytesHex') NOT BETWEEN 2 AND 16777216
          OR pg_catalog.octet_length(value->>'rawBytesHex')%2<>0
     )
     OR EXISTS (
       SELECT 1
       FROM pg_catalog.jsonb_array_elements(
         p_material_bundle->'revisions'
       ) AS member(value)
       WHERE pg_catalog.jsonb_typeof(value)<>'object'
          OR value - ARRAY['purpose','rawBytesHex','revisionId']<>'{}'::jsonb
          OR NOT (value ?& ARRAY['purpose','rawBytesHex','revisionId'])
          OR workflow_input_uuid_is_exact(value->>'revisionId') IS NOT TRUE
          OR pg_catalog.jsonb_typeof(value->'purpose')<>'string'
          OR pg_catalog.octet_length(value->>'purpose') NOT BETWEEN 1 AND 256
          OR value->>'purpose'<>pg_catalog.btrim(value->>'purpose')
          OR pg_catalog.jsonb_typeof(value->'rawBytesHex')<>'string'
          OR value->>'rawBytesHex' !~ '^[0-9a-f]+$'
          OR pg_catalog.octet_length(value->>'rawBytesHex') NOT BETWEEN 2 AND 33554432
          OR pg_catalog.octet_length(value->>'rawBytesHex')%2<>0
     )
     OR EXISTS (
       SELECT 1
       FROM pg_catalog.jsonb_array_elements(
         p_material_bundle->'reviewReceipts'
       ) AS member(value)
       WHERE pg_catalog.jsonb_typeof(value)<>'object'
          OR value - ARRAY['rawBytesHex','reviewRequestId']<>'{}'::jsonb
          OR NOT (value ?& ARRAY['rawBytesHex','reviewRequestId'])
          OR workflow_input_uuid_is_exact(value->>'reviewRequestId') IS NOT TRUE
          OR pg_catalog.jsonb_typeof(value->'rawBytesHex')<>'string'
          OR value->>'rawBytesHex' !~ '^[0-9a-f]+$'
          OR pg_catalog.octet_length(value->>'rawBytesHex') NOT BETWEEN 2 AND 2097152
          OR pg_catalog.octet_length(value->>'rawBytesHex')%2<>0
     ) THEN
    RAISE EXCEPTION 'v3 Quality material member is invalid, missing, or oversized'
      USING ERRCODE='WQC01';
  END IF;

  IF EXISTS (
       SELECT 1
       FROM (
         SELECT ordinality,
           pg_catalog.row_number() OVER (
             ORDER BY pg_catalog.convert_to(value->>'role','UTF8'),
                      (value->>'manifestId')::uuid
           ) AS expected
         FROM pg_catalog.jsonb_array_elements(
           p_material_bundle->'inputManifests'
         ) WITH ORDINALITY AS member(value,ordinality)
       ) AS ordered
       WHERE ordinality<>expected
     )
     OR EXISTS (
       SELECT 1
       FROM (
         SELECT ordinality,
           pg_catalog.row_number() OVER (
             ORDER BY pg_catalog.convert_to(value->>'purpose','UTF8'),
                      (value->>'revisionId')::uuid
           ) AS expected
         FROM pg_catalog.jsonb_array_elements(
           p_material_bundle->'revisions'
         ) WITH ORDINALITY AS member(value,ordinality)
       ) AS ordered
       WHERE ordinality<>expected
     )
     OR EXISTS (
       SELECT 1
       FROM (
         SELECT ordinality,
           pg_catalog.row_number() OVER (
             ORDER BY (value->>'reviewRequestId')::uuid
           ) AS expected
         FROM pg_catalog.jsonb_array_elements(
           p_material_bundle->'reviewReceipts'
         ) WITH ORDINALITY AS member(value,ordinality)
       ) AS ordered
       WHERE ordinality<>expected
     ) THEN
    RAISE EXCEPTION 'v3 Quality material bundle is not deterministically ordered'
      USING ERRCODE='WQC01';
  END IF;

  IF (SELECT pg_catalog.count(*)
      FROM pg_catalog.jsonb_array_elements(
        p_material_bundle->'inputManifests'
      ))<>(SELECT pg_catalog.count(DISTINCT (value->>'role',value->>'manifestId'))
           FROM pg_catalog.jsonb_array_elements(
             p_material_bundle->'inputManifests'
           ) AS member(value))
     OR (SELECT pg_catalog.count(*)
         FROM pg_catalog.jsonb_array_elements(
           p_material_bundle->'revisions'
         ))<>(SELECT pg_catalog.count(DISTINCT value->>'revisionId')
              FROM pg_catalog.jsonb_array_elements(
                p_material_bundle->'revisions'
              ) AS member(value))
     OR (SELECT pg_catalog.count(*)
         FROM pg_catalog.jsonb_array_elements(
           p_material_bundle->'reviewReceipts'
         ))<>(SELECT pg_catalog.count(DISTINCT value->>'reviewRequestId')
              FROM pg_catalog.jsonb_array_elements(
                p_material_bundle->'reviewReceipts'
              ) AS member(value)) THEN
    RAISE EXCEPTION 'v3 Quality material identities are duplicated'
      USING ERRCODE='WQC01';
  END IF;

  v_retained_bytes:=pg_catalog.octet_length(p_definition_raw_bytes)
    +pg_catalog.octet_length(p_run_scope_raw_bytes)
    +pg_catalog.octet_length(p_node_input_raw_bytes)
    +pg_catalog.octet_length(p_build_manifest_raw_bytes)
    +pg_catalog.octet_length(p_build_contract_raw_bytes)
    +COALESCE((
      SELECT pg_catalog.sum(pg_catalog.octet_length(
        pg_catalog.decode(value->>'rawBytesHex','hex')
      ))
      FROM pg_catalog.jsonb_array_elements(
        p_material_bundle->'inputManifests'
      ) AS member(value)
    ),0)
    +COALESCE((
      SELECT pg_catalog.sum(pg_catalog.octet_length(
        pg_catalog.decode(value->>'rawBytesHex','hex')
      ))
      FROM pg_catalog.jsonb_array_elements(
        p_material_bundle->'revisions'
      ) AS member(value)
    ),0)
    +COALESCE((
      SELECT pg_catalog.sum(pg_catalog.octet_length(
        pg_catalog.decode(value->>'rawBytesHex','hex')
      ))
      FROM pg_catalog.jsonb_array_elements(
        p_material_bundle->'reviewReceipts'
      ) AS member(value)
    ),0);
  IF v_retained_bytes>134217728 THEN
    RAISE EXCEPTION 'v3 Quality retained material exceeds the aggregate bound'
      USING ERRCODE='WQC01';
  END IF;

  SELECT * INTO v_existing
  FROM workflow_v3_quality_completion_materials
  WHERE precommit_id=p_precommit_id
     OR completion_event_id=p_completion_event_id;
  IF FOUND THEN
    IF v_existing.precommit_id<>p_precommit_id
       OR v_existing.completion_event_id<>p_completion_event_id
       OR v_existing.definition_raw_bytes<>p_definition_raw_bytes
       OR v_existing.run_scope_raw_bytes<>p_run_scope_raw_bytes
       OR v_existing.node_input_raw_bytes<>p_node_input_raw_bytes
       OR v_existing.build_manifest_raw_bytes<>p_build_manifest_raw_bytes
       OR v_existing.build_contract_raw_bytes<>p_build_contract_raw_bytes
       OR v_existing.admission_bundle<>p_material_bundle THEN
      RAISE EXCEPTION 'v3 Quality material admission conflicts with immutable bytes'
        USING ERRCODE='WQC02';
    END IF;
    RETURN;
  END IF;

  INSERT INTO workflow_v3_quality_completion_materials(
    precommit_id,completion_event_id,definition_raw_bytes,
    run_scope_raw_bytes,node_input_raw_bytes,build_manifest_raw_bytes,
    build_contract_raw_bytes,admission_bundle,creation_transaction_id
  ) VALUES (
    p_precommit_id,p_completion_event_id,p_definition_raw_bytes,
    p_run_scope_raw_bytes,p_node_input_raw_bytes,p_build_manifest_raw_bytes,
    p_build_contract_raw_bytes,p_material_bundle,
    pg_catalog.pg_current_xact_id()::text
  );
  INSERT INTO workflow_v3_quality_completion_material_manifests(
    precommit_id,ordinal,manifest_id,role,raw_bytes,raw_bytes_hash,
    raw_bytes_size
  )
  SELECT p_precommit_id,ordinality-1,(value->>'manifestId')::uuid,
    value->>'role',pg_catalog.decode(value->>'rawBytesHex','hex'),
    workflow_input_raw_sha256(
      pg_catalog.decode(value->>'rawBytesHex','hex')
    ),
    pg_catalog.octet_length(
      pg_catalog.decode(value->>'rawBytesHex','hex')
    )
  FROM pg_catalog.jsonb_array_elements(
    p_material_bundle->'inputManifests'
  ) WITH ORDINALITY AS member(value,ordinality);
  INSERT INTO workflow_v3_quality_completion_material_revisions(
    precommit_id,ordinal,purpose,revision_id,raw_bytes,raw_bytes_hash,
    raw_bytes_size
  )
  SELECT p_precommit_id,ordinality-1,value->>'purpose',
    (value->>'revisionId')::uuid,
    pg_catalog.decode(value->>'rawBytesHex','hex'),
    workflow_input_raw_sha256(
      pg_catalog.decode(value->>'rawBytesHex','hex')
    ),
    pg_catalog.octet_length(
      pg_catalog.decode(value->>'rawBytesHex','hex')
    )
  FROM pg_catalog.jsonb_array_elements(
    p_material_bundle->'revisions'
  ) WITH ORDINALITY AS member(value,ordinality);
  INSERT INTO workflow_v3_quality_completion_material_review_receipts(
    precommit_id,ordinal,review_request_id,raw_bytes,raw_bytes_hash,
    raw_bytes_size
  )
  SELECT p_precommit_id,ordinality-1,(value->>'reviewRequestId')::uuid,
    pg_catalog.decode(value->>'rawBytesHex','hex'),
    workflow_input_raw_sha256(
      pg_catalog.decode(value->>'rawBytesHex','hex')
    ),
    pg_catalog.octet_length(
      pg_catalog.decode(value->>'rawBytesHex','hex')
    )
  FROM pg_catalog.jsonb_array_elements(
    p_material_bundle->'reviewReceipts'
  ) WITH ORDINALITY AS member(value,ordinality);
  IF EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_manifests AS current_member
       JOIN workflow_v3_quality_completion_material_manifests AS previous_member
         ON previous_member.precommit_id=current_member.precommit_id
        AND previous_member.ordinal=current_member.ordinal-1
       WHERE current_member.precommit_id=p_precommit_id
         AND (pg_catalog.convert_to(previous_member.role,'UTF8')>
                pg_catalog.convert_to(current_member.role,'UTF8')
           OR (previous_member.role=current_member.role
             AND previous_member.manifest_id>current_member.manifest_id))
     ) OR EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_revisions AS current_member
       JOIN workflow_v3_quality_completion_material_revisions AS previous_member
         ON previous_member.precommit_id=current_member.precommit_id
        AND previous_member.ordinal=current_member.ordinal-1
       WHERE current_member.precommit_id=p_precommit_id
         AND (pg_catalog.convert_to(previous_member.purpose,'UTF8')>
                pg_catalog.convert_to(current_member.purpose,'UTF8')
           OR (previous_member.purpose=current_member.purpose
             AND previous_member.revision_id>current_member.revision_id))
     ) OR EXISTS (
       SELECT 1
       FROM workflow_v3_quality_completion_material_review_receipts
         AS current_member
       JOIN workflow_v3_quality_completion_material_review_receipts
         AS previous_member
         ON previous_member.precommit_id=current_member.precommit_id
        AND previous_member.ordinal=current_member.ordinal-1
       WHERE current_member.precommit_id=p_precommit_id
         AND previous_member.review_request_id>
             current_member.review_request_id
     ) THEN
    RAISE EXCEPTION 'v3 Quality material bundle order is not canonical'
      USING ERRCODE='WQC01';
  END IF;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'v3 Quality material identity or shape conflicts'
      USING ERRCODE='WQC02';
END;
$function$;

-- Read-only, least-privilege prefetch plan.  It exposes only public artifact
-- content references plus the exact Canonical Review receipt bytes required
-- by the current policy.  The application resolves every referenced blob
-- before BEGIN; admit + precommit then re-lock and revalidate this graph in
-- the SERIALIZABLE commit transaction.
CREATE FUNCTION resolve_workflow_v3_quality_completion_material_plan_v1(
  p_workflow_run_id uuid,
  p_quality_node_run_id uuid,
  p_node_input_raw_bytes bytea
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_run workflow_runs%ROWTYPE;
  v_quality workflow_node_runs%ROWTYPE;
  v_definition workflow_definition_versions%ROWTYPE;
  v_target artifact_revisions%ROWTYPE;
  v_proposal implementation_proposals%ROWTYPE;
  v_build_manifest application_build_manifests%ROWTYPE;
  v_build_contract application_build_contracts%ROWTYPE;
  v_policy qualification_policy_authorities%ROWTYPE;
  v_node_input jsonb;
  v_target_revision_id uuid;
  v_manifests jsonb;
  v_revisions jsonb;
  v_receipts jsonb;
  v_required_receipt_count integer;
BEGIN
  PERFORM workflow_v3_quality_completion_require_primary_v1();
  IF workflow_v3_quality_completion_runtime_caller_is_exact_v1(
       'worksflow_application'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality material-plan caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF p_workflow_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_quality_node_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_quality_node_run_id::text) IS NOT TRUE
     OR p_node_input_raw_bytes IS NULL
     OR pg_catalog.octet_length(p_node_input_raw_bytes)
          NOT BETWEEN 1 AND 16777216 THEN
    RAISE EXCEPTION 'v3 Quality material-plan argument is invalid'
      USING ERRCODE='WQC01';
  END IF;
  BEGIN
    v_node_input:=pg_catalog.convert_from(
      p_node_input_raw_bytes,'UTF8'
    )::jsonb;
    v_target_revision_id:=(
      v_node_input#>>'{bindings,0,value,workspaceRevision,revisionId}'
    )::uuid;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'v3 Quality material-plan node input is invalid'
      USING ERRCODE='WQC01';
  END;
  SELECT * INTO v_run FROM workflow_runs WHERE id=p_workflow_run_id;
  SELECT * INTO v_quality FROM workflow_node_runs
  WHERE id=p_quality_node_run_id;
  SELECT * INTO v_definition FROM workflow_definition_versions
  WHERE id=v_run.definition_version_id;
  SELECT * INTO v_target FROM artifact_revisions
  WHERE id=v_target_revision_id;
  SELECT * INTO v_proposal FROM implementation_proposals
  WHERE id=v_target.implementation_proposal_id;
  SELECT * INTO v_build_manifest FROM application_build_manifests
  WHERE id=v_proposal.build_manifest_id;
  SELECT * INTO v_build_contract FROM application_build_contracts
  WHERE id=v_proposal.application_build_contract_id;
  SELECT * INTO v_policy FROM qualification_policy_authorities
  WHERE project_id=v_run.project_id
    AND execution_profile_version=v_run.execution_profile_version
    AND execution_profile_hash=workflow_input_normalize_sha256(
      v_run.execution_profile_hash
    )
  ORDER BY generation DESC LIMIT 1;
  IF v_run.id IS NULL OR v_quality.id IS NULL
     OR v_quality.run_id<>v_run.id OR v_quality.node_type<>'quality_gate'
     OR v_run.execution_profile_version<>'workflow-engine/v3'
     OR v_run.execution_profile_hash<>
          '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR v_definition.id IS NULL OR v_definition.published IS NOT TRUE
     OR v_target.id IS NULL OR v_proposal.id IS NULL
     OR v_build_manifest.id IS NULL OR v_build_contract.id IS NULL
     OR v_build_manifest.project_id<>v_run.project_id
     OR v_build_contract.project_id<>v_run.project_id
     OR v_policy.authority_id IS NULL OR v_policy.status<>'active'
     OR qualification_policy_authority_record_is_exact_v1(
          v_policy.authority_id
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'v3 Quality material-plan lineage is unavailable'
      USING ERRCODE='WQC03';
  END IF;

  WITH referenced(manifest_id,role) AS (
    SELECT v_run.input_manifest_id,'run'::text
    UNION
    SELECT (binding->'source'->'inputManifest'->>'id')::uuid,
           'predecessor'::text
    FROM pg_catalog.jsonb_array_elements(
      v_node_input->'bindings'
    ) AS item(binding)
    WHERE workflow_input_uuid_is_exact(
      binding->'source'->'inputManifest'->>'id'
    ) IS TRUE
    UNION
    SELECT (pin->'manifest'->>'id')::uuid,'predecessor'::text
    FROM pg_catalog.jsonb_array_elements(
      v_node_input->'bindings'
    ) AS item(binding)
    CROSS JOIN LATERAL pg_catalog.jsonb_array_elements(
      COALESCE(binding->'source'->'proposalPins','[]'::jsonb)
    ) AS member(pin)
    WHERE workflow_input_uuid_is_exact(pin->'manifest'->>'id') IS TRUE
  )
  SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
    'contentHash',manifest.content_hash,
    'contentRef',manifest.content_ref,
    'contentStore',manifest.content_store,
    'manifestId',manifest.id::text,
    'role',referenced.role
  ) ORDER BY pg_catalog.convert_to(referenced.role,'UTF8'),manifest.id),
    '[]'::jsonb)
  INTO v_manifests
  FROM referenced
  JOIN input_manifests AS manifest ON manifest.id=referenced.manifest_id
  WHERE manifest.project_id=v_run.project_id;

  WITH referenced(purpose,revision_id) AS (
    SELECT 'workspace-target'::text,v_target.id
    UNION ALL
    SELECT source.purpose,source.revision_id
    FROM application_build_contract_sources AS source
    WHERE source.contract_id=v_build_contract.id
  )
  SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
    'contentHash',revision.content_hash,
    'contentRef',revision.content_ref,
    'contentStore',revision.content_store,
    'purpose',referenced.purpose,
    'revisionId',revision.id::text
  ) ORDER BY pg_catalog.convert_to(referenced.purpose,'UTF8'),
             revision.id),'[]'::jsonb)
  INTO v_revisions
  FROM referenced
  JOIN artifact_revisions AS revision ON revision.id=referenced.revision_id
  JOIN artifacts AS artifact ON artifact.id=revision.artifact_id
  WHERE artifact.project_id=v_run.project_id;

  SELECT pg_catalog.count(*) INTO v_required_receipt_count
  FROM application_build_contract_sources AS source
  JOIN artifact_revisions AS revision ON revision.id=source.revision_id
  JOIN qualification_policy_review_defaults AS review_rule
    ON review_rule.authority_id=v_policy.authority_id
   AND review_rule.change_source=revision.change_source
   AND review_rule.canonical_review_required
  WHERE source.contract_id=v_build_contract.id;
  SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
    'rawBytesHex',pg_catalog.encode(receipt.receipt_bytes,'hex'),
    'reviewRequestId',receipt.review_request_id::text
  ) ORDER BY receipt.review_request_id),'[]'::jsonb)
  INTO v_receipts
  FROM application_build_contract_sources AS source
  JOIN artifact_revisions AS revision ON revision.id=source.revision_id
  JOIN qualification_policy_review_defaults AS review_rule
    ON review_rule.authority_id=v_policy.authority_id
   AND review_rule.change_source=revision.change_source
   AND review_rule.canonical_review_required
  JOIN canonical_review_approval_receipts AS receipt
    ON receipt.project_id=v_run.project_id
   AND receipt.revision_id=revision.id
  WHERE source.contract_id=v_build_contract.id;
  IF pg_catalog.jsonb_array_length(v_manifests)<1
     OR pg_catalog.jsonb_array_length(v_revisions)<1
     OR pg_catalog.jsonb_array_length(v_revisions)<>
          v_build_contract.source_count+1
     OR pg_catalog.jsonb_array_length(v_receipts)<>
          v_required_receipt_count THEN
    RAISE EXCEPTION 'v3 Quality material-plan graph is incomplete'
      USING ERRCODE='WQC03';
  END IF;
  RETURN pg_catalog.jsonb_build_object(
    'buildContract',pg_catalog.jsonb_build_object(
      'contentHash',v_build_contract.content_hash,
      'contentRef',v_build_contract.content_ref,
      'contentStore',v_build_contract.content_store
    ),
    'buildManifest',pg_catalog.jsonb_build_object(
      'contentHash',v_build_manifest.content_hash,
      'contentRef',v_build_manifest.content_ref,
      'contentStore',v_build_manifest.content_store
    ),
    'definitionRawBytesHex',pg_catalog.encode(
      workflow_input_canonical_jsonb_bytes(v_definition.content),'hex'
    ),
    'inputManifests',v_manifests,
    'nodeInputRawBytesHex',pg_catalog.encode(p_node_input_raw_bytes,'hex'),
    'reviewReceipts',v_receipts,
    'revisions',v_revisions,
    'runScopeRawBytesHex',pg_catalog.encode(
      workflow_input_canonical_jsonb_bytes(v_run.scope),'hex'
    )
  );
END;
$function$;

DO $workflow_v3_quality_completion_security$
DECLARE
  v_schema text:=pg_catalog.current_schema();
  v_table text;
  v_signature text;
  v_role text;
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname='worksflow_workflow_input_authority_operator'
      AND (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole
        OR rolreplication OR rolbypassrls)
  ) THEN
    RAISE EXCEPTION
      'Workflow Input Authority operator must be a private NOLOGIN role'
      USING ERRCODE='42501';
  END IF;
  FOREACH v_table IN ARRAY ARRAY[
    'workflow_v3_quality_completion_precommits',
    'workflow_v3_quality_completion_materials',
    'workflow_v3_quality_completion_material_manifests',
    'workflow_v3_quality_completion_material_revisions',
    'workflow_v3_quality_completion_material_review_receipts',
    'workflow_v3_quality_completion_candidate_snapshots',
    'workflow_v3_quality_completion_identity_reservations'
  ] LOOP
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON TABLE %I.%I FROM PUBLIC',v_schema,v_table
    );
    IF EXISTS (
      SELECT 1 FROM pg_catalog.pg_roles
      WHERE rolname='worksflow_migration_owner'
    ) THEN
      EXECUTE pg_catalog.format(
        'ALTER TABLE %I.%I OWNER TO worksflow_migration_owner',
        v_schema,v_table
      );
    END IF;
  END LOOP;
  FOREACH v_signature IN ARRAY ARRAY[
    'workflow_v3_quality_completion_runtime_caller_is_exact_v1(text)',
    'inspect_workflow_v3_quality_completion_precommit_v1(uuid)',
    'resolve_workflow_v3_quality_completion_material_plan_v1(uuid,uuid,bytea)',
    'freeze_workflow_input_authority_from_quality_precommit_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)',
    'resolve_workflow_v3_quality_completion_candidate_v1(uuid)',
    'precommit_workflow_v3_quality_completion_v1(uuid,uuid,uuid,uuid,uuid,uuid,uuid,bigint,uuid,text,integer,timestamptz,jsonb,uuid,bytea)',
    'workflow_v3_quality_completion_material_bundle_v1(uuid)',
    'workflow_v3_quality_completion_snapshot_is_exact_v1(uuid)',
    'workflow_v3_quality_completion_require_primary_v1()',
    'workflow_v3_quality_completion_rfc3339_v1(timestamptz)',
    'reject_workflow_v3_quality_completion_mutation_v1()',
    'admit_workflow_v3_quality_completion_materials_v1(uuid,uuid,bytea,bytea,bytea,bytea,bytea,jsonb)',
    'workflow_v3_quality_completion_commit_is_exact_v1(uuid)',
    'guard_workflow_v3_quality_completion_workflow_mutation_v1()',
    'guard_workflow_v3_quality_completion_event_mutation_v1()',
    'validate_workflow_v3_quality_completion_closure_v1()'
  ] LOOP
    EXECUTE pg_catalog.format(
      'ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I',
      v_schema,v_signature,v_schema
    );
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC',v_schema,v_signature
    );
    IF EXISTS (
      SELECT 1 FROM pg_catalog.pg_roles
      WHERE rolname='worksflow_migration_owner'
    ) THEN
      EXECUTE pg_catalog.format(
        'ALTER FUNCTION %I.%s OWNER TO worksflow_migration_owner',
        v_schema,v_signature
      );
    END IF;
  END LOOP;
  FOREACH v_role IN ARRAY ARRAY[
    'worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_qualification_evidence_operator',
    'worksflow_qualification_plan_operator',
    'worksflow_qualification_policy_operator',
    'worksflow_workflow_input_authority_operator',
    'worksflow_qualification_receipt_operator',
    'worksflow_qualification_input_precommit_operator',
    'worksflow_qualification_source_verifier_operator',
    'worksflow_qualification_credential_resolver_operator',
    'worksflow_qualification_promotion_operator',
    'worksflow_qualification_handoff_operator',
    'worksflow_qualification_release_operator'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=v_role) THEN
      FOREACH v_table IN ARRAY ARRAY[
        'workflow_v3_quality_completion_precommits',
        'workflow_v3_quality_completion_materials',
        'workflow_v3_quality_completion_material_manifests',
        'workflow_v3_quality_completion_material_revisions',
        'workflow_v3_quality_completion_material_review_receipts',
        'workflow_v3_quality_completion_candidate_snapshots',
        'workflow_v3_quality_completion_identity_reservations'
      ] LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON TABLE %I.%I FROM %I',v_schema,v_table,v_role
        );
      END LOOP;
      FOREACH v_signature IN ARRAY ARRAY[
        'workflow_v3_quality_completion_runtime_caller_is_exact_v1(text)',
        'inspect_workflow_v3_quality_completion_precommit_v1(uuid)',
        'resolve_workflow_v3_quality_completion_material_plan_v1(uuid,uuid,bytea)',
        'freeze_workflow_input_authority_from_quality_precommit_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)',
        'resolve_workflow_v3_quality_completion_candidate_v1(uuid)',
        'precommit_workflow_v3_quality_completion_v1(uuid,uuid,uuid,uuid,uuid,uuid,uuid,bigint,uuid,text,integer,timestamptz,jsonb,uuid,bytea)',
        'workflow_v3_quality_completion_material_bundle_v1(uuid)',
        'workflow_v3_quality_completion_snapshot_is_exact_v1(uuid)',
        'workflow_v3_quality_completion_require_primary_v1()',
        'workflow_v3_quality_completion_rfc3339_v1(timestamptz)',
        'reject_workflow_v3_quality_completion_mutation_v1()',
        'admit_workflow_v3_quality_completion_materials_v1(uuid,uuid,bytea,bytea,bytea,bytea,bytea,jsonb)',
        'workflow_v3_quality_completion_commit_is_exact_v1(uuid)',
        'guard_workflow_v3_quality_completion_workflow_mutation_v1()',
        'guard_workflow_v3_quality_completion_event_mutation_v1()',
        'validate_workflow_v3_quality_completion_closure_v1()'
      ] LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON FUNCTION %I.%s FROM %I',
          v_schema,v_signature,v_role
        );
      END LOOP;
    END IF;
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='worksflow_application'
  ) THEN
    FOREACH v_signature IN ARRAY ARRAY[
      'resolve_workflow_v3_quality_completion_material_plan_v1(uuid,uuid,bytea)',
      'admit_workflow_v3_quality_completion_materials_v1(uuid,uuid,bytea,bytea,bytea,bytea,bytea,jsonb)',
      'precommit_workflow_v3_quality_completion_v1(uuid,uuid,uuid,uuid,uuid,uuid,uuid,bigint,uuid,text,integer,timestamptz,jsonb,uuid,bytea)',
      'inspect_workflow_v3_quality_completion_precommit_v1(uuid)',
      'freeze_workflow_input_authority_from_quality_precommit_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb)'
    ] LOOP
      EXECUTE pg_catalog.format(
        'GRANT EXECUTE ON FUNCTION %I.%s TO worksflow_application',
        v_schema,v_signature
      );
    END LOOP;
    EXECUTE pg_catalog.format(
      'REVOKE EXECUTE ON FUNCTION %I.freeze_workflow_input_authority_v1(uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb) FROM worksflow_application',
      v_schema
    );
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname='worksflow_workflow_input_authority_operator'
  ) THEN
    IF pg_catalog.has_schema_privilege(
      'worksflow_workflow_input_authority_operator',v_schema,'USAGE'
    ) IS NOT TRUE THEN
      EXECUTE pg_catalog.format(
        'COMMENT ON FUNCTION %I.workflow_v3_quality_completion_runtime_caller_is_exact_v1(text) IS %L',
        v_schema,
        '000085 granted schema USAGE to worksflow_workflow_input_authority_operator'
      );
      EXECUTE pg_catalog.format(
        'GRANT USAGE ON SCHEMA %I TO worksflow_workflow_input_authority_operator',
        v_schema
      );
    END IF;
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.resolve_workflow_v3_quality_completion_candidate_v1(uuid) TO worksflow_workflow_input_authority_operator',
      v_schema
    );
  END IF;
END;
$workflow_v3_quality_completion_security$;

COMMENT ON FUNCTION precommit_workflow_v3_quality_completion_v1(
  uuid,uuid,uuid,uuid,uuid,uuid,uuid,bigint,uuid,text,integer,
  timestamptz,jsonb,uuid,bytea
) IS 'Freezes one exact v3 Quality completion candidate before the Workflow completion mutation commits.';
COMMENT ON FUNCTION resolve_workflow_v3_quality_completion_candidate_v1(uuid)
IS 'Operator-only immutable resolver for one exact v3 Quality completion event.';
