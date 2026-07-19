package workflow

import (
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/domain"
)

func TestWorkflowExecutionProfileV2CancelsUnselectedFanOutMergeAndReleasesSharedTail(t *testing.T) {
	definition := conditionFanOutMergeFixtureDefinition()
	run := conditionFanOutMergeFixtureRun(definition)
	condition := run.Nodes["condition"]
	if err := applyConditionRoute(run, definition, condition, "selected"); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	builder := newMutationBuilder(engine, run, now)
	workflowExecutionProfileV2Bundle().reconcile(engine, run, definition, builder)

	if run.Nodes["fan"].Status != NodeCancelled {
		t.Fatalf("unselected fan-out status = %s, want cancelled", run.Nodes["fan"].Status)
	}
	if run.Nodes["merge"].Status != NodeCancelled {
		t.Fatalf("zero-slice merge on the unselected fan-out route = %s, want cancelled", run.Nodes["merge"].Status)
	}
	if run.Nodes["shared"].Status != NodeReady {
		t.Fatalf("shared tail remained blocked after its alternative merge was cancelled: %s", run.Nodes["shared"].Status)
	}

	// Once the shared work completes, the ordinary tail can continue. This
	// proves cancellation is propagated as an unavailable alternative rather
	// than turning the whole run into a failed or permanently pending join.
	run.Nodes["shared"].Status = NodeCompleted
	run.Nodes["shared"].CompletedAt = timePointer(now)
	workflowExecutionProfileV2Bundle().reconcile(engine, run, definition, newMutationBuilder(engine, run, now.Add(time.Second)))
	if run.Nodes["terminal"].Status != NodeReady {
		t.Fatalf("terminal did not become runnable through the shared tail: %s", run.Nodes["terminal"].Status)
	}
}

func TestWorkflowExecutionProfileV2DoesNotCancelMergeWithValidUnfinishedFanOutPredecessor(t *testing.T) {
	definition := conditionFanOutMergeFixtureDefinition()
	run := conditionFanOutMergeFixtureRun(definition)
	// The route to fan is still selected and its predecessor is valid but has
	// not completed. A status-only check would incorrectly cancel this merge;
	// v2 requires the FanOut to have no effective predecessor at all.
	run.Nodes["condition"].Status = NodeRunning
	run.Nodes["selected"].Status = NodePending

	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	workflowExecutionProfileV2Bundle().reconcile(engine, run, definition, newMutationBuilder(engine, run, time.Now().UTC()))

	if run.Nodes["fan"].Status != NodePending {
		t.Fatalf("fan-out with a valid unfinished predecessor = %s, want pending", run.Nodes["fan"].Status)
	}
	if run.Nodes["merge"].Status != NodePending {
		t.Fatalf("merge with a reachable unfinished fan-out = %s, want pending", run.Nodes["merge"].Status)
	}
}

func TestWorkflowExecutionProfileV1RetainsFrozenZeroSliceMergeReconcile(t *testing.T) {
	definition := conditionFanOutMergeFixtureDefinition()
	run := conditionFanOutMergeFixtureRun(definition)
	if err := applyConditionRoute(run, definition, run.Nodes["condition"], "selected"); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(NewMemoryStore(nil))
	if err != nil {
		t.Fatal(err)
	}
	workflowExecutionProfileV1Bundle().reconcile(engine, run, definition, newMutationBuilder(engine, run, time.Now().UTC()))

	if run.Nodes["fan"].Status != NodeCancelled {
		t.Fatalf("frozen v1 did not cancel the directly disabled fan-out: %s", run.Nodes["fan"].Status)
	}
	if run.Nodes["merge"].Status != NodePending || run.Nodes["shared"].Status != NodePending {
		t.Fatalf("v1 reconcile was redirected to v2 semantics: merge=%s shared=%s", run.Nodes["merge"].Status, run.Nodes["shared"].Status)
	}
}

func conditionFanOutMergeFixtureDefinition() domain.WorkflowDefinition {
	nodes := []domain.NodeDefinition{
		{ID: "condition", Type: domain.NodeCondition, Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{{Name: "selected"}, {Name: "fanout"}}}},
		{ID: "selected", Type: domain.NodeTransform, Transform: &domain.TransformNodeConfig{Transform: "selected"}},
		{ID: "fan", Type: domain.NodeFanOut, FanOut: &domain.FanOutNodeConfig{MergeNodeID: "merge", MaxParallel: 1}},
		{ID: "fan-work", Type: domain.NodeTransform, Transform: &domain.TransformNodeConfig{Transform: "fan-work"}},
		{ID: "merge", Type: domain.NodeMerge, Merge: &domain.MergeNodeConfig{FanOutNodeID: "fan", Policy: domain.MergeAll}},
		{ID: "shared", Type: domain.NodeTransform, Transform: &domain.TransformNodeConfig{Transform: "shared"}},
		{ID: "terminal", Type: domain.NodeTransform, Transform: &domain.TransformNodeConfig{Transform: "terminal"}},
	}
	edges := []domain.WorkflowEdge{
		{ID: "condition-selected", From: "condition", FromPort: "selected", To: "selected"},
		{ID: "condition-fanout", From: "condition", FromPort: "fanout", To: "fan"},
		{ID: "fan-work", From: "fan", To: "fan-work"},
		{ID: "fan-merge", From: "fan-work", To: "merge"},
		{ID: "selected-shared", From: "selected", To: "shared"},
		{ID: "merge-shared", From: "merge", To: "shared"},
		{ID: "shared-terminal", From: "shared", To: "terminal"},
	}
	return domain.WorkflowDefinition{Nodes: nodes, Edges: edges}
}

func conditionFanOutMergeFixtureRun(definition domain.WorkflowDefinition) *RunRecord {
	run := &RunRecord{ID: "run-v2-reconcile", Status: RunRunning, Context: NewRunContext(), Nodes: map[string]*NodeRecord{}}
	statuses := map[string]NodeStatus{
		"condition": NodeCompleted,
		"selected":  NodeCompleted,
		"fan":       NodePending,
		"merge":     NodePending,
		"shared":    NodePending,
		"terminal":  NodePending,
	}
	for nodeID, status := range statuses {
		definitionNode, _ := definition.FindNode(nodeID)
		run.Nodes[nodeID] = &NodeRecord{ID: nodeID, RunID: run.ID, Key: nodeID, DefinitionNodeID: nodeID, Type: definitionNode.Type, Status: status}
		run.Context.Nodes[nodeID] = NodeMetadata{DefinitionNodeID: nodeID}
	}
	return run
}
