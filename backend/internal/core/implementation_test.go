package core

import (
	"testing"
)

func TestFileOperationsRequireExpectedHashForExistingFiles(t *testing.T) {
	t.Parallel()
	content := "new"
	workspace := map[string]any{
		"files": []any{map[string]any{"path": "src/app.ts", "content": "old", "language": "typescript", "revision": float64(1)}},
	}
	_, err := applyFileOperations(workspace, []FileOperation{{
		ID: "op-1", Kind: "file.upsert", Path: "src/app.ts", Content: &content,
	}})
	if !errorsIs(err, ErrProposalStale) {
		t.Fatalf("expected stale error, got %v", err)
	}
}

func TestFileOperationsApplyInDependencyOrder(t *testing.T) {
	t.Parallel()
	first := "one"
	second := "two"
	operations := []FileOperation{
		{ID: "second", Kind: "file.upsert", Path: "src/two.ts", Content: &second, DependsOn: []string{"first"}, Decision: ImplementationAccepted},
		{ID: "first", Kind: "file.upsert", Path: "src/one.ts", Content: &first, Decision: ImplementationAccepted},
	}
	ordered, err := acceptedImplementationOperations(operations)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].ID != "first" || ordered[1].ID != "second" {
		t.Fatalf("unexpected order: %s, %s", ordered[0].ID, ordered[1].ID)
	}
}

func TestWorkspacePathProtection(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"../secret", "/absolute", ".env", ".git/config", `bad\\path`} {
		if err := validateWorkspacePath(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
	if err := validateWorkspacePath("src/app/page.tsx"); err != nil {
		t.Fatalf("expected valid path: %v", err)
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		wrapped, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}
