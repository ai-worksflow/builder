package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFanOutItemKindIsExplicitAndClosed(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":true}`)
	nodes := []NodeDefinition{
		{ID: "source", Name: "Source", Type: NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &TransformNodeConfig{Transform: "source"}},
		{ID: "fan", Name: "Fan", Type: NodeFanOut, InputSchema: schema, OutputSchema: schema, FanOut: &FanOutNodeConfig{ItemsPath: "/items", SliceKeyPath: "/id", MergeNodeID: "merge", MaxParallel: 2, ItemKind: "arbitrary_hook"}},
		{ID: "work", Name: "Work", Type: NodeTransform, InputSchema: schema, OutputSchema: schema, Transform: &TransformNodeConfig{Transform: "work"}},
		{ID: "merge", Name: "Merge", Type: NodeMerge, InputSchema: schema, OutputSchema: schema, Merge: &MergeNodeConfig{FanOutNodeID: "fan", Policy: MergeAll}},
	}
	_, err := NewWorkflowDefinition(
		uuid.NewString(), 1, "Invalid fan-out", "2", nodes,
		[]WorkflowEdge{{ID: "a", From: "source", To: "fan"}, {ID: "b", From: "fan", To: "work"}, {ID: "c", From: "work", To: "merge"}},
		uuid.NewString(), time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("unknown fan-out itemKind was accepted")
	}
	nodes[1].FanOut.ItemKind = "blueprint_page"
	if _, err := NewWorkflowDefinition(
		uuid.NewString(), 1, "Blueprint page fan-out", "2", nodes,
		[]WorkflowEdge{{ID: "a", From: "source", To: "fan"}, {ID: "b", From: "fan", To: "work"}, {ID: "c", From: "work", To: "merge"}},
		uuid.NewString(), time.Now().UTC(),
	); err != nil {
		t.Fatalf("blueprint_page fan-out contract was rejected: %v", err)
	}
	nodes[1].FanOut.MaxItems = MaximumWorkflowFanOutItems
	if _, err := NewWorkflowDefinition(
		uuid.NewString(), 1, "Bounded Blueprint page fan-out", "2", nodes,
		[]WorkflowEdge{{ID: "a", From: "source", To: "fan"}, {ID: "b", From: "fan", To: "work"}, {ID: "c", From: "work", To: "merge"}},
		uuid.NewString(), time.Now().UTC(),
	); err != nil {
		t.Fatalf("legal maxItems was rejected: %v", err)
	}
	nodes[1].FanOut.MaxItems = MaximumWorkflowFanOutItems + 1
	if _, err := NewWorkflowDefinition(
		uuid.NewString(), 1, "Oversized fan-out", "2", nodes,
		[]WorkflowEdge{{ID: "a", From: "source", To: "fan"}, {ID: "b", From: "fan", To: "work"}, {ID: "c", From: "work", To: "merge"}},
		uuid.NewString(), time.Now().UTC(),
	); err == nil {
		t.Fatal("fan-out maxItems above the platform hard limit was accepted")
	}
}
