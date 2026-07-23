-- Fail closed if the hardened authority has already issued durable proof.
DO $canonical_review_authority_hardening_down_guard$
BEGIN
  LOCK TABLE review_requests IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE review_decisions IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE canonical_review_approval_receipts IN ACCESS EXCLUSIVE MODE;
  IF EXISTS (SELECT 1 FROM canonical_review_approval_receipts)
     OR EXISTS (SELECT 1 FROM review_requests WHERE review_authority_version = 1)
     OR EXISTS (SELECT 1 FROM review_decisions WHERE review_authority_version = 1) THEN
    RAISE EXCEPTION 'cannot roll back Canonical Review hardening after a receipt exists or version 1 review state exists';
  END IF;
END;
$canonical_review_authority_hardening_down_guard$;

ALTER TABLE review_decisions
  DROP CONSTRAINT review_decisions_authority_facts_check;

ALTER TABLE review_decisions
  ADD CONSTRAINT review_decisions_authority_facts_check CHECK (
    (review_authority_version = 0
      AND reviewer_role_at_decision IS NULL
      AND governance_mode_at_decision IS NULL
      AND owner_count_at_decision IS NULL
      AND sole_owner_id_at_decision IS NULL
      AND solo_review_confirmed IS NULL
      AND precondition_etag IS NULL)
    OR
    (review_authority_version = 1
      AND reviewer_role_at_decision IN ('owner', 'admin', 'editor')
      AND governance_mode_at_decision IN ('solo', 'team')
      AND owner_count_at_decision BETWEEN 1 AND 1000000
      AND ((owner_count_at_decision = 1 AND sole_owner_id_at_decision IS NOT NULL)
        OR (owner_count_at_decision <> 1 AND sole_owner_id_at_decision IS NULL))
      AND solo_review_confirmed IS NOT NULL
      AND precondition_etag IS NOT NULL
      AND octet_length(precondition_etag) BETWEEN 1 AND 512
      AND summary = btrim(summary)
      AND octet_length(summary) <= 4096
      AND (
        (solo_self_review = false AND solo_review_confirmed = false)
        OR
        (solo_self_review = true
          AND decision = 'approve'
          AND reviewer_role_at_decision = 'owner'
          AND governance_mode_at_decision = 'solo'
          AND owner_count_at_decision = 1
          AND sole_owner_id_at_decision = reviewer_id
          AND solo_review_confirmed = true
          AND octet_length(summary) BETWEEN 1 AND 4096)
      ))
  );

CREATE OR REPLACE FUNCTION canonical_review_approval_receipt_record_is_exact(
  p_receipt canonical_review_approval_receipts
) RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE STRICT PARALLEL SAFE
AS $function$
DECLARE
  v_root jsonb := p_receipt.receipt_document;
  v_request jsonb := p_receipt.review_request_snapshot_document;
  v_revision jsonb := p_receipt.revision_snapshot_document;
  v_policy jsonb := p_receipt.policy_snapshot_document;
  v_decisions jsonb := p_receipt.decisions_snapshot_document;
  v_governance jsonb := p_receipt.governance_snapshot_document;
  v_approval jsonb := p_receipt.approval_snapshot_document;
  v_value jsonb;
  v_decision jsonb;
  v_facts jsonb;
  v_ordinal bigint;
  v_count integer := 0;
  v_previous_order text := '';
  v_order text;
  v_seen_ids text[] := ARRAY[]::text[];
  v_seen_reviewers text[] := ARRAY[]::text[];
  v_any_solo boolean := false;
  v_issued_at_text text := to_char(p_receipt.issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"');
  v_uuid_pattern constant text := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
  v_time_pattern constant text := '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z$';
BEGIN
  IF canonical_review_jsonb_bytes(v_root) <> p_receipt.receipt_bytes
     OR canonical_review_authority_hash('worksflow.canonical-review.receipt/v1', p_receipt.receipt_bytes) <> p_receipt.receipt_hash
     OR canonical_review_jsonb_bytes(v_request) <> p_receipt.review_request_snapshot_bytes
     OR canonical_review_authority_hash('worksflow.canonical-review.review-request/v1', p_receipt.review_request_snapshot_bytes) <> p_receipt.review_request_snapshot_hash
     OR canonical_review_jsonb_bytes(v_revision) <> p_receipt.revision_snapshot_bytes
     OR canonical_review_authority_hash('worksflow.canonical-review.revision/v1', p_receipt.revision_snapshot_bytes) <> p_receipt.revision_snapshot_hash
     OR canonical_review_jsonb_bytes(v_policy) <> p_receipt.policy_snapshot_bytes
     OR canonical_review_authority_hash('worksflow.canonical-review.policy/v1', p_receipt.policy_snapshot_bytes) <> p_receipt.policy_snapshot_hash
     OR canonical_review_jsonb_bytes(v_decisions) <> p_receipt.decisions_snapshot_bytes
     OR canonical_review_authority_hash('worksflow.canonical-review.decisions/v1', p_receipt.decisions_snapshot_bytes) <> p_receipt.decisions_snapshot_hash
     OR canonical_review_jsonb_bytes(v_governance) <> p_receipt.governance_snapshot_bytes
     OR canonical_review_authority_hash('worksflow.canonical-review.governance/v1', p_receipt.governance_snapshot_bytes) <> p_receipt.governance_snapshot_hash
     OR canonical_review_jsonb_bytes(v_approval) <> p_receipt.approval_snapshot_bytes
     OR canonical_review_authority_hash('worksflow.canonical-review.approval/v1', p_receipt.approval_snapshot_bytes) <> p_receipt.approval_snapshot_hash THEN
    RETURN false;
  END IF;

  IF jsonb_typeof(v_root) <> 'object'
     OR v_root - ARRAY['approval','componentDigests','decisions','governance','issuedAt','mediaType','policy','reviewRequest','revision','schemaVersion'] <> '{}'::jsonb
     OR NOT (v_root ?& ARRAY['approval','componentDigests','decisions','governance','issuedAt','mediaType','policy','reviewRequest','revision','schemaVersion'])
     OR v_root->>'schemaVersion' <> 'worksflow-canonical-review-approval-receipt/v1'
     OR v_root->>'mediaType' <> 'application/vnd.worksflow.canonical-review-approval-receipt+json;version=1'
     OR v_root->>'issuedAt' <> v_issued_at_text
     OR v_root->'reviewRequest' <> v_request OR v_root->'revision' <> v_revision
     OR v_root->'policy' <> v_policy OR v_root->'decisions' <> v_decisions
     OR v_root->'governance' <> v_governance OR v_root->'approval' <> v_approval
     OR jsonb_typeof(v_root->'componentDigests') <> 'object'
     OR (v_root->'componentDigests') - ARRAY['approval','decisions','governance','policy','reviewRequest','revision'] <> '{}'::jsonb
     OR NOT ((v_root->'componentDigests') ?& ARRAY['approval','decisions','governance','policy','reviewRequest','revision'])
     OR v_root->'componentDigests' <> jsonb_build_object(
       'approval', p_receipt.approval_snapshot_hash,
       'decisions', p_receipt.decisions_snapshot_hash,
       'governance', p_receipt.governance_snapshot_hash,
       'policy', p_receipt.policy_snapshot_hash,
       'reviewRequest', p_receipt.review_request_snapshot_hash,
       'revision', p_receipt.revision_snapshot_hash
     ) THEN
    RETURN false;
  END IF;

  IF jsonb_typeof(v_request) <> 'object'
     OR v_request - ARRAY['artifactId','closedAt','closedByDecisionId','contentHash','id','projectId','requestedAt','requestedBy','reviewAuthorityVersion','revisionId','schemaVersion','status'] <> '{}'::jsonb
     OR NOT (v_request ?& ARRAY['artifactId','closedAt','closedByDecisionId','contentHash','id','projectId','requestedAt','requestedBy','reviewAuthorityVersion','revisionId','schemaVersion','status'])
     OR v_request->>'schemaVersion' <> 'worksflow-canonical-review-request-snapshot/v1'
     OR v_request->>'status' <> 'approved' OR v_request->'reviewAuthorityVersion' <> '1'::jsonb
     OR v_request->>'id' <> p_receipt.review_request_id::text
     OR v_request->>'projectId' <> p_receipt.project_id::text
     OR v_request->>'artifactId' <> p_receipt.artifact_id::text
     OR v_request->>'revisionId' <> p_receipt.revision_id::text
     OR v_request->>'contentHash' <> p_receipt.revision_content_hash
     OR v_request->>'closedByDecisionId' <> p_receipt.closed_by_decision_id::text
     OR v_request->>'closedAt' <> v_issued_at_text
     OR v_request->>'requestedAt' !~ v_time_pattern OR v_request->>'requestedAt' > v_issued_at_text
     OR v_request->>'requestedBy' !~ v_uuid_pattern THEN
    RETURN false;
  END IF;

  IF jsonb_typeof(v_revision) <> 'object'
     OR v_revision - ARRAY['approvedAt','artifactId','artifactSchemaVersion','byteSize','changeSource','changeSummary','contentHash','contentRef','contentStore','createdAt','createdBy','id','implementationProposalId','parentRevisionId','proposalId','revisionNumber','schemaVersion','sourceManifestId','supersededAt','workflowStatus'] <> '{}'::jsonb
     OR NOT (v_revision ?& ARRAY['approvedAt','artifactId','artifactSchemaVersion','byteSize','changeSource','changeSummary','contentHash','contentRef','contentStore','createdAt','createdBy','id','implementationProposalId','parentRevisionId','proposalId','revisionNumber','schemaVersion','sourceManifestId','supersededAt','workflowStatus'])
     OR v_revision->>'schemaVersion' <> 'worksflow-canonical-review-revision-snapshot/v1'
     OR v_revision->>'workflowStatus' <> 'approved' OR jsonb_typeof(v_revision->'supersededAt') <> 'null'
     OR v_revision->>'id' <> p_receipt.revision_id::text
     OR v_revision->>'artifactId' <> p_receipt.artifact_id::text
     OR v_revision->>'contentHash' <> p_receipt.revision_content_hash
     OR v_revision->>'approvedAt' <> v_issued_at_text
     OR v_revision->>'createdAt' !~ v_time_pattern OR v_revision->>'createdAt' > v_issued_at_text
     OR v_revision->>'createdBy' !~ v_uuid_pattern
     OR jsonb_typeof(v_revision->'artifactSchemaVersion') <> 'number'
     OR (v_revision->'artifactSchemaVersion')::text !~ '^[1-9][0-9]{0,15}$'
     OR jsonb_typeof(v_revision->'revisionNumber') <> 'number'
     OR (v_revision->'revisionNumber')::text !~ '^[1-9][0-9]{0,15}$'
     OR jsonb_typeof(v_revision->'byteSize') <> 'number'
     OR (v_revision->'byteSize')::text !~ '^(0|[1-9][0-9]{0,15})$'
     OR v_revision->>'changeSource' NOT IN ('human','ai_proposal','import','merge','rollback','system')
     OR jsonb_typeof(v_revision->'changeSummary') <> 'string' OR octet_length(v_revision->>'changeSummary') > 4096
     OR jsonb_typeof(v_revision->'contentStore') <> 'string' OR octet_length(v_revision->>'contentStore') NOT BETWEEN 1 AND 128
     OR jsonb_typeof(v_revision->'contentRef') <> 'string' OR octet_length(v_revision->>'contentRef') NOT BETWEEN 1 AND 65536 THEN
    RETURN false;
  END IF;
  FOREACH v_value IN ARRAY ARRAY[v_revision->'implementationProposalId',v_revision->'parentRevisionId',v_revision->'proposalId',v_revision->'sourceManifestId'] LOOP
    IF jsonb_typeof(v_value) NOT IN ('null','string')
       OR (jsonb_typeof(v_value) = 'string' AND v_value #>> '{}' !~ v_uuid_pattern) THEN
      RETURN false;
    END IF;
  END LOOP;

  IF jsonb_typeof(v_policy) <> 'object' OR v_policy - ARRAY['schemaVersion','value'] <> '{}'::jsonb
     OR NOT (v_policy ?& ARRAY['schemaVersion','value'])
     OR v_policy->>'schemaVersion' <> 'worksflow-canonical-review-policy-snapshot/v1'
     OR jsonb_typeof(v_policy->'value') <> 'object' THEN
    RETURN false;
  END IF;
  v_value := v_policy->'value';
  IF v_value - ARRAY['governanceMode','minimumApprovals','prohibitSelfReview','reviewerIds','soloSelfReviewOwnerId'] <> '{}'::jsonb
     OR NOT (v_value ?& ARRAY['governanceMode','minimumApprovals','prohibitSelfReview','reviewerIds','soloSelfReviewOwnerId'])
     OR v_value->>'governanceMode' <> p_receipt.governance_mode
     OR v_value->'minimumApprovals' <> to_jsonb(p_receipt.minimum_approvals)
     OR v_value->'prohibitSelfReview' <> 'true'::jsonb
     OR jsonb_typeof(v_value->'reviewerIds') <> 'array'
     OR jsonb_array_length(v_value->'reviewerIds') > 20
     OR (jsonb_array_length(v_value->'reviewerIds') > 0 AND p_receipt.minimum_approvals > jsonb_array_length(v_value->'reviewerIds'))
     OR jsonb_typeof(v_value->'soloSelfReviewOwnerId') NOT IN ('null','string')
     OR (jsonb_typeof(v_value->'soloSelfReviewOwnerId') = 'string' AND v_value->>'soloSelfReviewOwnerId' !~ v_uuid_pattern) THEN
    RETURN false;
  END IF;
  IF EXISTS (
    SELECT 1 FROM jsonb_array_elements(v_value->'reviewerIds') AS reviewer(item)
    WHERE jsonb_typeof(reviewer.item) <> 'string' OR reviewer.item #>> '{}' !~ v_uuid_pattern
  ) OR (
    SELECT count(*) FROM jsonb_array_elements_text(v_value->'reviewerIds')
  ) <> (
    SELECT count(DISTINCT reviewer_id) FROM jsonb_array_elements_text(v_value->'reviewerIds') AS reviewer(reviewer_id)
  ) THEN
    RETURN false;
  END IF;

  IF jsonb_typeof(v_governance) <> 'object'
     OR v_governance - ARRAY['mode','ownerCount','schemaVersion','soleOwnerId'] <> '{}'::jsonb
     OR NOT (v_governance ?& ARRAY['mode','ownerCount','schemaVersion','soleOwnerId'])
     OR v_governance->>'schemaVersion' <> 'worksflow-canonical-review-governance-snapshot/v1'
     OR v_governance->>'mode' <> p_receipt.governance_mode
     OR v_governance->'ownerCount' <> to_jsonb(p_receipt.owner_count)
     OR jsonb_typeof(v_governance->'ownerCount') <> 'number'
     OR (v_governance->'ownerCount')::text !~ '^[1-9][0-9]{0,6}$'
     OR jsonb_typeof(v_governance->'soleOwnerId') NOT IN ('null','string')
     OR ((v_governance->>'ownerCount')::integer = 1) <> (jsonb_typeof(v_governance->'soleOwnerId') = 'string')
     OR (p_receipt.sole_owner_id IS NULL AND jsonb_typeof(v_governance->'soleOwnerId') <> 'null')
     OR (p_receipt.sole_owner_id IS NOT NULL AND v_governance->>'soleOwnerId' <> p_receipt.sole_owner_id::text) THEN
    RETURN false;
  END IF;

  IF jsonb_typeof(v_approval) <> 'object'
     OR v_approval - ARRAY['approvalCount','approvalDecisionIds','approvedAt','artifactId','artifactKind','artifactLatestApprovedRevisionId','artifactLatestRevisionId','artifactLifecycle','artifactVersion','closedByDecisionId','minimumApprovals','projectId','revisionContentHash','revisionId','schemaVersion','soloSelfReview','subjectAuthorId'] <> '{}'::jsonb
     OR NOT (v_approval ?& ARRAY['approvalCount','approvalDecisionIds','approvedAt','artifactId','artifactKind','artifactLatestApprovedRevisionId','artifactLatestRevisionId','artifactLifecycle','artifactVersion','closedByDecisionId','minimumApprovals','projectId','revisionContentHash','revisionId','schemaVersion','soloSelfReview','subjectAuthorId'])
     OR v_approval->>'schemaVersion' <> 'worksflow-canonical-review-approval-snapshot/v1'
     OR v_approval->'approvalCount' <> to_jsonb(p_receipt.approval_count)
     OR v_approval->'minimumApprovals' <> to_jsonb(p_receipt.minimum_approvals)
     OR v_approval->'soloSelfReview' <> to_jsonb(p_receipt.solo_self_review)
     OR v_approval->>'approvedAt' <> v_issued_at_text
     OR v_approval->>'projectId' <> p_receipt.project_id::text
     OR v_approval->>'artifactId' <> p_receipt.artifact_id::text
     OR v_approval->>'revisionId' <> p_receipt.revision_id::text
     OR v_approval->>'revisionContentHash' <> p_receipt.revision_content_hash
     OR v_approval->>'closedByDecisionId' <> p_receipt.closed_by_decision_id::text
     OR v_approval->>'artifactLatestApprovedRevisionId' <> p_receipt.revision_id::text
     OR v_approval->>'artifactLatestRevisionId' <> p_receipt.revision_id::text
     OR v_approval->>'artifactLifecycle' <> 'active'
     OR jsonb_typeof(v_approval->'artifactKind') <> 'string' OR octet_length(v_approval->>'artifactKind') NOT BETWEEN 1 AND 128
     OR v_approval->>'artifactKind' NOT IN (
       'project_brief','product_requirements','decision_record','glossary_policy','reference_source',
       'change_request','requirement_baseline','blueprint','page_spec','prototype','prototype_flow',
       'fixture_bundle','design_system','token_set','component_registry','api_contract','data_contract',
       'permission_contract','workspace','test_report','quality_report'
     )
     OR jsonb_typeof(v_approval->'artifactVersion') <> 'number' OR (v_approval->'artifactVersion')::text !~ '^[1-9][0-9]{0,15}$'
     OR v_approval->>'subjectAuthorId' <> v_revision->>'createdBy'
     OR jsonb_typeof(v_approval->'approvalDecisionIds') <> 'array'
     OR jsonb_array_length(v_approval->'approvalDecisionIds') <> p_receipt.approval_count THEN
    RETURN false;
  END IF;

  IF jsonb_typeof(v_decisions) <> 'object'
     OR v_decisions - ARRAY['decisions','schemaVersion'] <> '{}'::jsonb
     OR NOT (v_decisions ?& ARRAY['decisions','schemaVersion'])
     OR v_decisions->>'schemaVersion' <> 'worksflow-canonical-review-decisions-snapshot/v1'
     OR jsonb_typeof(v_decisions->'decisions') <> 'array'
     OR jsonb_array_length(v_decisions->'decisions') <> p_receipt.approval_count THEN
    RETURN false;
  END IF;
  FOR v_decision, v_ordinal IN
    SELECT item, ordinal FROM jsonb_array_elements(v_decisions->'decisions') WITH ORDINALITY AS decision(item, ordinal)
  LOOP
    v_count := v_count + 1;
    IF jsonb_typeof(v_decision) <> 'object'
       OR v_decision - ARRAY['authorityFacts','createdAt','decision','id','reviewerId','soloSelfReview','summary'] <> '{}'::jsonb
       OR NOT (v_decision ?& ARRAY['authorityFacts','createdAt','decision','id','reviewerId','soloSelfReview','summary'])
       OR v_decision->>'decision' <> 'approve'
       OR v_decision->>'id' !~ v_uuid_pattern OR v_decision->>'reviewerId' !~ v_uuid_pattern
       OR v_decision->>'createdAt' !~ v_time_pattern OR v_decision->>'createdAt' > v_issued_at_text
       OR jsonb_typeof(v_decision->'summary') <> 'string'
       OR v_decision->>'summary' <> btrim(v_decision->>'summary') OR octet_length(v_decision->>'summary') > 4096
       OR jsonb_typeof(v_decision->'soloSelfReview') <> 'boolean'
       OR jsonb_typeof(v_decision->'authorityFacts') <> 'object' THEN
      RETURN false;
    END IF;
    v_facts := v_decision->'authorityFacts';
    IF v_facts - ARRAY['explicitConfirmation','governanceMode','ownerCount','preconditionETag','reviewerRole','soleOwnerId','version'] <> '{}'::jsonb
       OR NOT (v_facts ?& ARRAY['explicitConfirmation','governanceMode','ownerCount','preconditionETag','reviewerRole','soleOwnerId','version'])
       OR v_facts->'version' <> '1'::jsonb OR v_facts->>'governanceMode' <> p_receipt.governance_mode
       OR v_facts->'ownerCount' <> v_governance->'ownerCount'
       OR v_facts->'soleOwnerId' <> v_governance->'soleOwnerId'
       OR v_facts->>'reviewerRole' NOT IN ('owner','admin','editor')
       OR jsonb_typeof(v_facts->'preconditionETag') <> 'string'
       OR octet_length(v_facts->>'preconditionETag') NOT BETWEEN 1 AND 512
       OR v_facts->>'preconditionETag' <> btrim(v_facts->>'preconditionETag')
       OR jsonb_typeof(v_facts->'explicitConfirmation') <> 'boolean' THEN
      RETURN false;
    END IF;
    -- createdAt is fixed-width, so concatenation preserves the Go tuple order
    -- without attempting to place a forbidden NUL byte in PostgreSQL text.
    v_order := (v_decision->>'createdAt') || (v_decision->>'id');
    IF v_previous_order <> '' AND v_previous_order >= v_order THEN
      RETURN false;
    END IF;
    v_previous_order := v_order;
    IF v_decision->>'id' = ANY(v_seen_ids) OR v_decision->>'reviewerId' = ANY(v_seen_reviewers)
       OR v_approval->'approvalDecisionIds'->>(v_ordinal - 1)::integer <> v_decision->>'id' THEN
      RETURN false;
    END IF;
    v_seen_ids := array_append(v_seen_ids, v_decision->>'id');
    v_seen_reviewers := array_append(v_seen_reviewers, v_decision->>'reviewerId');
    IF jsonb_array_length(v_value->'reviewerIds') > 0
       AND NOT (v_value->'reviewerIds' ? (v_decision->>'reviewerId')) THEN
      RETURN false;
    END IF;
    IF (v_decision->'soloSelfReview')::boolean THEN
      v_any_solo := true;
      IF v_facts->>'reviewerRole' <> 'owner' OR p_receipt.governance_mode <> 'solo'
         OR v_governance->'ownerCount' <> '1'::jsonb
         OR v_governance->>'soleOwnerId' <> v_decision->>'reviewerId'
         OR v_facts->'explicitConfirmation' <> 'true'::jsonb
         OR v_decision->>'reviewerId' <> v_revision->>'createdBy'
         OR octet_length(v_decision->>'summary') = 0
         OR v_value->>'soloSelfReviewOwnerId' <> v_decision->>'reviewerId' THEN
        RETURN false;
      END IF;
    ELSIF v_facts->'explicitConfirmation' <> 'false'::jsonb
       OR v_decision->>'reviewerId' = v_revision->>'createdBy' THEN
      RETURN false;
    END IF;
  END LOOP;
  IF v_count <> p_receipt.approval_count OR p_receipt.approval_count <> p_receipt.minimum_approvals
     OR p_receipt.closed_by_decision_id::text <> v_decisions->'decisions'->-1->>'id'
     OR v_decisions->'decisions'->-1->>'createdAt' <> v_issued_at_text
     OR p_receipt.solo_self_review <> v_any_solo
     OR (p_receipt.solo_self_review AND v_value->>'soloSelfReviewOwnerId' IS NULL) THEN
    RETURN false;
  END IF;
  RETURN true;
EXCEPTION WHEN OTHERS THEN
  RETURN false;
END;
$function$;

CREATE OR REPLACE FUNCTION guard_canonical_review_source_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
AS $function$
DECLARE
  v_request review_requests%ROWTYPE;
BEGIN
  IF TG_TABLE_NAME = 'review_requests' THEN
    IF TG_OP = 'DELETE' THEN
      RAISE EXCEPTION 'Review requests cannot be deleted' USING ERRCODE = 'WCR02';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id
       OR NEW.revision_id IS DISTINCT FROM OLD.revision_id
       OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
       OR NEW.policy IS DISTINCT FROM OLD.policy
       OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
       OR NEW.requested_at IS DISTINCT FROM OLD.requested_at
       OR NEW.review_authority_version IS DISTINCT FROM OLD.review_authority_version THEN
      RAISE EXCEPTION 'Review request identity and policy are immutable' USING ERRCODE = 'WCR02';
    END IF;
    IF OLD.status <> 'open' THEN
      RAISE EXCEPTION 'Closed review requests are immutable' USING ERRCODE = 'WCR02';
    END IF;
    IF NEW.status = 'open' THEN
      IF NEW.closed_at IS NOT NULL OR NEW.closed_by_decision_id IS NOT NULL THEN
        RAISE EXCEPTION 'Open review request has closing material' USING ERRCODE = 'WCR01';
      END IF;
    ELSIF NEW.status IN ('approved', 'changes_requested') THEN
      IF NEW.closed_at IS NULL OR NEW.closed_by_decision_id IS NULL THEN
        RAISE EXCEPTION 'Decision-closed review request lacks its closing decision' USING ERRCODE = 'WCR01';
      END IF;
    ELSIF NEW.status IN ('withdrawn', 'stale') THEN
      IF NEW.closed_at IS NULL OR NEW.closed_by_decision_id IS NOT NULL THEN
        RAISE EXCEPTION 'Non-decision review closure is invalid' USING ERRCODE = 'WCR01';
      END IF;
    ELSE
      RAISE EXCEPTION 'Review request transition is invalid' USING ERRCODE = 'WCR01';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'Review decisions cannot be deleted' USING ERRCODE = 'WCR02';
  END IF;
  IF TG_OP = 'UPDATE' AND (
       NEW.id IS DISTINCT FROM OLD.id
       OR NEW.review_request_id IS DISTINCT FROM OLD.review_request_id
       OR NEW.reviewer_id IS DISTINCT FROM OLD.reviewer_id
       OR NEW.review_authority_version IS DISTINCT FROM OLD.review_authority_version
     ) THEN
    RAISE EXCEPTION 'Review decision identity is immutable' USING ERRCODE = 'WCR02';
  END IF;
  SELECT * INTO v_request FROM review_requests
  WHERE id = COALESCE(NEW.review_request_id, OLD.review_request_id)
  FOR UPDATE;
  IF v_request.id IS NULL OR v_request.status <> 'open'
     OR v_request.review_authority_version <> COALESCE(NEW.review_authority_version, OLD.review_authority_version) THEN
    RAISE EXCEPTION 'Review decisions may change only while their exact request is open'
      USING ERRCODE = 'WCR02';
  END IF;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION issue_canonical_review_approval_receipt(p_review_request_id uuid)
RETURNS TABLE (
  receipt_record canonical_review_approval_receipts,
  created boolean
)
LANGUAGE plpgsql
SECURITY DEFINER
AS $function$
DECLARE
  v_project_id uuid;
  v_project projects%ROWTYPE;
  v_request review_requests%ROWTYPE;
  v_revision artifact_revisions%ROWTYPE;
  v_artifact artifacts%ROWTYPE;
  v_closing review_decisions%ROWTYPE;
  v_existing canonical_review_approval_receipts%ROWTYPE;
  v_owner_count integer;
  v_sole_owner_id uuid;
  v_minimum integer;
  v_policy_mode text;
  v_policy_solo_owner uuid;
  v_decision_count integer;
  v_solo boolean;
  v_approval_ids jsonb;
  v_decisions_array jsonb;
  v_request_document jsonb;
  v_revision_document jsonb;
  v_policy_document jsonb;
  v_decisions_document jsonb;
  v_governance_document jsonb;
  v_approval_document jsonb;
  v_receipt_document jsonb;
  v_request_bytes bytea;
  v_revision_bytes bytea;
  v_policy_bytes bytea;
  v_decisions_bytes bytea;
  v_governance_bytes bytea;
  v_approval_bytes bytea;
  v_receipt_bytes bytea;
  v_request_hash text;
  v_revision_hash text;
  v_policy_hash text;
  v_decisions_hash text;
  v_governance_hash text;
  v_approval_hash text;
  v_receipt_hash text;
  v_issued_at timestamptz;
  v_last_decision_id uuid;
BEGIN
  IF p_review_request_id IS NULL THEN
    RAISE EXCEPTION 'Canonical Review request identity is required' USING ERRCODE = 'WCR01';
  END IF;
  SELECT project_id INTO v_project_id FROM review_requests WHERE id = p_review_request_id;
  IF v_project_id IS NULL THEN
    RAISE EXCEPTION 'Canonical Review request does not exist' USING ERRCODE = 'WCR01';
  END IF;
  SELECT * INTO v_project FROM projects WHERE id = v_project_id FOR UPDATE;
  IF v_project.id IS NULL THEN
    RAISE EXCEPTION 'Canonical Review project does not exist' USING ERRCODE = 'WCR01';
  END IF;
  SELECT * INTO v_request FROM review_requests WHERE id = p_review_request_id FOR UPDATE;

  SELECT * INTO v_existing FROM canonical_review_approval_receipts
  WHERE review_request_id = p_review_request_id;
  IF FOUND THEN
    SELECT * INTO v_existing FROM resolve_canonical_review_approval_receipt(
      v_existing.project_id, v_existing.revision_id, v_existing.receipt_hash
    );
    receipt_record := v_existing;
    created := false;
    RETURN NEXT;
    RETURN;
  END IF;

  IF v_request.review_authority_version <> 1 OR v_request.status <> 'approved'
     OR v_request.closed_at IS NULL OR v_request.closed_by_decision_id IS NULL
     OR v_request.policy - ARRAY['reviewerIds','minimumApprovals','prohibitSelfReview','governanceMode','soloSelfReviewOwnerId'] <> '{}'::jsonb
     OR NOT (v_request.policy ?& ARRAY['reviewerIds','minimumApprovals','prohibitSelfReview','governanceMode'])
     OR jsonb_typeof(v_request.policy->'reviewerIds') <> 'array'
     OR jsonb_typeof(v_request.policy->'minimumApprovals') <> 'number'
     OR (v_request.policy->>'minimumApprovals') !~ '^[1-9][0-9]?$'
     OR jsonb_typeof(v_request.policy->'prohibitSelfReview') <> 'boolean'
     OR v_request.policy->>'prohibitSelfReview' <> 'true'
     OR v_request.policy->>'governanceMode' NOT IN ('solo', 'team')
     OR jsonb_array_length(v_request.policy->'reviewerIds') > 20
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements(v_request.policy->'reviewerIds') AS reviewer(value)
       WHERE jsonb_typeof(reviewer.value) <> 'string'
          OR reviewer.value #>> '{}' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
     )
     OR (SELECT count(*) FROM jsonb_array_elements_text(v_request.policy->'reviewerIds')) <>
        (SELECT count(DISTINCT value) FROM jsonb_array_elements_text(v_request.policy->'reviewerIds') AS reviewer(value)) THEN
    RAISE EXCEPTION 'Canonical Review request or closed policy is invalid or legacy'
      USING ERRCODE = 'WCR02';
  END IF;
  v_minimum := (v_request.policy->>'minimumApprovals')::integer;
  v_policy_mode := v_request.policy->>'governanceMode';
  IF v_project.governance_mode <> v_policy_mode
     OR v_minimum NOT BETWEEN 1 AND 20
     OR (jsonb_array_length(v_request.policy->'reviewerIds') > 0
         AND v_minimum > jsonb_array_length(v_request.policy->'reviewerIds')) THEN
    RAISE EXCEPTION 'Canonical Review threshold is invalid' USING ERRCODE = 'WCR01';
  END IF;
  IF v_request.policy ? 'soloSelfReviewOwnerId' THEN
    IF jsonb_typeof(v_request.policy->'soloSelfReviewOwnerId') <> 'string'
       OR v_request.policy->>'soloSelfReviewOwnerId' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' THEN
      RAISE EXCEPTION 'Canonical Review Solo Owner policy is invalid' USING ERRCODE = 'WCR01';
    END IF;
    v_policy_solo_owner := (v_request.policy->>'soloSelfReviewOwnerId')::uuid;
  END IF;
  IF (v_policy_mode = 'team' AND v_policy_solo_owner IS NOT NULL)
     OR (v_policy_solo_owner IS NOT NULL
       AND NOT (v_request.policy->'reviewerIds' ? v_policy_solo_owner::text))
     OR EXISTS (
       SELECT 1 FROM jsonb_array_elements_text(v_request.policy->'reviewerIds') AS reviewer(id)
       WHERE NOT EXISTS (
         SELECT 1 FROM project_members AS member
         WHERE member.project_id = v_request.project_id
           AND member.user_id = reviewer.id::uuid
           AND member.role IN ('owner','admin','editor')
       )
     ) THEN
    RAISE EXCEPTION 'Canonical Review policy reviewer authority drifted' USING ERRCODE = 'WCR02';
  END IF;

  SELECT * INTO v_artifact FROM artifacts WHERE id = v_request.artifact_id FOR UPDATE;
  SELECT * INTO v_revision FROM artifact_revisions WHERE id = v_request.revision_id FOR UPDATE;
  SELECT * INTO v_closing FROM review_decisions
  WHERE review_request_id = v_request.id AND id = v_request.closed_by_decision_id FOR UPDATE;
  IF v_artifact.id IS NULL OR v_revision.id IS NULL OR v_closing.id IS NULL
     OR v_artifact.project_id <> v_request.project_id
     OR v_revision.artifact_id <> v_artifact.id
     OR v_revision.content_hash <> v_request.content_hash
     OR v_revision.workflow_status <> 'approved' OR v_revision.approved_at IS NULL
     OR v_revision.superseded_at IS NOT NULL
     OR v_revision.approved_at <> v_request.closed_at
     OR v_closing.created_at <> v_request.closed_at
     OR v_artifact.lifecycle <> 'active'
     OR v_artifact.latest_revision_id <> v_revision.id
     OR v_artifact.latest_approved_revision_id <> v_revision.id THEN
    RAISE EXCEPTION 'Canonical Review exact request, revision, or artifact closure is invalid'
      USING ERRCODE = 'WCR02';
  END IF;

  SELECT count(*)::integer,
         CASE WHEN count(*) = 1 THEN (array_agg(user_id ORDER BY user_id))[1] ELSE NULL END
  INTO v_owner_count, v_sole_owner_id
  FROM project_members WHERE project_id = v_request.project_id AND role = 'owner';
  IF v_owner_count < 1 OR (v_owner_count = 1) <> (v_sole_owner_id IS NOT NULL) THEN
    RAISE EXCEPTION 'Canonical Review governance closure is invalid' USING ERRCODE = 'WCR02';
  END IF;
  IF v_policy_solo_owner IS NOT NULL
     AND (v_policy_mode <> 'solo' OR v_owner_count <> 1 OR v_policy_solo_owner <> v_sole_owner_id
       OR v_policy_solo_owner <> v_revision.created_by) THEN
    RAISE EXCEPTION 'Canonical Review Solo Owner policy drifted' USING ERRCODE = 'WCR02';
  END IF;

  SELECT count(*)::integer,
         count(*) FILTER (WHERE solo_self_review)::integer > 0,
         jsonb_agg(id::text ORDER BY created_at, id),
         jsonb_agg(jsonb_build_object(
           'authorityFacts', jsonb_build_object(
             'explicitConfirmation', solo_review_confirmed,
             'governanceMode', governance_mode_at_decision,
             'ownerCount', owner_count_at_decision,
             'preconditionETag', precondition_etag,
             'reviewerRole', reviewer_role_at_decision,
             'soleOwnerId', CASE WHEN sole_owner_id_at_decision IS NULL THEN 'null'::jsonb ELSE to_jsonb(sole_owner_id_at_decision::text) END,
             'version', review_authority_version
           ),
           'createdAt', to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
           'decision', decision,
           'id', id::text,
           'reviewerId', reviewer_id::text,
           'soloSelfReview', solo_self_review,
           'summary', summary
         ) ORDER BY created_at, id)
  INTO v_decision_count, v_solo, v_approval_ids, v_decisions_array
  FROM review_decisions AS decision
  WHERE decision.review_request_id = v_request.id;

  IF v_decision_count <> v_minimum OR v_decision_count = 0
     OR EXISTS (
       SELECT 1 FROM review_decisions AS decision
       WHERE decision.review_request_id = v_request.id
         AND (decision.review_authority_version <> 1
           OR decision.decision <> 'approve'
           OR decision.created_at > v_request.closed_at
           OR decision.summary <> btrim(decision.summary)
           OR octet_length(decision.summary) > 4096
           OR octet_length(decision.precondition_etag) NOT BETWEEN 1 AND 512
           OR decision.reviewer_role_at_decision NOT IN ('owner','admin','editor')
           OR decision.governance_mode_at_decision <> v_policy_mode
           OR decision.owner_count_at_decision <> v_owner_count
           OR decision.sole_owner_id_at_decision IS DISTINCT FROM v_sole_owner_id
           OR NOT EXISTS (
             SELECT 1 FROM project_members AS current_member
             WHERE current_member.project_id = v_request.project_id
               AND current_member.user_id = decision.reviewer_id
               AND current_member.role = decision.reviewer_role_at_decision
               AND current_member.role IN ('owner','admin','editor')
           )
           OR (jsonb_array_length(v_request.policy->'reviewerIds') > 0
             AND NOT (v_request.policy->'reviewerIds' ? decision.reviewer_id::text))
           OR (
             decision.reviewer_id = v_revision.created_by AND NOT (
               decision.solo_self_review
               AND decision.reviewer_role_at_decision = 'owner'
               AND decision.governance_mode_at_decision = 'solo'
               AND decision.owner_count_at_decision = 1
               AND decision.sole_owner_id_at_decision = decision.reviewer_id
               AND decision.solo_review_confirmed
               AND octet_length(decision.summary) BETWEEN 1 AND 4096
               AND v_policy_mode = 'solo'
               AND v_policy_solo_owner = decision.reviewer_id
             )
           )
           OR (
             decision.reviewer_id <> v_revision.created_by
             AND (decision.solo_self_review OR decision.solo_review_confirmed)
           ))
     ) THEN
    RAISE EXCEPTION 'Canonical Review exact decision set is invalid or incomplete'
      USING ERRCODE = 'WCR02';
  END IF;

  IF v_solo AND (v_policy_mode <> 'solo' OR v_owner_count <> 1 OR v_sole_owner_id <> v_revision.created_by) THEN
    RAISE EXCEPTION 'Canonical Review Solo Owner closure drifted' USING ERRCODE = 'WCR02';
  END IF;

  SELECT id INTO v_last_decision_id FROM review_decisions
  WHERE review_request_id = v_request.id ORDER BY created_at DESC, id DESC LIMIT 1;
  IF v_last_decision_id IS DISTINCT FROM v_request.closed_by_decision_id THEN
    RAISE EXCEPTION 'Canonical Review closing decision is not the threshold-triggering decision'
      USING ERRCODE = 'WCR02';
  END IF;

  v_issued_at := date_trunc('milliseconds', v_request.closed_at);
  IF v_issued_at <> v_request.closed_at THEN
    RAISE EXCEPTION 'Canonical Review closure time is not millisecond canonical' USING ERRCODE = 'WCR01';
  END IF;

  v_request_document := jsonb_build_object(
    'artifactId', v_request.artifact_id::text,
    'closedAt', to_char(v_request.closed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'closedByDecisionId', v_request.closed_by_decision_id::text,
    'contentHash', v_request.content_hash,
    'id', v_request.id::text,
    'projectId', v_request.project_id::text,
    'requestedAt', to_char(v_request.requested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'requestedBy', v_request.requested_by::text,
    'reviewAuthorityVersion', 1,
    'revisionId', v_request.revision_id::text,
    'schemaVersion', 'worksflow-canonical-review-request-snapshot/v1',
    'status', 'approved'
  );
  v_revision_document := jsonb_build_object(
    'approvedAt', to_char(v_revision.approved_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'artifactId', v_revision.artifact_id::text,
    'artifactSchemaVersion', v_revision.schema_version,
    'byteSize', v_revision.byte_size,
    'changeSource', v_revision.change_source,
    'changeSummary', v_revision.change_summary,
    'contentHash', v_revision.content_hash,
    'contentRef', v_revision.content_ref,
    'contentStore', v_revision.content_store,
    'createdAt', to_char(v_revision.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'createdBy', v_revision.created_by::text,
    'id', v_revision.id::text,
    'implementationProposalId', CASE WHEN v_revision.implementation_proposal_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_revision.implementation_proposal_id::text) END,
    'parentRevisionId', CASE WHEN v_revision.parent_revision_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_revision.parent_revision_id::text) END,
    'proposalId', CASE WHEN v_revision.proposal_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_revision.proposal_id::text) END,
    'revisionNumber', v_revision.revision_number,
    'schemaVersion', 'worksflow-canonical-review-revision-snapshot/v1',
    'sourceManifestId', CASE WHEN v_revision.source_manifest_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_revision.source_manifest_id::text) END,
    'supersededAt', CASE WHEN v_revision.superseded_at IS NULL THEN 'null'::jsonb
      ELSE to_jsonb(to_char(v_revision.superseded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')) END,
    'workflowStatus', 'approved'
  );
  v_policy_document := jsonb_build_object(
    'schemaVersion', 'worksflow-canonical-review-policy-snapshot/v1',
    'value', jsonb_build_object(
      'governanceMode', v_policy_mode,
      'minimumApprovals', v_minimum,
      'prohibitSelfReview', true,
      'reviewerIds', v_request.policy->'reviewerIds',
      'soloSelfReviewOwnerId', CASE WHEN v_policy_solo_owner IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_policy_solo_owner::text) END
    )
  );
  v_decisions_document := jsonb_build_object(
    'decisions', v_decisions_array,
    'schemaVersion', 'worksflow-canonical-review-decisions-snapshot/v1'
  );
  v_governance_document := jsonb_build_object(
    'mode', v_project.governance_mode,
    'ownerCount', v_owner_count,
    'schemaVersion', 'worksflow-canonical-review-governance-snapshot/v1',
    'soleOwnerId', CASE WHEN v_sole_owner_id IS NULL THEN 'null'::jsonb ELSE to_jsonb(v_sole_owner_id::text) END
  );
  v_approval_document := jsonb_build_object(
    'approvalCount', v_decision_count,
    'approvalDecisionIds', v_approval_ids,
    'approvedAt', to_char(v_request.closed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'artifactId', v_artifact.id::text,
    'artifactKind', v_artifact.kind,
    'artifactLatestApprovedRevisionId', v_artifact.latest_approved_revision_id::text,
    'artifactLatestRevisionId', v_artifact.latest_revision_id::text,
    'artifactLifecycle', v_artifact.lifecycle,
    'artifactVersion', v_artifact.version,
    'closedByDecisionId', v_request.closed_by_decision_id::text,
    'minimumApprovals', v_minimum,
    'projectId', v_request.project_id::text,
    'revisionContentHash', v_revision.content_hash,
    'revisionId', v_revision.id::text,
    'schemaVersion', 'worksflow-canonical-review-approval-snapshot/v1',
    'soloSelfReview', v_solo,
    'subjectAuthorId', v_revision.created_by::text
  );

  v_request_bytes := canonical_review_jsonb_bytes(v_request_document);
  v_revision_bytes := canonical_review_jsonb_bytes(v_revision_document);
  v_policy_bytes := canonical_review_jsonb_bytes(v_policy_document);
  v_decisions_bytes := canonical_review_jsonb_bytes(v_decisions_document);
  v_governance_bytes := canonical_review_jsonb_bytes(v_governance_document);
  v_approval_bytes := canonical_review_jsonb_bytes(v_approval_document);
  v_request_hash := canonical_review_authority_hash('worksflow.canonical-review.review-request/v1', v_request_bytes);
  v_revision_hash := canonical_review_authority_hash('worksflow.canonical-review.revision/v1', v_revision_bytes);
  v_policy_hash := canonical_review_authority_hash('worksflow.canonical-review.policy/v1', v_policy_bytes);
  v_decisions_hash := canonical_review_authority_hash('worksflow.canonical-review.decisions/v1', v_decisions_bytes);
  v_governance_hash := canonical_review_authority_hash('worksflow.canonical-review.governance/v1', v_governance_bytes);
  v_approval_hash := canonical_review_authority_hash('worksflow.canonical-review.approval/v1', v_approval_bytes);
  v_receipt_document := jsonb_build_object(
    'approval', v_approval_document,
    'componentDigests', jsonb_build_object(
      'approval', v_approval_hash,
      'decisions', v_decisions_hash,
      'governance', v_governance_hash,
      'policy', v_policy_hash,
      'reviewRequest', v_request_hash,
      'revision', v_revision_hash
    ),
    'decisions', v_decisions_document,
    'governance', v_governance_document,
    'issuedAt', to_char(v_issued_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'mediaType', 'application/vnd.worksflow.canonical-review-approval-receipt+json;version=1',
    'policy', v_policy_document,
    'reviewRequest', v_request_document,
    'revision', v_revision_document,
    'schemaVersion', 'worksflow-canonical-review-approval-receipt/v1'
  );
  v_receipt_bytes := canonical_review_jsonb_bytes(v_receipt_document);
  v_receipt_hash := canonical_review_authority_hash('worksflow.canonical-review.receipt/v1', v_receipt_bytes);

  INSERT INTO canonical_review_approval_receipts (
    review_request_id, receipt_hash, receipt_bytes, receipt_document,
    review_request_snapshot_hash, review_request_snapshot_bytes, review_request_snapshot_document,
    revision_snapshot_hash, revision_snapshot_bytes, revision_snapshot_document,
    policy_snapshot_hash, policy_snapshot_bytes, policy_snapshot_document,
    decisions_snapshot_hash, decisions_snapshot_bytes, decisions_snapshot_document,
    governance_snapshot_hash, governance_snapshot_bytes, governance_snapshot_document,
    approval_snapshot_hash, approval_snapshot_bytes, approval_snapshot_document,
    project_id, artifact_id, revision_id, revision_content_hash, closed_by_decision_id,
    approval_count, minimum_approvals, governance_mode, owner_count, solo_self_review, sole_owner_id, issued_at
  ) VALUES (
    v_request.id, v_receipt_hash, v_receipt_bytes, v_receipt_document,
    v_request_hash, v_request_bytes, v_request_document,
    v_revision_hash, v_revision_bytes, v_revision_document,
    v_policy_hash, v_policy_bytes, v_policy_document,
    v_decisions_hash, v_decisions_bytes, v_decisions_document,
    v_governance_hash, v_governance_bytes, v_governance_document,
    v_approval_hash, v_approval_bytes, v_approval_document,
    v_request.project_id, v_artifact.id, v_revision.id, v_revision.content_hash,
    v_request.closed_by_decision_id, v_decision_count, v_minimum,
    v_project.governance_mode, v_owner_count, v_solo, v_sole_owner_id, v_issued_at
  ) RETURNING * INTO v_existing;
  receipt_record := v_existing;
  created := true;
  RETURN NEXT;
  RETURN;
EXCEPTION WHEN unique_violation OR foreign_key_violation THEN
  RAISE EXCEPTION 'Canonical Review authority identity conflicts' USING ERRCODE = 'WCR02';
END;
$function$;

DROP FUNCTION canonical_review_timestamp_is_exact(text);
DROP FUNCTION canonical_review_text_is_trimmed(text);
DROP FUNCTION canonical_review_uuid_is_exact(text);
