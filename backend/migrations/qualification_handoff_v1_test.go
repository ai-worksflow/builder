package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/domain"
)

const qualificationHandoffV1Migration = "000082_qualification_handoff_v1.up.sql"

func TestQualificationHandoffV1DeclaresClosedPrivateBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(qualificationHandoffV1Migration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000082_qualification_handoff_v1.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	for _, required := range []string{
		"CREATE TABLE qualification_promotion_v2_revision_transaction_grants",
		"CREATE TABLE qualification_promotion_v2_revision_authority_bindings",
		"CREATE TABLE qualification_promotion_v2_handoff_lineage_members",
		"CREATE TABLE qualification_promotion_v2_handoff_completions",
		"ADD COLUMN promotion_handoff_id uuid",
		"CREATE UNIQUE INDEX artifact_revisions_ordinary_content_unique",
		"WHERE promotion_handoff_id IS NULL",
		"pg_catalog.pg_current_xact_id()::text",
		"creation_transaction_id text NOT NULL",
		"completion.creation_transaction_id",
		"binding.creation_transaction_id",
		"backend_pid = pg_catalog.pg_backend_pid()",
		"DELETE FROM qualification_promotion_v2_revision_transaction_grants",
		"CREATE FUNCTION complete_qualification_promotion_v2_handoff(p_handoff_id uuid)",
		"CREATE FUNCTION inspect_qualification_promotion_v2_handoff_completion(",
		"pg_catalog.current_setting('transaction_isolation') <> 'serializable'",
		"pg_catalog.pg_is_in_recovery()",
		"pg_catalog.current_setting('transaction_read_only') <> 'off'",
		"qualification_input_precommit_caller_is_v1(",
		"'worksflow_qualification_handoff_operator'",
		"assert_current_workflow_input_authority_v1(",
		"qualification_promotion_v2_store_record_is_exact(",
		"qualification_input_precommit_authority_record_is_exact_v1(",
		"worksflow-qualification-promotion-output-revision-authorities/v1",
		"worksflow.qualification-handoff.revision-authorities/v1",
		"'revisionStateAtHandoff'",
		"'copiedLineage'",
		"worksflow-qualification-promotion-handoff-completion/v1",
		"worksflow.qualification-handoff.completion/v1",
		"'node.completed'",
		"'node.execution_authorization_required'",
		"'waiting_input'",
		"'ActionPublish'",
		"workflow-engine/v3 cannot complete before the qualified release authority",
		"qualification.promotion_handoff.pending",
		"CREATE TRIGGER qualification_promotion_v2_handoff_pending_dispatch",
		"CREATE CONSTRAINT TRIGGER qualification_handoff_grant_empty_closure",
		"CREATE CONSTRAINT TRIGGER qualification_handoff_completion_exact_closure",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Qualification Handoff migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"session_replication_role",
		"GRANT SELECT ON TABLE",
		"ON DELETE CASCADE",
		"completion.xmin::text",
		"binding.xmin::text",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Qualification Handoff migration unexpectedly contains %q", forbidden)
		}
	}
	downText := string(down)
	for _, required := range []string{
		"qualification_handoff_v1_down_guard",
		"cannot roll back Qualification Handoff v1 after durable output exists",
		"LOCK TABLE qualification_promotion_v2_handoff_lineage_members IN ACCESS EXCLUSIVE MODE",
		"DROP TABLE IF EXISTS qualification_promotion_v2_handoff_lineage_members",
		"DROP COLUMN promotion_handoff_id",
		"ADD CONSTRAINT artifact_revisions_artifact_id_content_hash_key",
		"CREATE OR REPLACE FUNCTION reserve_ordinary_artifact_revision_identity_v1()",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("Qualification Handoff rollback is missing %q", required)
		}
	}
}

func TestQualificationHandoffV1EmptyRollbackPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_handoff_v1_empty_")
	applyQualificationPromotionV2Prefix(t, ctx, database)
	applyQualificationHandoffV1(t, ctx, database)
	var tableCount, indexCount, functionCount, definerCount, triggerCount int
	if err := database.QueryRowContext(ctx, `
WITH private_tables AS (
  SELECT oid FROM pg_catalog.pg_class
  WHERE relnamespace=pg_catalog.current_schema()::regnamespace
    AND relkind='r'
    AND relname IN (
      'qualification_promotion_v2_revision_transaction_grants',
      'qualification_promotion_v2_revision_authority_bindings',
      'qualification_promotion_v2_handoff_lineage_members',
      'qualification_promotion_v2_handoff_completions'
    )
), handoff_functions AS (
  SELECT prosecdef FROM pg_catalog.pg_proc
  WHERE pronamespace=pg_catalog.current_schema()::regnamespace
    AND proname IN (
      'qualification_handoff_v1_hash',
      'qualification_handoff_v1_timestamp',
      'reject_qualification_handoff_v1_mutation',
      'enqueue_qualification_promotion_v2_handoff_v1',
      'qualification_handoff_v1_quality_result',
      'qualification_handoff_v1_completion_is_exact',
      'qualification_handoff_v1_completion_bundle',
      'inspect_qualification_promotion_v2_handoff_completion',
      'complete_qualification_promotion_v2_handoff',
      'validate_qualification_handoff_v1_closure'
    )
), handoff_triggers(name) AS (VALUES
  ('qualification_handoff_revision_authorities_immutable'),
  ('qualification_handoff_lineage_members_immutable'),
  ('qualification_handoff_completions_immutable'),
  ('qualification_handoff_outbox_immutable'),
  ('qualification_promotion_v2_handoff_pending_dispatch'),
  ('qualification_handoff_grant_empty_closure'),
  ('qualification_handoff_completion_exact_closure'),
  ('qualification_handoff_revision_authority_exact_closure'),
  ('qualification_handoff_lineage_member_exact_closure'),
  ('qualification_handoff_revision_exact_closure'),
  ('qualification_handoff_revision_source_exact_closure'),
  ('qualification_handoff_dependency_exact_closure'),
  ('qualification_handoff_trace_exact_closure'),
  ('qualification_handoff_event_exact_closure'),
  ('qualification_handoff_outbox_exact_closure'),
  ('qualification_handoff_node_exact_closure'),
  ('qualification_handoff_run_exact_closure')
)
SELECT
  (SELECT count(*) FROM private_tables),
  (SELECT count(*)
   FROM pg_catalog.pg_index AS catalog_index
   JOIN pg_catalog.pg_class AS index_relation
     ON index_relation.oid=catalog_index.indexrelid
   WHERE index_relation.relnamespace=pg_catalog.current_schema()::regnamespace
     AND (
       catalog_index.indrelid IN (SELECT oid FROM private_tables)
       OR index_relation.relname IN (
         'artifact_revisions_ordinary_content_unique',
         'artifact_revisions_promotion_handoff_unique',
         'qualification_promotion_handoff_pending_dispatch_unique'
       )
     )),
  (SELECT count(*) FROM handoff_functions),
  (SELECT count(*) FILTER (WHERE prosecdef) FROM handoff_functions),
  (SELECT count(*)
   FROM pg_catalog.pg_trigger AS catalog_trigger
   JOIN pg_catalog.pg_class AS trigger_relation
     ON trigger_relation.oid=catalog_trigger.tgrelid
   JOIN handoff_triggers ON handoff_triggers.name=catalog_trigger.tgname
   WHERE trigger_relation.relnamespace=pg_catalog.current_schema()::regnamespace
     AND NOT catalog_trigger.tgisinternal)`,
	).Scan(&tableCount, &indexCount, &functionCount, &definerCount, &triggerCount); err != nil {
		t.Fatalf("inspect Qualification Handoff catalog: %v", err)
	}
	if tableCount != 4 || indexCount != 19 || functionCount != 10 ||
		definerCount != 5 || triggerCount != 17 {
		t.Fatalf("Qualification Handoff catalog tables/indexes/functions/definers/triggers=%d/%d/%d/%d/%d, want 4/19/10/5/17",
			tableCount, indexCount, functionCount, definerCount, triggerCount)
	}
	down, err := files.ReadFile("000082_qualification_handoff_v1.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("empty Qualification Handoff rollback: %v", err)
	}
	var columnExists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema=current_schema()
    AND table_name='artifact_revisions'
    AND column_name='promotion_handoff_id'
)`).Scan(&columnExists); err != nil {
		t.Fatal(err)
	}
	if columnExists {
		t.Fatal("Qualification Handoff output discriminator survived empty rollback")
	}
	var privateTableCount int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM pg_catalog.pg_class
WHERE relnamespace=pg_catalog.current_schema()::regnamespace
  AND relname IN (
    'qualification_promotion_v2_revision_transaction_grants',
    'qualification_promotion_v2_revision_authority_bindings',
    'qualification_promotion_v2_handoff_lineage_members',
    'qualification_promotion_v2_handoff_completions'
  )`).Scan(&privateTableCount); err != nil {
		t.Fatal(err)
	}
	if privateTableCount != 0 {
		t.Fatalf("Qualification Handoff private relations after rollback=%d, want 0", privateTableCount)
	}
}

func TestQualificationHandoffV1FreshReplayAndNoEarlyPublishPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_handoff_v1_fresh_")
	applyQualificationPromotionV2Prefix(t, ctx, database)
	applyQualificationHandoffV1(t, ctx, database)

	wia := seedWorkflowInputCanary(t, ctx, database)
	publishNodeID := upgradeWorkflowInputCanaryToExactV3(t, ctx, database, &wia)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForPromotionV2(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatalf("freeze Handoff Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	receipt := completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	if receipt.receiptID == "" {
		t.Fatal("Handoff fixture did not produce a Receipt v3")
	}
	issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	command := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	promotion := consumeQualificationPromotionV2(t, ctx, database, command)
	if promotion["idempotent"] != false {
		t.Fatalf("Handoff fixture Promotion was not fresh: %#v", promotion)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO workflow_runs(
  id,project_id,definition_version_id,execution_profile_version,
  execution_profile_hash,status,scope,context,event_cursor,started_by
)
SELECT gen_random_uuid(),project_id,definition_version_id,
       execution_profile_version,execution_profile_hash,'completed',
       '{}'::jsonb,'{}'::jsonb,0,started_by
FROM workflow_runs WHERE id=$1`, wia.runID); err == nil ||
		!strings.Contains(err.Error(), "workflow-engine/v3 cannot complete before the qualified release authority") {
		t.Fatalf("completed v3 run insert error=%v, want qualified-release refusal", err)
	}

	var pendingDispatches int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM outbox_events
WHERE aggregate_type='qualification_promotion_v2_handoff'
  AND aggregate_id=$1::text
  AND event_type='qualification.promotion_handoff.pending'
  AND payload=jsonb_build_object('handoffId',$1::text)`, command[3],
	).Scan(&pendingDispatches); err != nil || pendingDispatches != 1 {
		t.Fatalf("pending Handoff dispatches=%d error=%v", pendingDispatches, err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE outbox_events
SET published_at=date_trunc('milliseconds',clock_timestamp())
WHERE aggregate_type='qualification_promotion_v2_handoff'
  AND aggregate_id=$1::text
  AND event_type='qualification.promotion_handoff.pending'`, command[3]); err == nil ||
		!strings.Contains(err.Error(), "WPH03") {
		t.Fatalf("pre-completion Handoff dispatch acknowledgement error=%v, want WPH03", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,parent_revision_id,schema_version,
  content_store,content_ref,content_hash,byte_size,workflow_status,
  change_source,change_summary,created_by
)
SELECT $1,artifact_id,revision_number+1,id,schema_version,
       content_store,content_ref,'sha256:' || repeat('7',64),byte_size,
       'draft','system','reserved-bypass',created_by
FROM artifact_revisions WHERE id=$2`, command[4], wia.targetRevisionID); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("ordinary use of Promotion output reservation error=%v, want WPH02", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,parent_revision_id,schema_version,
  content_store,content_ref,content_hash,byte_size,workflow_status,
  change_source,change_summary,created_by,promotion_handoff_id
)
SELECT $1,artifact_id,revision_number+1,id,schema_version,
       content_store,content_ref,content_hash,byte_size,
       'approved','system','forged-grant',created_by,$2
FROM artifact_revisions WHERE id=$3`, uuid.New(), command[3], wia.targetRevisionID); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("forged Handoff discriminator error=%v, want WPH02", err)
	}
	seedQualificationHandoffParentLineage(t, ctx, database, wia)

	type completionResult struct {
		bundle map[string]any
		err    error
	}
	results := make(chan completionResult, 2)
	for range 2 {
		go func() {
			bundle, completionErr := completeQualificationHandoffV1Raw(ctx, database, command[3])
			results <- completionResult{bundle: bundle, err: completionErr}
		}()
	}
	var fresh, replayed map[string]any
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Qualification Handoff: %v", result.err)
		}
		if result.bundle["idempotent"] == false {
			fresh = result.bundle
		} else if result.bundle["idempotent"] == true {
			replayed = result.bundle
		}
	}
	if fresh == nil || replayed == nil || fresh["handoffId"] != command[3].String() {
		t.Fatalf("concurrent Handoff fresh/replay drifted: fresh=%#v replay=%#v", fresh, replayed)
	}
	delete(fresh, "idempotent")
	delete(replayed, "idempotent")
	freshJSON, _ := json.Marshal(fresh)
	replayJSON, _ := json.Marshal(replayed)
	if string(freshJSON) != string(replayJSON) {
		t.Fatalf("Handoff replay differs from immutable fresh bundle")
	}

	var outputCount, completionCount, authorityCount, eventCount, outboxCount int
	var lineageCount, lineageTransactionMismatch int
	var authorityByteSize, largestFrozenTraceMetadata int
	var parentHash, outputHash, gateStatus, publishStatus, runStatus string
	var completionCreationTransactionID, bindingCreationTransactionID string
	var completionTupleXID, bindingTupleXID string
	var publishActor sql.NullString
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM artifact_revisions WHERE id=$1),
  (SELECT count(*) FROM qualification_promotion_v2_handoff_completions WHERE handoff_id=$2),
  (SELECT count(*) FROM qualification_promotion_v2_revision_authority_bindings WHERE handoff_id=$2),
  (SELECT count(*) FROM workflow_run_events WHERE run_id=$3 AND sequence IN (7,8)),
  (SELECT count(*) FROM outbox_events WHERE id IN (
    SELECT id FROM workflow_run_events WHERE run_id=$3 AND sequence IN (7,8)
  )),
  (SELECT content_hash FROM artifact_revisions WHERE id=$4),
  (SELECT content_hash FROM artifact_revisions WHERE id=$1),
  (SELECT status FROM workflow_node_runs WHERE id=$5),
	  (SELECT status FROM workflow_node_runs WHERE id=$6),
	  (SELECT status FROM workflow_runs WHERE id=$3),
	  (SELECT context#>'{nodes,publish,executionActor}' FROM workflow_runs WHERE id=$3),
	  (SELECT count(*) FROM qualification_promotion_v2_handoff_lineage_members
	   WHERE handoff_id=$2),
	  (SELECT count(*)
	   FROM qualification_promotion_v2_handoff_lineage_members AS member
	   JOIN qualification_promotion_v2_handoff_completions AS completion
	     ON completion.handoff_id=member.handoff_id
	   WHERE member.handoff_id=$2
	     AND member.creation_transaction_id<>completion.creation_transaction_id),
	  (SELECT octet_length(authority_bytes)
	   FROM qualification_promotion_v2_revision_authority_bindings
	   WHERE handoff_id=$2),
	  (SELECT max(octet_length(trace.metadata::text))
	   FROM qualification_promotion_v2_handoff_lineage_members AS member
	   JOIN trace_links AS trace ON trace.id=member.member_key::uuid
	   WHERE member.handoff_id=$2 AND member.member_kind='trace'),
	  (SELECT creation_transaction_id
	   FROM qualification_promotion_v2_handoff_completions WHERE handoff_id=$2),
	  (SELECT creation_transaction_id
	   FROM qualification_promotion_v2_revision_authority_bindings WHERE handoff_id=$2),
	  (SELECT xmin::text
	   FROM qualification_promotion_v2_handoff_completions WHERE handoff_id=$2),
	  (SELECT xmin::text
	   FROM qualification_promotion_v2_revision_authority_bindings WHERE handoff_id=$2)
	`, command[4], command[3], wia.runID, wia.targetRevisionID, wia.gateNodeID, publishNodeID).Scan(
		&outputCount, &completionCount, &authorityCount, &eventCount, &outboxCount,
		&parentHash, &outputHash, &gateStatus, &publishStatus, &runStatus, &publishActor,
		&lineageCount, &lineageTransactionMismatch, &authorityByteSize,
		&largestFrozenTraceMetadata, &completionCreationTransactionID,
		&bindingCreationTransactionID, &completionTupleXID, &bindingTupleXID,
	); err != nil {
		t.Fatal(err)
	}
	if outputCount != 1 || completionCount != 1 || authorityCount != 1 ||
		eventCount != 2 || outboxCount != 2 || parentHash != outputHash ||
		gateStatus != "completed" || publishStatus != "waiting_input" ||
		runStatus != "waiting_input" || publishActor.Valid {
		t.Fatalf("Handoff closure output/completion/authority/events/outbox=%d/%d/%d/%d/%d hashes=%q/%q gate/publish/run=%q/%q/%q actor=%s",
			outputCount, completionCount, authorityCount, eventCount, outboxCount,
			parentHash, outputHash, gateStatus, publishStatus, runStatus, publishActor.String)
	}
	if lineageCount < 2 || lineageTransactionMismatch != 0 ||
		authorityByteSize >= 1048576 || largestFrozenTraceMetadata <= 1048576 {
		t.Fatalf("Handoff bounded lineage count=%d transactionMismatch=%d authorityBytes=%d largestTraceMetadata=%d",
			lineageCount, lineageTransactionMismatch, authorityByteSize, largestFrozenTraceMetadata)
	}
	if completionCreationTransactionID != bindingCreationTransactionID ||
		completionCreationTransactionID == completionTupleXID ||
		bindingCreationTransactionID == bindingTupleXID {
		t.Fatalf("Handoff explicit creation transaction=%q/%q tuple xmin=%q/%q; want one top xid distinct from PL/pgSQL exception subxids",
			completionCreationTransactionID, bindingCreationTransactionID,
			completionTupleXID, bindingTupleXID)
	}

	var inspected []byte
	if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_promotion_v2_handoff_completion($1) AS value`, command[3],
	).Scan(&inspected); err != nil {
		t.Fatalf("inspect Handoff completion: %v", err)
	}
	if len(inspected) >= 1048576 {
		t.Fatalf("Handoff inspection expanded frozen >1 MiB trace metadata: bundleBytes=%d", len(inspected))
	}
	var inspection map[string]any
	if err := json.Unmarshal(inspected, &inspection); err != nil {
		t.Fatal(err)
	}
	if _, exists := inspection["idempotent"]; exists {
		t.Fatal("inspect-only Handoff bundle exposed completion idempotency")
	}
	if _, err := database.ExecContext(ctx, `
UPDATE outbox_events
SET attempts=attempts+1,
    available_at=available_at+interval '1 second',
    last_error='simulated retry'
WHERE id IN (
  SELECT id FROM workflow_run_events WHERE run_id=$1 AND sequence IN (7,8)
)`, wia.runID); err != nil {
		t.Fatalf("advance mutable Handoff Outbox delivery state: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_promotion_v2_handoff_completion($1) AS value`, command[3],
	).Scan(&inspected); err != nil {
		t.Fatalf("Handoff inspection depended on mutable Outbox delivery state: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE outbox_events
SET published_at=date_trunc('milliseconds',clock_timestamp())
WHERE aggregate_type='qualification_promotion_v2_handoff'
  AND aggregate_id=$1::text
  AND event_type='qualification.promotion_handoff.pending'`, command[3]); err != nil {
		t.Fatalf("post-inspection Handoff dispatch acknowledgement: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE outbox_events SET payload='{}'::jsonb
WHERE id=(SELECT gate_completed_event_id
          FROM qualification_promotion_v2_handoff_completions WHERE handoff_id=$1)`, command[3]); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("Handoff Outbox immutable payload update error=%v, want WPH02", err)
	}
	tamper, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		_ = tamper.Rollback()
		t.Fatal(err)
	}
	if _, err := tamper.ExecContext(ctx, `
UPDATE qualification_promotion_v2_revision_authority_bindings
SET authority_bytes=convert_to('{}','UTF8')
WHERE handoff_id=$1`, command[3]); err != nil {
		_ = tamper.Rollback()
		t.Fatal(err)
	}
	var ignored []byte
	inspectErr := tamper.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_promotion_v2_handoff_completion($1) AS value`, command[3],
	).Scan(&ignored)
	_ = tamper.Rollback()
	if inspectErr == nil || !strings.Contains(inspectErr.Error(), "WPH02") {
		t.Fatalf("tampered Handoff inspection error=%v, want WPH02", inspectErr)
	}
	assertQualificationHandoffReplayAllowsPostHandoffEvolution(
		t, ctx, database, wia, command[3], command[4], inspected,
	)

	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,parent_revision_id,schema_version,
  content_store,content_ref,content_hash,byte_size,workflow_status,
  change_source,change_summary,created_by
)
SELECT gen_random_uuid(),artifact_id,
       (SELECT max(candidate.revision_number)+1 FROM artifact_revisions AS candidate
        WHERE candidate.artifact_id=source.artifact_id),
       id,schema_version,
       content_store,content_ref,content_hash,byte_size,'draft','system','ordinary',created_by
FROM artifact_revisions AS source WHERE id=$1`, command[4]); err == nil {
		t.Fatal("ordinary same-content Revision bypassed migration82 partial uniqueness")
	}

	down, err := files.ReadFile("000082_qualification_handoff_v1.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "55000") {
		t.Fatalf("durable Handoff output rollback error=%v, want 55000", err)
	}
}

func TestQualificationHandoffV1LargeLineageHasBoundedClosurePostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_handoff_v1_capacity_")
	applyQualificationPromotionV2Prefix(t, ctx, database)
	applyQualificationHandoffV1(t, ctx, database)

	wia := seedWorkflowInputCanary(t, ctx, database)
	upgradeWorkflowInputCanaryToExactV3(t, ctx, database, &wia)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForPromotionV2(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatalf("freeze capacity Handoff Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	command := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	consumeQualificationPromotionV2(t, ctx, database, command)

	const membersPerKind = 512
	seedQualificationHandoffCapacityLineage(t, ctx, database, wia, membersPerKind)
	calls, executionTime, elapsed := completeQualificationHandoffV1Tracked(
		t, ctx, database, command[3],
	)
	t.Logf("large Handoff membersPerKind=%d exactCalls=%d exactExecutionMs=%.3f elapsed=%s",
		membersPerKind, calls, executionTime, elapsed)
	if calls < 2 || calls > 64 {
		t.Fatalf("large Handoff exact closure calls=%d, want a member-count-independent bounded count", calls)
	}
	if elapsed >= 45*time.Second {
		t.Fatalf("large Handoff exceeded bounded completion window: elapsed=%s exactExecutionMs=%.3f", elapsed, executionTime)
	}

	var lineageCount int
	var exact bool
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*)
   FROM qualification_promotion_v2_handoff_lineage_members
   WHERE handoff_id=$1),
  qualification_handoff_v1_completion_is_exact($1)`, command[3],
	).Scan(&lineageCount, &exact); err != nil {
		t.Fatalf("inspect large Handoff lineage: %v", err)
	}
	if lineageCount < 2*membersPerKind || !exact {
		t.Fatalf("large Handoff lineage count=%d exact=%t, want at least %d exact members",
			lineageCount, exact, 2*membersPerKind)
	}
}

func seedQualificationHandoffCapacityLineage(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	membersPerKind int,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_dependencies(
  id,project_id,source_artifact_id,source_revision_id,source_content_hash,
  target_artifact_id,target_revision_id,relation,required,created_by,created_at
)
SELECT gen_random_uuid(),$1,source.artifact_id,source.id,source.content_hash,
       target.artifact_id,target.id,
       'handoff-capacity-dependency-' || member.number::text,
       (member.number % 2)=0,$2,date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source
CROSS JOIN artifact_revisions AS target
CROSS JOIN generate_series(1,$5) AS member(number)
WHERE source.id=$3 AND target.id=$4`,
		wia.projectID, wia.userID, wia.targetRevisionID, wia.reportRevisionID,
		membersPerKind,
	); err != nil {
		t.Fatalf("seed %d Handoff capacity dependencies: %v", membersPerKind, err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO trace_links(
  id,project_id,source_artifact_id,source_revision_id,source_anchor_id,
  target_artifact_id,target_revision_id,target_anchor_id,relation,metadata,
  created_by,created_at
)
SELECT gen_random_uuid(),$1,source.artifact_id,source.id,
       'handoff-capacity-source-' || member.number::text,
       target.artifact_id,target.id,
       'handoff-capacity-target-' || member.number::text,
       'handoff-capacity-trace',
       jsonb_build_object('member',member.number,'payload',repeat('z',64)),
       $2,date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source
CROSS JOIN artifact_revisions AS target
CROSS JOIN generate_series(1,$5) AS member(number)
WHERE source.id=$3 AND target.id=$4`,
		wia.projectID, wia.userID, wia.targetRevisionID, wia.reportRevisionID,
		membersPerKind,
	); err != nil {
		t.Fatalf("seed %d Handoff capacity traces: %v", membersPerKind, err)
	}
}

func completeQualificationHandoffV1Tracked(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	handoffID uuid.UUID,
) (int64, float64, time.Duration) {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SET track_functions='pl'`); err != nil {
		t.Fatalf("enable Handoff function tracking: %v", err)
	}
	var exactFunctionOID int64
	if err := connection.QueryRowContext(ctx, `
SELECT 'qualification_handoff_v1_completion_is_exact(uuid)'::regprocedure::oid`,
	).Scan(&exactFunctionOID); err != nil {
		t.Fatalf("resolve Handoff exact function OID: %v", err)
	}
	if _, err := connection.ExecContext(ctx, `
SELECT pg_stat_reset_single_function_counters($1::oid)`, exactFunctionOID); err != nil {
		t.Fatalf("reset Handoff exact function counters: %v", err)
	}
	if _, err := connection.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text,0
  )
)`, handoffID); err != nil {
		t.Fatalf("lock tracked Handoff completion: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = connection.ExecContext(context.WithoutCancel(ctx), `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text,0
  )
)`, handoffID)
		}
	}()
	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL statement_timeout='45s'`); err != nil {
		t.Fatalf("bound large Handoff completion statement: %v", err)
	}
	startedAt := time.Now()
	var ignored []byte
	if err := transaction.QueryRowContext(ctx, `
SELECT value FROM complete_qualification_promotion_v2_handoff($1) AS value`, handoffID,
	).Scan(&ignored); err != nil {
		t.Fatalf("complete tracked large Handoff: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit tracked large Handoff: %v", err)
	}
	elapsed := time.Since(startedAt)
	var unlocked bool
	if err := connection.QueryRowContext(ctx, `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text,0
  )
)`, handoffID).Scan(&unlocked); err != nil || !unlocked {
		t.Fatalf("unlock tracked Handoff completion unlocked=%t: %v", unlocked, err)
	}
	locked = false
	if _, err := connection.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatalf("flush Handoff function counters: %v", err)
	}
	if _, err := connection.ExecContext(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
		t.Fatalf("clear Handoff function statistics snapshot: %v", err)
	}
	var calls int64
	var totalExecutionTime float64
	if err := connection.QueryRowContext(ctx, `
SELECT calls,total_time
FROM pg_stat_user_functions WHERE funcid=$1::oid`, exactFunctionOID,
	).Scan(&calls, &totalExecutionTime); err != nil {
		t.Fatalf("read Handoff exact function counters: %v", err)
	}
	return calls, totalExecutionTime, elapsed
}

func seedQualificationHandoffParentLineage(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_dependencies(
  id,project_id,source_artifact_id,source_revision_id,source_content_hash,
  target_artifact_id,target_revision_id,relation,required,created_by,created_at
)
SELECT $1,$2,source.artifact_id,source.id,source.content_hash,
       target.artifact_id,target.id,'handoff-frozen-copy',true,$3,
       date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source, artifact_revisions AS target
WHERE source.id=$4 AND target.id=$5`,
		uuid.New(), wia.projectID, wia.userID, wia.targetRevisionID, wia.reportRevisionID,
	); err != nil {
		t.Fatalf("seed Handoff dependency copy: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO trace_links(
  id,project_id,source_artifact_id,source_revision_id,source_anchor_id,
  target_artifact_id,target_revision_id,target_anchor_id,relation,metadata,
  created_by,created_at
)
SELECT $1,$2,source.artifact_id,source.id,'handoff-source',
       target.artifact_id,target.id,'report','handoff-frozen-copy',
       jsonb_build_object(
         'frozen',true,'largePayload',repeat('x',1100000)
       ),$3,
       date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source, artifact_revisions AS target
WHERE source.id=$4 AND target.id=$5`,
		uuid.New(), wia.projectID, wia.userID, wia.targetRevisionID, wia.reportRevisionID,
	); err != nil {
		t.Fatalf("seed Handoff trace copy: %v", err)
	}
}

func assertQualificationHandoffReplayAllowsPostHandoffEvolution(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	handoffID uuid.UUID,
	outputRevisionID uuid.UUID,
	inspectionBefore []byte,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `
UPDATE artifact_revisions SET promotion_handoff_id=NULL WHERE id=$1`,
		outputRevisionID,
	); err == nil {
		t.Fatal("Handoff output discriminator was mutable")
	}
	if _, err := database.ExecContext(ctx, `
UPDATE artifact_revisions SET workflow_status='draft' WHERE id=$1`,
		outputRevisionID,
	); err == nil || !strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("invalid Handoff output lifecycle error=%v, want WPH02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE artifact_revisions
SET approved_at=approved_at+interval '1 millisecond' WHERE id=$1`,
		outputRevisionID,
	); err == nil || !strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("mutable Handoff output approved_at error=%v, want WPH02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE artifact_revisions
SET workflow_status='approved',superseded_at=NULL WHERE id=$1`,
		wia.targetRevisionID,
	); err == nil || !strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("reopened Handoff parent lifecycle error=%v, want WPH02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE artifact_dependencies AS dependency
SET source_revision_id=$2,relation='handoff-frozen-copy-tampered'
FROM qualification_promotion_v2_handoff_lineage_members AS member
WHERE member.handoff_id=$1 AND member.member_kind='dependency'
  AND dependency.id=member.member_key::uuid
  AND dependency.relation='handoff-frozen-copy'`, handoffID, wia.targetRevisionID); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("move frozen Handoff dependency off output error=%v, want WPH02", err)
	}
	assertQualificationHandoffMultiIDClosureRoutesAll(
		t, ctx, database, handoffID, outputRevisionID,
	)
	if _, err := database.ExecContext(ctx, `
	UPDATE trace_links AS trace
SET metadata=jsonb_set(metadata,'{largePayload}',to_jsonb('tampered'::text),false)
FROM qualification_promotion_v2_handoff_lineage_members AS member
WHERE member.handoff_id=$1 AND member.member_kind='trace'
  AND trace.id=member.member_key::uuid
  AND trace.relation='handoff-frozen-copy'`, handoffID); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("tamper frozen large Handoff trace metadata error=%v, want WPH02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE trace_links AS trace
SET source_revision_id=$2,relation='handoff-frozen-copy-tampered'
FROM qualification_promotion_v2_handoff_lineage_members AS member
WHERE member.handoff_id=$1 AND member.member_kind='trace'
  AND trace.id=member.member_key::uuid
  AND trace.relation='handoff-frozen-copy'`, handoffID, wia.targetRevisionID); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("move frozen Handoff trace off output error=%v, want WPH02", err)
	}
	if _, err := database.ExecContext(ctx, `
	UPDATE qualification_promotion_v2_handoff_lineage_members
	SET row_hash='sha256:' || repeat('0',64)
	WHERE handoff_id=$1`, handoffID); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("tamper frozen Handoff member ledger error=%v, want WPH02", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_dependencies(
  id,project_id,source_artifact_id,source_revision_id,source_content_hash,
  target_artifact_id,target_revision_id,relation,required,created_by,created_at
)
SELECT $1,$2,source.artifact_id,source.id,source.content_hash,
       target.artifact_id,target.id,'handoff-post-copy-output',false,$3,
       date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source, artifact_revisions AS target
WHERE source.id=$4 AND target.id=$5`,
		uuid.New(), wia.projectID, wia.userID, outputRevisionID, wia.reportRevisionID,
	); err != nil {
		t.Fatalf("append legitimate output dependency after Handoff: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO trace_links(
  id,project_id,source_artifact_id,source_revision_id,source_anchor_id,
  target_artifact_id,target_revision_id,target_anchor_id,relation,metadata,
  created_by,created_at
)
SELECT $1,$2,source.artifact_id,source.id,'handoff-output',
       target.artifact_id,target.id,'report','handoff-post-copy-output',
       '{"postHandoff":true}'::jsonb,$3,
       date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source, artifact_revisions AS target
WHERE source.id=$4 AND target.id=$5`,
		uuid.New(), wia.projectID, wia.userID, outputRevisionID, wia.reportRevisionID,
	); err != nil {
		t.Fatalf("append legitimate output trace after Handoff: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_dependencies(
  id,project_id,source_artifact_id,source_revision_id,source_content_hash,
  target_artifact_id,target_revision_id,relation,required,created_by,created_at
)
SELECT $1,$2,source.artifact_id,source.id,source.content_hash,
       target.artifact_id,target.id,'handoff-post-copy-parent',false,$3,
       date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source, artifact_revisions AS target
WHERE source.id=$4 AND target.id=$5`,
		uuid.New(), wia.projectID, wia.userID, wia.targetRevisionID, wia.reportRevisionID,
	); err != nil {
		t.Fatalf("append ordinary parent dependency after Handoff: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO trace_links(
  id,project_id,source_artifact_id,source_revision_id,source_anchor_id,
  target_artifact_id,target_revision_id,target_anchor_id,relation,metadata,
  created_by,created_at
)
SELECT $1,$2,source.artifact_id,source.id,'handoff-parent',
       target.artifact_id,target.id,'report','handoff-post-copy-parent',
       '{"postHandoff":true}'::jsonb,$3,
       date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions AS source, artifact_revisions AS target
WHERE source.id=$4 AND target.id=$5`,
		uuid.New(), wia.projectID, wia.userID, wia.targetRevisionID, wia.reportRevisionID,
	); err != nil {
		t.Fatalf("append ordinary parent trace after Handoff: %v", err)
	}
	assertQualificationHandoffInspectionBytes(
		t, ctx, database, handoffID, inspectionBefore, "post-lineage-growth",
	)

	nextRevisionID := uuid.New()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,parent_revision_id,schema_version,
  content_store,content_ref,content_hash,byte_size,workflow_status,
  change_source,change_summary,created_by,created_at,approved_at
)
SELECT $1,artifact_id,revision_number+1,id,schema_version,
       content_store,content_ref,'sha256:' || repeat('6',64),byte_size,
       'approved','system','post-Handoff ordinary Revision',$2,
       date_trunc('milliseconds',clock_timestamp()),
       date_trunc('milliseconds',clock_timestamp())
FROM artifact_revisions WHERE id=$3`, nextRevisionID, wia.userID, outputRevisionID); err != nil {
		t.Fatalf("insert post-Handoff ordinary Revision: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE artifact_revisions
SET workflow_status='superseded',
    superseded_at=date_trunc('milliseconds',clock_timestamp())
WHERE id=$1 AND workflow_status='approved' AND superseded_at IS NULL`,
		outputRevisionID,
	); err != nil {
		t.Fatalf("normally supersede Handoff output: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE artifacts
SET latest_revision_id=$1,latest_approved_revision_id=$1,version=version+1,
    updated_at=date_trunc('milliseconds',clock_timestamp())
WHERE id=$2 AND latest_revision_id=$3 AND latest_approved_revision_id=$3`,
		nextRevisionID, wia.targetArtifactID, outputRevisionID,
	); err != nil {
		t.Fatalf("advance artifact after Handoff: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit normal post-Handoff supersede: %v", err)
	}
	assertQualificationHandoffInspectionBytes(
		t, ctx, database, handoffID, inspectionBefore, "post-supersede",
	)
}

func assertQualificationHandoffMultiIDClosureRoutesAll(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	handoffID uuid.UUID,
	outputRevisionID uuid.UUID,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	fakeHandoffID := uuid.New()
	fakeOutputRevisionID := uuid.New()
	fakeOperationID := uuid.New()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,parent_revision_id,schema_version,
  content_store,content_ref,content_hash,byte_size,workflow_status,
  change_source,change_summary,source_manifest_id,proposal_id,
  implementation_proposal_id,created_by,created_at,approved_at,
  superseded_at,promotion_handoff_id
)
SELECT $1,source.artifact_id,
       (SELECT max(candidate.revision_number)+1
        FROM artifact_revisions AS candidate
        WHERE candidate.artifact_id=source.artifact_id),
       source.id,source.schema_version,source.content_store,source.content_ref,
       source.content_hash,source.byte_size,'approved','system',
       'synthetic multi-Handoff closure probe',source.source_manifest_id,
       source.proposal_id,source.implementation_proposal_id,source.created_by,
       date_trunc('milliseconds',clock_timestamp()),
       date_trunc('milliseconds',clock_timestamp()),NULL,$2
FROM artifact_revisions AS source WHERE source.id=$3`,
		fakeOutputRevisionID, fakeHandoffID, outputRevisionID,
	); err != nil {
		t.Fatalf("seed synthetic second Handoff output: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO qualification_promotion_v2_handoff_completions(
  handoff_id,operation_id,consumption_hash,output_revision_id,
  output_revision_content_hash,project_id,workflow_run_id,node_run_id,node_key,
	  publish_node_run_id,publish_node_key,event_cursor_before,event_cursor_after,
	  gate_output_document,gate_completed_event_id,publish_authorization_event_id,
	  completion_hash,completion_bytes,completion_document,
	  creation_transaction_id,completed_at
)
SELECT $1::uuid,$2::uuid,
       'sha256:' || encode(sha256(convert_to($2::uuid::text,'UTF8')),'hex'),$3::uuid,
       output_revision_content_hash,project_id,workflow_run_id,node_run_id,node_key,
       publish_node_run_id,publish_node_key,event_cursor_before,event_cursor_after,
       gate_output_document,$4::uuid,$5::uuid,
       'sha256:' || encode(sha256(convert_to($1::uuid::text || ':completion','UTF8')),'hex'),
	       convert_to('{}','UTF8'),'{}'::jsonb,
	       pg_current_xact_id()::text,completed_at
FROM qualification_promotion_v2_handoff_completions WHERE handoff_id=$6::uuid`,
		fakeHandoffID, fakeOperationID, fakeOutputRevisionID,
		uuid.New(), uuid.New(), handoffID,
	); err != nil {
		t.Fatalf("seed synthetic second Handoff completion: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=origin`); err != nil {
		t.Fatal(err)
	}
	stub := fmt.Sprintf(`
CREATE OR REPLACE FUNCTION qualification_handoff_v1_completion_is_exact(p_handoff_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE CALLED ON NULL INPUT PARALLEL UNSAFE
SECURITY INVOKER
AS $function$
  SELECT p_handoff_id IS DISTINCT FROM '%s'::uuid
$function$`, handoffID)
	if _, err := transaction.ExecContext(ctx, stub); err != nil {
		t.Fatalf("install transactional multi-ID exactness probe: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE artifact_dependencies AS dependency
SET source_revision_id=$2,relation='handoff-frozen-copy-cross-handoff-tampered'
FROM qualification_promotion_v2_handoff_lineage_members AS member
WHERE member.handoff_id=$1 AND member.member_kind='dependency'
  AND dependency.id=member.member_key::uuid
  AND dependency.relation='handoff-frozen-copy'`, handoffID, fakeOutputRevisionID); err != nil {
		t.Fatalf("stage cross-Handoff endpoint tamper: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err == nil ||
		!strings.Contains(err.Error(), "WPH02") {
		t.Fatalf("multi-Handoff OLD+NEW closure routing error=%v, want WPH02", err)
	}
}

func assertQualificationHandoffInspectionBytes(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	handoffID uuid.UUID,
	want []byte,
	stage string,
) {
	t.Helper()
	var got []byte
	if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_promotion_v2_handoff_completion($1) AS value`,
		handoffID,
	).Scan(&got); err != nil {
		t.Fatalf("inspect Handoff %s: %v", stage, err)
	}
	if string(got) != string(want) {
		t.Fatalf("immutable Handoff inspection changed %s\n before=%s\n  after=%s",
			stage, want, got)
	}
}

func TestQualificationHandoffV1BackfillsPreexistingPendingDispatchPostgresCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_handoff_v1_backfill_")
	applyQualificationPromotionV2Prefix(t, ctx, database)
	wia := seedWorkflowInputCanary(t, ctx, database)
	upgradeWorkflowInputCanaryToExactV3(t, ctx, database, &wia)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForPromotionV2(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatal(err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	command := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	consumeQualificationPromotionV2(t, ctx, database, command)

	var before int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM outbox_events
WHERE event_type='qualification.promotion_handoff.pending'
  AND aggregate_id=$1::text`, command[3]).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("pre-migration82 Handoff unexpectedly had %d dispatches", before)
	}
	applyQualificationHandoffV1(t, ctx, database)
	var after int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM outbox_events
WHERE aggregate_type='qualification_promotion_v2_handoff'
  AND aggregate_id=$1::text
  AND event_type='qualification.promotion_handoff.pending'
  AND payload=jsonb_build_object('handoffId',$1::text)`, command[3]).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("migration82 pending-dispatch backfill count=%d, want 1", after)
	}
	down, err := files.ReadFile("000082_qualification_handoff_v1.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("rollback unmaterialized migration82 pending dispatch: %v", err)
	}
}

func applyQualificationHandoffV1(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	up, err := files.ReadFile(qualificationHandoffV1Migration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(up)); err != nil {
		t.Fatalf("apply Qualification Handoff v1: %v", err)
	}
}

func upgradeWorkflowInputCanaryToExactV3(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture *workflowInputCanary,
) uuid.UUID {
	t.Helper()
	seeded, _ := workflowExecutionProfileV3MigrationDefinition(t)
	edges := append([]domain.WorkflowEdge(nil), seeded.Edges...)
	for index := range edges {
		if edges[index].From == "quality" && edges[index].To == "external-qualification" {
			edges[index].ID = "quality-to-external"
		}
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		fixture.definitionID.String(), 1, "Qualification Handoff v3", "6",
		seeded.Nodes, edges, *seeded.InputContract, *seeded.OutputContract,
		seeded.ExecutionProfile, fixture.userID.String(), time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	publishNodeID := uuid.New()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_definition_versions
SET schema_version=6,content=$2,content_hash=$3
WHERE id=$1`, fixture.definitionVersionID, encoded, definition.Hash); err != nil {
		t.Fatalf("upgrade WIA fixture definition to exact v3: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE workflow_runs
SET context=jsonb_set(
  context,'{nodes,publish}',
  jsonb_build_object('definitionNodeId','publish'),true
)
WHERE id=$1`, fixture.runID); err != nil {
		t.Fatalf("add exact Publish context metadata: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workflow_node_runs(
  id,run_id,node_key,node_type,status,definition_node_id,slice_kind
) VALUES($1,$2,'publish','publish','pending','publish','root')`, publishNodeID, fixture.runID); err != nil {
		t.Fatalf("add exact Publish successor: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	fixture.definitionRaw = encoded
	return publishNodeID
}

func completeQualificationHandoffV1(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	handoffID uuid.UUID,
) map[string]any {
	t.Helper()
	bundle, err := completeQualificationHandoffV1Raw(ctx, database, handoffID)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func completeQualificationHandoffV1Raw(
	ctx context.Context,
	database *sql.DB,
	handoffID uuid.UUID,
) (map[string]any, error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text,0
  )
)`, handoffID); err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			_, _ = connection.ExecContext(context.WithoutCancel(ctx), `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text,0
  )
)`, handoffID)
		}
	}()
	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	var encoded []byte
	if err := transaction.QueryRowContext(ctx, `
SELECT value FROM complete_qualification_promotion_v2_handoff($1) AS value`, handoffID,
	).Scan(&encoded); err != nil {
		_ = transaction.Rollback()
		return nil, fmt.Errorf("complete Qualification Handoff: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit Qualification Handoff: %w", err)
	}
	var unlocked bool
	if err := connection.QueryRowContext(ctx, `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-handoff-v1:' || $1::text,0
  )
)`, handoffID).Scan(&unlocked); err != nil || !unlocked {
		return nil, fmt.Errorf("unlock Qualification Handoff session unlocked=%t: %w", unlocked, err)
	}
	locked = false
	var bundle map[string]any
	if err := json.Unmarshal(encoded, &bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}
