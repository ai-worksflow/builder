-- Snapshot-first Qualification Receipt v3 durable control plane.
--
-- bytea + SHA-256 is authoritative. JSONB/scalars are closed projections and
-- are rechecked by every owner-only write. JSONB is not treated as a raw
-- canonical-JSON authority; the migration owner is the trusted ALTER/DROP and
-- reviewed Go canonicalizer/verifier boundary.
DO $qualification_receipt_v3_hash_function$
DECLARE
  schema_name text := current_schema();
BEGIN
  EXECUTE format(
    'CREATE FUNCTION %I.qualification_receipt_v3_sha256(bytea) RETURNS text '
    'LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS %L',
    schema_name,
    'SELECT ''sha256:'' || pg_catalog.encode(pg_catalog.sha256($1), ''hex'')'
  );
END;
$qualification_receipt_v3_hash_function$;

-- request_hash is the durable request identity. Every UUID stored here is
-- already reserved by the immutable Plan: snapshot seal and verification
-- intentionally share operations.snapshotSeal and differ by kind/role/hash.
CREATE TABLE qualification_receipt_v3_requests (
  request_hash text PRIMARY KEY CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (octet_length(request_bytes) BETWEEN 1 AND 16777216),
  request_document jsonb NOT NULL CHECK (jsonb_typeof(request_document) = 'object'),
  plan_authority_id uuid NOT NULL REFERENCES qualification_plan_authorities(authority_id) ON DELETE RESTRICT,
  orchestration_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  request_kind text NOT NULL CHECK (request_kind IN ('snapshot-seal','snapshot-verify','receipt-sign')),
  signer_role text NOT NULL CHECK (signer_role IN (
    'sealer','verifier','qualification-runner','release-approver'
  )),
  operational_authority_id text NOT NULL CHECK (
    octet_length(operational_authority_id) BETWEEN 1 AND 256
    AND operational_authority_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
  ),
  authentication_key_id text NOT NULL CHECK (
    octet_length(authentication_key_id) BETWEEN 1 AND 256
    AND authentication_key_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
  ),
  signer_identity text NOT NULL CHECK (
    signer_identity = '' OR (
      octet_length(signer_identity) BETWEEN 1 AND 512
      AND signer_identity ~ '^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$'
    )
  ),
  signer_key_id text NOT NULL CHECK (
    signer_key_id = '' OR (
      octet_length(signer_key_id) BETWEEN 1 AND 256
      AND signer_key_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
    )
  ),
  snapshot_id text NOT NULL CHECK (
    octet_length(snapshot_id) BETWEEN 1 AND 256
    AND snapshot_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
  ),
  snapshot_digest text NOT NULL CHECK (snapshot_digest = '' OR snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
  receipt_id text NOT NULL CHECK (
    receipt_id = '' OR (
      octet_length(receipt_id) BETWEEN 1 AND 256
      AND receipt_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
    )
  ),

  plan_authority_hash text NOT NULL CHECK (plan_authority_hash ~ '^sha256:[0-9a-f]{64}$'),
  input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
  projection_hash text NOT NULL CHECK (projection_hash ~ '^sha256:[0-9a-f]{64}$'),
  evidence_plan_hash text NOT NULL CHECK (evidence_plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  target_hash text NOT NULL CHECK (target_hash ~ '^sha256:[0-9a-f]{64}$'),
  trust_hash text NOT NULL CHECK (trust_hash ~ '^sha256:[0-9a-f]{64}$'),
  trust_bindings_digest text NOT NULL CHECK (trust_bindings_digest ~ '^sha256:[0-9a-f]{64}$'),
  trust_policy_digest text NOT NULL CHECK (trust_policy_digest ~ '^sha256:[0-9a-f]{64}$'),

  evidence_head_version bigint NOT NULL CHECK (
    evidence_head_version BETWEEN 1 AND 9007199254740991
  ),
  evidence_last_event_id uuid NOT NULL REFERENCES qualification_evidence_events(event_id) ON DELETE RESTRICT,
  evidence_last_event_hash text NOT NULL CHECK (evidence_last_event_hash ~ '^sha256:[0-9a-f]{64}$'),
  evidence_command_digest text NOT NULL CHECK (evidence_command_digest ~ '^sha256:[0-9a-f]{64}$'),
  evidence_trust_digest text NOT NULL CHECK (evidence_trust_digest ~ '^sha256:[0-9a-f]{64}$'),
  evidence_closure_digest text NOT NULL CHECK (evidence_closure_digest ~ '^sha256:[0-9a-f]{64}$'),
  artifact_index_digest text NOT NULL CHECK (artifact_index_digest ~ '^sha256:[0-9a-f]{64}$'),

  payload_hash text NOT NULL CHECK (payload_hash = '' OR payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_bytes bytea CHECK (payload_bytes IS NULL OR octet_length(payload_bytes) BETWEEN 1 AND 8388608),
  pae_hash text NOT NULL CHECK (pae_hash = '' OR pae_hash ~ '^sha256:[0-9a-f]{64}$'),
  pae_bytes bytea CHECK (pae_bytes IS NULL OR octet_length(pae_bytes) BETWEEN 1 AND 8388864),
  started_at timestamptz NOT NULL CHECK (started_at = date_trunc('milliseconds', started_at)),

  CONSTRAINT qualification_receipt_v3_request_uuid_v4 CHECK (
    plan_authority_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND orchestration_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND operation_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND evidence_last_event_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_receipt_v3_request_role_kind CHECK (
    (request_kind = 'snapshot-seal' AND signer_role = 'sealer')
    OR (request_kind = 'snapshot-verify' AND signer_role = 'verifier')
    OR (request_kind = 'receipt-sign' AND signer_role IN ('qualification-runner','release-approver'))
  ),
  CONSTRAINT qualification_receipt_v3_request_closed_material CHECK (
    (request_kind = 'snapshot-seal'
      AND signer_identity = '' AND signer_key_id = '' AND snapshot_digest = ''
      AND receipt_id = '' AND payload_hash = '' AND payload_bytes IS NULL
      AND pae_hash = '' AND pae_bytes IS NULL)
    OR (request_kind = 'snapshot-verify'
      AND signer_identity = '' AND signer_key_id = '' AND snapshot_digest <> ''
      AND receipt_id = '' AND payload_hash = '' AND payload_bytes IS NULL
      AND pae_hash = '' AND pae_bytes IS NULL)
    OR (request_kind = 'receipt-sign'
      AND signer_identity <> '' AND signer_key_id <> '' AND snapshot_digest <> ''
      AND receipt_id <> '' AND payload_hash <> '' AND payload_bytes IS NOT NULL
      AND pae_hash <> '' AND pae_bytes IS NOT NULL AND payload_hash <> pae_hash)
  ),
  UNIQUE (plan_authority_id, request_kind, signer_role),
  UNIQUE (operation_id, request_kind, signer_role)
);

CREATE INDEX qualification_receipt_v3_requests_orchestration_idx
  ON qualification_receipt_v3_requests(orchestration_id, request_kind, signer_role);

-- observation_hash is the hash of the exact closed observation projection.
-- The externally supplied opaque lookup UUID never appears in this ledger.
CREATE TABLE qualification_receipt_v3_observations (
  request_hash text NOT NULL REFERENCES qualification_receipt_v3_requests(request_hash) ON DELETE RESTRICT,
  sequence bigint NOT NULL CHECK (sequence BETWEEN 1 AND 9007199254740991),
  generation bigint NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
  record_hash text NOT NULL CHECK (record_hash ~ '^sha256:[0-9a-f]{64}$'),
  observation_bytes bytea NOT NULL CHECK (octet_length(observation_bytes) BETWEEN 1 AND 1048576),
  observation_document jsonb NOT NULL CHECK (jsonb_typeof(observation_document) = 'object'),
  status text NOT NULL CHECK (status IN ('pending','not-invoked','committed','rejected')),
  observed_at timestamptz NOT NULL CHECK (observed_at = date_trunc('milliseconds', observed_at)),
  recorded_at timestamptz NOT NULL CHECK (recorded_at = date_trunc('milliseconds', recorded_at)),
  operational_authority_id text NOT NULL CHECK (
    octet_length(operational_authority_id) BETWEEN 1 AND 256
    AND operational_authority_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
  ),
  authentication_key_id text NOT NULL CHECK (
    octet_length(authentication_key_id) BETWEEN 1 AND 256
    AND authentication_key_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
  ),

  authentication_payload_hash text NOT NULL CHECK (
    authentication_payload_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  authentication_payload_bytes bytea NOT NULL CHECK (
    octet_length(authentication_payload_bytes) BETWEEN 1 AND 4194304
  ),
  authentication_payload_document jsonb NOT NULL CHECK (
    jsonb_typeof(authentication_payload_document) = 'object'
  ),
  authentication_envelope_hash text NOT NULL CHECK (
    authentication_envelope_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  authentication_envelope_bytes bytea NOT NULL CHECK (
    octet_length(authentication_envelope_bytes) BETWEEN 1 AND 4194304
  ),
  authentication_envelope_document jsonb NOT NULL CHECK (
    jsonb_typeof(authentication_envelope_document) = 'object'
  ),
  result_hash text CHECK (result_hash IS NULL OR result_hash ~ '^sha256:[0-9a-f]{64}$'),
  result_bytes bytea CHECK (result_bytes IS NULL OR octet_length(result_bytes) BETWEEN 1 AND 8388608),
  result_document jsonb CHECK (result_document IS NULL OR jsonb_typeof(result_document) = 'object'),
  signature_hash text CHECK (signature_hash IS NULL OR signature_hash ~ '^sha256:[0-9a-f]{64}$'),
  signature_bytes bytea CHECK (signature_bytes IS NULL OR octet_length(signature_bytes) = 64),
  claim_hash text CHECK (claim_hash IS NULL OR claim_hash ~ '^sha256:[0-9a-f]{64}$'),
  claim_id uuid UNIQUE,
  claim_bytes bytea CHECK (claim_bytes IS NULL OR octet_length(claim_bytes) BETWEEN 1 AND 1048576),
  claim_document jsonb CHECK (claim_document IS NULL OR jsonb_typeof(claim_document) = 'object'),
  acknowledgement_hash text CHECK (
    acknowledgement_hash IS NULL OR acknowledgement_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  acknowledgement_id uuid UNIQUE,
  acknowledgement_bytes bytea CHECK (
    acknowledgement_bytes IS NULL OR octet_length(acknowledgement_bytes) BETWEEN 1 AND 1048576
  ),
  acknowledgement_document jsonb CHECK (
    acknowledgement_document IS NULL OR jsonb_typeof(acknowledgement_document) = 'object'
  ),

  PRIMARY KEY (request_hash, sequence),
  UNIQUE (record_hash),
  UNIQUE (request_hash, record_hash),
  CONSTRAINT qualification_receipt_v3_observation_closed_material CHECK (
    (status IN ('pending','rejected')
      AND result_hash IS NULL AND result_bytes IS NULL AND result_document IS NULL
      AND signature_hash IS NULL AND signature_bytes IS NULL
      AND claim_hash IS NULL AND claim_id IS NULL AND claim_bytes IS NULL AND claim_document IS NULL
      AND acknowledgement_hash IS NULL AND acknowledgement_id IS NULL AND acknowledgement_bytes IS NULL
      AND acknowledgement_document IS NULL)
    OR (status = 'not-invoked'
      AND result_hash IS NULL AND result_bytes IS NULL AND result_document IS NULL
      AND signature_hash IS NULL AND signature_bytes IS NULL
      AND claim_hash IS NOT NULL AND claim_id IS NOT NULL AND claim_bytes IS NOT NULL AND claim_document IS NOT NULL
      AND acknowledgement_hash IS NOT NULL AND acknowledgement_id IS NOT NULL AND acknowledgement_bytes IS NOT NULL
      AND acknowledgement_document IS NOT NULL)
    OR (status = 'committed' AND (
      (result_hash IS NOT NULL AND result_bytes IS NOT NULL AND result_document IS NOT NULL
        AND signature_hash IS NULL AND signature_bytes IS NULL)
      OR (result_hash IS NULL AND result_bytes IS NULL AND result_document IS NULL
        AND signature_hash IS NOT NULL AND signature_bytes IS NOT NULL)
    ) AND claim_hash IS NULL AND claim_id IS NULL AND claim_bytes IS NULL AND claim_document IS NULL
      AND acknowledgement_hash IS NULL AND acknowledgement_id IS NULL AND acknowledgement_bytes IS NULL
      AND acknowledgement_document IS NULL)
  ),
  CONSTRAINT qualification_receipt_v3_observation_token_uuid_v4 CHECK (
    (claim_id IS NULL OR claim_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
    AND (acknowledgement_id IS NULL OR acknowledgement_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
    AND (claim_id IS NULL OR acknowledgement_id IS NULL OR claim_id <> acknowledgement_id)
  )
);

CREATE INDEX qualification_receipt_v3_observations_state_idx
  ON qualification_receipt_v3_observations(request_hash, generation, sequence, status);

CREATE TABLE qualification_receipt_v3_receipts (
  receipt_id text PRIMARY KEY CHECK (
    octet_length(receipt_id) BETWEEN 1 AND 256
    AND receipt_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
  ),
  plan_authority_id uuid NOT NULL UNIQUE REFERENCES qualification_plan_authorities(authority_id) ON DELETE RESTRICT,
  orchestration_id uuid NOT NULL,
  snapshot_operation_id uuid NOT NULL,
  receipt_sign_operation_id uuid NOT NULL UNIQUE,

  snapshot_request_hash text NOT NULL,
  snapshot_observation_hash text NOT NULL,
  verification_request_hash text NOT NULL,
  verification_observation_hash text NOT NULL,
  runner_request_hash text NOT NULL,
  runner_observation_hash text NOT NULL,
  approver_request_hash text NOT NULL,
  approver_observation_hash text NOT NULL,
  FOREIGN KEY (snapshot_request_hash, snapshot_observation_hash)
    REFERENCES qualification_receipt_v3_observations(request_hash, record_hash) ON DELETE RESTRICT,
  FOREIGN KEY (verification_request_hash, verification_observation_hash)
    REFERENCES qualification_receipt_v3_observations(request_hash, record_hash) ON DELETE RESTRICT,
  FOREIGN KEY (runner_request_hash, runner_observation_hash)
    REFERENCES qualification_receipt_v3_observations(request_hash, record_hash) ON DELETE RESTRICT,
  FOREIGN KEY (approver_request_hash, approver_observation_hash)
    REFERENCES qualification_receipt_v3_observations(request_hash, record_hash) ON DELETE RESTRICT,

  plan_authority_hash text NOT NULL CHECK (plan_authority_hash ~ '^sha256:[0-9a-f]{64}$'),
  evidence_closure_digest text NOT NULL CHECK (evidence_closure_digest ~ '^sha256:[0-9a-f]{64}$'),
  artifact_index_digest text NOT NULL CHECK (artifact_index_digest ~ '^sha256:[0-9a-f]{64}$'),
  snapshot_id text NOT NULL CHECK (octet_length(snapshot_id) BETWEEN 1 AND 256),
  snapshot_digest text NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),

  runner_identity text NOT NULL CHECK (octet_length(runner_identity) BETWEEN 1 AND 512),
  runner_key_id text NOT NULL CHECK (octet_length(runner_key_id) BETWEEN 1 AND 256),
  runner_signature_hash text NOT NULL CHECK (runner_signature_hash ~ '^sha256:[0-9a-f]{64}$'),
  runner_signature_bytes bytea NOT NULL CHECK (octet_length(runner_signature_bytes) = 64),
  approver_identity text NOT NULL CHECK (octet_length(approver_identity) BETWEEN 1 AND 512),
  approver_key_id text NOT NULL CHECK (octet_length(approver_key_id) BETWEEN 1 AND 256),
  approver_signature_hash text NOT NULL CHECK (approver_signature_hash ~ '^sha256:[0-9a-f]{64}$'),
  approver_signature_bytes bytea NOT NULL CHECK (octet_length(approver_signature_bytes) = 64),

  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  node_key text NOT NULL CHECK (octet_length(node_key) BETWEEN 1 AND 256),
  target_revision_id uuid NOT NULL,
  target_revision_content_hash text NOT NULL CHECK (target_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  subject text NOT NULL CHECK (
    octet_length(subject) BETWEEN 1 AND 256 AND subject = btrim(subject) AND subject !~ '[[:cntrl:]]'
  ),
  stage_gate text NOT NULL CHECK (stage_gate = 'external-qualification'),

  payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_bytes bytea NOT NULL CHECK (octet_length(payload_bytes) BETWEEN 1 AND 8388608),
  payload_document jsonb NOT NULL CHECK (jsonb_typeof(payload_document) = 'object'),
  pae_hash text NOT NULL CHECK (pae_hash ~ '^sha256:[0-9a-f]{64}$'),
  pae_bytes bytea NOT NULL CHECK (octet_length(pae_bytes) BETWEEN 1 AND 8388864),
  envelope_hash text NOT NULL CHECK (envelope_hash ~ '^sha256:[0-9a-f]{64}$'),
  envelope_bytes bytea NOT NULL CHECK (octet_length(envelope_bytes) BETWEEN 1 AND 16777216),
  envelope_document jsonb NOT NULL CHECK (jsonb_typeof(envelope_document) = 'object'),
  completion_hash text NOT NULL CHECK (completion_hash ~ '^sha256:[0-9a-f]{64}$'),
  completion_bytes bytea NOT NULL CHECK (octet_length(completion_bytes) BETWEEN 1 AND 1048576),
  completion_document jsonb NOT NULL CHECK (jsonb_typeof(completion_document) = 'object'),
  completed_at timestamptz NOT NULL CHECK (completed_at = date_trunc('milliseconds', completed_at)),

  CONSTRAINT qualification_receipt_v3_receipt_uuid_v4 CHECK (
    plan_authority_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND orchestration_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND snapshot_operation_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND receipt_sign_operation_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND project_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND workflow_run_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND target_revision_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND snapshot_operation_id <> receipt_sign_operation_id
  ),
  CONSTRAINT qualification_receipt_v3_receipt_distinct_requests CHECK (
    snapshot_request_hash <> verification_request_hash
    AND snapshot_request_hash <> runner_request_hash
    AND snapshot_request_hash <> approver_request_hash
    AND verification_request_hash <> runner_request_hash
    AND verification_request_hash <> approver_request_hash
    AND runner_request_hash <> approver_request_hash
  ),
  CONSTRAINT qualification_receipt_v3_receipt_independent_signers CHECK (
    runner_identity <> approver_identity AND runner_key_id <> approver_key_id
    AND runner_signature_hash <> approver_signature_hash
  )
);

CREATE INDEX qualification_receipt_v3_receipts_target_idx
  ON qualification_receipt_v3_receipts(project_id, workflow_run_id, node_key, target_revision_id);

CREATE FUNCTION reject_qualification_receipt_v3_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification Receipt v3 request, observation, and receipt ledgers are immutable'
    USING ERRCODE = 'WQR02';
END;
$function$;

CREATE FUNCTION guard_qualification_evidence_v1_receipt_tail_history_only()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  IF NEW.event_kind IN (
       'receipt-sign-started','receipt-signed','snapshot-seal-started',
       'snapshot-sealed','snapshot-verified'
     ) AND EXISTS (
       SELECT 1 FROM qualification_plan_authorities
       WHERE orchestration_id = NEW.orchestration_id
     ) THEN
    RAISE EXCEPTION 'Plan-linked Qualification Evidence v1 Receipt tail is history-only after Receipt v3 activation'
      USING ERRCODE = 'WQR02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE FUNCTION guard_qualification_promotion_v1_new_consumption_history_only()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification promotion v1 is history-only after Receipt v3 activation'
    USING ERRCODE = 'WQR02';
END;
$function$;

DO $qualification_receipt_v3_upgrade_fence$
BEGIN
  LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_requests IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_observations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_receipts IN SHARE ROW EXCLUSIVE MODE;

  IF EXISTS (
    SELECT 1
    FROM qualification_evidence_events AS event
    JOIN qualification_plan_authorities AS authority
      ON authority.orchestration_id = event.orchestration_id
    WHERE event.event_kind IN (
      'receipt-sign-started','receipt-signed','snapshot-seal-started',
      'snapshot-sealed','snapshot-verified'
    )
  ) THEN
    RAISE EXCEPTION 'cannot activate Qualification Receipt v3 over a Plan-linked v1 Receipt tail';
  END IF;

  -- Keep trigger DDL in this same lock-holding statement. Concatenating the
  -- keyword also lets the static exact count distinguish the three immutable
  -- table triggers from these two upgrade guards.
  EXECUTE 'CREATE ' ||
    'TRIGGER qualification_evidence_v1_receipt_tail_history_only '
    'BEFORE INSERT ON qualification_evidence_events FOR EACH ROW '
    'EXECUTE FUNCTION guard_qualification_evidence_v1_receipt_tail_history_only()';
  EXECUTE 'CREATE ' ||
    'TRIGGER qualification_promotion_v1_new_consumption_history_only '
    'BEFORE INSERT ON qualification_promotion_consumptions FOR EACH ROW '
    'EXECUTE FUNCTION guard_qualification_promotion_v1_new_consumption_history_only()';
END;
$qualification_receipt_v3_upgrade_fence$;


COMMENT ON TABLE qualification_receipt_v3_requests IS
  'Owner-only exact requests persisted before external seal, verify, or signer calls; no secret material.';
COMMENT ON TABLE qualification_receipt_v3_observations IS
  'Authenticated append-only request observations with DB-assigned record time and claim/ACK recovery proof.';
COMMENT ON TABLE qualification_receipt_v3_receipts IS
  'Immutable snapshot-first Receipt v3 DSSE completion; historical Receipt v2 cannot enter this table.';

CREATE FUNCTION complete_qualification_receipt_v3(
  p_plan_authority_id uuid,
  p_snapshot_request_hash text,
  p_snapshot_observation_hash text,
  p_verification_request_hash text,
  p_verification_observation_hash text,
  p_runner_request_hash text,
  p_runner_observation_hash text,
  p_approver_request_hash text,
  p_approver_observation_hash text,
  p_payload_hash text,
  p_payload_bytes bytea,
  p_payload_document jsonb,
  p_pae_hash text,
  p_pae_bytes bytea,
  p_envelope_hash text,
  p_envelope_bytes bytea,
  p_envelope_document jsonb,
  p_verification_envelope_hash text
) RETURNS TABLE (
  receipt_record qualification_receipt_v3_receipts,
  idempotent boolean
)
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_plan_authorities%ROWTYPE;
  v_head qualification_evidence_heads%ROWTYPE;
  v_event qualification_evidence_events%ROWTYPE;
  v_snapshot_request qualification_receipt_v3_requests%ROWTYPE;
  v_verification_request qualification_receipt_v3_requests%ROWTYPE;
  v_runner_request qualification_receipt_v3_requests%ROWTYPE;
  v_approver_request qualification_receipt_v3_requests%ROWTYPE;
  v_snapshot_observation qualification_receipt_v3_observations%ROWTYPE;
  v_verification_observation qualification_receipt_v3_observations%ROWTYPE;
  v_runner_observation qualification_receipt_v3_observations%ROWTYPE;
  v_approver_observation qualification_receipt_v3_observations%ROWTYPE;
  v_existing qualification_receipt_v3_receipts%ROWTYPE;
  v_receipt jsonb;
  v_target jsonb;
  v_runner_signature jsonb;
  v_approver_signature jsonb;
  v_completed_at timestamptz;
  v_completion_bytes bytea;
  v_completion_hash text;
  v_completion_document jsonb;
BEGIN
  IF p_plan_authority_id IS NULL
     OR p_plan_authority_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_snapshot_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_snapshot_observation_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_verification_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_verification_observation_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_runner_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_runner_observation_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_approver_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_approver_observation_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_payload_hash IS NULL OR p_payload_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_payload_bytes IS NULL OR octet_length(p_payload_bytes) NOT BETWEEN 1 AND 8388608
     OR get_byte(p_payload_bytes, 0) <> 123
     OR get_byte(p_payload_bytes, octet_length(p_payload_bytes) - 1) <> 125
     OR p_payload_document IS NULL OR jsonb_typeof(p_payload_document) <> 'object'
     OR qualification_receipt_v3_sha256(p_payload_bytes) <> p_payload_hash
     OR convert_from(p_payload_bytes, 'UTF8')::jsonb <> p_payload_document
     OR p_pae_hash IS NULL OR p_pae_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_pae_bytes IS NULL OR qualification_receipt_v3_sha256(p_pae_bytes) <> p_pae_hash
     OR p_pae_bytes <> (
       convert_to(
         'DSSEv1 28 application/vnd.in-toto+json ' || octet_length(p_payload_bytes)::text || ' ',
         'UTF8'
       ) || p_payload_bytes
     )
     OR p_envelope_hash IS NULL OR p_envelope_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_envelope_bytes IS NULL OR octet_length(p_envelope_bytes) NOT BETWEEN 1 AND 16777216
     OR get_byte(p_envelope_bytes, 0) <> 123
     OR get_byte(p_envelope_bytes, octet_length(p_envelope_bytes) - 1) <> 125
     OR p_envelope_document IS NULL OR jsonb_typeof(p_envelope_document) <> 'object'
     OR qualification_receipt_v3_sha256(p_envelope_bytes) <> p_envelope_hash
     OR convert_from(p_envelope_bytes, 'UTF8')::jsonb <> p_envelope_document
     OR p_verification_envelope_hash IS NULL
     OR p_verification_envelope_hash <> p_envelope_hash THEN
    RAISE EXCEPTION 'Qualification Receipt v3 completion raw byte/hash closure is invalid'
      USING ERRCODE = 'WQR03';
  END IF;
  IF NOT (p_payload_document ?& ARRAY['_type','predicate','predicateType','subject'])
     OR p_payload_document - ARRAY['_type','predicate','predicateType','subject'] <> '{}'::jsonb
     OR p_payload_document->>'_type' <> 'https://in-toto.io/Statement/v1'
     OR p_payload_document->>'predicateType'
        <> 'https://worksflow.dev/attestations/qualification-receipt/v3'
     OR jsonb_typeof(p_payload_document->'predicate') <> 'object'
     OR jsonb_typeof(p_payload_document->'subject') <> 'array'
     OR jsonb_array_length(p_payload_document->'subject') <> 1
     OR NOT (p_envelope_document ?& ARRAY['payload','payloadType','signatures'])
     OR p_envelope_document - ARRAY['payload','payloadType','signatures'] <> '{}'::jsonb
     OR p_envelope_document->>'payloadType' <> 'application/vnd.in-toto+json'
     OR (p_envelope_document->>'payload') !~ '^[A-Za-z0-9+/]*={0,2}$'
     OR decode(p_envelope_document->>'payload', 'base64') <> p_payload_bytes
     OR replace(encode(decode(p_envelope_document->>'payload', 'base64'), 'base64'), chr(10), '')
        <> p_envelope_document->>'payload'
     OR jsonb_typeof(p_envelope_document->'signatures') <> 'array'
     OR jsonb_array_length(p_envelope_document->'signatures') <> 2 THEN
    RAISE EXCEPTION 'Qualification Receipt v3 payload or DSSE envelope shape is invalid'
      USING ERRCODE = 'WQR03';
  END IF;
  v_receipt := p_payload_document->'predicate';
  IF NOT (v_receipt ?& ARRAY[
       'artifactIndex','build','completedAt','credentialSet','decision','evidence','evidencePlan',
       'goldenRuntime','issuedAt','operationId','planAuthority','qualificationManifest',
       'qualificationStartedAt','receiptId','schemaVersion','signers','snapshot',
       'snapshotVerification','source','target','templateRelease','trust'
     ])
     OR v_receipt - ARRAY[
       'artifactIndex','build','completedAt','credentialSet','decision','evidence','evidencePlan',
       'goldenRuntime','issuedAt','operationId','planAuthority','qualificationManifest',
       'qualificationStartedAt','receiptId','schemaVersion','signers','snapshot',
       'snapshotVerification','source','target','templateRelease','trust'
     ] <> '{}'::jsonb
     OR v_receipt->>'schemaVersion' <> 'worksflow-qualification-receipt/v3'
     OR v_receipt->>'decision' <> 'qualified' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 predicate is widened or invalid'
      USING ERRCODE = 'WQR03';
  END IF;

  LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_requests IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_observations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_receipts IN SHARE ROW EXCLUSIVE MODE;

  SELECT * INTO v_authority FROM qualification_plan_authorities
  WHERE authority_id = p_plan_authority_id;
  SELECT * INTO v_head FROM qualification_evidence_heads
  WHERE orchestration_id = v_authority.orchestration_id;
  SELECT * INTO v_event FROM qualification_evidence_events
  WHERE event_id = v_head.last_event_id;
  IF v_authority.authority_id IS NULL OR v_head.orchestration_id IS NULL OR v_event.event_id IS NULL
     OR qualification_receipt_v3_sha256(v_authority.input_bytes) <> v_authority.input_hash
     OR qualification_receipt_v3_sha256(v_authority.projection_bytes) <> v_authority.projection_hash
     OR qualification_receipt_v3_sha256(v_authority.evidence_plan_bytes) <> v_authority.evidence_plan_hash
     OR qualification_receipt_v3_sha256(v_authority.trust_bytes) <> v_authority.trust_hash
     OR qualification_receipt_v3_sha256(v_authority.target_bytes) <> v_authority.target_hash
     OR qualification_receipt_v3_sha256(v_authority.envelope_bytes) <> v_authority.envelope_hash
     OR v_head.phase <> 'artifact-indexed'
     OR v_head.plan_document <> v_authority.evidence_plan_document
     OR v_head.command_hash <> v_authority.evidence_plan_hash
     OR v_head.trust_bindings_digest <> v_authority.trust_bindings_digest
     OR v_event.orchestration_id <> v_head.orchestration_id
     OR v_event.version <> v_head.version
     OR v_event.event_id <> v_head.last_event_id
     OR v_event.event_kind <> 'artifact-indexed'
     OR qualification_receipt_v3_sha256(v_event.event_bytes) <> v_event.event_hash
     OR convert_from(v_event.event_bytes, 'UTF8')::jsonb <> v_event.event_document
     OR jsonb_typeof(v_event.event_document->'artifactIndex') IS DISTINCT FROM 'object'
     OR v_event.event_document->'artifactIndex'->>'stage' IS DISTINCT FROM 'committed' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 completion Plan or Evidence closure drifted'
      USING ERRCODE = 'WQR02';
  END IF;

  SELECT * INTO v_snapshot_request FROM qualification_receipt_v3_requests
  WHERE request_hash = p_snapshot_request_hash;
  SELECT * INTO v_verification_request FROM qualification_receipt_v3_requests
  WHERE request_hash = p_verification_request_hash;
  SELECT * INTO v_runner_request FROM qualification_receipt_v3_requests
  WHERE request_hash = p_runner_request_hash;
  SELECT * INTO v_approver_request FROM qualification_receipt_v3_requests
  WHERE request_hash = p_approver_request_hash;
  IF v_snapshot_request.request_hash IS NULL OR v_verification_request.request_hash IS NULL
     OR v_runner_request.request_hash IS NULL OR v_approver_request.request_hash IS NULL
     OR v_snapshot_request.plan_authority_id <> p_plan_authority_id
     OR v_verification_request.plan_authority_id <> p_plan_authority_id
     OR v_runner_request.plan_authority_id <> p_plan_authority_id
     OR v_approver_request.plan_authority_id <> p_plan_authority_id
     OR v_snapshot_request.request_kind <> 'snapshot-seal' OR v_snapshot_request.signer_role <> 'sealer'
     OR v_verification_request.request_kind <> 'snapshot-verify'
     OR v_verification_request.signer_role <> 'verifier'
     OR v_runner_request.request_kind <> 'receipt-sign'
     OR v_runner_request.signer_role <> 'qualification-runner'
     OR v_approver_request.request_kind <> 'receipt-sign'
     OR v_approver_request.signer_role <> 'release-approver'
     OR v_snapshot_request.operation_id
        <> (v_authority.evidence_plan_document->'operations'->>'snapshotSeal')::uuid
     OR v_verification_request.operation_id <> v_snapshot_request.operation_id
     OR v_runner_request.operation_id
        <> (v_authority.evidence_plan_document->'operations'->>'receiptSign')::uuid
     OR v_approver_request.operation_id <> v_runner_request.operation_id THEN
    RAISE EXCEPTION 'Qualification Receipt v3 completion does not bind four exact Plan-reserved requests'
      USING ERRCODE = 'WQR04';
  END IF;

  IF ROW(
       v_snapshot_request.evidence_head_version,
       v_snapshot_request.evidence_last_event_id,
       v_snapshot_request.evidence_last_event_hash,
       v_snapshot_request.evidence_command_digest,
       v_snapshot_request.evidence_trust_digest,
       v_snapshot_request.evidence_closure_digest,
       v_snapshot_request.artifact_index_digest
     ) IS DISTINCT FROM ROW(
       v_head.version,
       v_head.last_event_id,
       v_event.event_hash,
       v_head.command_hash,
       v_head.trust_bindings_digest,
       v_event.event_document->'artifactIndex'->>'evidenceClosureDigest',
       v_event.event_document->'artifactIndex'->>'contentDigest'
     )
     OR ROW(
       v_verification_request.evidence_head_version,
       v_verification_request.evidence_last_event_id,
       v_verification_request.evidence_last_event_hash,
       v_verification_request.evidence_command_digest,
       v_verification_request.evidence_trust_digest,
       v_verification_request.evidence_closure_digest,
       v_verification_request.artifact_index_digest
     ) IS DISTINCT FROM ROW(
       v_head.version,
       v_head.last_event_id,
       v_event.event_hash,
       v_head.command_hash,
       v_head.trust_bindings_digest,
       v_event.event_document->'artifactIndex'->>'evidenceClosureDigest',
       v_event.event_document->'artifactIndex'->>'contentDigest'
     )
     OR ROW(
       v_runner_request.evidence_head_version,
       v_runner_request.evidence_last_event_id,
       v_runner_request.evidence_last_event_hash,
       v_runner_request.evidence_command_digest,
       v_runner_request.evidence_trust_digest,
       v_runner_request.evidence_closure_digest,
       v_runner_request.artifact_index_digest
     ) IS DISTINCT FROM ROW(
       v_head.version,
       v_head.last_event_id,
       v_event.event_hash,
       v_head.command_hash,
       v_head.trust_bindings_digest,
       v_event.event_document->'artifactIndex'->>'evidenceClosureDigest',
       v_event.event_document->'artifactIndex'->>'contentDigest'
     )
     OR ROW(
       v_approver_request.evidence_head_version,
       v_approver_request.evidence_last_event_id,
       v_approver_request.evidence_last_event_hash,
       v_approver_request.evidence_command_digest,
       v_approver_request.evidence_trust_digest,
       v_approver_request.evidence_closure_digest,
       v_approver_request.artifact_index_digest
     ) IS DISTINCT FROM ROW(
       v_head.version,
       v_head.last_event_id,
       v_event.event_hash,
       v_head.command_hash,
       v_head.trust_bindings_digest,
       v_event.event_document->'artifactIndex'->>'evidenceClosureDigest',
       v_event.event_document->'artifactIndex'->>'contentDigest'
     ) THEN
    RAISE EXCEPTION 'Qualification Receipt v3 completion current indexed Evidence drifted from a frozen request'
      USING ERRCODE = 'WQR02';
  END IF;

  IF v_runner_request.payload_hash <> p_payload_hash
     OR v_approver_request.payload_hash <> p_payload_hash
     OR v_runner_request.payload_bytes <> p_payload_bytes
     OR v_approver_request.payload_bytes <> p_payload_bytes
     OR v_runner_request.pae_hash <> p_pae_hash OR v_approver_request.pae_hash <> p_pae_hash
     OR v_runner_request.pae_bytes <> p_pae_bytes OR v_approver_request.pae_bytes <> p_pae_bytes THEN
    RAISE EXCEPTION 'Qualification Receipt v3 signing requests do not bind the exact completion payload'
      USING ERRCODE = 'WQR04';
  END IF;

  SELECT * INTO v_snapshot_observation FROM qualification_receipt_v3_observations
  WHERE request_hash = p_snapshot_request_hash AND record_hash = p_snapshot_observation_hash;
  SELECT * INTO v_verification_observation FROM qualification_receipt_v3_observations
  WHERE request_hash = p_verification_request_hash AND record_hash = p_verification_observation_hash;
  SELECT * INTO v_runner_observation FROM qualification_receipt_v3_observations
  WHERE request_hash = p_runner_request_hash AND record_hash = p_runner_observation_hash;
  SELECT * INTO v_approver_observation FROM qualification_receipt_v3_observations
  WHERE request_hash = p_approver_request_hash AND record_hash = p_approver_observation_hash;
  IF v_snapshot_observation.record_hash IS NULL OR v_verification_observation.record_hash IS NULL
     OR v_runner_observation.record_hash IS NULL OR v_approver_observation.record_hash IS NULL
     OR v_snapshot_observation.status <> 'committed'
     OR v_verification_observation.status <> 'committed'
     OR v_runner_observation.status <> 'committed' OR v_approver_observation.status <> 'committed'
     OR EXISTS (
       SELECT 1 FROM qualification_receipt_v3_observations AS later
       WHERE (later.request_hash = p_snapshot_request_hash
              AND later.sequence > v_snapshot_observation.sequence)
          OR (later.request_hash = p_verification_request_hash
              AND later.sequence > v_verification_observation.sequence)
          OR (later.request_hash = p_runner_request_hash
              AND later.sequence > v_runner_observation.sequence)
          OR (later.request_hash = p_approver_request_hash
              AND later.sequence > v_approver_observation.sequence)
     ) THEN
    RAISE EXCEPTION 'Qualification Receipt v3 completion requires four latest committed terminal observations'
      USING ERRCODE = 'WQR04';
  END IF;

  IF v_snapshot_observation.result_document <> v_receipt->'snapshot'
     OR v_verification_observation.result_document <> v_receipt->'snapshotVerification'
     OR v_receipt->'evidencePlan' <> v_authority.evidence_plan_document
     OR v_receipt->'target' <> v_authority.target_document
     OR v_receipt->'trust' <> v_authority.trust_document
     OR v_receipt->'planAuthority'->>'authorityId' <> v_authority.authority_id::text
     OR v_receipt->'planAuthority'->>'authorityHash' <> v_authority.envelope_hash
     OR v_receipt->'planAuthority'->>'evidencePlanHash' <> v_authority.evidence_plan_hash
     OR v_receipt->'planAuthority'->>'inputHash' <> v_authority.input_hash
     OR v_receipt->'planAuthority'->>'projectionHash' <> v_authority.projection_hash
     OR v_receipt->'planAuthority'->>'targetHash' <> v_authority.target_hash
     OR v_receipt->'planAuthority'->>'trustHash' <> v_authority.trust_hash
     OR v_receipt->'evidence'->>'closureDigest' <> v_runner_request.evidence_closure_digest
     OR v_receipt->'artifactIndex'->>'contentDigest' <> v_runner_request.artifact_index_digest
     OR v_receipt->'snapshot'->>'snapshotId' <> v_runner_request.snapshot_id
     OR v_receipt->'snapshot'->>'snapshotDigest' <> v_runner_request.snapshot_digest
     OR v_receipt->>'receiptId' <> v_runner_request.receipt_id
     OR v_receipt->>'operationId' <> v_runner_request.operation_id::text
     OR p_payload_document->'subject'->0->>'name' <> v_runner_request.snapshot_id
     OR p_payload_document->'subject'->0->'digest'->>'sha256'
        <> replace(v_runner_request.snapshot_digest, 'sha256:', '') THEN
    RAISE EXCEPTION 'Qualification Receipt v3 signed payload drifted from Plan, snapshot, or Evidence'
      USING ERRCODE = 'WQR03';
  END IF;

  v_runner_signature := NULL;
  v_approver_signature := NULL;
  SELECT signature INTO v_runner_signature
  FROM jsonb_array_elements(p_envelope_document->'signatures') AS signature
  WHERE signature->>'keyid' = v_runner_request.signer_key_id;
  SELECT signature INTO v_approver_signature
  FROM jsonb_array_elements(p_envelope_document->'signatures') AS signature
  WHERE signature->>'keyid' = v_approver_request.signer_key_id;
  IF v_runner_signature IS NULL OR v_approver_signature IS NULL
     OR v_runner_signature - ARRAY['keyid','sig'] <> '{}'::jsonb
     OR v_approver_signature - ARRAY['keyid','sig'] <> '{}'::jsonb
     OR NOT (v_runner_signature ?& ARRAY['keyid','sig'])
     OR NOT (v_approver_signature ?& ARRAY['keyid','sig'])
     OR (v_runner_signature->>'sig') !~ '^[A-Za-z0-9+/]+={0,2}$'
     OR (v_approver_signature->>'sig') !~ '^[A-Za-z0-9+/]+={0,2}$'
     OR replace(encode(decode(v_runner_signature->>'sig', 'base64'), 'base64'), chr(10), '')
        <> v_runner_signature->>'sig'
     OR replace(encode(decode(v_approver_signature->>'sig', 'base64'), 'base64'), chr(10), '')
        <> v_approver_signature->>'sig'
     OR decode(v_runner_signature->>'sig', 'base64') <> v_runner_observation.signature_bytes
     OR decode(v_approver_signature->>'sig', 'base64') <> v_approver_observation.signature_bytes
     OR qualification_receipt_v3_sha256(decode(v_runner_signature->>'sig', 'base64'))
        <> v_runner_observation.signature_hash
     OR qualification_receipt_v3_sha256(decode(v_approver_signature->>'sig', 'base64'))
        <> v_approver_observation.signature_hash
     OR v_runner_request.signer_key_id COLLATE "C" >= v_approver_request.signer_key_id COLLATE "C"
        AND (p_envelope_document->'signatures'->0->>'keyid')
          <> v_approver_request.signer_key_id
     OR v_runner_request.signer_key_id COLLATE "C" < v_approver_request.signer_key_id COLLATE "C"
        AND (p_envelope_document->'signatures'->0->>'keyid')
          <> v_runner_request.signer_key_id THEN
    RAISE EXCEPTION 'Qualification Receipt v3 DSSE envelope does not contain exact committed signer bytes'
      USING ERRCODE = 'WQR03';
  END IF;

  v_target := v_authority.target_document->'promotionTarget';
  SELECT * INTO v_existing FROM qualification_receipt_v3_receipts
  WHERE plan_authority_id = p_plan_authority_id OR receipt_id = v_runner_request.receipt_id;
  IF FOUND THEN
    IF v_existing.plan_authority_id <> p_plan_authority_id
       OR v_existing.snapshot_request_hash <> p_snapshot_request_hash
       OR v_existing.snapshot_observation_hash <> p_snapshot_observation_hash
       OR v_existing.verification_request_hash <> p_verification_request_hash
       OR v_existing.verification_observation_hash <> p_verification_observation_hash
       OR v_existing.runner_request_hash <> p_runner_request_hash
       OR v_existing.runner_observation_hash <> p_runner_observation_hash
       OR v_existing.approver_request_hash <> p_approver_request_hash
       OR v_existing.approver_observation_hash <> p_approver_observation_hash
       OR v_existing.payload_hash <> p_payload_hash OR v_existing.payload_bytes <> p_payload_bytes
       OR v_existing.pae_hash <> p_pae_hash OR v_existing.pae_bytes <> p_pae_bytes
       OR v_existing.envelope_hash <> p_envelope_hash OR v_existing.envelope_bytes <> p_envelope_bytes
       OR v_existing.envelope_document <> p_envelope_document THEN
      RAISE EXCEPTION 'Qualification Receipt v3 completion is bound to different exact bytes'
        USING ERRCODE = 'WQR02';
    END IF;
    receipt_record := v_existing;
    idempotent := true;
    RETURN NEXT;
    RETURN;
  END IF;

  v_completed_at := date_trunc('milliseconds', clock_timestamp());
  IF v_completed_at <= greatest(
       v_snapshot_observation.recorded_at,
       v_verification_observation.recorded_at,
       v_runner_observation.recorded_at,
       v_approver_observation.recorded_at
     ) THEN
    RAISE EXCEPTION 'Qualification Receipt v3 Store clock did not advance beyond all source observations'
      USING ERRCODE = 'WQR02';
  END IF;
  v_completion_bytes := convert_to(
    '{"completedAt":' || to_jsonb(to_char(v_completed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))::text ||
    ',"envelopeHash":' || to_jsonb(p_envelope_hash)::text ||
    ',"evidenceClosureDigest":' || to_jsonb(v_runner_request.evidence_closure_digest)::text ||
    ',"observationHashes":{"approverSign":' || to_jsonb(p_approver_observation_hash)::text ||
      ',"runnerSign":' || to_jsonb(p_runner_observation_hash)::text ||
      ',"snapshotSeal":' || to_jsonb(p_snapshot_observation_hash)::text ||
      ',"snapshotVerify":' || to_jsonb(p_verification_observation_hash)::text || '}' ||
    ',"operations":{"receiptSign":' || to_jsonb(v_runner_request.operation_id::text)::text ||
      ',"snapshot":' || to_jsonb(v_snapshot_request.operation_id::text)::text || '}' ||
    ',"paeDigest":' || to_jsonb(p_pae_hash)::text ||
    ',"payloadDigest":' || to_jsonb(p_payload_hash)::text ||
    ',"planAuthorityHash":' || to_jsonb(v_authority.envelope_hash)::text ||
    ',"planAuthorityId":' || to_jsonb(v_authority.authority_id::text)::text ||
    ',"receiptId":' || to_jsonb(v_runner_request.receipt_id)::text ||
    ',"requestHashes":{"approverSign":' || to_jsonb(p_approver_request_hash)::text ||
      ',"runnerSign":' || to_jsonb(p_runner_request_hash)::text ||
      ',"snapshotSeal":' || to_jsonb(p_snapshot_request_hash)::text ||
      ',"snapshotVerify":' || to_jsonb(p_verification_request_hash)::text || '}' ||
    ',"schemaVersion":"worksflow-qualification-receipt-control-completion/v1"' ||
    ',"snapshotDigest":' || to_jsonb(v_runner_request.snapshot_digest)::text ||
    ',"snapshotId":' || to_jsonb(v_runner_request.snapshot_id)::text || '}',
    'UTF8'
  );
  v_completion_hash := qualification_receipt_v3_sha256(v_completion_bytes);
  v_completion_document := convert_from(v_completion_bytes, 'UTF8')::jsonb;

  INSERT INTO qualification_receipt_v3_receipts (
    receipt_id, plan_authority_id, orchestration_id, snapshot_operation_id,
    receipt_sign_operation_id, snapshot_request_hash, snapshot_observation_hash,
    verification_request_hash, verification_observation_hash, runner_request_hash,
    runner_observation_hash, approver_request_hash, approver_observation_hash,
    plan_authority_hash, evidence_closure_digest, artifact_index_digest,
    snapshot_id, snapshot_digest, runner_identity, runner_key_id,
    runner_signature_hash, runner_signature_bytes, approver_identity, approver_key_id,
    approver_signature_hash, approver_signature_bytes, project_id, workflow_run_id,
    node_key, target_revision_id, target_revision_content_hash, subject, stage_gate,
    payload_hash, payload_bytes, payload_document, pae_hash, pae_bytes,
    envelope_hash, envelope_bytes, envelope_document, completion_hash,
    completion_bytes, completion_document, completed_at
  ) VALUES (
    v_runner_request.receipt_id, p_plan_authority_id, v_authority.orchestration_id,
    v_snapshot_request.operation_id, v_runner_request.operation_id,
    p_snapshot_request_hash, p_snapshot_observation_hash,
    p_verification_request_hash, p_verification_observation_hash,
    p_runner_request_hash, p_runner_observation_hash,
    p_approver_request_hash, p_approver_observation_hash,
    v_authority.envelope_hash, v_runner_request.evidence_closure_digest,
    v_runner_request.artifact_index_digest, v_runner_request.snapshot_id,
    v_runner_request.snapshot_digest, v_runner_request.signer_identity,
    v_runner_request.signer_key_id, v_runner_observation.signature_hash,
    v_runner_observation.signature_bytes, v_approver_request.signer_identity,
    v_approver_request.signer_key_id, v_approver_observation.signature_hash,
    v_approver_observation.signature_bytes, (v_target->>'projectId')::uuid,
    (v_target->>'workflowRunId')::uuid, v_target->>'nodeKey',
    (v_target->'targetRevision'->>'id')::uuid,
    v_target->'targetRevision'->>'contentHash', v_target->>'subject', v_target->>'stageGate',
    p_payload_hash, p_payload_bytes, p_payload_document, p_pae_hash, p_pae_bytes,
    p_envelope_hash, p_envelope_bytes, p_envelope_document, v_completion_hash,
    v_completion_bytes, v_completion_document, v_completed_at
  ) RETURNING * INTO v_existing;
  receipt_record := v_existing;
  idempotent := false;
  RETURN NEXT;
  RETURN;
EXCEPTION WHEN unique_violation OR foreign_key_violation THEN
  RAISE EXCEPTION 'Qualification Receipt v3 immutable completion identity conflicts'
    USING ERRCODE = 'WQR02';
END;
$function$;

CREATE FUNCTION append_qualification_receipt_v3_observation(
  p_request_hash text,
  p_authentication_payload_hash text,
  p_authentication_payload_bytes bytea,
  p_authentication_payload_document jsonb,
  p_authentication_envelope_hash text,
  p_authentication_envelope_bytes bytea,
  p_authentication_envelope_document jsonb,
  p_result_hash text,
  p_result_bytes bytea,
  p_result_document jsonb,
  p_signature_hash text,
  p_signature_bytes bytea,
  p_claim_hash text,
  p_claim_bytes bytea,
  p_claim_document jsonb,
  p_acknowledgement_hash text,
  p_acknowledgement_bytes bytea,
  p_acknowledgement_document jsonb
) RETURNS TABLE (
  observation_record qualification_receipt_v3_observations,
  idempotent boolean
)
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_request qualification_receipt_v3_requests%ROWTYPE;
  v_existing qualification_receipt_v3_observations%ROWTYPE;
  v_previous qualification_receipt_v3_observations%ROWTYPE;
  v_authority qualification_plan_authorities%ROWTYPE;
  v_head qualification_evidence_heads%ROWTYPE;
  v_event qualification_evidence_events%ROWTYPE;
  v_generation bigint;
  v_sequence bigint;
  v_status text;
  v_observed_at timestamptz;
  v_result_at timestamptz;
  v_recorded_at timestamptz;
  v_record_hash text;
  v_record_bytes bytea;
  v_record_document jsonb;
BEGIN
  IF p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_authentication_payload_hash IS NULL
     OR p_authentication_payload_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_authentication_payload_bytes IS NULL
     OR octet_length(p_authentication_payload_bytes) NOT BETWEEN 1 AND 4194304
     OR get_byte(p_authentication_payload_bytes, 0) <> 123
     OR get_byte(p_authentication_payload_bytes, octet_length(p_authentication_payload_bytes) - 1) <> 125
     OR p_authentication_payload_document IS NULL
     OR jsonb_typeof(p_authentication_payload_document) <> 'object'
     OR qualification_receipt_v3_sha256(p_authentication_payload_bytes)
        <> p_authentication_payload_hash
     OR convert_from(p_authentication_payload_bytes, 'UTF8')::jsonb
        <> p_authentication_payload_document
     OR NOT (p_authentication_payload_document ?& ARRAY[
       'acknowledgementTokenHash','authenticationKeyId','generation','kind','observedAt',
       'operationId','operationalAuthorityId','planAuthorityId','requestHash','resultHash',
       'role','schemaVersion','sequence','signatureHash','status','claimTokenHash'
     ])
     OR p_authentication_payload_document - ARRAY[
       'acknowledgementTokenHash','authenticationKeyId','generation','kind','observedAt',
       'operationId','operationalAuthorityId','planAuthorityId','requestHash','resultHash',
       'role','schemaVersion','sequence','signatureHash','status','claimTokenHash'
     ] <> '{}'::jsonb
     OR p_authentication_payload_document->>'schemaVersion'
        <> 'worksflow-qualification-receipt-control-observation-payload/v1'
     OR p_authentication_payload_document->>'requestHash' <> p_request_hash
     OR jsonb_typeof(p_authentication_payload_document->'generation') <> 'number'
     OR jsonb_typeof(p_authentication_payload_document->'sequence') <> 'number'
     OR p_authentication_payload_document->>'generation' !~ '^[1-9][0-9]{0,18}$'
     OR p_authentication_payload_document->>'sequence' !~ '^[1-9][0-9]{0,18}$' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 authenticated observation envelope is invalid'
      USING ERRCODE = 'WQR03';
  END IF;
  IF p_authentication_envelope_hash IS NULL
     OR p_authentication_envelope_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_authentication_envelope_bytes IS NULL
     OR octet_length(p_authentication_envelope_bytes) NOT BETWEEN 1 AND 4194304
     OR get_byte(p_authentication_envelope_bytes, 0) <> 123
     OR get_byte(p_authentication_envelope_bytes, octet_length(p_authentication_envelope_bytes) - 1) <> 125
     OR p_authentication_envelope_document IS NULL
     OR jsonb_typeof(p_authentication_envelope_document) <> 'object'
     OR qualification_receipt_v3_sha256(p_authentication_envelope_bytes)
        <> p_authentication_envelope_hash
     OR convert_from(p_authentication_envelope_bytes, 'UTF8')::jsonb
        <> p_authentication_envelope_document
     OR NOT (p_authentication_envelope_document ?& ARRAY[
       'algorithm','keyId','operationalAuthorityId','payload','payloadType','schemaVersion','signature'
     ])
     OR p_authentication_envelope_document - ARRAY[
       'algorithm','keyId','operationalAuthorityId','payload','payloadType','schemaVersion','signature'
     ] <> '{}'::jsonb
     OR p_authentication_envelope_document->>'schemaVersion'
        <> 'worksflow-qualification-receipt-control-observation-proof/v1'
     OR p_authentication_envelope_document->>'algorithm' <> 'ed25519'
     OR p_authentication_envelope_document->>'keyId'
        <> p_authentication_payload_document->>'authenticationKeyId'
     OR p_authentication_envelope_document->>'operationalAuthorityId'
        <> p_authentication_payload_document->>'operationalAuthorityId'
     OR p_authentication_envelope_document->>'payloadType'
        <> 'application/vnd.worksflow.qualification-receipt-control-observation+json'
     OR (p_authentication_envelope_document->>'payload') !~ '^[A-Za-z0-9+/]*={0,2}$'
     OR decode(p_authentication_envelope_document->>'payload', 'base64')
        <> p_authentication_payload_bytes
     OR replace(encode(decode(p_authentication_envelope_document->>'payload', 'base64'), 'base64'),
          chr(10), '') <> p_authentication_envelope_document->>'payload'
     OR (p_authentication_envelope_document->>'signature') !~ '^[A-Za-z0-9+/]+={0,2}$'
     OR octet_length(decode(p_authentication_envelope_document->>'signature', 'base64')) <> 64
     OR replace(encode(decode(p_authentication_envelope_document->>'signature', 'base64'), 'base64'),
          chr(10), '') <> p_authentication_envelope_document->>'signature' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 signed observation proof envelope is invalid'
      USING ERRCODE = 'WQR03';
  END IF;
  BEGIN
    v_generation := (p_authentication_payload_document->>'generation')::bigint;
    v_sequence := (p_authentication_payload_document->>'sequence')::bigint;
    v_observed_at := (p_authentication_payload_document->>'observedAt')::timestamptz;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Qualification Receipt v3 authenticated observation scalar is malformed'
      USING ERRCODE = 'WQR03';
  END;
  v_status := p_authentication_payload_document->>'status';
  IF v_generation NOT BETWEEN 1 AND 9007199254740991
     OR v_sequence NOT BETWEEN 1 AND 9007199254740991
     OR v_status NOT IN ('pending','not-invoked','committed','rejected')
     OR to_char(v_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
        <> p_authentication_payload_document->>'observedAt'
     OR p_authentication_payload_document->>'planAuthorityId'
        !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_authentication_payload_document->>'operationId'
        !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_authentication_payload_document->>'authenticationKeyId'
        !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
     OR octet_length(p_authentication_payload_document->>'authenticationKeyId') > 256
     OR octet_length(p_authentication_payload_document->>'operationalAuthorityId') NOT BETWEEN 1 AND 256
     OR p_authentication_payload_document->>'operationalAuthorityId'
        !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 authenticated observation scalar is non-canonical'
      USING ERRCODE = 'WQR03';
  END IF;

  IF v_status = 'committed' AND p_authentication_payload_document->>'kind'
     IN ('snapshot-seal','snapshot-verify') THEN
    IF p_result_hash IS NULL OR p_result_hash !~ '^sha256:[0-9a-f]{64}$'
       OR p_result_bytes IS NULL OR octet_length(p_result_bytes) NOT BETWEEN 1 AND 8388608
       OR get_byte(p_result_bytes, 0) <> 123
       OR get_byte(p_result_bytes, octet_length(p_result_bytes) - 1) <> 125
       OR p_result_document IS NULL OR jsonb_typeof(p_result_document) <> 'object'
       OR qualification_receipt_v3_sha256(p_result_bytes) <> p_result_hash
       OR convert_from(p_result_bytes, 'UTF8')::jsonb <> p_result_document
       OR p_authentication_payload_document->>'resultHash' <> p_result_hash
       OR p_authentication_payload_document->>'signatureHash' <> ''
       OR p_signature_hash IS NOT NULL OR p_signature_bytes IS NOT NULL
       OR p_claim_hash IS NOT NULL OR p_claim_bytes IS NOT NULL OR p_claim_document IS NOT NULL
       OR p_acknowledgement_hash IS NOT NULL OR p_acknowledgement_bytes IS NOT NULL
       OR p_acknowledgement_document IS NOT NULL THEN
      RAISE EXCEPTION 'Qualification Receipt v3 committed snapshot observation lacks exact result closure'
        USING ERRCODE = 'WQR03';
    END IF;
  ELSIF v_status = 'committed' AND p_authentication_payload_document->>'kind' = 'receipt-sign' THEN
    IF p_signature_hash IS NULL OR p_signature_hash !~ '^sha256:[0-9a-f]{64}$'
       OR p_signature_bytes IS NULL OR octet_length(p_signature_bytes) <> 64
       OR qualification_receipt_v3_sha256(p_signature_bytes) <> p_signature_hash
       OR p_authentication_payload_document->>'signatureHash' <> p_signature_hash
       OR p_authentication_payload_document->>'resultHash' <> ''
       OR p_result_hash IS NOT NULL OR p_result_bytes IS NOT NULL OR p_result_document IS NOT NULL
       OR p_claim_hash IS NOT NULL OR p_claim_bytes IS NOT NULL OR p_claim_document IS NOT NULL
       OR p_acknowledgement_hash IS NOT NULL OR p_acknowledgement_bytes IS NOT NULL
       OR p_acknowledgement_document IS NOT NULL THEN
      RAISE EXCEPTION 'Qualification Receipt v3 committed signer observation lacks exact raw signature closure'
        USING ERRCODE = 'WQR03';
    END IF;
  ELSIF v_status = 'not-invoked' THEN
    IF p_authentication_payload_document->>'resultHash' <> ''
       OR p_authentication_payload_document->>'signatureHash' <> ''
       OR p_result_hash IS NOT NULL OR p_result_bytes IS NOT NULL OR p_result_document IS NOT NULL
       OR p_signature_hash IS NOT NULL OR p_signature_bytes IS NOT NULL
       OR p_claim_hash IS NULL OR p_claim_hash !~ '^sha256:[0-9a-f]{64}$'
       OR p_claim_bytes IS NULL OR p_claim_document IS NULL
       OR qualification_receipt_v3_sha256(p_claim_bytes) <> p_claim_hash
       OR convert_from(p_claim_bytes, 'UTF8')::jsonb <> p_claim_document
       OR p_acknowledgement_hash IS NULL
       OR p_acknowledgement_hash !~ '^sha256:[0-9a-f]{64}$'
       OR p_acknowledgement_bytes IS NULL OR p_acknowledgement_document IS NULL
       OR qualification_receipt_v3_sha256(p_acknowledgement_bytes) <> p_acknowledgement_hash
       OR convert_from(p_acknowledgement_bytes, 'UTF8')::jsonb <> p_acknowledgement_document
       OR p_authentication_payload_document->>'claimTokenHash' <> p_claim_hash
       OR p_authentication_payload_document->>'acknowledgementTokenHash'
          <> p_acknowledgement_hash THEN
      RAISE EXCEPTION 'Qualification Receipt v3 not-invoked observation lacks authenticated claim and ACK closure'
        USING ERRCODE = 'WQR03';
    END IF;
  ELSE
    IF p_authentication_payload_document->>'resultHash' <> ''
       OR p_authentication_payload_document->>'signatureHash' <> ''
       OR p_authentication_payload_document->>'claimTokenHash' <> ''
       OR p_authentication_payload_document->>'acknowledgementTokenHash' <> ''
       OR p_result_hash IS NOT NULL OR p_result_bytes IS NOT NULL OR p_result_document IS NOT NULL
       OR p_signature_hash IS NOT NULL OR p_signature_bytes IS NOT NULL
       OR p_claim_hash IS NOT NULL OR p_claim_bytes IS NOT NULL OR p_claim_document IS NOT NULL
       OR p_acknowledgement_hash IS NOT NULL OR p_acknowledgement_bytes IS NOT NULL
       OR p_acknowledgement_document IS NOT NULL THEN
      RAISE EXCEPTION 'Qualification Receipt v3 pending/rejected observation carries terminal material'
        USING ERRCODE = 'WQR03';
    END IF;
  END IF;

  LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_requests IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_observations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_receipts IN SHARE ROW EXCLUSIVE MODE;

  SELECT * INTO v_request FROM qualification_receipt_v3_requests
  WHERE request_hash = p_request_hash;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Receipt v3 request was not durably started before observation'
      USING ERRCODE = 'WQR01';
  END IF;
  IF p_authentication_payload_document->>'planAuthorityId' <> v_request.plan_authority_id::text
     OR p_authentication_payload_document->>'operationId' <> v_request.operation_id::text
     OR p_authentication_payload_document->>'kind' <> v_request.request_kind
     OR p_authentication_payload_document->>'role' <> v_request.signer_role
     OR p_authentication_payload_document->>'operationalAuthorityId'
        <> v_request.operational_authority_id
     OR p_authentication_payload_document->>'authenticationKeyId'
        <> v_request.authentication_key_id THEN
    RAISE EXCEPTION 'Qualification Receipt v3 observation drifted from exact started request'
      USING ERRCODE = 'WQR03';
  END IF;
  SELECT * INTO v_authority FROM qualification_plan_authorities
  WHERE authority_id = v_request.plan_authority_id;
  SELECT * INTO v_head FROM qualification_evidence_heads
  WHERE orchestration_id = v_request.orchestration_id;
  SELECT * INTO v_event FROM qualification_evidence_events
  WHERE event_id = v_request.evidence_last_event_id;
  IF v_authority.authority_id IS NULL OR v_head.orchestration_id IS NULL OR v_event.event_id IS NULL
     OR qualification_receipt_v3_sha256(v_authority.envelope_bytes) <> v_request.plan_authority_hash
     OR v_head.phase <> 'artifact-indexed' OR v_head.version <> v_request.evidence_head_version
     OR v_head.last_event_id <> v_request.evidence_last_event_id
     OR v_head.command_hash <> v_request.evidence_command_digest
     OR v_head.trust_bindings_digest <> v_request.evidence_trust_digest
     OR qualification_receipt_v3_sha256(v_event.event_bytes) <> v_request.evidence_last_event_hash
     OR v_event.event_document->'artifactIndex'->>'contentDigest' <> v_request.artifact_index_digest
     OR v_event.event_document->'artifactIndex'->>'evidenceClosureDigest'
        <> v_request.evidence_closure_digest THEN
    RAISE EXCEPTION 'Qualification Receipt v3 request authority or indexed Evidence drifted before observation'
      USING ERRCODE = 'WQR02';
  END IF;

  SELECT * INTO v_existing FROM qualification_receipt_v3_observations
  WHERE request_hash = p_request_hash AND sequence = v_sequence;
  IF FOUND THEN
    IF v_existing.generation <> v_generation OR v_existing.status <> v_status
       OR v_existing.observed_at <> v_observed_at
       OR v_existing.operational_authority_id <> v_request.operational_authority_id
       OR v_existing.authentication_key_id <> v_request.authentication_key_id
       OR v_existing.authentication_payload_hash <> p_authentication_payload_hash
       OR v_existing.authentication_payload_bytes <> p_authentication_payload_bytes
       OR v_existing.authentication_payload_document <> p_authentication_payload_document
       OR v_existing.authentication_envelope_hash <> p_authentication_envelope_hash
       OR v_existing.authentication_envelope_bytes <> p_authentication_envelope_bytes
       OR v_existing.authentication_envelope_document <> p_authentication_envelope_document
       OR v_existing.result_hash IS DISTINCT FROM p_result_hash
       OR v_existing.result_bytes IS DISTINCT FROM p_result_bytes
       OR v_existing.result_document IS DISTINCT FROM p_result_document
       OR v_existing.signature_hash IS DISTINCT FROM p_signature_hash
       OR v_existing.signature_bytes IS DISTINCT FROM p_signature_bytes
       OR v_existing.claim_hash IS DISTINCT FROM p_claim_hash
       OR v_existing.claim_bytes IS DISTINCT FROM p_claim_bytes
       OR v_existing.claim_document IS DISTINCT FROM p_claim_document
       OR v_existing.acknowledgement_hash IS DISTINCT FROM p_acknowledgement_hash
       OR v_existing.acknowledgement_bytes IS DISTINCT FROM p_acknowledgement_bytes
       OR v_existing.acknowledgement_document IS DISTINCT FROM p_acknowledgement_document THEN
      RAISE EXCEPTION 'Qualification Receipt v3 observation sequence is bound to different exact bytes'
        USING ERRCODE = 'WQR02';
    END IF;
    observation_record := v_existing;
    idempotent := true;
    RETURN NEXT;
    RETURN;
  END IF;

  SELECT * INTO v_previous FROM qualification_receipt_v3_observations
  WHERE request_hash = p_request_hash ORDER BY sequence DESC LIMIT 1;
  IF v_status = 'pending' THEN
    IF NOT FOUND THEN
      IF v_sequence <> 1 OR v_generation <> 1 THEN
        RAISE EXCEPTION 'Qualification Receipt v3 initial pending claim must be sequence/generation one'
          USING ERRCODE = 'WQR02';
      END IF;
    ELSIF v_previous.status <> 'not-invoked'
       OR v_sequence <> v_previous.sequence + 1
       OR v_generation <> v_previous.generation + 1 THEN
      RAISE EXCEPTION 'Qualification Receipt v3 retry ownership requires exact not-invoked predecessor and next generation'
        USING ERRCODE = 'WQR02';
    END IF;
  ELSE
    IF NOT FOUND OR v_previous.status <> 'pending'
       OR v_sequence <> v_previous.sequence + 1
       OR v_generation <> v_previous.generation THEN
      RAISE EXCEPTION 'Qualification Receipt v3 terminal observation must resolve the exact pending generation'
        USING ERRCODE = 'WQR02';
    END IF;
  END IF;

  v_recorded_at := date_trunc('milliseconds', clock_timestamp());
  IF v_recorded_at < v_request.started_at
     OR (v_previous.record_hash IS NOT NULL AND v_recorded_at < v_previous.recorded_at) THEN
    RAISE EXCEPTION 'Qualification Receipt v3 Store clock regressed before observation append'
      USING ERRCODE = 'WQR02';
  END IF;

  IF v_status = 'not-invoked' THEN
    IF NOT (p_claim_document ?& ARRAY[
         'claimId','generation','kind','operationId','operationalAuthorityId',
         'pendingEnvelopeHash','planAuthorityId','requestHash','role','schemaVersion'
       ])
       OR p_claim_document - ARRAY[
         'claimId','generation','kind','operationId','operationalAuthorityId',
         'pendingEnvelopeHash','planAuthorityId','requestHash','role','schemaVersion'
       ] <> '{}'::jsonb
       OR p_claim_document->>'schemaVersion'
          <> 'worksflow-qualification-receipt-control-claim/v1'
       OR p_claim_document->>'claimId'
          !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR p_claim_document->>'planAuthorityId' <> v_request.plan_authority_id::text
       OR p_claim_document->>'operationId' <> v_request.operation_id::text
       OR p_claim_document->>'kind' <> v_request.request_kind
       OR p_claim_document->>'role' <> v_request.signer_role
       OR p_claim_document->>'operationalAuthorityId' <> v_request.operational_authority_id
       OR p_claim_document->>'requestHash' <> v_request.request_hash
       OR (p_claim_document->>'generation')::bigint <> v_generation
       OR p_claim_document->>'pendingEnvelopeHash' <> v_previous.authentication_envelope_hash
       OR NOT (p_acknowledgement_document ?& ARRAY[
         'acknowledgementId','claimTokenHash','requestHash','schemaVersion','status'
       ])
       OR p_acknowledgement_document - ARRAY[
         'acknowledgementId','claimTokenHash','requestHash','schemaVersion','status'
       ] <> '{}'::jsonb
       OR p_acknowledgement_document->>'schemaVersion'
          <> 'worksflow-qualification-receipt-control-acknowledgement/v1'
       OR p_acknowledgement_document->>'acknowledgementId'
          !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR p_acknowledgement_document->>'claimTokenHash' <> p_claim_hash
       OR p_acknowledgement_document->>'requestHash' <> v_request.request_hash
       OR p_acknowledgement_document->>'status' <> 'not-invoked'
       OR p_acknowledgement_document->>'acknowledgementId' = p_claim_document->>'claimId' THEN
      RAISE EXCEPTION 'Qualification Receipt v3 claim/ACK does not release exact pending generation'
        USING ERRCODE = 'WQR03';
    END IF;
  ELSIF v_status = 'committed' AND v_request.request_kind = 'snapshot-seal' THEN
    IF NOT (p_result_document ?& ARRAY[
         'artifactIndexDigest','authorityId','evidenceClosureDigest','mode','operationId',
         'requestDigest','schemaVersion','sealedAt','snapshotDigest','snapshotId','stage'
       ])
       OR p_result_document - ARRAY[
         'artifactIndexDigest','authorityId','evidenceClosureDigest','mode','operationId',
         'requestDigest','schemaVersion','sealedAt','snapshotDigest','snapshotId','stage'
       ] <> '{}'::jsonb
       OR p_result_document->>'schemaVersion' <> 'worksflow-qualification-pre-receipt-snapshot/v3'
       OR p_result_document->>'operationId' <> v_request.operation_id::text
       OR p_result_document->>'requestDigest' <> v_request.request_hash
       OR p_result_document->>'authorityId' <> v_request.operational_authority_id
       OR p_result_document->>'stage' <> 'committed'
       OR p_result_document->>'mode' <> 'immutable-filesystem'
       OR p_result_document->>'snapshotId' <> v_request.snapshot_id
       OR p_result_document->>'snapshotDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR p_result_document->>'evidenceClosureDigest' <> v_request.evidence_closure_digest
       OR p_result_document->>'artifactIndexDigest' <> v_request.artifact_index_digest THEN
      RAISE EXCEPTION 'Qualification Receipt v3 seal result drifted from request closure'
        USING ERRCODE = 'WQR03';
    END IF;
    BEGIN
      v_result_at := (p_result_document->>'sealedAt')::timestamptz;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'Qualification Receipt v3 seal result time is malformed'
        USING ERRCODE = 'WQR03';
    END;
    IF to_char(v_result_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
         <> p_result_document->>'sealedAt'
       OR v_result_at < v_request.started_at OR v_result_at > v_observed_at
       OR v_result_at > v_recorded_at THEN
      RAISE EXCEPTION 'Qualification Receipt v3 seal result time is outside the trusted closure'
        USING ERRCODE = 'WQR03';
    END IF;
  ELSIF v_status = 'committed' AND v_request.request_kind = 'snapshot-verify' THEN
    IF NOT (p_result_document ?& ARRAY[
         'artifactIndexDigest','authorityId','evidenceClosureDigest','result','schemaVersion',
         'snapshotDigest','snapshotId','verifiedAt'
       ])
       OR p_result_document - ARRAY[
         'artifactIndexDigest','authorityId','evidenceClosureDigest','result','schemaVersion',
         'snapshotDigest','snapshotId','verifiedAt'
       ] <> '{}'::jsonb
       OR p_result_document->>'schemaVersion'
         <> 'worksflow-qualification-snapshot-verification/v3'
       OR p_result_document->>'authorityId' <> v_request.operational_authority_id
       OR p_result_document->>'result' <> 'verified'
       OR p_result_document->>'snapshotId' <> v_request.snapshot_id
       OR p_result_document->>'snapshotDigest' <> v_request.snapshot_digest
       OR p_result_document->>'evidenceClosureDigest' <> v_request.evidence_closure_digest
       OR p_result_document->>'artifactIndexDigest' <> v_request.artifact_index_digest THEN
      RAISE EXCEPTION 'Qualification Receipt v3 verification result drifted from sealed snapshot'
        USING ERRCODE = 'WQR03';
    END IF;
    BEGIN
      v_result_at := (p_result_document->>'verifiedAt')::timestamptz;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'Qualification Receipt v3 verification result time is malformed'
        USING ERRCODE = 'WQR03';
    END;
    IF to_char(v_result_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
         <> p_result_document->>'verifiedAt'
       OR v_result_at < v_request.started_at OR v_result_at > v_observed_at
       OR v_result_at > v_recorded_at THEN
      RAISE EXCEPTION 'Qualification Receipt v3 verification result time is outside the trusted closure'
        USING ERRCODE = 'WQR03';
    END IF;
  END IF;
  -- This is byte-for-byte CanonicalJSON(controlObservationProjection): exact
  -- lexical key order, no whitespace, authority ObservedAt distinct from the
  -- Store-assigned RecordedAt, and no schema or operational-authority widening.
  v_record_bytes := convert_to(
    '{"acknowledgementTokenHash":' || to_jsonb(COALESCE(p_acknowledgement_hash, ''))::text ||
    ',"authenticationEnvelopeHash":' || to_jsonb(p_authentication_envelope_hash)::text ||
    ',"authenticationKeyId":' || to_jsonb(v_request.authentication_key_id)::text ||
    ',"authenticationPayloadHash":' || to_jsonb(p_authentication_payload_hash)::text ||
    ',"claimTokenHash":' || to_jsonb(COALESCE(p_claim_hash, ''))::text ||
    ',"generation":' || v_generation::text ||
    ',"observedAt":' || to_jsonb(p_authentication_payload_document->>'observedAt')::text ||
    ',"recordedAt":' || to_jsonb(to_char(v_recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))::text ||
    ',"requestHash":' || to_jsonb(p_request_hash)::text ||
    ',"resultHash":' || to_jsonb(COALESCE(p_result_hash, ''))::text ||
    ',"sequence":' || v_sequence::text ||
    ',"signatureHash":' || to_jsonb(COALESCE(p_signature_hash, ''))::text ||
    ',"status":' || to_jsonb(v_status)::text || '}',
    'UTF8'
  );
  v_record_hash := qualification_receipt_v3_sha256(v_record_bytes);
  v_record_document := convert_from(v_record_bytes, 'UTF8')::jsonb;

  INSERT INTO qualification_receipt_v3_observations (
    request_hash, sequence, generation, record_hash, observation_bytes, observation_document,
    status, observed_at, recorded_at, operational_authority_id, authentication_key_id,
    authentication_payload_hash, authentication_payload_bytes, authentication_payload_document,
    authentication_envelope_hash, authentication_envelope_bytes, authentication_envelope_document,
    result_hash, result_bytes, result_document, signature_hash, signature_bytes,
    claim_hash, claim_bytes, claim_document, acknowledgement_hash, acknowledgement_bytes,
    acknowledgement_document, claim_id, acknowledgement_id
  ) VALUES (
    p_request_hash, v_sequence, v_generation, v_record_hash, v_record_bytes, v_record_document,
    v_status, v_observed_at, v_recorded_at, v_request.operational_authority_id,
    v_request.authentication_key_id, p_authentication_payload_hash,
    p_authentication_payload_bytes, p_authentication_payload_document,
    p_authentication_envelope_hash, p_authentication_envelope_bytes,
    p_authentication_envelope_document,
    p_result_hash, p_result_bytes, p_result_document, p_signature_hash, p_signature_bytes,
    p_claim_hash, p_claim_bytes, p_claim_document, p_acknowledgement_hash,
    p_acknowledgement_bytes, p_acknowledgement_document,
    CASE WHEN p_claim_document IS NULL THEN NULL ELSE (p_claim_document->>'claimId')::uuid END,
    CASE WHEN p_acknowledgement_document IS NULL THEN NULL
      ELSE (p_acknowledgement_document->>'acknowledgementId')::uuid END
  ) RETURNING * INTO v_existing;
  observation_record := v_existing;
  idempotent := false;
  RETURN NEXT;
  RETURN;
EXCEPTION WHEN unique_violation OR foreign_key_violation THEN
  RAISE EXCEPTION 'Qualification Receipt v3 observation sequence or record hash conflicts'
    USING ERRCODE = 'WQR02';
END;
$function$;

CREATE TRIGGER qualification_receipt_v3_requests_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_receipt_v3_requests
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_receipt_v3_mutation();

CREATE TRIGGER qualification_receipt_v3_observations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_receipt_v3_observations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_receipt_v3_mutation();

CREATE TRIGGER qualification_receipt_v3_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_receipt_v3_receipts
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_receipt_v3_mutation();

CREATE FUNCTION start_qualification_receipt_v3_requests(
  p_primary_request_hash text,
  p_primary_request_bytes bytea,
  p_primary_request_document jsonb,
  p_secondary_request_hash text,
  p_secondary_request_bytes bytea,
  p_secondary_request_document jsonb,
  p_payload_hash text,
  p_payload_bytes bytea,
  p_pae_hash text,
  p_pae_bytes bytea
) RETURNS TABLE (
  request_record qualification_receipt_v3_requests,
  created boolean
)
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_plan_authorities%ROWTYPE;
  v_head qualification_evidence_heads%ROWTYPE;
  v_last_event qualification_evidence_events%ROWTYPE;
  v_existing qualification_receipt_v3_requests%ROWTYPE;
  v_prior_request qualification_receipt_v3_requests%ROWTYPE;
  v_prior_observation qualification_receipt_v3_observations%ROWTYPE;
  v_seal_request qualification_receipt_v3_requests%ROWTYPE;
  v_seal_observation qualification_receipt_v3_observations%ROWTYPE;
  v_hashes text[] := ARRAY[p_primary_request_hash, p_secondary_request_hash];
  v_bytes bytea[] := ARRAY[p_primary_request_bytes, p_secondary_request_bytes];
  v_documents jsonb[] := ARRAY[p_primary_request_document, p_secondary_request_document];
  v_document jsonb;
  v_payload_document jsonb;
  v_count integer;
  v_index integer;
  v_kind text;
  v_role text;
  v_plan_authority_id uuid;
  v_operation_id uuid;
  v_orchestration_id uuid;
  v_event_id uuid;
  v_head_version bigint;
  v_existing_count integer := 0;
  v_now timestamptz;
BEGIN
  IF p_primary_request_document IS NULL OR jsonb_typeof(p_primary_request_document) <> 'object' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 primary request is absent or malformed'
      USING ERRCODE = 'WQR03';
  END IF;
  v_kind := p_primary_request_document->>'kind';
  IF v_kind = 'receipt-sign' THEN
    IF p_secondary_request_hash IS NULL OR p_secondary_request_bytes IS NULL
       OR p_secondary_request_document IS NULL OR p_payload_hash IS NULL
       OR p_payload_bytes IS NULL OR p_pae_hash IS NULL OR p_pae_bytes IS NULL THEN
      RAISE EXCEPTION 'Qualification Receipt v3 receipt-sign must atomically freeze payload, PAE, runner, and approver'
        USING ERRCODE = 'WQR03';
    END IF;
    v_count := 2;
  ELSE
    IF p_secondary_request_hash IS NOT NULL OR p_secondary_request_bytes IS NOT NULL
       OR p_secondary_request_document IS NOT NULL OR p_payload_hash IS NOT NULL
       OR p_payload_bytes IS NOT NULL OR p_pae_hash IS NOT NULL OR p_pae_bytes IS NOT NULL THEN
      RAISE EXCEPTION 'Qualification Receipt v3 snapshot request has widened signer material'
        USING ERRCODE = 'WQR03';
    END IF;
    v_count := 1;
  END IF;

  IF v_count = 2 THEN
    IF p_payload_hash !~ '^sha256:[0-9a-f]{64}$'
       OR octet_length(p_payload_bytes) NOT BETWEEN 1 AND 8388608
       OR get_byte(p_payload_bytes, 0) <> 123
       OR get_byte(p_payload_bytes, octet_length(p_payload_bytes) - 1) <> 125
       OR qualification_receipt_v3_sha256(p_payload_bytes) <> p_payload_hash
       OR p_pae_hash !~ '^sha256:[0-9a-f]{64}$'
       OR octet_length(p_pae_bytes) NOT BETWEEN 1 AND 8388864
       OR qualification_receipt_v3_sha256(p_pae_bytes) <> p_pae_hash
       OR p_pae_bytes <> (
         convert_to(
           'DSSEv1 28 application/vnd.in-toto+json ' || octet_length(p_payload_bytes)::text || ' ',
           'UTF8'
         ) || p_payload_bytes
       ) OR p_payload_hash = p_pae_hash THEN
      RAISE EXCEPTION 'Qualification Receipt v3 exact payload or DSSE PAE is invalid'
        USING ERRCODE = 'WQR03';
    END IF;
    BEGIN
      v_payload_document := convert_from(p_payload_bytes, 'UTF8')::jsonb;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'Qualification Receipt v3 payload is not UTF-8 JSON'
        USING ERRCODE = 'WQR03';
    END;
    IF NOT (v_payload_document ?& ARRAY['_type','predicate','predicateType','subject'])
       OR v_payload_document - ARRAY['_type','predicate','predicateType','subject'] <> '{}'::jsonb
       OR v_payload_document->>'_type' <> 'https://in-toto.io/Statement/v1'
       OR v_payload_document->>'predicateType'
          <> 'https://worksflow.dev/attestations/qualification-receipt/v3'
       OR jsonb_typeof(v_payload_document->'predicate') <> 'object'
       OR v_payload_document->'predicate'->>'schemaVersion' <> 'worksflow-qualification-receipt/v3'
       OR jsonb_typeof(v_payload_document->'subject') <> 'array'
       OR jsonb_array_length(v_payload_document->'subject') <> 1 THEN
      RAISE EXCEPTION 'Qualification Receipt v3 payload is not the closed v3 in-toto statement'
        USING ERRCODE = 'WQR03';
    END IF;
  END IF;

  FOR v_index IN 1..v_count LOOP
    v_document := v_documents[v_index];
    IF v_hashes[v_index] IS NULL OR v_hashes[v_index] !~ '^sha256:[0-9a-f]{64}$'
       OR v_bytes[v_index] IS NULL OR octet_length(v_bytes[v_index]) NOT BETWEEN 1 AND 16777216
       OR get_byte(v_bytes[v_index], 0) <> 123
       OR get_byte(v_bytes[v_index], octet_length(v_bytes[v_index]) - 1) <> 125
       OR v_document IS NULL OR jsonb_typeof(v_document) <> 'object'
       OR qualification_receipt_v3_sha256(v_bytes[v_index]) <> v_hashes[v_index]
       OR convert_from(v_bytes[v_index], 'UTF8')::jsonb <> v_document
       OR NOT (v_document ?& ARRAY[
         'artifactIndexDigest','authenticationKeyId','evidenceClosureDigest','evidenceCommandDigest',
         'evidenceHeadVersion','evidenceLastEventHash','evidenceLastEventId','evidencePlanHash',
         'evidenceTrustDigest','inputHash','kind','operationId','orchestrationId',
         'operationalAuthorityId','paeDigest','payloadDigest','planAuthorityHash','planAuthorityId',
         'projectionHash','receiptId','role','schemaVersion','signerIdentity','signerKeyId',
         'snapshotDigest','snapshotId','targetHash','trustBindingsDigest','trustHash','trustPolicyDigest'
       ])
       OR v_document - ARRAY[
         'artifactIndexDigest','authenticationKeyId','evidenceClosureDigest','evidenceCommandDigest',
         'evidenceHeadVersion','evidenceLastEventHash','evidenceLastEventId','evidencePlanHash',
         'evidenceTrustDigest','inputHash','kind','operationId','orchestrationId',
         'operationalAuthorityId','paeDigest','payloadDigest','planAuthorityHash','planAuthorityId',
         'projectionHash','receiptId','role','schemaVersion','signerIdentity','signerKeyId',
         'snapshotDigest','snapshotId','targetHash','trustBindingsDigest','trustHash','trustPolicyDigest'
       ] <> '{}'::jsonb
       OR v_document->>'schemaVersion' <> 'worksflow-qualification-receipt-control-request/v1'
       OR jsonb_typeof(v_document->'evidenceHeadVersion') <> 'number'
       OR v_document->>'evidenceHeadVersion' !~ '^[1-9][0-9]{0,18}$' THEN
      RAISE EXCEPTION 'Qualification Receipt v3 request bytes, hash, or closed root is invalid'
        USING ERRCODE = 'WQR03';
    END IF;
    BEGIN
      v_plan_authority_id := (v_document->>'planAuthorityId')::uuid;
      v_operation_id := (v_document->>'operationId')::uuid;
      v_orchestration_id := (v_document->>'orchestrationId')::uuid;
      v_event_id := (v_document->>'evidenceLastEventId')::uuid;
      v_head_version := (v_document->>'evidenceHeadVersion')::bigint;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'Qualification Receipt v3 request identity or version is malformed'
        USING ERRCODE = 'WQR03';
    END;
    IF v_plan_authority_id::text <> v_document->>'planAuthorityId'
       OR v_operation_id::text <> v_document->>'operationId'
       OR v_orchestration_id::text <> v_document->>'orchestrationId'
       OR v_event_id::text <> v_document->>'evidenceLastEventId'
       OR v_document->>'planAuthorityId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR v_document->>'operationId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR v_document->>'orchestrationId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR v_document->>'evidenceLastEventId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR v_head_version NOT BETWEEN 1 AND 9007199254740991
       OR v_document->>'artifactIndexDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'evidenceClosureDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'evidenceCommandDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'evidenceLastEventHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'evidencePlanHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'evidenceTrustDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'inputHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'planAuthorityHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'projectionHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'targetHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'trustBindingsDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'trustHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'trustPolicyDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR v_document->>'authenticationKeyId'
          !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
       OR octet_length(v_document->>'authenticationKeyId') > 256
       OR octet_length(v_document->>'operationalAuthorityId') NOT BETWEEN 1 AND 256
       OR v_document->>'operationalAuthorityId'
          !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
       OR octet_length(v_document->>'snapshotId') > 256
       OR (v_document->>'snapshotId') !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$' THEN
      RAISE EXCEPTION 'Qualification Receipt v3 request scalar is non-canonical'
        USING ERRCODE = 'WQR03';
    END IF;
    v_role := v_document->>'role';
    IF (v_document->>'kind' = 'snapshot-seal' AND v_role <> 'sealer')
       OR (v_document->>'kind' = 'snapshot-verify' AND v_role <> 'verifier')
       OR (v_document->>'kind' = 'receipt-sign'
         AND v_role NOT IN ('qualification-runner','release-approver'))
       OR v_document->>'kind' NOT IN ('snapshot-seal','snapshot-verify','receipt-sign') THEN
      RAISE EXCEPTION 'Qualification Receipt v3 request kind and role are inconsistent'
        USING ERRCODE = 'WQR03';
    END IF;
    IF v_document->>'kind' = 'snapshot-seal' THEN
      IF v_document->>'signerIdentity' <> '' OR v_document->>'signerKeyId' <> ''
         OR v_document->>'snapshotDigest' <> '' OR v_document->>'receiptId' <> ''
         OR v_document->>'payloadDigest' <> '' OR v_document->>'paeDigest' <> '' THEN
        RAISE EXCEPTION 'Qualification Receipt v3 snapshot seal request is widened'
          USING ERRCODE = 'WQR03';
      END IF;
    ELSIF v_document->>'kind' = 'snapshot-verify' THEN
      IF v_document->>'signerIdentity' <> '' OR v_document->>'signerKeyId' <> ''
         OR v_document->>'snapshotDigest' !~ '^sha256:[0-9a-f]{64}$'
         OR v_document->>'receiptId' <> '' OR v_document->>'payloadDigest' <> ''
         OR v_document->>'paeDigest' <> '' THEN
        RAISE EXCEPTION 'Qualification Receipt v3 snapshot verification request is widened'
          USING ERRCODE = 'WQR03';
      END IF;
    ELSE
      IF octet_length(v_document->>'signerIdentity') NOT BETWEEN 1 AND 512
         OR v_document->>'signerIdentity'
            !~ '^(?:spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+(?:[._:-][a-z0-9]+)*)$'
         OR (v_document->>'signerKeyId')
            !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
         OR octet_length(v_document->>'signerKeyId') > 256
         OR v_document->>'authenticationKeyId' <> v_document->>'signerKeyId'
         OR octet_length(v_document->>'receiptId') > 256
         OR (v_document->>'receiptId') !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
         OR v_document->>'snapshotDigest' !~ '^sha256:[0-9a-f]{64}$'
         OR v_document->>'payloadDigest' <> p_payload_hash
         OR v_document->>'paeDigest' <> p_pae_hash
         OR (v_document->>'role' = 'qualification-runner' AND (
           v_payload_document->'predicate'->'signers'->'runner'->>'role' <> 'qualification-runner'
           OR v_payload_document->'predicate'->'signers'->'runner'->>'identity'
              <> v_document->>'signerIdentity'
           OR v_payload_document->'predicate'->'signers'->'runner'->>'keyId'
              <> v_document->>'signerKeyId'
         ))
         OR (v_document->>'role' = 'release-approver' AND (
           v_payload_document->'predicate'->'signers'->'approver'->>'role' <> 'release-approver'
           OR v_payload_document->'predicate'->'signers'->'approver'->>'identity'
              <> v_document->>'signerIdentity'
           OR v_payload_document->'predicate'->'signers'->'approver'->>'keyId'
              <> v_document->>'signerKeyId'
         )) THEN
        RAISE EXCEPTION 'Qualification Receipt v3 signing request lacks exact signer, payload, or PAE closure'
          USING ERRCODE = 'WQR03';
      END IF;
    END IF;
  END LOOP;

  IF v_count = 2 THEN
    IF p_secondary_request_document->>'kind' <> 'receipt-sign'
       OR p_primary_request_document->>'role' = p_secondary_request_document->>'role'
       OR ARRAY[p_primary_request_document->>'role', p_secondary_request_document->>'role']
          @> ARRAY['qualification-runner','release-approver'] IS NOT TRUE
       OR p_primary_request_document->>'signerIdentity' = p_secondary_request_document->>'signerIdentity'
       OR p_primary_request_document->>'signerKeyId' = p_secondary_request_document->>'signerKeyId'
       OR p_primary_request_hash = p_secondary_request_hash THEN
      RAISE EXCEPTION 'Qualification Receipt v3 signer requests are not independent'
        USING ERRCODE = 'WQR03';
    END IF;
    FOR v_index IN 1..26 LOOP
      IF (ARRAY[
        p_primary_request_document->>'artifactIndexDigest', p_primary_request_document->>'evidenceClosureDigest',
        p_primary_request_document->>'evidenceCommandDigest', p_primary_request_document->>'evidenceHeadVersion',
        p_primary_request_document->>'evidenceLastEventHash', p_primary_request_document->>'evidenceLastEventId',
        p_primary_request_document->>'evidencePlanHash', p_primary_request_document->>'evidenceTrustDigest',
        p_primary_request_document->>'inputHash', p_primary_request_document->>'kind',
        p_primary_request_document->>'operationId', p_primary_request_document->>'orchestrationId',
        p_primary_request_document->>'operationalAuthorityId', p_primary_request_document->>'paeDigest',
        p_primary_request_document->>'payloadDigest', p_primary_request_document->>'planAuthorityHash',
        p_primary_request_document->>'planAuthorityId', p_primary_request_document->>'projectionHash',
        p_primary_request_document->>'receiptId', p_primary_request_document->>'schemaVersion',
        p_primary_request_document->>'snapshotDigest', p_primary_request_document->>'snapshotId',
        p_primary_request_document->>'targetHash', p_primary_request_document->>'trustBindingsDigest',
        p_primary_request_document->>'trustHash', p_primary_request_document->>'trustPolicyDigest'
      ])[v_index] IS DISTINCT FROM (ARRAY[
        p_secondary_request_document->>'artifactIndexDigest', p_secondary_request_document->>'evidenceClosureDigest',
        p_secondary_request_document->>'evidenceCommandDigest', p_secondary_request_document->>'evidenceHeadVersion',
        p_secondary_request_document->>'evidenceLastEventHash', p_secondary_request_document->>'evidenceLastEventId',
        p_secondary_request_document->>'evidencePlanHash', p_secondary_request_document->>'evidenceTrustDigest',
        p_secondary_request_document->>'inputHash', p_secondary_request_document->>'kind',
        p_secondary_request_document->>'operationId', p_secondary_request_document->>'orchestrationId',
        p_secondary_request_document->>'operationalAuthorityId', p_secondary_request_document->>'paeDigest',
        p_secondary_request_document->>'payloadDigest', p_secondary_request_document->>'planAuthorityHash',
        p_secondary_request_document->>'planAuthorityId', p_secondary_request_document->>'projectionHash',
        p_secondary_request_document->>'receiptId', p_secondary_request_document->>'schemaVersion',
        p_secondary_request_document->>'snapshotDigest', p_secondary_request_document->>'snapshotId',
        p_secondary_request_document->>'targetHash', p_secondary_request_document->>'trustBindingsDigest',
        p_secondary_request_document->>'trustHash', p_secondary_request_document->>'trustPolicyDigest'
      ])[v_index] THEN
        RAISE EXCEPTION 'Qualification Receipt v3 signer requests bind different immutable inputs'
          USING ERRCODE = 'WQR03';
      END IF;
    END LOOP;
  END IF;

  LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_requests IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_observations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_receipt_v3_receipts IN SHARE ROW EXCLUSIVE MODE;

  v_plan_authority_id := (p_primary_request_document->>'planAuthorityId')::uuid;
  v_orchestration_id := (p_primary_request_document->>'orchestrationId')::uuid;
  SELECT * INTO v_authority FROM qualification_plan_authorities
  WHERE authority_id = v_plan_authority_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Receipt v3 Plan Authority does not exist'
      USING ERRCODE = 'WQR04';
  END IF;
  IF qualification_receipt_v3_sha256(v_authority.input_bytes) <> v_authority.input_hash
     OR convert_from(v_authority.input_bytes, 'UTF8')::jsonb <> v_authority.input_document
     OR qualification_receipt_v3_sha256(v_authority.projection_bytes) <> v_authority.projection_hash
     OR convert_from(v_authority.projection_bytes, 'UTF8')::jsonb <> v_authority.projection_document
     OR qualification_receipt_v3_sha256(v_authority.evidence_plan_bytes) <> v_authority.evidence_plan_hash
     OR convert_from(v_authority.evidence_plan_bytes, 'UTF8')::jsonb <> v_authority.evidence_plan_document
     OR qualification_receipt_v3_sha256(v_authority.trust_bytes) <> v_authority.trust_hash
     OR convert_from(v_authority.trust_bytes, 'UTF8')::jsonb <> v_authority.trust_document
     OR qualification_receipt_v3_sha256(v_authority.target_bytes) <> v_authority.target_hash
     OR convert_from(v_authority.target_bytes, 'UTF8')::jsonb <> v_authority.target_document
     OR qualification_receipt_v3_sha256(v_authority.envelope_bytes) <> v_authority.envelope_hash
     OR convert_from(v_authority.envelope_bytes, 'UTF8')::jsonb <> v_authority.envelope_document
     OR v_authority.orchestration_id <> v_orchestration_id THEN
    RAISE EXCEPTION 'Qualification Receipt v3 Plan Authority byte closure or identity drifted'
      USING ERRCODE = 'WQR02';
  END IF;
  SELECT * INTO v_head FROM qualification_evidence_heads
  WHERE orchestration_id = v_orchestration_id;
  IF NOT FOUND OR v_head.phase <> 'artifact-indexed'
     OR v_head.plan_document <> v_authority.evidence_plan_document
     OR v_head.command_hash <> v_authority.evidence_plan_hash
     OR v_head.trust_bindings_digest <> v_authority.trust_bindings_digest THEN
    RAISE EXCEPTION 'Qualification Receipt v3 requires exact Plan-linked artifact-indexed Evidence head'
      USING ERRCODE = 'WQR04';
  END IF;
  SELECT * INTO v_last_event FROM qualification_evidence_events
  WHERE event_id = v_head.last_event_id;
  IF NOT FOUND OR v_last_event.orchestration_id <> v_orchestration_id
     OR v_last_event.version <> v_head.version OR v_last_event.event_kind <> 'artifact-indexed'
     OR qualification_receipt_v3_sha256(v_last_event.event_bytes) <> v_last_event.event_hash
     OR convert_from(v_last_event.event_bytes, 'UTF8')::jsonb <> v_last_event.event_document
     OR v_last_event.event_document->'artifactIndex'->>'stage' <> 'committed' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 indexed Evidence event closure is invalid'
      USING ERRCODE = 'WQR02';
  END IF;

  FOR v_index IN 1..v_count LOOP
    v_document := v_documents[v_index];
    IF v_document->>'planAuthorityId' <> v_authority.authority_id::text
       OR v_document->>'planAuthorityHash' <> v_authority.envelope_hash
       OR v_document->>'inputHash' <> v_authority.input_hash
       OR v_document->>'projectionHash' <> v_authority.projection_hash
       OR v_document->>'evidencePlanHash' <> v_authority.evidence_plan_hash
       OR v_document->>'targetHash' <> v_authority.target_hash
       OR v_document->>'trustHash' <> v_authority.trust_hash
       OR v_document->>'trustBindingsDigest' <> v_authority.trust_bindings_digest
       OR v_document->>'trustPolicyDigest' <> v_authority.trust_policy_digest
       OR v_document->>'orchestrationId' <> v_authority.orchestration_id::text
       OR (v_document->>'evidenceHeadVersion')::bigint <> v_head.version
       OR v_document->>'evidenceLastEventId' <> v_head.last_event_id::text
       OR v_document->>'evidenceLastEventHash' <> v_last_event.event_hash
       OR v_document->>'evidenceCommandDigest' <> v_head.command_hash
       OR v_document->>'evidenceTrustDigest' <> v_head.trust_bindings_digest
       OR v_document->>'artifactIndexDigest' <> v_last_event.event_document->'artifactIndex'->>'contentDigest'
       OR v_document->>'evidenceClosureDigest'
          <> v_last_event.event_document->'artifactIndex'->>'evidenceClosureDigest'
       OR v_document->>'snapshotId' <> v_authority.evidence_plan_document->'outputs'->>'snapshotId' THEN
      RAISE EXCEPTION 'Qualification Receipt v3 request drifted from Plan or indexed Evidence bytes'
        USING ERRCODE = 'WQR03';
    END IF;
    IF v_document->>'kind' IN ('snapshot-seal','snapshot-verify') THEN
      IF v_document->>'operationId' <> v_authority.evidence_plan_document->'operations'->>'snapshotSeal' THEN
        RAISE EXCEPTION 'Qualification Receipt v3 snapshot operation is not Plan-reserved'
          USING ERRCODE = 'WQR03';
      END IF;
    ELSIF v_document->>'operationId' <> v_authority.evidence_plan_document->'operations'->>'receiptSign'
       OR v_document->>'receiptId' <> v_authority.evidence_plan_document->'outputs'->>'receiptId' THEN
      RAISE EXCEPTION 'Qualification Receipt v3 signing operation or Receipt ID is not Plan-reserved'
        USING ERRCODE = 'WQR03';
    END IF;
    IF (v_document->>'kind' = 'snapshot-seal'
        AND v_document->>'operationalAuthorityId'
          <> v_authority.trust_document->'trustBindings'->>'sealerAuthorityId')
       OR (v_document->>'kind' = 'snapshot-verify'
        AND v_document->>'operationalAuthorityId'
          <> v_authority.trust_document->'trustBindings'->>'verifierAuthorityId')
       OR (v_document->>'kind' = 'receipt-sign'
        AND v_document->>'operationalAuthorityId'
          <> v_authority.trust_document->'trustBindings'->>'receiptAuthorityId') THEN
      RAISE EXCEPTION 'Qualification Receipt v3 operational authority is outside frozen trust'
        USING ERRCODE = 'WQR03';
    END IF;
  END LOOP;

  IF v_kind = 'snapshot-verify' THEN
    SELECT * INTO v_prior_request FROM qualification_receipt_v3_requests
    WHERE plan_authority_id = v_authority.authority_id
      AND request_kind = 'snapshot-seal' AND signer_role = 'sealer';
    SELECT * INTO v_prior_observation FROM qualification_receipt_v3_observations
    WHERE request_hash = v_prior_request.request_hash ORDER BY sequence DESC LIMIT 1;
    IF v_prior_request.request_hash IS NULL OR v_prior_observation.status <> 'committed'
       OR v_prior_observation.result_document->>'schemaVersion'
          <> 'worksflow-qualification-pre-receipt-snapshot/v3'
       OR v_prior_observation.result_document->>'snapshotDigest'
          <> p_primary_request_document->>'snapshotDigest' THEN
      RAISE EXCEPTION 'Qualification Receipt v3 verification requires exact committed seal result'
        USING ERRCODE = 'WQR04';
    END IF;
  ELSIF v_kind = 'receipt-sign' THEN
    SELECT * INTO v_seal_request FROM qualification_receipt_v3_requests
    WHERE plan_authority_id = v_authority.authority_id
      AND request_kind = 'snapshot-seal' AND signer_role = 'sealer';
    SELECT * INTO v_seal_observation FROM qualification_receipt_v3_observations
    WHERE request_hash = v_seal_request.request_hash ORDER BY sequence DESC LIMIT 1;
    SELECT * INTO v_prior_request FROM qualification_receipt_v3_requests
    WHERE plan_authority_id = v_authority.authority_id
      AND request_kind = 'snapshot-verify' AND signer_role = 'verifier';
    SELECT * INTO v_prior_observation FROM qualification_receipt_v3_observations
    WHERE request_hash = v_prior_request.request_hash ORDER BY sequence DESC LIMIT 1;
    IF v_seal_request.request_hash IS NULL OR v_seal_observation.status <> 'committed'
       OR v_prior_request.request_hash IS NULL OR v_prior_observation.status <> 'committed'
       OR v_prior_observation.result_document->>'schemaVersion'
          <> 'worksflow-qualification-snapshot-verification/v3'
       OR v_prior_observation.result_document->>'snapshotDigest'
          <> p_primary_request_document->>'snapshotDigest'
       OR v_payload_document->'predicate'->'snapshot' <> v_seal_observation.result_document
       OR v_payload_document->'predicate'->'snapshotVerification'
          <> v_prior_observation.result_document
       OR v_payload_document->'predicate'->'planAuthority'->>'authorityId' <> v_authority.authority_id::text
       OR v_payload_document->'predicate'->'planAuthority'->>'authorityHash' <> v_authority.envelope_hash
       OR v_payload_document->'predicate'->'evidence'->>'closureDigest'
          <> p_primary_request_document->>'evidenceClosureDigest'
       OR v_payload_document->'predicate'->'snapshot'->>'snapshotDigest'
          <> p_primary_request_document->>'snapshotDigest'
       OR v_payload_document->'predicate'->>'receiptId' <> p_primary_request_document->>'receiptId' THEN
      RAISE EXCEPTION 'Qualification Receipt v3 signing requires exact verified snapshot payload closure'
        USING ERRCODE = 'WQR04';
    END IF;
  END IF;

  FOR v_index IN 1..v_count LOOP
    v_document := v_documents[v_index];
    SELECT * INTO v_existing FROM qualification_receipt_v3_requests
    WHERE request_hash = v_hashes[v_index];
    IF FOUND THEN
      v_existing_count := v_existing_count + 1;
      IF v_existing.request_bytes <> v_bytes[v_index]
         OR v_existing.request_document <> v_document
         OR v_existing.operation_id::text <> v_document->>'operationId'
         OR v_existing.plan_authority_id::text <> v_document->>'planAuthorityId'
         OR v_existing.orchestration_id::text <> v_document->>'orchestrationId'
         OR v_existing.request_kind <> v_document->>'kind'
         OR v_existing.signer_role <> v_document->>'role'
         OR v_existing.operational_authority_id <> v_document->>'operationalAuthorityId'
         OR v_existing.authentication_key_id <> v_document->>'authenticationKeyId'
         OR v_existing.signer_identity <> v_document->>'signerIdentity'
         OR v_existing.signer_key_id <> v_document->>'signerKeyId'
         OR v_existing.snapshot_digest <> v_document->>'snapshotDigest'
         OR v_existing.receipt_id <> v_document->>'receiptId'
         OR v_existing.payload_hash <> COALESCE(p_payload_hash, '')
         OR v_existing.payload_bytes IS DISTINCT FROM p_payload_bytes
         OR v_existing.pae_hash <> COALESCE(p_pae_hash, '')
         OR v_existing.pae_bytes IS DISTINCT FROM p_pae_bytes THEN
        RAISE EXCEPTION 'Qualification Receipt v3 request hash is bound to different exact bytes'
          USING ERRCODE = 'WQR02';
      END IF;
    END IF;
  END LOOP;
  IF v_existing_count <> 0 THEN
    IF v_existing_count <> v_count THEN
      RAISE EXCEPTION 'Qualification Receipt v3 atomic signing request set is incomplete'
        USING ERRCODE = 'WQR02';
    END IF;
    RETURN QUERY
    SELECT requests, false
    FROM qualification_receipt_v3_requests AS requests
    WHERE requests.request_hash = ANY (v_hashes[1:v_count])
    ORDER BY array_position(v_hashes, requests.request_hash);
    RETURN;
  END IF;

  v_now := date_trunc('milliseconds', clock_timestamp());
  IF (v_kind = 'snapshot-verify' AND v_now < v_prior_observation.recorded_at)
     OR (v_kind = 'receipt-sign' AND (
       v_now < v_seal_observation.recorded_at OR v_now < v_prior_observation.recorded_at
     )) THEN
    RAISE EXCEPTION 'Qualification Receipt v3 Store clock regressed behind prerequisite terminal observations'
      USING ERRCODE = 'WQR02';
  END IF;
  FOR v_index IN 1..v_count LOOP
    v_document := v_documents[v_index];
    INSERT INTO qualification_receipt_v3_requests (
      request_hash, request_bytes, request_document, plan_authority_id, orchestration_id,
      operation_id, request_kind, signer_role, operational_authority_id, authentication_key_id,
      signer_identity, signer_key_id, snapshot_id, snapshot_digest, receipt_id,
      plan_authority_hash, input_hash, projection_hash, evidence_plan_hash, target_hash,
      trust_hash, trust_bindings_digest, trust_policy_digest, evidence_head_version,
      evidence_last_event_id, evidence_last_event_hash, evidence_command_digest,
      evidence_trust_digest, evidence_closure_digest, artifact_index_digest,
      payload_hash, payload_bytes, pae_hash, pae_bytes, started_at
    ) VALUES (
      v_hashes[v_index], v_bytes[v_index], v_document,
      (v_document->>'planAuthorityId')::uuid, (v_document->>'orchestrationId')::uuid,
      (v_document->>'operationId')::uuid, v_document->>'kind', v_document->>'role',
      v_document->>'operationalAuthorityId', v_document->>'authenticationKeyId',
      v_document->>'signerIdentity', v_document->>'signerKeyId', v_document->>'snapshotId',
      v_document->>'snapshotDigest', v_document->>'receiptId', v_document->>'planAuthorityHash',
      v_document->>'inputHash', v_document->>'projectionHash', v_document->>'evidencePlanHash',
      v_document->>'targetHash', v_document->>'trustHash', v_document->>'trustBindingsDigest',
      v_document->>'trustPolicyDigest', (v_document->>'evidenceHeadVersion')::bigint,
      (v_document->>'evidenceLastEventId')::uuid, v_document->>'evidenceLastEventHash',
      v_document->>'evidenceCommandDigest', v_document->>'evidenceTrustDigest',
      v_document->>'evidenceClosureDigest', v_document->>'artifactIndexDigest',
      COALESCE(p_payload_hash, ''), p_payload_bytes, COALESCE(p_pae_hash, ''), p_pae_bytes, v_now
    );
  END LOOP;
  RETURN QUERY
  SELECT requests, true
  FROM qualification_receipt_v3_requests AS requests
  WHERE requests.request_hash = ANY (v_hashes[1:v_count])
  ORDER BY array_position(v_hashes, requests.request_hash);
  RETURN;
EXCEPTION WHEN unique_violation OR foreign_key_violation THEN
  RAISE EXCEPTION 'Qualification Receipt v3 request identity conflicts with another exact input'
    USING ERRCODE = 'WQR02';
END;
$function$;
DO $qualification_receipt_v3_security_posture$
DECLARE
  schema_name text := current_schema();
  role_name text;
BEGIN
  EXECUTE format(
    'ALTER FUNCTION %I.qualification_receipt_v3_sha256(bytea) SET search_path TO pg_catalog',
    schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.reject_qualification_receipt_v3_mutation() SET search_path TO pg_catalog',
    schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.start_qualification_receipt_v3_requests('
    'text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.append_qualification_receipt_v3_observation('
    'text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text,bytea,jsonb) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.complete_qualification_receipt_v3('
    'uuid,text,text,text,text,text,text,text,text,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.guard_qualification_evidence_v1_receipt_tail_history_only() '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.guard_qualification_promotion_v1_new_consumption_history_only() '
    'SET search_path TO pg_catalog', schema_name
  );

  EXECUTE format('REVOKE ALL ON TABLE %I.qualification_receipt_v3_requests FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON TABLE %I.qualification_receipt_v3_observations FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON TABLE %I.qualification_receipt_v3_receipts FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON FUNCTION %I.qualification_receipt_v3_sha256(bytea) FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON FUNCTION %I.reject_qualification_receipt_v3_mutation() FROM PUBLIC', schema_name);
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.start_qualification_receipt_v3_requests('
    'text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea) FROM PUBLIC', schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.append_qualification_receipt_v3_observation('
    'text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text,bytea,jsonb) '
    'FROM PUBLIC', schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.complete_qualification_receipt_v3('
    'uuid,text,text,text,text,text,text,text,text,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text) '
    'FROM PUBLIC', schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.guard_qualification_evidence_v1_receipt_tail_history_only() FROM PUBLIC',
    schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.guard_qualification_promotion_v1_new_consumption_history_only() FROM PUBLIC',
    schema_name
  );

  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_receipt_v3_requests OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_receipt_v3_observations OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_receipt_v3_receipts OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.qualification_receipt_v3_sha256(bytea) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.reject_qualification_receipt_v3_mutation() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format(
      'ALTER FUNCTION %I.start_qualification_receipt_v3_requests('
      'text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea) OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.append_qualification_receipt_v3_observation('
      'text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text,bytea,jsonb) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.complete_qualification_receipt_v3('
      'uuid,text,text,text,text,text,text,text,text,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.guard_qualification_evidence_v1_receipt_tail_history_only() '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.guard_qualification_promotion_v1_new_consumption_history_only() '
      'OWNER TO worksflow_migration_owner', schema_name
    );
  END IF;

  FOREACH role_name IN ARRAY ARRAY[
    'worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_repository_index_gc_operator','worksflow_golden_fault_operator',
    'worksflow_qualification_promotion_operator'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('REVOKE ALL ON TABLE %I.qualification_receipt_v3_requests FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON TABLE %I.qualification_receipt_v3_observations FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON TABLE %I.qualification_receipt_v3_receipts FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.qualification_receipt_v3_sha256(bytea) FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.reject_qualification_receipt_v3_mutation() FROM %I', schema_name, role_name);
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.start_qualification_receipt_v3_requests('
        'text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea) FROM %I', schema_name, role_name
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.append_qualification_receipt_v3_observation('
        'text,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text,bytea,jsonb) '
        'FROM %I', schema_name, role_name
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.complete_qualification_receipt_v3('
        'uuid,text,text,text,text,text,text,text,text,text,bytea,jsonb,text,bytea,text,bytea,jsonb,text) '
        'FROM %I', schema_name, role_name
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.guard_qualification_evidence_v1_receipt_tail_history_only() FROM %I',
        schema_name, role_name
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.guard_qualification_promotion_v1_new_consumption_history_only() FROM %I',
        schema_name, role_name
      );
    END IF;
  END LOOP;
END;
$qualification_receipt_v3_security_posture$;
