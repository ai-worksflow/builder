-- Durable, append-only Model Governance activation registry.  The immutable
-- record is the operation, history, and exact-profile projection; the separate
-- head contains only a foreign key to one exact immutable record.  V1 runtime
-- writes are not granted at all; future operator writes must remain restricted
-- to the reviewed SECURITY DEFINER routines below.
CREATE TABLE model_governance_activation_records (
  operation_id uuid PRIMARY KEY,
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  workload text NOT NULL CHECK (
    octet_length(workload) BETWEEN 1 AND 128
    AND workload ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
  ),
  profile_id uuid NOT NULL,
  profile_content_hash text NOT NULL CHECK (profile_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  receipt_digest text NOT NULL CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
  receipt_payload_digest text NOT NULL CHECK (receipt_payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  activation_envelope_digest text NOT NULL CHECK (activation_envelope_digest ~ '^sha256:[0-9a-f]{64}$'),
  activation_payload_digest text NOT NULL CHECK (activation_payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  previous_generation bigint NOT NULL CHECK (previous_generation >= 0),
  generation bigint NOT NULL CHECK (generation > 0 AND generation = previous_generation + 1),
  previous_fence text NOT NULL CHECK (previous_fence ~ '^sha256:[0-9a-f]{64}$'),
  fence text NOT NULL CHECK (fence ~ '^sha256:[0-9a-f]{64}$' AND fence <> previous_fence),
  corpus_content_hash text NOT NULL CHECK (corpus_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  provider_route_authority_hash text NOT NULL CHECK (provider_route_authority_hash ~ '^sha256:[0-9a-f]{64}$'),
  runner_immutable_digest text NOT NULL CHECK (runner_immutable_digest ~ '^sha256:[0-9a-f]{64}$'),
  source_tree_digest text NOT NULL CHECK (source_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  trust_policy_hash text NOT NULL CHECK (trust_policy_hash ~ '^sha256:[0-9a-f]{64}$'),
  activated_at timestamptz NOT NULL CHECK (activated_at = date_trunc('milliseconds', activated_at)),
  CONSTRAINT model_governance_activation_operation_v4 CHECK (
    operation_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT model_governance_activation_profile_v4 CHECK (
    profile_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT model_governance_activation_envelope_payload_distinct CHECK (
    receipt_digest <> receipt_payload_digest
    AND activation_envelope_digest <> activation_payload_digest
  ),
  CONSTRAINT model_governance_activation_history_unique UNIQUE (workload, generation),
  CONSTRAINT model_governance_activation_exact_profile_unique UNIQUE (
    workload, profile_id, profile_content_hash
  ),
  CONSTRAINT model_governance_activation_workload_operation_unique UNIQUE (workload, operation_id)
);

CREATE TABLE model_governance_activation_heads (
  workload text PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE,
  CONSTRAINT model_governance_activation_head_record_fk
    FOREIGN KEY (workload, operation_id)
    REFERENCES model_governance_activation_records(workload, operation_id)
    ON UPDATE RESTRICT ON DELETE RESTRICT
);

-- A singleton durable anchor retains the exact canonical bytes as well as its
-- parsed document.  The document is never accepted as a second authority: the
-- observe routine verifies that it is precisely the JSON represented by bytes
-- and that the bytes have authority_hash.
CREATE TABLE model_governance_revocation_anchor (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  epoch bigint NOT NULL CHECK (epoch > 0),
  authority_hash text NOT NULL CHECK (authority_hash ~ '^sha256:[0-9a-f]{64}$'),
  authority_bytes bytea NOT NULL CHECK (octet_length(authority_bytes) BETWEEN 1 AND 1048576),
  authority_document jsonb NOT NULL CHECK (jsonb_typeof(authority_document) = 'object'),
  authority_issued_at timestamptz NOT NULL CHECK (
    authority_issued_at = date_trunc('milliseconds', authority_issued_at)
  ),
  authority_expires_at timestamptz NOT NULL CHECK (
    authority_expires_at = date_trunc('milliseconds', authority_expires_at)
    AND authority_expires_at > authority_issued_at
    AND authority_expires_at <= authority_issued_at + interval '5 minutes'
  ),
  observed_at timestamptz NOT NULL CHECK (observed_at = date_trunc('milliseconds', observed_at))
);

CREATE FUNCTION reject_model_governance_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog
AS $function$
BEGIN
  RAISE EXCEPTION 'Model Governance immutable authority cannot be updated, deleted, or truncated'
    USING ERRCODE = '55000';
END;
$function$;

CREATE TRIGGER model_governance_activation_records_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON model_governance_activation_records
FOR EACH STATEMENT EXECUTE FUNCTION reject_model_governance_immutable_mutation();

CREATE TRIGGER model_governance_activation_heads_no_delete
BEFORE DELETE OR TRUNCATE ON model_governance_activation_heads
FOR EACH STATEMENT EXECUTE FUNCTION reject_model_governance_immutable_mutation();

CREATE TRIGGER model_governance_revocation_anchor_no_delete
BEFORE DELETE OR TRUNCATE ON model_governance_revocation_anchor
FOR EACH STATEMENT EXECUTE FUNCTION reject_model_governance_immutable_mutation();

CREATE FUNCTION append_model_governance_activation(
  p_expected_generation bigint,
  p_expected_fence text,
  p_operation_id uuid,
  p_request_hash text,
  p_workload text,
  p_profile_id uuid,
  p_profile_content_hash text,
  p_receipt_digest text,
  p_receipt_payload_digest text,
  p_activation_envelope_digest text,
  p_activation_payload_digest text,
  p_previous_generation bigint,
  p_generation bigint,
  p_previous_fence text,
  p_fence text,
  p_corpus_content_hash text,
  p_provider_route_authority_hash text,
  p_runner_immutable_digest text,
  p_source_tree_digest text,
  p_trust_policy_hash text,
  p_activated_at timestamptz
)
RETURNS SETOF model_governance_activation_records
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $function$
DECLARE
  v_existing model_governance_activation_records%ROWTYPE;
  v_current_generation bigint;
  v_current_fence text;
  v_now timestamptz;
  v_request_document text;
  v_request_hash text;
  v_crypto_schema text;
BEGIN
  IF p_expected_generation IS NULL OR p_expected_generation < 0
     OR p_expected_fence IS NULL OR p_expected_fence !~ '^sha256:[0-9a-f]{64}$'
     OR p_operation_id IS NULL
     OR p_operation_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_workload IS NULL OR octet_length(p_workload) NOT BETWEEN 1 AND 128
     OR p_workload !~ '^[a-z0-9]+(-[a-z0-9]+)*$'
     OR p_profile_id IS NULL
     OR p_profile_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_profile_content_hash IS NULL OR p_profile_content_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_receipt_digest IS NULL OR p_receipt_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_receipt_payload_digest IS NULL OR p_receipt_payload_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_activation_envelope_digest IS NULL OR p_activation_envelope_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_activation_payload_digest IS NULL OR p_activation_payload_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_previous_generation IS NULL OR p_generation IS NULL
     OR p_previous_generation <> p_expected_generation
     OR p_generation <> p_previous_generation + 1
     OR p_previous_fence IS NULL OR p_previous_fence <> p_expected_fence
     OR p_fence IS NULL OR p_fence !~ '^sha256:[0-9a-f]{64}$' OR p_fence = p_previous_fence
     OR p_corpus_content_hash IS NULL OR p_corpus_content_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_provider_route_authority_hash IS NULL OR p_provider_route_authority_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_runner_immutable_digest IS NULL OR p_runner_immutable_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_source_tree_digest IS NULL OR p_source_tree_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_trust_policy_hash IS NULL OR p_trust_policy_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_activated_at IS NULL OR p_activated_at <> date_trunc('milliseconds', p_activated_at)
     OR p_receipt_digest = p_receipt_payload_digest
     OR p_activation_envelope_digest = p_activation_payload_digest THEN
    RAISE EXCEPTION 'Model Governance activation append is structurally invalid'
      USING ERRCODE = '40001';
  END IF;

  SELECT namespace.nspname
    INTO v_crypto_schema
  FROM pg_catalog.pg_extension AS extension
  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = extension.extnamespace
  WHERE extension.extname = 'pgcrypto';
  IF v_crypto_schema IS NULL THEN
    RAISE EXCEPTION 'pgcrypto is required for Model Governance request closure'
      USING ERRCODE = '55000';
  END IF;
  v_request_document := pg_catalog.format(
    '{"expectedFence":"%s","expectedGeneration":%s,"operationId":"%s","receiptDigest":"%s"}',
    p_expected_fence, p_expected_generation, p_operation_id::text, p_receipt_digest
  );
  EXECUTE pg_catalog.format(
    'SELECT ''sha256:'' || pg_catalog.encode(%I.digest(pg_catalog.convert_to($1, ''UTF8''), ''sha256''), ''hex'')',
    v_crypto_schema
  ) INTO v_request_hash USING v_request_document;
  IF v_request_hash <> p_request_hash THEN
    RAISE EXCEPTION 'Model Governance request hash does not close the exact append request'
      USING ERRCODE = '40001';
  END IF;

  SELECT * INTO v_existing
  FROM model_governance_activation_records
  WHERE operation_id = p_operation_id;
  IF FOUND THEN
    IF v_existing.request_hash <> p_request_hash THEN
      RAISE EXCEPTION 'Model Governance operation ID is bound to another request'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  SELECT record.generation, record.fence
    INTO v_current_generation, v_current_fence
  FROM model_governance_activation_heads AS head
  JOIN model_governance_activation_records AS record
    ON record.workload = head.workload AND record.operation_id = head.operation_id
  WHERE head.workload = p_workload
  FOR UPDATE OF head;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Model Governance empty workload head requires a separate signed bootstrap authority'
      USING ERRCODE = '40001';
  END IF;
  IF v_current_generation <> p_expected_generation OR v_current_fence <> p_expected_fence THEN
    RAISE EXCEPTION 'Model Governance workload generation/fence CAS conflict'
      USING ERRCODE = '40001';
  END IF;

  -- Take a fresh clock observation only after the CAS row is locked.  Lock
  -- waiting can never extend the authority of a timestamp checked earlier.
  v_now := date_trunc('milliseconds', clock_timestamp());
  IF p_activated_at < v_now - interval '30 seconds'
     OR p_activated_at > v_now + interval '30 seconds' THEN
    RAISE EXCEPTION 'Model Governance activation time is outside the PostgreSQL trusted-time fence'
      USING ERRCODE = '40001';
  END IF;

  INSERT INTO model_governance_activation_records (
    operation_id, request_hash, workload, profile_id, profile_content_hash,
    receipt_digest, receipt_payload_digest, activation_envelope_digest,
    activation_payload_digest, previous_generation, generation, previous_fence,
    fence, corpus_content_hash, provider_route_authority_hash,
    runner_immutable_digest, source_tree_digest, trust_policy_hash, activated_at
  ) VALUES (
    p_operation_id, p_request_hash, p_workload, p_profile_id, p_profile_content_hash,
    p_receipt_digest, p_receipt_payload_digest, p_activation_envelope_digest,
    p_activation_payload_digest, p_previous_generation, p_generation, p_previous_fence,
    p_fence, p_corpus_content_hash, p_provider_route_authority_hash,
    p_runner_immutable_digest, p_source_tree_digest, p_trust_policy_hash, p_activated_at
  );
  UPDATE model_governance_activation_heads
  SET operation_id = p_operation_id
  WHERE workload = p_workload;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Model Governance activation head disappeared during append'
      USING ERRCODE = '40001';
  END IF;

  RETURN QUERY
  SELECT * FROM model_governance_activation_records WHERE operation_id = p_operation_id;
EXCEPTION
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation
    OR serialization_failure OR deadlock_detected OR numeric_value_out_of_range THEN
    RAISE EXCEPTION 'Model Governance activation append conflict: %', SQLERRM
      USING ERRCODE = '40001';
END;
$function$;

CREATE FUNCTION observe_model_governance_revocation_authority(
  p_epoch bigint,
  p_authority_hash text,
  p_authority_bytes bytea,
  p_authority_document jsonb
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $function$
DECLARE
  v_current model_governance_revocation_anchor%ROWTYPE;
  v_actual_hash text;
  v_crypto_schema text;
  v_issued_at timestamptz;
  v_expires_at timestamptz;
  v_now timestamptz;
  v_anchor_found boolean;
BEGIN
  IF p_epoch IS NULL OR p_epoch <= 0
     OR p_authority_hash IS NULL OR p_authority_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_authority_bytes IS NULL OR octet_length(p_authority_bytes) NOT BETWEEN 1 AND 1048576
     OR p_authority_document IS NULL OR jsonb_typeof(p_authority_document) <> 'object'
     OR NOT p_authority_document ?& ARRAY[
       'digestRevocations', 'epoch', 'expiresAt', 'issuedAt', 'schemaVersion', 'signerRevocations'
     ]
     OR p_authority_document - ARRAY[
       'digestRevocations', 'epoch', 'expiresAt', 'issuedAt', 'schemaVersion', 'signerRevocations'
     ] <> '{}'::jsonb
     OR p_authority_document->>'schemaVersion' <> 'worksflow-model-governance-revocation-authority/v1'
     OR jsonb_typeof(p_authority_document->'epoch') <> 'number'
     OR (p_authority_document->>'epoch') !~ '^[1-9][0-9]*$'
     OR (p_authority_document->>'epoch')::numeric <> p_epoch::numeric
     OR jsonb_typeof(p_authority_document->'digestRevocations') <> 'array'
     OR jsonb_array_length(p_authority_document->'digestRevocations') > 4096
     OR jsonb_typeof(p_authority_document->'signerRevocations') <> 'array'
     OR jsonb_array_length(p_authority_document->'signerRevocations') > 1024
     OR (p_authority_document->>'issuedAt') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$'
     OR (p_authority_document->>'expiresAt') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$' THEN
    RAISE EXCEPTION 'Model Governance revocation authority shape is invalid'
      USING ERRCODE = '40001';
  END IF;

  IF EXISTS (
    SELECT 1 FROM jsonb_array_elements(p_authority_document->'digestRevocations') AS entry(value)
    WHERE jsonb_typeof(entry.value) <> 'object'
       OR NOT entry.value ?& ARRAY['digest', 'reasonHash', 'revokedAt']
       OR entry.value - ARRAY['digest', 'reasonHash', 'revokedAt'] <> '{}'::jsonb
       OR entry.value->>'digest' !~ '^sha256:[0-9a-f]{64}$'
       OR entry.value->>'reasonHash' !~ '^sha256:[0-9a-f]{64}$'
       OR entry.value->>'revokedAt' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$'
  ) OR EXISTS (
    SELECT 1 FROM jsonb_array_elements(p_authority_document->'signerRevocations') AS entry(value)
    WHERE jsonb_typeof(entry.value) <> 'object'
       OR NOT entry.value ?& ARRAY['keyId', 'policyHash', 'publicKeyHash', 'reasonHash', 'revokedAt']
       OR entry.value - ARRAY['keyId', 'policyHash', 'publicKeyHash', 'reasonHash', 'revokedAt'] <> '{}'::jsonb
       OR octet_length(entry.value->>'keyId') NOT BETWEEN 1 AND 128
       OR entry.value->>'keyId' !~ '^[a-z0-9]+(-[a-z0-9]+)*$'
       OR entry.value->>'policyHash' !~ '^sha256:[0-9a-f]{64}$'
       OR entry.value->>'publicKeyHash' !~ '^sha256:[0-9a-f]{64}$'
       OR entry.value->>'reasonHash' !~ '^sha256:[0-9a-f]{64}$'
       OR entry.value->>'revokedAt' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$'
  ) THEN
    RAISE EXCEPTION 'Model Governance revocation entries are structurally invalid'
      USING ERRCODE = '40001';
  END IF;

  SELECT namespace.nspname
    INTO v_crypto_schema
  FROM pg_catalog.pg_extension AS extension
  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = extension.extnamespace
  WHERE extension.extname = 'pgcrypto';
  IF v_crypto_schema IS NULL THEN
    RAISE EXCEPTION 'pgcrypto is required for Model Governance revocation closure'
      USING ERRCODE = '55000';
  END IF;
  EXECUTE pg_catalog.format(
    'SELECT ''sha256:'' || pg_catalog.encode(%I.digest($1, ''sha256''), ''hex'')',
    v_crypto_schema
  ) INTO v_actual_hash USING p_authority_bytes;
  IF v_actual_hash <> p_authority_hash
     OR pg_catalog.convert_from(p_authority_bytes, 'UTF8')::jsonb <> p_authority_document THEN
    RAISE EXCEPTION 'Model Governance revocation bytes, hash, and document do not close'
      USING ERRCODE = '40001';
  END IF;

  v_issued_at := (p_authority_document->>'issuedAt')::timestamptz;
  v_expires_at := (p_authority_document->>'expiresAt')::timestamptz;
  IF p_authority_document->>'issuedAt' <> to_char(v_issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
     OR p_authority_document->>'expiresAt' <> to_char(v_expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
     OR v_expires_at <= v_issued_at OR v_expires_at > v_issued_at + interval '5 minutes' THEN
    RAISE EXCEPTION 'Model Governance revocation validity window is invalid'
      USING ERRCODE = '40001';
  END IF;

  -- A table lock serializes the initially empty singleton as well as updates.
  -- Re-read the database clock after all lock waiting before accepting current
  -- authority, so an authority cannot expire while queued and then commit.
  LOCK TABLE model_governance_revocation_anchor IN SHARE ROW EXCLUSIVE MODE;
  SELECT * INTO v_current
  FROM model_governance_revocation_anchor
  WHERE singleton
  FOR UPDATE;
  v_anchor_found := FOUND;
  v_now := date_trunc('milliseconds', clock_timestamp());
  IF v_now < v_issued_at - interval '30 seconds' OR v_now >= v_expires_at THEN
    RAISE EXCEPTION 'Model Governance revocation authority is not current at the post-lock trusted time'
      USING ERRCODE = '40001';
  END IF;
  IF v_anchor_found THEN
    IF p_epoch < v_current.epoch
       OR (p_epoch = v_current.epoch AND p_authority_hash <> v_current.authority_hash)
       OR (p_epoch = v_current.epoch AND (
         p_authority_bytes <> v_current.authority_bytes
         OR p_authority_document <> v_current.authority_document
       ))
       OR (p_epoch > v_current.epoch AND (
         NOT ((p_authority_document->'digestRevocations') @> (v_current.authority_document->'digestRevocations'))
         OR NOT ((p_authority_document->'signerRevocations') @> (v_current.authority_document->'signerRevocations'))
       )) THEN
      RAISE EXCEPTION 'Model Governance revocation rollback, equivocation, or deletion'
        USING ERRCODE = '40001';
    END IF;
    IF p_epoch = v_current.epoch THEN
      RETURN;
    END IF;
    UPDATE model_governance_revocation_anchor
    SET epoch = p_epoch,
        authority_hash = p_authority_hash,
        authority_bytes = p_authority_bytes,
        authority_document = p_authority_document,
        authority_issued_at = v_issued_at,
        authority_expires_at = v_expires_at,
        observed_at = v_now
    WHERE singleton;
    RETURN;
  END IF;

  INSERT INTO model_governance_revocation_anchor (
    singleton, epoch, authority_hash, authority_bytes, authority_document,
    authority_issued_at, authority_expires_at, observed_at
  ) VALUES (
    true, p_epoch, p_authority_hash, p_authority_bytes, p_authority_document,
    v_issued_at, v_expires_at, v_now
  );
EXCEPTION
  WHEN unique_violation OR check_violation OR not_null_violation
    OR serialization_failure OR deadlock_detected OR numeric_value_out_of_range
    OR invalid_datetime_format OR character_not_in_repertoire THEN
    RAISE EXCEPTION 'Model Governance revocation observation conflict: %', SQLERRM
      USING ERRCODE = '40001';
END;
$function$;

COMMENT ON TABLE model_governance_activation_records IS
  'Immutable operation/history/exact-profile Model Governance activation records; no unsigned bootstrap insertion exists.';
COMMENT ON TABLE model_governance_activation_heads IS
  'CAS workload heads pointing to exact immutable Model Governance activation records.';
COMMENT ON TABLE model_governance_revocation_anchor IS
  'Singleton durable highest cumulative Model Governance revocation authority, retaining exact canonical bytes and parsed document.';

REVOKE ALL ON TABLE model_governance_activation_records FROM PUBLIC;
REVOKE ALL ON TABLE model_governance_activation_heads FROM PUBLIC;
REVOKE ALL ON TABLE model_governance_revocation_anchor FROM PUBLIC;
REVOKE ALL ON FUNCTION reject_model_governance_immutable_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION append_model_governance_activation(
  bigint, text, uuid, text, text, uuid, text, text, text, text, text,
  bigint, bigint, text, text, text, text, text, text, text, timestamptz
) FROM PUBLIC;
REVOKE ALL ON FUNCTION observe_model_governance_revocation_authority(
  bigint, text, bytea, jsonb
) FROM PUBLIC;

DO $model_governance_ownership_and_acl$
DECLARE
  schema_name text := current_schema();
BEGIN
  -- Harden SECURITY DEFINER lookup before exposing either entrypoint.
  EXECUTE format(
    'ALTER FUNCTION %I.append_model_governance_activation(bigint,text,uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,timestamptz) '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.observe_model_governance_revocation_authority(bigint,text,bytea,jsonb) '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format('ALTER TABLE %I.model_governance_activation_records OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.model_governance_activation_heads OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.model_governance_revocation_anchor OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.reject_model_governance_immutable_mutation() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format(
      'ALTER FUNCTION %I.append_model_governance_activation(bigint,text,uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,timestamptz) OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.observe_model_governance_revocation_authority(bigint,text,bytea,jsonb) OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application') THEN
    -- No API-role grant is made in v1.  A future independently provisioned
    -- governance operator/login and authority adapter must be reviewed before
    -- this store can be wired.  Explicit revocation also repairs predecessor
    -- or manually granted privileges when upgrading an existing database.
    EXECUTE format('REVOKE ALL ON TABLE %I.model_governance_activation_records FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.model_governance_activation_heads FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.model_governance_revocation_anchor FROM worksflow_application', schema_name);
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.append_model_governance_activation(bigint,text,uuid,text,text,uuid,text,text,text,text,text,bigint,bigint,text,text,text,text,text,text,text,timestamptz) FROM worksflow_application',
      schema_name
    );
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.observe_model_governance_revocation_authority(bigint,text,bytea,jsonb) FROM worksflow_application',
      schema_name
    );
  END IF;
END;
$model_governance_ownership_and_acl$;
