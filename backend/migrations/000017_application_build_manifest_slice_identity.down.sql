DROP TRIGGER IF EXISTS application_build_manifest_slice_identity ON application_build_manifests;
DROP FUNCTION IF EXISTS enforce_application_build_manifest_slice_identity();

ALTER TABLE application_build_manifests
  DROP CONSTRAINT IF EXISTS application_build_manifests_delivery_slice_shape_check,
  DROP COLUMN IF EXISTS delivery_slice_id;
