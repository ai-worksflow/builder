-- Qualification Promotion v2 is the first transaction allowed to consume a
-- terminal Qualification Receipt v3.  It composes only server-owned immutable
-- authorities and leaves an immutable pending handoff for migration 000082.
-- The fence is deliberately the first executable statement: WIA assertions,
-- Evidence/Plan/Receipt relation locks, and rolling DDL share this ordering.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

CREATE FUNCTION qualification_promotion_v2_hash(p_domain text, p_value bytea)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT 'sha256:' || pg_catalog.encode(
    pg_catalog.sha256(
      pg_catalog.convert_to('worksflow-qualification-promotion-hash/v2', 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || pg_catalog.convert_to(p_domain, 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || p_value
    ),
    'hex'
  )
$function$;

CREATE FUNCTION qualification_promotion_v2_timestamp(p_value timestamptz)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT pg_catalog.to_char(
    p_value AT TIME ZONE 'UTC',
    'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
  )
$function$;

-- This namespace is shared with every ordinary artifact revision.  The
-- primary key, rather than a check-then-insert trigger, decides concurrent
-- ownership of an output identity.
CREATE TABLE artifact_revision_identity_reservations (
  id uuid PRIMARY KEY,
  owner_kind text NOT NULL CHECK (
    owner_kind IN ('artifact-revision','qualification-promotion-v2')
  ),
  owner_operation_id uuid,
  reserved_at timestamptz NOT NULL CHECK (
    reserved_at = pg_catalog.date_trunc('milliseconds', reserved_at)
  ),
  CHECK (workflow_input_uuid_is_exact(id::text)),
  CHECK (
    (owner_kind = 'artifact-revision' AND owner_operation_id IS NULL)
    OR
    (owner_kind = 'qualification-promotion-v2'
      AND owner_operation_id IS NOT NULL
      AND workflow_input_uuid_is_exact(owner_operation_id::text))
  )
);

-- The migration transaction already owns the exclusive WIA rollout fence.
-- Take the ordinary-writer relation first, then backfill under its DDL lock.
LOCK TABLE artifact_revisions IN ACCESS EXCLUSIVE MODE;
INSERT INTO artifact_revision_identity_reservations(
  id, owner_kind, owner_operation_id, reserved_at
)
SELECT revision.id, 'artifact-revision', NULL,
       pg_catalog.date_trunc('milliseconds', revision.created_at)
FROM artifact_revisions AS revision
ORDER BY revision.id;

CREATE TABLE qualification_promotion_v2_independent_receipts (
  kind text NOT NULL CHECK (
    kind IN ('model-profile-activation','production-postgresql-posture')
  ),
  authority_id text NOT NULL CHECK (
    pg_catalog.octet_length(authority_id) BETWEEN 1 AND 256
    AND authority_id = pg_catalog.btrim(authority_id)
    AND authority_id ~ '^[a-z0-9]+(?:[a-z0-9._:/@+-]*[a-z0-9])?$'
    AND authority_id !~ '://'
    AND authority_id !~ '[[:cntrl:]]'
  ),
  authority_hash text NOT NULL CHECK (authority_hash ~ '^sha256:[0-9a-f]{64}$'),
  receipt_schema_version text NOT NULL CHECK (
    pg_catalog.octet_length(receipt_schema_version) BETWEEN 1 AND 256
  ),
  source_receipt_hash text NOT NULL CHECK (source_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
  source_receipt_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(source_receipt_bytes) BETWEEN 1 AND 16777216
  ),
  source_receipt_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(source_receipt_document) = 'object'
  ),
  admission_record_hash text NOT NULL UNIQUE CHECK (
    admission_record_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  admission_record_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(admission_record_bytes) BETWEEN 1 AND 1048576
  ),
  admission_record_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(admission_record_document) = 'object'
  ),
  issued_at timestamptz NOT NULL CHECK (issued_at = pg_catalog.date_trunc('milliseconds', issued_at)),
  expires_at timestamptz NOT NULL CHECK (expires_at = pg_catalog.date_trunc('milliseconds', expires_at)),
  verified_at timestamptz NOT NULL CHECK (verified_at = pg_catalog.date_trunc('milliseconds', verified_at)),
  PRIMARY KEY (kind, authority_id, authority_hash),
  CHECK (issued_at < verified_at AND verified_at < expires_at),
  CHECK (source_receipt_hash <> admission_record_hash)
);

CREATE TABLE qualification_promotion_v2_consumptions (
  operation_id uuid PRIMARY KEY,
  workflow_input_authority_id uuid NOT NULL UNIQUE
    REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  plan_authority_id uuid NOT NULL UNIQUE
    REFERENCES qualification_plan_authorities(authority_id) ON DELETE RESTRICT,
  input_precommit_authority_id uuid NOT NULL UNIQUE
    REFERENCES qualification_input_precommit_authorities(authority_id) ON DELETE RESTRICT,
  receipt_id text NOT NULL UNIQUE
    REFERENCES qualification_receipt_v3_receipts(receipt_id) ON DELETE RESTRICT,
  request_hash text NOT NULL UNIQUE CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
  request_bytes bytea NOT NULL CHECK (pg_catalog.octet_length(request_bytes) BETWEEN 1 AND 65536),
  request_document jsonb NOT NULL CHECK (pg_catalog.jsonb_typeof(request_document) = 'object'),
  evidence_event_set_hash text NOT NULL CHECK (evidence_event_set_hash ~ '^sha256:[0-9a-f]{64}$'),
  evidence_event_set_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(evidence_event_set_bytes) BETWEEN 1 AND 16777216
  ),
  evidence_event_set_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(evidence_event_set_document) = 'object'
  ),
  closure_hash text NOT NULL CHECK (closure_hash ~ '^sha256:[0-9a-f]{64}$'),
  closure_bytes bytea NOT NULL CHECK (pg_catalog.octet_length(closure_bytes) BETWEEN 1 AND 33554432),
  closure_document jsonb NOT NULL CHECK (pg_catalog.jsonb_typeof(closure_document) = 'object'),
  revision_intent_hash text NOT NULL CHECK (revision_intent_hash ~ '^sha256:[0-9a-f]{64}$'),
  revision_intent_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(revision_intent_bytes) BETWEEN 1 AND 1048576
  ),
  revision_intent_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(revision_intent_document) = 'object'
  ),
  consumption_hash text NOT NULL UNIQUE CHECK (consumption_hash ~ '^sha256:[0-9a-f]{64}$'),
  consumption_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(consumption_bytes) BETWEEN 1 AND 1048576
  ),
  consumption_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(consumption_document) = 'object'
  ),
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  node_run_id uuid NOT NULL,
  node_key text NOT NULL CHECK (pg_catalog.octet_length(node_key) BETWEEN 1 AND 256),
  target_artifact_id uuid NOT NULL,
  target_revision_id uuid NOT NULL,
  target_revision_content_hash text NOT NULL CHECK (
    target_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  subject text NOT NULL CHECK (
    pg_catalog.octet_length(subject) BETWEEN 1 AND 256
    AND subject = pg_catalog.btrim(subject)
    AND subject !~ '[[:cntrl:]]'
  ),
  stage_gate text NOT NULL CHECK (stage_gate = 'external-qualification'),
  consumed_at timestamptz NOT NULL CHECK (
    consumed_at = pg_catalog.date_trunc('milliseconds', consumed_at)
  ),
  CHECK (
    workflow_input_uuid_is_exact(operation_id::text)
    AND workflow_input_uuid_is_exact(workflow_input_authority_id::text)
    AND workflow_input_uuid_is_exact(plan_authority_id::text)
    AND workflow_input_uuid_is_exact(input_precommit_authority_id::text)
    AND workflow_input_uuid_is_exact(project_id::text)
    AND workflow_input_uuid_is_exact(workflow_run_id::text)
    AND workflow_input_uuid_is_exact(node_run_id::text)
    AND workflow_input_uuid_is_exact(target_artifact_id::text)
    AND workflow_input_uuid_is_exact(target_revision_id::text)
  ),
  CHECK (
    operation_id <> workflow_input_authority_id
    AND operation_id <> plan_authority_id
    AND operation_id <> input_precommit_authority_id
    AND workflow_input_authority_id <> plan_authority_id
    AND workflow_input_authority_id <> input_precommit_authority_id
    AND plan_authority_id <> input_precommit_authority_id
  ),
  UNIQUE (operation_id, revision_intent_hash),
  UNIQUE (operation_id, consumption_hash),
  FOREIGN KEY (target_revision_id) REFERENCES artifact_revisions(id) ON DELETE RESTRICT
);

CREATE INDEX qualification_promotion_v2_consumptions_target_idx
  ON qualification_promotion_v2_consumptions(
    project_id, workflow_run_id, node_run_id, target_revision_id, consumed_at
  );

CREATE TABLE qualification_promotion_v2_consumption_independent_receipts (
  operation_id uuid NOT NULL REFERENCES qualification_promotion_v2_consumptions(operation_id) ON DELETE RESTRICT,
  ordinal smallint NOT NULL CHECK (ordinal BETWEEN 0 AND 1),
  kind text NOT NULL,
  authority_id text NOT NULL,
  authority_hash text NOT NULL,
  admission_record_hash text NOT NULL,
  source_receipt_hash text NOT NULL,
  receipt_schema_version text NOT NULL,
  PRIMARY KEY (operation_id, ordinal),
  UNIQUE (operation_id, kind),
  FOREIGN KEY (kind, authority_id, authority_hash)
    REFERENCES qualification_promotion_v2_independent_receipts(kind, authority_id, authority_hash)
    ON DELETE RESTRICT,
  CHECK (admission_record_hash ~ '^sha256:[0-9a-f]{64}$'),
  CHECK (source_receipt_hash ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE qualification_promotion_v2_handoffs (
  handoff_id uuid PRIMARY KEY,
  operation_id uuid NOT NULL UNIQUE
    REFERENCES qualification_promotion_v2_consumptions(operation_id) ON DELETE RESTRICT,
  state text NOT NULL CHECK (state = 'pending'),
  output_revision_id uuid NOT NULL UNIQUE,
  revision_intent_hash text NOT NULL,
  consumption_hash text NOT NULL,
  handoff_hash text NOT NULL UNIQUE CHECK (handoff_hash ~ '^sha256:[0-9a-f]{64}$'),
  handoff_bytes bytea NOT NULL CHECK (pg_catalog.octet_length(handoff_bytes) BETWEEN 1 AND 1048576),
  handoff_document jsonb NOT NULL CHECK (pg_catalog.jsonb_typeof(handoff_document) = 'object'),
  created_at timestamptz NOT NULL CHECK (created_at = pg_catalog.date_trunc('milliseconds', created_at)),
  CHECK (
    workflow_input_uuid_is_exact(handoff_id::text)
    AND workflow_input_uuid_is_exact(output_revision_id::text)
    AND handoff_id <> operation_id
    AND handoff_id <> output_revision_id
    AND operation_id <> output_revision_id
  ),
  FOREIGN KEY (operation_id, revision_intent_hash)
    REFERENCES qualification_promotion_v2_consumptions(operation_id, revision_intent_hash)
    ON DELETE RESTRICT,
  FOREIGN KEY (operation_id, consumption_hash)
    REFERENCES qualification_promotion_v2_consumptions(operation_id, consumption_hash)
    ON DELETE RESTRICT
);

CREATE INDEX qualification_promotion_v2_handoffs_pending_idx
  ON qualification_promotion_v2_handoffs(created_at, handoff_id)
  WHERE state = 'pending';

CREATE TABLE qualification_promotion_v2_identity_reservations (
  identity_value uuid PRIMARY KEY,
  operation_id uuid NOT NULL REFERENCES qualification_promotion_v2_consumptions(operation_id) ON DELETE RESTRICT,
  identity_kind text NOT NULL CHECK (
    identity_kind IN ('operation','handoff','output-revision')
  ),
  ordinal smallint NOT NULL CHECK (ordinal BETWEEN 0 AND 2),
  reserved_at timestamptz NOT NULL CHECK (
    reserved_at = pg_catalog.date_trunc('milliseconds', reserved_at)
  ),
  UNIQUE (operation_id, identity_kind),
  UNIQUE (operation_id, ordinal),
  CHECK (workflow_input_uuid_is_exact(identity_value::text))
);

ALTER TABLE artifact_revision_identity_reservations
  ADD CONSTRAINT artifact_revision_identity_promotion_operation_fk
  FOREIGN KEY (owner_operation_id)
  REFERENCES qualification_promotion_v2_consumptions(operation_id)
  ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION reject_qualification_promotion_v2_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog
AS $function$
BEGIN
  RAISE EXCEPTION 'Qualification Promotion v2 records and reservations are immutable'
    USING ERRCODE = 'WPV02';
END;
$function$;

CREATE FUNCTION inspect_qualification_promotion_v2_operation(p_operation_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_promotion_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Promotion v2 inspection caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.pg_is_in_recovery()
     OR pg_catalog.current_setting('transaction_read_only') <> 'off' THEN
    RAISE EXCEPTION 'Qualification Promotion v2 inspection requires a read-write primary'
      USING ERRCODE = 'WPV03';
  END IF;
  IF p_operation_id IS NULL
     OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM qualification_promotion_v2_consumptions
    WHERE operation_id = p_operation_id
  ) THEN
    RETURN;
  END IF;
  RETURN NEXT qualification_promotion_v2_store_bundle(
    p_operation_id, false, false
  );
END;
$function$;

CREATE FUNCTION resolve_qualification_promotion_v2_handoff(p_handoff_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_operation_id uuid;
BEGIN
  IF p_handoff_id IS NULL
     OR workflow_input_uuid_is_exact(p_handoff_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  SELECT operation_id INTO v_operation_id
  FROM qualification_promotion_v2_handoffs
  WHERE handoff_id = p_handoff_id;
  IF v_operation_id IS NULL THEN RETURN; END IF;
  RETURN NEXT qualification_promotion_v2_store_bundle(
    v_operation_id, false, false
  );
END;
$function$;

CREATE FUNCTION assert_pending_qualification_promotion_v2_handoff(p_handoff_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_operation_id uuid;
  v_state text;
BEGIN
  IF p_handoff_id IS NULL
     OR workflow_input_uuid_is_exact(p_handoff_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  SELECT operation_id, state INTO v_operation_id, v_state
  FROM qualification_promotion_v2_handoffs
  WHERE handoff_id = p_handoff_id
  FOR UPDATE;
  IF v_operation_id IS NULL THEN RETURN; END IF;
  IF v_state <> 'pending' THEN
    RAISE EXCEPTION 'Qualification Promotion v2 handoff is not pending'
      USING ERRCODE = 'WPV03';
  END IF;
  PERFORM 1 FROM qualification_promotion_v2_consumptions
  WHERE operation_id = v_operation_id
  FOR UPDATE;
  IF qualification_promotion_v2_store_record_is_exact(v_operation_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Promotion v2 pending handoff is corrupt'
      USING ERRCODE = 'WPV02';
  END IF;
  RETURN NEXT qualification_promotion_v2_store_bundle(
    v_operation_id, false, false
  );
END;
$function$;

CREATE FUNCTION inspect_historical_qualification_promotion_v1_operation(
  p_operation_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_consumption qualification_promotion_consumptions%ROWTYPE;
  v_handoff qualification_promotion_handoffs%ROWTYPE;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_promotion_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'historical Qualification Promotion inspection caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF p_operation_id IS NULL
     OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE THEN
    RETURN;
  END IF;
  SELECT * INTO v_consumption
  FROM qualification_promotion_consumptions
  WHERE operation_id = p_operation_id;
  IF NOT FOUND THEN RETURN; END IF;
  SELECT * INTO v_handoff
  FROM qualification_promotion_handoffs
  WHERE operation_id = p_operation_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'historical Qualification Promotion v1 aggregate is corrupt'
      USING ERRCODE = 'WPV02';
  END IF;
  RETURN NEXT pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-v1-history-bundle/v1',
    'operationId', v_consumption.operation_id::text,
    'qualificationAuthorityId', v_consumption.qualification_authority_id::text,
    'requestHash', v_consumption.request_hash,
    'targetDigest', v_consumption.target_digest,
    'verifiedPromotionHash', v_consumption.verified_promotion_hash,
    'consumedAt', qualification_promotion_v2_timestamp(v_consumption.consumed_at),
    'handoffId', v_handoff.handoff_id::text,
    'state', v_handoff.state,
    'outputRevisionId', v_handoff.output_revision_id::text,
    'revisionIntentDigest', v_handoff.revision_intent_digest,
    'createdAt', qualification_promotion_v2_timestamp(v_handoff.created_at)
  );
END;
$function$;

COMMENT ON TABLE qualification_promotion_v2_consumptions IS
  'Append-only exact Promotion-v2 composition and single-use consumption; never a workflow completion.';
COMMENT ON TABLE qualification_promotion_v2_handoffs IS
  'Append-only pending Promotion-v2 revision intent for the separately reviewed migration-82 handoff consumer.';
COMMENT ON TABLE artifact_revision_identity_reservations IS
  'Global atomic owner namespace shared by ordinary artifact revisions and Promotion-v2 output reservations.';

CREATE FUNCTION qualification_promotion_v2_apply_security()
RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
AS $qualification_promotion_v2_security$
DECLARE
  schema_name text := pg_catalog.current_schema();
  role_name text;
  role_sql text;
BEGIN
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.qualification_promotion_v2_hash(text,bytea) '
    'SET search_path TO pg_catalog', schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.qualification_promotion_v2_timestamp(timestamptz) '
    'SET search_path TO pg_catalog', schema_name
  );
  FOREACH role_name IN ARRAY ARRAY[
    'PUBLIC','worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_repository_index_gc_operator','worksflow_golden_fault_operator',
    'worksflow_qualification_plan_operator','worksflow_qualification_policy_operator',
    'worksflow_workflow_input_authority_operator',
    'worksflow_qualification_promotion_operator'
  ] LOOP
    IF role_name = 'PUBLIC' OR EXISTS (
      SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = role_name
    ) THEN
      role_sql := CASE WHEN role_name = 'PUBLIC' THEN 'PUBLIC'
        ELSE pg_catalog.quote_ident(role_name) END;
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON TABLE %I.artifact_revision_identity_reservations FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON TABLE %I.qualification_promotion_v2_independent_receipts FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON TABLE %I.qualification_promotion_v2_consumptions FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON TABLE %I.qualification_promotion_v2_consumption_independent_receipts FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON TABLE %I.qualification_promotion_v2_handoffs FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON TABLE %I.qualification_promotion_v2_identity_reservations FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.qualification_promotion_v2_hash(text,bytea) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.qualification_promotion_v2_timestamp(timestamptz) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.reject_qualification_promotion_v2_mutation() FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.reserve_ordinary_artifact_revision_identity_v1() FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.qualification_promotion_v2_plan_is_exact(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.qualification_promotion_v2_store_record_is_exact(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.qualification_promotion_v2_store_bundle(uuid,boolean,boolean) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.inspect_qualification_promotion_v2_operation(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.resolve_qualification_promotion_v2_handoff(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.assert_pending_qualification_promotion_v2_handoff(uuid) FROM %s',
        schema_name, role_sql
      );
      EXECUTE pg_catalog.format(
        'REVOKE ALL ON FUNCTION %I.inspect_historical_qualification_promotion_v1_operation(uuid) FROM %s',
        schema_name, role_sql
      );
    END IF;
  END LOOP;

  -- Every definer routine is pinned to the migration schema.  pg_temp is last
  -- because consume needs transaction-local work only through trusted names.
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.reserve_ordinary_artifact_revision_identity_v1() '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.qualification_promotion_v2_plan_is_exact(uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.qualification_promotion_v2_store_record_is_exact(uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.qualification_promotion_v2_store_bundle(uuid,boolean,boolean) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.inspect_qualification_promotion_v2_operation(uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.resolve_qualification_promotion_v2_handoff(uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.assert_pending_qualification_promotion_v2_handoff(uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.inspect_historical_qualification_promotion_v1_operation(uuid) '
    'SET search_path TO pg_catalog, %I, pg_temp', schema_name, schema_name
  );

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner'
  ) THEN
    FOREACH role_name IN ARRAY ARRAY[
      'artifact_revision_identity_reservations',
      'qualification_promotion_v2_independent_receipts',
      'qualification_promotion_v2_consumptions',
      'qualification_promotion_v2_consumption_independent_receipts',
      'qualification_promotion_v2_handoffs',
      'qualification_promotion_v2_identity_reservations'
    ] LOOP
      EXECUTE pg_catalog.format(
        'ALTER TABLE %I.%I OWNER TO worksflow_migration_owner', schema_name, role_name
      );
    END LOOP;
    FOREACH role_name IN ARRAY ARRAY[
      'qualification_promotion_v2_hash(text,bytea)',
      'qualification_promotion_v2_timestamp(timestamptz)',
      'reject_qualification_promotion_v2_mutation()',
      'reserve_ordinary_artifact_revision_identity_v1()',
      'qualification_promotion_v2_plan_is_exact(uuid)',
      'qualification_promotion_v2_store_record_is_exact(uuid)',
      'qualification_promotion_v2_store_bundle(uuid,boolean,boolean)',
      'consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)',
      'inspect_qualification_promotion_v2_operation(uuid)',
      'resolve_qualification_promotion_v2_handoff(uuid)',
      'assert_pending_qualification_promotion_v2_handoff(uuid)',
      'inspect_historical_qualification_promotion_v1_operation(uuid)'
    ] LOOP
      EXECUTE pg_catalog.format(
        'ALTER FUNCTION %I.%s OWNER TO worksflow_migration_owner', schema_name, role_name
      );
    END LOOP;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_promotion_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON TABLE %I.qualification_promotion_consumptions '
      'FROM worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON TABLE %I.qualification_promotion_handoffs '
      'FROM worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON FUNCTION %I.consume_verified_qualification_promotion('
      'uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb) '
      'FROM worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON FUNCTION %I.assert_current_workflow_input_authority_v1(uuid) '
      'FROM worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON FUNCTION %I.assert_current_qualification_policy_authority_v1(uuid) '
      'FROM worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_promotion_operator',
      schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid) '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.inspect_qualification_promotion_v2_operation(uuid) '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.inspect_historical_qualification_promotion_v1_operation(uuid) '
      'TO worksflow_qualification_promotion_operator', schema_name
    );
  END IF;
END;
$qualification_promotion_v2_security$;

CREATE TRIGGER artifact_revision_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON artifact_revision_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_v2_mutation();
CREATE TRIGGER qualification_promotion_v2_independent_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_promotion_v2_independent_receipts
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_v2_mutation();
CREATE TRIGGER qualification_promotion_v2_consumptions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_promotion_v2_consumptions
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_v2_mutation();
CREATE TRIGGER qualification_promotion_v2_consumption_independent_receipts_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_promotion_v2_consumption_independent_receipts
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_v2_mutation();
CREATE TRIGGER qualification_promotion_v2_handoffs_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_promotion_v2_handoffs
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_v2_mutation();
CREATE TRIGGER qualification_promotion_v2_identity_reservations_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON qualification_promotion_v2_identity_reservations
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_promotion_v2_mutation();

CREATE FUNCTION reserve_ordinary_artifact_revision_identity_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_owner artifact_revision_identity_reservations%ROWTYPE;
BEGIN
  INSERT INTO artifact_revision_identity_reservations(
    id, owner_kind, owner_operation_id, reserved_at
  ) VALUES (
    NEW.id, 'artifact-revision', NULL,
    pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp())
  )
  ON CONFLICT (id) DO NOTHING;
  IF NOT FOUND THEN
    SELECT * INTO v_owner
    FROM artifact_revision_identity_reservations
    WHERE id = NEW.id
    FOR SHARE;
    IF v_owner.owner_kind <> 'artifact-revision' THEN
      RAISE EXCEPTION 'artifact revision identity is owned by Qualification Promotion v2'
        USING ERRCODE = 'WPV02';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER artifact_revisions_shared_identity_reservation
BEFORE INSERT ON artifact_revisions
FOR EACH ROW EXECUTE FUNCTION reserve_ordinary_artifact_revision_identity_v1();

-- Recompute the immutable Plan's byte/hash/JSON projections.  Freeze already
-- validates its closed domain shape; Promotion deliberately does not trust
-- that historical validation without independently replaying the bytes.
CREATE FUNCTION qualification_promotion_v2_plan_is_exact(p_authority_id uuid)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_plan qualification_plan_authorities%ROWTYPE;
BEGIN
  IF p_authority_id IS NULL OR workflow_input_uuid_is_exact(p_authority_id::text) IS NOT TRUE THEN
    RETURN false;
  END IF;
  SELECT * INTO v_plan
  FROM qualification_plan_authorities
  WHERE authority_id = p_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  RETURN
    qualification_plan_sha256(v_plan.request_bytes) = v_plan.request_hash
    AND qualification_plan_sha256(v_plan.input_bytes) = v_plan.input_hash
    AND qualification_plan_sha256(v_plan.projection_bytes) = v_plan.projection_hash
    AND qualification_plan_sha256(v_plan.evidence_plan_bytes) = v_plan.evidence_plan_hash
    AND qualification_plan_sha256(v_plan.trust_bytes) = v_plan.trust_hash
    AND qualification_plan_sha256(v_plan.target_bytes) = v_plan.target_hash
    AND qualification_plan_sha256(v_plan.envelope_bytes) = v_plan.envelope_hash
    AND workflow_input_canonical_jsonb_bytes(v_plan.request_document) = v_plan.request_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.input_document) = v_plan.input_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.projection_document) = v_plan.projection_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.evidence_plan_document) = v_plan.evidence_plan_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.trust_document) = v_plan.trust_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.target_document) = v_plan.target_bytes
    AND workflow_input_canonical_jsonb_bytes(v_plan.envelope_document) = v_plan.envelope_bytes
    AND v_plan.request_document->>'authorityId' = v_plan.authority_id::text
    AND v_plan.request_document->>'operationId' = v_plan.operation_id::text
    AND v_plan.request_document->>'inputAuthorityId' = v_plan.input_authority_id::text
    AND v_plan.evidence_plan_document->>'orchestrationId' = v_plan.orchestration_id::text
    AND v_plan.evidence_plan_document->>'runId' = v_plan.qualification_run_id::text
    AND v_plan.evidence_plan_document->>'fixtureId' = v_plan.fixture_id::text
    AND v_plan.target_document#>>'{promotionTarget,projectId}' = v_plan.project_id::text
    AND v_plan.target_document#>>'{promotionTarget,workflowRunId}' = v_plan.workflow_run_id::text
    AND v_plan.target_document#>>'{promotionTarget,nodeKey}' = v_plan.node_key
    AND v_plan.target_document#>>'{promotionTarget,targetRevision,id}' = v_plan.target_revision_id::text
    AND v_plan.target_document#>>'{promotionTarget,targetRevision,contentHash}' = v_plan.target_revision_content_hash
    AND v_plan.target_document#>>'{promotionTarget,subject}' = v_plan.subject
    AND v_plan.target_document#>>'{promotionTarget,stageGate}' = v_plan.stage_gate
    AND v_plan.envelope_document->>'authorityId' = v_plan.authority_id::text
    AND v_plan.envelope_document->>'operationId' = v_plan.operation_id::text
    AND v_plan.envelope_document->>'inputAuthorityId' = v_plan.input_authority_id::text
    AND v_plan.envelope_document->>'inputHash' = v_plan.input_hash
    AND v_plan.envelope_document->>'projectionHash' = v_plan.projection_hash
    AND v_plan.envelope_document->>'evidencePlanHash' = v_plan.evidence_plan_hash
    AND v_plan.envelope_document->>'targetHash' = v_plan.target_hash
    AND v_plan.envelope_document->>'trustHash' = v_plan.trust_hash;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_promotion_v2_store_record_is_exact(p_operation_id uuid)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_consumption qualification_promotion_v2_consumptions%ROWTYPE;
  v_handoff qualification_promotion_v2_handoffs%ROWTYPE;
  v_input_precommit qualification_input_precommit_authorities%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_plan qualification_plan_authorities%ROWTYPE;
  v_receipt qualification_receipt_v3_receipts%ROWTYPE;
  v_target jsonb;
  v_last_event jsonb;
  v_expected_request jsonb;
  v_expected_closure jsonb;
  v_expected_revision_intent jsonb;
  v_expected_consumption jsonb;
  v_expected_handoff jsonb;
BEGIN
  IF p_operation_id IS NULL OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE THEN
    RETURN false;
  END IF;
  SELECT * INTO v_consumption
  FROM qualification_promotion_v2_consumptions
  WHERE operation_id = p_operation_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_handoff
  FROM qualification_promotion_v2_handoffs
  WHERE operation_id = p_operation_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_input_precommit
  FROM qualification_input_precommit_authorities
  WHERE authority_id = v_consumption.input_precommit_authority_id;
  IF NOT FOUND OR qualification_input_precommit_authority_record_is_exact_v1(
    v_consumption.input_precommit_authority_id
  ) IS NOT TRUE THEN RETURN false; END IF;
  SELECT * INTO v_wia FROM workflow_input_authorities
  WHERE authority_id = v_consumption.workflow_input_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_plan FROM qualification_plan_authorities
  WHERE authority_id = v_consumption.plan_authority_id;
  IF NOT FOUND OR qualification_promotion_v2_plan_is_exact(v_plan.authority_id) IS NOT TRUE THEN
    RETURN false;
  END IF;
  SELECT * INTO v_receipt FROM qualification_receipt_v3_receipts
  WHERE receipt_id = v_consumption.receipt_id;
  IF NOT FOUND THEN RETURN false; END IF;

  v_target := pg_catalog.jsonb_build_object(
    'artifactId', v_consumption.target_artifact_id::text,
    'nodeKey', v_consumption.node_key,
    'nodeRunId', v_consumption.node_run_id::text,
    'projectId', v_consumption.project_id::text,
    'revisionContentHash', v_consumption.target_revision_content_hash,
    'revisionId', v_consumption.target_revision_id::text,
    'stageGate', v_consumption.stage_gate,
    'subject', v_consumption.subject,
    'workflowRunId', v_consumption.workflow_run_id::text
  );
  v_last_event := v_consumption.evidence_event_set_document->'events'->
    ((v_consumption.evidence_event_set_document->>'headVersion')::integer - 1);
  v_expected_request := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-consume-request/v2',
    'operationId', v_consumption.operation_id::text,
    'workflowInputAuthorityId', v_consumption.workflow_input_authority_id::text,
    'planAuthorityId', v_consumption.plan_authority_id::text,
    'handoffId', v_handoff.handoff_id::text,
    'outputRevisionId', v_handoff.output_revision_id::text
  );
  v_expected_closure := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-closure/v2',
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityId', v_wia.authority_id::text,
      'authorityHash', v_wia.authority_hash,
      'inputHash', v_wia.input_hash,
      'targetHash', v_wia.target_hash,
      'qualificationPolicyAuthorityId', v_wia.qualification_policy_authority_id::text,
      'qualificationPolicyAuthorityHash', v_wia.qualification_policy_authority_hash
    ),
    'inputPrecommit', pg_catalog.jsonb_build_object(
      'kind', 'qualification-input-precommit',
      'authorityId', v_input_precommit.authority_id::text,
      'authorityHash', v_input_precommit.authority_hash,
      'workflowInputAuthorityId', v_input_precommit.workflow_input_authority_id::text,
      'workflowInputAuthorityHash', v_input_precommit.workflow_input_authority_hash,
      'qualificationPolicyAuthorityId', v_input_precommit.qualification_policy_authority_id::text,
      'qualificationPolicyAuthorityHash', v_input_precommit.qualification_policy_authority_hash,
      'qualificationPlanAuthorityId', v_input_precommit.qualification_plan_authority_id::text,
      'qualificationPlanAuthorityHash', v_input_precommit.qualification_plan_authority_hash,
      'sourceRequestHash', v_input_precommit.source_request_hash,
      'sourceReceiptHash', v_input_precommit.source_receipt_hash,
      'sourceAdmissionHash', v_input_precommit.source_admission_hash,
      'credentialRequestHash', v_input_precommit.credential_request_hash,
      'credentialReceiptHash', v_input_precommit.credential_receipt_hash,
      'credentialAdmissionHash', v_input_precommit.credential_admission_hash
    ),
    'plan', pg_catalog.jsonb_build_object(
      'authorityId', v_plan.authority_id::text,
      'authorityHash', v_plan.envelope_hash,
      'inputAuthorityId', v_plan.input_authority_id::text,
      'inputHash', v_plan.input_hash,
      'projectionHash', v_plan.projection_hash,
      'evidencePlanHash', v_plan.evidence_plan_hash,
      'targetHash', v_plan.target_hash,
      'trustHash', v_plan.trust_hash,
      'orchestrationId', v_plan.orchestration_id::text,
      'qualificationRunId', v_plan.qualification_run_id::text
    ),
    'evidence', pg_catalog.jsonb_build_object(
      'headVersion', (v_consumption.evidence_event_set_document->>'headVersion')::bigint,
      'phase', 'artifact-indexed',
      'lastEventId', v_last_event->>'eventId',
      'lastEventHash', v_last_event->>'eventHash',
      'commandHash', v_plan.evidence_plan_hash,
      'trustBindingsDigest', v_plan.trust_bindings_digest,
      'evidenceClosureDigest', v_receipt.evidence_closure_digest,
      'artifactIndexDigest', v_receipt.artifact_index_digest,
      'eventSetDigest', v_consumption.evidence_event_set_hash
    ),
    'receipt', pg_catalog.jsonb_build_object(
      'receiptId', v_receipt.receipt_id,
      'envelopeHash', v_receipt.envelope_hash,
      'payloadHash', v_receipt.payload_hash,
      'paeHash', v_receipt.pae_hash,
      'completionHash', v_receipt.completion_hash,
      'snapshotRequestHash', v_receipt.snapshot_request_hash,
      'snapshotObservationHash', v_receipt.snapshot_observation_hash,
      'verificationRequestHash', v_receipt.verification_request_hash,
      'verificationObservationHash', v_receipt.verification_observation_hash,
      'runnerRequestHash', v_receipt.runner_request_hash,
      'runnerObservationHash', v_receipt.runner_observation_hash,
      'approverRequestHash', v_receipt.approver_request_hash,
      'approverObservationHash', v_receipt.approver_observation_hash
    ),
    'target', v_target,
    'independentAuthorities', '[]'::jsonb
  );
  v_expected_revision_intent := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-revision-intent/v2',
    'requestHash', v_consumption.request_hash,
    'closureHash', v_consumption.closure_hash,
    'outputRevisionId', v_handoff.output_revision_id::text,
    'revisionKind', 'external-qualification-promotion/v2',
    'target', v_target,
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityId', v_wia.authority_id::text, 'authorityHash', v_wia.authority_hash
    ),
    'plan', pg_catalog.jsonb_build_object(
      'authorityId', v_plan.authority_id::text, 'authorityHash', v_plan.envelope_hash
    ),
    'receipt', pg_catalog.jsonb_build_object(
      'receiptId', v_receipt.receipt_id, 'envelopeHash', v_receipt.envelope_hash
    )
  );
  v_expected_consumption := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-consumption/v2',
    'operationId', v_consumption.operation_id::text,
    'requestHash', v_consumption.request_hash,
    'closureHash', v_consumption.closure_hash,
    'revisionIntentHash', v_consumption.revision_intent_hash,
    'consumedAt', qualification_promotion_v2_timestamp(v_consumption.consumed_at)
  );
  v_expected_handoff := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-handoff/v2',
    'handoffId', v_handoff.handoff_id::text,
    'operationId', v_handoff.operation_id::text,
    'state', 'pending',
    'outputRevisionId', v_handoff.output_revision_id::text,
    'revisionIntentHash', v_handoff.revision_intent_hash,
    'workflowInputAuthorityId', v_consumption.workflow_input_authority_id::text,
    'planAuthorityId', v_consumption.plan_authority_id::text,
    'receiptId', v_consumption.receipt_id,
    'consumptionHash', v_handoff.consumption_hash,
    'target', v_target,
    'createdAt', qualification_promotion_v2_timestamp(v_handoff.created_at)
  );

  RETURN
    ROW(
      v_input_precommit.workflow_input_authority_id,
      v_input_precommit.workflow_input_authority_hash,
      v_input_precommit.qualification_policy_authority_id,
      v_input_precommit.qualification_policy_authority_hash,
      v_input_precommit.qualification_plan_authority_id,
      v_input_precommit.qualification_plan_authority_hash
    ) IS NOT DISTINCT FROM ROW(
      v_wia.authority_id, v_wia.authority_hash,
      v_wia.qualification_policy_authority_id,
      v_wia.qualification_policy_authority_hash,
      v_plan.authority_id, v_plan.envelope_hash
    )
    AND v_input_precommit.authority_id <> v_handoff.handoff_id
    AND v_input_precommit.authority_id <> v_handoff.output_revision_id
    AND v_consumption.evidence_event_set_document = pg_catalog.jsonb_build_object(
      'schemaVersion', 'worksflow-qualification-promotion-evidence-event-set/v2',
      'orchestrationId', v_plan.orchestration_id::text,
      'headVersion', (v_consumption.evidence_event_set_document->>'headVersion')::bigint,
      'events', v_consumption.evidence_event_set_document->'events'
    )
    AND pg_catalog.jsonb_array_length(
      v_consumption.evidence_event_set_document->'events'
    ) = (v_consumption.evidence_event_set_document->>'headVersion')::integer
    AND v_consumption.request_document = v_expected_request
    AND v_consumption.closure_document = v_expected_closure
    AND v_consumption.revision_intent_document = v_expected_revision_intent
    AND v_consumption.consumption_document = v_expected_consumption
    AND v_handoff.handoff_document = v_expected_handoff
    AND workflow_input_canonical_jsonb_bytes(v_consumption.request_document) = v_consumption.request_bytes
    AND workflow_input_canonical_jsonb_bytes(v_consumption.evidence_event_set_document)
      = v_consumption.evidence_event_set_bytes
    AND workflow_input_canonical_jsonb_bytes(v_consumption.closure_document) = v_consumption.closure_bytes
    AND workflow_input_canonical_jsonb_bytes(v_consumption.revision_intent_document)
      = v_consumption.revision_intent_bytes
    AND workflow_input_canonical_jsonb_bytes(v_consumption.consumption_document)
      = v_consumption.consumption_bytes
    AND workflow_input_canonical_jsonb_bytes(v_handoff.handoff_document) = v_handoff.handoff_bytes
    AND qualification_promotion_v2_hash(
      'worksflow.qualification-promotion.request/v2', v_consumption.request_bytes
    ) = v_consumption.request_hash
    AND qualification_promotion_v2_hash(
      'worksflow.qualification-promotion.evidence-event-set/v2',
      v_consumption.evidence_event_set_bytes
    ) = v_consumption.evidence_event_set_hash
    AND qualification_promotion_v2_hash(
      'worksflow.qualification-promotion.closure/v2', v_consumption.closure_bytes
    ) = v_consumption.closure_hash
    AND qualification_promotion_v2_hash(
      'worksflow.qualification-promotion.revision-intent/v2',
      v_consumption.revision_intent_bytes
    ) = v_consumption.revision_intent_hash
    AND qualification_promotion_v2_hash(
      'worksflow.qualification-promotion.consumption/v2', v_consumption.consumption_bytes
    ) = v_consumption.consumption_hash
    AND qualification_promotion_v2_hash(
      'worksflow.qualification-promotion.handoff/v2', v_handoff.handoff_bytes
    ) = v_handoff.handoff_hash
    AND v_consumption.request_document->>'schemaVersion'
      = 'worksflow-qualification-promotion-consume-request/v2'
    AND v_consumption.request_document->>'operationId' = v_consumption.operation_id::text
    AND v_consumption.request_document->>'workflowInputAuthorityId'
      = v_consumption.workflow_input_authority_id::text
    AND v_consumption.request_document->>'planAuthorityId' = v_consumption.plan_authority_id::text
    AND v_consumption.evidence_event_set_document->>'schemaVersion'
      = 'worksflow-qualification-promotion-evidence-event-set/v2'
    AND v_consumption.closure_document->>'schemaVersion'
      = 'worksflow-qualification-promotion-closure/v2'
    AND v_consumption.closure_document#>>'{evidence,eventSetDigest}'
      = v_consumption.evidence_event_set_hash
    AND v_consumption.closure_document->'independentAuthorities' = '[]'::jsonb
    AND v_consumption.revision_intent_document->>'schemaVersion'
      = 'worksflow-qualification-promotion-revision-intent/v2'
    AND v_consumption.revision_intent_document->>'requestHash' = v_consumption.request_hash
    AND v_consumption.revision_intent_document->>'closureHash' = v_consumption.closure_hash
    AND v_consumption.revision_intent_document->>'outputRevisionId' = v_handoff.output_revision_id::text
    AND v_consumption.consumption_document->>'schemaVersion'
      = 'worksflow-qualification-promotion-consumption/v2'
    AND v_consumption.consumption_document->>'operationId' = v_consumption.operation_id::text
    AND v_consumption.consumption_document->>'requestHash' = v_consumption.request_hash
    AND v_consumption.consumption_document->>'closureHash' = v_consumption.closure_hash
    AND v_consumption.consumption_document->>'revisionIntentHash'
      = v_consumption.revision_intent_hash
    AND v_consumption.consumption_document->>'consumedAt'
      = qualification_promotion_v2_timestamp(v_consumption.consumed_at)
    AND v_handoff.handoff_document->>'schemaVersion'
      = 'worksflow-qualification-promotion-handoff/v2'
    AND v_handoff.handoff_document->>'handoffId' = v_handoff.handoff_id::text
    AND v_handoff.handoff_document->>'operationId' = v_handoff.operation_id::text
    AND v_handoff.handoff_document->>'state' = 'pending'
    AND v_handoff.handoff_document->>'outputRevisionId' = v_handoff.output_revision_id::text
    AND v_handoff.handoff_document->>'revisionIntentHash' = v_handoff.revision_intent_hash
    AND v_handoff.handoff_document->>'consumptionHash' = v_handoff.consumption_hash
    AND v_handoff.handoff_document->>'createdAt'
      = qualification_promotion_v2_timestamp(v_handoff.created_at)
    AND v_handoff.revision_intent_hash = v_consumption.revision_intent_hash
    AND v_handoff.consumption_hash = v_consumption.consumption_hash
    AND v_handoff.created_at = v_consumption.consumed_at
    AND (SELECT pg_catalog.count(*)
         FROM qualification_promotion_v2_consumption_independent_receipts
         WHERE operation_id = p_operation_id) = 0
    AND (SELECT pg_catalog.count(*) FROM qualification_promotion_v2_identity_reservations
         WHERE operation_id = p_operation_id) = 3
    AND NOT EXISTS (
      SELECT 1
      FROM (VALUES
        (v_consumption.operation_id, 'operation'::text, 0::smallint),
        (v_handoff.handoff_id, 'handoff'::text, 1::smallint),
        (v_handoff.output_revision_id, 'output-revision'::text, 2::smallint)
      ) AS expected(identity_value, identity_kind, ordinal)
      LEFT JOIN qualification_promotion_v2_identity_reservations AS reservation
        ON reservation.identity_value = expected.identity_value
       AND reservation.operation_id = p_operation_id
       AND reservation.identity_kind = expected.identity_kind
       AND reservation.ordinal = expected.ordinal
       AND reservation.reserved_at = v_consumption.consumed_at
      WHERE reservation.identity_value IS NULL
    )
    AND EXISTS (
      SELECT 1 FROM artifact_revision_identity_reservations
      WHERE id = v_handoff.output_revision_id
        AND owner_kind = 'qualification-promotion-v2'
        AND owner_operation_id = p_operation_id
        AND reserved_at = v_consumption.consumed_at
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_promotion_v2_store_bundle(
  p_operation_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_consumption qualification_promotion_v2_consumptions%ROWTYPE;
  v_handoff qualification_promotion_v2_handoffs%ROWTYPE;
  v_bundle jsonb;
BEGIN
  IF qualification_promotion_v2_store_record_is_exact(p_operation_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Promotion v2 durable aggregate is corrupt'
      USING ERRCODE = 'WPV02';
  END IF;
  SELECT * INTO STRICT v_consumption
  FROM qualification_promotion_v2_consumptions WHERE operation_id = p_operation_id;
  SELECT * INTO STRICT v_handoff
  FROM qualification_promotion_v2_handoffs WHERE operation_id = p_operation_id;
  v_bundle := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-store-bundle/v2',
    'operationId', v_consumption.operation_id::text,
    'workflowInputAuthorityId', v_consumption.workflow_input_authority_id::text,
    'planAuthorityId', v_consumption.plan_authority_id::text,
    'receiptId', v_consumption.receipt_id,
    'evidenceEventSet', pg_catalog.jsonb_build_object(
      'hash', v_consumption.evidence_event_set_hash,
      'bytesHex', pg_catalog.encode(v_consumption.evidence_event_set_bytes, 'hex'),
      'document', v_consumption.evidence_event_set_document
    ),
    'request', pg_catalog.jsonb_build_object(
      'hash', v_consumption.request_hash,
      'bytesHex', pg_catalog.encode(v_consumption.request_bytes, 'hex'),
      'document', v_consumption.request_document
    ),
    'closure', pg_catalog.jsonb_build_object(
      'hash', v_consumption.closure_hash,
      'bytesHex', pg_catalog.encode(v_consumption.closure_bytes, 'hex'),
      'document', v_consumption.closure_document
    ),
    'revisionIntent', pg_catalog.jsonb_build_object(
      'hash', v_consumption.revision_intent_hash,
      'bytesHex', pg_catalog.encode(v_consumption.revision_intent_bytes, 'hex'),
      'document', v_consumption.revision_intent_document
    ),
    'consumption', pg_catalog.jsonb_build_object(
      'hash', v_consumption.consumption_hash,
      'bytesHex', pg_catalog.encode(v_consumption.consumption_bytes, 'hex'),
      'document', v_consumption.consumption_document,
      'consumedAt', qualification_promotion_v2_timestamp(v_consumption.consumed_at)
    ),
    'handoff', pg_catalog.jsonb_build_object(
      'handoffId', v_handoff.handoff_id::text,
      'hash', v_handoff.handoff_hash,
      'bytesHex', pg_catalog.encode(v_handoff.handoff_bytes, 'hex'),
      'document', v_handoff.handoff_document,
      'state', v_handoff.state,
      'outputRevisionId', v_handoff.output_revision_id::text,
      'createdAt', qualification_promotion_v2_timestamp(v_handoff.created_at)
    )
  );
  IF p_include_idempotent IS TRUE THEN
    v_bundle := v_bundle || pg_catalog.jsonb_build_object(
      'idempotent', COALESCE(p_idempotent, false)
    );
  END IF;
  RETURN v_bundle;
END;
$function$;

CREATE FUNCTION consume_qualification_promotion_v2(
  p_operation_id uuid,
  p_workflow_input_authority_id uuid,
  p_plan_authority_id uuid,
  p_handoff_id uuid,
  p_output_revision_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_existing qualification_promotion_v2_consumptions%ROWTYPE;
  v_existing_handoff qualification_promotion_v2_handoffs%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_policy qualification_policy_authorities%ROWTYPE;
  v_plan qualification_plan_authorities%ROWTYPE;
  v_input_precommit qualification_input_precommit_authorities%ROWTYPE;
  v_plan_locator uuid;
  v_head qualification_evidence_heads%ROWTYPE;
  v_last_event qualification_evidence_events%ROWTYPE;
  v_receipt qualification_receipt_v3_receipts%ROWTYPE;
  v_snapshot_request qualification_receipt_v3_requests%ROWTYPE;
  v_verification_request qualification_receipt_v3_requests%ROWTYPE;
  v_runner_request qualification_receipt_v3_requests%ROWTYPE;
  v_approver_request qualification_receipt_v3_requests%ROWTYPE;
  v_snapshot_observation qualification_receipt_v3_observations%ROWTYPE;
  v_verification_observation qualification_receipt_v3_observations%ROWTYPE;
  v_runner_observation qualification_receipt_v3_observations%ROWTYPE;
  v_approver_observation qualification_receipt_v3_observations%ROWTYPE;
  v_target_revision artifact_revisions%ROWTYPE;
  v_request_document jsonb;
  v_request_bytes bytea;
  v_request_hash text;
  v_event_set_document jsonb;
  v_event_set_bytes bytea;
  v_event_set_hash text;
  v_closure_document jsonb;
  v_closure_bytes bytea;
  v_closure_hash text;
  v_revision_intent_document jsonb;
  v_revision_intent_bytes bytea;
  v_revision_intent_hash text;
  v_consumption_document jsonb;
  v_consumption_bytes bytea;
  v_consumption_hash text;
  v_handoff_document jsonb;
  v_handoff_bytes bytea;
  v_handoff_hash text;
  v_target jsonb;
  v_events jsonb;
  v_now timestamptz;
  v_event_count bigint;
  v_operation_count bigint;
  v_reserved_operation_count bigint;
  v_independent_count integer;
BEGIN
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_promotion_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Promotion v2 caller is not authorized'
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Promotion v2 requires a SERIALIZABLE transaction'
      USING ERRCODE = 'WPV01';
  END IF;
  IF pg_catalog.pg_is_in_recovery()
     OR pg_catalog.current_setting('transaction_read_only') <> 'off' THEN
    RAISE EXCEPTION 'Qualification Promotion v2 consume requires a read-write primary'
      USING ERRCODE = 'WPV03';
  END IF;
  IF p_operation_id IS NULL
     OR p_workflow_input_authority_id IS NULL
     OR p_plan_authority_id IS NULL
     OR p_handoff_id IS NULL
     OR p_output_revision_id IS NULL
     OR workflow_input_uuid_is_exact(p_operation_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_workflow_input_authority_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_plan_authority_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_handoff_id::text) IS NOT TRUE
     OR workflow_input_uuid_is_exact(p_output_revision_id::text) IS NOT TRUE
     OR p_operation_id IN (
       p_workflow_input_authority_id, p_plan_authority_id, p_handoff_id, p_output_revision_id
     )
     OR p_workflow_input_authority_id IN (
       p_plan_authority_id, p_handoff_id, p_output_revision_id
     )
     OR p_plan_authority_id IN (p_handoff_id, p_output_revision_id)
     OR p_handoff_id = p_output_revision_id THEN
    RAISE EXCEPTION 'Qualification Promotion v2 command requires five pairwise-distinct UUIDv4 identities'
      USING ERRCODE = 'WPV01';
  END IF;

  v_request_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-consume-request/v2',
    'operationId', p_operation_id::text,
    'workflowInputAuthorityId', p_workflow_input_authority_id::text,
    'planAuthorityId', p_plan_authority_id::text,
    'handoffId', p_handoff_id::text,
    'outputRevisionId', p_output_revision_id::text
  );
  v_request_bytes := workflow_input_canonical_jsonb_bytes(v_request_document);
  v_request_hash := qualification_promotion_v2_hash(
    'worksflow.qualification-promotion.request/v2', v_request_bytes
  );

  -- No relation may be touched before this shared side of the rollout fence.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
  );
  -- The production adapter acquires a session advisory lock on this key before
  -- BEGIN.  That is essential at SERIALIZABLE: the transaction establishes its
  -- snapshot only after an exact concurrent owner commits.  This transaction
  -- advisory lock is an in-function defense for direct callers and keeps
  -- changed same-operation calls serialized too.
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-promotion-v2:operation:' || p_operation_id::text,
      0
    )
  );

  SELECT * INTO v_existing
  FROM qualification_promotion_v2_consumptions
  WHERE operation_id = p_operation_id
  FOR SHARE;
  IF FOUND THEN
    SELECT * INTO v_existing_handoff
    FROM qualification_promotion_v2_handoffs
    WHERE operation_id = p_operation_id
    FOR SHARE;
    IF NOT FOUND
       OR v_existing.request_hash <> v_request_hash
       OR v_existing.request_bytes <> v_request_bytes
       OR v_existing.request_document <> v_request_document
       OR v_existing.workflow_input_authority_id <> p_workflow_input_authority_id
       OR v_existing.plan_authority_id <> p_plan_authority_id
       OR v_existing_handoff.handoff_id <> p_handoff_id
       OR v_existing_handoff.output_revision_id <> p_output_revision_id
       OR qualification_promotion_v2_store_record_is_exact(p_operation_id) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Promotion v2 operation identity is bound to different or corrupt bytes'
        USING ERRCODE = 'WPV02';
    END IF;
    RETURN NEXT qualification_promotion_v2_store_bundle(p_operation_id, true, true);
    RETURN;
  END IF;

  -- Resolve only the project locator, then establish the global platform lock
  -- before asking WIA to re-lock and verify the complete current closure.
  SELECT project_id INTO v_wia.project_id
  FROM workflow_input_authorities
  WHERE authority_id = p_workflow_input_authority_id;
  IF v_wia.project_id IS NULL THEN
    RAISE EXCEPTION 'Workflow Input Authority is not ready'
      USING ERRCODE = 'WPV03';
  END IF;
  PERFORM 1 FROM projects WHERE id = v_wia.project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Workflow Input project is not ready'
      USING ERRCODE = 'WPV03';
  END IF;

  -- An exact concurrent attempt waited on the same project row.  Re-inspect
  -- after that wait so it converges to an immutable replay rather than a
  -- unique-violation result.
  SELECT * INTO v_existing
  FROM qualification_promotion_v2_consumptions
  WHERE operation_id = p_operation_id
  FOR SHARE;
  IF FOUND THEN
    SELECT * INTO v_existing_handoff
    FROM qualification_promotion_v2_handoffs
    WHERE operation_id = p_operation_id
    FOR SHARE;
    IF NOT FOUND
       OR v_existing.request_bytes <> v_request_bytes
       OR v_existing.workflow_input_authority_id <> p_workflow_input_authority_id
       OR v_existing.plan_authority_id <> p_plan_authority_id
       OR v_existing_handoff.handoff_id <> p_handoff_id
       OR v_existing_handoff.output_revision_id <> p_output_revision_id
       OR qualification_promotion_v2_store_record_is_exact(p_operation_id) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Promotion v2 operation replay conflicts after project lock'
        USING ERRCODE = 'WPV02';
    END IF;
    RETURN NEXT qualification_promotion_v2_store_bundle(p_operation_id, true, true);
    RETURN;
  END IF;
  IF EXISTS (
    SELECT 1 FROM qualification_promotion_v2_consumptions
    WHERE workflow_input_authority_id = p_workflow_input_authority_id
       OR plan_authority_id = p_plan_authority_id
  ) OR EXISTS (
    SELECT 1 FROM qualification_promotion_v2_handoffs
    WHERE handoff_id = p_handoff_id OR output_revision_id = p_output_revision_id
  ) THEN
    RAISE EXCEPTION 'Qualification Promotion v2 authority or handoff identity was consumed differently'
      USING ERRCODE = 'WPV02';
  END IF;

  BEGIN
    PERFORM 1 FROM assert_current_workflow_input_authority_v1(
      p_workflow_input_authority_id
    );
  EXCEPTION
    WHEN serialization_failure OR deadlock_detected THEN RAISE;
    WHEN OTHERS THEN
      RAISE EXCEPTION 'Workflow Input Authority is stale or not exact'
        USING ERRCODE = 'WPV04';
  END;
  SELECT * INTO v_wia
  FROM workflow_input_authorities
  WHERE authority_id = p_workflow_input_authority_id
  FOR SHARE;
  IF NOT FOUND OR workflow_input_authority_record_is_exact(p_workflow_input_authority_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Workflow Input Authority durable aggregate is corrupt'
      USING ERRCODE = 'WPV02';
  END IF;

  BEGIN
    PERFORM 1 FROM assert_current_qualification_policy_authority_v1(
      v_wia.qualification_policy_authority_id
    );
  EXCEPTION
    WHEN serialization_failure OR deadlock_detected THEN RAISE;
    WHEN OTHERS THEN
      RAISE EXCEPTION 'Qualification Policy Authority is stale or not exact'
        USING ERRCODE = 'WPV04';
  END;
  SELECT * INTO v_policy
  FROM qualification_policy_authorities
  WHERE authority_id = v_wia.qualification_policy_authority_id
  FOR SHARE;
  IF NOT FOUND
     OR qualification_policy_authority_record_is_exact_v1(v_policy.authority_id) IS NOT TRUE
     OR v_policy.authority_hash <> v_wia.qualification_policy_authority_hash
     OR pg_catalog.jsonb_typeof(v_policy.promotion_policy_document->'independentRequirements') <> 'array' THEN
    RAISE EXCEPTION 'Qualification Policy binding is corrupt'
      USING ERRCODE = 'WPV02';
  END IF;
  v_independent_count := pg_catalog.jsonb_array_length(
    v_policy.promotion_policy_document->'independentRequirements'
  );
  -- Promotion v2 has no independent-requirement admission writer. This
  -- branch intentionally precedes every independent receipt registry lookup.
  IF v_independent_count <> 0 THEN
    RAISE EXCEPTION 'policy-required independent authority adapters are not deployed'
      USING ERRCODE = 'WPV03';
  END IF;

  -- Immutable locator read only.  It authorizes no Plan fact and takes no row
  -- lock; the full Plan is re-read after Evidence in the established order.
  SELECT orchestration_id INTO v_plan_locator
  FROM qualification_plan_authorities
  WHERE authority_id = p_plan_authority_id;
  IF v_plan_locator IS NULL THEN
    RAISE EXCEPTION 'Qualification Plan Authority is not ready'
      USING ERRCODE = 'WPV03';
  END IF;

  LOCK TABLE qualification_evidence_events IN ROW SHARE MODE;
  LOCK TABLE qualification_evidence_operations IN ROW SHARE MODE;
  LOCK TABLE qualification_evidence_heads IN ROW SHARE MODE;

  SELECT * INTO v_head
  FROM qualification_evidence_heads
  WHERE orchestration_id = v_plan_locator
  FOR SHARE;
  IF NOT FOUND OR v_head.phase <> 'artifact-indexed'
     OR v_head.version NOT BETWEEN 1 AND 2048
     OR v_head.active_operation_id IS NOT NULL
     OR v_head.active_artifact_id <> '' THEN
    RAISE EXCEPTION 'Qualification Evidence is not at the Receipt-v3 artifact-indexed cut point'
      USING ERRCODE = 'WPV03';
  END IF;

  PERFORM 1
  FROM qualification_evidence_operations
  WHERE orchestration_id = v_plan_locator
  ORDER BY operation_id
  FOR SHARE;
  PERFORM 1
  FROM qualification_evidence_events
  WHERE orchestration_id = v_plan_locator
  ORDER BY version
  FOR SHARE;

  SELECT pg_catalog.count(*),
         pg_catalog.jsonb_agg(
           pg_catalog.jsonb_build_object(
             'version', event.version,
             'eventId', event.event_id::text,
             'eventHash', event.event_hash
           ) ORDER BY event.version
         )
    INTO v_event_count, v_events
  FROM qualification_evidence_events AS event
  WHERE event.orchestration_id = v_plan_locator;
  IF v_event_count <> v_head.version
     OR NOT EXISTS (
       SELECT 1
       FROM qualification_evidence_events AS event
       WHERE event.orchestration_id = v_plan_locator
       HAVING pg_catalog.min(event.version) = 1
          AND pg_catalog.max(event.version) = v_head.version
          AND pg_catalog.count(DISTINCT event.version) = v_head.version
     )
     OR EXISTS (
       SELECT 1 FROM qualification_evidence_events AS event
       WHERE event.orchestration_id = v_plan_locator
         AND (
           'sha256:' || pg_catalog.encode(pg_catalog.sha256(event.request_bytes), 'hex')
             <> event.request_hash
           OR 'sha256:' || pg_catalog.encode(pg_catalog.sha256(event.event_bytes), 'hex')
             <> event.event_hash
           OR workflow_input_canonical_jsonb_bytes(event.request_document) <> event.request_bytes
           OR workflow_input_canonical_jsonb_bytes(event.event_document) <> event.event_bytes
         )
     ) THEN
    RAISE EXCEPTION 'Qualification Evidence event set is missing, reordered, or corrupt'
      USING ERRCODE = 'WPV02';
  END IF;
  SELECT * INTO v_last_event
  FROM qualification_evidence_events
  WHERE orchestration_id = v_plan_locator AND version = v_head.version;
  IF NOT FOUND
     OR v_last_event.event_id <> v_head.last_event_id
     OR v_last_event.event_kind <> 'artifact-indexed'
     OR v_last_event.event_document#>>'{artifactIndex,stage}' <> 'committed'
     OR COALESCE(v_last_event.event_document#>>'{artifactIndex,contentDigest}', '') !~ '^sha256:[0-9a-f]{64}$'
     OR COALESCE(v_last_event.event_document#>>'{artifactIndex,evidenceClosureDigest}', '') !~ '^sha256:[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'Qualification Evidence terminal index binding is not exact'
      USING ERRCODE = 'WPV02';
  END IF;

  SELECT * INTO v_plan
  FROM qualification_plan_authorities
  WHERE authority_id = p_plan_authority_id
  FOR SHARE;
  IF NOT FOUND OR v_plan.orchestration_id <> v_plan_locator
     OR v_plan.input_authority_id <> p_workflow_input_authority_id
     OR qualification_promotion_v2_plan_is_exact(p_plan_authority_id) IS NOT TRUE
     OR v_head.command_hash <> v_plan.evidence_plan_hash
     OR v_head.trust_bindings_digest <> v_plan.trust_bindings_digest
     OR v_head.plan_document <> v_plan.evidence_plan_document THEN
    RAISE EXCEPTION 'Qualification Plan Authority is mismatched or corrupt'
      USING ERRCODE = 'WPV02';
  END IF;
  PERFORM 1
  FROM qualification_plan_identity_reservations
  WHERE authority_id = p_plan_authority_id
  ORDER BY identity_value
  FOR SHARE;
  SELECT pg_catalog.count(*) INTO v_reserved_operation_count
  FROM qualification_plan_identity_reservations
  WHERE authority_id = p_plan_authority_id AND identity_kind = 'evidence-operation';
  SELECT pg_catalog.count(*) INTO v_operation_count
  FROM qualification_evidence_operations AS operation
  JOIN qualification_plan_identity_reservations AS reservation
    ON reservation.identity_value = operation.operation_id::text
   AND reservation.authority_id = p_plan_authority_id
   AND reservation.identity_kind = 'evidence-operation'
  WHERE operation.orchestration_id = v_plan_locator;
  IF v_reserved_operation_count = 0
     OR v_operation_count <> v_reserved_operation_count
     OR EXISTS (
       SELECT 1 FROM qualification_evidence_operations AS operation
       WHERE operation.orchestration_id = v_plan_locator
         AND NOT EXISTS (
           SELECT 1 FROM qualification_plan_identity_reservations AS reservation
           WHERE reservation.authority_id = p_plan_authority_id
             AND reservation.identity_kind = 'evidence-operation'
             AND reservation.identity_value = operation.operation_id::text
         )
     )
     OR EXISTS (
       SELECT 1
       FROM qualification_evidence_operations AS operation
       WHERE operation.orchestration_id = v_plan_locator
         AND CASE operation.operation_kind
           WHEN 'reserve' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,reserve}'
           WHEN 'credential-issue' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,credentialIssue}'
           WHEN 'run-closure' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,runClosure}'
           WHEN 'kms-attestation' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,kmsAttestation}'
           WHEN 'credential-revocation' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,credentialRevocation}'
           WHEN 'artifact-index' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,artifactIndex}'
           WHEN 'receipt-sign' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,receiptSign}'
           WHEN 'snapshot-seal' THEN operation.operation_id::text
             <> v_plan.evidence_plan_document#>>'{operations,snapshotSeal}'
           WHEN 'encryption' THEN NOT EXISTS (
             SELECT 1
             FROM pg_catalog.jsonb_array_elements(
               v_plan.evidence_plan_document->'artifacts'
             ) AS artifact(value)
             WHERE artifact.value->>'classification' = 'restricted'
               AND artifact.value->>'id' = operation.artifact_id
               AND artifact.value->>'encryptionOperationId' = operation.operation_id::text
           )
           ELSE true
         END
     ) THEN
    RAISE EXCEPTION 'Qualification Evidence operation set does not equal the immutable Plan reservations'
      USING ERRCODE = 'WPV02';
  END IF;

  -- Resolve the one typed input-precommit authority only after the exact WIA,
  -- current Policy, and Plan rows are locked.  The reviewed migration-80
  -- resolver locks the authority plus both source/credential admission rows
  -- and replays their raw canonical bytes and reviewed executable bindings.
  -- Promotion never accepts a caller-supplied precommit identity.
  BEGIN
    SELECT * INTO STRICT v_input_precommit
    FROM resolve_qualification_input_precommit_for_promotion_v1(
      p_workflow_input_authority_id, p_plan_authority_id
    );
  EXCEPTION
    WHEN serialization_failure OR deadlock_detected THEN RAISE;
    WHEN no_data_found THEN
      RAISE EXCEPTION 'exact Qualification Input Precommit authority is not ready'
        USING ERRCODE = 'WPV03';
    WHEN too_many_rows THEN
      RAISE EXCEPTION 'Qualification Input Precommit authority resolution is non-unique'
        USING ERRCODE = 'WPV02';
    WHEN SQLSTATE 'WIP02' THEN
      RAISE EXCEPTION 'Qualification Input Precommit lock or byte replay failed'
        USING ERRCODE = 'WPV02';
  END;
  IF qualification_input_precommit_authority_record_is_exact_v1(
       v_input_precommit.authority_id
     ) IS NOT TRUE
     OR ROW(
       v_input_precommit.workflow_input_authority_id,
       v_input_precommit.workflow_input_authority_hash,
       v_input_precommit.qualification_policy_authority_id,
       v_input_precommit.qualification_policy_authority_hash,
       v_input_precommit.qualification_plan_authority_id,
       v_input_precommit.qualification_plan_authority_hash
     ) IS DISTINCT FROM ROW(
       v_wia.authority_id, v_wia.authority_hash,
       v_policy.authority_id, v_policy.authority_hash,
       v_plan.authority_id, v_plan.envelope_hash
     ) THEN
    RAISE EXCEPTION 'Qualification Input Precommit drifted from locked WIA, current Policy, or Plan'
      USING ERRCODE = 'WPV02';
  END IF;
  IF v_input_precommit.authority_id IN (
    p_operation_id, p_handoff_id, p_output_revision_id
  ) THEN
    RAISE EXCEPTION 'Promotion command identity aliases the resolved Input Precommit authority'
      USING ERRCODE = 'WPV02';
  END IF;

  -- Lock the four Plan-scoped requests before their terminal observations and
  -- only then lock the unique terminal receipt.
  SELECT * INTO v_snapshot_request
  FROM qualification_receipt_v3_requests
  WHERE plan_authority_id = p_plan_authority_id
    AND request_kind = 'snapshot-seal' AND signer_role = 'sealer'
  FOR SHARE;
  SELECT * INTO v_verification_request
  FROM qualification_receipt_v3_requests
  WHERE plan_authority_id = p_plan_authority_id
    AND request_kind = 'snapshot-verify' AND signer_role = 'verifier'
  FOR SHARE;
  SELECT * INTO v_runner_request
  FROM qualification_receipt_v3_requests
  WHERE plan_authority_id = p_plan_authority_id
    AND request_kind = 'receipt-sign' AND signer_role = 'qualification-runner'
  FOR SHARE;
  SELECT * INTO v_approver_request
  FROM qualification_receipt_v3_requests
  WHERE plan_authority_id = p_plan_authority_id
    AND request_kind = 'receipt-sign' AND signer_role = 'release-approver'
  FOR SHARE;
  IF v_snapshot_request.request_hash IS NULL
     OR v_verification_request.request_hash IS NULL
     OR v_runner_request.request_hash IS NULL
     OR v_approver_request.request_hash IS NULL THEN
    RAISE EXCEPTION 'Qualification Receipt v3 four-request closure is not ready'
      USING ERRCODE = 'WPV03';
  END IF;

  SELECT * INTO v_snapshot_observation
  FROM qualification_receipt_v3_observations
  WHERE request_hash = v_snapshot_request.request_hash
  ORDER BY sequence DESC LIMIT 1 FOR SHARE;
  SELECT * INTO v_verification_observation
  FROM qualification_receipt_v3_observations
  WHERE request_hash = v_verification_request.request_hash
  ORDER BY sequence DESC LIMIT 1 FOR SHARE;
  SELECT * INTO v_runner_observation
  FROM qualification_receipt_v3_observations
  WHERE request_hash = v_runner_request.request_hash
  ORDER BY sequence DESC LIMIT 1 FOR SHARE;
  SELECT * INTO v_approver_observation
  FROM qualification_receipt_v3_observations
  WHERE request_hash = v_approver_request.request_hash
  ORDER BY sequence DESC LIMIT 1 FOR SHARE;
  IF v_snapshot_observation.status IS DISTINCT FROM 'committed'
     OR v_verification_observation.status IS DISTINCT FROM 'committed'
     OR v_runner_observation.status IS DISTINCT FROM 'committed'
     OR v_approver_observation.status IS DISTINCT FROM 'committed' THEN
    RAISE EXCEPTION 'Qualification Receipt v3 latest observations are not terminal committed records'
      USING ERRCODE = 'WPV03';
  END IF;

  SELECT * INTO v_receipt
  FROM qualification_receipt_v3_receipts
  WHERE plan_authority_id = p_plan_authority_id
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'terminal Qualification Receipt v3 is not ready'
      USING ERRCODE = 'WPV03';
  END IF;
  IF ROW(
       v_receipt.snapshot_request_hash, v_receipt.snapshot_observation_hash,
       v_receipt.verification_request_hash, v_receipt.verification_observation_hash,
       v_receipt.runner_request_hash, v_receipt.runner_observation_hash,
       v_receipt.approver_request_hash, v_receipt.approver_observation_hash
     ) IS DISTINCT FROM ROW(
       v_snapshot_request.request_hash, v_snapshot_observation.record_hash,
       v_verification_request.request_hash, v_verification_observation.record_hash,
       v_runner_request.request_hash, v_runner_observation.record_hash,
       v_approver_request.request_hash, v_approver_observation.record_hash
     )
     OR EXISTS (
       SELECT 1 FROM (VALUES
         (v_snapshot_request.request_hash, v_snapshot_request.request_bytes, v_snapshot_request.request_document),
         (v_verification_request.request_hash, v_verification_request.request_bytes, v_verification_request.request_document),
         (v_runner_request.request_hash, v_runner_request.request_bytes, v_runner_request.request_document),
         (v_approver_request.request_hash, v_approver_request.request_bytes, v_approver_request.request_document)
       ) AS request(hash, bytes, document)
       WHERE qualification_receipt_v3_sha256(request.bytes) <> request.hash
          OR workflow_input_canonical_jsonb_bytes(request.document) <> request.bytes
     )
     OR EXISTS (
       SELECT 1
       FROM qualification_receipt_v3_requests AS request
       WHERE request.request_hash IN (
         v_snapshot_request.request_hash, v_verification_request.request_hash,
         v_runner_request.request_hash, v_approver_request.request_hash
       )
         AND (
           request.plan_authority_id <> v_plan.authority_id
           OR request.orchestration_id <> v_plan.orchestration_id
           OR request.plan_authority_hash <> v_plan.envelope_hash
           OR request.input_hash <> v_plan.input_hash
           OR request.projection_hash <> v_plan.projection_hash
           OR request.evidence_plan_hash <> v_plan.evidence_plan_hash
           OR request.target_hash <> v_plan.target_hash
           OR request.trust_hash <> v_plan.trust_hash
           OR request.trust_bindings_digest <> v_plan.trust_bindings_digest
           OR request.trust_policy_digest <> v_plan.trust_policy_digest
           OR request.evidence_head_version <> v_head.version
           OR request.evidence_last_event_id <> v_head.last_event_id
           OR request.evidence_last_event_hash <> v_last_event.event_hash
           OR request.evidence_command_digest <> v_head.command_hash
           OR request.evidence_trust_digest <> v_head.trust_bindings_digest
           OR request.evidence_closure_digest
              <> v_last_event.event_document#>>'{artifactIndex,evidenceClosureDigest}'
           OR request.artifact_index_digest
              <> v_last_event.event_document#>>'{artifactIndex,contentDigest}'
           OR request.request_document->>'schemaVersion'
              IS DISTINCT FROM 'worksflow-qualification-receipt-control-request/v1'
           OR request.request_document->>'kind'
              IS DISTINCT FROM request.request_kind
           OR request.request_document->>'role'
              IS DISTINCT FROM request.signer_role
           OR request.request_document->>'planAuthorityId'
              IS DISTINCT FROM request.plan_authority_id::text
           OR request.request_document->>'orchestrationId'
              IS DISTINCT FROM request.orchestration_id::text
           OR request.request_document->>'operationId'
              IS DISTINCT FROM request.operation_id::text
           OR request.request_document->>'operationalAuthorityId'
              IS DISTINCT FROM request.operational_authority_id
           OR request.request_document->>'authenticationKeyId'
              IS DISTINCT FROM request.authentication_key_id
           OR request.request_document->>'signerIdentity'
              IS DISTINCT FROM request.signer_identity
           OR request.request_document->>'signerKeyId'
              IS DISTINCT FROM request.signer_key_id
           OR request.request_document->>'snapshotId'
              IS DISTINCT FROM request.snapshot_id
           OR request.request_document->>'snapshotDigest'
              IS DISTINCT FROM request.snapshot_digest
           OR request.request_document->>'receiptId'
              IS DISTINCT FROM request.receipt_id
           OR request.request_document->>'planAuthorityHash'
              IS DISTINCT FROM request.plan_authority_hash
           OR request.request_document->>'inputHash'
              IS DISTINCT FROM request.input_hash
           OR request.request_document->>'projectionHash'
              IS DISTINCT FROM request.projection_hash
           OR request.request_document->>'evidencePlanHash'
              IS DISTINCT FROM request.evidence_plan_hash
           OR request.request_document->>'targetHash'
              IS DISTINCT FROM request.target_hash
           OR request.request_document->>'trustHash'
              IS DISTINCT FROM request.trust_hash
           OR request.request_document->>'trustBindingsDigest'
              IS DISTINCT FROM request.trust_bindings_digest
           OR request.request_document->>'trustPolicyDigest'
              IS DISTINCT FROM request.trust_policy_digest
           OR request.request_document->>'evidenceHeadVersion'
              IS DISTINCT FROM request.evidence_head_version::text
           OR request.request_document->>'evidenceLastEventId'
              IS DISTINCT FROM request.evidence_last_event_id::text
           OR request.request_document->>'evidenceLastEventHash'
              IS DISTINCT FROM request.evidence_last_event_hash
           OR request.request_document->>'evidenceCommandDigest'
              IS DISTINCT FROM request.evidence_command_digest
           OR request.request_document->>'evidenceTrustDigest'
              IS DISTINCT FROM request.evidence_trust_digest
           OR request.request_document->>'evidenceClosureDigest'
              IS DISTINCT FROM request.evidence_closure_digest
           OR request.request_document->>'artifactIndexDigest'
              IS DISTINCT FROM request.artifact_index_digest
           OR request.request_document->>'payloadDigest'
              IS DISTINCT FROM request.payload_hash
           OR request.request_document->>'paeDigest'
              IS DISTINCT FROM request.pae_hash
           OR request.snapshot_id
              IS DISTINCT FROM v_plan.evidence_plan_document#>>'{outputs,snapshotId}'
           OR request.operation_id IS DISTINCT FROM CASE request.request_kind
                WHEN 'snapshot-seal' THEN
                  (v_plan.evidence_plan_document#>>'{operations,snapshotSeal}')::uuid
                WHEN 'snapshot-verify' THEN
                  (v_plan.evidence_plan_document#>>'{operations,snapshotSeal}')::uuid
                ELSE (v_plan.evidence_plan_document#>>'{operations,receiptSign}')::uuid
              END
           OR request.operational_authority_id IS DISTINCT FROM CASE request.signer_role
                WHEN 'sealer' THEN
                  v_plan.trust_document#>>'{trustBindings,sealerAuthorityId}'
                WHEN 'verifier' THEN
                  v_plan.trust_document#>>'{trustBindings,verifierAuthorityId}'
                ELSE v_plan.trust_document#>>'{trustBindings,receiptAuthorityId}'
              END
         )
     )
     OR EXISTS (
       SELECT 1 FROM (VALUES
         (v_snapshot_observation.record_hash, v_snapshot_observation.observation_bytes, v_snapshot_observation.observation_document),
         (v_verification_observation.record_hash, v_verification_observation.observation_bytes, v_verification_observation.observation_document),
         (v_runner_observation.record_hash, v_runner_observation.observation_bytes, v_runner_observation.observation_document),
         (v_approver_observation.record_hash, v_approver_observation.observation_bytes, v_approver_observation.observation_document)
       ) AS observation(hash, bytes, document)
       WHERE qualification_receipt_v3_sha256(observation.bytes) <> observation.hash
          OR workflow_input_canonical_jsonb_bytes(observation.document) <> observation.bytes
     )
     OR EXISTS (
       SELECT 1
       FROM qualification_receipt_v3_observations AS observation
       WHERE (observation.request_hash, observation.record_hash) IN (
         (v_snapshot_request.request_hash, v_snapshot_observation.record_hash),
         (v_verification_request.request_hash, v_verification_observation.record_hash),
         (v_runner_request.request_hash, v_runner_observation.record_hash),
         (v_approver_request.request_hash, v_approver_observation.record_hash)
       )
         AND (
           qualification_receipt_v3_sha256(observation.authentication_payload_bytes)
             <> observation.authentication_payload_hash
           OR workflow_input_canonical_jsonb_bytes(observation.authentication_payload_document)
             <> observation.authentication_payload_bytes
           OR qualification_receipt_v3_sha256(observation.authentication_envelope_bytes)
             <> observation.authentication_envelope_hash
           OR workflow_input_canonical_jsonb_bytes(observation.authentication_envelope_document)
             <> observation.authentication_envelope_bytes
           OR (observation.result_bytes IS NOT NULL AND (
             qualification_receipt_v3_sha256(observation.result_bytes) <> observation.result_hash
             OR workflow_input_canonical_jsonb_bytes(observation.result_document)
                <> observation.result_bytes
           ))
           OR (observation.signature_bytes IS NOT NULL AND
             qualification_receipt_v3_sha256(observation.signature_bytes)
               <> observation.signature_hash)
           OR observation.observed_at > observation.recorded_at
         )
     )
     OR qualification_receipt_v3_sha256(v_receipt.payload_bytes) <> v_receipt.payload_hash
     OR qualification_receipt_v3_sha256(v_receipt.pae_bytes) <> v_receipt.pae_hash
     OR qualification_receipt_v3_sha256(v_receipt.envelope_bytes) <> v_receipt.envelope_hash
     OR qualification_receipt_v3_sha256(v_receipt.completion_bytes) <> v_receipt.completion_hash
     OR workflow_input_canonical_jsonb_bytes(v_receipt.payload_document) <> v_receipt.payload_bytes
     OR workflow_input_canonical_jsonb_bytes(v_receipt.envelope_document) <> v_receipt.envelope_bytes
     OR workflow_input_canonical_jsonb_bytes(v_receipt.completion_document) <> v_receipt.completion_bytes
     OR v_receipt.envelope_document->>'payloadType'
        IS DISTINCT FROM 'application/vnd.in-toto+json'
     OR pg_catalog.decode(v_receipt.envelope_document->>'payload', 'base64')
        IS DISTINCT FROM v_receipt.payload_bytes
     OR pg_catalog.replace(
          pg_catalog.encode(
            pg_catalog.decode(v_receipt.envelope_document->>'payload', 'base64'),
            'base64'
          ),
          pg_catalog.chr(10),
          ''
        ) IS DISTINCT FROM v_receipt.envelope_document->>'payload'
     OR v_receipt.plan_authority_hash <> v_plan.envelope_hash
     OR v_receipt.orchestration_id <> v_plan.orchestration_id
     OR v_receipt.evidence_closure_digest
        <> v_last_event.event_document#>>'{artifactIndex,evidenceClosureDigest}'
     OR v_receipt.artifact_index_digest
        <> v_last_event.event_document#>>'{artifactIndex,contentDigest}'
     OR v_receipt.payload_document#>'{predicate,artifactIndex}'
        IS DISTINCT FROM v_last_event.event_document->'artifactIndex'
     OR v_receipt.payload_document#>'{predicate,snapshot}'
        IS DISTINCT FROM v_snapshot_observation.result_document
     OR v_receipt.payload_document#>'{predicate,snapshotVerification}'
        IS DISTINCT FROM v_verification_observation.result_document
     OR v_receipt.payload_document#>>'{predicate,evidence,closureDigest}'
        IS DISTINCT FROM
          v_last_event.event_document#>>'{artifactIndex,evidenceClosureDigest}'
     OR v_receipt.payload_document#>>'{predicate,evidence,orchestrationId}'
        IS DISTINCT FROM v_plan.orchestration_id::text
     OR v_receipt.payload_document#>>'{predicate,evidence,runId}'
        IS DISTINCT FROM v_plan.evidence_plan_document->>'runId'
     OR v_receipt.payload_document#>>'{subject,0,name}'
        IS DISTINCT FROM v_snapshot_observation.result_document->>'snapshotId'
     OR v_receipt.payload_document#>>'{subject,0,digest,sha256}'
        IS DISTINCT FROM pg_catalog.replace(
          v_snapshot_observation.result_document->>'snapshotDigest', 'sha256:', ''
        )
     OR v_receipt.completion_document IS DISTINCT FROM
        pg_catalog.jsonb_build_object(
          'completedAt', qualification_promotion_v2_timestamp(v_receipt.completed_at),
          'envelopeHash', v_receipt.envelope_hash,
          'evidenceClosureDigest', v_receipt.evidence_closure_digest,
          'observationHashes', pg_catalog.jsonb_build_object(
            'approverSign', v_receipt.approver_observation_hash,
            'runnerSign', v_receipt.runner_observation_hash,
            'snapshotSeal', v_receipt.snapshot_observation_hash,
            'snapshotVerify', v_receipt.verification_observation_hash
          ),
          'operations', pg_catalog.jsonb_build_object(
            'receiptSign', v_runner_request.operation_id::text,
            'snapshot', v_snapshot_request.operation_id::text
          ),
          'paeDigest', v_receipt.pae_hash,
          'payloadDigest', v_receipt.payload_hash,
          'planAuthorityHash', v_receipt.plan_authority_hash,
          'planAuthorityId', v_receipt.plan_authority_id::text,
          'receiptId', v_receipt.receipt_id,
          'requestHashes', pg_catalog.jsonb_build_object(
            'approverSign', v_receipt.approver_request_hash,
            'runnerSign', v_receipt.runner_request_hash,
            'snapshotSeal', v_receipt.snapshot_request_hash,
            'snapshotVerify', v_receipt.verification_request_hash
          ),
          'schemaVersion', 'worksflow-qualification-receipt-control-completion/v1',
          'snapshotDigest', v_runner_request.snapshot_digest,
          'snapshotId', v_runner_request.snapshot_id
        )
     OR v_receipt.completed_at <= greatest(
       v_snapshot_observation.recorded_at,
       v_verification_observation.recorded_at,
       v_runner_observation.recorded_at,
       v_approver_observation.recorded_at
     )
     OR v_runner_request.payload_hash IS DISTINCT FROM v_receipt.payload_hash
     OR v_approver_request.payload_hash IS DISTINCT FROM v_receipt.payload_hash
     OR v_runner_request.payload_bytes IS DISTINCT FROM v_receipt.payload_bytes
     OR v_approver_request.payload_bytes IS DISTINCT FROM v_receipt.payload_bytes
     OR v_runner_request.pae_hash IS DISTINCT FROM v_receipt.pae_hash
     OR v_approver_request.pae_hash IS DISTINCT FROM v_receipt.pae_hash
     OR v_runner_request.pae_bytes IS DISTINCT FROM v_receipt.pae_bytes
     OR v_approver_request.pae_bytes IS DISTINCT FROM v_receipt.pae_bytes
     OR v_runner_request.pae_bytes IS DISTINCT FROM (
       pg_catalog.convert_to(
         'DSSEv1 28 application/vnd.in-toto+json '
           || pg_catalog.octet_length(v_runner_request.payload_bytes)::text || ' ',
         'UTF8'
       ) || v_runner_request.payload_bytes
     )
     OR v_approver_request.pae_bytes IS DISTINCT FROM (
       pg_catalog.convert_to(
         'DSSEv1 28 application/vnd.in-toto+json '
           || pg_catalog.octet_length(v_approver_request.payload_bytes)::text || ' ',
         'UTF8'
       ) || v_approver_request.payload_bytes
     )
     OR v_runner_request.authentication_key_id IS DISTINCT FROM
        v_runner_request.signer_key_id
     OR v_approver_request.authentication_key_id IS DISTINCT FROM
        v_approver_request.signer_key_id
     OR v_runner_request.signer_identity = ''
     OR v_approver_request.signer_identity = ''
     OR v_runner_request.signer_identity IS NOT DISTINCT FROM
        v_approver_request.signer_identity
     OR v_runner_request.signer_key_id IS NOT DISTINCT FROM
        v_approver_request.signer_key_id
     OR v_snapshot_request.payload_hash IS DISTINCT FROM ''
     OR v_verification_request.payload_hash IS DISTINCT FROM ''
     OR v_snapshot_request.payload_bytes IS NOT NULL
     OR v_verification_request.payload_bytes IS NOT NULL
     OR v_snapshot_request.pae_hash IS DISTINCT FROM ''
     OR v_verification_request.pae_hash IS DISTINCT FROM ''
     OR v_snapshot_request.pae_bytes IS NOT NULL
     OR v_verification_request.pae_bytes IS NOT NULL
     OR v_snapshot_request.snapshot_digest IS DISTINCT FROM ''
     OR v_verification_request.snapshot_digest IS DISTINCT FROM
        v_snapshot_observation.result_document->>'snapshotDigest'
     OR v_runner_request.snapshot_digest IS DISTINCT FROM
        v_snapshot_observation.result_document->>'snapshotDigest'
     OR v_approver_request.snapshot_digest IS DISTINCT FROM
        v_snapshot_observation.result_document->>'snapshotDigest'
     OR v_verification_observation.result_document->>'snapshotDigest'
        IS DISTINCT FROM v_snapshot_observation.result_document->>'snapshotDigest'
     OR v_snapshot_request.receipt_id IS DISTINCT FROM ''
     OR v_verification_request.receipt_id IS DISTINCT FROM ''
     OR v_runner_request.receipt_id IS DISTINCT FROM
        v_plan.evidence_plan_document#>>'{outputs,receiptId}'
     OR v_approver_request.receipt_id IS DISTINCT FROM
        v_plan.evidence_plan_document#>>'{outputs,receiptId}'
     OR v_receipt.payload_document#>>'{predicate,receiptId}' IS DISTINCT FROM
        v_plan.evidence_plan_document#>>'{outputs,receiptId}'
     OR v_receipt.payload_document#>>'{predicate,operationId}' IS DISTINCT FROM
        v_plan.evidence_plan_document#>>'{operations,receiptSign}'
     OR v_receipt.payload_document#>'{predicate,signers,runner}' IS DISTINCT FROM
        pg_catalog.jsonb_build_object(
          'identity', v_runner_request.signer_identity,
          'keyId', v_runner_request.signer_key_id,
          'role', v_runner_request.signer_role
        )
     OR v_receipt.payload_document#>'{predicate,signers,approver}' IS DISTINCT FROM
        pg_catalog.jsonb_build_object(
          'identity', v_approver_request.signer_identity,
          'keyId', v_approver_request.signer_key_id,
          'role', v_approver_request.signer_role
        )
     OR NOT EXISTS (
       SELECT 1
       FROM pg_catalog.jsonb_array_elements(v_receipt.envelope_document->'signatures')
         AS signature(value)
       WHERE signature.value->>'keyid' = v_runner_request.signer_key_id
         AND pg_catalog.decode(signature.value->>'sig', 'base64')
           = v_runner_observation.signature_bytes
     )
     OR NOT EXISTS (
       SELECT 1
       FROM pg_catalog.jsonb_array_elements(v_receipt.envelope_document->'signatures')
         AS signature(value)
       WHERE signature.value->>'keyid' = v_approver_request.signer_key_id
         AND pg_catalog.decode(signature.value->>'sig', 'base64')
           = v_approver_observation.signature_bytes
     ) THEN
    RAISE EXCEPTION 'terminal Qualification Receipt v3 exact byte/control closure is corrupt'
      USING ERRCODE = 'WPV02';
  END IF;

  -- The Plan input is not allowed to invent or weaken any value fixed by the
  -- reviewed Qualification Policy Authority.  The credential request-set
  -- digest and source-policy digest have no corresponding field in the v1
  -- Plan input.  This routine therefore does not claim those two independent
  -- precommit edges; production rollout remains blocked until the follow-up
  -- input-attestation authority binds them.  Every field which is currently
  -- representable on both sides is compared directly and NULL-safely here.
  IF v_plan.input_document->'artifactPolicy'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'artifactPolicy'
     OR v_plan.input_document->'artifacts'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'artifacts'
     OR v_plan.input_document->'goldenRuntime'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'goldenRuntime'
     OR v_plan.input_document->'outputPolicy'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'outputPolicy'
     OR v_plan.input_document->'outputs'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'outputs'
     OR v_plan.input_document->'recipient'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'recipient'
     OR v_plan.input_document->'templateRelease'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'templateRelease'
     OR v_plan.input_document->'trustBindings'
       IS DISTINCT FROM v_policy.plan_input_profile_document->'trustBindings'
     OR v_plan.input_document->>'trustPolicyDigest'
       IS DISTINCT FROM v_policy.plan_input_profile_document->>'trustPolicyDigest'
     OR v_plan.input_document->'qualificationManifest'
       IS DISTINCT FROM (
         (
           v_policy.plan_input_profile_document
             #> ARRAY['qualificationManifest']::text[]
         )::jsonb - 'planDigest'::text
       )
     OR v_plan.input_document->>'qualificationPlanDigest'
       IS DISTINCT FROM
         v_policy.plan_input_profile_document#>>'{qualificationManifest,planDigest}'
     OR v_plan.input_document#>>'{credential,audience}'
       IS DISTINCT FROM
         v_policy.plan_input_profile_document#>>'{credentialProfile,audience}'
     OR v_plan.input_document#>>'{credential,issuer}'
       IS DISTINCT FROM
         v_policy.plan_input_profile_document#>>'{credentialProfile,authorityId}'
     OR v_plan.input_document#>>'{credential,issuanceArtifactId}'
       IS DISTINCT FROM
         v_policy.plan_input_profile_document#>>'{credentialProfile,issuanceArtifactId}'
     OR v_plan.input_document#>>'{credential,revocationArtifactId}'
       IS DISTINCT FROM
         v_policy.plan_input_profile_document#>>'{credentialProfile,revocationArtifactId}' THEN
    RAISE EXCEPTION 'Qualification Plan input differs from the reviewed Policy profile'
      USING ERRCODE = 'WPV04';
  END IF;

  -- Receipt-v3 stores exact signed bytes, but Promotion must not rely on the
  -- signers having copied every Plan member correctly.  Rebind all lineage
  -- projections from the locked Plan itself.  These comparisons deliberately
  -- do not use a caller-provided aggregate digest.
  IF v_receipt.payload_document#>'{predicate,planAuthority}' IS DISTINCT FROM
       pg_catalog.jsonb_build_object(
         'artifactId', v_plan.plan_artifact_id,
         'authorityHash', v_plan.envelope_hash,
         'authorityId', v_plan.authority_id::text,
         'evidencePlanHash', v_plan.evidence_plan_hash,
         'freezeOperationId', v_plan.operation_id::text,
         'inputAuthorityId', v_plan.input_authority_id::text,
         'inputHash', v_plan.input_hash,
         'planDigest', v_plan.projection_hash,
         'projectionHash', v_plan.projection_hash,
         'targetHash', v_plan.target_hash,
         'trustBindingsDigest', v_plan.trust_bindings_digest,
         'trustHash', v_plan.trust_hash
       )
     OR v_receipt.payload_document#>'{predicate,evidencePlan}'
       IS DISTINCT FROM v_plan.evidence_plan_document
     OR v_receipt.payload_document#>'{predicate,target}'
       IS DISTINCT FROM v_plan.target_document
     OR v_receipt.payload_document#>'{predicate,trust}'
       IS DISTINCT FROM v_plan.trust_document
     OR v_receipt.payload_document#>'{predicate,source}'
       IS DISTINCT FROM v_plan.input_document->'source'
     OR v_receipt.payload_document#>'{predicate,templateRelease}'
       IS DISTINCT FROM v_plan.input_document->'templateRelease'
     OR v_receipt.payload_document#>'{predicate,goldenRuntime}'
       IS DISTINCT FROM v_plan.input_document->'goldenRuntime'
     OR v_receipt.payload_document#>'{predicate,qualificationManifest}'
       IS DISTINCT FROM v_plan.input_document->'qualificationManifest'
     OR v_receipt.payload_document#>'{predicate,build}' IS DISTINCT FROM
       pg_catalog.jsonb_build_object(
         'contract', v_plan.input_document->'buildContract',
         'manifest', v_plan.input_document->'buildManifest'
       )
     OR v_receipt.payload_document#>>'{predicate,credentialSet,setId}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,setId}'
     OR v_receipt.payload_document#>>'{predicate,credentialSet,issuer}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,issuer}'
     OR v_receipt.payload_document#>>'{predicate,credentialSet,audience}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,audience}'
     OR v_receipt.payload_document#>>'{predicate,credentialSet,setHandleHash}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,setHandleHash}'
     OR v_receipt.payload_document#>>'{predicate,credentialSet,memberBindingsDigest}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,memberBindingsDigest}'
     OR v_receipt.payload_document#>>'{predicate,credentialSet,memberCount}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,memberCount}'
     OR v_receipt.payload_document#>>'{predicate,credentialSet,issuance,artifactId}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,issuanceArtifactId}'
     OR v_receipt.payload_document#>>'{predicate,credentialSet,revocation,artifactId}'
       IS DISTINCT FROM v_plan.input_document#>>'{credential,revocationArtifactId}' THEN
    RAISE EXCEPTION 'terminal Qualification Receipt v3 lineage differs from the locked Plan'
      USING ERRCODE = 'WPV02';
  END IF;

  SELECT * INTO v_target_revision
  FROM artifact_revisions
  WHERE id = v_wia.target_revision_id
  FOR SHARE;
  IF NOT FOUND OR v_target_revision.artifact_id <> v_wia.target_artifact_id
     OR v_target_revision.content_hash <> v_wia.target_revision_content_hash
     OR v_plan.project_id <> v_wia.project_id
     OR v_plan.workflow_run_id <> v_wia.workflow_run_id
     OR v_plan.node_key <> v_wia.node_key
     OR v_plan.target_revision_id <> v_wia.target_revision_id
     OR v_plan.target_revision_content_hash <> v_wia.target_revision_content_hash
     OR v_plan.subject <> v_wia.manifest_subject
     OR v_plan.stage_gate <> v_wia.stage_gate
     OR v_receipt.project_id <> v_wia.project_id
     OR v_receipt.workflow_run_id <> v_wia.workflow_run_id
     OR v_receipt.node_key <> v_wia.node_key
     OR v_receipt.target_revision_id <> v_wia.target_revision_id
     OR v_receipt.target_revision_content_hash <> v_wia.target_revision_content_hash
     OR v_receipt.subject <> v_wia.manifest_subject
     OR v_receipt.stage_gate <> v_wia.stage_gate
     OR v_plan.input_document#>>'{buildManifest,id}' <> v_wia.build_manifest_id::text
     OR v_plan.input_document#>>'{buildManifest,contentHash}' <> v_wia.build_manifest_content_hash
     OR v_plan.input_document#>>'{buildContract,id}' <> v_wia.build_contract_id::text
     OR v_plan.input_document#>>'{buildContract,contentHash}' <> v_wia.build_contract_content_hash THEN
    RAISE EXCEPTION 'Workflow Input, Plan, Receipt, and artifact target bindings differ'
      USING ERRCODE = 'WPV04';
  END IF;

  v_target := pg_catalog.jsonb_build_object(
    'projectId', v_wia.project_id::text,
    'workflowRunId', v_wia.workflow_run_id::text,
    'nodeRunId', v_wia.node_run_id::text,
    'nodeKey', v_wia.node_key,
    'artifactId', v_wia.target_artifact_id::text,
    'revisionId', v_wia.target_revision_id::text,
    'revisionContentHash', v_wia.target_revision_content_hash,
    'subject', v_wia.manifest_subject,
    'stageGate', v_wia.stage_gate
  );
  v_event_set_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-evidence-event-set/v2',
    'orchestrationId', v_plan.orchestration_id::text,
    'headVersion', v_head.version,
    'events', v_events
  );
  v_event_set_bytes := workflow_input_canonical_jsonb_bytes(v_event_set_document);
  v_event_set_hash := qualification_promotion_v2_hash(
    'worksflow.qualification-promotion.evidence-event-set/v2', v_event_set_bytes
  );

  v_closure_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-closure/v2',
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityId', v_wia.authority_id::text,
      'authorityHash', v_wia.authority_hash,
      'inputHash', v_wia.input_hash,
      'targetHash', v_wia.target_hash,
      'qualificationPolicyAuthorityId', v_wia.qualification_policy_authority_id::text,
      'qualificationPolicyAuthorityHash', v_wia.qualification_policy_authority_hash
    ),
    'inputPrecommit', pg_catalog.jsonb_build_object(
      'kind', 'qualification-input-precommit',
      'authorityId', v_input_precommit.authority_id::text,
      'authorityHash', v_input_precommit.authority_hash,
      'workflowInputAuthorityId', v_input_precommit.workflow_input_authority_id::text,
      'workflowInputAuthorityHash', v_input_precommit.workflow_input_authority_hash,
      'qualificationPolicyAuthorityId', v_input_precommit.qualification_policy_authority_id::text,
      'qualificationPolicyAuthorityHash', v_input_precommit.qualification_policy_authority_hash,
      'qualificationPlanAuthorityId', v_input_precommit.qualification_plan_authority_id::text,
      'qualificationPlanAuthorityHash', v_input_precommit.qualification_plan_authority_hash,
      'sourceRequestHash', v_input_precommit.source_request_hash,
      'sourceReceiptHash', v_input_precommit.source_receipt_hash,
      'sourceAdmissionHash', v_input_precommit.source_admission_hash,
      'credentialRequestHash', v_input_precommit.credential_request_hash,
      'credentialReceiptHash', v_input_precommit.credential_receipt_hash,
      'credentialAdmissionHash', v_input_precommit.credential_admission_hash
    ),
    'plan', pg_catalog.jsonb_build_object(
      'authorityId', v_plan.authority_id::text,
      'authorityHash', v_plan.envelope_hash,
      'inputAuthorityId', v_plan.input_authority_id::text,
      'inputHash', v_plan.input_hash,
      'projectionHash', v_plan.projection_hash,
      'evidencePlanHash', v_plan.evidence_plan_hash,
      'targetHash', v_plan.target_hash,
      'trustHash', v_plan.trust_hash,
      'orchestrationId', v_plan.orchestration_id::text,
      'qualificationRunId', v_plan.qualification_run_id::text
    ),
    'evidence', pg_catalog.jsonb_build_object(
      'headVersion', v_head.version,
      'phase', v_head.phase,
      'lastEventId', v_head.last_event_id::text,
      'lastEventHash', v_last_event.event_hash,
      'commandHash', v_head.command_hash,
      'trustBindingsDigest', v_head.trust_bindings_digest,
      'evidenceClosureDigest', v_receipt.evidence_closure_digest,
      'artifactIndexDigest', v_receipt.artifact_index_digest,
      'eventSetDigest', v_event_set_hash
    ),
    'receipt', pg_catalog.jsonb_build_object(
      'receiptId', v_receipt.receipt_id,
      'envelopeHash', v_receipt.envelope_hash,
      'payloadHash', v_receipt.payload_hash,
      'paeHash', v_receipt.pae_hash,
      'completionHash', v_receipt.completion_hash,
      'snapshotRequestHash', v_receipt.snapshot_request_hash,
      'snapshotObservationHash', v_receipt.snapshot_observation_hash,
      'verificationRequestHash', v_receipt.verification_request_hash,
      'verificationObservationHash', v_receipt.verification_observation_hash,
      'runnerRequestHash', v_receipt.runner_request_hash,
      'runnerObservationHash', v_receipt.runner_observation_hash,
      'approverRequestHash', v_receipt.approver_request_hash,
      'approverObservationHash', v_receipt.approver_observation_hash
    ),
    'target', v_target,
    'independentAuthorities', '[]'::jsonb
  );
  v_closure_bytes := workflow_input_canonical_jsonb_bytes(v_closure_document);
  v_closure_hash := qualification_promotion_v2_hash(
    'worksflow.qualification-promotion.closure/v2', v_closure_bytes
  );
  v_revision_intent_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-revision-intent/v2',
    'requestHash', v_request_hash,
    'closureHash', v_closure_hash,
    'outputRevisionId', p_output_revision_id::text,
    'revisionKind', 'external-qualification-promotion/v2',
    'target', v_target,
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityId', v_wia.authority_id::text, 'authorityHash', v_wia.authority_hash
    ),
    'plan', pg_catalog.jsonb_build_object(
      'authorityId', v_plan.authority_id::text, 'authorityHash', v_plan.envelope_hash
    ),
    'receipt', pg_catalog.jsonb_build_object(
      'receiptId', v_receipt.receipt_id, 'envelopeHash', v_receipt.envelope_hash
    )
  );
  v_revision_intent_bytes := workflow_input_canonical_jsonb_bytes(v_revision_intent_document);
  v_revision_intent_hash := qualification_promotion_v2_hash(
    'worksflow.qualification-promotion.revision-intent/v2', v_revision_intent_bytes
  );

  v_now := pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp());
  v_consumption_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-consumption/v2',
    'operationId', p_operation_id::text,
    'requestHash', v_request_hash,
    'closureHash', v_closure_hash,
    'revisionIntentHash', v_revision_intent_hash,
    'consumedAt', qualification_promotion_v2_timestamp(v_now)
  );
  v_consumption_bytes := workflow_input_canonical_jsonb_bytes(v_consumption_document);
  v_consumption_hash := qualification_promotion_v2_hash(
    'worksflow.qualification-promotion.consumption/v2', v_consumption_bytes
  );
  v_handoff_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-handoff/v2',
    'handoffId', p_handoff_id::text,
    'operationId', p_operation_id::text,
    'state', 'pending',
    'outputRevisionId', p_output_revision_id::text,
    'revisionIntentHash', v_revision_intent_hash,
    'workflowInputAuthorityId', p_workflow_input_authority_id::text,
    'planAuthorityId', p_plan_authority_id::text,
    'receiptId', v_receipt.receipt_id,
    'consumptionHash', v_consumption_hash,
    'target', v_target,
    'createdAt', qualification_promotion_v2_timestamp(v_now)
  );
  v_handoff_bytes := workflow_input_canonical_jsonb_bytes(v_handoff_document);
  v_handoff_hash := qualification_promotion_v2_hash(
    'worksflow.qualification-promotion.handoff/v2', v_handoff_bytes
  );

  INSERT INTO qualification_promotion_v2_consumptions(
    operation_id, workflow_input_authority_id, plan_authority_id,
    input_precommit_authority_id, receipt_id,
    request_hash, request_bytes, request_document,
    evidence_event_set_hash, evidence_event_set_bytes, evidence_event_set_document,
    closure_hash, closure_bytes, closure_document,
    revision_intent_hash, revision_intent_bytes, revision_intent_document,
    consumption_hash, consumption_bytes, consumption_document,
    project_id, workflow_run_id, node_run_id, node_key, target_artifact_id,
    target_revision_id, target_revision_content_hash, subject, stage_gate, consumed_at
  ) VALUES (
    p_operation_id, p_workflow_input_authority_id, p_plan_authority_id,
    v_input_precommit.authority_id, v_receipt.receipt_id,
    v_request_hash, v_request_bytes, v_request_document,
    v_event_set_hash, v_event_set_bytes, v_event_set_document,
    v_closure_hash, v_closure_bytes, v_closure_document,
    v_revision_intent_hash, v_revision_intent_bytes, v_revision_intent_document,
    v_consumption_hash, v_consumption_bytes, v_consumption_document,
    v_wia.project_id, v_wia.workflow_run_id, v_wia.node_run_id, v_wia.node_key,
    v_wia.target_artifact_id, v_wia.target_revision_id,
    v_wia.target_revision_content_hash, v_wia.manifest_subject, v_wia.stage_gate, v_now
  );
  INSERT INTO qualification_promotion_v2_identity_reservations(
    identity_value, operation_id, identity_kind, ordinal, reserved_at
  ) VALUES
    (p_operation_id, p_operation_id, 'operation', 0, v_now),
    (p_handoff_id, p_operation_id, 'handoff', 1, v_now),
    (p_output_revision_id, p_operation_id, 'output-revision', 2, v_now);
  INSERT INTO artifact_revision_identity_reservations(
    id, owner_kind, owner_operation_id, reserved_at
  ) VALUES (
    p_output_revision_id, 'qualification-promotion-v2', p_operation_id, v_now
  );
  INSERT INTO qualification_promotion_v2_handoffs(
    handoff_id, operation_id, state, output_revision_id,
    revision_intent_hash, consumption_hash,
    handoff_hash, handoff_bytes, handoff_document, created_at
  ) VALUES (
    p_handoff_id, p_operation_id, 'pending', p_output_revision_id,
    v_revision_intent_hash, v_consumption_hash,
    v_handoff_hash, v_handoff_bytes, v_handoff_document, v_now
  );

  IF qualification_promotion_v2_store_record_is_exact(p_operation_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Promotion v2 just-inserted aggregate failed exact reload'
      USING ERRCODE = 'WPV02';
  END IF;
  RETURN NEXT qualification_promotion_v2_store_bundle(p_operation_id, true, false);
  RETURN;
EXCEPTION
  WHEN unique_violation OR foreign_key_violation OR check_violation OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Promotion v2 immutable identity or byte conflict'
      USING ERRCODE = 'WPV02';
  WHEN serialization_failure OR deadlock_detected THEN
    RAISE;
  WHEN invalid_text_representation OR numeric_value_out_of_range OR datetime_field_overflow THEN
    RAISE EXCEPTION 'Qualification Promotion v2 resolved authority is malformed'
      USING ERRCODE = 'WPV01';
END;
$function$;

-- Execute the posture transition only after every referenced helper/API
-- exists, then remove the migration-local coordinator itself.
SELECT qualification_promotion_v2_apply_security();
DROP FUNCTION qualification_promotion_v2_apply_security();
