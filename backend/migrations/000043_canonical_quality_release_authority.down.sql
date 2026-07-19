ALTER TABLE deployment_versions
  DROP CONSTRAINT IF EXISTS deployment_versions_release_bundle_exact_fk,
  DROP CONSTRAINT IF EXISTS deployment_versions_canonical_receipt_exact_fk,
  DROP CONSTRAINT IF EXISTS deployment_versions_canonical_authority_shape,
  DROP CONSTRAINT IF EXISTS deployment_versions_release_bundle_hash_check,
  DROP CONSTRAINT IF EXISTS deployment_versions_canonical_receipt_hash_check,
  DROP COLUMN IF EXISTS release_bundle_hash,
  DROP COLUMN IF EXISTS release_bundle_id,
  DROP COLUMN IF EXISTS canonical_receipt_hash,
  DROP COLUMN IF EXISTS canonical_receipt_id;

DROP TRIGGER IF EXISTS release_bundle_immutable ON release_bundles;
DROP TRIGGER IF EXISTS canonical_verification_receipt_immutable ON canonical_verification_receipts;
DROP TRIGGER IF EXISTS canonical_verification_plan_immutable ON canonical_verification_plans;
DROP FUNCTION IF EXISTS prevent_canonical_quality_mutation();
DROP TRIGGER IF EXISTS release_bundle_insert_guard ON release_bundles;
DROP FUNCTION IF EXISTS validate_release_bundle_insert();
DROP TABLE IF EXISTS release_bundles;
DROP FUNCTION IF EXISTS release_bundle_artifact_set_is_complete(jsonb);
DROP TRIGGER IF EXISTS canonical_verification_receipt_insert_guard ON canonical_verification_receipts;
DROP FUNCTION IF EXISTS validate_canonical_verification_receipt_insert();
DROP TABLE IF EXISTS canonical_verification_receipts;
DROP TABLE IF EXISTS canonical_verification_runs;
DROP TRIGGER IF EXISTS canonical_verification_plan_insert_guard ON canonical_verification_plans;
DROP FUNCTION IF EXISTS validate_canonical_verification_plan_insert();
DROP TABLE IF EXISTS canonical_verification_plans;
DROP FUNCTION IF EXISTS canonical_release_artifacts_are_valid(jsonb, integer);
