CREATE TABLE conversations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title text NOT NULL,
  status text NOT NULL DEFAULT 'active',
  version bigint NOT NULL DEFAULT 1,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  CONSTRAINT conversations_title_nonempty CHECK (length(trim(title)) BETWEEN 1 AND 160),
  CONSTRAINT conversations_status_check CHECK (status IN ('active', 'archived')),
  CONSTRAINT conversations_version_positive CHECK (version > 0)
);

CREATE INDEX conversations_project_history_idx
  ON conversations (project_id, created_at DESC, id DESC);

CREATE TABLE conversation_messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  sequence bigint NOT NULL,
  role text NOT NULL,
  content text NOT NULL,
  proposal_id uuid,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT conversation_messages_sequence_positive CHECK (sequence > 0),
  CONSTRAINT conversation_messages_role_check CHECK (role IN ('user', 'assistant')),
  CONSTRAINT conversation_messages_content_nonempty CHECK (length(trim(content)) BETWEEN 1 AND 32768),
  CONSTRAINT conversation_messages_assistant_proposal_check CHECK (
    (role = 'user' AND proposal_id IS NULL) OR
    (role = 'assistant' AND proposal_id IS NOT NULL)
  ),
  UNIQUE (conversation_id, sequence),
  UNIQUE (proposal_id)
);

CREATE INDEX conversation_messages_history_idx
  ON conversation_messages (conversation_id, sequence ASC, id ASC);

CREATE TABLE workflow_intent_proposals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  trigger_message_id uuid NOT NULL REFERENCES conversation_messages(id) ON DELETE RESTRICT,
  assistant_message_id uuid NOT NULL,
  kind text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  version bigint NOT NULL DEFAULT 1,
  suggested_definition_version_id uuid NOT NULL REFERENCES workflow_definition_versions(id) ON DELETE RESTRICT,
  scope jsonb NOT NULL DEFAULT '{}'::jsonb,
  source_refs jsonb NOT NULL,
  manifest_intent jsonb NOT NULL,
  workbench_instruction jsonb NOT NULL,
  origin text NOT NULL DEFAULT 'submitted',
  ai_provider text,
  ai_model text,
  ai_response_id text,
  decision_reason text NOT NULL DEFAULT '',
  proposed_by uuid NOT NULL REFERENCES users(id),
  decided_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  CONSTRAINT workflow_intent_proposals_kind_check CHECK (
    kind IN ('start_workflow', 'workbench_instruction')
  ),
  CONSTRAINT workflow_intent_proposals_status_check CHECK (
    status IN ('pending', 'accepted', 'rejected')
  ),
  CONSTRAINT workflow_intent_proposals_version_positive CHECK (version > 0),
  CONSTRAINT workflow_intent_proposals_scope_object CHECK (jsonb_typeof(scope) = 'object'),
  CONSTRAINT workflow_intent_proposals_sources_array CHECK (
    jsonb_typeof(source_refs) = 'array' AND jsonb_array_length(source_refs) > 0
  ),
  CONSTRAINT workflow_intent_proposals_manifest_object CHECK (jsonb_typeof(manifest_intent) = 'object'),
  CONSTRAINT workflow_intent_proposals_instruction_object CHECK (jsonb_typeof(workbench_instruction) = 'object'),
  CONSTRAINT workflow_intent_proposals_origin_check CHECK (origin IN ('submitted', 'ai')),
  CONSTRAINT workflow_intent_proposals_ai_provenance_check CHECK (
    (origin = 'submitted' AND ai_provider IS NULL AND ai_model IS NULL AND ai_response_id IS NULL) OR
    (origin = 'ai' AND length(trim(ai_provider)) > 0 AND length(trim(ai_model)) > 0)
  ),
  CONSTRAINT workflow_intent_proposals_decision_shape CHECK (
    (status = 'pending' AND decided_by IS NULL AND decided_at IS NULL) OR
    (status IN ('accepted', 'rejected') AND decided_by IS NOT NULL AND decided_at IS NOT NULL)
  ),
  UNIQUE (assistant_message_id)
);

ALTER TABLE conversation_messages
  ADD CONSTRAINT conversation_messages_proposal_fk
    FOREIGN KEY (proposal_id) REFERENCES workflow_intent_proposals(id) ON DELETE RESTRICT;

ALTER TABLE workflow_intent_proposals
  ADD CONSTRAINT workflow_intent_proposals_assistant_message_fk
    FOREIGN KEY (assistant_message_id) REFERENCES conversation_messages(id) ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX workflow_intent_proposals_history_idx
  ON workflow_intent_proposals (project_id, conversation_id, created_at DESC, id DESC);

CREATE TABLE conversation_commands (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  proposal_id uuid NOT NULL REFERENCES workflow_intent_proposals(id) ON DELETE RESTRICT,
  kind text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  version bigint NOT NULL DEFAULT 1,
  payload jsonb NOT NULL,
  result jsonb,
  failure jsonb,
  accepted_by uuid NOT NULL REFERENCES users(id),
  execution_actor_id uuid REFERENCES users(id),
  execution_claim uuid,
  claim_expires_at timestamptz,
  executed_by uuid REFERENCES users(id),
  rejected_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  executed_at timestamptz,
  rejected_at timestamptz,
  failed_at timestamptz,
  CONSTRAINT conversation_commands_kind_check CHECK (
    kind IN ('start_workflow', 'workbench_instruction')
  ),
  CONSTRAINT conversation_commands_status_check CHECK (
    status IN ('pending', 'executed', 'rejected', 'failed')
  ),
  CONSTRAINT conversation_commands_version_positive CHECK (version > 0),
  CONSTRAINT conversation_commands_payload_object CHECK (jsonb_typeof(payload) = 'object'),
  CONSTRAINT conversation_commands_result_object CHECK (result IS NULL OR jsonb_typeof(result) = 'object'),
  CONSTRAINT conversation_commands_failure_object CHECK (failure IS NULL OR jsonb_typeof(failure) = 'object'),
  CONSTRAINT conversation_commands_terminal_shape CHECK (
    (status = 'pending' AND executed_at IS NULL AND rejected_at IS NULL AND failed_at IS NULL) OR
    (status = 'executed' AND executed_at IS NOT NULL AND rejected_at IS NULL AND failed_at IS NULL) OR
    (status = 'rejected' AND executed_at IS NULL AND rejected_at IS NOT NULL AND failed_at IS NULL) OR
    (status = 'failed' AND executed_at IS NULL AND rejected_at IS NULL AND failed_at IS NOT NULL)
  ),
  UNIQUE (proposal_id)
);

CREATE INDEX conversation_commands_history_idx
  ON conversation_commands (project_id, conversation_id, created_at DESC, id DESC);
CREATE INDEX conversation_commands_pending_idx
  ON conversation_commands (status, claim_expires_at, created_at ASC)
  WHERE status = 'pending';

CREATE FUNCTION reject_conversation_message_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'conversation messages are immutable'
    USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversation_messages_immutable_update
BEFORE UPDATE OR DELETE ON conversation_messages
FOR EACH ROW EXECUTE FUNCTION reject_conversation_message_mutation();

CREATE FUNCTION guard_workflow_intent_proposal_identity() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.project_id IS DISTINCT FROM OLD.project_id
     OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
     OR NEW.trigger_message_id IS DISTINCT FROM OLD.trigger_message_id
     OR NEW.assistant_message_id IS DISTINCT FROM OLD.assistant_message_id
     OR NEW.kind IS DISTINCT FROM OLD.kind
     OR NEW.suggested_definition_version_id IS DISTINCT FROM OLD.suggested_definition_version_id
     OR NEW.scope IS DISTINCT FROM OLD.scope
     OR NEW.source_refs IS DISTINCT FROM OLD.source_refs
     OR NEW.manifest_intent IS DISTINCT FROM OLD.manifest_intent
     OR NEW.workbench_instruction IS DISTINCT FROM OLD.workbench_instruction
     OR NEW.origin IS DISTINCT FROM OLD.origin
     OR NEW.ai_provider IS DISTINCT FROM OLD.ai_provider
     OR NEW.ai_model IS DISTINCT FROM OLD.ai_model
     OR NEW.ai_response_id IS DISTINCT FROM OLD.ai_response_id
     OR NEW.proposed_by IS DISTINCT FROM OLD.proposed_by
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'workflow intent proposal identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workflow_intent_proposals_identity_immutable
BEFORE UPDATE ON workflow_intent_proposals
FOR EACH ROW EXECUTE FUNCTION guard_workflow_intent_proposal_identity();

CREATE FUNCTION guard_conversation_command_identity() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.project_id IS DISTINCT FROM OLD.project_id
     OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
     OR NEW.proposal_id IS DISTINCT FROM OLD.proposal_id
     OR NEW.kind IS DISTINCT FROM OLD.kind
     OR NEW.payload IS DISTINCT FROM OLD.payload
     OR NEW.accepted_by IS DISTINCT FROM OLD.accepted_by
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'conversation command identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversation_commands_identity_immutable
BEFORE UPDATE ON conversation_commands
FOR EACH ROW EXECUTE FUNCTION guard_conversation_command_identity();
