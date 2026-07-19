-- A BuildManifest is consumed by the same transaction that creates its
-- approved implementation WorkspaceRevision. RepositorySnapshot creation is
-- necessarily subsequent to that transaction, so requiring the mutable
-- manifest status to remain `frozen` made the production bootstrap path
-- impossible. Bind a consumed manifest only to the exact WorkspaceRevision
-- produced by its applied ImplementationProposal; the pre-apply frozen path
-- remains bound to manifest.workspace_revision_id.
CREATE OR REPLACE FUNCTION validate_repository_snapshot_reference()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM application_build_manifests AS manifest
    JOIN application_build_contracts AS contract
      ON contract.id = NEW.build_contract_id
     AND contract.project_id = manifest.project_id
     AND contract.build_manifest_id = manifest.id
     AND contract.build_manifest_hash = manifest.manifest_hash
    WHERE manifest.id = NEW.build_manifest_id
      AND manifest.project_id = NEW.project_id
      AND manifest.manifest_hash = NEW.build_manifest_hash
      AND manifest.status IN ('frozen', 'consumed')
      AND contract.contract_hash = NEW.build_contract_hash
      AND contract.status = 'ready'
      AND contract.full_stack_template_id = NEW.full_stack_template_id
      AND contract.full_stack_template_hash = NEW.full_stack_template_hash
      AND (
        (NEW.base_workspace_revision_id IS NULL
          AND manifest.status = 'frozen'
          AND manifest.workspace_revision_id IS NULL)
        OR
        (NEW.base_workspace_revision_id = manifest.workspace_revision_id
          AND manifest.status = 'frozen')
        OR
        (manifest.status = 'consumed' AND EXISTS (
          SELECT 1
          FROM artifact_revisions AS result_revision
          JOIN implementation_proposals AS proposal
            ON proposal.id = result_revision.implementation_proposal_id
           AND proposal.project_id = manifest.project_id
           AND proposal.build_manifest_id = manifest.id
           AND proposal.status IN ('applied', 'partially_applied')
          WHERE result_revision.id = NEW.base_workspace_revision_id
            AND result_revision.artifact_id = NEW.base_workspace_artifact_id
            AND result_revision.content_hash = NEW.base_workspace_content_hash
            AND result_revision.workflow_status IN ('approved', 'superseded')
        ))
      )
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot requires an exact ready BuildContract and canonical WorkspaceRevision for its frozen or consumed BuildManifest'
      USING ERRCODE = '23503';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM full_stack_template_components AS component
    LEFT JOIN template_release_policies AS policy
      ON policy.template_release_id = component.template_release_id
     AND policy.release_content_hash = component.template_release_content_hash
    WHERE component.full_stack_template_id = NEW.full_stack_template_id
      AND component.full_stack_content_hash = NEW.full_stack_template_hash
      AND (policy.template_release_id IS NULL OR policy.state <> 'approved')
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot FullStackTemplate contains a release that is not selectable'
      USING ERRCODE = '23503';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM full_stack_template_components AS component
    JOIN template_releases AS release
      ON release.id = component.template_release_id
     AND release.content_hash = component.template_release_content_hash
    WHERE component.full_stack_template_id = NEW.full_stack_template_id
      AND component.full_stack_content_hash = NEW.full_stack_template_hash
      AND (
        CASE
          WHEN jsonb_typeof(release.manifest->'protectedPaths') = 'array'
            THEN jsonb_array_length(release.manifest->'protectedPaths') = 0
          ELSE true
        END
        OR CASE
          WHEN jsonb_typeof(release.manifest->'extensionPaths') = 'array'
            THEN jsonb_array_length(release.manifest->'extensionPaths') = 0
          ELSE true
        END
      )
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot exact TemplateReleases require non-empty protectedPaths and extensionPaths'
      USING ERRCODE = '23503';
  END IF;

  IF NEW.base_workspace_revision_id IS NOT NULL AND NOT EXISTS (
    SELECT 1
    FROM artifacts AS artifact
    JOIN artifact_revisions AS revision ON revision.artifact_id = artifact.id
    WHERE artifact.id = NEW.base_workspace_artifact_id
      AND artifact.project_id = NEW.project_id
      AND artifact.kind = 'workspace'
      AND revision.id = NEW.base_workspace_revision_id
      AND revision.content_hash = NEW.base_workspace_content_hash
      AND revision.workflow_status IN ('approved', 'superseded')
  ) THEN
    RAISE EXCEPTION 'RepositorySnapshot base is not an exact canonical same-project WorkspaceRevision'
      USING ERRCODE = '23503';
  END IF;

  RETURN NEW;
END;
$$;
