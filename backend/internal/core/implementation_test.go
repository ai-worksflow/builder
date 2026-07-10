package core

import (
	"testing"
)

func TestImplementationRevisionLineageSourcesFreezeEveryInput(t *testing.T) {
	t.Parallel()

	blueprint := implementationTestVersionRef("blueprint")
	pageSpec := implementationTestVersionRef("page-spec")
	prototype := implementationTestVersionRef("prototype")
	requirement := implementationTestVersionRef("requirement")
	contract := implementationTestVersionRef("contract")
	designSystem := implementationTestVersionRef("design-system")
	workspace := implementationTestVersionRef("workspace")
	bundle := WorkbenchBundle{
		BlueprintRevision: blueprint, PageSpecRevision: pageSpec, PrototypeRevision: prototype,
		RequirementRevisions: []VersionRef{requirement}, ContractRevisions: []VersionRef{contract},
		DesignSystemRevisions: []VersionRef{designSystem}, CurrentWorkspaceRevision: &workspace,
	}

	sources := implementationRevisionLineageSources(bundle)
	want := []struct {
		ref      VersionRef
		purpose  string
		relation string
	}{
		{blueprint, "blueprint", "implemented_by"},
		{pageSpec, "page_spec", "implemented_by"},
		{prototype, "prototype", "implemented_by"},
		{requirement, "requirement", "implemented_by"},
		{contract, "contract", "implemented_by"},
		{designSystem, "design_system", "implemented_by"},
		{workspace, "workspace_base", "derives_from"},
	}
	if len(sources) != len(want) {
		t.Fatalf("expected every frozen Workbench input, got %d sources", len(sources))
	}
	for index, expected := range want {
		actual := sources[index]
		if actual.Ref != expected.ref || actual.Purpose != expected.purpose ||
			actual.Relation != expected.relation || !actual.Required {
			t.Fatalf("source %d lost exact lineage: got=%+v want=%+v", index, actual, expected)
		}
	}
}

func TestImplementationProposalBaseRequiresExactWorkspaceRef(t *testing.T) {
	t.Parallel()

	anchor := "workspace-root"
	exact := &VersionRef{
		ArtifactID: "workspace-artifact", RevisionID: "workspace-revision",
		ContentHash: "sha256:workspace", AnchorID: &anchor,
	}
	copyRef := cloneVersionRef(exact)
	if !optionalVersionRefsEqual(exact, copyRef) || !optionalVersionRefsEqual(nil, nil) {
		t.Fatal("identical exact workspace refs must match")
	}
	for name, mutate := range map[string]func(*VersionRef){
		"artifact": func(ref *VersionRef) { ref.ArtifactID = "other" },
		"revision": func(ref *VersionRef) { ref.RevisionID = "other" },
		"hash":     func(ref *VersionRef) { ref.ContentHash = "sha256:other" },
		"anchor":   func(ref *VersionRef) { other := "other"; ref.AnchorID = &other },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneVersionRef(exact)
			mutate(candidate)
			if optionalVersionRefsEqual(exact, candidate) {
				t.Fatalf("%s mismatch was accepted as an exact proposal base", name)
			}
		})
	}
	if optionalVersionRefsEqual(exact, nil) || optionalVersionRefsEqual(nil, exact) {
		t.Fatal("nil base is allowed only when there is no workspace ref")
	}
}

func implementationTestVersionRef(seed string) VersionRef {
	return VersionRef{
		ArtifactID: seed + "-artifact", RevisionID: seed + "-revision", ContentHash: "sha256:" + seed,
	}
}

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
