-- Explicit Candidate rebases never rewrite a mutable workspace in place. A
-- successor Candidate starts from the exact target RepositorySnapshot; the
-- immutable plan records the ancestor/current/target facts used by the
-- deterministic three-way merge, and conflicts remain explicit until a user
-- resolves every path.

CREATE TABLE candidate_rebases (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'candidate-rebase/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  operation_id text NOT NULL CHECK (
    operation_id = btrim(operation_id) AND length(operation_id) BETWEEN 1 AND 128
  ),
  predecessor_candidate_id uuid NOT NULL UNIQUE REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  successor_candidate_id uuid NOT NULL UNIQUE REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  target_build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  ancestor_tree_hash text NOT NULL CHECK (ancestor_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  predecessor_tree_hash text NOT NULL CHECK (predecessor_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  target_tree_hash text NOT NULL CHECK (target_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  planned_tree_hash text NOT NULL CHECK (planned_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  state text NOT NULL CHECK (state IN ('applying', 'conflicted', 'ready')),
  version bigint NOT NULL CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT candidate_rebase_distinct_candidates CHECK (
    predecessor_candidate_id <> successor_candidate_id
  ),
  CONSTRAINT candidate_rebase_operation_unique UNIQUE (project_id, created_by, operation_id),
  CONSTRAINT candidate_rebase_time_order CHECK (updated_at >= created_at)
);

CREATE INDEX candidate_rebases_predecessor_idx
  ON candidate_rebases (predecessor_candidate_id, created_at DESC, id DESC);
CREATE INDEX candidate_rebases_project_state_idx
  ON candidate_rebases (project_id, state, updated_at DESC, id DESC);

CREATE TABLE candidate_rebase_operations (
  rebase_id uuid NOT NULL REFERENCES candidate_rebases(id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal >= 0),
  operation_id text NOT NULL CHECK (
    operation_id = btrim(operation_id) AND length(operation_id) BETWEEN 1 AND 160
  ),
  operation jsonb NOT NULL CHECK (
    jsonb_typeof(operation) = 'object'
    AND operation->>'id' = operation_id
    AND operation->>'kind' IN ('file.upsert', 'file.delete')
    AND repository_path_is_safe(operation->>'path')
  ),
  PRIMARY KEY (rebase_id, ordinal),
  UNIQUE (rebase_id, operation_id)
);

CREATE TABLE candidate_rebase_conflicts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'candidate-rebase-conflict/v1'),
  rebase_id uuid NOT NULL REFERENCES candidate_rebases(id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal >= 0),
  path text NOT NULL CHECK (repository_path_is_safe(path)),
  ancestor_file jsonb,
  predecessor_file jsonb,
  target_file jsonb,
  state text NOT NULL CHECK (state IN ('open', 'resolved')),
  version bigint NOT NULL CHECK (version > 0),
  resolution_strategy text CHECK (
    resolution_strategy IS NULL OR resolution_strategy IN ('predecessor', 'target', 'current')
  ),
  resolution_content_hash text CHECK (
    resolution_content_hash IS NULL OR resolution_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  resolution_deleted boolean,
  resolution_file jsonb,
  resolved_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT candidate_rebase_conflict_order UNIQUE (rebase_id, ordinal),
  CONSTRAINT candidate_rebase_conflict_path UNIQUE (rebase_id, path),
  CONSTRAINT candidate_rebase_conflict_has_both_changes CHECK (
    predecessor_file IS DISTINCT FROM ancestor_file
    AND target_file IS DISTINCT FROM ancestor_file
    AND predecessor_file IS DISTINCT FROM target_file
  ),
  CONSTRAINT candidate_rebase_conflict_file_shapes CHECK (
    (ancestor_file IS NULL OR (
      jsonb_typeof(ancestor_file) = 'object'
      AND ancestor_file->>'path' = path
      AND ancestor_file->>'mode' IN ('100644', '100755')
      AND ancestor_file->>'contentHash' ~ '^sha256:[0-9a-f]{64}$'
      AND (ancestor_file->>'byteSize')::bigint BETWEEN 0 AND 4194304
    ))
    AND (predecessor_file IS NULL OR (
      jsonb_typeof(predecessor_file) = 'object'
      AND predecessor_file->>'path' = path
      AND predecessor_file->>'mode' IN ('100644', '100755')
      AND predecessor_file->>'contentHash' ~ '^sha256:[0-9a-f]{64}$'
      AND (predecessor_file->>'byteSize')::bigint BETWEEN 0 AND 4194304
    ))
    AND (target_file IS NULL OR (
      jsonb_typeof(target_file) = 'object'
      AND target_file->>'path' = path
      AND target_file->>'mode' IN ('100644', '100755')
      AND target_file->>'contentHash' ~ '^sha256:[0-9a-f]{64}$'
      AND (target_file->>'byteSize')::bigint BETWEEN 0 AND 4194304
    ))
  ),
  CONSTRAINT candidate_rebase_conflict_resolution_shape CHECK (
    (state = 'open'
      AND version = 1
      AND resolution_strategy IS NULL
      AND resolution_content_hash IS NULL
      AND resolution_deleted IS NULL
      AND resolution_file IS NULL
      AND resolved_by IS NULL
      AND resolved_at IS NULL)
    OR
    (state = 'resolved'
      AND version = 2
      AND resolution_strategy IS NOT NULL
      AND resolution_deleted IS NOT NULL
      AND ((resolution_deleted AND resolution_content_hash IS NULL AND resolution_file IS NULL)
        OR (NOT resolution_deleted
          AND resolution_content_hash IS NOT NULL
          AND jsonb_typeof(resolution_file) = 'object'
          AND resolution_file->>'path' = path
          AND resolution_file->>'mode' IN ('100644', '100755')
          AND resolution_file->>'contentHash' = resolution_content_hash
          AND (resolution_file->>'byteSize')::bigint BETWEEN 0 AND 4194304))
      AND (resolution_strategy = 'current'
        OR (resolution_strategy = 'predecessor'
          AND resolution_file IS NOT DISTINCT FROM predecessor_file)
        OR (resolution_strategy = 'target'
          AND resolution_file IS NOT DISTINCT FROM target_file))
      AND resolved_by IS NOT NULL
      AND resolved_at IS NOT NULL)
  )
);

CREATE INDEX candidate_rebase_conflicts_open_idx
  ON candidate_rebase_conflicts (rebase_id, ordinal)
  WHERE state = 'open';

CREATE OR REPLACE FUNCTION validate_candidate_rebase_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  predecessor candidate_workspaces%ROWTYPE;
  successor candidate_workspaces%ROWTYPE;
  predecessor_found boolean := false;
  successor_found boolean := false;
BEGIN
  IF NEW.state <> 'applying' OR NEW.version <> 1 OR NEW.updated_at <> NEW.created_at THEN
    RAISE EXCEPTION 'CandidateRebase must start applying at version 1'
      USING ERRCODE = '23514';
  END IF;

  SELECT * INTO predecessor
  FROM candidate_workspaces
  WHERE id = NEW.predecessor_candidate_id
  FOR UPDATE;
  predecessor_found := FOUND;
  SELECT * INTO successor
  FROM candidate_workspaces
  WHERE id = NEW.successor_candidate_id
  FOR UPDATE;
  successor_found := FOUND;

  IF NOT predecessor_found
     OR NOT successor_found
     OR predecessor.project_id <> NEW.project_id
     OR successor.project_id <> NEW.project_id
     OR successor.build_manifest_id <> NEW.target_build_manifest_id
     OR predecessor.base_tree_hash <> NEW.ancestor_tree_hash
     OR predecessor.current_tree_hash <> NEW.predecessor_tree_hash
     OR successor.base_tree_hash <> NEW.target_tree_hash
     OR successor.current_tree_hash <> successor.base_tree_hash
     OR successor.journal_sequence <> 0
     OR predecessor.conflicted
     OR predecessor.stale
     OR predecessor.rebase_required
     OR successor.status <> 'active' THEN
    RAISE EXCEPTION 'CandidateRebase does not bind exact predecessor and clean successor trees'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_rebase_insert_guard
BEFORE INSERT ON candidate_rebases
FOR EACH ROW EXECUTE FUNCTION validate_candidate_rebase_insert();

CREATE OR REPLACE FUNCTION validate_candidate_rebase_plan_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent_state text;
BEGIN
  SELECT state INTO parent_state
  FROM candidate_rebases
  WHERE id = NEW.rebase_id
  FOR KEY SHARE;
  IF NOT FOUND OR parent_state <> 'applying' THEN
    RAISE EXCEPTION 'CandidateRebase plan can only be populated while applying'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_rebase_operation_insert_guard
BEFORE INSERT ON candidate_rebase_operations
FOR EACH ROW EXECUTE FUNCTION validate_candidate_rebase_plan_insert();

CREATE TRIGGER candidate_rebase_conflict_insert_guard
BEFORE INSERT ON candidate_rebase_conflicts
FOR EACH ROW EXECUTE FUNCTION validate_candidate_rebase_plan_insert();

CREATE OR REPLACE FUNCTION validate_candidate_rebase_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  planned_count integer;
  applied_count integer;
  open_conflicts integer;
  successor_conflicted boolean;
  successor_tree_hash text;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'CandidateRebase lineage is immutable'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.operation_id,
    NEW.predecessor_candidate_id, NEW.successor_candidate_id,
    NEW.target_build_manifest_id, NEW.ancestor_tree_hash,
    NEW.predecessor_tree_hash, NEW.target_tree_hash, NEW.planned_tree_hash,
    NEW.plan_hash, NEW.created_by, NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.operation_id,
    OLD.predecessor_candidate_id, OLD.successor_candidate_id,
    OLD.target_build_manifest_id, OLD.ancestor_tree_hash,
    OLD.predecessor_tree_hash, OLD.target_tree_hash, OLD.planned_tree_hash,
    OLD.plan_hash, OLD.created_by, OLD.created_at
  ) OR NEW.version <> OLD.version + 1 OR NEW.updated_at <= OLD.updated_at THEN
    RAISE EXCEPTION 'CandidateRebase exact lineage is immutable and transitions require CAS'
      USING ERRCODE = '40001';
  END IF;
  IF NOT ((OLD.state = 'applying' AND NEW.state IN ('conflicted', 'ready'))
      OR (OLD.state = 'conflicted' AND NEW.state = 'ready')) THEN
    RAISE EXCEPTION 'invalid CandidateRebase transition'
      USING ERRCODE = '55000';
  END IF;

  SELECT count(*) INTO planned_count
  FROM candidate_rebase_operations WHERE rebase_id = NEW.id;
  SELECT count(*) INTO applied_count
  FROM candidate_rebase_operations AS plan
  JOIN candidate_workspace_journal AS journal
    ON journal.candidate_id = NEW.successor_candidate_id
   AND journal.operation_id = plan.operation_id
   AND journal.operation_kind = plan.operation->>'kind'
   AND journal.path = plan.operation->>'path'
   AND journal.expected_content_hash IS NOT DISTINCT FROM NULLIF(plan.operation->>'expectedHash', '')
   AND journal.content_hash IS NOT DISTINCT FROM NULLIF(plan.operation->>'contentHash', '')
   AND journal.byte_size IS NOT DISTINCT FROM CASE
     WHEN plan.operation->>'kind' = 'file.upsert'
       THEN COALESCE((NULLIF(plan.operation->>'byteSize', ''))::bigint, 0)
     ELSE NULL
   END
   AND journal.file_mode IS NOT DISTINCT FROM NULLIF(plan.operation->>'mode', '')
  WHERE plan.rebase_id = NEW.id;
  SELECT count(*) INTO open_conflicts
  FROM candidate_rebase_conflicts
  WHERE rebase_id = NEW.id AND state = 'open';
  SELECT conflicted, current_tree_hash INTO successor_conflicted, successor_tree_hash
  FROM candidate_workspaces WHERE id = NEW.successor_candidate_id;

  IF applied_count <> planned_count THEN
    RAISE EXCEPTION 'CandidateRebase cannot advance before every planned operation is journaled'
      USING ERRCODE = '40001';
  END IF;
  IF OLD.state = 'applying' AND successor_tree_hash <> NEW.planned_tree_hash THEN
    RAISE EXCEPTION 'CandidateRebase automatic result differs from its planned tree'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.state = 'conflicted' AND (open_conflicts = 0 OR NOT successor_conflicted) THEN
    RAISE EXCEPTION 'conflicted CandidateRebase requires open conflicts and a conflicted successor'
      USING ERRCODE = '23514';
  END IF;
  IF NEW.state = 'ready' AND (open_conflicts <> 0 OR successor_conflicted) THEN
    RAISE EXCEPTION 'ready CandidateRebase requires all conflicts resolved and successor unblocked'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_rebase_mutation_guard
BEFORE UPDATE OR DELETE ON candidate_rebases
FOR EACH ROW EXECUTE FUNCTION validate_candidate_rebase_mutation();

CREATE OR REPLACE FUNCTION prevent_candidate_rebase_plan_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'CandidateRebase operations are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_rebase_operation_immutable
BEFORE UPDATE OR DELETE ON candidate_rebase_operations
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_rebase_plan_mutation();

CREATE OR REPLACE FUNCTION validate_candidate_rebase_conflict_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'CandidateRebase conflicts are immutable audit records'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.rebase_id, NEW.ordinal, NEW.path,
    NEW.ancestor_file, NEW.predecessor_file, NEW.target_file, NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.rebase_id, OLD.ordinal, OLD.path,
    OLD.ancestor_file, OLD.predecessor_file, OLD.target_file, OLD.created_at
  ) OR OLD.state <> 'open' OR NEW.state <> 'resolved' OR NEW.version <> 2 THEN
    RAISE EXCEPTION 'CandidateRebase conflict permits one exact open-to-resolved transition'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_rebase_conflict_guard
BEFORE UPDATE OR DELETE ON candidate_rebase_conflicts
FOR EACH ROW EXECUTE FUNCTION validate_candidate_rebase_conflict_mutation();
