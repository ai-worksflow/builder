-- A body-only trigram index can make one tenant's common literal walk posting
-- lists contributed by every other tenant before the member join applies the
-- project fence. btree_gin lets PostgreSQL intersect the exact project UUID
-- with the trigram posting list inside one index access.
CREATE EXTENSION IF NOT EXISTS btree_gin WITH SCHEMA public;

CREATE INDEX repository_exact_tree_literal_index_blob_project_body_trgm_idx
  ON repository_exact_tree_literal_index_blobs
  USING gin (
    project_id,
    (body COLLATE "C") public.gin_trgm_ops
  )
  WHERE is_text;

CREATE INDEX repository_exact_tree_literal_index_blob_project_ascii_body_trgm_idx
  ON repository_exact_tree_literal_index_blobs
  USING gin (
    project_id,
    (translate(
      body COLLATE "C",
      'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
      'abcdefghijklmnopqrstuvwxyz'
    )) public.gin_trgm_ops
  )
  WHERE is_text;

-- Build both project-scoped replacements before removing the global posting
-- indexes so a migration never commits a state without a bounded lookup path.
DROP INDEX repository_exact_tree_literal_index_blob_body_trgm_idx;
DROP INDEX repository_exact_tree_literal_index_blob_ascii_body_trgm_idx;

COMMENT ON INDEX repository_exact_tree_literal_index_blob_project_body_trgm_idx IS
  'Project-fenced case-sensitive trigram candidates for immutable exact-tree repository search.';
COMMENT ON INDEX repository_exact_tree_literal_index_blob_project_ascii_body_trgm_idx IS
  'Project-fenced ASCII-folded trigram candidates for immutable exact-tree repository search.';
