ALTER TABLE workflow_intent_proposals
  ADD COLUMN desired_output_capability text NOT NULL DEFAULT 'application',
  ADD CONSTRAINT workflow_intent_proposals_desired_output_check CHECK (
    desired_output_capability = 'application'
  );

CREATE OR REPLACE FUNCTION guard_workflow_intent_desired_output_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF OLD.desired_output_capability IS DISTINCT FROM NEW.desired_output_capability THEN
    RAISE EXCEPTION 'workflow intent desired output capability is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER workflow_intent_desired_output_immutable
BEFORE UPDATE ON workflow_intent_proposals
FOR EACH ROW EXECUTE FUNCTION guard_workflow_intent_desired_output_identity();

ALTER TABLE implementation_proposals
  ADD COLUMN execution_source text NOT NULL DEFAULT 'manual_submission',
  ADD COLUMN conversation_command_id uuid REFERENCES conversation_commands(id) ON DELETE RESTRICT,
  ADD COLUMN supersedes_proposal_id uuid REFERENCES implementation_proposals(id) ON DELETE RESTRICT,
  ADD COLUMN instruction_hash text,
  ADD COLUMN ai_provider text,
  ADD COLUMN ai_model text,
  ADD CONSTRAINT implementation_proposals_execution_source_check CHECK (
    execution_source IN ('manual_submission', 'manual_generation', 'workflow_runner', 'conversation_command')
  ),
  ADD CONSTRAINT implementation_proposals_instruction_hash_check CHECK (
    instruction_hash IS NULL OR instruction_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT implementation_proposals_execution_shape_check CHECK (
    (
      execution_source = 'manual_submission'
      AND conversation_command_id IS NULL
      AND supersedes_proposal_id IS NULL
      AND instruction_hash IS NULL
      AND ai_provider IS NULL
      AND ai_model IS NULL
    ) OR (
      execution_source IN ('manual_generation', 'workflow_runner')
      AND conversation_command_id IS NULL
      AND instruction_hash IS NOT NULL
      AND ai_provider IS NOT NULL
      AND ai_model IS NOT NULL
      AND length(btrim(ai_provider)) > 0
      AND length(btrim(ai_model)) > 0
    ) OR (
      execution_source = 'conversation_command'
      AND conversation_command_id IS NOT NULL
      AND instruction_hash IS NOT NULL
      AND ai_provider IS NOT NULL
      AND ai_model IS NOT NULL
      AND length(btrim(ai_provider)) > 0
      AND length(btrim(ai_model)) > 0
    )
  ),
  ADD CONSTRAINT implementation_proposals_conversation_command_unique UNIQUE (conversation_command_id);

DO $$
DECLARE
  duplicate_leaf uuid;
  duplicate_count bigint;
BEGIN
  SELECT build_manifest_id, count(*)
    INTO duplicate_leaf, duplicate_count
  FROM implementation_proposals
  WHERE status IN ('open', 'reviewing', 'ready')
  GROUP BY build_manifest_id
  HAVING count(*) > 1
  ORDER BY build_manifest_id
  LIMIT 1;
  IF duplicate_leaf IS NOT NULL THEN
    RAISE EXCEPTION 'migration 015 cannot enforce one active proposal per Workbench leaf: leaf % has % active proposals; explicitly reject or mark obsolete proposals stale before retrying', duplicate_leaf, duplicate_count
      USING ERRCODE = '23514', DETAIL = 'No proposal was modified automatically because review decisions and lineage must not be guessed.';
  END IF;
END;
$$;

CREATE UNIQUE INDEX implementation_proposals_one_active_per_leaf_idx
  ON implementation_proposals (build_manifest_id)
  WHERE status IN ('open', 'reviewing', 'ready');

CREATE TABLE implementation_generation_claims (
  id uuid PRIMARY KEY,
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  root_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE CASCADE,
  request_key uuid NOT NULL,
  reserved_proposal_id uuid NOT NULL,
  execution_source text NOT NULL,
  conversation_command_id uuid REFERENCES conversation_commands(id) ON DELETE RESTRICT,
  governance_manifest_id uuid REFERENCES input_manifests(id) ON DELETE RESTRICT,
  governance_manifest_hash text,
  governance_source_refs jsonb,
  instruction jsonb NOT NULL,
  instruction_hash text NOT NULL,
  requested_model text NOT NULL,
  generation_contract_version text NOT NULL,
  system_prompt_hash text NOT NULL,
  output_schema_hash text NOT NULL,
  actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  expected_active_proposal_id uuid REFERENCES implementation_proposals(id) ON DELETE RESTRICT,
  expected_active_proposal_version bigint,
  claim_token uuid,
  claim_expires_at timestamptz,
  status text NOT NULL DEFAULT 'processing',
  attempt_count integer NOT NULL DEFAULT 1,
  completed_proposal_id uuid REFERENCES implementation_proposals(id) ON DELETE RESTRICT,
  last_failure text,
  last_failed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT implementation_generation_claims_source_check CHECK (
    execution_source IN ('manual_generation', 'workflow_runner', 'conversation_command')
  ),
  CONSTRAINT implementation_generation_claims_hash_check CHECK (
    instruction_hash ~ '^sha256:[0-9a-f]{64}$'
    AND system_prompt_hash ~ '^sha256:[0-9a-f]{64}$'
    AND output_schema_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  CONSTRAINT implementation_generation_claims_instruction_shape_check CHECK (
    jsonb_typeof(instruction) = 'object'
    AND instruction ? 'objective'
    AND instruction ? 'constraints'
    AND instruction - 'objective' - 'constraints' = '{}'::jsonb
    AND jsonb_typeof(instruction -> 'objective') = 'string'
    AND jsonb_typeof(instruction -> 'constraints') = 'array'
    AND length(btrim(requested_model)) > 0
    AND length(btrim(generation_contract_version)) > 0
  ),
  CONSTRAINT implementation_generation_claims_status_check CHECK (
    status IN ('processing', 'completed', 'failed')
  ),
  CONSTRAINT implementation_generation_claims_attempt_check CHECK (attempt_count > 0),
  CONSTRAINT implementation_generation_claims_supersede_shape_check CHECK (
    (expected_active_proposal_id IS NULL AND expected_active_proposal_version IS NULL)
    OR (expected_active_proposal_id IS NOT NULL AND expected_active_proposal_version > 0)
  ),
  CONSTRAINT implementation_generation_claims_command_shape_check CHECK (
    (execution_source = 'conversation_command' AND conversation_command_id IS NOT NULL
      AND request_key = conversation_command_id AND reserved_proposal_id = conversation_command_id)
    OR
    (execution_source IN ('manual_generation', 'workflow_runner') AND conversation_command_id IS NULL)
  ),
  CONSTRAINT implementation_generation_claims_governance_shape_check CHECK (
    (
      execution_source = 'conversation_command'
      AND governance_manifest_id IS NOT NULL
      AND governance_manifest_hash ~ '^sha256:[0-9a-f]{64}$'
      AND jsonb_typeof(governance_source_refs) = 'array'
      AND jsonb_array_length(governance_source_refs) > 0
    ) OR (
      execution_source IN ('manual_generation', 'workflow_runner')
      AND governance_manifest_id IS NULL
      AND governance_manifest_hash IS NULL
      AND governance_source_refs IS NULL
    )
  ),
  CONSTRAINT implementation_generation_claims_lease_shape_check CHECK (
    (status = 'processing' AND claim_token IS NOT NULL AND claim_expires_at IS NOT NULL
      AND completed_proposal_id IS NULL)
    OR
    (status = 'completed' AND claim_token IS NULL AND claim_expires_at IS NULL
      AND completed_proposal_id = reserved_proposal_id AND last_failure IS NULL AND last_failed_at IS NULL)
    OR
    (status = 'failed' AND claim_token IS NULL AND claim_expires_at IS NULL
      AND completed_proposal_id IS NULL AND last_failure IS NOT NULL AND last_failed_at IS NOT NULL)
  ),
  CONSTRAINT implementation_generation_claims_failure_check CHECK (
    last_failure IS NULL OR last_failure IN (
      'canceled', 'deadline_exceeded', 'not_found', 'forbidden', 'conflict',
      'invalid_input', 'proposal_stale', 'blocking_gate', 'content_not_ready',
      'ai_not_configured', 'ai_rate_limited', 'ai_unavailable',
      'ai_invalid_output', 'ai_context_too_long', 'internal'
    )
  ),
  UNIQUE (request_key),
  UNIQUE (reserved_proposal_id),
  UNIQUE (conversation_command_id)
);

CREATE UNIQUE INDEX implementation_generation_claims_one_processing_leaf_idx
  ON implementation_generation_claims (build_manifest_id)
  WHERE status = 'processing';

CREATE INDEX implementation_generation_claims_recovery_idx
  ON implementation_generation_claims (status, claim_expires_at, updated_at)
  WHERE status IN ('processing', 'failed');

CREATE OR REPLACE FUNCTION validate_implementation_generation_tenant_refs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  leaf_project_id uuid;
  leaf_root_id uuid;
  leaf_run_id uuid;
  root_project_id uuid;
  root_run_id uuid;
  run_definition_version_id uuid;
  command_project_id uuid;
  command_kind text;
  command_status text;
  command_payload jsonb;
  command_proposal_project_id uuid;
  command_proposal_kind text;
  command_proposal_status text;
  command_proposal_definition_version_id uuid;
  governance_project_id uuid;
  governance_manifest_hash text;
BEGIN
  SELECT project_id, COALESCE(NULLIF(root_manifest_id, '00000000-0000-0000-0000-000000000000'::uuid), id), workflow_run_id
    INTO leaf_project_id, leaf_root_id, leaf_run_id
  FROM application_build_manifests
  WHERE id = NEW.build_manifest_id;

  IF leaf_project_id IS NULL OR leaf_project_id <> NEW.project_id OR leaf_root_id <> NEW.root_manifest_id THEN
    RAISE EXCEPTION 'implementation generation claim is not bound to its same-project manifest root'
      USING ERRCODE = '23503';
  END IF;
  IF NEW.status = 'processing' AND EXISTS (
    SELECT 1 FROM application_build_manifests child
    WHERE child.derived_from_id = NEW.build_manifest_id
  ) THEN
    RAISE EXCEPTION 'implementation generation claim must target the active lineage leaf'
      USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM project_members
    WHERE project_id = NEW.project_id AND user_id = NEW.actor_id
  ) THEN
    RAISE EXCEPTION 'implementation generation actor is not a project member'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.conversation_command_id IS NOT NULL THEN
    SELECT command.project_id, command.kind, command.status, command.payload,
           proposal.project_id, proposal.kind, proposal.status, proposal.suggested_definition_version_id
      INTO command_project_id, command_kind, command_status, command_payload,
           command_proposal_project_id, command_proposal_kind, command_proposal_status,
           command_proposal_definition_version_id
    FROM conversation_commands AS command
    LEFT JOIN workflow_intent_proposals AS proposal ON proposal.id = command.proposal_id
    WHERE command.id = NEW.conversation_command_id;
    IF command_project_id IS NULL OR command_project_id <> NEW.project_id
      OR command_kind <> 'workbench_instruction'
      OR command_proposal_project_id IS NULL OR command_proposal_project_id <> NEW.project_id
      OR command_proposal_kind <> 'workbench_instruction'
      OR command_proposal_status <> 'accepted' THEN
      RAISE EXCEPTION 'implementation generation command is not backed by an accepted same-project Workbench proposal'
        USING ERRCODE = '23503';
    END IF;
    IF (
      TG_OP = 'INSERT'
      OR NEW.status = 'processing'
      OR (TG_OP = 'UPDATE' AND OLD.status IS DISTINCT FROM 'completed' AND NEW.status = 'completed')
    ) AND command_status IS DISTINCT FROM 'pending' THEN
      RAISE EXCEPTION 'conversation implementation generation claim requires a pending command'
        USING ERRCODE = '23514';
    END IF;
    SELECT project_id, workflow_run_id INTO root_project_id, root_run_id
    FROM application_build_manifests
    WHERE id = NEW.root_manifest_id;
    SELECT definition_version_id INTO run_definition_version_id
    FROM workflow_runs
    WHERE id = leaf_run_id AND project_id = NEW.project_id;
    IF leaf_run_id IS NULL
      OR root_project_id IS NULL OR root_project_id <> NEW.project_id
      OR root_run_id IS DISTINCT FROM leaf_run_id
      OR leaf_run_id::text IS DISTINCT FROM command_payload #>> '{workbench,expectedRunId}'
      OR NEW.root_manifest_id::text IS DISTINCT FROM command_payload #>> '{workbench,expectedBundleId}'
      OR run_definition_version_id IS NULL
      OR run_definition_version_id::text IS DISTINCT FROM command_payload #>> '{definitionVersionId}'
      OR command_proposal_definition_version_id IS DISTINCT FROM run_definition_version_id THEN
      RAISE EXCEPTION 'implementation generation target differs from the immutable accepted Workbench command'
        USING ERRCODE = '23514';
    END IF;
    SELECT manifest.project_id, manifest.manifest_hash INTO governance_project_id, governance_manifest_hash
    FROM input_manifests AS manifest WHERE manifest.id = NEW.governance_manifest_id;
    IF governance_project_id IS NULL OR governance_project_id <> NEW.project_id
      OR governance_manifest_hash <> NEW.governance_manifest_hash
      OR command_payload #>> '{manifestIntent,inputManifest,id}' <> NEW.governance_manifest_id::text
      OR command_payload #>> '{manifestIntent,inputManifest,hash}' <> NEW.governance_manifest_hash
      OR command_payload -> 'sourceRefs' IS DISTINCT FROM NEW.governance_source_refs
      OR NEW.instruction IS DISTINCT FROM jsonb_build_object(
        'objective', command_payload #>> '{workbench,objective}',
        'constraints', COALESCE(command_payload #> '{workbench,constraints}', '[]'::jsonb)
      ) THEN
      RAISE EXCEPTION 'implementation generation governance input differs from the immutable conversation command'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  IF NEW.status = 'processing' AND NEW.expected_active_proposal_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM implementation_proposals AS active
    WHERE active.id = NEW.expected_active_proposal_id
      AND active.project_id = NEW.project_id
      AND active.build_manifest_id = NEW.build_manifest_id
      AND active.version = NEW.expected_active_proposal_version
      AND active.status = 'open'
      AND active.execution_source <> 'conversation_command'
      AND active.accepted_count = 0
      AND active.rejected_count = 0
      AND active.applied_at IS NULL
      AND NOT EXISTS (
        SELECT 1 FROM implementation_operation_decisions decision
        WHERE decision.proposal_id = active.id
      )
  ) THEN
    RAISE EXCEPTION 'implementation generation supersede target is not the exact undecided open proposal'
      USING ERRCODE = '23514';
  END IF;
  IF NEW.status = 'processing' AND NEW.expected_active_proposal_id IS NULL AND EXISTS (
    SELECT 1 FROM implementation_proposals AS active
    WHERE active.build_manifest_id = NEW.build_manifest_id
      AND active.status IN ('open', 'reviewing', 'ready')
  ) THEN
    RAISE EXCEPTION 'implementation generation without supersede CAS found an active proposal'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION guard_implementation_generation_claim_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(
    OLD.build_manifest_id, OLD.project_id, OLD.root_manifest_id,
    OLD.request_key, OLD.reserved_proposal_id, OLD.execution_source, OLD.conversation_command_id,
    OLD.governance_manifest_id, OLD.governance_manifest_hash, OLD.governance_source_refs,
    OLD.instruction, OLD.instruction_hash, OLD.requested_model,
    OLD.generation_contract_version, OLD.system_prompt_hash, OLD.output_schema_hash,
    OLD.expected_active_proposal_id, OLD.expected_active_proposal_version, OLD.created_at
  ) IS DISTINCT FROM ROW(
    NEW.build_manifest_id, NEW.project_id, NEW.root_manifest_id,
    NEW.request_key, NEW.reserved_proposal_id, NEW.execution_source, NEW.conversation_command_id,
    NEW.governance_manifest_id, NEW.governance_manifest_hash, NEW.governance_source_refs,
    NEW.instruction, NEW.instruction_hash, NEW.requested_model,
    NEW.generation_contract_version, NEW.system_prompt_hash, NEW.output_schema_hash,
    NEW.expected_active_proposal_id, NEW.expected_active_proposal_version, NEW.created_at
  ) THEN
    RAISE EXCEPTION 'implementation generation replay identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  IF OLD.actor_id IS DISTINCT FROM NEW.actor_id AND OLD.execution_source <> 'conversation_command' THEN
    RAISE EXCEPTION 'non-conversation implementation generation actor is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER implementation_generation_claim_identity_immutable
BEFORE UPDATE ON implementation_generation_claims
FOR EACH ROW EXECUTE FUNCTION guard_implementation_generation_claim_identity();

CREATE TRIGGER implementation_generation_tenant_refs
BEFORE INSERT OR UPDATE ON implementation_generation_claims
FOR EACH ROW EXECUTE FUNCTION validate_implementation_generation_tenant_refs();

CREATE OR REPLACE FUNCTION guard_implementation_proposal_generation_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(
    OLD.project_id, OLD.build_manifest_id, OLD.created_by, OLD.created_at,
    OLD.execution_source, OLD.conversation_command_id, OLD.supersedes_proposal_id, OLD.instruction_hash,
    OLD.ai_provider, OLD.ai_model
  ) IS DISTINCT FROM ROW(
    NEW.project_id, NEW.build_manifest_id, NEW.created_by, NEW.created_at,
    NEW.execution_source, NEW.conversation_command_id, NEW.supersedes_proposal_id, NEW.instruction_hash,
    NEW.ai_provider, NEW.ai_model
  ) THEN
    RAISE EXCEPTION 'implementation proposal generation identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER implementation_proposal_generation_identity_immutable
BEFORE UPDATE ON implementation_proposals
FOR EACH ROW EXECUTE FUNCTION guard_implementation_proposal_generation_identity();

CREATE OR REPLACE FUNCTION validate_implementation_proposal_generation_refs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  command_project_id uuid;
  command_kind text;
  command_status text;
  manifest_project_id uuid;
BEGIN
  SELECT project_id INTO manifest_project_id
  FROM application_build_manifests WHERE id = NEW.build_manifest_id;
  IF manifest_project_id IS NULL OR manifest_project_id <> NEW.project_id THEN
    RAISE EXCEPTION 'implementation proposal is not bound to a same-project build manifest'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.conversation_command_id IS NOT NULL THEN
    SELECT project_id, kind, status INTO command_project_id, command_kind, command_status
    FROM conversation_commands WHERE id = NEW.conversation_command_id;
    IF command_project_id IS NULL OR command_project_id <> NEW.project_id OR command_kind <> 'workbench_instruction' THEN
      RAISE EXCEPTION 'implementation proposal command is not a same-project Workbench command'
        USING ERRCODE = '23503';
    END IF;
    IF command_status IS NULL OR command_status NOT IN ('pending', 'executed')
      OR (TG_OP = 'INSERT' AND command_status <> 'pending') THEN
      RAISE EXCEPTION 'conversation implementation proposal command is not executable'
        USING ERRCODE = '23514';
    END IF;
    IF command_status = 'pending' AND (
      NEW.status <> 'open'
      OR NEW.accepted_count <> 0
      OR NEW.rejected_count <> 0
      OR NEW.applied_at IS NOT NULL
      OR NEW.applied_by IS NOT NULL
    ) THEN
      RAISE EXCEPTION 'conversation implementation proposal cannot become reviewable before command receipt'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  IF TG_OP = 'INSERT' AND NEW.execution_source <> 'manual_submission' AND NOT EXISTS (
    SELECT 1 FROM implementation_generation_claims claim
    WHERE claim.reserved_proposal_id = NEW.id
      AND claim.project_id = NEW.project_id
      AND claim.build_manifest_id = NEW.build_manifest_id
      AND claim.execution_source = NEW.execution_source
      AND claim.conversation_command_id IS NOT DISTINCT FROM NEW.conversation_command_id
      AND claim.expected_active_proposal_id IS NOT DISTINCT FROM NEW.supersedes_proposal_id
      AND claim.instruction_hash = NEW.instruction_hash
      AND claim.actor_id = NEW.created_by
      AND claim.status = 'processing'
      AND claim.claim_token IS NOT NULL
      AND claim.claim_expires_at >= now()
      AND (
        (
          claim.expected_active_proposal_id IS NULL
          AND NOT EXISTS (
            SELECT 1 FROM implementation_proposals active
            WHERE active.build_manifest_id = NEW.build_manifest_id
              AND active.status IN ('open', 'reviewing', 'ready')
          )
        ) OR (
          claim.expected_active_proposal_id IS NOT NULL
          AND EXISTS (
            SELECT 1 FROM implementation_proposals superseded
            WHERE superseded.id = claim.expected_active_proposal_id
              AND superseded.project_id = NEW.project_id
              AND superseded.build_manifest_id = NEW.build_manifest_id
              AND superseded.status = 'stale'
              AND superseded.version = claim.expected_active_proposal_version + 1
          )
        )
      )
  ) THEN
    RAISE EXCEPTION 'generated implementation proposal has no matching live generation claim'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER implementation_proposal_generation_refs
BEFORE INSERT OR UPDATE ON implementation_proposals
FOR EACH ROW EXECUTE FUNCTION validate_implementation_proposal_generation_refs();

CREATE OR REPLACE FUNCTION guard_conversation_implementation_decision_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  target_proposal_id uuid;
  proposal_execution_source text;
  command_status text;
BEGIN
  IF TG_OP = 'DELETE' THEN
    target_proposal_id := OLD.proposal_id;
  ELSE
    target_proposal_id := NEW.proposal_id;
  END IF;
  SELECT proposal.execution_source, command.status
    INTO proposal_execution_source, command_status
  FROM implementation_proposals AS proposal
  LEFT JOIN conversation_commands AS command ON command.id = proposal.conversation_command_id
  WHERE proposal.id = target_proposal_id;
  IF proposal_execution_source = 'conversation_command' AND command_status IS DISTINCT FROM 'executed' THEN
    RAISE EXCEPTION 'conversation implementation decisions require a committed command receipt'
      USING ERRCODE = '23514';
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER conversation_implementation_decision_receipt_gate
BEFORE INSERT OR UPDATE OR DELETE ON implementation_operation_decisions
FOR EACH ROW EXECUTE FUNCTION guard_conversation_implementation_decision_receipt();
