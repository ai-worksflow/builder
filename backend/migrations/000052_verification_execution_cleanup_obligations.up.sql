-- Runtime cleanup is a durable, fence-qualified obligation. Execution claims
-- register one obligation before returning authority to a worker; Receipts may
-- only be inserted after the exact Attempt generation has been reconciled.

CREATE TABLE verification_execution_cleanups (
  scope text NOT NULL CHECK (scope IN ('candidate', 'canonical')),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  run_id uuid NOT NULL,
  attempt_id uuid NOT NULL,
  attempt_fence_epoch bigint NOT NULL CHECK (attempt_fence_epoch > 0),
  state text NOT NULL CHECK (state IN ('registered', 'pending', 'cleaning', 'completed')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  lease_worker_id text CHECK (
    lease_worker_id IS NULL OR (
      lease_worker_id = btrim(lease_worker_id)
      AND length(lease_worker_id) BETWEEN 1 AND 160
      AND lease_worker_id ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,159}$'
    )
  ),
  lease_epoch bigint NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
  claimed_at timestamptz,
  lease_expires_at timestamptz,
  next_attempt_at timestamptz,
  last_error text CHECK (
    last_error IS NULL OR (
      last_error = btrim(last_error) AND length(last_error) BETWEEN 1 AND 2000
    )
  ),
  completed_at timestamptz,
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  PRIMARY KEY (scope, attempt_id, attempt_fence_epoch),
  CONSTRAINT verification_execution_cleanup_state_shape CHECK (
    (state = 'registered'
      AND lease_worker_id IS NULL AND claimed_at IS NULL AND lease_expires_at IS NULL
      AND next_attempt_at IS NULL AND last_error IS NULL AND completed_at IS NULL)
    OR (state = 'pending'
      AND lease_worker_id IS NULL AND claimed_at IS NULL AND lease_expires_at IS NULL
      AND next_attempt_at IS NOT NULL AND completed_at IS NULL)
    OR (state = 'cleaning'
      AND lease_worker_id IS NOT NULL AND lease_epoch > 0
      AND claimed_at IS NOT NULL AND lease_expires_at > claimed_at
      AND next_attempt_at IS NULL AND last_error IS NULL AND completed_at IS NULL)
    OR (state = 'completed'
      AND lease_worker_id IS NULL AND claimed_at IS NULL AND lease_expires_at IS NULL
      AND next_attempt_at IS NULL AND last_error IS NULL AND completed_at IS NOT NULL)
  )
);

CREATE INDEX verification_execution_cleanups_claim_idx
  ON verification_execution_cleanups (scope, state, next_attempt_at, lease_expires_at, created_at);

-- Existing generations have no durable cleanup proof. Preserve live leases as
-- registered and conservatively schedule every terminal/expired fence for an
-- idempotent cleanup; never manufacture a completed fact during backfill.
INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch, state,
  next_attempt_at, completed_at, created_by, updated_by, created_at, updated_at
)
SELECT 'candidate', attempt.project_id, attempt.run_id, attempt.id, attempt.fence_epoch,
       CASE WHEN attempt.state IN ('claimed', 'materializing', 'preparing', 'running', 'collecting')
                   AND attempt.lease_expires_at > statement_timestamp()
         THEN 'registered' ELSE 'pending' END,
       CASE
         WHEN attempt.state IN ('claimed', 'materializing', 'preparing', 'running', 'collecting')
              AND attempt.lease_expires_at > statement_timestamp() THEN NULL
         ELSE statement_timestamp()
       END,
       NULL,
       attempt.created_by, attempt.updated_by, attempt.created_at, statement_timestamp()
FROM candidate_verification_attempts AS attempt
WHERE attempt.fence_epoch > 0
  AND attempt.state <> 'queued';

INSERT INTO verification_execution_cleanups (
  scope, project_id, run_id, attempt_id, attempt_fence_epoch, state,
  next_attempt_at, completed_at, created_by, updated_by, created_at, updated_at
)
SELECT 'canonical', attempt.project_id, attempt.run_id, attempt.id, attempt.fence_epoch,
       CASE WHEN attempt.state IN ('claimed', 'materializing', 'preparing', 'running', 'collecting')
                   AND attempt.lease_expires_at > statement_timestamp()
         THEN 'registered' ELSE 'pending' END,
       CASE
         WHEN attempt.state IN ('claimed', 'materializing', 'preparing', 'running', 'collecting')
              AND attempt.lease_expires_at > statement_timestamp() THEN NULL
         ELSE statement_timestamp()
       END,
       NULL,
       attempt.created_by, attempt.updated_by, attempt.created_at, statement_timestamp()
FROM canonical_verification_attempts AS attempt
WHERE attempt.fence_epoch > 0
  AND attempt.state <> 'queued';

CREATE OR REPLACE FUNCTION validate_verification_execution_cleanup_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'verification execution cleanup obligations are append-only facts'
      USING ERRCODE = '55000';
  END IF;

  IF TG_OP = 'INSERT' THEN
    IF NEW.state <> 'registered' OR NEW.version <> 1 OR NEW.lease_epoch <> 0 OR NOT (
      (NEW.scope = 'candidate' AND EXISTS (
        SELECT 1 FROM candidate_verification_attempts AS attempt
        WHERE attempt.id = NEW.attempt_id AND attempt.run_id = NEW.run_id
          AND attempt.project_id = NEW.project_id
          AND attempt.fence_epoch = NEW.attempt_fence_epoch
      ))
      OR (NEW.scope = 'canonical' AND EXISTS (
        SELECT 1 FROM canonical_verification_attempts AS attempt
        WHERE attempt.id = NEW.attempt_id AND attempt.run_id = NEW.run_id
          AND attempt.project_id = NEW.project_id
          AND attempt.fence_epoch = NEW.attempt_fence_epoch
      ))
    ) THEN
      RAISE EXCEPTION 'cleanup obligation must bind one exact persisted Attempt fence'
        USING ERRCODE = '23514';
    END IF;
    NEW.created_at := statement_timestamp();
    NEW.updated_at := statement_timestamp();
    RETURN NEW;
  END IF;

  IF ROW(NEW.scope, NEW.project_id, NEW.run_id, NEW.attempt_id, NEW.attempt_fence_epoch,
         NEW.created_by, NEW.created_at)
     IS DISTINCT FROM
     ROW(OLD.scope, OLD.project_id, OLD.run_id, OLD.attempt_id, OLD.attempt_fence_epoch,
         OLD.created_by, OLD.created_at)
     OR NEW.version <> OLD.version + 1
     OR NEW.lease_epoch < OLD.lease_epoch
     OR OLD.state = 'completed'
     OR NOT (
       (OLD.state = 'registered' AND NEW.state IN ('pending', 'cleaning', 'completed'))
       OR (OLD.state = 'pending' AND NEW.state = 'cleaning')
       OR (OLD.state = 'pending' AND NEW.state = 'pending'
         AND NEW.next_attempt_at < OLD.next_attempt_at)
       OR (OLD.state = 'cleaning' AND NEW.state IN ('cleaning', 'pending', 'completed'))
     ) THEN
    RAISE EXCEPTION 'invalid verification execution cleanup transition'
      USING ERRCODE = '40001';
  END IF;
  NEW.updated_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER verification_execution_cleanup_mutation_guard
BEFORE INSERT OR UPDATE OR DELETE ON verification_execution_cleanups
FOR EACH ROW EXECUTE FUNCTION validate_verification_execution_cleanup_mutation();

-- Canonical execution originally had no administrative cancellation path.
-- Permit only the narrow post-cleanup reconciliation of an expired execution
-- whose exact profile policy is no longer active.
CREATE OR REPLACE FUNCTION guard_canonical_verification_run_transition_v2()
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
  cleanup_reconcile boolean;
  queued_policy_reconcile boolean;
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

  cleanup_reconcile := OLD.state = ANY(active_states)
    AND NEW.state = 'cancelled'
    AND OLD.lease_expires_at <= statement_timestamp()
    AND EXISTS (
      SELECT 1
      FROM canonical_verification_attempts AS attempt
      JOIN verification_execution_cleanups AS cleanup
        ON cleanup.scope = 'canonical' AND cleanup.attempt_id = attempt.id
       AND cleanup.attempt_fence_epoch = attempt.fence_epoch
       AND cleanup.state = 'completed'
      WHERE attempt.run_id = OLD.id AND attempt.state = 'cancelled'
        AND attempt.fence_epoch = OLD.fence_epoch
    )
    AND NOT EXISTS (
      SELECT 1
      FROM canonical_verification_plans AS plan
      JOIN verification_profile_policies AS policy
        ON policy.profile_id = plan.verification_profile_id
       AND policy.profile_version = plan.verification_profile_version
       AND policy.profile_hash = plan.verification_profile_hash
       AND policy.state = 'active'
      WHERE plan.id = OLD.plan_id AND plan.plan_hash = OLD.plan_hash
    );

  queued_policy_reconcile := OLD.state = 'queued'
    AND NEW.state = 'cancelled'
    AND NOT EXISTS (
      SELECT 1 FROM canonical_verification_attempts AS attempt
      WHERE attempt.run_id = OLD.id
    )
    AND NOT EXISTS (
      SELECT 1 FROM verification_execution_cleanups AS cleanup
      WHERE cleanup.scope = 'canonical' AND cleanup.run_id = OLD.id
    )
    AND NOT EXISTS (
      SELECT 1
      FROM canonical_verification_plans AS plan
      JOIN verification_profile_policies AS policy
        ON policy.profile_id = plan.verification_profile_id
       AND policy.profile_version = plan.verification_profile_version
       AND policy.profile_hash = plan.verification_profile_hash
       AND policy.state = 'active'
      WHERE plan.id = OLD.plan_id AND plan.plan_hash = OLD.plan_hash
    );

  IF queued_policy_reconcile THEN
    IF NEW.fence_epoch <> 0
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.started_at IS NOT NULL
       OR NEW.finished_at IS NULL
       OR NEW.terminal_reason IS DISTINCT FROM
          'verification profile policy is inactive before Canonical execution claim'
       OR NEW.execution_error IS NOT NULL THEN
      RAISE EXCEPTION 'Queued Canonical policy reconciliation cannot claim or invent runtime resources'
        USING ERRCODE = '40001';
    END IF;
  ELSIF cleanup_reconcile THEN
    IF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.started_at IS DISTINCT FROM OLD.started_at
       OR NEW.finished_at IS NULL OR NEW.terminal_reason IS NULL
       OR NEW.execution_error IS NOT NULL THEN
      RAISE EXCEPTION 'Canonical cleanup reconciliation lost its exact expired fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
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

CREATE OR REPLACE FUNCTION guard_canonical_verification_attempt_transition_v2()
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
  cleanup_reconcile boolean;
  parent_found boolean;
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
  parent_found := FOUND;
  cleanup_reconcile := parent_found
    AND parent_run.state = OLD.state
    AND OLD.state = ANY(active_states)
    AND NEW.state = 'cancelled'
    AND OLD.lease_expires_at <= statement_timestamp()
    AND parent_run.lease_expires_at <= statement_timestamp()
    AND EXISTS (
      SELECT 1 FROM verification_execution_cleanups AS cleanup
      WHERE cleanup.scope = 'canonical' AND cleanup.attempt_id = OLD.id
        AND cleanup.attempt_fence_epoch = OLD.fence_epoch
        AND cleanup.state = 'completed'
    )
    AND NOT EXISTS (
      SELECT 1
      FROM canonical_verification_plans AS plan
      JOIN verification_profile_policies AS policy
        ON policy.profile_id = plan.verification_profile_id
       AND policy.profile_version = plan.verification_profile_version
       AND policy.profile_hash = plan.verification_profile_hash
       AND policy.state = 'active'
      WHERE plan.id = OLD.plan_id AND plan.plan_hash = OLD.plan_hash
    );
  IF NOT parent_found OR parent_run.state = ANY(terminal_states)
     OR (NOT cleanup_reconcile AND (
       parent_run.lease_expires_at IS NULL
       OR parent_run.lease_expires_at <= statement_timestamp()
       OR parent_run.lease_worker_id IS DISTINCT FROM COALESCE(NEW.lease_worker_id, OLD.lease_worker_id)
     )) THEN
    RAISE EXCEPTION 'Canonical VerificationAttempt lost its parent Run worker fence'
      USING ERRCODE = '40001';
  END IF;
  IF cleanup_reconcile THEN
    IF NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NULL
       OR NEW.terminal_reason IS NULL OR NEW.execution_error IS NOT NULL THEN
      RAISE EXCEPTION 'Canonical Attempt cleanup reconciliation lost its exact expired fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
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
      RAISE EXCEPTION 'Active Canonical VerificationAttempt changed unrelated lease facts'
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

DROP TRIGGER canonical_verification_run_transition_guard
  ON canonical_verification_runs;
CREATE TRIGGER canonical_verification_run_transition_guard
BEFORE UPDATE OR DELETE ON canonical_verification_runs
FOR EACH ROW EXECUTE FUNCTION guard_canonical_verification_run_transition_v2();

DROP TRIGGER canonical_verification_attempt_transition_guard
  ON canonical_verification_attempts;
CREATE TRIGGER canonical_verification_attempt_transition_guard
BEFORE UPDATE OR DELETE ON canonical_verification_attempts
FOR EACH ROW EXECUTE FUNCTION guard_canonical_verification_attempt_transition_v2();

-- A claim generation is valid only when Run, Attempt, and exact-fence cleanup
-- registration all exist in the same committed transaction. Deferred checks
-- intentionally reject rolling-upgrade writers that still perform the legacy
-- Run-then-Attempt split or omit cleanup registration.
CREATE OR REPLACE FUNCTION require_verification_claim_cleanup_registration()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  is_claim_generation boolean := NEW.state = 'claimed'
    AND (OLD.state = 'queued' OR NEW.fence_epoch > OLD.fence_epoch);
BEGIN
  IF NOT is_claim_generation THEN
    RETURN NULL;
  END IF;

  IF TG_TABLE_NAME = 'candidate_verification_runs' THEN
    IF NOT EXISTS (
      SELECT 1
      FROM candidate_verification_runs AS run
      JOIN candidate_verification_attempts AS attempt
        ON attempt.run_id = run.id AND attempt.project_id = run.project_id
       AND attempt.state = run.state AND attempt.fence_epoch = run.fence_epoch
      JOIN verification_execution_cleanups AS cleanup
        ON cleanup.scope = 'candidate' AND cleanup.project_id = run.project_id
       AND cleanup.run_id = run.id AND cleanup.attempt_id = attempt.id
       AND cleanup.attempt_fence_epoch = attempt.fence_epoch
       AND cleanup.state = 'registered'
      WHERE run.id = NEW.id AND run.project_id = NEW.project_id
        AND run.fence_epoch = NEW.fence_epoch
        AND run.state = 'claimed'
    ) THEN
      RAISE EXCEPTION 'Candidate claim requires its exact-fence cleanup registration in the same transaction'
        USING ERRCODE = '23514';
    END IF;
  ELSIF TG_TABLE_NAME = 'candidate_verification_attempts' THEN
    IF NOT EXISTS (
      SELECT 1
      FROM candidate_verification_attempts AS attempt
      JOIN candidate_verification_runs AS run
        ON run.id = attempt.run_id AND run.project_id = attempt.project_id
       AND run.state = attempt.state AND run.fence_epoch = attempt.fence_epoch
      JOIN verification_execution_cleanups AS cleanup
        ON cleanup.scope = 'candidate' AND cleanup.project_id = attempt.project_id
       AND cleanup.run_id = attempt.run_id AND cleanup.attempt_id = attempt.id
       AND cleanup.attempt_fence_epoch = attempt.fence_epoch
       AND cleanup.state = 'registered'
      WHERE attempt.id = NEW.id AND attempt.run_id = NEW.run_id
        AND attempt.project_id = NEW.project_id
        AND attempt.fence_epoch = NEW.fence_epoch
        AND attempt.state = 'claimed'
    ) THEN
      RAISE EXCEPTION 'Candidate Attempt claim requires its exact-fence cleanup registration in the same transaction'
        USING ERRCODE = '23514';
    END IF;
  ELSIF TG_TABLE_NAME = 'canonical_verification_runs' THEN
    IF NOT EXISTS (
      SELECT 1
      FROM canonical_verification_runs AS run
      JOIN canonical_verification_attempts AS attempt
        ON attempt.run_id = run.id AND attempt.project_id = run.project_id
       AND attempt.state = run.state AND attempt.fence_epoch = run.fence_epoch
      JOIN verification_execution_cleanups AS cleanup
        ON cleanup.scope = 'canonical' AND cleanup.project_id = run.project_id
       AND cleanup.run_id = run.id AND cleanup.attempt_id = attempt.id
       AND cleanup.attempt_fence_epoch = attempt.fence_epoch
       AND cleanup.state = 'registered'
      WHERE run.id = NEW.id AND run.project_id = NEW.project_id
        AND run.fence_epoch = NEW.fence_epoch
        AND run.state = 'claimed'
    ) THEN
      RAISE EXCEPTION 'Canonical claim requires its exact-fence cleanup registration in the same transaction'
        USING ERRCODE = '23514';
    END IF;
  ELSIF TG_TABLE_NAME = 'canonical_verification_attempts' THEN
    IF NOT EXISTS (
      SELECT 1
      FROM canonical_verification_attempts AS attempt
      JOIN canonical_verification_runs AS run
        ON run.id = attempt.run_id AND run.project_id = attempt.project_id
       AND run.state = attempt.state AND run.fence_epoch = attempt.fence_epoch
      JOIN verification_execution_cleanups AS cleanup
        ON cleanup.scope = 'canonical' AND cleanup.project_id = attempt.project_id
       AND cleanup.run_id = attempt.run_id AND cleanup.attempt_id = attempt.id
       AND cleanup.attempt_fence_epoch = attempt.fence_epoch
       AND cleanup.state = 'registered'
      WHERE attempt.id = NEW.id AND attempt.run_id = NEW.run_id
        AND attempt.project_id = NEW.project_id
        AND attempt.fence_epoch = NEW.fence_epoch
        AND attempt.state = 'claimed'
    ) THEN
      RAISE EXCEPTION 'Canonical Attempt claim requires its exact-fence cleanup registration in the same transaction'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'unsupported verification claim cleanup scope'
      USING ERRCODE = '22023';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER candidate_verification_run_claim_cleanup_guard
AFTER UPDATE ON candidate_verification_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_verification_claim_cleanup_registration();

CREATE CONSTRAINT TRIGGER candidate_verification_attempt_claim_cleanup_guard
AFTER UPDATE ON candidate_verification_attempts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_verification_claim_cleanup_registration();

CREATE CONSTRAINT TRIGGER canonical_verification_run_claim_cleanup_guard
AFTER UPDATE ON canonical_verification_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_verification_claim_cleanup_registration();

CREATE CONSTRAINT TRIGGER canonical_verification_attempt_claim_cleanup_guard
AFTER UPDATE ON canonical_verification_attempts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_verification_claim_cleanup_registration();

CREATE OR REPLACE FUNCTION require_verification_receipt_cleanup_complete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_TABLE_NAME = 'candidate_verification_receipts' THEN
    IF EXISTS (
      SELECT 1
      FROM jsonb_array_elements_text(NEW.attempt_ids) AS listed(id)
      LEFT JOIN candidate_verification_attempts AS attempt
        ON attempt.id = listed.id::uuid AND attempt.run_id = NEW.run_id
      LEFT JOIN verification_execution_cleanups AS cleanup
        ON cleanup.scope = 'candidate' AND cleanup.attempt_id = attempt.id
       AND cleanup.attempt_fence_epoch = attempt.fence_epoch
      WHERE attempt.id IS NULL OR cleanup.state IS DISTINCT FROM 'completed'
    ) THEN
      RAISE EXCEPTION 'Candidate Receipt requires completed cleanup for every exact Attempt fence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF TG_TABLE_NAME = 'canonical_verification_receipts' THEN
    IF EXISTS (
      SELECT 1
      FROM jsonb_array_elements_text(NEW.attempt_ids) AS listed(id)
      LEFT JOIN canonical_verification_attempts AS attempt
        ON attempt.id = listed.id::uuid AND attempt.run_id = NEW.run_id
      LEFT JOIN verification_execution_cleanups AS cleanup
        ON cleanup.scope = 'canonical' AND cleanup.attempt_id = attempt.id
       AND cleanup.attempt_fence_epoch = attempt.fence_epoch
      WHERE attempt.id IS NULL OR cleanup.state IS DISTINCT FROM 'completed'
    ) THEN
      RAISE EXCEPTION 'Canonical Receipt requires completed cleanup for every exact Attempt fence'
        USING ERRCODE = '40001';
    END IF;
  ELSE
    RAISE EXCEPTION 'unsupported verification Receipt cleanup scope'
      USING ERRCODE = '22023';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_verification_receipt_cleanup_guard
BEFORE INSERT ON candidate_verification_receipts
FOR EACH ROW EXECUTE FUNCTION require_verification_receipt_cleanup_complete();

CREATE TRIGGER canonical_verification_receipt_cleanup_guard
BEFORE INSERT ON canonical_verification_receipts
FOR EACH ROW EXECUTE FUNCTION require_verification_receipt_cleanup_complete();

COMMENT ON TABLE verification_execution_cleanups IS
  'Durable exact-fence cleanup obligations shared by Candidate and Canonical verification workers.';
