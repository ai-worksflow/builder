CREATE TABLE repository_file_blobs (
  id uuid PRIMARY KEY,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  store text NOT NULL CHECK (store = 'content'),
  owner_id uuid NOT NULL,
  content_ref text NOT NULL,
  content_object_hash text NOT NULL CHECK (content_object_hash ~ '^sha256:[0-9a-f]{64}$'),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  byte_size bigint NOT NULL CHECK (byte_size BETWEEN 0 AND 4194304),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT repository_file_blob_owner_identity CHECK (owner_id = id),
  CONSTRAINT repository_file_blob_content_ref_unique UNIQUE (content_ref),
  CONSTRAINT repository_file_blob_semantic_unique UNIQUE (project_id, content_hash, byte_size),
  CONSTRAINT repository_file_blob_ref_shape CHECK (
    content_ref = btrim(content_ref)
    AND length(content_ref) BETWEEN 1 AND 512
    AND content_ref !~ '[[:cntrl:]]'
  )
);

CREATE INDEX repository_file_blobs_project_created_idx
  ON repository_file_blobs (project_id, created_at DESC, id DESC);

CREATE OR REPLACE FUNCTION prevent_repository_file_blob_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'RepositoryFileBlob registration is immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER repository_file_blob_immutable
BEFORE UPDATE OR DELETE ON repository_file_blobs
FOR EACH ROW EXECUTE FUNCTION prevent_repository_file_blob_mutation();

COMMENT ON TABLE repository_file_blobs IS
  'Tenant-scoped immutable mapping from a server-derived file byte hash to one exact content.Store object. Runtime writes must pass through Repository FileBlobService.';

REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON repository_file_blobs FROM PUBLIC;
