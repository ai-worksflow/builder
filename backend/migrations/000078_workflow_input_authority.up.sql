-- Immutable Workflow Input Authority.
--
-- This migration is deliberately profile-neutral. Migration 000079 owns the
-- workflow-engine/v3 status/check topology. The nullable stable-node columns
-- below preserve historical rows while giving v3 one exact identity and one
-- deferred authority cycle. A freeze therefore cannot commit on its own: the
-- surrounding workflow transaction must attach the authority to the node and
-- append the exact activation event before COMMIT.

-- The matching runtime code must be deployed first. Runtime workflow writers
-- and transaction-bound Freeze/AssertCurrent composition take the shared side
-- before relation access; migration 78/79 and their down paths take this
-- exclusive side first. Standalone inspect/resolve calls are single-statement
-- reads and must not be extended into a lock-taking transaction without first
-- joining the same shared fence.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

DO $workflow_input_hash_functions$
DECLARE
  schema_name text := current_schema();
BEGIN
  EXECUTE format(
    'CREATE FUNCTION %I.workflow_input_raw_sha256(bytea) RETURNS text '
    'LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS %L',
    schema_name,
    'SELECT ''sha256:'' || pg_catalog.encode(pg_catalog.sha256($1), ''hex'')'
  );
END;
$workflow_input_hash_functions$;

CREATE FUNCTION workflow_input_authority_hash(p_domain text, p_value bytea)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT 'sha256:' || pg_catalog.encode(
    pg_catalog.sha256(
      pg_catalog.convert_to('worksflow-workflow-input-authority-hash/v1', 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || pg_catalog.convert_to(p_domain, 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || p_value
    ),
    'hex'
  )
$function$;

CREATE FUNCTION workflow_input_normalize_sha256(p_value text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
BEGIN
  IF p_value ~ '^sha256:[0-9a-f]{64}$' THEN
    RETURN p_value;
  END IF;
  IF p_value ~ '^[0-9a-f]{64}$' THEN
    RETURN 'sha256:' || p_value;
  END IF;
  RAISE EXCEPTION 'Workflow Input digest is not canonical SHA-256'
    USING ERRCODE = 'WIA03';
END;
$function$;

CREATE FUNCTION workflow_input_uuid_is_exact(p_value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT p_value ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND (p_value::uuid)::text = p_value
$function$;

CREATE FUNCTION workflow_input_timestamp_is_exact(p_value text)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
DECLARE
  v_timestamp timestamptz;
BEGIN
  IF p_value !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z$'
     OR right(split_part(p_value, '.', 2), 4) <> '000Z' THEN
    RETURN false;
  END IF;
  BEGIN
    v_timestamp := p_value::timestamptz;
  EXCEPTION WHEN OTHERS THEN
    RETURN false;
  END;
  RETURN to_char(v_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') = p_value;
END;
$function$;

-- This canonicalizer is used only on closed documents built by the database.
-- It is not a raw JSON validator: private Go preflight remains responsible for
-- duplicate-name/unknown-field/UTF-8 rejection before invoking freeze.
CREATE FUNCTION workflow_input_canonical_jsonb_bytes(p_value jsonb)
RETURNS bytea
LANGUAGE plpgsql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
DECLARE
  v_type text := pg_catalog.jsonb_typeof(p_value);
  v_text text;
BEGIN
  CASE v_type
    WHEN 'object' THEN
      SELECT '{' || COALESCE(pg_catalog.string_agg(
        pg_catalog.to_jsonb(item.key)::text || ':' ||
        pg_catalog.convert_from(workflow_input_canonical_jsonb_bytes(item.value), 'UTF8'),
        ',' ORDER BY pg_catalog.convert_to(item.key, 'UTF8')
      ), '') || '}'
      INTO v_text
      FROM pg_catalog.jsonb_each(p_value) AS item;
    WHEN 'array' THEN
      SELECT '[' || COALESCE(pg_catalog.string_agg(
        pg_catalog.convert_from(workflow_input_canonical_jsonb_bytes(item.value), 'UTF8'),
        ',' ORDER BY item.ordinal
      ), '') || ']'
      INTO v_text
      FROM pg_catalog.jsonb_array_elements(p_value) WITH ORDINALITY AS item(value, ordinal);
    WHEN 'string' THEN v_text := p_value::text;
    WHEN 'number' THEN
      IF p_value::text !~ '^(0|-?[1-9][0-9]{0,15})$'
         OR (p_value::text)::numeric NOT BETWEEN -9007199254740991 AND 9007199254740991 THEN
        RAISE EXCEPTION 'Workflow Input canonical JSON contains a non-canonical number'
          USING ERRCODE = 'WIA03';
      END IF;
      v_text := p_value::text;
    WHEN 'boolean' THEN v_text := p_value::text;
    WHEN 'null' THEN v_text := 'null';
    ELSE
      RAISE EXCEPTION 'Workflow Input canonical JSON type is invalid'
        USING ERRCODE = 'WIA03';
  END CASE;
  RETURN pg_catalog.convert_to(v_text, 'UTF8');
END;
$function$;

-- Qualification Policy Authority v1 is installed in this migration because
-- Workflow Input Freeze must never accept a caller-invented policy ID/hash.
-- The service resolves an opaque reviewed source and compiles all four exact
-- canonical documents; this owner boundary independently checks bytes, JSONB,
-- domain hashes, generation continuity, and normalized child closure.
CREATE FUNCTION qualification_policy_authority_hash(
  p_domain text,
  p_value bytea
)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT 'sha256:' || pg_catalog.encode(
    pg_catalog.sha256(
      pg_catalog.convert_to('worksflow-qualification-policy-authority-hash/v1', 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || pg_catalog.convert_to(p_domain, 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || p_value
    ),
    'hex'
  )
$function$;

CREATE TABLE qualification_policy_authorities (
  authority_id uuid PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE,
  -- Store response metadata only; it is intentionally excluded from every
  -- canonical component/root and lets the adapter classify exact replay.
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  policy_source_id text NOT NULL CHECK (
    octet_length(policy_source_id) BETWEEN 1 AND 256
    AND policy_source_id = btrim(policy_source_id)
    AND policy_source_id ~ '^[a-z0-9]([a-z0-9._:/@+-]*[a-z0-9])?$'
    AND policy_source_id !~ '://'
    AND policy_source_id !~ '[[:cntrl:]]'
  ),
  expected_previous_authority_hash text CHECK (
    expected_previous_authority_hash IS NULL
    OR expected_previous_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),

  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  execution_profile_version text NOT NULL CHECK (
    execution_profile_version = 'workflow-engine/v3'
  ),
  execution_profile_hash text NOT NULL CHECK (
    execution_profile_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  generation bigint NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
  previous_authority_hash text CHECK (
    previous_authority_hash IS NULL
    OR previous_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  status text NOT NULL CHECK (status IN ('active', 'suspended')),
  issued_at timestamptz NOT NULL CHECK (issued_at = date_trunc('milliseconds', issued_at)),
  external_gate_policy text NOT NULL CHECK (external_gate_policy = 'external-qualification/v1'),
  supersession_policy text NOT NULL CHECK (supersession_policy = 'invalidate-unconsumed/v1'),

  revision_policy_hash text NOT NULL CHECK (
    revision_policy_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  revision_policy_bytes bytea NOT NULL CHECK (
    octet_length(revision_policy_bytes) BETWEEN 1 AND 16777216
  ),
  revision_policy_document jsonb NOT NULL CHECK (
    jsonb_typeof(revision_policy_document) = 'object'
  ),

  plan_input_profile_hash text NOT NULL CHECK (
    plan_input_profile_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  plan_input_profile_bytes bytea NOT NULL CHECK (
    octet_length(plan_input_profile_bytes) BETWEEN 1 AND 16777216
  ),
  plan_input_profile_document jsonb NOT NULL CHECK (
    jsonb_typeof(plan_input_profile_document) = 'object'
  ),

  promotion_policy_hash text NOT NULL CHECK (
    promotion_policy_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  promotion_policy_bytes bytea NOT NULL CHECK (
    octet_length(promotion_policy_bytes) BETWEEN 1 AND 16777216
  ),
  promotion_policy_document jsonb NOT NULL CHECK (
    jsonb_typeof(promotion_policy_document) = 'object'
  ),

  authority_hash text NOT NULL UNIQUE CHECK (
    authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  authority_bytes bytea NOT NULL CHECK (
    octet_length(authority_bytes) BETWEEN 1 AND 33554432
  ),
  authority_document jsonb NOT NULL CHECK (
    jsonb_typeof(authority_document) = 'object'
  ),

  CONSTRAINT qualification_policy_authority_scope_generation_unique UNIQUE (
    project_id, execution_profile_version, execution_profile_hash, generation
  ),
  CONSTRAINT qualification_policy_authority_wia_exact_unique UNIQUE (
    authority_id, project_id, execution_profile_version,
    execution_profile_hash, authority_hash
  ),
  CONSTRAINT qualification_policy_authority_v4_identities CHECK (
    workflow_input_uuid_is_exact(authority_id::text)
    AND workflow_input_uuid_is_exact(operation_id::text)
    AND workflow_input_uuid_is_exact(project_id::text)
    AND authority_id <> operation_id
    AND authority_id <> project_id
    AND operation_id <> project_id
  ),
  CONSTRAINT qualification_policy_authority_predecessor_shape CHECK (
    (generation = 1 AND previous_authority_hash IS NULL
      AND expected_previous_authority_hash IS NULL)
    OR
    (generation > 1 AND previous_authority_hash IS NOT NULL
      AND expected_previous_authority_hash = previous_authority_hash)
  ),
  CONSTRAINT qualification_policy_authority_hash_domains_distinct CHECK (
    revision_policy_hash <> plan_input_profile_hash
    AND revision_policy_hash <> promotion_policy_hash
    AND revision_policy_hash <> authority_hash
    AND plan_input_profile_hash <> promotion_policy_hash
    AND plan_input_profile_hash <> authority_hash
    AND promotion_policy_hash <> authority_hash
    AND (previous_authority_hash IS NULL OR previous_authority_hash NOT IN (
      revision_policy_hash, plan_input_profile_hash, promotion_policy_hash
    ))
  )
);

CREATE INDEX qualification_policy_authorities_current_idx
  ON qualification_policy_authorities(
    project_id, execution_profile_version, execution_profile_hash, generation DESC
  );

CREATE TABLE qualification_policy_review_defaults (
  authority_id uuid NOT NULL REFERENCES qualification_policy_authorities(authority_id) ON DELETE RESTRICT,
  ordinal smallint NOT NULL CHECK (ordinal BETWEEN 0 AND 5),
  change_source text NOT NULL CHECK (change_source IN (
    'ai_proposal','human','import','merge','rollback','system'
  )),
  canonical_review_required boolean NOT NULL,
  PRIMARY KEY (authority_id, ordinal),
  UNIQUE (authority_id, change_source)
);

CREATE TABLE qualification_policy_exact_approved_sources (
  authority_id uuid NOT NULL REFERENCES qualification_policy_authorities(authority_id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 2047),
  source_kind text NOT NULL CHECK (
    octet_length(source_kind) BETWEEN 1 AND 128
    AND source_kind ~ '^[a-z0-9]([a-z0-9._:/@+-]*[a-z0-9])?$'
    AND source_kind <> 'workspace'
  ),
  purpose text NOT NULL CHECK (
    octet_length(purpose) BETWEEN 1 AND 256
    AND purpose ~ '^[a-z0-9]([a-z0-9._:/@+-]*[a-z0-9])?$'
    AND purpose <> 'workspace-target'
  ),
  artifact_id uuid NOT NULL,
  revision_id uuid NOT NULL,
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  PRIMARY KEY (authority_id, ordinal),
  UNIQUE (authority_id, source_kind, purpose, artifact_id, revision_id, content_hash),
  CONSTRAINT qualification_policy_exact_source_artifact_fk FOREIGN KEY (artifact_id)
    REFERENCES artifacts(id) ON DELETE RESTRICT,
  CONSTRAINT qualification_policy_exact_source_revision_fk FOREIGN KEY (revision_id)
    REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  CHECK (
    workflow_input_uuid_is_exact(artifact_id::text)
    AND workflow_input_uuid_is_exact(revision_id::text)
    AND artifact_id <> revision_id
  )
);

-- One persistent UUID namespace prevents an operation/authority identity from
-- ever aliasing a UUID retained as policy input. Embedded references may be
-- shared by later generations; their first append wins the reference row.
CREATE TABLE qualification_policy_identity_reservations (
  identity_value uuid PRIMARY KEY,
  identity_class text NOT NULL CHECK (
    identity_class IN ('authority','operation','embedded-reference')
  ),
  identity_role text NOT NULL CHECK (
    octet_length(identity_role) BETWEEN 1 AND 128
    AND identity_role ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
  ),
  owner_authority_id uuid NOT NULL REFERENCES qualification_policy_authorities(authority_id) ON DELETE RESTRICT,
  reserved_at timestamptz NOT NULL CHECK (reserved_at = date_trunc('milliseconds', reserved_at)),
  CHECK (workflow_input_uuid_is_exact(identity_value::text)),
  CHECK (
    (identity_class = 'authority' AND identity_role = 'authority')
    OR (identity_class = 'operation' AND identity_role = 'operation')
    OR (identity_class = 'embedded-reference' AND identity_role NOT IN ('authority','operation'))
  )
);

CREATE INDEX qualification_policy_identity_owner_idx
  ON qualification_policy_identity_reservations(owner_authority_id);

CREATE FUNCTION qualification_policy_embedded_uuid_references_v1(p_document jsonb)
RETURNS TABLE(identity_value uuid, identity_role text)
LANGUAGE plpgsql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
DECLARE
  v_value text;
BEGIN
  v_value := p_document->>'projectId';
  IF workflow_input_uuid_is_exact(v_value) THEN
    identity_value := v_value::uuid; identity_role := 'project'; RETURN NEXT;
  END IF;

  IF jsonb_typeof(p_document#>'{revisionPolicy,exactApprovedSources}') = 'array' THEN
    RETURN QUERY
    SELECT DISTINCT candidate.value::uuid, candidate.role
    FROM (
      SELECT item.value->>'artifactId' AS value, 'exact-source-artifact'::text AS role
      FROM jsonb_array_elements(p_document#>'{revisionPolicy,exactApprovedSources}') AS item(value)
      UNION ALL
      SELECT item.value->>'revisionId', 'exact-source-revision'
      FROM jsonb_array_elements(p_document#>'{revisionPolicy,exactApprovedSources}') AS item(value)
    ) AS candidate
    WHERE workflow_input_uuid_is_exact(candidate.value);
  END IF;

  RETURN QUERY
  SELECT DISTINCT candidate.value::uuid, candidate.role
  FROM (VALUES
    (p_document#>>'{planInputProfile,qualificationManifest,artifactId}', 'qualification-manifest-artifact'),
    (p_document#>>'{planInputProfile,qualificationManifest,revisionId}', 'qualification-manifest-revision'),
    (p_document#>>'{planInputProfile,templateRelease,id}', 'template-release'),
    (p_document#>>'{planInputProfile,goldenRuntime,fixtureId}', 'golden-fixture'),
    (p_document#>>'{planInputProfile,credentialProfile,authorityId}', 'credential-authority'),
    (p_document#>>'{planInputProfile,credentialProfile,issuanceArtifactId}', 'credential-issuance-artifact'),
    (p_document#>>'{planInputProfile,credentialProfile,revocationArtifactId}', 'credential-revocation-artifact'),
    (p_document#>>'{planInputProfile,recipient,keyResourceId}', 'kms-recipient-resource'),
    (p_document#>>'{planInputProfile,recipient,keyVersion}', 'kms-recipient-version'),
    (p_document#>>'{planInputProfile,outputs,kmsAttestationArtifactId}', 'kms-attestation-output'),
    (p_document#>>'{planInputProfile,outputs,artifactIndexId}', 'artifact-index-output'),
    (p_document#>>'{planInputProfile,outputs,receiptId}', 'receipt-output'),
    (p_document#>>'{planInputProfile,outputs,snapshotId}', 'snapshot-output'),
    (p_document#>>'{planInputProfile,trustBindings,captureAuthorityId}', 'capture-authority'),
    (p_document#>>'{planInputProfile,trustBindings,credentialAuthorityId}', 'credential-trust-authority'),
    (p_document#>>'{planInputProfile,trustBindings,encryptionAuthorityId}', 'encryption-authority'),
    (p_document#>>'{planInputProfile,trustBindings,indexerAuthorityId}', 'indexer-authority'),
    (p_document#>>'{planInputProfile,trustBindings,kmsAuthorityId}', 'kms-authority'),
    (p_document#>>'{planInputProfile,trustBindings,receiptAuthorityId}', 'receipt-authority'),
    (p_document#>>'{planInputProfile,trustBindings,sealerAuthorityId}', 'sealer-authority'),
    (p_document#>>'{planInputProfile,trustBindings,verifierAuthorityId}', 'verifier-authority')
  ) AS candidate(value, role)
  WHERE workflow_input_uuid_is_exact(candidate.value);

  IF jsonb_typeof(p_document#>'{planInputProfile,artifacts}') = 'array' THEN
    RETURN QUERY
    SELECT DISTINCT (item.value->>'id')::uuid, 'evidence-artifact'::text
    FROM jsonb_array_elements(p_document#>'{planInputProfile,artifacts}') AS item(value)
    WHERE workflow_input_uuid_is_exact(item.value->>'id');
  END IF;
  IF jsonb_typeof(p_document#>'{promotionPolicy,independentRequirements}') = 'array' THEN
    RETURN QUERY
    SELECT DISTINCT (item.value->>'authorityId')::uuid,
      ('independent-' || (item.value->>'kind'))::text
    FROM jsonb_array_elements(p_document#>'{promotionPolicy,independentRequirements}') AS item(value)
    WHERE workflow_input_uuid_is_exact(item.value->>'authorityId');
  END IF;
END;
$function$;

CREATE FUNCTION reject_qualification_policy_authority_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification Policy Authority records are immutable'
    USING ERRCODE = 'WPA03';
END;
$function$;

CREATE TRIGGER qualification_policy_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_policy_authorities
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_policy_authority_mutation();
CREATE TRIGGER qualification_policy_review_defaults_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_policy_review_defaults
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_policy_authority_mutation();
CREATE TRIGGER qualification_policy_exact_approved_sources_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_policy_exact_approved_sources
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_policy_authority_mutation();
CREATE TRIGGER qualification_policy_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_policy_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_policy_authority_mutation();

CREATE FUNCTION qualification_policy_authority_record_is_exact_v1(p_authority_id uuid)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_policy_authorities%ROWTYPE;
  v_expected_authority jsonb;
  v_review_defaults jsonb;
  v_exact_sources jsonb;
  v_review_names text[];
  v_previous qualification_policy_authorities%ROWTYPE;
BEGIN
  SELECT * INTO v_authority
  FROM qualification_policy_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN
    RETURN false;
  END IF;

  IF v_authority.revision_policy_bytes <>
       workflow_input_canonical_jsonb_bytes(v_authority.revision_policy_document)
     OR v_authority.plan_input_profile_bytes <>
       workflow_input_canonical_jsonb_bytes(v_authority.plan_input_profile_document)
     OR v_authority.promotion_policy_bytes <>
       workflow_input_canonical_jsonb_bytes(v_authority.promotion_policy_document)
     OR v_authority.authority_bytes <>
       workflow_input_canonical_jsonb_bytes(v_authority.authority_document)
     OR v_authority.revision_policy_hash <> qualification_policy_authority_hash(
       'worksflow.qualification-policy.revision/v1', v_authority.revision_policy_bytes
     )
     OR v_authority.plan_input_profile_hash <> qualification_policy_authority_hash(
       'worksflow.qualification-policy.plan-input-profile/v1', v_authority.plan_input_profile_bytes
     )
     OR v_authority.promotion_policy_hash <> qualification_policy_authority_hash(
       'worksflow.qualification-policy.promotion/v1', v_authority.promotion_policy_bytes
     )
     OR v_authority.authority_hash <> qualification_policy_authority_hash(
       'worksflow.qualification-policy.authority/v1', v_authority.authority_bytes
     ) THEN
    RETURN false;
  END IF;

  -- Closed component roots. Nested Plan members remain the responsibility of
  -- the strict trusted Go compiler, while their exact bytes and root embedding
  -- are independently authenticated here.
  IF NOT (v_authority.revision_policy_document ?& ARRAY[
       'exactApprovedSources','reviewByChangeSource','schemaVersion',
       'sourceCurrencyPolicy','workspaceTarget'
     ])
     OR v_authority.revision_policy_document - ARRAY[
       'exactApprovedSources','reviewByChangeSource','schemaVersion',
       'sourceCurrencyPolicy','workspaceTarget'
     ] <> '{}'::jsonb
     OR v_authority.revision_policy_document->>'schemaVersion' <>
       'worksflow-qualification-revision-policy/v1'
     OR v_authority.revision_policy_document->>'sourceCurrencyPolicy' <>
       'latest-approved-required'
     OR v_authority.revision_policy_document->'workspaceTarget' <>
       jsonb_build_object(
         'canonicalReviewRequired', false,
         'currencyPolicy', 'latest-approved-required'
       )
     OR jsonb_typeof(v_authority.revision_policy_document->'reviewByChangeSource') <> 'array'
     OR jsonb_typeof(v_authority.revision_policy_document->'exactApprovedSources') <> 'array'
     OR NOT (v_authority.plan_input_profile_document ?& ARRAY[
       'artifactPolicy','artifacts','credentialProfile','goldenRuntime','outputPolicy',
       'outputs','qualificationManifest','recipient','schemaVersion','sourcePolicyDigest',
       'templateRelease','trustBindings','trustPolicyDigest'
     ])
     OR v_authority.plan_input_profile_document - ARRAY[
       'artifactPolicy','artifacts','credentialProfile','goldenRuntime','outputPolicy',
       'outputs','qualificationManifest','recipient','schemaVersion','sourcePolicyDigest',
       'templateRelease','trustBindings','trustPolicyDigest'
     ] <> '{}'::jsonb
     OR v_authority.plan_input_profile_document->>'schemaVersion' <>
       'worksflow-qualification-plan-input-profile/v1'
     OR NOT (v_authority.promotion_policy_document ?& ARRAY[
       'independentRequirements','planAuthoritySchema','receiptSchema','schemaVersion','singleUseProtocol'
     ])
     OR v_authority.promotion_policy_document - ARRAY[
       'independentRequirements','planAuthoritySchema','receiptSchema','schemaVersion','singleUseProtocol'
     ] <> '{}'::jsonb
     OR v_authority.promotion_policy_document->>'schemaVersion' <>
       'worksflow-qualification-promotion-policy/v1'
     OR v_authority.promotion_policy_document->>'planAuthoritySchema' <>
       'worksflow-qualification-plan-authority/v1'
     OR v_authority.promotion_policy_document->>'receiptSchema' <>
       'worksflow-qualification-receipt/v3'
     OR v_authority.promotion_policy_document->>'singleUseProtocol' <>
       'worksflow-qualification-promotion-consume/v2'
     OR jsonb_typeof(v_authority.promotion_policy_document->'independentRequirements') <> 'array'
     OR jsonb_array_length(v_authority.promotion_policy_document->'independentRequirements') > 2
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(
         v_authority.promotion_policy_document->'independentRequirements'
       ) WITH ORDINALITY AS requirement(value, ordinal)
       WHERE jsonb_typeof(requirement.value) <> 'object'
         OR NOT (requirement.value ?& ARRAY['authorityHash','authorityId','kind'])
         OR requirement.value - ARRAY['authorityHash','authorityId','kind'] <> '{}'::jsonb
         OR requirement.value->>'kind' NOT IN (
           'model-profile-activation','production-postgresql-posture'
         )
         OR requirement.value->>'authorityHash' !~ '^sha256:[0-9a-f]{64}$'
         OR requirement.value->>'authorityId' !~ '^[a-z0-9]([a-z0-9._:/@+-]*[a-z0-9])?$'
     )
     OR EXISTS (
       SELECT 1
       FROM (
         SELECT requirement.value,
                lag(requirement.value->>'kind') OVER (ORDER BY requirement.ordinal) AS previous_kind
         FROM jsonb_array_elements(
           v_authority.promotion_policy_document->'independentRequirements'
         ) WITH ORDINALITY AS requirement(value, ordinal)
       ) AS ordered
       WHERE ordered.previous_kind IS NOT NULL
         AND ordered.previous_kind >= ordered.value->>'kind'
     )
     OR (SELECT count(DISTINCT requirement.value->>'authorityId')
         FROM jsonb_array_elements(
           v_authority.promotion_policy_document->'independentRequirements'
         ) AS requirement(value)) <>
        jsonb_array_length(v_authority.promotion_policy_document->'independentRequirements')
     OR (SELECT count(DISTINCT requirement.value->>'authorityHash')
         FROM jsonb_array_elements(
           v_authority.promotion_policy_document->'independentRequirements'
         ) AS requirement(value)) <>
        jsonb_array_length(v_authority.promotion_policy_document->'independentRequirements') THEN
    RETURN false;
  END IF;

  SELECT COALESCE(jsonb_agg(jsonb_build_object(
    'canonicalReviewRequired', member.canonical_review_required,
    'changeSource', member.change_source
  ) ORDER BY member.ordinal), '[]'::jsonb),
  COALESCE(array_agg(member.change_source ORDER BY member.ordinal), ARRAY[]::text[])
  INTO v_review_defaults, v_review_names
  FROM qualification_policy_review_defaults AS member
  WHERE member.authority_id = p_authority_id;

  SELECT COALESCE(jsonb_agg(jsonb_build_object(
    'artifactId', member.artifact_id::text,
    'contentHash', member.content_hash,
    'purpose', member.purpose,
    'revisionId', member.revision_id::text,
    'sourceKind', member.source_kind
  ) ORDER BY member.ordinal), '[]'::jsonb)
  INTO v_exact_sources
  FROM qualification_policy_exact_approved_sources AS member
  WHERE member.authority_id = p_authority_id;

  IF v_review_names <> ARRAY['ai_proposal','human','import','merge','rollback','system']::text[]
     OR (SELECT count(*) FROM qualification_policy_review_defaults
         WHERE authority_id = p_authority_id) <> 6
     OR (SELECT min(ordinal) FROM qualification_policy_review_defaults
         WHERE authority_id = p_authority_id) <> 0
     OR (SELECT max(ordinal) FROM qualification_policy_review_defaults
         WHERE authority_id = p_authority_id) <> 5
     OR (SELECT canonical_review_required
         FROM qualification_policy_review_defaults
         WHERE authority_id = p_authority_id AND change_source = 'human') IS NOT TRUE
     OR v_review_defaults <> v_authority.revision_policy_document->'reviewByChangeSource'
     OR v_exact_sources <> v_authority.revision_policy_document->'exactApprovedSources'
     OR (
       (SELECT count(*) FROM qualification_policy_exact_approved_sources
        WHERE authority_id = p_authority_id) > 0
       AND (
         (SELECT min(ordinal) FROM qualification_policy_exact_approved_sources
          WHERE authority_id = p_authority_id) <> 0
         OR
         (SELECT max(ordinal) FROM qualification_policy_exact_approved_sources
          WHERE authority_id = p_authority_id) <>
         (SELECT count(*) - 1 FROM qualification_policy_exact_approved_sources
          WHERE authority_id = p_authority_id)
       )
     )
     OR EXISTS (
       SELECT 1
       FROM (
         SELECT source_kind, purpose, artifact_id::text AS artifact_id,
                revision_id::text AS revision_id, content_hash,
                lag(source_kind) OVER (ORDER BY ordinal) AS previous_source_kind,
                lag(purpose) OVER (ORDER BY ordinal) AS previous_purpose,
                lag(artifact_id::text) OVER (ORDER BY ordinal) AS previous_artifact_id,
                lag(revision_id::text) OVER (ORDER BY ordinal) AS previous_revision_id,
                lag(content_hash) OVER (ORDER BY ordinal) AS previous_content_hash
         FROM qualification_policy_exact_approved_sources
         WHERE authority_id = p_authority_id
       ) AS ordered
       WHERE ordered.previous_source_kind IS NOT NULL
         AND ROW(
           ordered.previous_source_kind, ordered.previous_purpose,
           ordered.previous_artifact_id, ordered.previous_revision_id,
           ordered.previous_content_hash
         ) >= ROW(
           ordered.source_kind, ordered.purpose, ordered.artifact_id,
           ordered.revision_id, ordered.content_hash
         )
     )
     OR EXISTS (
       SELECT 1
       FROM qualification_policy_exact_approved_sources AS source
       LEFT JOIN artifacts AS artifact ON artifact.id = source.artifact_id
       LEFT JOIN artifact_revisions AS revision ON revision.id = source.revision_id
       WHERE source.authority_id = p_authority_id
         AND (
           artifact.id IS NULL
           OR artifact.project_id <> v_authority.project_id
           OR artifact.kind <> source.source_kind
           OR revision.id IS NULL
           OR revision.artifact_id <> source.artifact_id
           OR workflow_input_normalize_sha256(revision.content_hash) <> source.content_hash
         )
     )
     OR NOT EXISTS (
       SELECT 1 FROM qualification_policy_identity_reservations AS identity
       WHERE identity.identity_value = v_authority.authority_id
         AND identity.identity_class = 'authority'
         AND identity.identity_role = 'authority'
         AND identity.owner_authority_id = v_authority.authority_id
         AND identity.reserved_at = v_authority.issued_at
     )
     OR NOT EXISTS (
       SELECT 1 FROM qualification_policy_identity_reservations AS identity
       WHERE identity.identity_value = v_authority.operation_id
         AND identity.identity_class = 'operation'
         AND identity.identity_role = 'operation'
         AND identity.owner_authority_id = v_authority.authority_id
         AND identity.reserved_at = v_authority.issued_at
     )
     OR EXISTS (
       SELECT 1
       FROM qualification_policy_embedded_uuid_references_v1(
         v_authority.authority_document
       ) AS reference
       WHERE NOT EXISTS (
         SELECT 1 FROM qualification_policy_identity_reservations AS identity
         WHERE identity.identity_value = reference.identity_value
           AND identity.identity_class = 'embedded-reference'
       )
     )
     OR EXISTS (
       SELECT 1 FROM qualification_policy_identity_reservations AS identity
       WHERE identity.owner_authority_id = v_authority.authority_id
         AND NOT (
           (identity.identity_class = 'authority'
             AND identity.identity_value = v_authority.authority_id)
           OR (identity.identity_class = 'operation'
             AND identity.identity_value = v_authority.operation_id)
           OR (identity.identity_class = 'embedded-reference' AND EXISTS (
             SELECT 1
             FROM qualification_policy_embedded_uuid_references_v1(
               v_authority.authority_document
             ) AS reference
             WHERE reference.identity_value = identity.identity_value
           ))
         )
     ) THEN
    RETURN false;
  END IF;

  IF v_authority.generation = 1 THEN
    IF v_authority.previous_authority_hash IS NOT NULL
       OR v_authority.expected_previous_authority_hash IS NOT NULL THEN
      RETURN false;
    END IF;
  ELSE
    SELECT * INTO v_previous
    FROM qualification_policy_authorities
    WHERE project_id = v_authority.project_id
      AND execution_profile_version = v_authority.execution_profile_version
      AND execution_profile_hash = v_authority.execution_profile_hash
      AND generation = v_authority.generation - 1;
    IF NOT FOUND
       OR v_previous.authority_hash <> v_authority.previous_authority_hash
       OR v_authority.expected_previous_authority_hash <> v_authority.previous_authority_hash
       OR v_authority.issued_at < v_previous.issued_at THEN
      RETURN false;
    END IF;
  END IF;

  v_expected_authority := jsonb_build_object(
    'authorityId', v_authority.authority_id::text,
    'componentDigests', jsonb_build_object(
      'planInputProfile', v_authority.plan_input_profile_hash,
      'promotionPolicy', v_authority.promotion_policy_hash,
      'revisionPolicy', v_authority.revision_policy_hash
    ),
    'executionProfile', jsonb_build_object(
      'hash', v_authority.execution_profile_hash,
      'version', v_authority.execution_profile_version
    ),
    'externalGatePolicy', v_authority.external_gate_policy,
    'generation', v_authority.generation,
    'issuedAt', to_char(v_authority.issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
    'operationId', v_authority.operation_id::text,
    'planInputProfile', v_authority.plan_input_profile_document,
    'policySourceId', v_authority.policy_source_id,
    'previousAuthorityHash', to_jsonb(v_authority.previous_authority_hash),
    'projectId', v_authority.project_id::text,
    'promotionPolicy', v_authority.promotion_policy_document,
    'revisionPolicy', v_authority.revision_policy_document,
    'schemaVersion', 'worksflow-qualification-policy-authority/v1',
    'status', v_authority.status,
    'supersessionPolicy', v_authority.supersession_policy
  );
  RETURN v_authority.authority_document = v_expected_authority;
END;
$function$;

CREATE FUNCTION validate_qualification_policy_authority_closure_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authority_id uuid;
BEGIN
  IF TG_TABLE_NAME = 'qualification_policy_identity_reservations' THEN
    v_authority_id := COALESCE(NEW.owner_authority_id, OLD.owner_authority_id);
  ELSE
    v_authority_id := COALESCE(NEW.authority_id, OLD.authority_id);
  END IF;
  IF EXISTS (
    SELECT 1 FROM qualification_policy_authorities
    WHERE authority_id = v_authority_id
  ) AND qualification_policy_authority_record_is_exact_v1(v_authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Policy Authority closure is not exact'
      USING ERRCODE = 'WPA03';
  END IF;
  IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$function$;

CREATE CONSTRAINT TRIGGER qualification_policy_authorities_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON qualification_policy_authorities
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_policy_authority_closure_v1();
CREATE CONSTRAINT TRIGGER qualification_policy_review_defaults_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON qualification_policy_review_defaults
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_policy_authority_closure_v1();
CREATE CONSTRAINT TRIGGER qualification_policy_exact_sources_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON qualification_policy_exact_approved_sources
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_policy_authority_closure_v1();
CREATE CONSTRAINT TRIGGER qualification_policy_identity_reservations_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON qualification_policy_identity_reservations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_policy_authority_closure_v1();

CREATE FUNCTION issue_qualification_policy_authority_v1(
  p_operation_id uuid,
  p_authority_id uuid,
  p_policy_source_id text,
  p_expected_previous_authority_hash text,
  p_project_id uuid,
  p_execution_profile_version text,
  p_execution_profile_hash text,
  p_generation bigint,
  p_status text,
  p_issued_at timestamptz,
  p_external_gate_policy text,
  p_supersession_policy text,
  p_revision_policy_hash text,
  p_revision_policy_bytes bytea,
  p_revision_policy_document jsonb,
  p_plan_input_profile_hash text,
  p_plan_input_profile_bytes bytea,
  p_plan_input_profile_document jsonb,
  p_promotion_policy_hash text,
  p_promotion_policy_bytes bytea,
  p_promotion_policy_document jsonb,
  p_authority_hash text,
  p_authority_bytes bytea,
  p_authority_document jsonb
)
RETURNS SETOF qualification_policy_authorities
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_existing qualification_policy_authorities%ROWTYPE;
  v_current qualification_policy_authorities%ROWTYPE;
  v_expected_previous text := NULLIF(p_expected_previous_authority_hash, '');
  v_source jsonb;
  v_artifact artifacts%ROWTYPE;
  v_revision artifact_revisions%ROWTYPE;
BEGIN
  -- Shared rolling fence is the first database action. Identity advisory locks
  -- serialize the cross-column authority/operation UUID namespace; project row
  -- locking then serializes every profile generation within the project.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  IF p_operation_id IS NULL OR p_authority_id IS NULL OR p_project_id IS NULL
     OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_project_id::text) IS NOT TRUE
     OR p_operation_id IN (p_authority_id, p_project_id)
     OR p_authority_id = p_project_id THEN
    RAISE EXCEPTION 'Qualification Policy Authority identities are invalid'
      USING ERRCODE = 'WPA01';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-policy-identity:' || identity_value, 0
    )
  )
  FROM unnest(ARRAY[p_operation_id::text, p_authority_id::text]) AS identity(identity_value)
  ORDER BY identity_value;

  SELECT * INTO v_existing
  FROM qualification_policy_authorities
  WHERE operation_id = p_operation_id;
  IF FOUND THEN
    IF qualification_policy_authority_record_is_exact_v1(v_existing.authority_id) IS NOT TRUE
       OR v_existing.authority_id IS DISTINCT FROM p_authority_id
       OR v_existing.policy_source_id IS DISTINCT FROM p_policy_source_id
       OR v_existing.expected_previous_authority_hash IS DISTINCT FROM v_expected_previous
       OR v_existing.project_id IS DISTINCT FROM p_project_id
       OR v_existing.execution_profile_version IS DISTINCT FROM p_execution_profile_version
       OR v_existing.execution_profile_hash IS DISTINCT FROM p_execution_profile_hash
       OR v_existing.generation IS DISTINCT FROM p_generation
       OR v_existing.status IS DISTINCT FROM p_status
       OR v_existing.issued_at IS DISTINCT FROM p_issued_at
       OR v_existing.external_gate_policy IS DISTINCT FROM p_external_gate_policy
       OR v_existing.supersession_policy IS DISTINCT FROM p_supersession_policy
       OR v_existing.revision_policy_hash IS DISTINCT FROM p_revision_policy_hash
       OR v_existing.revision_policy_bytes IS DISTINCT FROM p_revision_policy_bytes
       OR v_existing.revision_policy_document IS DISTINCT FROM p_revision_policy_document
       OR v_existing.plan_input_profile_hash IS DISTINCT FROM p_plan_input_profile_hash
       OR v_existing.plan_input_profile_bytes IS DISTINCT FROM p_plan_input_profile_bytes
       OR v_existing.plan_input_profile_document IS DISTINCT FROM p_plan_input_profile_document
       OR v_existing.promotion_policy_hash IS DISTINCT FROM p_promotion_policy_hash
       OR v_existing.promotion_policy_bytes IS DISTINCT FROM p_promotion_policy_bytes
       OR v_existing.promotion_policy_document IS DISTINCT FROM p_promotion_policy_document
       OR v_existing.authority_hash IS DISTINCT FROM p_authority_hash
       OR v_existing.authority_bytes IS DISTINCT FROM p_authority_bytes
       OR v_existing.authority_document IS DISTINCT FROM p_authority_document THEN
      RAISE EXCEPTION 'Qualification Policy operation is bound to a different immutable record'
        USING ERRCODE = 'WPA03';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  IF EXISTS (
    SELECT 1 FROM qualification_policy_authorities
    WHERE authority_id IN (p_operation_id, p_authority_id)
       OR operation_id IN (p_operation_id, p_authority_id)
  ) THEN
    RAISE EXCEPTION 'Qualification Policy identity is already reserved'
      USING ERRCODE = 'WPA03';
  END IF;

  PERFORM 1 FROM projects WHERE id = p_project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Policy project does not exist'
      USING ERRCODE = 'WPA01';
  END IF;

  SELECT * INTO v_current
  FROM qualification_policy_authorities
  WHERE project_id = p_project_id
    AND execution_profile_version = p_execution_profile_version
    AND execution_profile_hash = p_execution_profile_hash
  ORDER BY generation DESC
  LIMIT 1;
  IF FOUND THEN
    IF p_generation <> v_current.generation + 1
       OR v_expected_previous IS DISTINCT FROM v_current.authority_hash
       OR p_authority_document->>'previousAuthorityHash' IS DISTINCT FROM v_current.authority_hash
       OR p_issued_at < v_current.issued_at THEN
      RAISE EXCEPTION 'Qualification Policy compare-and-swap cursor is stale'
        USING ERRCODE = 'WPA03';
    END IF;
  ELSIF p_generation <> 1 OR v_expected_previous IS NOT NULL
        OR jsonb_typeof(p_authority_document->'previousAuthorityHash') <> 'null' THEN
    RAISE EXCEPTION 'Qualification Policy first generation requires an empty predecessor'
      USING ERRCODE = 'WPA03';
  END IF;

  -- Exact-approved exceptions are not opaque IDs. Lock each immutable tuple in
  -- stable order and prove it belongs to this project, names its exact revision
  -- and hash, and has passed the approval lifecycle. Later supersession is a
  -- legitimate historical transition and does not invalidate the frozen row.
  FOR v_source IN
    SELECT item.value
    FROM jsonb_array_elements(
      p_revision_policy_document->'exactApprovedSources'
    ) AS item(value)
    ORDER BY
      pg_catalog.convert_to(item.value->>'sourceKind', 'UTF8'),
      pg_catalog.convert_to(item.value->>'purpose', 'UTF8'),
      item.value->>'artifactId', item.value->>'revisionId', item.value->>'contentHash'
  LOOP
    SELECT * INTO v_artifact
    FROM artifacts
    WHERE id = (v_source->>'artifactId')::uuid
    FOR UPDATE;
    SELECT * INTO v_revision
    FROM artifact_revisions
    WHERE id = (v_source->>'revisionId')::uuid
    FOR UPDATE;
    IF v_artifact.id IS NULL OR v_artifact.project_id <> p_project_id
       OR v_artifact.kind <> v_source->>'sourceKind'
       OR v_revision.id IS NULL OR v_revision.artifact_id <> v_artifact.id
       OR workflow_input_normalize_sha256(v_revision.content_hash) <>
          v_source->>'contentHash'
       OR v_revision.workflow_status <> 'approved'
       OR v_revision.approved_at IS NULL
       OR v_revision.superseded_at IS NOT NULL THEN
      RAISE EXCEPTION 'Qualification Policy exact-approved source is absent, cross-project, or not approved'
        USING ERRCODE = 'WPA03';
    END IF;
  END LOOP;

  INSERT INTO qualification_policy_authorities (
    authority_id, operation_id, policy_source_id, expected_previous_authority_hash,
    project_id, execution_profile_version, execution_profile_hash, generation,
    previous_authority_hash, status, issued_at, external_gate_policy, supersession_policy,
    revision_policy_hash, revision_policy_bytes, revision_policy_document,
    plan_input_profile_hash, plan_input_profile_bytes, plan_input_profile_document,
    promotion_policy_hash, promotion_policy_bytes, promotion_policy_document,
    authority_hash, authority_bytes, authority_document
  ) VALUES (
    p_authority_id, p_operation_id, p_policy_source_id, v_expected_previous,
    p_project_id, p_execution_profile_version, p_execution_profile_hash, p_generation,
    v_expected_previous, p_status, p_issued_at, p_external_gate_policy, p_supersession_policy,
    p_revision_policy_hash, p_revision_policy_bytes, p_revision_policy_document,
    p_plan_input_profile_hash, p_plan_input_profile_bytes, p_plan_input_profile_document,
    p_promotion_policy_hash, p_promotion_policy_bytes, p_promotion_policy_document,
    p_authority_hash, p_authority_bytes, p_authority_document
  );

  INSERT INTO qualification_policy_review_defaults (
    authority_id, ordinal, change_source, canonical_review_required
  )
  SELECT p_authority_id, item.ordinal::smallint - 1,
         item.value->>'changeSource',
         (item.value->>'canonicalReviewRequired')::boolean
  FROM jsonb_array_elements(
    p_revision_policy_document->'reviewByChangeSource'
  ) WITH ORDINALITY AS item(value, ordinal);

  INSERT INTO qualification_policy_exact_approved_sources (
    authority_id, ordinal, source_kind, purpose, artifact_id, revision_id, content_hash
  )
  SELECT p_authority_id, item.ordinal::integer - 1,
         item.value->>'sourceKind', item.value->>'purpose',
         (item.value->>'artifactId')::uuid, (item.value->>'revisionId')::uuid,
         item.value->>'contentHash'
  FROM jsonb_array_elements(
    p_revision_policy_document->'exactApprovedSources'
  ) WITH ORDINALITY AS item(value, ordinal);

  INSERT INTO qualification_policy_identity_reservations (
    identity_value, identity_class, identity_role, owner_authority_id, reserved_at
  ) VALUES
    (p_authority_id, 'authority', 'authority', p_authority_id, p_issued_at),
    (p_operation_id, 'operation', 'operation', p_authority_id, p_issued_at);

  INSERT INTO qualification_policy_identity_reservations (
    identity_value, identity_class, identity_role, owner_authority_id, reserved_at
  )
  SELECT DISTINCT ON (reference.identity_value)
    reference.identity_value, 'embedded-reference', reference.identity_role,
    p_authority_id, p_issued_at
  FROM qualification_policy_embedded_uuid_references_v1(
    p_authority_document
  ) AS reference
  ORDER BY reference.identity_value, reference.identity_role
  ON CONFLICT (identity_value) DO NOTHING;

  IF EXISTS (
    SELECT 1
    FROM qualification_policy_embedded_uuid_references_v1(
      p_authority_document
    ) AS reference
    JOIN qualification_policy_identity_reservations AS identity
      ON identity.identity_value = reference.identity_value
    WHERE identity.identity_class <> 'embedded-reference'
  ) THEN
    RAISE EXCEPTION 'Qualification Policy local identity collides with an embedded UUID reference'
      USING ERRCODE = 'WPA03';
  END IF;

  IF qualification_policy_authority_record_is_exact_v1(p_authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Policy candidate bytes, hashes, document, or closure are not exact'
      USING ERRCODE = 'WPA03';
  END IF;
  RETURN QUERY SELECT * FROM qualification_policy_authorities WHERE authority_id = p_authority_id;
END;
$function$;

CREATE FUNCTION inspect_qualification_policy_operation_v1(p_operation_id uuid)
RETURNS SETOF qualification_policy_authorities
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_policy_authorities%ROWTYPE;
BEGIN
  SELECT * INTO v_authority FROM qualification_policy_authorities
  WHERE operation_id = p_operation_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Policy operation is not found' USING ERRCODE = 'WPA02';
  END IF;
  IF qualification_policy_authority_record_is_exact_v1(v_authority.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Policy operation record is not exact' USING ERRCODE = 'WPA03';
  END IF;
  RETURN NEXT v_authority;
END;
$function$;

CREATE FUNCTION resolve_qualification_policy_authority_v1(p_authority_id uuid)
RETURNS SETOF qualification_policy_authorities
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_policy_authorities%ROWTYPE;
BEGIN
  SELECT * INTO v_authority FROM qualification_policy_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Policy authority is not found' USING ERRCODE = 'WPA02';
  END IF;
  IF qualification_policy_authority_record_is_exact_v1(v_authority.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Policy authority record is not exact' USING ERRCODE = 'WPA03';
  END IF;
  RETURN NEXT v_authority;
END;
$function$;

CREATE FUNCTION resolve_current_qualification_policy_authority_v1(
  p_project_id uuid,
  p_execution_profile_version text,
  p_execution_profile_hash text
)
RETURNS SETOF qualification_policy_authorities
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority qualification_policy_authorities%ROWTYPE;
BEGIN
  SELECT * INTO v_authority
  FROM qualification_policy_authorities
  WHERE project_id = p_project_id
    AND execution_profile_version = p_execution_profile_version
    AND execution_profile_hash = p_execution_profile_hash
  ORDER BY generation DESC
  LIMIT 1;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Current Qualification Policy authority is not found' USING ERRCODE = 'WPA02';
  END IF;
  IF qualification_policy_authority_record_is_exact_v1(v_authority.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Current Qualification Policy authority is not exact' USING ERRCODE = 'WPA03';
  END IF;
  RETURN NEXT v_authority;
END;
$function$;

CREATE FUNCTION assert_current_qualification_policy_authority_v1(p_authority_id uuid)
RETURNS SETOF qualification_policy_authorities
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_project_id uuid;
  v_authority qualification_policy_authorities%ROWTYPE;
BEGIN
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  SELECT project_id INTO v_project_id
  FROM qualification_policy_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Policy authority is not found' USING ERRCODE = 'WPA02';
  END IF;
  PERFORM 1 FROM projects WHERE id = v_project_id FOR UPDATE;
  SELECT * INTO v_authority
  FROM qualification_policy_authorities
  WHERE authority_id = p_authority_id
  FOR UPDATE;
  IF v_authority.status <> 'active'
     OR qualification_policy_authority_record_is_exact_v1(p_authority_id) IS NOT TRUE
     OR EXISTS (
       SELECT 1 FROM qualification_policy_authorities AS newer
       WHERE newer.project_id = v_authority.project_id
         AND newer.execution_profile_version = v_authority.execution_profile_version
         AND newer.execution_profile_hash = v_authority.execution_profile_hash
         AND newer.generation > v_authority.generation
     ) THEN
    RAISE EXCEPTION 'Qualification Policy authority is stale, suspended, or not exact'
      USING ERRCODE = 'WPA02';
  END IF;
  RETURN NEXT v_authority;
END;
$function$;

-- The receipt binding needs one exact relational key, not five independent
-- scalar comparisons against a mutable query result.
ALTER TABLE canonical_review_approval_receipts
  ADD CONSTRAINT canonical_review_receipts_workflow_input_exact_unique UNIQUE (
    review_request_id, receipt_hash, project_id, artifact_id, revision_id, revision_content_hash
  );

ALTER TABLE workflow_run_events
  ADD CONSTRAINT workflow_run_events_workflow_input_exact_unique
    UNIQUE (id, run_id, sequence);

ALTER TABLE workflow_node_runs
  ADD COLUMN definition_node_id text,
  ADD COLUMN slice_kind text,
  ADD COLUMN slice_id uuid,
  ADD COLUMN input_authority_id uuid,
  ADD CONSTRAINT workflow_node_runs_stable_identity_shape CHECK (
    (definition_node_id IS NULL AND slice_kind IS NULL AND slice_id IS NULL)
    OR
    (
      definition_node_id IS NOT NULL
      AND octet_length(definition_node_id) BETWEEN 1 AND 256
      AND definition_node_id = btrim(definition_node_id)
      AND definition_node_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$'
      AND (
        (slice_kind = 'root' AND slice_id IS NULL)
        OR (slice_kind = 'slice' AND slice_id IS NOT NULL)
      )
    )
  ),
  ADD CONSTRAINT workflow_node_runs_workflow_input_exact_unique
    UNIQUE (id, run_id, node_key);

CREATE UNIQUE INDEX workflow_node_runs_input_authority_unique
  ON workflow_node_runs(input_authority_id)
  WHERE input_authority_id IS NOT NULL;

CREATE TABLE workflow_input_authorities (
  authority_id uuid PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE,

  -- Internal-only issuer provenance. This is excluded from recovery bundles;
  -- callers use it only to distinguish a current-transaction insert from an
  -- exact concurrent replay.
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),

  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (octet_length(request_bytes) BETWEEN 1 AND 65536),
  request_document jsonb NOT NULL CHECK (jsonb_typeof(request_document) = 'object'),

  input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
  input_bytes bytea NOT NULL CHECK (octet_length(input_bytes) BETWEEN 1 AND 33554432),
  input_document jsonb NOT NULL CHECK (jsonb_typeof(input_document) = 'object'),

  target_hash text NOT NULL CHECK (target_hash ~ '^sha256:[0-9a-f]{64}$'),
  target_bytes bytea NOT NULL CHECK (octet_length(target_bytes) BETWEEN 1 AND 262144),
  target_document jsonb NOT NULL CHECK (jsonb_typeof(target_document) = 'object'),

  authority_hash text NOT NULL UNIQUE CHECK (authority_hash ~ '^sha256:[0-9a-f]{64}$'),
  authority_bytes bytea NOT NULL CHECK (octet_length(authority_bytes) BETWEEN 1 AND 1048576),
  authority_document jsonb NOT NULL CHECK (jsonb_typeof(authority_document) = 'object'),

  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  governance_mode text NOT NULL CHECK (governance_mode IN ('solo', 'team')),

  definition_id uuid NOT NULL REFERENCES workflow_definitions(id) ON DELETE RESTRICT,
  definition_version_id uuid NOT NULL REFERENCES workflow_definition_versions(id) ON DELETE RESTRICT,
  definition_version integer NOT NULL CHECK (definition_version BETWEEN 1 AND 2147483647),
  definition_hash text NOT NULL CHECK (definition_hash ~ '^sha256:[0-9a-f]{64}$'),
  definition_raw_bytes bytea NOT NULL CHECK (octet_length(definition_raw_bytes) BETWEEN 1 AND 8388608),
  definition_raw_bytes_hash text NOT NULL CHECK (definition_raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  definition_raw_bytes_size bigint NOT NULL CHECK (definition_raw_bytes_size BETWEEN 1 AND 8388608),
  execution_profile_version text NOT NULL CHECK (execution_profile_version = 'workflow-engine/v3'),
  execution_profile_hash text NOT NULL CHECK (
    execution_profile_hash = 'sha256:854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
  ),

  workflow_run_id uuid NOT NULL,
  run_input_manifest_id uuid NOT NULL,
  run_input_manifest_hash text NOT NULL CHECK (run_input_manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
  run_scope_raw_bytes bytea NOT NULL CHECK (octet_length(run_scope_raw_bytes) BETWEEN 1 AND 8388608),
  run_scope_raw_bytes_hash text NOT NULL CHECK (run_scope_raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  run_scope_raw_bytes_size bigint NOT NULL CHECK (run_scope_raw_bytes_size BETWEEN 1 AND 8388608),
  run_started_at timestamptz NOT NULL CHECK (run_started_at = date_trunc('milliseconds', run_started_at)),
  run_started_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,

  node_run_id uuid NOT NULL,
  node_key text NOT NULL CHECK (octet_length(node_key) BETWEEN 1 AND 256),
  node_type text NOT NULL CHECK (node_type = 'external_qualification_gate'),
  definition_node_id text NOT NULL CHECK (octet_length(definition_node_id) BETWEEN 1 AND 256),
  slice_kind text NOT NULL CHECK (slice_kind IN ('root', 'slice')),
  slice_id uuid,
  gate_name text NOT NULL CHECK (gate_name = 'external-qualification'),
  stage_gate text NOT NULL CHECK (stage_gate = 'external-qualification'),
  activation_event_id uuid NOT NULL,
  activation_event_sequence bigint NOT NULL CHECK (
    activation_event_sequence BETWEEN 1 AND 9007199254740991
  ),

  node_input_raw_bytes bytea NOT NULL CHECK (octet_length(node_input_raw_bytes) BETWEEN 1 AND 16777216),
  node_input_raw_bytes_hash text NOT NULL CHECK (node_input_raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  node_input_raw_bytes_size bigint NOT NULL CHECK (node_input_raw_bytes_size BETWEEN 1 AND 16777216),
  node_input_semantic_hash text NOT NULL CHECK (node_input_semantic_hash ~ '^sha256:[0-9a-f]{64}$'),
  node_input_binding_count integer NOT NULL CHECK (node_input_binding_count BETWEEN 1 AND 1024),

  manifest_subject text NOT NULL CHECK (
    octet_length(manifest_subject) BETWEEN 1 AND 256
    AND manifest_subject = btrim(manifest_subject)
    AND manifest_subject !~ '[[:cntrl:]]'
  ),
  target_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  target_revision_id uuid NOT NULL,
  target_revision_content_hash text NOT NULL CHECK (target_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'),

  quality_run_id uuid NOT NULL,
  quality_passed boolean NOT NULL CHECK (quality_passed),
  quality_workspace_revision_id uuid NOT NULL,
  quality_workspace_revision_content_hash text NOT NULL CHECK (
    quality_workspace_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),

  build_manifest_id uuid NOT NULL,
  build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_manifest_content_hash text NOT NULL CHECK (build_manifest_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_manifest_status text NOT NULL CHECK (build_manifest_status = 'consumed'),
  build_manifest_raw_bytes bytea NOT NULL CHECK (octet_length(build_manifest_raw_bytes) BETWEEN 1 AND 8388608),
  build_manifest_raw_bytes_hash text NOT NULL CHECK (build_manifest_raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_manifest_raw_bytes_size bigint NOT NULL CHECK (build_manifest_raw_bytes_size BETWEEN 1 AND 8388608),

  build_contract_id uuid NOT NULL,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_content_hash text NOT NULL CHECK (build_contract_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_status text NOT NULL CHECK (build_contract_status = 'ready'),
  build_contract_raw_bytes bytea NOT NULL CHECK (octet_length(build_contract_raw_bytes) BETWEEN 1 AND 8388608),
  build_contract_raw_bytes_hash text NOT NULL CHECK (build_contract_raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_raw_bytes_size bigint NOT NULL CHECK (build_contract_raw_bytes_size BETWEEN 1 AND 8388608),

  qualification_policy_authority_id uuid NOT NULL,
  qualification_policy_authority_hash text NOT NULL CHECK (
    qualification_policy_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  external_gate_policy text NOT NULL CHECK (external_gate_policy = 'external-qualification/v1'),

  predecessor_count integer NOT NULL CHECK (predecessor_count BETWEEN 1 AND 1024),
  manifest_count integer NOT NULL CHECK (manifest_count BETWEEN 1 AND 1024),
  revision_count integer NOT NULL CHECK (revision_count BETWEEN 1 AND 2048),
  review_receipt_count integer NOT NULL CHECK (review_receipt_count BETWEEN 0 AND 2048),
  frozen_at timestamptz NOT NULL CHECK (frozen_at = date_trunc('milliseconds', frozen_at)),

  CONSTRAINT workflow_input_authority_v4_identities CHECK (
    workflow_input_uuid_is_exact(authority_id::text)
    AND workflow_input_uuid_is_exact(operation_id::text)
    AND workflow_input_uuid_is_exact(project_id::text)
    AND workflow_input_uuid_is_exact(definition_id::text)
    AND workflow_input_uuid_is_exact(definition_version_id::text)
    AND workflow_input_uuid_is_exact(workflow_run_id::text)
    AND workflow_input_uuid_is_exact(run_input_manifest_id::text)
    AND workflow_input_uuid_is_exact(run_started_by::text)
    AND workflow_input_uuid_is_exact(node_run_id::text)
    AND workflow_input_uuid_is_exact(activation_event_id::text)
    AND workflow_input_uuid_is_exact(target_artifact_id::text)
    AND workflow_input_uuid_is_exact(target_revision_id::text)
    AND workflow_input_uuid_is_exact(quality_run_id::text)
    AND workflow_input_uuid_is_exact(quality_workspace_revision_id::text)
    AND workflow_input_uuid_is_exact(build_manifest_id::text)
    AND workflow_input_uuid_is_exact(build_contract_id::text)
    AND workflow_input_uuid_is_exact(qualification_policy_authority_id::text)
  ),
  CONSTRAINT workflow_input_authority_local_identities_distinct CHECK (
    authority_id <> operation_id
    AND authority_id <> activation_event_id
    AND operation_id <> activation_event_id
    AND authority_id <> workflow_run_id
    AND authority_id <> node_run_id
    AND operation_id <> workflow_run_id
    AND operation_id <> node_run_id
  ),
  CONSTRAINT workflow_input_authority_slice_shape CHECK (
    (slice_kind = 'root' AND slice_id IS NULL)
    OR (slice_kind = 'slice' AND slice_id IS NOT NULL)
  ),
  CONSTRAINT workflow_input_authority_run_fk FOREIGN KEY (workflow_run_id, project_id)
    REFERENCES workflow_runs(id, project_id) ON DELETE RESTRICT,
  CONSTRAINT workflow_input_authority_node_fk FOREIGN KEY (node_run_id, workflow_run_id, node_key)
    REFERENCES workflow_node_runs(id, run_id, node_key) ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT workflow_input_authority_activation_event_fk FOREIGN KEY (
    activation_event_id, workflow_run_id, activation_event_sequence
  ) REFERENCES workflow_run_events(id, run_id, sequence) ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT workflow_input_authority_target_revision_fk FOREIGN KEY (target_revision_id)
    REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  CONSTRAINT workflow_input_authority_quality_run_fk FOREIGN KEY (quality_run_id)
    REFERENCES quality_runs(id) ON DELETE RESTRICT,
  CONSTRAINT workflow_input_authority_quality_target_equal CHECK (
    quality_workspace_revision_id = target_revision_id
    AND quality_workspace_revision_content_hash = target_revision_content_hash
  ),
  CONSTRAINT workflow_input_authority_build_contract_fk FOREIGN KEY (
    build_contract_id, project_id, build_manifest_id, build_contract_hash
  ) REFERENCES application_build_contracts(id, project_id, build_manifest_id, contract_hash)
    ON DELETE RESTRICT,
  CONSTRAINT workflow_input_authority_qualification_policy_exact_fk FOREIGN KEY (
    qualification_policy_authority_id, project_id, execution_profile_version,
    execution_profile_hash, qualification_policy_authority_hash
  ) REFERENCES qualification_policy_authorities(
    authority_id, project_id, execution_profile_version,
    execution_profile_hash, authority_hash
  ) ON DELETE RESTRICT,
  CONSTRAINT workflow_input_authority_exact_node_unique UNIQUE (
    authority_id, workflow_run_id, node_run_id
  ),
  CONSTRAINT workflow_input_authority_node_generation_unique UNIQUE (
    workflow_run_id, node_run_id
  )
);

CREATE INDEX workflow_input_authorities_project_target_idx
  ON workflow_input_authorities(project_id, target_artifact_id, target_revision_id);
CREATE INDEX workflow_input_authorities_run_event_idx
  ON workflow_input_authorities(workflow_run_id, activation_event_sequence);

ALTER TABLE workflow_node_runs
  ADD CONSTRAINT workflow_node_runs_input_authority_fk FOREIGN KEY (
    input_authority_id, run_id, id
  ) REFERENCES workflow_input_authorities(authority_id, workflow_run_id, node_run_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE workflow_input_authority_identity_reservations (
  identity_value uuid PRIMARY KEY,
  authority_id uuid NOT NULL REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  identity_kind text NOT NULL CHECK (identity_kind IN ('authority', 'freeze-operation', 'activation-event')),
  ordinal integer NOT NULL CHECK (ordinal = 0),
  reserved_at timestamptz NOT NULL CHECK (reserved_at = date_trunc('milliseconds', reserved_at)),
  UNIQUE (authority_id, identity_kind, ordinal),
  CHECK (workflow_input_uuid_is_exact(identity_value::text))
);

CREATE INDEX workflow_input_authority_identity_authority_idx
  ON workflow_input_authority_identity_reservations(authority_id);

CREATE TABLE workflow_input_authority_predecessors (
  authority_id uuid NOT NULL REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 1023),
  edge_id text NOT NULL CHECK (octet_length(edge_id) BETWEEN 1 AND 256),
  source_node_run_id uuid NOT NULL REFERENCES workflow_node_runs(id) ON DELETE RESTRICT,
  source_node_key text NOT NULL CHECK (octet_length(source_node_key) BETWEEN 1 AND 256),
  source_definition_node_id text NOT NULL CHECK (octet_length(source_definition_node_id) BETWEEN 1 AND 256),
  source_node_type text NOT NULL CHECK (octet_length(source_node_type) BETWEEN 1 AND 128),
  source_slice_kind text NOT NULL CHECK (source_slice_kind IN ('root', 'slice')),
  source_slice_id uuid,
  source_status text NOT NULL CHECK (source_status = 'completed'),
  output_revision_number bigint NOT NULL CHECK (output_revision_number BETWEEN 1 AND 9007199254740991),
  source_port text NOT NULL CHECK (octet_length(source_port) BETWEEN 1 AND 256),
  target_port text NOT NULL CHECK (octet_length(target_port) BETWEEN 1 AND 256),
  mapping_kind text NOT NULL CHECK (mapping_kind IN ('identity', 'object-map')),
  mapping_ordinal integer NOT NULL CHECK (mapping_ordinal BETWEEN 0 AND 1023),
  output_hash text NOT NULL CHECK (output_hash ~ '^sha256:[0-9a-f]{64}$'),
  value_hash text NOT NULL CHECK (value_hash ~ '^sha256:[0-9a-f]{64}$'),
  member_document jsonb NOT NULL CHECK (jsonb_typeof(member_document) = 'object'),
  PRIMARY KEY (authority_id, ordinal),
  UNIQUE (authority_id, edge_id, source_node_run_id, source_port, target_port),
  CHECK (
    (source_slice_kind = 'root' AND source_slice_id IS NULL)
    OR (source_slice_kind = 'slice' AND source_slice_id IS NOT NULL)
  )
);

CREATE INDEX workflow_input_authority_predecessors_source_idx
  ON workflow_input_authority_predecessors(source_node_run_id, authority_id);

CREATE TABLE workflow_input_authority_manifests (
  authority_id uuid NOT NULL REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 1023),
  role text NOT NULL CHECK (role IN ('run', 'predecessor')),
  manifest_id uuid NOT NULL REFERENCES input_manifests(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  kind text NOT NULL CHECK (octet_length(kind) BETWEEN 1 AND 128),
  schema_version integer NOT NULL CHECK (schema_version BETWEEN 1 AND 2147483647),
  manifest_hash text NOT NULL CHECK (manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_store text NOT NULL CHECK (octet_length(content_store) BETWEEN 1 AND 128),
  content_ref text NOT NULL CHECK (octet_length(content_ref) BETWEEN 1 AND 65536),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  raw_bytes bytea NOT NULL CHECK (octet_length(raw_bytes) BETWEEN 1 AND 8388608),
  raw_bytes_hash text NOT NULL CHECK (raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  raw_bytes_size bigint NOT NULL CHECK (raw_bytes_size BETWEEN 1 AND 8388608),
  member_document jsonb NOT NULL CHECK (jsonb_typeof(member_document) = 'object'),
  PRIMARY KEY (authority_id, ordinal),
  UNIQUE (authority_id, role, manifest_id)
);

CREATE INDEX workflow_input_authority_manifests_manifest_idx
  ON workflow_input_authority_manifests(manifest_id, authority_id);

CREATE TABLE workflow_input_authority_revisions (
  authority_id uuid NOT NULL REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 2047),
  purpose text NOT NULL CHECK (
    octet_length(purpose) BETWEEN 1 AND 256 AND purpose = btrim(purpose)
  ),
  artifact_kind text NOT NULL CHECK (octet_length(artifact_kind) BETWEEN 1 AND 128),
  artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_store text NOT NULL CHECK (octet_length(content_store) BETWEEN 1 AND 128),
  content_ref text NOT NULL CHECK (octet_length(content_ref) BETWEEN 1 AND 65536),
  schema_version integer NOT NULL CHECK (schema_version BETWEEN 1 AND 2147483647),
  byte_size bigint NOT NULL CHECK (byte_size BETWEEN 1 AND 9007199254740991),
  raw_bytes bytea NOT NULL CHECK (octet_length(raw_bytes) BETWEEN 1 AND 16777216),
  raw_bytes_hash text NOT NULL CHECK (raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  workflow_status_at_freeze text NOT NULL CHECK (workflow_status_at_freeze = 'approved'),
  source_manifest_id uuid,
  proposal_id uuid,
  implementation_proposal_id uuid,
  was_latest_current boolean NOT NULL,
  was_latest_approved boolean NOT NULL,
  canonical_review_required boolean NOT NULL,
  change_source_at_freeze text NOT NULL CHECK (change_source_at_freeze IN (
    'ai_proposal','human','import','merge','rollback','system'
  )),
  source_required_at_freeze boolean NOT NULL,
  currency_policy text NOT NULL CHECK (
    currency_policy IN ('latest-approved-required','exact-approved')
  ),
  member_document jsonb NOT NULL CHECK (jsonb_typeof(member_document) = 'object'),
  PRIMARY KEY (authority_id, ordinal),
  UNIQUE (authority_id, purpose, artifact_id, revision_id)
);

CREATE INDEX workflow_input_authority_revisions_revision_idx
  ON workflow_input_authority_revisions(revision_id, authority_id);

CREATE TABLE workflow_input_authority_review_receipts (
  authority_id uuid NOT NULL REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 2047),
  purpose text NOT NULL CHECK (
    octet_length(purpose) BETWEEN 1 AND 256 AND purpose = btrim(purpose)
  ),
  review_request_id uuid NOT NULL,
  receipt_hash text NOT NULL CHECK (receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  receipt_bytes bytea NOT NULL CHECK (octet_length(receipt_bytes) BETWEEN 1 AND 1048576),
  receipt_document jsonb NOT NULL CHECK (jsonb_typeof(receipt_document) = 'object'),
  receipt_raw_bytes_hash text NOT NULL CHECK (receipt_raw_bytes_hash ~ '^sha256:[0-9a-f]{64}$'),
  receipt_raw_bytes_size bigint NOT NULL CHECK (receipt_raw_bytes_size BETWEEN 1 AND 1048576),
  project_id uuid NOT NULL,
  artifact_id uuid NOT NULL,
  revision_id uuid NOT NULL,
  revision_content_hash text NOT NULL CHECK (revision_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  receipt_schema_version text NOT NULL CHECK (
    receipt_schema_version = 'worksflow-canonical-review-approval-receipt/v1'
  ),
  member_document jsonb NOT NULL CHECK (jsonb_typeof(member_document) = 'object'),
  PRIMARY KEY (authority_id, ordinal),
  UNIQUE (authority_id, purpose, review_request_id),
  UNIQUE (authority_id, revision_id),
  CONSTRAINT workflow_input_review_receipt_exact_fk FOREIGN KEY (
    review_request_id, receipt_hash, project_id, artifact_id, revision_id, revision_content_hash
  ) REFERENCES canonical_review_approval_receipts(
    review_request_id, receipt_hash, project_id, artifact_id, revision_id, revision_content_hash
  ) ON DELETE RESTRICT
);

CREATE INDEX workflow_input_authority_review_receipts_revision_idx
  ON workflow_input_authority_review_receipts(revision_id, authority_id);

CREATE FUNCTION reject_workflow_input_authority_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  RAISE EXCEPTION 'Workflow Input Authority records are immutable'
    USING ERRCODE = 'WIA02';
END;
$function$;

CREATE TRIGGER workflow_input_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON workflow_input_authorities
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_input_authority_mutation();
CREATE TRIGGER workflow_input_authority_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON workflow_input_authority_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_input_authority_mutation();
CREATE TRIGGER workflow_input_authority_predecessors_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON workflow_input_authority_predecessors
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_input_authority_mutation();
CREATE TRIGGER workflow_input_authority_manifests_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON workflow_input_authority_manifests
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_input_authority_mutation();
CREATE TRIGGER workflow_input_authority_revisions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON workflow_input_authority_revisions
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_input_authority_mutation();
CREATE TRIGGER workflow_input_authority_review_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON workflow_input_authority_review_receipts
FOR EACH STATEMENT EXECUTE FUNCTION reject_workflow_input_authority_mutation();

CREATE FUNCTION guard_workflow_node_stable_identity_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  IF OLD.run_id IS DISTINCT FROM NEW.run_id
     OR OLD.node_key IS DISTINCT FROM NEW.node_key
     OR OLD.node_type IS DISTINCT FROM NEW.node_type
     OR OLD.definition_node_id IS DISTINCT FROM NEW.definition_node_id
     OR OLD.slice_kind IS DISTINCT FROM NEW.slice_kind
     OR OLD.slice_id IS DISTINCT FROM NEW.slice_id THEN
    RAISE EXCEPTION 'Workflow node run stable identity is immutable'
      USING ERRCODE = 'WIA02';
  END IF;
  IF OLD.input_authority_id IS NOT NULL
     AND OLD.input_authority_id IS DISTINCT FROM NEW.input_authority_id THEN
    RAISE EXCEPTION 'Workflow node Input Authority binding is immutable'
      USING ERRCODE = 'WIA02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER workflow_node_stable_identity_v1_immutable
BEFORE UPDATE ON workflow_node_runs
FOR EACH ROW EXECUTE FUNCTION guard_workflow_node_stable_identity_v1();

CREATE FUNCTION workflow_input_authority_record_is_exact(p_authority_id uuid)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
AS $function$
DECLARE
  v_authority workflow_input_authorities%ROWTYPE;
  v_predecessors jsonb;
  v_manifests jsonb;
  v_revisions jsonb;
  v_receipts jsonb;
  v_expected_request jsonb;
  v_expected_target jsonb;
  v_expected_input jsonb;
  v_expected_envelope jsonb;
  v_slice_identity jsonb;
  v_build_contract_document jsonb;
  v_retained_bytes bigint;
BEGIN
  SELECT * INTO v_authority
  FROM workflow_input_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN
    RETURN false;
  END IF;

  v_retained_bytes := octet_length(v_authority.definition_raw_bytes)
    + octet_length(v_authority.run_scope_raw_bytes)
    + octet_length(v_authority.node_input_raw_bytes)
    + octet_length(v_authority.build_manifest_raw_bytes)
    + octet_length(v_authority.build_contract_raw_bytes)
    + COALESCE((
      SELECT sum(octet_length(raw_bytes))
      FROM workflow_input_authority_manifests
      WHERE authority_id = p_authority_id
    ), 0)
    + COALESCE((
      SELECT sum(octet_length(raw_bytes))
      FROM workflow_input_authority_revisions
      WHERE authority_id = p_authority_id
    ), 0)
    + COALESCE((
      SELECT sum(octet_length(receipt_bytes))
      FROM workflow_input_authority_review_receipts
      WHERE authority_id = p_authority_id
    ), 0);
  IF v_retained_bytes > 134217728 THEN
    RETURN false;
  END IF;

  IF v_authority.definition_raw_bytes_size <> octet_length(v_authority.definition_raw_bytes)
     OR v_authority.definition_raw_bytes_hash <> workflow_input_raw_sha256(v_authority.definition_raw_bytes)
     OR v_authority.run_scope_raw_bytes_size <> octet_length(v_authority.run_scope_raw_bytes)
     OR v_authority.run_scope_raw_bytes_hash <> workflow_input_raw_sha256(v_authority.run_scope_raw_bytes)
     OR v_authority.node_input_raw_bytes_size <> octet_length(v_authority.node_input_raw_bytes)
     OR v_authority.node_input_raw_bytes_hash <> workflow_input_raw_sha256(v_authority.node_input_raw_bytes)
     OR v_authority.build_manifest_raw_bytes_size <> octet_length(v_authority.build_manifest_raw_bytes)
     OR v_authority.build_manifest_raw_bytes_hash <> workflow_input_raw_sha256(v_authority.build_manifest_raw_bytes)
     OR v_authority.build_contract_raw_bytes_size <> octet_length(v_authority.build_contract_raw_bytes)
     OR v_authority.build_contract_raw_bytes_hash <> workflow_input_raw_sha256(v_authority.build_contract_raw_bytes) THEN
    RETURN false;
  END IF;
  v_build_contract_document := convert_from(v_authority.build_contract_raw_bytes, 'UTF8')::jsonb;
  IF qualification_policy_authority_record_is_exact_v1(
       v_authority.qualification_policy_authority_id
     ) IS NOT TRUE
     OR NOT EXISTS (
       SELECT 1
       FROM qualification_policy_authorities AS policy
       WHERE policy.authority_id = v_authority.qualification_policy_authority_id
         AND policy.project_id = v_authority.project_id
         AND policy.execution_profile_version = v_authority.execution_profile_version
         AND policy.execution_profile_hash = v_authority.execution_profile_hash
         AND policy.authority_hash = v_authority.qualification_policy_authority_hash
         AND policy.external_gate_policy = v_authority.external_gate_policy
     ) THEN
    RETURN false;
  END IF;
  IF jsonb_typeof(v_build_contract_document->'sourceRevisions') IS DISTINCT FROM 'array'
     OR jsonb_typeof(v_build_contract_document->'templateReleaseRefs') IS DISTINCT FROM 'array'
     OR jsonb_typeof(v_build_contract_document->'obligations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_build_contract_document->'sourceRevisions') <> (
       SELECT count(*) FROM application_build_contract_sources
       WHERE contract_id = v_authority.build_contract_id
     )
     OR jsonb_array_length(v_build_contract_document->'templateReleaseRefs') <> (
       SELECT count(*) FROM application_build_contract_template_releases
       WHERE contract_id = v_authority.build_contract_id
     )
     OR jsonb_array_length(v_build_contract_document->'obligations') <> (
       SELECT count(*) FROM application_build_contract_obligations
       WHERE contract_id = v_authority.build_contract_id
     )
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_build_contract_document->'sourceRevisions') WITH ORDINALITY
         AS item(value, ordinal)
       LEFT JOIN application_build_contract_sources AS source
         ON source.contract_id = v_authority.build_contract_id AND source.ordinal = item.ordinal - 1
       WHERE source.contract_id IS NULL
          OR item.value->>'kind' <> source.source_kind
          OR item.value->>'purpose' <> source.purpose
          OR item.value->'required' IS DISTINCT FROM to_jsonb(source.required)
          OR item.value->>'artifactId' <> source.artifact_id::text
          OR item.value->>'revisionId' <> source.revision_id::text
          OR workflow_input_normalize_sha256(item.value->>'contentHash') <>
             workflow_input_normalize_sha256(source.content_hash)
          OR item.value->>'approvalStatus' <> 'approved'
     )
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_build_contract_document->'templateReleaseRefs') WITH ORDINALITY
         AS item(value, ordinal)
       LEFT JOIN application_build_contract_template_releases AS release
         ON release.contract_id = v_authority.build_contract_id AND release.ordinal = item.ordinal - 1
       WHERE release.contract_id IS NULL
          OR item.value->>'id' <> release.template_release_id::text
          OR workflow_input_normalize_sha256(item.value->>'releaseHash') <>
             workflow_input_normalize_sha256(release.template_release_content_hash)
          OR item.value->>'role' <> release.role
          OR item.value->>'certification' <> 'approved'
          OR item.value->>'policyStatus' <> 'active'
     )
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_build_contract_document->'obligations') WITH ORDINALITY
         AS item(value, ordinal)
       LEFT JOIN application_build_contract_obligations AS obligation
         ON obligation.contract_id = v_authority.build_contract_id
        AND obligation.obligation_id = item.value->>'id'
       LEFT JOIN artifacts AS obligation_artifact
         ON obligation_artifact.id = obligation.source_artifact_id
       LEFT JOIN application_build_contract_sources AS obligation_source
         ON obligation_source.contract_id = obligation.contract_id
        AND obligation_source.artifact_id = obligation.source_artifact_id
        AND obligation_source.revision_id = obligation.source_revision_id
        AND workflow_input_normalize_sha256(obligation_source.content_hash) =
            workflow_input_normalize_sha256(obligation.source_content_hash)
       WHERE obligation.contract_id IS NULL
          OR item.ordinal <> 1 + (
            SELECT count(*)
            FROM application_build_contract_obligations AS earlier
            WHERE earlier.contract_id = v_authority.build_contract_id
              AND convert_to(earlier.obligation_id, 'UTF8') < convert_to(obligation.obligation_id, 'UTF8')
          )
          OR item.value->>'level' <> obligation.level
          OR item.value->>'kind' <> obligation.kind
          OR item.value#>>'{sourceRevision,kind}' <> obligation_artifact.kind
          OR item.value#>>'{sourceRevision,purpose}' <> CASE
            WHEN obligation.source_revision_id = v_authority.target_revision_id THEN 'workspace-target'
            ELSE obligation_source.purpose
          END
          OR item.value#>'{sourceRevision,required}' IS DISTINCT FROM to_jsonb(CASE
            WHEN obligation.source_revision_id = v_authority.target_revision_id THEN true
            ELSE obligation_source.required
          END)
          OR item.value#>>'{sourceRevision,artifactId}' <> obligation.source_artifact_id::text
          OR item.value#>>'{sourceRevision,revisionId}' <> obligation.source_revision_id::text
          OR workflow_input_normalize_sha256(item.value#>>'{sourceRevision,contentHash}') <>
             workflow_input_normalize_sha256(obligation.source_content_hash)
          OR item.value#>>'{sourceRevision,approvalStatus}' <> 'approved'
          OR item.value->>'sourceAnchorId' <> obligation.source_anchor_id
          OR item.value->'oracleIds' IS DISTINCT FROM obligation.oracle_ids
          OR item.value->'dependsOn' IS DISTINCT FROM obligation.depends_on
          OR item.value->'waivable' IS DISTINCT FROM to_jsonb(obligation.waivable)
          OR item.value->>'status' <> obligation.status
          OR item.value->>'blockingReasonId' IS DISTINCT FROM obligation.blocking_reason_id
     ) THEN
    RETURN false;
  END IF;

  IF v_authority.node_input_binding_count <> v_authority.predecessor_count
     OR v_authority.review_receipt_count <> (
       SELECT count(*) FROM workflow_input_authority_revisions
       WHERE authority_id = p_authority_id AND canonical_review_required
     )
     OR (SELECT count(*) FROM workflow_input_authority_identity_reservations
         WHERE authority_id = p_authority_id) <> 3
     OR NOT EXISTS (
       SELECT 1 FROM workflow_input_authority_identity_reservations
       WHERE authority_id = p_authority_id AND identity_value = v_authority.authority_id
         AND identity_kind = 'authority' AND ordinal = 0
     )
     OR NOT EXISTS (
       SELECT 1 FROM workflow_input_authority_identity_reservations
       WHERE authority_id = p_authority_id AND identity_value = v_authority.operation_id
         AND identity_kind = 'freeze-operation' AND ordinal = 0
     )
     OR NOT EXISTS (
       SELECT 1 FROM workflow_input_authority_identity_reservations
       WHERE authority_id = p_authority_id AND identity_value = v_authority.activation_event_id
         AND identity_kind = 'activation-event' AND ordinal = 0
     )
     OR (SELECT count(*) FROM workflow_input_authority_revisions
         WHERE authority_id = p_authority_id
           AND purpose = 'workspace-target'
           AND artifact_id = v_authority.target_artifact_id
           AND revision_id = v_authority.target_revision_id
           AND content_hash = v_authority.target_revision_content_hash
           AND currency_policy = 'latest-approved-required'
           AND canonical_review_required IS FALSE
           AND change_source_at_freeze = 'system'
           AND source_required_at_freeze IS FALSE) <> 1
     OR (SELECT count(*) FROM workflow_input_authority_manifests
         WHERE authority_id = p_authority_id
           AND role = 'run'
           AND manifest_id = v_authority.run_input_manifest_id
           AND manifest_hash = v_authority.run_input_manifest_hash) <> 1
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_review_receipts AS receipt
       WHERE receipt.authority_id = p_authority_id
         AND NOT EXISTS (
           SELECT 1 FROM workflow_input_authority_revisions AS revision
           WHERE revision.authority_id = receipt.authority_id
             AND revision.purpose = receipt.purpose
             AND revision.artifact_id = receipt.artifact_id
             AND revision.revision_id = receipt.revision_id
             AND revision.content_hash = receipt.revision_content_hash
             AND revision.canonical_review_required
         )
     )
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_review_receipts AS receipt
       WHERE receipt.authority_id = p_authority_id
         AND receipt.purpose = 'workspace-target'
     )
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_revisions AS revision
       WHERE revision.authority_id = p_authority_id
         AND revision.canonical_review_required
         AND NOT EXISTS (
           SELECT 1 FROM workflow_input_authority_review_receipts AS receipt
           WHERE receipt.authority_id = revision.authority_id
             AND receipt.purpose = revision.purpose
             AND receipt.artifact_id = revision.artifact_id
             AND receipt.revision_id = revision.revision_id
             AND receipt.revision_content_hash = revision.content_hash
         )
     )
     OR EXISTS (
       SELECT 1
       FROM workflow_input_authority_revisions AS frozen
       JOIN qualification_policy_authorities AS policy
         ON policy.authority_id = v_authority.qualification_policy_authority_id
       LEFT JOIN artifact_revisions AS current_revision
         ON current_revision.id = frozen.revision_id
       LEFT JOIN application_build_contract_sources AS source
         ON source.contract_id = v_authority.build_contract_id
        AND source.purpose = frozen.purpose
        AND source.artifact_id = frozen.artifact_id
        AND source.revision_id = frozen.revision_id
        AND workflow_input_normalize_sha256(source.content_hash) = frozen.content_hash
       LEFT JOIN qualification_policy_review_defaults AS review_rule
         ON review_rule.authority_id = policy.authority_id
        AND review_rule.change_source = frozen.change_source_at_freeze
       WHERE frozen.authority_id = p_authority_id
         AND (
           current_revision.id IS NULL
           OR current_revision.change_source IS DISTINCT FROM frozen.change_source_at_freeze
           OR (
             frozen.purpose = 'workspace-target'
             AND (
               frozen.currency_policy IS DISTINCT FROM
                 policy.revision_policy_document#>>'{workspaceTarget,currencyPolicy}'
               OR frozen.canonical_review_required IS DISTINCT FROM
                 (policy.revision_policy_document#>>'{workspaceTarget,canonicalReviewRequired}')::boolean
               OR frozen.canonical_review_required
               OR frozen.source_required_at_freeze
             )
           )
           OR (
             frozen.purpose <> 'workspace-target'
             AND (
               source.contract_id IS NULL
               OR review_rule.authority_id IS NULL
               OR frozen.source_required_at_freeze IS DISTINCT FROM source.required
               OR frozen.canonical_review_required IS DISTINCT FROM review_rule.canonical_review_required
               OR frozen.currency_policy IS DISTINCT FROM CASE WHEN EXISTS (
                 SELECT 1
                 FROM qualification_policy_exact_approved_sources AS exact_source
                 WHERE exact_source.authority_id = policy.authority_id
                   AND exact_source.source_kind = frozen.artifact_kind
                   AND exact_source.purpose = frozen.purpose
                   AND exact_source.artifact_id = frozen.artifact_id
                   AND exact_source.revision_id = frozen.revision_id
                   AND exact_source.content_hash = frozen.content_hash
               ) THEN 'exact-approved'
                 ELSE policy.revision_policy_document->>'sourceCurrencyPolicy' END
             )
           )
         )
     ) THEN
    RETURN false;
  END IF;

  IF EXISTS (
    SELECT 1 FROM workflow_input_authority_predecessors AS member
    WHERE member.authority_id = p_authority_id
      AND (
        member.ordinal <> member.mapping_ordinal
        OR member.member_document->>'edgeId' IS DISTINCT FROM member.edge_id
        OR member.member_document->>'sourceNodeRunId' IS DISTINCT FROM member.source_node_run_id::text
        OR member.member_document->>'sourceNodeKey' IS DISTINCT FROM member.source_node_key
        OR member.member_document->>'sourceDefinitionNodeId' IS DISTINCT FROM member.source_definition_node_id
        OR member.member_document->>'sourceNodeType' IS DISTINCT FROM member.source_node_type
        OR member.member_document->>'sourceStatus' IS DISTINCT FROM member.source_status
        OR member.member_document->>'sourcePort' IS DISTINCT FROM member.source_port
        OR member.member_document->>'targetPort' IS DISTINCT FROM member.target_port
        OR member.member_document->>'mappingKind' IS DISTINCT FROM member.mapping_kind
        OR member.member_document->'mappingOrdinal' IS DISTINCT FROM to_jsonb(member.mapping_ordinal)
        OR member.member_document->'outputRevisionNumber' IS DISTINCT FROM to_jsonb(member.output_revision_number)
        OR member.member_document->>'outputHash' IS DISTINCT FROM member.output_hash
        OR member.member_document->>'valueHash' IS DISTINCT FROM member.value_hash
        OR member.member_document->'sourceSliceIdentity' IS DISTINCT FROM
          CASE WHEN member.source_slice_kind = 'root'
            THEN jsonb_build_object('kind', 'root')
            ELSE jsonb_build_object('id', member.source_slice_id::text, 'kind', 'slice') END
      )
  ) OR EXISTS (
    SELECT 1 FROM workflow_input_authority_manifests AS member
    WHERE member.authority_id = p_authority_id
      AND (
        member.raw_bytes_size <> octet_length(member.raw_bytes)
        OR member.raw_bytes_hash <> workflow_input_raw_sha256(member.raw_bytes)
        OR member.content_hash <> member.raw_bytes_hash
        OR member.member_document IS DISTINCT FROM jsonb_build_object(
          'contentHash', member.content_hash,
          'contentRef', member.content_ref,
          'contentStore', member.content_store,
          'id', member.manifest_id::text,
          'kind', member.kind,
          'manifestHash', member.manifest_hash,
          'projectId', member.project_id::text,
          'rawBytesHash', member.raw_bytes_hash,
          'rawBytesSize', member.raw_bytes_size,
          'role', member.role,
          'schemaVersion', member.schema_version
        )
      )
  ) OR EXISTS (
    SELECT 1 FROM workflow_input_authority_revisions AS member
    WHERE member.authority_id = p_authority_id
      AND (
        member.byte_size <> octet_length(member.raw_bytes)
        OR member.raw_bytes_hash <> workflow_input_raw_sha256(member.raw_bytes)
        OR member.content_hash <> member.raw_bytes_hash
        OR member.member_document IS DISTINCT FROM jsonb_build_object(
          'artifactId', member.artifact_id::text,
          'artifactKind', member.artifact_kind,
          'byteSize', member.byte_size,
          'canonicalReviewRequired', member.canonical_review_required,
          'changeSourceAtFreeze', member.change_source_at_freeze,
          'contentHash', member.content_hash,
          'contentRef', member.content_ref,
          'contentStore', member.content_store,
          'currencyPolicy', member.currency_policy,
          'implementationProposalId', CASE WHEN member.implementation_proposal_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(member.implementation_proposal_id::text) END,
          'isLatestApprovedAtFreeze', member.was_latest_approved,
          'isLatestCurrentAtFreeze', member.was_latest_current,
          'proposalId', CASE WHEN member.proposal_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(member.proposal_id::text) END,
          'purpose', member.purpose,
          'rawBytesHash', member.raw_bytes_hash,
          'revisionId', member.revision_id::text,
          'schemaVersion', member.schema_version,
          'sourceRequiredAtFreeze', member.source_required_at_freeze,
          'sourceManifestId', CASE WHEN member.source_manifest_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(member.source_manifest_id::text) END,
          'workflowStatusAtFreeze', member.workflow_status_at_freeze
        )
      )
  ) OR EXISTS (
    SELECT 1
    FROM workflow_input_authority_review_receipts AS member
    LEFT JOIN canonical_review_approval_receipts AS receipt
      ON receipt.review_request_id = member.review_request_id
     AND receipt.receipt_hash = member.receipt_hash
    WHERE member.authority_id = p_authority_id
      AND (
        receipt.review_request_id IS NULL
        OR
        member.receipt_raw_bytes_size <> octet_length(member.receipt_bytes)
        OR member.receipt_raw_bytes_hash <> workflow_input_raw_sha256(member.receipt_bytes)
        OR canonical_review_approval_receipt_record_is_exact(receipt) IS NOT TRUE
        OR member.receipt_bytes IS DISTINCT FROM receipt.receipt_bytes
        OR member.receipt_document IS DISTINCT FROM receipt.receipt_document
        OR member.member_document IS DISTINCT FROM jsonb_build_object(
          'artifactId', member.artifact_id::text,
          'projectId', member.project_id::text,
          'purpose', member.purpose,
          'receiptHash', member.receipt_hash,
          'receiptRawBytesHash', member.receipt_raw_bytes_hash,
          'receiptRawBytesSize', member.receipt_raw_bytes_size,
          'receiptSchemaVersion', member.receipt_schema_version,
          'reviewRequestId', member.review_request_id::text,
          'revisionContentHash', member.revision_content_hash,
          'revisionId', member.revision_id::text
        )
      )
  ) OR EXISTS (
    SELECT 1 FROM workflow_input_authority_predecessors AS predecessor
    WHERE predecessor.authority_id = p_authority_id
      AND predecessor.source_node_run_id = v_authority.quality_run_id
  ) OR (SELECT count(*)
        FROM workflow_input_authority_predecessors AS predecessor
        WHERE predecessor.authority_id = p_authority_id
          AND predecessor.source_node_type = 'quality_gate'
          AND EXISTS (
            SELECT 1
            FROM jsonb_array_elements(predecessor.member_document->'artifactRevisions') AS reference(value)
            WHERE value->>'revisionId' = v_authority.target_revision_id::text
              AND workflow_input_normalize_sha256(value->>'contentHash') =
                  v_authority.target_revision_content_hash
          )) <> 1 THEN
    RETURN false;
  END IF;

  SELECT COALESCE(jsonb_agg(member_document ORDER BY ordinal), '[]'::jsonb)
    INTO v_predecessors
  FROM workflow_input_authority_predecessors
  WHERE authority_id = p_authority_id;
  SELECT COALESCE(jsonb_agg(member_document ORDER BY ordinal), '[]'::jsonb)
    INTO v_manifests
  FROM workflow_input_authority_manifests
  WHERE authority_id = p_authority_id;
  SELECT COALESCE(jsonb_agg(member_document ORDER BY ordinal), '[]'::jsonb)
    INTO v_revisions
  FROM workflow_input_authority_revisions
  WHERE authority_id = p_authority_id;
  SELECT COALESCE(jsonb_agg(member_document ORDER BY ordinal), '[]'::jsonb)
    INTO v_receipts
  FROM workflow_input_authority_review_receipts
  WHERE authority_id = p_authority_id;

  IF (SELECT count(*) FROM workflow_input_authority_predecessors WHERE authority_id = p_authority_id) <> v_authority.predecessor_count
     OR (SELECT count(*) FROM workflow_input_authority_manifests WHERE authority_id = p_authority_id) <> v_authority.manifest_count
     OR (SELECT count(*) FROM workflow_input_authority_revisions WHERE authority_id = p_authority_id) <> v_authority.revision_count
     OR (SELECT count(*) FROM workflow_input_authority_review_receipts WHERE authority_id = p_authority_id) <> v_authority.review_receipt_count
     OR EXISTS (
       SELECT 1 FROM (
         SELECT ordinal, row_number() OVER (ORDER BY ordinal) - 1 AS expected
         FROM workflow_input_authority_predecessors WHERE authority_id = p_authority_id
       ) AS ordered WHERE ordinal <> expected
     ) OR EXISTS (
       SELECT 1 FROM (
         SELECT ordinal, row_number() OVER (ORDER BY ordinal) - 1 AS expected
         FROM workflow_input_authority_manifests WHERE authority_id = p_authority_id
       ) AS ordered WHERE ordinal <> expected
     ) OR EXISTS (
       SELECT 1 FROM (
         SELECT ordinal, row_number() OVER (ORDER BY ordinal) - 1 AS expected
         FROM workflow_input_authority_revisions WHERE authority_id = p_authority_id
       ) AS ordered WHERE ordinal <> expected
     ) OR EXISTS (
       SELECT 1 FROM (
         SELECT ordinal, row_number() OVER (ORDER BY ordinal) - 1 AS expected
         FROM workflow_input_authority_review_receipts WHERE authority_id = p_authority_id
       ) AS ordered WHERE ordinal <> expected
     ) THEN
    RETURN false;
  END IF;

  v_slice_identity := CASE WHEN v_authority.slice_kind = 'root'
    THEN jsonb_build_object('kind', 'root')
    ELSE jsonb_build_object('id', v_authority.slice_id::text, 'kind', 'slice') END;
  v_expected_request := jsonb_build_object(
    'authorityId', v_authority.authority_id::text,
    'expectedRunCursor', v_authority.activation_event_sequence - 1,
    'mediaType', 'application/vnd.worksflow.workflow-input-freeze-request+json;version=1',
    'nodeKey', v_authority.node_key,
    'nodeRunId', v_authority.node_run_id::text,
    'operationId', v_authority.operation_id::text,
    'projectId', v_authority.project_id::text,
    'schemaVersion', 'worksflow-workflow-input-freeze-request/v1',
    'workflowRunId', v_authority.workflow_run_id::text
  );
  v_expected_target := jsonb_build_object(
    'manifestSubject', v_authority.manifest_subject,
    'nodeKey', v_authority.node_key,
    'projectId', v_authority.project_id::text,
    'stageGate', v_authority.stage_gate,
    'targetRevisionContentHash', v_authority.target_revision_content_hash,
    'targetRevisionId', v_authority.target_revision_id::text,
    'workflowRunId', v_authority.workflow_run_id::text
  );
  v_expected_input := jsonb_build_object(
    'build', jsonb_build_object(
      'buildContract', jsonb_build_object(
        'contentHash', v_authority.build_contract_content_hash,
        'contractHash', v_authority.build_contract_hash,
        'id', v_authority.build_contract_id::text,
        'rawBytesHash', v_authority.build_contract_raw_bytes_hash,
        'rawBytesSize', v_authority.build_contract_raw_bytes_size,
        'statusAtFreeze', v_authority.build_contract_status
      ),
      'buildManifest', jsonb_build_object(
        'contentHash', v_authority.build_manifest_content_hash,
        'id', v_authority.build_manifest_id::text,
        'manifestHash', v_authority.build_manifest_hash,
        'rawBytesHash', v_authority.build_manifest_raw_bytes_hash,
        'rawBytesSize', v_authority.build_manifest_raw_bytes_size,
        'statusAtFreeze', v_authority.build_manifest_status
      )
    ),
    'definition', jsonb_build_object(
      'definitionHash', v_authority.definition_hash,
      'definitionId', v_authority.definition_id::text,
      'definitionVersion', v_authority.definition_version,
      'definitionVersionId', v_authority.definition_version_id::text,
      'executionProfileHash', v_authority.execution_profile_hash,
      'executionProfileVersion', v_authority.execution_profile_version,
      'rawBytesHash', v_authority.definition_raw_bytes_hash,
      'rawBytesSize', v_authority.definition_raw_bytes_size
    ),
    'gate', jsonb_build_object(
      'activationEventId', v_authority.activation_event_id::text,
      'activationEventSequence', v_authority.activation_event_sequence,
      'definitionNodeId', v_authority.definition_node_id,
      'gateName', v_authority.gate_name,
      'nodeKey', v_authority.node_key,
      'nodeRunId', v_authority.node_run_id::text,
      'nodeType', v_authority.node_type,
      'sliceIdentity', v_slice_identity,
      'stageGate', v_authority.stage_gate
    ),
    'inputManifests', v_manifests,
    'mediaType', 'application/vnd.worksflow.workflow-input+json;version=1',
    'nodeInput', jsonb_build_object(
      'bindingCount', v_authority.node_input_binding_count,
      'rawBytesHash', v_authority.node_input_raw_bytes_hash,
      'rawBytesSize', v_authority.node_input_raw_bytes_size,
      'semanticHash', v_authority.node_input_semantic_hash
    ),
    'predecessors', v_predecessors,
    'project', jsonb_build_object('governanceMode', v_authority.governance_mode, 'id', v_authority.project_id::text),
    'qualificationPolicy', jsonb_build_object(
      'authorityHash', v_authority.qualification_policy_authority_hash,
      'authorityId', v_authority.qualification_policy_authority_id::text,
      'externalGatePolicy', v_authority.external_gate_policy
    ),
    'qualityResult', jsonb_build_object(
      'buildManifestHash', v_authority.build_manifest_hash,
      'buildManifestId', v_authority.build_manifest_id::text,
      'passed', v_authority.quality_passed,
      'qualityRunId', v_authority.quality_run_id::text,
      'workspaceRevisionContentHash', v_authority.quality_workspace_revision_content_hash,
      'workspaceRevisionId', v_authority.quality_workspace_revision_id::text
    ),
    'reviewReceipts', v_receipts,
    'revisions', v_revisions,
    'run', jsonb_build_object(
      'id', v_authority.workflow_run_id::text,
      'inputManifestHash', v_authority.run_input_manifest_hash,
      'inputManifestId', v_authority.run_input_manifest_id::text,
      'scopeRawBytesHash', v_authority.run_scope_raw_bytes_hash,
      'scopeRawBytesSize', v_authority.run_scope_raw_bytes_size,
      'startedAt', to_char(v_authority.run_started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
      'startedBy', v_authority.run_started_by::text
    ),
    'schemaVersion', 'worksflow-workflow-input/v1',
    'target', v_expected_target,
    'targetHash', v_authority.target_hash
  );
  v_expected_envelope := jsonb_build_object(
    'authorityId', v_authority.authority_id::text,
    'inputHash', v_authority.input_hash,
    'mediaType', 'application/vnd.worksflow.workflow-input-authority+json;version=1',
    'nodeRunId', v_authority.node_run_id::text,
    'operationId', v_authority.operation_id::text,
    'projectId', v_authority.project_id::text,
    'requestHash', v_authority.request_hash,
    'schemaVersion', 'worksflow-workflow-input-authority/v1',
    'targetHash', v_authority.target_hash,
    'workflowRunId', v_authority.workflow_run_id::text
  );

  RETURN v_authority.request_document = v_expected_request
    AND v_authority.request_bytes = workflow_input_canonical_jsonb_bytes(v_expected_request)
    AND v_authority.request_hash = workflow_input_authority_hash(
      'worksflow.workflow-input.freeze-request/v1', v_authority.request_bytes
    )
    AND v_authority.target_document = v_expected_target
    AND v_authority.target_bytes = workflow_input_canonical_jsonb_bytes(v_expected_target)
    AND v_authority.target_hash = workflow_input_authority_hash(
      'worksflow.workflow-input.target/v1', v_authority.target_bytes
    )
    AND v_authority.input_document = v_expected_input
    AND v_authority.input_bytes = workflow_input_canonical_jsonb_bytes(v_expected_input)
    AND v_authority.input_hash = workflow_input_authority_hash(
      'worksflow.workflow-input.input/v1', v_authority.input_bytes
    )
    AND v_authority.authority_document = v_expected_envelope
    AND v_authority.authority_bytes = workflow_input_canonical_jsonb_bytes(v_expected_envelope)
    AND v_authority.authority_hash = workflow_input_authority_hash(
      'worksflow.workflow-input.authority/v1', v_authority.authority_bytes
    );
EXCEPTION WHEN OTHERS THEN
  RETURN false;
END;
$function$;

CREATE FUNCTION workflow_input_authority_replay_is_exact_v1(
  p_authority_id uuid,
  p_operation_id uuid,
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
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
AS $function$
DECLARE
  v_authority workflow_input_authorities%ROWTYPE;
  v_candidate jsonb;
BEGIN
  SELECT * INTO v_authority
  FROM workflow_input_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND OR workflow_input_authority_record_is_exact(p_authority_id) IS NOT TRUE THEN
    RETURN false;
  END IF;
  SELECT jsonb_build_object(
    'inputManifests', COALESCE((
      SELECT jsonb_agg(jsonb_build_object(
        'manifestId', member.manifest_id::text,
        'rawBytesHex', encode(member.raw_bytes, 'hex'),
        'role', member.role
      ) ORDER BY member.ordinal)
      FROM workflow_input_authority_manifests AS member
      WHERE member.authority_id = v_authority.authority_id
    ), '[]'::jsonb),
    'manifestSubject', v_authority.manifest_subject,
    'qualificationPolicy', jsonb_build_object(
      'authorityHash', v_authority.qualification_policy_authority_hash,
      'authorityId', v_authority.qualification_policy_authority_id::text,
      'externalGatePolicy', v_authority.external_gate_policy
    ),
    'qualityResult', jsonb_build_object(
      'buildContractHash', v_authority.build_contract_hash,
      'buildContractId', v_authority.build_contract_id::text,
      'buildManifestHash', v_authority.build_manifest_hash,
      'buildManifestId', v_authority.build_manifest_id::text,
      'passed', v_authority.quality_passed,
      'qualityRunId', v_authority.quality_run_id::text,
      'workspaceRevisionContentHash', v_authority.quality_workspace_revision_content_hash,
      'workspaceRevisionId', v_authority.quality_workspace_revision_id::text
    ),
    'reviewRequirements', COALESCE((
      SELECT jsonb_agg(jsonb_build_object(
        'purpose', member.purpose,
        'revisionId', member.revision_id::text
      ) ORDER BY member.ordinal)
      FROM workflow_input_authority_review_receipts AS member
      WHERE member.authority_id = v_authority.authority_id
    ), '[]'::jsonb),
    'revisions', COALESCE((
      SELECT jsonb_agg(jsonb_build_object(
        'canonicalReviewRequired', member.canonical_review_required,
        'currencyPolicy', member.currency_policy,
        'purpose', member.purpose,
        'rawBytesHex', encode(member.raw_bytes, 'hex'),
        'revisionId', member.revision_id::text
      ) ORDER BY convert_to(member.purpose, 'UTF8'), member.revision_id)
      FROM workflow_input_authority_revisions AS member
      WHERE member.authority_id = v_authority.authority_id
    ), '[]'::jsonb)
  ) INTO v_candidate;
  RETURN v_authority.operation_id = p_operation_id
    AND v_authority.workflow_run_id = p_workflow_run_id
    AND v_authority.node_run_id = p_node_run_id
    AND v_authority.activation_event_sequence - 1 = p_expected_run_cursor
    AND v_authority.activation_event_id = p_activation_event_id
    AND v_authority.activation_event_sequence = p_activation_event_sequence
    AND v_authority.definition_raw_bytes = p_definition_raw_bytes
    AND v_authority.run_scope_raw_bytes = p_run_scope_raw_bytes
    AND v_authority.node_input_raw_bytes = p_node_input_raw_bytes
    AND v_authority.build_manifest_raw_bytes = p_build_manifest_raw_bytes
    AND v_authority.build_contract_raw_bytes = p_build_contract_raw_bytes
    AND v_candidate = p_candidate;
EXCEPTION WHEN OTHERS THEN
  RETURN false;
END;
$function$;

CREATE FUNCTION guard_workflow_input_authority_event_identity_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_reservation workflow_input_authority_identity_reservations%ROWTYPE;
  v_authority workflow_input_authorities%ROWTYPE;
  v_bound_old boolean := false;
BEGIN
  -- UPDATE and DELETE must resolve the identity from OLD first. Looking only
  -- at NEW lets a writer move or delete a bound activation event, then
  -- reinsert the old ID with different actor/time while deferred foreign keys
  -- are temporarily open inside the transaction.
  IF TG_OP = 'INSERT' THEN
    SELECT * INTO v_reservation
    FROM workflow_input_authority_identity_reservations
    WHERE identity_value = NEW.id;
  ELSE
    SELECT * INTO v_reservation
    FROM workflow_input_authority_identity_reservations
    WHERE identity_value = OLD.id;
    v_bound_old := FOUND;
    IF TG_OP = 'UPDATE' AND NOT v_bound_old THEN
      SELECT * INTO v_reservation
      FROM workflow_input_authority_identity_reservations
      WHERE identity_value = NEW.id;
    END IF;
  END IF;
  IF NOT FOUND THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
  END IF;
  IF v_reservation.identity_kind <> 'activation-event' THEN
    RAISE EXCEPTION 'Workflow Input authority or operation identity cannot be reused as an event'
      USING ERRCODE = 'WIA01';
  END IF;
  IF TG_OP = 'DELETE' OR (
       TG_OP = 'UPDATE' AND v_bound_old AND NEW.id IS DISTINCT FROM OLD.id
     ) THEN
    RAISE EXCEPTION 'Workflow Input activation event identity is immutable'
      USING ERRCODE = 'WIA03';
  END IF;
  IF TG_OP = 'UPDATE' AND (
       NEW.actor_id IS DISTINCT FROM OLD.actor_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
     ) THEN
    RAISE EXCEPTION 'Workflow Input activation event actor and occurrence time are immutable'
      USING ERRCODE = 'WIA03';
  END IF;
  SELECT * INTO v_authority
  FROM workflow_input_authorities
  WHERE authority_id = v_reservation.authority_id;
  IF v_authority.authority_id IS NULL
     OR NEW.id IS DISTINCT FROM v_authority.activation_event_id
     OR NEW.run_id IS DISTINCT FROM v_authority.workflow_run_id
     OR NEW.sequence IS DISTINCT FROM v_authority.activation_event_sequence
     OR NEW.event_type IS DISTINCT FROM 'external_qualification_activated'
     OR NEW.node_key IS DISTINCT FROM v_authority.node_key
     OR NEW.payload IS DISTINCT FROM jsonb_build_object(
       'inputAuthorityId', v_authority.authority_id::text,
       'nodeRunId', v_authority.node_run_id::text
     ) THEN
    RAISE EXCEPTION 'Workflow Input activation event identity has one exact closed payload'
      USING ERRCODE = 'WIA03';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER workflow_input_authority_event_identity_guard
BEFORE INSERT OR UPDATE OR DELETE ON workflow_run_events
FOR EACH ROW EXECUTE FUNCTION guard_workflow_input_authority_event_identity_v1();

CREATE FUNCTION validate_workflow_input_authority_closure_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authority_id uuid;
  v_authority workflow_input_authorities%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_event workflow_run_events%ROWTYPE;
BEGIN
  IF TG_TABLE_NAME = 'workflow_input_authorities' THEN
    v_authority_id := COALESCE(NEW.authority_id, OLD.authority_id);
  ELSIF TG_TABLE_NAME IN (
    'workflow_input_authority_predecessors', 'workflow_input_authority_manifests',
    'workflow_input_authority_revisions', 'workflow_input_authority_review_receipts'
  ) THEN
    v_authority_id := COALESCE(NEW.authority_id, OLD.authority_id);
  ELSIF TG_TABLE_NAME = 'workflow_node_runs' THEN
    v_authority_id := COALESCE(NEW.input_authority_id, OLD.input_authority_id);
    IF v_authority_id IS NULL THEN
      IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
    END IF;
  ELSIF TG_TABLE_NAME = 'workflow_run_events' THEN
    IF TG_OP = 'UPDATE' THEN
      -- Inspect both tuple identities explicitly. OLD is authoritative for a
      -- previously bound event; NEW also covers an unbound row being moved
      -- into a reserved activation identity.
      SELECT authority_id INTO v_authority_id
      FROM workflow_input_authorities
      WHERE activation_event_id = OLD.id;
      IF v_authority_id IS NULL THEN
        SELECT authority_id INTO v_authority_id
        FROM workflow_input_authorities
        WHERE activation_event_id = NEW.id;
      END IF;
    ELSE
      SELECT authority_id INTO v_authority_id
      FROM workflow_input_authorities
      WHERE activation_event_id = COALESCE(NEW.id, OLD.id);
    END IF;
    IF v_authority_id IS NULL THEN
      IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
    END IF;
  END IF;

  SELECT * INTO v_authority FROM workflow_input_authorities WHERE authority_id = v_authority_id;
  IF NOT FOUND THEN
    IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
  END IF;
  SELECT * INTO v_run FROM workflow_runs WHERE id = v_authority.workflow_run_id;
  SELECT * INTO v_node FROM workflow_node_runs WHERE id = v_authority.node_run_id;
  SELECT * INTO v_event FROM workflow_run_events WHERE id = v_authority.activation_event_id;
  IF TG_TABLE_NAME = 'workflow_input_authorities' AND TG_OP = 'INSERT'
     AND (v_run.status IS DISTINCT FROM 'waiting_qualification'
       OR v_node.status IS DISTINCT FROM 'waiting_qualification') THEN
    RAISE EXCEPTION 'Workflow Input Authority issuance must commit in the exact waiting_qualification state'
      USING ERRCODE = 'WIA03';
  END IF;
  IF TG_TABLE_NAME = 'workflow_input_authorities' AND TG_OP = 'INSERT'
     AND v_event.id IS NOT NULL
     AND NOT EXISTS (
       SELECT 1
       FROM outbox_events AS outbox
       WHERE outbox.id = v_event.id
         AND outbox.aggregate_type = 'workflow_run'
         AND outbox.aggregate_id = v_run.id::text
         AND outbox.event_type = 'external_qualification_activated'
         AND outbox.subject = 'worksflow.workflow.run.event'
         AND outbox.headers = '{}'::jsonb
         AND outbox.attempts = 0
         AND outbox.published_at IS NULL
         AND outbox.last_error IS NULL
         AND outbox.available_at = v_event.created_at
         AND outbox.created_at = v_event.created_at
         AND jsonb_typeof(outbox.payload) = 'object'
         AND outbox.payload ?& ARRAY[
           'id','projectId','runId','sequence','type','occurredAt','payload','nodeKey'
         ]
         AND outbox.payload - CASE WHEN v_event.actor_id IS NULL THEN ARRAY[
           'id','projectId','runId','sequence','type','occurredAt','payload','nodeKey'
         ] ELSE ARRAY[
           'id','projectId','runId','sequence','type','occurredAt','payload','nodeKey','actorId'
         ] END = '{}'::jsonb
         AND outbox.payload->>'id' = v_event.id::text
         AND outbox.payload->>'projectId' = v_authority.project_id::text
         AND outbox.payload->>'runId' = v_event.run_id::text
         AND outbox.payload->'sequence' = to_jsonb(v_event.sequence)
         AND outbox.payload->>'type' = v_event.event_type
         AND outbox.payload->>'nodeKey' = v_event.node_key
         AND outbox.payload->'payload' = v_event.payload
         AND outbox.payload->>'occurredAt' ~
           '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$'
         AND (outbox.payload->>'occurredAt')::timestamptz = v_event.created_at
         AND (
           (v_event.actor_id IS NULL AND NOT (outbox.payload ? 'actorId'))
           OR outbox.payload->>'actorId' = v_event.actor_id::text
         )
     ) THEN
    RAISE EXCEPTION 'Workflow Input Authority issuance must enqueue its exact activation outbox event'
      USING ERRCODE = 'WIA03';
  END IF;
  IF v_run.id IS NULL
     OR v_run.project_id IS DISTINCT FROM v_authority.project_id
     OR v_run.event_cursor < v_authority.activation_event_sequence
     OR v_node.id IS NULL
     OR v_event.id IS NULL
     OR v_node.input_authority_id IS DISTINCT FROM v_authority.authority_id
     OR v_node.run_id IS DISTINCT FROM v_authority.workflow_run_id
     OR v_node.node_key IS DISTINCT FROM v_authority.node_key
     OR v_node.node_type IS DISTINCT FROM v_authority.node_type
     OR v_node.definition_node_id IS DISTINCT FROM v_authority.definition_node_id
     OR v_node.slice_kind IS DISTINCT FROM v_authority.slice_kind
     OR v_node.slice_id IS DISTINCT FROM v_authority.slice_id
     OR v_event.run_id IS DISTINCT FROM v_authority.workflow_run_id
     OR v_event.sequence IS DISTINCT FROM v_authority.activation_event_sequence
     OR v_event.event_type IS DISTINCT FROM 'external_qualification_activated'
     OR v_event.node_key IS DISTINCT FROM v_authority.node_key
     OR v_event.payload IS DISTINCT FROM jsonb_build_object(
       'inputAuthorityId', v_authority.authority_id::text,
       'nodeRunId', v_authority.node_run_id::text
     )
     OR workflow_input_authority_record_is_exact(v_authority.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input Authority, node attachment, activation event, or child closure is not exact'
      USING ERRCODE = 'WIA03';
  END IF;
  IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$function$;

CREATE CONSTRAINT TRIGGER workflow_input_authorities_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_input_authorities
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_input_authority_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_input_authority_predecessors_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_input_authority_predecessors
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_input_authority_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_input_authority_manifests_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_input_authority_manifests
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_input_authority_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_input_authority_revisions_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_input_authority_revisions
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_input_authority_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_input_authority_review_receipts_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_input_authority_review_receipts
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_input_authority_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_node_input_authority_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_node_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_input_authority_closure_v1();
CREATE CONSTRAINT TRIGGER workflow_input_authority_event_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_run_events
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_workflow_input_authority_closure_v1();

CREATE FUNCTION freeze_workflow_input_authority_v1(
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
SECURITY DEFINER
AS $function$
DECLARE
  v_existing workflow_input_authorities%ROWTYPE;
  v_project projects%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_node workflow_node_runs%ROWTYPE;
  v_source_node workflow_node_runs%ROWTYPE;
  v_definition_version workflow_definition_versions%ROWTYPE;
  v_definition workflow_definitions%ROWTYPE;
  v_manifest input_manifests%ROWTYPE;
  v_quality_run quality_runs%ROWTYPE;
  v_implementation_proposal implementation_proposals%ROWTYPE;
  v_build_manifest application_build_manifests%ROWTYPE;
  v_build_contract application_build_contracts%ROWTYPE;
  v_policy_authority qualification_policy_authorities%ROWTYPE;
  v_artifact artifacts%ROWTYPE;
  v_revision artifact_revisions%ROWTYPE;
  v_source_output_revision artifact_revisions%ROWTYPE;
  v_receipt canonical_review_approval_receipts%ROWTYPE;
  v_resolved_receipt canonical_review_approval_receipts%ROWTYPE;
  v_definition_document jsonb;
  v_definition_edge jsonb;
  v_build_manifest_document jsonb;
  v_build_contract_document jsonb;
  v_run_scope_document jsonb;
  v_node_input_document jsonb;
  v_quality jsonb;
  v_quality_output jsonb;
  v_quality_findings jsonb;
  v_quality_build_manifest jsonb;
  v_quality_created_at timestamptz;
  v_policy jsonb;
  v_item jsonb;
  v_binding jsonb;
  v_member jsonb;
  v_artifact_revisions jsonb;
  v_materialized_revisions jsonb;
  v_delivery_slice_refs jsonb;
  v_proposal_pins jsonb;
  v_input_manifest_ref jsonb;
  v_output_proposal_ref jsonb;
  v_predecessors jsonb := '[]'::jsonb;
  v_manifests jsonb := '[]'::jsonb;
  v_revisions jsonb := '[]'::jsonb;
  v_receipts jsonb := '[]'::jsonb;
  v_request_document jsonb;
  v_target_document jsonb;
  v_input_document jsonb;
  v_authority_document jsonb;
  v_request_bytes bytea;
  v_target_bytes bytea;
  v_input_bytes bytea;
  v_authority_bytes bytea;
  v_request_hash text;
  v_target_hash text;
  v_input_hash text;
  v_authority_hash text;
  v_project_id uuid;
  v_quality_run_id uuid;
  v_target_revision_id uuid;
  v_build_manifest_id uuid;
  v_build_contract_id uuid;
  v_policy_authority_id uuid;
  v_manifest_id uuid;
  v_revision_id uuid;
  v_review_revision_id uuid;
  v_raw bytea;
  v_raw_hash text;
  v_currency_policy text;
  v_change_source text;
  v_canonical_review_required boolean;
  v_source_required boolean;
  v_now timestamptz;
  v_ordinal integer;
  v_count integer;
  v_predecessor_count integer := 0;
  v_manifest_count integer := 0;
  v_revision_count integer := 0;
  v_receipt_count integer := 0;
  v_retained_bytes bigint := 0;
  v_quality_predecessors integer := 0;
  v_previous_order bytea;
  v_order bytea;
  v_slice_identity jsonb;
BEGIN
  -- Join the shared runtime side of the rolling migration fence before this
  -- function touches any relation. Migration 000079 and authority down
  -- migrations take the exclusive side first, while unrelated project
  -- activations remain concurrent.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  IF p_operation_id IS NULL OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE
     OR p_authority_id IS NULL OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE
     OR p_workflow_run_id IS NULL OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_node_run_id IS NULL OR workflow_input_uuid_is_exact(p_node_run_id::text) IS NOT TRUE
     OR p_activation_event_id IS NULL OR workflow_input_uuid_is_exact(p_activation_event_id::text) IS NOT TRUE
     OR p_expected_run_cursor IS NULL OR p_expected_run_cursor NOT BETWEEN 0 AND 9007199254740990
     OR p_activation_event_sequence IS NULL
     OR p_activation_event_sequence <> p_expected_run_cursor + 1
     OR p_authority_id = p_operation_id OR p_authority_id = p_activation_event_id
     OR p_operation_id = p_activation_event_id
     OR p_authority_id IN (p_workflow_run_id, p_node_run_id)
     OR p_operation_id IN (p_workflow_run_id, p_node_run_id) THEN
    RAISE EXCEPTION 'Workflow Input freeze identity or cursor is invalid'
      USING ERRCODE = 'WIA03';
  END IF;

  -- Exact replay inspection uses only retained PostgreSQL bytes, so it remains
  -- available after external content retirement without accepting a changed
  -- candidate under the same operation or node generation.
  SELECT count(*) INTO v_count
  FROM workflow_input_authorities
  WHERE operation_id = p_operation_id
     OR (workflow_run_id = p_workflow_run_id AND node_run_id = p_node_run_id);
  IF v_count > 1 THEN
    RAISE EXCEPTION 'Workflow Input operation and node identities belong to different authorities'
      USING ERRCODE = 'WIA01';
  ELSIF v_count = 1 THEN
    SELECT * INTO v_existing
    FROM workflow_input_authorities
    WHERE operation_id = p_operation_id
       OR (workflow_run_id = p_workflow_run_id AND node_run_id = p_node_run_id);
    IF v_existing.authority_id <> p_authority_id
       OR workflow_input_authority_replay_is_exact_v1(
         v_existing.authority_id, p_operation_id, p_workflow_run_id, p_node_run_id,
         p_expected_run_cursor, p_activation_event_id, p_activation_event_sequence,
         p_definition_raw_bytes, p_run_scope_raw_bytes, p_node_input_raw_bytes,
         p_build_manifest_raw_bytes, p_build_contract_raw_bytes, p_candidate
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Workflow Input operation or node is bound to different or corrupt authority bytes'
        USING ERRCODE = 'WIA01';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  SELECT project_id INTO v_project_id FROM workflow_runs WHERE id = p_workflow_run_id;
  IF v_project_id IS NULL THEN
    RAISE EXCEPTION 'Workflow Input run does not exist' USING ERRCODE = 'WIA04';
  END IF;
  SELECT * INTO v_project FROM projects WHERE id = v_project_id FOR UPDATE;
  IF NOT FOUND OR v_project.lifecycle <> 'active' THEN
    RAISE EXCEPTION 'Workflow Input project is absent or archived' USING ERRCODE = 'WIA04';
  END IF;
  -- Delivery and Impact writers use this same project-scoped advisory mutex
  -- before changing delivery_slices. Holding it through COMMIT makes every
  -- propagated slice pointer one snapshot rather than a mixed old/new read.
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended('delivery_slices:' || v_project.id::text, 0)
  );

  -- The project mutex serializes every same-project freeze. Recheck both
  -- idempotency keys after the lock to converge concurrent exact attempts.
  SELECT count(*) INTO v_count
  FROM workflow_input_authorities
  WHERE operation_id = p_operation_id
     OR (workflow_run_id = p_workflow_run_id AND node_run_id = p_node_run_id);
  IF v_count > 0 THEN
    SELECT * INTO v_existing
    FROM workflow_input_authorities
    WHERE operation_id = p_operation_id
       OR (workflow_run_id = p_workflow_run_id AND node_run_id = p_node_run_id)
    LIMIT 1;
    IF v_count <> 1 OR v_existing.authority_id <> p_authority_id
       OR workflow_input_authority_replay_is_exact_v1(
         v_existing.authority_id, p_operation_id, p_workflow_run_id, p_node_run_id,
         p_expected_run_cursor, p_activation_event_id, p_activation_event_sequence,
         p_definition_raw_bytes, p_run_scope_raw_bytes, p_node_input_raw_bytes,
         p_build_manifest_raw_bytes, p_build_contract_raw_bytes, p_candidate
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'Concurrent Workflow Input freeze conflicts with an existing generation'
        USING ERRCODE = 'WIA01';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  SELECT * INTO v_run FROM workflow_runs WHERE id = p_workflow_run_id FOR UPDATE;
  SELECT * INTO v_node FROM workflow_node_runs WHERE id = p_node_run_id FOR UPDATE;
  IF v_run.id IS NULL OR v_run.project_id <> v_project.id
     OR v_run.event_cursor <> p_expected_run_cursor
     OR v_run.status NOT IN ('running', 'waiting_input', 'waiting_review')
     OR v_run.started_at IS NULL
     OR v_run.started_at <> date_trunc('milliseconds', v_run.started_at)
     OR v_run.input_manifest_id IS NULL
     OR v_run.execution_profile_version <> 'workflow-engine/v3'
     OR v_run.execution_profile_hash <> '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR v_node.id IS NULL OR v_node.run_id <> v_run.id
     OR v_node.node_type <> 'external_qualification_gate'
     OR v_node.status <> 'pending'
     OR v_node.definition_node_id <> 'external-qualification'
     OR v_node.node_key <> 'external-qualification'
     OR v_node.slice_kind <> 'root' OR v_node.slice_id IS NOT NULL
     OR v_node.input_manifest_id IS NOT NULL
     OR v_node.input_authority_id IS NOT NULL THEN
    RAISE EXCEPTION 'Workflow Input run/node/profile/cursor is not an activatable v3 gate'
      USING ERRCODE = 'WIA04';
  END IF;

  SELECT * INTO v_definition_version
  FROM workflow_definition_versions WHERE id = v_run.definition_version_id FOR SHARE;
  SELECT definition.* INTO v_definition
  FROM workflow_definitions AS definition
  WHERE definition.id = v_definition_version.definition_id FOR SHARE;
  IF v_definition_version.id IS NULL OR v_definition.id IS NULL
     OR v_definition.project_id IS DISTINCT FROM v_project.id
     OR v_definition.lifecycle <> 'active'
     OR v_definition_version.published IS NOT TRUE
     OR v_definition_version.execution_profile_version <> v_run.execution_profile_version
     OR v_definition_version.execution_profile_hash <> v_run.execution_profile_hash
     OR v_definition_version.execution_profile_hash <> '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104' THEN
    RAISE EXCEPTION 'Workflow Input definition/profile identity is not exact and published'
      USING ERRCODE = 'WIA04';
  END IF;

  IF p_authority_id IN (
       v_project.id, v_definition.id, v_definition_version.id, v_run.input_manifest_id,
       v_target_revision_id, v_build_manifest_id, v_build_contract_id, v_policy_authority_id
     )
     OR p_operation_id IN (
       v_project.id, v_definition.id, v_definition_version.id, v_run.input_manifest_id,
       v_target_revision_id, v_build_manifest_id, v_build_contract_id, v_policy_authority_id
     )
     OR p_activation_event_id IN (
       v_project.id, v_definition.id, v_definition_version.id, v_run.id, v_node.id,
       v_run.input_manifest_id, v_target_revision_id, v_build_manifest_id,
       v_build_contract_id, v_policy_authority_id
     )
     OR EXISTS (
       SELECT 1 FROM workflow_run_events
       WHERE id IN (p_authority_id, p_operation_id, p_activation_event_id)
     ) THEN
    RAISE EXCEPTION 'Workflow Input locally allocated identity is reused by another authority role'
      USING ERRCODE = 'WIA01';
  END IF;

  IF p_candidate IS NULL OR jsonb_typeof(p_candidate) <> 'object'
     OR NOT (p_candidate ?& ARRAY[
       'inputManifests','manifestSubject','qualificationPolicy','qualityResult',
       'reviewRequirements','revisions'
     ])
     OR p_candidate - ARRAY[
       'inputManifests','manifestSubject','qualificationPolicy','qualityResult',
       'reviewRequirements','revisions'
     ] <> '{}'::jsonb
     OR jsonb_typeof(p_candidate->'inputManifests') <> 'array'
     OR jsonb_typeof(p_candidate->'revisions') <> 'array'
     OR jsonb_typeof(p_candidate->'reviewRequirements') <> 'array'
     OR jsonb_typeof(p_candidate->'manifestSubject') <> 'string'
     OR octet_length(p_candidate->>'manifestSubject') NOT BETWEEN 1 AND 256
     OR p_candidate->>'manifestSubject' <> btrim(p_candidate->>'manifestSubject')
     OR p_candidate->>'manifestSubject' ~ '[[:cntrl:]]'
     OR octet_length(p_candidate::text) > 67108864 THEN
    RAISE EXCEPTION 'Workflow Input private candidate root is invalid or widened'
      USING ERRCODE = 'WIA03';
  END IF;

  v_quality := p_candidate->'qualityResult';
  v_policy := p_candidate->'qualificationPolicy';
  IF jsonb_typeof(v_quality) <> 'object'
     OR NOT (v_quality ?& ARRAY[
       'buildContractHash','buildContractId','buildManifestHash','buildManifestId',
       'passed','qualityRunId','workspaceRevisionContentHash','workspaceRevisionId'
     ])
     OR v_quality - ARRAY[
       'buildContractHash','buildContractId','buildManifestHash','buildManifestId',
       'passed','qualityRunId','workspaceRevisionContentHash','workspaceRevisionId'
     ] <> '{}'::jsonb
     OR v_quality->'passed' <> 'true'::jsonb
     OR jsonb_typeof(v_policy) <> 'object'
     OR NOT (v_policy ?& ARRAY['authorityHash','authorityId','externalGatePolicy'])
     OR v_policy - ARRAY['authorityHash','authorityId','externalGatePolicy'] <> '{}'::jsonb
     OR v_policy->>'externalGatePolicy' <> 'external-qualification/v1'
     OR v_quality->>'buildContractHash' !~ '^sha256:[0-9a-f]{64}$'
     OR v_quality->>'buildManifestHash' !~ '^sha256:[0-9a-f]{64}$'
     OR v_quality->>'workspaceRevisionContentHash' !~ '^sha256:[0-9a-f]{64}$'
     OR v_policy->>'authorityHash' !~ '^sha256:[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'Workflow Input quality result or qualification policy is invalid'
      USING ERRCODE = 'WIA03';
  END IF;
  BEGIN
    v_quality_run_id := (v_quality->>'qualityRunId')::uuid;
    v_target_revision_id := (v_quality->>'workspaceRevisionId')::uuid;
    v_build_manifest_id := (v_quality->>'buildManifestId')::uuid;
    v_build_contract_id := (v_quality->>'buildContractId')::uuid;
    v_policy_authority_id := (v_policy->>'authorityId')::uuid;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Workflow Input candidate UUID is malformed' USING ERRCODE = 'WIA03';
  END;
  IF workflow_input_uuid_is_exact(v_quality->>'qualityRunId') IS NOT TRUE
     OR workflow_input_uuid_is_exact(v_quality->>'workspaceRevisionId') IS NOT TRUE
     OR workflow_input_uuid_is_exact(v_quality->>'buildManifestId') IS NOT TRUE
     OR workflow_input_uuid_is_exact(v_quality->>'buildContractId') IS NOT TRUE
     OR workflow_input_uuid_is_exact(v_policy->>'authorityId') IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input candidate UUID is non-canonical' USING ERRCODE = 'WIA03';
  END IF;
  IF p_authority_id IN (
       v_quality_run_id, v_target_revision_id, v_build_manifest_id,
       v_build_contract_id, v_policy_authority_id
     )
     OR p_operation_id IN (
       v_quality_run_id, v_target_revision_id, v_build_manifest_id,
       v_build_contract_id, v_policy_authority_id
     )
     OR p_activation_event_id IN (
       v_quality_run_id, v_target_revision_id, v_build_manifest_id,
       v_build_contract_id, v_policy_authority_id
     ) THEN
    RAISE EXCEPTION 'Workflow Input candidate identity collides with a locally allocated authority identity'
      USING ERRCODE = 'WIA01';
  END IF;

  -- The candidate repeats an expected policy identity, but only the largest
  -- committed generation for this locked project/Profile is authoritative.
  -- Policy issuance takes the same project row lock, so this head cannot move
  -- until the Freeze transaction commits or rolls back.
  SELECT * INTO v_policy_authority
  FROM qualification_policy_authorities
  WHERE project_id = v_project.id
    AND execution_profile_version = v_run.execution_profile_version
    AND execution_profile_hash = workflow_input_normalize_sha256(v_run.execution_profile_hash)
  ORDER BY generation DESC
  LIMIT 1
  FOR UPDATE;
  IF v_policy_authority.authority_id IS NULL
     OR v_policy_authority.status <> 'active'
     OR v_policy_authority.authority_id <> v_policy_authority_id
     OR v_policy_authority.authority_hash <> v_policy->>'authorityHash'
     OR v_policy_authority.external_gate_policy <> v_policy->>'externalGatePolicy'
     OR qualification_policy_authority_record_is_exact_v1(
       v_policy_authority.authority_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input qualification policy is absent, stale, suspended, or not exact'
      USING ERRCODE = 'WIA04';
  END IF;

  IF p_definition_raw_bytes IS NULL OR octet_length(p_definition_raw_bytes) NOT BETWEEN 1 AND 8388608
     OR p_run_scope_raw_bytes IS NULL OR octet_length(p_run_scope_raw_bytes) NOT BETWEEN 1 AND 8388608
     OR p_node_input_raw_bytes IS NULL OR octet_length(p_node_input_raw_bytes) NOT BETWEEN 1 AND 16777216
     OR p_build_manifest_raw_bytes IS NULL OR octet_length(p_build_manifest_raw_bytes) NOT BETWEEN 1 AND 8388608
     OR p_build_contract_raw_bytes IS NULL OR octet_length(p_build_contract_raw_bytes) NOT BETWEEN 1 AND 8388608 THEN
    RAISE EXCEPTION 'Workflow Input retained raw bytes are absent or oversized'
      USING ERRCODE = 'WIA03';
  END IF;
  v_retained_bytes := octet_length(p_definition_raw_bytes)
    + octet_length(p_run_scope_raw_bytes)
    + octet_length(p_node_input_raw_bytes)
    + octet_length(p_build_manifest_raw_bytes)
    + octet_length(p_build_contract_raw_bytes);
  IF v_retained_bytes > 134217728 THEN
    RAISE EXCEPTION 'Workflow Input aggregate retained bytes exceed the v1 bound'
      USING ERRCODE = 'WIA03';
  END IF;
  BEGIN
    v_definition_document := convert_from(p_definition_raw_bytes, 'UTF8')::jsonb;
    v_run_scope_document := convert_from(p_run_scope_raw_bytes, 'UTF8')::jsonb;
    v_node_input_document := convert_from(p_node_input_raw_bytes, 'UTF8')::jsonb;
    v_build_manifest_document := convert_from(p_build_manifest_raw_bytes, 'UTF8')::jsonb;
    v_build_contract_document := convert_from(p_build_contract_raw_bytes, 'UTF8')::jsonb;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Workflow Input retained Definition/run/NodeInput/BuildManifest/BuildContract bytes are invalid UTF-8/JSON'
      USING ERRCODE = 'WIA03';
  END;
  -- Established NodeInput hashes are intentionally not recomputed from
  -- jsonb here. domain.CanonicalJSON preserves JSON-number lexemes and uses
  -- encoding/json string escaping, while PostgreSQL jsonb normalizes both;
  -- recomputation would reject valid production envelopes. The only runtime
  -- entrypoint compiles before this call and validates the returned recovery
  -- bundle again inside this caller-owned transaction before COMMIT. SQL
  -- independently binds every typed relational fact below.
  IF v_definition_document IS DISTINCT FROM v_definition_version.content
     OR v_run_scope_document IS DISTINCT FROM v_run.scope
     OR v_node_input_document IS DISTINCT FROM v_run.context#>ARRAY['nodes',v_node.node_key,'input']
     OR jsonb_typeof(v_node_input_document) <> 'object'
     OR v_node_input_document - ARRAY['bindings','hash'] <> '{}'::jsonb
     OR NOT (v_node_input_document ?& ARRAY['bindings','hash'])
     OR jsonb_typeof(v_node_input_document->'bindings') <> 'array'
     OR jsonb_array_length(v_node_input_document->'bindings') NOT BETWEEN 1 AND 1024
     OR jsonb_typeof(v_node_input_document->'hash') <> 'string'
     OR workflow_input_normalize_sha256(v_node_input_document->>'hash') !~ '^sha256:[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'Workflow Input Definition, run scope, or NodeInput exact bytes disagree with locked state'
      USING ERRCODE = 'WIA04';
  END IF;
  IF jsonb_typeof(v_definition_document->'edges') IS DISTINCT FROM 'array'
     OR (
       SELECT count(*)
       FROM jsonb_array_elements(v_definition_document->'edges') AS edge(value)
       WHERE jsonb_typeof(edge.value) = 'object'
         AND edge.value->>'to' = v_node.definition_node_id
     ) <> jsonb_array_length(v_node_input_document->'bindings') THEN
    RAISE EXCEPTION 'Workflow Input NodeInput does not equal the external gate incoming Definition edge set'
      USING ERRCODE = 'WIA04';
  END IF;

  PERFORM 1
  FROM delivery_slices
  WHERE id IN (
    SELECT (slice_ref.value->>'id')::uuid
    FROM jsonb_array_elements(v_node_input_document->'bindings') AS binding(value)
    CROSS JOIN LATERAL jsonb_array_elements(
      CASE WHEN jsonb_typeof(binding.value#>'{source,deliverySliceRefs}') = 'array'
        THEN binding.value#>'{source,deliverySliceRefs}' ELSE '[]'::jsonb END
    ) AS slice_ref(value)
    WHERE slice_ref.value->>'id' ~ '^[0-9a-f-]{36}$'
  )
  ORDER BY id
  FOR SHARE;

  -- Follow the platform-wide artifact-before-revision lock order. Mapping
  -- revision IDs to artifact IDs is safe because revision ownership is
  -- immutable; every mapping is rechecked after both ordered lock sets.
  PERFORM 1 FROM artifacts
  WHERE id IN (
    SELECT revision.artifact_id
    FROM artifact_revisions AS revision
    WHERE revision.id = v_target_revision_id
       OR revision.id IN (
         SELECT (item->>'revisionId')::uuid
         FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
         WHERE jsonb_typeof(item) = 'object'
           AND item->>'revisionId' ~ '^[0-9a-f-]{36}$'
       )
  )
  ORDER BY id FOR UPDATE;
  PERFORM 1 FROM artifact_revisions
  WHERE id = v_target_revision_id
     OR id IN (
       SELECT (item->>'revisionId')::uuid
       FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
       WHERE jsonb_typeof(item) = 'object'
         AND item->>'revisionId' ~ '^[0-9a-f-]{36}$'
     )
  ORDER BY id FOR UPDATE;
  SELECT * INTO v_revision
  FROM artifact_revisions WHERE id = v_target_revision_id;
  SELECT * INTO v_artifact
  FROM artifacts WHERE id = v_revision.artifact_id;
  IF v_revision.id IS NULL OR v_artifact.id IS NULL
     OR v_artifact.project_id <> v_project.id
     OR v_artifact.kind <> 'workspace'
     OR v_artifact.lifecycle <> 'active'
     OR v_revision.workflow_status <> 'approved'
     OR v_revision.superseded_at IS NOT NULL
     OR v_artifact.latest_revision_id IS DISTINCT FROM v_revision.id
     OR v_artifact.latest_approved_revision_id IS DISTINCT FROM v_revision.id
     OR workflow_input_normalize_sha256(v_revision.content_hash) <> v_quality->>'workspaceRevisionContentHash' THEN
    RAISE EXCEPTION 'Workflow Input quality target is not the exact current approved Workspace revision'
      USING ERRCODE = 'WIA04';
  END IF;

  SELECT * INTO v_implementation_proposal
  FROM implementation_proposals
  WHERE id = v_revision.implementation_proposal_id
  FOR SHARE;
  IF v_implementation_proposal.id IS NULL
     OR v_implementation_proposal.project_id <> v_project.id
     OR v_implementation_proposal.status NOT IN ('applied','partially_applied')
     OR v_implementation_proposal.applied_at IS NULL
     OR v_implementation_proposal.applied_by IS NULL
     OR v_implementation_proposal.build_manifest_id <> v_build_manifest_id
     OR v_implementation_proposal.application_build_contract_id IS DISTINCT FROM v_build_contract_id
     OR workflow_input_normalize_sha256(v_implementation_proposal.application_build_contract_hash) <>
        v_quality->>'buildContractHash' THEN
    RAISE EXCEPTION 'Workflow Input target is not the exact applied implementation producer closure'
      USING ERRCODE = 'WIA04';
  END IF;

  SELECT * INTO v_quality_run
  FROM quality_runs WHERE id = v_quality_run_id FOR UPDATE;
  IF v_quality_run.id IS NULL
     OR v_quality_run.project_id <> v_project.id
     OR v_quality_run.workflow_run_id IS DISTINCT FROM v_run.id
     OR v_quality_run.workspace_artifact_id <> v_artifact.id
     OR v_quality_run.workspace_revision_id <> v_revision.id
     OR workflow_input_normalize_sha256(v_quality_run.workspace_content_hash) <>
        workflow_input_normalize_sha256(v_revision.content_hash)
     OR v_quality_run.status <> 'passed'
     OR v_quality_run.completed_at IS NULL THEN
    RAISE EXCEPTION 'Workflow Input qualityRunId is not one passing delivery QualityRun for the exact target'
      USING ERRCODE = 'WIA04';
  END IF;

  SELECT * INTO v_build_manifest
  FROM application_build_manifests WHERE id = v_build_manifest_id FOR UPDATE;
  SELECT * INTO v_build_contract
  FROM application_build_contracts WHERE id = v_build_contract_id FOR UPDATE;
  PERFORM 1 FROM application_build_contract_sources
  WHERE contract_id = v_build_contract_id
  ORDER BY ordinal FOR SHARE;
  PERFORM 1 FROM application_build_contract_template_releases
  WHERE contract_id = v_build_contract_id
  ORDER BY ordinal FOR SHARE;
  PERFORM 1 FROM application_build_contract_obligations
  WHERE contract_id = v_build_contract_id
  ORDER BY obligation_id FOR SHARE;
  IF v_build_manifest.id IS NULL OR v_build_contract.id IS NULL
     OR v_build_manifest.project_id <> v_project.id
     OR v_build_manifest.workflow_run_id IS DISTINCT FROM v_run.id
     OR v_build_manifest.status <> 'consumed' OR v_build_manifest.invalidated_at IS NOT NULL
     OR workflow_input_normalize_sha256(v_build_manifest.manifest_hash) <> v_quality->>'buildManifestHash'
     OR workflow_input_raw_sha256(p_build_manifest_raw_bytes) <> workflow_input_normalize_sha256(v_build_manifest.content_hash)
     OR v_build_contract.project_id <> v_project.id
     OR v_build_contract.build_manifest_id <> v_build_manifest.id
     OR workflow_input_normalize_sha256(v_build_contract.build_manifest_hash) <> workflow_input_normalize_sha256(v_build_manifest.manifest_hash)
     OR workflow_input_normalize_sha256(v_build_contract.contract_hash) <> v_quality->>'buildContractHash'
     OR workflow_input_raw_sha256(p_build_contract_raw_bytes) <> workflow_input_normalize_sha256(v_build_contract.content_hash)
     OR v_build_contract.status <> 'ready' OR v_build_contract.superseded_at IS NOT NULL
     OR v_build_contract.must_count < 1
     OR v_build_contract.must_ready_count <> v_build_contract.must_count
     OR v_build_contract.blocking_count <> 0 OR v_build_contract.conflict_count <> 0
     OR v_build_contract.source_count <> (SELECT count(*) FROM application_build_contract_sources WHERE contract_id = v_build_contract.id)
     OR v_build_contract.template_release_count <> (SELECT count(*) FROM application_build_contract_template_releases WHERE contract_id = v_build_contract.id)
     OR v_build_contract.obligation_count <> (SELECT count(*) FROM application_build_contract_obligations WHERE contract_id = v_build_contract.id)
     OR v_build_contract.must_count <> (
       SELECT count(*) FROM application_build_contract_obligations
       WHERE contract_id = v_build_contract.id AND level = 'must'
     )
     OR v_build_contract.must_ready_count <> (
       SELECT count(*) FROM application_build_contract_obligations
       WHERE contract_id = v_build_contract.id AND level = 'must' AND status IN ('ready','waived')
     ) THEN
    RAISE EXCEPTION 'Workflow Input BuildManifest/BuildContract exact closure is invalid'
      USING ERRCODE = 'WIA04';
  END IF;
  IF jsonb_typeof(v_build_contract_document) IS DISTINCT FROM 'object'
     OR v_build_contract_document->>'schemaVersion' <> v_build_contract.schema_version
     OR v_build_contract_document->>'projectId' <> v_build_contract.project_id::text
     OR v_build_contract_document->>'deliverySliceId' <> v_build_manifest.delivery_slice_id
     OR v_build_contract_document->>'status' <> v_build_contract.status
     OR v_build_contract_document#>>'{compiler,version}' <> v_build_contract.compiler_version
     OR workflow_input_normalize_sha256(v_build_contract_document#>>'{compiler,hash}') <>
        workflow_input_normalize_sha256(v_build_contract.compiler_hash)
     OR v_build_contract_document#>>'{buildManifest,id}' <> v_build_contract.build_manifest_id::text
     OR workflow_input_normalize_sha256(v_build_contract_document#>>'{buildManifest,contentHash}') <>
        workflow_input_normalize_sha256(v_build_contract.build_manifest_hash)
     OR v_build_contract_document#>>'{fullStackTemplate,id}' <> v_build_contract.full_stack_template_id::text
     OR workflow_input_normalize_sha256(v_build_contract_document#>>'{fullStackTemplate,contentHash}') <>
        workflow_input_normalize_sha256(v_build_contract.full_stack_template_hash)
     OR v_build_contract_document#>>'{fullStackTemplate,certification}' <> 'approved'
     OR v_build_contract_document#>>'{fullStackTemplate,policyStatus}' <> 'active'
     OR jsonb_typeof(v_build_contract_document->'sourceRevisions') IS DISTINCT FROM 'array'
     OR jsonb_typeof(v_build_contract_document->'templateReleaseRefs') IS DISTINCT FROM 'array'
     OR jsonb_typeof(v_build_contract_document->'obligations') IS DISTINCT FROM 'array'
     OR jsonb_array_length(v_build_contract_document->'sourceRevisions') <> v_build_contract.source_count
     OR jsonb_array_length(v_build_contract_document->'templateReleaseRefs') <> v_build_contract.template_release_count
     OR jsonb_array_length(v_build_contract_document->'obligations') <> v_build_contract.obligation_count
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_build_contract_document->'sourceRevisions') WITH ORDINALITY
         AS item(value, ordinal)
       LEFT JOIN application_build_contract_sources AS source
         ON source.contract_id = v_build_contract.id AND source.ordinal = item.ordinal - 1
       WHERE jsonb_typeof(item.value) <> 'object'
          OR item.value - ARRAY[
            'kind','purpose','required','artifactId','revisionId','contentHash','approvalStatus'
          ] <> '{}'::jsonb
          OR NOT (item.value ?& ARRAY[
            'kind','purpose','required','artifactId','revisionId','contentHash','approvalStatus'
          ])
          OR source.contract_id IS NULL
          OR item.value->>'kind' <> source.source_kind
          OR item.value->>'purpose' <> source.purpose
          OR item.value->'required' IS DISTINCT FROM to_jsonb(source.required)
          OR item.value->>'artifactId' <> source.artifact_id::text
          OR item.value->>'revisionId' <> source.revision_id::text
          OR workflow_input_normalize_sha256(item.value->>'contentHash') <>
             workflow_input_normalize_sha256(source.content_hash)
          OR item.value->>'approvalStatus' <> 'approved'
     )
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_build_contract_document->'templateReleaseRefs') WITH ORDINALITY
         AS item(value, ordinal)
       LEFT JOIN application_build_contract_template_releases AS release
         ON release.contract_id = v_build_contract.id AND release.ordinal = item.ordinal - 1
       WHERE jsonb_typeof(item.value) <> 'object'
          OR item.value - ARRAY['id','releaseHash','role','certification','policyStatus'] <> '{}'::jsonb
          OR NOT (item.value ?& ARRAY['id','releaseHash','role','certification','policyStatus'])
          OR release.contract_id IS NULL
          OR item.value->>'id' <> release.template_release_id::text
          OR workflow_input_normalize_sha256(item.value->>'releaseHash') <>
             workflow_input_normalize_sha256(release.template_release_content_hash)
          OR item.value->>'role' <> release.role
          OR item.value->>'certification' <> 'approved'
          OR item.value->>'policyStatus' <> 'active'
     )
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_build_contract_document->'obligations') WITH ORDINALITY
         AS item(value, ordinal)
       LEFT JOIN application_build_contract_obligations AS obligation
         ON obligation.contract_id = v_build_contract.id
        AND obligation.obligation_id = item.value->>'id'
       LEFT JOIN artifacts AS obligation_artifact
         ON obligation_artifact.id = obligation.source_artifact_id
       LEFT JOIN application_build_contract_sources AS obligation_source
         ON obligation_source.contract_id = obligation.contract_id
        AND obligation_source.artifact_id = obligation.source_artifact_id
        AND obligation_source.revision_id = obligation.source_revision_id
        AND workflow_input_normalize_sha256(obligation_source.content_hash) =
            workflow_input_normalize_sha256(obligation.source_content_hash)
       WHERE jsonb_typeof(item.value) <> 'object'
          OR item.value - ARRAY[
            'id','level','kind','sourceRevision','sourceAnchorId','oracleIds','dependsOn',
            'waivable','status','blockingReasonId'
          ] <> '{}'::jsonb
          OR NOT (item.value ?& ARRAY[
            'id','level','kind','sourceRevision','sourceAnchorId','oracleIds','dependsOn',
            'waivable','status'
          ])
          OR jsonb_typeof(item.value->'sourceRevision') <> 'object'
          OR (item.value->'sourceRevision') - ARRAY[
            'kind','purpose','required','artifactId','revisionId','contentHash','approvalStatus'
          ] <> '{}'::jsonb
          OR NOT ((item.value->'sourceRevision') ?& ARRAY[
            'kind','purpose','required','artifactId','revisionId','contentHash','approvalStatus'
          ])
          OR obligation.contract_id IS NULL
          OR item.ordinal <> 1 + (
            SELECT count(*)
            FROM application_build_contract_obligations AS earlier
            WHERE earlier.contract_id = v_build_contract.id
              AND convert_to(earlier.obligation_id, 'UTF8') < convert_to(obligation.obligation_id, 'UTF8')
          )
          OR item.value->>'level' <> obligation.level
          OR item.value->>'kind' <> obligation.kind
          OR item.value#>>'{sourceRevision,kind}' <> obligation_artifact.kind
          OR item.value#>>'{sourceRevision,purpose}' <> CASE
            WHEN obligation.source_revision_id = v_target_revision_id THEN 'workspace-target'
            ELSE obligation_source.purpose
          END
          OR item.value#>'{sourceRevision,required}' IS DISTINCT FROM to_jsonb(CASE
            WHEN obligation.source_revision_id = v_target_revision_id THEN true
            ELSE obligation_source.required
          END)
          OR item.value#>>'{sourceRevision,artifactId}' <> obligation.source_artifact_id::text
          OR item.value#>>'{sourceRevision,revisionId}' <> obligation.source_revision_id::text
          OR workflow_input_normalize_sha256(item.value#>>'{sourceRevision,contentHash}') <>
             workflow_input_normalize_sha256(obligation.source_content_hash)
          OR item.value#>>'{sourceRevision,approvalStatus}' <> 'approved'
          OR item.value->>'sourceAnchorId' <> obligation.source_anchor_id
          OR item.value->'oracleIds' IS DISTINCT FROM obligation.oracle_ids
          OR item.value->'dependsOn' IS DISTINCT FROM obligation.depends_on
          OR item.value->'waivable' IS DISTINCT FROM to_jsonb(obligation.waivable)
          OR item.value->>'status' <> obligation.status
          OR item.value->>'blockingReasonId' IS DISTINCT FROM obligation.blocking_reason_id
     ) THEN
    RAISE EXCEPTION 'Workflow Input retained BuildContract bytes disagree with exact child projections'
      USING ERRCODE = 'WIA04';
  END IF;
  IF jsonb_typeof(v_build_manifest_document) IS DISTINCT FROM 'object'
     OR v_build_manifest_document->>'id' <> v_build_manifest.id::text
     OR v_build_manifest_document->>'projectId' <> v_project.id::text
     OR v_build_manifest_document->>'rootBuildManifestId' <> v_build_manifest.root_manifest_id::text
     OR v_build_manifest_document->>'workflowRunId' <> v_run.id::text
     OR v_build_manifest_document->>'manifestGroupKey' <> v_build_manifest.manifest_group_key
     OR v_build_manifest_document->>'deliverySliceId' <> v_build_manifest.delivery_slice_id
     OR workflow_input_normalize_sha256(v_build_manifest_document->>'contentHash') <>
        workflow_input_normalize_sha256(v_build_manifest.manifest_hash)
     OR jsonb_typeof(v_build_manifest_document->'blueprintRevision') IS DISTINCT FROM 'object'
     OR jsonb_typeof(v_build_manifest_document->'pageSpecRevision') IS DISTINCT FROM 'object'
     OR jsonb_typeof(v_build_manifest_document->'prototypeRevision') IS DISTINCT FROM 'object'
     OR jsonb_typeof(v_build_manifest_document->'requirementRevisions') IS DISTINCT FROM 'array'
     OR jsonb_typeof(v_build_manifest_document->'contractRevisions') IS DISTINCT FROM 'array'
     OR jsonb_typeof(v_build_manifest_document->'designSystemRevisions') IS DISTINCT FROM 'array'
     OR jsonb_typeof(v_build_manifest_document->'contextRevisions') IS DISTINCT FROM 'array' THEN
    RAISE EXCEPTION 'Workflow Input retained Workbench bundle identity or source collections disagree with the locked leaf'
      USING ERRCODE = 'WIA04';
  END IF;

  -- Rebuild every predecessor from the exact retained NodeInput plus locked
  -- source-node rows. The final member order is the normative
  -- (edgeId, sourceNodeRunId, sourcePort, targetPort) byte order.
  v_previous_order := NULL;
  FOR v_binding, v_ordinal IN
    SELECT binding, (ordinality - 1)::integer
    FROM jsonb_array_elements(v_node_input_document->'bindings') WITH ORDINALITY
      AS item(binding, ordinality)
  LOOP
    IF jsonb_typeof(v_binding) <> 'object'
       OR NOT (v_binding ?& ARRAY['edgeId','fromPort','output','outputHash','source','toPort','value','valueHash'])
       OR v_binding - ARRAY['edgeId','fromPort','mapping','output','outputHash','source','toPort','value','valueHash'] <> '{}'::jsonb
       OR jsonb_typeof(v_binding->'edgeId') <> 'string'
       OR jsonb_typeof(v_binding->'fromPort') <> 'string'
       OR jsonb_typeof(v_binding->'toPort') <> 'string'
       OR octet_length(v_binding->>'edgeId') NOT BETWEEN 1 AND 256
       OR octet_length(v_binding->>'fromPort') NOT BETWEEN 1 AND 256
       OR octet_length(v_binding->>'toPort') NOT BETWEEN 1 AND 256
       OR jsonb_typeof(v_binding->'source') <> 'object'
       OR NOT ((v_binding->'source') ?& ARRAY['definitionNodeId','nodeKey','runId'])
       OR (v_binding->'source') - ARRAY[
         'artifactRevisions','definitionNodeId','deliverySliceRefs','inputManifest',
         'materializedArtifactRevisions','nodeKey','outputProposal','outputRevisionId',
         'proposalPins','runId','sliceId'
       ] <> '{}'::jsonb
       OR v_binding->'source'->>'runId' <> v_run.id::text
       OR jsonb_typeof(v_binding->'outputHash') <> 'string'
       OR jsonb_typeof(v_binding->'valueHash') <> 'string'
       OR workflow_input_normalize_sha256(v_binding->>'outputHash') !~ '^sha256:[0-9a-f]{64}$'
       OR workflow_input_normalize_sha256(v_binding->>'valueHash') !~ '^sha256:[0-9a-f]{64}$'
       OR (v_binding ? 'mapping' AND jsonb_typeof(v_binding->'mapping') IS DISTINCT FROM 'object')
       OR EXISTS (
         SELECT 1 FROM jsonb_each(
           CASE WHEN jsonb_typeof(v_binding->'mapping') = 'object'
             THEN v_binding->'mapping' ELSE '{}'::jsonb END
         ) AS mapping
         WHERE jsonb_typeof(mapping.value) <> 'string'
            OR octet_length(mapping.key) NOT BETWEEN 1 AND 256
            OR octet_length(mapping.value #>> '{}') NOT BETWEEN 1 AND 256
       ) THEN
      RAISE EXCEPTION 'Workflow Input predecessor binding shape is invalid'
        USING ERRCODE = 'WIA03';
    END IF;

    SELECT count(*) INTO v_count
    FROM jsonb_array_elements(v_definition_document->'edges') AS edge(value)
    WHERE edge.value->>'id' = v_binding->>'edgeId';
    IF v_count = 1 THEN
      SELECT edge.value INTO v_definition_edge
      FROM jsonb_array_elements(v_definition_document->'edges') AS edge(value)
      WHERE edge.value->>'id' = v_binding->>'edgeId';
    ELSE
      v_definition_edge := NULL;
    END IF;
    IF v_count <> 1
       OR jsonb_typeof(v_definition_edge) IS DISTINCT FROM 'object'
       OR v_definition_edge->>'from' <> v_binding->'source'->>'definitionNodeId'
       OR v_definition_edge->>'to' <> v_node.definition_node_id
       OR COALESCE(NULLIF(v_definition_edge->>'fromPort',''), 'default') <> v_binding->>'fromPort'
       OR COALESCE(NULLIF(v_definition_edge->>'toPort',''), 'default') <> v_binding->>'toPort'
       OR (v_definition_edge ? 'mapping'
         AND jsonb_typeof(v_definition_edge->'mapping') IS DISTINCT FROM 'object')
       OR (CASE WHEN jsonb_typeof(v_definition_edge->'mapping') = 'object'
             THEN v_definition_edge->'mapping' ELSE '{}'::jsonb END)
          IS DISTINCT FROM
          (CASE WHEN jsonb_typeof(v_binding->'mapping') = 'object'
             THEN v_binding->'mapping' ELSE '{}'::jsonb END) THEN
      RAISE EXCEPTION 'Workflow Input predecessor does not equal its exact Definition edge and mapping'
        USING ERRCODE = 'WIA04';
    END IF;

    SELECT * INTO v_source_node
    FROM workflow_node_runs
    WHERE run_id = v_run.id AND node_key = v_binding->'source'->>'nodeKey'
    FOR UPDATE;
    IF v_source_node.id IS NULL OR v_source_node.status <> 'completed'
       OR v_source_node.definition_node_id IS NULL
       OR v_source_node.definition_node_id <> v_binding->'source'->>'definitionNodeId'
       OR v_source_node.output_revision_id IS NULL
       OR (
         (v_source_node.slice_kind = 'root' AND (
           v_source_node.slice_id IS NOT NULL OR v_binding->'source'->>'sliceId' IS NOT NULL
         ))
         OR
         (v_source_node.slice_kind = 'slice' AND (
           v_source_node.slice_id IS NULL OR v_binding->'source'->>'sliceId' <> v_source_node.slice_id::text
         ))
         OR v_source_node.slice_kind NOT IN ('root','slice')
       )
       OR v_binding->'source'->>'outputRevisionId' IS DISTINCT FROM v_source_node.output_revision_id::text THEN
      RAISE EXCEPTION 'Workflow Input predecessor does not equal one completed stable source node'
        USING ERRCODE = 'WIA04';
    END IF;

    SELECT * INTO v_source_output_revision
    FROM artifact_revisions WHERE id = v_source_node.output_revision_id FOR SHARE;
    IF v_source_output_revision.id IS NULL
       OR v_source_output_revision.revision_number NOT BETWEEN 1 AND 9007199254740991 THEN
      RAISE EXCEPTION 'Workflow Input predecessor output revision is absent or unsafe'
        USING ERRCODE = 'WIA04';
    END IF;
    IF v_source_node.id = v_quality_run_id THEN
      RAISE EXCEPTION 'Workflow Input quality-run identity cannot be reused as a workflow node run'
        USING ERRCODE = 'WIA03';
    END IF;
    IF v_source_node.node_type = 'quality_gate' THEN
      v_quality_output := v_binding->'output';
      v_quality_findings := v_quality_output->'findings';
      v_quality_build_manifest := v_quality_output->'buildManifest';

      -- The external gate consumes the production QualityResult wire, not an
      -- untyped success marker. Identity mapping plus byte-identical
      -- output/value prevents a mutable edge transform from changing the
      -- quality decision after the producer completed.
      IF v_binding->'output' IS DISTINCT FROM v_binding->'value'
         OR (v_binding ? 'mapping' AND (
           SELECT count(*) FROM jsonb_each(v_binding->'mapping')
         ) <> 0)
         OR jsonb_typeof(v_quality_output) IS DISTINCT FROM 'object'
         OR (v_quality_output ?& ARRAY[
           'buildManifest','findings','passed','qualityRunId','workspaceRevision'
         ]) IS NOT TRUE
         OR v_quality_output - ARRAY[
           'buildManifest','findings','passed','qualityRunId','workspaceRevision'
         ] <> '{}'::jsonb
         OR v_quality_output->'passed' IS DISTINCT FROM 'true'::jsonb
         OR jsonb_typeof(v_quality_output->'qualityRunId') IS DISTINCT FROM 'string'
         OR v_quality_output->>'qualityRunId' <> v_quality_run.id::text
         OR jsonb_typeof(v_quality_output->'workspaceRevision') IS DISTINCT FROM 'object'
         OR ((v_quality_output->'workspaceRevision') ?& ARRAY[
           'artifactId','contentHash','revisionId'
         ]) IS NOT TRUE
         OR (v_quality_output->'workspaceRevision') - ARRAY[
           'artifactId','contentHash','revisionId'
         ] <> '{}'::jsonb
         OR v_quality_output#>>'{workspaceRevision,artifactId}' <> v_artifact.id::text
         OR v_quality_output#>>'{workspaceRevision,revisionId}' <> v_revision.id::text
         OR workflow_input_normalize_sha256(
           v_quality_output#>>'{workspaceRevision,contentHash}'
         ) <> workflow_input_normalize_sha256(v_revision.content_hash) THEN
        RAISE EXCEPTION 'Workflow Input quality binding is not the exact typed passing result'
          USING ERRCODE = 'WIA04';
      END IF;

      IF jsonb_typeof(v_quality_findings) IS DISTINCT FROM 'object'
         OR (v_quality_findings ?& ARRAY[
           'checks','diagnostics','qualityRunId','reportArtifactId',
           'reportRevisionId','score','workspaceRevision'
         ]) IS NOT TRUE
         OR v_quality_findings - ARRAY[
           'checks','diagnostics','qualityRunId','reportArtifactId',
           'reportRevisionId','score','workspaceRevision'
         ] <> '{}'::jsonb
         OR jsonb_typeof(v_quality_findings->'checks') IS DISTINCT FROM 'array'
         OR jsonb_typeof(v_quality_findings->'diagnostics') IS DISTINCT FROM 'array'
         OR jsonb_typeof(v_quality_findings->'qualityRunId') IS DISTINCT FROM 'string'
         OR v_quality_findings->>'qualityRunId' <> v_quality_run.id::text
         OR jsonb_typeof(v_quality_findings->'reportArtifactId') IS DISTINCT FROM 'string'
         OR workflow_input_uuid_is_exact(v_quality_findings->>'reportArtifactId') IS NOT TRUE
         OR jsonb_typeof(v_quality_findings->'reportRevisionId') IS DISTINCT FROM 'string'
         OR workflow_input_uuid_is_exact(v_quality_findings->>'reportRevisionId') IS NOT TRUE
         OR v_quality_run.report_artifact_id IS NULL
         OR v_quality_findings->>'reportArtifactId' <> v_quality_run.report_artifact_id::text
         OR v_quality_run.report_revision_id IS NULL
         OR v_quality_findings->>'reportRevisionId' <> v_quality_run.report_revision_id::text
         OR jsonb_typeof(v_quality_findings->'score') IS DISTINCT FROM 'number'
         OR (v_quality_findings->>'score' ~ '^(0|[1-9][0-9]*)$') IS NOT TRUE
         OR jsonb_typeof(v_quality_findings->'workspaceRevision') IS DISTINCT FROM 'object'
         OR ((v_quality_findings->'workspaceRevision') ?& ARRAY[
           'artifactId','contentHash','revisionId'
         ]) IS NOT TRUE
         OR (v_quality_findings->'workspaceRevision') - ARRAY[
           'artifactId','contentHash','revisionId'
         ] <> '{}'::jsonb
         OR v_quality_findings#>>'{workspaceRevision,artifactId}' <> v_artifact.id::text
         OR v_quality_findings#>>'{workspaceRevision,revisionId}' <> v_revision.id::text
         OR workflow_input_normalize_sha256(
           v_quality_findings#>>'{workspaceRevision,contentHash}'
         ) <> workflow_input_normalize_sha256(v_revision.content_hash) THEN
        RAISE EXCEPTION 'Workflow Input quality findings do not repeat the locked run/report/Workspace result'
          USING ERRCODE = 'WIA04';
      END IF;
      IF (v_quality_findings->>'score')::bigint NOT BETWEEN 0 AND 100
         OR (v_quality_findings->>'score')::bigint <> v_quality_run.score THEN
        RAISE EXCEPTION 'Workflow Input quality findings score disagrees with the locked QualityRun'
          USING ERRCODE = 'WIA04';
      END IF;

      IF jsonb_typeof(v_quality_build_manifest) IS DISTINCT FROM 'object'
         OR (v_quality_build_manifest ?& ARRAY[
           'bundleIds','constraints','createdAt','hash','manifestGroupKey',
           'projectId','runId','schemaVersion','sliceIds','sources'
         ]) IS NOT TRUE
         OR v_quality_build_manifest - ARRAY[
           'bundleIds','constraints','createdAt','hash','manifestGroupKey',
           'projectId','runId','schemaVersion','sliceIds','sources'
         ] <> '{}'::jsonb
         OR jsonb_typeof(v_quality_build_manifest->'schemaVersion') IS DISTINCT FROM 'number'
         OR (v_quality_build_manifest->>'schemaVersion' ~ '^[1-9][0-9]*$') IS NOT TRUE
         OR jsonb_typeof(v_quality_build_manifest->'projectId') IS DISTINCT FROM 'string'
         OR v_quality_build_manifest->>'projectId' <> v_project.id::text
         OR jsonb_typeof(v_quality_build_manifest->'runId') IS DISTINCT FROM 'string'
         OR v_quality_build_manifest->>'runId' <> v_run.id::text
         OR jsonb_typeof(v_quality_build_manifest->'manifestGroupKey') IS DISTINCT FROM 'string'
         OR workflow_input_uuid_is_exact(v_quality_build_manifest->>'manifestGroupKey') IS NOT TRUE
         OR v_build_manifest.manifest_group_key IS NULL
         OR v_quality_build_manifest->>'manifestGroupKey' <> v_build_manifest.manifest_group_key
         OR jsonb_typeof(v_quality_build_manifest->'sliceIds') IS DISTINCT FROM 'array'
         OR jsonb_array_length(v_quality_build_manifest->'sliceIds') NOT BETWEEN 1 AND 1024
         OR jsonb_typeof(v_quality_build_manifest->'bundleIds') IS DISTINCT FROM 'array'
         OR jsonb_array_length(v_quality_build_manifest->'bundleIds') <>
            jsonb_array_length(v_quality_build_manifest->'sliceIds')
         OR jsonb_typeof(v_quality_build_manifest->'sources') IS DISTINCT FROM 'array'
         OR jsonb_array_length(v_quality_build_manifest->'sources') NOT BETWEEN 1 AND 2048
         OR jsonb_typeof(v_quality_build_manifest->'createdAt') IS DISTINCT FROM 'string'
         OR (v_quality_build_manifest->>'createdAt' ~
           '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{0,8}[1-9])?Z$'
         ) IS NOT TRUE
         OR jsonb_typeof(v_quality_build_manifest->'hash') IS DISTINCT FROM 'string'
         OR workflow_input_normalize_sha256(v_quality_build_manifest->>'hash') !~
            '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'Workflow Input quality BuildManifest wire shape is invalid or widened'
          USING ERRCODE = 'WIA03';
      END IF;
      IF (v_quality_build_manifest->>'schemaVersion')::bigint NOT BETWEEN 1 AND 9007199254740991 THEN
        RAISE EXCEPTION 'Workflow Input quality BuildManifest schemaVersion is unsafe'
          USING ERRCODE = 'WIA03';
      END IF;
      BEGIN
        v_quality_created_at := (v_quality_build_manifest->>'createdAt')::timestamptz;
      EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION 'Workflow Input quality BuildManifest createdAt is not RFC3339 UTC'
          USING ERRCODE = 'WIA03';
      END;

      IF v_build_manifest.delivery_slice_id IS NULL
         OR v_quality_build_manifest->'bundleIds'->>(
           jsonb_array_length(v_quality_build_manifest->'bundleIds') - 1
         ) <> v_build_manifest.root_manifest_id::text
         OR v_quality_build_manifest->'sliceIds'->>(
           jsonb_array_length(v_quality_build_manifest->'sliceIds') - 1
         ) <> v_build_manifest.delivery_slice_id
         OR EXISTS (
           SELECT 1
           FROM jsonb_array_elements_text(v_quality_build_manifest->'bundleIds') AS bundle(value)
           WHERE workflow_input_uuid_is_exact(bundle.value) IS NOT TRUE
         )
         OR (SELECT count(*) FROM jsonb_array_elements_text(v_quality_build_manifest->'bundleIds')) <>
            (SELECT count(DISTINCT value) FROM jsonb_array_elements_text(v_quality_build_manifest->'bundleIds'))
         OR EXISTS (
           SELECT 1
           FROM jsonb_array_elements_text(v_quality_build_manifest->'sliceIds') AS slice(value)
           WHERE workflow_input_uuid_is_exact(slice.value) IS NOT TRUE
         )
         OR (SELECT count(*) FROM jsonb_array_elements_text(v_quality_build_manifest->'sliceIds')) <>
            (SELECT count(DISTINCT value) FROM jsonb_array_elements_text(v_quality_build_manifest->'sliceIds'))
         OR EXISTS (
           SELECT 1
           FROM jsonb_array_elements(v_quality_build_manifest->'sources') AS source(value)
           WHERE jsonb_typeof(source.value) IS DISTINCT FROM 'object'
              OR (source.value ?& ARRAY['artifactId','contentHash','revisionId']) IS NOT TRUE
              OR source.value - ARRAY['anchorId','artifactId','contentHash','revisionId'] <> '{}'::jsonb
              OR jsonb_typeof(source.value->'artifactId') IS DISTINCT FROM 'string'
              OR octet_length(btrim(source.value->>'artifactId')) NOT BETWEEN 1 AND 256
              OR jsonb_typeof(source.value->'revisionId') IS DISTINCT FROM 'string'
              OR octet_length(btrim(source.value->>'revisionId')) NOT BETWEEN 1 AND 256
              OR jsonb_typeof(source.value->'contentHash') IS DISTINCT FROM 'string'
              OR workflow_input_normalize_sha256(source.value->>'contentHash') !~ '^sha256:[0-9a-f]{64}$'
              OR (source.value ? 'anchorId' AND (
                jsonb_typeof(source.value->'anchorId') IS DISTINCT FROM 'string'
                OR source.value->>'anchorId' = ''
              ))
         )
         OR (SELECT count(*) FROM jsonb_array_elements(v_quality_build_manifest->'sources')) <>
            (SELECT count(*) FROM (
              SELECT DISTINCT
                source.value->>'artifactId' AS artifact_id,
                source.value->>'revisionId' AS revision_id,
                workflow_input_normalize_sha256(source.value->>'contentHash') AS content_hash,
                COALESCE(source.value->>'anchorId','') AS anchor_id
              FROM jsonb_array_elements(v_quality_build_manifest->'sources') AS source(value)
            ) AS unique_source)
         OR EXISTS (
           WITH required_source(reference) AS (
             SELECT v_build_manifest_document->'blueprintRevision'
             UNION ALL SELECT v_build_manifest_document->'pageSpecRevision'
             UNION ALL SELECT v_build_manifest_document->'prototypeRevision'
             UNION ALL
               SELECT item.value
               FROM jsonb_array_elements(v_build_manifest_document->'requirementRevisions') AS item(value)
             UNION ALL
               SELECT item.value
               FROM jsonb_array_elements(v_build_manifest_document->'contractRevisions') AS item(value)
             UNION ALL
               SELECT item.value
               FROM jsonb_array_elements(v_build_manifest_document->'designSystemRevisions') AS item(value)
             UNION ALL
               SELECT item.value->'revision'
               FROM jsonb_array_elements(v_build_manifest_document->'contextRevisions') AS item(value)
             UNION ALL
               SELECT v_build_manifest_document#>'{workflowContext,inputManifest,baseRevision}'
               WHERE jsonb_typeof(v_build_manifest_document#>'{workflowContext,inputManifest,baseRevision}') = 'object'
             UNION ALL
               SELECT item.value->'ref'
               FROM jsonb_array_elements(
                 CASE WHEN jsonb_typeof(v_build_manifest_document#>'{workflowContext,inputManifest,sources}') = 'array'
                   THEN v_build_manifest_document#>'{workflowContext,inputManifest,sources}' ELSE '[]'::jsonb END
               ) AS item(value)
           )
           SELECT 1
           FROM required_source AS required
           WHERE jsonb_typeof(required.reference) IS DISTINCT FROM 'object'
              OR (required.reference ?& ARRAY['artifactId','contentHash','revisionId']) IS NOT TRUE
              OR required.reference - ARRAY['anchorId','artifactId','contentHash','revisionId'] <> '{}'::jsonb
              OR NOT EXISTS (
                SELECT 1
                FROM jsonb_array_elements(v_quality_build_manifest->'sources') AS source(value)
                WHERE source.value->>'artifactId' = required.reference->>'artifactId'
                  AND source.value->>'revisionId' = required.reference->>'revisionId'
                  AND workflow_input_normalize_sha256(source.value->>'contentHash') =
                      workflow_input_normalize_sha256(required.reference->>'contentHash')
                  AND COALESCE(source.value->>'anchorId','') = COALESCE(required.reference->>'anchorId','')
              )
         ) THEN
        RAISE EXCEPTION 'Workflow Input quality BuildManifest does not bind the exact selected bundle lineage'
          USING ERRCODE = 'WIA04';
      END IF;

      IF v_source_node.output_revision_id <> v_target_revision_id
         OR NOT EXISTS (
           SELECT 1
           FROM jsonb_array_elements(
             CASE WHEN jsonb_typeof(v_binding->'source'->'artifactRevisions') = 'array'
               THEN v_binding->'source'->'artifactRevisions' ELSE '[]'::jsonb END
           ) AS reference(value)
           WHERE value->>'artifactId' = v_artifact.id::text
             AND value->>'revisionId' = v_target_revision_id::text
             AND workflow_input_normalize_sha256(value->>'contentHash') =
                 workflow_input_normalize_sha256(v_revision.content_hash)
         ) THEN
        RAISE EXCEPTION 'Workflow Input quality predecessor does not produce the exact target revision'
          USING ERRCODE = 'WIA04';
      END IF;
      v_quality_predecessors := v_quality_predecessors + 1;
    END IF;

    IF (v_source_node.input_manifest_id IS NULL) <> (v_binding->'source'->'inputManifest' IS NULL)
       OR (
         v_source_node.input_manifest_id IS NOT NULL AND NOT EXISTS (
           SELECT 1 FROM input_manifests AS manifest
           WHERE manifest.id = v_source_node.input_manifest_id
             AND v_binding->'source'->'inputManifest'->>'id' = manifest.id::text
             AND workflow_input_normalize_sha256(v_binding->'source'->'inputManifest'->>'hash') = workflow_input_normalize_sha256(manifest.manifest_hash)
         )
       )
       OR (v_source_node.output_proposal_id IS NULL) <> (v_binding->'source'->'outputProposal' IS NULL)
       OR (
         v_source_node.output_proposal_id IS NOT NULL AND NOT EXISTS (
           SELECT 1 FROM output_proposals AS proposal
           WHERE proposal.id = v_source_node.output_proposal_id
             AND v_binding->'source'->'outputProposal'->>'id' = proposal.id::text
             AND workflow_input_normalize_sha256(v_binding->'source'->'outputProposal'->>'payloadHash') = workflow_input_normalize_sha256(proposal.payload_hash)
         )
       ) THEN
      RAISE EXCEPTION 'Workflow Input predecessor manifest/proposal pointers disagree'
        USING ERRCODE = 'WIA04';
    END IF;

    IF (v_binding->'source' ? 'artifactRevisions'
          AND jsonb_typeof(v_binding->'source'->'artifactRevisions') IS DISTINCT FROM 'array')
       OR (v_binding->'source' ? 'materializedArtifactRevisions'
          AND jsonb_typeof(v_binding->'source'->'materializedArtifactRevisions') IS DISTINCT FROM 'array')
       OR (v_binding->'source' ? 'proposalPins'
          AND jsonb_typeof(v_binding->'source'->'proposalPins') IS DISTINCT FROM 'array')
       OR (v_binding->'source' ? 'deliverySliceRefs'
          AND jsonb_typeof(v_binding->'source'->'deliverySliceRefs') IS DISTINCT FROM 'array')
       OR EXISTS (
         SELECT 1
         FROM jsonb_array_elements(
           (CASE WHEN jsonb_typeof(v_binding->'source'->'artifactRevisions') = 'array'
             THEN v_binding->'source'->'artifactRevisions' ELSE '[]'::jsonb END)
           || (CASE WHEN jsonb_typeof(v_binding->'source'->'materializedArtifactRevisions') = 'array'
             THEN v_binding->'source'->'materializedArtifactRevisions' ELSE '[]'::jsonb END)
         ) AS ref
         WHERE jsonb_typeof(ref) <> 'object'
            OR NOT (ref ?& ARRAY['artifactId','contentHash','revisionId'])
            OR ref - ARRAY['anchorId','artifactId','contentHash','revisionId'] <> '{}'::jsonb
            OR NOT EXISTS (
              SELECT 1 FROM artifact_revisions AS revision
              JOIN artifacts AS artifact ON artifact.id = revision.artifact_id
              WHERE revision.id::text = ref->>'revisionId'
                AND artifact.id::text = ref->>'artifactId'
                AND artifact.project_id = v_project.id
                AND workflow_input_normalize_sha256(revision.content_hash) = workflow_input_normalize_sha256(ref->>'contentHash')
            )
       ) OR EXISTS (
         SELECT 1
         FROM jsonb_array_elements(
           CASE WHEN jsonb_typeof(v_binding->'source'->'proposalPins') = 'array'
             THEN v_binding->'source'->'proposalPins' ELSE '[]'::jsonb END
         ) AS pin
         WHERE jsonb_typeof(pin) <> 'object'
            OR NOT (pin ?& ARRAY['manifest','producerDefinitionNodeId','producerNodeKey','proposal'])
            OR pin - ARRAY['manifest','producerDefinitionNodeId','producerNodeKey','proposal'] <> '{}'::jsonb
            OR NOT EXISTS (
              SELECT 1 FROM output_proposals AS proposal
              JOIN input_manifests AS manifest ON manifest.id = proposal.input_manifest_id
              WHERE proposal.id::text = pin->'proposal'->>'id'
                AND workflow_input_normalize_sha256(proposal.payload_hash) = workflow_input_normalize_sha256(pin->'proposal'->>'payloadHash')
                AND manifest.id::text = pin->'manifest'->>'id'
                AND workflow_input_normalize_sha256(manifest.manifest_hash) = workflow_input_normalize_sha256(pin->'manifest'->>'hash')
            )
       ) OR EXISTS (
         SELECT 1
         FROM jsonb_array_elements(
           CASE WHEN jsonb_typeof(v_binding->'source'->'deliverySliceRefs') = 'array'
             THEN v_binding->'source'->'deliverySliceRefs' ELSE '[]'::jsonb END
         ) AS slice_ref
         WHERE jsonb_typeof(slice_ref) <> 'object'
            OR NOT (slice_ref ?& ARRAY['fanOutNodeId','id','key'])
            OR slice_ref - ARRAY['blueprint','fanOutNodeId','id','key','pageSpec','prototype'] <> '{}'::jsonb
            OR NOT EXISTS (
              SELECT 1 FROM delivery_slices AS slice
              WHERE slice.id::text = slice_ref->>'id'
                AND slice.project_id = v_project.id
                AND slice.slice_key = slice_ref->>'key'
                AND EXISTS (
                  SELECT 1
                  FROM jsonb_array_elements(v_definition_document->'nodes') AS node(value)
                  WHERE node.value->>'id' = slice_ref->>'fanOutNodeId'
                    AND node.value->>'type' = 'fan_out'
                )
                AND jsonb_typeof(slice_ref->'blueprint') = 'object'
                AND (slice_ref->'blueprint') ?& ARRAY['artifactId','contentHash','revisionId']
                AND (slice_ref->'blueprint') - ARRAY['anchorId','artifactId','contentHash','revisionId'] = '{}'::jsonb
                AND EXISTS (
                  SELECT 1
                  FROM artifact_revisions AS revision
                  JOIN artifacts AS artifact ON artifact.id = revision.artifact_id
                  WHERE revision.id = slice.blueprint_revision_id
                    AND revision.id::text = slice_ref#>>'{blueprint,revisionId}'
                    AND artifact.id::text = slice_ref#>>'{blueprint,artifactId}'
                    AND artifact.project_id = v_project.id
                    AND workflow_input_normalize_sha256(revision.content_hash) =
                        workflow_input_normalize_sha256(slice_ref#>>'{blueprint,contentHash}')
                )
                AND (
                  (slice.page_spec_revision_id IS NULL AND NOT (slice_ref ? 'pageSpec'))
                  OR (slice.page_spec_revision_id IS NOT NULL
                    AND jsonb_typeof(slice_ref->'pageSpec') = 'object'
                    AND (slice_ref->'pageSpec') ?& ARRAY['artifactId','contentHash','revisionId']
                    AND (slice_ref->'pageSpec') - ARRAY['anchorId','artifactId','contentHash','revisionId'] = '{}'::jsonb
                    AND EXISTS (
                      SELECT 1
                      FROM artifact_revisions AS revision
                      JOIN artifacts AS artifact ON artifact.id = revision.artifact_id
                      WHERE revision.id = slice.page_spec_revision_id
                        AND revision.id::text = slice_ref#>>'{pageSpec,revisionId}'
                        AND artifact.id::text = slice_ref#>>'{pageSpec,artifactId}'
                        AND artifact.project_id = v_project.id
                        AND workflow_input_normalize_sha256(revision.content_hash) =
                            workflow_input_normalize_sha256(slice_ref#>>'{pageSpec,contentHash}')
                    )
                  )
                )
                AND (
                  (slice.prototype_revision_id IS NULL AND NOT (slice_ref ? 'prototype'))
                  OR (slice.prototype_revision_id IS NOT NULL
                    AND jsonb_typeof(slice_ref->'prototype') = 'object'
                    AND (slice_ref->'prototype') ?& ARRAY['artifactId','contentHash','revisionId']
                    AND (slice_ref->'prototype') - ARRAY['anchorId','artifactId','contentHash','revisionId'] = '{}'::jsonb
                    AND EXISTS (
                      SELECT 1
                      FROM artifact_revisions AS revision
                      JOIN artifacts AS artifact ON artifact.id = revision.artifact_id
                      WHERE revision.id = slice.prototype_revision_id
                        AND revision.id::text = slice_ref#>>'{prototype,revisionId}'
                        AND artifact.id::text = slice_ref#>>'{prototype,artifactId}'
                        AND artifact.project_id = v_project.id
                        AND workflow_input_normalize_sha256(revision.content_hash) =
                            workflow_input_normalize_sha256(slice_ref#>>'{prototype,contentHash}')
                    )
                  )
                )
            )
       ) THEN
      RAISE EXCEPTION 'Workflow Input predecessor propagated lineage is malformed or stale'
        USING ERRCODE = 'WIA04';
    END IF;

    v_order := convert_to(v_binding->>'edgeId', 'UTF8') || decode('00','hex')
      || convert_to(v_source_node.id::text, 'UTF8') || decode('00','hex')
      || convert_to(v_binding->>'fromPort', 'UTF8') || decode('00','hex')
      || convert_to(v_binding->>'toPort', 'UTF8');
    IF v_previous_order IS NOT NULL AND v_previous_order >= v_order THEN
      RAISE EXCEPTION 'Workflow Input predecessors are not in strict canonical order'
        USING ERRCODE = 'WIA03';
    END IF;
    v_previous_order := v_order;
    v_slice_identity := CASE WHEN v_source_node.slice_kind = 'root'
      THEN jsonb_build_object('kind', 'root')
      ELSE jsonb_build_object('id', v_source_node.slice_id::text, 'kind', 'slice') END;
    SELECT COALESCE(jsonb_agg(
      jsonb_build_object(
        'artifactId', reference.value->>'artifactId',
        'contentHash', workflow_input_normalize_sha256(reference.value->>'contentHash'),
        'revisionId', reference.value->>'revisionId'
      ) || CASE WHEN reference.value ? 'anchorId'
        THEN jsonb_build_object('anchorId', reference.value->>'anchorId') ELSE '{}'::jsonb END
      ORDER BY reference.ordinal
    ), '[]'::jsonb) INTO v_artifact_revisions
    FROM jsonb_array_elements(
      CASE WHEN jsonb_typeof(v_binding->'source'->'artifactRevisions') = 'array'
        THEN v_binding->'source'->'artifactRevisions' ELSE '[]'::jsonb END
    ) WITH ORDINALITY AS reference(value, ordinal);
    SELECT COALESCE(jsonb_agg(
      jsonb_build_object(
        'artifactId', reference.value->>'artifactId',
        'contentHash', workflow_input_normalize_sha256(reference.value->>'contentHash'),
        'revisionId', reference.value->>'revisionId'
      ) || CASE WHEN reference.value ? 'anchorId'
        THEN jsonb_build_object('anchorId', reference.value->>'anchorId') ELSE '{}'::jsonb END
      ORDER BY reference.ordinal
    ), '[]'::jsonb) INTO v_materialized_revisions
    FROM jsonb_array_elements(
      CASE WHEN jsonb_typeof(v_binding->'source'->'materializedArtifactRevisions') = 'array'
        THEN v_binding->'source'->'materializedArtifactRevisions' ELSE '[]'::jsonb END
    ) WITH ORDINALITY AS reference(value, ordinal);
    SELECT COALESCE(jsonb_agg(
      jsonb_build_object(
        'fanOutNodeId', reference.value->>'fanOutNodeId',
        'id', reference.value->>'id',
        'key', reference.value->>'key'
      )
      || CASE WHEN jsonb_typeof(reference.value->'blueprint') = 'object' THEN
        jsonb_build_object('blueprint', jsonb_build_object(
          'artifactId', reference.value#>>'{blueprint,artifactId}',
          'contentHash', workflow_input_normalize_sha256(reference.value#>>'{blueprint,contentHash}'),
          'revisionId', reference.value#>>'{blueprint,revisionId}'
        ) || CASE WHEN reference.value->'blueprint' ? 'anchorId'
          THEN jsonb_build_object('anchorId', reference.value#>>'{blueprint,anchorId}') ELSE '{}'::jsonb END)
        ELSE '{}'::jsonb END
      || CASE WHEN jsonb_typeof(reference.value->'pageSpec') = 'object' THEN
        jsonb_build_object('pageSpec', jsonb_build_object(
          'artifactId', reference.value#>>'{pageSpec,artifactId}',
          'contentHash', workflow_input_normalize_sha256(reference.value#>>'{pageSpec,contentHash}'),
          'revisionId', reference.value#>>'{pageSpec,revisionId}'
        ) || CASE WHEN reference.value->'pageSpec' ? 'anchorId'
          THEN jsonb_build_object('anchorId', reference.value#>>'{pageSpec,anchorId}') ELSE '{}'::jsonb END)
        ELSE '{}'::jsonb END
      || CASE WHEN jsonb_typeof(reference.value->'prototype') = 'object' THEN
        jsonb_build_object('prototype', jsonb_build_object(
          'artifactId', reference.value#>>'{prototype,artifactId}',
          'contentHash', workflow_input_normalize_sha256(reference.value#>>'{prototype,contentHash}'),
          'revisionId', reference.value#>>'{prototype,revisionId}'
        ) || CASE WHEN reference.value->'prototype' ? 'anchorId'
          THEN jsonb_build_object('anchorId', reference.value#>>'{prototype,anchorId}') ELSE '{}'::jsonb END)
        ELSE '{}'::jsonb END
      ORDER BY reference.ordinal
    ), '[]'::jsonb) INTO v_delivery_slice_refs
    FROM jsonb_array_elements(
      CASE WHEN jsonb_typeof(v_binding->'source'->'deliverySliceRefs') = 'array'
        THEN v_binding->'source'->'deliverySliceRefs' ELSE '[]'::jsonb END
    ) WITH ORDINALITY AS reference(value, ordinal);
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
      'manifest', jsonb_build_object(
        'hash', workflow_input_normalize_sha256(pin.value#>>'{manifest,hash}'),
        'id', pin.value#>>'{manifest,id}'
      ),
      'producerDefinitionNodeId', pin.value->>'producerDefinitionNodeId',
      'producerNodeKey', pin.value->>'producerNodeKey',
      'proposal', jsonb_build_object(
        'id', pin.value#>>'{proposal,id}',
        'payloadHash', workflow_input_normalize_sha256(pin.value#>>'{proposal,payloadHash}')
      )
    ) ORDER BY pin.ordinal), '[]'::jsonb) INTO v_proposal_pins
    FROM jsonb_array_elements(
      CASE WHEN jsonb_typeof(v_binding->'source'->'proposalPins') = 'array'
        THEN v_binding->'source'->'proposalPins' ELSE '[]'::jsonb END
    ) WITH ORDINALITY AS pin(value, ordinal);
    v_input_manifest_ref := CASE
      WHEN jsonb_typeof(v_binding->'source'->'inputManifest') = 'object' THEN
        jsonb_build_object(
          'hash', workflow_input_normalize_sha256(v_binding->'source'->'inputManifest'->>'hash'),
          'id', v_binding->'source'->'inputManifest'->>'id'
        )
      ELSE 'null'::jsonb END;
    v_output_proposal_ref := CASE
      WHEN jsonb_typeof(v_binding->'source'->'outputProposal') = 'object' THEN
        jsonb_build_object(
          'id', v_binding->'source'->'outputProposal'->>'id',
          'payloadHash', workflow_input_normalize_sha256(v_binding->'source'->'outputProposal'->>'payloadHash')
        )
      ELSE 'null'::jsonb END;
    v_member := jsonb_build_object(
      'artifactRevisions', v_artifact_revisions,
      'bindingRawBytesHash', workflow_input_raw_sha256(workflow_input_canonical_jsonb_bytes(v_binding)),
      'deliverySliceRefs', v_delivery_slice_refs,
      'edgeId', v_binding->>'edgeId',
      'inputManifest', v_input_manifest_ref,
      'mappingHash', workflow_input_raw_sha256(workflow_input_canonical_jsonb_bytes(
        CASE WHEN jsonb_typeof(v_binding->'mapping') = 'object'
          THEN v_binding->'mapping' ELSE '{}'::jsonb END
      )),
      'mappingKind', CASE WHEN (
        SELECT count(*) FROM jsonb_each(
          CASE WHEN jsonb_typeof(v_binding->'mapping') = 'object'
            THEN v_binding->'mapping' ELSE '{}'::jsonb END
        )
      ) = 0 THEN 'identity' ELSE 'object-map' END,
      'mappingOrdinal', v_ordinal,
      'materializedArtifactRevisions', v_materialized_revisions,
      'outputHash', workflow_input_normalize_sha256(v_binding->>'outputHash'),
      'outputProposal', v_output_proposal_ref,
      'outputRevisionNumber', v_source_output_revision.revision_number,
      'proposalPins', v_proposal_pins,
      'sourceDefinitionNodeId', v_source_node.definition_node_id,
      'sourceNodeKey', v_source_node.node_key,
      'sourceNodeRunId', v_source_node.id::text,
      'sourceNodeType', v_source_node.node_type,
      'sourcePort', v_binding->>'fromPort',
      'sourceSliceIdentity', v_slice_identity,
      'sourceStatus', v_source_node.status,
      'targetPort', v_binding->>'toPort',
      'valueHash', workflow_input_normalize_sha256(v_binding->>'valueHash')
    );
    v_predecessors := v_predecessors || jsonb_build_array(v_member);
    v_predecessor_count := v_predecessor_count + 1;
  END LOOP;
  IF v_quality_predecessors <> 1 THEN
    RAISE EXCEPTION 'Workflow Input must have exactly one locked quality_gate predecessor'
      USING ERRCODE = 'WIA04';
  END IF;

  IF jsonb_array_length(p_candidate->'inputManifests') NOT BETWEEN 1 AND 1024
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
       WHERE jsonb_typeof(item) <> 'object'
          OR NOT (item ?& ARRAY['manifestId','rawBytesHex','role'])
          OR item - ARRAY['manifestId','rawBytesHex','role'] <> '{}'::jsonb
          OR item->>'role' NOT IN ('run','predecessor')
          OR workflow_input_uuid_is_exact(item->>'manifestId') IS NOT TRUE
          OR jsonb_typeof(item->'rawBytesHex') <> 'string'
          OR item->>'rawBytesHex' !~ '^[0-9a-f]+$'
          OR length(item->>'rawBytesHex') % 2 <> 0
          OR length(item->>'rawBytesHex') NOT BETWEEN 2 AND 16777216
     ) OR (
       SELECT count(*) FROM jsonb_array_elements(p_candidate->'inputManifests')
     ) <> (
       SELECT count(DISTINCT (item->>'role', item->>'manifestId'))
       FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
     ) THEN
    RAISE EXCEPTION 'Workflow Input manifest candidate set is invalid or duplicated'
      USING ERRCODE = 'WIA03';
  END IF;
  PERFORM 1 FROM input_manifests
  WHERE id IN (
    SELECT (item->>'manifestId')::uuid
    FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
  ) ORDER BY id FOR SHARE;

  FOR v_item IN
    SELECT item
    FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
    ORDER BY convert_to(item->>'role','UTF8'), (item->>'manifestId')::uuid
  LOOP
    v_manifest_id := (v_item->>'manifestId')::uuid;
    v_raw := decode(v_item->>'rawBytesHex','hex');
    v_retained_bytes := v_retained_bytes + octet_length(v_raw);
    IF v_retained_bytes > 134217728 THEN
      RAISE EXCEPTION 'Workflow Input aggregate retained bytes exceed the v1 bound'
        USING ERRCODE = 'WIA03';
    END IF;
    SELECT * INTO v_manifest FROM input_manifests WHERE id = v_manifest_id;
    IF v_manifest.id IS NULL OR v_manifest.project_id <> v_project.id
       OR workflow_input_raw_sha256(v_raw) <> workflow_input_normalize_sha256(v_manifest.content_hash)
       OR (
         v_item->>'role' = 'run' AND v_manifest.id <> v_run.input_manifest_id
       ) OR (
         v_item->>'role' = 'predecessor' AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(v_node_input_document->'bindings') AS binding
           WHERE binding->'source'->'inputManifest'->>'id' = v_manifest.id::text
              OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(COALESCE(binding->'source'->'proposalPins','[]'::jsonb)) AS pin
                WHERE pin->'manifest'->>'id' = v_manifest.id::text
              )
         )
       ) THEN
      RAISE EXCEPTION 'Workflow Input manifest raw bytes or role do not match locked lineage'
        USING ERRCODE = 'WIA04';
    END IF;
    v_raw_hash := workflow_input_raw_sha256(v_raw);
    v_member := jsonb_build_object(
      'contentHash', workflow_input_normalize_sha256(v_manifest.content_hash),
      'contentRef', v_manifest.content_ref,
      'contentStore', v_manifest.content_store,
      'id', v_manifest.id::text,
      'kind', v_manifest.kind,
      'manifestHash', workflow_input_normalize_sha256(v_manifest.manifest_hash),
      'projectId', v_manifest.project_id::text,
      'rawBytesHash', v_raw_hash,
      'rawBytesSize', octet_length(v_raw),
      'role', v_item->>'role',
      'schemaVersion', v_manifest.schema_version
    );
    v_manifests := v_manifests || jsonb_build_array(v_member);
    v_manifest_count := v_manifest_count + 1;
  END LOOP;
  IF (SELECT count(*) FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
      WHERE item->>'role' = 'run' AND item->>'manifestId' = v_run.input_manifest_id::text) <> 1
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_node_input_document->'bindings') AS binding
       WHERE binding->'source'->'inputManifest'->>'id' IS NOT NULL
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
           WHERE item->>'role' = 'predecessor'
             AND item->>'manifestId' = binding->'source'->'inputManifest'->>'id'
         )
     )
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_node_input_document->'bindings') AS binding
       CROSS JOIN LATERAL jsonb_array_elements(
         COALESCE(binding->'source'->'proposalPins','[]'::jsonb)
       ) AS pin
       WHERE pin->'manifest'->>'id' IS NOT NULL
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
           WHERE item->>'role' = 'predecessor'
             AND item->>'manifestId' = pin->'manifest'->>'id'
         )
     ) THEN
    RAISE EXCEPTION 'Workflow Input manifest set omits a required run/node/predecessor manifest'
      USING ERRCODE = 'WIA03';
  END IF;

  IF jsonb_array_length(p_candidate->'revisions') NOT BETWEEN 1 AND 2048
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
       WHERE jsonb_typeof(item) <> 'object'
          OR NOT (item ?& ARRAY[
            'canonicalReviewRequired','currencyPolicy','purpose','rawBytesHex','revisionId'
          ])
          OR item - ARRAY[
            'canonicalReviewRequired','currencyPolicy','purpose','rawBytesHex','revisionId'
          ] <> '{}'::jsonb
          OR jsonb_typeof(item->'canonicalReviewRequired') <> 'boolean'
          OR item->>'currencyPolicy' NOT IN ('latest-approved-required','exact-approved')
          OR jsonb_typeof(item->'purpose') <> 'string'
          OR octet_length(item->>'purpose') NOT BETWEEN 1 AND 256
          OR item->>'purpose' <> btrim(item->>'purpose')
          OR workflow_input_uuid_is_exact(item->>'revisionId') IS NOT TRUE
          OR jsonb_typeof(item->'rawBytesHex') <> 'string'
          OR item->>'rawBytesHex' !~ '^[0-9a-f]+$'
          OR length(item->>'rawBytesHex') % 2 <> 0
          OR length(item->>'rawBytesHex') NOT BETWEEN 2 AND 33554432
     ) OR (
       SELECT count(*) FROM jsonb_array_elements(p_candidate->'revisions')
     ) <> (
       SELECT count(DISTINCT (item->>'purpose', item->>'revisionId'))
       FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
     ) THEN
    RAISE EXCEPTION 'Workflow Input revision candidate set is invalid or duplicated'
      USING ERRCODE = 'WIA03';
  END IF;
  IF (SELECT count(*) FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
      WHERE item->>'purpose' = 'workspace-target'
        AND item->>'revisionId' = v_target_revision_id::text
        AND item->>'currencyPolicy' = 'latest-approved-required'
        AND item->'canonicalReviewRequired' = 'false'::jsonb) <> 1
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
       WHERE item->>'purpose' <> 'workspace-target'
         AND item->>'revisionId' = v_target_revision_id::text
     )
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
       WHERE item->>'purpose' <> 'workspace-target'
         AND NOT EXISTS (
           SELECT 1 FROM application_build_contract_sources AS source
           WHERE source.contract_id = v_build_contract.id
             AND source.purpose = item->>'purpose'
             AND source.revision_id::text = item->>'revisionId'
         )
     ) OR EXISTS (
       SELECT 1 FROM application_build_contract_sources AS source
       WHERE source.contract_id = v_build_contract.id
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
           WHERE item->>'purpose' = source.purpose
             AND item->>'revisionId' = source.revision_id::text
         )
     ) OR jsonb_array_length(p_candidate->'revisions') <> v_build_contract.source_count + 1 THEN
    RAISE EXCEPTION 'Workflow Input revision set is not exactly Workspace target plus BuildContract sources'
      USING ERRCODE = 'WIA03';
  END IF;

  FOR v_item IN
    SELECT candidate.item
    FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
    JOIN artifact_revisions AS revision ON revision.id = (candidate.item->>'revisionId')::uuid
    ORDER BY convert_to(candidate.item->>'purpose','UTF8'), revision.artifact_id, revision.id
  LOOP
    v_revision_id := (v_item->>'revisionId')::uuid;
    v_raw := decode(v_item->>'rawBytesHex','hex');
    v_retained_bytes := v_retained_bytes + octet_length(v_raw);
    IF v_retained_bytes > 134217728 THEN
      RAISE EXCEPTION 'Workflow Input aggregate retained bytes exceed the v1 bound'
        USING ERRCODE = 'WIA03';
    END IF;
    SELECT * INTO v_revision FROM artifact_revisions WHERE id = v_revision_id;
    SELECT * INTO v_artifact FROM artifacts WHERE id = v_revision.artifact_id;
    v_change_source := v_revision.change_source;
    IF v_item->>'purpose' = 'workspace-target' THEN
      v_currency_policy := v_policy_authority.revision_policy_document#>>
        '{workspaceTarget,currencyPolicy}';
      v_canonical_review_required := (
        v_policy_authority.revision_policy_document#>>
          '{workspaceTarget,canonicalReviewRequired}'
      )::boolean;
      v_source_required := false;
    ELSE
      SELECT source.required INTO v_source_required
      FROM application_build_contract_sources AS source
      WHERE source.contract_id = v_build_contract.id
        AND source.purpose = v_item->>'purpose'
        AND source.source_kind = v_artifact.kind
        AND source.artifact_id = v_artifact.id
        AND source.revision_id = v_revision.id
        AND workflow_input_normalize_sha256(source.content_hash) =
          workflow_input_normalize_sha256(v_revision.content_hash);
      SELECT rule.canonical_review_required INTO v_canonical_review_required
      FROM qualification_policy_review_defaults AS rule
      WHERE rule.authority_id = v_policy_authority.authority_id
        AND rule.change_source = v_change_source;
      IF EXISTS (
        SELECT 1
        FROM qualification_policy_exact_approved_sources AS exact_source
        WHERE exact_source.authority_id = v_policy_authority.authority_id
          AND exact_source.source_kind = v_artifact.kind
          AND exact_source.purpose = v_item->>'purpose'
          AND exact_source.artifact_id = v_artifact.id
          AND exact_source.revision_id = v_revision.id
          AND exact_source.content_hash = workflow_input_normalize_sha256(v_revision.content_hash)
      ) THEN
        v_currency_policy := 'exact-approved';
      ELSE
        v_currency_policy := v_policy_authority.revision_policy_document->>'sourceCurrencyPolicy';
      END IF;
    END IF;
    IF v_revision.id IS NULL OR v_artifact.id IS NULL
       OR v_artifact.project_id <> v_project.id OR v_artifact.lifecycle <> 'active'
       OR v_revision.workflow_status <> 'approved' OR v_revision.superseded_at IS NOT NULL
       OR v_revision.byte_size <> octet_length(v_raw)
       OR v_revision.byte_size NOT BETWEEN 1 AND 9007199254740991
       OR workflow_input_raw_sha256(v_raw) <> workflow_input_normalize_sha256(v_revision.content_hash)
       OR v_change_source IS NULL
       OR v_currency_policy IS NULL
       OR v_canonical_review_required IS NULL
       OR v_source_required IS NULL
       OR v_item->>'currencyPolicy' IS DISTINCT FROM v_currency_policy
       OR v_item->'canonicalReviewRequired' IS DISTINCT FROM
          to_jsonb(v_canonical_review_required)
       OR (v_currency_policy = 'latest-approved-required' AND (
         v_artifact.latest_revision_id IS DISTINCT FROM v_revision.id
         OR v_artifact.latest_approved_revision_id IS DISTINCT FROM v_revision.id
       ))
       OR (v_item->>'purpose' = 'workspace-target' AND (
         v_revision.id <> v_target_revision_id
         OR v_change_source <> 'system'
         OR v_canonical_review_required
         OR v_source_required
       ))
       OR (v_item->>'purpose' <> 'workspace-target' AND NOT EXISTS (
         SELECT 1 FROM application_build_contract_sources AS source
         WHERE source.contract_id = v_build_contract.id
           AND source.purpose = v_item->>'purpose'
           AND source.source_kind = v_artifact.kind
           AND source.artifact_id = v_artifact.id
           AND source.revision_id = v_revision.id
           AND workflow_input_normalize_sha256(source.content_hash) = workflow_input_normalize_sha256(v_revision.content_hash)
       )) THEN
      RAISE EXCEPTION 'Workflow Input revision bytes, source membership, approval, or currency is invalid'
        USING ERRCODE = 'WIA04';
    END IF;
    v_raw_hash := workflow_input_raw_sha256(v_raw);
    v_member := jsonb_build_object(
      'artifactId', v_artifact.id::text,
      'artifactKind', v_artifact.kind,
      'byteSize', v_revision.byte_size,
      'canonicalReviewRequired', v_canonical_review_required,
      'changeSourceAtFreeze', v_change_source,
      'contentHash', workflow_input_normalize_sha256(v_revision.content_hash),
      'contentRef', v_revision.content_ref,
      'contentStore', v_revision.content_store,
      'currencyPolicy', v_currency_policy,
      'implementationProposalId', CASE WHEN v_revision.implementation_proposal_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_revision.implementation_proposal_id::text) END,
      'isLatestApprovedAtFreeze', v_artifact.latest_approved_revision_id IS NOT DISTINCT FROM v_revision.id,
      'isLatestCurrentAtFreeze', v_artifact.latest_revision_id IS NOT DISTINCT FROM v_revision.id,
      'proposalId', CASE WHEN v_revision.proposal_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_revision.proposal_id::text) END,
      'purpose', v_item->>'purpose',
      'rawBytesHash', v_raw_hash,
      'revisionId', v_revision.id::text,
      'schemaVersion', v_revision.schema_version,
      'sourceRequiredAtFreeze', v_source_required,
      'sourceManifestId', CASE WHEN v_revision.source_manifest_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_revision.source_manifest_id::text) END,
      'workflowStatusAtFreeze', v_revision.workflow_status
    );
    v_revisions := v_revisions || jsonb_build_array(v_member);
    v_revision_count := v_revision_count + 1;
  END LOOP;

  IF EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
       CROSS JOIN LATERAL jsonb_array_elements(member->'artifactRevisions') AS item(reference)
       WHERE NOT EXISTS (
         SELECT 1 FROM jsonb_array_elements(v_revisions) AS revision(frozen)
         WHERE frozen->>'artifactId' = reference->>'artifactId'
           AND frozen->>'revisionId' = reference->>'revisionId'
           AND frozen->>'contentHash' = workflow_input_normalize_sha256(reference->>'contentHash')
       )
     ) OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
       CROSS JOIN LATERAL jsonb_array_elements(member->'materializedArtifactRevisions') AS item(reference)
       WHERE NOT EXISTS (
         SELECT 1 FROM jsonb_array_elements(member->'artifactRevisions') AS output(value)
         WHERE value->>'artifactId' = reference->>'artifactId'
           AND value->>'revisionId' = reference->>'revisionId'
           AND workflow_input_normalize_sha256(value->>'contentHash') =
               workflow_input_normalize_sha256(reference->>'contentHash')
       ) OR NOT EXISTS (
         SELECT 1 FROM jsonb_array_elements(v_revisions) AS revision(frozen)
         WHERE frozen->>'artifactId' = reference->>'artifactId'
           AND frozen->>'revisionId' = reference->>'revisionId'
           AND frozen->>'contentHash' = workflow_input_normalize_sha256(reference->>'contentHash')
       )
     ) OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
       CROSS JOIN LATERAL jsonb_array_elements(member->'deliverySliceRefs') AS item(slice)
       CROSS JOIN LATERAL jsonb_array_elements(jsonb_build_array(
         slice->'blueprint', slice->'pageSpec', slice->'prototype'
       )) AS nested(reference)
       WHERE jsonb_typeof(reference) = 'object'
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(v_revisions) AS revision(frozen)
           WHERE frozen->>'artifactId' = reference->>'artifactId'
             AND frozen->>'revisionId' = reference->>'revisionId'
             AND frozen->>'contentHash' = workflow_input_normalize_sha256(reference->>'contentHash')
         )
     ) THEN
    RAISE EXCEPTION 'Workflow Input predecessor lineage is absent from the exact frozen revision closure'
      USING ERRCODE = 'WIA04';
  END IF;

  IF jsonb_array_length(p_candidate->'reviewRequirements') > 2048
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(p_candidate->'reviewRequirements') AS candidate(item)
       WHERE jsonb_typeof(item) <> 'object'
          OR NOT (item ?& ARRAY['purpose','revisionId'])
          OR item - ARRAY['purpose','revisionId'] <> '{}'::jsonb
          OR jsonb_typeof(item->'purpose') <> 'string'
          OR octet_length(item->>'purpose') NOT BETWEEN 1 AND 256
          OR item->>'purpose' <> btrim(item->>'purpose')
          OR workflow_input_uuid_is_exact(item->>'revisionId') IS NOT TRUE
     ) OR (
       SELECT count(*) FROM jsonb_array_elements(p_candidate->'reviewRequirements')
     ) <> (
       SELECT count(DISTINCT (item->>'purpose', item->>'revisionId'))
       FROM jsonb_array_elements(p_candidate->'reviewRequirements') AS candidate(item)
     )
     OR (
       SELECT count(*) FROM jsonb_array_elements(p_candidate->'reviewRequirements')
     ) <> (
       SELECT count(DISTINCT item->>'revisionId')
       FROM jsonb_array_elements(p_candidate->'reviewRequirements') AS candidate(item)
     )
     OR jsonb_array_length(p_candidate->'reviewRequirements') <> (
       SELECT count(*)
       FROM jsonb_array_elements(v_revisions) AS revision(member)
       WHERE (member->>'canonicalReviewRequired')::boolean
     )
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_revisions) AS revision(member)
       WHERE (member->>'canonicalReviewRequired')::boolean
         AND NOT EXISTS (
           SELECT 1
           FROM jsonb_array_elements(p_candidate->'reviewRequirements') AS requirement(item)
           WHERE item->>'purpose' = member->>'purpose'
             AND item->>'revisionId' = member->>'revisionId'
         )
     )
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(p_candidate->'reviewRequirements') AS requirement(item)
       WHERE NOT EXISTS (
         SELECT 1 FROM jsonb_array_elements(v_revisions) AS revision(member)
         WHERE (member->>'canonicalReviewRequired')::boolean
           AND member->>'purpose' = item->>'purpose'
           AND member->>'revisionId' = item->>'revisionId'
       )
     ) THEN
    RAISE EXCEPTION 'Workflow Input Canonical Review requirement set must equal the policy-derived source subset'
      USING ERRCODE = 'WIA03';
  END IF;
  FOR v_item IN
    SELECT item
    FROM jsonb_array_elements(p_candidate->'reviewRequirements') AS candidate(item)
    ORDER BY convert_to(item->>'purpose','UTF8'), (item->>'revisionId')::uuid
  LOOP
    v_review_revision_id := (v_item->>'revisionId')::uuid;
    IF v_review_revision_id = v_target_revision_id
       OR NOT EXISTS (
         SELECT 1 FROM jsonb_array_elements(v_revisions) AS revision(member)
         WHERE member->>'purpose' = v_item->>'purpose'
           AND member->>'revisionId' = v_review_revision_id::text
           AND (member->>'canonicalReviewRequired')::boolean
       ) THEN
      RAISE EXCEPTION 'Workflow Input review requirement is not a frozen governed source revision'
        USING ERRCODE = 'WIA03';
    END IF;
    SELECT * INTO v_receipt
    FROM canonical_review_approval_receipts
    WHERE project_id = v_project.id AND revision_id = v_review_revision_id;
    IF v_receipt.review_request_id IS NULL THEN
      RAISE EXCEPTION 'Workflow Input required Canonical Review receipt is absent'
        USING ERRCODE = 'WIA04';
    END IF;
    SELECT * INTO v_resolved_receipt
    FROM resolve_canonical_review_approval_receipt(
      v_project.id, v_review_revision_id, v_receipt.receipt_hash
    );
    IF v_resolved_receipt.review_request_id IS NULL
       OR v_resolved_receipt.review_request_id <> v_receipt.review_request_id
       OR canonical_review_approval_receipt_record_is_exact(v_resolved_receipt) IS NOT TRUE THEN
      RAISE EXCEPTION 'Workflow Input required Canonical Review receipt is corrupt or stale'
        USING ERRCODE = 'WIA04';
    END IF;
    v_retained_bytes := v_retained_bytes + octet_length(v_resolved_receipt.receipt_bytes);
    IF v_retained_bytes > 134217728 THEN
      RAISE EXCEPTION 'Workflow Input aggregate retained bytes exceed the v1 bound'
        USING ERRCODE = 'WIA03';
    END IF;
    v_member := jsonb_build_object(
      'artifactId', v_resolved_receipt.artifact_id::text,
      'projectId', v_resolved_receipt.project_id::text,
      'purpose', v_item->>'purpose',
      'receiptHash', v_resolved_receipt.receipt_hash,
      'receiptRawBytesHash', workflow_input_raw_sha256(v_resolved_receipt.receipt_bytes),
      'receiptRawBytesSize', octet_length(v_resolved_receipt.receipt_bytes),
      'receiptSchemaVersion', 'worksflow-canonical-review-approval-receipt/v1',
      'reviewRequestId', v_resolved_receipt.review_request_id::text,
      'revisionContentHash', v_resolved_receipt.revision_content_hash,
      'revisionId', v_resolved_receipt.revision_id::text
    );
    v_receipts := v_receipts || jsonb_build_array(v_member);
    v_receipt_count := v_receipt_count + 1;
  END LOOP;

  SELECT * INTO v_revision FROM artifact_revisions WHERE id = v_target_revision_id;
  SELECT * INTO v_artifact FROM artifacts WHERE id = v_revision.artifact_id;

  -- Match the canonical package's global identity-role rule. Reuse inside one
  -- role is legitimate lineage deduplication; the same UUID carrying two
  -- different semantic roles is always an invalid authority.
  IF EXISTS (
    WITH identities(value, role) AS (
      SELECT p_authority_id, 'authority'::text
      UNION ALL SELECT p_operation_id, 'freeze-operation'
      UNION ALL SELECT p_activation_event_id, 'activation-event'
      UNION ALL SELECT v_project.id, 'project'
      UNION ALL SELECT v_run.id, 'workflow-run'
      UNION ALL SELECT v_node.id, 'node-run'
      UNION ALL SELECT v_run.started_by, 'user'
      UNION ALL SELECT v_definition.id, 'workflow-definition'
      UNION ALL SELECT v_definition_version.id, 'workflow-definition-version'
      UNION ALL SELECT v_run.input_manifest_id, 'input-manifest'
      UNION ALL SELECT v_build_manifest.id, 'build-manifest'
      UNION ALL SELECT v_build_contract.id, 'build-contract'
      UNION ALL SELECT v_policy_authority_id, 'qualification-policy-authority'
      UNION ALL SELECT v_quality_run_id, 'quality-run'
      UNION ALL SELECT v_artifact.id, 'artifact'
      UNION ALL SELECT v_target_revision_id, 'artifact-revision'
      UNION ALL
        SELECT (member->>'sourceNodeRunId')::uuid, 'node-run'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
      UNION ALL
        SELECT (member#>>'{sourceSliceIdentity,id}')::uuid, 'delivery-slice'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        WHERE member#>>'{sourceSliceIdentity,kind}' = 'slice'
      UNION ALL
        SELECT (member#>>'{inputManifest,id}')::uuid, 'input-manifest'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        WHERE jsonb_typeof(member->'inputManifest') = 'object'
      UNION ALL
        SELECT (member#>>'{outputProposal,id}')::uuid, 'output-proposal'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        WHERE jsonb_typeof(member->'outputProposal') = 'object'
      UNION ALL
        SELECT (reference->>'artifactId')::uuid, 'artifact'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        CROSS JOIN LATERAL jsonb_array_elements(
          member->'artifactRevisions' || member->'materializedArtifactRevisions'
        ) AS item(reference)
      UNION ALL
        SELECT (reference->>'revisionId')::uuid, 'artifact-revision'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        CROSS JOIN LATERAL jsonb_array_elements(
          member->'artifactRevisions' || member->'materializedArtifactRevisions'
        ) AS item(reference)
      UNION ALL
        SELECT (pin#>>'{manifest,id}')::uuid, 'input-manifest'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        CROSS JOIN LATERAL jsonb_array_elements(member->'proposalPins') AS item(pin)
      UNION ALL
        SELECT (pin#>>'{proposal,id}')::uuid, 'output-proposal'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        CROSS JOIN LATERAL jsonb_array_elements(member->'proposalPins') AS item(pin)
      UNION ALL
        SELECT (slice->>'id')::uuid, 'delivery-slice'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        CROSS JOIN LATERAL jsonb_array_elements(member->'deliverySliceRefs') AS item(slice)
      UNION ALL
        SELECT (reference->>'artifactId')::uuid, 'artifact'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        CROSS JOIN LATERAL jsonb_array_elements(member->'deliverySliceRefs') AS item(slice)
        CROSS JOIN LATERAL jsonb_array_elements(jsonb_build_array(
          slice->'blueprint', slice->'pageSpec', slice->'prototype'
        )) AS nested(reference)
        WHERE jsonb_typeof(reference) = 'object'
      UNION ALL
        SELECT (reference->>'revisionId')::uuid, 'artifact-revision'
        FROM jsonb_array_elements(v_predecessors) AS predecessor(member)
        CROSS JOIN LATERAL jsonb_array_elements(member->'deliverySliceRefs') AS item(slice)
        CROSS JOIN LATERAL jsonb_array_elements(jsonb_build_array(
          slice->'blueprint', slice->'pageSpec', slice->'prototype'
        )) AS nested(reference)
        WHERE jsonb_typeof(reference) = 'object'
      UNION ALL
        SELECT (member->>'id')::uuid, 'input-manifest'
        FROM jsonb_array_elements(v_manifests) AS manifest(member)
      UNION ALL
        SELECT (member->>'artifactId')::uuid, 'artifact'
        FROM jsonb_array_elements(v_revisions) AS revision(member)
      UNION ALL
        SELECT (member->>'revisionId')::uuid, 'artifact-revision'
        FROM jsonb_array_elements(v_revisions) AS revision(member)
      UNION ALL
        SELECT (member->>'sourceManifestId')::uuid, 'input-manifest'
        FROM jsonb_array_elements(v_revisions) AS revision(member)
        WHERE jsonb_typeof(member->'sourceManifestId') = 'string'
      UNION ALL
        SELECT (member->>'proposalId')::uuid, 'output-proposal'
        FROM jsonb_array_elements(v_revisions) AS revision(member)
        WHERE jsonb_typeof(member->'proposalId') = 'string'
      UNION ALL
        SELECT (member->>'implementationProposalId')::uuid, 'implementation-proposal'
        FROM jsonb_array_elements(v_revisions) AS revision(member)
        WHERE jsonb_typeof(member->'implementationProposalId') = 'string'
      UNION ALL
        SELECT (member->>'reviewRequestId')::uuid, 'review-request'
        FROM jsonb_array_elements(v_receipts) AS receipt(member)
    )
    SELECT 1
    FROM identities
    WHERE value IS NOT NULL
    GROUP BY value
    HAVING count(DISTINCT role) > 1
  ) THEN
    RAISE EXCEPTION 'Workflow Input UUID is reused across distinct semantic identity roles'
      USING ERRCODE = 'WIA03';
  END IF;

  v_slice_identity := jsonb_build_object('kind', 'root');
  v_request_document := jsonb_build_object(
    'authorityId', p_authority_id::text,
    'expectedRunCursor', p_expected_run_cursor,
    'mediaType', 'application/vnd.worksflow.workflow-input-freeze-request+json;version=1',
    'nodeKey', v_node.node_key,
    'nodeRunId', v_node.id::text,
    'operationId', p_operation_id::text,
    'projectId', v_project.id::text,
    'schemaVersion', 'worksflow-workflow-input-freeze-request/v1',
    'workflowRunId', v_run.id::text
  );
  v_target_document := jsonb_build_object(
    'manifestSubject', p_candidate->>'manifestSubject',
    'nodeKey', v_node.node_key,
    'projectId', v_project.id::text,
    'stageGate', 'external-qualification',
    'targetRevisionContentHash', workflow_input_normalize_sha256(v_revision.content_hash),
    'targetRevisionId', v_target_revision_id::text,
    'workflowRunId', v_run.id::text
  );
  v_request_bytes := workflow_input_canonical_jsonb_bytes(v_request_document);
  v_request_hash := workflow_input_authority_hash(
    'worksflow.workflow-input.freeze-request/v1', v_request_bytes
  );
  v_target_bytes := workflow_input_canonical_jsonb_bytes(v_target_document);
  v_target_hash := workflow_input_authority_hash(
    'worksflow.workflow-input.target/v1', v_target_bytes
  );
  v_input_document := jsonb_build_object(
    'build', jsonb_build_object(
      'buildContract', jsonb_build_object(
        'contentHash', workflow_input_normalize_sha256(v_build_contract.content_hash),
        'contractHash', workflow_input_normalize_sha256(v_build_contract.contract_hash),
        'id', v_build_contract.id::text,
        'rawBytesHash', workflow_input_raw_sha256(p_build_contract_raw_bytes),
        'rawBytesSize', octet_length(p_build_contract_raw_bytes),
        'statusAtFreeze', v_build_contract.status
      ),
      'buildManifest', jsonb_build_object(
        'contentHash', workflow_input_normalize_sha256(v_build_manifest.content_hash),
        'id', v_build_manifest.id::text,
        'manifestHash', workflow_input_normalize_sha256(v_build_manifest.manifest_hash),
        'rawBytesHash', workflow_input_raw_sha256(p_build_manifest_raw_bytes),
        'rawBytesSize', octet_length(p_build_manifest_raw_bytes),
        'statusAtFreeze', v_build_manifest.status
      )
    ),
    'definition', jsonb_build_object(
      'definitionHash', workflow_input_normalize_sha256(v_definition_version.content_hash),
      'definitionId', v_definition.id::text,
      'definitionVersion', v_definition_version.version,
      'definitionVersionId', v_definition_version.id::text,
      'executionProfileHash', workflow_input_normalize_sha256(v_definition_version.execution_profile_hash),
      'executionProfileVersion', v_definition_version.execution_profile_version,
      'rawBytesHash', workflow_input_raw_sha256(p_definition_raw_bytes),
      'rawBytesSize', octet_length(p_definition_raw_bytes)
    ),
    'gate', jsonb_build_object(
      'activationEventId', p_activation_event_id::text,
      'activationEventSequence', p_activation_event_sequence,
      'definitionNodeId', v_node.definition_node_id,
      'gateName', 'external-qualification',
      'nodeKey', v_node.node_key,
      'nodeRunId', v_node.id::text,
      'nodeType', v_node.node_type,
      'sliceIdentity', v_slice_identity,
      'stageGate', 'external-qualification'
    ),
    'inputManifests', v_manifests,
    'mediaType', 'application/vnd.worksflow.workflow-input+json;version=1',
    'nodeInput', jsonb_build_object(
      'bindingCount', v_predecessor_count,
      'rawBytesHash', workflow_input_raw_sha256(p_node_input_raw_bytes),
      'rawBytesSize', octet_length(p_node_input_raw_bytes),
      'semanticHash', workflow_input_normalize_sha256(v_node_input_document->>'hash')
    ),
    'predecessors', v_predecessors,
    'project', jsonb_build_object('governanceMode', v_project.governance_mode, 'id', v_project.id::text),
    'qualificationPolicy', v_policy,
    'qualityResult', jsonb_build_object(
      'buildManifestHash', workflow_input_normalize_sha256(v_build_manifest.manifest_hash),
      'buildManifestId', v_build_manifest.id::text,
      'passed', true,
      'qualityRunId', v_quality_run_id::text,
      'workspaceRevisionContentHash', workflow_input_normalize_sha256(v_revision.content_hash),
      'workspaceRevisionId', v_target_revision_id::text
    ),
    'reviewReceipts', v_receipts,
    'revisions', v_revisions,
    'run', jsonb_build_object(
      'id', v_run.id::text,
      'inputManifestHash', workflow_input_normalize_sha256((
        SELECT manifest_hash FROM input_manifests WHERE id = v_run.input_manifest_id
      )),
      'inputManifestId', v_run.input_manifest_id::text,
      'scopeRawBytesHash', workflow_input_raw_sha256(p_run_scope_raw_bytes),
      'scopeRawBytesSize', octet_length(p_run_scope_raw_bytes),
      'startedAt', to_char(v_run.started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
      'startedBy', v_run.started_by::text
    ),
    'schemaVersion', 'worksflow-workflow-input/v1',
    'target', v_target_document,
    'targetHash', v_target_hash
  );
  v_input_bytes := workflow_input_canonical_jsonb_bytes(v_input_document);
  v_input_hash := workflow_input_authority_hash(
    'worksflow.workflow-input.input/v1', v_input_bytes
  );
  v_authority_document := jsonb_build_object(
    'authorityId', p_authority_id::text,
    'inputHash', v_input_hash,
    'mediaType', 'application/vnd.worksflow.workflow-input-authority+json;version=1',
    'nodeRunId', v_node.id::text,
    'operationId', p_operation_id::text,
    'projectId', v_project.id::text,
    'requestHash', v_request_hash,
    'schemaVersion', 'worksflow-workflow-input-authority/v1',
    'targetHash', v_target_hash,
    'workflowRunId', v_run.id::text
  );
  v_authority_bytes := workflow_input_canonical_jsonb_bytes(v_authority_document);
  v_authority_hash := workflow_input_authority_hash(
    'worksflow.workflow-input.authority/v1', v_authority_bytes
  );

  v_now := GREATEST(
    date_trunc('milliseconds', clock_timestamp()),
    date_trunc('milliseconds', v_run.started_at)
  );

  INSERT INTO workflow_input_authorities (
    authority_id, operation_id,
    request_hash, request_bytes, request_document,
    input_hash, input_bytes, input_document,
    target_hash, target_bytes, target_document,
    authority_hash, authority_bytes, authority_document,
    project_id, governance_mode,
    definition_id, definition_version_id, definition_version, definition_hash,
    definition_raw_bytes, definition_raw_bytes_hash, definition_raw_bytes_size,
    execution_profile_version, execution_profile_hash,
    workflow_run_id, run_input_manifest_id, run_input_manifest_hash,
    run_scope_raw_bytes, run_scope_raw_bytes_hash, run_scope_raw_bytes_size,
    run_started_at, run_started_by,
    node_run_id, node_key, node_type, definition_node_id, slice_kind, slice_id,
    gate_name, stage_gate, activation_event_id, activation_event_sequence,
    node_input_raw_bytes, node_input_raw_bytes_hash, node_input_raw_bytes_size,
    node_input_semantic_hash, node_input_binding_count,
    manifest_subject, target_artifact_id, target_revision_id, target_revision_content_hash,
    quality_run_id, quality_passed, quality_workspace_revision_id,
    quality_workspace_revision_content_hash,
    build_manifest_id, build_manifest_hash, build_manifest_content_hash,
    build_manifest_status, build_manifest_raw_bytes, build_manifest_raw_bytes_hash,
    build_manifest_raw_bytes_size,
    build_contract_id, build_contract_hash, build_contract_content_hash,
    build_contract_status, build_contract_raw_bytes, build_contract_raw_bytes_hash,
    build_contract_raw_bytes_size,
    qualification_policy_authority_id, qualification_policy_authority_hash,
    external_gate_policy,
    predecessor_count, manifest_count, revision_count, review_receipt_count, frozen_at
  ) VALUES (
    p_authority_id, p_operation_id,
    v_request_hash, v_request_bytes, v_request_document,
    v_input_hash, v_input_bytes, v_input_document,
    v_target_hash, v_target_bytes, v_target_document,
    v_authority_hash, v_authority_bytes, v_authority_document,
    v_project.id, v_project.governance_mode,
    v_definition.id, v_definition_version.id, v_definition_version.version,
    workflow_input_normalize_sha256(v_definition_version.content_hash),
    p_definition_raw_bytes, workflow_input_raw_sha256(p_definition_raw_bytes), octet_length(p_definition_raw_bytes),
    v_definition_version.execution_profile_version,
    workflow_input_normalize_sha256(v_definition_version.execution_profile_hash),
    v_run.id, v_run.input_manifest_id, workflow_input_normalize_sha256((
      SELECT manifest_hash FROM input_manifests WHERE id = v_run.input_manifest_id
    )),
    p_run_scope_raw_bytes, workflow_input_raw_sha256(p_run_scope_raw_bytes), octet_length(p_run_scope_raw_bytes),
    date_trunc('milliseconds', v_run.started_at), v_run.started_by,
    v_node.id, v_node.node_key, v_node.node_type, v_node.definition_node_id,
    v_node.slice_kind, v_node.slice_id,
    'external-qualification', 'external-qualification',
    p_activation_event_id, p_activation_event_sequence,
    p_node_input_raw_bytes, workflow_input_raw_sha256(p_node_input_raw_bytes), octet_length(p_node_input_raw_bytes),
    workflow_input_normalize_sha256(v_node_input_document->>'hash'), v_predecessor_count,
    p_candidate->>'manifestSubject', v_artifact.id, v_revision.id,
    workflow_input_normalize_sha256(v_revision.content_hash),
    v_quality_run_id, true, v_revision.id, workflow_input_normalize_sha256(v_revision.content_hash),
    v_build_manifest.id, workflow_input_normalize_sha256(v_build_manifest.manifest_hash),
    workflow_input_normalize_sha256(v_build_manifest.content_hash), v_build_manifest.status,
    p_build_manifest_raw_bytes, workflow_input_raw_sha256(p_build_manifest_raw_bytes),
    octet_length(p_build_manifest_raw_bytes),
    v_build_contract.id, workflow_input_normalize_sha256(v_build_contract.contract_hash),
    workflow_input_normalize_sha256(v_build_contract.content_hash), v_build_contract.status,
    p_build_contract_raw_bytes, workflow_input_raw_sha256(p_build_contract_raw_bytes),
    octet_length(p_build_contract_raw_bytes),
    v_policy_authority_id, v_policy->>'authorityHash', v_policy->>'externalGatePolicy',
    v_predecessor_count, v_manifest_count, v_revision_count, v_receipt_count, v_now
  );

  INSERT INTO workflow_input_authority_identity_reservations(
    identity_value, authority_id, identity_kind, ordinal, reserved_at
  ) VALUES
    (p_authority_id, p_authority_id, 'authority', 0, v_now),
    (p_operation_id, p_authority_id, 'freeze-operation', 0, v_now),
    (p_activation_event_id, p_authority_id, 'activation-event', 0, v_now);

  FOR v_member, v_ordinal IN
    SELECT member, (ordinality - 1)::integer
    FROM jsonb_array_elements(v_predecessors) WITH ORDINALITY AS item(member, ordinality)
  LOOP
    INSERT INTO workflow_input_authority_predecessors (
      authority_id, ordinal, edge_id, source_node_run_id, source_node_key,
      source_definition_node_id, source_node_type, source_slice_kind, source_slice_id,
      source_status, output_revision_number, source_port, target_port,
      mapping_kind, mapping_ordinal, output_hash, value_hash, member_document
    ) VALUES (
      p_authority_id, v_ordinal, v_member->>'edgeId', (v_member->>'sourceNodeRunId')::uuid,
      v_member->>'sourceNodeKey', v_member->>'sourceDefinitionNodeId',
      v_member->>'sourceNodeType', v_member->'sourceSliceIdentity'->>'kind',
      CASE WHEN v_member->'sourceSliceIdentity'->>'kind' = 'slice'
        THEN (v_member->'sourceSliceIdentity'->>'id')::uuid ELSE NULL END,
      v_member->>'sourceStatus', (v_member->>'outputRevisionNumber')::bigint,
      v_member->>'sourcePort', v_member->>'targetPort', v_member->>'mappingKind',
      (v_member->>'mappingOrdinal')::integer, v_member->>'outputHash',
      v_member->>'valueHash', v_member
    );
  END LOOP;

  v_ordinal := 0;
  FOR v_item IN
    SELECT item
    FROM jsonb_array_elements(p_candidate->'inputManifests') AS candidate(item)
    ORDER BY convert_to(item->>'role','UTF8'), (item->>'manifestId')::uuid
  LOOP
    v_manifest_id := (v_item->>'manifestId')::uuid;
    v_raw := decode(v_item->>'rawBytesHex','hex');
    SELECT * INTO v_manifest FROM input_manifests WHERE id = v_manifest_id;
    v_member := v_manifests->v_ordinal;
    INSERT INTO workflow_input_authority_manifests (
      authority_id, ordinal, role, manifest_id, project_id, kind, schema_version,
      manifest_hash, content_store, content_ref, content_hash,
      raw_bytes, raw_bytes_hash, raw_bytes_size, member_document
    ) VALUES (
      p_authority_id, v_ordinal, v_item->>'role', v_manifest.id, v_manifest.project_id,
      v_manifest.kind, v_manifest.schema_version,
      workflow_input_normalize_sha256(v_manifest.manifest_hash),
      v_manifest.content_store, v_manifest.content_ref,
      workflow_input_normalize_sha256(v_manifest.content_hash),
      v_raw, workflow_input_raw_sha256(v_raw), octet_length(v_raw), v_member
    );
    v_ordinal := v_ordinal + 1;
  END LOOP;

  v_ordinal := 0;
  FOR v_item IN
    SELECT candidate.item
    FROM jsonb_array_elements(p_candidate->'revisions') AS candidate(item)
    JOIN artifact_revisions AS revision ON revision.id = (candidate.item->>'revisionId')::uuid
    ORDER BY convert_to(candidate.item->>'purpose','UTF8'), revision.artifact_id, revision.id
  LOOP
    v_revision_id := (v_item->>'revisionId')::uuid;
    v_raw := decode(v_item->>'rawBytesHex','hex');
    SELECT * INTO v_revision FROM artifact_revisions WHERE id = v_revision_id;
    SELECT * INTO v_artifact FROM artifacts WHERE id = v_revision.artifact_id;
    v_member := v_revisions->v_ordinal;
    INSERT INTO workflow_input_authority_revisions (
      authority_id, ordinal, purpose, artifact_kind, artifact_id, revision_id,
      content_hash, content_store, content_ref, schema_version, byte_size,
      raw_bytes, raw_bytes_hash, workflow_status_at_freeze, source_manifest_id,
      proposal_id, implementation_proposal_id, was_latest_current,
      was_latest_approved, canonical_review_required, change_source_at_freeze,
      source_required_at_freeze, currency_policy, member_document
    ) VALUES (
      p_authority_id, v_ordinal, v_item->>'purpose', v_artifact.kind, v_artifact.id,
      v_revision.id, workflow_input_normalize_sha256(v_revision.content_hash),
      v_revision.content_store, v_revision.content_ref, v_revision.schema_version,
      v_revision.byte_size, v_raw, workflow_input_raw_sha256(v_raw),
      v_revision.workflow_status, v_revision.source_manifest_id, v_revision.proposal_id,
      v_revision.implementation_proposal_id,
      v_artifact.latest_revision_id IS NOT DISTINCT FROM v_revision.id,
      v_artifact.latest_approved_revision_id IS NOT DISTINCT FROM v_revision.id,
      (v_member->>'canonicalReviewRequired')::boolean,
      v_member->>'changeSourceAtFreeze',
      (v_member->>'sourceRequiredAtFreeze')::boolean,
      v_member->>'currencyPolicy', v_member
    );
    v_ordinal := v_ordinal + 1;
  END LOOP;

  v_ordinal := 0;
  FOR v_item IN
    SELECT item
    FROM jsonb_array_elements(p_candidate->'reviewRequirements') AS candidate(item)
    ORDER BY convert_to(item->>'purpose','UTF8'), (item->>'revisionId')::uuid
  LOOP
    v_review_revision_id := (v_item->>'revisionId')::uuid;
    SELECT * INTO v_receipt
    FROM canonical_review_approval_receipts
    WHERE project_id = v_project.id AND revision_id = v_review_revision_id;
    SELECT * INTO v_resolved_receipt
    FROM resolve_canonical_review_approval_receipt(
      v_project.id, v_review_revision_id, v_receipt.receipt_hash
    );
    v_member := v_receipts->v_ordinal;
    INSERT INTO workflow_input_authority_review_receipts (
      authority_id, ordinal, purpose, review_request_id, receipt_hash,
      receipt_bytes, receipt_document, receipt_raw_bytes_hash,
      receipt_raw_bytes_size, project_id, artifact_id, revision_id,
      revision_content_hash, receipt_schema_version, member_document
    ) VALUES (
      p_authority_id, v_ordinal, v_item->>'purpose', v_resolved_receipt.review_request_id,
      v_resolved_receipt.receipt_hash, v_resolved_receipt.receipt_bytes,
      v_resolved_receipt.receipt_document,
      workflow_input_raw_sha256(v_resolved_receipt.receipt_bytes),
      octet_length(v_resolved_receipt.receipt_bytes), v_resolved_receipt.project_id,
      v_resolved_receipt.artifact_id, v_resolved_receipt.revision_id,
      v_resolved_receipt.revision_content_hash,
      'worksflow-canonical-review-approval-receipt/v1', v_member
    );
    v_ordinal := v_ordinal + 1;
  END LOOP;

  IF workflow_input_authority_record_is_exact(p_authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input database-authored authority failed independent relational verification'
      USING ERRCODE = 'WIA03';
  END IF;
  RETURN QUERY SELECT * FROM workflow_input_authorities WHERE authority_id = p_authority_id;
  RETURN;
EXCEPTION
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation
    OR serialization_failure OR deadlock_detected OR numeric_value_out_of_range THEN
    RAISE EXCEPTION 'Workflow Input freeze conflicts with an exact identity or locked fact: %', SQLERRM
      USING ERRCODE = 'WIA01';
END;
$function$;

CREATE FUNCTION workflow_input_authority_bundle_v1(p_authority_id uuid)
RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
AS $function$
DECLARE
  v_authority workflow_input_authorities%ROWTYPE;
  v_reservations jsonb;
  v_predecessors jsonb;
  v_manifests jsonb;
  v_revisions jsonb;
  v_receipts jsonb;
BEGIN
  SELECT * INTO v_authority
  FROM workflow_input_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  IF workflow_input_authority_record_is_exact(v_authority.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input Authority durable aggregate is corrupt'
      USING ERRCODE = 'WIA02';
  END IF;
  SELECT COALESCE(jsonb_agg(to_jsonb(member) ORDER BY member.identity_kind), '[]'::jsonb)
    INTO v_reservations
  FROM workflow_input_authority_identity_reservations AS member
  WHERE member.authority_id = v_authority.authority_id;
  SELECT COALESCE(jsonb_agg(to_jsonb(member) ORDER BY member.ordinal), '[]'::jsonb)
    INTO v_predecessors
  FROM workflow_input_authority_predecessors AS member
  WHERE member.authority_id = v_authority.authority_id;
  SELECT COALESCE(jsonb_agg(to_jsonb(member) ORDER BY member.ordinal), '[]'::jsonb)
    INTO v_manifests
  FROM workflow_input_authority_manifests AS member
  WHERE member.authority_id = v_authority.authority_id;
  SELECT COALESCE(jsonb_agg(to_jsonb(member) ORDER BY member.ordinal), '[]'::jsonb)
    INTO v_revisions
  FROM workflow_input_authority_revisions AS member
  WHERE member.authority_id = v_authority.authority_id;
  SELECT COALESCE(jsonb_agg(to_jsonb(member) ORDER BY member.ordinal), '[]'::jsonb)
    INTO v_receipts
  FROM workflow_input_authority_review_receipts AS member
  WHERE member.authority_id = v_authority.authority_id;
  RETURN jsonb_build_object(
    'authority', to_jsonb(v_authority) - 'creation_transaction_id',
    'identityReservations', v_reservations,
    'inputManifests', v_manifests,
    'predecessors', v_predecessors,
    'reviewReceipts', v_receipts,
    'revisions', v_revisions
  );
END;
$function$;

CREATE FUNCTION inspect_workflow_input_authority_operation_v1(p_operation_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority workflow_input_authorities%ROWTYPE;
BEGIN
  IF p_operation_id IS NULL OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input operation lookup is invalid' USING ERRCODE = 'WIA03';
  END IF;
  SELECT * INTO v_authority FROM workflow_input_authorities WHERE operation_id = p_operation_id;
  IF v_authority.authority_id IS NULL THEN RETURN; END IF;
  RETURN NEXT workflow_input_authority_bundle_v1(v_authority.authority_id);
END;
$function$;

CREATE FUNCTION resolve_workflow_input_authority_v1(p_authority_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority workflow_input_authorities%ROWTYPE;
BEGIN
  IF p_authority_id IS NULL OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input authority lookup is invalid' USING ERRCODE = 'WIA03';
  END IF;
  SELECT * INTO v_authority FROM workflow_input_authorities WHERE authority_id = p_authority_id;
  IF v_authority.authority_id IS NULL THEN RETURN; END IF;
  RETURN NEXT workflow_input_authority_bundle_v1(v_authority.authority_id);
END;
$function$;

CREATE FUNCTION resolve_workflow_input_authority_for_node_v1(
  p_workflow_run_id uuid,
  p_node_run_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
AS $function$
DECLARE
  v_authority workflow_input_authorities%ROWTYPE;
BEGIN
  IF p_workflow_run_id IS NULL OR workflow_input_uuid_is_exact(p_workflow_run_id::text) IS NOT TRUE
     OR p_node_run_id IS NULL OR workflow_input_uuid_is_exact(p_node_run_id::text) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input node lookup is invalid' USING ERRCODE = 'WIA03';
  END IF;
  SELECT * INTO v_authority FROM workflow_input_authorities
  WHERE workflow_run_id = p_workflow_run_id AND node_run_id = p_node_run_id;
  IF v_authority.authority_id IS NULL THEN RETURN; END IF;
  RETURN NEXT workflow_input_authority_bundle_v1(v_authority.authority_id);
END;
$function$;

CREATE FUNCTION assert_current_workflow_input_authority_v1(p_authority_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_authority workflow_input_authorities%ROWTYPE;
  v_policy_authority qualification_policy_authorities%ROWTYPE;
  v_project_id uuid;
BEGIN
  IF p_authority_id IS NULL OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input current-authority assertion is invalid'
      USING ERRCODE = 'WIA03';
  END IF;
  -- Promotion takes the same shared runtime fence as Freeze before its first
  -- relation read. Rolling migration/down transactions take the exclusive
  -- side before locking projects, so neither path can invert WIA/project lock
  -- order during a deploy.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  SELECT project_id INTO v_project_id
  FROM workflow_input_authorities WHERE authority_id = p_authority_id;
  IF v_project_id IS NULL THEN RETURN; END IF;
  PERFORM 1 FROM projects WHERE id = v_project_id FOR UPDATE;
  SELECT * INTO v_authority
  FROM workflow_input_authorities WHERE authority_id = p_authority_id FOR SHARE;
  SELECT * INTO v_policy_authority
  FROM qualification_policy_authorities
  WHERE authority_id = v_authority.qualification_policy_authority_id
  FOR UPDATE;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended('delivery_slices:' || v_project_id::text, 0)
  );

  -- Promotion must call this function inside its caller-owned consume
  -- transaction. Lock every mutable current fact in the platform lock order
  -- before revalidation, and keep those locks until the receipt/authority
  -- consumption and gate transition commit together.
  PERFORM 1 FROM workflow_runs
  WHERE id = v_authority.workflow_run_id
  FOR SHARE;
  PERFORM 1 FROM workflow_node_runs
  WHERE id = v_authority.node_run_id
     OR id IN (
       SELECT source_node_run_id
       FROM workflow_input_authority_predecessors
       WHERE authority_id = v_authority.authority_id
     )
  ORDER BY id FOR SHARE;
  PERFORM 1 FROM workflow_run_events
  WHERE id = v_authority.activation_event_id
  FOR SHARE;
  PERFORM 1 FROM workflow_definition_versions
  WHERE id = v_authority.definition_version_id
  FOR SHARE;
  PERFORM 1 FROM workflow_definitions
  WHERE id = v_authority.definition_id
  FOR SHARE;
  PERFORM 1 FROM delivery_slices
  WHERE id IN (
    SELECT (slice.value->>'id')::uuid
    FROM workflow_input_authority_predecessors AS predecessor
    CROSS JOIN LATERAL jsonb_array_elements(predecessor.member_document->'deliverySliceRefs') AS slice(value)
    WHERE predecessor.authority_id = v_authority.authority_id
  )
  ORDER BY id FOR SHARE;
  PERFORM 1 FROM artifacts
  WHERE id IN (
    SELECT artifact_id FROM workflow_input_authority_revisions
    WHERE authority_id = v_authority.authority_id
  )
  ORDER BY id FOR SHARE;
  PERFORM 1 FROM artifact_revisions
  WHERE id IN (
    SELECT revision_id FROM workflow_input_authority_revisions
    WHERE authority_id = v_authority.authority_id
  )
  ORDER BY id FOR SHARE;
  PERFORM 1 FROM implementation_proposals
  WHERE id = (
    SELECT implementation_proposal_id
    FROM artifact_revisions WHERE id = v_authority.target_revision_id
  )
  FOR SHARE;
  PERFORM 1 FROM quality_runs
  WHERE id = v_authority.quality_run_id
  FOR SHARE;
  PERFORM 1 FROM application_build_manifests
  WHERE id = v_authority.build_manifest_id
  FOR SHARE;
  PERFORM 1 FROM application_build_contracts
  WHERE id = v_authority.build_contract_id
  FOR SHARE;
  PERFORM 1 FROM application_build_contract_sources
  WHERE contract_id = v_authority.build_contract_id
  ORDER BY ordinal FOR SHARE;
  PERFORM 1 FROM application_build_contract_template_releases
  WHERE contract_id = v_authority.build_contract_id
  ORDER BY ordinal FOR SHARE;
  PERFORM 1 FROM application_build_contract_obligations
  WHERE contract_id = v_authority.build_contract_id
  ORDER BY obligation_id FOR SHARE;
  PERFORM 1 FROM input_manifests
  WHERE id IN (
    SELECT manifest_id FROM workflow_input_authority_manifests
    WHERE authority_id = v_authority.authority_id
  )
  ORDER BY id FOR SHARE;
  PERFORM 1 FROM output_proposals
  WHERE id IN (
    SELECT (member_document#>>'{outputProposal,id}')::uuid
    FROM workflow_input_authority_predecessors
    WHERE authority_id = v_authority.authority_id
      AND jsonb_typeof(member_document->'outputProposal') = 'object'
    UNION
    SELECT (pin.value#>>'{proposal,id}')::uuid
    FROM workflow_input_authority_predecessors AS predecessor
    CROSS JOIN LATERAL jsonb_array_elements(predecessor.member_document->'proposalPins') AS pin(value)
    WHERE predecessor.authority_id = v_authority.authority_id
  )
  ORDER BY id FOR SHARE;
  PERFORM 1 FROM canonical_review_approval_receipts
  WHERE review_request_id IN (
    SELECT review_request_id FROM workflow_input_authority_review_receipts
    WHERE authority_id = v_authority.authority_id
  )
  ORDER BY review_request_id FOR SHARE;
  IF v_authority.authority_id IS NULL THEN
    RETURN;
  END IF;
  IF workflow_input_authority_record_is_exact(v_authority.authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input Authority durable aggregate is corrupt'
      USING ERRCODE = 'WIA02';
  END IF;
  IF v_policy_authority.authority_id IS NULL
     OR v_policy_authority.project_id <> v_authority.project_id
     OR v_policy_authority.execution_profile_version <> v_authority.execution_profile_version
     OR v_policy_authority.execution_profile_hash <> v_authority.execution_profile_hash
     OR v_policy_authority.authority_hash <> v_authority.qualification_policy_authority_hash
     OR v_policy_authority.status <> 'active'
     OR qualification_policy_authority_record_is_exact_v1(
       v_policy_authority.authority_id
     ) IS NOT TRUE
     OR EXISTS (
       SELECT 1 FROM qualification_policy_authorities AS newer
       WHERE newer.project_id = v_policy_authority.project_id
         AND newer.execution_profile_version = v_policy_authority.execution_profile_version
         AND newer.execution_profile_hash = v_policy_authority.execution_profile_hash
         AND newer.generation > v_policy_authority.generation
     ) THEN
    RAISE EXCEPTION 'Workflow Input qualification policy is no longer current and active'
      USING ERRCODE = 'WIA04';
  END IF;
  IF NOT EXISTS (
       SELECT 1 FROM projects
       WHERE id = v_authority.project_id
         AND lifecycle = 'active'
         AND governance_mode = v_authority.governance_mode
     )
     OR NOT EXISTS (
       SELECT 1 FROM workflow_runs AS run
       JOIN workflow_definition_versions AS version ON version.id = run.definition_version_id
       WHERE run.id = v_authority.workflow_run_id
         AND run.project_id = v_authority.project_id
         AND run.event_cursor >= v_authority.activation_event_sequence
         AND run.execution_profile_version = v_authority.execution_profile_version
         AND workflow_input_normalize_sha256(run.execution_profile_hash) = v_authority.execution_profile_hash
         AND version.id = v_authority.definition_version_id
         AND version.definition_id = v_authority.definition_id
         AND version.version = v_authority.definition_version
         AND workflow_input_normalize_sha256(version.content_hash) = v_authority.definition_hash
     )
     OR NOT EXISTS (
       SELECT 1 FROM workflow_node_runs AS node
       WHERE node.id = v_authority.node_run_id
         AND node.run_id = v_authority.workflow_run_id
         AND node.node_key = v_authority.node_key
         AND node.node_type = v_authority.node_type
         AND node.definition_node_id = v_authority.definition_node_id
         AND node.slice_kind = v_authority.slice_kind
         AND node.slice_id IS NOT DISTINCT FROM v_authority.slice_id
         AND node.input_authority_id = v_authority.authority_id
         AND node.status = 'waiting_qualification'
     )
     OR NOT EXISTS (
       SELECT 1 FROM workflow_run_events AS event
       WHERE event.id = v_authority.activation_event_id
         AND event.run_id = v_authority.workflow_run_id
         AND event.sequence = v_authority.activation_event_sequence
         AND event.event_type = 'external_qualification_activated'
         AND event.node_key = v_authority.node_key
         AND event.payload = jsonb_build_object(
           'inputAuthorityId', v_authority.authority_id::text,
           'nodeRunId', v_authority.node_run_id::text
         )
     )
     OR NOT EXISTS (
       SELECT 1 FROM artifacts AS artifact
       JOIN artifact_revisions AS revision ON revision.artifact_id = artifact.id
       WHERE artifact.id = v_authority.target_artifact_id
         AND artifact.project_id = v_authority.project_id
         AND artifact.lifecycle = 'active'
         AND artifact.latest_revision_id = v_authority.target_revision_id
         AND artifact.latest_approved_revision_id = v_authority.target_revision_id
         AND revision.id = v_authority.target_revision_id
         AND revision.workflow_status = 'approved'
         AND revision.superseded_at IS NULL
         AND workflow_input_normalize_sha256(revision.content_hash) = v_authority.target_revision_content_hash
     )
     OR NOT EXISTS (
       SELECT 1 FROM quality_runs AS quality
       WHERE quality.id = v_authority.quality_run_id
         AND quality.project_id = v_authority.project_id
         AND quality.workflow_run_id = v_authority.workflow_run_id
         AND quality.workspace_artifact_id = v_authority.target_artifact_id
         AND quality.workspace_revision_id = v_authority.target_revision_id
         AND workflow_input_normalize_sha256(quality.workspace_content_hash) =
             v_authority.target_revision_content_hash
         AND quality.status = 'passed'
         AND quality.completed_at IS NOT NULL
         AND EXISTS (
           SELECT 1
           FROM workflow_input_authority_predecessors AS predecessor
           CROSS JOIN LATERAL jsonb_array_elements(
             convert_from(v_authority.node_input_raw_bytes, 'UTF8')::jsonb->'bindings'
           ) AS binding(value)
           WHERE predecessor.authority_id = v_authority.authority_id
             AND predecessor.source_node_type = 'quality_gate'
             AND binding.value->>'edgeId' = predecessor.edge_id
             AND binding.value#>>'{output,qualityRunId}' = quality.id::text
             AND binding.value#>>'{output,findings,qualityRunId}' = quality.id::text
             AND binding.value#>>'{output,findings,reportArtifactId}' = quality.report_artifact_id::text
             AND binding.value#>>'{output,findings,reportRevisionId}' = quality.report_revision_id::text
             AND (binding.value#>>'{output,findings,score}')::integer = quality.score
         )
     )
     OR NOT EXISTS (
       SELECT 1 FROM implementation_proposals AS proposal
       JOIN artifact_revisions AS revision
         ON revision.id = v_authority.target_revision_id
        AND revision.implementation_proposal_id = proposal.id
       WHERE proposal.project_id = v_authority.project_id
         AND proposal.status IN ('applied','partially_applied')
         AND proposal.applied_at IS NOT NULL
         AND proposal.applied_by IS NOT NULL
         AND proposal.build_manifest_id = v_authority.build_manifest_id
         AND proposal.application_build_contract_id = v_authority.build_contract_id
         AND workflow_input_normalize_sha256(proposal.application_build_contract_hash) =
             v_authority.build_contract_hash
     )
     OR NOT EXISTS (
       SELECT 1 FROM application_build_manifests AS manifest
       WHERE manifest.id = v_authority.build_manifest_id
         AND manifest.project_id = v_authority.project_id
         AND manifest.workflow_run_id = v_authority.workflow_run_id
         AND manifest.status = 'consumed' AND manifest.invalidated_at IS NULL
         AND workflow_input_normalize_sha256(manifest.manifest_hash) = v_authority.build_manifest_hash
         AND workflow_input_normalize_sha256(manifest.content_hash) = v_authority.build_manifest_content_hash
     )
     OR NOT EXISTS (
       SELECT 1 FROM application_build_contracts AS contract
       WHERE contract.id = v_authority.build_contract_id
         AND contract.project_id = v_authority.project_id
         AND contract.build_manifest_id = v_authority.build_manifest_id
         AND workflow_input_normalize_sha256(contract.contract_hash) = v_authority.build_contract_hash
         AND workflow_input_normalize_sha256(contract.content_hash) = v_authority.build_contract_content_hash
         AND contract.status = 'ready' AND contract.superseded_at IS NULL
         AND contract.must_count > 0
         AND contract.must_ready_count = contract.must_count
         AND contract.blocking_count = 0 AND contract.conflict_count = 0
         AND contract.source_count = (
           SELECT count(*) FROM application_build_contract_sources AS source
           WHERE source.contract_id = contract.id
         )
         AND contract.template_release_count = (
           SELECT count(*) FROM application_build_contract_template_releases AS release
           WHERE release.contract_id = contract.id
         )
         AND contract.obligation_count = (
           SELECT count(*) FROM application_build_contract_obligations AS obligation
           WHERE obligation.contract_id = contract.id
         )
         AND contract.must_count = (
           SELECT count(*) FROM application_build_contract_obligations AS obligation
           WHERE obligation.contract_id = contract.id AND obligation.level = 'must'
         )
         AND contract.must_ready_count = (
           SELECT count(*) FROM application_build_contract_obligations AS obligation
           WHERE obligation.contract_id = contract.id
             AND obligation.level = 'must' AND obligation.status IN ('ready','waived')
         )
         AND contract.source_count = (
           SELECT count(*) FROM workflow_input_authority_revisions AS frozen
           WHERE frozen.authority_id = v_authority.authority_id
             AND frozen.purpose <> 'workspace-target'
         )
         AND NOT EXISTS (
           SELECT 1 FROM application_build_contract_sources AS source
           WHERE source.contract_id = contract.id
             AND NOT EXISTS (
               SELECT 1 FROM workflow_input_authority_revisions AS frozen
               WHERE frozen.authority_id = v_authority.authority_id
                 AND frozen.purpose = source.purpose
                 AND frozen.artifact_id = source.artifact_id
                 AND frozen.revision_id = source.revision_id
                 AND frozen.content_hash = workflow_input_normalize_sha256(source.content_hash)
             )
         )
         AND NOT EXISTS (
           SELECT 1 FROM workflow_input_authority_revisions AS frozen
           WHERE frozen.authority_id = v_authority.authority_id
             AND frozen.purpose <> 'workspace-target'
             AND NOT EXISTS (
               SELECT 1 FROM application_build_contract_sources AS source
               WHERE source.contract_id = contract.id
                 AND source.purpose = frozen.purpose
                 AND source.artifact_id = frozen.artifact_id
                 AND source.revision_id = frozen.revision_id
                 AND workflow_input_normalize_sha256(source.content_hash) = frozen.content_hash
             )
         )
     )
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_manifests AS frozen
       LEFT JOIN input_manifests AS current ON current.id = frozen.manifest_id
       WHERE frozen.authority_id = v_authority.authority_id
         AND (
           current.id IS NULL OR current.project_id <> frozen.project_id
           OR current.kind <> frozen.kind OR current.schema_version <> frozen.schema_version
           OR current.content_store <> frozen.content_store OR current.content_ref <> frozen.content_ref
           OR workflow_input_normalize_sha256(current.content_hash) <> frozen.content_hash
           OR workflow_input_normalize_sha256(current.manifest_hash) <> frozen.manifest_hash
         )
     )
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_revisions AS frozen
       LEFT JOIN artifact_revisions AS revision ON revision.id = frozen.revision_id
       LEFT JOIN artifacts AS artifact ON artifact.id = frozen.artifact_id
       WHERE frozen.authority_id = v_authority.authority_id
         AND (
           revision.id IS NULL OR artifact.id IS NULL
           OR workflow_input_normalize_sha256(revision.content_hash) <> frozen.content_hash
           OR revision.content_store <> frozen.content_store OR revision.content_ref <> frozen.content_ref
           OR revision.schema_version <> frozen.schema_version OR revision.byte_size <> frozen.byte_size
           OR (frozen.currency_policy = 'latest-approved-required' AND (
             revision.workflow_status <> 'approved' OR revision.superseded_at IS NOT NULL
             OR artifact.latest_revision_id <> frozen.revision_id
             OR artifact.latest_approved_revision_id <> frozen.revision_id
           ))
         )
     )
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_predecessors AS frozen
       LEFT JOIN workflow_node_runs AS source ON source.id = frozen.source_node_run_id
       LEFT JOIN artifact_revisions AS output ON output.id = source.output_revision_id
       WHERE frozen.authority_id = v_authority.authority_id
         AND (
           source.id IS NULL OR source.run_id <> v_authority.workflow_run_id
           OR source.node_key <> frozen.source_node_key
           OR source.definition_node_id <> frozen.source_definition_node_id
           OR source.node_type <> frozen.source_node_type
           OR source.slice_kind <> frozen.source_slice_kind
           OR source.slice_id IS DISTINCT FROM frozen.source_slice_id
           OR source.status <> 'completed'
           OR source.output_revision_id IS DISTINCT FROM output.id
           OR output.revision_number <> frozen.output_revision_number
           OR source.input_manifest_id IS DISTINCT FROM CASE
             WHEN jsonb_typeof(frozen.member_document->'inputManifest') = 'object'
             THEN (frozen.member_document#>>'{inputManifest,id}')::uuid ELSE NULL END
           OR source.output_proposal_id IS DISTINCT FROM CASE
             WHEN jsonb_typeof(frozen.member_document->'outputProposal') = 'object'
             THEN (frozen.member_document#>>'{outputProposal,id}')::uuid ELSE NULL END
           OR NOT EXISTS (
             SELECT 1
             FROM jsonb_array_elements(
               convert_from(v_authority.node_input_raw_bytes, 'UTF8')::jsonb->'bindings'
             ) AS binding(value)
             WHERE binding.value->>'edgeId' = frozen.edge_id
               AND binding.value->>'fromPort' = frozen.source_port
               AND binding.value->>'toPort' = frozen.target_port
               AND binding.value#>>'{source,nodeKey}' = frozen.source_node_key
               AND binding.value#>>'{source,outputRevisionId}' = source.output_revision_id::text
           )
         )
     )
     OR EXISTS (
       SELECT 1
       FROM workflow_input_authority_predecessors AS frozen
       CROSS JOIN LATERAL jsonb_array_elements(frozen.member_document->'deliverySliceRefs') AS reference(value)
       WHERE frozen.authority_id = v_authority.authority_id
         AND NOT EXISTS (
           SELECT 1
           FROM delivery_slices AS slice
           WHERE slice.id::text = reference.value->>'id'
             AND slice.project_id = v_authority.project_id
             AND slice.slice_key = reference.value->>'key'
             AND slice.blueprint_revision_id::text = reference.value#>>'{blueprint,revisionId}'
             AND (
               (slice.page_spec_revision_id IS NULL AND NOT (reference.value ? 'pageSpec'))
               OR slice.page_spec_revision_id::text = reference.value#>>'{pageSpec,revisionId}'
             )
             AND (
               (slice.prototype_revision_id IS NULL AND NOT (reference.value ? 'prototype'))
               OR slice.prototype_revision_id::text = reference.value#>>'{prototype,revisionId}'
             )
         )
     )
     OR EXISTS (
       SELECT 1
       FROM workflow_input_authority_predecessors AS frozen
       LEFT JOIN output_proposals AS proposal
         ON proposal.id = (frozen.member_document#>>'{outputProposal,id}')::uuid
       WHERE frozen.authority_id = v_authority.authority_id
         AND jsonb_typeof(frozen.member_document->'outputProposal') = 'object'
         AND (
           proposal.id IS NULL
           OR workflow_input_normalize_sha256(proposal.payload_hash) <>
              workflow_input_normalize_sha256(frozen.member_document#>>'{outputProposal,payloadHash}')
         )
     )
     OR EXISTS (
       SELECT 1
       FROM workflow_input_authority_predecessors AS frozen
       CROSS JOIN LATERAL jsonb_array_elements(frozen.member_document->'proposalPins') AS pin(value)
       LEFT JOIN output_proposals AS proposal
         ON proposal.id = (pin.value#>>'{proposal,id}')::uuid
       LEFT JOIN input_manifests AS manifest
         ON manifest.id = (pin.value#>>'{manifest,id}')::uuid
       WHERE frozen.authority_id = v_authority.authority_id
         AND (
           proposal.id IS NULL OR manifest.id IS NULL
           OR workflow_input_normalize_sha256(proposal.payload_hash) <>
              workflow_input_normalize_sha256(pin.value#>>'{proposal,payloadHash}')
           OR workflow_input_normalize_sha256(manifest.manifest_hash) <>
              workflow_input_normalize_sha256(pin.value#>>'{manifest,hash}')
         )
     )
     OR (SELECT count(*)
         FROM workflow_input_authority_predecessors AS frozen
         JOIN workflow_node_runs AS source ON source.id = frozen.source_node_run_id
         WHERE frozen.authority_id = v_authority.authority_id
           AND source.node_type = 'quality_gate'
           AND source.output_revision_id = v_authority.target_revision_id
           AND EXISTS (
             SELECT 1
             FROM jsonb_array_elements(frozen.member_document->'artifactRevisions') AS reference(value)
             WHERE value->>'artifactId' = v_authority.target_artifact_id::text
               AND value->>'revisionId' = v_authority.target_revision_id::text
               AND workflow_input_normalize_sha256(value->>'contentHash') =
                   v_authority.target_revision_content_hash
           )) <> 1
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_predecessors AS frozen
       WHERE frozen.authority_id = v_authority.authority_id
         AND frozen.source_node_run_id = v_authority.quality_run_id
     )
     OR EXISTS (
       SELECT 1 FROM workflow_input_authority_review_receipts AS frozen
       WHERE frozen.authority_id = v_authority.authority_id
         AND NOT EXISTS (
           SELECT 1 FROM resolve_canonical_review_approval_receipt(
             frozen.project_id, frozen.revision_id, frozen.receipt_hash
           ) AS receipt
           WHERE receipt.review_request_id = frozen.review_request_id
             AND receipt.receipt_bytes = frozen.receipt_bytes
             AND receipt.receipt_document = frozen.receipt_document
         )
     ) THEN
    RAISE EXCEPTION 'Workflow Input Authority is no longer current'
      USING ERRCODE = 'WIA04';
  END IF;
  RETURN NEXT workflow_input_authority_bundle_v1(v_authority.authority_id);
END;
$function$;

DO $qualification_policy_authority_security$
DECLARE
  schema_name text := current_schema();
  role_name text;
  role_sql text;
  issue_signature text :=
    'uuid,uuid,text,text,uuid,text,text,bigint,text,timestamptz,text,text,' ||
    'text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb';
BEGIN
  EXECUTE format(
    'ALTER FUNCTION %I.qualification_policy_authority_hash(text,bytea) '
    'SET search_path TO pg_catalog', schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.reject_qualification_policy_authority_mutation() '
    'SET search_path TO pg_catalog', schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.qualification_policy_embedded_uuid_references_v1(jsonb) '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.qualification_policy_authority_record_is_exact_v1(uuid) '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.validate_qualification_policy_authority_closure_v1() '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.issue_qualification_policy_authority_v1(%s) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, issue_signature, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.inspect_qualification_policy_operation_v1(uuid) '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.resolve_qualification_policy_authority_v1(uuid) '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.resolve_current_qualification_policy_authority_v1(uuid,text,text) '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.assert_current_qualification_policy_authority_v1(uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );

  FOREACH role_name IN ARRAY ARRAY[
    'PUBLIC','worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_repository_index_gc_operator','worksflow_golden_fault_operator',
    'worksflow_qualification_plan_operator','worksflow_qualification_promotion_operator',
    'worksflow_qualification_policy_operator'
  ] LOOP
    IF role_name = 'PUBLIC' OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name
    ) THEN
      role_sql := CASE WHEN role_name = 'PUBLIC' THEN 'PUBLIC' ELSE quote_ident(role_name) END;
      EXECUTE format(
        'REVOKE ALL ON TABLE %I.qualification_policy_authorities FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON TABLE %I.qualification_policy_review_defaults FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON TABLE %I.qualification_policy_exact_approved_sources FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON TABLE %I.qualification_policy_identity_reservations FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.qualification_policy_authority_hash(text,bytea) FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.reject_qualification_policy_authority_mutation() FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.qualification_policy_embedded_uuid_references_v1(jsonb) FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.qualification_policy_authority_record_is_exact_v1(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.validate_qualification_policy_authority_closure_v1() FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.issue_qualification_policy_authority_v1(%s) FROM %s',
        schema_name, issue_signature, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.inspect_qualification_policy_operation_v1(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.resolve_qualification_policy_authority_v1(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.resolve_current_qualification_policy_authority_v1(uuid,text,text) FROM %s',
        schema_name, role_sql
      );
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.assert_current_qualification_policy_authority_v1(uuid) FROM %s',
        schema_name, role_sql
      );
    END IF;
  END LOOP;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner'
  ) THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_migration_owner', schema_name);
    EXECUTE format(
      'ALTER TABLE %I.qualification_policy_authorities OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER TABLE %I.qualification_policy_review_defaults OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER TABLE %I.qualification_policy_exact_approved_sources OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER TABLE %I.qualification_policy_identity_reservations OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.qualification_policy_authority_hash(text,bytea) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.reject_qualification_policy_authority_mutation() '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.qualification_policy_embedded_uuid_references_v1(jsonb) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.qualification_policy_authority_record_is_exact_v1(uuid) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.validate_qualification_policy_authority_closure_v1() '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.issue_qualification_policy_authority_v1(%s) '
      'OWNER TO worksflow_migration_owner', schema_name, issue_signature
    );
    EXECUTE format(
      'ALTER FUNCTION %I.inspect_qualification_policy_operation_v1(uuid) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.resolve_qualification_policy_authority_v1(uuid) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.resolve_current_qualification_policy_authority_v1(uuid,text,text) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.assert_current_qualification_policy_authority_v1(uuid) '
      'OWNER TO worksflow_migration_owner', schema_name
    );
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_policy_operator'
  ) THEN
    EXECUTE format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_policy_operator', schema_name
    );
    EXECUTE format(
      'GRANT EXECUTE ON FUNCTION %I.issue_qualification_policy_authority_v1(%s) '
      'TO worksflow_qualification_policy_operator', schema_name, issue_signature
    );
    EXECUTE format(
      'GRANT EXECUTE ON FUNCTION %I.inspect_qualification_policy_operation_v1(uuid) '
      'TO worksflow_qualification_policy_operator', schema_name
    );
    EXECUTE format(
      'GRANT EXECUTE ON FUNCTION %I.resolve_qualification_policy_authority_v1(uuid) '
      'TO worksflow_qualification_policy_operator', schema_name
    );
    EXECUTE format(
      'GRANT EXECUTE ON FUNCTION %I.resolve_current_qualification_policy_authority_v1(uuid,text,text) '
      'TO worksflow_qualification_policy_operator', schema_name
    );
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_promotion_operator'
  ) THEN
    EXECUTE format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE format(
      'GRANT EXECUTE ON FUNCTION %I.assert_current_qualification_policy_authority_v1(uuid) '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
  END IF;
END;
$qualification_policy_authority_security$;

DO $workflow_input_authority_security$
DECLARE
  schema_name text := current_schema();
  role_name text;
  role_sql text;
  freeze_signature text :=
    'uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb';
BEGIN
  EXECUTE format('ALTER FUNCTION %I.workflow_input_raw_sha256(bytea) SET search_path TO pg_catalog', schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_hash(text,bytea) SET search_path TO pg_catalog', schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_normalize_sha256(text) SET search_path TO pg_catalog', schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_uuid_is_exact(text) SET search_path TO pg_catalog', schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_timestamp_is_exact(text) SET search_path TO pg_catalog', schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_canonical_jsonb_bytes(jsonb) SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.reject_workflow_input_authority_mutation() SET search_path TO pg_catalog', schema_name);
  EXECUTE format('ALTER FUNCTION %I.guard_workflow_node_stable_identity_v1() SET search_path TO pg_catalog', schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_record_is_exact(uuid) SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_replay_is_exact_v1(%s) SET search_path TO pg_catalog, %I', schema_name, freeze_signature, schema_name);
  EXECUTE format('ALTER FUNCTION %I.guard_workflow_input_authority_event_identity_v1() SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.validate_workflow_input_authority_closure_v1() SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.freeze_workflow_input_authority_v1(%s) SET search_path TO pg_catalog, %I, pg_temp', schema_name, freeze_signature, schema_name);
  EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_bundle_v1(uuid) SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.inspect_workflow_input_authority_operation_v1(uuid) SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.resolve_workflow_input_authority_v1(uuid) SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.resolve_workflow_input_authority_for_node_v1(uuid,uuid) SET search_path TO pg_catalog, %I', schema_name, schema_name);
  EXECUTE format('ALTER FUNCTION %I.assert_current_workflow_input_authority_v1(uuid) SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name);

  FOREACH role_name IN ARRAY ARRAY[
    'PUBLIC','worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_repository_index_gc_operator','worksflow_golden_fault_operator',
    'worksflow_qualification_plan_operator','worksflow_qualification_promotion_operator'
  ] LOOP
    IF role_name = 'PUBLIC' OR EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
      role_sql := CASE WHEN role_name = 'PUBLIC' THEN 'PUBLIC' ELSE quote_ident(role_name) END;
      EXECUTE format('REVOKE ALL ON TABLE %I.workflow_input_authorities FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON TABLE %I.workflow_input_authority_identity_reservations FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON TABLE %I.workflow_input_authority_predecessors FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON TABLE %I.workflow_input_authority_manifests FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON TABLE %I.workflow_input_authority_revisions FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON TABLE %I.workflow_input_authority_review_receipts FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_raw_sha256(bytea) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_authority_hash(text,bytea) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_normalize_sha256(text) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_uuid_is_exact(text) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_timestamp_is_exact(text) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_canonical_jsonb_bytes(jsonb) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.reject_workflow_input_authority_mutation() FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.guard_workflow_node_stable_identity_v1() FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_authority_record_is_exact(uuid) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_authority_replay_is_exact_v1(%s) FROM %s', schema_name, freeze_signature, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.guard_workflow_input_authority_event_identity_v1() FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.validate_workflow_input_authority_closure_v1() FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.freeze_workflow_input_authority_v1(%s) FROM %s', schema_name, freeze_signature, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.workflow_input_authority_bundle_v1(uuid) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.inspect_workflow_input_authority_operation_v1(uuid) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.resolve_workflow_input_authority_v1(uuid) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.resolve_workflow_input_authority_for_node_v1(uuid,uuid) FROM %s', schema_name, role_sql);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.assert_current_workflow_input_authority_v1(uuid) FROM %s', schema_name, role_sql);
    END IF;
  END LOOP;

  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.workflow_input_authorities OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.workflow_input_authority_identity_reservations OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.workflow_input_authority_predecessors OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.workflow_input_authority_manifests OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.workflow_input_authority_revisions OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.workflow_input_authority_review_receipts OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_raw_sha256(bytea) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_hash(text,bytea) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_normalize_sha256(text) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_uuid_is_exact(text) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_timestamp_is_exact(text) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_canonical_jsonb_bytes(jsonb) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.reject_workflow_input_authority_mutation() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.guard_workflow_node_stable_identity_v1() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_record_is_exact(uuid) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_replay_is_exact_v1(%s) OWNER TO worksflow_migration_owner', schema_name, freeze_signature);
    EXECUTE format('ALTER FUNCTION %I.guard_workflow_input_authority_event_identity_v1() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.validate_workflow_input_authority_closure_v1() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.freeze_workflow_input_authority_v1(%s) OWNER TO worksflow_migration_owner', schema_name, freeze_signature);
    EXECUTE format('ALTER FUNCTION %I.workflow_input_authority_bundle_v1(uuid) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.inspect_workflow_input_authority_operation_v1(uuid) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.resolve_workflow_input_authority_v1(uuid) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.resolve_workflow_input_authority_for_node_v1(uuid,uuid) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.assert_current_workflow_input_authority_v1(uuid) OWNER TO worksflow_migration_owner', schema_name);
  END IF;

  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_application') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_application', schema_name);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.freeze_workflow_input_authority_v1(%s) TO worksflow_application', schema_name, freeze_signature);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.inspect_workflow_input_authority_operation_v1(uuid) TO worksflow_application', schema_name);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.resolve_workflow_input_authority_for_node_v1(uuid,uuid) TO worksflow_application', schema_name);
  END IF;
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_qualification_plan_operator') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_qualification_plan_operator', schema_name);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.resolve_workflow_input_authority_v1(uuid) TO worksflow_qualification_plan_operator', schema_name);
  END IF;
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_qualification_promotion_operator') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_qualification_promotion_operator', schema_name);
    EXECUTE format('GRANT EXECUTE ON FUNCTION %I.assert_current_workflow_input_authority_v1(uuid) TO worksflow_qualification_promotion_operator', schema_name);
  END IF;
END;
$workflow_input_authority_security$;

COMMENT ON FUNCTION freeze_workflow_input_authority_v1(
  uuid,uuid,uuid,uuid,bigint,uuid,bigint,bytea,bytea,bytea,bytea,bytea,jsonb
) IS 'Owner-side issuer. Caller supplies only server-preflight raw bytes and typed IDs/purposes; PostgreSQL locks current facts and authors all four authority documents.';
COMMENT ON FUNCTION assert_current_workflow_input_authority_v1(uuid) IS
  'Promotion-v2 lock/read primitive; it is not an approval or waiver command.';
