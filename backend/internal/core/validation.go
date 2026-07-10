package core

import (
	"encoding/json"
	"fmt"
	"strings"
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
	case "product_requirements", "requirement_baseline":
		findings = append(findings, validateRequirements(value)...)
	case "blueprint":
		findings = append(findings, validateBlueprint(value)...)
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

func validateProjectBrief(value map[string]any) []ValidationFinding {
	blocks := objectSlice(value["blocks"])
	findings := make([]ValidationFinding, 0)
	if len(blocks) == 0 {
		return append(findings, blocker("brief.blocks_required", "$.blocks", "Project Brief must contain structured blocks."))
	}
	goals := 0
	for index, block := range blocks {
		typeName, _ := block["type"].(string)
		if typeName == "goal" {
			goals++
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
		findings = append(findings, blocker("brief.goal_required", "$.blocks", "Project Brief must define at least one goal."))
	}
	return findings
}

func validateRequirements(value map[string]any) []ValidationFinding {
	blocks := objectSlice(value["blocks"])
	findings := make([]ValidationFinding, 0)
	if len(blocks) == 0 {
		return []ValidationFinding{blocker("requirements.blocks_required", "$.blocks", "Requirements must contain structured blocks.")}
	}
	requirementIDs := map[string]struct{}{}
	acceptanceByRequirement := map[string]int{}
	for index, block := range blocks {
		typeName, _ := block["type"].(string)
		if typeName == "openQuestion" && boolean(block["blocking"]) {
			status, _ := block["status"].(string)
			if status != "answered" && status != "resolved" && status != "waived" {
				findings = append(findings, blocker("requirements.blocking_question", fmt.Sprintf("$.blocks[%d]", index), "Blocking requirement questions must be resolved."))
			}
		}
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

func validateBlueprint(value map[string]any) []ValidationFinding {
	nodes := objectSlice(value["nodes"])
	edges := objectSlice(value["edges"])
	findings := make([]ValidationFinding, 0)
	if len(nodes) == 0 {
		return []ValidationFinding{blocker("blueprint.nodes_required", "$.nodes", "Blueprint must contain semantic nodes.")}
	}
	allowedNodes := map[string]bool{
		"feature": true, "page": true, "component": true, "apiOperation": true,
		"api": true, "dataEntity": true, "dataModel": true, "permission": true,
	}
	allowedEdges := map[string]bool{
		"drives": true, "satisfied_by": true, "contains": true, "navigates_to": true,
		"uses": true, "calls": true, "reads": true, "writes": true, "requires": true,
		"realized_by": true, "implemented_by": true, "verified_by": true,
	}
	nodeByID := make(map[string]map[string]any, len(nodes))
	pageHasFeature := map[string]bool{}
	contains := map[string][]string{}
	for index, node := range nodes {
		id := firstString(node, "id")
		key := firstString(node, "key", "businessKey")
		kind := firstString(node, "type", "kind")
		if id == "" || key == "" || !allowedNodes[kind] {
			findings = append(findings, blocker("blueprint.invalid_node", fmt.Sprintf("$.nodes[%d]", index), "Each editable node needs an ID, stable business key, and supported type."))
			continue
		}
		if _, duplicate := nodeByID[id]; duplicate {
			findings = append(findings, blocker("blueprint.duplicate_node", fmt.Sprintf("$.nodes[%d].id", index), "Node IDs must be unique."))
		}
		nodeByID[id] = node
		if kind == "page" {
			spec, _ := node["spec"].(map[string]any)
			if spec == nil {
				spec = node
			}
			if firstString(spec, "route") == "" || firstString(spec, "goal", "userGoal") == "" {
				findings = append(findings, blocker("blueprint.page_spec", fmt.Sprintf("$.nodes[%d]", index), "Every Page needs a route and user goal."))
			}
		}
	}
	for index, edge := range edges {
		from := firstString(edge, "from", "sourceNodeId", "source")
		to := firstString(edge, "to", "targetNodeId", "target")
		relation := firstString(edge, "type", "relation")
		if nodeByID[from] == nil || nodeByID[to] == nil || !allowedEdges[relation] || from == to {
			findings = append(findings, blocker("blueprint.invalid_edge", fmt.Sprintf("$.edges[%d]", index), "Edges must use valid endpoints and a supported semantic relation."))
			continue
		}
		if relation == "contains" {
			contains[from] = append(contains[from], to)
			if firstString(nodeByID[from], "type", "kind") == "feature" && firstString(nodeByID[to], "type", "kind") == "page" {
				pageHasFeature[to] = true
			}
		}
	}
	if hasDirectedCycle(contains) {
		findings = append(findings, blocker("blueprint.contains_cycle", "$.edges", "The contains relationship must be acyclic."))
	}
	for id, node := range nodeByID {
		if firstString(node, "type", "kind") == "page" && !pageHasFeature[id] {
			findings = append(findings, blocker("blueprint.page_feature", "$.nodes", fmt.Sprintf("Page %s must belong to a Feature.", id)))
		}
	}
	return findings
}

func validatePageSpec(value map[string]any) []ValidationFinding {
	findings := make([]ValidationFinding, 0)
	if firstString(value, "route") == "" {
		findings = append(findings, blocker("page_spec.route", "$.route", "PageSpec needs a route."))
	}
	if firstString(value, "goal", "userGoal") == "" {
		findings = append(findings, blocker("page_spec.goal", "$.goal", "PageSpec needs a user goal."))
	}
	states := objectSlice(value["states"])
	stateIDs := map[string]bool{}
	for _, state := range states {
		stateIDs[firstString(state, "id", "key", "name")] = true
	}
	for _, required := range []string{"ready", "loading", "empty", "error"} {
		if !stateIDs[required] {
			findings = append(findings, blocker("page_spec.required_state", "$.states", fmt.Sprintf("PageSpec must declare the %s state.", required)))
		}
	}
	if len(objectSlice(value["acceptanceRefs"])) == 0 && len(stringSlice(value["acceptanceCriterionIds"])) == 0 {
		findings = append(findings, blocker("page_spec.acceptance_trace", "$.acceptanceRefs", "PageSpec must trace to at least one acceptance criterion."))
	}
	return findings
}

func validatePrototype(value map[string]any) []ValidationFinding {
	findings := make([]ValidationFinding, 0)
	legacyPageSpecRef := firstString(value, "sourcePageSpecArtifactId") != "" &&
		firstString(value, "sourcePageSpecRevisionId") != "" &&
		firstString(value, "sourcePageSpecHash") != ""
	if !validVersionReference(value["pageSpecRevision"]) && !legacyPageSpecRef {
		findings = append(findings, blocker("prototype.page_spec_ref", "$.pageSpecRevision", "Prototype must pin an exact PageSpec revision and hash."))
	}
	if len(objectSlice(value["states"])) == 0 {
		findings = append(findings, blocker("prototype.states", "$.states", "Prototype must contain the PageSpec states."))
	}
	breakpoints := objectSlice(value["breakpoints"])
	if len(breakpoints) < 3 {
		findings = append(findings, blocker("prototype.breakpoints", "$.breakpoints", "Prototype must provide desktop, tablet, and mobile breakpoints."))
	}
	layerObjects := prototypeLayerObjects(value["layers"])
	layers := objectSlice(value["layers"])
	if len(layerObjects) == 0 && len(layers) == 0 {
		if scene, ok := value["scene"].(map[string]any); ok {
			layers = objectSlice(scene["layers"])
			layerObjects = prototypeLayerObjects(scene["layers"])
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
	frames := objectSlice(value["frames"])
	if len(frames) == 0 {
		findings = append(findings, blocker("prototype.frames", "$.frames", "Prototype must define a frame for each required state and breakpoint."))
	} else {
		coverage := map[string]bool{}
		for _, frame := range frames {
			coverage[firstString(frame, "stateId")+"\x00"+firstString(frame, "breakpointId")] = true
		}
		for _, state := range objectSlice(value["states"]) {
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
	for index, fixture := range objectSlice(value["fixtures"]) {
		if sanitized, exists := fixture["sanitized"].(bool); !exists || !sanitized {
			findings = append(findings, blocker("prototype.fixture_sanitized", fmt.Sprintf("$.fixtures[%d]", index), "Prototype fixtures must be marked sanitized."))
		}
	}
	return findings
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
