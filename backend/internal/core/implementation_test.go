package core

import (
	"testing"

	"github.com/worksflow/builder/backend/internal/domain"
)

func TestImplementationRevisionLineageSourcesFreezeEveryInput(t *testing.T) {
	t.Parallel()

	blueprint := implementationTestVersionRef("blueprint")
	pageSpec := implementationTestVersionRef("page-spec")
	prototype := implementationTestVersionRef("prototype")
	requirement := implementationTestVersionRef("requirement")
	contract := implementationTestVersionRef("contract")
	designSystem := implementationTestVersionRef("design-system")
	decision := implementationTestVersionRef("decision")
	workflowBase := implementationTestVersionRef("workflow-base")
	workflowAnchor := "page.orders"
	workflowSource := VersionRef{ArtifactID: "selection-artifact", RevisionID: "selection-revision", ContentHash: "sha256:selection", AnchorID: &workflowAnchor}
	workspace := implementationTestVersionRef("workspace")
	workflowBaseRef := domain.ArtifactRef{ArtifactID: workflowBase.ArtifactID, RevisionID: workflowBase.RevisionID, ContentHash: workflowBase.ContentHash}
	workflowSourceRef := domain.ArtifactRef{ArtifactID: workflowSource.ArtifactID, RevisionID: workflowSource.RevisionID, ContentHash: workflowSource.ContentHash, AnchorID: workflowAnchor}
	bundle := WorkbenchBundle{
		BlueprintRevision: blueprint, PageSpecRevision: pageSpec, PrototypeRevision: prototype,
		RequirementRevisions: []VersionRef{requirement}, ContractRevisions: []VersionRef{contract},
		DesignSystemRevisions: []VersionRef{designSystem}, ContextRevisions: []WorkbenchContextRevision{{Kind: "decision_record", Revision: decision}},
		WorkflowContext: &ApplicationBuildContext{InputManifest: domain.InputManifest{
			BaseRevision: &workflowBaseRef,
			Sources:      []domain.ManifestSource{{Ref: workflowSourceRef, Purpose: "blueprint_selection_node"}},
		}},
		CurrentWorkspaceRevision: &workspace,
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
		{decision, "context_decision_record", "implemented_by"},
		{workflowBase, "workflow_input_base", "implemented_by"},
		{workflowSource, "workflow_input:blueprint_selection_node", "implemented_by"},
		{workspace, "workspace_base", "derives_from"},
	}
	if len(sources) != len(want) {
		t.Fatalf("expected every frozen Workbench input, got %d sources", len(sources))
	}
	for index, expected := range want {
		actual := sources[index]
		if !exactWorkbenchVersionRef(actual.Ref, expected.ref) || actual.Purpose != expected.purpose ||
			actual.Relation != expected.relation || !actual.Required {
			t.Fatalf("source %d lost exact lineage: got=%+v want=%+v", index, actual, expected)
		}
	}
	manifestSources := buildManifestSources(bundle)
	for _, expected := range []VersionRef{decision, workflowBase, workflowSource} {
		found := false
		for _, actual := range manifestSources {
			if exactWorkbenchVersionRef(actual, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("build manifest source set lost contextual evidence: %+v", expected)
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

func TestFileOperationsHashExactStoredContent(t *testing.T) {
	t.Parallel()

	original := "  const value = 'original'\n\n"
	replacement := "const value = 'replacement'\n"
	for _, test := range []struct {
		name      string
		operation FileOperation
	}{
		{
			name: "upsert",
			operation: FileOperation{
				ID: "upsert", Kind: "file.upsert", Path: "src/original.js",
				Content: &replacement, ExpectedHash: hashText(original),
			},
		},
		{
			name: "delete",
			operation: FileOperation{
				ID: "delete", Kind: "file.delete", Path: "src/original.js",
				ExpectedHash: hashText(original),
			},
		},
		{
			name: "rename",
			operation: FileOperation{
				ID: "rename", Kind: "file.rename", FromPath: "src/original.js", Path: "src/renamed.js",
				ExpectedHash: hashText(original),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			workspace := map[string]any{
				"files": []any{map[string]any{
					"path": "src/original.js", "content": original,
					"language": "javascript", "revision": float64(1),
				}},
			}
			if _, err := applyFileOperations(workspace, []FileOperation{test.operation}); err != nil {
				t.Fatalf("expected the hash of the exact stored bytes to match: %v", err)
			}
		})
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
