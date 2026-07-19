-- A project may enter the Candidate/Sandbox path before its first immutable
-- WorkspaceRevision exists. Preserve that fact as one all-or-none NULL base
-- Workspace tuple while retaining the exact RepositorySnapshot and tree fence.

ALTER TABLE sandbox_sessions
  ALTER COLUMN base_workspace_artifact_id DROP NOT NULL,
  ALTER COLUMN base_workspace_revision_id DROP NOT NULL,
  ALTER COLUMN base_workspace_content_hash DROP NOT NULL,
  ADD CONSTRAINT sandbox_sessions_base_workspace_shape CHECK (
    (
      base_workspace_artifact_id IS NULL
      AND base_workspace_revision_id IS NULL
      AND base_workspace_content_hash IS NULL
    ) OR (
      base_workspace_artifact_id IS NOT NULL
      AND base_workspace_revision_id IS NOT NULL
      AND base_workspace_content_hash IS NOT NULL
    )
  );

ALTER TABLE candidate_implementation_freezes
  ALTER COLUMN base_workspace_artifact_id DROP NOT NULL,
  ALTER COLUMN base_workspace_revision_id DROP NOT NULL,
  ALTER COLUMN base_workspace_content_hash DROP NOT NULL,
  ADD CONSTRAINT candidate_implementation_freezes_base_workspace_shape CHECK (
    (
      base_workspace_artifact_id IS NULL
      AND base_workspace_revision_id IS NULL
      AND base_workspace_content_hash IS NULL
    ) OR (
      base_workspace_artifact_id IS NOT NULL
      AND base_workspace_revision_id IS NOT NULL
      AND base_workspace_content_hash IS NOT NULL
    )
  );

-- Replace only the base-Workspace comparisons in the current function bodies.
-- Reading the already-installed definition preserves every exact tree, lease,
-- receipt, build, and verification fence from the preceding migration chain.
DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef('validate_sandbox_session_insert()'::regprocedure)
    INTO definition;
  IF strpos(definition, 'candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id') = 0
     OR strpos(definition, 'candidate.base_workspace_revision_id = NEW.base_workspace_revision_id') = 0
     OR strpos(definition, 'candidate.base_workspace_content_hash = NEW.base_workspace_content_hash') = 0
     OR strpos(definition, 'snapshot.base_workspace_artifact_id = NEW.base_workspace_artifact_id') = 0
     OR strpos(definition, 'snapshot.base_workspace_revision_id = NEW.base_workspace_revision_id') = 0
     OR strpos(definition, 'snapshot.base_workspace_content_hash = NEW.base_workspace_content_hash') = 0 THEN
    RAISE EXCEPTION 'migration 054 cannot locate the current SandboxSession base Workspace fences'
      USING ERRCODE = '55000';
  END IF;
  definition := replace(
    definition,
    'candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id',
    'candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_revision_id = NEW.base_workspace_revision_id',
    'candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_content_hash = NEW.base_workspace_content_hash',
    'candidate.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash'
  );
  definition := replace(
    definition,
    'snapshot.base_workspace_artifact_id = NEW.base_workspace_artifact_id',
    'snapshot.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id'
  );
  definition := replace(
    definition,
    'snapshot.base_workspace_revision_id = NEW.base_workspace_revision_id',
    'snapshot.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id'
  );
  definition := replace(
    definition,
    'snapshot.base_workspace_content_hash = NEW.base_workspace_content_hash',
    'snapshot.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash'
  );
  EXECUTE definition;

  SELECT pg_get_functiondef('validate_candidate_implementation_freeze()'::regprocedure)
    INTO definition;
  IF strpos(definition, 'candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id') = 0
     OR strpos(definition, 'candidate.base_workspace_revision_id = NEW.base_workspace_revision_id') = 0
     OR strpos(definition, 'candidate.base_workspace_content_hash = NEW.base_workspace_content_hash') = 0
     OR strpos(definition, 'proposal.base_workspace_revision_id = NEW.base_workspace_revision_id') = 0 THEN
    RAISE EXCEPTION 'migration 054 cannot locate the current Candidate freeze base Workspace fences'
      USING ERRCODE = '55000';
  END IF;
  definition := replace(
    definition,
    'candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id',
    'candidate.base_workspace_artifact_id IS NOT DISTINCT FROM NEW.base_workspace_artifact_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_revision_id = NEW.base_workspace_revision_id',
    'candidate.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id'
  );
  definition := replace(
    definition,
    'candidate.base_workspace_content_hash = NEW.base_workspace_content_hash',
    'candidate.base_workspace_content_hash IS NOT DISTINCT FROM NEW.base_workspace_content_hash'
  );
  definition := replace(
    definition,
    'proposal.base_workspace_revision_id = NEW.base_workspace_revision_id',
    'proposal.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id'
  );
  EXECUTE definition;

  SELECT pg_get_functiondef('validate_candidate_implementation_proposal_receipt()'::regprocedure)
    INTO definition;
  IF strpos(definition, 'receipt.base_workspace_revision_id = NEW.base_workspace_revision_id') = 0 THEN
    RAISE EXCEPTION 'migration 054 cannot locate the current Candidate Proposal receipt base Workspace fence'
      USING ERRCODE = '55000';
  END IF;
  definition := replace(
    definition,
    'receipt.base_workspace_revision_id = NEW.base_workspace_revision_id',
    'receipt.base_workspace_revision_id IS NOT DISTINCT FROM NEW.base_workspace_revision_id'
  );
  EXECUTE definition;
END;
$migration$;

COMMENT ON CONSTRAINT sandbox_sessions_base_workspace_shape ON sandbox_sessions IS
  'The exact base Workspace tuple is either wholly absent for a fresh project or wholly present for revision-backed history.';
COMMENT ON CONSTRAINT candidate_implementation_freezes_base_workspace_shape ON candidate_implementation_freezes IS
  'The exact base Workspace tuple is either wholly absent for a fresh project or wholly present for revision-backed history.';
