-- Canonical execution is independently worker-fenced. Its Attempt/check/
-- coverage facts cannot be borrowed from Candidate verification, and a passed
-- Receipt is validated again at deferred commit time.

CREATE OR REPLACE FUNCTION validate_canonical_verification_run_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.state <> 'queued' OR NEW.version <> 1 OR NEW.fence_epoch <> 0
     OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
     OR NEW.lease_expires_at IS NOT NULL OR NEW.terminal_reason IS NOT NULL
     OR NEW.execution_error IS NOT NULL OR NEW.started_at IS NOT NULL
     OR NEW.finished_at IS NOT NULL THEN
    RAISE EXCEPTION 'Canonical VerificationRun must start queued at version 1 and fence 0'
      USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM canonical_verification_plans AS plan
    JOIN verification_profile_policies AS policy
      ON policy.profile_id = plan.verification_profile_id
     AND policy.profile_version = plan.verification_profile_version
     AND policy.profile_hash = plan.verification_profile_hash
     AND policy.state = 'active'
    WHERE plan.id = NEW.plan_id AND plan.plan_hash = NEW.plan_hash
      AND plan.project_id = NEW.project_id
  ) THEN
    RAISE EXCEPTION 'Canonical VerificationRun requires an exact Plan with an active profile'
      USING ERRCODE = '23503';
  END IF;
  NEW.updated_by := NEW.created_by;
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  RETURN NEW;
END;
$$;

CREATE TRIGGER canonical_verification_run_insert_guard
BEFORE INSERT ON canonical_verification_runs
FOR EACH ROW EXECUTE FUNCTION validate_canonical_verification_run_insert();

CREATE OR REPLACE FUNCTION guard_canonical_verification_run_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  active_states constant text[] := ARRAY['claimed','materializing','preparing','running','collecting'];
  terminal_states constant text[] := ARRAY['passed','failed','error','cancelled','timed_out'];
  normal_advance boolean :=
    (OLD.state = 'claimed' AND NEW.state = 'materializing')
    OR (OLD.state = 'materializing' AND NEW.state = 'preparing')
    OR (OLD.state = 'preparing' AND NEW.state = 'running')
    OR (OLD.state = 'running' AND NEW.state = 'collecting')
    OR (OLD.state = 'collecting' AND NEW.state IN ('passed','failed'))
    OR (OLD.state <> ALL(terminal_states) AND NEW.state IN ('error','timed_out'));
BEGIN
  IF TG_OP = 'DELETE' OR OLD.state = ANY(terminal_states) THEN
    RAISE EXCEPTION 'Terminal Canonical VerificationRun facts are immutable'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(
    NEW.id, NEW.schema_version, NEW.project_id, NEW.plan_id, NEW.plan_hash,
    NEW.request_key, NEW.request_hash, NEW.reason, NEW.created_by, NEW.created_at
  ) IS DISTINCT FROM ROW(
    OLD.id, OLD.schema_version, OLD.project_id, OLD.plan_id, OLD.plan_hash,
    OLD.request_key, OLD.request_hash, OLD.reason, OLD.created_by, OLD.created_at
  ) OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'Canonical VerificationRun transition lost identity or version CAS'
      USING ERRCODE = '40001';
  END IF;
  IF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF NEW.fence_epoch <> 1 OR NEW.lease_epoch <> 1 OR NEW.lease_worker_id IS NULL
       OR NEW.lease_expires_at <= statement_timestamp() OR NEW.started_at IS NULL
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Canonical VerificationRun claim must establish a live worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = ANY(active_states) AND NEW.state = 'claimed'
        AND OLD.lease_expires_at <= statement_timestamp() THEN
    IF NEW.fence_epoch <> OLD.fence_epoch + 1 OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Canonical VerificationRun reclaim must advance its worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = NEW.state AND OLD.state = ANY(active_states) THEN
    IF OLD.lease_expires_at <= statement_timestamp() OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at <= OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Canonical VerificationRun heartbeat lost its worker fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF normal_advance THEN
    IF NEW.fence_epoch <> OLD.fence_epoch
       OR OLD.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR (NEW.state <> ALL(terminal_states) AND (
         NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
         OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       )) THEN
      RAISE EXCEPTION 'Canonical VerificationRun transition lost its live worker fence'
        USING ERRCODE = '40001';
    END IF;
    IF NEW.state = ANY(terminal_states) THEN
      IF NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
         OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Terminal Canonical VerificationRun must close its lease and finish time'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'Active Canonical VerificationRun changed unrelated lease or finish facts'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'Invalid Canonical VerificationRun state transition'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER canonical_verification_run_transition_guard
BEFORE UPDATE OR DELETE ON canonical_verification_runs
FOR EACH ROW EXECUTE FUNCTION guard_canonical_verification_run_transition();

CREATE TABLE canonical_verification_attempts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'canonical-verification-attempt/v1'),
  run_id uuid NOT NULL REFERENCES canonical_verification_runs(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  plan_id uuid NOT NULL,
  plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 64),
  state text NOT NULL CHECK (state IN (
    'queued','claimed','materializing','preparing','running','collecting',
    'passed','failed','error','cancelled','timed_out'
  )),
  version bigint NOT NULL CHECK (version > 0),
  fence_epoch bigint NOT NULL CHECK (fence_epoch >= 0),
  lease_worker_id text,
  lease_epoch bigint,
  lease_expires_at timestamptz,
  terminal_reason text,
  execution_error text,
  started_at timestamptz,
  finished_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT canonical_verification_attempt_run_ordinal_unique UNIQUE (run_id, ordinal),
  CONSTRAINT canonical_verification_attempt_plan_exact_fk
    FOREIGN KEY (plan_id, plan_hash) REFERENCES canonical_verification_plans(id, plan_hash) ON DELETE RESTRICT,
  CONSTRAINT canonical_verification_attempt_lease_shape CHECK (
    (state = 'queued' AND fence_epoch = 0 AND lease_worker_id IS NULL AND lease_epoch IS NULL AND lease_expires_at IS NULL)
    OR (state IN ('claimed','materializing','preparing','running','collecting')
      AND fence_epoch > 0 AND lease_worker_id IS NOT NULL AND lease_epoch = fence_epoch AND lease_expires_at IS NOT NULL)
    OR (state IN ('passed','failed','error','cancelled','timed_out')
      AND fence_epoch > 0 AND lease_worker_id IS NULL AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  ),
  CONSTRAINT canonical_verification_attempt_terminal_shape CHECK (
    (state IN ('passed','failed','error','cancelled','timed_out') AND finished_at IS NOT NULL)
    OR (state NOT IN ('passed','failed','error','cancelled','timed_out') AND finished_at IS NULL)
  )
);

CREATE INDEX canonical_verification_attempts_run_idx
  ON canonical_verification_attempts (run_id, ordinal);

CREATE OR REPLACE FUNCTION validate_canonical_verification_attempt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.state <> 'queued' OR NEW.version <> 1 OR NEW.fence_epoch <> 0
     OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
     OR NEW.lease_expires_at IS NOT NULL OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL THEN
    RAISE EXCEPTION 'Canonical VerificationAttempt must start queued at version 1 and fence 0'
      USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM canonical_verification_runs AS run
    WHERE run.id = NEW.run_id AND run.project_id = NEW.project_id
      AND run.plan_id = NEW.plan_id AND run.plan_hash = NEW.plan_hash
      AND run.state = 'claimed'
  ) THEN
    RAISE EXCEPTION 'Canonical VerificationAttempt requires its exact claimed Run'
      USING ERRCODE = '40001';
  END IF;
  NEW.updated_by := NEW.created_by;
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  RETURN NEW;
END;
$$;

CREATE TRIGGER canonical_verification_attempt_insert_guard
BEFORE INSERT ON canonical_verification_attempts
FOR EACH ROW EXECUTE FUNCTION validate_canonical_verification_attempt_insert();

CREATE OR REPLACE FUNCTION guard_canonical_verification_attempt_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent_run canonical_verification_runs%ROWTYPE;
  active_states constant text[] := ARRAY['claimed','materializing','preparing','running','collecting'];
  terminal_states constant text[] := ARRAY['passed','failed','error','cancelled','timed_out'];
  normal_advance boolean :=
    (OLD.state = 'claimed' AND NEW.state = 'materializing')
    OR (OLD.state = 'materializing' AND NEW.state = 'preparing')
    OR (OLD.state = 'preparing' AND NEW.state = 'running')
    OR (OLD.state = 'running' AND NEW.state = 'collecting')
    OR (OLD.state = 'collecting' AND NEW.state IN ('passed','failed'))
    OR (OLD.state <> ALL(terminal_states) AND NEW.state IN ('error','timed_out'));
BEGIN
  IF TG_OP = 'DELETE' OR OLD.state = ANY(terminal_states) THEN
    RAISE EXCEPTION 'Terminal Canonical VerificationAttempt facts are immutable'
      USING ERRCODE = '55000';
  END IF;
  IF ROW(NEW.id, NEW.schema_version, NEW.run_id, NEW.project_id, NEW.plan_id, NEW.plan_hash,
         NEW.ordinal, NEW.created_by, NEW.created_at)
     IS DISTINCT FROM
     ROW(OLD.id, OLD.schema_version, OLD.run_id, OLD.project_id, OLD.plan_id, OLD.plan_hash,
         OLD.ordinal, OLD.created_by, OLD.created_at)
     OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'Canonical VerificationAttempt transition lost identity or version CAS'
      USING ERRCODE = '40001';
  END IF;
  SELECT * INTO parent_run FROM canonical_verification_runs WHERE id = OLD.run_id;
  IF NOT FOUND OR parent_run.state = ANY(terminal_states)
     OR parent_run.lease_expires_at IS NULL OR parent_run.lease_expires_at <= statement_timestamp()
     OR parent_run.lease_worker_id IS DISTINCT FROM COALESCE(NEW.lease_worker_id, OLD.lease_worker_id) THEN
    RAISE EXCEPTION 'Canonical VerificationAttempt lost its parent Run worker fence'
      USING ERRCODE = '40001';
  END IF;
  IF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF NEW.fence_epoch <> parent_run.fence_epoch OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM parent_run.lease_worker_id
       OR NEW.lease_expires_at IS DISTINCT FROM parent_run.lease_expires_at
       OR NEW.started_at IS NULL THEN
      RAISE EXCEPTION 'Canonical VerificationAttempt claim must match its Run fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = ANY(active_states) AND NEW.state = 'claimed'
        AND OLD.lease_expires_at <= statement_timestamp() THEN
    IF NEW.fence_epoch <> parent_run.fence_epoch OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM parent_run.lease_worker_id
       OR NEW.lease_expires_at IS DISTINCT FROM parent_run.lease_expires_at THEN
      RAISE EXCEPTION 'Canonical VerificationAttempt reclaim must match its Run fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = NEW.state AND OLD.state = ANY(active_states) THEN
    IF NEW.fence_epoch <> OLD.fence_epoch OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_expires_at <= OLD.lease_expires_at THEN
      RAISE EXCEPTION 'Canonical VerificationAttempt heartbeat lost its fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF normal_advance THEN
    IF NEW.fence_epoch <> OLD.fence_epoch OR OLD.lease_expires_at <= statement_timestamp()
       OR (NEW.state <> ALL(terminal_states) AND (
         NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
         OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       )) THEN
      RAISE EXCEPTION 'Canonical VerificationAttempt transition lost its fence'
        USING ERRCODE = '40001';
    END IF;
    IF NEW.state = ANY(terminal_states) THEN
      IF NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
         OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL THEN
        RAISE EXCEPTION 'Terminal Canonical VerificationAttempt must close its lease'
          USING ERRCODE = '23514';
      END IF;
    ELSIF NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
      RAISE EXCEPTION 'Canonical VerificationAttempt changed unrelated lease facts'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'Invalid Canonical VerificationAttempt state transition'
      USING ERRCODE = '23514';
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER canonical_verification_attempt_transition_guard
BEFORE UPDATE OR DELETE ON canonical_verification_attempts
FOR EACH ROW EXECUTE FUNCTION guard_canonical_verification_attempt_transition();

CREATE TABLE canonical_verification_checks (
  receipt_id uuid NOT NULL REFERENCES canonical_verification_receipts(id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 511),
  check_id text NOT NULL,
  kind text NOT NULL,
  required boolean NOT NULL,
  status text NOT NULL CHECK (status IN ('passed','failed','error','skipped')),
  attempt_id uuid NOT NULL REFERENCES canonical_verification_attempts(id) ON DELETE RESTRICT,
  PRIMARY KEY (receipt_id, ordinal),
  UNIQUE (receipt_id, check_id)
);

CREATE TABLE canonical_verification_obligation_coverage (
  receipt_id uuid NOT NULL REFERENCES canonical_verification_receipts(id) ON DELETE RESTRICT,
  ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 1023),
  obligation_id text NOT NULL,
  level text NOT NULL CHECK (level IN ('must','should')),
  check_ids jsonb NOT NULL CHECK (verification_json_string_array_is_valid(check_ids, 1, 512, 160)),
  status text NOT NULL CHECK (status IN ('passed','missing')),
  PRIMARY KEY (receipt_id, ordinal),
  UNIQUE (receipt_id, obligation_id)
);

CREATE OR REPLACE FUNCTION validate_canonical_receipt_complete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_receipt_id uuid := CASE
    WHEN TG_TABLE_NAME = 'canonical_verification_receipts' THEN (to_jsonb(NEW)->>'id')::uuid
    ELSE (to_jsonb(NEW)->>'receipt_id')::uuid
  END;
  receipt canonical_verification_receipts%ROWTYPE;
  plan canonical_verification_plans%ROWTYPE;
BEGIN
  SELECT * INTO receipt FROM canonical_verification_receipts WHERE id = target_receipt_id;
  IF NOT FOUND THEN RETURN NULL; END IF;
  SELECT * INTO plan FROM canonical_verification_plans WHERE id = receipt.plan_id;
  IF NOT FOUND OR NOT EXISTS (
    SELECT 1 FROM canonical_verification_runs AS run
    WHERE run.id = receipt.run_id AND run.state = receipt.decision
      AND run.plan_id = receipt.plan_id AND run.plan_hash = receipt.plan_hash
  ) THEN
    RAISE EXCEPTION 'Canonical Receipt commit requires its exact terminal Run and Plan'
      USING ERRCODE = '23514';
  END IF;
  IF (SELECT count(*) FROM canonical_verification_attempts AS attempt
      WHERE attempt.run_id = receipt.run_id AND attempt.state = receipt.decision
        AND receipt.attempt_ids ? attempt.id::text) <> jsonb_array_length(receipt.attempt_ids)
     OR (SELECT count(*) FROM canonical_verification_checks WHERE receipt_id = receipt.id) <> receipt.check_count
     OR (SELECT count(*) FROM canonical_verification_obligation_coverage WHERE receipt_id = receipt.id) <> receipt.coverage_count THEN
    RAISE EXCEPTION 'Canonical Receipt Attempt/check/coverage projection is incomplete'
      USING ERRCODE = '23514';
  END IF;
  IF receipt.decision = 'passed' AND (
    EXISTS (
      SELECT 1 FROM jsonb_array_elements_text(plan.required_check_ids) required(check_id)
      WHERE NOT EXISTS (
        SELECT 1 FROM canonical_verification_checks AS check_result
        WHERE check_result.receipt_id = receipt.id
          AND check_result.check_id = required.check_id AND check_result.required AND check_result.status = 'passed'
      )
    )
    OR EXISTS (
      SELECT 1 FROM canonical_verification_obligation_coverage AS coverage
      WHERE coverage.receipt_id = receipt.id AND coverage.level = 'must' AND coverage.status <> 'passed'
    )
	OR NOT EXISTS (SELECT 1 FROM canonical_verification_checks WHERE receipt_id = receipt.id AND check_id = 'release-artifacts' AND kind = 'release-manifest' AND required AND status = 'passed')
	OR NOT EXISTS (SELECT 1 FROM canonical_verification_checks WHERE receipt_id = receipt.id AND check_id = 'release-sbom' AND kind = 'sbom' AND required AND status = 'passed')
	OR NOT EXISTS (SELECT 1 FROM canonical_verification_checks WHERE receipt_id = receipt.id AND check_id = 'release-vulnerability' AND kind = 'vulnerability' AND required AND status = 'passed')
	OR NOT EXISTS (SELECT 1 FROM canonical_verification_checks WHERE receipt_id = receipt.id AND check_id = 'release-container-policy' AND kind = 'container-policy' AND required AND status = 'passed')
  ) THEN
    RAISE EXCEPTION 'passed Canonical Receipt requires every planned check, Must coverage, and release security check'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER canonical_receipt_complete_from_receipt
AFTER INSERT ON canonical_verification_receipts
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_canonical_receipt_complete();
CREATE CONSTRAINT TRIGGER canonical_receipt_complete_from_check
AFTER INSERT ON canonical_verification_checks
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_canonical_receipt_complete();
CREATE CONSTRAINT TRIGGER canonical_receipt_complete_from_coverage
AFTER INSERT ON canonical_verification_obligation_coverage
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_canonical_receipt_complete();

CREATE TRIGGER canonical_verification_check_immutable
BEFORE UPDATE OR DELETE ON canonical_verification_checks
FOR EACH ROW EXECUTE FUNCTION prevent_canonical_quality_mutation();
CREATE TRIGGER canonical_verification_coverage_immutable
BEFORE UPDATE OR DELETE ON canonical_verification_obligation_coverage
FOR EACH ROW EXECUTE FUNCTION prevent_canonical_quality_mutation();

CREATE OR REPLACE FUNCTION validate_canonical_verification_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM canonical_verification_runs AS run
    JOIN canonical_verification_plans AS plan
      ON plan.id = run.plan_id AND plan.plan_hash = run.plan_hash
    JOIN verification_profile_policies AS profile_policy
      ON profile_policy.profile_id = plan.verification_profile_id
     AND profile_policy.profile_version = plan.verification_profile_version
     AND profile_policy.profile_hash = plan.verification_profile_hash
     AND profile_policy.state = 'active'
    WHERE run.id = NEW.run_id AND run.project_id = NEW.project_id
      AND run.plan_id = NEW.plan_id AND run.plan_hash = NEW.plan_hash
      AND run.state IN ('collecting', NEW.decision)
      AND plan.project_id = NEW.project_id
      AND plan.workspace_artifact_id = NEW.workspace_artifact_id
      AND plan.workspace_revision_id = NEW.workspace_revision_id
      AND plan.workspace_content_hash = NEW.workspace_content_hash
      AND plan.build_manifest_id = NEW.build_manifest_id AND plan.build_manifest_hash = NEW.build_manifest_hash
      AND plan.build_contract_id = NEW.build_contract_id AND plan.build_contract_hash = NEW.build_contract_hash
      AND plan.full_stack_template_id = NEW.full_stack_template_id AND plan.full_stack_template_hash = NEW.full_stack_template_hash
      AND plan.verification_profile_id = NEW.verification_profile_id
      AND plan.verification_profile_version = NEW.verification_profile_version
      AND plan.verification_profile_hash = NEW.verification_profile_hash
      AND plan.check_count = NEW.check_count AND plan.obligation_count = NEW.coverage_count
  ) THEN
    RAISE EXCEPTION 'Canonical VerificationReceipt requires its exact collecting/terminal Run, Plan, WorkspaceRevision, profile, and lineage'
      USING ERRCODE = '40001';
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;
