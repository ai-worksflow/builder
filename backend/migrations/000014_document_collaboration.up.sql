CREATE TABLE artifact_collaboration_states (
  artifact_id uuid PRIMARY KEY REFERENCES artifacts(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  version bigint NOT NULL DEFAULT 1,
  updated_by uuid NOT NULL REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT artifact_collaboration_states_version_positive CHECK (version > 0),
  UNIQUE (artifact_id, project_id)
);

CREATE INDEX artifact_collaboration_states_project_idx
  ON artifact_collaboration_states (project_id, updated_at DESC, artifact_id);

CREATE TABLE artifact_member_bindings (
  artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  role text NOT NULL,
  reason text NOT NULL DEFAULT '',
  assigned_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  assigned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (artifact_id, user_id, role),
  CONSTRAINT artifact_member_bindings_role_check CHECK (
    role IN ('owner', 'assignee', 'downstream_owner', 'reviewer', 'watcher')
  ),
  CONSTRAINT artifact_member_bindings_user_member_fk
    FOREIGN KEY (project_id, user_id)
    REFERENCES project_members (project_id, user_id)
    ON DELETE RESTRICT,
  CONSTRAINT artifact_member_bindings_assigner_member_fk
    FOREIGN KEY (project_id, assigned_by)
    REFERENCES project_members (project_id, user_id)
    ON DELETE RESTRICT
);

CREATE INDEX artifact_member_bindings_project_idx
  ON artifact_member_bindings (project_id, role, artifact_id, user_id);

ALTER TABLE output_proposals
  ADD COLUMN ai_provider text,
  ADD COLUMN ai_model text;

CREATE TABLE document_generation_commands (
  id uuid PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  command_key text NOT NULL,
  request_hash text NOT NULL,
  source_bindings_etag text NOT NULL,
  resolved_owner_ids jsonb NOT NULL,
  status text NOT NULL DEFAULT 'processing',
  target_artifact_id uuid REFERENCES artifacts(id) ON DELETE RESTRICT,
  base_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  input_manifest_id uuid REFERENCES input_manifests(id) ON DELETE RESTRICT,
  output_proposal_id uuid REFERENCES output_proposals(id) ON DELETE RESTRICT,
  provider text NOT NULL DEFAULT '',
  model text NOT NULL DEFAULT '',
  attempt_count integer NOT NULL DEFAULT 1,
  last_failure text,
  last_failed_at timestamptz,
  locked_until timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT document_generation_commands_key_check CHECK (
    char_length(command_key) BETWEEN 1 AND 200
  ),
  CONSTRAINT document_generation_commands_hash_check CHECK (
    request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  CONSTRAINT document_generation_commands_source_bindings_etag_check CHECK (
    char_length(btrim(source_bindings_etag)) BETWEEN 2 AND 300
  ),
  CONSTRAINT document_generation_commands_resolved_owners_check CHECK (
    jsonb_typeof(resolved_owner_ids) = 'array'
    AND jsonb_array_length(resolved_owner_ids) > 0
  ),
  CONSTRAINT document_generation_commands_status_check CHECK (
    status IN ('processing', 'completed')
  ),
  CONSTRAINT document_generation_commands_attempt_count_check CHECK (
    attempt_count > 0
  ),
  CONSTRAINT document_generation_commands_failure_shape_check CHECK (
    (last_failure IS NULL AND last_failed_at IS NULL)
    OR (
      last_failure IN (
        'canceled', 'deadline_exceeded', 'not_found', 'forbidden',
        'conflict', 'invalid_input', 'proposal_stale', 'blocking_gate',
        'content_not_ready', 'internal'
      )
      AND last_failed_at IS NOT NULL
    )
  ),
  CONSTRAINT document_generation_commands_completion_check CHECK (
    status <> 'completed' OR (
      target_artifact_id IS NOT NULL AND base_revision_id IS NOT NULL
      AND input_manifest_id IS NOT NULL AND output_proposal_id IS NOT NULL
      AND length(btrim(provider)) > 0 AND length(btrim(model)) > 0
    )
  ),
  CONSTRAINT document_generation_commands_checkpoint_shape_check CHECK (
    (base_revision_id IS NULL AND target_artifact_id IS NULL)
    OR (base_revision_id IS NOT NULL AND target_artifact_id IS NOT NULL)
  ),
  CONSTRAINT document_generation_commands_manifest_shape_check CHECK (
    input_manifest_id IS NULL OR (base_revision_id IS NOT NULL AND target_artifact_id IS NOT NULL)
  ),
  CONSTRAINT document_generation_commands_proposal_shape_check CHECK (
    output_proposal_id IS NULL OR input_manifest_id IS NOT NULL
  ),
  UNIQUE (project_id, actor_id, command_key),
  UNIQUE (target_artifact_id),
  UNIQUE (input_manifest_id),
  UNIQUE (output_proposal_id)
);

CREATE INDEX document_generation_commands_recovery_idx
  ON document_generation_commands (status, locked_until, updated_at)
  WHERE status = 'processing';

CREATE OR REPLACE FUNCTION validate_artifact_collaboration_tenant_refs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM artifacts AS artifact
    WHERE artifact.id = NEW.artifact_id
      AND artifact.project_id = NEW.project_id
  ) THEN
    RAISE EXCEPTION 'artifact collaboration state is not a same-project artifact reference'
      USING ERRCODE = '23503';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM project_members AS member
    WHERE member.project_id = NEW.project_id
      AND member.user_id = NEW.updated_by
  ) THEN
    RAISE EXCEPTION 'artifact collaboration updater is not a project member'
      USING ERRCODE = '23503';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM artifact_member_bindings AS binding
    WHERE binding.artifact_id = NEW.artifact_id
      AND binding.project_id = NEW.project_id
      AND binding.role = 'owner'
  ) THEN
    RAISE EXCEPTION 'persisted artifact collaboration state requires an explicit owner binding'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER artifact_collaboration_tenant_refs
BEFORE INSERT OR UPDATE ON artifact_collaboration_states
FOR EACH ROW
EXECUTE FUNCTION validate_artifact_collaboration_tenant_refs();

CREATE OR REPLACE FUNCTION validate_artifact_member_binding_tenant_refs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM artifacts
    WHERE id = NEW.artifact_id AND project_id = NEW.project_id
  ) THEN
    RAISE EXCEPTION 'artifact member binding is not a same-project artifact reference'
      USING ERRCODE = '23503';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER artifact_member_binding_tenant_refs
BEFORE INSERT OR UPDATE ON artifact_member_bindings
FOR EACH ROW
EXECUTE FUNCTION validate_artifact_member_binding_tenant_refs();

CREATE OR REPLACE FUNCTION prevent_artifact_member_binding_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(OLD.artifact_id, OLD.project_id, OLD.user_id, OLD.role)
    IS DISTINCT FROM
    ROW(NEW.artifact_id, NEW.project_id, NEW.user_id, NEW.role) THEN
    RAISE EXCEPTION 'artifact member binding identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER artifact_member_binding_identity_immutable
BEFORE UPDATE ON artifact_member_bindings
FOR EACH ROW
EXECUTE FUNCTION prevent_artifact_member_binding_identity_mutation();

CREATE OR REPLACE FUNCTION validate_artifact_member_binding_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  checked_artifact_id uuid;
BEGIN
  checked_artifact_id := COALESCE(NEW.artifact_id, OLD.artifact_id);
  IF EXISTS (
    SELECT 1 FROM artifact_collaboration_states
    WHERE artifact_id = checked_artifact_id
  ) AND NOT EXISTS (
    SELECT 1 FROM artifact_member_bindings
    WHERE artifact_id = checked_artifact_id AND role = 'owner'
  ) THEN
    RAISE EXCEPTION 'persisted artifact collaboration bindings require an owner'
      USING ERRCODE = '23514';
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE CONSTRAINT TRIGGER artifact_member_binding_owner_required
AFTER INSERT OR UPDATE OR DELETE ON artifact_member_bindings
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION validate_artifact_member_binding_owner();

CREATE OR REPLACE FUNCTION validate_document_generation_command_refs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM project_members
    WHERE project_id = NEW.project_id AND user_id = NEW.actor_id
  ) THEN
    RAISE EXCEPTION 'document generation actor is not a project member'
      USING ERRCODE = '23503';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM jsonb_array_elements_text(NEW.resolved_owner_ids) AS owner(user_id)
    WHERE NOT EXISTS (
      SELECT 1 FROM project_members
      WHERE project_id = NEW.project_id
        AND user_id = owner.user_id::uuid
    )
  ) THEN
    RAISE EXCEPTION 'resolved downstream owners must be project members'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.target_artifact_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM artifacts
    WHERE id = NEW.target_artifact_id
      AND project_id = NEW.project_id
      AND kind IN (
        'project_brief', 'product_requirements', 'decision_record',
        'glossary_policy', 'reference_source', 'change_request',
        'api_contract', 'data_contract', 'permission_contract'
      )
  ) THEN
    RAISE EXCEPTION 'document generation target is not a same-project document artifact'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.base_revision_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM artifact_revisions
    WHERE id = NEW.base_revision_id
      AND artifact_id = NEW.target_artifact_id
  ) THEN
    RAISE EXCEPTION 'document generation base revision does not belong to its target document'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.input_manifest_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM input_manifests
    WHERE id = NEW.input_manifest_id
      AND project_id = NEW.project_id
      AND kind = 'document.downstream.generate'
  ) THEN
    RAISE EXCEPTION 'document generation manifest is not a same-project downstream manifest'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.output_proposal_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM output_proposals
    WHERE id = NEW.output_proposal_id
      AND project_id = NEW.project_id
      AND artifact_id = NEW.target_artifact_id
      AND base_revision_id = NEW.base_revision_id
      AND input_manifest_id = NEW.input_manifest_id
      AND kind = 'document.downstream.generate'
      AND ai_provider = NEW.provider
      AND ai_model = NEW.model
  ) THEN
    RAISE EXCEPTION 'document generation proposal does not match its project, target, base, manifest, and provider'
      USING ERRCODE = '23503';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER document_generation_command_refs
BEFORE INSERT OR UPDATE ON document_generation_commands
FOR EACH ROW
EXECUTE FUNCTION validate_document_generation_command_refs();

CREATE OR REPLACE FUNCTION prevent_document_generation_command_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(OLD.id, OLD.project_id, OLD.actor_id, OLD.command_key, OLD.request_hash, OLD.source_bindings_etag, OLD.resolved_owner_ids, OLD.created_at)
    IS DISTINCT FROM
    ROW(NEW.id, NEW.project_id, NEW.actor_id, NEW.command_key, NEW.request_hash, NEW.source_bindings_etag, NEW.resolved_owner_ids, NEW.created_at) THEN
    RAISE EXCEPTION 'document generation command identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER document_generation_command_identity_immutable
BEFORE UPDATE ON document_generation_commands
FOR EACH ROW
EXECUTE FUNCTION prevent_document_generation_command_identity_mutation();
