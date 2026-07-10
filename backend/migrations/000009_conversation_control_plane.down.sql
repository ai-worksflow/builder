DROP TRIGGER IF EXISTS conversation_commands_identity_immutable ON conversation_commands;
DROP FUNCTION IF EXISTS guard_conversation_command_identity();
DROP TRIGGER IF EXISTS workflow_intent_proposals_identity_immutable ON workflow_intent_proposals;
DROP FUNCTION IF EXISTS guard_workflow_intent_proposal_identity();
DROP TRIGGER IF EXISTS conversation_messages_immutable_update ON conversation_messages;
DROP FUNCTION IF EXISTS reject_conversation_message_mutation();
DROP TABLE IF EXISTS conversation_commands;
ALTER TABLE IF EXISTS workflow_intent_proposals
  DROP CONSTRAINT IF EXISTS workflow_intent_proposals_assistant_message_fk;
ALTER TABLE IF EXISTS conversation_messages
  DROP CONSTRAINT IF EXISTS conversation_messages_proposal_fk;
DROP TABLE IF EXISTS workflow_intent_proposals;
DROP TABLE IF EXISTS conversation_messages;
DROP TABLE IF EXISTS conversations;
