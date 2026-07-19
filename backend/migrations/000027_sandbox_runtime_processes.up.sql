-- Durable, epoch-fenced process projections for the interactive sandbox.
-- Clients select an admitted service/command pair; argv is resolved from the
-- exact TemplateRelease and is persisted here for restart-safe reconciliation.

CREATE OR REPLACE FUNCTION sandbox_process_argv_is_valid(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(value) <> 'array' THEN false
    WHEN jsonb_array_length(value) NOT BETWEEN 1 AND 64 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(value) AS argument(item)
      WHERE jsonb_typeof(argument.item) <> 'string'
         OR argument.item #>> '{}' = ''
         OR argument.item #>> '{}' <> btrim(argument.item #>> '{}')
         OR length(argument.item #>> '{}') > 4096
         OR argument.item #>> '{}' ~ '[[:cntrl:]]'
    )
  END
$$;

CREATE TABLE sandbox_runtime_processes (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'sandbox-process/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  session_version_at_creation bigint NOT NULL CHECK (session_version_at_creation > 0),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  service_id text NOT NULL,
  command_id text NOT NULL CHECK (
    command_id = btrim(command_id)
    AND command_id ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'
    AND length(command_id) <= 80
  ),
  template_release_id uuid NOT NULL,
  template_release_content_hash text NOT NULL CHECK (
    template_release_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  working_directory text NOT NULL CHECK (
    working_directory = btrim(working_directory)
    AND length(working_directory) BETWEEN 1 AND 512
    AND left(working_directory, 1) <> '/'
    AND position(E'\\' in working_directory) = 0
    AND working_directory !~ '(^|/)\.\.?(/|$)'
    AND working_directory !~ '[[:cntrl:]]'
  ),
  argv jsonb NOT NULL CHECK (sandbox_process_argv_is_valid(argv)),
  log_limit_bytes bigint NOT NULL CHECK (log_limit_bytes BETWEEN 1 AND 67108864),
  state text NOT NULL CHECK (state IN ('starting', 'running', 'exited', 'failed', 'orphaned')),
  version bigint NOT NULL CHECK (version > 0),
  pid integer CHECK (pid IS NULL OR pid >= 2),
  exit_code integer,
  failure text CHECK (
    failure IS NULL OR (failure = btrim(failure) AND length(failure) BETWEEN 1 AND 1000)
  ),
  log_bytes bigint NOT NULL CHECK (log_bytes >= 0 AND log_bytes <= log_limit_bytes),
  log_truncated boolean NOT NULL,
  runtime_started_at timestamptz,
  finished_at timestamptz,
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT sandbox_runtime_process_service_fk
    FOREIGN KEY (session_id, service_id)
    REFERENCES sandbox_session_services(session_id, service_id) ON DELETE RESTRICT,
  CONSTRAINT sandbox_runtime_process_template_fk
    FOREIGN KEY (session_id, template_release_id, template_release_content_hash)
    REFERENCES sandbox_session_template_releases(
      session_id, template_release_id, template_release_content_hash
    ) ON DELETE RESTRICT,
  CONSTRAINT sandbox_runtime_process_time_order CHECK (
    updated_at >= created_at
    AND (runtime_started_at IS NULL OR runtime_started_at >= created_at)
    AND (finished_at IS NULL OR (runtime_started_at IS NOT NULL AND finished_at >= runtime_started_at))
  )
);

CREATE INDEX sandbox_runtime_process_session_idx
  ON sandbox_runtime_processes (session_id, session_epoch, created_at DESC, id DESC);

CREATE INDEX sandbox_runtime_process_active_idx
  ON sandbox_runtime_processes (session_id, session_epoch, state, updated_at DESC)
  WHERE state IN ('starting', 'running');

CREATE OR REPLACE FUNCTION validate_sandbox_runtime_process_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_sessions%ROWTYPE;
BEGIN
  SELECT * INTO parent
  FROM sandbox_sessions
  WHERE id = NEW.session_id
  FOR SHARE;

  IF NOT FOUND
     OR parent.project_id <> NEW.project_id
     OR parent.state <> 'ready'
     OR parent.session_epoch <> NEW.session_epoch
     OR parent.version <> NEW.session_version_at_creation THEN
    RAISE EXCEPTION 'Sandbox process requires the exact ready session version and epoch'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state <> 'starting'
     OR NEW.version <> 1
     OR NEW.pid IS NOT NULL
     OR NEW.exit_code IS NOT NULL
     OR NEW.failure IS NOT NULL
     OR NEW.runtime_started_at IS NOT NULL
     OR NEW.finished_at IS NOT NULL
     OR NEW.log_bytes <> 0
     OR NEW.log_truncated THEN
    RAISE EXCEPTION 'Sandbox process must start as an empty starting projection at version 1'
      USING ERRCODE = '23514';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM sandbox_session_services AS service
    JOIN sandbox_sessions AS bound_session
      ON bound_session.id = service.session_id
    JOIN full_stack_template_components AS component
      ON component.full_stack_template_id = bound_session.full_stack_template_id
     AND component.full_stack_content_hash = bound_session.full_stack_template_hash
     AND component.role = service.kind
     AND component.template_release_id = service.template_release_id
     AND component.template_release_content_hash = service.template_release_content_hash
    JOIN template_releases AS release
      ON release.id = component.template_release_id
     AND release.content_hash = component.template_release_content_hash
    WHERE service.session_id = NEW.session_id
      AND service.service_id = NEW.service_id
      AND service.template_release_id = NEW.template_release_id
      AND service.template_release_content_hash = NEW.template_release_content_hash
      AND service.profiles ? NEW.command_id
      AND release.manifest->'commands' ? NEW.command_id
      AND release.manifest->'commands'->NEW.command_id->'argv' = NEW.argv
      AND NEW.working_directory = CASE
        WHEN release.manifest->'commands'->NEW.command_id->>'workingDirectory' = '.'
          THEN component.mount_path
        ELSE component.mount_path || '/' ||
          (release.manifest->'commands'->NEW.command_id->>'workingDirectory')
      END
  ) THEN
    RAISE EXCEPTION 'Sandbox process command is not bound to the exact session service and TemplateRelease'
      USING ERRCODE = '23503';
  END IF;

  IF (
    SELECT count(*)
    FROM sandbox_runtime_processes AS process
    WHERE process.session_id = NEW.session_id
      AND process.session_epoch = NEW.session_epoch
      AND process.state IN ('starting', 'running')
  ) >= LEAST(parent.pid_limit, 64) THEN
    RAISE EXCEPTION 'Sandbox process concurrency exceeds the session bound'
      USING ERRCODE = '23514';
  END IF;

  NEW.creation_transaction_id := txid_current();
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_runtime_process_insert_guard
BEFORE INSERT ON sandbox_runtime_processes
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_runtime_process_insert();

CREATE TABLE sandbox_runtime_process_events (
  process_id uuid NOT NULL REFERENCES sandbox_runtime_processes(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  sequence bigint NOT NULL CHECK (sequence > 0),
  process_version_from bigint NOT NULL CHECK (process_version_from > 0),
  process_version_to bigint NOT NULL CHECK (process_version_to = process_version_from + 1),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  event_kind text NOT NULL CHECK (event_kind IN (
    'runtime.observed', 'start.failed', 'signal.sent', 'epoch.fenced'
  )),
  state_from text NOT NULL CHECK (state_from IN ('starting', 'running', 'exited', 'failed', 'orphaned')),
  state_to text NOT NULL CHECK (state_to IN ('starting', 'running', 'exited', 'failed', 'orphaned')),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  signal text CHECK (signal IS NULL OR signal IN ('INT', 'TERM', 'KILL', 'HUP')),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  pid_to integer CHECK (pid_to IS NULL OR pid_to >= 2),
  exit_code_to integer,
  failure_to text CHECK (
    failure_to IS NULL OR (failure_to = btrim(failure_to) AND length(failure_to) BETWEEN 1 AND 1000)
  ),
  log_bytes_to bigint NOT NULL CHECK (log_bytes_to >= 0),
  log_truncated_to boolean NOT NULL,
  runtime_started_at_to timestamptz,
  finished_at_to timestamptz,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  PRIMARY KEY (process_id, sequence),
  CONSTRAINT sandbox_runtime_process_event_version_from_unique UNIQUE (process_id, process_version_from),
  CONSTRAINT sandbox_runtime_process_event_version_to_unique UNIQUE (process_id, process_version_to)
);

CREATE INDEX sandbox_runtime_process_event_actor_idx
  ON sandbox_runtime_process_events (actor_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_sandbox_runtime_process_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_runtime_processes%ROWTYPE;
  transition_valid boolean := false;
BEGIN
  SELECT * INTO parent
  FROM sandbox_runtime_processes
  WHERE id = NEW.process_id
  FOR UPDATE;

  IF NOT FOUND
     OR NEW.sequence <> parent.version
     OR NEW.process_version_from <> parent.version
     OR NEW.process_version_to <> parent.version + 1
     OR NEW.session_epoch <> parent.session_epoch
     OR NEW.state_from <> parent.state THEN
    RAISE EXCEPTION 'Sandbox process event failed its process version or epoch fence'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.log_bytes_to > parent.log_limit_bytes
     OR (NEW.runtime_started_at_to IS NOT NULL AND NEW.runtime_started_at_to < parent.created_at)
     OR (NEW.finished_at_to IS NOT NULL AND (
       NEW.runtime_started_at_to IS NULL OR NEW.finished_at_to < NEW.runtime_started_at_to
     )) THEN
    RAISE EXCEPTION 'Sandbox process event contains invalid runtime status facts'
      USING ERRCODE = '23514';
  END IF;

  IF NEW.state_to = 'running' THEN
    transition_valid := NEW.pid_to IS NOT NULL
      AND NEW.exit_code_to IS NULL
      AND NEW.failure_to IS NULL
      AND NEW.runtime_started_at_to IS NOT NULL
      AND NEW.finished_at_to IS NULL;
  ELSIF NEW.state_to = 'exited' THEN
    transition_valid := NEW.pid_to IS NOT NULL
      AND NEW.exit_code_to IS NOT NULL
      AND NEW.failure_to IS NULL
      AND NEW.runtime_started_at_to IS NOT NULL
      AND NEW.finished_at_to IS NOT NULL;
  ELSIF NEW.state_to = 'failed' THEN
    transition_valid := NEW.exit_code_to IS NOT NULL
      AND NEW.failure_to IS NOT NULL
      AND NEW.runtime_started_at_to IS NOT NULL
      AND NEW.finished_at_to IS NOT NULL;
  ELSIF NEW.state_to = 'orphaned' THEN
    transition_valid := NEW.exit_code_to IS NULL
      AND NEW.failure_to IS NOT NULL
      AND NEW.finished_at_to IS NOT NULL;
  END IF;

  IF NOT transition_valid THEN
    RAISE EXCEPTION 'Sandbox process event target status is invalid'
      USING ERRCODE = '23514';
  END IF;

  IF NEW.event_kind = 'runtime.observed' THEN
    transition_valid := parent.state IN ('starting', 'running')
      AND NEW.state_to IN ('running', 'exited', 'failed')
      AND NEW.signal IS NULL;
  ELSIF NEW.event_kind = 'start.failed' THEN
    transition_valid := parent.state = 'starting'
      AND NEW.state_to = 'failed'
      AND NEW.signal IS NULL;
  ELSIF NEW.event_kind = 'signal.sent' THEN
    transition_valid := parent.state = 'running'
      AND NEW.state_to IN ('running', 'exited', 'failed')
      AND NEW.signal IS NOT NULL;
  ELSIF NEW.event_kind = 'epoch.fenced' THEN
    transition_valid := parent.state IN ('starting', 'running')
      AND NEW.state_to = 'orphaned'
      AND NEW.signal IS NULL;
  ELSE
    transition_valid := false;
  END IF;

  IF NOT transition_valid THEN
    RAISE EXCEPTION 'Invalid Sandbox process state transition'
      USING ERRCODE = '55000';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_runtime_process_event_guard
BEFORE INSERT ON sandbox_runtime_process_events
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_runtime_process_event();

CREATE OR REPLACE FUNCTION advance_sandbox_runtime_process_from_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE sandbox_runtime_processes
  SET state = NEW.state_to,
      version = NEW.process_version_to,
      pid = NEW.pid_to,
      exit_code = NEW.exit_code_to,
      failure = NEW.failure_to,
      log_bytes = NEW.log_bytes_to,
      log_truncated = NEW.log_truncated_to,
      runtime_started_at = NEW.runtime_started_at_to,
      finished_at = NEW.finished_at_to,
      updated_at = NEW.created_at
  WHERE id = NEW.process_id
    AND version = NEW.process_version_from
    AND session_epoch = NEW.session_epoch
    AND state = NEW.state_from;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Sandbox process event CAS update lost its writer fence'
      USING ERRCODE = '40001';
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER sandbox_runtime_process_event_advance
AFTER INSERT ON sandbox_runtime_process_events
FOR EACH ROW EXECUTE FUNCTION advance_sandbox_runtime_process_from_event();

CREATE OR REPLACE FUNCTION prevent_sandbox_runtime_process_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'Sandbox process history is immutable'
      USING ERRCODE = '55000';
  END IF;

  IF ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.session_id,
    NEW.session_epoch, NEW.session_version_at_creation, NEW.actor_id,
    NEW.service_id, NEW.command_id, NEW.template_release_id,
    NEW.template_release_content_hash, NEW.working_directory, NEW.argv,
    NEW.log_limit_bytes, NEW.creation_transaction_id, NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.session_id,
    OLD.session_epoch, OLD.session_version_at_creation, OLD.actor_id,
    OLD.service_id, OLD.command_id, OLD.template_release_id,
    OLD.template_release_content_hash, OLD.working_directory, OLD.argv,
    OLD.log_limit_bytes, OLD.creation_transaction_id, OLD.created_at
  ) THEN
    RAISE EXCEPTION 'Sandbox process identity and exact command are immutable'
      USING ERRCODE = '55000';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM sandbox_runtime_process_events AS event
    WHERE event.process_id = OLD.id
      AND event.process_version_from = OLD.version
      AND event.process_version_to = NEW.version
      AND event.session_epoch = OLD.session_epoch
      AND event.state_from = OLD.state
      AND event.state_to = NEW.state
      AND ROW(
        event.pid_to, event.exit_code_to, event.failure_to,
        event.log_bytes_to, event.log_truncated_to,
        event.runtime_started_at_to, event.finished_at_to,
        event.creation_transaction_id
      ) IS NOT DISTINCT FROM ROW(
        NEW.pid, NEW.exit_code, NEW.failure,
        NEW.log_bytes, NEW.log_truncated,
        NEW.runtime_started_at, NEW.finished_at,
        txid_current()
      )
  ) THEN
    RAISE EXCEPTION 'Sandbox process projection may advance only from its append-only event'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_runtime_process_mutation_guard
BEFORE UPDATE OR DELETE ON sandbox_runtime_processes
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_runtime_process_mutation();

CREATE OR REPLACE FUNCTION prevent_sandbox_runtime_process_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Sandbox process events are append-only'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER sandbox_runtime_process_event_immutable
BEFORE UPDATE OR DELETE ON sandbox_runtime_process_events
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_runtime_process_event_mutation();

CREATE OR REPLACE FUNCTION validate_sandbox_runtime_process_event_chain()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_id uuid;
  parent sandbox_runtime_processes%ROWTYPE;
  event_count bigint;
  min_sequence bigint;
  max_sequence bigint;
BEGIN
  target_id := COALESCE(
    (to_jsonb(NEW)->>'id')::uuid,
    (to_jsonb(NEW)->>'process_id')::uuid
  );
  SELECT * INTO parent FROM sandbox_runtime_processes WHERE id = target_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Sandbox process event chain has no projection'
      USING ERRCODE = '23503';
  END IF;

  SELECT count(*), min(sequence), max(sequence)
  INTO event_count, min_sequence, max_sequence
  FROM sandbox_runtime_process_events
  WHERE process_id = target_id;

  IF event_count <> parent.version - 1
     OR (parent.version = 1 AND (min_sequence IS NOT NULL OR max_sequence IS NOT NULL))
     OR (parent.version > 1 AND (min_sequence <> 1 OR max_sequence <> parent.version - 1)) THEN
    RAISE EXCEPTION 'Sandbox process projection and event chain differ'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER sandbox_runtime_process_parent_event_chain_guard
AFTER INSERT OR UPDATE ON sandbox_runtime_processes
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_runtime_process_event_chain();

CREATE CONSTRAINT TRIGGER sandbox_runtime_process_event_chain_guard
AFTER INSERT ON sandbox_runtime_process_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_runtime_process_event_chain();
