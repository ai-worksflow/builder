-- Never discard Artifact Authority audit evidence or silently make a legacy
-- policy selectable again. Operators must explicitly migrate/remove all v2
-- authority lineage before this schema can be rolled back. Revoked v1 policies
-- remain revoked after a successful rollback.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM template_artifact_authority_receipts)
     OR EXISTS (
       SELECT 1 FROM template_admission_attempts
       WHERE schema_version = 'template-admission-attempt/v2'
          OR authority_receipt_id IS NOT NULL
     )
     OR EXISTS (
       SELECT 1 FROM template_releases
       WHERE schema_version = 'template-release/v2'
          OR authority_receipt_id IS NOT NULL
     )
     OR EXISTS (
       SELECT 1 FROM template_release_policies
       WHERE schema_version = 'template-release-policy/v2'
          OR authority_receipt_id IS NOT NULL
     ) THEN
    RAISE EXCEPTION 'migration 055 rollback requires explicit handling of all v2 Template Artifact Authority lineage and receipts'
      USING ERRCODE = '55000';
  END IF;
END;
$$;

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

  IF TG_OP = 'UPDATE' THEN
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
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_template_release_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM template_admission_attempts AS attempt
    WHERE attempt.id = NEW.admission_attempt_id
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
  ) THEN
    RAISE EXCEPTION 'template release does not exactly match its approved admission attempt'
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
    IF NEW.state <> 'approved' OR NEW.version <> 1 THEN
      RAISE EXCEPTION 'new template release policy must start approved at version 1'
        USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.template_release_id <> OLD.template_release_id
     OR NEW.release_content_hash <> OLD.release_content_hash
     OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'template release policy identity is immutable'
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

ALTER TABLE template_release_policies
  DROP CONSTRAINT template_release_policies_exact_authority_release_fk,
  DROP CONSTRAINT template_release_policies_authority_shape,
  DROP CONSTRAINT template_release_policies_schema_version_check,
  DROP COLUMN authority_policy_hash,
  DROP COLUMN authority_receipt_content_hash,
  DROP COLUMN authority_receipt_id,
  DROP COLUMN schema_version;

ALTER TABLE template_releases
  DROP CONSTRAINT template_releases_exact_authority_identity_unique,
  DROP CONSTRAINT template_releases_authority_receipt_exact_fk,
  DROP CONSTRAINT template_releases_authority_shape,
  DROP CONSTRAINT template_releases_schema_version_check,
  ADD CONSTRAINT template_releases_schema_version_check CHECK (
    schema_version = 'template-release/v1'
  ),
  DROP COLUMN authority_policy_hash,
  DROP COLUMN authority_receipt_content_hash,
  DROP COLUMN authority_receipt_id;

ALTER TABLE template_admission_attempts
  DROP CONSTRAINT template_admission_attempts_authority_receipt_exact_fk,
  DROP CONSTRAINT template_admission_attempts_authority_shape,
  DROP CONSTRAINT template_admission_attempts_schema_version_check,
  ADD CONSTRAINT template_admission_attempts_schema_version_check CHECK (
    schema_version = 'template-admission-attempt/v1'
  ),
  DROP COLUMN authority_policy_hash,
  DROP COLUMN authority_receipt_content_hash,
  DROP COLUMN authority_receipt_id;

DROP TRIGGER template_artifact_authority_receipt_immutable
  ON template_artifact_authority_receipts;
DROP FUNCTION prevent_template_artifact_authority_receipt_mutation();
DROP TRIGGER template_artifact_authority_receipt_exact
  ON template_artifact_authority_receipts;
DROP FUNCTION validate_template_artifact_authority_receipt_insert();
DROP TABLE template_artifact_authority_receipts;
