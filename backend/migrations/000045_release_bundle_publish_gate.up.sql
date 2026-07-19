CREATE INDEX deployment_versions_release_bundle_idx
  ON deployment_versions (release_bundle_id, release_bundle_hash, created_at DESC)
  WHERE release_bundle_id IS NOT NULL;

CREATE OR REPLACE FUNCTION validate_deployment_version_release_authority_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.canonical_receipt_id IS NULL OR NEW.canonical_receipt_hash IS NULL
     OR NEW.release_bundle_id IS NULL OR NEW.release_bundle_hash IS NULL THEN
    RAISE EXCEPTION 'new deployment versions require an exact Canonical Receipt and ReleaseBundle'
      USING ERRCODE = '40001';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM deployments AS deployment
    JOIN release_bundles AS bundle
      ON bundle.id = NEW.release_bundle_id
     AND bundle.bundle_hash = NEW.release_bundle_hash
     AND bundle.project_id = deployment.project_id
     AND bundle.workspace_artifact_id = NEW.workspace_artifact_id
     AND bundle.workspace_revision_id = NEW.workspace_revision_id
     AND bundle.workspace_content_hash = NEW.workspace_content_hash
     AND bundle.canonical_receipt_id = NEW.canonical_receipt_id
     AND bundle.canonical_receipt_hash = NEW.canonical_receipt_hash
    JOIN canonical_verification_receipts AS receipt
      ON receipt.id = bundle.canonical_receipt_id
     AND receipt.payload_hash = bundle.canonical_receipt_hash
     AND receipt.decision = 'passed'
     AND receipt.build_manifest_id = NEW.build_manifest_id
    JOIN quality_runs AS quality
      ON quality.id = NEW.quality_run_id
     AND quality.project_id = deployment.project_id
     AND quality.status = 'passed'
     AND quality.workspace_artifact_id = NEW.workspace_artifact_id
     AND quality.workspace_revision_id = NEW.workspace_revision_id
     AND quality.workspace_content_hash = NEW.workspace_content_hash
     AND quality.build_artifact_id = NEW.build_artifact_id
     AND quality.build_content_ref = NEW.build_content_ref
     AND quality.build_content_hash = NEW.build_content_hash
     AND quality.build_hash = NEW.build_hash
    WHERE deployment.id = NEW.deployment_id
      AND EXISTS (
        SELECT 1
        FROM jsonb_array_elements(bundle.release_artifacts) AS artifact
        WHERE artifact->>'kind' = 'web-static'
          AND artifact->>'contentHash' = NEW.build_hash
      )
  ) THEN
    RAISE EXCEPTION 'deployment version requires exact Bundle, Receipt, manifest, workspace, quality, and web-static artifact lineage'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.action = 'rollback' AND NOT EXISTS (
    SELECT 1
    FROM deployment_versions AS source
    WHERE source.id = NEW.source_version_id
      AND source.deployment_id = NEW.deployment_id
      AND source.status = 'ready'
      AND source.release_bundle_id = NEW.release_bundle_id
      AND source.release_bundle_hash = NEW.release_bundle_hash
      AND source.canonical_receipt_id = NEW.canonical_receipt_id
      AND source.canonical_receipt_hash = NEW.canonical_receipt_hash
      AND source.build_hash = NEW.build_hash
  ) THEN
    RAISE EXCEPTION 'rollback must create a new version from the exact prior ReleaseBundle and artifact'
      USING ERRCODE = '40001';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER deployment_version_release_authority_insert_guard
BEFORE INSERT ON deployment_versions
FOR EACH ROW EXECUTE FUNCTION validate_deployment_version_release_authority_insert();
