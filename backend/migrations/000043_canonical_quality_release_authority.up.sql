-- Canonical quality is a new execution over one immutable approved
-- WorkspaceRevision. It is deliberately separate from Candidate receipts and
-- from legacy delivery.quality_runs. Only this authority can build a
-- ReleaseBundle for new publish flows.

CREATE OR REPLACE FUNCTION canonical_release_artifacts_are_valid(
  values_json jsonb,
  minimum_count integer
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array'
      OR jsonb_array_length(values_json) NOT BETWEEN minimum_count AND 128 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'object'
         OR NOT (item.value ?& ARRAY['id','kind','store','ref','contentHash','mediaType','byteSize'])
         OR (SELECT count(*) <> 7 FROM jsonb_object_keys(item.value))
         OR item.value->>'id' !~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
         OR item.value->>'kind' !~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
         OR item.value->>'store' <> btrim(item.value->>'store')
         OR length(item.value->>'store') NOT BETWEEN 1 AND 80
         OR item.value->>'ref' <> btrim(item.value->>'ref')
         OR length(item.value->>'ref') NOT BETWEEN 1 AND 2000
         OR item.value->>'contentHash' !~ '^sha256:[0-9a-f]{64}$'
         OR item.value->>'mediaType' <> btrim(item.value->>'mediaType')
         OR length(item.value->>'mediaType') NOT BETWEEN 1 AND 256
         OR jsonb_typeof(item.value->'byteSize') <> 'number'
         OR (item.value->'byteSize')::text !~ '^[0-9]+$'
         OR length((item.value->'byteSize')::text) > 14
         OR (item.value->>'byteSize')::bigint > 10737418240
    ) AND (
      SELECT count(*) = count(DISTINCT item.value->>'id')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE TABLE canonical_verification_plans (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'canonical-verification-plan/v1'),
  scope text NOT NULL CHECK (scope = 'canonical'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  workspace_content_hash text NOT NULL CHECK (workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^sha256:[0-9a-f]{64}$'),
  full_stack_template_id uuid NOT NULL,
  full_stack_template_hash text NOT NULL CHECK (full_stack_template_hash ~ '^sha256:[0-9a-f]{64}$'),
  verification_profile_id text NOT NULL,
  verification_profile_version bigint NOT NULL CHECK (verification_profile_version > 0),
  verification_profile_hash text NOT NULL CHECK (verification_profile_hash ~ '^sha256:[0-9a-f]{64}$'),
  template_releases jsonb NOT NULL CHECK (verification_template_releases_are_valid(template_releases)),
  obligations jsonb NOT NULL CHECK (verification_obligations_are_valid(obligations)),
  check_ids jsonb NOT NULL CHECK (verification_json_string_array_is_valid(check_ids, 1, 512, 160)),
  required_check_ids jsonb NOT NULL CHECK (
    verification_json_string_array_is_valid(required_check_ids, 1, 512, 160)
    AND verification_string_array_is_subset(required_check_ids, check_ids)
  ),
  check_count integer NOT NULL CHECK (check_count = jsonb_array_length(check_ids) AND check_count BETWEEN 1 AND 512),
  obligation_count integer NOT NULL CHECK (
    obligation_count = jsonb_array_length(obligations) AND obligation_count BETWEEN 1 AND 1024
  ),
  runtime_policy_hash text NOT NULL CHECK (runtime_policy_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT canonical_verification_plan_content_identity CHECK (content_hash = plan_hash),
  CONSTRAINT canonical_verification_plan_exact_unique UNIQUE (id, plan_hash),
  CONSTRAINT canonical_verification_plan_subject_profile_unique UNIQUE (
    workspace_revision_id, workspace_content_hash,
    verification_profile_id, verification_profile_version, verification_profile_hash,
    runtime_policy_hash, plan_hash
  ),
  CONSTRAINT canonical_verification_plan_full_stack_exact_fk
    FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
    REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT canonical_verification_plan_profile_exact_fk
    FOREIGN KEY (verification_profile_id, verification_profile_version, verification_profile_hash)
    REFERENCES verification_profile_versions(profile_id, version, content_hash) ON DELETE RESTRICT
);

CREATE INDEX canonical_verification_plans_subject_idx
  ON canonical_verification_plans (project_id, workspace_revision_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_canonical_verification_plan_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM artifact_revisions AS revision
    JOIN artifacts AS artifact
      ON artifact.id = revision.artifact_id
     AND artifact.project_id = NEW.project_id
     AND artifact.kind = 'workspace'
    JOIN implementation_proposals AS proposal
      ON proposal.id = revision.implementation_proposal_id
     AND proposal.project_id = NEW.project_id
     AND proposal.status IN ('applied', 'partially_applied')
     AND proposal.applied_at IS NOT NULL
    JOIN application_build_manifests AS manifest
      ON manifest.id = NEW.build_manifest_id
     AND manifest.id = proposal.build_manifest_id
     AND manifest.project_id = NEW.project_id
     AND manifest.status = 'consumed'
     AND manifest.invalidated_at IS NULL
    JOIN application_build_contracts AS contract
      ON contract.id = NEW.build_contract_id
     AND contract.id = proposal.application_build_contract_id
     AND contract.project_id = NEW.project_id
     AND contract.status = 'ready'
     AND contract.must_ready_count = contract.must_count
     AND contract.blocking_count = 0
     AND contract.conflict_count = 0
    JOIN full_stack_template_releases AS full_stack
      ON full_stack.id = NEW.full_stack_template_id
     AND full_stack.content_hash = NEW.full_stack_template_hash
    JOIN verification_profile_versions AS profile
      ON profile.profile_id = NEW.verification_profile_id
     AND profile.version = NEW.verification_profile_version
     AND profile.content_hash = NEW.verification_profile_hash
    JOIN verification_profile_policies AS profile_policy
      ON profile_policy.profile_id = profile.profile_id
     AND profile_policy.profile_version = profile.version
     AND profile_policy.profile_hash = profile.content_hash
     AND profile_policy.state = 'active'
    WHERE revision.id = NEW.workspace_revision_id
      AND revision.artifact_id = NEW.workspace_artifact_id
      AND revision.content_hash = NEW.workspace_content_hash
      AND revision.workflow_status = 'approved'
      AND verification_normalize_sha256(manifest.manifest_hash) = NEW.build_manifest_hash
      AND verification_normalize_sha256(contract.contract_hash) = NEW.build_contract_hash
      AND contract.build_manifest_id = NEW.build_manifest_id
      AND verification_normalize_sha256(contract.build_manifest_hash) = NEW.build_manifest_hash
      AND contract.full_stack_template_id = NEW.full_stack_template_id
      AND contract.full_stack_template_hash = NEW.full_stack_template_hash
      AND verification_normalize_sha256(proposal.application_build_contract_hash) = NEW.build_contract_hash
  ) THEN
    RAISE EXCEPTION 'Canonical VerificationPlan requires one exact approved WorkspaceRevision, applied lineage, ready BuildContract, and active profile'
      USING ERRCODE = '40001';
  END IF;

  IF (SELECT count(*) FROM application_build_contract_template_releases WHERE contract_id = NEW.build_contract_id)
       <> jsonb_array_length(NEW.template_releases)
     OR EXISTS (
       SELECT 1
       FROM application_build_contract_template_releases AS release
       WHERE release.contract_id = NEW.build_contract_id
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(NEW.template_releases) AS item(value)
           WHERE item.value->>'role' = release.role
             AND item.value->>'id' = release.template_release_id::text
             AND item.value->>'contentHash' = verification_normalize_sha256(release.template_release_content_hash)
         )
     ) THEN
    RAISE EXCEPTION 'Canonical VerificationPlan TemplateRelease projection is not exact'
      USING ERRCODE = '23514';
  END IF;

  IF (SELECT count(*) FROM application_build_contract_obligations WHERE contract_id = NEW.build_contract_id)
       <> NEW.obligation_count
     OR EXISTS (
       SELECT 1
       FROM application_build_contract_obligations AS obligation
       WHERE obligation.contract_id = NEW.build_contract_id
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(NEW.obligations) AS item(value)
           WHERE item.value->>'id' = obligation.obligation_id
             AND item.value->>'level' = obligation.level
             AND item.value->>'status' = obligation.status
             AND item.value->'oracleIds' = obligation.oracle_ids
         )
     ) THEN
    RAISE EXCEPTION 'Canonical VerificationPlan obligation projection is not exact'
      USING ERRCODE = '23514';
  END IF;

	IF NOT (NEW.check_ids ? 'release-artifacts')
	 OR NOT (NEW.check_ids ? 'release-sbom')
     OR NOT (NEW.check_ids ? 'release-vulnerability')
     OR NOT (NEW.check_ids ? 'release-container-policy')
	 OR NOT (NEW.required_check_ids ? 'release-artifacts')
     OR NOT (NEW.required_check_ids ? 'release-sbom')
     OR NOT (NEW.required_check_ids ? 'release-vulnerability')
     OR NOT (NEW.required_check_ids ? 'release-container-policy') THEN
	RAISE EXCEPTION 'Canonical VerificationPlan requires release artifact, SBOM, vulnerability, and container policy checks'
      USING ERRCODE = '23514';
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER canonical_verification_plan_insert_guard
BEFORE INSERT ON canonical_verification_plans
FOR EACH ROW EXECUTE FUNCTION validate_canonical_verification_plan_insert();

CREATE TABLE canonical_verification_runs (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'canonical-verification-run/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  plan_id uuid NOT NULL,
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_key text NOT NULL CHECK (request_key = btrim(request_key) AND length(request_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  state text NOT NULL CHECK (state IN (
    'queued','claimed','materializing','preparing','running','collecting',
    'passed','failed','error','cancelled','timed_out'
  )),
  version bigint NOT NULL CHECK (version > 0),
  fence_epoch bigint NOT NULL CHECK (fence_epoch >= 0),
  lease_worker_id text,
  lease_epoch bigint CHECK (lease_epoch IS NULL OR lease_epoch > 0),
  lease_expires_at timestamptz,
  terminal_reason text,
  execution_error text,
  started_at timestamptz,
  finished_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT canonical_verification_run_plan_exact_fk
    FOREIGN KEY (plan_id, plan_hash) REFERENCES canonical_verification_plans(id, plan_hash) ON DELETE RESTRICT,
  CONSTRAINT canonical_verification_run_project_request_unique UNIQUE (project_id, request_key),
  CONSTRAINT canonical_verification_run_id_project_unique UNIQUE (id, project_id),
  CONSTRAINT canonical_verification_run_lease_shape CHECK (
    (state IN ('claimed','materializing','preparing','running','collecting')
      AND lease_worker_id IS NOT NULL AND lease_epoch IS NOT NULL AND lease_expires_at IS NOT NULL)
    OR (state NOT IN ('claimed','materializing','preparing','running','collecting')
      AND lease_worker_id IS NULL AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  ),
  CONSTRAINT canonical_verification_run_terminal_shape CHECK (
    (state IN ('passed','failed','error','cancelled','timed_out') AND finished_at IS NOT NULL)
    OR (state NOT IN ('passed','failed','error','cancelled','timed_out') AND finished_at IS NULL)
  )
);

CREATE INDEX canonical_verification_runs_claim_idx
  ON canonical_verification_runs (state, created_at, id)
  WHERE state = 'queued';

CREATE TABLE canonical_verification_receipts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'canonical-verification-receipt/v1'),
  scope text NOT NULL CHECK (scope = 'canonical'),
  run_id uuid NOT NULL UNIQUE REFERENCES canonical_verification_runs(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  plan_id uuid NOT NULL,
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  workspace_content_hash text NOT NULL CHECK (workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^sha256:[0-9a-f]{64}$'),
  full_stack_template_id uuid NOT NULL,
  full_stack_template_hash text NOT NULL CHECK (full_stack_template_hash ~ '^sha256:[0-9a-f]{64}$'),
  verification_profile_id text NOT NULL,
  verification_profile_version bigint NOT NULL CHECK (verification_profile_version > 0),
  verification_profile_hash text NOT NULL CHECK (verification_profile_hash ~ '^sha256:[0-9a-f]{64}$'),
  attempt_ids jsonb NOT NULL CHECK (verification_uuid_array_is_valid(attempt_ids, 1, 64)),
  release_artifacts jsonb NOT NULL,
  check_count integer NOT NULL CHECK (check_count BETWEEN 1 AND 512),
  coverage_count integer NOT NULL CHECK (coverage_count BETWEEN 1 AND 1024),
  must_count integer NOT NULL CHECK (must_count BETWEEN 1 AND coverage_count),
  must_passed_count integer NOT NULL CHECK (must_passed_count BETWEEN 0 AND must_count),
  blocker_count integer NOT NULL CHECK (blocker_count >= 0),
  warning_count integer NOT NULL CHECK (warning_count >= 0),
  decision text NOT NULL CHECK (decision IN ('passed','failed','error')),
  execution_error text,
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT canonical_verification_receipt_exact_unique UNIQUE (id, payload_hash),
  CONSTRAINT canonical_verification_receipt_plan_exact_fk
    FOREIGN KEY (plan_id, plan_hash) REFERENCES canonical_verification_plans(id, plan_hash) ON DELETE RESTRICT,
  CONSTRAINT canonical_verification_receipt_full_stack_exact_fk
    FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
    REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT canonical_verification_receipt_profile_exact_fk
    FOREIGN KEY (verification_profile_id, verification_profile_version, verification_profile_hash)
    REFERENCES verification_profile_versions(profile_id, version, content_hash) ON DELETE RESTRICT,
  CONSTRAINT canonical_verification_receipt_decision_shape CHECK (
    (decision = 'passed' AND execution_error IS NULL AND must_passed_count = must_count
      AND blocker_count = 0 AND canonical_release_artifacts_are_valid(release_artifacts, 1))
    OR (decision = 'failed' AND execution_error IS NULL AND blocker_count > 0
      AND canonical_release_artifacts_are_valid(release_artifacts, 0))
    OR (decision = 'error' AND execution_error IS NOT NULL AND blocker_count > 0
      AND canonical_release_artifacts_are_valid(release_artifacts, 0))
  )
);

CREATE INDEX canonical_verification_receipts_subject_idx
  ON canonical_verification_receipts (project_id, workspace_revision_id, decision, created_at DESC);

CREATE OR REPLACE FUNCTION validate_canonical_verification_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM canonical_verification_runs AS run
    JOIN canonical_verification_plans AS plan
      ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash
    JOIN verification_profile_policies AS profile_policy
      ON profile_policy.profile_id = plan.verification_profile_id
     AND profile_policy.profile_version = plan.verification_profile_version
     AND profile_policy.profile_hash = plan.verification_profile_hash
     AND profile_policy.state = 'active'
    WHERE run.id = NEW.run_id
      AND run.project_id = NEW.project_id
      AND run.plan_id = NEW.plan_id
      AND run.plan_hash = NEW.plan_hash
      AND run.state = NEW.decision
      AND plan.project_id = NEW.project_id
      AND plan.workspace_artifact_id = NEW.workspace_artifact_id
      AND plan.workspace_revision_id = NEW.workspace_revision_id
      AND plan.workspace_content_hash = NEW.workspace_content_hash
      AND plan.build_manifest_id = NEW.build_manifest_id
      AND plan.build_manifest_hash = NEW.build_manifest_hash
      AND plan.build_contract_id = NEW.build_contract_id
      AND plan.build_contract_hash = NEW.build_contract_hash
      AND plan.full_stack_template_id = NEW.full_stack_template_id
      AND plan.full_stack_template_hash = NEW.full_stack_template_hash
      AND plan.verification_profile_id = NEW.verification_profile_id
      AND plan.verification_profile_version = NEW.verification_profile_version
      AND plan.verification_profile_hash = NEW.verification_profile_hash
      AND plan.check_count = NEW.check_count
      AND plan.obligation_count = NEW.coverage_count
  ) THEN
    RAISE EXCEPTION 'Canonical VerificationReceipt requires its exact terminal Run, Plan, WorkspaceRevision, profile, and lineage'
      USING ERRCODE = '40001';
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER canonical_verification_receipt_insert_guard
BEFORE INSERT ON canonical_verification_receipts
FOR EACH ROW EXECUTE FUNCTION validate_canonical_verification_receipt_insert();

CREATE OR REPLACE FUNCTION release_bundle_artifact_set_is_complete(values_json jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT canonical_release_artifacts_are_valid(values_json, 8)
    AND EXISTS (
      SELECT 1 FROM jsonb_array_elements(values_json) AS item(value)
      WHERE item.value->>'kind' IN ('web-static','oci-image','service-image')
    )
    AND NOT EXISTS (
      SELECT required.kind
      FROM unnest(ARRAY[
        'migration', 'runtime-config-schema', 'health-readiness-contract',
        'sbom', 'vulnerability-report', 'provenance', 'signature'
      ]) AS required(kind)
      WHERE NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(values_json) AS item(value)
        WHERE item.value->>'kind' = required.kind
      )
    )
$$;

CREATE TABLE release_bundles (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-bundle/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  workspace_content_hash text NOT NULL CHECK (workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  canonical_receipt_id uuid NOT NULL,
  canonical_receipt_hash text NOT NULL CHECK (canonical_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  release_artifacts jsonb NOT NULL CHECK (release_bundle_artifact_set_is_complete(release_artifacts)),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  bundle_hash text NOT NULL CHECK (bundle_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT release_bundle_exact_unique UNIQUE (id, bundle_hash),
  CONSTRAINT release_bundle_subject_receipt_unique UNIQUE (
    workspace_revision_id, workspace_content_hash, canonical_receipt_id, canonical_receipt_hash
  ),
  CONSTRAINT release_bundle_canonical_receipt_exact_fk
    FOREIGN KEY (canonical_receipt_id, canonical_receipt_hash)
    REFERENCES canonical_verification_receipts(id, payload_hash) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION validate_release_bundle_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM canonical_verification_receipts AS receipt
    JOIN canonical_verification_runs AS run ON run.id = receipt.run_id AND run.state = 'passed'
    WHERE receipt.id = NEW.canonical_receipt_id
      AND receipt.payload_hash = NEW.canonical_receipt_hash
      AND receipt.project_id = NEW.project_id
      AND receipt.decision = 'passed'
      AND receipt.blocker_count = 0
      AND receipt.must_passed_count = receipt.must_count
      AND receipt.workspace_artifact_id = NEW.workspace_artifact_id
      AND receipt.workspace_revision_id = NEW.workspace_revision_id
      AND receipt.workspace_content_hash = NEW.workspace_content_hash
      AND receipt.release_artifacts = NEW.release_artifacts
  ) THEN
    RAISE EXCEPTION 'ReleaseBundle requires one exact passed Canonical Receipt and identical release artifacts'
      USING ERRCODE = '40001';
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_bundle_insert_guard
BEFORE INSERT ON release_bundles
FOR EACH ROW EXECUTE FUNCTION validate_release_bundle_insert();

CREATE OR REPLACE FUNCTION prevent_canonical_quality_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Canonical quality and ReleaseBundle facts are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER canonical_verification_plan_immutable
BEFORE UPDATE OR DELETE ON canonical_verification_plans
FOR EACH ROW EXECUTE FUNCTION prevent_canonical_quality_mutation();

CREATE TRIGGER canonical_verification_receipt_immutable
BEFORE UPDATE OR DELETE ON canonical_verification_receipts
FOR EACH ROW EXECUTE FUNCTION prevent_canonical_quality_mutation();

CREATE TRIGGER release_bundle_immutable
BEFORE UPDATE OR DELETE ON release_bundles
FOR EACH ROW EXECUTE FUNCTION prevent_canonical_quality_mutation();

ALTER TABLE deployment_versions
  ADD COLUMN canonical_receipt_id uuid,
  ADD COLUMN canonical_receipt_hash text,
  ADD COLUMN release_bundle_id uuid,
  ADD COLUMN release_bundle_hash text,
  ADD CONSTRAINT deployment_versions_canonical_receipt_hash_check CHECK (
    canonical_receipt_hash IS NULL OR canonical_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT deployment_versions_release_bundle_hash_check CHECK (
    release_bundle_hash IS NULL OR release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT deployment_versions_canonical_authority_shape CHECK (
    (canonical_receipt_id IS NULL AND canonical_receipt_hash IS NULL
      AND release_bundle_id IS NULL AND release_bundle_hash IS NULL)
    OR (canonical_receipt_id IS NOT NULL AND canonical_receipt_hash IS NOT NULL
      AND release_bundle_id IS NOT NULL AND release_bundle_hash IS NOT NULL)
  ),
  ADD CONSTRAINT deployment_versions_canonical_receipt_exact_fk
    FOREIGN KEY (canonical_receipt_id, canonical_receipt_hash)
    REFERENCES canonical_verification_receipts(id, payload_hash) ON DELETE RESTRICT,
  ADD CONSTRAINT deployment_versions_release_bundle_exact_fk
    FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT;

COMMENT ON TABLE canonical_verification_receipts IS
  'Independent quality authority for an exact immutable WorkspaceRevision; Candidate receipts cannot satisfy this relation.';
COMMENT ON TABLE release_bundles IS
  'Immutable publish handoff assembled only from one exact passed Canonical VerificationReceipt.';
