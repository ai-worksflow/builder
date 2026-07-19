-- One-shot Golden fault consumption is represented by two immutable facts:
-- an exact resource/fence reservation written before adapter execution, and an
-- optional terminal receipt written afterwards. There is deliberately no
-- UPDATE transition and no caller-controlled command payload.
CREATE TABLE golden_fault_consume_reservations (
  authority_id uuid PRIMARY KEY,
  fixture_id uuid NOT NULL,
  run_id uuid NOT NULL,
  operation_kind text NOT NULL CHECK (operation_kind IN (
    'agent-runner-crash',
    'agent-runner-timeout',
    'agent-security-canary',
    'controller-conflict',
    'controller-maintenance',
    'controller-not-found',
    'controller-timeout',
    'lsp-resource-pressure',
    'lsp-runtime-crash',
    'lsp-runtime-drift',
    'reference-gateway-outage',
    'reference-process-restart',
    'sandbox-dependency-crash'
  )),
  resource_selector text NOT NULL,
  expected_fence_digest text NOT NULL CHECK (expected_fence_digest ~ '^sha256:[0-9a-f]{64}$'),
  envelope_digest text NOT NULL CHECK (envelope_digest ~ '^sha256:[0-9a-f]{64}$'),
  payload_digest text NOT NULL CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
  predicate_digest text NOT NULL CHECK (predicate_digest ~ '^sha256:[0-9a-f]{64}$'),
  authority_issued_at timestamptz NOT NULL,
  authority_expires_at timestamptz NOT NULL,
  signer_identities jsonb NOT NULL CHECK (
    jsonb_typeof(signer_identities) = 'array'
    AND jsonb_array_length(signer_identities) BETWEEN 1 AND 8
  ),
  resolved_resource_id text NOT NULL CHECK (
    octet_length(resolved_resource_id) BETWEEN 1 AND 512
    AND resolved_resource_id ~ '^[A-Za-z0-9][A-Za-z0-9._:/-]*$'
  ),
  resolved_head_digest text NOT NULL CHECK (resolved_head_digest ~ '^sha256:[0-9a-f]{64}$'),
  resolved_fence_digest text NOT NULL CHECK (resolved_fence_digest ~ '^sha256:[0-9a-f]{64}$'),
  resolution_digest text NOT NULL CHECK (resolution_digest ~ '^sha256:[0-9a-f]{64}$'),
  adapter_invocation_id uuid NOT NULL UNIQUE,
  reserved_at timestamptz NOT NULL,
  CONSTRAINT golden_fault_authority_digest_distinct CHECK (envelope_digest <> payload_digest),
  CONSTRAINT golden_fault_authority_lifetime CHECK (
    authority_expires_at > authority_issued_at
    AND authority_expires_at <= authority_issued_at + interval '30 minutes'
    AND reserved_at >= authority_issued_at - interval '30 seconds'
    AND reserved_at < authority_expires_at
  ),
  CONSTRAINT golden_fault_expected_fence_exact CHECK (
    resolved_fence_digest = expected_fence_digest
  ),
  CONSTRAINT golden_fault_operation_selector_exact CHECK (
    resource_selector = CASE operation_kind
      WHEN 'agent-runner-crash' THEN 'agent.runner'
      WHEN 'agent-runner-timeout' THEN 'agent.runner'
      WHEN 'agent-security-canary' THEN 'agent.patch-policy'
      WHEN 'controller-conflict' THEN 'release.controller'
      WHEN 'controller-maintenance' THEN 'release.controller'
      WHEN 'controller-not-found' THEN 'release.controller'
      WHEN 'controller-timeout' THEN 'release.controller'
      WHEN 'lsp-resource-pressure' THEN 'lsp.runtime'
      WHEN 'lsp-runtime-crash' THEN 'lsp.runtime'
      WHEN 'lsp-runtime-drift' THEN 'lsp.runtime'
      WHEN 'reference-gateway-outage' THEN 'reference.gateway'
      WHEN 'reference-process-restart' THEN 'reference.process'
      WHEN 'sandbox-dependency-crash' THEN 'sandbox.dependency'
    END
  ),
  CONSTRAINT golden_fault_fixture_operation_unique UNIQUE (fixture_id, run_id, operation_kind)
);

CREATE TABLE golden_fault_consume_results (
  authority_id uuid PRIMARY KEY REFERENCES golden_fault_consume_reservations(authority_id) ON DELETE RESTRICT,
  result_id uuid NOT NULL UNIQUE,
  outcome text NOT NULL CHECK (outcome IN ('applied', 'refused')),
  adapter_result_digest text NOT NULL CHECK (adapter_result_digest ~ '^sha256:[0-9a-f]{64}$'),
  observed_head_digest text NOT NULL CHECK (observed_head_digest ~ '^sha256:[0-9a-f]{64}$'),
  observed_fence_digest text NOT NULL CHECK (observed_fence_digest ~ '^sha256:[0-9a-f]{64}$'),
  completed_at timestamptz NOT NULL,
  receipt_bytes bytea NOT NULL,
  receipt_document jsonb NOT NULL,
  receipt_digest text NOT NULL CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
  CONSTRAINT golden_fault_terminal_after_reservation CHECK (octet_length(receipt_bytes) BETWEEN 1 AND 65536),
  CONSTRAINT golden_fault_receipt_exact_shape CHECK (
    jsonb_typeof(receipt_document) = 'object'
    AND receipt_document ?& ARRAY[
      'adapterInvocationId', 'adapterResultDigest', 'authorityId', 'completedAt',
      'envelopeDigest', 'expectedFenceDigest', 'fixtureId', 'observedFenceDigest',
      'observedHeadDigest', 'operationKind', 'outcome', 'payloadDigest',
      'predicateDigest', 'reservedAt', 'resolutionDigest', 'resolvedFenceDigest',
      'resolvedHeadDigest', 'resolvedResourceId', 'resourceSelector', 'resultId',
      'runId', 'schemaVersion'
    ]
    AND receipt_document - ARRAY[
      'adapterInvocationId', 'adapterResultDigest', 'authorityId', 'completedAt',
      'envelopeDigest', 'expectedFenceDigest', 'fixtureId', 'observedFenceDigest',
      'observedHeadDigest', 'operationKind', 'outcome', 'payloadDigest',
      'predicateDigest', 'reservedAt', 'resolutionDigest', 'resolvedFenceDigest',
      'resolvedHeadDigest', 'resolvedResourceId', 'resourceSelector', 'resultId',
      'runId', 'schemaVersion'
    ] = '{}'::jsonb
    AND receipt_document->>'schemaVersion' = 'worksflow-golden-fault-consume-receipt/v1'
    AND receipt_document->>'authorityId' = authority_id::text
    AND receipt_document->>'resultId' = result_id::text
    AND receipt_document->>'outcome' = outcome
    AND receipt_document->>'adapterResultDigest' = adapter_result_digest
    AND receipt_document->>'observedHeadDigest' = observed_head_digest
    AND receipt_document->>'observedFenceDigest' = observed_fence_digest
    AND receipt_document->>'completedAt' = to_char(
      completed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    )
  )
);

CREATE INDEX golden_fault_consume_reservations_run_idx
  ON golden_fault_consume_reservations(run_id, fixture_id, reserved_at, authority_id);

CREATE FUNCTION validate_golden_fault_consume_result()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog
AS $function$
DECLARE
  schema_name text := TG_TABLE_SCHEMA;
  reservation_fixture_id uuid;
  reservation_run_id uuid;
  reservation_operation_kind text;
  reservation_resource_selector text;
  reservation_expected_fence_digest text;
  reservation_envelope_digest text;
  reservation_payload_digest text;
  reservation_predicate_digest text;
  reservation_resolved_resource_id text;
  reservation_resolved_head_digest text;
  reservation_resolved_fence_digest text;
  reservation_resolution_digest text;
  reservation_adapter_invocation_id uuid;
  reservation_reserved_at timestamptz;
  reservation_found boolean;
  reservation_row_count bigint;
  crypto_schema text;
  actual_receipt_digest text;
BEGIN
  EXECUTE format(
    'SELECT fixture_id, run_id, operation_kind, resource_selector, '
    'expected_fence_digest, envelope_digest, payload_digest, predicate_digest, '
    'resolved_resource_id, resolved_head_digest, resolved_fence_digest, '
    'resolution_digest, adapter_invocation_id, reserved_at '
    'FROM %I.golden_fault_consume_reservations WHERE authority_id = $1',
    schema_name
  )
  INTO
    reservation_fixture_id,
    reservation_run_id,
    reservation_operation_kind,
    reservation_resource_selector,
    reservation_expected_fence_digest,
    reservation_envelope_digest,
    reservation_payload_digest,
    reservation_predicate_digest,
    reservation_resolved_resource_id,
    reservation_resolved_head_digest,
    reservation_resolved_fence_digest,
    reservation_resolution_digest,
    reservation_adapter_invocation_id,
    reservation_reserved_at
  USING NEW.authority_id;
  GET DIAGNOSTICS reservation_row_count = ROW_COUNT;
  reservation_found := reservation_row_count = 1;

  SELECT namespace.nspname
    INTO crypto_schema
  FROM pg_catalog.pg_extension AS extension
  JOIN pg_catalog.pg_namespace AS namespace
    ON namespace.oid = extension.extnamespace
  WHERE extension.extname = 'pgcrypto';
  IF crypto_schema IS NULL THEN
    RAISE EXCEPTION 'pgcrypto is required to verify Golden fault receipt bytes'
      USING ERRCODE = '55000';
  END IF;
  EXECUTE format(
    'SELECT ''sha256:'' || encode(%I.digest($1, ''sha256''), ''hex'')',
    crypto_schema
  ) INTO actual_receipt_digest USING NEW.receipt_bytes;

  IF NOT reservation_found
     OR NEW.completed_at < reservation_reserved_at
     OR NEW.receipt_digest <> actual_receipt_digest
     OR convert_from(NEW.receipt_bytes, 'UTF8')::jsonb <> NEW.receipt_document
     OR NEW.receipt_document->>'fixtureId' <> reservation_fixture_id::text
     OR NEW.receipt_document->>'runId' <> reservation_run_id::text
     OR NEW.receipt_document->>'operationKind' <> reservation_operation_kind
     OR NEW.receipt_document->>'resourceSelector' <> reservation_resource_selector
     OR NEW.receipt_document->>'expectedFenceDigest' <> reservation_expected_fence_digest
     OR NEW.receipt_document->>'envelopeDigest' <> reservation_envelope_digest
     OR NEW.receipt_document->>'payloadDigest' <> reservation_payload_digest
     OR NEW.receipt_document->>'predicateDigest' <> reservation_predicate_digest
     OR NEW.receipt_document->>'resolvedResourceId' <> reservation_resolved_resource_id
     OR NEW.receipt_document->>'resolvedHeadDigest' <> reservation_resolved_head_digest
     OR NEW.receipt_document->>'resolvedFenceDigest' <> reservation_resolved_fence_digest
     OR NEW.receipt_document->>'resolutionDigest' <> reservation_resolution_digest
     OR NEW.receipt_document->>'adapterInvocationId' <> reservation_adapter_invocation_id::text
     OR NEW.receipt_document->>'reservedAt' <> to_char(
       reservation_reserved_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
     ) THEN
    RAISE EXCEPTION 'Golden fault terminal result does not bind its exact reservation and receipt bytes'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE FUNCTION reject_golden_fault_ledger_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog
AS $function$
BEGIN
  RAISE EXCEPTION 'Golden fault consume ledger is append-only'
    USING ERRCODE = '55000';
END;
$function$;

CREATE TRIGGER golden_fault_results_validate
BEFORE INSERT ON golden_fault_consume_results
FOR EACH ROW EXECUTE FUNCTION validate_golden_fault_consume_result();

CREATE TRIGGER golden_fault_reservations_append_only
BEFORE UPDATE OR DELETE ON golden_fault_consume_reservations
FOR EACH ROW EXECUTE FUNCTION reject_golden_fault_ledger_mutation();

CREATE TRIGGER golden_fault_results_append_only
BEFORE UPDATE OR DELETE ON golden_fault_consume_results
FOR EACH ROW EXECUTE FUNCTION reject_golden_fault_ledger_mutation();

COMMENT ON TABLE golden_fault_consume_reservations IS
  'Append-only CAS reservations binding one signed Golden fault authority to one dynamically resolved resource/head/fence and adapter invocation.';
COMMENT ON TABLE golden_fault_consume_results IS
  'Append-only terminal receipts for Golden fault reservations; a missing row is an explicitly queryable unknown outcome and never authorizes retry.';

REVOKE ALL ON TABLE golden_fault_consume_reservations FROM PUBLIC;
REVOKE ALL ON TABLE golden_fault_consume_results FROM PUBLIC;
REVOKE ALL ON FUNCTION validate_golden_fault_consume_result() FROM PUBLIC;
REVOKE ALL ON FUNCTION reject_golden_fault_ledger_mutation() FROM PUBLIC;

DO $ownership_and_acl$
DECLARE
  schema_name text := current_schema();
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
    EXECUTE format(
      'ALTER TABLE %I.golden_fault_consume_reservations OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER TABLE %I.golden_fault_consume_results OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.reject_golden_fault_ledger_mutation() OWNER TO worksflow_migration_owner',
      schema_name
    );
    EXECUTE format(
      'ALTER FUNCTION %I.validate_golden_fault_consume_result() OWNER TO worksflow_migration_owner',
      schema_name
    );
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_golden_fault_operator') THEN
    EXECUTE format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_golden_fault_operator',
      schema_name
    );
    EXECUTE format(
      'GRANT SELECT, INSERT ON TABLE %I.golden_fault_consume_reservations TO worksflow_golden_fault_operator',
      schema_name
    );
    EXECUTE format(
      'GRANT SELECT, INSERT ON TABLE %I.golden_fault_consume_results TO worksflow_golden_fault_operator',
      schema_name
    );
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'worksflow_application') THEN
    EXECUTE format(
      'REVOKE ALL ON TABLE %I.golden_fault_consume_reservations FROM worksflow_application',
      schema_name
    );
    EXECUTE format(
      'REVOKE ALL ON TABLE %I.golden_fault_consume_results FROM worksflow_application',
      schema_name
    );
  END IF;
END;
$ownership_and_acl$;
