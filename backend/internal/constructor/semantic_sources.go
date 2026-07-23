package constructor

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/contracts"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
)

var canonicalPageStateKeys = []string{"ready", "loading", "empty", "error"}

type semanticSourceValidation struct {
	validPageSpecRevisions map[string]bool
}

type blueprintSemanticSource struct {
	ref   ExactRevisionRef
	pages map[string]core.BlueprintSemanticNode
	valid bool
}

type pageStateSemantic struct {
	id       string
	key      string
	title    string
	required bool
}

type pageSpecSemanticSource struct {
	ref           ExactRevisionRef
	pageNodeID    string
	states        map[string]pageStateSemantic
	stateIDsByKey map[string]string
	valid         bool
}

// validateSemanticSources is the fail-closed semantic boundary for the three
// human-authored implementation authorities. Exact revision/hash/approval
// checks happen before this function; this layer proves that the frozen
// payloads describe one coherent Blueprint Page -> PageSpec -> Prototype
// lineage before PageSpec constraints can enter a ready BuildContract.
func validateSemanticSources(
	sources map[string][]PinnedBuildSource,
	deliverySlicePageNodeID string,
	contractFacts map[string]contracts.Facts,
	diagnostics *diagnosticBuilder,
) semanticSourceValidation {
	blueprint := validateBlueprintSemanticSource(sources["blueprint"], diagnostics)
	pageSpec := validatePageSpecSemanticSource(sources["page_spec"], blueprint, diagnostics)
	prototypeValid := validatePrototypeSemanticSource(sources["prototype"], blueprint, pageSpec, diagnostics)
	deliverySliceValid := validateDeliverySlicePage(deliverySlicePageNodeID, pageSpec, diagnostics)
	authority, authorityValid := validateExactSemanticAuthoritySources(sources, diagnostics)
	apiClosureValid := false
	if authorityValid {
		apiClosureValid = validateAPIOperationClosure(
			authority, contractFacts[contracts.KindAPI],
			singleSourceRevisionID(sources["blueprint"]), singleSourceRevisionID(sources[contracts.KindAPI]),
			diagnostics,
		)
	}

	validPageSpecs := map[string]bool{}
	if blueprint.valid && pageSpec.valid && prototypeValid && deliverySliceValid && authorityValid && apiClosureValid {
		validPageSpecs[pageSpec.ref.RevisionID] = true
	}
	return semanticSourceValidation{validPageSpecRevisions: validPageSpecs}
}

func validateDeliverySlicePage(
	deliverySlicePageNodeID string,
	pageSpec pageSpecSemanticSource,
	diagnostics *diagnosticBuilder,
) bool {
	if strings.TrimSpace(deliverySlicePageNodeID) == "" || strings.TrimSpace(deliverySlicePageNodeID) != pageSpec.pageNodeID {
		diagnostics.gap(
			"semantic-authority", "delivery_slice_page_mismatch", "$.deliverySliceId",
			"Delivery slice Blueprint anchor must exactly equal the PageSpec blueprintPageNodeId.",
			pageSpec.ref.RevisionID, nil,
		)
		return false
	}
	return true
}

func validateExactSemanticAuthoritySources(
	sources map[string][]PinnedBuildSource,
	diagnostics *diagnosticBuilder,
) (core.ExactSemanticAuthority, bool) {
	for _, kind := range []string{"requirement_baseline", "blueprint", "page_spec", "prototype"} {
		if len(sources[kind]) != 1 {
			return core.ExactSemanticAuthority{}, false
		}
	}
	authority, err := core.ValidateExactSemanticAuthority(core.ExactSemanticAuthorityInput{
		RequirementBaseline: exactSemanticArtifact(sources["requirement_baseline"][0]),
		Blueprint:           exactSemanticArtifact(sources["blueprint"][0]),
		PageSpec:            exactSemanticArtifact(sources["page_spec"][0]),
		Prototype:           exactSemanticArtifact(sources["prototype"][0]),
	})
	if err == nil {
		return authority, true
	}
	code, path := "semantic_authority_invalid", "$.sourceRevisions"
	var authorityError *core.ExactSemanticAuthorityError
	if errors.As(err, &authorityError) {
		if strings.TrimSpace(authorityError.Code) != "" {
			code = authorityError.Code
		}
		if strings.TrimSpace(authorityError.Path) != "" {
			path = authorityError.Path
		}
	}
	diagnostics.gap(
		"semantic-authority", code, path,
		"Exact semantic authority is invalid: "+err.Error()+".",
		semanticAuthoritySourceID(path, sources), nil,
	)
	return core.ExactSemanticAuthority{}, false
}

func exactSemanticArtifact(source PinnedBuildSource) core.ExactSemanticArtifact {
	return core.ExactSemanticArtifact{
		Payload: source.Content,
		Revision: core.VersionRef{
			ArtifactID: source.Ref.ArtifactID, RevisionID: source.Ref.RevisionID,
			ContentHash: source.Ref.ContentHash,
		},
	}
}

func semanticAuthoritySourceID(path string, sources map[string][]PinnedBuildSource) string {
	kind := "page_spec"
	switch {
	case strings.Contains(path, "requirementBaseline"):
		kind = "requirement_baseline"
	case strings.Contains(path, "blueprint"):
		kind = "blueprint"
	case strings.Contains(path, "prototype"):
		kind = "prototype"
	}
	return singleSourceRevisionID(sources[kind])
}

func singleSourceRevisionID(sources []PinnedBuildSource) string {
	if len(sources) != 1 {
		return ""
	}
	return sources[0].Ref.RevisionID
}

func validateAPIOperationClosure(
	authority core.ExactSemanticAuthority,
	apiFacts contracts.Facts,
	blueprintRevisionID string,
	apiRevisionID string,
	diagnostics *diagnosticBuilder,
) bool {
	if apiFacts.Kind != contracts.KindAPI {
		return false
	}
	valid := true
	factsByID := make(map[string]contracts.APIOperation, len(apiFacts.Operations))
	for _, operation := range apiFacts.Operations {
		factsByID[operation.ID] = operation
	}
	owned := make(map[string]bool, len(authority.OwnedAPIOperations))
	for _, expected := range authority.OwnedAPIOperations {
		owned[expected.ID] = true
		actual, exists := factsByID[expected.ID]
		if !exists {
			diagnostics.gap(
				"semantic-authority", "blueprint_api_operation_missing",
				"$.semantic.nodes[id="+expected.ID+"]",
				"API Contract must declare the operation owned by the delivery slice Blueprint Page.",
				blueprintRevisionID, nil,
			)
			valid = false
			continue
		}
		if expected.Method == "" || expected.Path == "" || actual.Method != expected.Method || actual.Path != expected.Path {
			diagnostics.gap(
				"semantic-authority", "blueprint_api_operation_conflict",
				"$.semantic.nodes[id="+expected.ID+"]",
				fmt.Sprintf(
					"Blueprint API operation %s %s %s must exactly match API Contract operation %s %s %s.",
					expected.ID, expected.Method, expected.Path, actual.ID, actual.Method, actual.Path,
				),
				blueprintRevisionID, nil,
			)
			valid = false
		}
	}
	for _, actual := range apiFacts.Operations {
		if owned[actual.ID] {
			continue
		}
		diagnostics.gap(
			"semantic-authority", "api_contract_operation_outside_delivery_slice",
			"$.paths[operationId="+actual.ID+"]",
			"API Contract contains an operation not declared and owned by the delivery slice Blueprint Page.",
			apiRevisionID, nil,
		)
		valid = false
	}
	return valid
}

func validateBlueprintSemanticSource(
	sources []PinnedBuildSource,
	diagnostics *diagnosticBuilder,
) blueprintSemanticSource {
	result := blueprintSemanticSource{pages: map[string]core.BlueprintSemanticNode{}}
	if len(sources) != 1 {
		return result
	}
	source := sources[0]
	result.ref = source.Ref
	result.valid = addArtifactSchemaFindings("blueprint", source, diagnostics)

	nodes, _, err := core.DecodeBlueprintSemanticGraph(source.Content)
	if err != nil {
		diagnostics.gap(
			"blueprint", "blueprint_semantic_invalid", "$.semantic",
			"Blueprint semantic graph is invalid: "+err.Error()+".", source.Ref.RevisionID, nil,
		)
		if strings.Contains(strings.ToLower(err.Error()), "no semantic page") {
			diagnostics.gap(
				"blueprint", "blueprint_page_missing", "$.semantic.nodes",
				"Blueprint must contain at least one semantic Page node.", source.Ref.RevisionID, nil,
			)
		}
		result.valid = false
		return result
	}
	for _, node := range nodes {
		if strings.EqualFold(node.Kind, "page") {
			result.pages[node.ID] = node
		}
	}
	if len(result.pages) == 0 {
		diagnostics.gap(
			"blueprint", "blueprint_page_missing", "$.semantic.nodes",
			"Blueprint must contain at least one semantic Page node.", source.Ref.RevisionID, nil,
		)
		result.valid = false
	}
	return result
}

func validatePageSpecSemanticSource(
	sources []PinnedBuildSource,
	blueprint blueprintSemanticSource,
	diagnostics *diagnosticBuilder,
) pageSpecSemanticSource {
	result := pageSpecSemanticSource{
		states: map[string]pageStateSemantic{}, stateIDsByKey: map[string]string{},
	}
	if len(sources) != 1 {
		return result
	}
	source := sources[0]
	result.ref = source.Ref
	result.valid = addArtifactSchemaFindings("page_spec", source, diagnostics)

	var page map[string]any
	if err := json.Unmarshal(source.Content, &page); err != nil || page == nil {
		diagnostics.gap(
			"page", "page_spec_invalid", "$", "PageSpec must be a JSON object.",
			source.Ref.RevisionID, nil,
		)
		result.valid = false
		return result
	}
	result.pageNodeID = stringField(page, "blueprintPageNodeId")
	blueprintPage, pageExists := blueprint.pages[result.pageNodeID]
	if result.pageNodeID == "" || !pageExists {
		diagnostics.gap(
			"page", "page_spec_blueprint_page_unknown", "$.blueprintPageNodeId",
			"PageSpec blueprintPageNodeId must identify a semantic Page in the exact Blueprint revision.",
			source.Ref.RevisionID, nil,
		)
		result.valid = false
	} else if !blueprint.valid {
		diagnostics.gap(
			"page", "page_spec_blueprint_unavailable", "$.blueprintPageNodeId",
			"PageSpec cannot bind an invalid exact Blueprint semantic graph.", source.Ref.RevisionID, nil,
		)
		result.valid = false
	} else if stringField(page, "title") != blueprintPage.Title ||
		stringField(page, "route") != blueprintPage.Route ||
		firstStringField(page, "userGoal", "goal") != blueprintPage.UserGoal {
		diagnostics.gap(
			"page", "page_spec_blueprint_page_conflict", "$.blueprintPageNodeId",
			"PageSpec title, route, and user goal must preserve the selected exact Blueprint Page semantics.",
			source.Ref.RevisionID, nil,
		)
		result.valid = false
	}

	states, statesValid := requiredObjectArray(page, "states")
	if !statesValid {
		diagnostics.gap(
			"page", "page_spec_states_invalid", "$.states",
			"PageSpec states must be an explicit array of state objects.", source.Ref.RevisionID, nil,
		)
		result.valid = false
		return result
	}
	for index, state := range states {
		id := stringField(state, "id")
		key := firstStringField(state, "key", "name")
		title := stringField(state, "title")
		required, requiredOK := state["required"].(bool)
		if id == "" || key == "" || title == "" || !requiredOK {
			diagnostics.gap(
				"page", "page_spec_state_invalid", fmt.Sprintf("$.states[%d]", index),
				"Every PageSpec state must declare a stable ID, key, title, and explicit required flag.",
				source.Ref.RevisionID, nil,
			)
			result.valid = false
			continue
		}
		if _, duplicate := result.states[id]; duplicate || result.stateIDsByKey[key] != "" {
			diagnostics.gap(
				"page", "page_spec_state_duplicate", fmt.Sprintf("$.states[%d]", index),
				"PageSpec state IDs and keys must be unique.", source.Ref.RevisionID, nil,
			)
			result.valid = false
			continue
		}
		result.states[id] = pageStateSemantic{id: id, key: key, title: title, required: required}
		result.stateIDsByKey[key] = id
	}
	for _, key := range canonicalPageStateKeys {
		id := result.stateIDsByKey[key]
		state, exists := result.states[id]
		if !exists {
			diagnostics.gap(
				"page", "page_spec_required_state_missing", "$.states[key="+key+"]",
				"PageSpec must declare the canonical "+key+" state.", source.Ref.RevisionID, nil,
			)
			result.valid = false
			continue
		}
		if !state.required {
			diagnostics.gap(
				"page", "page_spec_required_state_not_required", "$.states[key="+key+"].required",
				"The canonical "+key+" PageSpec state must be required.", source.Ref.RevisionID, nil,
			)
			result.valid = false
		}
	}
	return result
}

func validatePrototypeSemanticSource(
	sources []PinnedBuildSource,
	blueprint blueprintSemanticSource,
	pageSpec pageSpecSemanticSource,
	diagnostics *diagnosticBuilder,
) bool {
	if len(sources) != 1 {
		return false
	}
	source := sources[0]
	valid := addArtifactSchemaFindings("prototype", source, diagnostics)

	var prototype map[string]any
	if err := json.Unmarshal(source.Content, &prototype); err != nil || prototype == nil {
		diagnostics.gap(
			"prototype", "prototype_invalid", "$", "Prototype must be a JSON object.",
			source.Ref.RevisionID, nil,
		)
		return false
	}
	if exploratory, exists := prototype["exploratory"].(bool); !exists || exploratory {
		diagnostics.gap(
			"prototype", "prototype_formal_required", "$.exploratory",
			"BuildContract requires an explicit non-exploratory Prototype.", source.Ref.RevisionID, nil,
		)
		valid = false
	}

	revision, revisionOK := prototype["pageSpecRevision"].(map[string]any)
	if !revisionOK || stringField(revision, "artifactId") == "" ||
		stringField(revision, "revisionId") == "" ||
		!domain.IsCanonicalHash(stringField(revision, "contentHash")) ||
		stringField(revision, "anchorId") != "" {
		diagnostics.gap(
			"prototype", "prototype_page_spec_revision_invalid", "$.pageSpecRevision",
			"Prototype must pin one whole exact PageSpec artifact, revision, and canonical content hash.",
			source.Ref.RevisionID, nil,
		)
		valid = false
	} else if len(pageSpec.ref.RevisionID) == 0 ||
		stringField(revision, "artifactId") != pageSpec.ref.ArtifactID ||
		stringField(revision, "revisionId") != pageSpec.ref.RevisionID ||
		stringField(revision, "contentHash") != pageSpec.ref.ContentHash {
		diagnostics.gap(
			"prototype", "prototype_page_spec_revision_mismatch", "$.pageSpecRevision",
			"Prototype pageSpecRevision must exactly match the frozen PageSpec source pin.",
			source.Ref.RevisionID, nil,
		)
		valid = false
	}
	if !pageSpec.valid || !blueprint.valid || pageSpec.pageNodeID == "" || blueprint.pages[pageSpec.pageNodeID].ID == "" {
		diagnostics.gap(
			"prototype", "prototype_blueprint_page_unresolved", "$.pageSpecRevision",
			"Prototype must trace through its exact PageSpec to a semantic Page in the exact Blueprint revision.",
			source.Ref.RevisionID, nil,
		)
		valid = false
	}

	prototypeStates, statesValid := requiredObjectArray(prototype, "states")
	if !statesValid {
		diagnostics.gap(
			"prototype", "prototype_states_invalid", "$.states",
			"Prototype states must be an explicit array of state objects.", source.Ref.RevisionID, nil,
		)
		return false
	}
	statesByID := map[string]pageStateSemantic{}
	stateIDsByKey := map[string]string{}
	for index, state := range prototypeStates {
		id := stringField(state, "id")
		key := stringField(state, "key")
		title := stringField(state, "title")
		required, requiredOK := state["required"].(bool)
		if id == "" || key == "" || title == "" || !requiredOK {
			diagnostics.gap(
				"prototype", "prototype_state_invalid", fmt.Sprintf("$.states[%d]", index),
				"Every Prototype state must declare a stable ID, key, title, and explicit required flag.",
				source.Ref.RevisionID, nil,
			)
			valid = false
			continue
		}
		if _, duplicate := statesByID[id]; duplicate || stateIDsByKey[key] != "" {
			diagnostics.gap(
				"prototype", "prototype_state_duplicate", fmt.Sprintf("$.states[%d]", index),
				"Prototype state IDs and keys must be unique.", source.Ref.RevisionID, nil,
			)
			valid = false
			continue
		}
		actual := pageStateSemantic{id: id, key: key, title: title, required: required}
		statesByID[id] = actual
		stateIDsByKey[key] = id
		if pageStateID := stringField(state, "pageStateId"); pageStateID != "" && pageStateID != id {
			diagnostics.gap(
				"prototype", "prototype_page_state_conflict", fmt.Sprintf("$.states[%d].pageStateId", index),
				"Prototype pageStateId must preserve the exact PageSpec state ID.", source.Ref.RevisionID, nil,
			)
			valid = false
		}
		if pageSpec.valid {
			expected, exists := pageSpec.states[id]
			if !exists || expected.key != key || expected.title != title || expected.required != required || pageSpec.stateIDsByKey[key] != id {
				diagnostics.gap(
					"prototype", "prototype_page_state_conflict", fmt.Sprintf("$.states[%d]", index),
					"Prototype states must preserve the exact PageSpec state ID, key, title, and required flag.",
					source.Ref.RevisionID, nil,
				)
				valid = false
			}
		}
	}
	if pageSpec.valid {
		for id, expected := range pageSpec.states {
			actual, exists := statesByID[id]
			if !exists || actual.key != expected.key || actual.title != expected.title || actual.required != expected.required {
				diagnostics.gap(
					"prototype", "prototype_page_state_missing", "$.states[id="+id+"]",
					"Prototype must contain every exact PageSpec state without semantic drift.",
					source.Ref.RevisionID, nil,
				)
				valid = false
			}
		}
	}

	breakpoints, breakpointsValid := requiredObjectArray(prototype, "breakpoints")
	breakpointIDs := map[string]bool{}
	if !breakpointsValid || len(breakpoints) == 0 {
		diagnostics.gap(
			"prototype", "prototype_breakpoints_invalid", "$.breakpoints",
			"Prototype must declare explicit breakpoint objects for frame coverage.", source.Ref.RevisionID, nil,
		)
		valid = false
	} else {
		for index, breakpoint := range breakpoints {
			id := stringField(breakpoint, "id")
			if id == "" || breakpointIDs[id] {
				diagnostics.gap(
					"prototype", "prototype_breakpoint_invalid", fmt.Sprintf("$.breakpoints[%d]", index),
					"Prototype breakpoint IDs must be stable and unique.", source.Ref.RevisionID, nil,
				)
				valid = false
				continue
			}
			breakpointIDs[id] = true
		}
	}

	layers, layersValid := prototype["layers"].(map[string]any)
	layerIDs := map[string]bool{}
	if !layersValid || len(layers) == 0 {
		diagnostics.gap(
			"prototype", "prototype_layers_invalid", "$.layers",
			"Prototype layers must be a non-empty object record.", source.Ref.RevisionID, nil,
		)
		valid = false
	} else {
		for id, raw := range layers {
			if strings.TrimSpace(id) == "" {
				valid = false
				continue
			}
			if _, object := raw.(map[string]any); !object {
				diagnostics.gap(
					"prototype", "prototype_layer_invalid", "$.layers."+id,
					"Every Prototype layer record value must be an object.", source.Ref.RevisionID, nil,
				)
				valid = false
				continue
			}
			layerIDs[id] = true
		}
	}

	frames, framesValid := requiredObjectArray(prototype, "frames")
	coverage := map[string]bool{}
	frameIDs := map[string]bool{}
	if !framesValid || len(frames) == 0 {
		diagnostics.gap(
			"prototype", "prototype_frames_invalid", "$.frames",
			"Prototype frames must be a non-empty array of frame objects.", source.Ref.RevisionID, nil,
		)
		valid = false
	} else {
		for index, frame := range frames {
			id := stringField(frame, "id")
			stateID := stringField(frame, "stateId")
			breakpointID := stringField(frame, "breakpointId")
			rootLayerID := stringField(frame, "rootLayerId")
			pair := stateID + "\x00" + breakpointID
			if id == "" || frameIDs[id] || stateID == "" || breakpointID == "" || rootLayerID == "" ||
				statesByID[stateID].id == "" || !breakpointIDs[breakpointID] || !layerIDs[rootLayerID] || coverage[pair] {
				diagnostics.gap(
					"prototype", "prototype_frame_invalid", fmt.Sprintf("$.frames[%d]", index),
					"Every Prototype frame must be unique and reference an exact state, breakpoint, and root layer.",
					source.Ref.RevisionID, nil,
				)
				valid = false
				continue
			}
			frameIDs[id] = true
			coverage[pair] = true
		}
	}
	if pageSpec.valid {
		for stateID, state := range pageSpec.states {
			if !state.required {
				continue
			}
			for breakpointID := range breakpointIDs {
				if !coverage[stateID+"\x00"+breakpointID] {
					diagnostics.gap(
						"prototype", "prototype_page_state_frame_missing",
						"$.frames[state="+stateID+"][breakpoint="+breakpointID+"]",
						"Every required PageSpec state must have one Prototype frame at every declared breakpoint.",
						source.Ref.RevisionID, nil,
					)
					valid = false
				}
			}
		}
	}

	return valid
}

func addArtifactSchemaFindings(
	kind string,
	source PinnedBuildSource,
	diagnostics *diagnosticBuilder,
) bool {
	report := core.ValidateArtifactContent(kind, source.Content)
	blockers := 0
	for _, finding := range report.Findings {
		if finding.Severity != "blocker" {
			continue
		}
		blockers++
		code := strings.TrimSpace(finding.Code)
		if code == "" {
			code = kind + "_schema_invalid"
		}
		diagnostics.gap(
			kind+"-schema", code, finding.Path, finding.Message,
			source.Ref.RevisionID, nil,
		)
	}
	if !report.Valid && blockers == 0 {
		diagnostics.gap(
			kind+"-schema", kind+"_schema_invalid", "$",
			"Exact "+kind+" content failed its canonical schema gate.", source.Ref.RevisionID, nil,
		)
	}
	return report.Valid
}

func requiredObjectArray(value map[string]any, field string) ([]map[string]any, bool) {
	raw, exists := value[field]
	if !exists {
		return nil, false
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		object, objectOK := item.(map[string]any)
		if !objectOK {
			return nil, false
		}
		result = append(result, object)
	}
	return result, true
}
