package core

import (
	"encoding/json"
	"testing"

	"github.com/worksflow/builder/backend/internal/domain"
)

func TestRequirementsGateRequiresAcceptanceCriteria(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("product_requirements", json.RawMessage(`{
  "summary":"Define the product requirements.",
  "blocks": [{"id":"b1","type":"requirement","requirementId":"REQ-001","priority":"must"}]
}`))
	if report.Valid {
		t.Fatal("Must requirement without acceptance criteria must be blocked")
	}
	if !hasFinding(report, "requirements.must_has_ac") {
		t.Fatalf("unexpected findings: %#v", report.Findings)
	}
}

func TestRequirementsGateAcceptsCanonicalFrontendArrays(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("product_requirements", json.RawMessage(`{
  "summary":"Define a collaborative editor.",
  "blocks":[{"id":"context-1","type":"paragraph","text":"Preserve concurrent edits."}],
  "requirements":[{
    "id":"REQ-001","title":"Concurrent editing","statement":"Editors preserve each other's changes.",
    "priority":"must","acceptanceCriterionIds":["AC-001"],"sourceBlockIds":["context-1"]
  }],
  "acceptanceCriteria":[{
    "id":"AC-001","statement":"A stale ETag returns 412.","priority":"must","status":"open"
  }]
}`))
	if !report.Valid {
		t.Fatalf("expected canonical frontend requirements to pass: %#v", report.Findings)
	}
}

func TestRequirementsGateRejectsDanglingStructuredAcceptanceReference(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("product_requirements", json.RawMessage(`{
  "summary":"Define a collaborative editor.",
  "blocks":[{"id":"context-1","type":"paragraph","text":"Context."}],
  "requirements":[{
    "id":"REQ-001","statement":"Preserve edits.","priority":"must","acceptanceCriterionIds":["AC-MISSING"],"sourceBlockIds":["context-1"]
  }],
  "acceptanceCriteria":[]
}`))
	if report.Valid || !hasFinding(report, "requirements.ac_reference") {
		t.Fatalf("expected a dangling acceptance reference to fail: %#v", report.Findings)
	}
}

func TestRequirementBaselineUsesItsOwnImmutableContract(t *testing.T) {
	t.Parallel()
	payload := map[string]any{
		"schemaVersion": 1,
		"sourceVersions": []any{map[string]any{
			"artifactId": "document-1", "revisionId": "revision-1", "contentHash": "sha256:document",
		}},
		"actors":                    []any{},
		"journeys":                  []any{},
		"businessRules":             []any{},
		"nonFunctionalRequirements": []any{},
		"constraints":               []any{},
		"decisions":                 []any{},
		"references":                []any{},
		"nonBlockingOpenQuestions":  []any{},
		"requirements": []any{
			map[string]any{"type": "requirement", "requirementId": "REQ-001", "statement": "Preserve edits.", "priority": "must", "acceptanceCriterionIds": []any{"AC-001"}},
			map[string]any{"type": "acceptanceCriterion", "acceptanceCriterionId": "AC-001", "statement": "A stale ETag returns 412."},
		},
		"baselineHash": "",
	}
	hash, err := domain.CanonicalHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	payload["baselineHash"] = hash
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	report := ValidateArtifactContent("requirement_baseline", encoded)
	if !report.Valid {
		t.Fatalf("expected immutable Requirement Baseline to pass: %#v", report.Findings)
	}
	payload["baselineHash"] = "sha256:tampered"
	encoded, _ = json.Marshal(payload)
	report = ValidateArtifactContent("requirement_baseline", encoded)
	if report.Valid || !hasFinding(report, "baseline.hash_mismatch") {
		t.Fatalf("expected tampered baseline hash to fail: %#v", report.Findings)
	}
}

func TestProjectBriefGateRequiresNonEmptyGoalText(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("project_brief", json.RawMessage(`{
	"summary":"A valid summary.",
  "blocks": [{"id":"goal-1","type":"goal","text":"  "}]
}`))
	if report.Valid || !hasFinding(report, "brief.goal_text_required") || !hasFinding(report, "brief.goal_required") {
		t.Fatalf("expected an empty goal to block the Project Brief: %#v", report.Findings)
	}
}

func TestProjectBriefGateAcceptsResolvedCanonicalBlocks(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("project_brief", json.RawMessage(`{
	"summary":"Create a measurable support workflow.",
  "blocks": [
    {"id":"goal-1","type":"goal","text":"Reduce first-response time by 20%."},
    {"id":"question-1","type":"openQuestion","text":"Who owns triage?","blocking":true,"status":"answered"}
  ]
}`))
	if !report.Valid {
		t.Fatalf("expected resolved canonical Project Brief to pass: %#v", report.Findings)
	}
}

func TestProjectBriefGateRequiresSummary(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("project_brief", json.RawMessage(`{
  "summary":" ",
  "blocks":[{"id":"goal-1","type":"goal","text":"Reduce first-response time."}]
}`))
	if report.Valid || !hasFinding(report, "brief.summary_required") {
		t.Fatalf("expected a missing summary to block the Project Brief: %#v", report.Findings)
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

func TestBlueprintGateAcceptsCanonicalFrontendIR(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("blueprint", json.RawMessage(`{
  "nodes": [
    {"id":"feature-orders","key":"FEATURE-ORDERS","kind":"feature"},
    {"id":"page-orders","key":"PAGE-ORDERS","kind":"page","route":"/orders","userGoal":"Review open orders.","requirementIds":["REQ-001"]}
  ],
  "edges": [
    {"id":"edge-orders","sourceNodeId":"feature-orders","targetNodeId":"page-orders","kind":"contains","required":true}
  ]
}`))
	if !report.Valid {
		t.Fatalf("expected canonical frontend Blueprint IR to pass: %#v", report.Findings)
	}
}

func TestBlueprintGateRequiresPermissionForAPIOperations(t *testing.T) {
	t.Parallel()
	withoutPermission := ValidateArtifactContent("blueprint", json.RawMessage(`{
  "nodes":[{"id":"api-orders","key":"API-ORDERS","kind":"apiOperation","method":"GET","path":"/orders"}],
  "edges":[]
}`))
	if withoutPermission.Valid || !hasFinding(withoutPermission, "blueprint.api_permission") {
		t.Fatalf("expected unprotected API operation to fail: %#v", withoutPermission.Findings)
	}
	withPermission := ValidateArtifactContent("blueprint", json.RawMessage(`{
  "nodes":[
    {"id":"api-orders","key":"API-ORDERS","kind":"apiOperation","method":"GET","path":"/orders"},
    {"id":"permission-orders","key":"PERMISSION-ORDERS","kind":"permission"}
  ],
  "edges":[{"id":"edge-permission","sourceNodeId":"api-orders","targetNodeId":"permission-orders","kind":"requires"}]
}`))
	if !withPermission.Valid {
		t.Fatalf("expected protected API operation to pass: %#v", withPermission.Findings)
	}
}

func TestPageSpecGateAcceptsCompleteContent(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("page_spec", json.RawMessage(`{
  "blueprintPageNodeId":"page-orders",
  "title":"Orders",
  "route":"/orders",
  "goal":"List orders",
  "states":[
    {"id":"state-ready","key":"ready","title":"Ready"},
    {"id":"state-loading","key":"loading","title":"Loading"},
    {"id":"state-empty","key":"empty","title":"Empty"},
    {"id":"state-error","key":"error","title":"Error"}
  ],
  "acceptanceCriterionIds":["AC-001"]
}`))
	if !report.Valid {
		t.Fatalf("expected complete PageSpec to pass: %#v", report.Findings)
	}
}

func TestPageSpecGateUsesStableStateKeysInsteadOfServerIDs(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("page_spec", json.RawMessage(`{
  "route":"/orders",
  "title":"Orders",
  "blueprintPageNodeId":"page-orders",
  "userGoal":"List orders",
  "states":[
    {"id":"state-a","key":"ready","title":"Ready"},
    {"id":"state-b","key":"loading","title":"Loading"},
    {"id":"state-c","key":"empty","title":"Empty"},
    {"id":"state-d","key":"error","title":"Error"}
  ],
  "acceptanceCriterionIds":["AC-001"]
}`))
	if !report.Valid {
		t.Fatalf("expected stable PageSpec state keys to pass: %#v", report.Findings)
	}
}

func TestPrototypeGateAcceptsCanonicalResponsiveScene(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("prototype", json.RawMessage(`{
  "pageSpecRevision":{"artifactId":"page-spec-1","revisionId":"revision-1","contentHash":"sha256:page-spec"},
  "states":[{"id":"state-ready","key":"ready","title":"Ready","required":true,"fixtureIds":[]}],
  "breakpoints":[
    {"id":"bp-desktop","name":"Desktop"},
    {"id":"bp-tablet","name":"Tablet"},
    {"id":"bp-mobile","name":"Mobile"}
  ],
  "layers":{"layer-root":{"id":"layer-root","childIds":[],"kind":"frame"}},
  "frames":[
    {"id":"frame-desktop","stateId":"state-ready","breakpointId":"bp-desktop","rootLayerId":"layer-root"},
    {"id":"frame-tablet","stateId":"state-ready","breakpointId":"bp-tablet","rootLayerId":"layer-root"},
    {"id":"frame-mobile","stateId":"state-ready","breakpointId":"bp-mobile","rootLayerId":"layer-root"}
  ],
  "fixtures":[],
  "interactions":[]
}`))
	if !report.Valid {
		t.Fatalf("expected canonical responsive prototype to pass: %#v", report.Findings)
	}
}

func TestPrototypeGateRejectsExecutableInteractionAction(t *testing.T) {
	t.Parallel()
	report := ValidateArtifactContent("prototype", json.RawMessage(`{
  "pageSpecRevision":{"artifactId":"page-spec-1","revisionId":"revision-1","contentHash":"sha256:page-spec"},
  "states":[{"id":"state-ready","key":"ready","title":"Ready","required":true}],
  "breakpoints":[{"id":"bp-desktop","name":"Desktop"},{"id":"bp-tablet","name":"Tablet"},{"id":"bp-mobile","name":"Mobile"}],
  "layers":{"layer-root":{"id":"layer-root","childIds":[],"kind":"frame"}},
  "frames":[
    {"id":"f1","stateId":"state-ready","breakpointId":"bp-desktop","rootLayerId":"layer-root"},
    {"id":"f2","stateId":"state-ready","breakpointId":"bp-tablet","rootLayerId":"layer-root"},
    {"id":"f3","stateId":"state-ready","breakpointId":"bp-mobile","rootLayerId":"layer-root"}
  ],
  "interactions":[{"id":"interaction-1","sourceLayerId":"layer-root","trigger":"click","actions":[{"type":"javascript","source":"alert(1)"}]}]
}`))
	if report.Valid || !hasFinding(report, "prototype.invalid_action") {
		t.Fatalf("expected executable prototype action to fail: %#v", report.Findings)
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
