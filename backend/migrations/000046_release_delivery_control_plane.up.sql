CREATE TABLE release_preview_runs (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-preview-run/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  release_bundle_id uuid NOT NULL,
  release_bundle_hash text NOT NULL CHECK (release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_key text NOT NULL CHECK (request_key = btrim(request_key) AND length(request_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  state text NOT NULL CHECK (state IN ('queued','claimed','deploying','verifying','passed','failed','error','cancelled')),
  version bigint NOT NULL CHECK (version > 0),
  fence_epoch bigint NOT NULL DEFAULT 0 CHECK (fence_epoch >= 0),
  lease_worker_id text,
  lease_epoch bigint,
  lease_expires_at timestamptz,
  started_at timestamptz,
  finished_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_preview_run_bundle_exact_fk
    FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT,
  CONSTRAINT release_preview_run_request_unique UNIQUE (project_id, request_key),
  CONSTRAINT release_preview_run_terminal_shape CHECK (
    (state IN ('passed','failed','error','cancelled') AND finished_at IS NOT NULL)
    OR (state NOT IN ('passed','failed','error','cancelled') AND finished_at IS NULL)
  ),
  CONSTRAINT release_preview_run_lease_shape CHECK (
    (state IN ('claimed','deploying','verifying') AND lease_worker_id IS NOT NULL
      AND lease_epoch IS NOT NULL AND lease_expires_at IS NOT NULL)
    OR (state NOT IN ('claimed','deploying','verifying') AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  )
);

CREATE INDEX release_preview_runs_claim_idx
  ON release_preview_runs (state, lease_expires_at, created_at, id)
  WHERE state IN ('queued','claimed','deploying','verifying');

CREATE TABLE release_preview_receipts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-preview-receipt/v1'),
  run_id uuid NOT NULL UNIQUE REFERENCES release_preview_runs(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  release_bundle_id uuid NOT NULL,
  release_bundle_hash text NOT NULL CHECK (release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'),
  canonical_receipt_id uuid NOT NULL,
  canonical_receipt_hash text NOT NULL CHECK (canonical_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  workspace_artifact_id uuid NOT NULL REFERENCES artifacts(id) ON DELETE RESTRICT,
  workspace_revision_id uuid NOT NULL REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  workspace_content_hash text NOT NULL CHECK (workspace_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  release_artifacts jsonb NOT NULL CHECK (canonical_release_artifacts_are_valid(release_artifacts, 1)),
  namespace text NOT NULL CHECK (namespace = btrim(namespace) AND length(namespace) BETWEEN 1 AND 200),
  provider text NOT NULL CHECK (provider = btrim(provider) AND length(provider) BETWEEN 1 AND 128),
  provider_ref text NOT NULL CHECK (provider_ref = btrim(provider_ref) AND length(provider_ref) BETWEEN 1 AND 1000),
  checks jsonb NOT NULL CHECK (jsonb_typeof(checks) = 'array' AND jsonb_array_length(checks) BETWEEN 1 AND 128),
  decision text NOT NULL CHECK (decision IN ('passed','failed')),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_preview_receipt_exact_unique UNIQUE (id, payload_hash),
  CONSTRAINT release_preview_receipt_bundle_exact_fk
    FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT,
  CONSTRAINT release_preview_receipt_canonical_exact_fk
    FOREIGN KEY (canonical_receipt_id, canonical_receipt_hash)
    REFERENCES canonical_verification_receipts(id, payload_hash) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION validate_release_preview_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  has_failed boolean;
  checks_valid boolean;
  ids_strictly_ordered boolean;
BEGIN
  SELECT
    bool_and(
      jsonb_typeof(item) = 'object'
      AND (item->>'id') = btrim(item->>'id') AND length(item->>'id') BETWEEN 1 AND 128
      AND (item->>'kind') = btrim(item->>'kind') AND length(item->>'kind') BETWEEN 1 AND 128
      AND item->>'status' IN ('passed','failed')
      AND length(COALESCE(item->>'detail', '')) <= 2000
    ),
    bool_or(item->>'status' = 'failed'),
    bool_and(previous_id IS NULL OR previous_id < item->>'id')
  INTO checks_valid, has_failed, ids_strictly_ordered
  FROM (
    SELECT item, lag(item->>'id') OVER (ORDER BY ordinal) AS previous_id
    FROM jsonb_array_elements(NEW.checks) WITH ORDINALITY AS entry(item, ordinal)
  ) AS ordered_checks;

  IF checks_valid IS NOT TRUE OR ids_strictly_ordered IS NOT TRUE
     OR (NEW.decision = 'passed' AND has_failed)
     OR (NEW.decision = 'failed' AND NOT has_failed) THEN
    RAISE EXCEPTION 'PreviewReceipt decision must exactly match its ordered checks'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.decision = 'passed' AND EXISTS (
    SELECT required.kind
    FROM unnest(ARRAY['migration','health','smoke','contract','e2e']) AS required(kind)
    WHERE NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements(NEW.checks) AS item(value)
      WHERE item.value->>'kind' = required.kind AND item.value->>'status' = 'passed'
    )
  ) THEN
    RAISE EXCEPTION 'passed PreviewReceipt requires migration, health, smoke, contract, and e2e checks'
      USING ERRCODE = '40001';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM release_preview_runs AS run
    JOIN release_bundles AS bundle
      ON bundle.id = run.release_bundle_id AND bundle.bundle_hash = run.release_bundle_hash
    WHERE run.id = NEW.run_id
      AND run.project_id = NEW.project_id
      AND run.state = 'verifying'
      AND bundle.id = NEW.release_bundle_id
      AND bundle.bundle_hash = NEW.release_bundle_hash
      AND bundle.project_id = NEW.project_id
      AND bundle.canonical_receipt_id = NEW.canonical_receipt_id
      AND bundle.canonical_receipt_hash = NEW.canonical_receipt_hash
      AND bundle.workspace_artifact_id = NEW.workspace_artifact_id
      AND bundle.workspace_revision_id = NEW.workspace_revision_id
      AND bundle.workspace_content_hash = NEW.workspace_content_hash
      AND bundle.release_artifacts = NEW.release_artifacts
  ) THEN
    RAISE EXCEPTION 'PreviewReceipt requires the exact verifying Run and identical ReleaseBundle artifacts'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_preview_receipt_insert_guard
BEFORE INSERT ON release_preview_receipts
FOR EACH ROW EXECUTE FUNCTION validate_release_preview_receipt_insert();

CREATE TABLE release_promotion_approvals (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-promotion-approval/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  release_bundle_id uuid NOT NULL,
  release_bundle_hash text NOT NULL CHECK (release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'),
  preview_receipt_id uuid NOT NULL,
  preview_receipt_hash text NOT NULL CHECK (preview_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_promotion_approval_exact_unique UNIQUE (id, payload_hash),
  CONSTRAINT release_promotion_approval_bundle_exact_fk
    FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT,
  CONSTRAINT release_promotion_approval_preview_exact_fk
    FOREIGN KEY (preview_receipt_id, preview_receipt_hash)
    REFERENCES release_preview_receipts(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_promotion_approval_preview_unique UNIQUE (preview_receipt_id, preview_receipt_hash)
);

CREATE OR REPLACE FUNCTION validate_release_promotion_approval_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM release_preview_receipts AS preview
    WHERE preview.id = NEW.preview_receipt_id
      AND preview.payload_hash = NEW.preview_receipt_hash
      AND preview.project_id = NEW.project_id
      AND preview.release_bundle_id = NEW.release_bundle_id
      AND preview.release_bundle_hash = NEW.release_bundle_hash
      AND preview.decision = 'passed'
  ) THEN
    RAISE EXCEPTION 'PromotionApproval requires one exact passed PreviewReceipt for the same ReleaseBundle'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_promotion_approval_insert_guard
BEFORE INSERT ON release_promotion_approvals
FOR EACH ROW EXECUTE FUNCTION validate_release_promotion_approval_insert();

CREATE TABLE release_deployment_runs (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-deployment-run/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  operation text NOT NULL CHECK (operation IN ('promote','rollback')),
  release_bundle_id uuid NOT NULL,
  release_bundle_hash text NOT NULL CHECK (release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'),
  preview_receipt_id uuid NOT NULL,
  preview_receipt_hash text NOT NULL CHECK (preview_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  promotion_approval_id uuid NOT NULL,
  promotion_approval_hash text NOT NULL CHECK (promotion_approval_hash ~ '^sha256:[0-9a-f]{64}$'),
  source_revision_id uuid,
  source_revision_hash text,
  request_key text NOT NULL CHECK (request_key = btrim(request_key) AND length(request_key) BETWEEN 1 AND 128),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  reason text NOT NULL CHECK (reason = btrim(reason) AND length(reason) BETWEEN 1 AND 1000),
  state text NOT NULL CHECK (state IN ('queued','claimed','deploying','verifying','healthy','failed','error','cancelled')),
  version bigint NOT NULL CHECK (version > 0),
  fence_epoch bigint NOT NULL DEFAULT 0 CHECK (fence_epoch >= 0),
  lease_worker_id text,
  lease_epoch bigint,
  lease_expires_at timestamptz,
  started_at timestamptz,
  finished_at timestamptz,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_deployment_run_bundle_exact_fk
    FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT,
  CONSTRAINT release_deployment_run_preview_exact_fk
    FOREIGN KEY (preview_receipt_id, preview_receipt_hash)
    REFERENCES release_preview_receipts(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_deployment_run_approval_exact_fk
    FOREIGN KEY (promotion_approval_id, promotion_approval_hash)
    REFERENCES release_promotion_approvals(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_deployment_run_request_unique UNIQUE (project_id, request_key),
  CONSTRAINT release_deployment_run_source_shape CHECK (
    (operation = 'promote' AND source_revision_id IS NULL AND source_revision_hash IS NULL)
    OR (operation = 'rollback' AND source_revision_id IS NOT NULL
      AND source_revision_hash ~ '^sha256:[0-9a-f]{64}$')
  ),
  CONSTRAINT release_deployment_run_terminal_shape CHECK (
    (state IN ('healthy','failed','error','cancelled') AND finished_at IS NOT NULL)
    OR (state NOT IN ('healthy','failed','error','cancelled') AND finished_at IS NULL)
  ),
  CONSTRAINT release_deployment_run_lease_shape CHECK (
    (state IN ('claimed','deploying','verifying') AND lease_worker_id IS NOT NULL
      AND lease_epoch IS NOT NULL AND lease_expires_at IS NOT NULL)
    OR (state NOT IN ('claimed','deploying','verifying') AND lease_worker_id IS NULL
      AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  )
);

CREATE INDEX release_deployment_runs_claim_idx
  ON release_deployment_runs (state, lease_expires_at, created_at, id)
  WHERE state IN ('queued','claimed','deploying','verifying');

CREATE TABLE release_production_receipts (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-production-receipt/v1'),
  run_id uuid NOT NULL UNIQUE REFERENCES release_deployment_runs(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  operation text NOT NULL CHECK (operation IN ('promote','rollback')),
  release_bundle_id uuid NOT NULL,
  release_bundle_hash text NOT NULL CHECK (release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'),
  preview_receipt_id uuid NOT NULL,
  preview_receipt_hash text NOT NULL CHECK (preview_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  promotion_approval_id uuid NOT NULL,
  promotion_approval_hash text NOT NULL CHECK (promotion_approval_hash ~ '^sha256:[0-9a-f]{64}$'),
  source_revision_id uuid,
  source_revision_hash text,
  provider text NOT NULL CHECK (provider = btrim(provider) AND length(provider) BETWEEN 1 AND 128),
  provider_ref text NOT NULL CHECK (provider_ref = btrim(provider_ref) AND length(provider_ref) BETWEEN 1 AND 1000),
  public_url text NOT NULL CHECK (public_url = btrim(public_url) AND length(public_url) <= 2000),
  checks jsonb NOT NULL CHECK (jsonb_typeof(checks) = 'array' AND jsonb_array_length(checks) BETWEEN 1 AND 128),
  decision text NOT NULL CHECK (decision IN ('passed','failed')),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_production_receipt_exact_unique UNIQUE (id, payload_hash),
  CONSTRAINT release_production_receipt_bundle_exact_fk
    FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT,
  CONSTRAINT release_production_receipt_preview_exact_fk
    FOREIGN KEY (preview_receipt_id, preview_receipt_hash)
    REFERENCES release_preview_receipts(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_production_receipt_approval_exact_fk
    FOREIGN KEY (promotion_approval_id, promotion_approval_hash)
    REFERENCES release_promotion_approvals(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_production_receipt_source_shape CHECK (
    (operation = 'promote' AND source_revision_id IS NULL AND source_revision_hash IS NULL)
    OR (operation = 'rollback' AND source_revision_id IS NOT NULL
      AND source_revision_hash ~ '^sha256:[0-9a-f]{64}$')
  ),
  CONSTRAINT release_production_receipt_public_url_shape CHECK (
    decision = 'failed' OR length(public_url) BETWEEN 1 AND 2000
  )
);

CREATE OR REPLACE FUNCTION validate_release_production_receipt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  has_failed boolean;
  checks_valid boolean;
  ids_strictly_ordered boolean;
BEGIN
  SELECT
    bool_and(
      jsonb_typeof(item) = 'object'
      AND (item->>'id') = btrim(item->>'id') AND length(item->>'id') BETWEEN 1 AND 128
      AND (item->>'kind') = btrim(item->>'kind') AND length(item->>'kind') BETWEEN 1 AND 128
      AND item->>'status' IN ('passed','failed')
      AND length(COALESCE(item->>'detail', '')) <= 2000
    ),
    bool_or(item->>'status' = 'failed'),
    bool_and(previous_id IS NULL OR previous_id < item->>'id')
  INTO checks_valid, has_failed, ids_strictly_ordered
  FROM (
    SELECT item, lag(item->>'id') OVER (ORDER BY ordinal) AS previous_id
    FROM jsonb_array_elements(NEW.checks) WITH ORDINALITY AS entry(item, ordinal)
  ) AS ordered_checks;

  IF checks_valid IS NOT TRUE OR ids_strictly_ordered IS NOT TRUE
     OR (NEW.decision = 'passed' AND has_failed)
     OR (NEW.decision = 'failed' AND NOT has_failed) THEN
    RAISE EXCEPTION 'ProductionReceipt decision must exactly match its health checks'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.decision = 'passed' AND EXISTS (
    SELECT required.kind
    FROM unnest(ARRAY['health','rollout']) AS required(kind)
    WHERE NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements(NEW.checks) AS item(value)
      WHERE item.value->>'kind' = required.kind AND item.value->>'status' = 'passed'
    )
  ) THEN
    RAISE EXCEPTION 'passed ProductionReceipt requires health and rollout checks'
      USING ERRCODE = '40001';
  END IF;

  IF NOT EXISTS (
    SELECT 1
    FROM release_deployment_runs AS run
    JOIN release_preview_receipts AS preview
      ON preview.id = run.preview_receipt_id AND preview.payload_hash = run.preview_receipt_hash
    JOIN release_promotion_approvals AS approval
      ON approval.id = run.promotion_approval_id AND approval.payload_hash = run.promotion_approval_hash
    WHERE run.id = NEW.run_id
      AND run.project_id = NEW.project_id
      AND run.state = 'verifying'
      AND run.operation = NEW.operation
      AND run.release_bundle_id = NEW.release_bundle_id
      AND run.release_bundle_hash = NEW.release_bundle_hash
      AND run.preview_receipt_id = NEW.preview_receipt_id
      AND run.preview_receipt_hash = NEW.preview_receipt_hash
      AND run.promotion_approval_id = NEW.promotion_approval_id
      AND run.promotion_approval_hash = NEW.promotion_approval_hash
      AND run.source_revision_id IS NOT DISTINCT FROM NEW.source_revision_id
      AND run.source_revision_hash IS NOT DISTINCT FROM NEW.source_revision_hash
      AND preview.decision = 'passed'
      AND approval.release_bundle_id = NEW.release_bundle_id
      AND approval.release_bundle_hash = NEW.release_bundle_hash
  ) THEN
    RAISE EXCEPTION 'ProductionReceipt requires the exact verifying Run, passed PreviewReceipt, Approval, Bundle, and rollback source'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_production_receipt_insert_guard
BEFORE INSERT ON release_production_receipts
FOR EACH ROW EXECUTE FUNCTION validate_release_production_receipt_insert();

CREATE TABLE release_deployment_revisions (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-deployment-revision/v1'),
  run_id uuid NOT NULL UNIQUE REFERENCES release_deployment_runs(id) ON DELETE RESTRICT,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  operation text NOT NULL CHECK (operation IN ('promote','rollback')),
  release_bundle_id uuid NOT NULL,
  release_bundle_hash text NOT NULL CHECK (release_bundle_hash ~ '^sha256:[0-9a-f]{64}$'),
  preview_receipt_id uuid NOT NULL,
  preview_receipt_hash text NOT NULL CHECK (preview_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  promotion_approval_id uuid NOT NULL,
  promotion_approval_hash text NOT NULL CHECK (promotion_approval_hash ~ '^sha256:[0-9a-f]{64}$'),
  production_receipt_id uuid NOT NULL,
  production_receipt_hash text NOT NULL CHECK (production_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  source_revision_id uuid,
  source_revision_hash text,
  provider text NOT NULL CHECK (provider = btrim(provider) AND length(provider) BETWEEN 1 AND 128),
  provider_ref text NOT NULL CHECK (provider_ref = btrim(provider_ref) AND length(provider_ref) BETWEEN 1 AND 1000),
  public_url text NOT NULL CHECK (public_url = btrim(public_url) AND length(public_url) BETWEEN 1 AND 2000),
  checks jsonb NOT NULL CHECK (jsonb_typeof(checks) = 'array' AND jsonb_array_length(checks) BETWEEN 1 AND 128),
  content_store text NOT NULL CHECK (content_store = btrim(content_store) AND length(content_store) BETWEEN 1 AND 80),
  content_ref text NOT NULL UNIQUE CHECK (content_ref = btrim(content_ref) AND length(content_ref) BETWEEN 1 AND 2000),
  content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
  payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_deployment_revision_exact_unique UNIQUE (id, payload_hash),
  CONSTRAINT release_deployment_revision_bundle_exact_fk
    FOREIGN KEY (release_bundle_id, release_bundle_hash)
    REFERENCES release_bundles(id, bundle_hash) ON DELETE RESTRICT,
  CONSTRAINT release_deployment_revision_preview_exact_fk
    FOREIGN KEY (preview_receipt_id, preview_receipt_hash)
    REFERENCES release_preview_receipts(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_deployment_revision_approval_exact_fk
    FOREIGN KEY (promotion_approval_id, promotion_approval_hash)
    REFERENCES release_promotion_approvals(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_deployment_revision_production_receipt_exact_fk
    FOREIGN KEY (production_receipt_id, production_receipt_hash)
    REFERENCES release_production_receipts(id, payload_hash) ON DELETE RESTRICT,
  CONSTRAINT release_deployment_revision_source_shape CHECK (
    (operation = 'promote' AND source_revision_id IS NULL AND source_revision_hash IS NULL)
    OR (operation = 'rollback' AND source_revision_id IS NOT NULL
      AND source_revision_hash ~ '^sha256:[0-9a-f]{64}$')
  )
);

ALTER TABLE release_deployment_runs
  ADD CONSTRAINT release_deployment_run_source_exact_fk
  FOREIGN KEY (source_revision_id, source_revision_hash)
  REFERENCES release_deployment_revisions(id, payload_hash) ON DELETE RESTRICT;

ALTER TABLE release_deployment_revisions
  ADD CONSTRAINT release_deployment_revision_source_exact_fk
  FOREIGN KEY (source_revision_id, source_revision_hash)
  REFERENCES release_deployment_revisions(id, payload_hash) ON DELETE RESTRICT;

ALTER TABLE release_production_receipts
  ADD CONSTRAINT release_production_receipt_source_exact_fk
  FOREIGN KEY (source_revision_id, source_revision_hash)
  REFERENCES release_deployment_revisions(id, payload_hash) ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION validate_release_deployment_revision_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM release_deployment_runs AS run
    JOIN release_preview_receipts AS preview
      ON preview.id = run.preview_receipt_id AND preview.payload_hash = run.preview_receipt_hash
    JOIN release_promotion_approvals AS approval
      ON approval.id = run.promotion_approval_id AND approval.payload_hash = run.promotion_approval_hash
    JOIN release_production_receipts AS receipt
      ON receipt.id = NEW.production_receipt_id AND receipt.payload_hash = NEW.production_receipt_hash
    WHERE run.id = NEW.run_id
      AND run.project_id = NEW.project_id
      AND run.state = 'verifying'
      AND run.operation = NEW.operation
      AND run.release_bundle_id = NEW.release_bundle_id
      AND run.release_bundle_hash = NEW.release_bundle_hash
      AND run.preview_receipt_id = NEW.preview_receipt_id
      AND run.preview_receipt_hash = NEW.preview_receipt_hash
      AND run.promotion_approval_id = NEW.promotion_approval_id
      AND run.promotion_approval_hash = NEW.promotion_approval_hash
      AND run.source_revision_id IS NOT DISTINCT FROM NEW.source_revision_id
      AND run.source_revision_hash IS NOT DISTINCT FROM NEW.source_revision_hash
      AND preview.decision = 'passed'
      AND preview.release_bundle_id = NEW.release_bundle_id
      AND preview.release_bundle_hash = NEW.release_bundle_hash
      AND approval.release_bundle_id = NEW.release_bundle_id
      AND approval.release_bundle_hash = NEW.release_bundle_hash
      AND receipt.run_id = NEW.run_id
      AND receipt.project_id = NEW.project_id
      AND receipt.decision = 'passed'
      AND receipt.operation = NEW.operation
      AND receipt.release_bundle_id = NEW.release_bundle_id
      AND receipt.release_bundle_hash = NEW.release_bundle_hash
      AND receipt.preview_receipt_id = NEW.preview_receipt_id
      AND receipt.preview_receipt_hash = NEW.preview_receipt_hash
      AND receipt.promotion_approval_id = NEW.promotion_approval_id
      AND receipt.promotion_approval_hash = NEW.promotion_approval_hash
      AND receipt.source_revision_id IS NOT DISTINCT FROM NEW.source_revision_id
      AND receipt.source_revision_hash IS NOT DISTINCT FROM NEW.source_revision_hash
      AND receipt.provider = NEW.provider
      AND receipt.provider_ref = NEW.provider_ref
      AND receipt.public_url = NEW.public_url
      AND receipt.checks = NEW.checks
  ) THEN
    RAISE EXCEPTION 'DeploymentRevision requires the exact passed ProductionReceipt for the verifying Run'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_deployment_revision_insert_guard
BEFORE INSERT ON release_deployment_revisions
FOR EACH ROW EXECUTE FUNCTION validate_release_deployment_revision_insert();

CREATE OR REPLACE FUNCTION prevent_release_delivery_fact_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'Release delivery receipts, approvals, and revisions are immutable'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER release_preview_receipt_immutable
BEFORE UPDATE OR DELETE ON release_preview_receipts
FOR EACH ROW EXECUTE FUNCTION prevent_release_delivery_fact_mutation();

CREATE TRIGGER release_promotion_approval_immutable
BEFORE UPDATE OR DELETE ON release_promotion_approvals
FOR EACH ROW EXECUTE FUNCTION prevent_release_delivery_fact_mutation();

CREATE TRIGGER release_production_receipt_immutable
BEFORE UPDATE OR DELETE ON release_production_receipts
FOR EACH ROW EXECUTE FUNCTION prevent_release_delivery_fact_mutation();

CREATE TRIGGER release_deployment_revision_immutable
BEFORE UPDATE OR DELETE ON release_deployment_revisions
FOR EACH ROW EXECUTE FUNCTION prevent_release_delivery_fact_mutation();

CREATE OR REPLACE FUNCTION validate_release_delivery_run_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_identity jsonb;
  new_identity jsonb;
BEGIN
  old_identity := to_jsonb(OLD) - ARRAY[
    'state','version','fence_epoch','lease_worker_id','lease_epoch','lease_expires_at',
    'started_at','finished_at','updated_by','updated_at'
  ];
  new_identity := to_jsonb(NEW) - ARRAY[
    'state','version','fence_epoch','lease_worker_id','lease_epoch','lease_expires_at',
    'started_at','finished_at','updated_by','updated_at'
  ];
  IF old_identity IS DISTINCT FROM new_identity THEN
    RAISE EXCEPTION 'release delivery Run authority identity is immutable'
      USING ERRCODE = '55000';
  END IF;

  IF NEW.state = OLD.state THEN
    IF OLD.state NOT IN ('claimed','deploying','verifying')
       OR NEW.version <> OLD.version
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_expires_at <= OLD.lease_expires_at THEN
      RAISE EXCEPTION 'release delivery same-state update must be a fenced lease renewal'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'release delivery transition must increment version exactly once'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state = 'claimed' THEN
    IF NOT (
      OLD.state = 'queued'
      OR (OLD.state IN ('claimed','deploying','verifying') AND OLD.lease_expires_at <= statement_timestamp())
    ) OR NEW.fence_epoch <> OLD.fence_epoch + 1
      OR NEW.lease_epoch <> NEW.fence_epoch THEN
      RAISE EXCEPTION 'release delivery claim must establish a new fence over queued or expired work'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.fence_epoch <> OLD.fence_epoch
     OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
     OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch THEN
    IF NEW.state NOT IN ('passed','healthy','failed','error','cancelled')
       OR NEW.lease_worker_id IS NOT NULL OR NEW.lease_epoch IS NOT NULL OR NEW.lease_expires_at IS NOT NULL THEN
      RAISE EXCEPTION 'release delivery transition changed its worker fence'
        USING ERRCODE = '40001';
    END IF;
  END IF;

  IF NOT (
    (OLD.state = 'claimed' AND NEW.state = 'deploying')
    OR (OLD.state = 'deploying' AND NEW.state = 'verifying')
    OR (OLD.state IN ('claimed','deploying','verifying') AND NEW.state IN ('failed','error','cancelled'))
    OR (TG_TABLE_NAME = 'release_preview_runs' AND OLD.state = 'verifying' AND NEW.state IN ('passed','failed'))
    OR (TG_TABLE_NAME = 'release_deployment_runs' AND OLD.state = 'verifying' AND NEW.state IN ('healthy','failed'))
  ) THEN
    RAISE EXCEPTION 'invalid release delivery state transition'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_preview_runs' AND NEW.state IN ('passed','failed')
     AND NOT EXISTS (
       SELECT 1 FROM release_preview_receipts AS receipt
       WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
         AND receipt.decision = NEW.state
     ) THEN
    RAISE EXCEPTION 'terminal Preview Run requires its exact immutable PreviewReceipt'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'failed'
     AND NOT EXISTS (
       SELECT 1 FROM release_production_receipts AS receipt
       WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
         AND receipt.decision = 'failed'
     ) THEN
    RAISE EXCEPTION 'failed production Run requires its exact immutable failed ProductionReceipt'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'healthy'
     AND NOT EXISTS (
       SELECT 1
       FROM release_production_receipts AS receipt
       JOIN release_deployment_revisions AS revision
         ON revision.production_receipt_id = receipt.id
        AND revision.production_receipt_hash = receipt.payload_hash
       WHERE receipt.run_id = NEW.id AND receipt.project_id = NEW.project_id
         AND receipt.decision = 'passed' AND revision.run_id = NEW.id
     ) THEN
    RAISE EXCEPTION 'healthy production Run requires its exact passed ProductionReceipt and DeploymentRevision'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_preview_run_update_guard
BEFORE UPDATE ON release_preview_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_update();

CREATE TRIGGER release_deployment_run_update_guard
BEFORE UPDATE ON release_deployment_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_update();
