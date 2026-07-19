package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

func TestPlanPlatformPatchMergeIsThreeWayAtomicAndIdempotent(t *testing.T) {
	base := mustMergeTree(t,
		mergeFile("apps/web/a.ts", "a", 1),
		mergeFile("apps/web/b.ts", "b", 1),
		mergeFile("apps/web/delete.ts", "d", 1),
	)
	patch := mustMergePatch(t, base, []repository.FileOperation{
		{ID: "patch-a", Kind: repository.OperationUpsert, Path: "apps/web/a.ts", ExpectedHash: testHash("a"), ContentHash: testHash("c"), ByteSize: 1, Mode: "100644"},
		{ID: "patch-b", Kind: repository.OperationUpsert, Path: "apps/web/b.ts", ExpectedHash: testHash("b"), ContentHash: testHash("e"), ByteSize: 1, Mode: "100644"},
		{ID: "patch-delete", Kind: repository.OperationDelete, Path: "apps/web/delete.ts", ExpectedHash: testHash("d")},
	})
	proposed, err := ApplyPlatformPatch(base, patch)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := PlanPlatformPatchMerge(base, base, patch, uuid.NewString())
	if err != nil || len(plan.Conflicts) != 0 || len(plan.Operations) != 3 || plan.PlannedTreeHash != proposed.TreeHash {
		t.Fatalf("clean merge plan=%#v err=%v", plan, err)
	}
	for _, operation := range plan.Operations {
		if operation.ID == "" {
			t.Fatal("derived operation ID is empty")
		}
	}

	plan, err = PlanPlatformPatchMerge(base, proposed, patch, uuid.NewString())
	if err != nil || len(plan.Conflicts) != 0 || len(plan.Operations) != 0 || plan.PlannedTreeHash != proposed.TreeHash {
		t.Fatalf("idempotent merge plan=%#v err=%v", plan, err)
	}

	current := mustMergeTree(t,
		mergeFile("apps/web/a.ts", "f", 1),
		mergeFile("apps/web/b.ts", "b", 1),
		mergeFile("apps/web/delete.ts", "d", 1),
	)
	plan, err = PlanPlatformPatchMerge(base, current, patch, uuid.NewString())
	if err != nil || len(plan.Conflicts) != 1 || plan.Conflicts[0].Path != "apps/web/a.ts" ||
		len(plan.Operations) != 0 || plan.PlannedTreeHash != current.TreeHash {
		t.Fatalf("conflicted merge plan=%#v err=%v", plan, err)
	}
	conflict := plan.Conflicts[0]
	if conflict.Base.ContentHash != testHash("a") || conflict.Current.ContentHash != testHash("f") ||
		conflict.Proposed.ContentHash != testHash("c") {
		t.Fatalf("conflict states=%#v", conflict)
	}
}

func mustMergeTree(t *testing.T, files ...repository.TreeFile) repository.TreeManifest {
	t.Helper()
	tree, err := repository.NewTree(files)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func mergeFile(path, digit string, size int64) repository.TreeFile {
	return repository.TreeFile{Path: path, Mode: "100644", ContentHash: testHash(digit), ByteSize: size}
}

func mustMergePatch(
	t *testing.T,
	base repository.TreeManifest,
	operations []repository.FileOperation,
) PlatformPatch {
	t.Helper()
	proposed := base
	var changedBytes int64
	for _, operation := range operations {
		var err error
		proposed, err = repository.ApplyOperation(proposed, operation)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Kind == repository.OperationUpsert {
			changedBytes += operation.ByteSize
		}
	}
	patch := PlatformPatch{
		SchemaVersion: PlatformPatchSchemaVersion,
		AttemptID:     uuid.NewString(), ProjectID: uuid.NewString(), CandidateID: uuid.NewString(),
		TaskCapsule:       repository.ExactReference{ID: uuid.NewString(), ContentHash: testHash("1")},
		ConfigurationHash: testHash("2"), BaseTreeHash: base.TreeHash,
		ProposedTreeHash: proposed.TreeHash, Operations: operations, ChangedBytes: changedBytes,
	}
	hash, err := domain.CanonicalHash(platformPatchPayload(patch))
	if err != nil {
		t.Fatal(err)
	}
	patch.ContentHash = "sha256:" + hash
	return patch
}
