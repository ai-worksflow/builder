-- Durable, non-secret Qualification Evidence orchestration authority. The
-- closed event document contains commitments and stable references only; raw
-- credentials, cookies, headers, environment values, paths, and free-form
-- metadata have no column or event field.
--
-- The canonical Go PostgresStore is the only supported writer and replays the
-- just-inserted exact bytes in the same transaction. This owner-only routine is
-- not a general SQL API: JSONB equality cannot distinguish duplicate raw keys
-- or normalized number spellings. Do not grant its EXECUTE privilege to a
-- non-owner operator until a database-side raw canonical-JSON validator is
-- added. The owner already has ALTER/DROP authority and is a trust boundary.
DO $qualification_evidence_hash_function$
DECLARE
  schema_name text := current_schema();
  extension_schema text;
BEGIN
  SELECT n.nspname INTO extension_schema
  FROM pg_catalog.pg_extension AS e
  JOIN pg_catalog.pg_namespace AS n ON n.oid = e.extnamespace
  WHERE e.extname = 'pgcrypto';
  IF extension_schema IS NULL THEN
    RAISE EXCEPTION 'migration 000073 requires pgcrypto';
  END IF;
  EXECUTE format(
    'CREATE FUNCTION %I.qualification_evidence_sha256(bytea) RETURNS text '
    'LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS %L',
    schema_name,
    format('SELECT ''sha256:'' || pg_catalog.encode(%I.digest($1, ''sha256''), ''hex'')', extension_schema)
  );
END;
$qualification_evidence_hash_function$;

CREATE TABLE qualification_evidence_events (
  event_id uuid PRIMARY KEY,
  orchestration_id uuid NOT NULL,
  version bigint NOT NULL CHECK (version > 0),
  expected_version bigint NOT NULL CHECK (expected_version >= 0 AND version = expected_version + 1),
  event_kind text NOT NULL CHECK (event_kind IN (
    'reserved','credential-issue-started','credential-issued','run-closure-started',
    'run-closure-accepted','encryption-started','encryption-committed',
    'kms-attestation-started','kms-attested','credential-revocation-started',
    'credential-revoked','artifact-index-started','artifact-indexed',
    'receipt-sign-started','receipt-signed','snapshot-seal-started',
    'snapshot-sealed','snapshot-verified'
  )),
  operation_id uuid NOT NULL,
  active_artifact_id text NOT NULL CHECK (
    active_artifact_id = '' OR (
      octet_length(active_artifact_id) BETWEEN 1 AND 128
      AND active_artifact_id ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
    )
  ),
  event_at timestamptz NOT NULL CHECK (event_at = date_trunc('milliseconds', event_at)),
  requested_at timestamptz NOT NULL CHECK (requested_at = date_trunc('milliseconds', requested_at)),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (octet_length(request_bytes) BETWEEN 1 AND 2359296),
  request_document jsonb NOT NULL CHECK (jsonb_typeof(request_document) = 'object'),
  event_hash text NOT NULL CHECK (event_hash ~ '^sha256:[0-9a-f]{64}$'),
  event_bytes bytea NOT NULL CHECK (octet_length(event_bytes) BETWEEN 1 AND 2097152),
  event_document jsonb NOT NULL CHECK (jsonb_typeof(event_document) = 'object'),
  UNIQUE (orchestration_id, version)
);

-- Every mutating operation is reserved atomically with the Plan. Rows never
-- change; event_kind is a logical operation family and artifact_id is nonempty
-- only for the exact restricted artifact encrypted by that operation.
CREATE TABLE qualification_evidence_operations (
  operation_id uuid PRIMARY KEY,
  orchestration_id uuid NOT NULL,
  operation_kind text NOT NULL CHECK (operation_kind IN (
    'reserve','credential-issue','run-closure','encryption','kms-attestation',
    'credential-revocation','artifact-index','receipt-sign','snapshot-seal'
  )),
  artifact_id text NOT NULL CHECK (
    artifact_id = '' OR (
      octet_length(artifact_id) BETWEEN 1 AND 128
      AND artifact_id ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
    )
  ),
  reservation_event_id uuid NOT NULL REFERENCES qualification_evidence_events(event_id) ON DELETE RESTRICT,
  reserved_at timestamptz NOT NULL CHECK (reserved_at = date_trunc('milliseconds', reserved_at)),
  UNIQUE (orchestration_id, operation_kind, artifact_id),
  CHECK ((operation_kind = 'encryption') = (artifact_id <> ''))
);

-- This is a guarded acceleration projection, never an input to the domain
-- state machine. Store.Load replays and validates immutable events, then only
-- compares the replayed result with this row.
CREATE TABLE qualification_evidence_heads (
  orchestration_id uuid PRIMARY KEY,
  version bigint NOT NULL CHECK (version > 0),
  phase text NOT NULL CHECK (phase IN (
    'reserved','credential-issue-started','credential-issued','run-closure-started',
    'run-closure-accepted','encrypting','kms-attestation-started','kms-attested',
    'credential-revocation-started','credential-revoked','artifact-index-started',
    'artifact-indexed','receipt-sign-started','receipt-signed','snapshot-seal-started',
    'snapshot-sealed','complete'
  )),
  last_event_id uuid NOT NULL UNIQUE REFERENCES qualification_evidence_events(event_id) ON DELETE RESTRICT,
  last_event_at timestamptz NOT NULL CHECK (last_event_at = date_trunc('milliseconds', last_event_at)),
  command_hash text NOT NULL CHECK (command_hash ~ '^sha256:[0-9a-f]{64}$'),
  trust_bindings_digest text NOT NULL CHECK (trust_bindings_digest ~ '^sha256:[0-9a-f]{64}$'),
  active_operation_id uuid,
  active_artifact_id text NOT NULL CHECK (
    active_artifact_id = '' OR (
      octet_length(active_artifact_id) BETWEEN 1 AND 128
      AND active_artifact_id ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
    )
  ),
  plan_document jsonb NOT NULL CHECK (
    jsonb_typeof(plan_document) = 'object'
    AND octet_length(plan_document::text) BETWEEN 1 AND 2097152
  ),
  CHECK ((active_operation_id IS NULL) = (active_artifact_id = '') OR active_artifact_id = '')
);

-- Private transaction-local authorization for the head guard. It is not an
-- audit ledger and is empty at transaction boundaries.
CREATE TABLE qualification_evidence_projection_authorizations (
  transaction_id bigint NOT NULL,
  backend_pid integer NOT NULL,
  PRIMARY KEY (transaction_id, backend_pid)
);

CREATE FUNCTION reject_qualification_evidence_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification Evidence event and operation ledgers are immutable'
    USING ERRCODE = 'WQE03';
END;
$function$;

CREATE TRIGGER qualification_evidence_events_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_evidence_events
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_evidence_immutable_mutation();

CREATE TRIGGER qualification_evidence_operations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_evidence_operations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_evidence_immutable_mutation();

CREATE FUNCTION guard_qualification_evidence_head_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM qualification_evidence_projection_authorizations
    WHERE transaction_id = txid_current() AND backend_pid = pg_backend_pid()
  ) THEN
    RAISE EXCEPTION 'Qualification Evidence head may move only through append_qualification_evidence_event'
      USING ERRCODE = 'WQE03';
  END IF;
  RETURN NULL;
END;
$function$;

CREATE TRIGGER qualification_evidence_heads_guard
BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON qualification_evidence_heads
FOR EACH STATEMENT EXECUTE FUNCTION guard_qualification_evidence_head_projection();

CREATE FUNCTION append_qualification_evidence_event(
  p_request_hash text,
  p_request_bytes bytea,
  p_request_document jsonb,
  p_event_hash text,
  p_event_bytes bytea,
  p_event_document jsonb
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_event jsonb;
  v_plan jsonb;
  v_payload jsonb;
  v_existing qualification_evidence_events%ROWTYPE;
  v_head qualification_evidence_heads%ROWTYPE;
  v_operation qualification_evidence_operations%ROWTYPE;
  v_operation_row record;
  v_orchestration_id uuid;
  v_event_id uuid;
  v_operation_id uuid;
  v_expected_version bigint;
  v_requested_at timestamptz;
  v_now timestamptz;
  v_kind text;
  v_operation_kind text;
  v_next_phase text;
  v_next_active_operation uuid;
  v_next_active_artifact text := '';
  v_artifact_id text := '';
  v_payload_count integer;
  v_identity_count integer;
  v_distinct_identity_count integer;
BEGIN
  IF p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_request_bytes IS NULL OR octet_length(p_request_bytes) NOT BETWEEN 1 AND 2359296
     OR get_byte(p_request_bytes, 0) <> 123 OR get_byte(p_request_bytes, octet_length(p_request_bytes) - 1) <> 125
     OR p_request_document IS NULL OR jsonb_typeof(p_request_document) <> 'object'
     OR qualification_evidence_sha256(p_request_bytes) <> p_request_hash
     OR convert_from(p_request_bytes, 'UTF8')::jsonb <> p_request_document
     OR NOT (p_request_document ?& ARRAY['event','eventHash','expectedVersion','orchestrationId','schemaVersion'])
     OR p_request_document - ARRAY['event','eventHash','expectedVersion','orchestrationId','schemaVersion'] <> '{}'::jsonb
     OR p_request_document->>'schemaVersion' <> 'worksflow-qualification-evidence-event-request/v1'
     OR jsonb_typeof(p_request_document->'event') <> 'object'
     OR jsonb_typeof(p_request_document->'eventHash') <> 'string'
     OR jsonb_typeof(p_request_document->'expectedVersion') <> 'number'
     OR p_request_document->>'expectedVersion' !~ '^(0|[1-9][0-9]{0,18})$'
     OR jsonb_typeof(p_request_document->'orchestrationId') <> 'string' THEN
    RAISE EXCEPTION 'Qualification Evidence request bytes, hash, or closed root shape is invalid' USING ERRCODE = 'WQE03';
  END IF;
  IF p_event_hash IS NULL OR p_event_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_event_bytes IS NULL OR octet_length(p_event_bytes) NOT BETWEEN 1 AND 2097152
     OR get_byte(p_event_bytes, 0) <> 123 OR get_byte(p_event_bytes, octet_length(p_event_bytes) - 1) <> 125
     OR p_event_document IS NULL OR jsonb_typeof(p_event_document) <> 'object'
     OR qualification_evidence_sha256(p_event_bytes) <> p_event_hash
     OR convert_from(p_event_bytes, 'UTF8')::jsonb <> p_event_document
     OR p_request_document->>'eventHash' <> p_event_hash
     OR p_request_document->'event' <> p_event_document THEN
    RAISE EXCEPTION 'Qualification Evidence event bytes, hash, or document is invalid' USING ERRCODE = 'WQE03';
  END IF;

  v_event := p_event_document;
  IF NOT (v_event ?& ARRAY[
       'at','eventId','kind','operationId','commandHash','trustBindingsDigest','plan',
       'credentialIssue','runClosure','encryption','kmsAttestation','credentialRevocation',
       'artifactIndex','receipt','snapshot','verification'
     ])
     OR v_event - ARRAY[
       'at','eventId','kind','operationId','commandHash','trustBindingsDigest','plan',
       'credentialIssue','runClosure','encryption','kmsAttestation','credentialRevocation',
       'artifactIndex','receipt','snapshot','verification'
     ] <> '{}'::jsonb
     OR jsonb_typeof(v_event->'at') <> 'string'
     OR jsonb_typeof(v_event->'eventId') <> 'string'
     OR jsonb_typeof(v_event->'kind') <> 'string'
     OR jsonb_typeof(v_event->'operationId') <> 'string'
     OR jsonb_typeof(v_event->'commandHash') <> 'string'
     OR jsonb_typeof(v_event->'trustBindingsDigest') <> 'string' THEN
    RAISE EXCEPTION 'Qualification Evidence event has unknown, nullable, or widened root fields' USING ERRCODE = 'WQE03';
  END IF;
  BEGIN
    v_orchestration_id := (p_request_document->>'orchestrationId')::uuid;
    v_event_id := (v_event->>'eventId')::uuid;
    v_operation_id := (v_event->>'operationId')::uuid;
    v_expected_version := (p_request_document->>'expectedVersion')::bigint;
    v_requested_at := (v_event->>'at')::timestamptz;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Qualification Evidence scalar identity is malformed' USING ERRCODE = 'WQE03';
  END;
  v_kind := v_event->>'kind';
  IF v_orchestration_id::text <> p_request_document->>'orchestrationId'
     OR v_event_id::text <> v_event->>'eventId'
     OR v_operation_id::text <> v_event->>'operationId'
     OR p_request_document->>'orchestrationId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_event->>'eventId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_event->>'operationId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR to_char(v_requested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') <> v_event->>'at'
     OR v_kind NOT IN (
       'reserved','credential-issue-started','credential-issued','run-closure-started',
       'run-closure-accepted','encryption-started','encryption-committed',
       'kms-attestation-started','kms-attested','credential-revocation-started',
       'credential-revoked','artifact-index-started','artifact-indexed',
       'receipt-sign-started','receipt-signed','snapshot-seal-started',
       'snapshot-sealed','snapshot-verified'
     ) THEN
    RAISE EXCEPTION 'Qualification Evidence scalar values are non-canonical' USING ERRCODE = 'WQE03';
  END IF;

  v_payload_count :=
    (jsonb_typeof(v_event->'credentialIssue') = 'object')::integer +
    (jsonb_typeof(v_event->'runClosure') = 'object')::integer +
    (jsonb_typeof(v_event->'encryption') = 'object')::integer +
    (jsonb_typeof(v_event->'kmsAttestation') = 'object')::integer +
    (jsonb_typeof(v_event->'credentialRevocation') = 'object')::integer +
    (jsonb_typeof(v_event->'artifactIndex') = 'object')::integer +
    (jsonb_typeof(v_event->'receipt') = 'object')::integer +
    (jsonb_typeof(v_event->'snapshot') = 'object')::integer +
    (jsonb_typeof(v_event->'verification') = 'object')::integer;
  IF v_kind = 'reserved' THEN
    IF jsonb_typeof(v_event->'plan') <> 'object' OR v_payload_count <> 0
       OR v_event->>'commandHash' !~ '^sha256:[0-9a-f]{64}$'
       OR v_event->>'trustBindingsDigest' !~ '^sha256:[0-9a-f]{64}$' THEN
      RAISE EXCEPTION 'Qualification Evidence reservation payload is invalid' USING ERRCODE = 'WQE03';
    END IF;
  ELSIF v_kind IN (
    'credential-issue-started','run-closure-started','encryption-started',
    'kms-attestation-started','credential-revocation-started','artifact-index-started',
    'receipt-sign-started','snapshot-seal-started'
  ) THEN
    IF jsonb_typeof(v_event->'plan') <> 'null' OR v_payload_count <> 0
       OR v_event->>'commandHash' <> '' OR v_event->>'trustBindingsDigest' <> '' THEN
      RAISE EXCEPTION 'Qualification Evidence started event is widened' USING ERRCODE = 'WQE03';
    END IF;
  ELSE
    IF jsonb_typeof(v_event->'plan') <> 'null' OR v_payload_count <> 1
       OR v_event->>'commandHash' <> '' OR v_event->>'trustBindingsDigest' <> '' THEN
      RAISE EXCEPTION 'Qualification Evidence accepted event payload is widened' USING ERRCODE = 'WQE03';
    END IF;
    v_payload := CASE v_kind
      WHEN 'credential-issued' THEN v_event->'credentialIssue'
      WHEN 'run-closure-accepted' THEN v_event->'runClosure'
      WHEN 'encryption-committed' THEN v_event->'encryption'
      WHEN 'kms-attested' THEN v_event->'kmsAttestation'
      WHEN 'credential-revoked' THEN v_event->'credentialRevocation'
      WHEN 'artifact-indexed' THEN v_event->'artifactIndex'
      WHEN 'receipt-signed' THEN v_event->'receipt'
      WHEN 'snapshot-sealed' THEN v_event->'snapshot'
      WHEN 'snapshot-verified' THEN v_event->'verification'
    END;
    IF v_kind <> 'snapshot-verified' AND (
         jsonb_typeof(v_payload->'operationId') <> 'string'
         OR v_payload->>'operationId' <> v_operation_id::text
         OR jsonb_typeof(v_payload->'stage') <> 'string'
         OR v_payload->>'stage' <> 'committed'
       ) THEN
      RAISE EXCEPTION 'Qualification Evidence accepted payload is not bound to its committed operation' USING ERRCODE = 'WQE03';
    END IF;
  END IF;

  -- Serialize every global EventID, OperationID, and orchestration/version
  -- decision. Trusted event time is sampled only after these waiting locks.
  LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;

  SELECT * INTO v_existing FROM qualification_evidence_events WHERE event_id = v_event_id;
  IF FOUND THEN
    IF v_existing.orchestration_id <> v_orchestration_id
       OR v_existing.expected_version <> v_expected_version
       OR v_existing.request_hash <> p_request_hash
       OR v_existing.request_bytes <> p_request_bytes
       OR v_existing.request_document <> p_request_document
       OR v_existing.event_hash <> p_event_hash
       OR v_existing.event_bytes <> p_event_bytes
       OR v_existing.event_document <> p_event_document THEN
      RAISE EXCEPTION 'Qualification Evidence EventID is bound to different exact bytes' USING ERRCODE = 'WQE02';
    END IF;
    RETURN false;
  END IF;

  SELECT * INTO v_head FROM qualification_evidence_heads
  WHERE orchestration_id = v_orchestration_id FOR UPDATE;
  IF FOUND AND v_kind = 'reserved' THEN
    IF v_expected_version = 0
       AND v_head.plan_document = v_event->'plan'
       AND v_head.command_hash = v_event->>'commandHash'
       AND v_head.trust_bindings_digest = v_event->>'trustBindingsDigest' THEN
      RETURN false;
    END IF;
    RAISE EXCEPTION 'Qualification Evidence orchestration is bound to another Plan or command' USING ERRCODE = 'WQE02';
  ELSIF NOT FOUND AND v_kind <> 'reserved' THEN
    RAISE EXCEPTION 'Qualification Evidence orchestration does not exist' USING ERRCODE = 'WQE04';
  END IF;

  v_now := date_trunc('milliseconds', clock_timestamp());
  IF v_requested_at < v_now - interval '30 seconds' OR v_requested_at > v_now + interval '30 seconds' THEN
    RAISE EXCEPTION 'Qualification Evidence caller time is outside the post-lock trusted-time fence' USING ERRCODE = 'WQE03';
  END IF;

  IF v_kind = 'reserved' THEN
    v_plan := v_event->'plan';
    IF v_expected_version <> 0
       OR NOT (v_plan ?& ARRAY[
         'schemaVersion','orchestrationId','runId','fixtureId','qualificationPlanArtifactId',
         'planDigest','sourceTreeDigest','templateReleaseDigest','operations','credentialSet',
         'artifacts','recipient','outputs'
       ])
       OR v_plan - ARRAY[
         'schemaVersion','orchestrationId','runId','fixtureId','qualificationPlanArtifactId',
         'planDigest','sourceTreeDigest','templateReleaseDigest','operations','credentialSet',
         'artifacts','recipient','outputs'
       ] <> '{}'::jsonb
       OR v_plan->>'schemaVersion' <> 'worksflow-qualification-evidence-plan/v1'
       OR v_plan->>'orchestrationId' <> v_orchestration_id::text
       OR jsonb_typeof(v_plan->'operations') <> 'object'
       OR jsonb_typeof(v_plan->'credentialSet') <> 'object'
       OR jsonb_typeof(v_plan->'artifacts') <> 'array'
       OR jsonb_typeof(v_plan->'recipient') <> 'object'
       OR jsonb_typeof(v_plan->'outputs') <> 'object'
       OR NOT ((v_plan->'operations') ?& ARRAY[
         'reserve','credentialIssue','runClosure','kmsAttestation','credentialRevocation',
         'artifactIndex','receiptSign','snapshotSeal'
       ])
       OR (v_plan->'operations') - ARRAY[
         'reserve','credentialIssue','runClosure','kmsAttestation','credentialRevocation',
         'artifactIndex','receiptSign','snapshotSeal'
       ] <> '{}'::jsonb
       OR v_plan->'operations'->>'reserve' <> v_operation_id::text
       OR jsonb_array_length(v_plan->'artifacts') NOT BETWEEN 1 AND 512 THEN
      RAISE EXCEPTION 'Qualification Evidence Plan root or operation shape is invalid' USING ERRCODE = 'WQE03';
    END IF;
    IF EXISTS (
      SELECT 1
      FROM jsonb_array_elements(v_plan->'artifacts') WITH ORDINALITY AS artifact(value, ordinal)
      WHERE jsonb_typeof(value) <> 'object'
         OR NOT (value ?& ARRAY['id','kind','classification','encryptionOperationId'])
         OR value - ARRAY['id','kind','classification','encryptionOperationId'] <> '{}'::jsonb
         OR jsonb_typeof(value->'id') <> 'string'
         OR value->>'id' !~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
         OR octet_length(value->>'id') > 128
         OR value->>'kind' NOT IN (
           'run-result','trace','video','log','golden-document','fault-evidence','writer-drain-proof','runtime-proof'
         )
         OR value->>'classification' NOT IN ('distributable','restricted')
         OR ((value->>'kind' IN ('trace','video','log')) <> (value->>'classification' = 'restricted'))
         OR (
           value->>'classification' = 'restricted'
           AND value->>'encryptionOperationId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
         )
         OR (value->>'classification' = 'distributable' AND value->>'encryptionOperationId' <> '')
    ) OR EXISTS (
      SELECT 1 FROM (
        SELECT value->>'id' AS artifact_id,
               lag(value->>'id') OVER (ORDER BY ordinal) AS previous_id
        FROM jsonb_array_elements(v_plan->'artifacts') WITH ORDINALITY AS artifact(value, ordinal)
      ) AS ordered
      WHERE pg_catalog.convert_to(previous_id, 'UTF8') >= pg_catalog.convert_to(artifact_id, 'UTF8')
    ) OR NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements(v_plan->'artifacts') AS artifact
      WHERE artifact->>'kind' = 'trace' AND artifact->>'classification' = 'restricted'
    ) OR NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements(v_plan->'artifacts') AS artifact
      WHERE artifact->>'kind' = 'video' AND artifact->>'classification' = 'restricted'
    ) THEN
      RAISE EXCEPTION 'Qualification Evidence Plan artifact closure is invalid' USING ERRCODE = 'WQE03';
    END IF;

    WITH identities(value) AS (
      SELECT v_orchestration_id::text
      UNION ALL SELECT v_plan->>'runId'
      UNION ALL SELECT v_plan->>'fixtureId'
      UNION ALL SELECT v_plan->'credentialSet'->>'setId'
      UNION ALL SELECT operation.value
        FROM jsonb_each_text(v_plan->'operations') AS operation
      UNION ALL SELECT artifact->>'encryptionOperationId'
        FROM jsonb_array_elements(v_plan->'artifacts') AS artifact
        WHERE artifact->>'classification' = 'restricted'
    )
    SELECT count(*), count(DISTINCT value)
    INTO v_identity_count, v_distinct_identity_count FROM identities;
    IF v_identity_count <> v_distinct_identity_count
       OR EXISTS (
         SELECT 1 FROM jsonb_each_text(v_plan->'operations') AS operation
         WHERE operation.value !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       ) THEN
      RAISE EXCEPTION 'Qualification Evidence Plan identities are not globally distinct UUIDv4 values' USING ERRCODE = 'WQE03';
    END IF;

    FOR v_operation_row IN
      SELECT * FROM (VALUES
        ((v_plan->'operations'->>'reserve')::uuid, 'reserve'::text, ''::text),
        ((v_plan->'operations'->>'credentialIssue')::uuid, 'credential-issue'::text, ''::text),
        ((v_plan->'operations'->>'runClosure')::uuid, 'run-closure'::text, ''::text),
        ((v_plan->'operations'->>'kmsAttestation')::uuid, 'kms-attestation'::text, ''::text),
        ((v_plan->'operations'->>'credentialRevocation')::uuid, 'credential-revocation'::text, ''::text),
        ((v_plan->'operations'->>'artifactIndex')::uuid, 'artifact-index'::text, ''::text),
        ((v_plan->'operations'->>'receiptSign')::uuid, 'receipt-sign'::text, ''::text),
        ((v_plan->'operations'->>'snapshotSeal')::uuid, 'snapshot-seal'::text, ''::text)
      ) AS fixed(operation_id, operation_kind, artifact_id)
      UNION ALL
      SELECT (artifact->>'encryptionOperationId')::uuid, 'encryption', artifact->>'id'
      FROM jsonb_array_elements(v_plan->'artifacts') AS artifact
      WHERE artifact->>'classification' = 'restricted'
    LOOP
      SELECT * INTO v_operation FROM qualification_evidence_operations
      WHERE operation_id = v_operation_row.operation_id;
      IF FOUND THEN
        RAISE EXCEPTION 'Qualification Evidence OperationID is already globally bound' USING ERRCODE = 'WQE02';
      END IF;
    END LOOP;

    INSERT INTO qualification_evidence_events (
      event_id, orchestration_id, version, expected_version, event_kind, operation_id,
      active_artifact_id, event_at, requested_at, request_hash, request_bytes,
      request_document, event_hash, event_bytes, event_document
    ) VALUES (
      v_event_id, v_orchestration_id, 1, 0, v_kind, v_operation_id,
      '', v_now, v_requested_at, p_request_hash, p_request_bytes,
      p_request_document, p_event_hash, p_event_bytes, p_event_document
    );
    FOR v_operation_row IN
      SELECT * FROM (VALUES
        ((v_plan->'operations'->>'reserve')::uuid, 'reserve'::text, ''::text),
        ((v_plan->'operations'->>'credentialIssue')::uuid, 'credential-issue'::text, ''::text),
        ((v_plan->'operations'->>'runClosure')::uuid, 'run-closure'::text, ''::text),
        ((v_plan->'operations'->>'kmsAttestation')::uuid, 'kms-attestation'::text, ''::text),
        ((v_plan->'operations'->>'credentialRevocation')::uuid, 'credential-revocation'::text, ''::text),
        ((v_plan->'operations'->>'artifactIndex')::uuid, 'artifact-index'::text, ''::text),
        ((v_plan->'operations'->>'receiptSign')::uuid, 'receipt-sign'::text, ''::text),
        ((v_plan->'operations'->>'snapshotSeal')::uuid, 'snapshot-seal'::text, ''::text)
      ) AS fixed(operation_id, operation_kind, artifact_id)
      UNION ALL
      SELECT (artifact->>'encryptionOperationId')::uuid, 'encryption', artifact->>'id'
      FROM jsonb_array_elements(v_plan->'artifacts') AS artifact
      WHERE artifact->>'classification' = 'restricted'
    LOOP
      INSERT INTO qualification_evidence_operations (
        operation_id, orchestration_id, operation_kind, artifact_id, reservation_event_id, reserved_at
      ) VALUES (
        v_operation_row.operation_id, v_orchestration_id, v_operation_row.operation_kind,
        v_operation_row.artifact_id, v_event_id, v_now
      );
    END LOOP;
    INSERT INTO qualification_evidence_projection_authorizations(transaction_id, backend_pid)
    VALUES (txid_current(), pg_backend_pid());
    INSERT INTO qualification_evidence_heads (
      orchestration_id, version, phase, last_event_id, last_event_at, command_hash,
      trust_bindings_digest, active_operation_id, active_artifact_id, plan_document
    ) VALUES (
      v_orchestration_id, 1, 'reserved', v_event_id, v_now, v_event->>'commandHash',
      v_event->>'trustBindingsDigest', NULL, '', v_plan
    );
    DELETE FROM qualification_evidence_projection_authorizations
    WHERE transaction_id = txid_current() AND backend_pid = pg_backend_pid();
    RETURN true;
  END IF;

  IF v_head.version <> v_expected_version THEN
    RAISE EXCEPTION 'Qualification Evidence expected version does not match current head' USING ERRCODE = 'WQE01';
  END IF;
  IF v_requested_at < v_head.last_event_at THEN
    RAISE EXCEPTION 'Qualification Evidence event time regressed' USING ERRCODE = 'WQE03';
  END IF;

  SELECT * INTO v_operation FROM qualification_evidence_operations WHERE operation_id = v_operation_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Evidence event uses an unreserved OperationID' USING ERRCODE = 'WQE03';
  ELSIF v_operation.orchestration_id <> v_orchestration_id THEN
    RAISE EXCEPTION 'Qualification Evidence OperationID is globally bound to another orchestration' USING ERRCODE = 'WQE02';
  END IF;
  v_operation_kind := CASE
    WHEN v_kind IN ('credential-issue-started','credential-issued') THEN 'credential-issue'
    WHEN v_kind IN ('run-closure-started','run-closure-accepted') THEN 'run-closure'
    WHEN v_kind IN ('encryption-started','encryption-committed') THEN 'encryption'
    WHEN v_kind IN ('kms-attestation-started','kms-attested') THEN 'kms-attestation'
    WHEN v_kind IN ('credential-revocation-started','credential-revoked') THEN 'credential-revocation'
    WHEN v_kind IN ('artifact-index-started','artifact-indexed') THEN 'artifact-index'
    WHEN v_kind IN ('receipt-sign-started','receipt-signed') THEN 'receipt-sign'
    WHEN v_kind IN ('snapshot-seal-started','snapshot-sealed','snapshot-verified') THEN 'snapshot-seal'
  END;
  IF v_operation.operation_kind <> v_operation_kind THEN
    RAISE EXCEPTION 'Qualification Evidence OperationID is bound to another event family' USING ERRCODE = 'WQE02';
  END IF;
  v_artifact_id := v_operation.artifact_id;
  IF v_kind = 'encryption-committed' AND v_payload->>'artifactId' <> v_artifact_id THEN
    RAISE EXCEPTION 'Qualification Evidence encryption operation is bound to another active artifact' USING ERRCODE = 'WQE02';
  END IF;

  v_next_active_operation := NULL;
  IF v_kind = 'credential-issue-started' AND v_head.phase = 'reserved' AND v_head.active_operation_id IS NULL THEN
    v_next_phase := 'credential-issue-started'; v_next_active_operation := v_operation_id;
  ELSIF v_kind = 'credential-issued' AND v_head.phase = 'credential-issue-started' AND v_head.active_operation_id = v_operation_id THEN
    v_next_phase := 'credential-issued';
  ELSIF v_kind = 'run-closure-started' AND v_head.phase = 'credential-issued' AND v_head.active_operation_id IS NULL THEN
    v_next_phase := 'run-closure-started'; v_next_active_operation := v_operation_id;
  ELSIF v_kind = 'run-closure-accepted' AND v_head.phase = 'run-closure-started' AND v_head.active_operation_id = v_operation_id THEN
    v_next_phase := 'run-closure-accepted';
  ELSIF v_kind = 'encryption-started' AND v_head.phase IN ('run-closure-accepted','encrypting')
        AND v_head.active_operation_id IS NULL
        AND NOT EXISTS (
          SELECT 1 FROM qualification_evidence_events
          WHERE orchestration_id = v_orchestration_id AND operation_id = v_operation_id
        ) THEN
    v_next_phase := 'encrypting'; v_next_active_operation := v_operation_id; v_next_active_artifact := v_artifact_id;
  ELSIF v_kind = 'encryption-committed' AND v_head.phase = 'encrypting'
        AND v_head.active_operation_id = v_operation_id AND v_head.active_artifact_id = v_artifact_id THEN
    v_next_phase := 'encrypting';
  ELSIF v_kind = 'kms-attestation-started' AND v_head.phase = 'encrypting' AND v_head.active_operation_id IS NULL
        AND NOT EXISTS (
          SELECT 1 FROM qualification_evidence_operations AS operation
          WHERE operation.orchestration_id = v_orchestration_id
            AND operation.operation_kind = 'encryption'
            AND NOT EXISTS (
              SELECT 1 FROM qualification_evidence_events AS event
              WHERE event.orchestration_id = v_orchestration_id
                AND event.operation_id = operation.operation_id
                AND event.event_kind = 'encryption-committed'
            )
        ) THEN
    v_next_phase := 'kms-attestation-started'; v_next_active_operation := v_operation_id;
  ELSIF v_kind = 'kms-attested' AND v_head.phase = 'kms-attestation-started' AND v_head.active_operation_id = v_operation_id THEN
    v_next_phase := 'kms-attested';
  ELSIF v_kind = 'credential-revocation-started' AND v_head.phase = 'kms-attested' AND v_head.active_operation_id IS NULL THEN
    v_next_phase := 'credential-revocation-started'; v_next_active_operation := v_operation_id;
  ELSIF v_kind = 'credential-revoked' AND v_head.phase = 'credential-revocation-started' AND v_head.active_operation_id = v_operation_id THEN
    v_next_phase := 'credential-revoked';
  ELSIF v_kind = 'artifact-index-started' AND v_head.phase = 'credential-revoked' AND v_head.active_operation_id IS NULL THEN
    v_next_phase := 'artifact-index-started'; v_next_active_operation := v_operation_id;
  ELSIF v_kind = 'artifact-indexed' AND v_head.phase = 'artifact-index-started' AND v_head.active_operation_id = v_operation_id THEN
    v_next_phase := 'artifact-indexed';
  ELSIF v_kind = 'receipt-sign-started' AND v_head.phase = 'artifact-indexed' AND v_head.active_operation_id IS NULL THEN
    v_next_phase := 'receipt-sign-started'; v_next_active_operation := v_operation_id;
  ELSIF v_kind = 'receipt-signed' AND v_head.phase = 'receipt-sign-started' AND v_head.active_operation_id = v_operation_id THEN
    v_next_phase := 'receipt-signed';
  ELSIF v_kind = 'snapshot-seal-started' AND v_head.phase = 'receipt-signed' AND v_head.active_operation_id IS NULL THEN
    v_next_phase := 'snapshot-seal-started'; v_next_active_operation := v_operation_id;
  ELSIF v_kind = 'snapshot-sealed' AND v_head.phase = 'snapshot-seal-started' AND v_head.active_operation_id = v_operation_id THEN
    v_next_phase := 'snapshot-sealed';
  ELSIF v_kind = 'snapshot-verified' AND v_head.phase = 'snapshot-sealed' AND v_head.active_operation_id IS NULL THEN
    v_next_phase := 'complete';
  ELSE
    RAISE EXCEPTION 'Qualification Evidence phase, operation, or active artifact transition is invalid' USING ERRCODE = 'WQE03';
  END IF;

  INSERT INTO qualification_evidence_events (
    event_id, orchestration_id, version, expected_version, event_kind, operation_id,
    active_artifact_id, event_at, requested_at, request_hash, request_bytes,
    request_document, event_hash, event_bytes, event_document
  ) VALUES (
    v_event_id, v_orchestration_id, v_expected_version + 1, v_expected_version, v_kind, v_operation_id,
    v_artifact_id, v_now, v_requested_at, p_request_hash, p_request_bytes,
    p_request_document, p_event_hash, p_event_bytes, p_event_document
  );
  INSERT INTO qualification_evidence_projection_authorizations(transaction_id, backend_pid)
  VALUES (txid_current(), pg_backend_pid());
  UPDATE qualification_evidence_heads
  SET version = v_expected_version + 1,
      phase = v_next_phase,
      last_event_id = v_event_id,
      last_event_at = v_now,
      active_operation_id = v_next_active_operation,
      active_artifact_id = v_next_active_artifact
  WHERE orchestration_id = v_orchestration_id;
  DELETE FROM qualification_evidence_projection_authorizations
  WHERE transaction_id = txid_current() AND backend_pid = pg_backend_pid();
  RETURN true;
END;
$function$;

DO $qualification_evidence_security_posture$
DECLARE
  schema_name text := current_schema();
  role_name text;
BEGIN
  EXECUTE format(
    'ALTER FUNCTION %I.qualification_evidence_sha256(bytea) SET search_path TO pg_catalog',
    schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.reject_qualification_evidence_immutable_mutation() SET search_path TO pg_catalog',
    schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.guard_qualification_evidence_head_projection() SET search_path TO pg_catalog, %I',
    schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb) '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb) FROM PUBLIC',
    schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.qualification_evidence_sha256(bytea) FROM PUBLIC', schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.reject_qualification_evidence_immutable_mutation() FROM PUBLIC', schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.guard_qualification_evidence_head_projection() FROM PUBLIC', schema_name
  );
  EXECUTE format(
    'REVOKE ALL ON TABLE %I.qualification_evidence_events, %I.qualification_evidence_operations, '
    '%I.qualification_evidence_heads, %I.qualification_evidence_projection_authorizations FROM PUBLIC',
    schema_name, schema_name, schema_name, schema_name
  );
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format('ALTER TABLE %I.qualification_evidence_events OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_evidence_operations OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_evidence_heads OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_evidence_projection_authorizations OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.qualification_evidence_sha256(bytea) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.reject_qualification_evidence_immutable_mutation() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.guard_qualification_evidence_head_projection() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format(
      'ALTER FUNCTION %I.append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb) OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;
  FOREACH role_name IN ARRAY ARRAY[
    'worksflow_application','worksflow_schema_migrator','worksflow_auditor','worksflow_qualification_promotion_operator'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
      EXECUTE format(
        'REVOKE ALL ON TABLE %I.qualification_evidence_events, %I.qualification_evidence_operations, '
        '%I.qualification_evidence_heads, %I.qualification_evidence_projection_authorizations FROM %I',
        schema_name, schema_name, schema_name, schema_name, role_name
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.append_qualification_evidence_event(text,bytea,jsonb,text,bytea,jsonb) FROM %I',
        schema_name, role_name
      );
    END IF;
  END LOOP;
END;
$qualification_evidence_security_posture$;
