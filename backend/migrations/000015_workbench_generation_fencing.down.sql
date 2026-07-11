DROP TRIGGER IF EXISTS conversation_implementation_decision_receipt_gate ON implementation_operation_decisions;
DROP FUNCTION IF EXISTS guard_conversation_implementation_decision_receipt();
DROP TRIGGER IF EXISTS implementation_proposal_generation_refs ON implementation_proposals;
DROP FUNCTION IF EXISTS validate_implementation_proposal_generation_refs();
DROP TRIGGER IF EXISTS implementation_proposal_generation_identity_immutable ON implementation_proposals;
DROP FUNCTION IF EXISTS guard_implementation_proposal_generation_identity();
DROP TRIGGER IF EXISTS implementation_generation_tenant_refs ON implementation_generation_claims;
DROP FUNCTION IF EXISTS validate_implementation_generation_tenant_refs();
DROP TRIGGER IF EXISTS implementation_generation_claim_identity_immutable ON implementation_generation_claims;
DROP FUNCTION IF EXISTS guard_implementation_generation_claim_identity();
DROP INDEX IF EXISTS implementation_generation_claims_one_processing_leaf_idx;
DROP TABLE IF EXISTS implementation_generation_claims;
DROP INDEX IF EXISTS implementation_proposals_one_active_per_leaf_idx;
DROP TRIGGER IF EXISTS workflow_intent_desired_output_immutable ON workflow_intent_proposals;
DROP FUNCTION IF EXISTS guard_workflow_intent_desired_output_identity();
ALTER TABLE implementation_proposals
  DROP CONSTRAINT IF EXISTS implementation_proposals_conversation_command_unique,
  DROP CONSTRAINT IF EXISTS implementation_proposals_execution_shape_check,
  DROP CONSTRAINT IF EXISTS implementation_proposals_instruction_hash_check,
  DROP CONSTRAINT IF EXISTS implementation_proposals_execution_source_check,
  DROP COLUMN IF EXISTS ai_model,
  DROP COLUMN IF EXISTS ai_provider,
  DROP COLUMN IF EXISTS instruction_hash,
  DROP COLUMN IF EXISTS supersedes_proposal_id,
  DROP COLUMN IF EXISTS conversation_command_id,
  DROP COLUMN IF EXISTS execution_source;
ALTER TABLE workflow_intent_proposals
  DROP CONSTRAINT IF EXISTS workflow_intent_proposals_desired_output_check,
  DROP COLUMN IF EXISTS desired_output_capability;
