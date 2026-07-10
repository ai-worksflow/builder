package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func v2Schema(properties string, required ...string) json.RawMessage {
	requiredJSON, _ := json.Marshal(required)
	return json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":` + string(requiredJSON) + `}`)
}

func TestDefinitionV2ValidatesPortsAndCompleteConditionBranches(t *testing.T) {
	envelope := v2Schema(`"value":{"type":"string"}`, "value")
	nodes := []NodeDefinition{
		{ID: "input", Name: "Input", Type: NodeArtifactInput, InputSchema: envelope, OutputSchema: envelope, ArtifactInput: &ArtifactInputNodeConfig{AllowedTypes: []ArtifactType{ArtifactDocument}, MinimumArtifacts: 1}},
		{ID: "condition", Name: "Condition", Type: NodeCondition, InputSchema: envelope, OutputPorts: map[string]PortDefinition{"yes": {Schema: envelope}, "no": {Schema: envelope}}, Condition: &ConditionNodeConfig{Branches: []ConditionBranch{{Name: "yes", Expression: "$.value == 'yes'"}, {Name: "no", Default: true}}}},
		{ID: "yes", Name: "Yes", Type: NodeTransform, InputSchema: envelope, OutputSchema: envelope, Transform: &TransformNodeConfig{Transform: "identity"}},
		{ID: "no", Name: "No", Type: NodeTransform, InputSchema: envelope, OutputSchema: envelope, Transform: &TransformNodeConfig{Transform: "identity"}},
		{ID: "publish", Name: "Publish", Type: NodePublish, InputSchema: envelope, OutputSchema: envelope, Publish: &PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}},
	}
	edges := []WorkflowEdge{{ID: "e1", From: "input", To: "condition"}, {ID: "e2", From: "condition", FromPort: "yes", To: "yes"}, {ID: "e3", From: "condition", FromPort: "no", To: "no"}, {ID: "e4", From: "yes", To: "publish"}, {ID: "e5", From: "no", To: "publish"}}
	if _, err := NewWorkflowDefinition("workflow", 1, "Conditional", "2", nodes, edges, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	edges = edges[:len(edges)-1]
	if _, err := NewWorkflowDefinition("workflow", 1, "Conditional", "2", nodes, edges, "owner", time.Now()); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected incomplete condition branch/terminal validation, got %v", err)
	}
}

func TestDefinitionV2ValidatesFanOutMergePairAndTermination(t *testing.T) {
	envelope := v2Schema(`"value":{"type":"object"}`, "value")
	nodes := []NodeDefinition{
		{ID: "input", Name: "Input", Type: NodeArtifactInput, InputSchema: envelope, OutputSchema: envelope, ArtifactInput: &ArtifactInputNodeConfig{AllowedTypes: []ArtifactType{ArtifactBlueprint}, MinimumArtifacts: 1}},
		{ID: "fan", Name: "Fan", Type: NodeFanOut, InputSchema: envelope, OutputSchema: envelope, FanOut: &FanOutNodeConfig{ItemsPath: "/value/pages", SliceKeyPath: "/id", MergeNodeID: "merge", MaxParallel: 2}},
		{ID: "edit", Name: "Edit", Type: NodeHumanEdit, InputSchema: envelope, OutputSchema: envelope, HumanEdit: &HumanEditNodeConfig{ArtifactType: ArtifactPrototype, RequiredRole: "editor"}},
		{ID: "merge", Name: "Merge", Type: NodeMerge, InputSchema: envelope, OutputSchema: envelope, Merge: &MergeNodeConfig{FanOutNodeID: "fan", Policy: MergeAll}},
		{ID: "publish", Name: "Publish", Type: NodePublish, InputSchema: envelope, OutputSchema: envelope, Publish: &PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}},
	}
	edges := []WorkflowEdge{{ID: "e1", From: "input", To: "fan"}, {ID: "e2", From: "fan", To: "edit"}, {ID: "e3", From: "edit", To: "merge"}, {ID: "e4", From: "merge", To: "publish"}}
	definition, err := NewWorkflowDefinition("workflow", 1, "Fanout", "2", nodes, edges, "owner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	region, err := definition.FanOutRegion("fan")
	if err != nil || len(region) != 1 || region[0] != "edit" {
		t.Fatalf("unexpected fanout region: %v / %v", region, err)
	}
	nodes[3].Merge.FanOutNodeID = "other"
	if _, err := NewWorkflowDefinition("workflow", 1, "Fanout", "2", nodes, edges, "owner", time.Now()); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected reciprocal pair validation, got %v", err)
	}
}

func TestDefinitionV2RejectsMultipleEntriesAndUnreachableNodes(t *testing.T) {
	schema := v2Schema("")
	nodes := []NodeDefinition{
		{ID: "a", Name: "A", Type: NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &TransformNodeConfig{Transform: "a"}},
		{ID: "b", Name: "B", Type: NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &TransformNodeConfig{Transform: "b"}},
		{ID: "end", Name: "End", Type: NodePublish, InputSchema: schema, OutputSchema: schema, Publish: &PublishNodeConfig{Environment: "preview", RequiredRole: "owner"}},
	}
	if _, err := NewWorkflowDefinition("workflow", 1, "Broken", "2", nodes, []WorkflowEdge{{ID: "e1", From: "a", To: "end"}, {ID: "e2", From: "b", To: "end"}}, "owner", time.Now()); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected unique entry validation, got %v", err)
	}
}
