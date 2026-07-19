-- Immutable Candidate VerificationReceipt facts. A receipt, its checks, its
-- obligation coverage, and the terminal Run transition must commit together.
-- Child rows can be written only in the transaction that creates the parent.

CREATE OR REPLACE FUNCTION verification_sorted_string_array_is_valid(
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
  SELECT verification_json_string_array_is_valid(
      values_json, minimum_count, maximum_count, maximum_length
    )
    AND NOT EXISTS (
      SELECT 1
      FROM (
        SELECT item.value #>> '{}' AS value,
               lag(item.value #>> '{}') OVER (ORDER BY item.ordinality) AS previous_value
        FROM jsonb_array_elements(values_json) WITH ORDINALITY AS item(value, ordinality)
      ) AS ordered
      WHERE ordered.previous_value >= ordered.value
    )
$$;

CREATE OR REPLACE FUNCTION verification_uuid_array_is_valid(
  values_json jsonb,
  minimum_count integer,
  maximum_count integer
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array'
      OR jsonb_array_length(values_json) NOT BETWEEN minimum_count AND maximum_count THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'string'
         OR item.value #>> '{}' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    ) AND NOT EXISTS (
      SELECT 1
      FROM (
        SELECT item.value #>> '{}' AS value,
               lag(item.value #>> '{}') OVER (ORDER BY item.ordinality) AS previous_value
        FROM jsonb_array_elements(values_json) WITH ORDINALITY AS item(value, ordinality)
      ) AS ordered
      WHERE ordered.previous_value >= ordered.value
    )
  END
$$;

CREATE OR REPLACE FUNCTION verification_argv_is_valid(values_json jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array'
      OR jsonb_array_length(values_json) NOT BETWEEN 1 AND 64 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'string'
         OR length(item.value #>> '{}') NOT BETWEEN 1 AND 4096
    )
  END
$$;

CREATE OR REPLACE FUNCTION verification_blob_reference_is_valid(
  value jsonb,
  expected_owner_id uuid
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT jsonb_typeof(value) = 'object'
    AND value ?& ARRAY['store', 'ownerId', 'ref', 'contentHash', 'byteSize']
    AND (SELECT count(*) = 5 FROM jsonb_object_keys(value))
    AND value->>'store' = btrim(value->>'store')
    AND length(value->>'store') BETWEEN 1 AND 80
    AND value->>'ownerId' = expected_owner_id::text
    AND value->>'ref' = btrim(value->>'ref')
    AND length(value->>'ref') BETWEEN 1 AND 2000
    AND value->>'contentHash' ~ '^sha256:[0-9a-f]{64}$'
    AND value->>'byteSize' ~ '^[0-9]+$'
    AND length(value->>'byteSize') <= 12
    AND (value->>'byteSize')::bigint BETWEEN 0 AND 1073741824
$$;

CREATE OR REPLACE FUNCTION verification_diagnostics_are_valid(values_json jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(values_json) <> 'array'
      OR jsonb_array_length(values_json) > 4096 THEN false
    ELSE NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(values_json) AS item(value)
      WHERE jsonb_typeof(item.value) <> 'object'
         OR NOT (item.value ?& ARRAY['id', 'code', 'severity', 'message', 'line', 'column'])
         OR EXISTS (
           SELECT 1 FROM jsonb_object_keys(item.value) AS key(name)
           WHERE key.name NOT IN ('id', 'code', 'severity', 'message', 'path', 'line', 'column', 'suggestion')
         )
         OR item.value->>'id' !~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
         OR item.value->>'code' !~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
         OR item.value->>'severity' NOT IN ('blocker', 'warning', 'info')
         OR item.value->>'message' <> btrim(item.value->>'message')
         OR length(item.value->>'message') NOT BETWEEN 1 AND 4000
         OR jsonb_typeof(item.value->'line') <> 'number'
         OR (item.value->'line')::text !~ '^[0-9]+$'
         OR length((item.value->'line')::text) > 9
         OR jsonb_typeof(item.value->'column') <> 'number'
         OR (item.value->'column')::text !~ '^[0-9]+$'
         OR length((item.value->'column')::text) > 9
         OR (item.value ? 'path' AND item.value->>'path' <> ''
           AND NOT repository_path_is_safe(item.value->>'path'))
         OR (item.value ? 'suggestion' AND length(item.value->>'suggestion') > 4000)
    ) AND (
      SELECT count(*) = count(DISTINCT item.value->>'id')
      FROM jsonb_array_elements(values_json) AS item(value)
    )
  END
$$;

CREATE TABLE candidate_verification_receipts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'verification-receipt/v1'),
  scope text NOT NULL CHECK (scope = 'candidate'),
  run_id uuid NOT NULL UNIQUE REFERENCES candidate_verification_runs(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  plan_id uuid NOT NULL,
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  sandbox_session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  candidate_snapshot_id uuid NOT NULL REFERENCES candidate_snapshots(id) ON DELETE RESTRICT,
  candidate_version bigint NOT NULL CHECK (candidate_version > 0),
  journal_sequence bigint NOT NULL CHECK (journal_sequence >= 0),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  writer_lease_epoch bigint NOT NULL CHECK (writer_lease_epoch > 0),
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
  attempt_ids jsonb NOT NULL CHECK (verification_uuid_array_is_valid(attempt_ids, 1, 64)),
  check_count integer NOT NULL CHECK (check_count BETWEEN 1 AND 512),
  coverage_count integer NOT NULL CHECK (coverage_count BETWEEN 1 AND 1024),
  must_count integer NOT NULL CHECK (must_count >= 1 AND must_count <= coverage_count),
  must_passed_count integer NOT NULL CHECK (must_passed_count BETWEEN 0 AND must_count),
  blocker_count integer NOT NULL CHECK (blocker_count >= 0),
  warning_count integer NOT NULL CHECK (warning_count >= 0),
  decision text NOT NULL CHECK (decision IN ('passed', 'failed', 'error')),
  execution_error text CHECK (
    execution_error IS NULL OR (
      execution_error = btrim(execution_error) AND length(execution_error) BETWEEN 1 AND 2000
    )
  ),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  creation_transaction_id bigint NOT NULL DEFAULT txid_current(),
  CONSTRAINT candidate_verification_receipt_id_run_unique UNIQUE (id, run_id),
  CONSTRAINT candidate_verification_receipt_exact_unique UNIQUE (id, payload_hash),
  CONSTRAINT candidate_verification_receipt_plan_exact_fk
    FOREIGN KEY (plan_id, plan_hash)
    REFERENCES candidate_verification_plans(id, plan_hash) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_receipt_full_stack_exact_fk
    FOREIGN KEY (full_stack_template_id, full_stack_template_hash)
    REFERENCES full_stack_template_releases(id, content_hash) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_receipt_profile_exact_fk
    FOREIGN KEY (
      verification_profile_id,
      verification_profile_version,
      verification_profile_hash
    ) REFERENCES verification_profile_versions(profile_id, version, content_hash)
    ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_receipt_decision_shape CHECK (
    (decision = 'passed' AND execution_error IS NULL
      AND must_passed_count = must_count AND blocker_count = 0)
    OR (decision = 'failed' AND execution_error IS NULL AND blocker_count > 0)
    OR (decision = 'error' AND execution_error IS NOT NULL AND blocker_count > 0)
  )
);

CREATE INDEX candidate_verification_receipts_subject_idx
  ON candidate_verification_receipts (
    project_id, candidate_id, candidate_snapshot_id, decision, created_at DESC
  );

CREATE TABLE candidate_verification_checks (
  receipt_id uuid NOT NULL,
  run_id uuid NOT NULL,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 511),
  check_id text NOT NULL CHECK (
    check_id = btrim(check_id)
    AND check_id ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
  ),
  kind text NOT NULL CHECK (
    kind = btrim(kind)
    AND kind ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
  ),
  service_id text CHECK (
    service_id IS NULL OR (
      service_id = btrim(service_id)
      AND service_id ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
    )
  ),
  command_id text CHECK (
    command_id IS NULL OR (
      command_id = btrim(command_id)
      AND command_id ~ '^[A-Za-z0-9][A-Za-z0-9._:~/@-]{0,159}$'
    )
  ),
  required boolean NOT NULL,
  status text NOT NULL CHECK (status IN ('passed', 'failed', 'error')),
  attempt_id uuid NOT NULL,
  verifier_image_digest text NOT NULL CHECK (
    verifier_image_digest ~ '^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,255}@sha256:[0-9a-f]{64}$'
  ),
  argv jsonb NOT NULL CHECK (verification_argv_is_valid(argv)),
  working_directory text NOT NULL CHECK (
    working_directory = '.' OR repository_path_is_safe(working_directory)
  ),
  exit_code integer,
  started_at timestamptz NOT NULL,
  completed_at timestamptz NOT NULL,
  duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
  attempt_count bigint NOT NULL CHECK (attempt_count BETWEEN 1 AND 64),
  stdout jsonb,
  stderr jsonb,
  truncated boolean NOT NULL,
  redaction_count integer NOT NULL CHECK (redaction_count >= 0),
  oracle_ids jsonb NOT NULL CHECK (verification_sorted_string_array_is_valid(oracle_ids, 0, 256, 160)),
  acceptance_criterion_ids jsonb NOT NULL CHECK (
    verification_sorted_string_array_is_valid(acceptance_criterion_ids, 0, 256, 160)
  ),
  obligation_ids jsonb NOT NULL CHECK (
    verification_sorted_string_array_is_valid(obligation_ids, 0, 256, 160)
  ),
  diagnostics jsonb NOT NULL CHECK (verification_diagnostics_are_valid(diagnostics)),
  PRIMARY KEY (receipt_id, check_id),
  CONSTRAINT candidate_verification_check_ordinal_unique UNIQUE (receipt_id, ordinal),
  CONSTRAINT candidate_verification_check_receipt_run_fk
    FOREIGN KEY (receipt_id, run_id)
    REFERENCES candidate_verification_receipts(id, run_id) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_check_attempt_run_fk
    FOREIGN KEY (attempt_id, run_id)
    REFERENCES candidate_verification_attempts(id, run_id) ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_check_exit_shape CHECK (
    (status = 'passed' AND exit_code = 0)
    OR (status = 'failed' AND exit_code IS NOT NULL)
    OR (status = 'error' AND exit_code IS NULL)
  ),
  CONSTRAINT candidate_verification_check_time_shape CHECK (
    completed_at >= started_at
    AND duration_ms = floor(extract(epoch FROM (completed_at - started_at)) * 1000)::bigint
  )
);

CREATE INDEX candidate_verification_checks_attempt_idx
  ON candidate_verification_checks (attempt_id, receipt_id, ordinal);

CREATE TABLE candidate_verification_obligation_coverage (
  receipt_id uuid NOT NULL REFERENCES candidate_verification_receipts(id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 1023),
  build_contract_id uuid NOT NULL,
  obligation_id text NOT NULL,
  level text NOT NULL CHECK (level IN ('must', 'should')),
  oracle_ids jsonb NOT NULL CHECK (verification_sorted_string_array_is_valid(oracle_ids, 1, 256, 160)),
  check_ids jsonb NOT NULL CHECK (verification_sorted_string_array_is_valid(check_ids, 0, 512, 160)),
  status text NOT NULL CHECK (status IN ('passed', 'missing')),
  PRIMARY KEY (receipt_id, obligation_id),
  CONSTRAINT candidate_verification_coverage_ordinal_unique UNIQUE (receipt_id, ordinal),
  CONSTRAINT candidate_verification_coverage_obligation_fk
    FOREIGN KEY (build_contract_id, obligation_id)
    REFERENCES application_build_contract_obligations(contract_id, obligation_id)
    ON DELETE RESTRICT,
  CONSTRAINT candidate_verification_coverage_status_shape CHECK (
    (status = 'passed' AND jsonb_array_length(check_ids) > 0)
    OR (status = 'missing' AND jsonb_array_length(check_ids) = 0)
  )
);

CREATE OR REPLACE FUNCTION validate_candidate_verification_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM candidate_verification_runs AS run
    JOIN candidate_verification_plans AS plan
      ON plan.id = run.plan_id
     AND plan.plan_hash = run.plan_hash
     AND plan.project_id = run.project_id
    JOIN verification_profile_policies AS policy
      ON policy.profile_id = plan.verification_profile_id
     AND policy.profile_version = plan.verification_profile_version
     AND policy.profile_hash = plan.verification_profile_hash
     AND policy.state = 'active'
    WHERE run.id = NEW.run_id
      AND run.project_id = NEW.project_id
      AND run.plan_id = NEW.plan_id
      AND run.plan_hash = NEW.plan_hash
      AND run.state IN ('collecting', NEW.decision)
      AND plan.sandbox_session_id = NEW.sandbox_session_id
      AND plan.candidate_id = NEW.candidate_id
      AND plan.candidate_snapshot_id = NEW.candidate_snapshot_id
      AND plan.candidate_version = NEW.candidate_version
      AND plan.journal_sequence = NEW.journal_sequence
      AND plan.session_epoch = NEW.session_epoch
      AND plan.writer_lease_epoch = NEW.writer_lease_epoch
      AND plan.tree_hash = NEW.tree_hash
      AND plan.build_manifest_id = NEW.build_manifest_id
      AND plan.build_manifest_hash = NEW.build_manifest_hash
      AND plan.build_contract_id = NEW.build_contract_id
      AND plan.build_contract_hash = NEW.build_contract_hash
      AND plan.full_stack_template_id = NEW.full_stack_template_id
      AND plan.full_stack_template_hash = NEW.full_stack_template_hash
      AND plan.verification_profile_id = NEW.verification_profile_id
      AND plan.verification_profile_version = NEW.verification_profile_version
      AND plan.verification_profile_hash = NEW.verification_profile_hash
      AND plan.check_count = NEW.check_count
      AND plan.obligation_count = NEW.coverage_count
  ) THEN
    RAISE EXCEPTION 'VerificationReceipt must bind one exact collecting Run, Plan, subject, lineage, and active profile'
      USING ERRCODE = '40001';
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_receipt_insert_guard
BEFORE INSERT ON candidate_verification_receipts
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_receipt_insert();

CREATE OR REPLACE FUNCTION validate_candidate_verification_check_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent candidate_verification_receipts%ROWTYPE;
BEGIN
  SELECT * INTO parent
  FROM candidate_verification_receipts
  WHERE id = NEW.receipt_id;
  IF NOT FOUND OR parent.creation_transaction_id <> txid_current()
     OR parent.run_id <> NEW.run_id
     OR NOT parent.attempt_ids ? NEW.attempt_id::text THEN
    RAISE EXCEPTION 'VerificationReceipt is sealed; checks require its exact creation transaction and Attempt'
      USING ERRCODE = '55000';
  END IF;
  IF (NEW.stdout IS NOT NULL AND NOT verification_blob_reference_is_valid(NEW.stdout, NEW.attempt_id))
     OR (NEW.stderr IS NOT NULL AND NOT verification_blob_reference_is_valid(NEW.stderr, NEW.attempt_id)) THEN
    RAISE EXCEPTION 'Verification check log reference is not exact or bounded'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_check_insert_guard
BEFORE INSERT ON candidate_verification_checks
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_check_insert();

CREATE OR REPLACE FUNCTION validate_candidate_verification_coverage_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent candidate_verification_receipts%ROWTYPE;
BEGIN
  SELECT * INTO parent
  FROM candidate_verification_receipts
  WHERE id = NEW.receipt_id;
  IF NOT FOUND OR parent.creation_transaction_id <> txid_current()
     OR parent.build_contract_id <> NEW.build_contract_id THEN
    RAISE EXCEPTION 'VerificationReceipt is sealed; coverage requires its exact creation transaction and BuildContract'
      USING ERRCODE = '55000';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM application_build_contract_obligations AS obligation
    WHERE obligation.contract_id = NEW.build_contract_id
      AND obligation.obligation_id = NEW.obligation_id
      AND obligation.level = NEW.level
      AND obligation.oracle_ids = NEW.oracle_ids
      AND obligation.status IN ('ready', 'waived')
  ) THEN
    RAISE EXCEPTION 'Verification coverage does not match the exact BuildContract obligation'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_coverage_insert_guard
BEFORE INSERT ON candidate_verification_obligation_coverage
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_coverage_insert();

CREATE OR REPLACE FUNCTION prevent_candidate_verification_receipt_fact_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'VerificationReceipt, check, and coverage facts are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_verification_receipt_immutable
BEFORE UPDATE OR DELETE ON candidate_verification_receipts
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_verification_receipt_fact_mutation();

CREATE TRIGGER candidate_verification_check_immutable
BEFORE UPDATE OR DELETE ON candidate_verification_checks
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_verification_receipt_fact_mutation();

CREATE TRIGGER candidate_verification_coverage_immutable
BEFORE UPDATE OR DELETE ON candidate_verification_obligation_coverage
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_verification_receipt_fact_mutation();

CREATE OR REPLACE FUNCTION validate_candidate_verification_receipt_complete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_receipt_id uuid;
  receipt candidate_verification_receipts%ROWTYPE;
  plan candidate_verification_plans%ROWTYPE;
  actual_check_count integer;
  actual_coverage_count integer;
  actual_attempt_count integer;
  actual_must_count integer;
  actual_must_passed_count integer;
  actual_blocker_count integer;
  actual_warning_count integer;
  has_execution_error boolean;
  expected_decision text;
  coverage_record record;
  expected_check_ids jsonb;
  expected_coverage_status text;
BEGIN
  IF TG_TABLE_NAME = 'candidate_verification_receipts' THEN
    target_receipt_id := NEW.id;
  ELSE
    target_receipt_id := NEW.receipt_id;
  END IF;
  SELECT * INTO receipt
  FROM candidate_verification_receipts
  WHERE id = target_receipt_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  SELECT * INTO plan
  FROM candidate_verification_plans
  WHERE id = receipt.plan_id AND plan_hash = receipt.plan_hash;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'VerificationReceipt lost its exact immutable Plan'
      USING ERRCODE = '23514';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM candidate_verification_runs AS run
    JOIN verification_profile_policies AS policy
      ON policy.profile_id = receipt.verification_profile_id
     AND policy.profile_version = receipt.verification_profile_version
     AND policy.profile_hash = receipt.verification_profile_hash
     AND policy.state = 'active'
    WHERE run.id = receipt.run_id
      AND run.project_id = receipt.project_id
      AND run.plan_id = receipt.plan_id
      AND run.plan_hash = receipt.plan_hash
      AND run.state = receipt.decision
  ) THEN
    RAISE EXCEPTION 'VerificationReceipt final decision does not match its terminal Run and active profile'
      USING ERRCODE = '23514';
  END IF;

  SELECT count(*) INTO actual_attempt_count
  FROM candidate_verification_attempts AS attempt
  WHERE attempt.run_id = receipt.run_id;
  IF actual_attempt_count <> jsonb_array_length(receipt.attempt_ids)
     OR EXISTS (
       SELECT 1
       FROM candidate_verification_attempts AS attempt
       WHERE attempt.run_id = receipt.run_id
         AND (attempt.state NOT IN ('passed', 'failed', 'error', 'cancelled', 'timed_out')
           OR NOT receipt.attempt_ids ? attempt.id::text)
     )
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements_text(receipt.attempt_ids) AS listed(id)
       WHERE NOT EXISTS (
         SELECT 1 FROM candidate_verification_attempts AS attempt
         WHERE attempt.id = listed.id::uuid AND attempt.run_id = receipt.run_id
       )
     ) THEN
    RAISE EXCEPTION 'VerificationReceipt must list every exact terminal Attempt for its Run'
      USING ERRCODE = '23514';
  END IF;

  SELECT count(*) INTO actual_check_count
  FROM candidate_verification_checks
  WHERE receipt_id = receipt.id;
  IF actual_check_count <> receipt.check_count
     OR actual_check_count <> plan.check_count
     OR EXISTS (
       SELECT 1
       FROM candidate_verification_checks AS check_result
       WHERE check_result.receipt_id = receipt.id
         AND (
           NOT plan.check_ids ? check_result.check_id
           OR check_result.required IS DISTINCT FROM (plan.required_check_ids ? check_result.check_id)
           OR NOT receipt.attempt_ids ? check_result.attempt_id::text
         )
     )
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements_text(plan.check_ids) AS expected(id)
       WHERE NOT EXISTS (
         SELECT 1 FROM candidate_verification_checks AS check_result
         WHERE check_result.receipt_id = receipt.id AND check_result.check_id = expected.id
       )
     )
     OR EXISTS (
       SELECT 1
       FROM (
         SELECT ordinal, check_id,
                row_number() OVER (ORDER BY check_id) - 1 AS expected_ordinal
         FROM candidate_verification_checks
         WHERE receipt_id = receipt.id
       ) AS ordered
       WHERE ordered.ordinal <> ordered.expected_ordinal
     ) THEN
    RAISE EXCEPTION 'VerificationReceipt check projection is incomplete, reordered, or differs from its Plan'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM candidate_verification_checks AS check_result,
         LATERAL jsonb_array_elements_text(check_result.obligation_ids) AS listed(id)
    WHERE check_result.receipt_id = receipt.id
      AND NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(plan.obligations) AS obligation(value)
        WHERE obligation.value->>'id' = listed.id
      )
  ) OR EXISTS (
    SELECT 1
    FROM candidate_verification_checks AS check_result,
         LATERAL jsonb_array_elements_text(check_result.oracle_ids) AS listed(id)
    WHERE check_result.receipt_id = receipt.id
      AND NOT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(plan.obligations) AS obligation(value),
             LATERAL jsonb_array_elements_text(obligation.value->'oracleIds') AS oracle(id)
        WHERE oracle.id = listed.id
      )
  ) THEN
    RAISE EXCEPTION 'VerificationReceipt check references an unknown obligation or oracle'
      USING ERRCODE = '23514';
  END IF;

  SELECT count(*) INTO actual_coverage_count
  FROM candidate_verification_obligation_coverage
  WHERE receipt_id = receipt.id;
  IF actual_coverage_count <> receipt.coverage_count
     OR actual_coverage_count <> plan.obligation_count
     OR EXISTS (
       SELECT 1
       FROM (
         SELECT ordinal, obligation_id,
                row_number() OVER (ORDER BY obligation_id) - 1 AS expected_ordinal
         FROM candidate_verification_obligation_coverage
         WHERE receipt_id = receipt.id
       ) AS ordered
       WHERE ordered.ordinal <> ordered.expected_ordinal
     ) THEN
    RAISE EXCEPTION 'VerificationReceipt obligation coverage projection is incomplete or reordered'
      USING ERRCODE = '23514';
  END IF;

  FOR coverage_record IN
    SELECT * FROM candidate_verification_obligation_coverage
    WHERE receipt_id = receipt.id
    ORDER BY obligation_id
  LOOP
    SELECT COALESCE(jsonb_agg(check_result.check_id ORDER BY check_result.check_id), '[]'::jsonb)
      INTO expected_check_ids
    FROM candidate_verification_checks AS check_result
    WHERE check_result.receipt_id = receipt.id
      AND check_result.status = 'passed'
      AND check_result.obligation_ids ? coverage_record.obligation_id
      AND EXISTS (
        SELECT 1
        FROM jsonb_array_elements_text(check_result.oracle_ids) AS check_oracle(id)
        JOIN jsonb_array_elements_text(coverage_record.oracle_ids) AS required_oracle(id)
          ON required_oracle.id = check_oracle.id
      );
    expected_coverage_status := CASE
      WHEN jsonb_array_length(expected_check_ids) > 0 THEN 'passed'
      ELSE 'missing'
    END;
    IF coverage_record.check_ids <> expected_check_ids
       OR coverage_record.status <> expected_coverage_status THEN
      RAISE EXCEPTION 'VerificationReceipt obligation coverage is not derived from passed exact checks'
        USING ERRCODE = '23514';
    END IF;
  END LOOP;

  SELECT count(*) FILTER (WHERE level = 'must'),
         count(*) FILTER (WHERE level = 'must' AND status = 'passed')
    INTO actual_must_count, actual_must_passed_count
  FROM candidate_verification_obligation_coverage
  WHERE receipt_id = receipt.id;

  SELECT count(*) FILTER (WHERE required AND status <> 'passed'),
         count(*) FILTER (WHERE NOT required AND status <> 'passed'),
         bool_or(status = 'error')
    INTO actual_blocker_count, actual_warning_count, has_execution_error
  FROM candidate_verification_checks
  WHERE receipt_id = receipt.id;
  SELECT actual_blocker_count
           + count(*) FILTER (WHERE diagnostic.value->>'severity' = 'blocker'),
         actual_warning_count
           + count(*) FILTER (WHERE diagnostic.value->>'severity' = 'warning')
    INTO actual_blocker_count, actual_warning_count
  FROM candidate_verification_checks AS check_result
  CROSS JOIN LATERAL jsonb_array_elements(check_result.diagnostics) AS diagnostic(value)
  WHERE check_result.receipt_id = receipt.id;
  SELECT actual_blocker_count + count(*)
    INTO actual_blocker_count
  FROM candidate_verification_obligation_coverage
  WHERE receipt_id = receipt.id AND level = 'must' AND status <> 'passed';

  has_execution_error := COALESCE(has_execution_error, false) OR receipt.execution_error IS NOT NULL;
  IF has_execution_error AND actual_blocker_count = 0 THEN
    actual_blocker_count := 1;
  END IF;
  expected_decision := CASE
    WHEN has_execution_error THEN 'error'
    WHEN actual_blocker_count > 0 THEN 'failed'
    ELSE 'passed'
  END;
  IF receipt.must_count <> actual_must_count
     OR receipt.must_passed_count <> actual_must_passed_count
     OR receipt.blocker_count <> actual_blocker_count
     OR receipt.warning_count <> actual_warning_count
     OR receipt.decision <> expected_decision
     OR (receipt.decision = 'error' AND receipt.execution_error IS NULL)
     OR (receipt.decision <> 'error' AND receipt.execution_error IS NOT NULL) THEN
    RAISE EXCEPTION 'VerificationReceipt decision and summary counts are not derived from exact facts'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER candidate_verification_receipt_complete_guard
AFTER INSERT ON candidate_verification_receipts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_receipt_complete();

CREATE CONSTRAINT TRIGGER candidate_verification_check_complete_guard
AFTER INSERT ON candidate_verification_checks
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_receipt_complete();

CREATE CONSTRAINT TRIGGER candidate_verification_coverage_complete_guard
AFTER INSERT ON candidate_verification_obligation_coverage
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_receipt_complete();

CREATE OR REPLACE FUNCTION validate_candidate_verification_run_terminal_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.state IN ('passed', 'failed', 'error') AND NOT EXISTS (
    SELECT 1
    FROM candidate_verification_receipts AS receipt
    WHERE receipt.run_id = NEW.id
      AND receipt.project_id = NEW.project_id
      AND receipt.plan_id = NEW.plan_id
      AND receipt.plan_hash = NEW.plan_hash
      AND receipt.decision = NEW.state
  ) THEN
    RAISE EXCEPTION 'Terminal Candidate VerificationRun requires its exact immutable Receipt'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER candidate_verification_run_terminal_receipt_guard
AFTER UPDATE ON candidate_verification_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_verification_run_terminal_receipt();

COMMENT ON TABLE candidate_verification_receipts IS
  'Immutable Candidate quality decision bound to one exact Plan, subject, lineage, profile, and all Attempts.';
COMMENT ON TABLE candidate_verification_checks IS
  'Immutable executed check facts including digest-pinned verifier, argv, exit semantics, logs, and diagnostics.';
COMMENT ON TABLE candidate_verification_obligation_coverage IS
  'Immutable BuildContract obligation coverage derived only from passed exact checks.';
