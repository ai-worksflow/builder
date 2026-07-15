package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
)

type requirementTraceSnapshot struct {
	requirements            map[string]bool
	mustRequirements        map[string]bool
	acceptance              map[string]bool
	acceptanceByRequirement map[string]map[string]bool
}

func decodeRequirementTrace(payload json.RawMessage) (requirementTraceSnapshot, error) {
	var baseline struct {
		Requirements []json.RawMessage `json:"requirements"`
	}
	if err := json.Unmarshal(payload, &baseline); err != nil {
		return requirementTraceSnapshot{}, err
	}
	result := requirementTraceSnapshot{
		requirements: map[string]bool{}, mustRequirements: map[string]bool{},
		acceptance: map[string]bool{}, acceptanceByRequirement: map[string]map[string]bool{},
	}
	for _, raw := range baseline.Requirements {
		var item map[string]any
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(firstString(item, "type"))) {
		case "requirement":
			id := strings.TrimSpace(firstString(item, "requirementId", "id", "key"))
			if id == "" {
				continue
			}
			result.requirements[id] = true
			if strings.EqualFold(strings.TrimSpace(firstString(item, "priority")), "must") {
				result.mustRequirements[id] = true
			}
			allowed := result.acceptanceByRequirement[id]
			if allowed == nil {
				allowed = map[string]bool{}
				result.acceptanceByRequirement[id] = allowed
			}
			for _, acceptanceID := range stringSlice(item["acceptanceCriterionIds"]) {
				if acceptanceID = strings.TrimSpace(acceptanceID); acceptanceID != "" {
					allowed[acceptanceID] = true
				}
			}
		case "acceptancecriterion":
			id := strings.TrimSpace(firstString(item, "acceptanceCriterionId", "id", "key"))
			if id != "" {
				result.acceptance[id] = true
			}
		}
	}
	if len(result.requirements) == 0 {
		return requirementTraceSnapshot{}, fmt.Errorf("Requirement Baseline contains no stable requirement IDs")
	}
	for requirementID, acceptanceIDs := range result.acceptanceByRequirement {
		if result.mustRequirements[requirementID] && len(acceptanceIDs) == 0 {
			return requirementTraceSnapshot{}, fmt.Errorf("Must Requirement %q has no acceptance criteria", requirementID)
		}
		for acceptanceID := range acceptanceIDs {
			if !result.acceptance[acceptanceID] {
				return requirementTraceSnapshot{}, fmt.Errorf("Requirement %q references missing acceptance criterion %q", requirementID, acceptanceID)
			}
		}
	}
	return result, nil
}

// ValidateBlueprintAgainstRequirementBaseline checks that every Blueprint
// requirement reference belongs to the supplied Requirement Baseline and,
// when requested, that the Blueprint covers every Must requirement.
func ValidateBlueprintAgainstRequirementBaseline(
	blueprint, baseline json.RawMessage,
	requireMustCoverage bool,
) error {
	trace, err := decodeRequirementTrace(baseline)
	if err != nil {
		return fmt.Errorf("decode Requirement Baseline trace: %w", err)
	}
	if err := validateBlueprintRequirementTrace(blueprint, trace, requireMustCoverage); err != nil {
		return fmt.Errorf("validate Blueprint against Requirement Baseline trace: %w", err)
	}
	return nil
}

func validateBlueprintRequirementTrace(payload json.RawMessage, trace requirementTraceSnapshot, strictValues ...bool) error {
	var envelope struct {
		Nodes    []json.RawMessage `json:"nodes"`
		Semantic *struct {
			Nodes []json.RawMessage `json:"nodes"`
		} `json:"semantic"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode Blueprint semantic trace: %w", err)
	}
	semanticNodeCount := 0
	if envelope.Semantic != nil {
		semanticNodeCount = len(envelope.Semantic.Nodes)
	}
	// Workflow AI initializes an empty target before proposing the graph. The
	// generic approval validator still rejects an empty final Blueprint.
	if len(envelope.Nodes)+semanticNodeCount == 0 {
		return nil
	}
	nodes, _, err := DecodeBlueprintSemanticGraph(payload)
	if err != nil {
		return err
	}
	covered := map[string]bool{}
	for _, node := range nodes {
		for _, requirementID := range node.RequirementIDs {
			if !trace.requirements[requirementID] {
				return fmt.Errorf("Blueprint %s node %s references unknown Requirement Baseline ID %q", node.Kind, node.ID, requirementID)
			}
			covered[requirementID] = true
		}
	}
	if semanticStrict(strictValues) {
		for requirementID := range trace.mustRequirements {
			if !covered[requirementID] {
				return fmt.Errorf("Blueprint does not cover Must Requirement Baseline ID %q", requirementID)
			}
		}
	}
	return nil
}

func pageAcceptanceSet(page BlueprintSemanticNode, trace requirementTraceSnapshot) map[string]bool {
	allowed := map[string]bool{}
	for _, requirementID := range page.RequirementIDs {
		for acceptanceID := range trace.acceptanceByRequirement[requirementID] {
			if trace.acceptance[acceptanceID] {
				allowed[acceptanceID] = true
			}
		}
	}
	return allowed
}

type pageSemanticRelations struct {
	ownedAPIs          map[string]bool
	requiredAPIs       map[string]bool
	protectedAPIs      map[string]bool
	navigationTokens   map[string]string
	requiredNavigation map[string]bool
	allowedRoles       map[string]bool
	requiredRoles      map[string]bool
}

func blueprintPageRelations(page BlueprintSemanticNode, nodes []BlueprintSemanticNode, edges []BlueprintSemanticEdge) pageSemanticRelations {
	result := pageSemanticRelations{
		ownedAPIs: map[string]bool{}, requiredAPIs: map[string]bool{}, protectedAPIs: map[string]bool{},
		navigationTokens: map[string]string{}, requiredNavigation: map[string]bool{},
		allowedRoles: map[string]bool{}, requiredRoles: map[string]bool{},
	}
	nodeByID := map[string]BlueprintSemanticNode{}
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}
	apiPermissionRoles := map[string]map[string]bool{}
	for _, edge := range edges {
		source, target := nodeByID[edge.SourceID], nodeByID[edge.TargetID]
		if edge.Kind == "requires" && (source.Kind == "apioperation" || source.Kind == "api") && target.Kind == "permission" {
			result.protectedAPIs[source.ID] = true
			roles := apiPermissionRoles[source.ID]
			if roles == nil {
				roles = map[string]bool{}
				apiPermissionRoles[source.ID] = roles
			}
			for role := range blueprintPermissionRoles(target) {
				roles[role] = true
			}
		}
	}
	for _, edge := range edges {
		target := nodeByID[edge.TargetID]
		if edge.SourceID != page.ID {
			continue
		}
		switch edge.Kind {
		case "calls", "uses":
			if target.Kind == "apioperation" || target.Kind == "api" {
				result.ownedAPIs[target.ID] = true
				if edge.Required {
					result.requiredAPIs[target.ID] = true
				}
				for role := range apiPermissionRoles[target.ID] {
					result.allowedRoles[role] = true
					result.requiredRoles[role] = true
				}
			}
		case "navigates_to":
			if target.Kind == "page" {
				for _, token := range []string{target.ID, target.Key, target.Route} {
					if token = strings.TrimSpace(token); token != "" {
						result.navigationTokens[token] = target.ID
					}
				}
				if edge.Required {
					result.requiredNavigation[target.ID] = true
				}
			}
		case "requires":
			if target.Kind == "permission" {
				for role := range blueprintPermissionRoles(target) {
					result.allowedRoles[role] = true
					if edge.Required {
						result.requiredRoles[role] = true
					}
				}
				for _, alias := range []string{target.ID, target.Key} {
					if alias = strings.TrimSpace(alias); alias != "" {
						result.allowedRoles[alias] = true
					}
				}
			}
		}
	}
	return result
}

func blueprintPermissionRoles(permission BlueprintSemanticNode) map[string]bool {
	roles := stringSet(permission.Roles)
	if len(roles) == 0 {
		if role := strings.TrimSpace(permission.Key); role != "" {
			roles[role] = true
		} else if role := strings.TrimSpace(permission.ID); role != "" {
			roles[role] = true
		}
	}
	return roles
}

func validatePageSpecSemanticTrace(
	payload json.RawMessage,
	page BlueprintSemanticNode,
	nodes []BlueprintSemanticNode,
	edges []BlueprintSemanticEdge,
	trace requirementTraceSnapshot,
	strictValues ...bool,
) error {
	var content map[string]any
	if json.Unmarshal(payload, &content) != nil {
		return fmt.Errorf("PageSpec content must be a JSON object")
	}
	strict := semanticStrict(strictValues)
	route := strings.TrimSpace(firstString(content, "route"))
	if (strict && route != page.Route) || (!strict && route != "" && route != page.Route) {
		return fmt.Errorf("PageSpec route must exactly match its Blueprint Page")
	}
	goal, err := canonicalBlueprintAlias(
		"PageSpec user goal", false, firstString(content, "userGoal"), firstString(content, "goal"),
	)
	if err != nil {
		return err
	}
	if (strict && goal != page.UserGoal) || (!strict && goal != "" && goal != page.UserGoal) {
		return fmt.Errorf("PageSpec userGoal must exactly match its Blueprint Page")
	}
	allowedAcceptance := pageAcceptanceSet(page, trace)
	usedAcceptance := map[string]bool{}
	checkAcceptance := func(values []string, location string) error {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || !allowedAcceptance[value] {
				return fmt.Errorf("PageSpec %s references acceptance criterion %q outside its Blueprint Page requirements", location, value)
			}
			usedAcceptance[value] = true
		}
		return nil
	}
	if err := checkAcceptance(stringSlice(content["acceptanceCriterionIds"]), "acceptanceCriterionIds"); err != nil {
		return err
	}
	for _, ref := range objectSlice(content["acceptanceRefs"]) {
		if err := checkAcceptance([]string{firstString(ref, "acceptanceCriterionId", "id", "key")}, "acceptanceRefs"); err != nil {
			return err
		}
	}
	for _, state := range objectSlice(content["states"]) {
		if err := checkAcceptance(stringSlice(state["acceptanceCriterionIds"]), "state acceptanceCriterionIds"); err != nil {
			return err
		}
	}
	for _, interaction := range objectSlice(content["interactions"]) {
		if err := checkAcceptance(stringSlice(interaction["acceptanceCriterionIds"]), "interaction acceptanceCriterionIds"); err != nil {
			return err
		}
	}
	if strict {
		for acceptanceID := range allowedAcceptance {
			if !usedAcceptance[acceptanceID] {
				return fmt.Errorf("PageSpec does not cover acceptance criterion %q required by its Blueprint Page", acceptanceID)
			}
		}
	}

	relations := blueprintPageRelations(page, nodes, edges)
	usedAPIs := map[string]bool{}
	for _, binding := range objectSlice(content["dataBindings"]) {
		source := strings.TrimSpace(firstString(binding, "source"))
		operationID := strings.TrimSpace(firstString(binding, "operationId"))
		if source != "api" {
			if operationID != "" {
				return fmt.Errorf("PageSpec non-API data bindings cannot claim a Blueprint operationId")
			}
			continue
		}
		if operationID == "" || !relations.ownedAPIs[operationID] || !relations.protectedAPIs[operationID] {
			return fmt.Errorf("PageSpec API binding requires an existing permission-protected Blueprint operation owned by this Page through calls/uses")
		}
		usedAPIs[operationID] = true
	}
	if strict {
		for operationID := range relations.requiredAPIs {
			if !usedAPIs[operationID] {
				return fmt.Errorf("PageSpec does not realize required Blueprint API edge to %q", operationID)
			}
		}
	}

	roles := stringSet(stringSlice(content["requiredRoles"]))
	for role := range roles {
		if !relations.allowedRoles[role] {
			return fmt.Errorf("PageSpec required role %q is not authorized by a Page requires Permission edge", role)
		}
	}
	if strict && !sameSemanticSet(roles, relations.requiredRoles) {
		return fmt.Errorf("PageSpec requiredRoles must exactly realize its Blueprint Page permission edges")
	}

	usedNavigation := map[string]bool{}
	for _, interaction := range objectSlice(content["interactions"]) {
		target, err := canonicalBlueprintAlias(
			"PageSpec navigation target", false,
			firstString(interaction, "targetPageNodeId"),
			firstString(interaction, "targetPageSpecId"),
		)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}
		targetNodeID := relations.navigationTokens[target]
		if targetNodeID == "" {
			return fmt.Errorf("PageSpec navigation target %q is not connected by a Blueprint navigates_to edge", target)
		}
		usedNavigation[targetNodeID] = true
	}
	if strict {
		for targetNodeID := range relations.requiredNavigation {
			if !usedNavigation[targetNodeID] {
				return fmt.Errorf("PageSpec does not realize required Blueprint navigation to %q", targetNodeID)
			}
		}
	}
	return nil
}

type prototypeSemanticAuthority struct {
	requirementIDs     map[string]bool
	acceptanceIDs      map[string]bool
	pageNodeID         string
	pageSpecArtifactID string
	baselineRef        VersionRef
	blueprintRef       VersionRef
	pageSpecRef        VersionRef
}

func validatePrototypeSemanticTrace(
	payload, pageSpecPayload json.RawMessage,
	requireCoverage bool,
	authorities ...prototypeSemanticAuthority,
) error {
	var prototype, pageSpec map[string]any
	if json.Unmarshal(payload, &prototype) != nil || json.Unmarshal(pageSpecPayload, &pageSpec) != nil {
		return fmt.Errorf("Prototype and PageSpec content must be JSON objects")
	}
	authority := prototypeSemanticAuthority{requirementIDs: map[string]bool{}, acceptanceIDs: map[string]bool{}}
	if len(authorities) > 0 {
		authority = authorities[0]
		if authority.requirementIDs == nil {
			authority.requirementIDs = map[string]bool{}
		}
		if authority.acceptanceIDs == nil {
			authority.acceptanceIDs = map[string]bool{}
		}
	}
	if len(authority.acceptanceIDs) == 0 {
		collectAcceptanceIDs(pageSpec, authority.acceptanceIDs)
	}

	pageSpecStates := map[string]map[string]any{}
	pageSpecStateIDsByKey := map[string]string{}
	for _, state := range objectSlice(pageSpec["states"]) {
		id := strings.TrimSpace(firstString(state, "id"))
		key := strings.TrimSpace(firstString(state, "key", "name"))
		if id != "" {
			pageSpecStates[id] = state
		}
		if key != "" {
			pageSpecStateIDsByKey[key] = id
		}
	}
	prototypeStates := map[string]map[string]any{}
	for _, state := range objectSlice(prototype["states"]) {
		id := strings.TrimSpace(firstString(state, "id"))
		key := strings.TrimSpace(firstString(state, "key"))
		if id != "" {
			prototypeStates[id] = state
		}
		expected := pageSpecStates[id]
		if expected != nil && strings.TrimSpace(firstString(expected, "key", "name")) != key {
			return fmt.Errorf("Prototype state %q changes its PageSpec stable key", id)
		}
		if expectedID := pageSpecStateIDsByKey[key]; key != "" && expectedID != "" && expectedID != id {
			return fmt.Errorf("Prototype state key %q changes its PageSpec stable ID", key)
		}
		if requireCoverage && expected == nil {
			return fmt.Errorf("Prototype state %q is not declared by its PageSpec", id)
		}
		if expected != nil && pageSpecStateRequired(expected) && requireCoverage && !boolean(state["required"]) {
			return fmt.Errorf("Prototype state %q downgrades a required PageSpec state", id)
		}
		if pageStateID := strings.TrimSpace(firstString(state, "pageStateId")); pageStateID != "" {
			if expected == nil || pageStateID != id || strings.TrimSpace(firstString(expected, "key", "name")) != key {
				return fmt.Errorf("Prototype state %q points to the wrong PageSpec stable id/key", id)
			}
		}
	}
	if requireCoverage {
		for id, state := range pageSpecStates {
			candidate := prototypeStates[id]
			if candidate == nil || strings.TrimSpace(firstString(candidate, "key")) != strings.TrimSpace(firstString(state, "key", "name")) {
				return fmt.Errorf("Prototype does not contain PageSpec state %q with the same stable id/key", id)
			}
		}
	}

	layers := prototypeCanonicalLayerObjects(prototype)
	breakpoints := map[string]bool{}
	for _, breakpoint := range objectSlice(prototype["breakpoints"]) {
		if id := strings.TrimSpace(firstString(breakpoint, "id")); id != "" {
			breakpoints[id] = true
		}
	}
	frameCoverage := map[string]bool{}
	for _, frame := range objectSlice(prototype["frames"]) {
		stateID, breakpointID := strings.TrimSpace(firstString(frame, "stateId")), strings.TrimSpace(firstString(frame, "breakpointId"))
		if prototypeStates[stateID] == nil || !breakpoints[breakpointID] || layers[strings.TrimSpace(firstString(frame, "rootLayerId"))] == nil {
			return fmt.Errorf("Prototype frame references an unknown state, breakpoint, or root layer")
		}
		frameCoverage[stateID+"\x00"+breakpointID] = true
	}
	if requireCoverage {
		for stateID, state := range pageSpecStates {
			if !pageSpecStateRequired(state) {
				continue
			}
			for breakpointID := range breakpoints {
				if !frameCoverage[stateID+"\x00"+breakpointID] {
					return fmt.Errorf("required PageSpec state %q has no Prototype frame at breakpoint %q", stateID, breakpointID)
				}
			}
		}
	}

	pageSpecBindings := map[string]map[string]any{}
	allowedOperationIDs := map[string]bool{}
	for _, binding := range objectSlice(pageSpec["dataBindings"]) {
		id := strings.TrimSpace(firstString(binding, "id"))
		if id != "" {
			pageSpecBindings[id] = binding
		}
		if operationID := strings.TrimSpace(firstString(binding, "operationId")); strings.TrimSpace(firstString(binding, "source")) == "api" && operationID != "" {
			allowedOperationIDs[operationID] = true
		}
	}

	declaredFixtureState := map[string]string{}
	for stateID, state := range pageSpecStates {
		for _, fixtureID := range stringSlice(state["fixtureIds"]) {
			if fixtureID = strings.TrimSpace(fixtureID); fixtureID != "" {
				if prior := declaredFixtureState[fixtureID]; prior != "" && prior != stateID {
					return fmt.Errorf("PageSpec fixture %q is declared by multiple states", fixtureID)
				}
				declaredFixtureState[fixtureID] = stateID
			}
		}
	}
	prototypeFixtures := map[string]map[string]any{}
	for _, fixture := range objectSlice(prototype["fixtures"]) {
		id := strings.TrimSpace(firstString(fixture, "id"))
		stateID := strings.TrimSpace(firstString(fixture, "stateId"))
		if id == "" || prototypeFixtures[id] != nil {
			return fmt.Errorf("Prototype fixtures require unique stable IDs")
		}
		prototypeFixtures[id] = fixture
		if prototypeStates[stateID] == nil {
			return fmt.Errorf("Prototype fixture %q references unknown state %q", id, stateID)
		}
		expectedState := declaredFixtureState[id]
		if expectedState != "" && expectedState != stateID {
			return fmt.Errorf("Prototype fixture %q points to the wrong PageSpec state", id)
		}
		if requireCoverage && expectedState == "" {
			return fmt.Errorf("Prototype fixture %q is not declared by its PageSpec", id)
		}
		if operationID := strings.TrimSpace(firstString(fixture, "operationId")); operationID != "" && !allowedOperationIDs[operationID] {
			return fmt.Errorf("Prototype fixture %q references unknown PageSpec operationId %q", id, operationID)
		}
		if requireCoverage {
			if err := validatePrototypeFixtureDTO(fixture); err != nil {
				return fmt.Errorf("Prototype fixture %q: %w", id, err)
			}
		}
	}
	for stateID, state := range prototypeStates {
		fixtureIDs := stringSet(stringSlice(state["fixtureIds"]))
		for fixtureID := range fixtureIDs {
			fixture := prototypeFixtures[fixtureID]
			if fixture == nil || strings.TrimSpace(firstString(fixture, "stateId")) != stateID {
				return fmt.Errorf("Prototype state %q references missing or foreign fixture %q", stateID, fixtureID)
			}
		}
		if requireCoverage && !sameSemanticSet(fixtureIDs, stringSet(stringSlice(pageSpecStates[stateID]["fixtureIds"]))) {
			return fmt.Errorf("Prototype state %q fixtureIds differ from its PageSpec", stateID)
		}
	}
	if requireCoverage {
		for fixtureID := range declaredFixtureState {
			if prototypeFixtures[fixtureID] == nil {
				return fmt.Errorf("Prototype is missing PageSpec fixture %q", fixtureID)
			}
		}
	}

	pageSpecInteractions := map[string]map[string]any{}
	for _, interaction := range objectSlice(pageSpec["interactions"]) {
		if id := strings.TrimSpace(firstString(interaction, "id")); id != "" {
			pageSpecInteractions[id] = interaction
		}
	}
	prototypeInteractions := map[string]map[string]any{}
	referencedBindings := map[string]bool{}
	for _, interaction := range objectSlice(prototype["interactions"]) {
		id := strings.TrimSpace(firstString(interaction, "id"))
		if id == "" || prototypeInteractions[id] != nil {
			return fmt.Errorf("Prototype interactions require unique stable IDs")
		}
		prototypeInteractions[id] = interaction
		expected := pageSpecInteractions[id]
		if requireCoverage && expected == nil {
			return fmt.Errorf("Prototype interaction %q is not declared by its PageSpec", id)
		}
		if expected != nil && strings.TrimSpace(firstString(interaction, "trigger")) != strings.TrimSpace(firstString(expected, "trigger")) {
			return fmt.Errorf("Prototype interaction %q changes its PageSpec trigger", id)
		}
		if layers[strings.TrimSpace(firstString(interaction, "sourceLayerId"))] == nil {
			return fmt.Errorf("Prototype interaction %q references an unknown source layer", id)
		}
		declaredNavigation := ""
		if expected != nil {
			declaredNavigation, _ = canonicalBlueprintAlias(
				"Prototype navigation target", false,
				firstString(expected, "targetPageNodeId"), firstString(expected, "targetPageSpecId"),
			)
		}
		navigationSeen := false
		for _, action := range objectSlice(interaction["actions"]) {
			actionType := strings.TrimSpace(firstString(action, "type"))
			switch actionType {
			case "navigate":
				target, aliasErr := canonicalBlueprintAlias(
					"Prototype navigate action target", false,
					firstString(action, "targetPageNodeId"), firstString(action, "targetPageSpecId"),
				)
				if aliasErr != nil || target == "" || declaredNavigation == "" || target != declaredNavigation {
					return fmt.Errorf("Prototype interaction %q navigate action differs from its PageSpec navigation", id)
				}
				if strings.TrimSpace(firstString(action, "targetStateId")) != "" {
					return fmt.Errorf("Prototype interaction %q cannot pin an unverified remote targetStateId", id)
				}
				navigationSeen = true
			case "setState":
				if prototypeStates[strings.TrimSpace(firstString(action, "stateId"))] == nil {
					return fmt.Errorf("Prototype interaction %q setState references an unknown PageSpec state", id)
				}
			case "openOverlay":
				layer := layers[strings.TrimSpace(firstString(action, "layerId"))]
				if layer == nil || !strings.EqualFold(strings.TrimSpace(firstString(layer, "kind")), "overlay") {
					return fmt.Errorf("Prototype interaction %q openOverlay references an unknown non-overlay layer", id)
				}
			case "closeOverlay":
			case "updateBinding":
				bindingID := strings.TrimSpace(firstString(action, "bindingId"))
				if pageSpecBindings[bindingID] == nil {
					return fmt.Errorf("Prototype interaction %q references unknown PageSpec data binding %q", id, bindingID)
				}
				referencedBindings[bindingID] = true
			case "submitFixture":
				if prototypeFixtures[strings.TrimSpace(firstString(action, "fixtureId"))] == nil {
					return fmt.Errorf("Prototype interaction %q submitFixture references an unknown fixture", id)
				}
			default:
				return fmt.Errorf("Prototype interaction %q contains unsupported action %q", id, actionType)
			}
		}
		if requireCoverage && declaredNavigation != "" && !navigationSeen {
			return fmt.Errorf("Prototype interaction %q does not realize its PageSpec navigation", id)
		}
	}
	if requireCoverage {
		for interactionID := range pageSpecInteractions {
			if prototypeInteractions[interactionID] == nil {
				return fmt.Errorf("Prototype is missing PageSpec interaction %q", interactionID)
			}
		}
	}

	for layerID, layer := range layers {
		if bindingID := strings.TrimSpace(firstString(layer, "dataBindingId")); bindingID != "" {
			if pageSpecBindings[bindingID] == nil {
				return fmt.Errorf("Prototype layer %q references unknown PageSpec data binding %q", layerID, bindingID)
			}
			referencedBindings[bindingID] = true
		}
		if err := validatePrototypeTraceIDs(
			stringSlice(layer["requirementIds"]), stringSlice(layer["acceptanceCriterionIds"]), authority,
		); err != nil {
			return fmt.Errorf("Prototype layer %q: %w", layerID, err)
		}
		if componentRef, exists := layer["componentRef"]; exists && componentRef != nil && !validVersionReference(componentRef) {
			return fmt.Errorf("Prototype layer %q has an invalid componentRef", layerID)
		}
	}
	for bindingID, binding := range pageSpecBindings {
		if requireCoverage && boolean(binding["required"]) && !referencedBindings[bindingID] {
			return fmt.Errorf("Prototype does not realize required PageSpec data binding %q", bindingID)
		}
	}

	if err := validatePrototypeAuxiliaryReferences(prototype, prototypeStates, breakpoints, layers); err != nil {
		return err
	}
	if err := validatePrototypeTraceLinks(prototype, authority, layers, prototypeInteractions); err != nil {
		return err
	}
	return nil
}

func validatePrototypeFixtureDTO(fixture map[string]any) error {
	if strings.TrimSpace(firstString(fixture, "name")) == "" {
		return fmt.Errorf("name is required")
	}
	if _, exists := fixture["response"]; !exists {
		return fmt.Errorf("response is required")
	}
	statusCode, validStatusCode := exactSemanticInteger(fixture["statusCode"])
	if !validStatusCode || statusCode < 100 || statusCode > 599 {
		return fmt.Errorf("statusCode must be an HTTP status")
	}
	latency, validLatency := exactSemanticInteger(fixture["latencyMs"])
	if !validLatency || latency < 0 {
		return fmt.Errorf("latencyMs must be nonnegative")
	}
	if !boolean(fixture["sanitized"]) {
		return fmt.Errorf("sanitized must be true")
	}
	if !domain.IsCanonicalHash(strings.TrimSpace(firstString(fixture, "contentHash"))) {
		return fmt.Errorf("contentHash must be a canonical SHA-256 hash")
	}
	return nil
}

func exactSemanticInteger(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		converted := int(number)
		return converted, float64(converted) == number
	case int:
		return number, true
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	default:
		return 0, false
	}
}

func validatePrototypeTraceIDs(requirementIDs, acceptanceIDs []string, authority prototypeSemanticAuthority) error {
	for _, requirementID := range requirementIDs {
		if requirementID = strings.TrimSpace(requirementID); requirementID == "" || !authority.requirementIDs[requirementID] {
			return fmt.Errorf("references unknown Blueprint Page Requirement %q", requirementID)
		}
	}
	for _, acceptanceID := range acceptanceIDs {
		if acceptanceID = strings.TrimSpace(acceptanceID); acceptanceID == "" || !authority.acceptanceIDs[acceptanceID] {
			return fmt.Errorf("references unknown Blueprint Page acceptance criterion %q", acceptanceID)
		}
	}
	return nil
}

func validatePrototypeAuxiliaryReferences(
	prototype map[string]any,
	states map[string]map[string]any,
	breakpoints map[string]bool,
	layers map[string]map[string]any,
) error {
	seen := map[string]bool{}
	for _, override := range objectSlice(prototype["overrides"]) {
		id := strings.TrimSpace(firstString(override, "id"))
		if id == "" || seen[id] || layers[strings.TrimSpace(firstString(override, "layerId"))] == nil ||
			strings.TrimSpace(firstString(override, "propertyPath")) == "" {
			return fmt.Errorf("Prototype overrides require unique IDs, existing layers, and propertyPath")
		}
		seen[id] = true
		if stateID := strings.TrimSpace(firstString(override, "stateId")); stateID != "" && states[stateID] == nil {
			return fmt.Errorf("Prototype override %q references unknown state", id)
		}
		if breakpointID := strings.TrimSpace(firstString(override, "breakpointId")); breakpointID != "" && !breakpoints[breakpointID] {
			return fmt.Errorf("Prototype override %q references unknown breakpoint", id)
		}
	}
	for field, required := range map[string][]string{
		"tokenBindings":     {"propertyPath", "tokenId"},
		"componentBindings": {"componentId", "componentVersion"},
	} {
		seen = map[string]bool{}
		for _, binding := range objectSlice(prototype[field]) {
			id := strings.TrimSpace(firstString(binding, "id"))
			if id == "" || seen[id] || layers[strings.TrimSpace(firstString(binding, "layerId"))] == nil {
				return fmt.Errorf("Prototype %s require unique IDs and existing layers", field)
			}
			seen[id] = true
			for _, key := range required {
				if strings.TrimSpace(firstString(binding, key)) == "" {
					return fmt.Errorf("Prototype %s %q requires %s", field, id, key)
				}
			}
		}
	}
	for _, asset := range objectSlice(prototype["assets"]) {
		if strings.TrimSpace(firstString(asset, "assetId")) == "" || !domain.IsCanonicalHash(strings.TrimSpace(firstString(asset, "contentHash"))) {
			return fmt.Errorf("Prototype assets require assetId and canonical contentHash")
		}
	}
	return nil
}

func validatePrototypeTraceLinks(
	prototype map[string]any,
	authority prototypeSemanticAuthority,
	layers map[string]map[string]any,
	interactions map[string]map[string]any,
) error {
	seen := map[string]bool{}
	allowedRelations := map[string]bool{"derivesFrom": true, "satisfies": true, "implements": true, "verifies": true, "renders": true}
	upstreamKinds := map[string]bool{
		"requirement": true, "acceptancecriterion": true, "blueprintnode": true, "pagespec": true,
	}
	prototypeKinds := map[string]bool{"prototypelayer": true, "prototypeinteraction": true}
	for _, link := range objectSlice(prototype["traceLinks"]) {
		id := strings.TrimSpace(firstString(link, "id"))
		if id == "" || seen[id] || !allowedRelations[strings.TrimSpace(firstString(link, "relation"))] {
			return fmt.Errorf("Prototype traceLinks require unique IDs and supported relations")
		}
		seen[id] = true
		for endpointIndex, endpointName := range []string{"source", "target"} {
			endpoint, ok := link[endpointName].(map[string]any)
			if !ok {
				return fmt.Errorf("Prototype trace link %q has no %s endpoint", id, endpointName)
			}
			kind := strings.ToLower(strings.TrimSpace(firstString(endpoint, "kind")))
			endpointID := strings.TrimSpace(firstString(endpoint, "id"))
			if endpointIndex == 0 && !upstreamKinds[kind] || endpointIndex == 1 && !prototypeKinds[kind] {
				return fmt.Errorf("Prototype trace link %q must point from an authoritative upstream endpoint to a Prototype endpoint", id)
			}
			valid := false
			expectedRef := VersionRef{}
			switch kind {
			case "requirement":
				valid = authority.requirementIDs[endpointID]
				expectedRef = authority.baselineRef
			case "acceptancecriterion":
				valid = authority.acceptanceIDs[endpointID]
				expectedRef = authority.baselineRef
			case "blueprintnode":
				valid = endpointID != "" && endpointID == authority.pageNodeID
				expectedRef = authority.blueprintRef
			case "pagespec":
				valid = endpointID != "" && endpointID == authority.pageSpecArtifactID
				expectedRef = authority.pageSpecRef
			case "prototypelayer":
				valid = layers[endpointID] != nil
			case "prototypeinteraction":
				valid = interactions[endpointID] != nil
			}
			if !valid {
				return fmt.Errorf("Prototype trace link %q %s references unauthorized %s %q", id, endpointName, kind, endpointID)
			}
			version, carriesVersion := endpoint["version"]
			if endpointIndex == 1 && carriesVersion {
				return fmt.Errorf("Prototype trace link %q target cannot carry an unverified revision", id)
			}
			if endpointIndex == 0 && carriesVersion && !exactSemanticVersionRef(version, expectedRef) {
				return fmt.Errorf("Prototype trace link %q source version does not match its exact frozen authority", id)
			}
		}
	}
	return nil
}

func exactSemanticVersionRef(value any, expected VersionRef) bool {
	reference, ok := value.(map[string]any)
	if !ok || strings.TrimSpace(expected.ArtifactID) == "" || strings.TrimSpace(expected.RevisionID) == "" || strings.TrimSpace(expected.ContentHash) == "" {
		return false
	}
	if strings.TrimSpace(firstString(reference, "artifactId")) != expected.ArtifactID ||
		strings.TrimSpace(firstString(reference, "revisionId")) != expected.RevisionID ||
		strings.TrimSpace(firstString(reference, "contentHash")) != expected.ContentHash {
		return false
	}
	actualAnchor := strings.TrimSpace(firstString(reference, "anchorId"))
	expectedAnchor := ""
	if expected.AnchorID != nil {
		expectedAnchor = strings.TrimSpace(*expected.AnchorID)
	}
	return actualAnchor == expectedAnchor
}

func semanticVersionRef(value any) (VersionRef, bool) {
	reference, ok := value.(map[string]any)
	if !ok {
		return VersionRef{}, false
	}
	result := VersionRef{
		ArtifactID:  strings.TrimSpace(firstString(reference, "artifactId")),
		RevisionID:  strings.TrimSpace(firstString(reference, "revisionId")),
		ContentHash: strings.TrimSpace(firstString(reference, "contentHash")),
	}
	if anchor := strings.TrimSpace(firstString(reference, "anchorId")); anchor != "" {
		result.AnchorID = &anchor
	}
	return result, result.ArtifactID != "" && result.RevisionID != "" && result.ContentHash != ""
}

func collectAcceptanceIDs(content map[string]any, result map[string]bool) {
	for _, value := range stringSlice(content["acceptanceCriterionIds"]) {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = true
		}
	}
	for _, ref := range objectSlice(content["acceptanceRefs"]) {
		if value := strings.TrimSpace(firstString(ref, "acceptanceCriterionId", "id", "key")); value != "" {
			result[value] = true
		}
	}
	for _, field := range []string{"states", "interactions"} {
		for _, item := range objectSlice(content[field]) {
			for _, value := range stringSlice(item["acceptanceCriterionIds"]) {
				if value = strings.TrimSpace(value); value != "" {
					result[value] = true
				}
			}
		}
	}
}

func pageSpecStateRequired(state map[string]any) bool {
	key := strings.ToLower(strings.TrimSpace(firstString(state, "key", "name")))
	return boolean(state["required"]) || key == "ready" || key == "loading" || key == "empty" || key == "error"
}

func semanticStrict(values []bool) bool {
	return len(values) > 0 && values[0]
}

func stringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = true
		}
	}
	return result
}

func sameSemanticSet(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if !right[value] {
			return false
		}
	}
	return true
}
