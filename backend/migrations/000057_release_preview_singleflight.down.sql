-- Removing this index would re-open concurrent mutation of a deterministic
-- preview namespace.  Refuse downgrade while any v2 or blocked Preview
-- authority remains; migration 000056 must be downgraded as part of the same
-- explicitly empty authority boundary.  ReleaseBundle facts must also be
-- empty before restoring the historical trigger behavior.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM release_preview_runs
    WHERE schema_version = 'release-preview-run/v2'
       OR state = 'reconcile_blocked'
  ) OR EXISTS (
    SELECT 1
    FROM release_delivery_operations
    WHERE kind = 'preview'
  ) OR EXISTS (
    SELECT 1
    FROM release_bundles
  ) THEN
    RAISE EXCEPTION 'cannot downgrade preview single-flight while ReleaseBundle, v2, or reconcile-blocked Preview authority exists'
      USING ERRCODE = '40001';
  END IF;
END;
$$;

DROP INDEX IF EXISTS release_preview_runs_one_nonterminal_bundle_idx;

CREATE OR REPLACE FUNCTION validate_release_bundle_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
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
  NEW.created_at := statement_timestamp();
  NEW.creation_transaction_id := txid_current();
  RETURN NEW;
END;
$$;
