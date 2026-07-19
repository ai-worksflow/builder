package verification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

func TestCandidateReceiptBindsExactPassingCoverage(t *testing.T) {
	input := validCandidateReceiptInput()
	receipt, err := NewCandidateReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != DecisionPassed || receipt.BlockerCount != 0 || receipt.MustCount != 1 ||
		receipt.MustPassedCount != 1 || len(receipt.ObligationCoverage) != 2 ||
		receipt.ObligationCoverage[0].Status != "passed" || !exactSHA256(receipt.PayloadHash) {
		t.Fatalf("passing receipt lost derived facts: %#v", receipt)
	}
	parsed, err := ParseReceipt(receipt)
	if err != nil || parsed.PayloadHash != receipt.PayloadHash {
		t.Fatalf("parse exact receipt: parsed=%#v err=%v", parsed, err)
	}
	reference, err := receipt.PassedReference()
	if err != nil || reference.ID != receipt.ID || reference.ContentHash != receipt.PayloadHash {
		t.Fatalf("passing exact reference = %#v, err=%v", reference, err)
	}

	reordered := input
	reordered.AttemptIDs = append([]string(nil), input.AttemptIDs...)
	reordered.Checks = append([]CheckResult(nil), input.Checks...)
	reordered.Checks[0].OracleIDs = append([]string(nil), input.Checks[0].OracleIDs...)
	reordered.Checks[0].ObligationIDs = append([]string(nil), input.Checks[0].ObligationIDs...)
	reordered.Obligations = append([]ObligationRequirement(nil), input.Obligations...)
	reordered.Checks[0].OracleIDs = []string{"oracle-optional", "oracle-must"}
	reordered.Checks[0].ObligationIDs = []string{"OBL-optional", "OBL-must"}
	reordered.Obligations[0], reordered.Obligations[1] = reordered.Obligations[1], reordered.Obligations[0]
	reordered.AttemptIDs[0], reordered.AttemptIDs[1] = reordered.AttemptIDs[1], reordered.AttemptIDs[0]
	reorderedReceipt, err := NewCandidateReceipt(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedReceipt.PayloadHash != receipt.PayloadHash {
		t.Fatalf("semantic reordering changed payload hash: %s != %s", reorderedReceipt.PayloadHash, receipt.PayloadHash)
	}
}

func TestCandidateReceiptNormalizesEmptyCollectionsToJSONArrays(t *testing.T) {
	input := validCandidateReceiptInput()
	input.Checks[0].OracleIDs = nil
	input.Checks[0].AcceptanceCriterionIDs = nil
	input.Checks[0].ObligationIDs = nil
	input.Checks[0].Diagnostics = nil
	receipt, err := NewCandidateReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, expected := range []string{
		`"oracleIds":[]`, `"acceptanceCriterionIds":[]`,
		`"obligationIds":[]`, `"diagnostics":[]`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Receipt payload %s is missing stable collection %s", text, expected)
		}
	}
}

func TestCandidateReceiptFailsClosedForMissingMustCoverage(t *testing.T) {
	input := validCandidateReceiptInput()
	input.Checks[0].ObligationIDs = []string{"OBL-optional"}
	receipt, err := NewCandidateReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != DecisionFailed || receipt.MustPassedCount != 0 || receipt.BlockerCount == 0 {
		t.Fatalf("uncovered Must obligation did not block: %#v", receipt)
	}
	if _, err := receipt.PassedReference(); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("failed receipt produced an exact passing reference: %v", err)
	}
}

func TestCandidateReceiptSeparatesExecutionErrorFromFailedCheck(t *testing.T) {
	input := validCandidateReceiptInput()
	input.Checks[0].Status = CheckError
	input.Checks[0].ExitCode = nil
	input.ExecutionError = "quality worker lost its isolated runtime"
	receipt, err := NewCandidateReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != DecisionError || receipt.BlockerCount == 0 {
		t.Fatalf("infrastructure error did not remain distinct: %#v", receipt)
	}
}

func TestCandidateReceiptRejectsDriftAndUnqualifiedExecution(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewCandidateReceiptInput)
	}{
		{name: "mutable image", mutate: func(input *NewCandidateReceiptInput) { input.Checks[0].VerifierImageDigest = "quality-node:latest" }},
		{name: "foreign attempt", mutate: func(input *NewCandidateReceiptInput) { input.Checks[0].AttemptID = uuid.NewString() }},
		{name: "successful nonzero exit", mutate: func(input *NewCandidateReceiptInput) { value := 1; input.Checks[0].ExitCode = &value }},
		{name: "passed truncated evidence", mutate: func(input *NewCandidateReceiptInput) { input.Checks[0].Truncated = true }},
		{name: "duplicate obligation", mutate: func(input *NewCandidateReceiptInput) {
			input.Obligations = append(input.Obligations, input.Obligations[0])
		}},
		{name: "unbound oracle", mutate: func(input *NewCandidateReceiptInput) { input.Obligations[0].OracleIDs = nil }},
		{name: "noncanonical tree", mutate: func(input *NewCandidateReceiptInput) { input.Subject.TreeHash = strings.Repeat("a", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validCandidateReceiptInput()
			test.mutate(&input)
			if _, err := NewCandidateReceipt(input); !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("invalid input was accepted: %v", err)
			}
		})
	}

	receipt, err := NewCandidateReceipt(validCandidateReceiptInput())
	if err != nil {
		t.Fatal(err)
	}
	receipt.Subject.TreeHash = hashFixture("different-tree")
	if _, err := ParseReceipt(receipt); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("tampered receipt parsed: %v", err)
	}
}

func validCandidateReceiptInput() NewCandidateReceiptInput {
	now := time.Date(2026, 7, 17, 12, 0, 0, 123000, time.UTC)
	attemptA, attemptB := uuid.NewString(), uuid.NewString()
	exitCode := 0
	return NewCandidateReceiptInput{
		ID: uuid.NewString(), RunID: uuid.NewString(), ProjectID: uuid.NewString(),
		Subject: CandidateSubject{
			SessionID: uuid.NewString(), CandidateID: uuid.NewString(), CandidateSnapshotID: uuid.NewString(),
			CandidateVersion: 4, JournalSequence: 2, SessionEpoch: 1, WriterLeaseEpoch: 1,
			TreeHash: hashFixture("tree"),
		},
		BuildManifest:     repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("manifest")},
		BuildContract:     repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("contract")},
		FullStackTemplate: repository.ExactReference{ID: uuid.NewString(), ContentHash: hashFixture("template")},
		Profile:           ProfileReference{ID: "react-fastapi-postgres", Version: 1, ContentHash: hashFixture("profile")},
		Plan:              PlanReference{ID: uuid.NewString(), ContentHash: hashFixture("plan")},
		AttemptIDs:        []string{attemptB, attemptA},
		Checks: []CheckResult{{
			ID: "acceptance", Kind: "contract", Required: true, Status: CheckPassed, AttemptID: attemptA,
			VerifierImageDigest: "registry.example/quality@sha256:" + strings.Repeat("a", 64),
			Argv:                []string{"pytest", "tests/contract"}, WorkingDirectory: "services/api", ExitCode: &exitCode,
			StartedAt: now, CompletedAt: now.Add(time.Second), DurationMS: 1000, AttemptCount: 1,
			OracleIDs:              []string{"oracle-must", "oracle-optional"},
			AcceptanceCriterionIDs: []string{"AC-must"},
			ObligationIDs:          []string{"OBL-must", "OBL-optional"},
			Diagnostics:            []Diagnostic{},
		}},
		Obligations: []ObligationRequirement{
			{ID: "OBL-must", Level: "must", OracleIDs: []string{"oracle-must"}},
			{ID: "OBL-optional", Level: "should", OracleIDs: []string{"oracle-optional"}},
		},
		CreatedBy: uuid.NewString(), CreatedAt: now,
	}
}

func hashFixture(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
