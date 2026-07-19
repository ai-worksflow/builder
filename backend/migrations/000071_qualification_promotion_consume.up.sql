-- Exact external-qualification promotion receipts are consumed once by a
-- dedicated operator. The consumption and its pending workflow handoff are
-- append-only and are inserted by one SECURITY DEFINER routine/transaction.
CREATE TABLE qualification_promotion_consumptions (
  operation_id uuid PRIMARY KEY,
  qualification_authority_id uuid NOT NULL,
  request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (octet_length(request_bytes) BETWEEN 1 AND 65536),
  request_document jsonb NOT NULL CHECK (jsonb_typeof(request_document) = 'object'),
  target_digest text NOT NULL CHECK (target_digest ~ '^sha256:[0-9a-f]{64}$'),
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  node_key text NOT NULL CHECK (
    octet_length(node_key) BETWEEN 1 AND 256
    AND node_key ~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$'
  ),
  target_revision_id uuid NOT NULL,
  target_revision_content_hash text NOT NULL CHECK (target_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  subject text NOT NULL CHECK (
    octet_length(subject) BETWEEN 1 AND 256
    AND subject = btrim(subject)
    AND subject !~ '[[:cntrl:]]'
  ),
  stage_gate text NOT NULL CHECK (stage_gate = 'external-qualification'),
  authority_nonce uuid NOT NULL UNIQUE,
  authority_expires_at timestamptz NOT NULL CHECK (authority_expires_at = date_trunc('milliseconds', authority_expires_at)),
  promotion_authority_digest text NOT NULL CHECK (promotion_authority_digest ~ '^sha256:[0-9a-f]{64}$'),
  scope text NOT NULL CHECK (scope = 'external-qualification-only'),
  single_use_consumption text NOT NULL CHECK (single_use_consumption = 'downstream-append-only-ledger-cas-required'),
  qualification_run_id uuid NOT NULL,
  plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
  artifact_index_digest text NOT NULL CHECK (artifact_index_digest ~ '^sha256:[0-9a-f]{64}$'),
  receipt_payload_digest text NOT NULL CHECK (receipt_payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  receipt_bundle_digest text NOT NULL CHECK (receipt_bundle_digest ~ '^sha256:[0-9a-f]{64}$'),
  golden_authority_artifact_id text NOT NULL,
  golden_authority_document_digest text NOT NULL CHECK (golden_authority_document_digest ~ '^sha256:[0-9a-f]{64}$'),
  golden_fault_operation_set_digest text NOT NULL CHECK (golden_fault_operation_set_digest ~ '^sha256:[0-9a-f]{64}$'),
  golden_fixture_artifact_id text NOT NULL,
  golden_fixture_document_digest text NOT NULL CHECK (golden_fixture_document_digest ~ '^sha256:[0-9a-f]{64}$'),
  golden_fixture_id uuid NOT NULL,
  credential_issuer text NOT NULL,
  credential_audience text NOT NULL,
  credential_set_handle_hash text NOT NULL CHECK (credential_set_handle_hash ~ '^sha256:[0-9a-f]{64}$'),
  credential_member_bindings_digest text NOT NULL CHECK (credential_member_bindings_digest ~ '^sha256:[0-9a-f]{64}$'),
  credential_member_count integer NOT NULL CHECK (credential_member_count BETWEEN 1 AND 64),
  credential_issuance_artifact_id text NOT NULL,
  credential_issuance_payload_digest text NOT NULL CHECK (credential_issuance_payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  credential_revocation_artifact_id text NOT NULL,
  credential_revocation_payload_digest text NOT NULL CHECK (credential_revocation_payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  signer_identities jsonb NOT NULL CHECK (jsonb_typeof(signer_identities) = 'array' AND jsonb_array_length(signer_identities) BETWEEN 1 AND 16),
  credential_issuance_signer_identities jsonb NOT NULL CHECK (jsonb_typeof(credential_issuance_signer_identities) = 'array' AND jsonb_array_length(credential_issuance_signer_identities) BETWEEN 1 AND 16),
  credential_revocation_signer_identities jsonb NOT NULL CHECK (jsonb_typeof(credential_revocation_signer_identities) = 'array' AND jsonb_array_length(credential_revocation_signer_identities) BETWEEN 1 AND 16),
  encryption_signer_identities jsonb NOT NULL CHECK (jsonb_typeof(encryption_signer_identities) = 'array' AND jsonb_array_length(encryption_signer_identities) BETWEEN 1 AND 16),
  fault_authority_signer_identities jsonb NOT NULL CHECK (jsonb_typeof(fault_authority_signer_identities) = 'array' AND jsonb_array_length(fault_authority_signer_identities) BETWEEN 1 AND 16),
  fault_ledger_attestation_digest text NOT NULL CHECK (fault_ledger_attestation_digest ~ '^sha256:[0-9a-f]{64}$'),
  fault_ledger_attestor_signer_identities jsonb NOT NULL CHECK (jsonb_typeof(fault_ledger_attestor_signer_identities) = 'array' AND jsonb_array_length(fault_ledger_attestor_signer_identities) BETWEEN 1 AND 16),
  receipt_issued_at timestamptz NOT NULL CHECK (receipt_issued_at = date_trunc('milliseconds', receipt_issued_at)),
  decision text NOT NULL CHECK (decision = 'qualified'),
  verified_promotion_hash text NOT NULL CHECK (verified_promotion_hash ~ '^sha256:[0-9a-f]{64}$'),
  verified_promotion_bytes bytea NOT NULL CHECK (octet_length(verified_promotion_bytes) BETWEEN 1 AND 1048576),
  verified_promotion_document jsonb NOT NULL CHECK (jsonb_typeof(verified_promotion_document) = 'object'),
  consumed_at timestamptz NOT NULL CHECK (consumed_at = date_trunc('milliseconds', consumed_at)),
  CONSTRAINT qualification_promotion_operation_v4 CHECK (
    operation_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_authority_reference_v4 CHECK (
    qualification_authority_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_project_v4 CHECK (
    project_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_workflow_run_v4 CHECK (
    workflow_run_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_target_revision_v4 CHECK (
    target_revision_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_nonce_v4 CHECK (
    authority_nonce::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_run_v4 CHECK (
    qualification_run_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_fixture_v4 CHECK (
    golden_fixture_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  ),
  CONSTRAINT qualification_promotion_receipt_digests_distinct CHECK (receipt_payload_digest <> receipt_bundle_digest),
  CONSTRAINT qualification_promotion_golden_documents_distinct CHECK (
    golden_authority_artifact_id <> golden_fixture_artifact_id
    AND golden_authority_document_digest <> golden_fixture_document_digest
  ),
  CONSTRAINT qualification_promotion_credential_receipts_distinct CHECK (
    credential_issuance_artifact_id <> credential_revocation_artifact_id
    AND credential_issuance_payload_digest <> credential_revocation_payload_digest
  )
);

CREATE TABLE qualification_promotion_handoffs (
  handoff_id uuid PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE REFERENCES qualification_promotion_consumptions(operation_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  state text NOT NULL CHECK (state = 'pending'),
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  node_key text NOT NULL,
  source_revision_id uuid NOT NULL,
  source_revision_content_hash text NOT NULL CHECK (source_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'),
  subject text NOT NULL,
  stage_gate text NOT NULL CHECK (stage_gate = 'external-qualification'),
  output_revision_id uuid NOT NULL UNIQUE,
  revision_kind text NOT NULL CHECK (revision_kind = 'external-qualification-promotion-only/v1'),
  revision_intent_digest text NOT NULL CHECK (revision_intent_digest ~ '^sha256:[0-9a-f]{64}$'),
  revision_intent_bytes bytea NOT NULL CHECK (octet_length(revision_intent_bytes) BETWEEN 1 AND 65536),
  revision_intent_document jsonb NOT NULL CHECK (jsonb_typeof(revision_intent_document) = 'object'),
  authority_nonce uuid NOT NULL,
  promotion_authority_digest text NOT NULL CHECK (promotion_authority_digest ~ '^sha256:[0-9a-f]{64}$'),
  verified_promotion_hash text NOT NULL CHECK (verified_promotion_hash ~ '^sha256:[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL CHECK (created_at = date_trunc('milliseconds', created_at)),
  CONSTRAINT qualification_promotion_handoff_v4 CHECK (
    handoff_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    AND output_revision_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
  )
);

CREATE INDEX qualification_promotion_handoffs_pending_idx
  ON qualification_promotion_handoffs(created_at, handoff_id) WHERE state = 'pending';
CREATE INDEX qualification_promotion_consumptions_target_idx
  ON qualification_promotion_consumptions(project_id, workflow_run_id, node_key, target_revision_id, consumed_at);

CREATE FUNCTION reject_qualification_promotion_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog
AS $function$
BEGIN
  RAISE EXCEPTION 'qualification promotion ledger and handoff are append-only'
    USING ERRCODE = '55000';
END;
$function$;

CREATE TRIGGER qualification_promotion_consumptions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_promotion_consumptions
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_mutation();

CREATE TRIGGER qualification_promotion_handoffs_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_promotion_handoffs
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_mutation();

CREATE FUNCTION consume_verified_qualification_promotion(
  p_operation_id uuid,
  p_request_hash text,
  p_request_bytes bytea,
  p_request_document jsonb,
  p_target_digest text,
  p_verified_promotion_hash text,
  p_verified_promotion_bytes bytea,
  p_verified_promotion_document jsonb,
  p_handoff_id uuid,
  p_output_revision_id uuid,
  p_revision_intent_digest text,
  p_revision_intent_bytes bytea,
  p_revision_intent_document jsonb
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path FROM CURRENT
AS $function$
DECLARE
  v_existing qualification_promotion_consumptions%ROWTYPE;
  v_existing_handoff qualification_promotion_handoffs%ROWTYPE;
  v_target jsonb;
  v_golden jsonb;
  v_credential jsonb;
  v_now timestamptz;
  v_request_actual_hash text;
  v_verified_actual_hash text;
  v_intent_actual_hash text;
  v_crypto_schema text;
BEGIN
  IF p_operation_id IS NULL OR p_operation_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_handoff_id IS NULL OR p_handoff_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_output_revision_id IS NULL OR p_output_revision_id::text !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR p_request_hash IS NULL OR p_request_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_target_digest IS NULL OR p_target_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_verified_promotion_hash IS NULL OR p_verified_promotion_hash !~ '^sha256:[0-9a-f]{64}$'
     OR p_revision_intent_digest IS NULL OR p_revision_intent_digest !~ '^sha256:[0-9a-f]{64}$'
     OR p_request_bytes IS NULL OR octet_length(p_request_bytes) NOT BETWEEN 1 AND 65536
     OR p_verified_promotion_bytes IS NULL OR octet_length(p_verified_promotion_bytes) NOT BETWEEN 1 AND 1048576
     OR p_revision_intent_bytes IS NULL OR octet_length(p_revision_intent_bytes) NOT BETWEEN 1 AND 65536
     OR jsonb_typeof(p_request_document) <> 'object'
     OR jsonb_typeof(p_verified_promotion_document) <> 'object'
     OR jsonb_typeof(p_revision_intent_document) <> 'object' THEN
    RAISE EXCEPTION 'qualification promotion consume request is structurally invalid'
      USING ERRCODE = '40001';
  END IF;

  SELECT namespace.nspname INTO v_crypto_schema
  FROM pg_catalog.pg_extension AS extension
  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = extension.extnamespace
  WHERE extension.extname = 'pgcrypto';
  IF v_crypto_schema IS NULL THEN
    RAISE EXCEPTION 'pgcrypto is required for qualification promotion byte closure'
      USING ERRCODE = '55000';
  END IF;
  EXECUTE pg_catalog.format(
    'SELECT ''sha256:'' || pg_catalog.encode(%I.digest($1, ''sha256''), ''hex'')',
    v_crypto_schema
  ) INTO v_request_actual_hash USING p_request_bytes;
  EXECUTE pg_catalog.format(
    'SELECT ''sha256:'' || pg_catalog.encode(%I.digest($1, ''sha256''), ''hex'')',
    v_crypto_schema
  ) INTO v_verified_actual_hash USING p_verified_promotion_bytes;
  EXECUTE pg_catalog.format(
    'SELECT ''sha256:'' || pg_catalog.encode(%I.digest($1, ''sha256''), ''hex'')',
    v_crypto_schema
  ) INTO v_intent_actual_hash USING p_revision_intent_bytes;

  IF v_request_actual_hash <> p_request_hash
     OR v_verified_actual_hash <> p_verified_promotion_hash
     OR v_intent_actual_hash <> p_revision_intent_digest
     OR pg_catalog.convert_from(p_request_bytes, 'UTF8')::jsonb <> p_request_document
     OR pg_catalog.convert_from(p_verified_promotion_bytes, 'UTF8')::jsonb <> p_verified_promotion_document
     OR pg_catalog.convert_from(p_revision_intent_bytes, 'UTF8')::jsonb <> p_revision_intent_document
     OR NOT (p_request_document ?& ARRAY[
       'handoffId','operationId','outputRevisionId','qualificationAuthorityId','revisionIntentDigest',
       'schemaVersion','targetDigest','verifiedPromotionHash'
     ])
     OR p_request_document - ARRAY[
       'handoffId','operationId','outputRevisionId','qualificationAuthorityId','revisionIntentDigest',
       'schemaVersion','targetDigest','verifiedPromotionHash'
     ] <> '{}'::jsonb
     OR p_request_document->>'schemaVersion' <> 'worksflow-qualification-promotion-consume-request/v1'
     OR p_request_document->>'operationId' <> p_operation_id::text
     OR p_request_document->>'qualificationAuthorityId' IS NULL
     OR p_request_document->>'handoffId' <> p_handoff_id::text
     OR p_request_document->>'outputRevisionId' <> p_output_revision_id::text
     OR p_request_document->>'revisionIntentDigest' <> p_revision_intent_digest
     OR p_request_document->>'targetDigest' <> p_target_digest
     OR p_request_document->>'verifiedPromotionHash' <> p_verified_promotion_hash THEN
    RAISE EXCEPTION 'qualification promotion canonical request bytes are not exact'
      USING ERRCODE = '40001';
  END IF;

  IF NOT (p_verified_promotion_document ?& ARRAY[
       'artifactIndexDigest','authorityExpiresAt','authorityNonce','credentialIssuanceSignerIdentities',
       'credentialRevocationSignerIdentities','credentialSet','decision','encryptionSignerIdentities',
       'faultAuthoritySignerIdentities','faultLedgerAttestationDigest','faultLedgerAttestorSignerIdentities',
       'goldenRuntime','issuedAt','planDigest','promotionAuthorityDigest','promotionTarget',
       'receiptBundleDigest','receiptPayloadDigest','runId','scope','signerIdentities','singleUseConsumption'
     ])
     OR p_verified_promotion_document - ARRAY[
       'artifactIndexDigest','authorityExpiresAt','authorityNonce','credentialIssuanceSignerIdentities',
       'credentialRevocationSignerIdentities','credentialSet','decision','encryptionSignerIdentities',
       'faultAuthoritySignerIdentities','faultLedgerAttestationDigest','faultLedgerAttestorSignerIdentities',
       'goldenRuntime','issuedAt','planDigest','promotionAuthorityDigest','promotionTarget',
       'receiptBundleDigest','receiptPayloadDigest','runId','scope','signerIdentities','singleUseConsumption'
     ] <> '{}'::jsonb
     OR p_verified_promotion_document->>'scope' <> 'external-qualification-only'
     OR p_verified_promotion_document->>'singleUseConsumption' <> 'downstream-append-only-ledger-cas-required'
     OR p_verified_promotion_document->>'decision' <> 'qualified' THEN
    RAISE EXCEPTION 'verified qualification projection has an open or invalid top-level shape'
      USING ERRCODE = '40001';
  END IF;

  v_target := p_verified_promotion_document->'promotionTarget';
  v_golden := p_verified_promotion_document->'goldenRuntime';
  v_credential := p_verified_promotion_document->'credentialSet';
  IF jsonb_typeof(v_target) <> 'object'
     OR NOT (v_target ?& ARRAY['nodeKey','projectId','stageGate','subject','targetRevision','workflowRunId'])
     OR v_target - ARRAY['nodeKey','projectId','stageGate','subject','targetRevision','workflowRunId'] <> '{}'::jsonb
     OR jsonb_typeof(v_target->'targetRevision') <> 'object'
     OR NOT ((v_target->'targetRevision') ?& ARRAY['contentHash','id'])
     OR (v_target->'targetRevision') - ARRAY['contentHash','id'] <> '{}'::jsonb
     OR v_target->>'stageGate' <> 'external-qualification'
     OR jsonb_typeof(v_golden) <> 'object'
     OR NOT (v_golden ?& ARRAY[
       'authorityDocumentArtifactId','authorityDocumentDigest','faultOperationSetDigest',
       'fixtureDocumentArtifactId','fixtureDocumentDigest','fixtureId'
     ])
     OR v_golden - ARRAY[
       'authorityDocumentArtifactId','authorityDocumentDigest','faultOperationSetDigest',
       'fixtureDocumentArtifactId','fixtureDocumentDigest','fixtureId'
     ] <> '{}'::jsonb
     OR jsonb_typeof(v_credential) <> 'object'
     OR NOT (v_credential ?& ARRAY[
       'audience','issuanceArtifactId','issuancePayloadDigest','issuer','memberBindingsDigest',
       'memberCount','revocationArtifactId','revocationPayloadDigest','setHandleHash'
     ])
     OR v_credential - ARRAY[
       'audience','issuanceArtifactId','issuancePayloadDigest','issuer','memberBindingsDigest',
       'memberCount','revocationArtifactId','revocationPayloadDigest','setHandleHash'
     ] <> '{}'::jsonb
     OR jsonb_typeof(p_verified_promotion_document->'signerIdentities') <> 'array'
     OR jsonb_typeof(p_verified_promotion_document->'credentialIssuanceSignerIdentities') <> 'array'
     OR jsonb_typeof(p_verified_promotion_document->'credentialRevocationSignerIdentities') <> 'array'
     OR jsonb_typeof(p_verified_promotion_document->'encryptionSignerIdentities') <> 'array'
     OR jsonb_typeof(p_verified_promotion_document->'faultAuthoritySignerIdentities') <> 'array'
     OR jsonb_typeof(p_verified_promotion_document->'faultLedgerAttestorSignerIdentities') <> 'array' THEN
    RAISE EXCEPTION 'verified qualification target/root/evidence shape is not closed'
      USING ERRCODE = '40001';
  END IF;

  IF NOT (p_revision_intent_document ?& ARRAY[
       'authorityNonce','handoffId','outputRevisionId','promotionAuthorityDigest',
       'revisionKind','schemaVersion','sourceTarget','verifiedPromotionHash'
     ])
     OR p_revision_intent_document - ARRAY[
       'authorityNonce','handoffId','outputRevisionId','promotionAuthorityDigest',
       'revisionKind','schemaVersion','sourceTarget','verifiedPromotionHash'
     ] <> '{}'::jsonb
     OR p_revision_intent_document->>'schemaVersion' <> 'worksflow-qualification-promotion-revision-intent/v1'
     OR p_revision_intent_document->>'revisionKind' <> 'external-qualification-promotion-only/v1'
     OR p_revision_intent_document->>'handoffId' <> p_handoff_id::text
     OR p_revision_intent_document->>'outputRevisionId' <> p_output_revision_id::text
     OR p_revision_intent_document->>'authorityNonce' <> p_verified_promotion_document->>'authorityNonce'
     OR p_revision_intent_document->>'promotionAuthorityDigest' <> p_verified_promotion_document->>'promotionAuthorityDigest'
     OR p_revision_intent_document->>'verifiedPromotionHash' <> p_verified_promotion_hash
     OR p_revision_intent_document->'sourceTarget' <> v_target THEN
    RAISE EXCEPTION 'promotion-only revision intent does not bind exact verified authority and target'
      USING ERRCODE = '40001';
  END IF;

  -- This stable global lock is intentionally conservative: it serializes nonce
  -- inspection and the post-wait trusted clock without deadlock-prone lock order.
  LOCK TABLE qualification_promotion_consumptions IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE qualification_promotion_handoffs IN SHARE ROW EXCLUSIVE MODE;

  SELECT * INTO v_existing
  FROM qualification_promotion_consumptions
  WHERE operation_id = p_operation_id;
  IF FOUND THEN
    SELECT * INTO v_existing_handoff
    FROM qualification_promotion_handoffs
    WHERE operation_id = p_operation_id;
    IF NOT FOUND
       OR v_existing.request_hash <> p_request_hash
       OR v_existing.request_bytes <> p_request_bytes
       OR v_existing.request_document <> p_request_document
       OR v_existing.target_digest <> p_target_digest
       OR v_existing.verified_promotion_hash <> p_verified_promotion_hash
       OR v_existing.verified_promotion_bytes <> p_verified_promotion_bytes
       OR v_existing.verified_promotion_document <> p_verified_promotion_document
       OR v_existing_handoff.handoff_id <> p_handoff_id
       OR v_existing_handoff.output_revision_id <> p_output_revision_id
       OR v_existing_handoff.revision_intent_digest <> p_revision_intent_digest
       OR v_existing_handoff.revision_intent_bytes <> p_revision_intent_bytes
       OR v_existing_handoff.revision_intent_document <> p_revision_intent_document THEN
      RAISE EXCEPTION 'qualification promotion operation ID is bound to different bytes'
        USING ERRCODE = '40001';
    END IF;
    RETURN true;
  END IF;

  IF EXISTS (
    SELECT 1 FROM qualification_promotion_consumptions
    WHERE authority_nonce = (p_verified_promotion_document->>'authorityNonce')::uuid
  ) THEN
    RAISE EXCEPTION 'qualification promotion authority nonce was already consumed for another operation, target, or digest'
      USING ERRCODE = '40001';
  END IF;

  v_now := date_trunc('milliseconds', clock_timestamp());
  IF (p_verified_promotion_document->>'authorityExpiresAt')::timestamptz <= v_now
     OR (p_verified_promotion_document->>'issuedAt')::timestamptz >= (p_verified_promotion_document->>'authorityExpiresAt')::timestamptz
     OR (p_verified_promotion_document->>'issuedAt')::timestamptz > v_now + interval '30 seconds' THEN
    RAISE EXCEPTION 'qualification promotion authority is expired or outside the trusted post-lock time fence'
      USING ERRCODE = '40001';
  END IF;

  INSERT INTO qualification_promotion_consumptions (
    operation_id, qualification_authority_id, request_hash, request_bytes, request_document, target_digest,
    project_id, workflow_run_id, node_key, target_revision_id, target_revision_content_hash,
    subject, stage_gate, authority_nonce, authority_expires_at, promotion_authority_digest,
    scope, single_use_consumption, qualification_run_id, plan_digest, artifact_index_digest,
    receipt_payload_digest, receipt_bundle_digest,
    golden_authority_artifact_id, golden_authority_document_digest, golden_fault_operation_set_digest,
    golden_fixture_artifact_id, golden_fixture_document_digest, golden_fixture_id,
    credential_issuer, credential_audience, credential_set_handle_hash, credential_member_bindings_digest,
    credential_member_count, credential_issuance_artifact_id, credential_issuance_payload_digest,
    credential_revocation_artifact_id, credential_revocation_payload_digest,
    signer_identities, credential_issuance_signer_identities, credential_revocation_signer_identities,
    encryption_signer_identities, fault_authority_signer_identities, fault_ledger_attestation_digest,
    fault_ledger_attestor_signer_identities, receipt_issued_at, decision,
    verified_promotion_hash, verified_promotion_bytes, verified_promotion_document, consumed_at
  ) VALUES (
    p_operation_id, (p_request_document->>'qualificationAuthorityId')::uuid,
    p_request_hash, p_request_bytes, p_request_document, p_target_digest,
    (v_target->>'projectId')::uuid, (v_target->>'workflowRunId')::uuid, v_target->>'nodeKey',
    (v_target->'targetRevision'->>'id')::uuid, v_target->'targetRevision'->>'contentHash',
    v_target->>'subject', v_target->>'stageGate', (p_verified_promotion_document->>'authorityNonce')::uuid,
    (p_verified_promotion_document->>'authorityExpiresAt')::timestamptz,
    p_verified_promotion_document->>'promotionAuthorityDigest', p_verified_promotion_document->>'scope',
    p_verified_promotion_document->>'singleUseConsumption', (p_verified_promotion_document->>'runId')::uuid,
    p_verified_promotion_document->>'planDigest', p_verified_promotion_document->>'artifactIndexDigest',
    p_verified_promotion_document->>'receiptPayloadDigest', p_verified_promotion_document->>'receiptBundleDigest',
    v_golden->>'authorityDocumentArtifactId', v_golden->>'authorityDocumentDigest', v_golden->>'faultOperationSetDigest',
    v_golden->>'fixtureDocumentArtifactId', v_golden->>'fixtureDocumentDigest', (v_golden->>'fixtureId')::uuid,
    v_credential->>'issuer', v_credential->>'audience', v_credential->>'setHandleHash',
    v_credential->>'memberBindingsDigest', (v_credential->>'memberCount')::integer,
    v_credential->>'issuanceArtifactId', v_credential->>'issuancePayloadDigest',
    v_credential->>'revocationArtifactId', v_credential->>'revocationPayloadDigest',
    p_verified_promotion_document->'signerIdentities',
    p_verified_promotion_document->'credentialIssuanceSignerIdentities',
    p_verified_promotion_document->'credentialRevocationSignerIdentities',
    p_verified_promotion_document->'encryptionSignerIdentities',
    p_verified_promotion_document->'faultAuthoritySignerIdentities',
    p_verified_promotion_document->>'faultLedgerAttestationDigest',
    p_verified_promotion_document->'faultLedgerAttestorSignerIdentities',
    (p_verified_promotion_document->>'issuedAt')::timestamptz,
    p_verified_promotion_document->>'decision', p_verified_promotion_hash,
    p_verified_promotion_bytes, p_verified_promotion_document, v_now
  );

  INSERT INTO qualification_promotion_handoffs (
    handoff_id, operation_id, state, project_id, workflow_run_id, node_key,
    source_revision_id, source_revision_content_hash, subject, stage_gate,
    output_revision_id, revision_kind, revision_intent_digest, revision_intent_bytes,
    revision_intent_document, authority_nonce, promotion_authority_digest,
    verified_promotion_hash, created_at
  ) VALUES (
    p_handoff_id, p_operation_id, 'pending', (v_target->>'projectId')::uuid,
    (v_target->>'workflowRunId')::uuid, v_target->>'nodeKey',
    (v_target->'targetRevision'->>'id')::uuid, v_target->'targetRevision'->>'contentHash',
    v_target->>'subject', v_target->>'stageGate', p_output_revision_id,
    'external-qualification-promotion-only/v1', p_revision_intent_digest,
    p_revision_intent_bytes, p_revision_intent_document,
    (p_verified_promotion_document->>'authorityNonce')::uuid,
    p_verified_promotion_document->>'promotionAuthorityDigest', p_verified_promotion_hash, v_now
  );

  RETURN false;
EXCEPTION
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation
    OR invalid_text_representation OR serialization_failure OR deadlock_detected
    OR numeric_value_out_of_range OR datetime_field_overflow THEN
    RAISE EXCEPTION 'qualification promotion atomic consume conflict: %', SQLERRM
      USING ERRCODE = '40001';
END;
$function$;

COMMENT ON TABLE qualification_promotion_consumptions IS
  'Append-only exact VerifiedPromotion consumption ledger; each authority nonce is globally single-use.';
COMMENT ON TABLE qualification_promotion_handoffs IS
  'Append-only pending operator handoff for a preallocated promotion-only immutable revision; not proof of workflow submission.';
COMMENT ON FUNCTION consume_verified_qualification_promotion(
  uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb
) IS 'Dedicated-operator atomic external qualification consumption plus pending immutable-revision handoff.';

REVOKE ALL ON TABLE qualification_promotion_consumptions FROM PUBLIC;
REVOKE ALL ON TABLE qualification_promotion_handoffs FROM PUBLIC;
REVOKE ALL ON FUNCTION reject_qualification_promotion_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION consume_verified_qualification_promotion(
  uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb
) FROM PUBLIC;

DO $qualification_promotion_ownership_and_acl$
DECLARE
  schema_name text := current_schema();
BEGIN
  EXECUTE format(
    'ALTER FUNCTION %I.consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb) '
    'SET search_path TO pg_catalog, %I, pg_temp',
    schema_name, schema_name
  );
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format('ALTER TABLE %I.qualification_promotion_consumptions OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER TABLE %I.qualification_promotion_handoffs OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format('ALTER FUNCTION %I.reject_qualification_promotion_mutation() OWNER TO worksflow_migration_owner', schema_name);
    EXECUTE format(
      'ALTER FUNCTION %I.consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb) OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_qualification_promotion_operator') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO worksflow_qualification_promotion_operator', schema_name);
    EXECUTE format('GRANT SELECT ON TABLE %I.qualification_promotion_consumptions TO worksflow_qualification_promotion_operator', schema_name);
    EXECUTE format('GRANT SELECT ON TABLE %I.qualification_promotion_handoffs TO worksflow_qualification_promotion_operator', schema_name);
    EXECUTE format(
      'GRANT EXECUTE ON FUNCTION %I.consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb) TO worksflow_qualification_promotion_operator',
      schema_name
    );
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application') THEN
    EXECUTE format('REVOKE ALL ON TABLE %I.qualification_promotion_consumptions FROM worksflow_application', schema_name);
    EXECUTE format('REVOKE ALL ON TABLE %I.qualification_promotion_handoffs FROM worksflow_application', schema_name);
    EXECUTE format(
      'REVOKE ALL ON FUNCTION %I.consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb) FROM worksflow_application',
      schema_name
    );
  END IF;
END;
$qualification_promotion_ownership_and_acl$;
