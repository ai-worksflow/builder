-- Qualification Promotion v2 Handoff is the only database transition allowed
-- to materialize a consumed Promotion as a same-content Workspace Revision and
-- complete the frozen workflow-engine/v3 external qualification gate.
-- It stops at waiting_input and never fabricates an authenticated 'ActionPublish'.
-- Rolling WIA/Policy/Promotion DDL takes the exclusive side of this fence.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);

-- DDL follows the runtime project/workflow/artifact/Promotion order.  The
-- locks also make the dispatch backfill and guard replacement one cutover.
LOCK TABLE projects IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_run_events IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_definition_versions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifacts IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_revisions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_revision_sources IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_dependencies IN ACCESS EXCLUSIVE MODE;
LOCK TABLE trace_links IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_consumptions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_handoffs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_promotion_v2_identity_reservations IN ACCESS EXCLUSIVE MODE;
LOCK TABLE artifact_revision_identity_reservations IN ACCESS EXCLUSIVE MODE;
LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE;

CREATE FUNCTION qualification_handoff_v1_hash(p_domain text, p_value bytea)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT 'sha256:' || pg_catalog.encode(
    pg_catalog.sha256(
      pg_catalog.convert_to('worksflow-qualification-handoff-hash/v1', 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || pg_catalog.convert_to(p_domain, 'UTF8')
      || pg_catalog.decode('00', 'hex')
      || p_value
    ),
    'hex'
  )
$function$;

CREATE FUNCTION qualification_handoff_v1_timestamp(p_value timestamptz)
RETURNS text
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
  SELECT pg_catalog.to_char(
    p_value AT TIME ZONE 'UTC',
    'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
  )
$function$;

-- The grant is an owner-only transaction rendezvous.  It is inserted by the
-- completion function and atomically deleted by the artifact Revision trigger.
CREATE TABLE qualification_promotion_v2_revision_transaction_grants (
  output_revision_id uuid PRIMARY KEY,
  handoff_id uuid NOT NULL UNIQUE,
  operation_id uuid NOT NULL UNIQUE,
  backend_pid integer NOT NULL CHECK (backend_pid > 0),
  transaction_id text NOT NULL CHECK (
    pg_catalog.octet_length(transaction_id) BETWEEN 1 AND 64
  ),
  granted_at timestamptz NOT NULL CHECK (
    granted_at = pg_catalog.date_trunc('milliseconds', granted_at)
  ),
  CHECK (
    workflow_input_uuid_is_exact(output_revision_id::text)
    AND workflow_input_uuid_is_exact(handoff_id::text)
    AND workflow_input_uuid_is_exact(operation_id::text)
    AND output_revision_id <> handoff_id
    AND output_revision_id <> operation_id
    AND handoff_id <> operation_id
  ),
  FOREIGN KEY (handoff_id) REFERENCES qualification_promotion_v2_handoffs(handoff_id)
    ON DELETE RESTRICT,
  FOREIGN KEY (operation_id) REFERENCES qualification_promotion_v2_consumptions(operation_id)
    ON DELETE RESTRICT
);

CREATE TABLE qualification_promotion_v2_revision_authority_bindings (
  handoff_id uuid PRIMARY KEY
    REFERENCES qualification_promotion_v2_handoffs(handoff_id) ON DELETE RESTRICT,
  operation_id uuid NOT NULL UNIQUE
    REFERENCES qualification_promotion_v2_consumptions(operation_id) ON DELETE RESTRICT,
  output_revision_id uuid NOT NULL UNIQUE,
  workflow_input_authority_id uuid NOT NULL,
  workflow_input_authority_hash text NOT NULL CHECK (
    workflow_input_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  plan_authority_id uuid NOT NULL,
  plan_authority_hash text NOT NULL CHECK (
    plan_authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  receipt_id text NOT NULL,
  receipt_envelope_hash text NOT NULL CHECK (
    receipt_envelope_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  promotion_request_hash text NOT NULL CHECK (
    promotion_request_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  promotion_closure_hash text NOT NULL CHECK (
    promotion_closure_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  promotion_revision_intent_hash text NOT NULL CHECK (
    promotion_revision_intent_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  promotion_consumption_hash text NOT NULL CHECK (
    promotion_consumption_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  target_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(target_document) = 'object'
  ),
  authority_hash text NOT NULL UNIQUE CHECK (
    authority_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  authority_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(authority_bytes) BETWEEN 1 AND 1048576
  ),
  authority_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(authority_document) = 'object'
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  created_at timestamptz NOT NULL CHECK (
    created_at = pg_catalog.date_trunc('milliseconds', created_at)
  ),
  CHECK (
    workflow_input_uuid_is_exact(handoff_id::text)
    AND workflow_input_uuid_is_exact(operation_id::text)
    AND workflow_input_uuid_is_exact(output_revision_id::text)
    AND workflow_input_uuid_is_exact(workflow_input_authority_id::text)
    AND workflow_input_uuid_is_exact(plan_authority_id::text)
  ),
  FOREIGN KEY (workflow_input_authority_id)
    REFERENCES workflow_input_authorities(authority_id) ON DELETE RESTRICT,
  FOREIGN KEY (plan_authority_id)
    REFERENCES qualification_plan_authorities(authority_id) ON DELETE RESTRICT,
  FOREIGN KEY (receipt_id)
    REFERENCES qualification_receipt_v3_receipts(receipt_id) ON DELETE RESTRICT,
  FOREIGN KEY (output_revision_id)
    REFERENCES artifact_revisions(id) ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED
);

-- Each copied source/dependency/trace member is frozen independently. The
-- authority document binds only a bounded count/root summary, so a legitimate
-- large trace metadata value can never overflow the authority envelope.
CREATE TABLE qualification_promotion_v2_handoff_lineage_members (
  handoff_id uuid NOT NULL
    REFERENCES qualification_promotion_v2_handoffs(handoff_id) ON DELETE RESTRICT,
  member_kind text NOT NULL CHECK (
    member_kind IN ('source','dependency','trace')
  ),
  member_ordinal bigint NOT NULL CHECK (
    member_ordinal BETWEEN 0 AND 9007199254740991
  ),
  member_key text NOT NULL CHECK (
    pg_catalog.octet_length(member_key) BETWEEN 1 AND 64
  ),
  row_hash text NOT NULL CHECK (
    row_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  CONSTRAINT qualification_handoff_lineage_members_pkey PRIMARY KEY (
    handoff_id, member_kind, member_key
  ),
  CONSTRAINT qualification_handoff_lineage_members_ordinal_key UNIQUE (
    handoff_id, member_kind, member_ordinal
  ),
  CHECK (
    workflow_input_uuid_is_exact(handoff_id::text)
    AND (
      (member_kind = 'source' AND member_key ~ '^[0-9]{10}$')
      OR (member_kind IN ('dependency','trace')
          AND workflow_input_uuid_is_exact(member_key))
    )
  )
);

CREATE TABLE qualification_promotion_v2_handoff_completions (
  handoff_id uuid PRIMARY KEY
    REFERENCES qualification_promotion_v2_handoffs(handoff_id) ON DELETE RESTRICT,
  operation_id uuid NOT NULL UNIQUE,
  consumption_hash text NOT NULL UNIQUE CHECK (
    consumption_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  output_revision_id uuid NOT NULL UNIQUE,
  output_revision_content_hash text NOT NULL CHECK (
    output_revision_content_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  project_id uuid NOT NULL,
  workflow_run_id uuid NOT NULL,
  node_run_id uuid NOT NULL,
  node_key text NOT NULL CHECK (
    pg_catalog.octet_length(node_key) BETWEEN 1 AND 256
  ),
  publish_node_run_id uuid NOT NULL,
  publish_node_key text NOT NULL CHECK (
    pg_catalog.octet_length(publish_node_key) BETWEEN 1 AND 256
  ),
  event_cursor_before bigint NOT NULL CHECK (
    event_cursor_before BETWEEN 0 AND 9007199254740989
  ),
  event_cursor_after bigint NOT NULL CHECK (
    event_cursor_after = event_cursor_before + 2
  ),
  gate_output_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(gate_output_document) = 'object'
  ),
  gate_completed_event_id uuid NOT NULL UNIQUE,
  publish_authorization_event_id uuid NOT NULL UNIQUE,
  completion_hash text NOT NULL UNIQUE CHECK (
    completion_hash ~ '^sha256:[0-9a-f]{64}$'
  ),
  completion_bytes bytea NOT NULL CHECK (
    pg_catalog.octet_length(completion_bytes) BETWEEN 1 AND 1048576
  ),
  completion_document jsonb NOT NULL CHECK (
    pg_catalog.jsonb_typeof(completion_document) = 'object'
  ),
  creation_transaction_id text NOT NULL CHECK (
    creation_transaction_id ~ '^[1-9][0-9]{0,19}$'
  ),
  completed_at timestamptz NOT NULL CHECK (
    completed_at = pg_catalog.date_trunc('milliseconds', completed_at)
  ),
  CHECK (
    workflow_input_uuid_is_exact(handoff_id::text)
    AND workflow_input_uuid_is_exact(operation_id::text)
    AND workflow_input_uuid_is_exact(output_revision_id::text)
    AND workflow_input_uuid_is_exact(project_id::text)
    AND workflow_input_uuid_is_exact(workflow_run_id::text)
    AND workflow_input_uuid_is_exact(node_run_id::text)
    AND workflow_input_uuid_is_exact(publish_node_run_id::text)
    AND workflow_input_uuid_is_exact(gate_completed_event_id::text)
    AND workflow_input_uuid_is_exact(publish_authorization_event_id::text)
    AND gate_completed_event_id <> publish_authorization_event_id
    AND node_run_id <> publish_node_run_id
  ),
  FOREIGN KEY (operation_id, consumption_hash)
    REFERENCES qualification_promotion_v2_consumptions(operation_id, consumption_hash)
    ON DELETE RESTRICT,
  FOREIGN KEY (output_revision_id)
    REFERENCES artifact_revisions(id) ON DELETE RESTRICT,
  FOREIGN KEY (workflow_run_id, project_id)
    REFERENCES workflow_runs(id, project_id) ON DELETE RESTRICT,
  FOREIGN KEY (node_run_id, workflow_run_id, node_key)
    REFERENCES workflow_node_runs(id, run_id, node_key) ON DELETE RESTRICT
);

-- The second event has sequence event_cursor_after, while gate completion is
-- event_cursor_after-1; retain an explicit FK for the terminal member.
ALTER TABLE qualification_promotion_v2_handoff_completions
  ADD CONSTRAINT qualification_handoff_publish_event_fk FOREIGN KEY (
    publish_authorization_event_id, workflow_run_id, event_cursor_after
  ) REFERENCES workflow_run_events(id, run_id, sequence)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE artifact_revisions
  ADD COLUMN promotion_handoff_id uuid,
  ADD CONSTRAINT artifact_revisions_promotion_handoff_fk FOREIGN KEY (
    promotion_handoff_id
  ) REFERENCES qualification_promotion_v2_handoffs(handoff_id) ON DELETE RESTRICT,
  ADD CONSTRAINT artifact_revisions_promotion_handoff_unique UNIQUE (
    promotion_handoff_id
  );

ALTER TABLE artifact_revisions
  DROP CONSTRAINT artifact_revisions_artifact_id_content_hash_key;

CREATE UNIQUE INDEX artifact_revisions_ordinary_content_unique
  ON artifact_revisions(artifact_id, content_hash)
  WHERE promotion_handoff_id IS NULL;

-- Keep the base immutable-content rule and add the Handoff discriminator.
CREATE OR REPLACE FUNCTION reject_artifact_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
DECLARE
  v_is_handoff_parent boolean := false;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'artifact revisions cannot be deleted';
  END IF;
  IF NEW.artifact_id IS DISTINCT FROM OLD.artifact_id
    OR NEW.revision_number IS DISTINCT FROM OLD.revision_number
    OR NEW.parent_revision_id IS DISTINCT FROM OLD.parent_revision_id
    OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
    OR NEW.content_store IS DISTINCT FROM OLD.content_store
    OR NEW.content_ref IS DISTINCT FROM OLD.content_ref
    OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
    OR NEW.byte_size IS DISTINCT FROM OLD.byte_size
    OR NEW.change_source IS DISTINCT FROM OLD.change_source
    OR NEW.change_summary IS DISTINCT FROM OLD.change_summary
    OR NEW.source_manifest_id IS DISTINCT FROM OLD.source_manifest_id
    OR NEW.proposal_id IS DISTINCT FROM OLD.proposal_id
    OR NEW.implementation_proposal_id IS DISTINCT FROM OLD.implementation_proposal_id
    OR NEW.promotion_handoff_id IS DISTINCT FROM OLD.promotion_handoff_id
    OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'artifact revision content is immutable';
  END IF;
  SELECT EXISTS (
    SELECT 1 FROM artifact_revisions AS child
    WHERE child.parent_revision_id = OLD.id
      AND child.promotion_handoff_id IS NOT NULL
  ) INTO v_is_handoff_parent;
  IF OLD.promotion_handoff_id IS NOT NULL OR v_is_handoff_parent THEN
    IF NEW.approved_at IS DISTINCT FROM OLD.approved_at
       OR (OLD.workflow_status = 'approved' AND (
         (NEW.workflow_status = 'approved'
          AND NEW.superseded_at IS DISTINCT FROM OLD.superseded_at)
         OR (NEW.workflow_status = 'superseded' AND (
           OLD.superseded_at IS NOT NULL
           OR NEW.superseded_at IS NULL
         ))
         OR NEW.workflow_status NOT IN ('approved','superseded')
       ))
       OR (OLD.workflow_status = 'superseded' AND (
         NEW.workflow_status <> 'superseded'
         OR NEW.superseded_at IS DISTINCT FROM OLD.superseded_at
       ))
       OR OLD.workflow_status NOT IN ('approved','superseded') THEN
      RAISE EXCEPTION 'Qualification Handoff Revision lifecycle is monotonic'
        USING ERRCODE = 'WPH02';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

-- Replace migration-81's reservation trigger. Ordinary rows still reserve
-- their own ID. A Promotion-owned ID is admitted only while this exact backend
-- and transaction atomically consume the private Handoff grant.
CREATE OR REPLACE FUNCTION reserve_ordinary_artifact_revision_identity_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_owner artifact_revision_identity_reservations%ROWTYPE;
  v_grant qualification_promotion_v2_revision_transaction_grants%ROWTYPE;
BEGIN
  IF NEW.promotion_handoff_id IS NULL THEN
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
          USING ERRCODE = 'WPH02';
      END IF;
    END IF;
    RETURN NEW;
  END IF;

  DELETE FROM qualification_promotion_v2_revision_transaction_grants
  WHERE output_revision_id = NEW.id
    AND handoff_id = NEW.promotion_handoff_id
    AND backend_pid = pg_catalog.pg_backend_pid()
    AND transaction_id = pg_catalog.pg_current_xact_id()::text
  RETURNING * INTO v_grant;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'same-content Promotion Revision has no exact transaction grant'
      USING ERRCODE = 'WPH02';
  END IF;
  SELECT * INTO v_owner
  FROM artifact_revision_identity_reservations
  WHERE id = NEW.id
  FOR SHARE;
  IF v_owner.id IS NULL
     OR v_owner.owner_kind <> 'qualification-promotion-v2'
     OR v_owner.owner_operation_id <> v_grant.operation_id
     OR NOT EXISTS (
       SELECT 1 FROM qualification_promotion_v2_handoffs AS handoff
       WHERE handoff.handoff_id = NEW.promotion_handoff_id
         AND handoff.operation_id = v_grant.operation_id
         AND handoff.output_revision_id = NEW.id
         AND handoff.state = 'pending'
     ) THEN
    RAISE EXCEPTION 'same-content Promotion Revision reservation is not exact'
      USING ERRCODE = 'WPH02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE FUNCTION reject_qualification_handoff_v1_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $function$
BEGIN
  IF TG_TABLE_NAME = 'outbox_events' THEN
    IF OLD.event_type <> 'qualification.promotion_handoff.pending'
       AND NOT EXISTS (
         SELECT 1 FROM qualification_promotion_v2_handoff_completions
         WHERE gate_completed_event_id = OLD.id
            OR publish_authorization_event_id = OLD.id
       ) THEN
      IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
      RAISE EXCEPTION 'Qualification Handoff Outbox authority is immutable'
        USING ERRCODE = 'WPH02';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.aggregate_type IS DISTINCT FROM OLD.aggregate_type
       OR NEW.aggregate_id IS DISTINCT FROM OLD.aggregate_id
       OR NEW.event_type IS DISTINCT FROM OLD.event_type
       OR NEW.subject IS DISTINCT FROM OLD.subject
       OR NEW.payload IS DISTINCT FROM OLD.payload
       OR NEW.headers IS DISTINCT FROM OLD.headers
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
      RAISE EXCEPTION 'Qualification Handoff Outbox authority is immutable'
        USING ERRCODE = 'WPH02';
    END IF;
    IF OLD.event_type = 'qualification.promotion_handoff.pending'
       AND OLD.published_at IS NULL AND NEW.published_at IS NOT NULL
       AND NOT EXISTS (
         SELECT 1
         FROM qualification_promotion_v2_handoff_completions AS completion
         WHERE completion.handoff_id::text = OLD.aggregate_id
           AND qualification_handoff_v1_completion_is_exact(
             completion.handoff_id
           ) IS TRUE
       ) THEN
      RAISE EXCEPTION 'Qualification Handoff dispatch cannot acknowledge before exact completion inspection'
        USING ERRCODE = 'WPH03';
    END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'Qualification Handoff authority and completion ledgers are immutable'
    USING ERRCODE = 'WPH02';
END;
$function$;

CREATE TRIGGER qualification_handoff_revision_authorities_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_promotion_v2_revision_authority_bindings
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_handoff_v1_mutation();

CREATE TRIGGER qualification_handoff_lineage_members_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_promotion_v2_handoff_lineage_members
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_handoff_v1_mutation();

CREATE TRIGGER qualification_handoff_completions_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE
ON qualification_promotion_v2_handoff_completions
FOR EACH STATEMENT EXECUTE FUNCTION reject_qualification_handoff_v1_mutation();

CREATE TRIGGER qualification_handoff_outbox_immutable
BEFORE UPDATE OR DELETE ON outbox_events
FOR EACH ROW EXECUTE FUNCTION reject_qualification_handoff_v1_mutation();

-- A pending dispatch contains no caller-authored authority. The immutable
-- handoff remains the sole source of every resolved fact.
CREATE UNIQUE INDEX qualification_promotion_handoff_pending_dispatch_unique
  ON outbox_events(event_type, aggregate_id)
  WHERE event_type = 'qualification.promotion_handoff.pending';

CREATE FUNCTION enqueue_qualification_promotion_v2_handoff_v1()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
BEGIN
  INSERT INTO outbox_events(
    id, aggregate_type, aggregate_id, event_type, subject, payload, headers,
    attempts, available_at, published_at, last_error, created_at
  ) VALUES (
    pg_catalog.gen_random_uuid(), 'qualification_promotion_v2_handoff',
    NEW.handoff_id::text, 'qualification.promotion_handoff.pending',
    'worksflow.qualification.promotion-handoff.pending',
    pg_catalog.jsonb_build_object('handoffId', NEW.handoff_id::text),
    '{}'::jsonb, 0, NEW.created_at, NULL, NULL, NEW.created_at
  );
  RETURN NEW;
END;
$function$;

CREATE TRIGGER qualification_promotion_v2_handoff_pending_dispatch
AFTER INSERT ON qualification_promotion_v2_handoffs
FOR EACH ROW EXECUTE FUNCTION enqueue_qualification_promotion_v2_handoff_v1();

INSERT INTO outbox_events(
  id, aggregate_type, aggregate_id, event_type, subject, payload, headers,
  attempts, available_at, published_at, last_error, created_at
)
SELECT
  pg_catalog.gen_random_uuid(), 'qualification_promotion_v2_handoff',
  handoff.handoff_id::text, 'qualification.promotion_handoff.pending',
  'worksflow.qualification.promotion-handoff.pending',
  pg_catalog.jsonb_build_object('handoffId', handoff.handoff_id::text),
  '{}'::jsonb, 0, handoff.created_at, NULL, NULL, handoff.created_at
FROM qualification_promotion_v2_handoffs AS handoff
LEFT JOIN qualification_promotion_v2_handoff_completions AS completion
  ON completion.handoff_id = handoff.handoff_id
WHERE handoff.state = 'pending' AND completion.handoff_id IS NULL
ORDER BY handoff.created_at, handoff.handoff_id
ON CONFLICT DO NOTHING;

DO $qualification_handoff_v1_dispatch_backfill_exact$
BEGIN
  IF EXISTS (
       SELECT 1 FROM qualification_promotion_v2_handoffs AS handoff
       LEFT JOIN qualification_promotion_v2_handoff_completions AS completion
         ON completion.handoff_id = handoff.handoff_id
       WHERE handoff.state = 'pending'
         AND completion.handoff_id IS NULL
         AND (SELECT pg_catalog.count(*) FROM outbox_events AS outbox
              WHERE outbox.aggregate_type = 'qualification_promotion_v2_handoff'
                AND outbox.aggregate_id = handoff.handoff_id::text
                AND outbox.event_type = 'qualification.promotion_handoff.pending'
                AND outbox.subject = 'worksflow.qualification.promotion-handoff.pending'
                AND outbox.payload = pg_catalog.jsonb_build_object(
                  'handoffId', handoff.handoff_id::text
                )
                AND outbox.headers = '{}'::jsonb
                AND outbox.attempts = 0
                AND outbox.available_at = handoff.created_at
                AND outbox.published_at IS NULL
                AND outbox.last_error IS NULL
                AND outbox.created_at = handoff.created_at) <> 1
     )
     OR EXISTS (
       SELECT 1 FROM outbox_events AS outbox
       WHERE outbox.event_type = 'qualification.promotion_handoff.pending'
         AND NOT EXISTS (
           SELECT 1 FROM qualification_promotion_v2_handoffs AS handoff
           WHERE handoff.handoff_id::text = outbox.aggregate_id
         )
     ) THEN
    RAISE EXCEPTION 'Qualification Handoff pending-dispatch backfill is not exact'
      USING ERRCODE = 'WPH02';
  END IF;
END;
$qualification_handoff_v1_dispatch_backfill_exact$;

CREATE FUNCTION qualification_handoff_v1_quality_result(
  p_workflow_input_authority_id uuid,
  p_output_revision_id uuid
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE STRICT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_wia workflow_input_authorities%ROWTYPE;
  v_input jsonb;
  v_result jsonb;
BEGIN
  SELECT * INTO STRICT v_wia FROM workflow_input_authorities
  WHERE authority_id = p_workflow_input_authority_id;
  v_input := pg_catalog.convert_from(v_wia.node_input_raw_bytes, 'UTF8')::jsonb;
  IF pg_catalog.jsonb_typeof(v_input->'bindings') IS DISTINCT FROM 'array' THEN
    RAISE EXCEPTION 'Qualification Handoff requires one frozen QualityResult input'
      USING ERRCODE = 'WPH02';
  END IF;
  IF pg_catalog.jsonb_array_length(v_input->'bindings') IS DISTINCT FROM 1
     OR pg_catalog.jsonb_typeof(v_input#>'{bindings,0,output}')
       IS DISTINCT FROM 'object' THEN
    RAISE EXCEPTION 'Qualification Handoff requires one frozen QualityResult input'
      USING ERRCODE = 'WPH02';
  END IF;
  v_result := v_input#>'{bindings,0,output}';
  IF v_result->'passed' IS DISTINCT FROM 'true'::jsonb
     OR v_result->>'qualityRunId' IS DISTINCT FROM v_wia.quality_run_id::text
     OR v_result#>>'{workspaceRevision,artifactId}'
       IS DISTINCT FROM v_wia.target_artifact_id::text
     OR v_result#>>'{workspaceRevision,revisionId}'
       IS DISTINCT FROM v_wia.target_revision_id::text
     OR workflow_input_normalize_sha256(
       v_result#>>'{workspaceRevision,contentHash}'
     ) IS DISTINCT FROM v_wia.target_revision_content_hash
     OR pg_catalog.jsonb_typeof(v_result->'buildManifest')
       IS DISTINCT FROM 'object' THEN
    RAISE EXCEPTION 'Qualification Handoff frozen QualityResult disagrees with WIA'
      USING ERRCODE = 'WPH02';
  END IF;
  RETURN pg_catalog.jsonb_set(
    v_result, '{workspaceRevision,revisionId}',
    pg_catalog.to_jsonb(p_output_revision_id::text), false
  );
EXCEPTION
  WHEN invalid_text_representation OR character_not_in_repertoire THEN
    RAISE EXCEPTION 'Qualification Handoff frozen QualityResult is malformed'
      USING ERRCODE = 'WPH02';
END;
$function$;

CREATE FUNCTION qualification_handoff_v1_completion_is_exact(p_handoff_id uuid)
RETURNS boolean
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
  v_binding qualification_promotion_v2_revision_authority_bindings%ROWTYPE;
  v_handoff qualification_promotion_v2_handoffs%ROWTYPE;
  v_consumption qualification_promotion_v2_consumptions%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_plan qualification_plan_authorities%ROWTYPE;
  v_receipt qualification_receipt_v3_receipts%ROWTYPE;
  v_output artifact_revisions%ROWTYPE;
  v_parent artifact_revisions%ROWTYPE;
  v_artifact artifacts%ROWTYPE;
  v_gate workflow_node_runs%ROWTYPE;
  v_gate_event workflow_run_events%ROWTYPE;
  v_publish_event workflow_run_events%ROWTYPE;
  v_expected_authority jsonb;
  v_expected_completion jsonb;
  v_expected_events jsonb;
  v_expected_outbox jsonb;
  v_member record;
  v_member_document jsonb;
  v_expected_member_hash text;
  v_lineage_root_hash text;
  v_source_count bigint;
  v_dependency_count bigint;
  v_trace_count bigint;
BEGIN
  IF p_handoff_id IS NULL
     OR workflow_input_uuid_is_exact(p_handoff_id::text) IS NOT TRUE THEN
    RETURN false;
  END IF;
  SELECT * INTO v_completion
  FROM qualification_promotion_v2_handoff_completions
  WHERE handoff_id = p_handoff_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_binding
  FROM qualification_promotion_v2_revision_authority_bindings
  WHERE handoff_id = p_handoff_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_handoff
  FROM qualification_promotion_v2_handoffs
  WHERE handoff_id = p_handoff_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_consumption
  FROM qualification_promotion_v2_consumptions
  WHERE operation_id = v_handoff.operation_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_wia FROM workflow_input_authorities
  WHERE authority_id = v_consumption.workflow_input_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_plan FROM qualification_plan_authorities
  WHERE authority_id = v_consumption.plan_authority_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_receipt FROM qualification_receipt_v3_receipts
  WHERE receipt_id = v_consumption.receipt_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_output FROM artifact_revisions
  WHERE id = v_completion.output_revision_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_parent FROM artifact_revisions
  WHERE id = v_consumption.target_revision_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_artifact FROM artifacts
  WHERE id = v_consumption.target_artifact_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_gate FROM workflow_node_runs
  WHERE id = v_completion.node_run_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_gate_event FROM workflow_run_events
  WHERE id = v_completion.gate_completed_event_id;
  IF NOT FOUND THEN RETURN false; END IF;
  SELECT * INTO v_publish_event FROM workflow_run_events
  WHERE id = v_completion.publish_authorization_event_id;
  IF NOT FOUND THEN RETURN false; END IF;

  SELECT
    pg_catalog.count(*) FILTER (WHERE member_kind = 'source'),
    pg_catalog.count(*) FILTER (WHERE member_kind = 'dependency'),
    pg_catalog.count(*) FILTER (WHERE member_kind = 'trace')
  INTO v_source_count, v_dependency_count, v_trace_count
  FROM qualification_promotion_v2_handoff_lineage_members
  WHERE handoff_id = p_handoff_id;
  IF EXISTS (
    SELECT 1
    FROM (
      SELECT member_ordinal,
             pg_catalog.row_number() OVER (
               PARTITION BY member_kind
               ORDER BY pg_catalog.convert_to(member_key, 'UTF8')
             ) - 1 AS expected_ordinal
      FROM qualification_promotion_v2_handoff_lineage_members
      WHERE handoff_id = p_handoff_id
    ) AS ordered
    WHERE ordered.member_ordinal <> ordered.expected_ordinal
  ) THEN
    RETURN false;
  END IF;
  v_lineage_root_hash := qualification_handoff_v1_hash(
    'worksflow.qualification-handoff.lineage-root-seed/v1',
    pg_catalog.convert_to(
      'worksflow-qualification-handoff-copied-lineage/v1', 'UTF8'
    )
  );
  FOR v_member IN
    SELECT member_kind, member_ordinal, member_key, row_hash,
           creation_transaction_id
    FROM qualification_promotion_v2_handoff_lineage_members
    WHERE handoff_id = p_handoff_id
    ORDER BY CASE member_kind
      WHEN 'source' THEN 0 WHEN 'dependency' THEN 1 ELSE 2 END,
      pg_catalog.convert_to(member_key, 'UTF8')
  LOOP
    v_member_document := NULL;
    IF v_member.member_kind = 'source' THEN
      SELECT pg_catalog.jsonb_build_object(
        'schemaVersion', 'worksflow-qualification-handoff-lineage-member/v1',
        'memberKind', 'source',
        'memberKey', v_member.member_key,
        'outputRevisionId', v_output.id::text,
        'row', pg_catalog.jsonb_build_object(
          'ordinal', source.ordinal,
          'sourceArtifactId', source.source_artifact_id::text,
          'sourceRevisionId', source.source_revision_id::text,
          'sourceContentHash', source.source_content_hash,
          'sourceAnchorId', source.source_anchor_id,
          'purpose', source.purpose,
          'required', source.required,
          'addedBy', source.added_by::text,
          'addedAt', pg_catalog.to_char(
            source.added_at AT TIME ZONE 'UTC',
            'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
          )
        )
      ) INTO v_member_document
      FROM artifact_revision_sources AS source
      WHERE source.revision_id = v_output.id
        AND pg_catalog.lpad(source.ordinal::text, 10, '0') = v_member.member_key;
    ELSIF v_member.member_kind = 'dependency' THEN
      SELECT pg_catalog.jsonb_build_object(
        'schemaVersion', 'worksflow-qualification-handoff-lineage-member/v1',
        'memberKind', 'dependency',
        'memberKey', v_member.member_key,
        'outputRevisionId', v_output.id::text,
        'row', pg_catalog.jsonb_build_object(
          'id', dependency.id::text,
          'projectId', dependency.project_id::text,
          'sourceArtifactId', dependency.source_artifact_id::text,
          'sourceRevisionId', dependency.source_revision_id::text,
          'sourceContentHash', dependency.source_content_hash,
          'targetArtifactId', dependency.target_artifact_id::text,
          'targetRevisionId', dependency.target_revision_id::text,
          'relation', dependency.relation,
          'required', dependency.required,
          'createdBy', dependency.created_by::text,
          'createdAt', pg_catalog.to_char(
            dependency.created_at AT TIME ZONE 'UTC',
            'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
          )
        )
      ) INTO v_member_document
      FROM artifact_dependencies AS dependency
      WHERE dependency.id = v_member.member_key::uuid
        AND (
          dependency.source_revision_id = v_output.id
          OR dependency.target_revision_id = v_output.id
        );
    ELSE
      SELECT pg_catalog.jsonb_build_object(
        'schemaVersion', 'worksflow-qualification-handoff-lineage-member/v1',
        'memberKind', 'trace',
        'memberKey', v_member.member_key,
        'outputRevisionId', v_output.id::text,
        'row', pg_catalog.jsonb_build_object(
          'id', trace.id::text,
          'projectId', trace.project_id::text,
          'sourceArtifactId', trace.source_artifact_id::text,
          'sourceRevisionId', trace.source_revision_id::text,
          'sourceAnchorId', trace.source_anchor_id,
          'targetArtifactId', trace.target_artifact_id::text,
          'targetRevisionId', trace.target_revision_id::text,
          'targetAnchorId', trace.target_anchor_id,
          'relation', trace.relation,
          'metadata', trace.metadata,
          'createdBy', trace.created_by::text,
          'createdAt', pg_catalog.to_char(
            trace.created_at AT TIME ZONE 'UTC',
            'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
          )
        )
      ) INTO v_member_document
      FROM trace_links AS trace
      WHERE trace.id = v_member.member_key::uuid
        AND (
          trace.source_revision_id = v_output.id
          OR trace.target_revision_id = v_output.id
        );
    END IF;
    IF v_member_document IS NULL
       OR v_member.creation_transaction_id
          <> v_completion.creation_transaction_id THEN
      RETURN false;
    END IF;
    v_expected_member_hash := qualification_handoff_v1_hash(
      'worksflow.qualification-handoff.lineage-member.'
        || v_member.member_kind || '/v1',
      workflow_input_canonical_jsonb_bytes(v_member_document)
    );
    IF v_expected_member_hash <> v_member.row_hash THEN
      RETURN false;
    END IF;
    v_lineage_root_hash := qualification_handoff_v1_hash(
      'worksflow.qualification-handoff.lineage-root-step/v1',
      pg_catalog.convert_to(v_lineage_root_hash, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_member.member_kind, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_member.member_ordinal::text, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_member.member_key, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_member.row_hash, 'UTF8')
    );
  END LOOP;

  v_expected_authority := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-output-revision-authorities/v1',
    'handoffId', v_handoff.handoff_id::text,
    'operationId', v_consumption.operation_id::text,
    'outputRevisionId', v_handoff.output_revision_id::text,
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityId', v_wia.authority_id::text,
      'authorityHash', v_wia.authority_hash
    ),
    'plan', pg_catalog.jsonb_build_object(
      'authorityId', v_plan.authority_id::text,
      'authorityHash', v_plan.envelope_hash
    ),
    'receipt', pg_catalog.jsonb_build_object(
      'receiptId', v_receipt.receipt_id,
      'envelopeHash', v_receipt.envelope_hash
    ),
    'promotion', pg_catalog.jsonb_build_object(
      'requestHash', v_consumption.request_hash,
      'closureHash', v_consumption.closure_hash,
      'revisionIntentHash', v_consumption.revision_intent_hash,
      'consumptionHash', v_consumption.consumption_hash
    ),
    'target', v_consumption.closure_document->'target',
    'revisionStateAtHandoff', pg_catalog.jsonb_build_object(
      'workflowStatus', 'approved',
      'approvedAt', qualification_handoff_v1_timestamp(
        v_completion.completed_at
      ),
      'supersededAt', 'null'::jsonb,
      'parentWorkflowStatus', 'superseded',
      'parentApprovedAt', pg_catalog.to_char(
        v_parent.approved_at AT TIME ZONE 'UTC',
        'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
      ),
      'parentSupersededAt', qualification_handoff_v1_timestamp(
        v_completion.completed_at
      )
    ),
    'copiedLineage', v_binding.authority_document->'copiedLineage'
  );
  v_expected_events := pg_catalog.jsonb_build_array(
    pg_catalog.jsonb_build_object(
      'role', 'gate-completed',
      'eventId', v_completion.gate_completed_event_id::text,
      'eventSequence', v_completion.event_cursor_before + 1,
      'eventType', 'node.completed',
      'nodeRunId', v_completion.node_run_id::text,
      'nodeKey', v_completion.node_key
    ),
    pg_catalog.jsonb_build_object(
      'role', 'publish-authorization-required',
      'eventId', v_completion.publish_authorization_event_id::text,
      'eventSequence', v_completion.event_cursor_after,
      'eventType', 'node.execution_authorization_required',
      'nodeRunId', v_completion.publish_node_run_id::text,
      'nodeKey', v_completion.publish_node_key
    )
  );
  v_expected_outbox := pg_catalog.jsonb_build_array(
    pg_catalog.jsonb_build_object(
      'role', 'gate-completed',
      'outboxEventId', v_completion.gate_completed_event_id::text,
      'workflowEventId', v_completion.gate_completed_event_id::text,
      'eventType', 'node.completed'
    ),
    pg_catalog.jsonb_build_object(
      'role', 'publish-authorization-required',
      'outboxEventId', v_completion.publish_authorization_event_id::text,
      'workflowEventId', v_completion.publish_authorization_event_id::text,
      'eventType', 'node.execution_authorization_required'
    )
  );
  v_expected_completion := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-handoff-completion/v1',
    'handoffId', v_handoff.handoff_id::text,
    'operationId', v_consumption.operation_id::text,
    'consumptionHash', v_consumption.consumption_hash,
    'outputRevisionId', v_output.id::text,
    'outputRevisionContentHash', v_output.content_hash,
    'projectId', v_consumption.project_id::text,
    'workflowRunId', v_consumption.workflow_run_id::text,
    'nodeRunId', v_consumption.node_run_id::text,
    'nodeKey', v_consumption.node_key,
    'publishNodeRunId', v_completion.publish_node_run_id::text,
    'workflowEvents', v_expected_events,
    'outboxEvents', v_expected_outbox,
    'completedAt', qualification_handoff_v1_timestamp(v_completion.completed_at)
  );

  RETURN
    qualification_promotion_v2_store_record_is_exact(v_consumption.operation_id) IS TRUE
    AND workflow_input_authority_record_is_exact(v_wia.authority_id) IS TRUE
    AND v_binding.operation_id = v_consumption.operation_id
    AND v_binding.output_revision_id = v_handoff.output_revision_id
    AND v_binding.workflow_input_authority_id = v_wia.authority_id
    AND v_binding.workflow_input_authority_hash = v_wia.authority_hash
    AND v_binding.plan_authority_id = v_plan.authority_id
    AND v_binding.plan_authority_hash = v_plan.envelope_hash
    AND v_binding.receipt_id = v_receipt.receipt_id
    AND v_binding.receipt_envelope_hash = v_receipt.envelope_hash
    AND v_binding.promotion_request_hash = v_consumption.request_hash
    AND v_binding.promotion_closure_hash = v_consumption.closure_hash
    AND v_binding.promotion_revision_intent_hash = v_consumption.revision_intent_hash
    AND v_binding.promotion_consumption_hash = v_consumption.consumption_hash
    AND v_binding.target_document = v_consumption.closure_document->'target'
    AND v_binding.authority_document = v_expected_authority
    AND workflow_input_canonical_jsonb_bytes(v_binding.authority_document)
      = v_binding.authority_bytes
    AND qualification_handoff_v1_hash(
      'worksflow.qualification-handoff.revision-authorities/v1',
      v_binding.authority_bytes
    ) = v_binding.authority_hash
    AND v_binding.creation_transaction_id
      = v_completion.creation_transaction_id
    AND v_binding.created_at = v_completion.completed_at
    AND v_completion.operation_id = v_consumption.operation_id
    AND v_completion.consumption_hash = v_consumption.consumption_hash
    AND v_completion.output_revision_id = v_handoff.output_revision_id
    AND v_completion.output_revision_content_hash = v_consumption.target_revision_content_hash
    AND v_completion.project_id = v_consumption.project_id
    AND v_completion.workflow_run_id = v_consumption.workflow_run_id
    AND v_completion.node_run_id = v_consumption.node_run_id
    AND v_completion.node_key = v_consumption.node_key
    AND v_completion.gate_output_document = qualification_handoff_v1_quality_result(
      v_wia.authority_id, v_output.id
    )
    AND v_completion.completion_document = v_expected_completion
    AND workflow_input_canonical_jsonb_bytes(v_completion.completion_document)
      = v_completion.completion_bytes
    AND qualification_handoff_v1_hash(
      'worksflow.qualification-handoff.completion/v1',
      v_completion.completion_bytes
    ) = v_completion.completion_hash
    AND ROW(
      v_output.id, v_output.artifact_id, v_output.parent_revision_id,
      v_output.schema_version, v_output.content_store, v_output.content_ref,
      v_output.content_hash, v_output.byte_size, v_output.source_manifest_id,
      v_output.proposal_id, v_output.implementation_proposal_id
    ) IS NOT DISTINCT FROM ROW(
      v_handoff.output_revision_id, v_parent.artifact_id, v_parent.id,
      v_parent.schema_version, v_parent.content_store, v_parent.content_ref,
      v_parent.content_hash, v_parent.byte_size, v_parent.source_manifest_id,
      v_parent.proposal_id, v_parent.implementation_proposal_id
    )
    AND v_output.change_source = 'system'
    AND v_output.change_summary = 'External qualification v2 promotion handoff'
    AND v_output.created_by = v_wia.run_started_by
    AND v_output.created_at = v_completion.completed_at
    AND v_output.approved_at = v_completion.completed_at
    AND (
      (v_output.workflow_status = 'approved'
       AND v_output.superseded_at IS NULL)
      OR (v_output.workflow_status = 'superseded'
          AND v_output.superseded_at IS NOT NULL)
    )
    AND v_output.promotion_handoff_id = v_handoff.handoff_id
    AND v_parent.workflow_status = 'superseded'
    AND v_parent.approved_at IS NOT NULL
    AND v_parent.superseded_at = v_completion.completed_at
    AND v_binding.authority_document->'copiedLineage'
      = pg_catalog.jsonb_build_object(
        'schemaVersion',
          'worksflow-qualification-handoff-copied-lineage/v1',
        'rootHash', v_lineage_root_hash,
        'sourceCount', v_source_count,
        'dependencyCount', v_dependency_count,
        'traceCount', v_trace_count
      )
    AND v_gate.run_id = v_completion.workflow_run_id
    AND v_gate.node_key = v_completion.node_key
    AND v_gate.node_type = 'external_qualification_gate'
    AND v_gate.status = 'completed'
    AND v_gate.input_authority_id = v_wia.authority_id
    AND v_gate.output_revision_id = v_output.id
    AND v_gate.attempt = 0
    AND v_gate.input_manifest_id IS NULL
    AND v_gate.output_proposal_id IS NULL
    AND v_gate.lease_owner IS NULL
    AND v_gate.lease_expires_at IS NULL
    AND v_gate.started_at IS NULL
    AND v_gate.completed_at = v_completion.completed_at
    AND v_gate.failure IS NULL
    AND EXISTS (
      SELECT 1 FROM workflow_runs AS run
      WHERE run.id = v_completion.workflow_run_id
        AND run.context#>ARRAY['nodes',v_completion.node_key,'output']
          = v_completion.gate_output_document
    )
    AND v_gate_event.run_id = v_completion.workflow_run_id
    AND v_gate_event.sequence = v_completion.event_cursor_before + 1
    AND v_gate_event.event_type = 'node.completed'
    AND v_gate_event.node_key = v_completion.node_key
    AND v_gate_event.actor_id IS NULL
    AND v_gate_event.payload = pg_catalog.jsonb_build_object(
      'handoffId', v_handoff.handoff_id::text,
      'outputRevisionId', v_output.id::text
    )
    AND v_gate_event.created_at = v_completion.completed_at
    AND v_publish_event.run_id = v_completion.workflow_run_id
    AND v_publish_event.sequence = v_completion.event_cursor_after
    AND v_publish_event.event_type = 'node.execution_authorization_required'
    AND v_publish_event.node_key = v_completion.publish_node_key
    AND v_publish_event.actor_id IS NULL
    AND v_publish_event.payload = '{}'::jsonb
    AND v_publish_event.created_at = v_completion.completed_at
    AND NOT EXISTS (
      SELECT 1
      FROM (VALUES
        (v_gate_event.id, v_gate_event.event_type, v_gate_event.node_key,
         v_gate_event.sequence, v_gate_event.payload),
        (v_publish_event.id, v_publish_event.event_type, v_publish_event.node_key,
         v_publish_event.sequence, v_publish_event.payload)
      ) AS expected(id,event_type,node_key,sequence,payload)
      LEFT JOIN outbox_events AS outbox ON outbox.id = expected.id
        AND outbox.aggregate_type = 'workflow_run'
        AND outbox.aggregate_id = v_completion.workflow_run_id::text
        AND outbox.event_type = expected.event_type
        AND outbox.subject = 'worksflow.workflow.run.event'
        AND outbox.headers = '{}'::jsonb
        AND outbox.created_at = v_completion.completed_at
        AND outbox.payload = pg_catalog.jsonb_build_object(
          'id', expected.id::text,
          'projectId', v_completion.project_id::text,
          'runId', v_completion.workflow_run_id::text,
          'sequence', expected.sequence,
          'type', expected.event_type,
          'occurredAt', qualification_handoff_v1_timestamp(v_completion.completed_at),
          'payload', expected.payload,
          'nodeKey', expected.node_key
        )
      WHERE outbox.id IS NULL
    )
    AND NOT EXISTS (
      SELECT 1 FROM qualification_promotion_v2_revision_transaction_grants
      WHERE handoff_id = p_handoff_id
    );
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN OTHERS THEN RETURN false;
END;
$function$;

CREATE FUNCTION qualification_handoff_v1_completion_bundle(
  p_handoff_id uuid,
  p_include_idempotent boolean,
  p_idempotent boolean
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
DECLARE
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
  v_binding qualification_promotion_v2_revision_authority_bindings%ROWTYPE;
  v_revision artifact_revisions%ROWTYPE;
  v_bundle jsonb;
BEGIN
  IF qualification_handoff_v1_completion_is_exact(p_handoff_id) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Handoff immutable completion is corrupt'
      USING ERRCODE = 'WPH02';
  END IF;
  SELECT * INTO STRICT v_completion
  FROM qualification_promotion_v2_handoff_completions
  WHERE handoff_id = p_handoff_id;
  SELECT * INTO STRICT v_binding
  FROM qualification_promotion_v2_revision_authority_bindings
  WHERE handoff_id = p_handoff_id;
  SELECT * INTO STRICT v_revision
  FROM artifact_revisions WHERE id = v_completion.output_revision_id;
  v_bundle := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-handoff-completion-bundle/v1',
    'handoffId', p_handoff_id::text,
    'completion', pg_catalog.jsonb_build_object(
      'hash', v_completion.completion_hash,
      'bytesHex', pg_catalog.encode(v_completion.completion_bytes, 'hex'),
      'document', v_completion.completion_document
    ),
    'revisionAuthority', pg_catalog.jsonb_build_object(
      'hash', v_binding.authority_hash,
      'bytesHex', pg_catalog.encode(v_binding.authority_bytes, 'hex'),
      'document', v_binding.authority_document
    ),
    'outputRevision', pg_catalog.jsonb_build_object(
      'id', v_revision.id::text,
      'artifactId', v_revision.artifact_id::text,
      'parentRevisionId', v_revision.parent_revision_id::text,
      'revisionNumber', v_revision.revision_number,
      'schemaVersion', v_revision.schema_version,
      'contentStore', v_revision.content_store,
      'contentRef', v_revision.content_ref,
      'contentHash', v_revision.content_hash,
      'byteSize', v_revision.byte_size,
      'stateAtHandoff', pg_catalog.jsonb_build_object(
        'workflowStatus', 'approved',
        'approvedAt', qualification_handoff_v1_timestamp(
          v_completion.completed_at
        ),
        'supersededAt', 'null'::jsonb
      ),
      'promotionHandoffId', v_revision.promotion_handoff_id::text,
      'createdAt', qualification_handoff_v1_timestamp(v_revision.created_at)
    ),
    'workflow', pg_catalog.jsonb_build_object(
      'projectId', v_completion.project_id::text,
      'workflowRunId', v_completion.workflow_run_id::text,
      'gateNodeRunId', v_completion.node_run_id::text,
      'gateNodeKey', v_completion.node_key,
      'publishNodeRunId', v_completion.publish_node_run_id::text,
      'publishNodeKey', v_completion.publish_node_key,
      'eventCursorBefore', v_completion.event_cursor_before,
      'eventCursorAfter', v_completion.event_cursor_after,
      'qualityResult', v_completion.gate_output_document,
      'gateStatusAtHandoff', 'completed',
      'publishStatusAtHandoff', 'waiting_input',
      'runStatusAtHandoff', 'waiting_input'
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

CREATE FUNCTION inspect_qualification_promotion_v2_handoff_completion(
  p_handoff_id uuid
)
RETURNS SETOF jsonb
LANGUAGE plpgsql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
BEGIN
  IF p_handoff_id IS NULL
     OR workflow_input_uuid_is_exact(p_handoff_id::text) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Handoff inspection identity is invalid'
      USING ERRCODE = 'WPH01';
  END IF;
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_handoff_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Handoff inspection caller is not the exact runtime login'
      USING ERRCODE = '42501';
  END IF;
  IF pg_catalog.pg_is_in_recovery()
     OR pg_catalog.current_setting('transaction_read_only') <> 'off' THEN
    RAISE EXCEPTION 'Qualification Handoff inspection requires a read-write primary'
      USING ERRCODE = 'WPH03';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM qualification_promotion_v2_handoff_completions
    WHERE handoff_id = p_handoff_id
  ) THEN
    RETURN;
  END IF;
  RETURN NEXT qualification_handoff_v1_completion_bundle(
    p_handoff_id, false, false
  );
END;
$function$;

CREATE FUNCTION complete_qualification_promotion_v2_handoff(p_handoff_id uuid)
RETURNS SETOF jsonb
LANGUAGE plpgsql
VOLATILE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY DEFINER
AS $function$
DECLARE
  v_locator qualification_promotion_v2_handoffs%ROWTYPE;
  v_handoff qualification_promotion_v2_handoffs%ROWTYPE;
  v_consumption qualification_promotion_v2_consumptions%ROWTYPE;
  v_wia workflow_input_authorities%ROWTYPE;
  v_plan qualification_plan_authorities%ROWTYPE;
  v_input_precommit qualification_input_precommit_authorities%ROWTYPE;
  v_receipt qualification_receipt_v3_receipts%ROWTYPE;
  v_run workflow_runs%ROWTYPE;
  v_gate workflow_node_runs%ROWTYPE;
  v_publish workflow_node_runs%ROWTYPE;
  v_artifact artifacts%ROWTYPE;
  v_parent artifact_revisions%ROWTYPE;
  v_now timestamptz;
  v_project_id uuid;
  v_publish_definition_id text;
  v_publish_edge_id text;
  v_revision_number bigint;
  v_gate_event_id uuid;
  v_publish_event_id uuid;
  v_quality_result jsonb;
  v_target jsonb;
  v_copied_lineage jsonb;
  v_lineage_root_hash text;
  v_source_count bigint;
  v_dependency_count bigint;
  v_trace_count bigint;
  v_lineage_member record;
  v_authority_document jsonb;
  v_authority_bytes bytea;
  v_authority_hash text;
  v_workflow_events jsonb;
  v_outbox_events jsonb;
  v_completion_document jsonb;
  v_completion_bytes bytea;
  v_completion_hash text;
  v_creation_transaction_id text;
BEGIN
  -- This shared fence is deliberately the first relation-facing operation.
  PERFORM pg_catalog.pg_advisory_xact_lock_shared(
    pg_catalog.hashtextextended(
      'worksflow:workflow-input-authority-migration:v1', 0
    )
  );
  IF qualification_input_precommit_caller_is_v1(
       'worksflow_qualification_handoff_operator'
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Handoff caller is not the exact runtime login'
      USING ERRCODE = '42501';
  END IF;
  IF p_handoff_id IS NULL
     OR workflow_input_uuid_is_exact(p_handoff_id::text) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Handoff identity is invalid'
      USING ERRCODE = 'WPH01';
  END IF;
  IF pg_catalog.current_setting('transaction_isolation') <> 'serializable' THEN
    RAISE EXCEPTION 'Qualification Handoff requires SERIALIZABLE isolation'
      USING ERRCODE = 'WPH03';
  END IF;
  IF pg_catalog.pg_is_in_recovery()
     OR pg_catalog.current_setting('transaction_read_only') <> 'off' THEN
    RAISE EXCEPTION 'Qualification Handoff requires a read-write primary'
      USING ERRCODE = 'WPH03';
  END IF;
  PERFORM pg_catalog.pg_advisory_xact_lock(
    pg_catalog.hashtextextended(
      'worksflow:qualification-handoff-v1:' || p_handoff_id::text, 0
    )
  );

  -- Replay is historical and intentionally precedes all current-authority
  -- assertions. A later authenticated Publish may already have advanced.
  IF EXISTS (
    SELECT 1 FROM qualification_promotion_v2_handoff_completions
    WHERE handoff_id = p_handoff_id
  ) THEN
    RETURN NEXT qualification_handoff_v1_completion_bundle(
      p_handoff_id, true, true
    );
    RETURN;
  END IF;

  -- The immutable pending row is used only as an untrusted project locator.
  SELECT * INTO v_locator
  FROM qualification_promotion_v2_handoffs
  WHERE handoff_id = p_handoff_id;
  IF NOT FOUND THEN RETURN; END IF;
  BEGIN
    v_project_id := (v_locator.handoff_document#>>'{target,projectId}')::uuid;
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION 'Qualification Handoff locator is malformed'
      USING ERRCODE = 'WPH02';
  END;
  PERFORM 1 FROM projects WHERE id = v_project_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Handoff project is unavailable'
      USING ERRCODE = 'WPH03';
  END IF;

  SELECT * INTO v_consumption
  FROM qualification_promotion_v2_consumptions
  WHERE operation_id = v_locator.operation_id;
  IF NOT FOUND OR v_consumption.project_id IS DISTINCT FROM v_project_id THEN
    RAISE EXCEPTION 'Qualification Handoff locator disagrees with Promotion'
      USING ERRCODE = 'WPH02';
  END IF;
  PERFORM * FROM assert_current_workflow_input_authority_v1(
    v_consumption.workflow_input_authority_id
  );
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Handoff Workflow Input Authority is not current'
      USING ERRCODE = 'WPH03';
  END IF;

  SELECT * INTO v_run FROM workflow_runs
  WHERE id = v_consumption.workflow_run_id FOR UPDATE;
  PERFORM 1 FROM workflow_node_runs
  WHERE run_id = v_consumption.workflow_run_id
  ORDER BY id FOR UPDATE;
  SELECT * INTO v_gate FROM workflow_node_runs
  WHERE id = v_consumption.node_run_id;
  IF v_run.id IS NULL OR v_gate.id IS NULL THEN
    RAISE EXCEPTION 'Qualification Handoff workflow target is unavailable'
      USING ERRCODE = 'WPH03';
  END IF;

  SELECT node.value->>'id' INTO v_publish_definition_id
  FROM workflow_definition_versions AS version
  CROSS JOIN LATERAL pg_catalog.jsonb_array_elements(
    version.content->'nodes'
  ) AS node(value)
  WHERE version.id = v_run.definition_version_id
    AND node.value->>'type' = 'publish';
  SELECT edge.value->>'id' INTO v_publish_edge_id
  FROM workflow_definition_versions AS version
  CROSS JOIN LATERAL pg_catalog.jsonb_array_elements(
    version.content->'edges'
  ) AS edge(value)
  WHERE version.id = v_run.definition_version_id
    AND edge.value->>'from' = v_consumption.node_key
    AND edge.value->>'to' = v_publish_definition_id;
  IF v_publish_definition_id IS NULL
     OR v_publish_edge_id IS NULL
     OR COALESCE(
       (v_run.context#>>ARRAY['disabledEdges',v_publish_edge_id])::boolean,
       false
     )
     OR (SELECT pg_catalog.count(*)
         FROM workflow_definition_versions AS version
         CROSS JOIN LATERAL pg_catalog.jsonb_array_elements(
           version.content->'nodes'
         ) AS node(value)
         WHERE version.id = v_run.definition_version_id
           AND node.value->>'type' = 'publish') <> 1
     OR NOT EXISTS (
       SELECT 1 FROM workflow_definition_versions AS version
       CROSS JOIN LATERAL pg_catalog.jsonb_array_elements(
         version.content->'edges'
       ) AS edge(value)
       WHERE version.id = v_run.definition_version_id
         AND edge.value->>'from' = v_consumption.node_key
         AND edge.value->>'to' = v_publish_definition_id
     )
     OR (SELECT pg_catalog.count(*)
         FROM workflow_definition_versions AS version
         CROSS JOIN LATERAL pg_catalog.jsonb_array_elements(
           version.content->'edges'
         ) AS edge(value)
         WHERE version.id = v_run.definition_version_id
           AND edge.value->>'from' = v_consumption.node_key) <> 1
     OR (SELECT pg_catalog.count(*)
         FROM workflow_definition_versions AS version
         CROSS JOIN LATERAL pg_catalog.jsonb_array_elements(
           version.content->'edges'
         ) AS edge(value)
         WHERE version.id = v_run.definition_version_id
           AND edge.value->>'to' = v_publish_definition_id) <> 1 THEN
    RAISE EXCEPTION 'Qualification Handoff has no exact direct production Publish successor'
      USING ERRCODE = 'WPH02';
  END IF;
  SELECT * INTO v_publish FROM workflow_node_runs
  WHERE run_id = v_run.id
    AND definition_node_id = v_publish_definition_id
    AND node_type = 'publish';
  IF NOT FOUND
     OR (SELECT pg_catalog.count(*) FROM workflow_node_runs
         WHERE run_id = v_run.id
           AND definition_node_id = v_publish_definition_id
           AND node_type = 'publish') <> 1 THEN
    RAISE EXCEPTION 'Qualification Handoff Publish successor is ambiguous'
      USING ERRCODE = 'WPH02';
  END IF;

  SELECT * INTO v_artifact FROM artifacts
  WHERE id = v_consumption.target_artifact_id FOR UPDATE;
  PERFORM 1 FROM artifact_revisions
  WHERE artifact_id = v_consumption.target_artifact_id
  ORDER BY id FOR UPDATE;
  SELECT * INTO v_parent FROM artifact_revisions
  WHERE id = v_consumption.target_revision_id;
  PERFORM 1 FROM artifact_revision_sources
  WHERE revision_id = v_consumption.target_revision_id
  ORDER BY ordinal FOR SHARE;
  PERFORM 1 FROM artifact_dependencies
  WHERE source_revision_id = v_consumption.target_revision_id
     OR target_revision_id = v_consumption.target_revision_id
  ORDER BY id FOR SHARE;
  PERFORM 1 FROM trace_links
  WHERE source_revision_id = v_consumption.target_revision_id
     OR target_revision_id = v_consumption.target_revision_id
  ORDER BY id FOR SHARE;

  SELECT * INTO v_handoff
  FROM qualification_promotion_v2_handoffs
  WHERE handoff_id = p_handoff_id FOR UPDATE;
  PERFORM 1 FROM qualification_promotion_v2_identity_reservations
  WHERE operation_id = v_consumption.operation_id ORDER BY ordinal FOR UPDATE;
  PERFORM 1 FROM artifact_revision_identity_reservations
  WHERE id = v_handoff.output_revision_id FOR UPDATE;
  SELECT * INTO v_wia FROM workflow_input_authorities
  WHERE authority_id = v_consumption.workflow_input_authority_id FOR SHARE;
  SELECT * INTO v_plan FROM qualification_plan_authorities
  WHERE authority_id = v_consumption.plan_authority_id FOR SHARE;
  SELECT * INTO v_input_precommit FROM qualification_input_precommit_authorities
  WHERE authority_id = v_consumption.input_precommit_authority_id FOR SHARE;
  SELECT * INTO v_receipt FROM qualification_receipt_v3_receipts
  WHERE receipt_id = v_consumption.receipt_id FOR SHARE;

  IF qualification_promotion_v2_store_record_is_exact(
       v_consumption.operation_id
     ) IS NOT TRUE
     OR qualification_promotion_v2_plan_is_exact(v_plan.authority_id) IS NOT TRUE
     OR qualification_input_precommit_authority_record_is_exact_v1(
       v_input_precommit.authority_id
     ) IS NOT TRUE THEN
    RAISE EXCEPTION 'Qualification Handoff upstream Promotion closure is corrupt'
      USING ERRCODE = 'WPH02';
  END IF;
  IF v_run.project_id IS DISTINCT FROM v_consumption.project_id
     OR v_run.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
     OR workflow_input_normalize_sha256(v_run.execution_profile_hash)
       IS DISTINCT FROM v_wia.execution_profile_hash
     OR v_run.status IS DISTINCT FROM 'waiting_qualification'
     OR v_run.started_by IS DISTINCT FROM v_wia.run_started_by
     OR v_run.started_at IS DISTINCT FROM v_wia.run_started_at
     OR v_run.completed_at IS NOT NULL
     OR v_run.cancelled_at IS NOT NULL
     OR v_run.failure IS NOT NULL
     OR v_run.context#>ARRAY['nodes',v_gate.node_key,'output'] IS NOT NULL
     OR v_gate.run_id IS DISTINCT FROM v_run.id
     OR v_gate.node_key IS DISTINCT FROM v_consumption.node_key
     OR v_gate.node_type IS DISTINCT FROM 'external_qualification_gate'
     OR v_gate.definition_node_id IS DISTINCT FROM 'external-qualification'
     OR v_gate.slice_kind IS DISTINCT FROM 'root' OR v_gate.slice_id IS NOT NULL
     OR v_gate.status IS DISTINCT FROM 'waiting_qualification'
     OR v_gate.input_authority_id IS DISTINCT FROM v_wia.authority_id
     OR v_gate.output_revision_id IS NOT NULL
     OR v_gate.attempt IS DISTINCT FROM 0 OR v_gate.input_manifest_id IS NOT NULL
     OR v_gate.output_proposal_id IS NOT NULL
     OR v_gate.lease_owner IS NOT NULL OR v_gate.lease_expires_at IS NOT NULL
     OR v_gate.started_at IS NOT NULL OR v_gate.completed_at IS NOT NULL
     OR v_gate.failure IS NOT NULL
     OR v_publish.status IS DISTINCT FROM 'pending'
     OR v_publish.attempt IS DISTINCT FROM 0
     OR v_publish.input_manifest_id IS NOT NULL
     OR v_publish.output_proposal_id IS NOT NULL
     OR v_publish.output_revision_id IS NOT NULL
     OR v_publish.lease_owner IS NOT NULL
     OR v_publish.lease_expires_at IS NOT NULL
     OR v_publish.started_at IS NOT NULL
     OR v_publish.completed_at IS NOT NULL
     OR v_publish.failure IS NOT NULL
     OR v_run.context#>ARRAY['nodes',v_publish.node_key,'executionActor']
       IS NOT NULL
     OR v_run.context#>ARRAY['nodes',v_publish.node_key,'output']
       IS NOT NULL THEN
    RAISE EXCEPTION 'Qualification Handoff workflow or Publish state drifted'
      USING ERRCODE = 'WPH03';
  END IF;
  IF v_artifact.id IS NULL OR v_parent.id IS NULL
     OR v_artifact.project_id IS DISTINCT FROM v_consumption.project_id
     OR v_artifact.lifecycle IS DISTINCT FROM 'active'
     OR v_artifact.latest_revision_id IS DISTINCT FROM v_parent.id
     OR v_artifact.latest_approved_revision_id IS DISTINCT FROM v_parent.id
     OR v_parent.artifact_id IS DISTINCT FROM v_artifact.id
     OR v_parent.workflow_status IS DISTINCT FROM 'approved'
     OR v_parent.approved_at IS NULL
     OR v_parent.superseded_at IS NOT NULL
     OR v_parent.content_hash IS DISTINCT FROM v_consumption.target_revision_content_hash
     OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_handoff_completions
       WHERE handoff_id = p_handoff_id
     ) THEN
    RAISE EXCEPTION 'Qualification Handoff target is stale or already materialized'
      USING ERRCODE = 'WPH03';
  END IF;

  v_now := pg_catalog.date_trunc('milliseconds', pg_catalog.clock_timestamp());
  -- pg_current_xact_id() is the top-level full transaction identity even
  -- though this PL/pgSQL block has an EXCEPTION handler and therefore writes
  -- under a subtransaction xmin. Persist it explicitly; xmin is not a safe
  -- construction-transaction discriminator here.
  v_creation_transaction_id := pg_catalog.pg_current_xact_id()::text;
  v_gate_event_id := pg_catalog.gen_random_uuid();
  v_publish_event_id := pg_catalog.gen_random_uuid();
  SELECT COALESCE(pg_catalog.max(revision_number), 0) + 1
  INTO v_revision_number
  FROM artifact_revisions WHERE artifact_id = v_artifact.id;
  v_target := v_consumption.closure_document->'target';
  v_quality_result := qualification_handoff_v1_quality_result(
    v_wia.authority_id, v_handoff.output_revision_id
  );
  v_workflow_events := pg_catalog.jsonb_build_array(
    pg_catalog.jsonb_build_object(
      'role', 'gate-completed', 'eventId', v_gate_event_id::text,
      'eventSequence', v_run.event_cursor + 1, 'eventType', 'node.completed',
      'nodeRunId', v_gate.id::text, 'nodeKey', v_gate.node_key
    ),
    pg_catalog.jsonb_build_object(
      'role', 'publish-authorization-required',
      'eventId', v_publish_event_id::text,
      'eventSequence', v_run.event_cursor + 2,
      'eventType', 'node.execution_authorization_required',
      'nodeRunId', v_publish.id::text, 'nodeKey', v_publish.node_key
    )
  );
  v_outbox_events := pg_catalog.jsonb_build_array(
    pg_catalog.jsonb_build_object(
      'role', 'gate-completed', 'outboxEventId', v_gate_event_id::text,
      'workflowEventId', v_gate_event_id::text, 'eventType', 'node.completed'
    ),
    pg_catalog.jsonb_build_object(
      'role', 'publish-authorization-required',
      'outboxEventId', v_publish_event_id::text,
      'workflowEventId', v_publish_event_id::text,
      'eventType', 'node.execution_authorization_required'
    )
  );
  v_completion_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-handoff-completion/v1',
    'handoffId', v_handoff.handoff_id::text,
    'operationId', v_consumption.operation_id::text,
    'consumptionHash', v_consumption.consumption_hash,
    'outputRevisionId', v_handoff.output_revision_id::text,
    'outputRevisionContentHash', v_parent.content_hash,
    'projectId', v_consumption.project_id::text,
    'workflowRunId', v_consumption.workflow_run_id::text,
    'nodeRunId', v_consumption.node_run_id::text,
    'nodeKey', v_consumption.node_key,
    'publishNodeRunId', v_publish.id::text,
    'workflowEvents', v_workflow_events,
    'outboxEvents', v_outbox_events,
    'completedAt', qualification_handoff_v1_timestamp(v_now)
  );
  v_completion_bytes := workflow_input_canonical_jsonb_bytes(v_completion_document);
  v_completion_hash := qualification_handoff_v1_hash(
    'worksflow.qualification-handoff.completion/v1', v_completion_bytes
  );

  INSERT INTO qualification_promotion_v2_revision_transaction_grants(
    output_revision_id, handoff_id, operation_id, backend_pid,
    transaction_id, granted_at
  ) VALUES (
    v_handoff.output_revision_id, v_handoff.handoff_id,
    v_consumption.operation_id, pg_catalog.pg_backend_pid(),
    v_creation_transaction_id, v_now
  );
  INSERT INTO artifact_revisions(
    id, artifact_id, revision_number, parent_revision_id, schema_version,
    content_store, content_ref, content_hash, byte_size, workflow_status,
    change_source, change_summary, source_manifest_id, proposal_id,
    implementation_proposal_id, created_by, created_at, approved_at,
    superseded_at, promotion_handoff_id
  ) VALUES (
    v_handoff.output_revision_id, v_parent.artifact_id, v_revision_number,
    v_parent.id, v_parent.schema_version, v_parent.content_store,
    v_parent.content_ref, v_parent.content_hash, v_parent.byte_size,
    'approved', 'system', 'External qualification v2 promotion handoff',
    v_parent.source_manifest_id, v_parent.proposal_id,
    v_parent.implementation_proposal_id, v_wia.run_started_by,
    v_now, v_now, NULL, v_handoff.handoff_id
  );
  IF EXISTS (
    SELECT 1 FROM qualification_promotion_v2_revision_transaction_grants
    WHERE output_revision_id = v_handoff.output_revision_id
  ) THEN
    RAISE EXCEPTION 'Qualification Handoff transaction grant was not consumed'
      USING ERRCODE = 'WPH02';
  END IF;

  INSERT INTO artifact_revision_sources(
    revision_id, ordinal, source_artifact_id, source_revision_id,
    source_content_hash, source_anchor_id, purpose, required, added_by, added_at
  )
  SELECT v_handoff.output_revision_id, source.ordinal,
         source.source_artifact_id, source.source_revision_id,
         source.source_content_hash, source.source_anchor_id,
         source.purpose, source.required, source.added_by, source.added_at
  FROM artifact_revision_sources AS source
  WHERE source.revision_id = v_parent.id
  ORDER BY source.ordinal;
  INSERT INTO artifact_dependencies(
    id, project_id, source_artifact_id, source_revision_id,
    source_content_hash, target_artifact_id, target_revision_id,
    relation, required, created_by, created_at
  )
  SELECT pg_catalog.gen_random_uuid(), dependency.project_id,
         dependency.source_artifact_id,
         CASE WHEN dependency.source_revision_id = v_parent.id
           THEN v_handoff.output_revision_id ELSE dependency.source_revision_id END,
         dependency.source_content_hash, dependency.target_artifact_id,
         CASE WHEN dependency.target_revision_id = v_parent.id
           THEN v_handoff.output_revision_id ELSE dependency.target_revision_id END,
         dependency.relation, dependency.required,
         dependency.created_by, dependency.created_at
  FROM artifact_dependencies AS dependency
  WHERE dependency.source_revision_id = v_parent.id
     OR dependency.target_revision_id = v_parent.id
  ORDER BY dependency.id;
  INSERT INTO trace_links(
    id, project_id, source_artifact_id, source_revision_id, source_anchor_id,
    target_artifact_id, target_revision_id, target_anchor_id,
    relation, metadata, created_by, created_at
  )
  SELECT pg_catalog.gen_random_uuid(), trace.project_id,
         trace.source_artifact_id,
         CASE WHEN trace.source_revision_id = v_parent.id
           THEN v_handoff.output_revision_id ELSE trace.source_revision_id END,
         trace.source_anchor_id, trace.target_artifact_id,
         CASE WHEN trace.target_revision_id = v_parent.id
           THEN v_handoff.output_revision_id ELSE trace.target_revision_id END,
         trace.target_anchor_id, trace.relation, trace.metadata,
         trace.created_by, trace.created_at
  FROM trace_links AS trace
  WHERE trace.source_revision_id = v_parent.id
     OR trace.target_revision_id = v_parent.id
  ORDER BY trace.id;

  -- Prove the one-shot copy against the locked parent before freezing the
  -- copied members. Historical inspection verifies only these frozen members;
  -- later legitimate lineage growth is not part of Handoff equality.
  IF EXISTS (
       (SELECT ordinal,source_artifact_id,source_revision_id,source_content_hash,
               source_anchor_id,purpose,required,added_by,added_at
        FROM artifact_revision_sources WHERE revision_id = v_parent.id
        EXCEPT
        SELECT ordinal,source_artifact_id,source_revision_id,source_content_hash,
               source_anchor_id,purpose,required,added_by,added_at
        FROM artifact_revision_sources
        WHERE revision_id = v_handoff.output_revision_id)
       UNION ALL
       (SELECT ordinal,source_artifact_id,source_revision_id,source_content_hash,
               source_anchor_id,purpose,required,added_by,added_at
        FROM artifact_revision_sources
        WHERE revision_id = v_handoff.output_revision_id
        EXCEPT
        SELECT ordinal,source_artifact_id,source_revision_id,source_content_hash,
               source_anchor_id,purpose,required,added_by,added_at
        FROM artifact_revision_sources WHERE revision_id = v_parent.id)
     )
     OR EXISTS (
       SELECT 1 FROM artifact_dependencies AS parent_dependency
       WHERE (
         parent_dependency.source_revision_id = v_parent.id
         OR parent_dependency.target_revision_id = v_parent.id
       ) AND NOT EXISTS (
         SELECT 1 FROM artifact_dependencies AS output_dependency
         WHERE output_dependency.project_id = parent_dependency.project_id
           AND output_dependency.source_artifact_id = parent_dependency.source_artifact_id
           AND output_dependency.source_revision_id = CASE
             WHEN parent_dependency.source_revision_id = v_parent.id
               THEN v_handoff.output_revision_id
             ELSE parent_dependency.source_revision_id
           END
           AND output_dependency.source_content_hash = parent_dependency.source_content_hash
           AND output_dependency.target_artifact_id = parent_dependency.target_artifact_id
           AND output_dependency.target_revision_id IS NOT DISTINCT FROM CASE
             WHEN parent_dependency.target_revision_id = v_parent.id
               THEN v_handoff.output_revision_id
             ELSE parent_dependency.target_revision_id
           END
           AND output_dependency.relation = parent_dependency.relation
           AND output_dependency.required = parent_dependency.required
           AND output_dependency.created_by = parent_dependency.created_by
           AND output_dependency.created_at = parent_dependency.created_at
       )
     )
     OR EXISTS (
       SELECT 1 FROM artifact_dependencies AS output_dependency
       WHERE (
         output_dependency.source_revision_id = v_handoff.output_revision_id
         OR output_dependency.target_revision_id = v_handoff.output_revision_id
       ) AND NOT EXISTS (
         SELECT 1 FROM artifact_dependencies AS parent_dependency
         WHERE parent_dependency.project_id = output_dependency.project_id
           AND parent_dependency.source_artifact_id = output_dependency.source_artifact_id
           AND parent_dependency.source_revision_id = CASE
             WHEN output_dependency.source_revision_id = v_handoff.output_revision_id
               THEN v_parent.id
             ELSE output_dependency.source_revision_id
           END
           AND parent_dependency.source_content_hash = output_dependency.source_content_hash
           AND parent_dependency.target_artifact_id = output_dependency.target_artifact_id
           AND parent_dependency.target_revision_id IS NOT DISTINCT FROM CASE
             WHEN output_dependency.target_revision_id = v_handoff.output_revision_id
               THEN v_parent.id
             ELSE output_dependency.target_revision_id
           END
           AND parent_dependency.relation = output_dependency.relation
           AND parent_dependency.required = output_dependency.required
           AND parent_dependency.created_by = output_dependency.created_by
           AND parent_dependency.created_at = output_dependency.created_at
       )
     )
     OR EXISTS (
       SELECT 1 FROM trace_links AS parent_trace
       WHERE (
         parent_trace.source_revision_id = v_parent.id
         OR parent_trace.target_revision_id = v_parent.id
       ) AND NOT EXISTS (
         SELECT 1 FROM trace_links AS output_trace
         WHERE output_trace.project_id = parent_trace.project_id
           AND output_trace.source_artifact_id = parent_trace.source_artifact_id
           AND output_trace.source_revision_id = CASE
             WHEN parent_trace.source_revision_id = v_parent.id
               THEN v_handoff.output_revision_id
             ELSE parent_trace.source_revision_id
           END
           AND output_trace.source_anchor_id IS NOT DISTINCT FROM parent_trace.source_anchor_id
           AND output_trace.target_artifact_id = parent_trace.target_artifact_id
           AND output_trace.target_revision_id IS NOT DISTINCT FROM CASE
             WHEN parent_trace.target_revision_id = v_parent.id
               THEN v_handoff.output_revision_id
             ELSE parent_trace.target_revision_id
           END
           AND output_trace.target_anchor_id IS NOT DISTINCT FROM parent_trace.target_anchor_id
           AND output_trace.relation = parent_trace.relation
           AND output_trace.metadata = parent_trace.metadata
           AND output_trace.created_by = parent_trace.created_by
           AND output_trace.created_at = parent_trace.created_at
       )
     )
     OR EXISTS (
       SELECT 1 FROM trace_links AS output_trace
       WHERE (
         output_trace.source_revision_id = v_handoff.output_revision_id
         OR output_trace.target_revision_id = v_handoff.output_revision_id
       ) AND NOT EXISTS (
         SELECT 1 FROM trace_links AS parent_trace
         WHERE parent_trace.project_id = output_trace.project_id
           AND parent_trace.source_artifact_id = output_trace.source_artifact_id
           AND parent_trace.source_revision_id = CASE
             WHEN output_trace.source_revision_id = v_handoff.output_revision_id
               THEN v_parent.id
             ELSE output_trace.source_revision_id
           END
           AND parent_trace.source_anchor_id IS NOT DISTINCT FROM output_trace.source_anchor_id
           AND parent_trace.target_artifact_id = output_trace.target_artifact_id
           AND parent_trace.target_revision_id IS NOT DISTINCT FROM CASE
             WHEN output_trace.target_revision_id = v_handoff.output_revision_id
               THEN v_parent.id
             ELSE output_trace.target_revision_id
           END
           AND parent_trace.target_anchor_id IS NOT DISTINCT FROM output_trace.target_anchor_id
           AND parent_trace.relation = output_trace.relation
           AND parent_trace.metadata = output_trace.metadata
           AND parent_trace.created_by = output_trace.created_by
           AND parent_trace.created_at = output_trace.created_at
       )
     ) THEN
    RAISE EXCEPTION 'Qualification Handoff copied lineage is not exact'
      USING ERRCODE = 'WPH02';
  END IF;

  WITH members AS (
    SELECT pg_catalog.lpad(source.ordinal::text, 10, '0') AS member_key,
           pg_catalog.jsonb_build_object(
             'schemaVersion',
               'worksflow-qualification-handoff-lineage-member/v1',
             'memberKind', 'source',
             'memberKey', pg_catalog.lpad(source.ordinal::text, 10, '0'),
             'outputRevisionId', v_handoff.output_revision_id::text,
             'row', pg_catalog.jsonb_build_object(
               'ordinal', source.ordinal,
               'sourceArtifactId', source.source_artifact_id::text,
               'sourceRevisionId', source.source_revision_id::text,
               'sourceContentHash', source.source_content_hash,
               'sourceAnchorId', source.source_anchor_id,
               'purpose', source.purpose,
               'required', source.required,
               'addedBy', source.added_by::text,
               'addedAt', pg_catalog.to_char(
                 source.added_at AT TIME ZONE 'UTC',
                 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
               )
             )
           ) AS document
    FROM artifact_revision_sources AS source
    WHERE source.revision_id = v_handoff.output_revision_id
  )
  INSERT INTO qualification_promotion_v2_handoff_lineage_members(
    handoff_id,member_kind,member_ordinal,member_key,row_hash,
    creation_transaction_id
  )
  SELECT v_handoff.handoff_id, 'source',
         pg_catalog.row_number() OVER (
           ORDER BY pg_catalog.convert_to(member_key,'UTF8')
         ) - 1,
         member_key,
         qualification_handoff_v1_hash(
           'worksflow.qualification-handoff.lineage-member.source/v1',
           workflow_input_canonical_jsonb_bytes(document)
         ),
         v_creation_transaction_id
  FROM members
  ORDER BY pg_catalog.convert_to(member_key,'UTF8');

  WITH members AS (
    SELECT dependency.id::text AS member_key,
           pg_catalog.jsonb_build_object(
             'schemaVersion',
               'worksflow-qualification-handoff-lineage-member/v1',
             'memberKind', 'dependency',
             'memberKey', dependency.id::text,
             'outputRevisionId', v_handoff.output_revision_id::text,
             'row', pg_catalog.jsonb_build_object(
               'id', dependency.id::text,
               'projectId', dependency.project_id::text,
               'sourceArtifactId', dependency.source_artifact_id::text,
               'sourceRevisionId', dependency.source_revision_id::text,
               'sourceContentHash', dependency.source_content_hash,
               'targetArtifactId', dependency.target_artifact_id::text,
               'targetRevisionId', dependency.target_revision_id::text,
               'relation', dependency.relation,
               'required', dependency.required,
               'createdBy', dependency.created_by::text,
               'createdAt', pg_catalog.to_char(
                 dependency.created_at AT TIME ZONE 'UTC',
                 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
               )
             )
           ) AS document
    FROM artifact_dependencies AS dependency
    WHERE dependency.source_revision_id = v_handoff.output_revision_id
       OR dependency.target_revision_id = v_handoff.output_revision_id
  )
  INSERT INTO qualification_promotion_v2_handoff_lineage_members(
    handoff_id,member_kind,member_ordinal,member_key,row_hash,
    creation_transaction_id
  )
  SELECT v_handoff.handoff_id, 'dependency',
         pg_catalog.row_number() OVER (
           ORDER BY pg_catalog.convert_to(member_key,'UTF8')
         ) - 1,
         member_key,
         qualification_handoff_v1_hash(
           'worksflow.qualification-handoff.lineage-member.dependency/v1',
           workflow_input_canonical_jsonb_bytes(document)
         ),
         v_creation_transaction_id
  FROM members
  ORDER BY pg_catalog.convert_to(member_key,'UTF8');

  WITH members AS (
    SELECT trace.id::text AS member_key,
           pg_catalog.jsonb_build_object(
             'schemaVersion',
               'worksflow-qualification-handoff-lineage-member/v1',
             'memberKind', 'trace',
             'memberKey', trace.id::text,
             'outputRevisionId', v_handoff.output_revision_id::text,
             'row', pg_catalog.jsonb_build_object(
               'id', trace.id::text,
               'projectId', trace.project_id::text,
               'sourceArtifactId', trace.source_artifact_id::text,
               'sourceRevisionId', trace.source_revision_id::text,
               'sourceAnchorId', trace.source_anchor_id,
               'targetArtifactId', trace.target_artifact_id::text,
               'targetRevisionId', trace.target_revision_id::text,
               'targetAnchorId', trace.target_anchor_id,
               'relation', trace.relation,
               'metadata', trace.metadata,
               'createdBy', trace.created_by::text,
               'createdAt', pg_catalog.to_char(
                 trace.created_at AT TIME ZONE 'UTC',
                 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
               )
             )
           ) AS document
    FROM trace_links AS trace
    WHERE trace.source_revision_id = v_handoff.output_revision_id
       OR trace.target_revision_id = v_handoff.output_revision_id
  )
  INSERT INTO qualification_promotion_v2_handoff_lineage_members(
    handoff_id,member_kind,member_ordinal,member_key,row_hash,
    creation_transaction_id
  )
  SELECT v_handoff.handoff_id, 'trace',
         pg_catalog.row_number() OVER (
           ORDER BY pg_catalog.convert_to(member_key,'UTF8')
         ) - 1,
         member_key,
         qualification_handoff_v1_hash(
           'worksflow.qualification-handoff.lineage-member.trace/v1',
           workflow_input_canonical_jsonb_bytes(document)
         ),
         v_creation_transaction_id
  FROM members
  ORDER BY pg_catalog.convert_to(member_key,'UTF8');

  SELECT
    pg_catalog.count(*) FILTER (WHERE member_kind = 'source'),
    pg_catalog.count(*) FILTER (WHERE member_kind = 'dependency'),
    pg_catalog.count(*) FILTER (WHERE member_kind = 'trace')
  INTO v_source_count, v_dependency_count, v_trace_count
  FROM qualification_promotion_v2_handoff_lineage_members
  WHERE handoff_id = v_handoff.handoff_id;
  v_lineage_root_hash := qualification_handoff_v1_hash(
    'worksflow.qualification-handoff.lineage-root-seed/v1',
    pg_catalog.convert_to(
      'worksflow-qualification-handoff-copied-lineage/v1', 'UTF8'
    )
  );
  FOR v_lineage_member IN
    SELECT member_kind,member_ordinal,member_key,row_hash
    FROM qualification_promotion_v2_handoff_lineage_members
    WHERE handoff_id = v_handoff.handoff_id
    ORDER BY CASE member_kind
      WHEN 'source' THEN 0 WHEN 'dependency' THEN 1 ELSE 2 END,
      pg_catalog.convert_to(member_key,'UTF8')
  LOOP
    v_lineage_root_hash := qualification_handoff_v1_hash(
      'worksflow.qualification-handoff.lineage-root-step/v1',
      pg_catalog.convert_to(v_lineage_root_hash, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_lineage_member.member_kind, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_lineage_member.member_ordinal::text, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_lineage_member.member_key, 'UTF8')
      || pg_catalog.decode('00','hex')
      || pg_catalog.convert_to(v_lineage_member.row_hash, 'UTF8')
    );
  END LOOP;
  v_copied_lineage := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-handoff-copied-lineage/v1',
    'rootHash', v_lineage_root_hash,
    'sourceCount', v_source_count,
    'dependencyCount', v_dependency_count,
    'traceCount', v_trace_count
  );

  v_authority_document := pg_catalog.jsonb_build_object(
    'schemaVersion', 'worksflow-qualification-promotion-output-revision-authorities/v1',
    'handoffId', v_handoff.handoff_id::text,
    'operationId', v_consumption.operation_id::text,
    'outputRevisionId', v_handoff.output_revision_id::text,
    'workflowInput', pg_catalog.jsonb_build_object(
      'authorityId', v_wia.authority_id::text,
      'authorityHash', v_wia.authority_hash
    ),
    'plan', pg_catalog.jsonb_build_object(
      'authorityId', v_plan.authority_id::text,
      'authorityHash', v_plan.envelope_hash
    ),
    'receipt', pg_catalog.jsonb_build_object(
      'receiptId', v_receipt.receipt_id,
      'envelopeHash', v_receipt.envelope_hash
    ),
    'promotion', pg_catalog.jsonb_build_object(
      'requestHash', v_consumption.request_hash,
      'closureHash', v_consumption.closure_hash,
      'revisionIntentHash', v_consumption.revision_intent_hash,
      'consumptionHash', v_consumption.consumption_hash
    ),
    'target', v_target,
    'revisionStateAtHandoff', pg_catalog.jsonb_build_object(
      'workflowStatus', 'approved',
      'approvedAt', qualification_handoff_v1_timestamp(v_now),
      'supersededAt', 'null'::jsonb,
      'parentWorkflowStatus', 'superseded',
      'parentApprovedAt', pg_catalog.to_char(
        v_parent.approved_at AT TIME ZONE 'UTC',
        'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
      ),
      'parentSupersededAt', qualification_handoff_v1_timestamp(v_now)
    ),
    'copiedLineage', v_copied_lineage
  );
  v_authority_bytes := workflow_input_canonical_jsonb_bytes(v_authority_document);
  v_authority_hash := qualification_handoff_v1_hash(
    'worksflow.qualification-handoff.revision-authorities/v1',
    v_authority_bytes
  );

  INSERT INTO qualification_promotion_v2_revision_authority_bindings(
    handoff_id, operation_id, output_revision_id,
    workflow_input_authority_id, workflow_input_authority_hash,
    plan_authority_id, plan_authority_hash, receipt_id,
    receipt_envelope_hash, promotion_request_hash,
    promotion_closure_hash, promotion_revision_intent_hash,
    promotion_consumption_hash, target_document, authority_hash,
    authority_bytes, authority_document, creation_transaction_id, created_at
  ) VALUES (
    v_handoff.handoff_id, v_consumption.operation_id,
    v_handoff.output_revision_id, v_wia.authority_id, v_wia.authority_hash,
    v_plan.authority_id, v_plan.envelope_hash, v_receipt.receipt_id,
    v_receipt.envelope_hash, v_consumption.request_hash,
    v_consumption.closure_hash, v_consumption.revision_intent_hash,
    v_consumption.consumption_hash, v_target, v_authority_hash,
    v_authority_bytes, v_authority_document, v_creation_transaction_id, v_now
  );

  INSERT INTO workflow_run_events(
    id, run_id, sequence, event_type, node_key, payload, actor_id, created_at
  ) VALUES
    (v_gate_event_id, v_run.id, v_run.event_cursor + 1, 'node.completed',
     v_gate.node_key, pg_catalog.jsonb_build_object(
       'handoffId', v_handoff.handoff_id::text,
       'outputRevisionId', v_handoff.output_revision_id::text
     ), NULL, v_now),
    (v_publish_event_id, v_run.id, v_run.event_cursor + 2,
     'node.execution_authorization_required', v_publish.node_key,
     '{}'::jsonb, NULL, v_now);
  INSERT INTO outbox_events(
    id, aggregate_type, aggregate_id, event_type, subject, payload, headers,
    attempts, available_at, published_at, last_error, created_at
  )
  SELECT event.id, 'workflow_run', v_run.id::text, event.event_type,
         'worksflow.workflow.run.event',
         pg_catalog.jsonb_build_object(
           'id', event.id::text,
           'projectId', v_consumption.project_id::text,
           'runId', v_run.id::text,
           'sequence', event.sequence,
           'type', event.event_type,
           'occurredAt', qualification_handoff_v1_timestamp(v_now),
           'payload', event.payload,
           'nodeKey', event.node_key
         ),
         '{}'::jsonb, 0, v_now, NULL, NULL, v_now
  FROM workflow_run_events AS event
  WHERE event.id IN (v_gate_event_id, v_publish_event_id)
  ORDER BY event.sequence;

  INSERT INTO qualification_promotion_v2_handoff_completions(
    handoff_id, operation_id, consumption_hash, output_revision_id,
    output_revision_content_hash, project_id, workflow_run_id,
    node_run_id, node_key, publish_node_run_id, publish_node_key,
    event_cursor_before, event_cursor_after, gate_output_document,
    gate_completed_event_id, publish_authorization_event_id,
    completion_hash, completion_bytes, completion_document,
    creation_transaction_id, completed_at
  ) VALUES (
    v_handoff.handoff_id, v_consumption.operation_id,
    v_consumption.consumption_hash, v_handoff.output_revision_id,
    v_parent.content_hash, v_consumption.project_id, v_run.id,
    v_gate.id, v_gate.node_key, v_publish.id, v_publish.node_key,
    v_run.event_cursor, v_run.event_cursor + 2, v_quality_result,
    v_gate_event_id, v_publish_event_id, v_completion_hash,
    v_completion_bytes, v_completion_document, v_creation_transaction_id, v_now
  );

  UPDATE artifact_revisions
  SET workflow_status = 'superseded', superseded_at = v_now
  WHERE id = v_parent.id
    AND workflow_status = 'approved' AND superseded_at IS NULL;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Handoff parent Revision CAS failed'
      USING ERRCODE = 'WPH03';
  END IF;
  UPDATE artifacts
  SET latest_revision_id = v_handoff.output_revision_id,
      latest_approved_revision_id = v_handoff.output_revision_id,
      version = version + 1,
      updated_at = v_now
  WHERE id = v_artifact.id
    AND latest_revision_id = v_parent.id
    AND latest_approved_revision_id = v_parent.id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Handoff artifact pointer CAS failed'
      USING ERRCODE = 'WPH03';
  END IF;
  UPDATE workflow_node_runs
  SET status = 'completed', output_revision_id = v_handoff.output_revision_id,
      completed_at = v_now, updated_at = v_now
  WHERE id = v_gate.id AND status = 'waiting_qualification'
    AND output_revision_id IS NULL;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Handoff gate CAS failed'
      USING ERRCODE = 'WPH03';
  END IF;
  UPDATE workflow_node_runs
  SET status = 'waiting_input', updated_at = v_now
  WHERE id = v_publish.id AND status = 'pending'
    AND attempt = 0 AND output_revision_id IS NULL
    AND lease_owner IS NULL AND lease_expires_at IS NULL
    AND completed_at IS NULL AND failure IS NULL;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Handoff Publish CAS failed'
      USING ERRCODE = 'WPH03';
  END IF;
  UPDATE workflow_runs
  SET status = 'waiting_input', event_cursor = v_run.event_cursor + 2,
      context = pg_catalog.jsonb_set(
        context, ARRAY['nodes',v_gate.node_key,'output'],
        v_quality_result, true
      ),
      updated_at = v_now
  WHERE id = v_run.id AND status = 'waiting_qualification'
    AND event_cursor = v_run.event_cursor;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'Qualification Handoff run CAS failed'
      USING ERRCODE = 'WPH03';
  END IF;

  IF qualification_handoff_v1_completion_is_exact(p_handoff_id) IS NOT TRUE
     OR NOT EXISTS (
       SELECT 1 FROM artifacts
       WHERE id = v_artifact.id
         AND latest_revision_id = v_handoff.output_revision_id
         AND latest_approved_revision_id = v_handoff.output_revision_id
     )
     OR EXISTS (
       SELECT 1 FROM qualification_promotion_v2_revision_transaction_grants
     ) THEN
    RAISE EXCEPTION 'Qualification Handoff just-written closure is not exact'
      USING ERRCODE = 'WPH02';
  END IF;
  RETURN NEXT qualification_handoff_v1_completion_bundle(
    p_handoff_id, true, false
  );
  RETURN;
EXCEPTION
  WHEN serialization_failure OR deadlock_detected THEN RAISE;
  WHEN unique_violation OR foreign_key_violation OR check_violation
    OR not_null_violation THEN
    RAISE EXCEPTION 'Qualification Handoff immutable identity or closure conflict'
      USING ERRCODE = 'WPH02';
  WHEN invalid_text_representation OR numeric_value_out_of_range
    OR datetime_field_overflow THEN
    RAISE EXCEPTION 'Qualification Handoff resolved authority is malformed'
      USING ERRCODE = 'WPH02';
END;
$function$;

-- Replace migration-79's deny-until-Handoff guards with one narrow completed
-- shape. The deferred closure below proves the full cross-table aggregate.
CREATE OR REPLACE FUNCTION guard_workflow_execution_profile_v3_run()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
BEGIN
  IF NEW.execution_profile_version = 'workflow-engine/v3'
     OR NEW.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR NEW.status = 'waiting_qualification' THEN
    IF NEW.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
       OR NEW.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
       OR NOT EXISTS (
         SELECT 1 FROM workflow_definition_versions AS version
         WHERE version.id = NEW.definition_version_id
           AND version.execution_profile_version = NEW.execution_profile_version
           AND version.execution_profile_hash = NEW.execution_profile_hash
           AND version.content_hash = version.content->>'hash'
           AND workflow_execution_profile_v3_definition_is_database_admissible(
             version.content
           ) IS TRUE
       ) THEN
      RAISE EXCEPTION 'workflow run does not bind the exact workflow-engine/v3 definition'
        USING ERRCODE = '23514';
    END IF;
    IF NEW.status = 'completed' THEN
      RAISE EXCEPTION 'workflow-engine/v3 cannot complete before the qualified release authority'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  IF TG_OP = 'UPDATE'
     AND OLD.execution_profile_version = 'workflow-engine/v3' THEN
    SELECT * INTO v_completion
    FROM qualification_promotion_v2_handoff_completions
    WHERE workflow_run_id = NEW.id;
    IF OLD.status = 'waiting_qualification'
       AND NEW.status IS DISTINCT FROM 'waiting_qualification' THEN
      IF NEW.status <> 'waiting_input'
         OR v_completion.handoff_id IS NULL
         OR NEW.event_cursor <> v_completion.event_cursor_after
         OR NEW.context#>ARRAY['nodes',v_completion.node_key,'output']
           IS DISTINCT FROM v_completion.gate_output_document
         OR NOT EXISTS (
           SELECT 1 FROM workflow_node_runs AS gate
           WHERE gate.id = v_completion.node_run_id
             AND gate.status = 'completed'
             AND gate.output_revision_id = v_completion.output_revision_id
             AND gate.completed_at = v_completion.completed_at
         )
         OR NOT EXISTS (
           SELECT 1 FROM workflow_node_runs AS publish
           WHERE publish.id = v_completion.publish_node_run_id
             AND publish.status = 'waiting_input'
             AND publish.attempt = 0
             AND publish.output_revision_id IS NULL
             AND publish.lease_owner IS NULL
             AND publish.lease_expires_at IS NULL
             AND publish.started_at IS NULL
             AND publish.completed_at IS NULL
             AND publish.failure IS NULL
         ) THEN
        RAISE EXCEPTION 'workflow-engine/v3 may leave qualification only through exact Handoff'
          USING ERRCODE = '23514';
      END IF;
    END IF;
    IF v_completion.handoff_id IS NOT NULL
       AND NEW.context#>ARRAY['nodes',v_completion.node_key,'output']
         IS DISTINCT FROM v_completion.gate_output_document THEN
      RAISE EXCEPTION 'workflow-engine/v3 Handoff QualityResult is immutable'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

DROP TRIGGER workflow_execution_profile_v3_run_guard ON workflow_runs;
CREATE TRIGGER workflow_execution_profile_v3_run_guard
BEFORE INSERT OR UPDATE OF definition_version_id, execution_profile_version,
  execution_profile_hash, status, context, event_cursor
ON workflow_runs
FOR EACH ROW EXECUTE FUNCTION guard_workflow_execution_profile_v3_run();

CREATE OR REPLACE FUNCTION guard_external_qualification_gate_node_v3()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_run workflow_runs%ROWTYPE;
  v_definition jsonb;
  v_definition_type text;
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
BEGIN
  SELECT * INTO v_run FROM workflow_runs WHERE id = NEW.run_id;
  IF v_run.id IS NULL THEN
    RAISE EXCEPTION 'external qualification node has no workflow run'
      USING ERRCODE = '23503';
  END IF;
  SELECT version.content INTO v_definition
  FROM workflow_definition_versions AS version
  WHERE version.id = v_run.definition_version_id;
  IF NEW.definition_node_id IS NOT NULL THEN
    SELECT value->>'type' INTO v_definition_type
    FROM pg_catalog.jsonb_array_elements(
      COALESCE(v_definition->'nodes','[]'::jsonb)
    ) AS node(value)
    WHERE value->>'id' = NEW.definition_node_id;
  END IF;
  SELECT * INTO v_completion
  FROM qualification_promotion_v2_handoff_completions
  WHERE node_run_id = NEW.id;

  IF NEW.node_type = 'external_qualification_gate'
     OR NEW.status = 'waiting_qualification'
     OR NEW.input_authority_id IS NOT NULL THEN
    IF v_run.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
       OR v_run.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
       OR workflow_execution_profile_v3_definition_is_database_admissible(
         v_definition
       ) IS NOT TRUE
       OR NEW.node_type IS DISTINCT FROM 'external_qualification_gate'
       OR v_definition_type IS DISTINCT FROM 'external_qualification_gate'
       OR NEW.definition_node_id IS DISTINCT FROM 'external-qualification'
       OR NEW.node_key IS DISTINCT FROM 'external-qualification'
       OR NEW.slice_kind IS DISTINCT FROM 'root' OR NEW.slice_id IS NOT NULL
       OR NEW.attempt <> 0 OR NEW.lease_owner IS NOT NULL
       OR NEW.lease_expires_at IS NOT NULL OR NEW.started_at IS NOT NULL
       OR NEW.failure IS NOT NULL OR NEW.input_manifest_id IS NOT NULL
       OR NEW.output_proposal_id IS NOT NULL
       OR NEW.status NOT IN (
         'pending','waiting_qualification','completed','cancelled','stale'
       )
       OR (TG_OP = 'INSERT' AND NEW.status <> 'pending')
       OR (TG_OP = 'UPDATE'
         AND OLD.node_type = 'external_qualification_gate' AND (
           (OLD.status = 'pending' AND NEW.status NOT IN (
             'pending','waiting_qualification','cancelled','stale'
           ))
           OR (OLD.status = 'waiting_qualification'
             AND NEW.status NOT IN (
               'waiting_qualification','completed','cancelled','stale'
             ))
           OR (OLD.status IN ('completed','cancelled','stale')
             AND NEW.status <> OLD.status)
         ))
       OR (NEW.status = 'pending' AND NEW.input_authority_id IS NOT NULL)
       OR (NEW.status = 'waiting_qualification' AND (
         NEW.input_authority_id IS NULL OR NEW.output_revision_id IS NOT NULL
         OR NEW.completed_at IS NOT NULL
         OR NOT EXISTS (
           SELECT 1 FROM workflow_input_authorities AS authority
           WHERE authority.authority_id = NEW.input_authority_id
             AND authority.workflow_run_id = NEW.run_id
             AND authority.node_run_id = NEW.id
         )
       ))
       OR (NEW.status = 'completed' AND (
         v_completion.handoff_id IS NULL
         OR NEW.input_authority_id IS DISTINCT FROM (
           SELECT workflow_input_authority_id
           FROM qualification_promotion_v2_consumptions
           WHERE operation_id = v_completion.operation_id
         )
         OR NEW.output_revision_id IS DISTINCT FROM v_completion.output_revision_id
         OR NEW.completed_at IS DISTINCT FROM v_completion.completed_at
       ))
       OR (NEW.status <> 'completed' AND NEW.output_revision_id IS NOT NULL)
       OR (NEW.status <> 'completed' AND NEW.completed_at IS NOT NULL) THEN
      RAISE EXCEPTION 'dedicated external qualification gate cannot use a generic workflow transition'
        USING ERRCODE = '23514';
    END IF;
  ELSIF v_run.execution_profile_version = 'workflow-engine/v3'
        AND (NEW.definition_node_id IS NULL
          OR v_definition_type IS DISTINCT FROM NEW.node_type) THEN
    RAISE EXCEPTION 'workflow-engine/v3 node does not match its stable definition identity'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION validate_workflow_execution_profile_v3_run_closure()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_run_id uuid;
  v_run workflow_runs%ROWTYPE;
  v_expected_external_id text;
  v_gate workflow_node_runs%ROWTYPE;
  v_completion qualification_promotion_v2_handoff_completions%ROWTYPE;
BEGIN
  IF TG_TABLE_NAME = 'workflow_runs' THEN
    v_run_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
  ELSE
    v_run_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.run_id ELSE NEW.run_id END;
  END IF;
  SELECT * INTO v_run FROM workflow_runs WHERE id = v_run_id;
  IF v_run.id IS NULL OR v_run.execution_profile_version <> 'workflow-engine/v3' THEN
    RETURN NULL;
  END IF;
  SELECT value->>'id' INTO v_expected_external_id
  FROM workflow_definition_versions AS version,
       LATERAL pg_catalog.jsonb_array_elements(version.content->'nodes') AS node(value)
  WHERE version.id = v_run.definition_version_id
    AND value->>'type' = 'external_qualification_gate';
  SELECT * INTO v_gate FROM workflow_node_runs
  WHERE run_id = v_run.id
    AND node_type = 'external_qualification_gate'
    AND definition_node_id = v_expected_external_id
    AND node_key = 'external-qualification'
    AND slice_kind = 'root' AND slice_id IS NULL;
  IF v_expected_external_id IS NULL
     OR v_expected_external_id <> 'external-qualification'
     OR v_gate.id IS NULL
     OR (SELECT pg_catalog.count(*) FROM workflow_node_runs AS node
         WHERE node.run_id = v_run.id
           AND node.node_type = 'external_qualification_gate') <> 1 THEN
    RAISE EXCEPTION 'workflow-engine/v3 run lacks its exact external qualification gate closure'
      USING ERRCODE = '23514';
  END IF;
  SELECT * INTO v_completion
  FROM qualification_promotion_v2_handoff_completions
  WHERE workflow_run_id = v_run.id AND node_run_id = v_gate.id;
  IF v_completion.handoff_id IS NULL THEN
    IF v_gate.attempt <> 0 OR v_gate.lease_owner IS NOT NULL
       OR v_gate.lease_expires_at IS NOT NULL OR v_gate.started_at IS NOT NULL
       OR v_gate.completed_at IS NOT NULL OR v_gate.failure IS NOT NULL
       OR v_gate.input_manifest_id IS NOT NULL
       OR v_gate.output_proposal_id IS NOT NULL
       OR v_gate.output_revision_id IS NOT NULL
       OR v_gate.status NOT IN (
         'pending','waiting_qualification','cancelled','stale'
       )
       OR (v_run.status = 'waiting_qualification' AND (
         v_gate.status <> 'waiting_qualification'
         OR v_gate.input_authority_id IS NULL
       ))
       OR (v_run.status <> 'waiting_qualification'
         AND v_gate.status = 'waiting_qualification') THEN
      RAISE EXCEPTION 'workflow-engine/v3 run lacks its exact external qualification gate closure'
        USING ERRCODE = '23514';
    END IF;
  ELSE
    IF v_gate.status <> 'completed'
       OR v_gate.output_revision_id <> v_completion.output_revision_id
       OR v_gate.completed_at <> v_completion.completed_at
       OR qualification_handoff_v1_completion_is_exact(
         v_completion.handoff_id
       ) IS NOT TRUE THEN
      RAISE EXCEPTION 'workflow-engine/v3 completed gate lacks exact Handoff closure'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NULL;
END;
$function$;

CREATE FUNCTION validate_qualification_handoff_v1_closure()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_handoff_id uuid;
  v_handoff_ids uuid[] := ARRAY[]::uuid[];
  v_identities uuid[] := ARRAY[]::uuid[];
BEGIN
  IF TG_TABLE_NAME = 'qualification_promotion_v2_revision_transaction_grants' THEN
    IF EXISTS (
      SELECT 1 FROM qualification_promotion_v2_revision_transaction_grants
    ) THEN
      RAISE EXCEPTION 'Qualification Handoff transaction grant survived its Revision insert'
        USING ERRCODE = 'WPH02';
    END IF;
    RETURN NULL;
  ELSIF TG_TABLE_NAME = 'qualification_promotion_v2_handoff_completions' THEN
    v_handoff_ids := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.handoff_id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.handoff_id]
      ELSE ARRAY[OLD.handoff_id,NEW.handoff_id]
    END;
  ELSIF TG_TABLE_NAME = 'qualification_promotion_v2_revision_authority_bindings' THEN
    v_handoff_ids := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.handoff_id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.handoff_id]
      ELSE ARRAY[OLD.handoff_id,NEW.handoff_id]
    END;
  ELSIF TG_TABLE_NAME = 'qualification_promotion_v2_handoff_lineage_members' THEN
    v_handoff_ids := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.handoff_id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.handoff_id]
      ELSE ARRAY[OLD.handoff_id,NEW.handoff_id]
    END;
  ELSIF TG_TABLE_NAME = 'artifact_revisions' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.id]
      ELSE ARRAY[OLD.id,NEW.id]
    END;
    v_handoff_ids := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.promotion_handoff_id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.promotion_handoff_id]
      ELSE ARRAY[OLD.promotion_handoff_id,NEW.promotion_handoff_id]
    END;
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT affected.handoff_id), ARRAY[]::uuid[])
    INTO v_handoff_ids
    FROM (
      SELECT id AS handoff_id
      FROM pg_catalog.unnest(v_handoff_ids) AS direct(id)
      WHERE id IS NOT NULL
      UNION
      SELECT child.promotion_handoff_id
      FROM artifact_revisions AS child
      WHERE child.parent_revision_id = ANY(v_identities)
        AND child.promotion_handoff_id IS NOT NULL
    ) AS affected;
  ELSIF TG_TABLE_NAME = 'artifact_revision_sources' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.revision_id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.revision_id]
      ELSE ARRAY[OLD.revision_id,NEW.revision_id]
    END;
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT promotion_handoff_id), ARRAY[]::uuid[])
    INTO v_handoff_ids
    FROM artifact_revisions
    WHERE id = ANY(v_identities) AND promotion_handoff_id IS NOT NULL;
  ELSIF TG_TABLE_NAME = 'artifact_dependencies' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[
        NEW.source_revision_id,NEW.target_revision_id
      ]
      WHEN TG_OP = 'DELETE' THEN ARRAY[
        OLD.source_revision_id,OLD.target_revision_id
      ]
      ELSE ARRAY[
        OLD.source_revision_id,OLD.target_revision_id,
        NEW.source_revision_id,NEW.target_revision_id
      ]
    END;
    SELECT COALESCE(
      pg_catalog.array_agg(DISTINCT revision.promotion_handoff_id),
      ARRAY[]::uuid[]
    ) INTO v_handoff_ids
    FROM artifact_revisions AS revision
    WHERE revision.id = ANY(v_identities)
      AND revision.promotion_handoff_id IS NOT NULL;
  ELSIF TG_TABLE_NAME = 'trace_links' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[
        NEW.source_revision_id,NEW.target_revision_id
      ]
      WHEN TG_OP = 'DELETE' THEN ARRAY[
        OLD.source_revision_id,OLD.target_revision_id
      ]
      ELSE ARRAY[
        OLD.source_revision_id,OLD.target_revision_id,
        NEW.source_revision_id,NEW.target_revision_id
      ]
    END;
    SELECT COALESCE(
      pg_catalog.array_agg(DISTINCT revision.promotion_handoff_id),
      ARRAY[]::uuid[]
    ) INTO v_handoff_ids
    FROM artifact_revisions AS revision
    WHERE revision.id = ANY(v_identities)
      AND revision.promotion_handoff_id IS NOT NULL;
  ELSIF TG_TABLE_NAME = 'workflow_run_events' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.id]
      ELSE ARRAY[OLD.id,NEW.id]
    END;
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT handoff_id), ARRAY[]::uuid[])
    INTO v_handoff_ids
    FROM qualification_promotion_v2_handoff_completions
    WHERE gate_completed_event_id = ANY(v_identities)
       OR publish_authorization_event_id = ANY(v_identities);
  ELSIF TG_TABLE_NAME = 'outbox_events' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.id]
      ELSE ARRAY[OLD.id,NEW.id]
    END;
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT handoff_id), ARRAY[]::uuid[])
    INTO v_handoff_ids
    FROM qualification_promotion_v2_handoff_completions
    WHERE gate_completed_event_id = ANY(v_identities)
       OR publish_authorization_event_id = ANY(v_identities);
  ELSIF TG_TABLE_NAME = 'workflow_node_runs' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.id]
      ELSE ARRAY[OLD.id,NEW.id]
    END;
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT handoff_id), ARRAY[]::uuid[])
    INTO v_handoff_ids
    FROM qualification_promotion_v2_handoff_completions
    WHERE node_run_id = ANY(v_identities)
       OR publish_node_run_id = ANY(v_identities);
  ELSIF TG_TABLE_NAME = 'workflow_runs' THEN
    v_identities := CASE
      WHEN TG_OP = 'INSERT' THEN ARRAY[NEW.id]
      WHEN TG_OP = 'DELETE' THEN ARRAY[OLD.id]
      ELSE ARRAY[OLD.id,NEW.id]
    END;
    SELECT COALESCE(pg_catalog.array_agg(DISTINCT handoff_id), ARRAY[]::uuid[])
    INTO v_handoff_ids
    FROM qualification_promotion_v2_handoff_completions
    WHERE workflow_run_id = ANY(v_identities);
  END IF;
  FOREACH v_handoff_id IN ARRAY v_handoff_ids LOOP
    CONTINUE WHEN v_handoff_id IS NULL;
    -- Fresh completion inserts can enqueue thousands of per-member deferred
    -- events. The completion and binding triggers each retain a full closure
    -- check, so duplicate lineage events must not turn one bounded pass into
    -- O(N^2). Do not inspect xmin here: the completion function has an
    -- EXCEPTION handler, so its rows carry a subtransaction xmin while
    -- pg_current_xact_id() identifies the top-level transaction. The explicit
    -- immutable creation identity is stable across that boundary. Historical
    -- mutations never take this branch.
    IF TG_TABLE_NAME IN (
         'artifact_revision_sources','artifact_dependencies','trace_links',
         'qualification_promotion_v2_handoff_lineage_members'
       ) AND EXISTS (
         SELECT 1
         FROM qualification_promotion_v2_handoff_completions AS completion
         JOIN qualification_promotion_v2_revision_authority_bindings AS binding
           ON binding.handoff_id = completion.handoff_id
         WHERE completion.handoff_id = v_handoff_id
           AND completion.creation_transaction_id
             = pg_catalog.pg_current_xact_id()::text
           AND binding.creation_transaction_id
             = pg_catalog.pg_current_xact_id()::text
       ) THEN
      CONTINUE;
    END IF;
    IF NOT EXISTS (
         SELECT 1 FROM qualification_promotion_v2_handoff_completions
         WHERE handoff_id = v_handoff_id
       )
       OR qualification_handoff_v1_completion_is_exact(v_handoff_id) IS NOT TRUE THEN
      RAISE EXCEPTION 'Qualification Handoff deferred closure is incomplete or corrupt'
        USING ERRCODE = 'WPH02';
    END IF;
  END LOOP;
  RETURN NULL;
END;
$function$;

CREATE CONSTRAINT TRIGGER qualification_handoff_grant_empty_closure
AFTER INSERT OR UPDATE OR DELETE
ON qualification_promotion_v2_revision_transaction_grants
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_completion_exact_closure
AFTER INSERT ON qualification_promotion_v2_handoff_completions
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_revision_authority_exact_closure
AFTER INSERT ON qualification_promotion_v2_revision_authority_bindings
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_lineage_member_exact_closure
AFTER INSERT ON qualification_promotion_v2_handoff_lineage_members
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_revision_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON artifact_revisions
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_revision_source_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON artifact_revision_sources
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_dependency_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON artifact_dependencies
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_trace_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON trace_links
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_event_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_run_events
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_outbox_exact_closure
AFTER INSERT OR DELETE ON outbox_events
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_node_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_node_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();
CREATE CONSTRAINT TRIGGER qualification_handoff_run_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION validate_qualification_handoff_v1_closure();

COMMENT ON FUNCTION complete_qualification_promotion_v2_handoff(uuid) IS
  'Private session-affine SERIALIZABLE consumer for one immutable Promotion-v2 pending handoff.';
COMMENT ON FUNCTION inspect_qualification_promotion_v2_handoff_completion(uuid) IS
  'Read-write-primary recovery inspection for an immutable Qualification Handoff completion.';

DO $qualification_handoff_v1_security$
DECLARE
  v_schema text := pg_catalog.current_schema();
  v_role text;
  v_signature text;
  v_table text;
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_handoff_operator'
      AND (
        rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole
        OR rolreplication OR rolbypassrls
      )
  ) THEN
    RAISE EXCEPTION 'Qualification Handoff operator must be a private NOLOGIN role'
      USING ERRCODE = '42501';
  END IF;

  FOREACH v_table IN ARRAY ARRAY[
    'qualification_promotion_v2_revision_transaction_grants',
    'qualification_promotion_v2_revision_authority_bindings',
    'qualification_promotion_v2_handoff_lineage_members',
    'qualification_promotion_v2_handoff_completions'
  ] LOOP
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON TABLE %I.%I FROM PUBLIC', v_schema, v_table
    );
    IF EXISTS (
      SELECT 1 FROM pg_catalog.pg_roles
      WHERE rolname = 'worksflow_migration_owner'
    ) THEN
      EXECUTE pg_catalog.format(
        'ALTER TABLE %I.%I OWNER TO worksflow_migration_owner',
        v_schema, v_table
      );
    END IF;
  END LOOP;

  FOREACH v_signature IN ARRAY ARRAY[
    'qualification_handoff_v1_hash(text,bytea)',
    'qualification_handoff_v1_timestamp(timestamptz)',
    'reserve_ordinary_artifact_revision_identity_v1()',
    'reject_artifact_revision_mutation()',
    'reject_qualification_handoff_v1_mutation()',
    'enqueue_qualification_promotion_v2_handoff_v1()',
    'qualification_handoff_v1_quality_result(uuid,uuid)',
    'qualification_handoff_v1_completion_is_exact(uuid)',
    'qualification_handoff_v1_completion_bundle(uuid,boolean,boolean)',
    'inspect_qualification_promotion_v2_handoff_completion(uuid)',
    'complete_qualification_promotion_v2_handoff(uuid)',
    'guard_workflow_execution_profile_v3_run()',
    'guard_external_qualification_gate_node_v3()',
    'validate_workflow_execution_profile_v3_run_closure()',
    'validate_qualification_handoff_v1_closure()'
  ] LOOP
    EXECUTE pg_catalog.format(
      'ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I',
      v_schema, v_signature, v_schema
    );
    EXECUTE pg_catalog.format(
      'REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC', v_schema, v_signature
    );
    IF EXISTS (
      SELECT 1 FROM pg_catalog.pg_roles
      WHERE rolname = 'worksflow_migration_owner'
    ) THEN
      EXECUTE pg_catalog.format(
        'ALTER FUNCTION %I.%s OWNER TO worksflow_migration_owner',
        v_schema, v_signature
      );
    END IF;
  END LOOP;

  -- This is the sole migration-81 routine whose body migration82 must extend.
  -- Preserve migration81's frozen pg_temp-last proconfig exactly; the generic
  -- Handoff helper setting above intentionally omits pg_temp.
  EXECUTE pg_catalog.format(
    'ALTER FUNCTION %I.reserve_ordinary_artifact_revision_identity_v1() '
    'SET search_path TO pg_catalog, %I, pg_temp', v_schema, v_schema
  );

  FOREACH v_role IN ARRAY ARRAY[
    'worksflow_application','worksflow_schema_migrator','worksflow_auditor',
    'worksflow_qualification_evidence_operator',
    'worksflow_qualification_plan_operator',
    'worksflow_qualification_policy_operator',
    'worksflow_workflow_input_authority_operator',
    'worksflow_qualification_receipt_operator',
    'worksflow_qualification_input_precommit_operator',
    'worksflow_qualification_source_verifier_operator',
    'worksflow_qualification_credential_resolver_operator',
    'worksflow_qualification_promotion_operator',
    'worksflow_qualification_handoff_operator'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_role) THEN
      FOREACH v_table IN ARRAY ARRAY[
        'qualification_promotion_v2_revision_transaction_grants',
        'qualification_promotion_v2_revision_authority_bindings',
        'qualification_promotion_v2_handoff_lineage_members',
        'qualification_promotion_v2_handoff_completions'
      ] LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON TABLE %I.%I FROM %I', v_schema, v_table, v_role
        );
      END LOOP;
      FOREACH v_signature IN ARRAY ARRAY[
        'qualification_handoff_v1_hash(text,bytea)',
        'qualification_handoff_v1_timestamp(timestamptz)',
        'reserve_ordinary_artifact_revision_identity_v1()',
        'reject_artifact_revision_mutation()',
        'reject_qualification_handoff_v1_mutation()',
        'enqueue_qualification_promotion_v2_handoff_v1()',
        'qualification_handoff_v1_quality_result(uuid,uuid)',
        'qualification_handoff_v1_completion_is_exact(uuid)',
        'qualification_handoff_v1_completion_bundle(uuid,boolean,boolean)',
        'inspect_qualification_promotion_v2_handoff_completion(uuid)',
        'complete_qualification_promotion_v2_handoff(uuid)',
        'guard_workflow_execution_profile_v3_run()',
        'guard_external_qualification_gate_node_v3()',
        'validate_workflow_execution_profile_v3_run_closure()',
        'validate_qualification_handoff_v1_closure()'
      ] LOOP
        EXECUTE pg_catalog.format(
          'REVOKE ALL ON FUNCTION %I.%s FROM %I',
          v_schema, v_signature, v_role
        );
      END LOOP;
    END IF;
  END LOOP;

  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles
    WHERE rolname = 'worksflow_qualification_handoff_operator'
  ) THEN
    EXECUTE pg_catalog.format(
      'GRANT USAGE ON SCHEMA %I TO worksflow_qualification_handoff_operator',
      v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.complete_qualification_promotion_v2_handoff(uuid) '
      'TO worksflow_qualification_handoff_operator', v_schema
    );
    EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.inspect_qualification_promotion_v2_handoff_completion(uuid) '
      'TO worksflow_qualification_handoff_operator', v_schema
    );
  END IF;
END;
$qualification_handoff_v1_security$;
