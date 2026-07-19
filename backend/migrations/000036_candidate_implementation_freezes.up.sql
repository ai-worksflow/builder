ALTER TABLE implementation_proposals
  DROP CONSTRAINT implementation_proposals_execution_source_check,
  DROP CONSTRAINT implementation_proposals_execution_shape_check,
  ADD COLUMN candidate_snapshot_id uuid REFERENCES candidate_snapshots(id) ON DELETE RESTRICT,
  ADD COLUMN candidate_base_tree_hash text,
  ADD COLUMN candidate_tree_hash text,
  ADD CONSTRAINT implementation_proposals_candidate_base_tree_hash_check CHECK (
    candidate_base_tree_hash IS NULL OR candidate_base_tree_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT implementation_proposals_candidate_tree_hash_check CHECK (
    candidate_tree_hash IS NULL OR candidate_tree_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT implementation_proposals_execution_source_check CHECK (
    execution_source IN (
      'manual_submission', 'manual_generation', 'workflow_runner',
      'conversation_command', 'candidate_freeze'
    )
  ),
  ADD CONSTRAINT implementation_proposals_execution_shape_check CHECK (
    (
      execution_source = 'manual_submission'
      AND conversation_command_id IS NULL
      AND supersedes_proposal_id IS NULL
      AND instruction_hash IS NULL
      AND ai_provider IS NULL
      AND ai_model IS NULL
      AND candidate_snapshot_id IS NULL
      AND candidate_base_tree_hash IS NULL
      AND candidate_tree_hash IS NULL
    ) OR (
      execution_source IN ('manual_generation', 'workflow_runner')
      AND conversation_command_id IS NULL
      AND instruction_hash IS NOT NULL
      AND ai_provider IS NOT NULL
      AND ai_model IS NOT NULL
      AND length(btrim(ai_provider)) > 0
      AND length(btrim(ai_model)) > 0
      AND candidate_snapshot_id IS NULL
      AND candidate_base_tree_hash IS NULL
      AND candidate_tree_hash IS NULL
    ) OR (
      execution_source = 'conversation_command'
      AND conversation_command_id IS NOT NULL
      AND instruction_hash IS NOT NULL
      AND ai_provider IS NOT NULL
      AND ai_model IS NOT NULL
      AND length(btrim(ai_provider)) > 0
      AND length(btrim(ai_model)) > 0
      AND candidate_snapshot_id IS NULL
      AND candidate_base_tree_hash IS NULL
      AND candidate_tree_hash IS NULL
    ) OR (
      execution_source = 'candidate_freeze'
      AND conversation_command_id IS NULL
      AND supersedes_proposal_id IS NULL
      AND instruction_hash IS NULL
      AND ai_provider IS NULL
      AND ai_model IS NULL
      AND candidate_snapshot_id IS NOT NULL
      AND candidate_base_tree_hash IS NOT NULL
      AND candidate_tree_hash IS NOT NULL
    )
  );

CREATE TABLE candidate_implementation_freezes (
  id uuid PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  session_id uuid NOT NULL REFERENCES sandbox_sessions(id) ON DELETE RESTRICT,
  candidate_id uuid NOT NULL REFERENCES candidate_workspaces(id) ON DELETE RESTRICT,
  candidate_snapshot_id uuid NOT NULL REFERENCES candidate_snapshots(id) ON DELETE RESTRICT,
  implementation_proposal_id uuid NOT NULL UNIQUE
    REFERENCES implementation_proposals(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  request_key text NOT NULL CHECK (
    request_key = btrim(request_key) AND length(request_key) BETWEEN 1 AND 128
  ),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  session_version bigint NOT NULL CHECK (session_version > 0),
  candidate_version bigint NOT NULL CHECK (candidate_version > 0),
  journal_sequence bigint NOT NULL CHECK (journal_sequence >= 0),
  session_epoch bigint NOT NULL CHECK (session_epoch > 0),
  writer_lease_epoch bigint NOT NULL CHECK (writer_lease_epoch > 0),
  base_tree_hash text NOT NULL CHECK (base_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  candidate_tree_store text NOT NULL CHECK (
    candidate_tree_store = btrim(candidate_tree_store) AND length(candidate_tree_store) > 0
  ),
  candidate_tree_owner_id uuid NOT NULL,
  candidate_tree_ref text NOT NULL CHECK (
    candidate_tree_ref = btrim(candidate_tree_ref) AND length(candidate_tree_ref) > 0
  ),
  candidate_tree_content_hash text NOT NULL CHECK (
    candidate_tree_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  candidate_tree_hash text NOT NULL CHECK (candidate_tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  build_manifest_id uuid NOT NULL REFERENCES application_build_manifests(id) ON DELETE RESTRICT,
  build_manifest_hash text NOT NULL CHECK (build_manifest_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  build_contract_id uuid NOT NULL REFERENCES application_build_contracts(id) ON DELETE RESTRICT,
  build_contract_hash text NOT NULL CHECK (build_contract_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  full_stack_template_id uuid NOT NULL,
  full_stack_template_hash text NOT NULL CHECK (full_stack_template_hash ~ '^sha256:[0-9a-f]{64}$'),
  base_workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  base_workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  base_workspace_content_hash text NOT NULL CHECK (
    base_workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  proposal_payload_hash text NOT NULL CHECK (proposal_payload_hash ~ '^(sha256:)?[0-9a-f]{64}$'),
  operation_count integer NOT NULL CHECK (operation_count BETWEEN 1 AND 40000),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT candidate_implementation_freeze_request_unique UNIQUE (project_id, request_key),
  CONSTRAINT candidate_implementation_freeze_snapshot_unique UNIQUE (candidate_snapshot_id),
  CONSTRAINT candidate_implementation_freeze_tree_owner CHECK (candidate_tree_owner_id = candidate_id)
);

CREATE INDEX candidate_implementation_freezes_candidate_created_idx
  ON candidate_implementation_freezes (candidate_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION validate_candidate_implementation_freeze()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM candidate_workspaces AS candidate
    JOIN candidate_snapshots AS snapshot
      ON snapshot.id = NEW.candidate_snapshot_id
     AND snapshot.project_id = candidate.project_id
     AND snapshot.candidate_id = candidate.id
    JOIN repository_snapshots AS base
      ON base.id = candidate.repository_snapshot_id
     AND base.project_id = candidate.project_id
    JOIN sandbox_sessions AS session
      ON session.id = NEW.session_id
     AND session.project_id = candidate.project_id
     AND session.candidate_id = candidate.id
    JOIN implementation_proposals AS proposal
      ON proposal.id = NEW.implementation_proposal_id
     AND proposal.project_id = candidate.project_id
    WHERE candidate.id = NEW.candidate_id
      AND candidate.project_id = NEW.project_id
      AND candidate.status = 'active'
      AND candidate.version = NEW.candidate_version
      AND candidate.journal_sequence = NEW.journal_sequence
      AND candidate.session_epoch = NEW.session_epoch
      AND candidate.writer_lease_epoch = NEW.writer_lease_epoch
      AND candidate.writer_lease_owner_id = NEW.created_by
      AND candidate.writer_lease_expires_at > statement_timestamp()
      AND NOT candidate.conflicted
      AND NOT candidate.stale
      AND NOT candidate.rebase_required
      AND candidate.base_tree_hash = NEW.base_tree_hash
      AND candidate.current_tree_store = NEW.candidate_tree_store
      AND candidate.current_tree_owner_id = NEW.candidate_tree_owner_id
      AND candidate.current_tree_ref = NEW.candidate_tree_ref
      AND candidate.current_tree_content_hash = NEW.candidate_tree_content_hash
      AND candidate.current_tree_hash = NEW.candidate_tree_hash
      AND candidate.build_manifest_id = NEW.build_manifest_id
      AND candidate.build_manifest_hash = NEW.build_manifest_hash
      AND candidate.build_contract_id = NEW.build_contract_id
      AND candidate.build_contract_hash = NEW.build_contract_hash
      AND candidate.full_stack_template_id = NEW.full_stack_template_id
      AND candidate.full_stack_template_hash = NEW.full_stack_template_hash
      AND candidate.base_workspace_artifact_id = NEW.base_workspace_artifact_id
      AND candidate.base_workspace_revision_id = NEW.base_workspace_revision_id
      AND candidate.base_workspace_content_hash = NEW.base_workspace_content_hash
      AND snapshot.candidate_version = NEW.candidate_version
      AND snapshot.journal_sequence = NEW.journal_sequence
      AND snapshot.session_epoch = NEW.session_epoch
      AND snapshot.writer_lease_epoch = NEW.writer_lease_epoch
      AND snapshot.tree_store = NEW.candidate_tree_store
      AND snapshot.tree_owner_id = NEW.candidate_tree_owner_id
      AND snapshot.tree_ref = NEW.candidate_tree_ref
      AND snapshot.tree_content_hash = NEW.candidate_tree_content_hash
      AND snapshot.tree_hash = NEW.candidate_tree_hash
      AND base.tree_hash = NEW.base_tree_hash
      AND base.build_manifest_id = NEW.build_manifest_id
      AND base.build_manifest_hash = NEW.build_manifest_hash
      AND base.build_contract_id = NEW.build_contract_id
      AND base.build_contract_hash = NEW.build_contract_hash
      AND session.state = 'ready'
      AND session.version = NEW.session_version
      AND session.session_epoch = NEW.session_epoch
      AND session.candidate_version = NEW.candidate_version
      AND session.candidate_journal_sequence = NEW.journal_sequence
      AND session.candidate_writer_lease_epoch = NEW.writer_lease_epoch
      AND session.candidate_tree_hash = NEW.candidate_tree_hash
      AND session.latest_checkpoint_id = NEW.candidate_snapshot_id
      AND proposal.build_manifest_id = NEW.build_manifest_id
      AND proposal.application_build_contract_id = NEW.build_contract_id
      AND proposal.application_build_contract_hash = NEW.build_contract_hash
      AND proposal.base_workspace_revision_id = NEW.base_workspace_revision_id
      AND proposal.execution_source = 'candidate_freeze'
      AND proposal.candidate_snapshot_id = NEW.candidate_snapshot_id
      AND proposal.candidate_base_tree_hash = NEW.base_tree_hash
      AND proposal.candidate_tree_hash = NEW.candidate_tree_hash
      AND proposal.payload_hash = NEW.proposal_payload_hash
      AND proposal.operation_count = NEW.operation_count
      AND proposal.status = 'open'
      AND proposal.version = 1
      AND proposal.created_by = NEW.created_by
  ) THEN
    RAISE EXCEPTION 'Candidate freeze must bind one exact live session, checkpoint, tree, lineage, and proposal'
      USING ERRCODE = '40001';
  END IF;

  NEW.created_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER candidate_implementation_freeze_exact_guard
BEFORE INSERT ON candidate_implementation_freezes
FOR EACH ROW EXECUTE FUNCTION validate_candidate_implementation_freeze();

CREATE OR REPLACE FUNCTION prevent_candidate_implementation_freeze_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Candidate implementation freeze receipts are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER candidate_implementation_freeze_immutable
BEFORE UPDATE OR DELETE ON candidate_implementation_freezes
FOR EACH ROW EXECUTE FUNCTION prevent_candidate_implementation_freeze_mutation();

CREATE OR REPLACE FUNCTION guard_candidate_implementation_proposal_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF ROW(
    OLD.candidate_snapshot_id, OLD.candidate_base_tree_hash, OLD.candidate_tree_hash
  ) IS DISTINCT FROM ROW(
    NEW.candidate_snapshot_id, NEW.candidate_base_tree_hash, NEW.candidate_tree_hash
  ) THEN
    RAISE EXCEPTION 'implementation proposal Candidate source identity is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER implementation_proposal_candidate_identity_immutable
BEFORE UPDATE ON implementation_proposals
FOR EACH ROW EXECUTE FUNCTION guard_candidate_implementation_proposal_identity();

CREATE OR REPLACE FUNCTION validate_candidate_implementation_proposal_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.execution_source = 'candidate_freeze' AND NOT EXISTS (
    SELECT 1
    FROM candidate_implementation_freezes AS receipt
    JOIN candidate_workspace_control_events AS event
      ON event.candidate_id = receipt.candidate_id
     AND event.event_kind = 'candidate.frozen'
     AND event.candidate_snapshot_id = receipt.candidate_snapshot_id
     AND event.candidate_version_from = receipt.candidate_version
     AND event.candidate_version_to = receipt.candidate_version + 1
     AND event.actor_id = receipt.created_by
    WHERE receipt.implementation_proposal_id = NEW.id
      AND receipt.project_id = NEW.project_id
      AND receipt.build_manifest_id = NEW.build_manifest_id
      AND receipt.build_contract_id = NEW.application_build_contract_id
      AND receipt.build_contract_hash = NEW.application_build_contract_hash
      AND receipt.base_workspace_revision_id = NEW.base_workspace_revision_id
      AND receipt.candidate_snapshot_id = NEW.candidate_snapshot_id
      AND receipt.base_tree_hash = NEW.candidate_base_tree_hash
      AND receipt.candidate_tree_hash = NEW.candidate_tree_hash
      AND receipt.proposal_payload_hash = NEW.payload_hash
      AND receipt.operation_count = NEW.operation_count
  ) THEN
    RAISE EXCEPTION 'candidate implementation proposal requires its exact committed freeze receipt and control event'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER implementation_proposal_candidate_freeze_receipt
AFTER INSERT ON implementation_proposals
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_candidate_implementation_proposal_receipt();

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
  IF TG_OP = 'INSERT'
     AND NEW.execution_source NOT IN ('manual_submission', 'candidate_freeze')
     AND NOT EXISTS (
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

COMMENT ON TABLE candidate_implementation_freezes IS
  'Immutable exact receipt that atomically binds a CandidateSnapshot, governed implementation Proposal, Candidate freeze event, and Sandbox projection.';

REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON candidate_implementation_freezes FROM PUBLIC;
