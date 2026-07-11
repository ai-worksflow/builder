package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
)

func TestDeclarativeConditionEvaluatorV1UsesTypedInputAndCurrentSlice(t *testing.T) {
	t.Parallel()
	sliceID := uuid.NewString()
	execution := typedConditionExecution(t, sliceID, json.RawMessage(`{"decision":"checkout"}`))
	execution.Run.Context.Slices[sliceID] = SliceContext{
		ID: sliceID, Key: "checkout", Title: "Checkout", FanOutNodeID: "pages",
	}
	selected, err := (DeclarativeConditionEvaluatorV1{}).Evaluate(context.Background(), execution, []domain.ConditionBranch{
		{
			Name: "matched",
			Expression: `{"all":[
                {"path":"/inputs/bindings/0/toPort","op":"eq","value":"request"},
				{"path":"/inputs/ports/request/0/decision","op":"eq","value":"checkout"},
				{"path":"/inputs/edges/incoming/0/decision","op":"eq","value":"checkout"},
                {"path":"/slice/id","op":"eq","value":"` + sliceID + `"},
                {"path":"/slice/key","op":"eq","value":"checkout"}
            ]}`,
		},
		{Name: "fallback", Default: true},
	})
	if err != nil || selected != "matched" {
		t.Fatalf("typed slice condition selected %q: %v", selected, err)
	}
}

func TestDeclarativeConditionEvaluatorV1IsIndependentOfConcurrentGlobalState(t *testing.T) {
	t.Parallel()
	const workers = 32
	template := typedConditionExecution(t, "", json.RawMessage(`{"approved":true}`))
	results := make(chan string, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			execution := template
			execution.Run.Context = NewRunContext()
			// These snapshots deliberately disagree. The v1 evaluator must never
			// observe them because neither node is present in its typed input.
			execution.Run.Context.Values["global"] = json.RawMessage(`{"approved":false}`)
			execution.Run.Context.Nodes["unrelated"] = NodeMetadata{Output: json.RawMessage(
				map[bool]string{true: `{"approved":true}`, false: `{"approved":false}`}[index%2 == 0],
			)}
			selected, err := (DeclarativeConditionEvaluatorV1{}).Evaluate(context.Background(), execution, []domain.ConditionBranch{
				{Name: "yes", Expression: `{"path":"/inputs/ports/request/0/approved","op":"eq","value":true}`},
				{Name: "no", Default: true},
			})
			results <- selected
			errors <- err
		}(index)
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("typed condition failed under concurrent snapshots: %v", err)
		}
	}
	for result := range results {
		if result != "yes" {
			t.Fatalf("unrelated global state changed the typed branch: %q", result)
		}
	}
}

func TestCurrentConditionAuthoringRejectsGlobalContextRoots(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"nodes", "values", "slices"} {
		definition := definitionWithConditionExpression(t, `{"path":"/`+root+`/anything","op":"exists"}`)
		err := PlatformWorkflowCapabilities(true, true).ValidateDefinition(definition)
		if err == nil || !strings.Contains(err.Error(), "forbidden") {
			t.Fatalf("current authoring accepted /%s global context: %v", root, err)
		}
	}
	allowed := definitionWithConditionExpression(t, `{"path":"/inputs/ports/default/0/ready","op":"exists"}`)
	if err := PlatformWorkflowCapabilities(true, true).ValidateDefinition(allowed); err != nil {
		t.Fatalf("current authoring rejected an immutable typed input condition: %v", err)
	}
}

func typedConditionExecution(t *testing.T, sliceID string, value json.RawMessage) Execution {
	t.Helper()
	runID := uuid.NewString()
	inputs, err := domain.NewNodeInputEnvelope([]domain.NodeInputBinding{{
		EdgeID: "incoming", FromPort: "default", ToPort: "request",
		Source: domain.NodeOutputReference{
			RunID: runID, NodeKey: "source", DefinitionNodeID: "source", SliceID: sliceID,
		},
		Output: value, Value: value,
	}})
	if err != nil {
		t.Fatal(err)
	}
	profile := CurrentWorkflowExecutionProfileRef()
	run := RunRecord{
		ID: runID, ProjectID: uuid.NewString(), DefinitionVersionID: uuid.NewString(),
		Definition: domain.WorkflowDefinitionRef{
			ID: uuid.NewString(), Version: 1, Hash: strings.Repeat("a", 64), ExecutionProfile: profile,
		},
		ExecutionProfile: profile, Scope: json.RawMessage(`{"mode":"test"}`),
		Context: NewRunContext(), Nodes: map[string]*NodeRecord{},
	}
	node := NodeRecord{
		ID: uuid.NewString(), RunID: runID, Key: "condition", DefinitionNodeID: "condition",
		Type: domain.NodeCondition, SliceID: sliceID,
	}
	return Execution{Run: run, Node: node, Inputs: inputs}
}

func definitionWithConditionExpression(t *testing.T, expression string) domain.WorkflowDefinition {
	t.Helper()
	base := governedApplicationDefinition(t, uuid.NewString(), 1, uuid.NewString(), time.Now().UTC())
	source, _ := base.FindNode("source")
	condition := domain.NodeDefinition{
		ID: "brief-choice", Name: "Brief choice", Type: domain.NodeCondition,
		InputSchema: source.OutputSchema,
		OutputPorts: map[string]domain.PortDefinition{
			"yes": {Schema: source.OutputSchema}, "no": {Schema: source.OutputSchema},
		},
		Condition: &domain.ConditionNodeConfig{Branches: []domain.ConditionBranch{
			{Name: "yes", Expression: expression}, {Name: "no", Default: true},
		}},
	}
	nodes := append(append([]domain.NodeDefinition(nil), base.Nodes...), condition)
	edges := make([]domain.WorkflowEdge, 0, len(base.Edges)+2)
	for _, edge := range base.Edges {
		if edge.From == "source" && edge.To == "project-brief-ai" {
			continue
		}
		edges = append(edges, edge)
	}
	edges = append(edges,
		domain.WorkflowEdge{ID: "brief-choice-input", From: "source", To: condition.ID},
		domain.WorkflowEdge{ID: "brief-choice-yes", From: condition.ID, FromPort: "yes", To: "project-brief-ai"},
		domain.WorkflowEdge{ID: "brief-choice-no", From: condition.ID, FromPort: "no", To: "project-brief-ai"},
	)
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		base.ID, base.Version+1, base.Name, base.SchemaVersion, nodes, edges,
		*base.InputContract, *base.OutputContract, CurrentWorkflowExecutionProfileRef(),
		base.CreatedBy, base.CreatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
