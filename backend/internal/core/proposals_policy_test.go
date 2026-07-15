package core

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestOnlyExactManifestBaseMayBeAnUnapprovedSource(t *testing.T) {
	anchor := "brief"
	base := VersionRef{
		ArtifactID:  "project-brief",
		RevisionID:  "project-brief-r1",
		ContentHash: "sha256:project-brief-r1",
		AnchorID:    &anchor,
	}
	if !manifestSourceIsExactBase(&base, base) {
		t.Fatal("the exact base revision must be admissible as the editable manifest source")
	}
	if manifestSourceIsExactBase(nil, base) {
		t.Fatal("an unapproved formal source without a base revision must stay blocked")
	}
	for name, source := range map[string]VersionRef{
		"artifact": {ArtifactID: "other", RevisionID: base.RevisionID, ContentHash: base.ContentHash, AnchorID: base.AnchorID},
		"revision": {ArtifactID: base.ArtifactID, RevisionID: "other", ContentHash: base.ContentHash, AnchorID: base.AnchorID},
		"hash":     {ArtifactID: base.ArtifactID, RevisionID: base.RevisionID, ContentHash: "sha256:other", AnchorID: base.AnchorID},
		"anchor":   {ArtifactID: base.ArtifactID, RevisionID: base.RevisionID, ContentHash: base.ContentHash},
	} {
		t.Run(name, func(t *testing.T) {
			if manifestSourceIsExactBase(&base, source) {
				t.Fatalf("mismatched %s source bypassed the formal approval policy", name)
			}
		})
	}
}

func TestProposalPatchedContentMustPassArtifactValidationBeforeApply(t *testing.T) {
	t.Parallel()
	if err := validateProposalPatchedContent("prototype", canonicalPrototypeValidationPayload()); err != nil {
		t.Fatalf("canonical Prototype patch was rejected: %v", err)
	}

	var incomplete map[string]any
	if err := json.Unmarshal(canonicalPrototypeValidationPayload(), &incomplete); err != nil {
		t.Fatal(err)
	}
	delete(incomplete, "tokenBindings")
	payload, err := json.Marshal(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	err = validateProposalPatchedContent("prototype", payload)
	if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "prototype.array_contract") ||
		!strings.Contains(err.Error(), "$.tokenBindings") {
		t.Fatalf("invalid Prototype patch did not fail closed with validation evidence: %v", err)
	}
}

func TestPrototypeProposalCanonicalizationDoesNotInventSemanticContent(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{
  "sourcePageSpecArtifactId":"page-spec-1",
  "sourcePageSpecRevisionId":"revision-1",
  "sourcePageSpecHash":"sha256:page-spec",
  "scene":{"layers":[]},
  "legacyExtension":{"preserved":true}
}`)
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	if len(content["states"].([]any)) != 0 || len(content["frames"].([]any)) != 0 ||
		len(content["layers"].(map[string]any)) != 0 {
		t.Fatalf("compatibility migration invented semantic content: %#v", content)
	}
	if legacy, ok := content["legacyExtension"].(map[string]any); !ok || legacy["preserved"] != true {
		t.Fatalf("compatibility migration discarded an unknown field: %#v", content)
	}
}

func TestPrototypeProposalCanonicalizationUsesNamedBreakpointViewportDefaults(t *testing.T) {
	t.Parallel()
	var content map[string]any
	if err := json.Unmarshal(canonicalPrototypeValidationPayload(), &content); err != nil {
		t.Fatal(err)
	}
	for _, item := range content["breakpoints"].([]any) {
		breakpoint := item.(map[string]any)
		delete(breakpoint, "viewportWidth")
		delete(breakpoint, "viewportHeight")
		delete(breakpoint, "width")
		delete(breakpoint, "height")
	}
	content["breakpoints"] = append(content["breakpoints"].([]any), map[string]any{
		"id": "breakpoint-wide", "name": "Wide", "minWidth": 1600,
	})
	payload, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	expected := map[string][2]float64{
		"desktop": {1440, 900},
		"tablet":  {768, 1024},
		"mobile":  {390, 844},
		"wide":    {1440, 900},
	}
	for _, item := range content["breakpoints"].([]any) {
		breakpoint := item.(map[string]any)
		name := strings.ToLower(firstString(breakpoint, "name"))
		viewport := expected[name]
		if breakpoint["viewportWidth"] != viewport[0] || breakpoint["viewportHeight"] != viewport[1] {
			t.Fatalf("%s breakpoint did not receive its canonical viewport: %#v", name, breakpoint)
		}
	}
}

func TestPrototypeProposalCanonicalizationFallsBackToValidSceneLayers(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{
  "layers":[],
  "scene":{"layers":[{"id":"scene-root","name":"Scene root","type":"screen","childIds":[]}]}
}`)
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	layers := content["layers"].(map[string]any)
	root, ok := layers["scene-root"].(map[string]any)
	if !ok || len(layers) != 1 || root["kind"] != "frame" {
		t.Fatalf("invalid primary layers did not fall back to canonical scene layers: %#v", layers)
	}
}

func TestPrototypeProposalCanonicalizationPreservesExplicitInvalidViewport(t *testing.T) {
	t.Parallel()
	var content map[string]any
	if err := json.Unmarshal(canonicalPrototypeValidationPayload(), &content); err != nil {
		t.Fatal(err)
	}
	breakpoint := content["breakpoints"].([]any)[0].(map[string]any)
	breakpoint["viewportWidth"] = 1
	breakpoint["viewportHeight"] = 0
	payload, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	breakpoint = content["breakpoints"].([]any)[0].(map[string]any)
	if breakpoint["viewportWidth"] != float64(1) || breakpoint["viewportHeight"] != float64(0) {
		t.Fatalf("explicit invalid viewport was silently repaired: %#v", breakpoint)
	}
	err = validateProposalPatchedContent("prototype", canonical)
	if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "prototype.breakpoint_ui_contract") {
		t.Fatalf("explicit invalid viewport did not reach the strict gate: %v", err)
	}
}

func TestPrototypeProposalCanonicalizationRejectsMalformedLayerCollections(t *testing.T) {
	t.Parallel()
	validScene := map[string]any{"layers": []any{map[string]any{"id": "scene-root", "type": "screen"}}}
	tests := map[string]map[string]any{
		"primary_null": {
			"layers": nil,
			"scene":  validScene,
		},
		"primary_non_object": {
			"layers": []any{map[string]any{"id": "valid", "type": "screen"}, 17},
			"scene":  validScene,
		},
		"primary_missing_id": {
			"layers": []any{map[string]any{"name": "missing stable id"}},
			"scene":  validScene,
		},
		"primary_wrong_type": {"layers": "not-a-collection", "scene": validScene},
		"scene_non_object": {
			"layers": []any{},
			"scene":  map[string]any{"layers": []any{map[string]any{"id": "valid"}, false}},
		},
		"scene_missing_id": {
			"layers": []any{},
			"scene":  map[string]any{"layers": []any{map[string]any{"name": "missing stable id"}}},
		},
	}
	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			_, err = canonicalizeProposalPatchedContent("prototype", payload)
			if !errors.Is(err, ErrBlockingGate) {
				t.Fatalf("malformed layer collection did not fail closed: %v", err)
			}
		})
	}
}

func TestPrototypeProposalCanonicalizationRejectsMalformedTopLevelObjectArrays(t *testing.T) {
	t.Parallel()
	fields := []string{
		"states", "breakpoints", "frames", "overrides", "interactions", "fixtures",
		"tokenBindings", "componentBindings", "assets", "traceLinks",
	}
	invalidValues := map[string]any{
		"null":          nil,
		"wrong_type":    map[string]any{},
		"mixed_entries": []any{map[string]any{}, "not-an-object"},
	}
	for _, field := range fields {
		field := field
		for name, invalid := range invalidValues {
			name, invalid := name, invalid
			t.Run(field+"_"+name, func(t *testing.T) {
				t.Parallel()
				payload, err := json.Marshal(map[string]any{field: invalid})
				if err != nil {
					t.Fatal(err)
				}
				_, err = canonicalizeProposalPatchedContent("prototype", payload)
				if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), field) {
					t.Fatalf("malformed top-level %s did not fail closed: %v", field, err)
				}
			})
		}
	}
}

func TestPrototypeProposalCanonicalizationRejectsDuplicateLayerIDs(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{
  "layers":[
    {"id":"duplicate","name":"First","type":"screen"},
    {"id":"duplicate","name":"Second","type":"paragraph"}
  ]
}`)
	_, err := canonicalizeProposalPatchedContent("prototype", payload)
	if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate stable layer IDs did not fail closed: %v", err)
	}
}

func TestPrototypeProposalCanonicalizationPreservesUnknownLayerKindForValidation(t *testing.T) {
	t.Parallel()
	var content map[string]any
	if err := json.Unmarshal(canonicalPrototypeValidationPayload(), &content); err != nil {
		t.Fatal(err)
	}
	layer := content["layers"].(map[string]any)["layer-root"].(map[string]any)
	layer["kind"] = "customWidget"
	payload, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	layer = content["layers"].(map[string]any)["layer-root"].(map[string]any)
	if layer["kind"] != "customWidget" {
		t.Fatalf("unknown layer kind was silently coerced: %#v", layer)
	}
	err = validateProposalPatchedContent("prototype", canonical)
	if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "prototype.layer_ui_contract") {
		t.Fatalf("strict Prototype validation did not reject the unknown layer kind: %v", err)
	}
}

func TestPrototypeProposalCanonicalizationStrictInteractionCollections(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{
  "interactions":[
    {"id":"missing-collections"},
    {"id":"complete","guards":[{"type":"role"}],"actions":[{"type":"setState","stateId":"ready"}]}
  ]
}`)
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	interactions := content["interactions"].([]any)
	missing := interactions[0].(map[string]any)
	complete := interactions[1].(map[string]any)
	if len(missing["guards"].([]any)) != 0 || len(missing["actions"].([]any)) != 0 ||
		len(complete["guards"].([]any)) != 1 || len(complete["actions"].([]any)) != 1 ||
		complete["guards"].([]any)[0].(map[string]any)["type"] != "role" ||
		complete["actions"].([]any)[0].(map[string]any)["type"] != "setState" {
		t.Fatalf("interaction collections were not defaulted or preserved exactly: %#v", interactions)
	}

	invalidValues := map[string]any{
		"null":        nil,
		"wrong_type":  map[string]any{},
		"mixed_items": []any{map[string]any{}, 17},
	}
	for _, field := range []string{"guards", "actions"} {
		field := field
		for name, invalid := range invalidValues {
			name, invalid := name, invalid
			t.Run(field+"_"+name, func(t *testing.T) {
				t.Parallel()
				payload, err := json.Marshal(map[string]any{
					"interactions": []any{map[string]any{"id": "invalid", field: invalid}},
				})
				if err != nil {
					t.Fatal(err)
				}
				_, err = canonicalizeProposalPatchedContent("prototype", payload)
				if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), field) {
					t.Fatalf("explicit malformed interaction %s was silently repaired: %v", field, err)
				}
			})
		}
	}
}

func TestPrototypeProposalCanonicalizationRejectsExplicitInvalidLayerLayout(t *testing.T) {
	t.Parallel()
	tests := map[string]map[string]any{
		"layout_null": {"layout": nil},
		"x_non_numeric": {
			"layout": map[string]any{"x": "zero"},
		},
		"x_negative": {"layout": map[string]any{"x": -1}},
		"y_negative": {"layout": map[string]any{"y": -1}},
		"width_zero": {"layout": map[string]any{"width": 0}},
		"height_negative": {
			"layout": map[string]any{"height": -1},
		},
	}
	for name, layerFields := range tests {
		name, layerFields := name, layerFields
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			layer := map[string]any{"id": "root", "type": "screen"}
			for field, value := range layerFields {
				layer[field] = value
			}
			payload, err := json.Marshal(map[string]any{"layers": []any{layer}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = canonicalizeProposalPatchedContent("prototype", payload)
			if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "layout") {
				t.Fatalf("explicit invalid layout was silently repaired: %v", err)
			}
		})
	}

	_, err := canonicalPrototypeLayerLayout(
		map[string]any{"layout": map[string]any{"x": math.Inf(1)}},
		"root", 0, 390, 844, true,
	)
	if !errors.Is(err, ErrBlockingGate) {
		t.Fatalf("non-finite layout value was silently repaired: %v", err)
	}
}

func TestPrototypeProposalCanonicalizationDefaultsOnlyMissingLayerLayoutFields(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{
  "layers":[{"id":"root","type":"screen","layout":{"x":1.5,"width":320}}],
  "frames":[{"id":"frame","rootLayerId":"root"}]
}`)
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	layout := content["layers"].(map[string]any)["root"].(map[string]any)["layout"].(map[string]any)
	if layout["x"] != float64(1.5) || layout["width"] != float64(320) ||
		layout["y"] != float64(0) || layout["height"] != float64(1) {
		t.Fatalf("explicit valid layout values were changed or missing fields were not defaulted: %#v", layout)
	}
}

func TestPrototypeProposalCanonicalizationRejectsExplicitInvalidNestedFields(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		content map[string]any
		path    string
	}{
		"exploratory": {
			content: map[string]any{"exploratory": "false"}, path: "exploratory",
		},
		"state_required": {
			content: map[string]any{"states": []any{map[string]any{"required": "true"}}}, path: "required",
		},
		"state_fixture_ids": {
			content: map[string]any{"states": []any{map[string]any{"fixtureIds": []any{"fixture", 17}}}}, path: "fixtureIds",
		},
		"breakpoint_fractional_min_width": {
			content: map[string]any{"breakpoints": []any{map[string]any{"minWidth": 12.5}}}, path: "minWidth",
		},
		"breakpoint_negative_min_width": {
			content: map[string]any{"breakpoints": []any{map[string]any{"minWidth": -1}}}, path: "minWidth",
		},
		"layer_child_ids": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "childIds": []any{"child", false}}}}, path: "childIds",
		},
		"layer_requirement_ids": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "requirementIds": nil}}}, path: "requirementIds",
		},
		"layer_acceptance_ids": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "acceptanceCriterionIds": []any{""}}}}, path: "acceptanceCriterionIds",
		},
		"layer_style": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "style": []any{}}}}, path: "style",
		},
		"layer_field_metadata": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "fieldMetadata": nil}}}, path: "fieldMetadata",
		},
		"layer_properties": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "properties": "invalid"}}}, path: "properties",
		},
		"component_property_mapping": {
			content: map[string]any{"componentBindings": []any{map[string]any{"propertyMapping": nil}}}, path: "propertyMapping",
		},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(test.content)
			if err != nil {
				t.Fatal(err)
			}
			_, err = canonicalizeProposalPatchedContent("prototype", payload)
			if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), test.path) {
				t.Fatalf("explicit invalid %s was silently repaired: %v", test.path, err)
			}
		})
	}
}

func TestPrototypeProposalCanonicalizationRejectsExplicitInvalidTextFields(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		content map[string]any
		path    string
	}{
		"page_spec_revision_object": {
			content: map[string]any{"pageSpecRevision": nil}, path: "pageSpecRevision",
		},
		"page_spec_artifact_id": {
			content: map[string]any{
				"pageSpecRevision":         map[string]any{"artifactId": ""},
				"sourcePageSpecArtifactId": "legacy-artifact",
			}, path: "artifactId",
		},
		"page_spec_revision_id": {
			content: map[string]any{
				"pageSpecRevision":         map[string]any{"revisionId": 17},
				"sourcePageSpecRevisionId": "legacy-revision",
			}, path: "revisionId",
		},
		"page_spec_content_hash": {
			content: map[string]any{
				"pageSpecRevision":   map[string]any{"contentHash": ""},
				"sourcePageSpecHash": "sha256:legacy",
			}, path: "contentHash",
		},
		"state_id": {
			content: map[string]any{"states": []any{map[string]any{"id": ""}}}, path: "states[0].id",
		},
		"state_key": {
			content: map[string]any{"states": []any{map[string]any{"id": "state", "key": ""}}}, path: "states[0].key",
		},
		"state_title": {
			content: map[string]any{"states": []any{map[string]any{"id": "state", "title": 17}}}, path: "states[0].title",
		},
		"state_page_state_id_empty": {
			content: map[string]any{"states": []any{map[string]any{"id": "state", "pageStateId": ""}}}, path: "states[0].pageStateId",
		},
		"state_page_state_id_type": {
			content: map[string]any{"states": []any{map[string]any{"id": "state", "pageStateId": 17}}}, path: "states[0].pageStateId",
		},
		"breakpoint_id": {
			content: map[string]any{"breakpoints": []any{map[string]any{"id": "", "key": "desktop"}}}, path: "breakpoints[0].id",
		},
		"breakpoint_name": {
			content: map[string]any{"breakpoints": []any{map[string]any{"id": "desktop", "name": "", "title": "Desktop"}}}, path: "breakpoints[0].name",
		},
		"layer_id": {
			content: map[string]any{"layers": []any{map[string]any{"id": "", "layerId": "legacy"}}}, path: ".id",
		},
		"layer_kind": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "kind": "", "type": "screen"}}}, path: "kind",
		},
		"layer_name": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "name": nil}}}, path: "name",
		},
		"layer_parent_id": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "parentId": 17}}}, path: "parentId",
		},
		"layer_data_binding_id": {
			content: map[string]any{"layers": []any{map[string]any{"id": "root", "dataBindingId": ""}}}, path: "dataBindingId",
		},
		"layer_semantic_role": {
			content: map[string]any{"layers": []any{map[string]any{
				"id": "root", "semanticRole": nil, "props": map[string]any{"role": "main"},
			}}}, path: "semanticRole",
		},
		"frame_id": {
			content: map[string]any{"frames": []any{map[string]any{"id": ""}}}, path: "frames[0].id",
		},
		"frame_state_id": {
			content: map[string]any{"frames": []any{map[string]any{"stateId": nil}}}, path: "stateId",
		},
		"frame_breakpoint_id": {
			content: map[string]any{"frames": []any{map[string]any{"breakpointId": ""}}}, path: "breakpointId",
		},
		"frame_root_layer_id": {
			content: map[string]any{"frames": []any{map[string]any{"rootLayerId": 17}}}, path: "rootLayerId",
		},
		"frame_title": {
			content: map[string]any{"frames": []any{map[string]any{"title": ""}}}, path: "frames[0].title",
		},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(test.content)
			if err != nil {
				t.Fatal(err)
			}
			_, err = canonicalizeProposalPatchedContent("prototype", payload)
			if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), test.path) {
				t.Fatalf("explicit invalid text field %s was silently replaced: %v", test.path, err)
			}
		})
	}
}

func TestPrototypeProposalCanonicalizationRejectsMismatchedLayerRecordIdentity(t *testing.T) {
	t.Parallel()
	tests := map[string]map[string]any{
		"canonical_id": {
			"layers": map[string]any{"record-root": map[string]any{"id": "other-root"}},
		},
		"legacy_layer_id": {
			"layers": map[string]any{"record-root": map[string]any{"layerId": "other-root"}},
		},
		"ignored_legacy_id": {
			"layers": map[string]any{"record-root": map[string]any{
				"id": "record-root", "layerId": "other-root",
			}},
		},
		"scene_record": {
			"scene": map[string]any{"layers": map[string]any{
				"record-root": map[string]any{"id": "other-root"},
			}},
		},
	}
	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			_, err = canonicalizeProposalPatchedContent("prototype", payload)
			if !errors.Is(err, ErrBlockingGate) || !strings.Contains(err.Error(), "record key") {
				t.Fatalf("mismatched layer record identity was silently rekeyed: %v", err)
			}
		})
	}
}

func TestPrototypeProposalCanonicalizationUsesTextFallbacksOnlyWhenMissing(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{
  "sourcePageSpecArtifactId":"page-spec",
  "sourcePageSpecRevisionId":"revision",
  "sourcePageSpecHash":"sha256:page-spec",
  "states":[{"id":"state-ready","pageStateId":null}],
  "breakpoints":[{"key":"desktop","title":"Desktop"}],
  "layers":[{"layerId":"legacy-root","type":"screen","parentId":null,"dataBindingId":null,"props":{"role":"main"}}],
  "frames":[{"id":"frame","stateId":"state-ready","breakpointId":"desktop","rootLayerId":"legacy-root"}]
}`)
	canonical, err := canonicalizeProposalPatchedContent("prototype", payload)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	revision := content["pageSpecRevision"].(map[string]any)
	state := content["states"].([]any)[0].(map[string]any)
	breakpoint := content["breakpoints"].([]any)[0].(map[string]any)
	layer := content["layers"].(map[string]any)["legacy-root"].(map[string]any)
	frame := content["frames"].([]any)[0].(map[string]any)
	if revision["artifactId"] != "page-spec" || revision["revisionId"] != "revision" ||
		revision["contentHash"] != "sha256:page-spec" || state["key"] != "state-ready" ||
		state["title"] != "state-ready" || breakpoint["id"] != "desktop" ||
		breakpoint["name"] != "Desktop" || layer["id"] != "legacy-root" ||
		layer["kind"] != "frame" || layer["name"] != "Layer 1" ||
		layer["semanticRole"] != "main" || frame["title"] != "state-ready · Desktop" {
		t.Fatalf("missing canonical text fields did not use stable legacy fallbacks: %#v", content)
	}
	if _, exists := layer["parentId"]; exists {
		t.Fatalf("optional null parentId was not normalized to absence: %#v", layer)
	}
	if _, exists := layer["dataBindingId"]; exists {
		t.Fatalf("optional null dataBindingId was not normalized to absence: %#v", layer)
	}
	if _, exists := state["pageStateId"]; exists {
		t.Fatalf("optional null pageStateId was not normalized to absence: %#v", state)
	}

	canonicalPropertiesPayload := json.RawMessage(`{
  "layers":[{"id":"root","kind":"group","properties":{"text":"canonical"},"props":"ignored legacy"}]
}`)
	canonical, err = canonicalizeProposalPatchedContent("prototype", canonicalPropertiesPayload)
	if err != nil {
		t.Fatalf("canonical properties should take precedence over a legacy alias: %v", err)
	}
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	properties := content["layers"].(map[string]any)["root"].(map[string]any)["properties"].(map[string]any)
	if properties["text"] != "canonical" {
		t.Fatalf("canonical properties were replaced by the legacy alias: %#v", properties)
	}

	buttonPayload := json.RawMessage(`{
  "layers":[
    {"id":"missing-text","kind":"button","properties":{"label":"Legacy label"}},
    {"id":"explicit-text","kind":"button","properties":{"text":"","label":"Must not replace"}}
  ]
}`)
	canonical, err = canonicalizeProposalPatchedContent("prototype", buttonPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(canonical, &content); err != nil {
		t.Fatal(err)
	}
	layers := content["layers"].(map[string]any)
	missingText := layers["missing-text"].(map[string]any)["properties"].(map[string]any)
	explicitText := layers["explicit-text"].(map[string]any)["properties"].(map[string]any)
	if missingText["text"] != "Legacy label" || explicitText["text"] != "" {
		t.Fatalf("button label alias ignored canonical text presence: %#v %#v", missingText, explicitText)
	}
}
