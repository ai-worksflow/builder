ALTER TABLE application_build_manifests
  ADD COLUMN delivery_slice_id text;

-- The compiler output is one side of the identity comparison introduced by
-- this migration, so it cannot self-certify a historical delivery_slice_id.
-- Existing current groups must be audited offline against their immutable
-- Workbench bundle payloads (or quarantined/reclassified as migration-owned
-- legacy rows) before rollout. Failing here is safer than blessing a historic
-- SliceIDs permutation as truth.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM application_build_manifests
    WHERE workflow_run_id IS NOT NULL
      AND manifest_group_key <> 'legacy'
  ) THEN
    RAISE EXCEPTION USING
      ERRCODE = '23514',
      MESSAGE = '000017 refuses to self-certify existing current workflow build manifest slice identities',
      HINT = 'Offline-verify immutable bundle payloads, then quarantine those rows or deploy an audited environment-specific replacement for 000017.';
  END IF;
END $$;

ALTER TABLE application_build_manifests
  ADD CONSTRAINT application_build_manifests_delivery_slice_shape_check CHECK (
    (delivery_slice_id IS NULL OR length(btrim(delivery_slice_id)) BETWEEN 1 AND 512)
    AND
    (
      workflow_run_id IS NULL
      OR manifest_group_key = 'legacy'
      OR delivery_slice_id IS NOT NULL
    )
  );

CREATE OR REPLACE FUNCTION enforce_application_build_manifest_slice_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  parent_slice_id text;
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF NEW.delivery_slice_id IS DISTINCT FROM OLD.delivery_slice_id
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.root_manifest_id IS DISTINCT FROM OLD.root_manifest_id
       OR NEW.derived_from_id IS DISTINCT FROM OLD.derived_from_id
       OR NEW.root_ordinal IS DISTINCT FROM OLD.root_ordinal
       OR NEW.manifest_group_key IS DISTINCT FROM OLD.manifest_group_key THEN
      RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = 'application build manifest slice and lineage identity is immutable';
    END IF;
  END IF;

  IF NEW.workflow_run_id IS NOT NULL
     AND NEW.manifest_group_key <> 'legacy'
     AND NULLIF(btrim(NEW.delivery_slice_id), '') IS NULL THEN
    RAISE EXCEPTION USING
      ERRCODE = '23514',
      MESSAGE = 'current workflow build manifests require an exact delivery slice identity';
  END IF;

  IF NEW.derived_from_id IS NOT NULL THEN
    SELECT delivery_slice_id
    INTO parent_slice_id
    FROM application_build_manifests
    WHERE id = NEW.derived_from_id
      AND root_manifest_id = NEW.root_manifest_id
      AND project_id = NEW.project_id;

    IF NOT FOUND OR NEW.delivery_slice_id IS DISTINCT FROM parent_slice_id THEN
      RAISE EXCEPTION USING
        ERRCODE = '23514',
        MESSAGE = 'derived application build manifest delivery slice must match its exact parent';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER application_build_manifest_slice_identity
BEFORE INSERT OR UPDATE ON application_build_manifests
FOR EACH ROW EXECUTE FUNCTION enforce_application_build_manifest_slice_identity();
