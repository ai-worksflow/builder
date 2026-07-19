-- Candidate verification control plane. Profile and Plan content is immutable;
-- mutable policy state is kept separately. Run and Attempt projections advance
-- only through versioned, worker-fenced state transitions.

CREATE OR REPLACE FUNCTION verification_normalize_sha256(value text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN value ~ '^sha256:[0-9a-f]{64}$' THEN value
    WHEN value ~ '^[0-9a-f]{64}$' THEN 'sha256:' || value
    ELSE NULL
  END
$$;

CREATE OR REPLACE FUNCTION verification_json_string_array_is_valid(
  values_json jsonb,
  minimum_count integer,
  maximum_count integer,
  maximum_length integer
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array' THEN false
    WHEN jsonb_array_length(values_json) NOT BETWEEN minimum_count AND maximum_count THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'string'
         OR item.value #>> '{}' <> btrim(item.value #>> '{}')
         OR length(item.value #>> '{}') NOT BETWEEN 1 AND maximum_length
         OR item.value #>> '{}' !~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]*$'
    ) AND (
      SELECT count(*) = count(DISTINCT item.value #>> '{}')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE OR REPLACE FUNCTION verification_profile_images_are_valid(values_json jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array'
      OR jsonb_array_length(values_json) NOT BETWEEN 1 AND 16 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'object'
         OR NOT (item.value ?& ARRAY['role', 'image'])
         OR (SELECT count(*) <> 2 FROM jsonb_object_keys(item.value))
         OR item.value->>'role' !~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'
         OR length(item.value->>'role') > 80
         OR item.value->>'image' !~ '^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,255}@sha256:[0-9a-f]{64}$'
    ) AND (
      SELECT count(*) = count(DISTINCT item.value->>'role')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE OR REPLACE FUNCTION verification_string_array_is_subset(
  subset_json jsonb,
  superset_json jsonb
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(subset_json) = 'array'
    AND jsonb_typeof(superset_json) = 'array'
    AND NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements_text(subset_json) AS item(value)
      WHERE NOT superset_json ? item.value
    )
$$;

CREATE TABLE verification_profile_versions (
  profile_id text NOT NULL CHECK (
    profile_id = btrim(profile_id)
    AND profile_id ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
  ),
  version bigint NOT NULL CHECK (version > 0),
  schema_version text NOT NULL CHECK (schema_version = 'verification-profile/v1'),
  document jsonb NOT NULL CHECK (
    jsonb_typeof(document) = 'object'
    AND document->>'schemaVersion' = schema_version
    AND document->>'id' = profile_id
    AND document->>'version' = version::text
    AND document->>'profileHash' ~ '^sha256:[0-9a-f]{64}$'
    AND verification_json_string_array_is_valid(document->'supportedTemplateRoles', 1, 8, 80)
    AND verification_profile_images_are_valid(document->'verifierImages')
    AND jsonb_typeof(document->'builtInChecks') = 'array'
    AND jsonb_array_length(document->'builtInChecks') <= 128
    AND jsonb_typeof(document->'limits') = 'object'
    AND jsonb_typeof(document->'networkPolicy') = 'object'
    AND document->>'state' = 'active'
  ),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  PRIMARY KEY (profile_id, version),
  CONSTRAINT verification_profile_exact_unique UNIQUE (profile_id, version, content_hash),
  CONSTRAINT verification_profile_document_hash CHECK (document->>'profileHash' = content_hash)
);

CREATE TABLE verification_profile_policies (
  profile_id text NOT NULL,
  profile_version bigint NOT NULL,
  profile_hash text NOT NULL CHECK (profile_hash ~ '^sha256:[0-9a-f]{64}$'),
  state text NOT NULL CHECK (state IN ('active', 'deprecated', 'revoked')),
  policy_version bigint NOT NULL CHECK (policy_version > 0),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  PRIMARY KEY (profile_id, profile_version),
  CONSTRAINT verification_profile_policy_exact_fk
    FOREIGN KEY (profile_id, profile_version, profile_hash)
    REFERENCES verification_profile_versions(profile_id, version, content_hash)
    ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION validate_verification_profile_version_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  next_version bigint;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended('verification-profile:' || NEW.profile_id, 0));
  SELECT COALESCE(max(existing.version), 0) + 1 INTO next_version
  FROM verification_profile_versions AS existing
  WHERE existing.profile_id = NEW.profile_id;
  IF NEW.version <> next_version THEN
    RAISE EXCEPTION 'VerificationProfile versions must be contiguous'
      USING ERRCODE = '40001';
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER verification_profile_version_insert_guard
BEFORE INSERT ON verification_profile_versions
FOR EACH ROW EXECUTE FUNCTION validate_verification_profile_version_insert();

CREATE OR REPLACE FUNCTION prevent_verification_profile_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'VerificationProfile version content is immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER verification_profile_version_immutable
BEFORE UPDATE OR DELETE ON verification_profile_versions
FOR EACH ROW EXECUTE FUNCTION prevent_verification_profile_version_mutation();

CREATE OR REPLACE FUNCTION guard_verification_profile_policy()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'VerificationProfile policy history cannot be deleted'
      USING ERRCODE = '55000';
  END IF;
  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'active' OR NEW.policy_version <> 1 THEN
      RAISE EXCEPTION 'VerificationProfile policy must start active at version 1'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    IF ROW(NEW.profile_id, NEW.profile_version, NEW.profile_hash)
       IS DISTINCT FROM ROW(OLD.profile_id, OLD.profile_version, OLD.profile_hash)
       OR NEW.policy_version <> OLD.policy_version + 1
       OR NEW.state = OLD.state
       OR OLD.state = 'revoked'
       OR (OLD.state = 'active' AND NEW.state NOT IN ('deprecated', 'revoked'))
       OR (OLD.state = 'deprecated' AND NEW.state NOT IN ('active', 'revoked')) THEN
      RAISE EXCEPTION 'Invalid VerificationProfile policy CAS transition'
        USING ERRCODE = '40001';
    END IF;
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER verification_profile_policy_guard
BEFORE INSERT OR UPDATE OR DELETE ON verification_profile_policies
FOR EACH ROW EXECUTE FUNCTION guard_verification_profile_policy();

CREATE OR REPLACE FUNCTION verification_template_releases_are_valid(values_json jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array'
      OR jsonb_array_length(values_json) NOT BETWEEN 2 AND 8 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'object'
         OR NOT (item.value ?& ARRAY['role', 'id', 'contentHash'])
         OR (SELECT count(*) <> 3 FROM jsonb_object_keys(item.value))
         OR item.value->>'role' NOT IN ('web', 'api', 'worker')
         OR item.value->>'id' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
         OR item.value->>'contentHash' !~ '^sha256:[0-9a-f]{64}$'
    ) AND (
      SELECT count(*) = count(DISTINCT item.value->>'role')
        AND count(*) = count(DISTINCT item.value->>'id')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE OR REPLACE FUNCTION verification_obligations_are_valid(values_json jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array'
      OR jsonb_array_length(values_json) NOT BETWEEN 1 AND 1024 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'object'
         OR NOT (item.value ?& ARRAY['id', 'level', 'status', 'oracleIds'])
         OR (SELECT count(*) <> 4 FROM jsonb_object_keys(item.value))
         OR item.value->>'id' !~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
         OR item.value->>'level' NOT IN ('must', 'should')
         OR item.value->>'status' NOT IN ('ready', 'waived')
         OR NOT verification_json_string_array_is_valid(
           item.value->'oracleIds',
           CASE WHEN item.value->>'status' = 'ready' THEN 1 ELSE 0 END,
           128,
           160
         )
    ) AND (
      SELECT count(*) = count(DISTINCT item.value->>'id')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE TABLE candidate_verification_plans (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'candidate-verification-plan/v1'),
  scope text NOT NULL CHECK (scope = 'candidate'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  sandbox_session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  session_version bigint NOT NULL CHECK (session_version > 0),
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  candidate_snapshot_id uuid NOT NULL REFERENCES candidate_snapshots(id) ON DELETE RESTRICT,
  candidate_version bigint NOT NULL CHECK (candidate_version > 0),
  journal_sequence bigint NOT NULL CHECK (journal_sequence >= 0),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  writer_lease_epoch bigint NOT NULL CHECK (writer_lease_epoch > 0),
  tree_store text NOT NULL CHECK (tree_store = btrim(tree_store) AND length(tree_store) BETWEEN 1 AND 80),
  tree_owner_id uuid NOT NULL,
  tree_ref text NOT NULL CHECK (tree_ref = btrim(tree_ref) AND length(tree_ref) BETWEEN 1 AND 2000),
  tree_content_hash text NOT NULL CHECK (tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
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
  check_count integer NOT NULL CHECK (
    check_count BETWEEN 1 AND 512
    AND check_count = jsonb_array_length(check_ids)
  ),
  obligation_count integer NOT NULL CHECK (
    obligation_count BETWEEN 1 AND 1024
    AND obligation_count = jsonb_array_length(obligations)
  ),
  runtime_policy_hash text NOT NULL CHECK (runtime_policy_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT candidate_verification_plan_content_identity CHECK (content_hash = plan_hash),
  CONSTRAINT candidate_verification_plan_exact_unique UNIQUE (id, plan_hash),
  CONSTRAINT candidate_verification_plan_subject_profile_unique UNIQUE (
    candidate_snapshot_id,
    verification_profile_id,
    verification_profile_version,
    verification_profile_hash,
    runtime_policy_hash,
    plan_hash
  ),
  CONSTRAINT candidate_verification_plan_full_stack_exact_fk
    FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
    REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_plan_profile_exact_fk
    FOREIGN KEY (
      verification_profile_id,
      verification_profile_version,
      verification_profile_hash
    ) REFERENCES verification_profile_versions(profile_id, version, content_hash)
    ON DELETE RESTRICT
);

CREATE INDEX candidate_verification_plans_subject_idx
  ON candidate_verification_plans (project_id, candidate_id, candidate_snapshot_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_candidate_verification_plan_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  expected_template_count integer;
  expected_obligation_count integer;
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM candidate_workspaces AS candidate
    JOIN candidate_snapshots AS snapshot
      ON snapshot.id = NEW.candidate_snapshot_id
     AND snapshot.project_id = candidate.project_id
     AND snapshot.candidate_id = candidate.id
    JOIN sandbox_sessions AS session
      ON session.id = NEW.sandbox_session_id
     AND session.project_id = candidate.project_id
     AND session.candidate_id = candidate.id
    JOIN application_build_manifests AS manifest
      ON manifest.id = NEW.build_manifest_id
     AND manifest.project_id = candidate.project_id
    JOIN application_build_contracts AS contract
      ON contract.id = NEW.build_contract_id
     AND contract.project_id = candidate.project_id
    JOIN full_stack_template_releases AS full_stack
      ON full_stack.id = NEW.full_stack_template_id
     AND full_stack.content_hash = NEW.full_stack_template_hash
    JOIN verification_profile_versions AS profile
      ON profile.profile_id = NEW.verification_profile_id
     AND profile.version = NEW.verification_profile_version
     AND profile.content_hash = NEW.verification_profile_hash
    JOIN verification_profile_policies AS policy
      ON policy.profile_id = profile.profile_id
     AND policy.profile_version = profile.version
     AND policy.profile_hash = profile.content_hash
     AND policy.state = 'active'
    WHERE candidate.id = NEW.candidate_id
      AND candidate.project_id = NEW.project_id
      AND candidate.status = 'active'
      AND candidate.version = NEW.candidate_version
      AND candidate.journal_sequence = NEW.journal_sequence
      AND candidate.session_epoch = NEW.session_epoch
      AND candidate.writer_lease_epoch = NEW.writer_lease_epoch
      AND NOT candidate.conflicted
      AND NOT candidate.stale
      AND NOT candidate.rebase_required
      AND candidate.current_tree_store = NEW.tree_store
      AND candidate.current_tree_owner_id = NEW.tree_owner_id
      AND candidate.current_tree_ref = NEW.tree_ref
      AND candidate.current_tree_content_hash = NEW.tree_content_hash
      AND candidate.current_tree_hash = NEW.tree_hash
      AND candidate.build_manifest_id = NEW.build_manifest_id
      AND verification_normalize_sha256(candidate.build_manifest_hash) = NEW.build_manifest_hash
      AND candidate.build_contract_id = NEW.build_contract_id
      AND verification_normalize_sha256(candidate.build_contract_hash) = NEW.build_contract_hash
      AND candidate.full_stack_template_id = NEW.full_stack_template_id
      AND candidate.full_stack_template_hash = NEW.full_stack_template_hash
      AND snapshot.candidate_version = NEW.candidate_version
      AND snapshot.journal_sequence = NEW.journal_sequence
      AND snapshot.session_epoch = NEW.session_epoch
      AND snapshot.writer_lease_epoch = NEW.writer_lease_epoch
      AND snapshot.tree_store = NEW.tree_store
      AND snapshot.tree_owner_id = NEW.tree_owner_id
      AND snapshot.tree_ref = NEW.tree_ref
      AND snapshot.tree_content_hash = NEW.tree_content_hash
      AND snapshot.tree_hash = NEW.tree_hash
      AND session.actor_id = NEW.created_by
      AND session.state = 'ready'
      AND session.version = NEW.session_version
      AND session.session_epoch = NEW.session_epoch
      AND session.candidate_version = NEW.candidate_version
      AND session.candidate_journal_sequence = NEW.journal_sequence
      AND session.candidate_writer_lease_epoch = NEW.writer_lease_epoch
      AND session.candidate_tree_store = NEW.tree_store
      AND session.candidate_tree_owner_id = NEW.tree_owner_id
      AND session.candidate_tree_ref = NEW.tree_ref
      AND session.candidate_tree_content_hash = NEW.tree_content_hash
      AND session.candidate_tree_hash = NEW.tree_hash
      AND session.latest_checkpoint_id = NEW.candidate_snapshot_id
      AND session.build_manifest_id = NEW.build_manifest_id
      AND verification_normalize_sha256(session.build_manifest_hash) = NEW.build_manifest_hash
      AND session.build_contract_id = NEW.build_contract_id
      AND verification_normalize_sha256(session.build_contract_hash) = NEW.build_contract_hash
      AND session.full_stack_template_id = NEW.full_stack_template_id
      AND session.full_stack_template_hash = NEW.full_stack_template_hash
      AND manifest.status = 'frozen'
      AND verification_normalize_sha256(manifest.manifest_hash) = NEW.build_manifest_hash
      AND contract.build_manifest_id = NEW.build_manifest_id
      AND verification_normalize_sha256(contract.build_manifest_hash) = NEW.build_manifest_hash
      AND verification_normalize_sha256(contract.contract_hash) = NEW.build_contract_hash
      AND contract.full_stack_template_id = NEW.full_stack_template_id
      AND contract.full_stack_template_hash = NEW.full_stack_template_hash
      AND contract.status = 'ready'
      AND contract.must_ready_count = contract.must_count
      AND contract.blocking_count = 0
      AND contract.conflict_count = 0
  ) THEN
    RAISE EXCEPTION 'VerificationPlan must bind one exact current checkpoint, ready session, lineage, and active profile'
      USING ERRCODE = '40001';
  END IF;

  SELECT count(*) INTO expected_template_count
  FROM application_build_contract_template_releases AS release
  WHERE release.contract_id = NEW.build_contract_id;
  IF expected_template_count <> jsonb_array_length(NEW.template_releases)
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
    RAISE EXCEPTION 'VerificationPlan TemplateRelease projection is not exact'
      USING ERRCODE = '23514';
  END IF;

  SELECT count(*) INTO expected_obligation_count
  FROM application_build_contract_obligations AS obligation
  WHERE obligation.contract_id = NEW.build_contract_id;
  IF expected_obligation_count <> NEW.obligation_count
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
    RAISE EXCEPTION 'VerificationPlan obligation projection is not exact'
      USING ERRCODE = '23514';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_plan_insert_guard
BEFORE INSERT ON candidate_verification_plans
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_plan_insert();

CREATE OR REPLACE FUNCTION prevent_candidate_verification_plan_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Candidate VerificationPlan is immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_verification_plan_immutable
BEFORE UPDATE OR DELETE ON candidate_verification_plans
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_verification_plan_mutation();

CREATE TABLE candidate_verification_runs (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'candidate-verification-run/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  plan_id uuid NOT NULL,
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_key text NOT NULL CHECK (request_key = btrim(request_key) AND length(request_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  parent_run_id uuid,
  retry_reason text CHECK (
    retry_reason IS NULL OR (retry_reason = btrim(retry_reason) AND length(retry_reason) BETWEEN 1 AND 1000)
  ),
  state text NOT NULL CHECK (state IN (
    'queued', 'claimed', 'materializing', 'preparing', 'running', 'collecting',
    'passed', 'failed', 'error', 'cancelled', 'timed_out'
  )),
  version bigint NOT NULL CHECK (version > 0),
  fence_epoch bigint NOT NULL CHECK (fence_epoch >= 0),
  lease_worker_id text CHECK (
    lease_worker_id IS NULL OR (
      lease_worker_id = btrim(lease_worker_id)
      AND length(lease_worker_id) BETWEEN 1 AND 160
      AND lease_worker_id ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]*$'
    )
  ),
  lease_epoch bigint CHECK (lease_epoch IS NULL OR lease_epoch > 0),
  lease_expires_at timestamptz,
  terminal_reason text CHECK (
    terminal_reason IS NULL OR (
      terminal_reason = btrim(terminal_reason) AND length(terminal_reason) BETWEEN 1 AND 2000
    )
  ),
  execution_error text CHECK (
    execution_error IS NULL OR (
      execution_error = btrim(execution_error) AND length(execution_error) BETWEEN 1 AND 2000
    )
  ),
  started_at timestamptz,
  finished_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT candidate_verification_run_plan_exact_fk
    FOREIGN KEY (plan_id, plan_hash)
    REFERENCES candidate_verification_plans(id, plan_hash) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_run_project_request_unique UNIQUE (project_id, request_key),
  CONSTRAINT candidate_verification_run_id_project_unique UNIQUE (id, project_id),
  CONSTRAINT candidate_verification_run_parent_fk
    FOREIGN KEY (parent_run_id, project_id)
    REFERENCES candidate_verification_runs(id, project_id) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_run_retry_shape CHECK (
    (parent_run_id IS NULL AND retry_reason IS NULL)
    OR (parent_run_id IS NOT NULL AND retry_reason IS NOT NULL)
  ),
  CONSTRAINT candidate_verification_run_lease_shape CHECK (
    (state = 'queued' AND fence_epoch = 0 AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
    OR (state = 'cancelled' AND fence_epoch = 0 AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
    OR (state NOT IN ('queued', 'passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND fence_epoch > 0 AND lease_worker_id IS NOT NULL
      AND lease_epoch = fence_epoch AND lease_expires_at IS NOT NULL)
    OR (state IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND fence_epoch > 0 AND lease_worker_id IS NOT NULL
      AND lease_epoch = fence_epoch AND lease_expires_at IS NULL)
  ),
  CONSTRAINT candidate_verification_run_terminal_shape CHECK (
    (state NOT IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND terminal_reason IS NULL AND execution_error IS NULL AND finished_at IS NULL)
    OR (state = 'passed' AND terminal_reason IS NULL AND execution_error IS NULL AND finished_at IS NOT NULL)
    OR (state IN ('failed', 'cancelled', 'timed_out')
      AND terminal_reason IS NOT NULL AND execution_error IS NULL AND finished_at IS NOT NULL)
    OR (state = 'error' AND terminal_reason IS NOT NULL
      AND execution_error IS NOT NULL AND finished_at IS NOT NULL)
  ),
  CONSTRAINT candidate_verification_run_time_order CHECK (
    updated_at >= created_at
    AND (started_at IS NULL OR started_at >= created_at)
    AND (
      finished_at IS NULL
      OR (state = 'cancelled' AND fence_epoch = 0
        AND started_at IS NULL AND finished_at >= created_at)
      OR (started_at IS NOT NULL AND finished_at >= started_at)
    )
  )
);

CREATE INDEX candidate_verification_runs_plan_state_idx
  ON candidate_verification_runs (plan_id, state, updated_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_candidate_verification_run_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.state <> 'queued' OR NEW.version <> 1 OR NEW.fence_epoch <> 0
     OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
     OR NEW.lease_expires_at IS NOT NULL OR NEW.terminal_reason IS NOT NULL
     OR NEW.execution_error IS NOT NULL OR NEW.started_at IS NOT NULL
     OR NEW.finished_at IS NOT NULL THEN
    RAISE EXCEPTION 'Candidate VerificationRun must start queued at version 1 and fence 0'
      USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM candidate_verification_plans AS plan
    JOIN verification_profile_policies AS policy
      ON policy.profile_id = plan.verification_profile_id
     AND policy.profile_version = plan.verification_profile_version
     AND policy.profile_hash = plan.verification_profile_hash
     AND policy.state = 'active'
    WHERE plan.id = NEW.plan_id
      AND plan.plan_hash = NEW.plan_hash
      AND plan.project_id = NEW.project_id
  ) THEN
    RAISE EXCEPTION 'Candidate VerificationRun requires an exact Plan with an active profile'
      USING ERRCODE = '23503';
  END IF;
  IF NEW.parent_run_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM candidate_verification_runs AS parent
    WHERE parent.id = NEW.parent_run_id
      AND parent.project_id = NEW.project_id
      AND parent.plan_id = NEW.plan_id
      AND parent.plan_hash = NEW.plan_hash
      AND parent.state IN ('failed', 'error', 'cancelled', 'timed_out')
  ) THEN
    RAISE EXCEPTION 'Candidate VerificationRun retry must preserve an exact terminal parent Plan'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_by := NEW.created_by;
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_run_insert_guard
BEFORE INSERT ON candidate_verification_runs
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_run_insert();

CREATE OR REPLACE FUNCTION guard_candidate_verification_run_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_terminal boolean := OLD.state IN ('passed', 'failed', 'error', 'cancelled', 'timed_out');
  normal_advance boolean :=
    (OLD.state = 'claimed' AND NEW.state = 'materializing')
    OR (OLD.state = 'materializing' AND NEW.state = 'preparing')
    OR (OLD.state = 'preparing' AND NEW.state = 'running')
    OR (OLD.state = 'running' AND NEW.state = 'collecting')
    OR (OLD.state = 'collecting' AND NEW.state IN ('passed', 'failed'))
    OR (OLD.state NOT IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND NEW.state IN ('error', 'timed_out'));
BEGIN
  IF TG_OP = 'DELETE' OR old_terminal THEN
    RAISE EXCEPTION 'Terminal Candidate VerificationRun facts are immutable'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.plan_id, NEW.plan_hash,
    NEW.request_key, NEW.request_hash, NEW.reason, NEW.parent_run_id,
    NEW.retry_reason, NEW.created_by, NEW.created_at, NEW.creation_transaction_id
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.plan_id, OLD.plan_hash,
    OLD.request_key, OLD.request_hash, OLD.reason, OLD.parent_run_id,
    OLD.retry_reason, OLD.created_by, OLD.created_at, OLD.creation_transaction_id
  ) OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'Candidate VerificationRun transition lost identity or version CAS'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state = 'cancelled' THEN
    IF OLD.state = 'queued' THEN
      IF NEW.fence_epoch <> 0 OR NEW.lease_epoch IS NOT NULL
         OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
         OR NEW.started_at IS NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Queued Candidate VerificationRun cancellation must close without a worker lease'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'Candidate VerificationRun cancellation lost its exact execution fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS NULL OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Candidate VerificationRun claim must establish a live worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'claimed' AND NEW.state = 'claimed' THEN
    IF OLD.lease_expires_at <= statement_timestamp() THEN
      IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
         OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
         OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
        RAISE EXCEPTION 'Candidate VerificationRun reclaim must advance its worker fence'
          USING ERRCODE = '40001';
      END IF;
    ELSIF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at <= OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'Candidate VerificationRun lease renewal lost its worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF normal_advance THEN
    IF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR OLD.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'Candidate VerificationRun transition lost its live worker fence'
        USING ERRCODE = '40001';
    END IF;
    IF NEW.state IN ('passed', 'failed', 'error', 'cancelled', 'timed_out') THEN
      IF NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Terminal Candidate VerificationRun must close its lease and finish time'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Active Candidate VerificationRun changed unrelated lease or finish facts'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'Invalid Candidate VerificationRun state transition'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_run_transition_guard
BEFORE UPDATE OR DELETE ON candidate_verification_runs
FOR EACH ROW EXECUTE FUNCTION guard_candidate_verification_run_transition();

CREATE TABLE candidate_verification_attempts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'candidate-verification-attempt/v1'),
  run_id uuid NOT NULL REFERENCES candidate_verification_runs(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  plan_id uuid NOT NULL,
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 64),
  parent_attempt_id uuid,
  retry_reason text CHECK (
    retry_reason IS NULL OR (retry_reason = btrim(retry_reason) AND length(retry_reason) BETWEEN 1 AND 1000)
  ),
  state text NOT NULL CHECK (state IN (
    'queued', 'claimed', 'materializing', 'preparing', 'running', 'collecting',
    'passed', 'failed', 'error', 'cancelled', 'timed_out'
  )),
  version bigint NOT NULL CHECK (version > 0),
  fence_epoch bigint NOT NULL CHECK (fence_epoch >= 0),
  lease_worker_id text CHECK (
    lease_worker_id IS NULL OR (
      lease_worker_id = btrim(lease_worker_id)
      AND length(lease_worker_id) BETWEEN 1 AND 160
      AND lease_worker_id ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]*$'
    )
  ),
  lease_epoch bigint CHECK (lease_epoch IS NULL OR lease_epoch > 0),
  lease_expires_at timestamptz,
  terminal_reason text CHECK (
    terminal_reason IS NULL OR (
      terminal_reason = btrim(terminal_reason) AND length(terminal_reason) BETWEEN 1 AND 2000
    )
  ),
  execution_error text CHECK (
    execution_error IS NULL OR (
      execution_error = btrim(execution_error) AND length(execution_error) BETWEEN 1 AND 2000
    )
  ),
  started_at timestamptz,
  finished_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT candidate_verification_attempt_run_exact_fk
    FOREIGN KEY (run_id, project_id)
    REFERENCES candidate_verification_runs(id, project_id) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_attempt_plan_exact_fk
    FOREIGN KEY (plan_id, plan_hash)
    REFERENCES candidate_verification_plans(id, plan_hash) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_attempt_run_ordinal_unique UNIQUE (run_id, ordinal),
  CONSTRAINT candidate_verification_attempt_id_run_unique UNIQUE (id, run_id),
  CONSTRAINT candidate_verification_attempt_parent_fk
    FOREIGN KEY (parent_attempt_id, run_id)
    REFERENCES candidate_verification_attempts(id, run_id) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_attempt_retry_shape CHECK (
    (ordinal = 1 AND parent_attempt_id IS NULL AND retry_reason IS NULL)
    OR (ordinal > 1 AND parent_attempt_id IS NOT NULL AND retry_reason IS NOT NULL)
  ),
  CONSTRAINT candidate_verification_attempt_lease_shape CHECK (
    (state = 'queued' AND fence_epoch = 0 AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
    OR (state = 'cancelled' AND fence_epoch = 0 AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
    OR (state NOT IN ('queued', 'passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND fence_epoch > 0 AND lease_worker_id IS NOT NULL
      AND lease_epoch = fence_epoch AND lease_expires_at IS NOT NULL)
    OR (state IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND fence_epoch > 0 AND lease_worker_id IS NOT NULL
      AND lease_epoch = fence_epoch AND lease_expires_at IS NULL)
  ),
  CONSTRAINT candidate_verification_attempt_terminal_shape CHECK (
    (state NOT IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND terminal_reason IS NULL AND execution_error IS NULL AND finished_at IS NULL)
    OR (state = 'passed' AND terminal_reason IS NULL AND execution_error IS NULL AND finished_at IS NOT NULL)
    OR (state IN ('failed', 'cancelled', 'timed_out')
      AND terminal_reason IS NOT NULL AND execution_error IS NULL AND finished_at IS NOT NULL)
    OR (state = 'error' AND terminal_reason IS NOT NULL
      AND execution_error IS NOT NULL AND finished_at IS NOT NULL)
  ),
  CONSTRAINT candidate_verification_attempt_time_order CHECK (
    updated_at >= created_at
    AND (started_at IS NULL OR started_at >= created_at)
    AND (
      finished_at IS NULL
      OR (state = 'cancelled' AND fence_epoch = 0
        AND started_at IS NULL AND finished_at >= created_at)
      OR (started_at IS NOT NULL AND finished_at >= started_at)
    )
  )
);

CREATE INDEX candidate_verification_attempts_run_state_idx
  ON candidate_verification_attempts (run_id, state, ordinal DESC);

CREATE OR REPLACE FUNCTION validate_candidate_verification_attempt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent_run candidate_verification_runs%ROWTYPE;
  current_attempt_count integer;
BEGIN
  SELECT * INTO parent_run
  FROM candidate_verification_runs
  WHERE id = NEW.run_id
  FOR UPDATE;
  IF NOT FOUND OR parent_run.project_id <> NEW.project_id
     OR parent_run.plan_id <> NEW.plan_id OR parent_run.plan_hash <> NEW.plan_hash
     OR parent_run.state IN ('queued', 'passed', 'failed', 'error', 'cancelled', 'timed_out') THEN
    RAISE EXCEPTION 'VerificationAttempt requires an exact claimed nonterminal Run'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.state <> 'queued' OR NEW.version <> 1 OR NEW.fence_epoch <> 0
     OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
     OR NEW.lease_expires_at IS NOT NULL OR NEW.terminal_reason IS NOT NULL
     OR NEW.execution_error IS NOT NULL OR NEW.started_at IS NOT NULL
     OR NEW.finished_at IS NOT NULL THEN
    RAISE EXCEPTION 'VerificationAttempt must start queued at version 1 and fence 0'
      USING ERRCODE = '23514';
  END IF;
  SELECT count(*) INTO current_attempt_count
  FROM candidate_verification_attempts
  WHERE run_id = NEW.run_id;
  IF NEW.ordinal <> current_attempt_count + 1 THEN
    RAISE EXCEPTION 'VerificationAttempt ordinal must be contiguous'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.ordinal > 1 AND NOT EXISTS (
    SELECT 1 FROM candidate_verification_attempts AS parent
    WHERE parent.id = NEW.parent_attempt_id
      AND parent.run_id = NEW.run_id
      AND parent.ordinal = NEW.ordinal - 1
      AND parent.state IN ('failed', 'error', 'cancelled', 'timed_out')
  ) THEN
    RAISE EXCEPTION 'VerificationAttempt retry requires the previous terminal Attempt and explicit reason'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_by := NEW.created_by;
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_attempt_insert_guard
BEFORE INSERT ON candidate_verification_attempts
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_attempt_insert();

CREATE OR REPLACE FUNCTION guard_candidate_verification_attempt_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent_run candidate_verification_runs%ROWTYPE;
  old_terminal boolean := OLD.state IN ('passed', 'failed', 'error', 'cancelled', 'timed_out');
  normal_advance boolean :=
    (OLD.state = 'claimed' AND NEW.state = 'materializing')
    OR (OLD.state = 'materializing' AND NEW.state = 'preparing')
    OR (OLD.state = 'preparing' AND NEW.state = 'running')
    OR (OLD.state = 'running' AND NEW.state = 'collecting')
    OR (OLD.state = 'collecting' AND NEW.state IN ('passed', 'failed'))
    OR (OLD.state NOT IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
      AND NEW.state IN ('error', 'timed_out'));
BEGIN
  IF TG_OP = 'DELETE' OR old_terminal THEN
    RAISE EXCEPTION 'Terminal VerificationAttempt facts are immutable'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.run_id, NEW.project_id, NEW.plan_id,
    NEW.plan_hash, NEW.ordinal, NEW.parent_attempt_id, NEW.retry_reason,
    NEW.created_by, NEW.created_at, NEW.creation_transaction_id
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.run_id, OLD.project_id, OLD.plan_id,
    OLD.plan_hash, OLD.ordinal, OLD.parent_attempt_id, OLD.retry_reason,
    OLD.created_by, OLD.created_at, OLD.creation_transaction_id
  ) OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'VerificationAttempt transition lost identity or version CAS'
      USING ERRCODE = '40001';
  END IF;
  SELECT * INTO parent_run
  FROM candidate_verification_runs AS run
  WHERE run.id = OLD.run_id;
  IF NOT FOUND
     OR parent_run.state IN ('passed', 'failed', 'error', 'cancelled', 'timed_out') THEN
    RAISE EXCEPTION 'VerificationAttempt cannot advance after its Run is terminal'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.state <> 'cancelled' AND (
    parent_run.lease_expires_at IS NULL
    OR parent_run.lease_expires_at <= statement_timestamp()
    OR parent_run.lease_worker_id IS DISTINCT FROM COALESCE(NEW.lease_worker_id, OLD.lease_worker_id)
  ) THEN
    RAISE EXCEPTION 'VerificationAttempt transition lost its parent Run worker fence'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state = 'cancelled' THEN
    IF OLD.state = 'queued' THEN
      IF NEW.fence_epoch <> 0 OR NEW.lease_epoch IS NOT NULL
         OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
         OR NEW.started_at IS NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Queued VerificationAttempt cancellation must close without a worker lease'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'VerificationAttempt cancellation lost its exact execution fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS NULL OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'VerificationAttempt claim must establish a live worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'claimed' AND NEW.state = 'claimed' THEN
    IF OLD.lease_expires_at <= statement_timestamp() THEN
      IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
         OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
         OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
        RAISE EXCEPTION 'VerificationAttempt reclaim must advance its worker fence'
          USING ERRCODE = '40001';
      END IF;
    ELSIF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at <= OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'VerificationAttempt lease renewal lost its worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF normal_advance THEN
    IF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR OLD.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'VerificationAttempt transition lost its live worker fence'
        USING ERRCODE = '40001';
    END IF;
    IF NEW.state IN ('passed', 'failed', 'error', 'cancelled', 'timed_out') THEN
      IF NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Terminal VerificationAttempt must close its lease and finish time'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Active VerificationAttempt changed unrelated lease or finish facts'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'Invalid VerificationAttempt state transition'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_attempt_transition_guard
BEFORE UPDATE OR DELETE ON candidate_verification_attempts
FOR EACH ROW EXECUTE FUNCTION guard_candidate_verification_attempt_transition();

COMMENT ON TABLE verification_profile_versions IS
  'Immutable platform-owned VerificationProfile content; lifecycle state is stored separately.';
COMMENT ON TABLE candidate_verification_plans IS
  'Immutable exact CandidateSnapshot verification input compiled from approved lineage.';
COMMENT ON TABLE candidate_verification_runs IS
  'Versioned and worker-fenced aggregate for one exact Candidate VerificationPlan.';
COMMENT ON TABLE candidate_verification_attempts IS
  'Retry-aware worker-fenced execution attempts; terminal facts are immutable.';
