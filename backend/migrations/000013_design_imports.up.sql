CREATE TABLE design_imports (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_kind text NOT NULL CHECK (source_kind IN ('figma', 'penpot', 'excalidraw', 'tldraw', 'storybook', 'ladle', 'upload')),
    source_mode text NOT NULL CHECK (source_mode IN ('upload', 'remote_url')),
    source_name text NOT NULL,
    source_url text,
    file_name text,
    media_type text NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    raw_content_hash text NOT NULL CHECK (raw_content_hash LIKE 'sha256:%'),
    snapshot_store text NOT NULL DEFAULT 'mongo',
    snapshot_ref text NOT NULL,
    snapshot_content_hash text NOT NULL CHECK (snapshot_content_hash LIKE 'sha256:%'),
    snapshot_schema_version integer NOT NULL DEFAULT 1 CHECK (snapshot_schema_version > 0),
    selected_frame_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(selected_frame_ids) = 'array'),
    page_spec_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
    page_spec_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
    page_spec_content_hash text NOT NULL CHECK (page_spec_content_hash LIKE 'sha256:%'),
    creates_prototype boolean NOT NULL,
    expected_prototype_artifact_id uuid NOT NULL,
    expected_base_revision_id uuid NOT NULL,
    expected_input_manifest_id uuid NOT NULL,
    expected_output_proposal_id uuid NOT NULL,
    prototype_artifact_id uuid REFERENCES artifacts(id) ON DELETE RESTRICT,
    base_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
    input_manifest_id uuid REFERENCES input_manifests(id) ON DELETE RESTRICT,
    output_proposal_id uuid REFERENCES output_proposals(id) ON DELETE RESTRICT,
    operation_id text,
    applied_revision_id uuid REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
    pipeline_stage text NOT NULL DEFAULT 'snapshot_frozen' CHECK (
        pipeline_stage IN ('snapshot_frozen', 'target_frozen', 'manifest_frozen', 'proposal_ready')
    ),
    create_claim_token uuid,
    create_claimed_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    create_claimed_at timestamptz,
    create_claim_expires_at timestamptz,
    status text NOT NULL CHECK (status IN ('creating', 'open', 'applying', 'applied', 'rejected', 'failed')),
    failure_code text,
    failure_detail text,
    request_key_hash text NOT NULL CHECK (length(request_key_hash) = 64),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    decided_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    CONSTRAINT design_import_project_request_unique UNIQUE (project_id, request_key_hash),
    CONSTRAINT design_import_snapshot_ref_unique UNIQUE (snapshot_ref),
    CONSTRAINT design_import_proposal_unique UNIQUE (output_proposal_id),
    CONSTRAINT design_import_expected_manifest_unique UNIQUE (expected_input_manifest_id),
    CONSTRAINT design_import_expected_proposal_unique UNIQUE (expected_output_proposal_id),
    CONSTRAINT design_import_expected_identity CHECK (
        (prototype_artifact_id IS NULL OR prototype_artifact_id = expected_prototype_artifact_id)
        AND (base_revision_id IS NULL OR base_revision_id = expected_base_revision_id)
        AND (input_manifest_id IS NULL OR input_manifest_id = expected_input_manifest_id)
        AND (output_proposal_id IS NULL OR output_proposal_id = expected_output_proposal_id)
    ),
    CONSTRAINT design_import_pipeline_shape CHECK (
        (pipeline_stage = 'snapshot_frozen'
            AND prototype_artifact_id IS NULL AND base_revision_id IS NULL
            AND input_manifest_id IS NULL AND output_proposal_id IS NULL AND operation_id IS NULL)
        OR
        (pipeline_stage = 'target_frozen'
            AND prototype_artifact_id IS NOT NULL AND base_revision_id IS NOT NULL
            AND input_manifest_id IS NULL AND output_proposal_id IS NULL AND operation_id IS NULL)
        OR
        (pipeline_stage = 'manifest_frozen'
            AND prototype_artifact_id IS NOT NULL AND base_revision_id IS NOT NULL
            AND input_manifest_id IS NOT NULL AND output_proposal_id IS NULL AND operation_id IS NULL)
        OR
        (pipeline_stage = 'proposal_ready'
            AND prototype_artifact_id IS NOT NULL AND base_revision_id IS NOT NULL
            AND input_manifest_id IS NOT NULL AND output_proposal_id IS NOT NULL AND operation_id IS NOT NULL)
    ),
    CONSTRAINT design_import_status_stage_shape CHECK (
        (status = 'creating' AND pipeline_stage <> 'proposal_ready')
        OR status = 'failed'
        OR (status IN ('open', 'applying', 'applied', 'rejected') AND pipeline_stage = 'proposal_ready')
    ),
    CONSTRAINT design_import_claim_shape CHECK (
        (create_claim_token IS NULL AND create_claimed_by IS NULL AND create_claimed_at IS NULL AND create_claim_expires_at IS NULL)
        OR
        (create_claim_token IS NOT NULL AND create_claimed_by IS NOT NULL AND create_claimed_at IS NOT NULL
            AND create_claim_expires_at IS NOT NULL AND create_claim_expires_at > create_claimed_at
            AND status = 'creating' AND pipeline_stage <> 'proposal_ready')
    ),
    CONSTRAINT design_import_failure_shape CHECK (
        (status = 'failed' AND failure_code IS NOT NULL AND failure_detail IS NOT NULL)
        OR (status <> 'failed' AND failure_code IS NULL AND failure_detail IS NULL)
    ),
    CONSTRAINT design_import_independent_reviewer CHECK (
        decided_by IS NULL OR decided_by <> created_by
    ),
    CONSTRAINT design_import_remote_url_shape CHECK (
        (source_mode = 'upload' AND source_url IS NULL AND file_name IS NOT NULL)
        OR
        (source_mode = 'remote_url' AND source_url IS NOT NULL AND file_name IS NULL)
    )
);

CREATE INDEX design_imports_project_created_idx
    ON design_imports (project_id, created_at DESC, id DESC);

CREATE INDEX design_imports_project_status_idx
    ON design_imports (project_id, status, updated_at DESC);

CREATE INDEX design_imports_recoverable_claim_idx
    ON design_imports (create_claim_expires_at, id)
    WHERE status IN ('creating', 'failed') AND pipeline_stage <> 'proposal_ready';

CREATE INDEX design_imports_prototype_idx
    ON design_imports (prototype_artifact_id, updated_at DESC)
    WHERE prototype_artifact_id IS NOT NULL;

CREATE OR REPLACE FUNCTION validate_design_import_tenant_refs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM artifacts AS artifact
        JOIN artifact_revisions AS revision
          ON revision.artifact_id = artifact.id
        WHERE artifact.id = NEW.page_spec_artifact_id
          AND artifact.project_id = NEW.project_id
          AND artifact.kind = 'page_spec'
          AND revision.id = NEW.page_spec_revision_id
          AND revision.content_hash = NEW.page_spec_content_hash
    ) THEN
        RAISE EXCEPTION 'design import PageSpec revision is not an exact same-project reference'
            USING ERRCODE = '23503';
    END IF;

    IF NEW.prototype_artifact_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM artifacts AS artifact
        WHERE artifact.id = NEW.prototype_artifact_id
          AND artifact.project_id = NEW.project_id
          AND artifact.kind = 'prototype'
    ) THEN
        RAISE EXCEPTION 'design import Prototype is not a same-project reference'
            USING ERRCODE = '23503';
    END IF;

    IF NEW.create_claimed_by IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM project_members AS member
        WHERE member.project_id = NEW.project_id
          AND member.user_id = NEW.create_claimed_by
          AND member.role IN ('owner', 'admin', 'editor')
    ) THEN
        RAISE EXCEPTION 'design import creation claim is not held by a same-project editor'
            USING ERRCODE = '23503';
    END IF;

    IF NEW.base_revision_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM artifact_revisions AS revision
        WHERE revision.id = NEW.base_revision_id
          AND revision.artifact_id = NEW.prototype_artifact_id
          AND EXISTS (
              SELECT 1
              FROM artifact_revision_sources AS source
              WHERE source.revision_id = revision.id
                AND source.source_artifact_id = NEW.page_spec_artifact_id
                AND source.source_revision_id = NEW.page_spec_revision_id
                AND source.source_content_hash = NEW.page_spec_content_hash
                AND source.source_anchor_id IS NULL
                AND source.purpose = 'page_spec'
                AND source.required = true
          )
    ) THEN
        RAISE EXCEPTION 'design import base revision does not belong to its Prototype'
            USING ERRCODE = '23503';
    END IF;

    IF NEW.input_manifest_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM input_manifests AS manifest
        WHERE manifest.id = NEW.input_manifest_id
          AND manifest.project_id = NEW.project_id
          AND manifest.kind = 'design_import_to_prototype'
    ) THEN
        RAISE EXCEPTION 'design import manifest is not a same-project design import manifest'
            USING ERRCODE = '23503';
    END IF;

    IF NEW.output_proposal_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM output_proposals AS proposal
        WHERE proposal.id = NEW.output_proposal_id
          AND proposal.project_id = NEW.project_id
          AND proposal.artifact_id = NEW.prototype_artifact_id
          AND proposal.input_manifest_id = NEW.input_manifest_id
          AND proposal.base_revision_id = NEW.base_revision_id
          AND proposal.kind = 'design_import_to_prototype'
    ) THEN
        RAISE EXCEPTION 'design import proposal does not match its project, Prototype, manifest, and base revision'
            USING ERRCODE = '23503';
    END IF;

    IF NEW.applied_revision_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM artifact_revisions AS revision
        WHERE revision.id = NEW.applied_revision_id
          AND revision.artifact_id = NEW.prototype_artifact_id
          AND revision.proposal_id = NEW.output_proposal_id
          AND revision.source_manifest_id = NEW.input_manifest_id
    ) THEN
        RAISE EXCEPTION 'design import applied revision does not preserve its Prototype, proposal, and manifest lineage'
            USING ERRCODE = '23503';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER design_import_tenant_refs
BEFORE INSERT OR UPDATE ON design_imports
FOR EACH ROW
EXECUTE FUNCTION validate_design_import_tenant_refs();

CREATE OR REPLACE FUNCTION design_import_stage_rank(stage text)
RETURNS integer
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT CASE stage
        WHEN 'snapshot_frozen' THEN 1
        WHEN 'target_frozen' THEN 2
        WHEN 'manifest_frozen' THEN 3
        WHEN 'proposal_ready' THEN 4
        ELSE 0
    END
$$;

CREATE OR REPLACE FUNCTION validate_design_import_state_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'design import mutations require an exact next version' USING ERRCODE = '40001';
    END IF;

    IF design_import_stage_rank(NEW.pipeline_stage) < design_import_stage_rank(OLD.pipeline_stage) THEN
        RAISE EXCEPTION 'design import pipeline stages cannot move backwards' USING ERRCODE = '55000';
    END IF;

    IF OLD.status IN ('applied', 'rejected') AND NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'terminal design import decisions are immutable' USING ERRCODE = '55000';
    END IF;

    IF (OLD.prototype_artifact_id IS NOT NULL AND NEW.prototype_artifact_id IS DISTINCT FROM OLD.prototype_artifact_id)
       OR (OLD.base_revision_id IS NOT NULL AND NEW.base_revision_id IS DISTINCT FROM OLD.base_revision_id)
       OR (OLD.input_manifest_id IS NOT NULL AND NEW.input_manifest_id IS DISTINCT FROM OLD.input_manifest_id)
       OR (OLD.output_proposal_id IS NOT NULL AND NEW.output_proposal_id IS DISTINCT FROM OLD.output_proposal_id)
       OR (OLD.operation_id IS NOT NULL AND NEW.operation_id IS DISTINCT FROM OLD.operation_id)
       OR (OLD.applied_revision_id IS NOT NULL AND NEW.applied_revision_id IS DISTINCT FROM OLD.applied_revision_id)
       OR (OLD.decided_by IS NOT NULL AND NEW.decided_by IS DISTINCT FROM OLD.decided_by)
       OR (OLD.decided_at IS NOT NULL AND NEW.decided_at IS DISTINCT FROM OLD.decided_at) THEN
        RAISE EXCEPTION 'design import checkpoint identities are immutable once recorded' USING ERRCODE = '55000';
    END IF;

    IF OLD.create_claim_token IS NOT NULL
       AND NEW.create_claim_token IS NOT NULL
       AND NEW.create_claim_token <> OLD.create_claim_token
       AND OLD.create_claim_expires_at > statement_timestamp() THEN
        RAISE EXCEPTION 'an active design import creation lease cannot be replaced' USING ERRCODE = '40001';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER design_import_state_transition
BEFORE UPDATE ON design_imports
FOR EACH ROW
EXECUTE FUNCTION validate_design_import_state_transition();

CREATE OR REPLACE FUNCTION prohibit_design_import_creator_proposal_decision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM design_imports AS design_import
        WHERE design_import.project_id = (
            SELECT proposal.project_id FROM output_proposals AS proposal WHERE proposal.id = NEW.proposal_id
        )
          AND (design_import.output_proposal_id = NEW.proposal_id OR design_import.expected_output_proposal_id = NEW.proposal_id)
          AND design_import.created_by = NEW.decided_by
    ) THEN
        RAISE EXCEPTION 'design import creator cannot decide its conversion proposal' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER design_import_proposal_independent_decision
BEFORE INSERT OR UPDATE ON proposal_operation_decisions
FOR EACH ROW
EXECUTE FUNCTION prohibit_design_import_creator_proposal_decision();

CREATE OR REPLACE FUNCTION prohibit_design_import_creator_proposal_apply()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.kind = 'design_import_to_prototype'
       AND NEW.status IN ('applied', 'partially_applied')
       AND EXISTS (
           SELECT 1
           FROM design_imports AS design_import
           WHERE design_import.project_id = NEW.project_id
             AND (design_import.output_proposal_id = NEW.id OR design_import.expected_output_proposal_id = NEW.id)
             AND (NEW.applied_by IS NULL OR design_import.created_by = NEW.applied_by)
       ) THEN
        RAISE EXCEPTION 'design import creator cannot apply its conversion proposal' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER design_import_proposal_independent_apply
BEFORE UPDATE ON output_proposals
FOR EACH ROW
EXECUTE FUNCTION prohibit_design_import_creator_proposal_apply();

CREATE OR REPLACE FUNCTION prevent_design_import_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(
        OLD.project_id,
        OLD.source_kind,
        OLD.source_mode,
        OLD.source_name,
        OLD.source_url,
        OLD.file_name,
        OLD.media_type,
        OLD.byte_size,
        OLD.raw_content_hash,
        OLD.snapshot_store,
        OLD.snapshot_ref,
        OLD.snapshot_content_hash,
        OLD.snapshot_schema_version,
        OLD.selected_frame_ids,
        OLD.page_spec_artifact_id,
        OLD.page_spec_revision_id,
        OLD.page_spec_content_hash,
        OLD.creates_prototype,
        OLD.expected_prototype_artifact_id,
        OLD.expected_base_revision_id,
        OLD.expected_input_manifest_id,
        OLD.expected_output_proposal_id,
        OLD.created_by,
        OLD.created_at
    ) IS DISTINCT FROM ROW(
        NEW.project_id,
        NEW.source_kind,
        NEW.source_mode,
        NEW.source_name,
        NEW.source_url,
        NEW.file_name,
        NEW.media_type,
        NEW.byte_size,
        NEW.raw_content_hash,
        NEW.snapshot_store,
        NEW.snapshot_ref,
        NEW.snapshot_content_hash,
        NEW.snapshot_schema_version,
        NEW.selected_frame_ids,
        NEW.page_spec_artifact_id,
        NEW.page_spec_revision_id,
        NEW.page_spec_content_hash,
        NEW.creates_prototype,
        NEW.expected_prototype_artifact_id,
        NEW.expected_base_revision_id,
        NEW.expected_input_manifest_id,
        NEW.expected_output_proposal_id,
        NEW.created_by,
        NEW.created_at
    ) THEN
        RAISE EXCEPTION 'design import snapshot fields are immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER design_import_snapshot_immutable
BEFORE UPDATE ON design_imports
FOR EACH ROW
EXECUTE FUNCTION prevent_design_import_snapshot_mutation();
