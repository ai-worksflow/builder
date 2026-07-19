-- Immutable, replayable Agent patch merge plans. A plan binds one reviewed
-- PlatformPatch to the exact current Candidate and SandboxSession fences. The
-- optional application row can be inserted only after the complete ordered
-- journal batch exists; conflicts and no-op plans never mutate the Candidate.

CREATE OR REPLACE FUNCTION agent_patch_merge_operations_are_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(value) <> 'array' OR jsonb_array_length(value) > 2000 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(value) WITH ORDINALITY AS item(operation, ordinal)
      WHERE jsonb_typeof(item.operation) <> 'object'
         OR NOT (item.operation ?& ARRAY['id', 'kind', 'path'])
         OR item.operation->>'id' <> btrim(item.operation->>'id')
         OR length(item.operation->>'id') NOT BETWEEN 1 AND 160
         OR item.operation->>'kind' NOT IN ('file.upsert', 'file.delete')
         OR NOT repository_path_is_safe(item.operation->>'path')
         OR item.operation ? 'fromPath'
         OR EXISTS (
           SELECT 1 FROM jsonb_object_keys(item.operation) AS key(name)
           WHERE key.name NOT IN ('id', 'kind', 'path', 'expectedHash', 'contentHash', 'byteSize', 'mode')
         )
         OR (
           item.operation->>'kind' = 'file.upsert'
           AND (
             item.operation->>'contentHash' !~ '^sha256:[0-9a-f]{64}$'
             OR COALESCE(item.operation->>'byteSize', '0') !~ '^[0-9]+$'
             OR COALESCE((item.operation->>'byteSize')::bigint, 0) NOT BETWEEN 0 AND 4194304
             OR item.operation->>'mode' NOT IN ('100644', '100755')
             OR (item.operation ? 'expectedHash' AND item.operation->>'expectedHash' !~ '^sha256:[0-9a-f]{64}$')
           )
         )
         OR (
           item.operation->>'kind' = 'file.delete'
           AND (
             item.operation->>'expectedHash' !~ '^sha256:[0-9a-f]{64}$'
             OR item.operation ?| ARRAY['contentHash', 'byteSize', 'mode']
           )
         )
    )
    AND (
      SELECT count(*) = count(DISTINCT item.operation->>'id')
         AND count(*) = count(DISTINCT item.operation->>'path')
      FROM jsonb_array_elements(value) AS item(operation)
    )
    AND NOT EXISTS (
      SELECT 1
      FROM (
        SELECT item.operation->>'path' AS path,
               lag(item.operation->>'path') OVER (ORDER BY item.ordinal) AS previous_path
        FROM jsonb_array_elements(value) WITH ORDINALITY AS item(operation, ordinal)
      ) AS ordered
      WHERE ordered.previous_path IS NOT NULL AND ordered.path <= ordered.previous_path
    )
  END
$$;

CREATE OR REPLACE FUNCTION agent_patch_merge_conflicts_are_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(value) <> 'array' OR jsonb_array_length(value) > 2000 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(value) WITH ORDINALITY AS item(conflict, ordinal)
      WHERE jsonb_typeof(item.conflict) <> 'object'
         OR (SELECT count(*) FROM jsonb_object_keys(item.conflict)) <> 5
         OR NOT (item.conflict ?& ARRAY['path', 'reason', 'base', 'current', 'proposed'])
         OR NOT repository_path_is_safe(item.conflict->>'path')
         OR item.conflict->>'reason' <> 'concurrent_change'
         OR jsonb_typeof(item.conflict->'base') <> 'object'
         OR jsonb_typeof(item.conflict->'current') <> 'object'
         OR jsonb_typeof(item.conflict->'proposed') <> 'object'
         OR jsonb_typeof(item.conflict->'base'->'exists') <> 'boolean'
         OR jsonb_typeof(item.conflict->'current'->'exists') <> 'boolean'
         OR jsonb_typeof(item.conflict->'proposed'->'exists') <> 'boolean'
    )
    AND (
      SELECT count(*) = count(DISTINCT item.conflict->>'path')
      FROM jsonb_array_elements(value) AS item(conflict)
    )
    AND NOT EXISTS (
      SELECT 1
      FROM (
        SELECT item.conflict->>'path' AS path,
               lag(item.conflict->>'path') OVER (ORDER BY item.ordinal) AS previous_path
        FROM jsonb_array_elements(value) WITH ORDINALITY AS item(conflict, ordinal)
      ) AS ordered
      WHERE ordered.previous_path IS NOT NULL AND ordered.path <= ordered.previous_path
    )
  END
$$;

ALTER TABLE agent_attempts
  ADD CONSTRAINT agent_attempt_merge_lineage_unique
  UNIQUE (id, project_id, sandbox_session_id, candidate_id);

CREATE TABLE agent_patch_merge_plans (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'agent-patch-merge-plan/v1'),
  operation_id text NOT NULL CHECK (
    operation_id = btrim(operation_id) AND length(operation_id) BETWEEN 1 AND 128
    AND operation_id ~ '^[A-Za-z0-9._:~-]+$'
  ),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  sandbox_session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  attempt_id uuid NOT NULL,
  attempt_version bigint NOT NULL CHECK (attempt_version > 0),
  patch_reference jsonb NOT NULL CHECK (agent_blob_reference_is_valid(patch_reference)),
  patch_raw_hash text NOT NULL CHECK (patch_raw_hash ~ '^sha256:[0-9a-f]{64}$'),
  patch_content_hash text NOT NULL CHECK (patch_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  base_tree_hash text NOT NULL CHECK (base_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  current_tree_hash text NOT NULL CHECK (current_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  proposed_tree_hash text NOT NULL CHECK (proposed_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  planned_tree_hash text NOT NULL CHECK (planned_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  expected_session_version bigint NOT NULL CHECK (expected_session_version > 0),
  expected_session_epoch bigint NOT NULL CHECK (expected_session_epoch > 0),
  expected_candidate_version bigint NOT NULL CHECK (expected_candidate_version > 0),
  expected_candidate_journal_sequence bigint NOT NULL CHECK (expected_candidate_journal_sequence >= 0),
  expected_writer_lease_epoch bigint NOT NULL CHECK (expected_writer_lease_epoch > 0),
  disposition text NOT NULL CHECK (disposition IN ('planned', 'conflicted', 'noop')),
  operations jsonb NOT NULL CHECK (agent_patch_merge_operations_are_valid(operations)),
  conflicts jsonb NOT NULL CHECK (agent_patch_merge_conflicts_are_valid(conflicts)),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT agent_patch_merge_operation_unique UNIQUE (project_id, created_by, operation_id),
  CONSTRAINT agent_patch_merge_content_unique UNIQUE (id, content_hash),
  CONSTRAINT agent_patch_merge_attempt_fk
    FOREIGN KEY (attempt_id, project_id, sandbox_session_id, candidate_id)
    REFERENCES agent_attempts(id, project_id, sandbox_session_id, candidate_id) ON DELETE RESTRICT,
  CONSTRAINT agent_patch_merge_plan_shape CHECK (
    (disposition = 'planned'
      AND jsonb_array_length(operations) BETWEEN 1 AND 2000
      AND jsonb_array_length(conflicts) = 0
      AND current_tree_hash <> planned_tree_hash)
    OR
    (disposition = 'conflicted'
      AND jsonb_array_length(operations) = 0
      AND jsonb_array_length(conflicts) BETWEEN 1 AND 2000
      AND current_tree_hash = planned_tree_hash)
    OR
    (disposition = 'noop'
      AND jsonb_array_length(operations) = 0
      AND jsonb_array_length(conflicts) = 0
      AND current_tree_hash = planned_tree_hash)
  ),
  CONSTRAINT agent_patch_merge_patch_changes_tree CHECK (base_tree_hash <> proposed_tree_hash)
);

CREATE INDEX agent_patch_merge_attempt_idx
  ON agent_patch_merge_plans (attempt_id, created_at DESC, id DESC);
CREATE INDEX agent_patch_merge_candidate_idx
  ON agent_patch_merge_plans (candidate_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_agent_patch_merge_plan_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  attempt agent_attempts%ROWTYPE;
  candidate candidate_workspaces%ROWTYPE;
  session sandbox_sessions%ROWTYPE;
BEGIN
  SELECT * INTO attempt FROM agent_attempts WHERE id = NEW.attempt_id FOR SHARE;
  IF NOT FOUND
     OR attempt.project_id <> NEW.project_id
     OR attempt.sandbox_session_id <> NEW.sandbox_session_id
     OR attempt.candidate_id <> NEW.candidate_id
     OR attempt.version <> NEW.attempt_version
     OR attempt.state <> 'review_ready'
     OR attempt.base_candidate_tree_hash <> NEW.base_tree_hash
     OR attempt.evidence->'patch' IS DISTINCT FROM NEW.patch_reference
     OR NOT (attempt.evidence ? 'validation') THEN
    RAISE EXCEPTION 'Agent patch merge must bind the exact review-ready Attempt evidence'
      USING ERRCODE = '40001';
  END IF;

  SELECT * INTO candidate FROM candidate_workspaces
  WHERE id = NEW.candidate_id AND project_id = NEW.project_id FOR SHARE;
  IF NOT FOUND
     OR candidate.status <> 'active'
     OR candidate.version <> NEW.expected_candidate_version
     OR candidate.journal_sequence <> NEW.expected_candidate_journal_sequence
     OR candidate.session_epoch <> NEW.expected_session_epoch
     OR candidate.writer_lease_owner_id IS DISTINCT FROM NEW.created_by
     OR candidate.writer_lease_epoch <> NEW.expected_writer_lease_epoch
     OR candidate.writer_lease_expires_at IS NULL
     OR statement_timestamp() >= candidate.writer_lease_expires_at
     OR candidate.current_tree_hash <> NEW.current_tree_hash THEN
    RAISE EXCEPTION 'Agent patch merge Candidate fence changed'
      USING ERRCODE = '40001';
  END IF;

  SELECT * INTO session FROM sandbox_sessions
  WHERE id = NEW.sandbox_session_id AND project_id = NEW.project_id FOR SHARE;
  IF NOT FOUND
     OR session.candidate_id <> NEW.candidate_id
     OR session.state <> 'ready'
     OR session.version <> NEW.expected_session_version
     OR session.session_epoch <> NEW.expected_session_epoch
     OR session.candidate_version <> NEW.expected_candidate_version
     OR session.candidate_journal_sequence <> NEW.expected_candidate_journal_sequence
     OR session.candidate_writer_lease_epoch <> NEW.expected_writer_lease_epoch
     OR session.candidate_tree_hash <> NEW.current_tree_hash THEN
    RAISE EXCEPTION 'Agent patch merge SandboxSession fence changed'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_patch_merge_plan_insert_guard
BEFORE INSERT ON agent_patch_merge_plans
FOR EACH ROW EXECUTE FUNCTION validate_agent_patch_merge_plan_insert();

CREATE TABLE agent_patch_merge_applications (
  merge_id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'agent-patch-merge-application/v1'),
  plan_content_hash text NOT NULL CHECK (plan_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  journal_sequence_from bigint NOT NULL CHECK (journal_sequence_from > 0),
  journal_sequence_to bigint NOT NULL CHECK (journal_sequence_to >= journal_sequence_from),
  candidate_version_from bigint NOT NULL CHECK (candidate_version_from > 0),
  candidate_version_to bigint NOT NULL CHECK (candidate_version_to > candidate_version_from),
  before_tree_store text NOT NULL CHECK (length(btrim(before_tree_store)) > 0),
  before_tree_owner_id uuid NOT NULL,
  before_tree_ref text NOT NULL CHECK (length(btrim(before_tree_ref)) > 0),
  before_tree_content_hash text NOT NULL CHECK (before_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  before_tree_hash text NOT NULL CHECK (before_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  before_tree_file_count integer NOT NULL CHECK (before_tree_file_count BETWEEN 0 AND 20000),
  before_tree_byte_size bigint NOT NULL CHECK (before_tree_byte_size BETWEEN 0 AND 67108864),
  after_tree_store text NOT NULL CHECK (length(btrim(after_tree_store)) > 0),
  after_tree_owner_id uuid NOT NULL,
  after_tree_ref text NOT NULL CHECK (length(btrim(after_tree_ref)) > 0),
  after_tree_content_hash text NOT NULL CHECK (after_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  after_tree_hash text NOT NULL CHECK (after_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  after_tree_file_count integer NOT NULL CHECK (after_tree_file_count BETWEEN 0 AND 20000),
  after_tree_byte_size bigint NOT NULL CHECK (after_tree_byte_size BETWEEN 0 AND 67108864),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  applied_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  applied_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT agent_patch_merge_application_content_unique UNIQUE (merge_id, content_hash),
  CONSTRAINT agent_patch_merge_application_plan_fk
    FOREIGN KEY (merge_id, plan_content_hash)
    REFERENCES agent_patch_merge_plans(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT agent_patch_merge_application_span CHECK (
    journal_sequence_to - journal_sequence_from = candidate_version_to - candidate_version_from - 1
  ),
  CONSTRAINT agent_patch_merge_application_tree_change CHECK (before_tree_hash <> after_tree_hash)
);

CREATE OR REPLACE FUNCTION validate_agent_patch_merge_application_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  plan agent_patch_merge_plans%ROWTYPE;
  operation_count integer;
  item jsonb;
  journal candidate_workspace_journal%ROWTYPE;
  index integer;
BEGIN
  SELECT * INTO plan FROM agent_patch_merge_plans WHERE id = NEW.merge_id FOR SHARE;
  operation_count := jsonb_array_length(plan.operations);
  IF NOT FOUND
     OR plan.disposition <> 'planned'
     OR plan.content_hash <> NEW.plan_content_hash
     OR plan.project_id <> NEW.project_id
     OR plan.candidate_id <> NEW.candidate_id
     OR plan.created_by <> NEW.applied_by
     OR NEW.journal_sequence_from <> plan.expected_candidate_journal_sequence + 1
     OR NEW.journal_sequence_to <> plan.expected_candidate_journal_sequence + operation_count
     OR NEW.candidate_version_from <> plan.expected_candidate_version
     OR NEW.candidate_version_to <> plan.expected_candidate_version + operation_count
     OR NEW.before_tree_hash <> plan.current_tree_hash
     OR NEW.after_tree_hash <> plan.planned_tree_hash THEN
    RAISE EXCEPTION 'Agent patch merge application does not bind its immutable plan'
      USING ERRCODE = '23514';
  END IF;

  FOR index IN 0..operation_count - 1 LOOP
    item := plan.operations->index;
    SELECT * INTO journal
    FROM candidate_workspace_journal
    WHERE candidate_id = plan.candidate_id
      AND sequence = NEW.journal_sequence_from + index;
    IF NOT FOUND
       OR journal.candidate_version_from <> plan.expected_candidate_version + index
       OR journal.candidate_version_to <> plan.expected_candidate_version + index + 1
       OR journal.session_epoch <> plan.expected_session_epoch
       OR journal.writer_lease_epoch <> plan.expected_writer_lease_epoch
       OR journal.actor_id <> plan.created_by
       OR journal.attribution <> 'agent'
       OR journal.operation_id <> item->>'id'
       OR journal.operation_kind <> item->>'kind'
       OR journal.path <> item->>'path'
       OR journal.expected_content_hash IS DISTINCT FROM NULLIF(item->>'expectedHash', '')
       OR journal.content_hash IS DISTINCT FROM NULLIF(item->>'contentHash', '')
       OR journal.byte_size IS DISTINCT FROM (CASE
         WHEN item->>'kind' = 'file.upsert' THEN COALESCE((item->>'byteSize')::bigint, 0)
         ELSE NULL
       END)
       OR journal.file_mode IS DISTINCT FROM NULLIF(item->>'mode', '') THEN
      RAISE EXCEPTION 'Agent patch merge journal batch does not match its plan'
        USING ERRCODE = '23514';
    END IF;
    IF index = 0 AND (
      journal.before_tree_store <> NEW.before_tree_store
      OR journal.before_tree_owner_id <> NEW.before_tree_owner_id
      OR journal.before_tree_ref <> NEW.before_tree_ref
      OR journal.before_tree_content_hash <> NEW.before_tree_content_hash
      OR journal.before_tree_hash <> NEW.before_tree_hash
    ) THEN
      RAISE EXCEPTION 'Agent patch merge before tree pointer drifted' USING ERRCODE = '23514';
    END IF;
    IF index = operation_count - 1 AND (
      journal.after_tree_store <> NEW.after_tree_store
      OR journal.after_tree_owner_id <> NEW.after_tree_owner_id
      OR journal.after_tree_ref <> NEW.after_tree_ref
      OR journal.after_tree_content_hash <> NEW.after_tree_content_hash
      OR journal.after_tree_hash <> NEW.after_tree_hash
      OR journal.after_tree_file_count <> NEW.after_tree_file_count
      OR journal.after_tree_byte_size <> NEW.after_tree_byte_size
    ) THEN
      RAISE EXCEPTION 'Agent patch merge after tree pointer drifted' USING ERRCODE = '23514';
    END IF;
  END LOOP;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_patch_merge_application_insert_guard
BEFORE INSERT ON agent_patch_merge_applications
FOR EACH ROW EXECUTE FUNCTION validate_agent_patch_merge_application_insert();

CREATE OR REPLACE FUNCTION prevent_agent_patch_merge_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Agent patch merge plans and applications are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER agent_patch_merge_plan_immutable
BEFORE UPDATE OR DELETE ON agent_patch_merge_plans
FOR EACH ROW EXECUTE FUNCTION prevent_agent_patch_merge_mutation();

CREATE TRIGGER agent_patch_merge_application_immutable
BEFORE UPDATE OR DELETE ON agent_patch_merge_applications
FOR EACH ROW EXECUTE FUNCTION prevent_agent_patch_merge_mutation();
