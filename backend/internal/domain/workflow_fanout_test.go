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
}
