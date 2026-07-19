package repository

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestPlanCandidateRebaseDeterministicThreeWayMerge(t *testing.T) {
	base := rebaseTree(t,
		rebaseFile("api/changed.txt", "base changed", "100644"),
		rebaseFile("api/deleted.txt", "base deleted", "100644"),
		rebaseFile("api/conflict.txt", "base conflict", "100644"),
		rebaseFile("web/target-only.txt", "base target", "100644"),
	)
	predecessor := rebaseTree(t,
		rebaseFile("api/changed.txt", "predecessor changed", "100755"),
		rebaseFile("api/new.txt", "predecessor new", "100644"),
		rebaseFile("api/conflict.txt", "predecessor conflict", "100644"),
		rebaseFile("web/target-only.txt", "base target", "100644"),
	)
	target := rebaseTree(t,
		rebaseFile("api/changed.txt", "base changed", "100644"),
		rebaseFile("api/deleted.txt", "base deleted", "100644"),
		rebaseFile("api/conflict.txt", "target conflict", "100644"),
		rebaseFile("web/target-only.txt", "target changed", "100644"),
	)

	rebaseID := uuid.NewString()
	first, err := PlanCandidateRebase(rebaseID, base, predecessor, target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanCandidateRebase(rebaseID, base, predecessor, target)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanHash != second.PlanHash || first.PlannedTreeHash != second.PlannedTreeHash {
		t.Fatalf("non-deterministic plans: %#v / %#v", first, second)
	}
	if len(first.Operations) != 3 {
		t.Fatalf("operations = %#v, want predecessor change/add/delete", first.Operations)
	}
	wantKinds := []OperationKind{OperationUpsert, OperationDelete, OperationUpsert}
	wantPaths := []string{"api/changed.txt", "api/deleted.txt", "api/new.txt"}
	for index, operation := range first.Operations {
		if operation.Ordinal != index || operation.Operation.Kind != wantKinds[index] || operation.Operation.Path != wantPaths[index] {
			t.Fatalf("operations[%d] = %#v", index, operation)
		}
	}
	if first.Operations[0].Operation.ExpectedHash != target.Files[0].ContentHash ||
		first.Operations[0].Operation.Mode != "100755" {
		t.Fatalf("changed operation = %#v", first.Operations[0])
	}
	if len(first.Conflicts) != 1 || first.Conflicts[0].Path != "api/conflict.txt" ||
		first.Conflicts[0].AncestorFile == nil || first.Conflicts[0].PredecessorFile == nil ||
		first.Conflicts[0].TargetFile == nil {
		t.Fatalf("conflicts = %#v", first.Conflicts)
	}
	if first.PlannedTreeHash == target.TreeHash || first.PlanHash == "" {
		t.Fatalf("plan did not bind automatic result: %#v", first)
	}
}

func TestPlanCandidateRebaseHandlesConcurrentSameChangeAndDeleteConflict(t *testing.T) {
	base := rebaseTree(t, rebaseFile("shared.txt", "base", "100644"), rebaseFile("delete.txt", "base", "100644"))
	same := rebaseFile("shared.txt", "same", "100644")
	predecessor := rebaseTree(t, same)
	target := rebaseTree(t, same, rebaseFile("delete.txt", "target", "100644"))
	plan, err := PlanCandidateRebase(uuid.NewString(), base, predecessor, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 0 || len(plan.Conflicts) != 1 || plan.Conflicts[0].Path != "delete.txt" ||
		plan.Conflicts[0].PredecessorFile != nil {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlanCandidateRebaseRejectsCaseFoldCollision(t *testing.T) {
	base := rebaseTree(t)
	predecessor := rebaseTree(t, rebaseFile("Web/App.tsx", "ours", "100644"))
	target := rebaseTree(t, rebaseFile("web/app.tsx", "theirs", "100644"))
	_, err := PlanCandidateRebase(uuid.NewString(), base, predecessor, target)
	if !errors.Is(err, ErrInvalidRebase) {
		t.Fatalf("error = %v, want invalid rebase", err)
	}
}

func rebaseTree(t *testing.T, files ...TreeFile) TreeManifest {
	t.Helper()
	tree, err := NewTree(files)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func rebaseFile(path, value, mode string) TreeFile {
	digest := sha256.Sum256([]byte(value))
	return TreeFile{Path: path, Mode: mode, ContentHash: fmt.Sprintf("sha256:%x", digest[:]), ByteSize: int64(len(value))}
}
