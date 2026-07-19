-- Stage 2 Agent control plane. ContextPack and TaskCapsule are immutable,
-- exact-tree inputs. AgentAttempt is an append-only event projection with a
-- renewable worker lease; an expired lease can be taken over only by advancing
-- the fencing epoch, so the former worker can never publish a Patch.

CREATE OR REPLACE FUNCTION agent_json_string_array_is_valid(
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
    ) AND (
      SELECT count(*) = count(DISTINCT item.value #>> '{}')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE OR REPLACE FUNCTION agent_path_array_is_valid(
  values_json jsonb,
  minimum_count integer,
  maximum_count integer
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT agent_json_string_array_is_valid(values_json, minimum_count, maximum_count, 1024)
    AND NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements_text(values_json) AS item(path)
      WHERE NOT repository_path_is_safe(item.path)
    )
$$;

CREATE OR REPLACE FUNCTION agent_path_sets_are_disjoint(
  write_set jsonb,
  protected_paths jsonb
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT NOT EXISTS (
    SELECT 1
    FROM jsonb_array_elements_text(write_set) AS writable(path)
    CROSS JOIN jsonb_array_elements_text(protected_paths) AS protected(path)
    WHERE writable.path = protected.path
       OR writable.path LIKE protected.path || '/%'
       OR protected.path LIKE writable.path || '/%'
  )
$$;

CREATE OR REPLACE FUNCTION agent_blob_reference_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(value) = 'object'
    AND value ?& ARRAY['store', 'ownerId', 'ref', 'contentHash', 'byteSize']
    AND NOT value ?| ARRAY['path', 'url', 'secret', 'token']
    AND (SELECT count(*) = 5 FROM jsonb_object_keys(value))
    AND value->>'store' = btrim(value->>'store')
    AND length(value->>'store') BETWEEN 1 AND 80
    AND value->>'ownerId' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    AND value->>'ref' = btrim(value->>'ref')
    AND length(value->>'ref') BETWEEN 1 AND 2000
    AND value->>'contentHash' ~ '^sha256:[0-9a-f]{64}$'
    AND (value->>'byteSize') ~ '^[0-9]+$'
    AND (value->>'byteSize')::bigint BETWEEN 0 AND 4194304
$$;

CREATE OR REPLACE FUNCTION agent_exact_reference_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(value) = 'object'
    AND (SELECT count(*) = 2 FROM jsonb_object_keys(value))
    AND value->>'id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    AND value->>'contentHash' ~ '^(sha256:)?[0-9a-f]{64}$'
$$;

CREATE OR REPLACE FUNCTION agent_context_items_are_valid(items jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(items) <> 'array' OR jsonb_array_length(items) NOT BETWEEN 1 AND 512 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(items) AS entry(value)
      WHERE jsonb_typeof(entry.value) <> 'object'
         OR NOT (entry.value ?& ARRAY['key', 'kind', 'content', 'required'])
         OR entry.value->>'key' <> btrim(entry.value->>'key')
         OR length(entry.value->>'key') NOT BETWEEN 1 AND 240
         OR entry.value->>'key' !~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]*$'
         OR entry.value->>'kind' NOT IN (
           'build_contract', 'source_revision', 'repository_file', 'template_rule',
           'contract_section', 'prototype', 'acceptance', 'diagnostics'
         )
         OR jsonb_typeof(entry.value->'required') <> 'boolean'
         OR NOT agent_blob_reference_is_valid(entry.value->'content')
         OR (entry.value ? 'source' AND NOT agent_exact_reference_is_valid(entry.value->'source'))
         OR (entry.value ? 'path' AND NOT repository_path_is_safe(entry.value->>'path'))
         OR (entry.value->>'kind' = 'repository_file' AND NOT entry.value ? 'path')
         OR EXISTS (
           SELECT 1 FROM jsonb_object_keys(entry.value) AS key(name)
           WHERE key.name NOT IN ('key', 'kind', 'source', 'path', 'content', 'required')
         )
    ) AND (
      SELECT count(*) = count(DISTINCT (entry.value->>'kind', entry.value->>'key'))
      FROM jsonb_array_elements(items) AS entry(value)
    ) AND (
      SELECT COALESCE(sum((entry.value->'content'->>'byteSize')::bigint), 0) <= 33554432
      FROM jsonb_array_elements(items) AS entry(value)
    )
  END
$$;

CREATE OR REPLACE FUNCTION agent_exact_references_are_valid(values_json jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array' OR jsonb_array_length(values_json) NOT BETWEEN 1 AND 16 THEN false
    ELSE NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements(values_json) AS item(value)
      WHERE NOT agent_exact_reference_is_valid(item.value)
         OR item.value->>'contentHash' !~ '^sha256:[0-9a-f]{64}$'
    ) AND (
      SELECT count(*) = count(DISTINCT item.value->>'id')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE OR REPLACE FUNCTION agent_network_policy_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(value) = 'object'
    AND (SELECT count(*) = 2 FROM jsonb_object_keys(value))
    AND value->>'mode' IN ('none', 'registry_proxy')
    AND agent_json_string_array_is_valid(
      value->'allowedHosts',
      CASE WHEN value->>'mode' = 'none' THEN 0 ELSE 1 END,
      CASE WHEN value->>'mode' = 'none' THEN 0 ELSE 64 END,
      253
    )
    AND NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements_text(value->'allowedHosts') AS host(value)
      WHERE host.value <> lower(host.value)
         OR host.value !~ '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$'
         OR host.value LIKE '%..%'
    )
$$;

CREATE OR REPLACE FUNCTION agent_task_budgets_are_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(value) = 'object'
    AND (SELECT count(*) = 6 FROM jsonb_object_keys(value))
    AND (value->>'wallTimeSeconds')::bigint BETWEEN 1 AND 28800
    AND (value->>'maxInputTokens')::bigint BETWEEN 1 AND 4000000
    AND (value->>'maxOutputTokens')::bigint BETWEEN 1 AND 1000000
    AND (value->>'maxCommands')::bigint BETWEEN 1 AND 10000
    AND (value->>'maxLogBytes')::bigint BETWEEN 1024 AND 1073741824
    AND (value->>'maxPatchBytes')::bigint BETWEEN 1 AND 67108864
$$;

CREATE OR REPLACE FUNCTION agent_allowed_tools_are_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT agent_json_string_array_is_valid(value, 1, 16, 80)
    AND NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements_text(value) AS tool(name)
      WHERE tool.name NOT IN ('file.read', 'file.write', 'file.search', 'shell.exec', 'diagnostic.read')
    )
$$;

CREATE TABLE agent_context_packs (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'agent-context-pack/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  base_candidate_tree_hash text NOT NULL CHECK (base_candidate_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  items jsonb NOT NULL CHECK (agent_context_items_are_valid(items)),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT agent_context_pack_exact_unique UNIQUE (id, content_hash),
  CONSTRAINT agent_context_pack_build_contract_fk
    FOREIGN KEY (build_contract_id, build_contract_hash)
    REFERENCES application_build_contracts(id, contract_hash) ON DELETE RESTRICT
);

CREATE INDEX agent_context_packs_project_candidate_idx
  ON agent_context_packs (project_id, candidate_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_agent_context_pack_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_build_manifest_id uuid;
BEGIN
  NEW.created_at := statement_timestamp();
  SELECT candidate.build_manifest_id INTO target_build_manifest_id
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = NEW.candidate_id
    AND candidate.project_id = NEW.project_id
    AND candidate.status = 'active'
    AND candidate.current_tree_hash = NEW.base_candidate_tree_hash
    AND candidate.build_contract_id = NEW.build_contract_id
    AND candidate.build_contract_hash = NEW.build_contract_hash;
  IF NOT FOUND OR NOT exact_application_build_contract_is_ready(
    NEW.build_contract_id, NEW.build_contract_hash, NEW.project_id, target_build_manifest_id
  ) THEN
    RAISE EXCEPTION 'ContextPack must bind the exact active Candidate tree and ready BuildContract'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_context_pack_insert_guard
BEFORE INSERT ON agent_context_packs
FOR EACH ROW EXECUTE FUNCTION validate_agent_context_pack_insert();

CREATE OR REPLACE FUNCTION prevent_agent_immutable_row_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Agent ContextPack and TaskCapsule records are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER agent_context_pack_immutable
BEFORE UPDATE OR DELETE ON agent_context_packs
FOR EACH ROW EXECUTE FUNCTION prevent_agent_immutable_row_mutation();

CREATE TABLE agent_task_capsules (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'agent-task-capsule/v1'),
  task_key text NOT NULL CHECK (
    task_key = btrim(task_key) AND length(task_key) BETWEEN 1 AND 160
    AND task_key ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]*$'
  ),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  sandbox_session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  candidate_version bigint NOT NULL CHECK (candidate_version > 0),
  candidate_session_epoch bigint NOT NULL CHECK (candidate_session_epoch > 0),
  candidate_writer_lease_epoch bigint NOT NULL CHECK (candidate_writer_lease_epoch >= 0),
  base_candidate_tree_hash text NOT NULL CHECK (base_candidate_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  template_releases jsonb NOT NULL CHECK (agent_exact_references_are_valid(template_releases)),
  context_pack_id uuid NOT NULL,
  context_pack_hash text NOT NULL CHECK (context_pack_hash ~ '^sha256:[0-9a-f]{64}$'),
  objective text NOT NULL CHECK (objective = btrim(objective) AND length(objective) BETWEEN 1 AND 4000),
  obligation_ids jsonb NOT NULL CHECK (agent_json_string_array_is_valid(obligation_ids, 1, 512, 160)),
  acceptance_criterion_ids jsonb NOT NULL CHECK (
    agent_json_string_array_is_valid(acceptance_criterion_ids, 1, 512, 160)
  ),
  read_set jsonb NOT NULL CHECK (agent_path_array_is_valid(read_set, 1, 2048)),
  write_set jsonb NOT NULL CHECK (agent_path_array_is_valid(write_set, 1, 1024)),
  protected_paths jsonb NOT NULL CHECK (agent_path_array_is_valid(protected_paths, 1, 1024)),
  preconditions jsonb NOT NULL CHECK (agent_json_string_array_is_valid(preconditions, 1, 256, 2000)),
  postconditions jsonb NOT NULL CHECK (agent_json_string_array_is_valid(postconditions, 1, 256, 2000)),
  verification_command_ids jsonb NOT NULL CHECK (
    agent_json_string_array_is_valid(verification_command_ids, 1, 128, 160)
  ),
  allowed_tools jsonb NOT NULL CHECK (agent_allowed_tools_are_valid(allowed_tools)),
  network_policy jsonb NOT NULL CHECK (agent_network_policy_is_valid(network_policy)),
  budgets jsonb NOT NULL CHECK (agent_task_budgets_are_valid(budgets)),
  output_schema_hash text NOT NULL CHECK (output_schema_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT agent_task_capsule_write_policy CHECK (
    agent_path_sets_are_disjoint(write_set, protected_paths)
  ),
  CONSTRAINT agent_task_capsule_exact_unique UNIQUE (id, content_hash),
  CONSTRAINT agent_task_capsule_operation_unique UNIQUE (
    project_id, sandbox_session_id, task_key, content_hash
  ),
  CONSTRAINT agent_task_capsule_build_contract_fk
    FOREIGN KEY (build_contract_id, build_contract_hash)
    REFERENCES application_build_contracts(id, contract_hash) ON DELETE RESTRICT,
  CONSTRAINT agent_task_capsule_context_fk
    FOREIGN KEY (context_pack_id, context_pack_hash)
    REFERENCES agent_context_packs(id, content_hash) ON DELETE RESTRICT
);

CREATE INDEX agent_task_capsules_session_idx
  ON agent_task_capsules (sandbox_session_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_agent_task_capsule_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  expected_templates jsonb;
BEGIN
  NEW.created_at := statement_timestamp();
  SELECT COALESCE(jsonb_agg(
    jsonb_build_object(
      'id', selected.template_release_id::text,
      'contentHash', selected.template_release_content_hash
    ) ORDER BY selected.template_release_id::text
  ), '[]'::jsonb)
  INTO expected_templates
  FROM sandbox_session_template_releases AS selected
  WHERE selected.session_id = NEW.sandbox_session_id;

  IF expected_templates IS DISTINCT FROM NEW.template_releases
     OR NOT EXISTS (
       SELECT 1
       FROM agent_context_packs AS context_pack
       JOIN candidate_workspaces AS candidate
         ON candidate.id = context_pack.candidate_id
        AND candidate.project_id = context_pack.project_id
       JOIN sandbox_sessions AS session
         ON session.id = NEW.sandbox_session_id
        AND session.project_id = NEW.project_id
        AND session.candidate_id = NEW.candidate_id
       WHERE context_pack.id = NEW.context_pack_id
         AND context_pack.content_hash = NEW.context_pack_hash
         AND context_pack.project_id = NEW.project_id
         AND context_pack.candidate_id = NEW.candidate_id
         AND context_pack.base_candidate_tree_hash = NEW.base_candidate_tree_hash
         AND context_pack.build_contract_id = NEW.build_contract_id
         AND context_pack.build_contract_hash = NEW.build_contract_hash
         AND candidate.status = 'active'
         AND NOT candidate.conflicted
         AND NOT candidate.stale
         AND NOT candidate.rebase_required
         AND candidate.version = NEW.candidate_version
         AND candidate.session_epoch = NEW.candidate_session_epoch
         AND candidate.writer_lease_epoch = NEW.candidate_writer_lease_epoch
         AND candidate.current_tree_hash = NEW.base_candidate_tree_hash
         AND candidate.build_contract_id = NEW.build_contract_id
         AND candidate.build_contract_hash = NEW.build_contract_hash
         AND session.state = 'ready'
         AND session.session_epoch = NEW.candidate_session_epoch
         AND session.candidate_version = NEW.candidate_version
         AND session.candidate_writer_lease_epoch = NEW.candidate_writer_lease_epoch
         AND session.candidate_tree_hash = NEW.base_candidate_tree_hash
         AND session.build_contract_id = NEW.build_contract_id
         AND session.build_contract_hash = NEW.build_contract_hash
     ) THEN
    RAISE EXCEPTION 'TaskCapsule must bind one exact ready Session, Candidate, ContextPack, Contract, and Template set'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_task_capsule_insert_guard
BEFORE INSERT ON agent_task_capsules
FOR EACH ROW EXECUTE FUNCTION validate_agent_task_capsule_insert();

CREATE TRIGGER agent_task_capsule_immutable
BEFORE UPDATE OR DELETE ON agent_task_capsules
FOR EACH ROW EXECUTE FUNCTION prevent_agent_immutable_row_mutation();

CREATE OR REPLACE FUNCTION agent_executor_identity_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(value) = 'object'
    AND (SELECT count(*) = 9 FROM jsonb_object_keys(value))
    AND value->>'adapter' ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,79}$'
    AND value->>'provider' ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,79}$'
    AND value->>'model' = btrim(value->>'model')
    AND length(value->>'model') BETWEEN 1 AND 160
    AND value->>'runnerImageDigest' ~ '^sha256:[0-9a-f]{64}$'
    AND value->>'modelPolicyHash' ~ '^sha256:[0-9a-f]{64}$'
    AND value->>'parametersHash' ~ '^sha256:[0-9a-f]{64}$'
    AND value->>'promptHash' ~ '^sha256:[0-9a-f]{64}$'
    AND value->>'outputSchemaHash' ~ '^sha256:[0-9a-f]{64}$'
    AND value->>'toolchainHash' ~ '^sha256:[0-9a-f]{64}$'
$$;

CREATE OR REPLACE FUNCTION agent_attempt_evidence_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(value) = 'object'
    AND NOT EXISTS (
      SELECT 1 FROM jsonb_object_keys(value) AS key(name)
      WHERE key.name NOT IN ('patch', 'structuredResult', 'stdout', 'stderr', 'validation')
    )
    AND NOT EXISTS (
      SELECT 1 FROM jsonb_each(value) AS item(key, content)
      WHERE NOT agent_blob_reference_is_valid(item.content)
    )
$$;

CREATE TABLE agent_attempts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'agent-attempt/v1'),
  operation_id text NOT NULL CHECK (
    operation_id = btrim(operation_id) AND length(operation_id) BETWEEN 1 AND 128
    AND operation_id ~ '^[A-Za-z0-9._:~-]+$'
  ),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  sandbox_session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  task_capsule_id uuid NOT NULL,
  task_capsule_hash text NOT NULL CHECK (task_capsule_hash ~ '^sha256:[0-9a-f]{64}$'),
  context_pack_id uuid NOT NULL,
  context_pack_hash text NOT NULL CHECK (context_pack_hash ~ '^sha256:[0-9a-f]{64}$'),
  base_candidate_tree_hash text NOT NULL CHECK (base_candidate_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  executor jsonb NOT NULL CHECK (agent_executor_identity_is_valid(executor)),
  request_key_hash text NOT NULL CHECK (request_key_hash ~ '^sha256:[0-9a-f]{64}$'),
  configuration_hash text NOT NULL CHECK (configuration_hash ~ '^sha256:[0-9a-f]{64}$'),
  parent_attempt_id uuid REFERENCES agent_attempts(id) ON DELETE RESTRICT,
  retry_reason text CHECK (
    retry_reason IS NULL OR (retry_reason = btrim(retry_reason) AND length(retry_reason) BETWEEN 1 AND 1000)
  ),
  state text NOT NULL CHECK (state IN (
    'pending', 'ready', 'queued', 'claimed', 'running', 'patch_ready', 'validating',
    'review_ready', 'verification_failed', 'failed', 'timed_out', 'cancelled', 'stale'
  )),
  version bigint NOT NULL CHECK (version > 0),
  fence_epoch bigint NOT NULL CHECK (fence_epoch >= 0),
  lease_worker_id text,
  lease_epoch bigint,
  lease_expires_at timestamptz,
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (agent_attempt_evidence_is_valid(evidence)),
  exit_reason text CHECK (
    exit_reason IS NULL OR (exit_reason = btrim(exit_reason) AND length(exit_reason) BETWEEN 1 AND 2000)
  ),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  started_at timestamptz,
  finished_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT agent_attempt_operation_unique UNIQUE (project_id, created_by, operation_id),
  CONSTRAINT agent_attempt_task_fk
    FOREIGN KEY (task_capsule_id, task_capsule_hash)
    REFERENCES agent_task_capsules(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT agent_attempt_context_fk
    FOREIGN KEY (context_pack_id, context_pack_hash)
    REFERENCES agent_context_packs(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT agent_attempt_parent_shape CHECK (
    (parent_attempt_id IS NULL AND retry_reason IS NULL)
    OR (parent_attempt_id IS NOT NULL AND retry_reason IS NOT NULL)
  ),
  CONSTRAINT agent_attempt_lease_shape CHECK (
    (lease_worker_id IS NULL AND lease_epoch IS NULL AND lease_expires_at IS NULL)
    OR (lease_worker_id IS NOT NULL AND lease_epoch = fence_epoch AND lease_expires_at IS NOT NULL)
  ),
  CONSTRAINT agent_attempt_time_order CHECK (
    updated_at >= created_at
    AND (started_at IS NULL OR started_at >= created_at)
    AND (finished_at IS NULL OR (finished_at >= created_at AND finished_at >= COALESCE(started_at, created_at)))
  )
);

CREATE INDEX agent_attempts_task_idx
  ON agent_attempts (task_capsule_id, created_at DESC, id DESC);
CREATE INDEX agent_attempts_claim_idx
  ON agent_attempts (state, lease_expires_at, created_at, id)
  WHERE state IN ('queued', 'claimed', 'running', 'patch_ready', 'validating');
CREATE INDEX agent_attempts_project_idx
  ON agent_attempts (project_id, updated_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_agent_attempt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent agent_attempts%ROWTYPE;
BEGIN
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  NEW.creation_transaction_id := txid_current();
  IF NEW.state <> 'pending' OR NEW.version <> 1 OR NEW.fence_epoch <> 0
     OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
     OR NEW.lease_expires_at IS NOT NULL OR NEW.evidence <> '{}'::jsonb
     OR NEW.exit_reason IS NOT NULL OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL
     OR NOT EXISTS (
       SELECT 1
       FROM agent_task_capsules AS task
       JOIN agent_context_packs AS context_pack
         ON context_pack.id = task.context_pack_id
        AND context_pack.content_hash = task.context_pack_hash
       JOIN sandbox_sessions AS session
         ON session.id = task.sandbox_session_id
       JOIN candidate_workspaces AS candidate
         ON candidate.id = task.candidate_id
       WHERE task.id = NEW.task_capsule_id
         AND task.content_hash = NEW.task_capsule_hash
         AND task.project_id = NEW.project_id
         AND task.sandbox_session_id = NEW.sandbox_session_id
         AND task.candidate_id = NEW.candidate_id
         AND task.context_pack_id = NEW.context_pack_id
         AND task.context_pack_hash = NEW.context_pack_hash
         AND task.base_candidate_tree_hash = NEW.base_candidate_tree_hash
         AND task.build_contract_hash = NEW.build_contract_hash
         AND session.state = 'ready'
         AND session.candidate_version = task.candidate_version
         AND session.session_epoch = task.candidate_session_epoch
         AND session.candidate_writer_lease_epoch = task.candidate_writer_lease_epoch
         AND session.candidate_tree_hash = task.base_candidate_tree_hash
         AND candidate.status = 'active'
         AND NOT candidate.conflicted
         AND NOT candidate.stale
         AND NOT candidate.rebase_required
         AND candidate.version = task.candidate_version
         AND candidate.session_epoch = task.candidate_session_epoch
         AND candidate.writer_lease_epoch = task.candidate_writer_lease_epoch
         AND candidate.current_tree_hash = task.base_candidate_tree_hash
     ) THEN
    RAISE EXCEPTION 'AgentAttempt must start pending at version 1 against the exact live TaskCapsule tree'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.parent_attempt_id IS NOT NULL THEN
    SELECT * INTO parent FROM agent_attempts WHERE id = NEW.parent_attempt_id FOR SHARE;
    IF NOT FOUND
       OR parent.state NOT IN ('verification_failed', 'failed', 'timed_out', 'cancelled')
       OR parent.task_capsule_id <> NEW.task_capsule_id
       OR parent.task_capsule_hash <> NEW.task_capsule_hash
       OR parent.context_pack_id <> NEW.context_pack_id
       OR parent.context_pack_hash <> NEW.context_pack_hash
       OR parent.request_key_hash <> NEW.request_key_hash
       OR parent.configuration_hash <> NEW.configuration_hash
       OR parent.executor <> NEW.executor THEN
      RAISE EXCEPTION 'AgentAttempt retry must preserve the exact terminal request and executor'
        USING ERRCODE = '40001';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_attempt_insert_guard
BEFORE INSERT ON agent_attempts
FOR EACH ROW EXECUTE FUNCTION validate_agent_attempt_insert();

CREATE OR REPLACE FUNCTION agent_attempt_transition_is_valid(from_state text, to_state text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE from_state
    WHEN 'pending' THEN to_state = 'ready'
    WHEN 'ready' THEN to_state = 'queued'
    WHEN 'claimed' THEN to_state IN ('running', 'failed', 'timed_out')
    WHEN 'running' THEN to_state IN ('patch_ready', 'failed', 'timed_out')
    WHEN 'patch_ready' THEN to_state IN ('validating', 'failed')
    WHEN 'validating' THEN to_state IN ('review_ready', 'verification_failed', 'failed', 'timed_out')
    ELSE false
  END
$$;

CREATE TABLE agent_attempt_events (
  attempt_id uuid NOT NULL REFERENCES agent_attempts(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  sequence bigint NOT NULL CHECK (sequence > 0),
  version_from bigint NOT NULL CHECK (version_from > 0),
  version_to bigint NOT NULL CHECK (version_to = version_from + 1),
  state_from text NOT NULL,
  state_to text NOT NULL,
  fence_epoch_from bigint NOT NULL CHECK (fence_epoch_from >= 0),
  fence_epoch_to bigint NOT NULL CHECK (fence_epoch_to >= fence_epoch_from),
  event_kind text NOT NULL CHECK (event_kind IN (
    'lifecycle.advanced', 'lease.claimed', 'lease.reclaimed', 'lease.renewed',
    'control.cancelled', 'control.stale'
  )),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  worker_id text,
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 2000),
  lease_worker_id_to text,
  lease_epoch_to bigint,
  lease_expires_at_to timestamptz,
  evidence_to jsonb NOT NULL CHECK (agent_attempt_evidence_is_valid(evidence_to)),
  exit_reason_to text,
  started_at_to timestamptz,
  finished_at_to timestamptz,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  PRIMARY KEY (attempt_id, sequence),
  CONSTRAINT agent_attempt_event_version_from_unique UNIQUE (attempt_id, version_from),
  CONSTRAINT agent_attempt_event_version_to_unique UNIQUE (attempt_id, version_to),
  CONSTRAINT agent_attempt_event_lease_shape CHECK (
    (lease_worker_id_to IS NULL AND lease_epoch_to IS NULL AND lease_expires_at_to IS NULL)
    OR (lease_worker_id_to IS NOT NULL AND lease_epoch_to = fence_epoch_to AND lease_expires_at_to IS NOT NULL)
  )
);

CREATE INDEX agent_attempt_events_actor_idx
  ON agent_attempt_events (actor_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_agent_attempt_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent agent_attempts%ROWTYPE;
  worker_state boolean;
  terminal_to boolean;
BEGIN
  SELECT * INTO parent FROM agent_attempts WHERE id = NEW.attempt_id FOR UPDATE;
  IF NOT FOUND
     OR NEW.sequence <> parent.version
     OR NEW.version_from <> parent.version
     OR NEW.version_to <> parent.version + 1
     OR NEW.state_from <> parent.state
     OR NEW.fence_epoch_from <> parent.fence_epoch
     OR parent.state IN ('review_ready', 'verification_failed', 'failed', 'timed_out', 'cancelled', 'stale') THEN
    RAISE EXCEPTION 'AgentAttempt event failed its exact state, version, or fence precondition'
      USING ERRCODE = '40001';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  worker_state := parent.state IN ('claimed', 'running', 'patch_ready', 'validating');
  terminal_to := NEW.state_to IN (
    'review_ready', 'verification_failed', 'failed', 'timed_out', 'cancelled', 'stale'
  );
  IF NEW.event_kind = 'lifecycle.advanced' AND NEW.state_to = 'running' THEN
    NEW.started_at_to := NEW.created_at;
  END IF;
  IF NEW.event_kind = 'lifecycle.advanced' AND terminal_to THEN
    NEW.finished_at_to := NEW.created_at;
  END IF;
  IF NEW.event_kind IN ('control.cancelled', 'control.stale') THEN
    NEW.finished_at_to := NEW.created_at;
    NEW.exit_reason_to := NEW.reason;
  END IF;

  IF NEW.event_kind = 'lease.claimed' THEN
    IF parent.state <> 'queued' OR parent.lease_worker_id IS NOT NULL
       OR NEW.state_to <> 'claimed' OR NEW.fence_epoch_to <> parent.fence_epoch + 1
       OR NEW.worker_id IS NULL OR NEW.lease_worker_id_to <> NEW.worker_id
       OR NEW.lease_epoch_to <> NEW.fence_epoch_to
       OR NEW.lease_expires_at_to <= NEW.created_at
       OR NEW.lease_expires_at_to > NEW.created_at + interval '10 minutes'
       OR NEW.evidence_to <> parent.evidence OR NEW.exit_reason_to IS DISTINCT FROM parent.exit_reason
       OR NEW.started_at_to IS DISTINCT FROM parent.started_at
       OR NEW.finished_at_to IS DISTINCT FROM parent.finished_at THEN
      RAISE EXCEPTION 'invalid first AgentAttempt worker claim' USING ERRCODE = '55000';
    END IF;
  ELSIF NEW.event_kind = 'lease.reclaimed' THEN
    IF NOT worker_state OR parent.lease_expires_at IS NULL OR parent.lease_expires_at > NEW.created_at
       OR NEW.state_to <> parent.state OR NEW.fence_epoch_to <> parent.fence_epoch + 1
       OR NEW.worker_id IS NULL OR NEW.lease_worker_id_to <> NEW.worker_id
       OR NEW.lease_epoch_to <> NEW.fence_epoch_to
       OR NEW.lease_expires_at_to <= NEW.created_at
       OR NEW.lease_expires_at_to > NEW.created_at + interval '10 minutes'
       OR NEW.evidence_to <> parent.evidence OR NEW.exit_reason_to IS DISTINCT FROM parent.exit_reason
       OR NEW.started_at_to IS DISTINCT FROM parent.started_at
       OR NEW.finished_at_to IS DISTINCT FROM parent.finished_at THEN
      RAISE EXCEPTION 'invalid AgentAttempt worker lease takeover' USING ERRCODE = '55000';
    END IF;
  ELSIF NEW.event_kind = 'lease.renewed' THEN
    IF NOT worker_state OR parent.lease_worker_id IS NULL OR parent.lease_expires_at <= NEW.created_at
       OR NEW.state_to <> parent.state OR NEW.fence_epoch_to <> parent.fence_epoch
       OR NEW.worker_id IS DISTINCT FROM parent.lease_worker_id
       OR NEW.lease_worker_id_to IS DISTINCT FROM parent.lease_worker_id
       OR NEW.lease_epoch_to IS DISTINCT FROM parent.lease_epoch
       OR NEW.lease_expires_at_to <= parent.lease_expires_at
       OR NEW.lease_expires_at_to > NEW.created_at + interval '10 minutes'
       OR NEW.evidence_to <> parent.evidence OR NEW.exit_reason_to IS DISTINCT FROM parent.exit_reason
       OR NEW.started_at_to IS DISTINCT FROM parent.started_at
       OR NEW.finished_at_to IS DISTINCT FROM parent.finished_at THEN
      RAISE EXCEPTION 'invalid AgentAttempt worker lease renewal' USING ERRCODE = '55000';
    END IF;
  ELSIF NEW.event_kind IN ('control.cancelled', 'control.stale') THEN
    IF NEW.state_to <> (CASE WHEN NEW.event_kind = 'control.cancelled' THEN 'cancelled' ELSE 'stale' END)
       OR NEW.fence_epoch_to <> parent.fence_epoch + 1
       OR NEW.worker_id IS NOT NULL OR NEW.lease_worker_id_to IS NOT NULL
       OR NEW.lease_epoch_to IS NOT NULL OR NEW.lease_expires_at_to IS NOT NULL
       OR NEW.evidence_to <> parent.evidence OR NEW.exit_reason_to <> NEW.reason
       OR NEW.started_at_to IS DISTINCT FROM parent.started_at
       OR NEW.finished_at_to IS DISTINCT FROM NEW.created_at THEN
      RAISE EXCEPTION 'invalid AgentAttempt cancel/stale control transition' USING ERRCODE = '55000';
    END IF;
  ELSIF NEW.event_kind = 'lifecycle.advanced' THEN
    IF NOT agent_attempt_transition_is_valid(parent.state, NEW.state_to)
       OR NEW.fence_epoch_to <> parent.fence_epoch
       OR NOT (NEW.evidence_to @> parent.evidence) THEN
      RAISE EXCEPTION 'invalid AgentAttempt lifecycle transition' USING ERRCODE = '55000';
    END IF;
    IF worker_state THEN
      IF parent.lease_worker_id IS NULL OR parent.lease_expires_at <= NEW.created_at
         OR NEW.worker_id IS DISTINCT FROM parent.lease_worker_id
         OR NEW.lease_worker_id_to IS DISTINCT FROM (CASE WHEN terminal_to THEN NULL ELSE parent.lease_worker_id END)
         OR NEW.lease_epoch_to IS DISTINCT FROM (CASE WHEN terminal_to THEN NULL ELSE parent.lease_epoch END)
         OR NEW.lease_expires_at_to IS DISTINCT FROM (CASE WHEN terminal_to THEN NULL ELSE parent.lease_expires_at END) THEN
        RAISE EXCEPTION 'AgentAttempt lifecycle writer is not the current unexpired worker'
          USING ERRCODE = '40001';
      END IF;
    ELSE
      IF NEW.worker_id IS NOT NULL OR NEW.lease_worker_id_to IS NOT NULL
         OR NEW.lease_epoch_to IS NOT NULL OR NEW.lease_expires_at_to IS NOT NULL THEN
        RAISE EXCEPTION 'pre-worker AgentAttempt lifecycle cannot smuggle a lease'
          USING ERRCODE = '40001';
      END IF;
    END IF;
    IF NEW.state_to = 'running' AND (
      NEW.started_at_to IS NULL OR NEW.started_at_to <> NEW.created_at OR parent.started_at IS NOT NULL
    ) THEN
      RAISE EXCEPTION 'AgentAttempt running transition must capture its platform start time'
        USING ERRCODE = '55000';
    ELSIF NEW.state_to <> 'running' AND NEW.started_at_to IS DISTINCT FROM parent.started_at THEN
      RAISE EXCEPTION 'AgentAttempt start time is immutable after running'
        USING ERRCODE = '55000';
    END IF;
    IF NEW.state_to = 'patch_ready' AND (
      NOT NEW.evidence_to ? 'patch' OR NOT NEW.evidence_to ? 'structuredResult'
    ) THEN
      RAISE EXCEPTION 'patch_ready requires platform Patch and structured result evidence'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.state_to = 'review_ready' AND NOT NEW.evidence_to ? 'validation' THEN
      RAISE EXCEPTION 'review_ready requires platform validation evidence'
        USING ERRCODE = '23514';
    END IF;
    IF terminal_to THEN
      IF NEW.finished_at_to IS DISTINCT FROM NEW.created_at
         OR (NEW.state_to <> 'review_ready' AND (
           NEW.exit_reason_to IS NULL OR NEW.exit_reason_to <> btrim(NEW.exit_reason_to)
           OR length(NEW.exit_reason_to) NOT BETWEEN 1 AND 2000
         ))
         OR (NEW.state_to = 'review_ready' AND NEW.exit_reason_to IS NOT NULL) THEN
        RAISE EXCEPTION 'terminal AgentAttempt must capture exact finish and exit facts'
          USING ERRCODE = '55000';
      END IF;
    ELSE
      IF NEW.finished_at_to IS NOT NULL OR NEW.exit_reason_to IS NOT NULL THEN
        RAISE EXCEPTION 'non-terminal AgentAttempt cannot set finish or exit facts'
          USING ERRCODE = '55000';
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_attempt_event_guard
BEFORE INSERT ON agent_attempt_events
FOR EACH ROW EXECUTE FUNCTION validate_agent_attempt_event();

CREATE OR REPLACE FUNCTION advance_agent_attempt_from_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE agent_attempts
  SET state = NEW.state_to,
      version = NEW.version_to,
      fence_epoch = NEW.fence_epoch_to,
      lease_worker_id = NEW.lease_worker_id_to,
      lease_epoch = NEW.lease_epoch_to,
      lease_expires_at = NEW.lease_expires_at_to,
      evidence = NEW.evidence_to,
      exit_reason = NEW.exit_reason_to,
      started_at = NEW.started_at_to,
      finished_at = NEW.finished_at_to,
      updated_at = NEW.created_at
  WHERE id = NEW.attempt_id
    AND version = NEW.version_from
    AND state = NEW.state_from
    AND fence_epoch = NEW.fence_epoch_from;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'AgentAttempt event CAS update lost its worker fence'
      USING ERRCODE = '40001';
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER agent_attempt_event_advance
AFTER INSERT ON agent_attempt_events
FOR EACH ROW EXECUTE FUNCTION advance_agent_attempt_from_event();

CREATE OR REPLACE FUNCTION prevent_agent_attempt_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'AgentAttempt audit history cannot be deleted' USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.operation_id, NEW.project_id,
    NEW.sandbox_session_id, NEW.candidate_id,
    NEW.task_capsule_id, NEW.task_capsule_hash,
    NEW.context_pack_id, NEW.context_pack_hash,
    NEW.base_candidate_tree_hash, NEW.build_contract_hash,
    NEW.executor, NEW.request_key_hash, NEW.configuration_hash,
    NEW.parent_attempt_id, NEW.retry_reason, NEW.created_by,
    NEW.created_at, NEW.creation_transaction_id
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.operation_id, OLD.project_id,
    OLD.sandbox_session_id, OLD.candidate_id,
    OLD.task_capsule_id, OLD.task_capsule_hash,
    OLD.context_pack_id, OLD.context_pack_hash,
    OLD.base_candidate_tree_hash, OLD.build_contract_hash,
    OLD.executor, OLD.request_key_hash, OLD.configuration_hash,
    OLD.parent_attempt_id, OLD.retry_reason, OLD.created_by,
    OLD.created_at, OLD.creation_transaction_id
  ) OR NOT EXISTS (
    SELECT 1 FROM agent_attempt_events AS event
    WHERE event.attempt_id = OLD.id
      AND event.version_from = OLD.version
      AND event.version_to = NEW.version
      AND event.state_from = OLD.state
      AND event.state_to = NEW.state
      AND event.fence_epoch_from = OLD.fence_epoch
      AND event.fence_epoch_to = NEW.fence_epoch
      AND event.lease_worker_id_to IS NOT DISTINCT FROM NEW.lease_worker_id
      AND event.lease_epoch_to IS NOT DISTINCT FROM NEW.lease_epoch
      AND event.lease_expires_at_to IS NOT DISTINCT FROM NEW.lease_expires_at
      AND event.evidence_to = NEW.evidence
      AND event.exit_reason_to IS NOT DISTINCT FROM NEW.exit_reason
      AND event.started_at_to IS NOT DISTINCT FROM NEW.started_at
      AND event.finished_at_to IS NOT DISTINCT FROM NEW.finished_at
      AND event.created_at = NEW.updated_at
      AND event.creation_transaction_id = txid_current()
  ) THEN
    RAISE EXCEPTION 'AgentAttempt can change only through an append-only CAS event'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_attempt_mutation_guard
BEFORE UPDATE OR DELETE ON agent_attempts
FOR EACH ROW EXECUTE FUNCTION prevent_agent_attempt_mutation();

CREATE OR REPLACE FUNCTION prevent_agent_attempt_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'AgentAttempt events are append-only' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER agent_attempt_event_immutable
BEFORE UPDATE OR DELETE ON agent_attempt_events
FOR EACH ROW EXECUTE FUNCTION prevent_agent_attempt_event_mutation();

CREATE OR REPLACE FUNCTION validate_agent_attempt_event_chain()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_attempt_id uuid;
  parent agent_attempts%ROWTYPE;
  event_count bigint;
  minimum_sequence bigint;
  maximum_sequence bigint;
  tail agent_attempt_events%ROWTYPE;
BEGIN
  IF TG_TABLE_NAME = 'agent_attempts' THEN
    target_attempt_id := NEW.id;
  ELSE
    target_attempt_id := NEW.attempt_id;
  END IF;
  SELECT * INTO parent FROM agent_attempts WHERE id = target_attempt_id;
  IF NOT FOUND THEN RETURN NULL; END IF;
  SELECT count(*), min(sequence), max(sequence)
    INTO event_count, minimum_sequence, maximum_sequence
  FROM agent_attempt_events WHERE attempt_id = target_attempt_id;
  IF event_count <> parent.version - 1
     OR (event_count > 0 AND (minimum_sequence <> 1 OR maximum_sequence <> parent.version - 1)) THEN
    RAISE EXCEPTION 'AgentAttempt event chain is not contiguous' USING ERRCODE = '23514';
  END IF;
  IF event_count > 0 THEN
    SELECT * INTO tail FROM agent_attempt_events
    WHERE attempt_id = target_attempt_id ORDER BY sequence DESC LIMIT 1;
    IF tail.version_to <> parent.version OR tail.state_to <> parent.state
       OR tail.fence_epoch_to <> parent.fence_epoch OR tail.evidence_to <> parent.evidence
       OR tail.lease_worker_id_to IS DISTINCT FROM parent.lease_worker_id
       OR tail.lease_epoch_to IS DISTINCT FROM parent.lease_epoch
       OR tail.lease_expires_at_to IS DISTINCT FROM parent.lease_expires_at
       OR tail.exit_reason_to IS DISTINCT FROM parent.exit_reason
       OR tail.started_at_to IS DISTINCT FROM parent.started_at
       OR tail.finished_at_to IS DISTINCT FROM parent.finished_at
       OR tail.created_at <> parent.updated_at THEN
      RAISE EXCEPTION 'AgentAttempt event tail does not match its projection'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER agent_attempt_parent_event_chain_guard
AFTER INSERT OR UPDATE ON agent_attempts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_agent_attempt_event_chain();

CREATE CONSTRAINT TRIGGER agent_attempt_event_chain_guard
AFTER INSERT ON agent_attempt_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_agent_attempt_event_chain();
