package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// canonicalizeProposalPatchedContent is intentionally scoped to Proposal
// application. Historical Prototype proposals used the editor's former wire
// aliases, while direct draft and revision writes must continue to satisfy the
// strict canonical artifact contract without migration.
func canonicalizeProposalPatchedContent(kind string, payload json.RawMessage) (json.RawMessage, error) {
	if strings.TrimSpace(kind) != "prototype" || !json.Valid(payload) {
		return payload, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return payload, nil
	}
	prototype, ok := decoded.(map[string]any)
	if !ok {
		return payload, nil
	}
	if err := canonicalizePrototypeTopLevelObjectArrays(prototype); err != nil {
		return nil, err
	}

	if err := canonicalizePrototypePageSpecRevision(prototype); err != nil {
		return nil, err
	}
	if err := canonicalizePrototypeExploratory(prototype); err != nil {
		return nil, err
	}
	states, err := canonicalPrototypeStates(prototype["states"])
	if err != nil {
		return nil, err
	}
	prototype["states"] = states
	breakpoints, err := canonicalPrototypeBreakpoints(prototype["breakpoints"])
	if err != nil {
		return nil, err
	}
	prototype["breakpoints"] = breakpoints
	layers, err := canonicalPrototypeLayers(prototype)
	if err != nil {
		return nil, err
	}
	prototype["layers"] = layers
	frames, err := canonicalPrototypeFrames(
		prototype["frames"], prototype["states"], prototype["breakpoints"],
	)
	if err != nil {
		return nil, err
	}
	prototype["frames"] = frames
	for _, field := range []string{"overrides", "fixtures", "tokenBindings", "assets", "traceLinks"} {
		prototype[field] = prototypeObjectArray(prototype[field])
	}
	interactions, err := canonicalPrototypeInteractions(prototype["interactions"])
	if err != nil {
		return nil, err
	}
	prototype["interactions"] = interactions
	componentBindings, err := canonicalPrototypeComponentBindings(prototype["componentBindings"])
	if err != nil {
		return nil, err
	}
	prototype["componentBindings"] = componentBindings

	return json.Marshal(prototype)
}

func canonicalizePrototypeTopLevelObjectArrays(prototype map[string]any) error {
	for _, field := range []string{
		"states", "breakpoints", "frames", "overrides", "interactions", "fixtures",
		"tokenBindings", "componentBindings", "assets", "traceLinks",
	} {
		raw, exists := prototype[field]
		if !exists {
			prototype[field] = []any{}
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%w: Prototype %s must be an array", ErrBlockingGate, field)
		}
		canonical := make([]any, len(items))
		for index, item := range items {
			if _, ok := item.(map[string]any); !ok {
				return fmt.Errorf(
					"%w: Prototype %s[%d] must be a JSON object", ErrBlockingGate, field, index,
				)
			}
			canonical[index] = item
		}
		prototype[field] = canonical
	}
	return nil
}

func canonicalizePrototypePageSpecRevision(prototype map[string]any) error {
	revision := map[string]any{}
	if raw, exists := prototype["pageSpecRevision"]; exists {
		object, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: Prototype pageSpecRevision must be a JSON object", ErrBlockingGate)
		}
		revision = clonePrototypeObject(object)
	}
	for _, field := range []struct {
		canonical string
		legacy    string
	}{
		{canonical: "artifactId", legacy: "sourcePageSpecArtifactId"},
		{canonical: "revisionId", legacy: "sourcePageSpecRevisionId"},
		{canonical: "contentHash", legacy: "sourcePageSpecHash"},
	} {
		value, exists, err := explicitPrototypeText(
			revision, field.canonical, "Prototype pageSpecRevision."+field.canonical,
		)
		if err != nil {
			return err
		}
		if !exists {
			value, _, err = explicitPrototypeText(
				prototype, field.legacy, "Prototype "+field.legacy,
			)
			if err != nil {
				return err
			}
		}
		revision[field.canonical] = value
	}
	prototype["pageSpecRevision"] = revision
	return nil
}

func canonicalizePrototypeExploratory(prototype map[string]any) error {
	raw, exists := prototype["exploratory"]
	if !exists {
		prototype["exploratory"] = false
		return nil
	}
	exploratory, ok := raw.(bool)
	if !ok {
		return fmt.Errorf("%w: Prototype exploratory must be a boolean", ErrBlockingGate)
	}
	prototype["exploratory"] = exploratory
	return nil
}

func canonicalPrototypeStates(raw any) ([]any, error) {
	states := prototypeObjectArray(raw)
	for index, item := range states {
		state := clonePrototypeObject(item.(map[string]any))
		identifier, _, err := explicitPrototypeText(
			state, "id", fmt.Sprintf("Prototype states[%d].id", index),
		)
		if err != nil {
			return nil, err
		}
		key, keyExists, err := explicitPrototypeText(
			state, "key", fmt.Sprintf("Prototype states[%d].key", index),
		)
		if err != nil {
			return nil, err
		}
		if !keyExists {
			key = identifier
		}
		title, titleExists, err := explicitPrototypeText(
			state, "title", fmt.Sprintf("Prototype states[%d].title", index),
		)
		if err != nil {
			return nil, err
		}
		if !titleExists {
			title = firstPrototypeText(key, identifier)
		}
		state["id"] = identifier
		state["key"] = key
		state["title"] = title
		if rawRequired, exists := state["required"]; exists {
			required, ok := rawRequired.(bool)
			if !ok {
				return nil, fmt.Errorf(
					"%w: Prototype states[%d].required must be a boolean", ErrBlockingGate, index,
				)
			}
			state["required"] = required
		} else {
			state["required"] = false
		}
		fixtureIDs, err := strictPrototypeStringArray(
			state, "fixtureIds", fmt.Sprintf("Prototype states[%d].fixtureIds", index),
		)
		if err != nil {
			return nil, err
		}
		state["fixtureIds"] = fixtureIDs
		pageStateID, pageStateIDExists, err := optionalPrototypeText(
			state, "pageStateId", fmt.Sprintf("Prototype states[%d].pageStateId", index),
		)
		if err != nil {
			return nil, err
		}
		if pageStateIDExists {
			state["pageStateId"] = pageStateID
		}
		states[index] = state
	}
	return states, nil
}

func canonicalPrototypeBreakpoints(raw any) ([]any, error) {
	breakpoints := prototypeObjectArray(raw)
	for index, item := range breakpoints {
		breakpoint := clonePrototypeObject(item.(map[string]any))
		identifier, idExists, err := explicitPrototypeText(
			breakpoint, "id", fmt.Sprintf("Prototype breakpoints[%d].id", index),
		)
		if err != nil {
			return nil, err
		}
		if !idExists {
			identifier, _, err = explicitPrototypeText(
				breakpoint, "key", fmt.Sprintf("Prototype breakpoints[%d].key", index),
			)
			if err != nil {
				return nil, err
			}
		}
		name, nameExists, err := explicitPrototypeText(
			breakpoint, "name", fmt.Sprintf("Prototype breakpoints[%d].name", index),
		)
		if err != nil {
			return nil, err
		}
		if !nameExists {
			for _, alias := range []string{"title", "key"} {
				name, nameExists, err = explicitPrototypeText(
					breakpoint, alias, fmt.Sprintf("Prototype breakpoints[%d].%s", index, alias),
				)
				if err != nil {
					return nil, err
				}
				if nameExists {
					break
				}
			}
			if !nameExists {
				name = identifier
			}
		}
		breakpoint["id"] = identifier
		breakpoint["name"] = name
		if rawMinWidth, exists := breakpoint["minWidth"]; exists {
			if !validNonNegativePrototypeInteger(rawMinWidth) {
				return nil, fmt.Errorf(
					"%w: Prototype breakpoints[%d].minWidth must be a nonnegative integer",
					ErrBlockingGate, index,
				)
			}
			breakpoint["minWidth"] = rawMinWidth
		} else {
			breakpoint["minWidth"] = defaultPrototypeBreakpointMinWidth(name)
		}
		if rawMaxWidth, exists := breakpoint["maxWidth"]; exists {
			if !validNonNegativePrototypeInteger(rawMaxWidth) {
				return nil, fmt.Errorf(
					"%w: Prototype breakpoints[%d].maxWidth must be a nonnegative integer",
					ErrBlockingGate, index,
				)
			}
			breakpoint["maxWidth"] = rawMaxWidth
		}
		defaultWidth, defaultHeight := defaultPrototypeBreakpointViewport(name)
		breakpoint["viewportWidth"] = canonicalPrototypeViewportDimension(
			breakpoint, "viewportWidth", "width", defaultWidth,
		)
		breakpoint["viewportHeight"] = canonicalPrototypeViewportDimension(
			breakpoint, "viewportHeight", "height", defaultHeight,
		)
		breakpoints[index] = breakpoint
	}
	return breakpoints, nil
}

func canonicalPrototypeViewportDimension(
	breakpoint map[string]any,
	canonicalField string,
	legacyField string,
	fallback float64,
) any {
	if explicit, exists := breakpoint[canonicalField]; exists {
		return explicit
	}
	if legacy, exists := breakpoint[legacyField]; exists {
		if _, valid := finitePrototypeNumber(legacy); valid {
			return normalizedPrototypeNumber(legacy, fallback)
		}
		return legacy
	}
	return fallback
}

type prototypeLayerEntry struct {
	recordID string
	layer    map[string]any
}

func canonicalPrototypeLayers(prototype map[string]any) (map[string]any, error) {
	rawLayers, primaryExists := prototype["layers"]
	entries := []prototypeLayerEntry{}
	primaryEmpty := !primaryExists
	var err error
	if primaryExists {
		if rawLayers == nil {
			return nil, fmt.Errorf("%w: $.layers must be a layer array or object record", ErrBlockingGate)
		}
		entries, primaryEmpty, err = strictPrototypeLayerEntries(rawLayers, "$.layers")
		if err != nil {
			return nil, err
		}
	}
	if primaryEmpty {
		rawScene, sceneExists := prototype["scene"]
		if sceneExists && rawScene != nil {
			scene, ok := rawScene.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: Prototype scene must be a JSON object", ErrBlockingGate)
			}
			if rawSceneLayers, layersExist := scene["layers"]; layersExist && rawSceneLayers != nil {
				entries, _, err = strictPrototypeLayerEntries(rawSceneLayers, "$.scene.layers")
				if err != nil {
					return nil, err
				}
			}
		}
	}
	viewportWidth, viewportHeight := prototypeFallbackViewport(prototype["breakpoints"])
	rootLayerIDs := map[string]bool{}
	for _, item := range prototypeObjectArray(prototype["frames"]) {
		frame := item.(map[string]any)
		if rootLayerID := prototypeText(frame["rootLayerId"]); rootLayerID != "" {
			rootLayerIDs[rootLayerID] = true
		}
	}
	layers := make(map[string]any, len(entries))
	seenLayerIDs := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		layer := clonePrototypeObject(entry.layer)
		identifier := entry.recordID
		if _, duplicate := seenLayerIDs[identifier]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Prototype layer id %q", ErrBlockingGate, identifier)
		}
		seenLayerIDs[identifier] = struct{}{}
		kindText, kindExists, err := explicitPrototypeText(
			layer, "kind", fmt.Sprintf("Prototype layer %s kind", identifier),
		)
		if err != nil {
			return nil, err
		}
		if !kindExists {
			kindText, _, err = explicitPrototypeText(
				layer, "type", fmt.Sprintf("Prototype layer %s type", identifier),
			)
			if err != nil {
				return nil, err
			}
		}
		kind := canonicalPrototypeLayerKind(kindText)
		properties := map[string]any{}
		if rawProperties, exists := layer["properties"]; exists {
			canonicalProperties, ok := rawProperties.(map[string]any)
			if !ok {
				return nil, fmt.Errorf(
					"%w: Prototype layer %s properties must be a JSON object", ErrBlockingGate, identifier,
				)
			}
			properties = clonePrototypeObject(canonicalProperties)
		} else if rawProps, exists := layer["props"]; exists {
			props, ok := rawProps.(map[string]any)
			if !ok {
				return nil, fmt.Errorf(
					"%w: Prototype layer %s props must be a JSON object", ErrBlockingGate, identifier,
				)
			}
			properties = clonePrototypeObject(props)
		}
		if kind == "button" {
			if _, textExists := properties["text"]; !textExists {
				label, labelExists, err := explicitPrototypeText(
					properties, "label", fmt.Sprintf("Prototype layer %s properties.label", identifier),
				)
				if err != nil {
					return nil, err
				}
				if labelExists {
					properties["text"] = label
				}
			}
		}

		layer["id"] = identifier
		if parentID, exists, err := optionalPrototypeText(
			layer, "parentId", fmt.Sprintf("Prototype layer %s parentId", identifier),
		); err != nil {
			return nil, err
		} else if exists {
			layer["parentId"] = parentID
		}
		childIDs, err := strictPrototypeStringArray(
			layer, "childIds", fmt.Sprintf("Prototype layer %s childIds", identifier),
		)
		if err != nil {
			return nil, err
		}
		layer["childIds"] = childIDs
		layer["kind"] = kind
		name, nameExists, err := explicitPrototypeText(
			layer, "name", fmt.Sprintf("Prototype layer %s name", identifier),
		)
		if err != nil {
			return nil, err
		}
		if !nameExists {
			name = "Layer " + strconv.Itoa(index+1)
		}
		layer["name"] = name
		semanticRole, semanticRoleExists, err := explicitPrototypeText(
			layer, "semanticRole", fmt.Sprintf("Prototype layer %s semanticRole", identifier),
		)
		if err != nil {
			return nil, err
		}
		if !semanticRoleExists {
			semanticRole, semanticRoleExists, err = explicitPrototypeText(
				properties, "role", fmt.Sprintf("Prototype layer %s properties.role", identifier),
			)
			if err != nil {
				return nil, err
			}
		}
		if semanticRoleExists {
			layer["semanticRole"] = semanticRole
		}
		layout, err := canonicalPrototypeLayerLayout(
			layer, identifier, index, viewportWidth, viewportHeight, rootLayerIDs[identifier],
		)
		if err != nil {
			return nil, err
		}
		layer["layout"] = layout
		style, err := strictPrototypeObject(
			layer, "style", fmt.Sprintf("Prototype layer %s style", identifier),
		)
		if err != nil {
			return nil, err
		}
		layer["style"] = style
		layer["properties"] = properties
		if dataBindingID, exists, err := optionalPrototypeText(
			layer, "dataBindingId", fmt.Sprintf("Prototype layer %s dataBindingId", identifier),
		); err != nil {
			return nil, err
		} else if exists {
			layer["dataBindingId"] = dataBindingID
		}
		requirementIDs, err := strictPrototypeStringArray(
			layer, "requirementIds", fmt.Sprintf("Prototype layer %s requirementIds", identifier),
		)
		if err != nil {
			return nil, err
		}
		layer["requirementIds"] = requirementIDs
		acceptanceCriterionIDs, err := strictPrototypeStringArray(
			layer, "acceptanceCriterionIds",
			fmt.Sprintf("Prototype layer %s acceptanceCriterionIds", identifier),
		)
		if err != nil {
			return nil, err
		}
		layer["acceptanceCriterionIds"] = acceptanceCriterionIDs
		fieldMetadata, err := strictPrototypeObject(
			layer, "fieldMetadata", fmt.Sprintf("Prototype layer %s fieldMetadata", identifier),
		)
		if err != nil {
			return nil, err
		}
		layer["fieldMetadata"] = fieldMetadata
		layers[identifier] = layer
	}
	return layers, nil
}

func canonicalPrototypeLayerLayout(
	layer map[string]any,
	layerID string,
	index int,
	viewportWidth float64,
	viewportHeight float64,
	root bool,
) (map[string]any, error) {
	layout := map[string]any{}
	if raw, exists := layer["layout"]; exists {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"%w: Prototype layer %s layout must be a JSON object", ErrBlockingGate, layerID,
			)
		}
		layout = clonePrototypeObject(object)
	}
	fallbackWidth := math.Max(120, math.Min(640, viewportWidth-48))
	fallbackX, fallbackY, fallbackHeight := float64(24), 24+float64(index*52), float64(44)
	if root {
		fallbackX, fallbackY, fallbackWidth, fallbackHeight = 0, 0, viewportWidth, viewportHeight
	}
	for _, field := range []struct {
		name     string
		fallback float64
		positive bool
	}{
		{name: "x", fallback: fallbackX},
		{name: "y", fallback: fallbackY},
		{name: "width", fallback: fallbackWidth, positive: true},
		{name: "height", fallback: fallbackHeight, positive: true},
	} {
		raw, exists := layout[field.name]
		if !exists {
			layout[field.name] = field.fallback
			continue
		}
		value, valid := finitePrototypeNumber(raw)
		if !valid || (!field.positive && value < 0) || (field.positive && value <= 0) {
			expectation := "nonnegative "
			if field.positive {
				expectation = "positive "
			}
			return nil, fmt.Errorf(
				"%w: Prototype layer %s layout.%s must be a valid %snumber",
				ErrBlockingGate, layerID, field.name, expectation,
			)
		}
		layout[field.name] = raw
	}
	return layout, nil
}

func prototypeFallbackViewport(raw any) (float64, float64) {
	minimumWidth := math.Inf(1)
	maximumHeight := float64(1)
	for _, item := range prototypeObjectArray(raw) {
		breakpoint := item.(map[string]any)
		if width, valid := finitePrototypeNumber(breakpoint["viewportWidth"]); valid && width > 0 {
			minimumWidth = math.Min(minimumWidth, width)
		}
		if height, valid := finitePrototypeNumber(breakpoint["viewportHeight"]); valid && height > 0 {
			maximumHeight = math.Max(maximumHeight, height)
		}
	}
	if math.IsInf(minimumWidth, 1) {
		minimumWidth = 390
	}
	return minimumWidth, maximumHeight
}

func strictPrototypeLayerEntries(raw any, path string) ([]prototypeLayerEntry, bool, error) {
	if items, ok := raw.([]any); ok {
		if len(items) == 0 {
			return []prototypeLayerEntry{}, true, nil
		}
		entries := make([]prototypeLayerEntry, 0, len(items))
		for index, item := range items {
			layer, ok := item.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf(
					"%w: %s[%d] must be a JSON object", ErrBlockingGate, path, index,
				)
			}
			identifier, err := resolvePrototypeLayerID(layer, "", fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, false, err
			}
			entries = append(entries, prototypeLayerEntry{
				recordID: identifier,
				layer:    layer,
			})
		}
		return entries, false, nil
	}
	record, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%w: %s must be a layer array or object record", ErrBlockingGate, path)
	}
	if len(record) == 0 {
		return []prototypeLayerEntry{}, true, nil
	}
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]prototypeLayerEntry, 0, len(keys))
	for _, key := range keys {
		layer, ok := record[key].(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf(
				"%w: %s.%s must be a JSON object", ErrBlockingGate, path, key,
			)
		}
		identifier, err := resolvePrototypeLayerID(layer, key, path+"."+key)
		if err != nil {
			return nil, false, err
		}
		if identifier != key {
			return nil, false, fmt.Errorf(
				"%w: %s record key %q must match its explicit layer id %q",
				ErrBlockingGate, path+"."+key, key, identifier,
			)
		}
		if err := validatePrototypeLayerRecordIdentity(layer, key, path+"."+key); err != nil {
			return nil, false, err
		}
		entries = append(entries, prototypeLayerEntry{recordID: identifier, layer: layer})
	}
	return entries, false, nil
}

func validatePrototypeLayerRecordIdentity(layer map[string]any, recordID string, path string) error {
	for _, field := range []string{"id", "layerId"} {
		identifier, exists, err := explicitPrototypeText(layer, field, path+"."+field)
		if err != nil {
			return err
		}
		if exists && identifier != recordID {
			return fmt.Errorf(
				"%w: %s record key %q must match explicit %s %q",
				ErrBlockingGate, path, recordID, field, identifier,
			)
		}
	}
	return nil
}

func resolvePrototypeLayerID(layer map[string]any, recordID string, path string) (string, error) {
	identifier, exists, err := explicitPrototypeText(layer, "id", path+".id")
	if err != nil {
		return "", err
	}
	if exists {
		return identifier, nil
	}
	identifier, exists, err = explicitPrototypeText(layer, "layerId", path+".layerId")
	if err != nil {
		return "", err
	}
	if exists {
		return identifier, nil
	}
	identifier = strings.TrimSpace(recordID)
	if identifier == "" {
		return "", fmt.Errorf("%w: %s must declare a stable layer id", ErrBlockingGate, path)
	}
	return identifier, nil
}

func canonicalPrototypeFrames(raw, rawStates, rawBreakpoints any) ([]any, error) {
	stateTitles := map[string]string{}
	for _, item := range prototypeObjectArray(rawStates) {
		state := item.(map[string]any)
		stateTitles[prototypeText(state["id"])] = prototypeText(state["title"])
	}
	breakpointNames := map[string]string{}
	for _, item := range prototypeObjectArray(rawBreakpoints) {
		breakpoint := item.(map[string]any)
		breakpointNames[prototypeText(breakpoint["id"])] = prototypeText(breakpoint["name"])
	}

	frames := prototypeObjectArray(raw)
	for index, item := range frames {
		frame := clonePrototypeObject(item.(map[string]any))
		identifier, _, err := explicitPrototypeText(
			frame, "id", fmt.Sprintf("Prototype frames[%d].id", index),
		)
		if err != nil {
			return nil, err
		}
		stateID, _, err := explicitPrototypeText(
			frame, "stateId", fmt.Sprintf("Prototype frames[%d].stateId", index),
		)
		if err != nil {
			return nil, err
		}
		breakpointID, _, err := explicitPrototypeText(
			frame, "breakpointId", fmt.Sprintf("Prototype frames[%d].breakpointId", index),
		)
		if err != nil {
			return nil, err
		}
		rootLayerID, _, err := explicitPrototypeText(
			frame, "rootLayerId", fmt.Sprintf("Prototype frames[%d].rootLayerId", index),
		)
		if err != nil {
			return nil, err
		}
		title, titleExists, err := explicitPrototypeText(
			frame, "title", fmt.Sprintf("Prototype frames[%d].title", index),
		)
		if err != nil {
			return nil, err
		}
		if !titleExists {
			title = firstPrototypeText(stateTitles[stateID], "State") + " · " +
				firstPrototypeText(breakpointNames[breakpointID], "Breakpoint")
		}
		frame["id"] = identifier
		frame["stateId"] = stateID
		frame["breakpointId"] = breakpointID
		frame["rootLayerId"] = rootLayerID
		frame["title"] = title
		frames[index] = frame
	}
	return frames, nil
}

func canonicalPrototypeInteractions(raw any) ([]any, error) {
	interactions := prototypeObjectArray(raw)
	for index, item := range interactions {
		interaction := clonePrototypeObject(item.(map[string]any))
		guards, err := strictPrototypeNestedObjectArray(interaction, "guards", index)
		if err != nil {
			return nil, err
		}
		actions, err := strictPrototypeNestedObjectArray(interaction, "actions", index)
		if err != nil {
			return nil, err
		}
		interaction["guards"] = guards
		interaction["actions"] = actions
		interactions[index] = interaction
	}
	return interactions, nil
}

func strictPrototypeNestedObjectArray(
	interaction map[string]any,
	field string,
	interactionIndex int,
) ([]any, error) {
	raw, exists := interaction[field]
	if !exists {
		return []any{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf(
			"%w: Prototype interactions[%d].%s must be an array",
			ErrBlockingGate, interactionIndex, field,
		)
	}
	canonical := make([]any, len(items))
	for index, item := range items {
		if _, ok := item.(map[string]any); !ok {
			return nil, fmt.Errorf(
				"%w: Prototype interactions[%d].%s[%d] must be a JSON object",
				ErrBlockingGate, interactionIndex, field, index,
			)
		}
		canonical[index] = item
	}
	return canonical, nil
}

func canonicalPrototypeComponentBindings(raw any) ([]any, error) {
	bindings := prototypeObjectArray(raw)
	for index, item := range bindings {
		binding := clonePrototypeObject(item.(map[string]any))
		propertyMapping, err := strictPrototypeObject(
			binding, "propertyMapping",
			fmt.Sprintf("Prototype componentBindings[%d].propertyMapping", index),
		)
		if err != nil {
			return nil, err
		}
		binding["propertyMapping"] = propertyMapping
		bindings[index] = binding
	}
	return bindings, nil
}

func canonicalPrototypeLayerKind(raw any) string {
	kind := prototypeText(raw)
	switch kind {
	case "frame", "group", "text", "image", "componentInstance", "input", "button", "list", "overlay", "slot":
		return kind
	}
	switch strings.ToLower(kind) {
	case "screen":
		return "frame"
	case "section", "container", "card":
		return "group"
	case "component":
		return "componentInstance"
	case "heading", "label", "paragraph":
		return "text"
	default:
		return kind
	}
}

func prototypeObjectArray(raw any) []any {
	items, ok := raw.([]any)
	if !ok {
		return []any{}
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func strictPrototypeStringArray(object map[string]any, field string, path string) ([]any, error) {
	raw, exists := object[field]
	if !exists {
		return []any{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: %s must be an array", ErrBlockingGate, path)
	}
	result := make([]any, len(items))
	for index, item := range items {
		text, ok := item.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" {
			return nil, fmt.Errorf(
				"%w: %s[%d] must be a non-empty string", ErrBlockingGate, path, index,
			)
		}
		result[index] = text
	}
	return result, nil
}

func strictPrototypeObject(object map[string]any, field string, path string) (map[string]any, error) {
	raw, exists := object[field]
	if !exists {
		return map[string]any{}, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: %s must be a JSON object", ErrBlockingGate, path)
	}
	return clonePrototypeObject(value), nil
}

func prototypeObject(raw any) map[string]any {
	object, _ := raw.(map[string]any)
	return object
}

func clonePrototypeObject(raw map[string]any) map[string]any {
	result := make(map[string]any, len(raw))
	for key, value := range raw {
		result[key] = value
	}
	return result
}

func firstPrototypeText(values ...any) string {
	for _, value := range values {
		if text := prototypeText(value); text != "" {
			return text
		}
	}
	return ""
}

func explicitPrototypeText(object map[string]any, field string, path string) (string, bool, error) {
	raw, exists := object[field]
	if !exists {
		return "", false, nil
	}
	text, ok := raw.(string)
	text = strings.TrimSpace(text)
	if !ok || text == "" {
		return "", true, fmt.Errorf("%w: %s must be a non-empty string", ErrBlockingGate, path)
	}
	return text, true, nil
}

func optionalPrototypeText(object map[string]any, field string, path string) (string, bool, error) {
	raw, exists := object[field]
	if !exists {
		return "", false, nil
	}
	if raw == nil {
		delete(object, field)
		return "", false, nil
	}
	text, ok := raw.(string)
	text = strings.TrimSpace(text)
	if !ok || text == "" {
		return "", true, fmt.Errorf("%w: %s must be null or a non-empty string", ErrBlockingGate, path)
	}
	return text, true, nil
}

func prototypeText(raw any) string {
	text, _ := raw.(string)
	return strings.TrimSpace(text)
}

func normalizedPrototypeNumber(raw any, fallback float64) float64 {
	value, valid := finitePrototypeNumber(raw)
	if !valid {
		return fallback
	}
	return math.Round(math.Max(0, value))
}

func finitePrototypeNumber(raw any) (float64, bool) {
	var value float64
	var err error
	switch number := raw.(type) {
	case json.Number:
		value, err = number.Float64()
	case float64:
		value = number
	case float32:
		value = float64(number)
	case int:
		value = float64(number)
	case int64:
		value = float64(number)
	default:
		return 0, false
	}
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validNonNegativePrototypeInteger(raw any) bool {
	value, valid := finitePrototypeNumber(raw)
	return valid && value >= 0 && math.Trunc(value) == value && value <= float64(math.MaxInt64)
}

func defaultPrototypeBreakpointMinWidth(name string) float64 {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "desktop":
		return 1024
	case "tablet":
		return 768
	default:
		return 0
	}
}

func defaultPrototypeBreakpointViewport(name string) (float64, float64) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "desktop":
		return 1440, 900
	case "tablet":
		return 768, 1024
	case "mobile":
		return 390, 844
	default:
		return 1440, 900
	}
}
