-- A distinct signed Genesis projection is added without weakening the v69
-- ordinary activation path. Existing rows are explicitly classified as
-- ordinary activations; only the owner-only Genesis routine may create the
-- nullable Genesis projection fields.
ALTER TABLE model_governance_activation_records
  ADD COLUMN authority_kind text NOT NULL DEFAULT 'activation',
  ADD COLUMN genesis_envelope_digest text,
  ADD COLUMN genesis_payload_digest text,
  ADD COLUMN initial_revocation_authority_id text,
  ADD COLUMN initial_revocation_authority_hash text,
  ADD COLUMN initial_revocation_authority_epoch bigint;

ALTER TABLE model_governance_revocation_anchor
  ADD COLUMN current_trust_policy_hash text,
  ADD COLUMN trust_policy_revocation_hash text,
  ADD COLUMN trust_policy_revocation_epoch bigint,
  ADD COLUMN trust_policy_observed_at timestamptz,
  ADD CONSTRAINT model_governance_trust_policy_anchor_closed CHECK (
    (
      current_trust_policy_hash IS NULL
      AND trust_policy_revocation_hash IS NULL
      AND trust_policy_revocation_epoch IS NULL
      AND trust_policy_observed_at IS NULL
    ) OR (
      current_trust_policy_hash ~ '^sha256:[0-9a-f]{64}$'
      AND trust_policy_revocation_hash ~ '^sha256:[0-9a-f]{64}$'
      AND trust_policy_revocation_epoch > 0
      AND trust_policy_observed_at = date_trunc('milliseconds', trust_policy_observed_at)
    )
  );

ALTER TABLE model_governance_activation_records
  ADD CONSTRAINT model_governance_activation_authority_kind_closed CHECK (
    (
      authority_kind = 'activation'
      AND genesis_envelope_digest IS NULL
      AND genesis_payload_digest IS NULL
      AND initial_revocation_authority_id IS NULL
      AND initial_revocation_authority_hash IS NULL
      AND initial_revocation_authority_epoch IS NULL
    ) OR (
      authority_kind = 'genesis'
      AND previous_generation = 0
      AND generation = 1
      AND genesis_envelope_digest ~ '^sha256:[0-9a-f]{64}$'
      AND genesis_payload_digest ~ '^sha256:[0-9a-f]{64}$'
      AND genesis_envelope_digest <> genesis_payload_digest
      AND activation_envelope_digest = genesis_envelope_digest
      AND activation_payload_digest = genesis_payload_digest
      AND initial_revocation_authority_id = 'model-governance-revocations'
      AND initial_revocation_authority_hash ~ '^sha256:[0-9a-f]{64}$'
      AND initial_revocation_authority_epoch > 0
    )
  );

CREATE FUNCTION observe_model_governance_trust_policy(
  p_policy_hash text,
  p_revocation_authority_hash text,
  p_revocation_epoch bigint
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $function$
DECLARE
  v_anchor model_governance_revocation_anchor%ROWTYPE;
  v_now timestamptz;
BEGIN
  IF p_policy_hash IS NULL OR p_policy_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_revocation_authority_hash IS NULL OR p_revocation_authority_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_revocation_epoch IS NULL OR p_revocation_epoch <= 0 THEN
    RAISE EXCEPTION 'Model Governance trust-policy observation is structurally invalid'
      USING ERRCODE = '40001';
  END IF;
  LOCK TABLE model_governance_revocation_anchor IN SHARE ROW EXCLUSIVE MODE;
  SELECT * INTO v_anchor
  FROM model_governance_revocation_anchor
  WHERE singleton
  FOR UPDATE;
  v_now := date_trunc('milliseconds', clock_timestamp());
  IF NOT FOUND
     OR v_anchor.epoch <> p_revocation_epoch
     OR v_anchor.authority_hash <> p_revocation_authority_hash
     OR v_now < v_anchor.authority_issued_at - interval '30 seconds'
     OR v_now >= v_anchor.authority_expires_at THEN
    RAISE EXCEPTION 'Model Governance trust policy is not bound to the exact current revocation authority'
      USING ERRCODE = '40001';
  END IF;
  IF v_anchor.trust_policy_revocation_epoch IS NOT NULL THEN
    IF p_revocation_epoch < v_anchor.trust_policy_revocation_epoch
       OR (p_revocation_epoch = v_anchor.trust_policy_revocation_epoch AND (
         p_policy_hash <> v_anchor.current_trust_policy_hash
         OR p_revocation_authority_hash <> v_anchor.trust_policy_revocation_hash
       )) THEN
      RAISE EXCEPTION 'Model Governance trust-policy rollback or same-epoch equivocation'
        USING ERRCODE = '40001';
    END IF;
    IF p_revocation_epoch = v_anchor.trust_policy_revocation_epoch THEN
      RETURN;
    END IF;
  END IF;
  UPDATE model_governance_revocation_anchor
  SET current_trust_policy_hash = p_policy_hash,
      trust_policy_revocation_hash = p_revocation_authority_hash,
      trust_policy_revocation_epoch = p_revocation_epoch,
      trust_policy_observed_at = v_now
  WHERE singleton;
END;
$function$;

CREATE FUNCTION enforce_model_governance_activation_authority_anchor()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path FROM CURRENT
AS $function$
DECLARE
  v_anchor model_governance_revocation_anchor%ROWTYPE;
  v_now timestamptz;
BEGIN
  SELECT * INTO v_anchor
  FROM model_governance_revocation_anchor
  WHERE singleton
  FOR SHARE;
  v_now := date_trunc('milliseconds', clock_timestamp());
  IF NOT FOUND
     OR v_anchor.current_trust_policy_hash <> NEW.trust_policy_hash
     OR v_anchor.trust_policy_revocation_hash <> v_anchor.authority_hash
     OR v_anchor.trust_policy_revocation_epoch <> v_anchor.epoch
     OR v_now < v_anchor.authority_issued_at - interval '30 seconds'
     OR v_now >= v_anchor.authority_expires_at
     OR NEW.activated_at < v_now - interval '30 seconds'
     OR NEW.activated_at > v_now + interval '30 seconds' THEN
    RAISE EXCEPTION 'Model Governance activation trust/revocation authority drifted at atomic insert'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER model_governance_activation_authority_anchor
BEFORE INSERT ON model_governance_activation_records
FOR EACH ROW EXECUTE FUNCTION enforce_model_governance_activation_authority_anchor();

CREATE FUNCTION append_model_governance_genesis(
  p_operation_id uuid,
  p_request_hash text,
  p_workload text,
  p_profile_id uuid,
  p_profile_content_hash text,
  p_receipt_digest text,
  p_receipt_payload_digest text,
  p_genesis_envelope_digest text,
  p_genesis_payload_digest text,
  p_previous_generation bigint,
  p_generation bigint,
  p_previous_fence text,
  p_fence text,
  p_corpus_content_hash text,
  p_provider_route_authority_hash text,
  p_runner_immutable_digest text,
  p_source_tree_digest text,
  p_trust_policy_hash text,
  p_current_trust_policy_hash text,
  p_initial_revocation_authority_id text,
  p_initial_revocation_authority_hash text,
  p_initial_revocation_authority_epoch bigint,
  p_current_revocation_authority_hash text,
  p_current_revocation_authority_epoch bigint,
  p_activated_at timestamptz,
  p_expected_empty_fence text
)
RETURNS SETOF model_governance_activation_records
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $function$
DECLARE
  v_existing model_governance_activation_records%ROWTYPE;
  v_revocation model_governance_revocation_anchor%ROWTYPE;
  v_now timestamptz;
  v_request_document text;
  v_request_hash text;
  v_crypto_schema text;
BEGIN
  IF p_operation_id IS NULL
     OR p_operation_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_workload IS NULL OR octet_length(p_workload) NOT BETWEEN 1 AND 128
     OR p_workload !~ '^[a-z0-9]+(-[a-z0-9]+)*$'
     OR p_profile_id IS NULL
     OR p_profile_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_profile_content_hash IS NULL OR p_profile_content_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_receipt_digest IS NULL OR p_receipt_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_receipt_payload_digest IS NULL OR p_receipt_payload_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_genesis_envelope_digest IS NULL OR p_genesis_envelope_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_genesis_payload_digest IS NULL OR p_genesis_payload_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_receipt_digest = p_receipt_payload_digest
     OR p_genesis_envelope_digest = p_genesis_payload_digest
     OR p_previous_generation IS DISTINCT FROM 0 OR p_generation IS DISTINCT FROM 1
     OR p_previous_fence IS NULL OR p_previous_fence !~ '^sha256:[0-9a-f]{64}$'
     OR p_expected_empty_fence IS NULL OR p_expected_empty_fence <> p_previous_fence
     OR p_fence IS NULL OR p_fence !~ '^sha256:[0-9a-f]{64}$' OR p_fence = p_previous_fence
     OR p_corpus_content_hash IS NULL OR p_corpus_content_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_provider_route_authority_hash IS NULL OR p_provider_route_authority_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_runner_immutable_digest IS NULL OR p_runner_immutable_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_source_tree_digest IS NULL OR p_source_tree_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_trust_policy_hash IS NULL OR p_trust_policy_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_current_trust_policy_hash IS NULL OR p_current_trust_policy_hash <> p_trust_policy_hash
     OR p_initial_revocation_authority_id IS DISTINCT FROM 'model-governance-revocations'
     OR p_initial_revocation_authority_hash IS NULL OR p_initial_revocation_authority_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_initial_revocation_authority_epoch IS NULL OR p_initial_revocation_authority_epoch <= 0
     OR p_current_revocation_authority_hash IS NULL
     OR p_current_revocation_authority_hash <> p_initial_revocation_authority_hash
     OR p_current_revocation_authority_epoch IS NULL
     OR p_current_revocation_authority_epoch <> p_initial_revocation_authority_epoch
     OR p_activated_at IS NULL OR p_activated_at <> date_trunc('milliseconds', p_activated_at) THEN
    RAISE EXCEPTION 'Model Governance Genesis append is structurally invalid'
      USING ERRCODE = '40001';
  END IF;

  SELECT namespace.nspname INTO v_crypto_schema
  FROM pg_catalog.pg_extension AS extension
  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = extension.extnamespace
  WHERE extension.extname = 'pgcrypto';
  IF v_crypto_schema IS NULL THEN
    RAISE EXCEPTION 'pgcrypto is required for Model Governance Genesis request closure'
      USING ERRCODE = '55000';
  END IF;
  v_request_document := pg_catalog.format(
    '{"expectedEmptyFence":"%s","operationId":"%s","receiptDigest":"%s"}',
    p_expected_empty_fence, p_operation_id::text, p_receipt_digest
  );
  EXECUTE pg_catalog.format(
    'SELECT ''sha256:'' || pg_catalog.encode(%I.digest(pg_catalog.convert_to($1, ''UTF8''), ''sha256''), ''hex'')',
    v_crypto_schema
  ) INTO v_request_hash USING v_request_document;
  IF v_request_hash <> p_request_hash THEN
    RAISE EXCEPTION 'Model Governance Genesis request hash does not close the exact request'
      USING ERRCODE = '40001';
  END IF;

  -- The table lock serializes two concurrent first writers even while there is
  -- no workload-head row to lock. The revocation singleton is then locked in a
  -- stable order before the post-wait database clock observation.
  LOCK TABLE model_governance_activation_heads IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE model_governance_revocation_anchor IN SHARE ROW EXCLUSIVE MODE;

  SELECT * INTO v_existing
  FROM model_governance_activation_records
  WHERE operation_id = p_operation_id;
  IF FOUND THEN
    IF v_existing.request_hash <> p_request_hash
       OR v_existing.authority_kind <> 'genesis'
       OR v_existing.workload <> p_workload
       OR v_existing.profile_id <> p_profile_id
       OR v_existing.profile_content_hash <> p_profile_content_hash
       OR v_existing.receipt_digest <> p_receipt_digest
       OR v_existing.receipt_payload_digest <> p_receipt_payload_digest
       OR v_existing.genesis_envelope_digest <> p_genesis_envelope_digest
       OR v_existing.genesis_payload_digest <> p_genesis_payload_digest
       OR v_existing.previous_generation <> p_previous_generation
       OR v_existing.generation <> p_generation
       OR v_existing.previous_fence <> p_previous_fence
       OR v_existing.fence <> p_fence
       OR v_existing.corpus_content_hash <> p_corpus_content_hash
       OR v_existing.provider_route_authority_hash <> p_provider_route_authority_hash
       OR v_existing.runner_immutable_digest <> p_runner_immutable_digest
       OR v_existing.source_tree_digest <> p_source_tree_digest
       OR v_existing.trust_policy_hash <> p_trust_policy_hash
       OR v_existing.initial_revocation_authority_id <> p_initial_revocation_authority_id
       OR v_existing.initial_revocation_authority_hash <> p_initial_revocation_authority_hash
       OR v_existing.initial_revocation_authority_epoch <> p_initial_revocation_authority_epoch
       OR v_existing.activated_at <> p_activated_at THEN
      RAISE EXCEPTION 'Model Governance Genesis operation ID is bound to different bytes'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  IF EXISTS (
    SELECT 1 FROM model_governance_activation_heads WHERE workload = p_workload
  ) THEN
    RAISE EXCEPTION 'Model Governance Genesis requires an empty workload head'
      USING ERRCODE = '40001';
  END IF;
  SELECT * INTO v_revocation
  FROM model_governance_revocation_anchor
  WHERE singleton
  FOR UPDATE;
  IF NOT FOUND
     OR v_revocation.epoch <> p_current_revocation_authority_epoch
     OR v_revocation.authority_hash <> p_current_revocation_authority_hash
     OR v_revocation.current_trust_policy_hash <> p_current_trust_policy_hash
     OR v_revocation.trust_policy_revocation_hash <> p_current_revocation_authority_hash
     OR v_revocation.trust_policy_revocation_epoch <> p_current_revocation_authority_epoch THEN
    RAISE EXCEPTION 'Model Governance Genesis revocation authority drifted'
      USING ERRCODE = '40001';
  END IF;

  v_now := date_trunc('milliseconds', clock_timestamp());
  IF p_activated_at < v_now - interval '30 seconds'
     OR p_activated_at > v_now + interval '30 seconds'
     OR v_now < v_revocation.authority_issued_at - interval '30 seconds'
     OR v_now >= v_revocation.authority_expires_at THEN
    RAISE EXCEPTION 'Model Governance Genesis time or revocation authority is outside the post-lock trusted-time fence'
      USING ERRCODE = '40001';
  END IF;

  INSERT INTO model_governance_activation_records (
    operation_id, request_hash, authority_kind, workload, profile_id,
    profile_content_hash, receipt_digest, receipt_payload_digest,
    activation_envelope_digest, activation_payload_digest,
    previous_generation, generation, previous_fence, fence,
    corpus_content_hash, provider_route_authority_hash, runner_immutable_digest,
    source_tree_digest, trust_policy_hash, genesis_envelope_digest,
    genesis_payload_digest, initial_revocation_authority_id,
    initial_revocation_authority_hash, initial_revocation_authority_epoch,
    activated_at
  ) VALUES (
    p_operation_id, p_request_hash, 'genesis', p_workload, p_profile_id,
    p_profile_content_hash, p_receipt_digest, p_receipt_payload_digest,
    p_genesis_envelope_digest, p_genesis_payload_digest,
    p_previous_generation, p_generation, p_previous_fence, p_fence,
    p_corpus_content_hash, p_provider_route_authority_hash, p_runner_immutable_digest,
    p_source_tree_digest, p_trust_policy_hash, p_genesis_envelope_digest,
    p_genesis_payload_digest, p_initial_revocation_authority_id,
    p_initial_revocation_authority_hash, p_initial_revocation_authority_epoch,
    p_activated_at
  );
  INSERT INTO model_governance_activation_heads (workload, operation_id)
  VALUES (p_workload, p_operation_id);

  RETURN QUERY
  SELECT * FROM model_governance_activation_records WHERE operation_id = p_operation_id;
EXCEPTION
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation
    OR serialization_failure OR deadlock_detected OR numeric_value_out_of_range THEN
    RAISE EXCEPTION 'Model Governance Genesis append conflict: %', SQLERRM
      USING ERRCODE = '40001';
END;
$function$;

COMMENT ON FUNCTION append_model_governance_genesis(
  uuid, text, text, uuid, text, text, text, text, text, bigint, bigint,
  text, text, text, text, text, text, text, text, text, text, bigint,
  text, bigint, timestamptz, text
) IS 'Owner-only atomic signed Genesis append; no ordinary application execution grant exists.';

COMMENT ON FUNCTION observe_model_governance_trust_policy(text, text, bigint) IS
  'Fences one current trust-policy hash to an exact monotonic cumulative revocation epoch.';
COMMENT ON FUNCTION enforce_model_governance_activation_authority_anchor() IS
  'Atomic insert guard for ordinary activation and Genesis trust/revocation anchors.';

REVOKE ALL ON FUNCTION append_model_governance_genesis(
  uuid, text, text, uuid, text, text, text, text, text, bigint, bigint,
  text, text, text, text, text, text, text, text, text, text, bigint,
  text, bigint, timestamptz, text
) FROM PUBLIC;
REVOKE ALL ON FUNCTION observe_model_governance_trust_policy(text, text, bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION enforce_model_governance_activation_authority_anchor() FROM PUBLIC;

DO $model_governance_genesis_ownership_and_acl$
DECLARE
  schema_name text := current_schema();
BEGIN
  EXECUTE format(
    'ALTER FUNCTION %I.append_model_governance_genesis(uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,text,text,text,bigint,text,bigint,timestamptz,text) '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.observe_model_governance_trust_policy(text,text,bigint) '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.enforce_model_governance_activation_authority_anchor() '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format(
      'ALTER FUNCTION %I.append_model_governance_genesis(uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,text,text,text,bigint,text,bigint,timestamptz,text) OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.observe_model_governance_trust_policy(text,text,bigint) OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.enforce_model_governance_activation_authority_anchor() OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application') THEN
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.append_model_governance_genesis(uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,text,text,text,bigint,text,bigint,timestamptz,text) FROM worksflow_application',
      schema_name
    );
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.observe_model_governance_trust_policy(text,text,bigint) FROM worksflow_application',
      schema_name
    );
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.enforce_model_governance_activation_authority_anchor() FROM worksflow_application',
      schema_name
    );
  END IF;
END;
$model_governance_genesis_ownership_and_acl$;
