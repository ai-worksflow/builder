ALTER TABLE conversations
  ADD CONSTRAINT conversations_checkpoint_tenant_identity_unique UNIQUE (id, project_id);

ALTER TABLE conversation_messages
  ADD CONSTRAINT conversation_messages_checkpoint_identity_unique
    UNIQUE (id, conversation_id, sequence);

CREATE TABLE conversation_summary_checkpoints (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL,
  conversation_id uuid NOT NULL,
  previous_checkpoint_id uuid,
  through_message_id uuid NOT NULL,
  through_sequence bigint NOT NULL,
  message_count bigint NOT NULL,
  content_bytes bigint NOT NULL,
  prefix_hash bytea NOT NULL,
  hash_algorithm text NOT NULL DEFAULT 'conversation-prefix-chain/v1',
  summary text NOT NULL,
  summary_hash bytea NOT NULL,
  status text NOT NULL DEFAULT 'pending_review',
  version bigint NOT NULL DEFAULT 1,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  reviewed_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  reviewed_at timestamptz,
  review_reason text NOT NULL DEFAULT '',
  CONSTRAINT conversation_summary_checkpoints_tenant_fk
    FOREIGN KEY (conversation_id, project_id)
    REFERENCES conversations(id, project_id) ON DELETE CASCADE,
  CONSTRAINT conversation_summary_checkpoints_message_fk
    FOREIGN KEY (through_message_id, conversation_id, through_sequence)
    REFERENCES conversation_messages(id, conversation_id, sequence) ON DELETE RESTRICT,
  CONSTRAINT conversation_summary_checkpoints_identity_unique
    UNIQUE (id, conversation_id),
  CONSTRAINT conversation_summary_checkpoints_tenant_identity_unique
    UNIQUE (id, conversation_id, project_id),
  CONSTRAINT conversation_summary_checkpoints_previous_fk
    FOREIGN KEY (previous_checkpoint_id, conversation_id)
    REFERENCES conversation_summary_checkpoints(id, conversation_id) ON DELETE RESTRICT,
  CONSTRAINT conversation_summary_checkpoints_sequence_positive CHECK (through_sequence > 0),
  CONSTRAINT conversation_summary_checkpoints_complete_prefix CHECK (
    message_count = through_sequence AND message_count > 0
  ),
  CONSTRAINT conversation_summary_checkpoints_content_bytes_check CHECK (
    content_bytes >= message_count
  ),
  CONSTRAINT conversation_summary_checkpoints_prefix_hash_check CHECK (
    octet_length(prefix_hash) = 32
  ),
  CONSTRAINT conversation_summary_checkpoints_hash_algorithm_check CHECK (
    hash_algorithm = 'conversation-prefix-chain/v1'
  ),
  CONSTRAINT conversation_summary_checkpoints_summary_check CHECK (
    length(trim(summary)) > 0 AND octet_length(summary) <= 32768
  ),
  CONSTRAINT conversation_summary_checkpoints_summary_hash_check CHECK (
    octet_length(summary_hash) = 32
  ),
  CONSTRAINT conversation_summary_checkpoints_status_check CHECK (
    status IN ('pending_review', 'approved', 'rejected', 'superseded')
  ),
  CONSTRAINT conversation_summary_checkpoints_version_positive CHECK (version > 0),
  CONSTRAINT conversation_summary_checkpoints_review_reason_check CHECK (
    octet_length(review_reason) <= 4000
  ),
  CONSTRAINT conversation_summary_checkpoints_no_self_review CHECK (
    reviewed_by IS NULL OR reviewed_by <> created_by
  ),
  CONSTRAINT conversation_summary_checkpoints_review_shape CHECK (
    (
      status = 'pending_review'
      AND reviewed_by IS NULL
      AND reviewed_at IS NULL
      AND review_reason = ''
    ) OR (
      status IN ('approved', 'rejected')
      AND reviewed_by IS NOT NULL
      AND reviewed_at IS NOT NULL
    ) OR (
      status = 'superseded'
      AND reviewed_by IS NULL
      AND reviewed_at IS NOT NULL
      AND length(trim(review_reason)) > 0
    )
  )
);

ALTER TABLE conversations
  ADD COLUMN summary_checkpoint_head_id uuid,
  ADD CONSTRAINT conversations_summary_checkpoint_head_fk
    FOREIGN KEY (summary_checkpoint_head_id, id)
    REFERENCES conversation_summary_checkpoints(id, conversation_id) ON DELETE RESTRICT;

ALTER TABLE workflow_intent_proposals
  ADD COLUMN summary_checkpoint_id uuid,
  ADD COLUMN conversation_context jsonb,
  ADD COLUMN provider_input_hash bytea;

UPDATE workflow_intent_proposals
   SET conversation_context = '{"version":0,"mode":"legacy_unrecorded"}'::jsonb;

ALTER TABLE workflow_intent_proposals
  ALTER COLUMN conversation_context SET NOT NULL,
  ADD CONSTRAINT workflow_intent_proposals_summary_checkpoint_fk
    FOREIGN KEY (summary_checkpoint_id, conversation_id, project_id)
    REFERENCES conversation_summary_checkpoints(id, conversation_id, project_id) ON DELETE RESTRICT,
  ADD CONSTRAINT workflow_intent_proposals_conversation_context_object CHECK (
    jsonb_typeof(conversation_context) = 'object'
  ),
  ADD CONSTRAINT workflow_intent_proposals_provider_input_hash_check CHECK (
    provider_input_hash IS NULL OR octet_length(provider_input_hash) = 32
  ),
  ADD CONSTRAINT workflow_intent_proposals_conversation_context_shape CHECK (
    (
      conversation_context->>'mode' = 'legacy_unrecorded'
      AND conversation_context->>'version' = '0'
      AND summary_checkpoint_id IS NULL
      AND provider_input_hash IS NULL
    ) OR (
      origin = 'submitted'
      AND
      conversation_context->>'mode' = 'submitted'
      AND conversation_context->>'version' = '1'
      AND summary_checkpoint_id IS NULL
      AND provider_input_hash IS NULL
    ) OR (
      origin = 'ai'
      AND
      conversation_context->>'mode' = 'full_prefix'
      AND conversation_context->>'version' = '1'
      AND summary_checkpoint_id IS NULL
      AND provider_input_hash IS NOT NULL
      AND octet_length(provider_input_hash) = 32
    ) OR (
      origin = 'ai'
      AND
      conversation_context->>'mode' = 'checkpoint_tail'
      AND conversation_context->>'version' = '1'
      AND summary_checkpoint_id IS NOT NULL
      AND provider_input_hash IS NOT NULL
      AND octet_length(provider_input_hash) = 32
    )
  );

ALTER TABLE conversation_commands
  ADD COLUMN summary_checkpoint_id uuid,
  ADD COLUMN conversation_context jsonb,
  ADD COLUMN provider_input_hash bytea;

UPDATE conversation_commands
   SET conversation_context = '{"version":0,"mode":"legacy_unrecorded"}'::jsonb;

ALTER TABLE conversation_commands
  ALTER COLUMN conversation_context SET NOT NULL,
  ADD CONSTRAINT conversation_commands_summary_checkpoint_fk
    FOREIGN KEY (summary_checkpoint_id, conversation_id, project_id)
    REFERENCES conversation_summary_checkpoints(id, conversation_id, project_id) ON DELETE RESTRICT,
  ADD CONSTRAINT conversation_commands_conversation_context_object CHECK (
    jsonb_typeof(conversation_context) = 'object'
  ),
  ADD CONSTRAINT conversation_commands_provider_input_hash_check CHECK (
    provider_input_hash IS NULL OR octet_length(provider_input_hash) = 32
  );

CREATE FUNCTION validate_new_workflow_intent_conversation_context() RETURNS trigger AS $$
BEGIN
  IF (
    NEW.conversation_context->>'version' = '1'
    AND (
      (
        NEW.origin = 'submitted'
        AND NEW.conversation_context->>'mode' = 'submitted'
        AND NEW.summary_checkpoint_id IS NULL
        AND NEW.provider_input_hash IS NULL
      ) OR (
        NEW.origin = 'ai'
        AND octet_length(NEW.provider_input_hash) = 32
        AND (
          (
            NEW.conversation_context->>'mode' = 'full_prefix'
            AND NEW.summary_checkpoint_id IS NULL
          ) OR (
            NEW.conversation_context->>'mode' = 'checkpoint_tail'
            AND NEW.summary_checkpoint_id IS NOT NULL
          )
        )
      )
    )
  ) IS NOT TRUE THEN
    RAISE EXCEPTION 'new workflow intent proposal requires explicit conversation context provenance'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workflow_intent_new_conversation_context
BEFORE INSERT ON workflow_intent_proposals
FOR EACH ROW EXECUTE FUNCTION validate_new_workflow_intent_conversation_context();

CREATE FUNCTION guard_workflow_intent_conversation_context_identity() RETURNS trigger AS $$
BEGIN
  IF NEW.summary_checkpoint_id IS DISTINCT FROM OLD.summary_checkpoint_id
     OR NEW.conversation_context IS DISTINCT FROM OLD.conversation_context
     OR NEW.provider_input_hash IS DISTINCT FROM OLD.provider_input_hash THEN
    RAISE EXCEPTION 'workflow intent conversation context is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workflow_intent_conversation_context_immutable
BEFORE UPDATE ON workflow_intent_proposals
FOR EACH ROW EXECUTE FUNCTION guard_workflow_intent_conversation_context_identity();

CREATE FUNCTION validate_conversation_command_context() RETURNS trigger AS $$
DECLARE
  proposal_checkpoint_id uuid;
  proposal_context jsonb;
  proposal_input_hash bytea;
BEGIN
  SELECT summary_checkpoint_id, conversation_context, provider_input_hash
    INTO proposal_checkpoint_id, proposal_context, proposal_input_hash
    FROM workflow_intent_proposals
   WHERE id = NEW.proposal_id
     AND project_id = NEW.project_id
     AND conversation_id = NEW.conversation_id;
  IF NOT FOUND
     OR NEW.summary_checkpoint_id IS DISTINCT FROM proposal_checkpoint_id
     OR NEW.conversation_context IS DISTINCT FROM proposal_context
     OR NEW.provider_input_hash IS DISTINCT FROM proposal_input_hash THEN
    RAISE EXCEPTION 'conversation command context must match its accepted proposal'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversation_commands_context_match
BEFORE INSERT ON conversation_commands
FOR EACH ROW EXECUTE FUNCTION validate_conversation_command_context();

CREATE FUNCTION guard_conversation_command_context_identity() RETURNS trigger AS $$
BEGIN
  IF NEW.summary_checkpoint_id IS DISTINCT FROM OLD.summary_checkpoint_id
     OR NEW.conversation_context IS DISTINCT FROM OLD.conversation_context
     OR NEW.provider_input_hash IS DISTINCT FROM OLD.provider_input_hash THEN
    RAISE EXCEPTION 'conversation command conversation context is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversation_command_context_immutable
BEFORE UPDATE ON conversation_commands
FOR EACH ROW EXECUTE FUNCTION guard_conversation_command_context_identity();

CREATE INDEX conversation_summary_checkpoints_history_idx
  ON conversation_summary_checkpoints (conversation_id, created_at DESC, id DESC);

CREATE INDEX conversation_summary_checkpoints_approved_resolution_idx
  ON conversation_summary_checkpoints (conversation_id, through_sequence DESC)
  WHERE status = 'approved';

CREATE UNIQUE INDEX conversation_summary_checkpoints_approved_coverage_idx
  ON conversation_summary_checkpoints (conversation_id, through_sequence)
  WHERE status = 'approved';

CREATE UNIQUE INDEX conversation_summary_checkpoints_approved_child_idx
  ON conversation_summary_checkpoints (previous_checkpoint_id)
  WHERE status = 'approved' AND previous_checkpoint_id IS NOT NULL;

CREATE UNIQUE INDEX conversation_summary_checkpoints_approved_genesis_idx
  ON conversation_summary_checkpoints (conversation_id)
  WHERE status = 'approved' AND previous_checkpoint_id IS NULL;

CREATE FUNCTION guard_new_conversation_summary_checkpoint() RETURNS trigger AS $$
BEGIN
  IF NEW.status <> 'pending_review'
     OR NEW.version <> 1
     OR NEW.reviewed_by IS NOT NULL
     OR NEW.reviewed_at IS NOT NULL
     OR NEW.review_reason <> '' THEN
    RAISE EXCEPTION 'new conversation summary checkpoint must begin pending independent review'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversation_summary_checkpoints_pending_insert
BEFORE INSERT ON conversation_summary_checkpoints
FOR EACH ROW EXECUTE FUNCTION guard_new_conversation_summary_checkpoint();

CREATE FUNCTION guard_conversation_summary_checkpoint_mutation() RETURNS trigger AS $$
DECLARE
  current_head_id uuid;
  predecessor_status text;
  predecessor_sequence bigint;
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.project_id IS DISTINCT FROM OLD.project_id
     OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
     OR NEW.previous_checkpoint_id IS DISTINCT FROM OLD.previous_checkpoint_id
     OR NEW.through_message_id IS DISTINCT FROM OLD.through_message_id
     OR NEW.through_sequence IS DISTINCT FROM OLD.through_sequence
     OR NEW.message_count IS DISTINCT FROM OLD.message_count
     OR NEW.content_bytes IS DISTINCT FROM OLD.content_bytes
     OR NEW.prefix_hash IS DISTINCT FROM OLD.prefix_hash
     OR NEW.hash_algorithm IS DISTINCT FROM OLD.hash_algorithm
     OR NEW.summary IS DISTINCT FROM OLD.summary
     OR NEW.summary_hash IS DISTINCT FROM OLD.summary_hash
     OR NEW.created_by IS DISTINCT FROM OLD.created_by
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'conversation summary checkpoint identity is immutable'
      USING ERRCODE = '55000';
  END IF;

  IF OLD.status <> 'pending_review'
     OR NEW.status NOT IN ('approved', 'rejected', 'superseded')
     OR NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'invalid conversation summary checkpoint transition'
      USING ERRCODE = '55000';
  END IF;

  IF NEW.status = 'approved' THEN
    SELECT summary_checkpoint_head_id
      INTO current_head_id
      FROM conversations
     WHERE id = NEW.conversation_id
       AND project_id = NEW.project_id;
    IF NOT FOUND
       OR NEW.previous_checkpoint_id IS DISTINCT FROM current_head_id THEN
      RAISE EXCEPTION 'approved conversation summary checkpoint must extend the current head'
        USING ERRCODE = '55000';
    END IF;
    IF NEW.previous_checkpoint_id IS NOT NULL THEN
      SELECT status, through_sequence
        INTO predecessor_status, predecessor_sequence
        FROM conversation_summary_checkpoints
       WHERE id = NEW.previous_checkpoint_id
         AND conversation_id = NEW.conversation_id;
      IF NOT FOUND
         OR predecessor_status <> 'approved'
         OR predecessor_sequence >= NEW.through_sequence THEN
        RAISE EXCEPTION 'conversation summary checkpoint predecessor is not an approved earlier prefix'
          USING ERRCODE = '55000';
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversation_summary_checkpoints_controlled_mutation
BEFORE UPDATE ON conversation_summary_checkpoints
FOR EACH ROW EXECUTE FUNCTION guard_conversation_summary_checkpoint_mutation();

CREATE FUNCTION guard_conversation_summary_checkpoint_head() RETURNS trigger AS $$
DECLARE
  checkpoint_status text;
  checkpoint_previous_id uuid;
BEGIN
  IF NEW.summary_checkpoint_head_id IS DISTINCT FROM OLD.summary_checkpoint_head_id THEN
    IF NEW.summary_checkpoint_head_id IS NULL THEN
      RAISE EXCEPTION 'conversation summary checkpoint head cannot be cleared or rolled back'
        USING ERRCODE = '55000';
    END IF;
    SELECT status, previous_checkpoint_id
      INTO checkpoint_status, checkpoint_previous_id
      FROM conversation_summary_checkpoints
     WHERE id = NEW.summary_checkpoint_head_id
       AND conversation_id = OLD.id
       AND project_id = OLD.project_id;
    IF NOT FOUND
       OR checkpoint_status <> 'approved'
       OR checkpoint_previous_id IS DISTINCT FROM OLD.summary_checkpoint_head_id THEN
      RAISE EXCEPTION 'conversation summary checkpoint head must advance by one approved child'
        USING ERRCODE = '55000';
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversations_summary_checkpoint_head_forward_only
BEFORE UPDATE OF summary_checkpoint_head_id ON conversations
FOR EACH ROW EXECUTE FUNCTION guard_conversation_summary_checkpoint_head();

CREATE FUNCTION require_approved_summary_checkpoint_is_head() RETURNS trigger AS $$
DECLARE
  current_head_id uuid;
BEGIN
  IF NEW.status = 'approved' THEN
    SELECT summary_checkpoint_head_id
      INTO current_head_id
      FROM conversations
     WHERE id = NEW.conversation_id
       AND project_id = NEW.project_id;
    IF NOT FOUND OR current_head_id IS DISTINCT FROM NEW.id THEN
      RAISE EXCEPTION 'approved conversation summary checkpoint was not installed as the conversation head'
        USING ERRCODE = '55000';
    END IF;
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER conversation_summary_checkpoint_head_commit
AFTER INSERT OR UPDATE OF status ON conversation_summary_checkpoints
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION require_approved_summary_checkpoint_is_head();

CREATE FUNCTION reject_conversation_summary_checkpoint_delete() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'conversation summary checkpoints are immutable'
    USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER conversation_summary_checkpoints_immutable_delete
BEFORE DELETE ON conversation_summary_checkpoints
FOR EACH ROW EXECUTE FUNCTION reject_conversation_summary_checkpoint_delete();
