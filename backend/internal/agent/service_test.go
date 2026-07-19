package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type agentAccessFake struct {
	err       error
	viewCalls int
	editCalls int
}

func (access *agentAccessFake) RequireProjectView(context.Context, string, string) error {
	access.viewCalls++
	return access.err
}

func (access *agentAccessFake) RequireProjectEdit(context.Context, string, string) error {
	access.editCalls++
	return access.err
}

type controlStoreFake struct {
	packs      map[string]ContextPack
	capsules   map[string]TaskCapsule
	attempts   map[string]AgentAttempt
	operations map[string]string
	events     map[string][]AttemptEvent
	now        time.Time
}

func newControlStoreFake(now time.Time) *controlStoreFake {
	return &controlStoreFake{
		packs: map[string]ContextPack{}, capsules: map[string]TaskCapsule{},
		attempts: map[string]AgentAttempt{}, operations: map[string]string{},
		events: map[string][]AttemptEvent{}, now: now,
	}
}

func (store *controlStoreFake) SavePlan(_ context.Context, pack ContextPack, capsule TaskCapsule) (TaskPlan, error) {
	replayed := false
	if existing, found := store.packs[pack.ID]; found {
		if !sameContextPackInput(existing, pack) {
			return TaskPlan{}, ErrAgentOperationReplay
		}
		replayed = true
	}
	if existing, found := store.capsules[capsule.ID]; found {
		if !sameTaskCapsuleInput(existing, capsule) {
			return TaskPlan{}, ErrAgentOperationReplay
		}
		replayed = true
	}
	store.packs[pack.ID], store.capsules[capsule.ID] = pack, capsule
	return TaskPlan{ContextPack: pack, TaskCapsule: capsule, Replayed: replayed}, nil
}

func (store *controlStoreFake) CreateAttempt(
	_ context.Context,
	operationID string,
	attempt AgentAttempt,
) (AgentAttempt, bool, error) {
	if id, found := store.operations[operationID]; found {
		existing := store.attempts[id]
		if !sameAttemptCreation(existing, attempt) {
			return AgentAttempt{}, false, ErrAgentOperationReplay
		}
		return existing, true, nil
	}
	store.operations[operationID] = attempt.ID
	store.attempts[attempt.ID] = attempt
	return attempt, false, nil
}

func (store *controlStoreFake) FindAttemptByOperation(
	_ context.Context,
	projectID, _ string,
	operationID string,
) (AgentAttempt, bool, error) {
	id, found := store.operations[operationID]
	if !found {
		return AgentAttempt{}, false, nil
	}
	attempt := store.attempts[id]
	if attempt.ProjectID != projectID {
		return AgentAttempt{}, false, nil
	}
	return attempt, true, nil
}

func (store *controlStoreFake) GetContextPack(_ context.Context, projectID, id string) (ContextPack, error) {
	value, found := store.packs[id]
	if !found || value.ProjectID != projectID {
		return ContextPack{}, ErrPlanNotFound
	}
	return value, nil
}

func (store *controlStoreFake) GetTaskCapsule(_ context.Context, projectID, id string) (TaskCapsule, error) {
	value, found := store.capsules[id]
	if !found || value.ProjectID != projectID {
		return TaskCapsule{}, ErrPlanNotFound
	}
	return value, nil
}

func (store *controlStoreFake) GetAttempt(_ context.Context, projectID, id string) (AgentAttempt, error) {
	value, found := store.attempts[id]
	if !found || value.ProjectID != projectID {
		return AgentAttempt{}, ErrAttemptNotFound
	}
	return value, nil
}

func (store *controlStoreFake) ResolveAttemptProject(_ context.Context, id string) (string, error) {
	value, found := store.attempts[id]
	if !found {
		return "", ErrAttemptNotFound
	}
	return value.ProjectID, nil
}

func (store *controlStoreFake) ListAttempts(
	_ context.Context,
	projectID, sessionID string,
	_ int,
) ([]AgentAttempt, error) {
	values := []AgentAttempt{}
	for _, value := range store.attempts {
		if value.ProjectID == projectID && value.SandboxSessionID == sessionID {
			values = append(values, value)
		}
	}
	return values, nil
}

func (store *controlStoreFake) ListEvents(
	_ context.Context,
	projectID, attemptID string,
	after uint64,
	_ int,
) ([]AttemptEvent, error) {
	attempt, err := store.GetAttempt(context.Background(), projectID, attemptID)
	if err != nil || attempt.ID == "" {
		return nil, err
	}
	values := []AttemptEvent{}
	for _, event := range store.events[attemptID] {
		if event.Sequence > after {
			values = append(values, event)
		}
	}
	return values, nil
}

func (store *controlStoreFake) Advance(
	_ context.Context,
	attemptID string,
	input AdvanceAttemptInput,
) (AgentAttempt, error) {
	current, found := store.attempts[attemptID]
	if !found {
		return AgentAttempt{}, ErrAttemptNotFound
	}
	store.now = store.now.Add(time.Second)
	next, event, err := current.Advance(input, store.now)
	if err != nil {
		return AgentAttempt{}, err
	}
	store.attempts[attemptID] = next
	store.events[attemptID] = append(store.events[attemptID], event)
	return next, nil
}

func (store *controlStoreFake) Cancel(
	_ context.Context,
	attemptID string,
	expectedVersion uint64,
	actorID, reason string,
) (AgentAttempt, error) {
	current, found := store.attempts[attemptID]
	if !found {
		return AgentAttempt{}, ErrAttemptNotFound
	}
	store.now = store.now.Add(time.Second)
	next, event, err := current.Cancel(expectedVersion, actorID, reason, store.now)
	if err != nil {
		return AgentAttempt{}, err
	}
	store.attempts[attemptID] = next
	store.events[attemptID] = append(store.events[attemptID], event)
	return next, nil
}

func (store *controlStoreFake) Claim(
	_ context.Context,
	attemptID string,
	expectedVersion uint64,
	actorID, workerID string,
	ttl time.Duration,
) (AgentAttempt, error) {
	current, found := store.attempts[attemptID]
	if !found {
		return AgentAttempt{}, ErrAttemptNotFound
	}
	store.now = store.now.Add(time.Second)
	next, event, err := current.Claim(expectedVersion, actorID, workerID, ttl, store.now)
	if err != nil {
		return AgentAttempt{}, err
	}
	store.attempts[attemptID] = next
	store.events[attemptID] = append(store.events[attemptID], event)
	return next, nil
}

func (store *controlStoreFake) Renew(
	_ context.Context,
	attemptID string,
	expectedVersion, expectedFence uint64,
	actorID, workerID string,
	ttl time.Duration,
) (AgentAttempt, error) {
	current, found := store.attempts[attemptID]
	if !found {
		return AgentAttempt{}, ErrAttemptNotFound
	}
	store.now = store.now.Add(time.Second)
	next, event, err := current.Renew(expectedVersion, expectedFence, actorID, workerID, ttl, store.now)
	if err != nil {
		return AgentAttempt{}, err
	}
	store.attempts[attemptID] = next
	store.events[attemptID] = append(store.events[attemptID], event)
	return next, nil
}

func (store *controlStoreFake) MarkStale(
	_ context.Context,
	attemptID string,
	expectedVersion uint64,
	actorID, reason string,
) (AgentAttempt, error) {
	current, found := store.attempts[attemptID]
	if !found {
		return AgentAttempt{}, ErrAttemptNotFound
	}
	store.now = store.now.Add(time.Second)
	next, event, err := current.MarkStale(expectedVersion, actorID, reason, store.now)
	if err != nil {
		return AgentAttempt{}, err
	}
	store.attempts[attemptID] = next
	store.events[attemptID] = append(store.events[attemptID], event)
	return next, nil
}

func TestControlServiceCreatesQueuesRecoversAndRetriesExactAttempt(t *testing.T) {
	fixture := newAgentFixture(t)
	source := &planningSourceFake{facts: planningFactsFromFixture(fixture)}
	planner, err := NewDeterministicPlanner(source, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := NewStaticExecutorProfiles(map[string]ExecutorIdentity{"codex-default": testExecutor()})
	if err != nil {
		t.Fatal(err)
	}
	store := newControlStoreFake(fixture.now.Add(10 * time.Second))
	access := &agentAccessFake{}
	service, err := NewControlService(
		store, planner, profiles, access,
		func() time.Time { return fixture.now.Add(2 * time.Second) },
	)
	if err != nil {
		t.Fatal(err)
	}
	input := CreateTaskAttemptInput{
		ProjectID: fixture.taskInput.ProjectID, SandboxSessionID: fixture.taskInput.SandboxSessionID,
		TaskKey:         fixture.taskInput.TaskKey,
		Instruction:     "Implement the exact conversation slice and preserve all protected paths.",
		ExecutorProfile: "codex-default", ActorID: fixture.actorID, OperationID: "agent-create-1",
	}
	result, err := service.CreateTaskAttempt(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Attempt.State != AttemptQueued || result.Attempt.Version != 3 ||
		result.Attempt.ID != agentOperationIDs(input.ProjectID, input.ActorID, input.OperationID).attempt ||
		len(store.events[result.Attempt.ID]) != 2 {
		t.Fatalf("unexpected queued result: %#v", result)
	}

	replayed, err := service.CreateTaskAttempt(context.Background(), input)
	if err != nil || !replayed.Replayed || replayed.Attempt.ID != result.Attempt.ID ||
		replayed.Attempt.State != AttemptQueued {
		t.Fatalf("exact create recovery: result=%#v err=%v", replayed, err)
	}
	changed := input
	changed.Instruction = "Widen the write set."
	if _, err := service.CreateTaskAttempt(context.Background(), changed); !errors.Is(err, ErrAgentOperationReplay) {
		t.Fatalf("changed operation replay error = %v", err)
	}

	cancelled, err := service.CancelAttempt(
		context.Background(), result.Attempt.ID, fixture.actorID,
		result.Attempt.Version, "User cancelled before a Runner claimed the task.",
	)
	if err != nil || cancelled.State != AttemptCancelled {
		t.Fatalf("cancel: attempt=%#v err=%v", cancelled, err)
	}
	retryInput := RetryAttemptInput{
		ProjectID: input.ProjectID, ParentAttemptID: cancelled.ID,
		Reason:  "Retry after correcting the external task precondition.",
		ActorID: fixture.actorID, OperationID: "agent-retry-1",
	}
	retry, err := service.RetryAttempt(context.Background(), retryInput)
	if err != nil || retry.Attempt.State != AttemptQueued ||
		retry.Attempt.ParentAttemptID != cancelled.ID || retry.Attempt.Executor != cancelled.Executor {
		t.Fatalf("retry: result=%#v err=%v", retry, err)
	}
	changedRetry := retryInput
	changedRetry.Reason = "A different reason."
	if _, err := service.RetryAttempt(context.Background(), changedRetry); !errors.Is(err, ErrAgentOperationReplay) {
		t.Fatalf("changed retry replay error = %v", err)
	}

	// A process may commit the immutable retry row before advancing it to the
	// queue. Replaying the same operation must finish that exact transition
	// instead of returning a permanently pending Attempt.
	recoveryInput := retryInput
	recoveryInput.OperationID = "agent-retry-recovery"
	recoveryInput.Reason = "Recover the exact committed retry."
	recoveryIDs := agentOperationIDs(recoveryInput.ProjectID, recoveryInput.ActorID, recoveryInput.OperationID)
	pendingRetry, err := NewAttempt(NewAttemptInput{
		ID: recoveryIDs.attempt, CreatedBy: recoveryInput.ActorID,
		Executor: cancelled.Executor, Parent: &cancelled, RetryReason: recoveryInput.Reason,
	}, store.capsules[cancelled.TaskCapsule.ID], store.packs[cancelled.ContextPack.ID], fixture.now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	store.attempts[pendingRetry.ID] = pendingRetry
	store.operations[recoveryInput.OperationID] = pendingRetry.ID
	recoveredRetry, err := service.RetryAttempt(context.Background(), recoveryInput)
	if err != nil || !recoveredRetry.Replayed || recoveredRetry.Attempt.State != AttemptQueued ||
		recoveredRetry.Attempt.ID != pendingRetry.ID {
		t.Fatalf("pending retry recovery: result=%#v err=%v", recoveredRetry, err)
	}
	if access.editCalls < 6 {
		t.Fatalf("edit authorization calls = %d", access.editCalls)
	}
}

func TestControlServiceAuthorizesBeforePlanning(t *testing.T) {
	fixture := newAgentFixture(t)
	source := &planningSourceFake{facts: planningFactsFromFixture(fixture)}
	planner, err := NewDeterministicPlanner(source, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := NewStaticExecutorProfiles(map[string]ExecutorIdentity{"codex-default": testExecutor()})
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("denied")
	service, err := NewControlService(
		newControlStoreFake(fixture.now.Add(time.Minute)), planner, profiles,
		&agentAccessFake{err: denied}, func() time.Time { return fixture.now },
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateTaskAttempt(context.Background(), CreateTaskAttemptInput{
		ProjectID: fixture.taskInput.ProjectID, SandboxSessionID: fixture.taskInput.SandboxSessionID,
		TaskKey: fixture.taskInput.TaskKey, Instruction: "Implement exact task.",
		ExecutorProfile: "codex-default", ActorID: fixture.actorID, OperationID: "agent-denied-1",
	})
	if !errors.Is(err, denied) || source.calls != 0 {
		t.Fatalf("denied create: err=%v planning calls=%d", err, source.calls)
	}
}

type workerCapabilityFake struct {
	err     error
	actions []string
}

func (access *workerCapabilityFake) AuthorizeAgentWorker(
	_ context.Context,
	_ WorkerPrincipal,
	_ AgentAttempt,
	action string,
) error {
	access.actions = append(access.actions, action)
	return access.err
}

func TestWorkerServiceRequiresCapabilityAndInjectsPrincipal(t *testing.T) {
	fixture := newAgentFixture(t)
	pack, err := NewContextPack(fixture.contextInput, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := NewTaskCapsule(fixture.taskInput, pack, fixture.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttempt(NewAttemptInput{
		ID: uuid.NewString(), CreatedBy: fixture.actorID, Executor: testExecutor(),
	}, capsule, pack, fixture.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	store := newControlStoreFake(fixture.now.Add(10 * time.Second))
	store.packs[pack.ID], store.capsules[capsule.ID] = pack, capsule
	store.attempts[attempt.ID] = attempt
	attempt, err = store.Advance(context.Background(), attempt.ID, AdvanceAttemptInput{
		ExpectedVersion: 1, ActorID: fixture.actorID, Target: AttemptReady, Reason: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = store.Advance(context.Background(), attempt.ID, AdvanceAttemptInput{
		ExpectedVersion: attempt.Version, ActorID: fixture.actorID, Target: AttemptQueued, Reason: "queued",
	})
	if err != nil {
		t.Fatal(err)
	}

	denied := errors.New("capability denied")
	access := &workerCapabilityFake{err: denied}
	workers, err := NewWorkerService(store, access)
	if err != nil {
		t.Fatal(err)
	}
	principal := WorkerPrincipal{ActorID: fixture.actorID, WorkerID: "runner-a"}
	if _, err := workers.Claim(
		context.Background(), principal, attempt.ProjectID, attempt.ID, attempt.Version, time.Minute,
	); !errors.Is(err, denied) {
		t.Fatalf("denied worker claim error = %v", err)
	}
	if store.attempts[attempt.ID].State != AttemptQueued {
		t.Fatal("denied worker mutated the Attempt")
	}
	access.err = nil
	claimed, err := workers.Claim(
		context.Background(), principal, attempt.ProjectID, attempt.ID, attempt.Version, time.Minute,
	)
	if err != nil || claimed.State != AttemptClaimed || claimed.Lease.WorkerID != principal.WorkerID {
		t.Fatalf("authorized claim: attempt=%#v err=%v", claimed, err)
	}
	stalePrincipal := WorkerPrincipal{ActorID: fixture.actorID, WorkerID: "runner-b"}
	if _, err := workers.Advance(context.Background(), stalePrincipal, WorkerAdvanceInput{
		ProjectID: claimed.ProjectID, AttemptID: claimed.ID, ExpectedVersion: claimed.Version,
		ExpectedFenceEpoch: claimed.FenceEpoch, Target: AttemptRunning, Reason: "wrong worker",
	}); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("wrong worker advance error = %v", err)
	}
}

var _ ControlStore = (*controlStoreFake)(nil)
var _ WorkerStore = (*controlStoreFake)(nil)
