-- Durable lifecycle/audit projections for non-root interactive terminals.
-- Terminal bytes use the bounded Redis PTY stream; PostgreSQL stores identity,
-- fences, lifecycle, dimensions, and aggregate output accounting only.

CREATE TABLE sandbox_terminals (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'sandbox-terminal/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  session_version_at_creation bigint NOT NULL CHECK (session_version_at_creation > 0),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  working_directory text NOT NULL CHECK (
    working_directory = btrim(working_directory)
    AND length(working_directory) BETWEEN 1 AND 512
    AND left(working_directory, 1) <> '/'
    AND position(E'\\' in working_directory) = 0
    AND (
      working_directory = '.'
      OR working_directory !~ '(^|/)\.\.?(/|$)'
    )
    AND working_directory !~ '[[:cntrl:]]'
  ),
  shell_path text NOT NULL CHECK (shell_path = '/bin/bash'),
  rows integer NOT NULL CHECK (rows BETWEEN 2 AND 500),
  columns integer NOT NULL CHECK (columns BETWEEN 2 AND 500),
  output_limit_bytes bigint NOT NULL CHECK (output_limit_bytes BETWEEN 1024 AND 67108864),
  state text NOT NULL CHECK (state IN ('opening', 'running', 'exited', 'failed', 'orphaned')),
  version bigint NOT NULL CHECK (version > 0),
  exit_code integer,
  failure text CHECK (
    failure IS NULL OR (failure = btrim(failure) AND length(failure) BETWEEN 1 AND 1000)
  ),
  output_bytes bigint NOT NULL CHECK (output_bytes >= 0 AND output_bytes <= output_limit_bytes),
  output_truncated boolean NOT NULL,
  runtime_started_at timestamptz,
  finished_at timestamptz,
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT sandbox_terminal_time_order CHECK (
    updated_at >= created_at
    AND (runtime_started_at IS NULL OR runtime_started_at >= created_at)
    AND (finished_at IS NULL OR (runtime_started_at IS NOT NULL AND finished_at >= runtime_started_at))
  )
);

CREATE INDEX sandbox_terminal_session_idx
  ON sandbox_terminals (session_id, session_epoch, created_at DESC, id DESC);

CREATE INDEX sandbox_terminal_active_idx
  ON sandbox_terminals (session_id, session_epoch, state, updated_at DESC)
  WHERE state IN ('opening', 'running');

CREATE OR REPLACE FUNCTION validate_sandbox_terminal_insert()
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
    RAISE EXCEPTION 'Sandbox terminal requires the exact ready session version and epoch'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state <> 'opening'
     OR NEW.version <> 1
     OR NEW.exit_code IS NOT NULL
     OR NEW.failure IS NOT NULL
     OR NEW.output_bytes <> 0
     OR NEW.output_truncated
     OR NEW.runtime_started_at IS NOT NULL
     OR NEW.finished_at IS NOT NULL THEN
    RAISE EXCEPTION 'Sandbox terminal must start as an empty opening projection at version 1'
      USING ERRCODE = '23514';
  END IF;

  IF (
    SELECT count(*)
    FROM sandbox_terminals AS terminal
    WHERE terminal.session_id = NEW.session_id
      AND terminal.session_epoch = NEW.session_epoch
      AND terminal.state IN ('opening', 'running')
  ) >= LEAST(parent.pid_limit, 8) THEN
    RAISE EXCEPTION 'Sandbox terminal concurrency exceeds the session bound'
      USING ERRCODE = '23514';
  END IF;

  NEW.creation_transaction_id := txid_current();
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_terminal_insert_guard
BEFORE INSERT ON sandbox_terminals
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_terminal_insert();

CREATE TABLE sandbox_terminal_events (
  terminal_id uuid NOT NULL REFERENCES sandbox_terminals(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  sequence bigint NOT NULL CHECK (sequence > 0),
  terminal_version_from bigint NOT NULL CHECK (terminal_version_from > 0),
  terminal_version_to bigint NOT NULL CHECK (terminal_version_to = terminal_version_from + 1),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  event_kind text NOT NULL CHECK (event_kind IN (
    'runtime.opened', 'runtime.failed', 'runtime.exited',
    'attached', 'detached', 'resized', 'signal.sent', 'close.sent', 'epoch.fenced'
  )),
  state_from text NOT NULL CHECK (state_from IN ('opening', 'running', 'exited', 'failed', 'orphaned')),
  state_to text NOT NULL CHECK (state_to IN ('opening', 'running', 'exited', 'failed', 'orphaned')),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  request_id uuid,
  signal text CHECK (signal IS NULL OR signal IN ('INT', 'TERM', 'KILL', 'HUP')),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  rows_to integer NOT NULL CHECK (rows_to BETWEEN 2 AND 500),
  columns_to integer NOT NULL CHECK (columns_to BETWEEN 2 AND 500),
  exit_code_to integer,
  failure_to text CHECK (
    failure_to IS NULL OR (failure_to = btrim(failure_to) AND length(failure_to) BETWEEN 1 AND 1000)
  ),
  output_bytes_to bigint NOT NULL CHECK (output_bytes_to >= 0),
  output_truncated_to boolean NOT NULL,
  runtime_started_at_to timestamptz,
  finished_at_to timestamptz,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  PRIMARY KEY (terminal_id, sequence),
  CONSTRAINT sandbox_terminal_event_version_from_unique UNIQUE (terminal_id, terminal_version_from),
  CONSTRAINT sandbox_terminal_event_version_to_unique UNIQUE (terminal_id, terminal_version_to)
);

CREATE INDEX sandbox_terminal_event_actor_idx
  ON sandbox_terminal_events (actor_id, created_at DESC);

CREATE OR REPLACE FUNCTION validate_sandbox_terminal_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent sandbox_terminals%ROWTYPE;
  transition_valid boolean := false;
BEGIN
  SELECT * INTO parent
  FROM sandbox_terminals
  WHERE id = NEW.terminal_id
  FOR UPDATE;

  IF NOT FOUND
     OR NEW.sequence <> parent.version
     OR NEW.terminal_version_from <> parent.version
     OR NEW.terminal_version_to <> parent.version + 1
     OR NEW.session_epoch <> parent.session_epoch
     OR NEW.state_from <> parent.state
     OR NEW.output_bytes_to > parent.output_limit_bytes THEN
    RAISE EXCEPTION 'Sandbox terminal event failed its version, epoch, or output fence'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state_to = 'running' THEN
    transition_valid := NEW.exit_code_to IS NULL
      AND NEW.failure_to IS NULL
      AND NEW.runtime_started_at_to IS NOT NULL
      AND NEW.finished_at_to IS NULL;
  ELSIF NEW.state_to = 'exited' THEN
    transition_valid := NEW.exit_code_to IS NOT NULL
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
      AND NEW.runtime_started_at_to IS NOT NULL
      AND NEW.finished_at_to IS NOT NULL;
  END IF;

  IF NOT transition_valid THEN
    RAISE EXCEPTION 'Sandbox terminal event target facts are invalid'
      USING ERRCODE = '23514';
  END IF;

  IF NEW.event_kind = 'runtime.opened' THEN
    transition_valid := parent.state = 'opening'
      AND NEW.state_to = 'running'
      AND NEW.signal IS NULL;
  ELSIF NEW.event_kind = 'runtime.failed' THEN
    transition_valid := parent.state = 'opening'
      AND NEW.state_to = 'failed'
      AND NEW.signal IS NULL;
  ELSIF NEW.event_kind = 'runtime.exited' THEN
    transition_valid := parent.state IN ('opening', 'running')
      AND NEW.state_to IN ('exited', 'failed')
      AND NEW.signal IS NULL;
  ELSIF NEW.event_kind IN ('attached', 'detached', 'resized', 'close.sent') THEN
    transition_valid := parent.state = 'running'
      AND NEW.state_to = 'running'
      AND NEW.signal IS NULL
      AND NEW.output_bytes_to = parent.output_bytes
      AND NEW.output_truncated_to = parent.output_truncated;
  ELSIF NEW.event_kind = 'signal.sent' THEN
    transition_valid := parent.state = 'running'
      AND NEW.state_to = 'running'
      AND NEW.signal IS NOT NULL
      AND NEW.output_bytes_to = parent.output_bytes
      AND NEW.output_truncated_to = parent.output_truncated;
  ELSIF NEW.event_kind = 'epoch.fenced' THEN
    transition_valid := parent.state IN ('opening', 'running')
      AND NEW.state_to = 'orphaned'
      AND NEW.signal IS NULL;
  ELSE
    transition_valid := false;
  END IF;

  IF NOT transition_valid THEN
    RAISE EXCEPTION 'Invalid Sandbox terminal state transition'
      USING ERRCODE = '55000';
  END IF;

  IF NEW.event_kind <> 'resized'
     AND (NEW.rows_to <> parent.rows OR NEW.columns_to <> parent.columns) THEN
    RAISE EXCEPTION 'Only a resize event may change terminal dimensions'
      USING ERRCODE = '23514';
  END IF;

  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_terminal_event_guard
BEFORE INSERT ON sandbox_terminal_events
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_terminal_event();

CREATE OR REPLACE FUNCTION advance_sandbox_terminal_from_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE sandbox_terminals
  SET state = NEW.state_to,
      version = NEW.terminal_version_to,
      rows = NEW.rows_to,
      columns = NEW.columns_to,
      exit_code = NEW.exit_code_to,
      failure = NEW.failure_to,
      output_bytes = NEW.output_bytes_to,
      output_truncated = NEW.output_truncated_to,
      runtime_started_at = NEW.runtime_started_at_to,
      finished_at = NEW.finished_at_to,
      updated_at = NEW.created_at
  WHERE id = NEW.terminal_id
    AND version = NEW.terminal_version_from
    AND session_epoch = NEW.session_epoch
    AND state = NEW.state_from;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Sandbox terminal event CAS update lost its writer fence'
      USING ERRCODE = '40001';
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER sandbox_terminal_event_advance
AFTER INSERT ON sandbox_terminal_events
FOR EACH ROW EXECUTE FUNCTION advance_sandbox_terminal_from_event();

CREATE OR REPLACE FUNCTION prevent_sandbox_terminal_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'Sandbox terminal history is immutable'
      USING ERRCODE = '55000';
  END IF;

  IF ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.session_id,
    NEW.session_epoch, NEW.session_version_at_creation, NEW.actor_id,
    NEW.working_directory, NEW.shell_path, NEW.output_limit_bytes,
    NEW.creation_transaction_id, NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.session_id,
    OLD.session_epoch, OLD.session_version_at_creation, OLD.actor_id,
    OLD.working_directory, OLD.shell_path, OLD.output_limit_bytes,
    OLD.creation_transaction_id, OLD.created_at
  ) THEN
    RAISE EXCEPTION 'Sandbox terminal identity and fixed shell are immutable'
      USING ERRCODE = '55000';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM sandbox_terminal_events AS event
    WHERE event.terminal_id = OLD.id
      AND event.terminal_version_from = OLD.version
      AND event.terminal_version_to = NEW.version
      AND event.session_epoch = OLD.session_epoch
      AND event.state_from = OLD.state
      AND event.state_to = NEW.state
      AND ROW(
        event.rows_to, event.columns_to, event.exit_code_to, event.failure_to,
        event.output_bytes_to, event.output_truncated_to,
        event.runtime_started_at_to, event.finished_at_to,
        event.creation_transaction_id
      ) IS NOT DISTINCT FROM ROW(
        NEW.rows, NEW.columns, NEW.exit_code, NEW.failure,
        NEW.output_bytes, NEW.output_truncated,
        NEW.runtime_started_at, NEW.finished_at,
        txid_current()
      )
  ) THEN
    RAISE EXCEPTION 'Sandbox terminal projection may advance only from its append-only event'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER sandbox_terminal_mutation_guard
BEFORE UPDATE OR DELETE ON sandbox_terminals
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_terminal_mutation();

CREATE OR REPLACE FUNCTION prevent_sandbox_terminal_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Sandbox terminal events are append-only'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER sandbox_terminal_event_immutable
BEFORE UPDATE OR DELETE ON sandbox_terminal_events
FOR EACH ROW EXECUTE FUNCTION prevent_sandbox_terminal_event_mutation();

CREATE OR REPLACE FUNCTION validate_sandbox_terminal_event_chain()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_id uuid;
  parent sandbox_terminals%ROWTYPE;
  event_count bigint;
  min_sequence bigint;
  max_sequence bigint;
BEGIN
  target_id := COALESCE(
    (to_jsonb(NEW)->>'id')::uuid,
    (to_jsonb(NEW)->>'terminal_id')::uuid
  );
  SELECT * INTO parent FROM sandbox_terminals WHERE id = target_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Sandbox terminal event chain has no projection'
      USING ERRCODE = '23503';
  END IF;

  SELECT count(*), min(sequence), max(sequence)
  INTO event_count, min_sequence, max_sequence
  FROM sandbox_terminal_events
  WHERE terminal_id = target_id;

  IF event_count <> parent.version - 1
     OR (parent.version = 1 AND (min_sequence IS NOT NULL OR max_sequence IS NOT NULL))
     OR (parent.version > 1 AND (min_sequence <> 1 OR max_sequence <> parent.version - 1)) THEN
    RAISE EXCEPTION 'Sandbox terminal projection and event chain differ'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER sandbox_terminal_parent_event_chain_guard
AFTER INSERT OR UPDATE ON sandbox_terminals
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_terminal_event_chain();

CREATE CONSTRAINT TRIGGER sandbox_terminal_event_chain_guard
AFTER INSERT ON sandbox_terminal_events
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_sandbox_terminal_event_chain();
