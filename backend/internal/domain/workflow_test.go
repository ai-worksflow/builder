package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func objectSchemaJSON(properties string, required ...string) json.RawMessage {
	requiredJSON, _ := json.Marshal(required)
	return json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":` + string(requiredJSON) + `}`)
}

func validWorkflowNodes() []NodeDefinition {
	return []NodeDefinition{
		{
			ID: "requirements", Name: "Requirements", Type: NodeHumanTask,
			InputSchema: objectSchemaJSON(""), OutputSchema: objectSchemaJSON(`"requirements":{"type":"string"}`, "requirements"),
			HumanTask: &HumanTaskNodeConfig{TaskType: "requirements", RequiredRole: "editor"},
		},
		{
			ID: "blueprint", Name: "Generate blueprint", Type: NodeAI,
			InputSchema:  objectSchemaJSON(`"requirements":{"type":"string"}`, "requirements"),
			OutputSchema: objectSchemaJSON(`"blueprint":{"type":"object"}`, "blueprint"),
			AI:           &AINodeConfig{JobType: "decompose_pages", ModelPolicy: "default", OutputSchemaVersion: "v1"},
		},
		{
			ID: "approve", Name: "Approve blueprint", Type: NodeApproval,
			InputSchema: objectSchemaJSON(`"blueprint":{"type":"object"}`, "blueprint"), OutputSchema: objectSchemaJSON(""),
			Approval: &ApprovalNodeConfig{RequiredRole: "owner", MinimumApprovals: 1, ProhibitSelfReview: true},
		},
	}
}

func validWorkflow(t *testing.T) WorkflowDefinition {
	t.Helper()
	definition, err := NewWorkflowDefinition(
		"product-delivery", 1, "Product delivery", "v1", validWorkflowNodes(),
		[]WorkflowEdge{{ID: "e1", From: "requirements", To: "blueprint"}, {ID: "e2", From: "blueprint", To: "approve"}},
		"owner", testNow,
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestWorkflowRejectsTypeMismatchCycleAndSchemaMismatch(t *testing.T) {
	nodes := validWorkflowNodes()
	nodes[0].AI = &AINodeConfig{JobType: "bad", OutputSchemaVersion: "v1"}
	if _, err := NewWorkflowDefinition("wf", 1, "Workflow", "v1", nodes, nil, "owner", testNow); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected discriminated union validation, got %v", err)
	}

	nodes = validWorkflowNodes()
	edges := []WorkflowEdge{{ID: "e1", From: "requirements", To: "blueprint"}, {ID: "e2", From: "blueprint", To: "requirements"}}
	if _, err := NewWorkflowDefinition("wf", 1, "Workflow", "v1", nodes, edges, "owner", testNow); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected DAG validation, got %v", err)
	}

	nodes = validWorkflowNodes()
	nodes[1].InputSchema = objectSchemaJSON(`"missing":{"type":"string"}`, "missing")
	if _, err := NewWorkflowDefinition("wf", 1, "Workflow", "v1", nodes, []WorkflowEdge{{ID: "e1", From: "requirements", To: "blueprint"}}, "owner", testNow); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected schema compatibility failure, got %v", err)
	}
}

func TestWorkflowDefinitionHashDetectsMutation(t *testing.T) {
	definition := validWorkflow(t)
	definition.Nodes[0].Name = "Mutated"
	if err := definition.Validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected immutable definition hash conflict, got %v", err)
	}
}

func TestWorkflowRunPinsDefinitionAndManifestAndUnlocksDAG(t *testing.T) {
	definition := validWorkflow(t)
	manifestHash, _ := CanonicalHash(map[string]any{"manifest": 1})
	manifest := ManifestRef{ID: "manifest-1", Hash: manifestHash}
	run, err := NewWorkflowRun("run-1", "project-1", "owner", definition, manifest, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if run.Nodes["requirements"].Status != NodeRunReady || run.Nodes["blueprint"].Status != NodeRunPending {
		t.Fatalf("unexpected initial DAG state: %+v", run.Nodes)
	}
	if err := run.Start(1, testNow); err != nil {
		t.Fatal(err)
	}
	if err := run.StartNode(definition, "requirements", ManifestRef{}, 2, testNow); !errors.Is(err, ErrManifestUnpinned) {
		t.Fatalf("expected manifest pin guard, got %v", err)
	}
	if err := run.StartNode(definition, "requirements", manifest, 2, testNow); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteNode(definition, "requirements", nil, 3, testNow); err != nil {
		t.Fatal(err)
	}
	if run.Nodes["blueprint"].Status != NodeRunReady {
		t.Fatalf("successor was not unlocked: %+v", run.Nodes["blueprint"])
	}
	if err := run.StartNode(definition, "blueprint", manifest, 4, testNow); err != nil {
		t.Fatal(err)
	}
	if err := run.WaitForReview(definition, "blueprint", 5); err != nil {
		t.Fatal(err)
	}
	if run.Status != WorkflowRunWaiting {
		t.Fatalf("expected waiting run, got %s", run.Status)
	}
	if err := run.ResumeNode(definition, "blueprint", 6); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteNode(definition, "blueprint", nil, 7, testNow); err != nil {
		t.Fatal(err)
	}
	if err := run.StartNode(definition, "approve", manifest, 8, testNow); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteNode(definition, "approve", nil, 9, testNow); err != nil {
		t.Fatal(err)
	}
	if run.Status != WorkflowRunSucceeded || run.CompletedAt == nil {
		t.Fatalf("expected successful run, got %+v", run)
	}
	if err := run.Cancel(10, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal run must not transition, got %v", err)
	}
}

func TestDeliverySliceRequiresPinnedPrototypeAndStrictReview(t *testing.T) {
	blueprint := testRevision(t, "blueprint", "bp-v1", 1, `{}`).Ref("PAGE-1")
	slice, err := NewDeliverySlice("slice-1", "project-1", "PAGE-1", "Orders", blueprint, nil, nil, []string{"PAGE-1"}, true, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := slice.MarkReady(1, testNow); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected required prototype guard, got %v", err)
	}
	prototype := testRevision(t, "prototype", "prototype-v1", 1, `{}`).Ref("desktop")
	if err := slice.BindPrototype(prototype, 1, testNow); err != nil {
		t.Fatal(err)
	}
	if err := slice.MarkReady(2, testNow); err != nil {
		t.Fatal(err)
	}
	if err := slice.Start(3, testNow); err != nil {
		t.Fatal(err)
	}
	if err := slice.Complete(4, testNow); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("slice must be reviewed before complete, got %v", err)
	}
	if err := slice.SubmitReview(4, testNow); err != nil {
		t.Fatal(err)
	}
	if err := slice.Complete(5, testNow); err != nil {
		t.Fatal(err)
	}
}
