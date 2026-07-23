package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const userIntentMarker = "\n\nUser request (intent only; the exact constraints below remain authoritative): "

type ProjectAuthorizer interface {
	RequireProjectView(context.Context, string, string) error
	RequireProjectEdit(context.Context, string, string) error
}

type ControlStore interface {
	SavePlan(context.Context, ContextPack, TaskCapsule) (TaskPlan, error)
	CreateAttempt(context.Context, string, AgentAttempt) (AgentAttempt, bool, error)
	FindAttemptByOperation(context.Context, string, string, string) (AgentAttempt, bool, error)
	GetContextPack(context.Context, string, string) (ContextPack, error)
	GetTaskCapsule(context.Context, string, string) (TaskCapsule, error)
	GetAttempt(context.Context, string, string) (AgentAttempt, error)
	ResolveAttemptProject(context.Context, string) (string, error)
	ListAttempts(context.Context, string, string, int) ([]AgentAttempt, error)
	ListEvents(context.Context, string, string, uint64, int) ([]AttemptEvent, error)
	Advance(context.Context, string, AdvanceAttemptInput) (AgentAttempt, error)
	Cancel(context.Context, string, uint64, string, string) (AgentAttempt, error)
}

type ExecutorProfileResolver interface {
	ResolveExecutor(context.Context, string, string) (ExecutorIdentity, error)
}

// StaticExecutorProfiles is suitable for configuration-backed, pre-qualified
// profiles. The browser selects a stable profile ID, never raw model, prompt,
// image, schema, or toolchain hashes.
type StaticExecutorProfiles struct {
	profiles map[string]ExecutorIdentity
}

func NewStaticExecutorProfiles(values map[string]ExecutorIdentity) (*StaticExecutorProfiles, error) {
	if len(values) == 0 || len(values) > 64 {
		return nil, errors.New("one to 64 qualified executor profiles are required")
	}
	profiles := make(map[string]ExecutorIdentity, len(values))
	for key, value := range values {
		normalizedKey, err := normalizeStableValue(key, 80)
		if err != nil || normalizedKey != key {
			return nil, fmt.Errorf("invalid executor profile ID %q", key)
		}
		normalized, err := normalizeExecutor(value)
		if err != nil || normalized != value {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("executor profile %q is not canonical", key)
		}
		profiles[key] = normalized
	}
	return &StaticExecutorProfiles{profiles: profiles}, nil
}

func (profiles *StaticExecutorProfiles) ResolveExecutor(
	_ context.Context,
	_ string,
	profileID string,
) (ExecutorIdentity, error) {
	if profiles == nil {
		return ExecutorIdentity{}, fmt.Errorf("%w: executor profiles are unavailable", ErrPlanningBlocked)
	}
	value, exists := profiles.profiles[strings.TrimSpace(profileID)]
	if !exists || profileID != strings.TrimSpace(profileID) {
		return ExecutorIdentity{}, fmt.Errorf("%w: executor profile is not qualified", ErrPlanningBlocked)
	}
	return value, nil
}

type CreateTaskAttemptInput struct {
	ProjectID        string `json:"projectId"`
	SandboxSessionID string `json:"sandboxSessionId"`
	TaskKey          string `json:"taskKey"`
	Instruction      string `json:"instruction"`
	ExecutorProfile  string `json:"executorProfile"`
	ActorID          string `json:"-"`
	OperationID      string `json:"-"`
}

type RetryAttemptInput struct {
	ProjectID       string `json:"projectId"`
	ParentAttemptID string `json:"parentAttemptId"`
	Reason          string `json:"reason"`
	ActorID         string `json:"-"`
	OperationID     string `json:"-"`
}

type TaskAttemptResult struct {
	ContextPack ContextPack  `json:"contextPack"`
	TaskCapsule TaskCapsule  `json:"taskCapsule"`
	Attempt     AgentAttempt `json:"attempt"`
	Replayed    bool         `json:"replayed"`
}

type AdvanceTaskGraphInput struct {
	ProjectID        string `json:"projectId"`
	SandboxSessionID string `json:"sandboxSessionId"`
	Instruction      string `json:"instruction"`
	ExecutorProfile  string `json:"executorProfile"`
	ActorID          string `json:"-"`
	OperationID      string `json:"-"`
}

type TaskGraphAdvanceDisposition string

const (
	TaskGraphAdvanceStarted   TaskGraphAdvanceDisposition = "started"
	TaskGraphAdvanceWaiting   TaskGraphAdvanceDisposition = "waiting"
	TaskGraphAdvanceCompleted TaskGraphAdvanceDisposition = "completed"
	TaskGraphAdvanceBlocked   TaskGraphAdvanceDisposition = "blocked"
)

type TaskGraphAdvanceResult struct {
	Graph       TaskGraph                   `json:"graph"`
	Attempt     *TaskAttemptResult          `json:"attempt,omitempty"`
	Disposition TaskGraphAdvanceDisposition `json:"disposition"`
	Replayed    bool                        `json:"replayed"`
}

type ControlService struct {
	store     ControlStore
	planner   TaskPlanner
	executors ExecutorProfileResolver
	access    ProjectAuthorizer
	now       func() time.Time
}

func NewControlService(
	store ControlStore,
	planner TaskPlanner,
	executors ExecutorProfileResolver,
	access ProjectAuthorizer,
	now func() time.Time,
) (*ControlService, error) {
	if store == nil || planner == nil || executors == nil || access == nil || now == nil {
		return nil, errors.New("agent store, planner, executor profiles, authorizer, and clock are required")
	}
	return &ControlService{store: store, planner: planner, executors: executors, access: access, now: now}, nil
}

func (service *ControlService) CreateTaskAttempt(
	ctx context.Context,
	input CreateTaskAttemptInput,
) (TaskAttemptResult, error) {
	input, err := normalizeCreateTaskAttemptInput(input)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return TaskAttemptResult{}, fmt.Errorf("authorize AgentAttempt creation: %w", err)
	}
	executor, err := service.executors.ResolveExecutor(ctx, input.ProjectID, input.ExecutorProfile)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	if existing, found, findErr := service.store.FindAttemptByOperation(
		ctx, input.ProjectID, input.ActorID, input.OperationID,
	); findErr != nil {
		return TaskAttemptResult{}, findErr
	} else if found {
		return service.recoverCreate(ctx, input, executor, existing)
	}

	ids := agentOperationIDs(input.ProjectID, input.ActorID, input.OperationID)
	plan, err := service.planner.Plan(ctx, PlanTaskInput{
		ProjectID: input.ProjectID, SandboxSessionID: input.SandboxSessionID,
		TaskKey: input.TaskKey, Instruction: input.Instruction, ActorID: input.ActorID,
		ContextPackID: ids.contextPack, TaskCapsuleID: ids.taskCapsule,
	})
	if err != nil {
		return TaskAttemptResult{}, err
	}
	plan, err = service.store.SavePlan(ctx, plan.ContextPack, plan.TaskCapsule)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	now := service.now().UTC().Truncate(time.Microsecond)
	attempt, err := NewAttempt(NewAttemptInput{
		ID: ids.attempt, CreatedBy: input.ActorID, Executor: executor,
	}, plan.TaskCapsule, plan.ContextPack, now)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	attempt, replayedAttempt, err := service.store.CreateAttempt(ctx, input.OperationID, attempt)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	attempt, err = service.ensureQueued(ctx, attempt, input.ActorID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	return TaskAttemptResult{
		ContextPack: plan.ContextPack, TaskCapsule: plan.TaskCapsule, Attempt: attempt,
		Replayed: plan.Replayed || replayedAttempt,
	}, nil
}

func (service *ControlService) RetryAttempt(
	ctx context.Context,
	input RetryAttemptInput,
) (TaskAttemptResult, error) {
	input, err := normalizeRetryAttemptInput(input)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return TaskAttemptResult{}, fmt.Errorf("authorize AgentAttempt retry: %w", err)
	}
	if existing, found, findErr := service.store.FindAttemptByOperation(
		ctx, input.ProjectID, input.ActorID, input.OperationID,
	); findErr != nil {
		return TaskAttemptResult{}, findErr
	} else if found {
		ids := agentOperationIDs(input.ProjectID, input.ActorID, input.OperationID)
		if existing.ID != ids.attempt || existing.ProjectID != input.ProjectID ||
			existing.ParentAttemptID != input.ParentAttemptID || existing.RetryReason != input.Reason {
			return TaskAttemptResult{}, ErrAgentOperationReplay
		}
		existing, err = service.ensureQueued(ctx, existing, input.ActorID)
		if err != nil {
			return TaskAttemptResult{}, err
		}
		return service.loadResult(ctx, existing, true)
	}

	parent, err := service.store.GetAttempt(ctx, input.ProjectID, input.ParentAttemptID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	contextPack, err := service.store.GetContextPack(ctx, input.ProjectID, parent.ContextPack.ID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	capsule, err := service.store.GetTaskCapsule(ctx, input.ProjectID, parent.TaskCapsule.ID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	ids := agentOperationIDs(input.ProjectID, input.ActorID, input.OperationID)
	now := service.now().UTC().Truncate(time.Microsecond)
	retry, err := NewAttempt(NewAttemptInput{
		ID: ids.attempt, CreatedBy: input.ActorID, Executor: parent.Executor,
		Parent: &parent, RetryReason: input.Reason,
	}, capsule, contextPack, now)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	retry, replayed, err := service.store.CreateAttempt(ctx, input.OperationID, retry)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	retry, err = service.ensureQueued(ctx, retry, input.ActorID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	return TaskAttemptResult{
		ContextPack: contextPack, TaskCapsule: capsule, Attempt: retry, Replayed: replayed,
	}, nil
}

func (service *ControlService) GetAttempt(
	ctx context.Context,
	attemptID, actorID string,
) (TaskAttemptResult, error) {
	if !validUUIDs(attemptID, actorID) {
		return TaskAttemptResult{}, fmt.Errorf("%w: Attempt and actor IDs", ErrInvalidAttempt)
	}
	projectID, err := service.store.ResolveAttemptProject(ctx, attemptID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return TaskAttemptResult{}, fmt.Errorf("authorize AgentAttempt view: %w", err)
	}
	attempt, err := service.store.GetAttempt(ctx, projectID, attemptID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	return service.loadResult(ctx, attempt, false)
}

func (service *ControlService) ListAttempts(
	ctx context.Context,
	projectID, sessionID, actorID string,
	limit int,
) ([]AgentAttempt, error) {
	if !validUUIDs(projectID, sessionID, actorID) {
		return nil, fmt.Errorf("%w: project, Session, and actor IDs", ErrInvalidAttempt)
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return nil, fmt.Errorf("authorize AgentAttempt list: %w", err)
	}
	return service.store.ListAttempts(ctx, projectID, sessionID, limit)
}

func (service *ControlService) GetTaskGraph(
	ctx context.Context,
	projectID, sessionID, actorID string,
) (TaskGraph, error) {
	if !validUUIDs(projectID, sessionID, actorID) {
		return TaskGraph{}, fmt.Errorf("%w: project, Session, or actor identity", ErrTaskGraphBlocked)
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return TaskGraph{}, fmt.Errorf("authorize Agent task graph view: %w", err)
	}
	return service.loadTaskGraph(ctx, projectID, sessionID)
}

func (service *ControlService) AdvanceTaskGraph(
	ctx context.Context,
	input AdvanceTaskGraphInput,
) (TaskGraphAdvanceResult, error) {
	normalized, err := normalizeAdvanceTaskGraphInput(input)
	if err != nil {
		return TaskGraphAdvanceResult{}, err
	}
	input = normalized
	if err := service.access.RequireProjectEdit(ctx, input.ProjectID, input.ActorID); err != nil {
		return TaskGraphAdvanceResult{}, fmt.Errorf("authorize Agent task graph advance: %w", err)
	}
	executor, err := service.executors.ResolveExecutor(ctx, input.ProjectID, input.ExecutorProfile)
	if err != nil {
		return TaskGraphAdvanceResult{}, err
	}
	if existing, found, findErr := service.store.FindAttemptByOperation(
		ctx, input.ProjectID, input.ActorID, input.OperationID,
	); findErr != nil {
		return TaskGraphAdvanceResult{}, findErr
	} else if found {
		capsule, loadErr := service.store.GetTaskCapsule(ctx, input.ProjectID, existing.TaskCapsule.ID)
		if loadErr != nil {
			return TaskGraphAdvanceResult{}, loadErr
		}
		attempt, recoverErr := service.recoverCreate(ctx, CreateTaskAttemptInput{
			ProjectID: input.ProjectID, SandboxSessionID: input.SandboxSessionID,
			TaskKey: capsule.TaskKey, Instruction: input.Instruction,
			ExecutorProfile: input.ExecutorProfile, ActorID: input.ActorID,
			OperationID: input.OperationID,
		}, executor, existing)
		if recoverErr != nil {
			return TaskGraphAdvanceResult{}, recoverErr
		}
		graph, graphErr := service.loadTaskGraph(ctx, input.ProjectID, input.SandboxSessionID)
		if graphErr != nil {
			return TaskGraphAdvanceResult{}, graphErr
		}
		return TaskGraphAdvanceResult{
			Graph: graph, Attempt: &attempt,
			Disposition: TaskGraphAdvanceStarted, Replayed: true,
		}, nil
	}

	graph, err := service.loadTaskGraph(ctx, input.ProjectID, input.SandboxSessionID)
	if err != nil {
		return TaskGraphAdvanceResult{}, err
	}
	if graph.NextTaskKey == "" {
		return TaskGraphAdvanceResult{
			Graph: graph, Disposition: taskGraphAdvanceDisposition(graph.State),
		}, nil
	}
	attempt, err := service.CreateTaskAttempt(ctx, CreateTaskAttemptInput{
		ProjectID: input.ProjectID, SandboxSessionID: input.SandboxSessionID,
		TaskKey: graph.NextTaskKey, Instruction: input.Instruction,
		ExecutorProfile: input.ExecutorProfile, ActorID: input.ActorID,
		OperationID: input.OperationID,
	})
	if err != nil {
		return TaskGraphAdvanceResult{}, err
	}
	graph, err = service.loadTaskGraph(ctx, input.ProjectID, input.SandboxSessionID)
	if err != nil {
		return TaskGraphAdvanceResult{}, err
	}
	return TaskGraphAdvanceResult{
		Graph: graph, Attempt: &attempt,
		Disposition: TaskGraphAdvanceStarted, Replayed: attempt.Replayed,
	}, nil
}

func (service *ControlService) loadTaskGraph(
	ctx context.Context,
	projectID, sessionID string,
) (TaskGraph, error) {
	planner, ok := service.planner.(TaskGraphPlanner)
	if !ok {
		return TaskGraph{}, fmt.Errorf("%w: planner does not provide task graphs", ErrTaskGraphBlocked)
	}
	progressStore, ok := service.store.(TaskGraphProgressStore)
	if !ok {
		return TaskGraph{}, fmt.Errorf("%w: store does not provide task graph progress", ErrTaskGraphBlocked)
	}
	graph, err := planner.PlanTaskGraph(ctx, projectID, sessionID)
	if err != nil {
		return TaskGraph{}, err
	}
	progress, err := progressStore.ListTaskAttemptProgress(ctx, projectID, sessionID)
	if err != nil {
		return TaskGraph{}, err
	}
	return applyTaskGraphProgress(graph, progress), nil
}

func taskGraphAdvanceDisposition(state TaskGraphState) TaskGraphAdvanceDisposition {
	switch state {
	case TaskGraphCompleted:
		return TaskGraphAdvanceCompleted
	case TaskGraphBlocked:
		return TaskGraphAdvanceBlocked
	default:
		return TaskGraphAdvanceWaiting
	}
}

func (service *ControlService) ListEvents(
	ctx context.Context,
	attemptID, actorID string,
	afterSequence uint64,
	limit int,
) ([]AttemptEvent, error) {
	if !validUUIDs(attemptID, actorID) {
		return nil, fmt.Errorf("%w: Attempt and actor IDs", ErrInvalidAttempt)
	}
	projectID, err := service.store.ResolveAttemptProject(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if err := service.access.RequireProjectView(ctx, projectID, actorID); err != nil {
		return nil, fmt.Errorf("authorize AgentAttempt events: %w", err)
	}
	return service.store.ListEvents(ctx, projectID, attemptID, afterSequence, limit)
}

func (service *ControlService) CancelAttempt(
	ctx context.Context,
	attemptID, actorID string,
	expectedVersion uint64,
	reason string,
) (AgentAttempt, error) {
	if !validUUIDs(attemptID, actorID) || expectedVersion == 0 {
		return AgentAttempt{}, fmt.Errorf("%w: Attempt, actor, or version", ErrInvalidAttempt)
	}
	projectID, err := service.store.ResolveAttemptProject(ctx, attemptID)
	if err != nil {
		return AgentAttempt{}, err
	}
	if err := service.access.RequireProjectEdit(ctx, projectID, actorID); err != nil {
		return AgentAttempt{}, fmt.Errorf("authorize AgentAttempt cancellation: %w", err)
	}
	return service.store.Cancel(ctx, attemptID, expectedVersion, actorID, reason)
}

func (service *ControlService) recoverCreate(
	ctx context.Context,
	input CreateTaskAttemptInput,
	executor ExecutorIdentity,
	existing AgentAttempt,
) (TaskAttemptResult, error) {
	ids := agentOperationIDs(input.ProjectID, input.ActorID, input.OperationID)
	if existing.ID != ids.attempt || existing.ProjectID != input.ProjectID ||
		existing.SandboxSessionID != input.SandboxSessionID || existing.Executor != executor {
		return TaskAttemptResult{}, ErrAgentOperationReplay
	}
	capsule, err := service.store.GetTaskCapsule(ctx, input.ProjectID, existing.TaskCapsule.ID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	if capsule.TaskKey != input.TaskKey || !strings.HasSuffix(capsule.Objective, userIntentMarker+input.Instruction) {
		return TaskAttemptResult{}, ErrAgentOperationReplay
	}
	existing, err = service.ensureQueued(ctx, existing, input.ActorID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	return service.loadResult(ctx, existing, true)
}

func (service *ControlService) ensureQueued(
	ctx context.Context,
	attempt AgentAttempt,
	actorID string,
) (AgentAttempt, error) {
	for transitions := 0; transitions < 3; transitions++ {
		var target AttemptState
		var reason string
		switch attempt.State {
		case AttemptPending:
			target, reason = AttemptReady, "server validated the exact TaskCapsule and executor profile"
		case AttemptReady:
			target, reason = AttemptQueued, "server queued the exact AgentAttempt"
		default:
			return attempt, nil
		}
		next, err := service.store.Advance(ctx, attempt.ID, AdvanceAttemptInput{
			ExpectedVersion: attempt.Version, ExpectedFenceEpoch: attempt.FenceEpoch,
			ActorID: actorID, Target: target, Reason: reason,
		})
		if err == nil {
			attempt = next
			continue
		}
		if !errors.Is(err, ErrAttemptVersionConflict) && !errors.Is(err, ErrAttemptState) {
			return AgentAttempt{}, err
		}
		reloaded, reloadErr := service.store.GetAttempt(ctx, attempt.ProjectID, attempt.ID)
		if reloadErr != nil {
			return AgentAttempt{}, errors.Join(err, reloadErr)
		}
		attempt = reloaded
	}
	if attempt.State == AttemptPending || attempt.State == AttemptReady {
		return AgentAttempt{}, fmt.Errorf("%w: Attempt could not reach the queue", ErrAttemptVersionConflict)
	}
	return attempt, nil
}

func (service *ControlService) loadResult(
	ctx context.Context,
	attempt AgentAttempt,
	replayed bool,
) (TaskAttemptResult, error) {
	pack, err := service.store.GetContextPack(ctx, attempt.ProjectID, attempt.ContextPack.ID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	capsule, err := service.store.GetTaskCapsule(ctx, attempt.ProjectID, attempt.TaskCapsule.ID)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	return TaskAttemptResult{
		ContextPack: pack, TaskCapsule: capsule, Attempt: attempt, Replayed: replayed,
	}, nil
}

type operationIDs struct {
	contextPack string
	taskCapsule string
	attempt     string
}

func agentOperationIDs(projectID, actorID, operationID string) operationIDs {
	seed := "worksflow-agent/v1/" + projectID + "/" + actorID + "/" + operationID + "/"
	return operationIDs{
		contextPack: uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed+"context-pack")).String(),
		taskCapsule: uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed+"task-capsule")).String(),
		attempt:     uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed+"attempt")).String(),
	}
}

func normalizeCreateTaskAttemptInput(input CreateTaskAttemptInput) (CreateTaskAttemptInput, error) {
	if !validUUIDs(input.ProjectID, input.SandboxSessionID, input.ActorID) ||
		!agentOperationPattern.MatchString(input.OperationID) || input.OperationID != strings.TrimSpace(input.OperationID) {
		return CreateTaskAttemptInput{}, fmt.Errorf("%w: project, Session, actor, or operation identity", ErrInvalidAttempt)
	}
	taskKey, err := normalizeStableValue(input.TaskKey, 160)
	if err != nil || taskKey != input.TaskKey {
		return CreateTaskAttemptInput{}, fmt.Errorf("%w: task key", ErrInvalidTaskCapsule)
	}
	profile, err := normalizeStableValue(input.ExecutorProfile, 80)
	if err != nil || profile != input.ExecutorProfile {
		return CreateTaskAttemptInput{}, fmt.Errorf("%w: executor profile", ErrInvalidAttempt)
	}
	instruction := strings.TrimSpace(input.Instruction)
	if instruction == "" || len(instruction) > 2000 || strings.ContainsRune(instruction, '\x00') {
		return CreateTaskAttemptInput{}, fmt.Errorf("%w: instruction", ErrInvalidTaskCapsule)
	}
	input.Instruction = instruction
	return input, nil
}

func normalizeRetryAttemptInput(input RetryAttemptInput) (RetryAttemptInput, error) {
	if !validUUIDs(input.ProjectID, input.ParentAttemptID, input.ActorID) ||
		!agentOperationPattern.MatchString(input.OperationID) || input.OperationID != strings.TrimSpace(input.OperationID) {
		return RetryAttemptInput{}, fmt.Errorf("%w: project, parent, actor, or operation identity", ErrInvalidAttempt)
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || len(reason) > 1000 {
		return RetryAttemptInput{}, fmt.Errorf("%w: retry reason is required", ErrInvalidAttempt)
	}
	input.Reason = reason
	return input, nil
}

func normalizeAdvanceTaskGraphInput(input AdvanceTaskGraphInput) (AdvanceTaskGraphInput, error) {
	created, err := normalizeCreateTaskAttemptInput(CreateTaskAttemptInput{
		ProjectID: input.ProjectID, SandboxSessionID: input.SandboxSessionID,
		TaskKey: TaskKeyPrefix + "advance", Instruction: input.Instruction,
		ExecutorProfile: input.ExecutorProfile, ActorID: input.ActorID,
		OperationID: input.OperationID,
	})
	if err != nil {
		return AdvanceTaskGraphInput{}, err
	}
	input.Instruction = created.Instruction
	return input, nil
}

var _ ExecutorProfileResolver = (*StaticExecutorProfiles)(nil)
