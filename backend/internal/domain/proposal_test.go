package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testManifest(t *testing.T, base ArtifactRef) InputManifest {
	t.Helper()
	manifest, err := NewInputManifest(
		"manifest-1", "project-1", "update_blueprint", "slice-1", &base,
		[]ManifestSource{{Ref: base, Purpose: "approved blueprint"}}, json.RawMessage(`{"mode":"strict"}`),
		"blueprint-proposal/v1", "author", testNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func testProposal(t *testing.T, operations []ProposalOperation) *OutputProposal {
	t.Helper()
	base := testRevision(t, "blueprint", "bp-v1", 1, `{"items":[1],"name":"old"}`).Ref("")
	manifest := testManifest(t, base)
	proposal, err := NewOutputProposal(
		"proposal-1", "project-1", "blueprint", manifest.Ref(), base, operations,
		nil, nil, "ai-run", testNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func TestManifestRequiresPinnedSourcesAndDetectsMutation(t *testing.T) {
	_, err := NewInputManifest("m", "p", "job", "", nil, nil, nil, "v1", "user", testNow)
	if !errors.Is(err, ErrManifestUnpinned) {
		t.Fatalf("expected unpinned manifest error, got %v", err)
	}
	base := testRevision(t, "artifact", "rev-1", 1, `{}`).Ref("")
	manifest := testManifest(t, base)
	manifest.JobType = "mutated"
	if err := manifest.Validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected manifest hash conflict, got %v", err)
	}
	baseOnly, err := NewInputManifest(
		"base-only", "project", "refine_project_brief", "", &base, nil,
		json.RawMessage(`{"reviewedIntent":true}`), "project-brief-proposal/v1", "user", testNow,
	)
	if err != nil {
		t.Fatalf("exact base-only transform manifest was rejected: %v", err)
	}
	if baseOnly.BaseRevision == nil || !baseOnly.BaseRevision.Equal(base) || len(baseOnly.Sources) != 0 {
		t.Fatalf("base-only manifest lost its immutable target: %+v", baseOnly)
	}
	encoded, err := json.Marshal(baseOnly)
	if err != nil || !strings.Contains(string(encoded), `"sources":[]`) {
		t.Fatalf("base-only manifest sources must retain an array wire shape: %s err=%v", encoded, err)
	}
}

func TestProposalAppliesAcceptedOperationsInDependencyOrder(t *testing.T) {
	proposal := testProposal(t, []ProposalOperation{
		{ID: "rename", Kind: OperationReplace, Path: "/name", Value: json.RawMessage(`"new"`)},
		{ID: "append", Kind: OperationAdd, Path: "/items/-", Value: json.RawMessage(`2`), DependsOn: []string{"rename"}},
	})
	if err := proposal.Decide("rename", DecisionAccepted, "reviewer", "", 1); err != nil {
		t.Fatal(err)
	}
	if err := proposal.Decide("append", DecisionAccepted, "reviewer", "", 2); err != nil {
		t.Fatal(err)
	}
	operations, err := proposal.AcceptedOperations()
	if err != nil {
		t.Fatal(err)
	}
	patched, err := ApplyProposalPatch(json.RawMessage(`{"name":"old","items":[1]}`), operations)
	if err != nil {
		t.Fatal(err)
	}
	if string(patched) != `{"items":[1,2],"name":"new"}` {
		t.Fatalf("unexpected patch output: %s", patched)
	}
	if err := proposal.MarkApplied(3, testNow); err != nil {
		t.Fatal(err)
	}
	if proposal.Status != ProposalApplied {
		t.Fatalf("expected applied, got %s", proposal.Status)
	}
}

func TestProposalPartialApplyIsFinalAndDependencySafe(t *testing.T) {
	proposal := testProposal(t, []ProposalOperation{
		{ID: "rename", Kind: OperationReplace, Path: "/name", Value: json.RawMessage(`"new"`)},
		{ID: "append", Kind: OperationAdd, Path: "/items/-", Value: json.RawMessage(`2`)},
	})
	if err := proposal.Decide("rename", DecisionAccepted, "reviewer", "", 1); err != nil {
		t.Fatal(err)
	}
	if err := proposal.Decide("append", DecisionRejected, "reviewer", "not in scope", 2); err != nil {
		t.Fatal(err)
	}
	if err := proposal.MarkApplied(3, testNow); err != nil {
		t.Fatal(err)
	}
	if proposal.Status != ProposalPartiallyApplied || proposal.Operations[0].Decision != DecisionApplied {
		t.Fatalf("unexpected partial apply state: %+v", proposal)
	}
	if err := proposal.Decide("append", DecisionAccepted, "reviewer", "", 4); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial application must be final, got %v", err)
	}

	dependent := testProposal(t, []ProposalOperation{
		{ID: "base", Kind: OperationReplace, Path: "/name", Value: json.RawMessage(`"new"`)},
		{ID: "child", Kind: OperationAdd, Path: "/items/-", Value: json.RawMessage(`2`), DependsOn: []string{"base"}},
	})
	_ = dependent.Decide("base", DecisionRejected, "reviewer", "not wanted", 1)
	_ = dependent.Decide("child", DecisionAccepted, "reviewer", "", 2)
	if _, err := dependent.AcceptedOperations(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected rejected dependency to block apply, got %v", err)
	}
}

func TestProposalStaleAndPayloadMutationGuards(t *testing.T) {
	proposal := testProposal(t, []ProposalOperation{{ID: "rename", Kind: OperationReplace, Path: "/name", Value: json.RawMessage(`"new"`)}})
	proposal.Operations[0].Path = "/other"
	if err := proposal.ValidatePayloadHash(); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected immutable payload conflict, got %v", err)
	}
	proposal = testProposal(t, []ProposalOperation{{ID: "rename", Kind: OperationReplace, Path: "/name", Value: json.RawMessage(`"new"`)}})
	current := testRevision(t, "blueprint", "bp-v2", 2, `{}`).Ref("")
	if err := proposal.MarkStale(current, 1); err != nil {
		t.Fatal(err)
	}
	if proposal.Status != ProposalStale {
		t.Fatalf("expected stale proposal, got %s", proposal.Status)
	}
}
