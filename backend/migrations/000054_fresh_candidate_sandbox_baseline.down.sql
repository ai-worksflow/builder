-- Restoring NOT NULL would invent a Workspace baseline for fresh rows. Refuse
-- rollback until those rows have been explicitly handled by an operator.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM sandbox_sessions
    WHERE base_workspace_artifact_id IS NULL
       OR base_workspace_revision_id IS NULL
       OR base_workspace_content_hash IS NULL
  ) OR EXISTS (
    SELECT 1
    FROM candidate_implementation_freezes
    WHERE base_workspace_artifact_id IS NULL
       OR base_workspace_revision_id IS NULL
       OR base_workspace_content_hash IS NULL
  ) THEN
    RAISE EXCEPTION 'migration 054 rollback requires explicit handling of fresh Candidate/Sandbox rows without a base Workspace revision'
      USING ERRCODE = '23514';
  END IF;
END;
$$;

-- Restore only the equality operators changed by the up migration. Every
-- other current function-body fence remains byte-for-byte inherited.
DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef('validate_sandbox_session_insert()'::regprocedure)
    INTO definition;
  IF strpos(definition, 'candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id') = 0
     OR strpos(definition, 'candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id') = 0
     OR strpos(definition, 'candidate.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash') = 0
     OR strpos(definition, 'snapshot.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id') = 0
     OR strpos(definition, 'snapshot.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id') = 0
     OR strpos(definition, 'snapshot.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash') = 0 THEN
    RAISE EXCEPTION 'migration 054 rollback cannot locate the nullable SandboxSession base Workspace fences'
      USING ERRCODE = '55000';
  END IF;
  definition := replace(
    definition,
    'candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id',
    'candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id',
    'candidate.base_workspace_revision_id = NEW.base_workspace_revision_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash',
    'candidate.base_workspace_content_hash = NEW.base_workspace_content_hash'
  );
  definition := replace(
    definition,
    'snapshot.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id',
    'snapshot.base_workspace_artifact_id = NEW.base_workspace_artifact_id'
  );
  definition := replace(
    definition,
    'snapshot.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id',
    'snapshot.base_workspace_revision_id = NEW.base_workspace_revision_id'
  );
  definition := replace(
    definition,
    'snapshot.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash',
    'snapshot.base_workspace_content_hash = NEW.base_workspace_content_hash'
  );
  EXECUTE definition;

  SELECT pg_get_functiondef('validate_candidate_implementation_freeze()'::regprocedure)
    INTO definition;
  IF strpos(definition, 'candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id') = 0
     OR strpos(definition, 'candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id') = 0
     OR strpos(definition, 'candidate.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash') = 0
     OR strpos(definition, 'proposal.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id') = 0 THEN
    RAISE EXCEPTION 'migration 054 rollback cannot locate the nullable Candidate freeze base Workspace fences'
      USING ERRCODE = '55000';
  END IF;
  definition := replace(
    definition,
    'candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id',
    'candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id',
    'candidate.base_workspace_revision_id = NEW.base_workspace_revision_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash',
    'candidate.base_workspace_content_hash = NEW.base_workspace_content_hash'
  );
  definition := replace(
    definition,
    'proposal.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id',
    'proposal.base_workspace_revision_id = NEW.base_workspace_revision_id'
  );
  EXECUTE definition;

  SELECT pg_get_functiondef('validate_candidate_implementation_proposal_receipt()'::regprocedure)
    INTO definition;
  IF strpos(definition, 'receipt.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id') = 0 THEN
    RAISE EXCEPTION 'migration 054 rollback cannot locate the nullable Candidate Proposal receipt base Workspace fence'
      USING ERRCODE = '55000';
  END IF;
  definition := replace(
    definition,
    'receipt.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id',
    'receipt.base_workspace_revision_id = NEW.base_workspace_revision_id'
  );
  EXECUTE definition;
END;
$migration$;

ALTER TABLE candidate_implementation_freezes
  DROP CONSTRAINT IF EXISTS candidate_implementation_freezes_base_workspace_shape,
  ALTER COLUMN base_workspace_artifact_id SET NOT NULL,
  ALTER COLUMN base_workspace_revision_id SET NOT NULL,
  ALTER COLUMN base_workspace_content_hash SET NOT NULL;

ALTER TABLE sandbox_sessions
  DROP CONSTRAINT IF EXISTS sandbox_sessions_base_workspace_shape,
  ALTER COLUMN base_workspace_artifact_id SET NOT NULL,
  ALTER COLUMN base_workspace_revision_id SET NOT NULL,
  ALTER COLUMN base_workspace_content_hash SET NOT NULL;
