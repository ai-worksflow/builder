package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	qualificationinput "github.com/worksflow/builder/backend/internal/qualificationinputauthority"
	qualificationpolicy "github.com/worksflow/builder/backend/internal/qualificationpolicyauthority"
	qualificationpromotionv2 "github.com/worksflow/builder/backend/internal/qualificationpromotionv2"
)

const qualificationPromotionV2Migration = "000081_qualification_promotion_v2.up.sql"

func TestQualificationPromotionV2DeclaresClosedFailClosedBoundary(t *testing.T) {
	t.Parallel()
	up, err := files.ReadFile(qualificationPromotionV2Migration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000081_qualification_promotion_v2.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	fence := strings.Index(text, "SELECT pg_catalog.pg_advisory_xact_lock(")
	firstDDL := strings.Index(text, "CREATE FUNCTION qualification_promotion_v2_hash")
	if fence < 0 || firstDDL < 0 || fence > firstDDL {
		t.Fatal("Promotion v2 migration must acquire the exclusive WIA rollout fence before DDL")
	}
	for _, required := range []string{
		"CREATE TABLE artifact_revision_identity_reservations",
		"CREATE TABLE qualification_promotion_v2_independent_receipts",
		"CREATE TABLE qualification_promotion_v2_consumptions",
		"input_precommit_authority_id uuid NOT NULL UNIQUE",
		"REFERENCES qualification_input_precommit_authorities(authority_id) ON DELETE RESTRICT",
		"CREATE TABLE qualification_promotion_v2_consumption_independent_receipts",
		"CREATE TABLE qualification_promotion_v2_handoffs",
		"CREATE TABLE qualification_promotion_v2_identity_reservations",
		"owner_kind IN ('artifact-revision','qualification-promotion-v2')",
		"CREATE TRIGGER artifact_revisions_shared_identity_reservation",
		"CREATE FUNCTION consume_qualification_promotion_v2(",
		"CREATE FUNCTION inspect_qualification_promotion_v2_operation",
		"CREATE FUNCTION resolve_qualification_promotion_v2_handoff",
		"CREATE FUNCTION assert_pending_qualification_promotion_v2_handoff",
		"CREATE FUNCTION inspect_historical_qualification_promotion_v1_operation",
		"worksflow-qualification-promotion-store-bundle/v2",
		"worksflow-qualification-promotion-evidence-event-set/v2",
		"worksflow.qualification-promotion.evidence-event-set/v2",
		"pg_catalog.current_setting('transaction_isolation') <> 'serializable'",
		"pg_catalog.pg_is_in_recovery()",
		"pg_catalog.current_setting('transaction_read_only') <> 'off'",
		"Qualification Promotion v2 inspection requires a read-write primary",
		"LOCK TABLE qualification_evidence_events IN ROW SHARE MODE",
		"LOCK TABLE qualification_evidence_operations IN ROW SHARE MODE",
		"LOCK TABLE qualification_evidence_heads IN ROW SHARE MODE",
		"FROM projects WHERE id = v_wia.project_id FOR UPDATE",
		"assert_current_workflow_input_authority_v1(",
		"assert_current_qualification_policy_authority_v1(",
		"resolve_qualification_input_precommit_for_promotion_v1(",
		"qualification_input_precommit_authority_record_is_exact_v1(",
		"exact Qualification Input Precommit authority is not ready",
		"Qualification Input Precommit authority resolution is non-unique",
		"Qualification Input Precommit drifted from locked WIA, current Policy, or Plan",
		"policy-required independent authority adapters are not deployed",
		"Qualification Evidence operation set does not equal the immutable Plan reservations",
		"Qualification Receipt v3 four-request closure is not ready",
		"Qualification Plan input differs from the reviewed Policy profile",
		"terminal Qualification Receipt v3 lineage differs from the locked Plan",
		"v_receipt.payload_document#>'{predicate,source}'",
		"v_policy.plan_input_profile_document->'trustBindings'",
		"v_runner_request.payload_bytes IS DISTINCT FROM v_receipt.payload_bytes",
		"v_runner_request.pae_bytes IS DISTINCT FROM (",
		"v_receipt.payload_document#>'{predicate,artifactIndex}'",
		"v_receipt.completion_document IS DISTINCT FROM",
		"v_consumption.closure_document->'independentAuthorities' = '[]'::jsonb",
		"'inputPrecommit', pg_catalog.jsonb_build_object(",
		"'kind', 'qualification-input-precommit'",
		"v_consumption.closure_document = v_expected_closure",
		"v_consumption.revision_intent_document = v_expected_revision_intent",
		"v_handoff.handoff_document = v_expected_handoff",
		"(v_handoff.output_revision_id, 'output-revision'::text, 2::smallint)",
		"RETURN NEXT qualification_promotion_v2_store_bundle(p_operation_id, true, true)",
		"'idempotent', COALESCE(p_idempotent, false)",
		"REVOKE ALL ON FUNCTION %I.assert_current_workflow_input_authority_v1(uuid)",
		"GRANT EXECUTE ON FUNCTION %I.consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Qualification Promotion v2 migration is missing %q", required)
		}
	}
	independentFailure := strings.Index(text, "policy-required independent authority adapters are not deployed")
	independentLookup := strings.Index(text, "FROM qualification_promotion_v2_independent_receipts")
	if independentFailure < 0 || (independentLookup >= 0 && independentLookup < independentFailure) {
		t.Fatal("non-empty independent policy must fail before any registry lookup")
	}
	if calls := strings.Count(text, "FROM resolve_qualification_input_precommit_for_promotion_v1("); calls != 1 {
		t.Fatalf("Promotion must resolve and lock the exact input precommit once, found %d calls", calls)
	}
	for _, forbidden := range []string{
		"session_replication_role", "CREATE ROLE", "metadata jsonb", "ON DELETE CASCADE",
		"GRANT SELECT ON TABLE %I.qualification_promotion_v2_",
		"GRANT EXECUTE ON FUNCTION %I.resolve_qualification_promotion_v2_handoff",
		"GRANT EXECUTE ON FUNCTION %I.assert_pending_qualification_promotion_v2_handoff",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Qualification Promotion v2 migration unexpectedly contains %q", forbidden)
		}
	}
	downText := string(down)
	for _, required := range []string{
		"qualification_promotion_v2_down_guard",
		"LOCK TABLE qualification_promotion_v2_consumptions IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE artifact_revisions IN ACCESS EXCLUSIVE MODE",
		"LOCK TABLE artifact_revision_identity_reservations IN ACCESS EXCLUSIVE MODE",
		"cannot roll back Qualification Promotion v2 while immutable promotion history exists",
		"WHERE owner_kind = 'qualification-promotion-v2'",
		"DROP TRIGGER IF EXISTS artifact_revisions_shared_identity_reservation",
		"DROP TABLE IF EXISTS artifact_revision_identity_reservations",
		"GRANT EXECUTE ON FUNCTION %I.assert_current_workflow_input_authority_v1(uuid)",
	} {
		if !strings.Contains(downText, required) {
			t.Fatalf("Qualification Promotion v2 rollback is missing %q", required)
		}
	}
}

func TestQualificationPromotionV2EmptyRollbackPostgresCanary(t *testing.T) {
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
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_promotion_v2_empty_")
	applyQualificationPromotionV2Prefix(t, ctx, database)
	assertQualificationPromotionV2Catalog(t, ctx, database)

	actorID, projectID, artifactID, revisionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,display_name,password_hash)
VALUES($1,$2,'Promotion v2 canary','unused')`, []any{actorID, strings.ToLower(actorID.String()) + "@promotion.test"}},
		{`INSERT INTO projects(id,name,created_by,governance_mode)
VALUES($1,'Promotion v2 canary',$2,'solo')`, []any{projectID, actorID}},
		{`INSERT INTO artifacts(id,project_id,kind,artifact_key,title,created_by)
VALUES($1,$2,'workspace',$3,'Workspace',$4)`, []any{artifactID, projectID, "promotion-" + artifactID.String(), actorID}},
		{`INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,schema_version,content_store,content_ref,
  content_hash,byte_size,workflow_status,change_source,created_by
) VALUES($1,$2,1,1,'mongo',$3,$4,2,'draft','system',$5)`, []any{
			revisionID, artifactID, "revision/" + revisionID.String(),
			"sha256:" + strings.Repeat("1", 64), actorID,
		}},
	} {
		if _, err := database.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("ordinary revision fixture after migration81: %v", err)
		}
	}
	var ownerKind string
	if err := database.QueryRowContext(ctx, `
SELECT owner_kind FROM artifact_revision_identity_reservations WHERE id=$1`, revisionID,
	).Scan(&ownerKind); err != nil || ownerKind != "artifact-revision" {
		t.Fatalf("ordinary revision reservation owner=%q error=%v", ownerKind, err)
	}

	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	promotionID := uuid.New()
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO artifact_revision_identity_reservations(id,owner_kind,owner_operation_id,reserved_at)
VALUES($1,'qualification-promotion-v2',$2,date_trunc('milliseconds',clock_timestamp()))`,
		promotionID, uuid.New()); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("seed deferred promotion reservation collision: %v", err)
	}
	_, collisionErr := transaction.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,schema_version,content_store,content_ref,
  content_hash,byte_size,workflow_status,change_source,created_by
) VALUES($1,$2,2,1,'mongo',$3,$4,2,'draft','system',$5)`,
		promotionID, artifactID, "revision/"+promotionID.String(),
		"sha256:"+strings.Repeat("2", 64), actorID)
	_ = transaction.Rollback()
	if collisionErr == nil || !strings.Contains(collisionErr.Error(), "WPV02") {
		t.Fatalf("ordinary insert over Promotion reservation error=%v, want WPV02", collisionErr)
	}
	poisoned, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lockID, wrongID := uuid.New(), uuid.New()
	if _, err := poisoned.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text,0
  )
)`, lockID); err != nil {
		_ = poisoned.Close()
		t.Fatal(err)
	}
	if err := poisonQualificationPromotionV2Session(
		poisoned,
		errors.New("simulated session-lock acquisition result loss"),
	); err == nil {
		_ = poisoned.Close()
		t.Fatal("unknown session-lock acquisition result did not poison the possibly lock-bearing connection")
	}
	_ = poisoned.Close()
	verifier, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reacquired bool
	if err := verifier.QueryRowContext(ctx, `
SELECT pg_catalog.pg_try_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text,0
  )
)`, lockID).Scan(&reacquired); err != nil || !reacquired {
		_ = verifier.Close()
		t.Fatalf("acquisition-error poison leaked advisory lock reacquired=%t error=%v", reacquired, err)
	}
	if err := unlockQualificationPromotionV2Session(ctx, verifier, lockID); err != nil {
		_ = verifier.Close()
		t.Fatal(err)
	}
	_ = verifier.Close()

	poisoned, err = database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lockID, wrongID = uuid.New(), uuid.New()
	if _, err := poisoned.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text,0
  )
)`, lockID); err != nil {
		_ = poisoned.Close()
		t.Fatal(err)
	}
	if err := unlockQualificationPromotionV2Session(ctx, poisoned, wrongID); err == nil {
		_ = poisoned.Close()
		t.Fatal("false session unlock did not poison the possibly lock-bearing connection")
	}
	_ = poisoned.Close()
	verifier, err = database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close()
	reacquired = false
	if err := verifier.QueryRowContext(ctx, `
SELECT pg_catalog.pg_try_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text,0
  )
)`, lockID).Scan(&reacquired); err != nil || !reacquired {
		t.Fatalf("poisoned session leaked advisory lock reacquired=%t error=%v", reacquired, err)
	}
	if err := unlockQualificationPromotionV2Session(ctx, verifier, lockID); err != nil {
		t.Fatal(err)
	}

	down, err := files.ReadFile("000081_qualification_promotion_v2.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err != nil {
		t.Fatalf("empty Promotion v2 rollback with ordinary backfill: %v", err)
	}
	var remaining int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.tables
WHERE table_schema=current_schema()
  AND (table_name LIKE 'qualification_promotion_v2_%'
       OR table_name='artifact_revision_identity_reservations')`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("Promotion v2 tables remaining after empty rollback=%d", remaining)
	}
}

func TestQualificationPromotionV2NonemptyPolicyFailsBeforeRegistryPostgresCanary(t *testing.T) {
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
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_promotion_v2_nonempty_")
	applyQualificationPromotionV2Prefix(t, ctx, database)
	fixture := seedWorkflowInputCanary(t, ctx, database)
	activateWorkflowInputForPromotionV2(t, ctx, database, fixture)

	command := []uuid.UUID{uuid.New(), fixture.authorityID, uuid.New(), uuid.New(), uuid.New()}
	if _, err := database.ExecContext(ctx, `
SELECT * FROM consume_qualification_promotion_v2($1,$2,$3,$4,$5)`,
		command[0], command[1], command[2], command[3], command[4]); err == nil ||
		!strings.Contains(err.Error(), "WPV01") {
		t.Fatalf("non-serializable consume error=%v, want WPV01", err)
	}
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	_, consumeErr := transaction.ExecContext(ctx, `
SELECT * FROM consume_qualification_promotion_v2($1,$2,$3,$4,$5)`,
		command[0], command[1], command[2], command[3], command[4])
	_ = transaction.Rollback()
	if consumeErr == nil || !strings.Contains(consumeErr.Error(), "WPV03") ||
		!strings.Contains(consumeErr.Error(), "independent authority adapters") {
		t.Fatalf("non-empty independent policy consume error=%v, want pre-registry WPV03", consumeErr)
	}
	var rows int
	if err := database.QueryRowContext(ctx, `
SELECT count(*) FROM qualification_promotion_v2_consumptions`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("non-empty independent policy left %d partial consumptions", rows)
	}
}

func activateWorkflowInputForPromotionV2(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture workflowInputCanary,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freezeWorkflowInputCanary(ctx, transaction, fixture); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("freeze Workflow Input for Promotion v2: %v", err)
	}
	// The reusable migration-78 fixture predates migration-79's stricter full
	// definition shape. Disable only the later profile guard while attaching
	// this already independently exact WIA; this canary proves the migration-80
	// pre-registry failure branch, not the production no-bypass closure.
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE workflow_node_runs
SET status='waiting_qualification',input_authority_id=$2 WHERE id=$1`,
		fixture.gateNodeID, fixture.authorityID); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("attach Workflow Input authority: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO workflow_run_events(
  id,run_id,sequence,event_type,node_key,payload
) VALUES($1,$2,6,'external_qualification_activated','external-qualification',$3)`,
		fixture.eventID, fixture.runID,
		`{"inputAuthorityId":"`+fixture.authorityID.String()+`","nodeRunId":"`+fixture.gateNodeID.String()+`"}`); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("append Workflow Input activation event: %v", err)
	}
	if err := insertWorkflowInputActivationOutbox(ctx, transaction, fixture); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("append Workflow Input activation outbox: %v", err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE workflow_runs
SET status='waiting_qualification',event_cursor=6 WHERE id=$1`, fixture.runID); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("advance Workflow Input run: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit Workflow Input activation: %v", err)
	}
}

func TestQualificationPromotionV2FreshReplayConflictAndRollbackRefusalPostgresCanary(t *testing.T) {
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
	database := qualificationPlanMigrationDatabase(t, ctx, base, dsn, "qualification_promotion_v2_fresh_")
	applyQualificationPromotionV2Prefix(t, ctx, database)

	wia := seedWorkflowInputCanary(t, ctx, database)
	plan := newQualificationPlanMigrationFixture(t, qualificationPlanMigrationFixtureOptions{
		inputAuthorityID: wia.authorityID,
	})
	bindQualificationPlanToWorkflowInput(t, &plan, wia)
	bindWorkflowInputToEmptyPromotionPolicy(t, ctx, database, &wia, &plan)
	activateWorkflowInputForPromotionV2(t, ctx, database, wia)
	if err := freezeQualificationPlanMigrationFixture(ctx, database, plan); err != nil {
		t.Fatalf("freeze Promotion-v2 Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	completion := completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	if completion.receiptID == "" {
		t.Fatal("Promotion-v2 fixture did not produce a terminal Receipt v3")
	}
	missingCommand := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	if _, missingErr := consumeQualificationPromotionV2Raw(ctx, database, missingCommand); !errors.Is(
		missingErr, qualificationpromotionv2.ErrNotReady,
	) {
		t.Fatalf("missing exact input precommit error=%v, want ErrNotReady/WPV03", missingErr)
	}
	inputPrecommit := issueQualificationInputPrecommitForPromotion(t, ctx, database, wia, plan)
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
UPDATE qualification_plan_authorities
SET input_hash='sha256:' || repeat('9',64)
WHERE authority_id=$1`, []any{plan.authorityID}, "WPV02")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
UPDATE qualification_evidence_events
SET event_bytes=convert_to('{}','UTF8')
WHERE event_id=$1`, []any{indexed.eventID}, "WPV02")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
UPDATE qualification_receipt_v3_observations
SET observation_bytes=convert_to('{}','UTF8')
WHERE request_hash=(
  SELECT runner_request_hash FROM qualification_receipt_v3_receipts
  WHERE plan_authority_id=$1
)`, []any{plan.authorityID}, "WPV02")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
DELETE FROM qualification_receipt_v3_receipts
WHERE plan_authority_id=$1`, []any{plan.authorityID}, "WPV03")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
UPDATE qualification_input_precommit_authorities
SET authority_bytes=convert_to('{}','UTF8')
WHERE authority_id=$1`, []any{inputPrecommit.authorityID}, "WPV02")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
UPDATE qualification_input_source_receipt_admissions
SET admission_bytes=convert_to('{}','UTF8')
WHERE request_hash=$1`, []any{inputPrecommit.source.hash}, "WPV02")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
UPDATE qualification_input_credential_receipt_admissions
SET admission_bytes=convert_to('{}','UTF8')
WHERE request_hash=$1`, []any{inputPrecommit.credential.hash}, "WPV02")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
DELETE FROM qualification_input_precommit_executable_binding_generations
WHERE binding_role='source-verification'
  AND authority_id=(
    SELECT source_verifier_authority_id
    FROM qualification_input_precommit_authorities WHERE authority_id=$1
  )`, []any{inputPrecommit.authorityID}, "WPV02")
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
DELETE FROM qualification_input_precommit_executable_binding_generations
WHERE binding_role='credential-resolution'
  AND authority_id=(
    SELECT credential_resolver_authority_id
    FROM qualification_input_precommit_authorities WHERE authority_id=$1
  )`, []any{inputPrecommit.authorityID}, "WPV02")
	assertQualificationPromotionV2MultipleInputPrecommitProbe(t, ctx, database, wia, plan)

	command := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	type consumeResult struct {
		bundle map[string]any
		err    error
	}
	results := make(chan consumeResult, 2)
	for range 2 {
		go func() {
			bundle, consumeErr := consumeQualificationPromotionV2Raw(ctx, database, command)
			results <- consumeResult{bundle: bundle, err: consumeErr}
		}()
	}
	var fresh, concurrentReplay map[string]any
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent exact Promotion-v2 consume: %v", result.err)
		}
		if result.bundle["idempotent"] == false {
			fresh = result.bundle
		} else {
			concurrentReplay = result.bundle
		}
	}
	if fresh == nil || concurrentReplay == nil {
		t.Fatalf("concurrent exact Promotion-v2 results fresh=%v replay=%v", fresh, concurrentReplay)
	}
	if fresh["idempotent"] != false || fresh["operationId"] != command[0].String() ||
		fresh["workflowInputAuthorityId"] != command[1].String() ||
		fresh["planAuthorityId"] != command[2].String() ||
		fresh["receiptId"] != completion.receiptID {
		t.Fatalf("fresh Promotion-v2 bundle projection drifted: %#v", fresh)
	}
	expectedInputPrecommit := qualificationpromotionv2.InputPrecommitProjection{Kind: qualificationinput.PromotionBindingKindV1}
	if err := database.QueryRowContext(ctx, `
SELECT authority_hash,authority_id,
       credential_admission_hash,credential_receipt_hash,credential_request_hash,
       qualification_plan_authority_hash,qualification_plan_authority_id,
       qualification_policy_authority_hash,qualification_policy_authority_id,
       source_admission_hash,source_receipt_hash,source_request_hash,
       workflow_input_authority_hash,workflow_input_authority_id
FROM qualification_input_precommit_authorities WHERE authority_id=$1`, inputPrecommit.authorityID,
	).Scan(
		&expectedInputPrecommit.AuthorityHash, &expectedInputPrecommit.AuthorityID,
		&expectedInputPrecommit.CredentialAdmissionHash, &expectedInputPrecommit.CredentialReceiptHash,
		&expectedInputPrecommit.CredentialRequestHash,
		&expectedInputPrecommit.QualificationPlanAuthorityHash, &expectedInputPrecommit.QualificationPlanAuthorityID,
		&expectedInputPrecommit.QualificationPolicyAuthorityHash, &expectedInputPrecommit.QualificationPolicyAuthorityID,
		&expectedInputPrecommit.SourceAdmissionHash, &expectedInputPrecommit.SourceReceiptHash,
		&expectedInputPrecommit.SourceRequestHash,
		&expectedInputPrecommit.WorkflowInputAuthorityHash, &expectedInputPrecommit.WorkflowInputAuthorityID,
	); err != nil {
		t.Fatalf("resolve expected typed input precommit projection: %v", err)
	}
	if err := qualificationinput.ValidatePromotionBinding(qualificationinput.PromotionBinding{
		AuthorityHash: expectedInputPrecommit.AuthorityHash, AuthorityID: expectedInputPrecommit.AuthorityID,
		CredentialAdmissionHash: expectedInputPrecommit.CredentialAdmissionHash,
		CredentialReceiptHash:   expectedInputPrecommit.CredentialReceiptHash,
		CredentialRequestHash:   expectedInputPrecommit.CredentialRequestHash, Kind: expectedInputPrecommit.Kind,
		QualificationPlanAuthorityHash:   expectedInputPrecommit.QualificationPlanAuthorityHash,
		QualificationPlanAuthorityID:     expectedInputPrecommit.QualificationPlanAuthorityID,
		QualificationPolicyAuthorityHash: expectedInputPrecommit.QualificationPolicyAuthorityHash,
		QualificationPolicyAuthorityID:   expectedInputPrecommit.QualificationPolicyAuthorityID,
		SourceAdmissionHash:              expectedInputPrecommit.SourceAdmissionHash,
		SourceReceiptHash:                expectedInputPrecommit.SourceReceiptHash,
		SourceRequestHash:                expectedInputPrecommit.SourceRequestHash,
		WorkflowInputAuthorityHash:       expectedInputPrecommit.WorkflowInputAuthorityHash,
		WorkflowInputAuthorityID:         expectedInputPrecommit.WorkflowInputAuthorityID,
	}); err != nil {
		t.Fatal(err)
	}
	expectedInputBytes, err := json.Marshal(expectedInputPrecommit)
	if err != nil {
		t.Fatal(err)
	}
	var expectedInput map[string]any
	if err := json.Unmarshal(expectedInputBytes, &expectedInput); err != nil {
		t.Fatal(err)
	}
	closureMaterial := fresh["closure"].(map[string]any)
	closureDocument := closureMaterial["document"].(map[string]any)
	if !reflect.DeepEqual(closureDocument["inputPrecommit"], expectedInput) {
		t.Fatalf("typed input precommit closure=%#v, want %#v", closureDocument["inputPrecommit"], expectedInput)
	}
	var storedInputPrecommitID uuid.UUID
	if err := database.QueryRowContext(ctx, `
SELECT input_precommit_authority_id
FROM qualification_promotion_v2_consumptions WHERE operation_id=$1`, command[0],
	).Scan(&storedInputPrecommitID); err != nil || storedInputPrecommitID != inputPrecommit.authorityID {
		t.Fatalf("stored input precommit authority=%s error=%v", storedInputPrecommitID, err)
	}
	handoff := fresh["handoff"].(map[string]any)
	if handoff["handoffId"] != command[3].String() || handoff["outputRevisionId"] != command[4].String() ||
		handoff["state"] != "pending" {
		t.Fatalf("fresh pending handoff drifted: %#v", handoff)
	}
	assertQualificationPromotionV2CommittedReplayTamper(t, ctx, database, command, `
UPDATE qualification_promotion_v2_consumptions
SET closure_document=jsonb_set(
  closure_document,
  '{inputPrecommit,sourceReceiptHash}',
  to_jsonb('sha256:' || repeat('8',64))
)
WHERE operation_id=$1`, []any{command[0]})
	assertQualificationPromotionV2CommittedReplayTamper(t, ctx, database, command, `
UPDATE qualification_input_source_receipt_admissions
SET admission_bytes=convert_to('{}','UTF8')
WHERE request_hash=$1`, []any{inputPrecommit.source.hash})
	assertQualificationPromotionV2RolloutFence(t, ctx, database, command)
	replayed := consumeQualificationPromotionV2(t, ctx, database, command)
	if replayed["idempotent"] != true || replayed["consumption"].(map[string]any)["hash"] !=
		fresh["consumption"].(map[string]any)["hash"] || replayed["handoff"].(map[string]any)["hash"] !=
		fresh["handoff"].(map[string]any)["hash"] {
		t.Fatalf("exact Promotion-v2 replay did not return immutable bytes: %#v", replayed)
	}
	var inspected []byte
	if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_promotion_v2_operation($1) AS value`, command[0]).Scan(&inspected); err != nil {
		t.Fatalf("inspect Promotion-v2 operation: %v", err)
	}
	if _, err := qualificationpromotionv2.DecodeStoreBundle(inspected); err != nil {
		t.Fatalf("strict Go decoder rejected SQL inspect store bundle: %v", err)
	}
	var inspection map[string]any
	if err := json.Unmarshal(inspected, &inspection); err != nil {
		t.Fatal(err)
	}
	if _, exists := inspection["idempotent"]; exists {
		t.Fatal("inspect-only Promotion-v2 bundle exposed consume-only idempotent")
	}
	readOnlyInspection, err := database.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelSerializable,
		ReadOnly:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var ignoredInspection []byte
	readOnlyErr := readOnlyInspection.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_promotion_v2_operation($1) AS value`, command[0]).Scan(&ignoredInspection)
	_ = readOnlyInspection.Rollback()
	if readOnlyErr == nil || !strings.Contains(readOnlyErr.Error(), "WPV03") {
		t.Fatalf("read-only Promotion-v2 inspection error=%v, want WPV03", readOnlyErr)
	}
	var resolved []byte
	if err := database.QueryRowContext(ctx, `
SELECT value FROM resolve_qualification_promotion_v2_handoff($1) AS value`, command[3]).Scan(&resolved); err != nil {
		t.Fatalf("resolve Promotion-v2 handoff: %v", err)
	}
	if !bytes.Equal(inspected, resolved) {
		t.Fatal("handoff resolver did not return the same full closed store bundle")
	}

	conflictCommand := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	_, conflictErr := transaction.ExecContext(ctx, `
SELECT * FROM consume_qualification_promotion_v2($1,$2,$3,$4,$5)`,
		conflictCommand[0], conflictCommand[1], conflictCommand[2], conflictCommand[3], conflictCommand[4])
	_ = transaction.Rollback()
	if conflictErr == nil || !strings.Contains(conflictErr.Error(), "WPV02") {
		t.Fatalf("changed Promotion-v2 identity reuse error=%v, want WPV02", conflictErr)
	}
	var revisionExists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM artifact_revisions WHERE id=$1)`, command[4]).Scan(&revisionExists); err != nil {
		t.Fatal(err)
	}
	if revisionExists {
		t.Fatal("migration81 created the reserved output revision before handoff consumption")
	}
	var nodeStatus string
	var nodeOutput sql.NullString
	if err := database.QueryRowContext(ctx, `
SELECT status,output_revision_id::text
FROM workflow_node_runs WHERE id=$1`, wia.gateNodeID).Scan(&nodeStatus, &nodeOutput); err != nil {
		t.Fatal(err)
	}
	if nodeStatus != "waiting_qualification" || nodeOutput.Valid {
		t.Fatalf("migration81 advanced workflow node status/output=%q/%v", nodeStatus, nodeOutput)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO artifact_revisions(
  id,artifact_id,revision_number,schema_version,content_store,content_ref,
  content_hash,byte_size,workflow_status,change_source,created_by
)
SELECT $1,revision.artifact_id,revision.revision_number+1,revision.schema_version,
       revision.content_store,revision.content_ref,
       'sha256:' || repeat('7',64),revision.byte_size,'draft','system',revision.created_by
FROM artifact_revisions AS revision WHERE revision.id=$2`,
		command[4], wia.targetRevisionID); err == nil || !strings.Contains(err.Error(), "WPV02") {
		t.Fatalf("ordinary revision over committed Promotion reservation error=%v, want WPV02", err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE qualification_promotion_v2_consumptions
SET subject=subject WHERE operation_id=$1`, command[0]); err == nil || !strings.Contains(err.Error(), "WPV02") {
		t.Fatalf("Promotion-v2 immutable mutation error=%v, want WPV02", err)
	}
	down, err := files.ReadFile("000081_qualification_promotion_v2.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, string(down)); err == nil ||
		!strings.Contains(err.Error(), "immutable promotion history exists") {
		t.Fatalf("nonempty Promotion-v2 rollback error=%v, want immutable-history refusal", err)
	}
}

func assertQualificationPromotionV2CommittedReplayTamper(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	command []uuid.UUID,
	mutation string,
	arguments []any,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, mutation, arguments...); err != nil {
		t.Fatalf("apply committed Promotion-v2 replay tamper: %v", err)
	}
	_, replayErr := transaction.ExecContext(ctx, `
SELECT * FROM consume_qualification_promotion_v2($1,$2,$3,$4,$5)`,
		command[0], command[1], command[2], command[3], command[4])
	if replayErr == nil || !strings.Contains(replayErr.Error(), "WPV02") {
		t.Fatalf("committed Promotion-v2 tamper replay error=%v, want WPV02", replayErr)
	}
}

func assertQualificationPromotionV2MultipleInputPrecommitProbe(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	plan qualificationPlanMigrationFixture,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `
CREATE OR REPLACE FUNCTION resolve_qualification_input_precommit_for_promotion_v1(
  p_workflow_input_authority_id uuid,
  p_qualification_plan_authority_id uuid
)
RETURNS SETOF qualification_input_precommit_authorities
LANGUAGE sql VOLATILE SECURITY DEFINER
AS $probe$
  SELECT authority.*
  FROM qualification_input_precommit_authorities AS authority
  CROSS JOIN pg_catalog.generate_series(1,2) AS duplicate
  WHERE authority.workflow_input_authority_id=$1
    AND authority.qualification_plan_authority_id=$2
$probe$`); err != nil {
		t.Fatalf("install multiple-precommit resolver probe: %v", err)
	}
	command := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	_, consumeErr := transaction.ExecContext(ctx, `
SELECT * FROM consume_qualification_promotion_v2($1,$2,$3,$4,$5)`,
		command[0], command[1], command[2], command[3], command[4])
	if consumeErr == nil || !strings.Contains(consumeErr.Error(), "WPV02") {
		t.Fatalf("multiple exact input precommits error=%v, want WPV02", consumeErr)
	}
}

func assertQualificationPromotionV2TamperProbe(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	plan qualificationPlanMigrationFixture,
	mutation string,
	arguments []any,
	wantState string,
) {
	t.Helper()
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, mutation, arguments...); err != nil {
		t.Fatalf("apply Promotion-v2 tamper probe: %v", err)
	}
	command := []uuid.UUID{uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New()}
	_, consumeErr := transaction.ExecContext(ctx, `
SELECT * FROM consume_qualification_promotion_v2($1,$2,$3,$4,$5)`,
		command[0], command[1], command[2], command[3], command[4])
	if consumeErr == nil || !strings.Contains(consumeErr.Error(), wantState) {
		t.Fatalf("Promotion-v2 tamper probe error=%v, want %s", consumeErr, wantState)
	}
}

func assertQualificationPromotionV2RolloutFence(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	command []uuid.UUID,
) {
	t.Helper()
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_xact_lock(
  pg_catalog.hashtextextended('worksflow:workflow-input-authority-migration:v1',0)
)`); err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text,0
  )
)`, command[0]); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_ = unlockQualificationPromotionV2Session(context.WithoutCancel(ctx), connection, command[0])
		}
	}()
	replay, err := connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Rollback() }()
	var backendPID int
	if err := replay.QueryRowContext(ctx, `SELECT pg_catalog.pg_backend_pid()`).Scan(&backendPID); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	finished := make(chan error, 1)
	go func() {
		finished <- replay.QueryRowContext(ctx, `
SELECT value FROM consume_qualification_promotion_v2($1,$2,$3,$4,$5) AS value`,
			command[0], command[1], command[2], command[3], command[4]).Scan(&encoded)
	}()
	if err := waitForQualificationPlanAdvisoryLock(
		ctx, database, backendPID, "ShareLock", false, finished,
	); err != nil {
		t.Fatalf("wait for Promotion-v2 shared rollout fence: %v", err)
	}
	var relationFree bool
	if err := database.QueryRowContext(ctx, `
SELECT NOT EXISTS (
  SELECT 1
  FROM pg_catalog.pg_locks AS lock
  JOIN pg_catalog.pg_class AS relation ON relation.oid=lock.relation
  WHERE lock.pid=$1 AND lock.granted
    AND relation.relnamespace=current_schema()::regnamespace
    AND relation.relname IN (
      'qualification_promotion_v2_consumptions','projects',
      'workflow_input_authorities','qualification_evidence_events'
    )
)`, backendPID).Scan(&relationFree); err != nil {
		t.Fatal(err)
	}
	if !relationFree {
		t.Fatal("Promotion-v2 consume touched an authority relation before the rollout fence")
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("Promotion-v2 replay after rollout fence release: %v", err)
	}
	if err := replay.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := unlockQualificationPromotionV2Session(ctx, connection, command[0]); err != nil {
		t.Fatal(err)
	}
	locked = false
	var bundle map[string]any
	if err := json.Unmarshal(encoded, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle["idempotent"] != true {
		t.Fatalf("rollout-fenced exact replay bundle=%#v, want idempotent", bundle)
	}
}

func bindWorkflowInputToEmptyPromotionPolicy(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture *workflowInputCanary,
	plan *qualificationPlanMigrationFixture,
) {
	t.Helper()
	compiler := newQualificationPolicyFixtureCompiler(
		t, fixture.projectID, fixture.policyRecord.Document.ExecutionProfile.Hash, nil,
	)
	first := compiler.compile(
		t,
		fixture.policyRecord.Command,
		fixture.policyRecord.Document.Status,
		fixture.policyRecord.IssuedAt,
	)
	if first.AuthorityHash != fixture.policyRecord.AuthorityHash {
		t.Fatal("could not reproduce non-empty policy before issuing reviewed empty successor")
	}
	if plan != nil {
		bindQualificationPolicyProfileToPlan(t, &compiler.source.value.PlanInputProfile, *plan)
	}
	compiler.source.value.PromotionPolicy.IndependentRequirements = []qualificationpolicy.IndependentAuthorityBinding{}
	successor := compiler.compile(t, qualificationpolicy.IssueCommand{
		OperationID:                   uuid.New(),
		AuthorityID:                   uuid.New(),
		PolicySourceID:                first.Command.PolicySourceID,
		ExpectedPreviousAuthorityHash: first.AuthorityHash,
	}, qualificationpolicy.AuthorityStatusActive, first.IssuedAt.Add(time.Minute))
	issueQualificationPolicyRecord(t, ctx, database, successor)
	fixture.policyID = successor.Command.AuthorityID
	fixture.policyRecord = successor
	fixture.candidateRaw = workflowInputCandidateWithPolicy(
		t, fixture.candidateRaw, successor.Command.AuthorityID, successor.AuthorityHash,
	)
}

func bindQualificationPolicyProfileToPlan(
	t *testing.T,
	profile *qualificationpolicy.PlanInputProfile,
	plan qualificationPlanMigrationFixture,
) {
	t.Helper()
	input := qualificationReceiptV3DecodeMap(t, plan.input.document)
	decode := func(value any, target any) {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, target); err != nil {
			t.Fatal(err)
		}
	}
	decode(input["artifactPolicy"], &profile.ArtifactPolicy)
	decode(input["artifacts"], &profile.Artifacts)
	decode(input["goldenRuntime"], &profile.GoldenRuntime)
	decode(input["outputPolicy"], &profile.OutputPolicy)
	decode(input["outputs"], &profile.Outputs)
	decode(input["recipient"], &profile.Recipient)
	decode(input["templateRelease"], &profile.TemplateRelease)
	decode(input["trustBindings"], &profile.TrustBindings)
	profile.TrustPolicyDigest = input["trustPolicyDigest"].(string)

	manifest := input["qualificationManifest"].(map[string]any)
	profile.QualificationManifest.ArtifactID = manifest["artifactId"].(string)
	profile.QualificationManifest.RevisionID = manifest["revisionId"].(string)
	profile.QualificationManifest.ContentHash = manifest["contentHash"].(string)
	profile.QualificationManifest.PlanDigest = plan.projection.hash

	credential := input["credential"].(map[string]any)
	profile.CredentialProfile.Audience = credential["audience"].(string)
	profile.CredentialProfile.AuthorityID = credential["issuer"].(string)
	profile.CredentialProfile.IssuanceArtifactID = credential["issuanceArtifactId"].(string)
	profile.CredentialProfile.RevocationArtifactID = credential["revocationArtifactId"].(string)
}

func bindQualificationPlanToWorkflowInput(
	t *testing.T,
	plan *qualificationPlanMigrationFixture,
	wia workflowInputCanary,
) {
	t.Helper()
	promotionTarget := map[string]any{
		"projectId": wia.projectID.String(), "workflowRunId": wia.runID.String(),
		"nodeKey": "external-qualification",
		"targetRevision": map[string]any{
			"id": wia.targetRevisionID.String(), "contentHash": workflowInputDigest(wia.revisionRaw),
		},
		"subject": "workflow-input-canary", "stageGate": "external-qualification",
	}
	projection := qualificationReceiptV3DecodeMap(t, plan.projection.document)
	projection["subject"] = "workflow-input-canary"
	plan.projection = qualificationPlanMigrationCanonical(t, projection)
	input := qualificationReceiptV3DecodeMap(t, plan.input.document)
	input["promotionTarget"] = promotionTarget
	input["qualificationPlanDigest"] = plan.projection.hash
	input["buildManifest"] = map[string]any{
		"id":          wia.buildManifestID.String(),
		"contentHash": workflowInputDigest(wia.buildManifestRaw),
	}
	input["buildContract"] = map[string]any{
		"id":          wia.buildContractID.String(),
		"contentHash": workflowInputDigest(wia.buildContractRaw),
	}
	plan.input = qualificationPlanMigrationCanonical(t, input)
	plan.evidenceDocument["planDigest"] = plan.projection.hash
	plan.evidencePlan = qualificationPlanMigrationCanonical(t, plan.evidenceDocument)
	target := qualificationReceiptV3DecodeMap(t, plan.target.document)
	target["promotionTarget"] = promotionTarget
	plan.target = qualificationPlanMigrationCanonical(t, target)
	envelope := qualificationReceiptV3DecodeMap(t, plan.envelope.document)
	envelope["inputHash"] = plan.input.hash
	envelope["projectionHash"] = plan.projection.hash
	envelope["manifestPlanDigest"] = plan.projection.hash
	envelope["evidencePlanHash"] = plan.evidencePlan.hash
	envelope["targetHash"] = plan.target.hash
	plan.envelope = qualificationPlanMigrationCanonical(t, envelope)
}

func seedQualificationPromotionV2Operations(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	plan qualificationPlanMigrationFixture,
	reservationEventID uuid.UUID,
) {
	t.Helper()
	operations := qualificationReceiptV3Map(t, plan.evidenceDocument, "operations")
	rows := []struct {
		id       string
		kind     string
		artifact string
	}{
		{operations["reserve"].(string), "reserve", ""},
		{operations["credentialIssue"].(string), "credential-issue", ""},
		{operations["runClosure"].(string), "run-closure", ""},
		{operations["kmsAttestation"].(string), "kms-attestation", ""},
		{operations["credentialRevocation"].(string), "credential-revocation", ""},
		{operations["artifactIndex"].(string), "artifact-index", ""},
		{operations["receiptSign"].(string), "receipt-sign", ""},
		{operations["snapshotSeal"].(string), "snapshot-seal", ""},
	}
	for _, raw := range plan.evidenceDocument["artifacts"].([]any) {
		artifact := raw.(map[string]any)
		if artifact["classification"] == "restricted" {
			rows = append(rows, struct {
				id       string
				kind     string
				artifact string
			}{artifact["encryptionOperationId"].(string), "encryption", artifact["id"].(string)})
		}
	}
	for _, row := range rows {
		operationID, err := uuid.Parse(row.id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `
INSERT INTO qualification_evidence_operations(
  operation_id,orchestration_id,operation_kind,artifact_id,reservation_event_id,reserved_at
) VALUES($1,$2,$3,$4,$5,date_trunc('milliseconds',clock_timestamp()))`,
			operationID, plan.orchestrationID, row.kind, row.artifact, reservationEventID); err != nil {
			t.Fatalf("seed exact Plan-reserved Evidence operation %s: %v", row.kind, err)
		}
	}
}

func completeQualificationPromotionV2Receipt(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	plan qualificationPlanMigrationFixture,
	indexed qualificationReceiptV3Indexed,
) qualificationReceiptV3Completion {
	t.Helper()
	seal := qualificationReceiptV3Request(t, plan, indexed, "snapshot-seal", "sealer", "", "", "", nil, "", nil)
	if created, err := startQualificationReceiptV3(ctx, database, seal, qualificationPlanMigrationMaterial{}, nil, nil); err != nil || !created {
		t.Fatalf("start Promotion-v2 snapshot seal created=%t error=%v", created, err)
	}
	appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, seal, 1, 1, "pending", nil, nil, nil, nil))
	snapshotDigest := qualificationPlanMigrationDigest("promotion-v2-snapshot")
	sealResult := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndexDigest":   indexed.indexDigest,
		"authorityId":           seal.documentValue["operationalAuthorityId"],
		"evidenceClosureDigest": indexed.closureDigest,
		"mode":                  "immutable-filesystem", "operationId": seal.documentValue["operationId"],
		"requestDigest": seal.material.hash,
		"schemaVersion": "worksflow-qualification-pre-receipt-snapshot/v3",
		"sealedAt":      qualificationReceiptV3Time(time.Now()), "snapshotDigest": snapshotDigest,
		"snapshotId": seal.documentValue["snapshotId"], "stage": "committed",
	})
	sealRecord := appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, seal, 2, 1, "committed", &sealResult, nil, nil, nil))

	verification := qualificationReceiptV3Request(t, plan, indexed, "snapshot-verify", "verifier", snapshotDigest, "", "", nil, "", nil)
	if created, err := startQualificationReceiptV3(ctx, database, verification, qualificationPlanMigrationMaterial{}, nil, nil); err != nil || !created {
		t.Fatalf("start Promotion-v2 verification created=%t error=%v", created, err)
	}
	appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, verification, 1, 1, "pending", nil, nil, nil, nil))
	verificationResult := qualificationReceiptV3Canonical(t, map[string]any{
		"artifactIndexDigest":   indexed.indexDigest,
		"authorityId":           verification.documentValue["operationalAuthorityId"],
		"evidenceClosureDigest": indexed.closureDigest, "result": "verified",
		"schemaVersion":  "worksflow-qualification-snapshot-verification/v3",
		"snapshotDigest": snapshotDigest, "snapshotId": verification.documentValue["snapshotId"],
		"verifiedAt": qualificationReceiptV3Time(time.Now()),
	})
	verificationRecord := appendQualificationReceiptV3(t, ctx, database,
		qualificationReceiptV3Observation(t, verification, 2, 1, "committed", &verificationResult, nil, nil, nil))

	payload, pae := qualificationReceiptV3Payload(t, plan, indexed, sealResult, verificationResult)
	runner := qualificationReceiptV3Request(t, plan, indexed, "receipt-sign", "qualification-runner",
		snapshotDigest, "qualification-receipt", "runner-key", payload.bytes, pae.hash, pae.bytes)
	approver := qualificationReceiptV3Request(t, plan, indexed, "receipt-sign", "release-approver",
		snapshotDigest, "qualification-receipt", "approver-key", payload.bytes, pae.hash, pae.bytes)
	if created, err := startQualificationReceiptV3(ctx, database, runner, approver.material, payload.bytes, pae.bytes); err != nil || !created {
		t.Fatalf("start Promotion-v2 dual signing created=%t error=%v", created, err)
	}
	runnerRecord := qualificationReceiptV3CommitSigner(t, ctx, database, runner, bytes.Repeat([]byte{0x31}, 64))
	approverRecord := qualificationReceiptV3CommitSigner(t, ctx, database, approver, bytes.Repeat([]byte{0x52}, 64))
	envelope := qualificationReceiptV3Envelope(t, payload.bytes, runner, runnerRecord, approver, approverRecord)
	return completeQualificationReceiptV3(t, ctx, database, plan,
		seal, sealRecord, verification, verificationRecord,
		runner, runnerRecord, approver, approverRecord, payload, pae, envelope)
}

func consumeQualificationPromotionV2(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	command []uuid.UUID,
) map[string]any {
	t.Helper()
	value, err := consumeQualificationPromotionV2Raw(ctx, database, command)
	if err != nil {
		t.Fatalf("consume Qualification Promotion v2: %v", err)
	}
	return value
}

func consumeQualificationPromotionV2Raw(
	ctx context.Context,
	database *sql.DB,
	command []uuid.UUID,
) (map[string]any, error) {
	if len(command) != 5 {
		return nil, errors.New("Promotion-v2 test command must contain five identities")
	}
	store, err := qualificationpromotionv2.NewPostgresStore(database, qualificationpromotionv2.PostgresStoreConfig{
		SessionAffinityMode: qualificationpromotionv2.PostgresSessionAffinityDirect,
	})
	if err != nil {
		return nil, err
	}
	record, err := store.Consume(ctx, qualificationpromotionv2.ConsumeCommand{
		OperationID: command[0], WorkflowInputAuthorityID: command[1],
		PlanAuthorityID: command[2], HandoffID: command[3], OutputRevisionID: command[4],
	})
	if err != nil {
		return nil, err
	}
	bundle, err := qualificationpromotionv2.ConsumeStoreBundleFromRecord(record)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func acquireQualificationPromotionV2Session(
	ctx context.Context,
	connection *sql.Conn,
	operationID uuid.UUID,
) error {
	if _, err := connection.ExecContext(ctx, `
SELECT pg_catalog.pg_advisory_lock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text, 0
  )
)`, operationID); err != nil {
		// The backend can acquire a session lock even when cancellation, a driver
		// error, or a lost result prevents the client from observing success.  A
		// connection with an unknown acquisition outcome must never return to the
		// pool.
		return poisonQualificationPromotionV2Session(connection, err)
	}
	return nil
}

func unlockQualificationPromotionV2Session(
	parent context.Context,
	connection *sql.Conn,
	operationID uuid.UUID,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	var unlocked bool
	err := connection.QueryRowContext(cleanup, `
SELECT pg_catalog.pg_advisory_unlock(
  pg_catalog.hashtextextended(
    'worksflow:qualification-promotion-v2:operation:' || $1::text, 0
  )
)`, operationID).Scan(&unlocked)
	if err == nil && unlocked {
		return nil
	}
	if err != nil {
		return poisonQualificationPromotionV2Session(connection, err)
	}
	return poisonQualificationPromotionV2Session(
		connection,
		errors.New("Promotion-v2 session advisory unlock returned false"),
	)
}

func poisonQualificationPromotionV2Session(connection *sql.Conn, cause error) error {
	// Never return a possibly lock-bearing physical session to database/sql.
	// driver.ErrBadConn forces the pool to discard it; backend close releases
	// every session advisory lock when acquisition or unlock outcome is unknown.
	poisonErr := connection.Raw(func(any) error { return driver.ErrBadConn })
	return errors.Join(cause, poisonErr)
}

func applyQualificationPromotionV2Prefix(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `CREATE TABLE schema_migrations (
  version text PRIMARY KEY, checksum text NOT NULL, down_checksum text,
  applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name > qualificationPromotionV2Migration {
			break
		}
		if err := applyFile(ctx, connection, name); err != nil {
			t.Fatalf("apply prerequisite %s: %v", name, err)
		}
	}
}

func assertQualificationPromotionV2Catalog(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var tables, triggers, APIs int
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM pg_catalog.pg_class
   WHERE relnamespace=current_schema()::regnamespace AND relkind='r'
     AND relname IN (
       'artifact_revision_identity_reservations',
       'qualification_promotion_v2_independent_receipts',
       'qualification_promotion_v2_consumptions',
       'qualification_promotion_v2_consumption_independent_receipts',
       'qualification_promotion_v2_handoffs',
       'qualification_promotion_v2_identity_reservations'
     )),
  (SELECT count(*) FROM pg_catalog.pg_trigger AS trigger
   JOIN pg_catalog.pg_class AS relation ON relation.oid=trigger.tgrelid
   WHERE relation.relnamespace=current_schema()::regnamespace
     AND NOT trigger.tgisinternal
     AND trigger.tgname IN (
       'artifact_revisions_shared_identity_reservation',
       'artifact_revision_identity_reservations_immutable',
       'qualification_promotion_v2_independent_receipts_immutable',
       'qualification_promotion_v2_consumptions_immutable',
       'qualification_promotion_v2_consumption_independent_receipts_immutable',
       'qualification_promotion_v2_handoffs_immutable',
       'qualification_promotion_v2_identity_reservations_immutable'
     )),
  (SELECT count(*) FROM pg_catalog.pg_proc
   WHERE pronamespace=current_schema()::regnamespace
     AND proname IN (
       'consume_qualification_promotion_v2',
       'inspect_qualification_promotion_v2_operation',
       'resolve_qualification_promotion_v2_handoff',
       'assert_pending_qualification_promotion_v2_handoff',
       'inspect_historical_qualification_promotion_v1_operation'
     ))`,
	).Scan(&tables, &triggers, &APIs); err != nil {
		t.Fatal(err)
	}
	if tables != 6 || triggers != 7 || APIs != 5 {
		t.Fatalf("Promotion v2 catalog tables/triggers/APIs=%d/%d/%d, want 6/7/5", tables, triggers, APIs)
	}
	var publicTables, publicAPIs bool
	if err := database.QueryRowContext(ctx, `
SELECT
  EXISTS (
    SELECT 1 FROM pg_catalog.pg_class
    WHERE relnamespace=current_schema()::regnamespace
      AND relname LIKE 'qualification_promotion_v2_%'
      AND has_table_privilege('public',oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE')
  ),
  EXISTS (
    SELECT 1 FROM pg_catalog.pg_proc
    WHERE pronamespace=current_schema()::regnamespace
      AND proname IN ('consume_qualification_promotion_v2',
        'inspect_qualification_promotion_v2_operation',
        'resolve_qualification_promotion_v2_handoff',
        'assert_pending_qualification_promotion_v2_handoff')
      AND has_function_privilege('public',oid,'EXECUTE')
  )`,
	).Scan(&publicTables, &publicAPIs); err != nil {
		t.Fatal(err)
	}
	if publicTables || publicAPIs {
		t.Fatalf("Promotion v2 PUBLIC posture table/API=%t/%t, want false/false", publicTables, publicAPIs)
	}
	var exactAPIContract bool
	if err := database.QueryRowContext(ctx, `
SELECT count(*)=5
   AND bool_and(prosecdef)
   AND bool_and(NOT proisstrict)
   AND bool_and(proparallel='u')
   AND bool_and(proretset)
   AND bool_and(pg_catalog.pg_get_function_result(oid)='SETOF jsonb')
   AND bool_and(proconfig = ARRAY[
     'search_path=pg_catalog, ' || current_schema() || ', pg_temp'
   ]::text[])
   AND bool_and(
     (proname='consume_qualification_promotion_v2' AND provolatile='v')
     OR (proname='inspect_qualification_promotion_v2_operation' AND provolatile='s')
     OR (proname='resolve_qualification_promotion_v2_handoff' AND provolatile='s')
     OR (proname='assert_pending_qualification_promotion_v2_handoff' AND provolatile='v')
     OR (proname='inspect_historical_qualification_promotion_v1_operation' AND provolatile='s')
   )
FROM pg_catalog.pg_proc
WHERE pronamespace=current_schema()::regnamespace
  AND proname IN (
    'consume_qualification_promotion_v2',
    'inspect_qualification_promotion_v2_operation',
    'resolve_qualification_promotion_v2_handoff',
    'assert_pending_qualification_promotion_v2_handoff',
    'inspect_historical_qualification_promotion_v1_operation'
  )`).Scan(&exactAPIContract); err != nil {
		t.Fatal(err)
	}
	if !exactAPIContract {
		t.Fatal("Promotion v2 supported API signature/definer/volatility/parallel/search-path contract drifted")
	}
	var migrationOwnerExists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='worksflow_migration_owner')`,
	).Scan(&migrationOwnerExists); err != nil {
		t.Fatal(err)
	}
	if migrationOwnerExists {
		var exactOwners bool
		if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*)=6 AND bool_and(owner.rolname='worksflow_migration_owner')
   FROM pg_catalog.pg_class AS relation
   JOIN pg_catalog.pg_roles AS owner ON owner.oid=relation.relowner
   WHERE relation.relnamespace=current_schema()::regnamespace
     AND relation.relname IN (
       'artifact_revision_identity_reservations',
       'qualification_promotion_v2_independent_receipts',
       'qualification_promotion_v2_consumptions',
       'qualification_promotion_v2_consumption_independent_receipts',
       'qualification_promotion_v2_handoffs',
       'qualification_promotion_v2_identity_reservations'
     )),
  (SELECT bool_and(owner.rolname='worksflow_migration_owner')
   FROM pg_catalog.pg_proc AS routine
   JOIN pg_catalog.pg_roles AS owner ON owner.oid=routine.proowner
   WHERE routine.pronamespace=current_schema()::regnamespace
     AND routine.proname IN (
       'consume_qualification_promotion_v2',
       'inspect_qualification_promotion_v2_operation',
       'resolve_qualification_promotion_v2_handoff',
       'assert_pending_qualification_promotion_v2_handoff',
       'inspect_historical_qualification_promotion_v1_operation',
       'reserve_ordinary_artifact_revision_identity_v1'
     ))`,
		).Scan(&exactOwners, &exactAPIContract); err != nil {
			t.Fatal(err)
		}
		if !exactOwners || !exactAPIContract {
			t.Fatalf("Promotion v2 migration-owner posture tables/functions=%t/%t", exactOwners, exactAPIContract)
		}
	}
	var operatorExists bool
	if err := database.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM pg_catalog.pg_roles
  WHERE rolname='worksflow_qualification_promotion_operator'
)`).Scan(&operatorExists); err != nil {
		t.Fatal(err)
	}
	if operatorExists {
		var consume, inspect, history, handoffResolve, handoffAssert, oldConsume, wiaAssert, policyAssert bool
		if err := database.QueryRowContext(ctx, `
SELECT
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.consume_qualification_promotion_v2(uuid,uuid,uuid,uuid,uuid)',current_schema()),'EXECUTE'),
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.inspect_qualification_promotion_v2_operation(uuid)',current_schema()),'EXECUTE'),
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.inspect_historical_qualification_promotion_v1_operation(uuid)',current_schema()),'EXECUTE'),
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.resolve_qualification_promotion_v2_handoff(uuid)',current_schema()),'EXECUTE'),
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.assert_pending_qualification_promotion_v2_handoff(uuid)',current_schema()),'EXECUTE'),
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.consume_verified_qualification_promotion(uuid,text,bytea,jsonb,text,text,bytea,jsonb,uuid,uuid,text,bytea,jsonb)',current_schema()),'EXECUTE'),
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.assert_current_workflow_input_authority_v1(uuid)',current_schema()),'EXECUTE'),
  has_function_privilege('worksflow_qualification_promotion_operator',
    format('%I.assert_current_qualification_policy_authority_v1(uuid)',current_schema()),'EXECUTE')`,
		).Scan(&consume, &inspect, &history, &handoffResolve, &handoffAssert, &oldConsume, &wiaAssert, &policyAssert); err != nil {
			t.Fatal(err)
		}
		if !consume || !inspect || !history || handoffResolve || handoffAssert || oldConsume || wiaAssert || policyAssert {
			t.Fatalf("Promotion operator posture new=%t/%t/%t handoff=%t/%t old=%t assertions=%t/%t",
				consume, inspect, history, handoffResolve, handoffAssert, oldConsume, wiaAssert, policyAssert)
		}
	}
}
