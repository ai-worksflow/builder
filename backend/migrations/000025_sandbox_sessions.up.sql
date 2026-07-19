-- SandboxSession is a server-authoritative projection over an exact mutable
-- CandidateWorkspace. Configuration and lineage are sealed at creation;
-- current state can advance only from append-only, CAS-fenced events.

CREATE OR REPLACE FUNCTION sandbox_profiles_are_valid(profiles jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(profiles) <> 'array' THEN false
    WHEN jsonb_array_length(profiles) NOT BETWEEN 1 AND 8 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(profiles) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'string'
         OR item.value #>> '{}' <> btrim(item.value #>> '{}')
         OR item.value #>> '{}' !~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'
         OR length(item.value #>> '{}') > 80
    ) AND (
      SELECT count(*) = count(DISTINCT item.value #>> '{}')
      FROM jsonb_array_elements(profiles) AS item(value)
    )
  END
$$;

CREATE TABLE sandbox_sessions (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'sandbox-session/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  repository_snapshot_id uuid NOT NULL REFERENCES repository_snapshots(id) ON DELETE RESTRICT,
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  full_stack_template_id uuid NOT NULL,
  full_stack_template_hash text NOT NULL CHECK (full_stack_template_hash ~ '^sha256:[0-9a-f]{64}$'),
  base_workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  base_workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  base_workspace_content_hash text NOT NULL CHECK (base_workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  runner_image_digest text NOT NULL CHECK (runner_image_digest ~ '^sha256:[0-9a-f]{64}$'),
  candidate_version bigint NOT NULL CHECK (candidate_version > 0),
  candidate_journal_sequence bigint NOT NULL CHECK (candidate_journal_sequence >= 0),
  candidate_session_epoch bigint NOT NULL CHECK (candidate_session_epoch > 0),
  candidate_writer_lease_epoch bigint NOT NULL CHECK (candidate_writer_lease_epoch >= 0),
  candidate_tree_store text NOT NULL CHECK (candidate_tree_store = btrim(candidate_tree_store) AND length(candidate_tree_store) > 0),
  candidate_tree_owner_id uuid NOT NULL,
  candidate_tree_ref text NOT NULL CHECK (candidate_tree_ref = btrim(candidate_tree_ref) AND length(candidate_tree_ref) > 0),
  candidate_tree_content_hash text NOT NULL CHECK (candidate_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  candidate_tree_hash text NOT NULL CHECK (candidate_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  candidate_dirty boolean NOT NULL,
  candidate_conflicted boolean NOT NULL,
  candidate_stale boolean NOT NULL,
  candidate_rebase_required boolean NOT NULL,
  latest_checkpoint_id uuid REFERENCES candidate_snapshots(id) ON DELETE RESTRICT,
  state text NOT NULL CHECK (state IN (
    'provisioning', 'starting', 'ready', 'suspending', 'suspended',
    'resuming', 'terminating', 'terminated', 'failed'
  )),
  version bigint NOT NULL CHECK (version > 0),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  cpu_millis integer NOT NULL CHECK (cpu_millis BETWEEN 100 AND 64000),
  memory_bytes bigint NOT NULL CHECK (memory_bytes BETWEEN 67108864 AND 274877906944),
  workspace_bytes bigint NOT NULL CHECK (workspace_bytes BETWEEN 1048576 AND 1099511627776),
  pid_limit integer NOT NULL CHECK (pid_limit BETWEEN 1 AND 32768),
  preview_port_limit integer NOT NULL CHECK (preview_port_limit BETWEEN 0 AND 64),
  idle_hibernate_seconds integer NOT NULL CHECK (idle_hibernate_seconds > 0),
  max_runtime_seconds integer NOT NULL CHECK (max_runtime_seconds BETWEEN 1 AND 604800),
  idle_deadline timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  failure_reason text CHECK (
    failure_reason IS NULL OR (failure_reason = btrim(failure_reason) AND length(failure_reason) BETWEEN 1 AND 2000)
  ),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT sandbox_session_build_contract_exact_fk
    FOREIGN KEY (build_contract_id, build_contract_hash)
    REFERENCES application_build_contracts(id, contract_hash) ON DELETE RESTRICT,
  CONSTRAINT sandbox_session_full_stack_exact_fk
    FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
    REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT sandbox_session_candidate_epoch_matches CHECK (
    candidate_session_epoch = session_epoch
  ),
  CONSTRAINT sandbox_session_ttl_order CHECK (
    idle_hibernate_seconds <= max_runtime_seconds
    AND idle_deadline <= expires_at
    AND updated_at >= created_at
  )
);

CREATE INDEX sandbox_sessions_project_state_idx
  ON sandbox_sessions (project_id, state, updated_at DESC, id DESC);

CREATE INDEX sandbox_sessions_candidate_idx
  ON sandbox_sessions (candidate_id, state, updated_at DESC);

CREATE INDEX sandbox_sessions_expiry_idx
  ON sandbox_sessions (expires_at, idle_deadline)
  WHERE state NOT IN ('terminated', 'failed');

CREATE TABLE sandbox_session_template_releases (
  session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  ordinal integer NOT NULL CHECK (ordinal >= 0),
  role text NOT NULL CHECK (role IN ('web', 'api', 'worker')),
  template_release_id uuid NOT NULL,
  template_release_content_hash text NOT NULL CHECK (template_release_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  PRIMARY KEY (session_id, role),
  CONSTRAINT sandbox_session_template_ordinal_unique UNIQUE (session_id, ordinal),
  CONSTRAINT sandbox_session_template_exact_unique UNIQUE (
    session_id, template_release_id, template_release_content_hash
  ),
  CONSTRAINT sandbox_session_template_release_exact_fk
    FOREIGN KEY (template_release_id, template_release_content_hash)
    REFERENCES template_releases(id, content_hash) ON DELETE RESTRICT
);

CREATE TABLE sandbox_session_services (
  session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  service_id text NOT NULL CHECK (
    service_id = btrim(service_id)
    AND service_id ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'
    AND length(service_id) <= 80
  ),
  kind text NOT NULL CHECK (kind IN ('web', 'api', 'worker')),
  profiles jsonb NOT NULL CHECK (sandbox_profiles_are_valid(profiles)),
  template_release_id uuid NOT NULL,
  template_release_content_hash text NOT NULL CHECK (template_release_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  PRIMARY KEY (session_id, service_id),
  CONSTRAINT sandbox_session_service_template_fk
    FOREIGN KEY (session_id, template_release_id, template_release_content_hash)
    REFERENCES sandbox_session_template_releases(
      session_id, template_release_id, template_release_content_hash
    ) ON DELETE RESTRICT
);

CREATE TABLE sandbox_session_ports (
  session_id uuid NOT NULL,
  port_name text NOT NULL CHECK (
    port_name = btrim(port_name)
    AND port_name ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'
    AND length(port_name) <= 80
  ),
  service_id text NOT NULL,
  port_number integer NOT NULL CHECK (port_number BETWEEN 1024 AND 65535),
  protocol text NOT NULL CHECK (protocol IN ('http', 'https', 'tcp')),
  PRIMARY KEY (session_id, port_name),
  CONSTRAINT sandbox_session_port_number_unique UNIQUE (session_id, port_number),
  CONSTRAINT sandbox_session_port_service_fk
    FOREIGN KEY (session_id, service_id)
    REFERENCES sandbox_session_services(session_id, service_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION sandbox_checkpoint_is_exact(
  checkpoint_id uuid,
  target_candidate_id uuid,
  target_project_id uuid,
  target_candidate_version bigint,
  target_journal_sequence bigint,
  target_session_epoch bigint,
  target_writer_lease_epoch bigint,
  target_tree_store text,
  target_tree_owner_id uuid,
  target_tree_ref text,
  target_tree_content_hash text,
  target_tree_hash text
)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
  SELECT checkpoint_id IS NOT NULL AND EXISTS (
    SELECT 1
    FROM candidate_snapshots AS checkpoint
    WHERE checkpoint.id = checkpoint_id
      AND checkpoint.candidate_id = target_candidate_id
      AND checkpoint.project_id = target_project_id
      AND checkpoint.candidate_version = target_candidate_version
      AND checkpoint.journal_sequence = target_journal_sequence
      AND checkpoint.session_epoch = target_session_epoch
      AND checkpoint.writer_lease_epoch = target_writer_lease_epoch
      AND checkpoint.tree_store = target_tree_store
      AND checkpoint.tree_owner_id = target_tree_owner_id
      AND checkpoint.tree_ref = target_tree_ref
      AND checkpoint.tree_content_hash = target_tree_content_hash
      AND checkpoint.tree_hash = target_tree_hash
  )
$$;

CREATE OR REPLACE FUNCTION validate_sandbox_session_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  NEW.creation_transaction_id := txid_current();
  NEW.idle_deadline := NEW.created_at + make_interval(secs => NEW.idle_hibernate_seconds);
  NEW.expires_at := NEW.created_at + make_interval(secs => NEW.max_runtime_seconds);

  IF NEW.state <> 'provisioning'
     OR NEW.version <> 1
     OR NEW.failure_reason IS NOT NULL
     OR NEW.idle_hibernate_seconds > NEW.max_runtime_seconds THEN
    RAISE EXCEPTION 'SandboxSession must start as provisioning version 1 with bounded TTL'
      USING ERRCODE = '23514';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM candidate_workspaces AS candidate
    JOIN repository_snapshots AS snapshot
      ON snapshot.id = candidate.repository_snapshot_id
     AND snapshot.project_id = candidate.project_id
    WHERE candidate.id = NEW.candidate_id
      AND candidate.status = 'active'
      AND candidate.project_id = NEW.project_id
      AND candidate.repository_snapshot_id = NEW.repository_snapshot_id
      AND candidate.build_manifest_id = NEW.build_manifest_id
      AND candidate.build_manifest_hash = NEW.build_manifest_hash
      AND candidate.build_contract_id = NEW.build_contract_id
      AND candidate.build_contract_hash = NEW.build_contract_hash
      AND candidate.full_stack_template_id = NEW.full_stack_template_id
      AND candidate.full_stack_template_hash = NEW.full_stack_template_hash
      AND candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id
      AND candidate.base_workspace_revision_id = NEW.base_workspace_revision_id
      AND candidate.base_workspace_content_hash = NEW.base_workspace_content_hash
      AND candidate.version = NEW.candidate_version
      AND candidate.journal_sequence = NEW.candidate_journal_sequence
      AND candidate.session_epoch = NEW.candidate_session_epoch
      AND candidate.writer_lease_epoch = NEW.candidate_writer_lease_epoch
      AND candidate.current_tree_store = NEW.candidate_tree_store
      AND candidate.current_tree_owner_id = NEW.candidate_tree_owner_id
      AND candidate.current_tree_ref = NEW.candidate_tree_ref
      AND candidate.current_tree_content_hash = NEW.candidate_tree_content_hash
      AND candidate.current_tree_hash = NEW.candidate_tree_hash
      AND candidate.dirty = NEW.candidate_dirty
      AND candidate.conflicted = NEW.candidate_conflicted
      AND candidate.stale = NEW.candidate_stale
      AND candidate.rebase_required = NEW.candidate_rebase_required
      AND candidate.session_epoch = NEW.session_epoch
      AND snapshot.build_manifest_id = NEW.build_manifest_id
      AND snapshot.build_manifest_hash = NEW.build_manifest_hash
      AND snapshot.build_contract_id = NEW.build_contract_id
      AND snapshot.build_contract_hash = NEW.build_contract_hash
      AND snapshot.full_stack_template_id = NEW.full_stack_template_id
      AND snapshot.full_stack_template_hash = NEW.full_stack_template_hash
      AND snapshot.base_workspace_artifact_id = NEW.base_workspace_artifact_id
      AND snapshot.base_workspace_revision_id = NEW.base_workspace_revision_id
      AND snapshot.base_workspace_content_hash = NEW.base_workspace_content_hash
  ) THEN
    RAISE EXCEPTION 'SandboxSession must bind the exact live Candidate and RepositorySnapshot lineage'
      USING ERRCODE = '23503';
  END IF;

  IF NOT exact_application_build_contract_is_ready(
    NEW.build_contract_id, NEW.build_contract_hash, NEW.project_id, NEW.build_manifest_id
  ) THEN
    RAISE EXCEPTION 'SandboxSession requires an exact ready Application Build Contract'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.latest_checkpoint_id IS NOT NULL AND NOT sandbox_checkpoint_is_exact(
    NEW.latest_checkpoint_id, NEW.candidate_id, NEW.project_id,
    NEW.candidate_version, NEW.candidate_journal_sequence,
    NEW.candidate_session_epoch, NEW.candidate_writer_lease_epoch,
    NEW.candidate_tree_store, NEW.candidate_tree_owner_id,
    NEW.candidate_tree_ref, NEW.candidate_tree_content_hash, NEW.candidate_tree_hash
  ) THEN
    RAISE EXCEPTION 'SandboxSession initial checkpoint is not exact for its Candidate'
      USING ERRCODE = '40001';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_session_insert_guard
BEFORE INSERT ON sandbox_sessions
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_insert();

CREATE OR REPLACE FUNCTION validate_sandbox_session_configuration_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_sessions%ROWTYPE;
BEGIN
  SELECT * INTO parent
  FROM sandbox_sessions
  WHERE id = NEW.session_id;

  IF NOT FOUND OR parent.creation_transaction_id <> txid_current() THEN
    RAISE EXCEPTION 'SandboxSession configuration is sealed after its creation transaction'
      USING ERRCODE = '55000';
  END IF;

  IF TG_TABLE_NAME = 'sandbox_session_template_releases' THEN
    IF NOT EXISTS (
      SELECT 1
      FROM application_build_contract_template_releases AS selected
      JOIN full_stack_template_components AS component
        ON component.full_stack_template_id = parent.full_stack_template_id
       AND component.full_stack_content_hash = parent.full_stack_template_hash
       AND component.role = selected.role
       AND component.template_release_id = selected.template_release_id
       AND component.template_release_content_hash = selected.template_release_content_hash
      WHERE selected.contract_id = parent.build_contract_id
        AND selected.ordinal = NEW.ordinal
        AND selected.role = NEW.role
        AND selected.template_release_id = NEW.template_release_id
        AND selected.template_release_content_hash = NEW.template_release_content_hash
    ) THEN
      RAISE EXCEPTION 'SandboxSession TemplateRelease is not exact for its BuildContract and FullStackTemplate'
        USING ERRCODE = '23503';
    END IF;
  ELSIF TG_TABLE_NAME = 'sandbox_session_services' THEN
    IF (SELECT count(*) FROM sandbox_session_services WHERE session_id = NEW.session_id) >= 16 THEN
      RAISE EXCEPTION 'SandboxSession services exceed the immutable service bound'
        USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
      SELECT 1
      FROM sandbox_session_template_releases AS selected
      WHERE selected.session_id = NEW.session_id
        AND selected.role = NEW.kind
        AND selected.template_release_id = NEW.template_release_id
        AND selected.template_release_content_hash = NEW.template_release_content_hash
    ) THEN
      RAISE EXCEPTION 'SandboxSession service is not bound to an exact selected TemplateRelease role'
        USING ERRCODE = '23503';
    END IF;
  ELSIF TG_TABLE_NAME = 'sandbox_session_ports' THEN
    IF (SELECT count(*) FROM sandbox_session_ports WHERE session_id = NEW.session_id) >= parent.preview_port_limit THEN
      RAISE EXCEPTION 'SandboxSession ports exceed the immutable preview-port quota'
        USING ERRCODE = '23514';
    END IF;
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_session_template_insert_guard
BEFORE INSERT ON sandbox_session_template_releases
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_configuration_insert();

CREATE TRIGGER sandbox_session_service_insert_guard
BEFORE INSERT ON sandbox_session_services
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_configuration_insert();

CREATE TRIGGER sandbox_session_port_insert_guard
BEFORE INSERT ON sandbox_session_ports
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_configuration_insert();

CREATE OR REPLACE FUNCTION prevent_sandbox_session_configuration_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'SandboxSession lineage, services, and ports are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER sandbox_session_template_immutable
BEFORE UPDATE OR DELETE ON sandbox_session_template_releases
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_session_configuration_mutation();

CREATE TRIGGER sandbox_session_service_immutable
BEFORE UPDATE OR DELETE ON sandbox_session_services
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_session_configuration_mutation();

CREATE TRIGGER sandbox_session_port_immutable
BEFORE UPDATE OR DELETE ON sandbox_session_ports
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_session_configuration_mutation();

CREATE OR REPLACE FUNCTION validate_sandbox_session_configuration_complete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF EXISTS (
    SELECT ordinal, role, template_release_id, template_release_content_hash
    FROM sandbox_session_template_releases
    WHERE session_id = NEW.id
    EXCEPT
    SELECT ordinal, role, template_release_id, template_release_content_hash
    FROM application_build_contract_template_releases
    WHERE contract_id = NEW.build_contract_id
  ) OR EXISTS (
    SELECT ordinal, role, template_release_id, template_release_content_hash
    FROM application_build_contract_template_releases
    WHERE contract_id = NEW.build_contract_id
    EXCEPT
    SELECT ordinal, role, template_release_id, template_release_content_hash
    FROM sandbox_session_template_releases
    WHERE session_id = NEW.id
  ) THEN
    RAISE EXCEPTION 'SandboxSession TemplateRelease lineage is incomplete or differs from its BuildContract'
      USING ERRCODE = '23514';
  END IF;

  IF (SELECT count(*) FROM sandbox_session_services WHERE session_id = NEW.id) NOT BETWEEN 1 AND 16
     OR EXISTS (
       SELECT 1
       FROM sandbox_session_template_releases AS selected
       WHERE selected.session_id = NEW.id
         AND NOT EXISTS (
           SELECT 1
           FROM sandbox_session_services AS service
           WHERE service.session_id = selected.session_id
             AND service.kind = selected.role
             AND service.template_release_id = selected.template_release_id
             AND service.template_release_content_hash = selected.template_release_content_hash
         )
     )
     OR (SELECT count(*) FROM sandbox_session_ports WHERE session_id = NEW.id) > NEW.preview_port_limit THEN
    RAISE EXCEPTION 'SandboxSession services or ports are not a complete bounded projection'
      USING ERRCODE = '23514';
  END IF;

  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER sandbox_session_configuration_complete
AFTER INSERT ON sandbox_sessions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_configuration_complete();

CREATE TABLE sandbox_session_transition_events (
  session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  sequence bigint NOT NULL CHECK (sequence > 0),
  session_version_from bigint NOT NULL CHECK (session_version_from > 0),
  session_version_to bigint NOT NULL CHECK (session_version_to = session_version_from + 1),
  state_from text NOT NULL CHECK (state_from IN (
    'provisioning', 'starting', 'ready', 'suspending', 'suspended',
    'resuming', 'terminating', 'terminated', 'failed'
  )),
  state_to text NOT NULL CHECK (state_to IN (
    'provisioning', 'starting', 'ready', 'suspending', 'suspended',
    'resuming', 'terminating', 'terminated', 'failed'
  )),
  session_epoch_from bigint NOT NULL CHECK (session_epoch_from > 0),
  session_epoch_to bigint NOT NULL CHECK (session_epoch_to > 0),
  event_kind text NOT NULL CHECK (event_kind IN (
    'candidate.synced', 'checkpoint.attached',
    'lifecycle.started', 'lifecycle.ready', 'lifecycle.suspend_requested',
    'lifecycle.suspended', 'lifecycle.resume_requested',
    'lifecycle.terminate_requested', 'lifecycle.cancelled',
    'lifecycle.terminated', 'lifecycle.failed'
  )),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 2000),
  candidate_version_from bigint NOT NULL CHECK (candidate_version_from > 0),
  candidate_version_to bigint NOT NULL CHECK (candidate_version_to >= candidate_version_from),
  candidate_journal_sequence_from bigint NOT NULL CHECK (candidate_journal_sequence_from >= 0),
  candidate_journal_sequence_to bigint NOT NULL CHECK (candidate_journal_sequence_to >= candidate_journal_sequence_from),
  candidate_session_epoch_from bigint NOT NULL CHECK (candidate_session_epoch_from > 0),
  candidate_session_epoch_to bigint NOT NULL CHECK (candidate_session_epoch_to > 0),
  candidate_writer_lease_epoch_from bigint NOT NULL CHECK (candidate_writer_lease_epoch_from >= 0),
  candidate_writer_lease_epoch_to bigint NOT NULL CHECK (
    candidate_writer_lease_epoch_to >= candidate_writer_lease_epoch_from
  ),
  candidate_tree_store_from text NOT NULL,
  candidate_tree_store_to text NOT NULL,
  candidate_tree_owner_id_from uuid NOT NULL,
  candidate_tree_owner_id_to uuid NOT NULL,
  candidate_tree_ref_from text NOT NULL,
  candidate_tree_ref_to text NOT NULL,
  candidate_tree_content_hash_from text NOT NULL CHECK (
    candidate_tree_content_hash_from ~ '^sha256:[0-9a-f]{64}$'
  ),
  candidate_tree_content_hash_to text NOT NULL CHECK (
    candidate_tree_content_hash_to ~ '^sha256:[0-9a-f]{64}$'
  ),
  candidate_tree_hash_from text NOT NULL CHECK (candidate_tree_hash_from ~ '^sha256:[0-9a-f]{64}$'),
  candidate_tree_hash_to text NOT NULL CHECK (candidate_tree_hash_to ~ '^sha256:[0-9a-f]{64}$'),
  candidate_dirty_from boolean NOT NULL,
  candidate_dirty_to boolean NOT NULL,
  candidate_conflicted_from boolean NOT NULL,
  candidate_conflicted_to boolean NOT NULL,
  candidate_stale_from boolean NOT NULL,
  candidate_stale_to boolean NOT NULL,
  candidate_rebase_required_from boolean NOT NULL,
  candidate_rebase_required_to boolean NOT NULL,
  latest_checkpoint_id_from uuid REFERENCES candidate_snapshots(id) ON DELETE RESTRICT,
  latest_checkpoint_id_to uuid REFERENCES candidate_snapshots(id) ON DELETE RESTRICT,
  failure_reason_from text,
  failure_reason_to text,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  PRIMARY KEY (session_id, sequence),
  CONSTRAINT sandbox_session_event_version_from_unique UNIQUE (session_id, session_version_from),
  CONSTRAINT sandbox_session_event_version_to_unique UNIQUE (session_id, session_version_to),
  CONSTRAINT sandbox_session_event_candidate_epoch_matches CHECK (
    candidate_session_epoch_from = session_epoch_from
    AND candidate_session_epoch_to = session_epoch_to
  )
);

CREATE INDEX sandbox_session_transition_actor_idx
  ON sandbox_session_transition_events (actor_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_sandbox_session_transition_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_sessions%ROWTYPE;
  candidate candidate_workspaces%ROWTYPE;
  lifecycle_valid boolean := false;
  candidate_unchanged boolean;
BEGIN
  SELECT * INTO parent
  FROM sandbox_sessions
  WHERE id = NEW.session_id
  FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'SandboxSession does not exist'
      USING ERRCODE = '23503';
  END IF;

  SELECT * INTO candidate
  FROM candidate_workspaces
  WHERE id = parent.candidate_id
  FOR SHARE;

  IF NOT FOUND OR candidate.status <> 'active' THEN
    RAISE EXCEPTION 'SandboxSession transitions require the exact active Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.sequence <> parent.version
     OR NEW.session_version_from <> parent.version
     OR NEW.session_version_to <> parent.version + 1
     OR NEW.state_from <> parent.state
     OR NEW.session_epoch_from <> parent.session_epoch
     OR NEW.candidate_version_from <> parent.candidate_version
     OR NEW.candidate_journal_sequence_from <> parent.candidate_journal_sequence
     OR NEW.candidate_session_epoch_from <> parent.candidate_session_epoch
     OR NEW.candidate_writer_lease_epoch_from <> parent.candidate_writer_lease_epoch
     OR ROW(
       NEW.candidate_tree_store_from, NEW.candidate_tree_owner_id_from,
       NEW.candidate_tree_ref_from, NEW.candidate_tree_content_hash_from,
       NEW.candidate_tree_hash_from,
       NEW.candidate_dirty_from, NEW.candidate_conflicted_from,
       NEW.candidate_stale_from, NEW.candidate_rebase_required_from,
       NEW.latest_checkpoint_id_from, NEW.failure_reason_from
     ) IS DISTINCT FROM ROW(
       parent.candidate_tree_store, parent.candidate_tree_owner_id,
       parent.candidate_tree_ref, parent.candidate_tree_content_hash,
       parent.candidate_tree_hash,
       parent.candidate_dirty, parent.candidate_conflicted,
       parent.candidate_stale, parent.candidate_rebase_required,
       parent.latest_checkpoint_id, parent.failure_reason
     ) THEN
    RAISE EXCEPTION 'SandboxSession event failed state, epoch, or CAS version precondition'
      USING ERRCODE = '40001';
  END IF;

  candidate_unchanged := ROW(
    NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
    NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
    NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
    NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
    NEW.candidate_tree_hash_to,
    NEW.candidate_dirty_to, NEW.candidate_conflicted_to,
    NEW.candidate_stale_to, NEW.candidate_rebase_required_to
  ) IS NOT DISTINCT FROM ROW(
    parent.candidate_version, parent.candidate_journal_sequence,
    parent.candidate_session_epoch, parent.candidate_writer_lease_epoch,
    parent.candidate_tree_store, parent.candidate_tree_owner_id,
    parent.candidate_tree_ref, parent.candidate_tree_content_hash,
    parent.candidate_tree_hash,
    parent.candidate_dirty, parent.candidate_conflicted,
    parent.candidate_stale, parent.candidate_rebase_required
  );

  IF NEW.event_kind = 'candidate.synced' THEN
    lifecycle_valid := parent.state IN ('ready', 'resuming')
      AND NEW.state_to = parent.state
      AND NEW.session_epoch_to = parent.session_epoch
      AND candidate.version > parent.candidate_version;
  ELSIF NEW.event_kind = 'checkpoint.attached' THEN
    lifecycle_valid := parent.state IN ('ready', 'resuming')
      AND NEW.state_to = parent.state
      AND NEW.session_epoch_to = parent.session_epoch
      AND candidate_unchanged
      AND NEW.latest_checkpoint_id_to IS DISTINCT FROM parent.latest_checkpoint_id;
  ELSIF NEW.event_kind = 'lifecycle.started' THEN
    lifecycle_valid := parent.state = 'provisioning' AND NEW.state_to = 'starting';
  ELSIF NEW.event_kind = 'lifecycle.ready' THEN
    lifecycle_valid := parent.state IN ('starting', 'resuming') AND NEW.state_to = 'ready';
  ELSIF NEW.event_kind = 'lifecycle.suspend_requested' THEN
    lifecycle_valid := parent.state = 'ready' AND NEW.state_to = 'suspending';
  ELSIF NEW.event_kind = 'lifecycle.suspended' THEN
    lifecycle_valid := parent.state = 'suspending' AND NEW.state_to = 'suspended';
  ELSIF NEW.event_kind = 'lifecycle.resume_requested' THEN
    lifecycle_valid := parent.state = 'suspended'
      AND NEW.state_to = 'resuming'
      AND NEW.session_epoch_to = parent.session_epoch + 1
      AND NEW.candidate_session_epoch_to = parent.candidate_session_epoch + 1
      AND candidate.session_epoch = NEW.candidate_session_epoch_to
      AND candidate.version = parent.candidate_version + 1;
  ELSIF NEW.event_kind = 'lifecycle.terminate_requested' THEN
    lifecycle_valid := parent.state IN ('ready', 'suspended', 'failed') AND NEW.state_to = 'terminating';
  ELSIF NEW.event_kind = 'lifecycle.cancelled' THEN
    lifecycle_valid := parent.state IN ('provisioning', 'starting', 'resuming')
      AND NEW.state_to = 'terminating' AND NEW.reason = 'cancel';
  ELSIF NEW.event_kind = 'lifecycle.terminated' THEN
    lifecycle_valid := parent.state = 'terminating' AND NEW.state_to = 'terminated';
  ELSIF NEW.event_kind = 'lifecycle.failed' THEN
    lifecycle_valid := parent.state IN (
      'provisioning', 'starting', 'ready', 'suspending', 'resuming', 'terminating'
    ) AND NEW.state_to = 'failed';
  END IF;

  IF NOT lifecycle_valid THEN
    RAISE EXCEPTION 'invalid SandboxSession lifecycle transition'
      USING ERRCODE = '55000';
  END IF;

  IF NEW.event_kind <> 'candidate.synced'
     AND NEW.event_kind <> 'lifecycle.resume_requested'
     AND NOT candidate_unchanged THEN
    RAISE EXCEPTION 'SandboxSession lifecycle transition cannot smuggle a Candidate change'
      USING ERRCODE = '40001';
  END IF;

  IF ROW(
    candidate.version, candidate.journal_sequence,
    candidate.session_epoch, candidate.writer_lease_epoch,
    candidate.current_tree_store, candidate.current_tree_owner_id,
    candidate.current_tree_ref, candidate.current_tree_content_hash,
    candidate.current_tree_hash,
    candidate.dirty, candidate.conflicted, candidate.stale, candidate.rebase_required
  ) IS DISTINCT FROM ROW(
    NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
    NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
    NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
    NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
    NEW.candidate_tree_hash_to,
    NEW.candidate_dirty_to, NEW.candidate_conflicted_to,
    NEW.candidate_stale_to, NEW.candidate_rebase_required_to
  ) OR NEW.candidate_session_epoch_to <> NEW.session_epoch_to THEN
    RAISE EXCEPTION 'SandboxSession event does not project the exact live Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.latest_checkpoint_id_to IS DISTINCT FROM parent.latest_checkpoint_id
     AND NOT sandbox_checkpoint_is_exact(
       NEW.latest_checkpoint_id_to, parent.candidate_id, parent.project_id,
       NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
       NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
       NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
       NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
       NEW.candidate_tree_hash_to
     ) THEN
    RAISE EXCEPTION 'SandboxSession event checkpoint is not exact for the current Candidate'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.event_kind IN (
       'lifecycle.suspend_requested',
       'lifecycle.terminate_requested',
       'lifecycle.cancelled'
     )
     AND candidate.dirty
     AND NOT sandbox_checkpoint_is_exact(
       NEW.latest_checkpoint_id_to, parent.candidate_id, parent.project_id,
       NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
       NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
       NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
       NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
       NEW.candidate_tree_hash_to
     ) THEN
    RAISE EXCEPTION 'dirty Candidate requires an exact current CandidateSnapshot before suspend, terminate, or cancel'
      USING ERRCODE = '23514';
  END IF;

  IF NEW.event_kind = 'lifecycle.ready'
     AND parent.state = 'resuming'
     AND candidate.dirty
     AND NOT sandbox_checkpoint_is_exact(
       NEW.latest_checkpoint_id_to, parent.candidate_id, parent.project_id,
       NEW.candidate_version_to, NEW.candidate_journal_sequence_to,
       NEW.candidate_session_epoch_to, NEW.candidate_writer_lease_epoch_to,
       NEW.candidate_tree_store_to, NEW.candidate_tree_owner_id_to,
       NEW.candidate_tree_ref_to, NEW.candidate_tree_content_hash_to,
       NEW.candidate_tree_hash_to
     ) THEN
    RAISE EXCEPTION 'dirty resumed Candidate requires an exact current CandidateSnapshot before ready'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.event_kind = 'lifecycle.failed' THEN
    IF NEW.failure_reason_to IS DISTINCT FROM NEW.reason THEN
      RAISE EXCEPTION 'failed SandboxSession must persist its exact failure reason'
        USING ERRCODE = '23514';
    END IF;
  ELSIF NEW.failure_reason_to IS DISTINCT FROM parent.failure_reason THEN
    RAISE EXCEPTION 'SandboxSession failure reason may change only on failure'
      USING ERRCODE = '55000';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_session_transition_event_guard
BEFORE INSERT ON sandbox_session_transition_events
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_transition_event();

CREATE OR REPLACE FUNCTION advance_sandbox_session_from_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE sandbox_sessions
  SET state = NEW.state_to,
      version = NEW.session_version_to,
      session_epoch = NEW.session_epoch_to,
      candidate_version = NEW.candidate_version_to,
      candidate_journal_sequence = NEW.candidate_journal_sequence_to,
      candidate_session_epoch = NEW.candidate_session_epoch_to,
      candidate_writer_lease_epoch = NEW.candidate_writer_lease_epoch_to,
      candidate_tree_store = NEW.candidate_tree_store_to,
      candidate_tree_owner_id = NEW.candidate_tree_owner_id_to,
      candidate_tree_ref = NEW.candidate_tree_ref_to,
      candidate_tree_content_hash = NEW.candidate_tree_content_hash_to,
      candidate_tree_hash = NEW.candidate_tree_hash_to,
      candidate_dirty = NEW.candidate_dirty_to,
      candidate_conflicted = NEW.candidate_conflicted_to,
      candidate_stale = NEW.candidate_stale_to,
      candidate_rebase_required = NEW.candidate_rebase_required_to,
      latest_checkpoint_id = NEW.latest_checkpoint_id_to,
      failure_reason = NEW.failure_reason_to,
      idle_deadline = LEAST(
        expires_at,
        NEW.created_at + make_interval(secs => idle_hibernate_seconds)
      ),
      updated_at = NEW.created_at
  WHERE id = NEW.session_id
    AND version = NEW.session_version_from
    AND state = NEW.state_from
    AND session_epoch = NEW.session_epoch_from;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'SandboxSession event CAS update lost its writer fence'
      USING ERRCODE = '40001';
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER sandbox_session_transition_advance
AFTER INSERT ON sandbox_session_transition_events
FOR EACH ROW EXECUTE FUNCTION advance_sandbox_session_from_event();

CREATE OR REPLACE FUNCTION prevent_sandbox_session_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'SandboxSession history cannot be deleted; terminate it instead'
      USING ERRCODE = '55000';
  END IF;

  IF ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.actor_id,
    NEW.candidate_id, NEW.repository_snapshot_id,
    NEW.build_manifest_id, NEW.build_manifest_hash,
    NEW.build_contract_id, NEW.build_contract_hash,
    NEW.full_stack_template_id, NEW.full_stack_template_hash,
    NEW.base_workspace_artifact_id, NEW.base_workspace_revision_id,
    NEW.base_workspace_content_hash, NEW.runner_image_digest,
    NEW.cpu_millis, NEW.memory_bytes, NEW.workspace_bytes,
    NEW.pid_limit, NEW.preview_port_limit,
    NEW.idle_hibernate_seconds, NEW.max_runtime_seconds,
    NEW.expires_at, NEW.creation_transaction_id, NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.actor_id,
    OLD.candidate_id, OLD.repository_snapshot_id,
    OLD.build_manifest_id, OLD.build_manifest_hash,
    OLD.build_contract_id, OLD.build_contract_hash,
    OLD.full_stack_template_id, OLD.full_stack_template_hash,
    OLD.base_workspace_artifact_id, OLD.base_workspace_revision_id,
    OLD.base_workspace_content_hash, OLD.runner_image_digest,
    OLD.cpu_millis, OLD.memory_bytes, OLD.workspace_bytes,
    OLD.pid_limit, OLD.preview_port_limit,
    OLD.idle_hibernate_seconds, OLD.max_runtime_seconds,
    OLD.expires_at, OLD.creation_transaction_id, OLD.created_at
  ) THEN
    RAISE EXCEPTION 'SandboxSession exact lineage, quota, TTL, and runner identity are immutable'
      USING ERRCODE = '55000';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM sandbox_session_transition_events AS event
    WHERE event.session_id = OLD.id
      AND event.session_version_from = OLD.version
      AND event.session_version_to = NEW.version
      AND event.state_from = OLD.state
      AND event.state_to = NEW.state
      AND event.session_epoch_from = OLD.session_epoch
      AND event.session_epoch_to = NEW.session_epoch
      AND event.candidate_version_to = NEW.candidate_version
      AND event.candidate_journal_sequence_to = NEW.candidate_journal_sequence
      AND event.candidate_session_epoch_to = NEW.candidate_session_epoch
      AND event.candidate_writer_lease_epoch_to = NEW.candidate_writer_lease_epoch
      AND event.candidate_tree_store_to = NEW.candidate_tree_store
      AND event.candidate_tree_owner_id_to = NEW.candidate_tree_owner_id
      AND event.candidate_tree_ref_to = NEW.candidate_tree_ref
      AND event.candidate_tree_content_hash_to = NEW.candidate_tree_content_hash
      AND event.candidate_tree_hash_to = NEW.candidate_tree_hash
      AND event.candidate_dirty_to = NEW.candidate_dirty
      AND event.candidate_conflicted_to = NEW.candidate_conflicted
      AND event.candidate_stale_to = NEW.candidate_stale
      AND event.candidate_rebase_required_to = NEW.candidate_rebase_required
      AND event.latest_checkpoint_id_to IS NOT DISTINCT FROM NEW.latest_checkpoint_id
      AND event.failure_reason_to IS NOT DISTINCT FROM NEW.failure_reason
      AND NEW.idle_deadline = LEAST(
        OLD.expires_at,
        event.created_at + make_interval(secs => OLD.idle_hibernate_seconds)
      )
      AND NEW.updated_at = event.created_at
      AND event.creation_transaction_id = txid_current()
  ) THEN
    RAISE EXCEPTION 'SandboxSession can change only through an append-only CAS transition event'
      USING ERRCODE = '55000';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_session_mutation_guard
BEFORE UPDATE OR DELETE ON sandbox_sessions
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_session_mutation();

CREATE OR REPLACE FUNCTION prevent_sandbox_session_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'SandboxSession transition events are append-only'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER sandbox_session_transition_event_immutable
BEFORE UPDATE OR DELETE ON sandbox_session_transition_events
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_session_event_mutation();

CREATE OR REPLACE FUNCTION validate_sandbox_session_event_chain()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_session_id uuid;
  parent sandbox_sessions%ROWTYPE;
  event_count bigint;
  minimum_sequence bigint;
  maximum_sequence bigint;
  tail sandbox_session_transition_events%ROWTYPE;
BEGIN
  IF TG_TABLE_NAME = 'sandbox_sessions' THEN
    target_session_id := NEW.id;
  ELSE
    target_session_id := NEW.session_id;
  END IF;

  SELECT * INTO parent FROM sandbox_sessions WHERE id = target_session_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;

  SELECT count(*), min(sequence), max(sequence)
    INTO event_count, minimum_sequence, maximum_sequence
  FROM sandbox_session_transition_events
  WHERE session_id = target_session_id;

  IF event_count <> parent.version - 1
     OR (event_count > 0 AND (minimum_sequence <> 1 OR maximum_sequence <> event_count)) THEN
    RAISE EXCEPTION 'SandboxSession event sequence is not contiguous or does not match the parent CAS version'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM (
      SELECT sequence,
             session_version_from, state_from, session_epoch_from,
             candidate_version_from, candidate_journal_sequence_from,
             candidate_session_epoch_from, candidate_writer_lease_epoch_from,
             candidate_tree_store_from, candidate_tree_owner_id_from,
             candidate_tree_ref_from, candidate_tree_content_hash_from,
             candidate_tree_hash_from,
             candidate_dirty_from, candidate_conflicted_from,
             candidate_stale_from, candidate_rebase_required_from,
             latest_checkpoint_id_from, failure_reason_from,
             lag(session_version_to) OVER (ORDER BY sequence) AS previous_version,
             lag(state_to) OVER (ORDER BY sequence) AS previous_state,
             lag(session_epoch_to) OVER (ORDER BY sequence) AS previous_epoch,
             lag(candidate_version_to) OVER (ORDER BY sequence) AS previous_candidate_version,
             lag(candidate_journal_sequence_to) OVER (ORDER BY sequence) AS previous_journal,
             lag(candidate_session_epoch_to) OVER (ORDER BY sequence) AS previous_candidate_epoch,
             lag(candidate_writer_lease_epoch_to) OVER (ORDER BY sequence) AS previous_writer_epoch,
             lag(candidate_tree_store_to) OVER (ORDER BY sequence) AS previous_tree_store,
             lag(candidate_tree_owner_id_to) OVER (ORDER BY sequence) AS previous_tree_owner,
             lag(candidate_tree_ref_to) OVER (ORDER BY sequence) AS previous_tree_ref,
             lag(candidate_tree_content_hash_to) OVER (ORDER BY sequence) AS previous_tree_content_hash,
             lag(candidate_tree_hash_to) OVER (ORDER BY sequence) AS previous_tree_hash,
             lag(candidate_dirty_to) OVER (ORDER BY sequence) AS previous_dirty,
             lag(candidate_conflicted_to) OVER (ORDER BY sequence) AS previous_conflicted,
             lag(candidate_stale_to) OVER (ORDER BY sequence) AS previous_stale,
             lag(candidate_rebase_required_to) OVER (ORDER BY sequence) AS previous_rebase,
             lag(latest_checkpoint_id_to) OVER (ORDER BY sequence) AS previous_checkpoint,
             lag(failure_reason_to) OVER (ORDER BY sequence) AS previous_failure
      FROM sandbox_session_transition_events
      WHERE session_id = target_session_id
    ) AS chain
    WHERE sequence > 1 AND ROW(
      session_version_from, state_from, session_epoch_from,
      candidate_version_from, candidate_journal_sequence_from,
      candidate_session_epoch_from, candidate_writer_lease_epoch_from,
      candidate_tree_store_from, candidate_tree_owner_id_from,
      candidate_tree_ref_from, candidate_tree_content_hash_from,
      candidate_tree_hash_from,
      candidate_dirty_from, candidate_conflicted_from,
      candidate_stale_from, candidate_rebase_required_from,
      latest_checkpoint_id_from, failure_reason_from
    ) IS DISTINCT FROM ROW(
      previous_version, previous_state, previous_epoch,
      previous_candidate_version, previous_journal,
      previous_candidate_epoch, previous_writer_epoch,
      previous_tree_store, previous_tree_owner,
      previous_tree_ref, previous_tree_content_hash, previous_tree_hash,
      previous_dirty, previous_conflicted, previous_stale, previous_rebase,
      previous_checkpoint, previous_failure
    )
  ) THEN
    RAISE EXCEPTION 'SandboxSession append-only event chain is inconsistent'
      USING ERRCODE = '23514';
  END IF;

  IF event_count = 0 THEN
    IF parent.version <> 1 OR parent.state <> 'provisioning' THEN
      RAISE EXCEPTION 'SandboxSession without events must remain its initial projection'
        USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
  END IF;

  SELECT * INTO tail
  FROM sandbox_session_transition_events
  WHERE session_id = target_session_id
  ORDER BY sequence DESC
  LIMIT 1;

  IF ROW(
    tail.session_version_to, tail.state_to, tail.session_epoch_to,
    tail.candidate_version_to, tail.candidate_journal_sequence_to,
    tail.candidate_session_epoch_to, tail.candidate_writer_lease_epoch_to,
    tail.candidate_tree_store_to, tail.candidate_tree_owner_id_to,
    tail.candidate_tree_ref_to, tail.candidate_tree_content_hash_to,
    tail.candidate_tree_hash_to,
    tail.candidate_dirty_to, tail.candidate_conflicted_to,
    tail.candidate_stale_to, tail.candidate_rebase_required_to,
    tail.latest_checkpoint_id_to, tail.failure_reason_to
  ) IS DISTINCT FROM ROW(
    parent.version, parent.state, parent.session_epoch,
    parent.candidate_version, parent.candidate_journal_sequence,
    parent.candidate_session_epoch, parent.candidate_writer_lease_epoch,
    parent.candidate_tree_store, parent.candidate_tree_owner_id,
    parent.candidate_tree_ref, parent.candidate_tree_content_hash,
    parent.candidate_tree_hash,
    parent.candidate_dirty, parent.candidate_conflicted,
    parent.candidate_stale, parent.candidate_rebase_required,
    parent.latest_checkpoint_id, parent.failure_reason
  ) THEN
    RAISE EXCEPTION 'SandboxSession parent projection differs from its append-only event tail'
      USING ERRCODE = '23514';
  END IF;

  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER sandbox_session_parent_event_chain_guard
AFTER INSERT OR UPDATE ON sandbox_sessions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_event_chain();

CREATE CONSTRAINT TRIGGER sandbox_session_event_chain_guard
AFTER INSERT ON sandbox_session_transition_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_session_event_chain();

CREATE OR REPLACE FUNCTION sync_sandbox_session_candidate(
  target_session_id uuid,
  expected_version bigint,
  expected_session_epoch bigint,
  target_actor_id uuid
)
RETURNS TABLE (
  session_version bigint,
  session_epoch bigint,
  candidate_version bigint,
  candidate_tree_hash text
)
LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO sandbox_session_transition_events (
    session_id, sequence, session_version_from, session_version_to,
    state_from, state_to, session_epoch_from, session_epoch_to,
    event_kind, actor_id, reason,
    candidate_version_from, candidate_version_to,
    candidate_journal_sequence_from, candidate_journal_sequence_to,
    candidate_session_epoch_from, candidate_session_epoch_to,
    candidate_writer_lease_epoch_from, candidate_writer_lease_epoch_to,
    candidate_tree_store_from, candidate_tree_store_to,
    candidate_tree_owner_id_from, candidate_tree_owner_id_to,
    candidate_tree_ref_from, candidate_tree_ref_to,
    candidate_tree_content_hash_from, candidate_tree_content_hash_to,
    candidate_tree_hash_from, candidate_tree_hash_to,
    candidate_dirty_from, candidate_dirty_to,
    candidate_conflicted_from, candidate_conflicted_to,
    candidate_stale_from, candidate_stale_to,
    candidate_rebase_required_from, candidate_rebase_required_to,
    latest_checkpoint_id_from, latest_checkpoint_id_to,
    failure_reason_from, failure_reason_to
  )
  SELECT session.id, session.version, session.version, session.version + 1,
         session.state, session.state, session.session_epoch, session.session_epoch,
         'candidate.synced', target_actor_id, 'synchronize exact Candidate projection',
         session.candidate_version, candidate.version,
         session.candidate_journal_sequence, candidate.journal_sequence,
         session.candidate_session_epoch, candidate.session_epoch,
         session.candidate_writer_lease_epoch, candidate.writer_lease_epoch,
         session.candidate_tree_store, candidate.current_tree_store,
         session.candidate_tree_owner_id, candidate.current_tree_owner_id,
         session.candidate_tree_ref, candidate.current_tree_ref,
         session.candidate_tree_content_hash, candidate.current_tree_content_hash,
         session.candidate_tree_hash, candidate.current_tree_hash,
         session.candidate_dirty, candidate.dirty,
         session.candidate_conflicted, candidate.conflicted,
         session.candidate_stale, candidate.stale,
         session.candidate_rebase_required, candidate.rebase_required,
         session.latest_checkpoint_id, session.latest_checkpoint_id,
         session.failure_reason, session.failure_reason
  FROM sandbox_sessions AS session
  JOIN candidate_workspaces AS candidate ON candidate.id = session.candidate_id
  WHERE session.id = target_session_id
    AND session.version = expected_version
    AND session.session_epoch = expected_session_epoch
    AND session.state IN ('ready', 'resuming')
    AND candidate.status = 'active'
    AND candidate.session_epoch = session.session_epoch
    AND candidate.version > session.candidate_version;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'SandboxSession Candidate sync failed state, epoch, or CAS precondition'
      USING ERRCODE = '40001';
  END IF;

  RETURN QUERY
  SELECT session.version, session.session_epoch, session.candidate_version, session.candidate_tree_hash
  FROM sandbox_sessions AS session
  WHERE session.id = target_session_id;
END;
$$;

CREATE OR REPLACE FUNCTION attach_sandbox_session_checkpoint(
  target_session_id uuid,
  expected_version bigint,
  expected_session_epoch bigint,
  target_actor_id uuid,
  target_checkpoint_id uuid
)
RETURNS bigint
LANGUAGE plpgsql
AS $$
DECLARE
  next_version bigint;
BEGIN
  INSERT INTO sandbox_session_transition_events (
    session_id, sequence, session_version_from, session_version_to,
    state_from, state_to, session_epoch_from, session_epoch_to,
    event_kind, actor_id, reason,
    candidate_version_from, candidate_version_to,
    candidate_journal_sequence_from, candidate_journal_sequence_to,
    candidate_session_epoch_from, candidate_session_epoch_to,
    candidate_writer_lease_epoch_from, candidate_writer_lease_epoch_to,
    candidate_tree_store_from, candidate_tree_store_to,
    candidate_tree_owner_id_from, candidate_tree_owner_id_to,
    candidate_tree_ref_from, candidate_tree_ref_to,
    candidate_tree_content_hash_from, candidate_tree_content_hash_to,
    candidate_tree_hash_from, candidate_tree_hash_to,
    candidate_dirty_from, candidate_dirty_to,
    candidate_conflicted_from, candidate_conflicted_to,
    candidate_stale_from, candidate_stale_to,
    candidate_rebase_required_from, candidate_rebase_required_to,
    latest_checkpoint_id_from, latest_checkpoint_id_to,
    failure_reason_from, failure_reason_to
  )
  SELECT session.id, session.version, session.version, session.version + 1,
         session.state, session.state, session.session_epoch, session.session_epoch,
         'checkpoint.attached', target_actor_id, 'attach exact current Candidate checkpoint',
         session.candidate_version, session.candidate_version,
         session.candidate_journal_sequence, session.candidate_journal_sequence,
         session.candidate_session_epoch, session.candidate_session_epoch,
         session.candidate_writer_lease_epoch, session.candidate_writer_lease_epoch,
         session.candidate_tree_store, session.candidate_tree_store,
         session.candidate_tree_owner_id, session.candidate_tree_owner_id,
         session.candidate_tree_ref, session.candidate_tree_ref,
         session.candidate_tree_content_hash, session.candidate_tree_content_hash,
         session.candidate_tree_hash, session.candidate_tree_hash,
         session.candidate_dirty, session.candidate_dirty,
         session.candidate_conflicted, session.candidate_conflicted,
         session.candidate_stale, session.candidate_stale,
         session.candidate_rebase_required, session.candidate_rebase_required,
         session.latest_checkpoint_id, target_checkpoint_id,
         session.failure_reason, session.failure_reason
  FROM sandbox_sessions AS session
  WHERE session.id = target_session_id
    AND session.version = expected_version
    AND session.session_epoch = expected_session_epoch
    AND session.state IN ('ready', 'resuming');

  IF NOT FOUND THEN
    RAISE EXCEPTION 'SandboxSession checkpoint attach failed state, epoch, or CAS precondition'
      USING ERRCODE = '40001';
  END IF;

  SELECT version INTO next_version FROM sandbox_sessions WHERE id = target_session_id;
  RETURN next_version;
END;
$$;

CREATE OR REPLACE FUNCTION transition_sandbox_session(
  target_session_id uuid,
  expected_version bigint,
  expected_session_epoch bigint,
  target_state text,
  target_actor_id uuid,
  transition_reason text,
  target_checkpoint_id uuid
)
RETURNS TABLE (
  session_version bigint,
  session_state text,
  session_epoch bigint,
  candidate_version bigint
)
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_sessions%ROWTYPE;
  event_kind text;
  next_epoch bigint;
  next_failure_reason text;
BEGIN
  SELECT * INTO parent
  FROM sandbox_sessions
  WHERE id = target_session_id
  FOR UPDATE;

  IF NOT FOUND OR parent.version <> expected_version OR parent.session_epoch <> expected_session_epoch THEN
    RAISE EXCEPTION 'SandboxSession transition CAS or epoch precondition failed'
      USING ERRCODE = '40001';
  END IF;

  transition_reason := btrim(transition_reason);
  IF transition_reason IS NULL OR length(transition_reason) NOT BETWEEN 1 AND 2000 THEN
    RAISE EXCEPTION 'SandboxSession transition reason is required and bounded'
      USING ERRCODE = '22023';
  END IF;

  IF parent.state = 'provisioning' AND target_state = 'starting' THEN
    event_kind := 'lifecycle.started';
  ELSIF parent.state IN ('starting', 'resuming') AND target_state = 'ready' THEN
    event_kind := 'lifecycle.ready';
  ELSIF parent.state = 'ready' AND target_state = 'suspending' THEN
    event_kind := 'lifecycle.suspend_requested';
  ELSIF parent.state = 'suspending' AND target_state = 'suspended' THEN
    event_kind := 'lifecycle.suspended';
  ELSIF parent.state = 'suspended' AND target_state = 'resuming' THEN
    event_kind := 'lifecycle.resume_requested';
  ELSIF parent.state IN ('ready', 'suspended', 'failed') AND target_state = 'terminating' THEN
    event_kind := 'lifecycle.terminate_requested';
  ELSIF parent.state IN ('provisioning', 'starting', 'resuming')
        AND target_state = 'terminating' AND transition_reason = 'cancel' THEN
    event_kind := 'lifecycle.cancelled';
  ELSIF parent.state = 'terminating' AND target_state = 'terminated' THEN
    event_kind := 'lifecycle.terminated';
  ELSIF parent.state IN ('provisioning', 'starting', 'ready', 'suspending', 'resuming', 'terminating')
        AND target_state = 'failed' THEN
    event_kind := 'lifecycle.failed';
  ELSE
    RAISE EXCEPTION 'invalid SandboxSession lifecycle transition'
      USING ERRCODE = '55000';
  END IF;

  IF event_kind = 'lifecycle.resume_requested' THEN
    PERFORM * FROM rotate_candidate_workspace_session(
      parent.candidate_id, parent.candidate_version, parent.session_epoch, target_actor_id
    );
    next_epoch := parent.session_epoch + 1;
  ELSE
    next_epoch := parent.session_epoch;
  END IF;

  IF event_kind = 'lifecycle.failed' THEN
    next_failure_reason := transition_reason;
  ELSE
    next_failure_reason := parent.failure_reason;
  END IF;

  INSERT INTO sandbox_session_transition_events (
    session_id, sequence, session_version_from, session_version_to,
    state_from, state_to, session_epoch_from, session_epoch_to,
    event_kind, actor_id, reason,
    candidate_version_from, candidate_version_to,
    candidate_journal_sequence_from, candidate_journal_sequence_to,
    candidate_session_epoch_from, candidate_session_epoch_to,
    candidate_writer_lease_epoch_from, candidate_writer_lease_epoch_to,
    candidate_tree_store_from, candidate_tree_store_to,
    candidate_tree_owner_id_from, candidate_tree_owner_id_to,
    candidate_tree_ref_from, candidate_tree_ref_to,
    candidate_tree_content_hash_from, candidate_tree_content_hash_to,
    candidate_tree_hash_from, candidate_tree_hash_to,
    candidate_dirty_from, candidate_dirty_to,
    candidate_conflicted_from, candidate_conflicted_to,
    candidate_stale_from, candidate_stale_to,
    candidate_rebase_required_from, candidate_rebase_required_to,
    latest_checkpoint_id_from, latest_checkpoint_id_to,
    failure_reason_from, failure_reason_to
  )
  SELECT parent.id, parent.version, parent.version, parent.version + 1,
         parent.state, target_state, parent.session_epoch, next_epoch,
         event_kind, target_actor_id, transition_reason,
         parent.candidate_version, candidate.version,
         parent.candidate_journal_sequence, candidate.journal_sequence,
         parent.candidate_session_epoch, candidate.session_epoch,
         parent.candidate_writer_lease_epoch, candidate.writer_lease_epoch,
         parent.candidate_tree_store, candidate.current_tree_store,
         parent.candidate_tree_owner_id, candidate.current_tree_owner_id,
         parent.candidate_tree_ref, candidate.current_tree_ref,
         parent.candidate_tree_content_hash, candidate.current_tree_content_hash,
         parent.candidate_tree_hash, candidate.current_tree_hash,
         parent.candidate_dirty, candidate.dirty,
         parent.candidate_conflicted, candidate.conflicted,
         parent.candidate_stale, candidate.stale,
         parent.candidate_rebase_required, candidate.rebase_required,
         parent.latest_checkpoint_id, COALESCE(target_checkpoint_id, parent.latest_checkpoint_id),
         parent.failure_reason, next_failure_reason
  FROM candidate_workspaces AS candidate
  WHERE candidate.id = parent.candidate_id;

  RETURN QUERY
  SELECT session.version, session.state, session.session_epoch, session.candidate_version
  FROM sandbox_sessions AS session
  WHERE session.id = target_session_id;
END;
$$;
