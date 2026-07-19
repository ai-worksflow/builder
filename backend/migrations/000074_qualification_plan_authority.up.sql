-- Server-owned immutable Qualification Plan authority. This migration closes
-- caller-supplied Plan self-certification for new Qualification Evidence
-- reservations, but does not claim that a real target/source/credential/KMS
-- authority has been provisioned. The issuer must resolve those inputs from
-- trusted server state before calling the owner-only freeze routine.
--
-- bytea + SHA-256 is the authority for every canonical document. JSONB and
-- scalar columns are acceleration projections and are rechecked against the
-- bytes on every supported write/read path. PostgreSQL JSONB equality cannot
-- independently reject duplicate raw names or alternate number spellings, so
-- no non-owner EXECUTE grant is safe until a database-side raw canonical-JSON
-- validator is reviewed. The migration owner is already an ALTER/DROP trust
-- boundary; direct owner calls are not treated as an adversarial API.
DO $qualification_plan_hash_function$
DECLARE
  schema_name text := current_schema();
BEGIN
  -- PostgreSQL 16 exposes SHA-256 in pg_catalog. Using the core function keeps
  -- this helper independent of pgcrypto's install-schema ACL and avoids
  -- granting the migration owner USAGE on an arbitrary extension namespace.
  EXECUTE format(
    'CREATE FUNCTION %I.qualification_plan_sha256(bytea) RETURNS text '
    'LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS %L',
    schema_name,
    'SELECT ''sha256:'' || pg_catalog.encode(pg_catalog.sha256($1), ''hex'')'
  );
END;
$qualification_plan_hash_function$;

CREATE TABLE qualification_plan_authorities (
  authority_id uuid PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE,
  input_authority_id uuid NOT NULL,
  plan_artifact_id text NOT NULL UNIQUE CHECK (
    octet_length(plan_artifact_id) BETWEEN 1 AND 128
    AND plan_artifact_id ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
  ),
  orchestration_id uuid NOT NULL UNIQUE,
  qualification_run_id uuid NOT NULL,
  fixture_id uuid NOT NULL,
  credential_set_id uuid NOT NULL,

  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (octet_length(request_bytes) BETWEEN 1 AND 65536),
  request_document jsonb NOT NULL CHECK (jsonb_typeof(request_document) = 'object'),

  input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
  input_bytes bytea NOT NULL CHECK (octet_length(input_bytes) BETWEEN 1 AND 4194304),
  input_document jsonb NOT NULL CHECK (jsonb_typeof(input_document) = 'object'),

  projection_hash text NOT NULL CHECK (projection_hash ~ '^sha256:[0-9a-f]{64}$'),
  projection_bytes bytea NOT NULL CHECK (octet_length(projection_bytes) BETWEEN 1 AND 16777216),
  projection_document jsonb NOT NULL CHECK (jsonb_typeof(projection_document) = 'object'),

  evidence_plan_hash text NOT NULL CHECK (evidence_plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  evidence_plan_bytes bytea NOT NULL CHECK (octet_length(evidence_plan_bytes) BETWEEN 1 AND 2097152),
  evidence_plan_document jsonb NOT NULL CHECK (jsonb_typeof(evidence_plan_document) = 'object'),

  trust_hash text NOT NULL CHECK (trust_hash ~ '^sha256:[0-9a-f]{64}$'),
  trust_bytes bytea NOT NULL CHECK (octet_length(trust_bytes) BETWEEN 1 AND 1048576),
  trust_document jsonb NOT NULL CHECK (jsonb_typeof(trust_document) = 'object'),
  trust_bindings_digest text NOT NULL CHECK (trust_bindings_digest ~ '^sha256:[0-9a-f]{64}$'),
  trust_policy_digest text NOT NULL CHECK (trust_policy_digest ~ '^sha256:[0-9a-f]{64}$'),

  target_hash text NOT NULL CHECK (target_hash ~ '^sha256:[0-9a-f]{64}$'),
  target_bytes bytea NOT NULL CHECK (octet_length(target_bytes) BETWEEN 1 AND 262144),
  target_document jsonb NOT NULL CHECK (jsonb_typeof(target_document) = 'object'),
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  node_key text NOT NULL CHECK (
    octet_length(node_key) BETWEEN 1 AND 256
    AND node_key ~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$'
  ),
  target_revision_id uuid NOT NULL,
  target_revision_content_hash text NOT NULL CHECK (
    target_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  subject text NOT NULL CHECK (
    octet_length(subject) BETWEEN 1 AND 256
    AND subject = btrim(subject)
    AND subject !~ '[[:cntrl:]]'
  ),
  stage_gate text NOT NULL CHECK (stage_gate = 'external-qualification'),

  envelope_hash text NOT NULL CHECK (envelope_hash ~ '^sha256:[0-9a-f]{64}$'),
  envelope_bytes bytea NOT NULL CHECK (octet_length(envelope_bytes) BETWEEN 1 AND 1048576),
  envelope_document jsonb NOT NULL CHECK (jsonb_typeof(envelope_document) = 'object'),

  source_tree_digest text NOT NULL CHECK (source_tree_digest ~ '^sha256:[0-9a-f]{64}$'),
  template_release_digest text NOT NULL CHECK (template_release_digest ~ '^sha256:[0-9a-f]{64}$'),
  frozen_at timestamptz NOT NULL CHECK (frozen_at = date_trunc('milliseconds', frozen_at)),

  CONSTRAINT qualification_plan_authority_v4 CHECK (
    authority_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND operation_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND input_authority_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND orchestration_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND qualification_run_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND fixture_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND credential_set_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND project_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND workflow_run_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND target_revision_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_plan_root_identities_distinct CHECK (
    authority_id <> operation_id
    AND authority_id <> input_authority_id
    AND authority_id <> orchestration_id
    AND authority_id <> qualification_run_id
    AND authority_id <> fixture_id
    AND authority_id <> credential_set_id
    AND operation_id <> orchestration_id
    AND operation_id <> input_authority_id
    AND operation_id <> qualification_run_id
    AND operation_id <> fixture_id
    AND operation_id <> credential_set_id
    AND input_authority_id <> orchestration_id
    AND input_authority_id <> qualification_run_id
    AND input_authority_id <> fixture_id
    AND input_authority_id <> credential_set_id
    AND orchestration_id <> qualification_run_id
    AND orchestration_id <> fixture_id
    AND orchestration_id <> credential_set_id
    AND qualification_run_id <> fixture_id
    AND qualification_run_id <> credential_set_id
    AND fixture_id <> credential_set_id
  ),
  CONSTRAINT qualification_plan_hash_domains_distinct CHECK (
    projection_hash <> evidence_plan_hash
    AND input_hash <> envelope_hash
    AND trust_hash <> target_hash
  )
);

CREATE INDEX qualification_plan_authorities_target_idx
  ON qualification_plan_authorities(project_id, workflow_run_id, node_key, target_revision_id);

-- One global text namespace covers UUID identities and the stable Plan
-- artifact identity. Evidence operations intentionally reuse their authority
-- reservation later; migration 000073 remains the event-ledger owner of the
-- corresponding executed operation rows.
CREATE TABLE qualification_plan_identity_reservations (
  identity_value text PRIMARY KEY CHECK (octet_length(identity_value) BETWEEN 1 AND 128),
  authority_id uuid NOT NULL REFERENCES qualification_plan_authorities(authority_id) ON DELETE RESTRICT,
  identity_kind text NOT NULL CHECK (identity_kind IN (
    'authority','freeze-operation','input-authority','plan-artifact','orchestration',
    'qualification-run','credential-set','evidence-operation'
  )),
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 1024),
  reserved_at timestamptz NOT NULL CHECK (reserved_at = date_trunc('milliseconds', reserved_at)),
  UNIQUE (authority_id, identity_kind, ordinal),
  CHECK (
    (identity_kind = 'plan-artifact'
      AND identity_value ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$')
    OR
    (identity_kind <> 'plan-artifact'
      AND identity_value ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
  )
);

CREATE INDEX qualification_plan_identity_authority_idx
  ON qualification_plan_identity_reservations(authority_id);

CREATE FUNCTION reject_qualification_plan_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification Plan authority and identity reservations are immutable'
    USING ERRCODE = 'WQP03';
END;
$function$;

CREATE TRIGGER qualification_plan_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_plan_authorities
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_plan_immutable_mutation();

CREATE TRIGGER qualification_plan_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_plan_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_plan_immutable_mutation();

CREATE FUNCTION freeze_qualification_plan_authority(
  p_operation_id uuid,
  p_authority_id uuid,
  p_request_hash text,
  p_request_bytes bytea,
  p_request_document jsonb,
  p_input_hash text,
  p_input_bytes bytea,
  p_input_document jsonb,
  p_projection_hash text,
  p_projection_bytes bytea,
  p_projection_document jsonb,
  p_evidence_plan_hash text,
  p_evidence_plan_bytes bytea,
  p_evidence_plan_document jsonb,
  p_trust_hash text,
  p_trust_bytes bytea,
  p_trust_document jsonb,
  p_target_hash text,
  p_target_bytes bytea,
  p_target_document jsonb,
  p_envelope_hash text,
  p_envelope_bytes bytea,
  p_envelope_document jsonb
)
RETURNS SETOF qualification_plan_authorities
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_existing qualification_plan_authorities%ROWTYPE;
  v_evidence_plan jsonb;
  v_promotion_target jsonb;
  v_target_revision jsonb;
  v_now timestamptz;
  v_input_authority_id uuid;
  v_orchestration_id uuid;
  v_qualification_run_id uuid;
  v_fixture_id uuid;
  v_credential_set_id uuid;
  v_project_id uuid;
  v_workflow_run_id uuid;
  v_target_revision_id uuid;
  v_plan_artifact_id text;
  v_record_identity_values text[];
  v_reserved_identity_values text[];
  v_identity_count integer;
  v_distinct_identity_count integer;
BEGIN
  IF p_operation_id IS NULL
     OR p_operation_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_authority_id IS NULL
     OR p_authority_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_operation_id = p_authority_id THEN
    RAISE EXCEPTION 'Qualification Plan freeze identity is invalid' USING ERRCODE = 'WQP03';
  END IF;

  -- The owner-trusted Go issuer supplies canonical raw bytes. The checks below
  -- close each byte string to its hash/document and reject widened roots; they
  -- deliberately do not pretend JSONB equality is a raw canonical validator.
  IF p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_request_bytes IS NULL OR octet_length(p_request_bytes) NOT BETWEEN 1 AND 65536
     OR get_byte(p_request_bytes, 0) <> 123 OR get_byte(p_request_bytes, octet_length(p_request_bytes) - 1) <> 125
     OR p_request_document IS NULL OR jsonb_typeof(p_request_document) <> 'object'
     OR qualification_plan_sha256(p_request_bytes) <> p_request_hash
     OR convert_from(p_request_bytes, 'UTF8')::jsonb <> p_request_document
     OR NOT (p_request_document ?& ARRAY['authorityId','inputAuthorityId','operationId','schemaVersion'])
     OR p_request_document - ARRAY['authorityId','inputAuthorityId','operationId','schemaVersion'] <> '{}'::jsonb
     OR p_request_document->>'schemaVersion' <> 'worksflow-qualification-plan-freeze-request/v1'
     OR p_request_document->>'authorityId' <> p_authority_id::text
     OR p_request_document->>'operationId' <> p_operation_id::text
     OR jsonb_typeof(p_request_document->'inputAuthorityId') <> 'string' THEN
    RAISE EXCEPTION 'Qualification Plan freeze request bytes, hash, or shape is invalid' USING ERRCODE = 'WQP03';
  END IF;

  IF p_input_hash IS NULL OR p_input_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_input_bytes IS NULL OR octet_length(p_input_bytes) NOT BETWEEN 1 AND 4194304
     OR get_byte(p_input_bytes, 0) <> 123 OR get_byte(p_input_bytes, octet_length(p_input_bytes) - 1) <> 125
     OR p_input_document IS NULL OR jsonb_typeof(p_input_document) <> 'object'
     OR qualification_plan_sha256(p_input_bytes) <> p_input_hash
     OR convert_from(p_input_bytes, 'UTF8')::jsonb <> p_input_document
     OR NOT (p_input_document ?& ARRAY[
       'artifactPolicy','artifacts','buildContract','buildManifest','credential','goldenRuntime',
       'outputs','outputPolicy','promotionTarget','qualificationManifest','qualificationPlanDigest',
       'recipient','schemaVersion','source','templateRelease','trustBindings','trustPolicyDigest'
     ])
     OR p_input_document - ARRAY[
       'artifactPolicy','artifacts','buildContract','buildManifest','credential','goldenRuntime',
       'outputs','outputPolicy','promotionTarget','qualificationManifest','qualificationPlanDigest',
       'recipient','schemaVersion','source','templateRelease','trustBindings','trustPolicyDigest'
     ] <> '{}'::jsonb
     OR p_input_document->>'schemaVersion' <> 'worksflow-qualification-plan-input/v1'
     OR jsonb_typeof(p_input_document->'artifactPolicy') <> 'object'
     OR jsonb_typeof(p_input_document->'artifacts') <> 'array'
     OR jsonb_typeof(p_input_document->'buildContract') <> 'object'
     OR jsonb_typeof(p_input_document->'buildManifest') <> 'object'
     OR jsonb_typeof(p_input_document->'credential') <> 'object'
     OR jsonb_typeof(p_input_document->'goldenRuntime') <> 'object'
     OR jsonb_typeof(p_input_document->'outputs') <> 'object'
     OR jsonb_typeof(p_input_document->'outputPolicy') <> 'object'
     OR jsonb_typeof(p_input_document->'promotionTarget') <> 'object'
     OR jsonb_typeof(p_input_document->'qualificationManifest') <> 'object'
     OR jsonb_typeof(p_input_document->'qualificationPlanDigest') <> 'string'
     OR jsonb_typeof(p_input_document->'recipient') <> 'object'
     OR jsonb_typeof(p_input_document->'source') <> 'object'
     OR jsonb_typeof(p_input_document->'templateRelease') <> 'object'
     OR jsonb_typeof(p_input_document->'trustBindings') <> 'object'
     OR jsonb_typeof(p_input_document->'trustPolicyDigest') <> 'string'
     OR jsonb_typeof(p_input_document->'source'->'treeDigest') <> 'string'
     OR p_input_document->'source'->>'treeDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR jsonb_typeof(p_input_document->'goldenRuntime'->'fixtureId') <> 'string'
     OR p_input_document->>'qualificationPlanDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR p_input_document->>'trustPolicyDigest' !~ '^sha256:[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'Qualification Plan input bytes, hash, or closed root shape is invalid' USING ERRCODE = 'WQP03';
  END IF;

  IF jsonb_array_length(p_input_document->'artifacts') NOT BETWEEN 1 AND 512
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(p_input_document->'artifacts') AS input_artifact
       WHERE jsonb_typeof(input_artifact) <> 'object'
          OR NOT (input_artifact ?& ARRAY['classification','id','kind'])
          OR input_artifact - ARRAY['classification','id','kind'] <> '{}'::jsonb
          OR jsonb_typeof(input_artifact->'classification') <> 'string'
          OR jsonb_typeof(input_artifact->'id') <> 'string'
          OR jsonb_typeof(input_artifact->'kind') <> 'string'
          OR input_artifact->>'id' !~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
          OR octet_length(input_artifact->>'id') > 128
          OR input_artifact->>'kind' NOT IN (
            'run-result','trace','video','log','golden-document','fault-evidence',
            'writer-drain-proof','runtime-proof'
          )
          OR input_artifact->>'classification' NOT IN ('distributable','restricted')
          OR ((input_artifact->>'kind' IN ('trace','video','log')) <>
              (input_artifact->>'classification' = 'restricted'))
     ) OR EXISTS (
       SELECT 1 FROM (
         SELECT input_artifact->>'id' AS artifact_id,
                lag(input_artifact->>'id') OVER (ORDER BY ordinal) AS previous_id
         FROM jsonb_array_elements(p_input_document->'artifacts') WITH ORDINALITY
           AS item(input_artifact, ordinal)
       ) AS ordered
       WHERE pg_catalog.convert_to(previous_id, 'UTF8') >=
             pg_catalog.convert_to(artifact_id, 'UTF8')
     ) THEN
    RAISE EXCEPTION 'Qualification Plan input artifact closure is invalid' USING ERRCODE = 'WQP03';
  END IF;

  IF p_projection_hash IS NULL OR p_projection_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_projection_bytes IS NULL OR octet_length(p_projection_bytes) NOT BETWEEN 1 AND 16777216
     OR get_byte(p_projection_bytes, 0) <> 123 OR get_byte(p_projection_bytes, octet_length(p_projection_bytes) - 1) <> 125
     OR p_projection_document IS NULL OR jsonb_typeof(p_projection_document) <> 'object'
     OR qualification_plan_sha256(p_projection_bytes) <> p_projection_hash
     OR convert_from(p_projection_bytes, 'UTF8')::jsonb <> p_projection_document
     OR NOT (p_projection_document ?& ARRAY[
       'manifestSchemaVersion','policy','schemaVersion','sourceDocuments','subject','suites','supportFiles'
     ])
     OR p_projection_document - ARRAY[
       'manifestSchemaVersion','policy','schemaVersion','sourceDocuments','subject','suites','supportFiles'
     ] <> '{}'::jsonb
     OR p_projection_document->>'schemaVersion' <> 'worksflow-qualification-plan/v1'
     OR p_projection_document->>'manifestSchemaVersion' <> 'worksflow-qualification-manifest/v1'
     OR jsonb_typeof(p_projection_document->'policy') <> 'object'
     OR jsonb_typeof(p_projection_document->'sourceDocuments') <> 'array'
     OR jsonb_typeof(p_projection_document->'suites') <> 'array'
     OR jsonb_typeof(p_projection_document->'supportFiles') <> 'array'
     OR jsonb_array_length(p_projection_document->'sourceDocuments') = 0
     OR jsonb_array_length(p_projection_document->'suites') = 0
     OR jsonb_array_length(p_projection_document->'supportFiles') = 0
     OR jsonb_typeof(p_projection_document->'subject') <> 'string'
     OR p_projection_hash <> p_input_document->>'qualificationPlanDigest' THEN
    RAISE EXCEPTION 'Qualification Plan projection bytes, hash, or schema is invalid' USING ERRCODE = 'WQP03';
  END IF;

  IF p_evidence_plan_hash IS NULL OR p_evidence_plan_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_evidence_plan_bytes IS NULL OR octet_length(p_evidence_plan_bytes) NOT BETWEEN 1 AND 2097152
     OR get_byte(p_evidence_plan_bytes, 0) <> 123 OR get_byte(p_evidence_plan_bytes, octet_length(p_evidence_plan_bytes) - 1) <> 125
     OR p_evidence_plan_document IS NULL OR jsonb_typeof(p_evidence_plan_document) <> 'object'
     OR qualification_plan_sha256(p_evidence_plan_bytes) <> p_evidence_plan_hash
     OR convert_from(p_evidence_plan_bytes, 'UTF8')::jsonb <> p_evidence_plan_document
     OR NOT (p_evidence_plan_document ?& ARRAY[
       'schemaVersion','orchestrationId','runId','fixtureId','qualificationPlanArtifactId',
       'planDigest','sourceTreeDigest','templateReleaseDigest','operations','credentialSet',
       'artifacts','recipient','outputs'
     ])
     OR p_evidence_plan_document - ARRAY[
       'schemaVersion','orchestrationId','runId','fixtureId','qualificationPlanArtifactId',
       'planDigest','sourceTreeDigest','templateReleaseDigest','operations','credentialSet',
       'artifacts','recipient','outputs'
     ] <> '{}'::jsonb
     OR p_evidence_plan_document->>'schemaVersion' <> 'worksflow-qualification-evidence-plan/v1'
     OR p_evidence_plan_document->>'planDigest' <> p_projection_hash
     OR p_evidence_plan_document->>'sourceTreeDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR p_evidence_plan_document->>'templateReleaseDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR jsonb_typeof(p_evidence_plan_document->'operations') <> 'object'
     OR jsonb_typeof(p_evidence_plan_document->'credentialSet') <> 'object'
     OR jsonb_typeof(p_evidence_plan_document->'artifacts') <> 'array'
     OR jsonb_typeof(p_evidence_plan_document->'recipient') <> 'object'
     OR jsonb_typeof(p_evidence_plan_document->'outputs') <> 'object'
     OR NOT ((p_evidence_plan_document->'operations') ?& ARRAY[
       'reserve','credentialIssue','runClosure','kmsAttestation','credentialRevocation',
       'artifactIndex','receiptSign','snapshotSeal'
     ])
     OR (p_evidence_plan_document->'operations') - ARRAY[
       'reserve','credentialIssue','runClosure','kmsAttestation','credentialRevocation',
       'artifactIndex','receiptSign','snapshotSeal'
     ] <> '{}'::jsonb
     OR jsonb_array_length(p_evidence_plan_document->'artifacts') NOT BETWEEN 1 AND 512
     OR jsonb_array_length(p_evidence_plan_document->'artifacts') <>
        jsonb_array_length(p_input_document->'artifacts')
     OR p_evidence_plan_document->>'fixtureId' <>
        p_input_document->'goldenRuntime'->>'fixtureId'
     OR p_evidence_plan_document->>'sourceTreeDigest' <>
        p_input_document->'source'->>'treeDigest'
     OR p_evidence_plan_document->'credentialSet' <> p_input_document->'credential'
     OR p_evidence_plan_document->'outputs' <> p_input_document->'outputs'
     OR p_evidence_plan_document->'recipient' <> p_input_document->'recipient' THEN
    RAISE EXCEPTION 'Qualification Evidence Plan bytes, hash, or input closure is invalid' USING ERRCODE = 'WQP03';
  END IF;

  IF p_trust_hash IS NULL OR p_trust_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_trust_bytes IS NULL OR octet_length(p_trust_bytes) NOT BETWEEN 1 AND 1048576
     OR get_byte(p_trust_bytes, 0) <> 123 OR get_byte(p_trust_bytes, octet_length(p_trust_bytes) - 1) <> 125
     OR p_trust_document IS NULL OR jsonb_typeof(p_trust_document) <> 'object'
     OR qualification_plan_sha256(p_trust_bytes) <> p_trust_hash
     OR convert_from(p_trust_bytes, 'UTF8')::jsonb <> p_trust_document
     OR NOT (p_trust_document ?& ARRAY['schemaVersion','trustBindings','trustPolicyDigest'])
     OR p_trust_document - ARRAY['schemaVersion','trustBindings','trustPolicyDigest'] <> '{}'::jsonb
     OR p_trust_document->>'schemaVersion' <> 'worksflow-qualification-plan-trust/v1'
     OR jsonb_typeof(p_trust_document->'trustBindings') <> 'object'
     OR p_trust_document->>'trustPolicyDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR p_trust_document->'trustBindings' <> p_input_document->'trustBindings'
     OR p_trust_document->>'trustPolicyDigest' <> p_input_document->>'trustPolicyDigest' THEN
    RAISE EXCEPTION 'Qualification Plan trust bytes, hash, or input binding is invalid' USING ERRCODE = 'WQP03';
  END IF;

  IF p_target_hash IS NULL OR p_target_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_target_bytes IS NULL OR octet_length(p_target_bytes) NOT BETWEEN 1 AND 262144
     OR get_byte(p_target_bytes, 0) <> 123 OR get_byte(p_target_bytes, octet_length(p_target_bytes) - 1) <> 125
     OR p_target_document IS NULL OR jsonb_typeof(p_target_document) <> 'object'
     OR qualification_plan_sha256(p_target_bytes) <> p_target_hash
     OR convert_from(p_target_bytes, 'UTF8')::jsonb <> p_target_document
     OR NOT (p_target_document ?& ARRAY['promotionTarget','schemaVersion'])
     OR p_target_document - ARRAY['promotionTarget','schemaVersion'] <> '{}'::jsonb
     OR p_target_document->>'schemaVersion' <> 'worksflow-qualification-plan-target/v1'
     OR jsonb_typeof(p_target_document->'promotionTarget') <> 'object'
     OR p_target_document->'promotionTarget' <> p_input_document->'promotionTarget' THEN
    RAISE EXCEPTION 'Qualification Plan target bytes, hash, or input binding is invalid' USING ERRCODE = 'WQP03';
  END IF;

  IF p_envelope_hash IS NULL OR p_envelope_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_envelope_bytes IS NULL OR octet_length(p_envelope_bytes) NOT BETWEEN 1 AND 1048576
     OR get_byte(p_envelope_bytes, 0) <> 123 OR get_byte(p_envelope_bytes, octet_length(p_envelope_bytes) - 1) <> 125
     OR p_envelope_document IS NULL OR jsonb_typeof(p_envelope_document) <> 'object'
     OR qualification_plan_sha256(p_envelope_bytes) <> p_envelope_hash
     OR convert_from(p_envelope_bytes, 'UTF8')::jsonb <> p_envelope_document
     OR NOT (p_envelope_document ?& ARRAY[
       'artifactId','authorityId','evidencePlanHash','inputAuthorityId','inputHash',
       'manifestPlanDigest','operationId','projectionHash','schemaVersion','targetHash',
       'trustBindingsDigest','trustHash'
     ])
     OR p_envelope_document - ARRAY[
       'artifactId','authorityId','evidencePlanHash','inputAuthorityId','inputHash',
       'manifestPlanDigest','operationId','projectionHash','schemaVersion','targetHash',
       'trustBindingsDigest','trustHash'
     ] <> '{}'::jsonb
     OR p_envelope_document->>'schemaVersion' <> 'worksflow-qualification-plan-authority/v1'
     OR p_envelope_document->>'authorityId' <> p_authority_id::text
     OR p_envelope_document->>'operationId' <> p_operation_id::text
     OR p_envelope_document->>'inputAuthorityId' <> p_request_document->>'inputAuthorityId'
     OR p_envelope_document->>'inputHash' <> p_input_hash
     OR p_envelope_document->>'projectionHash' <> p_projection_hash
     OR p_envelope_document->>'manifestPlanDigest' <> p_projection_hash
     OR p_envelope_document->>'evidencePlanHash' <> p_evidence_plan_hash
     OR p_envelope_document->>'trustHash' <> p_trust_hash
     OR p_envelope_document->>'targetHash' <> p_target_hash
     OR p_envelope_document->>'trustBindingsDigest' !~ '^sha256:[0-9a-f]{64}$'
     OR p_envelope_document->>'artifactId' <> p_evidence_plan_document->>'qualificationPlanArtifactId'
     OR p_envelope_document->>'artifactId' <> 'qualification-plan-' || p_authority_id::text THEN
    RAISE EXCEPTION 'Qualification Plan authority envelope bytes, hash, or cross-binding is invalid' USING ERRCODE = 'WQP03';
  END IF;

  v_evidence_plan := p_evidence_plan_document;
  v_promotion_target := p_target_document->'promotionTarget';
  v_target_revision := v_promotion_target->'targetRevision';
  IF NOT (v_promotion_target ?& ARRAY[
       'nodeKey','projectId','stageGate','subject','targetRevision','workflowRunId'
     ])
     OR v_promotion_target - ARRAY[
       'nodeKey','projectId','stageGate','subject','targetRevision','workflowRunId'
     ] <> '{}'::jsonb
     OR jsonb_typeof(v_target_revision) <> 'object'
     OR NOT (v_target_revision ?& ARRAY['contentHash','id'])
     OR v_target_revision - ARRAY['contentHash','id'] <> '{}'::jsonb
     OR v_promotion_target->>'stageGate' <> 'external-qualification'
     OR v_target_revision->>'contentHash' !~ '^sha256:[0-9a-f]{64}$'
     OR v_promotion_target->>'subject' <> p_projection_document->>'subject'
     OR octet_length(v_promotion_target->>'nodeKey') NOT BETWEEN 1 AND 256
     OR v_promotion_target->>'nodeKey' !~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$'
     OR octet_length(v_promotion_target->>'subject') NOT BETWEEN 1 AND 256
     OR v_promotion_target->>'subject' <> btrim(v_promotion_target->>'subject')
     OR v_promotion_target->>'subject' ~ '[[:cntrl:]]' THEN
    RAISE EXCEPTION 'Qualification Plan promotion target is invalid or differs from the Plan subject' USING ERRCODE = 'WQP03';
  END IF;

  BEGIN
    v_input_authority_id := (p_request_document->>'inputAuthorityId')::uuid;
    v_orchestration_id := (v_evidence_plan->>'orchestrationId')::uuid;
    v_qualification_run_id := (v_evidence_plan->>'runId')::uuid;
    v_fixture_id := (v_evidence_plan->>'fixtureId')::uuid;
    v_credential_set_id := (v_evidence_plan->'credentialSet'->>'setId')::uuid;
    v_project_id := (v_promotion_target->>'projectId')::uuid;
    v_workflow_run_id := (v_promotion_target->>'workflowRunId')::uuid;
    v_target_revision_id := (v_target_revision->>'id')::uuid;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Qualification Plan projected UUID identity is malformed' USING ERRCODE = 'WQP03';
  END;
  v_plan_artifact_id := v_evidence_plan->>'qualificationPlanArtifactId';
  IF v_input_authority_id::text <> p_request_document->>'inputAuthorityId'
     OR v_orchestration_id::text <> v_evidence_plan->>'orchestrationId'
     OR v_qualification_run_id::text <> v_evidence_plan->>'runId'
     OR v_fixture_id::text <> v_evidence_plan->>'fixtureId'
     OR v_credential_set_id::text <> v_evidence_plan->'credentialSet'->>'setId'
     OR v_project_id::text <> v_promotion_target->>'projectId'
     OR v_workflow_run_id::text <> v_promotion_target->>'workflowRunId'
     OR v_target_revision_id::text <> v_target_revision->>'id'
     OR p_request_document->>'inputAuthorityId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_evidence_plan->>'orchestrationId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_evidence_plan->>'runId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_evidence_plan->>'fixtureId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_evidence_plan->'credentialSet'->>'setId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_promotion_target->>'projectId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_promotion_target->>'workflowRunId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR v_target_revision->>'id' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'Qualification Plan projected UUID identity is non-canonical' USING ERRCODE = 'WQP03';
  END IF;

  IF EXISTS (
    SELECT 1 FROM jsonb_each_text(v_evidence_plan->'operations') AS operation
    WHERE operation.value !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ) OR EXISTS (
    SELECT 1 FROM jsonb_array_elements(v_evidence_plan->'artifacts') AS artifact
    WHERE jsonb_typeof(artifact) <> 'object'
       OR NOT (artifact ?& ARRAY['classification','encryptionOperationId','id','kind'])
       OR artifact - ARRAY['classification','encryptionOperationId','id','kind'] <> '{}'::jsonb
       OR jsonb_typeof(artifact->'classification') <> 'string'
       OR jsonb_typeof(artifact->'encryptionOperationId') <> 'string'
       OR jsonb_typeof(artifact->'id') <> 'string'
       OR jsonb_typeof(artifact->'kind') <> 'string'
       OR artifact->>'id' !~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
       OR octet_length(artifact->>'id') > 128
       OR artifact->>'kind' NOT IN (
         'run-result','trace','video','log','golden-document','fault-evidence',
         'writer-drain-proof','runtime-proof'
       )
       OR artifact->>'classification' NOT IN ('distributable','restricted')
       OR ((artifact->>'kind' IN ('trace','video','log')) <>
           (artifact->>'classification' = 'restricted'))
       OR (
         artifact->>'classification' = 'restricted'
         AND artifact->>'encryptionOperationId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
       )
       OR (artifact->>'classification' = 'distributable' AND artifact->>'encryptionOperationId' <> '')
  ) OR EXISTS (
    SELECT 1
    FROM jsonb_array_elements(v_evidence_plan->'artifacts') WITH ORDINALITY
      AS evidence_item(evidence_artifact, ordinal)
    JOIN jsonb_array_elements(p_input_document->'artifacts') WITH ORDINALITY
      AS input_item(input_artifact, ordinal) USING (ordinal)
    WHERE evidence_artifact->'classification' IS DISTINCT FROM input_artifact->'classification'
       OR evidence_artifact->'id' IS DISTINCT FROM input_artifact->'id'
       OR evidence_artifact->'kind' IS DISTINCT FROM input_artifact->'kind'
  ) OR EXISTS (
    SELECT 1 FROM (
      SELECT artifact->>'id' AS artifact_id,
             lag(artifact->>'id') OVER (ORDER BY ordinal) AS previous_id
      FROM jsonb_array_elements(v_evidence_plan->'artifacts') WITH ORDINALITY
        AS item(artifact, ordinal)
    ) AS ordered
    WHERE pg_catalog.convert_to(previous_id, 'UTF8') >=
          pg_catalog.convert_to(artifact_id, 'UTF8')
  ) OR NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(v_evidence_plan->'artifacts') AS artifact
    WHERE artifact->>'kind' = 'trace' AND artifact->>'classification' = 'restricted'
  ) OR NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(v_evidence_plan->'artifacts') AS artifact
    WHERE artifact->>'kind' = 'video' AND artifact->>'classification' = 'restricted'
  ) THEN
    RAISE EXCEPTION 'Qualification Plan evidence operations or artifacts are invalid' USING ERRCODE = 'WQP03';
  END IF;

  -- Record-local uniqueness includes reusable upstream references such as the
  -- Golden FixtureID. Cross-authority reservations below deliberately exclude
  -- that fixture while retaining the single-use InputAuthorityID.
  SELECT array_agg(identity_value ORDER BY pg_catalog.convert_to(identity_value, 'UTF8')),
         count(*), count(DISTINCT identity_value)
  INTO v_record_identity_values, v_identity_count, v_distinct_identity_count
  FROM (
    SELECT p_authority_id::text AS identity_value
    UNION ALL SELECT p_operation_id::text
    UNION ALL SELECT v_input_authority_id::text
    UNION ALL SELECT v_plan_artifact_id
    UNION ALL SELECT v_orchestration_id::text
    UNION ALL SELECT v_qualification_run_id::text
    UNION ALL SELECT v_fixture_id::text
    UNION ALL SELECT v_credential_set_id::text
    UNION ALL SELECT operation.value FROM jsonb_each_text(v_evidence_plan->'operations') AS operation
    UNION ALL
      SELECT artifact->>'encryptionOperationId'
      FROM jsonb_array_elements(v_evidence_plan->'artifacts') AS artifact
      WHERE artifact->>'classification' = 'restricted'
  ) AS identities;
  IF v_record_identity_values IS NULL OR v_identity_count <> v_distinct_identity_count THEN
    RAISE EXCEPTION 'Qualification Plan record identities must be distinct' USING ERRCODE = 'WQP03';
  END IF;

  SELECT array_agg(identity_value ORDER BY pg_catalog.convert_to(identity_value, 'UTF8'))
  INTO v_reserved_identity_values
  FROM (
    SELECT p_authority_id::text AS identity_value
    UNION ALL SELECT p_operation_id::text
    UNION ALL SELECT v_input_authority_id::text
    UNION ALL SELECT v_plan_artifact_id
    UNION ALL SELECT v_orchestration_id::text
    UNION ALL SELECT v_qualification_run_id::text
    UNION ALL SELECT v_credential_set_id::text
    UNION ALL SELECT operation.value
      FROM jsonb_each_text(v_evidence_plan->'operations') AS operation
    UNION ALL
      SELECT artifact->>'encryptionOperationId'
      FROM jsonb_array_elements(v_evidence_plan->'artifacts') AS artifact
      WHERE artifact->>'classification' = 'restricted'
  ) AS identities;

  -- Lock order is shared with migration 000073. A freeze never holds a Plan
  -- table lock while waiting for an Evidence writer, and the reservation guard
  -- never inverts the order by observing a half-frozen authority.
  LOCK TABLE qualification_evidence_events IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_operations IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_evidence_heads IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_authorities IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_plan_identity_reservations IN SHARE ROW EXCLUSIVE MODE;

  SELECT * INTO v_existing
  FROM qualification_plan_authorities
  WHERE operation_id = p_operation_id;
  IF FOUND THEN
    IF v_existing.authority_id <> p_authority_id
       OR v_existing.input_authority_id <> v_input_authority_id
       OR v_existing.request_hash <> p_request_hash OR v_existing.request_bytes <> p_request_bytes OR v_existing.request_document <> p_request_document
       OR v_existing.input_hash <> p_input_hash OR v_existing.input_bytes <> p_input_bytes OR v_existing.input_document <> p_input_document
       OR v_existing.projection_hash <> p_projection_hash OR v_existing.projection_bytes <> p_projection_bytes OR v_existing.projection_document <> p_projection_document
       OR v_existing.evidence_plan_hash <> p_evidence_plan_hash OR v_existing.evidence_plan_bytes <> p_evidence_plan_bytes OR v_existing.evidence_plan_document <> p_evidence_plan_document
       OR v_existing.trust_hash <> p_trust_hash OR v_existing.trust_bytes <> p_trust_bytes OR v_existing.trust_document <> p_trust_document
       OR v_existing.target_hash <> p_target_hash OR v_existing.target_bytes <> p_target_bytes OR v_existing.target_document <> p_target_document
       OR v_existing.envelope_hash <> p_envelope_hash OR v_existing.envelope_bytes <> p_envelope_bytes OR v_existing.envelope_document <> p_envelope_document THEN
      RAISE EXCEPTION 'Qualification Plan operation ID is bound to different exact bytes' USING ERRCODE = 'WQP01';
    END IF;
    RETURN NEXT v_existing;
    RETURN;
  END IF;

  IF EXISTS (
       SELECT 1 FROM qualification_plan_identity_reservations
       WHERE identity_value = ANY(v_reserved_identity_values)
     ) OR EXISTS (
       SELECT 1 FROM qualification_evidence_events
       WHERE event_id::text = ANY(v_reserved_identity_values)
          OR orchestration_id::text = ANY(v_reserved_identity_values)
          OR operation_id::text = ANY(v_reserved_identity_values)
     ) OR EXISTS (
       SELECT 1 FROM qualification_evidence_operations
       WHERE operation_id::text = ANY(v_reserved_identity_values)
     ) OR EXISTS (
       SELECT 1 FROM qualification_evidence_heads AS head
       WHERE head.orchestration_id::text = ANY(v_reserved_identity_values)
          OR head.plan_document->>'runId' = ANY(v_reserved_identity_values)
          OR head.plan_document->'credentialSet'->>'setId' = ANY(v_reserved_identity_values)
          OR head.plan_document->>'qualificationPlanArtifactId' = ANY(v_reserved_identity_values)
          OR EXISTS (
            SELECT 1 FROM jsonb_each_text(head.plan_document->'operations') AS operation
            WHERE operation.value = ANY(v_reserved_identity_values)
          )
          OR EXISTS (
            SELECT 1 FROM jsonb_array_elements(head.plan_document->'artifacts') AS artifact
            WHERE artifact->>'classification' = 'restricted'
              AND artifact->>'encryptionOperationId' = ANY(v_reserved_identity_values)
          )
     ) THEN
    RAISE EXCEPTION 'Qualification Plan identity is already globally reserved or used by legacy Evidence' USING ERRCODE = 'WQP01';
  END IF;

  v_now := date_trunc('milliseconds', clock_timestamp());
  INSERT INTO qualification_plan_authorities (
    authority_id, operation_id, input_authority_id, plan_artifact_id,
    orchestration_id, qualification_run_id, fixture_id, credential_set_id,
    request_hash, request_bytes, request_document,
    input_hash, input_bytes, input_document,
    projection_hash, projection_bytes, projection_document,
    evidence_plan_hash, evidence_plan_bytes, evidence_plan_document,
    trust_hash, trust_bytes, trust_document, trust_bindings_digest, trust_policy_digest,
    target_hash, target_bytes, target_document, project_id, workflow_run_id, node_key,
    target_revision_id, target_revision_content_hash, subject, stage_gate,
    envelope_hash, envelope_bytes, envelope_document,
    source_tree_digest, template_release_digest, frozen_at
  ) VALUES (
    p_authority_id, p_operation_id, v_input_authority_id, v_plan_artifact_id,
    v_orchestration_id, v_qualification_run_id, v_fixture_id, v_credential_set_id,
    p_request_hash, p_request_bytes, p_request_document,
    p_input_hash, p_input_bytes, p_input_document,
    p_projection_hash, p_projection_bytes, p_projection_document,
    p_evidence_plan_hash, p_evidence_plan_bytes, p_evidence_plan_document,
    p_trust_hash, p_trust_bytes, p_trust_document,
    p_envelope_document->>'trustBindingsDigest', p_trust_document->>'trustPolicyDigest',
    p_target_hash, p_target_bytes, p_target_document,
    v_project_id, v_workflow_run_id, v_promotion_target->>'nodeKey',
    v_target_revision_id, v_target_revision->>'contentHash',
    v_promotion_target->>'subject', v_promotion_target->>'stageGate',
    p_envelope_hash, p_envelope_bytes, p_envelope_document,
    v_evidence_plan->>'sourceTreeDigest', v_evidence_plan->>'templateReleaseDigest', v_now
  );

  INSERT INTO qualification_plan_identity_reservations(
    identity_value, authority_id, identity_kind, ordinal, reserved_at
  )
  SELECT identity_value, p_authority_id, identity_kind, ordinal, v_now
  FROM (
    SELECT p_authority_id::text AS identity_value, 'authority'::text AS identity_kind, 0 AS ordinal
    UNION ALL SELECT p_operation_id::text, 'freeze-operation', 0
    UNION ALL SELECT v_input_authority_id::text, 'input-authority', 0
    UNION ALL SELECT v_plan_artifact_id, 'plan-artifact', 0
    UNION ALL SELECT v_orchestration_id::text, 'orchestration', 0
    UNION ALL SELECT v_qualification_run_id::text, 'qualification-run', 0
    UNION ALL SELECT v_credential_set_id::text, 'credential-set', 0
    UNION ALL SELECT v_evidence_plan->'operations'->>'reserve', 'evidence-operation', 0
    UNION ALL SELECT v_evidence_plan->'operations'->>'credentialIssue', 'evidence-operation', 1
    UNION ALL SELECT v_evidence_plan->'operations'->>'runClosure', 'evidence-operation', 2
    UNION ALL SELECT v_evidence_plan->'operations'->>'kmsAttestation', 'evidence-operation', 3
    UNION ALL SELECT v_evidence_plan->'operations'->>'credentialRevocation', 'evidence-operation', 4
    UNION ALL SELECT v_evidence_plan->'operations'->>'artifactIndex', 'evidence-operation', 5
    UNION ALL SELECT v_evidence_plan->'operations'->>'receiptSign', 'evidence-operation', 6
    UNION ALL SELECT v_evidence_plan->'operations'->>'snapshotSeal', 'evidence-operation', 7
    UNION ALL
      SELECT artifact->>'encryptionOperationId', 'evidence-operation', 8 + ordinal::integer
      FROM jsonb_array_elements(v_evidence_plan->'artifacts') WITH ORDINALITY AS item(artifact, ordinal)
      WHERE artifact->>'classification' = 'restricted'
  ) AS reservations
  ORDER BY pg_catalog.convert_to(identity_value, 'UTF8');

  RETURN QUERY
  SELECT * FROM qualification_plan_authorities WHERE authority_id = p_authority_id;
EXCEPTION
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation
    OR serialization_failure OR deadlock_detected OR numeric_value_out_of_range THEN
    RAISE EXCEPTION 'Qualification Plan freeze conflict: %', SQLERRM USING ERRCODE = 'WQP01';
END;
$function$;

-- Resolve is deliberately SECURITY INVOKER. Migration 000074 grants neither
-- table SELECT nor function EXECUTE to a runtime identity, so it does not add
-- a second definer data-exfiltration surface. A future Evidence operator needs
-- a separately reviewed canonical-byte validator and ACL migration.
CREATE FUNCTION resolve_qualification_plan_authority(p_authority_id uuid)
RETURNS SETOF qualification_plan_authorities
LANGUAGE sql
STABLE
SECURITY INVOKER
AS $function$
  SELECT authority.*
  FROM qualification_plan_authorities AS authority
  WHERE authority.authority_id = p_authority_id
$function$;

-- Existing migration-73 orchestrations may continue non-reservation recovery
-- after upgrade. Only a newly inserted `reserved` event must prove it came
-- from one exact immutable Plan authority.
CREATE FUNCTION guard_qualification_evidence_plan_authority()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
DECLARE
  v_authority qualification_plan_authorities%ROWTYPE;
BEGIN
  -- EventIDs are fresh ledger identities, never one of the Plan-owned UUIDs.
  -- OperationID and orchestrationID are intentionally reused by Evidence and
  -- therefore are not subjected to this reverse-collision check.
  IF EXISTS (
    SELECT 1 FROM qualification_plan_identity_reservations
    WHERE identity_value = NEW.event_id::text
  ) THEN
    RAISE EXCEPTION 'Qualification Evidence EventID collides with an immutable Plan identity'
      USING ERRCODE = 'WQP03';
  END IF;
  IF NEW.event_kind <> 'reserved' THEN
    RETURN NEW;
  END IF;
  SELECT * INTO v_authority
  FROM qualification_plan_authorities
  WHERE plan_artifact_id = NEW.event_document->'plan'->>'qualificationPlanArtifactId';
  IF NOT FOUND
     OR NEW.version <> 1
     OR NEW.expected_version <> 0
     OR v_authority.orchestration_id <> NEW.orchestration_id
     OR v_authority.evidence_plan_hash <> NEW.event_document->>'commandHash'
     OR v_authority.evidence_plan_document <> NEW.event_document->'plan'
     OR v_authority.trust_bindings_digest <> NEW.event_document->>'trustBindingsDigest'
     OR v_authority.evidence_plan_document->'operations'->>'reserve' <> NEW.operation_id::text THEN
    RAISE EXCEPTION 'Qualification Evidence reservation is not bound to one exact immutable Plan authority'
      USING ERRCODE = 'WQP03';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER qualification_evidence_plan_authority_guard
BEFORE INSERT ON qualification_evidence_events
FOR EACH ROW EXECUTE FUNCTION guard_qualification_evidence_plan_authority();

DO $qualification_plan_security_posture$
DECLARE
  schema_name text := current_schema();
  role_name text;
BEGIN
  EXECUTE format(
    'ALTER FUNCTION %I.qualification_plan_sha256(bytea) SET search_path TO pg_catalog', schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.reject_qualification_plan_immutable_mutation() SET search_path TO pg_catalog', schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.freeze_qualification_plan_authority('
    'uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,'
    'text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.resolve_qualification_plan_authority(uuid) '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );
  EXECUTE format(
    'ALTER FUNCTION %I.guard_qualification_evidence_plan_authority() '
    'SET search_path TO pg_catalog, %I', schema_name, schema_name
  );

  EXECUTE format('REVOKE ALL ON TABLE %I.qualification_plan_authorities FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON TABLE %I.qualification_plan_identity_reservations FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON FUNCTION %I.qualification_plan_sha256(bytea) FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON FUNCTION %I.reject_qualification_plan_immutable_mutation() FROM PUBLIC', schema_name);
  EXECUTE format(
    'REVOKE ALL ON FUNCTION %I.freeze_qualification_plan_authority('
    'uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,'
    'text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) FROM PUBLIC', schema_name
  );
  EXECUTE format('REVOKE ALL ON FUNCTION %I.resolve_qualification_plan_authority(uuid) FROM PUBLIC', schema_name);
  EXECUTE format('REVOKE ALL ON FUNCTION %I.guard_qualification_evidence_plan_authority() FROM PUBLIC', schema_name);

  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    -- A SECURITY DEFINER owner still needs namespace USAGE for its fixed
    -- search_path to resolve owned relations. Production schemas normally
    -- carry this posture already; make the authority self-contained as well.
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_plan_authorities OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_plan_identity_reservations OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.qualification_plan_sha256(bytea) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.reject_qualification_plan_immutable_mutation() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format(
      'ALTER FUNCTION %I.freeze_qualification_plan_authority('
      'uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,'
      'text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) OWNER TO worksflow_migration_owner', schema_name
    );
    EXECUTE format('ALTER FUNCTION %I.resolve_qualification_plan_authority(uuid) OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.guard_qualification_evidence_plan_authority() OWNER TO worksflow_migration_owner', schema_name);
  END IF;

  FOREACH role_name IN ARRAY ARRAY[
    'worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_repository_index_gc_operator','worksflow_golden_fault_operator',
    'worksflow_qualification_promotion_operator'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('REVOKE ALL ON TABLE %I.qualification_plan_authorities FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON TABLE %I.qualification_plan_identity_reservations FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.qualification_plan_sha256(bytea) FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.reject_qualification_plan_immutable_mutation() FROM %I', schema_name, role_name);
      EXECUTE format(
        'REVOKE ALL ON FUNCTION %I.freeze_qualification_plan_authority('
        'uuid,uuid,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb,'
        'text,bytea,jsonb,text,bytea,jsonb,text,bytea,jsonb) FROM %I', schema_name, role_name
      );
      EXECUTE format('REVOKE ALL ON FUNCTION %I.resolve_qualification_plan_authority(uuid) FROM %I', schema_name, role_name);
      EXECUTE format('REVOKE ALL ON FUNCTION %I.guard_qualification_evidence_plan_authority() FROM %I', schema_name, role_name);
    END IF;
  END LOOP;
END;
$qualification_plan_security_posture$;
