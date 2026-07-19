-- The outer Operation request hash is not sufficient if a buggy SQL writer
-- copies a projected inner hash after changing the embedded document. Recompute
-- every immutable nested release fact from its complete canonical JSON before
-- the request can become controller authority.

-- The scan and trigger installation are one authority transition. Block old
-- writers for the transaction so no Operation can commit between them.
LOCK TABLE release_delivery_operations IN SHARE ROW EXCLUSIVE MODE;

CREATE OR REPLACE FUNCTION release_delivery_embedded_hash_is_exact(
  document jsonb,
  hash_field text
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $$
  SELECT CASE
    WHEN jsonb_typeof(document) IS DISTINCT FROM 'object'
      OR hash_field IS NULL OR hash_field = '' OR hash_field <> btrim(hash_field)
      OR NOT COALESCE(document ? hash_field, false)
      OR jsonb_typeof(document->hash_field) IS DISTINCT FROM 'string'
      OR COALESCE(document->>hash_field, '') !~ '^sha256:[0-9a-f]{64}$'
    THEN false
    ELSE document->>hash_field = 'sha256:' || encode(sha256(convert_to(
        release_delivery_canonical_json(
          jsonb_set(document, ARRAY[hash_field], '""'::jsonb, false)
        ),
        'UTF8'
      )), 'hex')
  END
$$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM release_delivery_operations AS operation
    CROSS JOIN LATERAL (
      SELECT operation.request_document::jsonb->'payload' AS payload
    ) AS request
    WHERE NOT release_delivery_embedded_hash_is_exact(
      request.payload->'releaseBundle', 'bundleHash'
    )
    OR (
      operation.kind = 'production'
      AND (
        request.payload->'previewReceipt'->>'schemaVersion'
          <> 'release-preview-receipt/v2'
        OR NOT release_delivery_embedded_hash_is_exact(
          request.payload->'previewReceipt', 'payloadHash'
        )
        OR NOT release_delivery_embedded_hash_is_exact(
          request.payload->'promotionApproval', 'payloadHash'
        )
        OR (
          request.payload->'sourceRevision' IS DISTINCT FROM 'null'::jsonb
          AND (
            request.payload->'sourceRevision'->>'schemaVersion'
              <> 'release-deployment-revision/v2'
            OR NOT release_delivery_embedded_hash_is_exact(
              request.payload->'sourceRevision', 'payloadHash'
            )
          )
        )
      )
    )
  ) THEN
    RAISE EXCEPTION 'cannot establish nested release authority while a stored Operation contains a noncanonical embedded fact'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

CREATE OR REPLACE FUNCTION validate_release_delivery_nested_authority_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  payload jsonb := NEW.request_document::jsonb->'payload';
  source_revision jsonb := payload->'sourceRevision';
BEGIN
  IF NOT release_delivery_embedded_hash_is_exact(
    payload->'releaseBundle', 'bundleHash'
  ) THEN
    RAISE EXCEPTION 'release delivery Operation contains a noncanonical embedded ReleaseBundle'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.kind = 'production' THEN
    IF payload->'previewReceipt'->>'schemaVersion'
         <> 'release-preview-receipt/v2'
       OR NOT release_delivery_embedded_hash_is_exact(
         payload->'previewReceipt', 'payloadHash'
       )
       OR NOT release_delivery_embedded_hash_is_exact(
         payload->'promotionApproval', 'payloadHash'
       )
       OR (
         source_revision IS DISTINCT FROM 'null'::jsonb
         AND (
           source_revision->>'schemaVersion'
             <> 'release-deployment-revision/v2'
           OR NOT release_delivery_embedded_hash_is_exact(
             source_revision, 'payloadHash'
           )
         )
       ) THEN
      RAISE EXCEPTION 'production Operation contains a noncanonical embedded Receipt, Approval, or Revision'
        USING ERRCODE = '40001';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_operation_nested_authority_guard
BEFORE INSERT ON release_delivery_operations
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_nested_authority_insert();

COMMENT ON FUNCTION release_delivery_embedded_hash_is_exact(jsonb, text) IS
  'Recomputes one embedded release fact hash from the complete canonical document with only its hash field blanked.';
