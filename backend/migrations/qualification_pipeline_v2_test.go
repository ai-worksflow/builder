package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	qualificationpromotionv2 "github.com/worksflow/builder/backend/internal/qualificationpromotionv2"
)

// TestQualificationPipelineV2InputPromotionHandoffPostgresCanary is the
// executable cross-migration proof for the currently closed part of the
// qualification pipeline. ActionPublish and Release Controller delivery are a
// later authority boundary and deliberately are not simulated by this canary.
func TestQualificationPipelineV2InputPromotionHandoffPostgresCanary(t *testing.T) {
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
	database := qualificationPlanMigrationDatabase(
		t, ctx, base, dsn, "qualification_pipeline_v2_",
	)
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
		t.Fatalf("freeze pipeline Plan Authority: %v", err)
	}
	indexed := seedQualificationReceiptV3IndexedEvidence(t, ctx, database, plan)
	seedQualificationPromotionV2Operations(t, ctx, database, plan, indexed.eventID)
	receipt := completeQualificationPromotionV2Receipt(t, ctx, database, plan, indexed)
	if receipt.receiptID == "" {
		t.Fatal("pipeline fixture did not produce an exact terminal Receipt v3")
	}

	missingCommand := []uuid.UUID{
		uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New(),
	}
	if _, missingErr := consumeQualificationPromotionV2Raw(
		ctx, database, missingCommand,
	); !errors.Is(missingErr, qualificationpromotionv2.ErrNotReady) {
		t.Fatalf("Promotion without Input Precommit error=%v, want ErrNotReady", missingErr)
	}

	inputPrecommit := issueQualificationInputPrecommitForPromotion(
		t, ctx, database, wia, plan,
	)
	assertQualificationPromotionV2TamperProbe(t, ctx, database, wia, plan, `
UPDATE qualification_input_precommit_authorities
SET authority_bytes=convert_to('{}','UTF8')
WHERE authority_id=$1`, []any{inputPrecommit.authorityID}, "WPV02")
	assertQualificationPipelineV2InputInspection(
		t, ctx, database, inputPrecommit,
	)

	command := []uuid.UUID{
		uuid.New(), wia.authorityID, plan.authorityID, uuid.New(), uuid.New(),
	}
	promotionFresh, promotionReplay := consumeQualificationPipelineV2Concurrently(
		t, ctx, database, command,
	)
	assertQualificationPipelineV2SameBundle(
		t, promotionFresh, promotionReplay, "concurrent Promotion",
	)
	if promotionFresh["operationId"] != command[0].String() ||
		promotionFresh["workflowInputAuthorityId"] != wia.authorityID.String() ||
		promotionFresh["planAuthorityId"] != plan.authorityID.String() ||
		promotionFresh["receiptId"] != receipt.receiptID {
		t.Fatalf("Promotion did not retain the exact pipeline lineage: %#v", promotionFresh)
	}
	assertQualificationPipelineV2ChangedPromotionIdentitiesBlocked(
		t, ctx, database, command,
	)
	assertQualificationPipelineV2PromotionInspection(t, ctx, database, command)

	seedQualificationHandoffParentLineage(t, ctx, database, wia)
	handoffFresh, handoffReplay := completeQualificationPipelineV2Concurrently(
		t, ctx, database, command[3],
	)
	assertQualificationPipelineV2SameBundle(
		t, handoffFresh, handoffReplay, "concurrent Handoff",
	)
	outputRevision, ok := handoffFresh["outputRevision"].(map[string]any)
	if handoffFresh["handoffId"] != command[3].String() ||
		!ok || outputRevision["id"] != command[4].String() {
		t.Fatalf("Handoff did not retain the reserved identities: %#v", handoffFresh)
	}

	assertQualificationPipelineV2ClosedFacts(
		t, ctx, database, wia, publishNodeID, plan, receipt.receiptID,
		inputPrecommit.authorityID, command,
	)
	assertQualificationPipelineV2StageReplays(
		t, ctx, database, inputPrecommit, command,
	)
	assertQualificationPipelineV2ClosedFacts(
		t, ctx, database, wia, publishNodeID, plan, receipt.receiptID,
		inputPrecommit.authorityID, command,
	)
	assertQualificationPipelineV2RollbackRefusals(t, ctx, database)
}

func consumeQualificationPipelineV2Concurrently(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	command []uuid.UUID,
) (map[string]any, map[string]any) {
	t.Helper()
	type result struct {
		bundle map[string]any
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			bundle, err := consumeQualificationPromotionV2Raw(ctx, database, command)
			results <- result{bundle: bundle, err: err}
		}()
	}
	var fresh, replay map[string]any
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Promotion pipeline consume: %v", result.err)
		}
		switch result.bundle["idempotent"] {
		case false:
			fresh = result.bundle
		case true:
			replay = result.bundle
		default:
			t.Fatalf("Promotion result has no closed idempotency outcome: %#v", result.bundle)
		}
	}
	if fresh == nil || replay == nil {
		t.Fatalf("Promotion did not converge to one fresh and one replay: fresh=%#v replay=%#v", fresh, replay)
	}
	return fresh, replay
}

func completeQualificationPipelineV2Concurrently(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	handoffID uuid.UUID,
) (map[string]any, map[string]any) {
	t.Helper()
	type result struct {
		bundle map[string]any
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			bundle, err := completeQualificationHandoffV1Raw(ctx, database, handoffID)
			results <- result{bundle: bundle, err: err}
		}()
	}
	var fresh, replay map[string]any
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Handoff pipeline completion: %v", result.err)
		}
		switch result.bundle["idempotent"] {
		case false:
			fresh = result.bundle
		case true:
			replay = result.bundle
		default:
			t.Fatalf("Handoff result has no closed idempotency outcome: %#v", result.bundle)
		}
	}
	if fresh == nil || replay == nil {
		t.Fatalf("Handoff did not converge to one fresh and one replay: fresh=%#v replay=%#v", fresh, replay)
	}
	return fresh, replay
}

func assertQualificationPipelineV2SameBundle(
	t *testing.T,
	fresh map[string]any,
	replay map[string]any,
	stage string,
) {
	t.Helper()
	delete(fresh, "idempotent")
	delete(replay, "idempotent")
	freshBytes, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	replayBytes, err := json.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(freshBytes, replayBytes) {
		t.Fatalf("%s replay differs from the immutable fresh outcome", stage)
	}
}

func assertQualificationPipelineV2InputInspection(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	fixture qualificationInputPrecommitFixture,
) {
	t.Helper()
	var firstHash, secondHash string
	for index, target := range []*string{&firstHash, &secondHash} {
		if err := database.QueryRowContext(ctx, `
SELECT authority_hash
FROM inspect_qualification_input_precommit_operation_v1($1)`,
			fixture.operationID,
		).Scan(target); err != nil {
			t.Fatalf("inspect Input Precommit outcome %d: %v", index+1, err)
		}
	}
	if firstHash != fixture.authority.hash || secondHash != firstHash {
		t.Fatalf("Input Precommit inspection drifted: first=%q second=%q want=%q",
			firstHash, secondHash, fixture.authority.hash)
	}
}

func assertQualificationPipelineV2PromotionInspection(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	command []uuid.UUID,
) {
	t.Helper()
	var first, second []byte
	for index, target := range []*[]byte{&first, &second} {
		if err := database.QueryRowContext(ctx, `
SELECT value FROM inspect_qualification_promotion_v2_operation($1) AS value`,
			command[0],
		).Scan(target); err != nil {
			t.Fatalf("inspect Promotion outcome %d: %v", index+1, err)
		}
	}
	if !bytes.Equal(first, second) {
		t.Fatal("Promotion inspection returned different immutable bytes")
	}
	if _, err := qualificationpromotionv2.DecodeStoreBundle(first); err != nil {
		t.Fatalf("decode inspected Promotion outcome: %v", err)
	}
}

func assertQualificationPipelineV2ChangedPromotionIdentitiesBlocked(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	command []uuid.UUID,
) {
	t.Helper()
	for name, index := range map[string]int{
		"operation": 0,
		"handoff":   3,
		"output":    4,
	} {
		changed := append([]uuid.UUID(nil), command...)
		changed[index] = uuid.New()
		if _, err := consumeQualificationPromotionV2Raw(
			ctx, database, changed,
		); !errors.Is(err, qualificationpromotionv2.ErrConflict) {
			t.Fatalf("changed %s identity error=%v, want ErrConflict", name, err)
		}
	}
}

func assertQualificationPipelineV2StageReplays(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	input qualificationInputPrecommitFixture,
	command []uuid.UUID,
) {
	t.Helper()
	assertQualificationPipelineV2InputInspection(t, ctx, database, input)
	assertQualificationPipelineV2PromotionInspection(t, ctx, database, command)
	promotion := consumeQualificationPromotionV2(t, ctx, database, command)
	if promotion["idempotent"] != true {
		t.Fatalf("post-Handoff Promotion replay was not idempotent: %#v", promotion)
	}

	var first, second []byte
	for index, target := range []*[]byte{&first, &second} {
		if err := database.QueryRowContext(ctx, `
SELECT value
FROM inspect_qualification_promotion_v2_handoff_completion($1) AS value`,
			command[3],
		).Scan(target); err != nil {
			t.Fatalf("inspect Handoff outcome %d: %v", index+1, err)
		}
	}
	if !bytes.Equal(first, second) {
		t.Fatal("Handoff inspection returned different immutable bytes")
	}
	handoff := completeQualificationHandoffV1(t, ctx, database, command[3])
	if handoff["idempotent"] != true {
		t.Fatalf("completed Handoff replay was not idempotent: %#v", handoff)
	}
}

func assertQualificationPipelineV2ClosedFacts(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	wia workflowInputCanary,
	publishNodeID uuid.UUID,
	plan qualificationPlanMigrationFixture,
	receiptID string,
	inputPrecommitID uuid.UUID,
	command []uuid.UUID,
) {
	t.Helper()
	var precommits, consumptions, handoffs, pendingDispatches int
	var completions, outputRevisions, exactOutput, exactEvents, exactOutbox int
	var exactCompletion bool
	var gateStatus, publishStatus, runStatus string
	if err := database.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM qualification_input_precommit_authorities
   WHERE authority_id=$1 AND workflow_input_authority_id=$2
     AND qualification_policy_authority_id=$3
     AND qualification_plan_authority_id=$4),
  (SELECT count(*) FROM qualification_promotion_v2_consumptions
   WHERE operation_id=$5 AND workflow_input_authority_id=$2
     AND plan_authority_id=$4 AND input_precommit_authority_id=$1
     AND receipt_id=$6),
  (SELECT count(*) FROM qualification_promotion_v2_handoffs
   WHERE handoff_id=$7 AND operation_id=$5 AND output_revision_id=$8
     AND state='pending'),
  (SELECT count(*) FROM outbox_events
   WHERE aggregate_type='qualification_promotion_v2_handoff'
     AND aggregate_id=$7::text
     AND event_type='qualification.promotion_handoff.pending'
     AND subject='worksflow.qualification.promotion-handoff.pending'
     AND payload=jsonb_build_object('handoffId',$7::text)),
  (SELECT count(*) FROM qualification_promotion_v2_handoff_completions
   WHERE handoff_id=$7 AND operation_id=$5 AND output_revision_id=$8),
  (SELECT count(*) FROM artifact_revisions
   WHERE promotion_handoff_id=$7),
  (SELECT count(*) FROM artifact_revisions AS output
   JOIN artifact_revisions AS parent ON parent.id=output.parent_revision_id
   WHERE output.id=$8 AND output.parent_revision_id=$9
     AND output.promotion_handoff_id=$7
     AND output.workflow_status='approved'
     AND output.content_hash=parent.content_hash),
  qualification_handoff_v1_completion_is_exact($7),
  (SELECT count(*)
   FROM qualification_promotion_v2_handoff_completions AS completion
   JOIN workflow_run_events AS gate_event
     ON gate_event.id=completion.gate_completed_event_id
   JOIN workflow_run_events AS publish_event
     ON publish_event.id=completion.publish_authorization_event_id
   WHERE completion.handoff_id=$7
     AND gate_event.run_id=$10
     AND gate_event.sequence=completion.event_cursor_before+1
     AND gate_event.event_type='node.completed'
     AND gate_event.node_key=(SELECT node_key FROM workflow_node_runs WHERE id=$11)
     AND gate_event.payload=jsonb_build_object(
       'handoffId',$7::text,'outputRevisionId',$8::text
     )
     AND publish_event.run_id=$10
     AND publish_event.sequence=completion.event_cursor_after
     AND publish_event.event_type='node.execution_authorization_required'
     AND publish_event.node_key=(SELECT node_key FROM workflow_node_runs WHERE id=$12)
     AND publish_event.payload='{}'::jsonb),
  (SELECT count(*)
   FROM qualification_promotion_v2_handoff_completions AS completion
   JOIN outbox_events AS gate_outbox
     ON gate_outbox.id=completion.gate_completed_event_id
   JOIN outbox_events AS publish_outbox
     ON publish_outbox.id=completion.publish_authorization_event_id
   WHERE completion.handoff_id=$7
     AND gate_outbox.aggregate_type='workflow_run'
     AND gate_outbox.aggregate_id=$10::text
     AND gate_outbox.event_type='node.completed'
     AND gate_outbox.subject='worksflow.workflow.run.event'
     AND gate_outbox.payload#>>'{payload,handoffId}'=$7::text
     AND gate_outbox.payload#>>'{payload,outputRevisionId}'=$8::text
     AND publish_outbox.aggregate_type='workflow_run'
     AND publish_outbox.aggregate_id=$10::text
     AND publish_outbox.event_type='node.execution_authorization_required'
     AND publish_outbox.subject='worksflow.workflow.run.event'
     AND publish_outbox.payload->'payload'='{}'::jsonb),
  (SELECT status FROM workflow_node_runs WHERE id=$11),
  (SELECT status FROM workflow_node_runs WHERE id=$12),
  (SELECT status FROM workflow_runs WHERE id=$10)
`, inputPrecommitID, wia.authorityID, wia.policyID, plan.authorityID,
		command[0], receiptID, command[3], command[4], wia.targetRevisionID,
		wia.runID, wia.gateNodeID, publishNodeID,
	).Scan(
		&precommits, &consumptions, &handoffs, &pendingDispatches,
		&completions, &outputRevisions, &exactOutput, &exactCompletion,
		&exactEvents, &exactOutbox,
		&gateStatus, &publishStatus, &runStatus,
	); err != nil {
		t.Fatalf("inspect closed Qualification pipeline: %v", err)
	}
	if precommits != 1 || consumptions != 1 || handoffs != 1 ||
		pendingDispatches != 1 || completions != 1 || outputRevisions != 1 ||
		exactOutput != 1 ||
		!exactCompletion || exactEvents != 1 || exactOutbox != 1 ||
		gateStatus != "completed" || publishStatus != "waiting_input" ||
		runStatus != "waiting_input" {
		t.Fatalf("pipeline precommit/consumption/handoff/pending/completion/output/exactOutput=%d/%d/%d/%d/%d/%d/%d exact=%t events/outbox=%d/%d gate/publish/run=%q/%q/%q",
			precommits, consumptions, handoffs, pendingDispatches, completions,
			outputRevisions, exactOutput, exactCompletion, exactEvents, exactOutbox,
			gateStatus, publishStatus, runStatus)
	}
}

func assertQualificationPipelineV2RollbackRefusals(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	for _, rollback := range []struct {
		file string
		want string
	}{
		{"000082_qualification_handoff_v1.down.sql", "durable output exists"},
		{"000081_qualification_promotion_v2.down.sql", "immutable promotion history exists"},
		{"000080_qualification_input_precommit_authority.down.sql", "Promotion v2 or its handoff successor is installed"},
	} {
		down, err := files.ReadFile(rollback.file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, string(down)); err == nil ||
			!strings.Contains(err.Error(), rollback.want) {
			t.Fatalf("%s rollback error=%v, want refusal containing %q",
				rollback.file, err, rollback.want)
		}
	}
}
