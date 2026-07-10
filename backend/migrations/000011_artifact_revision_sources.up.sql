CREATE TABLE artifact_revision_sources (
  revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE CASCADE,
  ordinal integer NOT NULL,
  source_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  source_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  source_content_hash text NOT NULL,
  source_anchor_id text,
  purpose text NOT NULL,
  required boolean NOT NULL DEFAULT true,
  added_by uuid NOT NULL REFERENCES users(id),
  added_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (revision_id, source_revision_id, purpose),
  UNIQUE (revision_id, ordinal),
  CONSTRAINT artifact_revision_sources_ordinal_nonnegative CHECK (ordinal >= 0)
);

CREATE INDEX artifact_revision_sources_source_idx
  ON artifact_revision_sources (source_artifact_id, source_revision_id);

-- A draft still exactly at its base revision retains the immutable source set
-- that produced that revision. Drafts edited after revision creation are not
-- backfilled because their source set may already describe the next revision.
INSERT INTO artifact_revision_sources (
  revision_id,
  ordinal,
  source_artifact_id,
  source_revision_id,
  source_content_hash,
  source_anchor_id,
  purpose,
  required,
  added_by,
  added_at
)
SELECT
  revisions.id,
  (ROW_NUMBER() OVER (
    PARTITION BY revisions.id
    ORDER BY sources.added_at, sources.source_revision_id, sources.purpose
  ) - 1)::integer,
  sources.source_artifact_id,
  sources.source_revision_id,
  sources.source_content_hash,
  sources.source_anchor_id,
  sources.purpose,
  sources.required,
  sources.added_by,
  sources.added_at
FROM artifact_drafts AS drafts
JOIN artifacts AS target_artifacts ON target_artifacts.latest_draft_id = drafts.id
JOIN artifact_revisions AS revisions
  ON revisions.id = drafts.base_revision_id
  AND revisions.content_hash = drafts.content_hash
JOIN artifact_draft_sources AS sources ON sources.draft_id = drafts.id
WHERE drafts.updated_at <= revisions.created_at
ON CONFLICT (revision_id, source_revision_id, purpose) DO NOTHING;
