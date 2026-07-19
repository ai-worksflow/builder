-- A RepositorySnapshot becomes externally receiptable only after its exact
-- tree and every referenced FileBlob have been settled by the application.
-- This append-only marker prevents an authorized caller that can predict the
-- deterministic hash from observing a completion Receipt while content is
-- still pending finalization.
CREATE TABLE repository_snapshot_receipts (
  snapshot_id uuid PRIMARY KEY,
  project_id uuid NOT NULL,
  schema_version text NOT NULL CHECK (
    schema_version = 'repository-snapshot-receipt/v1'
  ),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  document jsonb NOT NULL,
  recorded_at timestamptz NOT NULL,
  CONSTRAINT repository_snapshot_receipt_snapshot_fk
    FOREIGN KEY (snapshot_id, project_id)
    REFERENCES repository_snapshots(id, project_id) ON DELETE RESTRICT,
  CONSTRAINT repository_snapshot_receipt_exact_identity_unique
    UNIQUE (snapshot_id, project_id, content_hash),
  CONSTRAINT repository_snapshot_receipt_document_exact CHECK (
    jsonb_typeof(document) = 'object'
    AND document ?& ARRAY['schemaVersion', 'contentHash', 'snapshot']
    AND document - ARRAY['schemaVersion', 'contentHash', 'snapshot'] = '{}'::jsonb
    AND document->>'schemaVersion' = schema_version
    AND document->>'contentHash' = content_hash
    AND jsonb_typeof(document->'snapshot') = 'object'
    AND document->'snapshot'->>'schemaVersion' = 'repository-snapshot-receipt-subject/v1'
    AND document->'snapshot'->>'id' = snapshot_id::text
    AND document->'snapshot'->>'projectId' = project_id::text
    AND jsonb_typeof(document->'snapshot'->'tree') = 'object'
    AND document->'snapshot'->'tree'->>'schemaVersion' = 'repository-snapshot-tree-commitment/v1'
    AND document->'snapshot'->'tree'->>'treeHash' ~ '^sha256:[0-9a-f]{64}$'
    AND document->'snapshot'->'tree'->>'contentObjectHash' ~ '^sha256:[0-9a-f]{64}$'
    AND jsonb_typeof(document->'snapshot'->'templateReleases') = 'array'
    AND jsonb_array_length(document->'snapshot'->'templateReleases') BETWEEN 2 AND 8
  )
);

CREATE INDEX repository_snapshot_receipts_project_recorded_idx
  ON repository_snapshot_receipts(project_id, recorded_at DESC, snapshot_id DESC);

COMMENT ON TABLE repository_snapshot_receipts IS
  'Append-only completion markers for compact, exact RepositorySnapshot lineage/tree/Template Artifact Authority receipts; inserted only after all snapshot content settles.';

REVOKE ALL ON TABLE repository_snapshot_receipts FROM PUBLIC;

DO $ownership_and_acl$
DECLARE
  schema_name text := current_schema();
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner'
  ) THEN
    EXECUTE format(
      'ALTER TABLE %I.repository_snapshot_receipts OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application'
  ) THEN
    EXECUTE format(
      'GRANT SELECT, INSERT ON TABLE %I.repository_snapshot_receipts TO worksflow_application',
      schema_name
    );
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname = 'worksflow_repository_index_gc_operator'
  ) THEN
    EXECUTE format(
      'REVOKE ALL ON TABLE %I.repository_snapshot_receipts FROM worksflow_repository_index_gc_operator',
      schema_name
    );
  END IF;
END;
$ownership_and_acl$;
