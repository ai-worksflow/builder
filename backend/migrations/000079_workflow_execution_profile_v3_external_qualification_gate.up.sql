-- Register the durable state vocabulary for the separately frozen
-- workflow-engine/v3 descriptor. The Current authoring/runtime alias remains
-- v2 until the private Promotion v2 and handoff consumers are sealed.

-- The Workflow Input issuer takes this key before its first relation access,
-- then takes the project mutex before mutable row locks. Taking the same key
-- before any DDL lock prevents a rolling migration deadlock.
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1', 0)
);
LOCK TABLE projects IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_node_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_definition_versions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_policy_authorities IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_policy_review_defaults IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_policy_exact_approved_sources IN ACCESS EXCLUSIVE MODE;
LOCK TABLE qualification_policy_identity_reservations IN ACCESS EXCLUSIVE MODE;
LOCK TABLE workflow_input_authorities IN ACCESS EXCLUSIVE MODE;

-- PostgreSQL deliberately enforces a conservative database-admission subset
-- of the frozen Go validator. The Go seam also proves arbitrary JSON-Schema
-- implication and semantic path state; duplicating those algorithms in
-- PL/pgSQL would create a second, drifting validator. Database admission is
-- therefore narrower: canonical/hash-bound definitions, the two registered
-- contracts, registered closed configs, open-object default ports, empty edge
-- mappings, and one of two frozen linear semantic signatures. This is a
-- one-way admission proof, not a complete SQL port: false may still describe a
-- definition accepted by Go, while true is confined to the byte-stable subset
-- exercised through the frozen Go validation seam.
CREATE FUNCTION workflow_execution_profile_v3_definition_is_database_admissible(p_document jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
DECLARE
  v_nodes jsonb;
  v_edges jsonb;
  v_node jsonb;
  v_edge jsonb;
  v_config jsonb;
  v_type text;
  v_config_key text;
  v_count integer;
  v_entry_id text;
  v_terminal_id text;
  v_workbench_id text;
  v_quality_id text;
  v_external_id text;
  v_publish_id text;
  v_external jsonb;
  v_path_signature text;
BEGIN
  IF jsonb_typeof(p_document) <> 'object'
     OR p_document - ARRAY[
       'id','version','name','schemaVersion','executionProfile','nodes','edges',
       'inputContract','outputContract','hash','createdBy','createdAt'
     ] <> '{}'::jsonb
     OR jsonb_typeof(p_document->'id') IS DISTINCT FROM 'string'
     OR p_document->>'id' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR jsonb_typeof(p_document->'version') IS DISTINCT FROM 'number'
     OR p_document->>'version' !~ '^[1-9][0-9]{0,8}$'
     OR jsonb_typeof(p_document->'name') IS DISTINCT FROM 'string'
     OR p_document->>'name' = '' OR p_document->>'name' <> btrim(p_document->>'name')
     OR jsonb_typeof(p_document->'schemaVersion') IS DISTINCT FROM 'string'
     OR p_document->>'schemaVersion' !~ '^[1-9][0-9]{0,8}$'
     OR jsonb_typeof(p_document->'hash') IS DISTINCT FROM 'string'
     OR p_document->>'hash' !~ '^[0-9a-f]{64}$'
     OR p_document->>'hash' IS DISTINCT FROM pg_catalog.encode(
       pg_catalog.sha256(workflow_input_canonical_jsonb_bytes(p_document - 'hash')), 'hex'
     )
     OR jsonb_typeof(p_document->'createdBy') IS DISTINCT FROM 'string'
     OR p_document->>'createdBy' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     OR jsonb_typeof(p_document->'createdAt') IS DISTINCT FROM 'string'
     OR p_document->>'createdAt' !~ '^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]{0,8}[1-9])?Z$'
     OR jsonb_typeof(p_document->'executionProfile') IS DISTINCT FROM 'object'
     OR (p_document->'executionProfile') - ARRAY['hash','version'] <> '{}'::jsonb
     OR p_document#>>'{executionProfile,version}' IS DISTINCT FROM 'workflow-engine/v3'
     OR p_document#>>'{executionProfile,hash}' IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR jsonb_typeof(p_document->'nodes') IS DISTINCT FROM 'array'
     OR jsonb_typeof(p_document->'edges') IS DISTINCT FROM 'array'
     OR jsonb_typeof(p_document->'inputContract') IS DISTINCT FROM 'object'
     OR p_document->'inputContract' NOT IN (
       '{"capability":"project_brief","manifestJobTypes":["conversation.workflow_intent","workflow_start"],"artifactKinds":["project_brief"],"minimumArtifacts":1,"maximumArtifacts":1,"requireApproved":false,"requiredSourcePurposes":["project_brief"],"manifestSchemaContracts":{"conversation.workflow_intent":"workflow-intent-input/v1","workflow_start":"workflow-input/v1"}}'::jsonb,
       '{"capability":"blueprint_selection","manifestJobTypes":["blueprint.selection"],"artifactKinds":["blueprint"],"minimumArtifacts":2,"maximumArtifacts":101,"requireApproved":true,"requiredSourcePurposes":["blueprint_selection_node","blueprint_selection_root"],"manifestSchemaContracts":{"blueprint.selection":"blueprint-selection/v1"}}'::jsonb
     )
     OR p_document->'outputContract' IS DISTINCT FROM
       '{"capability":"application","producedArtifactKinds":["workspace"],"terminalOutcome":"deployment","terminalNodeType":"publish"}'::jsonb THEN
    RETURN false;
  END IF;
  -- Go's time.Time JSON decoder rejects calendar/time overflows even when the
  -- surface text matches RFC3339. The exception fence below turns any failed
  -- cast into a conservative rejection.
  PERFORM (p_document->>'createdAt')::timestamptz;

  -- encoding/json escapes HTML-sensitive runes plus U+2028/U+2029 while
  -- PostgreSQL jsonb text leaves them literal. Reject those strings (and
  -- controls) so a caller cannot bind a PostgreSQL-only hash that changes when
  -- the Go loader unmarshals and recomputes WorkflowDefinition.Hash.
  IF EXISTS (
    WITH RECURSIVE document_values(value) AS (
      SELECT p_document
      UNION ALL
      SELECT child.value
      FROM document_values AS parent
      CROSS JOIN LATERAL (
        SELECT member.value
        FROM jsonb_each(CASE WHEN jsonb_typeof(parent.value) = 'object'
          THEN parent.value ELSE '{}'::jsonb END) AS member
        UNION ALL
        SELECT member.value
        FROM jsonb_array_elements(CASE WHEN jsonb_typeof(parent.value) = 'array'
          THEN parent.value ELSE '[]'::jsonb END) AS member(value)
      ) AS child
    )
    SELECT 1
    FROM document_values
    WHERE jsonb_typeof(value) = 'string'
      AND (
        value#>>'{}' ~ '[[:cntrl:]]'
        OR strpos(value#>>'{}', '<') > 0
        OR strpos(value#>>'{}', '>') > 0
        OR strpos(value#>>'{}', '&') > 0
        OR strpos(value#>>'{}', chr(8232)) > 0
        OR strpos(value#>>'{}', chr(8233)) > 0
      )
  ) THEN
    RETURN false;
  END IF;
  v_nodes := p_document->'nodes';
  v_edges := p_document->'edges';

  IF jsonb_array_length(v_nodes) NOT BETWEEN 5 AND 200
     OR jsonb_array_length(v_edges) > 1000
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
      WHERE value->>'type' = 'workbench_build') <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
      WHERE value->>'type' = 'quality_gate') <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
      WHERE value->>'type' = 'external_qualification_gate') <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
      WHERE value->>'type' = 'publish') <> 1 THEN
    RETURN false;
  END IF;

  FOR v_node IN SELECT value FROM jsonb_array_elements(v_nodes) AS node(value) LOOP
    IF jsonb_typeof(v_node) IS DISTINCT FROM 'object'
       OR v_node - ARRAY[
         'id','name','type','inputSchema','outputSchema','inputPorts','outputPorts',
         'artifactInput','aiTransform','humanEdit','reviewGate','condition','fanOut',
         'merge','manifestCompiler','workbenchBuild','publish','externalQualificationGate',
         'ai','humanTask','approval','transform','qualityGate','delivery'
       ] <> '{}'::jsonb
       OR jsonb_typeof(v_node->'id') IS DISTINCT FROM 'string'
       OR v_node->>'id' = '' OR v_node->>'id' <> btrim(v_node->>'id')
       OR octet_length(v_node->>'id') > 256
       OR v_node->>'id' !~ '^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$'
       OR jsonb_typeof(v_node->'name') IS DISTINCT FROM 'string'
       OR v_node->>'name' = '' OR v_node->>'name' <> btrim(v_node->>'name')
       OR jsonb_typeof(v_node->'type') IS DISTINCT FROM 'string'
       OR v_node->>'type' NOT IN (
         'artifact_input','ai_transform','human_edit','review_gate','fan_out','merge',
         'manifest_compiler','workbench_build','quality_gate',
         'external_qualification_gate','publish','transform'
       )
       OR v_node->'inputSchema' IS DISTINCT FROM '{"type":"object","additionalProperties":true}'::jsonb
       OR v_node->'outputSchema' IS DISTINCT FROM '{"type":"object","additionalProperties":true}'::jsonb
       OR v_node ? 'inputPorts' OR v_node ? 'outputPorts' THEN
      RETURN false;
    END IF;

    v_type := v_node->>'type';
    v_config_key := CASE v_type
      WHEN 'artifact_input' THEN 'artifactInput'
      WHEN 'ai_transform' THEN 'aiTransform'
      WHEN 'human_edit' THEN 'humanEdit'
      WHEN 'review_gate' THEN 'reviewGate'
      WHEN 'fan_out' THEN 'fanOut'
      WHEN 'merge' THEN 'merge'
      WHEN 'manifest_compiler' THEN 'manifestCompiler'
      WHEN 'workbench_build' THEN 'workbenchBuild'
      WHEN 'quality_gate' THEN 'qualityGate'
      WHEN 'external_qualification_gate' THEN 'externalQualificationGate'
      WHEN 'publish' THEN 'publish'
      WHEN 'transform' THEN 'transform'
      ELSE NULL
    END;
    SELECT count(*) INTO v_count
    FROM unnest(ARRAY[
      'artifactInput','aiTransform','humanEdit','reviewGate','condition','fanOut',
      'merge','manifestCompiler','workbenchBuild','publish','externalQualificationGate',
      'ai','humanTask','approval','transform','qualityGate','delivery'
    ]) AS config_key(value)
    WHERE v_node ? config_key.value;
    IF v_config_key IS NULL OR v_count <> 1
       OR jsonb_typeof(v_node->v_config_key) IS DISTINCT FROM 'object' THEN
      RETURN false;
    END IF;
    v_config := v_node->v_config_key;

    CASE v_type
      WHEN 'artifact_input' THEN
        IF (p_document->'inputContract'->>'capability' = 'project_brief'
            AND v_config IS DISTINCT FROM '{"allowedTypes":["document"],"allowedKinds":["project_brief"],"requireApproved":false,"minimumArtifacts":1,"maximumArtifacts":1}'::jsonb)
           OR (p_document->'inputContract'->>'capability' = 'blueprint_selection'
            AND v_config IS DISTINCT FROM '{"allowedTypes":["blueprint"],"allowedKinds":["blueprint"],"requireApproved":true,"minimumArtifacts":2,"maximumArtifacts":101}'::jsonb) THEN
          RETURN false;
        END IF;
      WHEN 'ai_transform' THEN
        IF v_config - ARRAY['jobType','modelPolicy','outputSchemaVersion','maxAttempts','timeout'] <> '{}'::jsonb
           OR jsonb_typeof(v_config->'jobType') IS DISTINCT FROM 'string'
           OR jsonb_typeof(v_config->'modelPolicy') IS DISTINCT FROM 'string'
           OR v_config->>'modelPolicy' <> 'project-default'
           OR jsonb_typeof(v_config->'outputSchemaVersion') IS DISTINCT FROM 'string'
           OR jsonb_typeof(v_config->'maxAttempts') IS DISTINCT FROM 'number'
           OR v_config->>'maxAttempts' !~ '^[1-9][0-9]{0,8}$'
           OR jsonb_typeof(v_config->'timeout') IS DISTINCT FROM 'number'
           OR v_config->>'timeout' !~ '^[1-9][0-9]{0,15}$'
           OR (v_config->>'jobType', v_config->>'outputSchemaVersion') NOT IN (
             ('refine_project_brief','project-brief-proposal/v1'),
             ('derive_requirements','requirements-proposal/v1'),
             ('decompose_pages','blueprint-proposal/v1'),
             ('generate_page_spec','page-spec-proposal/v1'),
             ('generate_prototype','prototype-proposal/v1')
           ) THEN
          RETURN false;
        END IF;
      WHEN 'human_edit' THEN
        IF v_config - ARRAY['artifactType','artifactKind','requiredRole','instructions'] <> '{}'::jsonb
           OR jsonb_typeof(v_config->'artifactType') IS DISTINCT FROM 'string'
           OR jsonb_typeof(v_config->'artifactKind') IS DISTINCT FROM 'string'
           OR jsonb_typeof(v_config->'requiredRole') IS DISTINCT FROM 'string'
           OR v_config->>'requiredRole' NOT IN ('owner','admin','editor','commenter','viewer')
           OR (v_config ? 'instructions' AND (
             jsonb_typeof(v_config->'instructions') IS DISTINCT FROM 'string' OR v_config->>'instructions' = ''
           ))
           OR (v_config->>'artifactType', v_config->>'artifactKind') NOT IN (
             ('document','project_brief'),('document','product_requirements'),
             ('blueprint','blueprint'),('blueprint','page_spec'),
             ('prototype','prototype')
           ) THEN
          RETURN false;
        END IF;
      WHEN 'review_gate' THEN
        IF v_config - ARRAY['requiredRole','minimumApprovals','prohibitSelfReview','allowWaiver'] <> '{}'::jsonb
           OR jsonb_typeof(v_config->'requiredRole') IS DISTINCT FROM 'string'
           OR v_config->>'requiredRole' NOT IN ('owner','admin','editor','commenter','viewer')
           OR jsonb_typeof(v_config->'minimumApprovals') IS DISTINCT FROM 'number'
           OR v_config->>'minimumApprovals' !~ '^[1-9][0-9]{0,8}$'
           OR v_config->'prohibitSelfReview' IS DISTINCT FROM 'true'::jsonb
           OR v_config->'allowWaiver' IS DISTINCT FROM 'false'::jsonb THEN
          RETURN false;
        END IF;
      WHEN 'fan_out' THEN
        IF v_config - ARRAY['itemsPath','sliceKeyPath','mergeNodeId','maxParallel','maxItems','itemKind'] <> '{}'::jsonb
           OR jsonb_typeof(v_config->'itemsPath') IS DISTINCT FROM 'string' OR v_config->>'itemsPath' !~ '^/'
           OR jsonb_typeof(v_config->'sliceKeyPath') IS DISTINCT FROM 'string' OR v_config->>'sliceKeyPath' !~ '^/'
           OR jsonb_typeof(v_config->'mergeNodeId') IS DISTINCT FROM 'string' OR v_config->>'mergeNodeId' = ''
           OR jsonb_typeof(v_config->'maxParallel') IS DISTINCT FROM 'number' OR v_config->>'maxParallel' !~ '^[1-9][0-9]{0,8}$'
           OR jsonb_typeof(v_config->'maxItems') IS DISTINCT FROM 'number' OR v_config->>'maxItems' !~ '^[1-9][0-9]?$|^100$'
           OR jsonb_typeof(v_config->'itemKind') IS DISTINCT FROM 'string'
           OR v_config->>'itemKind' NOT IN ('blueprint_page','blueprint_selection_page') THEN
          RETURN false;
        END IF;
      WHEN 'merge' THEN
        IF v_config - ARRAY['fanOutNodeId','policy','quorum','allowWaiver'] <> '{}'::jsonb
           OR jsonb_typeof(v_config->'fanOutNodeId') IS DISTINCT FROM 'string' OR v_config->>'fanOutNodeId' = ''
           OR jsonb_typeof(v_config->'policy') IS DISTINCT FROM 'string' OR v_config->>'policy' <> 'all'
           OR v_config->'allowWaiver' IS DISTINCT FROM 'false'::jsonb
           OR v_config ? 'quorum' THEN
          RETURN false;
        END IF;
      WHEN 'manifest_compiler' THEN
        IF v_config IS DISTINCT FROM '{"manifestKind":"application_build","schemaVersion":1,"hook":"application-build-manifest/v1"}'::jsonb THEN
          RETURN false;
        END IF;
      WHEN 'workbench_build' THEN
        IF v_config - ARRAY['buildManifestSchemaVersion','maxAttempts','timeout'] <> '{}'::jsonb
           OR v_config->'buildManifestSchemaVersion' IS DISTINCT FROM '1'::jsonb
           OR jsonb_typeof(v_config->'maxAttempts') IS DISTINCT FROM 'number' OR v_config->>'maxAttempts' !~ '^[1-9][0-9]{0,8}$'
           OR jsonb_typeof(v_config->'timeout') IS DISTINCT FROM 'number' OR v_config->>'timeout' !~ '^[1-9][0-9]{0,15}$' THEN
          RETURN false;
        END IF;
      WHEN 'quality_gate' THEN
        IF v_config - ARRAY['gateName','blocking','requiredRole'] <> '{}'::jsonb
           OR jsonb_typeof(v_config->'gateName') IS DISTINCT FROM 'string'
           OR v_config->>'gateName' <> 'release' OR v_config->'blocking' IS DISTINCT FROM 'true'::jsonb
           OR (v_config ? 'requiredRole' AND (
             jsonb_typeof(v_config->'requiredRole') IS DISTINCT FROM 'string'
             OR v_config->>'requiredRole' NOT IN ('owner','admin','editor','commenter','viewer')
           )) THEN
          RETURN false;
        END IF;
      WHEN 'external_qualification_gate' THEN
        IF v_config IS DISTINCT FROM jsonb_build_object(
          'blocking', true,
          'gateName', 'external-qualification',
          'inputAuthoritySchema', 'worksflow-workflow-input-authority/v1',
          'promotionProtocol', 'worksflow-qualification-promotion-consume/v2',
          'receiptSchema', 'worksflow-qualification-receipt/v3',
          'waiverPolicy', 'never'
        ) THEN
          RETURN false;
        END IF;
      WHEN 'publish' THEN
        IF v_config - ARRAY['environment','requiredRole','allowRollback'] <> '{}'::jsonb
           OR jsonb_typeof(v_config->'environment') IS DISTINCT FROM 'string'
           OR v_config->>'environment' <> 'production'
           OR jsonb_typeof(v_config->'requiredRole') IS DISTINCT FROM 'string'
           OR v_config->>'requiredRole' NOT IN ('owner','admin','editor','commenter','viewer')
           OR jsonb_typeof(v_config->'allowRollback') IS DISTINCT FROM 'boolean' THEN
          RETURN false;
        END IF;
      WHEN 'transform' THEN
        IF v_config IS DISTINCT FROM '{"transform":"selection_passthrough"}'::jsonb THEN
          RETURN false;
        END IF;
      ELSE
        RETURN false;
    END CASE;
  END LOOP;

  SELECT value->>'id' INTO v_workbench_id
  FROM jsonb_array_elements(v_nodes) AS node(value)
  WHERE value->>'type' = 'workbench_build';
  SELECT value->>'id' INTO v_quality_id
  FROM jsonb_array_elements(v_nodes) AS node(value)
  WHERE value->>'type' = 'quality_gate';
  SELECT value->>'id', value INTO v_external_id, v_external
  FROM jsonb_array_elements(v_nodes) AS node(value)
  WHERE value->>'type' = 'external_qualification_gate';
  SELECT value->>'id' INTO v_publish_id
  FROM jsonb_array_elements(v_nodes) AS node(value)
  WHERE value->>'type' = 'publish';

  IF v_workbench_id IS NULL OR v_quality_id IS NULL OR v_external_id IS NULL OR v_publish_id IS NULL
     OR v_external_id <> 'external-qualification'
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)) <>
        (SELECT count(DISTINCT value->>'id') FROM jsonb_array_elements(v_nodes) AS node(value)) THEN
    RETURN false;
  END IF;

  FOR v_edge IN SELECT value FROM jsonb_array_elements(v_edges) AS edge(value) LOOP
    IF jsonb_typeof(v_edge) IS DISTINCT FROM 'object'
       OR v_edge - ARRAY['id','from','fromPort','to','toPort','mapping'] <> '{}'::jsonb
       OR jsonb_typeof(v_edge->'id') IS DISTINCT FROM 'string'
       OR v_edge->>'id' = '' OR v_edge->>'id' <> btrim(v_edge->>'id')
       OR octet_length(v_edge->>'id') > 256
       OR jsonb_typeof(v_edge->'from') IS DISTINCT FROM 'string' OR v_edge->>'from' = ''
       OR jsonb_typeof(v_edge->'to') IS DISTINCT FROM 'string' OR v_edge->>'to' = ''
       OR v_edge->>'from' = v_edge->>'to'
       OR (v_edge ? 'fromPort' AND (
         jsonb_typeof(v_edge->'fromPort') IS DISTINCT FROM 'string' OR v_edge->>'fromPort' <> 'default'
       ))
       OR (v_edge ? 'toPort' AND (
         jsonb_typeof(v_edge->'toPort') IS DISTINCT FROM 'string' OR v_edge->>'toPort' <> 'default'
       ))
       OR v_edge ? 'mapping'
       OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
         WHERE value->>'id' = v_edge->>'from')
       OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
         WHERE value->>'id' = v_edge->>'to') THEN
      RETURN false;
    END IF;
  END LOOP;
  IF (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)) <>
       (SELECT count(DISTINCT value->>'id') FROM jsonb_array_elements(v_edges) AS edge(value))
     OR jsonb_array_length(v_edges) <> jsonb_array_length(v_nodes) - 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
         WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
           WHERE edge.value->>'to' = node.value->>'id')) <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
         WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
           WHERE edge.value->>'from' = node.value->>'id')) <> 1
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
              WHERE edge.value->>'to' = node.value->>'id') > 1
          OR (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
              WHERE edge.value->>'from' = node.value->>'id') > 1
     ) THEN
    RETURN false;
  END IF;
  SELECT node.value->>'id' INTO v_entry_id
  FROM jsonb_array_elements(v_nodes) AS node(value)
  WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
    WHERE edge.value->>'to' = node.value->>'id');
  SELECT node.value->>'id' INTO v_terminal_id
  FROM jsonb_array_elements(v_nodes) AS node(value)
  WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
    WHERE edge.value->>'from' = node.value->>'id');
  WITH RECURSIVE reachable(id) AS (
    SELECT v_entry_id
    UNION
    SELECT edge.value->>'to'
    FROM reachable
    JOIN LATERAL jsonb_array_elements(v_edges) AS edge(value)
      ON edge.value->>'from' = reachable.id
  )
  SELECT count(*) INTO v_count FROM reachable;
  IF v_count <> jsonb_array_length(v_nodes)
     OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE value->>'id' = v_entry_id AND value->>'type' = 'artifact_input')
     OR v_terminal_id <> v_publish_id
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE value->>'type' = 'artifact_input') <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE value->>'type' = 'manifest_compiler') <> 1
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE value->>'type' = 'ai_transform'
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
           JOIN LATERAL jsonb_array_elements(v_nodes) AS successor(value)
             ON successor.value->>'id' = edge.value->>'to'
           WHERE edge.value->>'from' = node.value->>'id'
             AND successor.value->>'type' = 'human_edit'
         )
     )
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE value->>'type' = 'human_edit'
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
           JOIN LATERAL jsonb_array_elements(v_nodes) AS successor(value)
             ON successor.value->>'id' = edge.value->>'to'
           WHERE edge.value->>'from' = node.value->>'id'
             AND successor.value->>'type' = 'review_gate'
         )
     )
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE value->>'type' = 'fan_out'
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(v_nodes) AS merge_node(value)
           WHERE merge_node.value->>'type' = 'merge'
             AND merge_node.value->>'id' = node.value#>>'{fanOut,mergeNodeId}'
             AND merge_node.value#>>'{merge,fanOutNodeId}' = node.value->>'id'
         )
     )
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_nodes) AS node(value)
       WHERE value->>'type' = 'merge'
         AND NOT EXISTS (
           SELECT 1 FROM jsonb_array_elements(v_nodes) AS fan_out_node(value)
           WHERE fan_out_node.value->>'type' = 'fan_out'
             AND fan_out_node.value->>'id' = node.value#>>'{merge,fanOutNodeId}'
             AND fan_out_node.value#>>'{fanOut,mergeNodeId}' = node.value->>'id'
         )
     ) THEN
    RETURN false;
  END IF;

  -- The full Go validator performs semantic lineage analysis. Admission is
  -- intentionally narrower than that language: these two linear signatures
  -- are the frozen, semantically proven Project Brief and Blueprint-selection
  -- delivery paths. Config validation above pins the capability behind every
  -- signature element; arbitrary node reordering therefore cannot enter the
  -- catalog merely because its graph is structurally connected.
  WITH RECURSIVE ordered_path(id, depth) AS (
    SELECT v_entry_id, 1
    UNION ALL
    SELECT edge.value->>'to', ordered_path.depth + 1
    FROM ordered_path
    JOIN LATERAL jsonb_array_elements(v_edges) AS edge(value)
      ON edge.value->>'from' = ordered_path.id
  )
  SELECT string_agg(
    CASE node.value->>'type'
      WHEN 'artifact_input' THEN 'artifact_input'
      WHEN 'ai_transform' THEN 'ai_transform:' || (node.value#>>'{aiTransform,jobType}')
      WHEN 'human_edit' THEN 'human_edit:' || (node.value#>>'{humanEdit,artifactKind}')
      WHEN 'review_gate' THEN 'review_gate'
      WHEN 'fan_out' THEN 'fan_out:' || (node.value#>>'{fanOut,itemKind}')
      WHEN 'merge' THEN 'merge'
      WHEN 'manifest_compiler' THEN 'manifest_compiler'
      WHEN 'workbench_build' THEN 'workbench_build'
      WHEN 'quality_gate' THEN 'quality_gate:release'
      WHEN 'external_qualification_gate' THEN 'external_qualification_gate'
      WHEN 'publish' THEN 'publish:production'
      WHEN 'transform' THEN 'transform:' || (node.value#>>'{transform,transform}')
      ELSE 'unsupported'
    END,
    '>' ORDER BY ordered_path.depth
  ) INTO v_path_signature
  FROM ordered_path
  JOIN LATERAL jsonb_array_elements(v_nodes) AS node(value)
    ON node.value->>'id' = ordered_path.id;
  IF (p_document->'inputContract'->>'capability' = 'project_brief'
      AND v_path_signature IS DISTINCT FROM
        'artifact_input>ai_transform:refine_project_brief>human_edit:project_brief>review_gate>ai_transform:derive_requirements>human_edit:product_requirements>review_gate>ai_transform:decompose_pages>human_edit:blueprint>review_gate>fan_out:blueprint_page>ai_transform:generate_page_spec>human_edit:page_spec>review_gate>ai_transform:generate_prototype>human_edit:prototype>review_gate>merge>manifest_compiler>workbench_build>quality_gate:release>external_qualification_gate>publish:production')
     OR (p_document->'inputContract'->>'capability' = 'blueprint_selection'
      AND v_path_signature IS DISTINCT FROM
        'artifact_input>fan_out:blueprint_selection_page>transform:selection_passthrough>merge>manifest_compiler>workbench_build>quality_gate:release>external_qualification_gate>publish:production') THEN
    RETURN false;
  END IF;

  -- The dedicated tail remains exact inside the conservative linear graph.
  IF (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'from' = v_workbench_id) <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'to' = v_quality_id) <> 1
     OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'from' = v_workbench_id AND value->>'to' = v_quality_id)
     OR (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'from' = v_quality_id) <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'to' = v_external_id) <> 1
     OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'from' = v_quality_id AND value->>'to' = v_external_id)
     OR (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'from' = v_external_id) <> 1
     OR (SELECT count(*) FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'to' = v_publish_id) <> 1
     OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'from' = v_external_id AND value->>'to' = v_publish_id)
     OR EXISTS (SELECT 1 FROM jsonb_array_elements(v_edges) AS edge(value)
      WHERE value->>'from' = v_publish_id) THEN
    RETURN false;
  END IF;
  RETURN true;
EXCEPTION WHEN others THEN
  RETURN false;
END;
$function$;

CREATE FUNCTION guard_workflow_execution_profile_v3_definition()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
BEGIN
  IF NEW.execution_profile_version = 'workflow-engine/v3'
     OR NEW.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(COALESCE(NEW.content->'nodes','[]'::jsonb)) AS node(value)
       WHERE value->>'type' = 'external_qualification_gate'
     ) THEN
    IF NEW.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
       OR NEW.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
       OR NEW.content_hash IS DISTINCT FROM NEW.content->>'hash'
       OR workflow_execution_profile_v3_definition_is_database_admissible(NEW.content) IS NOT TRUE THEN
      RAISE EXCEPTION 'workflow-engine/v3 definition or external qualification topology is not exact'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER workflow_execution_profile_v3_definition_guard
BEFORE INSERT OR UPDATE OF content, content_hash, execution_profile_version, execution_profile_hash
ON workflow_definition_versions
FOR EACH ROW EXECUTE FUNCTION guard_workflow_execution_profile_v3_definition();

CREATE FUNCTION guard_workflow_execution_profile_v3_run()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
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
           AND workflow_execution_profile_v3_definition_is_database_admissible(version.content) IS TRUE
       ) THEN
      RAISE EXCEPTION 'workflow run does not bind the exact workflow-engine/v3 definition'
        USING ERRCODE = '23514';
    END IF;
    -- Until migration 000082 installs the private handoff consumer, no v3
    -- run can legally complete: its dedicated gate itself cannot yet complete.
    IF NEW.status = 'completed' THEN
      RAISE EXCEPTION 'workflow-engine/v3 cannot complete before the private qualification handoff'
        USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER workflow_execution_profile_v3_run_guard
BEFORE INSERT OR UPDATE OF definition_version_id, execution_profile_version, execution_profile_hash, status
ON workflow_runs
FOR EACH ROW EXECUTE FUNCTION guard_workflow_execution_profile_v3_run();

CREATE FUNCTION guard_external_qualification_gate_node_v3()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_run workflow_runs%ROWTYPE;
  v_definition jsonb;
  v_definition_type text;
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
    FROM jsonb_array_elements(COALESCE(v_definition->'nodes','[]'::jsonb)) AS node(value)
    WHERE value->>'id' = NEW.definition_node_id;
  END IF;

  IF NEW.node_type = 'external_qualification_gate'
     OR NEW.status = 'waiting_qualification'
     OR NEW.input_authority_id IS NOT NULL THEN
    IF v_run.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
       OR v_run.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
       OR workflow_execution_profile_v3_definition_is_database_admissible(v_definition) IS NOT TRUE
       OR NEW.node_type IS DISTINCT FROM 'external_qualification_gate'
       OR v_definition_type IS DISTINCT FROM 'external_qualification_gate'
       OR NEW.definition_node_id IS DISTINCT FROM 'external-qualification'
       OR NEW.node_key IS DISTINCT FROM 'external-qualification'
       OR NEW.slice_kind IS DISTINCT FROM 'root' OR NEW.slice_id IS NOT NULL
       OR NEW.attempt <> 0 OR NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS NOT NULL OR NEW.completed_at IS NOT NULL OR NEW.failure IS NOT NULL
       OR NEW.input_manifest_id IS NOT NULL
       OR NEW.output_proposal_id IS NOT NULL
       OR NEW.status NOT IN ('pending','waiting_qualification','cancelled','stale')
       OR (TG_OP = 'INSERT' AND NEW.status <> 'pending')
       OR (TG_OP = 'UPDATE' AND OLD.node_type = 'external_qualification_gate' AND (
         (OLD.status = 'pending' AND NEW.status NOT IN ('pending','waiting_qualification','cancelled','stale'))
         OR (OLD.status = 'waiting_qualification' AND NEW.status NOT IN ('waiting_qualification','cancelled','stale'))
         OR (OLD.status IN ('cancelled','stale') AND NEW.status <> OLD.status)
       ))
       OR (NEW.status = 'pending' AND NEW.input_authority_id IS NOT NULL)
       OR (NEW.status = 'waiting_qualification' AND (
         NEW.input_authority_id IS NULL
         OR NOT EXISTS (
           SELECT 1 FROM workflow_input_authorities AS authority
           WHERE authority.authority_id = NEW.input_authority_id
             AND authority.workflow_run_id = NEW.run_id
             AND authority.node_run_id = NEW.id
         )
       ))
       OR NEW.output_revision_id IS NOT NULL THEN
      RAISE EXCEPTION 'dedicated external qualification gate cannot use a generic workflow transition'
        USING ERRCODE = '23514';
    END IF;
  ELSIF v_run.execution_profile_version = 'workflow-engine/v3'
        AND (NEW.definition_node_id IS NULL OR v_definition_type IS DISTINCT FROM NEW.node_type) THEN
    RAISE EXCEPTION 'workflow-engine/v3 node does not match its stable definition identity'
      USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE TRIGGER external_qualification_gate_node_v3_guard
BEFORE INSERT OR UPDATE ON workflow_node_runs
FOR EACH ROW EXECUTE FUNCTION guard_external_qualification_gate_node_v3();

-- Triggers govern future writes; they do not bless rows written by a binary
-- that raced ahead of this migration. Refuse the upgrade unless every
-- pre-existing v3/profile/gate-shaped row already satisfies the same closed
-- identity and topology predicates.
DO $workflow_execution_profile_v3_existing_guard$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM workflow_definition_versions AS version
    WHERE (
      version.execution_profile_version = 'workflow-engine/v3'
      OR version.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
      OR EXISTS (
        SELECT 1
        FROM jsonb_array_elements(
          CASE WHEN jsonb_typeof(version.content->'nodes') = 'array'
            THEN version.content->'nodes' ELSE '[]'::jsonb END
        ) AS node(value)
        WHERE value->>'type' = 'external_qualification_gate'
      )
    )
      AND (version.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
        OR version.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
        OR version.content_hash IS DISTINCT FROM version.content->>'hash'
        OR workflow_execution_profile_v3_definition_is_database_admissible(version.content) IS NOT TRUE)
  ) THEN
    RAISE EXCEPTION 'pre-existing workflow-engine/v3 definition is not exact'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM workflow_runs AS run
    WHERE (
      run.execution_profile_version = 'workflow-engine/v3'
      OR run.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
      OR run.status = 'waiting_qualification'
    )
      AND NOT EXISTS (
        SELECT 1
        FROM workflow_definition_versions AS version
        WHERE version.id = run.definition_version_id
          AND run.execution_profile_version = 'workflow-engine/v3'
          AND run.execution_profile_hash = '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
          AND version.execution_profile_version = run.execution_profile_version
          AND version.execution_profile_hash = run.execution_profile_hash
          AND version.content_hash = version.content->>'hash'
          AND workflow_execution_profile_v3_definition_is_database_admissible(version.content) IS TRUE
      )
  ) THEN
    RAISE EXCEPTION 'pre-existing workflow run does not bind the exact workflow-engine/v3 definition'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM workflow_node_runs AS node
    JOIN workflow_runs AS run ON run.id = node.run_id
    JOIN workflow_definition_versions AS version ON version.id = run.definition_version_id
    WHERE run.execution_profile_version = 'workflow-engine/v3'
      AND (
        run.execution_profile_hash <> '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
        OR version.content_hash IS DISTINCT FROM version.content->>'hash'
        OR workflow_execution_profile_v3_definition_is_database_admissible(version.content) IS NOT TRUE
        OR node.definition_node_id IS NULL
        OR node.slice_kind NOT IN ('root','slice')
        OR (node.slice_kind = 'root' AND node.slice_id IS NOT NULL)
        OR (node.slice_kind = 'slice' AND node.slice_id IS NULL)
        OR NOT EXISTS (
          SELECT 1
          FROM jsonb_array_elements(version.content->'nodes') AS definition_node(value)
          WHERE value->>'id' = node.definition_node_id
            AND value->>'type' = node.node_type
        )
      )
  ) THEN
    RAISE EXCEPTION 'pre-existing workflow-engine/v3 node lacks exact stable definition identity'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM workflow_node_runs AS node
    LEFT JOIN workflow_runs AS run ON run.id = node.run_id
    LEFT JOIN workflow_definition_versions AS version ON version.id = run.definition_version_id
    WHERE (
      node.node_type = 'external_qualification_gate'
      OR node.status = 'waiting_qualification'
      OR node.input_authority_id IS NOT NULL
    )
      AND (
        run.execution_profile_version IS DISTINCT FROM 'workflow-engine/v3'
        OR run.execution_profile_hash IS DISTINCT FROM '854312ee02ea7a39219e9a2f011b801abe5f03cfb0dac05e04199295f965a104'
        OR version.content_hash IS DISTINCT FROM version.content->>'hash'
        OR workflow_execution_profile_v3_definition_is_database_admissible(version.content) IS NOT TRUE
        OR node.node_type IS DISTINCT FROM 'external_qualification_gate'
        OR node.definition_node_id IS DISTINCT FROM 'external-qualification'
        OR node.node_key IS DISTINCT FROM 'external-qualification'
        OR node.slice_kind IS DISTINCT FROM 'root'
        OR node.slice_id IS NOT NULL
        OR node.attempt <> 0
        OR node.lease_owner IS NOT NULL
        OR node.lease_expires_at IS NOT NULL
        OR node.started_at IS NOT NULL
        OR node.completed_at IS NOT NULL
        OR node.failure IS NOT NULL
        OR node.input_manifest_id IS NOT NULL
        OR node.output_proposal_id IS NOT NULL
        OR node.output_revision_id IS NOT NULL
        OR node.status NOT IN ('pending','waiting_qualification','cancelled','stale')
        OR (node.status = 'pending' AND node.input_authority_id IS NOT NULL)
        OR (node.status = 'waiting_qualification' AND (
          node.input_authority_id IS NULL
          OR NOT EXISTS (
            SELECT 1
            FROM workflow_input_authorities AS authority
            WHERE authority.authority_id = node.input_authority_id
              AND authority.workflow_run_id = node.run_id
              AND authority.node_run_id = node.id
          )
        ))
        OR NOT EXISTS (
          SELECT 1
          FROM jsonb_array_elements(version.content->'nodes') AS definition_node(value)
          WHERE value->>'id' = node.definition_node_id
            AND value->>'type' = 'external_qualification_gate'
        )
      )
  ) THEN
    RAISE EXCEPTION 'pre-existing external qualification gate used a generic workflow transition'
      USING ERRCODE = '23514';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM workflow_runs AS run
    JOIN workflow_definition_versions AS version ON version.id = run.definition_version_id
    WHERE run.execution_profile_version = 'workflow-engine/v3'
      AND (
        SELECT count(*)
        FROM workflow_node_runs AS node
        JOIN LATERAL jsonb_array_elements(version.content->'nodes') AS definition_node(value)
          ON definition_node.value->>'id' = node.definition_node_id
        WHERE node.run_id = run.id
          AND node.node_type = 'external_qualification_gate'
          AND definition_node.value->>'type' = 'external_qualification_gate'
          AND node.slice_kind = 'root'
          AND node.slice_id IS NULL
      ) <> 1
  ) THEN
    RAISE EXCEPTION 'pre-existing workflow-engine/v3 run lacks its exact external qualification gate closure'
      USING ERRCODE = '23514';
  END IF;
END;
$workflow_execution_profile_v3_existing_guard$;

CREATE FUNCTION validate_workflow_execution_profile_v3_run_closure()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_run_id uuid;
  v_run workflow_runs%ROWTYPE;
  v_expected_external_id text;
BEGIN
  IF TG_TABLE_NAME = 'workflow_runs' THEN
    IF TG_OP = 'DELETE' THEN
      v_run_id := OLD.id;
    ELSE
      v_run_id := NEW.id;
    END IF;
  ELSE
    IF TG_OP = 'DELETE' THEN
      v_run_id := OLD.run_id;
    ELSE
      v_run_id := NEW.run_id;
    END IF;
  END IF;
  SELECT * INTO v_run FROM workflow_runs WHERE id = v_run_id;
  IF v_run.id IS NULL OR v_run.execution_profile_version <> 'workflow-engine/v3' THEN
    RETURN NULL;
  END IF;
  SELECT value->>'id' INTO v_expected_external_id
  FROM workflow_definition_versions AS version,
       LATERAL jsonb_array_elements(version.content->'nodes') AS node(value)
  WHERE version.id = v_run.definition_version_id
    AND value->>'type' = 'external_qualification_gate';
  IF v_expected_external_id IS NULL
     OR v_expected_external_id <> 'external-qualification'
     OR (SELECT count(*) FROM workflow_node_runs AS node
         WHERE node.run_id = v_run.id
           AND node.node_type = 'external_qualification_gate'
           AND node.definition_node_id = v_expected_external_id
           AND node.node_key = 'external-qualification'
           AND node.slice_kind = 'root' AND node.slice_id IS NULL
           AND node.attempt = 0
           AND node.lease_owner IS NULL AND node.lease_expires_at IS NULL
           AND node.started_at IS NULL AND node.completed_at IS NULL AND node.failure IS NULL
           AND node.input_manifest_id IS NULL
           AND node.output_proposal_id IS NULL AND node.output_revision_id IS NULL
           AND node.status IN ('pending','waiting_qualification','cancelled','stale')) <> 1
     OR (v_run.status = 'waiting_qualification' AND NOT EXISTS (
       SELECT 1 FROM workflow_node_runs AS node
       WHERE node.run_id = v_run.id
         AND node.node_type = 'external_qualification_gate'
         AND node.definition_node_id = 'external-qualification'
         AND node.node_key = 'external-qualification'
         AND node.status = 'waiting_qualification'
         AND node.input_authority_id IS NOT NULL
         AND node.started_at IS NULL AND node.completed_at IS NULL AND node.failure IS NULL
     ))
     OR (v_run.status <> 'waiting_qualification' AND EXISTS (
       SELECT 1 FROM workflow_node_runs AS node
       WHERE node.run_id = v_run.id
         AND node.node_type = 'external_qualification_gate'
         AND node.definition_node_id = 'external-qualification'
         AND node.node_key = 'external-qualification'
         AND node.status = 'waiting_qualification'
         AND node.input_authority_id IS NOT NULL
         AND node.started_at IS NULL AND node.completed_at IS NULL AND node.failure IS NULL
     )) THEN
    RAISE EXCEPTION 'workflow-engine/v3 run lacks its exact external qualification gate closure'
      USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$function$;

CREATE CONSTRAINT TRIGGER workflow_execution_profile_v3_run_exact_closure
AFTER INSERT OR UPDATE ON workflow_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_workflow_execution_profile_v3_run_closure();

CREATE CONSTRAINT TRIGGER workflow_execution_profile_v3_node_exact_closure
AFTER INSERT OR UPDATE OR DELETE ON workflow_node_runs
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION validate_workflow_execution_profile_v3_run_closure();

ALTER TABLE workflow_runs
  DROP CONSTRAINT workflow_runs_status_check,
  ADD CONSTRAINT workflow_runs_status_check CHECK (
    status IN ('pending','running','waiting_input','waiting_review','waiting_qualification','completed','failed','cancelled','stale')
  );

ALTER TABLE workflow_node_runs
  DROP CONSTRAINT workflow_node_runs_status_check,
  ADD CONSTRAINT workflow_node_runs_status_check CHECK (
    status IN ('pending','ready','running','waiting_input','waiting_review','waiting_qualification','completed','failed','cancelled','stale')
  );

DO $workflow_execution_profile_v3_security$
DECLARE
  schema_name text := current_schema();
  function_signature text;
BEGIN
  FOREACH function_signature IN ARRAY ARRAY[
    'workflow_execution_profile_v3_definition_is_database_admissible(jsonb)',
    'guard_workflow_execution_profile_v3_definition()',
    'guard_workflow_execution_profile_v3_run()',
    'guard_external_qualification_gate_node_v3()',
    'validate_workflow_execution_profile_v3_run_closure()'
  ] LOOP
    EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I', schema_name, function_signature, schema_name);
    EXECUTE format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC', schema_name, function_signature);
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'worksflow_migration_owner') THEN
      EXECUTE format('ALTER FUNCTION %I.%s OWNER TO worksflow_migration_owner', schema_name, function_signature);
    END IF;
  END LOOP;
END;
$workflow_execution_profile_v3_security$;

COMMENT ON FUNCTION workflow_execution_profile_v3_definition_is_database_admissible(jsonb) IS
  'One-way canonical/hash-bound database admission proof for two frozen workflow-engine/v3 semantic signatures; arbitrary schemas, mappings, conditions, and non-linear DAGs require Go authoring and are rejected here.';
COMMENT ON FUNCTION guard_external_qualification_gate_node_v3() IS
  'Rejects runner, manual approval, waiver, retry, generic completion, and output paths for the dedicated external qualification gate before the private 000082 handoff exists.';
