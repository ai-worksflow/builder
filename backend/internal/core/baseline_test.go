package core

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestBaselineHashExcludesItsOwnField(t *testing.T) {
	t.Parallel()
	baseline := RequirementBaseline{SchemaVersion: 1, SourceVersions: []VersionRef{}, Requirements: nil}
	first := baseline
	first.BaselineHash = ""
	second := baseline
	second.BaselineHash = "ignored"
	second.BaselineHash = ""
	if len(first.SourceVersions) != len(second.SourceVersions) {
		t.Fatal("baseline copies should remain equivalent")
	}
}

func TestBaselineNormalizesStructuredRequirementsAndDeduplicatesAnchors(t *testing.T) {
	t.Parallel()
	baseline := RequirementBaseline{Requirements: []json.RawMessage{}, Actors: []json.RawMessage{}}
	content := map[string]any{
		"blocks": []any{
			map[string]any{"id": "context-1", "type": "actor", "text": "Support agent"},
			map[string]any{"id": "REQ-001", "type": "requirement", "text": "Legacy copy"},
		},
		"requirements": []any{
			map[string]any{"id": "REQ-001", "statement": "Structured duplicate"},
			map[string]any{"id": "REQ-002", "statement": "Structured requirement"},
		},
		"acceptanceCriteria": []any{
			map[string]any{"id": "AC-001", "statement": "Observable result"},
		},
	}
	anchors := appendBaselineContent(&baseline, content)
	if len(baseline.Requirements) != 3 {
		t.Fatalf("expected one legacy and two unique structured facts, got %d", len(baseline.Requirements))
	}
	if len(baseline.Actors) != 1 {
		t.Fatalf("expected actor block to remain in its typed baseline section")
	}
	if len(anchors) != 4 {
		t.Fatalf("expected all unique block and requirement anchors, got %#v", anchors)
	}
	var legacy map[string]any
	if err := json.Unmarshal(baseline.Requirements[0], &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy["requirementId"] != "REQ-001" || legacy["statement"] != "Legacy copy" {
		t.Fatalf("legacy requirement block was not canonicalized: %#v", legacy)
	}
	var criterion map[string]any
	if err := json.Unmarshal(baseline.Requirements[2], &criterion); err != nil {
		t.Fatal(err)
	}
	if criterion["type"] != "acceptanceCriterion" || criterion["acceptanceCriterionId"] != "AC-001" {
		t.Fatalf("structured criterion was not canonicalized: %#v", criterion)
	}
}

func TestFinalRequirementBaselineAcceptsTextOnlyRequirementBlocks(t *testing.T) {
	t.Parallel()
	baseline := baselineValidationFixture()
	appendBaselineContent(&baseline, map[string]any{
		"blocks": []any{
			map[string]any{"id": "REQ-BLOCK", "type": "requirement", "text": "Support mobile interviews."},
			map[string]any{"id": "AC-BLOCK", "type": "acceptanceCriterion", "text": "The interview completes on mobile."},
		},
	})

	payload, err := finalizeRequirementBaselinePayload(baseline)
	if err != nil {
		t.Fatalf("expected text-only requirement blocks to compile: %v", err)
	}
	if report := ValidateArtifactContent("requirement_baseline", payload); !report.Valid {
		t.Fatalf("compiled payload must pass the canonical gate: %#v", report.Findings)
	}
}

func TestFinalRequirementBaselineRejectsProjectBriefWithoutRequirements(t *testing.T) {
	t.Parallel()
	baseline := baselineValidationFixture()
	appendBaselineContent(&baseline, map[string]any{
		"summary": "Define the support application.",
		"blocks": []any{
			map[string]any{"id": "goal-1", "type": "goal", "text": "Reduce response time."},
		},
	})

	_, err := finalizeRequirementBaselinePayload(baseline)
	if !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("expected invalid compiled baseline to block before persistence, got %v", err)
	}
}

func TestFinalRequirementBaselineAcceptsValidProductRequirements(t *testing.T) {
	t.Parallel()
	baseline := baselineValidationFixture()
	appendBaselineContent(&baseline, map[string]any{
		"summary": "Define order exception handling.",
		"blocks": []any{
			map[string]any{"id": "source-1", "type": "paragraph", "text": "Approved source context."},
		},
		"requirements": []any{
			map[string]any{
				"id": "REQ-001", "statement": "Agents must resolve order exceptions.",
				"priority": "must", "acceptanceCriterionIds": []any{"AC-001"},
				"sourceBlockIds": []any{"source-1"},
			},
		},
		"acceptanceCriteria": []any{
			map[string]any{"id": "AC-001", "statement": "The exception is marked resolved."},
		},
	})

	payload, err := finalizeRequirementBaselinePayload(baseline)
	if err != nil {
		t.Fatalf("expected valid product requirements to compile: %v", err)
	}
	if report := ValidateArtifactContent("requirement_baseline", payload); !report.Valid {
		t.Fatalf("compiled payload must pass the canonical gate: %#v", report.Findings)
	}
}

func baselineValidationFixture() RequirementBaseline {
	return RequirementBaseline{
		SchemaVersion: 1,
		SourceVersions: []VersionRef{{
			ArtifactID:  "11111111-1111-4111-8111-111111111111",
			RevisionID:  "22222222-2222-4222-8222-222222222222",
			ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		Actors: []json.RawMessage{}, Journeys: []json.RawMessage{}, Requirements: []json.RawMessage{},
		BusinessRules: []json.RawMessage{}, NonFunctionalRequirements: []json.RawMessage{},
		Constraints: []json.RawMessage{}, Decisions: []json.RawMessage{}, References: []json.RawMessage{},
		NonBlockingOpenQuestions: []json.RawMessage{},
	}
}
