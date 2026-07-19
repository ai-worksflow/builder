-- A delivery request can commit remotely while the local POST times out.  The
-- Run state alone cannot distinguish that case from a request which never
-- reached the controller.  Version 2 therefore records one stable operation
-- identity, every fenced attempt, and the controller's immutable exact result.
-- Unknown outcomes are reconciled; they are never resubmitted as new work.

DROP TRIGGER IF EXISTS release_preview_run_update_guard ON release_preview_runs;
DROP TRIGGER IF EXISTS release_deployment_run_update_guard ON release_deployment_runs;

DROP INDEX IF EXISTS release_preview_runs_claim_idx;
DROP INDEX IF EXISTS release_deployment_runs_claim_idx;
DROP INDEX IF EXISTS release_deployment_runs_one_nonterminal_environment_idx;

ALTER TABLE release_preview_runs
  DROP CONSTRAINT release_preview_runs_schema_version_check,
  DROP CONSTRAINT release_preview_runs_state_check,
  DROP CONSTRAINT release_preview_run_terminal_shape,
  DROP CONSTRAINT release_preview_run_lease_shape;

ALTER TABLE release_deployment_runs
  DROP CONSTRAINT release_deployment_runs_schema_version_check,
  DROP CONSTRAINT release_deployment_runs_state_check,
  DROP CONSTRAINT release_deployment_run_terminal_shape,
  DROP CONSTRAINT release_deployment_run_lease_shape;

-- A v1 active Run has no stable controller operation identity.  It cannot be
-- safely resumed or terminalized after this boundary.  Preserve it visibly as
-- blocked authority and require an explicit operator reconciliation.
UPDATE release_preview_runs
SET state = 'reconcile_blocked',
    version = version + 1,
    lease_worker_id = NULL,
    lease_epoch = NULL,
    lease_expires_at = NULL,
    finished_at = NULL,
    updated_at = statement_timestamp()
WHERE schema_version = 'release-preview-run/v1'
  AND state IN ('queued','claimed','deploying','verifying');

UPDATE release_deployment_runs
SET state = 'reconcile_blocked',
    version = version + 1,
    lease_worker_id = NULL,
    lease_epoch = NULL,
    lease_expires_at = NULL,
    finished_at = NULL,
    updated_at = statement_timestamp()
WHERE schema_version = 'release-deployment-run/v1'
  AND state IN ('queued','claimed','deploying','verifying');

ALTER TABLE release_preview_runs
  ADD CONSTRAINT release_preview_runs_schema_version_check CHECK (
    schema_version IN ('release-preview-run/v1','release-preview-run/v2')
  ),
  ADD CONSTRAINT release_preview_runs_state_check CHECK (
    (schema_version = 'release-preview-run/v1'
      AND state IN ('passed','failed','error','cancelled','reconcile_blocked'))
    OR
    (schema_version = 'release-preview-run/v2'
      AND state IN (
        'queued','claimed','submitting','reconcile_wait','reconciling','verifying',
        'passed','failed','error','cancelled','reconcile_blocked'
      ))
  ),
  ADD CONSTRAINT release_preview_run_terminal_shape CHECK (
    (state IN ('passed','failed','error','cancelled') AND finished_at IS NOT NULL)
    OR (state NOT IN ('passed','failed','error','cancelled') AND finished_at IS NULL)
  ),
  ADD CONSTRAINT release_preview_run_lease_shape CHECK (
    (schema_version = 'release-preview-run/v2'
      AND state IN ('claimed','submitting','reconciling','verifying')
      AND lease_worker_id IS NOT NULL
      AND lease_epoch IS NOT NULL
      AND lease_epoch = fence_epoch
      AND lease_expires_at IS NOT NULL)
    OR
    (state NOT IN ('claimed','submitting','reconciling','verifying')
      AND lease_worker_id IS NULL AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  );

ALTER TABLE release_deployment_runs
  ADD CONSTRAINT release_deployment_runs_schema_version_check CHECK (
    schema_version IN ('release-deployment-run/v1','release-deployment-run/v2')
  ),
  ADD CONSTRAINT release_deployment_runs_state_check CHECK (
    (schema_version = 'release-deployment-run/v1'
      AND state IN ('healthy','failed','error','cancelled','reconcile_blocked'))
    OR
    (schema_version = 'release-deployment-run/v2'
      AND state IN (
        'queued','claimed','submitting','reconcile_wait','reconciling','verifying',
        'healthy','failed','error','cancelled','reconcile_blocked'
      ))
  ),
  ADD CONSTRAINT release_deployment_run_terminal_shape CHECK (
    (state IN ('healthy','failed','error','cancelled') AND finished_at IS NOT NULL)
    OR (state NOT IN ('healthy','failed','error','cancelled') AND finished_at IS NULL)
  ),
  ADD CONSTRAINT release_deployment_run_lease_shape CHECK (
    (schema_version = 'release-deployment-run/v2'
      AND state IN ('claimed','submitting','reconciling','verifying')
      AND lease_worker_id IS NOT NULL
      AND lease_epoch IS NOT NULL
      AND lease_epoch = fence_epoch
      AND lease_expires_at IS NOT NULL)
    OR
    (state NOT IN ('claimed','submitting','reconciling','verifying')
      AND lease_worker_id IS NULL AND lease_epoch IS NULL AND lease_expires_at IS NULL)
  );

-- Go's canonical JSON is recursively key-sorted, whitespace-free JSON with
-- encoding/json's HTML-safe string escaping.  Keep the canonical bytes as
-- text so the exact request can be replayed after a crash, and independently
-- recompute its hash at the database boundary.
CREATE OR REPLACE FUNCTION release_delivery_canonical_json(value jsonb)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
DECLARE
  kind text := jsonb_typeof(value);
  encoded text;
BEGIN
  IF kind IN ('null','boolean') THEN
    RETURN value::text;
  END IF;
  IF kind = 'number' THEN
    IF value::text !~ '^-?(0|[1-9][0-9]*)$' THEN
      RAISE EXCEPTION 'release delivery canonical JSON permits integer numbers only'
        USING ERRCODE = '22023';
    END IF;
    RETURN value::text;
  END IF;
  IF kind = 'string' THEN
    encoded := value::text;
    encoded := replace(encoded, '&', '\u0026');
    encoded := replace(encoded, '<', '\u003c');
    encoded := replace(encoded, '>', '\u003e');
    encoded := replace(encoded, U&'\2028', '\u2028');
    encoded := replace(encoded, U&'\2029', '\u2029');
    RETURN encoded;
  END IF;
  IF kind = 'array' THEN
    SELECT '[' || COALESCE(string_agg(
      release_delivery_canonical_json(item.value), ',' ORDER BY item.ordinal
    ), '') || ']'
    INTO encoded
    FROM jsonb_array_elements(value) WITH ORDINALITY AS item(value, ordinal);
    RETURN encoded;
  END IF;
  IF kind = 'object' THEN
    SELECT '{' || COALESCE(string_agg(
      release_delivery_canonical_json(to_jsonb(entry.key)) || ':' ||
        release_delivery_canonical_json(entry.value),
      ',' ORDER BY entry.key COLLATE "C"
    ), '') || '}'
    INTO encoded
    FROM jsonb_each(value) AS entry(key, value);
    RETURN encoded;
  END IF;
  RAISE EXCEPTION 'unsupported release delivery JSON value'
    USING ERRCODE = '22023';
END;
$$;

CREATE TABLE release_delivery_operations (
  id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (schema_version = 'release-delivery-operation/v1'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  kind text NOT NULL CHECK (kind IN ('preview','production')),
  preview_run_id uuid UNIQUE REFERENCES release_preview_runs(id) ON DELETE RESTRICT,
  deployment_run_id uuid UNIQUE REFERENCES release_deployment_runs(id) ON DELETE RESTRICT,
  request_schema_version text NOT NULL CHECK (
    request_schema_version = 'release-delivery-operation-request/v3'
  ),
  request_document text NOT NULL CHECK (
    jsonb_typeof(request_document::jsonb) = 'object'
    AND octet_length(request_document) BETWEEN 2 AND 16777216
    AND request_document = release_delivery_canonical_json(request_document::jsonb)
  ),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  controller_schema_version text NOT NULL CHECK (
    controller_schema_version = 'release-delivery-controller-identity/v1'
  ),
  controller_id text NOT NULL CHECK (
    controller_id = btrim(controller_id) AND length(controller_id) BETWEEN 1 AND 200
  ),
  controller_version text NOT NULL CHECK (
    controller_version = btrim(controller_version) AND length(controller_version) BETWEEN 1 AND 120
  ),
  controller_protocol text NOT NULL CHECK (
    controller_protocol = 'worksflow.release-delivery/v3'
  ),
  controller_trust_key_digest text NOT NULL CHECK (
    controller_trust_key_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  remote_state text NOT NULL CHECK (remote_state IN (
    'prepared','submit_unknown','accepted','running','completed','rejected','quarantined'
  )),
  submit_attempt_count bigint NOT NULL DEFAULT 0 CHECK (submit_attempt_count >= 0),
  reconcile_attempt_count bigint NOT NULL DEFAULT 0 CHECK (reconcile_attempt_count >= 0),
  next_attempt_at timestamptz,
  first_submitted_at timestamptz,
  last_attempt_at timestamptz,
  last_observation_sequence bigint CHECK (
    last_observation_sequence IS NULL OR last_observation_sequence > 0
  ),
  last_observed_at timestamptz,
  terminal_result_hash text CHECK (
    terminal_result_hash IS NULL OR terminal_result_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  last_error_code text CHECK (
    last_error_code IS NULL
    OR (last_error_code = btrim(last_error_code) AND length(last_error_code) BETWEEN 1 AND 128)
  ),
  last_error_detail text CHECK (last_error_detail IS NULL OR length(last_error_detail) <= 4000),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_delivery_operation_run_shape CHECK (
    (kind = 'preview' AND preview_run_id IS NOT NULL AND deployment_run_id IS NULL)
    OR
    (kind = 'production' AND deployment_run_id IS NOT NULL AND preview_run_id IS NULL)
  ),
  CONSTRAINT release_delivery_operation_exact_unique UNIQUE (id, request_hash),
  CONSTRAINT release_delivery_operation_attempt_projection_shape CHECK (
    (submit_attempt_count = 0 AND first_submitted_at IS NULL)
    OR (submit_attempt_count > 0 AND first_submitted_at IS NOT NULL)
  ),
  CONSTRAINT release_delivery_operation_last_attempt_shape CHECK (
    (submit_attempt_count + reconcile_attempt_count = 0 AND last_attempt_at IS NULL)
    OR (submit_attempt_count + reconcile_attempt_count > 0 AND last_attempt_at IS NOT NULL)
  ),
  CONSTRAINT release_delivery_operation_observation_shape CHECK (
    (last_observation_sequence IS NULL AND last_observed_at IS NULL)
    OR (last_observation_sequence IS NOT NULL AND last_observed_at IS NOT NULL)
  ),
  CONSTRAINT release_delivery_operation_remote_shape CHECK (
    (remote_state = 'prepared' AND next_attempt_at IS NULL
      AND last_observation_sequence IS NULL AND terminal_result_hash IS NULL)
    OR
    (remote_state = 'submit_unknown'
      AND next_attempt_at IS NOT NULL
      AND terminal_result_hash IS NULL)
    OR
    (remote_state IN ('accepted','running')
      AND next_attempt_at IS NOT NULL AND last_observation_sequence IS NOT NULL
      AND terminal_result_hash IS NULL)
    OR
    (remote_state IN ('completed','rejected')
      AND next_attempt_at IS NULL AND last_observation_sequence IS NOT NULL
      AND terminal_result_hash IS NOT NULL)
    OR
    (remote_state = 'quarantined'
      AND next_attempt_at IS NULL
      AND terminal_result_hash IS NULL AND last_error_code IS NOT NULL)
  )
);

CREATE INDEX release_delivery_operations_reconcile_idx
  ON release_delivery_operations (next_attempt_at, created_at, id)
  WHERE remote_state IN ('submit_unknown','accepted','running');

CREATE OR REPLACE FUNCTION validate_release_delivery_operation_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  document jsonb := NEW.request_document::jsonb;
  payload jsonb := document->'payload';
  bundle_document jsonb := payload->'releaseBundle';
BEGIN
  IF NEW.remote_state <> 'prepared'
     OR NEW.submit_attempt_count <> 0
     OR NEW.reconcile_attempt_count <> 0
     OR NEW.next_attempt_at IS NOT NULL
     OR NEW.first_submitted_at IS NOT NULL
     OR NEW.last_attempt_at IS NOT NULL
     OR NEW.last_observation_sequence IS NOT NULL
     OR NEW.last_observed_at IS NOT NULL
     OR NEW.terminal_result_hash IS NOT NULL
     OR NEW.last_error_code IS NOT NULL
     OR NEW.last_error_detail IS NOT NULL THEN
    RAISE EXCEPTION 'release delivery Operation must start as pristine prepared authority'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.request_hash <> 'sha256:' || encode(sha256(
       convert_to(NEW.request_document, 'UTF8')
     ), 'hex')
     OR (SELECT count(*) FROM jsonb_object_keys(document)) <> 5
     OR document->>'schemaVersion' <> 'release-delivery-operation-document/v3'
     OR document->>'operationId' <> NEW.id::text
     OR document->>'kind' <> NEW.kind
     OR document->>'projectId' <> NEW.project_id::text
     OR jsonb_typeof(payload) <> 'object'
     OR payload->>'operationId' <> NEW.id::text
     OR payload->>'projectId' <> NEW.project_id::text THEN
    RAISE EXCEPTION 'Operation request must be exact canonical v3 bytes with its recomputed hash'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.kind = 'preview' THEN
    IF (SELECT count(*) FROM jsonb_object_keys(payload)) <> 7
       OR payload->>'schemaVersion' <> 'release-preview-operation-payload/v1'
       OR payload->>'runId' <> NEW.preview_run_id::text
       OR jsonb_typeof(payload->'reason') <> 'string'
       OR jsonb_typeof(payload->'namespace') <> 'string'
       OR length(payload->>'namespace') NOT BETWEEN 1 AND 200
       OR jsonb_typeof(bundle_document) <> 'object'
       OR NOT EXISTS (
         SELECT 1
         FROM release_preview_runs AS run
         JOIN release_bundles AS bundle
           ON bundle.id = run.release_bundle_id
          AND bundle.bundle_hash = run.release_bundle_hash
         WHERE run.id = NEW.preview_run_id
           AND run.schema_version = 'release-preview-run/v2'
           AND run.project_id = NEW.project_id
           AND run.created_by = NEW.created_by
           AND payload->>'reason' = run.reason
           AND (SELECT count(*) FROM jsonb_object_keys(bundle_document)) = 13
           AND bundle_document->>'schemaVersion' = 'release-bundle/v1'
           AND bundle_document->>'id' = bundle.id::text
           AND bundle_document->>'projectId' = bundle.project_id::text
           AND bundle_document->>'bundleHash' = bundle.bundle_hash
           AND bundle_document->'workspace' = jsonb_build_object(
             'workspaceArtifactId', bundle.workspace_artifact_id::text,
             'workspaceRevisionId', bundle.workspace_revision_id::text,
             'workspaceContentHash', bundle.workspace_content_hash
           )
           AND bundle_document->'canonicalReceipt' = jsonb_build_object(
             'id', bundle.canonical_receipt_id::text,
             'contentHash', bundle.canonical_receipt_hash
           )
           AND jsonb_typeof(bundle_document->'buildManifest') = 'object'
           AND jsonb_typeof(bundle_document->'buildContract') = 'object'
           AND jsonb_typeof(bundle_document->'fullStackTemplate') = 'object'
           AND jsonb_typeof(bundle_document->'verificationProfile') = 'object'
           AND bundle_document->'releaseArtifacts' = bundle.release_artifacts
           AND bundle_document->>'createdBy' = bundle.created_by::text
           AND (bundle_document->>'createdAt')::timestamptz = bundle.created_at
       ) THEN
      RAISE EXCEPTION 'preview Operation requires the exact v2 Run and canonical request projection'
        USING ERRCODE = '40001';
    END IF;
  ELSE
    IF (SELECT count(*) FROM jsonb_object_keys(payload)) <> 12
       OR payload->>'schemaVersion' <> 'release-production-operation-payload/v1'
       OR payload->>'runId' <> NEW.deployment_run_id::text
       OR jsonb_typeof(payload->'reason') <> 'string'
       OR jsonb_typeof(bundle_document) <> 'object'
       OR jsonb_typeof(payload->'previewReceipt') <> 'object'
       OR jsonb_typeof(payload->'promotionApproval') <> 'object'
       OR jsonb_typeof(payload->'expectedHead') <> 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(payload->'expectedHead')) <> 2
       OR NOT EXISTS (
         SELECT 1
         FROM release_deployment_runs AS run
         JOIN release_bundles AS bundle
           ON bundle.id = run.release_bundle_id
          AND bundle.bundle_hash = run.release_bundle_hash
         JOIN release_preview_receipts AS preview
           ON preview.id = run.preview_receipt_id
          AND preview.payload_hash = run.preview_receipt_hash
         JOIN release_promotion_approvals AS approval
           ON approval.id = run.promotion_approval_id
          AND approval.payload_hash = run.promotion_approval_hash
         LEFT JOIN release_deployment_revisions AS source
           ON source.id = run.source_revision_id
          AND source.payload_hash = run.source_revision_hash
         WHERE run.id = NEW.deployment_run_id
           AND run.schema_version = 'release-deployment-run/v2'
           AND run.project_id = NEW.project_id
           AND run.created_by = NEW.created_by
           AND payload->>'reason' = run.reason
           AND payload->>'environment' = run.environment
           AND payload->>'operation' = run.operation
           AND (SELECT count(*) FROM jsonb_object_keys(bundle_document)) = 13
           AND bundle_document->>'schemaVersion' = 'release-bundle/v1'
           AND bundle_document->>'id' = bundle.id::text
           AND bundle_document->>'projectId' = bundle.project_id::text
           AND bundle_document->>'bundleHash' = bundle.bundle_hash
           AND bundle_document->'workspace' = jsonb_build_object(
             'workspaceArtifactId', bundle.workspace_artifact_id::text,
             'workspaceRevisionId', bundle.workspace_revision_id::text,
             'workspaceContentHash', bundle.workspace_content_hash
           )
           AND bundle_document->'canonicalReceipt' = jsonb_build_object(
             'id', bundle.canonical_receipt_id::text,
             'contentHash', bundle.canonical_receipt_hash
           )
           AND jsonb_typeof(bundle_document->'buildManifest') = 'object'
           AND jsonb_typeof(bundle_document->'buildContract') = 'object'
           AND jsonb_typeof(bundle_document->'fullStackTemplate') = 'object'
           AND jsonb_typeof(bundle_document->'verificationProfile') = 'object'
           AND bundle_document->'releaseArtifacts' = bundle.release_artifacts
           AND bundle_document->>'createdBy' = bundle.created_by::text
           AND (bundle_document->>'createdAt')::timestamptz = bundle.created_at
           AND payload->'previewReceipt'->>'schemaVersion'
             IN ('release-preview-receipt/v1','release-preview-receipt/v2')
           AND payload->'previewReceipt'->>'id' = preview.id::text
           AND payload->'previewReceipt'->>'runId' = preview.run_id::text
           AND payload->'previewReceipt'->>'projectId' = preview.project_id::text
           AND payload->'previewReceipt'->>'payloadHash' = preview.payload_hash
           AND payload->'previewReceipt'->'releaseBundle' = jsonb_build_object(
             'id', preview.release_bundle_id::text,
             'contentHash', preview.release_bundle_hash
           )
           AND payload->'previewReceipt'->'releaseArtifacts' = preview.release_artifacts
           AND payload->'previewReceipt'->>'namespace' = preview.namespace
           AND payload->'previewReceipt'->>'provider' = preview.provider
           AND payload->'previewReceipt'->>'providerRef' = preview.provider_ref
           AND payload->'previewReceipt'->'checks' = preview.checks
           AND payload->'previewReceipt'->>'decision' = preview.decision
           AND payload->'previewReceipt'->>'createdBy' = preview.created_by::text
           AND (payload->'previewReceipt'->>'createdAt')::timestamptz = preview.created_at
           AND payload->'promotionApproval'->>'schemaVersion'
             = 'release-promotion-approval/v1'
           AND payload->'promotionApproval'->>'id' = approval.id::text
           AND payload->'promotionApproval'->>'projectId' = approval.project_id::text
           AND payload->'promotionApproval'->>'payloadHash' = approval.payload_hash
           AND payload->'promotionApproval'->>'reason' = approval.reason
           AND payload->'promotionApproval'->>'createdBy' = approval.created_by::text
           AND (payload->'promotionApproval'->>'createdAt')::timestamptz = approval.created_at
           AND (
             (run.source_revision_id IS NULL AND payload->'sourceRevision' = 'null'::jsonb)
             OR
             (run.source_revision_id IS NOT NULL
               AND jsonb_typeof(payload->'sourceRevision') = 'object'
               AND payload->'sourceRevision'->>'id' = source.id::text
               AND payload->'sourceRevision'->>'runId' = source.run_id::text
               AND payload->'sourceRevision'->>'projectId' = source.project_id::text
               AND payload->'sourceRevision'->>'payloadHash' = source.payload_hash
               AND payload->'sourceRevision'->>'operation' = source.operation
               AND payload->'sourceRevision'->'releaseBundle' = jsonb_build_object(
                 'id', source.release_bundle_id::text,
                 'contentHash', source.release_bundle_hash
               )
               AND payload->'sourceRevision'->'checks' = source.checks)
           )
           AND payload->'expectedHead' = jsonb_build_object(
             'revision', CASE WHEN run.expected_revision_id IS NULL THEN 'null'::jsonb
               ELSE jsonb_build_object(
                 'id', run.expected_revision_id::text,
                 'contentHash', run.expected_revision_hash
               ) END,
             'productionReceipt', CASE WHEN run.expected_production_receipt_id IS NULL
               THEN 'null'::jsonb ELSE jsonb_build_object(
                 'id', run.expected_production_receipt_id::text,
                 'contentHash', run.expected_production_receipt_hash
               ) END
           )
       ) THEN
      RAISE EXCEPTION 'production Operation requires the exact v2 Run, lineage, and expected head'
        USING ERRCODE = '40001';
    END IF;
  END IF;
  NEW.created_at := statement_timestamp();
  NEW.updated_at := NEW.created_at;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_operation_insert_guard
BEFORE INSERT ON release_delivery_operations
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_operation_insert();

CREATE TABLE release_delivery_operation_attempts (
  operation_id uuid NOT NULL REFERENCES release_delivery_operations(id) ON DELETE RESTRICT,
  ordinal bigint NOT NULL CHECK (ordinal > 0),
  schema_version text NOT NULL CHECK (
    schema_version = 'release-delivery-operation-attempt/v1'
  ),
  kind text NOT NULL CHECK (kind IN ('submit','reconcile','resubmit')),
  worker_id text NOT NULL CHECK (
    worker_id = btrim(worker_id) AND length(worker_id) BETWEEN 1 AND 200
  ),
  fence_epoch bigint NOT NULL CHECK (fence_epoch > 0),
  started_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  completed_at timestamptz,
  outcome text CHECK (outcome IS NULL OR outcome IN (
    'accepted','running','completed','rejected','unknown','retryable_error',
    'not_found','quarantined'
  )),
  http_status integer CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 599),
  response_hash text CHECK (
    response_hash IS NULL OR response_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  observation_sequence bigint CHECK (
    observation_sequence IS NULL OR observation_sequence > 0
  ),
  observed_at timestamptz,
  error_code text CHECK (
    error_code IS NULL
    OR (error_code = btrim(error_code) AND length(error_code) BETWEEN 1 AND 128)
  ),
  error_detail text CHECK (error_detail IS NULL OR length(error_detail) <= 4000),
  PRIMARY KEY (operation_id, ordinal),
  CONSTRAINT release_delivery_operation_attempt_fence_unique
    UNIQUE (operation_id, kind, fence_epoch),
  CONSTRAINT release_delivery_operation_attempt_completion_shape CHECK (
    (outcome IS NULL AND completed_at IS NULL AND http_status IS NULL
      AND response_hash IS NULL AND observation_sequence IS NULL AND observed_at IS NULL
      AND error_code IS NULL AND error_detail IS NULL)
    OR
    (outcome IN ('accepted','running','completed','rejected')
      AND completed_at IS NOT NULL AND response_hash IS NOT NULL
      AND observation_sequence IS NOT NULL AND observed_at IS NOT NULL
      AND error_code IS NULL AND error_detail IS NULL)
    OR
    (outcome = 'not_found' AND completed_at IS NOT NULL AND http_status = 404
      AND response_hash IS NULL AND observation_sequence IS NULL AND observed_at IS NULL
      AND error_code IS NOT NULL)
    OR
    (outcome IN ('unknown','retryable_error','quarantined')
      AND completed_at IS NOT NULL AND response_hash IS NULL
      AND observation_sequence IS NULL AND observed_at IS NULL
      AND error_code IS NOT NULL)
  )
);

CREATE INDEX release_delivery_operation_attempt_open_idx
  ON release_delivery_operation_attempts (operation_id)
  WHERE completed_at IS NULL;

CREATE OR REPLACE FUNCTION validate_release_delivery_operation_attempt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  operation release_delivery_operations%ROWTYPE;
  run_state text;
  run_worker text;
  run_fence bigint;
  run_expires timestamptz;
BEGIN
  SELECT * INTO STRICT operation
  FROM release_delivery_operations
  WHERE id = NEW.operation_id
  FOR UPDATE;

  IF operation.kind = 'preview' THEN
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_preview_runs WHERE id = operation.preview_run_id;
  ELSE
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_deployment_runs AS run WHERE run.id = operation.deployment_run_id;
  END IF;

  IF NEW.schema_version <> 'release-delivery-operation-attempt/v1'
     OR NEW.worker_id IS DISTINCT FROM run_worker
     OR NEW.fence_epoch IS DISTINCT FROM run_fence
     OR run_expires <= statement_timestamp()
     OR (NEW.kind = 'submit' AND NOT (
       run_state = 'submitting' AND operation.remote_state = 'prepared'
     ))
     OR (NEW.kind = 'reconcile' AND NOT (
       run_state = 'reconciling'
       AND operation.remote_state IN ('prepared','submit_unknown','accepted','running')
     ))
     OR (NEW.kind = 'resubmit' AND NOT (
       run_state = 'reconciling'
       AND operation.remote_state IN ('prepared','submit_unknown')
       AND EXISTS (
         SELECT 1
         FROM release_delivery_operation_attempts AS prior
         WHERE prior.operation_id = NEW.operation_id
           AND prior.kind = 'reconcile'
           AND prior.fence_epoch = NEW.fence_epoch
           AND prior.outcome = 'not_found'
       )
     ))
     OR NEW.completed_at IS NOT NULL
     OR NEW.outcome IS NOT NULL
     OR NEW.http_status IS NOT NULL
     OR NEW.response_hash IS NOT NULL
     OR NEW.observation_sequence IS NOT NULL
     OR NEW.observed_at IS NOT NULL
     OR NEW.error_code IS NOT NULL
     OR NEW.error_detail IS NOT NULL THEN
    RAISE EXCEPTION 'delivery attempt requires the exact live Run fence and operation phase'
      USING ERRCODE = '40001';
  END IF;

  SELECT count(*) + 1 INTO NEW.ordinal
  FROM release_delivery_operation_attempts
  WHERE operation_id = NEW.operation_id;
  NEW.started_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_operation_attempt_insert_guard
BEFORE INSERT ON release_delivery_operation_attempts
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_operation_attempt_insert();

CREATE OR REPLACE FUNCTION validate_release_delivery_operation_attempt_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  operation release_delivery_operations%ROWTYPE;
  old_identity jsonb;
  new_identity jsonb;
  run_state text;
  run_worker text;
  run_fence bigint;
  run_expires timestamptz;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'release delivery operation Attempts are append-only evidence'
      USING ERRCODE = '55000';
  END IF;

  old_identity := to_jsonb(OLD) - ARRAY[
    'completed_at','outcome','http_status','response_hash',
    'observation_sequence','observed_at','error_code','error_detail'
  ];
  new_identity := to_jsonb(NEW) - ARRAY[
    'completed_at','outcome','http_status','response_hash',
    'observation_sequence','observed_at','error_code','error_detail'
  ];
  IF old_identity IS DISTINCT FROM new_identity
     OR OLD.completed_at IS NOT NULL
     OR OLD.outcome IS NOT NULL
     OR NEW.outcome IS NULL THEN
    RAISE EXCEPTION 'delivery attempt may be completed exactly once'
      USING ERRCODE = '55000';
  END IF;

  SELECT * INTO STRICT operation
  FROM release_delivery_operations
  WHERE id = NEW.operation_id
  FOR UPDATE;
  IF operation.kind = 'preview' THEN
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_preview_runs WHERE id = operation.preview_run_id;
  ELSE
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_deployment_runs WHERE id = operation.deployment_run_id;
  END IF;

  IF NEW.worker_id IS DISTINCT FROM run_worker
     OR NEW.fence_epoch IS DISTINCT FROM run_fence
     OR run_expires <= statement_timestamp()
     OR (NEW.kind = 'submit' AND run_state <> 'submitting')
     OR (NEW.kind IN ('reconcile','resubmit') AND run_state <> 'reconciling') THEN
    RAISE EXCEPTION 'stale delivery fence cannot complete an operation Attempt'
      USING ERRCODE = '40001';
  END IF;
  NEW.completed_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_operation_attempt_mutation_guard
BEFORE UPDATE OR DELETE ON release_delivery_operation_attempts
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_operation_attempt_mutation();

CREATE TABLE release_delivery_operation_results (
  operation_id uuid PRIMARY KEY,
  schema_version text NOT NULL CHECK (
    schema_version = 'release-delivery-operation-result/v1'
  ),
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
  kind text NOT NULL CHECK (kind IN ('preview','production')),
  status text NOT NULL CHECK (status IN ('completed','rejected')),
  controller_schema_version text NOT NULL CHECK (
    controller_schema_version = 'release-delivery-controller-identity/v1'
  ),
  controller_id text NOT NULL CHECK (
    controller_id = btrim(controller_id) AND length(controller_id) BETWEEN 1 AND 200
  ),
  controller_version text NOT NULL CHECK (
    controller_version = btrim(controller_version) AND length(controller_version) BETWEEN 1 AND 120
  ),
  controller_protocol text NOT NULL CHECK (
    controller_protocol = 'worksflow.release-delivery/v3'
  ),
  controller_trust_key_digest text NOT NULL CHECK (
    controller_trust_key_digest ~ '^sha256:[0-9a-f]{64}$'
  ),
  provider text,
  provider_ref text,
  public_url text,
  checks jsonb NOT NULL CHECK (
    jsonb_typeof(checks) = 'array' AND jsonb_array_length(checks) <= 128
  ),
  previous_head_id uuid,
  previous_head_hash text CHECK (
    previous_head_hash IS NULL OR previous_head_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  no_mutation boolean NOT NULL,
  rejection_code text,
  rejection_detail text,
  worker_id text NOT NULL CHECK (
    worker_id = btrim(worker_id) AND length(worker_id) BETWEEN 1 AND 200
  ),
  fence_epoch bigint NOT NULL CHECK (fence_epoch > 0),
  completed_at timestamptz NOT NULL,
  result_document text NOT NULL CHECK (
    jsonb_typeof(result_document::jsonb) = 'object'
    AND octet_length(result_document) BETWEEN 2 AND 8388608
    AND result_document = release_delivery_canonical_json(result_document::jsonb)
  ),
  result_hash text NOT NULL CHECK (
    result_hash ~ '^sha256:[0-9a-f]{64}$'
    AND result_hash = 'sha256:' || encode(sha256(convert_to(
      release_delivery_canonical_json(jsonb_set(
        result_document::jsonb, '{resultHash}', '""'::jsonb, false
      )), 'UTF8'
    )), 'hex')
  ),
  created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
  CONSTRAINT release_delivery_operation_result_exact_unique UNIQUE (operation_id, result_hash),
  CONSTRAINT release_delivery_operation_result_request_exact_fk
    FOREIGN KEY (operation_id, request_hash)
    REFERENCES release_delivery_operations(id, request_hash) ON DELETE RESTRICT,
  CONSTRAINT release_delivery_operation_result_status_shape CHECK (
    (status = 'completed'
      AND no_mutation = false AND jsonb_array_length(checks) BETWEEN 1 AND 128
      AND provider IS NOT NULL AND provider = btrim(provider) AND length(provider) BETWEEN 1 AND 128
      AND provider_ref IS NOT NULL AND provider_ref = btrim(provider_ref)
      AND length(provider_ref) BETWEEN 1 AND 1000
      AND rejection_code IS NULL AND rejection_detail IS NULL)
    OR
    (status = 'rejected'
      AND no_mutation = true AND jsonb_array_length(checks) = 0
      AND provider IS NULL AND provider_ref IS NULL AND public_url IS NULL
      AND previous_head_id IS NULL AND previous_head_hash IS NULL
      AND rejection_code IS NOT NULL AND rejection_code = btrim(rejection_code)
      AND length(rejection_code) BETWEEN 1 AND 160
      AND rejection_detail IS NOT NULL AND length(rejection_detail) BETWEEN 1 AND 2000)
  ),
  CONSTRAINT release_delivery_operation_result_head_shape CHECK (
    (previous_head_id IS NULL AND previous_head_hash IS NULL)
    OR (previous_head_id IS NOT NULL AND previous_head_hash IS NOT NULL)
  ),
  CONSTRAINT release_delivery_operation_result_kind_head_shape CHECK (
    (kind = 'preview' AND previous_head_id IS NULL AND previous_head_hash IS NULL)
    OR (kind = 'production' AND status = 'rejected'
      AND previous_head_id IS NULL AND previous_head_hash IS NULL)
    OR (kind = 'production' AND status = 'completed')
  ),
  CONSTRAINT release_delivery_operation_result_optional_shape CHECK (
    (public_url IS NULL OR length(public_url) BETWEEN 1 AND 2000)
    AND (provider IS NULL OR length(provider) BETWEEN 1 AND 128)
    AND (provider_ref IS NULL OR length(provider_ref) BETWEEN 1 AND 1000)
  )
);

ALTER TABLE release_delivery_operations
  ADD CONSTRAINT release_delivery_operation_terminal_result_exact_fk
  FOREIGN KEY (id, terminal_result_hash)
  REFERENCES release_delivery_operation_results(operation_id, result_hash)
  ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE OR REPLACE FUNCTION validate_release_delivery_operation_result_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  operation release_delivery_operations%ROWTYPE;
  attempt release_delivery_operation_attempts%ROWTYPE;
  document jsonb := NEW.result_document::jsonb;
  run_state text;
  run_worker text;
  run_fence bigint;
  run_expires timestamptz;
  expected_revision_id uuid;
  expected_revision_hash text;
  expected_key_count integer;
BEGIN
  SELECT * INTO STRICT operation
  FROM release_delivery_operations
  WHERE id = NEW.operation_id
  FOR UPDATE;
  SELECT * INTO STRICT attempt
  FROM release_delivery_operation_attempts
  WHERE operation_id = NEW.operation_id
  ORDER BY ordinal DESC LIMIT 1;

  IF operation.kind = 'preview' THEN
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_preview_runs WHERE id = operation.preview_run_id;
    expected_revision_id := NULL;
    expected_revision_hash := NULL;
  ELSE
    SELECT run.state, run.lease_worker_id, run.lease_epoch, run.lease_expires_at,
           run.expected_revision_id, run.expected_revision_hash
    INTO STRICT run_state, run_worker, run_fence, run_expires,
                expected_revision_id, expected_revision_hash
    FROM release_deployment_runs AS run WHERE run.id = operation.deployment_run_id;
  END IF;

  IF operation.remote_state NOT IN ('prepared','submit_unknown','accepted','running')
     OR run_state NOT IN ('submitting','reconciling')
     OR run_expires <= statement_timestamp()
     OR NEW.request_hash <> operation.request_hash
     OR NEW.project_id <> operation.project_id
     OR NEW.kind <> operation.kind
     OR NEW.controller_schema_version <> operation.controller_schema_version
     OR NEW.controller_id <> operation.controller_id
     OR NEW.controller_version <> operation.controller_version
     OR NEW.controller_protocol <> operation.controller_protocol
     OR NEW.controller_trust_key_digest <> operation.controller_trust_key_digest
     OR NEW.worker_id IS DISTINCT FROM run_worker
     OR NEW.fence_epoch IS DISTINCT FROM run_fence
     OR attempt.completed_at IS NULL
     OR attempt.worker_id <> NEW.worker_id
     OR attempt.fence_epoch <> NEW.fence_epoch
     OR attempt.outcome <> NEW.status
     OR attempt.response_hash <> NEW.result_hash
     OR attempt.observation_sequence IS NULL
     OR attempt.observed_at IS NULL
     OR NEW.completed_at > attempt.observed_at
     OR (NEW.status = 'completed' AND (
       NEW.previous_head_id IS DISTINCT FROM expected_revision_id
       OR NEW.previous_head_hash IS DISTINCT FROM expected_revision_hash
     )) THEN
    RAISE EXCEPTION 'operation Result requires the exact request, controller, completed Attempt, and live fence'
      USING ERRCODE = '40001';
  END IF;

  expected_key_count := 11
    + CASE WHEN NEW.provider IS NULL THEN 0 ELSE 1 END
    + CASE WHEN NEW.provider_ref IS NULL THEN 0 ELSE 1 END
    + CASE WHEN NEW.public_url IS NULL THEN 0 ELSE 1 END
    + CASE WHEN NEW.previous_head_id IS NULL THEN 0 ELSE 1 END
    + CASE WHEN NEW.rejection_code IS NULL THEN 0 ELSE 1 END
    + CASE WHEN NEW.rejection_detail IS NULL THEN 0 ELSE 1 END;

  IF (SELECT count(*) FROM jsonb_object_keys(document)) <> expected_key_count
     OR document->>'schemaVersion' <> NEW.schema_version
     OR document->>'operationId' <> NEW.operation_id::text
     OR document->>'requestHash' <> NEW.request_hash
     OR document->>'kind' <> NEW.kind
     OR document->>'projectId' <> NEW.project_id::text
     OR document->>'status' <> NEW.status
     OR jsonb_typeof(document->'controller') <> 'object'
     OR (SELECT count(*) FROM jsonb_object_keys(document->'controller')) <> 5
     OR document->'controller' <> jsonb_build_object(
       'schemaVersion', NEW.controller_schema_version,
       'id', NEW.controller_id,
       'version', NEW.controller_version,
       'protocol', NEW.controller_protocol,
       'trustKeyDigest', NEW.controller_trust_key_digest
     )
     OR (document ? 'provider') <> (NEW.provider IS NOT NULL)
     OR (document ? 'providerRef') <> (NEW.provider_ref IS NOT NULL)
     OR (document ? 'publicUrl') <> (NEW.public_url IS NOT NULL)
     OR (document ? 'previousHead') <> (NEW.previous_head_id IS NOT NULL)
     OR (document ? 'rejectionCode') <> (NEW.rejection_code IS NOT NULL)
     OR (document ? 'rejectionDetail') <> (NEW.rejection_detail IS NOT NULL)
     OR (NEW.provider IS NOT NULL AND document->>'provider' <> NEW.provider)
     OR (NEW.provider_ref IS NOT NULL AND document->>'providerRef' <> NEW.provider_ref)
     OR (NEW.public_url IS NOT NULL AND document->>'publicUrl' <> NEW.public_url)
     OR document->'checks' IS DISTINCT FROM NEW.checks
     OR (document->>'noMutation')::boolean IS DISTINCT FROM NEW.no_mutation
     OR (NEW.previous_head_id IS NOT NULL AND document->'previousHead' <> jsonb_build_object(
       'id', NEW.previous_head_id::text, 'contentHash', NEW.previous_head_hash
     ))
     OR (NEW.rejection_code IS NOT NULL AND document->>'rejectionCode' <> NEW.rejection_code)
     OR (NEW.rejection_detail IS NOT NULL AND document->>'rejectionDetail' <> NEW.rejection_detail)
     OR (document->>'completedAt')::timestamptz IS DISTINCT FROM NEW.completed_at
     OR document->>'resultHash' <> NEW.result_hash THEN
    RAISE EXCEPTION 'operation Result document is not its exact canonical projection'
      USING ERRCODE = '40001';
  END IF;
  NEW.created_at := statement_timestamp();
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_operation_result_insert_guard
BEFORE INSERT ON release_delivery_operation_results
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_operation_result_insert();

CREATE OR REPLACE FUNCTION prevent_release_delivery_operation_result_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'release delivery operation Results are immutable exact authority'
    USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER release_delivery_operation_result_immutable
BEFORE UPDATE OR DELETE ON release_delivery_operation_results
FOR EACH ROW EXECUTE FUNCTION prevent_release_delivery_operation_result_mutation();

-- Delivery facts created from v2 Runs carry the exact controller Operation and
-- Result in their own canonical v2 payload.  Historical v1 facts remain
-- readable and cannot be retroactively decorated with v2 authority.
ALTER TABLE release_preview_receipts
  DROP CONSTRAINT release_preview_receipts_schema_version_check,
  ADD COLUMN controller_operation_id uuid,
  ADD COLUMN controller_result_hash text CHECK (
    controller_result_hash IS NULL OR controller_result_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT release_preview_receipts_schema_version_check CHECK (
    schema_version IN ('release-preview-receipt/v1','release-preview-receipt/v2')
  ),
  ADD CONSTRAINT release_preview_receipt_controller_shape CHECK (
    (schema_version = 'release-preview-receipt/v1'
      AND controller_operation_id IS NULL AND controller_result_hash IS NULL)
    OR (schema_version = 'release-preview-receipt/v2'
      AND controller_operation_id IS NOT NULL AND controller_result_hash IS NOT NULL)
  ),
  ADD CONSTRAINT release_preview_receipt_controller_result_exact_fk
    FOREIGN KEY (controller_operation_id, controller_result_hash)
    REFERENCES release_delivery_operation_results(operation_id, result_hash)
    ON DELETE RESTRICT;

ALTER TABLE release_production_receipts
  DROP CONSTRAINT release_production_receipts_schema_version_check,
  ADD COLUMN controller_operation_id uuid,
  ADD COLUMN controller_result_hash text CHECK (
    controller_result_hash IS NULL OR controller_result_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT release_production_receipts_schema_version_check CHECK (
    schema_version IN ('release-production-receipt/v1','release-production-receipt/v2')
  ),
  ADD CONSTRAINT release_production_receipt_controller_shape CHECK (
    (schema_version = 'release-production-receipt/v1'
      AND controller_operation_id IS NULL AND controller_result_hash IS NULL)
    OR (schema_version = 'release-production-receipt/v2'
      AND controller_operation_id IS NOT NULL AND controller_result_hash IS NOT NULL)
  ),
  ADD CONSTRAINT release_production_receipt_controller_result_exact_fk
    FOREIGN KEY (controller_operation_id, controller_result_hash)
    REFERENCES release_delivery_operation_results(operation_id, result_hash)
    ON DELETE RESTRICT;

ALTER TABLE release_deployment_revisions
  DROP CONSTRAINT release_deployment_revisions_schema_version_check,
  ADD COLUMN controller_operation_id uuid,
  ADD COLUMN controller_result_hash text CHECK (
    controller_result_hash IS NULL OR controller_result_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT release_deployment_revisions_schema_version_check CHECK (
    schema_version IN ('release-deployment-revision/v1','release-deployment-revision/v2')
  ),
  ADD CONSTRAINT release_deployment_revision_controller_shape CHECK (
    (schema_version = 'release-deployment-revision/v1'
      AND controller_operation_id IS NULL AND controller_result_hash IS NULL)
    OR (schema_version = 'release-deployment-revision/v2'
      AND controller_operation_id IS NOT NULL AND controller_result_hash IS NOT NULL)
  ),
  ADD CONSTRAINT release_deployment_revision_controller_result_exact_fk
    FOREIGN KEY (controller_operation_id, controller_result_hash)
    REFERENCES release_delivery_operation_results(operation_id, result_hash)
    ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION validate_release_delivery_fact_operation_authority_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_TABLE_NAME = 'release_preview_receipts' THEN
    IF NEW.schema_version <> 'release-preview-receipt/v2'
       OR NOT EXISTS (
         SELECT 1
         FROM release_preview_runs AS run
         JOIN release_delivery_operations AS operation
           ON operation.preview_run_id = run.id
         JOIN release_delivery_operation_results AS result
           ON result.operation_id = operation.id
          AND result.result_hash = operation.terminal_result_hash
         WHERE run.id = NEW.run_id
           AND run.schema_version = 'release-preview-run/v2'
           AND run.state = 'verifying'
           AND operation.id = NEW.controller_operation_id
           AND result.result_hash = NEW.controller_result_hash
           AND result.status = 'completed'
           AND result.kind = 'preview'
           AND result.project_id = NEW.project_id
           AND result.provider = NEW.provider
           AND result.provider_ref = NEW.provider_ref
           AND result.checks = NEW.checks
       ) THEN
      RAISE EXCEPTION 'v2 PreviewReceipt requires its exact immutable controller Operation Result'
        USING ERRCODE = '40001';
    END IF;
  ELSIF TG_TABLE_NAME = 'release_production_receipts' THEN
    IF NEW.schema_version <> 'release-production-receipt/v2'
       OR NOT EXISTS (
         SELECT 1
         FROM release_deployment_runs AS run
         JOIN release_delivery_operations AS operation
           ON operation.deployment_run_id = run.id
         JOIN release_delivery_operation_results AS result
           ON result.operation_id = operation.id
          AND result.result_hash = operation.terminal_result_hash
         WHERE run.id = NEW.run_id
           AND run.schema_version = 'release-deployment-run/v2'
           AND run.state = 'verifying'
           AND operation.id = NEW.controller_operation_id
           AND result.result_hash = NEW.controller_result_hash
           AND result.status = 'completed'
           AND result.kind = 'production'
           AND result.project_id = NEW.project_id
           AND result.provider = NEW.provider
           AND result.provider_ref = NEW.provider_ref
           AND result.public_url IS NOT DISTINCT FROM NULLIF(NEW.public_url, '')
           AND result.checks = NEW.checks
       ) THEN
      RAISE EXCEPTION 'v2 ProductionReceipt requires its exact immutable controller Operation Result'
        USING ERRCODE = '40001';
    END IF;
  ELSE
    IF NEW.schema_version <> 'release-deployment-revision/v2'
       OR NOT EXISTS (
         SELECT 1
         FROM release_deployment_runs AS run
         JOIN release_delivery_operations AS operation
           ON operation.deployment_run_id = run.id
         JOIN release_delivery_operation_results AS result
           ON result.operation_id = operation.id
          AND result.result_hash = operation.terminal_result_hash
         WHERE run.id = NEW.run_id
           AND run.schema_version = 'release-deployment-run/v2'
           AND run.state = 'verifying'
           AND operation.id = NEW.controller_operation_id
           AND result.result_hash = NEW.controller_result_hash
           AND result.status = 'completed'
           AND result.kind = 'production'
           AND result.provider = NEW.provider
           AND result.provider_ref = NEW.provider_ref
           AND result.public_url IS NOT DISTINCT FROM NULLIF(NEW.public_url, '')
           AND result.checks = NEW.checks
       ) THEN
      RAISE EXCEPTION 'v2 DeploymentRevision requires its exact immutable controller Result'
        USING ERRCODE = '40001';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_preview_receipt_operation_authority_guard
BEFORE INSERT ON release_preview_receipts
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_fact_operation_authority_insert();

CREATE TRIGGER release_production_receipt_operation_authority_guard
BEFORE INSERT ON release_production_receipts
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_fact_operation_authority_insert();

CREATE TRIGGER release_deployment_revision_operation_authority_guard
BEFORE INSERT ON release_deployment_revisions
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_fact_operation_authority_insert();

CREATE OR REPLACE FUNCTION validate_release_delivery_operation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_identity jsonb;
  new_identity jsonb;
  actual_submit_count bigint;
  actual_reconcile_count bigint;
  actual_first_submit timestamptz;
  actual_last_attempt timestamptz;
  attempt release_delivery_operation_attempts%ROWTYPE;
  run_state text;
  run_worker text;
  run_fence bigint;
  run_expires timestamptz;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'release delivery Operations cannot be deleted'
      USING ERRCODE = '55000';
  END IF;

  old_identity := to_jsonb(OLD) - ARRAY[
    'remote_state','submit_attempt_count','reconcile_attempt_count',
    'next_attempt_at','first_submitted_at','last_attempt_at',
    'last_observation_sequence','last_observed_at',
    'terminal_result_hash','last_error_code','last_error_detail','updated_at'
  ];
  new_identity := to_jsonb(NEW) - ARRAY[
    'remote_state','submit_attempt_count','reconcile_attempt_count',
    'next_attempt_at','first_submitted_at','last_attempt_at',
    'last_observation_sequence','last_observed_at',
    'terminal_result_hash','last_error_code','last_error_detail','updated_at'
  ];
  IF old_identity IS DISTINCT FROM new_identity THEN
    RAISE EXCEPTION 'release delivery Operation request and controller identity are immutable'
      USING ERRCODE = '55000';
  END IF;

  SELECT
    count(*) FILTER (WHERE kind IN ('submit','resubmit')),
    count(*) FILTER (WHERE kind = 'reconcile'),
    min(started_at) FILTER (WHERE kind IN ('submit','resubmit')),
    max(started_at)
  INTO actual_submit_count, actual_reconcile_count, actual_first_submit, actual_last_attempt
  FROM release_delivery_operation_attempts
  WHERE operation_id = NEW.id;

  IF NEW.submit_attempt_count <> actual_submit_count
     OR NEW.reconcile_attempt_count <> actual_reconcile_count
     OR NEW.first_submitted_at IS DISTINCT FROM actual_first_submit
     OR NEW.last_attempt_at IS DISTINCT FROM actual_last_attempt
     OR NEW.updated_at <= OLD.updated_at THEN
    RAISE EXCEPTION 'release delivery Operation attempt projection is not exact'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.remote_state = OLD.remote_state THEN
    IF NEW.submit_attempt_count + NEW.reconcile_attempt_count
         = OLD.submit_attempt_count + OLD.reconcile_attempt_count + 1 THEN
      IF NEW.next_attempt_at IS DISTINCT FROM OLD.next_attempt_at
         OR NEW.last_observation_sequence IS DISTINCT FROM OLD.last_observation_sequence
         OR NEW.last_observed_at IS DISTINCT FROM OLD.last_observed_at
         OR NEW.terminal_result_hash IS DISTINCT FROM OLD.terminal_result_hash
         OR NEW.last_error_code IS DISTINCT FROM OLD.last_error_code
         OR NEW.last_error_detail IS DISTINCT FROM OLD.last_error_detail THEN
        RAISE EXCEPTION 'Attempt append may only advance exact Operation counters'
          USING ERRCODE = '40001';
      END IF;
      RETURN NEW;
    END IF;
  END IF;

  IF OLD.remote_state IN ('completed','rejected','quarantined') THEN
    RAISE EXCEPTION 'terminal release delivery Operation authority is immutable'
      USING ERRCODE = '55000';
  END IF;
  IF NEW.submit_attempt_count <> OLD.submit_attempt_count
     OR NEW.reconcile_attempt_count <> OLD.reconcile_attempt_count
     OR NEW.first_submitted_at IS DISTINCT FROM OLD.first_submitted_at
     OR NEW.last_attempt_at IS DISTINCT FROM OLD.last_attempt_at THEN
    RAISE EXCEPTION 'Operation state transition cannot forge Attempt projections'
      USING ERRCODE = '40001';
  END IF;

  SELECT * INTO attempt
  FROM release_delivery_operation_attempts
  WHERE operation_id = NEW.id
  ORDER BY ordinal DESC LIMIT 1;
  IF NOT FOUND OR attempt.completed_at IS NULL THEN
    RAISE EXCEPTION 'Operation observation requires one exact completed Attempt'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.kind = 'preview' THEN
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_preview_runs WHERE id = NEW.preview_run_id;
  ELSE
    SELECT state, lease_worker_id, lease_epoch, lease_expires_at
    INTO STRICT run_state, run_worker, run_fence, run_expires
    FROM release_deployment_runs WHERE id = NEW.deployment_run_id;
  END IF;
  IF run_state NOT IN ('submitting','reconciling')
     OR run_expires <= statement_timestamp()
     OR attempt.worker_id IS DISTINCT FROM run_worker
     OR attempt.fence_epoch IS DISTINCT FROM run_fence THEN
    RAISE EXCEPTION 'Operation observation lost its exact live Run fence'
      USING ERRCODE = '40001';
  END IF;

  IF NOT (
    (OLD.remote_state = NEW.remote_state
      AND NEW.remote_state IN ('submit_unknown','accepted','running'))
    OR (OLD.remote_state = 'prepared'
      AND NEW.remote_state IN (
        'submit_unknown','accepted','running','completed','rejected','quarantined'
      ))
    OR (OLD.remote_state = 'submit_unknown'
      AND NEW.remote_state IN ('accepted','running','completed','rejected','quarantined'))
    OR (OLD.remote_state = 'accepted'
      AND NEW.remote_state IN ('running','completed','quarantined'))
    OR (OLD.remote_state = 'running'
      AND NEW.remote_state IN ('completed','quarantined'))
  ) THEN
    RAISE EXCEPTION 'release delivery remote observation is not monotonic'
      USING ERRCODE = '40001';
  END IF;

  IF NOT (
    (NEW.remote_state = 'submit_unknown'
      AND attempt.outcome IN ('unknown','retryable_error'))
    OR (NEW.remote_state = OLD.remote_state
      AND NEW.remote_state IN ('accepted','running')
      AND attempt.outcome IN ('unknown','retryable_error'))
    OR (NEW.remote_state IN ('accepted','running','completed','rejected','quarantined')
      AND attempt.outcome = NEW.remote_state)
  ) THEN
    RAISE EXCEPTION 'Operation state does not match its latest immutable Attempt outcome'
      USING ERRCODE = '40001';
  END IF;

  IF attempt.outcome IN ('accepted','running','completed','rejected') THEN
    IF attempt.observation_sequence IS NULL
       OR attempt.observed_at IS NULL
       OR NEW.last_observation_sequence IS DISTINCT FROM attempt.observation_sequence
       OR NEW.last_observed_at IS DISTINCT FROM attempt.observed_at
       OR (OLD.last_observation_sequence IS NOT NULL
         AND NEW.last_observation_sequence <= OLD.last_observation_sequence) THEN
      RAISE EXCEPTION 'remote observation sequence must advance exactly and monotonically'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.last_observation_sequence IS DISTINCT FROM OLD.last_observation_sequence
        OR NEW.last_observed_at IS DISTINCT FROM OLD.last_observed_at THEN
    RAISE EXCEPTION 'unknown or quarantined Attempt cannot fabricate a remote observation'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.remote_state IN ('completed','rejected') THEN
    IF NEW.next_attempt_at IS NOT NULL
       OR NEW.terminal_result_hash IS NULL
       OR NOT EXISTS (
         SELECT 1
         FROM release_delivery_operation_results AS result
         WHERE result.operation_id = NEW.id
           AND result.request_hash = NEW.request_hash
           AND result.result_hash = NEW.terminal_result_hash
           AND result.status = NEW.remote_state
           AND result.controller_schema_version = NEW.controller_schema_version
           AND result.controller_id = NEW.controller_id
           AND result.controller_version = NEW.controller_version
           AND result.controller_protocol = NEW.controller_protocol
           AND result.controller_trust_key_digest = NEW.controller_trust_key_digest
       ) THEN
      RAISE EXCEPTION 'terminal Operation requires its exact immutable controller Result'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.remote_state = 'quarantined' THEN
    IF NEW.next_attempt_at IS NOT NULL
       OR NEW.terminal_result_hash IS NOT NULL
       OR NEW.last_error_code IS NULL THEN
      RAISE EXCEPTION 'quarantined Operation requires explicit fail-closed evidence'
        USING ERRCODE = '40001';
    END IF;
  ELSIF NEW.next_attempt_at IS NULL OR NEW.terminal_result_hash IS NOT NULL THEN
    RAISE EXCEPTION 'nonterminal observed Operation requires a reconciliation schedule'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_delivery_operation_mutation_guard
BEFORE UPDATE OR DELETE ON release_delivery_operations
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_operation_mutation();

CREATE OR REPLACE FUNCTION project_release_delivery_operation_attempt_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  UPDATE release_delivery_operations
  SET submit_attempt_count = submit_attempt_count
        + CASE WHEN NEW.kind IN ('submit','resubmit') THEN 1 ELSE 0 END,
      reconcile_attempt_count = reconcile_attempt_count
        + CASE WHEN NEW.kind = 'reconcile' THEN 1 ELSE 0 END,
      first_submitted_at = CASE
        WHEN NEW.kind IN ('submit','resubmit')
          THEN COALESCE(first_submitted_at, NEW.started_at)
        ELSE first_submitted_at
      END,
      last_attempt_at = NEW.started_at,
      updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
  WHERE id = NEW.operation_id;
  RETURN NULL;
END;
$$;

CREATE TRIGGER release_delivery_operation_attempt_projection
AFTER INSERT ON release_delivery_operation_attempts
FOR EACH ROW EXECUTE FUNCTION project_release_delivery_operation_attempt_insert();

CREATE OR REPLACE FUNCTION validate_release_delivery_run_insert_v2()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF (TG_TABLE_NAME = 'release_preview_runs'
        AND NEW.schema_version <> 'release-preview-run/v2')
     OR (TG_TABLE_NAME = 'release_deployment_runs'
        AND NEW.schema_version <> 'release-deployment-run/v2') THEN
    RAISE EXCEPTION 'release delivery Run v1 is historical read-only authority'
      USING ERRCODE = '40001';
  END IF;
  IF NEW.state <> 'queued'
     OR NEW.version <> 1
     OR NEW.fence_epoch <> 0
     OR NEW.lease_worker_id IS NOT NULL
     OR NEW.lease_epoch IS NOT NULL
     OR NEW.lease_expires_at IS NOT NULL
     OR NEW.started_at IS NOT NULL
     OR NEW.finished_at IS NOT NULL THEN
    RAISE EXCEPTION 'release delivery Run v2 must start as pristine queued work'
      USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER release_preview_run_v2_insert_guard
BEFORE INSERT ON release_preview_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_insert_v2();

CREATE TRIGGER release_deployment_run_v2_insert_guard
BEFORE INSERT ON release_deployment_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_insert_v2();

CREATE OR REPLACE FUNCTION validate_release_delivery_run_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
  old_identity jsonb;
  new_identity jsonb;
  operation release_delivery_operations%ROWTYPE;
  result release_delivery_operation_results%ROWTYPE;
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
  IF OLD.schema_version IN ('release-preview-run/v1','release-deployment-run/v1') THEN
    RAISE EXCEPTION 'historical v1 release delivery Run is read-only; reconcile explicitly'
      USING ERRCODE = '55000';
  END IF;
  IF NEW.updated_at <= OLD.updated_at THEN
    RAISE EXCEPTION 'release delivery Run update time must advance'
      USING ERRCODE = '40001';
  END IF;

  IF TG_TABLE_NAME = 'release_preview_runs' THEN
    SELECT * INTO operation FROM release_delivery_operations
    WHERE preview_run_id = NEW.id;
  ELSE
    SELECT * INTO operation FROM release_delivery_operations
    WHERE deployment_run_id = NEW.id;
  END IF;
  IF NOT FOUND
     OR operation.project_id <> NEW.project_id THEN
    RAISE EXCEPTION 'release delivery Run transition requires its exact Operation authority'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.state = OLD.state THEN
    -- A live worker may only extend its own lease without changing version.
    IF OLD.state IN ('claimed','submitting','reconciling','verifying')
       AND OLD.lease_expires_at > statement_timestamp()
       AND NEW.version = OLD.version
       AND NEW.fence_epoch = OLD.fence_epoch
       AND NEW.lease_worker_id IS NOT DISTINCT FROM OLD.lease_worker_id
       AND NEW.lease_epoch IS NOT DISTINCT FROM OLD.lease_epoch
       AND NEW.lease_expires_at > OLD.lease_expires_at
       AND NEW.started_at IS NOT DISTINCT FROM OLD.started_at
       AND NEW.finished_at IS NOT DISTINCT FROM OLD.finished_at THEN
      RETURN NEW;
    END IF;
    -- Expired leased work is reclaimed with a new fence.  A submitting Run is
    -- intentionally excluded: it must move to reconciling, never resubmit.
    IF OLD.state IN ('claimed','reconciling','verifying')
       AND OLD.lease_expires_at <= statement_timestamp()
       AND NEW.version = OLD.version + 1
       AND NEW.fence_epoch = OLD.fence_epoch + 1
       AND NEW.lease_epoch = NEW.fence_epoch
       AND NEW.lease_worker_id IS NOT NULL
       AND NEW.lease_expires_at > statement_timestamp()
       AND NEW.started_at IS NOT DISTINCT FROM OLD.started_at
       AND NEW.finished_at IS NOT DISTINCT FROM OLD.finished_at THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'same-state delivery update must renew or reclaim one exact fence'
      USING ERRCODE = '40001';
  END IF;

  IF NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'release delivery transition must increment version exactly once'
      USING ERRCODE = '40001';
  END IF;

  IF OLD.state = 'queued' AND NEW.state = 'claimed' THEN
    IF operation.remote_state <> 'prepared'
       OR NEW.fence_epoch <> OLD.fence_epoch + 1
       OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL
       OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS NULL
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'release delivery claim must establish the first exact fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'reconcile_wait' AND NEW.state = 'reconciling' THEN
    IF operation.remote_state NOT IN ('submit_unknown','accepted','running')
       OR operation.next_attempt_at > statement_timestamp()
       OR NEW.fence_epoch <> OLD.fence_epoch + 1
       OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL
       OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'reconciliation claim requires due unknown remote authority and a new fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'submitting' AND NEW.state = 'reconciling'
     AND OLD.lease_expires_at <= statement_timestamp() THEN
    IF operation.remote_state NOT IN ('prepared','submit_unknown','accepted','running')
       OR NEW.fence_epoch <> OLD.fence_epoch + 1
       OR NEW.lease_epoch <> NEW.fence_epoch
       OR NEW.lease_worker_id IS NULL
       OR NEW.lease_expires_at <= statement_timestamp()
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'expired submission must be fenced into reconciliation'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'claimed' AND NEW.state = 'submitting' THEN
    IF operation.remote_state <> 'prepared'
       OR operation.submit_attempt_count <> 0
       OR OLD.lease_expires_at <= statement_timestamp()
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'submission requires one pristine Operation and the live claim fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'reconcile_wait' THEN
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state NOT IN ('submit_unknown','accepted','running')
       OR operation.next_attempt_at IS NULL
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL
       OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
      RAISE EXCEPTION 'unknown delivery outcome must release its lease into reconcile_wait'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'verifying' THEN
    SELECT * INTO result
    FROM release_delivery_operation_results
    WHERE operation_id = operation.id
      AND result_hash = operation.terminal_result_hash;
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'completed'
       OR NOT FOUND
       OR result.status <> 'completed'
       OR result.request_hash <> operation.request_hash
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR NEW.lease_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
      RAISE EXCEPTION 'verifying requires the exact completed controller Result under the live fence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'error' THEN
    SELECT * INTO result
    FROM release_delivery_operation_results
    WHERE operation_id = operation.id
      AND result_hash = operation.terminal_result_hash;
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'rejected'
       OR NOT FOUND
       OR result.status <> 'rejected'
       OR result.request_hash <> operation.request_hash
       OR result.worker_id IS DISTINCT FROM OLD.lease_worker_id
       OR result.fence_epoch IS DISTINCT FROM OLD.lease_epoch
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL
       OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'active-to-error requires the exact immutable rejected controller Result'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state IN ('submitting','reconciling') AND NEW.state = 'reconcile_blocked' THEN
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'quarantined'
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL
       OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.finished_at IS NOT NULL THEN
      RAISE EXCEPTION 'reconcile_blocked requires explicit quarantined operation evidence'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.state = 'cancelled' AND OLD.state IN ('queued','claimed') THEN
    IF operation.remote_state <> 'prepared'
       OR operation.submit_attempt_count <> 0
       OR (OLD.state = 'claimed' AND OLD.lease_expires_at <= statement_timestamp())
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL
       OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'cancellation is safe only before any submission Attempt'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.state = 'verifying'
     AND (
       (TG_TABLE_NAME = 'release_preview_runs' AND NEW.state IN ('passed','failed'))
       OR (TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state IN ('healthy','failed'))
     ) THEN
    SELECT * INTO result
    FROM release_delivery_operation_results
    WHERE operation_id = operation.id
      AND result_hash = operation.terminal_result_hash;
    IF OLD.lease_expires_at <= statement_timestamp()
       OR operation.remote_state <> 'completed'
       OR NOT FOUND
       OR result.status <> 'completed'
       OR NEW.fence_epoch <> OLD.fence_epoch
       OR NEW.lease_worker_id IS NOT NULL
       OR NEW.lease_epoch IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.finished_at IS NULL THEN
      RAISE EXCEPTION 'terminal delivery decision lost its exact completed operation fence'
        USING ERRCODE = '40001';
    END IF;

    IF TG_TABLE_NAME = 'release_preview_runs' AND NOT EXISTS (
      SELECT 1
      FROM release_preview_receipts AS receipt
      WHERE receipt.run_id = NEW.id
        AND receipt.project_id = NEW.project_id
        AND receipt.controller_operation_id = operation.id
        AND receipt.controller_result_hash = result.result_hash
        AND receipt.decision = NEW.state
        AND receipt.provider = result.provider
        AND receipt.provider_ref = result.provider_ref
        AND receipt.checks = result.checks
    ) THEN
      RAISE EXCEPTION 'terminal Preview Run requires its exact operation-backed PreviewReceipt'
        USING ERRCODE = '40001';
    END IF;

    IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'failed'
       AND NOT EXISTS (
         SELECT 1
         FROM release_production_receipts AS receipt
         WHERE receipt.run_id = NEW.id
           AND receipt.project_id = NEW.project_id
           AND receipt.controller_operation_id = operation.id
           AND receipt.controller_result_hash = result.result_hash
           AND receipt.decision = 'failed'
           AND receipt.provider = result.provider
           AND receipt.provider_ref = result.provider_ref
           AND NULLIF(receipt.public_url, '') IS NOT DISTINCT FROM result.public_url
           AND receipt.checks = result.checks
       ) THEN
      RAISE EXCEPTION 'failed production Run requires its exact operation-backed failed Receipt'
        USING ERRCODE = '40001';
    END IF;

    IF TG_TABLE_NAME = 'release_deployment_runs' AND NEW.state = 'healthy'
       AND NOT EXISTS (
         SELECT 1
         FROM release_production_receipts AS receipt
         JOIN release_deployment_revisions AS revision
           ON revision.production_receipt_id = receipt.id
          AND revision.production_receipt_hash = receipt.payload_hash
         WHERE receipt.run_id = NEW.id
           AND receipt.project_id = NEW.project_id
           AND receipt.controller_operation_id = operation.id
           AND receipt.controller_result_hash = result.result_hash
           AND receipt.decision = 'passed'
           AND receipt.provider = result.provider
           AND receipt.provider_ref = result.provider_ref
           AND NULLIF(receipt.public_url, '') IS NOT DISTINCT FROM result.public_url
           AND receipt.checks = result.checks
           AND revision.run_id = NEW.id
           AND revision.controller_operation_id = operation.id
           AND revision.controller_result_hash = result.result_hash
       ) THEN
      RAISE EXCEPTION 'healthy production Run requires exact operation-backed Receipt and Revision'
        USING ERRCODE = '40001';
    END IF;
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'invalid release delivery v2 state transition'
    USING ERRCODE = '40001';
END;
$$;

CREATE TRIGGER release_preview_run_update_guard
BEFORE UPDATE ON release_preview_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_update();

CREATE TRIGGER release_deployment_run_update_guard
BEFORE UPDATE ON release_deployment_runs
FOR EACH ROW EXECUTE FUNCTION validate_release_delivery_run_update();

CREATE INDEX release_preview_runs_claim_idx
  ON release_preview_runs (state, lease_expires_at, created_at, id)
  WHERE schema_version = 'release-preview-run/v2'
    AND state IN ('queued','claimed','submitting','reconcile_wait','reconciling','verifying');

CREATE INDEX release_deployment_runs_claim_idx
  ON release_deployment_runs (state, lease_expires_at, created_at, id)
  WHERE schema_version = 'release-deployment-run/v2'
    AND state IN ('queued','claimed','submitting','reconcile_wait','reconciling','verifying');

-- Unknown and blocked production operations keep the single-flight lock.  Only
-- an exact healthy/failed decision, explicit no-mutation rejection, or a safe
-- pre-submit cancellation releases the environment for another Run.
CREATE UNIQUE INDEX release_deployment_runs_one_nonterminal_environment_idx
  ON release_deployment_runs (project_id, environment)
  WHERE state IN (
    'queued','claimed','submitting','reconcile_wait','reconciling','verifying','reconcile_blocked'
  );

COMMENT ON TABLE release_delivery_operations IS
  'Stable controller idempotency identity for one exact v2 delivery Run; unknown submission outcomes must reconcile.';
COMMENT ON TABLE release_delivery_operation_attempts IS
  'Append-only, fence-bound evidence of every submit or reconcile call, including abandoned open calls.';
COMMENT ON TABLE release_delivery_operation_results IS
  'Immutable exact controller result pinned to request hash, controller trust digest, worker fence, and payload hash.';
