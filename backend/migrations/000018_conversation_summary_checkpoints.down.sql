ALTER TABLE conversations
  DROP CONSTRAINT IF EXISTS conversations_summary_checkpoint_head_fk,
  DROP COLUMN IF EXISTS summary_checkpoint_head_id;

DROP TRIGGER IF EXISTS conversation_command_context_immutable ON conversation_commands;
DROP FUNCTION IF EXISTS guard_conversation_command_context_identity();
DROP TRIGGER IF EXISTS conversation_commands_context_match ON conversation_commands;
DROP FUNCTION IF EXISTS validate_conversation_command_context();
DROP TRIGGER IF EXISTS workflow_intent_conversation_context_immutable
  ON workflow_intent_proposals;
DROP FUNCTION IF EXISTS guard_workflow_intent_conversation_context_identity();
DROP TRIGGER IF EXISTS workflow_intent_new_conversation_context
  ON workflow_intent_proposals;
DROP FUNCTION IF EXISTS validate_new_workflow_intent_conversation_context();

ALTER TABLE conversation_commands
  DROP CONSTRAINT IF EXISTS conversation_commands_summary_checkpoint_fk,
  DROP CONSTRAINT IF EXISTS conversation_commands_conversation_context_object,
  DROP CONSTRAINT IF EXISTS conversation_commands_provider_input_hash_check,
  DROP COLUMN IF EXISTS provider_input_hash,
  DROP COLUMN IF EXISTS conversation_context,
  DROP COLUMN IF EXISTS summary_checkpoint_id;

ALTER TABLE workflow_intent_proposals
  DROP CONSTRAINT IF EXISTS workflow_intent_proposals_summary_checkpoint_fk,
  DROP CONSTRAINT IF EXISTS workflow_intent_proposals_conversation_context_object,
  DROP CONSTRAINT IF EXISTS workflow_intent_proposals_provider_input_hash_check,
  DROP CONSTRAINT IF EXISTS workflow_intent_proposals_conversation_context_shape,
  DROP COLUMN IF EXISTS provider_input_hash,
  DROP COLUMN IF EXISTS conversation_context,
  DROP COLUMN IF EXISTS summary_checkpoint_id;

DROP TRIGGER IF EXISTS conversation_summary_checkpoints_immutable_delete
  ON conversation_summary_checkpoints;
DROP FUNCTION IF EXISTS reject_conversation_summary_checkpoint_delete();
DROP TRIGGER IF EXISTS conversation_summary_checkpoint_head_commit
  ON conversation_summary_checkpoints;
DROP FUNCTION IF EXISTS require_approved_summary_checkpoint_is_head();
DROP TRIGGER IF EXISTS conversations_summary_checkpoint_head_forward_only ON conversations;
DROP FUNCTION IF EXISTS guard_conversation_summary_checkpoint_head();
DROP TRIGGER IF EXISTS conversation_summary_checkpoints_controlled_mutation
  ON conversation_summary_checkpoints;
DROP FUNCTION IF EXISTS guard_conversation_summary_checkpoint_mutation();
DROP TRIGGER IF EXISTS conversation_summary_checkpoints_pending_insert
  ON conversation_summary_checkpoints;
DROP FUNCTION IF EXISTS guard_new_conversation_summary_checkpoint();
DROP INDEX IF EXISTS conversation_summary_checkpoints_approved_genesis_idx;
DROP INDEX IF EXISTS conversation_summary_checkpoints_approved_child_idx;
DROP INDEX IF EXISTS conversation_summary_checkpoints_approved_coverage_idx;
DROP INDEX IF EXISTS conversation_summary_checkpoints_approved_resolution_idx;
DROP INDEX IF EXISTS conversation_summary_checkpoints_history_idx;
DROP TABLE IF EXISTS conversation_summary_checkpoints;

ALTER TABLE conversation_messages
  DROP CONSTRAINT IF EXISTS conversation_messages_checkpoint_identity_unique;
ALTER TABLE conversations
  DROP CONSTRAINT IF EXISTS conversations_checkpoint_tenant_identity_unique;
