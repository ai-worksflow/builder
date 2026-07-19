CREATE OR REPLACE FUNCTION repository_path_is_safe(candidate_path text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT candidate_path IS NOT NULL
     AND candidate_path = btrim(candidate_path)
     AND length(candidate_path) BETWEEN 1 AND 512
     AND candidate_path !~ '[[:cntrl:]]'
     AND candidate_path !~ '[/\\]$'
     AND candidate_path !~ '^[/\\]'
     AND candidate_path !~ '\\'
     AND candidate_path !~ '(^|/)\.{1,2}(/|$)'
     AND candidate_path !~ '//'
     AND NOT EXISTS (
       SELECT 1
       FROM regexp_split_to_table(lower(candidate_path), '/') AS segment(value)
       WHERE segment.value IN ('.git', '.env', 'node_modules', '.next', 'dist', 'build', '__pycache__')
          OR segment.value LIKE '.env.%'
     )
$$;

-- Template path policies are relative to a component mount. Resolve them only
-- through the exact FullStackTemplate/component/TemplateRelease identities
-- pinned by the Candidate; never through a template name or mutable policy.
CREATE OR REPLACE FUNCTION repository_path_matches_template_policy(
  target_full_stack_template_id uuid,
  target_full_stack_template_hash text,
  candidate_path text,
  policy_field text
)
RETURNS boolean
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $$
  SELECT EXISTS (
    SELECT 1
    FROM full_stack_template_components AS component
    JOIN template_releases AS release
      ON release.id = component.template_release_id
     AND release.content_hash = component.template_release_content_hash
    CROSS JOIN LATERAL jsonb_array_elements_text(
      CASE
        WHEN jsonb_typeof(release.manifest -> policy_field) = 'array'
          THEN release.manifest -> policy_field
        ELSE '[]'::jsonb
      END
    ) AS policy(path)
    CROSS JOIN LATERAL (
      SELECT lower(component.mount_path || '/' || policy.path) AS effective_path
    ) AS resolved
    WHERE component.full_stack_template_id = target_full_stack_template_id
      AND component.full_stack_content_hash = target_full_stack_template_hash
      AND policy_field IN ('protectedPaths', 'extensionPaths')
      AND (
        lower(candidate_path) = resolved.effective_path
        OR left(lower(candidate_path), length(resolved.effective_path) + 1)
             = resolved.effective_path || '/'
      )
  )
$$;

CREATE TABLE repository_snapshots (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'repository-snapshot/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  full_stack_template_id uuid NOT NULL,
  full_stack_template_hash text NOT NULL CHECK (full_stack_template_hash ~ '^sha256:[0-9a-f]{64}$'),
  base_workspace_artifact_id uuid REFERENCES artifacts(id) ON DELETE RESTRICT,
  base_workspace_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  base_workspace_content_hash text CHECK (
    base_workspace_content_hash IS NULL OR base_workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  tree_store text NOT NULL CHECK (tree_store = btrim(tree_store) AND length(tree_store) > 0),
  tree_owner_id uuid NOT NULL,
  tree_ref text NOT NULL CHECK (tree_ref = btrim(tree_ref) AND length(tree_ref) > 0),
  tree_content_hash text NOT NULL CHECK (tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  tree_file_count integer NOT NULL CHECK (tree_file_count BETWEEN 0 AND 20000),
  tree_byte_size bigint NOT NULL CHECK (tree_byte_size BETWEEN 0 AND 67108864),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT repository_snapshot_project_identity_unique UNIQUE (id, project_id),
  CONSTRAINT repository_snapshot_exact_identity_unique UNIQUE (
    id, project_id, build_manifest_id, build_contract_id, build_contract_hash,
    full_stack_template_id, full_stack_template_hash
  ),
  CONSTRAINT repository_snapshot_full_stack_fk
    FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
    REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT repository_snapshot_tree_owner CHECK (tree_owner_id = id),
  CONSTRAINT repository_snapshot_base_workspace_shape CHECK (
    (base_workspace_artifact_id IS NULL
      AND base_workspace_revision_id IS NULL
      AND base_workspace_content_hash IS NULL)
    OR
    (base_workspace_artifact_id IS NOT NULL
      AND base_workspace_revision_id IS NOT NULL
      AND base_workspace_content_hash IS NOT NULL)
  )
);

CREATE INDEX repository_snapshots_project_created_idx
  ON repository_snapshots (project_id, created_at DESC, id DESC);

CREATE INDEX repository_snapshots_manifest_idx
  ON repository_snapshots (build_manifest_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_repository_snapshot_reference()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM application_build_manifests AS manifest
    JOIN application_build_contracts AS contract
      ON contract.id = NEW.build_contract_id
     AND contract.project_id = manifest.project_id
     AND contract.build_manifest_id = manifest.id
     AND contract.build_manifest_hash = manifest.manifest_hash
    WHERE manifest.id = NEW.build_manifest_id
      AND manifest.project_id = NEW.project_id
      AND manifest.manifest_hash = NEW.build_manifest_hash
      AND manifest.status = 'frozen'
      AND contract.contract_hash = NEW.build_contract_hash
      AND contract.status = 'ready'
      AND contract.full_stack_template_id = NEW.full_stack_template_id
      AND contract.full_stack_template_hash = NEW.full_stack_template_hash
      AND (
        (NEW.base_workspace_revision_id IS NULL AND manifest.workspace_revision_id IS NULL)
        OR manifest.workspace_revision_id = NEW.base_workspace_revision_id
      )
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot requires an exact ready same-project BuildContract and frozen BuildManifest'
      USING ERRCODE = '23503';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM full_stack_template_components AS component
    LEFT JOIN template_release_policies AS policy
      ON policy.template_release_id = component.template_release_id
     AND policy.release_content_hash = component.template_release_content_hash
    WHERE component.full_stack_template_id = NEW.full_stack_template_id
      AND component.full_stack_content_hash = NEW.full_stack_template_hash
      AND (policy.template_release_id IS NULL OR policy.state <> 'approved')
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot FullStackTemplate contains a release that is not selectable'
      USING ERRCODE = '23503';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM full_stack_template_components AS component
    JOIN template_releases AS release
      ON release.id = component.template_release_id
     AND release.content_hash = component.template_release_content_hash
    WHERE component.full_stack_template_id = NEW.full_stack_template_id
      AND component.full_stack_content_hash = NEW.full_stack_template_hash
      AND (
        CASE
          WHEN jsonb_typeof(release.manifest->'protectedPaths') = 'array'
            THEN jsonb_array_length(release.manifest->'protectedPaths') = 0
          ELSE true
        END
        OR CASE
          WHEN jsonb_typeof(release.manifest->'extensionPaths') = 'array'
            THEN jsonb_array_length(release.manifest->'extensionPaths') = 0
          ELSE true
        END
      )
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot exact TemplateReleases require non-empty protectedPaths and extensionPaths'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.base_workspace_revision_id IS NOT NULL AND NOT EXISTS (
    SELECT 1
    FROM artifacts AS artifact
    JOIN artifact_revisions AS revision ON revision.artifact_id = artifact.id
    WHERE artifact.id = NEW.base_workspace_artifact_id
      AND artifact.project_id = NEW.project_id
      AND artifact.kind = 'workspace'
      AND revision.id = NEW.base_workspace_revision_id
      AND revision.content_hash = NEW.base_workspace_content_hash
      AND revision.workflow_status IN ('approved', 'superseded')
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot base is not an exact canonical same-project WorkspaceRevision'
      USING ERRCODE = '23503';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER repository_snapshot_reference_guard
BEFORE INSERT ON repository_snapshots
FOR EACH ROW EXECUTE FUNCTION validate_repository_snapshot_reference();

CREATE OR REPLACE FUNCTION prevent_repository_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'RepositorySnapshot is immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER repository_snapshot_immutable
BEFORE UPDATE OR DELETE ON repository_snapshots
FOR EACH ROW EXECUTE FUNCTION prevent_repository_snapshot_mutation();

CREATE TABLE candidate_workspaces (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'candidate-workspace/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  repository_snapshot_id uuid NOT NULL,
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  full_stack_template_id uuid NOT NULL,
  full_stack_template_hash text NOT NULL CHECK (full_stack_template_hash ~ '^sha256:[0-9a-f]{64}$'),
  base_workspace_artifact_id uuid REFERENCES artifacts(id) ON DELETE RESTRICT,
  base_workspace_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  base_workspace_content_hash text CHECK (
    base_workspace_content_hash IS NULL OR base_workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  base_tree_store text NOT NULL CHECK (base_tree_store = btrim(base_tree_store) AND length(base_tree_store) > 0),
  base_tree_owner_id uuid NOT NULL,
  base_tree_ref text NOT NULL CHECK (base_tree_ref = btrim(base_tree_ref) AND length(base_tree_ref) > 0),
  base_tree_content_hash text NOT NULL CHECK (base_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  base_tree_hash text NOT NULL CHECK (base_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  current_tree_store text NOT NULL CHECK (current_tree_store = btrim(current_tree_store) AND length(current_tree_store) > 0),
  current_tree_owner_id uuid NOT NULL,
  current_tree_ref text NOT NULL CHECK (current_tree_ref = btrim(current_tree_ref) AND length(current_tree_ref) > 0),
  current_tree_content_hash text NOT NULL CHECK (current_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  current_tree_hash text NOT NULL CHECK (current_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  current_tree_file_count integer NOT NULL CHECK (current_tree_file_count BETWEEN 0 AND 20000),
  current_tree_byte_size bigint NOT NULL CHECK (current_tree_byte_size BETWEEN 0 AND 67108864),
  status text NOT NULL CHECK (status IN ('active', 'frozen', 'abandoned')),
  dirty boolean NOT NULL DEFAULT false,
  conflicted boolean NOT NULL DEFAULT false,
  stale boolean NOT NULL DEFAULT false,
  rebase_required boolean NOT NULL DEFAULT false,
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  version bigint NOT NULL CHECK (version > 0),
  journal_sequence bigint NOT NULL CHECK (journal_sequence >= 0),
  writer_lease_owner_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  writer_lease_epoch bigint NOT NULL CHECK (writer_lease_epoch >= 0),
  writer_lease_expires_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT candidate_workspace_snapshot_fk
    FOREIGN KEY (repository_snapshot_id, project_id)
    REFERENCES repository_snapshots(id, project_id) ON DELETE RESTRICT,
  CONSTRAINT candidate_workspace_full_stack_fk
    FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
    REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT candidate_workspace_base_shape CHECK (
    (base_workspace_artifact_id IS NULL
      AND base_workspace_revision_id IS NULL
      AND base_workspace_content_hash IS NULL)
    OR
    (base_workspace_artifact_id IS NOT NULL
      AND base_workspace_revision_id IS NOT NULL
      AND base_workspace_content_hash IS NOT NULL)
  ),
  CONSTRAINT candidate_workspace_lease_shape CHECK (
    (writer_lease_owner_id IS NULL AND writer_lease_expires_at IS NULL)
    OR
    (writer_lease_owner_id IS NOT NULL
      AND writer_lease_expires_at IS NOT NULL
      AND writer_lease_epoch > 0
      AND status = 'active')
  ),
  CONSTRAINT candidate_workspace_timestamp_order CHECK (updated_at >= created_at)
);

CREATE INDEX candidate_workspaces_project_status_idx
  ON candidate_workspaces (project_id, status, updated_at DESC, id DESC);

CREATE INDEX candidate_workspaces_lease_idx
  ON candidate_workspaces (writer_lease_expires_at, writer_lease_epoch)
  WHERE status = 'active' AND writer_lease_owner_id IS NOT NULL;

CREATE OR REPLACE FUNCTION validate_candidate_workspace_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.status <> 'active'
     OR NEW.version <> 1
     OR NEW.session_epoch <> 1
     OR NEW.journal_sequence <> 0
     OR NEW.writer_lease_epoch <> 0
     OR NEW.writer_lease_owner_id IS NOT NULL
     OR NEW.writer_lease_expires_at IS NOT NULL
     OR NEW.dirty OR NEW.conflicted OR NEW.stale OR NEW.rebase_required
     OR NEW.updated_at <> NEW.created_at THEN
    RAISE EXCEPTION 'CandidateWorkspace must start as a clean, unleased version 1 workspace'
      USING ERRCODE = '23514';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM repository_snapshots AS snapshot
    WHERE snapshot.id = NEW.repository_snapshot_id
      AND snapshot.project_id = NEW.project_id
      AND snapshot.build_manifest_id = NEW.build_manifest_id
      AND snapshot.build_manifest_hash = NEW.build_manifest_hash
      AND snapshot.build_contract_id = NEW.build_contract_id
      AND snapshot.build_contract_hash = NEW.build_contract_hash
      AND snapshot.full_stack_template_id = NEW.full_stack_template_id
      AND snapshot.full_stack_template_hash = NEW.full_stack_template_hash
      AND snapshot.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id
      AND snapshot.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id
      AND snapshot.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash
      AND snapshot.tree_store = NEW.base_tree_store
      AND snapshot.tree_owner_id = NEW.base_tree_owner_id
      AND snapshot.tree_ref = NEW.base_tree_ref
      AND snapshot.tree_content_hash = NEW.base_tree_content_hash
      AND snapshot.tree_hash = NEW.base_tree_hash
      AND snapshot.tree_store = NEW.current_tree_store
      AND snapshot.tree_owner_id = NEW.current_tree_owner_id
      AND snapshot.tree_ref = NEW.current_tree_ref
      AND snapshot.tree_content_hash = NEW.current_tree_content_hash
      AND snapshot.tree_hash = NEW.current_tree_hash
      AND snapshot.tree_file_count = NEW.current_tree_file_count
      AND snapshot.tree_byte_size = NEW.current_tree_byte_size
      AND snapshot.created_at <= NEW.created_at
  ) THEN
    RAISE EXCEPTION 'CandidateWorkspace does not exactly project its immutable RepositorySnapshot'
      USING ERRCODE = '23503';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_workspace_insert_guard
BEFORE INSERT ON candidate_workspaces
FOR EACH ROW EXECUTE FUNCTION validate_candidate_workspace_insert();

CREATE TABLE candidate_workspace_journal (
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  sequence bigint NOT NULL CHECK (sequence > 0),
  candidate_version_from bigint NOT NULL CHECK (candidate_version_from > 0),
  candidate_version_to bigint NOT NULL CHECK (candidate_version_to = candidate_version_from + 1),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  writer_lease_epoch bigint NOT NULL CHECK (writer_lease_epoch > 0),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  attribution text NOT NULL CHECK (attribution IN ('user', 'agent', 'merge', 'restore')),
  operation_id text NOT NULL CHECK (
    operation_id = btrim(operation_id) AND length(operation_id) BETWEEN 1 AND 160
  ),
  operation_kind text NOT NULL CHECK (operation_kind IN ('file.upsert', 'file.delete', 'file.rename')),
  path text NOT NULL CHECK (repository_path_is_safe(path)),
  from_path text CHECK (from_path IS NULL OR repository_path_is_safe(from_path)),
  expected_content_hash text CHECK (
    expected_content_hash IS NULL OR expected_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  content_hash text CHECK (content_hash IS NULL OR content_hash ~ '^sha256:[0-9a-f]{64}$'),
  byte_size bigint,
  file_mode text,
  before_tree_store text NOT NULL CHECK (before_tree_store = btrim(before_tree_store) AND length(before_tree_store) > 0),
  before_tree_owner_id uuid NOT NULL,
  before_tree_ref text NOT NULL CHECK (before_tree_ref = btrim(before_tree_ref) AND length(before_tree_ref) > 0),
  before_tree_content_hash text NOT NULL CHECK (before_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  before_tree_hash text NOT NULL CHECK (before_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  after_tree_store text NOT NULL CHECK (after_tree_store = btrim(after_tree_store) AND length(after_tree_store) > 0),
  after_tree_owner_id uuid NOT NULL,
  after_tree_ref text NOT NULL CHECK (after_tree_ref = btrim(after_tree_ref) AND length(after_tree_ref) > 0),
  after_tree_content_hash text NOT NULL CHECK (after_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  after_tree_hash text NOT NULL CHECK (after_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  after_tree_file_count integer NOT NULL CHECK (after_tree_file_count BETWEEN 0 AND 20000),
  after_tree_byte_size bigint NOT NULL CHECK (after_tree_byte_size BETWEEN 0 AND 67108864),
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  PRIMARY KEY (candidate_id, sequence),
  CONSTRAINT candidate_workspace_journal_operation_unique UNIQUE (candidate_id, operation_id),
  CONSTRAINT candidate_workspace_journal_after_owner CHECK (after_tree_owner_id = candidate_id),
  CONSTRAINT candidate_workspace_journal_tree_changes CHECK (before_tree_hash <> after_tree_hash),
  CONSTRAINT candidate_workspace_journal_operation_shape CHECK (
    (operation_kind = 'file.upsert'
      AND from_path IS NULL
      AND content_hash IS NOT NULL
      AND byte_size BETWEEN 0 AND 4194304
      AND file_mode IN ('100644', '100755'))
    OR
    (operation_kind = 'file.delete'
      AND from_path IS NULL
      AND expected_content_hash IS NOT NULL
      AND content_hash IS NULL
      AND byte_size IS NULL
      AND file_mode IS NULL)
    OR
    (operation_kind = 'file.rename'
      AND from_path IS NOT NULL
      AND from_path <> path
      AND expected_content_hash IS NOT NULL
      AND content_hash IS NULL
      AND byte_size IS NULL
      AND file_mode IS NULL)
  )
);

CREATE INDEX candidate_workspace_journal_actor_idx
  ON candidate_workspace_journal (actor_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_candidate_workspace_journal_append()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  candidate candidate_workspaces%ROWTYPE;
BEGIN
  SELECT * INTO candidate
  FROM candidate_workspaces
  WHERE id = NEW.candidate_id
  FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'CandidateWorkspace does not exist'
      USING ERRCODE = '23503';
  END IF;

  IF candidate.status <> 'active'
     OR candidate.version <> NEW.candidate_version_from
     OR NEW.candidate_version_to <> candidate.version + 1
     OR NEW.sequence <> candidate.journal_sequence + 1
     OR NEW.session_epoch <> candidate.session_epoch
     OR candidate.writer_lease_owner_id IS DISTINCT FROM NEW.actor_id
     OR candidate.writer_lease_epoch <> NEW.writer_lease_epoch
     OR candidate.writer_lease_expires_at IS NULL
     OR statement_timestamp() >= candidate.writer_lease_expires_at
     OR candidate.current_tree_store <> NEW.before_tree_store
     OR candidate.current_tree_owner_id <> NEW.before_tree_owner_id
     OR candidate.current_tree_ref <> NEW.before_tree_ref
     OR candidate.current_tree_content_hash <> NEW.before_tree_content_hash
     OR candidate.current_tree_hash <> NEW.before_tree_hash THEN
    RAISE EXCEPTION 'Candidate journal append failed lease, epoch, CAS version, sequence, or tree precondition'
      USING ERRCODE = '40001';
  END IF;

  IF repository_path_matches_template_policy(
       candidate.full_stack_template_id, candidate.full_stack_template_hash,
       NEW.path, 'protectedPaths'
     ) OR (
       NEW.from_path IS NOT NULL
       AND repository_path_matches_template_policy(
         candidate.full_stack_template_id, candidate.full_stack_template_hash,
         NEW.from_path, 'protectedPaths'
       )
     ) THEN
    RAISE EXCEPTION 'Candidate journal operation targets an exact TemplateRelease protected path'
      USING ERRCODE = '42501';
  END IF;

  IF NEW.attribution = 'agent' AND (
       NOT repository_path_matches_template_policy(
         candidate.full_stack_template_id, candidate.full_stack_template_hash,
         NEW.path, 'extensionPaths'
       ) OR (
         NEW.from_path IS NOT NULL
         AND NOT repository_path_matches_template_policy(
           candidate.full_stack_template_id, candidate.full_stack_template_hash,
           NEW.from_path, 'extensionPaths'
         )
       )
     ) THEN
    RAISE EXCEPTION 'Agent Candidate journal operations must remain inside exact TemplateRelease extension paths'
      USING ERRCODE = '42501';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_workspace_journal_append_guard
BEFORE INSERT ON candidate_workspace_journal
FOR EACH ROW EXECUTE FUNCTION validate_candidate_workspace_journal_append();

CREATE OR REPLACE FUNCTION advance_candidate_workspace_from_journal()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE candidate_workspaces
  SET current_tree_store = NEW.after_tree_store,
      current_tree_owner_id = NEW.after_tree_owner_id,
      current_tree_ref = NEW.after_tree_ref,
      current_tree_content_hash = NEW.after_tree_content_hash,
      current_tree_hash = NEW.after_tree_hash,
      current_tree_file_count = NEW.after_tree_file_count,
      current_tree_byte_size = NEW.after_tree_byte_size,
      dirty = true,
      version = NEW.candidate_version_to,
      journal_sequence = NEW.sequence,
      updated_at = NEW.created_at
  WHERE id = NEW.candidate_id
    AND version = NEW.candidate_version_from
    AND journal_sequence = NEW.sequence - 1;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate journal CAS update lost its writer fence'
      USING ERRCODE = '40001';
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER candidate_workspace_journal_advance
AFTER INSERT ON candidate_workspace_journal
FOR EACH ROW EXECUTE FUNCTION advance_candidate_workspace_from_journal();

CREATE TABLE candidate_workspace_control_events (
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  candidate_version_from bigint NOT NULL CHECK (candidate_version_from > 0),
  candidate_version_to bigint NOT NULL CHECK (candidate_version_to = candidate_version_from + 1),
  event_kind text NOT NULL CHECK (event_kind IN (
    'lease.acquired', 'session.rotated', 'candidate.flags_updated',
    'candidate.frozen', 'candidate.abandoned'
  )),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  session_epoch_from bigint NOT NULL CHECK (session_epoch_from > 0),
  session_epoch_to bigint NOT NULL CHECK (session_epoch_to > 0),
  writer_lease_epoch_from bigint NOT NULL CHECK (writer_lease_epoch_from >= 0),
  writer_lease_epoch_to bigint NOT NULL CHECK (writer_lease_epoch_to = writer_lease_epoch_from + 1),
  writer_lease_owner_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  writer_lease_expires_at timestamptz,
  conflicted_from boolean,
  conflicted_to boolean,
  stale_from boolean,
  stale_to boolean,
  rebase_required_from boolean,
  rebase_required_to boolean,
  target_status text CHECK (target_status IS NULL OR target_status IN ('frozen', 'abandoned')),
  candidate_snapshot_id uuid,
  reason text CHECK (reason IS NULL OR (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000)),
  evidence_ref text CHECK (
    evidence_ref IS NULL OR (evidence_ref = btrim(evidence_ref) AND length(evidence_ref) BETWEEN 1 AND 2000)
  ),
  evidence_hash text CHECK (evidence_hash IS NULL OR evidence_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  PRIMARY KEY (candidate_id, candidate_version_to),
  CONSTRAINT candidate_workspace_control_event_from_unique
    UNIQUE (candidate_id, candidate_version_from),
  CONSTRAINT candidate_workspace_control_event_evidence_shape CHECK (
    (evidence_ref IS NULL AND evidence_hash IS NULL)
    OR (evidence_ref IS NOT NULL AND evidence_hash IS NOT NULL)
  ),
  CONSTRAINT candidate_workspace_control_event_shape CHECK (
    (event_kind = 'lease.acquired'
      AND session_epoch_to = session_epoch_from
      AND writer_lease_owner_id IS NOT NULL
      AND writer_lease_expires_at IS NOT NULL
      AND ROW(conflicted_from, conflicted_to, stale_from, stale_to,
              rebase_required_from, rebase_required_to)
          IS NOT DISTINCT FROM ROW(NULL, NULL, NULL, NULL, NULL, NULL)
      AND target_status IS NULL
      AND candidate_snapshot_id IS NULL
      AND reason IS NULL
      AND evidence_ref IS NULL)
    OR
    (event_kind = 'session.rotated'
      AND session_epoch_to = session_epoch_from + 1
      AND writer_lease_owner_id IS NULL
      AND writer_lease_expires_at IS NULL
      AND ROW(conflicted_from, conflicted_to, stale_from, stale_to,
              rebase_required_from, rebase_required_to)
          IS NOT DISTINCT FROM ROW(NULL, NULL, NULL, NULL, NULL, NULL)
      AND target_status IS NULL
      AND candidate_snapshot_id IS NULL
      AND reason IS NULL
      AND evidence_ref IS NULL)
    OR
    (event_kind = 'candidate.flags_updated'
      AND session_epoch_to = session_epoch_from
      AND writer_lease_owner_id IS NULL
      AND writer_lease_expires_at IS NULL
      AND conflicted_from IS NOT NULL AND conflicted_to IS NOT NULL
      AND stale_from IS NOT NULL AND stale_to IS NOT NULL
      AND rebase_required_from IS NOT NULL AND rebase_required_to IS NOT NULL
      AND ROW(conflicted_from, stale_from, rebase_required_from)
          IS DISTINCT FROM ROW(conflicted_to, stale_to, rebase_required_to)
      AND target_status IS NULL
      AND candidate_snapshot_id IS NULL
      AND reason IS NOT NULL
      AND evidence_ref IS NOT NULL)
    OR
    (event_kind = 'candidate.frozen'
      AND session_epoch_to = session_epoch_from
      AND writer_lease_owner_id IS NULL
      AND writer_lease_expires_at IS NULL
      AND ROW(conflicted_from, conflicted_to, stale_from, stale_to,
              rebase_required_from, rebase_required_to)
          IS NOT DISTINCT FROM ROW(NULL, NULL, NULL, NULL, NULL, NULL)
      AND target_status = 'frozen'
      AND candidate_snapshot_id IS NOT NULL
      AND reason IS NOT NULL
      AND evidence_ref IS NULL)
    OR
    (event_kind = 'candidate.abandoned'
      AND session_epoch_to = session_epoch_from
      AND writer_lease_owner_id IS NULL
      AND writer_lease_expires_at IS NULL
      AND ROW(conflicted_from, conflicted_to, stale_from, stale_to,
              rebase_required_from, rebase_required_to)
          IS NOT DISTINCT FROM ROW(NULL, NULL, NULL, NULL, NULL, NULL)
      AND target_status = 'abandoned'
      AND reason IS NOT NULL
      AND evidence_ref IS NULL)
  )
);

CREATE INDEX candidate_workspace_control_events_actor_idx
  ON candidate_workspace_control_events (actor_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_candidate_workspace_control_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  candidate candidate_workspaces%ROWTYPE;
BEGIN
  SELECT * INTO candidate
  FROM candidate_workspaces
  WHERE id = NEW.candidate_id
  FOR UPDATE;

  IF NOT FOUND
     OR candidate.status <> 'active'
     OR candidate.version <> NEW.candidate_version_from
     OR NEW.candidate_version_to <> candidate.version + 1
     OR candidate.session_epoch <> NEW.session_epoch_from
     OR candidate.writer_lease_epoch <> NEW.writer_lease_epoch_from THEN
    RAISE EXCEPTION 'Candidate control event failed its state, epoch, or CAS version precondition'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.event_kind = 'lease.acquired' THEN
    IF NEW.actor_id <> NEW.writer_lease_owner_id
       OR NEW.writer_lease_expires_at <= statement_timestamp()
       OR NEW.writer_lease_expires_at > statement_timestamp() + interval '30 minutes'
       OR (
         candidate.writer_lease_owner_id IS NOT NULL
         AND candidate.writer_lease_expires_at > statement_timestamp()
         AND candidate.writer_lease_owner_id <> NEW.writer_lease_owner_id
       ) THEN
      RAISE EXCEPTION 'Candidate control event cannot steal or create an invalid writer lease'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.event_kind = 'candidate.flags_updated' THEN
    IF ROW(NEW.conflicted_from, NEW.stale_from, NEW.rebase_required_from)
         IS DISTINCT FROM ROW(candidate.conflicted, candidate.stale, candidate.rebase_required) THEN
      RAISE EXCEPTION 'Candidate flag transition does not match the exact persisted flag state'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.event_kind = 'candidate.frozen' THEN
    IF candidate.conflicted OR candidate.stale OR candidate.rebase_required THEN
      RAISE EXCEPTION 'Candidate cannot freeze while conflicted, stale, or requiring rebase'
        USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
      SELECT 1
      FROM candidate_snapshots AS snapshot
      WHERE snapshot.id = NEW.candidate_snapshot_id
        AND snapshot.candidate_id = candidate.id
        AND snapshot.project_id = candidate.project_id
        AND snapshot.candidate_version = candidate.version
        AND snapshot.journal_sequence = candidate.journal_sequence
        AND snapshot.session_epoch = candidate.session_epoch
        AND snapshot.writer_lease_epoch = candidate.writer_lease_epoch
        AND snapshot.tree_store = candidate.current_tree_store
        AND snapshot.tree_owner_id = candidate.current_tree_owner_id
        AND snapshot.tree_ref = candidate.current_tree_ref
        AND snapshot.tree_content_hash = candidate.current_tree_content_hash
        AND snapshot.tree_hash = candidate.current_tree_hash
        AND snapshot.tree_file_count = candidate.current_tree_file_count
        AND snapshot.tree_byte_size = candidate.current_tree_byte_size
    ) THEN
      RAISE EXCEPTION 'Candidate freeze requires the exact current fenced CandidateSnapshot'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.event_kind = 'candidate.abandoned' THEN
    IF candidate.dirty AND NEW.candidate_snapshot_id IS NULL THEN
      RAISE EXCEPTION 'Dirty Candidate abandonment requires an exact current CandidateSnapshot'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.candidate_snapshot_id IS NOT NULL AND NOT EXISTS (
      SELECT 1
      FROM candidate_snapshots AS snapshot
      WHERE snapshot.id = NEW.candidate_snapshot_id
        AND snapshot.candidate_id = candidate.id
        AND snapshot.project_id = candidate.project_id
        AND snapshot.candidate_version = candidate.version
        AND snapshot.journal_sequence = candidate.journal_sequence
        AND snapshot.session_epoch = candidate.session_epoch
        AND snapshot.writer_lease_epoch = candidate.writer_lease_epoch
        AND snapshot.tree_store = candidate.current_tree_store
        AND snapshot.tree_owner_id = candidate.current_tree_owner_id
        AND snapshot.tree_ref = candidate.current_tree_ref
        AND snapshot.tree_content_hash = candidate.current_tree_content_hash
        AND snapshot.tree_hash = candidate.current_tree_hash
        AND snapshot.tree_file_count = candidate.current_tree_file_count
        AND snapshot.tree_byte_size = candidate.current_tree_byte_size
    ) THEN
      RAISE EXCEPTION 'Candidate abandonment snapshot is not the exact current fenced CandidateSnapshot'
        USING ERRCODE = '40001';
    END IF;
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_workspace_control_event_guard
BEFORE INSERT ON candidate_workspace_control_events
FOR EACH ROW EXECUTE FUNCTION validate_candidate_workspace_control_event();

CREATE OR REPLACE FUNCTION advance_candidate_workspace_from_control_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE candidate_workspaces
  SET status = COALESCE(NEW.target_status, status),
      conflicted = COALESCE(NEW.conflicted_to, conflicted),
      stale = COALESCE(NEW.stale_to, stale),
      rebase_required = COALESCE(NEW.rebase_required_to, rebase_required),
      session_epoch = NEW.session_epoch_to,
      writer_lease_owner_id = NEW.writer_lease_owner_id,
      writer_lease_epoch = NEW.writer_lease_epoch_to,
      writer_lease_expires_at = NEW.writer_lease_expires_at,
      version = NEW.candidate_version_to,
      updated_at = NEW.created_at
  WHERE id = NEW.candidate_id
    AND version = NEW.candidate_version_from
    AND session_epoch = NEW.session_epoch_from
    AND writer_lease_epoch = NEW.writer_lease_epoch_from;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate control event CAS update lost its writer fence'
      USING ERRCODE = '40001';
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER candidate_workspace_control_event_advance
AFTER INSERT ON candidate_workspace_control_events
FOR EACH ROW EXECUTE FUNCTION advance_candidate_workspace_from_control_event();

CREATE OR REPLACE FUNCTION prevent_candidate_workspace_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  common_unchanged boolean;
  tree_unchanged boolean;
  state_unchanged boolean;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'CandidateWorkspace history cannot be deleted; abandon it instead'
      USING ERRCODE = '55000';
  END IF;

  NEW.updated_at := statement_timestamp();

  common_unchanged := ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.repository_snapshot_id,
    NEW.build_manifest_id, NEW.build_manifest_hash,
    NEW.build_contract_id, NEW.build_contract_hash,
    NEW.full_stack_template_id, NEW.full_stack_template_hash,
    NEW.base_workspace_artifact_id, NEW.base_workspace_revision_id,
    NEW.base_workspace_content_hash,
    NEW.base_tree_store, NEW.base_tree_owner_id, NEW.base_tree_ref,
    NEW.base_tree_content_hash, NEW.base_tree_hash,
    NEW.created_by, NEW.created_at
  ) IS NOT DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.repository_snapshot_id,
    OLD.build_manifest_id, OLD.build_manifest_hash,
    OLD.build_contract_id, OLD.build_contract_hash,
    OLD.full_stack_template_id, OLD.full_stack_template_hash,
    OLD.base_workspace_artifact_id, OLD.base_workspace_revision_id,
    OLD.base_workspace_content_hash,
    OLD.base_tree_store, OLD.base_tree_owner_id, OLD.base_tree_ref,
    OLD.base_tree_content_hash, OLD.base_tree_hash,
    OLD.created_by, OLD.created_at
  );

  IF NOT common_unchanged OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'CandidateWorkspace identity is immutable and every transition must increment its CAS version once'
      USING ERRCODE = '55000';
  END IF;

  tree_unchanged := ROW(
    NEW.current_tree_store, NEW.current_tree_owner_id, NEW.current_tree_ref,
    NEW.current_tree_content_hash, NEW.current_tree_hash,
    NEW.current_tree_file_count, NEW.current_tree_byte_size,
    NEW.dirty, NEW.journal_sequence
  ) IS NOT DISTINCT FROM ROW(
    OLD.current_tree_store, OLD.current_tree_owner_id, OLD.current_tree_ref,
    OLD.current_tree_content_hash, OLD.current_tree_hash,
    OLD.current_tree_file_count, OLD.current_tree_byte_size,
    OLD.dirty, OLD.journal_sequence
  );

  state_unchanged := ROW(
    NEW.status, NEW.conflicted, NEW.stale, NEW.rebase_required,
    NEW.session_epoch
  ) IS NOT DISTINCT FROM ROW(
    OLD.status, OLD.conflicted, OLD.stale, OLD.rebase_required,
    OLD.session_epoch
  );

  -- A journal row inserted in this transaction is the sole authority allowed
  -- to advance the mutable tree. No direct tree UPDATE can satisfy this branch.
  IF NEW.journal_sequence = OLD.journal_sequence + 1
     AND NEW.dirty
     AND state_unchanged
     AND NEW.writer_lease_owner_id IS NOT DISTINCT FROM OLD.writer_lease_owner_id
     AND NEW.writer_lease_epoch = OLD.writer_lease_epoch
     AND NEW.writer_lease_expires_at IS NOT DISTINCT FROM OLD.writer_lease_expires_at
     AND EXISTS (
       SELECT 1
       FROM candidate_workspace_journal AS journal
       WHERE journal.candidate_id = NEW.id
         AND journal.sequence = NEW.journal_sequence
         AND journal.candidate_version_from = OLD.version
         AND journal.candidate_version_to = NEW.version
         AND journal.before_tree_store = OLD.current_tree_store
         AND journal.before_tree_owner_id = OLD.current_tree_owner_id
         AND journal.before_tree_ref = OLD.current_tree_ref
         AND journal.before_tree_content_hash = OLD.current_tree_content_hash
         AND journal.before_tree_hash = OLD.current_tree_hash
         AND journal.after_tree_store = NEW.current_tree_store
         AND journal.after_tree_owner_id = NEW.current_tree_owner_id
         AND journal.after_tree_ref = NEW.current_tree_ref
         AND journal.after_tree_content_hash = NEW.current_tree_content_hash
         AND journal.after_tree_hash = NEW.current_tree_hash
         AND journal.after_tree_file_count = NEW.current_tree_file_count
         AND journal.after_tree_byte_size = NEW.current_tree_byte_size
         AND journal.creation_transaction_id = txid_current()
     ) THEN
    RETURN NEW;
  END IF;

  -- Lease/session/terminal transitions are authorized by an append-only CAS
  -- control event inserted in the same transaction. Direct Candidate UPDATEs
  -- cannot manufacture one of these transitions.
  IF tree_unchanged
     AND NEW.writer_lease_epoch = OLD.writer_lease_epoch + 1
     AND EXISTS (
       SELECT 1
       FROM candidate_workspace_control_events AS event
       WHERE event.candidate_id = NEW.id
         AND event.candidate_version_from = OLD.version
         AND event.candidate_version_to = NEW.version
         AND event.session_epoch_from = OLD.session_epoch
         AND event.session_epoch_to = NEW.session_epoch
         AND event.writer_lease_epoch_from = OLD.writer_lease_epoch
         AND event.writer_lease_epoch_to = NEW.writer_lease_epoch
         AND event.writer_lease_owner_id IS NOT DISTINCT FROM NEW.writer_lease_owner_id
         AND event.writer_lease_expires_at IS NOT DISTINCT FROM NEW.writer_lease_expires_at
         AND COALESCE(event.target_status, OLD.status) = NEW.status
         AND COALESCE(event.conflicted_from, OLD.conflicted) = OLD.conflicted
         AND COALESCE(event.stale_from, OLD.stale) = OLD.stale
         AND COALESCE(event.rebase_required_from, OLD.rebase_required) = OLD.rebase_required
         AND COALESCE(event.conflicted_to, OLD.conflicted) = NEW.conflicted
         AND COALESCE(event.stale_to, OLD.stale) = NEW.stale
         AND COALESCE(event.rebase_required_to, OLD.rebase_required) = NEW.rebase_required
         AND event.creation_transaction_id = txid_current()
     ) THEN
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'CandidateWorkspace can change only through a lease, session fence, journal append, or terminal transition'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_workspace_mutation_guard
BEFORE UPDATE OR DELETE ON candidate_workspaces
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_workspace_mutation();

CREATE OR REPLACE FUNCTION acquire_candidate_workspace_lease(
  target_candidate_id uuid,
  expected_version bigint,
  owner_id uuid,
  ttl_seconds integer
)
RETURNS TABLE (
  candidate_version bigint,
  session_epoch bigint,
  writer_lease_epoch bigint,
  writer_lease_expires_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  IF ttl_seconds IS NULL OR ttl_seconds <= 0 OR ttl_seconds > 1800 THEN
    RAISE EXCEPTION 'Candidate writer lease TTL must be between 1 and 1800 seconds'
      USING ERRCODE = '22023';
  END IF;

  INSERT INTO candidate_workspace_control_events (
    candidate_id, candidate_version_from, candidate_version_to, event_kind, actor_id,
    session_epoch_from, session_epoch_to,
    writer_lease_epoch_from, writer_lease_epoch_to,
    writer_lease_owner_id, writer_lease_expires_at
  )
  SELECT candidate.id, candidate.version, candidate.version + 1, 'lease.acquired', owner_id,
         candidate.session_epoch, candidate.session_epoch,
         candidate.writer_lease_epoch, candidate.writer_lease_epoch + 1,
         owner_id, statement_timestamp() + make_interval(secs => ttl_seconds)
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = target_candidate_id
    AND candidate.version = expected_version;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate writer lease CAS failed'
      USING ERRCODE = '40001';
  END IF;

  RETURN QUERY
  SELECT candidate.version, candidate.session_epoch,
         candidate.writer_lease_epoch, candidate.writer_lease_expires_at
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = target_candidate_id;
END;
$$;

CREATE OR REPLACE FUNCTION rotate_candidate_workspace_session(
  target_candidate_id uuid,
  expected_version bigint,
  expected_session_epoch bigint,
  actor_id uuid
)
RETURNS TABLE (candidate_version bigint, session_epoch bigint, writer_lease_epoch bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
BEGIN
  INSERT INTO candidate_workspace_control_events (
    candidate_id, candidate_version_from, candidate_version_to, event_kind, actor_id,
    session_epoch_from, session_epoch_to,
    writer_lease_epoch_from, writer_lease_epoch_to
  )
  SELECT candidate.id, candidate.version, candidate.version + 1, 'session.rotated', actor_id,
         candidate.session_epoch, candidate.session_epoch + 1,
         candidate.writer_lease_epoch, candidate.writer_lease_epoch + 1
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = target_candidate_id
    AND candidate.version = expected_version
    AND candidate.session_epoch = expected_session_epoch;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate session rotation CAS failed'
      USING ERRCODE = '40001';
  END IF;

  RETURN QUERY
  SELECT candidate.version, candidate.session_epoch, candidate.writer_lease_epoch
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = target_candidate_id;
END;
$$;

CREATE OR REPLACE FUNCTION update_candidate_workspace_flags(
  target_candidate_id uuid,
  expected_version bigint,
  expected_session_epoch bigint,
  expected_writer_lease_epoch bigint,
  actor_id uuid,
  target_conflicted boolean,
  target_stale boolean,
  target_rebase_required boolean,
  transition_reason text,
  transition_evidence_ref text,
  transition_evidence_hash text
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  next_version bigint;
BEGIN
  IF target_conflicted IS NULL OR target_stale IS NULL OR target_rebase_required IS NULL THEN
    RAISE EXCEPTION 'Candidate flag targets are required'
      USING ERRCODE = '22023';
  END IF;

  INSERT INTO candidate_workspace_control_events (
    candidate_id, candidate_version_from, candidate_version_to, event_kind, actor_id,
    session_epoch_from, session_epoch_to,
    writer_lease_epoch_from, writer_lease_epoch_to,
    conflicted_from, conflicted_to, stale_from, stale_to,
    rebase_required_from, rebase_required_to,
    reason, evidence_ref, evidence_hash
  )
  SELECT candidate.id, candidate.version, candidate.version + 1, 'candidate.flags_updated',
         actor_id, candidate.session_epoch, candidate.session_epoch,
         candidate.writer_lease_epoch, candidate.writer_lease_epoch + 1,
         candidate.conflicted, target_conflicted, candidate.stale, target_stale,
         candidate.rebase_required, target_rebase_required,
         transition_reason, transition_evidence_ref, transition_evidence_hash
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = target_candidate_id
    AND candidate.version = expected_version
    AND candidate.session_epoch = expected_session_epoch
    AND candidate.writer_lease_epoch = expected_writer_lease_epoch;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate flag transition CAS failed'
      USING ERRCODE = '40001';
  END IF;
  SELECT version INTO next_version
  FROM candidate_workspaces
  WHERE id = target_candidate_id;
  RETURN next_version;
END;
$$;

CREATE OR REPLACE FUNCTION freeze_candidate_workspace(
  target_candidate_id uuid,
  expected_version bigint,
  expected_session_epoch bigint,
  expected_writer_lease_epoch bigint,
  actor_id uuid,
  exact_candidate_snapshot_id uuid,
  freeze_reason text
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  next_version bigint;
BEGIN
  INSERT INTO candidate_workspace_control_events (
    candidate_id, candidate_version_from, candidate_version_to, event_kind, actor_id,
    session_epoch_from, session_epoch_to,
    writer_lease_epoch_from, writer_lease_epoch_to,
    target_status, candidate_snapshot_id, reason
  )
  SELECT candidate.id, candidate.version, candidate.version + 1, 'candidate.frozen', actor_id,
         candidate.session_epoch, candidate.session_epoch,
         candidate.writer_lease_epoch, candidate.writer_lease_epoch + 1,
         'frozen', exact_candidate_snapshot_id, freeze_reason
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = target_candidate_id
    AND candidate.version = expected_version
    AND candidate.session_epoch = expected_session_epoch
    AND candidate.writer_lease_epoch = expected_writer_lease_epoch;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate freeze CAS failed'
      USING ERRCODE = '40001';
  END IF;
  SELECT version INTO next_version FROM candidate_workspaces WHERE id = target_candidate_id;
  RETURN next_version;
END;
$$;

CREATE OR REPLACE FUNCTION abandon_candidate_workspace(
  target_candidate_id uuid,
  expected_version bigint,
  expected_session_epoch bigint,
  expected_writer_lease_epoch bigint,
  actor_id uuid,
  exact_candidate_snapshot_id uuid,
  abandonment_reason text
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $$
DECLARE
  next_version bigint;
BEGIN
  INSERT INTO candidate_workspace_control_events (
    candidate_id, candidate_version_from, candidate_version_to, event_kind, actor_id,
    session_epoch_from, session_epoch_to,
    writer_lease_epoch_from, writer_lease_epoch_to,
    target_status, candidate_snapshot_id, reason
  )
  SELECT candidate.id, candidate.version, candidate.version + 1, 'candidate.abandoned', actor_id,
         candidate.session_epoch, candidate.session_epoch,
         candidate.writer_lease_epoch, candidate.writer_lease_epoch + 1,
         'abandoned', exact_candidate_snapshot_id, abandonment_reason
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = target_candidate_id
    AND candidate.version = expected_version
    AND candidate.session_epoch = expected_session_epoch
    AND candidate.writer_lease_epoch = expected_writer_lease_epoch;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate abandonment CAS failed'
      USING ERRCODE = '40001';
  END IF;
  SELECT version INTO next_version FROM candidate_workspaces WHERE id = target_candidate_id;
  RETURN next_version;
END;
$$;

CREATE OR REPLACE FUNCTION validate_candidate_workspace_journal_chain()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_candidate_id uuid;
  candidate candidate_workspaces%ROWTYPE;
  projected_count bigint;
  minimum_sequence bigint;
  maximum_sequence bigint;
  first_tree_store text;
  first_tree_owner_id uuid;
  first_tree_ref text;
  first_tree_content_hash text;
  first_tree_hash text;
  last_tree_store text;
  last_tree_owner_id uuid;
  last_tree_ref text;
  last_tree_content_hash text;
  last_tree_hash text;
  last_file_count integer;
  last_byte_size bigint;
BEGIN
  IF TG_TABLE_NAME = 'candidate_workspaces' THEN
    target_candidate_id := NEW.id;
  ELSE
    target_candidate_id := NEW.candidate_id;
  END IF;

  SELECT * INTO candidate FROM candidate_workspaces WHERE id = target_candidate_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;

  SELECT count(*), min(sequence), max(sequence)
    INTO projected_count, minimum_sequence, maximum_sequence
  FROM candidate_workspace_journal
  WHERE candidate_id = target_candidate_id;

  IF projected_count <> candidate.journal_sequence
     OR (projected_count > 0 AND (minimum_sequence <> 1 OR maximum_sequence <> projected_count)) THEN
    RAISE EXCEPTION 'Candidate journal sequence is not contiguous or does not match the parent cursor'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM (
      SELECT sequence, candidate_version_from, candidate_version_to,
             before_tree_store, before_tree_owner_id, before_tree_ref,
             before_tree_content_hash, before_tree_hash,
             lag(candidate_version_to) OVER (ORDER BY sequence) AS previous_version,
             lag(after_tree_store) OVER (ORDER BY sequence) AS previous_store,
             lag(after_tree_owner_id) OVER (ORDER BY sequence) AS previous_owner_id,
             lag(after_tree_ref) OVER (ORDER BY sequence) AS previous_ref,
             lag(after_tree_content_hash) OVER (ORDER BY sequence) AS previous_content_hash,
             lag(after_tree_hash) OVER (ORDER BY sequence) AS previous_hash
      FROM candidate_workspace_journal
      WHERE candidate_id = target_candidate_id
    ) AS chain
    WHERE (sequence > 1 AND (
            candidate_version_from < previous_version
            OR before_tree_store <> previous_store
            OR before_tree_owner_id <> previous_owner_id
            OR before_tree_ref <> previous_ref
            OR before_tree_content_hash <> previous_content_hash
            OR before_tree_hash <> previous_hash
          ))
       OR candidate_version_to > candidate.version
  ) THEN
    RAISE EXCEPTION 'Candidate journal version or tree chain is inconsistent'
      USING ERRCODE = '23514';
  END IF;

  IF projected_count = 0 THEN
    IF ROW(candidate.current_tree_store, candidate.current_tree_owner_id, candidate.current_tree_ref,
           candidate.current_tree_content_hash, candidate.current_tree_hash)
       IS DISTINCT FROM ROW(candidate.base_tree_store, candidate.base_tree_owner_id, candidate.base_tree_ref,
                            candidate.base_tree_content_hash, candidate.base_tree_hash) THEN
      RAISE EXCEPTION 'Candidate without journal entries must retain its exact base tree'
        USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
  END IF;

  SELECT before_tree_store, before_tree_owner_id, before_tree_ref,
         before_tree_content_hash, before_tree_hash
    INTO first_tree_store, first_tree_owner_id, first_tree_ref,
         first_tree_content_hash, first_tree_hash
  FROM candidate_workspace_journal
  WHERE candidate_id = target_candidate_id
  ORDER BY sequence ASC
  LIMIT 1;

  SELECT after_tree_store, after_tree_owner_id, after_tree_ref,
         after_tree_content_hash, after_tree_hash,
         after_tree_file_count, after_tree_byte_size
    INTO last_tree_store, last_tree_owner_id, last_tree_ref,
         last_tree_content_hash, last_tree_hash, last_file_count, last_byte_size
  FROM candidate_workspace_journal
  WHERE candidate_id = target_candidate_id
  ORDER BY sequence DESC
  LIMIT 1;

  IF ROW(first_tree_store, first_tree_owner_id, first_tree_ref, first_tree_content_hash, first_tree_hash)
       IS DISTINCT FROM ROW(candidate.base_tree_store, candidate.base_tree_owner_id, candidate.base_tree_ref,
                            candidate.base_tree_content_hash, candidate.base_tree_hash)
     OR ROW(last_tree_store, last_tree_owner_id, last_tree_ref, last_tree_content_hash,
            last_tree_hash, last_file_count, last_byte_size)
       IS DISTINCT FROM ROW(
         candidate.current_tree_store, candidate.current_tree_owner_id, candidate.current_tree_ref,
         candidate.current_tree_content_hash, candidate.current_tree_hash,
         candidate.current_tree_file_count, candidate.current_tree_byte_size
       ) THEN
    RAISE EXCEPTION 'Candidate journal endpoints do not match the exact base/current tree references'
      USING ERRCODE = '23514';
  END IF;

  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER candidate_workspace_journal_parent_guard
AFTER INSERT OR UPDATE ON candidate_workspaces
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_workspace_journal_chain();

CREATE CONSTRAINT TRIGGER candidate_workspace_journal_chain_guard
AFTER INSERT ON candidate_workspace_journal
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_workspace_journal_chain();

CREATE OR REPLACE FUNCTION prevent_candidate_workspace_journal_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Candidate journal is append-only'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_workspace_journal_immutable
BEFORE UPDATE OR DELETE ON candidate_workspace_journal
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_workspace_journal_mutation();

CREATE OR REPLACE FUNCTION prevent_candidate_workspace_control_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Candidate control events are append-only'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_workspace_control_event_immutable
BEFORE UPDATE OR DELETE ON candidate_workspace_control_events
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_workspace_control_event_mutation();

CREATE TABLE candidate_snapshots (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'candidate-snapshot/v1'),
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  candidate_version bigint NOT NULL CHECK (candidate_version > 0),
  journal_sequence bigint NOT NULL CHECK (journal_sequence >= 0),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  writer_lease_epoch bigint NOT NULL CHECK (writer_lease_epoch > 0),
  tree_store text NOT NULL CHECK (tree_store = btrim(tree_store) AND length(tree_store) > 0),
  tree_owner_id uuid NOT NULL,
  tree_ref text NOT NULL CHECK (tree_ref = btrim(tree_ref) AND length(tree_ref) > 0),
  tree_content_hash text NOT NULL CHECK (tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  tree_file_count integer NOT NULL CHECK (tree_file_count BETWEEN 0 AND 20000),
  tree_byte_size bigint NOT NULL CHECK (tree_byte_size BETWEEN 0 AND 67108864),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT candidate_snapshot_exact_version_unique UNIQUE (candidate_id, candidate_version, tree_hash)
);

CREATE INDEX candidate_snapshots_candidate_created_idx
  ON candidate_snapshots (candidate_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_candidate_snapshot_exact_tree()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  candidate candidate_workspaces%ROWTYPE;
BEGIN
  SELECT * INTO candidate
  FROM candidate_workspaces
  WHERE id = NEW.candidate_id
  FOR SHARE;

  IF NOT FOUND
     OR candidate.status <> 'active'
     OR candidate.project_id <> NEW.project_id
     OR candidate.version <> NEW.candidate_version
     OR candidate.journal_sequence <> NEW.journal_sequence
     OR candidate.session_epoch <> NEW.session_epoch
     OR candidate.writer_lease_epoch <> NEW.writer_lease_epoch
     OR candidate.writer_lease_owner_id IS DISTINCT FROM NEW.created_by
     OR candidate.writer_lease_expires_at IS NULL
     OR statement_timestamp() >= candidate.writer_lease_expires_at
     OR candidate.current_tree_store <> NEW.tree_store
     OR candidate.current_tree_owner_id <> NEW.tree_owner_id
     OR candidate.current_tree_ref <> NEW.tree_ref
     OR candidate.current_tree_content_hash <> NEW.tree_content_hash
     OR candidate.current_tree_hash <> NEW.tree_hash
     OR candidate.current_tree_file_count <> NEW.tree_file_count
     OR candidate.current_tree_byte_size <> NEW.tree_byte_size THEN
    RAISE EXCEPTION 'CandidateSnapshot must capture the exact current Candidate tree and cursors'
      USING ERRCODE = '40001';
  END IF;

  NEW.created_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_snapshot_exact_tree_guard
BEFORE INSERT ON candidate_snapshots
FOR EACH ROW EXECUTE FUNCTION validate_candidate_snapshot_exact_tree();

CREATE OR REPLACE FUNCTION prevent_candidate_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'CandidateSnapshot is immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_snapshot_immutable
BEFORE UPDATE OR DELETE ON candidate_snapshots
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_snapshot_mutation();

ALTER TABLE candidate_workspace_control_events
  ADD CONSTRAINT candidate_workspace_control_event_snapshot_fk
  FOREIGN KEY (candidate_snapshot_id)
  REFERENCES candidate_snapshots(id)
  ON DELETE RESTRICT;

-- Tree blobs live outside PostgreSQL. These tables bind exact owner/ref,
-- content-object hash, semantic tree hash, and cursors, but PostgreSQL cannot
-- prove that an after-tree contains only the declared FileOperation delta.
-- Runtime roles must therefore use the Repository Service's deterministic
-- TreeStore Apply boundary; the migration owner is not a runtime identity.
COMMENT ON TABLE repository_snapshots IS
  'Direct runtime DML forbidden: immutable tree pointers are written only after verified TreeStore materialization.';
COMMENT ON TABLE candidate_workspace_journal IS
  'Direct runtime DML forbidden: after-tree pointers require deterministic verified TreeStore Apply; SQL does not inspect blob contents.';
COMMENT ON TABLE candidate_snapshots IS
  'Direct runtime DML forbidden: checkpoints reuse the exact fenced Candidate tree pointer.';

REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON repository_snapshots FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON candidate_workspaces FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON candidate_workspace_journal FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON candidate_workspace_control_events FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON candidate_snapshots FROM PUBLIC;

REVOKE ALL ON FUNCTION acquire_candidate_workspace_lease(uuid, bigint, uuid, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION rotate_candidate_workspace_session(uuid, bigint, bigint, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION update_candidate_workspace_flags(uuid, bigint, bigint, bigint, uuid, boolean, boolean, boolean, text, text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION freeze_candidate_workspace(uuid, bigint, bigint, bigint, uuid, uuid, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION abandon_candidate_workspace(uuid, bigint, bigint, bigint, uuid, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acquire_candidate_workspace_lease(uuid, bigint, uuid, integer) TO PUBLIC;
GRANT EXECUTE ON FUNCTION rotate_candidate_workspace_session(uuid, bigint, bigint, uuid) TO PUBLIC;
GRANT EXECUTE ON FUNCTION update_candidate_workspace_flags(uuid, bigint, bigint, bigint, uuid, boolean, boolean, boolean, text, text, text) TO PUBLIC;
GRANT EXECUTE ON FUNCTION freeze_candidate_workspace(uuid, bigint, bigint, bigint, uuid, uuid, text) TO PUBLIC;
GRANT EXECUTE ON FUNCTION abandon_candidate_workspace(uuid, bigint, bigint, bigint, uuid, uuid, text) TO PUBLIC;
