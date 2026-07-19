-- Stage 2 execution evidence remains in the bounded content store, while
-- PostgreSQL is the sole reachability authority. These indexes make exact
-- historical tree reconstruction and evidence reconciliation independent of
-- the mutable Candidate head.

CREATE INDEX candidate_workspace_journal_exact_after_tree_idx
  ON candidate_workspace_journal (candidate_id, after_tree_hash)
  INCLUDE (
    after_tree_store, after_tree_owner_id, after_tree_ref,
    after_tree_content_hash, after_tree_file_count, after_tree_byte_size
  );

CREATE INDEX agent_attempts_evidence_refs_idx
  ON agent_attempts USING gin (evidence jsonb_path_ops)
  WHERE evidence <> '{}'::jsonb;
