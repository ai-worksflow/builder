package core

import (
	"encoding/json"
	"testing"
)

func TestRequirementsGateRequiresAcceptanceCriteria(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("product_requirements", json.RawMessage(`{
  "blocks": [{"id":"b1","type":"requirement","requirementId":"REQ-001","priority":"must"}]
}`))
	if report.Valid {
		t.Fatal("Must requirement without acceptance criteria must be blocked")
	}
	if !hasFinding(report, "requirements.must_has_ac") {
		t.Fatalf("unexpected findings: %#v", report.Findings)
	}
}

func TestBlueprintGateRejectsContainsCycle(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("blueprint", json.RawMessage(`{
  "nodes": [
    {"id":"f1","key":"FEATURE-1","type":"feature"},
    {"id":"p1","key":"PAGE-1","type":"page","route":"/","goal":"Open app"}
  ],
  "edges": [
    {"from":"f1","to":"p1","type":"contains"},
    {"from":"p1","to":"f1","type":"contains"}
  ]
}`))
	if report.Valid || !hasFinding(report, "blueprint.contains_cycle") {
		t.Fatalf("expected contains cycle blocker: %#v", report.Findings)
	}
}

func TestPageSpecGateAcceptsCompleteContent(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("page_spec", json.RawMessage(`{
  "route":"/orders",
  "goal":"List orders",
  "states":[{"id":"ready"},{"id":"loading"},{"id":"empty"},{"id":"error"}],
  "acceptanceCriterionIds":["AC-001"]
}`))
	if !report.Valid {
		t.Fatalf("expected complete PageSpec to pass: %#v", report.Findings)
	}
}

func hasFinding(report ValidationReport, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
