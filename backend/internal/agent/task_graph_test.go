package agent

import (
	"errors"
	"testing"

	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	taskGraphProject  = "61000000-0000-4000-8000-000000000001"
	taskGraphSession  = "61000000-0000-4000-8000-000000000002"
	taskGraphContract = "61000000-0000-4000-8000-000000000003"
)

func TestBuildTaskGraphTopologicallySplitsMustObligations(t *testing.T) {
	graph, err := buildTaskGraph(
		taskGraphProject,
		taskGraphSession,
		repository.ExactReference{
			ID: taskGraphContract, ContentHash: "sha256:" + repeatHex("a"),
		},
		taskGraphContractFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if graph.SchemaVersion != TaskGraphSchemaVersion || graph.TotalCount != 2 ||
		graph.Tasks[0].Key != TaskKeyPrefix+"OBL-1" ||
		graph.Tasks[1].Key != TaskKeyPrefix+"OBL-2" ||
		!equalJSON(graph.Tasks[1].DependsOn, []string{TaskKeyPrefix + "OBL-1"}) ||
		!equalJSON(graph.Tasks[0].AcceptanceCriterionIDs, []string{"AC-1"}) ||
		!equalJSON(graph.Tasks[1].VerificationCommandIDs, []string{"test-ui"}) {
		t.Fatalf("unexpected graph: %#v", graph)
	}

	contract := taskGraphContractFixture()
	contract.Obligations[1].DependsOn = []string{"OBL-2"}
	if _, err := buildTaskGraphTasks(contract); !errors.Is(err, ErrTaskGraphBlocked) {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestApplyTaskGraphProgressRequiresAppliedDependencyChain(t *testing.T) {
	graph, err := buildTaskGraph(
		taskGraphProject,
		taskGraphSession,
		repository.ExactReference{
			ID: taskGraphContract, ContentHash: "sha256:" + repeatHex("a"),
		},
		taskGraphContractFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}

	initial := applyTaskGraphProgress(graph, nil)
	if initial.State != TaskGraphReady || initial.NextTaskKey != TaskKeyPrefix+"OBL-1" ||
		initial.Tasks[1].State != TaskGraphTaskBlocked {
		t.Fatalf("initial progress = %#v", initial)
	}

	running := applyTaskGraphProgress(graph, []TaskAttemptProgress{{
		TaskKey: TaskKeyPrefix + "OBL-1",
		Attempt: AgentAttempt{ID: taskGraphSession, State: AttemptRunning},
	}})
	if running.State != TaskGraphRunning || running.NextTaskKey != "" ||
		running.Tasks[0].State != TaskGraphTaskRunning {
		t.Fatalf("running progress = %#v", running)
	}

	afterFirstMerge := applyTaskGraphProgress(graph, []TaskAttemptProgress{{
		TaskKey: TaskKeyPrefix + "OBL-1",
		Attempt: AgentAttempt{ID: taskGraphSession, State: AttemptReviewReady},
		Applied: true,
	}})
	if afterFirstMerge.State != TaskGraphReady || afterFirstMerge.CompletedCount != 1 ||
		afterFirstMerge.NextTaskKey != TaskKeyPrefix+"OBL-2" {
		t.Fatalf("after first merge = %#v", afterFirstMerge)
	}

	failedSecond := applyTaskGraphProgress(graph, []TaskAttemptProgress{
		{
			TaskKey: TaskKeyPrefix + "OBL-2",
			Attempt: AgentAttempt{ID: taskGraphProject, State: AttemptFailed},
		},
		{
			TaskKey: TaskKeyPrefix + "OBL-1",
			Attempt: AgentAttempt{ID: taskGraphSession, State: AttemptReviewReady},
			Applied: true,
		},
	})
	if failedSecond.State != TaskGraphFailed ||
		failedSecond.NextTaskKey != TaskKeyPrefix+"OBL-2" {
		t.Fatalf("failed progress = %#v", failedSecond)
	}

	dependencyUndone := applyTaskGraphProgress(graph, []TaskAttemptProgress{
		{
			TaskKey: TaskKeyPrefix + "OBL-2",
			Attempt: AgentAttempt{ID: taskGraphProject, State: AttemptReviewReady},
			Applied: true,
		},
		{
			TaskKey: TaskKeyPrefix + "OBL-1",
			Attempt: AgentAttempt{ID: taskGraphSession, State: AttemptReviewReady},
		},
	})
	if dependencyUndone.CompletedCount != 0 || dependencyUndone.Tasks[1].State != TaskGraphTaskBlocked ||
		dependencyUndone.State != TaskGraphAwaitingReview {
		t.Fatalf("dependency undo progress = %#v", dependencyUndone)
	}
}

func TestPlanningTaskBindsOnlySelectedObligation(t *testing.T) {
	obligations, criteria, commands, dependencies, err := planningTask(
		taskGraphContractFixture(), TaskKeyPrefix+"OBL-2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(obligations, []string{"OBL-2"}) ||
		!equalJSON(criteria, []string{"AC-2"}) ||
		!equalJSON(commands, []string{"test-ui"}) ||
		!equalJSON(dependencies, []string{TaskKeyPrefix + "OBL-1"}) {
		t.Fatalf("selected task bindings=%v %v %v %v", obligations, criteria, commands, dependencies)
	}
}

func taskGraphContractFixture() constructor.ContractContent {
	return constructor.ContractContent{
		AcceptanceCriteria: []constructor.AcceptanceCriterion{
			{ID: "AC-2", Statement: "Render the complete interface"},
			{ID: "AC-1", Statement: "Establish the application foundation"},
		},
		Oracles: []constructor.Oracle{
			{ID: "oracle-core", AcceptanceCriterionIDs: []string{"AC-1"}, CommandID: "test-core"},
			{ID: "oracle-ui", AcceptanceCriterionIDs: []string{"AC-2"}, CommandID: "test-ui"},
		},
		Obligations: []constructor.Obligation{
			{
				ID: "OBL-2", Level: "must", Status: constructor.StatusReady,
				OracleIDs: []string{"oracle-ui"}, DependsOn: []string{"OBL-1"},
			},
			{
				ID: "OBL-1", Level: "must", Status: constructor.StatusReady,
				OracleIDs: []string{"oracle-core"},
			},
		},
	}
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
