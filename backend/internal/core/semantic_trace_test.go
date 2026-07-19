package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/worksflow/builder/backend/internal/domain"
)

func TestDecodeRequirementTraceRejectsDanglingAcceptanceCriterion(t *testing.T) {
	t.Parallel()
	if _, err := decodeRequirementTrace(json.RawMessage(`{
  "requirements":[{"type":"requirement","requirementId":"REQ-A","priority":"must","acceptanceCriterionIds":["AC-MISSING"]}]
}`)); err == nil || !strings.Contains(err.Error(), "AC-MISSING") {
		t.Fatalf("Requirement Baseline dangling acceptance criterion was silently ignored: %v", err)
	}
}

func TestBlueprintRequirementTraceUsesOnlyExactBaselineIDs(t *testing.T) {
	t.Parallel()

	trace := semanticTraceFixture(t)
	if err := validateBlueprintRequirementTrace(json.RawMessage(`{"schemaVersion":1,"nodes":[],"edges":[]}`), trace); err != nil {
		t.Fatalf("empty workflow Blueprint target must remain initializable: %v", err)
	}
	valid := semanticBlueprintFixture(t, []string{"REQ-A"})
	if err := validateBlueprintRequirementTrace(valid, trace); err != nil {
		t.Fatalf("exact Requirement Baseline ID was rejected: %v", err)
	}
	for name, requirementIDs := range map[string][]string{
		"forged": {"REQ-BOGUS"},
		"mixed":  {"REQ-A", "REQ-BOGUS"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateBlueprintRequirementTrace(semanticBlueprintFixture(t, requirementIDs), trace); err == nil || !strings.Contains(err.Error(), "REQ-BOGUS") {
				t.Fatalf("Blueprint accepted non-baseline requirement IDs: %v", err)
			}
		})
	}

	historicalTrace, err := decodeRequirementTrace(json.RawMessage(`{
		"requirements":[
			{"type":"requirement","requirementId":"REQ-CURRENT","acceptanceCriterionIds":["AC-CURRENT"]},
			{"type":"acceptanceCriterion","acceptanceCriterionId":"AC-CURRENT"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBlueprintRequirementTrace(semanticBlueprintFixture(t, []string{"REQ-A"}), historicalTrace); err == nil {
		t.Fatal("an ID found only in a historical baseline satisfied the current baseline trace")
	}
}

func TestBlueprintStrictTraceCoversEveryMustRequirementAcrossAllNodeKinds(t *testing.T) {
	t.Parallel()
	trace, err := decodeRequirementTrace(json.RawMessage(`{
  "requirements":[
    {"type":"requirement","requirementId":"REQ-PAGE","priority":"must","acceptanceCriterionIds":["AC-PAGE"]},
    {"type":"requirement","requirementId":"REQ-API","priority":"must","acceptanceCriterionIds":["AC-API"]},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-PAGE"},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-API"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{
  "nodes":[
    {"id":"feature","key":"FEATURE","kind":"feature","requirementIds":["REQ-API"]},
    {"id":"page","key":"PAGE","kind":"page","title":"Page","route":"/page","userGoal":"Use page","requirementIds":["REQ-PAGE"]}
  ],
  "edges":[{"id":"contains","sourceNodeId":"feature","targetNodeId":"page","kind":"contains"}]
}`)
	if err := validateBlueprintRequirementTrace(payload, trace, true); err != nil {
		t.Fatalf("global Must coverage across non-Page nodes was rejected: %v", err)
	}
	missing := json.RawMessage(`{
  "nodes":[
    {"id":"feature","key":"FEATURE","kind":"feature"},
    {"id":"page","key":"PAGE","kind":"page","title":"Page","route":"/page","userGoal":"Use page","requirementIds":["REQ-PAGE"]}
  ],
  "edges":[{"id":"contains","sourceNodeId":"feature","targetNodeId":"page","kind":"contains"}]
}`)
	if err := validateBlueprintRequirementTrace(missing, trace, true); err == nil || !strings.Contains(err.Error(), "REQ-API") {
		t.Fatalf("strict Blueprint omitted a Must requirement: %v", err)
	}
	forged := json.RawMessage(`{
  "nodes":[
    {"id":"feature","key":"FEATURE","kind":"feature","requirementIds":["REQ-FORGED"]},
    {"id":"page","key":"PAGE","kind":"page","title":"Page","route":"/page","userGoal":"Use page","requirementIds":["REQ-PAGE"]}
  ],
  "edges":[{"id":"contains","sourceNodeId":"feature","targetNodeId":"page","kind":"contains"}]
}`)
	if err := validateBlueprintRequirementTrace(forged, trace); err == nil || !strings.Contains(err.Error(), "REQ-FORGED") {
		t.Fatalf("non-Page node accepted a forged requirement ID: %v", err)
	}
}

func TestValidateBlueprintAgainstRequirementBaselineRejectsMalformedBaseline(t *testing.T) {
	t.Parallel()
	err := ValidateBlueprintAgainstRequirementBaseline(
		semanticBlueprintFixture(t, []string{"REQ-A"}),
		json.RawMessage(`{"requirements":[`),
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "decode Requirement Baseline trace") {
		t.Fatalf("malformed Requirement Baseline lacked trace context: %v", err)
	}
}

func TestValidateBlueprintAgainstRequirementBaselineAcceptsExactIDs(t *testing.T) {
	t.Parallel()
	baseline := requirementBaselineTraceFixture()
	if err := ValidateBlueprintAgainstRequirementBaseline(
		semanticBlueprintFixture(t, []string{"REQ-A", "REQ-B"}), baseline, true,
	); err != nil {
		t.Fatalf("exact Requirement Baseline IDs were rejected: %v", err)
	}
}

func TestValidateBlueprintAgainstRequirementBaselineRejectsUnknownID(t *testing.T) {
	t.Parallel()
	err := ValidateBlueprintAgainstRequirementBaseline(
		semanticBlueprintFixture(t, []string{"REQ-A", "REQ-UNKNOWN"}),
		requirementBaselineTraceFixture(),
		false,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "validate Blueprint against Requirement Baseline trace") ||
		!strings.Contains(err.Error(), "REQ-UNKNOWN") {
		t.Fatalf("unknown Requirement Baseline ID lacked trace context: %v", err)
	}
}

func TestValidateBlueprintAgainstRequirementBaselineRequiresMustCoverage(t *testing.T) {
	t.Parallel()
	blueprint := semanticBlueprintFixture(t, []string{"REQ-A"})
	baseline := requirementBaselineTraceFixture()
	if err := ValidateBlueprintAgainstRequirementBaseline(blueprint, baseline, false); err != nil {
		t.Fatalf("draft trace unexpectedly required full Must coverage: %v", err)
	}
	err := ValidateBlueprintAgainstRequirementBaseline(blueprint, baseline, true)
	if err == nil ||
		!strings.Contains(err.Error(), "validate Blueprint against Requirement Baseline trace") ||
		!strings.Contains(err.Error(), "REQ-B") {
		t.Fatalf("strict trace accepted missing Must coverage: %v", err)
	}
}

func TestValidateExactSemanticAuthorityUsesFrozenStrictTrace(t *testing.T) {
	t.Parallel()

	input := exactSemanticAuthorityFixture(t)
	authority, err := ValidateExactSemanticAuthority(input)
	if err != nil {
		t.Fatalf("exact semantic authority was rejected: %v", err)
	}
	if authority.PageNodeID != "page-a" || len(authority.OwnedAPIOperations) != 1 {
		t.Fatalf("authority = %#v", authority)
	}
	operation := authority.OwnedAPIOperations[0]
	if operation.ID != "api-a" || operation.Method != "GET" || operation.Path != "/api/a" {
		t.Fatalf("owned operation = %#v", operation)
	}

	schemaInvalid := input
	schemaInvalid.Prototype = exactSemanticArtifactFixture(t, "prototype", "prototype-schema-invalid", json.RawMessage(`{}`))
	_, err = ValidateExactSemanticAuthority(schemaInvalid)
	var schemaError *ExactSemanticAuthorityError
	if !errors.As(err, &schemaError) || schemaError.Code != "prototype_schema_invalid" {
		t.Fatalf("standalone authority skipped canonical Prototype schema: %v", err)
	}

	hashDrift := input
	hashDrift.Blueprint.Payload = semanticBlueprintFixture(t, []string{"REQ-A"})
	_, err = ValidateExactSemanticAuthority(hashDrift)
	var hashError *ExactSemanticAuthorityError
	if !errors.As(err, &hashError) || hashError.Code != "semantic_revision_invalid" {
		t.Fatalf("standalone authority accepted payload/hash drift: %v", err)
	}

	var prototype map[string]any
	if err := json.Unmarshal(input.Prototype.Payload, &prototype); err != nil {
		t.Fatal(err)
	}
	trace := prototype["traceLinks"].([]any)[0].(map[string]any)
	trace["source"].(map[string]any)["version"].(map[string]any)["revisionId"] = "page-spec-r2"
	mutated, err := json.Marshal(prototype)
	if err != nil {
		t.Fatal(err)
	}
	input.Prototype = exactSemanticArtifactFixture(t, "prototype", "prototype-r2", mutated)
	_, err = ValidateExactSemanticAuthority(input)
	var authorityError *ExactSemanticAuthorityError
	if !errors.As(err, &authorityError) || authorityError.Code != "prototype_semantic_trace_invalid" {
		t.Fatalf("wrong pinned Prototype trace was not precisely rejected: %v", err)
	}
}

func TestPageSpecSemanticTraceRejectsCrossPageAndForgedReferences(t *testing.T) {
	t.Parallel()

	trace := semanticTraceFixture(t)
	nodes, edges, err := DecodeBlueprintSemanticGraph(semanticBlueprintFixture(t, []string{"REQ-A"}))
	if err != nil {
		t.Fatal(err)
	}
	page := findSemanticPage(t, nodes, "page-a")
	valid := json.RawMessage(`{
		"blueprintPageNodeId":"page-a","title":"Page A","route":"/a","userGoal":"Use Page A",
		"acceptanceCriterionIds":["AC-A"],
		"acceptanceRefs":[{"acceptanceCriterionId":"AC-A"}],
		"states":[{"id":"state-ready","key":"ready","acceptanceCriterionIds":["AC-A"]}],
		"interactions":[{"id":"interaction-a","trigger":"click","outcome":"Show A","acceptanceCriterionIds":["AC-A"]}],
		"dataBindings":[{"id":"binding-a","name":"A","source":"api","operationId":"api-a","required":true}]
	}`)
	if err := validatePageSpecSemanticTrace(valid, page, nodes, edges, trace); err != nil {
		t.Fatalf("legal PageSpec semantic trace was rejected: %v", err)
	}
	strictValid := json.RawMessage(`{
    "title":"Page A","route":"/a","userGoal":"Use Page A","acceptanceCriterionIds":["AC-A"],
    "requiredRoles":["PERMISSION-A"],
    "dataBindings":[{"source":"api","operationId":"api-a"}],"interactions":[]
  }`)
	if err := validatePageSpecSemanticTrace(strictValid, page, nodes, edges, trace, true); err != nil {
		t.Fatalf("API Permission role did not propagate to the owning Page: %v", err)
	}
	strictWrongTitle := json.RawMessage(strings.Replace(string(strictValid), `"title":"Page A"`, `"title":"Wrong title"`, 1))
	if err := validatePageSpecSemanticTrace(strictWrongTitle, page, nodes, edges, trace, true); err == nil {
		t.Fatal("formal PageSpec accepted a title that differs from its Blueprint Page")
	}
	if err := validatePageSpecSemanticTrace(strictWrongTitle, page, nodes, edges, trace); err != nil {
		t.Fatalf("editable PageSpec draft rejected a provisional title: %v", err)
	}
	if err := validatePageSpecSemanticTrace(json.RawMessage(`{"blueprintPageNodeId":"page-a","route":"","userGoal":"","states":[],"interactions":[],"dataBindings":[]}`), page, nodes, edges, trace); err != nil {
		t.Fatalf("partial PageSpec draft without semantic references must remain editable: %v", err)
	}
	if err := validatePageSpecSemanticTrace(
		json.RawMessage(`{"blueprintPageNodeId":"page-a","route":"","userGoal":"","acceptanceCriterionIds":["AC-A"],"requiredRoles":["PERMISSION-A"],"dataBindings":[{"source":"api","operationId":"api-a"}]}`),
		page, nodes, edges, trace, true,
	); err == nil {
		t.Fatal("formal PageSpec accepted missing route/userGoal instead of the exact Blueprint Page values")
	}

	for name, payload := range map[string]json.RawMessage{
		"wrong_route":             json.RawMessage(`{"route":"/wrong"}`),
		"wrong_user_goal":         json.RawMessage(`{"userGoal":"Wrong goal"}`),
		"forged_acceptance":       json.RawMessage(`{"acceptanceCriterionIds":["AC-BOGUS"]}`),
		"mixed_acceptance":        json.RawMessage(`{"acceptanceCriterionIds":["AC-A","AC-BOGUS"]}`),
		"cross_page_state":        json.RawMessage(`{"states":[{"acceptanceCriterionIds":["AC-B"]}]}`),
		"cross_page_interaction":  json.RawMessage(`{"interactions":[{"acceptanceCriterionIds":["AC-B"]}]}`),
		"missing_operation":       json.RawMessage(`{"dataBindings":[{"source":"api"}]}`),
		"forged_operation":        json.RawMessage(`{"dataBindings":[{"source":"api","operationId":"api-bogus"}]}`),
		"local_operation_smuggle": json.RawMessage(`{"dataBindings":[{"source":"local","operationId":"api-a"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePageSpecSemanticTrace(payload, page, nodes, edges, trace); err == nil {
				t.Fatalf("PageSpec accepted invalid semantic trace: %s", payload)
			}
		})
	}

	unprotectedEdges := make([]BlueprintSemanticEdge, 0, len(edges))
	for _, edge := range edges {
		if edge.Kind != "requires" {
			unprotectedEdges = append(unprotectedEdges, edge)
		}
	}
	if err := validatePageSpecSemanticTrace(
		json.RawMessage(`{"dataBindings":[{"source":"api","operationId":"api-a"}]}`),
		page,
		nodes,
		unprotectedEdges,
		trace,
	); err == nil {
		t.Fatal("PageSpec accepted an API operation without its Blueprint permission edge")
	}
}

func TestPageSpecStrictTraceRequiresEveryPageAcceptanceCriterion(t *testing.T) {
	t.Parallel()
	trace, err := decodeRequirementTrace(json.RawMessage(`{
  "requirements":[
    {"type":"requirement","requirementId":"REQ-A","acceptanceCriterionIds":["AC-A1","AC-A2"]},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-A1"},
    {"type":"acceptanceCriterion","acceptanceCriterionId":"AC-A2"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges, err := DecodeBlueprintSemanticGraph(semanticBlueprintFixture(t, []string{"REQ-A"}))
	if err != nil {
		t.Fatal(err)
	}
	page := findSemanticPage(t, nodes, "page-a")
	partial := json.RawMessage(`{
    "title":"Page A","route":"/a","userGoal":"Use Page A","acceptanceCriterionIds":["AC-A1"],
    "requiredRoles":["PERMISSION-A"],"dataBindings":[{"source":"api","operationId":"api-a"}]
  }`)
	if err := validatePageSpecSemanticTrace(partial, page, nodes, edges, trace); err != nil {
		t.Fatalf("membership-valid draft should stay editable: %v", err)
	}
	if err := validatePageSpecSemanticTrace(partial, page, nodes, edges, trace, true); err == nil || !strings.Contains(err.Error(), "AC-A2") {
		t.Fatalf("formal PageSpec omitted a page acceptance criterion: %v", err)
	}
}

func TestBlueprintPageRelationsPropagateAPIPermissionRolesAndRespectOptionalPageRole(t *testing.T) {
	t.Parallel()
	nodes, edges, err := DecodeBlueprintSemanticGraph(semanticBlueprintFixture(t, []string{"REQ-A"}))
	if err != nil {
		t.Fatal(err)
	}
	nodes = append(nodes, BlueprintSemanticNode{ID: "permission-optional", Key: "PERMISSION-OPTIONAL", Kind: "permission", Roles: []string{"viewer"}})
	edges = append(edges, BlueprintSemanticEdge{ID: "optional-page-role", SourceID: "page-a", TargetID: "permission-optional", Kind: "requires", Required: false})
	relations := blueprintPageRelations(findSemanticPage(t, nodes, "page-a"), nodes, edges)
	if !relations.requiredRoles["PERMISSION-A"] {
		t.Fatal("API -> Permission role was not propagated through the Page -> API call")
	}
	if !relations.allowedRoles["viewer"] || relations.requiredRoles["viewer"] {
		t.Fatalf("optional Page -> Permission role became required: allowed=%v required=%v", relations.allowedRoles, relations.requiredRoles)
	}
}

func TestPrototypeSemanticTraceSeparatesDraftIdentityFromReviewCoverage(t *testing.T) {
	t.Parallel()

	pageSpec := json.RawMessage(`{
		"states":[
			{"id":"state-ready","key":"ready","required":true,"fixtureIds":["fixture-ready"]},
			{"id":"state-loading","key":"loading","required":true,"fixtureIds":[]},
			{"id":"state-empty","key":"empty","required":true,"fixtureIds":[]},
			{"id":"state-error","key":"error","required":true,"fixtureIds":[]}
		],
		"interactions":[{"id":"interaction-a","trigger":"click","outcome":"Open details"}],
		"dataBindings":[{"id":"binding-a","source":"api","required":true}]
	}`)
	emptyTarget := json.RawMessage(`{"states":[],"layers":{},"interactions":[],"fixtures":[]}`)
	if err := validatePrototypeSemanticTrace(emptyTarget, pageSpec, false); err != nil {
		t.Fatalf("empty Prototype workflow target must remain initializable: %v", err)
	}
	if err := validatePrototypeSemanticTrace(emptyTarget, pageSpec, true); err == nil {
		t.Fatal("empty Prototype target passed strict review coverage")
	}

	valid := json.RawMessage(`{
		"states":[
			{"id":"state-ready","key":"ready","required":true,"fixtureIds":["fixture-ready"],"pageStateId":"state-ready"},
			{"id":"state-loading","key":"loading","required":true,"fixtureIds":[]},
			{"id":"state-empty","key":"empty","required":true,"fixtureIds":[]},
			{"id":"state-error","key":"error","required":true,"fixtureIds":[]}
		],
		"breakpoints":[{"id":"desktop","name":"desktop"},{"id":"tablet","name":"tablet"},{"id":"mobile","name":"mobile"}],
		"layers":{"layer-a":{"id":"layer-a","kind":"frame","dataBindingId":"binding-a"}},
		"frames":[
			{"id":"ready-desktop","stateId":"state-ready","breakpointId":"desktop","rootLayerId":"layer-a"},
			{"id":"ready-tablet","stateId":"state-ready","breakpointId":"tablet","rootLayerId":"layer-a"},
			{"id":"ready-mobile","stateId":"state-ready","breakpointId":"mobile","rootLayerId":"layer-a"},
			{"id":"loading-desktop","stateId":"state-loading","breakpointId":"desktop","rootLayerId":"layer-a"},
			{"id":"loading-tablet","stateId":"state-loading","breakpointId":"tablet","rootLayerId":"layer-a"},
			{"id":"loading-mobile","stateId":"state-loading","breakpointId":"mobile","rootLayerId":"layer-a"},
			{"id":"empty-desktop","stateId":"state-empty","breakpointId":"desktop","rootLayerId":"layer-a"},
			{"id":"empty-tablet","stateId":"state-empty","breakpointId":"tablet","rootLayerId":"layer-a"},
			{"id":"empty-mobile","stateId":"state-empty","breakpointId":"mobile","rootLayerId":"layer-a"},
			{"id":"error-desktop","stateId":"state-error","breakpointId":"desktop","rootLayerId":"layer-a"},
			{"id":"error-tablet","stateId":"state-error","breakpointId":"tablet","rootLayerId":"layer-a"},
			{"id":"error-mobile","stateId":"state-error","breakpointId":"mobile","rootLayerId":"layer-a"}
		],
		"fixtures":[{"id":"fixture-ready","name":"Ready response","stateId":"state-ready","response":{},"statusCode":200,"latencyMs":0,"sanitized":true,"contentHash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
		"interactions":[{"id":"interaction-a","sourceLayerId":"layer-a","trigger":"click","actions":[{"type":"updateBinding","bindingId":"binding-a"}]}]
	}`)
	if err := validatePrototypeSemanticTrace(valid, pageSpec, true); err != nil {
		t.Fatalf("legal Prototype semantic coverage was rejected: %v", err)
	}

	for name, testCase := range map[string]struct {
		payload json.RawMessage
		strict  bool
	}{
		"missing_state_at_review": {
			payload: json.RawMessage(`{"states":[{"id":"state-ready","key":"ready"}],"fixtures":[],"interactions":[],"layers":{}}`), strict: true,
		},
		"same_key_different_id": {
			payload: json.RawMessage(`{"states":[{"id":"state-forged","key":"ready"}],"fixtures":[],"interactions":[],"layers":{}}`), strict: false,
		},
		"same_id_different_key": {
			payload: json.RawMessage(`{"states":[{"id":"state-ready","key":"forged"}],"fixtures":[],"interactions":[],"layers":{}}`), strict: false,
		},
		"fixture_wrong_state": {
			payload: json.RawMessage(`{"states":[],"fixtures":[{"id":"fixture-ready","stateId":"state-error"}],"interactions":[],"layers":{}}`), strict: false,
		},
		"missing_fixture_at_review": {
			payload: json.RawMessage(`{"states":[{"id":"state-ready","key":"ready"},{"id":"state-loading","key":"loading"},{"id":"state-empty","key":"empty"},{"id":"state-error","key":"error"}],"fixtures":[],"interactions":[{"id":"interaction-a","trigger":"click"}],"layers":{"layer-a":{"dataBindingId":"binding-a"}}}`), strict: true,
		},
		"wrong_interaction_trigger": {
			payload: json.RawMessage(`{"states":[],"fixtures":[],"interactions":[{"id":"interaction-a","trigger":"submit"}],"layers":{}}`), strict: false,
		},
		"wrong_interaction_id_at_review": {
			payload: json.RawMessage(`{"states":[{"id":"state-ready","key":"ready"},{"id":"state-loading","key":"loading"},{"id":"state-empty","key":"empty"},{"id":"state-error","key":"error"}],"fixtures":[{"id":"fixture-ready","stateId":"state-ready"}],"interactions":[{"id":"interaction-bogus","trigger":"click"}],"layers":{"layer-a":{"dataBindingId":"binding-a"}}}`), strict: true,
		},
		"missing_binding_at_review": {
			payload: json.RawMessage(`{"states":[{"id":"state-ready","key":"ready"},{"id":"state-loading","key":"loading"},{"id":"state-empty","key":"empty"},{"id":"state-error","key":"error"}],"fixtures":[{"id":"fixture-ready","stateId":"state-ready"}],"interactions":[{"id":"interaction-a","trigger":"click"}],"layers":{}}`), strict: true,
		},
		"unknown_binding_in_draft": {
			payload: json.RawMessage(`{"states":[],"fixtures":[],"interactions":[],"layers":{"layer-a":{"dataBindingId":"binding-bogus"}}}`), strict: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePrototypeSemanticTrace(testCase.payload, pageSpec, testCase.strict); err == nil {
				t.Fatalf("Prototype accepted invalid semantic trace: %s", testCase.payload)
			}
		})
	}
}

func TestPrototypeTraceLinksAreDirectionalAndVersionPinned(t *testing.T) {
	t.Parallel()
	baseline := VersionRef{ArtifactID: "baseline", RevisionID: "baseline-r1", ContentHash: "sha256:baseline"}
	anchor := "page-a"
	authority := prototypeSemanticAuthority{
		requirementIDs: map[string]bool{"REQ-A": true}, acceptanceIDs: map[string]bool{"AC-A": true},
		pageNodeID: "page-a", pageSpecArtifactID: "page-spec",
		baselineRef:  baseline,
		blueprintRef: VersionRef{ArtifactID: "blueprint", RevisionID: "blueprint-r1", ContentHash: "sha256:blueprint", AnchorID: &anchor},
		pageSpecRef:  VersionRef{ArtifactID: "page-spec", RevisionID: "page-spec-r1", ContentHash: "sha256:page-spec"},
	}
	layers := map[string]map[string]any{"layer-a": {"id": "layer-a"}}
	interactions := map[string]map[string]any{"interaction-a": {"id": "interaction-a"}}
	version := map[string]any{"artifactId": baseline.ArtifactID, "revisionId": baseline.RevisionID, "contentHash": baseline.ContentHash}
	valid := map[string]any{"traceLinks": []any{map[string]any{
		"id": "trace-a", "relation": "implements",
		"source": map[string]any{"kind": "requirement", "id": "REQ-A", "version": version},
		"target": map[string]any{"kind": "prototypeLayer", "id": "layer-a"},
	}}}
	if err := validatePrototypeTraceLinks(valid, authority, layers, interactions); err != nil {
		t.Fatalf("exact upstream-to-Prototype trace link was rejected: %v", err)
	}
	wrongVersion := cloneSemanticMap(t, valid)
	wrongVersion["traceLinks"].([]any)[0].(map[string]any)["source"].(map[string]any)["version"].(map[string]any)["revisionId"] = "baseline-r2"
	if err := validatePrototypeTraceLinks(wrongVersion, authority, layers, interactions); err == nil {
		t.Fatal("trace link accepted a non-authoritative upstream version")
	}
	reversed := map[string]any{"traceLinks": []any{map[string]any{
		"id": "trace-reversed", "relation": "implements",
		"source": map[string]any{"kind": "prototypeLayer", "id": "layer-a"},
		"target": map[string]any{"kind": "requirement", "id": "REQ-A"},
	}}}
	if err := validatePrototypeTraceLinks(reversed, authority, layers, interactions); err == nil {
		t.Fatal("trace link accepted a Prototype-to-upstream reversed direction")
	}
}

func cloneSemanticMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func semanticTraceFixture(t *testing.T) requirementTraceSnapshot {
	t.Helper()
	trace, err := decodeRequirementTrace(json.RawMessage(`{
		"requirements":[
			{"type":"requirement","requirementId":"REQ-A","acceptanceCriterionIds":["AC-A"]},
			{"type":"requirement","requirementId":"REQ-B","acceptanceCriterionIds":["AC-B"]},
			{"type":"acceptanceCriterion","acceptanceCriterionId":"AC-A"},
			{"type":"acceptanceCriterion","acceptanceCriterionId":"AC-B"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func requirementBaselineTraceFixture() json.RawMessage {
	return json.RawMessage(`{
		"requirements":[
			{"type":"requirement","requirementId":"REQ-A","priority":"must","acceptanceCriterionIds":["AC-A"]},
			{"type":"requirement","requirementId":"REQ-B","priority":"must","acceptanceCriterionIds":["AC-B"]},
			{"type":"acceptanceCriterion","acceptanceCriterionId":"AC-A"},
			{"type":"acceptanceCriterion","acceptanceCriterionId":"AC-B"}
		]
	}`)
}

func exactSemanticAuthorityFixture(t *testing.T) ExactSemanticAuthorityInput {
	t.Helper()
	baseline := exactSemanticArtifactFixture(t, "baseline", "baseline-r1", canonicalExactBaselineFixture(t))
	blueprint := exactSemanticArtifactFixture(t, "blueprint", "blueprint-r1", semanticBlueprintFixture(t, []string{"REQ-A", "REQ-B"}))
	pageSpecPayload := json.RawMessage(`{
    "blueprintPageNodeId":"page-a","title":"Page A","route":"/a","userGoal":"Use Page A",
    "acceptanceCriterionIds":["AC-A","AC-B"],"requiredRoles":["PERMISSION-A"],
    "states":[
      {"id":"state-ready","key":"ready","title":"Ready","required":true,"fixtureIds":[]},
      {"id":"state-loading","key":"loading","title":"Loading","required":true,"fixtureIds":[]},
      {"id":"state-empty","key":"empty","title":"Empty","required":true,"fixtureIds":[]},
      {"id":"state-error","key":"error","title":"Error","required":true,"fixtureIds":[]}
    ],
    "dataBindings":[{"id":"binding-a","name":"A","source":"api","operationId":"api-a","required":true}],
    "interactions":[]
  }`)
	pageSpec := exactSemanticArtifactFixture(t, "page-spec", "page-spec-r1", pageSpecPayload)
	states := []map[string]any{
		{"id": "state-ready", "key": "ready", "title": "Ready", "required": true, "fixtureIds": []string{}},
		{"id": "state-loading", "key": "loading", "title": "Loading", "required": true, "fixtureIds": []string{}},
		{"id": "state-empty", "key": "empty", "title": "Empty", "required": true, "fixtureIds": []string{}},
		{"id": "state-error", "key": "error", "title": "Error", "required": true, "fixtureIds": []string{}},
	}
	breakpoints := []map[string]any{
		{"id": "desktop", "name": "desktop", "minWidth": 1024, "viewportWidth": 1440, "viewportHeight": 900},
		{"id": "tablet", "name": "tablet", "minWidth": 768, "maxWidth": 1023, "viewportWidth": 768, "viewportHeight": 1024},
		{"id": "mobile", "name": "mobile", "minWidth": 0, "maxWidth": 767, "viewportWidth": 390, "viewportHeight": 844},
	}
	frames := make([]map[string]any, 0, len(states)*len(breakpoints))
	for _, state := range states {
		for _, breakpoint := range breakpoints {
			stateID, breakpointID := state["id"].(string), breakpoint["id"].(string)
			frames = append(frames, map[string]any{
				"id": stateID + "-" + breakpointID, "title": stateID + " " + breakpointID,
				"stateId": stateID, "breakpointId": breakpointID, "rootLayerId": "layer-a",
			})
		}
	}
	pageSpecVersion := map[string]any{
		"artifactId": pageSpec.Revision.ArtifactID, "revisionId": pageSpec.Revision.RevisionID,
		"contentHash": pageSpec.Revision.ContentHash,
	}
	prototypePayload, err := json.Marshal(map[string]any{
		"pageSpecRevision": pageSpecVersion, "exploratory": false,
		"states": states, "breakpoints": breakpoints,
		"layers": map[string]any{
			"layer-a": map[string]any{
				"id": "layer-a", "kind": "frame", "name": "Page A", "childIds": []string{}, "dataBindingId": "binding-a",
				"layout": map[string]any{"x": 0, "y": 0, "width": 1440, "height": 900},
				"style":  map[string]any{}, "properties": map[string]any{}, "fieldMetadata": map[string]any{},
				"requirementIds": []string{"REQ-A", "REQ-B"}, "acceptanceCriterionIds": []string{"AC-A", "AC-B"},
			},
		},
		"frames": frames, "overrides": []any{}, "fixtures": []any{}, "interactions": []any{},
		"tokenBindings": []any{}, "componentBindings": []any{}, "assets": []any{},
		"traceLinks": []any{map[string]any{
			"id": "trace-page-spec", "relation": "implements",
			"source": map[string]any{"kind": "pageSpec", "id": pageSpec.Revision.ArtifactID, "version": pageSpecVersion},
			"target": map[string]any{"kind": "prototypeLayer", "id": "layer-a"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prototype := exactSemanticArtifactFixture(t, "prototype", "prototype-r1", prototypePayload)
	return ExactSemanticAuthorityInput{
		RequirementBaseline: baseline, Blueprint: blueprint, PageSpec: pageSpec, Prototype: prototype,
	}
}

func canonicalExactBaselineFixture(t *testing.T) json.RawMessage {
	t.Helper()
	var baseline map[string]any
	if err := json.Unmarshal(requirementBaselineTraceFixture(), &baseline); err != nil {
		t.Fatal(err)
	}
	for _, raw := range baseline["requirements"].([]any) {
		item := raw.(map[string]any)
		if item["type"] == "requirement" {
			item["statement"] = "Implement " + item["requirementId"].(string)
		} else {
			item["statement"] = "Verify " + item["acceptanceCriterionId"].(string)
		}
	}
	baseline["sourceVersions"] = []any{map[string]any{
		"artifactId": "requirements", "revisionId": "requirements-r1",
		"contentHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	baseline["baselineHash"] = ""
	hash, err := domain.CanonicalHash(baseline)
	if err != nil {
		t.Fatal(err)
	}
	baseline["baselineHash"] = hash
	payload, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func exactSemanticArtifactFixture(t *testing.T, artifactID, revisionID string, payload json.RawMessage) ExactSemanticArtifact {
	t.Helper()
	hash, err := domain.CanonicalHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	return ExactSemanticArtifact{
		Payload:  payload,
		Revision: VersionRef{ArtifactID: artifactID, RevisionID: revisionID, ContentHash: hash},
	}
}

func semanticBlueprintFixture(t *testing.T, requirementIDs []string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"nodes": []any{
			map[string]any{"id": "feature-a", "key": "FEATURE-A", "kind": "feature", "title": "Feature A"},
			map[string]any{
				"id": "page-a", "key": "PAGE-A", "kind": "page", "title": "Page A",
				"route": "/a", "userGoal": "Use Page A", "requirementIds": requirementIDs,
			},
			map[string]any{"id": "api-a", "key": "API-A", "kind": "apiOperation", "method": "GET", "path": "/api/a"},
			map[string]any{"id": "permission-a", "key": "PERMISSION-A", "kind": "permission"},
		},
		"edges": []any{
			map[string]any{"id": "contains-a", "sourceNodeId": "feature-a", "targetNodeId": "page-a", "kind": "contains"},
			map[string]any{"id": "calls-a", "sourceNodeId": "page-a", "targetNodeId": "api-a", "kind": "calls", "required": true},
			map[string]any{"id": "protect-a", "sourceNodeId": "api-a", "targetNodeId": "permission-a", "kind": "requires"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func findSemanticPage(t *testing.T, nodes []BlueprintSemanticNode, id string) BlueprintSemanticNode {
	t.Helper()
	for _, node := range nodes {
		if node.ID == id && node.Kind == "page" {
			return node
		}
	}
	t.Fatalf("missing semantic Page %s", id)
	return BlueprintSemanticNode{}
}
