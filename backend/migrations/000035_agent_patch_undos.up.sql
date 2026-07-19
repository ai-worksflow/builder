-- Explicit, immutable undo plans for applied Agent patch merges. Undo is a
-- second three-way operation: affected paths must still equal the merged
-- result (or already equal the pre-merge state), while unrelated paths remain.

CREATE TABLE agent_patch_undo_plans (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'agent-patch-undo-plan/v1'),
  operation_id text NOT NULL CHECK (
    operation_id = btrim(operation_id) AND length(operation_id) BETWEEN 1 AND 128
    AND operation_id ~ '^[A-Za-z0-9._:~-]+$'
  ),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  sandbox_session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  merge_id uuid NOT NULL,
  merge_plan_content_hash text NOT NULL CHECK (merge_plan_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  merge_application_content_hash text NOT NULL CHECK (merge_application_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  merge_before_tree_hash text NOT NULL CHECK (merge_before_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  merged_tree_hash text NOT NULL CHECK (merged_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  current_tree_hash text NOT NULL CHECK (current_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
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
  CONSTRAINT agent_patch_undo_operation_unique UNIQUE (project_id, created_by, operation_id),
  CONSTRAINT agent_patch_undo_content_unique UNIQUE (id, content_hash),
  CONSTRAINT agent_patch_undo_merge_plan_fk
    FOREIGN KEY (merge_id, merge_plan_content_hash)
    REFERENCES agent_patch_merge_plans(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT agent_patch_undo_merge_application_fk
    FOREIGN KEY (merge_id, merge_application_content_hash)
    REFERENCES agent_patch_merge_applications(merge_id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT agent_patch_undo_plan_shape CHECK (
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
  CONSTRAINT agent_patch_undo_merge_changes_tree CHECK (merge_before_tree_hash <> merged_tree_hash)
);

CREATE INDEX agent_patch_undo_merge_idx
  ON agent_patch_undo_plans (merge_id, created_at DESC, id DESC);
CREATE INDEX agent_patch_undo_candidate_idx
  ON agent_patch_undo_plans (candidate_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_agent_patch_undo_plan_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  merge_plan agent_patch_merge_plans%ROWTYPE;
  merge_application agent_patch_merge_applications%ROWTYPE;
  candidate candidate_workspaces%ROWTYPE;
  session sandbox_sessions%ROWTYPE;
BEGIN
  SELECT * INTO merge_plan FROM agent_patch_merge_plans WHERE id = NEW.merge_id FOR SHARE;
  SELECT * INTO merge_application FROM agent_patch_merge_applications WHERE merge_id = NEW.merge_id FOR SHARE;
  IF merge_plan.id IS NULL OR merge_application.merge_id IS NULL
     OR merge_plan.content_hash <> NEW.merge_plan_content_hash
     OR merge_application.content_hash <> NEW.merge_application_content_hash
     OR merge_plan.disposition <> 'planned'
     OR merge_plan.project_id <> NEW.project_id
     OR merge_plan.sandbox_session_id <> NEW.sandbox_session_id
     OR merge_plan.candidate_id <> NEW.candidate_id
     OR merge_plan.created_by <> NEW.created_by
     OR merge_application.before_tree_hash <> NEW.merge_before_tree_hash
     OR merge_application.after_tree_hash <> NEW.merged_tree_hash
     OR merge_plan.current_tree_hash <> NEW.merge_before_tree_hash
     OR merge_plan.planned_tree_hash <> NEW.merged_tree_hash THEN
    RAISE EXCEPTION 'Agent patch undo must bind one exact applied merge'
      USING ERRCODE = '23514';
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
    RAISE EXCEPTION 'Agent patch undo Candidate fence changed'
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
    RAISE EXCEPTION 'Agent patch undo SandboxSession fence changed'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_patch_undo_plan_insert_guard
BEFORE INSERT ON agent_patch_undo_plans
FOR EACH ROW EXECUTE FUNCTION validate_agent_patch_undo_plan_insert();

CREATE TABLE agent_patch_undo_applications (
  undo_id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'agent-patch-undo-application/v1'),
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
  CONSTRAINT agent_patch_undo_application_content_unique UNIQUE (undo_id, content_hash),
  CONSTRAINT agent_patch_undo_application_plan_fk
    FOREIGN KEY (undo_id, plan_content_hash)
    REFERENCES agent_patch_undo_plans(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT agent_patch_undo_application_span CHECK (
    journal_sequence_to - journal_sequence_from = candidate_version_to - candidate_version_from - 1
  ),
  CONSTRAINT agent_patch_undo_application_tree_change CHECK (before_tree_hash <> after_tree_hash)
);

CREATE OR REPLACE FUNCTION validate_agent_patch_undo_application_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  plan agent_patch_undo_plans%ROWTYPE;
  operation_count integer;
  item jsonb;
  journal candidate_workspace_journal%ROWTYPE;
  index integer;
BEGIN
  SELECT * INTO plan FROM agent_patch_undo_plans WHERE id = NEW.undo_id FOR SHARE;
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
    RAISE EXCEPTION 'Agent patch undo application does not bind its immutable plan'
      USING ERRCODE = '23514';
  END IF;

  FOR index IN 0..operation_count - 1 LOOP
    item := plan.operations->index;
    SELECT * INTO journal FROM candidate_workspace_journal
    WHERE candidate_id = plan.candidate_id AND sequence = NEW.journal_sequence_from + index;
    IF NOT FOUND
       OR journal.candidate_version_from <> plan.expected_candidate_version + index
       OR journal.candidate_version_to <> plan.expected_candidate_version + index + 1
       OR journal.session_epoch <> plan.expected_session_epoch
       OR journal.writer_lease_epoch <> plan.expected_writer_lease_epoch
       OR journal.actor_id <> plan.created_by
       OR journal.attribution <> 'restore'
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
      RAISE EXCEPTION 'Agent patch undo journal batch does not match its plan'
        USING ERRCODE = '23514';
    END IF;
    IF index = 0 AND (
      journal.before_tree_store <> NEW.before_tree_store
      OR journal.before_tree_owner_id <> NEW.before_tree_owner_id
      OR journal.before_tree_ref <> NEW.before_tree_ref
      OR journal.before_tree_content_hash <> NEW.before_tree_content_hash
      OR journal.before_tree_hash <> NEW.before_tree_hash
    ) THEN
      RAISE EXCEPTION 'Agent patch undo before tree pointer drifted' USING ERRCODE = '23514';
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
      RAISE EXCEPTION 'Agent patch undo after tree pointer drifted' USING ERRCODE = '23514';
    END IF;
  END LOOP;
  RETURN NEW;
END;
$$;

CREATE TRIGGER agent_patch_undo_application_insert_guard
BEFORE INSERT ON agent_patch_undo_applications
FOR EACH ROW EXECUTE FUNCTION validate_agent_patch_undo_application_insert();

CREATE OR REPLACE FUNCTION prevent_agent_patch_undo_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Agent patch undo plans and applications are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER agent_patch_undo_plan_immutable
BEFORE UPDATE OR DELETE ON agent_patch_undo_plans
FOR EACH ROW EXECUTE FUNCTION prevent_agent_patch_undo_mutation();

CREATE TRIGGER agent_patch_undo_application_immutable
BEFORE UPDATE OR DELETE ON agent_patch_undo_applications
FOR EACH ROW EXECUTE FUNCTION prevent_agent_patch_undo_mutation();
