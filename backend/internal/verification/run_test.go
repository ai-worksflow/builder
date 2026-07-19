package verification

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPrepareCreateRunInputBindsSemanticRequest(t *testing.T) {
	input := CreateRunInput{
		ID: uuid.NewString(), ProjectID: uuid.NewString(),
		Plan:       PlanReference{ID: uuid.NewString(), ContentHash: hashFixture("run-plan")},
		RequestKey: " verify-candidate-1 ", Reason: " verify exact checkpoint ",
		CreatedBy: uuid.NewString(),
	}
	prepared, err := PrepareCreateRunInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.RequestKey != "verify-candidate-1" || prepared.Reason != "verify exact checkpoint" ||
		!exactSHA256(prepared.RequestHash) {
		t.Fatalf("prepared Run input = %#v", prepared)
	}
	if _, err := normalizeCreateRunInput(prepared, true); err != nil {
		t.Fatalf("prepared Run request was not self-verifying: %v", err)
	}

	replay := input
	replay.ID, replay.RequestKey = uuid.NewString(), "verify-candidate-retry-key"
	replayed, err := PrepareCreateRunInput(replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.RequestHash != prepared.RequestHash {
		t.Fatalf("resource or idempotency identity changed semantic request hash: %s != %s", replayed.RequestHash, prepared.RequestHash)
	}
	changed := input
	changed.Reason = "verify a different checkpoint purpose"
	changedPrepared, err := PrepareCreateRunInput(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedPrepared.RequestHash == prepared.RequestHash {
		t.Fatal("different Run reason retained the same request hash")
	}
	prepared.RequestHash = hashFixture("forged-request")
	if _, err := normalizeCreateRunInput(prepared, true); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("forged request hash = %v, want ErrInvalidRun", err)
	}
}

func TestDeriveRunActionsNeverOpensFreezeBeforeExactPassedReceipt(t *testing.T) {
	base := Run{State: RunQueued, Plan: PlanReference{ID: uuid.NewString(), ContentHash: hashFixture("actions-plan")}}
	actions, reasons := deriveRunActions(base, nil, true)
	if len(actions) != 1 || actions[0] != RunActionCancel || len(reasons) != 1 ||
		reasons[0].Code != RunBlockingInProgress {
		t.Fatalf("queued actions/reasons = %#v / %#v", actions, reasons)
	}

	receipt, err := NewCandidateReceipt(validCandidateReceiptInput())
	if err != nil {
		t.Fatal(err)
	}
	base.State = RunPassed
	actions, reasons = deriveRunActions(base, &receipt, true)
	if len(actions) != 2 || actions[0] != RunActionViewReceipt || actions[1] != RunActionFreeze || len(reasons) != 0 {
		t.Fatalf("fresh passed actions/reasons = %#v / %#v", actions, reasons)
	}
	actions, reasons = deriveRunActions(base, &receipt, false)
	if len(actions) != 1 || actions[0] != RunActionViewReceipt || len(reasons) != 1 ||
		reasons[0].Code != RunBlockingCandidateChanged || reasons[0].SourceRef == nil {
		t.Fatalf("stale passed actions/reasons = %#v / %#v", actions, reasons)
	}

	base.State = RunFailed
	actions, reasons = deriveRunActions(base, nil, true)
	if len(actions) != 1 || actions[0] != RunActionRetry || len(reasons) != 1 || reasons[0].Code != RunBlockingFailed {
		t.Fatalf("failed actions/reasons = %#v / %#v", actions, reasons)
	}
}

func TestRunViewSerializesStableEmptyCollectionsAndExplicitNulls(t *testing.T) {
	compiled, err := (PlanCompiler{}).Compile(validCandidatePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	run := Run{
		SchemaVersion: RunSchemaVersion, ID: uuid.NewString(), ProjectID: compiled.Content.ProjectID,
		Plan: PlanReference{ID: uuid.NewString(), ContentHash: compiled.PlanHash}, State: RunQueued,
	}
	view := buildRunView(run, Plan{ID: run.Plan.ID, Content: compiled.Content, PlanHash: compiled.PlanHash}, nil, 0, nil, true)
	payload, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, expected := range []string{
		`"allowedActions":["cancel"]`, `"blockingReasons":[{`,
		`"sourceRef":null`, `"receipt":null`, `"receiptDecision":null`, `"latestAttempt":null`,
	} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("RunView JSON %s is missing %s", encoded, expected)
		}
	}
}
