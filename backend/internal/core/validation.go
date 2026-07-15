package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

type ValidationFinding struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type ValidationReport struct {
	Valid    bool                `json:"valid"`
	Findings []ValidationFinding `json:"findings"`
}

func ValidateArtifactContent(kind string, payload json.RawMessage) ValidationReport {
	findings := make([]ValidationFinding, 0)
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return ValidationReport{Findings: []ValidationFinding{{
			Code: "invalid_json", Path: "$", Message: "Artifact content must be a JSON object.", Severity: "blocker",
		}}}
	}
	switch kind {
	case "project_brief":
		findings = append(findings, validateProjectBrief(value)...)
	case "product_requirements":
		findings = append(findings, validateRequirements(value)...)
	case "requirement_baseline":
		findings = append(findings, validateRequirementBaseline(value)...)
	case "blueprint":
		findings = append(findings, validateBlueprint(value)...)
		if _, _, err := DecodeBlueprintSemanticGraph(payload); err != nil {
			findings = append(findings, blocker("blueprint.application_pages", "$.nodes", err.Error()))
		}
	case "page_spec":
		findings = append(findings, validatePageSpec(value)...)
	case "prototype":
		findings = append(findings, validatePrototype(value)...)
	}
	valid := true
	for _, finding := range findings {
		if finding.Severity == "blocker" {
			valid = false
			break
		}
	}
	return ValidationReport{Valid: valid, Findings: findings}
}

func validateRequirementBaseline(value map[string]any) []ValidationFinding {
	findings := make([]ValidationFinding, 0)
	sources := objectSlice(value["sourceVersions"])
	if len(sources) == 0 {
		findings = append(findings, blocker("baseline.sources_required", "$.sourceVersions", "Requirement Baseline must pin at least one approved source revision."))
	}
	for index, source := range sources {
		if !validVersionReference(source) {
			findings = append(findings, blocker("baseline.invalid_source", fmt.Sprintf("$.sourceVersions[%d]", index), "Baseline sources require artifactId, revisionId, and contentHash."))
		}
	}
	criteria := map[string]struct{}{}
	requirements := make([]map[string]any, 0)
	anchors := map[string]struct{}{}
	for index, item := range objectSlice(value["requirements"]) {
		kind := firstString(item, "type")
		anchor := firstString(item, "requirementId", "acceptanceCriterionId", "key", "id")
		if anchor == "" || strings.TrimSpace(firstString(item, "statement")) == "" {
			findings = append(findings, blocker("baseline.invalid_requirement_fact", fmt.Sprintf("$.requirements[%d]", index), "Every baseline requirement fact needs a stable anchor and statement."))
			continue
		}
		if _, duplicate := anchors[anchor]; duplicate {
			findings = append(findings, blocker("baseline.duplicate_anchor", fmt.Sprintf("$.requirements[%d]", index), "Baseline requirement and acceptance anchors must be unique."))
		}
		anchors[anchor] = struct{}{}
		switch kind {
		case "requirement":
			requirements = append(requirements, item)
		case "acceptanceCriterion":
			criteria[anchor] = struct{}{}
		default:
			findings = append(findings, blocker("baseline.invalid_requirement_type", fmt.Sprintf("$.requirements[%d].type", index), "Baseline requirement facts must be requirement or acceptanceCriterion."))
		}
	}
	if len(requirements) == 0 {
		findings = append(findings, blocker("baseline.requirement_required", "$.requirements", "Requirement Baseline must contain at least one requirement."))
	}
	for index, requirement := range requirements {
		links := stringSlice(requirement["acceptanceCriterionIds"])
		if strings.EqualFold(firstString(requirement, "priority"), "must") && len(links) == 0 {
			findings = append(findings, blocker("baseline.must_has_ac", fmt.Sprintf("$.requirements[%d].acceptanceCriterionIds", index), "Every Must baseline requirement needs an acceptance criterion."))
		}
		for linkIndex, link := range links {
			if _, exists := criteria[link]; !exists {
				findings = append(findings, blocker("baseline.ac_reference", fmt.Sprintf("$.requirements[%d].acceptanceCriterionIds[%d]", index, linkIndex), "Baseline requirement references an acceptance criterion that is not present."))
			}
		}
	}
	expectedHash := firstString(value, "baselineHash")
	if expectedHash == "" {
		findings = append(findings, blocker("baseline.hash_required", "$.baselineHash", "Requirement Baseline must include its deterministic hash."))
	} else {
		hashPayload := make(map[string]any, len(value))
		for key, field := range value {
			hashPayload[key] = field
		}
		hashPayload["baselineHash"] = ""
		actualHash, err := domain.CanonicalHash(hashPayload)
		if err != nil || actualHash != expectedHash {
			findings = append(findings, blocker("baseline.hash_mismatch", "$.baselineHash", "Requirement Baseline hash does not match its canonical content."))
		}
	}
	return findings
}

func validateProjectBrief(value map[string]any) []ValidationFinding {
	blocks := objectSlice(value["blocks"])
	findings := make([]ValidationFinding, 0)
	if strings.TrimSpace(firstString(value, "summary")) == "" {
		findings = append(findings, blocker("brief.summary_required", "$.summary", "Project Brief must summarize the problem and desired outcome."))
	}
	if len(blocks) == 0 {
		return append(findings, blocker("brief.blocks_required", "$.blocks", "Project Brief must contain structured blocks."))
	}
	goals := 0
	for index, block := range blocks {
		typeName, _ := block["type"].(string)
		if typeName == "goal" {
			if strings.TrimSpace(firstString(block, "text")) == "" {
				findings = append(findings, blocker(
					"brief.goal_text_required", fmt.Sprintf("$.blocks[%d].text", index),
					"Every Project Brief goal must contain a measurable outcome.",
				))
			} else {
				goals++
			}
		}
		if typeName == "openQuestion" && boolean(block["blocking"]) {
			status, _ := block["status"].(string)
			if status != "answered" && status != "resolved" && status != "waived" {
				findings = append(findings, blocker(
					"brief.blocking_question", fmt.Sprintf("$.blocks[%d]", index),
					"Blocking questions must be answered, resolved, or explicitly waived before approval.",
				))
			}
		}
	}
	if goals == 0 {
		findings = append(findings, blocker("brief.goal_required", "$.blocks", "Project Brief must define at least one non-empty goal."))
	}
	return findings
}

func validateRequirements(value map[string]any) []ValidationFinding {
	blocks := objectSlice(value["blocks"])
	findings := make([]ValidationFinding, 0)
	if strings.TrimSpace(firstString(value, "summary")) == "" {
		findings = append(findings, blocker("requirements.summary_required", "$.summary", "Requirements must summarize the intended product outcome."))
	}
	if len(blocks) == 0 {
		findings = append(findings, blocker("requirements.blocks_required", "$.blocks", "Requirements must contain structured blocks."))
	}
	for index, block := range blocks {
		if firstString(block, "type") == "openQuestion" && boolean(block["blocking"]) {
			status := firstString(block, "status")
			if status != "answered" && status != "resolved" && status != "waived" {
				findings = append(findings, blocker("requirements.blocking_question", fmt.Sprintf("$.blocks[%d]", index), "Blocking requirement questions must be resolved."))
			}
		}
	}
	structuredRequirements := objectSlice(value["requirements"])
	structuredCriteria := objectSlice(value["acceptanceCriteria"])
	if len(structuredRequirements) > 0 || len(structuredCriteria) > 0 {
		return append(findings, validateStructuredRequirements(blocks, structuredRequirements, structuredCriteria)...)
	}
	requirementIDs := map[string]struct{}{}
	acceptanceByRequirement := map[string]int{}
	for index, block := range blocks {
		typeName, _ := block["type"].(string)
		if typeName == "requirement" {
			identifier := firstString(block, "requirementId", "key", "id")
			if identifier == "" {
				findings = append(findings, blocker("requirements.stable_id", fmt.Sprintf("$.blocks[%d]", index), "Every requirement needs a stable ID."))
				continue
			}
			if _, duplicate := requirementIDs[identifier]; duplicate {
				findings = append(findings, blocker("requirements.duplicate_id", fmt.Sprintf("$.blocks[%d]", index), "Requirement IDs must be unique."))
			}
			requirementIDs[identifier] = struct{}{}
			acceptanceByRequirement[identifier] += len(objectSlice(block["acceptanceCriteria"]))
			if strings.EqualFold(firstString(block, "priority"), "must") && acceptanceByRequirement[identifier] == 0 {
				// A separate AC block may satisfy this later; final check runs below.
			}
		}
		if typeName == "acceptanceCriterion" {
			requirementID := firstString(block, "requirementId", "parentRequirementId")
			if requirementID != "" {
				acceptanceByRequirement[requirementID]++
			}
			if firstString(block, "acceptanceCriterionId", "key", "id") == "" {
				findings = append(findings, blocker("requirements.ac_stable_id", fmt.Sprintf("$.blocks[%d]", index), "Every acceptance criterion needs a stable ID."))
			}
		}
	}
	if len(requirementIDs) == 0 {
		findings = append(findings, blocker("requirements.requirement_required", "$.blocks", "At least one requirement is required."))
	}
	for index, block := range blocks {
		if firstString(block, "type") != "requirement" || !strings.EqualFold(firstString(block, "priority"), "must") {
			continue
		}
		identifier := firstString(block, "requirementId", "key", "id")
		if identifier != "" && acceptanceByRequirement[identifier] == 0 {
			findings = append(findings, blocker("requirements.must_has_ac", fmt.Sprintf("$.blocks[%d]", index), "Every Must requirement needs at least one acceptance criterion."))
		}
	}
	return findings
}

func validateStructuredRequirements(blocks, requirements, criteria []map[string]any) []ValidationFinding {
	findings := make([]ValidationFinding, 0)
	blockIDs := make(map[string]struct{}, len(blocks))
	for _, block := range blocks {
		if identifier := firstString(block, "id"); identifier != "" {
			blockIDs[identifier] = struct{}{}
		}
	}
	criterionIDs := make(map[string]struct{}, len(criteria))
	for index, criterion := range criteria {
		identifier := firstString(criterion, "id", "key", "acceptanceCriterionId")
		if identifier == "" || strings.TrimSpace(firstString(criterion, "statement")) == "" {
			findings = append(findings, blocker("requirements.invalid_ac", fmt.Sprintf("$.acceptanceCriteria[%d]", index), "Every acceptance criterion needs a stable ID and statement."))
			continue
		}
		if _, duplicate := criterionIDs[identifier]; duplicate {
			findings = append(findings, blocker("requirements.duplicate_ac_id", fmt.Sprintf("$.acceptanceCriteria[%d].id", index), "Acceptance criterion IDs must be unique."))
		}
		criterionIDs[identifier] = struct{}{}
	}
	if len(requirements) == 0 {
		findings = append(findings, blocker("requirements.requirement_required", "$.requirements", "At least one requirement is required."))
		return findings
	}
	requirementIDs := make(map[string]struct{}, len(requirements))
	for index, requirement := range requirements {
		identifier := firstString(requirement, "id", "key", "requirementId")
		if identifier == "" || strings.TrimSpace(firstString(requirement, "statement")) == "" {
			findings = append(findings, blocker("requirements.invalid_requirement", fmt.Sprintf("$.requirements[%d]", index), "Every requirement needs a stable ID and statement."))
			continue
		}
		if _, duplicate := requirementIDs[identifier]; duplicate {
			findings = append(findings, blocker("requirements.duplicate_id", fmt.Sprintf("$.requirements[%d].id", index), "Requirement IDs must be unique."))
		}
		requirementIDs[identifier] = struct{}{}
		linked := stringSlice(requirement["acceptanceCriterionIds"])
		if strings.EqualFold(firstString(requirement, "priority"), "must") && len(linked) == 0 {
			findings = append(findings, blocker("requirements.must_has_ac", fmt.Sprintf("$.requirements[%d].acceptanceCriterionIds", index), "Every Must requirement needs at least one acceptance criterion."))
		}
		for linkIndex, criterionID := range linked {
			if _, exists := criterionIDs[criterionID]; !exists {
				findings = append(findings, blocker("requirements.ac_reference", fmt.Sprintf("$.requirements[%d].acceptanceCriterionIds[%d]", index, linkIndex), "Requirement references an acceptance criterion that does not exist."))
			}
		}
		sources := stringSlice(requirement["sourceBlockIds"])
		if len(sources) == 0 {
			findings = append(findings, blocker("requirements.source_block_required", fmt.Sprintf("$.requirements[%d].sourceBlockIds", index), "Every requirement must trace to at least one source block."))
		}
		for sourceIndex, blockID := range sources {
			if _, exists := blockIDs[blockID]; !exists {
				findings = append(findings, blocker("requirements.source_block_reference", fmt.Sprintf("$.requirements[%d].sourceBlockIds[%d]", index, sourceIndex), "Requirement references a source block that does not exist."))
			}
		}
	}
	return findings
}

func validateBlueprint(value map[string]any) []ValidationFinding {
	if semantic, exists := value["semantic"].(map[string]any); exists {
		// Approval validates the same representation consumed by selection and
		// runtime fan-out. DecodeBlueprintSemanticGraph separately rejects drift
		// when both root and semantic representations are present.
		value = semantic
	}
	nodes := objectSlice(value["nodes"])
	edges := objectSlice(value["edges"])
	findings := make([]ValidationFinding, 0)
	if len(nodes) == 0 {
		return []ValidationFinding{blocker("blueprint.nodes_required", "$.nodes", "Blueprint must contain semantic nodes.")}
	}
	allowedNodes := map[string]bool{
		"feature": true, "page": true, "component": true, "apioperation": true,
		"api": true, "dataentity": true, "datamodel": true, "permission": true,
	}
	allowedEdges := map[string]bool{
		"drives": true, "satisfied_by": true, "contains": true, "navigates_to": true,
		"uses": true, "calls": true, "reads": true, "writes": true, "requires": true,
		"realized_by": true, "implemented_by": true, "verified_by": true,
	}
	nodeByID := make(map[string]map[string]any, len(nodes))
	nodeKeys := make(map[string]struct{}, len(nodes))
	pageHasFeature := map[string]bool{}
	protectedOperations := map[string]bool{}
	contains := map[string][]string{}
	routes := map[string]struct{}{}
	operations := map[string]struct{}{}
	for index, node := range nodes {
		id := firstString(node, "id")
		key := firstString(node, "key", "businessKey")
		kind := strings.ToLower(firstString(node, "type", "kind"))
		if id == "" || key == "" || !allowedNodes[kind] {
			findings = append(findings, blocker("blueprint.invalid_node", fmt.Sprintf("$.nodes[%d]", index), "Each editable node needs an ID, stable business key, and supported type."))
			continue
		}
		if _, duplicate := nodeByID[id]; duplicate {
			findings = append(findings, blocker("blueprint.duplicate_node", fmt.Sprintf("$.nodes[%d].id", index), "Node IDs must be unique."))
		}
		if _, duplicate := nodeKeys[key]; duplicate {
			findings = append(findings, blocker("blueprint.duplicate_key", fmt.Sprintf("$.nodes[%d].key", index), "Blueprint business keys must be unique."))
		}
		nodeKeys[key] = struct{}{}
		nodeByID[id] = node
		if kind == "page" {
			spec, _ := node["spec"].(map[string]any)
			title := firstString(node, "title")
			if title == "" {
				title = firstString(spec, "title")
			}
			if title == "" {
				findings = append(findings, blocker("blueprint.page_title", fmt.Sprintf("$.nodes[%d].title", index), "Every Page needs a non-empty title."))
			}
			route := firstString(node, "route")
			if route == "" {
				route = firstString(spec, "route")
			}
			goal := firstString(node, "goal", "userGoal")
			if goal == "" {
				goal = firstString(spec, "goal", "userGoal")
			}
			if route == "" || goal == "" {
				findings = append(findings, blocker("blueprint.page_spec", fmt.Sprintf("$.nodes[%d]", index), "Every Page needs a route and user goal."))
			}
			if route != "" {
				if _, duplicate := routes[route]; duplicate {
					findings = append(findings, blocker("blueprint.duplicate_route", fmt.Sprintf("$.nodes[%d].route", index), "Page routes must be unique."))
				}
				routes[route] = struct{}{}
			}
			if len(stringSlice(node["requirementIds"])) == 0 {
				findings = append(findings, blocker("blueprint.page_requirement", fmt.Sprintf("$.nodes[%d].requirementIds", index), "Every Page must trace to at least one stable requirement ID."))
			}
		}
		if kind == "apioperation" || kind == "api" {
			method := strings.ToUpper(firstString(node, "method"))
			path := firstString(node, "path", "route")
			if !allowedHTTPMethod(method) || path == "" || !strings.HasPrefix(path, "/") {
				findings = append(findings, blocker("blueprint.api_operation", fmt.Sprintf("$.nodes[%d]", index), "Every API operation needs a supported HTTP method and absolute path."))
			} else {
				operation := method + " " + path
				if _, duplicate := operations[operation]; duplicate {
					findings = append(findings, blocker("blueprint.duplicate_operation", fmt.Sprintf("$.nodes[%d]", index), "API method/path pairs must be unique."))
				}
				operations[operation] = struct{}{}
			}
		}
	}
	for index, edge := range edges {
		from := firstString(edge, "from", "sourceNodeId", "source")
		to := firstString(edge, "to", "targetNodeId", "target")
		relation := strings.ToLower(firstString(edge, "type", "kind", "relation"))
		if nodeByID[from] == nil || nodeByID[to] == nil || !allowedEdges[relation] || from == to {
			findings = append(findings, blocker("blueprint.invalid_edge", fmt.Sprintf("$.edges[%d]", index), "Edges must use valid endpoints and a supported semantic relation."))
			continue
		}
		if relation == "contains" {
			contains[from] = append(contains[from], to)
			if strings.EqualFold(firstString(nodeByID[from], "type", "kind"), "feature") && strings.EqualFold(firstString(nodeByID[to], "type", "kind"), "page") {
				pageHasFeature[to] = true
			}
		}
		if relation == "requires" && strings.EqualFold(firstString(nodeByID[to], "type", "kind"), "permission") {
			protectedOperations[from] = true
		}
	}
	if hasDirectedCycle(contains) {
		findings = append(findings, blocker("blueprint.contains_cycle", "$.edges", "The contains relationship must be acyclic."))
	}
	for id, node := range nodeByID {
		if strings.EqualFold(firstString(node, "type", "kind"), "page") && !pageHasFeature[id] {
			findings = append(findings, blocker("blueprint.page_feature", "$.nodes", fmt.Sprintf("Page %s must belong to a Feature.", id)))
		}
		kind := strings.ToLower(firstString(node, "type", "kind"))
		if (kind == "apioperation" || kind == "api") && !protectedOperations[id] {
			findings = append(findings, blocker("blueprint.api_permission", "$.edges", fmt.Sprintf("API operation %s must require a Permission node.", id)))
		}
	}
	return findings
}

func allowedHTTPMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func validatePageSpec(value map[string]any) []ValidationFinding {
	findings := make([]ValidationFinding, 0)
	if firstString(value, "blueprintPageNodeId") == "" {
		findings = append(findings, blocker("page_spec.blueprint_node", "$.blueprintPageNodeId", "PageSpec must bind one stable Blueprint Page node."))
	}
	if firstString(value, "title") == "" {
		findings = append(findings, blocker("page_spec.title", "$.title", "PageSpec needs a title."))
	}
	route := firstString(value, "route")
	if route == "" || !strings.HasPrefix(route, "/") {
		findings = append(findings, blocker("page_spec.route", "$.route", "PageSpec needs a route."))
	}
	if firstString(value, "goal", "userGoal") == "" {
		findings = append(findings, blocker("page_spec.goal", "$.goal", "PageSpec needs a user goal."))
	}
	states := objectSlice(value["states"])
	stateIDs := map[string]bool{}
	stateKeys := map[string]bool{}
	for index, state := range states {
		identifier := firstString(state, "id")
		key := firstString(state, "key", "name")
		if identifier == "" || key == "" || firstString(state, "title") == "" {
			findings = append(findings, blocker("page_spec.invalid_state", fmt.Sprintf("$.states[%d]", index), "Every PageSpec state needs a stable ID, key, and title."))
		}
		if stateIDs[identifier] || stateKeys[key] {
			findings = append(findings, blocker("page_spec.duplicate_state", fmt.Sprintf("$.states[%d]", index), "PageSpec state IDs and keys must be unique."))
		}
		stateIDs[identifier] = true
		stateKeys[key] = true
	}
	for _, required := range []string{"ready", "loading", "empty", "error"} {
		if !stateKeys[required] {
			findings = append(findings, blocker("page_spec.required_state", "$.states", fmt.Sprintf("PageSpec must declare the %s state.", required)))
			continue
		}
		for index, state := range states {
			if firstString(state, "key", "name") == required && !boolean(state["required"]) {
				findings = append(findings, blocker("page_spec.required_state_flag", fmt.Sprintf("$.states[%d].required", index), fmt.Sprintf("The canonical %s state must be marked required.", required)))
			}
		}
	}
	if len(objectSlice(value["acceptanceRefs"])) == 0 && len(stringSlice(value["acceptanceCriterionIds"])) == 0 {
		findings = append(findings, blocker("page_spec.acceptance_trace", "$.acceptanceRefs", "PageSpec must trace to at least one acceptance criterion."))
	}
	bindingIDs := map[string]bool{}
	for index, binding := range objectSlice(value["dataBindings"]) {
		identifier := firstString(binding, "id")
		source := firstString(binding, "source")
		if identifier == "" || firstString(binding, "name") == "" || !allowedPageDataSource(source) {
			findings = append(findings, blocker("page_spec.invalid_binding", fmt.Sprintf("$.dataBindings[%d]", index), "Every data binding needs a stable ID, name, and supported source."))
		}
		if bindingIDs[identifier] {
			findings = append(findings, blocker("page_spec.duplicate_binding", fmt.Sprintf("$.dataBindings[%d].id", index), "PageSpec data binding IDs must be unique."))
		}
		if source == "api" && strings.TrimSpace(firstString(binding, "operationId")) == "" {
			findings = append(findings, blocker("page_spec.api_operation", fmt.Sprintf("$.dataBindings[%d].operationId", index), "API data bindings must name one stable Blueprint operation ID."))
		}
		bindingIDs[identifier] = true
	}
	interactionIDs := map[string]bool{}
	for index, interaction := range objectSlice(value["interactions"]) {
		identifier := firstString(interaction, "id")
		if identifier == "" || firstString(interaction, "trigger") == "" || firstString(interaction, "outcome") == "" {
			findings = append(findings, blocker("page_spec.invalid_interaction", fmt.Sprintf("$.interactions[%d]", index), "Every interaction needs a stable ID, trigger, and outcome."))
		}
		if interactionIDs[identifier] {
			findings = append(findings, blocker("page_spec.duplicate_interaction", fmt.Sprintf("$.interactions[%d].id", index), "PageSpec interaction IDs must be unique."))
		}
		interactionIDs[identifier] = true
	}
	return findings
}

func allowedPageDataSource(source string) bool {
	switch source {
	case "api", "database", "fixture", "local":
		return true
	default:
		return false
	}
}

func validatePrototype(value map[string]any) []ValidationFinding {
	findings := make([]ValidationFinding, 0)
	for _, field := range []string{
		"states", "breakpoints", "frames", "overrides", "interactions", "fixtures",
		"tokenBindings", "componentBindings", "assets", "traceLinks",
	} {
		if raw, exists := value[field]; exists && !isJSONObjectArray(raw) {
			findings = append(findings, blocker(
				"prototype.array_contract", "$."+field,
				fmt.Sprintf("Prototype %s must be an array containing only JSON objects.", field),
			))
		}
	}
	if raw, exists := value["layers"]; exists && !isPrototypeLayerCollection(raw) {
		findings = append(findings, blocker("prototype.layer_contract", "$.layers", "Prototype layers must be a record or array containing only JSON objects."))
	}
	legacyPageSpecRef := firstString(value, "sourcePageSpecArtifactId") != "" &&
		firstString(value, "sourcePageSpecRevisionId") != "" &&
		firstString(value, "sourcePageSpecHash") != ""
	if !validVersionReference(value["pageSpecRevision"]) && !legacyPageSpecRef {
		findings = append(findings, blocker("prototype.page_spec_ref", "$.pageSpecRevision", "Prototype must pin an exact PageSpec revision and hash."))
	}
	states := objectSlice(value["states"])
	if len(states) == 0 {
		findings = append(findings, blocker("prototype.states", "$.states", "Prototype must contain the PageSpec states."))
	}
	stateIDs := map[string]bool{}
	stateKeys := map[string]bool{}
	for index, state := range states {
		identifier := firstString(state, "id")
		key := firstString(state, "key")
		if identifier == "" || key == "" || firstString(state, "title") == "" {
			findings = append(findings, blocker("prototype.invalid_state", fmt.Sprintf("$.states[%d]", index), "Every prototype state needs a stable ID, key, and title."))
		}
		if _, exists := state["required"].(bool); !exists || !hasJSONStringArray(state, "fixtureIds") {
			findings = append(findings, blocker("prototype.state_contract", fmt.Sprintf("$.states[%d]", index), "Every prototype state must explicitly declare required and fixtureIds."))
		}
		if stateIDs[identifier] || stateKeys[key] {
			findings = append(findings, blocker("prototype.duplicate_state", fmt.Sprintf("$.states[%d]", index), "Prototype state IDs and keys must be unique."))
		}
		stateIDs[identifier] = true
		stateKeys[key] = true
	}
	breakpoints := objectSlice(value["breakpoints"])
	if len(breakpoints) < 3 {
		findings = append(findings, blocker("prototype.breakpoints", "$.breakpoints", "Prototype must provide desktop, tablet, and mobile breakpoints."))
	}
	breakpointIDs := map[string]bool{}
	breakpointNames := map[string]bool{}
	for index, breakpoint := range breakpoints {
		identifier := firstString(breakpoint, "id")
		name := strings.ToLower(firstString(breakpoint, "name", "key"))
		if identifier == "" || name == "" {
			findings = append(findings, blocker("prototype.invalid_breakpoint", fmt.Sprintf("$.breakpoints[%d]", index), "Every breakpoint needs a stable ID and name."))
		}
		if breakpointIDs[identifier] || breakpointNames[name] {
			findings = append(findings, blocker("prototype.duplicate_breakpoint", fmt.Sprintf("$.breakpoints[%d]", index), "Prototype breakpoint IDs and names must be unique."))
		}
		breakpointIDs[identifier] = true
		breakpointNames[name] = true
	}
	for _, required := range []string{"desktop", "tablet", "mobile"} {
		if !breakpointNames[required] {
			findings = append(findings, blocker("prototype.required_breakpoint", "$.breakpoints", fmt.Sprintf("Prototype must declare the %s breakpoint.", required)))
		}
	}
	layerObjects := prototypeCanonicalLayerObjects(value)
	layers := objectSlice(value["layers"])
	if len(layerObjects) == 0 && len(layers) == 0 {
		if scene, ok := value["scene"].(map[string]any); ok {
			layers = objectSlice(scene["layers"])
		}
	}
	if len(layerObjects) == 0 && len(layers) == 0 {
		findings = append(findings, blocker("prototype.layers", "$.layers", "Prototype must contain a semantic layer tree."))
	}
	seen := map[string]bool{}
	for id, layer := range layerObjects {
		if id == "" || seen[id] || firstString(layer, "id") != "" && firstString(layer, "id") != id {
			findings = append(findings, blocker("prototype.layer_id", "$.layers", "Every layer needs a unique stable ID."))
		}
		seen[id] = true
	}
	for index, layer := range layers {
		id := firstString(layer, "id", "layerId")
		if id == "" || seen[id] {
			findings = append(findings, blocker("prototype.layer_id", fmt.Sprintf("$.layers[%d]", index), "Every layer needs a unique stable ID."))
		}
		seen[id] = true
	}
	for id, layer := range layerObjects {
		if parentID := firstString(layer, "parentId"); parentID != "" && layerObjects[parentID] == nil {
			findings = append(findings, blocker("prototype.layer_parent", "$.layers."+id+".parentId", "Layer parentId must reference an existing layer."))
		}
		for childIndex, childID := range stringSlice(layer["childIds"]) {
			if layerObjects[childID] == nil || childID == id {
				findings = append(findings, blocker("prototype.layer_child", fmt.Sprintf("$.layers.%s.childIds[%d]", id, childIndex), "Layer childIds must reference another existing layer."))
			}
		}
		for _, field := range []string{"childIds", "requirementIds", "acceptanceCriterionIds"} {
			if _, exists := layer[field]; exists && !hasJSONStringArray(layer, field) {
				findings = append(findings, blocker("prototype.layer_array_contract", "$.layers."+id+"."+field, "Prototype layer trace and child fields must contain only stable string IDs."))
			}
		}
	}
	frames := objectSlice(value["frames"])
	if len(frames) == 0 {
		findings = append(findings, blocker("prototype.frames", "$.frames", "Prototype must define a frame for each required state and breakpoint."))
	} else {
		coverage := map[string]bool{}
		for index, frame := range frames {
			stateID := firstString(frame, "stateId")
			breakpointID := firstString(frame, "breakpointId")
			rootLayerID := firstString(frame, "rootLayerId")
			key := stateID + "\x00" + breakpointID
			if firstString(frame, "id") == "" || !stateIDs[stateID] || !breakpointIDs[breakpointID] || layerObjects[rootLayerID] == nil {
				findings = append(findings, blocker("prototype.invalid_frame", fmt.Sprintf("$.frames[%d]", index), "Every frame must reference an existing state, breakpoint, and root layer."))
			}
			if coverage[key] {
				findings = append(findings, blocker("prototype.duplicate_frame", fmt.Sprintf("$.frames[%d]", index), "Only one base frame is allowed per state and breakpoint pair."))
			}
			coverage[key] = true
		}
		for _, state := range states {
			if required, exists := state["required"].(bool); exists && !required {
				continue
			}
			stateID := firstString(state, "id")
			for _, breakpoint := range breakpoints {
				breakpointID := firstString(breakpoint, "id")
				if stateID != "" && breakpointID != "" && !coverage[stateID+"\x00"+breakpointID] {
					findings = append(findings, blocker("prototype.frame_coverage", "$.frames", fmt.Sprintf("State %s has no frame at breakpoint %s.", stateID, breakpointID)))
				}
			}
		}
	}
	fixtures := objectSlice(value["fixtures"])
	fixtureIDs := map[string]bool{}
	fixtureStateIDs := map[string]string{}
	for index, fixture := range fixtures {
		fixtureID := strings.TrimSpace(firstString(fixture, "id"))
		if fixtureID == "" || fixtureIDs[fixtureID] {
			findings = append(findings, blocker("prototype.fixture_id", fmt.Sprintf("$.fixtures[%d].id", index), "Prototype fixtures need unique stable IDs."))
		}
		fixtureIDs[fixtureID] = true
		stateID := strings.TrimSpace(firstString(fixture, "stateId"))
		fixtureStateIDs[fixtureID] = stateID
		if !stateIDs[stateID] {
			findings = append(findings, blocker("prototype.fixture_state", fmt.Sprintf("$.fixtures[%d].stateId", index), "Prototype fixture stateId must reference an existing state."))
		}
		if err := validatePrototypeFixtureDTO(fixture); err != nil {
			findings = append(findings, blocker("prototype.fixture_contract", fmt.Sprintf("$.fixtures[%d]", index), err.Error()))
		}
		if _, exists := fixture["operationId"]; exists && strings.TrimSpace(firstString(fixture, "operationId")) == "" {
			findings = append(findings, blocker("prototype.fixture_operation", fmt.Sprintf("$.fixtures[%d].operationId", index), "Fixture operationId must be a non-empty stable operation ID when present."))
		}
	}
	declaredFixtureState := map[string]string{}
	for stateIndex, state := range states {
		stateID := strings.TrimSpace(firstString(state, "id"))
		for fixtureIndex, fixtureID := range stringSlice(state["fixtureIds"]) {
			fixtureID = strings.TrimSpace(fixtureID)
			if fixtureID == "" || !fixtureIDs[fixtureID] || fixtureStateIDs[fixtureID] != stateID || declaredFixtureState[fixtureID] != "" {
				findings = append(findings, blocker("prototype.state_fixture_set", fmt.Sprintf("$.states[%d].fixtureIds[%d]", stateIndex, fixtureIndex), "State fixtureIds must be a duplicate-free exact set of fixtures owned by that state."))
			}
			declaredFixtureState[fixtureID] = stateID
		}
	}
	for fixtureID := range fixtureIDs {
		if declaredFixtureState[fixtureID] == "" {
			findings = append(findings, blocker("prototype.state_fixture_set", "$.states", fmt.Sprintf("Fixture %s must be declared by exactly one state fixtureIds set.", fixtureID)))
		}
	}
	allowedTriggers := map[string]bool{"click": true, "submit": true, "change": true, "hover": true, "load": true}
	allowedActions := map[string]bool{"navigate": true, "setState": true, "openOverlay": true, "closeOverlay": true, "updateBinding": true, "submitFixture": true}
	interactionIDs := map[string]bool{}
	for index, interaction := range objectSlice(value["interactions"]) {
		interactionID := strings.TrimSpace(firstString(interaction, "id"))
		if interactionID == "" || interactionIDs[interactionID] || layerObjects[firstString(interaction, "sourceLayerId")] == nil || !allowedTriggers[firstString(interaction, "trigger")] {
			findings = append(findings, blocker("prototype.invalid_interaction", fmt.Sprintf("$.interactions[%d]", index), "Interactions require a stable ID, existing source layer, and whitelisted trigger."))
		}
		interactionIDs[interactionID] = true
		actions := objectSlice(interaction["actions"])
		if !isJSONObjectArray(interaction["actions"]) {
			findings = append(findings, blocker("prototype.interaction_actions", fmt.Sprintf("$.interactions[%d].actions", index), "Prototype interaction actions must be an array containing only declarative action objects."))
		}
		if len(actions) == 0 {
			findings = append(findings, blocker("prototype.interaction_actions", fmt.Sprintf("$.interactions[%d].actions", index), "Every prototype interaction must declare at least one action."))
		}
		for actionIndex, action := range actions {
			actionType := firstString(action, "type")
			if !allowedActions[actionType] {
				findings = append(findings, blocker("prototype.invalid_action", fmt.Sprintf("$.interactions[%d].actions[%d]", index, actionIndex), "Prototype interaction actions must use the declarative whitelist."))
				continue
			}
			validReference := true
			switch actionType {
			case "navigate":
				target, err := canonicalBlueprintAlias(
					"Prototype navigate action target", false,
					firstString(action, "targetPageNodeId"), firstString(action, "targetPageSpecId"),
				)
				validReference = err == nil && target != ""
			case "setState":
				validReference = stateIDs[strings.TrimSpace(firstString(action, "stateId"))]
			case "openOverlay":
				layer := layerObjects[strings.TrimSpace(firstString(action, "layerId"))]
				validReference = layer != nil && strings.EqualFold(strings.TrimSpace(firstString(layer, "kind")), "overlay")
			case "updateBinding":
				_, hasValue := action["value"]
				validReference = strings.TrimSpace(firstString(action, "bindingId")) != "" && hasValue
			case "submitFixture":
				validReference = fixtureIDs[strings.TrimSpace(firstString(action, "fixtureId"))]
			}
			if !validReference {
				findings = append(findings, blocker("prototype.action_reference", fmt.Sprintf("$.interactions[%d].actions[%d]", index, actionIndex), "Prototype action fields must reference the exact declared state, overlay, binding, fixture, or navigation target."))
			}
		}
	}
	return findings
}

func hasJSONStringArray(value map[string]any, key string) bool {
	items, exists := value[key].([]any)
	if !exists {
		return false
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

func isJSONObjectArray(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if _, ok := item.(map[string]any); !ok {
			return false
		}
	}
	return true
}

func isPrototypeLayerCollection(value any) bool {
	if object, ok := value.(map[string]any); ok {
		for _, item := range object {
			if _, ok := item.(map[string]any); !ok {
				return false
			}
		}
		return true
	}
	return isJSONObjectArray(value)
}

func validVersionReference(value any) bool {
	reference, ok := value.(map[string]any)
	return ok && firstString(reference, "artifactId") != "" && firstString(reference, "revisionId") != "" && firstString(reference, "contentHash") != ""
}

func hasDirectedCycle(adjacency map[string][]string) bool {
	state := map[string]uint8{}
	var visit func(string) bool
	visit = func(node string) bool {
		if state[node] == 1 {
			return true
		}
		if state[node] == 2 {
			return false
		}
		state[node] = 1
		for _, next := range adjacency[node] {
			if visit(next) {
				return true
			}
		}
		state[node] = 2
		return false
	}
	for node := range adjacency {
		if visit(node) {
			return true
		}
	}
	return false
}

func objectSlice(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func boolean(value any) bool {
	result, _ := value.(bool)
	return result
}

func blocker(code, path, message string) ValidationFinding {
	return ValidationFinding{Code: code, Path: path, Message: message, Severity: "blocker"}
}
