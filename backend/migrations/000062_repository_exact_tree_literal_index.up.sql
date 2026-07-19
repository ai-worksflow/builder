-- pg_trgm is a hard deployment prerequisite for repository-scale literal
-- candidate lookup. Migration failure is intentional when the migration role
-- cannot install or use it; silently publishing an unindexed fallback would
-- permit unbounded scans. Keep the shared extension in public so temporary
-- per-test/application schemas can resolve the same operator class.
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE TABLE repository_exact_tree_literal_index_blobs (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  byte_size bigint NOT NULL CHECK (byte_size BETWEEN 0 AND 4194304),
  is_text boolean NOT NULL,
  body text,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  PRIMARY KEY (project_id, content_hash),
  CONSTRAINT repository_exact_tree_literal_index_blob_member_identity_unique
    UNIQUE (project_id, content_hash, byte_size, is_text),
  CONSTRAINT repository_exact_tree_literal_index_blob_body_shape CHECK (
    (is_text AND body IS NOT NULL AND octet_length(body) = byte_size)
    OR (NOT is_text AND body IS NULL)
  )
);

-- LIKE '%literal%' and immutable ASCII-only folding are the only lookup
-- predicates admitted by the store. C collation plus translate preserves the
-- existing Candidate scanner's A-Z/a-z semantics without PostgreSQL lower()
-- introducing Unicode-equivalent false candidates that can consume the
-- bounded candidate set.
CREATE INDEX repository_exact_tree_literal_index_blob_body_trgm_idx
  ON repository_exact_tree_literal_index_blobs
  USING gin ((body COLLATE "C") public.gin_trgm_ops)
  WHERE is_text;

CREATE INDEX repository_exact_tree_literal_index_blob_ascii_body_trgm_idx
  ON repository_exact_tree_literal_index_blobs
  USING gin ((translate(
    body COLLATE "C",
    'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
    'abcdefghijklmnopqrstuvwxyz'
  )) public.gin_trgm_ops)
  WHERE is_text;

CREATE TABLE repository_exact_tree_literal_index_manifests (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  schema_version text NOT NULL CHECK (
    schema_version = 'repository-exact-tree-literal-index/v1'
  ),
  status text NOT NULL CHECK (status IN ('building', 'ready')),
  file_count integer NOT NULL CHECK (file_count BETWEEN 0 AND 20000),
  text_file_count integer NOT NULL CHECK (text_file_count BETWEEN 0 AND file_count),
  skipped_file_count integer NOT NULL CHECK (
    skipped_file_count BETWEEN 0 AND file_count
    AND text_file_count + skipped_file_count = file_count
  ),
  total_bytes bigint NOT NULL CHECK (total_bytes BETWEEN 0 AND 67108864),
  tree_commitment text NOT NULL CHECK (tree_commitment ~ '^sha256:[0-9a-f]{64}$'),
  index_commitment text NOT NULL CHECK (index_commitment ~ '^sha256:[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  ready_at timestamptz,
  PRIMARY KEY (project_id, tree_hash),
  CONSTRAINT repository_exact_tree_literal_index_manifest_ready_shape CHECK (
    (status = 'building' AND ready_at IS NULL)
    OR (status = 'ready' AND ready_at IS NOT NULL AND ready_at >= created_at)
  )
);

CREATE TABLE repository_exact_tree_literal_index_members (
  project_id uuid NOT NULL,
  tree_hash text NOT NULL CHECK (tree_hash ~ '^sha256:[0-9a-f]{64}$'),
  path text COLLATE "C" NOT NULL CHECK (repository_path_is_safe(path)),
  mode text NOT NULL CHECK (mode IN ('100644', '100755')),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  byte_size bigint NOT NULL CHECK (byte_size BETWEEN 0 AND 4194304),
  indexed boolean NOT NULL,
  PRIMARY KEY (project_id, tree_hash, path),
  CONSTRAINT repository_exact_tree_literal_index_member_manifest_fk
    FOREIGN KEY (project_id, tree_hash)
    REFERENCES repository_exact_tree_literal_index_manifests(project_id, tree_hash)
    ON DELETE RESTRICT,
  CONSTRAINT repository_exact_tree_literal_index_member_blob_fk
    FOREIGN KEY (project_id, content_hash, byte_size, indexed)
    REFERENCES repository_exact_tree_literal_index_blobs(
      project_id, content_hash, byte_size, is_text
    )
    ON DELETE RESTRICT
);

CREATE INDEX repository_exact_tree_literal_index_member_blob_idx
  ON repository_exact_tree_literal_index_members (
    project_id, content_hash, byte_size, indexed, tree_hash, path
  );

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_blob_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Exact-tree literal index blobs are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER repository_exact_tree_literal_index_blob_immutable
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_blobs
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_blob_mutation();

CREATE TRIGGER repository_exact_tree_literal_index_blob_no_truncate
BEFORE TRUNCATE ON repository_exact_tree_literal_index_blobs
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_blob_mutation();

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_manifest_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.status <> 'building' OR NEW.ready_at IS NOT NULL THEN
    RAISE EXCEPTION 'Exact-tree literal index manifest must begin in building state'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER repository_exact_tree_literal_index_manifest_insert_guard
BEFORE INSERT ON repository_exact_tree_literal_index_manifests
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_manifest_insert();

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_member_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  manifest_status text;
BEGIN
  SELECT status
  INTO manifest_status
  FROM repository_exact_tree_literal_index_manifests
  WHERE project_id = NEW.project_id
    AND tree_hash = NEW.tree_hash
  FOR KEY SHARE;

  IF manifest_status IS DISTINCT FROM 'building' THEN
    RAISE EXCEPTION 'Exact-tree literal index members require one building manifest'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER repository_exact_tree_literal_index_member_insert_guard
BEFORE INSERT ON repository_exact_tree_literal_index_members
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_member_insert();

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_member_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Exact-tree literal index members are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER repository_exact_tree_literal_index_member_immutable
BEFORE UPDATE OR DELETE ON repository_exact_tree_literal_index_members
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_member_mutation();

CREATE TRIGGER repository_exact_tree_literal_index_member_no_truncate
BEFORE TRUNCATE ON repository_exact_tree_literal_index_members
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_member_mutation();

CREATE OR REPLACE FUNCTION publish_repository_exact_tree_literal_index_manifest()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  actual_file_count integer;
  actual_text_file_count integer;
  actual_total_bytes bigint;
BEGIN
  IF OLD.status <> 'building'
     OR NEW.status <> 'ready'
     OR NEW.ready_at IS NULL
     OR NEW.project_id IS DISTINCT FROM OLD.project_id
     OR NEW.tree_hash IS DISTINCT FROM OLD.tree_hash
     OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
     OR NEW.file_count IS DISTINCT FROM OLD.file_count
     OR NEW.text_file_count IS DISTINCT FROM OLD.text_file_count
     OR NEW.skipped_file_count IS DISTINCT FROM OLD.skipped_file_count
     OR NEW.total_bytes IS DISTINCT FROM OLD.total_bytes
     OR NEW.tree_commitment IS DISTINCT FROM OLD.tree_commitment
     OR NEW.index_commitment IS DISTINCT FROM OLD.index_commitment
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'Exact-tree literal index manifest allows only one building-to-ready transition'
      USING ERRCODE = '55000';
  END IF;

  SELECT
    count(*)::integer,
    count(*) FILTER (WHERE indexed)::integer,
    COALESCE(sum(byte_size), 0)::bigint
  INTO actual_file_count, actual_text_file_count, actual_total_bytes
  FROM repository_exact_tree_literal_index_members
  WHERE project_id = OLD.project_id
    AND tree_hash = OLD.tree_hash;

  IF actual_file_count <> NEW.file_count
     OR actual_text_file_count <> NEW.text_file_count
     OR actual_file_count - actual_text_file_count <> NEW.skipped_file_count
     OR actual_total_bytes <> NEW.total_bytes THEN
    RAISE EXCEPTION 'Exact-tree literal index manifest cannot become ready before all members are complete'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER repository_exact_tree_literal_index_manifest_publish_guard
BEFORE UPDATE ON repository_exact_tree_literal_index_manifests
FOR EACH ROW EXECUTE FUNCTION publish_repository_exact_tree_literal_index_manifest();

CREATE OR REPLACE FUNCTION guard_repository_exact_tree_literal_index_manifest_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Exact-tree literal index manifests are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER repository_exact_tree_literal_index_manifest_no_delete
BEFORE DELETE ON repository_exact_tree_literal_index_manifests
FOR EACH ROW EXECUTE FUNCTION guard_repository_exact_tree_literal_index_manifest_delete();

CREATE TRIGGER repository_exact_tree_literal_index_manifest_no_truncate
BEFORE TRUNCATE ON repository_exact_tree_literal_index_manifests
FOR EACH STATEMENT EXECUTE FUNCTION guard_repository_exact_tree_literal_index_manifest_delete();

COMMENT ON TABLE repository_exact_tree_literal_index_manifests IS
  'Ready commitment for one complete tenant-scoped canonical tree derived literal index. This accelerator is not repository source authority.';
COMMENT ON TABLE repository_exact_tree_literal_index_members IS
  'Immutable canonical path/mode/hash/size membership for one exact-tree derived index, including skipped binary files.';
COMMENT ON TABLE repository_exact_tree_literal_index_blobs IS
  'Tenant-scoped text-body deduplication by source byte hash for exact-tree literal candidate lookup.';

REVOKE INSERT, UPDATE, DELETE, TRUNCATE
  ON repository_exact_tree_literal_index_blobs FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE
  ON repository_exact_tree_literal_index_manifests FROM PUBLIC;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE
  ON repository_exact_tree_literal_index_members FROM PUBLIC;
