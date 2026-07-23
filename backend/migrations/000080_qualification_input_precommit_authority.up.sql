-- Qualification Input Precommit Authority v1 closes the independently
-- verified source-policy -> clean-source and credential-request -> resolved
-- credential-set edges before Promotion.  The rollout fence is the first
-- executable statement and is shared with WIA, Policy, Plan, and Promotion.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

CREATE FUNCTION qualification_input_precommit_hash_v1(p_domain text, p_value bytea)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT 'sha256:' || pg_catalog.encode(
    pg_catalog.sha256(
      pg_catalog.convert_to('worksflow-qualification-input-precommit-hash/v1', 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || pg_catalog.convert_to(p_domain, 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || p_value
    ),
    'hex'
  )
$function$;

CREATE FUNCTION qualification_input_precommit_timestamp_v1(p_value timestamptz)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT pg_catalog.to_char(
    p_value AT TIME ZONE 'UTC',
    'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
  )
$function$;

-- Frozen SQL counterpart of qualificationinputauthority.forbiddenSecretString.
-- Every non-opaque string admitted by this boundary is checked here so bytes
-- accepted by PostgreSQL cannot later fail the Go canonical decoder merely
-- because they contain a credential, provider token, host path, or header.
CREATE FUNCTION qualification_input_precommit_string_is_secret_free_v1(p_value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT folded.value !~ $pattern$-----begin[ \t]+(?:[a-z0-9]+[ \t]+)*private key-----$pattern$
    AND folded.value !~ $pattern$(?:^|[^a-z0-9_])(?:sk-[a-z0-9_-]{15,}[a-z0-9_]|(?:gh[pousr]|github_pat)_[a-z0-9_-]{15,}[a-z0-9_]|akia[0-9a-z]{16})(?:[^a-z0-9_]|$)$pattern$
    AND p_value !~ $pattern$(?:^|[^A-Za-z0-9_])eyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{7,}[a-zA-Z0-9_](?:[^A-Za-z0-9_]|$)$pattern$
    AND folded.value !~ $pattern$(?:^|[^a-z0-9_])[a-z][a-z0-9+.-]*://[^ \t\n\f\r/@:]+:[^ \t\n\f\r/@]+@$pattern$
    AND folded.value !~ $pattern$(?:^|[^a-z0-9_])(?:api[_-]?key|client[_-]?secret|auth[_-]?token|password|passwd|private[_-]?key|database[_-]?url)[ \t\n\f\r]*[:=][ \t\n\f\r]*["']?[^ \t\n\f\r]{8,}$pattern$
    AND folded.value !~ $pattern$(?:^|\n)[ \t]*(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key)[ \t]*:[ \t]*[^ \t\n\f\r]+$pattern$
    AND folded.value !~ $pattern$(?:^|[^a-z0-9_])bearer[ \t]+[a-z0-9._~+/-]{8,}=*$pattern$
    AND folded.value !~ $pattern$(?:^|\n)[ \t]*(?:export[ \t]+)?[a-z0-9_]*(?:secret|token|password|passwd|api_key|private_key|database_url)[a-z0-9_]*[ \t]*=[ \t]*[^ \t\n\f\r]+$pattern$
    AND folded.value !~ $pattern$(?:^|[ \t\n\f\r"'=(:])(?:file://|/(?:home|users|root|tmp|var|etc|run|proc|sys|dev|opt|srv|mnt|media|data|workspace|workspaces)(?:[/\\]|$)|[a-z]:[/\\]|\\\\[a-z0-9._-]+[/\\])$pattern$
  FROM (
    SELECT pg_catalog.translate(
      p_value,
      'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
      'abcdefghijklmnopqrstuvwxyz'
    ) AS value
  ) AS folded
$function$;

-- Runtime authority is one exact, inherited, non-settable LOGIN -> NOLOGIN
-- operator membership.  SECURITY DEFINER changes current_user, so the
-- original login is authenticated through session_user while the role GUC
-- proves that the session did not enter through SET ROLE.  The database owner
-- is the sole operational exception because it already owns ALTER/DROP and is
-- the migration/canary trust boundary.
CREATE FUNCTION qualification_input_precommit_caller_is_v1(p_expected_login text)
RETURNS boolean
LANGUAGE sql
STABLE STRICT PARALLEL SAFE
SECURITY INVOKER
AS $function$
  SELECT pg_catalog.current_setting('role') = 'none'
    AND (
      EXISTS (
        SELECT 1 FROM pg_catalog.pg_database AS database
        WHERE database.datname = pg_catalog.current_database()
          AND pg_catalog.pg_get_userbyid(database.datdba) = session_user::text
      )
      OR EXISTS (
        SELECT 1
        FROM pg_catalog.pg_roles AS login
        JOIN pg_catalog.pg_auth_members AS membership
          ON membership.member = login.oid
        JOIN pg_catalog.pg_roles AS operator
          ON operator.oid = membership.roleid
        WHERE login.rolname = session_user::text
          AND login.rolcanlogin
          AND login.rolinherit
          AND NOT (
            login.rolsuper OR login.rolbypassrls OR login.rolcreaterole
            OR login.rolcreatedb OR login.rolreplication
          )
          AND operator.rolname = p_expected_login
          AND NOT operator.rolcanlogin
          AND NOT (
            operator.rolsuper OR operator.rolbypassrls OR operator.rolcreaterole
            OR operator.rolcreatedb OR operator.rolreplication
          )
          AND membership.inherit_option
          AND NOT membership.set_option
          AND NOT membership.admin_option
          AND (
            SELECT pg_catalog.count(*)
            FROM pg_catalog.pg_auth_members AS outgoing
            WHERE outgoing.member = login.oid
          ) = 1
          AND (
            SELECT pg_catalog.count(*)
            FROM pg_catalog.pg_auth_members AS inbound
            WHERE inbound.roleid = operator.oid
          ) = 1
          AND NOT EXISTS (
            SELECT 1
            FROM pg_catalog.pg_auth_members AS transitive
            WHERE transitive.member = operator.oid
          )
          AND NOT EXISTS (
            SELECT 1 FROM pg_catalog.pg_database AS owned_database
            WHERE owned_database.datdba = login.oid
          )
          AND NOT EXISTS (
            SELECT 1 FROM pg_catalog.pg_namespace AS owned_namespace
            WHERE owned_namespace.nspowner = login.oid
          )
          AND NOT EXISTS (
            SELECT 1 FROM pg_catalog.pg_class AS owned_relation
            WHERE owned_relation.relowner = login.oid
          )
          AND NOT EXISTS (
            SELECT 1 FROM pg_catalog.pg_proc AS owned_routine
            WHERE owned_routine.proowner = login.oid
          )
      )
    )
$function$;

CREATE TABLE qualification_input_precommit_executable_binding_generations (
  binding_role text NOT NULL CHECK (
    binding_role IN ('source-verification','credential-resolution')
  ),
  generation bigint NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
  authority_id text NOT NULL UNIQUE CHECK (
    pg_catalog.octet_length(authority_id) BETWEEN 1 AND 256
    AND authority_id = pg_catalog.btrim(authority_id)
    AND authority_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
    AND authority_id !~ '[[:cntrl:]]'
  ),
  executable_digest text NOT NULL UNIQUE CHECK (
    executable_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  expected_previous_executable_digest text CHECK (
    expected_previous_executable_digest IS NULL
    OR expected_previous_executable_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  reviewed_at timestamptz NOT NULL CHECK (
    reviewed_at = pg_catalog.date_trunc('milliseconds', reviewed_at)
  ),
  PRIMARY KEY (binding_role, generation),
  UNIQUE (binding_role, authority_id, executable_digest),
  UNIQUE (binding_role, generation, authority_id, executable_digest),
  CHECK (
    (generation = 1 AND expected_previous_executable_digest IS NULL)
    OR (generation > 1 AND expected_previous_executable_digest IS NOT NULL)
  ),
  CHECK (expected_previous_executable_digest IS NULL
    OR expected_previous_executable_digest <> executable_digest)
);

-- This two-row deployment head is intentionally mutable only through the
-- owner-only review function.  Unlike a MAX(generation) predicate, locking a
-- concrete head row makes a concurrent rotation visible as a row-version
-- conflict to SERIALIZABLE admission/issue transactions instead of allowing
-- an old MVCC snapshot to certify a retired executable.
CREATE TABLE qualification_input_precommit_executable_binding_heads (
  binding_role text PRIMARY KEY CHECK (
    binding_role IN ('source-verification','credential-resolution')
  ),
  generation bigint NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
  authority_id text NOT NULL,
  executable_digest text NOT NULL CHECK (
    executable_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  advanced_at timestamptz NOT NULL CHECK (
    advanced_at = pg_catalog.date_trunc('milliseconds', advanced_at)
  ),
  FOREIGN KEY (binding_role, generation, authority_id, executable_digest)
    REFERENCES qualification_input_precommit_executable_binding_generations(
      binding_role, generation, authority_id, executable_digest
    ) ON DELETE RESTRICT,
  UNIQUE (authority_id),
  UNIQUE (executable_digest)
);

CREATE TABLE qualification_input_source_receipt_admissions (
  request_hash text PRIMARY KEY CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(request_bytes) BETWEEN 1 AND 4194304
  ),
  request_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(request_document) = 'object'
  ),
  admission_hash text NOT NULL UNIQUE CHECK (
    admission_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  admission_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(admission_bytes) BETWEEN 1 AND 4194304
  ),
  admission_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(admission_document) = 'object'
  ),
  authority_id text NOT NULL,
  executable_digest text NOT NULL CHECK (
    executable_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  receipt_hash text NOT NULL CHECK (receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  admitted_at timestamptz NOT NULL CHECK (
    admitted_at = pg_catalog.date_trunc('milliseconds', admitted_at)
  ),
  UNIQUE (request_hash, admission_hash, authority_id, executable_digest, receipt_hash),
  CHECK (
    request_hash <> admission_hash
    AND request_hash <> executable_digest
    AND request_hash <> receipt_hash
    AND admission_hash <> executable_digest
    AND admission_hash <> receipt_hash
    AND executable_digest <> receipt_hash
  )
);

CREATE TABLE qualification_input_credential_receipt_admissions (
  request_hash text PRIMARY KEY CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(request_bytes) BETWEEN 1 AND 4194304
  ),
  request_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(request_document) = 'object'
  ),
  admission_hash text NOT NULL UNIQUE CHECK (
    admission_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  admission_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(admission_bytes) BETWEEN 1 AND 4194304
  ),
  admission_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(admission_document) = 'object'
  ),
  authority_id text NOT NULL,
  executable_digest text NOT NULL CHECK (
    executable_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  receipt_hash text NOT NULL CHECK (receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  admitted_at timestamptz NOT NULL CHECK (
    admitted_at = pg_catalog.date_trunc('milliseconds', admitted_at)
  ),
  UNIQUE (request_hash, admission_hash, authority_id, executable_digest, receipt_hash),
  CHECK (
    request_hash <> admission_hash
    AND request_hash <> executable_digest
    AND request_hash <> receipt_hash
    AND admission_hash <> executable_digest
    AND admission_hash <> receipt_hash
    AND executable_digest <> receipt_hash
  )
);

CREATE TABLE qualification_input_precommit_authorities (
  authority_id uuid PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE,

  request_hash text NOT NULL UNIQUE CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(request_bytes) BETWEEN 1 AND 4194304
  ),
  request_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(request_document) = 'object'
  ),
  source_request_hash text NOT NULL CHECK (
    source_request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  source_request_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(source_request_bytes) BETWEEN 1 AND 4194304
  ),
  source_request_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(source_request_document) = 'object'
  ),
  credential_request_hash text NOT NULL CHECK (
    credential_request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  credential_request_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(credential_request_bytes) BETWEEN 1 AND 4194304
  ),
  credential_request_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(credential_request_document) = 'object'
  ),
  authority_hash text NOT NULL UNIQUE CHECK (
    authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  authority_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(authority_bytes) BETWEEN 1 AND 4194304
  ),
  authority_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(authority_document) = 'object'
  ),

  workflow_input_authority_id uuid NOT NULL UNIQUE
    REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  workflow_input_authority_hash text NOT NULL CHECK (
    workflow_input_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  workflow_input_hash text NOT NULL CHECK (
    workflow_input_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  qualification_policy_authority_id uuid NOT NULL
    REFERENCES qualification_policy_authorities(authority_id) ON DELETE RESTRICT,
  qualification_policy_authority_hash text NOT NULL CHECK (
    qualification_policy_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  policy_plan_input_profile_hash text NOT NULL CHECK (
    policy_plan_input_profile_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  source_policy_digest text NOT NULL CHECK (
    source_policy_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  credential_profile_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(credential_profile_document) = 'object'
  ),
  qualification_plan_authority_id uuid NOT NULL UNIQUE
    REFERENCES qualification_plan_authorities(authority_id) ON DELETE RESTRICT,
  qualification_plan_authority_hash text NOT NULL CHECK (
    qualification_plan_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  qualification_plan_input_authority_id uuid NOT NULL,
  qualification_plan_input_hash text NOT NULL CHECK (
    qualification_plan_input_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  source_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(source_document) = 'object'
  ),
  credential_set_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(credential_set_document) = 'object'
  ),

  source_verifier_authority_id text NOT NULL,
  source_verifier_executable_digest text NOT NULL CHECK (
    source_verifier_executable_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  source_receipt_hash text NOT NULL CHECK (
    source_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  source_admission_hash text NOT NULL CHECK (
    source_admission_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  credential_resolver_authority_id text NOT NULL,
  credential_resolver_executable_digest text NOT NULL CHECK (
    credential_resolver_executable_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  credential_receipt_hash text NOT NULL CHECK (
    credential_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  credential_admission_hash text NOT NULL CHECK (
    credential_admission_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  issued_at timestamptz NOT NULL CHECK (
    issued_at = pg_catalog.date_trunc('milliseconds', issued_at)
  ),

  FOREIGN KEY (
    source_request_hash, source_admission_hash, source_verifier_authority_id,
    source_verifier_executable_digest, source_receipt_hash
  ) REFERENCES qualification_input_source_receipt_admissions(
    request_hash, admission_hash, authority_id, executable_digest, receipt_hash
  ) ON DELETE RESTRICT,
  FOREIGN KEY (
    credential_request_hash, credential_admission_hash,
    credential_resolver_authority_id,
    credential_resolver_executable_digest, credential_receipt_hash
  ) REFERENCES qualification_input_credential_receipt_admissions(
    request_hash, admission_hash, authority_id, executable_digest, receipt_hash
  ) ON DELETE RESTRICT,
  CHECK (
    workflow_input_uuid_is_exact(authority_id::text)
    AND workflow_input_uuid_is_exact(operation_id::text)
    AND workflow_input_uuid_is_exact(workflow_input_authority_id::text)
    AND workflow_input_uuid_is_exact(qualification_policy_authority_id::text)
    AND workflow_input_uuid_is_exact(qualification_plan_authority_id::text)
    AND workflow_input_uuid_is_exact(qualification_plan_input_authority_id::text)
  ),
  CHECK (
    operation_id <> authority_id
    AND operation_id <> workflow_input_authority_id
    AND operation_id <> qualification_policy_authority_id
    AND operation_id <> qualification_plan_authority_id
    AND authority_id <> workflow_input_authority_id
    AND authority_id <> qualification_policy_authority_id
    AND authority_id <> qualification_plan_authority_id
    AND workflow_input_authority_id <> qualification_policy_authority_id
    AND workflow_input_authority_id <> qualification_plan_authority_id
    AND qualification_policy_authority_id <> qualification_plan_authority_id
  ),
  CHECK (qualification_plan_input_authority_id = workflow_input_authority_id),
  CHECK (
    source_request_hash <> source_receipt_hash
    AND source_request_hash <> source_admission_hash
    AND source_request_hash <> credential_request_hash
    AND source_request_hash <> credential_receipt_hash
    AND source_request_hash <> credential_admission_hash
    AND source_receipt_hash <> source_admission_hash
    AND source_receipt_hash <> credential_request_hash
    AND source_receipt_hash <> credential_receipt_hash
    AND source_receipt_hash <> credential_admission_hash
    AND source_admission_hash <> credential_request_hash
    AND source_admission_hash <> credential_receipt_hash
    AND source_admission_hash <> credential_admission_hash
    AND credential_request_hash <> credential_receipt_hash
    AND credential_request_hash <> credential_admission_hash
    AND credential_receipt_hash <> credential_admission_hash
  ),
  CHECK (
    request_hash <> source_request_hash
    AND request_hash <> credential_request_hash
    AND source_request_hash <> credential_request_hash
    AND source_verifier_authority_id <> credential_resolver_authority_id
    AND source_verifier_executable_digest <> credential_resolver_executable_digest
    AND source_receipt_hash <> credential_receipt_hash
    AND source_admission_hash <> credential_admission_hash
  )
);

CREATE TABLE qualification_input_precommit_identity_reservations (
  identity_value uuid PRIMARY KEY,
  authority_id uuid NOT NULL
    REFERENCES qualification_input_precommit_authorities(authority_id) ON DELETE RESTRICT,
  identity_kind text NOT NULL CHECK (identity_kind IN ('operation','authority')),
  ordinal smallint NOT NULL CHECK (ordinal BETWEEN 0 AND 1),
  reserved_at timestamptz NOT NULL CHECK (
    reserved_at = pg_catalog.date_trunc('milliseconds', reserved_at)
  ),
  UNIQUE (authority_id, identity_kind),
  UNIQUE (authority_id, ordinal),
  CHECK (workflow_input_uuid_is_exact(identity_value::text))
);

CREATE TABLE qualification_input_precommit_wia_reservations (
  workflow_input_authority_id uuid PRIMARY KEY
    REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  authority_id uuid NOT NULL UNIQUE
    REFERENCES qualification_input_precommit_authorities(authority_id) ON DELETE RESTRICT,
  reserved_at timestamptz NOT NULL CHECK (
    reserved_at = pg_catalog.date_trunc('milliseconds', reserved_at)
  )
);

CREATE TABLE qualification_input_precommit_plan_reservations (
  qualification_plan_authority_id uuid PRIMARY KEY
    REFERENCES qualification_plan_authorities(authority_id) ON DELETE RESTRICT,
  authority_id uuid NOT NULL UNIQUE
    REFERENCES qualification_input_precommit_authorities(authority_id) ON DELETE RESTRICT,
  reserved_at timestamptz NOT NULL CHECK (
    reserved_at = pg_catalog.date_trunc('milliseconds', reserved_at)
  )
);

CREATE INDEX qualification_input_precommit_policy_idx
  ON qualification_input_precommit_authorities(
    qualification_policy_authority_id, issued_at, authority_id
  );

CREATE FUNCTION reject_qualification_input_precommit_mutation_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification Input Precommit records are immutable'
    USING ERRCODE = 'WIP02';
END;
$function$;

CREATE TRIGGER qualification_input_precommit_executable_bindings_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_input_precommit_executable_binding_generations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();
CREATE TRIGGER qualification_input_precommit_binding_heads_no_removal
BEFORE DELETE OR TRUNCATE
ON qualification_input_precommit_executable_binding_heads
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();
CREATE TRIGGER qualification_input_source_receipt_admissions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_input_source_receipt_admissions
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();
CREATE TRIGGER qualification_input_credential_receipt_admissions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_input_credential_receipt_admissions
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();
CREATE TRIGGER qualification_input_precommit_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_input_precommit_authorities
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();
CREATE TRIGGER qualification_input_precommit_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_input_precommit_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();
CREATE TRIGGER qualification_input_precommit_wia_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_input_precommit_wia_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();
CREATE TRIGGER qualification_input_precommit_plan_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_input_precommit_plan_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_input_precommit_mutation_v1();

-- Owner-only reviewed deployment authority.  Runtime configuration is not a
-- substitute for this append-only generation chain.
CREATE FUNCTION review_qualification_input_precommit_executable_binding_v1(
  p_binding_role text,
  p_generation bigint,
  p_authority_id text,
  p_executable_digest text,
  p_expected_previous_executable_digest text
)
RETURNS SETOF qualification_input_precommit_executable_binding_generations
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_head qualification_input_precommit_executable_binding_heads%ROWTYPE;
  v_existing qualification_input_precommit_executable_binding_generations%ROWTYPE;
  v_now timestamptz;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  IF p_binding_role NOT IN ('source-verification','credential-resolution')
     OR p_generation IS NULL OR p_generation NOT BETWEEN 1 AND 9007199254740991
     OR p_authority_id IS NULL
     OR pg_catalog.octet_length(p_authority_id) NOT BETWEEN 1 AND 256
     OR p_authority_id <> pg_catalog.btrim(p_authority_id)
     OR p_authority_id !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
     OR p_authority_id ~ '[[:cntrl:]]'
     OR qualification_input_precommit_string_is_secret_free_v1(
       p_authority_id
     ) IS NOT TRUE
     OR p_executable_digest IS NULL
     OR p_executable_digest !~ '^sha256:[0-9a-f]{64}$'
     OR (p_expected_previous_executable_digest IS NOT NULL
       AND p_expected_previous_executable_digest !~ '^sha256:[0-9a-f]{64}$') THEN
    RAISE EXCEPTION 'Qualification Input executable binding command is invalid'
      USING ERRCODE = 'WIP01';
  END IF;

  LOCK TABLE qualification_input_precommit_executable_binding_generations
    IN SHARE ROW EXCLUSIVE MODE;
  SELECT * INTO v_head
  FROM qualification_input_precommit_executable_binding_heads
  WHERE binding_role = p_binding_role
  FOR UPDATE;
  SELECT * INTO v_existing
  FROM qualification_input_precommit_executable_binding_generations
  WHERE binding_role = p_binding_role AND generation = p_generation;
  IF FOUND THEN
    IF v_existing.authority_id IS DISTINCT FROM p_authority_id
       OR v_existing.executable_digest IS DISTINCT FROM p_executable_digest
       OR v_existing.expected_previous_executable_digest
          IS DISTINCT FROM p_expected_previous_executable_digest THEN
      RAISE EXCEPTION 'Qualification Input executable generation conflicts with immutable state'
        USING ERRCODE = 'WIP02';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  IF (p_generation = 1 AND (v_head.binding_role IS NOT NULL
       OR p_expected_previous_executable_digest IS NOT NULL))
     OR (p_generation > 1 AND (
       v_head.binding_role IS NULL OR v_head.generation <> p_generation - 1
       OR v_head.executable_digest IS DISTINCT FROM p_expected_previous_executable_digest
     )) THEN
    RAISE EXCEPTION 'Qualification Input executable generation is not contiguous'
      USING ERRCODE = 'WIP02';
  END IF;

  -- Global uniqueness also prevents a source deployment from ever satisfying
  -- the credential role (or vice versa), including across retired history.
  IF EXISTS (
    SELECT 1 FROM qualification_input_precommit_executable_binding_generations
    WHERE authority_id = p_authority_id OR executable_digest = p_executable_digest
  ) THEN
    RAISE EXCEPTION 'Qualification Input executable authority or digest aliases reviewed history'
      USING ERRCODE = 'WIP02';
  END IF;

  v_now := pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp());
  INSERT INTO qualification_input_precommit_executable_binding_generations(
    binding_role, generation, authority_id, executable_digest,
    expected_previous_executable_digest, reviewed_at
  ) VALUES (
    p_binding_role, p_generation, p_authority_id, p_executable_digest,
    p_expected_previous_executable_digest, v_now
  );
  IF p_generation = 1 THEN
    INSERT INTO qualification_input_precommit_executable_binding_heads(
      binding_role, generation, authority_id, executable_digest, advanced_at
    ) VALUES (
      p_binding_role, p_generation, p_authority_id, p_executable_digest, v_now
    );
  ELSE
    UPDATE qualification_input_precommit_executable_binding_heads
    SET generation = p_generation,
        authority_id = p_authority_id,
        executable_digest = p_executable_digest,
        advanced_at = v_now
    WHERE binding_role = p_binding_role
      AND generation = p_generation - 1
      AND executable_digest = p_expected_previous_executable_digest;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Qualification Input executable binding head changed concurrently'
        USING ERRCODE = 'WIP02';
    END IF;
  END IF;
  RETURN QUERY
  SELECT * FROM qualification_input_precommit_executable_binding_generations
  WHERE binding_role = p_binding_role AND generation = p_generation;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Input executable binding conflict'
      USING ERRCODE = 'WIP02';
END;
$function$;

CREATE FUNCTION qualification_input_source_admission_is_exact_v1(p_request_hash text)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_admission qualification_input_source_receipt_admissions%ROWTYPE;
  v_expected_admission jsonb;
  v_binding jsonb;
BEGIN
  IF p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$' THEN
    RETURN false;
  END IF;
  SELECT * INTO v_admission
  FROM qualification_input_source_receipt_admissions
  WHERE request_hash = p_request_hash;
  IF NOT FOUND THEN RETURN false; END IF;

  IF v_admission.request_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_admission.request_document)
     OR v_admission.admission_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_admission.admission_document)
     OR v_admission.request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.source-request/v1',
       v_admission.request_bytes
     )
     OR v_admission.admission_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.receipt-admission/v1',
       v_admission.admission_bytes
     ) THEN
    RETURN false;
  END IF;

  IF pg_catalog.jsonb_typeof(v_admission.request_document->'plan') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'policy') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'source') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'verifier') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'workflowInput') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'schemaVersion') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'sourcePolicyDigest') IS DISTINCT FROM 'string'
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(
       v_admission.request_document
     )) <> 7
     OR (v_admission.request_document ?& ARRAY[
       'plan','policy','schemaVersion','source','sourcePolicyDigest',
       'verifier','workflowInput'
     ]::text[]) IS NOT TRUE
     OR v_admission.request_document->>'schemaVersion' IS DISTINCT FROM
       'worksflow-qualification-source-verification-request/v1'
     OR v_admission.request_document->>'sourcePolicyDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR (v_admission.request_document->'source' ?& ARRAY[
       'commit','dirty','treeDigest','treeDigestSchema'
     ]::text[]) IS NOT TRUE
     OR pg_catalog.jsonb_typeof(
       v_admission.request_document->'source'->'commit'
     ) IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(
       v_admission.request_document->'source'->'dirty'
     ) IS DISTINCT FROM 'boolean'
     OR pg_catalog.jsonb_typeof(
       v_admission.request_document->'source'->'treeDigest'
     ) IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(
       v_admission.request_document->'source'->'treeDigestSchema'
     ) IS DISTINCT FROM 'string'
     OR v_admission.request_document->'source'->>'commit' !~ '^[0-9a-f]{40}$'
     OR v_admission.request_document->'source'->>'treeDigestSchema' IS DISTINCT FROM
       'worksflow-source-content-tree/v1'
     OR v_admission.request_document->'source'->>'treeDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR v_admission.request_document->'source'->'dirty' IS DISTINCT FROM 'false'::jsonb
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(
       v_admission.request_document->'source'
     )) <> 4
     OR v_admission.request_document->>'sourcePolicyDigest' =
       v_admission.request_document->'source'->>'treeDigest' THEN
    RETURN false;
  END IF;

  FOREACH v_binding IN ARRAY ARRAY[
    v_admission.request_document->'workflowInput',
    v_admission.request_document->'policy',
    v_admission.request_document->'plan'
  ] LOOP
    IF v_binding IS NULL OR pg_catalog.jsonb_typeof(v_binding) <> 'object'
       OR pg_catalog.jsonb_typeof(v_binding->'authorityHash') IS DISTINCT FROM 'string'
       OR pg_catalog.jsonb_typeof(v_binding->'authorityId') IS DISTINCT FROM 'string'
       OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(v_binding)) <> 2
       OR workflow_input_uuid_is_exact(v_binding->>'authorityId') IS NOT TRUE
       OR v_binding->>'authorityHash' !~ '^sha256:[0-9a-f]{64}$' THEN
      RETURN false;
    END IF;
  END LOOP;
  IF v_admission.request_document->'workflowInput'->>'authorityId' IN (
       v_admission.request_document->'policy'->>'authorityId',
       v_admission.request_document->'plan'->>'authorityId'
     )
     OR v_admission.request_document->'policy'->>'authorityId' =
       v_admission.request_document->'plan'->>'authorityId'
     OR v_admission.request_document->'workflowInput'->>'authorityHash' IN (
       v_admission.request_document->'policy'->>'authorityHash',
       v_admission.request_document->'plan'->>'authorityHash'
     )
     OR v_admission.request_document->'policy'->>'authorityHash' =
       v_admission.request_document->'plan'->>'authorityHash' THEN
    RETURN false;
  END IF;

  v_binding := v_admission.request_document->'verifier';
  IF v_binding IS NULL OR pg_catalog.jsonb_typeof(v_binding) <> 'object'
     OR pg_catalog.jsonb_typeof(v_binding->'authorityId') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_binding->'executableDigest') IS DISTINCT FROM 'string'
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(v_binding)) <> 2
     OR v_binding->>'authorityId' IS DISTINCT FROM v_admission.authority_id
     OR v_binding->>'executableDigest' IS DISTINCT FROM v_admission.executable_digest THEN
    RETURN false;
  END IF;

  v_expected_admission := pg_catalog.jsonb_build_object(
    'authorityId', v_admission.authority_id,
    'executableDigest', v_admission.executable_digest,
    'kind', 'source-verification',
    'receiptHash', v_admission.receipt_hash,
    'requestHash', v_admission.request_hash,
    'schemaVersion', 'worksflow-qualification-input-verification-receipt-admission/v1'
  );
  RETURN v_admission.admission_document = v_expected_admission
    AND EXISTS (
      SELECT 1 FROM qualification_input_precommit_executable_binding_generations
      WHERE binding_role = 'source-verification'
        AND authority_id = v_admission.authority_id
        AND executable_digest = v_admission.executable_digest
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
END;
$function$;

CREATE FUNCTION qualification_input_credential_admission_is_exact_v1(p_request_hash text)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_admission qualification_input_credential_receipt_admissions%ROWTYPE;
  v_expected_admission jsonb;
  v_binding jsonb;
  v_profile jsonb;
  v_set jsonb;
BEGIN
  IF p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$' THEN
    RETURN false;
  END IF;
  SELECT * INTO v_admission
  FROM qualification_input_credential_receipt_admissions
  WHERE request_hash = p_request_hash;
  IF NOT FOUND THEN RETURN false; END IF;

  IF v_admission.request_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_admission.request_document)
     OR v_admission.admission_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_admission.admission_document)
     OR v_admission.request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.credential-request/v1',
       v_admission.request_bytes
     )
     OR v_admission.admission_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.receipt-admission/v1',
       v_admission.admission_bytes
     ) THEN
    RETURN false;
  END IF;

  IF pg_catalog.jsonb_typeof(v_admission.request_document->'credentialProfile') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'credentialSet') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'plan') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'policy') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'resolver') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'workflowInput') IS DISTINCT FROM 'object'
     OR pg_catalog.jsonb_typeof(v_admission.request_document->'schemaVersion') IS DISTINCT FROM 'string'
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(
       v_admission.request_document
     )) <> 7
     OR (v_admission.request_document ?& ARRAY[
       'credentialProfile','credentialSet','plan','policy','resolver',
       'schemaVersion','workflowInput'
     ]::text[]) IS NOT TRUE
     OR v_admission.request_document->>'schemaVersion' IS DISTINCT FROM
       'worksflow-qualification-credential-resolution-request/v1' THEN
    RETURN false;
  END IF;

  FOREACH v_binding IN ARRAY ARRAY[
    v_admission.request_document->'workflowInput',
    v_admission.request_document->'policy',
    v_admission.request_document->'plan'
  ] LOOP
    IF v_binding IS NULL OR pg_catalog.jsonb_typeof(v_binding) <> 'object'
       OR pg_catalog.jsonb_typeof(v_binding->'authorityHash') IS DISTINCT FROM 'string'
       OR pg_catalog.jsonb_typeof(v_binding->'authorityId') IS DISTINCT FROM 'string'
       OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(v_binding)) <> 2
       OR workflow_input_uuid_is_exact(v_binding->>'authorityId') IS NOT TRUE
       OR v_binding->>'authorityHash' !~ '^sha256:[0-9a-f]{64}$' THEN
      RETURN false;
    END IF;
  END LOOP;
  IF v_admission.request_document->'workflowInput'->>'authorityId' IN (
       v_admission.request_document->'policy'->>'authorityId',
       v_admission.request_document->'plan'->>'authorityId'
     )
     OR v_admission.request_document->'policy'->>'authorityId' =
       v_admission.request_document->'plan'->>'authorityId'
     OR v_admission.request_document->'workflowInput'->>'authorityHash' IN (
       v_admission.request_document->'policy'->>'authorityHash',
       v_admission.request_document->'plan'->>'authorityHash'
     )
     OR v_admission.request_document->'policy'->>'authorityHash' =
       v_admission.request_document->'plan'->>'authorityHash' THEN
    RETURN false;
  END IF;

  v_binding := v_admission.request_document->'resolver';
  v_profile := v_admission.request_document->'credentialProfile';
  v_set := v_admission.request_document->'credentialSet';
  IF v_binding IS NULL OR pg_catalog.jsonb_typeof(v_binding) <> 'object'
     OR pg_catalog.jsonb_typeof(v_binding->'authorityId') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_binding->'executableDigest') IS DISTINCT FROM 'string'
     OR (v_binding ?& ARRAY['authorityId','executableDigest']::text[]) IS NOT TRUE
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(v_binding)) <> 2
     OR v_binding->>'authorityId' IS DISTINCT FROM v_admission.authority_id
     OR v_binding->>'executableDigest' IS DISTINCT FROM v_admission.executable_digest
     OR v_profile IS NULL OR pg_catalog.jsonb_typeof(v_profile) IS DISTINCT FROM 'object'
     OR v_set IS NULL OR pg_catalog.jsonb_typeof(v_set) IS DISTINCT FROM 'object'
     OR (v_profile ?& ARRAY[
       'audience','authorityId','issuanceArtifactId',
       'memberRequestSetDigest','revocationArtifactId'
     ]::text[]) IS NOT TRUE
     OR (v_set ?& ARRAY[
       'audience','issuanceArtifactId','issuer','memberBindingsDigest',
       'memberCount','revocationArtifactId','setHandleHash','setId'
     ]::text[]) IS NOT TRUE
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(v_profile)) <> 5
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(v_set)) <> 8
     OR pg_catalog.jsonb_typeof(v_profile->'audience') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_profile->'authorityId') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_profile->'issuanceArtifactId') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_profile->'memberRequestSetDigest') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_profile->'revocationArtifactId') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_set->'audience') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_set->'issuanceArtifactId') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_set->'issuer') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_set->'memberBindingsDigest') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_set->'memberCount') IS DISTINCT FROM 'number'
     OR pg_catalog.jsonb_typeof(v_set->'revocationArtifactId') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_set->'setHandleHash') IS DISTINCT FROM 'string'
     OR pg_catalog.jsonb_typeof(v_set->'setId') IS DISTINCT FROM 'string'
     OR (pg_catalog.octet_length(v_profile->>'authorityId') BETWEEN 1 AND 256) IS NOT TRUE
     OR v_profile->>'authorityId' !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
     OR qualification_input_precommit_string_is_secret_free_v1(
       v_profile->>'authorityId'
     ) IS NOT TRUE
     OR (pg_catalog.octet_length(v_set->>'issuer') BETWEEN 1 AND 256) IS NOT TRUE
     OR v_set->>'issuer' !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
     OR (pg_catalog.octet_length(pg_catalog.convert_to(
       v_profile->>'audience', 'UTF8'
     )) BETWEEN 1 AND 512) IS NOT TRUE
     OR v_profile->>'audience' ~ E'[\\r\\n]'
     OR pg_catalog.ascii(pg_catalog.left(v_profile->>'audience', 1)) IN (
       9,10,11,12,13,32,133,160,5760,
       8192,8193,8194,8195,8196,8197,8198,8199,8200,8201,8202,
       8232,8233,8239,8287,12288
     )
     OR pg_catalog.ascii(pg_catalog.right(v_profile->>'audience', 1)) IN (
       9,10,11,12,13,32,133,160,5760,
       8192,8193,8194,8195,8196,8197,8198,8199,8200,8201,8202,
       8232,8233,8239,8287,12288
     )
     OR qualification_input_precommit_string_is_secret_free_v1(
       v_profile->>'audience'
     ) IS NOT TRUE
     OR (pg_catalog.octet_length(v_profile->>'issuanceArtifactId') BETWEEN 1 AND 256) IS NOT TRUE
     OR v_profile->>'issuanceArtifactId' !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
     OR qualification_input_precommit_string_is_secret_free_v1(
       v_profile->>'issuanceArtifactId'
     ) IS NOT TRUE
     OR (pg_catalog.octet_length(v_profile->>'revocationArtifactId') BETWEEN 1 AND 256) IS NOT TRUE
     OR v_profile->>'revocationArtifactId' !~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
     OR qualification_input_precommit_string_is_secret_free_v1(
       v_profile->>'revocationArtifactId'
     ) IS NOT TRUE
     OR v_profile->>'memberRequestSetDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR v_set->>'memberBindingsDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR v_set->>'setHandleHash' !~ '^sha256:[0-9a-f]{64}$'
     OR workflow_input_uuid_is_exact(v_set->>'setId') IS NOT TRUE
     OR v_set->>'memberCount' !~ '^(?:[1-9]|[1-5][0-9]|6[0-4])$'
     OR v_profile->>'authorityId' IS DISTINCT FROM v_set->>'issuer'
     OR v_profile->>'audience' IS DISTINCT FROM v_set->>'audience'
     OR v_profile->>'issuanceArtifactId' IS DISTINCT FROM v_set->>'issuanceArtifactId'
     OR v_profile->>'revocationArtifactId' IS DISTINCT FROM v_set->>'revocationArtifactId'
     OR v_profile->>'issuanceArtifactId' = v_profile->>'revocationArtifactId'
     OR v_profile->>'memberRequestSetDigest' IN (
       v_set->>'memberBindingsDigest', v_set->>'setHandleHash'
     )
     OR v_set->>'memberBindingsDigest' = v_set->>'setHandleHash' THEN
    RETURN false;
  END IF;

  v_expected_admission := pg_catalog.jsonb_build_object(
    'authorityId', v_admission.authority_id,
    'executableDigest', v_admission.executable_digest,
    'kind', 'credential-resolution',
    'receiptHash', v_admission.receipt_hash,
    'requestHash', v_admission.request_hash,
    'schemaVersion', 'worksflow-qualification-input-verification-receipt-admission/v1'
  );
  RETURN v_admission.admission_document = v_expected_admission
    AND EXISTS (
      SELECT 1 FROM qualification_input_precommit_executable_binding_generations
      WHERE binding_role = 'credential-resolution'
        AND authority_id = v_admission.authority_id
        AND executable_digest = v_admission.executable_digest
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
END;
$function$;

CREATE FUNCTION admit_qualification_input_source_receipt_v1(
  p_request_hash text,
  p_request_bytes bytea,
  p_request_document jsonb,
  p_admission_hash text,
  p_admission_bytes bytea,
  p_admission_document jsonb
)
RETURNS SETOF qualification_input_source_receipt_admissions
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_binding qualification_input_precommit_executable_binding_generations%ROWTYPE;
  v_existing qualification_input_source_receipt_admissions%ROWTYPE;
  v_authority_id text;
  v_executable_digest text;
  v_receipt_hash text;
  v_expected_admission jsonb;
  v_now timestamptz;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_source_verifier_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input source admission caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  IF p_request_document IS NULL OR p_admission_document IS NULL THEN
    RAISE EXCEPTION 'Qualification Input source admission is invalid'
      USING ERRCODE = 'WIP01';
  END IF;
  v_authority_id := p_admission_document->>'authorityId';
  v_executable_digest := p_admission_document->>'executableDigest';
  v_receipt_hash := p_admission_document->>'receiptHash';
  v_expected_admission := pg_catalog.jsonb_build_object(
    'authorityId', v_authority_id,
    'executableDigest', v_executable_digest,
    'kind', 'source-verification',
    'receiptHash', v_receipt_hash,
    'requestHash', p_request_hash,
    'schemaVersion', 'worksflow-qualification-input-verification-receipt-admission/v1'
  );
  IF p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_admission_hash !~ '^sha256:[0-9a-f]{64}$'
     OR v_executable_digest !~ '^sha256:[0-9a-f]{64}$'
     OR v_receipt_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_request_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_request_document)
     OR p_admission_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_admission_document)
     OR p_request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.source-request/v1', p_request_bytes
     )
     OR p_admission_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.receipt-admission/v1', p_admission_bytes
     )
     OR p_admission_document IS DISTINCT FROM v_expected_admission
     OR p_request_document->'verifier' IS DISTINCT FROM pg_catalog.jsonb_build_object(
       'authorityId', v_authority_id, 'executableDigest', v_executable_digest
     ) THEN
    RAISE EXCEPTION 'Qualification Input source admission canonical closure is invalid'
      USING ERRCODE = 'WIP01';
  END IF;

  SELECT binding_history.* INTO v_binding
  FROM qualification_input_precommit_executable_binding_heads AS binding_head
  JOIN qualification_input_precommit_executable_binding_generations AS binding_history
    ON binding_history.binding_role = binding_head.binding_role
   AND binding_history.generation = binding_head.generation
   AND binding_history.authority_id = binding_head.authority_id
   AND binding_history.executable_digest = binding_head.executable_digest
  WHERE binding_head.binding_role = 'source-verification'
  FOR SHARE OF binding_head, binding_history;
  IF NOT FOUND OR v_binding.authority_id IS DISTINCT FROM v_authority_id
     OR v_binding.executable_digest IS DISTINCT FROM v_executable_digest THEN
    RAISE EXCEPTION 'Qualification Input source executable binding is not current'
      USING ERRCODE = 'WIP03';
  END IF;

  v_now := pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp());
  INSERT INTO qualification_input_source_receipt_admissions(
    request_hash, request_bytes, request_document,
    admission_hash, admission_bytes, admission_document,
    authority_id, executable_digest, receipt_hash, admitted_at
  ) VALUES (
    p_request_hash, p_request_bytes, p_request_document,
    p_admission_hash, p_admission_bytes, p_admission_document,
    v_authority_id, v_executable_digest, v_receipt_hash, v_now
  )
  ON CONFLICT (request_hash) DO NOTHING;

  SELECT * INTO v_existing
  FROM qualification_input_source_receipt_admissions
  WHERE request_hash = p_request_hash
  FOR SHARE;
  IF NOT FOUND
     OR qualification_input_source_admission_is_exact_v1(p_request_hash) IS NOT TRUE
     OR v_existing.authority_id IS DISTINCT FROM v_authority_id
     OR v_existing.executable_digest IS DISTINCT FROM v_executable_digest THEN
    RAISE EXCEPTION 'Qualification Input source first-commit winner is not exact'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NEXT v_existing;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Input source admission conflicts with immutable state'
      USING ERRCODE = 'WIP02';
END;
$function$;

CREATE FUNCTION admit_qualification_input_credential_receipt_v1(
  p_request_hash text,
  p_request_bytes bytea,
  p_request_document jsonb,
  p_admission_hash text,
  p_admission_bytes bytea,
  p_admission_document jsonb
)
RETURNS SETOF qualification_input_credential_receipt_admissions
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_binding qualification_input_precommit_executable_binding_generations%ROWTYPE;
  v_existing qualification_input_credential_receipt_admissions%ROWTYPE;
  v_authority_id text;
  v_executable_digest text;
  v_receipt_hash text;
  v_expected_admission jsonb;
  v_now timestamptz;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_credential_resolver_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input credential admission caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  IF p_request_document IS NULL OR p_admission_document IS NULL THEN
    RAISE EXCEPTION 'Qualification Input credential admission is invalid'
      USING ERRCODE = 'WIP01';
  END IF;
  v_authority_id := p_admission_document->>'authorityId';
  v_executable_digest := p_admission_document->>'executableDigest';
  v_receipt_hash := p_admission_document->>'receiptHash';
  v_expected_admission := pg_catalog.jsonb_build_object(
    'authorityId', v_authority_id,
    'executableDigest', v_executable_digest,
    'kind', 'credential-resolution',
    'receiptHash', v_receipt_hash,
    'requestHash', p_request_hash,
    'schemaVersion', 'worksflow-qualification-input-verification-receipt-admission/v1'
  );
  IF p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_admission_hash !~ '^sha256:[0-9a-f]{64}$'
     OR v_executable_digest !~ '^sha256:[0-9a-f]{64}$'
     OR v_receipt_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_request_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_request_document)
     OR p_admission_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_admission_document)
     OR p_request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.credential-request/v1', p_request_bytes
     )
     OR p_admission_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.receipt-admission/v1', p_admission_bytes
     )
     OR p_admission_document IS DISTINCT FROM v_expected_admission
     OR p_request_document->'resolver' IS DISTINCT FROM pg_catalog.jsonb_build_object(
       'authorityId', v_authority_id, 'executableDigest', v_executable_digest
     ) THEN
    RAISE EXCEPTION 'Qualification Input credential admission canonical closure is invalid'
      USING ERRCODE = 'WIP01';
  END IF;

  SELECT binding_history.* INTO v_binding
  FROM qualification_input_precommit_executable_binding_heads AS binding_head
  JOIN qualification_input_precommit_executable_binding_generations AS binding_history
    ON binding_history.binding_role = binding_head.binding_role
   AND binding_history.generation = binding_head.generation
   AND binding_history.authority_id = binding_head.authority_id
   AND binding_history.executable_digest = binding_head.executable_digest
  WHERE binding_head.binding_role = 'credential-resolution'
  FOR SHARE OF binding_head, binding_history;
  IF NOT FOUND OR v_binding.authority_id IS DISTINCT FROM v_authority_id
     OR v_binding.executable_digest IS DISTINCT FROM v_executable_digest THEN
    RAISE EXCEPTION 'Qualification Input credential executable binding is not current'
      USING ERRCODE = 'WIP03';
  END IF;

  v_now := pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp());
  INSERT INTO qualification_input_credential_receipt_admissions(
    request_hash, request_bytes, request_document,
    admission_hash, admission_bytes, admission_document,
    authority_id, executable_digest, receipt_hash, admitted_at
  ) VALUES (
    p_request_hash, p_request_bytes, p_request_document,
    p_admission_hash, p_admission_bytes, p_admission_document,
    v_authority_id, v_executable_digest, v_receipt_hash, v_now
  )
  ON CONFLICT (request_hash) DO NOTHING;

  SELECT * INTO v_existing
  FROM qualification_input_credential_receipt_admissions
  WHERE request_hash = p_request_hash
  FOR SHARE;
  IF NOT FOUND
     OR qualification_input_credential_admission_is_exact_v1(p_request_hash) IS NOT TRUE
     OR v_existing.authority_id IS DISTINCT FROM v_authority_id
     OR v_existing.executable_digest IS DISTINCT FROM v_executable_digest THEN
    RAISE EXCEPTION 'Qualification Input credential first-commit winner is not exact'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NEXT v_existing;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Input credential admission conflicts with immutable state'
      USING ERRCODE = 'WIP02';
END;
$function$;

CREATE FUNCTION inspect_qualification_input_source_receipt_v1(p_request_hash text)
RETURNS SETOF qualification_input_source_receipt_admissions
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_source_verifier_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input source inspection caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF qualification_input_source_admission_is_exact_v1(p_request_hash) IS TRUE THEN
    RETURN QUERY SELECT * FROM qualification_input_source_receipt_admissions
    WHERE request_hash = p_request_hash;
  END IF;
END;
$function$;

CREATE FUNCTION inspect_qualification_input_credential_receipt_v1(p_request_hash text)
RETURNS SETOF qualification_input_credential_receipt_admissions
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_credential_resolver_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input credential inspection caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF qualification_input_credential_admission_is_exact_v1(p_request_hash) IS TRUE THEN
    RETURN QUERY SELECT * FROM qualification_input_credential_receipt_admissions
    WHERE request_hash = p_request_hash;
  END IF;
END;
$function$;

CREATE FUNCTION resolve_qualification_input_source_receipt_admission_v1(
  p_admission_hash text
)
RETURNS SETOF qualification_input_source_receipt_admissions
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_admission qualification_input_source_receipt_admissions%ROWTYPE;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_source_verifier_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input source recovery caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF p_admission_hash IS NULL OR p_admission_hash !~ '^sha256:[0-9a-f]{64}$' THEN
    RETURN;
  END IF;
  SELECT * INTO v_admission FROM qualification_input_source_receipt_admissions
  WHERE admission_hash = p_admission_hash;
  IF NOT FOUND THEN RETURN; END IF;
  IF qualification_input_source_admission_is_exact_v1(
       v_admission.request_hash
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input source recovery admission is not exact'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NEXT v_admission;
END;
$function$;

CREATE FUNCTION resolve_qualification_input_credential_receipt_admission_v1(
  p_admission_hash text
)
RETURNS SETOF qualification_input_credential_receipt_admissions
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_admission qualification_input_credential_receipt_admissions%ROWTYPE;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_credential_resolver_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input credential recovery caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF p_admission_hash IS NULL OR p_admission_hash !~ '^sha256:[0-9a-f]{64}$' THEN
    RETURN;
  END IF;
  SELECT * INTO v_admission FROM qualification_input_credential_receipt_admissions
  WHERE admission_hash = p_admission_hash;
  IF NOT FOUND THEN RETURN; END IF;
  IF qualification_input_credential_admission_is_exact_v1(
       v_admission.request_hash
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input credential recovery admission is not exact'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NEXT v_admission;
END;
$function$;

CREATE FUNCTION qualification_input_precommit_plan_is_exact_v1(p_authority_id uuid)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_plan qualification_plan_authorities%ROWTYPE;
BEGIN
  IF p_authority_id IS NULL
     OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE THEN
    RETURN false;
  END IF;
  SELECT * INTO v_plan FROM qualification_plan_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  RETURN
    qualification_plan_sha256(v_plan.request_bytes) = v_plan.request_hash
    AND qualification_plan_sha256(v_plan.input_bytes) = v_plan.input_hash
    AND qualification_plan_sha256(v_plan.projection_bytes) = v_plan.projection_hash
    AND qualification_plan_sha256(v_plan.evidence_plan_bytes) = v_plan.evidence_plan_hash
    AND qualification_plan_sha256(v_plan.trust_bytes) = v_plan.trust_hash
    AND qualification_plan_sha256(v_plan.target_bytes) = v_plan.target_hash
    AND qualification_plan_sha256(v_plan.envelope_bytes) = v_plan.envelope_hash
    AND workflow_input_canonical_jsonb_bytes(v_plan.request_document) = v_plan.request_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.input_document) = v_plan.input_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.projection_document) = v_plan.projection_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.evidence_plan_document) = v_plan.evidence_plan_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.trust_document) = v_plan.trust_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.target_document) = v_plan.target_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.envelope_document) = v_plan.envelope_bytes
    AND v_plan.request_document = pg_catalog.jsonb_build_object(
      'authorityId', v_plan.authority_id::text,
      'inputAuthorityId', v_plan.input_authority_id::text,
      'operationId', v_plan.operation_id::text,
      'schemaVersion', 'worksflow-qualification-plan-freeze-request/v1'
    )
    AND v_plan.input_document->>'schemaVersion' =
      'worksflow-qualification-plan-input/v1'
    AND (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(
      v_plan.input_document->'source'
    )) = 4
    AND v_plan.input_document->'source'->>'commit' ~ '^[0-9a-f]{40}$'
    AND v_plan.input_document->'source'->'dirty' = 'false'::jsonb
    AND v_plan.input_document->'source'->>'treeDigestSchema' =
      'worksflow-source-content-tree/v1'
    AND v_plan.input_document->'source'->>'treeDigest' = v_plan.source_tree_digest
    AND v_plan.input_document->'credential' = v_plan.evidence_plan_document->'credentialSet'
    AND v_plan.envelope_document->>'authorityId' = v_plan.authority_id::text
    AND v_plan.envelope_document->>'operationId' = v_plan.operation_id::text
    AND v_plan.envelope_document->>'inputAuthorityId' = v_plan.input_authority_id::text
    AND v_plan.envelope_document->>'inputHash' = v_plan.input_hash
    AND v_plan.envelope_document->>'projectionHash' = v_plan.projection_hash
    AND v_plan.envelope_document->>'evidencePlanHash' = v_plan.evidence_plan_hash
    AND v_plan.envelope_document->>'targetHash' = v_plan.target_hash
    AND v_plan.envelope_document->>'trustHash' = v_plan.trust_hash;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_input_precommit_authority_record_is_exact_v1(
  p_authority_id uuid
)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_input_precommit_authorities%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_policy qualification_policy_authorities%ROWTYPE;
  v_plan qualification_plan_authorities%ROWTYPE;
  v_source qualification_input_source_receipt_admissions%ROWTYPE;
  v_credential qualification_input_credential_receipt_admissions%ROWTYPE;
  v_expected_request jsonb;
  v_expected_source_request jsonb;
  v_expected_credential_request jsonb;
  v_expected_authority jsonb;
  v_source_proof jsonb;
  v_credential_proof jsonb;
BEGIN
  IF p_authority_id IS NULL
     OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE THEN
    RETURN false;
  END IF;
  SELECT * INTO v_authority FROM qualification_input_precommit_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_wia FROM workflow_input_authorities
  WHERE authority_id = v_authority.workflow_input_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_policy FROM qualification_policy_authorities
  WHERE authority_id = v_authority.qualification_policy_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_plan FROM qualification_plan_authorities
  WHERE authority_id = v_authority.qualification_plan_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_source FROM qualification_input_source_receipt_admissions
  WHERE request_hash = v_authority.source_request_hash;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_credential FROM qualification_input_credential_receipt_admissions
  WHERE request_hash = v_authority.credential_request_hash;
  IF NOT FOUND THEN RETURN false; END IF;

  IF v_authority.request_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_authority.request_document)
     OR v_authority.source_request_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_authority.source_request_document)
     OR v_authority.credential_request_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_authority.credential_request_document)
     OR v_authority.authority_bytes IS DISTINCT FROM
       workflow_input_canonical_jsonb_bytes(v_authority.authority_document)
     OR v_authority.request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.request/v1', v_authority.request_bytes
     )
     OR v_authority.source_request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.source-request/v1',
       v_authority.source_request_bytes
     )
     OR v_authority.credential_request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.credential-request/v1',
       v_authority.credential_request_bytes
     )
     OR v_authority.authority_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.authority/v1', v_authority.authority_bytes
     )
     OR qualification_input_source_admission_is_exact_v1(
       v_authority.source_request_hash
     ) IS NOT TRUE
     OR qualification_input_credential_admission_is_exact_v1(
       v_authority.credential_request_hash
     ) IS NOT TRUE
     OR qualification_input_precommit_plan_is_exact_v1(
       v_authority.qualification_plan_authority_id
     ) IS NOT TRUE
     OR workflow_input_authority_record_is_exact(
       v_authority.workflow_input_authority_id
     ) IS NOT TRUE
     OR qualification_policy_authority_record_is_exact_v1(
       v_authority.qualification_policy_authority_id
     ) IS NOT TRUE THEN
    RETURN false;
  END IF;

  v_expected_request := pg_catalog.jsonb_build_object(
    'authorityId', v_authority.authority_id::text,
    'operationId', v_authority.operation_id::text,
    'qualificationPlanAuthorityId', v_plan.authority_id::text,
    'qualificationPolicyAuthorityId', v_policy.authority_id::text,
    'schemaVersion', 'worksflow-qualification-input-precommit-request/v1',
    'workflowInputAuthorityId', v_wia.authority_id::text
  );
  v_expected_source_request := pg_catalog.jsonb_build_object(
    'plan', pg_catalog.jsonb_build_object(
      'authorityHash', v_plan.envelope_hash, 'authorityId', v_plan.authority_id::text
    ),
    'policy', pg_catalog.jsonb_build_object(
      'authorityHash', v_policy.authority_hash, 'authorityId', v_policy.authority_id::text
    ),
    'schemaVersion', 'worksflow-qualification-source-verification-request/v1',
    'source', v_plan.input_document->'source',
    'sourcePolicyDigest', v_policy.plan_input_profile_document->>'sourcePolicyDigest',
    'verifier', pg_catalog.jsonb_build_object(
      'authorityId', v_source.authority_id,
      'executableDigest', v_source.executable_digest
    ),
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityHash', v_wia.authority_hash, 'authorityId', v_wia.authority_id::text
    )
  );
  v_expected_credential_request := pg_catalog.jsonb_build_object(
    'credentialProfile', v_policy.plan_input_profile_document->'credentialProfile',
    'credentialSet', v_plan.input_document->'credential',
    'plan', pg_catalog.jsonb_build_object(
      'authorityHash', v_plan.envelope_hash, 'authorityId', v_plan.authority_id::text
    ),
    'policy', pg_catalog.jsonb_build_object(
      'authorityHash', v_policy.authority_hash, 'authorityId', v_policy.authority_id::text
    ),
    'resolver', pg_catalog.jsonb_build_object(
      'authorityId', v_credential.authority_id,
      'executableDigest', v_credential.executable_digest
    ),
    'schemaVersion', 'worksflow-qualification-credential-resolution-request/v1',
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityHash', v_wia.authority_hash, 'authorityId', v_wia.authority_id::text
    )
  );
  v_source_proof := pg_catalog.jsonb_build_object(
    'admissionHash', v_source.admission_hash,
    'authorityId', v_source.authority_id,
    'executableDigest', v_source.executable_digest,
    'receiptHash', v_source.receipt_hash,
    'requestHash', v_source.request_hash
  );
  v_credential_proof := pg_catalog.jsonb_build_object(
    'admissionHash', v_credential.admission_hash,
    'authorityId', v_credential.authority_id,
    'executableDigest', v_credential.executable_digest,
    'receiptHash', v_credential.receipt_hash,
    'requestHash', v_credential.request_hash
  );
  v_expected_authority := pg_catalog.jsonb_build_object(
    'authorityId', v_authority.authority_id::text,
    'credentialProof', v_credential_proof,
    'credentialRequestHash', v_credential.request_hash,
    'issuedAt', qualification_input_precommit_timestamp_v1(v_authority.issued_at),
    'operationId', v_authority.operation_id::text,
    'plan', pg_catalog.jsonb_build_object(
      'authorityHash', v_plan.envelope_hash,
      'authorityId', v_plan.authority_id::text,
      'credentialSet', v_plan.input_document->'credential',
      'inputAuthorityId', v_plan.input_authority_id::text,
      'inputHash', v_plan.input_hash,
      'source', v_plan.input_document->'source'
    ),
    'policy', pg_catalog.jsonb_build_object(
      'authorityHash', v_policy.authority_hash,
      'authorityId', v_policy.authority_id::text,
      'credentialProfile', v_policy.plan_input_profile_document->'credentialProfile',
      'planInputProfileHash', v_policy.plan_input_profile_hash,
      'sourcePolicyDigest', v_policy.plan_input_profile_document->>'sourcePolicyDigest'
    ),
    'requestHash', v_authority.request_hash,
    'schemaVersion', 'worksflow-qualification-input-precommit-authority/v1',
    'sourceProof', v_source_proof,
    'sourceRequestHash', v_source.request_hash,
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityHash', v_wia.authority_hash,
      'authorityId', v_wia.authority_id::text,
      'inputHash', v_wia.input_hash,
      'qualificationPolicyAuthorityHash', v_wia.qualification_policy_authority_hash,
      'qualificationPolicyAuthorityId', v_wia.qualification_policy_authority_id::text
    )
  );

  RETURN v_authority.request_document = v_expected_request
    AND v_authority.source_request_document = v_expected_source_request
    AND v_authority.credential_request_document = v_expected_credential_request
    AND v_authority.authority_document = v_expected_authority
    AND v_authority.workflow_input_authority_hash = v_wia.authority_hash
    AND v_authority.workflow_input_hash = v_wia.input_hash
    AND v_authority.qualification_policy_authority_hash = v_policy.authority_hash
    AND v_authority.policy_plan_input_profile_hash = v_policy.plan_input_profile_hash
    AND v_authority.source_policy_digest =
      v_policy.plan_input_profile_document->>'sourcePolicyDigest'
    AND v_authority.credential_profile_document =
      v_policy.plan_input_profile_document->'credentialProfile'
    AND v_authority.qualification_plan_authority_hash = v_plan.envelope_hash
    AND v_authority.qualification_plan_input_authority_id = v_plan.input_authority_id
    AND v_authority.qualification_plan_input_hash = v_plan.input_hash
    AND v_authority.source_document = v_plan.input_document->'source'
    AND v_authority.credential_set_document = v_plan.input_document->'credential'
    AND v_authority.source_verifier_authority_id = v_source.authority_id
    AND v_authority.source_verifier_executable_digest = v_source.executable_digest
    AND v_authority.source_receipt_hash = v_source.receipt_hash
    AND v_authority.source_admission_hash = v_source.admission_hash
    AND v_authority.credential_resolver_authority_id = v_credential.authority_id
    AND v_authority.credential_resolver_executable_digest = v_credential.executable_digest
    AND v_authority.credential_receipt_hash = v_credential.receipt_hash
    AND v_authority.credential_admission_hash = v_credential.admission_hash
    AND v_wia.qualification_policy_authority_id = v_policy.authority_id
    AND v_wia.qualification_policy_authority_hash = v_policy.authority_hash
    AND v_plan.input_authority_id = v_wia.authority_id
    AND v_policy.plan_input_profile_document->'credentialProfile'->>'authorityId' =
      v_plan.input_document->'credential'->>'issuer'
    AND v_policy.plan_input_profile_document->'credentialProfile'->>'audience' =
      v_plan.input_document->'credential'->>'audience'
    AND v_policy.plan_input_profile_document->'credentialProfile'->>'issuanceArtifactId' =
      v_plan.input_document->'credential'->>'issuanceArtifactId'
    AND v_policy.plan_input_profile_document->'credentialProfile'->>'revocationArtifactId' =
      v_plan.input_document->'credential'->>'revocationArtifactId'
    AND (SELECT pg_catalog.count(*) FROM qualification_input_precommit_identity_reservations
         WHERE authority_id = v_authority.authority_id) = 2
    AND EXISTS (
      SELECT 1 FROM qualification_input_precommit_identity_reservations
      WHERE authority_id = v_authority.authority_id
        AND identity_value = v_authority.operation_id
        AND identity_kind = 'operation' AND ordinal = 0
        AND reserved_at = v_authority.issued_at
    )
    AND EXISTS (
      SELECT 1 FROM qualification_input_precommit_identity_reservations
      WHERE authority_id = v_authority.authority_id
        AND identity_value = v_authority.authority_id
        AND identity_kind = 'authority' AND ordinal = 1
        AND reserved_at = v_authority.issued_at
    )
    AND EXISTS (
      SELECT 1 FROM qualification_input_precommit_wia_reservations
      WHERE authority_id = v_authority.authority_id
        AND workflow_input_authority_id = v_authority.workflow_input_authority_id
        AND reserved_at = v_authority.issued_at
    )
    AND EXISTS (
      SELECT 1 FROM qualification_input_precommit_plan_reservations
      WHERE authority_id = v_authority.authority_id
        AND qualification_plan_authority_id = v_authority.qualification_plan_authority_id
        AND reserved_at = v_authority.issued_at
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION issue_qualification_input_precommit_v1(
  p_operation_id uuid,
  p_authority_id uuid,
  p_workflow_input_authority_id uuid,
  p_qualification_policy_authority_id uuid,
  p_qualification_plan_authority_id uuid,
  p_request_hash text,
  p_request_bytes bytea,
  p_request_document jsonb,
  p_source_request_hash text,
  p_source_request_bytes bytea,
  p_source_request_document jsonb,
  p_credential_request_hash text,
  p_credential_request_bytes bytea,
  p_credential_request_document jsonb,
  p_authority_hash text,
  p_authority_bytes bytea,
  p_authority_document jsonb
)
RETURNS SETOF qualification_input_precommit_authorities
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_existing qualification_input_precommit_authorities%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_policy qualification_policy_authorities%ROWTYPE;
  v_plan qualification_plan_authorities%ROWTYPE;
  v_source_binding qualification_input_precommit_executable_binding_generations%ROWTYPE;
  v_credential_binding qualification_input_precommit_executable_binding_generations%ROWTYPE;
  v_source qualification_input_source_receipt_admissions%ROWTYPE;
  v_credential qualification_input_credential_receipt_admissions%ROWTYPE;
  v_project_id uuid;
  v_issued_at timestamptz;
  v_expected_request jsonb;
  v_expected_source_request jsonb;
  v_expected_credential_request jsonb;
  v_expected_authority jsonb;
  v_source_proof jsonb;
  v_credential_proof jsonb;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_input_precommit_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable'
     OR pg_catalog.current_setting('transaction_read_only') <> 'off'
     OR pg_catalog.pg_is_in_recovery() THEN
    RAISE EXCEPTION 'Qualification Input Precommit requires a serializable read-write primary transaction'
      USING ERRCODE = 'WIP01';
  END IF;
  -- No authority relation is touched before the rollout fence.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );

  IF p_operation_id IS NULL OR p_authority_id IS NULL
     OR p_workflow_input_authority_id IS NULL
     OR p_qualification_policy_authority_id IS NULL
     OR p_qualification_plan_authority_id IS NULL
     OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_workflow_input_authority_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_qualification_policy_authority_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_qualification_plan_authority_id::text) IS NOT TRUE
     OR pg_catalog.array_length(ARRAY[
       p_operation_id,p_authority_id,p_workflow_input_authority_id,
       p_qualification_policy_authority_id,p_qualification_plan_authority_id
     ], 1) <> (
       SELECT pg_catalog.count(DISTINCT value)
       FROM pg_catalog.unnest(ARRAY[
         p_operation_id,p_authority_id,p_workflow_input_authority_id,
         p_qualification_policy_authority_id,p_qualification_plan_authority_id
       ]) AS identity(value)
     ) THEN
    RAISE EXCEPTION 'Qualification Input Precommit identity set is invalid'
      USING ERRCODE = 'WIP01';
  END IF;

  SELECT * INTO v_existing
  FROM qualification_input_precommit_authorities
  WHERE operation_id = p_operation_id
  FOR SHARE;
  IF FOUND THEN
    IF v_existing.authority_id IS DISTINCT FROM p_authority_id
       OR v_existing.workflow_input_authority_id IS DISTINCT FROM p_workflow_input_authority_id
       OR v_existing.qualification_policy_authority_id IS DISTINCT FROM p_qualification_policy_authority_id
       OR v_existing.qualification_plan_authority_id IS DISTINCT FROM p_qualification_plan_authority_id
       OR v_existing.request_hash IS DISTINCT FROM p_request_hash
       OR v_existing.request_bytes IS DISTINCT FROM p_request_bytes
       OR v_existing.request_document IS DISTINCT FROM p_request_document
       OR v_existing.source_request_hash IS DISTINCT FROM p_source_request_hash
       OR v_existing.source_request_bytes IS DISTINCT FROM p_source_request_bytes
       OR v_existing.source_request_document IS DISTINCT FROM p_source_request_document
       OR v_existing.credential_request_hash IS DISTINCT FROM p_credential_request_hash
       OR v_existing.credential_request_bytes IS DISTINCT FROM p_credential_request_bytes
       OR v_existing.credential_request_document IS DISTINCT FROM p_credential_request_document
       OR v_existing.authority_hash IS DISTINCT FROM p_authority_hash
       OR v_existing.authority_bytes IS DISTINCT FROM p_authority_bytes
       OR v_existing.authority_document IS DISTINCT FROM p_authority_document
       OR qualification_input_precommit_authority_record_is_exact_v1(
         v_existing.authority_id
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Input Precommit operation conflicts with immutable bytes'
        USING ERRCODE = 'WIP02';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  IF p_request_document IS NULL OR p_source_request_document IS NULL
     OR p_credential_request_document IS NULL OR p_authority_document IS NULL
     OR p_request_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_request_document)
     OR p_source_request_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_source_request_document)
     OR p_credential_request_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_credential_request_document)
     OR p_authority_bytes IS DISTINCT FROM workflow_input_canonical_jsonb_bytes(p_authority_document)
     OR p_request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.request/v1', p_request_bytes
     )
     OR p_source_request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.source-request/v1', p_source_request_bytes
     )
     OR p_credential_request_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.credential-request/v1',
       p_credential_request_bytes
     )
     OR p_authority_hash IS DISTINCT FROM qualification_input_precommit_hash_v1(
       'worksflow.qualification-input-precommit.authority/v1', p_authority_bytes
     ) THEN
    RAISE EXCEPTION 'Qualification Input Precommit canonical bytes or domain hashes are invalid'
      USING ERRCODE = 'WIP01';
  END IF;

  BEGIN
    IF p_authority_document->>'issuedAt' !~
       '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$' THEN
      RAISE EXCEPTION 'invalid issuedAt';
    END IF;
    v_issued_at := (p_authority_document->>'issuedAt')::timestamptz;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Qualification Input Precommit issuedAt is invalid'
      USING ERRCODE = 'WIP01';
  END;
  IF v_issued_at IS DISTINCT FROM pg_catalog.date_trunc('milliseconds', v_issued_at)
     OR qualification_input_precommit_timestamp_v1(v_issued_at) IS DISTINCT FROM
       p_authority_document->>'issuedAt'
     OR pg_catalog.abs(extract(epoch FROM (
       pg_catalog.clock_timestamp() - v_issued_at
     ))) > 300 THEN
    RAISE EXCEPTION 'Qualification Input Precommit issuedAt is outside trusted database time'
      USING ERRCODE = 'WIP03';
  END IF;

  -- Existing WIA lock order is authoritative: fence, project, WIA assertion
  -- (which owns its Policy/run/node order), reviewed bindings, Plan, receipts.
  SELECT project_id INTO v_project_id FROM workflow_input_authorities
  WHERE authority_id = p_workflow_input_authority_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Input Precommit WIA is not found'
      USING ERRCODE = 'WIP03';
  END IF;
  PERFORM 1 FROM projects WHERE id = v_project_id FOR UPDATE;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-input-precommit:project:' || v_project_id::text, 0
    )
  );
  PERFORM 1 FROM assert_current_workflow_input_authority_v1(
    p_workflow_input_authority_id
  );
  SELECT * INTO v_wia FROM workflow_input_authorities
  WHERE authority_id = p_workflow_input_authority_id
  FOR SHARE;
  IF NOT FOUND OR workflow_input_authority_record_is_exact(v_wia.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit WIA is stale or not exact'
      USING ERRCODE = 'WIP03';
  END IF;

  SELECT binding_history.* INTO v_source_binding
  FROM qualification_input_precommit_executable_binding_heads AS binding_head
  JOIN qualification_input_precommit_executable_binding_generations AS binding_history
    ON binding_history.binding_role = binding_head.binding_role
   AND binding_history.generation = binding_head.generation
   AND binding_history.authority_id = binding_head.authority_id
   AND binding_history.executable_digest = binding_head.executable_digest
  WHERE binding_head.binding_role = 'source-verification'
  FOR SHARE OF binding_head, binding_history;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Input source verifier binding is not reviewed'
      USING ERRCODE = 'WIP03';
  END IF;
  SELECT binding_history.* INTO v_credential_binding
  FROM qualification_input_precommit_executable_binding_heads AS binding_head
  JOIN qualification_input_precommit_executable_binding_generations AS binding_history
    ON binding_history.binding_role = binding_head.binding_role
   AND binding_history.generation = binding_head.generation
   AND binding_history.authority_id = binding_head.authority_id
   AND binding_history.executable_digest = binding_head.executable_digest
  WHERE binding_head.binding_role = 'credential-resolution'
  FOR SHARE OF binding_head, binding_history;
  IF NOT FOUND
     OR v_source_binding.authority_id = v_credential_binding.authority_id
     OR v_source_binding.executable_digest = v_credential_binding.executable_digest THEN
    RAISE EXCEPTION 'Qualification Input executable roles are absent or aliased'
      USING ERRCODE = 'WIP03';
  END IF;

  SELECT * INTO v_policy FROM qualification_policy_authorities
  WHERE authority_id = p_qualification_policy_authority_id
  FOR SHARE;
  IF NOT FOUND OR v_wia.qualification_policy_authority_id <> v_policy.authority_id
     OR v_wia.qualification_policy_authority_hash <> v_policy.authority_hash THEN
    RAISE EXCEPTION 'Qualification Input WIA does not bind the exact Policy'
      USING ERRCODE = 'WIP03';
  END IF;
  PERFORM 1 FROM assert_current_qualification_policy_authority_v1(v_policy.authority_id);
  IF qualification_policy_authority_record_is_exact_v1(v_policy.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Policy is not exact'
      USING ERRCODE = 'WIP03';
  END IF;

  SELECT * INTO v_plan FROM qualification_plan_authorities
  WHERE authority_id = p_qualification_plan_authority_id
  FOR SHARE;
  IF NOT FOUND OR v_plan.input_authority_id <> v_wia.authority_id
     OR qualification_input_precommit_plan_is_exact_v1(v_plan.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Plan does not bind the exact WIA'
      USING ERRCODE = 'WIP03';
  END IF;

  SELECT * INTO v_source FROM qualification_input_source_receipt_admissions
  WHERE request_hash = p_source_request_hash
  FOR SHARE;
  IF NOT FOUND OR qualification_input_source_admission_is_exact_v1(
       p_source_request_hash
     ) IS NOT TRUE
     OR v_source.authority_id <> v_source_binding.authority_id
     OR v_source.executable_digest <> v_source_binding.executable_digest
     OR v_source.request_bytes IS DISTINCT FROM p_source_request_bytes
     OR v_source.request_document IS DISTINCT FROM p_source_request_document THEN
    RAISE EXCEPTION 'Qualification Input source receipt admission is absent or stale'
      USING ERRCODE = 'WIP03';
  END IF;
  SELECT * INTO v_credential FROM qualification_input_credential_receipt_admissions
  WHERE request_hash = p_credential_request_hash
  FOR SHARE;
  IF NOT FOUND OR qualification_input_credential_admission_is_exact_v1(
       p_credential_request_hash
     ) IS NOT TRUE
     OR v_credential.authority_id <> v_credential_binding.authority_id
     OR v_credential.executable_digest <> v_credential_binding.executable_digest
     OR v_credential.request_bytes IS DISTINCT FROM p_credential_request_bytes
     OR v_credential.request_document IS DISTINCT FROM p_credential_request_document THEN
    RAISE EXCEPTION 'Qualification Input credential receipt admission is absent or stale'
      USING ERRCODE = 'WIP03';
  END IF;
  IF (
    SELECT pg_catalog.count(DISTINCT proof_hash)
    FROM pg_catalog.unnest(ARRAY[
      p_source_request_hash,
      v_source.receipt_hash,
      v_source.admission_hash,
      p_credential_request_hash,
      v_credential.receipt_hash,
      v_credential.admission_hash
    ]) AS proof_domain(proof_hash)
  ) <> 6 THEN
    RAISE EXCEPTION 'Qualification Input source and credential proof hash domains alias'
      USING ERRCODE = 'WIP03';
  END IF;

  IF v_policy.plan_input_profile_document->'credentialProfile'->>'authorityId' IS DISTINCT FROM
       v_plan.input_document->'credential'->>'issuer'
     OR v_policy.plan_input_profile_document->'credentialProfile'->>'audience' IS DISTINCT FROM
       v_plan.input_document->'credential'->>'audience'
     OR v_policy.plan_input_profile_document->'credentialProfile'->>'issuanceArtifactId' IS DISTINCT FROM
       v_plan.input_document->'credential'->>'issuanceArtifactId'
     OR v_policy.plan_input_profile_document->'credentialProfile'->>'revocationArtifactId' IS DISTINCT FROM
       v_plan.input_document->'credential'->>'revocationArtifactId'
     OR v_policy.plan_input_profile_document->'credentialProfile'->>'memberRequestSetDigest' IN (
       v_plan.input_document->'credential'->>'memberBindingsDigest',
       v_plan.input_document->'credential'->>'setHandleHash'
     )
     OR v_policy.plan_input_profile_document->>'sourcePolicyDigest' =
       v_plan.input_document->'source'->>'treeDigest' THEN
    RAISE EXCEPTION 'Qualification Input Policy and Plan projections drift or alias domains'
      USING ERRCODE = 'WIP03';
  END IF;

  v_expected_request := pg_catalog.jsonb_build_object(
    'authorityId', p_authority_id::text,
    'operationId', p_operation_id::text,
    'qualificationPlanAuthorityId', v_plan.authority_id::text,
    'qualificationPolicyAuthorityId', v_policy.authority_id::text,
    'schemaVersion', 'worksflow-qualification-input-precommit-request/v1',
    'workflowInputAuthorityId', v_wia.authority_id::text
  );
  v_expected_source_request := pg_catalog.jsonb_build_object(
    'plan', pg_catalog.jsonb_build_object(
      'authorityHash', v_plan.envelope_hash, 'authorityId', v_plan.authority_id::text
    ),
    'policy', pg_catalog.jsonb_build_object(
      'authorityHash', v_policy.authority_hash, 'authorityId', v_policy.authority_id::text
    ),
    'schemaVersion', 'worksflow-qualification-source-verification-request/v1',
    'source', v_plan.input_document->'source',
    'sourcePolicyDigest', v_policy.plan_input_profile_document->>'sourcePolicyDigest',
    'verifier', pg_catalog.jsonb_build_object(
      'authorityId', v_source_binding.authority_id,
      'executableDigest', v_source_binding.executable_digest
    ),
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityHash', v_wia.authority_hash, 'authorityId', v_wia.authority_id::text
    )
  );
  v_expected_credential_request := pg_catalog.jsonb_build_object(
    'credentialProfile', v_policy.plan_input_profile_document->'credentialProfile',
    'credentialSet', v_plan.input_document->'credential',
    'plan', pg_catalog.jsonb_build_object(
      'authorityHash', v_plan.envelope_hash, 'authorityId', v_plan.authority_id::text
    ),
    'policy', pg_catalog.jsonb_build_object(
      'authorityHash', v_policy.authority_hash, 'authorityId', v_policy.authority_id::text
    ),
    'resolver', pg_catalog.jsonb_build_object(
      'authorityId', v_credential_binding.authority_id,
      'executableDigest', v_credential_binding.executable_digest
    ),
    'schemaVersion', 'worksflow-qualification-credential-resolution-request/v1',
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityHash', v_wia.authority_hash, 'authorityId', v_wia.authority_id::text
    )
  );
  v_source_proof := pg_catalog.jsonb_build_object(
    'admissionHash', v_source.admission_hash,
    'authorityId', v_source.authority_id,
    'executableDigest', v_source.executable_digest,
    'receiptHash', v_source.receipt_hash,
    'requestHash', v_source.request_hash
  );
  v_credential_proof := pg_catalog.jsonb_build_object(
    'admissionHash', v_credential.admission_hash,
    'authorityId', v_credential.authority_id,
    'executableDigest', v_credential.executable_digest,
    'receiptHash', v_credential.receipt_hash,
    'requestHash', v_credential.request_hash
  );
  v_expected_authority := pg_catalog.jsonb_build_object(
    'authorityId', p_authority_id::text,
    'credentialProof', v_credential_proof,
    'credentialRequestHash', v_credential.request_hash,
    'issuedAt', qualification_input_precommit_timestamp_v1(v_issued_at),
    'operationId', p_operation_id::text,
    'plan', pg_catalog.jsonb_build_object(
      'authorityHash', v_plan.envelope_hash,
      'authorityId', v_plan.authority_id::text,
      'credentialSet', v_plan.input_document->'credential',
      'inputAuthorityId', v_plan.input_authority_id::text,
      'inputHash', v_plan.input_hash,
      'source', v_plan.input_document->'source'
    ),
    'policy', pg_catalog.jsonb_build_object(
      'authorityHash', v_policy.authority_hash,
      'authorityId', v_policy.authority_id::text,
      'credentialProfile', v_policy.plan_input_profile_document->'credentialProfile',
      'planInputProfileHash', v_policy.plan_input_profile_hash,
      'sourcePolicyDigest', v_policy.plan_input_profile_document->>'sourcePolicyDigest'
    ),
    'requestHash', p_request_hash,
    'schemaVersion', 'worksflow-qualification-input-precommit-authority/v1',
    'sourceProof', v_source_proof,
    'sourceRequestHash', v_source.request_hash,
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityHash', v_wia.authority_hash,
      'authorityId', v_wia.authority_id::text,
      'inputHash', v_wia.input_hash,
      'qualificationPolicyAuthorityHash', v_wia.qualification_policy_authority_hash,
      'qualificationPolicyAuthorityId', v_wia.qualification_policy_authority_id::text
    )
  );
  IF p_request_document IS DISTINCT FROM v_expected_request
     OR p_source_request_document IS DISTINCT FROM v_expected_source_request
     OR p_credential_request_document IS DISTINCT FROM v_expected_credential_request
     OR p_authority_document IS DISTINCT FROM v_expected_authority THEN
    RAISE EXCEPTION 'Qualification Input Precommit documents differ from locked authorities'
      USING ERRCODE = 'WIP03';
  END IF;

  IF EXISTS (
       SELECT 1 FROM qualification_input_precommit_identity_reservations
       WHERE identity_value IN (p_operation_id,p_authority_id)
     ) OR EXISTS (
       SELECT 1 FROM workflow_input_authority_identity_reservations
       WHERE identity_value IN (p_operation_id,p_authority_id)
     ) OR EXISTS (
       SELECT 1 FROM qualification_policy_identity_reservations
       WHERE identity_value IN (p_operation_id,p_authority_id)
     ) OR EXISTS (
       SELECT 1 FROM qualification_plan_identity_reservations
       WHERE identity_value IN (p_operation_id::text,p_authority_id::text)
     ) THEN
    RAISE EXCEPTION 'Qualification Input Precommit identity collides with immutable authority history'
      USING ERRCODE = 'WIP02';
  END IF;

  INSERT INTO qualification_input_precommit_authorities(
    authority_id, operation_id,
    request_hash, request_bytes, request_document,
    source_request_hash, source_request_bytes, source_request_document,
    credential_request_hash, credential_request_bytes, credential_request_document,
    authority_hash, authority_bytes, authority_document,
    workflow_input_authority_id, workflow_input_authority_hash, workflow_input_hash,
    qualification_policy_authority_id, qualification_policy_authority_hash,
    policy_plan_input_profile_hash, source_policy_digest, credential_profile_document,
    qualification_plan_authority_id, qualification_plan_authority_hash,
    qualification_plan_input_authority_id, qualification_plan_input_hash,
    source_document, credential_set_document,
    source_verifier_authority_id, source_verifier_executable_digest,
    source_receipt_hash, source_admission_hash,
    credential_resolver_authority_id, credential_resolver_executable_digest,
    credential_receipt_hash, credential_admission_hash, issued_at
  ) VALUES (
    p_authority_id, p_operation_id,
    p_request_hash, p_request_bytes, p_request_document,
    p_source_request_hash, p_source_request_bytes, p_source_request_document,
    p_credential_request_hash, p_credential_request_bytes, p_credential_request_document,
    p_authority_hash, p_authority_bytes, p_authority_document,
    v_wia.authority_id, v_wia.authority_hash, v_wia.input_hash,
    v_policy.authority_id, v_policy.authority_hash,
    v_policy.plan_input_profile_hash,
    v_policy.plan_input_profile_document->>'sourcePolicyDigest',
    v_policy.plan_input_profile_document->'credentialProfile',
    v_plan.authority_id, v_plan.envelope_hash, v_plan.input_authority_id,
    v_plan.input_hash, v_plan.input_document->'source', v_plan.input_document->'credential',
    v_source.authority_id, v_source.executable_digest,
    v_source.receipt_hash, v_source.admission_hash,
    v_credential.authority_id, v_credential.executable_digest,
    v_credential.receipt_hash, v_credential.admission_hash, v_issued_at
  );
  INSERT INTO qualification_input_precommit_identity_reservations(
    identity_value, authority_id, identity_kind, ordinal, reserved_at
  ) VALUES
    (p_operation_id, p_authority_id, 'operation', 0, v_issued_at),
    (p_authority_id, p_authority_id, 'authority', 1, v_issued_at);
  INSERT INTO qualification_input_precommit_wia_reservations(
    workflow_input_authority_id, authority_id, reserved_at
  ) VALUES (v_wia.authority_id, p_authority_id, v_issued_at);
  INSERT INTO qualification_input_precommit_plan_reservations(
    qualification_plan_authority_id, authority_id, reserved_at
  ) VALUES (v_plan.authority_id, p_authority_id, v_issued_at);

  IF qualification_input_precommit_authority_record_is_exact_v1(p_authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit append did not close exactly'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN QUERY SELECT * FROM qualification_input_precommit_authorities
  WHERE authority_id = p_authority_id;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Input Precommit conflicts with immutable state'
      USING ERRCODE = 'WIP02';
END;
$function$;

CREATE FUNCTION inspect_qualification_input_precommit_operation_v1(p_operation_id uuid)
RETURNS SETOF qualification_input_precommit_authorities
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_input_precommit_authorities%ROWTYPE;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_input_precommit_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit inspection caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF p_operation_id IS NULL
     OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  SELECT * INTO v_authority FROM qualification_input_precommit_authorities
  WHERE operation_id = p_operation_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF qualification_input_precommit_authority_record_is_exact_v1(
       v_authority.authority_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit stored operation is not exact'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NEXT v_authority;
END;
$function$;

CREATE FUNCTION resolve_qualification_input_precommit_authority_v1(p_authority_id uuid)
RETURNS SETOF qualification_input_precommit_authorities
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_input_precommit_authorities%ROWTYPE;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_input_precommit_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit resolver caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF p_authority_id IS NULL
     OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  SELECT * INTO v_authority FROM qualification_input_precommit_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN RETURN; END IF;
  IF qualification_input_precommit_authority_record_is_exact_v1(
       v_authority.authority_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit stored authority is not exact'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NEXT v_authority;
END;
$function$;

-- Promotion receives no table privilege.  This one exact resolver locks the
-- precommit and both local receipt admissions on the caller's serializable
-- transaction, and returns zero rows for a missing WIA+Plan pair.
CREATE FUNCTION resolve_qualification_input_precommit_for_promotion_v1(
  p_workflow_input_authority_id uuid,
  p_qualification_plan_authority_id uuid
)
RETURNS SETOF qualification_input_precommit_authorities
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_input_precommit_authorities%ROWTYPE;
  v_source_binding qualification_input_precommit_executable_binding_generations%ROWTYPE;
  v_credential_binding qualification_input_precommit_executable_binding_generations%ROWTYPE;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_promotion_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit Promotion resolver caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable'
     OR pg_catalog.current_setting('transaction_read_only') <> 'off'
     OR pg_catalog.pg_is_in_recovery() THEN
    RAISE EXCEPTION 'Qualification Input Precommit Promotion resolver requires a serializable read-write primary transaction'
      USING ERRCODE = 'WIP01';
  END IF;
  IF p_workflow_input_authority_id IS NULL
     OR p_qualification_plan_authority_id IS NULL
     OR workflow_input_uuid_is_exact(p_workflow_input_authority_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_qualification_plan_authority_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  SELECT * INTO v_authority FROM qualification_input_precommit_authorities
  WHERE workflow_input_authority_id = p_workflow_input_authority_id
    AND qualification_plan_authority_id = p_qualification_plan_authority_id
  FOR SHARE;
  IF NOT FOUND THEN RETURN; END IF;
  SELECT * INTO v_source_binding
  FROM qualification_input_precommit_executable_binding_generations
  WHERE binding_role = 'source-verification'
    AND authority_id = v_authority.source_verifier_authority_id
    AND executable_digest = v_authority.source_verifier_executable_digest
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Input Precommit reviewed source binding is missing'
      USING ERRCODE = 'WIP02';
  END IF;
  SELECT * INTO v_credential_binding
  FROM qualification_input_precommit_executable_binding_generations
  WHERE binding_role = 'credential-resolution'
    AND authority_id = v_authority.credential_resolver_authority_id
    AND executable_digest = v_authority.credential_resolver_executable_digest
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Input Precommit reviewed credential binding is missing'
      USING ERRCODE = 'WIP02';
  END IF;
  PERFORM 1 FROM qualification_input_source_receipt_admissions
  WHERE request_hash = v_authority.source_request_hash FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Input Precommit source admission is missing'
      USING ERRCODE = 'WIP02';
  END IF;
  PERFORM 1 FROM qualification_input_credential_receipt_admissions
  WHERE request_hash = v_authority.credential_request_hash FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Input Precommit credential admission is missing'
      USING ERRCODE = 'WIP02';
  END IF;
  IF qualification_input_precommit_authority_record_is_exact_v1(
       v_authority.authority_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit Promotion binding is not exact'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NEXT v_authority;
END;
$function$;

CREATE FUNCTION enforce_qualification_input_source_admission_closure_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
BEGIN
  IF qualification_input_source_admission_is_exact_v1(NEW.request_hash) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input source admission deferred closure failed'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NULL;
END;
$function$;

CREATE FUNCTION enforce_qualification_input_credential_admission_closure_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
BEGIN
  IF qualification_input_credential_admission_is_exact_v1(NEW.request_hash) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input credential admission deferred closure failed'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NULL;
END;
$function$;

CREATE FUNCTION enforce_qualification_input_precommit_authority_closure_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
BEGIN
  IF qualification_input_precommit_authority_record_is_exact_v1(
       NEW.authority_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Input Precommit deferred closure failed'
      USING ERRCODE = 'WIP02';
  END IF;
  RETURN NULL;
END;
$function$;

CREATE CONSTRAINT TRIGGER qualification_input_source_admission_exact_closure
AFTER INSERT ON qualification_input_source_receipt_admissions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_qualification_input_source_admission_closure_v1();
CREATE CONSTRAINT TRIGGER qualification_input_credential_admission_exact_closure
AFTER INSERT ON qualification_input_credential_receipt_admissions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_qualification_input_credential_admission_closure_v1();
CREATE CONSTRAINT TRIGGER qualification_input_precommit_authority_exact_closure
AFTER INSERT ON qualification_input_precommit_authorities
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION enforce_qualification_input_precommit_authority_closure_v1();

COMMENT ON TABLE qualification_input_precommit_authorities IS
  'Append-only exact source and credential input authority; not a Promotion approval.';
COMMENT ON TABLE qualification_input_source_receipt_admissions IS
  'First-commit-wins local admission of a privately verified external source receipt hash.';
COMMENT ON TABLE qualification_input_credential_receipt_admissions IS
  'First-commit-wins local admission of a privately resolved external credential receipt hash.';

CREATE FUNCTION qualification_input_precommit_apply_security_v1()
RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
DECLARE
  v_schema text := pg_catalog.current_schema();
  v_role text;
  v_role_sql text;
  v_table text;
  v_routine record;
BEGIN
  FOR v_table IN SELECT pg_catalog.unnest(ARRAY[
    'qualification_input_precommit_executable_binding_generations',
    'qualification_input_precommit_executable_binding_heads',
    'qualification_input_source_receipt_admissions',
    'qualification_input_credential_receipt_admissions',
    'qualification_input_precommit_authorities',
    'qualification_input_precommit_identity_reservations',
    'qualification_input_precommit_wia_reservations',
    'qualification_input_precommit_plan_reservations'
  ]) LOOP
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON TABLE %I.%I FROM PUBLIC', v_schema, v_table
    );
  END LOOP;

  FOR v_routine IN
    SELECT procedure.proname,
      pg_catalog.pg_get_function_identity_arguments(procedure.oid) AS arguments
    FROM pg_catalog.pg_proc AS procedure
    WHERE procedure.pronamespace = v_schema::pg_catalog.regnamespace
      AND (
        procedure.proname LIKE 'qualification_input_%'
        OR procedure.proname LIKE 'admit_qualification_input_%'
        OR procedure.proname LIKE 'inspect_qualification_input_%'
        OR procedure.proname LIKE 'resolve_qualification_input_%'
        OR procedure.proname LIKE 'review_qualification_input_%'
        OR procedure.proname LIKE 'reject_qualification_input_%'
        OR procedure.proname LIKE 'enforce_qualification_input_%'
        OR procedure.proname LIKE 'issue_qualification_input_%'
      )
  LOOP
    EXECUTE pg_catalog.format(
      'ALTER FUNCTION %I.%I(%s) SET search_path TO pg_catalog, %I',
      v_schema, v_routine.proname, v_routine.arguments, v_schema
    );
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON FUNCTION %I.%I(%s) FROM PUBLIC',
      v_schema, v_routine.proname, v_routine.arguments
    );
  END LOOP;

  FOREACH v_role IN ARRAY ARRAY[
    'worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_qualification_plan_operator','worksflow_qualification_policy_operator',
    'worksflow_workflow_input_authority_operator','worksflow_qualification_promotion_operator',
    'worksflow_qualification_input_precommit_operator',
    'worksflow_qualification_source_verifier_operator',
    'worksflow_qualification_credential_resolver_operator'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_role) THEN
      v_role_sql := pg_catalog.quote_ident(v_role);
      FOREACH v_table IN ARRAY ARRAY[
        'qualification_input_precommit_executable_binding_generations',
        'qualification_input_precommit_executable_binding_heads',
        'qualification_input_source_receipt_admissions',
        'qualification_input_credential_receipt_admissions',
        'qualification_input_precommit_authorities',
        'qualification_input_precommit_identity_reservations',
        'qualification_input_precommit_wia_reservations',
        'qualification_input_precommit_plan_reservations'
      ] LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON TABLE %I.%I FROM %s', v_schema, v_table, v_role_sql
        );
      END LOOP;
      FOR v_routine IN
        SELECT procedure.proname,
          pg_catalog.pg_get_function_identity_arguments(procedure.oid) AS arguments
        FROM pg_catalog.pg_proc AS procedure
        WHERE procedure.pronamespace = v_schema::pg_catalog.regnamespace
          AND (
            procedure.proname LIKE 'qualification_input_%'
            OR procedure.proname LIKE 'admit_qualification_input_%'
            OR procedure.proname LIKE 'inspect_qualification_input_%'
            OR procedure.proname LIKE 'resolve_qualification_input_%'
            OR procedure.proname LIKE 'review_qualification_input_%'
            OR procedure.proname LIKE 'reject_qualification_input_%'
            OR procedure.proname LIKE 'enforce_qualification_input_%'
            OR procedure.proname LIKE 'issue_qualification_input_%'
          )
      LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON FUNCTION %I.%I(%s) FROM %s',
          v_schema, v_routine.proname, v_routine.arguments, v_role_sql
        );
      END LOOP;
    END IF;
  END LOOP;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_input_precommit_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_input_precommit_operator',
      v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.issue_qualification_input_precommit_v1('
      'uuid,uuid,uuid,uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) '
      'TO worksflow_qualification_input_precommit_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.inspect_qualification_input_precommit_operation_v1(uuid) '
      'TO worksflow_qualification_input_precommit_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.resolve_qualification_input_precommit_authority_v1(uuid) '
      'TO worksflow_qualification_input_precommit_operator', v_schema
    );
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_source_verifier_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_source_verifier_operator',
      v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.admit_qualification_input_source_receipt_v1('
      'text,bytea,jsonb,text,bytea,jsonb) '
      'TO worksflow_qualification_source_verifier_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.inspect_qualification_input_source_receipt_v1(text) '
      'TO worksflow_qualification_source_verifier_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.resolve_qualification_input_source_receipt_admission_v1(text) '
      'TO worksflow_qualification_source_verifier_operator', v_schema
    );
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_credential_resolver_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_credential_resolver_operator',
      v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.admit_qualification_input_credential_receipt_v1('
      'text,bytea,jsonb,text,bytea,jsonb) '
      'TO worksflow_qualification_credential_resolver_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.inspect_qualification_input_credential_receipt_v1(text) '
      'TO worksflow_qualification_credential_resolver_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.resolve_qualification_input_credential_receipt_admission_v1(text) '
      'TO worksflow_qualification_credential_resolver_operator', v_schema
    );
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_promotion_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_promotion_operator',
      v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.resolve_qualification_input_precommit_for_promotion_v1(uuid,uuid) '
      'TO worksflow_qualification_promotion_operator', v_schema
    );
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner'
  ) THEN
    FOREACH v_table IN ARRAY ARRAY[
      'qualification_input_precommit_executable_binding_generations',
      'qualification_input_precommit_executable_binding_heads',
      'qualification_input_source_receipt_admissions',
      'qualification_input_credential_receipt_admissions',
      'qualification_input_precommit_authorities',
      'qualification_input_precommit_identity_reservations',
      'qualification_input_precommit_wia_reservations',
      'qualification_input_precommit_plan_reservations'
    ] LOOP
      EXECUTE pg_catalog.format(
        'ALTER TABLE %I.%I OWNER TO worksflow_migration_owner', v_schema, v_table
      );
    END LOOP;
    FOR v_routine IN
      SELECT procedure.proname,
        pg_catalog.pg_get_function_identity_arguments(procedure.oid) AS arguments
      FROM pg_catalog.pg_proc AS procedure
      WHERE procedure.pronamespace = v_schema::pg_catalog.regnamespace
        AND (
          procedure.proname LIKE 'qualification_input_%'
          OR procedure.proname LIKE 'admit_qualification_input_%'
          OR procedure.proname LIKE 'inspect_qualification_input_%'
          OR procedure.proname LIKE 'resolve_qualification_input_%'
          OR procedure.proname LIKE 'review_qualification_input_%'
          OR procedure.proname LIKE 'reject_qualification_input_%'
          OR procedure.proname LIKE 'enforce_qualification_input_%'
          OR procedure.proname LIKE 'issue_qualification_input_%'
        )
    LOOP
      EXECUTE pg_catalog.format(
        'ALTER FUNCTION %I.%I(%s) OWNER TO worksflow_migration_owner',
        v_schema, v_routine.proname, v_routine.arguments
      );
    END LOOP;
  END IF;
END;
$function$;

SELECT qualification_input_precommit_apply_security_v1();
