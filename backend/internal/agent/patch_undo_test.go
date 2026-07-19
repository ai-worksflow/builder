package agent

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/repository"
)

func TestPlanPlatformPatchMergeUndoPreservesUnrelatedChangesAndBlocksDrift(t *testing.T) {
	base := mustMergeTree(t,
		mergeFile("apps/web/a.ts", "a", 1),
		mergeFile("apps/web/unrelated.ts", "d", 1),
	)
	mergeOperation := repository.FileOperation{
		ID: "agent-merge:source:0000", Kind: repository.OperationUpsert, Path: "apps/web/a.ts",
		ExpectedHash: testHash("a"), ContentHash: testHash("b"), ByteSize: 1, Mode: "100644",
	}
	merged, err := repository.ApplyOperation(base, mergeOperation)
	if err != nil {
		t.Fatal(err)
	}
	merge := mustUndoMergeRecord(t, base, merged, mergeOperation)
	current := mustMergeTree(t,
		mergeFile("apps/web/a.ts", "b", 1),
		mergeFile("apps/web/unrelated.ts", "e", 1),
	)
	plan, err := PlanPlatformPatchMergeUndo(base, current, merge, uuid.NewString())
	if err != nil || len(plan.Conflicts) != 0 || len(plan.Operations) != 1 {
		t.Fatalf("undo plan=%#v err=%v", plan, err)
	}
	want := mustMergeTree(t,
		mergeFile("apps/web/a.ts", "a", 1),
		mergeFile("apps/web/unrelated.ts", "e", 1),
	)
	if plan.PlannedTreeHash != want.TreeHash || plan.Operations[0].ExpectedHash != testHash("b") {
		t.Fatalf("undo did not preserve unrelated state: %#v", plan)
	}

	drifted := mustMergeTree(t,
		mergeFile("apps/web/a.ts", "c", 1),
		mergeFile("apps/web/unrelated.ts", "e", 1),
	)
	plan, err = PlanPlatformPatchMergeUndo(base, drifted, merge, uuid.NewString())
	if err != nil || len(plan.Conflicts) != 1 || len(plan.Operations) != 0 ||
		plan.PlannedTreeHash != drifted.TreeHash {
		t.Fatalf("drifted undo plan=%#v err=%v", plan, err)
	}
}

func mustUndoMergeRecord(
	t *testing.T,
	base, merged repository.TreeManifest,
	operation repository.FileOperation,
) PatchMergePlanRecord {
	t.Helper()
	attemptID := uuid.NewString()
	record, err := NewPatchMergePlanRecord(NewPatchMergePlanInput{
		ID: uuid.NewString(), OperationID: "source-merge", ProjectID: uuid.NewString(),
		SandboxSessionID: uuid.NewString(), CandidateID: uuid.NewString(),
		AttemptID: attemptID, AttemptVersion: 8,
		PatchReference: BlobReference{
			Store: AgentEvidenceStore, OwnerID: attemptID, Ref: uuid.NewString(),
			ContentHash: testHash("8"), ByteSize: 128,
		},
		PatchRawHash: testHash("9"), PatchContentHash: testHash("7"),
		ExpectedSessionVersion: 5, ExpectedSessionEpoch: 1,
		ExpectedCandidateVersion: 2, ExpectedCandidateJournalSequence: 0,
		ExpectedWriterLeaseEpoch: 1, CreatedBy: uuid.NewString(),
	}, PlatformPatchMergePlan{
		BaseTreeHash: base.TreeHash, CurrentTreeHash: base.TreeHash,
		ProposedTreeHash: merged.TreeHash, PlannedTreeHash: merged.TreeHash,
		Operations: []repository.FileOperation{operation}, Conflicts: []PatchMergeConflict{},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return record
}
