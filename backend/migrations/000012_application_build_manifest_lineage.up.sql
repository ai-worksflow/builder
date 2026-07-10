ALTER TABLE application_build_manifests
  ADD COLUMN root_manifest_id uuid,
  ADD COLUMN derived_from_id uuid,
  ADD COLUMN workspace_revision_id uuid,
  ADD COLUMN root_ordinal integer,
  ADD COLUMN manifest_group_key text;

-- Every manifest that predates bundle rebasing is an independent root. Its
-- workspace input cannot be reconstructed exactly, so it deliberately remains
-- NULL rather than guessing from a later proposal or workspace revision.
UPDATE application_build_manifests
SET root_manifest_id = id
WHERE root_manifest_id IS NULL;

-- Older schemas enforced only workflow_run_id -> workflow_runs(id), so a
-- historical internal caller could have persisted a manifest under one
-- project while pointing at a run owned by another. The immutable manifest
-- payload cannot be safely rewritten in SQL. Abort with an explicit diagnostic
-- before installing the composite tenant FK instead of failing later with an
-- opaque constraint error or silently reassigning provenance.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM application_build_manifests AS manifest
    JOIN workflow_runs AS run ON run.id = manifest.workflow_run_id
    WHERE manifest.workflow_run_id IS NOT NULL
      AND manifest.project_id <> run.project_id
  ) THEN
    RAISE EXCEPTION USING
      ERRCODE = '23514',
      MESSAGE = '000012 cannot migrate cross-project application build manifest workflow lineage',
      HINT = 'Quarantine or repair the affected immutable manifests before retrying the migration.';
  END IF;
END $$;

-- Before rebasing existed, every workflow manifest was a root. Preserve those
-- roots in one deterministic legacy compiler group and assign a stable order
-- so the new sequential gate does not strand in-flight workflow runs.
WITH ordered_legacy_roots AS (
  SELECT id,
         row_number() OVER (
           PARTITION BY project_id, workflow_run_id
           ORDER BY created_at ASC, id ASC
         ) - 1 AS ordinal
  FROM application_build_manifests
  WHERE workflow_run_id IS NOT NULL
)
UPDATE application_build_manifests AS manifest
SET manifest_group_key = 'legacy',
    root_ordinal = ordered_legacy_roots.ordinal::integer
FROM ordered_legacy_roots
WHERE manifest.id = ordered_legacy_roots.id;

ALTER TABLE application_build_manifests
  ALTER COLUMN root_manifest_id SET NOT NULL;

-- These keys let the self-referencing foreign keys enforce both project and
-- root membership, rather than accepting a parent from another lineage.
ALTER TABLE application_build_manifests
  ADD CONSTRAINT application_build_manifests_id_project_unique
    UNIQUE (id, project_id),
  ADD CONSTRAINT application_build_manifests_id_root_project_unique
    UNIQUE (id, root_manifest_id, project_id);

ALTER TABLE workflow_runs
  ADD CONSTRAINT workflow_runs_id_project_unique
    UNIQUE (id, project_id);

ALTER TABLE application_build_manifests
  ADD CONSTRAINT application_build_manifests_root_fk
    FOREIGN KEY (root_manifest_id, project_id)
    REFERENCES application_build_manifests (id, project_id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT application_build_manifests_derived_from_fk
    FOREIGN KEY (derived_from_id, root_manifest_id, project_id)
    REFERENCES application_build_manifests (id, root_manifest_id, project_id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT application_build_manifests_workspace_revision_fk
    FOREIGN KEY (workspace_revision_id)
    REFERENCES artifact_revisions (id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT application_build_manifests_workflow_run_project_fk
    FOREIGN KEY (workflow_run_id, project_id)
    REFERENCES workflow_runs (id, project_id)
    ON DELETE RESTRICT,
  ADD CONSTRAINT application_build_manifests_lineage_shape_check CHECK (
    (root_manifest_id = id AND derived_from_id IS NULL)
    OR
    (
      root_manifest_id <> id
      AND derived_from_id IS NOT NULL
      AND derived_from_id <> id
    )
  ),
  ADD CONSTRAINT application_build_manifests_root_ordinal_nonnegative CHECK (
    root_ordinal IS NULL OR root_ordinal >= 0
  ),
  ADD CONSTRAINT application_build_manifests_workflow_group_shape_check CHECK (
    (
      workflow_run_id IS NULL
      AND root_ordinal IS NULL
      AND manifest_group_key IS NULL
    )
    OR
    (
      workflow_run_id IS NOT NULL
      AND root_ordinal IS NOT NULL
      AND length(btrim(manifest_group_key)) BETWEEN 1 AND 200
    )
  );

CREATE INDEX application_build_manifests_root_history_idx
  ON application_build_manifests (root_manifest_id, created_at ASC, id ASC);

CREATE UNIQUE INDEX application_build_manifests_derived_from_unique
  ON application_build_manifests (derived_from_id)
  WHERE derived_from_id IS NOT NULL;

-- A workspace revision may be rebased at most once within one manifest root,
-- even when callers retry with a different Idempotency-Key.
CREATE UNIQUE INDEX application_build_manifests_root_workspace_unique
  ON application_build_manifests (root_manifest_id, workspace_revision_id)
  WHERE workspace_revision_id IS NOT NULL;

CREATE UNIQUE INDEX application_build_manifests_run_root_ordinal_unique
  ON application_build_manifests (project_id, workflow_run_id, manifest_group_key, root_ordinal)
  WHERE derived_from_id IS NULL
    AND workflow_run_id IS NOT NULL
    AND manifest_group_key IS NOT NULL
    AND root_ordinal IS NOT NULL;
