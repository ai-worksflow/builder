package workflow

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestCurrentAuthoringRejectsNestedSchemaMismatchButLegacyReplayRemainsLoadable(t *testing.T) {
	t.Parallel()
	base := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	nodes := append([]domain.NodeDefinition(nil), base.Nodes...)
	for index := range nodes {
		switch nodes[index].ID {
		case "source":
			nodes[index].OutputSchema = json.RawMessage(`{
                "type":"object",
                "properties":{"payload":{"type":"object","properties":{"nested":{"type":"object"}}}},
                "required":["payload"]
            }`)
		case "project-brief-ai":
			nodes[index].InputSchema = json.RawMessage(`{
                "type":"object",
                "properties":{"payload":{"type":"object","properties":{"nested":{"properties":{"requiredValue":{"type":"string"}},"required":["requiredValue"]}}}},
                "required":["payload"]
            }`)
		}
	}
	current, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		base.ID, base.Version+1, base.Name, base.SchemaVersion, nodes, base.Edges,
		*base.InputContract, *base.OutputContract, CurrentWorkflowExecutionProfileRef(),
		base.CreatedBy, base.CreatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("historical shallow domain validator unexpectedly changed: %v", err)
	}
	if err := ValidateDefinitionForExecutionProfile(current, CurrentWorkflowExecutionProfileRef()); err == nil || !strings.Contains(err.Error(), "requiredValue") {
		t.Fatalf("current authoring accepted an unprovable nested edge: %v", err)
	}

	legacy, err := domain.NewWorkflowDefinitionWithContracts(
		base.ID, base.Version+1, base.Name, base.SchemaVersion, nodes, base.Edges,
		*base.InputContract, *base.OutputContract, base.CreatedBy, base.CreatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("legacy definition could no longer be decoded: %v", err)
	}
	if err := validateDefinitionV0Replay(legacy); err != nil {
		t.Fatalf("pre-pin replay was tightened by current authoring policy: %v", err)
	}
}

func TestMappedSchemaImplicationAccountsForPreservedSourceProperties(t *testing.T) {
	t.Parallel()
	source := json.RawMessage(`{"type":"object","properties":{"payload":{"type":"string"}},"required":["payload"],"additionalProperties":false}`)
	strictTarget := json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`)
	openTarget := json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":true}`)
	definition := portSchemaTestDefinition(t, source, strictTarget, map[string]string{"title": "payload"})
	if err := definition.Validate(); err != nil {
		t.Fatalf("domain fixture is not legacy-compatible: %v", err)
	}
	if err := validateCurrentDefinitionPortSchemas(definition); err == nil || !strings.Contains(err.Error(), "preserved source property") {
		t.Fatalf("strict target silently discarded a preserved mapped source property: %v", err)
	}
	definition = portSchemaTestDefinition(t, source, openTarget, map[string]string{"title": "payload"})
	if err := validateCurrentDefinitionPortSchemas(definition); err != nil {
		t.Fatalf("safe additive alias mapping was rejected: %v", err)
	}
}

func TestMappedSchemaImplicationRequiresEveryMappedSource(t *testing.T) {
	t.Parallel()
	source := json.RawMessage(`{"type":"object","properties":{"payload":{"type":"string"}},"additionalProperties":false}`)
	target := json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`)
	definition := portSchemaTestDefinition(t, source, target, map[string]string{"title": "payload"})
	if err := validateCurrentDefinitionPortSchemas(definition); err == nil || !strings.Contains(err.Error(), "may be absent") {
		t.Fatalf("optional mapping source was accepted even though runtime mapping requires it: %v", err)
	}
}

func TestCurrentSchemaAuthoringCompilesEveryPortAndAcceptsExactComplexContracts(t *testing.T) {
	t.Parallel()
	invalid := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","pattern":"["}}}`)
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, invalid, invalid, nil)); err == nil {
		t.Fatal("invalid but textually identical JSON Schema bypassed authoring compilation")
	}
	complex := json.RawMessage(`{
        "type":"object",
        "properties":{"payload":{"type":"object","oneOf":[
            {"required":["text"],"properties":{"text":{"type":"string"}}},
            {"required":["count"],"properties":{"count":{"type":"integer"}}}
        ]}},
        "required":["payload"],
        "additionalProperties":false
    }`)
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, complex, complex, nil)); err != nil {
		t.Fatalf("an exact compiled complex port contract was rejected: %v", err)
	}
}

func TestCurrentSchemaImplicationProvesAssertedContentSchemaRecursively(t *testing.T) {
	t.Parallel()
	const draft7 = `"$schema":"http://json-schema.org/draft-07/schema#",`
	target := json.RawMessage(`{` + draft7 + `
		"type":"object",
		"properties":{"value":{
			"type":"string",
			"contentMediaType":"application/json",
			"contentSchema":{
				"type":"object",
				"properties":{"x":{"type":"string"}},
				"required":["x"]
			}
		}},
		"required":["value"]
	}`)
	missingSourceSchema := json.RawMessage(`{` + draft7 + `
		"type":"object",
		"properties":{"value":{
			"type":"string",
			"contentMediaType":"application/json"
		}},
		"required":["value"]
	}`)
	witness := json.RawMessage(`{"value":"{}"}`)
	if err := validateAgainstSchema("source", missingSourceSchema, witness); err != nil {
		t.Fatalf("contentSchema counterexample must satisfy source: %v", err)
	}
	if err := validateAgainstSchema("target", target, witness); err == nil {
		t.Fatal("decoded JSON without x unexpectedly satisfied target contentSchema")
	}
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, missingSourceSchema, target, nil)); err == nil || !strings.Contains(err.Error(), "contentSchema") {
		t.Fatalf("source without contentSchema fed a target that validates decoded JSON: %v", err)
	}

	provenSource := json.RawMessage(`{` + draft7 + `
		"type":"object",
		"properties":{"value":{
			"type":"string",
			"minLength":2,
			"contentMediaType":"application/json",
			"contentSchema":{
				"type":"object",
				"properties":{"x":{"type":"string"}},
				"required":["x"]
			}
		}},
		"required":["value"]
	}`)
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, provenSource, target, nil)); err != nil {
		t.Fatalf("recursively compatible contentSchema was rejected: %v", err)
	}
}

func TestCurrentSchemaImplicationRejectsAnnotationOnlyFormatAndContentSources(t *testing.T) {
	t.Parallel()
	annotationFormat := json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"value":{"type":"string","format":"email"}},
		"required":["value"]
	}`)
	assertedFormat := json.RawMessage(`{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{"value":{"type":"string","format":"email"}},
		"required":["value"]
	}`)
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, annotationFormat, assertedFormat, nil)); err == nil || !strings.Contains(err.Error(), "asserted source format") {
		t.Fatalf("annotation-only source format was treated as a runtime guarantee: %v", err)
	}

	annotationContent := json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"value":{"type":"string","contentMediaType":"application/json"}},
		"required":["value"]
	}`)
	assertedContent := json.RawMessage(`{
		"$schema":"http://json-schema.org/draft-07/schema#",
		"type":"object",
		"properties":{"value":{"type":"string","contentMediaType":"application/json"}},
		"required":["value"]
	}`)
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, annotationContent, assertedContent, nil)); err == nil || !strings.Contains(err.Error(), "annotation-only source") {
		t.Fatalf("annotation-only source content was treated as a runtime guarantee: %v", err)
	}
}

func TestCurrentSchemaImplicationRejectsLegacyTupleTarget(t *testing.T) {
	t.Parallel()
	const draft7 = `"$schema":"http://json-schema.org/draft-07/schema#",`
	source := json.RawMessage(`{` + draft7 + `"type":"object","properties":{"value":{"type":"array","items":{}}},"required":["value"]}`)
	target := json.RawMessage(`{` + draft7 + `"type":"object","properties":{"value":{"type":"array","items":[{"type":"string"}]}},"required":["value"]}`)
	witness := json.RawMessage(`{"value":[1]}`)
	if err := validateAgainstSchema("source", source, witness); err != nil {
		t.Fatalf("legacy tuple counterexample must satisfy source: %v", err)
	}
	if err := validateAgainstSchema("target", target, witness); err == nil {
		t.Fatal("legacy tuple counterexample unexpectedly satisfied target")
	}
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, source, target, nil)); err == nil || !strings.Contains(err.Error(), "legacy tuple items") {
		t.Fatalf("legacy tuple target bypassed item implication: %v", err)
	}
}

func TestCurrentSchemaImplicationRejectsIgnoredSourcePrefixItems(t *testing.T) {
	t.Parallel()
	source := json.RawMessage(`{
		"type":"object",
		"properties":{"value":{
			"type":"array",
			"prefixItems":[{"type":"integer"}],
			"items":{"type":"string"}
		}},
		"required":["value"]
	}`)
	target := json.RawMessage(`{
		"type":"object",
		"properties":{"value":{"type":"array","items":{"type":"string"}}},
		"required":["value"]
	}`)
	witness := json.RawMessage(`{"value":[1]}`)
	if err := validateAgainstSchema("source", source, witness); err != nil {
		t.Fatalf("prefixItems witness must satisfy source: %v", err)
	}
	if err := validateAgainstSchema("target", target, witness); err == nil {
		t.Fatal("prefixItems witness unexpectedly satisfied homogeneous string target")
	}
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, source, target, nil)); err == nil || !strings.Contains(err.Error(), "source prefixItems") {
		t.Fatalf("source prefixItems were ignored during homogeneous item implication: %v", err)
	}
}

func TestCurrentSchemaImplicationRejectsIgnoredSourcePatternProperties(t *testing.T) {
	t.Parallel()
	source := json.RawMessage(`{
		"type":"object",
		"patternProperties":{"^x$":{"type":"integer"}},
		"additionalProperties":false
	}`)
	target := json.RawMessage(`{
		"type":"object",
		"properties":{"x":{"type":"string"}}
	}`)
	witness := json.RawMessage(`{"x":1}`)
	if err := validateAgainstSchema("source", source, witness); err != nil {
		t.Fatalf("patternProperties witness must satisfy source: %v", err)
	}
	if err := validateAgainstSchema("target", target, witness); err == nil {
		t.Fatal("patternProperties witness unexpectedly satisfied string target property")
	}
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, source, target, nil)); err == nil || !strings.Contains(err.Error(), "source patternProperties") {
		t.Fatalf("source patternProperties were replaced by additionalProperties semantics: %v", err)
	}

	openTarget := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	if err := validateCurrentDefinitionPortSchemas(portSchemaTestDefinition(t, source, openTarget, nil)); err != nil {
		t.Fatalf("source patternProperties should feed a completely open target: %v", err)
	}
}

func TestCurrentSchemaValidatorIsConcurrentAndFailClosed(t *testing.T) {
	base := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	nodes := append([]domain.NodeDefinition(nil), base.Nodes...)
	for index := range nodes {
		if nodes[index].ID == "project-brief-ai" {
			nodes[index].InputSchema = json.RawMessage(`{"type":"object","properties":{"optional":{"type":"string"}}}`)
		}
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		base.ID, base.Version+1, base.Name, base.SchemaVersion, nodes, base.Edges,
		*base.InputContract, *base.OutputContract, CurrentWorkflowExecutionProfileRef(),
		base.CreatedBy, base.CreatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- validateCurrentDefinitionPortSchemas(definition)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err == nil || !strings.Contains(err.Error(), "source type is unconstrained") {
			t.Fatalf("concurrent validator did not fail closed: %v", err)
		}
	}
}

func portSchemaTestDefinition(t *testing.T, source, target json.RawMessage, mapping map[string]string) domain.WorkflowDefinition {
	t.Helper()
	nodes := []domain.NodeDefinition{
		{
			ID: "source", Name: "Source", Type: domain.NodeArtifactInput,
			InputSchema: source, OutputSchema: source,
			ArtifactInput: &domain.ArtifactInputNodeConfig{
				AllowedTypes: []domain.ArtifactType{domain.ArtifactDocument}, MinimumArtifacts: 1,
			},
		},
		{
			ID: "target", Name: "Target", Type: domain.NodeTransform,
			InputSchema: target, OutputSchema: target,
			Transform: &domain.TransformNodeConfig{Transform: "selection_passthrough"},
		},
	}
	definition, err := domain.NewWorkflowDefinition(
		uuid.NewString(), 1, "Port schema fixture", "2", nodes,
		[]domain.WorkflowEdge{{ID: "mapped", From: "source", To: "target", Mapping: mapping}},
		uuid.NewString(), time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("build port schema fixture: %v", err)
	}
	return definition
}
