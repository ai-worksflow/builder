-- Durable non-secret CredentialSet authority. Public DSSE payload/envelope
-- bytes and one-way bindings are the only bounded blobs accepted. There is no
-- metadata, member-level credential, token, cookie, header, capability, or
-- path column.
DO $credential_set_hash_function$
DECLARE
  schema_name text := current_schema();
  extension_schema text;
BEGIN
  SELECT n.nspname INTO extension_schema
  FROM pg_catalog.pg_extension AS e
  JOIN pg_catalog.pg_namespace AS n ON n.oid = e.extnamespace
  WHERE e.extname = 'pgcrypto';
  IF extension_schema IS NULL THEN
    RAISE EXCEPTION 'migration 000072 requires pgcrypto';
  END IF;
  EXECUTE format(
    'CREATE FUNCTION %I.credential_set_sha256(bytea) RETURNS text '
    'LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS %L',
    schema_name,
    format('SELECT ''sha256:'' || pg_catalog.encode(%I.digest($1, ''sha256''), ''hex'')', extension_schema)
  );
END;
$credential_set_hash_function$;

CREATE TABLE credential_set_events (
  event_id uuid PRIMARY KEY,
  set_id uuid NOT NULL,
  version bigint NOT NULL CHECK (version > 0),
  event_kind text NOT NULL CHECK (event_kind IN (
    'issue-reserved','prepare-started','prepared','activation-started','activated',
    'issuance-sign-started','issued','revocation-reserved','revocation-started',
    'revoked','revocation-sign-started','revocation-attested','issue-failed','revocation-failed'
  )),
  operation_id text NOT NULL CHECK (octet_length(operation_id) BETWEEN 1 AND 192),
  event_at timestamptz NOT NULL CHECK (event_at = date_trunc('milliseconds', event_at)),
  requested_at timestamptz NOT NULL CHECK (requested_at = date_trunc('milliseconds', requested_at)),
  issued_at timestamptz CHECK (issued_at IS NULL OR issued_at = date_trunc('milliseconds', issued_at)),
  expires_at timestamptz CHECK (expires_at IS NULL OR expires_at = date_trunc('milliseconds', expires_at)),
  issue_command_hash text NOT NULL CHECK (issue_command_hash = '' OR issue_command_hash ~ '^sha256:[0-9a-f]{64}$'),
  revocation_command_hash text NOT NULL CHECK (revocation_command_hash = '' OR revocation_command_hash ~ '^sha256:[0-9a-f]{64}$'),
  revoked_at timestamptz CHECK (revoked_at IS NULL OR revoked_at = date_trunc('milliseconds', revoked_at)),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (octet_length(request_bytes) BETWEEN 1 AND 16384),
  request_document jsonb NOT NULL CHECK (jsonb_typeof(request_document) = 'object'),
  binding_hash text CHECK (binding_hash IS NULL OR binding_hash ~ '^sha256:[0-9a-f]{64}$'),
  binding_bytes bytea CHECK (binding_bytes IS NULL OR octet_length(binding_bytes) BETWEEN 1 AND 131072),
  binding_document jsonb CHECK (binding_document IS NULL OR jsonb_typeof(binding_document) = 'object'),
  envelope_digest text CHECK (envelope_digest IS NULL OR envelope_digest ~ '^sha256:[0-9a-f]{64}$'),
  envelope_bytes bytea CHECK (envelope_bytes IS NULL OR octet_length(envelope_bytes) BETWEEN 1 AND 393216),
  envelope_document jsonb CHECK (envelope_document IS NULL OR jsonb_typeof(envelope_document) = 'object'),
  attestation_key_id text CHECK (
    attestation_key_id IS NULL OR (
      octet_length(attestation_key_id) BETWEEN 1 AND 128
      AND attestation_key_id ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
    )
  ),
  payload_digest text CHECK (payload_digest IS NULL OR payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  payload_bytes bytea CHECK (payload_bytes IS NULL OR octet_length(payload_bytes) BETWEEN 1 AND 262144),
  payload_document jsonb CHECK (payload_document IS NULL OR jsonb_typeof(payload_document) = 'object'),
  UNIQUE (set_id, version),
  CHECK ((binding_hash IS NULL) = (binding_bytes IS NULL) AND (binding_hash IS NULL) = (binding_document IS NULL)),
  CHECK (
    (envelope_digest IS NULL) = (envelope_bytes IS NULL)
    AND (envelope_digest IS NULL) = (envelope_document IS NULL)
    AND (envelope_digest IS NULL) = (attestation_key_id IS NULL)
    AND (envelope_digest IS NULL) = (payload_digest IS NULL)
    AND (envelope_digest IS NULL) = (payload_bytes IS NULL)
    AND (envelope_digest IS NULL) = (payload_document IS NULL)
  )
);

CREATE TABLE credential_set_operations (
  operation_id uuid PRIMARY KEY,
  set_id uuid NOT NULL,
  operation_kind text NOT NULL CHECK (operation_kind IN ('issue','revoke')),
  command_hash text NOT NULL CHECK (command_hash ~ '^sha256:[0-9a-f]{64}$'),
  reservation_event_id uuid NOT NULL UNIQUE REFERENCES credential_set_events(event_id) ON DELETE RESTRICT,
  reserved_at timestamptz NOT NULL CHECK (reserved_at = date_trunc('milliseconds', reserved_at)),
  UNIQUE (set_id, operation_kind)
);

CREATE TABLE credential_set_heads (
  set_id uuid PRIMARY KEY,
  version bigint NOT NULL CHECK (version > 0),
  phase text NOT NULL CHECK (phase IN (
    'issue-reserved','prepare-started','prepared','activation-started','activated',
    'issuance-sign-started','issued','revocation-reserved','revocation-started',
    'revoked','revocation-sign-started','complete','issue-failed','revocation-failed'
  )),
  last_event_id uuid NOT NULL UNIQUE,
  last_event_at timestamptz NOT NULL CHECK (last_event_at = date_trunc('milliseconds', last_event_at)),
  issue_operation_id uuid NOT NULL UNIQUE,
  issue_command_hash text NOT NULL CHECK (issue_command_hash ~ '^sha256:[0-9a-f]{64}$'),
  issued_at timestamptz NOT NULL CHECK (issued_at = date_trunc('milliseconds', issued_at)),
  expires_at timestamptz NOT NULL CHECK (
    expires_at = date_trunc('milliseconds', expires_at)
    AND expires_at > issued_at
    AND expires_at <= issued_at + interval '30 minutes'
  ),
  binding_hash text CHECK (binding_hash IS NULL OR binding_hash ~ '^sha256:[0-9a-f]{64}$'),
  binding_bytes bytea CHECK (binding_bytes IS NULL OR octet_length(binding_bytes) BETWEEN 1 AND 131072),
  issue_envelope_digest text CHECK (issue_envelope_digest IS NULL OR issue_envelope_digest ~ '^sha256:[0-9a-f]{64}$'),
  issue_envelope_bytes bytea CHECK (issue_envelope_bytes IS NULL OR octet_length(issue_envelope_bytes) BETWEEN 1 AND 393216),
  issue_attestation_key_id text CHECK (issue_attestation_key_id IS NULL OR octet_length(issue_attestation_key_id) BETWEEN 1 AND 128),
  issue_payload_digest text CHECK (issue_payload_digest IS NULL OR issue_payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  issue_payload_bytes bytea CHECK (issue_payload_bytes IS NULL OR octet_length(issue_payload_bytes) BETWEEN 1 AND 262144),
  revocation_operation_id uuid UNIQUE,
  revocation_command_hash text CHECK (revocation_command_hash IS NULL OR revocation_command_hash ~ '^sha256:[0-9a-f]{64}$'),
  revoked_at timestamptz CHECK (revoked_at IS NULL OR revoked_at = date_trunc('milliseconds', revoked_at)),
  revocation_envelope_digest text CHECK (revocation_envelope_digest IS NULL OR revocation_envelope_digest ~ '^sha256:[0-9a-f]{64}$'),
  revocation_envelope_bytes bytea CHECK (revocation_envelope_bytes IS NULL OR octet_length(revocation_envelope_bytes) BETWEEN 1 AND 393216),
  revocation_attestation_key_id text CHECK (revocation_attestation_key_id IS NULL OR octet_length(revocation_attestation_key_id) BETWEEN 1 AND 128),
  revocation_payload_digest text CHECK (revocation_payload_digest IS NULL OR revocation_payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  revocation_payload_bytes bytea CHECK (revocation_payload_bytes IS NULL OR octet_length(revocation_payload_bytes) BETWEEN 1 AND 262144),
  CHECK ((binding_hash IS NULL) = (binding_bytes IS NULL)),
  CHECK (
    (issue_envelope_digest IS NULL) = (issue_envelope_bytes IS NULL)
    AND (issue_envelope_digest IS NULL) = (issue_attestation_key_id IS NULL)
    AND (issue_envelope_digest IS NULL) = (issue_payload_digest IS NULL)
    AND (issue_envelope_digest IS NULL) = (issue_payload_bytes IS NULL)
  ),
  CHECK (
    (revocation_envelope_digest IS NULL) = (revocation_envelope_bytes IS NULL)
    AND (revocation_envelope_digest IS NULL) = (revocation_attestation_key_id IS NULL)
    AND (revocation_envelope_digest IS NULL) = (revocation_payload_digest IS NULL)
    AND (revocation_envelope_digest IS NULL) = (revocation_payload_bytes IS NULL)
  )
);

-- This table is private, ephemeral transaction authorization, not audit data.
-- It lets the trigger reject direct head projection writes, including writes
-- by roles that accidentally receive table DML later.
CREATE TABLE credential_set_projection_authorizations (
  transaction_id bigint NOT NULL,
  backend_pid integer NOT NULL,
  PRIMARY KEY (transaction_id, backend_pid)
);

CREATE FUNCTION reject_credential_set_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'CredentialSet event and operation ledgers are immutable'
    USING ERRCODE = 'WSC03';
END;
$function$;

CREATE TRIGGER credential_set_events_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON credential_set_events
FOR EACH STATEMENT EXECUTE FUNCTION reject_credential_set_immutable_mutation();

CREATE TRIGGER credential_set_operations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON credential_set_operations
FOR EACH STATEMENT EXECUTE FUNCTION reject_credential_set_immutable_mutation();

CREATE FUNCTION guard_credential_set_head_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM credential_set_projection_authorizations
    WHERE transaction_id = txid_current() AND backend_pid = pg_backend_pid()
  ) THEN
    RAISE EXCEPTION 'CredentialSet head projection may move only through append_credential_set_event'
      USING ERRCODE = 'WSC03';
  END IF;
  RETURN NULL;
END;
$function$;

CREATE TRIGGER credential_set_heads_guard
BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON credential_set_heads
FOR EACH STATEMENT EXECUTE FUNCTION guard_credential_set_head_projection();

CREATE FUNCTION append_credential_set_event(
  p_request_hash text,
  p_request_bytes bytea,
  p_request_document jsonb,
  p_binding_hash text,
  p_binding_bytes bytea,
  p_binding_document jsonb,
  p_envelope_digest text,
  p_envelope_bytes bytea,
  p_envelope_document jsonb,
  p_payload_digest text,
  p_payload_bytes bytea,
  p_payload_document jsonb
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_event jsonb;
  v_attestation jsonb;
  v_binding_ref jsonb;
  v_existing credential_set_events%ROWTYPE;
  v_head credential_set_heads%ROWTYPE;
  v_set_id uuid;
  v_event_id uuid;
  v_expected_version bigint;
  v_kind text;
  v_operation_id text;
  v_requested_at timestamptz;
  v_issued_at timestamptz;
  v_expires_at timestamptz;
  v_binding_issued_at timestamptz;
  v_binding_expires_at timestamptz;
  v_revoked_at timestamptz;
  v_issue_hash text;
  v_revoke_hash text;
  v_now timestamptz;
  v_next_phase text;
  v_binding_keys text[] := ARRAY[
    'audience','expiresAt','fixtureId','issuedAt','issuer','memberBindingsDigest',
    'memberCount','members','runId','setHandleHash','setId'
  ];
  v_member jsonb;
  v_predicate jsonb;
  v_signature jsonb;
  v_head_binding jsonb;
  v_member_bindings_canonical text;
BEGIN
  IF p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_request_bytes IS NULL OR octet_length(p_request_bytes) NOT BETWEEN 1 AND 16384
     OR get_byte(p_request_bytes, 0) <> 123 OR get_byte(p_request_bytes, octet_length(p_request_bytes) - 1) <> 125
     OR p_request_document IS NULL OR jsonb_typeof(p_request_document) <> 'object'
     OR credential_set_sha256(p_request_bytes) <> p_request_hash
     OR convert_from(p_request_bytes, 'UTF8')::jsonb <> p_request_document
     OR NOT (p_request_document ?& ARRAY['event','expectedVersion','schemaVersion','setId'])
     OR p_request_document - ARRAY['event','expectedVersion','schemaVersion','setId'] <> '{}'::jsonb
     OR jsonb_typeof(p_request_document->'schemaVersion') <> 'string'
     OR jsonb_typeof(p_request_document->'setId') <> 'string'
     OR p_request_document->>'schemaVersion' <> 'worksflow-credential-set-event-request/v1'
     OR jsonb_typeof(p_request_document->'expectedVersion') <> 'number'
     OR p_request_document->>'expectedVersion' !~ '^(0|[1-9][0-9]{0,18})$'
     OR jsonb_typeof(p_request_document->'event') <> 'object' THEN
    RAISE EXCEPTION 'CredentialSet event request bytes, hash, or root shape is invalid' USING ERRCODE = 'WSC03';
  END IF;
  v_event := p_request_document->'event';
  IF NOT (v_event ?& ARRAY[
       'attestation','binding','eventId','expiresAt','issueCommandHash','issuedAt',
       'kind','operationId','requestedAt','revocationCommandHash','revokedAt'
     ])
     OR v_event - ARRAY[
       'attestation','binding','eventId','expiresAt','issueCommandHash','issuedAt',
       'kind','operationId','requestedAt','revocationCommandHash','revokedAt'
     ] <> '{}'::jsonb
     OR jsonb_typeof(v_event->'eventId') <> 'string'
     OR jsonb_typeof(v_event->'kind') <> 'string'
     OR jsonb_typeof(v_event->'operationId') <> 'string'
     OR jsonb_typeof(v_event->'requestedAt') <> 'string'
     OR jsonb_typeof(v_event->'issuedAt') <> 'string'
     OR jsonb_typeof(v_event->'expiresAt') <> 'string'
     OR jsonb_typeof(v_event->'issueCommandHash') <> 'string'
     OR jsonb_typeof(v_event->'revocationCommandHash') <> 'string'
     OR jsonb_typeof(v_event->'revokedAt') <> 'string' THEN
    RAISE EXCEPTION 'CredentialSet event projection has unknown, nullable, or widened fields' USING ERRCODE = 'WSC03';
  END IF;

  BEGIN
    v_set_id := (p_request_document->>'setId')::uuid;
    v_event_id := (v_event->>'eventId')::uuid;
    v_expected_version := (p_request_document->>'expectedVersion')::bigint;
    v_requested_at := (v_event->>'requestedAt')::timestamptz;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'CredentialSet event scalar identity is malformed' USING ERRCODE = 'WSC03';
  END;
  v_kind := v_event->>'kind';
  v_operation_id := v_event->>'operationId';
  v_issue_hash := v_event->>'issueCommandHash';
  v_revoke_hash := v_event->>'revocationCommandHash';
  IF v_set_id::text <> p_request_document->>'setId'
     OR v_event_id::text <> v_event->>'eventId'
     OR p_request_document->>'setId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_event->>'eventId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR to_char(v_requested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') <> v_event->>'requestedAt'
     OR v_kind NOT IN (
       'issue-reserved','prepare-started','prepared','activation-started','activated',
       'issuance-sign-started','issued','revocation-reserved','revocation-started',
       'revoked','revocation-sign-started','revocation-attested','issue-failed','revocation-failed'
     )
     OR octet_length(v_operation_id) NOT BETWEEN 1 AND 192
     OR v_issue_hash !~ '^(|sha256:[0-9a-f]{64})$'
     OR v_revoke_hash !~ '^(|sha256:[0-9a-f]{64})$' THEN
    RAISE EXCEPTION 'CredentialSet event scalar values are non-canonical' USING ERRCODE = 'WSC03';
  END IF;

  -- Serialize all immutable-ID and set/version decisions. The trusted event
  -- time is sampled only after this potentially waiting lock.
  LOCK TABLE credential_set_events IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE credential_set_heads IN SHARE ROW EXCLUSIVE MODE;

  SELECT * INTO v_existing FROM credential_set_events WHERE event_id = v_event_id;
  IF FOUND THEN
    IF v_existing.request_hash <> p_request_hash
       OR v_existing.request_bytes <> p_request_bytes
       OR v_existing.request_document <> p_request_document
       OR v_existing.binding_hash IS DISTINCT FROM p_binding_hash
       OR v_existing.binding_bytes IS DISTINCT FROM p_binding_bytes
       OR v_existing.binding_document IS DISTINCT FROM p_binding_document
       OR v_existing.envelope_digest IS DISTINCT FROM p_envelope_digest
       OR v_existing.envelope_bytes IS DISTINCT FROM p_envelope_bytes
       OR v_existing.envelope_document IS DISTINCT FROM p_envelope_document
       OR v_existing.payload_digest IS DISTINCT FROM p_payload_digest
       OR v_existing.payload_bytes IS DISTINCT FROM p_payload_bytes
       OR v_existing.payload_document IS DISTINCT FROM p_payload_document THEN
      RAISE EXCEPTION 'CredentialSet event ID is bound to different exact bytes' USING ERRCODE = 'WSC02';
    END IF;
    RETURN false;
  END IF;

  SELECT * INTO v_head FROM credential_set_heads WHERE set_id = v_set_id FOR UPDATE;
  IF v_kind = 'issue-reserved' AND FOUND THEN
    IF v_head.issue_operation_id::text = v_operation_id
       AND v_head.issue_command_hash = v_issue_hash
       AND to_char(v_head.issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') = v_event->>'issuedAt'
       AND to_char(v_head.expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') = v_event->>'expiresAt' THEN
      RETURN false;
    END IF;
    RAISE EXCEPTION 'CredentialSet set ID or issue operation is already bound to different input' USING ERRCODE = 'WSC02';
  ELSIF NOT FOUND AND v_kind <> 'issue-reserved' THEN
    RAISE EXCEPTION 'CredentialSet does not exist' USING ERRCODE = 'WSC04';
  END IF;

  IF v_kind = 'issue-reserved' AND EXISTS (
    SELECT 1 FROM credential_set_operations
    WHERE operation_id = v_operation_id::uuid
      AND (set_id <> v_set_id OR operation_kind <> 'issue' OR command_hash <> v_issue_hash)
  ) THEN
    RAISE EXCEPTION 'CredentialSet issue operation ID is bound to another exact command' USING ERRCODE = 'WSC02';
  END IF;

  v_now := date_trunc('milliseconds', clock_timestamp());
  IF v_requested_at < v_now - interval '30 seconds' OR v_requested_at > v_now + interval '30 seconds' THEN
    RAISE EXCEPTION 'CredentialSet caller time is outside the post-lock trusted-time fence' USING ERRCODE = 'WSC03';
  END IF;

  IF (p_binding_hash IS NULL) <> (p_binding_bytes IS NULL)
     OR (p_binding_hash IS NULL) <> (p_binding_document IS NULL)
     OR (p_envelope_digest IS NULL) <> (p_envelope_bytes IS NULL)
     OR (p_envelope_digest IS NULL) <> (p_envelope_document IS NULL)
     OR (p_envelope_digest IS NULL) <> (p_payload_digest IS NULL)
     OR (p_envelope_digest IS NULL) <> (p_payload_bytes IS NULL)
     OR (p_envelope_digest IS NULL) <> (p_payload_document IS NULL) THEN
    RAISE EXCEPTION 'CredentialSet structured bytes are partially supplied' USING ERRCODE = 'WSC03';
  END IF;

  v_binding_ref := v_event->'binding';
  IF p_binding_hash IS NULL THEN
    IF jsonb_typeof(v_binding_ref) <> 'null' THEN
      RAISE EXCEPTION 'CredentialSet event unexpectedly references binding bytes' USING ERRCODE = 'WSC03';
    END IF;
  ELSE
    IF p_binding_hash !~ '^sha256:[0-9a-f]{64}$'
       OR octet_length(p_binding_bytes) NOT BETWEEN 1 AND 131072
       OR get_byte(p_binding_bytes, 0) <> 123 OR get_byte(p_binding_bytes, octet_length(p_binding_bytes) - 1) <> 125
       OR credential_set_sha256(p_binding_bytes) <> p_binding_hash
       OR convert_from(p_binding_bytes, 'UTF8')::jsonb <> p_binding_document
       OR p_binding_document <> jsonb_strip_nulls(p_binding_document)
       OR jsonb_typeof(v_binding_ref) <> 'object'
       OR NOT (v_binding_ref ? 'contentHash') OR v_binding_ref - 'contentHash' <> '{}'::jsonb
       OR v_binding_ref->>'contentHash' <> p_binding_hash
       OR NOT (p_binding_document ?& v_binding_keys)
       OR p_binding_document - v_binding_keys <> '{}'::jsonb
       OR jsonb_typeof(p_binding_document->'audience') <> 'string'
       OR jsonb_typeof(p_binding_document->'expiresAt') <> 'string'
       OR jsonb_typeof(p_binding_document->'fixtureId') <> 'string'
       OR jsonb_typeof(p_binding_document->'issuedAt') <> 'string'
       OR jsonb_typeof(p_binding_document->'issuer') <> 'string'
       OR jsonb_typeof(p_binding_document->'memberBindingsDigest') <> 'string'
       OR jsonb_typeof(p_binding_document->'runId') <> 'string'
       OR jsonb_typeof(p_binding_document->'setHandleHash') <> 'string'
       OR jsonb_typeof(p_binding_document->'setId') <> 'string'
       OR p_binding_document->>'setId' <> v_set_id::text
       OR p_binding_document->>'setId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR p_binding_document->>'runId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR p_binding_document->>'fixtureId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR p_binding_document->>'setHandleHash' !~ '^sha256:[0-9a-f]{64}$'
       OR p_binding_document->>'memberBindingsDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR p_binding_document->>'setHandleHash' = p_binding_document->>'memberBindingsDigest'
       OR jsonb_typeof(p_binding_document->'memberCount') <> 'number'
       OR p_binding_document->>'memberCount' !~ '^[1-9][0-9]?$'
       OR (p_binding_document->>'memberCount')::integer NOT BETWEEN 1 AND 64
       OR jsonb_typeof(p_binding_document->'members') <> 'array'
       OR jsonb_array_length(p_binding_document->'members') <> (p_binding_document->>'memberCount')::integer THEN
      RAISE EXCEPTION 'CredentialSet binding bytes, hash, reference, or exact shape is invalid' USING ERRCODE = 'WSC03';
    END IF;
    BEGIN
      PERFORM (p_binding_document->>'setId')::uuid;
      PERFORM (p_binding_document->>'runId')::uuid;
      PERFORM (p_binding_document->>'fixtureId')::uuid;
      v_binding_issued_at := (p_binding_document->>'issuedAt')::timestamptz;
      v_binding_expires_at := (p_binding_document->>'expiresAt')::timestamptz;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'CredentialSet binding identity or time is malformed' USING ERRCODE = 'WSC03';
    END;
    IF p_binding_document->>'issuer' !~ '^(spiffe://[a-z0-9.-]+/[a-z0-9._/-]+|[a-z0-9]+([._/-][a-z0-9]+)*)$'
       OR p_binding_document->>'audience' !~ '^(urn:[a-z0-9][a-z0-9:._/-]+|[a-z0-9]+([._/-][a-z0-9]+)*)$'
       OR octet_length(p_binding_document->>'issuer') > 256
       OR octet_length(p_binding_document->>'audience') > 512
       OR p_binding_document->>'setId' IN (p_binding_document->>'runId', p_binding_document->>'fixtureId')
       OR p_binding_document->>'runId' = p_binding_document->>'fixtureId'
       OR p_binding_document->>'runId' = v_head.issue_operation_id::text
       OR p_binding_document->>'fixtureId' = v_head.issue_operation_id::text
       OR to_char(v_binding_issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') <> p_binding_document->>'issuedAt'
       OR to_char(v_binding_expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') <> p_binding_document->>'expiresAt'
       OR v_binding_expires_at <= v_binding_issued_at OR v_binding_expires_at > v_binding_issued_at + interval '30 minutes'
       OR EXISTS (
         SELECT 1 FROM jsonb_array_elements(p_binding_document->'members') AS member
         WHERE member->>'credentialHandleHash' = p_binding_document->>'setHandleHash'
       )
       OR (SELECT count(DISTINCT member->>'slot') FROM jsonb_array_elements(p_binding_document->'members') AS member)
          <> jsonb_array_length(p_binding_document->'members')
       OR (SELECT count(DISTINCT member->>'credentialHandleHash') FROM jsonb_array_elements(p_binding_document->'members') AS member)
          <> jsonb_array_length(p_binding_document->'members')
       OR EXISTS (
         SELECT 1
         FROM jsonb_array_elements(p_binding_document->'members') WITH ORDINALITY AS left_member(value, position)
         JOIN jsonb_array_elements(p_binding_document->'members') WITH ORDINALITY AS right_member(value, position)
           ON right_member.position = left_member.position + 1
         WHERE (
           convert_to(left_member.value->>'slot','UTF8'), convert_to(left_member.value->>'actorId','UTF8'),
           convert_to(left_member.value->>'kind','UTF8'), convert_to(left_member.value->>'credentialHandleHash','UTF8')
         ) >= (
           convert_to(right_member.value->>'slot','UTF8'), convert_to(right_member.value->>'actorId','UTF8'),
           convert_to(right_member.value->>'kind','UTF8'), convert_to(right_member.value->>'credentialHandleHash','UTF8')
         )
       ) THEN
      RAISE EXCEPTION 'CredentialSet binding authority, lifetime, uniqueness, or ordering is invalid' USING ERRCODE = 'WSC03';
    END IF;
    FOR v_member IN SELECT value FROM jsonb_array_elements(p_binding_document->'members') LOOP
      IF jsonb_typeof(v_member) <> 'object'
         OR NOT (v_member ?& ARRAY['actorId','credentialHandleHash','kind','slot'])
         OR v_member - ARRAY['actorId','credentialHandleHash','kind','slot'] <> '{}'::jsonb
         OR v_member->>'credentialHandleHash' !~ '^sha256:[0-9a-f]{64}$'
         OR v_member->>'kind' NOT IN ('token','storage-state')
         OR v_member->>'slot' !~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
         OR octet_length(v_member->>'slot') > 128
         OR v_member->>'actorId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' THEN
        RAISE EXCEPTION 'CredentialSet binding member has unknown, nullable, or widened fields' USING ERRCODE = 'WSC03';
      END IF;
      PERFORM (v_member->>'actorId')::uuid;
    END LOOP;
    SELECT '{"members":[' || string_agg(
      replace(replace(pg_catalog.json_build_object(
        'actorId', value->>'actorId',
        'credentialHandleHash', value->>'credentialHandleHash',
        'kind', value->>'kind',
        'slot', value->>'slot'
      )::text, ' : ', ':'), ', ', ','), ',' ORDER BY position
    ) || '],"schemaVersion":"worksflow-credential-set-member-bindings/v1"}'
    INTO v_member_bindings_canonical
    FROM jsonb_array_elements(p_binding_document->'members') WITH ORDINALITY AS member(value, position);
    IF credential_set_sha256(convert_to(v_member_bindings_canonical, 'UTF8')) <>
       p_binding_document->>'memberBindingsDigest' THEN
      RAISE EXCEPTION 'CredentialSet canonical member binding digest does not match exact members' USING ERRCODE = 'WSC03';
    END IF;
  END IF;

  v_attestation := v_event->'attestation';
  IF p_envelope_digest IS NULL THEN
    IF jsonb_typeof(v_attestation) <> 'null' THEN
      RAISE EXCEPTION 'CredentialSet event unexpectedly references attestation bytes' USING ERRCODE = 'WSC03';
    END IF;
  ELSE
    IF p_envelope_digest !~ '^sha256:[0-9a-f]{64}$' OR p_payload_digest !~ '^sha256:[0-9a-f]{64}$'
       OR octet_length(p_envelope_bytes) NOT BETWEEN 1 AND 393216
       OR octet_length(p_payload_bytes) NOT BETWEEN 1 AND 262144
       OR get_byte(p_envelope_bytes, 0) <> 123 OR get_byte(p_envelope_bytes, octet_length(p_envelope_bytes)-1) <> 125
       OR get_byte(p_payload_bytes, 0) <> 123 OR get_byte(p_payload_bytes, octet_length(p_payload_bytes)-1) <> 125
       OR credential_set_sha256(p_envelope_bytes) <> p_envelope_digest
       OR credential_set_sha256(p_payload_bytes) <> p_payload_digest
       OR convert_from(p_envelope_bytes, 'UTF8')::jsonb <> p_envelope_document
       OR convert_from(p_payload_bytes, 'UTF8')::jsonb <> p_payload_document
       OR p_envelope_document <> jsonb_strip_nulls(p_envelope_document)
       OR p_payload_document <> jsonb_strip_nulls(p_payload_document)
       OR jsonb_typeof(v_attestation) <> 'object'
       OR NOT (v_attestation ?& ARRAY['envelopeDigest','keyId','payloadDigest'])
       OR v_attestation - ARRAY['envelopeDigest','keyId','payloadDigest'] <> '{}'::jsonb
       OR v_attestation->>'envelopeDigest' <> p_envelope_digest
       OR v_attestation->>'payloadDigest' <> p_payload_digest
       OR v_attestation->>'keyId' !~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
       OR NOT (p_envelope_document ?& ARRAY['payload','payloadType','signatures'])
       OR p_envelope_document - ARRAY['payload','payloadType','signatures'] <> '{}'::jsonb
       OR p_envelope_document->>'payloadType' <> 'application/vnd.in-toto+json'
       OR jsonb_typeof(p_envelope_document->'signatures') <> 'array'
       OR jsonb_array_length(p_envelope_document->'signatures') <> 1
       OR decode(p_envelope_document->>'payload', 'base64') <> p_payload_bytes
       OR replace(encode(decode(p_envelope_document->>'payload', 'base64'), 'base64'), E'\n', '') <> p_envelope_document->>'payload'
       OR NOT (p_payload_document ?& ARRAY['_type','predicate','predicateType','subject'])
       OR p_payload_document - ARRAY['_type','predicate','predicateType','subject'] <> '{}'::jsonb
       OR p_payload_document->>'_type' <> 'https://in-toto.io/Statement/v1'
       OR jsonb_typeof(p_payload_document->'subject') <> 'array'
       OR jsonb_array_length(p_payload_document->'subject') <> 1
       OR jsonb_typeof(p_payload_document->'predicate') <> 'object' THEN
      RAISE EXCEPTION 'CredentialSet attestation bytes, hashes, or exact DSSE shape is invalid' USING ERRCODE = 'WSC03';
    END IF;
    v_signature := p_envelope_document->'signatures'->0;
    IF NOT (v_signature ?& ARRAY['keyid','sig'])
       OR v_signature - ARRAY['keyid','sig'] <> '{}'::jsonb
       OR v_signature->>'keyid' <> v_attestation->>'keyId'
       OR octet_length(decode(v_signature->>'sig', 'base64')) NOT BETWEEN 1 AND 16384
       OR replace(encode(decode(v_signature->>'sig', 'base64'), 'base64'), E'\n', '') <> v_signature->>'sig' THEN
      RAISE EXCEPTION 'CredentialSet DSSE signature has unknown or non-canonical fields' USING ERRCODE = 'WSC03';
    END IF;
    v_predicate := p_payload_document->'predicate';
    IF v_head.binding_bytes IS NULL THEN
      RAISE EXCEPTION 'CredentialSet attestation has no exact stored binding' USING ERRCODE = 'WSC03';
    END IF;
    v_head_binding := convert_from(v_head.binding_bytes, 'UTF8')::jsonb;
    IF NOT ((p_payload_document->'subject'->0) ?& ARRAY['digest','name'])
       OR (p_payload_document->'subject'->0) - ARRAY['digest','name'] <> '{}'::jsonb
       OR jsonb_typeof(p_payload_document->'subject'->0->'digest') <> 'object'
       OR NOT ((p_payload_document->'subject'->0->'digest') ? 'sha256')
       OR (p_payload_document->'subject'->0->'digest') - 'sha256' <> '{}'::jsonb
       OR p_payload_document->'subject'->0->>'name' <>
          'worksflow-credential-set/' || substring(v_head_binding->>'setHandleHash' FROM 8)
       OR p_payload_document->'subject'->0->'digest'->>'sha256' <>
          substring(v_head_binding->>'setHandleHash' FROM 8) THEN
      RAISE EXCEPTION 'CredentialSet attestation subject does not bind the exact set handle' USING ERRCODE = 'WSC03';
    END IF;
  END IF;

  IF v_kind = 'issue-reserved' THEN
    BEGIN
      v_issued_at := (v_event->>'issuedAt')::timestamptz;
      v_expires_at := (v_event->>'expiresAt')::timestamptz;
      PERFORM v_operation_id::uuid;
    EXCEPTION WHEN OTHERS THEN
      RAISE EXCEPTION 'CredentialSet issue reservation window or operation is malformed' USING ERRCODE = 'WSC03';
    END;
    IF v_expected_version <> 0 OR v_issue_hash !~ '^sha256:[0-9a-f]{64}$' OR v_revoke_hash <> ''
       OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL
       OR v_issued_at < v_now - interval '30 seconds' OR v_issued_at > v_now + interval '30 seconds'
       OR v_expires_at <= v_now OR v_expires_at <= v_issued_at OR v_expires_at > v_issued_at + interval '30 minutes'
       OR to_char(v_issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') <> v_event->>'issuedAt'
       OR to_char(v_expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') <> v_event->>'expiresAt'
       OR v_operation_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       OR v_operation_id = v_set_id::text THEN
      RAISE EXCEPTION 'CredentialSet issue reservation is invalid or expired' USING ERRCODE = 'WSC03';
    END IF;
    v_next_phase := 'issue-reserved';
  ELSE
    IF v_expected_version <> v_head.version THEN
      RAISE EXCEPTION 'CredentialSet expected version does not match current head' USING ERRCODE = 'WSC01';
    END IF;
    IF v_event->>'issuedAt' <> '' OR v_event->>'expiresAt' <> '' OR v_issue_hash <> '' THEN
      RAISE EXCEPTION 'CredentialSet non-reservation event widened the issuance window' USING ERRCODE = 'WSC03';
    END IF;
    IF v_head.phase IN ('issue-reserved','prepare-started','prepared','activation-started','activated','issuance-sign-started')
       AND v_now >= v_head.expires_at THEN
      RAISE EXCEPTION 'CredentialSet issuance window expired before completion' USING ERRCODE = 'WSC03';
    END IF;
    CASE v_kind
      WHEN 'prepare-started' THEN
        IF v_head.phase <> 'issue-reserved' OR v_operation_id <> v_head.issue_operation_id::text OR v_revoke_hash <> ''
           OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid prepare-started transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'prepare-started';
      WHEN 'prepared' THEN
        IF v_head.phase <> 'prepare-started' OR v_operation_id <> v_head.issue_operation_id::text OR v_revoke_hash <> ''
           OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NULL OR p_envelope_digest IS NOT NULL
           OR p_binding_document->>'issuedAt' <> to_char(v_head.issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
           OR p_binding_document->>'expiresAt' <> to_char(v_head.expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') THEN
          RAISE EXCEPTION 'invalid prepared transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'prepared';
      WHEN 'activation-started' THEN
        IF v_head.phase <> 'prepared' OR v_operation_id <> v_head.issue_operation_id::text OR v_revoke_hash <> ''
           OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid activation-started transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'activation-started';
      WHEN 'activated' THEN
        IF v_head.phase <> 'activation-started' OR v_operation_id <> v_head.issue_operation_id::text OR v_revoke_hash <> ''
           OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NULL OR p_binding_hash <> v_head.binding_hash OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid activated transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'activated';
      WHEN 'issuance-sign-started' THEN
        IF v_head.phase <> 'activated' OR v_operation_id <> 'credential-sign/' || v_head.issue_operation_id::text || '/attestation'
           OR v_revoke_hash <> '' OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid issuance-sign-started transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'issuance-sign-started';
      WHEN 'issued' THEN
        IF v_head.phase <> 'issuance-sign-started' OR v_operation_id <> 'credential-sign/' || v_head.issue_operation_id::text || '/attestation'
           OR v_revoke_hash <> '' OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NULL
           OR p_payload_document->>'predicateType' <> 'https://worksflow.dev/attestations/credential-set-issuance/v1'
           OR NOT (v_predicate ?& ARRAY[
             'audience','expiresAt','fixtureId','issuedAt','issuer','memberBindingsDigest',
             'memberCount','members','runId','schemaVersion','setHandleHash','status'
           ])
           OR v_predicate - ARRAY[
             'audience','expiresAt','fixtureId','issuedAt','issuer','memberBindingsDigest',
             'memberCount','members','runId','schemaVersion','setHandleHash','status'
           ] <> '{}'::jsonb
           OR v_predicate->>'schemaVersion' <> 'worksflow-credential-set-issuance/v1'
           OR v_predicate->>'status' <> 'issued'
           OR v_predicate->'members' <> v_head_binding->'members'
           OR v_predicate->>'audience' <> v_head_binding->>'audience'
           OR v_predicate->>'expiresAt' <> v_head_binding->>'expiresAt'
           OR v_predicate->>'fixtureId' <> v_head_binding->>'fixtureId'
           OR v_predicate->>'issuedAt' <> v_head_binding->>'issuedAt'
           OR v_predicate->>'issuer' <> v_head_binding->>'issuer'
           OR v_predicate->>'memberBindingsDigest' <> v_head_binding->>'memberBindingsDigest'
           OR v_predicate->'memberCount' <> v_head_binding->'memberCount'
           OR v_predicate->>'runId' <> v_head_binding->>'runId'
           OR v_predicate->>'setHandleHash' <> v_head_binding->>'setHandleHash' THEN
          RAISE EXCEPTION 'invalid issued transition or issuance payload' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'issued';
      WHEN 'revocation-reserved' THEN
        BEGIN v_revoked_at := (v_event->>'revokedAt')::timestamptz; PERFORM v_operation_id::uuid;
        EXCEPTION WHEN OTHERS THEN RAISE EXCEPTION 'malformed revocation reservation' USING ERRCODE = 'WSC03'; END;
        IF v_head.phase <> 'issued' OR v_revoke_hash !~ '^sha256:[0-9a-f]{64}$'
           OR v_operation_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
           OR v_operation_id = v_head.issue_operation_id::text OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL
           OR v_revoked_at <= v_head.issued_at OR v_revoked_at >= v_head.expires_at
           OR v_revoked_at < v_now - interval '30 seconds' OR v_revoked_at > v_now + interval '30 seconds'
           OR to_char(v_revoked_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') <> v_event->>'revokedAt' THEN
          RAISE EXCEPTION 'invalid or expired revocation reservation' USING ERRCODE = 'WSC03'; END IF;
        IF EXISTS (
          SELECT 1 FROM credential_set_operations
          WHERE operation_id = v_operation_id::uuid
            AND (set_id <> v_set_id OR operation_kind <> 'revoke' OR command_hash <> v_revoke_hash)
        ) THEN
          RAISE EXCEPTION 'CredentialSet revoke operation ID is bound to another exact command' USING ERRCODE = 'WSC02';
        END IF;
        v_next_phase := 'revocation-reserved';
      WHEN 'revocation-started' THEN
        IF v_head.phase <> 'revocation-reserved' OR v_operation_id <> v_head.revocation_operation_id::text OR v_revoke_hash <> ''
           OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid revocation-started transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'revocation-started';
      WHEN 'revoked' THEN
        IF v_head.phase <> 'revocation-started' OR v_operation_id <> v_head.revocation_operation_id::text OR v_revoke_hash <> ''
           OR p_binding_hash IS NULL OR p_binding_hash <> v_head.binding_hash OR p_envelope_digest IS NOT NULL
           OR v_event->>'revokedAt' <> to_char(v_head.revoked_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') THEN
          RAISE EXCEPTION 'invalid revoked transition' USING ERRCODE = 'WSC03'; END IF;
        v_revoked_at := v_head.revoked_at; v_next_phase := 'revoked';
      WHEN 'revocation-sign-started' THEN
        IF v_head.phase <> 'revoked' OR v_operation_id <> 'credential-sign/' || v_head.revocation_operation_id::text || '/attestation'
           OR v_revoke_hash <> '' OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid revocation-sign-started transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'revocation-sign-started';
      WHEN 'revocation-attested' THEN
        IF v_head.phase <> 'revocation-sign-started' OR v_operation_id <> 'credential-sign/' || v_head.revocation_operation_id::text || '/attestation'
           OR v_revoke_hash <> '' OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NULL
           OR p_payload_document->>'predicateType' <> 'https://worksflow.dev/attestations/credential-set-revocation/v1'
           OR NOT (v_predicate ?& ARRAY[
             'audience','expiresAt','fixtureId','issuedAt','issuer','memberBindingsDigest',
             'memberCount','members','revokedAt','runId','schemaVersion','setHandleHash','status'
           ])
           OR v_predicate - ARRAY[
             'audience','expiresAt','fixtureId','issuedAt','issuer','memberBindingsDigest',
             'memberCount','members','revokedAt','runId','schemaVersion','setHandleHash','status'
           ] <> '{}'::jsonb
           OR v_predicate->>'schemaVersion' <> 'worksflow-credential-set-revocation/v1'
           OR v_predicate->>'status' <> 'revoked'
           OR v_predicate->'members' <> v_head_binding->'members'
           OR v_predicate->>'audience' <> v_head_binding->>'audience'
           OR v_predicate->>'expiresAt' <> v_head_binding->>'expiresAt'
           OR v_predicate->>'fixtureId' <> v_head_binding->>'fixtureId'
           OR v_predicate->>'issuedAt' <> v_head_binding->>'issuedAt'
           OR v_predicate->>'issuer' <> v_head_binding->>'issuer'
           OR v_predicate->>'memberBindingsDigest' <> v_head_binding->>'memberBindingsDigest'
           OR v_predicate->'memberCount' <> v_head_binding->'memberCount'
           OR v_predicate->>'runId' <> v_head_binding->>'runId'
           OR v_predicate->>'setHandleHash' <> v_head_binding->>'setHandleHash'
           OR v_predicate->>'revokedAt' <> to_char(v_head.revoked_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') THEN
          RAISE EXCEPTION 'invalid revocation-attested transition or payload' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'complete';
      WHEN 'issue-failed' THEN
        IF v_head.phase NOT IN ('prepare-started','activation-started') OR v_operation_id <> v_head.issue_operation_id::text
           OR v_revoke_hash <> '' OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid issue-failed transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'issue-failed';
      WHEN 'revocation-failed' THEN
        IF v_head.phase <> 'revocation-started' OR v_operation_id <> v_head.revocation_operation_id::text
           OR v_revoke_hash <> '' OR v_event->>'revokedAt' <> '' OR p_binding_hash IS NOT NULL OR p_envelope_digest IS NOT NULL THEN
          RAISE EXCEPTION 'invalid revocation-failed transition' USING ERRCODE = 'WSC03'; END IF;
        v_next_phase := 'revocation-failed';
      ELSE
        RAISE EXCEPTION 'unsupported CredentialSet event transition' USING ERRCODE = 'WSC03';
    END CASE;
  END IF;

  INSERT INTO credential_set_events (
    event_id,set_id,version,event_kind,operation_id,event_at,requested_at,issued_at,expires_at,
    issue_command_hash,revocation_command_hash,revoked_at,request_hash,request_bytes,request_document,
    binding_hash,binding_bytes,binding_document,envelope_digest,envelope_bytes,envelope_document,
    attestation_key_id,payload_digest,payload_bytes,payload_document
  ) VALUES (
    v_event_id,v_set_id,CASE WHEN v_kind='issue-reserved' THEN 1 ELSE v_head.version+1 END,
    v_kind,v_operation_id,v_now,v_requested_at,v_issued_at,v_expires_at,v_issue_hash,v_revoke_hash,v_revoked_at,
    p_request_hash,p_request_bytes,p_request_document,p_binding_hash,p_binding_bytes,p_binding_document,
    p_envelope_digest,p_envelope_bytes,p_envelope_document,v_attestation->>'keyId',p_payload_digest,p_payload_bytes,p_payload_document
  );

  IF v_kind = 'issue-reserved' THEN
    INSERT INTO credential_set_operations(operation_id,set_id,operation_kind,command_hash,reservation_event_id,reserved_at)
    VALUES (v_operation_id::uuid,v_set_id,'issue',v_issue_hash,v_event_id,v_now);
  ELSIF v_kind = 'revocation-reserved' THEN
    INSERT INTO credential_set_operations(operation_id,set_id,operation_kind,command_hash,reservation_event_id,reserved_at)
    VALUES (v_operation_id::uuid,v_set_id,'revoke',v_revoke_hash,v_event_id,v_now);
  END IF;

  INSERT INTO credential_set_projection_authorizations(transaction_id,backend_pid)
  VALUES (txid_current(),pg_backend_pid()) ON CONFLICT DO NOTHING;

  IF v_kind = 'issue-reserved' THEN
    INSERT INTO credential_set_heads(
      set_id,version,phase,last_event_id,last_event_at,issue_operation_id,issue_command_hash,issued_at,expires_at
    ) VALUES (v_set_id,1,v_next_phase,v_event_id,v_now,v_operation_id::uuid,v_issue_hash,v_issued_at,v_expires_at);
  ELSE
    UPDATE credential_set_heads SET
      version = version + 1,
      phase = v_next_phase,
      last_event_id = v_event_id,
      last_event_at = v_now,
      binding_hash = CASE WHEN v_kind='prepared' THEN p_binding_hash ELSE binding_hash END,
      binding_bytes = CASE WHEN v_kind='prepared' THEN p_binding_bytes ELSE binding_bytes END,
      issue_envelope_digest = CASE WHEN v_kind='issued' THEN p_envelope_digest ELSE issue_envelope_digest END,
      issue_envelope_bytes = CASE WHEN v_kind='issued' THEN p_envelope_bytes ELSE issue_envelope_bytes END,
      issue_attestation_key_id = CASE WHEN v_kind='issued' THEN v_attestation->>'keyId' ELSE issue_attestation_key_id END,
      issue_payload_digest = CASE WHEN v_kind='issued' THEN p_payload_digest ELSE issue_payload_digest END,
      issue_payload_bytes = CASE WHEN v_kind='issued' THEN p_payload_bytes ELSE issue_payload_bytes END,
      revocation_operation_id = CASE WHEN v_kind='revocation-reserved' THEN v_operation_id::uuid ELSE revocation_operation_id END,
      revocation_command_hash = CASE WHEN v_kind='revocation-reserved' THEN v_revoke_hash ELSE revocation_command_hash END,
      revoked_at = CASE WHEN v_kind='revocation-reserved' THEN v_revoked_at ELSE revoked_at END,
      revocation_envelope_digest = CASE WHEN v_kind='revocation-attested' THEN p_envelope_digest ELSE revocation_envelope_digest END,
      revocation_envelope_bytes = CASE WHEN v_kind='revocation-attested' THEN p_envelope_bytes ELSE revocation_envelope_bytes END,
      revocation_attestation_key_id = CASE WHEN v_kind='revocation-attested' THEN v_attestation->>'keyId' ELSE revocation_attestation_key_id END,
      revocation_payload_digest = CASE WHEN v_kind='revocation-attested' THEN p_payload_digest ELSE revocation_payload_digest END,
      revocation_payload_bytes = CASE WHEN v_kind='revocation-attested' THEN p_payload_bytes ELSE revocation_payload_bytes END
    WHERE set_id = v_set_id AND version = v_expected_version;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'CredentialSet head changed during projection' USING ERRCODE = 'WSC01';
    END IF;
  END IF;

  DELETE FROM credential_set_projection_authorizations
  WHERE transaction_id=txid_current() AND backend_pid=pg_backend_pid();
  RETURN true;
EXCEPTION
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation
    OR invalid_text_representation OR numeric_value_out_of_range OR datetime_field_overflow
    OR character_not_in_repertoire OR data_exception THEN
    RAISE EXCEPTION 'CredentialSet exact append rejected: %', SQLERRM USING ERRCODE = 'WSC03';
END;
$function$;

COMMENT ON TABLE credential_set_events IS
  'Append-only non-secret CredentialSet event ledger with exact canonical event/binding/attestation bytes.';
COMMENT ON TABLE credential_set_operations IS
  'Append-only globally unique issue and revoke operation identities; no member-level operation exists.';
COMMENT ON TABLE credential_set_heads IS
  'Current CredentialSet projection movable only by append_credential_set_event.';
COMMENT ON FUNCTION append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) IS
  'Owner-only exact event append, event-ID replay, set/version CAS, post-lock DB time, and closed projection transition.';

REVOKE ALL ON TABLE credential_set_events FROM PUBLIC;
REVOKE ALL ON TABLE credential_set_operations FROM PUBLIC;
REVOKE ALL ON TABLE credential_set_heads FROM PUBLIC;
REVOKE ALL ON TABLE credential_set_projection_authorizations FROM PUBLIC;
REVOKE ALL ON FUNCTION credential_set_sha256(bytea) FROM PUBLIC;
REVOKE ALL ON FUNCTION reject_credential_set_immutable_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION guard_credential_set_head_projection() FROM PUBLIC;
REVOKE ALL ON FUNCTION append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) FROM PUBLIC;

DO $credential_set_ownership_and_acl$
DECLARE
  schema_name text := current_schema();
BEGIN
  EXECUTE format(
    'ALTER FUNCTION %I.append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.guard_credential_set_head_projection() '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.reject_credential_set_immutable_mutation() '
    'SET search_path TO pg_catalog', schema_name
  );
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format('ALTER TABLE %I.credential_set_events OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.credential_set_operations OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.credential_set_heads OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.credential_set_projection_authorizations OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.credential_set_sha256(bytea) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.reject_credential_set_immutable_mutation() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.guard_credential_set_head_projection() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format(
      'ALTER FUNCTION %I.append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application') THEN
    EXECUTE format('REVOKE ALL ON TABLE %I.credential_set_events FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.credential_set_operations FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.credential_set_heads FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.credential_set_projection_authorizations FROM worksflow_application', schema_name);
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.append_credential_set_event(text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) FROM worksflow_application',
      schema_name
    );
  END IF;
END;
$credential_set_ownership_and_acl$;
