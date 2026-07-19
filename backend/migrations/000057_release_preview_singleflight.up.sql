-- One exact ReleaseBundle maps to one deterministic preview namespace.  A
-- second nonterminal Run could therefore mutate the same remote namespace
-- concurrently even though it has a different API idempotency key.  Keep the
-- lock for every locally active, remotely uncertain, or quarantined Run; only
-- an explicit terminal decision releases it.

-- ReleaseBundle.createdAt is part of its canonical content and Bundle hash.
-- Migration 000043 used to replace that caller-supplied value with the SQL
-- statement time, making the immutable SQL projection disagree with the
-- content that Store.Create had already persisted.  Preserve the explicit
-- canonical timestamp and only project the database transaction identity.
CREATE OR REPLACE FUNCTION validate_release_bundle_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.created_at IS NULL
     OR NEW.created_at = TIMESTAMPTZ '0001-01-01 00:00:00+00'
     OR NEW.created_at > statement_timestamp() THEN
    RAISE EXCEPTION 'ReleaseBundle created_at must be an explicit nonzero canonical time that is not in the future'
      USING ERRCODE = '22007';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM canonical_verification_receipts AS receipt
    JOIN canonical_verification_runs AS run ON run.id = receipt.run_id AND run.state = 'passed'
    WHERE receipt.id = NEW.canonical_receipt_id
      AND receipt.payload_hash = NEW.canonical_receipt_hash
      AND receipt.project_id = NEW.project_id
      AND receipt.decision = 'passed'
      AND receipt.blocker_count = 0
      AND receipt.must_passed_count = receipt.must_count
      AND receipt.workspace_artifact_id = NEW.workspace_artifact_id
      AND receipt.workspace_revision_id = NEW.workspace_revision_id
      AND receipt.workspace_content_hash = NEW.workspace_content_hash
      AND receipt.release_artifacts = NEW.release_artifacts
  ) THEN
    RAISE EXCEPTION 'ReleaseBundle requires one exact passed Canonical Receipt and identical release artifacts'
      USING ERRCODE = '40001';
  END IF;
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM release_preview_runs
    WHERE state IN (
      'queued','claimed','submitting','reconcile_wait','reconciling','verifying','reconcile_blocked'
    )
    GROUP BY project_id, release_bundle_id, release_bundle_hash
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'cannot establish preview single-flight while duplicate nonterminal exact Bundle authority exists; reconcile it explicitly'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

CREATE UNIQUE INDEX release_preview_runs_one_nonterminal_bundle_idx
  ON release_preview_runs (project_id, release_bundle_id, release_bundle_hash)
  WHERE state IN (
    'queued','claimed','submitting','reconcile_wait','reconciling','verifying','reconcile_blocked'
  );

COMMENT ON INDEX release_preview_runs_one_nonterminal_bundle_idx IS
  'One deterministic preview namespace may have only one active, uncertain, or reconcile-blocked exact ReleaseBundle Run.';
