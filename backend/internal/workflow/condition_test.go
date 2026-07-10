package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/worksflow/builder/backend/internal/domain"
)

func TestDeclarativeConditionEvaluatorUsesScopeValuesAndNodeOutputs(t *testing.T) {
	run := RunRecord{
		ID: "run", ProjectID: "project", Status: RunRunning, Scope: json.RawMessage(`{"priority":"high","score":12}`),
		Context: NewRunContext(),
	}
	run.Context.Values["flags"] = json.RawMessage(`{"ready":true}`)
	run.Context.Nodes["brief"] = NodeMetadata{Output: json.RawMessage(`{"openQuestions":0}`)}
	evaluator := DeclarativeConditionEvaluator{}
	selected, err := evaluator.Evaluate(context.Background(), Execution{Run: run}, []domain.ConditionBranch{
		{Name: "ready", Expression: `{"all":[{"path":"/scope/priority","op":"eq","value":"high"},{"path":"/scope/score","op":"gte","value":10},{"path":"/values/flags/ready"},{"path":"/nodes/brief/openQuestions","op":"eq","value":0}]}`},
		{Name: "fallback", Default: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "ready" {
		t.Fatalf("selected branch = %q", selected)
	}
}

func TestDeclarativeConditionEvaluatorFallsBackAndRejectsCodeLikeRules(t *testing.T) {
	evaluator := DeclarativeConditionEvaluator{}
	execution := Execution{Run: RunRecord{Scope: json.RawMessage(`{}`), Context: NewRunContext()}}
	selected, err := evaluator.Evaluate(context.Background(), execution, []domain.ConditionBranch{
		{Name: "matched", Expression: `{"path":"/scope/missing","op":"exists"}`},
		{Name: "fallback", Default: true},
	})
	if err != nil || selected != "fallback" {
		t.Fatalf("fallback = %q, error = %v", selected, err)
	}
	_, err = evaluator.Evaluate(context.Background(), execution, []domain.ConditionBranch{
		{Name: "unsafe", Expression: `{"eval":"process.exit()"}`},
		{Name: "fallback", Default: true},
	})
	if err == nil {
		t.Fatal("unknown/code-like rule was accepted")
	}
}
