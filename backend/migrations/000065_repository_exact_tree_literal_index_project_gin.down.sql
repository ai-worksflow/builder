-- Restore the 000062 lookup paths before removing the project-scoped indexes.
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

DROP INDEX repository_exact_tree_literal_index_blob_project_ascii_body_trgm_idx;
DROP INDEX repository_exact_tree_literal_index_blob_project_body_trgm_idx;

-- btree_gin is shared infrastructure and may predate this feature. Downgrade
-- intentionally leaves the extension installed.
