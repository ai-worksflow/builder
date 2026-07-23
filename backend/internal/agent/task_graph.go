package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/repository"
)

const (
	TaskGraphSchemaVersion = "agent-task-graph/v1"
	TaskKeyPrefix          = "obligation/"
	MaximumTaskGraphTasks  = 100
)

var ErrTaskGraphBlocked = errors.New("Agent task graph is blocked")

type TaskGraphTaskState string

const (
	TaskGraphTaskPending     TaskGraphTaskState = "pending"
	TaskGraphTaskBlocked     TaskGraphTaskState = "blocked"
	TaskGraphTaskRunning     TaskGraphTaskState = "running"
	TaskGraphTaskReviewReady TaskGraphTaskState = "review_ready"
	TaskGraphTaskFailed      TaskGraphTaskState = "failed"
	TaskGraphTaskCompleted   TaskGraphTaskState = "completed"
)

type TaskGraphState string

const (
	TaskGraphReady          TaskGraphState = "ready"
	TaskGraphRunning        TaskGraphState = "running"
	TaskGraphAwaitingReview TaskGraphState = "awaiting_review"
	TaskGraphFailed         TaskGraphState = "failed"
	TaskGraphCompleted      TaskGraphState = "completed"
	TaskGraphBlocked        TaskGraphState = "blocked"
)

type TaskGraphTask struct {
	Key                    string             `json:"key"`
	Title                  string             `json:"title"`
	ObligationIDs          []string           `json:"obligationIds"`
	AcceptanceCriterionIDs []string           `json:"acceptanceCriterionIds"`
	VerificationCommandIDs []string           `json:"verificationCommandIds"`
	DependsOn              []string           `json:"dependsOn"`
	State                  TaskGraphTaskState `json:"state"`
	LatestAttemptID        string             `json:"latestAttemptId,omitempty"`
	LatestAttemptState     AttemptState       `json:"latestAttemptState,omitempty"`
}

type TaskGraph struct {
	SchemaVersion    string                    `json:"schemaVersion"`
	ProjectID        string                    `json:"projectId"`
	SandboxSessionID string                    `json:"sandboxSessionId"`
	BuildContract    repository.ExactReference `json:"buildContract"`
	State            TaskGraphState            `json:"state"`
	Tasks            []TaskGraphTask           `json:"tasks"`
	NextTaskKey      string                    `json:"nextTaskKey,omitempty"`
	CompletedCount   int                       `json:"completedCount"`
	TotalCount       int                       `json:"totalCount"`
}

type TaskAttemptProgress struct {
	Attempt AgentAttempt
	TaskKey string
	Applied bool
}

type TaskGraphPlanningSource interface {
	LoadTaskGraph(context.Context, string, string) (TaskGraph, error)
}

type TaskGraphProgressStore interface {
	ListTaskAttemptProgress(context.Context, string, string) ([]TaskAttemptProgress, error)
}

func buildTaskGraph(
	projectID, sessionID string,
	buildContract repository.ExactReference,
	contract constructor.ContractContent,
) (TaskGraph, error) {
	if !validUUIDs(projectID, sessionID, buildContract.ID) ||
		!exactHashPattern.MatchString(buildContract.ContentHash) {
		return TaskGraph{}, fmt.Errorf("%w: task graph identity", ErrTaskGraphBlocked)
	}
	tasks, err := buildTaskGraphTasks(contract)
	if err != nil {
		return TaskGraph{}, err
	}
	return TaskGraph{
		SchemaVersion: TaskGraphSchemaVersion, ProjectID: projectID,
		SandboxSessionID: sessionID, BuildContract: buildContract,
		State: TaskGraphReady, Tasks: tasks, TotalCount: len(tasks),
	}, nil
}

func buildTaskGraphTasks(contract constructor.ContractContent) ([]TaskGraphTask, error) {
	criteria := make(map[string]constructor.AcceptanceCriterion, len(contract.AcceptanceCriteria))
	for _, criterion := range contract.AcceptanceCriteria {
		criteria[criterion.ID] = criterion
	}
	oracles := make(map[string]constructor.Oracle, len(contract.Oracles))
	for _, oracle := range contract.Oracles {
		oracles[oracle.ID] = oracle
	}
	must := make(map[string]constructor.Obligation)
	for _, obligation := range contract.Obligations {
		if obligation.Level != "must" {
			continue
		}
		if obligation.Status != constructor.StatusReady || len(obligation.OracleIDs) == 0 {
			return nil, fmt.Errorf("%w: Must obligation %s is not executable", ErrTaskGraphBlocked, obligation.ID)
		}
		must[obligation.ID] = obligation
	}
	if len(must) == 0 || len(must) > MaximumTaskGraphTasks {
		return nil, fmt.Errorf("%w: task count must be between 1 and %d", ErrTaskGraphBlocked, MaximumTaskGraphTasks)
	}

	tasksByObligation := make(map[string]TaskGraphTask, len(must))
	for obligationID, obligation := range must {
		acceptanceSet := map[string]bool{}
		commandSet := map[string]bool{}
		for _, oracleID := range obligation.OracleIDs {
			oracle, exists := oracles[oracleID]
			if !exists || len(oracle.AcceptanceCriterionIDs) == 0 {
				return nil, fmt.Errorf("%w: Oracle %s is missing", ErrTaskGraphBlocked, oracleID)
			}
			commandID := oracle.CommandID
			if commandID == "" {
				commandID = "oracle:" + oracle.ID
			}
			commandSet[commandID] = true
			for _, criterionID := range oracle.AcceptanceCriterionIDs {
				if _, exists := criteria[criterionID]; !exists {
					return nil, fmt.Errorf("%w: acceptance criterion %s is missing", ErrTaskGraphBlocked, criterionID)
				}
				acceptanceSet[criterionID] = true
			}
		}
		dependencies := make([]string, 0, len(obligation.DependsOn))
		for _, dependencyID := range obligation.DependsOn {
			if _, exists := must[dependencyID]; !exists || dependencyID == obligationID {
				return nil, fmt.Errorf("%w: obligation %s has an invalid dependency %s", ErrTaskGraphBlocked, obligationID, dependencyID)
			}
			dependencies = append(dependencies, TaskKeyPrefix+dependencyID)
		}
		sort.Strings(dependencies)
		acceptanceIDs := sortedSet(acceptanceSet)
		title := obligationID
		if len(acceptanceIDs) > 0 {
			title = strings.TrimSpace(criteria[acceptanceIDs[0]].Statement)
			if title == "" {
				title = obligationID
			}
		}
		titleRunes := []rune(title)
		if len(titleRunes) > 240 {
			title = string(titleRunes[:240])
		}
		tasksByObligation[obligationID] = TaskGraphTask{
			Key: TaskKeyPrefix + obligationID, Title: title,
			ObligationIDs: []string{obligationID}, AcceptanceCriterionIDs: acceptanceIDs,
			VerificationCommandIDs: sortedSet(commandSet), DependsOn: dependencies,
			State: TaskGraphTaskPending,
		}
	}

	ordered, err := topologicalTaskOrder(tasksByObligation)
	if err != nil {
		return nil, err
	}
	return ordered, nil
}

func topologicalTaskOrder(tasks map[string]TaskGraphTask) ([]TaskGraphTask, error) {
	indegree := make(map[string]int, len(tasks))
	children := make(map[string][]string, len(tasks))
	byKey := make(map[string]TaskGraphTask, len(tasks))
	for obligationID, task := range tasks {
		byKey[task.Key] = task
		indegree[task.Key] = len(task.DependsOn)
		for _, dependency := range task.DependsOn {
			children[dependency] = append(children[dependency], TaskKeyPrefix+obligationID)
		}
	}
	ready := []string{}
	for key, count := range indegree {
		if count == 0 {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)
	ordered := make([]TaskGraphTask, 0, len(tasks))
	for len(ready) > 0 {
		key := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byKey[key])
		for _, child := range children[key] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(tasks) {
		return nil, fmt.Errorf("%w: obligation dependency cycle", ErrTaskGraphBlocked)
	}
	return ordered, nil
}

func applyTaskGraphProgress(graph TaskGraph, progress []TaskAttemptProgress) TaskGraph {
	byTask := make(map[string][]TaskAttemptProgress)
	for _, item := range progress {
		byTask[item.TaskKey] = append(byTask[item.TaskKey], item)
	}
	for key := range byTask {
		sort.SliceStable(byTask[key], func(left, right int) bool {
			leftAttempt, rightAttempt := byTask[key][left].Attempt, byTask[key][right].Attempt
			if leftAttempt.UpdatedAt.Equal(rightAttempt.UpdatedAt) {
				return leftAttempt.ID > rightAttempt.ID
			}
			return leftAttempt.UpdatedAt.After(rightAttempt.UpdatedAt)
		})
	}
	completed := map[string]bool{}
	active := false
	reviewReady := false
	failed := false
	for index := range graph.Tasks {
		task := &graph.Tasks[index]
		attempts := byTask[task.Key]
		dependenciesComplete := true
		for _, dependency := range task.DependsOn {
			if !completed[dependency] {
				dependenciesComplete = false
				break
			}
		}
		applied := false
		for _, item := range attempts {
			applied = applied || item.Applied
		}
		if applied && dependenciesComplete {
			task.State = TaskGraphTaskCompleted
			completed[task.Key] = true
			continue
		}
		if !dependenciesComplete {
			task.State = TaskGraphTaskBlocked
			continue
		}
		if len(attempts) == 0 {
			task.State = TaskGraphTaskPending
			continue
		}
		latest := attempts[0].Attempt
		task.LatestAttemptID, task.LatestAttemptState = latest.ID, latest.State
		switch {
		case latest.State == AttemptReviewReady:
			task.State, reviewReady = TaskGraphTaskReviewReady, true
		case finalState(latest.State):
			task.State, failed = TaskGraphTaskFailed, true
		default:
			task.State, active = TaskGraphTaskRunning, true
		}
	}
	graph.CompletedCount = len(completed)
	switch {
	case graph.CompletedCount == graph.TotalCount:
		graph.State = TaskGraphCompleted
	case failed:
		graph.State = TaskGraphFailed
	case reviewReady:
		graph.State = TaskGraphAwaitingReview
	case active:
		graph.State = TaskGraphRunning
	default:
		graph.State = TaskGraphReady
	}
	if graph.State == TaskGraphFailed {
		for _, task := range graph.Tasks {
			if task.State == TaskGraphTaskFailed {
				graph.NextTaskKey = task.Key
				break
			}
		}
	}
	if graph.State == TaskGraphReady {
		for _, task := range graph.Tasks {
			if task.State == TaskGraphTaskPending {
				graph.NextTaskKey = task.Key
				break
			}
		}
		if graph.NextTaskKey == "" {
			graph.State = TaskGraphBlocked
		}
	}
	return graph
}
