-- Template admission previously accepted caller-shaped evidence and DSSE
-- metadata without a durable Artifact Authority decision. This migration does
-- not verify registries, signatures, trust roots, or transparency logs inside
-- PostgreSQL. A trusted Artifact Authority writer must perform those external
-- checks first and persist its canonical, passed receipt here. PostgreSQL then
-- makes that receipt immutable and enforces exact receipt lineage everywhere a
-- v2 TemplateRelease can become selectable.

CREATE TABLE template_artifact_authority_receipts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (
    schema_version = 'template-artifact-authority-receipt/v1'
  ),
  decision text NOT NULL CHECK (decision = 'passed'),
  subject_hash text NOT NULL CHECK (subject_hash ~ '^sha256:[0-9a-f]{64}$'),
  source_tree_hash text NOT NULL CHECK (source_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
  sbom_digest text NOT NULL CHECK (sbom_digest ~ '^sha256:[0-9a-f]{64}$'),
  signature_bundle_digest text NOT NULL CHECK (
    signature_bundle_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  policy_hash text NOT NULL CHECK (policy_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  authority_id text NOT NULL CHECK (length(trim(authority_id)) BETWEEN 1 AND 240),
  authority_version text NOT NULL CHECK (length(trim(authority_version)) BETWEEN 1 AND 120),
  verifier_image_digest text NOT NULL CHECK (
    verifier_image_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  trust_root_digest text NOT NULL CHECK (
    trust_root_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  transparency_log_id text NOT NULL CHECK (
    length(trim(transparency_log_id)) BETWEEN 1 AND 240
  ),
  transparency_entry_uuid text NOT NULL CHECK (
    length(transparency_entry_uuid) BETWEEN 8 AND 256
    AND transparency_entry_uuid ~ '^[A-Za-z0-9._:-]+$'
  ),
  transparency_log_index bigint NOT NULL CHECK (transparency_log_index >= 0),
  transparency_bundle_digest text NOT NULL CHECK (
    transparency_bundle_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  transparency_tree_size bigint NOT NULL CHECK (
    transparency_tree_size > 0
    AND transparency_tree_size > transparency_log_index
  ),
  transparency_root_hash text NOT NULL CHECK (
    transparency_root_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  integrated_at timestamptz NOT NULL,
  verification_reference text NOT NULL CHECK (
    length(trim(verification_reference)) > 0
  ),
  verified_at timestamptz NOT NULL,
  recorded_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL,
  document jsonb NOT NULL,
  CONSTRAINT template_artifact_authority_receipt_time_order CHECK (
    integrated_at <= verified_at AND verified_at <= created_at
  ),
  CONSTRAINT template_artifact_authority_receipt_document_exact CHECK (
    jsonb_typeof(document) = 'object'
    AND document ?& ARRAY[
      'id', 'schemaVersion', 'decision', 'subjectHash', 'sourceTreeHash',
      'artifactDigest', 'sbomDigest', 'signatureBundleDigest', 'policyHash',
      'contentHash', 'authority', 'verifierImageDigest', 'trustRootDigest',
      'transparencyLog', 'verificationReference', 'evidence', 'signature',
      'artifactDescriptor', 'sbomDescriptor', 'proof', 'verifiedAt',
      'recordedBy', 'createdAt'
    ]
    AND document->>'id' = id::text
    AND document->>'schemaVersion' = schema_version
    AND document->>'decision' = decision
    AND document->>'subjectHash' = subject_hash
    AND document->>'sourceTreeHash' = source_tree_hash
    AND document->>'artifactDigest' = artifact_digest
    AND document->>'sbomDigest' = sbom_digest
    AND document->>'signatureBundleDigest' = signature_bundle_digest
    AND document->>'policyHash' = policy_hash
    AND document->>'contentHash' = content_hash
    AND document->>'verifierImageDigest' = verifier_image_digest
    AND document->>'trustRootDigest' = trust_root_digest
    AND document->>'verificationReference' = verification_reference
    AND jsonb_typeof(document->'evidence') = 'array'
    AND jsonb_array_length(document->'evidence') = 17
    AND jsonb_typeof(document->'signature') = 'object'
    AND document->'signature'->>'format' = 'dsse'
    AND document->'signature'->>'subjectHash' = subject_hash
    AND document->'signature'->>'bundleDigest' = signature_bundle_digest
    AND document->>'recordedBy' = recorded_by::text
    AND (document->>'verifiedAt')::timestamptz = verified_at
    AND (document->>'createdAt')::timestamptz = created_at
    AND jsonb_typeof(document->'authority') = 'object'
    AND document->'authority' ?& ARRAY['id', 'version']
    AND document->'authority'->>'id' = authority_id
    AND document->'authority'->>'version' = authority_version
    AND jsonb_typeof(document->'transparencyLog') = 'object'
    AND document->'transparencyLog' ?& ARRAY[
      'id', 'entryUuid', 'logIndex', 'integratedAt'
    ]
    AND document->'transparencyLog'->>'id' = transparency_log_id
    AND document->'transparencyLog'->>'entryUuid' = transparency_entry_uuid
    AND (document->'transparencyLog'->>'logIndex')::bigint = transparency_log_index
    AND (document->'transparencyLog'->>'integratedAt')::timestamptz = integrated_at
    AND jsonb_typeof(document->'artifactDescriptor') = 'object'
    AND document->'artifactDescriptor' ?& ARRAY[
      'reference', 'mediaType', 'digest', 'sizeBytes', 'config', 'layers',
      'totalBytes'
    ]
    AND length(trim(document->'artifactDescriptor'->>'reference')) > 0
    AND document->'artifactDescriptor'->>'reference' NOT LIKE '%://%'
    AND right(
      document->'artifactDescriptor'->>'reference',
      length('@' || artifact_digest)
    ) = '@' || artifact_digest
    AND length(trim(document->'artifactDescriptor'->>'mediaType')) > 0
    AND document->'artifactDescriptor'->>'digest' = artifact_digest
    AND (document->'artifactDescriptor'->>'sizeBytes')::bigint > 0
    AND jsonb_typeof(document->'artifactDescriptor'->'config') = 'object'
    AND document->'artifactDescriptor'->'config' ?& ARRAY[
      'mediaType', 'digest', 'sizeBytes'
    ]
    AND jsonb_typeof(document->'artifactDescriptor'->'layers') = 'array'
    AND (document->'artifactDescriptor'->>'totalBytes')::bigint > 0
    AND jsonb_typeof(document->'sbomDescriptor') = 'object'
    AND document->'sbomDescriptor' ?& ARRAY[
      'schemaVersion', 'digest', 'serviceCount', 'services'
    ]
    AND document->'sbomDescriptor'->>'schemaVersion' =
        'worksflow.template-sbom-aggregate/v1'
    AND document->'sbomDescriptor'->>'digest' = sbom_digest
    AND jsonb_typeof(document->'sbomDescriptor'->'services') = 'array'
    AND (document->'sbomDescriptor'->>'serviceCount')::integer =
        jsonb_array_length(document->'sbomDescriptor'->'services')
    AND (document->'sbomDescriptor'->>'serviceCount')::integer > 0
    AND (document->'sbomDescriptor'->>'serviceCount')::integer <= 16
    AND jsonb_typeof(document->'proof') = 'object'
    AND document->'proof' ?& ARRAY[
      'payloadType', 'predicateType', 'payloadDigest',
      'signatureBundleDigest', 'transparencyBundleDigest', 'logId',
      'entryUuid', 'logIndex', 'treeSize', 'rootHash', 'integratedAt',
      'signerIdentities', 'checkpointSignedAt'
    ]
    AND length(trim(document->'proof'->>'payloadType')) > 0
    AND length(trim(document->'proof'->>'predicateType')) > 0
    AND document->'proof'->>'payloadDigest' ~ '^sha256:[0-9a-f]{64}$'
    AND document->'proof'->>'signatureBundleDigest' = signature_bundle_digest
    AND document->'proof'->>'transparencyBundleDigest' = transparency_bundle_digest
    AND document->'proof'->>'logId' = transparency_log_id
    AND document->'proof'->>'entryUuid' = transparency_entry_uuid
    AND (document->'proof'->>'logIndex')::bigint = transparency_log_index
    AND (document->'proof'->>'treeSize')::bigint = transparency_tree_size
    AND (document->'proof'->>'logIndex')::bigint <
        (document->'proof'->>'treeSize')::bigint
    AND document->'proof'->>'rootHash' = transparency_root_hash
    AND (document->'proof'->>'integratedAt')::timestamptz = integrated_at
    AND jsonb_typeof(document->'proof'->'signerIdentities') = 'array'
    AND jsonb_array_length(document->'proof'->'signerIdentities') > 0
    AND (document->'proof'->>'checkpointSignedAt')::timestamptz <= verified_at
  ),
  CONSTRAINT template_artifact_authority_receipt_exact_identity_unique
    UNIQUE (id, content_hash, subject_hash, policy_hash),
  CONSTRAINT template_artifact_authority_receipt_content_hash_unique
    UNIQUE (content_hash)
);

COMMENT ON TABLE template_artifact_authority_receipts IS
  'Immutable passed decisions recorded by an external Template Artifact Authority. PostgreSQL validates document projection and exact lineage only; it does not perform network or cryptographic verification.';

CREATE INDEX template_artifact_authority_receipts_subject_idx
  ON template_artifact_authority_receipts (subject_hash, policy_hash, verified_at DESC);

CREATE FUNCTION validate_template_artifact_authority_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  required_gate text;
  required_gates text[] := ARRAY[
    'source_identity', 'manifest_schema', 'license_spdx',
    'dependency_lock', 'registry_policy', 'install', 'lint', 'typecheck',
    'unit_test', 'build', 'start_health', 'contract_smoke',
    'container_build', 'secret_scan', 'sbom', 'vulnerability',
    'signature_attestation'
  ];
  evidence_count integer;
  distinct_gate_count integer;
  artifact_total_bytes bigint;
  artifact_computed_total_bytes bigint;
  signer_count integer;
  distinct_signer_count integer;
  signer_projection text;
BEGIN
  IF length(trim(NEW.document->'artifactDescriptor'->'config'->>'mediaType')) = 0
     OR NEW.document->'artifactDescriptor'->'config'->>'digest'
          !~ '^sha256:[0-9a-f]{64}$'
     OR (NEW.document->'artifactDescriptor'->'config'->>'sizeBytes')::bigint <= 0
     OR jsonb_array_length(NEW.document->'artifactDescriptor'->'layers') NOT BETWEEN 1 AND 128
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(NEW.document->'artifactDescriptor'->'layers') AS layer
       WHERE jsonb_typeof(layer) <> 'object'
          OR NOT (layer ?& ARRAY['mediaType', 'digest', 'sizeBytes'])
          OR length(trim(layer->>'mediaType')) = 0
          OR layer->>'digest' !~ '^sha256:[0-9a-f]{64}$'
          OR (layer->>'sizeBytes')::bigint <= 0
     ) THEN
    RAISE EXCEPTION 'Template Artifact Authority receipt has an invalid immutable artifact descriptor'
      USING ERRCODE = '23514';
  END IF;

  artifact_total_bytes := (NEW.document->'artifactDescriptor'->>'totalBytes')::bigint;
  SELECT
    (NEW.document->'artifactDescriptor'->>'sizeBytes')::bigint
    + (NEW.document->'artifactDescriptor'->'config'->>'sizeBytes')::bigint
    + COALESCE(sum((layer->>'sizeBytes')::bigint), 0)
    INTO artifact_computed_total_bytes
  FROM jsonb_array_elements(NEW.document->'artifactDescriptor'->'layers') AS layer;
  IF artifact_total_bytes <> artifact_computed_total_bytes THEN
    RAISE EXCEPTION 'Template Artifact Authority artifact totalBytes is not exact for manifest, config, and ordered layers'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM jsonb_array_elements(NEW.document->'sbomDescriptor'->'services') AS service
    WHERE jsonb_typeof(service) <> 'object'
       OR NOT (service ?& ARRAY[
         'serviceId', 'imageReference', 'imageDigest', 'referrerReference',
         'referrerDigest', 'statementDigest', 'predicateDigest',
         'spdxVersion', 'documentNamespace', 'evidenceHash'
       ])
       OR service->>'serviceId' !~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'
       OR length(trim(service->>'imageReference')) = 0
       OR service->>'imageDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR service->>'imageReference' LIKE '%://%'
       OR right(
            service->>'imageReference',
            length('@' || (service->>'imageDigest'))
          ) <> ('@' || (service->>'imageDigest'))
       OR length(trim(service->>'referrerReference')) = 0
       OR service->>'referrerDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR service->>'referrerReference' LIKE '%://%'
       OR right(
            service->>'referrerReference',
            length('@' || (service->>'referrerDigest'))
          ) <> ('@' || (service->>'referrerDigest'))
       OR service->>'statementDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR service->>'predicateDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR service->>'spdxVersion' <> 'SPDX-2.3'
       OR length(trim(service->>'documentNamespace')) = 0
       OR service->>'evidenceHash' !~ '^sha256:[0-9a-f]{64}$'
  ) OR EXISTS (
    SELECT 1
    FROM (
      SELECT
        service->>'serviceId' AS service_id,
        lag((service->>'serviceId') COLLATE "C") OVER (ORDER BY ordinal) AS previous_service_id
      FROM jsonb_array_elements(NEW.document->'sbomDescriptor'->'services')
        WITH ORDINALITY AS item(service, ordinal)
    ) AS ordered
    WHERE ordered.previous_service_id COLLATE "C" >= ordered.service_id COLLATE "C"
  ) THEN
    RAISE EXCEPTION 'Template Artifact Authority SBOM services must be typed and strictly ordered by serviceId'
      USING ERRCODE = '23514';
  END IF;

  SELECT
    count(*), count(DISTINCT signer #>> '{}'),
    string_agg((signer #>> '{}') COLLATE "C", ',' ORDER BY ordinal)
    INTO signer_count, distinct_signer_count, signer_projection
  FROM jsonb_array_elements(NEW.document->'proof'->'signerIdentities')
    WITH ORDINALITY AS item(signer, ordinal);
  IF signer_count <> distinct_signer_count
     OR EXISTS (
       SELECT 1
       FROM jsonb_array_elements(NEW.document->'proof'->'signerIdentities') AS signer
       WHERE jsonb_typeof(signer) <> 'string'
          OR length(trim(signer #>> '{}')) = 0
     )
     OR EXISTS (
       SELECT 1
       FROM (
         SELECT
           signer #>> '{}' AS signer_identity,
           lag((signer #>> '{}') COLLATE "C") OVER (ORDER BY ordinal) AS previous_signer_identity
         FROM jsonb_array_elements(NEW.document->'proof'->'signerIdentities')
           WITH ORDINALITY AS item(signer, ordinal)
       ) AS ordered
       WHERE ordered.previous_signer_identity COLLATE "C" >= ordered.signer_identity COLLATE "C"
     )
     OR signer_projection <> NEW.document->'signature'->>'signer'
     OR (NEW.document->'proof'->>'checkpointSignedAt')::timestamptz < NEW.integrated_at
     OR (NEW.document->'proof'->>'checkpointSignedAt')::timestamptz > NEW.verified_at THEN
    RAISE EXCEPTION 'Template Artifact Authority proof signers or checkpoint time are invalid'
      USING ERRCODE = '23514';
  END IF;

  SELECT count(*), count(DISTINCT item->>'gate')
    INTO evidence_count, distinct_gate_count
  FROM jsonb_array_elements(NEW.document->'evidence') AS item;

  IF evidence_count <> array_length(required_gates, 1)
     OR distinct_gate_count <> array_length(required_gates, 1) THEN
    RAISE EXCEPTION 'Template Artifact Authority receipt requires exactly one evidence record per gate'
      USING ERRCODE = '23514';
  END IF;

  FOREACH required_gate IN ARRAY required_gates LOOP
    IF NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(NEW.document->'evidence') AS item
      WHERE item->>'gate' = required_gate
        AND item->>'outcome' = 'passed'
        AND item->>'subjectHash' = NEW.subject_hash
        AND item->>'digest' ~ '^sha256:[0-9a-f]{64}$'
        AND length(trim(item->>'reference')) > 0
        AND length(trim(item->>'producer')) > 0
        AND length(trim(item->>'invocationId')) > 0
        AND (item->>'observedAt')::timestamptz <= NEW.verified_at
    ) THEN
      RAISE EXCEPTION 'Template Artifact Authority receipt lacks canonical passing evidence for %', required_gate
        USING ERRCODE = '23514';
    END IF;
  END LOOP;

  IF length(trim(NEW.document->'signature'->>'signer')) = 0
     OR length(trim(NEW.document->'signature'->>'transparencyLogRef')) = 0
     OR (NEW.document->'signature'->>'signedAt')::timestamptz > NEW.verified_at THEN
    RAISE EXCEPTION 'Template Artifact Authority receipt signature proof is incomplete or later than verification'
      USING ERRCODE = '23514';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER template_artifact_authority_receipt_exact
BEFORE INSERT ON template_artifact_authority_receipts
FOR EACH ROW EXECUTE FUNCTION validate_template_artifact_authority_receipt_insert();

CREATE FUNCTION prevent_template_artifact_authority_receipt_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Template Artifact Authority receipts are immutable and cannot be updated or deleted'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER template_artifact_authority_receipt_immutable
BEFORE UPDATE OR DELETE ON template_artifact_authority_receipts
FOR EACH ROW EXECUTE FUNCTION prevent_template_artifact_authority_receipt_mutation();

ALTER TABLE template_admission_attempts
  DROP CONSTRAINT template_admission_attempts_schema_version_check,
  ADD COLUMN authority_receipt_id uuid,
  ADD COLUMN authority_receipt_content_hash text,
  ADD COLUMN authority_policy_hash text,
  ADD CONSTRAINT template_admission_attempts_schema_version_check CHECK (
    schema_version IN ('template-admission-attempt/v1', 'template-admission-attempt/v2')
  ),
  ADD CONSTRAINT template_admission_attempts_authority_shape CHECK (
    (
      schema_version = 'template-admission-attempt/v1'
      AND authority_receipt_id IS NULL
      AND authority_receipt_content_hash IS NULL
      AND authority_policy_hash IS NULL
    ) OR (
      schema_version = 'template-admission-attempt/v2'
      AND (
        (
          status IN ('candidate', 'validating', 'rejected')
          AND authority_receipt_id IS NULL
          AND authority_receipt_content_hash IS NULL
          AND authority_policy_hash IS NULL
        ) OR (
          status = 'approved'
          AND authority_receipt_id IS NOT NULL
          AND authority_receipt_content_hash IS NOT NULL
          AND authority_policy_hash IS NOT NULL
        )
      )
    )
  ),
  ADD CONSTRAINT template_admission_attempts_authority_receipt_exact_fk
    FOREIGN KEY (
      authority_receipt_id, authority_receipt_content_hash,
      subject_hash, authority_policy_hash
    ) REFERENCES template_artifact_authority_receipts (
      id, content_hash, subject_hash, policy_hash
    ) ON DELETE RESTRICT;

ALTER TABLE template_releases
  DROP CONSTRAINT template_releases_schema_version_check,
  ADD COLUMN authority_receipt_id uuid,
  ADD COLUMN authority_receipt_content_hash text,
  ADD COLUMN authority_policy_hash text,
  ADD CONSTRAINT template_releases_schema_version_check CHECK (
    schema_version IN ('template-release/v1', 'template-release/v2')
  ),
  ADD CONSTRAINT template_releases_authority_shape CHECK (
    (
      schema_version = 'template-release/v1'
      AND authority_receipt_id IS NULL
      AND authority_receipt_content_hash IS NULL
      AND authority_policy_hash IS NULL
    ) OR (
      schema_version = 'template-release/v2'
      AND authority_receipt_id IS NOT NULL
      AND authority_receipt_content_hash IS NOT NULL
      AND authority_policy_hash IS NOT NULL
    )
  ),
  ADD CONSTRAINT template_releases_authority_receipt_exact_fk
    FOREIGN KEY (
      authority_receipt_id, authority_receipt_content_hash,
      subject_hash, authority_policy_hash
    ) REFERENCES template_artifact_authority_receipts (
      id, content_hash, subject_hash, policy_hash
    ) ON DELETE RESTRICT,
  ADD CONSTRAINT template_releases_exact_authority_identity_unique UNIQUE (
    id, content_hash, authority_receipt_id,
    authority_receipt_content_hash, authority_policy_hash
  );

-- Existing v1 policy rows were admitted without an Artifact Authority receipt.
-- Revoke every still-selectable row inside this migration transaction before
-- installing the v2-only policy trigger. Revocation is intentionally not
-- undone by the down migration.
LOCK TABLE template_release_policies IN ACCESS EXCLUSIVE MODE;

UPDATE template_release_policies
SET state = 'revoked',
    version = version + 1,
    reason = 'revoked by migration 055: exact Template Artifact Authority receipt required; prior reason: ' || reason,
    updated_at = statement_timestamp()
WHERE state = 'approved';

ALTER TABLE template_release_policies
  ADD COLUMN schema_version text NOT NULL DEFAULT 'template-release-policy/v1',
  ADD COLUMN authority_receipt_id uuid,
  ADD COLUMN authority_receipt_content_hash text,
  ADD COLUMN authority_policy_hash text,
  ADD CONSTRAINT template_release_policies_schema_version_check CHECK (
    schema_version IN ('template-release-policy/v1', 'template-release-policy/v2')
  ),
  ADD CONSTRAINT template_release_policies_authority_shape CHECK (
    (
      schema_version = 'template-release-policy/v1'
      AND state IN ('deprecated', 'revoked')
      AND authority_receipt_id IS NULL
      AND authority_receipt_content_hash IS NULL
      AND authority_policy_hash IS NULL
    ) OR (
      schema_version = 'template-release-policy/v2'
      AND authority_receipt_id IS NOT NULL
      AND authority_receipt_content_hash IS NOT NULL
      AND authority_policy_hash IS NOT NULL
    )
  ),
  ADD CONSTRAINT template_release_policies_exact_authority_release_fk
    FOREIGN KEY (
      template_release_id, release_content_hash,
      authority_receipt_id, authority_receipt_content_hash,
      authority_policy_hash
    ) REFERENCES template_releases (
      id, content_hash, authority_receipt_id,
      authority_receipt_content_hash, authority_policy_hash
    ) ON DELETE RESTRICT;

ALTER TABLE template_release_policies
  ALTER COLUMN schema_version SET DEFAULT 'template-release-policy/v2';

CREATE OR REPLACE FUNCTION validate_template_admission_attempt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  required_gate text;
  required_gates text[] := ARRAY[
    'source_identity', 'manifest_schema', 'license_spdx',
    'dependency_lock', 'registry_policy', 'install', 'lint', 'typecheck',
    'unit_test', 'build', 'start_health', 'contract_smoke',
    'container_build', 'secret_scan', 'sbom', 'vulnerability',
    'signature_attestation'
  ];
  evidence_count integer;
  distinct_gate_count integer;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'template admission attempts are append-only audit records'
      USING ERRCODE = '55000';
  END IF;

  IF TG_OP = 'INSERT' AND NEW.status <> 'candidate' THEN
    RAISE EXCEPTION 'new template admission attempts must start as candidates'
      USING ERRCODE = '23514';
  END IF;

  IF TG_OP = 'UPDATE' THEN
    IF OLD.schema_version = 'template-admission-attempt/v1' THEN
      RAISE EXCEPTION 'historical v1 template admission attempts are read-only; start a v2 admission'
        USING ERRCODE = '55000';
    END IF;
    IF NEW.version <> OLD.version + 1 THEN
      RAISE EXCEPTION 'template admission transitions require an exact next version'
        USING ERRCODE = '40001';
    END IF;
    IF NOT (
      (OLD.status = 'candidate' AND NEW.status = 'validating')
      OR (OLD.status = 'validating' AND NEW.status IN ('approved', 'rejected'))
    ) THEN
      RAISE EXCEPTION 'invalid template admission state transition'
        USING ERRCODE = '55000';
    END IF;
    IF ROW(
      OLD.id, OLD.schema_version, OLD.source, OLD.manifest,
      OLD.sbom_digest, OLD.license_expression, OLD.license_digest,
      OLD.subject_hash, OLD.requested_by, OLD.created_at
    ) IS DISTINCT FROM ROW(
      NEW.id, NEW.schema_version, NEW.source, NEW.manifest,
      NEW.sbom_digest, NEW.license_expression, NEW.license_digest,
      NEW.subject_hash, NEW.requested_by, NEW.created_at
    ) THEN
      RAISE EXCEPTION 'template admission candidate identity is immutable'
        USING ERRCODE = '55000';
    END IF;
  END IF;

  IF NEW.status = 'approved' THEN
    IF NEW.schema_version <> 'template-admission-attempt/v2' THEN
      RAISE EXCEPTION 'new v1 template admission approval is disabled; exact Artifact Authority receipt is required'
        USING ERRCODE = '23514';
    END IF;

    SELECT count(*), count(DISTINCT item->>'gate')
      INTO evidence_count, distinct_gate_count
      FROM jsonb_array_elements(NEW.evidence) AS item;
    IF evidence_count <> array_length(required_gates, 1)
       OR distinct_gate_count <> array_length(required_gates, 1) THEN
      RAISE EXCEPTION 'approved template admission requires exactly one evidence record per gate'
        USING ERRCODE = '23514';
    END IF;
    FOREACH required_gate IN ARRAY required_gates LOOP
      IF NOT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(NEW.evidence) AS item
        WHERE item->>'gate' = required_gate
          AND item->>'outcome' = 'passed'
          AND item->>'subjectHash' = NEW.subject_hash
          AND item->>'digest' ~ '^sha256:[0-9a-f]{64}$'
          AND length(trim(item->>'reference')) > 0
          AND length(trim(item->>'producer')) > 0
          AND length(trim(item->>'invocationId')) > 0
          AND item ? 'observedAt'
      ) THEN
        RAISE EXCEPTION 'approved template admission lacks passing exact-subject evidence for %', required_gate
          USING ERRCODE = '23514';
      END IF;
    END LOOP;
    IF NEW.signature->>'format' <> 'dsse'
       OR NEW.signature->>'subjectHash' <> NEW.subject_hash
       OR NEW.signature->>'bundleDigest' !~ '^sha256:[0-9a-f]{64}$'
       OR length(trim(NEW.signature->>'signer')) = 0
       OR length(trim(NEW.signature->>'transparencyLogRef')) = 0
       OR NOT (NEW.signature ? 'signedAt') THEN
      RAISE EXCEPTION 'approved template admission signature is incomplete or does not bind its subject'
        USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
      SELECT 1
      FROM template_artifact_authority_receipts AS receipt
      WHERE receipt.id = NEW.authority_receipt_id
        AND receipt.content_hash = NEW.authority_receipt_content_hash
        AND receipt.subject_hash = NEW.subject_hash
        AND receipt.policy_hash = NEW.authority_policy_hash
        AND receipt.decision = 'passed'
        AND receipt.source_tree_hash = NEW.source->>'treeHash'
        AND receipt.sbom_digest = NEW.sbom_digest
        AND receipt.signature_bundle_digest = NEW.signature->>'bundleDigest'
        AND receipt.verified_at <= NEW.evaluated_at
    ) THEN
      RAISE EXCEPTION 'approved v2 template admission requires an exact passed Artifact Authority receipt'
        USING ERRCODE = '23503';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_template_release_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.schema_version <> 'template-release/v2' THEN
    RAISE EXCEPTION 'new v1 TemplateRelease creation is disabled; exact Artifact Authority receipt is required'
      USING ERRCODE = '23514';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM template_admission_attempts AS attempt
    JOIN template_artifact_authority_receipts AS receipt
      ON receipt.id = attempt.authority_receipt_id
     AND receipt.content_hash = attempt.authority_receipt_content_hash
     AND receipt.subject_hash = attempt.subject_hash
     AND receipt.policy_hash = attempt.authority_policy_hash
     AND receipt.decision = 'passed'
    WHERE attempt.id = NEW.admission_attempt_id
      AND attempt.schema_version = 'template-admission-attempt/v2'
      AND attempt.status = 'approved'
      AND attempt.approved_release_id = NEW.id
      AND attempt.subject_hash = NEW.subject_hash
      AND attempt.source->>'repository' = NEW.source_repository
      AND attempt.source->>'branch' = NEW.source_branch
      AND attempt.source->>'commit' = NEW.source_commit
      AND attempt.source->>'treeHash' = NEW.tree_hash
      AND attempt.manifest = NEW.manifest
      AND attempt.sbom_digest = NEW.sbom_digest
      AND attempt.license_expression = NEW.license_expression
      AND attempt.license_digest = NEW.license_digest
      AND attempt.evidence = NEW.evidence_refs
      AND attempt.signature = NEW.signature
      AND attempt.evaluated_by = NEW.approved_by
      AND attempt.evaluated_at = NEW.approved_at
      AND attempt.authority_receipt_id = NEW.authority_receipt_id
      AND attempt.authority_receipt_content_hash = NEW.authority_receipt_content_hash
      AND attempt.authority_policy_hash = NEW.authority_policy_hash
      AND receipt.source_tree_hash = NEW.tree_hash
      AND receipt.sbom_digest = NEW.sbom_digest
      AND receipt.signature_bundle_digest = NEW.signature->>'bundleDigest'
  ) THEN
    RAISE EXCEPTION 'v2 TemplateRelease does not exactly match its approved admission and passed Artifact Authority receipt'
      USING ERRCODE = '23503';
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_template_release_policy()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'template release policy history cannot be deleted'
      USING ERRCODE = '55000';
  END IF;
  IF TG_OP = 'INSERT' THEN
    IF NEW.schema_version <> 'template-release-policy/v2'
       OR NEW.state <> 'approved'
       OR NEW.version <> 1 THEN
      RAISE EXCEPTION 'new template release policy must start as authority-bound v2 approved at version 1'
        USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
      SELECT 1
      FROM template_releases AS release
      JOIN template_artifact_authority_receipts AS receipt
        ON receipt.id = release.authority_receipt_id
       AND receipt.content_hash = release.authority_receipt_content_hash
       AND receipt.subject_hash = release.subject_hash
       AND receipt.policy_hash = release.authority_policy_hash
       AND receipt.decision = 'passed'
      WHERE release.id = NEW.template_release_id
        AND release.content_hash = NEW.release_content_hash
        AND release.schema_version = 'template-release/v2'
        AND release.authority_receipt_id = NEW.authority_receipt_id
        AND release.authority_receipt_content_hash = NEW.authority_receipt_content_hash
        AND release.authority_policy_hash = NEW.authority_policy_hash
    ) THEN
      RAISE EXCEPTION 'approved v2 template release policy requires the exact release Artifact Authority receipt'
        USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.schema_version = 'template-release-policy/v1' THEN
    RAISE EXCEPTION 'historical v1 template release policies are read-only'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.template_release_id, NEW.release_content_hash, NEW.schema_version,
    NEW.authority_receipt_id, NEW.authority_receipt_content_hash,
    NEW.authority_policy_hash, NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.template_release_id, OLD.release_content_hash, OLD.schema_version,
    OLD.authority_receipt_id, OLD.authority_receipt_content_hash,
    OLD.authority_policy_hash, OLD.created_at
  ) THEN
    RAISE EXCEPTION 'template release policy authority identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  IF NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'template release policy transitions require an exact next version'
      USING ERRCODE = '40001';
  END IF;
  IF OLD.state <> 'approved' OR NEW.state NOT IN ('deprecated', 'revoked') THEN
    RAISE EXCEPTION 'invalid template release policy transition'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;
