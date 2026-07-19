-- A quarantined v2 delivery Operation may only leave reconcile_blocked after
-- an administrator records a complete immutable Case. The Case does not
-- decide the remote outcome: it authorizes one due GET of the same exact
-- Operation ID/hash under the same pinned controller identity. Once governed
-- reconciliation has occurred, resubmission is permanently forbidden.

CREATE OR REPLACE FUNCTION release_delivery_rfc3339_microsecond(value timestamptz)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
  SELECT to_char(value AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS') ||
    CASE
      WHEN mod(floor(extract(microseconds FROM value))::bigint, 1000000) = 0 THEN ''
      ELSE '.' || regexp_replace(
        lpad(mod(floor(extract(microseconds FROM value))::bigint, 1000000)::text, 6, '0'),
        '0+$', ''
      )
    END || 'Z'
$$;

CREATE TABLE release_delivery_reconciliation_cases (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (
    schema_version = 'release-delivery-reconciliation-case/v1'
  ),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  run_kind text NOT NULL CHECK (run_kind IN ('preview','production')),
  run_id uuid NOT NULL,
  run_schema_version text NOT NULL CHECK (
    (run_kind = 'preview' AND run_schema_version = 'release-preview-run/v2')
    OR
    (run_kind = 'production' AND run_schema_version = 'release-deployment-run/v2')
  ),
  expected_run_version bigint NOT NULL CHECK (expected_run_version > 0),
  operation_id uuid NOT NULL,
  operation_request_hash text NOT NULL CHECK (
    operation_request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  controller_schema_version text NOT NULL CHECK (
    controller_schema_version = 'release-delivery-controller-identity/v1'
  ),
  controller_id text NOT NULL CHECK (
    controller_id = btrim(controller_id) AND length(controller_id) BETWEEN 1 AND 200
  ),
  controller_version text NOT NULL CHECK (
    controller_version = btrim(controller_version)
    AND length(controller_version) BETWEEN 1 AND 120
  ),
  controller_protocol text NOT NULL CHECK (
    controller_protocol = 'worksflow.release-delivery/v3'
  ),
  controller_trust_key_digest text NOT NULL CHECK (
    controller_trust_key_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  previous_remote_state text NOT NULL CHECK (previous_remote_state = 'quarantined'),
  resume_remote_state text NOT NULL CHECK (
    resume_remote_state IN ('submit_unknown','accepted','running')
  ),
  submit_attempt_count bigint NOT NULL CHECK (submit_attempt_count >= 0),
  reconcile_attempt_count bigint NOT NULL CHECK (reconcile_attempt_count >= 0),
  last_attempt_ordinal bigint NOT NULL CHECK (last_attempt_ordinal > 0),
  last_attempt_kind text NOT NULL CHECK (
    last_attempt_kind IN ('submit','reconcile','resubmit')
  ),
  last_attempt_worker_id text NOT NULL CHECK (
    last_attempt_worker_id = btrim(last_attempt_worker_id)
    AND length(last_attempt_worker_id) BETWEEN 1 AND 200
  ),
  last_attempt_fence_epoch bigint NOT NULL CHECK (last_attempt_fence_epoch > 0),
  last_attempt_started_at timestamptz NOT NULL,
  last_attempt_completed_at timestamptz NOT NULL,
  last_attempt_outcome text NOT NULL CHECK (last_attempt_outcome = 'quarantined'),
  last_observation_sequence bigint CHECK (
    last_observation_sequence IS NULL OR last_observation_sequence > 0
  ),
  last_observed_at timestamptz,
  quarantine_error_code text NOT NULL CHECK (
    quarantine_error_code = btrim(quarantine_error_code)
    AND length(quarantine_error_code) BETWEEN 1 AND 128
  ),
  quarantine_error_detail text NOT NULL CHECK (
    length(btrim(quarantine_error_detail)) BETWEEN 1 AND 4000
  ),
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
  idempotency_key text NOT NULL CHECK (
    idempotency_key = btrim(idempotency_key)
    AND length(idempotency_key) BETWEEN 1 AND 128
  ),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  case_document text NOT NULL CHECK (
    jsonb_typeof(case_document::jsonb) = 'object'
    AND octet_length(case_document) BETWEEN 2 AND 262144
    AND case_document = release_delivery_canonical_json(case_document::jsonb)
  ),
  case_hash text NOT NULL CHECK (case_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL,
  created_txid bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT release_delivery_reconciliation_case_operation_exact_fk
    FOREIGN KEY (operation_id, operation_request_hash)
    REFERENCES release_delivery_operations(id, request_hash) ON DELETE RESTRICT,
  CONSTRAINT release_delivery_reconciliation_case_observation_shape CHECK (
    (last_observation_sequence IS NULL AND last_observed_at IS NULL
      AND resume_remote_state = 'submit_unknown')
    OR
    (last_observation_sequence IS NOT NULL AND last_observed_at IS NOT NULL
      AND resume_remote_state IN ('submit_unknown','accepted','running'))
  ),
  CONSTRAINT release_delivery_reconciliation_case_project_key_unique
    UNIQUE (project_id, idempotency_key),
  CONSTRAINT release_delivery_reconciliation_case_run_version_unique
    UNIQUE (operation_id, expected_run_version)
);

CREATE INDEX release_delivery_reconciliation_cases_project_audit_idx
  ON release_delivery_reconciliation_cases (project_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_release_delivery_reconciliation_case_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  operation release_delivery_operations%ROWTYPE;
  attempt release_delivery_operation_attempts%ROWTYPE;
  run_state text;
  run_schema text;
  run_version bigint;
  run_worker text;
  run_lease_epoch bigint;
  run_lease_expires timestamptz;
  derived_resume text;
  expected_document jsonb;
  expected_request_hash text;
BEGIN
  SELECT * INTO STRICT operation
  FROM release_delivery_operations
  WHERE id = NEW.operation_id
  FOR UPDATE;

  IF operation.kind = 'preview' THEN
    SELECT schema_version, state, version, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_schema, run_state, run_version, run_worker, run_lease_epoch, run_lease_expires
    FROM release_preview_runs
    WHERE id = operation.preview_run_id AND id = NEW.run_id
    FOR UPDATE;
  ELSE
    SELECT schema_version, state, version, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_schema, run_state, run_version, run_worker, run_lease_epoch, run_lease_expires
    FROM release_deployment_runs
    WHERE id = operation.deployment_run_id AND id = NEW.run_id
    FOR UPDATE;
  END IF;

  SELECT * INTO STRICT attempt
  FROM release_delivery_operation_attempts
  WHERE operation_id = operation.id
  ORDER BY ordinal DESC LIMIT 1;

  SELECT observed.outcome INTO derived_resume
  FROM release_delivery_operation_attempts AS observed
  WHERE observed.operation_id = operation.id
    AND observed.outcome IN ('accepted','running')
    AND observed.observation_sequence IS NOT NULL
    AND observed.observed_at IS NOT NULL
  ORDER BY observed.ordinal DESC LIMIT 1;
  derived_resume := COALESCE(derived_resume, 'submit_unknown');

  IF NEW.schema_version <> 'release-delivery-reconciliation-case/v1'
     OR NEW.project_id <> operation.project_id
     OR NEW.run_kind <> operation.kind
     OR NEW.run_schema_version <> run_schema
     OR run_schema NOT IN ('release-preview-run/v2','release-deployment-run/v2')
     OR run_state <> 'reconcile_blocked'
     OR run_version <> NEW.expected_run_version
     OR run_worker IS NOT NULL OR run_lease_epoch IS NOT NULL OR run_lease_expires IS NOT NULL
     OR operation.remote_state <> 'quarantined'
     OR operation.terminal_result_hash IS NOT NULL
     OR NEW.operation_request_hash <> operation.request_hash
     OR NEW.controller_schema_version <> operation.controller_schema_version
     OR NEW.controller_id <> operation.controller_id
     OR NEW.controller_version <> operation.controller_version
     OR NEW.controller_protocol <> operation.controller_protocol
     OR NEW.controller_trust_key_digest <> operation.controller_trust_key_digest
     OR NEW.previous_remote_state <> operation.remote_state
     OR NEW.resume_remote_state <> derived_resume
     OR NEW.submit_attempt_count <> operation.submit_attempt_count
     OR NEW.reconcile_attempt_count <> operation.reconcile_attempt_count
     OR NEW.last_observation_sequence IS DISTINCT FROM operation.last_observation_sequence
     OR NEW.last_observed_at IS DISTINCT FROM operation.last_observed_at
     OR NEW.quarantine_error_code IS DISTINCT FROM operation.last_error_code
     OR NEW.quarantine_error_detail IS DISTINCT FROM operation.last_error_detail
     OR NEW.last_attempt_ordinal <> attempt.ordinal
     OR NEW.last_attempt_kind <> attempt.kind
     OR NEW.last_attempt_worker_id <> attempt.worker_id
     OR NEW.last_attempt_fence_epoch <> attempt.fence_epoch
     OR NEW.last_attempt_started_at <> attempt.started_at
     OR NEW.last_attempt_completed_at IS DISTINCT FROM attempt.completed_at
     OR NEW.last_attempt_outcome IS DISTINCT FROM attempt.outcome
     OR NEW.quarantine_error_code IS DISTINCT FROM attempt.error_code
     OR NEW.quarantine_error_detail IS DISTINCT FROM attempt.error_detail
     OR attempt.outcome <> 'quarantined'
     OR NEW.created_at < transaction_timestamp()
     OR NEW.created_at > statement_timestamp()
     OR NEW.created_txid <> txid_current()
     OR NOT EXISTS (
       SELECT 1 FROM project_members AS member
       WHERE member.project_id = NEW.project_id
         AND member.user_id = NEW.actor_id
         AND member.role IN ('owner','admin')
     ) THEN
    RAISE EXCEPTION 'delivery reconciliation Case requires exact blocked v2 authority and an administrator'
      USING ERRCODE = '40001';
  END IF;

  expected_request_hash := 'sha256:' || encode(sha256(convert_to(
    release_delivery_canonical_json(jsonb_build_object(
      'operation', 'resume-blocked-delivery',
      'projectId', NEW.project_id::text,
      'runKind', NEW.run_kind,
      'runId', NEW.run_id::text,
      'expectedVersion', NEW.expected_run_version,
      'expectedErrorCode', NEW.quarantine_error_code,
      'reason', NEW.reason
    )), 'UTF8'
  )), 'hex');

  expected_document := jsonb_build_object(
    'schemaVersion', NEW.schema_version,
    'id', NEW.id::text,
    'projectId', NEW.project_id::text,
    'runKind', NEW.run_kind,
    'runId', NEW.run_id::text,
    'runSchemaVersion', NEW.run_schema_version,
    'expectedRunVersion', NEW.expected_run_version,
    'operationId', NEW.operation_id::text,
    'operationRequestHash', NEW.operation_request_hash,
    'controller', jsonb_build_object(
      'schemaVersion', NEW.controller_schema_version,
      'id', NEW.controller_id,
      'version', NEW.controller_version,
      'protocol', NEW.controller_protocol,
      'trustKeyDigest', NEW.controller_trust_key_digest
    ),
    'previousRemoteState', NEW.previous_remote_state,
    'resumeRemoteState', NEW.resume_remote_state,
    'submitAttemptCount', NEW.submit_attempt_count,
    'reconcileAttemptCount', NEW.reconcile_attempt_count,
    'lastAttempt', jsonb_build_object(
      'ordinal', NEW.last_attempt_ordinal,
      'kind', NEW.last_attempt_kind,
      'workerId', NEW.last_attempt_worker_id,
      'fenceEpoch', NEW.last_attempt_fence_epoch,
      'startedAt', to_jsonb(release_delivery_rfc3339_microsecond(NEW.last_attempt_started_at)),
      'completedAt', to_jsonb(release_delivery_rfc3339_microsecond(NEW.last_attempt_completed_at)),
      'outcome', NEW.last_attempt_outcome,
      'errorCode', NEW.quarantine_error_code,
      'errorDetail', NEW.quarantine_error_detail
    ),
    'lastObservation', CASE
      WHEN NEW.last_observation_sequence IS NULL THEN 'null'::jsonb
      ELSE jsonb_build_object(
        'sequence', NEW.last_observation_sequence,
        'observedAt', to_jsonb(release_delivery_rfc3339_microsecond(NEW.last_observed_at))
      )
    END,
    'quarantineError', jsonb_build_object(
      'code', NEW.quarantine_error_code,
      'detail', NEW.quarantine_error_detail
    ),
    'actorId', NEW.actor_id::text,
    'reason', NEW.reason,
    'idempotencyKey', NEW.idempotency_key,
    'requestHash', NEW.request_hash,
    'caseHash', NEW.case_hash,
    'createdAt', to_jsonb(release_delivery_rfc3339_microsecond(NEW.created_at))
  );

  IF NEW.request_hash <> expected_request_hash
     OR NEW.case_document::jsonb <> expected_document
     OR NEW.case_document <> release_delivery_canonical_json(expected_document)
     OR NEW.case_hash <> 'sha256:' || encode(sha256(convert_to(
       release_delivery_canonical_json(jsonb_set(
         expected_document, '{caseHash}', '""'::jsonb, false
       )), 'UTF8'
     )), 'hex') THEN
    RAISE EXCEPTION 'delivery reconciliation Case must be its exact canonical request and projection'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_reconciliation_case_insert_guard
BEFORE INSERT ON release_delivery_reconciliation_cases
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_reconciliation_case_insert();

CREATE OR REPLACE FUNCTION validate_release_delivery_reconciliation_case_applied()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  operation release_delivery_operations%ROWTYPE;
  run_state text;
  run_version bigint;
  run_worker text;
  run_lease_epoch bigint;
  run_lease_expires timestamptz;
BEGIN
  SELECT * INTO STRICT operation
  FROM release_delivery_operations WHERE id = NEW.operation_id;
  IF NEW.run_kind = 'preview' THEN
    SELECT state, version, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_version, run_worker, run_lease_epoch, run_lease_expires
    FROM release_preview_runs WHERE id = NEW.run_id;
  ELSE
    SELECT state, version, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_version, run_worker, run_lease_epoch, run_lease_expires
    FROM release_deployment_runs WHERE id = NEW.run_id;
  END IF;
  IF NEW.created_txid <> txid_current()
     OR operation.remote_state <> NEW.resume_remote_state
     OR operation.next_attempt_at IS NULL
     OR operation.terminal_result_hash IS NOT NULL
     OR operation.last_error_code IS NOT NULL
     OR operation.last_error_detail IS NOT NULL
     OR run_state <> 'reconcile_wait'
     OR run_version <> NEW.expected_run_version + 1
     OR run_worker IS NOT NULL OR run_lease_epoch IS NOT NULL OR run_lease_expires IS NOT NULL THEN
    RAISE EXCEPTION 'delivery reconciliation Case and exact GET-only resume must commit atomically'
      USING ERRCODE = '40001';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER release_delivery_reconciliation_case_applied_guard
AFTER INSERT ON release_delivery_reconciliation_cases
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_reconciliation_case_applied();

CREATE OR REPLACE FUNCTION prevent_release_delivery_reconciliation_case_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'release delivery reconciliation Cases are immutable append-only evidence'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER release_delivery_reconciliation_case_immutable
BEFORE UPDATE OR DELETE ON release_delivery_reconciliation_cases
FOR EACH ROW EXECUTE FUNCTION prevent_release_delivery_reconciliation_case_mutation();

CREATE OR REPLACE FUNCTION prevent_resubmit_after_delivery_reconciliation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.kind = 'resubmit' AND EXISTS (
    SELECT 1 FROM release_delivery_reconciliation_cases AS resolution
    WHERE resolution.operation_id = NEW.operation_id
  ) THEN
    RAISE EXCEPTION 'governed delivery reconciliation is GET-only; resubmission is forbidden'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_operation_attempt_00_reconcile_only_guard
BEFORE INSERT ON release_delivery_operation_attempts
FOR EACH ROW EXECUTE FUNCTION prevent_resubmit_after_delivery_reconciliation();

CREATE OR REPLACE FUNCTION validate_release_delivery_operation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_identity jsonb;
  new_identity jsonb;
  actual_submit_count bigint;
  actual_reconcile_count bigint;
  actual_first_submit timestamptz;
  actual_last_attempt timestamptz;
  attempt release_delivery_operation_attempts%ROWTYPE;
  run_state text;
  run_version bigint;
  run_worker text;
  run_fence bigint;
  run_expires timestamptz;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'release delivery Operations cannot be deleted'
      USING ERRCODE = '55000';
  END IF;

  old_identity := to_jsonb(OLD) - ARRAY[
    'remote_state','submit_attempt_count','reconcile_attempt_count',
    'next_attempt_at','first_submitted_at','last_attempt_at',
    'last_observation_sequence','last_observed_at',
    'terminal_result_hash','last_error_code','last_error_detail','updated_at'
  ];
  new_identity := to_jsonb(NEW) - ARRAY[
    'remote_state','submit_attempt_count','reconcile_attempt_count',
    'next_attempt_at','first_submitted_at','last_attempt_at',
    'last_observation_sequence','last_observed_at',
    'terminal_result_hash','last_error_code','last_error_detail','updated_at'
  ];
  IF old_identity IS DISTINCT FROM new_identity THEN
    RAISE EXCEPTION 'release delivery Operation request and controller identity are immutable'
      USING ERRCODE = '55000';
  END IF;

  SELECT
    count(*) FILTER (WHERE kind IN ('submit','resubmit')),
    count(*) FILTER (WHERE kind = 'reconcile'),
    min(started_at) FILTER (WHERE kind IN ('submit','resubmit')),
    max(started_at)
  INTO actual_submit_count, actual_reconcile_count, actual_first_submit, actual_last_attempt
  FROM release_delivery_operation_attempts
  WHERE operation_id = NEW.id;

  IF NEW.submit_attempt_count <> actual_submit_count
     OR NEW.reconcile_attempt_count <> actual_reconcile_count
     OR NEW.first_submitted_at IS DISTINCT FROM actual_first_submit
     OR NEW.last_attempt_at IS DISTINCT FROM actual_last_attempt
     OR NEW.updated_at <= OLD.updated_at THEN
    RAISE EXCEPTION 'release delivery Operation attempt projection is not exact'
      USING ERRCODE = '40001';
  END IF;

  -- This is the sole new edge introduced by migration 58. The immutable Case
  -- was inserted while both Operation and Run were locked, and binds every
  -- value that this projection is allowed to change.
  IF OLD.remote_state = 'quarantined'
     AND NEW.remote_state IN ('submit_unknown','accepted','running') THEN
    IF NEW.kind = 'preview' THEN
      SELECT state, version, lease_worker_id, lease_epoch, lease_expires_at
      INTO STRICT run_state, run_version, run_worker, run_fence, run_expires
      FROM release_preview_runs WHERE id = NEW.preview_run_id;
    ELSE
      SELECT state, version, lease_worker_id, lease_epoch, lease_expires_at
      INTO STRICT run_state, run_version, run_worker, run_fence, run_expires
      FROM release_deployment_runs WHERE id = NEW.deployment_run_id;
    END IF;
    IF run_state <> 'reconcile_blocked'
       OR run_worker IS NOT NULL OR run_fence IS NOT NULL OR run_expires IS NOT NULL
       OR NEW.submit_attempt_count <> OLD.submit_attempt_count
       OR NEW.reconcile_attempt_count <> OLD.reconcile_attempt_count
       OR NEW.first_submitted_at IS DISTINCT FROM OLD.first_submitted_at
       OR NEW.last_attempt_at IS DISTINCT FROM OLD.last_attempt_at
       OR NEW.last_observation_sequence IS DISTINCT FROM OLD.last_observation_sequence
       OR NEW.last_observed_at IS DISTINCT FROM OLD.last_observed_at
       OR NEW.terminal_result_hash IS NOT NULL
       OR NEW.last_error_code IS NOT NULL OR NEW.last_error_detail IS NOT NULL
       OR NEW.next_attempt_at IS NULL OR NEW.next_attempt_at > statement_timestamp()
       OR NOT EXISTS (
         SELECT 1
         FROM release_delivery_reconciliation_cases AS resolution
         WHERE resolution.operation_id = OLD.id
           AND resolution.operation_request_hash = OLD.request_hash
           AND resolution.project_id = OLD.project_id
           AND resolution.run_kind = OLD.kind
           AND resolution.run_id = CASE
             WHEN OLD.kind = 'preview' THEN OLD.preview_run_id
             ELSE OLD.deployment_run_id
           END
           AND resolution.expected_run_version = run_version
           AND resolution.previous_remote_state = OLD.remote_state
           AND resolution.resume_remote_state = NEW.remote_state
           AND resolution.submit_attempt_count = OLD.submit_attempt_count
           AND resolution.reconcile_attempt_count = OLD.reconcile_attempt_count
           AND resolution.last_observation_sequence IS NOT DISTINCT FROM OLD.last_observation_sequence
           AND resolution.last_observed_at IS NOT DISTINCT FROM OLD.last_observed_at
           AND resolution.quarantine_error_code IS NOT DISTINCT FROM OLD.last_error_code
           AND resolution.quarantine_error_detail IS NOT DISTINCT FROM OLD.last_error_detail
           AND resolution.controller_schema_version = OLD.controller_schema_version
           AND resolution.controller_id = OLD.controller_id
           AND resolution.controller_version = OLD.controller_version
           AND resolution.controller_protocol = OLD.controller_protocol
           AND resolution.controller_trust_key_digest = OLD.controller_trust_key_digest
           AND resolution.created_txid = txid_current()
       ) THEN
      RAISE EXCEPTION 'quarantined Operation may resume only from one exact immutable reconciliation Case'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.remote_state = OLD.remote_state THEN
    IF NEW.submit_attempt_count + NEW.reconcile_attempt_count
         = OLD.submit_attempt_count + OLD.reconcile_attempt_count + 1 THEN
      IF NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at
         OR NEW.last_observation_sequence IS DISTINCT FROM OLD.last_observation_sequence
         OR NEW.last_observed_at IS DISTINCT FROM OLD.last_observed_at
         OR NEW.terminal_result_hash IS DISTINCT FROM OLD.terminal_result_hash
         OR NEW.last_error_code IS DISTINCT FROM OLD.last_error_code
         OR NEW.last_error_detail IS DISTINCT FROM OLD.last_error_detail THEN
        RAISE EXCEPTION 'Attempt append may only advance exact Operation counters'
          USING ERRCODE = '40001';
      END IF;
      RETURN NEW;
    END IF;
  END IF;

  IF OLD.remote_state IN ('completed','rejected','quarantined') THEN
    RAISE EXCEPTION 'terminal release delivery Operation authority is immutable'
      USING ERRCODE = '55000';
  END IF;
  IF NEW.submit_attempt_count <> OLD.submit_attempt_count
     OR NEW.reconcile_attempt_count <> OLD.reconcile_attempt_count
     OR NEW.first_submitted_at IS DISTINCT FROM OLD.first_submitted_at
     OR NEW.last_attempt_at IS DISTINCT FROM OLD.last_attempt_at THEN
    RAISE EXCEPTION 'Operation state transition cannot forge Attempt projections'
      USING ERRCODE = '40001';
  END IF;

  SELECT * INTO attempt
  FROM release_delivery_operation_attempts
  WHERE operation_id = NEW.id
  ORDER BY ordinal DESC LIMIT 1;
  IF NOT FOUND OR attempt.completed_at IS NULL THEN
    RAISE EXCEPTION 'Operation observation requires one exact completed Attempt'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.kind = 'preview' THEN
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_preview_runs WHERE id = NEW.preview_run_id;
  ELSE
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_deployment_runs WHERE id = NEW.deployment_run_id;
  END IF;
  IF run_state NOT IN ('submitting','reconciling')
     OR run_expires <= statement_timestamp()
     OR attempt.worker_id IS DISTINCT FROM run_worker
     OR attempt.fence_epoch IS DISTINCT FROM run_fence THEN
    RAISE EXCEPTION 'Operation observation lost its exact live Run fence'
      USING ERRCODE = '40001';
  END IF;

  IF NOT (
    (OLD.remote_state = NEW.remote_state
      AND NEW.remote_state IN ('submit_unknown','accepted','running'))
    OR (OLD.remote_state = 'prepared'
      AND NEW.remote_state IN (
        'submit_unknown','accepted','running','completed','rejected','quarantined'
      ))
    OR (OLD.remote_state = 'submit_unknown'
      AND NEW.remote_state IN ('accepted','running','completed','rejected','quarantined'))
    OR (OLD.remote_state = 'accepted'
      AND NEW.remote_state IN ('running','completed','quarantined'))
    OR (OLD.remote_state = 'running'
      AND NEW.remote_state IN ('completed','quarantined'))
  ) THEN
    RAISE EXCEPTION 'release delivery remote observation is not monotonic'
      USING ERRCODE = '40001';
  END IF;

  IF NOT (
    (NEW.remote_state = 'submit_unknown'
      AND attempt.outcome IN ('unknown','retryable_error'))
    OR (NEW.remote_state = OLD.remote_state
      AND NEW.remote_state IN ('accepted','running')
      AND attempt.outcome IN ('unknown','retryable_error'))
    OR (NEW.remote_state IN ('accepted','running','completed','rejected','quarantined')
      AND attempt.outcome = NEW.remote_state)
  ) THEN
    RAISE EXCEPTION 'Operation state does not match its latest immutable Attempt outcome'
      USING ERRCODE = '40001';
  END IF;

  IF attempt.outcome IN ('accepted','running','completed','rejected') THEN
    IF attempt.observation_sequence IS NULL
       OR attempt.observed_at IS NULL
       OR NEW.last_observation_sequence IS DISTINCT FROM attempt.observation_sequence
       OR NEW.last_observed_at IS DISTINCT FROM attempt.observed_at
       OR (OLD.last_observation_sequence IS NOT NULL
         AND NEW.last_observation_sequence <= OLD.last_observation_sequence) THEN
      RAISE EXCEPTION 'remote observation sequence must advance exactly and monotonically'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.last_observation_sequence IS DISTINCT FROM OLD.last_observation_sequence
        OR NEW.last_observed_at IS DISTINCT FROM OLD.last_observed_at THEN
    RAISE EXCEPTION 'unknown or quarantined Attempt cannot fabricate a remote observation'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.remote_state IN ('completed','rejected') THEN
    IF NEW.next_attempt_at IS NOT NULL
       OR NEW.terminal_result_hash IS NULL
       OR NOT EXISTS (
         SELECT 1
         FROM release_delivery_operation_results AS result
         WHERE result.operation_id = NEW.id
           AND result.request_hash = NEW.request_hash
           AND result.result_hash = NEW.terminal_result_hash
           AND result.status = NEW.remote_state
           AND result.controller_schema_version = NEW.controller_schema_version
           AND result.controller_id = NEW.controller_id
           AND result.controller_version = NEW.controller_version
           AND result.controller_protocol = NEW.controller_protocol
           AND result.controller_trust_key_digest = NEW.controller_trust_key_digest
       ) THEN
      RAISE EXCEPTION 'terminal Operation requires its exact immutable controller Result'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.remote_state = 'quarantined' THEN
    IF NEW.next_attempt_at IS NOT NULL
       OR NEW.terminal_result_hash IS NOT NULL
       OR NEW.last_error_code IS NULL THEN
      RAISE EXCEPTION 'quarantined Operation requires explicit fail-closed evidence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.next_attempt_at IS NULL OR NEW.terminal_result_hash IS NOT NULL THEN
    RAISE EXCEPTION 'nonterminal observed Operation requires a reconciliation schedule'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_release_delivery_run_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_identity jsonb;
  new_identity jsonb;
  operation release_delivery_operations%ROWTYPE;
  result release_delivery_operation_results%ROWTYPE;
BEGIN
  old_identity := to_jsonb(OLD) - ARRAY[
    'state','version','fence_epoch','lease_worker_id','lease_epoch','lease_expires_at',
    'started_at','finished_at','updated_by','updated_at'
  ];
  new_identity := to_jsonb(NEW) - ARRAY[
    'state','version','fence_epoch','lease_worker_id','lease_epoch','lease_expires_at',
    'started_at','finished_at','updated_by','updated_at'
  ];
  IF old_identity IS DISTINCT FROM new_identity THEN
    RAISE EXCEPTION 'release delivery Run authority identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  IF OLD.schema_version IN ('release-preview-run/v1','release-deployment-run/v1') THEN
    RAISE EXCEPTION 'historical v1 release delivery Run is read-only; reconcile explicitly'
      USING ERRCODE = '55000';
  END IF;
  IF NEW.updated_at <= OLD.updated_at THEN
    RAISE EXCEPTION 'release delivery Run update time must advance'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_preview_runs' THEN
    SELECT * INTO operation FROM release_delivery_operations
    WHERE preview_run_id = NEW.id;
  ELSE
    SELECT * INTO operation FROM release_delivery_operations
    WHERE deployment_run_id = NEW.id;
  END IF;
  IF NOT FOUND OR operation.project_id <> NEW.project_id THEN
    RAISE EXCEPTION 'release delivery Run transition requires its exact Operation authority'
      USING ERRCODE = '40001';
  END IF;

  -- The only operator-authorized outgoing edge. Operation was already moved
  -- from quarantined to the Case-derived nonterminal state in this transaction.
  IF OLD.state = 'reconcile_blocked' AND NEW.state = 'reconcile_wait' THEN
    IF NEW.version <> OLD.version + 1
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NOT NULL
       OR operation.remote_state NOT IN ('submit_unknown','accepted','running')
       OR operation.next_attempt_at IS NULL OR operation.next_attempt_at > statement_timestamp()
       OR operation.terminal_result_hash IS NOT NULL
       OR operation.last_error_code IS NOT NULL OR operation.last_error_detail IS NOT NULL
       OR NOT EXISTS (
         SELECT 1
         FROM release_delivery_reconciliation_cases AS resolution
         WHERE resolution.operation_id = operation.id
           AND resolution.operation_request_hash = operation.request_hash
           AND resolution.project_id = OLD.project_id
           AND resolution.run_kind = operation.kind
           AND resolution.run_id = OLD.id
           AND resolution.run_schema_version = OLD.schema_version
           AND resolution.expected_run_version = OLD.version
           AND resolution.resume_remote_state = operation.remote_state
           AND resolution.actor_id = NEW.updated_by
           AND resolution.created_txid = txid_current()
       ) THEN
      RAISE EXCEPTION 'reconcile_blocked may leave only through its exact immutable reconciliation Case'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.state = OLD.state THEN
    IF OLD.state IN ('claimed','submitting','reconciling','verifying')
       AND OLD.lease_expires_at > statement_timestamp()
       AND NEW.version = OLD.version
       AND NEW.fence_epoch = OLD.fence_epoch
       AND NEW.lease_worker_id IS NOT DISTINCT FROM OLD.lease_worker_id
       AND NEW.lease_epoch IS NOT DISTINCT FROM OLD.lease_epoch
       AND NEW.lease_expires_at > OLD.lease_expires_at
       AND NEW.started_at IS NOT DISTINCT FROM OLD.started_at
       AND NEW.finished_at IS NOT DISTINCT FROM OLD.finished_at THEN
      RETURN NEW;
    END IF;
    IF OLD.state IN ('claimed','reconciling','verifying')
       AND OLD.lease_expires_at <= statement_timestamp()
       AND NEW.version = OLD.version + 1
       AND NEW.fence_epoch = OLD.fence_epoch + 1
       AND NEW.lease_epoch = NEW.fence_epoch
       AND NEW.lease_worker_id IS NOT NULL
       AND NEW.lease_expires_at > statement_timestamp()
       AND NEW.started_at IS NOT DISTINCT FROM OLD.started_at
       AND NEW.finished_at IS NOT DISTINCT FROM OLD.finished_at THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'same-state delivery update must renew or reclaim one exact fence'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'release delivery transition must increment version exactly once'
      USING ERRCODE = '40001';
  END IF;

  IF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF operation.remote_state <> 'prepared'
       OR NEW.fence_epoch <> OLD.fence_epoch + 1
       OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL
       OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS NULL
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'release delivery claim must establish the first exact fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'reconcile_wait' AND NEW.state = 'reconciling' THEN
    IF operation.remote_state NOT IN ('submit_unknown','accepted','running')
       OR operation.next_attempt_at > statement_timestamp()
       OR NEW.fence_epoch <> OLD.fence_epoch + 1
       OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL
       OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'reconciliation claim requires due unknown remote authority and a new fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'submitting' AND NEW.state = 'reconciling'
     AND OLD.lease_expires_at <= statement_timestamp() THEN
    IF operation.remote_state NOT IN ('prepared','submit_unknown','accepted','running')
       OR NEW.fence_epoch <> OLD.fence_epoch + 1
       OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL
       OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'expired submission must be fenced into reconciliation'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'claimed' AND NEW.state = 'submitting' THEN
    IF operation.remote_state <> 'prepared'
       OR operation.submit_attempt_count <> 0
       OR OLD.lease_expires_at <= statement_timestamp()
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'submission requires one pristine Operation and the live claim fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'reconcile_wait' THEN
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state NOT IN ('submit_unknown','accepted','running')
       OR operation.next_attempt_at IS NULL
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL
       OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'unknown delivery outcome must release its lease into reconcile_wait'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'verifying' THEN
    SELECT * INTO result
    FROM release_delivery_operation_results
    WHERE operation_id = operation.id AND result_hash = operation.terminal_result_hash;
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'completed' OR NOT FOUND
       OR result.status <> 'completed' OR result.request_hash <> operation.request_hash
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
      RAISE EXCEPTION 'verifying requires the exact completed controller Result under the live fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'error' THEN
    SELECT * INTO result
    FROM release_delivery_operation_results
    WHERE operation_id = operation.id AND result_hash = operation.terminal_result_hash;
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'rejected' OR NOT FOUND
       OR result.status <> 'rejected' OR result.request_hash <> operation.request_hash
       OR result.worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR result.fence_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'active-to-error requires the exact immutable rejected controller Result'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'reconcile_blocked' THEN
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'quarantined'
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'reconcile_blocked requires explicit quarantined operation evidence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.state = 'cancelled' AND OLD.state IN ('queued','claimed') THEN
    IF operation.remote_state <> 'prepared' OR operation.submit_attempt_count <> 0
       OR (OLD.state = 'claimed' AND OLD.lease_expires_at <= statement_timestamp())
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'cancellation is safe only before any submission Attempt'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'verifying'
     AND (
       (TG_TABLE_NAME = 'release_preview_runs' AND NEW.state IN ('passed','failed'))
       OR (TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state IN ('healthy','failed'))
     ) THEN
    SELECT * INTO result
    FROM release_delivery_operation_results
    WHERE operation_id = operation.id AND result_hash = operation.terminal_result_hash;
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'completed' OR NOT FOUND
       OR result.status <> 'completed'
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'terminal delivery decision lost its exact completed operation fence'
        USING ERRCODE = '40001';
    END IF;

    IF TG_TABLE_NAME = 'release_preview_runs' AND NOT EXISTS (
      SELECT 1 FROM release_preview_receipts AS receipt
      WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
        AND receipt.controller_operation_id = operation.id
        AND receipt.controller_result_hash = result.result_hash
        AND receipt.decision = NEW.state
        AND receipt.provider = result.provider AND receipt.provider_ref = result.provider_ref
        AND receipt.checks = result.checks
    ) THEN
      RAISE EXCEPTION 'terminal Preview Run requires its exact operation-backed PreviewReceipt'
        USING ERRCODE = '40001';
    END IF;

    IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'failed'
       AND NOT EXISTS (
         SELECT 1 FROM release_production_receipts AS receipt
         WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
           AND receipt.controller_operation_id = operation.id
           AND receipt.controller_result_hash = result.result_hash
           AND receipt.decision = 'failed'
           AND receipt.provider = result.provider AND receipt.provider_ref = result.provider_ref
           AND NULLIF(receipt.public_url, '') IS NOT DISTINCT FROM result.public_url
           AND receipt.checks = result.checks
       ) THEN
      RAISE EXCEPTION 'failed production Run requires its exact operation-backed failed Receipt'
        USING ERRCODE = '40001';
    END IF;

    IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'healthy'
       AND NOT EXISTS (
         SELECT 1
         FROM release_production_receipts AS receipt
         JOIN release_deployment_revisions AS revision
           ON revision.production_receipt_id = receipt.id
          AND revision.production_receipt_hash = receipt.payload_hash
         WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
           AND receipt.controller_operation_id = operation.id
           AND receipt.controller_result_hash = result.result_hash
           AND receipt.decision = 'passed'
           AND receipt.provider = result.provider AND receipt.provider_ref = result.provider_ref
           AND NULLIF(receipt.public_url, '') IS NOT DISTINCT FROM result.public_url
           AND receipt.checks = result.checks
           AND revision.run_id = NEW.id
           AND revision.controller_operation_id = operation.id
           AND revision.controller_result_hash = result.result_hash
       ) THEN
      RAISE EXCEPTION 'healthy production Run requires exact operation-backed Receipt and Revision'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'invalid release delivery v2 state transition'
    USING ERRCODE = '40001';
END;
$$;
