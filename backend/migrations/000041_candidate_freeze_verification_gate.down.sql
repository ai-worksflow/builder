DROP TRIGGER IF EXISTS candidate_implementation_freeze_verification_guard
  ON candidate_implementation_freezes;
DROP FUNCTION IF EXISTS validate_candidate_implementation_freeze_verification();

CREATE OR REPLACE FUNCTION guard_candidate_implementation_proposal_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(
    OLD.candidate_snapshot_id, OLD.candidate_base_tree_hash, OLD.candidate_tree_hash
  ) IS DISTINCT FROM ROW(
    NEW.candidate_snapshot_id, NEW.candidate_base_tree_hash, NEW.candidate_tree_hash
  ) THEN
    RAISE EXCEPTION 'implementation proposal Candidate source identity is immutable'
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
  ) THEN
    RAISE EXCEPTION 'candidate implementation proposal requires its exact committed freeze receipt and control event'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

ALTER TABLE candidate_implementation_freezes
  DROP CONSTRAINT IF EXISTS candidate_implementation_freezes_verification_exact_fk,
  DROP CONSTRAINT IF EXISTS candidate_implementation_freezes_verification_shape,
  DROP COLUMN IF EXISTS verification_receipt_hash,
  DROP COLUMN IF EXISTS verification_receipt_id,
  DROP COLUMN IF EXISTS verification_binding_version;

ALTER TABLE implementation_proposals
  DROP CONSTRAINT IF EXISTS implementation_proposals_candidate_verification_exact_fk,
  DROP CONSTRAINT IF EXISTS implementation_proposals_candidate_verification_shape,
  DROP CONSTRAINT IF EXISTS implementation_proposals_candidate_verification_hash_check,
  DROP CONSTRAINT IF EXISTS implementation_proposals_candidate_verification_version_check,
  DROP COLUMN IF EXISTS candidate_verification_receipt_hash,
  DROP COLUMN IF EXISTS candidate_verification_receipt_id,
  DROP COLUMN IF EXISTS candidate_verification_binding_version;
