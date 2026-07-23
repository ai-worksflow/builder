package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/worksflow/builder/backend/internal/repository"
)

func TestValidateRunnerResultClosureRequiresExactCompleteCoverage(t *testing.T) {
	capsule := TaskCapsule{
		ObligationIDs:          []string{"OBL-1", "OBL-2"},
		AcceptanceCriterionIDs: []string{"AC-1", "AC-2"},
		VerificationCommandIDs: []string{"test-contract", "typecheck-web"},
	}
	patch := CapturedPatch{Changes: []CapturedFileChange{
		{Operation: repository.FileOperation{Path: "apps/api/route.ts"}},
		{Operation: repository.FileOperation{Path: "apps/web/page.tsx"}},
	}}
	complete := runnerStructuredResult{
		Summary:      "Complete vertical implementation.",
		ChangedPaths: []string{"apps/api/route.ts", "apps/web/page.tsx"},
		Obligations: []runnerCoverageResult{
			{ID: "OBL-1", Status: "satisfied", Note: "Implemented."},
			{ID: "OBL-2", Status: "satisfied", Note: "Implemented."},
		},
		AcceptanceCriteria: []runnerCoverageResult{
			{ID: "AC-1", Status: "satisfied", Note: "Satisfied."},
			{ID: "AC-2", Status: "satisfied", Note: "Satisfied."},
		},
		Verification: []runnerVerificationResult{
			{CommandID: "test-contract", Status: "passed", Note: "Passed."},
			{CommandID: "typecheck-web", Status: "passed", Note: "Passed."},
		},
		ResourceGraph: runnerResourceGraph{Applicable: true},
		Blockers:      []string{},
	}
	payload, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRunnerResultClosure(capsule, patch, payload); err != nil {
		t.Fatalf("complete closure was rejected: %v", err)
	}

	partial := complete
	partial.AcceptanceCriteria = partial.AcceptanceCriteria[:1]
	payload, _ = json.Marshal(partial)
	if err := validateRunnerResultClosure(capsule, patch, payload); !errors.Is(err, ErrExecutionBlocked) {
		t.Fatalf("partial acceptance coverage error = %v", err)
	}

	failed := complete
	failed.Verification = append([]runnerVerificationResult(nil), complete.Verification...)
	failed.Verification[1].Status = "failed"
	payload, _ = json.Marshal(failed)
	if err := validateRunnerResultClosure(capsule, patch, payload); !errors.Is(err, ErrExecutionBlocked) {
		t.Fatalf("failed verification error = %v", err)
	}

	drifted := complete
	drifted.ChangedPaths = []string{"apps/web/page.tsx"}
	payload, _ = json.Marshal(drifted)
	if err := validateRunnerResultClosure(capsule, patch, payload); !errors.Is(err, ErrExecutionDrift) {
		t.Fatalf("changed-path drift error = %v", err)
	}
}

func TestValidateRunnerResourceGraphRejectsEmojiAndAdHocIcons(t *testing.T) {
	capsule := TaskCapsule{
		ObligationIDs: []string{"OBL-1"}, AcceptanceCriterionIDs: []string{"AC-1"},
	}
	patch := CapturedPatch{Changes: []CapturedFileChange{{
		Operation: repository.FileOperation{Kind: repository.OperationUpsert, Path: "app/page.tsx"},
		Content:   []byte(`<button aria-label="Search">🔍</button>`),
	}}}
	if err := validateRunnerResourceGraph(capsule, patch, runnerResourceGraph{Applicable: true}); !errors.Is(err, ErrExecutionBlocked) {
		t.Fatalf("emoji icon error = %v", err)
	}

	patch.Changes[0].Content = []byte(`<button aria-label="Search"><Search /></button>`)
	graph := runnerResourceGraph{
		Applicable: true,
		Nodes: []runnerResourceNode{{
			ID: "resource.search-icon", Kind: "icon", Purpose: "Open search",
			RequirementIDs: []string{"AC-1"}, Consumers: []string{"app/page.tsx#SearchButton"},
			Source: "icon_library", Reference: "lucide-react:Search",
			Accessibility: "Decorative; the button supplies the accessible name.", Status: "resolved",
		}},
		Edges: []runnerResourceEdge{
			{From: "AC-1", To: "resource.search-icon", Relation: "supports"},
			{From: "resource.search-icon", To: "app/page.tsx#SearchButton", Relation: "consumed_by"},
		},
	}
	if err := validateRunnerResourceGraph(capsule, patch, graph); err != nil {
		t.Fatalf("valid icon graph was rejected: %v", err)
	}
	graph.Nodes[0].Source = "generated_svg"
	graph.Nodes[0].Reference = "public/search.svg"
	if err := validateRunnerResourceGraph(capsule, patch, graph); !errors.Is(err, ErrExecutionBlocked) {
		t.Fatalf("ad hoc generated icon error = %v", err)
	}
}
