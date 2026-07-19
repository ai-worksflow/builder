-- Require every Candidate freeze and its Proposal to bind the exact passed,
-- fresh Candidate VerificationReceipt. The receipt transitively pins its Run,
-- Plan, active VerificationProfile, subject checkpoint, and build lineage.

ALTER TABLE implementation_proposals
  ADD COLUMN candidate_verification_binding_version text,
  ADD COLUMN candidate_verification_receipt_id uuid,
  ADD COLUMN candidate_verification_receipt_hash text,
  ADD CONSTRAINT implementation_proposals_candidate_verification_version_check CHECK (
    candidate_verification_binding_version IS NULL
    OR candidate_verification_binding_version = 'candidate-verification-binding/v1'
  ),
  ADD CONSTRAINT implementation_proposals_candidate_verification_hash_check CHECK (
    candidate_verification_receipt_hash IS NULL
    OR candidate_verification_receipt_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT implementation_proposals_candidate_verification_shape CHECK (
    (
      execution_source = 'candidate_freeze'
      AND candidate_verification_binding_version = 'candidate-verification-binding/v1'
      AND candidate_verification_receipt_id IS NOT NULL
      AND candidate_verification_receipt_hash IS NOT NULL
    ) OR (
      execution_source <> 'candidate_freeze'
      AND candidate_verification_binding_version IS NULL
      AND candidate_verification_receipt_id IS NULL
      AND candidate_verification_receipt_hash IS NULL
    )
  ) NOT VALID,
  ADD CONSTRAINT implementation_proposals_candidate_verification_exact_fk
    FOREIGN KEY (candidate_verification_receipt_id, candidate_verification_receipt_hash)
    REFERENCES candidate_verification_receipts(id, payload_hash)
    ON DELETE RESTRICT NOT VALID;

ALTER TABLE candidate_implementation_freezes
  ADD COLUMN verification_binding_version text,
  ADD COLUMN verification_receipt_id uuid,
  ADD COLUMN verification_receipt_hash text,
  ADD CONSTRAINT candidate_implementation_freezes_verification_shape CHECK (
    (verification_binding_version IS NULL
      AND verification_receipt_id IS NULL
      AND verification_receipt_hash IS NULL)
    OR (verification_binding_version = 'candidate-verification-binding/v1'
      AND verification_receipt_id IS NOT NULL
      AND verification_normalize_sha256(verification_receipt_hash) = verification_receipt_hash)
  ) NOT VALID,
  ADD CONSTRAINT candidate_implementation_freezes_verification_exact_fk
    FOREIGN KEY (verification_receipt_id, verification_receipt_hash)
    REFERENCES candidate_verification_receipts(id, payload_hash)
    ON DELETE RESTRICT NOT VALID;

CREATE OR REPLACE FUNCTION validate_candidate_implementation_freeze_verification()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  PERFORM 1
  FROM candidate_verification_receipts AS verification_receipt
  JOIN candidate_verification_runs AS verification_run
    ON verification_run.id = verification_receipt.run_id
   AND verification_run.project_id = verification_receipt.project_id
   AND verification_run.plan_id = verification_receipt.plan_id
   AND verification_run.plan_hash = verification_receipt.plan_hash
  JOIN candidate_verification_plans AS verification_plan
    ON verification_plan.id = verification_receipt.plan_id
   AND verification_plan.plan_hash = verification_receipt.plan_hash
   AND verification_plan.project_id = verification_receipt.project_id
  JOIN verification_profile_policies AS profile_policy
    ON profile_policy.profile_id = verification_receipt.verification_profile_id
   AND profile_policy.profile_version = verification_receipt.verification_profile_version
   AND profile_policy.profile_hash = verification_receipt.verification_profile_hash
  JOIN implementation_proposals AS proposal
    ON proposal.id = NEW.implementation_proposal_id
   AND proposal.project_id = NEW.project_id
  WHERE verification_receipt.id = NEW.verification_receipt_id
    AND verification_receipt.payload_hash = NEW.verification_receipt_hash
    AND verification_receipt.decision = 'passed'
    AND verification_receipt.must_passed_count = verification_receipt.must_count
    AND verification_receipt.blocker_count = 0
    AND verification_run.state = 'passed'
    AND profile_policy.state = 'active'
    AND verification_receipt.project_id = NEW.project_id
    AND verification_receipt.sandbox_session_id = NEW.session_id
    AND verification_receipt.candidate_id = NEW.candidate_id
    AND verification_receipt.candidate_snapshot_id = NEW.candidate_snapshot_id
    AND verification_receipt.candidate_version = NEW.candidate_version
    AND verification_receipt.journal_sequence = NEW.journal_sequence
    AND verification_receipt.session_epoch = NEW.session_epoch
    AND verification_receipt.writer_lease_epoch = NEW.writer_lease_epoch
    AND verification_receipt.tree_hash = NEW.candidate_tree_hash
    AND verification_plan.sandbox_session_id = NEW.session_id
    AND verification_plan.session_version = NEW.session_version
    AND verification_plan.candidate_id = NEW.candidate_id
    AND verification_plan.candidate_snapshot_id = NEW.candidate_snapshot_id
    AND verification_plan.candidate_version = NEW.candidate_version
    AND verification_plan.journal_sequence = NEW.journal_sequence
    AND verification_plan.session_epoch = NEW.session_epoch
    AND verification_plan.writer_lease_epoch = NEW.writer_lease_epoch
    AND verification_plan.tree_owner_id = NEW.candidate_tree_owner_id
    AND verification_plan.tree_store = NEW.candidate_tree_store
    AND verification_plan.tree_ref = NEW.candidate_tree_ref
    AND verification_plan.tree_content_hash = NEW.candidate_tree_content_hash
    AND verification_plan.tree_hash = NEW.candidate_tree_hash
    AND verification_receipt.build_manifest_id = NEW.build_manifest_id
    AND verification_receipt.build_manifest_hash = verification_normalize_sha256(NEW.build_manifest_hash)
    AND verification_receipt.build_contract_id = NEW.build_contract_id
    AND verification_receipt.build_contract_hash = verification_normalize_sha256(NEW.build_contract_hash)
    AND verification_receipt.full_stack_template_id = NEW.full_stack_template_id
    AND verification_receipt.full_stack_template_hash = NEW.full_stack_template_hash
    AND verification_plan.build_manifest_id = NEW.build_manifest_id
    AND verification_plan.build_manifest_hash = verification_normalize_sha256(NEW.build_manifest_hash)
    AND verification_plan.build_contract_id = NEW.build_contract_id
    AND verification_plan.build_contract_hash = verification_normalize_sha256(NEW.build_contract_hash)
    AND verification_plan.full_stack_template_id = NEW.full_stack_template_id
    AND verification_plan.full_stack_template_hash = NEW.full_stack_template_hash
    AND proposal.candidate_verification_binding_version = NEW.verification_binding_version
    AND proposal.candidate_verification_receipt_id = NEW.verification_receipt_id
    AND proposal.candidate_verification_receipt_hash = NEW.verification_receipt_hash
    AND proposal.candidate_snapshot_id = NEW.candidate_snapshot_id
    AND proposal.candidate_tree_hash = NEW.candidate_tree_hash
  FOR KEY SHARE OF verification_receipt, verification_run, verification_plan, profile_policy, proposal;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Candidate freeze requires one exact passed fresh VerificationReceipt, Run, Plan, profile, checkpoint, tree, and build lineage'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_implementation_freeze_verification_guard
BEFORE INSERT ON candidate_implementation_freezes
FOR EACH ROW EXECUTE FUNCTION validate_candidate_implementation_freeze_verification();

CREATE OR REPLACE FUNCTION guard_candidate_implementation_proposal_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(
    OLD.candidate_snapshot_id,
    OLD.candidate_base_tree_hash,
    OLD.candidate_tree_hash,
    OLD.candidate_verification_binding_version,
    OLD.candidate_verification_receipt_id,
    OLD.candidate_verification_receipt_hash
  ) IS DISTINCT FROM ROW(
    NEW.candidate_snapshot_id,
    NEW.candidate_base_tree_hash,
    NEW.candidate_tree_hash,
    NEW.candidate_verification_binding_version,
    NEW.candidate_verification_receipt_id,
    NEW.candidate_verification_receipt_hash
  ) THEN
    RAISE EXCEPTION 'implementation proposal Candidate source and VerificationReceipt identity are immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_candidate_implementation_proposal_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.execution_source = 'candidate_freeze' AND NOT EXISTS (
    SELECT 1
    FROM candidate_implementation_freezes AS receipt
    JOIN candidate_workspace_control_events AS event
      ON event.candidate_id = receipt.candidate_id
     AND event.event_kind = 'candidate.frozen'
     AND event.candidate_snapshot_id = receipt.candidate_snapshot_id
     AND event.candidate_version_from = receipt.candidate_version
     AND event.candidate_version_to = receipt.candidate_version + 1
     AND event.actor_id = receipt.created_by
    JOIN candidate_verification_receipts AS verification_receipt
      ON verification_receipt.id = receipt.verification_receipt_id
     AND verification_receipt.payload_hash = receipt.verification_receipt_hash
     AND verification_receipt.decision = 'passed'
    JOIN candidate_verification_runs AS verification_run
      ON verification_run.id = verification_receipt.run_id
     AND verification_run.state = 'passed'
     AND verification_run.plan_id = verification_receipt.plan_id
     AND verification_run.plan_hash = verification_receipt.plan_hash
    JOIN candidate_verification_plans AS verification_plan
      ON verification_plan.id = verification_receipt.plan_id
     AND verification_plan.plan_hash = verification_receipt.plan_hash
    JOIN verification_profile_policies AS profile_policy
      ON profile_policy.profile_id = verification_receipt.verification_profile_id
     AND profile_policy.profile_version = verification_receipt.verification_profile_version
     AND profile_policy.profile_hash = verification_receipt.verification_profile_hash
     AND profile_policy.state = 'active'
    WHERE receipt.implementation_proposal_id = NEW.id
      AND receipt.project_id = NEW.project_id
      AND receipt.build_manifest_id = NEW.build_manifest_id
      AND receipt.build_contract_id = NEW.application_build_contract_id
      AND receipt.build_contract_hash = NEW.application_build_contract_hash
      AND receipt.base_workspace_revision_id = NEW.base_workspace_revision_id
      AND receipt.candidate_snapshot_id = NEW.candidate_snapshot_id
      AND receipt.base_tree_hash = NEW.candidate_base_tree_hash
      AND receipt.candidate_tree_hash = NEW.candidate_tree_hash
      AND receipt.proposal_payload_hash = NEW.payload_hash
      AND receipt.operation_count = NEW.operation_count
      AND receipt.verification_binding_version = NEW.candidate_verification_binding_version
      AND receipt.verification_receipt_id = NEW.candidate_verification_receipt_id
      AND receipt.verification_receipt_hash = NEW.candidate_verification_receipt_hash
      AND verification_receipt.project_id = receipt.project_id
      AND verification_receipt.sandbox_session_id = receipt.session_id
      AND verification_receipt.candidate_id = receipt.candidate_id
      AND verification_receipt.candidate_snapshot_id = receipt.candidate_snapshot_id
      AND verification_receipt.candidate_version = receipt.candidate_version
      AND verification_receipt.journal_sequence = receipt.journal_sequence
      AND verification_receipt.session_epoch = receipt.session_epoch
      AND verification_receipt.writer_lease_epoch = receipt.writer_lease_epoch
      AND verification_receipt.tree_hash = receipt.candidate_tree_hash
      AND verification_plan.session_version = receipt.session_version
  ) THEN
    RAISE EXCEPTION 'candidate implementation proposal requires its exact passed VerificationReceipt and committed freeze receipt'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

COMMENT ON COLUMN implementation_proposals.candidate_verification_receipt_id IS
  'Exact passed Candidate VerificationReceipt identity preserved through review and immutable WorkspaceRevision proposal lineage.';
COMMENT ON COLUMN candidate_implementation_freezes.verification_receipt_id IS
  'Exact passed Candidate VerificationReceipt that authorized this atomic freeze.';
