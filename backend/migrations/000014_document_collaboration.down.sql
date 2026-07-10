DROP TRIGGER IF EXISTS document_generation_command_identity_immutable ON document_generation_commands;
DROP TRIGGER IF EXISTS document_generation_command_refs ON document_generation_commands;
DROP TRIGGER IF EXISTS artifact_member_binding_owner_required ON artifact_member_bindings;
DROP TRIGGER IF EXISTS artifact_member_binding_identity_immutable ON artifact_member_bindings;
DROP TRIGGER IF EXISTS artifact_member_binding_tenant_refs ON artifact_member_bindings;
DROP TRIGGER IF EXISTS artifact_collaboration_tenant_refs ON artifact_collaboration_states;
DROP FUNCTION IF EXISTS prevent_document_generation_command_identity_mutation();
DROP FUNCTION IF EXISTS validate_document_generation_command_refs();
DROP FUNCTION IF EXISTS validate_artifact_member_binding_owner();
DROP FUNCTION IF EXISTS prevent_artifact_member_binding_identity_mutation();
DROP FUNCTION IF EXISTS validate_artifact_member_binding_tenant_refs();
DROP FUNCTION IF EXISTS validate_artifact_collaboration_tenant_refs();
DROP TABLE IF EXISTS document_generation_commands;
ALTER TABLE output_proposals
  DROP COLUMN IF EXISTS ai_model,
  DROP COLUMN IF EXISTS ai_provider;
DROP TABLE IF EXISTS artifact_collaboration_states;
DROP TABLE IF EXISTS artifact_member_bindings;
