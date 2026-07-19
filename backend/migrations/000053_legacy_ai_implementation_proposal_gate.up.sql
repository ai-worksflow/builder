-- Retire the legacy AI-to-ImplementationProposal write paths at the database
-- boundary. New executable proposals must come from the exact Candidate freeze
-- path (or an explicitly projected manual submission), while historical rows
-- remain readable and can still be terminalized without guessing payload facts.

ALTER TABLE implementation_proposals
  ADD COLUMN unimplemented_count integer,
  ADD COLUMN blocking_diagnostic_count integer,
  ADD CONSTRAINT implementation_proposals_unimplemented_count_check CHECK (
    unimplemented_count IS NULL OR unimplemented_count >= 0
  ),
  ADD CONSTRAINT implementation_proposals_blocking_diagnostic_count_check CHECK (
    blocking_diagnostic_count IS NULL OR blocking_diagnostic_count >= 0
  );

-- candidate_freeze is constructed only from an exact Candidate tree diff and
-- trace links. That path cannot carry diagnostics or unimplemented items, and
-- its separate deferred receipt guard continues to prove the exact freeze.
--
-- Migration 041 deliberately installed its VerificationReceipt shape as NOT
-- VALID so pre-verification Candidate history stayed readable. PostgreSQL
-- enforces a NOT VALID check on every subsequently updated row, so temporarily
-- remove and restore that check in this transaction while projecting the two
-- new columns. The restored shape gives unverified pre-041 Candidate history
-- one narrow escape hatch: it may be terminalized without inventing a receipt.
-- Preserve the constraint's validation state if an operator had validated it
-- after deployment (in which case no such history can exist).
DO $$
DECLARE
  candidate_verification_shape_was_valid boolean;
BEGIN
  SELECT constraint_row.convalidated
    INTO candidate_verification_shape_was_valid
  FROM pg_constraint AS constraint_row
  JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
  JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
  WHERE namespace.nspname = current_schema()
    AND relation.relname = 'implementation_proposals'
    AND constraint_row.conname = 'implementation_proposals_candidate_verification_shape';

  IF candidate_verification_shape_was_valid IS NULL THEN
    RAISE EXCEPTION 'migration 053 cannot locate the Candidate VerificationReceipt shape constraint'
      USING ERRCODE = '55000';
  END IF;

  ALTER TABLE implementation_proposals
    DROP CONSTRAINT implementation_proposals_candidate_verification_shape;

  -- Applied Candidate history may point at a now-consumed manifest. This is a
  -- projection-only backfill whose source semantics are fixed by the
  -- candidate_freeze execution shape, so do not re-evaluate current Build
  -- Contract policy while touching that immutable historical row.
  ALTER TABLE implementation_proposals
    DISABLE TRIGGER implementation_proposal_build_contract_guard;

  UPDATE implementation_proposals
  SET unimplemented_count = 0,
      blocking_diagnostic_count = 0
  WHERE execution_source = 'candidate_freeze';

  ALTER TABLE implementation_proposals
    ENABLE TRIGGER implementation_proposal_build_contract_guard;

  ALTER TABLE implementation_proposals
    ADD CONSTRAINT implementation_proposals_candidate_verification_shape CHECK (
      (
        execution_source = 'candidate_freeze'
        AND (
          (
            candidate_verification_binding_version = 'candidate-verification-binding/v1'
            AND candidate_verification_receipt_id IS NOT NULL
            AND candidate_verification_receipt_hash IS NOT NULL
          ) OR (
            status IN ('stale', 'rejected', 'failed')
            AND candidate_verification_binding_version IS NULL
            AND candidate_verification_receipt_id IS NULL
            AND candidate_verification_receipt_hash IS NULL
          )
        )
      ) OR (
        execution_source <> 'candidate_freeze'
        AND candidate_verification_binding_version IS NULL
        AND candidate_verification_receipt_id IS NULL
        AND candidate_verification_receipt_hash IS NULL
      )
    ) NOT VALID;

  IF candidate_verification_shape_was_valid THEN
    ALTER TABLE implementation_proposals
      VALIDATE CONSTRAINT implementation_proposals_candidate_verification_shape;
  END IF;
END;
$$;

COMMENT ON COLUMN implementation_proposals.unimplemented_count IS
  'Immutable payload projection. NULL is reserved for pre-migration manual/legacy AI history; every new Proposal must declare an exact value.';
COMMENT ON COLUMN implementation_proposals.blocking_diagnostic_count IS
  'Immutable payload projection. NULL is reserved for pre-migration manual/legacy AI history; every new Proposal must declare an exact value.';

CREATE FUNCTION guard_legacy_ai_implementation_proposal()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  legacy_ai_source boolean;
BEGIN
  legacy_ai_source := NEW.execution_source IN (
    'manual_generation', 'workflow_runner', 'conversation_command'
  );

  IF legacy_ai_source THEN
    IF TG_OP = 'INSERT' THEN
      RAISE EXCEPTION 'legacy AI ImplementationProposal creation is disabled; use the exact Candidate freeze path'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.status IN ('reviewing', 'ready', 'applied', 'partially_applied') THEN
      RAISE EXCEPTION 'legacy AI ImplementationProposal cannot be reviewable or applied; terminalize it as stale, rejected, or failed'
        USING ERRCODE = '23514';
    END IF;
  END IF;

  IF TG_OP = 'INSERT' THEN
    -- The relaxed NOT VALID shape above exists only so immutable historical
    -- rows can move to a terminal state. It is never an alternate creation
    -- path for a new unverified Candidate Proposal.
    IF NEW.execution_source = 'candidate_freeze' AND (
      NEW.candidate_verification_binding_version IS DISTINCT FROM 'candidate-verification-binding/v1'
      OR NEW.candidate_verification_receipt_id IS NULL
      OR NEW.candidate_verification_receipt_hash IS NULL
    ) THEN
      RAISE EXCEPTION 'new candidate_freeze ImplementationProposal requires an exact VerificationReceipt binding'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.unimplemented_count IS NULL OR NEW.blocking_diagnostic_count IS NULL THEN
      RAISE EXCEPTION 'new ImplementationProposal requires exact unimplemented and blocking diagnostic projections'
        USING ERRCODE = '23514';
    END IF;
  ELSIF ROW(OLD.unimplemented_count, OLD.blocking_diagnostic_count)
      IS DISTINCT FROM ROW(NEW.unimplemented_count, NEW.blocking_diagnostic_count) THEN
    -- Historical non-Candidate rows deliberately retain NULL projections. They
    -- may acquire both exact projections once, atomically, but projections are
    -- immutable after that first authoritative write.
    IF NOT (
      OLD.unimplemented_count IS NULL
      AND OLD.blocking_diagnostic_count IS NULL
      AND NEW.unimplemented_count IS NOT NULL
      AND NEW.blocking_diagnostic_count IS NOT NULL
    ) THEN
      RAISE EXCEPTION 'ImplementationProposal diagnostic projections are immutable'
        USING ERRCODE = '55000';
    END IF;
  END IF;

  IF NEW.execution_source = 'candidate_freeze'
     AND (NEW.unimplemented_count <> 0 OR NEW.blocking_diagnostic_count <> 0) THEN
    RAISE EXCEPTION 'candidate_freeze ImplementationProposal diagnostic projections must both be zero'
      USING ERRCODE = '23514';
  END IF;

  IF NEW.status IN ('reviewing', 'ready', 'applied', 'partially_applied')
     AND (
       NEW.unimplemented_count IS DISTINCT FROM 0
       OR NEW.blocking_diagnostic_count IS DISTINCT FROM 0
     ) THEN
    RAISE EXCEPTION 'reviewable or applied ImplementationProposal requires zero unimplemented items and zero blocking diagnostics'
      USING ERRCODE = '23514';
  END IF;

  RETURN NEW;
END;
$$;

-- PostgreSQL executes same-kind triggers by name. The 00 prefix makes this
-- retirement gate run before the older generation/build-contract guards, so a
-- retired source always fails with the canonical gate and cannot probe claims.
CREATE TRIGGER implementation_proposal_00_legacy_ai_gate
BEFORE INSERT OR UPDATE ON implementation_proposals
FOR EACH ROW EXECUTE FUNCTION guard_legacy_ai_implementation_proposal();

CREATE FUNCTION guard_legacy_ai_implementation_decision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_proposal_id uuid;
  target_execution_source text;
  target_verification_binding_version text;
  target_verification_receipt_id uuid;
  target_verification_receipt_hash text;
BEGIN
  IF TG_OP = 'DELETE' THEN
    target_proposal_id := OLD.proposal_id;
  ELSE
    target_proposal_id := NEW.proposal_id;
  END IF;

  SELECT
    proposal.execution_source,
    proposal.candidate_verification_binding_version,
    proposal.candidate_verification_receipt_id,
    proposal.candidate_verification_receipt_hash
    INTO
      target_execution_source,
      target_verification_binding_version,
      target_verification_receipt_id,
      target_verification_receipt_hash
  FROM implementation_proposals AS proposal
  WHERE proposal.id = target_proposal_id
  FOR KEY SHARE;

  IF target_execution_source IN (
    'manual_generation', 'workflow_runner', 'conversation_command'
  ) THEN
    RAISE EXCEPTION 'legacy AI ImplementationProposal decisions are disabled; terminalize the Proposal without changing decisions'
      USING ERRCODE = '23514';
  END IF;

  IF target_execution_source = 'candidate_freeze' THEN
    IF target_verification_binding_version IS DISTINCT FROM 'candidate-verification-binding/v1'
       OR target_verification_receipt_id IS NULL
       OR target_verification_receipt_hash IS NULL THEN
      RAISE EXCEPTION 'candidate_freeze ImplementationProposal decisions require an exact verified freeze receipt'
        USING ERRCODE = '23514';
    END IF;

    PERFORM 1
    FROM candidate_implementation_freezes AS freeze_receipt
    JOIN candidate_verification_receipts AS verification_receipt
      ON verification_receipt.id = freeze_receipt.verification_receipt_id
     AND verification_receipt.payload_hash = freeze_receipt.verification_receipt_hash
    WHERE freeze_receipt.implementation_proposal_id = target_proposal_id
      AND freeze_receipt.verification_binding_version = target_verification_binding_version
      AND freeze_receipt.verification_receipt_id = target_verification_receipt_id
      AND freeze_receipt.verification_receipt_hash = target_verification_receipt_hash
    FOR KEY SHARE OF freeze_receipt, verification_receipt;

    IF NOT FOUND THEN
      RAISE EXCEPTION 'candidate_freeze ImplementationProposal decisions require an exact verified freeze receipt'
        USING ERRCODE = '23514';
    END IF;
  END IF;

  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER implementation_operation_decision_00_legacy_ai_gate
BEFORE INSERT OR UPDATE OR DELETE ON implementation_operation_decisions
FOR EACH ROW EXECUTE FUNCTION guard_legacy_ai_implementation_decision();
