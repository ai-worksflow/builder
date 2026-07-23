-- Migration 84 depends on the canonical-review forward-equivalence boundary
-- in migration 83. workflow-engine/v3 qualified release is a closed database authority. It
-- turns one authenticated ActionPublish into one exact production controller
-- operation and permits completion only from its immutable healthy result.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended(
    'worksflow:workflow-input-authority-migration:v1', 0
  )
);

LOCK TABLE projects IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_run_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_handoff_completions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_revision_authority_bindings IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_bundles IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_preview_receipts IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_promotion_approvals IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_deployment_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_delivery_operations IN ACCESS EXCLUSIVE MODE;

LOCK TABLE release_delivery_operation_results IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_production_receipts IN ACCESS EXCLUSIVE MODE;
LOCK TABLE release_deployment_revisions IN ACCESS EXCLUSIVE MODE;

-- A published draft-v3 fact cannot be reinterpreted under the qualified
-- dispatch identity.  Environments which contain one must allocate v4.
DO $qualification_release_v1_old_hash_fence$
DECLARE
  v_old text := 'aca0fbcc902ad0b51da4beb7df9c5f4ab58036540aa4046a3f62e848728b37ef';
BEGIN
  IF EXISTS (
       SELECT 1 FROM workflow_definition_versions
       WHERE execution_profile_hash = v_old OR content::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM workflow_runs
       WHERE execution_profile_hash = v_old OR context::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM workflow_input_authorities
       WHERE execution_profile_hash IN (v_old, 'sha256:' || v_old)
          OR authority_document::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM qualification_policy_authorities
       WHERE execution_profile_hash IN (v_old, 'sha256:' || v_old)
          OR authority_document::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_consumptions
       WHERE consumption_document::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_handoffs
       WHERE handoff_document::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_handoff_completions
       WHERE completion_document::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_revision_authority_bindings
       WHERE authority_document::text LIKE '%' || v_old || '%'
     ) OR EXISTS (
       SELECT 1 FROM artifact_revisions
       WHERE promotion_handoff_id IS NOT NULL
         AND change_summary LIKE '%' || v_old || '%'
     ) THEN
    RAISE EXCEPTION '000084 cannot reinterpret persisted draft workflow-engine/v3 authority; allocate workflow-engine/v4'
      USING ERRCODE = 'WQR02';
  END IF;
END;
$qualification_release_v1_old_hash_fence$;

CREATE FUNCTION qualification_release_v1_hash(p_domain text, p_value bytea)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT 'sha256:' || pg_catalog.encode(
    pg_catalog.sha256(
      pg_catalog.convert_to('worksflow-qualification-release-hash/v1', 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || pg_catalog.convert_to(p_domain, 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || p_value
    ),
    'hex'
  )
$function$;

CREATE FUNCTION qualification_release_v1_timestamp(p_value timestamptz)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT pg_catalog.to_char(
    p_value AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
  )
$function$;

CREATE FUNCTION qualification_release_v1_uuid_v4_is_exact(p_value uuid)
RETURNS boolean
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT workflow_input_uuid_is_exact(p_value::text)
     AND (pg_catalog.get_byte(pg_catalog.uuid_send(p_value), 6) >> 4) = 4
     AND (pg_catalog.get_byte(pg_catalog.uuid_send(p_value), 8) & 192) = 128
$function$;

CREATE TABLE qualification_release_v1_controller_bootstraps (
  bootstrap_id uuid PRIMARY KEY,
  controller_schema_version text NOT NULL CHECK (
    controller_schema_version = 'release-delivery-controller-identity/v1'
  ),
  controller_id text NOT NULL CHECK (
    controller_id = pg_catalog.btrim(controller_id)
    AND pg_catalog.octet_length(controller_id) BETWEEN 1 AND 200
  ),
  controller_version text NOT NULL CHECK (
    controller_version = pg_catalog.btrim(controller_version)
    AND pg_catalog.octet_length(controller_version) BETWEEN 1 AND 120
  ),
  controller_protocol text NOT NULL CHECK (
    controller_protocol = 'worksflow.release-delivery/v3'
  ),
  controller_trust_key_digest text NOT NULL CHECK (
    controller_trust_key_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  bootstrap_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(bootstrap_bytes) BETWEEN 1 AND 65536
  ),
  bootstrap_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(bootstrap_document) = 'object'
  ),
  bootstrap_hash text NOT NULL UNIQUE CHECK (
    bootstrap_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  bootstrapped_at timestamptz NOT NULL CHECK (
    bootstrapped_at = pg_catalog.date_trunc('milliseconds', bootstrapped_at)
  ),
  CHECK (qualification_release_v1_uuid_v4_is_exact(bootstrap_id)),
  CONSTRAINT qualification_release_v1_bootstrap_exact_unique
    UNIQUE (bootstrap_id, bootstrap_hash)
);

CREATE UNIQUE INDEX qualification_release_v1_controller_bootstrap_singleton
  ON qualification_release_v1_controller_bootstraps ((true));

CREATE TABLE qualification_release_v1_identity_reservations (
  identity_value uuid PRIMARY KEY,
  owner_kind text NOT NULL CHECK (owner_kind IN (
    'controller-bootstrap', 'authorization', 'authorization-operation',
    'action-event-outbox', 'production-controller-operation',
    'completion-event-outbox'
  )),
  owner_authorization_id uuid,
  ordinal smallint NOT NULL CHECK (ordinal BETWEEN 0 AND 5),
  reserved_at timestamptz NOT NULL CHECK (
    reserved_at = pg_catalog.date_trunc('milliseconds', reserved_at)
  ),
  CHECK (qualification_release_v1_uuid_v4_is_exact(identity_value)),
  CHECK (
    (owner_kind = 'controller-bootstrap'
      AND owner_authorization_id IS NULL AND ordinal = 0)
    OR (owner_kind <> 'controller-bootstrap'
      AND owner_authorization_id IS NOT NULL
      AND workflow_input_uuid_is_exact(owner_authorization_id::text))
  ),
  UNIQUE (owner_authorization_id, owner_kind),
  UNIQUE (owner_authorization_id, ordinal)
);

CREATE TABLE qualification_release_v1_authorizations (
  authorization_id uuid PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE,
  handoff_id uuid NOT NULL UNIQUE,
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  publish_node_run_id uuid NOT NULL UNIQUE,
  publish_node_key text NOT NULL CHECK (
    publish_node_key = pg_catalog.btrim(publish_node_key)
    AND pg_catalog.octet_length(publish_node_key) BETWEEN 1 AND 256
    AND publish_node_key ~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$'
  ),
  action_event_id uuid NOT NULL UNIQUE,
  completion_event_id uuid NOT NULL UNIQUE,
  action_event_sequence bigint NOT NULL CHECK (
    action_event_sequence BETWEEN 1 AND 9007199254740991
  ),
  actor_id uuid NOT NULL,
  actor_role text NOT NULL CHECK (actor_role IN ('owner','admin')),
  output_revision_id uuid NOT NULL UNIQUE,
  parent_revision_id uuid NOT NULL,
  artifact_id uuid NOT NULL,
  handoff_completion_hash text NOT NULL CHECK (
    handoff_completion_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  handoff_consumption_hash text NOT NULL CHECK (
    handoff_consumption_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  handoff_revision_authority_hash text NOT NULL CHECK (
    handoff_revision_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  release_bundle_id uuid NOT NULL,
  release_bundle_hash text NOT NULL CHECK (
    release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  canonical_receipt_id uuid NOT NULL,
  canonical_receipt_hash text NOT NULL CHECK (
    canonical_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  preview_receipt_id uuid NOT NULL,
  preview_receipt_hash text NOT NULL CHECK (
    preview_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  promotion_approval_id uuid NOT NULL,
  promotion_approval_hash text NOT NULL CHECK (
    promotion_approval_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  build_manifest_id uuid NOT NULL,
  build_manifest_hash text NOT NULL CHECK (
    build_manifest_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  build_contract_id uuid NOT NULL,
  build_contract_hash text NOT NULL CHECK (
    build_contract_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  release_request_key text NOT NULL CHECK (
    release_request_key = pg_catalog.btrim(release_request_key)
    AND pg_catalog.octet_length(release_request_key) BETWEEN 1 AND 128
  ),
  expected_production_run_id uuid NOT NULL UNIQUE,
  request_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(request_bytes) BETWEEN 1 AND 65536
  ),
  request_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(request_document) = 'object'
  ),
  request_hash text NOT NULL UNIQUE CHECK (
    request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  equivalence_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(equivalence_bytes) BETWEEN 1 AND 1048576
  ),
  equivalence_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(equivalence_document) = 'object'
  ),
  equivalence_hash text NOT NULL UNIQUE CHECK (
    equivalence_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  authorization_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(authorization_bytes) BETWEEN 1 AND 1048576
  ),
  authorization_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(authorization_document) = 'object'
  ),
  authorization_hash text NOT NULL UNIQUE CHECK (
    authorization_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  authorized_at timestamptz NOT NULL CHECK (
    authorized_at = pg_catalog.date_trunc('milliseconds', authorized_at)
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  CHECK (
    qualification_release_v1_uuid_v4_is_exact(authorization_id)
    AND qualification_release_v1_uuid_v4_is_exact(operation_id)
    AND qualification_release_v1_uuid_v4_is_exact(action_event_id)
    AND qualification_release_v1_uuid_v4_is_exact(completion_event_id)
    AND qualification_release_v1_uuid_v4_is_exact(expected_production_run_id)
    AND workflow_input_uuid_is_exact(handoff_id::text)
    AND workflow_input_uuid_is_exact(project_id::text)
    AND workflow_input_uuid_is_exact(workflow_run_id::text)
    AND workflow_input_uuid_is_exact(publish_node_run_id::text)
    AND workflow_input_uuid_is_exact(actor_id::text)
    AND workflow_input_uuid_is_exact(output_revision_id::text)
    AND workflow_input_uuid_is_exact(parent_revision_id::text)
    AND workflow_input_uuid_is_exact(artifact_id::text)
    AND output_revision_id <> parent_revision_id
    AND action_event_id <> completion_event_id
  ),
  UNIQUE (workflow_run_id, publish_node_run_id),
  UNIQUE (workflow_run_id, action_event_sequence),
  UNIQUE (project_id, release_request_key),
  FOREIGN KEY (handoff_id)
    REFERENCES qualification_promotion_v2_handoff_completions(handoff_id)
    ON DELETE RESTRICT,
  FOREIGN KEY (workflow_run_id, project_id)
    REFERENCES workflow_runs(id, project_id) ON DELETE RESTRICT,
  FOREIGN KEY (publish_node_run_id, workflow_run_id, publish_node_key)
    REFERENCES workflow_node_runs(id, run_id, node_key) ON DELETE RESTRICT,
  FOREIGN KEY (action_event_id, workflow_run_id, action_event_sequence)
    REFERENCES workflow_run_events(id, run_id, sequence)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY (output_revision_id)
    REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  FOREIGN KEY (parent_revision_id)
    REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  FOREIGN KEY (artifact_id)
    REFERENCES artifacts(id) ON DELETE RESTRICT,
  FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT,
  FOREIGN KEY (canonical_receipt_id, canonical_receipt_hash)
    REFERENCES canonical_verification_receipts(id, payload_hash) ON DELETE RESTRICT,
  FOREIGN KEY (preview_receipt_id, preview_receipt_hash)
    REFERENCES release_preview_receipts(id, payload_hash) ON DELETE RESTRICT,
  FOREIGN KEY (promotion_approval_id, promotion_approval_hash)
    REFERENCES release_promotion_approvals(id, payload_hash) ON DELETE RESTRICT,
  FOREIGN KEY (build_manifest_id)
    REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  FOREIGN KEY (build_contract_id)
    REFERENCES application_build_contracts(id) ON DELETE RESTRICT
);

CREATE TABLE qualification_release_v1_controller_bindings (
  authorization_id uuid PRIMARY KEY
    REFERENCES qualification_release_v1_authorizations(authorization_id)
      ON DELETE RESTRICT,
  lease_claim_event_id uuid NOT NULL UNIQUE,
  lease_claim_hash text NOT NULL CHECK (
    lease_claim_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  lease_attempt integer NOT NULL CHECK (
    lease_attempt BETWEEN 1 AND 2147483647
  ),
  lease_owner text NOT NULL CHECK (
    lease_owner = pg_catalog.btrim(lease_owner)
    AND pg_catalog.octet_length(lease_owner) BETWEEN 1 AND 200
  ),
  bootstrap_id uuid NOT NULL,
  bootstrap_hash text NOT NULL,
  project_id uuid NOT NULL,
  production_run_id uuid NOT NULL UNIQUE,
  controller_operation_id uuid NOT NULL UNIQUE,
  controller_request_hash text NOT NULL UNIQUE CHECK (
    controller_request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  controller_schema_version text NOT NULL,
  controller_id text NOT NULL,
  controller_version text NOT NULL,
  controller_protocol text NOT NULL,
  controller_trust_key_digest text NOT NULL,
  binding_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(binding_bytes) BETWEEN 1 AND 1048576
  ),
  binding_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(binding_document) = 'object'
  ),
  binding_hash text NOT NULL UNIQUE CHECK (
    binding_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  bound_at timestamptz NOT NULL CHECK (
    bound_at = pg_catalog.date_trunc('milliseconds', bound_at)
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  FOREIGN KEY (bootstrap_id, bootstrap_hash)
    REFERENCES qualification_release_v1_controller_bootstraps(
      bootstrap_id, bootstrap_hash
    ) ON DELETE RESTRICT,
  FOREIGN KEY (production_run_id)
    REFERENCES release_deployment_runs(id) ON DELETE RESTRICT
      DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY (controller_operation_id, controller_request_hash)
    REFERENCES release_delivery_operations(id, request_hash)
      ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

-- Every Workflow lease epoch has its own durable owner.  The claim event ID is
-- supplied by the server command so a commit-unknown retry can resolve the
-- exact epoch without allocating a second attempt.
CREATE TABLE qualification_release_v1_lease_claims (
  claim_event_id uuid PRIMARY KEY,
  authorization_id uuid NOT NULL,
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  publish_node_run_id uuid NOT NULL,
  publish_node_key text NOT NULL CHECK (
    publish_node_key = pg_catalog.btrim(publish_node_key)
    AND pg_catalog.octet_length(publish_node_key) BETWEEN 1 AND 256
    AND publish_node_key ~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$'
  ),
  event_sequence bigint NOT NULL CHECK (
    event_sequence BETWEEN 1 AND 9007199254740991
  ),
  lease_attempt integer NOT NULL CHECK (
    lease_attempt BETWEEN 1 AND 2147483647
  ),
  lease_owner text NOT NULL CHECK (
    lease_owner = pg_catalog.btrim(lease_owner)
    AND pg_catalog.octet_length(lease_owner) BETWEEN 1 AND 200
  ),
  lease_duration_milliseconds integer NOT NULL CHECK (
    lease_duration_milliseconds BETWEEN 1000 AND 300000
  ),
  claimed_at timestamptz NOT NULL CHECK (
    claimed_at = pg_catalog.date_trunc('milliseconds', claimed_at)
  ),
  initial_lease_expires_at timestamptz NOT NULL CHECK (
    initial_lease_expires_at =
      pg_catalog.date_trunc('milliseconds', initial_lease_expires_at)
    AND initial_lease_expires_at > claimed_at
    AND initial_lease_expires_at <= claimed_at + interval '5 minutes'
  ),
  claim_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(claim_bytes) BETWEEN 1 AND 65536
  ),
  claim_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(claim_document) = 'object'
  ),
  claim_hash text NOT NULL UNIQUE CHECK (
    claim_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  CHECK (
    qualification_release_v1_uuid_v4_is_exact(claim_event_id)
    AND workflow_input_uuid_is_exact(authorization_id::text)
    AND workflow_input_uuid_is_exact(project_id::text)
    AND workflow_input_uuid_is_exact(workflow_run_id::text)
    AND workflow_input_uuid_is_exact(publish_node_run_id::text)
    AND initial_lease_expires_at = claimed_at
      + lease_duration_milliseconds * interval '1 millisecond'
  ),
  UNIQUE (authorization_id, lease_attempt),
  UNIQUE (workflow_run_id, event_sequence),
  FOREIGN KEY (authorization_id)
    REFERENCES qualification_release_v1_authorizations(authorization_id)
      ON DELETE RESTRICT,
  FOREIGN KEY (workflow_run_id, project_id)
    REFERENCES workflow_runs(id, project_id) ON DELETE RESTRICT,
  FOREIGN KEY (publish_node_run_id, workflow_run_id, publish_node_key)
    REFERENCES workflow_node_runs(id, run_id, node_key) ON DELETE RESTRICT,
  FOREIGN KEY (claim_event_id, workflow_run_id, event_sequence)
    REFERENCES workflow_run_events(id, run_id, sequence)
      ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

ALTER TABLE qualification_release_v1_controller_bindings
  ADD CONSTRAINT qualification_release_v1_binding_claim_fk
  FOREIGN KEY (lease_claim_event_id)
  REFERENCES qualification_release_v1_lease_claims(claim_event_id)
  ON DELETE RESTRICT;

CREATE TABLE qualification_release_v1_results (
  authorization_id uuid PRIMARY KEY
    REFERENCES qualification_release_v1_authorizations(authorization_id)
      ON DELETE RESTRICT,
  outcome text NOT NULL CHECK (
    outcome IN (
      'healthy','production_failed','controller_rejected',
      'pre_submit_cancelled'
    )
  ),
  project_id uuid NOT NULL,
  production_run_id uuid NOT NULL UNIQUE,
  production_run_version bigint NOT NULL CHECK (
    production_run_version BETWEEN 1 AND 9007199254740991
  ),
  controller_operation_id uuid NOT NULL UNIQUE,
  controller_request_hash text NOT NULL,
  controller_result_hash text CHECK (
    controller_result_hash IS NULL
    OR controller_result_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  production_receipt_id uuid UNIQUE,
  production_receipt_hash text CHECK (
    production_receipt_hash IS NULL
    OR production_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  deployment_revision_id uuid UNIQUE,
  deployment_revision_hash text CHECK (
    deployment_revision_hash IS NULL
    OR deployment_revision_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  public_url text CHECK (
    public_url IS NULL OR (
      public_url = pg_catalog.btrim(public_url)
      AND pg_catalog.octet_length(public_url) BETWEEN 1 AND 2000
    )
  ),
  publish_result_document jsonb CHECK (
    publish_result_document IS NULL
    OR pg_catalog.jsonb_typeof(publish_result_document) = 'object'
  ),
  failure_document jsonb CHECK (
    failure_document IS NULL
    OR pg_catalog.jsonb_typeof(failure_document) = 'object'
  ),
  result_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(result_bytes) BETWEEN 1 AND 1048576
  ),
  result_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(result_document) = 'object'
  ),
  result_hash text NOT NULL UNIQUE CHECK (
    result_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  completed_at timestamptz NOT NULL CHECK (
    completed_at = pg_catalog.date_trunc('milliseconds', completed_at)
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  CHECK (
    (outcome='healthy'
      AND controller_result_hash IS NOT NULL
      AND production_receipt_id IS NOT NULL
      AND production_receipt_hash IS NOT NULL
      AND deployment_revision_id IS NOT NULL
      AND deployment_revision_hash IS NOT NULL
      AND public_url IS NOT NULL
      AND publish_result_document IS NOT NULL
      AND failure_document IS NULL)
    OR
    (outcome='production_failed'
      AND controller_result_hash IS NOT NULL
      AND production_receipt_id IS NOT NULL
      AND production_receipt_hash IS NOT NULL
      AND deployment_revision_id IS NULL
      AND deployment_revision_hash IS NULL
      AND public_url IS NULL
      AND publish_result_document IS NULL
      AND failure_document IS NOT NULL)
    OR
    (outcome='controller_rejected'
      AND controller_result_hash IS NOT NULL
      AND production_receipt_id IS NULL
      AND production_receipt_hash IS NULL
      AND deployment_revision_id IS NULL
      AND deployment_revision_hash IS NULL
      AND public_url IS NULL
      AND publish_result_document IS NULL
      AND failure_document IS NOT NULL)
    OR
    (outcome='pre_submit_cancelled'
      AND controller_result_hash IS NULL
      AND production_receipt_id IS NULL
      AND production_receipt_hash IS NULL
      AND deployment_revision_id IS NULL
      AND deployment_revision_hash IS NULL
      AND public_url IS NULL
      AND publish_result_document IS NULL
      AND failure_document IS NOT NULL)
  ),
  FOREIGN KEY (production_run_id)
    REFERENCES release_deployment_runs(id) ON DELETE RESTRICT,
  FOREIGN KEY (controller_operation_id, controller_request_hash)
    REFERENCES release_delivery_operations(id, request_hash) ON DELETE RESTRICT,
  FOREIGN KEY (controller_operation_id, controller_result_hash)
    REFERENCES release_delivery_operation_results(operation_id, result_hash)
      ON DELETE RESTRICT,
  FOREIGN KEY (production_receipt_id, production_receipt_hash)
    REFERENCES release_production_receipts(id, payload_hash) ON DELETE RESTRICT,
  FOREIGN KEY (deployment_revision_id, deployment_revision_hash)
    REFERENCES release_deployment_revisions(id, payload_hash) ON DELETE RESTRICT
);

CREATE TABLE qualification_release_v1_transaction_grants (
  authorization_id uuid NOT NULL,
  grant_kind text NOT NULL CHECK (
    grant_kind IN (
      'authorize_ready','claim_lease','renew_lease','healthy_complete',
      'terminal_fail'
    )
  ),
  target_kind text NOT NULL CHECK (target_kind IN ('run','node')),
  backend_pid integer NOT NULL CHECK (backend_pid > 0),
  transaction_id text NOT NULL CHECK (
    transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  publish_node_run_id uuid NOT NULL,
  production_run_id uuid NOT NULL,
  from_status text NOT NULL,
  to_status text NOT NULL,
  lease_claim_event_id uuid,
  lease_claim_event_sequence bigint,
  lease_owner text,
  lease_attempt integer,
  lease_expires_at timestamptz,
  granted_at timestamptz NOT NULL CHECK (
    granted_at = pg_catalog.date_trunc('milliseconds', granted_at)
  ),
  PRIMARY KEY (authorization_id, grant_kind, target_kind),
  FOREIGN KEY (authorization_id)
    REFERENCES qualification_release_v1_authorizations(authorization_id)
      ON DELETE RESTRICT,
  CHECK (
    (grant_kind = 'authorize_ready'
      AND lease_claim_event_id IS NULL
      AND lease_claim_event_sequence IS NULL
      AND lease_owner IS NULL AND lease_attempt IS NULL
      AND lease_expires_at IS NULL AND (
      (target_kind = 'node' AND from_status = 'waiting_input' AND to_status = 'ready')
      OR (target_kind = 'run' AND from_status = 'waiting_input' AND to_status = 'running')
    )) OR
    (grant_kind = 'claim_lease'
      AND lease_claim_event_id IS NOT NULL
      AND qualification_release_v1_uuid_v4_is_exact(lease_claim_event_id)
      AND lease_claim_event_sequence BETWEEN 1 AND 9007199254740991
      AND lease_owner = pg_catalog.btrim(lease_owner)
      AND pg_catalog.octet_length(lease_owner) BETWEEN 1 AND 200
      AND lease_attempt BETWEEN 1 AND 2147483647
      AND lease_expires_at = pg_catalog.date_trunc('milliseconds',lease_expires_at)
      AND (
        (target_kind='run' AND from_status='running' AND to_status='running')
        OR (target_kind='node' AND from_status IN ('ready','running')
          AND to_status='running')
      )
    ) OR
    (grant_kind = 'renew_lease' AND target_kind='node'
      AND from_status='running' AND to_status='running'
      AND lease_claim_event_id IS NOT NULL
      AND qualification_release_v1_uuid_v4_is_exact(lease_claim_event_id)
      AND lease_claim_event_sequence BETWEEN 1 AND 9007199254740991
      AND lease_owner = pg_catalog.btrim(lease_owner)
      AND pg_catalog.octet_length(lease_owner) BETWEEN 1 AND 200
      AND lease_attempt BETWEEN 1 AND 2147483647
      AND lease_expires_at = pg_catalog.date_trunc('milliseconds',lease_expires_at)
    ) OR
    (grant_kind = 'healthy_complete'
      AND lease_claim_event_id IS NOT NULL
      AND qualification_release_v1_uuid_v4_is_exact(lease_claim_event_id)
      AND lease_claim_event_sequence BETWEEN 1 AND 9007199254740991
      AND lease_owner = pg_catalog.btrim(lease_owner)
      AND pg_catalog.octet_length(lease_owner) BETWEEN 1 AND 200
      AND lease_attempt BETWEEN 1 AND 2147483647
      AND lease_expires_at = pg_catalog.date_trunc('milliseconds',lease_expires_at)
      AND (
      (target_kind = 'node' AND from_status = 'running' AND to_status = 'completed')
      OR (target_kind = 'run' AND from_status = 'running' AND to_status = 'completed')
    )) OR
    (grant_kind = 'terminal_fail'
      AND lease_claim_event_id IS NOT NULL
      AND qualification_release_v1_uuid_v4_is_exact(lease_claim_event_id)
      AND lease_claim_event_sequence BETWEEN 1 AND 9007199254740991
      AND lease_owner = pg_catalog.btrim(lease_owner)
      AND pg_catalog.octet_length(lease_owner) BETWEEN 1 AND 200
      AND lease_attempt BETWEEN 1 AND 2147483647
      AND lease_expires_at = pg_catalog.date_trunc('milliseconds',lease_expires_at)
      AND target_kind IN ('node','run')
      AND from_status='running' AND to_status='failed')
  )
);

CREATE INDEX qualification_release_v1_authorizations_project_idx
  ON qualification_release_v1_authorizations(project_id, authorized_at, authorization_id);
CREATE INDEX qualification_release_v1_authorizations_handoff_idx
  ON qualification_release_v1_authorizations(handoff_id, output_revision_id);
CREATE INDEX qualification_release_v1_bindings_project_idx
  ON qualification_release_v1_controller_bindings(project_id, production_run_id);
CREATE INDEX qualification_release_v1_results_project_idx
  ON qualification_release_v1_results(project_id, completed_at, authorization_id);
CREATE INDEX qualification_release_v1_lease_claims_authorization_idx
  ON qualification_release_v1_lease_claims(
    authorization_id, event_sequence, claim_event_id
  );
CREATE INDEX qualification_release_v1_grants_transaction_idx
  ON qualification_release_v1_transaction_grants(backend_pid, transaction_id);

CREATE FUNCTION reject_qualification_release_v1_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification Release v1 authority is immutable'
    USING ERRCODE = 'WQR02';
END;
$function$;

CREATE TRIGGER qualification_release_controller_bootstraps_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_release_v1_controller_bootstraps
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_release_v1_mutation();
CREATE TRIGGER qualification_release_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_release_v1_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_release_v1_mutation();
CREATE TRIGGER qualification_release_authorizations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_release_v1_authorizations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_release_v1_mutation();
CREATE TRIGGER qualification_release_controller_bindings_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_release_v1_controller_bindings
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_release_v1_mutation();
CREATE TRIGGER qualification_release_lease_claims_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_release_v1_lease_claims
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_release_v1_mutation();
CREATE TRIGGER qualification_release_results_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_release_v1_results
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_release_v1_mutation();

CREATE FUNCTION qualification_release_v1_runtime_caller_is_exact()
RETURNS boolean
LANGUAGE sql
STABLE PARALLEL SAFE
SECURITY INVOKER
AS $function$
  SELECT qualification_input_precommit_caller_is_v1(
    'worksflow_qualification_release_operator'
  )
$function$;

CREATE FUNCTION qualification_release_v1_bootstrap_caller_is_exact()
RETURNS boolean
LANGUAGE sql
STABLE PARALLEL SAFE
SECURITY INVOKER
AS $function$
  SELECT pg_catalog.current_setting('role') = 'none'
    AND (
      EXISTS (
        SELECT 1 FROM pg_catalog.pg_database AS database
        WHERE database.datname = pg_catalog.current_database()
          AND pg_catalog.pg_get_userbyid(database.datdba) = session_user::text
      )
      OR (
        EXISTS (
          SELECT 1 FROM pg_catalog.pg_roles
          WHERE rolname = 'worksflow_migration_owner'
        )
        AND pg_catalog.pg_has_role(
          session_user, 'worksflow_migration_owner', 'MEMBER'
        )
      )
    )
$function$;

CREATE FUNCTION qualification_release_v1_require_primary()
RETURNS void
LANGUAGE plpgsql
STABLE PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
BEGIN
  IF pg_catalog.pg_is_in_recovery()
     OR pg_catalog.current_setting('transaction_read_only') <> 'off' THEN
    RAISE EXCEPTION 'Qualification Release v1 requires a read-write primary'
      USING ERRCODE = 'WQR03';
  END IF;
END;
$function$;

CREATE FUNCTION qualification_release_v1_controller_bootstrap_is_exact(
  p_bootstrap_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_expected jsonb;
BEGIN
  SELECT * INTO v_bootstrap
  FROM qualification_release_v1_controller_bootstraps
  WHERE bootstrap_id = p_bootstrap_id;
  IF NOT FOUND THEN RETURN false; END IF;
  v_expected := pg_catalog.jsonb_build_object(
    'bootstrapId', v_bootstrap.bootstrap_id::text,
    'controller', pg_catalog.jsonb_build_object(
      'id', v_bootstrap.controller_id,
      'protocol', v_bootstrap.controller_protocol,
      'schemaVersion', v_bootstrap.controller_schema_version,
      'trustKeyDigest', v_bootstrap.controller_trust_key_digest,
      'version', v_bootstrap.controller_version
    ),
    'schemaVersion',
      'worksflow-qualification-release-controller-bootstrap/v1'
  );
  RETURN v_bootstrap.bootstrap_document = v_expected
    AND v_bootstrap.bootstrap_bytes
      = workflow_input_canonical_jsonb_bytes(v_expected)
    AND v_bootstrap.bootstrap_hash = qualification_release_v1_hash(
      'worksflow.qualification-release.controller-bootstrap/v1',
      v_bootstrap.bootstrap_bytes
    )
    AND EXISTS (
      SELECT 1 FROM qualification_release_v1_identity_reservations
      WHERE identity_value = v_bootstrap.bootstrap_id
        AND owner_kind = 'controller-bootstrap'
        AND owner_authorization_id IS NULL
        AND ordinal = 0
        AND reserved_at = v_bootstrap.bootstrapped_at
    )
    AND (SELECT pg_catalog.count(*)
         FROM qualification_release_v1_controller_bootstraps) = 1;
EXCEPTION WHEN OTHERS THEN
  RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_bootstrap_bundle(
  p_bootstrap_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_bundle jsonb;
BEGIN
  IF qualification_release_v1_controller_bootstrap_is_exact(
       p_bootstrap_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release Controller bootstrap is corrupt'
      USING ERRCODE = 'WQR02';
  END IF;
  SELECT * INTO STRICT v_bootstrap
  FROM qualification_release_v1_controller_bootstraps
  WHERE bootstrap_id = p_bootstrap_id;
  v_bundle := pg_catalog.jsonb_build_object(
    'schemaVersion',
      'worksflow-qualification-release-controller-bootstrap-bundle/v1',
    'bootstrapId', v_bootstrap.bootstrap_id::text,
    'bootstrap', pg_catalog.jsonb_build_object(
      'hash', v_bootstrap.bootstrap_hash,
      'bytesHex', pg_catalog.encode(v_bootstrap.bootstrap_bytes, 'hex'),
      'document', v_bootstrap.bootstrap_document
    )
  );
  IF p_include_idempotent THEN
    v_bundle := v_bundle || pg_catalog.jsonb_build_object(
      'idempotent', COALESCE(p_idempotent, false)
    );
  END IF;
  RETURN v_bundle;
END;
$function$;

CREATE FUNCTION bootstrap_qualification_release_controller_v1(
  p_bootstrap_id uuid,
  p_controller_id text,
  p_controller_version text,
  p_controller_protocol text,
  p_controller_trust_key_digest text
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_existing qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_now timestamptz;
  v_document jsonb;
  v_bytes bytea;
  v_hash text;
BEGIN
  -- Bootstrap is a durable mutation and therefore participates in the same
  -- rollout fence as every runtime mutation before touching any relation.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1', 0
    )
  );
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_bootstrap_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Controller bootstrap caller is not the migration credential'
      USING ERRCODE = '42501';
  END IF;
  IF p_bootstrap_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_bootstrap_id) IS NOT TRUE
     OR p_controller_id IS NULL
     OR p_controller_id <> pg_catalog.btrim(p_controller_id)
     OR pg_catalog.octet_length(p_controller_id) NOT BETWEEN 1 AND 200
     OR p_controller_version IS NULL
     OR p_controller_version <> pg_catalog.btrim(p_controller_version)
     OR pg_catalog.octet_length(p_controller_version) NOT BETWEEN 1 AND 120
     OR p_controller_protocol IS DISTINCT FROM 'worksflow.release-delivery/v3'
     OR p_controller_trust_key_digest IS NULL
     OR p_controller_trust_key_digest !~ '^sha256:[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'Controller bootstrap request is invalid'
      USING ERRCODE = 'WQR01';
  END IF;
  v_document := pg_catalog.jsonb_build_object(
    'bootstrapId', p_bootstrap_id::text,
    'controller', pg_catalog.jsonb_build_object(
      'id', p_controller_id,
      'protocol', p_controller_protocol,
      'schemaVersion', 'release-delivery-controller-identity/v1',
      'trustKeyDigest', p_controller_trust_key_digest,
      'version', p_controller_version
    ),
    'schemaVersion',
      'worksflow-qualification-release-controller-bootstrap/v1'
  );
  v_bytes := workflow_input_canonical_jsonb_bytes(v_document);
  v_hash := qualification_release_v1_hash(
    'worksflow.qualification-release.controller-bootstrap/v1', v_bytes
  );
  LOCK TABLE qualification_release_v1_controller_bootstraps
    IN SHARE ROW EXCLUSIVE MODE;
  SELECT * INTO v_existing
  FROM qualification_release_v1_controller_bootstraps LIMIT 1 FOR UPDATE;
  IF FOUND THEN
    IF v_existing.bootstrap_id IS DISTINCT FROM p_bootstrap_id
       OR v_existing.bootstrap_bytes IS DISTINCT FROM v_bytes
       OR v_existing.bootstrap_hash IS DISTINCT FROM v_hash
       OR qualification_release_v1_controller_bootstrap_is_exact(
            v_existing.bootstrap_id
          ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Controller bootstrap conflicts with immutable v1 identity'
        USING ERRCODE = 'WQR02';
    END IF;
    RETURN NEXT qualification_release_v1_bootstrap_bundle(
      p_bootstrap_id, true, true
    );
    RETURN;
  END IF;
  v_now := pg_catalog.date_trunc(
    'milliseconds', pg_catalog.clock_timestamp()
  );
  INSERT INTO qualification_release_v1_identity_reservations(
    identity_value, owner_kind, owner_authorization_id, ordinal, reserved_at
  ) VALUES (
    p_bootstrap_id, 'controller-bootstrap', NULL, 0, v_now
  );
  INSERT INTO qualification_release_v1_controller_bootstraps(
    bootstrap_id, controller_schema_version, controller_id,
    controller_version, controller_protocol, controller_trust_key_digest,
    bootstrap_bytes, bootstrap_document, bootstrap_hash,
    creation_transaction_id, bootstrapped_at
  ) VALUES (
    p_bootstrap_id, 'release-delivery-controller-identity/v1',
    p_controller_id, p_controller_version, p_controller_protocol,
    p_controller_trust_key_digest, v_bytes, v_document, v_hash,
    pg_catalog.pg_current_xact_id()::text, v_now
  );
  IF qualification_release_v1_controller_bootstrap_is_exact(
       p_bootstrap_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Controller bootstrap just-written closure is not exact'
      USING ERRCODE = 'WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_bootstrap_bundle(
    p_bootstrap_id, true, false
  );
END;
$function$;

CREATE FUNCTION inspect_qualification_release_controller_bootstrap_v1()
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_bootstrap_id uuid;
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE
     AND qualification_release_v1_bootstrap_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Controller bootstrap inspection caller is unauthorized'
      USING ERRCODE = '42501';
  END IF;
  SELECT bootstrap_id INTO v_bootstrap_id
  FROM qualification_release_v1_controller_bootstraps;
  IF NOT FOUND THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_bootstrap_bundle(
    v_bootstrap_id, false, false
  );
END;
$function$;

CREATE FUNCTION qualification_release_v1_workspace_revision_document(
  p_revision_id uuid
)
RETURNS jsonb
LANGUAGE sql
STABLE STRICT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
  SELECT pg_catalog.jsonb_build_object(
    'byteSize', revision.byte_size,
    'contentHash', workflow_input_normalize_sha256(revision.content_hash),
    'contentRef', revision.content_ref,
    'contentStore', revision.content_store,
    'id', revision.id::text,
    'implementationProposalId', CASE
      WHEN revision.implementation_proposal_id IS NULL THEN 'null'::jsonb
      ELSE pg_catalog.to_jsonb(revision.implementation_proposal_id::text)
    END,
    'proposalId', CASE
      WHEN revision.proposal_id IS NULL THEN 'null'::jsonb
      ELSE pg_catalog.to_jsonb(revision.proposal_id::text)
    END,
    'schemaVersion', revision.schema_version,
    'sourceManifestId', revision.source_manifest_id::text
  )
  FROM artifact_revisions AS revision
  WHERE revision.id = p_revision_id
    AND revision.source_manifest_id IS NOT NULL
    AND revision.byte_size BETWEEN 0 AND 9007199254740991
    AND workflow_input_normalize_sha256(revision.content_hash)
          ~ '^sha256:[0-9a-f]{64}$'
$function$;

CREATE FUNCTION qualification_release_v1_authorization_is_exact(
  p_authorization_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
  v_binding qualification_promotion_v2_revision_authority_bindings%ROWTYPE;
  v_consumption qualification_promotion_v2_consumptions%ROWTYPE;
  v_output artifact_revisions%ROWTYPE;
  v_parent artifact_revisions%ROWTYPE;
  v_bundle release_bundles%ROWTYPE;
  v_receipt canonical_verification_receipts%ROWTYPE;
  v_preview release_preview_receipts%ROWTYPE;
  v_approval release_promotion_approvals%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_definition workflow_definition_versions%ROWTYPE;
  v_publish workflow_node_runs%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
  v_publish_definition jsonb;
  v_required_role text;
  v_request jsonb;
  v_equivalence jsonb;
  v_actor jsonb;
  v_document jsonb;
BEGIN
  SELECT * INTO v_authorization
  FROM qualification_release_v1_authorizations
  WHERE authorization_id = p_authorization_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_completion
  FROM qualification_promotion_v2_handoff_completions
  WHERE handoff_id = v_authorization.handoff_id;
  SELECT * INTO v_binding
  FROM qualification_promotion_v2_revision_authority_bindings
  WHERE handoff_id = v_authorization.handoff_id;
  SELECT * INTO v_consumption
  FROM qualification_promotion_v2_consumptions
  WHERE operation_id = v_completion.operation_id;
  SELECT * INTO v_output FROM artifact_revisions
  WHERE id = v_authorization.output_revision_id;
  SELECT * INTO v_parent FROM artifact_revisions
  WHERE id = v_authorization.parent_revision_id;
  SELECT * INTO v_bundle FROM release_bundles
  WHERE id = v_authorization.release_bundle_id;
  SELECT * INTO v_receipt FROM canonical_verification_receipts
  WHERE id = v_authorization.canonical_receipt_id;
  SELECT * INTO v_preview FROM release_preview_receipts
  WHERE id = v_authorization.preview_receipt_id;
  SELECT * INTO v_approval FROM release_promotion_approvals
  WHERE id = v_authorization.promotion_approval_id;
  SELECT * INTO v_wia FROM workflow_input_authorities
  WHERE authority_id = v_consumption.workflow_input_authority_id;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id = v_authorization.workflow_run_id;
  SELECT * INTO v_definition FROM workflow_definition_versions
  WHERE id = v_run.definition_version_id;
  SELECT * INTO v_publish FROM workflow_node_runs
  WHERE id = v_authorization.publish_node_run_id;
  SELECT * INTO v_event FROM workflow_run_events
  WHERE id = v_authorization.action_event_id;
  IF v_completion.handoff_id IS NULL OR v_binding.handoff_id IS NULL
     OR v_consumption.operation_id IS NULL OR v_output.id IS NULL
     OR v_parent.id IS NULL OR v_bundle.id IS NULL OR v_receipt.id IS NULL
     OR v_preview.id IS NULL OR v_approval.id IS NULL OR v_wia.authority_id IS NULL
     OR v_run.id IS NULL OR v_definition.id IS NULL
     OR v_publish.id IS NULL OR v_event.id IS NULL THEN
    RETURN false;
  END IF;
  SELECT node.value INTO v_publish_definition
  FROM pg_catalog.jsonb_array_elements(v_definition.content->'nodes') AS node(value)
  WHERE node.value->>'id'=v_authorization.publish_node_key
    AND node.value->>'type'='publish';
  v_required_role:=v_publish_definition#>>'{publish,requiredRole}';
  v_request := pg_catalog.jsonb_build_object(
    'actionEventId', v_authorization.action_event_id::text,
    'authorizationId', v_authorization.authorization_id::text,
    'operationId', v_authorization.operation_id::text,
    'projectId', v_authorization.project_id::text,
    'publishNodeRunId', v_authorization.publish_node_run_id::text,
    'releaseRequestKey', v_authorization.release_request_key,
    'schemaVersion',
      'worksflow-qualification-release-authorization-request/v1',
    'workflowRunId', v_authorization.workflow_run_id::text
  );
  v_equivalence := pg_catalog.jsonb_build_object(
    'handoff', pg_catalog.jsonb_build_object(
      'completionHash', v_completion.completion_hash,
      'consumptionHash', v_consumption.consumption_hash,
      'handoffId', v_completion.handoff_id::text,
      'revisionAuthorityHash', v_binding.authority_hash
    ),
    'schemaVersion',
      'worksflow-qualification-release-workspace-equivalence/v1',
    'workspace', pg_catalog.jsonb_build_object(
      'artifactId', v_output.artifact_id::text,
      'output', qualification_release_v1_workspace_revision_document(
        v_output.id
      ),
      'parent', qualification_release_v1_workspace_revision_document(
        v_parent.id
      )
    )
  );
  v_actor := pg_catalog.jsonb_build_object(
    'action', 'publish',
    'actorId', v_authorization.actor_id::text,
    'authorizedAt', qualification_release_v1_timestamp(
      v_authorization.authorized_at
    ),
    'role', v_authorization.actor_role,
    'source', 'authenticated_command'
  );
  v_document := pg_catalog.jsonb_build_object(
    'authorizationId', v_authorization.authorization_id::text,
    'authorizedAt', qualification_release_v1_timestamp(
      v_authorization.authorized_at
    ),
    'equivalenceHash', v_authorization.equivalence_hash,
    'operationId', v_authorization.operation_id::text,
    'release', pg_catalog.jsonb_build_object(
      'buildContract', pg_catalog.jsonb_build_object(
        'hash', v_authorization.build_contract_hash,
        'id', v_authorization.build_contract_id::text
      ),
      'buildManifest', pg_catalog.jsonb_build_object(
        'hash', v_authorization.build_manifest_hash,
        'id', v_authorization.build_manifest_id::text
      ),
      'canonicalReceipt', pg_catalog.jsonb_build_object(
        'hash', v_authorization.canonical_receipt_hash,
        'id', v_authorization.canonical_receipt_id::text
      ),
      'expectedProductionRunId',
        v_authorization.expected_production_run_id::text,
      'previewReceipt', pg_catalog.jsonb_build_object(
        'hash', v_authorization.preview_receipt_hash,
        'id', v_authorization.preview_receipt_id::text
      ),
      'promotionApproval', pg_catalog.jsonb_build_object(
        'hash', v_authorization.promotion_approval_hash,
        'id', v_authorization.promotion_approval_id::text
      ),
      'releaseBundle', pg_catalog.jsonb_build_object(
        'hash', v_authorization.release_bundle_hash,
        'id', v_authorization.release_bundle_id::text
      ),
      'requestKey', v_authorization.release_request_key
    ),
    'schemaVersion', 'worksflow-qualification-release-authorization/v1',
    'workflow', pg_catalog.jsonb_build_object(
      'actionEvent', pg_catalog.jsonb_build_object(
        'id', v_authorization.action_event_id::text,
        'sequence', v_authorization.action_event_sequence,
        'type', 'node.execution_authorized'
      ),
      'actor', v_actor,
      'executionProfile', pg_catalog.jsonb_build_object(
        'hash',
          '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104',
        'version', 'workflow-engine/v3'
      ),
      'projectId', v_authorization.project_id::text,
      'publishNodeKey', v_authorization.publish_node_key,
      'publishNodeRunId', v_authorization.publish_node_run_id::text,
      'workflowRunId', v_authorization.workflow_run_id::text
    )
  );
  RETURN qualification_handoff_v1_completion_is_exact(
           v_authorization.handoff_id
         ) IS TRUE
    AND v_authorization.handoff_completion_hash = v_completion.completion_hash
    AND v_authorization.handoff_consumption_hash = v_consumption.consumption_hash
    AND v_authorization.handoff_revision_authority_hash = v_binding.authority_hash
    AND v_authorization.project_id = v_completion.project_id
    AND v_authorization.workflow_run_id = v_completion.workflow_run_id
    AND v_authorization.publish_node_run_id = v_completion.publish_node_run_id
    AND v_authorization.publish_node_key = v_completion.publish_node_key
    AND v_authorization.output_revision_id = v_completion.output_revision_id
    AND v_output.parent_revision_id = v_parent.id
    AND v_output.artifact_id = v_parent.artifact_id
    AND v_authorization.artifact_id = v_output.artifact_id
    AND ROW(
      v_output.artifact_id, v_output.schema_version, v_output.content_store,
      v_output.content_ref, workflow_input_normalize_sha256(v_output.content_hash),
      v_output.byte_size, v_output.source_manifest_id, v_output.proposal_id,
      v_output.implementation_proposal_id
    ) IS NOT DISTINCT FROM ROW(
      v_parent.artifact_id, v_parent.schema_version, v_parent.content_store,
      v_parent.content_ref, workflow_input_normalize_sha256(v_parent.content_hash),
      v_parent.byte_size, v_parent.source_manifest_id, v_parent.proposal_id,
      v_parent.implementation_proposal_id
    )
    AND v_bundle.project_id = v_authorization.project_id
    AND v_bundle.workspace_artifact_id = v_parent.artifact_id
    AND v_bundle.workspace_revision_id = v_parent.id
    AND workflow_input_normalize_sha256(v_bundle.workspace_content_hash)
      = workflow_input_normalize_sha256(v_parent.content_hash)
    AND v_bundle.bundle_hash = v_authorization.release_bundle_hash
    AND v_bundle.canonical_receipt_id = v_authorization.canonical_receipt_id
    AND v_bundle.canonical_receipt_hash = v_authorization.canonical_receipt_hash
    AND v_receipt.project_id = v_authorization.project_id
    AND v_receipt.workspace_artifact_id = v_parent.artifact_id
    AND v_receipt.workspace_revision_id = v_parent.id
    AND v_receipt.workspace_content_hash
      = workflow_input_normalize_sha256(v_parent.content_hash)
    AND v_receipt.decision = 'passed' AND v_receipt.blocker_count = 0
    AND v_receipt.build_manifest_id = v_wia.build_manifest_id
    AND v_receipt.build_manifest_hash = v_wia.build_manifest_hash
    AND v_receipt.build_contract_id = v_wia.build_contract_id
    AND v_receipt.build_contract_hash = v_wia.build_contract_hash
    AND v_authorization.build_manifest_id = v_wia.build_manifest_id
    AND v_authorization.build_manifest_hash = v_wia.build_manifest_hash
    AND v_authorization.build_contract_id = v_wia.build_contract_id
    AND v_authorization.build_contract_hash = v_wia.build_contract_hash
    AND v_preview.schema_version = 'release-preview-receipt/v2'
    AND v_preview.project_id = v_authorization.project_id
    AND v_preview.release_bundle_id = v_bundle.id
    AND v_preview.release_bundle_hash = v_bundle.bundle_hash
    AND v_preview.decision = 'passed'
    AND v_preview.payload_hash = v_authorization.preview_receipt_hash
    AND v_approval.project_id = v_authorization.project_id
    AND v_approval.release_bundle_id = v_bundle.id
    AND v_approval.release_bundle_hash = v_bundle.bundle_hash
    AND v_approval.preview_receipt_id = v_preview.id
    AND v_approval.preview_receipt_hash = v_preview.payload_hash
    AND v_approval.payload_hash = v_authorization.promotion_approval_hash
    AND v_authorization.request_document = v_request
    AND v_authorization.request_bytes
      = workflow_input_canonical_jsonb_bytes(v_request)
    AND v_authorization.request_hash = qualification_release_v1_hash(
      'worksflow.qualification-release.request/v1',
      v_authorization.request_bytes
    )
    AND v_authorization.equivalence_document = v_equivalence
    AND v_authorization.equivalence_bytes
      = workflow_input_canonical_jsonb_bytes(v_equivalence)
    AND v_authorization.equivalence_hash = qualification_release_v1_hash(
      'worksflow.qualification-release.equivalence/v1',
      v_authorization.equivalence_bytes
    )
    AND v_authorization.authorization_document = v_document
    AND v_authorization.authorization_bytes
      = workflow_input_canonical_jsonb_bytes(v_document)
    AND v_authorization.authorization_hash = qualification_release_v1_hash(
      'worksflow.qualification-release.authorization/v1',
      v_authorization.authorization_bytes
    )
    AND v_run.project_id = v_authorization.project_id
    AND v_run.execution_profile_version = 'workflow-engine/v3'
    AND v_run.execution_profile_hash
      = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
    AND (
      (v_run.status='running' AND v_run.completed_at IS NULL
        AND v_run.cancelled_at IS NULL AND v_run.failure IS NULL)
      OR
      (v_run.status='completed' AND v_run.completed_at IS NOT NULL
        AND v_run.cancelled_at IS NULL AND v_run.failure IS NULL)
      OR
      (v_run.status='failed' AND v_run.completed_at IS NOT NULL
        AND v_run.cancelled_at IS NULL AND v_run.failure IS NOT NULL)
    )
    AND v_definition.published IS TRUE
    AND v_definition.execution_profile_version='workflow-engine/v3'
    AND v_definition.execution_profile_hash
      ='854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
    AND workflow_execution_profile_v3_definition_is_database_admissible(
          v_definition.content
        ) IS TRUE
    AND v_publish_definition IS NOT NULL
    AND v_authorization.publish_node_key=v_publish_definition->>'id'
    AND pg_catalog.array_position(
          ARRAY['viewer','commenter','editor','admin','owner'],
          v_authorization.actor_role
        ) >= pg_catalog.array_position(
          ARRAY['viewer','commenter','editor','admin','owner'],v_required_role
        )
    AND v_run.event_cursor >= v_authorization.action_event_sequence
    AND v_run.context#>ARRAY[
      'nodes',v_authorization.publish_node_key,'executionActor'
    ] = v_actor
    AND v_publish.run_id = v_run.id
    AND v_publish.node_key = v_authorization.publish_node_key
    AND v_publish.node_type = 'publish'
    AND v_publish.status IN ('ready','running','completed','failed')
    AND v_publish.input_manifest_id IS NULL
    AND v_publish.output_proposal_id IS NULL
    AND v_publish.output_revision_id IS NULL
    AND v_event.run_id = v_run.id
    AND v_event.sequence = v_authorization.action_event_sequence
    AND v_event.event_type = 'node.execution_authorized'
    AND v_event.node_key = v_publish.node_key
    AND v_event.actor_id = v_authorization.actor_id
    AND v_event.created_at = v_authorization.authorized_at
    AND v_event.payload = pg_catalog.jsonb_build_object(
      'role', v_authorization.actor_role,
      'action', 'publish',
      'source', 'authenticated_command',
      'authorizedAt', qualification_release_v1_timestamp(
        v_authorization.authorized_at
      )
    )
    AND EXISTS (
      SELECT 1 FROM outbox_events AS outbox
      WHERE outbox.id = v_event.id
        AND outbox.aggregate_type = 'workflow_run'
        AND outbox.aggregate_id = v_run.id::text
        AND outbox.event_type = v_event.event_type
        AND outbox.subject = 'worksflow.workflow.run.event'
        AND outbox.headers = '{}'::jsonb
        AND outbox.created_at = v_event.created_at
        AND outbox.payload = pg_catalog.jsonb_build_object(
          'id', v_event.id::text,
          'projectId', v_authorization.project_id::text,
          'runId', v_run.id::text,
          'sequence', v_event.sequence,
          'type', v_event.event_type,
          'occurredAt', qualification_release_v1_timestamp(v_event.created_at),
          'payload', v_event.payload,
          'nodeKey', v_event.node_key,
          'actorId', v_event.actor_id::text
        )
    )
    AND (SELECT pg_catalog.count(*)
         FROM qualification_release_v1_identity_reservations
         WHERE owner_authorization_id = v_authorization.authorization_id) = 5
    AND NOT EXISTS (
      SELECT 1
      FROM (VALUES
        (v_authorization.authorization_id, 'authorization'::text, 0::smallint),
        (v_authorization.operation_id, 'authorization-operation'::text, 1::smallint),
        (v_authorization.action_event_id, 'action-event-outbox'::text, 2::smallint),
        (v_authorization.expected_production_run_id,
          'production-controller-operation'::text, 3::smallint),
        (v_authorization.completion_event_id,
          'completion-event-outbox'::text, 4::smallint)
      ) AS expected(identity_value,owner_kind,ordinal)
      LEFT JOIN qualification_release_v1_identity_reservations AS reservation
        ON reservation.identity_value = expected.identity_value
       AND reservation.owner_kind = expected.owner_kind
       AND reservation.ordinal = expected.ordinal
       AND reservation.owner_authorization_id = v_authorization.authorization_id
       AND reservation.reserved_at = v_authorization.authorized_at
      WHERE reservation.identity_value IS NULL
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_authorization_bundle(
  p_authorization_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_bundle jsonb;
BEGIN
  IF qualification_release_v1_authorization_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release authorization is corrupt'
      USING ERRCODE = 'WQR02';
  END IF;
  SELECT * INTO STRICT v_authorization
  FROM qualification_release_v1_authorizations
  WHERE authorization_id = p_authorization_id;
  v_bundle := pg_catalog.jsonb_build_object(
    'schemaVersion',
      'worksflow-qualification-release-authorization-bundle/v1',
    'operationId', v_authorization.operation_id::text,
    'authorizationId', v_authorization.authorization_id::text,
    'request', pg_catalog.jsonb_build_object(
      'hash', v_authorization.request_hash,
      'bytesHex', pg_catalog.encode(v_authorization.request_bytes, 'hex'),
      'document', v_authorization.request_document
    ),
    'equivalence', pg_catalog.jsonb_build_object(
      'hash', v_authorization.equivalence_hash,
      'bytesHex', pg_catalog.encode(v_authorization.equivalence_bytes, 'hex'),
      'document', v_authorization.equivalence_document
    ),
    'authorization', pg_catalog.jsonb_build_object(
      'hash', v_authorization.authorization_hash,
      'bytesHex', pg_catalog.encode(v_authorization.authorization_bytes, 'hex'),
      'document', v_authorization.authorization_document
    )
  );
  IF p_include_idempotent THEN
    v_bundle := v_bundle || pg_catalog.jsonb_build_object(
      'idempotent', COALESCE(p_idempotent, false)
    );
  END IF;
  RETURN v_bundle;
END;
$function$;

CREATE FUNCTION authorize_qualification_release_v1(
  p_operation_id uuid,
  p_authorization_id uuid,
  p_action_event_id uuid,
  p_release_request_key text,
  p_project_id uuid,
  p_workflow_run_id uuid,
  p_publish_node_run_id uuid,
  p_actor_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_existing qualification_release_v1_authorizations%ROWTYPE;
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
  v_binding qualification_promotion_v2_revision_authority_bindings%ROWTYPE;
  v_consumption qualification_promotion_v2_consumptions%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_definition workflow_definition_versions%ROWTYPE;
  v_publish workflow_node_runs%ROWTYPE;
  v_output artifact_revisions%ROWTYPE;
  v_parent artifact_revisions%ROWTYPE;
  v_bundle release_bundles%ROWTYPE;
  v_receipt canonical_verification_receipts%ROWTYPE;
  v_preview release_preview_receipts%ROWTYPE;
  v_approval release_promotion_approvals%ROWTYPE;
  v_bundle_id uuid;
  v_receipt_id uuid;
  v_preview_id uuid;
  v_approval_id uuid;
  v_actor_role text;
  v_publish_definition jsonb;
  v_publish_config jsonb;
  v_required_role text;
  v_release_count bigint;
  v_now timestamptz;
  v_expected_production_run_id uuid;
  v_completion_event_id uuid;
  v_request jsonb;
  v_request_bytes bytea;
  v_request_hash text;
  v_equivalence jsonb;
  v_equivalence_bytes bytea;
  v_equivalence_hash text;
  v_actor jsonb;
  v_document jsonb;
  v_document_bytes bytea;
  v_document_hash text;
  v_event_payload jsonb;
BEGIN
  -- The shared rollout fence is the first relation-facing entrypoint.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1', 0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release authorization caller is unauthorized'
      USING ERRCODE = '42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Release authorization requires SERIALIZABLE isolation'
      USING ERRCODE = 'WQR03';
  END IF;
  IF p_operation_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_operation_id) IS NOT TRUE
     OR p_authorization_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_authorization_id) IS NOT TRUE
     OR p_action_event_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_action_event_id) IS NOT TRUE
     OR p_project_id IS NULL
     OR workflow_input_uuid_is_exact(p_project_id::text) IS NOT TRUE
     OR p_workflow_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_publish_node_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_publish_node_run_id::text) IS NOT TRUE
     OR p_actor_id IS NULL
     OR workflow_input_uuid_is_exact(p_actor_id::text) IS NOT TRUE
     OR p_release_request_key IS NULL
     OR p_release_request_key <> pg_catalog.btrim(p_release_request_key)
     OR pg_catalog.octet_length(p_release_request_key) NOT BETWEEN 1 AND 128
     OR cardinality(ARRAY[
       p_operation_id,p_authorization_id,p_action_event_id
     ]) <> cardinality(ARRAY(
       SELECT DISTINCT value FROM pg_catalog.unnest(ARRAY[
         p_operation_id,p_authorization_id,p_action_event_id
       ]) AS identity(value)
     )) THEN
    RAISE EXCEPTION 'Qualification Release authorization request is invalid'
      USING ERRCODE = 'WQR01';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-v1:'
      || p_workflow_run_id::text || ':' || p_publish_node_run_id::text, 0
    )
  );

  SELECT * INTO v_existing
  FROM qualification_release_v1_authorizations
  WHERE operation_id = p_operation_id;
  IF FOUND THEN
    IF v_existing.authorization_id IS DISTINCT FROM p_authorization_id
       OR v_existing.action_event_id IS DISTINCT FROM p_action_event_id
       OR v_existing.release_request_key IS DISTINCT FROM p_release_request_key
       OR v_existing.project_id IS DISTINCT FROM p_project_id
       OR v_existing.workflow_run_id IS DISTINCT FROM p_workflow_run_id
       OR v_existing.publish_node_run_id IS DISTINCT FROM p_publish_node_run_id
       OR v_existing.actor_id IS DISTINCT FROM p_actor_id
       OR qualification_release_v1_authorization_is_exact(
            v_existing.authorization_id
          ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release authorization replay conflicts'
        USING ERRCODE = 'WQR02';
    END IF;
    RETURN NEXT qualification_release_v1_authorization_bundle(
      p_authorization_id, true, true
    );
    RETURN;
  END IF;

  -- Handoff is an untrusted locator until the project lock and full closure.
  SELECT * INTO v_completion
  FROM qualification_promotion_v2_handoff_completions
  WHERE workflow_run_id = p_workflow_run_id
    AND publish_node_run_id = p_publish_node_run_id;
  IF NOT FOUND
     OR (SELECT pg_catalog.count(*)
         FROM qualification_promotion_v2_handoff_completions
         WHERE workflow_run_id = p_workflow_run_id
           AND publish_node_run_id = p_publish_node_run_id) <> 1 THEN
    RAISE EXCEPTION 'Qualification Release Handoff is not ready'
      USING ERRCODE = 'WQR03';
  END IF;
  IF v_completion.project_id IS DISTINCT FROM p_project_id THEN
    RAISE EXCEPTION 'Qualification Release project locator disagrees'
      USING ERRCODE = 'WQR02';
  END IF;
  PERFORM 1 FROM projects
  WHERE id = p_project_id AND lifecycle = 'active' FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE = 'WQR03';
  END IF;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id = p_workflow_run_id FOR UPDATE;
  SELECT * INTO v_definition FROM workflow_definition_versions
  WHERE id = v_run.definition_version_id FOR SHARE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id = p_workflow_run_id ORDER BY id FOR UPDATE;
  SELECT * INTO v_publish FROM workflow_node_runs
  WHERE id = p_publish_node_run_id;
  SELECT * INTO v_completion
  FROM qualification_promotion_v2_handoff_completions
  WHERE handoff_id = v_completion.handoff_id FOR SHARE;
  SELECT * INTO v_binding
  FROM qualification_promotion_v2_revision_authority_bindings
  WHERE handoff_id = v_completion.handoff_id FOR SHARE;
  SELECT * INTO v_consumption
  FROM qualification_promotion_v2_consumptions
  WHERE operation_id = v_completion.operation_id FOR SHARE;
  SELECT * INTO v_wia FROM workflow_input_authorities
  WHERE authority_id = v_consumption.workflow_input_authority_id FOR SHARE;
  SELECT * INTO v_output FROM artifact_revisions
  WHERE id = v_completion.output_revision_id FOR SHARE;
  SELECT * INTO v_parent FROM artifact_revisions
  WHERE id = v_output.parent_revision_id FOR SHARE;
  SELECT node.value INTO v_publish_definition
  FROM pg_catalog.jsonb_array_elements(v_definition.content->'nodes') AS node(value)
  WHERE node.value->>'id'=v_completion.publish_node_key
    AND node.value->>'type'='publish';
  v_publish_config:=v_publish_definition->'publish';
  v_required_role:=v_publish_config->>'requiredRole';
  IF v_run.id IS NULL OR v_publish.id IS NULL OR v_binding.handoff_id IS NULL
     OR v_consumption.operation_id IS NULL OR v_wia.authority_id IS NULL
     OR v_output.id IS NULL OR v_parent.id IS NULL OR v_definition.id IS NULL
     OR v_publish_definition IS NULL
     OR qualification_handoff_v1_completion_is_exact(
          v_completion.handoff_id
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release Handoff closure is corrupt'
      USING ERRCODE = 'WQR02';
  END IF;
  IF v_run.project_id IS DISTINCT FROM p_project_id
     OR v_run.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
     OR v_run.execution_profile_hash IS DISTINCT FROM
       '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR v_run.status IS DISTINCT FROM 'waiting_input'
     OR v_run.completed_at IS NOT NULL OR v_run.cancelled_at IS NOT NULL
     OR v_run.failure IS NOT NULL
     OR v_publish.run_id IS DISTINCT FROM v_run.id
     OR v_publish.node_key IS DISTINCT FROM v_completion.publish_node_key
     OR v_publish.node_type IS DISTINCT FROM 'publish'
     OR v_publish.status IS DISTINCT FROM 'waiting_input'
     OR v_publish.attempt IS DISTINCT FROM 0
     OR v_publish.input_manifest_id IS NOT NULL
     OR v_publish.output_proposal_id IS NOT NULL
     OR v_publish.output_revision_id IS NOT NULL
     OR v_publish.lease_owner IS NOT NULL
     OR v_publish.lease_expires_at IS NOT NULL
     OR v_publish.started_at IS NOT NULL OR v_publish.completed_at IS NOT NULL
     OR v_publish.failure IS NOT NULL
     OR v_definition.published IS NOT TRUE
     OR v_definition.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
     OR v_definition.execution_profile_hash IS DISTINCT FROM
       '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR workflow_execution_profile_v3_definition_is_database_admissible(
          v_definition.content
        ) IS NOT TRUE
     OR v_publish_config IS NULL
     OR v_required_role NOT IN ('owner','admin','editor','commenter','viewer')
     OR v_run.context#>ARRAY[
          'nodes',v_publish.node_key,'executionActor'
        ] IS NOT NULL
     OR v_run.context#>ARRAY['nodes',v_publish.node_key,'output'] IS NOT NULL
     OR v_run.event_cursor IS DISTINCT FROM v_completion.event_cursor_after THEN
    RAISE EXCEPTION 'Qualification Release Publish state is not ready'
      USING ERRCODE = 'WQR03';
  END IF;
  SELECT role INTO v_actor_role FROM project_members
  WHERE project_id = p_project_id AND user_id = p_actor_id;
  IF v_actor_role NOT IN ('owner','admin')
     OR pg_catalog.array_position(
          ARRAY['viewer','commenter','editor','admin','owner'],v_actor_role
        ) < pg_catalog.array_position(
          ARRAY['viewer','commenter','editor','admin','owner'],v_required_role
        )
     OR NOT EXISTS (
       SELECT 1 FROM users WHERE id = p_actor_id AND disabled_at IS NULL
     ) THEN
    RAISE EXCEPTION 'Qualification Release actor is not current owner or admin'
      USING ERRCODE = '42501';
  END IF;
  IF v_output.parent_revision_id IS DISTINCT FROM v_parent.id
     OR v_output.artifact_id IS DISTINCT FROM v_parent.artifact_id
     OR v_output.source_manifest_id IS NULL
     OR v_parent.source_manifest_id IS NULL
     OR qualification_release_v1_workspace_revision_document(v_output.id) IS NULL
     OR qualification_release_v1_workspace_revision_document(v_parent.id) IS NULL
     OR ROW(
       v_output.artifact_id,v_output.schema_version,v_output.content_store,
       v_output.content_ref,workflow_input_normalize_sha256(v_output.content_hash),
       v_output.byte_size,v_output.source_manifest_id,v_output.proposal_id,
       v_output.implementation_proposal_id
     ) IS DISTINCT FROM ROW(
       v_parent.artifact_id,v_parent.schema_version,v_parent.content_store,
       v_parent.content_ref,workflow_input_normalize_sha256(v_parent.content_hash),
       v_parent.byte_size,v_parent.source_manifest_id,v_parent.proposal_id,
       v_parent.implementation_proposal_id
     ) THEN
    RAISE EXCEPTION 'Qualification Release Workspace equivalence is invalid'
      USING ERRCODE = 'WQR02';
  END IF;

  SELECT pg_catalog.count(*) INTO v_release_count
  FROM release_bundles AS bundle
  JOIN canonical_verification_receipts AS receipt
    ON receipt.id = bundle.canonical_receipt_id
   AND receipt.payload_hash = bundle.canonical_receipt_hash
  JOIN release_preview_receipts AS preview
    ON preview.release_bundle_id = bundle.id
   AND preview.release_bundle_hash = bundle.bundle_hash
   AND preview.schema_version = 'release-preview-receipt/v2'
   AND preview.decision = 'passed'
  JOIN release_promotion_approvals AS approval
    ON approval.release_bundle_id = bundle.id
   AND approval.release_bundle_hash = bundle.bundle_hash
   AND approval.preview_receipt_id = preview.id
   AND approval.preview_receipt_hash = preview.payload_hash
  WHERE bundle.project_id = p_project_id
    AND bundle.workspace_artifact_id = v_parent.artifact_id
    AND bundle.workspace_revision_id = v_parent.id
    AND bundle.workspace_content_hash
      = workflow_input_normalize_sha256(v_parent.content_hash)
    AND receipt.project_id = p_project_id
    AND receipt.workspace_artifact_id = v_parent.artifact_id
    AND receipt.workspace_revision_id = v_parent.id
    AND receipt.workspace_content_hash
      = workflow_input_normalize_sha256(v_parent.content_hash)
    AND receipt.decision = 'passed' AND receipt.blocker_count = 0
    AND receipt.build_manifest_id = v_wia.build_manifest_id
    AND receipt.build_manifest_hash = v_wia.build_manifest_hash
    AND receipt.build_contract_id = v_wia.build_contract_id
    AND receipt.build_contract_hash = v_wia.build_contract_hash;
  IF v_release_count <> 1 THEN
    RAISE EXCEPTION 'Qualification Release exact Bundle/Preview/Approval is not uniquely ready'
      USING ERRCODE = 'WQR03';
  END IF;
  SELECT bundle.id, receipt.id, preview.id, approval.id
  INTO v_bundle_id, v_receipt_id, v_preview_id, v_approval_id
  FROM release_bundles AS bundle
  JOIN canonical_verification_receipts AS receipt
    ON receipt.id = bundle.canonical_receipt_id
   AND receipt.payload_hash = bundle.canonical_receipt_hash
  JOIN release_preview_receipts AS preview
    ON preview.release_bundle_id = bundle.id
   AND preview.release_bundle_hash = bundle.bundle_hash
   AND preview.schema_version = 'release-preview-receipt/v2'
   AND preview.decision = 'passed'
  JOIN release_promotion_approvals AS approval
    ON approval.release_bundle_id = bundle.id
   AND approval.release_bundle_hash = bundle.bundle_hash
   AND approval.preview_receipt_id = preview.id
   AND approval.preview_receipt_hash = preview.payload_hash
  WHERE bundle.project_id = p_project_id
    AND bundle.workspace_artifact_id = v_parent.artifact_id
    AND bundle.workspace_revision_id = v_parent.id
    AND bundle.workspace_content_hash
      = workflow_input_normalize_sha256(v_parent.content_hash)
    AND receipt.project_id = p_project_id
    AND receipt.workspace_artifact_id = v_parent.artifact_id
    AND receipt.workspace_revision_id = v_parent.id
    AND receipt.workspace_content_hash
      = workflow_input_normalize_sha256(v_parent.content_hash)
    AND receipt.decision = 'passed' AND receipt.blocker_count = 0
    AND receipt.build_manifest_id = v_wia.build_manifest_id
    AND receipt.build_manifest_hash = v_wia.build_manifest_hash
    AND receipt.build_contract_id = v_wia.build_contract_id
    AND receipt.build_contract_hash = v_wia.build_contract_hash
  FOR SHARE OF bundle, receipt, preview, approval;
  SELECT * INTO v_bundle FROM release_bundles WHERE id = v_bundle_id;
  SELECT * INTO v_receipt FROM canonical_verification_receipts
  WHERE id = v_receipt_id;
  SELECT * INTO v_preview FROM release_preview_receipts
  WHERE id = v_preview_id;
  SELECT * INTO v_approval FROM release_promotion_approvals
  WHERE id = v_approval_id;

  v_now := pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp());
  v_expected_production_run_id := pg_catalog.gen_random_uuid();
  v_completion_event_id := pg_catalog.gen_random_uuid();
  v_request := pg_catalog.jsonb_build_object(
    'actionEventId', p_action_event_id::text,
    'authorizationId', p_authorization_id::text,
    'operationId', p_operation_id::text,
    'projectId', p_project_id::text,
    'publishNodeRunId', p_publish_node_run_id::text,
    'releaseRequestKey', p_release_request_key,
    'schemaVersion',
      'worksflow-qualification-release-authorization-request/v1',
    'workflowRunId', p_workflow_run_id::text
  );
  v_request_bytes := workflow_input_canonical_jsonb_bytes(v_request);
  v_request_hash := qualification_release_v1_hash(
    'worksflow.qualification-release.request/v1', v_request_bytes
  );
  v_equivalence := pg_catalog.jsonb_build_object(
    'handoff', pg_catalog.jsonb_build_object(
      'completionHash', v_completion.completion_hash,
      'consumptionHash', v_consumption.consumption_hash,
      'handoffId', v_completion.handoff_id::text,
      'revisionAuthorityHash', v_binding.authority_hash
    ),
    'schemaVersion',
      'worksflow-qualification-release-workspace-equivalence/v1',
    'workspace', pg_catalog.jsonb_build_object(
      'artifactId', v_output.artifact_id::text,
      'output', qualification_release_v1_workspace_revision_document(v_output.id),
      'parent', qualification_release_v1_workspace_revision_document(v_parent.id)
    )
  );
  v_equivalence_bytes := workflow_input_canonical_jsonb_bytes(v_equivalence);
  v_equivalence_hash := qualification_release_v1_hash(
    'worksflow.qualification-release.equivalence/v1', v_equivalence_bytes
  );
  v_actor := pg_catalog.jsonb_build_object(
    'action', 'publish', 'actorId', p_actor_id::text,
    'authorizedAt', qualification_release_v1_timestamp(v_now),
    'role', v_actor_role, 'source', 'authenticated_command'
  );
  v_document := pg_catalog.jsonb_build_object(
    'authorizationId', p_authorization_id::text,
    'authorizedAt', qualification_release_v1_timestamp(v_now),
    'equivalenceHash', v_equivalence_hash,
    'operationId', p_operation_id::text,
    'release', pg_catalog.jsonb_build_object(
      'buildContract', pg_catalog.jsonb_build_object(
        'hash', v_wia.build_contract_hash, 'id', v_wia.build_contract_id::text
      ),
      'buildManifest', pg_catalog.jsonb_build_object(
        'hash', v_wia.build_manifest_hash, 'id', v_wia.build_manifest_id::text
      ),
      'canonicalReceipt', pg_catalog.jsonb_build_object(
        'hash', v_bundle.canonical_receipt_hash,
        'id', v_bundle.canonical_receipt_id::text
      ),
      'expectedProductionRunId', v_expected_production_run_id::text,
      'previewReceipt', pg_catalog.jsonb_build_object(
        'hash', v_preview.payload_hash, 'id', v_preview.id::text
      ),
      'promotionApproval', pg_catalog.jsonb_build_object(
        'hash', v_approval.payload_hash, 'id', v_approval.id::text
      ),
      'releaseBundle', pg_catalog.jsonb_build_object(
        'hash', v_bundle.bundle_hash, 'id', v_bundle.id::text
      ),
      'requestKey', p_release_request_key
    ),
    'schemaVersion', 'worksflow-qualification-release-authorization/v1',
    'workflow', pg_catalog.jsonb_build_object(
      'actionEvent', pg_catalog.jsonb_build_object(
        'id', p_action_event_id::text,
        'sequence', v_run.event_cursor + 1,
        'type', 'node.execution_authorized'
      ),
      'actor', v_actor,
      'executionProfile', pg_catalog.jsonb_build_object(
        'hash',
          '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104',
        'version', 'workflow-engine/v3'
      ),
      'projectId', p_project_id::text,
      'publishNodeKey', v_publish.node_key,
      'publishNodeRunId', v_publish.id::text,
      'workflowRunId', v_run.id::text
    )
  );
  v_document_bytes := workflow_input_canonical_jsonb_bytes(v_document);
  v_document_hash := qualification_release_v1_hash(
    'worksflow.qualification-release.authorization/v1', v_document_bytes
  );

  INSERT INTO qualification_release_v1_identity_reservations(
    identity_value, owner_kind, owner_authorization_id, ordinal, reserved_at
  ) VALUES
    (p_authorization_id,'authorization',p_authorization_id,0,v_now),
    (p_operation_id,'authorization-operation',p_authorization_id,1,v_now),
    (p_action_event_id,'action-event-outbox',p_authorization_id,2,v_now),
    (v_expected_production_run_id,'production-controller-operation',
      p_authorization_id,3,v_now),
    (v_completion_event_id,'completion-event-outbox',
      p_authorization_id,4,v_now);
  INSERT INTO qualification_release_v1_authorizations(
    authorization_id,operation_id,handoff_id,project_id,workflow_run_id,
    publish_node_run_id,publish_node_key,action_event_id,completion_event_id,
    action_event_sequence,
    actor_id,actor_role,output_revision_id,parent_revision_id,artifact_id,
    handoff_completion_hash,handoff_consumption_hash,
    handoff_revision_authority_hash,release_bundle_id,release_bundle_hash,
    canonical_receipt_id,canonical_receipt_hash,preview_receipt_id,
    preview_receipt_hash,promotion_approval_id,promotion_approval_hash,
    build_manifest_id,build_manifest_hash,build_contract_id,build_contract_hash,
    release_request_key,expected_production_run_id,request_bytes,
    request_document,request_hash,equivalence_bytes,equivalence_document,
    equivalence_hash,authorization_bytes,authorization_document,
    authorization_hash,authorized_at,creation_transaction_id
  ) VALUES (
    p_authorization_id,p_operation_id,v_completion.handoff_id,p_project_id,
    p_workflow_run_id,p_publish_node_run_id,v_publish.node_key,
    p_action_event_id,v_completion_event_id,v_run.event_cursor+1,p_actor_id,v_actor_role,
    v_output.id,v_parent.id,v_output.artifact_id,v_completion.completion_hash,
    v_consumption.consumption_hash,v_binding.authority_hash,v_bundle.id,
    v_bundle.bundle_hash,v_bundle.canonical_receipt_id,
    v_bundle.canonical_receipt_hash,v_preview.id,v_preview.payload_hash,
    v_approval.id,v_approval.payload_hash,v_wia.build_manifest_id,
    v_wia.build_manifest_hash,v_wia.build_contract_id,v_wia.build_contract_hash,
    p_release_request_key,v_expected_production_run_id,v_request_bytes,
    v_request,v_request_hash,v_equivalence_bytes,v_equivalence,
    v_equivalence_hash,v_document_bytes,v_document,v_document_hash,v_now,
    pg_catalog.pg_current_xact_id()::text
  );
  INSERT INTO qualification_release_v1_transaction_grants(
    authorization_id,grant_kind,target_kind,backend_pid,transaction_id,
    project_id,workflow_run_id,publish_node_run_id,production_run_id,
    from_status,to_status,granted_at
  ) VALUES
    (p_authorization_id,'authorize_ready','run',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,p_project_id,p_workflow_run_id,
      p_publish_node_run_id,v_expected_production_run_id,
      'waiting_input','running',v_now),
    (p_authorization_id,'authorize_ready','node',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,p_project_id,p_workflow_run_id,
      p_publish_node_run_id,v_expected_production_run_id,
      'waiting_input','ready',v_now);
  v_event_payload := pg_catalog.jsonb_build_object(
    'role', v_actor_role, 'action', 'publish',
    'source', 'authenticated_command',
    'authorizedAt', qualification_release_v1_timestamp(v_now)
  );
  INSERT INTO workflow_run_events(
    id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
  ) VALUES (
    p_action_event_id,p_workflow_run_id,v_run.event_cursor+1,
    'node.execution_authorized',v_publish.node_key,v_event_payload,p_actor_id,v_now
  );
  INSERT INTO outbox_events(
    id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
    attempts,available_at,published_at,last_error,created_at
  ) VALUES (
    p_action_event_id,'workflow_run',p_workflow_run_id::text,
    'node.execution_authorized','worksflow.workflow.run.event',
    pg_catalog.jsonb_build_object(
      'id',p_action_event_id::text,'projectId',p_project_id::text,
      'runId',p_workflow_run_id::text,'sequence',v_run.event_cursor+1,
      'type','node.execution_authorized',
      'occurredAt',qualification_release_v1_timestamp(v_now),
      'payload',v_event_payload,'nodeKey',v_publish.node_key,
      'actorId',p_actor_id::text
    ),'{}'::jsonb,0,v_now,NULL,NULL,v_now
  );
  UPDATE workflow_runs
  SET status='running',event_cursor=v_run.event_cursor+1,
      context=pg_catalog.jsonb_set(
        context,ARRAY['nodes',v_publish.node_key,'executionActor'],v_actor,true
      ),updated_at=v_now
  WHERE id=v_run.id AND status='waiting_input'
    AND event_cursor=v_run.event_cursor;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release run authorization CAS failed'
      USING ERRCODE = 'WQR03';
  END IF;
  UPDATE workflow_node_runs
  SET status='ready',available_at=v_now,failure=NULL,completed_at=NULL,
      updated_at=v_now
  WHERE id=v_publish.id AND run_id=v_run.id AND status='waiting_input';
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release node authorization CAS failed'
      USING ERRCODE = 'WQR03';
  END IF;
  IF EXISTS (
       SELECT 1 FROM qualification_release_v1_transaction_grants
       WHERE authorization_id=p_authorization_id
     ) OR qualification_release_v1_authorization_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release authorization closure is incomplete'
      USING ERRCODE = 'WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_authorization_bundle(
    p_authorization_id,true,false
  );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Release immutable identity or closure conflict'
      USING ERRCODE = 'WQR02';
END;
$function$;

CREATE FUNCTION qualification_release_v1_lease_claim_is_exact(
  p_claim_event_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
  v_document jsonb;
BEGIN
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE claim_event_id=p_claim_event_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=v_claim.authorization_id;
  SELECT * INTO v_event FROM workflow_run_events
  WHERE id=v_claim.claim_event_id;
  IF v_authorization.authorization_id IS NULL OR v_event.id IS NULL THEN
    RETURN false;
  END IF;
  v_document:=pg_catalog.jsonb_build_object(
    'authorizationId',v_claim.authorization_id::text,
    'claimEvent',pg_catalog.jsonb_build_object(
      'id',v_claim.claim_event_id::text,
      'sequence',v_claim.event_sequence,
      'type','node.claimed'
    ),
    'claimedAt',qualification_release_v1_timestamp(v_claim.claimed_at),
    'lease',pg_catalog.jsonb_build_object(
      'attempt',v_claim.lease_attempt,
      'durationMilliseconds',v_claim.lease_duration_milliseconds,
      'initialExpiresAt',qualification_release_v1_timestamp(
        v_claim.initial_lease_expires_at
      ),
      'owner',v_claim.lease_owner
    ),
    'schemaVersion','worksflow-qualification-release-lease-claim/v1',
    'workflow',pg_catalog.jsonb_build_object(
      'projectId',v_claim.project_id::text,
      'publishNodeKey',v_claim.publish_node_key,
      'publishNodeRunId',v_claim.publish_node_run_id::text,
      'workflowRunId',v_claim.workflow_run_id::text
    )
  );
  RETURN qualification_release_v1_authorization_is_exact(
           v_claim.authorization_id
         ) IS TRUE
    AND v_claim.project_id=v_authorization.project_id
    AND v_claim.workflow_run_id=v_authorization.workflow_run_id
    AND v_claim.publish_node_run_id=v_authorization.publish_node_run_id
    AND v_claim.publish_node_key=v_authorization.publish_node_key
    AND v_claim.event_sequence=
          v_authorization.action_event_sequence+v_claim.lease_attempt
    AND v_claim.initial_lease_expires_at=v_claim.claimed_at
          + v_claim.lease_duration_milliseconds*interval '1 millisecond'
    AND v_claim.claim_document=v_document
    AND v_claim.claim_bytes=workflow_input_canonical_jsonb_bytes(v_document)
    AND v_claim.claim_hash=qualification_release_v1_hash(
      'worksflow.qualification-release.lease-claim/v1',v_claim.claim_bytes
    )
    AND v_event.run_id=v_claim.workflow_run_id
    AND v_event.sequence=v_claim.event_sequence
    AND v_event.event_type='node.claimed'
    AND v_event.node_key=v_claim.publish_node_key
    AND v_event.actor_id IS NULL
    AND v_event.created_at=v_claim.claimed_at
    AND v_event.payload=pg_catalog.jsonb_build_object(
      'attempt',v_claim.lease_attempt,
      'leaseExpiresAt',qualification_release_v1_timestamp(
        v_claim.initial_lease_expires_at
      ),
      'workerId',v_claim.lease_owner
    )
    AND EXISTS (
      SELECT 1 FROM outbox_events AS outbox
      WHERE outbox.id=v_claim.claim_event_id
        AND outbox.aggregate_type='workflow_run'
        AND outbox.aggregate_id=v_claim.workflow_run_id::text
        AND outbox.event_type='node.claimed'
        AND outbox.subject='worksflow.workflow.run.event'
        AND outbox.headers='{}'::jsonb
        AND outbox.created_at=v_claim.claimed_at
        AND outbox.payload=pg_catalog.jsonb_build_object(
          'id',v_claim.claim_event_id::text,
          'projectId',v_claim.project_id::text,
          'runId',v_claim.workflow_run_id::text,
          'sequence',v_claim.event_sequence,
          'type','node.claimed',
          'occurredAt',qualification_release_v1_timestamp(v_claim.claimed_at),
          'payload',v_event.payload,
          'nodeKey',v_claim.publish_node_key
        )
    )
    AND NOT EXISTS (
      SELECT 1 FROM qualification_release_v1_identity_reservations
      WHERE identity_value=v_claim.claim_event_id
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_lease_claim_chain_is_exact(
  p_authorization_id uuid,
  p_through_attempt integer
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_count bigint;
  v_min integer;
  v_max integer;
BEGIN
  IF p_through_attempt IS NULL OR p_through_attempt < 0 THEN RETURN false; END IF;
  SELECT pg_catalog.count(*),pg_catalog.min(lease_attempt),
         pg_catalog.max(lease_attempt)
  INTO v_count,v_min,v_max
  FROM qualification_release_v1_lease_claims
  WHERE authorization_id=p_authorization_id;
  IF p_through_attempt=0 THEN RETURN v_count=0; END IF;
  RETURN v_count=p_through_attempt AND v_min=1 AND v_max=p_through_attempt
    AND NOT EXISTS (
      SELECT 1 FROM qualification_release_v1_lease_claims AS claim
      WHERE claim.authorization_id=p_authorization_id
        AND qualification_release_v1_lease_claim_is_exact(
              claim.claim_event_id
            ) IS NOT TRUE
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_lease_claim_bundle(
  p_claim_event_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_bundle jsonb;
  v_active boolean;
BEGIN
  IF qualification_release_v1_lease_claim_is_exact(
       p_claim_event_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release lease claim is corrupt'
      USING ERRCODE='WQR02';
  END IF;
  SELECT * INTO STRICT v_claim FROM qualification_release_v1_lease_claims
  WHERE claim_event_id=p_claim_event_id;
  SELECT * INTO STRICT v_node FROM workflow_node_runs
  WHERE id=v_claim.publish_node_run_id;
  SELECT * INTO STRICT v_run FROM workflow_runs
  WHERE id=v_claim.workflow_run_id;
  v_active:=v_node.status='running'
    AND v_node.attempt=v_claim.lease_attempt
    AND v_node.lease_owner=v_claim.lease_owner
    AND v_node.lease_expires_at>pg_catalog.clock_timestamp()
    AND v_run.status='running'
    AND v_run.completed_at IS NULL AND v_run.cancelled_at IS NULL
    AND v_run.failure IS NULL
    AND v_run.event_cursor=v_claim.event_sequence
    AND qualification_release_v1_lease_claim_chain_is_exact(
          v_claim.authorization_id,v_node.attempt
        ) IS TRUE;
  v_bundle:=pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-lease-claim-bundle/v1',
    'claimEventId',v_claim.claim_event_id::text,
    'active',v_active,
    'currentLeaseExpiresAt',CASE WHEN v_active THEN pg_catalog.to_jsonb(
      qualification_release_v1_timestamp(v_node.lease_expires_at)
    ) ELSE 'null'::jsonb END,
    'claim',pg_catalog.jsonb_build_object(
      'hash',v_claim.claim_hash,
      'bytesHex',pg_catalog.encode(v_claim.claim_bytes,'hex'),
      'document',v_claim.claim_document
    )
  );
  IF p_include_idempotent THEN
    v_bundle:=v_bundle||pg_catalog.jsonb_build_object(
      'idempotent',COALESCE(p_idempotent,false)
    );
  END IF;
  RETURN v_bundle;
END;
$function$;

CREATE FUNCTION claim_qualification_release_publish_v1(
  p_authorization_id uuid,
  p_workflow_run_id uuid,
  p_publish_node_run_id uuid,
  p_claim_event_id uuid,
  p_lease_owner text,
  p_lease_duration_milliseconds integer
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_existing qualification_release_v1_lease_claims%ROWTYPE;
  v_previous qualification_release_v1_lease_claims%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_now timestamptz;
  v_expires_at timestamptz;
  v_attempt integer;
  v_sequence bigint;
  v_document jsonb;
  v_bytes bytea;
  v_hash text;
  v_event_payload jsonb;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release claim caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation')<>'serializable' THEN
    RAISE EXCEPTION 'Qualification Release claim requires SERIALIZABLE isolation'
      USING ERRCODE='WQR03';
  END IF;
  IF p_authorization_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_authorization_id) IS NOT TRUE
     OR p_workflow_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_publish_node_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_publish_node_run_id::text) IS NOT TRUE
     OR p_claim_event_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_claim_event_id) IS NOT TRUE
     OR p_lease_owner IS NULL OR p_lease_owner<>pg_catalog.btrim(p_lease_owner)
     OR pg_catalog.octet_length(p_lease_owner) NOT BETWEEN 1 AND 200
     OR p_lease_duration_milliseconds IS NULL
     OR p_lease_duration_milliseconds NOT BETWEEN 1000 AND 300000 THEN
    RAISE EXCEPTION 'Qualification Release claim request is invalid'
      USING ERRCODE='WQR01';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-v1:'||p_workflow_run_id::text||':'
      ||p_publish_node_run_id::text,0
    )
  );
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF v_authorization.workflow_run_id IS DISTINCT FROM p_workflow_run_id
     OR v_authorization.publish_node_run_id IS DISTINCT FROM
          p_publish_node_run_id THEN
    RAISE EXCEPTION 'Qualification Release claim target conflicts'
      USING ERRCODE='WQR02';
  END IF;
  PERFORM 1 FROM projects WHERE id=v_authorization.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=p_workflow_run_id FOR UPDATE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id=p_workflow_run_id ORDER BY id FOR UPDATE;
  SELECT * INTO v_node FROM workflow_node_runs
  WHERE id=p_publish_node_run_id;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_existing FROM qualification_release_v1_lease_claims
  WHERE claim_event_id=p_claim_event_id FOR SHARE;
  IF FOUND THEN
    IF v_existing.authorization_id IS DISTINCT FROM p_authorization_id
       OR v_existing.workflow_run_id IS DISTINCT FROM p_workflow_run_id
       OR v_existing.publish_node_run_id IS DISTINCT FROM p_publish_node_run_id
       OR v_existing.lease_owner IS DISTINCT FROM p_lease_owner
       OR v_existing.lease_duration_milliseconds IS DISTINCT FROM
            p_lease_duration_milliseconds
       OR qualification_release_v1_lease_claim_is_exact(
            p_claim_event_id
          ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release claim identity conflicts'
        USING ERRCODE='WQR02';
    END IF;
    RETURN NEXT qualification_release_v1_lease_claim_bundle(
      p_claim_event_id,true,true
    );
    RETURN;
  END IF;
  IF EXISTS (
       SELECT 1 FROM qualification_release_v1_identity_reservations
       WHERE identity_value=p_claim_event_id
     ) OR EXISTS (
       SELECT 1 FROM workflow_run_events WHERE id=p_claim_event_id
     ) OR EXISTS (
       SELECT 1 FROM outbox_events WHERE id=p_claim_event_id
     ) THEN
    RAISE EXCEPTION 'Qualification Release claim event identity is unavailable'
      USING ERRCODE='WQR02';
  END IF;
  IF qualification_release_v1_authorization_is_exact(
       p_authorization_id
     ) IS NOT TRUE
     OR v_run.id IS NULL OR v_node.id IS NULL
     OR v_run.status IS DISTINCT FROM 'running'
     OR v_run.completed_at IS NOT NULL OR v_run.failure IS NOT NULL
     OR v_node.run_id IS DISTINCT FROM v_run.id
     OR v_node.node_key IS DISTINCT FROM v_authorization.publish_node_key
     OR v_node.node_type IS DISTINCT FROM 'publish'
     OR v_run.event_cursor IS DISTINCT FROM
          v_authorization.action_event_sequence+v_node.attempt
     OR qualification_release_v1_lease_claim_chain_is_exact(
          p_authorization_id,v_node.attempt
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release claim authority is stale'
      USING ERRCODE='WQR03';
  END IF;
  v_now:=pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  IF v_node.status='ready' THEN
    IF v_node.attempt<>0 OR v_node.lease_owner IS NOT NULL
       OR v_node.lease_expires_at IS NOT NULL OR v_node.started_at IS NOT NULL THEN
      RAISE EXCEPTION 'Qualification Release ready lease is corrupt'
        USING ERRCODE='WQR02';
    END IF;
  ELSIF v_node.status='running' THEN
    IF v_node.lease_expires_at IS NULL OR v_node.lease_owner IS NULL
       OR v_node.lease_expires_at>=v_now THEN
      RAISE EXCEPTION 'Qualification Release Publish is not claimable'
        USING ERRCODE='WQR03';
    END IF;
    SELECT * INTO v_previous FROM qualification_release_v1_lease_claims
    WHERE authorization_id=p_authorization_id
      AND lease_attempt=v_node.attempt;
    IF NOT FOUND OR v_previous.lease_owner IS DISTINCT FROM v_node.lease_owner
       OR v_previous.event_sequence IS DISTINCT FROM v_run.event_cursor THEN
      RAISE EXCEPTION 'Qualification Release prior lease epoch is corrupt'
        USING ERRCODE='WQR02';
    END IF;
  ELSE
    RAISE EXCEPTION 'Qualification Release Publish is not claimable'
      USING ERRCODE='WQR03';
  END IF;
  v_attempt:=v_node.attempt+1;
  v_sequence:=v_run.event_cursor+1;
  v_expires_at:=v_now+p_lease_duration_milliseconds*interval '1 millisecond';
  v_document:=pg_catalog.jsonb_build_object(
    'authorizationId',p_authorization_id::text,
    'claimEvent',pg_catalog.jsonb_build_object(
      'id',p_claim_event_id::text,'sequence',v_sequence,'type','node.claimed'
    ),
    'claimedAt',qualification_release_v1_timestamp(v_now),
    'lease',pg_catalog.jsonb_build_object(
      'attempt',v_attempt,
      'durationMilliseconds',p_lease_duration_milliseconds,
      'initialExpiresAt',qualification_release_v1_timestamp(v_expires_at),
      'owner',p_lease_owner
    ),
    'schemaVersion','worksflow-qualification-release-lease-claim/v1',
    'workflow',pg_catalog.jsonb_build_object(
      'projectId',v_authorization.project_id::text,
      'publishNodeKey',v_authorization.publish_node_key,
      'publishNodeRunId',v_authorization.publish_node_run_id::text,
      'workflowRunId',v_authorization.workflow_run_id::text
    )
  );
  v_bytes:=workflow_input_canonical_jsonb_bytes(v_document);
  v_hash:=qualification_release_v1_hash(
    'worksflow.qualification-release.lease-claim/v1',v_bytes
  );
  v_event_payload:=pg_catalog.jsonb_build_object(
    'attempt',v_attempt,
    'leaseExpiresAt',qualification_release_v1_timestamp(v_expires_at),
    'workerId',p_lease_owner
  );
  INSERT INTO qualification_release_v1_lease_claims(
    claim_event_id,authorization_id,project_id,workflow_run_id,
    publish_node_run_id,publish_node_key,event_sequence,lease_attempt,
    lease_owner,lease_duration_milliseconds,claimed_at,
    initial_lease_expires_at,claim_bytes,claim_document,claim_hash,
    creation_transaction_id
  ) VALUES (
    p_claim_event_id,p_authorization_id,v_authorization.project_id,
    v_run.id,v_node.id,v_node.node_key,v_sequence,v_attempt,p_lease_owner,
    p_lease_duration_milliseconds,v_now,v_expires_at,v_bytes,v_document,v_hash,
    pg_catalog.pg_current_xact_id()::text
  );
  INSERT INTO qualification_release_v1_transaction_grants(
    authorization_id,grant_kind,target_kind,backend_pid,transaction_id,
    project_id,workflow_run_id,publish_node_run_id,production_run_id,
    from_status,to_status,lease_claim_event_id,
    lease_claim_event_sequence,lease_owner,lease_attempt,lease_expires_at,
    granted_at
  ) VALUES
    (p_authorization_id,'claim_lease','run',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,v_authorization.project_id,
      v_run.id,v_node.id,v_authorization.expected_production_run_id,
      'running','running',p_claim_event_id,v_sequence,p_lease_owner,
      v_attempt,v_expires_at,v_now),
    (p_authorization_id,'claim_lease','node',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,v_authorization.project_id,
      v_run.id,v_node.id,v_authorization.expected_production_run_id,
      v_node.status,'running',p_claim_event_id,v_sequence,p_lease_owner,
      v_attempt,v_expires_at,v_now);
  INSERT INTO workflow_run_events(
    id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
  ) VALUES (
    p_claim_event_id,v_run.id,v_sequence,'node.claimed',v_node.node_key,
    v_event_payload,NULL,v_now
  );
  INSERT INTO outbox_events(
    id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
    attempts,available_at,published_at,last_error,created_at
  ) VALUES (
    p_claim_event_id,'workflow_run',v_run.id::text,'node.claimed',
    'worksflow.workflow.run.event',pg_catalog.jsonb_build_object(
      'id',p_claim_event_id::text,
      'projectId',v_authorization.project_id::text,
      'runId',v_run.id::text,'sequence',v_sequence,'type','node.claimed',
      'occurredAt',qualification_release_v1_timestamp(v_now),
      'payload',v_event_payload,'nodeKey',v_node.node_key
    ),'{}'::jsonb,0,v_now,NULL,NULL,v_now
  );
  UPDATE workflow_runs
  SET event_cursor=v_sequence,updated_at=v_now
  WHERE id=v_run.id AND status='running' AND event_cursor=v_run.event_cursor;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release claim run CAS failed'
      USING ERRCODE='WQR03';
  END IF;
  UPDATE workflow_node_runs
  SET status='running',attempt=v_attempt,lease_owner=p_lease_owner,
      lease_expires_at=v_expires_at,started_at=COALESCE(started_at,v_now),
      updated_at=v_now
  WHERE id=v_node.id AND run_id=v_run.id AND status=v_node.status
    AND attempt=v_node.attempt
    AND lease_owner IS NOT DISTINCT FROM v_node.lease_owner
    AND lease_expires_at IS NOT DISTINCT FROM v_node.lease_expires_at;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release claim node CAS failed'
      USING ERRCODE='WQR03';
  END IF;
  IF EXISTS (
       SELECT 1 FROM qualification_release_v1_transaction_grants
       WHERE authorization_id=p_authorization_id
     ) OR qualification_release_v1_lease_claim_chain_is_exact(
       p_authorization_id,v_attempt
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release lease claim closure is incomplete'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_lease_claim_bundle(
    p_claim_event_id,true,false
  );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Release lease claim identity conflict'
      USING ERRCODE='WQR02';
END;
$function$;

CREATE FUNCTION inspect_qualification_release_publish_claim_v1(
  p_claim_event_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release claim inspection caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM qualification_release_v1_lease_claims
    WHERE claim_event_id=p_claim_event_id
  ) THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_lease_claim_bundle(
    p_claim_event_id,false,false
  );
END;
$function$;

CREATE FUNCTION renew_qualification_release_publish_lease_v1(
  p_authorization_id uuid,
  p_workflow_run_id uuid,
  p_publish_node_run_id uuid,
  p_claim_event_id uuid,
  p_lease_owner text,
  p_lease_attempt integer,
  p_expected_lease_expires_at timestamptz,
  p_new_lease_expires_at timestamptz
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_now timestamptz;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release renew caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation')<>'serializable' THEN
    RAISE EXCEPTION 'Qualification Release renew requires SERIALIZABLE isolation'
      USING ERRCODE='WQR03';
  END IF;
  IF p_authorization_id IS NULL OR p_workflow_run_id IS NULL
     OR p_publish_node_run_id IS NULL OR p_claim_event_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_claim_event_id) IS NOT TRUE
     OR p_lease_owner IS NULL OR p_lease_owner<>pg_catalog.btrim(p_lease_owner)
     OR pg_catalog.octet_length(p_lease_owner) NOT BETWEEN 1 AND 200
     OR p_lease_attempt IS NULL OR p_lease_attempt NOT BETWEEN 1 AND 2147483647
     OR p_expected_lease_expires_at IS NULL
     OR p_new_lease_expires_at IS NULL
     OR p_expected_lease_expires_at<>
          pg_catalog.date_trunc('milliseconds',p_expected_lease_expires_at)
     OR p_new_lease_expires_at<>
          pg_catalog.date_trunc('milliseconds',p_new_lease_expires_at)
     OR p_new_lease_expires_at<=p_expected_lease_expires_at THEN
    RAISE EXCEPTION 'Qualification Release renew request is invalid'
      USING ERRCODE='WQR01';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-v1:'||p_workflow_run_id::text||':'
      ||p_publish_node_run_id::text,0
    )
  );
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF v_authorization.workflow_run_id IS DISTINCT FROM p_workflow_run_id
     OR v_authorization.publish_node_run_id IS DISTINCT FROM
          p_publish_node_run_id THEN
    RAISE EXCEPTION 'Qualification Release renew target conflicts'
      USING ERRCODE='WQR02';
  END IF;
  PERFORM 1 FROM projects WHERE id=v_authorization.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=p_workflow_run_id FOR UPDATE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id=p_workflow_run_id ORDER BY id FOR UPDATE;
  SELECT * INTO v_node FROM workflow_node_runs
  WHERE id=p_publish_node_run_id;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE claim_event_id=p_claim_event_id FOR SHARE;
  IF NOT FOUND OR v_claim.authorization_id IS DISTINCT FROM p_authorization_id
     OR v_claim.lease_owner IS DISTINCT FROM p_lease_owner
     OR v_claim.lease_attempt IS DISTINCT FROM p_lease_attempt
     OR qualification_release_v1_lease_claim_is_exact(
          p_claim_event_id
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release renew claim is stale'
      USING ERRCODE='WQR03';
  END IF;
  v_now:=pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  IF v_run.status IS DISTINCT FROM 'running'
     OR v_run.completed_at IS NOT NULL OR v_run.cancelled_at IS NOT NULL
     OR v_run.failure IS NOT NULL
     OR v_run.event_cursor IS DISTINCT FROM v_claim.event_sequence
     OR v_node.status IS DISTINCT FROM 'running'
     OR v_node.lease_owner IS DISTINCT FROM p_lease_owner
     OR v_node.attempt IS DISTINCT FROM p_lease_attempt
     OR v_node.lease_expires_at IS NULL OR v_node.lease_expires_at<=v_now
     OR qualification_release_v1_lease_claim_chain_is_exact(
          p_authorization_id,p_lease_attempt
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release Workflow lease is stale'
      USING ERRCODE='WQR03';
  END IF;
  IF v_node.lease_expires_at IS NOT DISTINCT FROM p_new_lease_expires_at THEN
    RETURN NEXT qualification_release_v1_lease_claim_bundle(
      p_claim_event_id,true,true
    );
    RETURN;
  END IF;
  IF p_new_lease_expires_at>v_now+interval '5 minutes'
     OR v_node.lease_expires_at IS DISTINCT FROM p_expected_lease_expires_at
     OR p_new_lease_expires_at<=v_node.lease_expires_at THEN
    RAISE EXCEPTION 'Qualification Release Workflow lease is stale'
      USING ERRCODE='WQR03';
  END IF;
  INSERT INTO qualification_release_v1_transaction_grants(
    authorization_id,grant_kind,target_kind,backend_pid,transaction_id,
    project_id,workflow_run_id,publish_node_run_id,production_run_id,
    from_status,to_status,lease_claim_event_id,
    lease_claim_event_sequence,lease_owner,lease_attempt,lease_expires_at,
    granted_at
  ) VALUES (
    p_authorization_id,'renew_lease','node',pg_catalog.pg_backend_pid(),
    pg_catalog.pg_current_xact_id()::text,v_authorization.project_id,
    v_run.id,v_node.id,v_authorization.expected_production_run_id,
    'running','running',p_claim_event_id,v_claim.event_sequence,
    p_lease_owner,p_lease_attempt,p_new_lease_expires_at,v_now
  );
  UPDATE workflow_node_runs
  SET lease_expires_at=p_new_lease_expires_at,updated_at=v_now
  WHERE id=v_node.id AND status='running' AND attempt=p_lease_attempt
    AND lease_owner=p_lease_owner
    AND lease_expires_at=p_expected_lease_expires_at;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release renew CAS failed'
      USING ERRCODE='WQR03';
  END IF;
  IF EXISTS (
    SELECT 1 FROM qualification_release_v1_transaction_grants
    WHERE authorization_id=p_authorization_id
  ) THEN
    RAISE EXCEPTION 'Qualification Release renew grant was not consumed'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_lease_claim_bundle(
    p_claim_event_id,true,false
  );
END;
$function$;

CREATE FUNCTION inspect_qualification_release_operation_v1(p_operation_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE v_authorization_id uuid;
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release inspection caller is unauthorized'
      USING ERRCODE = '42501';
  END IF;
  SELECT authorization_id INTO v_authorization_id
  FROM qualification_release_v1_authorizations
  WHERE operation_id=p_operation_id;
  IF NOT FOUND THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_authorization_bundle(
    v_authorization_id,false,false
  );
END;
$function$;

CREATE FUNCTION resolve_qualification_release_authorization_v1(
  p_authorization_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release resolution caller is unauthorized'
      USING ERRCODE = '42501';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM qualification_release_v1_authorizations
    WHERE authorization_id=p_authorization_id
  ) THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_authorization_bundle(
    p_authorization_id,false,false
  );
END;
$function$;

CREATE FUNCTION resolve_qualification_release_for_publish_v1(
  p_workflow_run_id uuid,
  p_publish_node_run_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE v_authorization_id uuid;
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release resolution caller is unauthorized'
      USING ERRCODE = '42501';
  END IF;
  SELECT authorization_id INTO v_authorization_id
  FROM qualification_release_v1_authorizations
  WHERE workflow_run_id=p_workflow_run_id
    AND publish_node_run_id=p_publish_node_run_id;
  IF NOT FOUND THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_authorization_bundle(
    v_authorization_id,false,false
  );
END;
$function$;

CREATE FUNCTION qualification_release_v1_controller_binding_is_exact(
  p_authorization_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_binding qualification_release_v1_controller_bindings%ROWTYPE;
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_run release_deployment_runs%ROWTYPE;
  v_operation release_delivery_operations%ROWTYPE;
  v_expected jsonb;
BEGIN
  SELECT * INTO v_binding
  FROM qualification_release_v1_controller_bindings
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_authorization
  FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  SELECT * INTO v_bootstrap
  FROM qualification_release_v1_controller_bootstraps
  WHERE bootstrap_id=v_binding.bootstrap_id;
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE claim_event_id=v_binding.lease_claim_event_id;
  SELECT * INTO v_run FROM release_deployment_runs
  WHERE id=v_binding.production_run_id;
  SELECT * INTO v_operation FROM release_delivery_operations
  WHERE id=v_binding.controller_operation_id;
  IF v_authorization.authorization_id IS NULL OR v_claim.claim_event_id IS NULL
     OR v_bootstrap.bootstrap_id IS NULL
     OR v_run.id IS NULL OR v_operation.id IS NULL THEN RETURN false; END IF;
  v_expected := pg_catalog.jsonb_build_object(
    'schemaVersion',
      'worksflow-qualification-release-controller-binding/v1',
    'authorizationId', p_authorization_id::text,
    'workflowLeaseClaim',pg_catalog.jsonb_build_object(
      'attempt',v_claim.lease_attempt,
      'hash',v_claim.claim_hash,
      'id',v_claim.claim_event_id::text,
      'owner',v_claim.lease_owner
    ),
    'productionRun', pg_catalog.jsonb_build_object(
      'id',v_run.id::text,'projectId',v_run.project_id::text,
      'environment','production','operation','promote','stateAtBind','queued'
    ),
    'controllerOperation', pg_catalog.jsonb_build_object(
      'id',v_operation.id::text,'requestHash',v_operation.request_hash,
      'controller',pg_catalog.jsonb_build_object(
        'schemaVersion',v_bootstrap.controller_schema_version,
        'id',v_bootstrap.controller_id,
        'version',v_bootstrap.controller_version,
        'protocol',v_bootstrap.controller_protocol,
        'trustKeyDigest',v_bootstrap.controller_trust_key_digest
      )
    ),
    'release',pg_catalog.jsonb_build_object(
      'releaseBundle',pg_catalog.jsonb_build_object(
        'id',v_authorization.release_bundle_id::text,
        'hash',v_authorization.release_bundle_hash
      ),
      'previewReceipt',pg_catalog.jsonb_build_object(
        'id',v_authorization.preview_receipt_id::text,
        'hash',v_authorization.preview_receipt_hash
      ),
      'promotionApproval',pg_catalog.jsonb_build_object(
        'id',v_authorization.promotion_approval_id::text,
        'hash',v_authorization.promotion_approval_hash
      )
    ),
    'boundAt',qualification_release_v1_timestamp(v_binding.bound_at)
  );
  RETURN qualification_release_v1_authorization_is_exact(
           p_authorization_id
         ) IS TRUE
    AND qualification_release_v1_controller_bootstrap_is_exact(
          v_bootstrap.bootstrap_id
        ) IS TRUE
    AND qualification_release_v1_lease_claim_is_exact(
          v_claim.claim_event_id
        ) IS TRUE
    AND v_claim.authorization_id=v_authorization.authorization_id
    AND v_binding.lease_claim_hash=v_claim.claim_hash
    AND v_binding.lease_attempt=v_claim.lease_attempt
    AND v_binding.lease_owner=v_claim.lease_owner
    AND v_binding.bootstrap_hash=v_bootstrap.bootstrap_hash
    AND v_binding.project_id=v_authorization.project_id
    AND v_binding.production_run_id=v_authorization.expected_production_run_id
    AND v_binding.controller_operation_id=v_run.id
    AND v_operation.deployment_run_id=v_run.id
    AND v_operation.kind='production'
    AND v_operation.project_id=v_authorization.project_id
    AND v_operation.request_hash=v_binding.controller_request_hash
    AND v_operation.controller_schema_version=v_bootstrap.controller_schema_version
    AND v_operation.controller_id=v_bootstrap.controller_id
    AND v_operation.controller_version=v_bootstrap.controller_version
    AND v_operation.controller_protocol=v_bootstrap.controller_protocol
    AND v_operation.controller_trust_key_digest
      =v_bootstrap.controller_trust_key_digest
    AND v_run.project_id=v_authorization.project_id
    AND v_run.schema_version='release-deployment-run/v2'
    AND v_run.environment='production' AND v_run.operation='promote'
    AND v_run.release_bundle_id=v_authorization.release_bundle_id
    AND v_run.release_bundle_hash=v_authorization.release_bundle_hash
    AND v_run.preview_receipt_id=v_authorization.preview_receipt_id
    AND v_run.preview_receipt_hash=v_authorization.preview_receipt_hash
    AND v_run.promotion_approval_id=v_authorization.promotion_approval_id
    AND v_run.promotion_approval_hash=v_authorization.promotion_approval_hash
    AND v_run.source_revision_id IS NULL AND v_run.source_revision_hash IS NULL
    AND v_run.request_key=v_authorization.release_request_key
    AND v_run.request_hash=v_authorization.authorization_hash
    AND v_run.reason='Qualified workflow-engine/v3 ActionPublish'
    AND v_run.created_by=v_authorization.actor_id
    AND v_binding.controller_schema_version=v_bootstrap.controller_schema_version
    AND v_binding.controller_id=v_bootstrap.controller_id
    AND v_binding.controller_version=v_bootstrap.controller_version
    AND v_binding.controller_protocol=v_bootstrap.controller_protocol
    AND v_binding.controller_trust_key_digest
      =v_bootstrap.controller_trust_key_digest
    AND v_binding.binding_document=v_expected
    AND v_binding.binding_bytes=workflow_input_canonical_jsonb_bytes(v_expected)
    AND v_binding.binding_hash=qualification_release_v1_hash(
      'worksflow.qualification-release.controller-binding/v1',
      v_binding.binding_bytes
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_controller_bundle(
  p_authorization_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_binding qualification_release_v1_controller_bindings%ROWTYPE;
  v_bundle jsonb;
BEGIN
  IF qualification_release_v1_controller_binding_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release controller binding is corrupt'
      USING ERRCODE='WQR02';
  END IF;
  SELECT * INTO STRICT v_binding
  FROM qualification_release_v1_controller_bindings
  WHERE authorization_id=p_authorization_id;
  v_bundle := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-controller-bundle/v1',
    'authorizationId',p_authorization_id::text,
    'binding',pg_catalog.jsonb_build_object(
      'hash',v_binding.binding_hash,
      'bytesHex',pg_catalog.encode(v_binding.binding_bytes,'hex'),
      'document',v_binding.binding_document
    )
  );
  IF p_include_idempotent THEN
    v_bundle := v_bundle || pg_catalog.jsonb_build_object(
      'idempotent',COALESCE(p_idempotent,false)
    );
  END IF;
  RETURN v_bundle;
END;
$function$;

CREATE FUNCTION start_qualification_release_controller_v1(
  p_authorization_id uuid,
  p_claim_event_id uuid,
  p_lease_owner text,
  p_lease_attempt integer
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_existing qualification_release_v1_controller_bindings%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_workflow_run workflow_runs%ROWTYPE;
  v_publish_node workflow_node_runs%ROWTYPE;
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_bundle release_bundles%ROWTYPE;
  v_receipt canonical_verification_receipts%ROWTYPE;
  v_preview release_preview_receipts%ROWTYPE;
  v_approval release_promotion_approvals%ROWTYPE;
  v_head release_production_heads%ROWTYPE;
  v_now timestamptz;
  v_bundle_document jsonb;
  v_preview_document jsonb;
  v_approval_document jsonb;
  v_payload jsonb;
  v_request_document jsonb;
  v_request_text text;
  v_request_hash text;
  v_binding_document jsonb;
  v_binding_bytes bytea;
  v_binding_hash text;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release controller caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Release controller start requires SERIALIZABLE isolation'
      USING ERRCODE='WQR03';
  END IF;
  IF p_authorization_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_authorization_id) IS NOT TRUE
     OR p_claim_event_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_claim_event_id) IS NOT TRUE
     OR p_lease_owner IS NULL OR p_lease_owner<>pg_catalog.btrim(p_lease_owner)
     OR pg_catalog.octet_length(p_lease_owner) NOT BETWEEN 1 AND 200
     OR p_lease_attempt IS NULL OR p_lease_attempt NOT BETWEEN 1 AND 2147483647 THEN
    RAISE EXCEPTION 'Qualification Release controller start request is invalid'
      USING ERRCODE='WQR01';
  END IF;
  SELECT * INTO v_authorization
  FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN; END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-v1:'
      ||v_authorization.workflow_run_id::text||':'
      ||v_authorization.publish_node_run_id::text,0
    )
  );
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-controller-v1:'
      ||p_authorization_id::text,0
    )
  );
  PERFORM 1 FROM projects
  WHERE id=v_authorization.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_workflow_run FROM workflow_runs
  WHERE id=v_authorization.workflow_run_id FOR UPDATE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id=v_authorization.workflow_run_id ORDER BY id FOR UPDATE;
  SELECT * INTO v_publish_node FROM workflow_node_runs
  WHERE id=v_authorization.publish_node_run_id;
  SELECT * INTO v_bootstrap
  FROM qualification_release_v1_controller_bootstraps FOR SHARE;
  IF NOT FOUND
     OR qualification_release_v1_controller_bootstrap_is_exact(
          v_bootstrap.bootstrap_id
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release Controller bootstrap is not ready'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_authorization
  FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE claim_event_id=p_claim_event_id FOR SHARE;
  IF qualification_release_v1_authorization_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release authorization is corrupt'
      USING ERRCODE='WQR02';
  END IF;
  IF v_claim.claim_event_id IS NULL
     OR v_claim.authorization_id IS DISTINCT FROM p_authorization_id
     OR v_claim.lease_owner IS DISTINCT FROM p_lease_owner
     OR v_claim.lease_attempt IS DISTINCT FROM p_lease_attempt
     OR qualification_release_v1_lease_claim_is_exact(
          v_claim.claim_event_id
        ) IS NOT TRUE
     OR qualification_release_v1_lease_claim_chain_is_exact(
          p_authorization_id,v_publish_node.attempt
        ) IS NOT TRUE
     OR v_workflow_run.status IS DISTINCT FROM 'running'
     OR v_workflow_run.completed_at IS NOT NULL
     OR v_workflow_run.cancelled_at IS NOT NULL
     OR v_workflow_run.failure IS NOT NULL
     OR v_workflow_run.event_cursor IS DISTINCT FROM v_claim.event_sequence
     OR v_publish_node.status IS DISTINCT FROM 'running'
     OR v_publish_node.attempt IS DISTINCT FROM v_claim.lease_attempt
     OR v_publish_node.lease_owner IS DISTINCT FROM v_claim.lease_owner
     OR v_publish_node.lease_expires_at IS NULL
     OR v_publish_node.lease_expires_at<=pg_catalog.clock_timestamp() THEN
    RAISE EXCEPTION 'Qualification Release controller start requires an active exact claim'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_existing
  FROM qualification_release_v1_controller_bindings
  WHERE authorization_id=p_authorization_id;
  IF FOUND THEN
    IF qualification_release_v1_controller_binding_is_exact(
         p_authorization_id
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release controller replay is corrupt'
        USING ERRCODE='WQR02';
    END IF;
    RETURN NEXT qualification_release_v1_controller_bundle(
      p_authorization_id,true,true
    );
    RETURN;
  END IF;
  SELECT * INTO v_bundle FROM release_bundles
  WHERE id=v_authorization.release_bundle_id FOR SHARE;
  SELECT * INTO v_receipt FROM canonical_verification_receipts
  WHERE id=v_authorization.canonical_receipt_id FOR SHARE;
  SELECT * INTO v_preview FROM release_preview_receipts
  WHERE id=v_authorization.preview_receipt_id FOR SHARE;
  SELECT * INTO v_approval FROM release_promotion_approvals
  WHERE id=v_authorization.promotion_approval_id FOR SHARE;
  INSERT INTO release_production_heads(
    project_id,environment,generation,updated_by
  ) VALUES (
    v_authorization.project_id,'production',0,v_authorization.actor_id
  ) ON CONFLICT(project_id,environment) DO NOTHING;
  SELECT * INTO v_head FROM release_production_heads
  WHERE project_id=v_authorization.project_id AND environment='production'
  FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release production head is unavailable'
      USING ERRCODE='WQR03';
  END IF;

  v_bundle_document := pg_catalog.jsonb_build_object(
    'schemaVersion','release-bundle/v1','id',v_bundle.id::text,
    'projectId',v_bundle.project_id::text,
    'workspace',pg_catalog.jsonb_build_object(
      'workspaceArtifactId',v_bundle.workspace_artifact_id::text,
      'workspaceRevisionId',v_bundle.workspace_revision_id::text,
      'workspaceContentHash',v_bundle.workspace_content_hash
    ),
    'canonicalReceipt',pg_catalog.jsonb_build_object(
      'id',v_bundle.canonical_receipt_id::text,
      'contentHash',v_bundle.canonical_receipt_hash
    ),
    'buildManifest',pg_catalog.jsonb_build_object(
      'id',v_receipt.build_manifest_id::text,
      'contentHash',v_receipt.build_manifest_hash
    ),
    'buildContract',pg_catalog.jsonb_build_object(
      'id',v_receipt.build_contract_id::text,
      'contentHash',v_receipt.build_contract_hash
    ),
    'fullStackTemplate',pg_catalog.jsonb_build_object(
      'id',v_receipt.full_stack_template_id::text,
      'contentHash',v_receipt.full_stack_template_hash
    ),
    'verificationProfile',pg_catalog.jsonb_build_object(
      'id',v_receipt.verification_profile_id,
      'version',v_receipt.verification_profile_version,
      'contentHash',v_receipt.verification_profile_hash
    ),
    'releaseArtifacts',v_bundle.release_artifacts,
    'bundleHash',v_bundle.bundle_hash,
    'createdBy',v_bundle.created_by::text,
    'createdAt',release_delivery_rfc3339_microsecond(v_bundle.created_at)
  );
  IF release_delivery_embedded_hash_is_exact(
       v_bundle_document,'bundleHash'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release reconstructed ReleaseBundle hash mismatch: stored %, calculated %',
      v_bundle.bundle_hash,
      'sha256:'||pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(
        release_delivery_canonical_json(
          pg_catalog.jsonb_set(v_bundle_document,ARRAY['bundleHash'],'""'::jsonb,false)
        ),'UTF8'
      )),'hex')
      USING ERRCODE='WQR02';
  END IF;
  v_preview_document := pg_catalog.jsonb_build_object(
    'schemaVersion',v_preview.schema_version,'id',v_preview.id::text,
    'runId',v_preview.run_id::text,'projectId',v_preview.project_id::text,
    'releaseBundle',pg_catalog.jsonb_build_object(
      'id',v_preview.release_bundle_id::text,
      'contentHash',v_preview.release_bundle_hash
    ),
    'canonicalReceipt',pg_catalog.jsonb_build_object(
      'id',v_preview.canonical_receipt_id::text,
      'contentHash',v_preview.canonical_receipt_hash
    ),
    'workspace',pg_catalog.jsonb_build_object(
      'workspaceArtifactId',v_preview.workspace_artifact_id::text,
      'workspaceRevisionId',v_preview.workspace_revision_id::text,
      'workspaceContentHash',v_preview.workspace_content_hash
    ),
    'releaseArtifacts',v_preview.release_artifacts,
    'namespace',v_preview.namespace,'provider',v_preview.provider,
    'providerRef',v_preview.provider_ref,'checks',v_preview.checks,
    'decision',v_preview.decision,
    'controllerOperation',pg_catalog.jsonb_build_object(
      'operationId',v_preview.controller_operation_id::text,
      'resultHash',v_preview.controller_result_hash
    ),
    'payloadHash',v_preview.payload_hash,'createdBy',v_preview.created_by::text,
    'createdAt',release_delivery_rfc3339_microsecond(v_preview.created_at)
  );
  v_approval_document := pg_catalog.jsonb_build_object(
    'schemaVersion',v_approval.schema_version,'id',v_approval.id::text,
    'projectId',v_approval.project_id::text,
    'releaseBundle',pg_catalog.jsonb_build_object(
      'id',v_approval.release_bundle_id::text,
      'contentHash',v_approval.release_bundle_hash
    ),
    'previewReceipt',pg_catalog.jsonb_build_object(
      'id',v_approval.preview_receipt_id::text,
      'contentHash',v_approval.preview_receipt_hash
    ),
    'reason',v_approval.reason,'payloadHash',v_approval.payload_hash,
    'createdBy',v_approval.created_by::text,
    'createdAt',release_delivery_rfc3339_microsecond(v_approval.created_at)
  );
  v_payload := pg_catalog.jsonb_build_object(
    'schemaVersion','release-production-operation-payload/v1',
    'operationId',v_authorization.expected_production_run_id::text,
    'runId',v_authorization.expected_production_run_id::text,
    'projectId',v_authorization.project_id::text,
    'reason','Qualified workflow-engine/v3 ActionPublish',
    'environment','production','operation','promote',
    'releaseBundle',v_bundle_document,'previewReceipt',v_preview_document,
    'promotionApproval',v_approval_document,'sourceRevision','null'::jsonb,
    'expectedHead',pg_catalog.jsonb_build_object(
      'revision',CASE WHEN v_head.deployment_revision_id IS NULL
        THEN 'null'::jsonb ELSE pg_catalog.jsonb_build_object(
          'id',v_head.deployment_revision_id::text,
          'contentHash',v_head.deployment_revision_hash
        ) END,
      'productionReceipt',CASE WHEN v_head.production_receipt_id IS NULL
        THEN 'null'::jsonb ELSE pg_catalog.jsonb_build_object(
          'id',v_head.production_receipt_id::text,
          'contentHash',v_head.production_receipt_hash
        ) END
    )
  );
  v_request_document := pg_catalog.jsonb_build_object(
    'schemaVersion','release-delivery-operation-document/v3',
    'operationId',v_authorization.expected_production_run_id::text,
    'kind','production','projectId',v_authorization.project_id::text,
    'payload',v_payload
  );
  v_request_text := release_delivery_canonical_json(v_request_document);
  v_request_hash := 'sha256:'||pg_catalog.encode(
    pg_catalog.sha256(pg_catalog.convert_to(v_request_text,'UTF8')),'hex'
  );
  v_now := pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  INSERT INTO release_deployment_runs(
    id,schema_version,project_id,environment,operation,
    release_bundle_id,release_bundle_hash,preview_receipt_id,
    preview_receipt_hash,promotion_approval_id,promotion_approval_hash,
    source_revision_id,source_revision_hash,expected_revision_id,
    expected_revision_hash,expected_production_receipt_id,
    expected_production_receipt_hash,request_key,request_hash,reason,
    state,version,created_by,updated_by
  ) VALUES (
    v_authorization.expected_production_run_id,'release-deployment-run/v2',
    v_authorization.project_id,'production','promote',v_bundle.id,
    v_bundle.bundle_hash,v_preview.id,v_preview.payload_hash,v_approval.id,
    v_approval.payload_hash,NULL,NULL,v_head.deployment_revision_id,
    v_head.deployment_revision_hash,v_head.production_receipt_id,
    v_head.production_receipt_hash,v_authorization.release_request_key,
    v_authorization.authorization_hash,
    'Qualified workflow-engine/v3 ActionPublish','queued',1,
    v_authorization.actor_id,v_authorization.actor_id
  );
  INSERT INTO release_delivery_operations(
    id,schema_version,project_id,kind,preview_run_id,deployment_run_id,
    request_schema_version,request_document,request_hash,
    controller_schema_version,controller_id,controller_version,
    controller_protocol,controller_trust_key_digest,remote_state,created_by
  ) VALUES (
    v_authorization.expected_production_run_id,
    'release-delivery-operation/v1',v_authorization.project_id,'production',
    NULL,v_authorization.expected_production_run_id,
    'release-delivery-operation-request/v3',v_request_text,v_request_hash,
    v_bootstrap.controller_schema_version,v_bootstrap.controller_id,
    v_bootstrap.controller_version,v_bootstrap.controller_protocol,
    v_bootstrap.controller_trust_key_digest,'prepared',v_authorization.actor_id
  );
  v_binding_document := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-controller-binding/v1',
    'authorizationId',p_authorization_id::text,
    'workflowLeaseClaim',pg_catalog.jsonb_build_object(
      'attempt',v_claim.lease_attempt,
      'hash',v_claim.claim_hash,
      'id',v_claim.claim_event_id::text,
      'owner',v_claim.lease_owner
    ),
    'productionRun',pg_catalog.jsonb_build_object(
      'id',v_authorization.expected_production_run_id::text,
      'projectId',v_authorization.project_id::text,
      'environment','production','operation','promote','stateAtBind','queued'
    ),
    'controllerOperation',pg_catalog.jsonb_build_object(
      'id',v_authorization.expected_production_run_id::text,
      'requestHash',v_request_hash,
      'controller',pg_catalog.jsonb_build_object(
        'schemaVersion',v_bootstrap.controller_schema_version,
        'id',v_bootstrap.controller_id,'version',v_bootstrap.controller_version,
        'protocol',v_bootstrap.controller_protocol,
        'trustKeyDigest',v_bootstrap.controller_trust_key_digest
      )
    ),
    'release',pg_catalog.jsonb_build_object(
      'releaseBundle',pg_catalog.jsonb_build_object(
        'id',v_authorization.release_bundle_id::text,
        'hash',v_authorization.release_bundle_hash
      ),
      'previewReceipt',pg_catalog.jsonb_build_object(
        'id',v_authorization.preview_receipt_id::text,
        'hash',v_authorization.preview_receipt_hash
      ),
      'promotionApproval',pg_catalog.jsonb_build_object(
        'id',v_authorization.promotion_approval_id::text,
        'hash',v_authorization.promotion_approval_hash
      )
    ),
    'boundAt',qualification_release_v1_timestamp(v_now)
  );
  v_binding_bytes := workflow_input_canonical_jsonb_bytes(v_binding_document);
  v_binding_hash := qualification_release_v1_hash(
    'worksflow.qualification-release.controller-binding/v1',v_binding_bytes
  );
  INSERT INTO qualification_release_v1_controller_bindings(
    authorization_id,lease_claim_event_id,lease_claim_hash,lease_attempt,
    lease_owner,bootstrap_id,bootstrap_hash,project_id,production_run_id,
    controller_operation_id,controller_request_hash,controller_schema_version,
    controller_id,controller_version,controller_protocol,
    controller_trust_key_digest,binding_bytes,binding_document,binding_hash,
    bound_at,creation_transaction_id
  ) VALUES (
    p_authorization_id,v_claim.claim_event_id,v_claim.claim_hash,
    v_claim.lease_attempt,v_claim.lease_owner,
    v_bootstrap.bootstrap_id,v_bootstrap.bootstrap_hash,
    v_authorization.project_id,v_authorization.expected_production_run_id,
    v_authorization.expected_production_run_id,v_request_hash,
    v_bootstrap.controller_schema_version,v_bootstrap.controller_id,
    v_bootstrap.controller_version,v_bootstrap.controller_protocol,
    v_bootstrap.controller_trust_key_digest,v_binding_bytes,
    v_binding_document,v_binding_hash,v_now,
    pg_catalog.pg_current_xact_id()::text
  );
  IF qualification_release_v1_controller_binding_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release controller closure is incomplete'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_controller_bundle(
    p_authorization_id,true,false
  );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Release controller identity or closure conflict'
      USING ERRCODE='WQR02';
END;
$function$;

CREATE FUNCTION inspect_qualification_release_controller_v1(
  p_authorization_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release controller inspection caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM qualification_release_v1_controller_bindings
    WHERE authorization_id=p_authorization_id
  ) THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_controller_bundle(
    p_authorization_id,false,false
  );
END;
$function$;

CREATE FUNCTION qualification_release_v1_result_is_exact(
  p_authorization_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_result qualification_release_v1_results%ROWTYPE;
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_publish workflow_node_runs%ROWTYPE;
  v_binding qualification_release_v1_controller_bindings%ROWTYPE;
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_run release_deployment_runs%ROWTYPE;
  v_operation release_delivery_operations%ROWTYPE;
  v_controller_result release_delivery_operation_results%ROWTYPE;
  v_receipt release_production_receipts%ROWTYPE;
  v_revision release_deployment_revisions%ROWTYPE;
  v_expected jsonb;
BEGIN
  SELECT * INTO v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  SELECT * INTO v_binding FROM qualification_release_v1_controller_bindings
  WHERE authorization_id=p_authorization_id;
  SELECT * INTO v_publish FROM workflow_node_runs
  WHERE id=v_authorization.publish_node_run_id;
  SELECT * INTO v_bootstrap FROM qualification_release_v1_controller_bootstraps
  WHERE bootstrap_id=v_binding.bootstrap_id;
  SELECT * INTO v_run FROM release_deployment_runs
  WHERE id=v_result.production_run_id;
  SELECT * INTO v_operation FROM release_delivery_operations
  WHERE id=v_result.controller_operation_id;
  SELECT * INTO v_controller_result FROM release_delivery_operation_results
  WHERE operation_id=v_result.controller_operation_id
    AND result_hash=v_result.controller_result_hash;
  SELECT * INTO v_receipt FROM release_production_receipts
  WHERE id=v_result.production_receipt_id;
  SELECT * INTO v_revision FROM release_deployment_revisions
  WHERE id=v_result.deployment_revision_id;
  IF v_authorization.authorization_id IS NULL OR v_publish.id IS NULL
     OR v_binding.authorization_id IS NULL
     OR v_bootstrap.bootstrap_id IS NULL OR v_run.id IS NULL
     OR v_operation.id IS NULL OR v_controller_result.operation_id IS NULL
     OR v_receipt.id IS NULL OR v_revision.id IS NULL THEN RETURN false; END IF;
  v_expected := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-result/v1',
    'authorizationId',p_authorization_id::text,
    'productionRun',pg_catalog.jsonb_build_object(
      'id',v_run.id::text,'state','healthy','version',v_result.production_run_version
    ),
    'controllerOperation',pg_catalog.jsonb_build_object(
      'id',v_operation.id::text,'requestHash',v_operation.request_hash,
      'resultHash',v_controller_result.result_hash,
      'controller',pg_catalog.jsonb_build_object(
        'schemaVersion',v_bootstrap.controller_schema_version,
        'id',v_bootstrap.controller_id,'version',v_bootstrap.controller_version,
        'protocol',v_bootstrap.controller_protocol,
        'trustKeyDigest',v_bootstrap.controller_trust_key_digest
      )
    ),
    'productionReceipt',pg_catalog.jsonb_build_object(
      'id',v_receipt.id::text,'hash',v_receipt.payload_hash
    ),
    'deploymentRevision',pg_catalog.jsonb_build_object(
      'id',v_revision.id::text,'hash',v_revision.payload_hash
    ),
    'publishResult',pg_catalog.jsonb_build_object(
      'url',v_result.public_url,'deploymentId',v_revision.id::text
    ),
    'completedAt',qualification_release_v1_timestamp(v_result.completed_at)
  );
  RETURN qualification_release_v1_controller_binding_is_exact(
           p_authorization_id
         ) IS TRUE
    AND v_publish.run_id=v_authorization.workflow_run_id
    AND v_publish.node_key=v_authorization.publish_node_key
    AND v_publish.node_type='publish'
    AND v_publish.status IN ('running','completed')
    AND v_publish.input_manifest_id IS NULL
    AND v_publish.output_proposal_id IS NULL
    AND v_publish.output_revision_id IS NULL
    AND v_result.outcome='healthy'
    AND v_result.failure_document IS NULL
    AND v_result.project_id=v_authorization.project_id
    AND v_result.production_run_id=v_binding.production_run_id
    AND v_result.production_run_version=v_run.version
    AND v_run.state='healthy' AND v_run.finished_at IS NOT NULL
    AND v_run.release_bundle_id=v_authorization.release_bundle_id
    AND v_run.release_bundle_hash=v_authorization.release_bundle_hash
    AND v_run.preview_receipt_id=v_authorization.preview_receipt_id
    AND v_run.preview_receipt_hash=v_authorization.preview_receipt_hash
    AND v_run.promotion_approval_id=v_authorization.promotion_approval_id
    AND v_run.promotion_approval_hash=v_authorization.promotion_approval_hash
    AND v_result.controller_operation_id=v_binding.controller_operation_id
    AND v_result.controller_request_hash=v_binding.controller_request_hash
    AND v_operation.remote_state='completed'
    AND v_operation.terminal_result_hash=v_controller_result.result_hash
    AND v_controller_result.status='completed'
    AND v_controller_result.project_id=v_authorization.project_id
    AND v_controller_result.kind='production'
    AND v_controller_result.request_hash=v_operation.request_hash
    AND v_controller_result.controller_schema_version
      =v_bootstrap.controller_schema_version
    AND v_controller_result.controller_id=v_bootstrap.controller_id
    AND v_controller_result.controller_version=v_bootstrap.controller_version
    AND v_controller_result.controller_protocol=v_bootstrap.controller_protocol
    AND v_controller_result.controller_trust_key_digest
      =v_bootstrap.controller_trust_key_digest
    AND v_controller_result.public_url=v_result.public_url
    AND v_receipt.run_id=v_run.id AND v_receipt.project_id=v_run.project_id
    AND v_receipt.schema_version='release-production-receipt/v2'
    AND v_receipt.decision='passed'
    AND v_receipt.controller_operation_id=v_operation.id
    AND v_receipt.controller_result_hash=v_controller_result.result_hash
    AND v_receipt.public_url=v_result.public_url
    AND v_receipt.payload_hash=v_result.production_receipt_hash
    AND v_revision.run_id=v_run.id AND v_revision.project_id=v_run.project_id
    AND v_revision.schema_version='release-deployment-revision/v2'
    AND v_revision.production_receipt_id=v_receipt.id
    AND v_revision.production_receipt_hash=v_receipt.payload_hash
    AND v_revision.controller_operation_id=v_operation.id
    AND v_revision.controller_result_hash=v_controller_result.result_hash
    AND v_revision.public_url=v_result.public_url
    AND v_revision.payload_hash=v_result.deployment_revision_hash
    AND v_result.publish_result_document=pg_catalog.jsonb_build_object(
      'url',v_result.public_url,'deploymentId',v_revision.id::text
    )
    AND v_result.result_document=v_expected
    AND v_result.result_bytes=workflow_input_canonical_jsonb_bytes(v_expected)
    AND v_result.result_hash=qualification_release_v1_hash(
      'worksflow.qualification-release.result/v1',v_result.result_bytes
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_result_bundle(
  p_authorization_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_result qualification_release_v1_results%ROWTYPE;
  v_bundle jsonb;
BEGIN
  IF qualification_release_v1_result_is_exact(p_authorization_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release result is corrupt'
      USING ERRCODE='WQR02';
  END IF;
  SELECT * INTO STRICT v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  v_bundle := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-result-bundle/v1',
    'authorizationId',p_authorization_id::text,
    'result',pg_catalog.jsonb_build_object(
      'hash',v_result.result_hash,
      'bytesHex',pg_catalog.encode(v_result.result_bytes,'hex'),
      'document',v_result.result_document
    ),
    'publishResult',v_result.publish_result_document
  );
  IF p_include_idempotent THEN
    v_bundle := v_bundle||pg_catalog.jsonb_build_object(
      'idempotent',COALESCE(p_idempotent,false)
    );
  END IF;
  RETURN v_bundle;
END;
$function$;

CREATE FUNCTION record_qualification_release_result_v1(
  p_authorization_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_binding qualification_release_v1_controller_bindings%ROWTYPE;
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_existing qualification_release_v1_results%ROWTYPE;
  v_run release_deployment_runs%ROWTYPE;
  v_operation release_delivery_operations%ROWTYPE;
  v_controller_result release_delivery_operation_results%ROWTYPE;
  v_receipt release_production_receipts%ROWTYPE;
  v_revision release_deployment_revisions%ROWTYPE;
  v_now timestamptz;
  v_publish_result jsonb;
  v_document jsonb;
  v_bytes bytea;
  v_hash text;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release result caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Release result requires SERIALIZABLE isolation'
      USING ERRCODE='WQR03';
  END IF;
  IF p_authorization_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_authorization_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release result identity is invalid'
      USING ERRCODE='WQR01';
  END IF;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN; END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-result-v1:'||p_authorization_id::text,0
    )
  );
  SELECT * INTO v_existing FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  IF FOUND THEN
    IF qualification_release_v1_result_is_exact(p_authorization_id) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release result replay is corrupt'
        USING ERRCODE='WQR02';
    END IF;
    RETURN NEXT qualification_release_v1_result_bundle(
      p_authorization_id,true,true
    );
    RETURN;
  END IF;
  PERFORM 1 FROM projects WHERE id=v_authorization.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_binding FROM qualification_release_v1_controller_bindings
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_bootstrap FROM qualification_release_v1_controller_bootstraps
  WHERE bootstrap_id=v_binding.bootstrap_id FOR SHARE;
  IF qualification_release_v1_controller_binding_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release controller binding is corrupt'
      USING ERRCODE='WQR02';
  END IF;
  SELECT * INTO v_run FROM release_deployment_runs
  WHERE id=v_binding.production_run_id FOR SHARE;
  SELECT * INTO v_operation FROM release_delivery_operations
  WHERE id=v_binding.controller_operation_id FOR SHARE;
  SELECT * INTO v_controller_result FROM release_delivery_operation_results
  WHERE operation_id=v_binding.controller_operation_id
    AND result_hash=v_operation.terminal_result_hash FOR SHARE;
  SELECT * INTO v_receipt FROM release_production_receipts
  WHERE run_id=v_run.id AND controller_operation_id=v_operation.id
    AND controller_result_hash=v_controller_result.result_hash FOR SHARE;
  SELECT * INTO v_revision FROM release_deployment_revisions
  WHERE run_id=v_run.id AND production_receipt_id=v_receipt.id
    AND production_receipt_hash=v_receipt.payload_hash
    AND controller_operation_id=v_operation.id
    AND controller_result_hash=v_controller_result.result_hash FOR SHARE;
  IF v_run.state IS DISTINCT FROM 'healthy'
     OR v_operation.remote_state IS DISTINCT FROM 'completed'
     OR v_controller_result.status IS DISTINCT FROM 'completed'
     OR v_receipt.decision IS DISTINCT FROM 'passed'
     OR v_receipt.schema_version IS DISTINCT FROM 'release-production-receipt/v2'
     OR v_revision.schema_version IS DISTINCT FROM 'release-deployment-revision/v2'
     OR v_controller_result.public_url IS NULL
     OR v_receipt.public_url IS DISTINCT FROM v_controller_result.public_url
     OR v_revision.public_url IS DISTINCT FROM v_controller_result.public_url THEN
    RAISE EXCEPTION 'Qualification Release healthy Controller result is not ready'
      USING ERRCODE='WQR03';
  END IF;
  v_now := pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  v_publish_result := pg_catalog.jsonb_build_object(
    'url',v_controller_result.public_url,'deploymentId',v_revision.id::text
  );
  v_document := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-result/v1',
    'authorizationId',p_authorization_id::text,
    'productionRun',pg_catalog.jsonb_build_object(
      'id',v_run.id::text,'state','healthy','version',v_run.version
    ),
    'controllerOperation',pg_catalog.jsonb_build_object(
      'id',v_operation.id::text,'requestHash',v_operation.request_hash,
      'resultHash',v_controller_result.result_hash,
      'controller',pg_catalog.jsonb_build_object(
        'schemaVersion',v_bootstrap.controller_schema_version,
        'id',v_bootstrap.controller_id,'version',v_bootstrap.controller_version,
        'protocol',v_bootstrap.controller_protocol,
        'trustKeyDigest',v_bootstrap.controller_trust_key_digest
      )
    ),
    'productionReceipt',pg_catalog.jsonb_build_object(
      'id',v_receipt.id::text,'hash',v_receipt.payload_hash
    ),
    'deploymentRevision',pg_catalog.jsonb_build_object(
      'id',v_revision.id::text,'hash',v_revision.payload_hash
    ),
    'publishResult',v_publish_result,
    'completedAt',qualification_release_v1_timestamp(v_now)
  );
  v_bytes := workflow_input_canonical_jsonb_bytes(v_document);
  v_hash := qualification_release_v1_hash(
    'worksflow.qualification-release.result/v1',v_bytes
  );
  INSERT INTO qualification_release_v1_results(
    authorization_id,outcome,project_id,production_run_id,production_run_version,
    controller_operation_id,controller_request_hash,controller_result_hash,
    production_receipt_id,production_receipt_hash,deployment_revision_id,
    deployment_revision_hash,public_url,publish_result_document,failure_document,
    result_bytes,result_document,result_hash,completed_at,creation_transaction_id
  ) VALUES (
    p_authorization_id,'healthy',v_authorization.project_id,v_run.id,v_run.version,
    v_operation.id,v_operation.request_hash,v_controller_result.result_hash,
    v_receipt.id,v_receipt.payload_hash,v_revision.id,v_revision.payload_hash,
    v_controller_result.public_url,v_publish_result,NULL,v_bytes,v_document,
    v_hash,v_now,pg_catalog.pg_current_xact_id()::text
  );
  IF qualification_release_v1_result_is_exact(p_authorization_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release result closure is incomplete'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_result_bundle(
    p_authorization_id,true,false
  );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Release result identity or closure conflict'
      USING ERRCODE='WQR02';
END;
$function$;

CREATE FUNCTION inspect_qualification_release_result_v1(
  p_authorization_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release result inspection caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM qualification_release_v1_results
    WHERE authorization_id=p_authorization_id
  ) THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_result_bundle(
    p_authorization_id,false,false
  );
END;
$function$;

CREATE FUNCTION qualification_release_v1_failure_is_exact(
  p_authorization_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_result qualification_release_v1_results%ROWTYPE;
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_publish workflow_node_runs%ROWTYPE;
  v_binding qualification_release_v1_controller_bindings%ROWTYPE;
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_run release_deployment_runs%ROWTYPE;
  v_operation release_delivery_operations%ROWTYPE;
  v_controller_result release_delivery_operation_results%ROWTYPE;
  v_receipt release_production_receipts%ROWTYPE;
  v_failure jsonb;
  v_expected jsonb;
BEGIN
  SELECT * INTO v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND OR v_result.outcome NOT IN (
    'production_failed','controller_rejected','pre_submit_cancelled'
  ) THEN RETURN false; END IF;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  SELECT * INTO v_binding FROM qualification_release_v1_controller_bindings
  WHERE authorization_id=p_authorization_id;
  SELECT * INTO v_publish FROM workflow_node_runs
  WHERE id=v_authorization.publish_node_run_id;
  SELECT * INTO v_bootstrap FROM qualification_release_v1_controller_bootstraps
  WHERE bootstrap_id=v_binding.bootstrap_id;
  SELECT * INTO v_run FROM release_deployment_runs
  WHERE id=v_result.production_run_id;
  SELECT * INTO v_operation FROM release_delivery_operations
  WHERE id=v_result.controller_operation_id;
  IF v_result.controller_result_hash IS NOT NULL THEN
    SELECT * INTO v_controller_result FROM release_delivery_operation_results
    WHERE operation_id=v_result.controller_operation_id
      AND result_hash=v_result.controller_result_hash;
  END IF;
  IF v_result.production_receipt_id IS NOT NULL THEN
    SELECT * INTO v_receipt FROM release_production_receipts
    WHERE id=v_result.production_receipt_id;
  END IF;
  IF v_authorization.authorization_id IS NULL OR v_publish.id IS NULL
     OR v_binding.authorization_id IS NULL
     OR v_bootstrap.bootstrap_id IS NULL OR v_run.id IS NULL
     OR v_operation.id IS NULL
     OR (
       v_result.outcome<>'pre_submit_cancelled'
       AND v_controller_result.operation_id IS NULL
     ) THEN RETURN false; END IF;
  v_failure := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-workflow-failure/v1',
    'code',CASE v_result.outcome
      WHEN 'production_failed' THEN 'release_production_checks_failed'
      WHEN 'controller_rejected' THEN 'release_controller_rejected'
      ELSE 'release_cancelled_before_submission'
    END,
    'outcome',v_result.outcome
  );
  v_expected := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-failure/v1',
    'authorizationId',p_authorization_id::text,
    'outcome',v_result.outcome,
    'productionRun',pg_catalog.jsonb_build_object(
      'id',v_run.id::text,'state',v_run.state,
      'version',v_result.production_run_version
    ),
    'controllerOperation',pg_catalog.jsonb_build_object(
      'id',v_operation.id::text,'requestHash',v_operation.request_hash,
      'resultHash',v_result.controller_result_hash,
      'controller',pg_catalog.jsonb_build_object(
        'schemaVersion',v_bootstrap.controller_schema_version,
        'id',v_bootstrap.controller_id,'version',v_bootstrap.controller_version,
        'protocol',v_bootstrap.controller_protocol,
        'trustKeyDigest',v_bootstrap.controller_trust_key_digest
      )
    ),
    'failure',v_failure,
    'completedAt',qualification_release_v1_timestamp(v_result.completed_at)
  );
  IF v_result.outcome='production_failed' THEN
    v_expected := v_expected||pg_catalog.jsonb_build_object(
      'productionReceipt',pg_catalog.jsonb_build_object(
        'id',v_receipt.id::text,'hash',v_receipt.payload_hash
      )
    );
  END IF;
  RETURN qualification_release_v1_controller_binding_is_exact(
           p_authorization_id
         ) IS TRUE
    AND v_publish.run_id=v_authorization.workflow_run_id
    AND v_publish.node_key=v_authorization.publish_node_key
    AND v_publish.node_type='publish'
    AND v_publish.status IN ('running','failed')
    AND v_publish.input_manifest_id IS NULL
    AND v_publish.output_proposal_id IS NULL
    AND v_publish.output_revision_id IS NULL
    AND v_result.project_id=v_authorization.project_id
    AND v_result.production_run_id=v_binding.production_run_id
    AND v_result.production_run_version=v_run.version
    AND v_run.finished_at IS NOT NULL
    AND v_run.release_bundle_id=v_authorization.release_bundle_id
    AND v_run.release_bundle_hash=v_authorization.release_bundle_hash
    AND v_run.preview_receipt_id=v_authorization.preview_receipt_id
    AND v_run.preview_receipt_hash=v_authorization.preview_receipt_hash
    AND v_run.promotion_approval_id=v_authorization.promotion_approval_id
    AND v_run.promotion_approval_hash=v_authorization.promotion_approval_hash
    AND v_result.controller_operation_id=v_binding.controller_operation_id
    AND v_result.controller_request_hash=v_binding.controller_request_hash
    AND (
      v_result.outcome='pre_submit_cancelled'
      OR (
        v_operation.terminal_result_hash=v_controller_result.result_hash
        AND v_controller_result.project_id=v_authorization.project_id
        AND v_controller_result.kind='production'
        AND v_controller_result.request_hash=v_operation.request_hash
        AND v_controller_result.controller_schema_version
          =v_bootstrap.controller_schema_version
        AND v_controller_result.controller_id=v_bootstrap.controller_id
        AND v_controller_result.controller_version=v_bootstrap.controller_version
        AND v_controller_result.controller_protocol
          =v_bootstrap.controller_protocol
        AND v_controller_result.controller_trust_key_digest
          =v_bootstrap.controller_trust_key_digest
      )
    )
    AND v_result.deployment_revision_id IS NULL
    AND v_result.deployment_revision_hash IS NULL
    AND v_result.public_url IS NULL
    AND v_result.publish_result_document IS NULL
    AND v_result.failure_document=v_failure
    AND (
      (v_result.outcome='production_failed'
        AND v_run.state='failed'
        AND v_operation.remote_state='completed'
        AND v_controller_result.status='completed'
        AND v_receipt.id IS NOT NULL
        AND v_receipt.run_id=v_run.id
        AND v_receipt.project_id=v_run.project_id
        AND v_receipt.schema_version='release-production-receipt/v2'
        AND v_receipt.decision='failed'
        AND v_receipt.controller_operation_id=v_operation.id
        AND v_receipt.controller_result_hash=v_controller_result.result_hash
        AND v_receipt.provider=v_controller_result.provider
        AND v_receipt.provider_ref=v_controller_result.provider_ref
        AND NULLIF(v_receipt.public_url,'') IS NOT DISTINCT FROM
              v_controller_result.public_url
        AND v_receipt.checks=v_controller_result.checks
        AND v_receipt.payload_hash=v_result.production_receipt_hash
        AND NOT EXISTS (
          SELECT 1 FROM release_deployment_revisions AS revision
          WHERE revision.run_id=v_run.id
             OR revision.controller_operation_id=v_operation.id
        ))
      OR
      (v_result.outcome='controller_rejected'
        AND v_run.state='error'
        AND v_operation.remote_state='rejected'
        AND v_controller_result.status='rejected'
        AND v_result.production_receipt_id IS NULL
        AND v_result.production_receipt_hash IS NULL
        AND NOT EXISTS (
          SELECT 1 FROM release_production_receipts AS receipt
          WHERE receipt.run_id=v_run.id
             OR receipt.controller_operation_id=v_operation.id
        )
        AND NOT EXISTS (
          SELECT 1 FROM release_deployment_revisions AS revision
          WHERE revision.run_id=v_run.id
             OR revision.controller_operation_id=v_operation.id
        ))
      OR
      (v_result.outcome='pre_submit_cancelled'
        AND v_run.state='cancelled'
        AND v_run.lease_worker_id IS NULL
        AND v_run.lease_epoch IS NULL
        AND v_run.lease_expires_at IS NULL
        AND v_operation.remote_state='prepared'
        AND v_operation.submit_attempt_count=0
        AND v_operation.reconcile_attempt_count=0
        AND v_operation.first_submitted_at IS NULL
        AND v_operation.last_attempt_at IS NULL
        AND v_operation.terminal_result_hash IS NULL
        AND v_result.controller_result_hash IS NULL
        AND v_result.production_receipt_id IS NULL
        AND v_result.production_receipt_hash IS NULL
        AND NOT EXISTS (
          SELECT 1 FROM release_delivery_operation_attempts AS attempt
          WHERE attempt.operation_id=v_operation.id
        )
        AND NOT EXISTS (
          SELECT 1 FROM release_delivery_operation_results AS result
          WHERE result.operation_id=v_operation.id
        )
        AND NOT EXISTS (
          SELECT 1 FROM release_production_receipts AS receipt
          WHERE receipt.run_id=v_run.id
             OR receipt.controller_operation_id=v_operation.id
        )
        AND NOT EXISTS (
          SELECT 1 FROM release_deployment_revisions AS revision
          WHERE revision.run_id=v_run.id
             OR revision.controller_operation_id=v_operation.id
        ))
    )
    AND v_result.result_document=v_expected
    AND v_result.result_bytes=workflow_input_canonical_jsonb_bytes(v_expected)
    AND v_result.result_hash=qualification_release_v1_hash(
      'worksflow.qualification-release.failure/v1',v_result.result_bytes
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_failure_bundle(
  p_authorization_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_result qualification_release_v1_results%ROWTYPE;
  v_bundle jsonb;
BEGIN
  IF qualification_release_v1_failure_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release failure is corrupt'
      USING ERRCODE='WQR02';
  END IF;
  SELECT * INTO STRICT v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  v_bundle := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-failure-bundle/v1',
    'authorizationId',p_authorization_id::text,
    'outcome',v_result.outcome,
    'result',pg_catalog.jsonb_build_object(
      'hash',v_result.result_hash,
      'bytesHex',pg_catalog.encode(v_result.result_bytes,'hex'),
      'document',v_result.result_document
    ),
    'failure',v_result.failure_document
  );
  IF p_include_idempotent THEN
    v_bundle := v_bundle||pg_catalog.jsonb_build_object(
      'idempotent',COALESCE(p_idempotent,false)
    );
  END IF;
  RETURN v_bundle;
END;
$function$;

CREATE FUNCTION record_qualification_release_failure_v1(
  p_authorization_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_binding qualification_release_v1_controller_bindings%ROWTYPE;
  v_bootstrap qualification_release_v1_controller_bootstraps%ROWTYPE;
  v_existing qualification_release_v1_results%ROWTYPE;
  v_run release_deployment_runs%ROWTYPE;
  v_operation release_delivery_operations%ROWTYPE;
  v_controller_result release_delivery_operation_results%ROWTYPE;
  v_receipt release_production_receipts%ROWTYPE;
  v_outcome text;
  v_now timestamptz;
  v_failure jsonb;
  v_document jsonb;
  v_bytes bytea;
  v_hash text;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release failure caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Release failure requires SERIALIZABLE isolation'
      USING ERRCODE='WQR03';
  END IF;
  IF p_authorization_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(
          p_authorization_id
        ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release failure identity is invalid'
      USING ERRCODE='WQR01';
  END IF;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN; END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-result-v1:'||p_authorization_id::text,0
    )
  );
  SELECT * INTO v_existing FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  IF FOUND THEN
    IF qualification_release_v1_failure_is_exact(
         p_authorization_id
       ) IS TRUE THEN
      RETURN NEXT qualification_release_v1_failure_bundle(
        p_authorization_id,true,true
      );
      RETURN;
    END IF;
    RAISE EXCEPTION 'Qualification Release terminal outcome conflicts'
      USING ERRCODE='WQR02';
  END IF;
  PERFORM 1 FROM projects WHERE id=v_authorization.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_binding FROM qualification_release_v1_controller_bindings
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_bootstrap FROM qualification_release_v1_controller_bootstraps
  WHERE bootstrap_id=v_binding.bootstrap_id FOR SHARE;
  IF qualification_release_v1_controller_binding_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release controller binding is corrupt'
      USING ERRCODE='WQR02';
  END IF;
  SELECT * INTO v_run FROM release_deployment_runs
  WHERE id=v_binding.production_run_id FOR SHARE;
  SELECT * INTO v_operation FROM release_delivery_operations
  WHERE id=v_binding.controller_operation_id FOR SHARE;
  SELECT * INTO v_controller_result FROM release_delivery_operation_results
  WHERE operation_id=v_binding.controller_operation_id
    AND result_hash=v_operation.terminal_result_hash FOR SHARE;
  IF v_run.state='failed' THEN
    v_outcome := 'production_failed';
    SELECT * INTO v_receipt FROM release_production_receipts
    WHERE run_id=v_run.id
      AND controller_operation_id=v_operation.id
      AND controller_result_hash=v_controller_result.result_hash FOR SHARE;
  ELSIF v_run.state='error' THEN
    v_outcome := 'controller_rejected';
  ELSIF v_run.state='cancelled' THEN
    v_outcome := 'pre_submit_cancelled';
  ELSE
    RAISE EXCEPTION 'Qualification Release terminal failure is not ready'
      USING ERRCODE='WQR03';
  END IF;
  IF v_run.finished_at IS NULL
     OR v_operation.deployment_run_id IS DISTINCT FROM v_run.id
     OR v_operation.project_id IS DISTINCT FROM v_authorization.project_id
     OR v_operation.kind IS DISTINCT FROM 'production'
     OR v_operation.request_hash IS DISTINCT FROM
          v_binding.controller_request_hash
     OR (v_outcome<>'pre_submit_cancelled' AND (
          v_controller_result.operation_id IS NULL
          OR v_controller_result.project_id IS DISTINCT FROM
               v_authorization.project_id
          OR v_controller_result.kind IS DISTINCT FROM 'production'
          OR v_controller_result.request_hash IS DISTINCT FROM
               v_operation.request_hash
          OR v_controller_result.controller_schema_version IS DISTINCT FROM
               v_bootstrap.controller_schema_version
          OR v_controller_result.controller_id IS DISTINCT FROM
               v_bootstrap.controller_id
          OR v_controller_result.controller_version IS DISTINCT FROM
               v_bootstrap.controller_version
          OR v_controller_result.controller_protocol IS DISTINCT FROM
               v_bootstrap.controller_protocol
          OR v_controller_result.controller_trust_key_digest IS DISTINCT FROM
               v_bootstrap.controller_trust_key_digest
        ))
     OR (v_outcome='production_failed' AND (
          v_operation.remote_state IS DISTINCT FROM 'completed'
          OR v_controller_result.status IS DISTINCT FROM 'completed'
          OR v_receipt.id IS NULL
          OR v_receipt.schema_version IS DISTINCT FROM
               'release-production-receipt/v2'
          OR v_receipt.project_id IS DISTINCT FROM v_run.project_id
          OR v_receipt.decision IS DISTINCT FROM 'failed'
          OR v_receipt.provider IS DISTINCT FROM v_controller_result.provider
          OR v_receipt.provider_ref IS DISTINCT FROM
               v_controller_result.provider_ref
          OR NULLIF(v_receipt.public_url,'') IS DISTINCT FROM
               v_controller_result.public_url
          OR v_receipt.checks IS DISTINCT FROM v_controller_result.checks
        ))
     OR (v_outcome='controller_rejected' AND (
          v_operation.remote_state IS DISTINCT FROM 'rejected'
          OR v_controller_result.status IS DISTINCT FROM 'rejected'
          OR EXISTS (
            SELECT 1 FROM release_production_receipts AS receipt
            WHERE receipt.run_id=v_run.id
               OR receipt.controller_operation_id=v_operation.id
          )
        ))
     OR (v_outcome='pre_submit_cancelled' AND (
          v_operation.remote_state IS DISTINCT FROM 'prepared'
          OR v_operation.submit_attempt_count IS DISTINCT FROM 0
          OR v_operation.reconcile_attempt_count IS DISTINCT FROM 0
          OR v_operation.first_submitted_at IS NOT NULL
          OR v_operation.last_attempt_at IS NOT NULL
          OR v_operation.terminal_result_hash IS NOT NULL
          OR v_controller_result.operation_id IS NOT NULL
          OR v_run.lease_worker_id IS NOT NULL
          OR v_run.lease_epoch IS NOT NULL
          OR v_run.lease_expires_at IS NOT NULL
          OR EXISTS (
            SELECT 1 FROM release_delivery_operation_attempts AS attempt
            WHERE attempt.operation_id=v_operation.id
          )
          OR EXISTS (
            SELECT 1 FROM release_delivery_operation_results AS result
            WHERE result.operation_id=v_operation.id
          )
          OR EXISTS (
            SELECT 1 FROM release_production_receipts AS receipt
            WHERE receipt.run_id=v_run.id
               OR receipt.controller_operation_id=v_operation.id
          )
        ))
     OR EXISTS (
          SELECT 1 FROM release_deployment_revisions AS revision
          WHERE revision.run_id=v_run.id
             OR revision.controller_operation_id=v_operation.id
        ) THEN
    RAISE EXCEPTION 'Qualification Release terminal failure evidence is not exact'
      USING ERRCODE='WQR02';
  END IF;
  v_now := pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  v_failure := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-workflow-failure/v1',
    'code',CASE v_outcome
      WHEN 'production_failed' THEN 'release_production_checks_failed'
      WHEN 'controller_rejected' THEN 'release_controller_rejected'
      ELSE 'release_cancelled_before_submission'
    END,
    'outcome',v_outcome
  );
  v_document := pg_catalog.jsonb_build_object(
    'schemaVersion','worksflow-qualification-release-failure/v1',
    'authorizationId',p_authorization_id::text,
    'outcome',v_outcome,
    'productionRun',pg_catalog.jsonb_build_object(
      'id',v_run.id::text,'state',v_run.state,'version',v_run.version
    ),
    'controllerOperation',pg_catalog.jsonb_build_object(
      'id',v_operation.id::text,'requestHash',v_operation.request_hash,
      'resultHash',v_controller_result.result_hash,
      'controller',pg_catalog.jsonb_build_object(
        'schemaVersion',v_bootstrap.controller_schema_version,
        'id',v_bootstrap.controller_id,'version',v_bootstrap.controller_version,
        'protocol',v_bootstrap.controller_protocol,
        'trustKeyDigest',v_bootstrap.controller_trust_key_digest
      )
    ),
    'failure',v_failure,
    'completedAt',qualification_release_v1_timestamp(v_now)
  );
  IF v_outcome='production_failed' THEN
    v_document := v_document||pg_catalog.jsonb_build_object(
      'productionReceipt',pg_catalog.jsonb_build_object(
        'id',v_receipt.id::text,'hash',v_receipt.payload_hash
      )
    );
  END IF;
  v_bytes := workflow_input_canonical_jsonb_bytes(v_document);
  v_hash := qualification_release_v1_hash(
    'worksflow.qualification-release.failure/v1',v_bytes
  );
  INSERT INTO qualification_release_v1_results(
    authorization_id,outcome,project_id,production_run_id,
    production_run_version,controller_operation_id,
    controller_request_hash,controller_result_hash,production_receipt_id,
    production_receipt_hash,deployment_revision_id,
    deployment_revision_hash,public_url,publish_result_document,
    failure_document,result_bytes,result_document,result_hash,completed_at,
    creation_transaction_id
  ) VALUES (
    p_authorization_id,v_outcome,v_authorization.project_id,v_run.id,
    v_run.version,v_operation.id,v_operation.request_hash,
    v_controller_result.result_hash,
    CASE WHEN v_outcome='production_failed' THEN v_receipt.id ELSE NULL END,
    CASE WHEN v_outcome='production_failed' THEN v_receipt.payload_hash ELSE NULL END,
    NULL,NULL,NULL,NULL,v_failure,v_bytes,v_document,v_hash,v_now,
    pg_catalog.pg_current_xact_id()::text
  );
  IF qualification_release_v1_failure_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release failure closure is incomplete'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_failure_bundle(
    p_authorization_id,true,false
  );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Release failure identity or closure conflict'
      USING ERRCODE='WQR02';
END;
$function$;

CREATE FUNCTION inspect_qualification_release_failure_v1(
  p_authorization_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
BEGIN
  PERFORM qualification_release_v1_require_primary();
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release failure inspection caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM qualification_release_v1_results
    WHERE authorization_id=p_authorization_id
      AND outcome IN (
        'production_failed','controller_rejected','pre_submit_cancelled'
      )
  ) THEN RETURN; END IF;
  RETURN NEXT qualification_release_v1_failure_bundle(
    p_authorization_id,false,false
  );
END;
$function$;

CREATE FUNCTION qualification_release_v1_completion_is_exact(
  p_authorization_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_result qualification_release_v1_results%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
  v_actor jsonb;
BEGIN
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=v_authorization.workflow_run_id;
  SELECT * INTO v_node FROM workflow_node_runs
  WHERE id=v_authorization.publish_node_run_id;
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE authorization_id=p_authorization_id
    AND lease_attempt=v_node.attempt;
  SELECT * INTO v_event FROM workflow_run_events
  WHERE id=v_authorization.completion_event_id;
  IF v_result.authorization_id IS NULL OR v_claim.claim_event_id IS NULL
     OR v_run.id IS NULL OR v_node.id IS NULL OR v_event.id IS NULL THEN
    RETURN false;
  END IF;
  v_actor := v_authorization.authorization_document#>'{workflow,actor}';
  RETURN qualification_release_v1_result_is_exact(p_authorization_id) IS TRUE
    AND v_run.status='completed' AND v_run.completed_at=v_event.created_at
    AND v_run.failure IS NULL AND v_run.cancelled_at IS NULL
    AND qualification_release_v1_lease_claim_chain_is_exact(
          p_authorization_id,v_node.attempt
        ) IS TRUE
    AND qualification_release_v1_lease_claim_is_exact(
          v_claim.claim_event_id
        ) IS TRUE
    AND v_claim.event_sequence=
          v_authorization.action_event_sequence+v_node.attempt
    AND v_run.event_cursor=v_claim.event_sequence+1
    AND v_run.context#>ARRAY['nodes',v_node.node_key,'executionActor']=v_actor
    AND v_run.context#>ARRAY['nodes',v_node.node_key,'output']
      =v_result.publish_result_document
    AND v_node.status='completed'
    AND v_node.input_manifest_id IS NULL
    AND v_node.output_proposal_id IS NULL
    AND v_node.output_revision_id IS NULL
    AND v_node.completed_at=v_event.created_at
    AND v_node.lease_owner IS NULL AND v_node.lease_expires_at IS NULL
    AND v_node.failure IS NULL
    AND v_event.run_id=v_run.id
    AND v_event.sequence=v_claim.event_sequence+1
    AND v_event.event_type='node.completed'
    AND v_event.node_key=v_node.node_key
    AND v_event.actor_id=v_authorization.actor_id
    AND v_event.payload=pg_catalog.jsonb_build_object(
      'attempt',v_node.attempt,'claimEventId',v_claim.claim_event_id::text,
      'leaseOwner',v_claim.lease_owner,'role',v_authorization.actor_role,
      'action','publish','source','authenticated_command',
      'authorizedAt',qualification_release_v1_timestamp(
        v_authorization.authorized_at
      )
    )
    AND EXISTS (
      SELECT 1 FROM outbox_events AS outbox
      WHERE outbox.id=v_event.id
        AND outbox.aggregate_type='workflow_run'
        AND outbox.aggregate_id=v_run.id::text
        AND outbox.event_type='node.completed'
        AND outbox.subject='worksflow.workflow.run.event'
        AND outbox.headers='{}'::jsonb
        AND outbox.created_at=v_event.created_at
        AND outbox.payload=pg_catalog.jsonb_build_object(
          'id',v_event.id::text,'projectId',v_authorization.project_id::text,
          'runId',v_run.id::text,'sequence',v_event.sequence,
          'type','node.completed',
          'occurredAt',qualification_release_v1_timestamp(v_event.created_at),
          'payload',v_event.payload,'nodeKey',v_node.node_key,
          'actorId',v_authorization.actor_id::text
        )
    )
    AND NOT EXISTS (
      SELECT 1 FROM qualification_release_v1_transaction_grants
      WHERE authorization_id=p_authorization_id
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_release_v1_failure_completion_is_exact(
  p_authorization_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_result qualification_release_v1_results%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
  v_actor jsonb;
BEGIN
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=v_authorization.workflow_run_id;
  SELECT * INTO v_node FROM workflow_node_runs
  WHERE id=v_authorization.publish_node_run_id;
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE authorization_id=p_authorization_id
    AND lease_attempt=v_node.attempt;
  SELECT * INTO v_event FROM workflow_run_events
  WHERE id=v_authorization.completion_event_id;
  IF v_result.authorization_id IS NULL OR v_claim.claim_event_id IS NULL
     OR v_run.id IS NULL OR v_node.id IS NULL OR v_event.id IS NULL THEN
    RETURN false;
  END IF;
  v_actor := v_authorization.authorization_document#>'{workflow,actor}';
  RETURN qualification_release_v1_failure_is_exact(
           p_authorization_id
         ) IS TRUE
    AND v_run.status='failed' AND v_run.completed_at=v_event.created_at
    AND v_run.failure=v_result.failure_document
    AND v_run.cancelled_at IS NULL
    AND qualification_release_v1_lease_claim_chain_is_exact(
          p_authorization_id,v_node.attempt
        ) IS TRUE
    AND qualification_release_v1_lease_claim_is_exact(
          v_claim.claim_event_id
        ) IS TRUE
    AND v_claim.event_sequence=
          v_authorization.action_event_sequence+v_node.attempt
    AND v_run.event_cursor=v_claim.event_sequence+1
    AND v_run.context#>ARRAY['nodes',v_node.node_key,'executionActor']=v_actor
    AND v_run.context#>ARRAY['nodes',v_node.node_key,'output'] IS NULL
    AND v_node.status='failed'
    AND v_node.input_manifest_id IS NULL
    AND v_node.output_proposal_id IS NULL
    AND v_node.output_revision_id IS NULL
    AND v_node.completed_at=v_event.created_at
    AND v_node.lease_owner IS NULL AND v_node.lease_expires_at IS NULL
    AND v_node.failure=v_result.failure_document
    AND v_event.run_id=v_run.id
    AND v_event.sequence=v_claim.event_sequence+1
    AND v_event.event_type='node.failed'
    AND v_event.node_key=v_node.node_key
    AND v_event.actor_id=v_authorization.actor_id
    AND v_event.payload=pg_catalog.jsonb_build_object(
      'attempt',v_node.attempt,'claimEventId',v_claim.claim_event_id::text,
      'leaseOwner',v_claim.lease_owner,'role',v_authorization.actor_role,
      'action','publish','source','authenticated_command',
      'authorizedAt',qualification_release_v1_timestamp(
        v_authorization.authorized_at
      ),
      'outcome',v_result.outcome,'failure',v_result.failure_document
    )
    AND EXISTS (
      SELECT 1 FROM outbox_events AS outbox
      WHERE outbox.id=v_event.id
        AND outbox.aggregate_type='workflow_run'
        AND outbox.aggregate_id=v_run.id::text
        AND outbox.event_type='node.failed'
        AND outbox.subject='worksflow.workflow.run.event'
        AND outbox.headers='{}'::jsonb
        AND outbox.created_at=v_event.created_at
        AND outbox.payload=pg_catalog.jsonb_build_object(
          'id',v_event.id::text,'projectId',v_authorization.project_id::text,
          'runId',v_run.id::text,'sequence',v_event.sequence,
          'type','node.failed',
          'occurredAt',qualification_release_v1_timestamp(v_event.created_at),
          'payload',v_event.payload,'nodeKey',v_node.node_key,
          'actorId',v_authorization.actor_id::text
        )
    )
    AND NOT EXISTS (
      SELECT 1 FROM qualification_release_v1_transaction_grants
      WHERE authorization_id=p_authorization_id
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

-- Migration 82 deliberately denied every v3 completed state until this
-- authority existed. Preserve its exact Handoff transition checks while
-- opening only the run projection backed by a committed healthy result and
-- the preallocated completion event. The qualification_release trigger fires
-- first by name and consumes the transaction-scoped run grant.
CREATE OR REPLACE FUNCTION guard_workflow_execution_profile_v3_run()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_result qualification_release_v1_results%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_publish workflow_node_runs%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
BEGIN
  IF NEW.execution_profile_version = 'workflow-engine/v3'
     OR NEW.execution_profile_hash =
       '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR NEW.status = 'waiting_qualification' THEN
    IF NEW.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
       OR NEW.execution_profile_hash IS DISTINCT FROM
         '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
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
      SELECT * INTO v_authorization
      FROM qualification_release_v1_authorizations
      WHERE workflow_run_id=NEW.id;
      SELECT * INTO v_result FROM qualification_release_v1_results
      WHERE authorization_id=v_authorization.authorization_id;
      SELECT * INTO v_publish FROM workflow_node_runs
      WHERE id=v_authorization.publish_node_run_id;
      SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
      WHERE authorization_id=v_authorization.authorization_id
        AND lease_attempt=v_publish.attempt;
      SELECT * INTO v_event FROM workflow_run_events
      WHERE id=v_authorization.completion_event_id;
      IF TG_OP IS DISTINCT FROM 'UPDATE' OR OLD.status IS DISTINCT FROM 'running'
         OR v_authorization.authorization_id IS NULL
         OR qualification_release_v1_result_is_exact(
              v_authorization.authorization_id
            ) IS NOT TRUE
         OR v_claim.claim_event_id IS NULL
         OR v_publish.id IS NULL
         OR v_publish.input_manifest_id IS NOT NULL
         OR v_publish.output_proposal_id IS NOT NULL
         OR v_publish.output_revision_id IS NOT NULL
         OR qualification_release_v1_lease_claim_chain_is_exact(
              v_authorization.authorization_id,v_claim.lease_attempt
            ) IS NOT TRUE
         OR NEW.completed_at IS NULL OR NEW.cancelled_at IS NOT NULL
         OR NEW.failure IS NOT NULL
         OR NEW.event_cursor IS DISTINCT FROM v_claim.event_sequence+1
         OR NEW.completed_at IS DISTINCT FROM v_event.created_at
         OR NEW.context#>ARRAY[
              'nodes',v_authorization.publish_node_key,'executionActor'
            ] IS DISTINCT FROM
              v_authorization.authorization_document#>'{workflow,actor}'
         OR NEW.context#>ARRAY[
              'nodes',v_authorization.publish_node_key,'output'
            ] IS DISTINCT FROM v_result.publish_result_document
         OR v_event.run_id IS DISTINCT FROM NEW.id
         OR v_event.sequence IS DISTINCT FROM NEW.event_cursor
         OR v_event.event_type IS DISTINCT FROM 'node.completed'
         OR v_event.node_key IS DISTINCT FROM v_authorization.publish_node_key
         OR v_event.actor_id IS DISTINCT FROM v_authorization.actor_id THEN
        RAISE EXCEPTION 'workflow-engine/v3 cannot complete before the qualified release authority'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.status = 'failed' AND EXISTS (
      SELECT 1 FROM qualification_release_v1_authorizations
      WHERE workflow_run_id=NEW.id
    ) THEN
      SELECT * INTO v_authorization
      FROM qualification_release_v1_authorizations
      WHERE workflow_run_id=NEW.id;
      SELECT * INTO v_result FROM qualification_release_v1_results
      WHERE authorization_id=v_authorization.authorization_id;
      SELECT * INTO v_publish FROM workflow_node_runs
      WHERE id=v_authorization.publish_node_run_id;
      SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
      WHERE authorization_id=v_authorization.authorization_id
        AND lease_attempt=v_publish.attempt;
      SELECT * INTO v_event FROM workflow_run_events
      WHERE id=v_authorization.completion_event_id;
      IF TG_OP IS DISTINCT FROM 'UPDATE' OR OLD.status IS DISTINCT FROM 'running'
         OR v_authorization.authorization_id IS NULL
         OR qualification_release_v1_failure_is_exact(
              v_authorization.authorization_id
            ) IS NOT TRUE
         OR v_claim.claim_event_id IS NULL
         OR v_publish.id IS NULL
         OR v_publish.input_manifest_id IS NOT NULL
         OR v_publish.output_proposal_id IS NOT NULL
         OR v_publish.output_revision_id IS NOT NULL
         OR qualification_release_v1_lease_claim_chain_is_exact(
              v_authorization.authorization_id,v_claim.lease_attempt
            ) IS NOT TRUE
         OR NEW.completed_at IS NULL OR NEW.cancelled_at IS NOT NULL
         OR NEW.failure IS DISTINCT FROM v_result.failure_document
         OR NEW.event_cursor IS DISTINCT FROM v_claim.event_sequence+1
         OR NEW.completed_at IS DISTINCT FROM v_event.created_at
         OR NEW.context#>ARRAY[
              'nodes',v_authorization.publish_node_key,'executionActor'
            ] IS DISTINCT FROM
              v_authorization.authorization_document#>'{workflow,actor}'
         OR NEW.context#>ARRAY[
              'nodes',v_authorization.publish_node_key,'output'
            ] IS NOT NULL
         OR v_event.run_id IS DISTINCT FROM NEW.id
         OR v_event.sequence IS DISTINCT FROM NEW.event_cursor
         OR v_event.event_type IS DISTINCT FROM 'node.failed'
         OR v_event.node_key IS DISTINCT FROM v_authorization.publish_node_key
         OR v_event.actor_id IS DISTINCT FROM v_authorization.actor_id THEN
        RAISE EXCEPTION 'workflow-engine/v3 cannot fail before the qualified release authority'
          USING ERRCODE = '23514';
      END IF;
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

CREATE FUNCTION apply_qualification_release_result_v1(
  p_authorization_id uuid,
  p_workflow_run_id uuid,
  p_publish_node_run_id uuid,
  p_lease_owner text,
  p_lease_attempt integer
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_result qualification_release_v1_results%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_now timestamptz;
  v_event_payload jsonb;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release apply caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Release apply requires SERIALIZABLE isolation'
      USING ERRCODE='WQR03';
  END IF;
  IF p_authorization_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(p_authorization_id) IS NOT TRUE
     OR p_workflow_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_publish_node_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_publish_node_run_id::text) IS NOT TRUE
     OR p_lease_owner IS NULL OR p_lease_owner<>pg_catalog.btrim(p_lease_owner)
     OR pg_catalog.octet_length(p_lease_owner) NOT BETWEEN 1 AND 200
     OR p_lease_attempt IS NULL OR p_lease_attempt NOT BETWEEN 1 AND 2147483647 THEN
    RAISE EXCEPTION 'Qualification Release apply request is invalid'
      USING ERRCODE='WQR01';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-v1:'||p_workflow_run_id::text||':'
      ||p_publish_node_run_id::text,0
    )
  );
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF v_authorization.workflow_run_id IS DISTINCT FROM p_workflow_run_id
     OR v_authorization.publish_node_run_id IS DISTINCT FROM p_publish_node_run_id THEN
    RAISE EXCEPTION 'Qualification Release apply target conflicts'
      USING ERRCODE='WQR02';
  END IF;
  -- A lost completion response replays without a controller call.
  IF EXISTS (
       SELECT 1 FROM workflow_node_runs
       WHERE id=p_publish_node_run_id AND status='completed'
     ) THEN
    SELECT * INTO v_node FROM workflow_node_runs
    WHERE id=p_publish_node_run_id;
    SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
    WHERE authorization_id=p_authorization_id
      AND lease_attempt=v_node.attempt;
    IF qualification_release_v1_completion_is_exact(
         p_authorization_id
       ) IS NOT TRUE OR v_claim.claim_event_id IS NULL THEN
      RAISE EXCEPTION 'Qualification Release completed replay is corrupt'
        USING ERRCODE='WQR02';
    END IF;
    IF v_claim.lease_owner IS DISTINCT FROM p_lease_owner
       OR v_claim.lease_attempt IS DISTINCT FROM p_lease_attempt THEN
      RAISE EXCEPTION 'Qualification Release completed replay lease conflicts'
        USING ERRCODE='WQR03';
    END IF;
    RETURN NEXT qualification_release_v1_result_bundle(
      p_authorization_id,true,true
    );
    RETURN;
  END IF;
  PERFORM 1 FROM projects
  WHERE id=v_authorization.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=p_workflow_run_id FOR UPDATE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id=p_workflow_run_id ORDER BY id FOR UPDATE;
  SELECT * INTO v_node FROM workflow_node_runs
  WHERE id=p_publish_node_run_id;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE authorization_id=p_authorization_id
    AND lease_attempt=v_node.attempt FOR SHARE;
  IF qualification_release_v1_authorization_is_exact(
       p_authorization_id
     ) IS NOT TRUE
     OR qualification_release_v1_result_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release immutable result is not exact'
      USING ERRCODE='WQR02';
  END IF;
  IF v_run.status IS DISTINCT FROM 'running'
     OR v_run.completed_at IS NOT NULL OR v_run.failure IS NOT NULL
     OR v_claim.claim_event_id IS NULL
     OR qualification_release_v1_lease_claim_is_exact(
          v_claim.claim_event_id
        ) IS NOT TRUE
     OR qualification_release_v1_lease_claim_chain_is_exact(
          p_authorization_id,v_node.attempt
        ) IS NOT TRUE
     OR v_run.event_cursor IS DISTINCT FROM v_claim.event_sequence
     OR v_node.status IS DISTINCT FROM 'running'
     OR v_node.run_id IS DISTINCT FROM v_run.id
     OR v_node.node_key IS DISTINCT FROM v_authorization.publish_node_key
     OR v_node.lease_owner IS DISTINCT FROM p_lease_owner
     OR v_node.attempt IS DISTINCT FROM p_lease_attempt
     OR v_node.lease_expires_at IS NULL
     OR v_node.lease_expires_at <= pg_catalog.clock_timestamp()
     OR v_claim.lease_owner IS DISTINCT FROM p_lease_owner
     OR v_claim.lease_attempt IS DISTINCT FROM p_lease_attempt
     OR v_run.context#>ARRAY['nodes',v_node.node_key,'executionActor']
       IS DISTINCT FROM v_authorization.authorization_document#>'{workflow,actor}' THEN
    RAISE EXCEPTION 'Qualification Release Workflow lease is stale'
      USING ERRCODE='WQR03';
  END IF;
  v_now := pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  INSERT INTO qualification_release_v1_transaction_grants(
    authorization_id,grant_kind,target_kind,backend_pid,transaction_id,
    project_id,workflow_run_id,publish_node_run_id,production_run_id,
    from_status,to_status,lease_claim_event_id,
    lease_claim_event_sequence,lease_owner,lease_attempt,lease_expires_at,
    granted_at
  ) VALUES
    (p_authorization_id,'healthy_complete','run',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,v_authorization.project_id,
      v_run.id,v_node.id,v_result.production_run_id,'running','completed',
      v_claim.claim_event_id,v_claim.event_sequence,p_lease_owner,
      p_lease_attempt,v_node.lease_expires_at,v_now),
    (p_authorization_id,'healthy_complete','node',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,v_authorization.project_id,
      v_run.id,v_node.id,v_result.production_run_id,'running','completed',
      v_claim.claim_event_id,v_claim.event_sequence,p_lease_owner,
      p_lease_attempt,v_node.lease_expires_at,v_now);
  v_event_payload := pg_catalog.jsonb_build_object(
    'attempt',v_node.attempt,'claimEventId',v_claim.claim_event_id::text,
    'leaseOwner',v_claim.lease_owner,'role',v_authorization.actor_role,
    'action','publish','source','authenticated_command',
    'authorizedAt',qualification_release_v1_timestamp(
      v_authorization.authorized_at
    )
  );
  INSERT INTO workflow_run_events(
    id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
  ) VALUES (
    v_authorization.completion_event_id,v_run.id,v_run.event_cursor+1,
    'node.completed',v_node.node_key,v_event_payload,
    v_authorization.actor_id,v_now
  );
  INSERT INTO outbox_events(
    id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
    attempts,available_at,published_at,last_error,created_at
  ) VALUES (
    v_authorization.completion_event_id,'workflow_run',v_run.id::text,
    'node.completed','worksflow.workflow.run.event',
    pg_catalog.jsonb_build_object(
      'id',v_authorization.completion_event_id::text,
      'projectId',v_authorization.project_id::text,'runId',v_run.id::text,
      'sequence',v_run.event_cursor+1,'type','node.completed',
      'occurredAt',qualification_release_v1_timestamp(v_now),
      'payload',v_event_payload,'nodeKey',v_node.node_key,
      'actorId',v_authorization.actor_id::text
    ),'{}'::jsonb,0,v_now,NULL,NULL,v_now
  );
  UPDATE workflow_runs
  SET status='completed',event_cursor=v_run.event_cursor+1,
      context=pg_catalog.jsonb_set(
        context,ARRAY['nodes',v_node.node_key,'output'],
        v_result.publish_result_document,true
      ),completed_at=v_now,failure=NULL,updated_at=v_now
  WHERE id=v_run.id AND status='running' AND event_cursor=v_run.event_cursor;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release run completion CAS failed'
      USING ERRCODE='WQR03';
  END IF;
  UPDATE workflow_node_runs
  SET status='completed',lease_owner=NULL,lease_expires_at=NULL,
      completed_at=v_now,failure=NULL,updated_at=v_now
  WHERE id=v_node.id AND status='running' AND lease_owner=p_lease_owner
    AND attempt=p_lease_attempt;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release node completion CAS failed'
      USING ERRCODE='WQR03';
  END IF;
  IF qualification_release_v1_completion_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release completion closure is incomplete'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_result_bundle(
    p_authorization_id,true,false
  );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Release completion identity or closure conflict'
      USING ERRCODE='WQR02';
END;
$function$;

CREATE FUNCTION apply_qualification_release_failure_v1(
  p_authorization_id uuid,
  p_workflow_run_id uuid,
  p_publish_node_run_id uuid,
  p_lease_owner text,
  p_lease_attempt integer
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_result qualification_release_v1_results%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_now timestamptz;
  v_event_payload jsonb;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1',0
    )
  );
  IF qualification_release_v1_runtime_caller_is_exact() IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release failure apply caller is unauthorized'
      USING ERRCODE='42501';
  END IF;
  PERFORM qualification_release_v1_require_primary();
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Release failure apply requires SERIALIZABLE isolation'
      USING ERRCODE='WQR03';
  END IF;
  IF p_authorization_id IS NULL
     OR qualification_release_v1_uuid_v4_is_exact(
          p_authorization_id
        ) IS NOT TRUE
     OR p_workflow_run_id IS NULL
     OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_publish_node_run_id IS NULL
     OR workflow_input_uuid_is_exact(
          p_publish_node_run_id::text
        ) IS NOT TRUE
     OR p_lease_owner IS NULL
     OR p_lease_owner<>pg_catalog.btrim(p_lease_owner)
     OR pg_catalog.octet_length(p_lease_owner) NOT BETWEEN 1 AND 200
     OR p_lease_attempt IS NULL
     OR p_lease_attempt NOT BETWEEN 1 AND 2147483647 THEN
    RAISE EXCEPTION 'Qualification Release failure apply request is invalid'
      USING ERRCODE='WQR01';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-release-v1:'||p_workflow_run_id::text||':'
      ||p_publish_node_run_id::text,0
    )
  );
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF v_authorization.workflow_run_id IS DISTINCT FROM p_workflow_run_id
     OR v_authorization.publish_node_run_id IS DISTINCT FROM
          p_publish_node_run_id THEN
    RAISE EXCEPTION 'Qualification Release failure apply target conflicts'
      USING ERRCODE='WQR02';
  END IF;
  IF EXISTS (
       SELECT 1 FROM workflow_node_runs
       WHERE id=p_publish_node_run_id AND status='failed'
     ) THEN
    SELECT * INTO v_node FROM workflow_node_runs
    WHERE id=p_publish_node_run_id;
    SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
    WHERE authorization_id=p_authorization_id
      AND lease_attempt=v_node.attempt;
    IF qualification_release_v1_failure_completion_is_exact(
         p_authorization_id
       ) IS NOT TRUE OR v_claim.claim_event_id IS NULL THEN
      RAISE EXCEPTION 'Qualification Release failed replay is corrupt'
        USING ERRCODE='WQR02';
    END IF;
    IF v_claim.lease_owner IS DISTINCT FROM p_lease_owner
       OR v_claim.lease_attempt IS DISTINCT FROM p_lease_attempt THEN
      RAISE EXCEPTION 'Qualification Release failed replay lease conflicts'
        USING ERRCODE='WQR03';
    END IF;
    RETURN NEXT qualification_release_v1_failure_bundle(
      p_authorization_id,true,true
    );
    RETURN;
  ELSIF EXISTS (
    SELECT 1 FROM workflow_node_runs
    WHERE id=p_publish_node_run_id AND status='completed'
  ) THEN
    RAISE EXCEPTION 'Qualification Release terminal outcome conflicts'
      USING ERRCODE='WQR02';
  END IF;
  PERFORM 1 FROM projects
  WHERE id=v_authorization.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release project is unavailable'
      USING ERRCODE='WQR03';
  END IF;
  SELECT * INTO v_run FROM workflow_runs
  WHERE id=p_workflow_run_id FOR UPDATE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id=p_workflow_run_id ORDER BY id FOR UPDATE;
  SELECT * INTO v_node FROM workflow_node_runs
  WHERE id=p_publish_node_run_id;
  SELECT * INTO v_authorization FROM qualification_release_v1_authorizations
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_result FROM qualification_release_v1_results
  WHERE authorization_id=p_authorization_id FOR SHARE;
  SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
  WHERE authorization_id=p_authorization_id
    AND lease_attempt=v_node.attempt FOR SHARE;
  IF qualification_release_v1_authorization_is_exact(
       p_authorization_id
     ) IS NOT TRUE
     OR qualification_release_v1_failure_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release immutable failure is not exact'
      USING ERRCODE='WQR02';
  END IF;
  IF v_run.status IS DISTINCT FROM 'running'
     OR v_run.completed_at IS NOT NULL OR v_run.failure IS NOT NULL
     OR v_run.cancelled_at IS NOT NULL
     OR v_claim.claim_event_id IS NULL
     OR qualification_release_v1_lease_claim_is_exact(
          v_claim.claim_event_id
        ) IS NOT TRUE
     OR qualification_release_v1_lease_claim_chain_is_exact(
          p_authorization_id,v_node.attempt
        ) IS NOT TRUE
     OR v_run.event_cursor IS DISTINCT FROM v_claim.event_sequence
     OR v_node.status IS DISTINCT FROM 'running'
     OR v_node.run_id IS DISTINCT FROM v_run.id
     OR v_node.node_key IS DISTINCT FROM v_authorization.publish_node_key
     OR v_node.lease_owner IS DISTINCT FROM p_lease_owner
     OR v_node.attempt IS DISTINCT FROM p_lease_attempt
     OR v_node.lease_expires_at IS NULL
     OR v_node.lease_expires_at <= pg_catalog.clock_timestamp()
     OR v_claim.lease_owner IS DISTINCT FROM p_lease_owner
     OR v_claim.lease_attempt IS DISTINCT FROM p_lease_attempt
     OR v_run.context#>ARRAY['nodes',v_node.node_key,'executionActor']
       IS DISTINCT FROM v_authorization.authorization_document#>'{workflow,actor}'
     OR v_run.context#>ARRAY['nodes',v_node.node_key,'output'] IS NOT NULL THEN
    RAISE EXCEPTION 'Qualification Release Workflow lease is stale'
      USING ERRCODE='WQR03';
  END IF;
  v_now := pg_catalog.date_trunc('milliseconds',pg_catalog.clock_timestamp());
  INSERT INTO qualification_release_v1_transaction_grants(
    authorization_id,grant_kind,target_kind,backend_pid,transaction_id,
    project_id,workflow_run_id,publish_node_run_id,production_run_id,
    from_status,to_status,lease_claim_event_id,
    lease_claim_event_sequence,lease_owner,lease_attempt,lease_expires_at,
    granted_at
  ) VALUES
    (p_authorization_id,'terminal_fail','run',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,v_authorization.project_id,
      v_run.id,v_node.id,v_result.production_run_id,'running','failed',
      v_claim.claim_event_id,v_claim.event_sequence,p_lease_owner,
      p_lease_attempt,v_node.lease_expires_at,v_now),
    (p_authorization_id,'terminal_fail','node',pg_catalog.pg_backend_pid(),
      pg_catalog.pg_current_xact_id()::text,v_authorization.project_id,
      v_run.id,v_node.id,v_result.production_run_id,'running','failed',
      v_claim.claim_event_id,v_claim.event_sequence,p_lease_owner,
      p_lease_attempt,v_node.lease_expires_at,v_now);
  v_event_payload := pg_catalog.jsonb_build_object(
    'attempt',v_node.attempt,'claimEventId',v_claim.claim_event_id::text,
    'leaseOwner',v_claim.lease_owner,'role',v_authorization.actor_role,
    'action','publish','source','authenticated_command',
    'authorizedAt',qualification_release_v1_timestamp(
      v_authorization.authorized_at
    ),
    'outcome',v_result.outcome,'failure',v_result.failure_document
  );
  INSERT INTO workflow_run_events(
    id,run_id,sequence,event_type,node_key,payload,actor_id,created_at
  ) VALUES (
    v_authorization.completion_event_id,v_run.id,v_run.event_cursor+1,
    'node.failed',v_node.node_key,v_event_payload,
    v_authorization.actor_id,v_now
  );
  INSERT INTO outbox_events(
    id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
    attempts,available_at,published_at,last_error,created_at
  ) VALUES (
    v_authorization.completion_event_id,'workflow_run',v_run.id::text,
    'node.failed','worksflow.workflow.run.event',
    pg_catalog.jsonb_build_object(
      'id',v_authorization.completion_event_id::text,
      'projectId',v_authorization.project_id::text,'runId',v_run.id::text,
      'sequence',v_run.event_cursor+1,'type','node.failed',
      'occurredAt',qualification_release_v1_timestamp(v_now),
      'payload',v_event_payload,'nodeKey',v_node.node_key,
      'actorId',v_authorization.actor_id::text
    ),'{}'::jsonb,0,v_now,NULL,NULL,v_now
  );
  UPDATE workflow_runs
  SET status='failed',event_cursor=v_run.event_cursor+1,
      completed_at=v_now,cancelled_at=NULL,
      failure=v_result.failure_document,updated_at=v_now
  WHERE id=v_run.id AND status='running' AND event_cursor=v_run.event_cursor;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release run failure CAS failed'
      USING ERRCODE='WQR03';
  END IF;
  UPDATE workflow_node_runs
  SET status='failed',lease_owner=NULL,lease_expires_at=NULL,
      completed_at=v_now,failure=v_result.failure_document,updated_at=v_now
  WHERE id=v_node.id AND status='running' AND lease_owner=p_lease_owner
    AND attempt=p_lease_attempt;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Release node failure CAS failed'
      USING ERRCODE='WQR03';
  END IF;
  IF qualification_release_v1_failure_completion_is_exact(
       p_authorization_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Release failure closure is incomplete'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEXT qualification_release_v1_failure_bundle(
    p_authorization_id,true,false
  );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Release failure identity or closure conflict'
      USING ERRCODE='WQR02';
END;
$function$;

CREATE FUNCTION guard_qualification_release_v1_workflow_transition()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization qualification_release_v1_authorizations%ROWTYPE;
  v_claim qualification_release_v1_lease_claims%ROWTYPE;
  v_grant qualification_release_v1_transaction_grants%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
BEGIN
  IF TG_TABLE_NAME='workflow_runs' THEN
    IF NEW.execution_profile_version<>'workflow-engine/v3' THEN RETURN NEW; END IF;
    SELECT * INTO v_authorization
    FROM qualification_release_v1_authorizations
    WHERE workflow_run_id=NEW.id;
    IF NOT FOUND THEN
      IF OLD.status='waiting_input' AND NEW.status<>'waiting_input' THEN
        RAISE EXCEPTION 'workflow-engine/v3 Publish transition lacks authorization'
          USING ERRCODE='WQR02';
      END IF;
      RETURN NEW;
    END IF;
    IF OLD.status='waiting_input' AND NEW.status='running' THEN
      DELETE FROM qualification_release_v1_transaction_grants
      WHERE authorization_id=v_authorization.authorization_id
        AND grant_kind='authorize_ready' AND target_kind='run'
        AND backend_pid=pg_catalog.pg_backend_pid()
        AND transaction_id=pg_catalog.pg_current_xact_id()::text
        AND project_id=v_authorization.project_id
        AND workflow_run_id=NEW.id
        AND publish_node_run_id=v_authorization.publish_node_run_id
        AND production_run_id=v_authorization.expected_production_run_id
        AND from_status=OLD.status AND to_status=NEW.status
      RETURNING * INTO v_grant;
      IF NOT FOUND THEN
        RAISE EXCEPTION 'Publish run authorization has no exact transaction grant'
          USING ERRCODE='WQR02';
      END IF;
    ELSIF OLD.status='running' AND NEW.status='running'
          AND NEW.event_cursor IS DISTINCT FROM OLD.event_cursor THEN
      DELETE FROM qualification_release_v1_transaction_grants
      WHERE authorization_id=v_authorization.authorization_id
        AND grant_kind='claim_lease' AND target_kind='run'
        AND backend_pid=pg_catalog.pg_backend_pid()
        AND transaction_id=pg_catalog.pg_current_xact_id()::text
        AND project_id=v_authorization.project_id
        AND workflow_run_id=NEW.id
        AND publish_node_run_id=v_authorization.publish_node_run_id
        AND production_run_id=v_authorization.expected_production_run_id
        AND from_status=OLD.status AND to_status=NEW.status
        AND lease_claim_event_sequence=NEW.event_cursor
      RETURNING * INTO v_grant;
      SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
      WHERE claim_event_id=v_grant.lease_claim_event_id;
      IF NOT FOUND OR NEW.event_cursor<>OLD.event_cursor+1
         OR NEW.context IS DISTINCT FROM OLD.context
         OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
         OR NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at
         OR NEW.failure IS DISTINCT FROM OLD.failure
         OR v_claim.authorization_id IS DISTINCT FROM
              v_authorization.authorization_id
         OR v_claim.event_sequence IS DISTINCT FROM NEW.event_cursor
         OR v_claim.lease_owner IS DISTINCT FROM v_grant.lease_owner
         OR v_claim.lease_attempt IS DISTINCT FROM v_grant.lease_attempt
         OR v_claim.initial_lease_expires_at IS DISTINCT FROM
              v_grant.lease_expires_at
         OR qualification_release_v1_lease_claim_is_exact(
              v_claim.claim_event_id
            ) IS NOT TRUE THEN
        RAISE EXCEPTION 'Publish run claim has no exact transaction grant'
          USING ERRCODE='WQR02';
      END IF;
    ELSIF OLD.status='running' AND NEW.status='running' THEN
      IF NEW.context IS DISTINCT FROM OLD.context
         OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
         OR NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at
         OR NEW.failure IS DISTINCT FROM OLD.failure THEN
        RAISE EXCEPTION 'Publish run projection cannot bypass release authority'
          USING ERRCODE='WQR02';
      END IF;
    ELSIF OLD.status='running' AND NEW.status='completed' THEN
      DELETE FROM qualification_release_v1_transaction_grants
      WHERE authorization_id=v_authorization.authorization_id
        AND grant_kind='healthy_complete' AND target_kind='run'
        AND backend_pid=pg_catalog.pg_backend_pid()
        AND transaction_id=pg_catalog.pg_current_xact_id()::text
        AND project_id=v_authorization.project_id
        AND workflow_run_id=NEW.id
        AND publish_node_run_id=v_authorization.publish_node_run_id
        AND production_run_id=v_authorization.expected_production_run_id
        AND from_status=OLD.status AND to_status=NEW.status
        AND lease_claim_event_sequence=OLD.event_cursor
      RETURNING * INTO v_grant;
      SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
      WHERE claim_event_id=v_grant.lease_claim_event_id;
      IF NOT FOUND OR v_claim.authorization_id IS DISTINCT FROM
            v_authorization.authorization_id
         OR v_claim.event_sequence IS DISTINCT FROM OLD.event_cursor
         OR v_claim.lease_owner IS DISTINCT FROM v_grant.lease_owner
         OR v_claim.lease_attempt IS DISTINCT FROM v_grant.lease_attempt
         OR qualification_release_v1_lease_claim_is_exact(
              v_claim.claim_event_id
            ) IS NOT TRUE
         OR qualification_release_v1_result_is_exact(
           v_authorization.authorization_id
         ) IS NOT TRUE THEN
        RAISE EXCEPTION 'Publish run completion has no exact healthy grant'
          USING ERRCODE='WQR02';
      END IF;
    ELSIF OLD.status='running' AND NEW.status='failed' THEN
      DELETE FROM qualification_release_v1_transaction_grants
      WHERE authorization_id=v_authorization.authorization_id
        AND grant_kind='terminal_fail' AND target_kind='run'
        AND backend_pid=pg_catalog.pg_backend_pid()
        AND transaction_id=pg_catalog.pg_current_xact_id()::text
        AND project_id=v_authorization.project_id
        AND workflow_run_id=NEW.id
        AND publish_node_run_id=v_authorization.publish_node_run_id
        AND production_run_id=v_authorization.expected_production_run_id
        AND from_status=OLD.status AND to_status=NEW.status
        AND lease_claim_event_sequence=OLD.event_cursor
      RETURNING * INTO v_grant;
      SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
      WHERE claim_event_id=v_grant.lease_claim_event_id;
      IF NOT FOUND OR NEW.event_cursor<>OLD.event_cursor+1
         OR NEW.context IS DISTINCT FROM OLD.context
         OR NEW.completed_at IS NULL
         OR NEW.cancelled_at IS NOT NULL
         OR v_claim.authorization_id IS DISTINCT FROM
              v_authorization.authorization_id
         OR v_claim.event_sequence IS DISTINCT FROM OLD.event_cursor
         OR v_claim.lease_owner IS DISTINCT FROM v_grant.lease_owner
         OR v_claim.lease_attempt IS DISTINCT FROM v_grant.lease_attempt
         OR qualification_release_v1_lease_claim_is_exact(
              v_claim.claim_event_id
            ) IS NOT TRUE
         OR qualification_release_v1_failure_is_exact(
              v_authorization.authorization_id
            ) IS NOT TRUE
         OR NEW.failure IS DISTINCT FROM (
              SELECT failure_document
              FROM qualification_release_v1_results
              WHERE authorization_id=v_authorization.authorization_id
            ) THEN
        RAISE EXCEPTION 'Publish run failure has no exact terminal grant'
          USING ERRCODE='WQR02';
      END IF;
    ELSIF NEW.status IS DISTINCT FROM OLD.status
          OR NEW.context IS DISTINCT FROM OLD.context
          OR NEW.event_cursor IS DISTINCT FROM OLD.event_cursor
          OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
          OR NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at
          OR NEW.failure IS DISTINCT FROM OLD.failure THEN
      RAISE EXCEPTION 'Publish run transition cannot bypass release authority'
        USING ERRCODE='WQR02';
    END IF;
    RETURN NEW;
  END IF;

  SELECT * INTO v_run FROM workflow_runs WHERE id=NEW.run_id;
  IF NEW.node_type<>'publish'
     OR v_run.execution_profile_version<>'workflow-engine/v3' THEN RETURN NEW; END IF;
  SELECT * INTO v_authorization
  FROM qualification_release_v1_authorizations
  WHERE publish_node_run_id=NEW.id AND workflow_run_id=NEW.run_id;
  IF OLD.status='waiting_input' AND NEW.status='ready' THEN
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Publish node authorization lacks immutable authority'
        USING ERRCODE='WQR02';
    END IF;
    DELETE FROM qualification_release_v1_transaction_grants
    WHERE authorization_id=v_authorization.authorization_id
      AND grant_kind='authorize_ready' AND target_kind='node'
      AND backend_pid=pg_catalog.pg_backend_pid()
      AND transaction_id=pg_catalog.pg_current_xact_id()::text
      AND project_id=v_authorization.project_id
      AND workflow_run_id=NEW.run_id AND publish_node_run_id=NEW.id
      AND production_run_id=v_authorization.expected_production_run_id
      AND from_status=OLD.status AND to_status=NEW.status
    RETURNING * INTO v_grant;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Publish node authorization has no exact transaction grant'
        USING ERRCODE='WQR02';
    ELSIF NEW.input_manifest_id IS NOT NULL
          OR NEW.output_proposal_id IS NOT NULL
          OR NEW.output_revision_id IS NOT NULL THEN
      RAISE EXCEPTION 'Publish authorization cannot attach artifact references'
        USING ERRCODE='WQR02';
    END IF;
  ELSIF OLD.status IN ('ready','running') AND NEW.status='running'
        AND NEW.attempt=OLD.attempt+1 THEN
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Publish claim lacks immutable authorization'
        USING ERRCODE='WQR02';
    END IF;
    DELETE FROM qualification_release_v1_transaction_grants
    WHERE authorization_id=v_authorization.authorization_id
      AND grant_kind='claim_lease' AND target_kind='node'
      AND backend_pid=pg_catalog.pg_backend_pid()
      AND transaction_id=pg_catalog.pg_current_xact_id()::text
      AND project_id=v_authorization.project_id
      AND workflow_run_id=NEW.run_id AND publish_node_run_id=NEW.id
      AND production_run_id=v_authorization.expected_production_run_id
      AND from_status=OLD.status AND to_status=NEW.status
      AND lease_owner=NEW.lease_owner AND lease_attempt=NEW.attempt
      AND lease_expires_at=NEW.lease_expires_at
    RETURNING * INTO v_grant;
    SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
    WHERE claim_event_id=v_grant.lease_claim_event_id;
    IF NOT FOUND
       OR qualification_release_v1_authorization_is_exact(
            v_authorization.authorization_id
          ) IS NOT TRUE
       OR qualification_release_v1_lease_claim_is_exact(
            v_claim.claim_event_id
          ) IS NOT TRUE
       OR v_claim.authorization_id IS DISTINCT FROM
            v_authorization.authorization_id
       OR v_claim.event_sequence IS DISTINCT FROM
            v_grant.lease_claim_event_sequence
       OR v_claim.lease_attempt IS DISTINCT FROM NEW.attempt
       OR v_claim.lease_owner IS DISTINCT FROM NEW.lease_owner
       OR v_claim.initial_lease_expires_at IS DISTINCT FROM
            NEW.lease_expires_at
       OR NEW.lease_owner IS NULL OR NEW.lease_expires_at IS NULL
       OR NEW.started_at IS DISTINCT FROM COALESCE(
            OLD.started_at,v_claim.claimed_at
          )
       OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
       OR NEW.failure IS DISTINCT FROM OLD.failure
       OR NEW.input_manifest_id IS NOT NULL
       OR NEW.output_proposal_id IS NOT NULL
       OR NEW.output_revision_id IS NOT NULL
       OR v_run.context#>ARRAY['nodes',NEW.node_key,'executionActor']
         IS DISTINCT FROM v_authorization.authorization_document#>'{workflow,actor}' THEN
      RAISE EXCEPTION 'Publish claim lacks exact authenticated authority'
        USING ERRCODE='WQR02';
    END IF;
  ELSIF OLD.status='running' AND NEW.status='running'
        AND NEW.attempt=OLD.attempt
        AND NEW.lease_owner IS NOT DISTINCT FROM OLD.lease_owner
        AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Publish renew lacks immutable authorization'
        USING ERRCODE='WQR02';
    END IF;
    DELETE FROM qualification_release_v1_transaction_grants
    WHERE authorization_id=v_authorization.authorization_id
      AND grant_kind='renew_lease' AND target_kind='node'
      AND backend_pid=pg_catalog.pg_backend_pid()
      AND transaction_id=pg_catalog.pg_current_xact_id()::text
      AND project_id=v_authorization.project_id
      AND workflow_run_id=NEW.run_id AND publish_node_run_id=NEW.id
      AND production_run_id=v_authorization.expected_production_run_id
      AND from_status=OLD.status AND to_status=NEW.status
      AND lease_owner=NEW.lease_owner AND lease_attempt=NEW.attempt
      AND lease_expires_at=NEW.lease_expires_at
    RETURNING * INTO v_grant;
    SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
    WHERE claim_event_id=v_grant.lease_claim_event_id;
    IF NOT FOUND OR NEW.lease_expires_at<=OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
       OR NEW.failure IS DISTINCT FROM OLD.failure
       OR NEW.input_manifest_id IS NOT NULL
       OR NEW.output_proposal_id IS NOT NULL
       OR NEW.output_revision_id IS NOT NULL
       OR v_claim.authorization_id IS DISTINCT FROM
            v_authorization.authorization_id
       OR v_claim.event_sequence IS DISTINCT FROM
            v_grant.lease_claim_event_sequence
       OR v_claim.lease_attempt IS DISTINCT FROM NEW.attempt
       OR v_claim.lease_owner IS DISTINCT FROM NEW.lease_owner
       OR qualification_release_v1_lease_claim_is_exact(
            v_claim.claim_event_id
          ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Publish renew lacks exact lease authority'
        USING ERRCODE='WQR02';
    END IF;
  ELSIF OLD.status='running' AND NEW.status='completed' THEN
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Publish completion lacks immutable authorization'
        USING ERRCODE='WQR02';
    END IF;
    DELETE FROM qualification_release_v1_transaction_grants
    WHERE authorization_id=v_authorization.authorization_id
      AND grant_kind='healthy_complete' AND target_kind='node'
      AND backend_pid=pg_catalog.pg_backend_pid()
      AND transaction_id=pg_catalog.pg_current_xact_id()::text
      AND project_id=v_authorization.project_id
      AND workflow_run_id=NEW.run_id AND publish_node_run_id=NEW.id
      AND production_run_id=v_authorization.expected_production_run_id
      AND from_status=OLD.status AND to_status=NEW.status
      AND lease_owner=OLD.lease_owner AND lease_attempt=OLD.attempt
      AND lease_expires_at=OLD.lease_expires_at
    RETURNING * INTO v_grant;
    SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
    WHERE claim_event_id=v_grant.lease_claim_event_id;
    IF NOT FOUND OR v_claim.authorization_id IS DISTINCT FROM
          v_authorization.authorization_id
       OR v_claim.event_sequence IS DISTINCT FROM
            v_grant.lease_claim_event_sequence
       OR v_claim.lease_attempt IS DISTINCT FROM OLD.attempt
       OR v_claim.lease_owner IS DISTINCT FROM OLD.lease_owner
       OR qualification_release_v1_lease_claim_is_exact(
            v_claim.claim_event_id
          ) IS NOT TRUE
       OR qualification_release_v1_result_is_exact(
         v_authorization.authorization_id
       ) IS NOT TRUE
       OR NEW.input_manifest_id IS NOT NULL
       OR NEW.output_proposal_id IS NOT NULL
       OR NEW.output_revision_id IS NOT NULL THEN
      RAISE EXCEPTION 'Publish completion has no exact healthy transaction grant'
        USING ERRCODE='WQR02';
    END IF;
  ELSIF OLD.status='running' AND NEW.status='failed' THEN
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Publish failure lacks immutable authorization'
        USING ERRCODE='WQR02';
    END IF;
    DELETE FROM qualification_release_v1_transaction_grants
    WHERE authorization_id=v_authorization.authorization_id
      AND grant_kind='terminal_fail' AND target_kind='node'
      AND backend_pid=pg_catalog.pg_backend_pid()
      AND transaction_id=pg_catalog.pg_current_xact_id()::text
      AND project_id=v_authorization.project_id
      AND workflow_run_id=NEW.run_id AND publish_node_run_id=NEW.id
      AND production_run_id=v_authorization.expected_production_run_id
      AND from_status=OLD.status AND to_status=NEW.status
      AND lease_owner=OLD.lease_owner AND lease_attempt=OLD.attempt
      AND lease_expires_at=OLD.lease_expires_at
    RETURNING * INTO v_grant;
    SELECT * INTO v_claim FROM qualification_release_v1_lease_claims
    WHERE claim_event_id=v_grant.lease_claim_event_id;
    IF NOT FOUND OR NEW.lease_owner IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.completed_at IS NULL
       OR v_claim.authorization_id IS DISTINCT FROM
            v_authorization.authorization_id
       OR v_claim.event_sequence IS DISTINCT FROM
            v_grant.lease_claim_event_sequence
       OR v_claim.lease_attempt IS DISTINCT FROM OLD.attempt
       OR v_claim.lease_owner IS DISTINCT FROM OLD.lease_owner
       OR qualification_release_v1_lease_claim_is_exact(
            v_claim.claim_event_id
          ) IS NOT TRUE
       OR qualification_release_v1_failure_is_exact(
            v_authorization.authorization_id
          ) IS NOT TRUE
       OR NEW.failure IS DISTINCT FROM (
            SELECT failure_document
            FROM qualification_release_v1_results
            WHERE authorization_id=v_authorization.authorization_id
          )
      OR NEW.input_manifest_id IS NOT NULL
      OR NEW.output_proposal_id IS NOT NULL
      OR NEW.output_revision_id IS NOT NULL THEN
      RAISE EXCEPTION 'Publish failure has no exact terminal transaction grant'
        USING ERRCODE='WQR02';
    END IF;
  ELSIF (
          v_authorization.authorization_id IS NOT NULL
          OR OLD.status IN ('waiting_input','ready','running')
          OR NEW.status IN ('ready','running','completed','failed')
        ) AND (
          NEW.status IS DISTINCT FROM OLD.status
          OR NEW.attempt IS DISTINCT FROM OLD.attempt
          OR NEW.lease_owner IS DISTINCT FROM OLD.lease_owner
          OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
          OR NEW.started_at IS DISTINCT FROM OLD.started_at
          OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
          OR NEW.failure IS DISTINCT FROM OLD.failure
          OR NEW.input_manifest_id IS DISTINCT FROM OLD.input_manifest_id
          OR NEW.output_proposal_id IS DISTINCT FROM OLD.output_proposal_id
          OR NEW.output_revision_id IS DISTINCT FROM OLD.output_revision_id
        ) THEN
    RAISE EXCEPTION 'Publish node transition cannot bypass release authority'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER qualification_release_v1_run_transition_guard
BEFORE UPDATE OF status,context,event_cursor,completed_at,cancelled_at,failure
ON workflow_runs FOR EACH ROW
EXECUTE FUNCTION guard_qualification_release_v1_workflow_transition();
CREATE TRIGGER qualification_release_v1_node_transition_guard
BEFORE UPDATE OF status,attempt,input_manifest_id,output_proposal_id,
  output_revision_id,lease_owner,lease_expires_at,started_at,completed_at,failure
ON workflow_node_runs FOR EACH ROW
EXECUTE FUNCTION guard_qualification_release_v1_workflow_transition();

CREATE FUNCTION guard_qualification_release_v1_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE v_protected boolean;
BEGIN
  IF TG_TABLE_NAME='workflow_run_events' THEN
    SELECT EXISTS (
      SELECT 1 FROM qualification_release_v1_authorizations
      WHERE action_event_id=OLD.id OR completion_event_id=OLD.id
      UNION ALL
      SELECT 1 FROM qualification_release_v1_lease_claims
      WHERE claim_event_id=OLD.id
    ) INTO v_protected;
    IF NOT v_protected THEN RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END; END IF;
    RAISE EXCEPTION 'Qualification Release Workflow event is immutable'
      USING ERRCODE='WQR02';
  END IF;
  SELECT EXISTS (
    SELECT 1 FROM qualification_release_v1_authorizations
    WHERE action_event_id=OLD.id OR completion_event_id=OLD.id
    UNION ALL
    SELECT 1 FROM qualification_release_v1_lease_claims
    WHERE claim_event_id=OLD.id
  ) INTO v_protected;
  IF NOT v_protected THEN RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END; END IF;
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'Qualification Release Outbox event is immutable'
      USING ERRCODE='WQR02';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.aggregate_type IS DISTINCT FROM OLD.aggregate_type
     OR NEW.aggregate_id IS DISTINCT FROM OLD.aggregate_id
     OR NEW.event_type IS DISTINCT FROM OLD.event_type
     OR NEW.subject IS DISTINCT FROM OLD.subject
     OR NEW.payload IS DISTINCT FROM OLD.payload
     OR NEW.headers IS DISTINCT FROM OLD.headers
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'Qualification Release Outbox identity is immutable'
      USING ERRCODE='WQR02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER qualification_release_v1_workflow_event_immutable
BEFORE UPDATE OR DELETE ON workflow_run_events FOR EACH ROW
EXECUTE FUNCTION guard_qualification_release_v1_event_mutation();
CREATE TRIGGER qualification_release_v1_outbox_event_immutable
BEFORE UPDATE OR DELETE ON outbox_events FOR EACH ROW
EXECUTE FUNCTION guard_qualification_release_v1_event_mutation();

CREATE FUNCTION validate_qualification_release_v1_closure()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authorization_id uuid;
  v_ids uuid[]:=ARRAY[]::uuid[];
BEGIN
  IF TG_TABLE_NAME='qualification_release_v1_transaction_grants' THEN
    IF EXISTS (SELECT 1 FROM qualification_release_v1_transaction_grants) THEN
      RAISE EXCEPTION 'Qualification Release transaction grant survived commit'
        USING ERRCODE='WQR02';
    END IF;
    RETURN NULL;
  ELSIF TG_TABLE_NAME='qualification_release_v1_controller_bootstraps' THEN
    IF qualification_release_v1_controller_bootstrap_is_exact(
         CASE WHEN TG_OP='DELETE' THEN OLD.bootstrap_id ELSE NEW.bootstrap_id END
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release bootstrap closure is incomplete'
        USING ERRCODE='WQR02';
    END IF;
    RETURN NULL;
  ELSIF TG_TABLE_NAME='qualification_release_v1_authorizations' THEN
    v_ids:=ARRAY[CASE WHEN TG_OP='DELETE' THEN OLD.authorization_id ELSE NEW.authorization_id END];
  ELSIF TG_TABLE_NAME='qualification_release_v1_controller_bindings' THEN
    v_ids:=ARRAY[CASE WHEN TG_OP='DELETE' THEN OLD.authorization_id ELSE NEW.authorization_id END];
  ELSIF TG_TABLE_NAME='qualification_release_v1_lease_claims' THEN
    v_ids:=ARRAY[CASE WHEN TG_OP='DELETE' THEN OLD.authorization_id ELSE NEW.authorization_id END];
  ELSIF TG_TABLE_NAME='qualification_release_v1_results' THEN
    v_ids:=ARRAY[CASE WHEN TG_OP='DELETE' THEN OLD.authorization_id ELSE NEW.authorization_id END];
  ELSIF TG_TABLE_NAME='workflow_runs' THEN
    SELECT COALESCE(pg_catalog.array_agg(authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM qualification_release_v1_authorizations
    WHERE workflow_run_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  ELSIF TG_TABLE_NAME='workflow_node_runs' THEN
    SELECT COALESCE(pg_catalog.array_agg(authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM qualification_release_v1_authorizations
    WHERE publish_node_run_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  ELSIF TG_TABLE_NAME='workflow_run_events' OR TG_TABLE_NAME='outbox_events' THEN
    SELECT COALESCE(pg_catalog.array_agg(authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM (
      SELECT authorization_id FROM qualification_release_v1_authorizations
      WHERE action_event_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END
         OR completion_event_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END
      UNION
      SELECT authorization_id FROM qualification_release_v1_lease_claims
      WHERE claim_event_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END
    ) AS affected;
  ELSIF TG_TABLE_NAME='release_deployment_runs' THEN
    SELECT COALESCE(pg_catalog.array_agg(authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM qualification_release_v1_controller_bindings
    WHERE production_run_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  ELSIF TG_TABLE_NAME='release_delivery_operations' THEN
    SELECT COALESCE(pg_catalog.array_agg(authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM qualification_release_v1_controller_bindings
    WHERE controller_operation_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  ELSIF TG_TABLE_NAME='release_delivery_operation_results' THEN
    SELECT COALESCE(pg_catalog.array_agg(result.authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM qualification_release_v1_results AS result
    WHERE result.controller_operation_id=CASE WHEN TG_OP='DELETE' THEN OLD.operation_id ELSE NEW.operation_id END;
  ELSIF TG_TABLE_NAME='release_production_receipts' THEN
    SELECT COALESCE(pg_catalog.array_agg(authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM qualification_release_v1_results
    WHERE production_receipt_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  ELSIF TG_TABLE_NAME='release_deployment_revisions' THEN
    SELECT COALESCE(pg_catalog.array_agg(authorization_id),ARRAY[]::uuid[])
    INTO v_ids FROM qualification_release_v1_results
    WHERE deployment_revision_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END;
  END IF;
  FOREACH v_authorization_id IN ARRAY v_ids LOOP
    IF qualification_release_v1_authorization_is_exact(v_authorization_id) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release authorization closure is incomplete'
        USING ERRCODE='WQR02';
    END IF;
    IF EXISTS (
         SELECT 1 FROM qualification_release_v1_controller_bindings
         WHERE authorization_id=v_authorization_id
       ) AND qualification_release_v1_controller_binding_is_exact(
         v_authorization_id
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release controller closure is incomplete'
        USING ERRCODE='WQR02';
    END IF;
    IF EXISTS (
         SELECT 1 FROM qualification_release_v1_lease_claims
         WHERE authorization_id=v_authorization_id
       ) AND qualification_release_v1_lease_claim_chain_is_exact(
         v_authorization_id,
         (SELECT pg_catalog.max(lease_attempt)
          FROM qualification_release_v1_lease_claims
          WHERE authorization_id=v_authorization_id)
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Release lease claim closure is incomplete'
        USING ERRCODE='WQR02';
    END IF;
    IF EXISTS (
         SELECT 1 FROM qualification_release_v1_results
         WHERE authorization_id=v_authorization_id
       ) AND NOT (
         qualification_release_v1_result_is_exact(v_authorization_id) IS TRUE
         OR qualification_release_v1_failure_is_exact(v_authorization_id) IS TRUE
       ) THEN
      RAISE EXCEPTION 'Qualification Release result closure is incomplete'
        USING ERRCODE='WQR02';
    END IF;
    IF EXISTS (
         SELECT 1 FROM workflow_run_events AS event
         JOIN qualification_release_v1_authorizations AS release_authorization
           ON release_authorization.authorization_id=v_authorization_id
          AND event.id=release_authorization.completion_event_id
       ) AND NOT (
         qualification_release_v1_completion_is_exact(v_authorization_id) IS TRUE
         OR qualification_release_v1_failure_completion_is_exact(
              v_authorization_id
            ) IS TRUE
       ) THEN
      RAISE EXCEPTION 'Qualification Release Workflow terminal closure is incomplete'
        USING ERRCODE='WQR02';
    END IF;
  END LOOP;
  RETURN NULL;
END;
$function$;

CREATE CONSTRAINT TRIGGER qualification_release_grant_empty_closure
AFTER INSERT OR UPDATE OR DELETE ON qualification_release_v1_transaction_grants
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_bootstrap_exact_closure
AFTER INSERT ON qualification_release_v1_controller_bootstraps
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_authorization_exact_closure
AFTER INSERT ON qualification_release_v1_authorizations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_binding_exact_closure
AFTER INSERT ON qualification_release_v1_controller_bindings
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_lease_claim_exact_closure
AFTER INSERT ON qualification_release_v1_lease_claims
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_result_exact_closure
AFTER INSERT ON qualification_release_v1_results
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_workflow_run_exact_closure
AFTER UPDATE OR DELETE ON workflow_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_workflow_node_exact_closure
AFTER UPDATE OR DELETE ON workflow_node_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_workflow_event_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_run_events
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_outbox_exact_closure
AFTER INSERT OR DELETE ON outbox_events
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_production_run_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON release_deployment_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_controller_operation_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON release_delivery_operations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_controller_result_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON release_delivery_operation_results
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_production_receipt_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON release_production_receipts
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_release_deployment_revision_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON release_deployment_revisions
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_release_v1_closure();

DO $qualification_release_v1_security$
DECLARE
  v_schema text:=pg_catalog.current_schema();
  v_table text;
  v_signature text;
  v_role text;
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname='worksflow_qualification_release_operator'
      AND (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole
        OR rolreplication OR rolbypassrls)
  ) THEN
    RAISE EXCEPTION 'Qualification Release operator must be a private NOLOGIN role'
      USING ERRCODE='42501';
  END IF;
  FOREACH v_table IN ARRAY ARRAY[
    'qualification_release_v1_controller_bootstraps',
    'qualification_release_v1_identity_reservations',
    'qualification_release_v1_authorizations',
    'qualification_release_v1_controller_bindings',
    'qualification_release_v1_lease_claims',
    'qualification_release_v1_results',
    'qualification_release_v1_transaction_grants'
  ] LOOP
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON TABLE %I.%I FROM PUBLIC',v_schema,v_table
    );
    IF EXISTS (
      SELECT 1 FROM pg_catalog.pg_roles
      WHERE rolname='worksflow_migration_owner'
    ) THEN
      EXECUTE pg_catalog.format(
        'ALTER TABLE %I.%I OWNER TO worksflow_migration_owner',v_schema,v_table
      );
    END IF;
  END LOOP;
  FOREACH v_signature IN ARRAY ARRAY[
    'qualification_release_v1_hash(text,bytea)',
    'qualification_release_v1_timestamp(timestamptz)',
    'qualification_release_v1_uuid_v4_is_exact(uuid)',
    'reject_qualification_release_v1_mutation()',
    'qualification_release_v1_runtime_caller_is_exact()',
    'qualification_release_v1_bootstrap_caller_is_exact()',
    'qualification_release_v1_require_primary()',
    'qualification_release_v1_controller_bootstrap_is_exact(uuid)',
    'qualification_release_v1_bootstrap_bundle(uuid,boolean,boolean)',
    'bootstrap_qualification_release_controller_v1(uuid,text,text,text,text)',
    'inspect_qualification_release_controller_bootstrap_v1()',
    'qualification_release_v1_workspace_revision_document(uuid)',
    'qualification_release_v1_authorization_is_exact(uuid)',
    'qualification_release_v1_authorization_bundle(uuid,boolean,boolean)',
    'authorize_qualification_release_v1(uuid,uuid,uuid,text,uuid,uuid,uuid,uuid)',
    'qualification_release_v1_lease_claim_is_exact(uuid)',
    'qualification_release_v1_lease_claim_chain_is_exact(uuid,integer)',
    'qualification_release_v1_lease_claim_bundle(uuid,boolean,boolean)',
    'claim_qualification_release_publish_v1(uuid,uuid,uuid,uuid,text,integer)',
    'inspect_qualification_release_publish_claim_v1(uuid)',
    'renew_qualification_release_publish_lease_v1(uuid,uuid,uuid,uuid,text,integer,timestamptz,timestamptz)',
    'inspect_qualification_release_operation_v1(uuid)',
    'resolve_qualification_release_authorization_v1(uuid)',
    'resolve_qualification_release_for_publish_v1(uuid,uuid)',
    'qualification_release_v1_controller_binding_is_exact(uuid)',
    'qualification_release_v1_controller_bundle(uuid,boolean,boolean)',
    'start_qualification_release_controller_v1(uuid,uuid,text,integer)',
    'inspect_qualification_release_controller_v1(uuid)',
    'qualification_release_v1_result_is_exact(uuid)',
    'qualification_release_v1_result_bundle(uuid,boolean,boolean)',
    'record_qualification_release_result_v1(uuid)',
    'inspect_qualification_release_result_v1(uuid)',
    'qualification_release_v1_failure_is_exact(uuid)',
    'qualification_release_v1_failure_bundle(uuid,boolean,boolean)',
    'record_qualification_release_failure_v1(uuid)',
    'inspect_qualification_release_failure_v1(uuid)',
    'qualification_release_v1_completion_is_exact(uuid)',
    'qualification_release_v1_failure_completion_is_exact(uuid)',
    'apply_qualification_release_result_v1(uuid,uuid,uuid,text,integer)',
    'apply_qualification_release_failure_v1(uuid,uuid,uuid,text,integer)',
    'guard_workflow_execution_profile_v3_run()',
    'guard_qualification_release_v1_workflow_transition()',
    'guard_qualification_release_v1_event_mutation()',
    'validate_qualification_release_v1_closure()'
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
        'qualification_release_v1_controller_bootstraps',
        'qualification_release_v1_identity_reservations',
        'qualification_release_v1_authorizations',
        'qualification_release_v1_controller_bindings',
        'qualification_release_v1_lease_claims',
        'qualification_release_v1_results',
        'qualification_release_v1_transaction_grants'
      ] LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON TABLE %I.%I FROM %I',v_schema,v_table,v_role
        );
      END LOOP;
      FOREACH v_signature IN ARRAY ARRAY[
        'qualification_release_v1_hash(text,bytea)',
        'qualification_release_v1_timestamp(timestamptz)',
        'qualification_release_v1_uuid_v4_is_exact(uuid)',
        'reject_qualification_release_v1_mutation()',
        'qualification_release_v1_runtime_caller_is_exact()',
        'qualification_release_v1_bootstrap_caller_is_exact()',
        'qualification_release_v1_require_primary()',
        'qualification_release_v1_controller_bootstrap_is_exact(uuid)',
        'qualification_release_v1_bootstrap_bundle(uuid,boolean,boolean)',
        'bootstrap_qualification_release_controller_v1(uuid,text,text,text,text)',
        'inspect_qualification_release_controller_bootstrap_v1()',
        'qualification_release_v1_workspace_revision_document(uuid)',
        'qualification_release_v1_authorization_is_exact(uuid)',
        'qualification_release_v1_authorization_bundle(uuid,boolean,boolean)',
        'authorize_qualification_release_v1(uuid,uuid,uuid,text,uuid,uuid,uuid,uuid)',
        'qualification_release_v1_lease_claim_is_exact(uuid)',
        'qualification_release_v1_lease_claim_chain_is_exact(uuid,integer)',
        'qualification_release_v1_lease_claim_bundle(uuid,boolean,boolean)',
        'claim_qualification_release_publish_v1(uuid,uuid,uuid,uuid,text,integer)',
        'inspect_qualification_release_publish_claim_v1(uuid)',
        'renew_qualification_release_publish_lease_v1(uuid,uuid,uuid,uuid,text,integer,timestamptz,timestamptz)',
        'inspect_qualification_release_operation_v1(uuid)',
        'resolve_qualification_release_authorization_v1(uuid)',
        'resolve_qualification_release_for_publish_v1(uuid,uuid)',
        'qualification_release_v1_controller_binding_is_exact(uuid)',
        'qualification_release_v1_controller_bundle(uuid,boolean,boolean)',
        'start_qualification_release_controller_v1(uuid,uuid,text,integer)',
        'inspect_qualification_release_controller_v1(uuid)',
        'qualification_release_v1_result_is_exact(uuid)',
        'qualification_release_v1_result_bundle(uuid,boolean,boolean)',
        'record_qualification_release_result_v1(uuid)',
        'inspect_qualification_release_result_v1(uuid)',
        'qualification_release_v1_failure_is_exact(uuid)',
        'qualification_release_v1_failure_bundle(uuid,boolean,boolean)',
        'record_qualification_release_failure_v1(uuid)',
        'inspect_qualification_release_failure_v1(uuid)',
        'qualification_release_v1_completion_is_exact(uuid)',
        'qualification_release_v1_failure_completion_is_exact(uuid)',
        'apply_qualification_release_result_v1(uuid,uuid,uuid,text,integer)',
        'apply_qualification_release_failure_v1(uuid,uuid,uuid,text,integer)',
        'guard_workflow_execution_profile_v3_run()',
        'guard_qualification_release_v1_workflow_transition()',
        'guard_qualification_release_v1_event_mutation()',
        'validate_qualification_release_v1_closure()'
      ] LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON FUNCTION %I.%s FROM %I',v_schema,v_signature,v_role
        );
      END LOOP;
    END IF;
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname='worksflow_qualification_release_operator'
  ) THEN
    IF pg_catalog.has_schema_privilege(
      'worksflow_qualification_release_operator',v_schema,'USAGE'
    ) IS NOT TRUE THEN
      EXECUTE pg_catalog.format(
        'COMMENT ON FUNCTION %I.qualification_release_v1_runtime_caller_is_exact() IS %L',
        v_schema,
        '000084 granted schema USAGE to worksflow_qualification_release_operator'
      );
      EXECUTE pg_catalog.format(
        'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_release_operator',
        v_schema
      );
    END IF;
    FOREACH v_signature IN ARRAY ARRAY[
      'inspect_qualification_release_controller_bootstrap_v1()',
      'authorize_qualification_release_v1(uuid,uuid,uuid,text,uuid,uuid,uuid,uuid)',
      'claim_qualification_release_publish_v1(uuid,uuid,uuid,uuid,text,integer)',
      'inspect_qualification_release_publish_claim_v1(uuid)',
      'renew_qualification_release_publish_lease_v1(uuid,uuid,uuid,uuid,text,integer,timestamptz,timestamptz)',
      'inspect_qualification_release_operation_v1(uuid)',
      'resolve_qualification_release_authorization_v1(uuid)',
      'resolve_qualification_release_for_publish_v1(uuid,uuid)',
      'start_qualification_release_controller_v1(uuid,uuid,text,integer)',
      'inspect_qualification_release_controller_v1(uuid)',
      'record_qualification_release_result_v1(uuid)',
      'inspect_qualification_release_result_v1(uuid)',
      'record_qualification_release_failure_v1(uuid)',
      'inspect_qualification_release_failure_v1(uuid)',
      'apply_qualification_release_result_v1(uuid,uuid,uuid,text,integer)',
      'apply_qualification_release_failure_v1(uuid,uuid,uuid,text,integer)'
    ] LOOP
      EXECUTE pg_catalog.format(
        'GRANT EXECUTE ON FUNCTION %I.%s TO worksflow_qualification_release_operator',
        v_schema,v_signature
      );
    END LOOP;
  END IF;
END;
$qualification_release_v1_security$;

COMMENT ON FUNCTION authorize_qualification_release_v1(
  uuid,uuid,uuid,text,uuid,uuid,uuid,uuid
) IS 'Only ActionPublish authority for one exact workflow-engine/v3 Handoff output.';
COMMENT ON FUNCTION start_qualification_release_controller_v1(uuid,uuid,text,integer) IS
  'Creates or exact-replays one controller-bound production Run without remote I/O.';
COMMENT ON FUNCTION apply_qualification_release_result_v1(
  uuid,uuid,uuid,text,integer
) IS 'Only healthy-result completion mutation for workflow-engine/v3 Publish.';
COMMENT ON FUNCTION apply_qualification_release_failure_v1(
  uuid,uuid,uuid,text,integer
) IS 'Only exact terminal-failure mutation for workflow-engine/v3 Publish.';
