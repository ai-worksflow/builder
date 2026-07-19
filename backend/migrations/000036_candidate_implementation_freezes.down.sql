DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM candidate_implementation_freezes)
     OR EXISTS (
       SELECT 1 FROM implementation_proposals WHERE execution_source = 'candidate_freeze'
     ) THEN
    RAISE EXCEPTION 'migration 036 cannot be rolled back while Candidate freeze receipts or proposals exist'
      USING ERRCODE = '23514';
  END IF;
END;
$$;

DROP TRIGGER IF EXISTS implementation_proposal_candidate_freeze_receipt ON implementation_proposals;
DROP FUNCTION IF EXISTS validate_candidate_implementation_proposal_receipt();
DROP TRIGGER IF EXISTS implementation_proposal_candidate_identity_immutable ON implementation_proposals;
DROP FUNCTION IF EXISTS guard_candidate_implementation_proposal_identity();
DROP TRIGGER IF EXISTS candidate_implementation_freeze_immutable ON candidate_implementation_freezes;
DROP FUNCTION IF EXISTS prevent_candidate_implementation_freeze_mutation();
DROP TRIGGER IF EXISTS candidate_implementation_freeze_exact_guard ON candidate_implementation_freezes;
DROP FUNCTION IF EXISTS validate_candidate_implementation_freeze();
DROP TABLE IF EXISTS candidate_implementation_freezes;

ALTER TABLE implementation_proposals
  DROP CONSTRAINT implementation_proposals_execution_source_check,
  DROP CONSTRAINT implementation_proposals_execution_shape_check,
  DROP CONSTRAINT implementation_proposals_candidate_base_tree_hash_check,
  DROP CONSTRAINT implementation_proposals_candidate_tree_hash_check,
  DROP COLUMN candidate_snapshot_id,
  DROP COLUMN candidate_base_tree_hash,
  DROP COLUMN candidate_tree_hash,
  ADD CONSTRAINT implementation_proposals_execution_source_check CHECK (
    execution_source IN ('manual_submission', 'manual_generation', 'workflow_runner', 'conversation_command')
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
  );

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
