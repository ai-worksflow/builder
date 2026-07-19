-- Removing this guard would make already-created v3 Operations executable
-- under a weaker interpretation. Downgrade is allowed only at an empty
-- release-delivery authority boundary.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM release_delivery_operations) THEN
    RAISE EXCEPTION 'cannot downgrade nested release authority while delivery Operations exist'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

DROP TRIGGER IF EXISTS release_delivery_operation_nested_authority_guard
  ON release_delivery_operations;
DROP FUNCTION IF EXISTS validate_release_delivery_nested_authority_insert();
DROP FUNCTION IF EXISTS release_delivery_embedded_hash_is_exact(jsonb, text);
