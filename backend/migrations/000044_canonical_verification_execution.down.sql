DROP TRIGGER IF EXISTS canonical_receipt_complete_from_coverage ON canonical_verification_obligation_coverage;
DROP TRIGGER IF EXISTS canonical_receipt_complete_from_check ON canonical_verification_checks;
DROP TRIGGER IF EXISTS canonical_receipt_complete_from_receipt ON canonical_verification_receipts;
DROP FUNCTION IF EXISTS validate_canonical_receipt_complete();
DROP TRIGGER IF EXISTS canonical_verification_coverage_immutable ON canonical_verification_obligation_coverage;
DROP TRIGGER IF EXISTS canonical_verification_check_immutable ON canonical_verification_checks;
DROP TABLE IF EXISTS canonical_verification_obligation_coverage;
DROP TABLE IF EXISTS canonical_verification_checks;
DROP TRIGGER IF EXISTS canonical_verification_attempt_transition_guard ON canonical_verification_attempts;
DROP TRIGGER IF EXISTS canonical_verification_attempt_insert_guard ON canonical_verification_attempts;
DROP FUNCTION IF EXISTS guard_canonical_verification_attempt_transition();
DROP FUNCTION IF EXISTS validate_canonical_verification_attempt_insert();
DROP TABLE IF EXISTS canonical_verification_attempts;
DROP TRIGGER IF EXISTS canonical_verification_run_transition_guard ON canonical_verification_runs;
DROP TRIGGER IF EXISTS canonical_verification_run_insert_guard ON canonical_verification_runs;
DROP FUNCTION IF EXISTS guard_canonical_verification_run_transition();
DROP FUNCTION IF EXISTS validate_canonical_verification_run_insert();

-- Restore migration 43's terminal-only Receipt boundary. Migration 44 widens
-- this function only so a worker can atomically insert child evidence and then
-- terminalize the Run in one deferred transaction.
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
    WHERE run.id = NEW.run_id
      AND run.project_id = NEW.project_id
      AND run.plan_id = NEW.plan_id
      AND run.plan_hash = NEW.plan_hash
      AND run.state = NEW.decision
      AND plan.project_id = NEW.project_id
      AND plan.workspace_artifact_id = NEW.workspace_artifact_id
      AND plan.workspace_revision_id = NEW.workspace_revision_id
      AND plan.workspace_content_hash = NEW.workspace_content_hash
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
    RAISE EXCEPTION 'Canonical VerificationReceipt requires its exact terminal Run, Plan, WorkspaceRevision, profile, and lineage'
      USING ERRCODE = '40001';
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;
