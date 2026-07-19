-- Template admission is a platform supply-chain boundary. External template
-- repositories remain candidates until exact source, manifest, and evidence
-- commitments have passed every required gate.

ALTER TABLE artifacts DROP CONSTRAINT artifacts_kind_check;
ALTER TABLE artifacts ADD CONSTRAINT artifacts_kind_check CHECK (kind IN (
  'project_brief', 'product_requirements', 'decision_record',
  'glossary_policy', 'reference_source', 'change_request',
  'requirement_baseline', 'blueprint', 'page_spec', 'prototype',
  'prototype_flow', 'fixture_bundle', 'design_system', 'token_set',
  'component_registry', 'api_contract', 'data_contract',
  'permission_contract', 'ai_runtime_contract', 'deployment_contract',
  'verification_contract', 'workspace', 'test_report', 'quality_report'
));

CREATE TABLE template_admission_attempts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'template-admission-attempt/v1'),
  status text NOT NULL CHECK (status IN ('candidate', 'validating', 'approved', 'rejected')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  source jsonb NOT NULL CHECK (
    jsonb_typeof(source) = 'object'
    AND source ?& ARRAY['repository', 'branch', 'commit', 'treeHash']
    AND source->>'repository' LIKE 'https://%.git'
    AND source->>'commit' ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
    AND source->>'treeHash' ~ '^sha256:[0-9a-f]{64}$'
  ),
  manifest jsonb NOT NULL CHECK (
    jsonb_typeof(manifest) = 'object'
    AND manifest->>'schemaVersion' = 'template-manifest/v1'
    AND length(trim(manifest->>'templateId')) > 0
    AND length(trim(manifest->>'version')) > 0
  ),
  sbom_digest text NOT NULL CHECK (sbom_digest ~ '^sha256:[0-9a-f]{64}$'),
  license_expression text NOT NULL CHECK (
    length(trim(license_expression)) > 0
    AND upper(trim(license_expression)) NOT IN ('NONE', 'NOASSERTION')
  ),
  license_digest text NOT NULL CHECK (license_digest ~ '^sha256:[0-9a-f]{64}$'),
  subject_hash text NOT NULL CHECK (subject_hash ~ '^sha256:[0-9a-f]{64}$'),
  evidence jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
  signature jsonb CHECK (signature IS NULL OR jsonb_typeof(signature) = 'object'),
  findings jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(findings) = 'array'),
  approved_release_id uuid,
  requested_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  evaluated_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  evaluated_at timestamptz,
  CONSTRAINT template_admission_review_independent CHECK (
    evaluated_by IS NULL OR evaluated_by <> requested_by
  ),
  CONSTRAINT template_admission_status_shape CHECK (
    (status IN ('candidate', 'validating')
      AND jsonb_array_length(evidence) = 0
      AND signature IS NULL
      AND jsonb_array_length(findings) = 0
      AND approved_release_id IS NULL
      AND evaluated_by IS NULL
      AND evaluated_at IS NULL)
    OR
    (status = 'approved'
      AND jsonb_array_length(evidence) > 0
      AND signature IS NOT NULL
      AND jsonb_array_length(findings) = 0
      AND approved_release_id IS NOT NULL
      AND evaluated_by IS NOT NULL
      AND evaluated_at IS NOT NULL)
    OR
    (status = 'rejected'
      AND signature IS NOT NULL
      AND jsonb_array_length(findings) > 0
      AND approved_release_id IS NULL
      AND evaluated_by IS NOT NULL
      AND evaluated_at IS NOT NULL)
  )
);

CREATE INDEX template_admission_attempts_status_idx
  ON template_admission_attempts (status, updated_at, id);
CREATE INDEX template_admission_attempts_subject_idx
  ON template_admission_attempts (subject_hash, created_at DESC);

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

CREATE TRIGGER template_admission_attempt_guard
BEFORE INSERT OR UPDATE OR DELETE ON template_admission_attempts
FOR EACH ROW
EXECUTE FUNCTION validate_template_admission_attempt();

CREATE TABLE template_releases (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'template-release/v1'),
  admission_attempt_id uuid NOT NULL UNIQUE REFERENCES template_admission_attempts(id) ON DELETE RESTRICT,
  template_id text NOT NULL CHECK (template_id ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'),
  release_version text NOT NULL CHECK (length(trim(release_version)) > 0),
  source_repository text NOT NULL CHECK (source_repository LIKE 'https://%.git'),
  source_branch text NOT NULL CHECK (length(trim(source_branch)) > 0),
  source_commit text NOT NULL CHECK (source_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  manifest jsonb NOT NULL CHECK (
    jsonb_typeof(manifest) = 'object'
    AND manifest->>'schemaVersion' = 'template-manifest/v1'
    AND manifest->>'templateId' = template_id
    AND manifest->>'version' = release_version
  ),
  sbom_digest text NOT NULL CHECK (sbom_digest ~ '^sha256:[0-9a-f]{64}$'),
  license_expression text NOT NULL CHECK (length(trim(license_expression)) > 0),
  license_digest text NOT NULL CHECK (license_digest ~ '^sha256:[0-9a-f]{64}$'),
  evidence_refs jsonb NOT NULL CHECK (
    jsonb_typeof(evidence_refs) = 'array'
    AND jsonb_array_length(evidence_refs) = 17
  ),
  signature jsonb NOT NULL CHECK (jsonb_typeof(signature) = 'object'),
  subject_hash text NOT NULL CHECK (subject_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  approved_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  approved_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT template_releases_content_hash_unique UNIQUE (content_hash),
  CONSTRAINT template_releases_exact_identity_unique UNIQUE (id, content_hash)
);

CREATE INDEX template_releases_template_version_idx
  ON template_releases (template_id, release_version, approved_at DESC);

ALTER TABLE template_admission_attempts
  ADD CONSTRAINT template_admission_approved_release_fk
  FOREIGN KEY (approved_release_id)
  REFERENCES template_releases(id)
  ON DELETE RESTRICT
  DEFERRABLE INITIALLY DEFERRED;

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

CREATE TRIGGER template_release_exact_admission
BEFORE INSERT ON template_releases
FOR EACH ROW
EXECUTE FUNCTION validate_template_release_insert();

CREATE OR REPLACE FUNCTION prevent_template_release_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'template release content is immutable and cannot be updated or deleted'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER template_release_immutable
BEFORE UPDATE OR DELETE ON template_releases
FOR EACH ROW
EXECUTE FUNCTION prevent_template_release_mutation();

CREATE TABLE template_release_policies (
  template_release_id uuid PRIMARY KEY,
  release_content_hash text NOT NULL,
  state text NOT NULL CHECK (state IN ('approved', 'deprecated', 'revoked')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  reason text NOT NULL CHECK (length(trim(reason)) > 0),
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT template_release_policy_exact_release_fk
    FOREIGN KEY (template_release_id, release_content_hash)
    REFERENCES template_releases(id, content_hash)
    ON DELETE RESTRICT
);

CREATE INDEX template_release_policies_selectable_idx
  ON template_release_policies (state, updated_at DESC)
  WHERE state = 'approved';

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

CREATE TRIGGER template_release_policy_guard
BEFORE INSERT OR UPDATE OR DELETE ON template_release_policies
FOR EACH ROW
EXECUTE FUNCTION validate_template_release_policy();

CREATE TABLE full_stack_template_releases (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'full-stack-template/v1'),
  template_id text NOT NULL CHECK (template_id ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'),
  release_version text NOT NULL CHECK (length(trim(release_version)) > 0),
  document jsonb NOT NULL CHECK (
    jsonb_typeof(document) = 'object'
    AND document->>'id' = id::text
    AND document->>'schemaVersion' = schema_version
    AND document->>'templateId' = template_id
    AND document->>'version' = release_version
    AND jsonb_typeof(document->'components') = 'array'
    AND jsonb_array_length(document->'components') BETWEEN 2 AND 8
    AND jsonb_typeof(document->'layout') = 'object'
  ),
  content_hash text NOT NULL CHECK (
    content_hash ~ '^sha256:[0-9a-f]{64}$'
    AND document->>'contentHash' = content_hash
  ),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT full_stack_template_document_identity CHECK (
    document->>'createdBy' = created_by::text
    AND (document->>'createdAt')::timestamptz = created_at
  ),
  CONSTRAINT full_stack_template_content_hash_unique UNIQUE (content_hash),
  CONSTRAINT full_stack_template_exact_identity_unique UNIQUE (id, content_hash),
  CONSTRAINT full_stack_template_version_unique UNIQUE (template_id, release_version)
);

CREATE TABLE full_stack_template_components (
  full_stack_template_id uuid NOT NULL,
  full_stack_content_hash text NOT NULL,
  role text NOT NULL CHECK (role IN ('web', 'api', 'worker')),
  mount_path text NOT NULL CHECK (
    length(trim(mount_path)) > 0
    AND mount_path !~ '(^/|(^|/)\.\.(/|$)|\\)'
  ),
  template_release_id uuid NOT NULL,
  template_release_content_hash text NOT NULL,
  PRIMARY KEY (full_stack_template_id, role),
  CONSTRAINT full_stack_template_component_mount_unique
    UNIQUE (full_stack_template_id, mount_path),
  CONSTRAINT full_stack_template_component_stack_fk
    FOREIGN KEY (full_stack_template_id, full_stack_content_hash)
    REFERENCES full_stack_template_releases(id, content_hash)
    ON DELETE RESTRICT,
  CONSTRAINT full_stack_template_component_release_fk
    FOREIGN KEY (template_release_id, template_release_content_hash)
    REFERENCES template_releases(id, content_hash)
    ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION validate_full_stack_template_component()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP IN ('UPDATE', 'DELETE') THEN
    RAISE EXCEPTION 'full-stack template component identities are immutable'
      USING ERRCODE = '55000';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM full_stack_template_releases AS full_stack,
         LATERAL jsonb_array_elements(full_stack.document->'components') AS component
    WHERE full_stack.id = NEW.full_stack_template_id
      AND full_stack.content_hash = NEW.full_stack_content_hash
      AND component->>'role' = NEW.role
      AND component->>'mountPath' = NEW.mount_path
      AND component->'release'->>'id' = NEW.template_release_id::text
      AND component->'release'->>'contentHash' = NEW.template_release_content_hash
  ) THEN
    RAISE EXCEPTION 'full-stack component does not exactly match its immutable document'
      USING ERRCODE = '23503';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM template_release_policies AS policy
    WHERE policy.template_release_id = NEW.template_release_id
      AND policy.release_content_hash = NEW.template_release_content_hash
      AND policy.state = 'approved'
  ) THEN
    RAISE EXCEPTION 'full-stack templates may only select approved exact template releases'
      USING ERRCODE = '23503';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER full_stack_template_component_guard
BEFORE INSERT OR UPDATE OR DELETE ON full_stack_template_components
FOR EACH ROW
EXECUTE FUNCTION validate_full_stack_template_component();

CREATE OR REPLACE FUNCTION validate_full_stack_template_complete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  expected_count integer;
  actual_count integer;
BEGIN
  expected_count := jsonb_array_length(NEW.document->'components');
  SELECT count(*) INTO actual_count
  FROM full_stack_template_components AS component
  WHERE component.full_stack_template_id = NEW.id
    AND component.full_stack_content_hash = NEW.content_hash;
  IF actual_count <> expected_count
     OR NOT EXISTS (
       SELECT 1 FROM full_stack_template_components
       WHERE full_stack_template_id = NEW.id
         AND full_stack_content_hash = NEW.content_hash
         AND role = 'web'
     )
     OR NOT EXISTS (
       SELECT 1 FROM full_stack_template_components
       WHERE full_stack_template_id = NEW.id
         AND full_stack_content_hash = NEW.content_hash
         AND role = 'api'
     ) THEN
    RAISE EXCEPTION 'full-stack template requires exact complete web and api components'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER full_stack_template_complete
AFTER INSERT ON full_stack_template_releases
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION validate_full_stack_template_complete();

CREATE OR REPLACE FUNCTION prevent_full_stack_template_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'full-stack template release content is immutable and cannot be updated or deleted'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER full_stack_template_release_immutable
BEFORE UPDATE OR DELETE ON full_stack_template_releases
FOR EACH ROW
EXECUTE FUNCTION prevent_full_stack_template_mutation();
